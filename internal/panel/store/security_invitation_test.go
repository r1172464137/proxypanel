package store

import (
	"context"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/period"
	"proxypanel/internal/panel/secrets"
)

// securityInvitationFixture builds a server, node, group and subscription that
// a redeemed invitation binds to. Everything is created through the store, so
// the fixture reflects the real rows the redemption transaction reads.
func securityInvitationFixture(t *testing.T, st *Store, validDays *int) (node *domain.Node, sub *domain.Subscription) {
	t.Helper()
	ctx := context.Background()
	server := regServer(t, st, "invite-server")
	node = regNode(t, st, server.ID, "invite-node", 20461)
	group, err := st.CreateNodeGroup(ctx, &domain.NodeGroup{
		ID: ids.New(ids.NodeGroup), Name: "invite-group", NodeIDs: []string{node.ID},
	})
	if err != nil {
		t.Fatalf("create node group: %v", err)
	}
	quota := int64(1024)
	sub, err = st.CreateSubscription(ctx, &domain.Subscription{
		ID: ids.New(ids.Subscription), Name: "invite-sub", Timezone: "UTC", AnchorDay: 1,
		AllowNewBindings: true, AccessEnabled: true, QuotaBytes: &quota,
		DefaultValidDays: validDays, NodeGroupIDs: []string{group.ID},
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return node, sub
}

func securityInvitedMember() *domain.Member {
	return &domain.Member{
		ID: ids.New(ids.Member), Username: "invited-user", PasswordHash: "invited-test-hash",
		Role: "member", Active: true, ProxyEnabled: true,
	}
}

// A redeemed invitation must initialize the binding exactly like an
// administrator-created member: cycle, default expiry, bound_at, history row and
// credentials. Without the cycle the ledger is read under a NULL period key and
// the quota never applies.
func TestSecurityRedeemInvitationInitializesBinding(t *testing.T) {
	st := newRegStore(t)
	node, sub := securityInvitationFixture(t, st, intPtr(3))
	ctx := context.Background()
	codeHash := secrets.HashToken(secrets.NewToken(24))
	if _, err := st.CreateInvitation(ctx, codeHash, &sub.ID, time.Now().Add(time.Hour), 1, "test"); err != nil {
		t.Fatal(err)
	}

	m, err := st.RedeemInvitation(ctx, codeHash, securityInvitedMember())
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if m.PeriodStart == nil || m.PeriodEnd == nil {
		t.Fatalf("redeemed member has no cycle: %+v", m)
	}
	want := period.Containing(sub.AnchorDay, time.UTC, st.now())
	if !m.PeriodStart.Equal(want.Start) || !m.PeriodEnd.Equal(want.End) {
		t.Fatalf("cycle = [%s,%s), want [%s,%s)", m.PeriodStart, m.PeriodEnd, want.Start, want.End)
	}
	if m.PeriodTimezone != "UTC" || m.PeriodAnchorDay != sub.AnchorDay {
		t.Fatalf("cycle rule = %q/%d", m.PeriodTimezone, m.PeriodAnchorDay)
	}
	if m.BoundAt == nil {
		t.Fatal("bound_at was not set")
	}
	if m.ExpiresAt == nil {
		t.Fatal("default_valid_days did not set expires_at")
	}
	delta := m.ExpiresAt.Sub(*m.BoundAt)
	if delta < 71*time.Hour || delta > 73*time.Hour {
		t.Fatalf("expires_at - bound_at = %s, want ~3 days", delta)
	}

	var bindings int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM member_bindings
		WHERE member_id=$1 AND subscription_id=$2 AND ended_at IS NULL`, m.ID, sub.ID).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 1 {
		t.Fatalf("open binding history rows = %d, want 1", bindings)
	}
	var creds int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM credentials
		WHERE member_id=$1 AND node_id=$2 AND state='active'`, m.ID, node.ID).Scan(&creds); err != nil {
		t.Fatal(err)
	}
	if creds != 1 {
		t.Fatalf("active credentials after redemption = %d, want 1", creds)
	}
	var used int
	if err := st.DB.QueryRowContext(ctx, `SELECT used_count FROM invitations WHERE code_hash=$1`, codeHash).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 1 {
		t.Fatalf("invitation used_count = %d, want 1", used)
	}
}

// The default expiry is computed from the store clock, not from a second
// time.Now() call, so a controllable clock yields exactly default_valid_days.
func TestSecurityRedeemInvitationExpiryUsesStoreClock(t *testing.T) {
	st := newRegStore(t)
	_, sub := securityInvitationFixture(t, st, intPtr(7))
	fixed := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	st.Now = func() time.Time { return fixed }
	ctx := context.Background()
	codeHash := secrets.HashToken(secrets.NewToken(24))
	if _, err := st.CreateInvitation(ctx, codeHash, &sub.ID, fixed.Add(time.Hour), 1, "test"); err != nil {
		t.Fatal(err)
	}
	m, err := st.RedeemInvitation(ctx, codeHash, securityInvitedMember())
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if m.ExpiresAt == nil || !m.ExpiresAt.Equal(fixed.AddDate(0, 0, 7)) {
		t.Fatalf("expires_at = %v, want %v", m.ExpiresAt, fixed.AddDate(0, 0, 7))
	}
	if m.BoundAt == nil || !m.BoundAt.Equal(fixed) {
		t.Fatalf("bound_at = %v, want %v", m.BoundAt, fixed)
	}
	if m.PeriodStart == nil || !m.PeriodStart.Equal(period.Containing(sub.AnchorDay, time.UTC, fixed).Start) {
		t.Fatalf("cycle start = %v, want the cycle containing the store clock", m.PeriodStart)
	}
}

// A subscription that no longer accepts new bindings must refuse the redemption
// and leave the code unused, otherwise an already issued invitation keeps
// creating bindings after the operator closed the subscription.
func TestSecurityRedeemInvitationHonoursClosedBindings(t *testing.T) {
	st := newRegStore(t)
	_, sub := securityInvitationFixture(t, st, nil)
	ctx := context.Background()
	if _, err := st.UpdateSubscription(ctx, sub.ID, 0, func(s *domain.Subscription) error {
		s.AllowNewBindings = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	codeHash := secrets.HashToken(secrets.NewToken(24))
	if _, err := st.CreateInvitation(ctx, codeHash, &sub.ID, time.Now().Add(time.Hour), 1, "test"); err != nil {
		t.Fatal(err)
	}
	member := securityInvitedMember()
	_, err := st.RedeemInvitation(ctx, codeHash, member)
	if code := securityErrorCode(err); code != domain.CodeSubscriptionNotBindable {
		t.Fatalf("redeem with closed bindings: err=%v (code %q)", err, code)
	}
	var members, used int
	if err := st.DB.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM members WHERE id=$1), (SELECT count(*) FROM invitations WHERE code_hash=$2 AND used_at IS NOT NULL)`,
		member.ID, codeHash).Scan(&members, &used); err != nil {
		t.Fatal(err)
	}
	if members != 0 || used != 0 {
		t.Fatalf("rejected redemption wrote state: members=%d used=%d", members, used)
	}
}

// A failure while minting the initial credentials must roll the whole
// redemption back: no member row, no consumed invitation, no partial binding.
func TestSecurityRedeemInvitationRollsBackOnCredentialFailure(t *testing.T) {
	st := newRegStore(t)
	_, sub := securityInvitationFixture(t, st, intPtr(3))
	ctx := context.Background()
	codeHash := secrets.HashToken(secrets.NewToken(24))
	if _, err := st.CreateInvitation(ctx, codeHash, &sub.ID, time.Now().Add(time.Hour), 1, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `CREATE FUNCTION fail_credential_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected credential failure'; END $$;
		CREATE TRIGGER fail_credential BEFORE INSERT ON credentials
		FOR EACH ROW EXECUTE FUNCTION fail_credential_insert()`); err != nil {
		t.Fatal(err)
	}
	member := securityInvitedMember()
	if _, err := st.RedeemInvitation(ctx, codeHash, member); err == nil {
		t.Fatal("redemption succeeded despite a failed credential insert")
	}
	var members, used, bindings int
	if err := st.DB.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM members WHERE id=$1),
		(SELECT count(*) FROM invitations WHERE code_hash=$2 AND used_at IS NOT NULL),
		(SELECT count(*) FROM member_bindings WHERE member_id=$1)`, member.ID, codeHash).Scan(&members, &used, &bindings); err != nil {
		t.Fatal(err)
	}
	if members != 0 || used != 0 || bindings != 0 {
		t.Fatalf("failed redemption left state: members=%d used=%d bindings=%d", members, used, bindings)
	}
	// The invitation is still redeemable once the fault is gone.
	if _, err := st.DB.ExecContext(ctx, `DROP TRIGGER fail_credential ON credentials`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RedeemInvitation(ctx, codeHash, member); err != nil {
		t.Fatalf("redemption after clearing the fault: %v", err)
	}
}

// CheckInvitation is the cheap preflight: a missing, expired, revoked or
// already-used code is reported without touching the member/password path, and
// recovery mode wins over the invitation state.
func TestSecurityCheckInvitationPreflight(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()

	if code := securityErrorCode(st.CheckInvitation(ctx, secrets.HashToken("unknown"))); code != domain.CodeValidationFailed {
		t.Fatalf("unknown code preflight = %q", code)
	}

	expired := secrets.HashToken(secrets.NewToken(24))
	if _, err := st.CreateInvitation(ctx, expired, nil, time.Now().Add(-time.Minute), 1, "test"); err != nil {
		t.Fatal(err)
	}
	if code := securityErrorCode(st.CheckInvitation(ctx, expired)); code != domain.CodeValidationFailed {
		t.Fatalf("expired code preflight = %q", code)
	}

	live := secrets.HashToken(secrets.NewToken(24))
	if _, err := st.CreateInvitation(ctx, live, nil, time.Now().Add(time.Hour), 1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckInvitation(ctx, live); err != nil {
		t.Fatalf("usable code preflight failed: %v", err)
	}
	if _, err := st.RedeemInvitation(ctx, live, securityInvitedMember()); err != nil {
		t.Fatalf("redeem usable code: %v", err)
	}
	if code := securityErrorCode(st.CheckInvitation(ctx, live)); code != domain.CodeValidationFailed {
		t.Fatalf("used code preflight = %q", code)
	}

	if _, err := st.EnterRecoveryMode(ctx); err != nil {
		t.Fatal(err)
	}
	if code := securityErrorCode(st.CheckInvitation(ctx, live)); code != domain.CodeRecoveryMode {
		t.Fatalf("recovery preflight = %q", code)
	}
}

func intPtr(v int) *int { return &v }

func TestSecurityInvitationUnboundAndArchived(t *testing.T) {
	t.Run("unbound", func(t *testing.T) {
		st := newRegStore(t)
		ctx := context.Background()
		code := secrets.HashToken("no-plan")
		if _, err := st.CreateInvitation(ctx, code, nil, time.Now().Add(time.Hour), 1, "test"); err != nil {
			t.Fatal(err)
		}
		m, err := st.RedeemInvitation(ctx, code, securityInvitedMember())
		if err != nil {
			t.Fatal(err)
		}
		if m.SubscriptionID != nil || m.PeriodStart != nil || m.PeriodEnd != nil || m.BoundAt != nil {
			t.Fatal("unbound invitation created authorization")
		}
		var n int
		if err := st.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM credentials WHERE member_id=$1)+(SELECT count(*) FROM member_bindings WHERE member_id=$1)`, m.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("unbound invitation minted credentials or binding")
		}
	})
	t.Run("archived", func(t *testing.T) {
		st := newRegStore(t)
		ctx := context.Background()
		_, sub := securityInvitationFixture(t, st, nil)
		code := secrets.HashToken("archived")
		if _, err := st.CreateInvitation(ctx, code, &sub.ID, time.Now().Add(time.Hour), 1, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB.ExecContext(ctx, `UPDATE subscriptions SET archived=true WHERE id=$1`, sub.ID); err != nil {
			t.Fatal(err)
		}
		m := securityInvitedMember()
		if _, err := st.RedeemInvitation(ctx, code, m); securityErrorCode(err) != domain.CodeSubscriptionNotBindable {
			t.Fatalf("archived redemption: %v", err)
		}
		var n int
		if err := st.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM members WHERE id=$1)+(SELECT used_count FROM invitations WHERE code_hash=$2)`, m.ID, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("archived redemption left side effects")
		}
	})
}
