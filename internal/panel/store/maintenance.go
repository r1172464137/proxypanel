package store

// Maintenance pass persistence: alert state reconciliation, the durable
// Telegram notification outbox and bounded retention cleanup.
//
// Everything in this file is driven by internal/panel/maintenance, but the
// authority is PostgreSQL (plan F11/F12): the pass runs inside one transaction
// guarded by a pg advisory lock, so exactly one panel process can run it at a
// time, and alert state survives restarts because it lives in `events`.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"proxypanel/internal/panel/access"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
)

// ─── alert model ───────────────────────────────────────────────────────────

// Alert kinds owned by the maintenance pass. Alert state is persisted in
// `events`: exactly one open row per (kind, subject) through
// events.dedupe_key = "<kind>:<subject_id>" (backed by the partial unique
// index events_open_dedupe), plus a recovery row that closes it.
const (
	AlertAgentOffline       = "agent_offline"
	AlertCoreInstanceFailed = "core_instance_failed"
	AlertMemberExpired      = "member_expired"
	// AlertMemberOverQuota reuses the dedupe key that the usage ledger already
	// writes ("over_quota:<member>"), so the pass adopts that event instead of
	// creating a second one for the same condition.
	AlertMemberOverQuota = "over_quota"
	AlertServerExpiring  = "server_expiring"
)

// MaintenanceAlertKinds lists every alert kind reconciled by the pass.
var MaintenanceAlertKinds = []string{
	AlertAgentOffline,
	AlertCoreInstanceFailed,
	AlertMemberExpired,
	AlertMemberOverQuota,
	AlertServerExpiring,
}

const (
	// AgentOfflineAfter is the heartbeat tolerance. A freshly registered agent
	// gets the same grace because created_at is used until the first heartbeat.
	AgentOfflineAfter = 45 * time.Second
	// ServerExpiryHorizon warns this far ahead of servers.expires_at.
	ServerExpiryHorizon = 7 * 24 * time.Hour
	// MinRetentionDays is the hard lower bound of the cleanup policy: a
	// misconfigured retention can never delete recent history.
	MinRetentionDays = 7
	// MaxRetentionDays keeps a typo from pinning rows forever.
	MaxRetentionDays = 3650
	// MaintenanceLockKey is the advisory lock key of the maintenance pass. It
	// is deliberately different from the migration lock (7401941).
	MaintenanceLockKey int64 = 7401942
)

// ErrMaintenanceBusy reports that another session already holds the lock.
var ErrMaintenanceBusy = errors.New("store: another maintenance pass is running")

// MaintenanceBounds bounds a single pass. Nothing here is unbounded.
type MaintenanceBounds struct {
	// AlertScan is the candidate row limit per alert kind.
	AlertScan int
	// Cleanup is the row limit per delete statement.
	Cleanup int
}

// DefaultMaintenanceBounds returns the bounds used by the panel.
func DefaultMaintenanceBounds() MaintenanceBounds {
	return MaintenanceBounds{AlertScan: 200, Cleanup: 500}
}

func normalizeBounds(b MaintenanceBounds) MaintenanceBounds {
	def := DefaultMaintenanceBounds()
	if b.AlertScan <= 0 {
		b.AlertScan = def.AlertScan
	}
	if b.AlertScan > 2000 {
		b.AlertScan = 2000
	}
	if b.Cleanup <= 0 {
		b.Cleanup = def.Cleanup
	}
	if b.Cleanup > 5000 {
		b.Cleanup = 5000
	}
	return b
}

// NormalizeRetentionDays clamps a configured retention to the policy floor.
func NormalizeRetentionDays(days int) int {
	if days < MinRetentionDays {
		return MinRetentionDays
	}
	if days > MaxRetentionDays {
		return MaxRetentionDays
	}
	return days
}

// AlertObservation is one (kind, subject) condition currently true in
// PostgreSQL. It is a fact, not a decision: the caller decides what to do.
type AlertObservation struct {
	Kind        string
	Severity    string
	SubjectType string
	SubjectID   string
	SubjectName string
	Message     string
	DedupeKey   string
}

// ObservationSet is the alert view of one pass. Incomplete marks kinds whose
// candidate scan hit MaintenanceBounds.AlertScan; such kinds must never be
// auto-recovered, because "not observed" would only mean "not scanned".
type ObservationSet struct {
	Alerts     []AlertObservation
	Incomplete map[string]bool
}

// Complete reports whether the scan for kind covered every candidate row.
func (o ObservationSet) Complete(kind string) bool { return !o.Incomplete[kind] }

// OpenAlert is an open (unrecovered) event owned by the maintenance pass.
type OpenAlert struct {
	EventID     string
	Kind        string
	Severity    string
	SubjectType string
	SubjectID   string
	Message     string
	DedupeKey   string
}

// MaintenanceTx is the transaction handle of one maintenance pass. It is only
// valid inside InMaintenanceTx.
type MaintenanceTx struct {
	s      *Store
	tx     *sql.Tx
	bounds MaintenanceBounds
}

// InMaintenanceTx runs fn in one transaction that holds the maintenance
// advisory lock, guaranteeing a single executor across panel processes. It
// returns ErrMaintenanceBusy when another session holds the lock (fn is not
// called and no work is done).
func (s *Store) InMaintenanceTx(ctx context.Context, bounds MaintenanceBounds, fn func(context.Context, *MaintenanceTx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return dbErr(err)
	}
	rollback := func() { _ = tx.Rollback() }
	var locked bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", MaintenanceLockKey).Scan(&locked); err != nil {
		rollback()
		return fmt.Errorf("maintenance lock: %w", dbErr(err))
	}
	if !locked {
		rollback()
		return ErrMaintenanceBusy
	}
	defer func() {
		if p := recover(); p != nil {
			rollback()
			panic(p)
		}
	}()
	m := &MaintenanceTx{s: s, tx: tx, bounds: normalizeBounds(bounds)}
	if err := fn(ctx, m); err != nil {
		rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return dbErr(err)
	}
	return nil
}

// Settings reads the current settings row inside the pass (retention, whether
// Telegram is enabled). The bot token is not read here.
func (m *MaintenanceTx) Settings(ctx context.Context) (*domain.Settings, error) {
	var v domain.Settings
	var tokenEnc, defaults []byte
	if err := m.tx.QueryRowContext(ctx, `SELECT timezone, retention_days, telegram_enabled, telegram_chat_id,
		telegram_token_enc, telegram_token_set, template_defaults, version, updated_at
		FROM settings WHERE id=1`).Scan(&v.Timezone, &v.RetentionDays, &v.TelegramEnabled, &v.TelegramChatID,
		&tokenEnc, &v.TelegramTokenSet, &defaults, &v.Version, &v.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	v.TemplateDefaults = scanObject(defaults)
	v.UpdatedAt = utc(v.UpdatedAt)
	return &v, nil
}

// ─── observation ───────────────────────────────────────────────────────────

// ObserveAlerts computes the alert facts. The member checks confirm every
// candidate with the single access algorithm (internal/panel/access) instead of
// re-implementing the rules here.
func (m *MaintenanceTx) ObserveAlerts(ctx context.Context, now time.Time) (ObservationSet, error) {
	now = now.UTC()
	set := ObservationSet{Alerts: make([]AlertObservation, 0, 8), Incomplete: map[string]bool{}}
	if err := m.observeAgents(ctx, now, &set); err != nil {
		return set, err
	}
	if err := m.observeCoreInstances(ctx, &set); err != nil {
		return set, err
	}
	if err := m.observeExpiredMembers(ctx, now, &set); err != nil {
		return set, err
	}
	if err := m.observeOverQuotaMembers(ctx, now, &set); err != nil {
		return set, err
	}
	if err := m.observeExpiringServers(ctx, now, &set); err != nil {
		return set, err
	}
	sort.Slice(set.Alerts, func(i, j int) bool { return set.Alerts[i].DedupeKey < set.Alerts[j].DedupeKey })
	return set, nil
}

func alertKey(kind, subjectID string) string { return kind + ":" + subjectID }

func (m *MaintenanceTx) observeAgents(ctx context.Context, now time.Time, set *ObservationSet) error {
	rows, err := m.tx.QueryContext(ctx, `SELECT id, name, last_heartbeat_at, created_at
		FROM agents
		WHERE NOT archived AND COALESCE(last_heartbeat_at, created_at) <= $1
		ORDER BY COALESCE(last_heartbeat_at, created_at)
		LIMIT $2`, now.Add(-AgentOfflineAfter), m.bounds.AlertScan+1)
	if err != nil {
		return dbErr(err)
	}
	defer rows.Close()
	type candidate struct {
		id, name string
		lastSeen time.Time
		never    bool
	}
	var cands []candidate
	for rows.Next() {
		var id, name string
		var heartbeat sql.NullTime
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &heartbeat, &createdAt); err != nil {
			return dbErr(err)
		}
		c := candidate{id: id, name: name, lastSeen: createdAt.UTC(), never: !heartbeat.Valid}
		if heartbeat.Valid {
			c.lastSeen = heartbeat.Time.UTC()
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return dbErr(err)
	}
	if len(cands) > m.bounds.AlertScan {
		set.Incomplete[AlertAgentOffline] = true
		cands = cands[:m.bounds.AlertScan]
	}
	for _, c := range cands {
		var msg string
		if c.never {
			msg = fmt.Sprintf("agent %q has not sent a heartbeat since registration %s",
				c.name, c.lastSeen.Format(time.RFC3339))
		} else {
			msg = fmt.Sprintf("agent %q has been offline for %s (last heartbeat %s)",
				c.name, shortDuration(now.Sub(c.lastSeen)), c.lastSeen.Format(time.RFC3339))
		}
		set.Alerts = append(set.Alerts, AlertObservation{
			Kind:        AlertAgentOffline,
			Severity:    "warning",
			SubjectType: "agent",
			SubjectID:   c.id,
			SubjectName: c.name,
			Message:     msg,
			DedupeKey:   alertKey(AlertAgentOffline, c.id),
		})
	}
	return nil
}

func (m *MaintenanceTx) observeCoreInstances(ctx context.Context, set *ObservationSet) error {
	rows, err := m.tx.QueryContext(ctx, `SELECT ci.id, ci.node_id, n.name, ci.last_error
		FROM core_instances ci JOIN nodes n ON n.id = ci.node_id
		WHERE ci.state = 'failed'
		ORDER BY ci.updated_at DESC
		LIMIT $1`, m.bounds.AlertScan+1)
	if err != nil {
		return dbErr(err)
	}
	defer rows.Close()
	type candidate struct {
		id, nodeID, nodeName, lastError string
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.nodeID, &c.nodeName, &c.lastError); err != nil {
			return dbErr(err)
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return dbErr(err)
	}
	if len(cands) > m.bounds.AlertScan {
		set.Incomplete[AlertCoreInstanceFailed] = true
		cands = cands[:m.bounds.AlertScan]
	}
	for _, c := range cands {
		msg := fmt.Sprintf("core instance for node %q is in state failed", c.nodeName)
		if detail := strings.TrimSpace(c.lastError); detail != "" {
			msg += ": " + truncateMessage(detail, 200)
		}
		set.Alerts = append(set.Alerts, AlertObservation{
			Kind:        AlertCoreInstanceFailed,
			Severity:    "error",
			SubjectType: "core_instance",
			SubjectID:   c.id,
			SubjectName: c.nodeName,
			Message:     msg,
			DedupeKey:   alertKey(AlertCoreInstanceFailed, c.id),
		})
	}
	return nil
}

// memberDecision loads the authoritative access decision for a member. The
// access algorithm stays the single source of truth: no rule is re-implemented
// for alerting.
func (m *MaintenanceTx) memberDecision(ctx context.Context, memberID string) (*domain.Member, access.Decision, error) {
	mem, err := m.s.GetMember(ctx, memberID)
	if err != nil {
		return nil, access.Decision{}, err
	}
	res, err := m.s.EvaluateAccessForMember(ctx, mem)
	if err != nil {
		return nil, access.Decision{}, err
	}
	return mem, res.Decision, nil
}

func hasReason(d access.Decision, reason string) bool {
	for _, r := range d.Reasons {
		if r == reason {
			return true
		}
	}
	return false
}

// skipNotFound tolerates a row that disappeared between the candidate scan and
// the confirmation query (concurrent delete) instead of failing the pass.
func skipNotFound(err error) bool {
	var de *domain.Error
	return errors.As(err, &de) && de.Code == domain.CodeNotFound
}

func (m *MaintenanceTx) observeExpiredMembers(ctx context.Context, now time.Time, set *ObservationSet) error {
	rows, err := m.tx.QueryContext(ctx, `SELECT m.id, m.username, m.expires_at
		FROM members m
		WHERE NOT m.archived AND m.active AND m.subscription_id IS NOT NULL
		  AND m.expires_at IS NOT NULL AND m.expires_at <= $1
		ORDER BY m.expires_at
		LIMIT $2`, now, m.bounds.AlertScan+1)
	if err != nil {
		return dbErr(err)
	}
	type candidate struct {
		id, username string
		expiresAt    time.Time
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.username, &c.expiresAt); err != nil {
			rows.Close()
			return dbErr(err)
		}
		c.expiresAt = c.expiresAt.UTC()
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return dbErr(err)
	}
	rows.Close()
	if len(cands) > m.bounds.AlertScan {
		set.Incomplete[AlertMemberExpired] = true
		cands = cands[:m.bounds.AlertScan]
	}
	for _, c := range cands {
		_, decision, err := m.memberDecision(ctx, c.id)
		if err != nil {
			if skipNotFound(err) {
				continue
			}
			return err
		}
		if !hasReason(decision, access.ReasonBindingExpired) {
			continue
		}
		set.Alerts = append(set.Alerts, AlertObservation{
			Kind:        AlertMemberExpired,
			Severity:    "warning",
			SubjectType: "member",
			SubjectID:   c.id,
			SubjectName: c.username,
			Message: fmt.Sprintf("member %q binding expired at %s",
				c.username, c.expiresAt.Format(time.RFC3339)),
			DedupeKey: alertKey(AlertMemberExpired, c.id),
		})
	}
	return nil
}

func (m *MaintenanceTx) observeOverQuotaMembers(ctx context.Context, now time.Time, set *ObservationSet) error {
	// The candidate filter is only an index-friendly narrowing; the access
	// decision below is authoritative.
	rows, err := m.tx.QueryContext(ctx, `
		SELECT m.id, m.username
		FROM members m
		JOIN subscriptions s ON s.id = m.subscription_id
		WHERE NOT m.archived AND m.active AND m.proxy_enabled
		  AND NOT s.archived AND s.access_enabled
		  AND COALESCE(CASE WHEN m.quota_override_enabled THEN m.quota_override_bytes ELSE s.quota_bytes END) IS NOT NULL
		  AND (m.over_quota OR COALESCE((
			SELECT SUM(ue.up_bytes + ue.down_bytes) FROM usage_entries ue
			WHERE ue.member_id = m.id
			  AND ((m.period_start IS NULL AND ue.period_start IS NULL) OR ue.period_start = m.period_start)
		  ), 0) >= COALESCE(CASE WHEN m.quota_override_enabled THEN m.quota_override_bytes ELSE s.quota_bytes END))
		ORDER BY m.id
		LIMIT $1`, m.bounds.AlertScan+1)
	if err != nil {
		return dbErr(err)
	}
	type candidate struct{ id, username string }
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.username); err != nil {
			rows.Close()
			return dbErr(err)
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return dbErr(err)
	}
	rows.Close()
	if len(cands) > m.bounds.AlertScan {
		set.Incomplete[AlertMemberOverQuota] = true
		cands = cands[:m.bounds.AlertScan]
	}
	for _, c := range cands {
		_, decision, err := m.memberDecision(ctx, c.id)
		if err != nil {
			if skipNotFound(err) {
				continue
			}
			return err
		}
		if !hasReason(decision, access.ReasonOverQuota) {
			continue
		}
		set.Alerts = append(set.Alerts, AlertObservation{
			Kind:        AlertMemberOverQuota,
			Severity:    "warning",
			SubjectType: "member",
			SubjectID:   c.id,
			SubjectName: c.username,
			Message: fmt.Sprintf("member %q reached the effective quota (%s of %s)",
				c.username, humanBytes(decision.UsedBytes), quotaText(decision.EffectiveQuotaBytes)),
			DedupeKey: alertKey(AlertMemberOverQuota, c.id),
		})
	}
	return nil
}

func (m *MaintenanceTx) observeExpiringServers(ctx context.Context, now time.Time, set *ObservationSet) error {
	rows, err := m.tx.QueryContext(ctx, `SELECT id, name, expires_at
		FROM servers
		WHERE NOT archived AND expires_at IS NOT NULL AND expires_at <= $1
		ORDER BY expires_at
		LIMIT $2`, now.Add(ServerExpiryHorizon), m.bounds.AlertScan+1)
	if err != nil {
		return dbErr(err)
	}
	defer rows.Close()
	type candidate struct {
		id, name  string
		expiresAt time.Time
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.name, &c.expiresAt); err != nil {
			return dbErr(err)
		}
		c.expiresAt = c.expiresAt.UTC()
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return dbErr(err)
	}
	if len(cands) > m.bounds.AlertScan {
		set.Incomplete[AlertServerExpiring] = true
		cands = cands[:m.bounds.AlertScan]
	}
	for _, c := range cands {
		severity, msg := "warning", ""
		if c.expiresAt.After(now) {
			msg = fmt.Sprintf("server %q expires in %s (at %s)",
				c.name, shortDuration(c.expiresAt.Sub(now)), c.expiresAt.Format(time.RFC3339))
		} else {
			severity = "error"
			msg = fmt.Sprintf("server %q expired %s ago (at %s)",
				c.name, shortDuration(now.Sub(c.expiresAt)), c.expiresAt.Format(time.RFC3339))
		}
		set.Alerts = append(set.Alerts, AlertObservation{
			Kind:        AlertServerExpiring,
			Severity:    severity,
			SubjectType: "server",
			SubjectID:   c.id,
			SubjectName: c.name,
			Message:     msg,
			DedupeKey:   alertKey(AlertServerExpiring, c.id),
		})
	}
	return nil
}

// ─── alert state ───────────────────────────────────────────────────────────

// OpenAlerts lists the open events owned by the pass. Ownership is decided by
// the dedupe key prefix ("<kind>:<subject>"), so an event written by another
// component for the same condition (for example the ledger's over quota event)
// is adopted instead of duplicated.
func (m *MaintenanceTx) OpenAlerts(ctx context.Context, kinds []string) ([]OpenAlert, error) {
	kinds = normalizeKinds(kinds)
	if len(kinds) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(kinds))
	placeholders := make([]string, 0, len(kinds))
	for i, k := range kinds {
		args = append(args, k)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
	}
	q := `SELECT id, kind, severity, subject_type, subject_id, message, dedupe_key
		FROM events
		WHERE state = 'open' AND dedupe_key IS NOT NULL
		  AND split_part(dedupe_key, ':', 1) IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY created_at, id`
	rows, err := m.tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]OpenAlert, 0, 8)
	for rows.Next() {
		var a OpenAlert
		if err := rows.Scan(&a.EventID, &a.Kind, &a.Severity, &a.SubjectType, &a.SubjectID, &a.Message, &a.DedupeKey); err != nil {
			return nil, dbErr(err)
		}
		out = append(out, a)
	}
	return out, dbErr(rows.Err())
}

func normalizeKinds(kinds []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RaiseAlert opens an alert event. When an open event with the same dedupe key
// already exists (written by this pass in an earlier tick, or by another
// component) it is adopted: raised is false and the existing event id is
// returned, so no duplicate event is ever created per tick.
func (m *MaintenanceTx) RaiseAlert(ctx context.Context, o AlertObservation) (eventID string, raised bool, err error) {
	if strings.TrimSpace(o.DedupeKey) == "" {
		return "", false, domain.Internal(errors.New("maintenance alert without dedupe key"))
	}
	id := newID(ids.Event)
	res, err := m.tx.ExecContext(ctx, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message, dedupe_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
		id, o.Kind, o.Severity, o.SubjectType, o.SubjectID, o.Message, o.DedupeKey)
	if err != nil {
		return "", false, dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return id, true, nil
	}
	var existing string
	if err := m.tx.QueryRowContext(ctx, `SELECT id FROM events WHERE dedupe_key = $1 AND state = 'open'
		ORDER BY created_at LIMIT 1`, o.DedupeKey).Scan(&existing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Lost the race with a concurrent close: retry once by reusing the
			// insert path is not needed, the next tick observes it again.
			return "", false, nil
		}
		return "", false, dbErr(err)
	}
	return existing, false, nil
}

// RecoverAlert closes an open alert and records the recovery event in the same
// transaction. It returns "" when the alert was already closed (idempotent).
func (m *MaintenanceTx) RecoverAlert(ctx context.Context, eventID, message string) (recoveryEventID string, err error) {
	var kind, subjectType, subjectID string
	err = m.tx.QueryRowContext(ctx, `UPDATE events SET state = 'recovered', acked_at = now(), acked_by = 'maintenance'
		WHERE id = $1 AND state = 'open'
		RETURNING kind, subject_type, subject_id`, eventID).Scan(&kind, &subjectType, &subjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", dbErr(err)
	}
	recoveryID := newID(ids.Event)
	if _, err := m.tx.ExecContext(ctx, `INSERT INTO events (id, kind, severity, subject_type, subject_id, message, state)
		VALUES ($1,$2,'info',$3,$4,$5,'recovered')`,
		recoveryID, kind, subjectType, subjectID, message); err != nil {
		return "", dbErr(err)
	}
	return recoveryID, nil
}

// ─── notification outbox ───────────────────────────────────────────────────

// Notification is one outbox row. The message text never contains secrets: it
// is generated from alert facts only.
type Notification struct {
	ID            string
	EventID       string
	Kind          string
	SubjectType   string
	SubjectID     string
	Text          string
	State         string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	SentAt        *time.Time
	CreatedAt     time.Time
}

// EnqueueNotification stores one outbox row per event. The unique event_id makes
// the call idempotent across ticks, restarts and multiple panel processes. The
// row is intentionally not foreign keyed to events: a notification must never
// disappear because older events were cleaned up.
func (m *MaintenanceTx) EnqueueNotification(ctx context.Context, eventID, kind, subjectType, subjectID, text string) (bool, error) {
	if strings.TrimSpace(eventID) == "" {
		return false, nil
	}
	res, err := m.tx.ExecContext(ctx, `INSERT INTO notification_outbox
		(id, event_id, channel, kind, subject_type, subject_id, text)
		VALUES ($1,$2,'telegram',$3,$4,$5,$6) ON CONFLICT (event_id) DO NOTHING`,
		newID("ntf"), eventID, kind, subjectType, subjectID, text)
	if err != nil {
		return false, dbErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

const notificationCols = `id, event_id, kind, subject_type, subject_id, text, state, attempts,
	next_attempt_at, last_error, sent_at, created_at`

func scanNotification(row interface{ Scan(...any) error }) (*Notification, error) {
	var n Notification
	var sent sql.NullTime
	if err := row.Scan(&n.ID, &n.EventID, &n.Kind, &n.SubjectType, &n.SubjectID, &n.Text, &n.State, &n.Attempts,
		&n.NextAttemptAt, &n.LastError, &sent, &n.CreatedAt); err != nil {
		return nil, dbErr(err)
	}
	if sent.Valid {
		t := sent.Time.UTC()
		n.SentAt = &t
	}
	n.NextAttemptAt = utc(n.NextAttemptAt)
	n.CreatedAt = utc(n.CreatedAt)
	return &n, nil
}

// ClaimNotifications leases up to limit due rows for delivery and counts the
// attempt. SKIP LOCKED keeps concurrent senders (several panel processes) from
// picking the same row. Attempts are counted at claim time so a crashing sender
// still backs off instead of hot looping.
func (s *Store) ClaimNotifications(ctx context.Context, now time.Time, limit int) ([]Notification, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `
		WITH due AS (
			SELECT id FROM notification_outbox
			WHERE state = 'pending' AND next_attempt_at <= $1
			ORDER BY next_attempt_at, created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notification_outbox o
		SET attempts = o.attempts + 1,
		    next_attempt_at = $1::timestamptz + interval '5 minutes',
		    updated_at = $1::timestamptz
		FROM due WHERE o.id = due.id
		RETURNING o.id, o.event_id, o.kind, o.subject_type, o.subject_id, o.text, o.state, o.attempts,
		          o.next_attempt_at, o.last_error, o.sent_at, o.created_at`, now.UTC(), limit)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]Notification, 0, limit)
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, dbErr(rows.Err())
}

// MarkNotificationSent is only called after a real HTTP 200 with ok=true.
func (s *Store) MarkNotificationSent(ctx context.Context, id string, sentAt time.Time) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE notification_outbox
		SET state = 'sent', sent_at = $2, last_error = '', updated_at = $2
		WHERE id = $1 AND state = 'pending'`, id, sentAt.UTC())
	return dbErr(err)
}

// MarkNotificationFailed records a sanitized error and the next attempt. dead
// marks the row as exhausted; such rows are kept (never deleted) so delivery
// attempts stay auditable.
func (s *Store) MarkNotificationFailed(ctx context.Context, id, sanitizedErr string, nextAttempt time.Time, dead bool) error {
	state := "pending"
	if dead {
		state = "failed"
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE notification_outbox
		SET state = $2, last_error = $3, next_attempt_at = $4, updated_at = now()
		WHERE id = $1`, id, state, truncateMessage(sanitizedErr, 300), nextAttempt.UTC())
	return dbErr(err)
}

// ListNotifications returns outbox rows newest first (operations and tests).
func (s *Store) ListNotifications(ctx context.Context, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+notificationCols+` FROM notification_outbox
		ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]Notification, 0, limit)
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, dbErr(rows.Err())
}

// ─── bounded retention cleanup ─────────────────────────────────────────────

// CleanupStats counts the rows removed by one bounded cleanup.
type CleanupStats struct {
	Previews          int64
	Sessions          int64
	RegistrationCodes int64
	Invitations       int64
	Events            int64
	AuditLogs         int64
	BatchItems        int64
	Batches           int64
}

// Total is the number of removed rows.
func (c CleanupStats) Total() int64 {
	return c.Previews + c.Sessions + c.RegistrationCodes + c.Invitations + c.Events +
		c.AuditLogs + c.BatchItems + c.Batches
}

// cleanupStatement is one bounded delete. Keeping them in a table makes the
// "never delete the ledger" rule testable (see maintenance_test.go).
type cleanupStatement struct {
	Name  string
	Query string
}

// cleanupStatements is the complete cleanup allow list. Ledger and
// deduplication tables (usage_entries, usage_batches, credentials,
// idempotency_records), unfinished work (tasks, notification_outbox) and all
// business records (members, servers, nodes, subscriptions, agents) are
// intentionally absent.
//
// $1 is the "safe" boundary (now - 1 day) and $2 the retention cutoff
// (now - retention_days); $3 is the per statement row bound.
var cleanupStatements = []cleanupStatement{
	{Name: "previews", Query: `DELETE FROM previews WHERE id IN (
		SELECT id FROM previews
		WHERE (consumed_at IS NULL AND expires_at < $1)
		   OR (consumed_at IS NOT NULL AND consumed_at < $2)
		ORDER BY created_at LIMIT $3)`},
	{Name: "sessions", Query: `DELETE FROM sessions WHERE id IN (
		SELECT id FROM sessions
		WHERE expires_at < $1 OR (revoked_at IS NOT NULL AND revoked_at < $1)
		ORDER BY created_at LIMIT $3)`},
	{Name: "registration_codes", Query: `DELETE FROM registration_codes WHERE id IN (
		SELECT id FROM registration_codes
		WHERE (used_at IS NULL AND expires_at < $1)
		   OR (used_at IS NOT NULL AND used_at < $2)
		ORDER BY expires_at LIMIT $3)`},
	{Name: "invitations", Query: `DELETE FROM invitations WHERE id IN (
		SELECT id FROM invitations
		WHERE (used_count = 0 AND revoked_at IS NULL AND expires_at < $1)
		   OR (used_count > 0 AND used_at IS NOT NULL AND used_at < $2)
		   OR (revoked_at IS NOT NULL AND revoked_at < $1)
		ORDER BY created_at LIMIT $3)`},
	{Name: "events", Query: `DELETE FROM events WHERE id IN (
		SELECT e.id FROM events e
		WHERE e.state IN ('recovered', 'acked')
		  AND e.created_at < $2
		  AND NOT EXISTS (SELECT 1 FROM notification_outbox o
		                  WHERE o.event_id = e.id AND o.state <> 'sent')
		ORDER BY e.created_at LIMIT $3)`},
	{Name: "audit_logs", Query: `DELETE FROM audit_logs WHERE id IN (
		SELECT id FROM audit_logs WHERE created_at < $2 ORDER BY created_at LIMIT $3)`},
	{Name: "batch_items", Query: `DELETE FROM batch_items WHERE id IN (
		SELECT bi.id FROM batch_items bi JOIN batches b ON b.id = bi.batch_id
		WHERE b.state = 'completed' AND b.completed_at IS NOT NULL AND b.completed_at < $2
		  AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.batch_id = b.id AND t.state IN ('pending', 'dispatched'))
		ORDER BY bi.created_at LIMIT $3)`},
	{Name: "batches", Query: `DELETE FROM batches WHERE id IN (
		SELECT b.id FROM batches b
		WHERE b.state = 'completed' AND b.completed_at IS NOT NULL AND b.completed_at < $2
		  AND NOT EXISTS (SELECT 1 FROM batch_items bi WHERE bi.batch_id = b.id)
		  AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.batch_id = b.id AND t.state IN ('pending', 'dispatched'))
		ORDER BY b.completed_at LIMIT $3)`},
}

// Cleanup removes aged, safe-to-drop rows in bounded batches. Every statement is
// limited, so a pass can never run an unbounded delete.
func (m *MaintenanceTx) Cleanup(ctx context.Context, retentionDays int, now time.Time) (CleanupStats, error) {
	var stats CleanupStats
	retention := NormalizeRetentionDays(retentionDays)
	now = now.UTC()
	grace := now.Add(-24 * time.Hour)
	cutoff := now.AddDate(0, 0, -retention)
	limit := m.bounds.Cleanup
	if limit <= 0 {
		limit = 100
	}
	for _, stmt := range cleanupStatements {
		// Some whitelist statements use only one cutoff. Type all shared bind
		// parameters explicitly so PostgreSQL can prepare those statements too.
		query := `WITH cleanup_parameters AS (SELECT $1::timestamptz AS grace, $2::timestamptz AS cutoff, $3::integer AS row_limit) ` + stmt.Query
		res, err := m.tx.ExecContext(ctx, query, grace, cutoff, limit)
		if err != nil {
			return stats, fmt.Errorf("cleanup %s: %w", stmt.Name, dbErr(err))
		}
		n, _ := res.RowsAffected()
		switch stmt.Name {
		case "previews":
			stats.Previews = n
		case "sessions":
			stats.Sessions = n
		case "registration_codes":
			stats.RegistrationCodes = n
		case "invitations":
			stats.Invitations = n
		case "events":
			stats.Events = n
		case "audit_logs":
			stats.AuditLogs = n
		case "batch_items":
			stats.BatchItems = n
		case "batches":
			stats.Batches = n
		}
	}
	return stats, nil
}

// ─── run bookkeeping ───────────────────────────────────────────────────────

// MaintenanceRunResult is the persisted summary of one pass.
type MaintenanceRunResult struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Raised     int
	Adopted    int
	Recovered  int
	Enqueued   int
	Sent       int
	Failed     int
	Skipped    int
	Cleanup    CleanupStats
	Error      string
}

// RecordMaintenanceRun stores the summary in maintenance_state (single row).
func (s *Store) RecordMaintenanceRun(ctx context.Context, r MaintenanceRunResult) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE maintenance_state SET
		runs = runs + 1,
		alerts_raised = alerts_raised + $1,
		alerts_adopted = alerts_adopted + $2,
		alerts_recovered = alerts_recovered + $3,
		notifications_enqueued = notifications_enqueued + $4,
		notifications_sent = notifications_sent + $5,
		notifications_failed = notifications_failed + $6,
		rows_cleaned = rows_cleaned + $7,
		last_run_at = $8,
		last_error = $9,
		updated_at = now()
		WHERE id = 1`,
		r.Raised, r.Adopted, r.Recovered, r.Enqueued, r.Sent, r.Failed, r.Cleanup.Total(),
		r.FinishedAt.UTC(), truncateMessage(r.Error, 500))
	return dbErr(err)
}

// ─── small helpers ─────────────────────────────────────────────────────────

// truncateMessage bounds free text that ends up in events, outbox rows and
// logs. It never adds quotes so it cannot be mistaken for JSON.
func truncateMessage(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// shortDuration renders a coarse, log friendly duration (e.g. "3d4h", "12m").
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d >= 24*time.Hour:
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd%dh", days, hours)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
}

// humanBytes renders a byte count for operator messages.
func humanBytes(v int64) string {
	const unit = 1024
	if v < unit {
		return fmt.Sprintf("%dB", v)
	}
	value := float64(v)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	for _, u := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f%s", value, u)
		}
	}
	return fmt.Sprintf("%.1fEiB", value)
}

func quotaText(q *int64) string {
	if q == nil {
		return "unlimited"
	}
	return humanBytes(*q)
}
