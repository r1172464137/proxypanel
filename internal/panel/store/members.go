package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"proxypanel/internal/panel/domain"
)

// ─── members ───────────────────────────────────────────────────────────────

const memberCols = `id, username, password_hash, role, active, proxy_enabled, archived, subscription_id,
	expires_at, quota_override_bytes, quota_override_enabled, period_start, period_end, period_timezone,
	period_anchor_day, pending_period_rule, over_quota, bound_at, last_login_at, version, created_at, updated_at`

func scanMember(row interface{ Scan(...any) error }) (*domain.Member, error) {
	var m domain.Member
	var subID sql.NullString
	var expires, periodStart, periodEnd, boundAt, lastLogin sql.NullTime
	var quotaOverride sql.NullInt64
	var pending []byte
	if err := row.Scan(&m.ID, &m.Username, &m.PasswordHash, &m.Role, &m.Active, &m.ProxyEnabled, &m.Archived,
		&subID, &expires, &quotaOverride, &m.QuotaOverrideEnabled, &periodStart, &periodEnd, &m.PeriodTimezone,
		&m.PeriodAnchorDay, &pending, &m.OverQuota, &boundAt, &lastLogin, &m.Version, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	if subID.Valid {
		v := subID.String
		m.SubscriptionID = &v
	}
	if expires.Valid {
		t := expires.Time.UTC()
		m.ExpiresAt = &t
	}
	if quotaOverride.Valid {
		v := quotaOverride.Int64
		m.QuotaOverrideBytes = &v
	}
	if periodStart.Valid {
		t := periodStart.Time.UTC()
		m.PeriodStart = &t
	}
	if periodEnd.Valid {
		t := periodEnd.Time.UTC()
		m.PeriodEnd = &t
	}
	if boundAt.Valid {
		t := boundAt.Time.UTC()
		m.BoundAt = &t
	}
	if lastLogin.Valid {
		t := lastLogin.Time.UTC()
		m.LastLoginAt = &t
	}
	if len(pending) > 0 && string(pending) != "null" {
		var rule domain.PeriodRule
		if err := json.Unmarshal(pending, &rule); err == nil {
			m.PendingPeriodRule = &rule
		}
	}
	m.CreatedAt = utc(m.CreatedAt)
	m.UpdatedAt = utc(m.UpdatedAt)
	m.AccessReasons = []string{}
	m.AllowedNodeIDs = []string{}
	return &m, nil
}

// GetMember loads one member (including the password hash for auth).
func (s *Store) GetMember(ctx context.Context, id string) (*domain.Member, error) {
	m, err := scanMember(s.DB.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1`, id))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("member not found")
		}
		return nil, err
	}
	return m, nil
}

// GetMemberByUsername is used only by login; it never filters on active so the
// caller can return a uniform failure for unknown vs suspended accounts.
func (s *Store) GetMemberByUsername(ctx context.Context, username string) (*domain.Member, error) {
	m, err := scanMember(s.DB.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE lower(username)=lower($1)`, strings.TrimSpace(username)))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("member not found")
		}
		return nil, err
	}
	return m, nil
}

// ListMembers returns members ordered by username.
//
// 默认只返回未归档成员，与 ListServers/ListNodes 保持一致。归档成员是"已删除但
// 为审计与账本保留的记录"，把它们混进常规列表会让真实成员被淹没
// （实测一次全量测试后 85 条里 83 条是归档的）。需要查看归档记录时显式传
// includeArchived。
func (s *Store) ListMembers(ctx context.Context, limit int, subscriptionID string, includeArchived bool) ([]domain.Member, error) {
	q := `SELECT ` + memberCols + ` FROM members WHERE true`
	args := []any{}
	if !includeArchived {
		q += ` AND NOT archived`
	}
	if subscriptionID != "" {
		args = append(args, subscriptionID)
		q += ` AND subscription_id = $` + itoa(len(args))
	}
	q += ` ORDER BY lower(username) LIMIT $` + itoa(len(args)+1)
	args = append(args, clampLimit(limit))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Member, 0, 16)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, dbErr(rows.Err())
}

// listAllMembers returns every member page by page. Snapshots and internal
// reconciliation must use it instead of the public ListMembers limit, so a
// panel with more than 200 members never silently drops users from the
// authorization push.
func (s *Store) listAllMembers(ctx context.Context, subscriptionID string) ([]domain.Member, error) {
	out := make([]domain.Member, 0, 32)
	lastName, lastID := "", ""
	first := true
	for {
		q := `SELECT ` + memberCols + ` FROM members WHERE true`
		args := []any{}
		if subscriptionID != "" {
			args = append(args, subscriptionID)
			q += ` AND subscription_id = $` + itoa(len(args))
		}
		if !first {
			base := len(args)
			args = append(args, lastName, lastID)
			q += ` AND (lower(username), id) > ($` + itoa(base+1) + `::text, $` + itoa(base+2) + `::text)`
		}
		args = append(args, internalPageSize)
		q += ` ORDER BY lower(username), id LIMIT $` + itoa(len(args))
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, dbErr(err)
		}
		page := 0
		for rows.Next() {
			m, err := scanMember(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, *m)
			lastName, lastID = lowerString(m.Username), m.ID
			page++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, dbErr(err)
		}
		if page < internalPageSize {
			return out, nil
		}
		first = false
	}
}

// CountMembers returns the member counts used by the overview.
func (s *Store) CountMembers(ctx context.Context) (total, active int, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE active AND NOT archived) FROM members`).Scan(&total, &active)
	return total, active, dbErr(err)
}

// CreateMember inserts a member row.
func (s *Store) CreateMember(ctx context.Context, m *domain.Member) (*domain.Member, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO members (`+memberCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,false,$17,$18,1,now(),now())`,
			m.ID, m.Username, m.PasswordHash, m.Role, m.Active, m.ProxyEnabled, m.Archived, nullableString(m.SubscriptionID),
			nullableTime(m.ExpiresAt), nullableInt64(m.QuotaOverrideBytes), m.QuotaOverrideEnabled,
			nullableTime(m.PeriodStart), nullableTime(m.PeriodEnd), m.PeriodTimezone, m.PeriodAnchorDay,
			pendingJSON(m.PendingPeriodRule), nullableTime(m.BoundAt), nullableTime(m.LastLoginAt)); err != nil {
			return dbErr(err)
		}
		// A new member can grant access, so the revision that makes the agents
		// re-read their authorization commits together with the row.
		_, err := s.bumpRevisionsTx(ctx, tx, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetMember(ctx, m.ID)
}

// UpdateMember applies fn under the shared control lock and a member row lock.
// The last-admin invariant is checked in the same transaction as the update.
func (s *Store) UpdateMember(ctx context.Context, id string, expectedVersion int64, fn func(*domain.Member) error) (*domain.Member, error) {
	var out *domain.Member
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := lockMemberChangesTx(ctx, tx); err != nil {
			return err
		}
		cur, err := scanMember(tx.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if expectedVersion != 0 && cur.Version != expectedVersion {
			return domain.Conflict(domain.CodeConflict, "member was modified by another request")
		}
		before := *cur
		if err := fn(cur); err != nil {
			return err
		}
		if err := protectLastAdminTx(ctx, tx, &before, cur); err != nil {
			return err
		}
		cur.Version++
		cur.UpdatedAt = s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE members SET username=$2,password_hash=$3,role=$4,active=$5,
			proxy_enabled=$6,archived=$7,subscription_id=$8,expires_at=$9,quota_override_bytes=$10,
			quota_override_enabled=$11,period_start=$12,period_end=$13,period_timezone=$14,period_anchor_day=$15,
			pending_period_rule=$16,over_quota=$17,bound_at=$18,last_login_at=$19,version=$20,updated_at=$21 WHERE id=$1`,
			cur.ID, cur.Username, cur.PasswordHash, cur.Role, cur.Active, cur.ProxyEnabled, cur.Archived,
			nullableString(cur.SubscriptionID), nullableTime(cur.ExpiresAt), nullableInt64(cur.QuotaOverrideBytes),
			cur.QuotaOverrideEnabled, nullableTime(cur.PeriodStart), nullableTime(cur.PeriodEnd), cur.PeriodTimezone,
			cur.PeriodAnchorDay, pendingJSON(cur.PendingPeriodRule), cur.OverQuota, nullableTime(cur.BoundAt),
			nullableTime(cur.LastLoginAt), cur.Version, cur.UpdatedAt); err != nil {
			return dbErr(err)
		}
		// A binding change closes the previous binding history row and opens a
		// new one; history is never deleted.
		if derefString(before.SubscriptionID) != derefString(cur.SubscriptionID) {
			if _, err := tx.ExecContext(ctx, `UPDATE member_bindings SET ended_at=now()
				WHERE member_id=$1 AND ended_at IS NULL`, cur.ID); err != nil {
				return dbErr(err)
			}
			if cur.SubscriptionID != nil {
				if _, err := tx.ExecContext(ctx, `INSERT INTO member_bindings (id, member_id, subscription_id, bound_at, expires_at, reason)
					VALUES ($1,$2,$3,now(),$4,'binding_change')`,
					memberBindingID(cur.ID, cur.Version), cur.ID, *cur.SubscriptionID, nullableTime(cur.ExpiresAt)); err != nil {
					return dbErr(err)
				}
			}
		}
		// Active, proxy switch, binding, expiry and quota changes all decide
		// what agents may serve: publish the revision in this transaction.
		if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

func memberBindingID(memberID string, version int64) string {
	return "mbd_" + memberID + "_" + itoa(int(version)) + "_" + itoa(int(time.Now().UnixNano()%1000000))
}

// lockMemberChangesTx serializes ordinary member changes with batch changes.
// Always take this lock before member rows so cross-member removals cannot both
// count the other administrator as still usable.
func lockMemberChangesTx(ctx context.Context, tx *sql.Tx) error {
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT revision_counter FROM control_state WHERE id=1 FOR UPDATE`).Scan(&revision)
	return dbErr(err)
}

// protectLastAdminTx requires lockMemberChangesTx and counts through the writer's
// transaction, including earlier changes in the same batch.
func protectLastAdminTx(ctx context.Context, tx *sql.Tx, before, after *domain.Member) error {
	usable := func(m *domain.Member) bool { return m.Role == "admin" && m.Active && !m.Archived }
	if !usable(before) || usable(after) {
		return nil
	}
	n, err := countActiveAdmins(ctx, tx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return domain.E(domain.CodeLastAdmin, 409, "the last active administrator cannot be removed")
	}
	return nil
}

// countActiveAdmins counts usable admins using the supplied transaction/query.
func countActiveAdmins(ctx context.Context, q querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM members
		WHERE role='admin' AND active AND NOT archived`).Scan(&n)
	return n, dbErr(err)
}

// CountActiveAdmins is a read-only observation, not a safe mutation guard.
func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	return countActiveAdmins(ctx, s.DB)
}

// ─── sessions ──────────────────────────────────────────────────────────────

func (s *Store) CreateSession(ctx context.Context, sess *domain.Session) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions (id, member_id, token_hash, csrf_token, ip, user_agent,
		created_at, last_seen_at, expires_at) VALUES ($1,$2,$3,$4,$5,$6,now(),now(),$7)`,
		sess.ID, sess.MemberID, sess.TokenHash, sess.CSRFToken, sess.IP, sess.UserAgent, sess.ExpiresAt)
	return dbErr(err)
}

// SessionByTokenHash returns the session and member for a cookie token hash.
// The lookup always hits PostgreSQL: a cache hit must never bypass revocation.
func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, *domain.Member, error) {
	var sess domain.Session
	var lastSeen sql.NullTime
	var revoked sql.NullTime
	err := s.DB.QueryRowContext(ctx, `SELECT id, member_id, csrf_token, ip, user_agent, created_at, last_seen_at,
		expires_at, revoked_at FROM sessions WHERE token_hash=$1`, tokenHash).Scan(
		&sess.ID, &sess.MemberID, &sess.CSRFToken, &sess.IP, &sess.UserAgent, &sess.CreatedAt, &lastSeen,
		&sess.ExpiresAt, &revoked)
	if err != nil {
		if de, ok := dbErr(err).(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, nil, domain.E(domain.CodeUnauthenticated, 401, "not authenticated")
		}
		return nil, nil, dbErr(err)
	}
	sess.CreatedAt = utc(sess.CreatedAt)
	sess.ExpiresAt = utc(sess.ExpiresAt)
	if lastSeen.Valid {
		t := lastSeen.Time.UTC()
		sess.LastSeenAt = &t
	}
	if revoked.Valid {
		t := revoked.Time.UTC()
		sess.RevokedAt = &t
	}
	if sess.RevokedAt != nil || !sess.ExpiresAt.After(s.now()) {
		return nil, nil, domain.E(domain.CodeUnauthenticated, 401, "session expired or revoked")
	}
	m, err := s.GetMember(ctx, sess.MemberID)
	if err != nil {
		return nil, nil, err
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at=now() WHERE id=$1`, sess.ID)
	return &sess, m, nil
}

func (s *Store) RevokeSession(ctx context.Context, id, memberID string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE sessions SET revoked_at=now()
		WHERE id=$1 AND member_id=$2 AND revoked_at IS NULL`, id, memberID)
	if err != nil {
		return dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.NotFound("session not found")
	}
	return nil
}

func (s *Store) RevokeAllSessions(ctx context.Context, memberID, exceptSessionID string) (int, error) {
	q := `UPDATE sessions SET revoked_at=now() WHERE member_id=$1 AND revoked_at IS NULL`
	args := []any{memberID}
	if exceptSessionID != "" {
		q += ` AND id <> $2`
		args = append(args, exceptSessionID)
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, dbErr(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) ListSessions(ctx context.Context, memberID, currentSessionID string) ([]domain.Session, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, member_id, csrf_token, ip, user_agent, created_at, last_seen_at,
		expires_at, revoked_at FROM sessions WHERE member_id=$1 ORDER BY created_at DESC LIMIT 100`, memberID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Session, 0, 4)
	for rows.Next() {
		var v domain.Session
		var lastSeen, revoked sql.NullTime
		if err := rows.Scan(&v.ID, &v.MemberID, &v.CSRFToken, &v.IP, &v.UserAgent, &v.CreatedAt, &lastSeen,
			&v.ExpiresAt, &revoked); err != nil {
			return nil, dbErr(err)
		}
		v.CreatedAt = utc(v.CreatedAt)
		v.ExpiresAt = utc(v.ExpiresAt)
		if lastSeen.Valid {
			t := lastSeen.Time.UTC()
			v.LastSeenAt = &t
		}
		if revoked.Valid {
			t := revoked.Time.UTC()
			v.RevokedAt = &t
		}
		v.CSRFToken = "" // never list CSRF tokens
		v.Current = v.ID == currentSessionID
		out = append(out, v)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) TouchLogin(ctx context.Context, memberID string) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE members SET last_login_at=now() WHERE id=$1`, memberID)
}

// ─── invitations ───────────────────────────────────────────────────────────

const invitationCols = `id, subscription_id, expires_at, max_uses, used_count, used_at, used_by_member_id,
	revoked_at, created_by, created_at`

func scanInvitation(row interface{ Scan(...any) error }) (*domain.Invitation, error) {
	var v domain.Invitation
	var subID, usedBy sql.NullString
	var usedAt, revoked sql.NullTime
	if err := row.Scan(&v.ID, &subID, &v.ExpiresAt, &v.MaxUses, &v.UsedCount, &usedAt, &usedBy, &revoked,
		&v.CreatedBy, &v.CreatedAt); err != nil {
		return nil, dbErr(err)
	}
	if subID.Valid {
		x := subID.String
		v.SubscriptionID = &x
	}
	if usedBy.Valid {
		x := usedBy.String
		v.UsedByMemberID = &x
	}
	if usedAt.Valid {
		t := usedAt.Time.UTC()
		v.UsedAt = &t
	}
	if revoked.Valid {
		t := revoked.Time.UTC()
		v.RevokedAt = &t
	}
	v.ExpiresAt = utc(v.ExpiresAt)
	v.CreatedAt = utc(v.CreatedAt)
	switch {
	case v.RevokedAt != nil:
		v.State = "revoked"
	case v.UsedCount >= v.MaxUses:
		v.State = "used"
	case !v.ExpiresAt.After(time.Now().UTC()):
		v.State = "expired"
	default:
		v.State = "unused"
	}
	return &v, nil
}

func (s *Store) CreateInvitation(ctx context.Context, codeHash string, subID *string, expiresAt time.Time, maxUses int, createdBy string) (string, error) {
	id := newID("inv")
	if maxUses <= 0 {
		maxUses = 1
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO invitations (id, code_hash, subscription_id, expires_at, max_uses, created_by)
		VALUES ($1,$2,$3,$4,$5,$6)`, id, codeHash, nullableString(subID), expiresAt.UTC(), maxUses, createdBy)
	return id, dbErr(err)
}

func (s *Store) ListInvitations(ctx context.Context, limit int) ([]domain.Invitation, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+invitationCols+` FROM invitations ORDER BY created_at DESC LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Invitation, 0, 8)
	for rows.Next() {
		v, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, dbErr(rows.Err())
}

// InvitationByCodeHash loads an invitation for redemption.
func (s *Store) InvitationByCodeHash(ctx context.Context, codeHash string) (*domain.Invitation, error) {
	v, err := scanInvitation(s.DB.QueryRowContext(ctx, `SELECT `+invitationCols+` FROM invitations WHERE code_hash=$1`, codeHash))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.Validation("invitation is not usable")
		}
		return nil, err
	}
	return v, nil
}

// RedeemInvitation atomically consumes an invitation and creates the member.
func (s *Store) RedeemInvitation(ctx context.Context, codeHash string, member *domain.Member) (*domain.Member, error) {
	var created *domain.Member
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var id string
		var expiresAt time.Time
		var maxUses, usedCount int
		var revoked, usedAt sql.NullTime
		var subID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT id, subscription_id, expires_at, max_uses, used_count, used_at, revoked_at
			FROM invitations WHERE code_hash=$1 FOR UPDATE`, codeHash).Scan(
			&id, &subID, &expiresAt, &maxUses, &usedCount, &usedAt, &revoked); err != nil {
			return domain.Validation("invitation is not usable")
		}
		if revoked.Valid || usedAt.Valid || usedCount >= maxUses || !expiresAt.After(s.now()) {
			return domain.Validation("invitation is not usable")
		}
		if subID.Valid {
			member.SubscriptionID = &subID.String
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO members (`+memberCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,false,$17,now(),1,now(),now())`,
			member.ID, member.Username, member.PasswordHash, member.Role, member.Active, member.ProxyEnabled,
			member.Archived, nullableString(member.SubscriptionID), nullableTime(member.ExpiresAt),
			nullableInt64(member.QuotaOverrideBytes), member.QuotaOverrideEnabled, nullableTime(member.PeriodStart),
			nullableTime(member.PeriodEnd), member.PeriodTimezone, member.PeriodAnchorDay,
			pendingJSON(member.PendingPeriodRule), nullableTime(member.BoundAt)); err != nil {
			return dbErr(err)
		}
		// Keep the invitation row locked, but insert the member before writing
		// its immediately checked used_by_member_id foreign key.
		if _, err := tx.ExecContext(ctx, `UPDATE invitations SET used_count=used_count+1, used_at=now(),
			used_by_member_id=$2 WHERE id=$1`, id, member.ID); err != nil {
			return dbErr(err)
		}
		// The redeemed member is a new authorization subject: publish it.
		if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	created, err = s.GetMember(ctx, member.ID)
	return created, err
}

func (s *Store) RevokeInvitation(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE invitations SET revoked_at=now()
		WHERE id=$1 AND revoked_at IS NULL AND used_at IS NULL`, id)
	if err != nil {
		return dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.Conflict(domain.CodeConflict, "invitation is already used, revoked or missing")
	}
	return nil
}

// ─── subscription tokens ───────────────────────────────────────────────────

// ActiveSubscriptionToken returns the decrypted token of a member.
func (s *Store) ActiveSubscriptionToken(ctx context.Context, memberID string) (token string, id string, err error) {
	var enc []byte
	err = s.DB.QueryRowContext(ctx, `SELECT id, token_enc FROM subscription_tokens
		WHERE member_id=$1 AND revoked_at IS NULL`, memberID).Scan(&id, &enc)
	if err != nil {
		if de, ok := dbErr(err).(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return "", "", domain.NotFound("member has no active subscription token")
		}
		return "", "", dbErr(err)
	}
	plain, err := s.decryptSecret(enc)
	return plain, id, err
}

// EnsureSubscriptionToken creates a token if the member has none.
func (s *Store) EnsureSubscriptionToken(ctx context.Context, memberID, tokenHash string, tokenPlain string) (string, error) {
	enc, err := s.encryptSecret(tokenPlain)
	if err != nil {
		return "", err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO subscription_tokens (id, member_id, token_hash, token_enc)
		VALUES ($1,$2,$3,$4) ON CONFLICT (member_id) WHERE revoked_at IS NULL DO NOTHING`,
		newID("stk"), memberID, tokenHash, enc)
	if err != nil {
		return "", dbErr(err)
	}
	return s.memberTokenPlain(ctx, memberID)
}

func (s *Store) memberTokenPlain(ctx context.Context, memberID string) (string, error) {
	tok, _, err := s.ActiveSubscriptionToken(ctx, memberID)
	return tok, err
}

// RotateSubscriptionToken revokes the old token and stores a new one. Existing
// proxy credentials are untouched (contract: rotation does not revoke access).
func (s *Store) RotateSubscriptionToken(ctx context.Context, memberID, tokenHash, tokenPlain string) (string, error) {
	enc, err := s.encryptSecret(tokenPlain)
	if err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE subscription_tokens SET revoked_at=now()
			WHERE member_id=$1 AND revoked_at IS NULL`, memberID); err != nil {
			return dbErr(err)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO subscription_tokens (id, member_id, token_hash, token_enc, rotated_at)
			VALUES ($1,$2,$3,$4,now())`, newID("stk"), memberID, tokenHash, enc)
		return dbErr(err)
	})
	if err != nil {
		return "", err
	}
	return tokenPlain, nil
}

// MemberBySubscriptionToken resolves a public token to its member.
func (s *Store) MemberBySubscriptionToken(ctx context.Context, tokenHash string) (*domain.Member, string, error) {
	var memberID, tokenID string
	err := s.DB.QueryRowContext(ctx, `SELECT member_id, id FROM subscription_tokens
		WHERE token_hash=$1 AND revoked_at IS NULL`, tokenHash).Scan(&memberID, &tokenID)
	if err != nil {
		if de, ok := dbErr(err).(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, "", domain.NotFound("subscription token not found")
		}
		return nil, "", dbErr(err)
	}
	m, err := s.GetMember(ctx, memberID)
	if err != nil {
		return nil, "", err
	}
	return m, tokenID, nil
}

// ─── credentials ───────────────────────────────────────────────────────────

// MemberCredentials returns active credentials with decrypted secrets.
func (s *Store) MemberCredentials(ctx context.Context, memberID string) ([]domain.Credential, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id, c.member_id, c.node_id, n.name, c.kind, c.state,
		c.created_at, c.revoked_at, c.rotated_reason, c.secret_enc
		FROM credentials c JOIN nodes n ON n.id=c.node_id
		WHERE c.member_id=$1 AND c.state='active' ORDER BY n.sort_order, lower(n.name)`, memberID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Credential, 0, 8)
	for rows.Next() {
		var v domain.Credential
		var enc []byte
		var revokedAt sql.NullTime
		if err := rows.Scan(&v.ID, &v.MemberID, &v.NodeID, &v.NodeName, &v.Kind, &v.State, &v.CreatedAt,
			&revokedAt, &v.RotatedReason, &enc); err != nil {
			return nil, dbErr(err)
		}
		v.CreatedAt = utc(v.CreatedAt)
		if revokedAt.Valid {
			t := revokedAt.Time.UTC()
			v.RevokedAt = &t
		}
		if plain, err := s.decryptSecret(enc); err == nil {
			v.Secret = plain
		}
		out = append(out, v)
	}
	return out, dbErr(rows.Err())
}

// EnsureCredentials creates credentials for the given nodes when missing and
// revokes active credentials for nodes that are no longer authorized.
func (s *Store) EnsureCredentials(ctx context.Context, memberID string, nodeIDs []string, gen func(kind string) (id, secret, kind2 string, err error)) (created int, revoked int, err error) {
	want := map[string]bool{}
	for _, id := range nodeIDs {
		want[id] = true
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, node_id, kind FROM credentials
			WHERE member_id=$1 AND state='active' FOR UPDATE`, memberID)
		if err != nil {
			return dbErr(err)
		}
		existing := map[string]string{}
		var toRevoke []string
		for rows.Next() {
			var id, nodeID, kind string
			if err := rows.Scan(&id, &nodeID, &kind); err != nil {
				rows.Close()
				return dbErr(err)
			}
			existing[nodeID] = kind
			if !want[nodeID] {
				toRevoke = append(toRevoke, id)
			}
		}
		rows.Close()
		for _, id := range toRevoke {
			if _, err := tx.ExecContext(ctx, `UPDATE credentials SET state='revoked', revoked_at=now(),
				rotated_reason='node unauthorized' WHERE id=$1`, id); err != nil {
				return dbErr(err)
			}
			revoked++
		}
		for nodeID := range want {
			if _, ok := existing[nodeID]; ok {
				continue
			}
			credID, secret, kind, err := gen("")
			if err != nil {
				return err
			}
			enc, err := s.encryptSecret(secret)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO credentials (id, member_id, node_id, kind, secret_enc, state)
				VALUES ($1,$2,$3,$4,$5,'active') ON CONFLICT (member_id, node_id) WHERE state='active' DO NOTHING`,
				credID, memberID, nodeID, kind, enc); err != nil {
				return dbErr(err)
			}
			created++
		}
		// The credential set is what agents render: revocations and new
		// secrets must reach them with the same commit.
		if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
			return err
		}
		return nil
	})
	return created, revoked, err
}

// RotateCredentials revokes the active credentials for the given nodes and
// replaces them in one transaction (hard revocation of the old secrets).
func (s *Store) RotateCredentials(ctx context.Context, memberID string, nodeIDs []string, gen func(nodeID string) (id, secret, kind string, err error)) (int, error) {
	count := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, nodeID := range nodeIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE credentials SET state='revoked', revoked_at=now(),
				rotated_reason='rotation' WHERE member_id=$1 AND node_id=$2 AND state='active'`, memberID, nodeID); err != nil {
				return dbErr(err)
			}
			credID, secret, kind, err := gen(nodeID)
			if err != nil {
				return err
			}
			enc, err := s.encryptSecret(secret)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO credentials (id, member_id, node_id, kind, secret_enc, state)
				VALUES ($1,$2,$3,$4,$5,'active')`, credID, memberID, nodeID, kind, enc); err != nil {
				return dbErr(err)
			}
			count++
		}
		// The rotation hard revokes the previous secrets, so the new set is
		// published in this transaction.
		if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
			return err
		}
		return nil
	})
	return count, err
}

// PurgeCredentials revokes every active credential of a member (hard revoke).
func (s *Store) PurgeCredentials(ctx context.Context, memberID, reason string) (int, error) {
	n := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE credentials SET state='revoked', revoked_at=now(), rotated_reason=$2
			WHERE member_id=$1 AND state='active'`, memberID, reason)
		if err != nil {
			return dbErr(err)
		}
		affected, _ := res.RowsAffected()
		n = int(affected)
		_, err = s.bumpRevisionsTx(ctx, tx, nil)
		return err
	})
	return n, err
}

// NodeHasCredentialHistory reports whether a member ever had a credential on a
// node (historical mapping used by usage accounting).
func (s *Store) NodeHasCredentialHistory(ctx context.Context, nodeID, memberID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM credentials WHERE node_id=$1 AND member_id=$2`).Scan(&n)
	return n > 0, dbErr(err)
}
