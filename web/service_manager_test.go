package main

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

type recordingRunner struct {
	calls [][]string
	err   error
}

func (r *recordingRunner) Run(name string, args ...string) error {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	return r.err
}

type exitCodeError int

func (e exitCodeError) Error() string {
	return "exit " + strconv.Itoa(int(e))
}

func (e exitCodeError) ExitCode() int {
	return int(e)
}

func TestSystemdServiceManagerUsesSystemctl(t *testing.T) {
	runner := &recordingRunner{}
	manager := systemdServiceManager{runner: runner}

	if err := manager.Start("realm"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop("realm"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restart("realm"); err != nil {
		t.Fatal(err)
	}
	active, err := manager.IsActive("realm")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("expected active service")
	}

	expected := [][]string{
		{"systemctl", "start", "realm"},
		{"systemctl", "stop", "realm"},
		{"systemctl", "restart", "realm"},
		{"systemctl", "is-active", "--quiet", "realm"},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("calls mismatch: %#v", runner.calls)
	}
}

func TestOpenRCServiceManagerUsesRcService(t *testing.T) {
	runner := &recordingRunner{}
	manager := openRCServiceManager{runner: runner}

	if err := manager.Start("realm"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop("realm"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restart("realm"); err != nil {
		t.Fatal(err)
	}
	active, err := manager.IsActive("realm")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("expected active service")
	}

	expected := [][]string{
		{"rc-service", "realm", "start"},
		{"rc-service", "realm", "stop"},
		{"rc-service", "realm", "restart"},
		{"rc-service", "realm", "status"},
	}
	if !reflect.DeepEqual(runner.calls, expected) {
		t.Fatalf("calls mismatch: %#v", runner.calls)
	}
}

func TestServiceManagersTreatInactiveExitCodeAsStopped(t *testing.T) {
	tests := []struct {
		name    string
		manager ServiceManager
	}{
		{name: "systemd", manager: systemdServiceManager{runner: &recordingRunner{err: exitCodeError(3)}}},
		{name: "openrc", manager: openRCServiceManager{runner: &recordingRunner{err: exitCodeError(3)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			active, err := tt.manager.IsActive("realm")
			if err != nil {
				t.Fatal(err)
			}
			if active {
				t.Fatal("expected stopped service")
			}
		})
	}
}

func TestServiceManagersReturnUnexpectedStatusErrors(t *testing.T) {
	tests := []struct {
		name    string
		manager ServiceManager
	}{
		{name: "systemd", manager: systemdServiceManager{runner: &recordingRunner{err: errors.New("missing command")}}},
		{name: "openrc", manager: openRCServiceManager{runner: &recordingRunner{err: errors.New("missing command")}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			active, err := tt.manager.IsActive("realm")
			if err == nil {
				t.Fatal("expected error")
			}
			if active {
				t.Fatal("expected inactive result with error")
			}
		})
	}
}
