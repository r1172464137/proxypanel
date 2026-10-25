package store

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
)

// Decode the wire shape rather than relying on Go fields that legacy callers use.
func assertReadinessContract(t *testing.T, r *UpgradeReadiness) map[string]any {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var dto map[string]any
	if err = json.Unmarshal(raw, &dto); err != nil {
		t.Fatal(err)
	}
	checked, ok := dto["checked_at"].(string)
	if !ok {
		t.Fatalf("missing checked_at: %s", raw)
	}
	at, err := time.Parse(time.RFC3339Nano, checked)
	if err != nil || time.Since(at) > time.Minute || at.After(time.Now().Add(time.Second)) {
		t.Fatalf("not this inspection's timestamp: %s", checked)
	}
	if version, ok := dto["candidate_version"].(string); !ok || version == "" {
		t.Fatalf("missing honest version: %s", raw)
	}
	schema, ok := dto["schema"].(map[string]any)
	if !ok {
		t.Fatalf("missing schema: %s", raw)
	}
	for _, key := range []string{"applied_migrations", "pending_migrations"} {
		if _, ok := schema[key].([]any); !ok || !reflect.DeepEqual(schema[key], dto[key]) {
			t.Fatalf("schema alias %s differs: %s", key, raw)
		}
	}
	checks, ok := dto["checks"].([]any)
	if !ok || len(checks) != len(r.Checks) {
		t.Fatalf("invented or null checks: %s", raw)
	}
	blockers, ok := dto["blockers"].([]any)
	if !ok {
		t.Fatalf("null blockers: %s", raw)
	}
	want := []any{}
	for i, value := range checks {
		c := value.(map[string]any)
		legacy := r.Checks[i]
		state := map[string]string{"blocked": "fail", "unknown": "unknown", "ready": "pass"}[legacy.Status]
		if c["code"] != legacy.Code || c["status"] != legacy.Status || c["count"] != float64(legacy.Count) || c["remedy"] != legacy.Remedy || c["summary"] != legacy.Remedy || c["affected_count"] != float64(legacy.Count) || c["state"] != state || c["severity"] != "blocker" {
			t.Fatalf("contradictory aliases: %s", raw)
		}
		if _, ok := c["evidence"].([]any); !ok {
			t.Fatalf("null evidence: %s", raw)
		}
		if state != "pass" && !slices.Contains(want, c["id"]) {
			want = append(want, c["id"])
		}
	}
	if !reflect.DeepEqual(blockers, want) {
		t.Fatalf("blockers not from checks: %s", raw)
	}
	if strings.Contains(string(raw), "not-a-secret") || strings.Contains(string(raw), "token_hash") {
		t.Fatal("private database material leaked")
	}
	return dto
}

func TestReadinessPublicContractSameSnapshot(t *testing.T) {
	st := upgradeTestStore(t, true)
	ready := upgradeReady(t, st)
	dto := assertReadinessContract(t, ready)
	if ready.Status != "ready" || dto["schema"].(map[string]any)["checksum_ok"] != true {
		t.Fatalf("verified prefix: %+v", dto)
	}
	upgradeSeedAgent(t, st)
	unknown := upgradeReady(t, st)
	dto = assertReadinessContract(t, unknown)
	if unknown.Status != "unknown" || !slices.Contains(dto["blockers"].([]any), any("agent_state_unreadable")) {
		t.Fatalf("unknown must not pass: %+v", dto)
	}
	upgradeExec(t, st, `UPDATE schema_migrations SET checksum='wrong' WHERE version='0001_init.sql'`)
	blocked := upgradeReady(t, st)
	dto = assertReadinessContract(t, blocked)
	if blocked.Status != "blocked" || dto["schema"].(map[string]any)["checksum_ok"] != false || !slices.Contains(dto["blockers"].([]any), any("migration_checksum_mismatch")) {
		t.Fatalf("checksum mismatch: %+v", dto)
	}
	err := st.Migrate(t.Context(), regMigrationsDir(t))
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeReadinessBlocked || de.Status != http.StatusConflict || !strings.Contains(err.Error(), "MIGRATION_CHECKSUM_MISMATCH") {
		t.Fatalf("unsafe upgrade lost typed refusal or cause: %v", err)
	}
	if !reflect.DeepEqual(de.Details["blockers"], []string{"migration_checksum_mismatch", "agent_state_unreadable"}) {
		t.Fatalf("refusal blockers: %+v", de.Details)
	}
}

func TestReadinessPublicContractUnverifiedSchema(t *testing.T) {
	st := upgradeTestStore(t, false)
	empty := upgradeReady(t, st)
	initial := assertReadinessContract(t, empty)
	if empty.Status != "ready" || len(empty.Pending) != countShippedMigrations(t) || initial["schema"].(map[string]any)["checksum_ok"] != true {
		t.Fatalf("empty schema must preserve preflight semantics: %+v", initial)
	}
	upgradeExec(t, st, `CREATE TABLE unrelated(id int)`)
	r := upgradeReady(t, st)
	dto := assertReadinessContract(t, r)
	if r.Status != "unknown" || dto["schema"].(map[string]any)["checksum_ok"] != nil {
		t.Fatalf("unrecorded schema cannot claim checksum success: %+v", dto)
	}
}
