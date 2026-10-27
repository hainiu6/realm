package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidateForwardingRuleWithRoundRobin(t *testing.T) {
	rule := ForwardingRule{
		Listen:       "[::]:10000",
		Remote:       "10.0.0.11:443",
		ExtraRemotes: []string{"10.0.0.12:443", "[2001:db8::13]:443"},
		Balance:      "roundrobin: 2, 1, 1",
	}

	if err := validateForwardingRule(rule); err != nil {
		t.Fatalf("expected valid rule, got %v", err)
	}
}

func TestPanelSessionLastsThirtyDays(t *testing.T) {
	options := sessionOptions(panelSessionMaxAge)
	if options.MaxAge != 30*24*60*60 {
		t.Fatalf("expected 30-day session, got %d seconds", options.MaxAge)
	}
	if !options.HttpOnly {
		t.Fatal("session cookie must remain HttpOnly")
	}
}

func TestValidateForwardingRuleWithIPHash(t *testing.T) {
	rule := ForwardingRule{
		Listen:       "0.0.0.0:10000",
		Remote:       "node-a.example.com:443",
		ExtraRemotes: []string{"node-b.example.com:443"},
		Balance:      "iphash: 1, 1",
	}

	if err := validateForwardingRule(rule); err != nil {
		t.Fatalf("expected valid rule, got %v", err)
	}
}

func TestValidateForwardingRuleRejectsInvalidLoadBalancing(t *testing.T) {
	tests := []struct {
		name    string
		rule    ForwardingRule
		message string
	}{
		{
			name: "missing strategy",
			rule: ForwardingRule{
				Listen:       "[::]:10000",
				Remote:       "10.0.0.11:443",
				ExtraRemotes: []string{"10.0.0.12:443"},
			},
			message: "必须设置负载均衡策略",
		},
		{
			name: "unsupported strategy",
			rule: ForwardingRule{
				Listen:       "[::]:10000",
				Remote:       "10.0.0.11:443",
				ExtraRemotes: []string{"10.0.0.12:443"},
				Balance:      "random: 1, 1",
			},
			message: "不支持的负载均衡策略",
		},
		{
			name: "wrong weight count",
			rule: ForwardingRule{
				Listen:       "[::]:10000",
				Remote:       "10.0.0.11:443",
				ExtraRemotes: []string{"10.0.0.12:443"},
				Balance:      "roundrobin: 1",
			},
			message: "权重数量必须与远端数量一致",
		},
		{
			name: "zero weight",
			rule: ForwardingRule{
				Listen:       "[::]:10000",
				Remote:       "10.0.0.11:443",
				ExtraRemotes: []string{"10.0.0.12:443"},
				Balance:      "roundrobin: 1, 0",
			},
			message: "权重必须是大于 0 的整数",
		},
		{
			name: "duplicate remote",
			rule: ForwardingRule{
				Listen:       "[::]:10000",
				Remote:       "10.0.0.11:443",
				ExtraRemotes: []string{"10.0.0.11:443"},
				Balance:      "roundrobin: 1, 1",
			},
			message: "远端地址不能重复",
		},
		{
			name: "invalid extra remote",
			rule: ForwardingRule{
				Listen:       "[::]:10000",
				Remote:       "10.0.0.11:443",
				ExtraRemotes: []string{"10.0.0.12"},
				Balance:      "roundrobin: 1, 1",
			},
			message: "格式无效",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateForwardingRule(tt.rule)
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("expected error containing %q, got %v", tt.message, err)
			}
		})
	}
}

func TestSaveAndLoadConfigPreservesLoadBalancing(t *testing.T) {
	originalPath := realmConfigPath
	originalConfig := config
	t.Cleanup(func() {
		realmConfigPath = originalPath
		config = originalConfig
	})

	realmConfigPath = filepath.Join(t.TempDir(), "config.toml")
	config = Config{}
	config.Network.UseUDP = true
	config.Endpoints = []ForwardingRule{
		{
			Listen:       "[::]:10000",
			Remote:       "10.0.0.11:443",
			ExtraRemotes: []string{"10.0.0.12:443", "10.0.0.13:443"},
			Balance:      "roundrobin: 2, 1, 1",
		},
	}

	if err := SaveConfig(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(realmConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`extra_remotes = ["10.0.0.12:443", "10.0.0.13:443"]`,
		`balance = "roundrobin: 2, 1, 1"`,
	} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("saved config does not contain %q:\n%s", expected, contents)
		}
	}

	config = Config{}
	if err := LoadConfig(); err != nil {
		t.Fatal(err)
	}

	want := ForwardingRule{
		Listen:       "[::]:10000",
		Remote:       "10.0.0.11:443",
		ExtraRemotes: []string{"10.0.0.12:443", "10.0.0.13:443"},
		Balance:      "roundrobin: 2, 1, 1",
	}
	if len(config.Endpoints) != 1 || !reflect.DeepEqual(config.Endpoints[0], want) {
		t.Fatalf("round trip mismatch: %#v", config.Endpoints)
	}
}

func TestSaveConfigOmitsEmptyLoadBalancingFields(t *testing.T) {
	originalPath := realmConfigPath
	originalConfig := config
	t.Cleanup(func() {
		realmConfigPath = originalPath
		config = originalConfig
	})

	realmConfigPath = filepath.Join(t.TempDir(), "config.toml")
	config = Config{
		Endpoints: []ForwardingRule{
			{Listen: "[::]:10000", Remote: "10.0.0.11:443"},
		},
	}

	if err := SaveConfig(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(realmConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "extra_remotes") || strings.Contains(string(contents), "balance") {
		t.Fatalf("single-remote config contains empty load-balancing fields:\n%s", contents)
	}
}

func TestSaveEmptyConfigWritesTopLevelEndpoints(t *testing.T) {
	originalPath := realmConfigPath
	originalConfig := config
	t.Cleanup(func() {
		realmConfigPath = originalPath
		config = originalConfig
	})

	realmConfigPath = filepath.Join(t.TempDir(), "config.toml")
	config = Config{}
	config.Network.UseUDP = true

	if err := SaveConfig(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(realmConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(contents), "endpoints = []\n") {
		t.Fatalf("empty config is missing top-level endpoints:\n%s", contents)
	}

	config = Config{}
	if err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if config.Endpoints == nil || len(config.Endpoints) != 0 {
		t.Fatalf("expected a non-nil empty endpoints list, got %#v", config.Endpoints)
	}
}

func TestUpdateForwardingRuleLocked(t *testing.T) {
	originalPath := realmConfigPath
	originalConfig := config
	t.Cleanup(func() {
		realmConfigPath = originalPath
		config = originalConfig
	})

	realmConfigPath = filepath.Join(t.TempDir(), "config.toml")
	config = Config{
		Endpoints: []ForwardingRule{
			{Listen: "[::]:10000", Remote: "10.0.0.11:443"},
			{Listen: "[::]:20000", Remote: "10.0.0.21:443"},
		},
	}
	updated := ForwardingRule{
		Listen:       "[::]:10001",
		Remote:       "10.0.0.12:8443",
		ExtraRemotes: []string{"10.0.0.13:8443"},
		Balance:      "roundrobin: 2, 1",
	}

	if err := updateForwardingRuleLocked("[::]:10000", updated); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config.Endpoints[0], updated) {
		t.Fatalf("rule was not updated: %#v", config.Endpoints[0])
	}

	config = Config{}
	if err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if len(config.Endpoints) != 2 || !reflect.DeepEqual(config.Endpoints[0], updated) {
		t.Fatalf("updated rule was not persisted: %#v", config.Endpoints)
	}
}

func TestUpdateForwardingRuleLockedRejectsConflictAndMissingRule(t *testing.T) {
	originalPath := realmConfigPath
	originalConfig := config
	t.Cleanup(func() {
		realmConfigPath = originalPath
		config = originalConfig
	})

	realmConfigPath = filepath.Join(t.TempDir(), "config.toml")
	config = Config{
		Endpoints: []ForwardingRule{
			{Listen: "[::]:10000", Remote: "10.0.0.11:443"},
			{Listen: "[::]:20000", Remote: "10.0.0.21:443"},
		},
	}

	conflicting := ForwardingRule{Listen: "[::]:20000", Remote: "10.0.0.12:443"}
	if err := updateForwardingRuleLocked("[::]:10000", conflicting); !errors.Is(err, errListenConflict) {
		t.Fatalf("expected listen conflict, got %v", err)
	}
	if err := updateForwardingRuleLocked("[::]:30000", conflicting); !errors.Is(err, errRuleNotFound) {
		t.Fatalf("expected missing rule, got %v", err)
	}
}

func TestDeleteForwardingRulesLocked(t *testing.T) {
	originalPath := realmConfigPath
	originalConfig := config
	t.Cleanup(func() {
		realmConfigPath = originalPath
		config = originalConfig
	})

	realmConfigPath = filepath.Join(t.TempDir(), "config.toml")
	config = Config{
		Endpoints: []ForwardingRule{
			{Listen: "[::]:10000", Remote: "10.0.0.11:443"},
			{Listen: "[::]:20000", Remote: "10.0.0.21:443"},
			{Listen: "[::]:30000", Remote: "10.0.0.31:443"},
		},
	}

	deleted, err := deleteForwardingRulesLocked([]string{"[::]:10000", "[::]:30000", "[::]:30000"})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 deleted rules, got %d", deleted)
	}
	if len(config.Endpoints) != 1 || config.Endpoints[0].Listen != "[::]:20000" {
		t.Fatalf("unexpected remaining rules: %#v", config.Endpoints)
	}

	config = Config{}
	if err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if len(config.Endpoints) != 1 || config.Endpoints[0].Listen != "[::]:20000" {
		t.Fatalf("deleted rules were not persisted: %#v", config.Endpoints)
	}
}

func TestDeleteForwardingRulesLockedHandlesMissingAndAllRules(t *testing.T) {
	originalPath := realmConfigPath
	originalConfig := config
	t.Cleanup(func() {
		realmConfigPath = originalPath
		config = originalConfig
	})

	realmConfigPath = filepath.Join(t.TempDir(), "config.toml")
	config = Config{Endpoints: []ForwardingRule{{Listen: "[::]:10000", Remote: "10.0.0.11:443"}}}

	if _, err := deleteForwardingRulesLocked([]string{"[::]:9999"}); !errors.Is(err, errRuleNotFound) {
		t.Fatalf("expected missing rule error, got %v", err)
	}
	deleted, err := deleteForwardingRulesLocked([]string{"[::]:10000"})
	if err != nil || deleted != 1 {
		t.Fatalf("expected final rule deletion, got deleted=%d err=%v", deleted, err)
	}
	contents, err := os.ReadFile(realmConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(contents), "endpoints = []\n") {
		t.Fatalf("deleting all rules did not write a valid empty config:\n%s", contents)
	}
}
