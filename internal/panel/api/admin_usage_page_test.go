package api_test

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"testing"
)

func assertCursorError(t *testing.T, status int, body map[string]any) {
	t.Helper()
	if status != 400 {
		t.Fatalf("cursor status=%d body=%v", status, body)
	}
	e := body["error"].(map[string]any)
	if e["code"] != "CURSOR_INVALID" {
		t.Fatalf("cursor code=%v", e)
	}
	fields := e["details"].(map[string]any)["field_errors"].(map[string]any)
	if fields["cursor"] != "invalid" && fields["cursor"] != "expired_scope" {
		t.Fatalf("cursor fields=%v", fields)
	}
}

func TestAdminUsagePaginationPostgres(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	ctx := context.Background()
	// No production list/key helpers: fixture rows and an independent SQL oracle.
	_, err := e.st.DB.ExecContext(ctx, `INSERT INTO usage_entries(agent_id,batch_id,member_id,node_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at,estimated,boundary_split,correction)
 SELECT 'agent','batch-'||i,'member-'||(i%5),'node-'||(i%13),
 '2020-01-01T00:00:00Z'::timestamptz + (i/130)*interval '1 month',
 '2020-04-01T00:00:00Z'::timestamptz + (i%2)*interval '1 day',i,2*i,'2020-01-01','2020-01-02',i%2=0,i%3=0,i%7=0
 FROM generate_series(259,0,-1) i`)
	if err != nil {
		t.Fatal(err)
	}
	// Multiple ledger samples in one aggregate must not be split across pages.
	_, err = e.st.DB.ExecContext(ctx, `INSERT INTO usage_entries(agent_id,batch_id,member_id,node_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at)
 VALUES('agent','extra','member-0','node-0','2020-01-01T00:00:00Z','2020-04-01T00:00:00Z',7,9,'2020-01-01','2020-01-02')`)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/usage"
	key := func(v map[string]any) string {
		return fmt.Sprint(v["period_start"], "/", v["member_id"], "/", v["node_id"], "/", v["period_end"])
	}
	var first string
	for _, filter := range []string{"", "member_id=member-0", "node_id=node-0", "from=2020-02-01&to=2020-03-01"} {
		where := "true"
		switch filter {
		case "member_id=member-0":
			where = "member_id='member-0'"
		case "node_id=node-0":
			where = "node_id='node-0'"
		case "from=2020-02-01&to=2020-03-01":
			where = "period_start>='2020-02-01T00:00:00Z' AND period_start<'2020-03-01T00:00:00Z'"
		}
		rows, err := e.st.DB.QueryContext(ctx, `SELECT to_char(period_start AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')||'/'||member_id||'/'||node_id||'/'||to_char(period_end AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),sum(up_bytes),sum(down_bytes),bool_or(estimated),bool_or(boundary_split),bool_or(correction) FROM usage_entries WHERE `+where+` GROUP BY period_start,member_id,node_id,period_end ORDER BY period_start DESC,member_id COLLATE "C",node_id COLLATE "C",period_end`)
		if err != nil {
			t.Fatal(err)
		}
		expected := []string{}
		values := map[string][]any{}
		for rows.Next() {
			var k string
			var up, down int64
			var est, split, correction bool
			if err := rows.Scan(&k, &up, &down, &est, &split, &correction); err != nil {
				t.Fatal(err)
			}
			expected = append(expected, k)
			values[k] = []any{float64(up), float64(down), est, split, correction}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		got := []string{}
		seen := map[string]bool{}
		cursor := ""
		for page := 0; page < 30; page++ {
			limit := 17
			if page > 0 {
				limit = 23
			}
			r := e.mustCall(t, a, "GET", path+"?"+filter+"&limit="+strconv.Itoa(limit)+"&cursor="+url.QueryEscape(cursor), nil)
			if r.status != 200 {
				t.Fatal(r.raw)
			}
			items := r.body["items"].([]any)
			if len(items) > limit {
				t.Fatal("limit ignored")
			}
			for _, raw := range items {
				v := raw.(map[string]any)
				k := key(v)
				if seen[k] {
					t.Fatalf("duplicate %s", k)
				}
				seen[k] = true
				got = append(got, k)
				actual := []any{v["up_bytes"], v["down_bytes"], v["estimated"], v["boundary_split"], v["correction"]}
				if !reflect.DeepEqual(actual, values[k]) || v["direction"] == nil || v["algorithm_version"] == nil {
					t.Fatalf("aggregate changed: %v", v)
				}
			}
			if r.body["next_cursor"] == nil {
				break
			}
			cursor = r.body["next_cursor"].(string)
			if filter == "" && page == 0 {
				first = cursor
			}
			if page == 29 {
				t.Fatal("nonterminating")
			}
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("filter %s: got %d want %d", filter, len(got), len(expected))
		}
	}
	if first == "" {
		t.Fatal("missing cursor")
	}
	otherID := wantField(t, e.createMember(t, a, map[string]any{"username": "usage-other-admin", "password": "usage-password-123"}), "id")
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE members SET role='admin' WHERE id=$1`, otherID); err != nil {
		t.Fatal(err)
	}
	other := e.loginAs(t, "usage-other-admin", "usage-password-123")
	cross := e.mustCall(t, other, "GET", path+"?cursor="+url.QueryEscape(first), nil)
	assertCursorError(t, cross.status, cross.body)
	// Core cursors also bind administrator identity.
	if _, err := e.st.DB.ExecContext(ctx, `INSERT INTO servers(id,name) VALUES('usage-s1','usage-s1'),('usage-s2','usage-s2')`); err != nil {
		t.Fatal(err)
	}
	core := e.mustCall(t, a, "GET", "/api/v1/admin/servers?limit=1", nil)
	coreCursor := core.body["next_cursor"].(string)
	cross = e.mustCall(t, other, "GET", "/api/v1/admin/servers?cursor="+url.QueryEscape(coreCursor), nil)
	assertCursorError(t, cross.status, cross.body)
	cross = e.mustCall(t, a, "GET", path+"?cursor="+url.QueryEscape(coreCursor), nil)
	assertCursorError(t, cross.status, cross.body)
	for _, query := range []string{"", "limit=999", "limit=0", "limit=-9"} {
		r := e.mustCall(t, a, "GET", path+"?"+query, nil)
		want := 50
		if query == "limit=999" {
			want = 200
		}
		if query == "limit=0" || query == "limit=-9" {
			want = 1
		}
		if r.status != 200 || len(r.body["items"].([]any)) != want || r.body["next_cursor"] == nil {
			t.Fatalf("%s: %s", query, r.raw)
		}
	}
	for _, tc := range []struct{ q, field string }{{"limit=oops", "limit"}, {"limit=", "limit"}, {"limit=1&limit=2", "limit"}, {"member_id=x&member_id=y", "member_id"}, {"node_id=x&node_id=y", "node_id"}, {"from=bad", "from"}, {"to=bad", "to"}, {"from=2020-02-01&to=2020-01-01", "to"}, {"unknown=1", "unknown"}} {
		r := e.mustCall(t, a, "GET", path+"?"+tc.q, nil)
		r.expect(t, 400, "VALIDATION_FAILED")
		fields := r.body["error"].(map[string]any)["details"].(map[string]any)["field_errors"].(map[string]any)
		if fields[tc.field] == nil {
			t.Fatal(r.raw)
		}
	}
	for _, query := range []string{"cursor=!bad!", "cursor=x&cursor=y", "member_id=member-0&cursor=" + url.QueryEscape(first), "node_id=node-0&cursor=" + url.QueryEscape(first), "from=2020-01-01&cursor=" + url.QueryEscape(first), "to=2021-01-01&cursor=" + url.QueryEscape(first)} {
		r := e.mustCall(t, a, "GET", path+"?"+query, nil)
		assertCursorError(t, r.status, r.body)
	}
	r := e.mustCall(t, a, "GET", "/api/v1/admin/servers?cursor="+url.QueryEscape(first), nil)
	assertCursorError(t, r.status, r.body)
	// Equivalent normalized filters and different page size are allowed.
	r = e.mustCall(t, a, "GET", path+"?from=2020-01-01&limit=1", nil)
	c := r.body["next_cursor"].(string)
	r = e.mustCall(t, a, "GET", path+"?from="+url.QueryEscape("2019-12-31T19:00:00-05:00")+"&limit=2&cursor="+url.QueryEscape(c), nil)
	if r.status != 200 || len(r.body["items"].([]any)) != 2 {
		t.Fatal(r.raw)
	}
	r = e.mustCall(t, a, "GET", path+"?member_id=absent", nil)
	if r.status != 200 || len(r.body["items"].([]any)) != 0 || r.body["next_cursor"] != nil {
		t.Fatal(r.raw)
	}
	// Exactly 130 rows fit one page; a full final page has no phantom cursor.
	r = e.mustCall(t, a, "GET", path+"?from=2020-02-01&to=2020-03-01&limit=130", nil)
	if r.status != 200 || len(r.body["items"].([]any)) != 130 || r.body["next_cursor"] != nil {
		t.Fatal(r.raw)
	}
	// A valid seek can become empty (e.g. concurrent deletion), still null.
	if _, err := e.st.DB.ExecContext(ctx, `DELETE FROM usage_entries`); err != nil {
		t.Fatal(err)
	}
	r = e.mustCall(t, a, "GET", path+"?cursor="+url.QueryEscape(first), nil)
	if r.status != 200 || len(r.body["items"].([]any)) != 0 || r.body["next_cursor"] != nil {
		t.Fatal(r.raw)
	}
}
