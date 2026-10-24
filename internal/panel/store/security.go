package store

import (
	"context"
	"database/sql"
	"reflect"

	"proxypanel/internal/panel/domain"
)

// CheckInvitation is a cheap preflight BEFORE bcrypt, never the consumption
// authority. RedeemInvitation repeats all relevant checks under transaction locks.
func (s *Store) CheckInvitation(ctx context.Context, codeHash string) error {
	state, err := s.GetControlState(ctx)
	if err != nil {
		return err
	}
	if state.RecoveryMode {
		return domain.E(domain.CodeRecoveryMode, 409, "panel is in recovery coordination mode; ordinary changes are paused")
	}
	inv, err := s.InvitationByCodeHash(ctx, codeHash)
	if err != nil {
		return err
	}
	if inv.RevokedAt != nil || inv.UsedAt != nil || inv.UsedCount >= inv.MaxUses || !inv.ExpiresAt.After(s.now()) {
		return domain.Validation("invitation is not usable")
	}
	return nil
}

func (s *Store) prepareInvitedBindingTx(ctx context.Context, tx *sql.Tx, m *domain.Member) (*domain.Subscription, error) {
	if m.SubscriptionID == nil {
		return nil, nil
	}
	sub, err := scanSubscription(tx.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1 FOR SHARE`, *m.SubscriptionID))
	if err != nil {
		return nil, err
	}
	if sub.Archived || !sub.AllowNewBindings {
		return nil, domain.Conflict(domain.CodeSubscriptionNotBindable, "subscription does not accept new bindings")
	}
	now := s.now()
	m.BoundAt = &now
	if m.ExpiresAt == nil && sub.DefaultValidDays != nil {
		expiry := now.AddDate(0, 0, *sub.DefaultValidDays)
		m.ExpiresAt = &expiry
	}
	return sub, nil
}

func (s *Store) invitedCredentialsTx(ctx context.Context, tx *sql.Tx, m *domain.Member, sub *domain.Subscription) error {
	if sub == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO member_bindings (id,member_id,subscription_id,bound_at,expires_at,reason)
 VALUES ($1,$2,$3,$4,$5,'invitation')`, newID("mbd"), m.ID, sub.ID, m.BoundAt, nullableTime(m.ExpiresAt)); err != nil {
		return dbErr(err)
	}
	// Binding uses the shared cycle helper; never duplicate attribution rules.
	if err := s.refreshMemberPeriod(ctx, tx, m); err != nil {
		return err
	}
	// Only nodes in the bound subscription, with authorization still evaluated by
	// the shared algorithm. No public listing limit is used here.
	rows, err := tx.QueryContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE NOT archived AND id IN (
 SELECT gm.node_id FROM node_group_members gm JOIN subscription_node_groups sg ON sg.node_group_id=gm.group_id WHERE sg.subscription_id=$1) ORDER BY id`, sub.ID)
	if err != nil {
		return dbErr(err)
	}
	nodes := []domain.Node{}
	groups := map[string][]string{}
	for rows.Next() {
		n, err := s.scanNodeRow(rows)
		if err != nil {
			rows.Close()
			return err
		}
		nodes = append(nodes, *n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return dbErr(err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT gm.node_id, gm.group_id FROM node_group_members gm
 JOIN subscription_node_groups sg ON sg.node_group_id=gm.group_id WHERE sg.subscription_id=$1`, sub.ID)
	if err != nil {
		return dbErr(err)
	}
	copySub := *sub
	copySub.NodeGroupIDs = nil
	seen := map[string]bool{}
	for rows.Next() {
		var nodeID, groupID string
		if err := rows.Scan(&nodeID, &groupID); err != nil {
			rows.Close()
			return dbErr(err)
		}
		groups[nodeID] = append(groups[nodeID], groupID)
		if !seen[groupID] {
			copySub.NodeGroupIDs = append(copySub.NodeGroupIDs, groupID)
			seen[groupID] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return dbErr(err)
	}
	ev := Evidence{Member: m, Subscription: &copySub, Nodes: nodes, NodeGroups: groups}
	allowed := ev.Decision(s.now()).AllowedNodeIDs
	return s.reconcileCredentials(ctx, tx, m.ID, allowed, nodes)
}

// Password changes and session revocation commit together. Only the authenticated
// self-service path may keep a session or change a password during recovery.
func (s *Store) ChangePassword(ctx context.Context, id, expectedHash, newHash, keepSessionID string) (int, error) {
	_, n, err := s.updateMember(ctx, id, 0, keepSessionID, true, func(m *domain.Member) error {
		if m.PasswordHash != expectedHash {
			return domain.E(domain.CodeUnauthenticated, 401, "password changed; authenticate again")
		}
		m.PasswordHash = newHash
		return nil
	})
	return n, err
}

func revokeSessionsQ(ctx context.Context, q querier, memberID, exceptID string) (int, error) {
	res, err := q.ExecContext(ctx, `UPDATE sessions SET revoked_at=now() WHERE member_id=$1 AND revoked_at IS NULL AND id <> $2`, memberID, exceptID)
	if err != nil {
		return 0, dbErr(err)
	}
	n, err := res.RowsAffected()
	return int(n), dbErr(err)
}

// passwordOnlyMemberChange guards the recovery exception against future mixed edits.
func passwordOnlyMemberChange(before, after *domain.Member) bool {
	b, a := *before, *after
	b.PasswordHash, a.PasswordHash = "", ""
	return reflect.DeepEqual(b, a)
}
