package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/usagecalc"
)

type ReadinessCheck struct {
	Code   string `json:"code"`
	Status string `json:"status"`
	Count  int    `json:"count"`
	Remedy string `json:"remedy"`
}
type UpgradeReadiness struct {
	Status           string           `json:"status"`
	CheckedAt        time.Time        `json:"checked_at"`
	CandidateVersion string           `json:"candidate_version"`
	StopVerification string           `json:"stop_verification,omitempty"`
	Phase            string           `json:"phase"`
	Checks           []ReadinessCheck `json:"checks"`
	DatabaseSHA256   string           `json:"database_sha256,omitempty"`
	EvidenceSHA256   string           `json:"evidence_sha256,omitempty"`
	Applied          []string         `json:"applied_migrations"`
	Pending          []string         `json:"pending_migrations"`
	PendingBatches   int              `json:"pending_batches"`
	DuplicateBatches int              `json:"duplicate_batches"`
	periods          []PeriodAssertion
	owners           []OwnerAssertion
	evidence         *UpgradeEvidence
	checksumOK       *bool
}

// ReadinessSchema uses null for an unavailable/unverifiable checksum result.
// In particular, an inspection error is not evidence of checksum success.
type ReadinessSchema struct {
	AppliedMigrations []string `json:"applied_migrations"`
	PendingMigrations []string `json:"pending_migrations"`
	ChecksumOK        *bool    `json:"checksum_ok"`
}

// ReadinessCheckDTO is a projection, not a second set of checks. Keep the
// original Go fields and legacy JSON for deployment/helper consumers.
type ReadinessCheckDTO struct {
	ReadinessCheck
	ID            string   `json:"id"`
	Severity      string   `json:"severity"`
	State         string   `json:"state"`
	Summary       string   `json:"summary"`
	AffectedCount int      `json:"affected_count"`
	Evidence      []string `json:"evidence"`
}

// CandidateBuildVersion is shared by HTTP and CLI. There is no separate Panel
// release-version variable today; Go records the module version, or (devel).
func CandidateBuildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "devel"
}

func newUpgradeReadiness(at time.Time, preflight bool) *UpgradeReadiness {
	r := &UpgradeReadiness{Status: "ready", CheckedAt: at.UTC(), CandidateVersion: CandidateBuildVersion(), Phase: "post-migrate", Checks: []ReadinessCheck{}, Applied: []string{}, Pending: []string{}}
	if preflight {
		r.Phase = "preflight"
	}
	return r
}

// UnknownUpgradeReadiness deliberately excludes raw driver/filesystem errors,
// which may contain credentials or private material paths.
func UnknownUpgradeReadiness(preflight bool) *UpgradeReadiness {
	r := newUpgradeReadiness(time.Now(), preflight)
	r.add("INSPECTION_UNAVAILABLE", "unknown", 1, "Verify database access, schema and configured migration files, then rerun the read-only check.")
	return r
}

func readinessCheckID(code string) string {
	switch code {
	case "MEMBER_PERIOD_UNKNOWN", "MEMBER_PERIOD_HISTORY_MISSING", "MEMBER_TIMEZONE_INVALID", "PERIOD_PROOF_INVALID":
		return "member_cycle_unproven"
	case "LEDGER_PERIOD_UNPROVEN":
		return "ledger_rows_without_period"
	case "PERIOD_OVERLAP":
		return "member_period_overlap"
	case "BATCH_CLOCK_INVALID":
		return "usage_batch_clock_skew"
	case "TASK_ORPHAN", "ITEM_TASK_ORPHAN":
		return "batch_task_orphan"
	case "AGENT_EVIDENCE_MISSING", "AGENT_SCHEMA_UNSUPPORTED":
		return "agent_state_unreadable"
	case "PENDING_SAMPLE_UNPROVEN":
		return "node_unattributable_pending"
	default:
		return strings.ToLower(code)
	}
}

func (r *UpgradeReadiness) blockerCodes() []string {
	codes := []string{}
	seen := map[string]bool{}
	for _, c := range r.Checks {
		if c.Status != "blocked" && c.Status != "unknown" {
			continue
		}
		id := readinessCheckID(c.Code)
		if !seen[id] {
			codes = append(codes, id)
			seen[id] = true
		}
	}
	return codes
}

// MarshalJSON adds the public contract from exactly the internal result used by
// migration gates. No pass/zero rows, queue facts or external observations are
// inferred. Pending migrations keep their existing phase-dependent semantics.
func (r UpgradeReadiness) MarshalJSON() ([]byte, error) {
	type legacy UpgradeReadiness
	copy := legacy(r)
	copy.Applied = append([]string{}, r.Applied...)
	copy.Pending = append([]string{}, r.Pending...)
	checks := make([]ReadinessCheckDTO, 0, len(r.Checks))
	for _, c := range r.Checks {
		state := "unknown"
		if c.Status == "blocked" {
			state = "fail"
		} else if c.Status == "ready" {
			state = "pass"
		}
		evidence := []string{fmt.Sprintf("check=%s; status=%s; affected_count=%d", c.Code, c.Status, c.Count)}
		if r.DatabaseSHA256 != "" {
			evidence = append(evidence, "database_sha256="+r.DatabaseSHA256)
		}
		checks = append(checks, ReadinessCheckDTO{ReadinessCheck: c, ID: readinessCheckID(c.Code), Severity: "blocker", State: state, Summary: c.Remedy, AffectedCount: c.Count, Evidence: evidence})
	}
	return json.Marshal(struct {
		*legacy
		Schema   ReadinessSchema     `json:"schema"`
		Checks   []ReadinessCheckDTO `json:"checks"`
		Blockers []string            `json:"blockers"`
	}{&copy, ReadinessSchema{copy.Applied, copy.Pending, r.checksumOK}, checks, r.blockerCodes()})
}

// Only existing unsafe-upgrade/startup refusal branches call this. Diagnostic
// GETs still return 200; entering recovery is deliberately not gated here.
func readinessBlocked(reason string, r *UpgradeReadiness, extra ...string) error {
	codes := r.blockerCodes()
	codes = append(codes, extra...)
	return domain.Conflict(domain.CodeReadinessBlocked, "%s", reason).WithDetail("blockers", codes)
}

func (r *UpgradeReadiness) add(code, status string, count int, remedy string) {
	if count == 0 {
		return
	}
	r.Checks = append(r.Checks, ReadinessCheck{code, status, count, remedy})
	if status == "blocked" || (status == "unknown" && r.Status != "blocked") {
		r.Status = status
	}
}

// CheckUpgradeReadiness uses one read-only MVCC snapshot, never decrypts data,
// and consumes only the locally configured evidence path (not HTTP input).
func (s *Store) CheckUpgradeReadiness(ctx context.Context, preflight bool) (*UpgradeReadiness, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := s.checkUpgradeReadinessQ(ctx, tx, preflight)
	if err != nil {
		return nil, err
	}
	return r, tx.Commit()
}

// migrationReadinessReceipt is stack-local to one migration run. It is created
// only after at least one file committed, never persisted or retained by Store.
// The baseline applies only to stop-review binding; all other checks use current
// facts, whose full digest must equal the last file's expected post-state.
type migrationReadinessReceipt struct {
	baseline string
	expected string
	evidence string
}

// Lock the control row before other tables, matching business-writer lock
// order. Table locks also freeze writers that do not use the control gate.
func lockUpgradeReadinessTx(ctx context.Context, tx *sql.Tx) error {
	var control bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('control_state') IS NOT NULL`).Scan(&control); err != nil {
		return err
	}
	if control {
		if _, err := tx.ExecContext(ctx, `SELECT id FROM control_state WHERE id=1 FOR UPDATE`); err != nil {
			return err
		}
	}
	return lockUpgradeTables(ctx, tx)
}

// The caller still owns the migration advisory lock. READ COMMITTED is
// intentional: after waiting for fact-freezing locks, read the latest committed
// state, not a snapshot taken before a concurrent writer finished.
func (s *Store) checkPostMigrationReadiness(ctx context.Context, conn *sql.Conn, receipt *migrationReadinessReceipt) error {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("post-migration readiness unavailable: %w", err)
	}
	defer tx.Rollback()
	if err := lockUpgradeReadinessTx(ctx, tx); err != nil {
		return fmt.Errorf("post-migration readiness unavailable: %w", err)
	}
	baseline := ""
	if receipt != nil {
		baseline = receipt.baseline
	}
	r, err := s.checkUpgradeReadinessWithBaselineQ(ctx, tx, false, baseline)
	if err != nil {
		return fmt.Errorf("post-migration readiness unavailable: %w", err)
	}
	if receipt != nil && (r.DatabaseSHA256 != receipt.expected || r.EvidenceSHA256 != receipt.evidence) {
		return readinessBlocked("post-migration upgrade evidence or database changed; rerun preflight", r, "upgrade_proof_changed")
	}
	if r.Status != "ready" {
		return readinessBlocked(fmt.Sprintf("post-migration readiness %s: %v", r.Status, r.Checks), r)
	}
	if len(r.Pending) != 0 {
		return readinessBlocked(fmt.Sprintf("post-migration readiness incomplete: %v", r.Pending), r, "pending_migration")
	}
	return tx.Commit()
}

type upgradeSnapshot map[string][]json.RawMessage

func readUpgradeSnapshot(ctx context.Context, q querier) (upgradeSnapshot, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename`)
	if err != nil {
		return nil, "", err
	}
	var names []string
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return nil, "", err
		}
		names = append(names, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	data := upgradeSnapshot{}
	h := sha256.New()
	// Include structure as well as contents: an empty altered table is still a
	// different database. Catalog object OIDs are deliberately not exported.
	structure, err := q.QueryContext(ctx, `SELECT c.relname, a.attname, format_type(a.atttypid,a.atttypmod), a.attnotnull FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid WHERE n.nspname=current_schema() AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped ORDER BY c.relname,a.attnum`)
	if err != nil {
		return nil, "", err
	}
	for structure.Next() {
		var table, column, kind string
		var required bool
		if err = structure.Scan(&table, &column, &kind, &required); err != nil {
			structure.Close()
			return nil, "", err
		}
		fmt.Fprintf(h, "column:%q:%q:%q:%t\n", table, column, kind, required)
	}
	err = structure.Err()
	structure.Close()
	if err != nil {
		return nil, "", err
	}
	for _, name := range names {
		// JSON is hashed in memory only, never returned or logged. Includes control
		// generation/revision, migration prefix, credentials and immutable ledger.
		rows, err = q.QueryContext(ctx, `SELECT to_jsonb(t)::text FROM `+pgx.Identifier{name}.Sanitize()+` t ORDER BY to_jsonb(t)::text`)
		if err != nil {
			return nil, "", err
		}
		fmt.Fprintf(h, "%d:%s\n", len(name), name)
		data[name] = []json.RawMessage{}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, "", err
			}
			fmt.Fprintf(h, "%d:%s\n", len(raw), raw)
			data[name] = append(data[name], json.RawMessage(raw))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, "", err
		}
	}
	return data, hex.EncodeToString(h.Sum(nil)), nil
}
func decodeUpgradeRows[T any](data upgradeSnapshot, name string) ([]T, error) {
	out := []T{}
	for _, raw := range data[name] {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

type upgradeMember struct {
	ID             string
	SubscriptionID *string    `json:"subscription_id"`
	Start          *time.Time `json:"period_start"`
	End            *time.Time `json:"period_end"`
	Timezone       string     `json:"period_timezone"`
	Anchor         int        `json:"period_anchor_day"`
}
type upgradeAgent struct {
	ID         string
	ServerID   string `json:"server_id"`
	Version    string
	Generation int64 `json:"control_generation"`
	Applied    int64 `json:"applied_revision"`
	Desired    int64 `json:"desired_revision"`
}
type upgradeNode struct {
	ID       string
	ServerID string `json:"server_id"`
}
type upgradeEntry struct {
	MemberID    string    `json:"member_id"`
	Start       time.Time `json:"period_start"`
	End         time.Time `json:"period_end"`
	SampleStart time.Time `json:"sample_started_at"`
	SampleEnd   time.Time `json:"sample_ended_at"`
}

func (s *Store) checkUpgradeReadinessQ(ctx context.Context, q querier, preflight bool) (*UpgradeReadiness, error) {
	return s.checkUpgradeReadinessWithBaselineQ(ctx, q, preflight, "")
}

// Only the controlled migration run supplies a baseline, after validating its
// initial ready proof. A stop review binds that original snapshot, not its own
// DDL/imports. Every file AND Open's final verification compare the CURRENT
// snapshot with the exact expected digest and require unchanged evidence.
func (s *Store) checkUpgradeReadinessWithBaselineQ(ctx context.Context, q querier, preflight bool, baseline string) (*UpgradeReadiness, error) {
	r := newUpgradeReadiness(s.now(), preflight)
	data, fingerprint, err := readUpgradeSnapshot(ctx, q)
	if err != nil {
		return nil, err
	}
	r.DatabaseSHA256 = fingerprint
	if err = s.checkUpgradeMigrations(data, r); err != nil {
		return nil, err
	}
	// External stopped-Agent evidence is an upgrade-transition proof, not a
	// permanent licence required for every later restart. It is demanded when a
	// pending migration could actually touch usage accounting — those are the
	// only migrations where un-uploaded Agent batches could be lost or
	// mis-attributed. See pendingTouchesAccounting for why the default is
	// "demand it".
	//
	// A non-empty baseline means we are inside a controlled migration run. That
	// run fixes the decision once, before its first DDL, and reuses it for every
	// in-run recheck: otherwise the first applied file turns the pending set into
	// a different question and a schema-only run would demand evidence halfway
	// through, which no operator can satisfy.
	requireExternalEvidence := s.pendingTouchesAccounting(r.Pending)
	if baseline != "" {
		requireExternalEvidence = s.upgradeEvidenceRequired
	}
	var evidence *UpgradeEvidence
	if requireExternalEvidence {
		evidence, err = LoadUpgradeEvidence(s.upgradeEvidenceFile)
		if err != nil {
			r.add("EVIDENCE_INVALID", "blocked", 1, "Correct the local evidence manifest and referenced files; rerun preflight.")
		}
		r.evidence = evidence
		if evidence != nil {
			r.EvidenceSHA256 = evidence.ManifestSHA256
		}
	}
	if len(data) == 0 {
		return r, nil
	}
	if _, ok := data["schema_migrations"]; !ok {
		r.add("MIGRATION_REGISTRY_MISSING", "unknown", 1, "Existing tables without a recorded migration prefix cannot be upgraded automatically.")
		return r, nil
	}
	for _, table := range []string{"members", "agents", "nodes", "credentials", "usage_entries", "usage_batches", "batches", "batch_items", "tasks", "control_state"} {
		if _, ok := data[table]; !ok {
			r.add("SCHEMA_INCOMPLETE", "unknown", 1, "Restore a complete known migration prefix before checking.")
			return r, nil
		}
	}
	members, err := decodeUpgradeRows[upgradeMember](data, "members")
	if err != nil {
		return nil, err
	}
	agents, err := decodeUpgradeRows[upgradeAgent](data, "agents")
	if err != nil {
		return nil, err
	}
	nodes, err := decodeUpgradeRows[upgradeNode](data, "nodes")
	if err != nil {
		return nil, err
	}
	entries, err := decodeUpgradeRows[upgradeEntry](data, "usage_entries")
	if err != nil {
		return nil, err
	}
	// A schema-current Open skips only the external transition material. Every
	// database-internal checksum and semantic check below still runs.
	historical, err := decodeUpgradeRows[struct {
		MemberID string    `json:"member_id"`
		Start    time.Time `json:"period_start"`
		End      time.Time `json:"period_end"`
		Timezone string
		Anchor   int `json:"anchor_day"`
	}](data, "member_periods")
	if err != nil {
		return nil, err
	}
	for _, p := range historical {
		r.periods = append(r.periods, PeriodAssertion{MemberID: p.MemberID, Start: p.Start, End: p.End, Timezone: p.Timezone, AnchorDay: p.Anchor})
	}
	memberSet := map[string]bool{}
	for _, m := range members {
		memberSet[m.ID] = true
	}
	if evidence != nil {
		for _, p := range evidence.PeriodAssertions {
			if !memberSet[p.MemberID] {
				r.add("PERIOD_MEMBER_UNKNOWN", "blocked", 1, "Correct the asserted member identity.")
				continue
			}
			r.periods = append(r.periods, p)
		}
	}
	// A valid persisted current interval is direct evidence; never reconstruct it
	// from today's subscription rules or rewrite any old ledger keys.
	for _, m := range members {
		if m.Start == nil || m.End == nil || !m.Start.Before(*m.End) || m.Anchor < 1 || m.Anchor > 31 || m.Timezone == "" {
			if m.SubscriptionID != nil || m.Start != nil || m.End != nil {
				r.add("MEMBER_PERIOD_UNKNOWN", "blocked", 1, "Explicitly rebind the member to a proven period using the existing administration flow.")
			}
			continue
		}
		if _, err := time.LoadLocation(m.Timezone); err != nil {
			r.add("MEMBER_TIMEZONE_INVALID", "blocked", 1, "Correct the member period timezone explicitly.")
			continue
		}
		found := false
		for _, p := range r.periods {
			if p.MemberID == m.ID && p.Start.Equal(*m.Start) && p.End.Equal(*m.End) && p.AnchorDay == m.Anchor && p.Timezone == m.Timezone {
				found = true
			}
		}
		if !found {
			if _, migrated := data["member_periods"]; migrated {
				r.add("MEMBER_PERIOD_HISTORY_MISSING", "blocked", 1, "The migrated database is missing the persisted current period; reconcile through the audited member binding flow.")
			} else {
				r.periods = append(r.periods, PeriodAssertion{MemberID: m.ID, Start: *m.Start, End: *m.End, Timezone: m.Timezone, AnchorDay: m.Anchor})
			}
		}
	}
	// Deduplicate exact assertions, reject any conflicting or overlapping interval.
	unique := []PeriodAssertion{}
	for _, p := range r.periods {
		same := false
		for _, old := range unique {
			if old.MemberID != p.MemberID {
				continue
			}
			if old.Start.Equal(p.Start) && old.End.Equal(p.End) && old.AnchorDay == p.AnchorDay && old.Timezone == p.Timezone {
				same = true
				break
			}
			if old.Start.Before(p.End) && p.Start.Before(old.End) {
				r.add("PERIOD_OVERLAP", "blocked", 1, "Correct conflicting period proofs; do not change the ledger.")
			}
		}
		if !same {
			unique = append(unique, p)
		}
	}
	r.periods = unique
	for _, p := range r.periods {
		if err := validatePeriodRecord(PeriodRecord{MemberID: p.MemberID, PeriodStart: p.Start, PeriodEnd: p.End, AnchorDay: p.AnchorDay, Timezone: p.Timezone, Source: "proven_ledger"}); err != nil {
			r.add("PERIOD_PROOF_INVALID", "blocked", 1, "Correct explicit timezone, anchor and PostgreSQL-precision period boundaries.")
		}
	}
	for _, e := range entries {
		covered := false
		for _, p := range r.periods {
			if p.MemberID == e.MemberID && p.Start.Equal(e.Start) && p.End.Equal(e.End) {
				covered = true
			}
		}
		if !covered || !e.Start.Before(e.End) || !e.SampleStart.Before(e.SampleEnd) {
			r.add("LEDGER_PERIOD_UNPROVEN", "blocked", 1, "Supply explicit historical period evidence; never rewrite ledger bytes or period keys.")
		}
	}
	nodeSet := map[string]string{}
	for _, n := range nodes {
		nodeSet[n.ID] = n.ServerID
	}
	agentSet := map[string]upgradeAgent{}
	for _, a := range agents {
		agentSet[a.ID] = a
	}
	if evidence != nil {
		for _, o := range evidence.OwnerAssertions {
			a, ok := agentSet[o.AgentID]
			_, nodeOK := nodeSet[o.NodeID]
			if !ok || !nodeOK || a.ServerID != o.ServerID || o.ValidTo != nil && o.ReleasedRevision == nil {
				r.add("OWNER_IDENTITY_INVALID", "blocked", 1, "Prove the node, server, agent identity and release revision explicitly.")
				continue
			}
			r.owners = append(r.owners, o)
		}
	}
	for i, o := range r.owners {
		for _, p := range r.owners[:i] {
			if o.NodeID == p.NodeID && (p.ValidTo == nil || o.ValidFrom.Before(*p.ValidTo)) && (o.ValidTo == nil || p.ValidFrom.Before(*o.ValidTo)) {
				r.add("OWNER_OVERLAP", "blocked", 1, "Resolve overlapping execution windows explicitly.")
			}
		}
	}
	stopProof := *r
	if baseline != "" {
		stopProof.DatabaseSHA256 = baseline
	}
	if requireExternalEvidence {
		for _, a := range agents {
			var input *AgentEvidenceInput
			if evidence != nil {
				for i := range evidence.AgentExports {
					if evidence.AgentExports[i].AgentID == a.ID {
						if input != nil {
							r.add("AGENT_EVIDENCE_DUPLICATE", "blocked", 1, "Supply one consistent export per agent.")
						}
						input = &evidence.AgentExports[i]
					}
				}
			}
			if input == nil {
				r.add("AGENT_EVIDENCE_MISSING", "unknown", 1, "Drain with the old uploader or provide a consistent stopped-state export and external stop evidence.")
				continue
			}
			x := input.Export
			if a.Version == "" || input.ObservedAgentVersion != a.Version || x.ControlGeneration != fmt.Sprint(a.Generation) || x.AppliedRevision != a.Applied {
				r.add("AGENT_STATE_MISMATCH", "blocked", 1, "Reconcile observed agent version, generation and revision against PostgreSQL.")
			}
			// Artifact v1/schema1 can contain D's durable apply_intent and
			// applied_snapshot keys. Capability is not implied by schema version.
			if x.StateSchemaVersion != "1" {
				r.add("AGENT_SCHEMA_UNSUPPORTED", "unknown", 1, "Use a reviewed exporter for the actual agent state schema; do not relabel state.")
			}
			if input.StopReview == nil {
				if !x.ApplyIntentTrackingSupported {
					r.add("AGENT_INTENT_UNPROVEN", "unknown", 1, "Legacy journal is unavailable; provide administrator-reviewed per-instance execution reconciliation instead. Do not manufacture intent records.")
				}
				r.add("AGENT_STOP_UNVERIFIED", "unknown", 1, "Provide a structured administrator-reviewed stop record with restart fence, process identity/termination observations, ordered capture and complete instance/window reconciliation. Hashes alone do not prove stop.")
			} else if err := verifyAdministratorStop(a, input, data, &stopProof); err != nil {
				r.add("AGENT_STOP_REVIEW_INVALID", "blocked", 1, "Administrator stop review failed: "+err.Error()+". Correct external materials; do not migrate.")
			} else {
				r.StopVerification = "管理员核对 / administrator-reviewed external observations; not independently observed by panel"
			}
			if x.PendingReportCount != 0 || x.PendingApplyIntentCount != 0 {
				r.add("AGENT_EXECUTION_PENDING", "blocked", 1, "Complete and acknowledge execution/reporting with the old service before stopping it.")
			}
			seen := map[string]bool{}
			for _, b := range x.PendingBatches {
				r.PendingBatches++
				if seen[b.BatchID] {
					r.add("LOCAL_BATCH_DUPLICATE", "blocked", 1, "Obtain a consistent export without repeated local batch IDs.")
				}
				seen[b.BatchID] = true
				var hash string
				err := q.QueryRowContext(ctx, `SELECT payload_sha256 FROM usage_batches WHERE agent_id=$1 AND batch_id=$2`, a.ID, b.BatchID).Scan(&hash)
				if err == nil {
					if hash != usagecalc.PayloadSHA256(b) {
						r.add("BATCH_HASH_MISMATCH", "blocked", 1, "Investigate same-ID/different-payload corruption without editing queued bytes.")
					} else {
						r.DuplicateBatches++
					}
					continue
				}
				if err != sql.ErrNoRows {
					return nil, err
				}
				if len(b.Samples) == 0 {
					r.add("BATCH_EMPTY", "blocked", 1, "Investigate invalid queued batch without dropping it.")
				}
				for _, sample := range b.Samples {
					owner := false
					for _, o := range r.owners {
						if o.NodeID == sample.NodeID && o.AgentID == a.ID && !sample.StartedAt.Before(o.ValidFrom) && (o.ValidTo == nil || !sample.EndedAt.After(*o.ValidTo)) {
							owner = true
						}
					}
					period := false
					for _, p := range r.periods {
						if p.MemberID == sample.MemberID && !sample.StartedAt.Before(p.Start) && !sample.EndedAt.After(p.End) {
							period = true
						}
					}
					var credential bool
					err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM credentials WHERE member_id=$1 AND node_id=$2 AND created_at <= $3 AND (revoked_at IS NULL OR revoked_at >= $4))`, sample.MemberID, sample.NodeID, sample.StartedAt, sample.EndedAt).Scan(&credential)
					if err != nil {
						return nil, err
					}
					if !owner || !period || !credential || !sample.StartedAt.Before(sample.EndedAt) || sample.UploadBytes < 0 || sample.DownloadBytes < 0 {
						r.add("PENDING_SAMPLE_UNPROVEN", "blocked", 1, "Supply complete execution/period/credential provenance for every original sample, or drain using the old uploader.")
					}
				}
			}
		}
	}
	if evidence != nil {
		for _, a := range evidence.AgentExports {
			if _, ok := agentSet[a.AgentID]; !ok {
				r.add("AGENT_ID_UNKNOWN", "blocked", 1, "Correct the exported agent identity.")
			}
		}
	}
	// A retained permanent submission receipt proves a task's original batch
	// even after bounded cleanup has removed the presentation/history rows.
	// Legacy schemas still require the actual batch; do not relax their gate.
	taskOrphanQuery := `SELECT count(*) FROM tasks t WHERE t.batch_id<>'' AND NOT EXISTS(SELECT 1 FROM batches b WHERE b.id=t.batch_id)`
	if _, ok := data["batch_idempotency"]; ok {
		taskOrphanQuery += ` AND NOT EXISTS(SELECT 1 FROM batch_idempotency d WHERE d.batch_id=t.batch_id)`
	}
	// SQL checks report actual affected counts, not fabricated zero statistics.
	for _, c := range []struct{ code, query string }{
		{"TASK_ORPHAN", taskOrphanQuery},
		{"ITEM_TASK_ORPHAN", `SELECT count(*) FROM batch_items i WHERE i.task_id<>'' AND NOT EXISTS(SELECT 1 FROM tasks t WHERE t.id=i.task_id)`},
		{"BATCH_CLOCK_INVALID", `SELECT count(*) FROM usage_batches WHERE clock_skew OR ended_at<started_at`},
	} {
		var count int
		if err = q.QueryRowContext(ctx, c.query).Scan(&count); err != nil {
			return nil, err
		}
		r.add(c.code, "blocked", count, "Investigate and reconcile existing records explicitly before upgrading.")
	}
	return r, nil
}
func (s *Store) checkUpgradeMigrations(data upgradeSnapshot, r *UpgradeReadiness) error {
	files, err := os.ReadDir(s.migrationsDir)
	if err != nil {
		return fmt.Errorf("read migration directory: %w", err)
	}
	applied, err := decodeUpgradeRows[struct{ Version, Checksum string }](data, "schema_migrations")
	if err != nil {
		return err
	}
	known := map[string]string{}
	for _, a := range applied {
		known[a.Version] = a.Checksum
		r.Applied = append(r.Applied, a.Version)
	}
	sort.Strings(r.Applied)
	missing := false
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(s.migrationsDir, f.Name()))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		if hash, ok := known[f.Name()]; ok {
			if hash != hex.EncodeToString(sum[:]) {
				r.add("MIGRATION_CHECKSUM_MISMATCH", "blocked", 1, "Restore the original migration file; never alter applied migration checksums.")
			}
			if missing {
				r.add("MIGRATION_PREFIX_GAP", "blocked", 1, "Restore a complete applied migration prefix.")
			}
			delete(known, f.Name())
		} else {
			missing = true
			r.Pending = append(r.Pending, f.Name())
		}
	}
	r.add("MIGRATION_UNKNOWN", "blocked", len(known), "Use the matching migration set for this database.")
	// Only publish success after all recorded files were actually checked.
	// A nonempty unregistered schema or unknown recorded migration is unproven.
	checksumOK := true
	for _, c := range r.Checks {
		if c.Code == "MIGRATION_CHECKSUM_MISMATCH" {
			checksumOK = false
		}
	}
	if !checksumOK || len(known) == 0 && (len(data) == 0 || data["schema_migrations"] != nil) {
		r.checksumOK = &checksumOK
	}
	return nil
}
