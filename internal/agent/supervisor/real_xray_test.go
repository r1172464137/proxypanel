//go:build xrayreal

package supervisor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"proxypanel/internal/agent/config"
	"proxypanel/internal/agent/control"
	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

func realCorePath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("XRAY_BIN")
	if path == "" {
		path = `E:\SDK\Xray\v26.3.27\xray.exe`
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real Xray binary not available (%s): %v", path, err)
	}
	return path
}

// freeTCPPort asks the OS for a dynamic loopback port; no lab or business
// listener is probed or reused by these tests.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func realEchoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func realVLESSEndpoint(t *testing.T, inboundPort int, users []contracts.AuthorizedUser) contracts.Instance {
	t.Helper()
	clients := make([]map[string]any, 0, len(users))
	for _, u := range users {
		clients = append(clients, map[string]any{
			"id":    "01020304-0506-0708-090a-0b0c0d0e0f10",
			"email": u.Email,
		})
	}
	doc := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "vless-in", "listen": "127.0.0.1", "port": inboundPort, "protocol": "vless",
			"settings":       map[string]any{"decryption": "none", "clients": clients},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return contracts.Instance{ID: "node-1", NodeID: "node-1", Config: raw, Users: users}
}

// TestRealCoreLifecycleAndUsage runs the complete agent against the official
// Xray binary: snapshot apply, real proxy traffic, usage upload and revocation.
func TestRealCoreLifecycleAndUsage(t *testing.T) {
	bin := realCorePath(t)
	echoPort := realEchoServer(t)
	inboundPort := freeTCPPort(t)

	panel := &fakePanel{}
	server := httptest.NewServer(panel)
	defer server.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		PanelURL:          server.URL,
		Token:             "integration-token",
		AgentID:           "agent-1",
		XrayPath:          bin,
		StateDir:          filepath.Join(dir, "state"),
		AllowLoopbackHTTP: true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("config: %v", err)
	}
	client, err := control.New(cfg.PanelURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	sup, err := New(Options{Config: cfg, Client: client, Logger: log.New(io.Discard, "", 0), Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sup.Close()
	ctx := context.Background()

	users := []contracts.AuthorizedUser{{MemberID: "member-alice", Email: "alice@example.test"}}
	panel.setSnapshot("gen-real", 1, []contracts.Instance{realVLESSEndpoint(t, inboundPort, users)}, len(users))
	if err := sup.syncOnce(ctx); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}
	reports := panel.appliedReports()
	if len(reports) != 1 || reports[0].State != "applied" {
		t.Fatalf("unexpected apply reports: %+v", reports)
	}

	// Generate real proxy traffic through the core.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inboundPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dial inbound: %v", err)
	}
	header := []byte{0x00}
	header = append(header, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}...)
	header = append(header, 0x00, 0x01)
	header = binary.BigEndian.AppendUint16(header, uint16(echoPort))
	header = append(header, 0x01, 127, 0, 0, 1)
	if _, err := conn.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	payload := []byte("real-core-usage-payload")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("read response header: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	_ = conn.Close()

	// Sample and upload until the panel has a usage sample for the member.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if err := sup.sampleOnce(ctx); err != nil {
			t.Fatalf("sampleOnce: %v", err)
		}
		if err := sup.uploadOnce(ctx); err != nil {
			t.Fatalf("uploadOnce: %v", err)
		}
		panel.mu.Lock()
		batches := append([]contracts.UsageBatch{}, panel.usage...)
		panel.mu.Unlock()
		found := false
		for _, b := range batches {
			for _, s := range b.Samples {
				if s.MemberID == "member-alice" && s.UploadBytes > 0 && s.NodeID == "node-1" {
					found = true
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no usage for member-alice was uploaded (batches=%d)", len(batches))
		}
		time.Sleep(300 * time.Millisecond)
	}

	// Revoke the member: the instance must be restarted without the client.
	empty := realVLESSEndpoint(t, inboundPort, nil)
	panel.setSnapshot("gen-real", 2, []contracts.Instance{empty}, 0)
	if err := sup.syncOnce(ctx); err != nil {
		t.Fatalf("revocation sync: %v", err)
	}
	records, err := sup.Store().Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("unexpected records: %+v", records)
	}
	if records[0].AppliedUserCount != 0 || len(records[0].Users) != 0 {
		t.Fatalf("member still authorized after revocation: %+v", records[0])
	}
	// The core must still answer on its (new) management API and the instance
	// must be reported as applied with zero users.
	stats := sup.Manager().Snapshot()
	if len(stats) != 1 || stats[0].State == "failed" || stats[0].AppliedUserCount != 0 {
		t.Fatalf("unexpected instance state after revocation: %+v", stats)
	}
	// A new connection must be rejected by the core (no authorized users).
	rejected, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inboundPort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial after revocation: %v", err)
	}
	defer rejected.Close()
	_ = rejected.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := rejected.Write(header); err == nil {
		one := make([]byte, 1)
		if _, err := rejected.Read(one); err == nil {
			t.Fatalf("revoked user still received a response")
		}
	}
}

// liveVLESSConn is an established proxy connection that keeps sending and
// receiving through the core, so a revocation has to terminate it actively.
type liveVLESSConn struct {
	conn   net.Conn
	errCh  chan error
	stopCh chan struct{}
	pings  atomic.Int64
}

func startLiveVLESS(t *testing.T, inboundPort, echoPort int) *liveVLESSConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inboundPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dial inbound: %v", err)
	}
	header := []byte{0x00}
	header = append(header, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16)
	header = append(header, 0x00, 0x01)
	header = binary.BigEndian.AppendUint16(header, uint16(echoPort))
	header = append(header, 0x01, 127, 0, 0, 1)
	if _, err := conn.Write(header); err != nil {
		t.Fatalf("write VLESS header: %v", err)
	}
	lc := &liveVLESSConn{conn: conn, errCh: make(chan error, 1), stopCh: make(chan struct{})}
	go lc.pingLoop()
	t.Cleanup(func() { _ = conn.Close() })
	return lc
}

// pingLoop keeps the connection busy: a write plus a full read of the echo per
// round. A deadline error means the connection is still usable, anything else
// means it was terminated.
func (l *liveVLESSConn) pingLoop() {
	payload := []byte("agent-live-revocation-ping")
	buf := make([]byte, len(payload))
	for {
		select {
		case <-l.stopCh:
			return
		default:
		}
		if _, err := l.conn.Write(payload); err != nil {
			l.errCh <- err
			return
		}
		_ = l.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		if _, err := io.ReadFull(l.conn, buf); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			l.errCh <- err
			return
		}
		l.pings.Add(1)
		time.Sleep(100 * time.Millisecond)
	}
}

func (l *liveVLESSConn) waitPings(t *testing.T, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for l.pings.Load() < want {
		select {
		case err := <-l.errCh:
			t.Fatalf("the proxy connection died before the revocation: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the proxy connection carried only %d successful round trips", l.pings.Load())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitClosed waits for the established connection to be terminated and returns
// how long that took. Still-open connections after the budget fail the test.
func (l *liveVLESSConn) waitClosed(t *testing.T, budget time.Duration) time.Duration {
	t.Helper()
	started := time.Now()
	select {
	case err := <-l.errCh:
		elapsed := time.Since(started)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("the old connection was still usable after %s", elapsed)
		}
		return elapsed
	case <-time.After(budget):
		t.Fatalf("the old proxy connection was not terminated within %s", budget)
		return 0
	}
}

func (l *liveVLESSConn) close() {
	close(l.stopCh)
	_ = l.conn.Close()
}

func startRealAgent(t *testing.T) (*Supervisor, *fakePanel, string) {
	t.Helper()
	panel := &fakePanel{}
	server := httptest.NewServer(panel)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	cfg := &config.Config{
		PanelURL:          server.URL,
		Token:             "live-revocation-token",
		AgentID:           "agent-live",
		XrayPath:          realCorePath(t),
		StateDir:          filepath.Join(dir, "state"),
		AllowLoopbackHTTP: true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("config: %v", err)
	}
	client, err := control.New(cfg.PanelURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	sup, err := New(Options{Config: cfg, Client: client, Logger: log.New(io.Discard, "", 0), Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		var exits []<-chan struct{}
		for _, inst := range sup.Manager().Snapshot() {
			if ch := sup.Manager().ExitSignal(inst.InstanceID); ch != nil {
				exits = append(exits, ch)
			}
		}
		if err := sup.Close(); err != nil {
			t.Errorf("owned core cleanup: %v", err)
		}
		for _, ch := range exits {
			select {
			case <-ch:
			case <-time.After(10 * time.Second):
				t.Error("owned core exit not confirmed during cleanup")
			}
		}
		left, err := filepath.Glob(filepath.Join(dir, "state", "instances", "*", "xray.pid"))
		if err != nil || len(left) > 0 {
			t.Errorf("owned pid recovery records remain: %v err=%v", left, err)
		}
		t.Log("cleanup: all owned exit events confirmed; no owned pid records remain")
	})
	return sup, panel, dir
}

// TestRealCoreRevokeTerminatesEstablishedConnection proves the revocation
// requirement with the real core: an established TCP session through the proxy
// must be terminated (not merely refused for new connections) within 60 seconds.
func TestRealCoreRevokeTerminatesEstablishedConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real core test in short mode")
	}
	const budget = 60 * time.Second
	users := []contracts.AuthorizedUser{{MemberID: "member-live", Email: "alice@example.test"}}

	t.Run("empty snapshot stops the instance", func(t *testing.T) {
		echoPort := realEchoServer(t)
		inboundPort := freeTCPPort(t)
		sup, panel, _ := startRealAgent(t)
		ctx := context.Background()
		panel.setSnapshot("gen-live", 1, []contracts.Instance{realVLESSEndpoint(t, inboundPort, users)}, 1)
		if err := sup.syncOnce(ctx); err != nil {
			t.Fatalf("apply: %v", err)
		}
		live := startLiveVLESS(t, inboundPort, echoPort)
		defer live.close()
		live.waitPings(t, 2, 20*time.Second)

		revokeStarted := time.Now()
		panel.setSnapshot("gen-live", 2, nil, 0)
		if err := sup.syncOnce(ctx); err != nil {
			t.Fatalf("empty snapshot: %v", err)
		}
		live.waitClosed(t, budget)
		total := time.Since(revokeStarted)
		if total > budget {
			t.Fatalf("revocation took %s", total)
		}
		t.Logf("established connection terminated %s after the revocation started (budget %s)", total.Round(time.Millisecond), budget)
		if got := sup.Manager().Snapshot(); len(got) != 0 {
			t.Fatalf("the instance is still tracked: %+v", got)
		}
		if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inboundPort), 2*time.Second); err == nil {
			t.Fatal("new connections must be refused after the instance was removed")
		}
	})

	t.Run("local expiry rebuilds without the user", func(t *testing.T) {
		echoPort := realEchoServer(t)
		inboundPort := freeTCPPort(t)
		sup, panel, _ := startRealAgent(t)
		ctx := context.Background()
		panel.setSnapshot("gen-live", 1, []contracts.Instance{realVLESSEndpoint(t, inboundPort, users)}, 1)
		if err := sup.syncOnce(ctx); err != nil {
			t.Fatalf("apply: %v", err)
		}
		live := startLiveVLESS(t, inboundPort, echoPort)
		defer live.close()
		live.waitPings(t, 2, 20*time.Second)
		// Updating only a future expiry must take the unchanged-hash path while
		// still persisting the new user metadata for offline enforcement.
		before, err := sup.Store().Instances(ctx)
		if err != nil || len(before) != 1 {
			t.Fatalf("initial records: %+v, err=%v", before, err)
		}
		expiry := time.Now().Add(4 * time.Second)
		expiringUsers := []contracts.AuthorizedUser{{MemberID: "member-live", Email: "alice@example.test", ExpiresAt: &expiry}}
		panel.setSnapshot("gen-live", 2, []contracts.Instance{realVLESSEndpoint(t, inboundPort, expiringUsers)}, 1)
		if err := sup.syncOnce(ctx); err != nil {
			t.Fatalf("expiry-only update: %v", err)
		}
		after, err := sup.Store().Instances(ctx)
		if err != nil || len(after) != 1 {
			t.Fatalf("updated records: %+v, err=%v", after, err)
		}
		if after[0].State != xray.StateUnchanged || after[0].CoreGeneration != before[0].CoreGeneration || after[0].ConfigHash != before[0].ConfigHash {
			t.Fatal("the expiry-only update restarted or reconfigured the core")
		}
		if len(after[0].Users) != 1 || after[0].Users[0].ExpiresAt == nil || !after[0].Users[0].ExpiresAt.Equal(expiry) {
			t.Fatalf("the updated user expiry was not persisted: %+v", after[0].Users)
		}

		// The panel is not involved: only the locally stored expiry applies.
		for time.Now().Before(expiry) {
			time.Sleep(100 * time.Millisecond)
		}
		revokeStarted := time.Now()
		if err := sup.expiryOnce(ctx); err != nil {
			t.Fatalf("expiryOnce: %v", err)
		}
		rebuild := time.Since(revokeStarted)
		live.waitClosed(t, budget)
		if rebuild > 10*time.Second {
			t.Fatalf("the local expiry rebuild was too slow: %s", rebuild)
		}
		// The last usage of the terminated connections must have been collected
		// before the core was stopped.
		pending, err := sup.Store().PendingBatches(ctx, 10)
		if err != nil {
			t.Fatalf("pending batches: %v", err)
		}
		var upload int64
		for _, batch := range pending {
			for _, sample := range batch.Samples {
				upload += sample.UploadBytes
			}
		}
		if upload <= 0 {
			t.Fatal("the final usage sample was lost when the core was rebuilt")
		}
		t.Logf("final usage preserved: %d upload bytes across %d batch(es)", upload, len(pending))
		total := time.Since(revokeStarted)
		if total > budget {
			t.Fatalf("local expiry revocation took %s", total)
		}
		t.Logf("established connection terminated %s after the local expiry rebuild started (budget %s)", total.Round(time.Millisecond), budget)
		instances := sup.Manager().Snapshot()
		if len(instances) != 1 || instances[0].AppliedUserCount != 0 {
			t.Fatalf("the instance still authorizes users: %+v", instances)
		}
	})
}

// dialLiveVLESS retries until the core accepts the handshake, so the test does not
// depend on how fast the real process binds its inbound.
func dialLiveVLESS(t *testing.T, inboundPort, echoPort int, budget time.Duration) *liveVLESSConn {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inboundPort), 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return startLiveVLESS(t, inboundPort, echoPort)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the core never accepted connections on port %d: %v", inboundPort, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitRealRevision(t *testing.T, sup *Supervisor, revision int64, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		identity, err := sup.Store().Identity(context.Background())
		if err == nil && identity.AppliedRevision >= revision {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("revision %d was not applied in time (identity=%+v err=%v)", revision, identity, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRealCoreRunConcurrentLoops runs the complete agent (snapshot, stats, upload,
// heartbeat and expiry loops) against the official core while instances are
// revoked and re-granted, then requires a bounded shutdown with no leftover
// process. It is the real-core counterpart of the fake-runner concurrency
// regression.
func TestRealCoreRunConcurrentLoops(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real core test in short mode")
	}
	echoPort := realEchoServer(t)
	inboundPort := freeTCPPort(t)
	panel := &fakePanel{}
	server := httptest.NewServer(panel)
	defer server.Close()
	dir := t.TempDir()
	cfg := &config.Config{
		PanelURL:                 server.URL,
		Token:                    "real-loops-token",
		AgentID:                  "agent-real-loops",
		XrayPath:                 realCorePath(t),
		StateDir:                 filepath.Join(dir, "state"),
		PollIntervalSeconds:      1,
		StatsIntervalSeconds:     1,
		HeartbeatIntervalSeconds: 1,
		AllowLoopbackHTTP:        true,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("config: %v", err)
	}
	client, err := control.New(cfg.PanelURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	sup, err := New(Options{Config: cfg, Client: client, Logger: log.New(io.Discard, "", 0), Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer sup.Close()

	users := []contracts.AuthorizedUser{{MemberID: "member-live", Email: "alice@example.test"}}
	panel.setSnapshot("gen-real", 1, []contracts.Instance{realVLESSEndpoint(t, inboundPort, users)}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	waitRealRevision(t, sup, 1, 25*time.Second)
	first := dialLiveVLESS(t, inboundPort, echoPort, 25*time.Second)
	first.waitPings(t, 2, 25*time.Second)

	// Revoke, then re-grant, while every loop is running.
	panel.setSnapshot("gen-real", 2, []contracts.Instance{realVLESSEndpoint(t, inboundPort, nil)}, 0)
	waitRealRevision(t, sup, 2, 25*time.Second)
	first.waitClosed(t, 60*time.Second)

	panel.setSnapshot("gen-real", 3, []contracts.Instance{realVLESSEndpoint(t, inboundPort, users)}, 1)
	waitRealRevision(t, sup, 3, 25*time.Second)
	second := dialLiveVLESS(t, inboundPort, echoPort, 25*time.Second)
	second.waitPings(t, 2, 25*time.Second)
	second.close()

	// Usage must have been sampled and uploaded by the loops.
	deadline := time.Now().Add(20 * time.Second)
	for {
		panel.mu.Lock()
		batches := len(panel.usage)
		panel.mu.Unlock()
		if batches > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the real loops never uploaded a usage batch")
		}
		time.Sleep(200 * time.Millisecond)
	}

	shutdownStarted := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("Run did not shut down in bounded time")
	}
	t.Logf("agent shut down in %s", time.Since(shutdownStarted).Round(time.Millisecond))
	if got := sup.Manager().Snapshot(); len(got) > 0 {
		for _, inst := range got {
			if inst.State == xray.StateApplied {
				t.Fatalf("core still running after shutdown: %+v", inst)
			}
		}
	}
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inboundPort), 2*time.Second); err == nil {
		conn.Close()
		t.Fatal("the core is still listening after shutdown")
	}
}

// TestRealRestoreDropsPersistedExpiredUser is the real-binary evidence for the
// A14 copy: a persisted instance record can still list a user whose delivered
// ExpiresAt already passed (for example when a shutdown interrupted the local
// expiry rebuild). An actual core restart must authorize only the users that are
// still valid - the stored record alone never revives access.
func TestRealRestoreDropsPersistedExpiredUser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real core test in short mode")
	}
	sup, _, _ := startRealAgent(t)
	ctx := context.Background()
	inboundPort := freeTCPPort(t)
	past := time.Now().UTC().Add(-time.Minute)
	future := time.Now().UTC().Add(time.Hour)
	users := []contracts.AuthorizedUser{
		{MemberID: "member-expired", Email: "expired@example.test", ExpiresAt: &past},
		{MemberID: "member-live", Email: "live@example.test", ExpiresAt: &future},
	}
	instance := realVLESSEndpoint(t, inboundPort, users)
	if err := sup.Store().RecordInstance(ctx, state.InstanceRecord{
		NodeID:     "node-1",
		State:      xray.StateStopped,
		Config:     instance.Config,
		Users:      instance.Users,
		NextExpiry: past,
		UpdatedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed the persisted record: %v", err)
	}
	if err := sup.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// The real core must be running and reachable ...
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inboundPort), 5*time.Second)
	if err != nil {
		t.Fatalf("the restored core is not listening: %v", err)
	}
	_ = conn.Close()
	// ... with only the still-valid client in the configuration it was given.
	raw, err := os.ReadFile(filepath.Join(sup.Store().Dir(), "instances", "node-1", "config.json"))
	if err != nil {
		t.Fatalf("read the restored core configuration: %v", err)
	}
	if got := countClients(string(raw)); got != 1 {
		t.Fatalf("the restored core authorizes %d client(s), want 1", got)
	}
	if !strings.Contains(string(raw), "live@example.test") || strings.Contains(string(raw), "expired@example.test") {
		t.Fatal("the restored core kept the already expired client")
	}
	records, err := sup.Store().Instances(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %+v err=%v", records, err)
	}
	if len(records[0].Users) != 1 || records[0].Users[0].MemberID != "member-live" {
		t.Fatalf("restore did not persist the filtered authorization: %+v", records[0].Users)
	}
	if !records[0].NextExpiry.Equal(future.Truncate(time.Millisecond)) {
		t.Fatalf("restored expiry trigger = %v, want %v", records[0].NextExpiry, future.Truncate(time.Millisecond))
	}
}
