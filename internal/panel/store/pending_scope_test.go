package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The scope decision is what lets an additive migration upgrade without a full
// drain ceremony. It must never widen by omission, so every unclear case falls
// back to demanding the evidence.
func TestMigrationTouchesAccounting(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "declared schema-only, no accounting table",
			body: "-- readiness: schema-only\n-- 只加列\nALTER TABLE nodes ADD COLUMN IF NOT EXISTS x jsonb;\n",
			want: false,
		},
		{
			name: "no declaration at all",
			body: "ALTER TABLE nodes ADD COLUMN IF NOT EXISTS x jsonb;\n",
			want: true,
		},
		{
			name: "declared but touches usage_entries",
			body: "-- readiness: schema-only\nUPDATE usage_entries SET n = 0;\n",
			want: true,
		},
		{
			name: "declared but touches batches",
			body: "-- readiness: schema-only\nALTER TABLE batches ADD COLUMN y int;\n",
			want: true,
		},
		{
			name: "declared but rewrites members",
			body: "-- readiness: schema-only\nUPDATE members SET period_timezone = 'UTC';\n",
			want: true,
		},
		{
			name: "unrecognised declaration value is not a licence",
			body: "-- readiness: whatever\nALTER TABLE nodes ADD COLUMN x int;\n",
			want: true,
		},
		{
			name: "explicit accounting declaration",
			body: "-- readiness: accounting\n-- 说明\nALTER TABLE usage_batches ADD COLUMN z int;\n",
			want: true,
		},
		{
			name: "marker after the first statement does not count",
			body: "ALTER TABLE nodes ADD COLUMN x int;\n-- readiness: schema-only\n",
			want: true,
		},
		{
			// "batches_count" on nodes is not the accounting table: word boundaries
			// keep the scan from matching an identifier that merely starts the same.
			name: "similar but unrelated identifier",
			body: "-- readiness: schema-only\nALTER TABLE nodes ADD COLUMN batches_count int;\n",
			want: false,
		},
		{
			// Mentions of accounting tables inside comments must not count either;
			// otherwise a well documented migration never qualifies.
			name: "accounting names only in comments",
			body: "-- readiness: schema-only\n-- 不碰 usage_entries / usage_batches / batches / members\nALTER TABLE nodes ADD COLUMN x int;\n",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := migrationTouchesAccounting("0000_test.sql", c.body); got != c.want {
				t.Fatalf("migrationTouchesAccounting = %v, want %v", got, c.want)
			}
		})
	}
}

// Shipped migrations must declare their scope. Without this, someone adding an
// additive migration and forgetting the marker silently keeps every deployment
// blocked on evidence — the failure mode this scoping was added to remove.
func TestShippedMigrationsDeclareScope(t *testing.T) {
	dir := regMigrationsDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		checked++
		// Declared schema-only migrations must stay out of scope; the rest are
		// expected to be accounting-related and therefore in scope.
		declared := declaredSchemaOnly(string(body))
		touches := migrationTouchesAccounting(entry.Name(), string(body))
		if declared && touches {
			t.Fatalf("%s declares schema-only but still touches an accounting table; remove the declaration or the statement", entry.Name())
		}
	}
	if checked == 0 {
		t.Fatal("no migrations were checked")
	}
}

// A pending migration that cannot be read must keep the strict behaviour.
func TestUnreadablePendingMigrationDemandsEvidence(t *testing.T) {
	s := &Store{migrationsDir: t.TempDir()}
	if !s.pendingTouchesAccounting([]string{"missing.sql"}) {
		t.Fatal("unreadable migration must demand evidence")
	}
}

// End to end: only an additive schema-only migration pending -> no evidence
// required. A pending migration that touches accounting still refuses.
func TestUpgradeOpenScopesEvidenceToAccountingMigrations(t *testing.T) {
	ctx := t.Context()
	st := upgradeTestStore(t, false)
	if err := st.Migrate(ctx, regMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	upgradeSeedAgent(t, st)
	st.upgradeEvidenceFile = filepath.Join(t.TempDir(), "absent.json")

	// Copy the full shipped set plus one additive migration that declares
	// schema-only; no evidence file exists.
	dir := upgradePrefixDir(t, allShippedMigrations(t))
	upgradeWrite(t, filepath.Join(dir, "9999_additive.sql"),
		[]byte("-- readiness: schema-only\n-- 只加列\nALTER TABLE nodes ADD COLUMN IF NOT EXISTS scope_probe jsonb;\n"))
	opened, err := Open(ctx, upgradeOpenOptions(t, st, dir))
	if err != nil {
		t.Fatalf("additive migration must not require Agent evidence: %v", err)
	}
	opened.Close()

	// Same shape, but the pending migration touches usage accounting.
	dir2 := upgradePrefixDir(t, allShippedMigrations(t))
	upgradeWrite(t, filepath.Join(dir2, "9998_accounting.sql"),
		[]byte("ALTER TABLE usage_batches ADD COLUMN IF NOT EXISTS scope_probe int;\n"))
	blocked, err := Open(ctx, upgradeOpenOptions(t, st, dir2))
	if blocked != nil {
		blocked.Close()
	}
	if err == nil || !containsAll(err.Error(), "AGENT_EVIDENCE_MISSING") {
		t.Fatalf("accounting migration must still demand Agent evidence: %v", err)
	}
}

func allShippedMigrations(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(regMigrationsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".sql" {
			n++
		}
	}
	return n
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for i := 0; i+len(n) <= len(haystack); i++ {
			if haystack[i:i+len(n)] == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// countShippedMigrations is the number of migration files in the repository. Tests
// assert against it instead of a hard-coded count, so adding a migration does not
// require touching upgrade fixtures.
func countShippedMigrations(t *testing.T) int {
	t.Helper()
	return allShippedMigrations(t)
}
