package e2e

// Owned web-demo / US-PR-016 real cross-version Agent + SQLite state closure.
//
// This case never touches the old 8443/8445 stacks, the shared Docker fixture or
// the shared data-plane runner. Everything is a new owner temporary directory,
// loopback port and owned PostgreSQL schema (PANEL_TEST_DATABASE_URL, database
// prefix dp_owned_ enforced below).
//
// It is deliberately NOT a restatement of TestDockerAgentVersionUpgrade /
// TestDockerCoreVersionSwitch: those drive deploy/docker-lifecycle.sh against
// the shared acceptance image/volume. Here the two Agent binaries are real
// builds of two different git commits (old = PP_DEMO_UPGRADE_OLD_AGENT, new =
// PP_DEMO_UPGRADE_NEW_AGENT), the SQLite state directory is created by the old
// binary itself, and the current core binaries are the same official Xray
// releases the Agent whitelist names.
//
// Required environment:
//   PANEL_TEST_DATABASE_URL      owned PostgreSQL (127.0.0.1, database dp_owned_*)
//   PP_DEMO_UPGRADE_OLD_AGENT    Agent binary built from the old git commit
//   PP_DEMO_UPGRADE_NEW_AGENT    Agent binary built from the current candidate
//   PP_DEMO_UPGRADE_OLD_CORE     official Xray 26.3.23 binary
//   PP_DEMO_UPGRADE_NEW_CORE     official Xray 26.3.27 binary
// Optional:
//   PP_DEMO_UPGRADE_STATE_DIR    evidence output directory (default: t.TempDir)
//
// Every switch is proven by (a) the exact process we started, (b) its sha256,
// (c) a heartbeat strictly newer than the pre-action snapshot, (d) the core
// version the Agent reports, and (e) the queue/ledger/authorization rows. The
// version *string* alone is never used: both known commits label themselves
// 0.1.1, so a string comparison would not be an upgrade proof.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	agentconfig "proxypanel/internal/agent/config"
	"proxypanel/internal/agent/state"
	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/api"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
	"proxypanel/internal/panel/usagecalc"

	_ "modernc.org/sqlite"
)

type demoUpgradeAgentRow struct {
	ID          string
	Version     string
	XrayVersion string
	Applied     int64
	Desired     int64
	Generation  int64
	Heartbeat   time.Time
}

type demoUpgradeEnv struct {
	oldAgent, newAgent, oldCore, newCore   string
	oldSHA, newSHA, oldCoreSHA, newCoreSHA string
	oldCoreVersion, newCoreVersion         string
	evidenceDir                            string
}

// demoUpgradeBlockProxy relays everything to the real panel except Agent usage
// uploads, which return 503 while blocked. It is a transport fault only: the
// Agent cannot acknowledge and therefore must keep its outbox.
type demoUpgradeBlockProxy struct {
	target  *url.URL
	blocked atomic.Bool
	server  *httptest.Server

	mu     sync.Mutex
	status map[string]int // "POST /api/v1/agent/heartbeat 409" -> count
	seen   map[string]string
}

func newDemoUpgradeBlockProxy(t *testing.T, target string) *demoUpgradeBlockProxy {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	p := &demoUpgradeBlockProxy{target: u, status: map[string]int{}, seen: map[string]string{}}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.blocked.Load() && r.URL.Path == "/api/v1/agent/usage" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"demo_upgrade_upload_blocked"}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		req, err := http.NewRequestWithContext(r.Context(), r.Method, p.target.Scheme+"://"+p.target.Host+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		req.Header = r.Header.Clone()
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		if strings.HasPrefix(r.URL.Path, "/api/v1/agent/") {
			key := fmt.Sprintf("%s %s %d", r.Method, r.URL.Path, res.StatusCode)
			p.mu.Lock()
			p.status[key]++
			if res.StatusCode >= 300 && p.seen[key] == "" {
				preview, _ := io.ReadAll(io.LimitReader(res.Body, 400))
				p.seen[key] = strings.TrimSpace(string(preview))
				res.Body = io.NopCloser(io.MultiReader(bytes.NewReader(preview), res.Body))
			}
			p.mu.Unlock()
		}
		for k, v := range res.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(p.server.Close)
	return p
}

type demoUpgradeRun struct {
	t        *testing.T
	ctx      context.Context
	env      demoUpgradeEnv
	st       *store.Store
	api      *dataPlaneAPI
	base     string
	schema   string
	work     string
	stateDir string

	proxy    *demoUpgradeBlockProxy
	agentID  string
	serverID string
	nodeID   string
	memberID string
	subID    string
	token    string
	password string

	agents []*demoUpgradeAgent
}

type demoUpgradeAgent struct {
	label string
	bin   string
	core  string
	sha   string
	cmd   *exec.Cmd
	done  chan error
	log   string
}

func demoUpgradeSum(raw []byte) []byte {
	s := sha256.Sum256(raw)
	return s[:]
}

func demoUpgradeFileSHA(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return hex.EncodeToString(demoUpgradeSum(raw))
}

func demoUpgradeEnvRead(t *testing.T) demoUpgradeEnv {
	t.Helper()
	get := func(key string) string {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			t.Fatalf("not_run: %s is required (owned old/new Agent binaries and official cores)", key)
		}
		if _, err := os.Stat(v); err != nil {
			t.Fatalf("%s=%s is not readable: %v", key, v, err)
		}
		return v
	}
	e := demoUpgradeEnv{
		oldAgent: get("PP_DEMO_UPGRADE_OLD_AGENT"), newAgent: get("PP_DEMO_UPGRADE_NEW_AGENT"),
		oldCore: get("PP_DEMO_UPGRADE_OLD_CORE"), newCore: get("PP_DEMO_UPGRADE_NEW_CORE"),
		evidenceDir: strings.TrimSpace(os.Getenv("PP_DEMO_UPGRADE_STATE_DIR")),
	}
	e.oldSHA, e.newSHA = demoUpgradeFileSHA(t, e.oldAgent), demoUpgradeFileSHA(t, e.newAgent)
	e.oldCoreSHA, e.newCoreSHA = demoUpgradeFileSHA(t, e.oldCore), demoUpgradeFileSHA(t, e.newCore)
	if e.oldSHA == e.newSHA {
		t.Fatalf("the old and the new Agent binary are identical (%s): a rebuild alone is not a cross-version upgrade", e.oldSHA)
	}
	if e.oldCoreSHA == e.newCoreSHA {
		t.Fatalf("the old and the new core binary are identical (%s): a core switch was not configured", e.oldCoreSHA)
	}
	// Both commits label themselves 0.1.1; record it instead of pretending the
	// version string proves the switch.
	for _, pair := range []struct{ name, bin string }{{"old", e.oldAgent}, {"new", e.newAgent}} {
		out, err := exec.Command(pair.bin, "-version").CombinedOutput()
		if err != nil {
			t.Fatalf("%s Agent -version failed: %v (%s)", pair.name, err, out)
		}
		t.Logf("%s Agent %s sha256=%s version=%q", pair.name, pair.bin, demoUpgradeFileSHA(t, pair.bin), strings.TrimSpace(string(out)))
	}
	for _, pair := range []struct{ name, bin string }{{"old", e.oldCore}, {"new", e.newCore}} {
		out, err := exec.Command(pair.bin, "version").CombinedOutput()
		if err != nil {
			t.Fatalf("%s core version failed: %v (%s)", pair.name, err, out)
		}
		line, _, _ := strings.Cut(string(out), "\n")
		version := demoUpgradeCoreVersion(t, strings.TrimSpace(line))
		if pair.name == "old" {
			e.oldCoreVersion = version
		} else {
			e.newCoreVersion = version
		}
		t.Logf("%s core %s sha256=%s version=%q", pair.name, pair.bin, demoUpgradeFileSHA(t, pair.bin), strings.TrimSpace(line))
	}
	if e.oldCoreVersion == e.newCoreVersion {
		t.Fatalf("both core binaries report %s: the core switch would not move the verified version list", e.oldCoreVersion)
	}
	return e
}

// demoUpgradeCoreVersion extracts "26.3.23" from the core's version banner.
func demoUpgradeCoreVersion(t *testing.T, banner string) string {
	t.Helper()
	m := regexp.MustCompile(`Xray (\d+\.\d+\.\d+)`).FindStringSubmatch(banner)
	if m == nil {
		t.Fatalf("core version banner is unreadable: %q", banner)
	}
	return m[1]
}

func TestWebDemoUpgradeClosure(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PANEL_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Fatal("not_run: PANEL_TEST_DATABASE_URL is required (owned PostgreSQL, database prefix dp_owned_)")
	}
	dbc, err := pgx.ParseConfig(dsn)
	if err != nil || dbc.Host != "127.0.0.1" || !strings.HasPrefix(dbc.Database, "dp_owned_") {
		t.Fatal("refusing a non-owned PostgreSQL: host must be 127.0.0.1 and database must start with dp_owned_")
	}
	env := demoUpgradeEnvRead(t)
	ctx := context.Background()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect owned PostgreSQL: %v", err)
	}
	defer db.Close(ctx)
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "ppdemo_upg_" + hex.EncodeToString(random)
	if _, err := db.Exec(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := db.Exec(clean, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Errorf("owned schema cleanup failed: %v", err)
		} else {
			t.Log("cleanup: owned PostgreSQL schema dropped")
		}
	}()
	key, err := secrets.ParseKey(strings.Repeat("e2e-only-random-schema-", 3))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{DatabaseURL: dsn, MasterKey: key, MigrationsDir: "../../migrations", SearchPath: schema})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	password := "Upgrade-only-" + hex.EncodeToString(random)
	hash, err := secrets.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "admin", PasswordHash: hash, Role: "admin", Active: true, ProxyEnabled: true}); err != nil {
		t.Fatal(err)
	}
	limiter, err := limit.New("") // process-local limiter: no shared Redis writes
	if err != nil {
		t.Fatal(err)
	}
	defer limiter.Close()
	panel := &api.Server{Store: st, RateLimiter: limiter, Cfg: api.Config{Dev: true, Listen: "127.0.0.1:8080", SessionTTL: time.Hour}}
	server := httptest.NewServer(panel.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	admin := &dataPlaneAPI{t: t, base: server.URL, client: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	admin.login("admin", password)

	work := t.TempDir()
	stateDir := env.evidenceDir
	if stateDir == "" {
		stateDir = filepath.Join(work, "state")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &demoUpgradeRun{t: t, ctx: ctx, env: env, st: st, api: admin, base: server.URL, schema: schema, work: work, stateDir: stateDir, password: password}
	t.Logf("owned schema=%s state_dir=%s (state contents are not committed)", schema, stateDir)
	r.proxy = newDemoUpgradeBlockProxy(t, server.URL)

	srv := admin.request("POST", "/api/v1/admin/servers", map[string]any{"name": "upgrade-e2e", "address": "127.0.0.1", "region": "test"})
	r.serverID = srv["id"].(string)
	inbound := unusedPort(t)
	node := admin.request("POST", "/api/v1/admin/nodes", map[string]any{
		"name": "upgrade-vless", "server_id": r.serverID, "enabled": true, "host": "127.0.0.1", "listen": "127.0.0.1", "port": inbound, "protocol": "vless",
		"stream_settings": map[string]any{"network": "tcp", "security": "none"}, "protocol_settings": map[string]any{"decryption": "none"}, "client_settings": map[string]any{},
	})
	r.nodeID = node["id"].(string)
	group := admin.request("POST", "/api/v1/admin/node-groups", map[string]any{"name": "upgrade-group", "node_ids": []any{r.nodeID}})
	sub := admin.request("POST", "/api/v1/admin/subscriptions", map[string]any{"name": "upgrade-sub", "node_group_ids": []any{group["id"]}, "quota_bytes": 1 << 30, "timezone": "Asia/Shanghai", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true})
	r.subID = sub["id"].(string)
	member := admin.request("POST", "/api/v1/admin/members", map[string]any{"username": "upgrade-member", "password": password, "role": "member", "subscription_id": r.subID})
	r.memberID = member["id"].(string)
	registration := admin.request("POST", "/api/v1/admin/servers/"+r.serverID+"/registration-codes", nil)

	// ---- 1. old Agent binary creates the SQLite state itself ---------------
	old := r.startAgent("old", env.oldAgent, env.oldCore, registration["code"].(string))
	before := r.waitAgent("old Agent registered and applied", old, env.oldCoreVersion, time.Time{})
	r.agentID = before.ID
	t.Logf("old Agent state created by the real binary: agent=%s version=%s core=%s applied=%d generation=%d", before.ID, before.Version, before.XrayVersion, before.Applied, before.Generation)

	// ---- 2. real traffic, then a queue that cannot drain -------------------
	r.proxy.blocked.Store(true)
	memberAPI := r.memberClient()
	subscription := memberAPI.request("GET", "/api/v1/me/subscription", nil)
	token, _ := subscription["token"].(string)
	if token == "" {
		t.Fatal("member subscription token missing")
	}
	r.token = token
	payloadBytes, payloadSHA := r.transfer(t, "upgrade-traffic")
	t.Logf("real proxied transfer through the old Agent/core: %d bytes sha256=%x", payloadBytes, payloadSHA)
	waitFor(t, 60*time.Second, "the old Agent sampled the transfer into its outbox", func() bool {
		return r.stateCount(`SELECT count(*) FROM batches WHERE state='pending'`) > 0
	})

	// Revoke the member while the outbox is still held: the narrowed
	// authorization is what the upgrade must carry forward, never widen.
	preview := admin.request("POST", "/api/v1/admin/batches/preview", map[string]any{"operation": "set_active", "member_ids": []any{r.memberID}, "values": map[string]any{"active": false}})
	admin.request("POST", "/api/v1/admin/batches", map[string]any{"preview_id": preview["id"], "confirm": true})
	waitFor(t, 90*time.Second, "the old Agent applied the revocation locally", func() bool {
		row := r.agentRow()
		return row.Applied == row.Desired && row.Applied > 0 && r.statusAppliedUsers() == 0
	})
	// The revocation's final sample is appended only when the core still reports
	// counters, so it is recorded, not required: the held set is whatever the
	// stopped state really contains.
	waitFor(t, 30*time.Second, "the held outbox still carries the sampled traffic after the revocation", func() bool {
		return r.stateCount(`SELECT count(*) FROM batches WHERE state='pending'`) >= 1
	})

	// ---- 3. controlled stop + consistent queue/state evidence -------------
	stopped := r.stopAgent(old, "old Agent")
	if stopped != nil {
		t.Fatalf("the old Agent did not stop cleanly (a nonzero exit would mean an unconfirmed core stop): %v", stopped)
	}
	t.Logf("old Agent stopped cleanly, state files: %s", r.stateFileSummary())
	evidence := r.readEvidence("old Agent stopped state")
	queue := evidence
	if queue.PendingReportCount != 0 {
		t.Logf("old state also carried %d report(s) in its outbox", queue.PendingReportCount)
	}
	if len(queue.PendingBatches) == 0 {
		t.Fatal("the stopped old state has an empty outbox: this run would not prove queue preservation")
	}
	beforeIDs := make([]string, 0, len(queue.PendingBatches))
	queuedMemberBytes := int64(0)
	for _, b := range queue.PendingBatches {
		beforeIDs = append(beforeIDs, b.BatchID)
		for _, s := range b.Samples {
			if s.MemberID == r.memberID {
				queuedMemberBytes += s.UploadBytes + s.DownloadBytes
			}
		}
	}
	sort.Strings(beforeIDs)
	if queuedMemberBytes <= 0 {
		t.Fatalf("the retained outbox carries no member byte (%d): nothing to preserve", queuedMemberBytes)
	}
	baselineBefore := r.baselineSum()
	t.Logf("retained outbox: %d batch(es) %v member_bytes=%d baseline_rows=%d applied=%d generation=%s evidence_schema=%s database_sha256=%s",
		len(beforeIDs), beforeIDs, queuedMemberBytes, baselineBefore, evidence.AppliedRevision, evidence.ControlGeneration, evidence.StateSchemaVersion, evidence.DatabaseSHA256)
	for _, b := range queue.PendingBatches {
		t.Logf("pending batch %s payload_sha256=%s samples=%d", b.BatchID, usagecalc.PayloadSHA256(b), len(b.Samples))
	}

	// A state copy with an unmerged WAL must be refused, not "repaired" by us.
	unmerged := r.copyState("unmerged-wal")
	walPath := filepath.Join(unmerged, "agent.db-wal")
	if err := os.WriteFile(walPath, []byte("unmerged-wal-sample"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadUpgradeEvidence(ctx, unmerged); err == nil {
		t.Fatal("a state copy with an unmerged -wal file was accepted as upgrade evidence")
	} else {
		t.Logf("preflight refused the unmerged-WAL sample as expected: %v", err)
	}
	unsupported := r.copyState("unsupported-layout")
	if err := demoUpgradeExecSQL(filepath.Join(unsupported, "agent.db"), `CREATE TABLE demo_upgrade_unknown(id TEXT)`); err != nil {
		t.Fatalf("prepare unsupported layout copy: %v", err)
	}
	unsupportedSHA := demoUpgradeFileSHA(t, filepath.Join(unsupported, "agent.db"))
	if _, err := state.ReadUpgradeEvidence(ctx, unsupported); err == nil {
		t.Fatal("a state copy with an unknown table was accepted as upgrade evidence")
	} else {
		t.Logf("preflight refused the unknown-layout sample as expected: %v", err)
	}
	if demoUpgradeFileSHA(t, filepath.Join(unsupported, "agent.db")) != unsupportedSHA {
		t.Fatal("the refused preflight mutated the state copy")
	}
	t.Log("failed-upgrade path: the production preflight refuses unmerged WAL / unknown layout and leaves the input byte-identical; the old Agent's own state was never switched")

	// ---- 4. new Agent binary opens/migrates the same state, old core -------
	r.proxy.blocked.Store(false)
	fresh := r.startAgent("new", env.newAgent, env.oldCore, "")
	afterOpen := r.waitAgent("new Agent opened and migrated the old state", fresh, env.oldCoreVersion, before.Heartbeat)
	if afterOpen.ID != before.ID {
		t.Fatalf("the Agent identity changed across the upgrade: %s -> %s", before.ID, afterOpen.ID)
	}
	if afterOpen.Generation != before.Generation {
		t.Fatalf("the control generation changed across the upgrade: %d -> %d", before.Generation, afterOpen.Generation)
	}
	if afterOpen.Applied < before.Applied {
		t.Fatalf("the applied revision regressed across the upgrade: %d -> %d", before.Applied, afterOpen.Applied)
	}
	t.Logf("new Agent opened the old SQLite state: agent=%s core=%s applied=%d (before %d) generation=%d", afterOpen.ID, afterOpen.XrayVersion, afterOpen.Applied, before.Applied, afterOpen.Generation)

	waitFor(t, 120*time.Second, "the new Agent drained the retained outbox", func() bool {
		return r.stateCount(`SELECT count(*) FROM batches`) == 0
	})
	// Every retained batch id must be in PostgreSQL exactly once with the same
	// payload digest; nothing outside the captured set may have arrived.
	for _, b := range queue.PendingBatches {
		id, want := b.BatchID, usagecalc.PayloadSHA256(b)
		rows, got := r.pgBatch(id)
		if rows != 1 {
			t.Fatalf("batch %s is stored %d time(s) after the upgrade, expected exactly 1", id, rows)
		}
		if got != want {
			t.Fatalf("batch %s was credited with payload %s, the retained outbox held %s", id, got, want)
		}
		t.Logf("batch %s credited exactly once with the original payload sha256=%s", id, got)
	}
	var pgBatches int64
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM usage_batches WHERE agent_id=$1`, r.agentID).Scan(&pgBatches); err != nil {
		t.Fatal(err)
	}
	if pgBatches != int64(len(beforeIDs)) {
		t.Fatalf("PostgreSQL stored %d batch(es) for this Agent, the retained outbox held %d", pgBatches, len(beforeIDs))
	}
	var ledger int64
	if err := st.DB.QueryRowContext(ctx, `SELECT coalesce(sum(up_bytes+down_bytes),0) FROM usage_entries WHERE member_id=$1`, r.memberID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != queuedMemberBytes {
		t.Fatalf("the member ledger holds %d byte(s), the retained outbox held %d", ledger, queuedMemberBytes)
	}
	if got := r.baselineSum(); got != baselineBefore {
		t.Fatalf("the Agent baseline changed across the upgrade: %d -> %d (a lost/rebuilt baseline would double count)", baselineBefore, got)
	}
	if users := r.statusAppliedUsers(); users != 0 {
		t.Fatalf("the upgraded Agent re-applied %d user(s) after the revocation: the upgrade widened authorization", users)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("revoked subscription export returned %d after the upgrade, want 403", code)
	}
	t.Logf("queue/ledger/authorization preserved: %d batch(es) / %d member byte(s) once, baseline %d unchanged, revoked export=403", len(beforeIDs), queuedMemberBytes, baselineBefore)

	// ---- 5. real core switch 26.3.23 -> 26.3.27 under the new Agent -------
	preCore := r.agentRow()
	r.stopAgent(fresh, "new Agent before core switch")
	switched := r.startAgent("new-core", env.newAgent, env.newCore, "")
	afterCore := r.waitAgent("core switched to the current official release", switched, env.newCoreVersion, preCore.Heartbeat)
	if afterCore.ID != before.ID || afterCore.Generation != before.Generation {
		t.Fatal("the core switch changed the Agent identity/control generation")
	}
	if users := r.statusAppliedUsers(); users != 0 {
		t.Fatalf("the core switch re-applied %d revoked user(s)", users)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("revoked subscription export returned %d after the core switch, want 403", code)
	}
	if pending := r.stateCount(`SELECT count(*) FROM batches`); pending != 0 {
		t.Fatalf("the core switch left %d pending batch(es)", pending)
	}
	t.Logf("core switch observable: heartbeat core=%q applied=%d/%d generation=%d, revoked export=403, outbox empty", afterCore.XrayVersion, afterCore.Applied, afterCore.Desired, afterCore.Generation)

	// ---- 6. technical rollback: core back to 26.3.23, then the old binary --
	preRollback := r.agentRow()
	r.stopAgent(switched, "new Agent before core rollback")
	rolled := r.startAgent("rollback-core", env.newAgent, env.oldCore, "")
	afterRollback := r.waitAgent("core rolled back to the verified older release", rolled, env.oldCoreVersion, preRollback.Heartbeat)
	if users := r.statusAppliedUsers(); users != 0 {
		t.Fatalf("the core rollback re-applied %d revoked user(s)", users)
	}
	t.Logf("core rollback observable: heartbeat core=%q applied=%d generation=%d", afterRollback.XrayVersion, afterRollback.Applied, afterRollback.Generation)

	preOld := r.agentRow()
	r.stopAgent(rolled, "new Agent before software rollback")
	back := r.startAgent("rollback-binary", env.oldAgent, env.oldCore, "")
	afterOld := r.waitAgent("old Agent binary restarted on the upgraded state", back, env.oldCoreVersion, preOld.Heartbeat)
	if afterOld.ID != before.ID {
		t.Fatal("the software rollback changed the Agent identity")
	}
	if afterOld.Applied < afterOld.Desired || afterOld.Desired == 0 {
		t.Fatalf("the rolled back Agent did not converge: applied=%d desired=%d", afterOld.Applied, afterOld.Desired)
	}
	if users := r.statusAppliedUsers(); users != 0 {
		t.Fatalf("the software rollback restored %d revoked user(s) from old data", users)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("revoked subscription export returned %d after the software rollback, want 403", code)
	}
	t.Logf("software rollback: old binary sha256=%s on the same state, applied=%d/%d, revoked export=403 (database restore was never used as a rollback)", r.env.oldSHA, afterOld.Applied, afterOld.Desired)
	r.stopAgent(back, "rolled back old Agent")
	if err := os.WriteFile(filepath.Join(r.stateDir, "demo-upgrade-receipt.json"), []byte(fmt.Sprintf(
		"{\"status\":\"pass\",\"agent_id\":%q,\"old_agent_sha256\":%q,\"new_agent_sha256\":%q,\"old_core_sha256\":%q,\"new_core_sha256\":%q,\"retained_batches\":%d,\"retained_member_bytes\":%d,\"baseline\":%d}\n",
		r.agentID, r.env.oldSHA, r.env.newSHA, r.env.oldCoreSHA, r.env.newCoreSHA, len(beforeIDs), queuedMemberBytes, baselineBefore)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("PASS: real old binary/SQLite state -> held outbox -> new binary opens/migrates -> current Panel crediting once -> core switch/rollback -> old binary rollback, no authorization widening")
}

func (r *demoUpgradeRun) startAgent(label, bin, core, registrationCode string) *demoUpgradeAgent {
	r.t.Helper()
	// The configuration is written from the production config type, so the real
	// binaries decode exactly what the current code accepts (no hand-written
	// field list that could silently omit a required default).
	agentCfg := &agentconfig.Config{
		PanelURL: r.proxy.server.URL, XrayPath: core, StateDir: r.stateDir, RegistrationCode: registrationCode,
		PollIntervalSeconds: 1, StatsIntervalSeconds: 1, HeartbeatIntervalSeconds: 1, AllowLoopbackHTTP: true,
	}
	if err := agentCfg.Normalize(); err != nil {
		r.t.Fatalf("%s Agent config: %v", label, err)
	}
	raw, err := json.Marshal(agentCfg)
	if err != nil {
		r.t.Fatal(err)
	}
	cfgPath := filepath.Join(r.work, "agent-"+label+".json")
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		r.t.Fatal(err)
	}
	logPath := filepath.Join(r.work, "agent-"+label+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command(bin, "-config", cfgPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		r.t.Fatalf("start %s Agent: %v", label, err)
	}
	a := &demoUpgradeAgent{label: label, bin: bin, core: core, sha: demoUpgradeFileSHA(r.t, bin), cmd: cmd, done: make(chan error, 1), log: logPath}
	go func() {
		err := cmd.Wait()
		logFile.Close()
		a.done <- err
	}()
	r.agents = append(r.agents, a)
	r.t.Cleanup(func() {
		if a.cmd.ProcessState == nil {
			_ = a.cmd.Process.Kill()
			select {
			case <-a.done:
			case <-time.After(10 * time.Second):
			}
		}
	})
	r.t.Logf("started %s Agent pid=%d bin=%s sha256=%s core=%s", label, cmd.Process.Pid, bin, a.sha, core)
	return a
}

func (r *demoUpgradeRun) stopAgent(a *demoUpgradeAgent, what string) error {
	r.t.Helper()
	if a.cmd.ProcessState != nil {
		return nil
	}
	if err := a.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case err := <-a.done:
		r.t.Logf("%s exited: %v (log %s)", what, err, a.log)
		if err != nil {
			if logs, rerr := os.ReadFile(a.log); rerr == nil && r.t.Failed() {
				r.t.Logf("sanitized %s log:\n%s", what, regexp.MustCompile(`[A-Za-z0-9_+/=-]{24,}`).ReplaceAllString(string(logs), "[redacted]"))
			}
		}
		return err
	case <-time.After(90 * time.Second):
		_ = a.cmd.Process.Kill()
		return fmt.Errorf("%s did not stop within 90s", what)
	}
}

func demoUpgradeWaitBudget() time.Duration {
	raw := strings.TrimSpace(os.Getenv("PP_DEMO_UPGRADE_AGENT_WAIT_SECONDS"))
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 180 * time.Second
}

func (r *demoUpgradeRun) waitAgent(what string, a *demoUpgradeAgent, wantCore string, after time.Time) demoUpgradeAgentRow {
	r.t.Helper()
	var row demoUpgradeAgentRow
	budget := demoUpgradeWaitBudget()
	defer func() {
		if r.t.Failed() {
			// Diagnostics only; tokens/credentials are redacted.
			if logs, err := os.ReadFile(a.log); err == nil {
				r.t.Logf("sanitized %s log (last 60 lines):\n%s", a.label, demoUpgradeTail(regexp.MustCompile(`[A-Za-z0-9_+/=-]{24,}`).ReplaceAllString(string(logs), "[redacted]"), 60))
			}
			r.t.Logf("agent row at timeout: %+v", r.agentRow())
			r.t.Logf("panel responses to the Agent control protocol at timeout:\n%s", r.proxy.audit())
			var receipts int64
			_ = r.st.DB.QueryRowContext(r.ctx, `SELECT count(*) FROM agent_receipts`).Scan(&receipts)
			r.t.Logf("agent_receipts rows: %d", receipts)
		}
	}()
	waitFor(r.t, budget, what, func() bool {
		row = r.agentRow()
		if row.ID == "" || row.Heartbeat.IsZero() || !row.Heartbeat.After(after) {
			return false
		}
		// Confirm the reported heartbeat came from the process we started, not a
		// stale row: the binary path of the live PID must be the requested one.
		exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", a.cmd.Process.Pid))
		if err != nil || exe != a.bin {
			return false
		}
		if row.Applied <= 0 || row.Applied != row.Desired {
			return false
		}
		if wantCore != "" && row.XrayVersion != wantCore {
			return false
		}
		return true
	})
	return row
}

func demoUpgradeTail(text string, lines int) string {
	parts := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

// audit reports what the real panel answered to the Agent control protocol; it is
// the difference between "the Agent never tried" and "the panel refused".
func (p *demoUpgradeBlockProxy) audit() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := make([]string, 0, len(p.status))
	for k := range p.status {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s x%d", k, p.status[k]))
		if v := p.seen[k]; v != "" {
			out = append(out, "   body: "+v)
		}
	}
	return strings.Join(out, "\n")
}

func (r *demoUpgradeRun) agentRow() demoUpgradeAgentRow {
	var row demoUpgradeAgentRow
	q := `SELECT id, version, xray_version, applied_revision, desired_revision, control_generation, coalesce(last_heartbeat_at, to_timestamp(0)) FROM agents WHERE archived=false ORDER BY created_at LIMIT 1`
	err := r.st.DB.QueryRowContext(r.ctx, q).Scan(&row.ID, &row.Version, &row.XrayVersion, &row.Applied, &row.Desired, &row.Generation, &row.Heartbeat)
	if err != nil && err != sql.ErrNoRows {
		r.t.Fatalf("read Agent row: %v", err)
	}
	return row
}

func (r *demoUpgradeRun) memberClient() *dataPlaneAPI {
	jar, _ := cookiejar.New(nil)
	c := &dataPlaneAPI{t: r.t, base: r.base, client: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	c.login("upgrade-member", r.password)
	return c
}

// transfer runs the exported member config through a real core and fetches a
// fixed payload. The proxy path is loopback only; the response hash is checked.
func (r *demoUpgradeRun) transfer(t *testing.T, name string) (int, [32]byte) {
	t.Helper()
	client := r.memberClient()
	subscription := client.request("GET", "/api/v1/me/subscription", nil)
	token, _ := subscription["token"].(string)
	res, err := http.Get(r.base + "/sub/" + token + "?format=xray")
	if err != nil {
		t.Fatalf("download member export: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("member export status %d", res.StatusCode)
	}
	var cfg map[string]any
	if err := json.NewDecoder(res.Body).Decode(&cfg); err != nil {
		t.Fatalf("member export is not JSON: %v", err)
	}
	outbounds, _ := cfg["outbounds"].([]any)
	if len(outbounds) == 0 {
		t.Fatal("export missing outbound")
	}
	if outbounds[0].(map[string]any)["protocol"] != "vless" {
		t.Fatal("export outbound is not the member proxy")
	}
	socksPort := unusedPort(t)
	cfg["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}
	cfg["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": outbounds[0].(map[string]any)["tag"]}}}
	cfgPath := writeConfig(t, r.work, name+"-client.json", cfg)
	coreCmd := exec.Command(r.env.newCore, "run", "-c", cfgPath)
	var coreLog bytes.Buffer
	coreCmd.Stdout, coreCmd.Stderr = &coreLog, &coreLog
	if err := coreCmd.Start(); err != nil {
		t.Fatalf("start client core: %v", err)
	}
	defer func() {
		if coreCmd.ProcessState == nil {
			_ = coreCmd.Process.Kill()
			_ = coreCmd.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("client core never listened: %s", coreLog.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	payload := strings.Repeat("webdemo-upgrade-", 4096) // 69632 bytes, fixed
	want := sha256.Sum256([]byte(payload))
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, payload)
	}))
	defer target.Close()
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", socksPort)})}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	var got []byte
	waitFor(t, 45*time.Second, "real transfer through the exported credentials", func() bool {
		transport.CloseIdleConnections()
		res, err := httpClient.Get(target.URL)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != 200 || len(body) != len(payload) {
			return false
		}
		got = body
		return true
	})
	if sha256.Sum256(got) != want {
		t.Fatal("proxied payload digest mismatch")
	}
	return len(got), want
}

func (r *demoUpgradeRun) stateCount(query string) int64 {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(r.stateDir, "agent.db"))+"?mode=ro&immutable=0&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return -1
	}
	defer db.Close()
	var n int64
	if err := db.QueryRow(query).Scan(&n); err != nil {
		return -1
	}
	return n
}

func (r *demoUpgradeRun) baselineSum() int64 {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(r.stateDir, "agent.db"))+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return -1
	}
	defer db.Close()
	var n int64
	if err := db.QueryRow(`SELECT coalesce(sum(value),0) FROM baseline`).Scan(&n); err != nil {
		return -1
	}
	return n
}

func (r *demoUpgradeRun) statusAppliedUsers() int {
	raw, err := os.ReadFile(filepath.Join(r.stateDir, "status.json"))
	if err != nil {
		return -1
	}
	var doc struct {
		Instances []struct {
			AppliedUsers int `json:"applied_users"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return -1
	}
	total := 0
	for _, inst := range doc.Instances {
		total += inst.AppliedUsers
	}
	return total
}

func (r *demoUpgradeRun) subscriptionStatus() int {
	res, err := http.Get(r.base + "/sub/" + r.token + "?format=xray")
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func (r *demoUpgradeRun) pgBatch(id string) (int, string) {
	var rows int
	var sha string
	if err := r.st.DB.QueryRowContext(r.ctx, `SELECT count(*), coalesce(min(payload_sha256),'') FROM usage_batches WHERE agent_id=$1 AND batch_id=$2`, r.agentID, id).Scan(&rows, &sha); err != nil {
		r.t.Fatalf("read batch %s: %v", id, err)
	}
	return rows, sha
}

func (r *demoUpgradeRun) stateFileSummary() string {
	entries, err := os.ReadDir(r.stateDir)
	if err != nil {
		return "unreadable"
	}
	parts := make([]string, 0, len(entries))
	blocked := make([]string, 0, 2)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		parts = append(parts, e.Name())
		if strings.HasSuffix(e.Name(), "-wal") || strings.HasSuffix(e.Name(), "-journal") {
			blocked = append(blocked, e.Name())
		}
	}
	sort.Strings(parts)
	if len(blocked) > 0 {
		r.t.Fatalf("the stopped old Agent left an unmerged %v: the SQLite evidence cannot be taken as complete", blocked)
	}
	return strings.Join(parts, ",")
}

func (r *demoUpgradeRun) readEvidence(what string) *contracts.AgentStateEvidence {
	r.t.Helper()
	ev, err := state.ReadUpgradeEvidence(r.ctx, r.stateDir)
	if err != nil {
		r.t.Fatalf("%s: production state evidence reader refused the stopped state: %v", what, err)
	}
	return ev
}

func (r *demoUpgradeRun) copyState(label string) string {
	r.t.Helper()
	dir := filepath.Join(r.work, label)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.t.Fatal(err)
	}
	for _, name := range []string{"agent.db"} {
		raw, err := os.ReadFile(filepath.Join(r.stateDir, name))
		if err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	return dir
}

func demoUpgradeExecSQL(path, stmt string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(stmt)
	return err
}
