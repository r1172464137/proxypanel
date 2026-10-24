package api_test

import (
	"context"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// newEnv owns a random schema in the isolated PostgreSQL, and drops only that
// schema. Bulk fixtures avoid exercising unrelated write transactions.
func TestAdminPaginationPostgres(t *testing.T) {
	e := newEnv(t)
	a := e.adminSession(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.st.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"servers", "templates", "node_groups", "subscriptions"} {
		extra, values := "", ""
		if table == "templates" {
			extra = ",format"
			values = ",'xray'"
		}
		exec(`INSERT INTO ` + table + `(id,name` + extra + `) SELECT 'page-'||lpad(i::text,3,'0'),'Entry-'||lpad(i::text,3,'0')` + values + ` FROM generate_series(234,0,-1) i`)
	}
	exec(`INSERT INTO nodes(id,server_id,name,host,port,protocol,sort_order) SELECT 'page-'||lpad(i::text,3,'0'),'page-000','Entry-'||lpad(i::text,3,'0'),'example.test',22000+i,'vless',i%3 FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO members(id,username,password_hash) SELECT 'page-'||lpad(i::text,3,'0'),'Entry-'||lpad(i::text,3,'0'),'not-a-login-hash' FROM generate_series(234,0,-1) i`)
	// Related fields must remain complete even when the parent is paginated.
	exec(`INSERT INTO node_group_members(group_id,node_id) VALUES('page-234','page-234')`)
	exec(`INSERT INTO subscription_node_groups(subscription_id,node_group_id) VALUES('page-234','page-234')`)
	exec(`UPDATE members SET archived=true WHERE id='page-001'`)
	exec(`UPDATE servers SET archived=true WHERE id='page-001'`)
	exec(`UPDATE nodes SET archived=true WHERE id='page-001'`)
	exec(`UPDATE members SET subscription_id='page-234' WHERE id='page-234'`)
	// Unicode and literal LIKE metacharacters must use identical seek/order keys.
	exec(`UPDATE nodes SET name='Éclair_%\tail' WHERE id='page-233'`)
	exec(`UPDATE members SET username='Éclair_%\tail' WHERE id='page-233'`)
	// Exercise the ID tie-break independently of today's unique name index.
	// This index is removed ONLY in this test's exclusively owned schema.
	exec(`DROP INDEX nodes_name_key`)
	exec(`UPDATE nodes SET name='entry-000',sort_order=0 WHERE id='page-003'`)
	firstCursors := map[string]string{}
	for _, endpoint := range []string{"servers", "nodes", "node-groups", "subscriptions", "templates", "members"} {
		t.Run(endpoint, func(t *testing.T) {
			table := strings.ReplaceAll(endpoint, "-", "_")
			name := "name"
			if endpoint == "members" {
				name = "username"
			}
			where := "true"
			if endpoint == "members" || endpoint == "servers" || endpoint == "nodes" {
				where = "NOT archived"
			}
			order := `lower(` + name + `) COLLATE "C",id COLLATE "C"`
			if endpoint == "nodes" {
				order = "sort_order," + order
			}
			rows, err := e.st.DB.QueryContext(ctx, `SELECT id FROM `+table+` WHERE `+where+` ORDER BY `+order)
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
			path := "/api/v1/admin/" + endpoint
			got := []string{}
			cursor := ""
			seen := map[string]bool{}
			for page := 0; page < 20; page++ {
				r := e.mustCall(t, a, "GET", path+"?limit=71&cursor="+url.QueryEscape(cursor), nil)
				if r.status != 200 {
					t.Fatalf("status %d: %s", r.status, r.raw)
				}
				items := r.body["items"].([]any)
				if len(items) > 71 {
					t.Fatal("limit ignored")
				}
				for _, raw := range items {
					item := raw.(map[string]any)
					id := item["id"].(string)
					if seen[id] {
						t.Fatalf("duplicate %s", id)
					}
					seen[id] = true
					got = append(got, id)
					if item["password_hash"] != nil || item["server_key"] != nil {
						t.Fatal("secret field returned")
					}
					if endpoint == "members" {
						if _, ok := item["access_reasons"].([]any); !ok {
							t.Fatal("member decoration missing")
						}
					}
					if id == "page-234" {
						if endpoint == "node-groups" && len(item["node_ids"].([]any)) != 1 {
							t.Fatal("lost node group members")
						}
						if endpoint == "subscriptions" && (len(item["node_group_ids"].([]any)) != 1 || item["bound_members"] != float64(1)) {
							t.Fatal("lost subscription details")
						}
					}
				}
				if r.body["next_cursor"] == nil {
					break
				}
				cursor = r.body["next_cursor"].(string)
				if page == 0 {
					firstCursors[endpoint] = cursor
				}
				if page == 19 {
					t.Fatal("pagination did not terminate")
				}
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("order/count: got %d want %d", len(got), len(expected))
			}
			if firstCursors[endpoint] == "" {
				t.Fatal("truncated collection reported complete")
			}
			for _, tc := range []struct {
				query         string
				status, count int
			}{{"", 200, 50}, {"?limit=9999", 200, 200}, {"?limit=0", 200, 1}, {"?limit=-9", 200, 1}, {"?limit=oops", 400, 0}, {"?cursor=!bad!", 400, 0}, {"?limit=2&limit=3", 400, 0}} {
				r := e.mustCall(t, a, "GET", path+tc.query, nil)
				if r.status != tc.status {
					t.Fatalf("%s status %d", tc.query, r.status)
				}
				if tc.status == 200 && len(r.body["items"].([]any)) != tc.count {
					t.Fatalf("%s count mismatch", tc.query)
				}
			}
		})
	}
	for endpoint, cursor := range firstCursors {
		for other := range firstCursors {
			if endpoint == other {
				continue
			}
			r := e.mustCall(t, a, "GET", "/api/v1/admin/"+other+"?cursor="+url.QueryEscape(cursor), nil)
			if r.status != 400 {
				t.Fatalf("cross endpoint %s to %s: %d", endpoint, other, r.status)
			}
		}
		r := e.mustCall(t, a, "GET", "/api/v1/admin/"+endpoint+"?cursor="+url.QueryEscape(cursor[:len(cursor)/2]), nil)
		if r.status != 400 {
			t.Fatal("tampered cursor accepted")
		}
	}
	for _, endpoint := range []string{"nodes", "members"} {
		path := "/api/v1/admin/" + endpoint
		// Target sorts beyond page one; q must be applied before LIMIT in SQL.
		for _, q := range []string{"  ENTRY-234  ", `_%\tail`} {
			r := e.mustCall(t, a, "GET", path+"?q="+url.QueryEscape(q)+"&limit=1", nil)
			if r.status != 200 || len(r.body["items"].([]any)) != 1 || r.body["next_cursor"] != nil {
				t.Fatalf("global/literal q failed: %s", r.raw)
			}
		}
		// Traverse a multi-page filtered result, including a page-size change.
		// Entry-2 has exactly 34 matches, all beyond the unfiltered first page
		// for members. Compare to the same filter at the full 200-row bound.
		full := e.mustCall(t, a, "GET", path+"?q=Entry-2&limit=200", nil)
		want := []string{}
		for _, raw := range full.body["items"].([]any) {
			want = append(want, raw.(map[string]any)["id"].(string))
		}
		if len(want) != 34 {
			t.Fatalf("filtered fixture count=%d", len(want))
		}
		got := []string{}
		cursor := ""
		for page := 0; page < 10; page++ {
			limit := 11
			if page > 0 {
				limit = 13
			}
			res := e.mustCall(t, a, "GET", path+"?q=Entry-2&limit="+strconv.Itoa(limit)+"&cursor="+url.QueryEscape(cursor), nil)
			if res.status != 200 {
				t.Fatalf("filtered seek status=%d", res.status)
			}
			for _, raw := range res.body["items"].([]any) {
				got = append(got, raw.(map[string]any)["id"].(string))
			}
			if res.body["next_cursor"] == nil {
				break
			}
			cursor = res.body["next_cursor"].(string)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatal("filtered seek duplicated, lost or reordered rows")
		}
		r := e.mustCall(t, a, "GET", path+"?q=Entry&limit=1", nil)
		c := r.body["next_cursor"].(string)
		for _, filter := range []string{"q=other", "", "q=Entry&" + map[string]string{"nodes": "server_id=page-001", "members": "subscription_id=page-001"}[endpoint]} {
			r := e.mustCall(t, a, "GET", path+"?"+filter+"&cursor="+url.QueryEscape(c), nil)
			if r.status != 400 {
				t.Fatal("changed filter accepted")
			}
		}
		r = e.mustCall(t, a, "GET", path+"?q="+strings.Repeat("x", 65), nil)
		if r.status != 400 {
			t.Fatal("oversize q accepted")
		}
		// Exact page boundary and empty result must not invent a next page.
		for _, q := range []string{"Entry-234", "no-match"} {
			r = e.mustCall(t, a, "GET", path+"?q="+q+"&limit=1", nil)
			if r.body["next_cursor"] != nil {
				t.Fatal("invented next page")
			}
		}
	}
	for _, tc := range []struct {
		endpoint, filter string
		want             int
	}{{"nodes", "server_id=page-000", 200}, {"members", "subscription_id=page-234", 1}, {"members", "include_archived=true", 200}} {
		r := e.mustCall(t, a, "GET", "/api/v1/admin/"+tc.endpoint+"?limit=200&"+tc.filter, nil)
		if r.status != 200 || len(r.body["items"].([]any)) != tc.want {
			t.Fatalf("filter %s failed: %s", tc.filter, r.raw)
		}
	}
	// Even a same-endpoint cursor cannot toggle include_archived.
	r := e.mustCall(t, a, "GET", "/api/v1/admin/members?include_archived=true&cursor="+url.QueryEscape(firstCursors["members"]), nil)
	if r.status != 400 {
		t.Fatal("archive scope changed")
	}
}
