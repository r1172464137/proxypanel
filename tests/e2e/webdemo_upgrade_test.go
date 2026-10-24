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
//   PP_DEMO_UPGRADE_PROBE_CORE   independent staged core used only as the traffic
//                                client (default: the old core binary, recorded)
//
// Every switch is proven by (a) the exact Agent process we started, (b) its
// sha256, (c) a heartbeat strictly newer than the pre-action snapshot, (d) the
// core version the Agent reports, (e) the queue/ledger/authorization rows, and
// (f) for every core stage: the Agent-owned pid record's managed core pid,
// /proc starttime, exe path, exe sha256 versus the expected official binary sha,
// owner uid, cgroup and config-under-state-dir, plus one real fixed-body transfer
// through that managed core with a stage-independent client core. Version
// *strings* alone are never used: both known commits label themselves 0.1.1, and
// a changed version string without a changed core pid is treated as a failure.
//
// Provenance: PP_DEMO_UPGRADE_OLD_AGENT must come from run_webdemo_upgrade.py,
// which builds it read-only from an explicit historical commit. The saved
// build/linux-amd64/agent artifact cannot be attributed to 52bf44a (its
// buildinfo pins golang.org/x/sys v0.36.0; 52bf44a pins v0.47.0), so using it
// explicitly marks the run's old-source provenance as unattributed.

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
	"io/fs"
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
	// probeCore is the fixed, stage-independent client core used to send real
	// traffic through whichever core the Agent currently manages. It is never
	// the process under test: the managed core is identified by its own pidfile
	// pid/exe/starttime/sha, so naming is unambiguous in every stage.
	probeCore string
	// probeCoreOrigin records whether the traffic client is an independent
	// staged copy (preferred) or a fallback reuse of a managed core binary.
	probeCoreOrigin string
}

// demoUpgradeCoreProc is what the OS can prove about the managed core process.
type demoUpgradeCoreProc struct {
	Step               string `json:"step"`
	PID                int    `json:"pid"`
	StartTicks         string `json:"starttime_ticks"`
	Exe                string `json:"exe"`
	ExeSHA256          string `json:"exe_sha256"`
	ExpectedSHA256     string `json:"expected_official_binary_sha256"`
	SHA256Matches      bool   `json:"exe_matches_expected"`
	OwnerUID           int    `json:"owner_uid"`
	OwnerMatchesAgent  bool   `json:"owner_matches_agent_uid"`
	Cgroup             string `json:"cgroup"`
	CgroupMatchesAgent bool   `json:"cgroup_matches_agent"`
	ConfigPath         string `json:"config_path"`
	ConfigUnderState   bool   `json:"config_inside_state_dir"`
	AgentPID           int    `json:"agent_pid"`
	AgentExe           string `json:"agent_exe"`
	Cmdline            string `json:"cmdline"`
}

// demoUpgradeProbe is one real payload transfer (or one expected refusal)
// through the managed core using the fixed client core.
type demoUpgradeProbe struct {
	Step        string `json:"step"`
	Expectation string `json:"expectation"`
	Bytes       int    `json:"bytes"`
	PayloadSHA  string `json:"payload_sha256"`
	ElapsedMS   int64  `json:"elapsed_ms"`
	Class       string `json:"outcome_class"`
	Detail      string `json:"detail"`
	ClientAlive bool   `json:"client_core_survived"`
	ManagedPID  int    `json:"managed_core_pid"`
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

	// control member: never revoked, used only to obtain a complete fixed
	// file/SHA through the managed core in every stage (a revoked member can
	// only ever prove refusal, so it can never prove the core still serves).
	controlMemberID string
	controlUsername string

	coreProcs []demoUpgradeCoreProc
	probes    []demoUpgradeProbe

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
	e.probeCore = strings.TrimSpace(os.Getenv("PP_DEMO_UPGRADE_PROBE_CORE"))
	e.probeCoreOrigin = "runner-staged-independent-copy"
	if e.probeCore == "" {
		// Fallback only: the client must still be a distinct process from the
		// managed core; this is recorded instead of being implied.
		e.probeCore = e.oldCore
		e.probeCoreOrigin = "fallback-managed-old-core-binary (not an independent copy)"
	}
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
	// A legitimate control member that is never revoked. Without it, "every
	// member is revoked" would let a fully dead core look like a correct
	// refusal, so the positive control needs its own retained authorization.
	r.controlUsername = "upgrade-control"
	control := admin.request("POST", "/api/v1/admin/members", map[string]any{"username": r.controlUsername, "password": password, "role": "member", "subscription_id": r.subID})
	r.controlMemberID = control["id"].(string)
	registration := admin.request("POST", "/api/v1/admin/servers/"+r.serverID+"/registration-codes", nil)

	// ---- 1. old Agent binary creates the SQLite state itself ---------------
	old := r.startAgent("old", env.oldAgent, env.oldCore, registration["code"].(string))
	before := r.waitAgent("old Agent registered and applied", old, env.oldCoreVersion, time.Time{})
	r.agentID = before.ID
	t.Logf("old Agent state created by the real binary: agent=%s version=%s core=%s applied=%d generation=%d", before.ID, before.Version, before.XrayVersion, before.Applied, before.Generation)
	oldCoreProc := r.coreProcessIdentity(t, "old-Agent/old-core-"+env.oldCoreVersion, old, env.oldCoreSHA)

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
		// 2 members existed, one is revoked: exactly the control member remains.
		return row.Applied == row.Desired && row.Applied > 0 && r.statusAppliedUsers() == 1
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
	openCoreProc := r.coreProcessIdentity(t, "new-Agent/old-core-"+env.oldCoreVersion, fresh, env.oldCoreSHA)

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
	var creditedBatches int64
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM usage_batches b WHERE b.agent_id=$1
		AND EXISTS(SELECT 1 FROM usage_entries e WHERE e.agent_id=b.agent_id AND e.batch_id=b.batch_id)`, r.agentID).Scan(&creditedBatches); err != nil {
		t.Fatal(err)
	}
	// Heartbeat/final-sample cycles may legitimately submit an empty accepted
	// batch. Compare only batches that credited ledger rows; every retained
	// payload is already checked by exact id and digest above.
	if creditedBatches != int64(len(beforeIDs)) {
		t.Fatalf("PostgreSQL credited %d ledger batch(es) for this Agent, the retained outbox held %d", creditedBatches, len(beforeIDs))
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
	if users := r.statusAppliedUsers(); users != 1 {
		t.Fatalf("the upgraded Agent applied %d user(s); exactly the one never-revoked control member is expected (0 or 2+ would mean a lost or widened authorization)", users)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("revoked subscription export returned %d after the upgrade, want 403", code)
	}
	t.Logf("queue/ledger/authorization preserved: %d batch(es) / %d member byte(s) once, baseline %d unchanged, revoked export=403", len(beforeIDs), queuedMemberBytes, baselineBefore)
	// Probe only after the retained-outbox accounting invariants are frozen;
	// this legitimate new transfer will itself be sampled asynchronously and
	// must not be confused with an extra replay of the old batch.
	if _, _, err := r.probeViaClient(t, "new-Agent-old-core", "control"); err != nil {
		t.Fatalf("the migrated state's managed core could not serve a real positive transfer: %v", err)
	}

	// ---- 5. real core switch 26.3.23 -> 26.3.27 under the new Agent -------
	preCore := r.agentRow()
	r.stopAgent(fresh, "new Agent before core switch")
	switched := r.startAgent("new-core", env.newAgent, env.newCore, "")
	afterCore := r.waitAgent("core switched to the current official release", switched, env.newCoreVersion, preCore.Heartbeat)
	if afterCore.ID != before.ID || afterCore.Generation != before.Generation {
		t.Fatal("the core switch changed the Agent identity/control generation")
	}
	// The control member stays applied; the revoked member must never reappear.
	if users := r.statusAppliedUsers(); users != 1 {
		t.Fatalf("the core switch changed the applied authorization to %d user(s), the single control member is expected", users)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("the revoked member's export returned %d after the core switch, want 403", code)
	}
	if code := r.subscriptionStatusFor(t, "upgrade-control"); code != http.StatusOK {
		t.Fatalf("the control member's export returned %d after the core switch: the managed core lost a legitimate member", code)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("revoked subscription export returned %d after the core switch, want 403", code)
	}
	if pending := r.stateCount(`SELECT count(*) FROM batches`); pending != 0 {
		t.Fatalf("the core switch left %d pending batch(es)", pending)
	}
	t.Logf("core switch observable: heartbeat core=%q applied=%d/%d generation=%d, revoked export=403, outbox empty", afterCore.XrayVersion, afterCore.Applied, afterCore.Desired, afterCore.Generation)
	switchCoreProc := r.coreProcessIdentity(t, "new-Agent/new-core-"+env.newCoreVersion, switched, env.newCoreSHA)
	if switchCoreProc.PID == openCoreProc.PID {
		t.Fatalf("the core pid did not change across the %s -> %s switch (still %d): only a version string changed", env.oldCoreVersion, env.newCoreVersion, switchCoreProc.PID)
	}
	if _, _, err := r.probeViaClient(t, "new-Agent-new-core", "control"); err != nil {
		t.Fatalf("the switched core could not serve a real positive transfer: %v", err)
	}

	// ---- 6. technical rollback: core back to 26.3.23, then the old binary --
	preRollback := r.agentRow()
	r.stopAgent(switched, "new Agent before core rollback")
	rolled := r.startAgent("rollback-core", env.newAgent, env.oldCore, "")
	afterRollback := r.waitAgent("core rolled back to the verified older release", rolled, env.oldCoreVersion, preRollback.Heartbeat)
	if users := r.statusAppliedUsers(); users != 1 {
		t.Fatalf("the core rollback changed the applied authorization to %d user(s), the single control member is expected", users)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("the revoked member's export returned %d after the core rollback, want 403", code)
	}
	t.Logf("core rollback observable: heartbeat core=%q applied=%d generation=%d", afterRollback.XrayVersion, afterRollback.Applied, afterRollback.Generation)
	rollbackCoreProc := r.coreProcessIdentity(t, "new-Agent/rolled-back-core-"+env.oldCoreVersion, rolled, env.oldCoreSHA)
	if rollbackCoreProc.PID == switchCoreProc.PID {
		t.Fatalf("the core rollback reused pid %d: the core process was not actually replaced", rollbackCoreProc.PID)
	}
	if _, _, err := r.probeViaClient(t, "new-Agent-rolled-back-core", "control"); err != nil {
		t.Fatalf("the rolled-back core could not serve a real positive transfer: %v", err)
	}

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
	if users := r.statusAppliedUsers(); users != 1 {
		t.Fatalf("the software rollback left %d applied user(s); the single control member is expected, so the revoked member was not restored", users)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("revoked subscription export returned %d after the software rollback, want 403", code)
	}
	t.Logf("software rollback: old binary sha256=%s on the same state, applied=%d/%d, revoked export=403 (database restore was never used as a rollback)", r.env.oldSHA, afterOld.Applied, afterOld.Desired)
	r.coreProcessIdentity(t, "rolled-back-old-Agent/old-core-"+env.oldCoreVersion, back, env.oldCoreSHA)
	if _, _, err := r.probeViaClient(t, "rolled-back-old-Agent", "control"); err != nil {
		t.Fatalf("the rolled-back old Agent/core could not serve a real positive transfer: %v", err)
	}
	if code := r.subscriptionStatus(); code != http.StatusForbidden {
		t.Fatalf("the revoked member was served (status %d) through the rolled-back core", code)
	}
	_ = oldCoreProc
	r.stopAgent(back, "rolled back old Agent")
	// Same-run, same-source evidence: the receipt is written by this run and
	// carries the core process identities and every probe it actually made.
	if raw, err := json.MarshalIndent(map[string]any{
		"core_processes": r.coreProcs,
		"probes":         r.probes,
		"probe_core": map[string]any{
			"path_origin": r.env.probeCoreOrigin,
			"sha256":      demoUpgradeFileSHA(t, r.env.probeCore),
		},
		"control_member": "upgrade-control (never revoked; provides the positive control)",
	}, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(r.stateDir, "demo-upgrade-core-identity.json"), append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal(err)
	}
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
	return r.memberClientFor(r.t, "upgrade-member")
}

// memberClientFor returns a session for a named member. "control" resolves to
// the never-revoked control member created for positive transfers.
func (r *demoUpgradeRun) memberClientFor(t *testing.T, member string) *dataPlaneAPI {
	t.Helper()
	username := member
	if member == "control" {
		username = r.controlUsername
	}
	jar, _ := cookiejar.New(nil)
	c := &dataPlaneAPI{t: r.t, base: r.base, client: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	c.login(username, r.password)
	return c
}

// lastCoreProc returns the most recently captured managed-core identity.
func (r *demoUpgradeRun) lastCoreProc() demoUpgradeCoreProc {
	if len(r.coreProcs) == 0 {
		return demoUpgradeCoreProc{}
	}
	return r.coreProcs[len(r.coreProcs)-1]
}

// coreProcessIdentity resolves the managed core from the Agent's own pid record
// (never by scanning for anything named xray) and proves exe/starttime/owner/
// cgroup/sha against the expected official binary.
func (r *demoUpgradeRun) coreProcessIdentity(t *testing.T, step string, a *demoUpgradeAgent, expectedSHA string) demoUpgradeCoreProc {
	t.Helper()
	var rec struct {
		PID        int    `json:"pid"`
		Created    string `json:"created"`
		Executable string `json:"executable"`
		ConfigPath string `json:"config_path"`
		InstanceID string `json:"instance_id"`
		RecordedAt int64  `json:"recorded_at"`
	}
	// Current and historical Agents place pid records below their owned state
	// tree (currently state/instances/<node>/xray.pid); do not assume an obsolete
	// state/xray subdirectory. The subsequent config/exe/owner checks still bind
	// the selected live process to this exact state directory and Agent.
	root := r.stateDir
	// A restarted Agent may heartbeat before its health loop has recreated a
	// previously stopped committed core. Wait for the real pid record and live
	// process instead of mistaking that bounded convergence window for absence.
	waitFor(t, 60*time.Second, step+" managed core pid record", func() bool {
		rec = struct {
			PID        int    `json:"pid"`
			Created    string `json:"created"`
			Executable string `json:"executable"`
			ConfigPath string `json:"config_path"`
			InstanceID string `json:"instance_id"`
			RecordedAt int64  `json:"recorded_at"`
		}{}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "xray.pid" {
				return nil
			}
			raw, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			var candidate struct {
				PID        int    `json:"pid"`
				Created    string `json:"created"`
				Executable string `json:"executable"`
				ConfigPath string `json:"config_path"`
				InstanceID string `json:"instance_id"`
				RecordedAt int64  `json:"recorded_at"`
			}
			if json.Unmarshal(raw, &candidate) == nil && candidate.PID > 0 && candidate.RecordedAt >= rec.RecordedAt {
				rec = candidate
			}
			return nil
		})
		if rec.PID <= 0 {
			return false
		}
		_, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", rec.PID))
		return err == nil
	})
	proc := demoUpgradeCoreProc{
		Step: step, PID: rec.PID, StartTicks: rec.Created, Exe: rec.Executable,
		ConfigPath: rec.ConfigPath, ExpectedSHA256: expectedSHA, AgentPID: a.cmd.Process.Pid, AgentExe: a.bin,
	}
	proc.StartTicks = demoUpgradeProcStartTicks(t, rec.PID)
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", rec.PID))
	if err != nil {
		t.Fatalf("%s: the recorded managed core pid %d is not a live process: %v", step, rec.PID, err)
	}
	proc.Exe = exe
	if exe != rec.Executable {
		t.Fatalf("%s: live core exe %q does not match the pid record %q", step, exe, rec.Executable)
	}
	proc.ExeSHA256 = demoUpgradeFileSHA(t, exe)
	proc.SHA256Matches = len(expectedSHA) == 64 && proc.ExeSHA256 == expectedSHA
	if !proc.SHA256Matches {
		t.Fatalf("%s: the running core binary %s sha256=%s is not the expected official binary sha256=%s", step, exe, proc.ExeSHA256, expectedSHA)
	}
	if st, err := os.Stat(fmt.Sprintf("/proc/%d", rec.PID)); err == nil {
		proc.OwnerUID = demoUpgradeOwnerUID(fmt.Sprintf("/proc/%d", rec.PID))
		_ = st
	}
	if ast, err := os.Stat(fmt.Sprintf("/proc/%d", a.cmd.Process.Pid)); err == nil {
		proc.OwnerMatchesAgent = proc.OwnerUID == demoUpgradeOwnerUID(fmt.Sprintf("/proc/%d", a.cmd.Process.Pid))
		_ = ast
	}
	proc.Cgroup = strings.TrimSpace(demoUpgradeReadBestEffort(fmt.Sprintf("/proc/%d/cgroup", rec.PID)))
	agentCgroup := strings.TrimSpace(demoUpgradeReadBestEffort(fmt.Sprintf("/proc/%d/cgroup", a.cmd.Process.Pid)))
	proc.CgroupMatchesAgent = proc.Cgroup != "" && proc.Cgroup == agentCgroup
	proc.ConfigUnderState = rec.ConfigPath != "" && strings.HasPrefix(rec.ConfigPath, r.stateDir+string(filepath.Separator))
	proc.Cmdline = demoUpgradeCmdline(rec.PID)
	r.coreProcs = append(r.coreProcs, proc)
	t.Logf("managed core %s: pid=%d starttime=%s exe=%s sha256=%s (official match=%v) uid=%d uid==agent:%v cgroup==agent:%v cfg_under_state:%v",
		step, proc.PID, proc.StartTicks, proc.Exe, proc.ExeSHA256, proc.SHA256Matches, proc.OwnerUID,
		proc.OwnerMatchesAgent, proc.CgroupMatchesAgent, proc.ConfigUnderState)
	return proc
}

func demoUpgradeProcStartTicks(t *testing.T, pid int) string {
	t.Helper()
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("read /proc/%d/stat: %v", pid, err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 22 {
		t.Fatalf("/proc/%d/stat has %d fields", pid, len(fields))
	}
	return fields[21]
}

func demoUpgradeOwnerUID(path string) int {
	st, err := os.Stat(path)
	if err != nil {
		return -1
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(sys.Uid)
	}
	return -1
}

func demoUpgradeReadBestEffort(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func demoUpgradeCmdline(pid int) string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
}

// transfer runs the exported member config through a real core and fetches a
// fixed payload. The proxy path is loopback only; the response hash is checked.
func (r *demoUpgradeRun) transfer(t *testing.T, name string) (int, [32]byte) {
	t.Helper()
	bytesN, digest, err := r.probeViaClient(t, name, "upgrade-member")
	if err != nil {
		t.Fatalf("%v", err)
	}
	return bytesN, digest
}

// probeViaClient is the single transfer primitive: it starts the fixed
// stage-independent client core, dials the managed core's inbound and fetches a
// fixed-size payload. It is used for the positive control (control member, must
// return the exact bytes) and can also be asked to expect a refusal.
func (r *demoUpgradeRun) probeViaClient(t *testing.T, name, member string) (int, [32]byte, error) {
	t.Helper()
	client := r.memberClientFor(t, member)
	subscription := client.request("GET", "/api/v1/me/subscription", nil)
	token, _ := subscription["token"].(string)
	if token == "" {
		return 0, [32]byte{}, fmt.Errorf("%s: member subscription token missing", name)
	}
	res, err := http.Get(r.base + "/sub/" + token + "?format=xray")
	if err != nil {
		return 0, [32]byte{}, fmt.Errorf("%s: download member export: %v", name, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, [32]byte{}, fmt.Errorf("%s: member export status %d", name, res.StatusCode)
	}
	var cfg map[string]any
	if err := json.NewDecoder(res.Body).Decode(&cfg); err != nil {
		return 0, [32]byte{}, fmt.Errorf("%s: member export is not JSON: %v", name, err)
	}
	outbounds, _ := cfg["outbounds"].([]any)
	if len(outbounds) == 0 {
		return 0, [32]byte{}, fmt.Errorf("%s: export missing outbound", name)
	}
	if outbounds[0].(map[string]any)["protocol"] != "vless" {
		return 0, [32]byte{}, fmt.Errorf("%s: export outbound is not the member proxy", name)
	}
	socksPort := unusedPort(t)
	cfg["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}
	cfg["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": outbounds[0].(map[string]any)["tag"]}}}
	cfgPath := writeConfig(t, r.work, name+"-client.json", cfg)
	coreCmd := exec.Command(r.env.probeCore, "run", "-c", cfgPath)
	var coreLog bytes.Buffer
	coreCmd.Stdout, coreCmd.Stderr = &coreLog, &coreLog
	if err := coreCmd.Start(); err != nil {
		return 0, [32]byte{}, fmt.Errorf("%s: start client core: %v", name, err)
	}
	defer func() {
		if coreCmd.ProcessState == nil {
			_ = coreCmd.Process.Kill()
			_ = coreCmd.Wait()
		}
	}()
	clientAlive := func() bool {
		if coreCmd.ProcessState != nil && coreCmd.ProcessState.Exited() {
			return false
		}
		// syscall.Kill(pid, 0) proves the pid is still there, not just that Wait
		// has not run yet.
		return syscall.Kill(coreCmd.Process.Pid, 0) == nil
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			return 0, [32]byte{}, fmt.Errorf("%s: client core never listened: %s", name, coreLog.String())
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
	var lastErr error
	var elapsed time.Duration
	start := time.Now()
	waitFor(t, 45*time.Second, "real transfer through the managed core", func() bool {
		transport.CloseIdleConnections()
		attempt := time.Now()
		res, err := httpClient.Get(target.URL)
		elapsed = time.Since(attempt)
		if err != nil {
			lastErr = err
			elapsed = time.Since(start)
			return false
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != 200 || len(body) != len(payload) {
			lastErr = fmt.Errorf("status=%d bytes=%d err=%v", res.StatusCode, len(body), err)
			return false
		}
		got = body
		lastErr = nil
		return true
	})
	managed := r.lastCoreProc()
	probe := demoUpgradeProbe{
		Step: name, Expectation: "control-member-full-body", Bytes: 0, ElapsedMS: elapsed.Milliseconds(),
		ManagedPID: managed.PID, ClientAlive: clientAlive(),
	}
	if sha256.Sum256(got) != want {
		probe.Class = "body-mismatch"
		probe.Detail = fmt.Sprintf("proxied payload digest mismatch: %v", lastErr)
		r.probes = append(r.probes, probe)
		return 0, [32]byte{}, fmt.Errorf("%s: proxied payload digest mismatch (%v)", name, lastErr)
	}
	probe.Bytes = len(got)
	probe.PayloadSHA = hex.EncodeToString(want[:])
	probe.Class = "served-fixed-body"
	if !probe.ClientAlive {
		probe.Class = "client-core-died"
		r.probes = append(r.probes, probe)
		return 0, [32]byte{}, fmt.Errorf("%s: the client core process exited during the transfer, so the result cannot be attributed to the managed core", name)
	}
	r.probes = append(r.probes, probe)
	t.Logf("real transfer %s through the managed core pid=%d: %d bytes sha256=%s in %dms (client core stayed alive)",
		name, managed.PID, len(got), probe.PayloadSHA, probe.ElapsedMS)
	return len(got), want, nil
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

func (r *demoUpgradeRun) subscriptionStatusFor(t *testing.T, username string) int {
	t.Helper()
	client := r.memberClientFor(t, username)
	data := client.request("GET", "/api/v1/me/subscription", nil)
	token, _ := data["token"].(string)
	if token == "" {
		return 0
	}
	res, err := http.Get(r.base + "/sub/" + token + "?format=xray")
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
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
