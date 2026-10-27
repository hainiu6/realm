package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

const serviceCommandTimeout = 10 * time.Second

type ServiceManager interface {
	Start(serviceName string) error
	Stop(serviceName string) error
	Restart(serviceName string) error
	IsActive(serviceName string) (bool, error)
}

type commandRunner interface {
	Run(name string, args ...string) error
}

type execCommandRunner struct {
	timeout time.Duration
}

func (r execCommandRunner) Run(name string, args ...string) error {
	timeout := r.timeout
	if timeout == 0 {
		timeout = serviceCommandTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Run()
}

type systemdServiceManager struct {
	runner  commandRunner
	command string
}

func (m systemdServiceManager) commandPath() string {
	if m.command != "" {
		return m.command
	}
	return "systemctl"
}

func (m systemdServiceManager) Start(serviceName string) error {
	return m.runner.Run(m.commandPath(), "start", serviceName)
}

func (m systemdServiceManager) Stop(serviceName string) error {
	return m.runner.Run(m.commandPath(), "stop", serviceName)
}

func (m systemdServiceManager) Restart(serviceName string) error {
	return m.runner.Run(m.commandPath(), "restart", serviceName)
}

func (m systemdServiceManager) IsActive(serviceName string) (bool, error) {
	err := m.runner.Run(m.commandPath(), "is-active", "--quiet", serviceName)
	if err == nil {
		return true, nil
	}
	if isInactiveServiceError(err) {
		return false, nil
	}
	return false, err
}

type openRCServiceManager struct {
	runner  commandRunner
	command string
}

func (m openRCServiceManager) commandPath() string {
	if m.command != "" {
		return m.command
	}
	return "rc-service"
}

func (m openRCServiceManager) Start(serviceName string) error {
	return m.runner.Run(m.commandPath(), serviceName, "start")
}

func (m openRCServiceManager) Stop(serviceName string) error {
	return m.runner.Run(m.commandPath(), serviceName, "stop")
}

func (m openRCServiceManager) Restart(serviceName string) error {
	return m.runner.Run(m.commandPath(), serviceName, "restart")
}

func (m openRCServiceManager) IsActive(serviceName string) (bool, error) {
	err := m.runner.Run(m.commandPath(), serviceName, "status")
	if err == nil {
		return true, nil
	}
	if isInactiveServiceError(err) {
		return false, nil
	}
	return false, err
}

func newServiceManager() ServiceManager {
	runner := execCommandRunner{timeout: serviceCommandTimeout}
	systemdCommand, hasSystemd := resolveExecutable([]string{"/bin/systemctl", "/usr/bin/systemctl"}, "systemctl")
	openRCCommand, hasOpenRC := resolveExecutable([]string{"/sbin/rc-service", "/usr/sbin/rc-service"}, "rc-service")

	switch os.Getenv("REALM_SERVICE_MANAGER") {
	case "openrc":
		return openRCServiceManager{runner: runner, command: openRCCommand}
	case "systemd":
		return systemdServiceManager{runner: runner, command: systemdCommand}
	}

	if _, err := os.Stat("/etc/alpine-release"); err == nil && hasOpenRC {
		return openRCServiceManager{runner: runner, command: openRCCommand}
	}
	if hasSystemd {
		return systemdServiceManager{runner: runner, command: systemdCommand}
	}
	if hasOpenRC {
		return openRCServiceManager{runner: runner, command: openRCCommand}
	}

	return systemdServiceManager{runner: runner, command: systemdCommand}
}

func resolveExecutable(candidates []string, fallback string) (string, bool) {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0 {
			return candidate, true
		}
	}
	return fallback, false
}

type exitCoder interface {
	ExitCode() int
}

func isInactiveServiceError(err error) bool {
	var exitErr exitCoder
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 3
}
