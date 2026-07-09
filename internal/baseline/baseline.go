// Package baseline captures labeled system snapshots and diffs them against
// the live system (or each other) so you can answer "what changed?" — new or
// gone processes, per-process memory deltas, new or gone listening ports, and
// system-metric shifts. Baselines are stored as inspectable JSON files.
package baseline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// validName rejects baseline names that would escape the baseline directory or
// collide with the ".baseline-*" temp prefix. The name becomes a filename
// (<name>.json), so it must be a single path component with no separators,
// no "."/".." and no leading dot.
func validName(name string) error {
	if name == "" {
		return fmt.Errorf("baseline name is empty")
	}
	if name == "." || name == ".." || filepath.Base(name) != name ||
		strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) ||
		strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid baseline name %q (no path separators or leading dot)", name)
	}
	return nil
}

// ProcSnap is a process as recorded in a baseline.
type ProcSnap struct {
	Name       string  `json:"name"`
	Memory     uint64  `json:"memory"`
	CPUPercent float64 `json:"cpu_percent"`
}

// Listener is a listening socket as recorded in a baseline.
type Listener struct {
	Proto   string `json:"proto"`
	Port    uint32 `json:"port"`
	PID     int32  `json:"pid"`
	Process string `json:"process"`
}

// Baseline is a labeled snapshot of the system.
type Baseline struct {
	Name       string             `json:"name"`
	CapturedAt time.Time          `json:"captured_at"`
	CPUUsage   float64            `json:"cpu_usage"`
	MemUsage   float64            `json:"mem_usage"`
	Load1      float64            `json:"load1"`
	Processes  map[int32]ProcSnap `json:"processes"`
	Listeners  []Listener         `json:"listeners"`

	// Swap/disk snapshot added for diff verdicts (sprint 4.5). Baselines
	// saved by older builds unmarshal these as zero; DiskTotal == 0 is the
	// sentinel for "pre-verdict schema" and suppresses swap/disk verdicts
	// (a real capture always has a nonzero disk total).
	SwapUsed  uint64 `json:"swap_used,omitempty"`
	SwapTotal uint64 `json:"swap_total,omitempty"`
	DiskUsed  uint64 `json:"disk_used,omitempty"`  // bytes used on the root partition
	DiskTotal uint64 `json:"disk_total,omitempty"` // bytes total on the root partition
}

// Save writes b to dir/<name>.json atomically (temp file + rename).
func Save(dir string, b *Baseline) error {
	if err := validName(b.Name); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(dir, b.Name+".json")
	tmp, err := os.CreateTemp(dir, ".baseline-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Load reads the named baseline from dir.
func Load(dir, name string) (*Baseline, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return nil, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse baseline %q: %w", name, err)
	}
	return &b, nil
}

// List returns the names of saved baselines in dir, sorted.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if filepath.Ext(n) == ".json" && n[0] != '.' {
			names = append(names, n[:len(n)-len(".json")])
		}
	}
	sort.Strings(names)
	return names, nil
}

// Delete removes the named baseline from dir.
func Delete(dir, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, name+".json"))
}

// ProcChange describes a process that appeared, vanished, or changed memory.
type ProcChange struct {
	PID      int32  `json:"pid"`
	Name     string `json:"name"`
	OldMem   uint64 `json:"old_mem,omitempty"`
	NewMem   uint64 `json:"new_mem,omitempty"`
	MemDelta int64  `json:"mem_delta,omitempty"`
}

// Diff is the result of comparing two baselines (from -> to).
type Diff struct {
	From          string       `json:"from"`
	To            string       `json:"to"`
	CPUDelta      float64      `json:"cpu_delta"`
	MemDelta      float64      `json:"mem_delta"`
	Load1Delta    float64      `json:"load1_delta"`
	NewProcs      []ProcChange `json:"new_procs"`
	GoneProcs     []ProcChange `json:"gone_procs"`
	ChangedProcs  []ProcChange `json:"changed_procs"`
	NewListeners  []Listener   `json:"new_listeners"`
	GoneListeners []Listener   `json:"gone_listeners"`

	// Verdicts interprets the significant deltas. Populated by
	// ComputeVerdicts, not Compute, so Compute stays a pure diff.
	Verdicts []Verdict `json:"verdicts,omitempty"`
}

// Compute diffs old -> new. A process appearing only in new is "new", only in
// old is "gone", and present in both with |Δmem| >= memThreshold is "changed".
// Listeners are keyed by proto/port/pid.
func Compute(old, new *Baseline, memThreshold uint64) Diff {
	d := Diff{
		From:       old.Name,
		To:         new.Name,
		CPUDelta:   new.CPUUsage - old.CPUUsage,
		MemDelta:   new.MemUsage - old.MemUsage,
		Load1Delta: new.Load1 - old.Load1,
	}
	for pid, np := range new.Processes {
		op, ok := old.Processes[pid]
		if !ok {
			d.NewProcs = append(d.NewProcs, ProcChange{PID: pid, Name: np.Name, NewMem: np.Memory})
			continue
		}
		delta := int64(np.Memory) - int64(op.Memory)
		abs := delta
		if abs < 0 {
			abs = -abs
		}
		if uint64(abs) >= memThreshold {
			d.ChangedProcs = append(d.ChangedProcs, ProcChange{
				PID: pid, Name: np.Name, OldMem: op.Memory, NewMem: np.Memory, MemDelta: delta,
			})
		}
	}
	for pid, op := range old.Processes {
		if _, ok := new.Processes[pid]; !ok {
			d.GoneProcs = append(d.GoneProcs, ProcChange{PID: pid, Name: op.Name, OldMem: op.Memory})
		}
	}

	oldL := listenerSet(old.Listeners)
	newL := listenerSet(new.Listeners)
	for k, l := range newL {
		if _, ok := oldL[k]; !ok {
			d.NewListeners = append(d.NewListeners, l)
		}
	}
	for k, l := range oldL {
		if _, ok := newL[k]; !ok {
			d.GoneListeners = append(d.GoneListeners, l)
		}
	}

	sortProcChanges(d.NewProcs)
	sortProcChanges(d.GoneProcs)
	// Changed procs: biggest absolute memory movement first.
	sort.Slice(d.ChangedProcs, func(i, j int) bool { return absI64(d.ChangedProcs[i].MemDelta) > absI64(d.ChangedProcs[j].MemDelta) })
	sortListeners(d.NewListeners)
	sortListeners(d.GoneListeners)
	return d
}

func listenerSet(ls []Listener) map[string]Listener {
	m := make(map[string]Listener, len(ls))
	for _, l := range ls {
		m[fmt.Sprintf("%s/%d/%d", l.Proto, l.Port, l.PID)] = l
	}
	return m
}

func sortProcChanges(p []ProcChange) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].Name != p[j].Name {
			return p[i].Name < p[j].Name
		}
		return p[i].PID < p[j].PID
	})
}

func sortListeners(l []Listener) {
	sort.Slice(l, func(i, j int) bool {
		// Port, then proto/pid/process tiebreakers so listeners that share a
		// port (e.g. tcp+udp, or the same port across PIDs/IP families) order
		// deterministically and diff output stays stable.
		if l[i].Port != l[j].Port {
			return l[i].Port < l[j].Port
		}
		if l[i].Proto != l[j].Proto {
			return l[i].Proto < l[j].Proto
		}
		if l[i].PID != l[j].PID {
			return l[i].PID < l[j].PID
		}
		return l[i].Process < l[j].Process
	})
}

func absI64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
