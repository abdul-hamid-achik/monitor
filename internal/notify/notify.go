// Package notify delivers analyzer alerts to outbound sinks: an HTTP webhook
// (POSTs the alert as JSON) and native desktop notifications (osascript on
// macOS, notify-send on Linux). Both are best-effort — the caller decides how
// to surface delivery failures.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// Diagnosis mirrors analyzer.Diagnosis so webhook payloads and desktop
// notifications can explain WHY an alert fired without importing the
// analyzer package.
type Diagnosis struct {
	Summary     string   `json:"summary,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	NextActions []string `json:"next_actions,omitempty"`
}

// AlertPayload is the JSON body POSTed by Webhook. Embedding collector.Alert
// keeps every pre-existing top-level key (severity, rule, pid, process,
// detail) unchanged; "diagnosis" is a new optional key — additive, so
// existing webhook consumers are unaffected.
//
// AlertPayload.Diagnosis (depth 0) and the embedded collector.Alert.Diagnosis
// (depth 1) share the same JSON key "diagnosis"; encoding/json's shallowest-
// wins rule means the depth-0 field is always what gets marshaled, and the
// embedded one is invisible to json.Marshal no matter its value. Every
// constructor here MUST therefore populate the outer field explicitly
// (see resolveDiagnosis) rather than relying on the embedded Alert to
// contribute it.
type AlertPayload struct {
	collector.Alert
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
}

// resolveDiagnosis picks the diagnosis to attach to a payload/notification:
// the explicit d when the caller supplied one, otherwise a mirror of
// a.Diagnosis. Without this fallback, a caller that sets Alert.Diagnosis but
// passes d == nil would silently lose the diagnosis — AlertPayload's own
// Diagnosis field shadows the embedded collector.Alert one under
// encoding/json's depth rules, so nil-d always wins the marshal regardless
// of what the embedded Alert carries.
func resolveDiagnosis(a collector.Alert, d *Diagnosis) *Diagnosis {
	if d != nil {
		return d
	}
	if a.Diagnosis == nil {
		return nil
	}
	return &Diagnosis{
		Summary:     a.Diagnosis.Summary,
		Evidence:    a.Diagnosis.Evidence,
		Confidence:  a.Diagnosis.Confidence,
		NextActions: a.Diagnosis.NextActions,
	}
}

// Webhook POSTs the alert (plus optional diagnosis) as JSON to url. d, when
// non-nil, overrides a.Diagnosis; when nil, a.Diagnosis (if any) is used —
// see resolveDiagnosis. A non-2xx response is an error.
func Webhook(ctx context.Context, client *http.Client, url string, a collector.Alert, d *Diagnosis) error {
	body, err := json.Marshal(AlertPayload{Alert: a, Diagnosis: resolveDiagnosis(a, d)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	// Drain the body (bounded) so the transport can return the connection to
	// the keep-alive pool instead of dropping it — alerts can POST to the same
	// URL every tick. Applies on both the success and non-2xx paths.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("webhook %s: close response: %w", url, err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// Desktop shows a native desktop notification for the alert. d, when
// non-nil, overrides a.Diagnosis; when nil, a.Diagnosis (if any) is used —
// see resolveDiagnosis. Returns an error when no notifier is available
// (headless / unsupported OS).
func Desktop(ctx context.Context, a collector.Alert, d *Diagnosis) error {
	title, msg := desktopText(a, resolveDiagnosis(a, d))
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf("display notification %q with title %q", msg, title)
		return exec.CommandContext(ctx, "osascript", "-e", script).Run()
	case "linux":
		if _, err := exec.LookPath("notify-send"); err != nil {
			return fmt.Errorf("notify-send not available")
		}
		return exec.CommandContext(ctx, "notify-send", title, msg).Run()
	default:
		return fmt.Errorf("desktop notifications not supported on %s", runtime.GOOS)
	}
}

// desktopText builds the notification title and body. When a diagnosis is
// present its Summary replaces the terse rule Detail (that is the whole
// point of Sprint 4.4: "suspected memory leak" becomes "RSS grew 42%/10min
// while CPU stayed flat ..."), and the confidence level is appended.
// Split from Desktop so the formatting is unit-testable without osascript.
func desktopText(a collector.Alert, d *Diagnosis) (title, msg string) {
	title = "monitor: " + a.Rule
	body := a.Detail
	if d != nil && d.Summary != "" {
		body = d.Summary
	}
	msg = body
	if a.Process != "" {
		msg = fmt.Sprintf("%s (%s pid %d)", body, a.Process, a.PID)
	}
	if d != nil && d.Confidence != "" {
		msg += " [confidence: " + d.Confidence + "]"
	}
	// Sanitize quotes so they can't break the osascript / shell argument.
	title = strings.ReplaceAll(title, `"`, "'")
	msg = strings.ReplaceAll(msg, `"`, "'")
	return title, msg
}
