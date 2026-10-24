package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProductStatusReadModel(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	e.mustCall(t, nil, "GET", "/api/v1/me/status", nil).expect(t, 401, "UNAUTHENTICATED")
	overview := func() map[string]any {
		return e.mustCall(t, a, "GET", "/api/v1/admin/overview", nil).ok(t)["setup"].(map[string]any)
	}
	unbound := e.mustCall(t, a, "GET", "/api/v1/me/status", nil).ok(t)
	if unbound["data_complete"] != true {
		t.Fatalf("an unbound account is not corrupt data: %v", unbound)
	}
	setup := overview()
	if setup["completed_count"] != float64(0) {
		t.Fatalf("empty setup: %v", setup)
	}
	steps := setup["steps"].([]any)
	if len(steps) != 6 || steps[5].(map[string]any)["state"] != "unknown" {
		t.Fatalf("steps: %v", steps)
	}
	server := e.createServer(t, a, "product-server")
	node := e.createNode(t, a, server, "product-node", 14443)
	group := e.createGroup(t, a, "product-group", []string{node})
	sub := e.createSubscription(t, a, map[string]any{"name": "product-sub", "quota_bytes": 1000, "node_group_ids": []string{group}, "timezone": "UTC", "anchor_day": 1})
	m := e.createMember(t, a, map[string]any{"username": "product-member", "password": "product-password-1", "subscription_id": sub})
	id := wantHeadline(t, m)
	u := e.loginAs(t, "product-member", "product-password-1")
	e.mustCall(t, u, "GET", "/api/v1/admin/overview", nil).expect(t, 403, "FORBIDDEN")
	e.mustCall(t, u, "GET", "/api/v1/admin/members/"+id, nil).expect(t, 403, "FORBIDDEN")
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO usage_entries (agent_id,batch_id,member_id,node_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at) SELECT 'product-agent','product-batch',id,$2,period_start,period_end,30,12,now()-interval '1 second',now() FROM members WHERE id=$1`, id, node)
	status := e.mustCall(t, u, "GET", "/api/v1/me/status", nil).ok(t)
	member := status["member"].(map[string]any)
	if status["data_complete"] != true || member["used_bytes"] != float64(42) || member["effective_quota_bytes"] != float64(1000) {
		t.Fatalf("status %v", status)
	}
	observed := overview()["steps"].([]any)[5].(map[string]any)
	if observed["state"] != "completed" || observed["reason"] != "traffic_observed_not_all_clients_verified" {
		t.Fatalf("ledger evidence not consumed: %v", observed)
	}
	originalEnd := member["period_end"]
	detail, _ := e.memberDetail(t, a, id)
	if detail["data_complete"] != true || detail["used_bytes"] != float64(42) {
		t.Fatalf("detail: %v", detail)
	}
	importData := e.mustCall(t, u, "GET", "/api/v1/me/subscription", nil).ok(t)
	for _, raw := range importData["format_status"].([]any) {
		if raw.(map[string]any)["available"] != true {
			t.Fatalf("format: %v", raw)
		}
	}
	if importData["period_end"] != originalEnd || importData["rotation_impact"] != "old_link_invalid_new_required" {
		t.Fatalf("import fields %v", importData)
	}
	// A saved node that only some encoders can represent must not produce four
	// advertised working links. This is persisted PG input, not a mocked renderer.
	exec(`UPDATE nodes SET stream_settings='{"network":"httpupgrade","security":"none","httpupgradeSettings":{"path":"/proxy"}}' WHERE id=$1`, node)
	importData = e.mustCall(t, u, "GET", "/api/v1/me/subscription", nil).ok(t)
	for _, raw := range importData["format_status"].([]any) {
		f := raw.(map[string]any)
		if f["format"] == "mihomo" && (f["available"] != false || !strings.HasPrefix(f["reason"].(string), "protocol_unsupported:")) {
			t.Fatalf("unsupported: %v", f)
		}
	}
	// Missing immutable history must not erase proven ledger/quota/date values.
	exec(`DELETE FROM member_periods WHERE member_id=$1`, id)
	status = e.mustCall(t, u, "GET", "/api/v1/me/status", nil).ok(t)
	member = status["member"].(map[string]any)
	if status["data_complete"] != false || member["used_bytes"] != float64(42) || member["effective_quota_bytes"] != float64(1000) || member["period_end"] != originalEnd {
		t.Fatalf("incomplete: %v", status)
	}
	issues := status["data_issues"].([]any)
	if len(issues) != 2 || issues[0] != "cycle_unproven" || issues[1] != "ledger_unattributable" {
		t.Fatalf("issues: %v", issues)
	}
	e.mustCall(t, a, "POST", "/api/v1/admin/recovery-mode/enter", nil).ok(t)
	snapshot := func() string {
		var raw string
		if err := e.st.DB.QueryRowContext(ctx, `SELECT jsonb_build_object('members',(SELECT jsonb_agg(to_jsonb(m)) FROM members m),'periods',(SELECT jsonb_agg(to_jsonb(p)) FROM member_periods p),'credentials',(SELECT jsonb_agg(to_jsonb(c)) FROM credentials c),'tokens',(SELECT jsonb_agg(to_jsonb(t)) FROM subscription_tokens t),'control',(SELECT to_jsonb(c) FROM control_state c WHERE id=1))::text`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// Expired persisted interval: GETs must not try to roll it or build credentials.
	exec(`UPDATE members SET period_start=period_start-interval '2 years',period_end=period_end-interval '2 years' WHERE id=$1`, id)
	before := snapshot()
	status = e.mustCall(t, u, "GET", "/api/v1/me/status", nil).ok(t)
	e.mustCall(t, a, "GET", "/api/v1/admin/members/"+id, nil).ok(t)
	frozen := e.mustCall(t, u, "GET", "/api/v1/me/subscription", nil).ok(t)
	if frozen["token"] != importData["token"] {
		t.Fatal("token changed")
	}
	if after := snapshot(); after != before {
		t.Fatal("recovery GET mutated persisted authorization")
	}
	// Only the import area fails when no token exists; status remains readable.
	exec(`DELETE FROM subscription_tokens WHERE member_id=$1`, id)
	before = snapshot()
	e.mustCall(t, u, "GET", "/api/v1/me/subscription", nil).expect(t, 409, "RECOVERY_MODE")
	e.mustCall(t, u, "GET", "/api/v1/me/status", nil).ok(t)
	if snapshot() != before {
		t.Fatal("missing-token GET created authorization material")
	}
	raw, _ := json.Marshal(status)
	for _, secret := range []string{"password_hash", "token_enc", "server_key", "product-password-1"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret field: %s", secret)
		}
	}
}

func TestProductSetupEvidence(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	server := e.createServer(t, a, "setup-server")
	node := e.createNode(t, a, server, "setup-node", 15443)
	group := e.createGroup(t, a, "setup-group", []string{node})
	sub := e.createSubscription(t, a, map[string]any{"name": "setup-sub", "node_group_ids": []string{group}, "timezone": "UTC", "anchor_day": 1})
	e.createMember(t, a, map[string]any{"username": "setup-member", "password": "setup-password-1", "subscription_id": sub})
	setup := e.mustCall(t, a, "GET", "/api/v1/admin/overview", nil).ok(t)["setup"].(map[string]any)
	if setup["completed_count"] != float64(4) {
		t.Fatalf("no heartbeat/no traffic: %v", setup)
	}
	if setup["steps"].([]any)[5].(map[string]any)["state"] != "unknown" {
		t.Fatal("objects are not connection evidence")
	}
	e.registerAgent(t, a, server)
	if _, err := e.st.DB.ExecContext(context.Background(), `UPDATE agents SET last_heartbeat_at=now() WHERE server_id=$1`, server); err != nil {
		t.Fatal(err)
	}
	setup = e.mustCall(t, a, "GET", "/api/v1/admin/overview", nil).ok(t)["setup"].(map[string]any)
	if setup["completed_count"] != float64(5) {
		t.Fatalf("heartbeat evidence: %v", setup)
	}
	if setup["steps"].([]any)[5].(map[string]any)["state"] != "unknown" {
		t.Fatal("heartbeat is not client connection verification")
	}
	if _, err := e.st.DB.ExecContext(context.Background(), `UPDATE agents SET last_heartbeat_at=NULL WHERE server_id=$1`, server); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.ExecContext(context.Background(), `UPDATE nodes SET enabled=false WHERE id=$1`, node); err != nil {
		t.Fatal(err)
	}
	setup = e.mustCall(t, a, "GET", "/api/v1/admin/overview", nil).ok(t)["setup"].(map[string]any)
	if setup["completed_count"] != float64(0) {
		t.Fatalf("disabled node advertised delivery: %v", setup)
	}
}
