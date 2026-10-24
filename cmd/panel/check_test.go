package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"proxypanel/internal/panel/store"
)

func TestCheckPreflightWithoutMasterKeyOrMigrations(t *testing.T) {
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
	schema := "pp_check_" + hex.EncodeToString(random)
	if _, err = conn.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := conn.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	scopedDSN := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("invalid test DSN")
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scopedDSN = u.String()
	}
	t.Setenv("DATABASE_URL", scopedDSN)
	t.Setenv("PANEL_MASTER_KEY", "")
	t.Setenv("PANEL_MIGRATIONS_DIR", "../../migrations")
	t.Setenv("PANEL_UPGRADE_EVIDENCE_FILE", "")
	var out, stderr bytes.Buffer
	assertSameSource := func(want string) {
		t.Helper()
		var wire map[string]any
		if err := json.Unmarshal(out.Bytes(), &wire); err != nil {
			t.Fatal(err)
		}
		if wire["status"] != want || wire["candidate_version"] != store.CandidateBuildVersion() {
			t.Fatalf("CLI contract: %s", &out)
		}
		checked, err := time.Parse(time.RFC3339Nano, wire["checked_at"].(string))
		if err != nil || time.Since(checked) > time.Minute {
			t.Fatalf("CLI timestamp: %s", &out)
		}
		ro, err := store.OpenReadOnly(ctx, store.Options{DatabaseURL: scopedDSN, MigrationsDir: "../../migrations"})
		if err != nil {
			t.Fatal(err)
		}
		defer ro.Close()
		source, err := ro.CheckUpgradeReadiness(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var direct map[string]any
		if err = json.Unmarshal(raw, &direct); err != nil {
			t.Fatal(err)
		}
		delete(wire, "checked_at")
		delete(direct, "checked_at")
		if !reflect.DeepEqual(wire, direct) {
			t.Fatalf("CLI/store disagreement: %s vs %s", &out, raw)
		}
	}
	if code := runCheck(ctx, []string{"--preflight", "--json"}, &out, &stderr); code != 0 {
		t.Fatalf("exit=%d out=%s stderr=%s", code, &out, &stderr)
	}
	assertSameSource("ready")
	var r store.UpgradeReadiness
	if err = json.Unmarshal(out.Bytes(), &r); err != nil || r.Status != "ready" || r.Phase != "preflight" {
		t.Fatalf("%+v %v", r, err)
	}
	var count int
	if err = conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname=$1`, schema).Scan(&count); err != nil || count != 0 {
		t.Fatalf("CLI mutated empty schema: %d %v", count, err)
	}
	// Explicit --evidence takes precedence over the configured environment file.
	t.Setenv("PANEL_UPGRADE_EVIDENCE_FILE", filepath.Join(t.TempDir(), "missing.json"))
	out.Reset()
	stderr.Reset()
	if code := runCheck(ctx, []string{"--preflight", "--json"}, &out, &stderr); code != 2 {
		t.Fatalf("missing configured material exit=%d: %s", code, &out)
	}
	assertSameSource("blocked")
	if strings.Contains(out.String(), os.Getenv("PANEL_UPGRADE_EVIDENCE_FILE")) {
		t.Fatal("local evidence path leaked")
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err = os.WriteFile(path, []byte(`{"format_version":"proxypanel.upgrade-evidence/v1"}`), 0400); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := runCheck(ctx, []string{"--preflight", "--evidence", path, "--json"}, &out, &stderr); code != 0 {
		t.Fatalf("CLI evidence precedence exit=%d: %s", code, &out)
	}
	// A real, unrecorded schema is unknown, not an empty/ready database.
	t.Setenv("PANEL_UPGRADE_EVIDENCE_FILE", "")
	if _, err = conn.Exec(ctx, `CREATE TABLE `+pgx.Identifier{schema, "unrecorded"}.Sanitize()+`(id int)`); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := runCheck(ctx, []string{"--preflight", "--json"}, &out, &stderr); code != 3 {
		t.Fatalf("unknown exit=%d: %s", code, &out)
	}
	assertSameSource("unknown")
	// Current documented configuration/inspection failure is unknown (3),
	// while argument syntax errors remain 2. Neither prints raw DSN errors.
	t.Setenv("DATABASE_URL", "postgres://user:private-password@bad.invalid/db?invalid=%")
	out.Reset()
	stderr.Reset()
	if code := runCheck(ctx, []string{"--json"}, &out, &stderr); code != 3 {
		t.Fatalf("configuration failure exit=%d", code)
	}
	if strings.Contains(out.String()+stderr.String(), "private-password") || !strings.Contains(out.String(), `"checksum_ok":null`) {
		t.Fatalf("unsafe configuration failure: %s %s", &out, &stderr)
	}
	if code := runCheck(ctx, []string{"--invalid"}, &out, &stderr); code != 2 {
		t.Fatalf("argument failure exit=%d", code)
	}
}
