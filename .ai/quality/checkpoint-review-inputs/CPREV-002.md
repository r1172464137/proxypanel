# CPREV-002 frozen input

Target: 3435f1d..fe4bf8e. No current WIP included.

## Complete relevant diff
```diff
diff --git a/scripts/backend-test.py b/scripts/backend-test.py
new file mode 100644
index 0000000..0296d89
--- /dev/null
+++ b/scripts/backend-test.py
@@ -0,0 +1,229 @@
+#!/usr/bin/env python3
+"""Run registered Go test selections, checking discovery and execution evidence."""
+
+import argparse
+import datetime as dt
+import hashlib
+import json
+import os
+from pathlib import Path
+import re
+import shutil
+import subprocess
+import sys
+import uuid
+
+ROOT = Path(__file__).resolve().parent.parent
+MANIFEST = ROOT / "scripts/backend-test-manifest.json"
+EVIDENCE = ROOT / ".ai/evidence/backend-test"
+
+
+def parse_args():
+    parser = argparse.ArgumentParser(description=__doc__)
+    parser.add_argument("--list", action="store_true", help="show registered selections without executing Go")
+    parser.add_argument("--check-manifest", action="store_true", help="compile/discover tests without running selected cases")
+    parser.add_argument("--json", action="store_true", help="emit one JSON summary")
+    parser.add_argument("--with-docker", action="store_true", help="allow explicitly registered Docker selections")
+    parser.add_argument("filters", nargs="*", metavar="package[/keyword]")
+    return parser.parse_args()
+
+
+def load_selections(filters):
+    entries = json.loads(MANIFEST.read_text())
+    if not isinstance(entries, list) or not entries:
+        raise ValueError("test manifest must be a nonempty array")
+    ids = set()
+    for entry in entries:
+        if not isinstance(entry, dict):
+            raise ValueError("manifest entry must be an object")
+        name = entry.get("id")
+        if not isinstance(name, str) or not name or name in ids:
+            raise ValueError("selection IDs must be nonempty and unique")
+        ids.add(name)
+        for key in ("packages", "expected_tests", "stories", "requires"):
+            values = entry.get(key)
+            if not isinstance(values, list) or any(not isinstance(x, str) or not x for x in values):
+                raise ValueError(f"{name}: invalid {key}")
+        if not entry["packages"] or not entry["expected_tests"]:
+            raise ValueError(f"{name}: packages and expected_tests cannot be empty")
+        if len(entry["expected_tests"]) != len(set(entry["expected_tests"])):
+            raise ValueError(f"{name}: duplicate expected test")
+        if any(not re.fullmatch(r"\./(?:internal|cmd|tests)(?:/[A-Za-z0-9_./-]+)?", x)
+               or ".." in x.split("/") for x in entry["packages"]):
+            raise ValueError(f"{name}: invalid Go package scope")
+        if not isinstance(entry.get("run"), str) or not entry["run"]:
+            raise ValueError(f"{name}: empty test expression")
+        unknown = set(entry["requires"]) - {"postgres", "redis", "xray", "docker", "unix"}
+        if unknown:
+            raise ValueError(f"{name}: unknown requirements: {sorted(unknown)}")
+        timeout = entry.get("timeout_seconds", 120)
+        if type(timeout) is not int or timeout < 1:
+            raise ValueError(f"{name}: invalid timeout")
+    if not filters:
+        return entries
+    selected = []
+    for needle in filters:
+        matches = [e for e in entries if needle.casefold() in e["id"].casefold()]
+        if not matches:
+            raise ValueError(f"no registered selection matches {needle!r}")
+        for entry in matches:
+            if entry not in selected:
+                selected.append(entry)
+    return selected
+
+
+def command(entry, discover=False):
+    if discover:
+        return ["go", "test", "-list", ".*", *entry["packages"]]
+    return ["go", "test", "-json", "-count=1", "-p=1",
+            f"-timeout={entry.get('timeout_seconds', 120)}s", "-run", entry["run"], *entry["packages"]]
+
+
+def input_fingerprint():
+    result = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
+                            cwd=ROOT, capture_output=True, check=True)
+    names = set(result.stdout.decode().split("\0"))
+    names.update({"scripts/backend-test.py", "scripts/backend-test-manifest.json"})
+    files = {}
+    for name in sorted(names):
+        if not name or name.startswith((".ai/", ".memory/", "web/")):
+            continue
+        if not (name.endswith((".go", ".sql", ".sh")) or name in
+                {"go.mod", "go.sum", "scripts/backend-test.py", "scripts/backend-test-manifest.json"}):
+            continue
+        path = ROOT / name
+        files[name] = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else "deleted"
+    digest = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
+    return {"sha256": digest, "files": files}
+
+
+def environment_missing(entry, allow_docker):
+    missing = []
+    for requirement in entry["requires"]:
+        if requirement == "unix" and os.name != "posix":
+            missing.append("Unix test environment")
+        elif requirement == "postgres":
+            if not os.environ.get("PANEL_TEST_DATABASE_URL") or os.environ.get("PANEL_TEST_DB_ISOLATED") != "1":
+                missing.append("isolated PANEL_TEST_DATABASE_URL and PANEL_TEST_DB_ISOLATED=1")
+        elif requirement == "redis" and not os.environ.get("PANEL_TEST_REDIS_URL"):
+            missing.append("PANEL_TEST_REDIS_URL")
+        elif requirement == "xray":
+            binary = os.environ.get("XRAY_BIN", "")
+            if not binary or not os.access(binary, os.X_OK):
+                missing.append("executable XRAY_BIN")
+        elif requirement == "docker" and not allow_docker:
+            missing.append("explicit --with-docker permission")
+    return missing
+
+
+def run_logged(args, path, timeout):
+    started = dt.datetime.now(dt.timezone.utc).isoformat()
+    try:
+        process = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout,
+                                 env={**os.environ, "PANEL_BASH": os.environ.get("PANEL_BASH", "/bin/bash")})
+        output = process.stdout + process.stderr
+        code = process.returncode
+    except subprocess.TimeoutExpired as exc:
+        output = ""
+        for value in (exc.stdout, exc.stderr):
+            if value:
+                output += value.decode(errors="replace") if isinstance(value, bytes) else value
+        output += "\nRunner wall timeout exceeded\n"
+        code = 124
+    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
+    with os.fdopen(fd, "w") as log:
+        log.write(output)
+    return {"command": args, "cwd": str(ROOT), "exit_code": code, "started_at": started,
+            "ended_at": dt.datetime.now(dt.timezone.utc).isoformat(), "log": str(path)}, output
+
+
+def check_go():
+    if not shutil.which("go"):
+        raise ValueError("go is not on PATH; load the project toolchain first")
+    version = subprocess.run(["go", "version"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
+    wanted = re.search(r"^go (\d+)\.(\d+)(?:\.(\d+))?", (ROOT / "go.mod").read_text(), re.M)
+    actual = re.search(r"go(\d+)\.(\d+)(?:\.(\d+))?", version)
+    if not wanted or not actual or tuple(int(x or 0) for x in actual.groups()) < tuple(int(x or 0) for x in wanted.groups()):
+        raise ValueError("installed Go does not satisfy go.mod")
+    return version
+
+
+def main():
+    args = parse_args()
+    selections = load_selections(args.filters)
+    summary = {"status": "planned", "coverage": "registered_selections_only", "repository_full_suite": False,
+               "results": [{**entry, "command": command(entry)} for entry in selections]}
+    if args.list:
+        print(json.dumps(summary, ensure_ascii=False, indent=2))
+        return 0
+    version = check_go()
+    EVIDENCE.mkdir(parents=True, exist_ok=True)
+    run_dir = EVIDENCE / (dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
+    run_dir.mkdir(mode=0o700)
+    before = input_fingerprint()
+    results = []
+    for number, entry in enumerate(selections):
+        result = {"id": entry["id"], "expected_tests": entry["expected_tests"], "stories": entry["stories"]}
+        results.append(result)
+        if not args.check_manifest:
+            missing = environment_missing(entry, args.with_docker)
+            if missing:
+                result.update(status="not_run", reason=missing)
+                continue
+        discovery, text = run_logged(command(entry, discover=True), run_dir / f"{number}-discovery.log", 180)
+        result["discovery"] = discovery
+        found = {line.strip() for line in text.splitlines() if re.fullmatch(r"Test\w+", line.strip())}
+        missing = set(entry["expected_tests"]) - found
+        if discovery["exit_code"] != 0 or missing:
+            result.update(status="failed", reason="test discovery failed or expected cases missing", missing_tests=sorted(missing))
+            continue
+        if args.check_manifest:
+            result["status"] = "manifest_valid"
+            continue
+        run, text = run_logged(command(entry), run_dir / f"{number}-tests.jsonl", entry.get("timeout_seconds", 120) + 60)
+        result["execution"] = run
+        events = []
+        for line in text.splitlines():
+            try:
+                event = json.loads(line)
+            except ValueError:
+                continue
+            if isinstance(event, dict) and event.get("Test"):
+                events.append(event)
+        ran = {e["Test"] for e in events if e.get("Action") == "run"}
+        passed = {e["Test"] for e in events if e.get("Action") == "pass"}
+        failed = {e["Test"] for e in events if e.get("Action") == "fail"}
+        skipped = {e["Test"] for e in events if e.get("Action") == "skip"}
+        missing = set(entry["expected_tests"]) - ran
+        result.update(ran=sorted(ran), passed=sorted(passed), failed=sorted(failed), skipped=sorted(skipped), missing_tests=sorted(missing))
+        if run["exit_code"] != 0 or failed or missing:
+            result["status"] = "failed"
+        elif skipped or not set(entry["expected_tests"]) <= passed:
+            result["status"] = "incomplete"
+        else:
+            result["status"] = "passed"
+    after = input_fingerprint()
+    stable = before == after
+    good = {"manifest_valid"} if args.check_manifest else {"passed"}
+    complete = stable and all(r["status"] in good for r in results)
+    summary = {"status": "passed" if complete else "failed", "mode": "manifest_check" if args.check_manifest else "test",
+               "coverage": "registered_selections_only", "repository_full_suite": False, "go_version": version,
+               "inputs_stable": stable, "inputs": before, "results": results, "report": str(run_dir / "result.json")}
+    fd = os.open(run_dir / "result.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
+    with os.fdopen(fd, "w") as output:
+        json.dump(summary, output, ensure_ascii=False, indent=2)
+    if args.json:
+        print(json.dumps(summary, ensure_ascii=False))
+    else:
+        for result in results:
+            print(f"{result['id']}: {result['status']}; ran={len(result.get('ran', []))}, skipped={len(result.get('skipped', []))}")
+        print("Evidence:", summary["report"])
+    return 0 if complete else 1
+
+
+if __name__ == "__main__":
+    try:
+        sys.exit(main())
+    except (ValueError, OSError, subprocess.SubprocessError) as error:
+        print(json.dumps({"status": "error", "error": str(error)}), file=sys.stderr)
+        sys.exit(2)
diff --git a/tests/e2e/deploy_agent_test.go b/tests/e2e/deploy_agent_test.go
new file mode 100644
index 0000000..62239a2
--- /dev/null
+++ b/tests/e2e/deploy_agent_test.go
@@ -0,0 +1,231 @@
+package e2e
+
+import (
+	"os"
+	"os/exec"
+	"path/filepath"
+	"strings"
+	"testing"
+)
+
+// These tests stub only privileged installation operations. They execute the
+// real install_unit/render_unit logic and inspect the actual unit it would
+// install, without touching /etc, creating accounts, downloading binaries or
+// invoking systemd.
+func deploymentLibrary(t *testing.T) string {
+	t.Helper()
+	b, err := os.ReadFile("../../deploy/deploy-agent.sh")
+	if err != nil {
+		t.Fatal(err)
+	}
+	text := string(b)
+	// The fixed script guards main() itself, so sourcing is already inert. Strip
+	// the unguarded pre-fix invocation as well, so the same test can be run
+	// against the parent commit to reproduce the original failures.
+	text = strings.Replace(text, "\nmain \"$@\"\n", "\n", 1)
+	dir := t.TempDir()
+	if err := os.WriteFile(filepath.Join(dir, "deploy-agent.sh"), []byte(text), 0600); err != nil {
+		t.Fatal(err)
+	}
+	// Copy the release-managed unit next to the script: the pre-fix installer
+	// preferred it, which is exactly the defect under test.
+	unit, err := os.ReadFile("../../deploy/proxypanel-agent.service")
+	if err != nil {
+		t.Fatal(err)
+	}
+	if err := os.WriteFile(filepath.Join(dir, "proxypanel-agent.service"), unit, 0600); err != nil {
+		t.Fatal(err)
+	}
+	return filepath.Join(dir, "deploy-agent.sh")
+}
+
+// runInstaller sources the script with the privileged steps stubbed and runs
+// install_unit, returning the unit text that would land in /etc/systemd/system.
+func runInstaller(t *testing.T, state, ca string) string {
+	t.Helper()
+	script := deploymentLibrary(t)
+	out := filepath.Join(t.TempDir(), "unit")
+	cmd := exec.Command("/bin/bash", "-c", `
+set -eu
+source "$LIBRARY"
+ARG_STATE_DIR="$TEST_STATE"
+ARG_PANEL_CA="$TEST_CA"
+install() { cp -- "${@: -2:1}" "$UNIT_OUTPUT"; }
+systemctl() { :; }
+install_unit
+`)
+	cmd.Env = append(os.Environ(),
+		"LIBRARY="+script, "UNIT_OUTPUT="+out, "TEST_STATE="+state, "TEST_CA="+ca)
+	if b, err := cmd.CombinedOutput(); err != nil {
+		t.Fatalf("installer: %v: %s", err, b)
+	}
+	b, err := os.ReadFile(out)
+	if err != nil {
+		t.Fatal(err)
+	}
+	return string(b)
+}
+
+func TestStandaloneAgentUnitMatchesInstalledBinary(t *testing.T) {
+	state := "/var/lib/proxypanel-agent-custom"
+	unit := runInstaller(t, state, "/synthetic/ca.crt")
+	for _, expected := range []string{
+		"WorkingDirectory=/opt/proxypanel/agent",
+		"ExecStart=/opt/proxypanel/agent/agent -config /etc/proxypanel/agent.json",
+		"User=proxypanel-agent",
+		"ReadWritePaths=" + state,
+		"Environment=SSL_CERT_FILE=/usr/local/share/ca-certificates/proxypanel-panel.crt",
+	} {
+		if !strings.Contains(unit, expected) {
+			t.Errorf("unit does not match standalone install: missing %q\n%s", expected, unit)
+		}
+	}
+	if strings.Contains(unit, "/opt/proxypanel/current") {
+		t.Fatal("copied release-manager unit into standalone installation")
+	}
+	// The release unit delegates state creation to systemd via StateDirectory=,
+	// which would always create /var/lib/proxypanel-agent and ignore a custom
+	// --state-dir. The standalone unit must not inherit that.
+	if strings.Contains(unit, "StateDirectory") {
+		t.Fatalf("copied StateDirectory= from the release unit:\n%s", unit)
+	}
+}
+
+// The CA override must only appear when a private CA was actually supplied;
+// otherwise SSL_CERT_FILE points at a nonexistent file on every install.
+func TestStandaloneAgentUnitOmitsCAOverrideWithoutPrivateCA(t *testing.T) {
+	state := "/var/lib/proxypanel-agent-custom"
+	unit := runInstaller(t, state, "")
+	if strings.Contains(unit, "SSL_CERT_FILE") {
+		t.Fatalf("unit sets SSL_CERT_FILE without --panel-ca:\n%s", unit)
+	}
+	if !strings.Contains(unit, "ReadWritePaths="+state) {
+		t.Fatalf("custom state directory missing:\n%s", unit)
+	}
+}
+
+// A trailing slash is a normal way to type a directory but used to defeat the
+// exact-match deny list: /etc/ produced ReadWritePaths=/etc/ in the unit.
+func TestStandaloneAgentRejectsUnsafeStatePath(t *testing.T) {
+	for _, bad := range []string{
+		"/",
+		"/var",
+		"/var/lib",
+		"/var/lib/",
+		"/etc",
+		"/etc/",
+		"/etc/systemd/system",
+		"/usr/local/bin",
+		"/boot/loader",
+		"/dev/shm/x",
+		"/proc/x",
+		"/tmp",
+		"/tmp/state",
+		"/home/state",
+		"/root/state",
+		"/opt/proxypanel/agent",
+		"/var/log/x",
+		"relative/state",
+		"/tmp/a/../b",
+		"/tmp/state with spaces",
+		"/tmp/state%h",
+	} {
+		t.Run(bad, func(t *testing.T) {
+			script := deploymentLibrary(t)
+			cmd := exec.Command("/bin/bash", "-c", `source "$LIBRARY"; validate_state_dir "$BAD_STATE"`)
+			cmd.Env = append(os.Environ(), "LIBRARY="+script, "BAD_STATE="+bad)
+			out, err := cmd.CombinedOutput()
+			exit, ok := err.(*exec.ExitError)
+			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "状态") {
+				t.Fatalf("expected state validation refusal, not a missing command: path=%q err=%v output=%s", bad, err, out)
+			}
+		})
+	}
+}
+
+func TestStandaloneAgentAcceptsNormalStatePath(t *testing.T) {
+	for _, ok := range []string{
+		"/var/lib/proxypanel-agent",
+		"/srv/proxypanel-agent",
+		"/data/proxypanel-agent",
+	} {
+		t.Run(ok, func(t *testing.T) {
+			script := deploymentLibrary(t)
+			cmd := exec.Command("/bin/bash", "-c", `source "$LIBRARY"; validate_state_dir "$BAD_STATE"`)
+			cmd.Env = append(os.Environ(), "LIBRARY="+script, "BAD_STATE="+ok)
+			if b, err := cmd.CombinedOutput(); err != nil {
+				t.Fatalf("normal state path rejected: %q (%s)", ok, b)
+			}
+		})
+	}
+}
+
+// A trailing slash must be normalized rather than silently changing the unit
+// path, and must not leave a double slash in ReadWritePaths.
+func TestStandaloneAgentNormalizesTrailingSlashState(t *testing.T) {
+	state := "/var/lib/proxypanel-agent-custom/"
+	unit := runInstaller(t, state, "")
+	want := "ReadWritePaths=/var/lib/proxypanel-agent-custom\n"
+	if !strings.Contains(unit, want) {
+		t.Fatalf("trailing slash not normalized, want %q:\n%s", want, unit)
+	}
+}
+
+// Preserving an existing config must also leave the existing unit and trust
+// store alone: the preserved config may point at a different state directory
+// or CA than the flags being passed on this run.
+func TestStandaloneAgentKeepConfigSkipsUnitAndCA(t *testing.T) {
+	script := deploymentLibrary(t)
+	cmd := exec.Command("/bin/bash", "-c", `
+set -eu
+source "$LIBRARY"
+require_root() { :; }
+detect_arch() { ARCH=x86_64; }
+require_systemd() { :; }
+check_tools() { :; }
+check_disk() { :; }
+collect_inputs() { :; }
+create_account() { :; }
+install_xray() { :; }
+install_agent_binary() { :; }
+prepare_state() { :; }
+write_config() { ARG_KEEP_CONFIG=1; }
+install_panel_ca() { echo CA_INSTALLED; exit 99; }
+install_unit() { echo UNIT_INSTALLED; exit 99; }
+run_config_check() { echo CONFIG_CHECKED; exit 99; }
+start_service() { echo SERVICE_STARTED; exit 99; }
+systemctl() { :; }
+report() { :; }
+main
+`)
+	cmd.Env = append(os.Environ(), "LIBRARY="+script)
+	out, err := cmd.CombinedOutput()
+	if err != nil {
+		t.Fatalf("keep-config run failed: %v: %s", err, out)
+	}
+	for _, forbidden := range []string{"CA_INSTALLED", "UNIT_INSTALLED", "CONFIG_CHECKED", "SERVICE_STARTED"} {
+		if strings.Contains(string(out), forbidden) {
+			t.Fatalf("--keep-config path ran %s:\n%s", forbidden, out)
+		}
+	}
+}
+
+// `bash < deploy-agent.sh` (no file argument) must not abort on set -u before
+// reaching usage; BASH_SOURCE is unset in that mode.
+func TestStandaloneAgentRunsWithoutBashSource(t *testing.T) {
+	script := deploymentLibrary(t)
+	f, err := os.Open(script)
+	if err != nil {
+		t.Fatal(err)
+	}
+	defer f.Close()
+	cmd := exec.Command("/bin/bash", "-s", "--", "--help")
+	cmd.Stdin = f
+	out, err := cmd.CombinedOutput()
+	if err != nil {
+		t.Fatalf("stdin invocation failed: %v: %s", err, out)
+	}
+	if !strings.Contains(string(out), "用法") {
+		t.Fatalf("usage not printed:\n%s", out)
+	}
+}

```

## scripts/backend-test.py at fe4bf8e
```text
1: #!/usr/bin/env python3
2: """Run registered Go test selections, checking discovery and execution evidence."""
3: 
4: import argparse
5: import datetime as dt
6: import hashlib
7: import json
8: import os
9: from pathlib import Path
10: import re
11: import shutil
12: import subprocess
13: import sys
14: import uuid
15: 
16: ROOT = Path(__file__).resolve().parent.parent
17: MANIFEST = ROOT / "scripts/backend-test-manifest.json"
18: EVIDENCE = ROOT / ".ai/evidence/backend-test"
19: 
20: 
21: def parse_args():
22:     parser = argparse.ArgumentParser(description=__doc__)
23:     parser.add_argument("--list", action="store_true", help="show registered selections without executing Go")
24:     parser.add_argument("--check-manifest", action="store_true", help="compile/discover tests without running selected cases")
25:     parser.add_argument("--json", action="store_true", help="emit one JSON summary")
26:     parser.add_argument("--with-docker", action="store_true", help="allow explicitly registered Docker selections")
27:     parser.add_argument("filters", nargs="*", metavar="package[/keyword]")
28:     return parser.parse_args()
29: 
30: 
31: def load_selections(filters):
32:     entries = json.loads(MANIFEST.read_text())
33:     if not isinstance(entries, list) or not entries:
34:         raise ValueError("test manifest must be a nonempty array")
35:     ids = set()
36:     for entry in entries:
37:         if not isinstance(entry, dict):
38:             raise ValueError("manifest entry must be an object")
39:         name = entry.get("id")
40:         if not isinstance(name, str) or not name or name in ids:
41:             raise ValueError("selection IDs must be nonempty and unique")
42:         ids.add(name)
43:         for key in ("packages", "expected_tests", "stories", "requires"):
44:             values = entry.get(key)
45:             if not isinstance(values, list) or any(not isinstance(x, str) or not x for x in values):
46:                 raise ValueError(f"{name}: invalid {key}")
47:         if not entry["packages"] or not entry["expected_tests"]:
48:             raise ValueError(f"{name}: packages and expected_tests cannot be empty")
49:         if len(entry["expected_tests"]) != len(set(entry["expected_tests"])):
50:             raise ValueError(f"{name}: duplicate expected test")
51:         if any(not re.fullmatch(r"\./(?:internal|cmd|tests)(?:/[A-Za-z0-9_./-]+)?", x)
52:                or ".." in x.split("/") for x in entry["packages"]):
53:             raise ValueError(f"{name}: invalid Go package scope")
54:         if not isinstance(entry.get("run"), str) or not entry["run"]:
55:             raise ValueError(f"{name}: empty test expression")
56:         unknown = set(entry["requires"]) - {"postgres", "redis", "xray", "docker", "unix"}
57:         if unknown:
58:             raise ValueError(f"{name}: unknown requirements: {sorted(unknown)}")
59:         timeout = entry.get("timeout_seconds", 120)
60:         if type(timeout) is not int or timeout < 1:
61:             raise ValueError(f"{name}: invalid timeout")
62:     if not filters:
63:         return entries
64:     selected = []
65:     for needle in filters:
66:         matches = [e for e in entries if needle.casefold() in e["id"].casefold()]
67:         if not matches:
68:             raise ValueError(f"no registered selection matches {needle!r}")
69:         for entry in matches:
70:             if entry not in selected:
71:                 selected.append(entry)
72:     return selected
73: 
74: 
75: def command(entry, discover=False):
76:     if discover:
77:         return ["go", "test", "-list", ".*", *entry["packages"]]
78:     return ["go", "test", "-json", "-count=1", "-p=1",
79:             f"-timeout={entry.get('timeout_seconds', 120)}s", "-run", entry["run"], *entry["packages"]]
80: 
81: 
82: def input_fingerprint():
83:     result = subprocess.run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
84:                             cwd=ROOT, capture_output=True, check=True)
85:     names = set(result.stdout.decode().split("\0"))
86:     names.update({"scripts/backend-test.py", "scripts/backend-test-manifest.json"})
87:     files = {}
88:     for name in sorted(names):
89:         if not name or name.startswith((".ai/", ".memory/", "web/")):
90:             continue
91:         if not (name.endswith((".go", ".sql", ".sh")) or name in
92:                 {"go.mod", "go.sum", "scripts/backend-test.py", "scripts/backend-test-manifest.json"}):
93:             continue
94:         path = ROOT / name
95:         files[name] = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else "deleted"
96:     digest = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
97:     return {"sha256": digest, "files": files}
98: 
99: 
100: def environment_missing(entry, allow_docker):
101:     missing = []
102:     for requirement in entry["requires"]:
103:         if requirement == "unix" and os.name != "posix":
104:             missing.append("Unix test environment")
105:         elif requirement == "postgres":
106:             if not os.environ.get("PANEL_TEST_DATABASE_URL") or os.environ.get("PANEL_TEST_DB_ISOLATED") != "1":
107:                 missing.append("isolated PANEL_TEST_DATABASE_URL and PANEL_TEST_DB_ISOLATED=1")
108:         elif requirement == "redis" and not os.environ.get("PANEL_TEST_REDIS_URL"):
109:             missing.append("PANEL_TEST_REDIS_URL")
110:         elif requirement == "xray":
111:             binary = os.environ.get("XRAY_BIN", "")
112:             if not binary or not os.access(binary, os.X_OK):
113:                 missing.append("executable XRAY_BIN")
114:         elif requirement == "docker" and not allow_docker:
115:             missing.append("explicit --with-docker permission")
116:     return missing
117: 
118: 
119: def run_logged(args, path, timeout):
120:     started = dt.datetime.now(dt.timezone.utc).isoformat()
121:     try:
122:         process = subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout,
123:                                  env={**os.environ, "PANEL_BASH": os.environ.get("PANEL_BASH", "/bin/bash")})
124:         output = process.stdout + process.stderr
125:         code = process.returncode
126:     except subprocess.TimeoutExpired as exc:
127:         output = ""
128:         for value in (exc.stdout, exc.stderr):
129:             if value:
130:                 output += value.decode(errors="replace") if isinstance(value, bytes) else value
131:         output += "\nRunner wall timeout exceeded\n"
132:         code = 124
133:     fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
134:     with os.fdopen(fd, "w") as log:
135:         log.write(output)
136:     return {"command": args, "cwd": str(ROOT), "exit_code": code, "started_at": started,
137:             "ended_at": dt.datetime.now(dt.timezone.utc).isoformat(), "log": str(path)}, output
138: 
139: 
140: def check_go():
141:     if not shutil.which("go"):
142:         raise ValueError("go is not on PATH; load the project toolchain first")
143:     version = subprocess.run(["go", "version"], cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
144:     wanted = re.search(r"^go (\d+)\.(\d+)(?:\.(\d+))?", (ROOT / "go.mod").read_text(), re.M)
145:     actual = re.search(r"go(\d+)\.(\d+)(?:\.(\d+))?", version)
146:     if not wanted or not actual or tuple(int(x or 0) for x in actual.groups()) < tuple(int(x or 0) for x in wanted.groups()):
147:         raise ValueError("installed Go does not satisfy go.mod")
148:     return version
149: 
150: 
151: def main():
152:     args = parse_args()
153:     selections = load_selections(args.filters)
154:     summary = {"status": "planned", "coverage": "registered_selections_only", "repository_full_suite": False,
155:                "results": [{**entry, "command": command(entry)} for entry in selections]}
156:     if args.list:
157:         print(json.dumps(summary, ensure_ascii=False, indent=2))
158:         return 0
159:     version = check_go()
160:     EVIDENCE.mkdir(parents=True, exist_ok=True)
161:     run_dir = EVIDENCE / (dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8])
162:     run_dir.mkdir(mode=0o700)
163:     before = input_fingerprint()
164:     results = []
165:     for number, entry in enumerate(selections):
166:         result = {"id": entry["id"], "expected_tests": entry["expected_tests"], "stories": entry["stories"]}
167:         results.append(result)
168:         if not args.check_manifest:
169:             missing = environment_missing(entry, args.with_docker)
170:             if missing:
171:                 result.update(status="not_run", reason=missing)
172:                 continue
173:         discovery, text = run_logged(command(entry, discover=True), run_dir / f"{number}-discovery.log", 180)
174:         result["discovery"] = discovery
175:         found = {line.strip() for line in text.splitlines() if re.fullmatch(r"Test\w+", line.strip())}
176:         missing = set(entry["expected_tests"]) - found
177:         if discovery["exit_code"] != 0 or missing:
178:             result.update(status="failed", reason="test discovery failed or expected cases missing", missing_tests=sorted(missing))
179:             continue
180:         if args.check_manifest:
181:             result["status"] = "manifest_valid"
182:             continue
183:         run, text = run_logged(command(entry), run_dir / f"{number}-tests.jsonl", entry.get("timeout_seconds", 120) + 60)
184:         result["execution"] = run
185:         events = []
186:         for line in text.splitlines():
187:             try:
188:                 event = json.loads(line)
189:             except ValueError:
190:                 continue
191:             if isinstance(event, dict) and event.get("Test"):
192:                 events.append(event)
193:         ran = {e["Test"] for e in events if e.get("Action") == "run"}
194:         passed = {e["Test"] for e in events if e.get("Action") == "pass"}
195:         failed = {e["Test"] for e in events if e.get("Action") == "fail"}
196:         skipped = {e["Test"] for e in events if e.get("Action") == "skip"}
197:         missing = set(entry["expected_tests"]) - ran
198:         result.update(ran=sorted(ran), passed=sorted(passed), failed=sorted(failed), skipped=sorted(skipped), missing_tests=sorted(missing))
199:         if run["exit_code"] != 0 or failed or missing:
200:             result["status"] = "failed"
201:         elif skipped or not set(entry["expected_tests"]) <= passed:
202:             result["status"] = "incomplete"
203:         else:
204:             result["status"] = "passed"
205:     after = input_fingerprint()
206:     stable = before == after
207:     good = {"manifest_valid"} if args.check_manifest else {"passed"}
208:     complete = stable and all(r["status"] in good for r in results)
209:     summary = {"status": "passed" if complete else "failed", "mode": "manifest_check" if args.check_manifest else "test",
210:                "coverage": "registered_selections_only", "repository_full_suite": False, "go_version": version,
211:                "inputs_stable": stable, "inputs": before, "results": results, "report": str(run_dir / "result.json")}
212:     fd = os.open(run_dir / "result.json", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
213:     with os.fdopen(fd, "w") as output:
214:         json.dump(summary, output, ensure_ascii=False, indent=2)
215:     if args.json:
216:         print(json.dumps(summary, ensure_ascii=False))
217:     else:
218:         for result in results:
219:             print(f"{result['id']}: {result['status']}; ran={len(result.get('ran', []))}, skipped={len(result.get('skipped', []))}")
220:         print("Evidence:", summary["report"])
221:     return 0 if complete else 1
222: 
223: 
224: if __name__ == "__main__":
225:     try:
226:         sys.exit(main())
227:     except (ValueError, OSError, subprocess.SubprocessError) as error:
228:         print(json.dumps({"status": "error", "error": str(error)}), file=sys.stderr)
229:         sys.exit(2)
```

## tests/e2e/deploy_agent_test.go at fe4bf8e
```text
1: package e2e
2: 
3: import (
4: 	"os"
5: 	"os/exec"
6: 	"path/filepath"
7: 	"strings"
8: 	"testing"
9: )
10: 
11: // These tests stub only privileged installation operations. They execute the
12: // real install_unit/render_unit logic and inspect the actual unit it would
13: // install, without touching /etc, creating accounts, downloading binaries or
14: // invoking systemd.
15: func deploymentLibrary(t *testing.T) string {
16: 	t.Helper()
17: 	b, err := os.ReadFile("../../deploy/deploy-agent.sh")
18: 	if err != nil {
19: 		t.Fatal(err)
20: 	}
21: 	text := string(b)
22: 	// The fixed script guards main() itself, so sourcing is already inert. Strip
23: 	// the unguarded pre-fix invocation as well, so the same test can be run
24: 	// against the parent commit to reproduce the original failures.
25: 	text = strings.Replace(text, "\nmain \"$@\"\n", "\n", 1)
26: 	dir := t.TempDir()
27: 	if err := os.WriteFile(filepath.Join(dir, "deploy-agent.sh"), []byte(text), 0600); err != nil {
28: 		t.Fatal(err)
29: 	}
30: 	// Copy the release-managed unit next to the script: the pre-fix installer
31: 	// preferred it, which is exactly the defect under test.
32: 	unit, err := os.ReadFile("../../deploy/proxypanel-agent.service")
33: 	if err != nil {
34: 		t.Fatal(err)
35: 	}
36: 	if err := os.WriteFile(filepath.Join(dir, "proxypanel-agent.service"), unit, 0600); err != nil {
37: 		t.Fatal(err)
38: 	}
39: 	return filepath.Join(dir, "deploy-agent.sh")
40: }
41: 
42: // runInstaller sources the script with the privileged steps stubbed and runs
43: // install_unit, returning the unit text that would land in /etc/systemd/system.
44: func runInstaller(t *testing.T, state, ca string) string {
45: 	t.Helper()
46: 	script := deploymentLibrary(t)
47: 	out := filepath.Join(t.TempDir(), "unit")
48: 	cmd := exec.Command("/bin/bash", "-c", `
49: set -eu
50: source "$LIBRARY"
51: ARG_STATE_DIR="$TEST_STATE"
52: ARG_PANEL_CA="$TEST_CA"
53: install() { cp -- "${@: -2:1}" "$UNIT_OUTPUT"; }
54: systemctl() { :; }
55: install_unit
56: `)
57: 	cmd.Env = append(os.Environ(),
58: 		"LIBRARY="+script, "UNIT_OUTPUT="+out, "TEST_STATE="+state, "TEST_CA="+ca)
59: 	if b, err := cmd.CombinedOutput(); err != nil {
60: 		t.Fatalf("installer: %v: %s", err, b)
61: 	}
62: 	b, err := os.ReadFile(out)
63: 	if err != nil {
64: 		t.Fatal(err)
65: 	}
66: 	return string(b)
67: }
68: 
69: func TestStandaloneAgentUnitMatchesInstalledBinary(t *testing.T) {
70: 	state := "/var/lib/proxypanel-agent-custom"
71: 	unit := runInstaller(t, state, "/synthetic/ca.crt")
72: 	for _, expected := range []string{
73: 		"WorkingDirectory=/opt/proxypanel/agent",
74: 		"ExecStart=/opt/proxypanel/agent/agent -config /etc/proxypanel/agent.json",
75: 		"User=proxypanel-agent",
76: 		"ReadWritePaths=" + state,
77: 		"Environment=SSL_CERT_FILE=/usr/local/share/ca-certificates/proxypanel-panel.crt",
78: 	} {
79: 		if !strings.Contains(unit, expected) {
80: 			t.Errorf("unit does not match standalone install: missing %q\n%s", expected, unit)
81: 		}
82: 	}
83: 	if strings.Contains(unit, "/opt/proxypanel/current") {
84: 		t.Fatal("copied release-manager unit into standalone installation")
85: 	}
86: 	// The release unit delegates state creation to systemd via StateDirectory=,
87: 	// which would always create /var/lib/proxypanel-agent and ignore a custom
88: 	// --state-dir. The standalone unit must not inherit that.
89: 	if strings.Contains(unit, "StateDirectory") {
90: 		t.Fatalf("copied StateDirectory= from the release unit:\n%s", unit)
91: 	}
92: }
93: 
94: // The CA override must only appear when a private CA was actually supplied;
95: // otherwise SSL_CERT_FILE points at a nonexistent file on every install.
96: func TestStandaloneAgentUnitOmitsCAOverrideWithoutPrivateCA(t *testing.T) {
97: 	state := "/var/lib/proxypanel-agent-custom"
98: 	unit := runInstaller(t, state, "")
99: 	if strings.Contains(unit, "SSL_CERT_FILE") {
100: 		t.Fatalf("unit sets SSL_CERT_FILE without --panel-ca:\n%s", unit)
101: 	}
102: 	if !strings.Contains(unit, "ReadWritePaths="+state) {
103: 		t.Fatalf("custom state directory missing:\n%s", unit)
104: 	}
105: }
106: 
107: // A trailing slash is a normal way to type a directory but used to defeat the
108: // exact-match deny list: /etc/ produced ReadWritePaths=/etc/ in the unit.
109: func TestStandaloneAgentRejectsUnsafeStatePath(t *testing.T) {
110: 	for _, bad := range []string{
111: 		"/",
112: 		"/var",
113: 		"/var/lib",
114: 		"/var/lib/",
115: 		"/etc",
116: 		"/etc/",
117: 		"/etc/systemd/system",
118: 		"/usr/local/bin",
119: 		"/boot/loader",
120: 		"/dev/shm/x",
121: 		"/proc/x",
122: 		"/tmp",
123: 		"/tmp/state",
124: 		"/home/state",
125: 		"/root/state",
126: 		"/opt/proxypanel/agent",
127: 		"/var/log/x",
128: 		"relative/state",
129: 		"/tmp/a/../b",
130: 		"/tmp/state with spaces",
131: 		"/tmp/state%h",
132: 	} {
133: 		t.Run(bad, func(t *testing.T) {
134: 			script := deploymentLibrary(t)
135: 			cmd := exec.Command("/bin/bash", "-c", `source "$LIBRARY"; validate_state_dir "$BAD_STATE"`)
136: 			cmd.Env = append(os.Environ(), "LIBRARY="+script, "BAD_STATE="+bad)
137: 			out, err := cmd.CombinedOutput()
138: 			exit, ok := err.(*exec.ExitError)
139: 			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "状态") {
140: 				t.Fatalf("expected state validation refusal, not a missing command: path=%q err=%v output=%s", bad, err, out)
141: 			}
142: 		})
143: 	}
144: }
145: 
146: func TestStandaloneAgentAcceptsNormalStatePath(t *testing.T) {
147: 	for _, ok := range []string{
148: 		"/var/lib/proxypanel-agent",
149: 		"/srv/proxypanel-agent",
150: 		"/data/proxypanel-agent",
151: 	} {
152: 		t.Run(ok, func(t *testing.T) {
153: 			script := deploymentLibrary(t)
154: 			cmd := exec.Command("/bin/bash", "-c", `source "$LIBRARY"; validate_state_dir "$BAD_STATE"`)
155: 			cmd.Env = append(os.Environ(), "LIBRARY="+script, "BAD_STATE="+ok)
156: 			if b, err := cmd.CombinedOutput(); err != nil {
157: 				t.Fatalf("normal state path rejected: %q (%s)", ok, b)
158: 			}
159: 		})
160: 	}
161: }
162: 
163: // A trailing slash must be normalized rather than silently changing the unit
164: // path, and must not leave a double slash in ReadWritePaths.
165: func TestStandaloneAgentNormalizesTrailingSlashState(t *testing.T) {
166: 	state := "/var/lib/proxypanel-agent-custom/"
167: 	unit := runInstaller(t, state, "")
168: 	want := "ReadWritePaths=/var/lib/proxypanel-agent-custom\n"
169: 	if !strings.Contains(unit, want) {
170: 		t.Fatalf("trailing slash not normalized, want %q:\n%s", want, unit)
171: 	}
172: }
173: 
174: // Preserving an existing config must also leave the existing unit and trust
175: // store alone: the preserved config may point at a different state directory
176: // or CA than the flags being passed on this run.
177: func TestStandaloneAgentKeepConfigSkipsUnitAndCA(t *testing.T) {
178: 	script := deploymentLibrary(t)
179: 	cmd := exec.Command("/bin/bash", "-c", `
180: set -eu
181: source "$LIBRARY"
182: require_root() { :; }
183: detect_arch() { ARCH=x86_64; }
184: require_systemd() { :; }
185: check_tools() { :; }
186: check_disk() { :; }
187: collect_inputs() { :; }
188: create_account() { :; }
189: install_xray() { :; }
190: install_agent_binary() { :; }
191: prepare_state() { :; }
192: write_config() { ARG_KEEP_CONFIG=1; }
193: install_panel_ca() { echo CA_INSTALLED; exit 99; }
194: install_unit() { echo UNIT_INSTALLED; exit 99; }
195: run_config_check() { echo CONFIG_CHECKED; exit 99; }
196: start_service() { echo SERVICE_STARTED; exit 99; }
197: systemctl() { :; }
198: report() { :; }
199: main
200: `)
201: 	cmd.Env = append(os.Environ(), "LIBRARY="+script)
202: 	out, err := cmd.CombinedOutput()
203: 	if err != nil {
204: 		t.Fatalf("keep-config run failed: %v: %s", err, out)
205: 	}
206: 	for _, forbidden := range []string{"CA_INSTALLED", "UNIT_INSTALLED", "CONFIG_CHECKED", "SERVICE_STARTED"} {
207: 		if strings.Contains(string(out), forbidden) {
208: 			t.Fatalf("--keep-config path ran %s:\n%s", forbidden, out)
209: 		}
210: 	}
211: }
212: 
213: // `bash < deploy-agent.sh` (no file argument) must not abort on set -u before
214: // reaching usage; BASH_SOURCE is unset in that mode.
215: func TestStandaloneAgentRunsWithoutBashSource(t *testing.T) {
216: 	script := deploymentLibrary(t)
217: 	f, err := os.Open(script)
218: 	if err != nil {
219: 		t.Fatal(err)
220: 	}
221: 	defer f.Close()
222: 	cmd := exec.Command("/bin/bash", "-s", "--", "--help")
223: 	cmd.Stdin = f
224: 	out, err := cmd.CombinedOutput()
225: 	if err != nil {
226: 		t.Fatalf("stdin invocation failed: %v: %s", err, out)
227: 	}
228: 	if !strings.Contains(string(out), "用法") {
229: 		t.Fatalf("usage not printed:\n%s", out)
230: 	}
231: }
```

## deploy/proxypanel-agent.service at fe4bf8e
```text
1: [Unit]
2: Description=Proxypanel Xray node agent
3: After=network-online.target
4: Wants=network-online.target
5: 
6: [Service]
7: Type=simple
8: User=proxypanel-agent
9: Group=proxypanel-agent
10: WorkingDirectory=/opt/proxypanel/current
11: ExecStart=/opt/proxypanel/current/agent -config /etc/proxypanel/agent.json
12: Restart=on-failure
13: RestartSec=5
14: TimeoutStopSec=75
15: KillMode=control-group
16: UMask=0077
17: NoNewPrivileges=true
18: PrivateTmp=true
19: ProtectSystem=strict
20: ProtectHome=true
21: StateDirectory=proxypanel-agent
22: StateDirectoryMode=0700
23: ReadWritePaths=/var/lib/proxypanel-agent
24: AmbientCapabilities=CAP_NET_BIND_SERVICE
25: CapabilityBoundingSet=CAP_NET_BIND_SERVICE
26: RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
27: 
28: [Install]
29: WantedBy=multi-user.target
```
