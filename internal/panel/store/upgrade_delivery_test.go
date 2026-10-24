package store

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Reuses the existing legitimate stop-review fixture and its initial proof
// verifier. Only systemctl is stubbed; candidate CLI and PostgreSQL are real.
func TestUpgradeDeliveryHelper(t *testing.T) {
	st := upgradeTestStore(t, true)
	m, review, dir := stopFixture(t, st)
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	candidate := filepath.Join(base, "releases", "new")
	if err = os.MkdirAll(candidate, 0755); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv("UPGRADE_TEST_PANEL_BINARY")
	if binary == "" {
		t.Fatal("UPGRADE_TEST_PANEL_BINARY must be the real built candidate")
	}
	if err = os.Symlink(binary, filepath.Join(candidate, "panel")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "migrations"), filepath.Join(candidate, "migrations")); err != nil {
		t.Fatal(err)
	}
	upgradeWrite(t, filepath.Join(base, ".managed-by-proxypanel"), nil)
	old := filepath.Join(base, "releases", "old")
	if err = os.MkdirAll(old, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(old, filepath.Join(base, "current")); err != nil {
		t.Fatal(err)
	}
	stub := t.TempDir()
	log := filepath.Join(stub, "calls")
	if err = os.WriteFile(filepath.Join(stub, "systemctl"), []byte("#!/bin/sh\necho \"$*\" >> \"$UPGRADE_TEST_CALLS\"\n[ \"$1\" = show ] || exit 99\necho inactive\n"), 0755); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err = st.DB.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("PANEL_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("options", "-csearch_path="+schema)
	u.RawQuery = q.Encode()
	run := func(action, evidence string, want int, text string) {
		t.Helper()
		cmd := exec.Command("bash", "-c", `source "$1"; if [[ $2 == activate ]]; then panel_activate "$3" new "$4"; else panel_rollback_check "$5"; fi`, "test", filepath.Join(root, "deploy", "upgrade-panel.sh"), action, base, evidence, old)
		cmd.Env = append(os.Environ(), "DATABASE_URL="+u.String(), "PATH="+stub+":"+os.Getenv("PATH"), "UPGRADE_TEST_CALLS="+log)
		out, e := cmd.CombinedOutput()
		code := 0
		if e != nil {
			if x, ok := e.(*exec.ExitError); ok {
				code = x.ExitCode()
			} else {
				t.Fatal(e)
			}
		}
		t.Logf("%s exit=%d output=%s", action, code, out)
		if code != want || !strings.Contains(string(out), text) {
			t.Fatalf("want exit=%d text=%q", want, text)
		}
	}
	before := upgradeReady(t, st).DatabaseSHA256
	run("activate", "", 3, "AGENT_EVIDENCE_MISSING")
	review.PostgreSQLSHA256 = strings.Repeat("0", 64)
	saveStopFixture(t, m, review, dir)
	run("activate", st.upgradeEvidenceFile, 2, "AGENT_STOP")
	if got, _ := os.Readlink(filepath.Join(base, "current")); got != old {
		t.Fatal("failed check switched current")
	}
	if after := upgradeReady(t, st).DatabaseSHA256; after != before {
		t.Fatal("readonly helper changed DB")
	}
	var count int
	if err = st.DB.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
	// Restore complete legitimate materials, without changing the proof algorithm.
	review.PostgreSQLSHA256 = before
	saveStopFixture(t, m, review, dir)
	run("activate", st.upgradeEvidenceFile, 0, "ACTIVATED_NOT_STARTED")
	if got, _ := os.Readlink(filepath.Join(base, "current")); got != candidate {
		t.Fatal("ready did not activate")
	}
	if after := upgradeReady(t, st).DatabaseSHA256; after != before {
		t.Fatal("ready check changed DB")
	}
	if err = os.Symlink(upgradePrefixDir(t, 4), filepath.Join(old, "migrations")); err != nil {
		t.Fatal(err)
	}
	run("rollback", "", 0, "keep service stopped")
	if err = os.Remove(filepath.Join(old, "migrations")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(upgradePrefixDir(t, 2), filepath.Join(old, "migrations")); err != nil {
		t.Fatal(err)
	}
	run("rollback", "", 2, "OLD_BINARY_SCHEMA_COMPATIBILITY_UNPROVEN")
	if got, _ := os.Readlink(filepath.Join(base, "current")); got != candidate {
		t.Fatal("rollback check changed management pointer")
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "start") {
		t.Fatal("helper started service")
	}
	if err = st.DB.QueryRowContext(context.Background(), `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
}
