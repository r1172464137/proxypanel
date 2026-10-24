package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"proxypanel/internal/contracts"
)

func inspectionDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("state input must be a nonempty regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func rejectRecoveryLogs(path string) error {
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != 0 {
			return fmt.Errorf("state backup has a recovery log; obtain a complete checkpointed backup first")
		}
	}
	return nil
}

// ReadUpgradeEvidence inspects a consistent stopped-state backup, without
// migration, chmod, checkpoint or secret output. File consistency alone does
// not establish that an operator stopped the original live Agent.
func ReadUpgradeEvidence(ctx context.Context, dir string) (*contracts.AgentStateEvidence, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	if resolved != abs {
		return nil, fmt.Errorf("inspection directory must not traverse symlinks")
	}
	path := filepath.Join(abs, fileName)
	if err := rejectRecoveryLogs(path); err != nil {
		return nil, err
	}
	before, err := inspectionDigest(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	query.Set("_pragma", "query_only(1)")
	uri.RawQuery = query.Encode()
	// Open would create/migrate the input and set WAL/permissions.
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("open inspection input: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return nil, fmt.Errorf("state backup integrity check failed")
	}
	if err := inspectKnownLayout(ctx, db); err != nil {
		return nil, err
	}
	local := &Store{db: db, dir: abs, path: path}
	values, err := local.metaMany(ctx, MetaAgentID, MetaSchemaVersion, MetaControlGeneration, MetaAppliedRevision)
	if err != nil {
		return nil, fmt.Errorf("read state metadata: %w", err)
	}
	if values[MetaSchemaVersion] != "1" {
		return nil, fmt.Errorf("unsupported state schema version")
	}
	if values[MetaAgentID] == "" {
		return nil, fmt.Errorf("state backup has no registered Agent identity")
	}
	revision, err := strconv.ParseInt(values[MetaAppliedRevision], 10, 64)
	if err != nil || revision < 0 {
		return nil, fmt.Errorf("state backup has an invalid applied revision")
	}
	out := &contracts.AgentStateEvidence{
		FormatVersion: contracts.AgentEvidenceFormat, CapturedAt: time.Now().UTC(),
		DatabaseSHA256: before, StateSchemaVersion: values[MetaSchemaVersion],
		AgentID: values[MetaAgentID], ControlGeneration: values[MetaControlGeneration],
		AppliedRevision: revision, PendingBatches: []contracts.UsageBatch{},
	}
	var batches, samples, otherStates int64
	if err := db.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(CASE WHEN state <> 'pending' THEN 1 ELSE 0 END),0) FROM batches`).Scan(&batches, &otherStates); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM samples`).Scan(&samples); err != nil {
		return nil, err
	}
	if otherStates != 0 || batches > MaxQueuedBatches*4 || samples > MaxQueuedSamples*4 {
		return nil, fmt.Errorf("unsupported queue state or export safety limit exceeded; input is unchanged")
	}
	rows, err := db.QueryContext(ctx, `SELECT batch_id, sample_count FROM batches ORDER BY created_at, batch_id`)
	if err != nil {
		return nil, err
	}
	type batchMeta struct {
		id    string
		count int
	}
	var pending []batchMeta
	for rows.Next() {
		var item batchMeta
		if err := rows.Scan(&item.id, &item.count); err != nil {
			rows.Close()
			return nil, err
		}
		pending = append(pending, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var exported int64
	for _, item := range pending {
		list, err := local.batchSamples(ctx, item.id)
		if err != nil {
			return nil, err
		}
		if item.count <= 0 || len(list) != item.count {
			return nil, fmt.Errorf("batch sample count is inconsistent; input is unchanged")
		}
		out.PendingBatches = append(out.PendingBatches, contracts.UsageBatch{
			SchemaVersion: contracts.SchemaVersion, BatchID: item.id, Samples: list,
		})
		exported += int64(len(list))
	}
	if exported != samples {
		return nil, fmt.Errorf("state backup contains orphaned samples; refusing an incomplete export")
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM reports`).Scan(&out.PendingReportCount); err != nil {
		return nil, err
	}
	if err := rejectRecoveryLogs(path); err != nil {
		return nil, err
	}
	after, err := inspectionDigest(path)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, fmt.Errorf("state input changed during inspection")
	}
	return out, nil
}

func inspectKnownLayout(ctx context.Context, db *sql.DB) error {
	tables := map[string]bool{"meta": false, "instances": false, "baseline": false, "sample_clock": false,
		"batches": false, "samples": false, "reports": false}
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT GLOB 'sqlite_*'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if _, ok := tables[name]; !ok {
			rows.Close()
			return fmt.Errorf("unsupported state layout; unknown tables cannot be assumed empty")
		}
		tables[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, found := range tables {
		if !found {
			return fmt.Errorf("unsupported state layout; required table is missing")
		}
	}
	columns := map[string][]string{
		"meta":         {"key", "value"},
		"instances":    {"node_id", "config_hash", "core_generation", "state", "error", "applied_user_count", "config", "users", "next_expiry", "updated_at"},
		"baseline":     {"node_id", "core_generation", "counter", "value", "updated_at"},
		"sample_clock": {"node_id", "core_generation", "last_sampled_at"},
		"batches":      {"batch_id", "created_at", "sample_count", "attempts", "last_error", "state"},
		"samples":      {"batch_id", "seq", "node_id", "member_id", "core_generation", "started_at", "ended_at", "upload", "download"},
		"reports":      {"id", "kind", "payload", "created_at", "attempts", "last_error"},
	}
	for table, expected := range columns {
		rows, err := db.QueryContext(ctx, `PRAGMA table_info("`+table+`")`)
		if err != nil {
			return err
		}
		actual := map[string]bool{}
		for rows.Next() {
			var cid, notNull, primary int
			var name, kind string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
				rows.Close()
				return err
			}
			actual[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(actual) != len(expected) {
			return fmt.Errorf("unsupported state columns; unknown data cannot be assumed absent")
		}
		for _, name := range expected {
			if !actual[name] {
				return fmt.Errorf("unsupported state columns; required data is missing")
			}
		}
	}
	knownKeys := map[string]bool{MetaAgentID: true, MetaToken: true, MetaControlGeneration: true,
		MetaAppliedRevision: true, MetaGenerationHistory: true, MetaSchemaVersion: true,
		MetaAcceptedGeneration: true, MetaPendingGeneration: true}
	rows, err = db.QueryContext(ctx, `SELECT key FROM meta`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		if !knownKeys[key] {
			return fmt.Errorf("unsupported state metadata; unknown intent cannot be assumed absent")
		}
	}
	return rows.Err()
}
