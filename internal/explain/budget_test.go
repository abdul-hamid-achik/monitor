package explain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// bigContext builds a deliberately oversized Context -- long snippet, many
// frames/causes -- so applyBudget's shrink-until-it-fits backstop actually
// has to do real work, not just pass through fixed caps that already
// happened to fit.
func bigContext() *Context {
	lines := make([]string, 0, 41)
	for i := 0; i < 41; i++ {
		lines = append(lines, fmt.Sprintf("    // padding line %d to make this snippet unrealistically long for a real crash site", i))
	}
	frames := make([]FrameEntry, 0, 30)
	for i := 0; i < 30; i++ {
		frames = append(frames, FrameEntry{
			Function: fmt.Sprintf("someModeratelyLongFunctionName%d", i),
			File:     fmt.Sprintf("src/deeply/nested/package/module_%d.go", i),
			Line:     100 + i, InApp: true,
		})
	}
	causes := make([]CauseEntry, 0, 10)
	for i := 0; i < 10; i++ {
		causes = append(causes, CauseEntry{
			Type:    fmt.Sprintf("SomeWrapperErrorType%d", i),
			Culprit: &CauseCulprit{Function: fmt.Sprintf("wrap%d", i), File: fmt.Sprintf("src/wrap_%d.go", i), Line: i + 1},
		})
	}
	return &Context{
		Schema: Schema, Budget: string(BudgetBrief), GeneratedAt: time.Now().UTC(),
		Issue: IssueSummary{
			ID: "ISS-DEADBEEFCAFEBABE", ShortID: "DEAD", Status: "open", Kind: "exception",
			Title:   "a very long title describing a very long and elaborate failure mode that goes on",
			Project: "polyglot", Service: "workload",
		},
		Timeline: Timeline{FirstSeen: time.Now().Add(-time.Hour), LastSeen: time.Now(), Occurrences: 12, TimeSource: "live"},
		Culprit: &CulpritInfo{
			Function: "someModeratelyLongFunctionName0", File: "src/deeply/nested/package/module_0.go", Line: 120,
			Source: "stack", Confidence: "high", Range: &CulpritRange{Start: 80, End: 130, Source: "codemap"},
			Snippet: &Snippet{Start: 80, Lines: lines, Highlight: 120, SHA256: strings.Repeat("a", 64)},
		},
		Causes: causes, Frames: frames,
		Impact:       ImpactInfo{Status: SectionOK, Callers: 3, BlastRadius: 12, Tests: 1, CallGraph: "confirmed"},
		LastTouched:  LastTouched{Status: SectionOK, SHA: "abc1234", Subject: "fix: something", AuthorEmail: "dev@example.com"},
		RelatedNotes: RelatedNotes{Status: SectionSkipped, Items: []RelatedNoteItem{}},
		Degraded:     []Degraded{},
		Next:         []NextAction{{CLI: "monitor issue dead --md", Why: "paste-ready fix context"}},
		Privacy:      Privacy{TextIsUntrusted: true},
	}
}

func TestApplyBudgetBriefFitsTargetSize(t *testing.T) {
	c := bigContext()
	applyBudget(c, BudgetBrief)
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > briefMaxBytes {
		t.Fatalf("brief size = %d bytes, want <= %d", len(data), briefMaxBytes)
	}
	if c.LastTouched.AuthorEmail != "" {
		t.Error("brief must never carry an author email")
	}
}

func TestApplyBudgetStandardFitsTargetSize(t *testing.T) {
	c := bigContext()
	c.Budget = string(BudgetStandard)
	applyBudget(c, BudgetStandard)
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > standardMaxBytes {
		t.Fatalf("standard size = %d bytes, want <= %d", len(data), standardMaxBytes)
	}
	// standard keeps the author email (only brief strips it).
	if c.LastTouched.AuthorEmail == "" {
		t.Error("standard should retain the author email")
	}
}

func TestApplyBudgetFullNeverTruncates(t *testing.T) {
	c := bigContext()
	frameCount, causeCount := len(c.Frames), len(c.Causes)
	applyBudget(c, BudgetFull)
	if len(c.Frames) != frameCount || len(c.Causes) != causeCount {
		t.Fatalf("full budget truncated: frames %d->%d causes %d->%d", frameCount, len(c.Frames), causeCount, len(c.Causes))
	}
	if c.Truncated.Frames != 0 || c.Truncated.Causes != 0 {
		t.Errorf("truncated = %+v, want zero at full budget", c.Truncated)
	}
}

func TestApplyBudgetRecordsTruncatedCounts(t *testing.T) {
	c := bigContext()
	applyBudget(c, BudgetBrief)
	if c.Truncated.Frames == 0 {
		t.Error("expected Truncated.Frames > 0 for a 30-frame context at brief")
	}
}

func TestShrinkSnippetKeepsHighlightCentered(t *testing.T) {
	s := &Snippet{Start: 10, Highlight: 15, Lines: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"}}
	for len(s.Lines) > 1 {
		before := len(s.Lines)
		shrinkSnippet(s)
		if len(s.Lines) != before-1 {
			t.Fatalf("shrinkSnippet did not remove exactly one line: %d -> %d", before, len(s.Lines))
		}
		if s.Highlight < s.Start || s.Highlight > s.Start+len(s.Lines)-1 {
			t.Fatalf("highlight %d fell outside [%d,%d]", s.Highlight, s.Start, s.Start+len(s.Lines)-1)
		}
	}
}

func TestShrinkSnippetSingleLineNoOp(t *testing.T) {
	s := &Snippet{Start: 5, Highlight: 5, Lines: []string{"only"}}
	shrinkSnippet(s)
	if len(s.Lines) != 1 {
		t.Fatalf("single-line snippet was shrunk: %+v", s)
	}
}
