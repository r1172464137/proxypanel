package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests stub only privileged installation operations. They execute the
// real install_unit/render_unit logic and inspect the actual unit it would
// install, without touching /etc, creating accounts, downloading binaries or
// invoking systemd.
func deploymentLibrary(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../deploy/deploy-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	// The fixed script guards main() itself, so sourcing is already inert. Strip
	// the unguarded pre-fix invocation as well, so the same test can be run
	// against the parent commit to reproduce the original failures.
	text = strings.Replace(text, "\nmain \"$@\"\n", "\n", 1)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy-agent.sh"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	// Copy the release-managed unit next to the script: the pre-fix installer
	// preferred it, which is exactly the defect under test.
	unit, err := os.ReadFile("../../deploy/proxypanel-agent.service")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proxypanel-agent.service"), unit, 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "deploy-agent.sh")
}

// runInstaller sources the script with the privileged steps stubbed and runs
// install_unit, returning the unit text that would land in /etc/systemd/system.
func runInstaller(t *testing.T, state, ca string) string {
	t.Helper()
	script := deploymentLibrary(t)
	out := filepath.Join(t.TempDir(), "unit")
	cmd := exec.Command("/bin/bash", "-c", `
set -eu
source "$LIBRARY"
ARG_STATE_DIR="$TEST_STATE"
ARG_PANEL_CA="$TEST_CA"
install() { cp -- "${@: -2:1}" "$UNIT_OUTPUT"; }
systemctl() { :; }
install_unit
`)
	cmd.Env = append(os.Environ(),
		"LIBRARY="+script, "UNIT_OUTPUT="+out, "TEST_STATE="+state, "TEST_CA="+ca)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installer: %v: %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStandaloneAgentUnitMatchesInstalledBinary(t *testing.T) {
	state := "/var/lib/proxypanel-agent-custom"
	unit := runInstaller(t, state, "/synthetic/ca.crt")
	for _, expected := range []string{
		"WorkingDirectory=/opt/proxypanel/agent",
		"ExecStart=/opt/proxypanel/agent/agent -config /etc/proxypanel/agent.json",
		"User=proxypanel-agent",
		"ReadWritePaths=" + state,
		"Environment=SSL_CERT_FILE=/usr/local/share/ca-certificates/proxypanel-panel.crt",
	} {
		if !strings.Contains(unit, expected) {
			t.Errorf("unit does not match standalone install: missing %q\n%s", expected, unit)
		}
	}
	if strings.Contains(unit, "/opt/proxypanel/current") {
		t.Fatal("copied release-manager unit into standalone installation")
	}
	// The release unit delegates state creation to systemd via StateDirectory=,
	// which would always create /var/lib/proxypanel-agent and ignore a custom
	// --state-dir. The standalone unit must not inherit that.
	if strings.Contains(unit, "StateDirectory") {
		t.Fatalf("copied StateDirectory= from the release unit:\n%s", unit)
	}
}

// The CA override must only appear when a private CA was actually supplied;
// otherwise SSL_CERT_FILE points at a nonexistent file on every install.
func TestStandaloneAgentUnitOmitsCAOverrideWithoutPrivateCA(t *testing.T) {
	state := "/var/lib/proxypanel-agent-custom"
	unit := runInstaller(t, state, "")
	if strings.Contains(unit, "SSL_CERT_FILE") {
		t.Fatalf("unit sets SSL_CERT_FILE without --panel-ca:\n%s", unit)
	}
	if !strings.Contains(unit, "ReadWritePaths="+state) {
		t.Fatalf("custom state directory missing:\n%s", unit)
	}
}

// A trailing slash is a normal way to type a directory but used to defeat the
// exact-match deny list: /etc/ produced ReadWritePaths=/etc/ in the unit.
func TestStandaloneAgentRejectsUnsafeStatePath(t *testing.T) {
	for _, bad := range []string{
		"/",
		"/var",
		"/var/lib",
		"/var/lib/",
		"/etc",
		"/etc/",
		"/etc/systemd/system",
		"/usr/local/bin",
		"/boot/loader",
		"/dev/shm/x",
		"/proc/x",
		"/tmp",
		"/tmp/state",
		"/home/state",
		"/root/state",
		"/opt/proxypanel/agent",
		"/var/log/x",
		"relative/state",
		"/tmp/a/../b",
		"/tmp/state with spaces",
		"/tmp/state%h",
	} {
		t.Run(bad, func(t *testing.T) {
			script := deploymentLibrary(t)
			cmd := exec.Command("/bin/bash", "-c", `source "$LIBRARY"; validate_state_dir "$BAD_STATE"`)
			cmd.Env = append(os.Environ(), "LIBRARY="+script, "BAD_STATE="+bad)
			out, err := cmd.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "状态") {
				t.Fatalf("expected state validation refusal, not a missing command: path=%q err=%v output=%s", bad, err, out)
			}
		})
	}
}

func TestStandaloneAgentAcceptsNormalStatePath(t *testing.T) {
	for _, ok := range []string{
		"/var/lib/proxypanel-agent",
		"/srv/proxypanel-agent",
		"/data/proxypanel-agent",
	} {
		t.Run(ok, func(t *testing.T) {
			script := deploymentLibrary(t)
			cmd := exec.Command("/bin/bash", "-c", `source "$LIBRARY"; validate_state_dir "$BAD_STATE"`)
			cmd.Env = append(os.Environ(), "LIBRARY="+script, "BAD_STATE="+ok)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("normal state path rejected: %q (%s)", ok, b)
			}
		})
	}
}

// A trailing slash must be normalized rather than silently changing the unit
// path, and must not leave a double slash in ReadWritePaths.
func TestStandaloneAgentNormalizesTrailingSlashState(t *testing.T) {
	state := "/var/lib/proxypanel-agent-custom/"
	unit := runInstaller(t, state, "")
	want := "ReadWritePaths=/var/lib/proxypanel-agent-custom\n"
	if !strings.Contains(unit, want) {
		t.Fatalf("trailing slash not normalized, want %q:\n%s", want, unit)
	}
}

// Preserving an existing config must also leave the existing unit and trust
// store alone: the preserved config may point at a different state directory
// or CA than the flags being passed on this run.
func TestStandaloneAgentKeepConfigSkipsUnitAndCA(t *testing.T) {
	script := deploymentLibrary(t)
	cmd := exec.Command("/bin/bash", "-c", `
set -eu
source "$LIBRARY"
require_root() { :; }
detect_arch() { ARCH=x86_64; }
require_systemd() { :; }
check_tools() { :; }
check_disk() { :; }
collect_inputs() { :; }
create_account() { :; }
install_xray() { :; }
install_agent_binary() { :; }
prepare_state() { :; }
write_config() { ARG_KEEP_CONFIG=1; }
install_panel_ca() { echo CA_INSTALLED; exit 99; }
install_unit() { echo UNIT_INSTALLED; exit 99; }
run_config_check() { echo CONFIG_CHECKED; exit 99; }
start_service() { echo SERVICE_STARTED; exit 99; }
systemctl() { :; }
report() { :; }
main
`)
	cmd.Env = append(os.Environ(), "LIBRARY="+script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("keep-config run failed: %v: %s", err, out)
	}
	for _, forbidden := range []string{"CA_INSTALLED", "UNIT_INSTALLED", "CONFIG_CHECKED", "SERVICE_STARTED"} {
		if strings.Contains(string(out), forbidden) {
			t.Fatalf("--keep-config path ran %s:\n%s", forbidden, out)
		}
	}
}

// Stdin execution must reach the real entry guard without BASH_SOURCE.
// Stub only its installation dispatch so this test cannot modify the host.
func TestStandaloneAgentRunsWithoutBashSource(t *testing.T) {
	script := deploymentLibrary(t)
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	const entry = "\n  main \"$@\"\n"
	const guard = "${BASH_SOURCE[0]:-$0}"
	text := string(raw)
	if strings.Count(text, entry) != 1 || strings.Count(text, guard) != 1 {
		t.Fatal("expected one real guarded installation dispatch")
	}
	const marker = "STDIN_ENTRY_REACHED"
	text = strings.Replace(text, entry, "\n  printf '%s\\n' '"+marker+"'\n", 1)
	for _, tc := range []struct {
		name        string
		brokenGuard bool
	}{
		{"production-guard", false},
		{"missing-fallback-control", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := text
			if tc.brokenGuard {
				input = strings.Replace(input, guard, "${BASH_SOURCE[0]}", 1)
			}
			cmd := exec.Command("/bin/bash", "-s", "--")
			cmd.Stdin = strings.NewReader(input)
			out, err := cmd.CombinedOutput()
			if tc.brokenGuard {
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "BASH_SOURCE") || strings.Contains(string(out), marker) {
					t.Fatalf("missing guard fallback was not detected: err=%v output=%s", err, out)
				}
				return
			}
			if err != nil || !strings.Contains(string(out), marker) {
				t.Fatalf("stdin did not reach guarded entry: err=%v output=%s", err, out)
			}
		})
	}
}
