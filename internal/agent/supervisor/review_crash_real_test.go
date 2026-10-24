//go:build xrayreal

package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

// killRealCore sends SIGKILL to one process the test agent started itself. The
// pid comes from the agent's own pid record, so no unrelated process is touched.
func killRealCore(t *testing.T, pid int) {
	t.Helper()
	if pid <= 1 {
		t.Fatalf("refusing to kill pid %d", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the test core pid %d: %v", pid, err)
	}
}

// waitPortOpen waits until the inbound port of an instance accepts a connection.
func waitPortOpen(t *testing.T, addr string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the inbound port %s never opened: %v", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// portClosed reports whether the inbound port refuses connections.
func portClosed(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

// corePIDOf reads the pid record of an instance. It is the only supported way to
// find the real core process of a test agent: the manager owns it and records it.
func corePIDOf(t *testing.T, root, instanceID string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "state", "instances", instanceID, "xray.pid"))
	if err != nil {
		t.Fatalf("read pid record of %s: %v", instanceID, err)
	}
	var rec struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode pid record: %v", err)
	}
	if rec.PID <= 0 {
		t.Fatalf("the pid record of %s has no pid: %s", instanceID, raw)
	}
	return rec.PID
}

// TestRealCoreCrashIsDetectedAndRecovered kills the real core process out of band
// (SIGKILL, exactly like an OOM kill) and proves that the agent
//   - never advertises the dead core as applied,
//   - restarts it without any new panel revision (the panel answers 204), and
//   - keeps the authorization of the committed record.
//
// Only the process the test agent started itself in this temporary state
// directory is killed; the pid comes from the agent's own pid record.
func TestRealCoreCrashIsDetectedAndRecovered(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real core test in short mode")
	}
	inboundPort := freeTCPPort(t)
	sup, panel, dir := startRealAgent(t)
	ctx := context.Background()
	users := []contracts.AuthorizedUser{{MemberID: "member-crash", Email: "alice@example.test"}}
	panel.setSnapshot("gen-crash", 1, []contracts.Instance{realVLESSEndpoint(t, inboundPort, users)}, 1)
	if err := sup.syncOnce(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	before, err := sup.Store().Instances(ctx)
	if err != nil || len(before) != 1 {
		t.Fatalf("records: %+v err=%v", before, err)
	}
	generationBefore := before[0].CoreGeneration
	pid := corePIDOf(t, dir, "node-1")

	// Kill the real core out of band: no Stop call, no pid file cleanup.
	exit := sup.Manager().ExitSignal("node-1")
	killRealCore(t, pid)
	select {
	case <-exit:
	case <-time.After(10 * time.Second):
		t.Fatal("killed core exit event was not observed")
	}
	if inst := sup.Manager().Snapshot(); len(inst) != 1 || inst[0].State != xray.StateFailed {
		t.Fatalf("the killed core is still advertised as %+v", inst)
	}

	// The panel keeps answering 204 for revision 1: only the health loop can heal
	// this. Give it the full recovery backoff.
	if err := sup.healthOnce(ctx); err != nil {
		t.Fatalf("health recovery: %v", err)
	}
	inst := sup.Manager().Snapshot()
	if len(inst) != 1 || inst[0].State != xray.StateApplied || inst[0].AppliedUserCount != 1 {
		t.Fatalf("the crashed core was never recovered without a new revision: %+v", sup.Manager().Snapshot())
	}
	if newPID := corePIDOf(t, dir, "node-1"); newPID == pid {
		t.Fatalf("the recorded pid was not replaced after the crash: %d", newPID)
	}
	after, err := sup.Store().Instances(ctx)
	if err != nil || len(after) != 1 {
		t.Fatalf("records after recovery: %+v err=%v", after, err)
	}
	if len(after[0].Users) != 1 || after[0].Users[0].Email != "alice@example.test" {
		t.Fatalf("the recovered core lost its authorization: %+v", after[0].Users)
	}
	if after[0].CoreGeneration == generationBefore {
		t.Fatalf("the recovered core reused the generation of the dead process: %q", after[0].CoreGeneration)
	}
	// The recovered core really serves again.
	addr := fmt.Sprintf("127.0.0.1:%d", inboundPort)
	waitPortOpen(t, addr, 20*time.Second)
}

// TestRealCoreOverloadStopIsDurable proves the protection stop against a real
// core: the process is really gone (the inbound port is free again) and the
// durable record does not claim it is running.
func TestRealCoreOverloadStopIsDurable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real core test in short mode")
	}
	inboundPort := freeTCPPort(t)
	sup, panel, _ := startRealAgent(t)
	ctx := context.Background()
	users := []contracts.AuthorizedUser{{MemberID: "member-overload", Email: "alice@example.test"}}
	panel.setSnapshot("gen-overload", 1, []contracts.Instance{realVLESSEndpoint(t, inboundPort, users)}, 1)
	if err := sup.syncOnce(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", inboundPort)
	waitPortOpen(t, addr, 20*time.Second)

	sup.setQueueOverCapacity(true)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if len(sup.Manager().Snapshot()) == 0 && portClosed(addr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the protection stop did not free the inbound port: %+v", sup.Manager().Snapshot())
		}
		time.Sleep(100 * time.Millisecond)
	}
	records, err := sup.Store().Instances(ctx)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	for _, rec := range records {
		if rec.State == xray.StateApplied {
			t.Fatalf("a stopped core is still recorded as applied: %+v", rec)
		}
	}
	// A snapshot that arrives while the protection is engaged must not start a
	// core, even though the state database is perfectly healthy.
	sup.setQueueOverCapacity(true)
	panel.setSnapshot("gen-overload", 2, []contracts.Instance{realVLESSEndpoint(t, inboundPort, users)}, 1)
	if err := sup.syncOnce(ctx); err == nil {
		t.Fatal("a snapshot bypassed the overload start gate")
	}
	if !portClosed(addr) {
		t.Fatal("a core was started while the overload protection was engaged")
	}
}
