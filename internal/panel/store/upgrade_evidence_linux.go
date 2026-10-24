//go:build linux

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func sameUpgradeFile(a, b os.FileInfo) bool {
	if !os.SameFile(a, b) || a.Mode() != b.Mode() || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	// Include change time, but not access time (our own read can update it).
	left, leftOK := a.Sys().(*syscall.Stat_t)
	right, rightOK := b.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && left.Ctim == right.Ctim
}

// Walk pinned roots rather than following a path between Lstat and Open.
// Each component must be a real directory and the leaf a regular file.
// Root confinement also prevents a racing symlink from escaping its parent.
func openUpgradeFile(path string) (*os.File, error) {
	volume := filepath.VolumeName(path)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(path, volume), string(filepath.Separator)), string(filepath.Separator))
	for i, part := range parts {
		before, err := root.Lstat(part)
		if err != nil {
			return nil, err
		}
		if i == len(parts)-1 {
			if !before.Mode().IsRegular() {
				return nil, fmt.Errorf("not a regular evidence file: %q", path)
			}
			// No-follow closes the Lstat/Open race; nonblocking prevents a
			// raced FIFO from hanging before its descriptor can be checked.
			file, err := root.OpenFile(part, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return nil, err
			}
			after, err := file.Stat()
			if err != nil || !sameUpgradeFile(before, after) {
				_ = file.Close()
				return nil, fmt.Errorf("evidence changed while opening %q: %v", path, err)
			}
			return file, nil
		}
		if !before.IsDir() {
			return nil, fmt.Errorf("not a real evidence directory: %q", part)
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		after, err := next.Stat(".")
		entry, entryErr := root.Lstat(part)
		if err != nil || entryErr != nil || !sameUpgradeFile(before, after) || !sameUpgradeFile(before, entry) {
			_ = next.Close()
			return nil, fmt.Errorf("evidence directory changed: %q", part)
		}
		_ = root.Close()
		root = next
	}
	return nil, errors.New("missing evidence filename")
}
