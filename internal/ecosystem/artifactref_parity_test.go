package ecosystem

import (
	"encoding/json"
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
