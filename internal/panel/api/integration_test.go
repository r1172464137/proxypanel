package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/api"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

// Integration tests require an explicitly injected test database URL, e.g.
//
//	$env:PANEL_TEST_DATABASE_URL = "postgres://proxypanel:<password>@127.0.0.1:15432/proxypanel?sslmode=disable"
//
// They are skipped when it is not set. For safety the URL must point at the
// documented test instance only; every run creates and drops its own schema.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PANEL_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("PANEL_TEST_DATABASE_URL is not set; skipping PostgreSQL integration tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("PANEL_TEST_DATABASE_URL is not a valid DSN: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 15432 || cfg.Database != "proxypanel" {
		t.Fatalf("refusing to run integration tests against %s:%d/%s; only 127.0.0.1:15432/proxypanel is allowed",
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

type env struct {
	t       *testing.T
	st      *store.Store
	srv     *api.Server
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
	agent   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := testDSN(t)
	ctx := context.Background()
	schema := "pptest_" + strings.ToLower(randomHex(t, 6))

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

	key, err := secrets.ParseKey(strings.Repeat("integration-test-master-key-", 2))
	if err != nil {
		t.Fatalf("master key: %v", err)
	}
	st, err := store.Open(ctx, store.Options{
		DatabaseURL: dsn, MasterKey: key, MigrationsDir: migrationsDir(t), SearchPath: schema,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)

	limiter, err := limit.New("")
	if err != nil {
		t.Fatalf("limiter: %v", err)
	}
	t.Cleanup(func() { _ = limiter.Close() })

	srv := &api.Server{
		Store: st, RateLimiter: limiter,
		Cfg: api.Config{Dev: true, Listen: "127.0.0.1:8080", PublicURL: "http://127.0.0.1:8080",
			SessionTTL: time.Hour},
	}
	e := &env{t: t, st: st, srv: srv, handler: srv.Handler()}
	e.createAdmin()
	return e
}

func (e *env) createAdmin() {
	e.t.Helper()
	hash, err := secrets.HashPassword("admin-password-1")
	if err != nil {
		e.t.Fatalf("hash: %v", err)
	}
	if _, err := e.st.CreateMember(context.Background(), &domain.Member{
		ID: ids.New(ids.Member), Username: "admin", PasswordHash: hash,
		Role: "admin", Active: true, ProxyEnabled: true,
	}); err != nil {
		e.t.Fatalf("create admin: %v", err)
	}
}

type response struct {
	Status int
	Body   map[string]any
	Header http.Header
}

func (e *env) do(method, path string, body any) *response {
	e.t.Helper()
	return e.doWith(method, path, body, nil)
}

func (e *env) doWith(method, path string, body any, header map[string]string) *response {
	e.t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	if e.csrf != "" {
		req.Header.Set("X-CSRF-Token", e.csrf)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	out := &response{Status: rec.Code, Header: rec.Header()}
	if rec.Body.Len() > 0 {
		var parsed map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			e.t.Fatalf("%s %s returned non JSON body %q: %v", method, path, rec.Body.String(), err)
		}
		out.Body = parsed
	}
	return out
}

func (r *response) data(t *testing.T) map[string]any {
	t.Helper()
	if r.Status < 200 || r.Status > 299 {
		t.Fatalf("expected success, got %d: %v", r.Status, r.Body)
	}
	d, ok := r.Body["data"].(map[string]any)
	if !ok {
		t.Fatalf("response has no data object: %v", r.Body)
	}
	return d
}

func (r *response) items(t *testing.T) []any {
	t.Helper()
	if r.Status < 200 || r.Status > 299 {
		t.Fatalf("expected success, got %d: %v", r.Status, r.Body)
	}
	items, ok := r.Body["items"].([]any)
	if !ok {
		t.Fatalf("response has no items array: %v", r.Body)
	}
	return items
}

func (r *response) errCode(t *testing.T) string {
	t.Helper()
	errObj, ok := r.Body["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error envelope, got %d: %v", r.Status, r.Body)
	}
	code, _ := errObj["code"].(string)
	return code
}

func (e *env) login() {
	e.t.Helper()
	res := e.do("POST", "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "admin-password-1",
	})
	data := res.data(e.t)
	csrf, _ := data["csrf_token"].(string)
	if csrf == "" {
		e.t.Fatalf("login did not return a csrf token: %v", data)
	}
	e.csrf = csrf
	for _, c := range (&http.Response{Header: res.Header}).Cookies() {
		if c.Name == "pp_session" {
			e.cookie = c
		}
	}
	if e.cookie == nil {
		e.t.Fatalf("login did not set the session cookie: %v", res.Header)
	}
}

func (e *env) str(data map[string]any, key string) string {
	e.t.Helper()
	v, _ := data[key].(string)
	if v == "" {
		e.t.Fatalf("missing %q in %v", key, data)
	}
	return v
}

// TestPanelFlow exercises the frozen HTTP contract against a real PostgreSQL
// schema: auth + CSRF, resources, batch preview/submit, usage idempotency and
// snapshots (including the empty authorization case).
func TestPanelFlow(t *testing.T) {
	e := newEnv(t)

	t.Run("login_and_csrf", func(t *testing.T) {
		bad := e.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "wrong"})
		if bad.Status != http.StatusUnauthorized {
			t.Fatalf("wrong password = %d, want 401", bad.Status)
		}
		e.login()
		session := e.do("GET", "/api/v1/auth/session", nil).data(t)
		if _, ok := session["user"].(map[string]any); !ok {
			t.Fatalf("session payload = %v", session)
		}
		// A write without the CSRF header must be refused.
		saved := e.csrf
		e.csrf = ""
		noCSRF := e.do("POST", "/api/v1/admin/servers", map[string]any{"name": "srv-nocsrf"})
		if noCSRF.Status != http.StatusForbidden || noCSRF.errCode(t) != domain.CodeCSRFFailed {
			t.Fatalf("missing CSRF = %d %v", noCSRF.Status, noCSRF.Body)
		}
		e.csrf = saved
	})

	var serverID, groupID, nodeID, subA, subB, memberID string
	t.Run("create_resources", func(t *testing.T) {
		serverID = e.str(e.do("POST", "/api/v1/admin/servers", map[string]any{
			"name": "tokyo-1", "address": "node.example.invalid", "region": "jp",
		}).data(t), "id")

		nodeID = e.str(e.do("POST", "/api/v1/admin/nodes", map[string]any{
			"name": "vless-reality", "server_id": serverID, "host": "node.example.invalid",
			"port": 443, "listen": "0.0.0.0", "protocol": "vless", "flow": "xtls-rprx-vision",
			"stream_settings": map[string]any{
				"network": "tcp", "security": "reality",
				"realitySettings": map[string]any{"shortIds": []string{"0123abcd"},
					"serverNames": []string{"www.example.invalid"}, "dest": "www.example.invalid:443"},
				"sockopt": map[string]any{"tcpFastOpen": true},
			},
			"protocol_settings": map[string]any{"decryption": "none"},
			"client_settings":   map[string]any{"server_name": "www.example.invalid"},
		}).data(t), "id")

		groupID = e.str(e.do("POST", "/api/v1/admin/node-groups", map[string]any{
			"name": "group-a", "node_ids": []string{nodeID},
		}).data(t), "id")

		subA = e.str(e.do("POST", "/api/v1/admin/subscriptions", map[string]any{
			"name": "sub-a", "node_group_ids": []string{groupID}, "quota_bytes": 1 << 30,
			"timezone": "Asia/Shanghai", "anchor_day": 1, "default_valid_days": 30,
			"allow_new_bindings": true, "access_enabled": true,
		}).data(t), "id")
		subB = e.str(e.do("POST", "/api/v1/admin/subscriptions", map[string]any{
			"name": "sub-b", "node_group_ids": []string{groupID}, "quota_bytes": 2 << 30,
			"timezone": "Asia/Shanghai", "anchor_day": 1, "allow_new_bindings": true,
			"access_enabled": true,
		}).data(t), "id")

		member := e.do("POST", "/api/v1/admin/members", map[string]any{
			"username": "alice", "password": "alice-password-1", "role": "member",
			"subscription_id": subA,
		}).data(t)
		memberID = e.str(member, "id")
		if member["subscription_id"] != subA {
			t.Fatalf("member subscription = %v", member["subscription_id"])
		}

		detail := e.do("GET", "/api/v1/admin/members/"+memberID, nil).data(t)
		access, _ := detail["access"].(map[string]any)
		if access["access"] != true {
			t.Fatalf("member should have access: %v", detail)
		}
		nodes, _ := access["allowed_node_ids"].([]any)
		if len(nodes) != 1 {
			t.Fatalf("allowed nodes = %v", access["allowed_node_ids"])
		}
	})

	var previewID, digest, batchID string
	var rulesVersion float64
	t.Run("batch_preview_and_submit", func(t *testing.T) {
		preview := e.do("POST", "/api/v1/admin/batches/preview", map[string]any{
			"operation": "assign_subscription", "member_ids": []string{memberID},
			"values": map[string]any{"subscription_id": subB}, "reason": "switch plan",
		}).data(t)
		previewID = e.str(preview, "id")
		digest = e.str(preview, "member_set_digest")
		rulesVersion = preview["rules_version"].(float64)
		if preview["affected_count"].(float64) != 1 {
			t.Fatalf("affected_count = %v", preview["affected_count"])
		}

		// The direct PATCH path must refuse to bypass the preview flow.
		direct := e.do("PATCH", "/api/v1/admin/subscriptions/"+subA, map[string]any{
			"version": 1, "quota_bytes": 42,
		})
		if direct.Status != http.StatusConflict || direct.errCode(t) != domain.CodePreviewRequired {
			t.Fatalf("direct subscription edit = %d %v", direct.Status, direct.Body)
		}

		payload := map[string]any{
			"preview_id": previewID, "confirm": true,
			"rules_version": rulesVersion, "member_set_digest": digest,
		}
		submit := e.doWith("POST", "/api/v1/admin/batches", payload,
			map[string]string{"Idempotency-Key": "idem-" + previewID})
		data := submit.data(t)
		batchID = e.str(data, "id")
		if data["state"] != "applied" {
			t.Fatalf("batch state = %v", data["state"])
		}
		results, _ := data["results"].([]any)
		if len(results) == 0 {
			t.Fatalf("expected per object dispatch rows: %v", data)
		}

		member := e.do("GET", "/api/v1/admin/members/"+memberID, nil).data(t)
		got := member["member"].(map[string]any)
		if got["subscription_id"] != subB {
			t.Fatalf("member subscription after batch = %v", got["subscription_id"])
		}

		// Replaying the same idempotency key returns the stored result.
		replay := e.doWith("POST", "/api/v1/admin/batches", payload,
			map[string]string{"Idempotency-Key": "idem-" + previewID}).data(t)
		if replay["id"] != batchID || replay["idempotent_replay"] != true {
			t.Fatalf("replay = %v", replay)
		}
		// Omitting version preconditions changes the payload, not a valid retry.
		changed := e.doWith("POST", "/api/v1/admin/batches", map[string]any{
			"preview_id": previewID, "confirm": true,
		}, map[string]string{"Idempotency-Key": "idem-" + previewID})
		if changed.Status != http.StatusConflict || changed.errCode(t) != domain.CodeIdempotencyKeyReused {
			t.Fatalf("changed retry = %d %v", changed.Status, changed.Body)
		}
		// A different key for the same consumed preview is rejected.
		reuse := e.doWith("POST", "/api/v1/admin/batches", map[string]any{
			"preview_id": previewID, "confirm": true,
		}, map[string]string{"Idempotency-Key": "idem-other-" + previewID})
		if reuse.Status != http.StatusConflict {
			t.Fatalf("consumed preview reuse = %d %v", reuse.Status, reuse.Body)
		}

		got2 := e.do("GET", "/api/v1/admin/batches/"+batchID, nil).data(t)
		if got2["id"] != batchID {
			t.Fatalf("get batch = %v", got2)
		}
	})

	t.Run("expired_preview_and_idempotency_conflict", func(t *testing.T) {
		preview := e.do("POST", "/api/v1/admin/batches/preview", map[string]any{
			"operation": "set_active", "member_ids": []string{memberID},
			"values": map[string]any{"active": false},
		}).data(t)
		id := e.str(preview, "id")
		if _, err := e.st.DB.ExecContext(context.Background(),
			`UPDATE previews SET expires_at = now() - interval '1 minute' WHERE id=$1`, id); err != nil {
			t.Fatalf("expire preview: %v", err)
		}
		res := e.doWith("POST", "/api/v1/admin/batches", map[string]any{
			"preview_id": id, "confirm": true,
		}, map[string]string{"Idempotency-Key": "idem-expired-" + id})
		if res.Status != http.StatusConflict || res.errCode(t) != domain.CodePreviewStale {
			t.Fatalf("expired preview = %d %v", res.Status, res.Body)
		}

		fresh := e.do("POST", "/api/v1/admin/batches/preview", map[string]any{
			"operation": "set_expiry", "member_ids": []string{memberID},
			"values": map[string]any{"expires_at": time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)},
		}).data(t)
		freshID := e.str(fresh, "id")
		key := "idem-conflict-" + freshID
		if res := e.doWith("POST", "/api/v1/admin/batches", map[string]any{
			"preview_id": freshID, "confirm": true,
		}, map[string]string{"Idempotency-Key": key}); res.Status != 200 {
			t.Fatalf("first submit = %d %v", res.Status, res.Body)
		}
		other := e.do("POST", "/api/v1/admin/batches/preview", map[string]any{
			"operation": "set_expiry", "member_ids": []string{memberID},
			"values": map[string]any{"expires_at": time.Now().UTC().Add(72 * time.Hour).Format(time.RFC3339)},
		}).data(t)
		conflict := e.doWith("POST", "/api/v1/admin/batches", map[string]any{
			"preview_id": e.str(other, "id"), "confirm": true,
		}, map[string]string{"Idempotency-Key": key})
		if conflict.Status != http.StatusConflict || conflict.errCode(t) != domain.CodeIdempotencyKeyReused {
			t.Fatalf("idempotency reuse = %d %v", conflict.Status, conflict.Body)
		}
	})

	var agentToken string
	t.Run("agent_registration_and_usage", func(t *testing.T) {
		code := e.str(e.do("POST", "/api/v1/admin/servers/"+serverID+"/registration-codes", nil).data(t), "code")
		reg := e.do("POST", "/api/v1/agent/register", map[string]any{
			"code": code, "name": "agent-a", "version": "test",
		}).data(t)
		agentToken = e.str(reg, "token")
		agentHeader := map[string]string{"Authorization": "Bearer " + agentToken}

		// The real API has established authorization and binding history before
		// the sample starts; advance only the reporting clock, not that history.
		started := time.Now().UTC()
		now := started.Add(time.Minute)
		e.st.Now = func() time.Time { return now }
		batch := contracts.UsageBatch{
			SchemaVersion: contracts.SchemaVersion,
			BatchID:       "batch-" + randomHex(t, 4),
			Samples: []contracts.UsageSample{{
				NodeID: nodeID, MemberID: memberID, CoreGeneration: "1",
				StartedAt: now.Add(-time.Minute), EndedAt: now,
				UploadBytes: 1024, DownloadBytes: 2048,
			}},
		}
		first := e.doWith("POST", "/api/v1/agent/usage", batch, agentHeader).data(t)
		if first["status"] != "accepted" {
			t.Fatalf("first usage ack = %v", first)
		}
		duplicate := e.doWith("POST", "/api/v1/agent/usage", batch, agentHeader).data(t)
		if duplicate["status"] != "duplicate" {
			t.Fatalf("duplicate usage ack = %v", duplicate)
		}
		mismatched := batch
		mismatched.Samples = []contracts.UsageSample{{
			NodeID: nodeID, MemberID: memberID, CoreGeneration: "1",
			StartedAt: now.Add(-time.Minute), EndedAt: now,
			UploadBytes: 1, DownloadBytes: 1,
		}}
		res := e.doWith("POST", "/api/v1/agent/usage", mismatched, agentHeader)
		if res.Status != http.StatusConflict || res.errCode(t) != domain.CodeBatchPayloadMismatch {
			t.Fatalf("payload mismatch = %d %v", res.Status, res.Body)
		}
	})

	t.Run("snapshot_and_receipts", func(t *testing.T) {
		agentHeader := map[string]string{"Authorization": "Bearer " + agentToken}
		if _, err := e.st.BumpRevisions(context.Background(), nil); err != nil {
			t.Fatalf("bump: %v", err)
		}
		snap := e.doWith("GET", "/api/v1/agent/snapshot", nil, agentHeader).data(t)
		revision := int64(snap["revision"].(float64))
		instances, _ := snap["instances"].([]any)
		if len(instances) != 1 {
			t.Fatalf("instances = %v", snap["instances"])
		}
		if snap["expected_user_count"].(float64) != 1 {
			t.Fatalf("expected_user_count = %v", snap["expected_user_count"])
		}

		// Receipts for a revision that was never issued must be refused.
		forged := e.doWith("POST", "/api/v1/agent/applied", contracts.ApplyReport{
			ControlGeneration: fmt.Sprint(snap["control_generation"]),
			Revision:          revision + 500, State: "applied",
			AppliedAt: time.Now().UTC(),
		}, agentHeader)
		if forged.Status != http.StatusConflict || forged.errCode(t) != domain.CodeRevisionStale {
			t.Fatalf("forged revision = %d %v", forged.Status, forged.Body)
		}

		ok := e.doWith("POST", "/api/v1/agent/applied", contracts.ApplyReport{
			ControlGeneration: fmt.Sprint(snap["control_generation"]),
			Revision:          revision, State: "applied", AppliedAt: time.Now().UTC(),
			Results: []contracts.InstanceResult{{
				InstanceID: nodeID, State: "succeeded", AppliedUserCount: 1, OldConnectionsTerminated: true,
			}},
		}, agentHeader).data(t)
		if ok["accepted"] != true {
			t.Fatalf("receipt = %v", ok)
		}
		// The client's own revision AND generation determine the 204 response.
		query := fmt.Sprintf("/api/v1/agent/snapshot?revision=%d&control_generation=%s", revision, snap["control_generation"])
		noChange := e.doWith("GET", query, nil, agentHeader)
		changedGeneration := e.doWith("GET", fmt.Sprintf("/api/v1/agent/snapshot?revision=%d&control_generation=old-backup", revision), nil, agentHeader)
		if changedGeneration.Status != http.StatusOK {
			t.Fatalf("same revision with another generation = %d", changedGeneration.Status)
		}
		missingClientState := e.doWith("GET", "/api/v1/agent/snapshot", nil, agentHeader)
		if missingClientState.Status != http.StatusOK {
			t.Fatalf("missing client state must receive full snapshot: %d", missingClientState.Status)
		}
		if noChange.Status != http.StatusNoContent {
			t.Fatalf("unchanged snapshot = %d %v", noChange.Status, noChange.Body)
		}
	})

	t.Run("empty_authorization_snapshot", func(t *testing.T) {
		agentHeader := map[string]string{"Authorization": "Bearer " + agentToken}
		preview := e.do("POST", "/api/v1/admin/batches/preview", map[string]any{
			"operation": "set_active", "member_ids": []string{memberID},
			"values": map[string]any{"active": false},
		}).data(t)
		if res := e.doWith("POST", "/api/v1/admin/batches", map[string]any{
			"preview_id": e.str(preview, "id"), "confirm": true,
		}, map[string]string{"Idempotency-Key": "idem-empty-" + previewID}); res.Status != 200 {
			t.Fatalf("deactivate batch = %d %v", res.Status, res.Body)
		}
		snap := e.doWith("GET", "/api/v1/agent/snapshot", nil, agentHeader).data(t)
		if snap["expected_user_count"].(float64) != 0 {
			t.Fatalf("expected_user_count after deactivation = %v", snap["expected_user_count"])
		}
		instances, _ := snap["instances"].([]any)
		if len(instances) == 0 {
			t.Fatal("instances must still be delivered when the authorized set is empty")
		}
		users, ok := instances[0].(map[string]any)["users"].([]any)
		if !ok {
			t.Fatalf("users must be an empty array, not null: %v", instances[0])
		}
		if len(users) != 0 {
			t.Fatalf("users = %v", users)
		}
	})

	t.Run("recovery_mode_blocks_writes_and_snapshots", func(t *testing.T) {
		agentHeader := map[string]string{"Authorization": "Bearer " + agentToken}
		enter := e.do("POST", "/api/v1/admin/recovery-mode/enter", nil).data(t)
		if enter["recovery_mode"] != true {
			t.Fatalf("enter recovery = %v", enter)
		}
		blocked := e.do("POST", "/api/v1/admin/servers", map[string]any{"name": "srv-blocked"})
		if blocked.Status != http.StatusConflict || blocked.errCode(t) != domain.CodeRecoveryMode {
			t.Fatalf("write during recovery = %d %v", blocked.Status, blocked.Body)
		}
		if res := e.doWith("GET", "/api/v1/agent/snapshot", nil, agentHeader); res.Status != http.StatusConflict {
			t.Fatalf("snapshot during recovery = %d %v", res.Status, res.Body)
		}
		bad := e.do("POST", "/api/v1/admin/recovery-mode/exit", map[string]any{"confirm": "verified"})
		if bad.Status != http.StatusBadRequest {
			t.Fatalf("exit without checks = %d %v", bad.Status, bad.Body)
		}
		good := e.do("POST", "/api/v1/admin/recovery-mode/exit", map[string]any{
			"confirm": "verified", "agent_revisions_checked": true, "ledger_checked": true,
		}).data(t)
		if good["recovery_mode"] != false {
			t.Fatalf("exit recovery = %v", good)
		}
	})
}
