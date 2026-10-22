package maintenance

// PostgreSQL integration tests for the maintenance pass. They are skipped unless
// PANEL_TEST_DATABASE_URL is set (see docs/test-databases.md) and every run owns
// an isolated schema that is dropped afterwards. No real Telegram credentials
// are used: the notifier is pointed at a local httptest server, so "sent" only
// ever means "the simulated Bot API answered 200 + ok=true".

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PANEL_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("PANEL_TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("PANEL_TEST_DATABASE_URL is not a valid DSN: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 15432 || cfg.Database != "proxypanel" {
		t.Fatalf("refusing to run against %s:%d/%s; only the documented test instance 127.0.0.1:15432/proxypanel is allowed",
			cfg.Host, cfg.Port, cfg.Database)
	}
	return dsn
}

func migrationsDir(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"../../../migrations", "migrations"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatalf("migrations directory not found")
	return ""
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := testDSN(t)
	ctx := context.Background()
	schema := "ppmt_" + strings.ToLower(randomHex(t, 6))

	adminConn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	if _, err := adminConn.Exec(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		_ = adminConn.Close(ctx)
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = adminConn.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		_ = adminConn.Close(cleanupCtx)
	})

	key, err := secrets.ParseKey(strings.Repeat("maintenance-integration-master-key-", 2))
	if err != nil {
		t.Fatalf("master key: %v", err)
	}
	st, err := store.Open(ctx, store.Options{
		DatabaseURL: dsn, MasterKey: key, MigrationsDir: migrationsDir(t), SearchPath: schema, MaxConns: 6,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func mustExec(t *testing.T, st *store.Store, query string, args ...any) {
	t.Helper()
	if _, err := st.DB.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", firstLine(query), err)
	}
}

func firstLine(q string) string {
	if i := strings.IndexByte(q, '\n'); i > 0 {
		return q[:i]
	}
	return q
}

func scalarInt(t *testing.T, st *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", firstLine(query), err)
	}
	return n
}

type fixture struct {
	st  *store.Store
	now time.Time
}

func seed(t *testing.T, st *store.Store) *fixture {
	t.Helper()
	now := time.Now().UTC()
	ctx := context.Background()

	mustExec(t, st, `INSERT INTO servers (id, name, address, expires_at) VALUES ('srv_1','edge', '10.0.0.1', $1)`,
		now.Add(72*time.Hour))
	mustExec(t, st, `INSERT INTO servers (id, name, address) VALUES ('srv_2','stable','10.0.0.2')`)
	mustExec(t, st, `INSERT INTO nodes (id, server_id, name, host, port, protocol) VALUES ('nd_1','srv_1','node-1','10.0.0.1',443,'vless')`)
	mustExec(t, st, `INSERT INTO core_instances (id, server_id, node_id, state, last_error) VALUES ('ci_1','srv_1','nd_1','failed','core exited with code 1')`)

	// Agent that stopped heartbeating 10 minutes ago.
	mustExec(t, st, `INSERT INTO agents (id, server_id, name, token_hash, last_heartbeat_at, created_at)
		VALUES ('agt_off','srv_1','edge-off',$1,$2,$3)`, "hash-off", now.Add(-10*time.Minute), now.Add(-2*time.Hour))
	// Agent registered 5 seconds ago that never sent a heartbeat: still in grace.
	mustExec(t, st, `INSERT INTO agents (id, server_id, name, token_hash, created_at)
		VALUES ('agt_new','srv_1','edge-new',$1,$2)`, "hash-new", now.Add(-5*time.Second))

	mustExec(t, st, `INSERT INTO subscriptions (id, name, quota_bytes, timezone, anchor_day)
		VALUES ('sub_1','pool',NULL,'Asia/Shanghai',1)`)
	mustExec(t, st, `INSERT INTO subscriptions (id, name, quota_bytes, timezone, anchor_day)
		VALUES ('sub_2','zero-quota',0,'Asia/Shanghai',1)`)
	// Expired binding.
	mustExec(t, st, `INSERT INTO members (id, username, password_hash, subscription_id, expires_at, bound_at)
		VALUES ('mbr_exp','expired',$1,'sub_1',$2,$3)`, "x", now.Add(-time.Hour), now.Add(-40*24*time.Hour))
	// Over quota: the effective quota is 0, so any usage (even none) is over it.
	mustExec(t, st, `INSERT INTO members (id, username, password_hash, subscription_id, expires_at, bound_at)
		VALUES ('mbr_over','over',$1,'sub_2',$2,$3)`, "x", now.Add(10*24*time.Hour), now.Add(-24*time.Hour))
	mustExec(t, st, `UPDATE members SET over_quota=true WHERE id='mbr_over'`)

	// Ledger and credential rows that the cleanup must never touch.
	mustExec(t, st, `INSERT INTO usage_batches (agent_id, batch_id, payload_sha256, started_at, ended_at)
		VALUES ('agt_off','ub_1','sha',$1,$2)`, now.Add(-80*time.Hour), now.Add(-79*time.Hour))
	mustExec(t, st, `INSERT INTO usage_entries (agent_id, batch_id, member_id, node_id, period_start, period_end,
			up_bytes, down_bytes, sample_started_at, sample_ended_at, received_at)
		VALUES ('agt_off','ub_1','mbr_exp','nd_1',$1,$2,10,20,$3,$4,$5)`,
		now.Add(-80*time.Hour), now.Add(-79*time.Hour), now.Add(-80*time.Hour), now.Add(-79*time.Hour), now.Add(-79*time.Hour))
	mustExec(t, st, `INSERT INTO credentials (id, member_id, node_id, kind, secret_enc)
		VALUES ('crd_1','mbr_exp','nd_1','uuid',$1)`, []byte{1, 2, 3})
	mustExec(t, st, `INSERT INTO idempotency_records (key, scope, fingerprint) VALUES ('idem_old','batch','fp')`)

	// Aged rows that the bounded cleanup is allowed to delete.
	mustExec(t, st, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message, state, created_at)
		VALUES ('evt_old_acked','agent_offline','warning','agent','agt_off','old','acked',$1)`, now.Add(-60*24*time.Hour))
	mustExec(t, st, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message, state, created_at)
		VALUES ('evt_old_open','agent_offline','warning','agent','agt_off','old open','open',$1)`, now.Add(-60*24*time.Hour))
	mustExec(t, st, `INSERT INTO audit_logs (id, action, created_at) VALUES ('aud_old','settings.update',$1)`,
		now.Add(-60*24*time.Hour))
	mustExec(t, st, `INSERT INTO previews (id, operation, payload, plan, rules_version, member_set_digest, expires_at, created_at)
		VALUES ('prv_old','assign_subscription','{}','{}',1,'',$1,$2)`, now.Add(-10*24*time.Hour), now.Add(-20*24*time.Hour))
	mustExec(t, st, `INSERT INTO sessions (id, member_id, token_hash, csrf_token, expires_at, created_at)
		VALUES ('ses_old','mbr_exp','sess-hash','csrf',$1,$2)`, now.Add(-5*24*time.Hour), now.Add(-30*24*time.Hour))
	mustExec(t, st, `INSERT INTO registration_codes (id, server_id, code_hash, expires_at) VALUES ('rc_old','srv_1','rc-hash',$1)`,
		now.Add(-5*24*time.Hour))
	mustExec(t, st, `INSERT INTO invitations (id, code_hash, expires_at) VALUES ('inv_old','inv-hash',$1)`,
		now.Add(-5*24*time.Hour))

	// A completed batch from two months ago: safe to drop.
	mustExec(t, st, `INSERT INTO batches (id, preview_id, operation, state, idempotency_key, request_fingerprint, completed_at, created_at)
		VALUES ('bat_old','prv_old','assign_subscription','completed','idem-bat-old','fp',$1,$2)`,
		now.Add(-60*24*time.Hour), now.Add(-61*24*time.Hour))
	mustExec(t, st, `INSERT INTO batch_items (id, batch_id, member_id, node_id, state) VALUES ('bi_old','bat_old','mbr_exp','nd_1','succeeded')`)
	// A completed batch whose task is still pending: must be preserved.
	mustExec(t, st, `INSERT INTO batches (id, preview_id, operation, state, idempotency_key, request_fingerprint, completed_at, created_at)
		VALUES ('bat_pending','prv_old','assign_subscription','completed','idem-bat-pending','fp',$1,$2)`,
		now.Add(-60*24*time.Hour), now.Add(-61*24*time.Hour))
	mustExec(t, st, `INSERT INTO batch_items (id, batch_id, member_id, node_id, state) VALUES ('bi_pending','bat_pending','mbr_exp','nd_1','pending')`)
	mustExec(t, st, `INSERT INTO tasks (id, kind, agent_id, batch_id, state) VALUES ('tsk_open','apply','agt_off','bat_pending','pending')`)

	// Retention floor policy: 7 days.
	if _, err := st.UpdateSettings(ctx, 0, func(v *domain.Settings) error {
		v.RetentionDays = 7
		v.TelegramEnabled = false
		return nil
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}
	return &fixture{st: st, now: now}
}

func TestMaintenanceIntegrationPass(t *testing.T) {
	st := openTestStore(t)
	seed(t, st)
	ctx := context.Background()

	// A simulated Bot API that acknowledges every message.
	var delivered int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		delivered++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	svc := newService(st, srv.Client())
	svc.baseURL = srv.URL // test only
	svc.limits.Notifications = 20

	// ── pass 1: every condition is raised exactly once, nothing is sent while
	// Telegram is disabled.
	stats, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if stats.Skipped {
		t.Fatal("first pass must not be skipped")
	}
	if stats.Raised != 5 {
		t.Fatalf("raised = %d, want 5 (agent_offline, core_instance_failed, member_expired, over_quota, server_expiring); stats %+v",
			stats.Raised, stats)
	}
	if stats.Sent != 0 || delivered != 0 {
		t.Fatalf("notifications must not be sent while Telegram is disabled: %+v", stats)
	}
	if stats.Cleanup.Total() == 0 {
		t.Fatal("the cleanup must remove the aged rows")
	}

	// ── pass 2: no duplicate events, no duplicate outbox rows.
	stats2, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if stats2.Raised != 0 {
		t.Fatalf("second pass raised %d duplicate alerts", stats2.Raised)
	}
	if stats2.Adopted != 5 {
		t.Fatalf("second pass adopted %d, want 5", stats2.Adopted)
	}
	if stats2.Enqueued != 0 {
		t.Fatalf("second pass enqueued %d duplicate notifications", stats2.Enqueued)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM events WHERE state='open' AND dedupe_key IS NOT NULL`); got != 5 {
		t.Fatalf("open alert events = %d, want 5", got)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM notification_outbox`); got != 5 {
		t.Fatalf("outbox rows = %d, want 5", got)
	}

	// ── cleanup policy: the ledger, credentials, idempotency records and
	// unfinished work survive; aged session/preview/code/invitation/audit/event
	// rows are gone.
	if got := scalarInt(t, st, `SELECT count(*) FROM usage_entries`); got != 1 {
		t.Fatalf("usage_entries must never be cleaned up, got %d rows", got)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM usage_batches`); got != 1 {
		t.Fatalf("usage_batches must never be cleaned up, got %d rows", got)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM credentials`); got != 1 {
		t.Fatalf("credentials must never be cleaned up, got %d rows", got)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM idempotency_records`); got != 1 {
		t.Fatalf("idempotency_records must never be cleaned up, got %d rows", got)
	}
	for _, table := range []string{"audit_logs", "previews", "sessions", "registration_codes", "invitations"} {
		if got := scalarInt(t, st, `SELECT count(*) FROM `+table); got != 0 {
			t.Fatalf("%s still has %d aged rows", table, got)
		}
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM events WHERE id='evt_old_acked'`); got != 0 {
		t.Fatal("a closed event older than the retention must be removed")
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM events WHERE id='evt_old_open'`); got != 1 {
		t.Fatal("an open alert must never be deleted by the cleanup")
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM batches WHERE id='bat_old'`); got != 0 {
		t.Fatal("an old completed batch must be removed")
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM batch_items WHERE batch_id='bat_old'`); got != 0 {
		t.Fatal("the batch items of a removed batch must be removed first (FK order)")
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM batches WHERE id='bat_pending'`); got != 1 {
		t.Fatal("a batch with a pending task must be preserved")
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM tasks`); got != 1 {
		t.Fatal("tasks must never be deleted by the maintenance pass")
	}

	// ── the agent in the registration grace window is not alerted.
	if got := scalarInt(t, st, `SELECT count(*) FROM events WHERE dedupe_key='agent_offline:agt_new'`); got != 0 {
		t.Fatal("an agent registered seconds ago must not be reported offline")
	}

	// ── enable Telegram: the queued notifications are delivered now, and the
	// decision follows the latest settings.
	if _, err := st.UpdateSettings(ctx, 0, func(v *domain.Settings) error {
		v.TelegramEnabled = true
		v.TelegramChatID = "-1001234567890"
		v.TelegramToken = "123456:simulated-token"
		return nil
	}); err != nil {
		t.Fatalf("enable telegram: %v", err)
	}
	stats3, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("delivery pass: %v", err)
	}
	if stats3.Sent != 5 || delivered != 5 {
		t.Fatalf("sent = %d (server saw %d), want 5", stats3.Sent, delivered)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM notification_outbox WHERE state='sent' AND sent_at IS NOT NULL`); got != 5 {
		t.Fatalf("outbox sent rows = %d, want 5", got)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM notification_outbox WHERE text LIKE '%simulated-token%'`); got != 0 {
		t.Fatal("the bot token must never be stored in the outbox")
	}

	// ── recovery: the heartbeat returns, so the alert closes and a recovery
	// event plus notification is written.
	mustExec(t, st, `UPDATE agents SET last_heartbeat_at=now() WHERE id='agt_off'`)
	stats4, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	if stats4.Recovered != 1 {
		t.Fatalf("recovered = %d, want 1", stats4.Recovered)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM events WHERE dedupe_key='agent_offline:agt_off' AND state='open'`); got != 0 {
		t.Fatal("the recovered alert must no longer be open")
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM events WHERE kind='agent_offline' AND state='recovered' AND message LIKE '%已恢复%'`); got < 1 {
		t.Fatal("a recovery event must be recorded")
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM notification_outbox o JOIN events e ON e.id=o.event_id
		WHERE e.state='recovered'`); got < 1 {
		t.Fatal("the recovery must be notified through the outbox")
	}

	// ── the alert comes back later: it is a new open event, not a duplicate of
	// the old one.
	mustExec(t, st, `UPDATE agents SET last_heartbeat_at=now() - interval '10 minutes' WHERE id='agt_off'`)
	stats5, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("re-raise pass: %v", err)
	}
	if stats5.Raised != 1 {
		t.Fatalf("re-raised = %d, want 1", stats5.Raised)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM events WHERE dedupe_key='agent_offline:agt_off' AND state='open'`); got != 1 {
		t.Fatal("exactly one open event may exist per kind and subject")
	}
}

// Only one process may run a pass at a time; the others skip instead of racing.
func TestMaintenanceIntegrationSingleExecutor(t *testing.T) {
	st := openTestStore(t)
	seed(t, st)
	ctx := context.Background()

	conn, err := st.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, store.MaintenanceLockKey); err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, store.MaintenanceLockKey)
	}()

	svc := newService(st, nil)
	stats, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("pass under a held lock: %v", err)
	}
	if !stats.Skipped {
		t.Fatalf("the pass must be skipped while another session holds the lock: %+v", stats)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM events WHERE state='open' AND dedupe_key IS NOT NULL`); got != 0 {
		t.Fatalf("a skipped pass must not write events, found %d", got)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, store.MaintenanceLockKey); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	stats, err = svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("pass after unlock: %v", err)
	}
	if stats.Skipped || stats.Raised != 5 {
		t.Fatalf("after the lock is released the pass must run: %+v", stats)
	}
}

// A failing Bot API must never be reported as delivered: rows stay pending with
// a sanitized error and a backoff.
func TestMaintenanceIntegrationDeliveryFailure(t *testing.T) {
	st := openTestStore(t)
	seed(t, st)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":30}}`))
	}))
	defer srv.Close()

	svc := newService(st, srv.Client())
	svc.baseURL = srv.URL
	svc.limits.Notifications = 20

	if _, err := svc.RunOnce(ctx); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if _, err := st.UpdateSettings(ctx, 0, func(v *domain.Settings) error {
		v.TelegramEnabled = true
		v.TelegramChatID = "-1001234567890"
		v.TelegramToken = "123456:simulated-token"
		return nil
	}); err != nil {
		t.Fatalf("enable telegram: %v", err)
	}
	stats, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("delivery pass: %v", err)
	}
	if stats.Sent != 0 {
		t.Fatalf("a 429 must not be reported as delivered: %+v", stats)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM notification_outbox WHERE state='sent'`); got != 0 {
		t.Fatalf("no outbox row may be marked sent after a 429, found %d", got)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM notification_outbox WHERE state='pending' AND attempts > 0 AND last_error LIKE '%HTTP 429%'`); got == 0 {
		t.Fatal("the failed attempt must be recorded with a sanitized error")
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM notification_outbox WHERE last_error LIKE '%simulated-token%' OR last_error LIKE '%api.telegram.org%'`); got != 0 {
		t.Fatal("the recorded error must not contain the token or the request URL")
	}
	raw, err := st.ListNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	for _, n := range raw {
		if strings.Contains(n.Text, "simulated-token") {
			t.Fatal("notification text must never contain the bot token")
		}
		if n.LastError == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(n.LastError), &v); err == nil {
			t.Fatalf("the sanitized error must be plain text, got JSON %q", n.LastError)
		}
	}
}

// A panel host whose clock lags the database must still deliver. Outbox rows are
// created with the database clock (`DEFAULT now()`), so leasing them with the
// caller's wall clock silently stalls the queue whenever the panel clock is even
// slightly behind - the row is "not due yet" from the panel's point of view, and
// the delay grows with the skew. The same skew pushed retry backoffs into the
// future. Leasing and backoff are therefore evaluated on the database clock.
//
// This is a real deployment concern, not a test artefact: a panel and its
// PostgreSQL do not share a clock, and the acceptance topology already measured
// multi-second offsets between hosts.
func TestMaintenanceDeliversWhenPanelClockLagsDatabase(t *testing.T) {
	st := openTestStore(t)
	seed(t, st)
	ctx := context.Background()

	var delivered int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	svc := newService(st, srv.Client())
	svc.baseURL = srv.URL
	svc.limits.Notifications = 20

	// The panel believes it is one minute behind the database. A correct
	// implementation must still see the freshly enqueued rows as due.
	lagging := time.Now().UTC().Add(-time.Minute)
	svc.now = func() time.Time { return lagging }

	if _, err := svc.RunOnce(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if _, err := st.UpdateSettings(ctx, 0, func(v *domain.Settings) error {
		v.TelegramEnabled = true
		v.TelegramChatID = "-1001234567890"
		v.TelegramToken = "123456:simulated-token"
		return nil
	}); err != nil {
		t.Fatalf("enable telegram: %v", err)
	}
	stats, err := svc.RunOnce(ctx)
	if err != nil {
		t.Fatalf("delivery pass: %v", err)
	}
	if stats.Sent != 5 || delivered != 5 {
		t.Fatalf("a panel clock lagging the database must not stall delivery: sent = %d (server saw %d), want 5",
			stats.Sent, delivered)
	}
	if got := scalarInt(t, st, `SELECT count(*) FROM notification_outbox WHERE state='sent'`); got != 5 {
		t.Fatalf("outbox sent rows = %d, want 5", got)
	}
}

// The mirror case: when delivery fails, the recorded retry must be a real delay
// ahead of the database clock, even if the panel clock leads or lags. Otherwise a
// lagging panel retries too early (hot loop) and a leading one stalls for the
// skew's duration.
func TestMaintenanceBackoffIsMeasuredFromDatabaseClock(t *testing.T) {
	st := openTestStore(t)
	seed(t, st)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":30}}`))
	}))
	defer srv.Close()

	svc := newService(st, srv.Client())
	svc.baseURL = srv.URL
	svc.limits.Notifications = 20

	// Panel clock leads the database by an hour: a naive absolute deadline would
	// be "one hour minus 30s" in the future and the row would never retry.
	svc.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }

	if _, err := svc.RunOnce(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if _, err := st.UpdateSettings(ctx, 0, func(v *domain.Settings) error {
		v.TelegramEnabled = true
		v.TelegramChatID = "-1001234567890"
		v.TelegramToken = "123456:simulated-token"
		return nil
	}); err != nil {
		t.Fatalf("enable telegram: %v", err)
	}
	if _, err := svc.RunOnce(ctx); err != nil {
		t.Fatalf("delivery pass: %v", err)
	}

	// The retry must be due relative to the database, not to the skewed panel.
	var dueIn float64
	if err := st.DB.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM (next_attempt_at - now()))
		FROM notification_outbox ORDER BY next_attempt_at LIMIT 1`).Scan(&dueIn); err != nil {
		t.Fatalf("read next_attempt_at: %v", err)
	}
	if dueIn < 25 || dueIn > 40 {
		t.Fatalf("the retry must be ~30s ahead of the database clock, got %.1fs (a skewed panel must not stretch or collapse the backoff)", dueIn)
	}
}
