package api_test

import "testing"

func TestABCReadinessMountedDuringRecovery(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)
	e.mustCall(t, nil, "GET", "/api/v1/admin/upgrade-readiness", nil).expect(t, 401, "UNAUTHENTICATED")
	e.mustCall(t, admin, "POST", "/api/v1/admin/recovery-mode/enter", nil).ok(t)
	e.mustCall(t, admin, "GET", "/api/v1/admin/upgrade-readiness?path=/etc/passwd", nil).ok(t)
}
