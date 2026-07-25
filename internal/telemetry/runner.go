package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

const (
	// MinSampleInterval bounds the cost of telemetry's host-wide scans. The
	// exporter is intended for durable host trends, not high-frequency tracing.
	MinSampleInterval = time.Second

	defaultFlushTimeout = 2 * time.Second
)

// CaptureFunc returns one collector snapshot and any analyzer alerts produced
// from that same snapshot. The exporter sanitizes both before serialization.
type CaptureFunc func(context.Context) (collector.SystemInfo, []collector.Alert, error)

type clockReading struct {
	wall      time.Time
	monotonic time.Duration
}

// runnerClock keeps scheduling independent from the civil clock. Tests use
// this seam to exercise suspend, delayed capture, and wall-clock correction
// without sleeping.
type runnerClock interface {
	Now() clockReading
	Wait(context.Context, time.Duration) error
}

type systemRunnerClock struct {
	origin time.Time
}

func newSystemRunnerClock() *systemRunnerClock {
	return &systemRunnerClock{origin: time.Now()}
}

func (c *systemRunnerClock) Now() clockReading {
	now := time.Now()
	return clockReading{
		wall:      now.UTC(),
		monotonic: now.Sub(c.origin),
	}
}

func (*systemRunnerClock) Wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Runner streams telemetry windows to a caller-owned writer. It performs no
// network requests, authentication, retries, or persistent writes.
type Runner struct {
	Interval        time.Duration
	Window          time.Duration
	Once            bool
	ProducerVersion string
	SessionID       string
	Capture         CaptureFunc
	Writer          io.Writer

	clock        runnerClock
	flushTimeout time.Duration
}

// Run prewarms counter-based metrics, samples until cancellation, and emits
// newline-delimited V1 envelopes. Cadence and window deadlines use a monotonic
// clock; UTC is only a serialization projection. A normal cancellation makes
// one bounded attempt to flush a non-empty partial window.
func (r Runner) Run(ctx context.Context) error {
	if err := r.validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil
	}

	clock := r.clock
	if clock == nil {
		clock = newSystemRunnerClock()
	}
	flushTimeout := r.flushTimeout
	if flushTimeout <= 0 {
		flushTimeout = defaultFlushTimeout
	}
	sessionID := r.SessionID
	if sessionID == "" {
		var err error
		sessionID, err = newSessionID()
		if err != nil {
			return fmt.Errorf("create telemetry session id: %w", err)
		}
	}
	if !validSessionID(sessionID) {
		return fmt.Errorf("invalid telemetry session id")
	}

	// Counter-derived network and disk rates need a prior sample. The warm-up
	// snapshot is intentionally not exported.
	if _, _, err := r.Capture(ctx); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("prewarm telemetry collector: %w", err)
	}

	start := clock.Now()
	if start.wall.IsZero() {
		return fmt.Errorf("telemetry clock returned a zero wall time")
	}
	start.wall = start.wall.UTC()

	if r.Once {
		return r.runOnce(ctx, clock, start, sessionID, flushTimeout)
	}
	return r.runContinuous(ctx, clock, start, sessionID, flushTimeout)
}

func (r Runner) runOnce(ctx context.Context, clock runnerClock, start clockReading, sessionID string, flushTimeout time.Duration) error {
	if err := clock.Wait(ctx, r.Interval); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("wait for telemetry sample: %w", err)
	}
	beforeCapture, err := readClock(clock, start.monotonic)
	if err != nil {
		return err
	}
	info, alerts, err := r.Capture(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("capture telemetry sample: %w", err)
	}
	afterCapture, err := readClock(clock, beforeCapture.monotonic)
	if err != nil {
		return err
	}
	builder := NewBuilder()
	if err := builder.Add(info, alerts); err != nil {
		return err
	}
	toMono := afterCapture.monotonic
	if toMono <= start.monotonic {
		toMono = start.monotonic + time.Nanosecond
	}
	emitter := windowEmitter{
		runner:            r,
		sessionID:         sessionID,
		sequence:          1,
		anchor:            start,
		builder:           builder,
		windowStartMono:   start.monotonic,
		cancellationGrace: flushTimeout,
	}
	if err := emitter.emit(ctx, toMono, afterCapture, true); err != nil {
		return err
	}
	return nil
}

func (r Runner) runContinuous(ctx context.Context, clock runnerClock, warmStart clockReading, sessionID string, flushTimeout time.Duration) error {
	// Start the first export window with an actual rate-bearing sample. The
	// warm-up and this wait stay outside the window, so an initial scheduler
	// delay cannot masquerade as a complete long window.
	if err := clock.Wait(ctx, r.Interval); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("wait for first telemetry sample: %w", err)
	}
	beforeFirst, err := readClock(clock, warmStart.monotonic)
	if err != nil {
		return err
	}
	firstPartial := beforeFirst.monotonic-(warmStart.monotonic+r.Interval) >= r.Interval
	info, alerts, err := r.Capture(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("capture first telemetry sample: %w", err)
	}
	first, err := readClock(clock, beforeFirst.monotonic)
	if err != nil {
		return err
	}
	if first.monotonic-beforeFirst.monotonic >= r.Interval {
		firstPartial = true
	}

	builder := NewBuilder()
	if err := builder.Add(info, alerts); err != nil {
		return err
	}
	emitter := windowEmitter{
		runner:            r,
		sessionID:         sessionID,
		sequence:          1,
		anchor:            first,
		builder:           builder,
		windowStartMono:   first.monotonic,
		cancellationGrace: flushTimeout,
	}
	windowEnd := first.monotonic + r.Window
	nextSample := first.monotonic + r.Interval
	windowPartial := firstPartial
	lastMono := first.monotonic

	flushPartial := func(reading clockReading) error {
		if emitter.builder.SampleCount() == 0 {
			return nil
		}
		toMono := reading.monotonic
		if toMono > windowEnd {
			toMono = windowEnd
		}
		if toMono <= emitter.windowStartMono {
			toMono = emitter.windowStartMono + time.Nanosecond
		}
		// A pre-cancelled context tells the writer to make exactly one attempt
		// bounded by cancellationGrace. Reusing one in-flight write avoids a
		// duplicate/interleaved retry if stdout is blocked.
		flushCtx, cancel := context.WithCancel(context.Background())
		cancel()
		return emitter.emit(flushCtx, toMono, reading, true)
	}

	for {
		deadline := nextSample
		if windowEnd < deadline {
			deadline = windowEnd
		}
		delay := deadline - lastMono
		if delay > 0 {
			if err := clock.Wait(ctx, delay); err != nil {
				reading, clockErr := readClock(clock, lastMono)
				if clockErr != nil {
					return clockErr
				}
				if ctx.Err() != nil {
					return flushPartial(reading)
				}
				return fmt.Errorf("wait for telemetry deadline: %w", err)
			}
		}

		reading, err := readClock(clock, lastMono)
		if err != nil {
			return err
		}
		lastMono = reading.monotonic
		if err := ctx.Err(); err != nil {
			return flushPartial(reading)
		}
		if reading.monotonic < deadline {
			// A timer may wake spuriously; monotonic time remains authoritative.
			continue
		}

		// Close elapsed windows before collecting. This prevents a late sample
		// from being attached to a period that already ended.
		if reading.monotonic >= windowEnd {
			if reading.monotonic-windowEnd >= r.Interval {
				windowPartial = true
			}
			if emitter.builder.SampleCount() > 0 {
				if err := emitter.emit(ctx, windowEnd, reading, windowPartial); err != nil {
					return err
				}
			}
			emitter.windowStartMono, windowEnd = advanceElapsedWindows(
				reading.monotonic, windowEnd, r.Window,
			)
			emitter.builder = NewBuilder()
			windowPartial = false
			// Skip only deadlines that are at least a full interval stale.
			previousSample := nextSample
			var skipped int64
			nextSample, skipped = retainNewestDueSample(nextSample, reading.monotonic, r.Interval)
			if skipped > 0 && previousSample < windowEnd && nextSample > emitter.windowStartMono {
				windowPartial = true
			}
			continue
		}

		// A delayed wake may have missed one or more complete sample slots. Keep
		// the newest due slot, mark the window incomplete, and take one sample.
		var skipped int64
		nextSample, skipped = retainNewestDueSample(nextSample, reading.monotonic, r.Interval)
		if skipped > 0 {
			windowPartial = true
		}
		if nextSample > reading.monotonic || nextSample >= windowEnd {
			continue
		}
		scheduledSample := nextSample
		info, alerts, captureErr := r.Capture(ctx)
		afterCapture, clockErr := readClock(clock, reading.monotonic)
		if clockErr != nil {
			return clockErr
		}
		lastMono = afterCapture.monotonic
		if captureErr != nil {
			if errors.Is(captureErr, context.Canceled) && ctx.Err() != nil {
				return flushPartial(afterCapture)
			}
			if flushErr := flushPartial(afterCapture); flushErr != nil {
				return errors.Join(fmt.Errorf("capture telemetry sample: %w", captureErr), flushErr)
			}
			return fmt.Errorf("capture telemetry sample: %w", captureErr)
		}

		if afterCapture.monotonic >= windowEnd {
			windowPartial = true
			// The snapshot completed outside this window. Discard it rather
			// than assigning a late measurement to an elapsed period.
			if emitter.builder.SampleCount() > 0 {
				if err := emitter.emit(ctx, windowEnd, afterCapture, true); err != nil {
					return err
				}
			}
			emitter.windowStartMono, windowEnd = advanceElapsedWindows(
				afterCapture.monotonic, windowEnd, r.Window,
			)
			emitter.builder = NewBuilder()
			windowPartial = false
			nextSample = scheduledSample + r.Interval
			previousSample := nextSample
			nextSample, skipped = retainNewestDueSample(nextSample, afterCapture.monotonic, r.Interval)
			if skipped > 0 && previousSample < windowEnd && nextSample > emitter.windowStartMono {
				windowPartial = true
			}
			continue
		}

		if err := emitter.builder.Add(info, alerts); err != nil {
			return err
		}
		nextSample = scheduledSample + r.Interval
		nextSample, skipped = advancePastDueSamples(nextSample, afterCapture.monotonic, r.Interval)
		if skipped > 0 {
			windowPartial = true
		}
	}
}

type windowEmitter struct {
	runner            Runner
	sessionID         string
	sequence          uint64
	anchor            clockReading
	builder           *Builder
	windowStartMono   time.Duration
	cancellationGrace time.Duration
}

func (e *windowEmitter) emit(ctx context.Context, toMono time.Duration, reading clockReading, partial bool) error {
	if e.builder.SampleCount() == 0 {
		return nil
	}
	if toMono <= e.windowStartMono {
		return fmt.Errorf("telemetry window must have positive duration")
	}
	from := e.wallAt(e.windowStartMono)
	to := e.wallAt(toMono)
	emittedAt := reading.wall.UTC()
	if emittedAt.Before(to) {
		emittedAt = to
	}
	envelope, err := e.builder.Build(
		e.sessionID, e.sequence, e.runner.ProducerVersion,
		from, to, emittedAt, e.runner.Interval, partial,
	)
	if err != nil {
		return err
	}
	line, err := MarshalNDJSON(envelope)
	if err != nil {
		return err
	}
	if err := writeWithContext(ctx, e.runner.Writer, line, e.cancellationGrace); err != nil {
		return err
	}
	e.sequence++
	e.builder = NewBuilder()
	return nil
}

func (e *windowEmitter) wallAt(monotonic time.Duration) time.Time {
	return e.anchor.wall.Add(monotonic - e.anchor.monotonic).UTC()
}

func readClock(clock runnerClock, notBefore time.Duration) (clockReading, error) {
	reading := clock.Now()
	if reading.wall.IsZero() {
		return clockReading{}, fmt.Errorf("telemetry clock returned a zero wall time")
	}
	if reading.monotonic < notBefore {
		return clockReading{}, fmt.Errorf("telemetry monotonic clock moved backwards")
	}
	reading.wall = reading.wall.UTC()
	return reading, nil
}

func writeWithContext(ctx context.Context, writer io.Writer, data []byte, cancellationGrace time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if cancellationGrace <= 0 {
		cancellationGrace = defaultFlushTimeout
	}
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := writer.Write(data)
		done <- result{n: n, err: err}
	}()
	validate := func(outcome result) error {
		if outcome.err != nil {
			return outcome.err
		}
		if outcome.n != len(data) {
			return io.ErrShortWrite
		}
		return nil
	}
	select {
	case outcome := <-done:
		return validate(outcome)
	case <-ctx.Done():
		timer := time.NewTimer(cancellationGrace)
		defer timer.Stop()
		select {
		case outcome := <-done:
			return validate(outcome)
		case <-timer.C:
			return fmt.Errorf("telemetry write did not finish during cancellation grace: %w", context.DeadlineExceeded)
		}
	}
}

// retainNewestDueSample discards complete stale slots but leaves the newest
// due slot available for one capture. Arithmetic keeps resume-after-suspend
// work O(1), even after a long machine sleep.
func retainNewestDueSample(next, now, interval time.Duration) (time.Duration, int64) {
	if next+interval > now {
		return next, 0
	}
	skipped := int64((now - next) / interval)
	return next + time.Duration(skipped)*interval, skipped
}

// advancePastDueSamples moves a schedule strictly after now after a capture.
func advancePastDueSamples(next, now, interval time.Duration) (time.Duration, int64) {
	if next > now {
		return next, 0
	}
	skipped := int64((now-next)/interval) + 1
	return next + time.Duration(skipped)*interval, skipped
}

// advanceElapsedWindows moves the schedule to the unique window containing now,
// or to the window beginning at now when now is exactly a boundary. Arithmetic
// keeps resume-after-stall work O(1), even when many empty windows elapsed.
func advanceElapsedWindows(now, currentEnd, window time.Duration) (time.Duration, time.Duration) {
	elapsed := (now-currentEnd)/window + 1
	nextEnd := currentEnd + elapsed*window
	return nextEnd - window, nextEnd
}

func (r Runner) validate() error {
	if r.Interval < MinSampleInterval {
		return fmt.Errorf("telemetry interval must be at least %s", MinSampleInterval)
	}
	if r.Window < r.Interval {
		return fmt.Errorf("telemetry window must be at least the interval")
	}
	samples := int(math.Ceil(float64(r.Window) / float64(r.Interval)))
	if samples > MaxSamplesPerWindow {
		return fmt.Errorf("telemetry window would contain %d samples; maximum is %d", samples, MaxSamplesPerWindow)
	}
	if !validVersion(r.ProducerVersion) {
		return fmt.Errorf("telemetry producer version is invalid")
	}
	if r.Capture == nil {
		return fmt.Errorf("telemetry capture function is required")
	}
	if r.Writer == nil {
		return fmt.Errorf("telemetry writer is required")
	}
	return nil
}

func newSessionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
