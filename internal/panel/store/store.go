// Package store is the PostgreSQL persistence layer. PostgreSQL is the
// authority for business state, authorization, tasks and the usage ledger;
// Redis is only used for caching and rate limiting (see internal/panel/cache).
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/secrets"
)

// Store is the panel persistence handle.
type Store struct {
	DB  *sql.DB
	Key *secrets.Key
	Now func() time.Time

	readOnly bool
}

// Options configures the pool.
type Options struct {
	DatabaseURL    string
	MasterKey      *secrets.Key
	MigrationsDir  string
	MaxConns       int
	SearchPath     string
	ConnectTimeout time.Duration
}

// ErrReadOnly marks operations forbidden on the inspection connection.
var ErrReadOnly = errors.New("store is read-only")

// Open connects, verifies the database and applies pending migrations.
func Open(ctx context.Context, opts Options) (*Store, error) {
	s, err := openDatabase(ctx, opts, false)
	if err != nil {
		return nil, err
	}
	dir := opts.MigrationsDir
	if dir == "" {
		dir = os.Getenv("PANEL_MIGRATIONS_DIR")
	}
	if dir == "" {
		dir = "migrations"
	}
	if err := s.Migrate(ctx, dir); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly inspects an existing or empty database without migrations or a
// master key. It is not a sandbox for callers that can issue arbitrary SQL.
func OpenReadOnly(ctx context.Context, opts Options) (*Store, error) {
	return openDatabase(ctx, opts, true)
}

func openDatabase(ctx context.Context, opts Options, readOnly bool) (*Store, error) {
	if strings.TrimSpace(opts.DatabaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	if !readOnly && opts.MasterKey == nil {
		return nil, errors.New("PANEL_MASTER_KEY is required")
	}
	connConfig, err := pgx.ParseConfig(opts.DatabaseURL)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL configuration")
	}
	if opts.ConnectTimeout > 0 {
		connConfig.ConnectTimeout = opts.ConnectTimeout
	}
	if opts.SearchPath != "" || readOnly {
		if connConfig.RuntimeParams == nil {
			connConfig.RuntimeParams = map[string]string{}
		}
		if opts.SearchPath != "" {
			connConfig.RuntimeParams["search_path"] = opts.SearchPath
		}
		if readOnly {
			connConfig.RuntimeParams["default_transaction_read_only"] = "on"
		}
	}
	db := stdlib.OpenDB(*connConfig)
	if opts.MaxConns > 0 {
		db.SetMaxOpenConns(opts.MaxConns)
		db.SetMaxIdleConns(opts.MaxConns)
	} else {
		db.SetMaxOpenConns(16)
	}
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres unreachable: %w", err)
	}
	s := &Store{DB: db, Now: func() time.Time { return time.Now().UTC() }, readOnly: readOnly}
	if !readOnly {
		s.Key = opts.MasterKey
	}
	return s, nil
}

// Close releases the pool.
func (s *Store) Close() { _ = s.DB.Close() }

// Ping verifies PostgreSQL availability (used by /readyz).
func (s *Store) Ping(ctx context.Context) error {
	if err := s.DB.PingContext(ctx); err != nil {
		return dbErr(err)
	}
	return nil
}

// now returns the store clock (overridable in tests).
func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Migrate applies migrations in lexical order under a PostgreSQL advisory lock.
// The lock makes concurrent panel starts safe; a checksum mismatch aborts
// instead of silently re-applying a changed migration.
func (s *Store) Migrate(ctx context.Context, dir string) error {
	if s.readOnly {
		return ErrReadOnly
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	if len(files) == 0 {
		return fmt.Errorf("no migrations found in %q", dir)
	}

	// A dedicated connection keeps the advisory lock for the whole run.
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	const lockKey int64 = 7401941
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey)
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY,
		checksum text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		err = conn.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = $1", name).Scan(&existing)
		switch {
		case err == nil:
			if existing != checksum {
				return fmt.Errorf("migration %s was modified after being applied (checksum mismatch)", name)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)", name, checksum); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ─── helpers ──────────────────────────────────────────────────────────────

// querier is satisfied by *sql.DB, *sql.Tx and *sql.Conn.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn in a transaction, rolling back on error or panic.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if s.readOnly {
		return ErrReadOnly
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return dbErr(err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return dbErr(err)
	}
	return nil
}

// dbErr maps driver errors onto wire errors without leaking SQL text.
func dbErr(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505": // unique_violation
			return domain.Conflict(domain.CodeConflict, "a record with the same unique value already exists")
		case "23503": // foreign_key_violation
			return domain.Validation("referenced object does not exist")
		case "23514": // check_violation
			return domain.Validation("value violates a constraint")
		case "40001", "40P01": // serialization failure / deadlock
			return domain.E(domain.CodeConflict, http.StatusConflict, "concurrent update, please retry").WithRetryable(true)
		case "57014": // statement timeout
			return domain.E(domain.CodeDBUnavailable, http.StatusServiceUnavailable, "database timeout").WithRetryable(true)
		}
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return domain.E(domain.CodeDBUnavailable, http.StatusServiceUnavailable, "database timeout").WithRetryable(true)
	}
	msg := err.Error()
	if strings.Contains(msg, "connection") || strings.Contains(msg, "conn closed") || strings.Contains(msg, "bad connection") {
		return domain.E(domain.CodeDBUnavailable, http.StatusServiceUnavailable, "database unavailable").WithRetryable(true)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotFound("object not found")
	}
	return err
}

func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UTC()
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// encodeObject marshals an Object for a jsonb column.
func encodeObject(o domain.Object) []byte {
	if o == nil {
		o = domain.NewObject()
	}
	raw, err := o.MarshalJSON()
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// scanObject decodes a jsonb column, tolerating NULL.
func scanObject(raw []byte) domain.Object {
	o := domain.NewObject()
	if len(raw) == 0 {
		return o
	}
	_ = o.UnmarshalJSON(raw)
	return o
}

func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

// encryptSecret stores a secret with the master key.
func (s *Store) encryptSecret(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	if s.readOnly {
		return nil, ErrReadOnly
	}
	if s.Key == nil {
		return nil, errors.New("PANEL_MASTER_KEY is required")
	}
	return s.Key.EncryptString(plain)
}

// decryptSecret returns "" when the ciphertext is empty.
func (s *Store) decryptSecret(ct []byte) (string, error) {
	if len(ct) == 0 {
		return "", nil
	}
	if s.readOnly {
		return "", ErrReadOnly
	}
	if s.Key == nil {
		return "", errors.New("PANEL_MASTER_KEY is required")
	}
	return s.Key.DecryptString(ct)
}

// newID generates an opaque identifier with one of the ids.* prefixes.
func newID(prefix string) string { return ids.New(prefix) }

// itoa64 renders a base 10 integer (used for revisions on the wire).
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }
