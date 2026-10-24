package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
)

func periodTestRecord() PeriodRecord {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	return PeriodRecord{MemberID: "member", PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0), AnchorDay: 1, Timezone: "UTC", Source: "binding"}
}

func TestPeriodRecordValidation(t *testing.T) {
	base := periodTestRecord()
	for _, source := range []string{"binding", "roll_forward", "proven_current", "proven_ledger"} {
		rec := base
		rec.Source = source
		if err := validatePeriodRecord(rec); err != nil {
			t.Fatal(err)
		}
	}
	for _, zone := range []string{"UTC", "Asia/Shanghai", "America/New_York"} {
		rec := base
		rec.Timezone = zone
		if err := validatePeriodRecord(rec); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(*PeriodRecord){
		"empty member": func(r *PeriodRecord) { r.MemberID = "" },
		"equal":        func(r *PeriodRecord) { r.PeriodEnd = r.PeriodStart },
		"reversed":     func(r *PeriodRecord) { r.PeriodEnd = r.PeriodStart.Add(-time.Hour) },
		"zero":         func(r *PeriodRecord) { r.PeriodStart = time.Time{} },
		"precision":    func(r *PeriodRecord) { r.PeriodStart = r.PeriodStart.Add(time.Nanosecond) },
		"anchor zero":  func(r *PeriodRecord) { r.AnchorDay = 0 },
		"anchor 32":    func(r *PeriodRecord) { r.AnchorDay = 32 },
		"empty zone":   func(r *PeriodRecord) { r.Timezone = "" },
		"local":        func(r *PeriodRecord) { r.Timezone = "Local" },
		"unknown zone": func(r *PeriodRecord) { r.Timezone = "Not/AZone" },
		"offset":       func(r *PeriodRecord) { r.Timezone = "+08:00" },
		"source":       func(r *PeriodRecord) { r.Source = "guessed" },
		"metadata":     func(r *PeriodRecord) { r.CreatedAt = r.PeriodStart },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rec := base
			mutate(&rec)
			if err := validatePeriodRecord(rec); !errors.Is(err, ErrPeriodInvalid) {
				t.Fatalf("got %v", err)
			}
		})
	}
	for _, anchor := range []int{1, 31} {
		rec := base
		rec.AnchorDay = anchor
		if err := validatePeriodRecord(rec); err != nil {
			t.Fatal(err)
		}
	}
}

func periodTestMember(t *testing.T, st *Store) PeriodRecord {
	t.Helper()
	rec := periodTestRecord()
	// Storage primitive tests need a subject with no registered interval yet;
	// bound CreateMember now correctly records history itself.
	member, err := st.CreateMember(context.Background(), &domain.Member{ID: newID("mem"), Username: "period-member", PasswordHash: "fixture", Role: "member", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	rec.MemberID = member.ID
	return rec
}

func periodTestWrite(t *testing.T, st *Store, rec PeriodRecord) error {
	t.Helper()
	tx, err := st.DB.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := recordMemberPeriodTx(context.Background(), tx, rec); err != nil {
		return err
	}
	return tx.Commit()
}

func TestPeriodRecordHistory(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	rec := periodTestMember(t, st)
	if err := periodTestWrite(t, st, rec); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListMemberPeriods(ctx, rec.MemberID, rec.PeriodStart, rec.PeriodEnd)
	if err != nil || len(before) != 1 {
		t.Fatalf("initial history: %v %v", before, err)
	}
	if err := periodTestWrite(t, st, rec); err != nil {
		t.Fatalf("idempotence: %v", err)
	}
	after, err := st.ListMemberPeriods(ctx, rec.MemberID, rec.PeriodStart, rec.PeriodEnd)
	if err != nil || len(after) != 1 || !after[0].CreatedAt.Equal(before[0].CreatedAt) {
		t.Fatalf("idempotence changed history: %v %v", after, err)
	}
	for _, mutate := range []func(*PeriodRecord){
		func(r *PeriodRecord) { r.PeriodEnd = r.PeriodEnd.Add(time.Hour) },
		func(r *PeriodRecord) { r.AnchorDay = 2 },
		func(r *PeriodRecord) { r.Timezone = "Asia/Shanghai" },
		func(r *PeriodRecord) { r.Source = "proven_current" },
		func(r *PeriodRecord) { r.PeriodStart = r.PeriodStart.Add(time.Hour) },
		func(r *PeriodRecord) { r.PeriodStart = r.PeriodStart.Add(-time.Hour) },
	} {
		bad := rec
		mutate(&bad)
		if err := periodTestWrite(t, st, bad); !errors.Is(err, ErrPeriodConflict) {
			t.Fatalf("expected conflict: %v", err)
		}
	}
	next := rec
	next.PeriodStart = rec.PeriodEnd
	next.PeriodEnd = next.PeriodStart.AddDate(0, 1, 0)
	next.Source = "roll_forward"
	if err := periodTestWrite(t, st, next); err != nil {
		t.Fatalf("adjacency: %v", err)
	}
	rows, err := st.ListMemberPeriods(ctx, rec.MemberID, next.PeriodStart, next.PeriodEnd)
	if err != nil || len(rows) != 1 || !rows[0].PeriodStart.Equal(next.PeriodStart) {
		t.Fatalf("half-open query: %v %v", rows, err)
	}
	rows, err = st.ListMemberPeriods(ctx, rec.MemberID, rec.PeriodStart, next.PeriodEnd)
	if err != nil || len(rows) != 2 || !rows[0].PeriodStart.Equal(rec.PeriodStart) {
		t.Fatalf("ordered history: %v %v", rows, err)
	}
	// A rollback must not leave a record.
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	later := next
	later.PeriodStart = next.PeriodEnd
	later.PeriodEnd = later.PeriodStart.AddDate(0, 1, 0)
	if err := recordMemberPeriodTx(ctx, tx, later); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	rows, err = st.ListMemberPeriods(ctx, rec.MemberID, later.PeriodStart, later.PeriodEnd)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rollback: %v %v", rows, err)
	}
	for _, isolation := range []sql.IsolationLevel{sql.LevelRepeatableRead, sql.LevelSerializable} {
		tx, err := st.DB.BeginTx(ctx, &sql.TxOptions{Isolation: isolation})
		if err != nil {
			t.Fatal(err)
		}
		err = recordMemberPeriodTx(ctx, tx, later)
		_ = tx.Rollback()
		if !errors.Is(err, ErrPeriodInvalid) {
			t.Fatalf("fixed snapshot accepted: %v", err)
		}
	}
}

func TestPeriodRecordConcurrent(t *testing.T) {
	for _, mode := range []string{"overlap", "identical", "adjacent"} {
		t.Run(mode, func(t *testing.T) {
			st := newRegStore(t)
			rec := periodTestMember(t, st)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			tx1, err := st.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx1.Rollback()
			tx2, err := st.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx2.Rollback()
			var pid int
			if err := tx2.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			// Initially no ledger rows exist. The first writer must still lock members.
			if err := recordMemberPeriodTx(ctx, tx1, rec); err != nil {
				t.Fatal(err)
			}
			second := rec
			if mode == "overlap" {
				second.PeriodStart = rec.PeriodStart.Add(time.Hour)
			}
			if mode == "adjacent" {
				second.PeriodStart = rec.PeriodEnd
				second.PeriodEnd = second.PeriodStart.AddDate(0, 1, 0)
			}
			done := make(chan error, 1)
			go func() { done <- recordMemberPeriodTx(ctx, tx2, second) }()
			// Observe an actual PostgreSQL lock wait, not a timing-only assumption.
			for {
				var waiting bool
				if err := st.DB.QueryRowContext(ctx, `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("writer bypassed member lock: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err := tx1.Commit(); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if mode == "overlap" {
				if !errors.Is(err, ErrPeriodConflict) {
					t.Fatalf("concurrent overlap: %v", err)
				}
				_ = tx2.Rollback()
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := tx2.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := st.ListMemberPeriods(ctx, rec.MemberID, rec.PeriodStart, rec.PeriodEnd.AddDate(0, 2, 0))
			want := 1
			if mode == "adjacent" {
				want = 2
			}
			if err != nil || len(rows) != want {
				t.Fatalf("rows=%d want=%d err=%v", len(rows), want, err)
			}
		})
	}
}

func TestPeriodSchemaOnly(t *testing.T) {
	st := newRegStore(t)
	for _, table := range []string{"member_periods", "node_server_history"} {
		var count int
		if err := st.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s not empty: %d %v", table, count, err)
		}
	}
}
