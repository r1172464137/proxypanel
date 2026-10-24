package e2e

// Physical-fault fixtures. These require the owned runner, never a shared DSN.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"proxypanel/internal/agent/control"
	"proxypanel/internal/agent/reporting"
	"proxypanel/internal/agent/state"
	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/api"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

func faultCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func faultPanel(t *testing.T, redisURL string) (*dataPlaneAPI, *storeFault) {
	t.Helper()
	dsn := os.Getenv("PANEL_TEST_DATABASE_URL")
	cfg, err := pgx.ParseConfig(dsn)
	faultCheck(t, err)
	if cfg.Host != "127.0.0.1" || !strings.HasPrefix(cfg.Database, "dp_owned_") || os.Getenv("PP_FAULT_OWNER") == "" {
		t.Fatal("owned runner required")
	}
	ctx := context.Background()
	key, err := secrets.ParseKey(strings.Repeat("fault-only-key-", 5))
	faultCheck(t, err)
	db, err := pgx.Connect(ctx, dsn)
	faultCheck(t, err)
	schema := fmt.Sprintf("fault_%d", time.Now().UnixNano())
	_, err = db.Exec(ctx, `CREATE SCHEMA `+schema)
	faultCheck(t, err)
	db.Close(ctx)
	st, err := store.Open(ctx, store.Options{DatabaseURL: dsn, MasterKey: key, MigrationsDir: "../../migrations", SearchPath: schema})
	faultCheck(t, err)
	t.Cleanup(st.Close)
	hash, err := secrets.HashPassword("Fault-only-password-123")
	faultCheck(t, err)
	_, err = st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "admin", PasswordHash: hash, Role: "admin", Active: true, ProxyEnabled: true})
	faultCheck(t, err)
	limiter, err := limit.New(redisURL)
	faultCheck(t, err)
	t.Cleanup(func() { limiter.Close() })
	panel := &api.Server{Store: st, RateLimiter: limiter, Cfg: api.Config{Dev: true, RedisDSN: redisURL, SessionTTL: time.Hour}}
	server := httptest.NewServer(panel.Handler())
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	a := &dataPlaneAPI{t: t, base: server.URL, client: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
	a.login("admin", "Fault-only-password-123")
	return a, &storeFault{st.DB}
}

type storeFault struct{ db *sql.DB }

// This observer only records real HTTP; it never fabricates responses.
type faultWire struct {
	status  int
	code    string
	payload []byte
	body    []byte
}

func (w *faultWire) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == control.PathUsage {
		w.payload, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(w.payload))
	}
	res, err := http.DefaultTransport.RoundTrip(r)
	if err == nil && r.URL.Path == control.PathUsage {
		w.status = res.StatusCode
		w.body, _ = io.ReadAll(res.Body)
		res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(w.body))
		var x struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		json.Unmarshal(w.body, &x)
		w.code = x.Error.Code
	}
	return res, err
}

func TestRealFaultPostgresOutage(t *testing.T) {
	a, st := faultPanel(t, "")
	ctx := context.Background()
	srv := a.request("POST", "/api/v1/admin/servers", map[string]any{"name": "fault-server", "address": "127.0.0.1", "region": "test"})
	node := a.request("POST", "/api/v1/admin/nodes", map[string]any{"name": "fault-node", "server_id": srv["id"], "enabled": true, "host": "127.0.0.1", "listen": "127.0.0.1", "port": unusedPort(t), "protocol": "vless", "stream_settings": map[string]any{"network": "tcp", "security": "none"}, "protocol_settings": map[string]any{"decryption": "none"}, "client_settings": map[string]any{}})
	group := a.request("POST", "/api/v1/admin/node-groups", map[string]any{"name": "fault-group", "node_ids": []any{node["id"]}})
	sub := a.request("POST", "/api/v1/admin/subscriptions", map[string]any{"name": "fault-sub", "node_group_ids": []any{group["id"]}, "quota_bytes": 1 << 30, "timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true})
	member := a.request("POST", "/api/v1/admin/members", map[string]any{"username": "fault-member", "password": "Fault-only-password-123", "role": "member", "subscription_id": sub["id"]})
	reg := a.request("POST", "/api/v1/admin/servers/"+srv["id"].(string)+"/registration-codes", nil)
	c, err := control.New(a.base, "fault-test")
	faultCheck(t, err)
	wire := &faultWire{}
	c.SetHTTPClient(&http.Client{Transport: wire, Timeout: 15 * time.Second})
	id, err := c.Register(ctx, contracts.RegistrationRequest{Code: reg["code"].(string), Name: "fault-agent", Version: "fault-test"})
	faultCheck(t, err)
	c.SetToken(id.Token)
	snap, err := c.Snapshot(ctx, 0, "")
	faultCheck(t, err)
	if snap == nil || len(snap.Instances) != 1 {
		t.Fatal("missing authorized snapshot")
	}
	s, err := state.Open(t.TempDir())
	faultCheck(t, err)
	defer s.Close()
	uploader := reporting.NewUploader(s, c, nil, nil)
	counter := []state.CounterItem{{MemberID: member["id"].(string), Counter: "user>>>fault>>>traffic>>>downlink", Value: 100}}
	sample := func() contracts.UsageBatch {
		t.Helper()
		_, err := s.Measure(ctx, node["id"].(string), "fault-core", time.Now().UTC(), counter)
		faultCheck(t, err)
		p, err := s.PendingBatches(ctx, 10)
		faultCheck(t, err)
		if len(p) != 1 {
			t.Fatalf("queue=%d", len(p))
		}
		return contracts.UsageBatch{SchemaVersion: contracts.SchemaVersion, BatchID: p[0].BatchID, Samples: p[0].Samples}
	}
	first := sample()
	r, err := uploader.RunOnce(ctx)
	faultCheck(t, err)
	if r.Acked != 1 || wire.status != 200 {
		t.Fatalf("positive upload %+v status=%d", r, wire.status)
	}
	ledger := func(batch string) [4]int64 {
		t.Helper()
		var v [4]int64
		faultCheck(t, st.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(up_bytes+down_bytes),0) FROM usage_entries WHERE batch_id=$1`, batch).Scan(&v[0], &v[1]))
		faultCheck(t, st.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_batches WHERE batch_id=$1`, batch).Scan(&v[2]))
		faultCheck(t, st.db.QueryRowContext(ctx, `SELECT coalesce(sum(up_bytes+down_bytes),0) FROM usage_entries WHERE member_id=$1`, member["id"]).Scan(&v[3]))
		return v
	}
	before := ledger(first.BatchID)
	if before != [4]int64{1, 100, 1, 100} {
		t.Fatalf("positive ledger=%v", before)
	}
	time.Sleep(5 * time.Millisecond)
	counter[0].Value = 250
	pending := sample()
	original, _ := json.Marshal(pending)
	pgctl, pgdata := os.Getenv("PP_FAULT_PG_CTL"), os.Getenv("PP_FAULT_PG_DATA")
	if pgctl == "" || os.Getenv("PP_FAULT_PG_OPTIONS") == "" || !strings.HasPrefix(filepath.Base(filepath.Dir(pgdata)), "pp-real-fault-") {
		t.Fatal("owned PG control missing")
	}
	pg := func(action string) {
		t.Helper()
		args := []string{"-D", pgdata, "-w", "-t", "20"}
		if action == "stop" {
			args = append(args, "-m", "fast")
		}
		args = append(args, action)
		if action == "start" {
			args = append(args, "-l", os.Getenv("PP_FAULT_PG_LOG"), "-o", os.Getenv("PP_FAULT_PG_OPTIONS"))
		}
		out, err := exec.Command(pgctl, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("owned pg %s: %v %s", action, err, out)
		}
	}
	pg("stop")
	t.Cleanup(func() {
		exec.Command(pgctl, "-D", pgdata, "-w", "-t", "20", "-l", os.Getenv("PP_FAULT_PG_LOG"), "-o", os.Getenv("PP_FAULT_PG_OPTIONS"), "start").Run()
	})
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", cfgPort(t)), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("PG port still accepting")
	}
	r, err = uploader.RunOnce(ctx)
	if err == nil || r.Acked != 0 || r.Failed != 1 || !reflect.DeepEqual(r.Retained, []string{pending.BatchID}) {
		t.Fatalf("outage must be explicit non-ACK: %+v err=%v HTTP=%d code=%s body=%s", r, err, wire.status, wire.code, wire.body)
	}
	p, err := s.PendingBatches(ctx, 10)
	faultCheck(t, err)
	if len(p) != 1 {
		t.Fatal("lost pending batch")
	}
	retained, _ := json.Marshal(contracts.UsageBatch{SchemaVersion: contracts.SchemaVersion, BatchID: p[0].BatchID, Samples: p[0].Samples})
	if !bytes.Equal(original, retained) || !bytes.Equal(original, wire.payload) {
		t.Fatal("payload/ID changed during failure")
	}
	t.Logf("physical PG stop: HTTP=%d code=%s no ACK; retained batch=%s sha256=%x attempts=%d", wire.status, wire.code, pending.BatchID, sha256.Sum256(original), p[0].Attempts)
	// Keep the explicit service-unavailable oracle, but finish durability and
	// recovery checks even if error classification is a production defect.
	if wire.status != 503 || wire.code != "DB_UNAVAILABLE" {
		t.Errorf("PG outage classification: want 503 DB_UNAVAILABLE, got %d %s body=%s", wire.status, wire.code, wire.body)
	}
	pg("start")
	if got := ledger(pending.BatchID); got != [4]int64{0, 0, 0, 100} {
		t.Fatalf("business half-commit: %v", got)
	}
	if got := ledger(first.BatchID); got != before {
		t.Fatalf("prior ledger changed: %v", got)
	}
	t.Logf("SQL after restart BEFORE resend: failed batch rows/bytes/receipt/member=[0 0 0 100]; prior committed batch unchanged=%v", before)
	r, err = uploader.RunOnce(ctx)
	faultCheck(t, err)
	if r.Acked != 1 || wire.status != 200 || !bytes.Equal(wire.payload, original) {
		t.Fatal("recovery did not ACK identical payload")
	}
	committed := ledger(pending.BatchID)
	if committed != [4]int64{1, 150, 1, 250} {
		t.Fatalf("recovered ledger=%v", committed)
	}
	ack, err := c.Usage(ctx, pending)
	faultCheck(t, err)
	if ack.Status != "duplicate" || ledger(pending.BatchID) != committed {
		t.Fatal("replay double credited")
	}
	p, err = s.PendingBatches(ctx, 10)
	faultCheck(t, err)
	if len(p) != 0 {
		t.Fatal("ACKed queue not empty")
	}
	t.Logf("recovery: original payload accepted, replay duplicate, unique ledger rows/bytes/receipt/member=%v; queue empty", committed)
}
func cfgPort(t *testing.T) uint16 {
	c, e := pgx.ParseConfig(os.Getenv("PANEL_TEST_DATABASE_URL"))
	faultCheck(t, e)
	return c.Port
}

func TestRealFaultRedisOutage(t *testing.T) {
	bin := os.Getenv("PP_FAULT_REDIS_BIN")
	if bin == "" {
		t.Fatal("owned Redis binary required")
	}
	dir := t.TempDir()
	port := unusedPort(t)
	var proc *exec.Cmd
	start := func() {
		t.Helper()
		proc = exec.Command(bin, "--bind", "127.0.0.1", "--port", fmt.Sprint(port), "--dir", dir, "--save", "", "--appendonly", "no", "--protected-mode", "yes", "--set-proc-title", "no")
		faultCheck(t, proc.Start())
		waitFor(t, 10*time.Second, "owned redis listening", func() bool {
			c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if e == nil {
				c.Close()
			}
			return e == nil
		})
	}
	stop := func() {
		if proc != nil {
			proc.Process.Signal(syscall.SIGTERM)
			proc.Wait()
			proc = nil
		}
	}
	start()
	defer stop()
	a, _ := faultPanel(t, fmt.Sprintf("redis://127.0.0.1:%d/0", port))
	ready := func(status, redis string) {
		t.Helper()
		d := a.request("GET", "/readyz", nil)
		if d["status"] != status || d["redis"] != redis || d["postgres"] != "ok" {
			t.Fatalf("readyz=%v", d)
		}
	}
	ready("ok", "ok")
	stop()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err == nil {
		c.Close()
		t.Fatal("Redis still listening")
	}
	ready("degraded", "unavailable")
	// Contract is bounded local fallback, NOT a blanket 503. Exercise actual
	// unauthenticated login admission, including the specific 429 oracle.
	for i := 0; i < 11; i++ {
		raw := strings.NewReader(`{"username":"outage-unknown","password":"wrong"}`)
		res, err := http.Post(a.base+"/api/v1/auth/login", "application/json", raw)
		faultCheck(t, err)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var out struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		faultCheck(t, json.Unmarshal(b, &out))
		want := 401
		code := "UNAUTHENTICATED"
		if i >= 10 {
			want = 429
			code = "RATE_LIMITED"
		}
		if res.StatusCode != want || out.Error.Code != code {
			t.Fatalf("fallback attempt %d HTTP=%d code=%s want %d/%s body=%s", i+1, res.StatusCode, out.Error.Code, want, code, b)
		}
	}
	t.Log("physical Redis stop: readyz=200 degraded/unavailable; login 1..10=401 UNAUTHENTICATED, 11=429 RATE_LIMITED (bounded local fallback)")
	start()
	ready("ok", "ok")
	a.login("admin", "Fault-only-password-123")
	t.Log("recovery: readyz=ok/redis ok; legitimate login succeeds")
}

func TestRealFaultENOSPC(t *testing.T) {
	root := os.Getenv("PP_FAULT_FS")
	if root == "" {
		t.Fatal("private mount namespace runner required")
	}
	var fs syscall.Statfs_t
	faultCheck(t, syscall.Statfs(root, &fs))
	if fs.Type != 0x01021994 || fs.Blocks*uint64(fs.Bsize) > 16<<20 {
		t.Fatal("refusing non-private bounded tmpfs")
	}
	ctx := context.Background()
	dir := filepath.Join(root, "state")
	s, err := state.Open(dir)
	faultCheck(t, err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	items := []state.CounterItem{{MemberID: "member-fault", Counter: "user>>>fault>>>traffic>>>downlink", Value: 100}}
	_, err = s.Measure(ctx, "node-fault", "core-fault", now, items)
	faultCheck(t, err)
	faultCheck(t, s.SetApplied(ctx, "generation-fault", 7))
	faultCheck(t, s.Close())
	s, err = state.Open(dir)
	faultCheck(t, err)
	before, err := s.PendingBatches(ctx, 100)
	faultCheck(t, err)
	if len(before) != 1 {
		t.Fatal("positive queue absent")
	}
	original, _ := json.Marshal(before)
	facts := func() string {
		t.Helper()
		db, e := sql.Open("sqlite", "file:"+s.Path()+"?mode=ro")
		faultCheck(t, e)
		defer db.Close()
		var b, clock, seq, revision int64
		faultCheck(t, db.QueryRow(`SELECT value FROM baseline`).Scan(&b))
		faultCheck(t, db.QueryRow(`SELECT last_sampled_at FROM sample_clock`).Scan(&clock))
		faultCheck(t, db.QueryRow(`SELECT seq FROM samples`).Scan(&seq))
		faultCheck(t, db.QueryRow(`SELECT value FROM meta WHERE key='applied_revision'`).Scan(&revision))
		return fmt.Sprintf("baseline=%d clock=%d seq=%d revision=%d", b, clock, seq, revision)
	}
	beforeFacts := facts()
	t.Log("before physical FULL: " + beforeFacts)
	// Grow the real SQLite WAL beyond any previously allocated space; no mocks,
	// quotas, permission errors, host /tmp filling or injected SQLite failures.
	filler := filepath.Join(root, "owned-filler")
	f, err := os.Create(filler)
	faultCheck(t, err)
	block := make([]byte, 4096)
	var filled int64
	for {
		n, e := f.Write(block)
		filled += int64(n)
		if e != nil {
			if !errors.Is(e, syscall.ENOSPC) {
				t.Fatal(e)
			}
			break
		}
		if filled > 16<<20 {
			t.Fatal("bounded filesystem guard failed")
		}
	}
	faultCheck(t, f.Close())
	t.Logf("physical private tmpfs filled: write returned syscall.ENOSPC after %d bytes", filled)
	items[0].Value = 250
	_, err = s.Measure(ctx, "node-fault", "core-fault", now.Add(time.Second), items)
	var sqliteCode interface{ Code() int }
	if err == nil || !errors.As(err, &sqliteCode) || sqliteCode.Code() != 13 {
		t.Fatalf("want real SQLite FULL (13), got %T %v", err, err)
	}
	after, e := s.PendingBatches(ctx, 100)
	faultCheck(t, e)
	encoded, _ := json.Marshal(after)
	if !bytes.Equal(original, encoded) {
		t.Fatal("FULL lost or changed original queue")
	}
	identity, e := s.Identity(ctx)
	faultCheck(t, e)
	if identity.AppliedRevision != 7 {
		t.Fatal("watermark changed")
	}
	if got := facts(); got != beforeFacts {
		t.Fatalf("FULL changed durable SQL facts: %s", got)
	}
	faultCheck(t, s.Close())
	// Opening at capacity must either retain readable state or report actual
	// SQLite FULL, never a permission/nonexistent-path surrogate.
	fullOpen, openErr := state.Open(dir)
	if openErr == nil {
		q, e := fullOpen.PendingBatches(ctx, 100)
		faultCheck(t, e)
		raw, _ := json.Marshal(q)
		if !bytes.Equal(raw, original) {
			t.Fatal("open while FULL changed queue")
		}
		faultCheck(t, fullOpen.Close())
		t.Log("state.Open at physical FULL succeeded with original queue intact")
	} else {
		var code interface{ Code() int }
		if !errors.As(openErr, &code) || code.Code() != 13 {
			t.Fatalf("open while FULL: %v", openErr)
		}
		t.Log("state.Open at physical FULL returned SQLite FULL(13)")
	}
	faultCheck(t, os.Remove(filler))
	s, err = state.Open(dir)
	faultCheck(t, err)
	defer s.Close()
	if got := facts(); got != beforeFacts {
		t.Fatalf("reopen changed durable SQL facts: %s", got)
	}
	t.Log("after FULL and reopen: " + facts())
	after, e = s.PendingBatches(ctx, 100)
	faultCheck(t, e)
	encoded, _ = json.Marshal(after)
	if !bytes.Equal(original, encoded) {
		t.Fatal("reopen lost original queue")
	}
	result, err := s.Measure(ctx, "node-fault", "core-fault", now.Add(time.Second), items)
	faultCheck(t, err)
	after, e = s.PendingBatches(ctx, 100)
	faultCheck(t, e)
	if len(after) != 2 || result.BatchID == before[0].BatchID {
		t.Fatal("recovery did not add distinct batch")
	}
	var total int64
	var delta int64
	for _, b := range after {
		for _, x := range b.Samples {
			total += x.DownloadBytes
			if b.BatchID == result.BatchID {
				delta += x.DownloadBytes
				if !x.StartedAt.Equal(now) {
					t.Fatal("failed sample advanced sample_clock")
				}
			}
		}
	}
	if total != 250 || delta != 150 {
		t.Fatalf("lost/duplicate baseline delta: total=%d recovery=%d", total, delta)
	}
	identity, e = s.Identity(ctx)
	faultCheck(t, e)
	if identity.AppliedRevision != 7 {
		t.Fatal("reopen lost watermark")
	}
	t.Logf("SQLite FULL: original queue sha256=%x retained; reopen after filler removal preserved applied_revision=7, original ID and sample sequence; recovery delta=150 total=250", sha256.Sum256(original))
}
