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
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
	r := &UpgradeReadiness{Status: "ready", Phase: "post-migrate", Checks: []ReadinessCheck{}, Applied: []string{}, Pending: []string{}}
	if preflight {
		r.Phase = "preflight"
	}
	evidence, err := LoadUpgradeEvidence(s.upgradeEvidenceFile)
	if err != nil {
		r.add("EVIDENCE_INVALID", "blocked", 1, "Correct the local evidence manifest and referenced files; rerun preflight.")
	}
	r.evidence = evidence
	if evidence != nil {
		r.EvidenceSHA256 = evidence.ManifestSHA256
	}
	data, fingerprint, err := readUpgradeSnapshot(ctx, q)
	if err != nil {
		return nil, err
	}
	r.DatabaseSHA256 = fingerprint
	if err = s.checkUpgradeMigrations(data, r); err != nil {
		return nil, err
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
		// The only exporter supported by this baseline is schema 1, which
		// has no intent journal. A manifest boolean cannot upgrade that fact.
		if x.StateSchemaVersion != "1" {
			r.add("AGENT_SCHEMA_UNSUPPORTED", "unknown", 1, "Use a reviewed exporter for the actual agent state schema; do not relabel state.")
		}
		if x.StateSchemaVersion == "1" && x.ApplyIntentTrackingSupported {
			r.add("AGENT_INTENT_CLAIM_INVALID", "blocked", 1, "Schema 1 cannot track apply intents; obtain truthful execution evidence.")
		}
		if !x.ApplyIntentTrackingSupported || x.StateSchemaVersion == "1" {
			r.add("AGENT_INTENT_UNPROVEN", "unknown", 1, "This state schema cannot prove execution completion; obtain explicit supported execution evidence. Zero is not proof.")
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
	if evidence != nil {
		for _, a := range evidence.AgentExports {
			if _, ok := agentSet[a.AgentID]; !ok {
				r.add("AGENT_ID_UNKNOWN", "blocked", 1, "Correct the exported agent identity.")
			}
		}
	}
	// SQL checks report actual affected counts, not fabricated zero statistics.
	for _, c := range []struct{ code, query string }{
		{"TASK_ORPHAN", `SELECT count(*) FROM tasks t WHERE t.batch_id<>'' AND NOT EXISTS(SELECT 1 FROM batches b WHERE b.id=t.batch_id)`},
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
	return nil
}
