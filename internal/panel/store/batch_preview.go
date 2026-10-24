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

// Batch operations accepted by /batches/preview (frozen contract list).
const (
	OpAssignSubscription = "assign_subscription"
	OpSetActive          = "set_active"
	OpSetExpiry          = "set_expiry"
	OpClearOverride      = "clear_override"
	OpUpdateSubscription = "update_subscription"
	OpUpdateNodeGroup    = "update_node_group"
)

// PreviewTTL is the lifetime of a preview before it must be regenerated.
const PreviewTTL = 10 * time.Minute

// PreviewRequest is the /batches/preview input.
type PreviewRequest struct {
	Operation string        `json:"operation"`
	MemberIDs []string      `json:"member_ids"`
	Values    domain.Object `json:"values"`
	Reason    string        `json:"reason"`
	CreatedBy string        `json:"-"`
}

// PreviewItem is the per member before/after row shown to the operator.
type PreviewItem struct {
	MemberID       string        `json:"member_id"`
	Username       string        `json:"username"`
	Before         domain.Object `json:"before"`
	After          domain.Object `json:"after"`
	Consequences   []string      `json:"consequences"`
	Blocked        bool          `json:"blocked"`
	BlockedReason  string        `json:"blocked_reason,omitempty"`
	OverQuotaAfter bool          `json:"over_quota_after"`
	HasAccessAfter bool          `json:"has_access_after"`
	NodesAdded     int           `json:"nodes_added"`
	NodesRemoved   int           `json:"nodes_removed"`
	Expiring       bool          `json:"expiring"`
}

// Preview is a persisted, versioned plan.
type Preview struct {
	ID              string          `json:"id"`
	Operation       string          `json:"operation"`
	Version         int64           `json:"version"`
	RulesVersion    int64           `json:"rules_version"`
	MemberSetDigest string          `json:"member_set_digest"`
	MemberCount     int             `json:"member_count"`
	AffectedNodes   int             `json:"affected_nodes"`
	AffectedAgents  int             `json:"affected_agents"`
	AffectedCount   int             `json:"affected_count"`
	Items           []PreviewItem   `json:"items"`
	Exceptions      []domain.Object `json:"exceptions"`
	Warnings        []string        `json:"warnings"`
	Conclusion      domain.Object   `json:"conclusion"`
	Payload         domain.Object   `json:"payload"`
	ExpiresAt       time.Time       `json:"expires_at"`
	CreatedAt       time.Time       `json:"created_at"`
}

// memberStamp identifies the versioned member set a preview was built from.
type memberStamp struct {
	MemberID            string `json:"member_id"`
	MemberVersion       int64  `json:"member_version"`
	SubscriptionID      string `json:"subscription_id"`
	SubscriptionVersion int64  `json:"subscription_version"`
}

// digestMembers returns the sha256 member set digest.
func digestMembers(stamps []memberStamp) string {
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].MemberID < stamps[j].MemberID })
	h := sha256.New()
	for _, st := range stamps {
		h.Write([]byte(st.MemberID))
		h.Write([]byte("|"))
		h.Write([]byte(itoa64(st.MemberVersion)))
		h.Write([]byte("|"))
		h.Write([]byte(st.SubscriptionID))
		h.Write([]byte("|"))
		h.Write([]byte(itoa64(st.SubscriptionVersion)))
		h.Write([]byte("\n"))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (s *Store) rulesVersionQ(ctx context.Context, q querier) (int64, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT revision_counter FROM control_state WHERE id=1`).Scan(&v)
	return v, dbErr(err)
}

func (s *Store) memberStampsQ(ctx context.Context, q querier, memberIDs []string) ([]memberStamp, error) {
	out := make([]memberStamp, 0, len(memberIDs))
	for _, id := range memberIDs {
		var st memberStamp
		var subID sql.NullString
		st.MemberID = id
		if err := q.QueryRowContext(ctx, `SELECT version, subscription_id FROM members WHERE id=$1`, id).
			Scan(&st.MemberVersion, &subID); err != nil {
			return nil, dbErr(err)
		}
		if subID.Valid {
			st.SubscriptionID = subID.String
			var sv int64
			if err := q.QueryRowContext(ctx, `SELECT version FROM subscriptions WHERE id=$1`, st.SubscriptionID).Scan(&sv); err != nil {
				return nil, dbErr(err)
			}
			st.SubscriptionVersion = sv
		}
		out = append(out, st)
	}
	return out, nil
}

// simulateDecision evaluates the access algorithm against modified evidence.
func simulateDecision(ev *Evidence, now time.Time, mutate func(*Evidence)) access.Decision {
	if mutate != nil {
		cp := *ev
		cp.Nodes = append([]domain.Node(nil), ev.Nodes...)
		cp.NodeGroups = map[string][]string{}
		for k, v := range ev.NodeGroups {
			cp.NodeGroups[k] = append([]string(nil), v...)
		}
		mutate(&cp)
		ev = &cp
	}
	return ev.Decision(now)
}

func nodeSet(ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func diffNodes(before, after []string) (added, removed int) {
	b, a := nodeSet(before), nodeSet(after)
	for id := range a {
		if !b[id] {
			added++
		}
	}
	for id := range b {
		if !a[id] {
			removed++
		}
	}
	return added, removed
}

// resolveTargets returns the member rows a preview applies to.
func (s *Store) resolveTargets(ctx context.Context, req *PreviewRequest) ([]domain.Member, error) {
	switch req.Operation {
	case OpAssignSubscription, OpSetActive, OpSetExpiry, OpClearOverride:
		if len(req.MemberIDs) == 0 {
			return nil, domain.Validation("member_ids is required for %s", req.Operation)
		}
		seen := map[string]bool{}
		out := make([]domain.Member, 0, len(req.MemberIDs))
		for _, id := range req.MemberIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			m, err := s.GetMember(ctx, id)
			if err != nil {
				return nil, err
			}
			out = append(out, *m)
		}
		return out, nil
	case OpUpdateSubscription:
		subID, _ := req.Values.String("id")
		if strings.TrimSpace(subID) == "" {
			return nil, domain.Validation("values.id (subscription) is required")
		}
		if _, err := s.GetSubscription(ctx, subID); err != nil {
			return nil, err
		}
		return s.membersOfSubscriptions(ctx, []string{subID})
	case OpUpdateNodeGroup:
		groupID, _ := req.Values.String("id")
		if strings.TrimSpace(groupID) == "" {
			return nil, domain.Validation("values.id (node group) is required")
		}
		if _, err := s.GetNodeGroup(ctx, groupID); err != nil {
			return nil, err
		}
		rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT subscription_id FROM subscription_node_groups WHERE node_group_id=$1`, groupID)
		if err != nil {
			return nil, dbErr(err)
		}
		var subIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, dbErr(err)
			}
			subIDs = append(subIDs, id)
		}
		rows.Close()
		if len(subIDs) == 0 {
			return []domain.Member{}, nil
		}
		return s.membersOfSubscriptions(ctx, subIDs)
	default:
		return nil, domain.Validation("unsupported operation %q", req.Operation)
	}
}

func (s *Store) membersOfSubscriptions(ctx context.Context, subIDs []string) ([]domain.Member, error) {
	out := make([]domain.Member, 0, 16)
	for _, subID := range subIDs {
		rows, err := s.DB.QueryContext(ctx, `SELECT `+memberCols+` FROM members
			WHERE subscription_id=$1 AND NOT archived ORDER BY lower(username)`, subID)
		if err != nil {
			return nil, dbErr(err)
		}
		for rows.Next() {
			m, err := scanMember(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, *m)
		}
		rows.Close()
	}
	return out, nil
}

// patchSubscription loads the subscription and applies a validated patch.
func (s *Store) patchedSubscription(ctx context.Context, values domain.Object) (*domain.Subscription, error) {
	id, _ := values.String("id")
	sub, err := s.GetSubscription(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	patch, ok := values.Sub("patch")
	if !ok {
		return nil, domain.Validation("values.patch is required")
	}
	if err := applySubscriptionPatchObject(sub, patch); err != nil {
		return nil, err
	}
	return sub, nil
}

// applySubscriptionPatchObject applies writable subscription fields from JSON.
func applySubscriptionPatchObject(sub *domain.Subscription, patch domain.Object) error {
	if v, ok := patch.String("name"); ok {
		if strings.TrimSpace(v) == "" {
			return domain.Validation("name must not be empty")
		}
		sub.Name = v
	}
	if v, ok := patch.String("description"); ok {
		sub.Description = v
	}
	if v, ok := patch.Strings("node_group_ids"); ok {
		sub.NodeGroupIDs = v
	}
	if raw, ok := patch.Get("quota_bytes"); ok {
		if string(raw) == "null" {
			sub.QuotaBytes = nil
		} else {
			var n int64
			if err := json.Unmarshal(raw, &n); err != nil {
				return domain.Validation("quota_bytes must be an integer or null")
			}
			if n < 0 {
				return domain.Validation("quota_bytes must not be negative")
			}
			sub.QuotaBytes = &n
		}
	}
	if v, ok := patch.String("timezone"); ok {
		if _, err := period.LoadTimezone(v); err != nil {
			return domain.Validation("%s", err.Error())
		}
		sub.Timezone = v
	}
	if v, ok := patch.Int("anchor_day"); ok {
		if v < 1 || v > 31 {
			return domain.Validation("anchor_day must be between 1 and 31")
		}
		sub.AnchorDay = int(v)
	}
	if raw, ok := patch.Get("default_valid_days"); ok {
		if string(raw) == "null" {
			sub.DefaultValidDays = nil
		} else {
			var n int
			if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
				return domain.Validation("default_valid_days must be a positive integer or null")
			}
			sub.DefaultValidDays = &n
		}
	}
	if v, ok := patch.Bool("allow_new_bindings"); ok {
		sub.AllowNewBindings = v
	}
	if v, ok := patch.Bool("access_enabled"); ok {
		sub.AccessEnabled = v
	}
	if v, ok := patch.Bool("archived"); ok {
		sub.Archived = v
	}
	if raw, ok := patch.Get("template_id"); ok {
		if string(raw) == "null" {
			sub.TemplateID = nil
		} else {
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				return domain.Validation("template_id must be a string or null")
			}
			sub.TemplateID = &v
		}
	}
	return nil
}

// patchedNodeGroup applies a node group patch in memory.
func (s *Store) patchedNodeGroup(ctx context.Context, values domain.Object) (*domain.NodeGroup, []string, error) {
	id, _ := values.String("id")
	group, err := s.GetNodeGroup(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, nil, err
	}
	patch, ok := values.Sub("patch")
	if !ok {
		return nil, nil, domain.Validation("values.patch is required")
	}
	nodeIDs := group.NodeIDs
	if v, ok := patch.String("name"); ok {
		if strings.TrimSpace(v) == "" {
			return nil, nil, domain.Validation("name must not be empty")
		}
		group.Name = v
	}
	if v, ok := patch.String("description"); ok {
		group.Description = v
	}
	if v, ok := patch.Strings("node_ids"); ok {
		nodeIDs = v
	}
	return group, nodeIDs, nil
}

// CreatePreview builds and persists a versioned preview plan.
func (s *Store) CreatePreview(ctx context.Context, req PreviewRequest) (*Preview, error) {
	req.Operation = strings.ToLower(strings.TrimSpace(req.Operation))
	targets, err := s.resolveTargets(ctx, &req)
	if err != nil {
		return nil, err
	}
	now := s.now()
	rulesVersion, err := s.rulesVersionQ(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	var patchedSub *domain.Subscription
	var patchedGroupNodes []string
	switch req.Operation {
	case OpUpdateSubscription:
		patchedSub, err = s.patchedSubscription(ctx, req.Values)
		if err != nil {
			return nil, err
		}
	case OpUpdateNodeGroup:
		_, patchedGroupNodes, err = s.patchedNodeGroup(ctx, req.Values)
		if err != nil {
			return nil, err
		}
	}

	items := make([]PreviewItem, 0, len(targets))
	exceptions := make([]domain.Object, 0)
	warnings := make([]string, 0)
	overQuota := make([]string, 0)
	noAccess := make([]string, 0)
	expiring := make([]string, 0)
	affectedNodes := map[string]bool{}
	affectedAgents := map[string]bool{}
	agentByNode, err := s.agentByNode(ctx)
	if err != nil {
		return nil, err
	}

	for _, m := range targets {
		ev, err := s.Evidence(ctx, &m)
		if err != nil {
			return nil, err
		}
		beforeDec := ev.Decision(now)
		afterDec, mutateErr := s.simulateChange(ctx, ev, req, patchedSub, patchedGroupNodes, now)
		if mutateErr != nil {
			return nil, mutateErr
		}
		added, removed := diffNodes(beforeDec.AllowedNodeIDs, afterDec.AllowedNodeIDs)
		before := domain.NewObject()
		after := domain.NewObject()
		_ = before.Set("subscription_id", derefStringPtr(m.SubscriptionID))
		_ = before.Set("active", m.Active)
		_ = before.Set("quota_bytes", ev.EffectiveQuota(now))
		_ = before.Set("used_bytes", ev.UsedBytes)
		_ = before.Set("expires_at", m.ExpiresAt)
		_ = before.Set("allowed_nodes", len(beforeDec.AllowedNodeIDs))
		_ = after.Set("subscription_id", afterSubscriptionID(m, req))
		_ = after.Set("active", afterActive(m, req))
		_ = after.Set("quota_bytes", afterDec.EffectiveQuotaBytes)
		_ = after.Set("expires_at", afterExpiry(m, req))
		_ = after.Set("allowed_nodes", len(afterDec.AllowedNodeIDs))
		item := PreviewItem{
			MemberID: m.ID, Username: m.Username, Before: before, After: after,
			Consequences:   make([]string, 0, 3),
			OverQuotaAfter: afterDec.ProxyAccess == access.ProxyOverQuota,
			HasAccessAfter: afterDec.Access,
			NodesAdded:     added, NodesRemoved: removed,
			Expiring: m.ExpiresAt != nil && m.ExpiresAt.Before(now.Add(7*24*time.Hour)),
		}
		if added > 0 {
			item.Consequences = append(item.Consequences, "nodes_added")
		}
		if removed > 0 {
			item.Consequences = append(item.Consequences, "nodes_removed")
		}
		if item.OverQuotaAfter {
			item.Consequences = append(item.Consequences, "over_quota_after")
			overQuota = append(overQuota, m.ID)
			warnings = append(warnings, m.Username+" 的已用量在新规则下仍超出额度，提交后将立即撤权")
		}
		if beforeDec.Access && !afterDec.Access {
			item.Consequences = append(item.Consequences, "loses_access")
			noAccess = append(noAccess, m.ID)
		}
		if req.Operation == OpSetActive && !afterActive(m, req) && m.Role == "admin" {
			admins, err := s.CountActiveAdmins(ctx)
			if err != nil {
				return nil, err
			}
			if admins <= 1 {
				item.Blocked = true
				item.BlockedReason = domain.CodeLastAdmin
				exceptions = append(exceptions, domain.Object{
					"member_id": json.RawMessage(`"` + m.ID + `"`),
					"reason":    json.RawMessage(`"LAST_ADMIN"`),
				})
			}
		}
		if m.ExpiresAt != nil && m.ExpiresAt.Before(now.Add(7*24*time.Hour)) {
			expiring = append(expiring, m.ID)
		}
		for _, id := range union(beforeDec.AllowedNodeIDs, afterDec.AllowedNodeIDs) {
			affectedNodes[id] = true
			if agent := agentByNode[id]; agent != "" {
				affectedAgents[agent] = true
			}
		}
		items = append(items, item)
	}

	stamps, err := s.memberStampsQ(ctx, s.DB, memberIDsOf(items))
	if err != nil {
		return nil, err
	}
	digest := digestMembers(stamps)
	conclusion := domain.NewObject()
	_ = conclusion.Set("over_quota_members", overQuota)
	_ = conclusion.Set("loses_access_members", noAccess)
	_ = conclusion.Set("expiring_members", expiring)

	payload := domain.NewObject()
	_ = payload.Set("operation", req.Operation)
	_ = payload.Set("member_ids", memberIDsOf(items))
	_ = payload.Set("values", req.Values)
	_ = payload.Set("reason", req.Reason)

	preview := &Preview{
		ID: newID(ids.Preview), Operation: req.Operation, Version: rulesVersion,
		RulesVersion: rulesVersion, MemberSetDigest: digest, MemberCount: len(items),
		AffectedNodes: len(affectedNodes), AffectedAgents: len(affectedAgents),
		AffectedCount: len(items), Items: items, Exceptions: exceptions, Warnings: warnings,
		Conclusion: conclusion, Payload: payload, ExpiresAt: now.Add(PreviewTTL), CreatedAt: now,
	}
	planJSON, err := json.Marshal(preview)
	if err != nil {
		return nil, err
	}
	err = s.inWritableTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO previews (id, operation, payload, plan, rules_version,
		member_set_digest, created_by, created_at, expires_at, conclusion, affected_nodes, affected_agents, member_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			preview.ID, preview.Operation, encodeObject(payload), planJSON, preview.RulesVersion,
			preview.MemberSetDigest, req.CreatedBy, preview.CreatedAt, preview.ExpiresAt,
			encodeObject(conclusion), preview.AffectedNodes, preview.AffectedAgents, preview.MemberCount)
		return dbErr(err)
	})
	if err != nil {
		return nil, err
	}
	return preview, nil
}

// simulateChange computes the post-change decision for a preview.
func (s *Store) simulateChange(ctx context.Context, ev *Evidence, req PreviewRequest, patchedSub *domain.Subscription,
	patchedGroupNodes []string, now time.Time) (access.Decision, error) {
	switch req.Operation {
	case OpSetActive:
		active := true
		if v, ok := req.Values.Bool("active"); ok {
			active = v
		}
		return simulateDecision(ev, now, func(e *Evidence) {
			cp := *e.Member
			cp.Active = active
			e.Member = &cp
		}), nil
	case OpSetExpiry:
		exp, err := nullableTimeFrom(req.Values)
		if err != nil {
			return access.Decision{}, err
		}
		return simulateDecision(ev, now, func(e *Evidence) {
			cp := *e.Member
			cp.ExpiresAt = exp
			e.Member = &cp
		}), nil
	case OpClearOverride:
		return simulateDecision(ev, now, func(e *Evidence) {
			cp := *e.Member
			cp.QuotaOverrideEnabled = false
			cp.QuotaOverrideBytes = nil
			e.Member = &cp
		}), nil
	case OpAssignSubscription:
		subID, _ := req.Values.String("subscription_id")
		sub, err := s.GetSubscription(ctx, strings.TrimSpace(subID))
		if err != nil {
			return access.Decision{}, err
		}
		if sub.Archived || !sub.AllowNewBindings {
			return access.Decision{}, domain.Conflict(domain.CodeSubscriptionNotBindable,
				"subscription %q does not accept new bindings", sub.Name)
		}
		return simulateDecision(ev, now, func(e *Evidence) {
			cp := *e.Member
			cp.SubscriptionID = &sub.ID
			e.Member = &cp
			e.Subscription = sub
			e.GroupIDs = sub.NodeGroupIDs
		}), nil
	case OpUpdateSubscription:
		if patchedSub == nil {
			return access.Decision{}, domain.Validation("values.patch is required")
		}
		return simulateDecision(ev, now, func(e *Evidence) {
			e.Subscription = patchedSub
			e.GroupIDs = patchedSub.NodeGroupIDs
		}), nil
	case OpUpdateNodeGroup:
		groupID, _ := req.Values.String("id")
		want := nodeSet(patchedGroupNodes)
		return simulateDecision(ev, now, func(e *Evidence) {
			for nodeID, groups := range e.NodeGroups {
				next := make([]string, 0, len(groups))
				for _, g := range groups {
					if g != groupID {
						next = append(next, g)
					}
				}
				e.NodeGroups[nodeID] = next
			}
			// A newly added node may have no previous group membership at all,
			// so it will not have appeared in the existing NodeGroups map.
			for nodeID := range want {
				e.NodeGroups[nodeID] = append(e.NodeGroups[nodeID], groupID)
			}
		}), nil
	}
	return access.Decision{}, domain.Validation("unsupported operation %q", req.Operation)
}

// agentByNode maps node id to the agent that hosts its core instance.
func (s *Store) agentByNode(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT node_id, agent_id FROM core_instances WHERE agent_id IS NOT NULL`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var nodeID, agentID string
		if err := rows.Scan(&nodeID, &agentID); err != nil {
			return nil, dbErr(err)
		}
		out[nodeID] = agentID
	}
	return out, dbErr(rows.Err())
}

func memberIDsOf(items []PreviewItem) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.MemberID)
	}
	return out
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, v := range a {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v := range b {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func derefStringPtr(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableTimeFrom(values domain.Object) (*time.Time, error) {
	raw, ok := values.Get("expires_at")
	if !ok {
		return nil, domain.Validation("values.expires_at is required (null clears it)")
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var t time.Time
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, domain.Validation("values.expires_at must be RFC3339 or null")
	}
	u := t.UTC()
	return &u, nil
}

func afterSubscriptionID(m domain.Member, req PreviewRequest) any {
	if req.Operation != OpAssignSubscription {
		return derefStringPtr(m.SubscriptionID)
	}
	if v, ok := req.Values.String("subscription_id"); ok {
		return v
	}
	return derefStringPtr(m.SubscriptionID)
}

func afterActive(m domain.Member, req PreviewRequest) bool {
	if req.Operation != OpSetActive {
		return m.Active
	}
	if v, ok := req.Values.Bool("active"); ok {
		return v
	}
	return m.Active
}

func afterExpiry(m domain.Member, req PreviewRequest) any {
	if req.Operation != OpSetExpiry {
		return m.ExpiresAt
	}
	t, _ := nullableTimeFrom(req.Values)
	return t
}

// EffectiveQuota exposes the effative quota of the evidence for previews.
func (ev *Evidence) EffectiveQuota(now time.Time) *int64 {
	d := ev.Decision(now)
	return d.EffectiveQuotaBytes
}

// GetPreview loads a stored preview.
func (s *Store) GetPreview(ctx context.Context, id string) (*Preview, error) {
	var plan []byte
	var consumed sql.NullTime
	var batchID sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT plan, consumed_at, NULLIF(consumed_by_batch,'')
		FROM previews WHERE id=$1`, id).Scan(&plan, &consumed, &batchID)
	if err != nil {
		if de, ok := dbErr(err).(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("preview not found")
		}
		return nil, dbErr(err)
	}
	var p Preview
	if err := json.Unmarshal(plan, &p); err != nil {
		return nil, err
	}
	if p.Items == nil {
		p.Items = []PreviewItem{}
	}
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
	if p.Exceptions == nil {
		p.Exceptions = []domain.Object{}
	}
	return &p, nil
}
