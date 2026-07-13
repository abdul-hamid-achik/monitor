package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newReloadCmd triggers an in-process refresh on a running monitor TUI.
// Useful after `monitor logs capture` (or any other workflow that
// changes the log store / fcheap stash) when the TUI is still open.
//
// The TUI binary embeds internal/reload, which exposes POST /reload on
// 127.0.0.1:7351. This subcommand posts to that endpoint and prints the
// response. If no TUI is running, it exits non-zero with a clear error
// so scripts can detect the failure.
func newReloadCmd() *cobra.Command {
	var (
		addr string
		wait time.Duration
	)
	cmd := &cobra.Command{
		Use:   "reload",
		Short: "Trigger a refresh on the running TUI's /reload endpoint",
		Long: `reload POSTs to the /reload endpoint exposed by a running monitor
TUI binary (127.0.0.1:7351 by default). Useful after ` + "`monitor logs capture`" + ` or any
other workflow that changes data the TUI caches.

The endpoint is loopback-only; remote processes cannot reach it. If
no TUI is running, reload exits non-zero with a clear error.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			url := "http://" + addr + "/reload"
			client := &http.Client{Timeout: wait}
			req, err := http.NewRequest(http.MethodPost, url, nil)
			if err != nil {
				return fmt.Errorf("build request: %w", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("POST %s: %w", url, err)
			}
			body, readErr := io.ReadAll(resp.Body)
			if err := errors.Join(readErr, resp.Body.Close()); err != nil {
				return fmt.Errorf("read POST %s response: %w", url, err)
			}
			bodyStr := strings.TrimSpace(string(body))
			if resp.StatusCode == http.StatusOK {
				fmt.Println(bodyStr)
				return nil
			}
			// 405 (method not allowed) is a hard error; 500 means the
			// endpoint is up but the in-process reload failed. Both
			// surface the body so the operator can debug.
			return fmt.Errorf("POST %s returned %d: %s", url, resp.StatusCode, bodyStr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:7351",
		"address of the running monitor TUI's /reload endpoint")
	cmd.Flags().DurationVar(&wait, "timeout", 3*time.Second,
		"HTTP client timeout for the POST")

	return cmd
}
