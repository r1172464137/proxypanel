package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"proxypanel/internal/panel/domain"
)

// OperationsFilter contains only the filters declared by backend.md §4.6.
// After is a validated pair of sort key and ID; every query fetches limit+1.
type OperationsFilter struct {
	State, BatchID, AgentID, Q, Action, Actor string
}

func operationsQuery(base, key, id string, descending bool, limit int, after []string, filters map[string]string) (string, []any) {
	args := []any{}
	q := base
	for _, column := range []string{"state", "batch_id", "agent_id", "action", "actor", "q"} {
		value := filters[column]
		if value == "" {
			continue
		}
		args = append(args, value)
		p := "$" + itoa(len(args))
		switch column {
		case "actor":
			q += " AND (actor_id=" + p + " OR actor_username=" + p + ")"
		case "q":
			args[len(args)-1] = "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value) + "%"
			q += " AND message ILIKE " + p + ` ESCAPE '\'`
		default:
			q += " AND " + column + "=" + p
		}
	}
	op, direction := ">", ""
	if descending {
		op, direction = "<", " DESC"
	}
	if len(after) == 2 {
		var first any = after[0]
		if descending {
			first, _ = time.Parse(time.RFC3339Nano, after[0])
		}
		args = append(args, first, after[1])
		q += " AND (" + key + "," + id + ` COLLATE "C")` + op + "($" + itoa(len(args)-1) + ",$" + itoa(len(args)) + ` COLLATE "C")`
	}
	args = append(args, clampLimit(limit)+1)
	q += " ORDER BY " + key + direction + "," + id + ` COLLATE "C"` + direction + " LIMIT $" + itoa(len(args))
	return q, args
}

// OperationsNameKey uses the same database collation/casing as the list SQL.
func (s *Store) OperationsNameKey(ctx context.Context, name string) (string, error) {
	var key string
	err := s.DB.QueryRowContext(ctx, `SELECT lower($1::text)`, name).Scan(&key)
	return key, dbErr(err)
}

func (s *Store) ListAgentsPage(ctx context.Context, limit int, after []string) ([]domain.Agent, error) {
	q, args := operationsQuery(`SELECT `+agentCols+` FROM agents WHERE NOT archived`, `lower(name) COLLATE "C"`, "id", false, limit, after, nil)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Agent, 0)
	now := s.now()
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		a.TokenHash = ""
		if a.LastHeartbeatAt != nil {
			a.Online = now.Sub(*a.LastHeartbeatAt) <= 45*time.Second
			a.OfflineSeconds = int64(now.Sub(*a.LastHeartbeatAt).Seconds())
			if a.OfflineSeconds < 0 {
				a.OfflineSeconds = 0
			}
		}
		out = append(out, *a)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) ListCoreInstancesPage(ctx context.Context, limit int, after []string) ([]domain.CoreInstance, error) {
	q, args := operationsQuery(`SELECT `+coreInstanceCols+` FROM core_instances ci JOIN nodes n ON n.id=ci.node_id WHERE NOT n.archived`, `lower(n.name) COLLATE "C"`, "ci.id", false, limit, after, nil)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.CoreInstance, 0)
	for rows.Next() {
		var ci domain.CoreInstance
		var agent sql.NullString
		var started, applied sql.NullTime
		if err := rows.Scan(&ci.ID, &ci.ServerID, &agent, &ci.NodeID, &ci.NodeName, &ci.Generation, &ci.ConfigRevision, &ci.State, &started, &applied, &ci.LastError, &ci.UpdatedAt); err != nil {
			return nil, dbErr(err)
		}
		if agent.Valid {
			ci.AgentID = &agent.String
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

func (s *Store) ListInvitationsPage(ctx context.Context, limit int, after []string) ([]domain.Invitation, error) {
	q, args := operationsQuery(`SELECT `+invitationCols+` FROM invitations WHERE true`, "created_at", "id", true, limit, after, nil)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Invitation, 0)
	for rows.Next() {
		v, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) ListTasksPage(ctx context.Context, f OperationsFilter, limit int, after []string) ([]domain.Task, error) {
	q, args := operationsQuery(`SELECT `+taskCols+` FROM tasks WHERE true`, "created_at", "id", true, limit, after, map[string]string{"state": f.State, "batch_id": f.BatchID, "agent_id": f.AgentID})
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Task, 0)
	for rows.Next() {
		v, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) ListEventsPage(ctx context.Context, f OperationsFilter, limit int, after []string) ([]domain.Event, error) {
	q, args := operationsQuery(`SELECT `+eventCols+` FROM events WHERE true`, "created_at", "id", true, limit, after, map[string]string{"state": f.State, "q": f.Q})
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Event, 0)
	for rows.Next() {
		var e domain.Event
		var ack sql.NullTime
		if err := rows.Scan(&e.ID, &e.Kind, &e.Severity, &e.SubjectType, &e.SubjectID, &e.Message, &e.State, &e.DedupeKey, &e.CreatedAt, &ack, &e.AckedBy); err != nil {
			return nil, dbErr(err)
		}
		if ack.Valid {
			t := ack.Time.UTC()
			e.AckedAt = &t
		}
		e.CreatedAt = utc(e.CreatedAt)
		out = append(out, e)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) ListAuditPage(ctx context.Context, f OperationsFilter, limit int, after []string) ([]domain.AuditLog, error) {
	q, args := operationsQuery(`SELECT id,actor_id,actor_username,action,target_type,target_id,detail,ip,request_id,created_at FROM audit_logs WHERE true`, "created_at", "id", true, limit, after, map[string]string{"action": f.Action, "actor": f.Actor})
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.AuditLog, 0)
	for rows.Next() {
		var a domain.AuditLog
		var detail []byte
		if err := rows.Scan(&a.ID, &a.ActorID, &a.ActorUsername, &a.Action, &a.TargetType, &a.TargetID, &detail, &a.IP, &a.RequestID, &a.CreatedAt); err != nil {
			return nil, dbErr(err)
		}
		a.Detail = scanObject(detail)
		a.CreatedAt = utc(a.CreatedAt)
		out = append(out, a)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) ListBatchesPage(ctx context.Context, limit int, after []string) ([]domain.Batch, error) {
	q, args := operationsQuery(`SELECT id,preview_id,operation,state,idempotency_key,created_by,created_at,completed_at,summary FROM batches WHERE true`, "created_at", "id", true, limit, after, nil)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	out := make([]domain.Batch, 0)
	for rows.Next() {
		var b domain.Batch
		var completed sql.NullTime
		var raw []byte
		if err := rows.Scan(&b.ID, &b.PreviewID, &b.Operation, &b.State, &b.IdempotencyKey, &b.CreatedBy, &b.CreatedAt, &completed, &raw); err != nil {
			rows.Close()
			return nil, dbErr(err)
		}
		b.CreatedAt = utc(b.CreatedAt)
		if completed.Valid {
			t := completed.Time.UTC()
			b.CompletedAt = &t
		}
		b.Summary = scanObject(raw)
		out = append(out, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, dbErr(err)
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	counts, err := s.batchStateCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		sum := out[i].Summary
		if sum == nil {
			sum = domain.NewObject()
		}
		total := 0
		for state, n := range counts[out[i].ID] {
			_ = sum.Set(state, n)
			total += n
		}
		_ = sum.Set("total", total)
		out[i].Summary = sum
	}
	return out, nil
}
