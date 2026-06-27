package baseline

import (
	"testing"
	"time"
)

func TestSaveLoadListDelete(t *testing.T) {
	dir := t.TempDir()
	b := &Baseline{
		Name:       "pre-deploy",
		CapturedAt: time.Now().Truncate(time.Second),
		CPUUsage:   12.5,
		MemUsage:   40,
		Load1:      1.2,
		Processes:  map[int32]ProcSnap{100: {Name: "node", Memory: 1000}},
		Listeners:  []Listener{{Proto: "tcp", Port: 8080, PID: 100, Process: "node"}},
	}
	if err := Save(dir, b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	names, err := List(dir)
	if err != nil || len(names) != 1 || names[0] != "pre-deploy" {
		t.Fatalf("List = %v, %v", names, err)
	}
	got, err := Load(dir, "pre-deploy")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CPUUsage != 12.5 || got.Processes[100].Name != "node" || len(got.Listeners) != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if err := Delete(dir, "pre-deploy"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if names, _ := List(dir); len(names) != 0 {
		t.Errorf("after delete, List = %v", names)
	}
}

func TestCompute(t *testing.T) {
	old := &Baseline{
		Name: "old", CPUUsage: 10, MemUsage: 30, Load1: 1,
		Processes: map[int32]ProcSnap{
			1: {Name: "keep", Memory: 1000},
			2: {Name: "grow", Memory: 1000},
			3: {Name: "gone", Memory: 500},
		},
		Listeners: []Listener{{Proto: "tcp", Port: 5432, PID: 1, Process: "pg"}},
	}
	newB := &Baseline{
		Name: "new", CPUUsage: 25, MemUsage: 55, Load1: 2.5,
		Processes: map[int32]ProcSnap{
			1: {Name: "keep", Memory: 1010},  // +10, below threshold
			2: {Name: "grow", Memory: 200000}, // big growth
			4: {Name: "new", Memory: 4000},    // appeared
		},
		Listeners: []Listener{
			{Proto: "tcp", Port: 5432, PID: 1, Process: "pg"}, // unchanged
			{Proto: "tcp", Port: 6060, PID: 4, Process: "new"}, // new
		},
	}
	d := Compute(old, newB, 50_000)

	if d.CPUDelta != 15 || d.MemDelta != 25 || d.Load1Delta != 1.5 {
		t.Errorf("metric deltas = %+v", d)
	}
	if len(d.NewProcs) != 1 || d.NewProcs[0].PID != 4 {
		t.Errorf("new procs = %+v", d.NewProcs)
	}
	if len(d.GoneProcs) != 1 || d.GoneProcs[0].PID != 3 {
		t.Errorf("gone procs = %+v", d.GoneProcs)
	}
	// pid 1 (+10) is below threshold; pid 2 (+199000) is above.
	if len(d.ChangedProcs) != 1 || d.ChangedProcs[0].PID != 2 || d.ChangedProcs[0].MemDelta != 199000 {
		t.Errorf("changed procs = %+v", d.ChangedProcs)
	}
	if len(d.NewListeners) != 1 || d.NewListeners[0].Port != 6060 {
		t.Errorf("new listeners = %+v", d.NewListeners)
	}
	if len(d.GoneListeners) != 0 {
		t.Errorf("gone listeners = %+v", d.GoneListeners)
	}
}
