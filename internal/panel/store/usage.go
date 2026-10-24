package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/usagecalc"
)

// SubmitUsageBatch accounts an agent batch exactly once. The ack is only
// produced after the PostgreSQL transaction commits.
func (s *Store) SubmitUsageBatch(ctx context.Context, agent *domain.Agent, batch contracts.UsageBatch) (status string, err error) {
	hash := usagecalc.PayloadSHA256(batch)
	status = "accepted"
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		recovery, err := recoveryModeTx(ctx, tx)
		if err != nil {
			return err
		}
		// Serialize a batch without holding an Agent row while quota updates
		// publish to all Agents (which would invert the revision lock order).
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "usage:"+agent.ID+":"+batch.BatchID); err != nil {
			return dbErr(err)
		}
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM usage_batches
			WHERE agent_id=$1 AND batch_id=$2 FOR UPDATE`, agent.ID, batch.BatchID).Scan(&existing)
		switch {
		case err == nil:
			if existing != hash {
				return domain.E(domain.CodeBatchPayloadMismatch, 409,
					"batch_id already exists with a different payload").
					WithDetail("batch_id", batch.BatchID).WithDetail("existing_payload_sha256", existing)
			}
			status = "duplicate"
			return nil
		case err != sql.ErrNoRows:
			return dbErr(err)
		}

		validator := usagecalc.Validator{
			Now:           s.now(),
			AgentServerID: agent.ServerID,
			NodeHasHistory: func(nodeID, memberID string) bool {
				var n int
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE node_id=$1 AND member_id=$2`,
					nodeID, memberID).Scan(&n); err != nil {
					return false
				}
				return n > 0
			},
		}
		if rejects := usagecalc.Validate(batch, validator); len(rejects) != 0 {
			code := usagecalc.RejectCodePriority(rejects)
			status := 400
			switch code {
			case "BATCH_OWNERSHIP_MISMATCH":
				status = 403
			case "BATCH_CLOCK_SKEW":
				status = 422
			}
			e := domain.E(code, status, "usage batch rejected: %s", rejects[0].Message)
			e.Details = map[string]any{"rejects": rejects}
			return e
		}

		for _, sample := range batch.Samples {
			if err := nodeAuthorizedForRangeTx(ctx, tx, sample.NodeID, agent.ServerID, agent.ID, sample.StartedAt, sample.EndedAt); err != nil {
				return err
			}
		}
		// Cross-Agent batches may touch the same members in opposite sample
		// order. Acquire every member lock in the same order before advancing.
		memberIDs := make([]string, 0, len(batch.Samples))
		for _, sample := range batch.Samples {
			memberIDs = append(memberIDs, sample.MemberID)
		}
		locked, err := tx.QueryContext(ctx, `SELECT id FROM members WHERE id=ANY($1) ORDER BY id FOR UPDATE`, memberIDs)
		if err != nil {
			return dbErr(err)
		}
		for locked.Next() {
			var id string
			if err := locked.Scan(&id); err != nil {
				locked.Close()
				return err
			}
		}
		err = locked.Err()
		locked.Close()
		if err != nil {
			return dbErr(err)
		}
		startedAt, endedAt := batchBounds(batch.Samples)
		if _, err := tx.ExecContext(ctx, `INSERT INTO usage_batches
			(agent_id, batch_id, payload_sha256, status, sample_count, started_at, ended_at, core_generation)
			VALUES ($1,$2,$3,'accepted',$4,$5,$6,$7)`,
			agent.ID, batch.BatchID, hash, len(batch.Samples), nullableTime(startedAt), nullableTime(endedAt),
			firstGeneration(batch.Samples)); err != nil {
			return dbErr(err)
		}

		touched := map[string]bool{}
		ledgerPart := 0
		for _, sample := range batch.Samples {
			// The member row is locked so the cycle advance below cannot race
			// with another batch or a read path for the same member.
			m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, sample.MemberID))
			if err != nil {
				if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
					return domain.E(domain.CodeBatchOwnershipMismatch, 403, "unknown member in usage batch")
				}
				return err
			}
			// Accounting for the cycle that contains now is only correct once the
			// cycle itself was rolled forward: otherwise a new month's bytes are
			// booked against the previous period_start, which is what made the
			// quota never reset.
			if !recovery {
				if _, err := s.advanceMemberPeriodTx(ctx, tx, m, s.now()); err != nil {
					if errors.Is(err, ErrPeriodInvalid) || errors.Is(err, ErrPeriodConflict) {
						return ledgerUnattributable()
					}
					return err
				}
			}
			windows, err := recordedWindowsTx(ctx, tx, m.ID, sample.StartedAt, sample.EndedAt)
			if err != nil {
				return err
			}
			var currentStart time.Time
			if m.PeriodStart != nil {
				currentStart = *m.PeriodStart
			}
			allocs := usagecalc.Allocate(sample, windows, currentStart)
			if len(allocs) == 0 {
				continue
			}
			credID := ""
			_ = tx.QueryRowContext(ctx, `SELECT id FROM credentials WHERE node_id=$1 AND member_id=$2
				ORDER BY state='active' DESC, created_at DESC LIMIT 1`, sample.NodeID, sample.MemberID).Scan(&credID)
			for _, a := range allocs {
				if _, err := tx.ExecContext(ctx, `INSERT INTO usage_entries
					(agent_id, batch_id, member_id, node_id, credential_id, period_start, period_end, up_bytes, down_bytes,
					 sample_started_at, sample_ended_at, estimated, boundary_split, correction, core_generation, split_part)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
					`,
					agent.ID, batch.BatchID, sample.MemberID, sample.NodeID, credID, a.PeriodStart, a.PeriodEnd,
					a.UpBytes, a.DownBytes, sample.StartedAt.UTC(), sample.EndedAt.UTC(), a.Estimated,
					a.BoundarySplit, a.Correction, sample.CoreGeneration, ledgerPart); err != nil {
					return dbErr(err)
				}
				ledgerPart++
			}
			touched[sample.MemberID] = true
		}

		// Quota enforcement: the ledger is authoritative, excess revokes access
		// in the same transaction and schedules a revision bump.
		for memberID := range touched {
			// Recovery accepts attributable ledger telemetry, but never implicitly
			// relaxes quota state or recreates authorization credentials.
			if recovery {
				continue
			}
			if err := s.applyQuotaState(ctx, tx, memberID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if e, ok := err.(*domain.Error); ok && e.Code == "LEDGER_UNATTRIBUTABLE" {
			// The rejected batch and every partial entry rolled back. The event
			// is intentionally outside that transaction and contains no secrets.
			_ = s.RecordEvent(ctx, "ledger_unattributable", "warning", "agent", agent.ID,
				"usage batch requires historical evidence", "ledger_unattributable:"+agent.ID+":"+batch.BatchID)
		}
		return "", err
	}
	return status, nil
}

// applyQuotaState re-evaluates the ledger for the member's current cycle and
// flips over_quota, publishing the change (revision bump) in the same
// transaction. Both directions are published: a quota increase or a new cycle
// restores access and the agents must learn it now.
func (s *Store) applyQuotaState(ctx context.Context, tx *sql.Tx, memberID string) error {
	m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, memberID))
	if err != nil {
		return err
	}
	used, err := s.usedBytesQ(ctx, tx, memberID, m.PeriodStart)
	if err != nil {
		return err
	}
	quota, err := effectiveQuotaQ(ctx, tx, m)
	if err != nil {
		return err
	}
	over := quota != nil && used >= *quota
	if over == m.OverQuota {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE members SET over_quota=$2, updated_at=now() WHERE id=$1`, memberID, over); err != nil {
		return dbErr(err)
	}
	// Reconcile actual encrypted secrets in this transaction, not just the
	// verdict bit. Recovery still obeys every other authorization restriction.
	if err := s.reconcileQuotaCredentialsTx(ctx, tx, m, used); err != nil {
		return err
	}
	if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
		return err
	}
	if over {
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message, dedupe_key)
			VALUES ($1,'member_over_quota','warning','member',$2,'member reached the effective quota',$3)
			ON CONFLICT DO NOTHING`, newID(ids.Event), memberID, "over_quota:"+memberID); err != nil {
			return dbErr(err)
		}
	}
	return nil
}

// effectiveQuotaQ returns the quota that applies to the member's running cycle,
// including audited quota adjustments, or nil when it is unlimited.
func effectiveQuotaQ(ctx context.Context, q querier, m *domain.Member) (*int64, error) {
	var base *int64
	if m.QuotaOverrideEnabled {
		base = m.QuotaOverrideBytes
	} else if m.SubscriptionID != nil {
		var quota sql.NullInt64
		err := q.QueryRowContext(ctx, `SELECT quota_bytes FROM subscriptions WHERE id=$1`, *m.SubscriptionID).Scan(&quota)
		switch {
		case err == sql.ErrNoRows:
		case err != nil:
			return nil, dbErr(err)
		case quota.Valid:
			v := quota.Int64
			base = &v
		}
	}
	adj, err := quotaAdjustmentSumQ(ctx, q, m.ID, m.PeriodStart)
	if err != nil {
		return nil, err
	}
	if base == nil || adj == 0 {
		return base, nil
	}
	shifted := *base + adj
	if shifted < 0 {
		shifted = 0
	}
	return &shifted, nil
}
func (s *Store) usedBytesQ(ctx context.Context, q querier, memberID string, periodStart *time.Time) (int64, error) {
	var used int64
	var err error
	if periodStart == nil {
		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries WHERE member_id=$1`, memberID).Scan(&used)
	} else {
		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries
			WHERE member_id=$1 AND period_start=$2`, memberID, periodStart.UTC()).Scan(&used)
	}
	return used, dbErr(err)
}

func batchBounds(samples []contracts.UsageSample) (*time.Time, *time.Time) {
	if len(samples) == 0 {
		return nil, nil
	}
	start, end := samples[0].StartedAt, samples[0].EndedAt
	for _, s := range samples[1:] {
		if s.StartedAt.Before(start) {
			start = s.StartedAt
		}
		if s.EndedAt.After(end) {
			end = s.EndedAt
		}
	}
	su, eu := start.UTC(), end.UTC()
	return &su, &eu
}

func firstGeneration(samples []contracts.UsageSample) string {
	if len(samples) == 0 {
		return ""
	}
	return samples[0].CoreGeneration
}

// UsageFilter selects ledger rows.
type UsageFilter struct {
	MemberID string
	NodeID   string
	From     *time.Time
	To       *time.Time
	Limit    int
}

// ListUsage aggregates ledger rows per member x node x cycle.
func (s *Store) ListUsage(ctx context.Context, f UsageFilter) ([]domain.UsageRow, error) {
	q := `SELECT member_id, node_id, period_start, period_end, SUM(up_bytes), SUM(down_bytes),
		BOOL_OR(estimated), BOOL_OR(boundary_split), BOOL_OR(correction)
		FROM usage_entries WHERE true`
	args := []any{}
	if f.MemberID != "" {
		args = append(args, f.MemberID)
		q += ` AND member_id=$` + itoa(len(args))
	}
	if f.NodeID != "" {
		args = append(args, f.NodeID)
		q += ` AND node_id=$` + itoa(len(args))
	}
	if f.From != nil {
		args = append(args, f.From.UTC())
		q += ` AND period_start >= $` + itoa(len(args))
	}
	if f.To != nil {
		args = append(args, f.To.UTC())
		q += ` AND period_start < $` + itoa(len(args))
	}
	args = append(args, clampLimit(f.Limit))
	q += ` GROUP BY member_id, node_id, period_start, period_end ORDER BY period_start DESC LIMIT $` + itoa(len(args))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.UsageRow, 0, 8)
	for rows.Next() {
		var v domain.UsageRow
		if err := rows.Scan(&v.MemberID, &v.NodeID, &v.PeriodStart, &v.PeriodEnd, &v.UpBytes, &v.DownBytes,
			&v.Estimated, &v.BoundarySplit, &v.Correction); err != nil {
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

// CreateQuotaAdjustment records an audited quota change for the current cycle.
// The adjustment, the recomputed quota verdict and the revision that publishes
// it share one transaction, so a lowered quota cannot commit without revoking.
func (s *Store) CreateQuotaAdjustment(ctx context.Context, memberID string, bytes int64, reason, actor string) (*domain.QuotaAdjustment, error) {
	adj := &domain.QuotaAdjustment{
		ID: newID(ids.QuotaAdjustment), MemberID: memberID, Bytes: bytes, Reason: reason,
		CreatedBy: actor, CreatedAt: s.now(),
	}
	err := s.inWritableTx(ctx, func(tx *sql.Tx) error {
		m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, memberID))
		if err != nil {
			return err
		}
		if _, err := s.advanceMemberPeriodTx(ctx, tx, m, s.now()); err != nil {
			return err
		}
		adj.PeriodStart = m.PeriodStart
		if _, err := tx.ExecContext(ctx, `INSERT INTO quota_adjustments (id, member_id, bytes, reason, period_start, created_by, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, adj.ID, adj.MemberID, adj.Bytes, adj.Reason,
			nullableTime(adj.PeriodStart), adj.CreatedBy, adj.CreatedAt); err != nil {
			return dbErr(err)
		}
		// Recompute instead of blindly clearing the flag: a negative adjustment
		// that puts the quota below the used bytes keeps the member revoked.
		if err := s.applyQuotaState(ctx, tx, memberID); err != nil {
			return err
		}
		_, err = s.bumpRevisionsTx(ctx, tx, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return adj, nil
}

func (s *Store) ListQuotaAdjustments(ctx context.Context, memberID string, limit int) ([]domain.QuotaAdjustment, error) {
	q := `SELECT id, member_id, bytes, reason, period_start, created_by, created_at FROM quota_adjustments`
	args := []any{}
	if memberID != "" {
		args = append(args, memberID)
		q += ` WHERE member_id=$1`
	}
	args = append(args, clampLimit(limit))
	q += ` ORDER BY created_at DESC LIMIT $` + itoa(len(args))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.QuotaAdjustment, 0, 4)
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
