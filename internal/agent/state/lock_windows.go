//go:build windows

package state

import (
	"fmt"
	"io"
	"path/filepath"
	"syscall"
)

// AcquireLock takes an exclusive lock on the state directory. Windows keeps the
// file handle in share-deny mode so a second agent cannot use the same state
// directory and a crashed agent releases the lock with its process.
func AcquireLock(dir string) (io.Closer, error) {
	path := filepath.Join(dir, "agent.lock")
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("lock state directory: %w", err)
	}
	h, err := syscall.CreateFile(ptr, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("state directory %s is already in use by another agent: %w", dir, err)
	}
	return &windowsLock{h: h}, nil
}

type windowsLock struct{ h syscall.Handle }

func (l *windowsLock) Close() error {
	if l.h == 0 {
		return nil
	}
	err := syscall.CloseHandle(l.h)
	l.h = 0
	return err
}
