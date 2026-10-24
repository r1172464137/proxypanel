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

// The static part of the contract is only a cheap lint: it can never prove the
// runtime isolation. It is paired with the executed run below (uid, lock, real
// probes) and must not be reported as security evidence on its own.
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
		// plaintext download of an executable payload
		"http://archive.ubuntu.com", "--proto '=http'",
		// caller-supplied binaries that would bypass origin verification
		"PPF_NGINX_BIN", "PPF_NGINX_DEB", "PPF_NSS_CERTUTIL",
	} {
		if strings.Contains(executed, forbidden) {
			t.Errorf("script must not execute %q", forbidden)
		}
	}
	for _, required := range []string{
		"set -euo pipefail", "umask 077",
		// local runtime lease, remote lock intentionally unused
		"proxypanel-webdemo-local-runtime.lock", "flock -w", "release_lock",
		// HTTPS + GPG-authenticated package origin, no bare digest trust
		"--proto '=https'", "gpgv --keyring", "Good signature",
		"main/binary-amd64/Packages.gz", "deb_sha256_verified",
		// real hosting #2 switch
		"start_panel api", "PANEL_WEB_ROOT=\"\"", "serving API only",
		"go_direct_probes", "session_survives_api_only_restart",
		// strict TLS negatives
		"[ \"$rc\" = \"60\" ]", "unable to get local issuer certificate",
		"no alternative certificate subject name", "--noproxy '*'",
		// evidence binding
		"web/tests/host-nginx-smoke.mjs",
		"dpkg-deb -x", "openssl verify", "--cacert", "rejectUnauthorized",
		"'/proc/{pid}/exe'", "PANEL_WEB_ROOT",
		"X-Forwarded-For", "host-before.json", "host-after.json",
		"go-test.rawexit", "browser_report_assert",
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
	// The "no lost listener" claim must be asserted, not merely computed.
	if !strings.Contains(body, "assert not stale_listen") {
		t.Error("compare_host_identity computes stale_listen but never asserts it")
	}
	if !strings.Contains(body, "euid_is_root") {
		t.Error("manifest does not record the non-root uid assertion")
	}
}

// The stale-listener half of the protection oracle is the part that lets a
// parallel task's shutdown silently become "no change". Prove it fails.
func TestWebdemoHostNginxStaleListenerFails(t *testing.T) {
	root := hostNginxScriptRoot(t)
	dir := t.TempDir()
	write := func(name string, listeners []string) string {
		p := filepath.Join(dir, name)
		payload := map[string]any{
			"kernel": "k", "pid1_cmdline": "/sbin/init", "pid1_ns_pid": "a",
			"host_ns_pid": "a", "host_ns_mnt": "a", "host_ns_net": "a",
			"cgroup_self": "0::/x", "docker_env_marker": false,
			"system_nginx_present": false, "uid": 1002, "gid": 1002,
			"listen_127": listeners,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}
	before := write("before.json", []string{"127.0.0.1:1", "127.0.0.1:2"})
	after := write("after.json", []string{"127.0.0.1:1"})
	out := filepath.Join(dir, "diff.json")
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "webdemo-host-nginx-verify.sh"))
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	body := string(raw)
	start := strings.Index(body, "compare_host_identity() {")
	if start < 0 {
		t.Fatal("compare_host_identity not found")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("compare_host_identity end not found")
	}
	fn := body[start : start+end+3]
	script := "set -euo pipefail\nEVIDENCE=" + dir + "\n" + fn +
		"\ncompare_host_identity\n"
	scriptPath := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"PPF_TEST_BEFORE="+before, "PPF_TEST_AFTER="+after, "PPF_TEST_OUT="+out)
	// compare_host_identity derives its paths from $EVIDENCE, so stage the files
	// under the expected names.
	if err := os.Rename(before, filepath.Join(dir, "host-before.json")); err != nil {
		t.Fatalf("stage before: %v", err)
	}
	if err := os.Rename(after, filepath.Join(dir, "host-after.json")); err != nil {
		t.Fatalf("stage after: %v", err)
	}
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a lost loopback listener was accepted as a pass: %s", output)
	}
	if !strings.Contains(string(output), "disappeared") {
		t.Fatalf("failure was not attributed to the lost listener: %s", output)
	}
	diff, err := os.ReadFile(filepath.Join(dir, "host-identity-diff.json"))
	if err != nil {
		t.Fatalf("read diff: %v", err)
	}
	if !strings.Contains(string(diff), "127.0.0.1:2") {
		t.Fatalf("diff does not record the lost listener: %s", diff)
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
		"official nginx ELF from GPG-verified Ubuntu index",
		"local-runtime lock held (fd 9) before the before-snapshot",
		"strict TLS: default store exit60+issuer",
		"spa: Go itself serves root/deeplink/asset as SPA",
		"panel restarted in API-only mode with the same DB/key/binary",
		"api: Go itself answers non-HTML 404 for root/deeplink/asset",
		"real browser form login/deep refresh/logout/404 via host nginx (spa",
		"real browser form login/deep refresh/logout/404 via host nginx (api",
		"current-host, non-container process with private ELF",
		"protected host identity unchanged",
		"no leftover nginx/panel process",
		"local-runtime lock released after all owned processes were reaped",
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
	// Same-invocation Go evidence must be raw JSON with an explicit non-skip pass.
	var goActs map[string]int
	summaryRaw, err := os.ReadFile(filepath.Join(evidence, "go-test.summary.json"))
	if err != nil {
		t.Fatalf("go-test.summary.json: %v", err)
	}
	if err := json.Unmarshal(summaryRaw, &goActs); err != nil {
		t.Fatalf("go-test.summary.json parse: %v", err)
	}
	if goActs["run"] == 0 || goActs["skip"] != 0 || goActs["fail"] != 0 {
		t.Fatalf("Go contract evidence not a clean executed pass: %v", goActs)
	}
	if raw, err := os.ReadFile(filepath.Join(evidence, "go-test.rawexit")); err != nil || strings.TrimSpace(string(raw)) != "0" {
		t.Fatalf("go-test.rawexit not 0: %q %v", raw, err)
	}

	manifestRaw, err := os.ReadFile(filepath.Join(evidence, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var manifest struct {
		UID           int    `json:"uid"`
		EUIDIsRoot    bool   `json:"euid_is_root"`
		RemoteLock    bool   `json:"remote_runtime_lock_used"`
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
	if manifest.UID == 0 || manifest.EUIDIsRoot {
		t.Errorf("manifest records a root run (uid=%d)", manifest.UID)
	}
	if manifest.RemoteLock {
		t.Error("manifest claims a remote runtime resource; this round must be host-local only")
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
	for _, fragment := range []string{
		"https://archive.ubuntu.com", "deb_sha256_verified=", "Version: 1.24.0",
		"Good signature", "signing_key=", "installed=no",
	} {
		if !strings.Contains(prov, fragment) {
			t.Errorf("nginx provenance missing %q in %q", fragment, prov)
		}
	}
}
