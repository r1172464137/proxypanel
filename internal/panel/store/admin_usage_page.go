package store

import (
	"context"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/usagecalc"
)

// ListAdminUsagePage preserves ListUsage's cycle-start filtering and complete
// aggregate identity. The seek predicate never cuts through an aggregate.
// Mixed ordering: period_start DESC; member_id, node_id, period_end ASC.
func (s *Store) ListAdminUsagePage(ctx context.Context, f UsageFilter, after []string) ([]domain.UsageRow, error) {
	q := `SELECT member_id,node_id,period_start,period_end,SUM(up_bytes),SUM(down_bytes),
 BOOL_OR(estimated),BOOL_OR(boundary_split),BOOL_OR(correction) FROM usage_entries WHERE true`
	args := []any{}
	add := func(v any) string { args = append(args, v); return "$" + itoa(len(args)) }
	if f.MemberID != "" {
		q += ` AND member_id=` + add(f.MemberID)
	}
	if f.NodeID != "" {
		q += ` AND node_id=` + add(f.NodeID)
	}
	if f.From != nil {
		q += ` AND period_start>=` + add(f.From.UTC())
	}
	if f.To != nil {
		q += ` AND period_start<` + add(f.To.UTC())
	}
	if after != nil {
		start, err := time.Parse(time.RFC3339Nano, after[0])
		if err != nil {
			return nil, err
		}
		end, err := time.Parse(time.RFC3339Nano, after[3])
		if err != nil {
			return nil, err
		}
		p := add(start)
		m := add(after[1])
		n := add(after[2])
		e := add(end)
		q += ` AND (period_start<` + p + ` OR (period_start=` + p + ` AND (member_id COLLATE "C",node_id COLLATE "C",period_end)>(` + m + ` COLLATE "C",` + n + ` COLLATE "C",` + e + `::timestamptz)))`
	}
	// Do not clamp limit+1 through the legacy 200-row cap.
	limit := f.Limit
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	q += ` GROUP BY member_id,node_id,period_start,period_end ORDER BY period_start DESC,member_id COLLATE "C",node_id COLLATE "C",period_end LIMIT ` + add(limit+1)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.UsageRow, 0, limit+1)
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
