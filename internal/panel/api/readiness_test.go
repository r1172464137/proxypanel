package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
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
	if response.Data.Status != "ready" || len(response.Data.Pending) != 5 {
		t.Fatalf("%s", w.Body)
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname=$1`, schema).Scan(&count); err != nil || count != 0 {
		t.Fatalf("readiness changed schema: %d %v", count, err)
	}
}
