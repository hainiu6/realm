package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Comments keep paused rules portable without exposing them as active Realm endpoints.
const pausedRulePrefix = "# realm-panel-paused: "

type pausedRule struct {
	Position int            `json:"position"`
	Rule     ForwardingRule `json:"rule"`
}

func restorePausedRules(data []byte, loaded *Config) error {
	var paused []pausedRule
	seen := make(map[string]bool)
	for _, rule := range loaded.Endpoints {
		seen[rule.Listen] = true
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, pausedRulePrefix) {
			continue
		}
		var entry pausedRule
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, pausedRulePrefix)), &entry); err != nil {
			return fmt.Errorf("暂停规则数据损坏: %w", err)
		}
		if err := validateForwardingRule(entry.Rule); err != nil {
			return fmt.Errorf("暂停规则无效: %w", err)
		}
		// An explicit active endpoint takes precedence over a stale paused comment.
		if seen[entry.Rule.Listen] {
			continue
		}
		seen[entry.Rule.Listen] = true
		entry.Rule.Disabled = true
		paused = append(paused, entry)
	}
	sort.SliceStable(paused, func(i, j int) bool { return paused[i].Position < paused[j].Position })
	for _, entry := range paused {
		position := entry.Position
		if position < 0 {
			position = 0
		}
		if position > len(loaded.Endpoints) {
			position = len(loaded.Endpoints)
		}
		loaded.Endpoints = append(loaded.Endpoints, ForwardingRule{})
		copy(loaded.Endpoints[position+1:], loaded.Endpoints[position:])
		loaded.Endpoints[position] = entry.Rule
	}
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".realm-panel-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Caller holds mu. A failed restart restores the previous rule state on disk.
func toggleRuleLocked(listen string, disabled bool, manager ServiceManager) error {
	for i := range config.Endpoints {
		if config.Endpoints[i].Listen != listen {
			continue
		}
		previous := config.Endpoints[i].Disabled
		if previous == disabled {
			return nil
		}
		config.Endpoints[i].Disabled = disabled
		if err := saveConfigLocked(); err != nil {
			config.Endpoints[i].Disabled = previous
			return err
		}
		if err := manager.Restart("realm"); err != nil {
			config.Endpoints[i].Disabled = previous
			if restoreErr := saveConfigLocked(); restoreErr != nil {
				return fmt.Errorf("重启失败且恢复配置失败，请检查服务日志: %v / %v", err, restoreErr)
			}
			if restoreErr := manager.Restart("realm"); restoreErr != nil {
				return fmt.Errorf("配置已恢复，但 Realm 重启失败，请检查服务日志: %v", restoreErr)
			}
			return fmt.Errorf("Realm 重启失败，已恢复原规则状态: %w", err)
		}
		return nil
	}
	return errRuleNotFound
}
