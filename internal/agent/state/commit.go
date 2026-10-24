package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"proxypanel/internal/contracts"
)

const (
	// MetaApplyIntent holds the exact authorized target before any core changes.
	MetaApplyIntent = "apply_intent"
	// MetaAppliedSnapshot holds the last completely committed authorization, not
	// a mutable plaintext snapshot file. Pending intent takes precedence on restart.
	MetaAppliedSnapshot = "applied_snapshot"
)

// ErrApplyConflict means the caller must reconcile persisted state before
// touching a core again; blindly retrying an older apply is unsafe.
var ErrApplyConflict = errors.New("apply intent conflicts with persisted state")

func metaTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return raw, err
}

func setMetaTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func decodeSnapshot(raw string) (*contracts.Snapshot, error) {
	if raw == "" {
		return nil, nil
	}
	var snap contracts.Snapshot
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return nil, fmt.Errorf("decode persisted snapshot: %w", err)
	}
	return &snap, nil
}

// SaveApplyIntent durably fences authorization before core changes. A newer
// revision may replace a pending target in the same generation; an equal
// revision must have exactly the same payload. Cross-generation transitions
// require the existing operator acceptance marker (except initial bootstrap).
func (s *Store) SaveApplyIntent(ctx context.Context, snap contracts.Snapshot) error {
	if snap.SchemaVersion != contracts.SchemaVersion || strings.TrimSpace(snap.ControlGeneration) == "" || snap.Revision < 0 {
		return fmt.Errorf("invalid apply target")
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkApplyWatermark(ctx, tx, snap.ControlGeneration, snap.Revision); err != nil {
		return err
	}
	committedRaw, err := metaTx(ctx, tx, MetaAppliedSnapshot)
	if err != nil {
		return err
	}
	committed, err := decodeSnapshot(committedRaw)
	if err != nil {
		return err
	}
	if committed != nil && committed.ControlGeneration == snap.ControlGeneration && committed.Revision == snap.Revision && committedRaw != string(raw) {
		return ErrApplyConflict
	}
	pendingRaw, err := metaTx(ctx, tx, MetaApplyIntent)
	if err != nil {
		return err
	}
	pending, err := decodeSnapshot(pendingRaw)
	if err != nil {
		return err
	}
	if pending != nil {
		if pending.ControlGeneration != snap.ControlGeneration || pending.Revision > snap.Revision || (pending.Revision == snap.Revision && pendingRaw != string(raw)) {
			return ErrApplyConflict
		}
	}
	if err := setMetaTx(ctx, tx, MetaApplyIntent, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func checkApplyWatermark(ctx context.Context, tx *sql.Tx, generation string, revision int64) error {
	current, err := metaTx(ctx, tx, MetaControlGeneration)
	if err != nil {
		return err
	}
	raw, err := metaTx(ctx, tx, MetaAppliedRevision)
	if err != nil {
		return err
	}
	var applied int64
	if raw != "" {
		applied, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid applied revision: %w", err)
		}
	}
	if current == generation {
		if revision < applied {
			return ErrApplyConflict
		}
		return nil
	}
	historyRaw, err := metaTx(ctx, tx, MetaGenerationHistory)
	if err != nil {
		return err
	}
	var history []string
	if historyRaw != "" {
		if err := json.Unmarshal([]byte(historyRaw), &history); err != nil {
			return err
		}
	}
	for _, old := range history {
		if old == generation {
			return ErrApplyConflict
		}
	}
	if current != "" {
		accepted, err := metaTx(ctx, tx, MetaAcceptedGeneration)
		if err != nil {
			return err
		}
		if accepted != generation {
			return ErrApplyConflict
		}
	}
	return nil
}

// ApplyIntent returns a pending target for restart reconciliation. Its presence
// is not evidence of successful application, even if instances were persisted.
func (s *Store) ApplyIntent(ctx context.Context) (*contracts.Snapshot, error) {
	raw, err := s.Meta(ctx, MetaApplyIntent)
	if err != nil {
		return nil, err
	}
	return decodeSnapshot(raw)
}

// AppliedSnapshot returns the last authorization committed with its instances
// and watermark. It must not be used instead of a pending ApplyIntent.
func (s *Store) AppliedSnapshot(ctx context.Context) (*contracts.Snapshot, error) {
	raw, err := s.Meta(ctx, MetaAppliedSnapshot)
	if err != nil {
		return nil, err
	}
	return decodeSnapshot(raw)
}

// CommitSnapshot atomically replaces the complete instance set and queues its
// report. Only a successful apply advances authorization/history/watermark and
// consumes intent. Failed applies persist recovery records but retain intent.
// Like Measure, this uses the Store's single SQLite connection and immediate
// transaction, so sampling cannot interleave with any part of the commit.
func (s *Store) CommitSnapshot(ctx context.Context, records []InstanceRecord, report contracts.ApplyReport, applied bool) error {
	if applied != (report.State == "applied") {
		return fmt.Errorf("inconsistent apply outcome")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	intentRaw, err := metaTx(ctx, tx, MetaApplyIntent)
	if err != nil {
		return err
	}
	intent, err := decodeSnapshot(intentRaw)
	if err != nil {
		return err
	}
	if intent == nil || intent.ControlGeneration != report.ControlGeneration || intent.Revision != report.Revision {
		return ErrApplyConflict
	}
	if err := checkApplyWatermark(ctx, tx, report.ControlGeneration, report.Revision); err != nil {
		return err
	}
	seen := make(map[string]bool, len(records))
	for _, rec := range records {
		if rec.NodeID == "" || seen[rec.NodeID] {
			return fmt.Errorf("empty or duplicate instance node ID")
		}
		seen[rec.NodeID] = true
		if err := recordInstanceTx(ctx, tx, rec); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT node_id FROM instances`)
	if err != nil {
		return err
	}
	var removed []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !seen[id] {
			removed = append(removed, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM instances WHERE node_id=?`, id); err != nil {
			return err
		}
	}
	if applied {
		if err := setMetaTx(ctx, tx, MetaAppliedSnapshot, intentRaw); err != nil {
			return err
		}
		if err := setMetaTx(ctx, tx, MetaControlGeneration, report.ControlGeneration); err != nil {
			return err
		}
		if err := setMetaTx(ctx, tx, MetaAppliedRevision, strconv.FormatInt(report.Revision, 10)); err != nil {
			return err
		}
		historyRaw, err := metaTx(ctx, tx, MetaGenerationHistory)
		if err != nil {
			return err
		}
		var history []string
		if historyRaw != "" {
			if err := json.Unmarshal([]byte(historyRaw), &history); err != nil {
				return err
			}
		}
		found := false
		for _, g := range history {
			found = found || g == report.ControlGeneration
		}
		if !found {
			history = append(history, report.ControlGeneration)
		}
		h, err := json.Marshal(history)
		if err != nil {
			return err
		}
		if err := setMetaTx(ctx, tx, MetaGenerationHistory, string(h)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key IN (?,?,?)`, MetaApplyIntent, MetaAcceptedGeneration, MetaPendingGeneration); err != nil {
			return err
		}
	}
	id := fmt.Sprintf("%s:r%d:%s", report.ControlGeneration, report.Revision, report.State)
	if _, err := tx.ExecContext(ctx, `INSERT INTO reports(id,kind,payload,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, id, ReportKindApplied, raw, report.AppliedAt.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// ProbeWritable performs a durable write rather than a read-only health check.
func (s *Store) ProbeWritable(ctx context.Context) error {
	return s.SetMeta(ctx, map[string]string{"write_probe": "ok"})
}
