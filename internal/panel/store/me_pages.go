package store

import (
	"context"
	"database/sql"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/usagecalc"
)

// ListMeUsagePage returns complete per-node/cycle aggregates, not a cycle
// summary. period_end breaks ties even for historical rows sharing a start.
// The HTTP cursor is validated and member-bound before calling this helper.
func (s *Store) ListMeUsagePage(ctx context.Context, member string, limit int, after []string) ([]domain.UsageRow, error) {
	args := []any{member}
	q := `SELECT member_id,node_id,period_start,period_end,SUM(up_bytes),SUM(down_bytes),
 BOOL_OR(estimated),BOOL_OR(boundary_split),BOOL_OR(correction)
 FROM usage_entries WHERE member_id=$1`
	if after != nil {
		start, _ := time.Parse(time.RFC3339Nano, after[0])
		end, _ := time.Parse(time.RFC3339Nano, after[2])
		args = append(args, start, after[1], end)
		q += ` AND (period_start<$2 OR (period_start=$2 AND (node_id COLLATE "C",period_end)>($3 COLLATE "C",$4)))`
	}
	args = append(args, clampLimit(limit)+1)
	q += ` GROUP BY member_id,node_id,period_start,period_end ORDER BY period_start DESC,node_id COLLATE "C",period_end LIMIT $` + itoa(len(args))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.UsageRow, 0)
	for rows.Next() {
		var v domain.UsageRow
		if err := rows.Scan(&v.MemberID, &v.NodeID, &v.PeriodStart, &v.PeriodEnd, &v.UpBytes, &v.DownBytes, &v.Estimated, &v.BoundarySplit, &v.Correction); err != nil {
			return nil, dbErr(err)
		}
		v.PeriodStart = utc(v.PeriodStart)
		v.PeriodEnd = utc(v.PeriodEnd)
		v.Direction = usagecalc.Direction
		v.AlgorithmVersion = usagecalc.MeteringAlgorithmVersion
		out = append(out, v)
	}
	return out, dbErr(rows.Err())
}

// ListMeAdjustments returns the complete member-scoped collection on page one.
// It deliberately does not call the capped public/admin list helper.
func (s *Store) ListMeAdjustments(ctx context.Context, member string) ([]domain.QuotaAdjustment, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,member_id,bytes,reason,period_start,created_by,created_at FROM quota_adjustments WHERE member_id=$1 ORDER BY created_at DESC,id COLLATE "C" DESC`, member)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.QuotaAdjustment, 0)
	for rows.Next() {
		var v domain.QuotaAdjustment
		var ps sql.NullTime
		if err := rows.Scan(&v.ID, &v.MemberID, &v.Bytes, &v.Reason, &ps, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, dbErr(err)
		}
		if ps.Valid {
			t := ps.Time.UTC()
			v.PeriodStart = &t
		}
		v.CreatedAt = utc(v.CreatedAt)
		out = append(out, v)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) ListMeSessionsPage(ctx context.Context, member, current string, limit int, after []string) ([]domain.Session, error) {
	args := []any{member}
	q := `SELECT id,member_id,ip,user_agent,created_at,last_seen_at,expires_at,revoked_at FROM sessions WHERE member_id=$1`
	if after != nil {
		t, _ := time.Parse(time.RFC3339Nano, after[0])
		args = append(args, t, after[1])
		q += ` AND (created_at,id COLLATE "C")<($2,$3 COLLATE "C")`
	}
	args = append(args, clampLimit(limit)+1)
	q += ` ORDER BY created_at DESC,id COLLATE "C" DESC LIMIT $` + itoa(len(args))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Session, 0)
	for rows.Next() {
		var v domain.Session
		var seen, revoked sql.NullTime
		if err := rows.Scan(&v.ID, &v.MemberID, &v.IP, &v.UserAgent, &v.CreatedAt, &seen, &v.ExpiresAt, &revoked); err != nil {
			return nil, dbErr(err)
		}
		v.CreatedAt = utc(v.CreatedAt)
		v.ExpiresAt = utc(v.ExpiresAt)
		if seen.Valid {
			t := seen.Time.UTC()
			v.LastSeenAt = &t
		}
		if revoked.Valid {
			t := revoked.Time.UTC()
			v.RevokedAt = &t
		}
		v.Current = v.ID == current
		out = append(out, v)
	}
	return out, dbErr(rows.Err())
}
