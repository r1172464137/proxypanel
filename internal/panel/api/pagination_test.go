package api_test

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"testing"
)

// Uses newEnv's random, exclusively owned schema on the isolated local PG.
// Fixtures are deliberately inserted in reverse order with tied timestamps.
func TestMePaginationPostgres(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	server := e.createServer(t, a, "page-server")
	ids := make([]string, 0, 235)
	for i := 234; i >= 0; i-- {
		name := fmt.Sprintf("page-%03d", i)
		ids = append(ids, e.createNode(t, a, server, name, 22000+i))
	}
	// This node sorts before the allowed nodes but is deliberately unauthorized.
	e.createNode(t, a, server, "aaa-excluded", 23000)
	group := e.createGroup(t, a, "page-group", ids)
	sub := e.createSubscription(t, a, map[string]any{"name": "page-sub", "node_group_ids": []string{group}, "timezone": "UTC", "anchor_day": 1})
	mid := wantField(t, e.createMember(t, a, map[string]any{"username": "page-user", "password": "page-password-1", "subscription_id": sub}), "id")
	otherID := wantField(t, e.createMember(t, a, map[string]any{"username": "page-other", "password": "page-password-1"}), "id")
	member := e.loginAs(t, "page-user", "page-password-1")
	other := e.loginAs(t, "page-other", "page-password-1")
	for i := 234; i >= 0; i-- {
		id := fmt.Sprintf("page-session-%03d", i)
		exec(`INSERT INTO sessions(id,member_id,token_hash,csrf_token,created_at,expires_at) VALUES($1,$2,$1,'secret','2020-01-01',now()+interval '1 hour')`, id, mid)
		exec(`INSERT INTO quota_adjustments(id,member_id,bytes,reason) VALUES($1,$2,1,'page-test')`, fmt.Sprintf("page-adj-%03d", i), mid)
		exec(`INSERT INTO usage_entries(agent_id,batch_id,member_id,node_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at) VALUES('page-agent',$1,$2,$3,'2020-01-01','2020-02-01',1,2,'2020-01-01','2020-01-02')`, fmt.Sprintf("page-batch-%03d", i), mid, ids[i])
	}
	// Historical same-start/same-node aggregate: period_end must break the tie.
	exec(`INSERT INTO usage_entries(agent_id,batch_id,member_id,node_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at) VALUES('page-agent','end-tie',$1,$2,'2020-01-01','2020-03-01',3,4,'2020-01-01','2020-01-02')`, mid, ids[0])
	exec(`INSERT INTO usage_entries(agent_id,batch_id,member_id,node_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at) VALUES('page-agent','other',$1,'other-node','2020-01-01','2020-02-01',99,99,'2020-01-01','2020-01-02')`, otherID)
	exec(`INSERT INTO quota_adjustments(id,member_id,bytes,reason) VALUES('other-adj',$1,1,'private')`, otherID)
	for _, endpoint := range []string{"nodes", "usage", "sessions"} {
		t.Run(endpoint, func(t *testing.T) {
			path := "/api/v1/me/" + endpoint
			// Compare to the complete authoritative collection, not a capped helper.
			var q string
			switch endpoint {
			case "nodes":
				q = `SELECT id FROM nodes WHERE id<>'unused' AND name ILIKE 'page-%' ORDER BY sort_order,lower(name) COLLATE "C",id COLLATE "C"`
			case "sessions":
				q = `SELECT id FROM sessions WHERE member_id=$1 ORDER BY created_at DESC,id COLLATE "C" DESC`
			case "usage":
				q = `SELECT node_id||'/'||to_char(period_end AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM usage_entries WHERE member_id=$1 GROUP BY node_id,period_start,period_end ORDER BY period_start DESC,node_id COLLATE "C",period_end`
			}
			args := []any{mid}
			if endpoint == "nodes" {
				args = nil
			}
			rows, err := e.st.DB.QueryContext(ctx, q, args...)
			if err != nil {
				t.Fatal(err)
			}
			expected := []string{}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				expected = append(expected, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			seen := map[string]bool{}
			got := []string{}
			cursor := ""
			firstCursor := ""
			for page := 0; page < 20; page++ {
				res := e.mustCall(t, member, "GET", path+"?limit=71&cursor="+url.QueryEscape(cursor), nil)
				if res.status != 200 {
					t.Fatalf("status %d: %s", res.status, res.raw)
				}
				var items []any
				if endpoint == "usage" {
					obj, ok := res.body["items"].(map[string]any)
					if !ok {
						t.Fatal("usage items must remain an object")
					}
					items = obj["usage"].([]any)
					adj := obj["adjustments"].([]any)
					want := 0
					if page == 0 {
						want = 235
					}
					if len(adj) != want {
						t.Fatalf("adjustments=%d want=%d", len(adj), want)
					}
				} else {
					items = res.body["items"].([]any)
				}
				if len(items) > 71 {
					t.Fatal("limit ignored")
				}
				for _, raw := range items {
					item := raw.(map[string]any)
					id, _ := item["id"].(string)
					if endpoint == "usage" {
						if item["member_id"] != mid {
							t.Fatal("cross-member usage")
						}
						id = item["node_id"].(string) + "/" + item["period_end"].(string)
					}
					if endpoint == "sessions" {
						if item["csrf_token"] != nil && item["csrf_token"] != "" {
							t.Fatal("csrf leaked")
						}
					}
					if seen[id] {
						t.Fatalf("duplicate %s", id)
					}
					seen[id] = true
					got = append(got, id)
				}
				next := res.body["next_cursor"]
				if next == nil {
					break
				}
				cursor = next.(string)
				if page == 0 {
					firstCursor = cursor
				}
				if page == 19 {
					t.Fatal("pagination did not terminate")
				}
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("page order/count differs: got %d want %d", len(got), len(expected))
			}
			if firstCursor == "" {
				t.Fatal("truncated collection reported complete")
			}
			for _, bad := range []string{"!bad!", firstCursor[:len(firstCursor)-3] + "AAA"} {
				if r := e.mustCall(t, member, "GET", path+"?cursor="+url.QueryEscape(bad), nil); r.status != 400 {
					t.Fatalf("invalid cursor status %d", r.status)
				}
			}
			if r := e.mustCall(t, other, "GET", path+"?cursor="+url.QueryEscape(firstCursor), nil); r.status != 400 {
				t.Fatalf("cross-member cursor=%d", r.status)
			}
			otherEndpoint := "nodes"
			if endpoint == "nodes" {
				otherEndpoint = "sessions"
			}
			if r := e.mustCall(t, member, "GET", "/api/v1/me/"+otherEndpoint+"?cursor="+url.QueryEscape(firstCursor), nil); r.status != 400 {
				t.Fatalf("cross-endpoint cursor=%d", r.status)
			}
			for _, tc := range []struct {
				limit string
				want  int
			}{{"", 50}, {"999", 200}, {"0", 1}} {
				r := e.mustCall(t, member, "GET", path+"?limit="+tc.limit, nil)
				if r.status != 200 {
					t.Fatalf("limit %s=%d", tc.limit, r.status)
				}
				var items []any
				if endpoint == "usage" {
					items = r.body["items"].(map[string]any)["usage"].([]any)
				} else {
					items = r.body["items"].([]any)
				}
				if len(items) != tc.want {
					t.Fatalf("limit %s returned %d", tc.limit, len(items))
				}
			}
		})
	}
	// The unbound member cannot see somebody else's nodes or usage.
	for _, endpoint := range []string{"nodes", "usage"} {
		r := e.mustCall(t, other, "GET", "/api/v1/me/"+endpoint, nil)
		if r.status != 200 {
			t.Fatal(r.raw)
		}
		if endpoint == "nodes" && len(r.body["items"].([]any)) != 0 {
			t.Fatal("cross-member nodes")
		}
		if endpoint == "usage" && len(r.body["items"].(map[string]any)["usage"].([]any)) != 1 {
			t.Fatal("cross-member usage count")
		}
	}
}
