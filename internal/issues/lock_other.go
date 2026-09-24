//go:build !unix

package issues

import "os"

// flockExclusive/flockRelease are a best-effort no-op on non-unix platforms.
// monitor currently ships for macOS and Linux only (AGENTS.md's Platform
// line); on any other GOOS the per-process writerLocks mutex (writer.go)
// still protects the common single-process case, and OpenStoreWait still
// falls back to veclite's own (buggy, but only-option-here) file lock.
//
// TODO(local-sentry CC-6 follow-up): a real cross-process lock on Windows
// needs LockFileEx (golang.org/x/sys/windows), a new dependency this
// package does not currently have a reason to add.
func flockExclusive(f *os.File) error { return nil }

func flockRelease(f *os.File) error { return nil }
