package store

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeRetentionDaysEnforcesFloor(t *testing.T) {
	cases := map[int]int{
		0:    MinRetentionDays,
		-30:  MinRetentionDays,
		3:    MinRetentionDays,
		7:    7,
		90:   90,
		9999: MaxRetentionDays,
	}
	for in, want := range cases {
		if got := NormalizeRetentionDays(in); got != want {
			t.Fatalf("NormalizeRetentionDays(%d) = %d, want %d", in, got, want)
		}
	}
	if MinRetentionDays != 7 {
		t.Fatalf("the retention floor is part of the policy and must stay 7 days, got %d", MinRetentionDays)
	}
}

func TestNormalizeBoundsIsAlwaysBounded(t *testing.T) {
	got := normalizeBounds(MaintenanceBounds{})
	if got.AlertScan <= 0 || got.Cleanup <= 0 {
		t.Fatalf("zero bounds must fall back to the default: %+v", got)
	}
	got = normalizeBounds(MaintenanceBounds{AlertScan: 1 << 20, Cleanup: 1 << 20})
	if got.AlertScan > 2000 || got.Cleanup > 5000 {
		t.Fatalf("bounds must be capped: %+v", got)
	}
}

// The cleanup allow list must never touch the accounting ledger, credentials,
// idempotency records or unfinished work: deleting those would break quota
// accounting, replay protection and the RPO of confirmed reports.
func TestCleanupNeverDeletesProtectedTables(t *testing.T) {
	protected := []string{
		"usage_entries", "usage_batches", "credentials", "idempotency_records",
		"agent_receipts", "tasks", "notification_outbox", "members", "servers",
		"nodes", "node_groups", "subscriptions", "templates", "settings",
		"control_state", "member_bindings", "quota_adjustments", "subscription_tokens",
	}
	for _, stmt := range cleanupStatements {
		upper := strings.ToUpper(stmt.Query)
		for _, table := range protected {
			if strings.Contains(upper, "DELETE FROM "+strings.ToUpper(table)) {
				t.Fatalf("cleanup statement %q deletes the protected table %q", stmt.Name, table)
			}
		}
	}
}

func TestCleanupStatementsAreBoundedAndUseThePolicyArguments(t *testing.T) {
	if len(cleanupStatements) == 0 {
		t.Fatal("no cleanup statements")
	}
	seen := map[string]bool{}
	for _, stmt := range cleanupStatements {
		if seen[stmt.Name] {
			t.Fatalf("duplicate cleanup statement %q", stmt.Name)
		}
		seen[stmt.Name] = true
		if !strings.Contains(stmt.Query, "LIMIT $3") {
			t.Fatalf("cleanup statement %q is not bounded by the per tick limit", stmt.Name)
		}
		if !strings.Contains(stmt.Query, "$1") && !strings.Contains(stmt.Query, "$2") {
			t.Fatalf("cleanup statement %q does not use a policy argument", stmt.Name)
		}
	}
	// Retention (not just expiry) must gate the history tables.
	for _, name := range []string{"events", "audit_logs", "previews", "batches", "batch_items"} {
		var found bool
		for _, stmt := range cleanupStatements {
			if stmt.Name != name {
				continue
			}
			found = true
			if !strings.Contains(stmt.Query, "$2") {
				t.Fatalf("cleanup statement %q does not use the retention cutoff", name)
			}
		}
		if !found {
			t.Fatalf("missing cleanup statement %q", name)
		}
	}
	// Batch children are removed before their parents (FK order).
	var order []string
	for _, stmt := range cleanupStatements {
		order = append(order, stmt.Name)
	}
	items, batches := -1, -1
	for i, name := range order {
		switch name {
		case "batch_items":
			items = i
		case "batches":
			batches = i
		}
	}
	if items < 0 || batches < 0 || items > batches {
		t.Fatalf("batch_items must be cleaned before batches, got order %v", order)
	}
}

func TestCleanupKeepsOpenAlertsAndUndeliveredNotifications(t *testing.T) {
	for _, stmt := range cleanupStatements {
		if stmt.Name != "events" {
			continue
		}
		q := stmt.Query
		if !strings.Contains(q, "e.state IN ('recovered', 'acked')") {
			t.Fatalf("events cleanup must only delete closed events: %s", q)
		}
		if !strings.Contains(q, "notification_outbox") || !strings.Contains(q, "o.state <> 'sent'") {
			t.Fatalf("events cleanup must keep events with undelivered notifications: %s", q)
		}
		return
	}
	t.Fatal("no events cleanup statement")
}

func TestAlertDedupeKeyIsKindAndSubject(t *testing.T) {
	if got := alertKey(AlertAgentOffline, "agt_1"); got != "agent_offline:agt_1" {
		t.Fatalf("alertKey = %q", got)
	}
	if got := alertKey(AlertMemberOverQuota, "mbr_1"); got != "over_quota:mbr_1" {
		t.Fatalf("the over quota alert must reuse the ledger dedupe key, got %q", got)
	}
	if len(MaintenanceAlertKinds) != 5 {
		t.Fatalf("expected 5 managed alert kinds, got %d", len(MaintenanceAlertKinds))
	}
	if MaintenanceLockKey == 7401941 {
		t.Fatal("the maintenance lock must not reuse the migration lock key")
	}
	if AgentOfflineAfter != 45*time.Second {
		t.Fatalf("the offline tolerance is frozen at 45s, got %s", AgentOfflineAfter)
	}
	if ServerExpiryHorizon != 7*24*time.Hour {
		t.Fatalf("the server expiry horizon is 7 days, got %s", ServerExpiryHorizon)
	}
}

func TestObservationSetCompleteness(t *testing.T) {
	set := ObservationSet{Incomplete: map[string]bool{AlertAgentOffline: true}}
	if set.Complete(AlertAgentOffline) {
		t.Fatal("a truncated scan must not be reported as complete")
	}
	if !set.Complete(AlertServerExpiring) {
		t.Fatal("an untruncated kind is complete")
	}
}

func TestTruncateMessage(t *testing.T) {
	if got := truncateMessage("  hello  ", 32); got != "hello" {
		t.Fatalf("truncateMessage trimmed = %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := truncateMessage(long, 10); len(got) != 13 || !strings.HasSuffix(got, "...") {
		t.Fatalf("truncateMessage did not bound the text: %q", got)
	}
}

func TestShortDurationAndBytes(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:          "30s",
		12 * time.Minute:          "12m",
		90 * time.Minute:          "1h30m",
		30 * time.Hour:            "1d6h",
		-1 * (2 * 24 * time.Hour): "2d0h",
	}
	for in, want := range cases {
		if got := shortDuration(in); got != want {
			t.Fatalf("shortDuration(%s) = %q, want %q", in, got, want)
		}
	}
	if got := quotaText(nil); got != "unlimited" {
		t.Fatalf("quotaText(nil) = %q", got)
	}
	if got := humanBytes(0); got != "0B" {
		t.Fatalf("humanBytes(0) = %q", got)
	}
	if got := humanBytes(1536); got != "1.5KiB" {
		t.Fatalf("humanBytes(1536) = %q", got)
	}
}
