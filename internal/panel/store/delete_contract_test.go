package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
)

func deleteFacts(t *testing.T, st *Store) string {
	t.Helper()
	var raw string
	// Exact row snapshots, including timestamps and ledger payloads, not just counts.
	err := st.DB.QueryRow(`SELECT jsonb_build_array(
 (SELECT coalesce(jsonb_agg(to_jsonb(b) ORDER BY id),'[]') FROM member_bindings b),
 (SELECT coalesce(jsonb_agg(to_jsonb(u) ORDER BY id),'[]') FROM usage_entries u),
 (SELECT coalesce(jsonb_agg(to_jsonb(g) ORDER BY subscription_id,node_group_id),'[]') FROM subscription_node_groups g),
 (SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY id),'[]') FROM members m))::text`).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDeleteContractStoreHistory(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	g := mustCreateNodeGroup(t, st, "history-group")
	sub := mustCreateSubscriptionWithGroups(t, st, "history", []string{g.ID})
	exec(`INSERT INTO members(id,username,password_hash,archived,subscription_id) VALUES('m','m','x',true,$1)`, sub.ID)
	exec(`INSERT INTO member_bindings(id,member_id,subscription_id,ended_at,reason) VALUES('b','m',$1,now(),'history')`, sub.ID)
	exec(`INSERT INTO usage_entries(agent_id,batch_id,member_id,node_id,period_start,period_end,up_bytes,down_bytes,sample_started_at,sample_ended_at) VALUES('a','batch','m','node',now()-interval '1 day',now()+interval '1 day',123,456,now()-interval '1 hour',now())`)
	before := deleteFacts(t, st)
	var controlBefore string
	if err := st.DB.QueryRow(`SELECT to_jsonb(c)::text FROM control_state c`).Scan(&controlBefore); err != nil {
		t.Fatal(err)
	}
	type result struct {
		sub *domain.Subscription
		err error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	for i := 0; i < cap(results); i++ {
		go func() {
			<-start
			v, err := st.DeleteSubscription(ctx, sub.ID)
			results <- result{v, err}
		}()
	}
	close(start)
	var got *domain.Subscription
	for i := 0; i < cap(results); i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if got == nil {
			got = r.sub
		} else if !reflect.DeepEqual(got, r.sub) {
			t.Fatalf("concurrent DELETE snapshots differ: %+v != %+v", got, r.sub)
		}
	}
	var controlAfter string
	if err := st.DB.QueryRow(`SELECT to_jsonb(c)::text FROM control_state c`).Scan(&controlAfter); err != nil {
		t.Fatal(err)
	}
	if controlBefore != controlAfter {
		t.Fatal("retirement advanced control generation/revision")
	}
	if !got.Archived || got.Version != sub.Version+1 {
		t.Fatalf("retirement: %+v", got)
	}
	expected := *sub
	expected.Archived = true
	expected.Version++
	expected.UpdatedAt = got.UpdatedAt
	if !reflect.DeepEqual(&expected, got) {
		a, _ := json.Marshal(expected)
		b, _ := json.Marshal(got)
		t.Fatalf("snapshot drift: %s != %s", a, b)
	}
	if after := deleteFacts(t, st); before != after {
		t.Fatalf("history/ledger/groups/member mutation: %s != %s", before, after)
	}
	again, err := st.DeleteSubscription(ctx, sub.ID)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("idempotence: %+v %v", again, err)
	}
	exec(`UPDATE members SET archived=false WHERE id='m'`)
	facts := deleteFacts(t, st)
	_, err = st.DeleteSubscription(ctx, sub.ID)
	if de, ok := err.(*domain.Error); !ok || de.Code != domain.CodeNodeInUse {
		t.Fatalf("archived with current binding: %v", err)
	}
	if facts != deleteFacts(t, st) {
		t.Fatal("409 side effects")
	}
}

// Both DELETEs must wait for the control row before locking their object. A
// reference writer holding that row commits first; DELETE must see its commit.
func TestDeleteContractControlSerialization(t *testing.T) {
	for _, kind := range []string{"template", "subscription"} {
		t.Run(kind, func(t *testing.T) {
			st := newRegStore(t)
			ctx := context.Background()
			sub := mustCreateSubscription(t, st, "serial")
			if _, err := st.DB.Exec(`INSERT INTO templates(id,name,format,content) VALUES('t','t','xray','{}')`); err != nil {
				t.Fatal(err)
			}
			writer, err := st.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Rollback()
			if err := requireWritableTx(ctx, writer); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if kind == "template" {
					done <- st.DeleteTemplate(ctx, "t")
				} else {
					_, err := st.DeleteSubscription(ctx, sub.ID)
					done <- err
				}
			}()
			var writerPID int
			if err := writer.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				select {
				case err := <-done:
					t.Fatalf("DELETE bypassed control lock: %v", err)
				default:
				}
				var waiting bool
				if err := st.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, writerPID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("DELETE never waited for control row")
				}
				time.Sleep(10 * time.Millisecond)
			}
			// NOWAIT also detects a reversed object->control lock order without hanging.
			table, id := "templates", "t"
			if kind == "subscription" {
				table, id = "subscriptions", sub.ID
			}
			if _, err := writer.ExecContext(ctx, `SELECT id FROM `+table+` WHERE id=$1 FOR UPDATE NOWAIT`, id); err != nil {
				t.Fatalf("object locked ahead of control: %v", err)
			}
			if kind == "template" {
				_, err = writer.ExecContext(ctx, `UPDATE subscriptions SET template_id='t' WHERE id=$1`, sub.ID)
			} else {
				_, err = writer.ExecContext(ctx, `INSERT INTO members(id,username,password_hash,subscription_id) VALUES('m','m','x',$1)`, sub.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = writer.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("DELETE did not finish")
			}
			code := domain.CodeConflict
			if kind == "subscription" {
				code = domain.CodeNodeInUse
			}
			if de, ok := err.(*domain.Error); !ok || de.Code != code {
				t.Fatalf("committed reference ignored: %v", err)
			}
		})
	}
}
