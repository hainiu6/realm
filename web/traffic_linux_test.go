//go:build linux

package main

import (
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// CI runs this in a fresh Linux network namespace; never touch a host firewall.
func TestNFTLive(t *testing.T) {
	if os.Getenv("REALM_NFT_TEST") != "1" {
		t.Skip("requires isolated Linux network namespace")
	}
	if output, err := exec.Command("ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		t.Fatalf("loopback: %s %v", output, err)
	}
	listen4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listen4.Close()
	listen6, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listen6.Close()
	m, err := newTrafficMonitor(filepath.Join(t.TempDir(), "traffic.json"), systemNFTRunner{})
	if err != nil {
		t.Fatal(err)
	}
	c := Config{}
	c.Network.UseUDP = true
	c.Endpoints = []ForwardingRule{{Listen: listen4.Addr().String()}, {Listen: listen6.Addr().String()}}
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	m.SampleSpeed(c)
	transfer := func(listener net.Listener) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadFull(conn, make([]byte, 25000)); err != nil {
				done <- err
				return
			}
			_, err = conn.Write(bytes.Repeat([]byte("d"), 50000))
			done <- err
		}()
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write(bytes.Repeat([]byte("u"), 25000)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(conn, make([]byte, 50000)); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	udpTransfer := func(address string) {
		t.Helper()
		server, err := net.ListenPacket("udp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		done := make(chan error, 1)
		go func() {
			server.SetDeadline(time.Now().Add(5 * time.Second))
			_, peer, err := server.ReadFrom(make([]byte, 65535))
			if err != nil {
				done <- err
				return
			}
			_, err = server.WriteTo(bytes.Repeat([]byte("d"), 8192), peer)
			done <- err
		}()
		client, err := net.DialTimeout("udp", address, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		client.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := client.Write(bytes.Repeat([]byte("u"), 4096)); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Read(make([]byte, 65535)); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	transfer(listen4)
	transfer(listen6)
	udpTransfer(listen4.Addr().String())
	udpTransfer(listen6.Addr().String())
	time.Sleep(100 * time.Millisecond)
	m.SampleSpeed(c)
	if speed := m.Speed(); !speed.Available || speed.Upload <= 0 || speed.Download <= 0 {
		t.Fatalf("live speed missing: %#v", speed)
	}
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	for _, rule := range c.Endpoints {
		usage := m.state.Rules[rule.Listen]
		if usage.Upload < 25000+4096 || usage.Download < 50000+8192 {
			t.Fatalf("actual TCP/UDP traffic not counted for %s: %#v", rule.Listen, usage)
		}
		today, yesterday, warning := dailyView(usage, m.nowTime())
		if today.Upload+yesterday.Upload != usage.Upload || today.Download+yesterday.Download != usage.Download || warning != "" {
			t.Fatalf("live daily traffic incorrect: %#v / %#v", today, yesterday)
		}
	}
	second := m.state.Rules[c.Endpoints[1].Listen]
	if count, err := m.Reset(c, []string{c.Endpoints[0].Listen}); err != nil || count != 1 {
		t.Fatalf("reset: %d %v", count, err)
	}
	if m.state.Rules[c.Endpoints[0].Listen].Upload != 0 || m.state.Rules[c.Endpoints[1].Listen].Upload != second.Upload {
		t.Fatal("reset affected other rule")
	}
	c.Endpoints[0].Disabled = true
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	paused := m.state.Rules[c.Endpoints[0].Listen].Upload
	transfer(listen4)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if m.state.Rules[c.Endpoints[0].Listen].Upload != paused {
		t.Fatal("paused rule traffic still counted")
	}
	c.Endpoints[0].Disabled = false
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	transfer(listen4)
	time.Sleep(100 * time.Millisecond)
	if err := m.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if m.state.Rules[c.Endpoints[0].Listen].Upload < paused+25000 {
		t.Fatal("enabled rule did not resume counting")
	}
	totals := m.state.Rules[c.Endpoints[0].Listen]
	reloaded, err := newTrafficMonitor(m.path, systemNFTRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.Refresh(c); err != nil {
		t.Fatal(err)
	}
	if reloaded.state.Rules[c.Endpoints[0].Listen].Upload != totals.Upload {
		t.Fatal("restart recounted old traffic")
	}
	t.Log("IPv4/IPv6 TCP/UDP byte counters, selected reset, pause/resume and persistence verified")
}
