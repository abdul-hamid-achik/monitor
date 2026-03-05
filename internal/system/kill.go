package system

import (
	"fmt"
	"os/user"
	"syscall"

	"github.com/shirou/gopsutil/v4/process"
)

// ProtectedProcessNames contains critical system processes that should not be killed
var ProtectedProcessNames = map[string]bool{
	"launchd":        true,
	"kernel_task":    true,
	"init":           true,
	"System":         true,
	"loginwindow":    true,
	"WindowServer":   true,
	"Finder":         true,
	"Dock":           true,
	"SystemUIServer": true,
	"coreaudiod":     true,
	"powerd":         true,
	"thermald":       true,
	"kernelmanagerd": true,
	"syspolicyd":     true,
	"trustd":         true,
	"securityd":      true,
}

// CriticalProcessNames contains processes that should never be killed (system stability risk)
var CriticalProcessNames = map[string]bool{
	"launchd":     true,
	"kernel_task": true,
	"init":        true,
}

// KillProcess safely terminates a process (non-blocking)
func KillProcess(pid int32, force bool) error {
	p, err := process.NewProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to get process: %w", err)
	}

	// Check if process still exists
	exists, err := p.IsRunning()
	if err != nil || !exists {
		return fmt.Errorf("process no longer exists")
	}

	// Determine signal
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}

	// Send signal
	if err := p.SendSignal(sig); err != nil {
		return fmt.Errorf("failed to send signal: %w", err)
	}

	return nil
}

// CheckKillSafety checks if it's safe to kill the given processes
func CheckKillSafety(pids []int32) KillConfirmation {
	var confirmation KillConfirmation
	var warnings []string

	for _, pid := range pids {
		p, err := process.NewProcess(pid)
		if err != nil {
			continue
		}

		name, _ := p.Name()
		username, _ := p.Username()

		procInfo := ProcessInfo{
			PID:  pid,
			Name: name,
			User: username,
		}

		// Check if protected using shared list
		isProtected := ProtectedProcessNames[name] || pid == 1 || pid < 100
		isSystem := username == "root" || username == "_mbsetupuser"

		if isProtected {
			confirmation.HasProtected = true
			procInfo.IsProtected = true
			warnings = append(warnings, fmt.Sprintf("%s (PID %d) is a protected system process", name, pid))
		} else if isSystem {
			confirmation.HasSystem = true
			procInfo.IsSystem = true
			warnings = append(warnings, fmt.Sprintf("%s (PID %d) is a system process (root-owned)", name, pid))
		}

		// Check if requires sudo
		currentUser := getCurrentUser()
		if username != "" && username != currentUser && currentUser != "root" {
			confirmation.RequiresSudo = true
			warnings = append(warnings, fmt.Sprintf("%s (PID %d) is owned by %s, may require elevated privileges", name, pid, username))
		}

		confirmation.Processes = append(confirmation.Processes, procInfo)
	}

	confirmation.SafetyWarnings = warnings
	return confirmation
}

// getCurrentUser returns the current username
func getCurrentUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// ValidateProcessKill validates if a process can be safely killed
func ValidateProcessKill(pid int32) (bool, []string) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return false, []string{"Process not found"}
	}

	var warnings []string

	name, _ := p.Name()
	username, _ := p.Username()

	// Critical system processes that should never be killed
	criticalProcesses := map[string]bool{
		"launchd":     true,
		"kernel_task": true,
		"init":        true,
	}

	if criticalProcesses[name] || pid == 1 {
		warnings = append(warnings, "CRITICAL: This is a core system process. Killing it may cause system instability or kernel panic.")
		return false, warnings
	}

	// Important system services
	importantProcesses := map[string]bool{
		"loginwindow":  true,
		"WindowServer": true,
		"Finder":       true,
		"Dock":         true,
		"SystemUIServer": true,
		"coreaudiod":   true,
		"powerd":       true,
		"thermald":     true,
	}

	if importantProcesses[name] {
		warnings = append(warnings, fmt.Sprintf("WARNING: %s is an important system service. Killing it may cause unexpected behavior.", name))
	}

	// Root-owned processes
	if username == "root" {
		warnings = append(warnings, "CAUTION: This process is owned by root. Ensure you have a good reason to terminate it.")
	}

	// Check if process belongs to current user
	currentUser := getCurrentUser()
	if username != "" && username != currentUser && currentUser != "root" {
		warnings = append(warnings, fmt.Sprintf("CAUTION: This process is owned by %s. You may not have permission to kill it.", username))
	}

	return len(warnings) == 0 || !criticalProcesses[name], warnings
}
