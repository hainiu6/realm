package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type fakeNFT struct {
	counters    map[string]uint64
	builds      int
	script      string
	unavailable bool
}

func (r *fakeNFT) Run(input string, args ...string) ([]byte, error) {
	if r.unavailable {
		return nil, errors.New("permission denied")
	}
	if input != "" {
		r.builds++
		r.script = input
		r.counters = map[string]uint64{}
		for _, match := range regexp.MustCompile(`add counter inet realm_panel_traffic (\w+)`).FindAllStringSubmatch(input, -1) {
			r.counters[match[1]] = 0
		}
		return nil, nil
	}
	if r.counters == nil {
		return []byte("No such file or directory"), errors.New("table missing")
	}
	var objects []any
	for name, bytes := range r.counters {
		objects = append(objects, map[string]any{"counter": map[string]any{"name": name, "bytes": bytes}})
	}
	return json.Marshal(map[string]any{"nftables": objects})
}
func (r *fakeNFT) set(listen string, up, down uint64) {
	for name := range r.counters {
		if strings.HasPrefix(name, "u_"+trafficID(listen)+"_") {
			r.counters[name] = up
		}
		if strings.HasPrefix(name, "d_"+trafficID(listen)+"_") {
			r.counters[name] = down
		}
	}
}

func TestTrafficPersistenceResetAndPause(t *testing.T) {
	runner := &fakeNFT{}
	path := filepath.Join(t.TempDir(), "traffic.json")
	m, err := newTrafficMonitor(path, runner)
	if err != nil {
		t.Fatal(err)
	}
	c := Config{}
	c.Network.UseUDP = true
	a, b := "[::]:10001", "127.0.0.1:10002"
	c.Endpoints = []ForwardingRule{{Listen: a}, {Listen: b}}
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	runner.set(a, 1000, 2000)
	runner.set(b, 3000, 4000)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if m.state.Rules[a].Upload != 1000 || m.state.Rules[b].Download != 4000 {
		t.Fatal("directional totals incorrect")
	}
	if count, err := m.Reset(c, []string{a, a}); err != nil || count != 1 {
		t.Fatalf("reset: %d %v", count, err)
	}
	runner.set(a, 1100, 2200)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if m.state.Rules[a].Upload != 100 || m.state.Rules[a].Download != 200 || m.state.Rules[b].Upload != 3000 {
		t.Fatal("reset recounts old traffic or resets another rule")
	}
	m, err = newTrafficMonitor(path, runner)
	if err != nil {
		t.Fatal(err)
	}
	beforeBuilds := runner.builds
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if runner.builds != beforeBuilds || m.state.Rules[a].Upload != 100 {
		t.Fatal("panel restart double counted or reset counters")
	}
	c.Endpoints[0].Disabled = true
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runner.script, "dport 10001") || m.state.Rules[a].Upload != 100 {
		t.Fatal("pause did not freeze traffic")
	}
	c.Endpoints[0].Disabled = false
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	runner.set(a, 10, 20)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if m.state.Rules[a].Upload != 110 || m.state.Rules[a].Download != 220 {
		t.Fatal("enable lost previous totals")
	}
	// A fresh kernel table after reboot must add new traffic without subtracting history.
	runner.counters = nil
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	runner.set(a, 5, 7)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if m.state.Rules[a].Upload != 115 || m.state.Rules[a].Download != 227 {
		t.Fatal("kernel restart lost totals")
	}
	runner.unavailable = true
	if _, err := m.Reset(c, []string{a}); err == nil {
		t.Fatal("unavailable stats falsely cleared")
	}
	if m.Available || m.Warning == "" || m.state.Rules[a].Upload != 115 {
		t.Fatal("stats failure erased history or hid warning")
	}
}

func TestTrafficResetSaveFailureRetainsTotals(t *testing.T) {
	runner := &fakeNFT{}
	m, err := newTrafficMonitor(filepath.Join(t.TempDir(), "traffic.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Endpoints: []ForwardingRule{{Listen: "[::]:10001"}}}
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	runner.set("[::]:10001", 100, 200)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	m.path = filepath.Join(t.TempDir(), "missing", "traffic.json")
	if _, err := m.Reset(c, []string{"[::]:10001"}); err == nil {
		t.Fatal("save failure hidden")
	}
	if m.state.Rules["[::]:10001"].Upload != 100 {
		t.Fatal("failed reset cleared memory totals")
	}
}

func TestEditingListenPreservesTraffic(t *testing.T) {
	isolatedConfig(t)
	runner := &fakeNFT{}
	var err error
	traffic, err = newTrafficMonitor(filepath.Join(t.TempDir(), "traffic.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	old, current := "[::]:10001", "[::]:11001"
	config.Endpoints = []ForwardingRule{{Listen: old, Remote: "203.0.113.1:443"}}
	if err := SaveConfig(); err != nil {
		t.Fatal(err)
	}
	runner.set(old, 1000, 2000)
	if err := updateForwardingRuleLocked(old, ForwardingRule{Listen: current, Remote: "203.0.113.2:443"}); err != nil {
		t.Fatal(err)
	}
	runner.set(current, 100, 200)
	if err := traffic.Refresh(config); err != nil {
		t.Fatal(err)
	}
	usage := traffic.state.Rules[current]
	if usage.Upload != 1100 || usage.Download != 2200 {
		t.Fatalf("listen edit lost or double counted history: %#v", usage)
	}
	if _, exists := traffic.state.Rules[old]; exists {
		t.Fatal("old listen still owns history")
	}
	if _, err := deleteForwardingRulesLocked([]string{current}); err != nil {
		t.Fatal(err)
	}
	if len(traffic.state.Rules) != 0 {
		t.Fatal("deleted rule retained history")
	}
}

func TestTrafficOnlyTouchesOwnedCounterTable(t *testing.T) {
	c := Config{}
	c.Network.UseUDP = true
	c.Endpoints = []ForwardingRule{{Listen: "0.0.0.0:10001"}, {Listen: "[2001:db8::1]:10002"}, {Listen: "[::]:10003", Disabled: true}}
	script, err := trafficScript(c, "abcd", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"delete table inet realm_panel_traffic", "meta nfproto ipv4", "ip6 daddr 2001:db8::1", "udp dport 10001", "tcp sport 10002"} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, bad := range []string{"flush ruleset", " drop", " reject", "dport 10003"} {
		if strings.Contains(script, bad) {
			t.Fatalf("unexpected firewall change: %s", bad)
		}
	}
	runner := &fakeNFT{counters: map[string]uint64{"external_counter": 123}}
	m, err := newTrafficMonitor(filepath.Join(t.TempDir(), "traffic.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(c); err == nil || runner.builds != 0 {
		t.Fatal("unowned table overwritten")
	}
	if _, err := trafficScript(Config{Endpoints: []ForwardingRule{{Listen: "example.com:10001"}}}, "abcd", false); err == nil {
		t.Fatal("hostname silently miscounted")
	}
	if _, err := os.Stat(m.path); !os.IsNotExist(err) {
		t.Fatal("failure created misleading stats")
	}
}
