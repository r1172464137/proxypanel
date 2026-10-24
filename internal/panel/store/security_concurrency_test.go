package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/secrets"
)

// securityErrorCode never dereferences a nil error: a missing failure is itself
// a failed expectation, not a panic inside the assertion helper.
func securityErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return domain.AsError(err).Code
}

func securityMember(t *testing.T, st *Store) *domain.Member {
	t.Helper()
	m, err := st.CreateMember(context.Background(), &domain.Member{
		ID: ids.New(ids.Member), Username: "security-member", PasswordHash: "verified-test-hash-before",
		Role: "member", Active: true, ProxyEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func securitySession(m *domain.Member) *domain.Session {
	return &domain.Session{
		ID: ids.New(ids.Session), MemberID: m.ID, TokenHash: secrets.HashToken(secrets.NewToken(32)),
		CSRFToken: secrets.NewToken(32), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
}

// securityWaitForBlocked waits until n backends are queued behind the fixture's
// gate transaction. It only observes this fixture's own PIDs, not application
// rows, and bails out with a timeout instead of hanging the suite.
func securityWaitForBlocked(t *testing.T, ctx context.Context, st *Store, pid, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		err := st.DB.QueryRowContext(ctx, `WITH RECURSIVE blocked(pid) AS (
			SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
			UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))
		) SELECT count(*) FROM blocked`, pid).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d production operation(s) did not reach the transaction gate", n)
}

// TestSecurityPasswordResetSerializesSessionIssuance proves the two orderings
// (reset first / login first) both end with no live session for the old
// verified password. A bcrypt check that finished before the reset cannot insert
// a session afterwards.
func TestSecurityPasswordResetSerializesSessionIssuance(t *testing.T) {
	for _, first := range []string{"reset", "login"} {
		t.Run(first, func(t *testing.T) {
			st := newRegStore(t)
			m := securityMember(t, st)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			gate, err := st.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Rollback (not Commit) is the cleanup path, so a failed assertion
			// still releases the blocked production goroutines.
			defer gate.Rollback()
			var pid int
			if err = gate.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			var locked string
			if err = gate.QueryRowContext(ctx, `SELECT id FROM members WHERE id=$1 FOR UPDATE`, m.ID).Scan(&locked); err != nil {
				t.Fatal(err)
			}

			session := securitySession(m)
			resetOldHash := m.PasswordHash
			reset := func() error {
				_, err := st.UpdateMember(ctx, m.ID, 0, func(cur *domain.Member) error {
					cur.PasswordHash = "verified-test-hash-after"
					return nil
				})
				return err
			}
			login := func() error { return st.CreateSessionForPassword(ctx, session, resetOldHash) }

			resetDone := make(chan error, 1)
			loginDone := make(chan error, 1)
			startReset := func() { go func() { resetDone <- reset() }() }
			startLogin := func() { go func() { loginDone <- login() }() }
			if first == "reset" {
				startReset()
			} else {
				startLogin()
			}
			securityWaitForBlocked(t, ctx, st, pid, 1)
			if first == "reset" {
				startLogin()
			} else {
				startReset()
			}
			securityWaitForBlocked(t, ctx, st, pid, 2)

			if err = gate.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = <-resetDone; err != nil {
				t.Fatalf("password reset failed: %v", err)
			}
			// Either the login lost the race (authenticate again) or it was
			// inserted first and then revoked by the reset. Both are safe; a
			// successful login that survives is not.
			if code := securityErrorCode(<-loginDone); code != "" && code != domain.CodeUnauthenticated {
				t.Fatalf("unexpected login error code %q", code)
			}

			var live int
			if err = st.DB.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE member_id=$1 AND revoked_at IS NULL`, m.ID).Scan(&live); err != nil {
				t.Fatal(err)
			}
			if live != 0 {
				t.Fatalf("old verified password left %d live session(s) across a reset", live)
			}
			// A caller that paused after bcrypt and resumes now must be rejected.
			if code := securityErrorCode(st.CreateSessionForPassword(ctx, securitySession(m), resetOldHash)); code != domain.CodeUnauthenticated {
				t.Fatalf("stale verified hash accepted (code %q)", code)
			}
			// The new password still works, i.e. the reset committed.
			if err := st.CreateSessionForPassword(ctx, securitySession(m), "verified-test-hash-after"); err != nil {
				t.Fatalf("new password could not create a session: %v", err)
			}
		})
	}
}

// TestSecuritySelfPasswordPreservesOnlyInitiatingSession covers the self-service
// rescue path: it is allowed during recovery, keeps the initiating session, and
// refuses a stale concurrent write that would resurrect an older password.
func TestSecuritySelfPasswordPreservesOnlyInitiatingSession(t *testing.T) {
	st := newRegStore(t)
	m := securityMember(t, st)
	ctx := context.Background()
	current, other := securitySession(m), securitySession(m)
	for _, sess := range []*domain.Session{current, other} {
		if err := st.CreateSessionForPassword(ctx, sess, m.PasswordHash); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.EnterRecoveryMode(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := st.ChangePassword(ctx, m.ID, m.PasswordHash, "verified-test-hash-after", current.ID)
	if err != nil || n != 1 {
		t.Fatalf("self change: revoked=%d err=%v", n, err)
	}
	var live int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE member_id=$1 AND revoked_at IS NULL AND id=$2`, m.ID, current.ID).Scan(&live); err != nil || live != 1 {
		t.Fatalf("initiating session not preserved: live=%d err=%v", live, err)
	}
	if _, err := st.ChangePassword(ctx, m.ID, "verified-test-hash-after", "verified-test-hash-third", other.ID); securityErrorCode(err) != domain.CodeUnauthenticated {
		// The other session was revoked by the previous change, so a stale
		// self-service request through it must not be able to write again even
		// with the current password hash.
		t.Fatalf("revoked session could still change the password: %v", err)
	}
	// The preserved session can still use the rescue path.
	if _, err := st.ChangePassword(ctx, m.ID, "verified-test-hash-after", "verified-test-hash-third", current.ID); err != nil {
		t.Fatalf("preserved session could not change the password again: %v", err)
	}
}

// TestSecurityTargetedWritesRecheckRecoveryAfterLockWait covers the transaction
// boundary, not just the API middleware: a writer that already passed the HTTP
// guard must still be rejected when recovery is entered while it waits for the
// control lock.
func TestSecurityTargetedWritesRecheckRecoveryAfterLockWait(t *testing.T) {
	for _, name := range []string{"redeem", "ensure-token", "rotate-token", "rotate-credentials", "admin-member"} {
		t.Run(name, func(t *testing.T) {
			st := newRegStore(t)
			m := securityMember(t, st)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			invHash := secrets.HashToken(secrets.NewToken(24))
			if _, err := st.CreateInvitation(ctx, invHash, nil, time.Now().Add(time.Hour), 1, "test"); err != nil {
				t.Fatal(err)
			}
			server := regServer(t, st, "recover-register")
			regHash := secrets.HashToken(secrets.NewToken(24))
			if _, err := st.CreateRegistrationCode(ctx, server.ID, regHash, time.Now().Add(time.Hour), "test"); err != nil {
				t.Fatal(err)
			}

			var operation func() error
			switch name {
			case "redeem":
				operation = func() error {
					_, err := st.RedeemInvitation(ctx, invHash, &domain.Member{
						ID: ids.New(ids.Member), Username: "late-invite", PasswordHash: "test-hash",
						Role: "member", Active: true, ProxyEnabled: true,
					})
					return err
				}
			case "register":
				operation = func() error {
					_, _, err := st.RegisterAgent(ctx, regHash, "late-agent", "test", secrets.HashToken("test-agent-token"), "test")
					return err
				}
			case "ensure-token":
				operation = func() error {
					_, err := st.EnsureSubscriptionToken(ctx, m.ID, secrets.HashToken("test-token"), "test-token")
					return err
				}
			case "rotate-token":
				operation = func() error {
					_, err := st.RotateSubscriptionToken(ctx, m.ID, secrets.HashToken("test-token"), "test-token")
					return err
				}
			case "rotate-credentials":
				operation = func() error {
					_, err := st.RotateCredentials(ctx, m.ID, nil, func(string) (string, string, string, error) {
						t.Error("generator called while recovery active")
						return "", "", "", nil
					})
					return err
				}
			case "admin-member":
				operation = func() error {
					_, err := st.UpdateMember(ctx, m.ID, m.Version, func(*domain.Member) error {
						t.Error("mutator called while recovery active")
						return nil
					})
					return err
				}
			}

			gate, err := st.DB.BeginTx(ctx, &sql.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback()
			var pid int
			var mode bool
			if err = gate.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if err = gate.QueryRowContext(ctx, `SELECT recovery_mode FROM control_state WHERE id=1 FOR UPDATE`).Scan(&mode); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- operation() }()
			securityWaitForBlocked(t, ctx, st, pid, 1)
			if _, err = gate.ExecContext(ctx, `UPDATE control_state SET recovery_mode=true WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			if err = gate.Commit(); err != nil {
				t.Fatal(err)
			}
			if code := securityErrorCode(<-done); code != domain.CodeRecoveryMode {
				t.Fatalf("queued write escaped recovery (code %q)", code)
			}
			var used int
			if err = st.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM invitations WHERE used_at IS NOT NULL)
				+(SELECT count(*) FROM registration_codes WHERE used_at IS NOT NULL)
				+(SELECT count(*) FROM subscription_tokens)
				+(SELECT count(*) FROM members WHERE username='late-invite')`).Scan(&used); err != nil {
				t.Fatal(err)
			}
			if used != 0 {
				t.Fatalf("rejected transaction wrote authorization state: count=%d", used)
			}
		})
	}
}
