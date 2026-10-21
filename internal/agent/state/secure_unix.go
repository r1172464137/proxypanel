//go:build !windows

package state

import (
	"fmt"
	"os"
)

// securePath restricts the state directory to its owner.
func securePath(path string) error {
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("restrict the permissions of %s: %w", path, err)
	}
	return nil
}

// secureFile restricts a file inside the state directory to its owner.
func secureFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("restrict the permissions of %s: %w", path, err)
	}
	return nil
}
