package xray

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// procRoleEnv makes the test binary act as a long running helper process so the
// leftover-process reclamation can be tested against a real process.
const procRoleEnv = "AGENT_XRAY_TEST_PROC_ROLE"

func TestMain(m *testing.M) {
	if os.Getenv(procRoleEnv) == "sleep" {
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testBinaryPath(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

// startHelperProcess launches a copy of the test binary that only sleeps. The
// instance config path is passed as an argument so the command line check can be
// satisfied on platforms that expose it.
func startHelperProcess(t *testing.T, cfgPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(testBinaryPath(t), cfgPath)
	cmd.Env = append(os.Environ(), procRoleEnv+"=sleep")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func newStaleManager(t *testing.T, xrayPath string) (*Manager, string, string) {
	t.Helper()
	mgr, err := NewManager(t.TempDir(), xrayPath, newFakeRunner(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	dir, err := mgr.instanceDir("node-1")
	if err != nil {
		t.Fatalf("instanceDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return mgr, dir, filepath.Join(dir, "config.json")
}

// TestReapStaleKillsVerifiedLeftover proves the positive path: a process whose
// persisted identity matches is stopped.
func TestReapStaleKillsVerifiedLeftover(t *testing.T) {
	exe := testBinaryPath(t)
	mgr, dir, cfgPath := newStaleManager(t, exe)
	helper := startHelperProcess(t, cfgPath)
	live, found, err := processIdentity(helper.Process.Pid)
	if err != nil || !found {
		t.Fatalf("processIdentity: found=%v err=%v", found, err)
	}
	if err := writePidFile(dir, pidRecord{
		PID: helper.Process.Pid, Created: live.Created, Executable: live.Executable,
		ConfigPath: cfgPath, InstanceID: "node-1", CoreGeneration: "gen-1",
	}); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	if err := mgr.reapStale(dir, cfgPath); err != nil {
		t.Fatalf("reapStale of a verified leftover process: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, werr := helper.Process.Wait()
		done <- werr
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the verified leftover process was not stopped")
	}
	if _, err := os.Stat(filepath.Join(dir, pidFileName)); !os.IsNotExist(err) {
		t.Fatalf("pid record was not removed: %v", err)
	}
}

// TestReapStaleRefusesForeignProcess proves that a pid which now belongs to
// another program is never killed.
func TestReapStaleRefusesForeignProcess(t *testing.T) {
	mgr, dir, cfgPath := newStaleManager(t, filepath.Join(t.TempDir(), "xray.exe"))
	self, found, err := processIdentity(os.Getpid())
	if err != nil || !found {
		t.Fatalf("processIdentity(self): found=%v err=%v", found, err)
	}
	if err := writePidFile(dir, pidRecord{
		PID: os.Getpid(), Created: self.Created, Executable: self.Executable,
		ConfigPath: cfgPath, InstanceID: "node-1",
	}); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	err = mgr.reapStale(dir, cfgPath)
	if err == nil || !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess, got %v", err)
	}
	if !strings.Contains(err.Error(), "configured core") {
		t.Fatalf("error should explain the executable mismatch: %v", err)
	}
}

// TestReapStaleRefusesReusedPid simulates pid reuse: the recorded creation
// identity does not match the live process.
func TestReapStaleRefusesReusedPid(t *testing.T) {
	self, found, err := processIdentity(os.Getpid())
	if err != nil || !found {
		t.Fatalf("processIdentity(self): %v", err)
	}
	mgr, dir, cfgPath := newStaleManager(t, self.Executable)
	if err := writePidFile(dir, pidRecord{
		PID: os.Getpid(), Created: self.Created + "-reused", Executable: self.Executable,
		ConfigPath: cfgPath, InstanceID: "node-1",
	}); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	err = mgr.reapStale(dir, cfgPath)
	if err == nil || !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("expected ErrUnverifiedProcess for a reused pid, got %v", err)
	}
	if !strings.Contains(err.Error(), "reused") {
		t.Fatalf("error should mention pid reuse: %v", err)
	}
}

// TestReapStaleRefusesMissingIdentity shows that an old plain-pid record (or a
// record without a provable identity) is not trusted.
func TestReapStaleRefusesMissingIdentity(t *testing.T) {
	self, found, err := processIdentity(os.Getpid())
	if err != nil || !found {
		t.Fatalf("processIdentity(self): %v", err)
	}
	mgr, dir, cfgPath := newStaleManager(t, self.Executable)
	if err := writePidFile(dir, pidRecord{PID: os.Getpid(), Executable: self.Executable, ConfigPath: cfgPath}); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	if err := mgr.reapStale(dir, cfgPath); err == nil || !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("a record without a creation identity must not be trusted, got %v", err)
	}
}

// TestReapStaleRefusesOtherInstanceConfig proves the config ownership check.
func TestReapStaleRefusesOtherInstanceConfig(t *testing.T) {
	exe := testBinaryPath(t)
	mgr, dir, cfgPath := newStaleManager(t, exe)
	helper := startHelperProcess(t, cfgPath)
	live, found, err := processIdentity(helper.Process.Pid)
	if err != nil || !found {
		t.Fatalf("processIdentity: %v", err)
	}
	other := filepath.Join(t.TempDir(), "config.json")
	if err := writePidFile(dir, pidRecord{
		PID: helper.Process.Pid, Created: live.Created, Executable: live.Executable,
		ConfigPath: other, InstanceID: "node-1",
	}); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	if err := mgr.reapStale(dir, cfgPath); err == nil || !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("a record for another instance must not be trusted, got %v", err)
	}
}

func TestReapStaleUnreadableRecord(t *testing.T) {
	mgr, dir, cfgPath := newStaleManager(t, filepath.Join(t.TempDir(), "xray.exe"))
	if err := os.WriteFile(filepath.Join(dir, pidFileName), []byte("42"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.reapStale(dir, cfgPath); err == nil || !errors.Is(err, ErrUnverifiedProcess) {
		t.Fatalf("an unreadable record must not be trusted, got %v", err)
	}
}

func TestReapStaleIgnoresMissingProcess(t *testing.T) {
	mgr, dir, cfgPath := newStaleManager(t, filepath.Join(t.TempDir(), "xray.exe"))
	missing := 0
	for pid := 1 << 27; pid > 0; pid-- {
		_, found, err := processIdentity(pid)
		if err == nil && !found {
			missing = pid
			break
		}
	}
	if missing == 0 {
		t.Skip("could not find an unused pid on this host")
	}
	if err := writePidFile(dir, pidRecord{PID: missing, Created: "0", Executable: "x", ConfigPath: cfgPath}); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	if err := mgr.reapStale(dir, cfgPath); err != nil {
		t.Fatalf("a missing process must not be an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, pidFileName)); !os.IsNotExist(err) {
		t.Fatalf("stale pid record was not removed: %v", err)
	}
}

// TestApplyFailsWhenLeftoverProcessUnverified proves the manager refuses to
// start an instance while an unverifiable leftover process exists.
func TestApplyFailsWhenLeftoverProcessUnverified(t *testing.T) {
	self, found, err := processIdentity(os.Getpid())
	if err != nil || !found {
		t.Fatalf("processIdentity(self): %v", err)
	}
	mgr, dir, cfgPath := newStaleManager(t, self.Executable)
	if err := writePidFile(dir, pidRecord{
		PID: os.Getpid(), Created: self.Created + "-other", Executable: self.Executable,
		ConfigPath: cfgPath, InstanceID: "node-1",
	}); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	runner := newFakeRunner()
	mgr.runner = runner
	results := mgr.Apply(t.Context(), []Target{targetFor(t, "alice@example.test")}, ApplyOptions{})
	if len(results) != 1 || results[0].State != StateFailed {
		t.Fatalf("unexpected results: %+v", results)
	}
	if !strings.Contains(results[0].Error, "cannot prove ownership") {
		t.Fatalf("error should explain the unverified process: %q", results[0].Error)
	}
	if runner.startedCount() != 0 {
		t.Fatal("the manager must not start a new core while an unverified process exists")
	}
}

// TestReapStaleSkipsOwnedProcess proves the manager never treats its own running
// core as a leftover process (that would kill a healthy instance and lose the
// final usage sample).
func TestReapStaleSkipsOwnedProcess(t *testing.T) {
	exe := testBinaryPath(t)
	mgr, dir, cfgPath := newStaleManager(t, exe)
	helper := startHelperProcess(t, cfgPath)
	live, found, err := processIdentity(helper.Process.Pid)
	if err != nil || !found {
		t.Fatalf("processIdentity: found=%v err=%v", found, err)
	}
	if err := writePidFile(dir, pidRecord{
		PID: helper.Process.Pid, Created: live.Created, Executable: live.Executable,
		ConfigPath: cfgPath, InstanceID: "node-1",
	}); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	mgr.mu.Lock()
	mgr.owned[helper.Process.Pid] = true
	mgr.mu.Unlock()

	if err := mgr.reapStale(dir, cfgPath); err != nil {
		t.Fatalf("reapStale of an owned process must be a no-op: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, found, err := processIdentity(helper.Process.Pid); err != nil || !found {
		t.Fatalf("the managed process was killed: found=%v err=%v", found, err)
	}
	if _, err := os.Stat(filepath.Join(dir, pidFileName)); err != nil {
		t.Fatalf("the pid record of the running process was removed: %v", err)
	}
}
