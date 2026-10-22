package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/secrets"
)

// advanceControlRevisionTx advances the global control revision. Callers must
// already hold the control_state row lock (lockMemberChangesTx) and publish the
// affected agents inside the same transaction, so a committed change can never
// lose its revision.
func (s *Store) advanceControlRevisionTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var rev int64
	if err := tx.QueryRowContext(ctx, `UPDATE control_state SET revision_counter=revision_counter+1,
		updated_at=now() WHERE id=1 RETURNING revision_counter`).Scan(&rev); err != nil {
		return 0, dbErr(err)
	}
	return rev, nil
}

// bumpRevisionsTx advances the global control revision and repoints the agents
// that must pick the change up (all agents when nodeIDs is empty). It always
// runs inside the same transaction as the business change it publishes: the
// control_state singleton row lock serializes concurrent rule updates and the
// agents' desired_revision becomes part of the same commit, so a committed
// revocation can never be lost on a later, separate bump.
// Agents compare per instance hashes, so an extra revision never causes a blind
// restart.
func (s *Store) bumpRevisionsTx(ctx context.Context, tx *sql.Tx, nodeIDs []string) (int64, error) {
	rev, err := s.advanceControlRevisionTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	if len(nodeIDs) == 0 {
		_, err := tx.ExecContext(ctx, `UPDATE agents SET desired_revision=$1, updated_at=now()
			WHERE NOT archived AND desired_revision < $1`, rev)
		return rev, dbErr(err)
	}
	for _, nodeID := range nodeIDs {
		// Dispatch reaches the agent through the core instance link and through
		// the node's server, so a node created before its agent registered (or
		// moved to another server) still revokes on the right host instead of
		// silently keeping the old authorization.
		if _, err := tx.ExecContext(ctx, `UPDATE agents a SET desired_revision=$2, updated_at=now()
			FROM core_instances ci
			WHERE ci.node_id=$1 AND NOT a.archived AND a.desired_revision < $2
			  AND (ci.agent_id = a.id OR a.server_id = ci.server_id)`, nodeID, rev); err != nil {
			return 0, dbErr(err)
		}
	}
	return rev, nil
}

// bumpServerAgentsTx publishes an already advanced revision to every agent of a
// server. It is used when an object leaves a server: the previous server has to
// revoke it too, not only the new one.
func (s *Store) bumpServerAgentsTx(ctx context.Context, tx *sql.Tx, serverID string, rev int64) error {
	if serverID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE agents SET desired_revision=$2, updated_at=now()
		WHERE server_id=$1 AND NOT archived AND desired_revision < $2`, serverID, rev)
	return dbErr(err)
}

// BumpRevisions is the exported, non transactional variant. It is kept for
// recovery tooling only: normal changes must publish the revision inside the
// transaction that stores the change (see bumpRevisionsTx), never after it.
func (s *Store) BumpRevisions(ctx context.Context, nodeIDs []string) (int64, error) {
	var rev int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		v, err := s.bumpRevisionsTx(ctx, tx, nodeIDs)
		rev = v
		return err
	})
	return rev, err
}

// ─── registration codes & agents ───────────────────────────────────────────

func (s *Store) CreateRegistrationCode(ctx context.Context, serverID, codeHash string, expiresAt time.Time, createdBy string) (string, error) {
	if _, err := s.GetServer(ctx, serverID); err != nil {
		return "", err
	}
	id := newID(ids.Registration)
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO registration_codes (id, server_id, code_hash, expires_at, created_by)
		VALUES ($1,$2,$3,$4,$5)`, id, serverID, codeHash, expiresAt.UTC(), createdBy); err != nil {
		return "", dbErr(err)
	}
	return id, nil
}

// RegisterAgent consumes a one time registration code atomically.
func (s *Store) RegisterAgent(ctx context.Context, codeHash, name, version, tokenHash, tokenPrefix string) (*domain.Agent, string, error) {
	var agent *domain.Agent
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var codeID, serverID string
		var expiresAt time.Time
		var usedAt sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT id, server_id, expires_at, used_at FROM registration_codes
			WHERE code_hash=$1 FOR UPDATE`, codeHash).Scan(&codeID, &serverID, &expiresAt, &usedAt); err != nil {
			if err == sql.ErrNoRows {
				return domain.E(domain.CodeAgentAuthFailed, 401, "invalid registration code")
			}
			return dbErr(err)
		}
		if usedAt.Valid || !expiresAt.After(s.now()) {
			return domain.E(domain.CodeAgentAuthFailed, 401, "registration code already used or expired")
		}
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT control_generation FROM control_state WHERE id=1`).Scan(&generation); err != nil {
			return dbErr(err)
		}
		id := newID(ids.Agent)
		if _, err := tx.ExecContext(ctx, `INSERT INTO agents (id, server_id, name, version, token_hash, token_prefix,
			control_generation, desired_revision, applied_revision, instances, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,0,0,'[]',now(),now())`,
			id, serverID, name, version, tokenHash, tokenPrefix, generation); err != nil {
			return dbErr(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE registration_codes SET used_at=now(), used_by_agent_id=$2 WHERE id=$1`, codeID, id); err != nil {
			return dbErr(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE core_instances SET agent_id=$2 WHERE server_id=$1 AND agent_id IS NULL`, serverID, id); err != nil {
			return dbErr(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET desired_revision=control_state.revision_counter FROM control_state WHERE agents.id=$1 AND control_state.id=1`, id); err != nil {
			return dbErr(err)
		}
		agent = &domain.Agent{ID: id, ServerID: serverID}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	a, err := s.GetAgent(ctx, agent.ID)
	if err != nil {
		return nil, "", err
	}
	return a, "", nil
}

const agentCols = `id, server_id, name, version, xray_version, token_hash, token_prefix, control_generation,
	desired_revision, applied_revision, applied_at, last_heartbeat_at, uptime_seconds, queue_depth, metrics,
	instances, archived, created_at, updated_at`

func scanAgent(row interface{ Scan(...any) error }) (*domain.Agent, error) {
	var a domain.Agent
	var appliedAt, heartbeat sql.NullTime
	var metrics, instances []byte
	if err := row.Scan(&a.ID, &a.ServerID, &a.Name, &a.Version, &a.XrayVersion, &a.TokenHash, &a.TokenPrefix,
		&a.ControlGeneration, &a.DesiredRevision, &a.AppliedRevision, &appliedAt, &heartbeat, &a.UptimeSeconds,
		&a.QueueDepth, &metrics, &instances, &a.Archive, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	a.Metrics = scanObject(metrics)
	if len(instances) == 0 {
		instances = []byte("[]")
	}
	a.Instances = json.RawMessage(instances)
	if appliedAt.Valid {
		t := appliedAt.Time.UTC()
		a.AppliedAt = &t
	}
	if heartbeat.Valid {
		t := heartbeat.Time.UTC()
		a.LastHeartbeatAt = &t
	}
	a.CreatedAt = utc(a.CreatedAt)
	a.UpdatedAt = utc(a.UpdatedAt)
	return &a, nil
}

func (s *Store) GetAgent(ctx context.Context, id string) (*domain.Agent, error) {
	a, err := scanAgent(s.DB.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE id=$1`, id))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("agent not found")
		}
		return nil, err
	}
	a.TokenHash = ""
	return a, nil
}

// AgentByTokenHash authenticates an agent bearer token.
func (s *Store) AgentByTokenHash(ctx context.Context, tokenHash string) (*domain.Agent, error) {
	a, err := scanAgent(s.DB.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents
		WHERE token_hash=$1 AND NOT archived`, tokenHash))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.E(domain.CodeAgentAuthFailed, 401, "invalid agent token")
		}
		return nil, err
	}
	return a, nil
}

// ListAgents returns agents with derived online state (offline after 45s).
func (s *Store) ListAgents(ctx context.Context, limit int) ([]domain.Agent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+agentCols+` FROM agents WHERE NOT archived
		ORDER BY lower(name) LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Agent, 0, 4)
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		a.TokenHash = ""
		now := s.now()
		if a.LastHeartbeatAt != nil && now.Sub(*a.LastHeartbeatAt) <= 45*time.Second {
			a.Online = true
		}
		if a.LastHeartbeatAt != nil {
			a.OfflineSeconds = int64(now.Sub(*a.LastHeartbeatAt).Seconds())
			if a.OfflineSeconds < 0 {
				a.OfflineSeconds = 0
			}
		}
		out = append(out, *a)
	}
	return out, dbErr(rows.Err())
}

// RecordHeartbeat stores the agent heartbeat and reported instance states.
func (s *Store) RecordHeartbeat(ctx context.Context, agentID string, hb contracts.Heartbeat) error {
	instances, err := json.Marshal(hb.Instances)
	if err != nil {
		return err
	}
	metrics, err := json.Marshal(hb.Metrics)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE agents SET version=$2, xray_version=$3, uptime_seconds=$4,
		queue_depth=$5, metrics=$6, instances=$7, last_heartbeat_at=now(), updated_at=now() WHERE id=$1`,
		agentID, hb.Version, hb.XrayVersion, hb.UptimeSeconds, hb.QueueDepth, metrics, instances)
	if err != nil {
		return dbErr(err)
	}
	for _, r := range hb.Instances {
		if r.InstanceID == "" {
			continue
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE core_instances SET state=$2, applied_at=COALESCE(applied_at, now()),
			last_error=$3, updated_at=now() WHERE node_id=$1`, r.InstanceID, r.State, r.Error); err != nil {
			return dbErr(err)
		}
	}
	return nil
}

// RecordApplyReport stores an idempotent application receipt. A stale revision
// never moves the applied pointer backwards.
func (s *Store) RecordApplyReport(ctx context.Context, agentID string, report contracts.ApplyReport) error {
	now := s.now()
	if report.AppliedAt.IsZero() {
		report.AppliedAt = now
	}
	if report.AppliedAt.After(now.Add(5 * time.Minute)) {
		return domain.Validation("applied_at is in the future beyond the clock tolerance")
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var desired, applied, generation int64
		if err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, control_generation
			FROM agents WHERE id=$1 FOR UPDATE`, agentID).Scan(&desired, &applied, &generation); err != nil {
			return dbErr(err)
		}
		// A receipt for a revision the panel never issued must never be trusted.
		if report.Revision > desired {
			return domain.E(domain.CodeRevisionStale, 409,
				"receipt revision %d was never issued (desired revision is %d)", report.Revision, desired)
		}
		if report.ControlGeneration != "" && report.ControlGeneration != itoa64(generation) {
			return domain.E(domain.CodeRevisionStale, 409,
				"receipt control generation does not match the current generation")
		}
		if report.Revision < applied {
			// Stale receipt: record it for the audit trail but never move state.
			_, err := tx.ExecContext(ctx, `INSERT INTO agent_receipts (agent_id, revision, state, applied_at, payload)
				VALUES ($1,$2,$3,$4,$5) ON CONFLICT (agent_id, revision) DO NOTHING`,
				agentID, report.Revision, report.State, report.AppliedAt.UTC(), payload)
			return dbErr(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_receipts (agent_id, revision, state, applied_at, payload)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT (agent_id, revision) DO NOTHING`,
			agentID, report.Revision, report.State, report.AppliedAt.UTC(), payload); err != nil {
			return dbErr(err)
		}
		switch report.State {
		case "applied", "applied_with_errors":
			if _, err := tx.ExecContext(ctx, `UPDATE agents SET applied_revision=$2, applied_at=$3,
				updated_at=now() WHERE id=$1`, agentID, report.Revision, report.AppliedAt.UTC()); err != nil {
				return dbErr(err)
			}
			failed := report.State == "applied_with_errors"
			if err := s.markAgentItems(ctx, tx, agentID, report.Revision, "applied", ""); err != nil {
				return err
			}
			if failed {
				if err := s.markAgentItems(ctx, tx, agentID, report.Revision, "failed", "agent reported partial failure"); err != nil {
					return err
				}
			} else if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state='succeeded', updated_at=now()
				WHERE agent_id=$1 AND state IN ('pending','dispatched')`, agentID); err != nil {
				return dbErr(err)
			}
		case "failed":
			msg := "agent reported a failed apply"
			for _, r := range report.Results {
				if r.Error != "" {
					msg = r.Error
					break
				}
			}
			if err := s.markAgentItems(ctx, tx, agentID, report.Revision, "failed", msg); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state='failed', last_error=$2, updated_at=now()
				WHERE agent_id=$1 AND state IN ('pending','dispatched')`, agentID, msg); err != nil {
				return dbErr(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message, dedupe_key)
				VALUES ($1,'agent_apply_failed','warning','agent',$2,$3,$4) ON CONFLICT DO NOTHING`,
				newID(ids.Event), agentID, msg, "apply_failed:"+agentID+":"+itoa64(report.Revision)); err != nil {
				return dbErr(err)
			}
		}
		for _, r := range report.Results {
			if r.InstanceID == "" {
				continue
			}
			switch r.State {
			case "succeeded", "applied", "running":
				// A new config revision means the core restarted, so the running
				// generation advances exactly once per applied revision.
				if _, err := tx.ExecContext(ctx, `UPDATE core_instances SET state='running', last_error='',
					generation = CASE WHEN config_revision = $2 THEN generation ELSE generation + 1 END,
					last_started_at = CASE WHEN config_revision = $2 THEN last_started_at ELSE now() END,
					config_revision = $2, applied_at=now(), updated_at=now() WHERE node_id=$1`,
					r.InstanceID, report.Revision); err != nil {
					return dbErr(err)
				}
			default:
				if _, err := tx.ExecContext(ctx, `UPDATE core_instances SET state=$2, last_error=$3,
					updated_at=now() WHERE node_id=$1`, r.InstanceID, r.State, r.Error); err != nil {
					return dbErr(err)
				}
			}
		}
		return nil
	})
}

// ─── tasks, events, audit, overview ────────────────────────────────────────

const taskCols = `id, kind, agent_id, node_id, member_id, batch_id, state, attempts, max_attempts,
	last_error, available_at, locked_at, created_at, updated_at`

func (s *Store) ListTasks(ctx context.Context, state string, limit int) ([]domain.Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks`
	args := []any{}
	if state != "" {
		args = append(args, state)
		q += ` WHERE state=$1`
	}
	args = append(args, clampLimit(limit))
	q += ` ORDER BY created_at DESC LIMIT $` + itoa(len(args))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Task, 0, 8)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, dbErr(rows.Err())
}

// scanTask maps one tasks row onto the wire shape.
func scanTask(row interface{ Scan(...any) error }) (domain.Task, error) {
	var t domain.Task
	var locked sql.NullTime
	if err := row.Scan(&t.ID, &t.Kind, &t.AgentID, &t.NodeID, &t.MemberID, &t.BatchID, &t.State, &t.Attempts,
		&t.MaxAttempts, &t.LastError, &t.AvailableAt, &locked, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Task{}, err
		}
		return domain.Task{}, dbErr(err)
	}
	if locked.Valid {
		x := locked.Time.UTC()
		t.LockedAt = &x
	}
	t.AvailableAt = utc(t.AvailableAt)
	t.CreatedAt = utc(t.CreatedAt)
	t.UpdatedAt = utc(t.UpdatedAt)
	return t, nil
}

// taskByIDQ loads one task through the caller's transaction.
func (s *Store) taskByIDQ(ctx context.Context, q querier, id string) (*domain.Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=$1`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.NotFound("task not found")
		}
		return nil, err
	}
	return &t, nil
}

// RetryTask turns a failed or timed out dispatch into a fresh dispatch of the
// *same* task. It never re-runs the business change: it re-opens the task and the
// batch items that belong to it, stamps them with a newly published control
// revision and points the affected agent at that revision, all in one
// transaction that follows the panel's lock order (control_state, then agents,
// then task and batch items).
//
// Refusals are explicit instead of silent no-ops:
//   - a succeeded task is terminal and is never re-opened;
//   - a task that is still pending/dispatched is already queued, so a second
//     call cannot publish two revisions for one retry;
//   - a task that spent its attempts is not retryable any more.
//
// The new revision carries the *current* authorization because snapshots are
// built from live state, so retrying a dispatch can never restore a credential
// that was revoked in the meantime.
func (s *Store) RetryTask(ctx context.Context, id string) (*domain.Task, error) {
	var out *domain.Task
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		// Lock order: the control_state row first (every writer that publishes a
		// revision takes it), then the agent row, then the task and its items.
		// This matches SubmitBatch (control_state -> agents -> tasks) and
		// RecordApplyReport (agents -> items/tasks), so a retry can never deadlock
		// against them.
		if err := lockMemberChangesTx(ctx, tx); err != nil {
			return err
		}
		task, err := s.taskByIDQ(ctx, tx, id)
		if err != nil {
			return err
		}
		switch task.State {
		case "failed", "timeout":
		case "succeeded":
			return domain.Conflict(domain.CodeConflict,
				"task already succeeded; a completed task is never re-opened").WithRetryable(false)
		case "pending", "dispatched":
			return domain.Conflict(domain.CodeConflict,
				"task is already queued; the previous retry has not been receipted yet").WithRetryable(false)
		default:
			return domain.Conflict(domain.CodeConflict, "task state %q is not retryable", task.State)
		}
		if task.Attempts >= task.MaxAttempts {
			return domain.E(domain.CodeConflict, 409,
				"task exhausted its %d attempts", task.MaxAttempts).
				WithDetail("attempts", task.Attempts).WithDetail("max_attempts", task.MaxAttempts).
				WithRetryable(false)
		}
		if strings.TrimSpace(task.AgentID) == "" {
			return domain.Conflict(domain.CodeConflict, "task has no agent to dispatch to")
		}

		// Publish the new revision to exactly the owning agent, before the task
		// rows are locked (see the lock order above).
		rev, err := s.advanceControlRevisionTx(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET desired_revision=$2, updated_at=now()
			WHERE id=$1 AND NOT archived AND desired_revision < $2`, task.AgentID, rev); err != nil {
			return dbErr(err)
		}

		// Re-open the task itself. The guard repeats the state and attempt checks
		// against the locked row, so a concurrent retry cannot spend two attempts.
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET state='pending', attempts=attempts+1,
			available_at=now(), locked_at=NULL, last_error='', updated_at=now()
			WHERE id=$1 AND state IN ('failed','timeout') AND attempts < max_attempts`, id)
		if err != nil {
			return dbErr(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return domain.Conflict(domain.CodeConflict, "task is no longer retryable")
		}

		// Re-open the batch items of this task and stamp them with the new
		// revision: an item that still carried the old revision would be swept as
		// superseded before the agent could receipt it.
		if _, err := tx.ExecContext(ctx, `UPDATE batch_items SET state='pending', error='',
			revision=$2, updated_at=now() WHERE task_id=$1 AND state IN ('timeout','failed')`,
			id, rev); err != nil {
			return dbErr(err)
		}
		// A completed batch has to accept new items again, otherwise the sweep
		// would close it with a pending item inside.
		if _, err := tx.ExecContext(ctx, `UPDATE batches SET state='applied', completed_at=NULL
			WHERE id=$1 AND state='completed'`, task.BatchID); err != nil {
			return dbErr(err)
		}
		out, err = s.taskByIDQ(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const eventCols = `id, kind, severity, subject_type, subject_id, message, state, COALESCE(dedupe_key,''), created_at, acked_at, acked_by`

func (s *Store) ListEvents(ctx context.Context, limit int) ([]domain.Event, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+eventCols+` FROM events ORDER BY created_at DESC LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Event, 0, 8)
	for rows.Next() {
		var e domain.Event
		var acked sql.NullTime
		if err := rows.Scan(&e.ID, &e.Kind, &e.Severity, &e.SubjectType, &e.SubjectID, &e.Message, &e.State,
			&e.DedupeKey, &e.CreatedAt, &acked, &e.AckedBy); err != nil {
			return nil, dbErr(err)
		}
		if acked.Valid {
			t := acked.Time.UTC()
			e.AckedAt = &t
		}
		e.CreatedAt = utc(e.CreatedAt)
		out = append(out, e)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) AckEvent(ctx context.Context, id, actor string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE events SET state='acked', acked_at=now(), acked_by=$2
		WHERE id=$1 AND state='open'`, id, actor)
	if err != nil {
		return dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.NotFound("open event not found")
	}
	return nil
}

func (s *Store) RecordEvent(ctx context.Context, kind, severity, subjectType, subjectID, message, dedupeKey string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message, dedupe_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
		newID(ids.Event), kind, severity, subjectType, subjectID, message, nullableString(&dedupeKey))
	return dbErr(err)
}

// RecordAudit appends an audit row. Details are supplied by callers and must
// never contain secrets.
func (s *Store) RecordAudit(ctx context.Context, actorID, actorName, action, targetType, targetID string, detail domain.Object, ip, requestID string) error {
	if detail == nil {
		detail = domain.NewObject()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO audit_logs (id, actor_id, actor_username, action, target_type,
		target_id, detail, ip, request_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		newID(ids.Audit), actorID, actorName, action, targetType, targetID, encodeObject(detail), ip, requestID)
	return dbErr(err)
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]domain.AuditLog, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, actor_id, actor_username, action, target_type, target_id,
		detail, ip, request_id, created_at FROM audit_logs ORDER BY created_at DESC LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.AuditLog, 0, 16)
	for rows.Next() {
		var a domain.AuditLog
		var detail []byte
		if err := rows.Scan(&a.ID, &a.ActorID, &a.ActorUsername, &a.Action, &a.TargetType, &a.TargetID,
			&detail, &a.IP, &a.RequestID, &a.CreatedAt); err != nil {
			return nil, dbErr(err)
		}
		a.Detail = scanObject(detail)
		a.CreatedAt = utc(a.CreatedAt)
		out = append(out, a)
	}
	return out, dbErr(rows.Err())
}

// Overview aggregates the admin dashboard counters.
func (s *Store) Overview(ctx context.Context) (domain.Object, error) {
	out := domain.NewObject()
	counts := map[string]any{}
	var members, servers, nodes, subs int
	if err := s.DB.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM members WHERE NOT archived),
		(SELECT count(*) FROM servers WHERE NOT archived),
		(SELECT count(*) FROM nodes WHERE NOT archived),
		(SELECT count(*) FROM subscriptions WHERE NOT archived)`).Scan(
		&members, &servers, &nodes, &subs); err != nil {
		return nil, dbErr(err)
	}
	counts["members"] = members
	counts["servers"] = servers
	counts["nodes"] = nodes
	counts["subscriptions"] = subs
	var usageBytes int64
	if err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries`).Scan(&usageBytes); err != nil {
		return nil, dbErr(err)
	}
	var agentsOnline, pendingTasks int
	if err := s.DB.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM agents WHERE NOT archived AND last_heartbeat_at > now() - interval '45 seconds'),
		(SELECT count(*) FROM tasks WHERE state='pending')`).Scan(&agentsOnline, &pendingTasks); err != nil {
		return nil, dbErr(err)
	}
	events, err := s.ListEvents(ctx, 5)
	if err != nil {
		return nil, err
	}
	_ = out.Set("counts", counts)
	_ = out.Set("usage_bytes", usageBytes)
	_ = out.Set("agents_online", agentsOnline)
	_ = out.Set("pending_tasks", pendingTasks)
	_ = out.Set("recent_events", events)
	return out, nil
}

const coreInstanceCols = `ci.id, ci.server_id, ci.agent_id, ci.node_id, n.name, ci.generation,
	ci.config_revision, ci.state, ci.last_started_at, ci.applied_at, ci.last_error, ci.updated_at`

// ListCoreInstances 列出核心实例。
//
// 必须排除归档节点：节点删除是"归档"，归档后它的实例行仍留在 core_instances，
// 而 ListNodes 已经按 NOT archived 过滤。不过滤会让运维列表越用越长
// （实测一次全量测试后就从 5 条涨到 83 条，全是已归档节点的 stopped 实例），
// 真实实例被淹没在里面。
func (s *Store) ListCoreInstances(ctx context.Context, limit int) ([]domain.CoreInstance, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+coreInstanceCols+` FROM core_instances ci
		JOIN nodes n ON n.id=ci.node_id
		WHERE NOT n.archived
		ORDER BY lower(n.name) LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.CoreInstance, 0, 8)
	for rows.Next() {
		var ci domain.CoreInstance
		var agentID sql.NullString
		var started, applied sql.NullTime
		if err := rows.Scan(&ci.ID, &ci.ServerID, &agentID, &ci.NodeID, &ci.NodeName, &ci.Generation,
			&ci.ConfigRevision, &ci.State, &started, &applied, &ci.LastError, &ci.UpdatedAt); err != nil {
			return nil, dbErr(err)
		}
		if agentID.Valid {
			v := agentID.String
			ci.AgentID = &v
		}
		if started.Valid {
			t := started.Time.UTC()
			ci.LastStartedAt = &t
		}
		if applied.Valid {
			t := applied.Time.UTC()
			ci.AppliedAt = &t
		}
		ci.UpdatedAt = utc(ci.UpdatedAt)
		out = append(out, ci)
	}
	return out, dbErr(rows.Err())
}

// EnterRecoveryMode pauses normal synchronization after a restore.
func (s *Store) EnterRecoveryMode(ctx context.Context) (*domain.ControlState, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, dbErr(err)
	}
	defer tx.Rollback()
	var previous int64
	if err := tx.QueryRowContext(ctx, `SELECT control_generation FROM control_state WHERE id=1 FOR UPDATE`).Scan(&previous); err != nil {
		return nil, dbErr(err)
	}
	generation, err := newControlGeneration(previous)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE control_state SET recovery_mode=true, recovery_entered_at=now(),
		control_generation=$1, updated_at=now() WHERE id=1`, generation); err != nil {
		return nil, dbErr(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET control_generation=$1`, generation); err != nil {
		return nil, dbErr(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, dbErr(err)
	}
	return s.GetControlState(ctx)
}

// Random epochs avoid deterministic collisions when a backup rolls a counter
// back. Stay within JavaScript's exact integer range for operator-facing JSON.
// Operators must still compare against every live node before recovery exit.
func newControlGeneration(previous int64) (int64, error) {
	for {
		n, err := rand.Int(rand.Reader, big.NewInt((1<<53)-1))
		if err != nil {
			return 0, err
		}
		generation := n.Int64() + 1
		if generation != previous {
			return generation, nil
		}
	}
}

// ExitRecoveryMode resumes synchronization only with an explicit confirmation
// string from the operator; the caller validates the confirmation.
func (s *Store) ExitRecoveryMode(ctx context.Context) (*domain.ControlState, error) {
	if _, err := s.DB.ExecContext(ctx, `UPDATE control_state SET recovery_mode=false, recovery_entered_at=NULL,
		updated_at=now() WHERE id=1`); err != nil {
		return nil, dbErr(err)
	}
	return s.GetControlState(ctx)
}

// ─── credential generation ─────────────────────────────────────────────────

// NewCredentialSecret returns a credential secret appropriate for the protocol.
func NewCredentialSecret(node domain.Node) (id, secret, kind string) {
	switch strings.ToLower(node.Protocol) {
	case protocol.VLESS, protocol.VMess:
		return newID(ids.Credential), ids.NewUUID(), "uuid"
	case protocol.Trojan:
		return newID(ids.Credential), secrets.NewToken(24), "password"
	default:
		secret := secrets.NewToken(16)
		method := strings.ToLower(node.Cipher)
		if method == "" {
			method, _ = node.ProtocolSettings.String("method")
			method = strings.ToLower(method)
		}
		if strings.HasPrefix(method, "2022-blake3-aes-128-gcm") {
			secret = base64Std(16)
		} else if strings.HasPrefix(method, "2022-") {
			secret = base64Std(32)
		}
		return newID(ids.Credential), secret, "password"
	}
}

func base64Std(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	return base64.StdEncoding.EncodeToString(b)
}
