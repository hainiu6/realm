package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

type toggleManager struct {
	failures int
	calls    int
}

func (m *toggleManager) Start(string) error            { return nil }
func (m *toggleManager) Stop(string) error             { return nil }
func (m *toggleManager) IsActive(string) (bool, error) { return true, nil }
func (m *toggleManager) Restart(string) error {
	m.calls++
	if m.failures > 0 {
		m.failures--
		return errors.New("restart failed")
	}
	return nil
}

func isolatedConfig(t *testing.T) {
	t.Helper()
	previousPath, previousConfig, previousTraffic := realmConfigPath, config, traffic
	t.Cleanup(func() { realmConfigPath, config, traffic = previousPath, previousConfig, previousTraffic })
	realmConfigPath = filepath.Join(t.TempDir(), "config.toml")
	traffic = nil
	config = Config{}
}

func TestPausedRulesPersistWithoutBecomingRealmEndpoints(t *testing.T) {
	isolatedConfig(t)
	config.Network.UseUDP = true
	config.Endpoints = []ForwardingRule{
		{Listen: "[::]:10001", Remote: "203.0.113.1:443"},
		{Listen: "[::]:10002", Remote: "203.0.113.2:443", ExtraRemotes: []string{"[2001:db8::2]:443"}, Balance: "roundrobin: 2, 3"},
		{Listen: "[::]:10003", Remote: "203.0.113.3:443"},
	}
	manager := &toggleManager{}
	if err := SaveConfig(); err != nil {
		t.Fatal(err)
	}
	if err := toggleRuleLocked("[::]:10002", true, manager); err != nil {
		t.Fatal(err)
	}
	if err := toggleRuleLocked("[::]:10001", true, manager); err != nil {
		t.Fatal(err)
	}
	want := config
	data, err := os.ReadFile(realmConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var realm Config
	if _, err := toml.Decode(string(data), &realm); err != nil {
		t.Fatal(err)
	}
	if len(realm.Endpoints) != 1 || realm.Endpoints[0].Listen != "[::]:10003" {
		t.Fatalf("paused rules still active: %#v", realm.Endpoints)
	}
	config = Config{}
	if err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(config, want) {
		t.Fatalf("paused rules, order or weights lost: %#v", config)
	}
	if err := toggleRuleLocked("[::]:10002", false, manager); err != nil {
		t.Fatal(err)
	}
	if config.Endpoints[1].Disabled || config.Endpoints[1].Balance != "roundrobin: 2, 3" {
		t.Fatal("enable lost settings")
	}
	if err := toggleRuleLocked("[::]:10002", false, manager); err != nil {
		t.Fatal(err)
	}
	if manager.calls != 3 {
		t.Fatalf("idempotent toggle restarted service: %d", manager.calls)
	}
	if err := toggleRuleLocked("[::]:10002", true, manager); err != nil {
		t.Fatal(err)
	}
	if err := toggleRuleLocked("[::]:10003", true, manager); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(realmConfigPath)
	if !strings.HasPrefix(string(data), "endpoints = []\n") {
		t.Fatal("all-paused config cannot start Realm")
	}
}

func TestToggleRollsBackOnRestartFailure(t *testing.T) {
	isolatedConfig(t)
	config.Endpoints = []ForwardingRule{{Listen: "[::]:10001", Remote: "203.0.113.1:443"}}
	if err := SaveConfig(); err != nil {
		t.Fatal(err)
	}
	manager := &toggleManager{failures: 1}
	if err := toggleRuleLocked("[::]:10001", true, manager); err == nil {
		t.Fatal("restart failure hidden")
	}
	if err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if config.Endpoints[0].Disabled || manager.calls != 2 {
		t.Fatal("failed pause not rolled back")
	}
}

func TestEditingPausedRuleDoesNotEnableIt(t *testing.T) {
	isolatedConfig(t)
	config.Endpoints = []ForwardingRule{{Listen: "[::]:10001", Remote: "203.0.113.1:443", Disabled: true}}
	if err := SaveConfig(); err != nil {
		t.Fatal(err)
	}
	if err := updateForwardingRuleLocked("[::]:10001", ForwardingRule{Listen: "[::]:11001", Remote: "203.0.113.2:443"}); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if !config.Endpoints[0].Disabled || config.Endpoints[0].Listen != "[::]:11001" {
		t.Fatal("editing enabled a paused rule")
	}
	if _, err := deleteForwardingRulesLocked([]string{"[::]:11001"}); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	if len(config.Endpoints) != 0 {
		t.Fatal("deleted paused rule reappeared")
	}
}
