package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"proxypanel/internal/panel/access"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/period"
)

// BatchSubmitRequest is the /batches input.
type BatchSubmitRequest struct {
	PreviewID       string `json:"preview_id"`
	IdempotencyKey  string `json:"-"`
	RulesVersion    int64  `json:"rules_version"`
	MemberSetDigest string `json:"member_set_digest"`
	Confirm         bool   `json:"confirm"`
	CreatedBy       string `json:"-"`
}

// BatchSubmitResult is returned for both a fresh submit and a replay.
type BatchSubmitResult struct {
	Batch   *domain.Batch      `json:"batch"`
	Results []domain.BatchItem `json:"results"`
	Summary domain.Object      `json:"summary"`
	Replay  bool               `json:"idempotent_replay"`
}

// requestFingerprint binds an idempotency key to one logical request.
func requestFingerprint(previewID string, confirm bool) string {
	payload := previewID + "|" + itoa64(boolToInt(confirm))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func boolToInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

// SubmitBatch applies a previewed change set atomically.
func (s *Store) SubmitBatch(ctx context.Context, req BatchSubmitRequest) (*BatchSubmitResult, error) {
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return nil, domain.Validation("Idempotency-Key header is required")
	}
	if !req.Confirm {
		return nil, domain.Validation("confirm must be true")
	}
	if strings.TrimSpace(req.PreviewID) == "" {
		return nil, domain.Validation("preview_id is required")
	}
	fingerprint := requestFingerprint(req.PreviewID, req.Confirm)
	var result *BatchSubmitResult
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := lockMemberChangesTx(ctx, tx); err != nil {
			return err
		}
		// 1. Idempotency: same key + same fingerprint replays the stored result.
		var existingID, existingFingerprint string
		err := tx.QueryRowContext(ctx, `SELECT id, request_fingerprint FROM batches
			WHERE idempotency_key=$1 FOR UPDATE`, req.IdempotencyKey).Scan(&existingID, &existingFingerprint)
		switch {
		case err == nil:
			if existingFingerprint != fingerprint {
				return domain.E(domain.CodeIdempotencyKeyReused, 409,
					"Idempotency-Key was already used for a different request")
			}
			batch, items, err := s.loadBatchTx(ctx, tx, existingID)
			if err != nil {
				return err
			}
			result = &BatchSubmitResult{Batch: batch, Results: items, Summary: batch.Summary, Replay: true}
			return nil
		case err != sql.ErrNoRows:
			return dbErr(err)
		}

		// 2. Load and lock the preview.
		var planRaw, payloadRaw []byte
		var storedDigest string
		var storedRules int64
		var expiresAt time.Time
		var consumedAt sql.NullTime
		var consumedBy sql.NullString
		var operation string
		if err := tx.QueryRowContext(ctx, `SELECT plan, payload, rules_version, member_set_digest, expires_at,
			consumed_at, consumed_by_batch, operation FROM previews WHERE id=$1 FOR UPDATE`, req.PreviewID).
			Scan(&planRaw, &payloadRaw, &storedRules, &storedDigest, &expiresAt, &consumedAt, &consumedBy, &operation); err != nil {
			if err == sql.ErrNoRows {
				return domain.NotFound("preview not found")
			}
			return dbErr(err)
		}
		var plan Preview
		if err := json.Unmarshal(planRaw, &plan); err != nil {
			return domain.Internal(err)
		}
		if consumedAt.Valid {
			return domain.Conflict(domain.CodeConflict, "preview was already submitted")
		}
		now := s.now()
		if !expiresAt.After(now) {
			return domain.E(domain.CodePreviewStale, 409, "preview expired, please preview again").WithRetryable(true)
		}

		// 3. Version + member set checks (fail closed).
		currentRules, err := s.rulesVersionQ(ctx, tx)
		if err != nil {
			return err
		}
		if currentRules != storedRules || (req.RulesVersion != 0 && req.RulesVersion != storedRules) {
			return domain.E(domain.CodePreviewStale, 409, "rules changed since the preview").WithRetryable(true)
		}
		stamps, err := s.memberStampsQ(ctx, tx, memberIDsOf(plan.Items))
		if err != nil {
			return err
		}
		if digest := digestMembers(stamps); digest != storedDigest ||
			(req.MemberSetDigest != "" && req.MemberSetDigest != storedDigest) {
			return domain.E(domain.CodePreviewStale, 409, "member set changed since the preview").WithRetryable(true)
		}

		// 4. Recompute the dynamic conclusion; a changed conclusion needs a new
		//    confirmation even though the numeric values may have drifted.
		changed, err := s.conclusionChanged(ctx, &plan, now)
		if err != nil {
			return err
		}
		if len(changed) > 0 {
			return domain.E(domain.CodePreviewConclusionChanged, 409,
				"quota/access outcome changed since the preview").WithDetail("members", changed).WithRetryable(true)
		}

		// 5. Apply the change set.
		applied, err := s.applyPreview(ctx, tx, &plan, now)
		if err != nil {
			return err
		}

		batchID := newID(ids.Batch)
		if _, err := tx.ExecContext(ctx, `UPDATE previews SET consumed_at=now(), consumed_by_batch=$2 WHERE id=$1`,
			plan.ID, batchID); err != nil {
			return dbErr(err)
		}
		reqRaw, _ := json.Marshal(map[string]any{
			"preview_id": plan.ID, "rules_version": storedRules, "member_set_digest": storedDigest,
			"confirm": true, "operation": operation,
		})
		if _, err := tx.ExecContext(ctx, `INSERT INTO batches (id, preview_id, operation, state, idempotency_key,
			request_fingerprint, summary, created_by, created_at, completed_at, member_count, affected_nodes,
			rules_version, member_set_digest, request)
			VALUES ($1,$2,$3,'applied',$4,$5,$6,$7,$8,NULL,$9,$10,$11,$12,$13)`,
			batchID, plan.ID, operation, req.IdempotencyKey, fingerprint, encodeObject(applied.summary),
			req.CreatedBy, now, len(applied.items), applied.affectedNodes, storedRules, storedDigest, reqRaw); err != nil {
			return dbErr(err)
		}
		for _, it := range applied.items {
			it.BatchID = batchID
			if _, err := tx.ExecContext(ctx, `INSERT INTO batch_items (id, batch_id, member_id, node_id, agent_id,
				state, task_id, error, revision, reason, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now(),now())`,
				newID("bitem"), batchID, it.MemberID, it.NodeID, it.AgentID, it.State, it.TaskID, it.Error,
				applied.revision, batchReason(&plan)); err != nil {
				return dbErr(err)
			}
		}
		for agentID, taskID := range applied.tasks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO tasks (id, kind, agent_id, state, idempotency_key, batch_id)
				VALUES ($1,'authorization.sync',$2,'pending',$3,$4)`,
				taskID, agentID, batchID+":"+agentID, batchID); err != nil {
				return dbErr(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs (id, actor_id, actor_username, action, target_type,
			target_id, detail) VALUES ($1,'',$2,'batch.submit','batch',$3,$4)`,
			newID(ids.Audit), req.CreatedBy, batchID, encodeObject(domain.Object{
				"operation": json.RawMessage(`"` + operation + `"`),
				"members":   json.RawMessage(`"` + itoa(len(applied.items)) + `"`),
			})); err != nil {
			return dbErr(err)
		}
		batch := &domain.Batch{ID: batchID, PreviewID: plan.ID, Operation: operation, State: "applied",
			IdempotencyKey: req.IdempotencyKey, CreatedBy: req.CreatedBy, CreatedAt: now,
			Results: applied.items, Summary: applied.summary}
		result = &BatchSubmitResult{Batch: batch, Results: applied.items, Summary: applied.summary}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// conclusionChanged returns members whose outcome differs from the preview.
func (s *Store) conclusionChanged(ctx context.Context, plan *Preview, now time.Time) ([]string, error) {
	storedOver := map[string]bool{}
	storedLose := map[string]bool{}
	if list, ok := plan.Conclusion.Strings("over_quota_members"); ok {
		for _, id := range list {
			storedOver[id] = true
		}
	}
	if list, ok := plan.Conclusion.Strings("loses_access_members"); ok {
		for _, id := range list {
			storedLose[id] = true
		}
	}
	var patchedSub *domain.Subscription
	var patchedGroupNodes []string
	values, _ := plan.Payload.Sub("values")
	req := PreviewRequest{Operation: plan.Operation, Values: values}
	var err error
	switch plan.Operation {
	case OpUpdateSubscription:
		patchedSub, err = s.patchedSubscription(ctx, values)
		if err != nil {
			return nil, err
		}
	case OpUpdateNodeGroup:
		_, patchedGroupNodes, err = s.patchedNodeGroup(ctx, values)
		if err != nil {
			return nil, err
		}
	}
	var changed []string
	for _, item := range plan.Items {
		m, err := s.GetMember(ctx, item.MemberID)
		if err != nil {
			return nil, err
		}
		ev, err := s.Evidence(ctx, m)
		if err != nil {
			return nil, err
		}
		before := ev.Decision(now)
		after, err := s.simulateChange(ctx, ev, req, patchedSub, patchedGroupNodes, now)
		if err != nil {
			return nil, err
		}
		nowOver := after.ProxyAccess == access.ProxyOverQuota
		nowLose := before.Access && !after.Access
		if nowOver != storedOver[item.MemberID] || nowLose != storedLose[item.MemberID] {
			changed = append(changed, item.MemberID)
		}
	}
	return changed, nil
}

// appliedBatch is the intermediate result of applying a preview.
type appliedBatch struct {
	items         []domain.BatchItem
	summary       domain.Object
	tasks         map[string]string
	affectedNodes int
	revision      int64
}

// applyPreview performs the business change inside the caller's transaction.
func (s *Store) applyPreview(ctx context.Context, tx *sql.Tx, plan *Preview, now time.Time) (*appliedBatch, error) {
	values, _ := plan.Payload.Sub("values")
	out := &appliedBatch{items: make([]domain.BatchItem, 0, len(plan.Items)), tasks: map[string]string{}}
	affectedNodes := map[string]bool{}
	agentByNode, err := s.agentByNode(ctx)
	if err != nil {
		return nil, err
	}

	switch plan.Operation {
	case OpUpdateSubscription:
		if err := s.applySubscriptionPatch(ctx, tx, values, now); err != nil {
			return nil, err
		}
	case OpUpdateNodeGroup:
		if err := s.applyNodeGroupPatch(ctx, tx, values); err != nil {
			return nil, err
		}
	}

	for _, item := range plan.Items {
		m, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, item.MemberID))
		if err != nil {
			return nil, err
		}
		// A cycle that ended must be rolled forward before the batch evaluates
		// and reconciles the member, or the change would be applied to a member
		// that is still priced against the previous cycle.
		if _, err := s.advanceMemberPeriodTx(ctx, tx, m, now); err != nil {
			return nil, err
		}
		ev, err := s.Evidence(ctx, m)
		if err != nil {
			return nil, err
		}
		var patchedSub *domain.Subscription
		var patchedGroupNodes []string
		req := PreviewRequest{Operation: plan.Operation, Values: values}
		switch plan.Operation {
		case OpUpdateSubscription:
			if patchedSub, err = s.patchedSubscription(ctx, values); err != nil {
				return nil, err
			}
		case OpUpdateNodeGroup:
			if _, patchedGroupNodes, err = s.patchedNodeGroup(ctx, values); err != nil {
				return nil, err
			}
		}
		before := ev.Decision(now)
		after, err := s.simulateChange(ctx, ev, req, patchedSub, patchedGroupNodes, now)
		if err != nil {
			return nil, err
		}

		switch plan.Operation {
		case OpAssignSubscription:
			subID, _ := values.String("subscription_id")
			sub, err := s.GetSubscription(ctx, strings.TrimSpace(subID))
			if err != nil {
				return nil, err
			}
			if err := s.applySubscriptionBinding(ctx, tx, m, sub, now); err != nil {
				return nil, err
			}
		case OpSetActive:
			active := true
			if v, ok := values.Bool("active"); ok {
				active = v
			}
			beforeMember := *m
			m.Active = active
			if err := protectLastAdminTx(ctx, tx, &beforeMember, m); err != nil {
				return nil, err
			}
			if !active {
				m.ProxyEnabled = false
			}
			if _, err := tx.ExecContext(ctx, `UPDATE members SET active=$2, proxy_enabled=$3, version=version+1,
				updated_at=now() WHERE id=$1`, m.ID, m.Active, m.ProxyEnabled); err != nil {
				return nil, dbErr(err)
			}
			if !active {
				if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=now()
					WHERE member_id=$1 AND revoked_at IS NULL`, m.ID); err != nil {
					return nil, dbErr(err)
				}
			}
		case OpSetExpiry:
			exp, err := nullableTimeFrom(values)
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE members SET expires_at=$2, version=version+1,
				updated_at=now() WHERE id=$1`, m.ID, nullableTime(exp)); err != nil {
				return nil, dbErr(err)
			}
		case OpClearOverride:
			if _, err := tx.ExecContext(ctx, `UPDATE members SET quota_override_enabled=false,
				quota_override_bytes=NULL, version=version+1, updated_at=now() WHERE id=$1`, m.ID); err != nil {
				return nil, dbErr(err)
			}
		case OpUpdateSubscription, OpUpdateNodeGroup:
			if err := s.syncMemberCycle(ctx, tx, m, now); err != nil {
				return nil, err
			}
		}

		// Credentials follow the new authorization set: nodes no longer allowed
		// are revoked, newly allowed nodes get a fresh credential.
		nodes, err := s.nodesForIDs(ctx, union(before.AllowedNodeIDs, after.AllowedNodeIDs))
		if err != nil {
			return nil, err
		}
		if err := s.reconcileCredentials(ctx, tx, m.ID, after.AllowedNodeIDs, nodes); err != nil {
			return nil, err
		}
		// Keep the persisted quota verdict (and its revocation event) in sync.
		if err := s.applyQuotaState(ctx, tx, m.ID); err != nil {
			return nil, err
		}

		dispatch := map[string]bool{}
		for _, nodeID := range union(before.AllowedNodeIDs, after.AllowedNodeIDs) {
			affectedNodes[nodeID] = true
			agentID := agentByNode[nodeID]
			if agentID == "" {
				continue
			}
			if !dispatch[nodeID] {
				dispatch[nodeID] = true
				taskID := newID(ids.Task)
				out.tasks[agentID] = taskID
				out.items = append(out.items, domain.BatchItem{MemberID: m.ID, NodeID: nodeID,
					AgentID: agentID, State: "pending", TaskID: taskID})
			}
		}
		if len(dispatch) == 0 {
			out.items = append(out.items, domain.BatchItem{MemberID: m.ID, State: "applied"})
		}
	}

	rev, err := s.bumpRevisionsTx(ctx, tx, nil)
	if err != nil {
		return nil, err
	}
	out.revision = rev
	out.affectedNodes = len(affectedNodes)
	summary := domain.NewObject()
	_ = summary.Set("members", len(plan.Items))
	_ = summary.Set("nodes", len(affectedNodes))
	_ = summary.Set("agents", len(out.tasks))
	pending := 0
	appliedCount := 0
	for _, it := range out.items {
		if it.State == "pending" {
			pending++
		} else {
			appliedCount++
		}
	}
	_ = summary.Set("pending", pending)
	_ = summary.Set("rules_applied", appliedCount)
	out.summary = summary
	return out, nil
}

// applySubscriptionBinding binds a member to a subscription keeping the running
// cycle and the subscription token; only the next cycle uses the new rule.
func (s *Store) applySubscriptionBinding(ctx context.Context, tx *sql.Tx, m *domain.Member, sub *domain.Subscription, now time.Time) error {
	loc, err := period.LoadTimezone(sub.Timezone)
	if err != nil {
		return domain.Validation("%s", err.Error())
	}
	pending := &domain.PeriodRule{AnchorDay: sub.AnchorDay, Timezone: sub.Timezone, SubmittedAt: &now}
	periodStart, periodEnd := m.PeriodStart, m.PeriodEnd
	anchor, tz := m.PeriodAnchorDay, m.PeriodTimezone
	if periodStart == nil || periodEnd == nil || !periodEnd.After(now) {
		win := period.Containing(sub.AnchorDay, loc, now)
		periodStart, periodEnd = &win.Start, &win.End
		anchor, tz = sub.AnchorDay, sub.Timezone
		pending = nil
	}
	expires := m.ExpiresAt
	if expires == nil && sub.DefaultValidDays != nil {
		exp := now.AddDate(0, 0, *sub.DefaultValidDays)
		expires = &exp
	}
	if _, err := tx.ExecContext(ctx, `UPDATE members SET subscription_id=$2, expires_at=$3, period_start=$4,
		period_end=$5, period_timezone=$6, period_anchor_day=$7, pending_period_rule=$8, bound_at=now(),
		version=version+1, updated_at=now() WHERE id=$1`,
		m.ID, sub.ID, nullableTime(expires), nullableTime(periodStart), nullableTime(periodEnd), tz, anchor,
		pendingJSON(pending)); err != nil {
		return dbErr(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE member_bindings SET ended_at=now() WHERE member_id=$1 AND ended_at IS NULL`, m.ID); err != nil {
		return dbErr(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO member_bindings (id, member_id, subscription_id, bound_at, expires_at, reason)
		VALUES ($1,$2,$3,now(),$4,'batch_assign')`, newID("mbd"), m.ID, sub.ID, nullableTime(expires)); err != nil {
		return dbErr(err)
	}
	return nil
}

// applySubscriptionPatch writes a previewed subscription patch.
func (s *Store) applySubscriptionPatch(ctx context.Context, tx *sql.Tx, values domain.Object, now time.Time) error {
	sub, err := scanSubscription(tx.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1 FOR UPDATE`,
		mustString(values, "id")))
	if err != nil {
		return err
	}
	patch, _ := values.Sub("patch")
	oldAnchor, oldTZ := sub.AnchorDay, sub.Timezone
	if err := applySubscriptionPatchObject(sub, patch); err != nil {
		return err
	}
	if sub.AnchorDay != oldAnchor || sub.Timezone != oldTZ {
		sub.PendingPeriodRule = &domain.PeriodRule{AnchorDay: sub.AnchorDay, Timezone: sub.Timezone,
			EffectiveFrom: nil, SubmittedAt: &now}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE subscriptions SET name=$2, description=$3, quota_bytes=$4, timezone=$5,
		anchor_day=$6, default_valid_days=$7, allow_new_bindings=$8, access_enabled=$9, archived=$10,
		template_id=$11, pending_period_rule=$12, version=version+1, updated_at=now() WHERE id=$1`,
		sub.ID, sub.Name, sub.Description, nullableInt64(sub.QuotaBytes), sub.Timezone, sub.AnchorDay,
		nullableInt(sub.DefaultValidDays), sub.AllowNewBindings, sub.AccessEnabled, sub.Archived,
		nullableString(sub.TemplateID), pendingJSON(sub.PendingPeriodRule)); err != nil {
		return dbErr(err)
	}
	if ids, ok := patch.Strings("node_group_ids"); ok {
		return setSubscriptionGroups(ctx, tx, sub.ID, ids)
	}
	return nil
}

// applyNodeGroupPatch writes a previewed node group patch.
func (s *Store) applyNodeGroupPatch(ctx context.Context, tx *sql.Tx, values domain.Object) error {
	id := mustString(values, "id")
	var name, description string
	if err := tx.QueryRowContext(ctx, `SELECT name, description FROM node_groups WHERE id=$1 FOR UPDATE`, id).
		Scan(&name, &description); err != nil {
		return dbErr(err)
	}
	patch, _ := values.Sub("patch")
	if v, ok := patch.String("name"); ok {
		if strings.TrimSpace(v) == "" {
			return domain.Validation("name must not be empty")
		}
		name = v
	}
	if v, ok := patch.String("description"); ok {
		description = v
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_groups SET name=$2, description=$3, version=version+1,
		updated_at=now() WHERE id=$1`, id, name, description); err != nil {
		return dbErr(err)
	}
	if ids, ok := patch.Strings("node_ids"); ok {
		return setGroupNodes(ctx, tx, id, ids)
	}
	return nil
}

// syncMemberCycle keeps the running cycle and schedules the subscription rule
// for the next cycle only.
func (s *Store) syncMemberCycle(ctx context.Context, tx *sql.Tx, m *domain.Member, now time.Time) error {
	if m.SubscriptionID == nil {
		return nil
	}
	sub, err := scanSubscription(tx.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1`, *m.SubscriptionID))
	if err != nil {
		return err
	}
	pending := &domain.PeriodRule{AnchorDay: sub.AnchorDay, Timezone: sub.Timezone, SubmittedAt: &now}
	if m.PeriodAnchorDay == sub.AnchorDay && m.PeriodTimezone == sub.Timezone {
		pending = nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE members SET pending_period_rule=$2, version=version+1, updated_at=now() WHERE id=$1`, m.ID,
		pendingJSON(pending)); err != nil {
		return dbErr(err)
	}
	m.PendingPeriodRule = pending
	return nil
}

// nodesForIDs loads the node rows used for credential generation.
func (s *Store) nodesForIDs(ctx context.Context, nodeIDs []string) ([]domain.Node, error) {
	all, err := s.listAllNodes(ctx, "")
	if err != nil {
		return nil, err
	}
	want := nodeSet(nodeIDs)
	out := make([]domain.Node, 0, len(want))
	for _, n := range all {
		if want[n.ID] {
			out = append(out, n)
		}
	}
	return out, nil
}

// reconcileCredentials keeps member x node credentials aligned with access.
func (s *Store) reconcileCredentials(ctx context.Context, tx *sql.Tx, memberID string, allowed []string, nodes []domain.Node) error {
	want := nodeSet(allowed)
	byID := map[string]domain.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, node_id FROM credentials WHERE member_id=$1 AND state='active' FOR UPDATE`, memberID)
	if err != nil {
		return dbErr(err)
	}
	type active struct{ id, nodeID string }
	var current []active
	for rows.Next() {
		var a active
		if err := rows.Scan(&a.id, &a.nodeID); err != nil {
			rows.Close()
			return dbErr(err)
		}
		current = append(current, a)
	}
	rows.Close()
	have := map[string]bool{}
	for _, a := range current {
		if !want[a.nodeID] {
			if _, err := tx.ExecContext(ctx, `UPDATE credentials SET state='revoked', revoked_at=now(),
				rotated_reason='node no longer authorized' WHERE id=$1`, a.id); err != nil {
				return dbErr(err)
			}
			continue
		}
		have[a.nodeID] = true
	}
	for nodeID := range want {
		if have[nodeID] {
			continue
		}
		node, ok := byID[nodeID]
		if !ok {
			continue
		}
		credID, secret, kind := NewCredentialSecret(node)
		enc, err := s.encryptSecret(secret)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO credentials (id, member_id, node_id, kind, secret_enc, state)
			VALUES ($1,$2,$3,$4,$5,'active') ON CONFLICT DO NOTHING`, credID, memberID, nodeID, kind, enc); err != nil {
			return dbErr(err)
		}
	}
	return nil
}

// loadBatchTx loads a batch and its items inside a transaction.
func (s *Store) loadBatchTx(ctx context.Context, q querier, id string) (*domain.Batch, []domain.BatchItem, error) {
	batch, items, err := s.batchRows(ctx, q, id)
	return batch, items, err
}

func (s *Store) batchRows(ctx context.Context, q querier, id string) (*domain.Batch, []domain.BatchItem, error) {
	var b domain.Batch
	var completed sql.NullTime
	var summaryRaw []byte
	if err := q.QueryRowContext(ctx, `SELECT id, preview_id, operation, state, idempotency_key, created_by,
		created_at, completed_at, summary FROM batches WHERE id=$1`, id).Scan(&b.ID, &b.PreviewID, &b.Operation,
		&b.State, &b.IdempotencyKey, &b.CreatedBy, &b.CreatedAt, &completed, &summaryRaw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, domain.NotFound("batch not found")
		}
		return nil, nil, dbErr(err)
	}
	b.CreatedAt = utc(b.CreatedAt)
	if completed.Valid {
		t := completed.Time.UTC()
		b.CompletedAt = &t
	}
	b.Summary = scanObject(summaryRaw)
	items, err := s.batchItems(ctx, q, id)
	if err != nil {
		return nil, nil, err
	}
	b.Results = items
	return &b, items, nil
}

func (s *Store) batchItems(ctx context.Context, q querier, batchID string) ([]domain.BatchItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, batch_id, member_id, node_id, agent_id, state, task_id, error
		FROM batch_items WHERE batch_id=$1 ORDER BY created_at, member_id, node_id`, batchID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.BatchItem, 0, 8)
	for rows.Next() {
		var it domain.BatchItem
		if err := rows.Scan(&it.ID, &it.BatchID, &it.MemberID, &it.NodeID, &it.AgentID, &it.State,
			&it.TaskID, &it.Error); err != nil {
			return nil, dbErr(err)
		}
		out = append(out, it)
	}
	return out, dbErr(rows.Err())
}

// GetBatch returns a batch with per object dispatch state.
func (s *Store) GetBatch(ctx context.Context, id string) (*BatchSubmitResult, error) {
	batch, items, err := s.batchRows(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	sum := batch.Summary
	_ = sum.Set("succeeded", countState(items, "succeeded"))
	_ = sum.Set("failed", countState(items, "failed"))
	_ = sum.Set("timeout", countState(items, "timeout"))
	_ = sum.Set("pending", countState(items, "pending"))
	_ = sum.Set("superseded", countState(items, "superseded"))
	return &BatchSubmitResult{Batch: batch, Results: items, Summary: sum}, nil
}

func countState(items []domain.BatchItem, state string) int {
	n := 0
	for _, it := range items {
		if it.State == state {
			n++
		}
	}
	return n
}

func mustString(o domain.Object, key string) string {
	v, _ := o.String(key)
	return strings.TrimSpace(v)
}

// SweepTimeouts marks dispatch rows that missed the revocation SLA and
// supersedes items overtaken by a newer revision. It never reports success.
func (s *Store) SweepTimeouts(ctx context.Context, now time.Time) (timedOut int, superseded int, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET state='timeout',
			last_error='no agent receipt within the SLA', updated_at=now()
			WHERE state IN ('pending','dispatched')
			  AND available_at < $1::timestamptz - interval '60 seconds'
			  AND (agent_id='' OR agent_id IN (
				SELECT id FROM agents WHERE last_heartbeat_at IS NULL
				   OR last_heartbeat_at < $1::timestamptz - interval '45 seconds'))`, now)
		if err != nil {
			return dbErr(err)
		}
		n, _ := res.RowsAffected()
		timedOut = int(n)
		if _, err := tx.ExecContext(ctx, `UPDATE batch_items bi SET state='timeout', updated_at=now(),
			error = COALESCE(NULLIF(bi.error,''), 'timeout: no agent receipt')
			FROM tasks t WHERE bi.task_id=t.id AND bi.state IN ('pending','dispatched') AND t.state='timeout'`); err != nil {
			return dbErr(err)
		}
		res, err = tx.ExecContext(ctx, `UPDATE batch_items bi SET state='superseded', updated_at=now(),
			error='superseded by a newer revision'
			FROM agents a WHERE bi.agent_id=a.id AND bi.state IN ('pending','dispatched')
			  AND bi.revision > 0 AND bi.revision < a.desired_revision`)
		if err != nil {
			return dbErr(err)
		}
		n, _ = res.RowsAffected()
		superseded = int(n)
		if _, err := tx.ExecContext(ctx, `UPDATE batches b SET state='completed', completed_at=now()
			WHERE b.state <> 'completed' AND NOT EXISTS (
				SELECT 1 FROM batch_items bi WHERE bi.batch_id=b.id AND bi.state IN ('pending','dispatched'))`); err != nil {
			return dbErr(err)
		}
		if timedOut > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message)
				VALUES ($1,'dispatch_timeout','warning','task','','dispatch did not receive an agent receipt within 60 seconds')`,
				newID(ids.Event)); err != nil {
				return dbErr(err)
			}
		}
		return nil
	})
	return timedOut, superseded, err
}

// MarkAgentItemsApplied advances batch items after a successful receipt.
func (s *Store) markAgentItems(ctx context.Context, tx *sql.Tx, agentID string, revision int64, state, message string) error {
	if state == "applied" {
		_, err := tx.ExecContext(ctx, `UPDATE batch_items SET state='succeeded', updated_at=now()
			WHERE agent_id=$1 AND revision <= $2 AND state IN ('pending','dispatched')`, agentID, revision)
		return dbErr(err)
	}
	_, err := tx.ExecContext(ctx, `UPDATE batch_items SET state='failed', updated_at=now(), error=$3
		WHERE agent_id=$1 AND revision <= $2 AND state IN ('pending','dispatched')`, agentID, revision, message)
	return dbErr(err)
}

var _ = sort.Strings

// batchReason returns the operator supplied reason from the preview payload.
func batchReason(plan *Preview) string {
	if v, ok := plan.Payload.String("reason"); ok {
		return v
	}
	return ""
}

// SyncCredentials reconciles the member x node credential set for the given
// access decision outside of a batch (member create/edit).
func (s *Store) SyncCredentials(ctx context.Context, memberID string, nodes []domain.Node, allowed []string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.reconcileCredentials(ctx, tx, memberID, allowed, nodes); err != nil {
			return err
		}
		_, err := s.bumpRevisionsTx(ctx, tx, nil)
		return err
	})
}
