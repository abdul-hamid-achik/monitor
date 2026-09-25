//go:build race

package devrun

// raceEnabled reports whether the test binary was built with -race. The race
// detector instruments the copy/scan goroutines, so wall-clock budgets are
// scaled up under it (see TestRunBurstNeverSlowsTheChildEvenWithStoreLocked)
// instead of skipped: there is no benchmark backstop here, and CI must keep
// checking the bounded-channel tripwire, loosely.
const raceEnabled = true
