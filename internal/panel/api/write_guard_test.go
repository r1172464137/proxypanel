package api_test

import (
	"net/http"
	"testing"
)

// 这两个回归用例对应两个真实缺陷，都来自 e2e 并发/恢复模式测试：
//
//  1. 省略 version 的并发 PATCH 两个都成功 → 后写静默覆盖先写（乐观并发被绕过）
//  2. 恢复协调模式期间成员 /me/* 的写接口没被拦截 → 仍能轮换 token、轮换凭据、删会话
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

// TestRecoveryModeBlocksMemberSelfServiceWrites：恢复协调期间成员自助写接口必须 409。
//
// 成员自助改 token / 凭据 / 删会话都动授权相关状态并写库，与恢复模式
// "暂停普通变更、核对后再推送权限"的语义冲突。修改自己的密码保留放行
// （自救路径，不改授权）。
func TestRecoveryModeBlocksMemberSelfServiceWrites(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)

	created := e.createMember(t, admin, map[string]any{
		"username": "recovery-target", "password": "recovery-target-pw-1", "role": "member",
	})
	wantField(t, created, "id")
	sess := e.loginAs(t, "recovery-target", "recovery-target-pw-1")

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
		{"删除会话", "DELETE", "/api/v1/me/sessions/some-id"},
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
