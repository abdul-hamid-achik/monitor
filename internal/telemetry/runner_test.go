package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(data []byte) (int, error) { return f(data) }

type fakeRunnerClock struct {
	mu      sync.Mutex
	wall    time.Time
	mono    time.Duration
	waitFun func(context.Context, time.Duration) error
}

func newFakeRunnerClock() *fakeRunnerClock {
	return &fakeRunnerClock{
		wall: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	}
}

func (c *fakeRunnerClock) Now() clockReading {
	c.mu.Lock()
	defer c.mu.Unlock()
	return clockReading{wall: c.wall, monotonic: c.mono}
}

func (c *fakeRunnerClock) Wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.waitFun != nil {
		return c.waitFun(ctx, delay)
	}
	c.Advance(delay, delay)
	return nil
}

func (c *fakeRunnerClock) Advance(monotonic, wall time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mono += monotonic
	c.wall = c.wall.Add(wall)
}

func (c *fakeRunnerClock) JumpWall(delta time.Duration) {
	c.Advance(0, delta)
}

func decodeLines(t *testing.T, data []byte) []WindowEnvelope {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) == 1 && len(lines[0]) == 0 {
		return nil
	}
	out := make([]WindowEnvelope, 0, len(lines))
	for _, line := range lines {
		var envelope WindowEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		out = append(out, envelope)
	}
	return out
}

func TestRunnerOncePrewarmsAndEmitsOnePartialWindow(t *testing.T) {
	var captures atomic.Int32
	var output bytes.Buffer
	runner := Runner{
		Interval:        time.Second,
		Window:          time.Second,
		Once:            true,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			captures.Add(1)
			return observedInfo(), nil, nil
		},
		Writer: &output,
		clock:  newFakeRunnerClock(),
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := captures.Load(); got != 2 {
		t.Fatalf("captures = %d, want one prewarm plus one sample", got)
	}
	lines := decodeLines(t, output.Bytes())
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	if !lines[0].Window.Partial || lines[0].Window.SampleCount != 1 || lines[0].Sequence != 1 {
		t.Fatalf("window = %+v sequence=%d", lines[0].Window, lines[0].Sequence)
	}
	if got := lines[0].Window.To.Sub(lines[0].Window.From); got != time.Second {
		t.Fatalf("window duration = %s, want 1s", got)
	}
}

func TestRunnerCancellationFlushesPartialWindowAndExitsCleanly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var captures atomic.Int32
	var output bytes.Buffer
	runner := Runner{
		Interval:        time.Second,
		Window:          30 * time.Second,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			if captures.Add(1) == 2 {
				cancel()
			}
			return observedInfo(), nil, nil
		},
		Writer: &output,
		clock:  newFakeRunnerClock(),
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run after cancellation: %v", err)
	}
	lines := decodeLines(t, output.Bytes())
	if len(lines) != 1 || !lines[0].Window.Partial || lines[0].Window.SampleCount != 1 {
		t.Fatalf("partial cancellation output = %+v", lines)
	}
	if !lines[0].Window.To.After(lines[0].Window.From) {
		t.Fatalf("partial window is not strictly ordered: %+v", lines[0].Window)
	}
}

func TestRunnerEmitsCompletedWindowsWithMonotonicSequence(t *testing.T) {
	stop := errors.New("fixture stop")
	var output bytes.Buffer
	writes := 0
	runner := Runner{
		Interval:        time.Second,
		Window:          2 * time.Second,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			return observedInfo(), nil, nil
		},
		Writer: writerFunc(func(data []byte) (int, error) {
			writes++
			n, _ := output.Write(data)
			if writes == 2 {
				return n, stop
			}
			return n, nil
		}),
		clock: newFakeRunnerClock(),
	}
	if err := runner.Run(context.Background()); !errors.Is(err, stop) {
		t.Fatalf("Run error = %v, want fixture stop", err)
	}
	lines := decodeLines(t, output.Bytes())
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2: %s", len(lines), output.Bytes())
	}
	for i, line := range lines {
		if line.Sequence != uint64(i+1) {
			t.Errorf("line %d sequence = %d", i, line.Sequence)
		}
		if line.Window.Partial {
			t.Errorf("line %d unexpectedly partial", i)
		}
		if !line.Window.To.After(line.Window.From) {
			t.Errorf("line %d has non-positive window: %+v", i, line.Window)
		}
	}
	if !lines[0].Window.To.Equal(lines[1].Window.From) {
		t.Fatalf("window boundary gap: first.to=%v second.from=%v",
			lines[0].Window.To, lines[1].Window.From)
	}
}

func TestRunnerMarksMissedDeadlineWindowPartialAndKeepsScheduledBounds(t *testing.T) {
	clock := newFakeRunnerClock()
	waits := 0
	clock.waitFun = func(_ context.Context, delay time.Duration) error {
		waits++
		if waits == 2 {
			clock.Advance(4*time.Second, 4*time.Second)
		} else {
			clock.Advance(delay, delay)
		}
		return nil
	}
	stop := errors.New("fixture stop")
	var output bytes.Buffer
	runner := Runner{
		Interval:        time.Second,
		Window:          3 * time.Second,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			return observedInfo(), nil, nil
		},
		Writer: writerFunc(func(data []byte) (int, error) {
			n, _ := output.Write(data)
			return n, stop
		}),
		clock: clock,
	}
	if err := runner.Run(context.Background()); !errors.Is(err, stop) {
		t.Fatalf("Run error = %v, want fixture stop", err)
	}
	line := decodeLines(t, output.Bytes())[0]
	if !line.Window.Partial || line.Window.SampleCount != 1 {
		t.Fatalf("missed-deadline window = %+v", line.Window)
	}
	if got := line.Window.To.Sub(line.Window.From); got != 3*time.Second {
		t.Fatalf("window duration = %s, want scheduled 3s", got)
	}
}

func TestRunnerLargeCatchUpIsBoundedAndCancellationRemainsPrompt(t *testing.T) {
	const jump = time.Duration(1_000_000_000) * time.Second

	tests := []struct {
		name        string
		advanceWait bool
		wantCapture int32
	}{
		{name: "late_wake", advanceWait: true, wantCapture: 2},
		{name: "late_capture", wantCapture: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			clock := newFakeRunnerClock()
			waits := 0
			clock.waitFun = func(_ context.Context, delay time.Duration) error {
				waits++
				if tt.advanceWait && waits == 2 {
					clock.Advance(jump, jump)
				} else {
					clock.Advance(delay, delay)
				}
				return nil
			}

			var captures atomic.Int32
			var output bytes.Buffer
			runner := Runner{
				Interval:        time.Second,
				Window:          2 * time.Second,
				ProducerVersion: "test",
				SessionID:       "0123456789abcdef0123456789abcdef",
				Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
					captureNumber := captures.Add(1)
					if !tt.advanceWait && captureNumber == 3 {
						clock.Advance(jump, jump)
					}
					return observedInfo(), nil, nil
				},
				Writer: writerFunc(func(data []byte) (int, error) {
					n, err := output.Write(data)
					cancel()
					return n, err
				}),
				clock: clock,
			}

			done := make(chan error, 1)
			go func() {
				done <- runner.Run(ctx)
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Run after large catch-up: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("large catch-up looped per elapsed window or ignored cancellation")
			}

			if got := captures.Load(); got != tt.wantCapture {
				t.Fatalf("captures = %d, want %d", got, tt.wantCapture)
			}
			lines := decodeLines(t, output.Bytes())
			if len(lines) != 1 {
				t.Fatalf("lines = %d, want 1: %s", len(lines), output.Bytes())
			}
			if !lines[0].Window.Partial || lines[0].Window.SampleCount != 1 {
				t.Fatalf("large-jump window = %+v", lines[0].Window)
			}
			if got := lines[0].Window.To.Sub(lines[0].Window.From); got != 2*time.Second {
				t.Fatalf("window duration = %s, want 2s", got)
			}
		})
	}
}

func TestRunnerDiscardsCaptureThatCompletesAfterWindowDeadline(t *testing.T) {
	clock := newFakeRunnerClock()
	var captures atomic.Int32
	stop := errors.New("fixture stop")
	var output bytes.Buffer
	runner := Runner{
		Interval:        time.Second,
		Window:          3 * time.Second,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			if captures.Add(1) == 3 {
				clock.Advance(3*time.Second, 3*time.Second)
			}
			return observedInfo(), nil, nil
		},
		Writer: writerFunc(func(data []byte) (int, error) {
			n, _ := output.Write(data)
			return n, stop
		}),
		clock: clock,
	}
	if err := runner.Run(context.Background()); !errors.Is(err, stop) {
		t.Fatalf("Run error = %v, want fixture stop", err)
	}
	line := decodeLines(t, output.Bytes())[0]
	if !line.Window.Partial {
		t.Fatal("slow capture must mark the elapsed window partial")
	}
	if line.Window.SampleCount != 1 {
		t.Fatalf("late sample was added to elapsed window: sample_count=%d", line.Window.SampleCount)
	}
	if got := line.Window.To.Sub(line.Window.From); got != 3*time.Second {
		t.Fatalf("slow capture stretched window to %s", got)
	}
}

func TestRunnerMarksFirstWindowPartialAfterSlowInitialCapture(t *testing.T) {
	clock := newFakeRunnerClock()
	var captures atomic.Int32
	stop := errors.New("fixture stop")
	var output bytes.Buffer
	runner := Runner{
		Interval:        time.Second,
		Window:          2 * time.Second,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			if captures.Add(1) == 2 {
				clock.Advance(2*time.Second, 2*time.Second)
			}
			return observedInfo(), nil, nil
		},
		Writer: writerFunc(func(data []byte) (int, error) {
			n, _ := output.Write(data)
			return n, stop
		}),
		clock: clock,
	}
	if err := runner.Run(context.Background()); !errors.Is(err, stop) {
		t.Fatalf("Run error = %v, want fixture stop", err)
	}
	line := decodeLines(t, output.Bytes())[0]
	if !line.Window.Partial {
		t.Fatal("slow initial capture must mark the first window partial")
	}
	if got := line.Window.To.Sub(line.Window.From); got != 2*time.Second {
		t.Fatalf("slow initial capture stretched window to %s", got)
	}
}

func TestRunnerIgnoresForwardAndBackwardWallClockJumps(t *testing.T) {
	for _, jump := range []time.Duration{24 * time.Hour, -24 * time.Hour} {
		t.Run(jump.String(), func(t *testing.T) {
			clock := newFakeRunnerClock()
			var captures atomic.Int32
			var output bytes.Buffer
			runner := Runner{
				Interval:        time.Second,
				Window:          time.Second,
				Once:            true,
				ProducerVersion: "test",
				SessionID:       "0123456789abcdef0123456789abcdef",
				Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
					if captures.Add(1) == 2 {
						clock.JumpWall(jump)
					}
					return observedInfo(), nil, nil
				},
				Writer: &output,
				clock:  clock,
			}
			if err := runner.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			line := decodeLines(t, output.Bytes())[0]
			if got := line.Window.To.Sub(line.Window.From); got != time.Second {
				t.Fatalf("wall jump changed window duration to %s", got)
			}
			if !line.Window.To.After(line.Window.From) || line.EmittedAt.Before(line.Window.To) {
				t.Fatalf("timestamps are not ordered: %+v emitted=%v", line.Window, line.EmittedAt)
			}
		})
	}
}

func TestRunnerContinuousCadenceIgnoresWallClockJumps(t *testing.T) {
	for _, jump := range []time.Duration{24 * time.Hour, -24 * time.Hour} {
		t.Run(jump.String(), func(t *testing.T) {
			clock := newFakeRunnerClock()
			waits := 0
			clock.waitFun = func(_ context.Context, delay time.Duration) error {
				waits++
				clock.Advance(delay, delay)
				if waits == 2 {
					clock.JumpWall(jump)
				}
				return nil
			}
			stop := errors.New("fixture stop")
			var output bytes.Buffer
			runner := Runner{
				Interval:        time.Second,
				Window:          2 * time.Second,
				ProducerVersion: "test",
				SessionID:       "0123456789abcdef0123456789abcdef",
				Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
					return observedInfo(), nil, nil
				},
				Writer: writerFunc(func(data []byte) (int, error) {
					n, _ := output.Write(data)
					return n, stop
				}),
				clock: clock,
			}
			if err := runner.Run(context.Background()); !errors.Is(err, stop) {
				t.Fatalf("Run error = %v, want fixture stop", err)
			}
			line := decodeLines(t, output.Bytes())[0]
			if line.Window.Partial {
				t.Fatalf("wall correction marked complete cadence partial: %+v", line.Window)
			}
			if got := line.Window.To.Sub(line.Window.From); got != 2*time.Second {
				t.Fatalf("wall jump changed complete window duration to %s", got)
			}
			if !line.Window.To.After(line.Window.From) || line.EmittedAt.Before(line.Window.To) {
				t.Fatalf("timestamps are not ordered: %+v emitted=%v", line.Window, line.EmittedAt)
			}
		})
	}
}

func TestRunnerFlushesThenReportsCaptureFailure(t *testing.T) {
	var captures atomic.Int32
	var output bytes.Buffer
	runner := Runner{
		Interval:        time.Second,
		Window:          30 * time.Second,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			switch captures.Add(1) {
			case 1, 2:
				return observedInfo(), nil, nil
			default:
				return collector.SystemInfo{}, nil, errors.New("fixture capture failed")
			}
		},
		Writer: &output,
		clock:  newFakeRunnerClock(),
	}
	err := runner.Run(context.Background())
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("fixture capture failed")) {
		t.Fatalf("Run error = %v", err)
	}
	lines := decodeLines(t, output.Bytes())
	if len(lines) != 1 || !lines[0].Window.Partial || lines[0].Window.SampleCount != 1 {
		t.Fatalf("failure flush = %+v", lines)
	}
}

func TestRunnerPropagatesWriterErrorsAndShortWrites(t *testing.T) {
	tests := []struct {
		name   string
		writer io.Writer
		want   error
	}{
		{
			name: "writer_error",
			writer: writerFunc(func([]byte) (int, error) {
				return 0, errors.New("fixture pipe closed")
			}),
		},
		{
			name: "short_write",
			writer: writerFunc(func(data []byte) (int, error) {
				return len(data) - 1, nil
			}),
			want: io.ErrShortWrite,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := Runner{
				Interval:        time.Second,
				Window:          time.Second,
				Once:            true,
				ProducerVersion: "test",
				SessionID:       "0123456789abcdef0123456789abcdef",
				Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
					return observedInfo(), nil, nil
				},
				Writer: tt.writer,
				clock:  newFakeRunnerClock(),
			}
			err := runner.Run(context.Background())
			if err == nil {
				t.Fatal("expected writer failure")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Run error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRunnerCancellationLetsInFlightWriteFinishDuringGrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	runner := Runner{
		Interval:        time.Second,
		Window:          time.Second,
		Once:            true,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			return observedInfo(), nil, nil
		},
		Writer: writerFunc(func(data []byte) (int, error) {
			close(started)
			<-release
			return len(data), nil
		}),
		clock: newFakeRunnerClock(),
	}
	go func() {
		done <- runner.Run(ctx)
	}()
	<-started
	cancel()
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after graceful in-flight write: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight writer did not finish during cancellation grace")
	}
}

func TestRunnerCancellationBoundsPermanentlyBlockedWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	runner := Runner{
		Interval:        time.Second,
		Window:          time.Second,
		Once:            true,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			return observedInfo(), nil, nil
		},
		Writer: writerFunc(func(data []byte) (int, error) {
			close(started)
			<-release
			return len(data), nil
		}),
		clock:        newFakeRunnerClock(),
		flushTimeout: 10 * time.Millisecond,
	}
	go func() {
		done <- runner.Run(ctx)
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run error = %v, want cancellation-grace deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("permanently blocked writer prevented bounded cancellation")
	}
	close(release)
}

func TestRunnerCancellationImmediatelyBeforeEmitStillFlushesOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var captures atomic.Int32
	var output bytes.Buffer
	runner := Runner{
		Interval:        time.Second,
		Window:          time.Second,
		Once:            true,
		ProducerVersion: "test",
		SessionID:       "0123456789abcdef0123456789abcdef",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			if captures.Add(1) == 2 {
				cancel()
			}
			return observedInfo(), nil, nil
		},
		Writer: &output,
		clock:  newFakeRunnerClock(),
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	lines := decodeLines(t, output.Bytes())
	if len(lines) != 1 || !lines[0].Window.Partial {
		t.Fatalf("immediate-cancellation flush = %+v", lines)
	}
}

func TestRunnerValidationAndAlreadyCancelledContext(t *testing.T) {
	valid := Runner{
		Interval:        time.Second,
		Window:          2 * time.Second,
		ProducerVersion: "test",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			return observedInfo(), nil, nil
		},
		Writer: io.Discard,
		clock:  newFakeRunnerClock(),
	}
	tests := []struct {
		name   string
		mutate func(*Runner)
	}{
		{"interval", func(r *Runner) { r.Interval = 0 }},
		{"sub_second_interval", func(r *Runner) { r.Interval = time.Second - time.Nanosecond }},
		{"window", func(r *Runner) { r.Window = time.Millisecond }},
		{"sample_bound", func(r *Runner) { r.Interval = time.Second; r.Window = 3601 * time.Second }},
		{"version", func(r *Runner) { r.ProducerVersion = "bad version" }},
		{"capture", func(r *Runner) { r.Capture = nil }},
		{"writer", func(r *Runner) { r.Writer = nil }},
		{"session", func(r *Runner) { r.SessionID = "not-a-session" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := valid
			tt.mutate(&runner)
			if err := runner.Run(context.Background()); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	valid.Capture = func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
		called = true
		return observedInfo(), nil, nil
	}
	if err := valid.Run(ctx); err != nil {
		t.Fatalf("already-cancelled Run: %v", err)
	}
	if called {
		t.Fatal("already-cancelled runner must not capture")
	}
}

func TestRunnerRejectsBackwardMonotonicClock(t *testing.T) {
	clock := newFakeRunnerClock()
	clock.waitFun = func(context.Context, time.Duration) error {
		clock.Advance(-time.Nanosecond, 0)
		return nil
	}
	runner := Runner{
		Interval:        time.Second,
		Window:          time.Second,
		Once:            true,
		ProducerVersion: "test",
		Capture: func(context.Context) (collector.SystemInfo, []collector.Alert, error) {
			return observedInfo(), nil, nil
		},
		Writer: io.Discard,
		clock:  clock,
	}
	if err := runner.Run(context.Background()); err == nil ||
		!bytes.Contains([]byte(err.Error()), []byte("monotonic clock moved backwards")) {
		t.Fatalf("Run error = %v", err)
	}
}

func TestNewSessionIDIsRandomAndValid(t *testing.T) {
	first, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !validSessionID(first) || !validSessionID(second) {
		t.Fatalf("invalid generated ids %q %q", first, second)
	}
	if first == second {
		t.Fatal("generated session ids unexpectedly match")
	}
}

func TestAdvanceElapsedWindowsUsesScheduledBounds(t *testing.T) {
	const jump = time.Duration(1_000_000_000) * time.Second
	start, end := advanceElapsedWindows(jump, time.Second, time.Second)
	if start != jump || end != jump+time.Second {
		t.Fatalf("advanced window = [%s, %s), want [%s, %s)",
			start, end, jump, jump+time.Second)
	}
}
