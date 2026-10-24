package e2e

// Owned browser demo fixture (web-demo group A). It is NOT an acceptance claim:
// it starts a real Panel, a real AgentSupervisor and the official Xray core on
// loopback so web/tests/product-readiness-e2e.mjs can drive the real UI against
// a live control plane. Every helper is demoUI*-prefixed so it never collides
// with the shared data-plane runner.
//
// Isolation: the process only accepts a PostgreSQL database whose name starts
// with `wdemo_` on 127.0.0.1, creates its own random schema, opens no public
// port (net.Listen("127.0.0.1:0")), and tears the schema and Agent down when the
// browser writes the done signal. The launcher (web/tests/webdemo-env.sh) is
// responsible for the host-wide runtime flock; authoring never holds it.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"proxypanel/internal/agent/config"
	"proxypanel/internal/agent/control"
	"proxypanel/internal/agent/supervisor"
	"proxypanel/internal/panel/api"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type demoUIFixture struct {
	t        *testing.T
	base     string
	out      string
	schema   string
	admin    string
	pass     string
	dsn      string
	bin      string
	agent    *supervisor.Supervisor
	stop     context.CancelFunc
	done     chan struct{}
	db       *pgx.Conn
	agentRun chan error
}

func demoUIWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// demoUIReadyFile is the handshake the browser runner polls: it only exists
// after the Panel, the owned schema and the ready admin account are usable.
func (f *demoUIFixture) writeReady(extra map[string]any) {
	f.t.Helper()
	state := map[string]any{
		"status": "ready", "base": f.base, "schema": f.schema,
		"admin_username": "admin", "admin_password": f.pass,
		"database_scope": "owned wdemo_ database + random schema", "transport": "loopback development HTTP",
	}
	for k, v := range extra {
		state[k] = v
	}
	demoUIWriteJSON(f.t, filepath.Join(f.out, "state.json"), state)
}

func TestWebDemoUIFixture(t *testing.T) {
	dsn := os.Getenv("WEBDEMO_TEST_DATABASE_URL")
	xray := os.Getenv("WEBDEMO_XRAY_BIN")
	webRoot := os.Getenv("WEBDEMO_WEB_ROOT")
	out := os.Getenv("WEBDEMO_RUN_DIR")
	if dsn == "" || xray == "" || webRoot == "" || out == "" {
		t.Fatal("not_run: WEBDEMO_TEST_DATABASE_URL, WEBDEMO_XRAY_BIN, WEBDEMO_WEB_ROOT and WEBDEMO_RUN_DIR are required")
	}
	dbc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	if dbc.Host != "127.0.0.1" || !strings.HasPrefix(dbc.Database, "wdemo_") {
		t.Fatal("refusing non-owned fixture PostgreSQL (need 127.0.0.1/wdemo_*)")
	}
	if !strings.HasPrefix(out, "/tmp/") {
		t.Fatal("refusing non-private run directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect fixture PostgreSQL failed")
	}
	defer db.Close(ctx)
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"db", "agent"} {
		if err := os.MkdirAll(filepath.Join(out, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f := &demoUIFixture{t: t, out: out, schema: "wdemo_ui_" + hex.EncodeToString(random), dsn: dsn, bin: xray, done: make(chan struct{}), db: db,
		agentRun: make(chan error, 1)}
	if _, err := db.Exec(ctx, `CREATE SCHEMA "`+f.schema+`"`); err != nil {
		t.Fatal(err)
	}
	// The raw observation connection has no search_path of its own (unlike the
	// store pool): pin it to the owned schema so read-only queries can never
	// read another tenant's tables and never silently fall back to public.
	if _, err := db.Exec(ctx, `SET search_path TO "`+f.schema+`"`); err != nil {
		t.Fatal(err)
	}
	var pinned string
	if err := db.QueryRow(ctx, `SELECT current_schema()`).Scan(&pinned); err != nil || pinned != f.schema {
		t.Fatalf("observation connection not pinned to the owned schema: %q", pinned)
	}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := db.Exec(clean, `DROP SCHEMA "`+f.schema+`" CASCADE`); err != nil {
			t.Error("fixture schema cleanup failed")
		} else {
			t.Log("cleanup: fixture schema dropped")
		}
	}()
	key, err := secrets.ParseKey(strings.Repeat("webdemo-ui-random-schema-", 2) + "aa")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{DatabaseURL: dsn, MasterKey: key, MigrationsDir: "../../migrations", SearchPath: f.schema})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f.pass = "Webdemo-only-password-" + hex.EncodeToString(random)
	hash, err := secrets.HashPassword(f.pass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "admin", PasswordHash: hash, Role: "admin", Active: true, ProxyEnabled: true}); err != nil {
		t.Fatal(err)
	}
	limiter, err := limit.New("")
	if err != nil {
		t.Fatal(err)
	}
	defer limiter.Close()
	// Bind the real loopback port first: the member subscription URL is built
	// from PublicURL (falling back to Listen), so a `127.0.0.1:0` config would
	// hand the page a dead link and the UI-level "检查配置能否获取" would fail
	// with a network error unrelated to the product.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + listener.Addr().String()
	panel := &api.Server{Store: st, RateLimiter: limiter, Cfg: api.Config{Dev: true, Listen: listener.Addr().String(), PublicURL: baseURL, SessionTTL: time.Hour, WebRoot: webRoot}}
	server := httptest.NewUnstartedServer(panel.Handler())
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	f.base = baseURL
	f.admin = "admin"
	demoUIWriteJSON(t, filepath.Join(out, "build.json"), map[string]any{
		"fixture": "TestWebDemoUIFixture", "schema": f.schema, "web_root_sha_hint": "see launcher manifest",
	})

	// The Agent is started only on demand from the browser runner
	// (`agent/start` request file), so the UI itself creates every business
	// object before the Agent registers.
	agentStarted := make(chan string, 1)
	go demoUIWatchCommands(ctx, f, agentStarted)

	// Loopback ports for the real node inbound, the exported Xray client's SOCKS
	// inbound and the resumed client. Allocated here so the browser runner never
	// guesses, and closed again before use (release race is fail-closed).
	ports := []int{unusedPort(t), unusedPort(t), unusedPort(t)}
	f.writeReady(map[string]any{
		"schema": f.schema,
		"ports":  map[string]any{"inbound": ports[0], "socks": ports[1], "restored_socks": ports[2]},
	})
	t.Logf("fixture ready at %s (schema %s)", f.base, f.schema)

	// done signal from the browser runner. The fixture budget is bounded (the
	// launcher enforces 8 minutes for the whole run) so a lost done signal can
	// never hold the host runtime lock; the agent is always stopped *and
	// awaited* below, so no owned core child outlives this process.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	budget := 8 * time.Minute
	if raw := os.Getenv("WEBDEMO_FIXTURE_BUDGET"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 && d <= 15*time.Minute {
			budget = d
		}
	}
	stopReason := "done"
	timeout := time.After(budget)
	select {
	case <-f.done:
		t.Log("fixture: done signal received, shutting down")
	case <-timeout:
		stopReason = "fixture wall-clock budget reached"
		t.Log("fixture: " + stopReason + ", shutting down")
	case sig := <-sigs:
		stopReason = "signal " + sig.String()
		t.Log("fixture: signal received, shutting down")
	}
	if f.stop != nil {
		f.stop()
		// Confirm the supervisor (and therefore its official Xray children)
		// really finished instead of exiting on a fire-and-forget cancel.
		select {
		case runErr := <-f.agentRun:
			demoUIWriteJSON(t, filepath.Join(out, "agent", "stopped.json"), map[string]any{
				"reason": stopReason, "stopped": true, "error": demoUIRedact(fmt.Sprint(runErr)),
				"at": time.Now().UTC().Format(time.RFC3339)})
		case <-time.After(45 * time.Second):
			demoUIWriteJSON(t, filepath.Join(out, "agent", "stopped.json"), map[string]any{
				"reason": stopReason, "stopped": false, "error": "agent did not stop within 45s",
				"at": time.Now().UTC().Format(time.RFC3339)})
			t.Error("owned AgentSupervisor did not stop within 45s; refusing to claim clean shutdown")
		}
	}
	_ = os.Remove(filepath.Join(out, "done"))
}

// demoUIWatchCommands polls the run directory: the browser writes `agent/start`
// containing the one-time registration code once it created server → node →
// group → plan → member through the UI. This keeps creation order honest: no
// Agent reports exist before the UI created the objects.
func demoUIWatchCommands(ctx context.Context, f *demoUIFixture, started chan<- string) {
	startPath := filepath.Join(f.out, "agent", "start")
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
		if _, err := os.Stat(filepath.Join(f.out, "done")); err == nil {
			close(f.done)
			return
		}
		demoUIWatchDB(f)
		raw, err := os.ReadFile(startPath)
		if err != nil || len(raw) == 0 {
			continue
		}
		_ = os.Remove(startPath)
		var cmd struct {
			RegistrationCode string `json:"registration_code"`
		}
		if err := json.Unmarshal(raw, &cmd); err != nil || cmd.RegistrationCode == "" {
			demoUIWriteJSON(f.t, filepath.Join(f.out, "agent", "error.json"), map[string]any{"error": "invalid agent start command"})
			continue
		}
		if err := f.startAgent(ctx, cmd.RegistrationCode); err != nil {
			demoUIWriteJSON(f.t, filepath.Join(f.out, "agent", "error.json"), map[string]any{"error": err.Error()})
			continue
		}
		started <- cmd.RegistrationCode
		demoUIWriteJSON(f.t, filepath.Join(f.out, "agent", "started.json"), map[string]any{"agent": "real supervisor", "core": "official xray", "core_path": f.bin})
	}
}

// demoUIWatchDB serves read-only ledger snapshots requested by the browser
// runner. Only single SELECT statements are accepted: fixtures must never write
// the final usage or pretend to be an Agent report.
func demoUIWatchDB(f *demoUIFixture) {
	dir := filepath.Join(f.out, "db")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".request.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
		var req struct {
			ID  string `json:"id"`
			SQL string `json:"sql"`
		}
		result := map[string]any{}
		if err := json.Unmarshal(raw, &req); err != nil || req.ID == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.SQL)), "select") {
			result["error"] = "only read-only SELECT is allowed"
		} else {
			rows, err := f.db.Query(context.Background(), req.SQL)
			if err != nil {
				result["error"] = "query failed"
			} else {
				out := []map[string]any{}
				for rows.Next() {
					vals, err := rows.Values()
					if err != nil {
						result["error"] = "row read failed"
						break
					}
					row := map[string]any{}
					for i, fd := range rows.FieldDescriptions() {
						row[fd.Name] = vals[i]
					}
					out = append(out, row)
				}
				rows.Close()
				result["rows"] = out
			}
		}
		demoUIWriteJSON(f.t, filepath.Join(dir, req.ID+".result.json"), result)
	}
}

func (f *demoUIFixture) startAgent(parent context.Context, registrationCode string) error {
	cfg := &config.Config{
		PanelURL: f.base, RegistrationCode: registrationCode, XrayPath: f.bin,
		StateDir: filepath.Join(f.out, "agent", "state"), AllowLoopbackHTTP: true,
		PollIntervalSeconds: 2, StatsIntervalSeconds: 2, HeartbeatIntervalSeconds: 3,
	}
	if err := cfg.Normalize(); err != nil {
		return err
	}
	client, err := control.New(cfg.PanelURL, config.AgentVersion)
	if err != nil {
		return err
	}
	var logs strings.Builder
	sup, err := supervisor.New(supervisor.Options{Config: cfg, Client: client, Logger: log.New(&demoUILogWriter{&logs}, "", 0)})
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(parent)
	f.agent, f.stop = sup, stop
	go func() {
		err := sup.Run(ctx)
		// Sanitized diagnostics only: never dump tokens or private keys.
		demoUIWriteJSON(f.t, filepath.Join(f.out, "agent", "run.json"), map[string]any{
			"ended": err == nil, "detail": demoUIRedact(logs.String()), "at": time.Now().UTC().Format(time.RFC3339),
		})
		closeErr := sup.Close()
		select {
		case f.agentRun <- errors.Join(err, closeErr):
		default:
		}
	}()
	return nil
}

// demoUILogWriter keeps the last N KiB of Agent logs for sanitized diagnostics.
type demoUILogWriter struct{ sb *strings.Builder }

func (w *demoUILogWriter) Write(p []byte) (int, error) {
	if w.sb.Len() < 256<<10 {
		w.sb.Write(p)
	}
	return len(p), nil
}

// demoUIRedact replaces long machine tokens (registration codes, agent tokens,
// UUIDs) so diagnostics never carry credentials.
func demoUIRedact(raw string) string {
	var b strings.Builder
	for _, field := range strings.Fields(raw) {
		if len(field) >= 32 {
			b.WriteString("[redacted]")
		} else {
			b.WriteString(field)
		}
		b.WriteByte(' ')
	}
	return b.String()
}
