package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"proxypanel/internal/panel/store"
)

func TestUpgradeReadinessUsesConfiguredStoreNotRequestPath(t *testing.T) {
	dsn := os.Getenv("PANEL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PANEL_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	random := make([]byte, 12)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "pp_api_ready_" + hex.EncodeToString(random)
	if _, err = conn.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := conn.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	t.Setenv("PANEL_UPGRADE_EVIDENCE_FILE", "")
	st, err := store.OpenReadOnly(ctx, store.Options{DatabaseURL: dsn, SearchPath: schema, MigrationsDir: "../../../migrations"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	server := &Server{Store: st}
	w := httptest.NewRecorder()
	server.handleGetUpgradeReadiness(w, httptest.NewRequest("GET", "/api/v1/admin/upgrade-readiness?evidence=/etc/shadow&path=/etc/passwd", nil))
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body)
	}
	var response struct {
		Data store.UpgradeReadiness `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	// This empty configured schema must report the exact shipped migrations,
	// regardless of the attacker-controlled evidence/path query parameters.
	wantPending := []string{
		"0001_init.sql",
		"0002_batch_preview.sql",
		"0003_maintenance.sql",
		"0004_node_external_port.sql",
		"0005_period_and_owner_history.sql",
		"0006_batch_task_durability.sql",
	}
	if response.Data.Status != "ready" || !slices.Equal(response.Data.Pending, wantPending) {
		t.Fatalf("want ready with pending %v; got %s", wantPending, w.Body)
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname=$1`, schema).Scan(&count); err != nil || count != 0 {
		t.Fatalf("readiness changed schema: %d %v", count, err)
	}
	// Even when PostgreSQL inspection is unavailable, diagnosis remains HTTP
	// 200 unknown, with null checksum and non-null arrays, never driver text.
	st.Close()
	w = httptest.NewRecorder()
	server.handleGetUpgradeReadiness(w, httptest.NewRequest("GET", "/api/v1/admin/upgrade-readiness?path=/etc/passwd", nil))
	var failed struct {
		Data map[string]any `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &failed); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || failed.Data["status"] != "unknown" || failed.Data["checked_at"] == nil || failed.Data["candidate_version"] != store.CandidateBuildVersion() {
		t.Fatalf("unavailable diagnosis: %s", w.Body)
	}
	schemaState := failed.Data["schema"].(map[string]any)
	if schemaState["checksum_ok"] != nil || schemaState["applied_migrations"] == nil || schemaState["pending_migrations"] == nil || failed.Data["blockers"] == nil {
		t.Fatalf("invented success/null arrays: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "/etc/") || strings.Contains(w.Body.String(), "sql: database is closed") {
		t.Fatalf("raw error/path leaked: %s", w.Body)
	}
}
