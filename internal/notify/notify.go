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

// Webhook POSTs the alert as JSON to url. A non-2xx response is an error.
func Webhook(ctx context.Context, client *http.Client, url string, a collector.Alert) error {
	body, err := json.Marshal(a)
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
	defer resp.Body.Close()
	// Drain the body (bounded) so the transport can return the connection to
	// the keep-alive pool instead of dropping it — alerts can POST to the same
	// URL every tick. Applies on both the success and non-2xx paths.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// Desktop shows a native desktop notification for the alert. Returns an error
// when no notifier is available (headless / unsupported OS).
func Desktop(ctx context.Context, a collector.Alert) error {
	title := "monitor: " + a.Rule
	msg := a.Detail
	if a.Process != "" {
		msg = fmt.Sprintf("%s (%s pid %d)", a.Detail, a.Process, a.PID)
	}
	// Sanitize quotes so they can't break the osascript / shell argument.
	title = strings.ReplaceAll(title, `"`, "'")
	msg = strings.ReplaceAll(msg, `"`, "'")

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
