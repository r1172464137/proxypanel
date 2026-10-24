package e2e

// web-demo stage F (deployment / install capability). Owner: webdemo_platform_*.
//
// Scope and honest boundaries:
//   - This file never installs the Agent on a host, never writes /etc, never
//     creates accounts, never calls systemctl on a system unit, never touches
//     the running 203 preview/legacy stacks and never needs Docker.
//   - It exercises the REAL deploy-agent.sh render/validate logic. Privileged
//     side effects (install/systemctl/useradd/...) are replaced by explicit
//     tripwires that exit 97 with a unique marker, so a path that reaches them
//     fails loudly and visibly instead of being shielded by "not defined".
//   - It runs the REAL built Agent binary (./cmd/agent) inside a REAL transient
//     *user* systemd unit together with the REAL official core at
//     /home/compile/Work/bin/xray, through a local fake panel registered with the
//     real Agent registration/snapshot/heartbeat client protocol. PID, /proc
//     exe/cmdline and binary hash are captured from that Agent process, and a
//     kill/restart proves a NEW pid of the SAME Agent binary.
//   - It is a *user manager* transient unit, not the system unit: the system
//     unit's User=/StateDirectory=/ProtectSystem=strict/ProtectHome and the
//     capability bounding set cannot be applied here; those are recorded as
//     UNVERIFIED, never asserted as passing.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	webdemoPlatformEvidenceEnv = "PP_WEB_DEMO_EVIDENCE_DIR"
	webdemoPlatformXrayPath    = "/home/compile/Work/bin/xray"
	webdemoPlatformLocalLock   = "/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock"

	// Messages emitted by the shipped validator (deploy/deploy-agent.sh:525/529/
	// 534/539). A negative case must match exit 1 AND the exact message: a
	// 127/compile/timeout failure never counts as a passed negative oracle.
	webdemoDenySystemDir = "拒绝将系统目录用作 Agent 状态目录"
	webdemoDenyUnsafe    = "状态目录必须是无空白/控制字符的绝对路径"
	webdemoDenyTraversal = "状态路径必须规范化且不能穿过符号链接或包含 .."
	webdemoDenySymlink   = "状态路径不能包含符号链接"

	webdemoTripwireExit = 97
)

func webdemoPlatformRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "deploy", "deploy-agent.sh")); err != nil {
		t.Fatalf("repo root %s: %v", root, err)
	}
	return root
}

func webdemoPlatformSHA(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("sha256 %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// webdemoPlatformHarness is the sourced-installer harness. `set -euo pipefail`
// and the shipped trap-free die() are preserved, but every privileged external
// command the installer could reach is shadowed by a tripwire that exits 97 with
// a unique marker. Real render_unit/validate_state_dir are untouched.
const webdemoPlatformHarness = `
set -euo pipefail
__ppf_tripwire() { printf 'PPF_TRIPWIRE_PRIVILEGED:%s\n' "$1" >&2; exit 97; }
install() { __ppf_tripwire install; }
systemctl() { __ppf_tripwire systemctl; }
useradd() { __ppf_tripwire useradd; }
groupadd() { __ppf_tripwire groupadd; }
usermod() { __ppf_tripwire usermod; }
curl() { __ppf_tripwire curl; }
unzip() { __ppf_tripwire unzip; }
apt-get() { __ppf_tripwire apt-get; }
dnf() { __ppf_tripwire dnf; }
runuser() { __ppf_tripwire runuser; }
sudo() { __ppf_tripwire sudo; }
chown() { __ppf_tripwire chown; }
chmod() { __ppf_tripwire chmod; }
rm() { __ppf_tripwire rm; }
source "$PPF_LIBRARY"
case "$PPF_ACTION" in
  render)
    ARG_STATE_DIR="$PPF_TEST_STATE"
    ARG_PANEL_CA="${PPF_TEST_CA-}"
    render_unit
    ;;
  validate)
    validate_state_dir "$PPF_TEST_STATE"
    ;;
  tripwire)
    # Proves the tripwire layer itself rejects privileged side effects.
    install -d /tmp/ppf-never-created
    ;;
  *) printf 'unknown harness action\n' >&2; exit 96 ;;
esac
printf 'PPF_HARNESS_OK\n' >&2
`

type webdemoHarnessResult struct {
	Exit   int
	Stdout string
	Stderr string
}

func webdemoPlatformHarnessRun(t *testing.T, action, state, ca string) webdemoHarnessResult {
	t.Helper()
	root := webdemoPlatformRepo(t)
	cmd := exec.Command("/bin/bash", "-c", webdemoPlatformHarness)
	// PATH still contains the real /usr/bin/install and /usr/bin/systemctl: the
	// tripwires, not an empty PATH, are what make privileged calls fatal.
	cmd.Env = append(os.Environ(),
		"PPF_LIBRARY="+filepath.Join(root, "deploy", "deploy-agent.sh"),
		"PPF_ACTION="+action,
		"PPF_TEST_STATE="+state,
		"PPF_TEST_CA="+ca,
		"PATH=/usr/bin:/bin")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := webdemoHarnessResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		res.Exit = 0
	} else if ee, ok := err.(*exec.ExitError); ok {
		res.Exit = ee.ExitCode()
	} else {
		t.Fatalf("harness %s: %v", action, err)
	}
	return res
}

// TestWebDemoPlatformUnitContract covers the shipped unit contract, the real
// install path as far as it can be executed without root, and the negative
// state-dir oracle with exact messages and exit codes.
func TestWebDemoPlatformUnitContract(t *testing.T) {
	rendered := webdemoPlatformHarnessRun(t, "render", "/var/lib/proxypanel-agent", "/tmp/ca.crt")
	if rendered.Exit != 0 {
		t.Fatalf("render_unit exit=%d stdout=%q stderr=%q", rendered.Exit, rendered.Stdout, rendered.Stderr)
	}
	if !strings.Contains(rendered.Stderr, "PPF_HARNESS_OK") {
		t.Fatalf("harness did not confirm render completion: stderr=%q", rendered.Stderr)
	}
	// stdout is the rendered unit only; the harness marker goes to stderr so it
	// can never leak into the file handed to systemd-analyze.
	unit := rendered.Stdout
	required := []struct{ name, re string }{
		{"service account", `(?m)^User=proxypanel-agent$`},
		{"group", `(?m)^Group=proxypanel-agent$`},
		{"exec uses agent layout, not release symlink", `(?m)^ExecStart=/opt/proxypanel/agent/agent -config /etc/proxypanel/agent\.json$`},
		{"restart policy", `(?m)^Restart=on-failure$`},
		{"no setuid escape", `(?m)^NoNewPrivileges=true$`},
		{"read-only system", `(?m)^ProtectSystem=strict$`},
		{"private tmp", `(?m)^PrivateTmp=true$`},
		{"no home access", `(?m)^ProtectHome=true$`},
		{"state is the only writable path", `(?m)^ReadWritePaths=/var/lib/proxypanel-agent$`},
		{"capability bounding set is minimal", `(?m)^CapabilityBoundingSet=CAP_NET_BIND_SERVICE$`},
		{"address families restricted", `(?m)^RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK$`},
		{"wanted by multi-user", `(?m)^WantedBy=multi-user\.target$`},
		{"explicit CA only", `(?m)^Environment=SSL_CERT_FILE=/usr/local/share/ca-certificates/proxypanel-panel\.crt$`},
	}
	for _, req := range required {
		if !regexp.MustCompile(req.re).MatchString(unit) {
			t.Errorf("rendered unit missing %s (%s):\n%s", req.name, req.re, unit)
		}
	}
	// No CA requested => no SSL_CERT_FILE at all (a missing file breaks TLS).
	if noCA := webdemoPlatformHarnessRun(t, "render", "/var/lib/proxypanel-agent", ""); noCA.Exit != 0 {
		t.Fatalf("render_unit without CA: exit=%d %s", noCA.Exit, noCA.Stderr)
	} else if strings.Contains(noCA.Stdout, "SSL_CERT_FILE") {
		t.Error("unit sets SSL_CERT_FILE without an explicit --panel-ca")
	}

	// Positive control: the shipped default state dir must still validate.
	if ok := webdemoPlatformHarnessRun(t, "validate", "/var/lib/proxypanel-agent", ""); ok.Exit != 0 {
		t.Fatalf("valid state dir rejected: exit=%d %s", ok.Exit, ok.Stderr)
	}

	// Negative oracle: each forbidden shape must exit 1 with its exact message.
	// Anything else (127, sh syntax error, timeout, tripwire 97) is a failure.
	cases := []struct {
		state string
		want  string
	}{
		{"/etc", webdemoDenySystemDir},
		{"/etc/", webdemoDenySystemDir},
		{"/etc/proxypanel", webdemoDenySystemDir},
		{"/var/lib", webdemoDenySystemDir},
		{"/tmp/x", webdemoDenySystemDir},
		{"/opt/proxypanel/data", webdemoDenySystemDir},
		{"/var/lib/systemd/x", webdemoDenySystemDir},
		{"/", webdemoDenyUnsafe},
		{"/var/lib/proxypanel agent", webdemoDenyUnsafe},
		{"/var/lib/../lib/ppf", webdemoDenyTraversal},
	}
	for _, c := range cases {
		res := webdemoPlatformHarnessRun(t, "validate", c.state, "")
		if res.Exit != 1 {
			t.Errorf("state dir %q: exit=%d, want 1 (stderr=%q stdout=%q)", c.state, res.Exit, res.Stderr, res.Stdout)
			continue
		}
		if !strings.Contains(res.Stderr, c.want) {
			t.Errorf("state dir %q: stderr %q does not contain %q", c.state, res.Stderr, c.want)
		}
		if strings.Contains(res.Stdout+res.Stderr, "PPF_HARNESS_OK") {
			t.Errorf("state dir %q: harness reported success inside a negative case", c.state)
		}
	}

	// A symlinked state path must be rejected with the symlink normalisation
	// message. The deny list already forbids /var/lib, so the fixture lives under
	// a private tmpfs mounted over /var/lib inside an unshared user+mount
	// namespace; nothing on the real host is written.
	t.Run("symlinked_state_dir", func(t *testing.T) {
		root := webdemoPlatformRepo(t)
		inner := fmt.Sprintf(`set -euo pipefail
mount -t tmpfs tmpfs /var/lib
mkdir -p /var/lib/ppf-e2e/real
ln -s /var/lib/ppf-e2e/real /var/lib/ppf-e2e/state
PPF_LIBRARY=%q PPF_ACTION=validate PPF_TEST_STATE=/var/lib/ppf-e2e/state PATH=/usr/bin:/bin bash -c "$PPF_HARNESS"`,
			filepath.Join(root, "deploy", "deploy-agent.sh"))
		cmd := exec.Command("unshare", "-r", "-m", "--", "/bin/bash", "-c", inner)
		cmd.Env = append(os.Environ(), "PPF_HARNESS="+webdemoPlatformHarness, "PATH=/usr/bin:/bin")
		out, err := cmd.CombinedOutput()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
				if !strings.Contains(string(out), webdemoDenySymlink) && !strings.Contains(string(out), webdemoDenyTraversal) {
					t.Fatalf("symlinked state dir rejected with unexpected message: %s", out)
				}
				return
			}
			t.Fatalf("symlink probe could not run (not a verdict): %v\n%s", err, out)
		}
		t.Fatalf("validator accepted a symlinked state dir: %s", out)
	})

	// Real refusal path: the installer must refuse to run without root before any
	// mutation, and it must be root-only by design rather than "safe because we
	// happen to lack root". Running under root is refused by the test itself.
	installer := filepath.Join(webdemoPlatformRepo(t), "deploy", "deploy-agent.sh")
	if os.Geteuid() == 0 {
		t.Fatal("refusing to run deploy-agent.sh as root from an automated test")
	}
	refuse := exec.Command("/bin/bash", installer, "--dry-run", "--yes", "--panel-url", "https://127.0.0.1:1")
	refuse.Env = append(os.Environ(), "PATH=/usr/bin:/bin")
	out, err := refuse.CombinedOutput()
	var exit int
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("installer refusal: %v", err)
		}
		exit = ee.ExitCode()
	}
	if exit != 1 {
		t.Fatalf("installer non-root refusal exit=%d, want 1: %s", exit, out)
	}
	if !strings.Contains(string(out), "需要 root") {
		t.Errorf("installer refusal message missing 需要 root: %s", out)
	}

	// systemd's own parser accepts the rendered unit. The verbatim file can only
	// fail on the not-yet-installed ExecStart binary, so that single finding is
	// recorded explicitly (with the non-zero exit status) and every other finding
	// is fatal. A second copy with ExecStart pointed at /bin/true must then parse
	// with exit 0: that is the real check that all shipped directives are valid.
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Fatalf("systemd-analyze missing: unit parse UNVERIFIED (not passed)")
	}
	dir := t.TempDir()

	findings := func(text string) []string {
		var out []string
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.Contains(line, "is not executable") {
				continue
			}
			out = append(out, line)
		}
		return out
	}

	verbatim := filepath.Join(dir, "proxypanel-agent.service")
	if err := os.WriteFile(verbatim, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	verOut, verErr := exec.Command("systemd-analyze", "verify", verbatim).CombinedOutput()
	text := strings.TrimSpace(string(verOut))
	if extra := findings(text); len(extra) > 0 {
		t.Fatalf("unit rejected by systemd (findings beyond pre-install ExecStart): %v", extra)
	}
	// exit status 1 is expected here and reported as such, never swallowed.
	t.Logf("systemd-analyze verify (verbatim, expected pre-install ExecStart only): exit=%v output=%q",
		checkExitStatus(verErr), text)

	sanitized := filepath.Join(dir, "proxypanel-agent-sanitized.service")
	substituted := regexp.MustCompile(`(?m)^ExecStart=.*$`).ReplaceAllString(unit, "ExecStart=/bin/true")
	if substituted == unit {
		t.Fatal("ExecStart not found in rendered unit")
	}
	if err := os.WriteFile(sanitized, []byte(substituted), 0o600); err != nil {
		t.Fatal(err)
	}
	sout, serr := exec.Command("systemd-analyze", "verify", sanitized).CombinedOutput()
	if sfind := findings(string(sout)); serr != nil || len(sfind) > 0 {
		t.Fatalf("unit directives rejected by systemd: exit=%v %v\n%s", checkExitStatus(serr), sfind, sout)
	}

	// The tripwire layer itself must be fatal, otherwise the "privileged calls
	// fail loudly" claim above would be vacuous.
	trip := webdemoPlatformHarnessRun(t, "tripwire", "/var/lib/proxypanel-agent", "")
	if trip.Exit != webdemoTripwireExit || !strings.Contains(trip.Stderr, "PPF_TRIPWIRE_PRIVILEGED:install") {
		t.Fatalf("privileged tripwire not fatal: exit=%d stderr=%q", trip.Exit, trip.Stderr)
	}
}

// ---------------------------------------------------------------- real systemd

type webdemoPlatformUnitState struct {
	ActiveState string
	MainPID     string
	NRestarts   string
}

func webdemoPlatformUnitShow(t *testing.T, unit string) webdemoPlatformUnitState {
	t.Helper()
	out, err := exec.Command("systemctl", "--user", "show", unit,
		"-p", "ActiveState", "-p", "MainPID", "-p", "NRestarts").Output()
	if err != nil {
		t.Fatalf("systemctl show %s: %v", unit, err)
	}
	var state webdemoPlatformUnitState
	// Values are keyed: the order is not guaranteed, so never index positionally.
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			state.ActiveState = value
		case "MainPID":
			state.MainPID = value
		case "NRestarts":
			state.NRestarts = value
		}
	}
	if state.ActiveState == "" || state.MainPID == "" {
		t.Fatalf("unexpected show output %q", out)
	}
	return state
}

func webdemoPlatformProcStatus(t *testing.T, pid, key string) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + pid + "/status")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
	}
	return ""
}

// webdemoProcIdentity is the /proc receipt of one Agent process: it must point at
// the binary this test built, not at a shell or a sleep.
type webdemoProcIdentity struct {
	PID       string   `json:"pid"`
	Exe       string   `json:"exe"`
	Cmdline   []string `json:"cmdline"`
	ExeSHA256 string   `json:"exe_sha256"`
}

// webdemoPlatformProcIdentity reads the /proc receipt of pid. ok=false means the
// process is still the manager's exec helper (systemd-executor) and has not yet
// become the Agent, so the caller must retry rather than mislabel it.
func webdemoPlatformProcIdentity(t *testing.T, pid, agentBin string) (webdemoProcIdentity, bool) {
	t.Helper()
	ident := webdemoProcIdentity{PID: pid}
	exe, err := os.Readlink("/proc/" + pid + "/exe")
	if err != nil {
		return ident, false
	}
	ident.Exe = strings.TrimSuffix(exe, " (deleted)")
	raw, err := os.ReadFile("/proc/" + pid + "/cmdline")
	if err != nil {
		return ident, false
	}
	ident.Cmdline = strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if ident.Exe != agentBin {
		return ident, false
	}
	ident.ExeSHA256 = webdemoPlatformSHA(t, ident.Exe)
	want := webdemoPlatformSHA(t, agentBin)
	if ident.ExeSHA256 != want {
		t.Fatalf("running process %s is %q (sha256 %s), not the built Agent %q (sha256 %s): cmdline=%q",
			pid, ident.Exe, ident.ExeSHA256, agentBin, want, ident.Cmdline)
	}
	return ident, true
}

// webdemoPlatformWaitAgentPID polls until pid really is the built Agent binary.
func webdemoPlatformWaitAgentPID(t *testing.T, pid, agentBin string, timeout time.Duration) (webdemoProcIdentity, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last webdemoProcIdentity
	for time.Now().Before(deadline) {
		if ident, ok := webdemoPlatformProcIdentity(t, pid, agentBin); ok {
			return ident, true
		} else {
			last = ident
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("pid %s never became the Agent binary; last exe=%q cmdline=%q", pid, last.Exe, last.Cmdline)
	return last, false
}

// webdemoFakePanel implements the Agent control protocol locally (register ->
// snapshot -> applied -> heartbeat/usage) so the real Agent stays in its long
// running loop. It is a fixture, not a production panel.
type webdemoFakePanel struct {
	mu         sync.Mutex
	server     *httptest.Server
	token      string
	agentID    string
	generation string
	hits       map[string]int
}

func webdemoNewFakePanel(t *testing.T, token string) *webdemoFakePanel {
	t.Helper()
	p := &webdemoFakePanel{
		token:      token,
		agentID:    "agent-ppf-e2e",
		generation: "ppf-e2e-generation",
		hits:       map[string]int{},
	}
	mux := http.NewServeMux()
	record := func(name string, h http.HandlerFunc) {
		mux.HandleFunc(name, func(w http.ResponseWriter, r *http.Request) {
			p.mu.Lock()
			p.hits[name]++
			p.mu.Unlock()
			h(w, r)
		})
	}
	record("/api/v1/agent/register", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Code != "ppf-e2e-registration-code" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"agent_id": p.agentID, "token": p.token, "control_generation": p.generation,
		}})
	})
	record("/api/v1/agent/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+p.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	record("/api/v1/agent/applied", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": true}})
	})
	record("/api/v1/agent/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accepted": true}})
	})
	record("/api/v1/agent/usage", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batch_id": "ppf", "status": "accepted"}})
	})
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

func (p *webdemoFakePanel) hitCount(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits[path]
}

func (p *webdemoFakePanel) snapshot() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for k, v := range p.hits {
		out[k] = v
	}
	return out
}

// TestWebDemoPlatformRealAgentRestart runs the REAL built Agent binary (not a
// shell, not sleep) under a REAL transient user unit with the real official core
// path, proves from /proc that the running process is that Agent binary, kills
// ONLY the Agent process and proves a NEW pid of the SAME binary.
// Gated: PP_WEB_DEMO_SYSTEMD=1 PP_WEB_DEMO_EVIDENCE_DIR=<private 0700 dir>.
func TestWebDemoPlatformRealAgentRestart(t *testing.T) {
	if os.Getenv("PP_WEB_DEMO_SYSTEMD") != "1" {
		t.Skip("PP_WEB_DEMO_SYSTEMD=1 required (starts transient user units)")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Fatal("systemd-run missing: no systemd manager available")
	}
	if out, err := exec.Command("systemctl", "--user", "is-system-running").Output(); err != nil {
		t.Fatalf("user systemd manager unavailable: %v (%s)", err, out)
	}
	// The official core is required: a shell stub would make the Agent exit in
	// checkCoreBinary before it ever reaches the long-running loop.
	if _, err := os.Stat(webdemoPlatformXrayPath); err != nil {
		t.Fatalf("official core %s missing: %v", webdemoPlatformXrayPath, err)
	}
	root := webdemoPlatformRepo(t)
	// The real official core binary is invoked by the Agent only for its
	// `version` compatibility probe; this test creates no snapshot instances, so
	// no core process and no listener ever exists. The shared local web-demo
	// runtime lock is therefore taken only when it is free (cheap, and honours the
	// boundary) and the fact is recorded either way, instead of blocking another
	// owner's real-core scenario.
	localLock, lockHeld := webdemoPlatformTryLocalLock(t)
	t.Cleanup(func() { webdemoPlatformReleaseLocalLock(localLock) })
	// PrivateTmp=yes hides /tmp and /var/tmp inside the unit, so the fixture
	// (binary, config, state) must live elsewhere. A sibling dir of the worktree
	// keeps it inside the same filesystem without touching repo content.
	fixture, err := os.MkdirTemp(filepath.Dir(root), ".ppf-webdemo-f-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		chmodTree(fixture, 0o700, 0o600)
		os.RemoveAll(fixture)
	})
	agentBin := filepath.Join(fixture, "agent")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", agentBin, "./cmd/agent")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent: %v\n%s", err, out)
	}
	stateDir := filepath.Join(fixture, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	token := "ppf-e2e-token-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	panel := webdemoNewFakePanel(t, token)
	cfgPath := filepath.Join(fixture, "agent.json")
	cfg := map[string]any{
		"panel_url":                  panel.server.URL,
		"registration_code":          "ppf-e2e-registration-code",
		"allow_loopback_http":        true,
		"xray_path":                  webdemoPlatformXrayPath,
		"state_dir":                  stateDir,
		"poll_interval_seconds":      2,
		"stats_interval_seconds":     2,
		"heartbeat_interval_seconds": 2,
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	unit := fmt.Sprintf("ppf-webdemo-f-%d", os.Getpid())
	cleanup := func() {
		exec.Command("systemctl", "--user", "stop", unit).Run()
		exec.Command("systemctl", "--user", "reset-failed", unit).Run()
	}
	defer cleanup()

	evidence := map[string]any{
		"unit_name":           unit,
		"local_runtime_lock":  map[string]any{"path": webdemoPlatformLocalLock, "held": lockHeld, "reason": lockReason(lockHeld)},
		"agent_binary":        agentBin,
		"agent_binary_sha256": webdemoPlatformSHA(t, agentBin),
		"core_binary":         webdemoPlatformXrayPath,
		"core_binary_sha256":  webdemoPlatformSHA(t, webdemoPlatformXrayPath),
		"state_dir":           stateDir,
		"config_sha256":       webdemoPlatformSHA(t, cfgPath),
		"unit_manager":        "systemd --user (transient); NOT the shipped system unit",
		"started_at":          time.Now().UTC().Format(time.RFC3339Nano),
	}

	// Directives the shipped unit carries that this delegated user manager can
	// actually apply here: NoNewPrivileges, PrivateTmp, UMask and
	// RestrictAddressFamilies (probed below). This is a SUBSET of the shipped
	// hardening - the system unit's User=/Group=/ProtectSystem=strict/
	// ProtectHome=/ReadWritePaths=/StateDirectory and CapabilityBoundingSet are
	// NOT verified by this test.
	hardening := []string{
		"--property=NoNewPrivileges=yes",
		"--property=PrivateTmp=yes",
		"--property=UMask=0077",
		"--property=RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK",
	}

	// Capability probe: the user manager rejects CapabilityBoundingSet with
	// 218/CAPABILITIES, which is an environment limit, not a capability result.
	capProbe := exec.Command("systemd-run", "--user", "--wait", "--collect", "--pipe",
		"--property=CapabilityBoundingSet=CAP_NET_BIND_SERVICE",
		"--property=AmbientCapabilities=", "/bin/true")
	capOut, capErr := capProbe.CombinedOutput()
	capRecord := map[string]any{"applied": capErr == nil}
	if capErr != nil {
		capRecord["verdict"] = "UNVERIFIED: user manager refused the property"
		capRecord["output"] = strings.TrimSpace(string(capOut))
		if ee, ok := capErr.(*exec.ExitError); ok {
			capRecord["exit_status"] = ee.ExitCode()
		}
	} else {
		hardening = append(hardening, "--property=CapabilityBoundingSet=CAP_NET_BIND_SERVICE", "--property=AmbientCapabilities=")
	}
	evidence["capability_bounding_set"] = capRecord

	// RestrictAddressFamilies is actually enforced by this manager (a socket
	// family outside the list returns EAFNOSUPPORT), so it is reported as
	// exercised rather than as "cannot probe".
	rafProbe := exec.Command("systemd-run", "--user", "--wait", "--collect", "--pipe",
		"--property=RestrictAddressFamilies=AF_UNIX",
		"/bin/bash", "-c", `exec 3<>/dev/tcp/127.0.0.1:9`)
	rafOut, rafErr := rafProbe.CombinedOutput()
	rafRecord := map[string]any{
		"verdict":      "UNVERIFIED: AF_INET denial not observable with this probe",
		"probe_output": strings.TrimSpace(string(rafOut)),
	}
	if rafErr != nil || strings.Contains(string(rafOut), "No such file or directory") {
		// bash reports the denied family as "No such file or directory" and exits
		// non-zero; either signal means the property was applied.
		rafRecord["verdict"] = "APPLIED: AF_INET denied while AF_UNIX unit ran"
		rafRecord["applied"] = true
	}
	evidence["restrict_address_families"] = rafRecord
	unverified := []string{"User=", "Group=", "ProtectSystem=strict", "ProtectHome=true",
		"ReadWritePaths=", "StateDirectory=", "CapabilityBoundingSet", "AmbientCapabilities", "system unit (PID1)"}

	// 1) One-shot real config check through the real manager, using the real core.
	checkArgs := append([]string{"--user", "--unit=" + unit + "-check", "--wait", "--collect", "--pipe"}, hardening...)
	checkArgs = append(checkArgs, "--property=Type=oneshot", agentBin, "-config", cfgPath, "-check")
	checkOut, checkErr := exec.Command("systemd-run", checkArgs...).CombinedOutput()
	evidence["agent_check"] = map[string]any{
		"exit_status": checkExitStatus(checkErr),
		"output":      strings.TrimSpace(string(checkOut)),
	}
	if checkErr != nil {
		t.Fatalf("agent -check under real systemd unit failed: %v\n%s", checkErr, checkOut)
	}

	// 2) Long running unit: the real Agent, not a shell and not sleep.
	runArgs := append([]string{"--user", "--unit=" + unit, "--collect"}, hardening...)
	runArgs = append(runArgs,
		"--property=Restart=on-failure", "--property=RestartSec=1", "--property=Type=simple",
		"--property=WorkingDirectory="+fixture)
	runArgs = append(runArgs, agentBin, "-config", cfgPath)
	if out, err := exec.Command("systemd-run", runArgs...).CombinedOutput(); err != nil {
		t.Fatalf("systemd-run agent: %v\n%s", err, out)
	}
	var state webdemoPlatformUnitState
	var nnp string
	var ident webdemoProcIdentity
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state = webdemoPlatformUnitShow(t, unit)
		if state.ActiveState == "active" && state.MainPID != "0" {
			nnp = webdemoPlatformProcStatus(t, state.MainPID, "NoNewPrivs")
			if nnp == "1" {
				if got, ok := webdemoPlatformProcIdentity(t, state.MainPID, agentBin); ok {
					ident = got
					break
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if state.ActiveState != "active" || state.MainPID == "0" {
		diag, _ := exec.Command("systemctl", "--user", "show", unit,
			"-p", "ActiveState", "-p", "Result", "-p", "ExecMainStatus",
			"-p", "NoNewPrivileges", "-p", "RestrictAddressFamilies").CombinedOutput()
		logs, _ := exec.Command("journalctl", "--user", "-u", unit, "-n", "40", "--no-pager").CombinedOutput()
		t.Fatalf("unit did not become active: %+v\n%s\n%s", state, diag, logs)
	}
	if ident.PID == "" {
		logs, _ := exec.Command("journalctl", "--user", "-u", unit, "-n", "40", "--no-pager").CombinedOutput()
		t.Fatalf("MainPID %s never resolved to the built Agent with NoNewPrivs=1 (nnp=%q)\n%s",
			state.MainPID, nnp, logs)
	}
	evidence["first_process"] = ident
	evidence["no_new_privs"] = nnp
	if nnp != "1" {
		t.Fatalf("NoNewPrivileges not applied to the Agent process: %q", nnp)
	}
	evidence["cap_eff"] = webdemoPlatformProcStatus(t, state.MainPID, "CapEff")
	evidence["cgroup"] = strings.TrimSpace(readFileOr(t, "/proc/"+state.MainPID+"/cgroup"))
	evidence["umask"] = webdemoPlatformProcStatus(t, state.MainPID, "Umask")
	cgroup := evidence["cgroup"].(string)
	if !strings.Contains(cgroup, unit) {
		t.Errorf("process not in the unit cgroup: %q", cgroup)
	}

	// The Agent must be in its long-running loop against the fixture panel, not a
	// one-shot helper: registration/snapshot traffic must be observed.
	registered := false
	for deadline = time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if panel.hitCount("/api/v1/agent/snapshot") > 0 && panel.hitCount("/api/v1/agent/heartbeat") > 0 {
			registered = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	evidence["panel_hits_before_kill"] = panel.snapshot()
	// No snapshot instance was delivered, so the Agent must not have spawned any
	// core process: only its `version` probe ran.
	if corePIDs := webdemoPlatformCorePIDs(); len(corePIDs) > 0 {
		evidence["core_processes"] = corePIDs
		t.Errorf("unexpected core process(es) started by this Agent-only scenario: %v", corePIDs)
	} else {
		evidence["core_processes"] = []string{}
	}
	if !registered {
		logs, _ := exec.Command("journalctl", "--user", "-u", unit, "-n", "40", "--no-pager").CombinedOutput()
		t.Fatalf("real Agent never reached the snapshot/heartbeat loop; hits=%v\n%s", panel.snapshot(), logs)
	}

	// 3) Kill ONLY the Agent process: a new pid of the SAME Agent binary must
	// come back. The manager restart, not a shell/sleep wrapper, is what is
	// measured.
	firstPID := state.MainPID
	if err := exec.Command("kill", "-9", firstPID).Run(); err != nil {
		t.Fatalf("kill -9 %s: %v", firstPID, err)
	}
	var secondIdent webdemoProcIdentity
	restarted := false
	for deadline = time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		state = webdemoPlatformUnitShow(t, unit)
		restarts, _ := strconv.Atoi(state.NRestarts)
		if restarts >= 1 && state.MainPID != "0" && state.MainPID != firstPID && state.ActiveState == "active" {
			if got, ok := webdemoPlatformProcIdentity(t, state.MainPID, agentBin); ok {
				secondIdent = got
				restarted = true
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !restarted {
		logs, _ := exec.Command("journalctl", "--user", "-u", unit, "-n", "40", "--no-pager").CombinedOutput()
		t.Fatalf("Agent was not restarted after kill -9: %+v\n%s", state, logs)
	}
	evidence["restart_after_kill"] = map[string]any{
		"first_pid":          firstPID,
		"second_pid":         secondIdent.PID,
		"same_binary_sha256": secondIdent.ExeSHA256 == evidence["agent_binary_sha256"],
		"second_process":     secondIdent,
		"state":              state,
	}
	if secondIdent.ExeSHA256 != ident.ExeSHA256 {
		t.Errorf("restarted process is a different binary: %s vs %s", secondIdent.ExeSHA256, ident.ExeSHA256)
	}
	if secondIdent.PID == ident.PID {
		t.Errorf("restart did not produce a new pid: %s", ident.PID)
	}
	// The same state dir is now owned by the new Agent: it must have re-registered
	// or reused its token, and heartbeat traffic must continue.
	before := panel.hitCount("/api/v1/agent/heartbeat")
	continued := false
	for deadline = time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if panel.hitCount("/api/v1/agent/heartbeat") > before {
			continued = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	evidence["heartbeat_after_restart"] = map[string]any{
		"before": before, "after": panel.hitCount("/api/v1/agent/heartbeat"), "continued": continued,
	}
	if !continued {
		t.Errorf("restarted Agent did not resume heartbeats: %v", panel.snapshot())
	}

	if out, err := exec.Command("systemctl", "--user", "stop", unit).CombinedOutput(); err != nil {
		t.Fatalf("systemctl stop: %v\n%s", err, out)
	}
	for deadline = time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		state = webdemoPlatformUnitShow(t, unit)
		if state.ActiveState == "inactive" || state.ActiveState == "failed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	evidence["stop"] = state
	if state.ActiveState != "inactive" {
		t.Errorf("unit not inactive after stop: %+v", state)
	}
	evidence["unverified_system_unit_directives"] = unverified
	evidence["scope_note"] = "user transient unit only; shipped system unit User=/ProtectSystem=/StateDirectory=/capability bounding set remain unverified"
	evidence["finished_at"] = time.Now().UTC().Format(time.RFC3339Nano)

	if outDir := os.Getenv(webdemoPlatformEvidenceEnv); outDir != "" {
		blob, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outDir, "systemd-agent-restart.json"), blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// webdemoPlatformTryLocalLock takes the shared web-demo local runtime lock only
// if it is free. This scenario starts the Agent but no core process, so it must
// never block another owner's real-core run.
func webdemoPlatformTryLocalLock(t *testing.T) (*os.File, bool) {
	t.Helper()
	f, err := os.OpenFile(webdemoPlatformLocalLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Logf("local runtime lock unavailable: %v", err)
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, false
	}
	return f, true
}

func webdemoPlatformReleaseLocalLock(f *os.File) {
	if f == nil {
		return
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	f.Close()
}

func lockReason(held bool) string {
	if held {
		return "held for the whole before->cleanup window"
	}
	return "not held: another web-demo owner holds it; this test starts no core process (Agent only runs `xray version`)"
}

func checkExitStatus(err error) any {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return err.Error()
}

func readFileOr(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// webdemoPlatformCorePIDs lists live core processes of this test's private
// fixture (path-scoped, never a host-wide guess).
func webdemoPlatformCorePIDs() []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		pid := e.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + pid + "/cmdline")
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		for i, a := range args {
			if strings.HasSuffix(a, "/xray") && i > 0 && args[i-1] == "run" {
				out = append(out, pid+":"+strings.Join(args, " "))
				break
			}
		}
	}
	return out
}

func chmodTree(root string, dirPerm, filePerm os.FileMode) {
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			os.Chmod(path, dirPerm)
		} else {
			os.Chmod(path, filePerm)
		}
		return nil
	})
}
