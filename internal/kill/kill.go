// Package kill safely terminates processes with protection against critical
// system processes and provides pre-flight safety checks.
package kill

import (
	"fmt"
	"syscall"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// Confirmation is the result of a safety check.
type Confirmation struct {
	Processes      []collector.ProcessInfo
	HasProtected   bool
	HasSystem      bool
	SafetyWarnings []string
}

// Kill sends SIGTERM (or SIGKILL with force=true) to the given PID.
func Kill(pid int32, force bool) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return fmt.Errorf("get process %d: %w", pid, err)
	}
	running, err := p.IsRunning()
	if err != nil || !running {
		return fmt.Errorf("process %d is not running", pid)
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if err := p.SendSignal(sig); err != nil {
		return fmt.Errorf("signal %s to pid %d: %w", sig, pid, err)
	}
	return nil
}

// CheckSafety returns a Confirmation listing warnings.
func CheckSafety(pids []int32) Confirmation {
	var conf Confirmation
	for _, pid := range pids {
		p, err := process.NewProcess(pid)
		if err != nil {
			continue
		}
		name, _ := p.Name()
		user, _ := p.Username()
		pi := collector.ProcessInfo{PID: pid, Name: name, User: user}
		isProtected := collector.ProtectedProcessNames[name] || pid == 1 || pid < 100
		isSystem := user == "root" || user == "_mbsetupuser"
		if isProtected {
			conf.HasProtected = true
			pi.IsProtected = true
			conf.SafetyWarnings = append(conf.SafetyWarnings,
				fmt.Sprintf("%s (pid %d) is a protected system process", name, pid))
		} else if isSystem {
			conf.HasSystem = true
			pi.IsSystem = true
			conf.SafetyWarnings = append(conf.SafetyWarnings,
				fmt.Sprintf("%s (pid %d) is owned by %s", name, pid, user))
		}
		conf.Processes = append(conf.Processes, pi)
	}
	return conf
}
