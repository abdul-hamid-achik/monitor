//go:build unix

package issues

import (
	"errors"
	"os"
	"syscall"
)

// flockExclusive takes a non-blocking exclusive advisory lock on f via
// syscall.Flock (see crossProcessLock's doc comment in writer.go for why
// this, rather than veclite's own file lock, is what guards the veclite
// open->close cycle). A lock already held by another process's file
// descriptor returns errWriterLockHeld; any other failure is returned
// as-is.
func flockExclusive(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errWriterLockHeld
	}
	return err
}

// flockRelease releases f's advisory lock. It never removes the underlying
// file -- see crossProcessLock's doc comment for why that unlink is exactly
// the veclite defect this lock works around.
func flockRelease(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
