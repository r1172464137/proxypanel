package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/period"
)

// PeriodRecord is an immutable, explicitly supplied historical interval.
// CreatedAt is database recording metadata, not evidence of interval boundaries;
// it is read-only and must be zero on input to recordMemberPeriodTx.
type PeriodRecord struct {
	MemberID    string
	PeriodStart time.Time
	PeriodEnd   time.Time
	AnchorDay   int
	Timezone    string
	Source      string
	CreatedAt   time.Time
}

var (
	ErrPeriodInvalid  = errors.New("invalid member period")
	ErrPeriodConflict = errors.New("member period conflicts with recorded history")
)

func validatePeriodRange(start, end time.Time) error {
	if start.IsZero() || end.IsZero() || !start.Before(end) || start.Nanosecond()%1000 != 0 || end.Nanosecond()%1000 != 0 {
		return fmt.Errorf("%w: require start < end at PostgreSQL microsecond precision", ErrPeriodInvalid)
	}
	return nil
}

func validatePeriodRecord(rec PeriodRecord) error {
	if rec.MemberID == "" || rec.AnchorDay < 1 || rec.AnchorDay > 31 || !rec.CreatedAt.IsZero() {
		return fmt.Errorf("%w: member, anchor or recording metadata", ErrPeriodInvalid)
	}
	if err := validatePeriodRange(rec.PeriodStart, rec.PeriodEnd); err != nil {
		return err
	}
	if _, err := period.LoadTimezone(rec.Timezone); err != nil {
		return fmt.Errorf("%w: %v", ErrPeriodInvalid, err)
	}
	switch rec.Source {
	case "binding", "roll_forward", "proven_current", "proven_ledger":
	default:
		return fmt.Errorf("%w: source", ErrPeriodInvalid)
	}
	return nil
}

// recordMemberPeriodTx appends only: identical primary key and content is a no-op,
// all other overlap is a conflict. The caller owns commit/rollback and must roll
// back on error. Every writer must use this helper, locking members before history.
// READ COMMITTED is required: a fixed snapshot could miss a writer that committed
// while this transaction waited for the member lock. No existing row is updated.
func recordMemberPeriodTx(ctx context.Context, tx *sql.Tx, rec PeriodRecord) error {
	if err := validatePeriodRecord(rec); err != nil {
		return err
	}
	if tx == nil {
		return fmt.Errorf("%w: transaction required", ErrPeriodInvalid)
	}
	var isolation string
	if err := tx.QueryRowContext(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return err
	}
	if isolation != "read committed" {
		return fmt.Errorf("%w: READ COMMITTED transaction required", ErrPeriodInvalid)
	}
	var memberID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM members WHERE id=$1 FOR UPDATE`, rec.MemberID).Scan(&memberID); err != nil {
		return err
	}
	var old PeriodRecord
	err := tx.QueryRowContext(ctx, `SELECT member_id, period_start, period_end, anchor_day, timezone, source, created_at
  FROM member_periods WHERE member_id=$1 AND period_start=$2`, rec.MemberID, rec.PeriodStart).Scan(
		&old.MemberID, &old.PeriodStart, &old.PeriodEnd, &old.AnchorDay, &old.Timezone, &old.Source, &old.CreatedAt)
	if err == nil {
		if old.PeriodEnd.Equal(rec.PeriodEnd) && old.AnchorDay == rec.AnchorDay && old.Timezone == rec.Timezone && old.Source == rec.Source {
			return nil
		}
		return ErrPeriodConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var overlap bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM member_periods
  WHERE member_id=$1 AND period_start < $3 AND period_end > $2)`, rec.MemberID, rec.PeriodStart, rec.PeriodEnd).Scan(&overlap); err != nil {
		return err
	}
	if overlap {
		return ErrPeriodConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO member_periods (member_id, period_start, period_end, anchor_day, timezone, source)
  VALUES ($1,$2,$3,$4,$5,$6)`, rec.MemberID, rec.PeriodStart, rec.PeriodEnd, rec.AnchorDay, rec.Timezone, rec.Source)
	return err
}

// recordRunningPeriodTx registers the explicit persisted interval, not a
// calendar reconstruction. Migration preflight is responsible for proving old
// persisted data before business startup. Existing provenance stays immutable.
func recordRunningPeriodTx(ctx context.Context, tx *sql.Tx, m *domain.Member, source string) error {
	if m.PeriodStart == nil || m.PeriodEnd == nil {
		return nil
	}
	var end time.Time
	var anchor int
	var zone string
	err := tx.QueryRowContext(ctx, `SELECT period_end, anchor_day, timezone FROM member_periods WHERE member_id=$1 AND period_start=$2`, m.ID, *m.PeriodStart).Scan(&end, &anchor, &zone)
	if err == nil {
		if !end.Equal(*m.PeriodEnd) || anchor != m.PeriodAnchorDay || zone != m.PeriodTimezone {
			return ErrPeriodConflict
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	return recordMemberPeriodTx(ctx, tx, PeriodRecord{MemberID: m.ID, PeriodStart: *m.PeriodStart,
		PeriodEnd: *m.PeriodEnd, AnchorDay: m.PeriodAnchorDay, Timezone: m.PeriodTimezone, Source: source})
}

// recordedWindowsTx refuses any gap, even for a zero-byte sample. Allocation
// must not redistribute the unprovable part over the remaining known windows.
func recordedWindowsTx(ctx context.Context, tx *sql.Tx, memberID string, start, end time.Time) ([]period.Boundary, error) {
	rows, err := tx.QueryContext(ctx, `SELECT period_start, period_end FROM member_periods
		WHERE member_id=$1 AND period_start < $3 AND period_end > $2 ORDER BY period_start`, memberID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cursor := start
	var windows []period.Boundary
	for rows.Next() {
		var w period.Boundary
		if err := rows.Scan(&w.Start, &w.End); err != nil {
			return nil, err
		}
		if w.Start.After(cursor) {
			return nil, ledgerUnattributable()
		}
		if len(windows) > 0 && w.Start.Before(windows[len(windows)-1].End) {
			return nil, ledgerUnattributable()
		}
		windows = append(windows, w)
		cursor = w.End
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if cursor.Before(end) {
		return nil, ledgerUnattributable()
	}
	return windows, nil
}

func ledgerUnattributable() error {
	return domain.E("LEDGER_UNATTRIBUTABLE", 422, "the complete sample interval cannot be proven from recorded history")
}

// ListMemberPeriods returns recorded intervals intersecting [start,end), ordered
// by start. It neither infers missing history nor consults current member rules.
func (s *Store) ListMemberPeriods(ctx context.Context, memberID string, start, end time.Time) ([]PeriodRecord, error) {
	if memberID == "" {
		return nil, fmt.Errorf("%w: member required", ErrPeriodInvalid)
	}
	if err := validatePeriodRange(start, end); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT member_id, period_start, period_end, anchor_day, timezone, source, created_at
  FROM member_periods WHERE member_id=$1 AND period_start < $3 AND period_end > $2 ORDER BY period_start`, memberID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]PeriodRecord, 0)
	for rows.Next() {
		var rec PeriodRecord
		if err := rows.Scan(&rec.MemberID, &rec.PeriodStart, &rec.PeriodEnd, &rec.AnchorDay, &rec.Timezone, &rec.Source, &rec.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}
