package e2e

// Owned dual-Agent / offline / real-UDP acceptance for the web-demo phase (task B).
//
// Story coverage (see .ai/super-run/product-readiness/web-demo.md and
// .ai/user-stories/core/product-readiness.md):
//
//	US-PR-011  an offline node does not block revocation/update of the reachable
//	           node; the recovered hop is tightened, never widened; an already
//	           dispatched expiry is enforced inside the 60s window; new and
//	           existing connections are refused, not just an API status.
//	US-PR-012  per-object batch state/taskID/attempts/revision is traceable for two
//	           real targets; a stale ACK never completes a newer task; a document
//	           the real core rejects cannot drag an unrelated node.
//	US-PR-014  a real core crash and a real technical rollback keep the legal
//	           current authorization and never revive a withdrawn credential.
//	US-PR-017  a member cannot read admin batch data; the evidence contains no
//	           password, token or credential material.
//
// Everything is owned: a private PostgreSQL cluster/database created by
// run_webdemo_dual.py (database prefix wdd_owned_), a random schema, two real
// official Xray cores driven by two real supervisors, runtime loopback-only
// ports and a private TCP/UDP echo target. No shared fixture is touched, no
// 8443/8445/agent-lab service is used, and no business result is fabricated with
// SQL: every state below comes from the real panel API, the real Agent supervisor
// or a real Xray process. Read-only SQL is used only to read back what the panel
// persisted (revision, task, receipt, credential state) as evidence.
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"

	agentconfig "proxypanel/internal/agent/config"
	"proxypanel/internal/agent/control"
	agentstate "proxypanel/internal/agent/state"
	"proxypanel/internal/agent/supervisor"
	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/api"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

// dualPassword is the only password of this fixture. It is asserted NOT to
// appear in the written evidence.
const dualPassword = "Wdd-owned-pw-2f4c9a1e-never-evidence"

// dualLog is a concurrency-safe log sink for sanitized Agent diagnostics.
type dualLog struct {
	mu  chan struct{}
	buf []byte
}

func newDualLog() *dualLog {
	l := &dualLog{mu: make(chan struct{}, 1)}
	l.mu <- struct{}{}
	return l
}
func (b *dualLog) Write(p []byte) (int, error) {
	<-b.mu
	b.buf = append(b.buf, p...)
	b.mu <- struct{}{}
	return len(p), nil
}
func (b *dualLog) String() string {
	<-b.mu
	s := string(b.buf)
	b.mu <- struct{}{}
	return s
}

type dualAgent struct {
	serverID string
	nodeID   string
	nodePort int
	stateDir string
	sup      *supervisor.Supervisor
	cancel   context.CancelFunc
	done     chan error
	token    string
	agentID  string
	logs     *dualLog
	running  bool
}

type dualTarget struct {
	http *httptest.Server
	udp  net.PacketConn
}

type dualProxy struct {
	client    *http.Client
	transport *http.Transport
	socks     string
	tag       string
}

type dualFixture struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	panel  *httptest.Server
	admin  *dataPlaneAPI
	bin    string
	schema string
	member map[string]string // id, username, token
	carol  map[string]string
	agents []*dualAgent
	nodes  []string
	group  string
	target *dualTarget
	facts  map[string]any // evidence, written even when the test fails later
	trace  []string
}

func (f *dualFixture) note(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	f.t.Log(line)
	f.trace = append(f.trace, line)
}

// TestWebDemoDualAgent is the single owned acceptance entry point. The owned
// runner selects exactly this test.
func TestWebDemoDualAgent(t *testing.T) {
	dsn := os.Getenv("PP_DEMO_DUAL_DATABASE_URL")
	bin := os.Getenv("PP_DEMO_DUAL_XRAY_BIN")
	if dsn == "" || bin == "" {
		t.Fatal("not_run: PP_DEMO_DUAL_DATABASE_URL and PP_DEMO_DUAL_XRAY_BIN are required")
	}
	dbc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid owned database URL")
	}
	if dbc.Host != "127.0.0.1" || !strings.HasPrefix(dbc.Database, "wdd_owned_") {
		t.Fatal("refusing a non-owned PostgreSQL target (need 127.0.0.1/wdd_owned_*)")
	}
	ctx := context.Background()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect owned PostgreSQL failed")
	}
	defer db.Close(ctx)
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "wdd_" + hex.EncodeToString(random)
	if _, err := db.Exec(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := db.Exec(clean, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error("owned schema cleanup failed")
		} else {
			t.Log("cleanup: owned schema dropped")
		}
	}()
	key, err := secrets.ParseKey(strings.Repeat("wdd-owned-random-schema-", 3))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{DatabaseURL: dsn, MasterKey: key, MigrationsDir: "../../migrations", SearchPath: schema})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hash, err := secrets.HashPassword(dualPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "admin",
		PasswordHash: hash, Role: "admin", Active: true, ProxyEnabled: true}); err != nil {
		t.Fatal(err)
	}
	limiter, err := limit.New("") // process-local limiter: no shared Redis writes
	if err != nil {
		t.Fatal(err)
	}
	defer limiter.Close()
	panel := &api.Server{Store: st, RateLimiter: limiter,
		Cfg: api.Config{Dev: true, Listen: "127.0.0.1:8080", SessionTTL: time.Hour}}
	server := httptest.NewServer(panel.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	admin := &dataPlaneAPI{t: t, base: server.URL, client: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	admin.login("admin", dualPassword)

	f := &dualFixture{t: t, ctx: ctx, st: st, panel: server, admin: admin, bin: bin,
		schema: schema, facts: map[string]any{}}
	f.facts["schema"] = schema
	f.facts["panel_url"] = server.URL
	f.facts["core_binary"] = bin
	if sum, err := os.ReadFile(bin); err == nil {
		f.facts["core_sha256"] = fmt.Sprintf("%x", sha256.Sum256(sum))
	}
	t.Cleanup(f.writeEvidence)
	t.Cleanup(func() {
		for i := range f.agents {
			f.stopAgentBestEffort(i)
		}
	})
	f.note("owned real fixture: schema=%s panel=%s core=%s", schema, server.URL, bin)

	// --- one panel, two real servers/nodes, one group/subscription, two members
	groupNodes := make([]any, 0, 2)
	for i := 0; i < 2; i++ {
		srv := admin.request("POST", "/api/v1/admin/servers", map[string]any{
			"name": fmt.Sprintf("wdd-server-%d", i+1), "address": "127.0.0.1", "region": "wdd-owned"})
		port := unusedPort(t)
		node := admin.request("POST", "/api/v1/admin/nodes", map[string]any{
			"name": fmt.Sprintf("wdd-vless-%d", i+1), "server_id": srv["id"], "enabled": true,
			"host": "127.0.0.1", "listen": "127.0.0.1", "port": port, "protocol": "vless",
			"stream_settings":   map[string]any{"network": "tcp", "security": "none"},
			"protocol_settings": map[string]any{"decryption": "none"}, "client_settings": map[string]any{}})
		groupNodes = append(groupNodes, node["id"])
		a := &dualAgent{serverID: srv["id"].(string), nodeID: node["id"].(string), nodePort: port,
			stateDir: filepath.Join(t.TempDir(), fmt.Sprintf("agent-%d", i+1)), logs: newDualLog()}
		f.agents = append(f.agents, a)
		f.nodes = append(f.nodes, a.nodeID)
		f.note("node %d: id=%s server=%s loopback port=%d", i+1, a.nodeID, a.serverID, port)
	}
	group := admin.request("POST", "/api/v1/admin/node-groups", map[string]any{"name": "wdd-group", "node_ids": groupNodes})
	f.group = group["id"].(string)
	sub := admin.request("POST", "/api/v1/admin/subscriptions", map[string]any{
		"name": "wdd-sub", "node_group_ids": []any{group["id"]}, "quota_bytes": 1 << 30,
		"timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true})
	alice := admin.request("POST", "/api/v1/admin/members", map[string]any{
		"username": "wdd-alice", "password": dualPassword, "role": "member", "subscription_id": sub["id"]})
	f.member = map[string]string{"id": alice["id"].(string), "username": "wdd-alice"}
	carol := admin.request("POST", "/api/v1/admin/members", map[string]any{
		"username": "wdd-carol", "password": dualPassword, "role": "member", "subscription_id": sub["id"]})
	f.carol = map[string]string{"id": carol["id"].(string), "username": "wdd-carol"}
	f.facts["member"] = f.member["id"]
	f.facts["second_member"] = f.carol["id"]

	// --- private TCP/UDP echo target (owned, loopback, random port) -----------
	payload := strings.Repeat("wdd-verified-payload-", 3164) // 63280 bytes
	expectedHash := sha256.Sum256([]byte(payload))
	f.target = &dualTarget{}
	f.target.http = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream" {
			_, _ = io.WriteString(w, payload)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		for {
			if _, err := io.WriteString(w, "wdd-persistent\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(f.target.http.Close)
	udpEcho, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.target.udp = udpEcho
	t.Cleanup(func() { udpEcho.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := udpEcho.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udpEcho.WriteTo(buf[:n], addr)
		}
	}()
	f.facts["target_tcp"] = f.target.http.URL
	f.facts["target_udp"] = udpEcho.LocalAddr().String()
	f.facts["fixture_bytes"] = len(payload)
	f.facts["fixture_sha256"] = fmt.Sprintf("%x", expectedHash)
	f.note("owned target tcp=%s udp=%s fixture=%d bytes sha256=%x",
		strings.TrimPrefix(f.target.http.URL, "http://"), udpEcho.LocalAddr(), len(payload), expectedHash)

	// --- two real Agents, one registration code each -------------------------
	for i, a := range f.agents {
		code := admin.request("POST", "/api/v1/admin/servers/"+a.serverID+"/registration-codes", nil)
		f.startAgent(i, code["code"].(string))
	}
	f.waitApplied(90 * time.Second)
	f.note("two real Agents registered separately and applied the same authoritative snapshot")

	aliceCfg, aliceToken := f.export("wdd-alice")
	f.member["token"] = aliceToken
	clients := []*dualProxy{f.clientFor(0, aliceCfg), f.clientFor(1, aliceCfg)}
	f.note("member subscription token issued (len=%d; value redacted in evidence)", len(aliceToken))
	for i, c := range clients {
		if err := f.probeTCP(c, payload, expectedHash); err != nil {
			t.Fatalf("positive control on node %d before any fault failed: %v", i+1, err)
		}
		want := []byte(fmt.Sprintf("wdd-real-udp-node-%d", i+1))
		got, err := f.probeUDP(c, want)
		if err != nil {
			t.Fatalf("real official-core UDP through node %d failed: %v", i+1, err)
		}
		if string(got) != string(want) {
			t.Fatalf("UDP echo mismatch on node %d: %q", i+1, got)
		}
		f.note("node %d: real TCP %d bytes (SHA256 match) + real SOCKS5 UDP echo %d bytes verified by real server core", i+1, len(payload), len(got))
	}

	// --- US-PR-011 (offline/late case): dispatched expiry is bounded ----------
	carolCfg, _ := f.export("wdd-carol")
	carolClient := f.clientFor(0, carolCfg)
	if err := f.probeTCP(carolClient, payload, expectedHash); err != nil {
		t.Fatalf("carol positive control failed: %v", err)
	}
	expiry := time.Now().Add(15 * time.Second).Truncate(time.Second)
	expiryBatch := f.submitBatch("set_expiry", []any{f.carol["id"]},
		map[string]any{"expires_at": expiry.UTC().Format(time.RFC3339)})
	f.waitApplied(90 * time.Second)
	expD, expA := f.revisionPair(0)
	items := f.batchItems(expiryBatch)
	if len(items) != 2 {
		t.Fatalf("expiry batch must dispatch to both real targets, got %d item(s)", len(items))
	}
	expiryRefusal := ""
	for time.Now().Before(expiry.Add(60 * time.Second)) {
		if err := f.probeTCP(carolClient, payload, expectedHash); err != nil {
			expiryRefusal = err.Error()
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if expiryRefusal == "" {
		t.Fatal("the dispatched expiry did not stop real traffic within 60s of expires_at")
	}
	lateD, lateA := f.revisionPair(0)
	if lateD != expD || lateA != expA {
		t.Fatalf("expiry enforcement needed a new revision (desired %d->%d applied %d->%d): not a local bounded enforcement", expD, lateD, expA, lateA)
	}
	f.note("expiry: carol refused %.1fs after expires_at with %q, no new revision (desired stays %d applied=%d)",
		time.Since(expiry).Seconds(), expiryRefusal, lateD, lateA)
	f.facts["expiry"] = map[string]any{"expires_at": expiry.UTC().Format(time.RFC3339),
		"denied_after_seconds": time.Since(expiry).Seconds(), "desired_revision": lateD,
		"applied_revision": lateA, "refusal": expiryRefusal,
		"items": items}

	// --- US-PR-012: a rejected document cannot drag an unrelated node ---------
	f.patchNode(f.nodes[0], map[string]any{"protocol_settings": map[string]any{
		"decryption": "none", "fallbacks": "not-an-array"}})
	f.note("invalid Xray document dispatched to node 1 only; the real core must reject it")
	f.waitCondition(120*time.Second, "node 1 documents diverged (desired>applied)", func() bool {
		d, a := f.revisionPair(0)
		return d > a
	})
	for i := 0; i < 5; i++ {
		if err := f.probeTCP(clients[1], payload, expectedHash); err != nil {
			t.Fatalf("node 2 stopped serving while node 1 had a rejected document: %v", err)
		}
		time.Sleep(time.Second)
	}
	f.waitCondition(150*time.Second, "node 1 serves again from its last known good document", func() bool {
		return f.probeTCP(clients[0], payload, expectedHash) == nil
	})
	f.note("node 2 served 5 consecutive probes while node 1 had a rejected document; node 1 recovered from last-known-good; invalid document never applied")
	f.patchNode(f.nodes[0], map[string]any{"protocol_settings": map[string]any{"decryption": "none"}})
	f.waitApplied(150 * time.Second)
	f.note("valid document restored and converged on both Agents")

	// --- US-PR-011: one Agent offline, the other one revokes anyway ----------
	streamClient := &http.Client{Transport: clients[0].transport} // no Client.Timeout: the deadline measures revocation
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	streamReq, _ := http.NewRequestWithContext(streamCtx, "GET", f.target.http.URL+"/stream", nil)
	stream, err := streamClient.Do(streamReq)
	if err != nil {
		t.Fatalf("open persistent proxied stream on node 1 failed: %v", err)
	}
	defer stream.Body.Close()
	first := make([]byte, 15)
	if _, err := io.ReadFull(stream.Body, first); err != nil {
		t.Fatalf("persistent stream never became active: %v", err)
	}
	streamDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stream.Body); close(streamDone) }()

	f.stopAgent(1)
	f.note("owned failure injection: Agent 2 / node 2 stopped (offline)")
	offlineEvidence := ""
	if err := f.probeTCP(clients[1], payload, expectedHash); err == nil {
		t.Fatal("node 2 still served after its Agent was stopped: the offline fixture is not real")
	} else {
		offlineEvidence = err.Error()
	}
	f.agents[1].token, f.agents[1].agentID = f.agentToken(1)
	f.note("node 2 offline evidence: %q", offlineEvidence)

	revokeBatch := f.submitBatch("set_active", []any{f.member["id"]}, map[string]any{"active": false})
	revokedAt := time.Now()
	select {
	case <-streamDone:
		f.note("existing TCP stream on node 1 terminated %.2fs after revocation (Agent 2 offline)",
			time.Since(revokedAt).Seconds())
	case <-time.After(60 * time.Second):
		t.Fatal("existing connection survived the 60s revocation window while the other Agent was offline")
	}
	newRefusal := ""
	for time.Now().Before(revokedAt.Add(60 * time.Second)) {
		if err := f.probeTCP(clients[0], payload, expectedHash); err != nil {
			newRefusal = err.Error()
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if newRefusal == "" {
		t.Fatal("the reachable node accepted a new connection after revocation")
	}
	if elapsed := time.Since(revokedAt); elapsed > 60*time.Second {
		t.Fatalf("reachable node denial exceeded 60s: %s", elapsed)
	}
	revD1, revA1 := f.revisionPair(0)
	if revD1 == revA1 {
		t.Fatal("the reachable Agent should have applied the revocation revision")
	}
	revD2, revA2 := f.revisionPair(1)
	if revD2 <= revA2 {
		t.Fatalf("the offline Agent should stay behind (desired=%d applied=%d)", revD2, revA2)
	}
	f.note("node 1 denied new connections %.2fs after revocation (%q) while node 2 stayed offline; node1 rev %d/%d node2 rev %d/%d",
		time.Since(revokedAt).Seconds(), newRefusal, revD1, revA1, revD2, revA2)

	// Per-object dispatch traceability for both real targets.
	items = f.batchItems(revokeBatch)
	if len(items) != 2 {
		t.Fatalf("revocation batch must have one item per real target, got %d", len(items))
	}
	itemFacts := make([]map[string]any, 0, 2)
	seenAgents := map[string]bool{}
	for _, it := range items {
		agentID, _ := it["agent_id"].(string)
		taskID, _ := it["task_id"].(string)
		state, _ := it["state"].(string)
		nodeID, _ := it["node_id"].(string)
		if agentID == "" || taskID == "" || nodeID == "" {
			t.Fatalf("batch item lacks agent/task/node trace: %+v", it)
		}
		seenAgents[agentID] = true
		var attempts, maxAttempts int
		var revision int64
		var taskState string
		if err := st.DB.QueryRowContext(ctx, `SELECT attempts, max_attempts, coalesce(revision,0), state
			FROM tasks WHERE id=$1`, taskID).Scan(&attempts, &maxAttempts, &revision, &taskState); err != nil {
			t.Fatalf("task %s is not readable through the panel store: %v", taskID, err)
		}
		if revision <= 0 {
			t.Fatalf("task %s is not bound to a published revision", taskID)
		}
		f.note("batch %s item node=%s agent=%s state=%s task=%s attempts=%d/%d revision=%d task_state=%s",
			revokeBatch, nodeID, agentID, state, taskID, attempts, maxAttempts, revision, taskState)
		itemFacts = append(itemFacts, map[string]any{"node_id": nodeID, "agent_id": agentID, "state": state,
			"task_id": taskID, "attempts": attempts, "max_attempts": maxAttempts,
			"revision": revision, "task_state": taskState})
		if agentID == f.agents[1].agentID && state != "pending" && state != "dispatched" {
			t.Fatalf("offline target item state=%q, want pending/dispatched before the SLA sweep", state)
		}
	}
	if len(seenAgents) != 2 {
		t.Fatalf("batch items did not cover two distinct real Agents: %v", seenAgents)
	}

	// The reachable target must have its real Agent receipt before the sweep:
	// otherwise the SLA sweep would legitimately time out an item that was only
	// still pending because this assertion ran faster than one poll interval.
	f.waitCondition(60*time.Second, "the reachable target item records its real Agent receipt", func() bool {
		for _, it := range f.batchItems(revokeBatch) {
			if it["node_id"] == f.nodes[0] && it["state"] == "succeeded" {
				return true
			}
		}
		return false
	})

	// Panel sweeper semantics, using the same store function cmd/panel calls every
	// 15s, with the sweep clock advanced past the 60s dispatch SLA. The offline
	// item becomes an explicit retryable timeout; the reachable success is never
	// rewritten, and no other row is touched.
	if _, _, err := st.SweepTimeouts(ctx, time.Now().Add(70*time.Second)); err != nil {
		t.Fatalf("panel timeout sweep failed: %v", err)
	}
	states := map[string]string{}
	reasons := map[string]string{}
	for _, it := range f.batchItems(revokeBatch) {
		nodeID, _ := it["node_id"].(string)
		states[nodeID], _ = it["state"].(string)
		reasons[nodeID], _ = it["error"].(string)
	}
	if states[f.nodes[0]] != "succeeded" {
		t.Fatalf("reachable target item state=%q after the sweep, want succeeded", states[f.nodes[0]])
	}
	if states[f.nodes[1]] != "timeout" {
		t.Fatalf("offline target item state=%q after the SLA sweep, want timeout", states[f.nodes[1]])
	}
	if strings.TrimSpace(reasons[f.nodes[1]]) == "" {
		t.Fatal("the timed out offline item has no recorded reason")
	}
	f.note("per-object partial state after the panel SLA sweep (clock +70s): node1=succeeded node2=timeout reason=%q",
		reasons[f.nodes[1]])
	f.facts["revocation_batch"] = map[string]any{"batch_id": revokeBatch, "items_before_sweep": itemFacts,
		"items_after_sweep": f.batchItems(revokeBatch), "revoked_at": revokedAt.UTC().Format(time.RFC3339Nano)}

	// --- US-PR-011: the recovered hop is tightened, never widened ------------
	f.startAgent(1, "")
	f.waitApplied(120 * time.Second)
	f.note("offline Agent recovered and reached the desired (tightened) revision")
	oldRefusal2 := ""
	for i := 0; i < 20; i++ {
		if err := f.probeTCP(clients[1], payload, expectedHash); err != nil {
			oldRefusal2 = err.Error()
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if oldRefusal2 == "" {
		t.Fatal("the recovered node still accepted the withdrawn credential")
	}
	exportStatus, _, _ := f.exportRaw(aliceToken)
	if exportStatus != http.StatusForbidden {
		t.Fatalf("revoked subscription export status=%d, want 403", exportStatus)
	}
	f.note("recovered node refused the withdrawn credential (%q); subscription export=403", oldRefusal2)

	// --- US-PR-011: restoration re-issues credentials, old ones stay dead ----
	f.submitBatch("set_active", []any{f.member["id"]}, map[string]any{"active": true})
	f.waitApplied(120 * time.Second)
	if err := f.probeTCP(clients[1], payload, expectedHash); err == nil {
		t.Fatal("a permanently revoked credential regained access after restoration")
	}
	newCfg, newToken := f.export("wdd-alice")
	newClients := []*dualProxy{f.clientFor(0, newCfg), f.clientFor(1, newCfg)}
	if err := f.probeTCP(newClients[1], payload, expectedHash); err != nil {
		t.Fatalf("the newly exported credential does not serve on the recovered node: %v", err)
	}
	oldID := exportedVLESSIDs(aliceCfg)
	newID := exportedVLESSIDs(newCfg)
	if len(newID) == 0 || strings.Join(oldID, ",") == strings.Join(newID, ",") {
		t.Fatalf("restoration did not issue new node credentials (old=%v new=%v)", oldID, newID)
	}
	f.note("restoration: old credentials refused on both nodes; newly exported credential (rotated UUID set, old=%v new=%v) serves on the recovered node; subscription link token unchanged=%v (separate explicit reset action)",
		oldID, newID, newToken == aliceToken)

	// --- US-PR-012: a stale ACK never completes a newer task ----------------
	staleTaskID := ""
	staleRevision := int64(0)
	for _, it := range f.batchItems(revokeBatch) {
		if it["node_id"] != f.nodes[1] {
			continue
		}
		staleTaskID, _ = it["task_id"].(string)
		if err := st.DB.QueryRowContext(ctx, `SELECT coalesce(revision,0) FROM tasks WHERE id=$1`, staleTaskID).Scan(&staleRevision); err != nil {
			t.Fatal(err)
		}
	}
	if staleTaskID == "" || staleRevision == 0 {
		t.Fatal("no timed out task to retry")
	}
	f.stopAgent(1)
	retried := f.admin.request("POST", "/api/v1/admin/tasks/"+staleTaskID+"/retry", map[string]any{})
	if got, _ := retried["state"].(string); got != "pending" {
		t.Fatalf("retried task state=%q, want pending", got)
	}
	if attempts, _ := retried["attempts"].(float64); attempts < 2 {
		t.Fatalf("retried task attempts=%v, want >=2", retried["attempts"])
	}
	newRevision, _ := retried["revision"].(float64)
	if id := retried["id"].(string); id != staleTaskID {
		t.Fatalf("retry created a new task id %s instead of re-opening %s", id, staleTaskID)
	}
	if int64(newRevision) <= staleRevision {
		t.Fatalf("retry did not bind a newer revision: %v <= %d", retried["revision"], staleRevision)
	}
	// Replay the real Agent's old receipt (old revision, same Agent token) against
	// the reopened task: it must be recorded for the audit trail and change nothing.
	ctrl, err := control.New(f.panel.URL, agentconfig.AgentVersion)
	if err != nil {
		t.Fatal(err)
	}
	ctrl.SetToken(f.agents[1].token)
	ctrl.SetAgentID(f.agents[1].agentID)
	_, _, generation := f.revision(1)
	if err := ctrl.Applied(ctx, dualApplyReport(staleRevision, generation)); err != nil {
		t.Fatalf("the panel refused an old receipt for an already applied revision (must be recorded as stale, not an error): %v", err)
	}
	_, _, nowDesired := f.revision(1)
	futureErr := ctrl.Applied(ctx, dualApplyReport(nowDesired+1000, generation))
	if futureErr == nil {
		t.Fatal("a receipt for a never issued revision was accepted")
	}
	f.note("future revision receipt rejected with concrete error: %q", futureErr.Error())
	var taskState, itemState string
	var taskRev int64
	var attempts int
	if err := st.DB.QueryRowContext(ctx, `SELECT state, attempts, coalesce(revision,0) FROM tasks WHERE id=$1`, staleTaskID).
		Scan(&taskState, &attempts, &taskRev); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, `SELECT state FROM batch_items WHERE task_id=$1`, staleTaskID).Scan(&itemState); err != nil {
		t.Fatal(err)
	}
	if taskState != "pending" || itemState != "pending" || taskRev != int64(newRevision) || attempts != 2 {
		t.Fatalf("the old ACK changed the retried task: state=%s item=%s revision=%d (new %v) attempts=%d",
			taskState, itemState, taskRev, retried["revision"], attempts)
	}
	var receipts int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM agent_receipts WHERE agent_id=$1 AND revision=$2`,
		f.agents[1].agentID, staleRevision).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("the stale receipt was not recorded once for the audit trail (%d rows)", receipts)
	}
	f.note("stale ACK revision=%d recorded as a receipt only; the retried task stays pending revision=%d attempts=%d item=%s",
		staleRevision, taskRev, attempts, itemState)
	f.facts["retry"] = map[string]any{"task_id": staleTaskID, "stale_revision": staleRevision,
		"new_revision": int64(newRevision), "attempts": attempts,
		"future_receipt_error": futureErr.Error()}

	f.startAgent(1, "")
	f.waitCondition(240*time.Second, "the retried task converges after the real Agent reapplies", func() bool {
		var state string
		if err := st.DB.QueryRowContext(ctx, `SELECT state FROM tasks WHERE id=$1`, staleTaskID).Scan(&state); err != nil {
			return false
		}
		return state == "succeeded"
	})
	f.note("the retried task converged through the real Agent receipt; the stale ACK never completed it")

	// --- US-PR-014: a real technical rollback keeps the legal authorization ---
	// and never revives the withdrawn credential.
	f.patchNode(f.nodes[0], map[string]any{"protocol_settings": map[string]any{
		"decryption": "none", "fallbacks": "not-an-array"}})
	f.waitCondition(120*time.Second, "node 1 documents diverged again (desired>applied)", func() bool {
		d, a := f.revisionPair(0)
		return d > a
	})
	if err := f.probeTCP(newClients[0], payload, expectedHash); err == nil {
		f.note("node 1 kept serving the current authorization from last-known-good during the rejected document")
	} else {
		f.note("node 1 was briefly unavailable during the rejected-document rollback: %q", err)
	}
	f.waitCondition(150*time.Second, "node 1 serves again after the technical rollback", func() bool {
		return f.probeTCP(newClients[0], payload, expectedHash) == nil
	})
	if err := f.probeTCP(clients[0], payload, expectedHash); err == nil {
		t.Fatal("the documented rollback revived the permanently withdrawn credential")
	}
	rbD, rbA := f.revisionPair(0)
	f.patchNode(f.nodes[0], map[string]any{"protocol_settings": map[string]any{"decryption": "none"}})
	f.waitApplied(150 * time.Second)
	f.note("rollback: current authorization stayed legal (desired=%d applied=%d), withdrawn credential still refused", rbD, rbA)

	// --- US-PR-014: a real core crash keeps the legal authorization ----------
	if err := f.probeTCP(newClients[0], payload, expectedHash); err != nil {
		t.Fatalf("node 1 positive control before the crash failed: %v", err)
	}
	killed := f.killCore(0)
	if killed == 0 {
		t.Fatal("no owned node-1 core process was found to crash")
	}
	crashRefusal := ""
	if err := f.probeTCP(newClients[0], payload, expectedHash); err == nil {
		f.note("node 1 still answered right after the crash (listener handover)")
	} else {
		crashRefusal = err.Error()
	}
	f.waitCondition(180*time.Second, "node 1 serves again after the real core crash", func() bool {
		return f.probeTCP(newClients[0], payload, expectedHash) == nil
	})
	if err := f.probeTCP(clients[0], payload, expectedHash); err == nil {
		t.Fatal("the crashed/restarted core revived the permanently withdrawn credential")
	}
	crD, crA := f.revisionPair(0)
	if crD != crA || crD <= 0 {
		t.Fatalf("after crash recovery the Agent did not keep a converged legal revision: desired=%d applied=%d", crD, crA)
	}
	var activePerNode int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(DISTINCT node_id) FROM credentials WHERE member_id=$1 AND state='active'`,
		f.member["id"]).Scan(&activePerNode); err != nil {
		t.Fatal(err)
	}
	if activePerNode != 2 {
		t.Fatalf("expected the restored credential to be active on both nodes, found %d", activePerNode)
	}
	f.note("real core crash (pid %d killed via pidfd, refusal=%q) recovered to the same legal revision desired=%d applied=%d; withdrawn credential still refused; active credential nodes=%d",
		killed, crashRefusal, crD, crA, activePerNode)
	f.facts["crash"] = map[string]any{"killed_pid": killed, "refusal": crashRefusal,
		"desired_revision": crD, "applied_revision": crA, "active_credential_nodes": activePerNode}

	// --- US-PR-017 / US-PR-018: member scope + admin traceability -----------
	memberJar, _ := cookiejar.New(nil)
	memberAPI := &dataPlaneAPI{t: t, base: f.panel.URL, client: &http.Client{Jar: memberJar, Timeout: 10 * time.Second}}
	memberAPI.login(f.carol["username"], dualPassword)
	status, body := f.rawRequest(memberAPI, "GET", "/api/v1/admin/batches/"+revokeBatch)
	if status != http.StatusForbidden {
		t.Fatalf("member read of the admin batch detail returned %d (%v), want 403", status, body)
	}
	errBody, _ := body["error"].(map[string]any)
	f.note("member access to admin batch detail denied with 403 and code %v", errBody["code"])
	tasks, err := st.ListTasks(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) < 4 {
		t.Fatalf("expected at least the expiry/revocation/recovery/retry tasks, got %d", len(tasks))
	}
	for _, task := range tasks {
		if task.ID == "" || task.Revision == nil || task.Attempts < 0 || task.AgentID == "" {
			t.Fatalf("task row is not traceable: %+v", task)
		}
	}
	info, err := os.Stat(f.agents[0].stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("Agent state dir mode=%o, want 0700", info.Mode().Perm())
	}
	f.note("panel store exposes %d traceable task rows (revision/attempts/agent); member scope denied; Agent state dir is 0700", len(tasks))

	// Panel-persisted state as consumable evidence for the UI chain (task A).
	f.facts["agents"] = f.agentFacts()
	f.facts["batches"] = f.batchFacts(expiryBatch, revokeBatch)
	f.facts["tasks"] = f.taskFacts()
	f.facts["credentials"] = f.credentialFacts()
	f.facts["member_status"] = f.memberStatusFacts()
	f.facts["core_processes"] = f.coreProcessFacts()
	f.facts["conclusion"] = "dual-agent offline independence, bounded expiry, real UDP, per-object partial tracing, stale ACK isolation, rollback/crash without revival"
	f.note("DUAL-AGENT ACCEPTANCE COMPLETE")
	// Diagnostics are only printed on failure and are secret-scrubbed.
	if t.Failed() {
		scrub := regexp.MustCompile(`[A-Za-z0-9_+/=-]{24,}`)
		for i, a := range f.agents {
			t.Logf("sanitized Agent %d diagnostics:\n%s", i+1, scrub.ReplaceAllString(a.logs.String(), "[redacted]"))
		}
	}
}

// ---- test flow helpers -----------------------------------------------------

func (f *dualFixture) startAgent(i int, registrationCode string) {
	f.t.Helper()
	a := f.agents[i]
	client, err := control.New(f.panel.URL, agentconfig.AgentVersion)
	if err != nil {
		f.t.Fatal(err)
	}
	cfg := &agentconfig.Config{PanelURL: f.panel.URL, XrayPath: f.bin, StateDir: a.stateDir,
		AllowLoopbackHTTP: true, PollIntervalSeconds: 1, StatsIntervalSeconds: 1, HeartbeatIntervalSeconds: 1}
	if registrationCode != "" {
		cfg.RegistrationCode = registrationCode
	} else if a.token != "" {
		cfg.Token, cfg.AgentID = a.token, a.agentID
	}
	if err := cfg.Normalize(); err != nil {
		f.t.Fatal(err)
	}
	sup, err := supervisor.New(supervisor.Options{Config: cfg, Client: client, Logger: log.New(a.logs, "", 0)})
	if err != nil {
		f.t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	a.sup, a.cancel, a.done, a.running = sup, cancel, done, true
	go func() { done <- sup.Run(runCtx) }()
	f.t.Logf("Agent %d started (owned state dir, real core, poll/stats/heartbeat=1s)", i+1)
}

func (f *dualFixture) stopAgent(i int) {
	f.t.Helper()
	if err := f.stopAgentNow(i); err != nil {
		f.t.Fatal(err)
	}
}

func (f *dualFixture) stopAgentNow(i int) error {
	a := f.agents[i]
	if !a.running {
		return nil
	}
	a.cancel()
	select {
	case <-a.done:
	case <-time.After(45 * time.Second):
		a.running = false
		return fmt.Errorf("Agent %d did not stop", i+1)
	}
	a.running = false
	a.cancel = nil
	if err := a.sup.Close(); err != nil {
		return fmt.Errorf("Agent %d close failed: %w", i+1, err)
	}
	return nil
}

func (f *dualFixture) stopAgentBestEffort(i int) {
	if !f.agents[i].running {
		return
	}
	if err := f.stopAgentNow(i); err != nil {
		f.t.Error(err)
	}
}

// agentToken reads the persisted identity of a stopped Agent's owned state dir.
func (f *dualFixture) agentToken(i int) (string, string) {
	f.t.Helper()
	st, err := agentstate.Open(f.agents[i].stateDir)
	if err != nil {
		f.t.Fatalf("open owned Agent state: %v", err)
	}
	defer st.Close()
	id, err := st.Identity(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	if id.Token == "" || id.AgentID == "" {
		f.t.Fatal("owned Agent state has no token/agent id")
	}
	return id.Token, id.AgentID
}

func (f *dualFixture) revision(i int) (int64, int64, int64) {
	f.t.Helper()
	var desired, applied, generation int64
	if err := f.st.DB.QueryRowContext(f.ctx, `SELECT desired_revision, applied_revision, control_generation
		FROM agents WHERE server_id=$1 AND archived=false`, f.agents[i].serverID).Scan(&desired, &applied, &generation); err != nil {
		f.t.Fatalf("agent revision query failed: %v", err)
	}
	return desired, applied, generation
}

func (f *dualFixture) revisionPair(i int) (int64, int64) {
	d, a, _ := f.revision(i)
	return d, a
}

func (f *dualFixture) waitApplied(budget time.Duration) {
	f.t.Helper()
	f.waitCondition(budget, "both real Agents reached the desired revision", func() bool {
		for i := range f.agents {
			if !f.agents[i].running {
				continue
			}
			d, a := f.revisionPair(i)
			if d == 0 || a != d {
				return false
			}
		}
		return true
	})
}

func (f *dualFixture) waitCondition(budget time.Duration, what string, check func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	f.t.Fatalf("timed out waiting for %s", what)
}

func (f *dualFixture) export(username string) (map[string]any, string) {
	f.t.Helper()
	jar, _ := cookiejar.New(nil)
	user := &dataPlaneAPI{t: f.t, base: f.panel.URL, client: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
	user.login(username, dualPassword)
	sub := user.request("GET", "/api/v1/me/subscription", nil)
	token, _ := sub["token"].(string)
	if token == "" {
		f.t.Fatal("member subscription token missing")
	}
	status, cfg, _ := f.exportRaw(token)
	if status != http.StatusOK {
		f.t.Fatalf("subscription export status=%d", status)
	}
	return cfg, token
}

func (f *dualFixture) exportRaw(token string) (int, map[string]any, error) {
	res, err := http.Get(f.panel.URL + "/sub/" + token + "?format=xray")
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, nil, nil
	}
	var cfg map[string]any
	if err := json.NewDecoder(res.Body).Decode(&cfg); err != nil {
		return res.StatusCode, nil, err
	}
	return res.StatusCode, cfg, nil
}

// clientFor turns the real exported subscription into an official Xray client
// core whose only routed outbound is the requested node. The export itself is
// used (only inbound/routing are replaced), so the credential is the panel's.
func (f *dualFixture) clientFor(node int, exported map[string]any) *dualProxy {
	f.t.Helper()
	raw, err := json.Marshal(exported)
	if err != nil {
		f.t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		f.t.Fatal(err)
	}
	want := f.agents[node].nodePort
	var outbound map[string]any
	for _, o := range cfg["outbounds"].([]any) {
		ob, _ := o.(map[string]any)
		settings, _ := ob["settings"].(map[string]any)
		vnext, _ := settings["vnext"].([]any)
		if len(vnext) == 0 {
			continue
		}
		server, _ := vnext[0].(map[string]any)
		if port, ok := server["port"].(float64); ok && int(port) == want {
			outbound = ob
			break
		}
	}
	if outbound == nil || outbound["protocol"] != "vless" {
		f.t.Fatalf("the export has no VLESS outbound for node %d (port %d)", node+1, want)
	}
	tag := outbound["tag"].(string)
	socksPort := unusedPort(f.t)
	cfg["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks",
		"settings": map[string]any{"auth": "noauth", "udp": true}}}
	cfg["routing"] = map[string]any{"domainStrategy": "AsIs", "rules": []any{
		map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": tag}}}
	path := writeConfig(f.t, f.t.TempDir(), fmt.Sprintf("wdd-client-%d.json", node+1), cfg)
	startCore(f.t, f.bin, path, socksPort)
	tr := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", socksPort)})}
	f.t.Cleanup(tr.CloseIdleConnections)
	f.t.Logf("official Xray client core for node %d: socks=127.0.0.1:%d -> exported outbound tag=%q", node+1, socksPort, tag)
	return &dualProxy{client: &http.Client{Transport: tr, Timeout: 5 * time.Second}, transport: tr,
		socks: fmt.Sprintf("127.0.0.1:%d", socksPort), tag: tag}
}

func (f *dualFixture) probeTCP(p *dualProxy, payload string, want [32]byte) error {
	p.transport.CloseIdleConnections()
	res, err := p.client.Get(f.target.http.URL)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("proxy HTTP status %d", res.StatusCode)
	}
	if len(body) != len(payload) || sha256.Sum256(body) != want {
		return fmt.Errorf("proxied body mismatch (%d bytes)", len(body))
	}
	return nil
}

// probeUDP performs a real SOCKS5 UDP ASSOCIATE through the official client core
// and the real server core; UDP is never tunneled inside a TCP frame here.
func (f *dualFixture) probeUDP(p *dualProxy, payload []byte) ([]byte, error) {
	ctrl, err := net.DialTimeout("tcp", p.socks, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer ctrl.Close()
	_ = ctrl.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := ctrl.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(ctrl, greeting); err != nil || greeting[0] != 5 || greeting[1] != 0 {
		return nil, fmt.Errorf("socks5 greeting failed: %v %v", greeting, err)
	}
	if _, err := ctrl.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return nil, err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(ctrl, head); err != nil || head[1] != 0 {
		return nil, fmt.Errorf("socks5 udp associate refused: %v %v", head, err)
	}
	addrLen := 4
	switch head[3] {
	case 1:
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(ctrl, l); err != nil {
			return nil, err
		}
		addrLen = int(l[0])
	case 4:
		addrLen = 16
	default:
		return nil, fmt.Errorf("unexpected socks5 relay atyp %d", head[3])
	}
	rest := make([]byte, addrLen+2)
	if _, err := io.ReadFull(ctrl, rest); err != nil {
		return nil, err
	}
	port := int(binary.BigEndian.Uint16(rest[addrLen:]))
	relay := net.JoinHostPort(net.IP(rest[:addrLen]).String(), strconv.Itoa(port))
	if port == 0 || strings.HasPrefix(relay, "0.0.0.0:") {
		relay = ctrl.LocalAddr().String()
	}
	target := f.target.udp.LocalAddr().(*net.UDPAddr)
	udp, err := net.Dial("udp", relay)
	if err != nil {
		return nil, err
	}
	defer udp.Close()
	_ = udp.SetDeadline(time.Now().Add(5 * time.Second))
	packet := []byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(packet[8:], uint16(target.Port))
	packet = append(packet, payload...)
	if _, err := udp.Write(packet); err != nil {
		return nil, err
	}
	reply := make([]byte, 65535)
	n, err := udp.Read(reply)
	if err != nil {
		return nil, err
	}
	if n < 10 || reply[0] != 0 || reply[1] != 0 || reply[2] != 0 {
		return nil, fmt.Errorf("malformed socks5 udp reply (%d bytes)", n)
	}
	off := 4 + 4 + 2
	switch reply[3] {
	case 3:
		if n < 5 {
			return nil, fmt.Errorf("truncated socks5 udp reply")
		}
		off = 4 + 1 + int(reply[4]) + 2
	case 4:
		off = 4 + 16 + 2
	}
	if n < off {
		return nil, fmt.Errorf("truncated socks5 udp reply payload")
	}
	return reply[off:n], nil
}

func (f *dualFixture) patchNode(nodeID string, values map[string]any) {
	f.t.Helper()
	node := f.admin.request("GET", "/api/v1/admin/nodes/"+nodeID, nil)
	if node["version"] == nil {
		f.t.Fatal("node detail missing version")
	}
	values["version"] = node["version"]
	f.admin.request("PATCH", "/api/v1/admin/nodes/"+nodeID, values)
}

func (f *dualFixture) submitBatch(operation string, memberIDs []any, values map[string]any) string {
	f.t.Helper()
	preview := f.admin.request("POST", "/api/v1/admin/batches/preview", map[string]any{
		"operation": operation, "member_ids": memberIDs, "values": values})
	submitted := f.admin.request("POST", "/api/v1/admin/batches", map[string]any{"preview_id": preview["id"], "confirm": true})
	id, _ := submitted["id"].(string)
	if id == "" {
		f.t.Fatal("batch submission without id")
	}
	f.note("real batch %s submitted operation=%s targets=%d", id, operation, len(memberIDs))
	return id
}

func (f *dualFixture) batchItems(batchID string) []map[string]any {
	f.t.Helper()
	data := f.admin.request("GET", "/api/v1/admin/batches/"+batchID, nil)
	raw, _ := data["results"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (f *dualFixture) rawRequest(a *dataPlaneAPI, method, path string) (int, map[string]any) {
	f.t.Helper()
	req, err := http.NewRequest(method, a.base+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("X-CSRF-Token", a.csrf)
	res, err := a.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// killCore kills the owned node core through pidfd: the exact process is pinned,
// so a recycled PID can never be hit. Only configs below this test's private
// state directory are eligible.
func (f *dualFixture) killCore(i int) int {
	f.t.Helper()
	root := f.agents[i].stateDir
	for _, p := range mustGlob("/proc/[0-9]*") {
		pid, err := strconv.Atoi(filepath.Base(p))
		if err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join(p, "exe"))
		if err != nil || exe != f.bin {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p, "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		config := ""
		for j, v := range argv {
			if v == "-c" && j+1 < len(argv) {
				config = argv[j+1]
			}
		}
		if config == "" || !strings.HasPrefix(config, root) {
			continue
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			f.t.Fatalf("pidfd open for owned core %d failed: %v", pid, err)
		}
		defer unix.Close(fd)
		if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil {
			f.t.Fatalf("pidfd kill of owned core %d failed: %v", pid, err)
		}
		f.note("owned core crash injected: node %d core pid=%d config=%s killed via pidfd", i+1, pid, config)
		return pid
	}
	return 0
}

func mustGlob(pattern string) []string {
	out, _ := filepath.Glob(pattern)
	return out
}

// exportedVLESSIDs lists the VLESS user ids of the real export, used to prove a
// credential rotation without ever logging a full secret.
func exportedVLESSIDs(cfg map[string]any) []string {
	out := []string{}
	outs, _ := cfg["outbounds"].([]any)
	for _, o := range outs {
		ob, _ := o.(map[string]any)
		settings, _ := ob["settings"].(map[string]any)
		vnext, _ := settings["vnext"].([]any)
		for _, v := range vnext {
			server, _ := v.(map[string]any)
			users, _ := server["users"].([]any)
			for _, u := range users {
				user, _ := u.(map[string]any)
				if id, ok := user["id"].(string); ok && id != "" {
					out = append(out, id[:8]+"…")
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// dualApplyReport builds the receipt payload a real Agent sends for one applied
// revision; used only to replay a stale/future receipt.
func dualApplyReport(revision, generation int64) contracts.ApplyReport {
	return contracts.ApplyReport{Revision: revision, ControlGeneration: strconv.FormatInt(generation, 10),
		State: "applied", AppliedAt: time.Now().UTC()}
}

// ---- panel-persisted evidence (consumable by the UI/story owner) -----------

func (f *dualFixture) agentFacts() []map[string]any {
	rows, err := f.st.DB.QueryContext(f.ctx, `SELECT id, server_id, desired_revision, applied_revision,
		coalesce(last_heartbeat_at, now()), archived FROM agents ORDER BY created_at`)
	if err != nil {
		f.t.Fatalf("agent evidence query failed: %v", err)
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, serverID string
		var desired, applied int64
		var heartbeat time.Time
		var archived bool
		if err := rows.Scan(&id, &serverID, &desired, &applied, &heartbeat, &archived); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, map[string]any{"agent_id": id, "server_id": serverID,
			"desired_revision": desired, "applied_revision": applied,
			"last_heartbeat_at": heartbeat.UTC().Format(time.RFC3339Nano), "archived": archived})
	}
	return out
}

func (f *dualFixture) batchFacts(ids ...string) []map[string]any {
	out := []map[string]any{}
	for _, id := range ids {
		data := f.admin.request("GET", "/api/v1/admin/batches/"+id, nil)
		out = append(out, map[string]any{"batch_id": id, "state": data["state"],
			"operation": data["operation"], "summary": data["summary"], "results": data["results"]})
	}
	return out
}

func (f *dualFixture) taskFacts() []map[string]any {
	tasks, err := f.st.ListTasks(f.ctx, "", 100)
	if err != nil {
		f.t.Fatalf("task evidence query failed: %v", err)
	}
	out := []map[string]any{}
	for _, task := range tasks {
		revision := int64(0)
		if task.Revision != nil {
			revision = *task.Revision
		}
		out = append(out, map[string]any{"task_id": task.ID, "kind": task.Kind, "agent_id": task.AgentID,
			"node_id": task.NodeID, "batch_id": task.BatchID, "state": task.State,
			"attempts": task.Attempts, "revision": revision, "last_error": task.LastError})
	}
	return out
}

func (f *dualFixture) credentialFacts() []map[string]any {
	rows, err := f.st.DB.QueryContext(f.ctx, `SELECT member_id, node_id, state,
		coalesce(rotated_reason,''), coalesce(revoked_at, to_timestamp(0)) FROM credentials ORDER BY created_at`)
	if err != nil {
		f.t.Fatalf("credential evidence query failed: %v", err)
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var memberID, nodeID, state, reason string
		var revokedAt time.Time
		if err := rows.Scan(&memberID, &nodeID, &state, &reason, &revokedAt); err != nil {
			f.t.Fatal(err)
		}
		item := map[string]any{"member_id": memberID, "node_id": nodeID, "state": state, "reason": reason}
		if !revokedAt.IsZero() {
			item["revoked_at"] = revokedAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, item)
	}
	return out
}

func (f *dualFixture) memberStatusFacts() []map[string]any {
	out := []map[string]any{}
	rows, err := f.st.DB.QueryContext(f.ctx, `SELECT id, username, active, proxy_enabled,
		coalesce(expires_at, to_timestamp(0)) FROM members WHERE role='member' ORDER BY username`)
	if err != nil {
		f.t.Fatalf("member evidence query failed: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, username string
		var active, proxyEnabled bool
		var expires time.Time
		if err := rows.Scan(&id, &username, &active, &proxyEnabled, &expires); err != nil {
			f.t.Fatal(err)
		}
		item := map[string]any{"member_id": id, "username": username, "active": active, "proxy_enabled": proxyEnabled}
		if !expires.IsZero() {
			item["expires_at"] = expires.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, item)
	}
	return out
}

func (f *dualFixture) coreProcessFacts() []map[string]any {
	out := []map[string]any{}
	for _, p := range mustGlob("/proc/[0-9]*") {
		pid, err := strconv.Atoi(filepath.Base(p))
		if err != nil {
			continue
		}
		exe, err := os.Readlink(filepath.Join(p, "exe"))
		if err != nil || exe != f.bin {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p, "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		config := ""
		for j, v := range argv {
			if v == "-c" && j+1 < len(argv) {
				config = argv[j+1]
			}
		}
		owner := ""
		for i, a := range f.agents {
			if strings.HasPrefix(config, a.stateDir) {
				owner = fmt.Sprintf("agent-%d/node-%d", i+1, i+1)
			}
		}
		if owner == "" {
			continue // never report a core that this fixture does not own
		}
		out = append(out, map[string]any{"pid": pid, "owner": owner, "config": config})
	}
	return out
}

// writeEvidence persists the panel-API/persisted state and traffic/process
// evidence for the UI chain owner. It never contains a password or token.
func (f *dualFixture) writeEvidence() {
	dir := os.Getenv("PP_DEMO_DUAL_EVIDENCE_DIR")
	if dir == "" {
		return
	}
	f.facts["trace"] = f.trace
	f.facts["test_failed"] = f.t.Failed()
	if f.target != nil {
		f.facts["core_processes"] = f.coreProcessFacts()
	}
	raw, err := json.MarshalIndent(f.facts, "", "  ")
	if err != nil {
		f.t.Error("marshal evidence:", err)
		return
	}
	text := string(raw)
	for _, secret := range []string{dualPassword, f.member["token"], f.carol["token"], f.agents[0].token, f.agents[1].token} {
		if secret == "" {
			continue
		}
		if strings.Contains(text, secret) {
			f.t.Error("evidence would contain a secret; refusing to write it")
			return
		}
	}
	path := filepath.Join(dir, "dual-state.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		f.t.Error("write evidence:", err)
		return
	}
	_ = os.Chmod(path, 0o600)
	f.t.Logf("evidence written to %s (%d bytes, mode 0600)", path, len(raw))
}
