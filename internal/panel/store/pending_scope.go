package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Migration readiness scope.
//
// The upgrade preflight requires external stopped-Agent evidence so that no
// un-uploaded usage batch is lost or mis-attributed while the schema changes.
// That guarantee only applies to migrations that can actually influence usage
// accounting. Demanding it for a migration that merely adds a column to `nodes`
// turns every additive change into a full drain ceremony for every registered
// Agent, which is stricter than the guarantee needs and blocks routine upgrades.
//
// The decision is explicit, not inferred: a migration may declare
//
//	-- readiness: schema-only
//
// at the top of the file when it cannot affect usage accounting. Anything else —
// no declaration, an unrecognised declaration, an unreadable file, or a declared
// schema-only migration that still mentions an accounting table — keeps the
// evidence requirement. The default therefore stays fail-safe: forgetting the
// marker only makes the gate stricter, never weaker.

const readinessMarker = "-- readiness:"

// accountingTables are the tables whose content carries un-uploaded or
// un-attributed usage. A migration touching any of them needs the evidence.
var accountingTables = []string{
	"usage_entries", "usage_batches", "batches", "batch_items",
	"members", "period_assertions", "owner_assertions", "tasks",
}

// pendingTouchesAccounting reports whether any pending migration may affect usage
// accounting and therefore requires external stopped-Agent evidence.
func (s *Store) pendingTouchesAccounting(pending []string) bool {
	for _, name := range pending {
		body, err := os.ReadFile(filepath.Join(s.migrationsDir, name))
		if err != nil {
			// Cannot read what we are about to apply: keep the strict behaviour.
			return true
		}
		if migrationTouchesAccounting(name, string(body)) {
			return true
		}
	}
	return false
}

// migrationTouchesAccounting is the pure decision, split out for tests.
func migrationTouchesAccounting(name, body string) bool {
	// A declared schema-only migration still has to pass the table scan: the
	// marker states intent, the scan verifies it.
	if declaredSchemaOnly(body) && !mentionsAccountingTable(body) {
		return false
	}
	return true
}

// declaredSchemaOnly reads the leading comment block for the marker. Only the
// header counts, so a marker quoted in the middle of a statement cannot opt a
// migration out by accident.
func declaredSchemaOnly(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "--") {
			// The header ends at the first statement.
			return false
		}
		if strings.HasPrefix(trimmed, readinessMarker) {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, readinessMarker))
			return value == "schema-only"
		}
	}
	return false
}

var accountingTablePattern = regexp.MustCompile(`(?i)\b(` + strings.Join(accountingTables, "|") + `)\b`)

// mentionsAccountingTable looks at statements only. Migration files document what
// they do — 0007 explains in a comment that it touches none of the accounting
// tables — and matching comments would make every well-documented migration look
// accounting-related, which is exactly backwards.
func mentionsAccountingTable(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if idx := strings.Index(trimmed, "--"); idx >= 0 {
			trimmed = trimmed[:idx]
		}
		if accountingTablePattern.MatchString(trimmed) {
			return true
		}
	}
	return false
}
