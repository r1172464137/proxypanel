package xray

// Regression for the orphan-core reclaim failure that only appears on a host
// whose boot time drifts.
//
// Observed on WSL2 against a real core: after the agent process was killed, five
// xray processes survived as orphans holding the inbound ports. The restarted
// agent refused to reclaim any of them:
//
//	instance nd_…: leftover core process could not be reclaimed: cannot prove
//	ownership of a leftover core process: pid 68296 creation identity changed
//	(recorded 1792642247/523740, live 1792642344/523740); the pid was reused
//
// The starttime component (523740) was identical - it really was the process the
// agent had started. Only the boot-time prefix had moved, because /proc/stat's
// btime is derived from the current wall clock minus uptime and is recomputed
// whenever the clock is adjusted or the host is suspended. Every instance stayed
// failed, a second core was started onto the same port, and no retry could heal
// it without an operator killing the processes by hand.
//
// These tests pin both halves: normal clock drift must not be mistaken for pid
// reuse, and a genuinely reused pid (different starttime) must still be refused.

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestCreationIdentityToleratesBootClockDrift(t *testing.T) {
	cases := []struct {
		name     string
		recorded string
		live     string
		want     bool
	}{
		// The exact pair from the failing run.
		{"observed drift", "1792642247/523740", "1792642344/523740", true},
		{"identical", "1792642350/523740", "1792642350/523740", true},
		{"drift under a minute", "1792642350/42", "1792642391/42", true},
		{"drift exactly at tolerance", "1792642350/42", "1792645950/42", true},
		{"drift just over tolerance", "1792642350/42", "1792645951/42", false},
		// A real reboot moves btime by the previous uptime, which is far larger.
		{"reboot", "1792560000/42", "1792642350/42", false},
		// starttime is the authoritative half: a different start with the same
		// boot time is a different process.
		{"same boot different start", "1792642350/42", "1792642350/43", false},
		{"drift but different start", "1792642247/523740", "1792642344/523741", false},
		// An unknown boot component must not contradict a matching starttime...
		{"unknown recorded boot", "?/42", "1792642350/42", true},
		{"unknown live boot", "1792642350/42", "?/42", true},
		// ...but a malformed identity falls back to strict equality.
		{"garbage both sides", "garbage", "garbage", true},
		{"garbage vs real", "garbage", "1792642350/42", false},
		{"missing separator", "1792642350", "1792642350", true},
		{"missing start", "1792642350/", "1792642350/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := creationIdentityMatches(tc.recorded, tc.live); got != tc.want {
				t.Fatalf("creationIdentityMatches(%q, %q) = %v, want %v",
					tc.recorded, tc.live, got, tc.want)
			}
		})
	}
}

// A leftover core whose recorded identity carries a drifted boot-time prefix must
// be reclaimable, otherwise the instance can never recover on a host whose boot
// time moves.
func TestReclaimToleratesBootClockDrift(t *testing.T) {
	shortStopVerify(t)
	exe := testBinaryPath(t)
	root := t.TempDir()
	mgr, _ := newScriptedManager(t, exe, root)

	dir, err := mgr.instanceDir("node-1")
	if err != nil {
		t.Fatalf("instanceDir: %v", err)
	}
	addr := freeListenAddress(t)
	helper := startPortHoldingHelper(t, filepath.Join(dir, "config.json"), addr)

	live, ok, err := processIdentity(helper.Process.Pid)
	if err != nil || !ok {
		t.Skipf("process identity unavailable on this platform: %v", err)
	}
	boot, start, split := splitCreationIdentity(live.Created)
	if !split {
		t.Fatalf("unexpected identity format %q", live.Created)
	}
	driftedBoot, err := strconv.ParseInt(boot, 10, 64)
	if err != nil {
		t.Skipf("boot time component %q is not numeric: %v", boot, err)
	}
	// Move the prefix back by 97 seconds: the exact shape of the real failure.
	drifted := strconv.FormatInt(driftedBoot-97, 10) + "/" + start

	if err := writePidFile(dir, pidRecord{
		PID:        helper.Process.Pid,
		Created:    drifted,
		Executable: live.Executable,
		ConfigPath: filepath.Join(dir, "config.json"),
		InstanceID: "node-1",
		RecordedAt: 1,
	}); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	if err := mgr.reapStale(dir); err != nil {
		t.Fatalf("a leftover core with a drifted boot time must be reclaimable, got: %v", err)
	}
	if !waitPortReleased(t, addr, 5*time.Second) {
		t.Fatalf("%s is still held after the reclaim", addr)
	}
}

// waitPortReleased polls until the address is free. reapStale sends SIGKILL and
// returns immediately, so the kernel may need a moment to close the listening
// socket; sampling once would make this test flaky under load.
func waitPortReleased(t *testing.T, addr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !portIsBusy(t, addr) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return !portIsBusy(t, addr)
}

// The safety property must survive the tolerance: a pid whose starttime differs
// (a genuine reuse) is still refused, and the process is left alone.
func TestReclaimStillRefusesAReusedPid(t *testing.T) {
	shortStopVerify(t)
	exe := testBinaryPath(t)
	root := t.TempDir()
	mgr, _ := newScriptedManager(t, exe, root)

	dir, err := mgr.instanceDir("node-1")
	if err != nil {
		t.Fatalf("instanceDir: %v", err)
	}
	addr := freeListenAddress(t)
	helper := startPortHoldingHelper(t, filepath.Join(dir, "config.json"), addr)

	live, ok, err := processIdentity(helper.Process.Pid)
	if err != nil || !ok {
		t.Skipf("process identity unavailable on this platform: %v", err)
	}
	boot, start, split := splitCreationIdentity(live.Created)
	if !split {
		t.Fatalf("unexpected identity format %q", live.Created)
	}
	startTicks, err := strconv.ParseInt(start, 10, 64)
	if err != nil {
		t.Skipf("starttime component %q is not numeric: %v", start, err)
	}
	bumped := boot + "/" + strconv.FormatInt(startTicks+1, 10)

	if err := writePidFile(dir, pidRecord{
		PID:        helper.Process.Pid,
		Created:    bumped,
		Executable: live.Executable,
		ConfigPath: filepath.Join(dir, "config.json"),
		InstanceID: "node-1",
		RecordedAt: 1,
	}); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	if err := mgr.reapStale(dir); err == nil {
		t.Fatal("a pid with a different starttime must be refused, not reclaimed")
	}
	if !portIsBusy(t, addr) {
		t.Fatal("the mismatched process must not have been killed")
	}
}
