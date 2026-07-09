package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestWebhookPostsAlertJSON(t *testing.T) {
	got := make(chan collector.Alert, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		var a collector.Alert
		_ = json.NewDecoder(r.Body).Decode(&a)
		got <- a
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	alert := collector.Alert{Severity: "warning", Rule: "rss_growth", PID: 4821, Process: "node", Detail: "suspected memory leak"}
	if err := Webhook(context.Background(), srv.Client(), srv.URL, alert, nil); err != nil {
		t.Fatalf("Webhook: %v", err)
	}
	select {
	case a := <-got:
		if a.Rule != "rss_growth" || a.PID != 4821 {
			t.Errorf("server received %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook never reached the server")
	}
}

func TestWebhookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := Webhook(context.Background(), srv.Client(), srv.URL, collector.Alert{Rule: "x"}, nil); err == nil {
		t.Error("expected an error on a 500 response")
	}
}

func TestWebhookIncludesDiagnosis(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		got <- m
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	alert := collector.Alert{Severity: "warning", Rule: "rss_growth", PID: 4821, Process: "node", Detail: "suspected memory leak"}
	diag := &Diagnosis{Summary: "s", Evidence: []string{"e1"}, Confidence: "high", NextActions: []string{"n1"}}
	if err := Webhook(context.Background(), srv.Client(), srv.URL, alert, diag); err != nil {
		t.Fatalf("Webhook: %v", err)
	}
	select {
	case m := <-got:
		if m["rule"] != "rss_growth" {
			t.Errorf("rule = %v, want rss_growth (schema must stay unchanged)", m["rule"])
		}
		d, ok := m["diagnosis"].(map[string]any)
		if !ok {
			t.Fatalf("diagnosis missing or wrong shape: %v", m)
		}
		if d["confidence"] != "high" {
			t.Errorf("diagnosis.confidence = %v, want high", d["confidence"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook never reached the server")
	}
}

// TestWebhookFallsBackToAlertDiagnosisWhenDNil locks in the fix for the
// AlertPayload shadowing footgun: AlertPayload.Diagnosis (depth 0) shadows
// the embedded collector.Alert.Diagnosis (depth 1) under encoding/json's
// rules, so a caller that sets Alert.Diagnosis but passes d == nil must
// still see "diagnosis" in the payload — Webhook derives it from
// a.Diagnosis in that case instead of silently dropping it.
func TestWebhookFallsBackToAlertDiagnosisWhenDNil(t *testing.T) {
	got := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got <- raw
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	alert := collector.Alert{
		Severity: "warning", Rule: "rss_growth", PID: 4821, Process: "node", Detail: "suspected memory leak",
		Diagnosis: &collector.Diagnosis{
			Summary:     "RSS grew 42%/10min",
			Evidence:    []string{"slope 3.2MB/min"},
			Confidence:  "high",
			NextActions: []string{"monitor investigate 4821"},
		},
	}
	if err := Webhook(context.Background(), srv.Client(), srv.URL, alert, nil); err != nil {
		t.Fatalf("Webhook: %v", err)
	}
	select {
	case raw := <-got:
		// (b) exactly one "diagnosis" key in the marshaled JSON — no
		// duplicate from the embedded collector.Alert.Diagnosis field.
		if n := strings.Count(string(raw), `"diagnosis":`); n != 1 {
			t.Fatalf(`want exactly one "diagnosis" key, got %d: %s`, n, raw)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// (a) the diagnosis key is still emitted, derived from a.Diagnosis.
		d, ok := m["diagnosis"].(map[string]any)
		if !ok {
			t.Fatalf("diagnosis missing or wrong shape: %v", m)
		}
		if d["summary"] != "RSS grew 42%/10min" || d["confidence"] != "high" {
			t.Errorf("diagnosis = %v, want it to mirror alert.Diagnosis", d)
		}
		if m["rule"] != "rss_growth" || m["pid"] != float64(4821) {
			t.Errorf("top-level keys changed unexpectedly: %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook never reached the server")
	}
}

func TestWebhookOmitsDiagnosisWhenNil(t *testing.T) {
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		got <- m
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Webhook(context.Background(), srv.Client(), srv.URL, collector.Alert{Rule: "x"}, nil); err != nil {
		t.Fatalf("Webhook: %v", err)
	}
	select {
	case m := <-got:
		if _, ok := m["diagnosis"]; ok {
			t.Errorf("diagnosis key should be absent when nil; got %v", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook never reached the server")
	}
}

func TestDesktopText(t *testing.T) {
	base := collector.Alert{Rule: "rss_growth", Detail: "suspected memory leak", Process: "node", PID: 42}
	tests := []struct {
		name      string
		alert     collector.Alert
		diag      *Diagnosis
		wantTitle string
		wantMsg   string
	}{
		{
			name:      "nil diagnosis falls back to Detail",
			alert:     base,
			diag:      nil,
			wantTitle: "monitor: rss_growth",
			wantMsg:   "suspected memory leak (node pid 42)",
		},
		{
			name:      "diagnosis summary replaces detail, confidence appended",
			alert:     base,
			diag:      &Diagnosis{Summary: "RSS grew 42%/10min", Confidence: "high"},
			wantTitle: "monitor: rss_growth",
			wantMsg:   "RSS grew 42%/10min (node pid 42) [confidence: high]",
		},
		{
			name:      "empty summary falls back to detail, confidence still appended",
			alert:     base,
			diag:      &Diagnosis{Confidence: "low"},
			wantTitle: "monitor: rss_growth",
			wantMsg:   "suspected memory leak (node pid 42) [confidence: low]",
		},
		{
			name:      "no process omits pid suffix",
			alert:     collector.Alert{Rule: "rss_growth", Detail: "suspected memory leak", PID: 42},
			diag:      &Diagnosis{Summary: "RSS grew 42%/10min", Confidence: "high"},
			wantTitle: "monitor: rss_growth",
			wantMsg:   "RSS grew 42%/10min [confidence: high]",
		},
		{
			name:      "quotes in summary are sanitized",
			alert:     base,
			diag:      &Diagnosis{Summary: `say "hi"`},
			wantTitle: "monitor: rss_growth",
			wantMsg:   "say 'hi' (node pid 42)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, msg := desktopText(tt.alert, tt.diag)
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
			if msg != tt.wantMsg {
				t.Errorf("msg = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}
