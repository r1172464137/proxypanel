// Package state is the agent local transactional store (SQLite): identity,
// applied instance records, usage sampling baselines and the upload queue.
package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"proxypanel/internal/contracts"
)

// Queue capacity limits: reaching them stops the affected instances instead of
// silently dropping unacknowledged usage data.
const (
	MaxQueuedBatches int64 = 2000
	MaxQueuedSamples int64 = 200000

	fileName = "agent.db"
)

// Report kinds stored in the outbox table.
const (
	ReportKindApplied = "applied"
)

// Meta keys.
const (
	MetaAgentID           = "agent_id"
	MetaToken             = "token"
	MetaControlGeneration = "control_generation"
	MetaAppliedRevision   = "applied_revision"
	MetaGenerationHistory = "generation_history"
	MetaSchemaVersion     = "schema_version"
	// MetaAcceptedGeneration stores the control generation an operator
	// explicitly accepted on this node; MetaPendingGeneration stores the
	// generation the panel offers but the agent refuses to trust yet.
	MetaAcceptedGeneration = "accepted_generation"
	MetaPendingGeneration  = "pending_generation"
)

// Store is the agent local database. All writers go through one connection so
// SQLite cannot deadlock against itself.
type Store struct {
	db   *sql.DB
	dir  string
	path string
}

// Open opens (and creates) the agent database inside dir.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("state directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	// The state directory holds the agent token: restrict it explicitly (a
	// protected DACL on Windows, 0700 on Unix) before anything is written.
	if err := securePath(abs); err != nil {
		return nil, err
	}
	path := filepath.Join(abs, fileName)
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open state database: %w", err)
	}
	// The token lives in this database: restrict the file as well (0600 on
	// Unix; on Windows the inherited directory DACL applies).
	if err := secureFile(path); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, dir: abs, path: path}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Path is the database file path.
func (s *Store) Path() string { return s.path }

// Dir is the agent state directory.
func (s *Store) Dir() string { return s.dir }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS instances (
			node_id TEXT PRIMARY KEY,
			config_hash TEXT NOT NULL DEFAULT '',
			core_generation TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			applied_user_count INTEGER NOT NULL DEFAULT 0,
			config BLOB,
			users BLOB,
			next_expiry INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS baseline (
			node_id TEXT NOT NULL,
			core_generation TEXT NOT NULL,
			counter TEXT NOT NULL,
			value INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (node_id, core_generation, counter)
		)`,
		`CREATE TABLE IF NOT EXISTS sample_clock (
			node_id TEXT NOT NULL,
			core_generation TEXT NOT NULL,
			last_sampled_at INTEGER NOT NULL,
			PRIMARY KEY (node_id, core_generation)
		)`,
		`CREATE TABLE IF NOT EXISTS batches (
			batch_id TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL,
			sample_count INTEGER NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'pending'
		)`,
		`CREATE TABLE IF NOT EXISTS samples (
			batch_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			node_id TEXT NOT NULL,
			member_id TEXT NOT NULL,
			core_generation TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			ended_at INTEGER NOT NULL,
			upload INTEGER NOT NULL,
			download INTEGER NOT NULL,
			PRIMARY KEY (batch_id, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_batches_created ON batches(created_at)`,
		`CREATE TABLE IF NOT EXISTS reports (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			payload BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT ''
		)`,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate state: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate state: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		MetaSchemaVersion, "1"); err != nil {
		return fmt.Errorf("migrate state: %w", err)
	}
	return tx.Commit()
}

// Identity is the persisted agent identity and apply watermark.
type Identity struct {
	AgentID           string
	Token             string
	ControlGeneration string
	AppliedRevision   int64
}

// Identity reads the persisted identity.
func (s *Store) Identity(ctx context.Context) (Identity, error) {
	var id Identity
	values, err := s.metaMany(ctx, MetaAgentID, MetaToken, MetaControlGeneration, MetaAppliedRevision)
	if err != nil {
		return id, err
	}
	id.AgentID = values[MetaAgentID]
	id.Token = values[MetaToken]
	id.ControlGeneration = values[MetaControlGeneration]
	if v := values[MetaAppliedRevision]; v != "" {
		rev, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return id, fmt.Errorf("stored applied_revision %q is invalid", v)
		}
		id.AppliedRevision = rev
	}
	return id, nil
}

// SaveIdentity persists the identity (token material stays inside the 0700
// state directory).
func (s *Store) SaveIdentity(ctx context.Context, id Identity) error {
	return s.SetMeta(ctx, map[string]string{
		MetaAgentID:           id.AgentID,
		MetaToken:             id.Token,
		MetaControlGeneration: id.ControlGeneration,
		MetaAppliedRevision:   strconv.FormatInt(id.AppliedRevision, 10),
	})
}

// SetApplied persists the applied control generation and revision.
func (s *Store) SetApplied(ctx context.Context, generation string, revision int64) error {
	return s.SetMeta(ctx, map[string]string{
		MetaControlGeneration: generation,
		MetaAppliedRevision:   strconv.FormatInt(revision, 10),
	})
}

// SetMeta writes several meta keys in one transaction.
func (s *Store) SetMeta(ctx context.Context, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, k := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, values[k]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Meta reads a single meta value ("" when absent).
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

func (s *Store) metaMany(ctx context.Context, keys ...string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		v, err := s.Meta(ctx, k)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// GenerationHistory returns the control generations seen so far (oldest first).
func (s *Store) GenerationHistory(ctx context.Context) ([]string, error) {
	raw, err := s.Meta(ctx, MetaGenerationHistory)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("stored generation history is invalid: %w", err)
	}
	return out, nil
}

// RecordGeneration appends a control generation to the history once.
func (s *Store) RecordGeneration(ctx context.Context, generation string) error {
	history, err := s.GenerationHistory(ctx)
	if err != nil {
		return err
	}
	for _, g := range history {
		if g == generation {
			return nil
		}
	}
	history = append(history, generation)
	raw, err := json.Marshal(history)
	if err != nil {
		return err
	}
	return s.SetMeta(ctx, map[string]string{MetaGenerationHistory: string(raw)})
}

// DeleteMeta removes one meta key.
func (s *Store) DeleteMeta(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, key)
	return err
}

// AcceptedGeneration returns the control generation an operator explicitly
// accepted on this node ("" when none is pending acceptance).
func (s *Store) AcceptedGeneration(ctx context.Context) (string, error) {
	return s.Meta(ctx, MetaAcceptedGeneration)
}

// SetAcceptedGeneration records the explicitly accepted control generation.
func (s *Store) SetAcceptedGeneration(ctx context.Context, generation string) error {
	if strings.TrimSpace(generation) == "" {
		return s.DeleteMeta(ctx, MetaAcceptedGeneration)
	}
	return s.SetMeta(ctx, map[string]string{MetaAcceptedGeneration: generation})
}

// ClearAcceptedGeneration drops the acceptance marker after it was used.
func (s *Store) ClearAcceptedGeneration(ctx context.Context) error {
	return s.DeleteMeta(ctx, MetaAcceptedGeneration)
}

// PendingGeneration returns the control generation the panel offers but the
// agent refuses to apply until an operator accepts it.
func (s *Store) PendingGeneration(ctx context.Context) (string, error) {
	return s.Meta(ctx, MetaPendingGeneration)
}

// SetPendingGeneration records (or clears) the coordination pending generation.
func (s *Store) SetPendingGeneration(ctx context.Context, generation string) error {
	if strings.TrimSpace(generation) == "" {
		return s.DeleteMeta(ctx, MetaPendingGeneration)
	}
	return s.SetMeta(ctx, map[string]string{MetaPendingGeneration: generation})
}

// InstanceRecord is a persisted applied instance.
type InstanceRecord struct {
	NodeID           string
	ConfigHash       string
	CoreGeneration   string
	State            string
	Error            string
	AppliedUserCount int
	Config           []byte
	Users            []contracts.AuthorizedUser
	NextExpiry       time.Time
	UpdatedAt        time.Time
}

// RecordInstance upserts one instance record.
func (s *Store) RecordInstance(ctx context.Context, rec InstanceRecord) error {
	cfg := strings.TrimSpace(string(rec.Config))
	config := []byte(nil)
	if cfg != "" {
		config = []byte(cfg)
	}
	usersRaw, err := json.Marshal(rec.Users)
	if err != nil {
		return err
	}
	next := int64(0)
	if !rec.NextExpiry.IsZero() {
		next = rec.NextExpiry.UTC().UnixMilli()
	}
	updated := rec.UpdatedAt
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO instances(node_id, config_hash, core_generation, state, error, applied_user_count, config, users, next_expiry, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(node_id) DO UPDATE SET
			config_hash=excluded.config_hash,
			core_generation=excluded.core_generation,
			state=excluded.state,
			error=excluded.error,
			applied_user_count=excluded.applied_user_count,
			config=COALESCE(excluded.config, instances.config),
			users=COALESCE(excluded.users, instances.users),
			next_expiry=excluded.next_expiry,
			updated_at=excluded.updated_at`,
		rec.NodeID, rec.ConfigHash, rec.CoreGeneration, rec.State, rec.Error, rec.AppliedUserCount, config, string(usersRaw), next, updated.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("record instance %s: %w", rec.NodeID, err)
	}
	return tx.Commit()
}

// Instances returns all persisted instance records ordered by node id.
func (s *Store) Instances(ctx context.Context) ([]InstanceRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, config_hash, core_generation, state, error, applied_user_count, config, users, next_expiry, updated_at FROM instances ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstanceRecord
	for rows.Next() {
		var (
			rec        InstanceRecord
			config     []byte
			usersRaw   string
			nextMillis int64
			updated    int64
		)
		if err := rows.Scan(&rec.NodeID, &rec.ConfigHash, &rec.CoreGeneration, &rec.State, &rec.Error, &rec.AppliedUserCount, &config, &usersRaw, &nextMillis, &updated); err != nil {
			return nil, err
		}
		rec.Config = config
		if strings.TrimSpace(usersRaw) != "" && usersRaw != "null" {
			if err := json.Unmarshal([]byte(usersRaw), &rec.Users); err != nil {
				return nil, fmt.Errorf("instance %s users: %w", rec.NodeID, err)
			}
		}
		if nextMillis > 0 {
			rec.NextExpiry = time.UnixMilli(nextMillis).UTC()
		}
		rec.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, rec)
	}
	return out, rows.Err()
}

// RemoveInstance deletes one instance record.
func (s *Store) RemoveInstance(ctx context.Context, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM instances WHERE node_id = ?`, nodeID)
	return err
}

// Sample is one resolved counter value for a member.
type CounterItem struct {
	MemberID string
	Counter  string
	Value    int64
}

// MeasureResult reports what the sampling transaction produced.
type MeasureResult struct {
	BatchID    string
	SampleRows int
	StartedAt  time.Time
	EndedAt    time.Time
}

// Measure applies one sampling round: counter baselines and the new upload
// batch are written in a single transaction so a crash can neither double count
// nor lose a delta.
func (s *Store) Measure(ctx context.Context, nodeID, coreGeneration string, endedAt time.Time, items []CounterItem) (MeasureResult, error) {
	res := MeasureResult{EndedAt: endedAt.UTC()}
	if len(items) == 0 {
		return res, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()

	startedAt := res.EndedAt
	var lastMillis int64
	err = tx.QueryRowContext(ctx, `SELECT last_sampled_at FROM sample_clock WHERE node_id = ? AND core_generation = ?`, nodeID, coreGeneration).Scan(&lastMillis)
	switch {
	case err == nil:
		startedAt = time.UnixMilli(lastMillis).UTC()
	case errors.Is(err, sql.ErrNoRows):
	default:
		return res, err
	}
	if !startedAt.Before(res.EndedAt) {
		startedAt = res.EndedAt.Add(-time.Millisecond)
	}
	res.StartedAt = startedAt

	type pair struct{ up, down int64 }
	perMember := map[string]*pair{}
	nowMillis := res.EndedAt.UnixMilli()
	for _, it := range items {
		if strings.TrimSpace(it.Counter) == "" || strings.TrimSpace(it.MemberID) == "" {
			continue
		}
		var base int64
		var found bool
		err := tx.QueryRowContext(ctx, `SELECT value FROM baseline WHERE node_id = ? AND core_generation = ? AND counter = ?`,
			nodeID, coreGeneration, it.Counter).Scan(&base)
		switch {
		case err == nil:
			found = true
		case errors.Is(err, sql.ErrNoRows):
		default:
			return res, err
		}
		delta := it.Value
		if found {
			delta = it.Value - base
			if delta < 0 {
				// The counter went backwards (core restarted inside the same
				// generation); restart from zero instead of emitting a negative
				// delta.
				delta = it.Value
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO baseline(node_id, core_generation, counter, value, updated_at)
			VALUES(?,?,?,?,?)
			ON CONFLICT(node_id, core_generation, counter) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			nodeID, coreGeneration, it.Counter, it.Value, nowMillis); err != nil {
			return res, err
		}
		if delta <= 0 {
			continue
		}
		switch {
		case strings.HasSuffix(it.Counter, ">>>uplink"):
			p := perMember[it.MemberID]
			if p == nil {
				p = &pair{}
				perMember[it.MemberID] = p
			}
			p.up += delta
		case strings.HasSuffix(it.Counter, ">>>downlink"):
			p := perMember[it.MemberID]
			if p == nil {
				p = &pair{}
				perMember[it.MemberID] = p
			}
			p.down += delta
		}
	}
	if len(perMember) > 0 {
		batchID, err := newBatchID()
		if err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO batches(batch_id, created_at, sample_count, state) VALUES(?,?,?, 'pending')`,
			batchID, nowMillis, len(perMember)); err != nil {
			return res, err
		}
		members := make([]string, 0, len(perMember))
		for m := range perMember {
			members = append(members, m)
		}
		sort.Strings(members)
		seq := 0
		for _, m := range members {
			p := perMember[m]
			if _, err := tx.ExecContext(ctx, `INSERT INTO samples(batch_id, seq, node_id, member_id, core_generation, started_at, ended_at, upload, download)
				VALUES(?,?,?,?,?,?,?,?,?)`,
				batchID, seq, nodeID, m, coreGeneration, startedAt.UnixMilli(), res.EndedAt.UnixMilli(), p.up, p.down); err != nil {
				return res, err
			}
			seq++
		}
		res.BatchID = batchID
		res.SampleRows = seq
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sample_clock(node_id, core_generation, last_sampled_at) VALUES(?,?,?)
		ON CONFLICT(node_id, core_generation) DO UPDATE SET last_sampled_at=excluded.last_sampled_at`,
		nodeID, coreGeneration, nowMillis); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return MeasureResult{}, err
	}
	return res, nil
}

// PendingBatch is one unacknowledged upload batch.
type PendingBatch struct {
	BatchID   string
	CreatedAt time.Time
	Attempts  int
	LastError string
	Samples   []contracts.UsageSample
}

// PendingBatches returns the oldest pending batches.
func (s *Store) PendingBatches(ctx context.Context, limit int) ([]PendingBatch, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT batch_id, created_at, attempts, last_error FROM batches WHERE state = 'pending' ORDER BY created_at, batch_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingBatch
	for rows.Next() {
		var (
			b         PendingBatch
			createdMs int64
		)
		if err := rows.Scan(&b.BatchID, &createdMs, &b.Attempts, &b.LastError); err != nil {
			return nil, err
		}
		b.CreatedAt = time.UnixMilli(createdMs).UTC()
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		samples, err := s.batchSamples(ctx, out[i].BatchID)
		if err != nil {
			return nil, err
		}
		out[i].Samples = samples
	}
	return out, nil
}

func (s *Store) batchSamples(ctx context.Context, batchID string) ([]contracts.UsageSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, member_id, core_generation, started_at, ended_at, upload, download FROM samples WHERE batch_id = ? ORDER BY seq`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.UsageSample
	for rows.Next() {
		var (
			smp       contracts.UsageSample
			startedMs int64
			endedMs   int64
		)
		if err := rows.Scan(&smp.NodeID, &smp.MemberID, &smp.CoreGeneration, &startedMs, &endedMs, &smp.UploadBytes, &smp.DownloadBytes); err != nil {
			return nil, err
		}
		smp.StartedAt = time.UnixMilli(startedMs).UTC()
		smp.EndedAt = time.UnixMilli(endedMs).UTC()
		out = append(out, smp)
	}
	return out, rows.Err()
}

// MarkBatchAttempt records a failed delivery attempt.
func (s *Store) MarkBatchAttempt(ctx context.Context, batchID, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE batches SET attempts = attempts + 1, last_error = ? WHERE batch_id = ?`, truncate(errMsg, 400), batchID)
	return err
}

// DeleteBatch removes an acknowledged batch and its samples.
func (s *Store) DeleteBatch(ctx context.Context, batchID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM samples WHERE batch_id = ?`, batchID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM batches WHERE batch_id = ?`, batchID); err != nil {
		return err
	}
	return tx.Commit()
}

// QueueDepth is the current upload backlog.
type QueueDepth struct {
	Batches       int64
	Samples       int64
	OldestCreated time.Time
}

// OverCapacity reports whether the backlog exceeds the configured limits.
func (d QueueDepth) OverCapacity() bool {
	return d.Batches > MaxQueuedBatches || d.Samples > MaxQueuedSamples
}

// QueueDepth computes the current backlog.
func (s *Store) QueueDepth(ctx context.Context) (QueueDepth, error) {
	var depth QueueDepth
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM batches WHERE state = 'pending'`).Scan(&depth.Batches); err != nil {
		return depth, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM samples`).Scan(&depth.Samples); err != nil {
		return depth, err
	}
	var oldest int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(created_at), 0) FROM batches WHERE state = 'pending'`).Scan(&oldest); err != nil {
		return depth, err
	}
	if oldest > 0 {
		depth.OldestCreated = time.UnixMilli(oldest).UTC()
	}
	return depth, nil
}

// PendingReport is a persisted report that was not acknowledged yet.
type PendingReport struct {
	ID        string
	Kind      string
	Payload   []byte
	Attempts  int
	CreatedAt time.Time
}

// SaveReport stores (or refreshes) an outbox entry for an apply report.
func (s *Store) SaveReport(ctx context.Context, id, kind string, payload []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO reports(id, kind, payload, created_at) VALUES(?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, kind=excluded.kind`,
		id, kind, payload, time.Now().UTC().UnixMilli())
	return err
}

// PendingReports lists queued reports oldest first.
func (s *Store) PendingReports(ctx context.Context, limit int) ([]PendingReport, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, payload, attempts, created_at FROM reports ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingReport
	for rows.Next() {
		var (
			r         PendingReport
			createdMs int64
		)
		if err := rows.Scan(&r.ID, &r.Kind, &r.Payload, &r.Attempts, &createdMs); err != nil {
			return nil, err
		}
		r.CreatedAt = time.UnixMilli(createdMs).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteReport removes an acknowledged report.
func (s *Store) DeleteReport(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM reports WHERE id = ?`, id)
	return err
}

// MarkReportAttempt records a failed delivery attempt.
func (s *Store) MarkReportAttempt(ctx context.Context, id, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE reports SET attempts = attempts + 1, last_error = ? WHERE id = ?`, truncate(errMsg, 400), id)
	return err
}

func newBatchID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "b_" + hex.EncodeToString(buf[:]), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
