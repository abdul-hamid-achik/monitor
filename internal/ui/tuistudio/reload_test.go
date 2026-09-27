package tuistudio

import "testing"

// TestReloaderRejectsInactiveApp mirrors internal/ui/studio's
// TestProgramReloaderRejectsInactiveStudio: a Reloader that was never
// attached to a running app must report an error, not silently do nothing.
func TestReloaderRejectsInactiveApp(t *testing.T) {
	if err := NewReloader().Reload(); err == nil {
		t.Fatal("expected an error from an inactive Reloader")
	}
}

// TestReloaderRefreshesTheAttachedStudio confirms that Reload() (what
// `monitor reload` triggers over --reload-server) takes an immediate
// sample through the same refreshNow path "r" uses.
func TestReloaderRefreshesTheAttachedStudio(t *testing.T) {
	s := newFixtureStudio(t)
	r := NewReloader()
	r.attach(s)
	defer r.detach(s)

	before := s.last
	if err := r.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !s.last.LastUpdate.After(before.LastUpdate) {
		t.Fatalf("expected Reload to take a new sample: before=%v after=%v", before.LastUpdate, s.last.LastUpdate)
	}
}
