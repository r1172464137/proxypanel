// Acceptance scenarios A01–A05 from docs/specs/acceptance.md, run through the
// frozen HTTP contract against a real PostgreSQL schema:
//
//	AC-AUTH-01/02  one invitation code is redeemable exactly once (concurrently)
//	AC-AUTH-03/04/08 member scope, session revocation, password change
//	AC-AUTH-05/06  the last usable administrator cannot be removed
//	AC-SUB-01..05  continuous inheritance, personal override, suspension,
//	               unbinding
//	AC-USAGE-03..08 subscription switch, rule/timezone edits, boundary splits,
//	               quota changes
//	AC-NODE-01..04 node group propagation, deduplication, no unauthorized node
//
// They need a real PostgreSQL instance because every guarantee is about
// transaction behaviour and persisted cycles. They are skipped unless
// PANEL_TEST_DATABASE_URL points at the documented isolated instance; newEnv
// (integration_test.go) creates and drops one schema per run.
//
// Batch atomicity and idempotency (AC-BATCH-01..08) are covered by
// TestPanelFlow in integration_test.go; the scenarios below only use the
// preview/submit flow as the transport for the changes the contract says must
// not be applied directly.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

// ─── request plumbing ──────────────────────────────────────────────────────

// acceptSession is one logged-in browser session (cookie + CSRF token). A nil
// session sends no credentials (public endpoints).
type acceptSession struct {
	cookie *http.Cookie
	csrf   string
}

// acceptResponse keeps the raw body as well as the decoded envelope so a
// failure message always shows what the panel actually answered.
type acceptResponse struct {
	status int
	header http.Header
	raw    string
	body   map[string]any
}

// call runs one request through the real handler. It never calls t.Fatalf, so
// it is safe to use from goroutines; callers assert on the result.
func (e *env) call(s *acceptSession, method, path string, body any, header map[string]string) (*acceptResponse, error) {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if s != nil {
		if s.cookie != nil {
			req.AddCookie(s.cookie)
		}
		if s.csrf != "" {
			req.Header.Set("X-CSRF-Token", s.csrf)
		}
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	out := &acceptResponse{status: rec.Code, header: rec.Header(), raw: rec.Body.String()}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	}
	return out, nil
}

func (e *env) mustCall(t *testing.T, s *acceptSession, method, path string, body any) *acceptResponse {
	t.Helper()
	return e.mustCallWith(t, s, method, path, body, nil)
}

// withVersion 给 PATCH/PUT 的 body 补上资源当前的 version。
//
// 服务端现在强制 PATCH/PUT 必须带 version（否则并发两次 PATCH 都能成功、
// 后写静默覆盖先写）。这些验收用例关心的是业务语义（权限、撤权、并发互斥），
// 不是版本协商本身，所以在这里统一补上；专门验证"缺 version 必须 400"的用例
// 不经过这个路径。
func (e *env) withVersion(t *testing.T, s *acceptSession, path string, body any) any {
	t.Helper()
	m, ok := body.(map[string]any)
	if !ok {
		return body
	}
	if _, has := m["version"]; has {
		return body
	}
	get := e.mustCall(t, s, "GET", path, nil)
	if get.status != http.StatusOK {
		return body
	}
	data, _ := get.body["data"].(map[string]any)
	version := int64(0)
	if v, ok := data["version"].(float64); ok {
		version = int64(v)
	} else {
		// 成员详情返回 {member, access}，版本在 member 里
		if inner, ok := data["member"].(map[string]any); ok {
			if v, ok := inner["version"].(float64); ok {
				version = int64(v)
			}
		}
	}
	if version <= 0 {
		return body
	}
	next := make(map[string]any, len(m)+1)
	for k, v := range m {
		next[k] = v
	}
	next["version"] = version
	return next
}

func (e *env) mustCallWith(t *testing.T, s *acceptSession, method, path string, body any, header map[string]string) *acceptResponse {
	t.Helper()
	if method == "PATCH" || method == "PUT" {
		body = e.withVersion(t, s, path, body)
	}
	res, err := e.call(s, method, path, body, header)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

// ok asserts a 2xx answer and returns the "data" object (empty when the
// endpoint answers with a list envelope).
func (r *acceptResponse) ok(t *testing.T) map[string]any {
	t.Helper()
	if r.status < 200 || r.status > 299 {
		t.Fatalf("expected success, got %d: %s", r.status, r.raw)
	}
	if data, isMap := r.body["data"].(map[string]any); isMap {
		return data
	}
	return map[string]any{}
}

func (r *acceptResponse) expect(t *testing.T, status int, code string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d: %s", r.status, status, r.raw)
	}
	if code != "" && r.errCode() != code {
		t.Fatalf("error code = %q, want %q: %s", r.errCode(), code, r.raw)
	}
}

func (r *acceptResponse) errCode() string {
	errObj, _ := r.body["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

func (r *acceptResponse) errMessage() string {
	errObj, _ := r.body["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	return msg
}

func (r *acceptResponse) listItems(t *testing.T) []any {
	t.Helper()
	items, isList := r.body["items"].([]any)
	if !isList {
		t.Fatalf("expected a list envelope, got %d: %s", r.status, r.raw)
	}
	return items
}

// ─── assertion helpers ─────────────────────────────────────────────────────

func wantString(t *testing.T, obj map[string]any, key, want string) {
	t.Helper()
	got, _ := obj[key].(string)
	if got != want {
		t.Fatalf("%s = %q, want %q (%v)", key, got, want, obj)
	}
}

func wantNumber(t *testing.T, obj map[string]any, key string, want float64) {
	t.Helper()
	got, _ := obj[key].(float64)
	if got != want {
		t.Fatalf("%s = %v, want %v (%v)", key, got, want, obj)
	}
}

func wantHeadline(t *testing.T, data map[string]any) string {
	t.Helper()
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatalf("response has no id: %v", data)
	}
	return id
}

func wantField(t *testing.T, data map[string]any, key string) string {
	t.Helper()
	v, _ := data[key].(string)
	if v == "" {
		t.Fatalf("response has no %q: %v", key, data)
	}
	return v
}

// fieldString returns a string field without asserting a specific value.
func fieldString(t *testing.T, data map[string]any, key string) string {
	t.Helper()
	v, _ := data[key].(string)
	if v == "" {
		t.Fatalf("response has no %q: %v", key, data)
	}
	return v
}

// ─── login and fixture helpers (all through the real API) ──────────────────

func (e *env) adminSession(t *testing.T) *acceptSession {
	t.Helper()
	e.login()
	return &acceptSession{cookie: e.cookie, csrf: e.csrf}
}

func (e *env) loginAs(t *testing.T, username, password string) *acceptSession {
	t.Helper()
	res := e.mustCall(t, nil, "POST", "/api/v1/auth/login", map[string]any{
		"username": username, "password": password,
	})
	data := res.ok(t)
	s := &acceptSession{csrf: wantField(t, data, "csrf_token")}
	for _, c := range (&http.Response{Header: res.header}).Cookies() {
		if c.Name == "pp_session" {
			s.cookie = c
		}
	}
	if s.cookie == nil {
		t.Fatalf("login of %q did not set the session cookie: %s", username, res.raw)
	}
	return s
}

func (e *env) createServer(t *testing.T, admin *acceptSession, name string) string {
	t.Helper()
	return wantHeadline(t, e.mustCall(t, admin, "POST", "/api/v1/admin/servers", map[string]any{
		"name": name, "address": "node.example.invalid", "region": "test",
	}).ok(t))
}

func (e *env) createNode(t *testing.T, admin *acceptSession, serverID, name string, port int) string {
	t.Helper()
	return wantHeadline(t, e.mustCall(t, admin, "POST", "/api/v1/admin/nodes", map[string]any{
		"name": name, "server_id": serverID, "host": name + ".example.invalid", "port": port,
		"listen": "0.0.0.0", "protocol": "vless",
		"stream_settings":   map[string]any{"network": "tcp", "security": "none"},
		"protocol_settings": map[string]any{"decryption": "none"},
		"client_settings":   map[string]any{},
	}).ok(t))
}

func (e *env) createGroup(t *testing.T, admin *acceptSession, name string, nodeIDs []string) string {
	t.Helper()
	return wantHeadline(t, e.mustCall(t, admin, "POST", "/api/v1/admin/node-groups", map[string]any{
		"name": name, "node_ids": nodeIDs,
	}).ok(t))
}

func (e *env) createSubscription(t *testing.T, admin *acceptSession, body map[string]any) string {
	t.Helper()
	return wantHeadline(t, e.mustCall(t, admin, "POST", "/api/v1/admin/subscriptions", body).ok(t))
}

func (e *env) createMember(t *testing.T, admin *acceptSession, body map[string]any) map[string]any {
	t.Helper()
	return e.mustCall(t, admin, "POST", "/api/v1/admin/members", body).ok(t)
}

// previewAndSubmit runs the frozen change flow: preview, then submit with the
// returned version stamps. It returns the submit payload.
func (e *env) previewAndSubmit(t *testing.T, admin *acceptSession, preview map[string]any) map[string]any {
	t.Helper()
	p := e.mustCall(t, admin, "POST", "/api/v1/admin/batches/preview", preview).ok(t)
	previewID := wantField(t, p, "id")
	submit := e.mustCallWith(t, admin, "POST", "/api/v1/admin/batches", map[string]any{
		"preview_id": previewID, "confirm": true,
		"rules_version": p["rules_version"], "member_set_digest": p["member_set_digest"],
	}, map[string]string{"Idempotency-Key": "accept-" + previewID})
	data := submit.ok(t)
	if data["state"] != "applied" {
		t.Fatalf("batch state = %v: %s", data["state"], submit.raw)
	}
	return data
}

// memberDetail returns the member row and the access decision the panel
// computed for it.
func (e *env) memberDetail(t *testing.T, admin *acceptSession, memberID string) (map[string]any, map[string]any) {
	t.Helper()
	data := e.mustCall(t, admin, "GET", "/api/v1/admin/members/"+memberID, nil).ok(t)
	member, _ := data["member"].(map[string]any)
	access, _ := data["access"].(map[string]any)
	if member == nil || access == nil {
		t.Fatalf("member detail has no member/access object: %v", data)
	}
	return member, access
}

func (e *env) findMemberID(t *testing.T, admin *acceptSession, username string) string {
	t.Helper()
	for _, raw := range e.mustCall(t, admin, "GET", "/api/v1/admin/members", nil).listItems(t) {
		m, _ := raw.(map[string]any)
		if name, _ := m["username"].(string); name == username {
			return wantHeadline(t, m)
		}
	}
	t.Fatalf("member %q not listed", username)
	return ""
}

func (e *env) registerAgent(t *testing.T, admin *acceptSession, serverID string) string {
	t.Helper()
	code := wantField(t, e.mustCall(t, admin, "POST",
		"/api/v1/admin/servers/"+serverID+"/registration-codes", nil).ok(t), "code")
	reg := e.mustCall(t, nil, "POST", "/api/v1/agent/register", map[string]any{
		"code": code, "name": "acceptance-agent", "version": "test",
	}).ok(t)
	return wantField(t, reg, "token")
}

func (e *env) agentSnapshot(t *testing.T, agentToken string) map[string]any {
	t.Helper()
	return e.mustCallWith(t, nil, "GET", "/api/v1/agent/snapshot", nil,
		map[string]string{"Authorization": "Bearer " + agentToken}).ok(t)
}

// snapshotUsers returns nodeID -> number of users in the agent snapshot.
func snapshotUsers(t *testing.T, snap map[string]any) map[string]int {
	t.Helper()
	out := map[string]int{}
	instances, _ := snap["instances"].([]any)
	for _, raw := range instances {
		inst, _ := raw.(map[string]any)
		nodeID, _ := inst["node_id"].(string)
		users, _ := inst["users"].([]any)
		out[nodeID] = len(users)
	}
	return out
}

// fetchSubscription calls the public subscription endpoint.
func (e *env) fetchSubscription(token, format string) (int, string) {
	req := httptest.NewRequest("GET", "/sub/"+token+"?format="+format, nil)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// outboundPorts returns the node ports emitted in the xray client output, one
// entry per outbound, so duplicates and unauthorized nodes are both visible.
func (e *env) outboundPorts(t *testing.T, token string) []int {
	t.Helper()
	status, raw := e.fetchSubscription(token, "xray")
	if status != http.StatusOK {
		t.Fatalf("subscription output = %d: %s", status, raw)
	}
	var doc struct {
		Outbounds []struct {
			Protocol string         `json:"protocol"`
			Settings map[string]any `json:"settings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("subscription output is not the xray JSON contract: %v (%s)", err, raw)
	}
	var ports []int
	for _, ob := range doc.Outbounds {
		if ob.Protocol == "freedom" || ob.Protocol == "blackhole" {
			continue
		}
		collectPorts(ob.Settings, &ports)
	}
	return ports
}

func collectPorts(v any, out *[]int) {
	switch node := v.(type) {
	case map[string]any:
		for key, value := range node {
			if key == "port" {
				if p, isNum := value.(float64); isNum {
					*out = append(*out, int(p))
				}
				continue
			}
			collectPorts(value, out)
		}
	case []any:
		for _, item := range node {
			collectPorts(item, out)
		}
	}
}

func countInts(list []int, want int) int {
	n := 0
	for _, v := range list {
		if v == want {
			n++
		}
	}
	return n
}

// ─── database assertions (read only) ───────────────────────────────────────

func (e *env) activeCredentials(t *testing.T, memberID, nodeID string) int {
	t.Helper()
	q := `SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active'`
	args := []any{memberID}
	if nodeID != "" {
		q += ` AND node_id=$2`
		args = append(args, nodeID)
	}
	var n int
	if err := e.st.DB.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	return n
}

func (e *env) activeAdmins(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.st.DB.QueryRowContext(context.Background(),
		`SELECT count(*) FROM members WHERE role='admin' AND active AND NOT archived`).Scan(&n); err != nil {
		t.Fatalf("count active admins: %v", err)
	}
	return n
}

func (e *env) scalar(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.DB.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// ─── AC-AUTH-01/02 ─────────────────────────────────────────────────────────

// AC-AUTH-01/02: one invitation code creates exactly one member even when it is
// redeemed concurrently, and a used, expired or revoked code is refused with an
// identical body so the endpoint never reveals which state a code is in.
func TestAcceptanceInvitationRedeemsOnce(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)

	created := e.mustCall(t, admin, "POST", "/api/v1/admin/invitations", map[string]any{}).ok(t)
	code := wantField(t, created, "code")

	const attempts = 12
	type outcome struct {
		status int
		code   string
		msg    string
		raw    string
	}
	results := make([]outcome, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := e.call(nil, "POST", "/api/v1/auth/redeem", map[string]any{
				"code": code, "username": fmt.Sprintf("invitee-%02d", i), "password": "invitee-password-1",
			}, nil)
			if err != nil {
				results[i] = outcome{status: -1, msg: err.Error()}
				return
			}
			results[i] = outcome{status: res.status, code: res.errCode(), msg: res.errMessage(), raw: res.raw}
		}(i)
	}
	wg.Wait()

	successes := 0
	for i, r := range results {
		switch r.status {
		case http.StatusCreated:
			successes++
		case http.StatusBadRequest:
			if r.code != "VALIDATION_FAILED" {
				t.Fatalf("attempt %d rejected with %q, want the uniform VALIDATION_FAILED: %s", i, r.code, r.raw)
			}
		default:
			t.Fatalf("attempt %d returned status %d: %s", i, r.status, r.raw)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent redemptions succeeded %d times, want exactly 1 (%v)", successes, results)
	}
	if n := e.scalar(t, `SELECT count(*) FROM members WHERE username LIKE 'invitee-%'`); n != 1 {
		t.Fatalf("members created by the invitation = %d, want 1", n)
	}

	// An expired code, a revoked code and the already used code must be
	// indistinguishable to the caller.
	past := time.Now().UTC().Add(-time.Hour)
	expiredCode := wantField(t, e.mustCall(t, admin, "POST", "/api/v1/admin/invitations",
		map[string]any{"expires_at": past}).ok(t), "code")
	revoked := e.mustCall(t, admin, "POST", "/api/v1/admin/invitations", map[string]any{}).ok(t)
	revokedCode := wantField(t, revoked, "code")
	e.mustCall(t, admin, "POST", "/api/v1/admin/invitations/"+wantHeadline(t, revoked)+"/revoke", nil).ok(t)

	refusals := []outcome{}
	for i, candidate := range []string{code, expiredCode, revokedCode} {
		res := e.mustCall(t, nil, "POST", "/api/v1/auth/redeem", map[string]any{
			"code": candidate, "username": fmt.Sprintf("late-%02d", i), "password": "late-password-1",
		})
		if res.status != http.StatusBadRequest {
			t.Fatalf("code %d was accepted (%d): %s", i, res.status, res.raw)
		}
		refusals = append(refusals, outcome{status: res.status, code: res.errCode(), msg: res.errMessage()})
	}
	for i := 1; i < len(refusals); i++ {
		if refusals[i].code != refusals[0].code || refusals[i].msg != refusals[0].msg {
			t.Fatalf("used/expired/revoked refusals differ: %q %q vs %q %q",
				refusals[0].code, refusals[0].msg, refusals[i].code, refusals[i].msg)
		}
	}
	if n := e.scalar(t, `SELECT count(*) FROM members WHERE username LIKE 'late-%'`); n != 0 {
		t.Fatalf("refused redemptions created %d memberships", n)
	}
}

// ─── AC-AUTH-03/04/08 ──────────────────────────────────────────────────────

// AC-AUTH-03/04/08: the member surface cannot reach another member's data or an
// admin endpoint, deactivation invalidates the live session and the proxy
// credentials, and a password change revokes every other session.
func TestAcceptanceSessionRevocationAndScope(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)

	serverID := e.createServer(t, admin, "srv-session")
	nodeID := e.createNode(t, admin, serverID, "node-session", 20443)
	groupID := e.createGroup(t, admin, "group-session", []string{nodeID})
	subID := e.createSubscription(t, admin, map[string]any{
		"name": "sub-session", "node_group_ids": []string{groupID}, "quota_bytes": int64(1) << 32,
		"timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true,
	})
	memberID := wantHeadline(t, e.createMember(t, admin, map[string]any{
		"username": "sess-user", "password": "sess-password-1", "role": "member", "subscription_id": subID,
	}))
	e.createMember(t, admin, map[string]any{
		"username": "sess-other", "password": "other-password-1", "role": "member", "subscription_id": subID,
	})

	sess := e.loginAs(t, "sess-user", "sess-password-1")
	other := e.loginAs(t, "sess-other", "other-password-1")
	e.mustCall(t, sess, "GET", "/api/v1/auth/session", nil).ok(t)

	// AC-AUTH-03: a member session cannot use the admin surface, and another
	// member's session id cannot be revoked through the member endpoint.
	e.mustCall(t, sess, "GET", "/api/v1/admin/members", nil).expect(t, http.StatusForbidden, "FORBIDDEN")
	var otherSessionID string
	for _, raw := range e.mustCall(t, other, "GET", "/api/v1/me/sessions", nil).listItems(t) {
		if row, isMap := raw.(map[string]any); isMap {
			if id, _ := row["id"].(string); id != "" {
				otherSessionID = id
			}
		}
	}
	if otherSessionID == "" {
		t.Fatal("the other member has no listed session")
	}
	e.mustCall(t, sess, "DELETE", "/api/v1/me/sessions/"+otherSessionID, nil).
		expect(t, http.StatusNotFound, "NOT_FOUND")
	e.mustCall(t, other, "GET", "/api/v1/auth/session", nil).ok(t)

	// AC-AUTH-04: deactivation revokes the live session and the proxy
	// credentials of the member, in the same request.
	if n := e.activeCredentials(t, memberID, nodeID); n != 1 {
		t.Fatalf("active credentials before deactivation = %d, want 1", n)
	}
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+memberID, map[string]any{"active": false}).ok(t)
	e.mustCall(t, sess, "GET", "/api/v1/auth/session", nil).expect(t, http.StatusUnauthorized, "UNAUTHENTICATED")
	if n := e.activeCredentials(t, memberID, ""); n != 0 {
		t.Fatalf("active credentials after deactivation = %d, want 0", n)
	}
	if n := e.scalar(t, `SELECT count(*) FROM sessions WHERE member_id=$1 AND revoked_at IS NOT NULL`, memberID); n == 0 {
		t.Fatal("deactivation did not revoke the member sessions")
	}

	// AC-AUTH-08: a password change revokes the other sessions and keeps the
	// session that changed it.
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+memberID, map[string]any{"active": true}).ok(t)
	changer := e.loginAs(t, "sess-user", "sess-password-1")
	victim := e.loginAs(t, "sess-user", "sess-password-1")
	e.mustCall(t, changer, "POST", "/api/v1/me/password", map[string]any{
		"current_password": "sess-password-1", "new_password": "sess-password-2",
	}).ok(t)
	e.mustCall(t, changer, "GET", "/api/v1/auth/session", nil).ok(t)
	e.mustCall(t, victim, "GET", "/api/v1/auth/session", nil).expect(t, http.StatusUnauthorized, "UNAUTHENTICATED")
	e.loginAs(t, "sess-user", "sess-password-2")

	if n := e.scalar(t, `SELECT count(*) FROM audit_logs WHERE action='password.change' AND target_id=$1`, memberID); n == 0 {
		t.Fatal("the password change was not audited")
	}
	if n := e.scalar(t, `SELECT count(*) FROM audit_logs WHERE detail::text LIKE '%sess-password-2%'`); n != 0 {
		t.Fatalf("audit rows leak the new password (%d rows)", n)
	}
}

// ─── AC-AUTH-05/06 ─────────────────────────────────────────────────────────

// AC-AUTH-05/06: concurrent demote/disable/archive attempts against the only
// usable administrator all fail with LAST_ADMIN, and with two administrators the
// last one is still protected once the other is disabled.
func TestAcceptanceLastAdminProtection(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)
	adminID := e.findMemberID(t, admin, "admin")

	if n := e.activeAdmins(t); n != 1 {
		t.Fatalf("fixture has %d active admins, want the single bootstrap admin", n)
	}

	const attempts = 20
	// PATCH 现在强制带 version。这些尝试**全部会被 LAST_ADMIN 拒绝**、不写库，
	// 所以 version 不会变；给所有 goroutine 同一个初始 version 即可。
	adminVersion := e.memberVersion(t, admin, adminID)
	statuses := make([]int, attempts)
	codes := make([]string, attempts)
	raws := make([]string, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var method, path string
			var body any
			switch i % 3 {
			case 0:
				method, path, body = "PATCH", "/api/v1/admin/members/"+adminID, map[string]any{"role": "member", "version": adminVersion}
			case 1:
				method, path, body = "PATCH", "/api/v1/admin/members/"+adminID, map[string]any{"active": false, "version": adminVersion}
			default:
				method, path = "DELETE", "/api/v1/admin/members/"+adminID
			}
			res, err := e.call(admin, method, path, body, nil)
			if err != nil {
				statuses[i], raws[i] = -1, err.Error()
				return
			}
			statuses[i], codes[i], raws[i] = res.status, res.errCode(), res.raw
		}(i)
	}
	wg.Wait()

	for i := range statuses {
		if statuses[i] != http.StatusConflict || codes[i] != "LAST_ADMIN" {
			t.Fatalf("attempt %d = %d %q, want 409 LAST_ADMIN: %s", i, statuses[i], codes[i], raws[i])
		}
	}
	if n := e.activeAdmins(t); n != 1 {
		t.Fatalf("active admins after the concurrent attempts = %d, want 1", n)
	}
	if n := e.scalar(t, `SELECT count(*) FROM members WHERE id=$1 AND role='admin' AND active AND NOT archived`, adminID); n != 1 {
		t.Fatal("the protected administrator was demoted, disabled or archived")
	}

	// When a second administrator exists it can be disabled, but the remaining
	// one stays protected whatever the requested change is.
	secondID := wantHeadline(t, e.createMember(t, admin, map[string]any{
		"username": "admin-two", "password": "admin-two-password-1", "role": "admin",
	}))
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+secondID, map[string]any{"active": false}).ok(t)
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+adminID, map[string]any{"active": false}).
		expect(t, http.StatusConflict, "LAST_ADMIN")
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+adminID, map[string]any{"role": "member"}).
		expect(t, http.StatusConflict, "LAST_ADMIN")
	e.mustCall(t, admin, "DELETE", "/api/v1/admin/members/"+adminID, nil).expect(t, http.StatusConflict, "LAST_ADMIN")
	if n := e.activeAdmins(t); n != 1 {
		t.Fatalf("active admins after the sequential attempts = %d, want 1", n)
	}
}

// Two usable admins target EACH OTHER, including ordinary/batch races. Holding
// control_state until both authenticated requests are waiting makes the old
// count-outside-the-transaction TOCTOU reproducible without sleeps in production.
func TestAcceptanceTwoAdminsConcurrentCrossChanges(t *testing.T) {
	_ = testDSN(t) // skip the scenario itself when no PostgreSQL fixture is supplied
	ops := []string{"demote", "disable", "archive", "batch-disable"}
	for i, first := range ops {
		for _, second := range ops[i:] {
			t.Run(first+"/"+second, func(t *testing.T) {
				e := newEnv(t)
				one := e.adminSession(t)
				oneID := e.findMemberID(t, one, "admin")
				twoID := wantHeadline(t, e.createMember(t, one, map[string]any{
					"username": "admin-two", "password": "admin-two-password-1", "role": "admin",
				}))
				two := e.loginAs(t, "admin-two", "admin-two-password-1")
				if n := e.activeAdmins(t); n != 2 {
					t.Fatalf("fixture has %d usable admins, want 2", n)
				}
				type change struct {
					session      *acceptSession
					method, path string
					body         any
					header       map[string]string
				}
				makeChange := func(op string, session *acceptSession, target string) change {
					c := change{session: session, method: "PATCH", path: "/api/v1/admin/members/" + target}
					switch op {
					case "demote":
						// PATCH 强制带 version；该变更被 LAST_ADMIN 拒绝时不写库，version 不变。
						c.body = map[string]any{"role": "member", "version": e.memberVersion(t, session, target)}
					case "disable":
						c.body = map[string]any{"active": false, "version": e.memberVersion(t, session, target)}
					case "archive":
						c.method = "DELETE"
					case "batch-disable":
						p := e.mustCall(t, session, "POST", "/api/v1/admin/batches/preview", map[string]any{
							"operation": "set_active", "member_ids": []string{target},
							"values": map[string]any{"active": false}, "reason": "cross-admin race",
						}).ok(t)
						c.method, c.path = "POST", "/api/v1/admin/batches"
						c.body = map[string]any{"preview_id": p["id"], "confirm": true,
							"rules_version": p["rules_version"], "member_set_digest": p["member_set_digest"]}
						c.header = map[string]string{"Idempotency-Key": "cross-" + wantField(t, p, "id")}
					}
					return c
				}
				changes := []change{makeChange(first, one, twoID), makeChange(second, two, oneID)}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				gate, err := e.st.DB.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer gate.Rollback()
				var gatePID, revision int
				if err := gate.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&gatePID); err != nil {
					t.Fatal(err)
				}
				if err := gate.QueryRowContext(ctx, `SELECT revision_counter FROM control_state WHERE id=1 FOR UPDATE`).Scan(&revision); err != nil {
					t.Fatal(err)
				}
				var results [2]*acceptResponse
				var callErrors [2]error
				var wg sync.WaitGroup
				for i, c := range changes {
					wg.Add(1)
					go func(i int, c change) {
						defer wg.Done()
						results[i], callErrors[i] = e.call(c.session, c.method, c.path, c.body, c.header)
					}(i, c)
				}
				// Include queued waiters (a later waiter may be blocked by the
				// first waiter rather than directly by our gate transaction).
				waiters := 0
				for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
					err = e.st.DB.QueryRowContext(ctx, `WITH RECURSIVE blocked(pid) AS (
						SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
						UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))
					) SELECT count(*) FROM blocked`, gatePID).Scan(&waiters)
					if err != nil || waiters >= 2 {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				commitErr := gate.Commit() // always release before waiting/asserting
				wg.Wait()
				if err != nil || commitErr != nil || waiters != 2 {
					t.Fatalf("did not synchronize both mutations: waiters=%d, poll=%v, release=%v", waiters, err, commitErr)
				}
				succeeded, refused := 0, 0
				for i, res := range results {
					if callErrors[i] != nil {
						t.Fatal(callErrors[i])
					}
					if res.status == http.StatusOK {
						succeeded++
						continue
					}
					// A batch queued behind a committed change must re-preview;
					// an ordinary write must fail the transactional LAST_ADMIN guard.
					if res.status != http.StatusConflict || (res.errCode() != "LAST_ADMIN" &&
						!(changes[i].path == "/api/v1/admin/batches" && res.errCode() == "PREVIEW_STALE")) {
						t.Fatalf("cross-change %d = %d: %s", i, res.status, res.raw)
					}
					refused++
				}
				if succeeded != 1 || refused != 1 || e.activeAdmins(t) != 1 {
					t.Fatalf("cross-changes: succeeded=%d refused=%d active-admins=%d; want 1/1/1", succeeded, refused, e.activeAdmins(t))
				}
			})
		}
	}
}

// ─── AC-SUB-01..05 ─────────────────────────────────────────────────────────

// AC-SUB-01..05: a member continuously inherits its subscription, a personal
// override replaces only the quota value, neither override nor membership keeps
// proxy access alive for a suspended account, and an unbound member can still
// sign in and read its history.
func TestAcceptanceInheritanceAndOverrideCannotBypass(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)

	serverID := e.createServer(t, admin, "srv-sub")
	nodeID := e.createNode(t, admin, serverID, "node-sub", 20443)
	groupID := e.createGroup(t, admin, "group-sub", []string{nodeID})
	subID := e.createSubscription(t, admin, map[string]any{
		"name": "sub-inherit", "node_group_ids": []string{groupID}, "quota_bytes": int64(10) << 30,
		"timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true,
	})
	memberID := wantHeadline(t, e.createMember(t, admin, map[string]any{
		"username": "inherit-user", "password": "inherit-password-1", "role": "member", "subscription_id": subID,
	}))
	agentToken := e.registerAgent(t, admin, serverID)
	memberSess := e.loginAs(t, "inherit-user", "inherit-password-1")
	token := wantField(t, e.mustCall(t, memberSess, "GET", "/api/v1/me/subscription", nil).ok(t), "token")

	// AC-SUB-01: raising the subscription quota reaches the bound member without
	// rebinding (inheritance, not a copy).
	raised := int64(20) << 30
	e.previewAndSubmit(t, admin, map[string]any{
		"operation": "update_subscription",
		"values":    map[string]any{"id": subID, "patch": map[string]any{"quota_bytes": raised}},
		"reason":    "raise the inherited quota",
	})
	_, access := e.memberDetail(t, admin, memberID)
	wantNumber(t, access, "effective_quota_bytes", float64(raised))
	wantString(t, access, "quota_source", "subscription")

	// AC-SUB-02/03: the override wins, clearing it restores the inherited value.
	override := int64(5) << 30
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+memberID, map[string]any{
		"quota_override_bytes": override,
	}).ok(t)
	_, access = e.memberDetail(t, admin, memberID)
	wantNumber(t, access, "effective_quota_bytes", float64(override))
	wantString(t, access, "quota_source", "override")
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+memberID, map[string]any{
		"quota_override_bytes": nil,
	}).ok(t)
	_, access = e.memberDetail(t, admin, memberID)
	wantNumber(t, access, "effective_quota_bytes", float64(raised))
	wantString(t, access, "quota_source", "subscription")

	// AC-SUB-04: a suspended account keeps no proxy access even though it still
	// carries a personal quota override.
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+memberID, map[string]any{
		"quota_override_bytes": override,
	}).ok(t)
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+memberID, map[string]any{"active": false}).ok(t)
	member, access := e.memberDetail(t, admin, memberID)
	if access["access"] != false {
		t.Fatalf("a suspended member still has access: %v", access)
	}
	wantString(t, access, "proxy_access", "account_blocked")
	if nodes, _ := access["allowed_node_ids"].([]any); len(nodes) != 0 {
		t.Fatalf("a suspended member still has allowed nodes: %v", nodes)
	}
	if member["quota_override_enabled"] != true {
		t.Fatalf("the override was not preserved for the suspension check: %v", member)
	}
	if status, raw := e.fetchSubscription(token, "xray"); status != http.StatusForbidden {
		t.Fatalf("suspended member subscription output = %d, want 403: %s", status, raw)
	}
	snap := e.agentSnapshot(t, agentToken)
	if users := snapshotUsers(t, snap); users[nodeID] != 0 {
		t.Fatalf("agent snapshot still carries %d users for the suspended member", users[nodeID])
	}
	if count, _ := snap["expected_user_count"].(float64); count != 0 {
		t.Fatalf("expected_user_count = %v, want 0", snap["expected_user_count"])
	}

	// AC-SUB-05: an unbound member can still sign in and read its history, but
	// has no proxy access and no node output.
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+memberID, map[string]any{"active": true}).ok(t)
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/members/"+memberID, map[string]any{"subscription_id": nil}).ok(t)
	memberSess = e.loginAs(t, "inherit-user", "inherit-password-1")
	status := e.mustCall(t, memberSess, "GET", "/api/v1/me/status", nil).ok(t)
	statusAccess, _ := status["access"].(map[string]any)
	if statusAccess == nil {
		t.Fatalf("me/status has no access object: %v", status)
	}
	wantString(t, statusAccess, "proxy_access", "no_subscription")
	usageBody := e.mustCall(t, memberSess, "GET", "/api/v1/me/usage", nil)
	if usageBody.status != http.StatusOK {
		t.Fatalf("unbound member usage = %d: %s", usageBody.status, usageBody.raw)
	}
	if _, isMap := usageBody.body["items"].(map[string]any); !isMap {
		t.Fatalf("usage envelope = %s", usageBody.raw)
	}
	if code, raw := e.fetchSubscription(token, "xray"); code != http.StatusForbidden {
		t.Fatalf("unbound member subscription output = %d, want 403: %s", code, raw)
	}
}

// ─── AC-USAGE-03 ───────────────────────────────────────────────────────────

// AC-USAGE-03: switching a member from one subscription to another keeps the
// subscription token, the running cycle and the accounted usage; the new rule
// takes over at the next boundary.
func TestAcceptanceSwitchSubscriptionKeepsTokenUsageAndCycle(t *testing.T) {
	e := newEnv(t)
	// Keep authentication valid while the business clock crosses a month.
	e.srv.Cfg.SessionTTL = 90 * 24 * time.Hour
	admin := e.adminSession(t)

	serverID := e.createServer(t, admin, "srv-switch")
	nodeID := e.createNode(t, admin, serverID, "node-switch", 20443)
	groupID := e.createGroup(t, admin, "group-switch", []string{nodeID})
	subA := e.createSubscription(t, admin, map[string]any{
		"name": "sub-switch-a", "node_group_ids": []string{groupID}, "quota_bytes": int64(1) << 30,
		"timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true,
	})
	subB := e.createSubscription(t, admin, map[string]any{
		"name": "sub-switch-b", "node_group_ids": []string{groupID}, "quota_bytes": int64(2) << 30,
		"timezone": "Asia/Tokyo", "anchor_day": 15, "allow_new_bindings": true, "access_enabled": true,
	})
	memberID := wantHeadline(t, e.createMember(t, admin, map[string]any{
		"username": "switch-user", "password": "switch-password-1", "role": "member", "subscription_id": subA,
	}))
	agentToken := e.registerAgent(t, admin, serverID)

	// Freeze only after PostgreSQL has timestamped real authorization history.
	started := time.Now().UTC()
	now := started.Add(2 * time.Minute)
	e.st.Now = func() time.Time { return now }
	status, err := e.call(nil, "POST", "/api/v1/agent/usage", contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion, BatchID: "accept-switch-" + randomHex(t, 4),
		Samples: []contracts.UsageSample{{
			NodeID: nodeID, MemberID: memberID, CoreGeneration: "1",
			StartedAt: now.Add(-2 * time.Minute), EndedAt: now,
			UploadBytes: 400, DownloadBytes: 600,
		}},
	}, map[string]string{"Authorization": "Bearer " + agentToken})
	if err != nil {
		t.Fatalf("submit usage: %v", err)
	}
	if ack := status.ok(t); ack["status"] != "accepted" {
		t.Fatalf("usage ack = %v", ack)
	}

	memberSess := e.loginAs(t, "switch-user", "switch-password-1")
	tokenBefore := wantField(t, e.mustCall(t, memberSess, "GET", "/api/v1/me/subscription", nil).ok(t), "token")

	member0, access0 := e.memberDetail(t, admin, memberID)
	start0, _ := member0["period_start"].(string)
	end0, _ := member0["period_end"].(string)
	if start0 == "" || end0 == "" {
		t.Fatalf("the member has no persisted cycle: %v", member0)
	}
	wantNumber(t, access0, "used_bytes", 1000)

	// Traffic measured under A will arrive only after switching to B.
	lateStart := now
	now = now.Add(2 * time.Minute)
	// The batch flow is the frozen way to change a bound member's plan.
	e.previewAndSubmit(t, admin, map[string]any{
		"operation": "assign_subscription", "member_ids": []string{memberID},
		"values": map[string]any{"subscription_id": subB}, "reason": "plan change",
	})

	e.mustCallWith(t, nil, "POST", "/api/v1/agent/usage", contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion, BatchID: "accept-switch-late-" + randomHex(t, 4),
		Samples: []contracts.UsageSample{{
			NodeID: nodeID, MemberID: memberID, CoreGeneration: "1",
			StartedAt: lateStart, EndedAt: lateStart.Add(time.Minute),
			UploadBytes: 100, DownloadBytes: 200,
		}},
	}, map[string]string{"Authorization": "Bearer " + agentToken}).ok(t)
	member1, access1 := e.memberDetail(t, admin, memberID)
	wantString(t, member1, "subscription_id", subB)
	if got, _ := member1["period_start"].(string); got != start0 {
		t.Fatalf("period_start moved on a subscription switch: %q -> %q", start0, got)
	}
	if got, _ := member1["period_end"].(string); got != end0 {
		t.Fatalf("period_end moved on a subscription switch: %q -> %q", end0, got)
	}
	wantNumber(t, access1, "used_bytes", 1300)
	wantString(t, access1, "quota_source", "subscription")
	wantNumber(t, access1, "effective_quota_bytes", float64(int64(2)<<30))

	tokenAfter := wantField(t, e.mustCall(t, memberSess, "GET", "/api/v1/me/subscription", nil).ok(t), "token")
	if tokenAfter != tokenBefore {
		t.Fatalf("the subscription token changed on a plan switch: %q -> %q", tokenBefore, tokenAfter)
	}
	if code, raw := e.fetchSubscription(tokenBefore, "xray"); code != http.StatusOK {
		t.Fatalf("the unchanged subscription link stopped working: %d %s", code, raw)
	}

	pending, _ := member1["pending_period_rule"].(map[string]any)
	if pending == nil {
		t.Fatalf("the new cycle rule was not scheduled for the next boundary: %v", member1)
	}
	wantNumber(t, pending, "anchor_day", 15)
	wantString(t, pending, "timezone", "Asia/Tokyo")

	// At the next boundary the new subscription rule is used and the cycle starts
	// from an empty ledger slice; the closed cycle keeps its rows. The boundary is
	// crossed by advancing the panel clock (marked clock injection: the test does
	// not claim a natural month of waiting).
	fakeNow, err := time.Parse(time.RFC3339Nano, end0)
	if err != nil {
		t.Fatal(err)
	}
	e.st.Now = func() time.Time { return fakeNow }
	// Product-status GET is read-only; agent snapshot is an authoritative
	// production path that advances the cycle and publishes authorization.
	e.agentSnapshot(t, agentToken)
	member2, access2 := e.memberDetail(t, admin, memberID)
	// The first new-rule cycle is contiguous, never backdated into the closed
	// cycle. Its end is the next Tokyo day-15 midnight, independently stated.
	wantStart := fakeNow
	wantEnd := time.Date(fakeNow.Year(), fakeNow.Month(), 14, 15, 0, 0, 0, time.UTC)
	wantString(t, member2, "period_end", wantEnd.Format(time.RFC3339))
	gotStart, err := time.Parse(time.RFC3339Nano, fieldString(t, member2, "period_start"))
	if err != nil {
		t.Fatalf("parse period_start: %v", err)
	}
	if !gotStart.Equal(wantStart) {
		t.Fatalf("new cycle start = %s, want %s (new subscription rule)", gotStart, wantStart)
	}
	wantString(t, member2, "period_timezone", "Asia/Tokyo")
	wantNumber(t, member2, "period_anchor_day", 15)
	wantNumber(t, access2, "used_bytes", 0)
	if member2["pending_period_rule"] != nil {
		t.Fatalf("the applied rule is still pending: %v", member2["pending_period_rule"])
	}
	closedStart, err := time.Parse(time.RFC3339Nano, start0)
	if err != nil {
		t.Fatalf("parse the previous period start: %v", err)
	}
	if n := e.scalar(t, `SELECT count(*) FROM usage_entries WHERE member_id=$1 AND period_start=$2`,
		memberID, closedStart); n != 2 {
		t.Fatalf("the closed cycle lost its ledger rows (%d)", n)
	}
	if n := e.scalar(t, `SELECT COALESCE(sum(up_bytes+down_bytes),0) FROM usage_entries WHERE member_id=$1 AND period_start=$2`, memberID, closedStart); n != 1300 {
		t.Fatalf("subscription switch/rollover changed closed-cycle bytes: %d", n)
	}
}

// ─── AC-USAGE-04/05 ────────────────────────────────────────────────────────

// AC-USAGE-04/05: editing the cycle rule (here the timezone) of a subscription
// keeps the running cycle of every bound member and never rewrites the accounted
// usage of that cycle.
func TestAcceptanceSubscriptionRuleEditKeepsRunningCycle(t *testing.T) {
	e := newEnv(t)
	e.srv.Cfg.SessionTTL = 90 * 24 * time.Hour // fixture survives the injected boundary
	admin := e.adminSession(t)

	serverID := e.createServer(t, admin, "srv-rule")
	nodeID := e.createNode(t, admin, serverID, "node-rule", 20443)
	groupID := e.createGroup(t, admin, "group-rule", []string{nodeID})
	subID := e.createSubscription(t, admin, map[string]any{
		"name": "sub-rule", "node_group_ids": []string{groupID}, "quota_bytes": int64(1) << 30,
		"timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true,
	})
	memberID := wantHeadline(t, e.createMember(t, admin, map[string]any{
		"username": "rule-user", "password": "rule-password-1", "role": "member", "subscription_id": subID,
	}))
	agentToken := e.registerAgent(t, admin, serverID)

	started := time.Now().UTC()
	now := started.Add(2 * time.Minute)
	e.st.Now = func() time.Time { return now }
	if res := e.mustCallWith(t, nil, "POST", "/api/v1/agent/usage", contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion, BatchID: "accept-rule-" + randomHex(t, 4),
		Samples: []contracts.UsageSample{{
			NodeID: nodeID, MemberID: memberID, CoreGeneration: "1",
			StartedAt: now.Add(-2 * time.Minute), EndedAt: now,
			UploadBytes: 300, DownloadBytes: 700,
		}},
	}, map[string]string{"Authorization": "Bearer " + agentToken}); res.status != http.StatusOK {
		t.Fatalf("submit usage = %d: %s", res.status, res.raw)
	}

	member0, access0 := e.memberDetail(t, admin, memberID)
	start0, _ := member0["period_start"].(string)
	end0, _ := member0["period_end"].(string)
	wantNumber(t, access0, "used_bytes", 1000)

	// A bound subscription edit must go through the preview flow, and the refused
	// direct PATCH must not be partially applied.
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/subscriptions/"+subID, map[string]any{
		"timezone": "Asia/Tokyo",
	}).expect(t, http.StatusConflict, "PREVIEW_REQUIRED")
	wantString(t, e.mustCall(t, admin, "GET", "/api/v1/admin/subscriptions/"+subID, nil).ok(t), "timezone", "UTC")
	e.previewAndSubmit(t, admin, map[string]any{
		"operation": "update_subscription",
		"values":    map[string]any{"id": subID, "patch": map[string]any{"timezone": "Asia/Tokyo"}},
		"reason":    "business timezone change",
	})
	wantString(t, e.mustCall(t, admin, "GET", "/api/v1/admin/subscriptions/"+subID, nil).ok(t), "timezone", "Asia/Tokyo")

	member1, access1 := e.memberDetail(t, admin, memberID)
	if got, _ := member1["period_start"].(string); got != start0 {
		t.Fatalf("the rule edit moved the running cycle: %q -> %q", start0, got)
	}
	if got, _ := member1["period_end"].(string); got != end0 {
		t.Fatalf("the rule edit moved the running cycle end: %q -> %q", end0, got)
	}
	wantNumber(t, access1, "used_bytes", 1000)

	// The edit itself must persist pending, without waiting for a rollover.
	pending, _ := member1["pending_period_rule"].(map[string]any)
	if pending == nil {
		t.Fatal("AC-USAGE-05: the edited rule was not immediately scheduled")
	}
	wantString(t, pending, "timezone", "Asia/Tokyo")
	wantNumber(t, pending, "anchor_day", 1)
	wantString(t, member1, "period_timezone", "UTC")

	// Marked clock injection at the FIRST old-rule boundary, not a cycle later.
	boundary, err := time.Parse(time.RFC3339Nano, end0)
	if err != nil {
		t.Fatal(err)
	}
	e.st.Now = func() time.Time { return boundary }
	e.agentSnapshot(t, agentToken)
	member2, access2 := e.memberDetail(t, admin, memberID)
	wantNumber(t, access2, "used_bytes", 0)
	wantNumber(t, member2, "period_anchor_day", 1)
	wantString(t, member2, "period_timezone", "Asia/Tokyo")
	if member2["pending_period_rule"] != nil {
		t.Fatalf("the first boundary did not consume the edited rule: %v", member2)
	}
	// First new-rule cycle starts exactly at the old end, ending at the next
	// Tokyo month boundary; it must not overlap/rewrite the closed UTC cycle.
	wantStart := boundary
	wantEnd := boundary.AddDate(0, 1, 0).Add(-9 * time.Hour)
	wantString(t, member2, "period_end", wantEnd.Format(time.RFC3339))
	gotStart, err := time.Parse(time.RFC3339Nano, fieldString(t, member2, "period_start"))
	if err != nil || !gotStart.Equal(wantStart) {
		t.Fatalf("next cycle start = %s, %v; want %s", gotStart, err, wantStart)
	}
	closedStart, err := time.Parse(time.RFC3339Nano, start0)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.scalar(t, `SELECT count(*) FROM usage_entries WHERE member_id=$1 AND period_start=$2`, memberID, closedStart); n != 1 {
		t.Fatalf("rule edit lost the closed-cycle ledger rows: %d", n)
	}
	if n := e.scalar(t, `SELECT COALESCE(sum(up_bytes+down_bytes),0) FROM usage_entries WHERE member_id=$1 AND period_start=$2`, memberID, closedStart); n != 1000 {
		t.Fatalf("rule edit/rollover changed closed-cycle bytes: %d", n)
	}
}

// ─── AC-USAGE-03/07/08 + cross boundary reporting ──────────────────────────

// AC-USAGE-07/08 and cross cycle reporting: a sample straddling a cycle boundary
// is split by duration into the cycle it happened in, and a quota lowered below
// the accounted usage revokes access immediately while raising it restores it.
func TestAcceptanceBoundarySplitAndQuotaChange(t *testing.T) {
	e := newEnv(t)
	e.srv.Cfg.SessionTTL = 90 * 24 * time.Hour
	admin := e.adminSession(t)
	const anchorDay = 1

	serverID := e.createServer(t, admin, "srv-boundary")
	nodeID := e.createNode(t, admin, serverID, "node-boundary", 20443)
	groupID := e.createGroup(t, admin, "group-boundary", []string{nodeID})
	subID := e.createSubscription(t, admin, map[string]any{
		"name": "sub-boundary", "node_group_ids": []string{groupID}, "quota_bytes": int64(1) << 30,
		"timezone": "UTC", "anchor_day": anchorDay, "allow_new_bindings": true, "access_enabled": true,
	})
	memberID := wantHeadline(t, e.createMember(t, admin, map[string]any{
		"username": "boundary-user", "password": "boundary-password-1", "role": "member",
		"subscription_id": subID,
	}))
	agentToken := e.registerAgent(t, admin, serverID)
	memberSess := e.loginAs(t, "boundary-user", "boundary-password-1")
	token := wantField(t, e.mustCall(t, memberSess, "GET", "/api/v1/me/subscription", nil).ok(t), "token")

	// Freeze only after real authorization history exists. Never backdate a
	// fixture ahead of PostgreSQL's creation timestamps.
	recorded := time.Now().UTC()
	boundary := time.Date(recorded.Year(), recorded.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	if boundary.Sub(recorded) < 2*time.Hour {
		boundary = boundary.AddDate(0, 1, 0)
	}
	now := boundary.Add(-time.Hour)
	e.st.Now = func() time.Time { return now }
	// Snapshot drives rollover; product-status reads alone must not write.
	e.agentSnapshot(t, agentToken)
	// Materialize both cycles through the real API before accepting a late report.
	closed, _ := e.memberDetail(t, admin, memberID)
	wantString(t, closed, "period_start", boundary.AddDate(0, -1, 0).Format(time.RFC3339))
	wantString(t, closed, "period_end", boundary.Format(time.RFC3339))
	now = boundary.Add(2 * time.Hour)
	e.agentSnapshot(t, agentToken)
	member0, _ := e.memberDetail(t, admin, memberID)
	start0, err := time.Parse(time.RFC3339Nano, fieldString(t, member0, "period_start"))
	if err != nil {
		t.Fatalf("parse period_start: %v", err)
	}
	if !start0.Equal(boundary) {
		t.Fatalf("persisted cycle starts at %s, want %s", start0, boundary)
	}

	// A sample that starts before and ends after the boundary: 1200 bytes are
	// split by duration, 600 into the closed cycle and 600 into the current one.
	if res := e.mustCallWith(t, nil, "POST", "/api/v1/agent/usage", contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion, BatchID: "accept-boundary-" + randomHex(t, 4),
		Samples: []contracts.UsageSample{{
			NodeID: nodeID, MemberID: memberID, CoreGeneration: "1",
			StartedAt: boundary.Add(-30 * time.Minute), EndedAt: boundary.Add(30 * time.Minute),
			UploadBytes: 600, DownloadBytes: 600,
		}},
	}, map[string]string{"Authorization": "Bearer " + agentToken}); res.status != http.StatusOK {
		t.Fatalf("submit straddling usage = %d: %s", res.status, res.raw)
	}

	type ledgerRow struct {
		periodStart time.Time
		bytes       int64
		split       bool
		estimated   bool
		correction  bool
	}
	rows, err := e.st.DB.QueryContext(context.Background(), `SELECT period_start, up_bytes+down_bytes,
		boundary_split, estimated, correction FROM usage_entries WHERE member_id=$1 ORDER BY period_start`, memberID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var ledger []ledgerRow
	for rows.Next() {
		var r ledgerRow
		if err := rows.Scan(&r.periodStart, &r.bytes, &r.split, &r.estimated, &r.correction); err != nil {
			rows.Close()
			t.Fatalf("scan ledger: %v", err)
		}
		ledger = append(ledger, r)
	}
	rows.Close()
	if len(ledger) != 2 {
		t.Fatalf("ledger rows for a straddling sample = %d, want 2: %+v", len(ledger), ledger)
	}
	var total int64
	for _, r := range ledger {
		total += r.bytes
	}
	if total != 1200 {
		t.Fatalf("the split lost bytes: %d, want 1200", total)
	}
	if !ledger[0].split || !ledger[0].estimated || !ledger[0].correction {
		t.Fatalf("the closed cycle slice is not flagged boundary_split/estimated/correction: %+v", ledger[0])
	}
	if ledger[0].bytes != 600 || ledger[1].bytes != 600 {
		t.Fatalf("duration split must conserve each slice: %+v", ledger)
	}
	if !ledger[0].periodStart.Equal(boundary.AddDate(0, -1, 0)) || !ledger[1].periodStart.Equal(boundary) {
		t.Fatalf("the slices are not booked into their own cycles: %+v", ledger)
	}

	_, access := e.memberDetail(t, admin, memberID)
	wantNumber(t, access, "used_bytes", 600)
	if access["access"] != true {
		t.Fatalf("late reported usage revoked a member that is below quota: %v", access)
	}

	// AC-USAGE-07: lowering the quota below the used bytes revokes access at
	// once and leaves no node in any output.
	e.previewAndSubmit(t, admin, map[string]any{
		"operation": "update_subscription",
		"values":    map[string]any{"id": subID, "patch": map[string]any{"quota_bytes": 500}},
		"reason":    "lower the quota below the used bytes",
	})
	member1, access1 := e.memberDetail(t, admin, memberID)
	if access1["access"] != false {
		t.Fatalf("a lowered quota below the used bytes kept access: %v", access1)
	}
	wantString(t, access1, "proxy_access", "over_quota")
	if nodes, _ := access1["allowed_node_ids"].([]any); len(nodes) != 0 {
		t.Fatalf("an over quota member still has allowed nodes: %v", nodes)
	}
	if member1["over_quota"] != true {
		t.Fatalf("over_quota was not persisted: %v", member1)
	}
	if status, raw := e.fetchSubscription(token, "xray"); status != http.StatusForbidden {
		t.Fatalf("over quota subscription output = %d, want 403: %s", status, raw)
	}
	if users := snapshotUsers(t, e.agentSnapshot(t, agentToken)); users[nodeID] != 0 {
		t.Fatalf("agent snapshot still carries %d users for the over quota member", users[nodeID])
	}

	// AC-USAGE-08: raising the quota restores access in the same request.
	e.previewAndSubmit(t, admin, map[string]any{
		"operation": "update_subscription",
		"values":    map[string]any{"id": subID, "patch": map[string]any{"quota_bytes": int64(5) << 30}},
		"reason":    "restore the quota",
	})
	member2, access2 := e.memberDetail(t, admin, memberID)
	if member2["over_quota"] != false {
		t.Fatalf("over_quota was not cleared: %v", member2)
	}
	if access2["access"] != true {
		t.Fatalf("raising the quota did not restore access: %v", access2)
	}
	if status, raw := e.fetchSubscription(token, "xray"); status != http.StatusOK {
		t.Fatalf("restored subscription output = %d: %s", status, raw)
	}
}

// ─── AC-NODE-01..04 ────────────────────────────────────────────────────────

// AC-NODE-01..04: a node group change propagates to every member of the
// subscriptions that reference it, a node that is covered by several authorized
// groups is emitted once, a node outside the authorized groups is never emitted
// or given a credential, and a template change never leaves the output stale.
func TestAcceptanceNodeGroupPropagationAndDedupe(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)

	const (
		portOne   = 20443
		portTwo   = 20444
		portThree = 20445
	)
	serverID := e.createServer(t, admin, "srv-nodes")
	nodeOne := e.createNode(t, admin, serverID, "node-one", portOne)
	nodeTwo := e.createNode(t, admin, serverID, "node-two", portTwo)
	nodeThree := e.createNode(t, admin, serverID, "node-three", portThree)
	groupOne := e.createGroup(t, admin, "group-one", []string{nodeOne})
	groupTwo := e.createGroup(t, admin, "group-two", []string{nodeOne, nodeTwo})
	subID := e.createSubscription(t, admin, map[string]any{
		"name": "sub-nodes", "node_group_ids": []string{groupOne, groupTwo},
		"quota_bytes": int64(1) << 30, "timezone": "UTC", "anchor_day": 1,
		"allow_new_bindings": true, "access_enabled": true,
	})
	memberID := wantHeadline(t, e.createMember(t, admin, map[string]any{
		"username": "nodes-user", "password": "nodes-password-1", "role": "member", "subscription_id": subID,
	}))
	agentToken := e.registerAgent(t, admin, serverID)
	memberSess := e.loginAs(t, "nodes-user", "nodes-password-1")
	token := wantField(t, e.mustCall(t, memberSess, "GET", "/api/v1/me/subscription", nil).ok(t), "token")

	// AC-NODE-02: node-one is covered by both authorized groups and must appear
	// exactly once; AC-NODE-03: node-three is not authorized at all.
	ports := e.outboundPorts(t, token)
	if n := countInts(ports, portOne); n != 1 {
		t.Fatalf("node covered by two authorized groups emitted %d times, want 1 (%v)", n, ports)
	}
	if n := countInts(ports, portTwo); n != 1 {
		t.Fatalf("second authorized node emitted %d times, want 1 (%v)", n, ports)
	}
	if n := countInts(ports, portThree); n != 0 {
		t.Fatalf("unauthorized node emitted %d times, want 0 (%v)", n, ports)
	}
	if n := e.activeCredentials(t, memberID, nodeThree); n != 0 {
		t.Fatalf("the unauthorized node has %d active credentials for the member", n)
	}
	users := snapshotUsers(t, e.agentSnapshot(t, agentToken))
	if users[nodeOne] != 1 || users[nodeTwo] != 1 {
		t.Fatalf("authorized nodes carry %d/%d users, want 1/1", users[nodeOne], users[nodeTwo])
	}
	if users[nodeThree] != 0 {
		t.Fatalf("the unauthorized node carries %d users", users[nodeThree])
	}

	// AC-NODE-01: adding a node to a referenced group must go through the
	// preview flow, and then reaches the member without touching the subscription.
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/node-groups/"+groupOne, map[string]any{
		"node_ids": []string{nodeOne, nodeThree},
	}).expect(t, http.StatusConflict, "PREVIEW_REQUIRED")
	e.previewAndSubmit(t, admin, map[string]any{
		"operation": "update_node_group",
		"values":    map[string]any{"id": groupOne, "patch": map[string]any{"node_ids": []string{nodeOne, nodeThree}}},
		"reason":    "add a node to the referenced group",
	})

	ports = e.outboundPorts(t, token)
	if n := countInts(ports, portThree); n != 1 {
		t.Fatalf("the propagated node is emitted %d times, want 1 (%v)", n, ports)
	}
	if n := countInts(ports, portOne); n != 1 {
		t.Fatalf("deduplication broke after the group change: node-one emitted %d times (%v)", n, ports)
	}
	if n := e.activeCredentials(t, memberID, nodeThree); n != 1 {
		t.Fatalf("the propagated node has %d credentials for the member, want 1", n)
	}
	if users := snapshotUsers(t, e.agentSnapshot(t, agentToken)); users[nodeThree] != 1 {
		t.Fatalf("the agent snapshot did not receive the propagated node (%d users)", users[nodeThree])
	}

	// AC-NODE-04: bind and edit the existing format-specific template entity.
	// Malicious authorization fields must not replace generated endpoints/users.
	_, beforeTemplate := e.fetchSubscription(token, "xray")
	var original map[string]any
	if err := json.Unmarshal([]byte(beforeTemplate), &original); err != nil {
		t.Fatal(err)
	}
	content := map[string]any{
		"dns": map[string]any{"servers": []string{"1.1.1.1"}},
		"outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{
			"vnext": []any{map[string]any{"address": "unauthorized.invalid", "port": 29999,
				"users": []any{map[string]any{"id": "unauthorized-credential"}}}},
		}}},
		"inbounds": []any{}, "routing": map[string]any{"rules": []any{}},
	}
	templateID := wantHeadline(t, e.mustCall(t, admin, "POST", "/api/v1/admin/templates", map[string]any{
		"name": "tpl-nodes", "format": "xray", "content": content,
	}).ok(t))
	sub := e.mustCall(t, admin, "PATCH", "/api/v1/admin/subscriptions/"+subID, map[string]any{
		"template_id": templateID,
	}).ok(t)
	wantString(t, sub, "template_id", templateID)
	assertTemplate := func(wantDNS string) {
		t.Helper()
		res := e.mustCall(t, nil, "GET", "/sub/"+token+"?format=xray", nil)
		res.expect(t, http.StatusOK, "")
		if got := res.header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("subscription cache policy = %q", got)
		}
		dns, _ := res.body["dns"].(map[string]any)
		servers, _ := dns["servers"].([]any)
		if len(servers) != 1 || servers[0] != wantDNS {
			t.Fatalf("AC-NODE-04: template DNS = %v, want %q", dns, wantDNS)
		}
		for _, key := range []string{"outbounds", "inbounds", "routing"} {
			want, _ := json.Marshal(original[key])
			got, _ := json.Marshal(res.body[key])
			if !bytes.Equal(got, want) {
				t.Fatalf("template replaced panel-generated %s", key)
			}
		}
	}
	assertTemplate("1.1.1.1")
	content["dns"] = map[string]any{"servers": []string{"9.9.9.9"}}
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/templates/"+templateID, map[string]any{"content": content}).ok(t)
	assertTemplate("9.9.9.9") // same URL, no token/credential rotation or cache flush
	if n := countInts(e.outboundPorts(t, token), 29999); n != 0 {
		t.Fatalf("template introduced an unauthorized endpoint (%d)", n)
	}
	// An Xray template must not be interpreted as a sing-box configuration.
	status, raw := e.fetchSubscription(token, "singbox")
	if status != http.StatusOK {
		t.Fatalf("other format = %d: %s", status, raw)
	}
	var other map[string]any
	if err := json.Unmarshal([]byte(raw), &other); err != nil {
		t.Fatal(err)
	}
	if _, exists := other["dns"]; exists {
		t.Fatal("an Xray-only template leaked into sing-box output")
	}
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/subscriptions/"+subID, map[string]any{"template_id": nil}).ok(t)
	status, raw = e.fetchSubscription(token, "xray")
	if status != http.StatusOK || raw != beforeTemplate {
		t.Fatalf("clearing the template did not immediately restore the generated config (status %d)", status)
	}
}

// memberVersion 取成员的当前 version（PATCH 强制要求）。
func (e *env) memberVersion(t *testing.T, s *acceptSession, id string) int64 {
	t.Helper()
	res := e.mustCall(t, s, "GET", "/api/v1/admin/members/"+id, nil)
	data, _ := res.body["data"].(map[string]any)
	if inner, ok := data["member"].(map[string]any); ok {
		if v, ok := inner["version"].(float64); ok {
			return int64(v)
		}
	}
	if v, ok := data["version"].(float64); ok {
		return int64(v)
	}
	return 1
}
