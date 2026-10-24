package xray

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRevocationStopsOldCoreBeforeRollbackValidation(t *testing.T) {
	shortStopVerify(t)
	mgr, runner := newScriptedManager(t, filepath.Join(t.TempDir(), "xray.exe"), t.TempDir())
	if _, ok := applyOneHelper(t, mgr, targetFor(t, "alice@example.test", "bob@example.test")); !ok {
		t.Fatal("initial apply failed")
	}
	runner.setTestFn(func(ctx context.Context, configPath string) error {
		return errors.New("xray -test failed: unknown field")
	})
	revoked := targetFor(t, "alice@example.test")
	results := mgr.Apply(context.Background(), []Target{revoked}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("both configurations are rejected, want failed, got %+v", results)
	}
	// The revocation contract is unchanged: no invented applied count.
	if results[0].AppliedUserCount != 0 {
		t.Fatalf("a failed apply must not report applied users: %+v", results[0])
	}
	events := runner.eventList()
	rollbackIdx := indexOfEvent(events, func(e string) bool { return e == "test:config.rollback.json" })
	stopIdx := indexOfEvent(events, func(e string) bool { return strings.HasPrefix(e, "stop:") })
	if rollbackIdx < 0 || stopIdx < 0 {
		t.Fatalf("missing lifecycle events: %v", events)
	}
	if stopIdx > rollbackIdx {
		t.Fatalf("revoked users were retained while validating rollback: %v", events)
	}
}
