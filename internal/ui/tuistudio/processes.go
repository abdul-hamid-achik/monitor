package tuistudio

import (
	"fmt"
	"sort"
	"strings"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/tuimark"
)

// processHandlers is the Processes-tab slice of the action table: table
// navigation/marks, sort, the "/" filter, and the kill and detail flows.
// The safety gate (never sending a signal to a protected or system-owned
// process) is enforced once, in killConfirm, from data
// internal/kill.CheckSafety derived — never re-decided ad hoc here.
func (s *studio) processHandlers() map[string]tuimark.Handler {
	hs := map[string]tuimark.Handler{}
	on := func(name string, h tuimark.Handler) { hs[name] = h }

	on("cursor_moved", func(ev tuimark.Event) error {
		s.trackCursor(ev)
		return nil
	})
	on("marks_changed", func(ev tuimark.Event) error {
		arr, _ := ev.Value.([]any)
		s.mu.Lock()
		s.marked = map[int32]bool{}
		for _, v := range arr {
			if f, ok := v.(float64); ok {
				s.marked[int32(f)] = true
			}
		}
		s.mu.Unlock()
		return nil
	})
	on("sort_cpu", func(tuimark.Event) error { return s.sortBy("cpu") })
	on("sort_mem", func(tuimark.Event) error { return s.sortBy("memory") })

	on("filter_open", func(tuimark.Event) error {
		s.mu.Lock()
		s.savedQuery = s.query
		s.mu.Unlock()
		return joinErrs([]error{s.ui.Set("filter_open", true), s.ui.Set("@focus", "#filter")})
	})
	on("filter", func(ev tuimark.Event) error {
		q, _ := ev.Value.(string)
		s.mu.Lock()
		s.query = q
		s.mu.Unlock()
		return s.publishProcs()
	})
	on("filter_apply", func(tuimark.Event) error { return s.ui.Set("@focus", "#procs") })
	on("filter_cancel", func(tuimark.Event) error {
		s.mu.Lock()
		s.query = s.savedQuery
		q := s.query
		s.mu.Unlock()
		return joinErrs([]error{
			s.ui.Set("query", q), s.publishProcs(), s.ui.Set("filter_open", false), s.ui.Set("@focus", "#procs"),
		})
	})

	on("kill_ask", func(ev tuimark.Event) error { return s.killAsk(ev, false) })
	on("kill_force_ask", func(ev tuimark.Event) error { return s.killAsk(ev, true) })
	on("kill_confirm", func(tuimark.Event) error { return s.killConfirm() })
	on("kill_cancel", func(tuimark.Event) error {
		s.mu.Lock()
		s.showKill = false
		s.mu.Unlock()
		return s.ui.Set("kill_open", false)
	})

	on("diagnose", func(ev tuimark.Event) error {
		s.trackCursor(ev)
		s.mu.Lock()
		s.detailPID = s.cursorPID
		s.detailOpen = true
		s.mu.Unlock()
		return joinErrs([]error{s.publishDetail(), s.ui.Set("detail_open", true)})
	})
	on("detail_refresh", func(tuimark.Event) error { return s.publishDetail() })
	on("detail_close", func(tuimark.Event) error {
		s.mu.Lock()
		s.detailOpen = false
		s.mu.Unlock()
		return s.ui.Set("detail_open", false)
	})
	return hs
}

// trackCursor takes the process table's cursor row pid from a keymap
// event's keys (SPEC §8.2: a table's on:select/keymap events carry the
// cursor row's key under its each-alias, "p" for key="p.pid").
func (s *studio) trackCursor(ev tuimark.Event) {
	if pid, ok := ev.Keys["p"].(float64); ok {
		s.mu.Lock()
		s.cursorPID = int32(pid)
		s.mu.Unlock()
	}
}

// sortBy re-sorts the process table by key; pressing the same key again
// reverses direction, matching the Bubble Tea studio's 'c'/'m' behavior.
// The header arrow is derived data, not document logic.
func (s *studio) sortBy(key string) error {
	s.mu.Lock()
	if s.lastSort == key {
		s.sortAsc = !s.sortAsc
	} else {
		s.sortKey, s.sortAsc = key, false
	}
	s.lastSort = key
	arrow := "▼"
	if s.sortAsc {
		arrow = "▲"
	}
	h := map[string]any{"name": "NAME", "cpu": "CPU%", "mem": "MEM"}
	h[map[string]string{"cpu": "cpu", "memory": "mem"}[key]] = h[map[string]string{"cpu": "cpu", "memory": "mem"}[key]].(string) + arrow
	s.mu.Unlock()
	return joinErrs([]error{s.ui.Set("h", h), s.publishProcs()})
}

// filteredProcesses applies the system-process visibility setting and the
// "/" filter's case-insensitive substring over name/pid/user, matching the
// Bubble Tea studio's filteredProcesses.
func (s *studio) filteredProcesses() []collector.ProcessInfo {
	s.mu.Lock()
	procs := s.last.Processes
	query := strings.ToLower(s.query)
	showSys := s.settings != nil && s.settings.ShowSystemProcesses
	s.mu.Unlock()

	out := make([]collector.ProcessInfo, 0, len(procs))
	for _, p := range procs {
		if !showSys && p.IsSystem {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(fmt.Sprintf("%s %d %s", p.Name, p.PID, p.User))
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// sortedProcesses sorts filteredProcesses by the current sort key/direction
// and caps the result at settings.MaxProcesses, matching the Bubble Tea
// studio's currentProcessView.
func (s *studio) sortedProcesses() []collector.ProcessInfo {
	procs := s.filteredProcesses()
	s.mu.Lock()
	key, asc := s.sortKey, s.sortAsc
	max := config.DefaultSettings.MaxProcesses
	if s.settings != nil && s.settings.MaxProcesses > 0 {
		max = s.settings.MaxProcesses
	}
	s.mu.Unlock()

	less := func(a, b collector.ProcessInfo) bool {
		if key == "memory" {
			return a.Memory > b.Memory
		}
		return a.CPUPercent > b.CPUPercent
	}
	sort.SliceStable(procs, func(i, j int) bool {
		if asc {
			return less(procs[j], procs[i])
		}
		return less(procs[i], procs[j])
	})
	if max > 0 && len(procs) > max {
		procs = procs[:max]
	}
	return procs
}

func (s *studio) publishProcs() error {
	procs := s.sortedProcesses()
	s.mu.Lock()
	marked := s.marked
	s.mu.Unlock()
	rows := make([]any, len(procs))
	for i, p := range procs {
		rows[i] = map[string]any{
			"pid": float64(p.PID), "name": p.Name, "cpu": fmt.Sprintf("%.1f", p.CPUPercent),
			"mem": collector.FormatBytes(p.Memory), "io": collector.FormatBytes(p.IOReadBytes + p.IOWriteBytes),
			"threads": float64(p.Threads), "user": p.User, "protected": p.IsProtected || p.IsSystem,
			"cpu_hot": p.CPUPercent >= 50,
		}
	}
	markedArr := make([]any, 0, len(marked))
	for pid := range marked {
		if _, ok := s.findProcess(pid); ok {
			markedArr = append(markedArr, float64(pid))
		}
	}
	return joinErrs([]error{s.ui.Set("procs", rows), s.ui.Set("marked_pids", markedArr)})
}

func (s *studio) findProcess(pid int32) (collector.ProcessInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.last.Processes {
		if p.PID == pid {
			return p, true
		}
	}
	return collector.ProcessInfo{}, false
}

// killAsk fills the confirmation from the marked processes, else the
// cursor row, using internal/kill.CheckSafety (or its fixture
// equivalent) so a spec exercises the exact same protected/system
// classification the CLI and MCP gate use.
func (s *studio) killAsk(ev tuimark.Event, force bool) error {
	s.trackCursor(ev)
	s.mu.Lock()
	var pids []int32
	for pid := range s.marked {
		pids = append(pids, pid)
	}
	if len(pids) == 0 {
		pids = []int32{s.cursorPID}
	}
	s.mu.Unlock()

	conf := s.kill.CheckSafety(pids)
	if len(conf.Processes) == 0 {
		return nil
	}
	rows := make([]any, len(conf.Processes))
	for i, p := range conf.Processes {
		state, note := "[ELIGIBLE]", ""
		blocked := p.IsProtected || p.IsSystem
		if p.IsProtected {
			state, note = "[BLOCKED ]", "protected"
		} else if p.IsSystem {
			state, note = "[BLOCKED ]", "owned by "+p.User
		}
		rows[i] = map[string]any{"pid": float64(p.PID), "state": state, "name": p.Name, "note": note, "blocked": blocked}
	}
	s.mu.Lock()
	s.killConf = conf
	s.forceKill = force
	s.showKill = true
	s.mu.Unlock()
	return joinErrs([]error{s.ui.Set("kill_rows", rows), s.ui.Set("kill_force", force), s.ui.Set("kill_open", true)})
}

// killConfirm sends the signal to every eligible (non-protected,
// non-system) process in the frozen confirmation, verifying each one
// (which can block up to ~2s per pid: internal/kill.KillVerified polls),
// so it runs off the handler goroutine. A protected or system process in
// the confirmation is never signaled, matching the CLI/MCP gate. The
// selection is cleared unconditionally on confirm (spared or not),
// matching the Bubble Tea studio's handleKillConfirmKeys.
func (s *studio) killConfirm() error {
	s.mu.Lock()
	conf := s.killConf
	force := s.forceKill
	s.showKill = false
	s.marked = map[int32]bool{}
	s.mu.Unlock()

	var attempted []int32
	for _, p := range conf.Processes {
		if p.IsProtected || p.IsSystem {
			continue
		}
		attempted = append(attempted, p.PID)
	}

	errs := []error{s.ui.Set("kill_open", false), s.publishProcs()}
	if len(attempted) > 0 {
		go func() {
			for _, pid := range attempted {
				_, _ = s.kill.Verify(pid, force)
			}
			_ = s.refreshNow()
		}()
	}
	return joinErrs(errs)
}

// publishDetail writes the read-only, PID-pinned diagnostic panel; once
// the pinned pid leaves the snapshot (it exited, or was terminated), it
// reports that instead of the last values it had.
func (s *studio) publishDetail() error {
	s.mu.Lock()
	pid := s.detailPID
	s.mu.Unlock()
	if pid < 0 {
		return nil
	}
	p, ok := s.findProcess(pid)
	d := map[string]any{"title": fmt.Sprintf("process detail · pid %d", pid), "present": ok, "rows": []any{}}
	if ok {
		safety := "USER · termination requires confirmation"
		switch {
		case p.IsProtected:
			safety = "PROTECTED · termination is blocked"
		case p.IsSystem:
			safety = "SYSTEM · termination is blocked by policy"
		}
		d["title"] = fmt.Sprintf("process detail · %s · pid %d", p.Name, pid)
		rows := [][2]string{
			{"pid", fmt.Sprintf("%d", p.PID)}, {"name", p.Name}, {"user", p.User}, {"status", p.Status},
			{"cpu", fmt.Sprintf("%.1f%%", p.CPUPercent)}, {"memory", collector.FormatBytes(p.Memory)},
			{"mem share", fmt.Sprintf("%.1f%%", p.MemoryPercent)}, {"threads", fmt.Sprintf("%d", p.Threads)},
			{"parent", fmt.Sprintf("%d", p.Parent)},
			{"i/o read", collector.FormatBytes(p.IOReadBytes)}, {"i/o write", collector.FormatBytes(p.IOWriteBytes)},
			{"protection", safety},
		}
		rowsOut := make([]any, len(rows))
		for i, r := range rows {
			rowsOut[i] = map[string]any{"k": r[0], "label": r[0], "value": r[1]}
		}
		d["rows"] = rowsOut
	}
	return s.ui.Set("detail", d)
}
