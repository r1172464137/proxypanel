package store

// Regression for the missing batch history endpoint.
//
// The frontend shipped a batch list page and the API registered
// GET /api/v1/admin/batches/{id} but not the collection itself: a GET on
// /batches fell through to the {id} pattern's method set and answered 405, so the
// history page could only render an error. These tests pin ListBatches' ordering,
// its per-state counters and its limit handling.

import (
	"context"
	"fmt"
	"testing"
)

// seedBatch inserts one batch plus the given item states.
func seedBatch(t *testing.T, st *Store, id, state string, ageMinutes int, itemStates ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO previews
		(id, operation, payload, plan, rules_version, member_set_digest, expires_at)
		VALUES ('prv_'||$1,'set_active','{}','{}',1,'',now() + interval '1 hour')
		ON CONFLICT (id) DO NOTHING`, id); err != nil {
		t.Fatalf("seed preview: %v", err)
	}
	// A finished state also carries completed_at; an unfinished one must not.
	var completed any
	if state == "applied" || state == "failed" {
		completed = state
	}
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO batches
		(id, preview_id, operation, state, idempotency_key, request_fingerprint, created_at, completed_at)
		VALUES ($1,'prv_'||$1,'set_active',$2,'ik-'||$1,'fp',
		        now() - make_interval(mins => $3::int),
		        CASE WHEN $4::text IS NULL THEN NULL ELSE now() - make_interval(mins => $3::int) END)`,
		id, state, ageMinutes, completed); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	for i, s := range itemStates {
		if _, err := st.DB.ExecContext(ctx, `INSERT INTO batch_items
			(id, batch_id, member_id, node_id, state) VALUES ($1,$2,$3,'nd_lb',$4)`,
			fmt.Sprintf("bi_%s_%d", id, i), id, fmt.Sprintf("mbr_%d", i), s); err != nil {
			t.Fatalf("seed item: %v", err)
		}
	}
}

func TestListBatchesReturnsNewestFirstWithCounters(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	seedBatch(t, st, "bat_old", "applied", 120, "succeeded", "succeeded", "failed")
	seedBatch(t, st, "bat_new", "pending", 5, "pending")

	got, err := st.ListBatches(ctx, 50)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d batches, want 2", len(got))
	}
	// The history page shows most recent first.
	if got[0].ID != "bat_new" || got[1].ID != "bat_old" {
		t.Fatalf("order = %s, %s; want bat_new then bat_old", got[0].ID, got[1].ID)
	}

	old := got[1]
	if v, _ := old.Summary.Int("succeeded"); v != 2 {
		t.Fatalf("old.succeeded = %d, want 2 (summary %+v)", v, old.Summary)
	}
	if v, _ := old.Summary.Int("failed"); v != 1 {
		t.Fatalf("old.failed = %d, want 1", v)
	}
	if v, _ := old.Summary.Int("total"); v != 3 {
		t.Fatalf("old.total = %d, want 3", v)
	}

	newest := got[0]
	if v, _ := newest.Summary.Int("pending"); v != 1 {
		t.Fatalf("new.pending = %d, want 1", v)
	}
	if newest.CompletedAt != nil {
		t.Fatalf("an unfinished batch must have no completed_at, got %v", newest.CompletedAt)
	}
	if old.CompletedAt == nil {
		t.Fatal("a completed batch should carry completed_at when the column is set")
	}
}

func TestListBatchesRespectsLimitAndHandlesEmpty(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	got, err := st.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches on empty table: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty table returned %d rows", len(got))
	}

	for i := 0; i < 5; i++ {
		seedBatch(t, st, fmt.Sprintf("bat_l%d", i), "applied", i*10, "succeeded")
	}
	got, err = st.ListBatches(ctx, 2)
	if err != nil {
		t.Fatalf("ListBatches with limit: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit=2 returned %d rows, want 2", len(got))
	}
	// The limit must keep the newest rows, not an arbitrary two.
	if got[0].ID != "bat_l0" || got[1].ID != "bat_l1" {
		t.Fatalf("limit kept %s, %s; want the two newest (bat_l0, bat_l1)", got[0].ID, got[1].ID)
	}

	// A clamp of 0 or negative must not return everything.
	if got, err := st.ListBatches(ctx, 0); err != nil || len(got) > 5 {
		t.Fatalf("limit=0 returned %d rows (err=%v)", len(got), err)
	}
}
