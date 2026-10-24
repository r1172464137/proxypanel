package store

import (
	"context"
	"database/sql"
	"time"

	"proxypanel/internal/panel/access"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/period"
)

// Evidence is everything the single authorization algorithm needs for one
// member, loaded from PostgreSQL.
type Evidence struct {
	Member       *domain.Member
	Subscription *domain.Subscription
	GroupIDs     []string
	Nodes        []domain.Node
	NodeGroups   map[string][]string
	UsedBytes    int64
	AdjustBytes  int64
}

// NodeGroupMap returns nodeID -> authorized group ids.
func (s *Store) NodeGroupMap(ctx context.Context) (map[string][]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT node_id, group_id FROM node_group_members ORDER BY position`)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var nodeID, groupID string
		if err := rows.Scan(&nodeID, &groupID); err != nil {
			return nil, dbErr(err)
		}
		out[nodeID] = append(out[nodeID], groupID)
	}
	return out, dbErr(rows.Err())
}

// UsedBytes sums the ledger for a member's current cycle.
func (s *Store) UsedBytes(ctx context.Context, memberID string, periodStart *time.Time) (int64, error) {
	var used int64
	var err error
	if periodStart == nil {
		err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries
			WHERE member_id=$1 AND period_start IS NULL`, memberID).Scan(&used)
	} else {
		err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(up_bytes+down_bytes),0) FROM usage_entries
			WHERE member_id=$1 AND period_start=$2`, memberID, periodStart.UTC()).Scan(&used)
	}
	return used, dbErr(err)
}

// QuotaAdjustmentSum sums audited quota adjustments for the cycle. Adjustments
// never modify the original usage rows.
func (s *Store) QuotaAdjustmentSum(ctx context.Context, memberID string, periodStart *time.Time) (int64, error) {
	return quotaAdjustmentSumQ(ctx, s.DB, memberID, periodStart)
}

// quotaAdjustmentSumQ is the transaction scoped variant of QuotaAdjustmentSum.
func quotaAdjustmentSumQ(ctx context.Context, q querier, memberID string, periodStart *time.Time) (int64, error) {
	var sum int64
	var err error
	if periodStart == nil {
		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM quota_adjustments
			WHERE member_id=$1 AND period_start IS NULL`, memberID).Scan(&sum)
	} else {
		err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM quota_adjustments
			WHERE member_id=$1 AND period_start=$2`, memberID, periodStart.UTC()).Scan(&sum)
	}
	return sum, dbErr(err)
}

// Evidence loads the decision inputs for a member.
func (s *Store) Evidence(ctx context.Context, member *domain.Member) (*Evidence, error) {
	ev := &Evidence{Member: member, NodeGroups: map[string][]string{}}
	if member.SubscriptionID != nil {
		sub, err := s.GetSubscription(ctx, *member.SubscriptionID)
		if err != nil {
			return nil, err
		}
		ev.Subscription = sub
		ev.GroupIDs = sub.NodeGroupIDs
	}
	nodes, err := s.listAllNodes(ctx, "")
	if err != nil {
		return nil, err
	}
	ev.Nodes = nodes
	groups, err := s.NodeGroupMap(ctx)
	if err != nil {
		return nil, err
	}
	ev.NodeGroups = groups
	used, err := s.UsedBytes(ctx, member.ID, member.PeriodStart)
	if err != nil {
		return nil, err
	}
	ev.UsedBytes = used
	adj, err := s.QuotaAdjustmentSum(ctx, member.ID, member.PeriodStart)
	if err != nil {
		return nil, err
	}
	ev.AdjustBytes = adj
	return ev, nil
}

// ─── bulk decision inputs ──────────────────────────────────────────────────

// EvidenceBatch holds the decision inputs shared by many members. Loading them
// once keeps a snapshot build linear in the member count: the naive version
// reloaded every node, node group map and subscription once per member, which is
// N members x M nodes round trips and was the reason a 205 member snapshot never
// finished.
type EvidenceBatch struct {
	Nodes         []domain.Node
	NodeGroups    map[string][]string
	subscriptions map[string]*domain.Subscription
	used          map[string]int64
	adjustments   map[string]int64
}

// cycleLedgerKey identifies one member's ledger slice. A nil period start means
// "no persisted cycle", which sums the whole ledger (same rule as UsedBytes).
func cycleLedgerKey(memberID string, periodStart *time.Time) string {
	if periodStart == nil {
		return memberID + "|\x00"
	}
	return memberID + "|" + periodStart.UTC().Format(time.RFC3339Nano)
}

// Evidence builds the single member evidence bundle from the batch, so the
// authorization algorithm still runs exactly once per member and on exactly the
// same inputs as Store.Evidence.
func (b *EvidenceBatch) Evidence(m *domain.Member) *Evidence {
	ev := &Evidence{Member: m, Nodes: b.Nodes, NodeGroups: b.NodeGroups}
	if m.SubscriptionID != nil {
		if sub := b.subscriptions[*m.SubscriptionID]; sub != nil {
			ev.Subscription = sub
			ev.GroupIDs = sub.NodeGroupIDs
		}
	}
	if b.used != nil {
		ev.UsedBytes = b.used[cycleLedgerKey(m.ID, m.PeriodStart)]
	}
	if b.adjustments != nil {
		ev.AdjustBytes = b.adjustments[cycleLedgerKey(m.ID, m.PeriodStart)]
	}
	return ev
}

// LoadEvidenceBatch loads the shared authorization inputs for the given members
// with a constant number of queries (nodes, node group map, subscriptions, one
// ledger aggregate and one adjustment aggregate).
func (s *Store) LoadEvidenceBatch(ctx context.Context, members []domain.Member) (*EvidenceBatch, error) {
	b := &EvidenceBatch{
		NodeGroups:    map[string][]string{},
		subscriptions: map[string]*domain.Subscription{},
		used:          map[string]int64{},
		adjustments:   map[string]int64{},
	}
	nodes, err := s.listAllNodes(ctx, "")
	if err != nil {
		return nil, err
	}
	b.Nodes = nodes
	groups, err := s.NodeGroupMap(ctx)
	if err != nil {
		return nil, err
	}
	b.NodeGroups = groups

	memberIDs := make([]string, 0, len(members))
	subIDs := make([]string, 0, len(members))
	seenMember := map[string]bool{}
	seenSub := map[string]bool{}
	for i := range members {
		if id := members[i].ID; id != "" && !seenMember[id] {
			seenMember[id] = true
			memberIDs = append(memberIDs, id)
		}
		if sub := members[i].SubscriptionID; sub != nil && !seenSub[*sub] {
			seenSub[*sub] = true
			subIDs = append(subIDs, *sub)
		}
	}
	if len(subIDs) > 0 {
		subs, err := s.loadSubscriptions(ctx, s.DB, subIDs)
		if err != nil {
			return nil, err
		}
		b.subscriptions = subs
	}
	if len(memberIDs) == 0 {
		return b, nil
	}
	if err := s.sumLedgerByCycle(ctx, memberIDs, b.used); err != nil {
		return nil, err
	}
	if err := s.sumAdjustmentsByCycle(ctx, memberIDs, b.adjustments); err != nil {
		return nil, err
	}
	return b, nil
}

// loadSubscriptions loads the given subscriptions including their node groups.
func (s *Store) loadSubscriptions(ctx context.Context, q querier, ids []string) (map[string]*domain.Subscription, error) {
	out := make(map[string]*domain.Subscription, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, dbErr(err)
	}
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out[sub.ID] = sub
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, dbErr(err)
	}
	groupRows, err := q.QueryContext(ctx, `SELECT subscription_id, node_group_id FROM subscription_node_groups
		WHERE subscription_id = ANY($1) ORDER BY subscription_id, position`, ids)
	if err != nil {
		return nil, dbErr(err)
	}
	defer groupRows.Close()
	for groupRows.Next() {
		var subID, groupID string
		if err := groupRows.Scan(&subID, &groupID); err != nil {
			return nil, dbErr(err)
		}
		if sub := out[subID]; sub != nil {
			sub.NodeGroupIDs = append(sub.NodeGroupIDs, groupID)
		}
	}
	return out, dbErr(groupRows.Err())
}

// sumLedgerByCycle fills member x cycle usage totals in one aggregate query.
func (s *Store) sumLedgerByCycle(ctx context.Context, memberIDs []string, into map[string]int64) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT member_id, period_start,
		COALESCE(SUM(up_bytes + down_bytes), 0)
		FROM usage_entries WHERE member_id = ANY($1)
		GROUP BY member_id, period_start`, memberIDs)
	if err != nil {
		return dbErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var memberID string
		var periodStart sql.NullTime
		var sum int64
		if err := rows.Scan(&memberID, &periodStart, &sum); err != nil {
			return dbErr(err)
		}
		var key time.Time
		if periodStart.Valid {
			key = periodStart.Time
			into[cycleLedgerKey(memberID, &key)] = sum
			continue
		}
		into[cycleLedgerKey(memberID, nil)] = sum
	}
	return dbErr(rows.Err())
}

// sumAdjustmentsByCycle fills member x cycle quota adjustment totals in one
// aggregate query.
func (s *Store) sumAdjustmentsByCycle(ctx context.Context, memberIDs []string, into map[string]int64) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT member_id, period_start, COALESCE(SUM(bytes), 0)
		FROM quota_adjustments WHERE member_id = ANY($1)
		GROUP BY member_id, period_start`, memberIDs)
	if err != nil {
		return dbErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var memberID string
		var periodStart sql.NullTime
		var sum int64
		if err := rows.Scan(&memberID, &periodStart, &sum); err != nil {
			return dbErr(err)
		}
		var key time.Time
		if periodStart.Valid {
			key = periodStart.Time
			into[cycleLedgerKey(memberID, &key)] = sum
			continue
		}
		into[cycleLedgerKey(memberID, nil)] = sum
	}
	return dbErr(rows.Err())
}

// activeCredentialSecrets returns memberID -> nodeID -> secret for the active
// credentials on the given nodes, decrypted in one pass.
func (s *Store) activeCredentialSecrets(ctx context.Context, nodeIDs []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	if len(nodeIDs) == 0 {
		return out, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT member_id, node_id, secret_enc FROM credentials
		WHERE state='active' AND node_id = ANY($1)`, nodeIDs)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var memberID, nodeID string
		var enc []byte
		if err := rows.Scan(&memberID, &nodeID, &enc); err != nil {
			return nil, dbErr(err)
		}
		plain, err := s.decryptSecret(enc)
		if err != nil || plain == "" {
			continue
		}
		if out[memberID] == nil {
			out[memberID] = map[string]string{}
		}
		out[memberID][nodeID] = plain
	}
	return out, dbErr(rows.Err())
}

// Decision evaluates the frozen algorithm for one evidence bundle.
func (ev *Evidence) Decision(now time.Time) access.Decision {
	m := ev.Member
	in := access.Input{
		Now:                  now,
		MemberID:             m.ID,
		AccountActive:        m.Active,
		AccountArchived:      m.Archived,
		ProxyEnabled:         m.ProxyEnabled,
		BindingPresent:       m.SubscriptionID != nil,
		BindingExpiresAt:     m.ExpiresAt,
		UsedBytes:            ev.UsedBytes,
		OverrideQuotaEnabled: m.QuotaOverrideEnabled,
		OverrideQuotaBytes:   m.QuotaOverrideBytes,
	}
	if m.SubscriptionID != nil {
		in.SubscriptionID = *m.SubscriptionID
	}
	if ev.Subscription != nil {
		in.SubscriptionAccessEnabled = ev.Subscription.AccessEnabled
		in.SubscriptionArchived = ev.Subscription.Archived
		in.SubscriptionQuotaBytes = ev.Subscription.QuotaBytes
		in.AuthorizedGroupIDs = ev.Subscription.NodeGroupIDs
	}
	// Audited quota adjustments shift the effective quota for this cycle.
	if ev.AdjustBytes != 0 {
		base, _ := access.EffectiveQuota(in.SubscriptionQuotaBytes, in.OverrideQuotaBytes, in.OverrideQuotaEnabled)
		if base != nil {
			shifted := *base + ev.AdjustBytes
			if shifted < 0 {
				shifted = 0
			}
			in.OverrideQuotaEnabled = true
			in.OverrideQuotaBytes = &shifted
		}
	}
	states := make([]access.NodeState, 0, len(ev.Nodes))
	for _, n := range ev.Nodes {
		states = append(states, access.NodeState{ID: n.ID, Name: n.Name, Enabled: n.Enabled, GroupIDs: ev.NodeGroups[n.ID]})
	}
	in.Nodes = states
	return access.Decide(in)
}

// AccessResult bundles a decision with the nodes it was computed from.
type AccessResult struct {
	Decision access.Decision
	Evidence *Evidence
}

// EvaluateAccess computes the decision for a member id.
func (s *Store) EvaluateAccess(ctx context.Context, memberID string) (*AccessResult, error) {
	m, err := s.GetMember(ctx, memberID)
	if err != nil {
		return nil, err
	}
	return s.EvaluateAccessForMember(ctx, m)
}

// EvaluateAccessForMember computes the decision for a loaded member.
func (s *Store) EvaluateAccessForMember(ctx context.Context, m *domain.Member) (*AccessResult, error) {
	// A cycle that ended must be rolled forward before the decision is taken:
	// otherwise the authorization would be evaluated against the previous
	// cycle's ledger and the previous cycle's rule.
	if err := s.ensureMemberPeriod(ctx, m); err != nil {
		return nil, err
	}
	ev, err := s.Evidence(ctx, m)
	if err != nil {
		return nil, err
	}
	d := ev.Decision(s.now())
	m.UsedBytes = ev.UsedBytes
	m.EffectiveQuotaBytes = d.EffectiveQuotaBytes
	m.AccessReasons = d.Reasons
	m.ProxyAccess = d.ProxyAccess
	m.AllowedNodeIDs = d.AllowedNodeIDs
	return &AccessResult{Decision: d, Evidence: ev}, nil
}

// AllowedNodes returns the node rows a member may currently use.
func (s *Store) AllowedNodes(ctx context.Context, res *AccessResult) []domain.Node {
	allowed := map[string]bool{}
	for _, id := range res.Decision.AllowedNodeIDs {
		allowed[id] = true
	}
	out := make([]domain.Node, 0, len(allowed))
	for _, n := range res.Evidence.Nodes {
		if allowed[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

// refreshMemberPeriod recomputes the persisted cycle of a member after a rule
// or binding change. The running cycle is preserved; only the next boundary
// follows the (possibly new) rule.
func (s *Store) refreshMemberPeriod(ctx context.Context, tx *sql.Tx, m *domain.Member) error {
	if m.SubscriptionID == nil {
		if err := recordRunningPeriodTx(ctx, tx, m, "proven_current"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE members SET period_start=NULL, period_end=NULL,
			period_timezone='', period_anchor_day=0 WHERE id=$1`, m.ID); err != nil {
			return dbErr(err)
		}
		m.PeriodStart, m.PeriodEnd = nil, nil
		return nil
	}
	sub, err := scanSubscription(tx.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1`, *m.SubscriptionID))
	if err != nil {
		return err
	}
	anchor, tz := sub.AnchorDay, sub.Timezone
	if m.PendingPeriodRule != nil {
		anchor, tz = m.PendingPeriodRule.AnchorDay, m.PendingPeriodRule.Timezone
	}
	loc, err := period.LoadTimezone(tz)
	if err != nil {
		return domain.Validation("invalid timezone %q on subscription", tz)
	}
	now := s.now()
	if m.PeriodStart != nil && m.PeriodEnd != nil {
		// Preserve the running rule as well as its boundaries. A replacement
		// binding takes effect only after the old end, never retroactively.
		if m.PeriodAnchorDay != anchor || m.PeriodTimezone != tz {
			m.PendingPeriodRule = &domain.PeriodRule{AnchorDay: anchor, Timezone: tz, SubmittedAt: &now}
		}
		if err := recordRunningPeriodTx(ctx, tx, m, "proven_current"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE members SET pending_period_rule=$2 WHERE id=$1`, m.ID, pendingJSON(m.PendingPeriodRule)); err != nil {
			return dbErr(err)
		}
		_, err := s.advanceMemberPeriodTx(ctx, tx, m, now)
		return err
	}
	win := period.Containing(anchor, loc, now)
	m.PeriodStart, m.PeriodEnd = &win.Start, &win.End
	m.PeriodTimezone, m.PeriodAnchorDay = tz, anchor
	m.PendingPeriodRule = nil
	if err := recordRunningPeriodTx(ctx, tx, m, "binding"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE members SET period_start=$2, period_end=$3, period_timezone=$4,
		period_anchor_day=$5, pending_period_rule=NULL WHERE id=$1`, m.ID, nullableTime(m.PeriodStart), nullableTime(m.PeriodEnd),
		m.PeriodTimezone, m.PeriodAnchorDay); err != nil {
		return dbErr(err)
	}
	return nil
}

// reconcileQuotaCredentialsTx loads the decision exclusively from this
// transaction so uncommitted quota/period changes cannot be missed.
func (s *Store) reconcileQuotaCredentialsTx(ctx context.Context, tx *sql.Tx, m *domain.Member, used int64) error {
	ev := &Evidence{Member: m, UsedBytes: used, NodeGroups: map[string][]string{}}
	if m.SubscriptionID != nil {
		subs, err := s.loadSubscriptions(ctx, tx, []string{*m.SubscriptionID})
		if err != nil {
			return err
		}
		ev.Subscription = subs[*m.SubscriptionID]
	}
	adj, err := quotaAdjustmentSumQ(ctx, tx, m.ID, m.PeriodStart)
	if err != nil {
		return err
	}
	ev.AdjustBytes = adj
	rows, err := tx.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE NOT archived ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		n, err := s.scanNodeRow(rows)
		if err != nil {
			rows.Close()
			return err
		}
		ev.Nodes = append(ev.Nodes, *n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT node_id, group_id FROM node_group_members`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var node, group string
		if err := rows.Scan(&node, &group); err != nil {
			rows.Close()
			return err
		}
		ev.NodeGroups[node] = append(ev.NodeGroups[node], group)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return s.reconcileCredentials(ctx, tx, m.ID, ev.Decision(s.now()).AllowedNodeIDs, ev.Nodes)
}

// ─── authoritative cycle advancement ───────────────────────────────────────

// periodAdvance is the pure outcome of the cycle roll forward. It is pure so
// the boundary semantics stay testable without a database: a running cycle is
// never recomputed, and a rule submitted while the cycle was running takes
// effect exactly at the next boundary.
type periodAdvance struct {
	Start, End  time.Time
	Anchor      int
	Timezone    string
	Pending     *domain.PeriodRule
	Advanced    bool
	RuleApplied bool
}

// computeAdvance decides what the member's persisted cycle must be at now. It
// only ever moves an existing cycle forward: a member without a persisted cycle
// is left alone, so a read can never hand out a fresh (reset) cycle.
func computeAdvance(m *domain.Member, sub *domain.Subscription, now time.Time) (periodAdvance, error) {
	out := periodAdvance{Anchor: m.PeriodAnchorDay, Timezone: m.PeriodTimezone, Pending: m.PendingPeriodRule}
	if m.PeriodStart == nil || m.PeriodEnd == nil {
		return out, nil
	}
	anchor, tz := m.PeriodAnchorDay, m.PeriodTimezone
	if anchor == 0 || tz == "" {
		if sub == nil {
			return out, nil
		}
		anchor, tz = sub.AnchorDay, sub.Timezone
	}
	if m.PeriodEnd.After(now) {
		// The running cycle stays authoritative, whatever the subscription now
		// says; a pending rule waits for the next boundary.
		out.Start, out.End, out.Anchor, out.Timezone = *m.PeriodStart, *m.PeriodEnd, anchor, tz
		return out, nil
	}
	if p := out.Pending; p != nil {
		// The cycle ended, so the submitted rule becomes the running rule.
		anchor, tz = p.AnchorDay, p.Timezone
		out.Pending = nil
		out.RuleApplied = true
	}
	loc, err := period.LoadTimezone(tz)
	if err != nil {
		return out, domain.Validation("invalid cycle timezone %q", tz)
	}
	win := period.Transition(*m.PeriodEnd, anchor, loc, now)
	out.Start, out.End, out.Anchor, out.Timezone = win.Start, win.End, anchor, tz
	out.Advanced = true
	return out, nil
}

// advanceMemberPeriodTx rolls the member's cycle forward inside the caller's
// transaction, then recomputes the persisted quota verdict for the new cycle
// and publishes the result with a revision bump in that same transaction. The
// caller must already hold the member row (or accept the extra row lock).
func (s *Store) advanceMemberPeriodTx(ctx context.Context, tx *sql.Tx, m *domain.Member, now time.Time) (bool, error) {
	var sub *domain.Subscription
	if m.SubscriptionID != nil {
		loaded, err := scanSubscription(tx.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1`,
			*m.SubscriptionID))
		if err != nil {
			return false, err
		}
		sub = loaded
	}
	adv, err := computeAdvance(m, sub, now)
	if err != nil {
		return false, err
	}
	if m.PeriodStart != nil && m.PeriodEnd != nil {
		if err := recordRunningPeriodTx(ctx, tx, m, "proven_current"); err != nil {
			return false, err
		}
	}
	if !adv.Advanced {
		return false, nil
	}
	loc, err := period.LoadTimezone(adv.Timezone)
	if err != nil {
		return false, err
	}
	// Persist every skipped interval, including a short transition interval.
	// The old end is the only origin; new rules never reconstruct the past.
	var start, end time.Time
	for cursor := *m.PeriodEnd; !cursor.After(now); {
		next := period.NextBoundary(cursor, adv.Anchor, loc)
		if err := recordMemberPeriodTx(ctx, tx, PeriodRecord{MemberID: m.ID,
			PeriodStart: cursor, PeriodEnd: next, AnchorDay: adv.Anchor,
			Timezone: adv.Timezone, Source: "roll_forward"}); err != nil {
			return false, err
		}
		start, end = cursor, next
		cursor = next
	}
	m.PeriodStart, m.PeriodEnd = &start, &end
	m.PeriodAnchorDay, m.PeriodTimezone = adv.Anchor, adv.Timezone
	m.PendingPeriodRule = adv.Pending
	if _, err := tx.ExecContext(ctx, `UPDATE members SET period_start=$2, period_end=$3, period_timezone=$4,
		period_anchor_day=$5, pending_period_rule=$6, updated_at=now() WHERE id=$1`,
		m.ID, nullableTime(m.PeriodStart), nullableTime(m.PeriodEnd), m.PeriodTimezone, m.PeriodAnchorDay,
		pendingJSON(m.PendingPeriodRule)); err != nil {
		return false, dbErr(err)
	}
	// The new cycle starts from an empty ledger slice: a member that was over
	// quota gets its access back, and the agents must be told so now instead of
	// on the next unrelated write.
	if err := s.applyQuotaState(ctx, tx, m.ID); err != nil {
		return false, err
	}
	if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
		return false, err
	}
	return true, nil
}

// ensureMemberPeriod advances a member outside a transaction and refreshes the
// in memory row. A running cycle costs one comparison, so per request read
// paths can call it for every member they evaluate.
func (s *Store) ensureMemberPeriod(ctx context.Context, m *domain.Member) error {
	if m == nil || m.SubscriptionID == nil || m.PeriodStart == nil || m.PeriodEnd == nil {
		return nil
	}
	now := s.now()
	if m.PeriodEnd.After(now) {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, m.ID))
		if err != nil {
			return err
		}
		advanced, err := s.advanceMemberPeriodTx(ctx, tx, cur, now)
		if err != nil {
			return err
		}
		if !advanced {
			return nil
		}
		m.PeriodStart, m.PeriodEnd = cur.PeriodStart, cur.PeriodEnd
		m.PeriodAnchorDay, m.PeriodTimezone = cur.PeriodAnchorDay, cur.PeriodTimezone
		m.PendingPeriodRule, m.OverQuota = cur.PendingPeriodRule, cur.OverQuota
		return nil
	})
}

// AdvancePeriods is the authoritative cycle tick. It rolls every member whose
// cycle ended into the cycle containing now, applies pending rules at the
// boundary, recomputes over_quota and publishes the revision, each in its own
// transaction. It is safe to call from a background ticker and from any code
// path that cannot advance inline; the authorization read paths call
// ensureMemberPeriod for the members they touch.
func (s *Store) AdvancePeriods(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		now = s.now()
	}
	now = now.UTC()
	// Only members that already have a persisted cycle are advanced: a member
	// without one keeps the legacy evaluation and is never handed a fresh
	// (reset) cycle by a background pass.
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM members
		WHERE subscription_id IS NOT NULL AND period_start IS NOT NULL AND period_end IS NOT NULL
		  AND period_end <= $1
		ORDER BY id LIMIT 5000`, now)
	if err != nil {
		return 0, dbErr(err)
	}
	ids := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, dbErr(err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, dbErr(err)
	}
	advanced := 0
	for _, id := range ids {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			cur, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, id))
			if err != nil {
				return err
			}
			ok, err := s.advanceMemberPeriodTx(ctx, tx, cur, now)
			if ok {
				advanced++
			}
			return err
		})
		if err != nil {
			return advanced, err
		}
	}
	return advanced, nil
}
