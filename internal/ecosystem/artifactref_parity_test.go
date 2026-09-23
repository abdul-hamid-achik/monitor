package ecosystem

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileCheapCanonicalMonitorIncidentFixture is the exact byte-for-byte JSON
// produced by file.cheap's own canonical implementation
// (internal/artifactref.NewLocal in ~/projects/file.cheap), for:
//
//	NewLocal(fixtureID, "monitor.incident", LocalOptions{
//		Kind: "monitor.incident",
//		Producer: Producer{
//			Tool: "monitor", NativeSchema: "urn:monitor.dev:incident:v1",
//			NativeID: "incident_01", Entrypoint: "manifest.json",
//		},
//	})
//
// captured live (2026-09-22) via a throwaway test run against file.cheap's
// own repo — never committed there — so this is copied fixture data, not a
// hand-maintained guess at the shape. It is also exactly the
// "monitor.incident"/producer shape Chalupa's chalupa-ci.py
// validate_monitor_incident_ref and contract.ts's localArtifactRefV1Schema
// both require.
const fileCheapCanonicalMonitorIncidentFixture = `{"$schema":"urn:filecheap.dev:artifact-ref:v1","version":1,"provider":"fcheap-local","uri":"fcheap://stash/demo_20260723_184500.123456789_0123456789abcdef01234567","artifact_id":"demo_20260723_184500.123456789_0123456789abcdef01234567","kind":"monitor.incident","producer":{"tool":"monitor","native_schema":"urn:monitor.dev:incident:v1","native_id":"incident_01","entrypoint":"manifest.json"}}`

// TestArtifactRefV1ParityWithFileCheapCanonicalShape decodes file.cheap's own
// canonical output byte-for-byte and asserts monitor's independently
// hand-duplicated ArtifactRefV1 accepts it, round-trips it losslessly, and
// re-marshals $schema present (not dropped) — the actual bug this quick win
// fixes (monitor previously tagged $schema `,omitempty`, while Chalupa's
// validator requires it present and exact).
func TestArtifactRefV1ParityWithFileCheapCanonicalShape(t *testing.T) {
	ref, err := decodeArtifactRef([]byte(fileCheapCanonicalMonitorIncidentFixture))
	if err != nil {
		t.Fatalf("decode file.cheap's canonical fixture: %v", err)
	}
	if err := ref.Validate(); err != nil {
		t.Fatalf("file.cheap's canonical fixture failed monitor's Validate(): %v", err)
	}
	if ref.Schema != artifactRefSchema {
		t.Errorf("Schema = %q, want %q", ref.Schema, artifactRefSchema)
	}
	if ref.Kind != "monitor.incident" || ref.Producer == nil || ref.Producer.Tool != "monitor" {
		t.Fatalf("decoded ref = %+v", ref)
	}

	remarshaled, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !strings.Contains(string(remarshaled), `"$schema":"`+artifactRefSchema+`"`) {
		t.Errorf("re-marshaled ref dropped/omitted $schema: %s", remarshaled)
	}

	var roundTrip map[string]any
	if err := json.Unmarshal(remarshaled, &roundTrip); err != nil {
		t.Fatalf("unmarshal re-marshaled ref: %v", err)
	}
	if _, present := roundTrip["$schema"]; !present {
		t.Errorf("re-marshaled ref is missing the $schema key entirely: %s", remarshaled)
	}
}

// TestArtifactRefV1RejectsFileCheapsInvalidShapes mirrors the negative cases
// file.cheap's own artifact_ref_test.go / Chalupa's chalupa-ci.py
// validate_monitor_incident_ref both reject, confirming monitor's
// independently-duplicated Validate() agrees on rejection, not just
// acceptance.
func TestArtifactRefV1RejectsFileCheapsInvalidShapes(t *testing.T) {
	valid := map[string]any{}
	if err := json.Unmarshal([]byte(fileCheapCanonicalMonitorIncidentFixture), &valid); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing $schema", func(m map[string]any) { delete(m, "$schema") }},
		{"wrong $schema", func(m map[string]any) { m["$schema"] = "urn:filecheap.dev:artifact-ref:v2" }},
		{"empty $schema", func(m map[string]any) { m["$schema"] = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := map[string]any{}
			for k, v := range valid {
				mutated[k] = v
			}
			tt.mutate(mutated)
			data, err := json.Marshal(mutated)
			if err != nil {
				t.Fatal(err)
			}
			ref, decodeErr := decodeArtifactRef(data)
			if decodeErr == nil {
				if err := ref.Validate(); err == nil {
					t.Fatalf("%s: expected rejection, got a validated ref %+v", tt.name, ref)
				}
			}
		})
	}
}

// TestNewLocalArtifactRefMatchesFileCheapShape asserts monitor's own
// constructor produces the identical shape to file.cheap's NewLocal for the
// same inputs (field-for-field, not just "passes Validate()").
func TestNewLocalArtifactRefMatchesFileCheapShape(t *testing.T) {
	const fixtureID = "demo_20260723_184500.123456789_0123456789abcdef01234567"
	ref, err := NewLocalArtifactRef(fixtureID, "monitor.incident", &ArtifactProducer{
		Tool:         "monitor",
		NativeSchema: "urn:monitor.dev:incident:v1",
		NativeID:     "incident_01",
		Entrypoint:   "manifest.json",
	})
	if err != nil {
		t.Fatalf("NewLocalArtifactRef: %v", err)
	}
	got, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	var gotMap, wantMap map[string]any
	if err := json.Unmarshal(got, &gotMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(fileCheapCanonicalMonitorIncidentFixture), &wantMap); err != nil {
		t.Fatal(err)
	}
	if len(gotMap) != len(wantMap) {
		t.Fatalf("field count = %d, want %d\n got: %s\nwant: %s", len(gotMap), len(wantMap), got, fileCheapCanonicalMonitorIncidentFixture)
	}
	for k, want := range wantMap {
		gotVal, ok := gotMap[k]
		if !ok {
			t.Errorf("missing field %q", k)
			continue
		}
		gotJSON, _ := json.Marshal(gotVal)
		wantJSON, _ := json.Marshal(want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("field %q = %s, want %s", k, gotJSON, wantJSON)
		}
	}
}

// artifactRefCorpusDir is the pinned file.cheap conformance corpus subset
// (fcheap-local provider only); see testdata/artifact-ref/v1/README.md for
// the pinned commit, checksums, and why cloud/link fixtures are excluded.
const artifactRefCorpusDir = "testdata/artifact-ref/v1"

// TestArtifactRefV1CorpusChecksumsMatch guards the copied fixtures against
// silent drift: editing a fixture without updating CHECKSUMS.sha256 (in a
// reviewed diff) fails the build instead of quietly changing what the
// parity tests below actually exercise.
func TestArtifactRefV1CorpusChecksumsMatch(t *testing.T) {
	manifestPath := filepath.Join(artifactRefCorpusDir, "CHECKSUMS.sha256")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(manifest)), "\n")
	if len(lines) == 0 {
		t.Fatalf("%s is empty", manifestPath)
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed line in %s: %q", manifestPath, line)
		}
		wantHash, rel := fields[0], fields[1]
		data, err := os.ReadFile(filepath.Join(artifactRefCorpusDir, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != wantHash {
			t.Errorf("%s hash changed: got %s, want %s (update CHECKSUMS.sha256 deliberately if this fixture change is intentional)", rel, got, wantHash)
		}
	}
}

func artifactRefCorpusFiles(t *testing.T, sub string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(artifactRefCorpusDir, sub))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("no fixtures found under %s/%s", artifactRefCorpusDir, sub)
	}
	return names
}

// TestArtifactRefV1CorpusAcceptsValidFcheapLocalFixtures runs every valid
// fcheap-local fixture from file.cheap's own conformance corpus through
// monitor's independently-duplicated decodeArtifactRef + Validate, per the
// roadmap's done-when: a real parity test against that corpus, not just one
// hand-generated fixture.
func TestArtifactRefV1CorpusAcceptsValidFcheapLocalFixtures(t *testing.T) {
	for _, name := range artifactRefCorpusFiles(t, "valid") {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(artifactRefCorpusDir, "valid", name))
			if err != nil {
				t.Fatal(err)
			}
			ref, err := decodeArtifactRef(data)
			if err != nil {
				t.Fatalf("decodeArtifactRef: %v", err)
			}
			if err := ref.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// TestArtifactRefV1CorpusRejectsInvalidFcheapLocalFixtures mirrors the
// negative half of the same corpus: each fixture must be rejected either at
// decode (e.g. an unknown field under DisallowUnknownFields) or at Validate.
func TestArtifactRefV1CorpusRejectsInvalidFcheapLocalFixtures(t *testing.T) {
	for _, name := range artifactRefCorpusFiles(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(artifactRefCorpusDir, "invalid", name))
			if err != nil {
				t.Fatal(err)
			}
			ref, decodeErr := decodeArtifactRef(data)
			if decodeErr != nil {
				return // rejected at decode: still a pass, just earlier
			}
			if err := ref.Validate(); err == nil {
				t.Fatalf("expected rejection, got a validated ref %+v", ref)
			}
		})
	}
}
