//go:build race

package scrub

// raceEnabled reports whether the test binary was built with -race. The race
// detector slows regexp-heavy code by an order of magnitude, so wall-clock
// budget tests skip themselves under it (the benchmarks still measure the
// real cost).
const raceEnabled = true
