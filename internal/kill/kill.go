// Package kill safely terminates processes with protection against critical
// system processes and provides pre-flight safety checks.
package kill

import (
	"fmt"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// Confirmation is the result of a safety check. JSON tags keep it snake_case,
// matching the embedded collector.ProcessInfo and the rest of the JSON surface
// (it is serialized verbatim by both `monitor kill --json` and MCP monitor_kill).
type Confirmation struct {
	Processes      []collector.ProcessInfo `json:"processes"`
	HasProtected   bool                    `json:"has_protected"`
	HasSystem      bool                    `json:"has_system"`
	SafetyWarnings []string                `json:"safety_warnings"`
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
		isProtected := collector.IsProtectedProcess(name, pid)
		isSystem := collector.IsSystemProcess(user)
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

// Outcome is the observed post-signal state of the target process.
type Outcome string

const (
	OutcomeTerminated   Outcome = "terminated"
	OutcomeStillRunning Outcome = "still_running"
	OutcomeUnknown      Outcome = "unknown"
)

// Result is the verified receipt for one kill attempt. snake_case JSON to
// match Confirmation and the rest of the CLI/MCP surface.
type Result struct {
	PID        int32   `json:"pid"`
	Signal     string  `json:"signal"` // "SIGTERM" | "SIGKILL"
	Outcome    Outcome `json:"outcome"`
	WaitedMs   int64   `json:"waited_ms"`
	NextAction string  `json:"next_action,omitempty"`
}

// Poll knobs as package vars so tests can shrink them (~2s total per spec).
var (
	verifyTimeout = 2 * time.Second
	pollInterval  = 100 * time.Millisecond
)

// KillVerified sends the signal like Kill, then polls (pollInterval ticks,
// up to verifyTimeout total) to observe whether the process actually exited.
// It NEVER escalates to SIGKILL itself: a surviving process is reported as
// still_running with a next_action suggesting force, and the caller decides.
func KillVerified(pid int32, force bool) (Result, error) {
	res := Result{PID: pid, Signal: "SIGTERM", Outcome: OutcomeUnknown}
	if force {
		res.Signal = "SIGKILL"
	}
	if err := Kill(pid, force); err != nil {
		return res, err
	}
	start := time.Now()
	deadline := start.Add(verifyTimeout)
	lastCheckOK := true
	for {
		gone, ok := processGone(pid)
		lastCheckOK = ok
		if ok && gone {
			res.Outcome = OutcomeTerminated
			res.WaitedMs = time.Since(start).Milliseconds()
			return res, nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(pollInterval)
	}
	res.WaitedMs = time.Since(start).Milliseconds()
	if !lastCheckOK {
		res.Outcome = OutcomeUnknown
		res.NextAction = fmt.Sprintf("could not verify process state; check manually with 'ps -p %d' or 'monitor process %d'", pid, pid)
		return res, nil
	}
	res.Outcome = OutcomeStillRunning
	if force {
		res.NextAction = fmt.Sprintf("process survived SIGKILL (likely uninterruptible or zombie state); inspect with 'monitor process %d'", pid)
	} else {
		res.NextAction = "process ignored SIGTERM; if termination is required, retry with force (CLI: --force, MCP: force:true) to send SIGKILL"
	}
	return res, nil
}

// processGone reports whether pid no longer exists. A zombie (dead, awaiting
// parent reap) counts as gone — the signal did its job. ok=false means the
// check itself failed and the state is unknowable.
func processGone(pid int32) (gone bool, ok bool) {
	exists, err := process.PidExists(pid)
	if err != nil {
		return false, false
	}
	if !exists {
		return true, true
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return true, true // raced away between the two calls
	}
	statuses, err := p.Status()
	if err != nil {
		return false, true // alive but unreadable: running as far as we know
	}
	for _, st := range statuses {
		if st == process.Zombie {
			return true, true
		}
	}
	return false, true
}
