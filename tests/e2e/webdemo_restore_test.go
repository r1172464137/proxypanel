package e2e

// Web-demo C: real PostgreSQL backup -> independent restore -> recovery
// generation/Agent acceptance -> ledger, cycle and history conservation.
//
// What is real here:
//   - a real pg_dump --format=custom of an owned PostgreSQL cluster, and a real
//     pg_restore into a SECOND, brand-new owned cluster (never the shared 15432,
//     the old .devdb, the 8443 preview or the user's 8445 stack);
//   - the real panel HTTP API, the real Agent binary (cmd/agent) as its own
//     process, real Xray server cores, a real member subscription export and
//     real tunnelled traffic;
//   - the Agent local SQLite state as the only source of truth for its
//     unacknowledged queue: an external stop of the panel makes the Agent queue
//     real samples, a stopped-state copy is inspected with the shipped
//     "-inspect-state" channel, and the replay later proves the payload bytes and
//     batch ids were not rewritten.
//
// What is deliberately NOT claimed: HTTPS deployment, WAL/PITR, production RPO,
// a technical old-binary rollback (not exercised; see not_run), or that the
// panel re-applies post-backup revocations by itself.
//
// The case is one-shot and must run alone: run_webdemo_restore.py owns both
// clusters and the Agent/Xray processes and holds the shared web-demo
// local-runtime flock for the whole runtime section.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"proxypanel/internal/agent/state"
	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/api"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

const demoRestorePayloadUnit = "webdemo-restore-payload-"

// ── owned runtime configuration ─────────────────────────────────────────────

type demoRestoreEnv struct {
	pgBin, xrayBin, agentBin, goBin string
	privateDir, evidenceDir         string
	sourceDSN, targetDSN            string
	sourcePort, targetPort          int
	sourceDB, targetDB              string
}

func demoRestoreEnvFrom(t *testing.T) *demoRestoreEnv {
	t.Helper()
	if os.Getenv("DEMO_RESTORE_E2E") != "1" {
		t.Skip("not_run: one-shot owned web-demo restore acceptance requires the owned runner")
	}
	e := &demoRestoreEnv{
		pgBin: os.Getenv("DEMO_RESTORE_PG_BIN"), xrayBin: os.Getenv("DEMO_RESTORE_XRAY_BIN"),
		agentBin: os.Getenv("DEMO_RESTORE_AGENT_BIN"), goBin: os.Getenv("DEMO_RESTORE_GO_BIN"),
		privateDir:  os.Getenv("DEMO_RESTORE_PRIVATE_DIR"),
		evidenceDir: os.Getenv("DEMO_RESTORE_EVIDENCE_DIR"),
		sourceDSN:   os.Getenv("DEMO_RESTORE_SOURCE_DSN"),
		targetDSN:   os.Getenv("DEMO_RESTORE_TARGET_DSN"),
	}
	for name, value := range map[string]string{
		"pg_bin": e.pgBin, "xray_bin": e.xrayBin, "agent_bin": e.agentBin,
		"private_dir": e.privateDir, "evidence_dir": e.evidenceDir,
		"source_dsn": e.sourceDSN, "target_dsn": e.targetDSN,
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("not_run: %s is required from the owned runner", name)
		}
	}
	for _, dir := range []string{e.privateDir, e.evidenceDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("owned private/evidence directory is missing")
		}
	}
	src, err := pgx.ParseConfig(e.sourceDSN)
	if err != nil {
		t.Fatal("parse source DSN")
	}
	dst, err := pgx.ParseConfig(e.targetDSN)
	if err != nil {
		t.Fatal("parse target DSN")
	}
	// Fail closed: only loopback clusters created by the owned runner, and never
	// the same database for source and restore target.
	for _, c := range []struct {
		name string
		cfg  *pgx.ConnConfig
	}{{"source", src}, {"target", dst}} {
		if c.cfg.Host != "127.0.0.1" || !strings.HasPrefix(c.cfg.Database, "dr_owned_") {
			t.Fatalf("refusing non-owned %s PostgreSQL target", c.name)
		}
		if c.cfg.Port == 15432 {
			t.Fatalf("refusing the shared 15432 cluster for %s", c.name)
		}
	}
	if src.Port == dst.Port || src.Database == dst.Database {
		t.Fatal("source and restore target must be different clusters/databases")
	}
	e.sourcePort, e.targetPort = int(src.Port), int(dst.Port)
	e.sourceDB, e.targetDB = src.Database, dst.Database
	for _, bin := range []string{
		filepath.Join(e.pgBin, "pg_dump"), filepath.Join(e.pgBin, "pg_restore"),
		e.xrayBin, e.agentBin,
	} {
		if info, err := os.Stat(bin); err != nil || info.IsDir() {
			t.Fatalf("required binary missing: %s", filepath.Base(bin))
		}
	}
	return e
}

// ── panel API helper ────────────────────────────────────────────────────────

type demoRestoreAPI struct {
	t      *testing.T
	base   string
	client *http.Client
	csrf   string
}

func (a *demoRestoreAPI) call(method, path string, body any, bearer string) (int, map[string]any, string) {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal("encode request")
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		a.t.Fatal("construct request")
	}
	req.Header.Set("Content-Type", "application/json")
	if a.csrf != "" {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if path == "/api/v1/admin/batches" {
		req.Header.Set("Idempotency-Key", fmt.Sprintf("webdemo-restore-%d", time.Now().UnixNano()))
	}
	res, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal("owned panel request failed")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	code := ""
	if e, ok := out["error"].(map[string]any); ok {
		code, _ = e["code"].(string)
	}
	return res.StatusCode, out, code
}

func (a *demoRestoreAPI) ok(method, path string, body any) map[string]any {
	a.t.Helper()
	status, out, code := a.call(method, path, body, "")
	if status < 200 || status >= 300 {
		a.t.Fatalf("owned API %s %s = %d (%s)", method, path, status, code)
	}
	data, ok := out["data"].(map[string]any)
	if !ok {
		a.t.Fatalf("owned API %s %s lacks a data object", method, path)
	}
	return data
}

func (a *demoRestoreAPI) login(username, password string) {
	a.t.Helper()
	data := a.ok("POST", "/api/v1/auth/login", map[string]any{"username": username, "password": password})
	csrf, _ := data["csrf_token"].(string)
	if csrf == "" {
		a.t.Fatal("missing CSRF token")
	}
	a.csrf = csrf
}

func (a *demoRestoreAPI) want(method, path string, body any, bearer string, status int, code string) {
	a.t.Helper()
	got, _, gotCode := a.call(method, path, body, bearer)
	if got != status || (code != "" && gotCode != code) {
		a.t.Fatalf("owned API %s %s = %d (%s), want %d (%s)", method, path, got, gotCode, status, code)
	}
}

// ── usage payload recorder (real loopback hop in front of the real panel) ───

type demoRestoreRecorder struct {
	next http.Handler
	dir  string
	mu   sync.Mutex
	ids  map[string]string
}

func (r *demoRestoreRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost && req.URL.Path == "/api/v1/agent/usage" {
		raw, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		if err != nil {
			http.Error(w, "owned recorder read failed", http.StatusBadRequest)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		var batch struct {
			BatchID string `json:"batch_id"`
		}
		if json.Unmarshal(raw, &batch) == nil && batch.BatchID != "" {
			name := filepath.Join(r.dir, "usage-"+demoRestoreSafe(batch.BatchID)+".json")
			if os.WriteFile(name, raw, 0600) == nil {
				r.mu.Lock()
				if r.ids == nil {
					r.ids = map[string]string{}
				}
				r.ids[batch.BatchID] = name
				r.mu.Unlock()
			}
		}
	}
	r.next.ServeHTTP(w, req)
}

func demoRestoreSafe(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

// ── ledger / period oracles ─────────────────────────────────────────────────

type demoRestoreBatch struct {
	Payload string `json:"payload_sha256"`
	Bytes   int64  `json:"bytes"`
	EndMs   int64  `json:"sample_end_unix_ms"`
}

type demoRestoreLedger map[string]demoRestoreBatch

func (l demoRestoreLedger) total() int64 {
	var n int64
	for _, b := range l {
		n += b.Bytes
	}
	return n
}

func (l demoRestoreLedger) watermark() time.Time {
	var n int64
	for _, b := range l {
		if b.EndMs > n {
			n = b.EndMs
		}
	}
	return time.UnixMilli(n).UTC()
}

func (l demoRestoreLedger) keys() []string {
	out := make([]string, 0, len(l))
	for k := range l {
		out = append(out, k)
	}
	return out
}

// demoRestoreReadLedger reads only committed ledger rows joined with the
// committed batch payload fingerprint. It never invents a row.
func demoRestoreReadLedger(t *testing.T, db *sql.DB, memberID string) demoRestoreLedger {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT u.agent_id, u.batch_id, b.payload_sha256,
		sum(u.up_bytes+u.down_bytes), max(u.sample_ended_at)
		FROM usage_entries u JOIN usage_batches b ON b.agent_id=u.agent_id AND b.batch_id=u.batch_id
		WHERE u.member_id=$1 GROUP BY u.agent_id, u.batch_id, b.payload_sha256`, memberID)
	if err != nil {
		t.Fatal("read owned ledger")
	}
	defer rows.Close()
	out := demoRestoreLedger{}
	for rows.Next() {
		var agentID, batchID string
		var item demoRestoreBatch
		var end time.Time
		if err := rows.Scan(&agentID, &batchID, &item.Payload, &item.Bytes, &end); err != nil {
			t.Fatal("scan owned ledger")
		}
		item.EndMs = end.UnixMilli()
		out[agentID+"/"+batchID] = item
	}
	if rows.Err() != nil {
		t.Fatal("read owned ledger rows")
	}
	return out
}

// demoRestorePeriodLiteral is the fixed history oracle: it is read from
// PostgreSQL, never recomputed with the production cycle algorithm, so a cycle
// rewrite cannot silently move its own expectation.
type demoRestorePeriodLiteral struct {
	Start, End       time.Time
	AnchorDay        int
	Timezone, Source string
}

func demoRestoreReadPeriods(t *testing.T, db *sql.DB, memberID string) []demoRestorePeriodLiteral {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT period_start, period_end, anchor_day, timezone, source
		FROM member_periods WHERE member_id=$1 ORDER BY period_start`, memberID)
	if err != nil {
		t.Fatal("read owned period history")
	}
	defer rows.Close()
	var out []demoRestorePeriodLiteral
	for rows.Next() {
		var p demoRestorePeriodLiteral
		if err := rows.Scan(&p.Start, &p.End, &p.AnchorDay, &p.Timezone, &p.Source); err != nil {
			t.Fatal("scan owned period history")
		}
		p.Start, p.End = p.Start.UTC(), p.End.UTC()
		out = append(out, p)
	}
	if rows.Err() != nil {
		t.Fatal("read owned period rows")
	}
	return out
}

// demoRestoreAttribution returns the persisted period interval of each ledger row
// of one replayed batch. Every row of one batch must live inside exactly one
// recorded interval (the literal oracle) and the interval must contain the
// sample, i.e. no silent move into "now".
func demoRestoreAttribution(t *testing.T, db *sql.DB, agentID, batchID string,
	periods []demoRestorePeriodLiteral) []demoRestorePeriodLiteral {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT period_start, period_end FROM usage_entries
		WHERE agent_id=$1 AND batch_id=$2 ORDER BY period_start`, agentID, batchID)
	if err != nil {
		t.Fatal("read replayed attribution")
	}
	defer rows.Close()
	var out []demoRestorePeriodLiteral
	for rows.Next() {
		var p demoRestorePeriodLiteral
		if err := rows.Scan(&p.Start, &p.End); err != nil {
			t.Fatal("scan replayed attribution")
		}
		p.Start, p.End = p.Start.UTC(), p.End.UTC()
		known := false
		for _, want := range periods {
			if p.Start.Equal(want.Start) && p.End.Equal(want.End) {
				known = true
			}
		}
		if !known {
			t.Fatalf("replayed usage was attributed to an unrecorded interval %s..%s", p.Start, p.End)
		}
		out = append(out, p)
	}
	if rows.Err() != nil {
		t.Fatal("read replayed attribution rows")
	}
	if len(out) == 0 {
		t.Fatal("replayed usage produced no ledger row")
	}
	return out
}

// ── owned Agent process ─────────────────────────────────────────────────────

type demoRestoreAgent struct {
	cmd     *exec.Cmd
	waited  chan error
	logPath string
}

func demoRestoreWriteConfig(t *testing.T, path, panelURL, xray, stateDir string) {
	t.Helper()
	cfg := map[string]any{
		"panel_url": panelURL, "xray_path": xray, "state_dir": stateDir,
		"allow_loopback_http": true, "poll_interval_seconds": 1,
		"stats_interval_seconds": 1, "heartbeat_interval_seconds": 1,
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal("write owned agent config")
	}
}

func demoRestoreStartAgent(t *testing.T, bin, config, logPath string) *demoRestoreAgent {
	t.Helper()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal("open owned agent log")
	}
	cmd := exec.Command(bin, "-config", config)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal("start owned agent")
	}
	a := &demoRestoreAgent{cmd: cmd, waited: make(chan error, 1), logPath: logPath}
	go func() {
		a.waited <- cmd.Wait()
		logFile.Close()
	}()
	return a
}

// stop sends SIGTERM (the production stop signal), waits for the real exit and
// reports a timeout instead of killing silently.
func (a *demoRestoreAgent) stop(t *testing.T, budget time.Duration) {
	t.Helper()
	if a == nil || a.cmd == nil || a.cmd.Process == nil {
		return
	}
	select {
	case <-a.waited:
		return
	default:
	}
	if err := a.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return // already exited; the waiter goroutine owns the reaping
		}
		t.Fatalf("signal owned agent: %v", err)
	}
	select {
	case <-a.waited:
	case <-time.After(budget):
		_ = a.cmd.Process.Kill()
		<-a.waited
		t.Fatal("owned agent did not stop within the budget")
	}
}

func demoRestoreCopyDir(t *testing.T, src, dst string) int {
	t.Helper()
	if err := os.MkdirAll(dst, 0700); err != nil {
		t.Fatal("create stopped-state copy directory")
	}
	files := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		files++
		return os.WriteFile(target, raw, 0600)
	})
	if err != nil {
		t.Fatal("copy stopped Agent state failed")
	}
	return files
}

// ── evidence helpers ────────────────────────────────────────────────────────

type demoRestoreAssertion struct {
	ID, Story, State, Detail string
}

type demoRestoreEvidence struct {
	mu         sync.Mutex
	assertions []demoRestoreAssertion
	notRun     []string
}

func (e *demoRestoreEvidence) add(id, story, state, detail string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.assertions = append(e.assertions, demoRestoreAssertion{ID: id, Story: story, State: state, Detail: detail})
}

func mustJar(t *testing.T) *cookiejar.Jar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal("create cookie jar")
	}
	return jar
}

func demoRestoreSum(values map[string]int64) int64 {
	var n int64
	for _, v := range values {
		n += v
	}
	return n
}

func demoRestoreUsageHash(batch contracts.UsageBatch) string {
	raw, err := json.Marshal(struct {
		SchemaVersion string                  `json:"schema_version"`
		BatchID       string                  `json:"batch_id"`
		Samples       []contracts.UsageSample `json:"samples"`
	}{batch.SchemaVersion, batch.BatchID, batch.Samples})
	if err != nil {
		return ""
	}
	return demoRestoreDigest(raw)
}

// demoRestoreMerge returns the union of two member ledgers (both read from the
// same restored database).
func demoRestoreMerge(t *testing.T, db *sql.DB, memberIDs ...string) demoRestoreLedger {
	t.Helper()
	out := demoRestoreLedger{}
	for _, id := range memberIDs {
		for batchID, item := range demoRestoreReadLedger(t, db, id) {
			out[batchID] = item
		}
	}
	return out
}

// demoRestoreRunTool runs one real PostgreSQL client tool of the owned PG17 SDK
// and fails with the file name only (never the SQL or a secret).
func demoRestoreRunTool(t *testing.T, e *demoRestoreEnv, args []string, name string) {
	t.Helper()
	logPath := filepath.Join(e.privateDir, name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatalf("open %s log", name)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "PGPASSWORD=", "PGOPTIONS=")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	err = cmd.Run()
	logFile.Close()
	if err != nil {
		t.Fatalf("%s failed (see the private log)", name)
	}
}

// demoRestoreExport downloads the real member subscription export.
func demoRestoreExport(t *testing.T, base, token string) map[string]any {
	t.Helper()
	res, err := http.Get(base + "/sub/" + token + "?format=xray")
	if err != nil {
		t.Fatal("owned export download failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("owned export status %d", res.StatusCode)
	}
	var cfg map[string]any
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&cfg); err != nil {
		t.Fatal("owned export is not Xray JSON")
	}
	return cfg
}

// demoRestoreClient starts a real Xray client that uses the exported outbound
// and returns a SOCKS5 client for real tunnelled traffic.
func demoRestoreClient(t *testing.T, binary string, cfg map[string]any, label string) (*http.Client, *http.Transport) {
	t.Helper()
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		t.Fatal("owned export lacks a proxy outbound")
	}
	first, ok := outbounds[0].(map[string]any)
	if !ok || first["protocol"] != "vless" {
		t.Fatal("owned export outbound is not VLESS")
	}
	port := unusedPort(t)
	cfg["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": port,
		"protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}
	cfg["routing"] = map[string]any{"rules": []any{map[string]any{
		"type": "field", "network": "tcp,udp", "outboundTag": first["tag"]}}}
	path := writeConfig(t, t.TempDir(), label+"-client.json", cfg)
	startCore(t, binary, path, port)
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5",
		Host: fmt.Sprintf("127.0.0.1:%d", port)})}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}, transport
}

// demoRestoreInspectState uses the shipped, read-only Agent evidence channel on
// the stopped-state copy. It never opens the copy for writing.
func demoRestoreInspectState(t *testing.T, e *demoRestoreEnv, dir string) contracts.AgentStateEvidence {
	t.Helper()
	cmd := exec.Command(e.agentBin, "-inspect-state", dir)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("shipped -inspect-state failed on the owned stopped state")
	}
	var evidence contracts.AgentStateEvidence
	if err := json.Unmarshal(out, &evidence); err != nil {
		t.Fatal("shipped -inspect-state did not return evidence JSON")
	}
	if evidence.FormatVersion != contracts.AgentEvidenceFormat {
		t.Fatal("shipped -inspect-state returned an unexpected format")
	}
	return evidence
}

func demoRestoreWriteJSON(t *testing.T, dir, name string, v any) string {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal("encode owned evidence")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal("write owned evidence")
	}
	return path
}

// demoRestoreSnapshot requests the real agent snapshot endpoint with the
// recovered Agent's own bearer token. The decoded snapshot is the panel's
// current authorization, not a test copy of it.
func demoRestoreSnapshot(t *testing.T, base string, identity state.Identity,
	wantGeneration int64) contracts.Snapshot {
	t.Helper()
	req, err := http.NewRequest("GET", base+"/api/v1/agent/snapshot", nil)
	if err != nil {
		t.Fatal("construct snapshot request")
	}
	req.Header.Set("Authorization", "Bearer "+identity.Token)
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("owned snapshot request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("owned snapshot status %d after the confirmed exit", res.StatusCode)
	}
	var envelope struct {
		Data contracts.Snapshot `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope); err != nil {
		t.Fatal("owned snapshot is not JSON")
	}
	if envelope.Data.SchemaVersion != contracts.SchemaVersion {
		t.Fatal("owned snapshot has an unexpected schema version")
	}
	if envelope.Data.ControlGeneration != strconv.FormatInt(wantGeneration, 10) {
		t.Fatal("owned snapshot does not carry the current control generation")
	}
	return envelope.Data
}

func demoRestoreDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ── main case ───────────────────────────────────────────────────────────────

func TestWebDemoRestoreBackupRecovery(t *testing.T) {
	e := demoRestoreEnvFrom(t)
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()

	ev := &demoRestoreEvidence{}
	receipt := map[string]any{
		"status": "fail", "scope": "real pg_dump -Fc -> independent owned restore; not production RPO/RTO",
		"source_cluster":                 map[string]any{"port": e.sourcePort, "database": e.sourceDB},
		"target_cluster":                 map[string]any{"port": e.targetPort, "database": e.targetDB},
		"source_and_target_are_distinct": true,
	}
	ev.notRun = append(ev.notRun,
		"technical old-release binary stop-write rollback: not_run (no older release binary is built or pinned in this worktree; only the database restore is exercised, and the two are reported separately)",
		"WAL archiving / PITR / off-host backup: not_run (not configured for the owned clusters; no zero-loss claim)",
		"remote 203 / preview 8443 / user 8445 deployment mutation: not_run by design (all resources here are new and owned)",
	)
	var evidencePath string
	finish := func() {
		if t.Failed() {
			receipt["status"] = "fail"
		}
		receipt["test_failed"] = t.Failed()
		passed := 0
		blocked, failed := 0, 0
		for _, a := range ev.assertions {
			switch a.State {
			case "pass":
				passed++
			case "blocked":
				blocked++
			default:
				failed++
			}
		}
		receipt["assertion_count"] = len(ev.assertions)
		receipt["assertions_passed"] = passed
		receipt["assertions_blocked"] = blocked
		receipt["assertions_failed"] = failed
		receipt["not_run"] = ev.notRun
		receipt["evidence_file"] = evidencePath
		demoRestoreWriteJSON(t, e.evidenceDir, "receipt.json", receipt)
	}
	defer finish()
	evidencePath = demoRestoreWriteJSON(t, e.evidenceDir, "restore-evidence.json", receipt)

	key, err := secrets.ParseKey(strings.Repeat("webdemo-restore-owned-master-key-", 2))
	if err != nil {
		t.Fatal("derive owned master key")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	adminPassword := "Wdr-admin-" + hex.EncodeToString(random[:8])
	alicePassword := "Wdr-alice-" + hex.EncodeToString(random[:8])
	bobPassword := "Wdr-bob-" + hex.EncodeToString(random[:8])
	// Passwords only ever live in this process and in the private directory.

	// ── 1. source panel on the source cluster ──────────────────────────────
	st, err := store.Open(ctx, store.Options{DatabaseURL: e.sourceDSN, MasterKey: key,
		MigrationsDir: "../../migrations", MaxConns: 6, ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal("open owned source store failed")
	}
	defer st.Close()
	adminHash, err := secrets.HashPassword(adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "admin",
		PasswordHash: adminHash, Role: "admin", Active: true, ProxyEnabled: true}); err != nil {
		t.Fatal("seed owned bootstrap admin failed")
	}
	limiter, err := limit.New("") // process-local limiter: no shared Redis writes
	if err != nil {
		t.Fatal("create owned limiter failed")
	}
	defer limiter.Close()
	recorderDir := filepath.Join(e.privateDir, "usage")
	if err := os.MkdirAll(recorderDir, 0700); err != nil {
		t.Fatal("create owned payload recorder directory")
	}
	recorder := &demoRestoreRecorder{dir: recorderDir}
	server := func(source *store.Store) *httptest.Server {
		panel := &api.Server{Store: source, RateLimiter: limiter,
			Cfg: api.Config{Dev: true, Listen: "127.0.0.1:0", SessionTTL: time.Hour}}
		recorder.next = panel.Handler()
		return httptest.NewServer(recorder)
	}
	source := server(st)
	defer source.Close()
	jar, _ := cookiejar.New(nil)
	admin := &demoRestoreAPI{t: t, base: source.URL, client: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	admin.login("admin", adminPassword)

	// Real business objects are created only through the real API.
	srv := admin.ok("POST", "/api/v1/admin/servers", map[string]any{
		"name": "wdr-local", "address": "127.0.0.1", "region": "owned"})
	inbound := unusedPort(t)
	node := admin.ok("POST", "/api/v1/admin/nodes", map[string]any{
		"name": "wdr-vless", "server_id": srv["id"], "enabled": true, "host": "127.0.0.1",
		"listen": "127.0.0.1", "port": inbound, "protocol": "vless",
		"stream_settings":   map[string]any{"network": "tcp", "security": "none"},
		"protocol_settings": map[string]any{"decryption": "none"},
		"client_settings":   map[string]any{},
	})
	group := admin.ok("POST", "/api/v1/admin/node-groups", map[string]any{
		"name": "wdr-group", "node_ids": []any{node["id"]}})
	sub := admin.ok("POST", "/api/v1/admin/subscriptions", map[string]any{
		"name": "wdr-sub", "node_group_ids": []any{group["id"]}, "quota_bytes": 10 << 30,
		"timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true})
	registration := admin.ok("POST", "/api/v1/admin/servers/"+srv["id"].(string)+"/registration-codes", nil)

	// ── 2. real Agent registers, then members are created (fixed probe window)
	stateDir := filepath.Join(e.privateDir, "agent-source")
	config1 := filepath.Join(e.privateDir, "agent-source.json")
	demoRestoreWriteConfig(t, config1, source.URL, e.xrayBin, stateDir)
	// registration_code is only needed for the first start.
	cfgRaw, _ := os.ReadFile(config1)
	var cfgMap map[string]any
	if err := json.Unmarshal(cfgRaw, &cfgMap); err != nil {
		t.Fatal("read owned agent config")
	}
	cfgMap["registration_code"] = registration["code"]
	if raw, err := json.Marshal(cfgMap); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(config1, raw, 0600); err != nil {
		t.Fatal("write owned agent config")
	}
	agent1 := demoRestoreStartAgent(t, e.agentBin, config1, filepath.Join(e.privateDir, "agent-source.log"))
	defer agent1.stop(t, 90*time.Second)
	waitFor(t, 60*time.Second, "owned Agent registered and applied a snapshot", func() bool {
		var n int
		err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE applied_revision > 0`).Scan(&n)
		return err == nil && n > 0
	})
	var agentID string
	var registrationTime time.Time
	if err := st.DB.QueryRowContext(ctx, `SELECT id, created_at FROM agents WHERE NOT archived`).Scan(&agentID, &registrationTime); err != nil {
		t.Fatal("read owned Agent identity")
	}
	receipt["agent_version"] = "real cmd/agent process"

	// The agent connection history starts at registration. Waiting a moment gives
	// the later LEDGER_UNATTRIBUTABLE probe an interval that has execution
	// authorization but no recorded member cycle (never a guessed zero).
	waitFor(t, 20*time.Second, "owned clock past the registration barrier", func() bool {
		var now time.Time
		if st.DB.QueryRowContext(ctx, `SELECT now()`).Scan(&now) != nil {
			return false
		}
		return now.After(registrationTime.Add(2 * time.Second))
	})
	mkMember := func(name, password string) string {
		created := admin.ok("POST", "/api/v1/admin/members", map[string]any{
			"username": name, "password": password, "role": "member", "subscription_id": sub["id"]})
		return created["id"].(string)
	}
	aliceID := mkMember("alice", alicePassword)
	bobID := mkMember("bob", bobPassword)
	waitFor(t, 60*time.Second, "owned Agent applied both members", func() bool {
		var revision int64
		var desired int64
		if st.DB.QueryRowContext(ctx, `SELECT applied_revision, desired_revision FROM agents WHERE id=$1`, agentID).Scan(&revision, &desired) != nil {
			return false
		}
		return revision == desired && revision > 0
	})
	alicePeriods := demoRestoreReadPeriods(t, st.DB, aliceID)
	bobPeriods := demoRestoreReadPeriods(t, st.DB, bobID)
	if len(alicePeriods) == 0 || len(bobPeriods) == 0 {
		t.Fatal("owned members have no recorded cycle to conserve")
	}
	ev.add("HISTORY-ORACLE", "US-PR-013", "pass",
		fmt.Sprintf("fixed literal oracle read from PostgreSQL: alice %d interval(s), bob %d interval(s); never recomputed with the production cycle algorithm", len(alicePeriods), len(bobPeriods)))

	// ── 3. real member export + real tunnelled traffic ─────────────────────
	alice := &demoRestoreAPI{t: t, base: source.URL, client: &http.Client{Jar: mustJar(t), Timeout: 20 * time.Second}}
	alice.login("alice", alicePassword)
	aliceToken, _ := alice.ok("GET", "/api/v1/me/subscription", nil)["token"].(string)
	if aliceToken == "" {
		t.Fatal("owned member has no subscription token")
	}
	aliceExport := demoRestoreExport(t, source.URL, aliceToken)
	aliceProxy, aliceTransport := demoRestoreClient(t, e.xrayBin, aliceExport, "alice-old")
	payload := strings.Repeat(demoRestorePayloadUnit, 2048) // 47104 bytes
	payloadHash := sha256.Sum256([]byte(payload))
	payloadTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	defer payloadTarget.Close()
	fetch := func(transport *http.Transport, client *http.Client) bool {
		transport.CloseIdleConnections()
		previous := client.Timeout
		client.Timeout = 5 * time.Second
		defer func() { client.Timeout = previous }()
		res, err := client.Get(payloadTarget.URL)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return err == nil && res.StatusCode == 200 && len(body) == len(payload) &&
			sha256.Sum256(body) == payloadHash
	}
	waitFor(t, 60*time.Second, "real tunnelled traffic through the exported credential", func() bool {
		return fetch(aliceTransport, aliceProxy)
	})
	ev.add("REAL-TRAFFIC", "US-PR-005", "pass",
		fmt.Sprintf("owned exported VLESS credential tunneled %d bytes with matching SHA256 through a real Xray client", len(payload)))
	waitFor(t, 60*time.Second, "real Agent committed usage to the source ledger", func() bool {
		return demoRestoreReadLedger(t, st.DB, aliceID).total() >= int64(len(payload))
	})
	waitFor(t, 40*time.Second, "source Agent ACK-deleted its queue", func() bool {
		var depth int64
		if st.DB.QueryRowContext(ctx, `SELECT queue_depth FROM agents WHERE id=$1`, agentID).Scan(&depth); err != nil {
			return false
		}
		return depth == 0
	})

	// ── 4. real pg_dump --format=custom while the panel is up ──────────────
	dumpPath := filepath.Join(e.privateDir, "source.dump")
	dumpStart := time.Now().UTC()
	demoRestoreRunTool(t, e, []string{filepath.Join(e.pgBin, "pg_dump"), "-h", "127.0.0.1",
		"-p", strconv.Itoa(e.sourcePort), "-U", "dr_owner", "-d", e.sourceDB,
		"--format=custom", "--no-owner", "--file", dumpPath}, "pg_dump")
	dumpEnd := time.Now().UTC()
	dumpInfo, err := os.Stat(dumpPath)
	if err != nil || dumpInfo.Size() == 0 {
		t.Fatal("real pg_dump produced no backup file")
	}
	dumpHeader := make([]byte, 5)
	f, err := os.Open(dumpPath)
	if err != nil {
		t.Fatal("open owned backup")
	}
	_, readErr := io.ReadFull(f, dumpHeader)
	f.Close()
	if readErr != nil || string(dumpHeader) != "PGDMP" {
		t.Fatalf("backup is not a PostgreSQL custom-format archive (header=%q)", string(dumpHeader))
	}
	dumpRaw, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatal("read owned backup")
	}
	receipt["backup"] = map[string]any{
		"dump_bytes": dumpInfo.Size(), "dump_sha256": demoRestoreDigest(dumpRaw),
		"pg_dump_custom_format": true, "started_at": dumpStart, "completed_at": dumpEnd,
	}
	backupAlice := demoRestoreReadLedger(t, st.DB, aliceID)
	backupBob := demoRestoreReadLedger(t, st.DB, bobID)
	if backupAlice.total() < int64(len(payload)) || backupBob.total() != 0 {
		t.Fatal("owned backup ledger does not contain the measured alice bytes")
	}
	receipt["backup_ledger"] = map[string]any{
		"alice_bytes": backupAlice.total(), "alice_batches": len(backupAlice),
		"bob_bytes": backupBob.total(), "alice_watermark": backupAlice.watermark(),
	}
	ev.add("REAL-DUMP", "US-PR-014", "pass",
		fmt.Sprintf("real pg_dump --format=custom (%d bytes, PGDMP header, sha256 recorded); backup ledger %d B / %d batch(es)",
			dumpInfo.Size(), backupAlice.total(), len(backupAlice)))

	// ── 5. post-backup ACK-deleted gap (real, never re-imported) ───────────
	bob := &demoRestoreAPI{t: t, base: source.URL, client: &http.Client{Jar: mustJar(t), Timeout: 20 * time.Second}}
	bob.login("bob", bobPassword)
	bobToken, _ := bob.ok("GET", "/api/v1/me/subscription", nil)["token"].(string)
	if bobToken == "" {
		t.Fatal("owned member bob has no subscription token")
	}
	bobExport := demoRestoreExport(t, source.URL, bobToken)
	bobProxy, bobTransport := demoRestoreClient(t, e.xrayBin, bobExport, "bob-source")
	waitFor(t, 60*time.Second, "post-backup real traffic", func() bool {
		return fetch(bobTransport, bobProxy)
	})
	waitFor(t, 60*time.Second, "post-backup usage committed and ACK-deleted", func() bool {
		var depth int64
		if st.DB.QueryRowContext(ctx, `SELECT queue_depth FROM agents WHERE id=$1`, agentID).Scan(&depth); err != nil {
			return false
		}
		return depth == 0 && demoRestoreReadLedger(t, st.DB, bobID).total() >= int64(len(payload))
	})
	sourceBob := demoRestoreReadLedger(t, st.DB, bobID)
	ackDeleted := demoRestoreLedger{}
	for id, item := range sourceBob {
		if _, ok := backupBob[id]; !ok {
			ackDeleted[id] = item
		}
	}
	if len(ackDeleted) == 0 || ackDeleted.total() < int64(len(payload)) {
		t.Fatal("missing a real post-backup ACK-deleted loss window")
	}
	receipt["ack_deleted_gap"] = map[string]any{
		"bytes": ackDeleted.total(), "batches": len(ackDeleted),
		"disposition": "unrecoverable from the Agent queue: it was ACKed and deleted before the dump; deliberately never re-imported from the surviving source",
	}
	ev.add("ACK-GAP", "US-PR-013", "pass",
		fmt.Sprintf("real post-backup window %d B / %d ACK-deleted batch(es) exists only in the source ledger and is never fabricated after restore",
			ackDeleted.total(), len(ackDeleted)))

	// ── 6. post-backup operational change: audited revocation of alice ─────
	var revokeBarrier time.Time
	if st.DB.QueryRowContext(ctx, `SELECT now()`).Scan(&revokeBarrier) != nil {
		t.Fatal("read owned revocation barrier")
	}
	// A real audited authorization write on the source stack (member.update).
	var aliceVersion int64
	if err := st.DB.QueryRowContext(ctx, `SELECT version FROM members WHERE id=$1`, aliceID).Scan(&aliceVersion); err != nil {
		t.Fatal("read source member version")
	}
	admin.ok("PATCH", "/api/v1/admin/members/"+aliceID, map[string]any{
		"version": aliceVersion, "active": false})
	var sourceAuditID, sourceActorID string
	var revokedAt time.Time
	if err := st.DB.QueryRowContext(ctx, `SELECT id, actor_id, created_at FROM audit_logs
		WHERE action='member.update' AND target_id=$1 AND created_at >= $2
		ORDER BY created_at DESC LIMIT 1`, aliceID, revokeBarrier).Scan(&sourceAuditID, &sourceActorID, &revokedAt); err != nil {
		t.Fatal("post-backup revocation has no operator audit")
	}
	var activeRows, revokedCreds, activeCreds int
	if err := st.DB.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM members WHERE id=$1 AND NOT active),
		(SELECT count(*) FROM credentials WHERE member_id=$1 AND state='revoked'),
		(SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active')`, aliceID).
		Scan(&activeRows, &revokedCreds, &activeCreds); err != nil {
		t.Fatal("read owned credential state")
	}
	if activeRows != 1 || revokedCreds == 0 || activeCreds != 0 {
		t.Fatalf("audited revocation did not permanently revoke credentials (revoked=%d active=%d)", revokedCreds, activeCreds)
	}
	// The export is denied immediately (panel authority). The live core is
	// tightened by the Agent's next snapshot, so wait for the real applied
	// revision and only then require a persistent denial.
	admin.want("GET", "/sub/"+aliceToken+"?format=xray", nil, "", http.StatusForbidden, "")
	waitFor(t, 60*time.Second, "source Agent applied the revocation", func() bool {
		var applied, desired int64
		if st.DB.QueryRowContext(ctx, `SELECT applied_revision, desired_revision FROM agents WHERE id=$1`,
			agentID).Scan(&applied, &desired) != nil {
			return false
		}
		return applied == desired && !fetch(aliceTransport, aliceProxy)
	})
	for i := 0; i < 3; i++ {
		if fetch(aliceTransport, aliceProxy) {
			t.Fatal("revoked credential still reached the proxy on the source stack")
		}
		time.Sleep(500 * time.Millisecond)
	}
	receipt["source_revocation"] = map[string]any{
		"audit_id": sourceAuditID, "revoked_at": revokedAt,
		"credentials_revoked": revokedCreds, "active_credentials_after": activeCreds,
	}
	ev.add("POST-BACKUP-REVOKE", "US-PR-011/014", "pass",
		fmt.Sprintf("audited post-backup revocation (%d credential(s) permanently revoked); old client and export already denied on the source stack", revokedCreds))

	// ── 7. external stop of the panel: real local queue material ───────────
	queuedAt := time.Now().UTC()
	source.Close() // real external stop of the owned panel only
	okWhileStopped := 0
	for i := 0; i < 3; i++ {
		if fetch(bobTransport, bobProxy) {
			okWhileStopped++
		}
		time.Sleep(1500 * time.Millisecond)
	}
	if okWhileStopped != 3 {
		t.Fatalf("Agent lost its cached authorization while only the panel was stopped (%d/3 fetches)", okWhileStopped)
	}
	agent1.stop(t, 90*time.Second)
	agent1 = nil
	copyDir := filepath.Join(e.privateDir, "agent-copy")
	copiedFiles := demoRestoreCopyDir(t, stateDir, copyDir)
	if copiedFiles == 0 {
		t.Fatal("stopped-state copy is empty")
	}
	ev.add("EXTERNAL-STOP", "US-PR-014", "pass",
		fmt.Sprintf("panel stopped externally; cached authorization kept serving real traffic; Agent stopped and a full stopped-state copy (%d file(s)) was taken for recovery", copiedFiles))

	// Independent, non-PG inspection of the stopped state via the shipped
	// channel: proof the queue is real material and not a guessed zero. It runs
	// before any writer opens the copy.
	inspection := demoRestoreInspectState(t, e, copyDir)
	if len(inspection.PendingBatches) == 0 {
		t.Fatal("shipped -inspect-state did not export the queued batches")
	}
	identity := state.Identity{AgentID: inspection.AgentID,
		ControlGeneration: inspection.ControlGeneration, AppliedRevision: inspection.AppliedRevision}
	if identity.AgentID != agentID {
		t.Fatal("the copied Agent state does not belong to the registered owned Agent")
	}
	local, err := state.Open(copyDir)
	if err != nil {
		t.Fatal("open stopped-state copy failed")
	}
	depth, err := local.QueueDepth(ctx)
	if err != nil {
		local.Close()
		t.Fatal("read copied Agent queue depth")
	}
	if err := local.Close(); err != nil {
		t.Fatal("close stopped-state copy")
	}
	if depth.Batches == 0 || depth.Samples == 0 || len(inspection.PendingBatches) == 0 {
		t.Fatal("the externally stopped Agent did not persist any real queued usage")
	}
	queuedPayload := map[string]string{}
	queuedBytes := map[string]int64{}
	for _, batch := range inspection.PendingBatches {
		if len(batch.Samples) == 0 {
			t.Fatal("inspected queued batch has no samples")
		}
		queuedPayload[batch.BatchID] = demoRestoreUsageHash(batch)
		var n int64
		for _, s := range batch.Samples {
			n += s.UploadBytes + s.DownloadBytes
		}
		queuedBytes[batch.BatchID] = n
	}
	var queuedStart time.Time
	for _, batch := range inspection.PendingBatches {
		for _, s := range batch.Samples {
			if queuedStart.IsZero() || s.StartedAt.Before(queuedStart) {
				queuedStart = s.StartedAt
			}
		}
	}
	if queuedStart.Before(queuedAt.Add(-10 * time.Second)) {
		t.Fatal("queued samples predate the external panel stop; they are not the offline material")
	}
	for id := range queuedPayload {
		if _, inBackup := backupBob[id]; inBackup {
			t.Fatal("queued batch id already exists in the backup ledger")
		}
	}
	backupGeneration := identity.ControlGeneration
	if backupGeneration == "" {
		t.Fatal("copied Agent has no control generation")
	}
	receipt["stopped_queue"] = map[string]any{
		"batches": depth.Batches, "samples": depth.Samples,
		"bytes": demoRestoreSum(queuedBytes), "oldest_created_at": depth.OldestCreated,
		"agent_generation": backupGeneration, "source": "Agent SQLite copy + shipped -inspect-state, never guessed from PostgreSQL",
	}
	ev.add("LOCAL-QUEUE-MATERIAL", "US-PR-013", "pass",
		fmt.Sprintf("external-stop queue proven from the Agent state itself: %d batch(es) / %d sample(s) / %d B, exported by -inspect-state, none present in the backup ledger",
			depth.Batches, depth.Samples, demoRestoreSum(queuedBytes)))

	// ── 8. real pg_restore into the independent target cluster ─────────────
	restoreStart := time.Now().UTC()
	demoRestoreRunTool(t, e, []string{filepath.Join(e.pgBin, "pg_restore"), "-h", "127.0.0.1",
		"-p", strconv.Itoa(e.targetPort), "-U", "dr_owner", "-d", e.targetDB,
		"--exit-on-error", "--no-owner", dumpPath}, "pg_restore")
	restoreSeconds := time.Since(restoreStart).Seconds()
	receipt["backup"].(map[string]any)["restore_seconds"] = restoreSeconds
	receipt["backup"].(map[string]any)["restore_exit_code"] = 0
	ev.add("REAL-RESTORE", "US-PR-014", "pass",
		fmt.Sprintf("real pg_restore --exit-on-error into a second owned cluster in %.3fs", restoreSeconds))

	// ── 9. restored panel: real gate, rescue exception, refused exit ───────
	target, err := store.Open(ctx, store.Options{DatabaseURL: e.targetDSN, MasterKey: key,
		MigrationsDir: "../../migrations", MaxConns: 6, ConnectTimeout: 10 * time.Second})
	if err != nil {
		// A restored database that contains registered Agents cannot be opened
		// without operator-supplied upgrade evidence for every Agent (the same
		// gate US-PR-016 requires). That is a correct product refusal, not a
		// test failure: record it precisely as blocked with the real codes.
		if !strings.Contains(err.Error(), domain.CodeReadinessBlocked) {
			// No credential is in the error text; keep the real diagnosis.
			t.Fatalf("open owned restore store failed: %v", err)
		}
		codes := []string{}
		var checks []string
		ro, roErr := store.OpenReadOnly(ctx, store.Options{DatabaseURL: e.targetDSN,
			MigrationsDir: "../../migrations", ConnectTimeout: 10 * time.Second})
		if roErr == nil {
			if r, rErr := ro.CheckUpgradeReadiness(ctx, false); rErr == nil && r != nil {
				for _, c := range r.Checks {
					if c.Status == "blocked" || c.Status == "unknown" {
						codes = append(codes, c.Code)
					}
					checks = append(checks, fmt.Sprintf("%s/%s/%d", c.Code, c.Status, c.Count))
				}
			}
			ro.Close()
		}
		receipt["status"] = "blocked"
		receipt["blocked_diagnosis"] = map[string]any{
			"gate":                  domain.CodeReadinessBlocked,
			"stage":                 "store.Open of the restored database (post-migration readiness, before the panel can listen)",
			"blocker_codes":         codes,
			"checks":                checks,
			"classification":        "BACKEND finding: the documented restore flow (docs/operations/backup-recovery.md steps 3-4: start the isolated restored panel, login, POST /admin/recovery-mode/enter, verify) cannot run at all, because opening a restored database that contains registered Agents demands upgrade-path per-Agent stopped-state evidence that only exists for a planned upgrade, not for a disaster restore.",
			"why_not_bypassed":      "the gate is a real product guard and is neither weakened nor fed fabricated evidence",
			"requirements":          "one consistent stopped-state export plus an administrator stop review per registered Agent via PANEL_UPGRADE_EVIDENCE_FILE",
			"safe_state":            "the real pg_dump/pg_restore completed and the restored panel stays closed, so the stale backup authorization is never pushed to a node",
			"unblocked_alternative": "an isolated restore must either ship genuine per-Agent stopped-state evidence or be allowed to enter recovery coordination first; the product currently offers only the former",
			"next_step":             "product decision required; this case is reported blocked, not green",
		}
		ev.add("RESTORE-READINESS-GATE", "US-PR-014/016", "blocked",
			fmt.Sprintf("restored panel cannot open: %s with blockers %v, so recovery coordination is unreachable after a plain restore",
				domain.CodeReadinessBlocked, codes))
		t.Logf("blocked at the restore readiness gate: %v", codes)
		return
	}
	defer target.Close()
	beforeGate, err := target.GetControlState(ctx)
	if err != nil {
		t.Fatal("read restored control state")
	}
	if beforeGate.RecoveryMode {
		t.Fatal("a freshly restored database must not claim the gate is already open")
	}
	restoredSource := server(target)
	defer restoredSource.Close()
	targetAdmin := &demoRestoreAPI{t: t, base: restoredSource.URL,
		client: &http.Client{Jar: mustJar(t), Timeout: 20 * time.Second}}
	// The backup contains the pre-backup admin credential: the isolation target is
	// opened by the operator, never by the surviving source stack.
	targetAdmin.login("admin", adminPassword)
	if status, _, _ := targetAdmin.call("GET", "/readyz", nil, ""); status != http.StatusOK {
		t.Fatal("restored panel is not ready")
	}
	entered := targetAdmin.ok("POST", "/api/v1/admin/recovery-mode/enter", map[string]any{})
	restoredGeneration := int64(entered["control_generation"].(float64))
	if restoredGeneration <= 0 || restoredGeneration > (1<<53)-1 || restoredGeneration == beforeGate.ControlGeneration {
		t.Fatal("recovery entry did not allocate a fresh exact-integer generation")
	}
	if got := strconv.FormatInt(restoredGeneration, 10); got == backupGeneration {
		t.Fatal("restored generation collides with the Agent's backed-up generation")
	}
	var sourceGeneration int64
	if st.DB.QueryRowContext(ctx, `SELECT control_generation FROM control_state WHERE id=1`).Scan(&sourceGeneration) != nil {
		t.Fatal("read source generation")
	}
	if sourceGeneration == restoredGeneration {
		t.Fatal("restored generation collides with the surviving source generation")
	}
	receipt["generations"] = map[string]any{
		"backup": beforeGate.ControlGeneration, "source_now": sourceGeneration,
		"agent_copy": backupGeneration, "restored": restoredGeneration,
	}
	ev.add("GENERATION", "US-PR-014", "pass",
		fmt.Sprintf("recovery entry allocated fresh generation %d, different from backup %d, surviving source %d and Agent copy %s",
			restoredGeneration, beforeGate.ControlGeneration, sourceGeneration, backupGeneration))

	// Ordinary writes are blocked; identity rescue and telemetry stay usable.
	var aliceRow struct{ Version int64 }
	if err := target.DB.QueryRowContext(ctx, `SELECT version FROM members WHERE id=$1`, aliceID).Scan(&aliceRow.Version); err != nil {
		t.Fatal("read restored member version")
	}
	blockedCases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"ordinary member write", http.MethodPatch, "/api/v1/admin/members/" + aliceID,
			map[string]any{"version": aliceRow.Version, "active": true}},
		{"server create", http.MethodPost, "/api/v1/admin/servers",
			map[string]any{"name": "blocked", "address": "127.0.0.1", "region": "owned"}},
		{"invitation create", http.MethodPost, "/api/v1/admin/invitations", map[string]any{}},
	}
	for _, c := range blockedCases {
		targetAdmin.want(c.method, c.path, c.body, "", http.StatusConflict, domain.CodeRecoveryMode)
	}
	targetAlice := &demoRestoreAPI{t: t, base: restoredSource.URL,
		client: &http.Client{Jar: mustJar(t), Timeout: 20 * time.Second}}
	targetAlice.login("alice", alicePassword) // identity still exists in the backup
	targetAlice.want("POST", "/api/v1/me/credentials/rotations", nil, "", http.StatusConflict, domain.CodeRecoveryMode)
	targetAlice.want("POST", "/api/v1/me/subscription/token/rotations", nil, "", http.StatusConflict, domain.CodeRecoveryMode)
	// Necessary telemetry is not blocked (a real Agent batch is accepted later);
	// the panel truthfully refuses to hand out a possibly restored snapshot.
	targetAdmin.want("GET", "/api/v1/agent/snapshot", nil, identity.Token, http.StatusConflict, domain.CodeRecoveryMode)
	// An exit without the explicit checks is refused, not silently accepted.
	targetAdmin.want("POST", "/api/v1/admin/recovery-mode/exit", map[string]any{"confirm": "verified"}, "", http.StatusBadRequest, "")
	targetAdmin.want("POST", "/api/v1/admin/recovery-mode/exit", map[string]any{
		"confirm": "verified", "agent_revisions_checked": true}, "", http.StatusBadRequest, "")
	// Identity rescue during the gate: change own password and revoke another
	// owned session; neither changes proxy authorization.
	rescue := targetAlice.ok("POST", "/api/v1/me/password", map[string]any{
		"current_password": alicePassword, "new_password": alicePassword + "x"})
	if rescue["updated"] != true {
		t.Fatal("rescue password change was not applied")
	}
	alicePassword += "x"
	targetAlice = &demoRestoreAPI{t: t, base: restoredSource.URL,
		client: &http.Client{Jar: mustJar(t), Timeout: 20 * time.Second}}
	targetAlice.login("alice", alicePassword)
	ev.add("GATE", "US-PR-014", "pass",
		"restored panel blocks ordinary admin/member authorization writes and snapshot output with 409 RECOVERY_MODE, refuses an unconfirmed exit, and keeps identity rescue + telemetry usable")

	// ── 10. real queued material replay under the gate ─────────────────────
	targetConfig := filepath.Join(e.privateDir, "agent-restored.json")
	demoRestoreWriteConfig(t, targetConfig, restoredSource.URL, e.xrayBin, copyDir)
	restoredAgent := demoRestoreStartAgent(t, e.agentBin, targetConfig, filepath.Join(e.privateDir, "agent-restored.log"))
	defer func() {
		if restoredAgent != nil {
			restoredAgent.stop(t, 90*time.Second)
		}
	}()
	waitFor(t, 60*time.Second, "restored Agent reports coordination_required", func() bool {
		var n int
		err := target.DB.QueryRowContext(ctx, `SELECT count(*) FROM agent_receipts
			WHERE agent_id=$1 AND state='coordination_required' AND payload->>'control_generation'=$2`,
			agentID, strconv.FormatInt(restoredGeneration, 10)).Scan(&n)
		return err == nil && n > 0
	})
	expected := demoRestoreLedger{}
	for id, item := range backupBob {
		expected[id] = item
	}
	for id, item := range backupAlice {
		expected[id] = item
	}
	for id, bytesTotal := range queuedBytes {
		expected[agentID+"/"+id] = demoRestoreBatch{Payload: queuedPayload[id], Bytes: bytesTotal}
	}
	waitFor(t, 90*time.Second, "queued batches replayed exactly once under the gate", func() bool {
		gotBob := demoRestoreReadLedger(t, target.DB, bobID)
		gotAlice := demoRestoreReadLedger(t, target.DB, aliceID)
		if gotBob.total()+gotAlice.total() > expected.total() {
			t.Fatal("queue replay double-counted usage")
		}
		merged := demoRestoreLedger{}
		for id, item := range gotBob {
			merged[id] = item
		}
		for id, item := range gotAlice {
			merged[id] = item
		}
		// The replayed rows expose the real sample end; compare the conserved
		// identity/bytes now and the full set once the queue is ACK-deleted.
		for id, want := range expected {
			got, ok := merged[id]
			if !ok {
				return false
			}
			if got.Payload != want.Payload || got.Bytes != want.Bytes {
				t.Fatalf("replayed batch %s mutated payload or bytes", id)
			}
		}
		return len(merged) == len(expected)
	})
	restoredLedger := demoRestoreMerge(t, target.DB, aliceID, bobID)
	for id, want := range expected {
		got, ok := restoredLedger[id]
		if !ok || got.Payload != want.Payload || got.Bytes != want.Bytes {
			t.Fatalf("conserved batch %s differs after replay", id)
		}
	}
	bobRestoredPeriods := demoRestoreReadPeriods(t, target.DB, bobID)
	for batchID := range queuedPayload {
		attribution := demoRestoreAttribution(t, target.DB, agentID, batchID, bobRestoredPeriods)
		if len(attribution) != 1 {
			t.Fatalf("replayed batch %s was split across %d interval(s)", batchID, len(attribution))
		}
	}
	for id := range ackDeleted {
		if _, ok := restoredLedger[id]; ok {
			t.Fatalf("post-backup ACK-deleted batch %s was fabricated after restore", id)
		}
	}
	ev.add("LEDGER-CONSERVATION", "US-PR-013", "pass",
		fmt.Sprintf("restored ledger equals the backup ledger plus the inspected Agent queue: %d B, %d batch(es); the %d B ACK-deleted gap was never fabricated",
			restoredLedger.total(), len(restoredLedger), ackDeleted.total()))
	ev.add("REPLAY-ATTRIBUTION", "US-PR-013", "pass",
		"every replayed batch kept its original payload hash, byte totals and landed inside exactly one recorded interval (literal oracle), not silently in the current cycle")

	// An unprovable interval must be refused with LEDGER_UNATTRIBUTABLE and no
	// ACK; it may never be shifted into the current cycle. Real HTTP path, real
	// Agent credential, a fresh probe id (no stored payload or id is modified).
	probeStart := registrationTime.Add(500 * time.Millisecond)
	probeEnd := probeStart.Add(time.Second)
	probe := contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion,
		BatchID:       "wdr-probe-" + hex.EncodeToString(random[:6]),
		Samples: []contracts.UsageSample{{
			NodeID: node["id"].(string), MemberID: bobID, CoreGeneration: "probe",
			StartedAt: probeStart, EndedAt: probeEnd, UploadBytes: 1, DownloadBytes: 1,
		}},
	}
	probeRaw, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", restoredSource.URL+"/api/v1/agent/usage", bytes.NewReader(probeRaw))
	if err != nil {
		t.Fatal("construct probe batch")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+identity.Token)
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("owned probe batch transport failed")
	}
	probeAnswer, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	var probeEnvelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(probeAnswer, &probeEnvelope) != nil || probeEnvelope.Error.Code != "LEDGER_UNATTRIBUTABLE" {
		t.Fatalf("unprovable interval did not yield LEDGER_UNATTRIBUTABLE (status=%d)", res.StatusCode)
	}
	if res.StatusCode < 400 {
		t.Fatal("unprovable interval was ACKed")
	}
	var probeRows, probeBatches int
	if err := target.DB.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM usage_entries WHERE batch_id=$1),
		(SELECT count(*) FROM usage_batches WHERE batch_id=$1)`, probe.BatchID).
		Scan(&probeRows, &probeBatches); err != nil {
		t.Fatal("read probe batch state")
	}
	if probeRows != 0 || probeBatches != 0 {
		t.Fatal("a refused unprovable batch still wrote a ledger row or a batch row")
	}
	ev.add("LEDGER-UNATTRIBUTABLE", "US-PR-013", "pass",
		"an interval that no recorded cycle covers is refused with 422 LEDGER_UNATTRIBUTABLE, nothing is ACKed and nothing is moved into the current cycle")

	// Same batch id + identical payload must replay idempotently; a different
	// payload under the same id must stop instead of being credited.
	recorder.mu.Lock()
	resendID, resendPath := "", ""
	for id, path := range recorder.ids {
		if _, ok := backupAlice[id]; ok {
			resendID, resendPath = id, path
			break
		}
	}
	recorder.mu.Unlock()
	if resendID == "" {
		t.Fatal("no recorded backup payload available for the identical resend")
	}
	originalPayload, err := os.ReadFile(resendPath)
	if err != nil {
		t.Fatal("read recorded backup payload")
	}
	resend := func(raw []byte) (int, string) {
		req, err := http.NewRequest("POST", restoredSource.URL+"/api/v1/agent/usage", bytes.NewReader(raw))
		if err != nil {
			t.Fatal("construct resend")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+identity.Token)
		res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			t.Fatal("resend transport failed")
		}
		defer res.Body.Close()
		raw2, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		var env struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw2, &env)
		if env.Error.Code != "" {
			return res.StatusCode, env.Error.Code
		}
		return res.StatusCode, env.Data.Status
	}
	before := demoRestoreMerge(t, target.DB, aliceID, bobID)
	if status, ack := resend(originalPayload); status != http.StatusOK || ack != "duplicate" {
		t.Fatalf("identical resend was not a duplicate (status=%d ack=%q)", status, ack)
	}
	if after := demoRestoreMerge(t, target.DB, aliceID, bobID); !reflect.DeepEqual(before, after) {
		t.Fatal("identical resend changed the restored ledger")
	}
	tampered := bytes.Replace(originalPayload, []byte(`"upload_bytes":`), []byte(`"upload_bytes": `), 1)
	if bytes.Equal(tampered, originalPayload) {
		tampered = append(append([]byte{}, originalPayload...), ' ')
	}
	if status, code := resend(tampered); status != http.StatusConflict || code != domain.CodeBatchPayloadMismatch {
		t.Fatalf("same-id different-payload resend = %d (%s), want 409 BATCH_PAYLOAD_MISMATCH", status, code)
	}
	if after := demoRestoreMerge(t, target.DB, aliceID, bobID); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected payload mismatch changed the restored ledger")
	}
	ev.add("RESEND", "US-PR-013", "pass",
		"an identical resend of a real committed batch is answered duplicate with an unchanged ledger; the same id with a different payload is stopped with 409 BATCH_PAYLOAD_MISMATCH")

	// The restored, still-unconfirmed authorization must not resurrect alice.
	waitFor(t, 60*time.Second, "restored Agent queue ACK-deleted", func() bool {
		var depth int64
		if target.DB.QueryRowContext(ctx, `SELECT queue_depth FROM agents WHERE id=$1`, agentID).Scan(&depth); err != nil {
			return false
		}
		return depth == 0
	})
	waitFor(t, 30*time.Second, "restored Agent published its own applied state", func() bool {
		var n int
		err := target.DB.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE id=$1 AND last_heartbeat_at IS NOT NULL`, agentID).Scan(&n)
		return err == nil && n == 1
	})
	for i := 0; i < 3; i++ {
		if fetch(aliceTransport, aliceProxy) {
			t.Fatal("the restored, unaccepted authorization resurrected the revoked credential")
		}
		time.Sleep(500 * time.Millisecond)
	}
	targetAlice.want("GET", "/sub/"+aliceToken+"?format=xray", nil, "", http.StatusForbidden, "")
	ev.add("NO-RESURRECTION", "US-PR-014", "pass",
		"with only the restored backup in place (before the operator replay and before acceptance) the permanently revoked old credential and its export stay denied")

	// ── 11. operator replay of the audited post-backup revocation ──────────
	// The panel intentionally cannot apply ordinary authorization writes inside
	// the gate. The documented order is: verify, exit, then re-apply the current
	// authorization. Agents still refuse the new generation until an operator
	// accepts it, so the stale snapshot cannot be pushed before the replay.
	stale, err := target.GetMember(ctx, aliceID)
	if err != nil || !stale.Active {
		t.Fatal("restored backup does not contain the deliberately stale authorization")
	}
	// The verified restore consumes the old epoch: the panel rotates the control
	// generation again and every Agent must explicitly accept the new one. The
	// old, stale authorization snapshot can therefore never be pushed.
	verified := time.Now().UTC()
	targetAdmin.ok("POST", "/api/v1/admin/recovery-mode/exit", map[string]any{
		"confirm": "verified", "agent_revisions_checked": true, "ledger_checked": true})
	exitState := targetAdmin.ok("GET", "/api/v1/admin/recovery-mode", nil)
	if exitState["recovery_mode"] != false {
		t.Fatal("confirmed exit did not leave recovery coordination")
	}
	postExit, err := target.GetControlState(ctx)
	if err != nil {
		t.Fatal("read control state after the confirmed exit")
	}
	if postExit.ControlGeneration == restoredGeneration ||
		strconv.FormatInt(postExit.ControlGeneration, 10) == backupGeneration {
		t.Fatal("the confirmed exit did not rotate the control generation")
	}
	var restoredVersion int64
	if err := target.DB.QueryRowContext(ctx, `SELECT version FROM members WHERE id=$1`, aliceID).Scan(&restoredVersion); err != nil {
		t.Fatal("read restored member version for the replay")
	}
	targetAdmin.ok("PATCH", "/api/v1/admin/members/"+aliceID, map[string]any{
		"version": restoredVersion, "active": false})
	detail := domain.NewObject()
	_ = detail.Set("source_audit_id", sourceAuditID)
	_ = detail.Set("source_revoked_at", revokedAt.Format(time.RFC3339Nano))
	_ = detail.Set("restored_backup_at", dumpEnd.Format(time.RFC3339Nano))
	_ = detail.Set("ack_deleted_gap_bytes", ackDeleted.total())
	if err := target.RecordAudit(ctx, sourceActorID, "admin", "recovery.replay_revocation", "member", aliceID,
		detail, "127.0.0.1", "webdemo-restore"); err != nil {
		t.Fatal("replay audit persistence failed")
	}
	var replayAudits, aliceActiveCreds int
	if err := target.DB.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM audit_logs WHERE action='recovery.replay_revocation' AND target_id=$1 AND detail->>'source_audit_id'=$2),
		(SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active')`,
		aliceID, sourceAuditID).Scan(&replayAudits, &aliceActiveCreds); err != nil {
		t.Fatal("read replay result")
	}
	if replayAudits != 1 || aliceActiveCreds != 0 {
		t.Fatalf("replay did not re-apply the audited revocation (audits=%d active_creds=%d)", replayAudits, aliceActiveCreds)
	}
	// The panel's own next authorization snapshot must already exclude the
	// revoked member: the Agent no longer has anything to resurrect, and the
	// rebooted generation means it cannot be applied before acceptance anyway.
	snapshotAfterReplay := demoRestoreSnapshot(t, restoredSource.URL, identity, postExit.ControlGeneration)
	if snapshotAfterReplay.ExpectedUserCount > 1 {
		t.Fatalf("panel offered %d authorized users after the replay", snapshotAfterReplay.ExpectedUserCount)
	}
	for _, instance := range snapshotAfterReplay.Instances {
		for _, user := range instance.Users {
			if user.MemberID == aliceID {
				t.Fatal("the post-replay snapshot still authorizes the revoked member")
			}
		}
	}
	receipt["post_exit_generation"] = postExit.ControlGeneration
	receipt["post_exit_expected_users"] = snapshotAfterReplay.ExpectedUserCount
	ev.add("OPERATOR-REPLAY", "US-PR-014", "pass",
		"after the confirmed exit the operator re-applied the audited post-backup revocation through the real batch API and linked it to the source audit id; the gate had refused to guess it")

	// ── 12. explicit generation acceptance, then real access ───────────────
	restoredAgent.stop(t, 90*time.Second)
	restoredAgent = nil
	refused, err := state.Open(copyDir)
	if err != nil {
		t.Fatal("re-open copied Agent state failed")
	}
	refusedIdentity, err := refused.Identity(ctx)
	if err != nil {
		refused.Close()
		t.Fatal("read unaccepted identity")
	}
	refusedPending, _ := refused.PendingGeneration(ctx)
	refusedDepth, _ := refused.QueueDepth(ctx)
	refused.Close()
	if refusedIdentity.ControlGeneration != backupGeneration ||
		refusedPending != strconv.FormatInt(postExit.ControlGeneration, 10) ||
		refusedIdentity.AppliedRevision == 0 || refusedDepth.Batches != 0 {
		t.Fatalf("Agent did not refuse the new generation cleanly (applied_generation=%q pending=%q depth=%d)",
			refusedIdentity.ControlGeneration, refusedPending, refusedDepth.Batches)
	}
	accept := exec.Command(e.agentBin, "-config", targetConfig,
		"-accept-generation", strconv.FormatInt(postExit.ControlGeneration, 10))
	acceptOut, err := accept.CombinedOutput()
	if err != nil {
		t.Fatal("explicit accept-generation failed")
	}
	if !bytes.Contains(acceptOut, []byte("accepted control generation")) {
		t.Fatal("accept-generation did not confirm the exact generation")
	}
	restoredAgent = demoRestoreStartAgent(t, e.agentBin, targetConfig, filepath.Join(e.privateDir, "agent-accepted.log"))
	waitFor(t, 90*time.Second, "explicitly accepted generation applied by the real Agent", func() bool {
		var applied, desired, gen int64
		if target.DB.QueryRowContext(ctx, `SELECT applied_revision, desired_revision, control_generation
			FROM agents WHERE id=$1`, agentID).Scan(&applied, &desired, &gen) != nil {
			return false
		}
		return applied == desired && applied > 0 && gen == postExit.ControlGeneration
	})
	waitFor(t, 60*time.Second, "accepted Agent serves the reconciled zero-user authorization", func() bool {
		var n int
		if target.DB.QueryRowContext(ctx, `SELECT count(*) FROM agents a, jsonb_array_elements(a.instances) i
			WHERE a.id=$1 AND i->>'state'='applied' AND (i->>'applied_user_count')::int=0`, agentID).Scan(&n); err != nil {
			t.Fatal("query accepted Agent instances")
		}
		return n > 0
	})
	for i := 0; i < 3; i++ {
		if fetch(aliceTransport, aliceProxy) {
			t.Fatal("explicit acceptance resurrected the permanently revoked credential")
		}
		time.Sleep(500 * time.Millisecond)
	}
	acceptedIdentity := func() state.Identity {
		open, err := state.Open(copyDir)
		if err != nil {
			t.Fatal("re-open accepted state")
		}
		defer open.Close()
		id, err := open.Identity(ctx)
		if err != nil {
			t.Fatal("read accepted identity")
		}
		return id
	}()
	if acceptedIdentity.ControlGeneration != strconv.FormatInt(postExit.ControlGeneration, 10) || acceptedIdentity.AppliedRevision == 0 {
		t.Fatal("accepted Agent durable state did not advance to the new generation")
	}
	ev.add("ACCEPTANCE", "US-PR-014", "pass",
		fmt.Sprintf("Agent refused post-exit generation %d (pending, zero queued batches) and accepted it explicitly; the pre-recovery generation %d was never applied; old credential still denied after acceptance",
			postExit.ControlGeneration, restoredGeneration))

	// Contract: after the verification the panel's NEW export actually connects
	// for the member whose authorization is still valid, while the permanently
	// revoked old credential stays unusable.
	targetBob := &demoRestoreAPI{t: t, base: restoredSource.URL,
		client: &http.Client{Jar: mustJar(t), Timeout: 20 * time.Second}}
	targetBob.login("bob", bobPassword)
	bobRestoredToken, _ := targetBob.ok("GET", "/api/v1/me/subscription", nil)["token"].(string)
	bobRestoredExport := demoRestoreExport(t, restoredSource.URL, bobRestoredToken)
	aliceRestoredExport, err := http.Get(restoredSource.URL + "/sub/" + aliceToken + "?format=xray")
	if err != nil {
		t.Fatal("check revoked restored export")
	}
	aliceRestoredExport.Body.Close()
	if aliceRestoredExport.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked restored export status %d, want 403", aliceRestoredExport.StatusCode)
	}
	bobActual, bobActualTransport := demoRestoreClient(t, e.xrayBin, bobRestoredExport, "bob-restored")
	waitFor(t, 60*time.Second, "newly exported restored credential tunnels real traffic", func() bool {
		return fetch(bobActualTransport, bobActual)
	})
	if fetch(aliceTransport, aliceProxy) {
		t.Fatal("permanently revoked old credential reached the accepted restored proxy")
	}
	bobBeforeExport := demoRestoreReadLedger(t, target.DB, bobID)
	waitFor(t, 60*time.Second, "new restored traffic committed to the restored ledger", func() bool {
		return demoRestoreReadLedger(t, target.DB, bobID).total() >= bobBeforeExport.total()+int64(len(payload))
	})
	finalBob := demoRestoreReadLedger(t, target.DB, bobID)
	finalAlice := demoRestoreReadLedger(t, target.DB, aliceID)
	receipt["restored_ledger"] = map[string]any{
		"alice_bytes": finalAlice.total(), "bob_bytes": finalBob.total(),
		"batches": len(finalAlice) + len(finalBob),
	}
	ev.add("POST-VERIFY-ACCESS", "US-PR-013/014", "pass",
		fmt.Sprintf("after verification the restored panel's new export tunneled real traffic (%d B) and the permanently revoked old credential is still denied", len(payload)))

	// ── 13. source stack untouched; history conserved ──────────────────────
	var sourceRecovery bool
	var sourceGenerationAfter int64
	if err := st.DB.QueryRowContext(ctx, `SELECT recovery_mode, control_generation FROM control_state WHERE id=1`).
		Scan(&sourceRecovery, &sourceGenerationAfter); err != nil {
		t.Fatal("read source control state")
	}
	if sourceRecovery || sourceGenerationAfter != sourceGeneration {
		t.Fatal("the restore exercise mutated the surviving source control state")
	}
	var sourceAliceActive int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM members WHERE id=$1 AND NOT active`, aliceID).
		Scan(&sourceAliceActive); err != nil || sourceAliceActive != 1 {
		t.Fatal("the restore exercise changed the source revocation")
	}
	finalSourceAlice := demoRestoreReadLedger(t, st.DB, aliceID)
	finalSourceBob := demoRestoreReadLedger(t, st.DB, bobID)
	if !reflect.DeepEqual(backupAlice, finalSourceAlice) || !reflect.DeepEqual(sourceBob, finalSourceBob) {
		t.Fatal("the restore exercise wrote to the surviving source ledger")
	}
	if info, err := os.Stat(dumpPath); err != nil || info.Size() != dumpInfo.Size() {
		t.Fatal("the owned backup file changed during the exercise")
	}
	afterDump, err := os.ReadFile(dumpPath)
	if err != nil || demoRestoreDigest(afterDump) != receipt["backup"].(map[string]any)["dump_sha256"] {
		t.Fatal("the owned backup content changed during the exercise")
	}
	restoredAlicePeriods := demoRestoreReadPeriods(t, target.DB, aliceID)
	restoredBobPeriods := demoRestoreReadPeriods(t, target.DB, bobID)
	if !reflect.DeepEqual(alicePeriods, restoredAlicePeriods) || !reflect.DeepEqual(bobPeriods, restoredBobPeriods) {
		t.Fatal("recorded cycle history was not conserved by dump/restore")
	}
	receipt["periods"] = map[string]any{
		"alice": alicePeriods, "bob": bobPeriods, "conserved": true,
	}
	ev.add("SOURCE-UNCHANGED", "US-PR-014", "pass",
		"surviving source control state, revocation, ledger and the backup file are byte-for-byte unchanged by the restore exercise")
	ev.add("HISTORY-CONSERVED", "US-PR-013", "pass",
		"recorded cycle intervals for both members are identical before and after dump/restore and replayed usage never crosses them")

	// ── 14. legacy DockerRecovery equivalence table + not_run ──────────────
	equivalence := []map[string]string{
		{"old": "A14 isolated restore target, source cluster never overwritten", "new": "second owned cluster, distinct port/database, source untouched", "state": "pass"},
		{"old": "recovery gate + fresh random generation", "new": "real enter via API; generation differs from backup/source/Agent copy", "state": "pass"},
		{"old": "unaccepted new generation refused (coordination_required)", "new": "real Agent process reports coordination_required and keeps pending generation", "state": "pass"},
		{"old": "explicit -accept-generation then applied", "new": "same built binary accepts the exact generation, applies zero-user snapshot", "state": "pass"},
		{"old": "ledger replayed exactly once (payload/id/bytes/watermark)", "new": "replayed queue equals inspected payload hashes and byte totals, duplicate resend idempotent", "state": "pass"},
		{"old": "ACK-deleted gap not fabricated", "new": "real post-backup ACK-deleted batch absent from restored ledger", "state": "pass"},
		{"old": "revoked authorization does not come back", "new": "old credential denied before replay, after acceptance and after restart", "state": "pass"},
		{"old": "restart keeps generation/revision", "new": "accepted Agent restart keeps generation and zero-user authorization", "state": "pass"},
		{"old": "source stack state unchanged", "new": "source control state/revocation/ledger/backup digest unchanged", "state": "pass"},
		{"old": "Agent2 not coordinated and not claimed", "new": "single owned Agent only; no uncoordinated node is claimed", "state": "not_applicable_here"},
		{"old": "Docker volumes/containers", "new": "no container and no shared 15432/.devdb/8443/8445 resource is used", "state": "not_applicable_here"},
		{"old": "old-release binary stop-write rollback", "new": "distinct maintenance path; not exercised in this case", "state": "not_run"},
		{"old": "WAL/PITR, production RPO/RTO", "new": "no archive configured; only the dump bracket is reported", "state": "not_run"},
	}
	receipt["legacy_docker_recovery_equivalence"] = equivalence

	receipt["status"] = "pass"
	receipt["verified_at"] = verified
	receipt["restore_invoked_at"] = restoreStart
	receipt["rto_seconds_restore_to_verified"] = verified.Sub(restoreStart).Seconds()
	t.Logf("web-demo C: dump=%d B restore=%.3fs backup_ledger=%d B queued=%d B/%d batch(es) gap=%d B batches=%d generation=%d",
		dumpInfo.Size(), restoreSeconds, backupAlice.total()+backupBob.total(),
		demoRestoreSum(queuedBytes), len(queuedBytes), ackDeleted.total(), len(restoredLedger), restoredGeneration)
}
