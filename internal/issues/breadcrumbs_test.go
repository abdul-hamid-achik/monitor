package issues

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBreadcrumbsAreBoundedAndStored(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "issues.veclite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var crumbs []Breadcrumb
	for i := 0; i < MaxBreadcrumbs+5; i++ {
		crumbs = append(crumbs, Breadcrumb{Timestamp: time.Now(), Category: "db", Message: "query " + string(rune('a'+i%26)), Level: "INFO"})
	}
	crumbs = append(crumbs, Breadcrumb{Message: "   "}) // empty: dropped
	crumbs = append(crumbs, Breadcrumb{Message: strings.Repeat("x", 1000)})
	_, occ, err := store.UpsertOccurrence(OccurrenceInput{
		Project: "demo", Kind: KindException, Title: "boom", Symbols: []string{"f"}, Severity: "error",
		Breadcrumbs: crumbs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(occ.Breadcrumbs) != MaxBreadcrumbs {
		t.Fatalf("kept %d breadcrumbs, want %d", len(occ.Breadcrumbs), MaxBreadcrumbs)
	}
	last := occ.Breadcrumbs[len(occ.Breadcrumbs)-1]
	if len([]rune(last.Message)) != maxBreadcrumbMessage {
		t.Fatalf("long message kept %d runes, want %d", len([]rune(last.Message)), maxBreadcrumbMessage)
	}
	if occ.Breadcrumbs[0].Level != "info" {
		t.Fatalf("level not normalized: %q", occ.Breadcrumbs[0].Level)
	}
	stored, err := store.Occurrences(occ.IssueID, 0)
	if err != nil || len(stored) != 1 || len(stored[0].Breadcrumbs) != MaxBreadcrumbs {
		t.Fatalf("round trip lost breadcrumbs: %v %+v", err, stored)
	}
}

func TestTagsAreBoundedAndStored(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "issues.veclite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tags := map[string]string{"empty": "  ", " ": "no key", "long": strings.Repeat("v", 500)}
	for i := 0; i < MaxTags+10; i++ {
		tags[fmt.Sprintf("k%03d", i)] = "v"
	}
	_, occ, err := store.UpsertOccurrence(OccurrenceInput{
		Project: "demo", Kind: KindException, Title: "boom", Symbols: []string{"f"}, Severity: "error",
		Tags: tags,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(occ.Tags) != MaxTags {
		t.Fatalf("kept %d tags, want %d", len(occ.Tags), MaxTags)
	}
	if _, ok := occ.Tags["empty"]; ok {
		t.Fatal("empty value kept")
	}
	if v := occ.Tags["long"]; v != "" && len([]rune(v)) != maxTagValueLen {
		t.Fatalf("long value kept %d runes", len([]rune(v)))
	}
	stored, err := store.Occurrences(occ.IssueID, 0)
	if err != nil || len(stored) != 1 || len(stored[0].Tags) != MaxTags {
		t.Fatalf("round trip lost tags: %v %+v", err, stored)
	}

	_, plain, err := store.UpsertOccurrence(OccurrenceInput{
		Project: "demo", Kind: KindException, Title: "other", Symbols: []string{"g"}, Severity: "error",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Tags != nil || plain.Breadcrumbs != nil {
		t.Fatalf("an occurrence without SDK context grew tags/breadcrumbs: %+v %+v", plain.Tags, plain.Breadcrumbs)
	}
}
