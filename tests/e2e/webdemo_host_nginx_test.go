package e2e

// web-demo stage F: real HOST-user nginx layer. This test owns only the new
// scripts/webdemo-host-nginx-verify.sh asset and never touches the shared
// dual-hosting runner, the 8445 preview, the shared 15432 devdb or system nginx.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func hostNginxScriptRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("repository root not found")
	return ""
}

// The script's safety contract is the thing that must not silently regress:
// no root, no package install, no PATH rewrite, no docker/systemd mutation, and
// no fixed/privileged port.
func TestWebdemoHostNginxVerifyScriptContract(t *testing.T) {
	root := hostNginxScriptRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "webdemo-host-nginx-verify.sh"))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	body := string(raw)
	// Comments describe the boundary ("no docker/podman use"); only executed
	// lines are checked for forbidden commands.
	var code []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		code = append(code, line)
	}
	executed := strings.Join(code, "\n")
	for _, forbidden := range []string{
		"sudo ", "sudo\t", "apt-get install", "apt install", "dpkg -i",
		"systemctl start", "systemctl stop", "systemctl enable", "export PATH=",
		"docker ps", "docker run", "docker rm", "docker exec", "docker build",
		"podman run", "podman ps",
		"listen 0.0.0.0", "listen [::]", "--privileged", "127.0.0.1:8443",
		"127.0.0.1:8445", "/.devdb", ":15432",
	} {
		if strings.Contains(executed, forbidden) {
			t.Errorf("script must not execute %q", forbidden)
		}
	}
	for _, required := range []string{
		"set -euo pipefail", "umask 077",
		"proxypanel-webdemo-remote-runtime.lock",
		"web/tests/host-nginx-smoke.mjs",
		"dpkg-deb -x", "openssl verify", "--cacert", "rejectUnauthorized",
		"'/proc/{pid}/exe'", "host-nginx-smoke.mjs", "PANEL_WEB_ROOT",
		"X-Forwarded-For", "host-before.json", "host-after.json",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("script is missing required fragment %q", required)
		}
	}
	// Every listen directive must pin loopback and use the random port variable.
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "listen ") && !strings.Contains(trimmed, "listen 127.0.0.1:$HTTPS_PORT") {
			t.Errorf("non-loopback/non-random listen directive: %q", trimmed)
		}
	}
}

// PPF_HOST_NGINX_E2E=1 runs the real host-user nginx verification (private temp
// prefix, random loopback ports, owned PG/panel, private CA). It is opt-in so the
// default Go suite stays fast; evidence lands in PPF_EVIDENCE.
func TestWebdemoHostNginxRealRun(t *testing.T) {
	if os.Getenv("PPF_HOST_NGINX_E2E") != "1" {
		t.Skip("set PPF_HOST_NGINX_E2E=1 to run the real host-user nginx verification")
	}
	root := hostNginxScriptRoot(t)
	evidence := os.Getenv("PPF_EVIDENCE")
	if evidence == "" {
		t.Fatal("PPF_EVIDENCE must be set for the real run")
	}
	if _, err := os.Stat(evidence); err == nil {
		t.Fatalf("PPF_EVIDENCE must be a fresh path: %s", evidence)
	}
	cmd := exec.Command("bash", filepath.Join(root, "scripts", "webdemo-host-nginx-verify.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PPF_EVIDENCE="+evidence)
	out, err := cmd.CombinedOutput()
	t.Logf("verify output:\n%s", out)
	if err != nil {
		t.Fatalf("verify script failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(evidence, "result.json"))
	if err != nil {
		t.Fatalf("result.json: %v", err)
	}
	var result struct {
		Status string   `json:"status"`
		Checks []string `json:"checks"`
		NotRun []string `json:"not_run"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("result.json parse: %v", err)
	}
	if result.Status != "passed" {
		t.Fatalf("status=%q checks=%v", result.Status, result.Checks)
	}
	want := []string{
		"strict TLS: default store rejects",
		"real browser form login/deep refresh/logout/404 via host nginx (spa",
		"real browser form login/deep refresh/logout/404 via host nginx (api",
		"current-host, non-container process with private ELF",
		"protected host identity unchanged",
		"no leftover nginx/panel process",
	}
	joined := strings.Join(result.Checks, "\n")
	for _, fragment := range want {
		if !strings.Contains(joined, fragment) {
			t.Errorf("missing check %q in %v", fragment, result.Checks)
		}
	}
	for _, line := range result.NotRun {
		t.Logf("NOT_RUN: %s", line)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(evidence, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var manifest struct {
		SourceSHA     string `json:"source_sha"`
		SourceDirty   bool   `json:"source_dirty"`
		ScriptSHA     string `json:"script_sha256"`
		PanelBinSHA   string `json:"panel_binary_sha256"`
		WebIndexSHA   string `json:"web_index_sha256"`
		WebAssetSHA   string `json:"web_asset_sha256"`
		TestScriptSHA string `json:"test_script_sha256"`
		NginxELF      struct {
			SHA256     string   `json:"sha256"`
			Provenance []string `json:"provenance"`
		} `json:"nginx_elf"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatalf("manifest.json parse: %v", err)
	}
	if manifest.SourceSHA == "" || len(manifest.SourceSHA) != 40 {
		t.Errorf("manifest source_sha missing: %q", manifest.SourceSHA)
	}
	for name, value := range map[string]string{
		"script": manifest.ScriptSHA, "panel": manifest.PanelBinSHA,
		"web_index": manifest.WebIndexSHA, "web_asset": manifest.WebAssetSHA,
		"test_script": manifest.TestScriptSHA, "nginx": manifest.NginxELF.SHA256,
	} {
		if len(value) != 64 {
			t.Errorf("manifest %s sha256 not bound: %q", name, value)
		}
	}
	prov := strings.Join(manifest.NginxELF.Provenance, "\n")
	for _, fragment := range []string{"archive.ubuntu.com", "deb_sha256=", "Version: 1.24.0"} {
		if !strings.Contains(prov, fragment) {
			t.Errorf("nginx provenance missing %q in %q", fragment, prov)
		}
	}
}
