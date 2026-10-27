package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

const panelSessionMaxAge = 30 * 24 * 60 * 60

// serverIPs reads this server's active interfaces rather than the browser's IP.
func serverIPs() ([]string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	ips := make([]string, 0)
	seen := make(map[string]bool)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || !ip.IsGlobalUnicast() {
				continue
			}
			value := ip.String()
			if !seen[value] {
				ips = append(ips, value)
				seen[value] = true
			}
		}
	}
	// Public addresses precede private ones; IPv4 precedes IPv6 within each group.
	sort.Slice(ips, func(i, j int) bool {
		left, right := net.ParseIP(ips[i]), net.ParseIP(ips[j])
		if left.IsPrivate() != right.IsPrivate() {
			return !left.IsPrivate()
		}
		if (left.To4() != nil) != (right.To4() != nil) {
			return left.To4() != nil
		}
		return ips[i] < ips[j]
	})
	return ips, nil
}

type ForwardingRule struct {
	Listen       string   `toml:"listen" json:"listen"`
	Remote       string   `toml:"remote" json:"remote"`
	ExtraRemotes []string `toml:"extra_remotes,omitempty" json:"extra_remotes,omitempty"`
	Balance      string   `toml:"balance,omitempty" json:"balance,omitempty"`
	Disabled     bool     `toml:"-" json:"disabled,omitempty"`
}

type Config struct {
	Network struct {
		NoTCP  bool `toml:"no_tcp"`
		UseUDP bool `toml:"use_udp"`
	} `toml:"network"`
	Endpoints []ForwardingRule `toml:"endpoints"`
}

type PanelConfig struct {
	Auth struct {
		Password string `toml:"password"`
	} `toml:"auth"`
	Server struct {
		Port          int    `toml:"port"`
		SessionSecret string `toml:"session_secret"`
	} `toml:"server"`
	HTTPS struct {
		Enabled  bool   `toml:"enabled"`
		CertFile string `toml:"cert_file"`
		KeyFile  string `toml:"key_file"`
	} `toml:"https"`
	Realm struct {
		ConfigPath string `toml:"config_path"`
	} `toml:"realm"`
}

var (
	mu                sync.Mutex
	config            Config
	panelConfig       PanelConfig
	httpsWarningOnce  sync.Once
	realmConfigPath   = "/root/.realm/config.toml"
	errRuleNotFound   = errors.New("未找到转发规则")
	errListenConflict = errors.New("端口已存在")
	traffic           *TrafficMonitor
)

func LoadConfig() error {
	data, err := os.ReadFile(realmConfigPath)
	if err != nil {
		return err
	}

	var loaded Config
	if _, err := toml.Decode(string(data), &loaded); err != nil {
		return err
	}
	if err := restorePausedRules(data, &loaded); err != nil {
		return err
	}
	config = loaded

	return nil
}

func LoadPanelConfig() error {
	data, err := os.ReadFile("./config.toml")
	if err != nil {
		return err
	}

	if _, err := toml.Decode(string(data), &panelConfig); err != nil {
		return err
	}

	return nil
}

// saveConfigLocked writes config to disk. Caller must hold mu.
func saveConfigLocked() error {
	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)

	// Realm 2.9.5 requires the top-level endpoints field even when there are
	// no forwarding rules. It must be written before [network] so TOML keeps
	// it at the document root.
	activeCount := 0
	for _, rule := range config.Endpoints {
		if !rule.Disabled {
			activeCount++
		}
	}
	if activeCount == 0 {
		buf.WriteString("endpoints = []\n\n")
	}
	for position, rule := range config.Endpoints {
		if !rule.Disabled {
			continue
		}
		data, err := json.Marshal(pausedRule{Position: position, Rule: rule})
		if err != nil {
			return err
		}
		buf.WriteString(pausedRulePrefix)
		buf.Write(data)
		buf.WriteByte('\n')
	}

	if err := encoder.Encode(map[string]any{"network": config.Network}); err != nil {
		return err
	}

	if activeCount > 0 {
		buf.WriteString("\n")
		for _, endpoint := range config.Endpoints {
			if endpoint.Disabled {
				continue
			}
			buf.WriteString("[[endpoints]]\n")
			if err := encoder.Encode(endpoint); err != nil {
				return err
			}
			buf.WriteString("\n")
		}
	}

	if err := atomicWrite(realmConfigPath, buf.Bytes(), 0644); err != nil {
		return err
	}
	if traffic != nil {
		traffic.Refresh(config)
	}
	return nil
}

func SaveConfig() error {
	mu.Lock()
	defer mu.Unlock()
	return saveConfigLocked()
}

func validateForwardingAddress(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return err
	}

	if host == "" {
		return fmt.Errorf("host 不能为空")
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil {
		return err
	}
	if portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("port 超出范围")
	}

	return nil
}

func validateForwardingRule(rule ForwardingRule) error {
	if err := validateForwardingAddress(rule.Listen); err != nil {
		return fmt.Errorf("listen 格式无效")
	}
	if err := validateForwardingAddress(rule.Remote); err != nil {
		return fmt.Errorf("remote 格式无效")
	}

	seenRemotes := map[string]struct{}{rule.Remote: {}}
	for _, remote := range rule.ExtraRemotes {
		if err := validateForwardingAddress(remote); err != nil {
			return fmt.Errorf("额外远端 %q 格式无效", remote)
		}
		if _, exists := seenRemotes[remote]; exists {
			return fmt.Errorf("远端地址不能重复: %s", remote)
		}
		seenRemotes[remote] = struct{}{}
	}

	backendCount := 1 + len(rule.ExtraRemotes)
	if backendCount > 1 && strings.TrimSpace(rule.Balance) == "" {
		return fmt.Errorf("多个远端必须设置负载均衡策略")
	}
	if strings.TrimSpace(rule.Balance) != "" {
		parts := strings.SplitN(rule.Balance, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("balance 格式无效，应为 strategy: weight1, weight2")
		}

		strategy := strings.TrimSpace(parts[0])
		if strategy != "roundrobin" && strategy != "iphash" {
			return fmt.Errorf("不支持的负载均衡策略: %s", strategy)
		}

		weightValues := strings.Split(parts[1], ",")
		if len(weightValues) != backendCount {
			return fmt.Errorf("权重数量必须与远端数量一致")
		}
		for _, value := range weightValues {
			weight, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || weight < 1 {
				return fmt.Errorf("权重必须是大于 0 的整数")
			}
		}
	}

	return nil
}

// updateForwardingRuleLocked replaces a rule and persists the configuration.
// Caller must hold mu.
func updateForwardingRuleLocked(originalListen string, input ForwardingRule) error {
	ruleIndex := -1
	for i, rule := range config.Endpoints {
		if rule.Listen == originalListen {
			ruleIndex = i
			break
		}
	}
	if ruleIndex == -1 {
		return errRuleNotFound
	}

	for i, rule := range config.Endpoints {
		if i != ruleIndex && rule.Listen == input.Listen {
			return errListenConflict
		}
	}

	originalRule := config.Endpoints[ruleIndex]
	input.Disabled = originalRule.Disabled
	config.Endpoints[ruleIndex] = input
	if err := saveConfigLocked(); err != nil {
		config.Endpoints[ruleIndex] = originalRule
		return err
	}
	if traffic != nil {
		traffic.Move(originalListen, input.Listen)
	}

	return nil
}

// deleteForwardingRulesLocked removes all matching rules and persists once.
// Caller must hold mu.
func deleteForwardingRulesLocked(listens []string) (int, error) {
	targets := make(map[string]struct{}, len(listens))
	for _, listen := range listens {
		if listen != "" {
			targets[listen] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return 0, errRuleNotFound
	}

	originalRules := config.Endpoints
	remaining := make([]ForwardingRule, 0, len(originalRules))
	deleted := 0
	for _, rule := range originalRules {
		if _, remove := targets[rule.Listen]; remove {
			deleted++
			continue
		}
		remaining = append(remaining, rule)
	}
	if deleted == 0 {
		return 0, errRuleNotFound
	}

	config.Endpoints = remaining
	if err := saveConfigLocked(); err != nil {
		config.Endpoints = originalRules
		return 0, err
	}
	if traffic != nil {
		for _, rule := range originalRules {
			if _, removed := targets[rule.Listen]; removed {
				delete(traffic.state.Rules, rule.Listen)
			}
		}
		if err := traffic.persist(); err != nil {
			traffic.Available, traffic.Warning = false, "流量记录保存失败："+err.Error()
		}
	}

	return deleted, nil
}

func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		user := session.Get("user")
		if user == nil {
			c.Redirect(http.StatusFound, "/login")
			c.Abort()
			return
		}
		c.Next()
	}
}

func sessionOptions(maxAge int) sessions.Options {
	return sessions.Options{
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   panelConfig.HTTPS.Enabled,
		SameSite: http.SameSiteStrictMode,
	}
}

func HTTPSRedirect() gin.HandlerFunc {
	return func(c *gin.Context) {
		if panelConfig.HTTPS.Enabled && c.Request.TLS == nil {
			target := "https://" + c.Request.Host + c.Request.URL.Path
			if c.Request.URL.RawQuery != "" {
				target += "?" + c.Request.URL.RawQuery
			}
			c.Redirect(http.StatusMovedPermanently, target)
			c.Abort()
			return
		}
		c.Next()
	}
}

func main() {
	if err := LoadPanelConfig(); err != nil {
		log.Fatalf("无法加载面板配置: %v", err)
	}

	if panelConfig.Realm.ConfigPath != "" {
		realmConfigPath = panelConfig.Realm.ConfigPath
	}

	if err := LoadConfig(); err != nil {
		log.Fatalf("无法加载 realm 配置: %v", err)
	}
	var err error
	traffic, err = newTrafficMonitor(realmConfigPath+".traffic.json", systemNFTRunner{})
	if err != nil {
		log.Fatalf("无法加载流量记录: %v", err)
	}
	if err := traffic.Refresh(config); err != nil {
		log.Printf("流量统计暂不可用: %v", err)
	}
	traffic.SampleSpeed(config)
	go func() {
		ticker := time.NewTicker(trafficSpeedInterval)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			traffic.SampleSpeed(config)
			mu.Unlock()
		}
	}()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			mu.Lock()
			if err := LoadConfig(); err == nil {
				traffic.Refresh(config)
			} else {
				traffic.Available, traffic.Warning = false, "读取规则失败，统计暂未更新："+err.Error()
			}
			mu.Unlock()
		}
	}()

	r := gin.Default()
	serviceManager := newServiceManager()

	sessionSecret := panelConfig.Server.SessionSecret
	if sessionSecret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("生成 session secret 失败: %v", err)
		}
		sessionSecret = hex.EncodeToString(b)
	}
	store := cookie.NewStore([]byte(sessionSecret))
	store.Options(sessionOptions(panelSessionMaxAge))
	r.Use(sessions.Sessions("realm_session", store))
	r.Use(HTTPSRedirect())

	r.Static("/static", "./static")

	r.GET("/login", func(c *gin.Context) {
		session := sessions.Default(c)
		if session.Get("user") != nil {
			c.Redirect(http.StatusFound, "/")
			return
		}
		c.File("./templates/login.html")
	})

	r.POST("/login", func(c *gin.Context) {
		var loginData struct {
			Password string `json:"password"`
		}

		if err := c.ShouldBindJSON(&loginData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求"})
			return
		}

		if loginData.Password == panelConfig.Auth.Password {
			session := sessions.Default(c)
			session.Set("user", true)
			session.Options(sessionOptions(panelSessionMaxAge))
			if err := session.Save(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Session保存失败"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"message": "登录成功"})
		} else {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "密码错误"})
		}
	})

	authorized := r.Group("/")
	authorized.Use(AuthRequired())
	{
		authorized.GET("/", func(c *gin.Context) {
			if !panelConfig.HTTPS.Enabled {
				httpsWarningOnce.Do(func() {
					c.Header("X-HTTPS-Warning", "当前未启用HTTPS，强烈建议启用HTTPS")
				})
			}
			c.File("./templates/index.html")
		})

		authorized.GET("/server_info", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			ips, err := serverIPs()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "无法读取本机 IP"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"ips": ips})
		})

		authorized.GET("/get_rules", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
			pageStr := c.Query("page")
			sizeStr := c.Query("size")
			page, err := strconv.Atoi(pageStr)
			if err != nil || page < 1 {
				page = 1
			}
			size, err := strconv.Atoi(sizeStr)
			if err != nil || size < 1 {
				size = 1000
			}

			mu.Lock()
			defer mu.Unlock()

			// 每次请求重新从磁盘加载，确保手动修改 config.toml 后能立即反映
			if err := LoadConfig(); err != nil {
				c.JSON(500, gin.H{"error": "读取配置文件失败"})
				return
			}

			totalCount := len(config.Endpoints)
			start := (page - 1) * size
			end := start + size
			if start >= totalCount {
				start = totalCount
			}
			if end > totalCount {
				end = totalCount
			}
			paginatedRules := config.Endpoints[start:end]
			traffic.Refresh(config)
			type ruleView struct {
				ForwardingRule
				UploadBytes            uint64 `json:"upload_bytes"`
				DownloadBytes          uint64 `json:"download_bytes"`
				TodayUploadBytes       uint64 `json:"today_upload_bytes"`
				TodayDownloadBytes     uint64 `json:"today_download_bytes"`
				YesterdayUploadBytes   uint64 `json:"yesterday_upload_bytes"`
				YesterdayDownloadBytes uint64 `json:"yesterday_download_bytes"`
				DailyWarning           string `json:"daily_warning,omitempty"`
			}
			now := traffic.nowTime()
			todayDate, yesterdayDate := dailyDates(now)
			views := make([]ruleView, 0, len(paginatedRules))
			for _, rule := range paginatedRules {
				usage := traffic.state.Rules[rule.Listen]
				today, yesterday, warning := dailyView(usage, now)
				views = append(views, ruleView{
					ForwardingRule: rule, UploadBytes: usage.Upload, DownloadBytes: usage.Download,
					TodayUploadBytes: today.Upload, TodayDownloadBytes: today.Download,
					YesterdayUploadBytes: yesterday.Upload, YesterdayDownloadBytes: yesterday.Download,
					DailyWarning: warning,
				})
			}

			c.JSON(200, gin.H{
				"rules":             views,
				"total":             totalCount,
				"traffic_available": traffic.Available,
				"traffic_warning":   traffic.Warning,
				"traffic_today":     todayDate,
				"traffic_yesterday": yesterdayDate,
				"traffic_timezone":  now.Format("MST -07:00"),
			})
		})

		authorized.GET("/traffic_speed", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store")
			mu.Lock()
			speed := traffic.Speed()
			mu.Unlock()
			c.JSON(http.StatusOK, speed)
		})

		authorized.POST("/add_rule", func(c *gin.Context) {
			var input ForwardingRule

			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(400, gin.H{"error": "无效的输入"})
				return
			}

			if err := validateForwardingRule(input); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}

			mu.Lock()
			for _, rule := range config.Endpoints {
				if rule.Listen == input.Listen {
					mu.Unlock()
					c.JSON(409, gin.H{"error": "端口已存在"})
					return
				}
			}
			config.Endpoints = append(config.Endpoints, input)
			err := saveConfigLocked()
			if err != nil {
				config.Endpoints = config.Endpoints[:len(config.Endpoints)-1]
			}
			mu.Unlock()

			if err != nil {
				c.JSON(500, gin.H{"error": "保存配置失败"})
				return
			}

			c.JSON(201, input)
		})

		authorized.PUT("/update_rule", func(c *gin.Context) {
			originalListen := c.Query("listen")
			if originalListen == "" {
				c.JSON(400, gin.H{"error": "listen 参数不能为空"})
				return
			}

			var input ForwardingRule
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(400, gin.H{"error": "无效的输入"})
				return
			}
			if err := validateForwardingRule(input); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}

			mu.Lock()
			err := updateForwardingRuleLocked(originalListen, input)
			if err == nil {
				for _, rule := range config.Endpoints {
					if rule.Listen == input.Listen {
						input = rule
						break
					}
				}
			}
			mu.Unlock()

			switch {
			case errors.Is(err, errRuleNotFound):
				c.JSON(404, gin.H{"error": err.Error()})
			case errors.Is(err, errListenConflict):
				c.JSON(409, gin.H{"error": err.Error()})
			case err != nil:
				c.JSON(500, gin.H{"error": "保存配置失败"})
			default:
				c.JSON(200, input)
			}
		})

		authorized.DELETE("/delete_rule", func(c *gin.Context) {
			listen := c.Query("listen")

			if listen == "" {
				c.JSON(400, gin.H{"error": "listen 参数不能为空"})
				return
			}

			mu.Lock()
			_, err := deleteForwardingRulesLocked([]string{listen})
			mu.Unlock()

			if err != nil && !errors.Is(err, errRuleNotFound) {
				c.JSON(500, gin.H{"error": "保存转发规则失败"})
				return
			}

			if err == nil {
				c.JSON(200, gin.H{"message": "保存转发规则成功"})
			} else {
				c.JSON(404, gin.H{"error": "未找到转发规则"})
			}
		})

		authorized.DELETE("/delete_rules", func(c *gin.Context) {
			var input struct {
				Listens []string `json:"listens"`
			}
			if err := c.ShouldBindJSON(&input); err != nil || len(input.Listens) == 0 {
				c.JSON(400, gin.H{"error": "请选择要删除的规则"})
				return
			}

			mu.Lock()
			deleted, err := deleteForwardingRulesLocked(input.Listens)
			mu.Unlock()

			switch {
			case errors.Is(err, errRuleNotFound):
				c.JSON(404, gin.H{"error": err.Error()})
			case err != nil:
				c.JSON(500, gin.H{"error": "保存转发规则失败"})
			default:
				c.JSON(200, gin.H{"message": "批量删除成功", "deleted": deleted})
			}
		})

		authorized.PUT("/toggle_rule", func(c *gin.Context) {
			var input struct {
				Listen   string `json:"listen"`
				Disabled *bool  `json:"disabled"`
			}
			if err := c.ShouldBindJSON(&input); err != nil || input.Listen == "" || input.Disabled == nil {
				c.JSON(400, gin.H{"error": "请指定规则和暂停状态"})
				return
			}
			mu.Lock()
			err := LoadConfig()
			if err == nil {
				err = toggleRuleLocked(input.Listen, *input.Disabled, serviceManager)
			}
			mu.Unlock()
			if errors.Is(err, errRuleNotFound) {
				c.JSON(404, gin.H{"error": err.Error()})
			} else if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
			} else {
				c.JSON(200, gin.H{"message": "规则状态已更新", "disabled": *input.Disabled})
			}
		})

		authorized.POST("/reset_traffic", func(c *gin.Context) {
			var input struct {
				Listens []string `json:"listens"`
			}
			if err := c.ShouldBindJSON(&input); err != nil || len(input.Listens) == 0 {
				c.JSON(400, gin.H{"error": "请选择要清空流量的规则"})
				return
			}
			mu.Lock()
			err := LoadConfig()
			count := 0
			if err == nil {
				count, err = traffic.Reset(config, input.Listens)
			}
			mu.Unlock()
			if errors.Is(err, errRuleNotFound) {
				c.JSON(404, gin.H{"error": err.Error()})
			} else if err != nil {
				c.JSON(500, gin.H{"error": err.Error()})
			} else {
				c.JSON(200, gin.H{"message": "所选流量已清空", "cleared": count})
			}
		})

		authorized.POST("/start_service", func(c *gin.Context) {
			if err := serviceManager.Start("realm"); err != nil {
				c.JSON(500, gin.H{"error": "服务启动失败"})
				return
			}

			c.JSON(200, gin.H{"message": "服务启动成功"})
		})

		authorized.POST("/stop_service", func(c *gin.Context) {
			if err := serviceManager.Stop("realm"); err != nil {
				c.JSON(500, gin.H{"error": "服务停止失败"})
				return
			}

			c.JSON(200, gin.H{"message": "服务停止成功"})
		})

		authorized.POST("/restart_service", func(c *gin.Context) {
			if err := serviceManager.Restart("realm"); err != nil {
				c.JSON(500, gin.H{"error": "服务重启失败"})
				return
			}

			c.JSON(200, gin.H{"message": "服务重启成功"})
		})

		authorized.GET("/check_status", func(c *gin.Context) {
			c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")

			active, err := serviceManager.IsActive("realm")
			status := "启用"
			if err != nil {
				status = "未知状态"
			} else if !active {
				status = "未启用"
			}

			c.JSON(200, gin.H{"status": status})
		})

		authorized.POST("/logout", func(c *gin.Context) {
			session := sessions.Default(c)
			session.Clear()
			session.Save()
			c.JSON(http.StatusOK, gin.H{"message": "登出成功"})
		})
	}

	port := panelConfig.Server.Port
	if port == 0 {
		port = 8081 // 默认端口
	}

	if panelConfig.HTTPS.Enabled {
		if panelConfig.HTTPS.CertFile == "" || panelConfig.HTTPS.KeyFile == "" {
			log.Println("警告：HTTPS 已启用，但证书或密钥文件路径未指定。将使用 HTTP 继续。")
			log.Printf("服务器正在使用 HTTP 运行，端口：%d\n", port)
			r.Run(fmt.Sprintf(":%d", port))
		} else {
			log.Printf("服务器正在使用 HTTPS 运行，端口：%d\n", port)
			go func() {
				log.Printf("HTTP 服务器正在运行，端口：8082，用于重定向到 HTTPS\n")
				if err := http.ListenAndServe(":8082", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					target := "https://" + r.Host + r.URL.Path
					if r.URL.RawQuery != "" {
						target += "?" + r.URL.RawQuery
					}
					http.Redirect(w, r, target, http.StatusMovedPermanently)
				})); err != nil {
					log.Fatalf("HTTP 服务器错误: %v", err)
				}
			}()
			if err := r.RunTLS(fmt.Sprintf(":%d", port), panelConfig.HTTPS.CertFile, panelConfig.HTTPS.KeyFile); err != nil {
				log.Fatalf("HTTPS 服务器错误: %v", err)
			}
		}
	} else {
		log.Println("警告：未启用 HTTPS，强烈建议启用 HTTPS。")
		log.Printf("服务器正在使用 HTTP 运行，端口：%d\n", port)
		r.Run(fmt.Sprintf(":%d", port))
	}
}
