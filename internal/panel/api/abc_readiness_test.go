package api_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestABCReadinessMountedDuringRecovery(t *testing.T) {
	e := newEnv(t)
	admin := e.adminSession(t)
	e.mustCall(t, nil, "GET", "/api/v1/admin/upgrade-readiness", nil).expect(t, 401, "UNAUTHENTICATED")
	e.createMember(t, admin, map[string]any{"username": "readiness-member", "password": "member-password-1", "role": "member"})
	member := e.loginAs(t, "readiness-member", "member-password-1")
	e.mustCall(t, member, "GET", "/api/v1/admin/upgrade-readiness", nil).expect(t, 403, "FORBIDDEN")
	assertSource := func(want string) {
		t.Helper()
		wire := e.mustCall(t, admin, "GET", "/api/v1/admin/upgrade-readiness?path=/etc/passwd&evidence=/etc/shadow", nil).ok(t)
		if wire["status"] != want {
			t.Fatalf("diagnostic status: %+v", wire)
		}
		checked, err := time.Parse(time.RFC3339Nano, wire["checked_at"].(string))
		if err != nil || time.Since(checked) > time.Minute {
			t.Fatalf("inspection time: %+v", wire)
		}
		source, err := e.st.CheckUpgradeReadiness(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var direct map[string]any
		if err = json.Unmarshal(raw, &direct); err != nil {
			t.Fatal(err)
		}
		delete(wire, "checked_at")
		delete(direct, "checked_at")
		if !reflect.DeepEqual(wire, direct) {
			t.Fatalf("HTTP/store disagreement: %+v vs %s", wire, raw)
		}
		if strings.Contains(string(raw), "password_hash") || strings.Contains(string(raw), "/etc/") || strings.Contains(string(raw), "token_hash") {
			t.Fatal("private material leaked")
		}
	}
	assertSource("ready")
	serverID := e.createServer(t, admin, "readiness-agent-server")
	e.registerAgent(t, admin, serverID)
	assertSource("unknown")
	if _, err := e.st.DB.Exec(`UPDATE schema_migrations SET checksum='wrong' WHERE version='0001_init.sql'`); err != nil {
		t.Fatal(err)
	}
	assertSource("blocked")
	// Entering recovery must remain available even when readiness is blocked.
	e.mustCall(t, admin, "POST", "/api/v1/admin/recovery-mode/enter", nil).ok(t)
	assertSource("blocked")
}
