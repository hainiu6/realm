package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const trafficTable = "realm_panel_traffic"
const trafficSpeedInterval = 3 * time.Second

type trafficSpeed struct {
	Upload    float64 `json:"upload_bytes_per_second"`
	Download  float64 `json:"download_bytes_per_second"`
	Available bool    `json:"available"`
	Warning   string  `json:"warning,omitempty"`
	SampledAt int64   `json:"sampled_at"`
}

type trafficUsage struct {
	Upload        uint64                  `json:"upload"`
	Download      uint64                  `json:"download"`
	RawUp         uint64                  `json:"raw_up"`
	RawDown       uint64                  `json:"raw_down"`
	Epoch         string                  `json:"epoch"`
	Days          map[string]dailyTraffic `json:"days,omitempty"`
	DailySampleAt int64                   `json:"daily_sample_at,omitempty"`
}

type trafficState struct {
	Signature string                  `json:"signature"`
	Rules     map[string]trafficUsage `json:"rules"`
}

type nftRunner interface {
	Run(input string, args ...string) ([]byte, error)
}

type systemNFTRunner struct{}

func (systemNFTRunner) Run(input string, args ...string) ([]byte, error) {
	path, found := resolveExecutable([]string{"/usr/sbin/nft", "/sbin/nft", "/usr/bin/nft"}, "nft")
	if !found {
		if _, err := exec.LookPath(path); err != nil {
			return nil, errors.New("未安装 nftables，请更新脚本后重新安装面板")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("nftables: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return output, nil
}

// All monitor methods are called under the panel's mu, including periodic polling.
type TrafficMonitor struct {
	path      string
	runner    nftRunner
	state     trafficState
	Available bool
	Warning   string
	speed     trafficSpeed
	speedRaw  map[string]rawTraffic
	speedAt   time.Time
	clock     func() time.Time
}

// Speed uses kernel counter deltas, not resettable saved totals. One shared
// sampler keeps the measurement interval independent of browser polls.
func (m *TrafficMonitor) SampleSpeed(c Config) {
	raw, exists, err := m.read()
	if err == nil && !exists {
		err = errors.New("流量计数表尚未就绪")
	}
	if err == nil {
		for _, rule := range c.Endpoints {
			if !rule.Disabled {
				if _, ok := raw[trafficID(rule.Listen)]; !ok {
					err = errors.New("规则流量计数器尚未就绪")
					break
				}
			}
		}
	}
	if err != nil {
		m.speed = trafficSpeed{Warning: "速度统计不可用：" + err.Error()}
		m.speedRaw, m.speedAt = nil, time.Time{}
		return
	}
	m.sampleSpeed(c, raw, time.Now())
}

func (m *TrafficMonitor) sampleSpeed(c Config, raw map[string]rawTraffic, now time.Time) {
	seconds := now.Sub(m.speedAt).Seconds()
	var up, down float64
	current := make(map[string]rawTraffic)
	for _, rule := range c.Endpoints {
		if rule.Disabled {
			continue
		}
		id := trafficID(rule.Listen)
		value, ok := raw[id]
		if !ok {
			continue
		}
		current[id] = value
		previous, exists := m.speedRaw[id]
		// New or replaced counters establish a baseline; never turn their
		// history or counter rollback into a speed spike.
		if m.speedAt.IsZero() || seconds <= 0 || !exists || previous.epoch != value.epoch {
			continue
		}
		if value.up >= previous.up {
			up += float64(value.up-previous.up) / seconds
		}
		if value.down >= previous.down {
			down += float64(value.down-previous.down) / seconds
		}
	}
	m.speed = trafficSpeed{Upload: up, Download: down, Available: true, SampledAt: now.UnixMilli()}
	m.speedRaw, m.speedAt = current, now
}

func (m *TrafficMonitor) Speed() trafficSpeed {
	speed := m.speed
	if speed.Available && time.Since(m.speedAt) > 3*trafficSpeedInterval {
		speed.Available, speed.Warning = false, "速度统计暂未更新"
	}
	if !m.Available {
		speed.Available, speed.Warning = false, m.Warning
	}
	if !speed.Available {
		speed.Upload, speed.Download = 0, 0
	}
	return speed
}

func newTrafficMonitor(path string, runner nftRunner) (*TrafficMonitor, error) {
	m := &TrafficMonitor{path: path, runner: runner, state: trafficState{Rules: make(map[string]trafficUsage)}}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &m.state); err != nil {
			return nil, fmt.Errorf("流量记录格式错误: %w", err)
		}
		if m.state.Rules == nil {
			m.state.Rules = make(map[string]trafficUsage)
		}
	}
	return m, nil
}

func trafficID(listen string) string {
	hash := sha256.Sum256([]byte(listen))
	return hex.EncodeToString(hash[:12])
}

type rawTraffic struct {
	up, down uint64
	epoch    string
}

func (m *TrafficMonitor) read() (map[string]rawTraffic, bool, error) {
	data, err := m.runner.Run("", "-j", "list", "table", "inet", trafficTable)
	if err != nil {
		if strings.Contains(string(data), "No such file or directory") || strings.Contains(string(data), "does not exist") {
			return nil, false, nil
		}
		return nil, false, err
	}
	var listing struct {
		Objects []struct {
			Counter *struct {
				Name  string `json:"name"`
				Bytes uint64 `json:"bytes"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		return nil, true, fmt.Errorf("无法解析 nftables 计数器: %w", err)
	}
	result := make(map[string]rawTraffic)
	owned := false
	for _, object := range listing.Objects {
		if object.Counter == nil {
			continue
		}
		name := object.Counter.Name
		if strings.HasPrefix(name, "generation_") {
			owned = true
			continue
		}
		parts := strings.Split(name, "_")
		if len(parts) != 3 || (parts[0] != "u" && parts[0] != "d") {
			continue
		}
		value := result[parts[1]]
		value.epoch = parts[2]
		if parts[0] == "u" {
			value.up = object.Counter.Bytes
		} else {
			value.down = object.Counter.Bytes
		}
		result[parts[1]] = value
	}
	if !owned {
		return nil, true, errors.New("同名 nftables 表不属于面板，无法启用流量统计")
	}
	return result, true, nil
}

func trafficSignature(c Config) string {
	var listens []string
	for _, rule := range c.Endpoints {
		if !rule.Disabled {
			listens = append(listens, rule.Listen)
		}
	}
	sort.Strings(listens)
	value := fmt.Sprintf("%t/%t/%s", c.Network.NoTCP, c.Network.UseUDP, strings.Join(listens, "\n"))
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

// This creates counter-only chains. No accept/drop/reject rules or other tables are modified.
func trafficScript(c Config, epoch string, replace bool) (string, error) {
	var script strings.Builder
	if replace {
		fmt.Fprintf(&script, "delete table inet %s\n", trafficTable)
	}
	fmt.Fprintf(&script, "add table inet %s\nadd counter inet %s generation_%s\n", trafficTable, trafficTable, epoch)
	fmt.Fprintf(&script, "add chain inet %s inbound { type filter hook input priority 10; policy accept; }\n", trafficTable)
	fmt.Fprintf(&script, "add chain inet %s outbound { type filter hook output priority 10; policy accept; }\n", trafficTable)
	for _, rule := range c.Endpoints {
		if rule.Disabled {
			continue
		}
		host, port, err := net.SplitHostPort(rule.Listen)
		if err != nil {
			return "", err
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", errors.New("监听端口格式无效")
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return "", fmt.Errorf("监听地址 %s 不是 IP，无法精确统计端口流量", host)
		}
		id := trafficID(rule.Listen)
		for _, direction := range []string{"u", "d"} {
			counter := direction + "_" + id + "_" + epoch
			fmt.Fprintf(&script, "add counter inet %s %s\n", trafficTable, counter)
			chain, addressField, portField := "inbound", "daddr", "dport"
			if direction == "d" {
				chain, addressField, portField = "outbound", "saddr", "sport"
			}
			addressMatch := ""
			switch {
			case ip.To4() != nil && ip.IsUnspecified():
				addressMatch = "meta nfproto ipv4 "
			case !ip.IsUnspecified() && ip.To4() != nil:
				addressMatch = fmt.Sprintf("ip %s %s ", addressField, ip.String())
			case !ip.IsUnspecified():
				addressMatch = fmt.Sprintf("ip6 %s %s ", addressField, ip.String())
			}
			// [::] listens also cover IPv4-mapped connections in Realm's default mode.
			for _, protocol := range []string{"tcp", "udp"} {
				if (protocol == "tcp" && c.Network.NoTCP) || (protocol == "udp" && !c.Network.UseUDP) {
					continue
				}
				fmt.Fprintf(&script, "add rule inet %s %s %smeta l4proto %s %s %s %d counter name %s\n", trafficTable, chain, addressMatch, protocol, protocol, portField, portNumber, counter)
			}
		}
	}
	return script.String(), nil
}

func (m *TrafficMonitor) accumulate(raw map[string]rawTraffic) {
	now := m.nowTime()
	for listen, usage := range m.state.Rules {
		pruneDaily(&usage, now)
		value, ok := raw[trafficID(listen)]
		if !ok {
			usage.DailySampleAt = now.UnixMilli()
			m.state.Rules[listen] = usage
			continue
		}
		var up, down uint64
		if usage.Epoch != value.epoch || value.up < usage.RawUp {
			up = value.up
		} else {
			up = value.up - usage.RawUp
		}
		if usage.Epoch != value.epoch || value.down < usage.RawDown {
			down = value.down
		} else {
			down = value.down - usage.RawDown
		}
		usage.Upload += up
		usage.Download += down
		recordDaily(&usage, up, down, now)
		usage.RawUp, usage.RawDown, usage.Epoch = value.up, value.down, value.epoch
		m.state.Rules[listen] = usage
	}
}

func (m *TrafficMonitor) persist() error {
	data, err := json.Marshal(m.state)
	if err != nil {
		return err
	}
	return atomicWrite(m.path, data, 0600)
}

func (m *TrafficMonitor) Refresh(c Config) error {
	err := m.refresh(c)
	m.Available = err == nil
	m.Warning = ""
	if err != nil {
		m.Warning = "流量统计不可用（显示上次保存值）：" + err.Error()
	}
	return err
}

func (m *TrafficMonitor) refresh(c Config) error {
	for _, rule := range c.Endpoints {
		if _, ok := m.state.Rules[rule.Listen]; !ok {
			m.state.Rules[rule.Listen] = trafficUsage{}
		}
	}
	raw, exists, err := m.read()
	if err != nil {
		return err
	}
	m.accumulate(raw)
	signature := trafficSignature(c)
	if !exists || m.state.Signature != signature {
		var bytes [8]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return err
		}
		epoch := hex.EncodeToString(bytes[:])
		script, err := trafficScript(c, epoch, exists)
		if err != nil {
			return err
		}
		// Preserve the old counters before atomically replacing our own table.
		if err := m.persist(); err != nil {
			return err
		}
		if _, err := m.runner.Run(script, "-f", "-"); err != nil {
			return err
		}
		m.state.Signature = signature
		// Read immediately so a packet arriving during setup is not lost.
		raw, _, err = m.read()
		if err != nil {
			return err
		}
		m.accumulate(raw)
	}
	return m.persist()
}

func (m *TrafficMonitor) Reset(c Config, listens []string) (int, error) {
	if err := m.Refresh(c); err != nil {
		return 0, err
	}
	targets := make(map[string]bool)
	for _, listen := range listens {
		targets[listen] = true
	}
	previous := make(map[string]trafficUsage)
	for _, rule := range c.Endpoints {
		if !targets[rule.Listen] {
			continue
		}
		usage := m.state.Rules[rule.Listen]
		previous[rule.Listen] = usage
		usage.Upload, usage.Download = 0, 0
		m.state.Rules[rule.Listen] = usage
	}
	if len(previous) == 0 {
		return 0, errRuleNotFound
	}
	if err := m.persist(); err != nil {
		for listen, usage := range previous {
			m.state.Rules[listen] = usage
		}
		return 0, err
	}
	return len(previous), nil
}

func (m *TrafficMonitor) Move(previous, current string) {
	if previous == current {
		return
	}
	usage := m.state.Rules[current]
	old := m.state.Rules[previous]
	usage.Upload += old.Upload
	usage.Download += old.Download
	if usage.Days == nil {
		usage.Days = make(map[string]dailyTraffic)
	}
	for date, day := range old.Days {
		currentDay := usage.Days[date]
		currentDay.Upload += day.Upload
		currentDay.Download += day.Download
		currentDay.Incomplete = currentDay.Incomplete || day.Incomplete
		usage.Days[date] = currentDay
	}
	m.state.Rules[current] = usage
	delete(m.state.Rules, previous)
	if err := m.persist(); err != nil {
		m.Available, m.Warning = false, "流量记录保存失败："+err.Error()
	}
}
