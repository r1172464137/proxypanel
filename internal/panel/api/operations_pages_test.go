package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

// Test-only mux wires the three delegated route replacements explicitly. It is
// not evidence that api.go has already been updated by the route owner.
func TestOperationsPagesPostgres(t *testing.T) {
	dsn := os.Getenv("PANEL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PANEL_TEST_DATABASE_URL not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 15432 || cfg.Database != "proxypanel" {
		t.Fatal("refusing non-isolated test database")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("operations_%d", time.Now().UnixNano())
	// A random database-generated suffix makes simultaneous worktrees independent.
	var suffix string
	if err := conn.QueryRow(ctx, `SELECT substr(md5(random()::text),1,12)`).Scan(&suffix); err != nil {
		t.Fatal(err)
	}
	schema += "_" + suffix
	if _, err := conn.Exec(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, `DROP SCHEMA "`+schema+`" CASCADE`); _ = conn.Close(ctx) }()
	key, err := secrets.ParseKey(strings.Repeat("operations-test-key-", 4))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{DatabaseURL: dsn, MasterKey: key, MigrationsDir: filepath.Join("..", "..", "..", "migrations"), SearchPath: schema})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := st.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO servers(id,name,address) VALUES('server','server','localhost')`)
	exec(`INSERT INTO agents(id,server_id,name,token_hash,last_heartbeat_at) SELECT 'a-'||lpad(i::text,3,'0'),'server','Tied Name','hash-'||i,now() FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO nodes(id,server_id,name,host,port,protocol) SELECT 'n-'||lpad(i::text,3,'0'),'server','node-'||lpad(i::text,3,'0'),'localhost',10000+i,'vless' FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO core_instances(id,server_id,node_id) SELECT 'c-'||lpad(i::text,3,'0'),'server','n-'||lpad(i::text,3,'0') FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO invitations(id,code_hash,expires_at,created_at) SELECT 'i-'||lpad(i::text,3,'0'),'secret-'||i,now()+interval '1 day','2020-01-01' FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO batches(id,preview_id,operation,idempotency_key,request_fingerprint,created_at) SELECT 'b-'||lpad(i::text,3,'0'),'preview','set_active','idem-'||i,'fingerprint','2020-01-01' FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO batch_items(id,batch_id,state) VALUES('item','b-234','pending')`)
	exec(`INSERT INTO tasks(id,kind,state,batch_id,agent_id,created_at) SELECT 't-'||lpad(i::text,3,'0'),'sync','pending','b-001','a-001','2020-01-01' FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO tasks(id,kind,state,batch_id,agent_id) VALUES('excluded-task','sync','failed','other','other')`)
	exec(`INSERT INTO events(id,kind,state,message,created_at) SELECT 'e-'||lpad(i::text,3,'0'),'test','open','literal %_\ marker','2020-01-01' FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO events(id,kind,state,message) VALUES('excluded-event','test','acked','literal XXX marker')`)
	exec(`INSERT INTO audit_logs(id,actor_id,actor_username,action,created_at) SELECT 'l-'||lpad(i::text,3,'0'),'admin-id','operator','change','2020-01-01' FROM generate_series(234,0,-1) i`)
	exec(`INSERT INTO audit_logs(id,actor_id,action) VALUES('excluded-log','other','login')`)
	exec(`UPDATE control_state SET recovery_mode=true WHERE id=1`)
	// All list SQL, including summary and cursor-name queries, must work on a
	// genuinely read-only connection in recovery mode. A write fails at PostgreSQL.
	st.DB.SetMaxOpenConns(1)
	exec(`SET default_transaction_read_only=on`)
	s := &Server{Store: st}
	handlers := map[string]http.HandlerFunc{"agents": s.handleAdminAgents, "core-instances": s.handleAdminCoreInstances, "invitations": s.handleListInvitations, "batches": s.handleListBatches, "tasks": s.handleOperationsTasks, "events": s.handleOperationsEvents, "audit-logs": s.handleOperationsAuditLogs}
	filters := map[string]string{"tasks": "state=pending&batch_id=b-001&agent_id=a-001", "events": "state=open&q=" + url.QueryEscape(`%_\`), "audit-logs": "action=change&actor=operator"}
	call := func(endpoint, query, role, admin string) (int, map[string]any) {
		r := httptest.NewRequest("GET", "/api/v1/admin/"+endpoint+"?"+query, nil)
		r = r.WithContext(context.WithValue(r.Context(), ctxMember, &domain.Member{ID: admin, Role: role}))
		w := httptest.NewRecorder()
		s.requireAdmin(handlers[endpoint])(w, r)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code == 400 {
			e := body["error"].(map[string]any)
			fields := e["details"].(map[string]any)["field_errors"].(map[string]any)
			want := domain.CodeValidationFailed
			if r.URL.Query().Get("cursor") != "" {
				want = domain.CodeCursorInvalid
				reason := fields["cursor"]
				if reason != "invalid" && reason != "expired_scope" {
					t.Errorf("cursor reason=%v", reason)
				}
			}
			if e["code"] != want {
				t.Errorf("query %s: code=%v want=%s", query, e["code"], want)
			}
		}
		return w.Code, body
	}
	for _, endpoint := range []string{"agents", "core-instances", "invitations", "batches", "tasks", "events", "audit-logs"} {
		t.Run(endpoint, func(t *testing.T) {
			query := filters[endpoint]
			cursor := ""
			first := ""
			got := []string{}
			for page := 0; page < 10; page++ {
				status, body := call(endpoint, query+"&limit=71&cursor="+url.QueryEscape(cursor), "admin", "admin-id")
				if status != 200 {
					t.Fatalf("status=%d body=%v", status, body)
				}
				items := body["items"].([]any)
				if len(items) > 71 {
					t.Fatal("limit ignored")
				}
				for _, raw := range items {
					v := raw.(map[string]any)
					got = append(got, v["id"].(string))
					if endpoint == "agents" && v["token_hash"] != nil {
						t.Fatal("token leaked")
					}
					if endpoint == "invitations" && v["code"] != nil {
						t.Fatal("code leaked")
					}
					if endpoint == "batches" && v["id"] == "b-234" {
						if v["summary"].(map[string]any)["total"] != float64(1) {
							t.Fatal("batch counts lost")
						}
					}
				}
				if body["next_cursor"] == nil {
					break
				}
				cursor = body["next_cursor"].(string)
				if page == 0 {
					first = cursor
				}
				if page == 9 {
					t.Fatal("nonterminating cursor")
				}
			}
			expected := []string{}
			prefix := map[string]string{"agents": "a", "core-instances": "c", "invitations": "i", "batches": "b", "tasks": "t", "events": "e", "audit-logs": "l"}[endpoint]
			for i := 0; i < 235; i++ {
				n := 234 - i
				if endpoint == "agents" || endpoint == "core-instances" {
					n = i
				}
				expected = append(expected, fmt.Sprintf("%s-%03d", prefix, n))
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("order/count got=%d want=235: %v", len(got), got)
			}
			if first == "" {
				t.Fatal("truncated collection claimed complete")
			}
			for _, tc := range []struct {
				limit string
				want  int
			}{{"", 50}, {"999", 200}, {"0", 1}} {
				status, b := call(endpoint, query+"&limit="+tc.limit, "admin", "admin-id")
				if status != 200 || len(b["items"].([]any)) != tc.want || b["next_cursor"] == nil {
					t.Fatalf("limit %s: %d %v", tc.limit, status, b)
				}
			}
			for _, bad := range []string{"!invalid", first[:len(first)-4] + "AAAA"} {
				if status, _ := call(endpoint, query+"&cursor="+url.QueryEscape(bad), "admin", "admin-id"); status != 400 {
					t.Fatal("bad cursor accepted")
				}
			}
			if status, _ := call(endpoint, query+"&cursor="+url.QueryEscape(first), "admin", "another-admin"); status != 400 {
				t.Fatal("cross-admin cursor accepted")
			}
			other := "agents"
			if endpoint == other {
				other = "invitations"
			}
			if status, _ := call(other, "cursor="+url.QueryEscape(first), "admin", "admin-id"); status != 400 {
				t.Fatal("cross-endpoint cursor accepted")
			}
			if status, _ := call(endpoint, query, "member", "member-id"); status != 403 {
				t.Fatalf("member permission=%d", status)
			}
			if filters[endpoint] != "" {
				if status, _ := call(endpoint, "cursor="+url.QueryEscape(first), "admin", "admin-id"); status != 400 {
					t.Fatal("cross-filter cursor accepted")
				}
			}
			// An exact final page must not advertise a phantom following page.
			status, tail := call(endpoint, query+"&limit=164&cursor="+url.QueryEscape(first), "admin", "admin-id")
			if status != 200 || len(tail["items"].([]any)) != 164 || tail["next_cursor"] != nil {
				t.Fatalf("exact final page: %d %v", status, tail)
			}
			if status, _ := call(endpoint, query+"&limit=oops", "admin", "admin-id"); status != 400 {
				t.Fatal("invalid limit accepted")
			}
		})
	}
	// Each declared filter independently excludes mismatches; actor accepts either
	// stable ID or exact username. Wildcards in q are literal, not SQL patterns.
	for _, tc := range []struct {
		endpoint, query string
		count           int
	}{{"tasks", "state=failed", 1}, {"tasks", "batch_id=other", 1}, {"tasks", "agent_id=other", 1}, {"events", "state=acked", 1}, {"events", "q=XXX", 1}, {"audit-logs", "action=login", 1}, {"audit-logs", "actor=other", 1}, {"audit-logs", "actor=admin-id", 200}, {"events", "q=not-found", 0}} {
		status, b := call(tc.endpoint, tc.query+"&limit=200", "admin", "admin-id")
		if status != 200 || len(b["items"].([]any)) != tc.count {
			t.Fatalf("filter %s %s: %d %v", tc.endpoint, tc.query, status, b)
		}
	}
	if status, _ := call("events", "q="+strings.Repeat("x", 65), "admin", "admin-id"); status != 400 {
		t.Fatal("oversized q accepted")
	}
}
