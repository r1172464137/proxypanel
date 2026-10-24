package api_test

import (
	"net/http"
	"testing"
)

// 这两个回归用例对应两个真实缺陷，都来自 e2e 并发/恢复模式测试：
//
//  1. 省略 version 的并发 PATCH 两个都成功 → 后写静默覆盖先写（乐观并发被绕过）
//  2. 恢复协调模式期间普通授权写必须拦截；本人 Web 会话撤销属于救援例外
//
// 两个都是"接口没拦住"，所以修在 API 层，测试也放在 API 层。

// TestPatchWithoutVersionIsRejected：PATCH/PUT 缺少 version 必须 400。
//
// store 层用 `expectedVersion != 0` 表示"跳过乐观并发检查"，这个内部约定
// 曾经泄漏到 API 面：客户端省略 version 就能让两次并发 PATCH 都成功。
func TestPatchWithoutVersionIsRejected(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)

	serverID := e.createServer(t, admin, "no-version")

	cases := []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{"patch 省略 version", "PATCH", "/api/v1/admin/servers/" + serverID,
			map[string]any{"region": "no-version"}},
		{"patch 传 version=0", "PATCH", "/api/v1/admin/servers/" + serverID,
			map[string]any{"region": "zero-version", "version": 0}},
		{"put 省略 version", "PUT", "/api/v1/admin/settings",
			map[string]any{"telegram_enabled": false}},
	}
	for _, tc := range cases {
		// 直接走 e.call：mustCall 会自动补 version，这里要的正是缺失的情况。
		res, err := e.call(admin, tc.method, tc.path, tc.body, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		// version=0 在 JSON 里解成零值，与"省略"等价；两者都必须拒绝。
		if res.status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400: %s", tc.name, res.status, res.raw)
		}
		if res.errCode() != "VALIDATION_FAILED" {
			t.Fatalf("%s code = %q, want VALIDATION_FAILED", tc.name, res.errCode())
		}
	}

	// 带上正确 version 必须成功，否则接口不可用。
	current := e.mustCall(t, admin, "GET", "/api/v1/admin/servers/"+serverID, nil).ok(t)
	e.mustCall(t, admin, "PATCH", "/api/v1/admin/servers/"+serverID, map[string]any{
		"region": "with-version", "version": current["version"],
	}).ok(t)
}

// TestRecoveryModeBlocksMemberSelfServiceWrites: ordinary proxy authorization
// writes remain blocked; password changes and owned Web-session rescue stay usable.
func TestRecoveryModeBlocksMemberSelfServiceWrites(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)

	serverID := e.createServer(t, admin, "rescue-server")
	nodeID := e.createNode(t, admin, serverID, "rescue-node", 20443)
	groupID := e.createGroup(t, admin, "rescue-group", []string{nodeID})
	subID := e.createSubscription(t, admin, map[string]any{
		"name": "rescue-sub", "node_group_ids": []string{groupID}, "quota_bytes": 1 << 30,
		"timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true,
	})
	created := e.createMember(t, admin, map[string]any{
		"username": "recovery-target", "password": "recovery-target-pw-1", "role": "member",
		"subscription_id": subID,
	})
	memberID := wantField(t, created, "id")
	sess := e.loginAs(t, "recovery-target", "recovery-target-pw-1")
	victim := e.loginAs(t, "recovery-target", "recovery-target-pw-1")
	e.createMember(t, admin, map[string]any{
		"username": "rescue-other", "password": "rescue-other-pw-1", "role": "member",
	})
	other := e.loginAs(t, "rescue-other", "rescue-other-pw-1")
	currentID := func(s *acceptSession) string {
		for _, raw := range e.mustCall(t, s, "GET", "/api/v1/me/sessions", nil).listItems(t) {
			row := raw.(map[string]any)
			if row["current"] == true {
				return wantField(t, row, "id")
			}
		}
		t.Fatal("no current session listed")
		return ""
	}
	victimID, otherID := currentID(victim), currentID(other)
	tokenBefore := wantField(t, e.mustCall(t, sess, "GET", "/api/v1/me/subscription", nil).ok(t), "token")
	credentialsBefore := e.activeCredentials(t, memberID, nodeID)
	if credentialsBefore != 1 {
		t.Fatalf("rescue fixture credentials = %d, want 1", credentialsBefore)
	}

	e.mustCall(t, admin, "POST", "/api/v1/admin/recovery-mode/enter", map[string]any{})

	// 对照：管理员写路由被拦（既有行为，确认恢复模式真的生效了）。
	res, err := e.call(admin, "POST", "/api/v1/admin/servers", map[string]any{
		"name": "blocked", "address": "node.example.invalid", "region": "test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.status != http.StatusConflict || res.errCode() != "RECOVERY_MODE" {
		t.Fatalf("admin write during recovery = %d %q, want 409 RECOVERY_MODE: %s",
			res.status, res.errCode(), res.raw)
	}

	blocked := []struct {
		name   string
		method string
		path   string
	}{
		{"轮换订阅 token", "POST", "/api/v1/me/subscription/token/rotations"},
		{"轮换代理凭据", "POST", "/api/v1/me/credentials/rotations"},
	}
	for _, tc := range blocked {
		res, err := e.call(sess, tc.method, tc.path, nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.status != http.StatusConflict || res.errCode() != "RECOVERY_MODE" {
			t.Fatalf("成员%s during recovery = %d %q, want 409 RECOVERY_MODE: %s",
				tc.name, res.status, res.errCode(), res.raw)
		}
	}

	// A real second owned session can be rescued, without proxy side effects.
	e.mustCall(t, sess, "DELETE", "/api/v1/me/sessions/"+victimID, nil).ok(t)
	e.mustCall(t, victim, "GET", "/api/v1/auth/session", nil).expect(t, http.StatusUnauthorized, "UNAUTHENTICATED")
	for _, id := range []string{otherID, "some-id"} {
		e.mustCall(t, sess, "DELETE", "/api/v1/me/sessions/"+id, nil).expect(t, http.StatusNotFound, "NOT_FOUND")
	}
	e.mustCall(t, other, "GET", "/api/v1/auth/session", nil).ok(t)
	tokenAfter := wantField(t, e.mustCall(t, sess, "GET", "/api/v1/me/subscription", nil).ok(t), "token")
	if tokenBefore != tokenAfter || e.activeCredentials(t, memberID, nodeID) != credentialsBefore {
		t.Fatal("Web session rescue changed proxy authorization")
	}
	// Keep the current-session contract as an explicit regression, not a 409.
	// Run in a subtest so a production failure does not hide password rescue.
	t.Run("current_session_cannot_be_revoked", func(t *testing.T) {
		e.mustCall(t, sess, "DELETE", "/api/v1/me/sessions/"+currentID(sess), nil).
			expect(t, http.StatusBadRequest, "VALIDATION_FAILED")
		e.mustCall(t, sess, "GET", "/api/v1/auth/session", nil).ok(t)
	})
	// Reauthenticate independently so the remaining rescue checks run even if
	// the production current-session guard regresses.
	sess = e.loginAs(t, "recovery-target", "recovery-target-pw-1")

	// 读接口必须仍可用，否则成员看不到自己的状态。
	e.mustCall(t, sess, "GET", "/api/v1/me/sessions", nil).ok(t)

	// 改自己密码是自救路径，恢复模式期间放行。
	e.mustCall(t, sess, "POST", "/api/v1/me/password", map[string]any{
		"current_password": "recovery-target-pw-1", "new_password": "recovery-target-pw-2",
	}).ok(t)

	// 退出恢复模式后，自助写恢复正常。
	e.mustCall(t, admin, "POST", "/api/v1/admin/recovery-mode/exit", map[string]any{
		"confirm": "verified", "agent_revisions_checked": true, "ledger_checked": true,
	}).ok(t)
	// 改密码后旧会话被撤销，用新密码重新登录确认成员可用。
	e.loginAs(t, "recovery-target", "recovery-target-pw-2")
	e.mustCall(t, sess, "POST", "/api/v1/me/subscription/token/rotations", nil).ok(t)
}
