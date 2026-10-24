package api_test

import (
	"context"
	"reflect"
	"testing"
)

// DELETE deliberately needs neither query nor body version. These tests exercise
// the public envelope and real PostgreSQL, not an HTTP-only preflight.
func TestDeleteContractHTTP(t *testing.T) {
	e := newEnv(t)
	e.login()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	sub := func(id string) { exec(`INSERT INTO subscriptions(id,name) VALUES($1,$1)`, id) }
	del := func(kind, id string) *response { return e.do("DELETE", "/api/v1/admin/"+kind+"/"+id, nil) }
	wantErr := func(r *response, status int, code string) {
		t.Helper()
		if r.Status != status || r.errCode(t) != code {
			t.Fatalf("want %d %s, got %d %v", status, code, r.Status, r.Body)
		}
	}
	sub("history")
	exec(`INSERT INTO members(id,username,password_hash,archived,subscription_id) VALUES('historian','historian','x',true,'history')`)
	exec(`INSERT INTO member_bindings(id,member_id,subscription_id,ended_at,reason) VALUES('binding','historian','history',now(),'keep me')`)
	exec(`INSERT INTO node_groups(id,name) VALUES('group','group')`)
	exec(`INSERT INTO subscription_node_groups(subscription_id,node_group_id) VALUES('history','group')`)
	exec(`INSERT INTO usage_entries(agent_id,batch_id,member_id,node_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at) VALUES('a','batch','historian','node',now()-interval '1 day',now()+interval '1 day',123,456,now()-interval '1 hour',now())`)
	facts := func() string {
		t.Helper()
		var s string
		if err := e.st.DB.QueryRowContext(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(b) ORDER BY id) FROM member_bindings b),(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM usage_entries u),(SELECT jsonb_agg(to_jsonb(g) ORDER BY subscription_id,node_group_id) FROM subscription_node_groups g),(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM members m))::text`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	originalFacts := facts()
	before := e.do("GET", "/api/v1/admin/subscriptions/history", nil).data(t)
	retired := del("subscriptions", "history").data(t)
	t.Logf("retirement response data: %#v", retired)
	if retired["retired"] != true || retired["archived"] != true || retired["version"] != before["version"].(float64)+1 {
		t.Fatalf("retirement: %v", retired)
	}
	expected := map[string]any{}
	for k, v := range before {
		expected[k] = v
	}
	expected["archived"] = true
	expected["retired"] = true
	expected["version"] = retired["version"]
	expected["updated_at"] = retired["updated_at"]
	if !reflect.DeepEqual(expected, retired) {
		t.Fatalf("snapshot changed beyond retirement: before=%v after=%v", expected, retired)
	}
	again := del("subscriptions", "history").data(t)
	if !reflect.DeepEqual(retired, again) {
		t.Fatalf("non-idempotent: %v != %v", retired, again)
	}
	if originalFacts != facts() {
		t.Fatal("HTTP retirement changed history, ledger, groups or members")
	}
	get := e.do("GET", "/api/v1/admin/subscriptions/history", nil).data(t)
	if _, ok := get["retired"]; ok {
		t.Fatal("DELETE-only retired leaked into GET")
	}
	sub("empty")
	emptyBefore := e.do("GET", "/api/v1/admin/subscriptions/empty", nil).data(t)
	physical := del("subscriptions", "empty").data(t)
	emptyBefore["retired"] = false
	if !reflect.DeepEqual(emptyBefore, physical) {
		t.Fatalf("physical deletion snapshot changed: %v != %v", emptyBefore, physical)
	}
	t.Logf("physical deletion response data: %#v", physical)
	if physical["retired"] != false {
		t.Fatalf("physical delete: %v", physical)
	}
	wantErr(del("subscriptions", "empty"), 404, "NOT_FOUND")
	sub("bound")
	exec(`INSERT INTO members(id,username,password_hash,subscription_id) VALUES('current','current','x','bound')`)
	boundBefore := e.do("GET", "/api/v1/admin/subscriptions/bound", nil).data(t)
	wantErr(del("subscriptions", "bound"), 409, "NODE_IN_USE")
	if !reflect.DeepEqual(boundBefore, e.do("GET", "/api/v1/admin/subscriptions/bound", nil).data(t)) {
		t.Fatal("409 mutated subscription")
	}
	exec(`INSERT INTO templates(id,name,format,content) VALUES('template','template','xray','{}')`)
	exec(`UPDATE subscriptions SET template_id='template' WHERE id='bound'`)
	conflict := del("templates", "template")
	wantErr(conflict, 409, "CONFLICT")
	t.Logf("template reference refusal: %#v", conflict.Body)
	details := conflict.Body["error"].(map[string]any)["details"].(map[string]any)
	if details["reason"] != "template_in_use" || details["reference_count"] != float64(1) {
		t.Fatalf("conflict details: %v", details)
	}
	var ref string
	if err := e.st.DB.QueryRowContext(ctx, `SELECT template_id FROM subscriptions WHERE id='bound'`).Scan(&ref); err != nil || ref != "template" {
		t.Fatalf("reference changed: %q %v", ref, err)
	}
	exec(`UPDATE subscriptions SET template_id=NULL WHERE id='bound'`)
	if del("templates", "template").data(t)["deleted"] != true {
		t.Fatal("unreferenced template not deleted")
	}
	wantErr(del("templates", "template"), 404, "NOT_FOUND")
	exec(`INSERT INTO templates(id,name,format,content) VALUES('archived-template','archived-template','xray','{}')`)
	exec(`UPDATE subscriptions SET template_id='archived-template' WHERE id='history'`)
	if del("templates", "archived-template").data(t)["deleted"] != true {
		t.Fatal("archived reference blocks deletion")
	}
	var nullRef *string
	if err := e.st.DB.QueryRowContext(ctx, `SELECT template_id FROM subscriptions WHERE id='history'`).Scan(&nullRef); err != nil || nullRef != nil {
		t.Fatalf("archived reference not cleared: %v %v", nullRef, err)
	}
	exec(`UPDATE control_state SET recovery_mode=true WHERE id=1`)
	wantErr(del("subscriptions", "missing"), 409, "RECOVERY_MODE")
	wantErr(del("templates", "missing"), 409, "RECOVERY_MODE")
}
