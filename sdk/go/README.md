# monitor Go SDK

```sh
go get github.com/abdul-hamid-achik/monitor/sdk/go
```

Monitor's own error SDK for Go. It has zero dependencies, runs locally, and
never talks to a server. Every captured error becomes one `monitor.event.v1`
JSON file that the [monitor](https://monitorcli.dev) CLI groups into issues
and points at a line.

Go cannot be instrumented from outside, so this SDK is explicit. Without it,
`monitor run -- go run .` still records panics from the trace Go prints.

```go
import monitor "github.com/abdul-hamid-achik/monitor/sdk/go"

func main() {
	monitor.Init(monitor.Options{Service: "api", Release: version, Environment: "dev"})
	defer monitor.Recover() // records a panic, then panics again

	monitor.SetTag("region", "mx")
	monitor.AddBreadcrumb("db", "select users")

	if err := charge(order); err != nil {
		monitor.CaptureError(err, monitor.WithTags(map[string]string{"order": order.ID}))
	}
	monitor.CaptureMessage("cache rebuilt", monitor.LevelInfo)
}

func worker() {
	defer monitor.RecoverAndContinue() // records a panic and keeps the goroutine's caller alive
	// ...
}
```

`CaptureError` records the error's type, message and `Unwrap` chain, plus the
stack where it was captured. A panic recorded by `Recover` groups with the trace
Go prints for the same panic. Under `monitor run`, events go to that launch.
Anywhere else, they go to `$XDG_STATE_HOME/monitor/events/inbox`, which
`monitor issues` and `monitor events drain` read. At most 50 events are written
per 10 seconds.
