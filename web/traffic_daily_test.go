package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func dailyMonitor(t *testing.T, now *time.Time) (*TrafficMonitor, *fakeNFT, Config) {
	t.Helper()
	runner := &fakeNFT{}
	m, err := newTrafficMonitor(filepath.Join(t.TempDir(), "traffic.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	m.clock = func() time.Time { return *now }
	c := Config{Endpoints: []ForwardingRule{{Listen: "[::]:10001"}}}
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	return m, runner, c
}

func TestDailyRolloverPersistenceAndReset(t *testing.T) {
	zone := time.FixedZone("CST", 8*60*60)
	now := time.Date(2026, 10, 24, 23, 59, 40, 0, zone)
	m, runner, c := dailyMonitor(t, &now)
	listen := c.Endpoints[0].Listen
	now = now.Add(10 * time.Second)
	runner.set(listen, 100, 200)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	current, previous, _ := dailyView(m.state.Rules[listen], now)
	if current.Upload != 100 || current.Download != 200 || previous.Upload != 0 {
		t.Fatal("same-day count incorrect")
	}
	// 20 seconds, ten seconds on each side of local midnight.
	now = now.Add(20 * time.Second)
	runner.set(listen, 300, 600)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	current, previous, warning := dailyView(m.state.Rules[listen], now)
	if current.Upload != 100 || current.Download != 200 || previous.Upload != 200 || previous.Download != 400 || warning != "" {
		t.Fatalf("midnight split: today=%#v yesterday=%#v warning=%s", current, previous, warning)
	}
	reloaded, err := newTrafficMonitor(m.path, runner)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.clock = m.clock
	if err := reloaded.Refresh(c); err != nil {
		t.Fatal(err)
	}
	a, b, _ := dailyView(reloaded.state.Rules[listen], now)
	if a != current || b != previous {
		t.Fatal("restart lost or double-counted daily traffic")
	}
	if _, err := reloaded.Reset(c, []string{listen}); err != nil {
		t.Fatal(err)
	}
	a, b, _ = dailyView(reloaded.state.Rules[listen], now)
	if a != current || b != previous || reloaded.state.Rules[listen].Upload != 0 {
		t.Fatal("cumulative reset changed daily history")
	}
	now = now.Add(15 * time.Second)
	runner.set(listen, 350, 700)
	if err := reloaded.Refresh(c); err != nil {
		t.Fatal(err)
	}
	a, _, _ = dailyView(reloaded.state.Rules[listen], now)
	if a.Upload != 150 || a.Download != 300 || reloaded.state.Rules[listen].Upload != 50 {
		t.Fatal("reset baseline corrupted daily accounting")
	}
}

func TestDailyLegacyMigrationDoesNotBackfillHistory(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	m, runner, c := dailyMonitor(t, &now)
	listen := c.Endpoints[0].Listen
	legacy := m.state.Rules[listen]
	legacy.Upload, legacy.Download = 9000, 18000
	legacy.Days, legacy.DailySampleAt = nil, 0
	m.state.Rules[listen] = legacy
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newTrafficMonitor(m.path, runner)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.clock = m.clock
	runner.set(listen, 1000, 2000)
	if err := reloaded.Refresh(c); err != nil {
		t.Fatal(err)
	}
	a, b, _ := dailyView(reloaded.state.Rules[listen], now)
	if a.Upload != 0 || b.Upload != 0 || reloaded.state.Rules[listen].Upload != 10000 {
		t.Fatal("old cumulative data mislabeled as today's usage or lost")
	}
	now = now.Add(15 * time.Second)
	runner.set(listen, 1100, 2200)
	if err := reloaded.Refresh(c); err != nil {
		t.Fatal(err)
	}
	a, _, _ = dailyView(reloaded.state.Rules[listen], now)
	if a.Upload != 100 || a.Download != 200 {
		t.Fatal("migration did not begin new daily tracking")
	}
}

func TestDailyOfflineAcrossDaysIsNotInvented(t *testing.T) {
	now := time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC)
	m, runner, c := dailyMonitor(t, &now)
	listen := c.Endpoints[0].Listen
	now = now.Add(15 * time.Second)
	runner.set(listen, 100, 200)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Hour)
	runner.set(listen, 1100, 2200)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	a, b, warning := dailyView(m.state.Rules[listen], now)
	if a.Upload != 0 || b.Upload != 100 || !a.Incomplete || !b.Incomplete || warning == "" || m.state.Rules[listen].Upload != 1100 {
		t.Fatal("unrecoverable cross-day outage was guessed or lifetime bytes lost")
	}
	// A long same-day outage can be attributed unambiguously.
	now = now.Add(time.Hour)
	runner.set(listen, 1200, 2400)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	a, _, _ = dailyView(m.state.Rules[listen], now)
	if a.Upload != 100 {
		t.Fatal("same-day outage bytes lost")
	}
	// Old buckets/partial flags age out; a paused rule must also age correctly.
	c.Endpoints[0].Disabled = true
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	now = now.AddDate(0, 0, 3)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	a, b, warning = dailyView(m.state.Rules[listen], now)
	if a.Upload != 0 || b.Upload != 0 || len(m.state.Rules[listen].Days) != 0 || warning != "" {
		t.Fatal("paused rule kept stale date buckets")
	}
}

func TestDailyMoveRetainsBuckets(t *testing.T) {
	isolatedConfig(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	m, runner, c := dailyMonitor(t, &now)
	traffic, config = m, c
	old, next := "[::]:10001", "[::]:11001"
	config.Endpoints[0].Remote = "203.0.113.1:443"
	if err := SaveConfig(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(15 * time.Second)
	runner.set(old, 123, 456)
	if err := updateForwardingRuleLocked(old, ForwardingRule{Listen: next, Remote: "203.0.113.2:443"}); err != nil {
		t.Fatal(err)
	}
	a, _, _ := dailyView(traffic.state.Rules[next], now)
	if a.Upload != 123 || a.Download != 456 {
		t.Fatal("editing listen lost daily history")
	}
	now = now.Add(15 * time.Second)
	runner.set(next, 10, 20)
	if err := traffic.Refresh(config); err != nil {
		t.Fatal(err)
	}
	a, _, _ = dailyView(traffic.state.Rules[next], now)
	if a.Upload != 133 || a.Download != 476 {
		t.Fatal("new listen did not continue daily totals")
	}
	data, err := os.ReadFile(traffic.path)
	if err != nil {
		t.Fatal(err)
	}
	var saved trafficState
	if err := json.Unmarshal(data, &saved); err != nil || saved.Rules[next].Days["2026-10-25"].Upload != 133 {
		t.Fatal("daily history not saved")
	}
}

func TestDailyCalendarBoundariesAndDST(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{
		time.Date(2027, 1, 1, 0, 0, 5, 0, time.FixedZone("CST", 8*60*60)),
		time.Date(2026, 3, 9, 0, 0, 5, 0, zone),  // follows a 23-hour day
		time.Date(2026, 11, 2, 0, 0, 5, 0, zone), // follows a 25-hour day
	} {
		today, yesterday := dailyDates(now)
		usage := trafficUsage{DailySampleAt: now.Add(-10 * time.Second).UnixMilli()}
		pruneDaily(&usage, now)
		recordDaily(&usage, 101, 201, now)
		if usage.Days[today].Upload != 51 || usage.Days[yesterday].Upload != 50 || usage.Days[today].Download+usage.Days[yesterday].Download != 201 {
			t.Fatalf("calendar split incorrect at %s: %#v", now, usage.Days)
		}
	}
	if got := proportionalBytes(math.MaxUint64, time.Second, 2*time.Second); got != math.MaxUint64/2 {
		t.Fatal("large byte counts overflowed")
	}
}
