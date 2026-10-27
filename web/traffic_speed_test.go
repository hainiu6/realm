package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficSpeedAggregateAndCounterChanges(t *testing.T) {
	a, b, paused := "[::]:10001", "[::]:10002", "[::]:10003"
	c := Config{Endpoints: []ForwardingRule{{Listen: a}, {Listen: b}, {Listen: paused, Disabled: true}}}
	m := &TrafficMonitor{Available: true}
	now := time.Now()
	raw := map[string]rawTraffic{
		trafficID(a):      {up: 100000, down: 200000, epoch: "one"},
		trafficID(b):      {up: 200000, down: 300000, epoch: "one"},
		trafficID(paused): {up: 900000, down: 900000, epoch: "one"},
	}
	m.sampleSpeed(c, raw, now)
	if !m.speed.Available || m.speed.Upload != 0 || m.speed.Download != 0 {
		t.Fatal("first sample must establish baseline without counting old traffic")
	}
	raw[trafficID(a)] = rawTraffic{up: 103000, down: 206000, epoch: "one"}
	raw[trafficID(b)] = rawTraffic{up: 206000, down: 312000, epoch: "one"}
	raw[trafficID(paused)] = rawTraffic{up: 1900000, down: 1900000, epoch: "one"}
	m.sampleSpeed(c, raw, now.Add(3*time.Second))
	if m.speed.Upload != 3000 || m.speed.Download != 6000 {
		t.Fatalf("incorrect aggregate or paused rule included: %#v", m.speed)
	}
	// A real 6-second interval (e.g. a delayed command) must use 6, not 3.
	raw[trafficID(a)] = rawTraffic{up: 109000, down: 218000, epoch: "one"}
	m.sampleSpeed(c, raw, now.Add(9*time.Second))
	if m.speed.Upload != 1000 || m.speed.Download != 2000 {
		t.Fatalf("actual elapsed time not respected: %#v", m.speed)
	}
	// Table rebuild and counter reset must never underflow or count history.
	raw[trafficID(a)] = rawTraffic{up: 999999, down: 999999, epoch: "two"}
	raw[trafficID(b)] = rawTraffic{up: 1, down: 2, epoch: "one"}
	m.sampleSpeed(c, raw, now.Add(12*time.Second))
	if m.speed.Upload != 0 || m.speed.Download != 0 {
		t.Fatalf("counter reset produced a speed spike: %#v", m.speed)
	}
	m.speedAt = time.Now().Add(-10 * time.Second)
	if m.Speed().Available {
		t.Fatal("stale speed reported as live")
	}
}

func TestClearingTotalsDoesNotChangeSpeed(t *testing.T) {
	runner := &fakeNFT{}
	m, err := newTrafficMonitor(filepath.Join(t.TempDir(), "traffic.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	listen := "[::]:10001"
	c := Config{Endpoints: []ForwardingRule{{Listen: listen}}}
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	runner.set(listen, 100000, 200000)
	raw, _, _ := m.read()
	now := time.Now()
	m.sampleSpeed(c, raw, now)
	runner.set(listen, 103000, 206000)
	if _, err := m.Reset(c, []string{listen}); err != nil {
		t.Fatal(err)
	}
	raw, _, _ = m.read()
	m.sampleSpeed(c, raw, now.Add(3*time.Second))
	if m.speed.Upload != 1000 || m.speed.Download != 2000 {
		t.Fatalf("clearing usage affected speed: %#v", m.speed)
	}
	if m.state.Rules[listen].Upload != 0 || m.state.Rules[listen].Download != 0 {
		t.Fatal("speed sampling changed cleared totals")
	}
	runner.unavailable = true
	m.SampleSpeed(c)
	if m.speed.Available || m.speed.Warning == "" || m.speedRaw != nil {
		t.Fatal("unavailable counter read retained stale speed")
	}
	runner.unavailable = false
	m.SampleSpeed(c)
	if !m.speed.Available || m.speed.Upload != 0 || m.speed.Download != 0 {
		t.Fatal("recovery counted outage traffic as current speed")
	}
}

func TestTrafficSpeedEmptyAndMissingCounters(t *testing.T) {
	runner := &fakeNFT{}
	m, err := newTrafficMonitor(filepath.Join(t.TempDir(), "traffic.json"), runner)
	if err != nil {
		t.Fatal(err)
	}
	c := Config{}
	m.SampleSpeed(c)
	if m.speed.Available {
		t.Fatal("missing table accepted")
	}
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	m.SampleSpeed(c)
	if !m.Speed().Available || m.Speed().Upload != 0 {
		t.Fatal("empty/all-paused config must report zero speed")
	}
	c.Endpoints = []ForwardingRule{{Listen: "[::]:10001"}}
	m.SampleSpeed(c)
	if m.speed.Available {
		t.Fatal("missing rule counters accepted")
	}
}
