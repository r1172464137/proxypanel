//go:build !windows

package state

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireLock takes an exclusive flock on the state directory lock file. The
// lock disappears when the process dies, so a crash cannot block a restart.
func AcquireLock(dir string) (io.Closer, error) {
	path := filepath.Join(dir, "agent.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock state directory: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state directory %s is already in use by another agent: %w", dir, err)
	}
	return &unixLock{f: f}, nil
}

type unixLock struct{ f *os.File }

func (l *unixLock) Close() error {
	if l.f == nil {
		return nil
	}
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	cerr := l.f.Close()
	l.f = nil
	if err != nil {
		return err
	}
	return cerr
}
