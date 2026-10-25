package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
)

func upgradePrefixDir(t *testing.T, count int) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(regMigrationsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if count == 0 {
			break
		}
		body, err := os.ReadFile(filepath.Join(regMigrationsDir(t), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		upgradeWrite(t, filepath.Join(dir, entry.Name()), body)
		count--
	}
	if count != 0 {
		t.Fatal("migration prefix unavailable")
	}
	return dir
}

func upgradeOpenOptions(t *testing.T, st *Store, dir string) Options {
	t.Helper()
	var schema string
	if err := st.DB.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return Options{DatabaseURL: os.Getenv("PANEL_TEST_DATABASE_URL"), MasterKey: st.Key, SearchPath: schema, MigrationsDir: dir, UpgradeEvidenceFile: st.upgradeEvidenceFile}
}

// Exercise the final verifier's boundary directly, splitting the already
// controlled migration and final check without adding a production hook.
func TestUpgradeOpenFinalProofRecheck(t *testing.T) {
	for _, change := range []string{"unchanged", "business-write", "materials", "expired-fence"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			st := upgradeTestStore(t, true)
			m, review, dir := stopFixture(t, st)
			if change == "expired-fence" {
				review.Fence.ValidUntil = time.Now().UTC().Add(2 * time.Second)
				saveStopFixture(t, m, review, dir)
			}
			initial := upgradeReady(t, st)
			if initial.Status != "ready" {
				t.Fatalf("initial proof: %+v", initial)
			}
			if err := st.Migrate(ctx, regMigrationsDir(t)); err != nil {
				t.Fatal(err)
			}
			_, expected, err := readUpgradeSnapshot(ctx, st.DB)
			if err != nil {
				t.Fatal(err)
			}
			receipt := &migrationReadinessReceipt{baseline: initial.DatabaseSHA256, expected: expected, evidence: initial.EvidenceSHA256}
			conn, err := st.DB.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// Match the final verifier's production advisory-lock ownership.
			if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(7401941)`); err != nil {
				t.Fatal(err)
			}
			defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7401941)`)
			hold, err := st.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Rollback()
			if _, err = hold.ExecContext(ctx, `LOCK TABLE members IN SHARE MODE`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- st.checkPostMigrationReadiness(ctx, conn, receipt) }()
			waiting := false
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				if err = st.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='members'::regclass AND mode='ShareRowExclusiveLock' AND NOT granted)`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("final verifier never reached fact-freezing lock")
			}
			switch change {
			case "business-write":
				if _, err = hold.ExecContext(ctx, `UPDATE members SET username='changed-after-last-file' WHERE id='m'`); err != nil {
					t.Fatal(err)
				}
			case "materials":
				review.Administrator = "another legitimate test administrator"
				saveStopFixture(t, m, review, dir)
			case "expired-fence":
				time.Sleep(time.Until(review.Fence.ValidUntil) + 10*time.Millisecond)
			}
			if err = hold.Commit(); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if change == "unchanged" {
				if err != nil {
					t.Fatalf("unchanged final proof: %v", err)
				}
				t.Log("unchanged full DB/evidence digest and administrator semantics accepted")
			} else {
				want := "upgrade evidence or database changed"
				if change == "expired-fence" {
					want = "AGENT_STOP_REVIEW_INVALID"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("want %q, got %v", want, err)
				}
				var de *domain.Error
				if !errors.As(err, &de) || de.Code != domain.CodeReadinessBlocked || de.Status != 409 {
					t.Fatalf("final refusal must preserve typed READINESS_BLOCKED: %v", err)
				}
				codes, ok := de.Details["blockers"].([]string)
				if !ok || len(codes) == 0 {
					t.Fatalf("missing real final blockers: %+v", de)
				}
				t.Logf("final boundary correctly refused %s: %v", change, err)
			}
		})
	}
}

func TestUpgradeOpenPostMigrationSemantics(t *testing.T) {
	st := upgradeTestStore(t, true)
	upgradeSeedMember(t, st)
	dir := upgradePrefixDir(t, 6)
	// Test-only migration: expected digest includes this write, but it must not
	// make the current semantic failure acceptable to the final check.
	upgradeWrite(t, filepath.Join(dir, "0007_test_invalid_period.sql"), []byte(`UPDATE members SET period_anchor_day=0 WHERE id='m';`))
	opened, err := Open(context.Background(), upgradeOpenOptions(t, st, dir))
	if opened != nil {
		opened.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "post-migration readiness blocked") || !strings.Contains(err.Error(), "MEMBER_PERIOD_UNKNOWN") {
		t.Fatalf("post-migration semantics must still block: %v", err)
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeReadinessBlocked {
		t.Fatalf("Open lost typed semantic refusal: %v", err)
	}
	codes, ok := de.Details["blockers"].([]string)
	if !ok || len(codes) != 1 || codes[0] != "member_cycle_unproven" {
		t.Fatalf("Open semantic blockers: %+v", de.Details)
	}
	t.Logf("matching expected digest does not bypass current semantics: %v", err)
}

func TestUpgradeOpenControlledMigration(t *testing.T) {
	for _, prefix := range []int{5, 2} {
		t.Run(itoaStop(prefix), func(t *testing.T) {
			ctx := context.Background()
			st := upgradeTestStore(t, false)
			dir := upgradePrefixDir(t, prefix)
			if err := st.Migrate(ctx, dir); err != nil {
				t.Fatal(err)
			}
			stopFixture(t, st)
			if r := upgradeReady(t, st); r.Status != "ready" {
				t.Fatalf("initial proof: %+v", r)
			}
			opts := upgradeOpenOptions(t, st, dir)
			control, err := Open(ctx, opts)
			if err != nil {
				t.Fatalf("no-pending positive control: %v", err)
			}
			control.Close()
			opts.MigrationsDir = regMigrationsDir(t)
			opened, err := Open(ctx, opts)
			var versions int
			if qerr := st.DB.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&versions); qerr != nil {
				t.Fatal(qerr)
			}
			if err != nil {
				t.Fatalf("controlled Open failed after registry=%d: %v", versions, err)
			}
			opened.Close()
			// 迁移总数随目录变化，不能写死：否则每加一个迁移都要改测试，掩盖真正的回归。
			if want := countShippedMigrations(t); versions != want {
				t.Fatalf("registry=%d, want %d (all shipped migrations applied)", versions, want)
			}
			t.Logf("prefix=%d: no-pending ready; controlled Open committed every pending migration and passed final verification", prefix)
			again, err := Open(ctx, opts)
			if err != nil {
				t.Fatalf("schema-current later Open must not reuse or require the expired transition proof: %v", err)
			}
			again.Close()
			t.Log("later schema-current Open reran internal readiness without treating old transition evidence as a permanent licence")
		})
	}
}

func TestUpgradeOpenSchemaCurrentAgentWithoutTransitionEvidence(t *testing.T) {
	ctx := context.Background()
	st := upgradeTestStore(t, false)
	if err := st.Migrate(ctx, regMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	upgradeSeedAgent(t, st)
	st.upgradeEvidenceFile = filepath.Join(t.TempDir(), "stale-or-missing-transition-proof.json")
	opts := upgradeOpenOptions(t, st, regMigrationsDir(t))
	opened, err := Open(ctx, opts)
	if err != nil {
		t.Fatalf("schema-current startup with a registered Agent must remain available: %v", err)
	}
	opened.Close()

	upgradeExec(t, st, `INSERT INTO tasks(id,kind,batch_id) VALUES ('restart-orphan','authorization.sync','missing-batch')`)
	blocked, err := Open(ctx, opts)
	if blocked != nil {
		blocked.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "TASK_ORPHAN") {
		t.Fatalf("schema-current startup must still reject internal semantic corruption: %v", err)
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeReadinessBlocked {
		t.Fatalf("internal corruption lost typed readiness refusal: %v", err)
	}
}
