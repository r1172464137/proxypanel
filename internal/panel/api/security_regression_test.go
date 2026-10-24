package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/secrets"
)

func TestSecurityInvitationBindingAndQuota(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	sid := e.createServer(t, a, "invite-server")
	nid := e.createNode(t, a, sid, "invite-node", 20443)
	gid := e.createGroup(t, a, "invite-group", []string{nid})
	sub := e.createSubscription(t, a, map[string]any{"name": "invite-sub", "node_group_ids": []string{gid}, "quota_bytes": 1024, "default_valid_days": 1, "timezone": "UTC", "anchor_day": 1})
	inv := e.mustCall(t, a, "POST", "/api/v1/admin/invitations", map[string]any{"subscription_id": sub}).ok(t)
	created := e.mustCall(t, nil, "POST", "/api/v1/auth/redeem", map[string]any{"code": inv["code"], "username": "invite-user", "password": "invite-password-1"}).ok(t)
	mid := wantField(t, created, "id")
	for _, key := range []string{"period_start", "period_end", "expires_at", "bound_at"} {
		if created[key] == nil {
			t.Errorf("redeemed member missing %s", key)
		}
	}
	if n := e.activeCredentials(t, mid, nid); n != 1 {
		t.Errorf("credentials=%d, want 1 immediately after redemption", n)
	}
	if n := e.scalar(t, `SELECT count(*) FROM member_bindings WHERE member_id=$1 AND ended_at IS NULL`, mid); n != 1 {
		t.Errorf("binding history=%d", n)
	}
	// Keep the baseline exploit reachable even when the initial-credential assertion failed.
	m := e.loginAs(t, "invite-user", "invite-password-1")
	e.mustCall(t, m, "POST", "/api/v1/me/credentials/rotations", map[string]any{"scope": "all_nodes"}).ok(t)
	tok := e.mustCall(t, m, "GET", "/api/v1/me/subscription", nil).ok(t)
	agent := e.registerAgent(t, a, sid)
	now := time.Now().UTC()
	batch := contracts.UsageBatch{SchemaVersion: contracts.SchemaVersion, BatchID: "invite-quota", Samples: []contracts.UsageSample{{NodeID: nid, MemberID: mid, CoreGeneration: "1", StartedAt: now.Add(-time.Second), EndedAt: now, UploadBytes: 2048}}}
	e.mustCallWith(t, nil, "POST", "/api/v1/agent/usage", batch, map[string]string{"Authorization": "Bearer " + agent}).ok(t)
	status := e.mustCall(t, m, "GET", "/api/v1/me/status", nil).ok(t)
	if status["access"].(map[string]any)["access"] != false {
		t.Errorf("over-quota redeemed member still authorized")
	}
	res, err := e.call(nil, "GET", "/sub/"+tok["token"].(string), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.status != http.StatusForbidden {
		t.Errorf("over-quota subscription status=%d", res.status)
	}
	snap := e.agentSnapshot(t, agent)
	if snap["expected_user_count"].(float64) != 0 {
		t.Errorf("over-quota member remains in snapshot")
	}
}

func TestSecurityAdminPasswordResetRevokes(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	row := e.createMember(t, a, map[string]any{"username": "reset-user", "password": "reset-password-1"})
	id := wantField(t, row, "id")
	old := e.loginAs(t, "reset-user", "reset-password-1")
	v := e.memberVersion(t, a, id)
	e.mustCall(t, a, "PATCH", "/api/v1/admin/members/"+id, map[string]any{"version": v, "password": "reset-password-2"}).ok(t)
	e.mustCall(t, old, "GET", "/api/v1/auth/session", nil).expect(t, 401, "UNAUTHENTICATED")
	e.loginAs(t, "reset-user", "reset-password-2")
	// Every session of that member is revoked, not only the one the test kept
	// around: two concurrent sessions must both die on a reset.
	first := e.loginAs(t, "reset-user", "reset-password-2")
	second := e.loginAs(t, "reset-user", "reset-password-2")
	e.mustCall(t, a, "PATCH", "/api/v1/admin/members/"+id,
		map[string]any{"version": e.memberVersion(t, a, id), "password": "reset-password-3"}).ok(t)
	for i, sess := range []*acceptSession{first, second} {
		e.mustCall(t, sess, "GET", "/api/v1/auth/session", nil).expect(t, 401, "UNAUTHENTICATED")
		_ = i
	}
}

// The administrator reset path must also revoke sessions when the account is
// archived through DELETE, and when a batch deactivates the member.
func TestSecurityLoginCanonicalRateKey(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 11; i++ {
		res := e.mustCall(t, nil, "POST", "/api/v1/auth/login", map[string]any{"username": strings.Repeat(" ", i) + "missing-user", "password": "wrong"})
		want := 401
		if i == 10 {
			want = 429
		}
		if res.status != want {
			t.Fatalf("attempt %d status=%d want %d", i, res.status, want)
		}
	}
}

func TestSecurityAnonymousRedeemBudgetAndCheapReject(t *testing.T) {
	e := newEnv(t)
	t.Run("cheap-code-before-bcrypt", func(t *testing.T) {
		// bcrypt rejects >72 bytes; an unusable invitation must be rejected before bcrypt is reached.
		res := e.mustCall(t, nil, "POST", "/api/v1/auth/redeem", map[string]any{"code": "not-a-code", "username": "candidate", "password": strings.Repeat("p", 73)})
		if res.status != 400 || res.errMessage() != "invitation is not usable" {
			t.Fatalf("invalid invitation reached password hashing: status=%d message=%q", res.status, res.errMessage())
		}
	})
	t.Run("budget", func(t *testing.T) {
		limited := false
		for i := 0; i < 25; i++ {
			res := e.mustCall(t, nil, "POST", "/api/v1/auth/redeem", map[string]any{"code": "not-a-code", "username": "candidate", "password": "candidate-password-1"})
			if res.status == 429 {
				limited = true
				break
			}
		}
		if !limited {
			t.Fatal("anonymous redemption has no source budget")
		}
	})
}

func TestSecurityLoginCapacityIsolation(t *testing.T) {
	e := newEnv(t)
	// All these requests are one source. It must be stopped before allocating a table of usernames.
	for i := 0; i < limit.MaxLocalKeys; i++ {
		res := e.mustCall(t, nil, "POST", "/api/v1/auth/login", map[string]any{"username": fmt.Sprintf("absent-%d", i), "password": "wrong"})
		if res.status == 429 {
			break
		}
	}
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"admin-password-1"}`))
	req.RemoteAddr = "203.0.113.42:1234"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("other source correct login rejected: %d", rec.Code)
	}
}

func TestSecurityLoginOriginAndMediaType(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"username": "admin", "password": "admin-password-1"}
	e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body, map[string]string{"Origin": "https://foreign.example.invalid"}).expect(t, 403, "CSRF_FAILED")
	e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body, map[string]string{"Content-Type": "text/plain"}).expect(t, 400, "VALIDATION_FAILED")
	e.srv.Cfg.AllowedOrigins = "https://frontend.example.invalid"
	e.handler = e.srv.Handler()
	res := e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body, map[string]string{"Origin": "https://frontend.example.invalid", "Sec-Fetch-Site": "cross-site"})
	res.ok(t)
	if res.header.Get("Access-Control-Allow-Origin") != "https://frontend.example.invalid" {
		t.Fatal("legitimate cross-origin JSON login lost CORS")
	}
}

// The login origin check must accept the panel's own origin and reject every
// variant a browser can produce for a cross-site form, without trusting
// X-Forwarded-Proto or a non-canonical Origin spelling.
func TestSecurityLoginOriginMatrix(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"username": "admin", "password": "admin-password-1"}

	t.Run("canonical public url", func(t *testing.T) {
		e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body,
			map[string]string{"Origin": "http://127.0.0.1:8080"}).ok(t)
	})
	t.Run("format media type with charset", func(t *testing.T) {
		e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body,
			map[string]string{"Content-Type": "application/json; charset=utf-8"}).ok(t)
	})
	for name, origin := range map[string]string{
		"same host wrong scheme":  "https://127.0.0.1:8080",
		"foreign host":            "http://evil.example.invalid",
		"suffix of the real host": "http://127.0.0.1:8080.evil.invalid",
		"credentials in origin":   "http://user@127.0.0.1:8080",
		"origin with a path":      "http://127.0.0.1:8080/sub",
		"null origin":             "null",
		"not a url":               "127.0.0.1:8080",
	} {
		t.Run(name, func(t *testing.T) {
			e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body,
				map[string]string{"Origin": origin}).expect(t, 403, "CSRF_FAILED")
		})
	}
	t.Run("cross-site fetch metadata without origin", func(t *testing.T) {
		e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body,
			map[string]string{"Sec-Fetch-Site": "cross-site"}).expect(t, 403, "CSRF_FAILED")
	})
	t.Run("same-origin fetch metadata", func(t *testing.T) {
		e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body,
			map[string]string{"Sec-Fetch-Site": "same-origin"}).ok(t)
	})
	t.Run("forwarded proto is not trusted", func(t *testing.T) {
		e.mustCallWith(t, nil, "POST", "/api/v1/auth/login", body,
			map[string]string{"Origin": "https://127.0.0.1:8080", "X-Forwarded-Proto": "https"}).expect(t, 403, "CSRF_FAILED")
	})
}

func TestSecurityStrictJSONThroughPublicHandler(t *testing.T) {
	e := newEnv(t)
	for _, tail := range []string{`{"unknown":true}`, "garbage", strings.Repeat(" ", 1<<20)} {
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"admin-password-1"}`+tail))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Errorf("malformed/oversize request accepted: %d", rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Error("invalid request created a session")
		}
	}
}

// The whole request, not just the first decoder value, is bounded: a chunked
// body over the limit and a body with trailing whitespace/newlines must be
// decided by total size, and the exact limit must still be accepted.
func TestSecurityStrictJSONBodyBoundary(t *testing.T) {
	e := newEnv(t)
	prefix := `{"username":"admin","password":"admin-password-1"}`

	t.Run("chunked oversize body", func(t *testing.T) {
		body := io.MultiReader(
			strings.NewReader(prefix),
			strings.NewReader(strings.Repeat(" ", (1<<20)-len(prefix)+1)),
		)
		req := httptest.NewRequest("POST", "/api/v1/auth/login", body)
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = -1 // force chunked transfer encoding
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("chunked oversize body accepted: %d", rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Fatal("oversize request created a session")
		}
	})
	t.Run("trailing newline", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(prefix+"\n"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("a JSON document followed by whitespace must be accepted: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("second document", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(prefix+prefix))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("two JSON documents accepted: %d", rec.Code)
		}
	})
	t.Run("empty body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatalf("empty body = %d, want 400", rec.Code)
		}
	})
}

func TestSecurityInvitationClosedBindings(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	sub := e.createSubscription(t, a, map[string]any{"name": "closed", "allow_new_bindings": false})
	inv := e.mustCall(t, a, "POST", "/api/v1/admin/invitations", map[string]any{"subscription_id": sub}).ok(t)
	res := e.mustCall(t, nil, "POST", "/api/v1/auth/redeem", map[string]any{"code": inv["code"], "username": "closed-user", "password": "closed-password-1"})
	res.expect(t, 400, "VALIDATION_FAILED")
	if n := e.scalar(t, `SELECT count(*) FROM invitations WHERE used_at IS NOT NULL`); n != 0 {
		t.Fatal("rejected invitation was consumed")
	}
}

// A failed password reset must not commit the hash while leaving the old session live.
func TestSecurityAdminResetRollbackOnRevokeFailure(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	row := e.createMember(t, a, map[string]any{"username": "rollback-user", "password": "rollback-password-1"})
	id := wantField(t, row, "id")
	e.loginAs(t, "rollback-user", "rollback-password-1")
	ctx := context.Background()
	_, err := e.st.DB.ExecContext(ctx, `CREATE FUNCTION fail_session_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test revoke failure'; END $$; CREATE TRIGGER fail_revoke BEFORE UPDATE OF revoked_at ON sessions FOR EACH ROW EXECUTE FUNCTION fail_session_revoke()`)
	if err != nil {
		t.Fatal(err)
	}
	res := e.mustCall(t, a, "PATCH", "/api/v1/admin/members/"+id, map[string]any{"version": e.memberVersion(t, a, id), "password": "rollback-password-2"})
	if res.status != 500 {
		t.Errorf("reset with failed revocation status=%d", res.status)
	}
	m, err := e.st.GetMember(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := secrets.VerifyPassword(m.PasswordHash, "rollback-password-1")
	if !ok {
		t.Fatal("hash committed despite failed revocation")
	}
}

func TestSecurityRecoveryIdentityRescue(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	e.createMember(t, a, map[string]any{"username": "rescue", "password": "rescue-password-1"})
	old := e.loginAs(t, "rescue", "rescue-password-1")
	e.mustCall(t, a, "POST", "/api/v1/admin/recovery-mode/enter", nil).ok(t)
	fresh := e.loginAs(t, "rescue", "rescue-password-1")
	e.mustCall(t, fresh, "POST", "/api/v1/me/password", map[string]any{"current_password": "rescue-password-1", "new_password": "rescue-password-2"}).ok(t)
	e.mustCall(t, old, "GET", "/api/v1/auth/session", nil).expect(t, 401, "UNAUTHENTICATED")
	e.mustCall(t, fresh, "GET", "/api/v1/auth/session", nil).ok(t)
	e.mustCall(t, fresh, "POST", "/api/v1/me/subscription/token/rotations", nil).expect(t, 409, "RECOVERY_MODE")
	e.mustCall(t, fresh, "POST", "/api/v1/auth/logout", nil).ok(t)
	e.loginAs(t, "rescue", "rescue-password-2")
}
