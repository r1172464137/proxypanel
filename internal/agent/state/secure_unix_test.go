//go:build !windows

package state

import (
	"os"
	"testing"
)

// TestStateDirPermissions proves the state directory is owner-only on Unix.
func TestStateDirPermissions(t *testing.T) {
	store := newStore(t)
	info, err := os.Stat(store.Dir())
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state directory mode = %o, want 700", perm)
	}
	info, err = os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state database mode = %o, want 600", perm)
	}
}
