package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if err := Webhook(context.Background(), srv.Client(), srv.URL, alert); err != nil {
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
	if err := Webhook(context.Background(), srv.Client(), srv.URL, collector.Alert{Rule: "x"}); err == nil {
		t.Error("expected an error on a 500 response")
	}
}
