package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/usagecalc"
)

// Uses only an unpredictable owned schema; never changes the shared database
// or another test's schema. No hardcoded legacy tunnel/DB-name assumptions.
func upgradeTestStore(t *testing.T, old bool) *Store {
	t.Helper()
	dsn := os.Getenv("PANEL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PANEL_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect isolated PostgreSQL:", err)
	}
	schema := "pp_upgrade_" + regRandomHex(t, 12)
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		if err != nil {
			t.Error(err)
		}
		admin.Close(context.Background())
	})
	key, err := secrets.ParseKey(strings.Repeat("upgrade-test-master-key-", 2))
	if err != nil {
		t.Fatal(err)
	}
	st, err := openDatabase(ctx, Options{DatabaseURL: dsn, MasterKey: key, SearchPath: schema, MigrationsDir: regMigrationsDir(t)}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if old {
		dir := t.TempDir()
		for _, name := range []string{"0001_init.sql", "0002_batch_preview.sql", "0003_maintenance.sql", "0004_node_external_port.sql"} {
			body, err := os.ReadFile(filepath.Join(regMigrationsDir(t), name))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err = st.Migrate(ctx, dir); err != nil {
			t.Fatal(err)
		}
		st.migrationsDir = regMigrationsDir(t)
	}
	return st
}
func upgradeExec(t *testing.T, st *Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func upgradeReady(t *testing.T, st *Store) *UpgradeReadiness {
	t.Helper()
	r, err := st.CheckUpgradeReadiness(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func upgradeHas(r *UpgradeReadiness, code string) bool {
	for _, c := range r.Checks {
		if c.Code == code {
			return true
		}
	}
	return false
}
func upgradeSeedMember(t *testing.T, st *Store) {
	upgradeExec(t, st, `INSERT INTO members(id,username,password_hash,period_start,period_end,period_timezone,period_anchor_day) VALUES ('m','member','not-a-secret','2025-01-01Z','2025-02-01Z','UTC',1)`)
}
func upgradeEvidence(t *testing.T, st *Store, batches []contracts.UsageBatch, supported bool, counts ...int64) {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0400); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	stop := write("stop.json", map[string]string{"operator": "maintenance stopped; reference external service record"})
	var intents, reports int64
	if len(counts) > 0 {
		intents = counts[0]
	}
	if len(counts) > 1 {
		reports = counts[1]
	}
	exported := write("agent.json", UpgradeAgentStateInput{PendingApplyIntentCount: intents, PendingReportCount: reports, FormatVersion: upgradeAgentEvidenceFormat, CapturedAt: time.Now().UTC(), DatabaseSHA256: strings.Repeat("a", 64), StateSchemaVersion: "1", AgentID: "a", ControlGeneration: "1", AppliedRevision: 0, PendingBatches: batches, ApplyIntentTrackingSupported: supported})
	manifest := UpgradeEvidence{FormatVersion: UpgradeEvidenceFormat, AgentExports: []AgentEvidenceInput{{AgentID: "a", File: "agent.json", SHA256: exported, ObservedAgentVersion: "legacy", StoppedEvidenceFile: "stop.json", StoppedEvidenceSHA256: stop}}}
	write("manifest.json", manifest)
	st.upgradeEvidenceFile = filepath.Join(dir, "manifest.json")
}
func upgradeSeedAgent(t *testing.T, st *Store) {
	upgradeExec(t, st, `INSERT INTO servers(id,name) VALUES ('s','server')`)
	upgradeExec(t, st, `INSERT INTO agents(id,server_id,version,token_hash) VALUES ('a','s','legacy','hash')`)
}

func TestUpgradeReadinessEmptyReadOnlyAndMigrate(t *testing.T) {
	st := upgradeTestStore(t, false)
	r := upgradeReady(t, st)
	if r.Status != "ready" || len(r.Pending) != 5 {
		t.Fatalf("%+v", r)
	}
	var count int
	if err := st.DB.QueryRow(`SELECT count(*) FROM pg_tables WHERE schemaname=current_schema()`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preflight wrote tables: %d %v", count, err)
	}
	var schema string
	if err := st.DB.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(context.Background(), Options{DatabaseURL: os.Getenv("PANEL_TEST_DATABASE_URL"), SearchPath: schema, MigrationsDir: regMigrationsDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if ro.Key != nil {
		t.Fatal("read-only store has master key")
	}
	if _, err = ro.DB.Exec(`CREATE TABLE forbidden(id int)`); err == nil {
		t.Fatal("read-only database allowed DDL")
	}
	if err = ro.Migrate(context.Background(), regMigrationsDir(t)); err != ErrReadOnly {
		t.Fatalf("%v", err)
	}
	if err = st.Migrate(context.Background(), regMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	r = upgradeReady(t, st)
	if r.Status != "ready" || len(r.Applied) != 5 {
		t.Fatalf("%+v", r)
	}
}
func TestUpgradeReadinessLegacyAndUnknown(t *testing.T) {
	st := upgradeTestStore(t, true)
	upgradeSeedMember(t, st)
	if r := upgradeReady(t, st); r.Status != "ready" {
		t.Fatalf("%+v", r)
	}
	if err := st.Migrate(context.Background(), regMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := st.DB.QueryRow(`SELECT count(*) FROM member_periods WHERE source='proven_current'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("period import %d %v", count, err)
	}
	upgradeSeedAgent(t, st)
	r := upgradeReady(t, st)
	if r.Status != "unknown" || !upgradeHas(r, "AGENT_EVIDENCE_MISSING") {
		t.Fatalf("%+v", r)
	}
}
func TestUpgradeReadinessACKLostAndHashMismatch(t *testing.T) {
	st := upgradeTestStore(t, true)
	upgradeSeedAgent(t, st)
	batch := contracts.UsageBatch{SchemaVersion: "usage/v1", BatchID: "accepted-before-ack", Samples: []contracts.UsageSample{{NodeID: "historical-node", MemberID: "historical-member", StartedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), EndedAt: time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC), UploadBytes: 7}}}
	upgradeExec(t, st, `INSERT INTO usage_batches(agent_id,batch_id,payload_sha256) VALUES ('a',$1,$2)`, batch.BatchID, usagecalc.PayloadSHA256(batch))
	upgradeEvidence(t, st, []contracts.UsageBatch{batch}, false)
	r := upgradeReady(t, st)
	if r.PendingBatches != 1 || r.DuplicateBatches != 1 || r.Status != "unknown" || !upgradeHas(r, "AGENT_INTENT_UNPROVEN") || upgradeHas(r, "PENDING_SAMPLE_UNPROVEN") {
		t.Fatalf("ACK lost must retain pending without recreating ownership: %+v", r)
	}
	if err := st.Migrate(context.Background(), regMigrationsDir(t)); err == nil {
		t.Fatal("unknown allowed migration")
	}
	var exists bool
	st.DB.QueryRow(`SELECT to_regclass('member_periods') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("blocked/unknown created 0005 tables")
	}
	upgradeExec(t, st, `UPDATE usage_batches SET payload_sha256='different'`)
	r = upgradeReady(t, st)
	if r.Status != "blocked" || !upgradeHas(r, "BATCH_HASH_MISMATCH") {
		t.Fatalf("%+v", r)
	}
}
func TestUpgradeReadinessSchemaOneCannotClaimIntents(t *testing.T) {
	st := upgradeTestStore(t, true)
	upgradeSeedAgent(t, st)
	upgradeEvidence(t, st, nil, true)
	r := upgradeReady(t, st)
	// D's atomic state implementation genuinely supports intent tracking in
	// schema1. Accept that capability, but never infer stop from count zero.
	if r.Status != "unknown" || !upgradeHas(r, "AGENT_STOP_UNVERIFIED") || upgradeHas(r, "AGENT_INTENT_CLAIM_INVALID") || upgradeHas(r, "AGENT_INTENT_UNPROVEN") {
		t.Fatalf("%+v", r)
	}
}
func TestUpgradeReadinessDurableExecutionPending(t *testing.T) {
	for _, counts := range [][2]int64{{1, 0}, {0, 1}} {
		st := upgradeTestStore(t, true)
		upgradeSeedAgent(t, st)
		upgradeEvidence(t, st, nil, true, counts[0], counts[1])
		r := upgradeReady(t, st)
		if r.Status != "blocked" || !upgradeHas(r, "AGENT_EXECUTION_PENDING") || upgradeHas(r, "AGENT_INTENT_CLAIM_INVALID") {
			t.Fatalf("%+v", r)
		}
		if err := st.Migrate(context.Background(), regMigrationsDir(t)); err == nil {
			t.Fatal("pending execution permitted DDL")
		}
		var exists bool
		if err := st.DB.QueryRow(`SELECT to_regclass('member_periods') IS NOT NULL`).Scan(&exists); err != nil || exists {
			t.Fatalf("pending proof created tables: %v %v", exists, err)
		}
	}
}

func TestUpgradeReadinessImportRollback(t *testing.T) {
	st := upgradeTestStore(t, true)
	upgradeSeedMember(t, st)
	upgradeExec(t, st, `CREATE FUNCTION reject_upgrade_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$`)
	upgradeExec(t, st, `CREATE TRIGGER reject_upgrade_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_upgrade_audit()`)
	err := st.Migrate(context.Background(), regMigrationsDir(t))
	if err == nil || !strings.Contains(err.Error(), "injected audit failure") {
		t.Fatalf("expected transactional import failure: %v", err)
	}
	var exists bool
	if err = st.DB.QueryRow(`SELECT to_regclass('member_periods') IS NOT NULL OR to_regclass('node_server_history') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("failed file DDL not rolled back: %v %v", exists, err)
	}
	var count int
	st.DB.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count)
	if count != 4 {
		t.Fatalf("earlier migration prefix lost or failed file registered: %d", count)
	}
	upgradeExec(t, st, `DROP TRIGGER reject_upgrade_audit ON audit_logs`)
	if err = st.Migrate(context.Background(), regMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
}
func TestUpgradeReadinessDatabaseFingerprintChange(t *testing.T) {
	st := upgradeTestStore(t, true)
	upgradeSeedMember(t, st)
	hold, err := st.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	if _, err = hold.Exec(`LOCK TABLE members IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- st.Migrate(ctx, regMigrationsDir(t)) }()
	deadline := time.Now().Add(10 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err = st.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='members'::regclass AND mode='ShareRowExclusiveLock' AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("migrator did not reach fact-freezing lock")
	}
	if _, err = hold.Exec(`UPDATE members SET period_anchor_day=2 WHERE id='m'`); err != nil {
		t.Fatal(err)
	}
	if err = hold.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil || !strings.Contains(err.Error(), "database changed") {
		t.Fatalf("fingerprint change did not stop migration: %v", err)
	}
	var exists bool
	st.DB.QueryRow(`SELECT to_regclass('member_periods') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("fingerprint failure left DDL")
	}
}
