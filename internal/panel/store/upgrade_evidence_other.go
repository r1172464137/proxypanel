//go:build !linux

package store

import (
	"errors"
	"os"
)

func sameUpgradeFile(a, b os.FileInfo) bool { return false }
func openUpgradeFile(path string) (*os.File, error) {
	return nil, errors.New("upgrade evidence protected file access is unsupported on this platform")
}
