# CPREV-003 frozen input

Target: 3435f1d..fe4bf8e. No current WIP included.

## Complete relevant diff
```diff
diff --git a/deploy/deploy-agent.sh b/deploy/deploy-agent.sh
index 822f12f..032bb6d 100755
--- a/deploy/deploy-agent.sh
+++ b/deploy/deploy-agent.sh
@@ -18,8 +18,8 @@
 #   sudo bash deploy-agent.sh --dry-run       # 只打印将要执行的操作
 #   sudo bash deploy-agent.sh --yes ...       # 非交互（参数见 --help）
 #
-# 重要：本脚本未经真机验证。项目已验证的是 Windows 本地运行与 Docker 容器运行；
-# systemd 安装路径、真实 VPS 跨机链路都尚未实测。首次使用请先在测试机上跑通。
+# systemd 安装路径尚未在目标生产机验证；首次使用先在隔离节点验收。
+# 脚本级回归不等于已安装/重启过真实宿主服务。
 set -euo pipefail
 
 readonly SCRIPT_VERSION="1.0"
@@ -37,6 +37,20 @@ readonly XRAY_BASE="/opt/proxypanel/xray"
 readonly AGENT_DIR="/opt/proxypanel/agent"
 readonly AGENT_BIN="$AGENT_DIR/agent"
 readonly UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
+readonly CA_FILE="/usr/local/share/ca-certificates/proxypanel-panel.crt"
+
+# ReadWritePaths must not grant writes to system files or other services' data.
+# This deny list is defence in depth, not an exhaustive security boundary.
+# ProtectHome/PrivateTmp also make home and temporary paths unusable for state.
+readonly -a FORBIDDEN_STATE_TREES=(
+  /etc /usr /bin /sbin /lib /lib32 /lib64 /libx32 /boot /dev /proc /sys
+  /run /var/run /var/log /var/lock /var/spool /var/lib/systemd
+  /var/lib/docker /var/lib/postgresql /var/lib/pgsql /var/lib/mysql
+  /var/lib/redis /tmp /var/tmp /home /root /opt/proxypanel
+)
+readonly -a FORBIDDEN_STATE_EXACT=(
+  / /var /var/lib /opt /srv /mnt /media
+)
 
 ASSUME_YES=""
 DRY_RUN=""
@@ -168,7 +182,7 @@ require_systemd() {
 
 check_tools() {
   local missing=()
-  for tool in curl sha256sum unzip install useradd; do
+  for tool in curl sha256sum unzip install useradd realpath mktemp; do
     command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
   done
   if [ ${#missing[@]} -gt 0 ]; then
@@ -265,9 +279,8 @@ collect_inputs() {
     die "从 URL 安装必须提供 --agent-sha256，否则无法校验下载内容"
   fi
 
-  if [ -z "$ARG_STATE_DIR" ]; then
-    ARG_STATE_DIR="$STATE_DIR"
-  fi
+  ARG_STATE_DIR=$(normalize_state_dir "${ARG_STATE_DIR:-$STATE_DIR}")
+  validate_state_dir "$ARG_STATE_DIR"
 
   # 非交互模式（-y）下不再提问可选值：没有 --panel-ca 就按"不用私有 CA"处理，
   # 否则 read 在无 tty 的环境里会立即 EOF 并把部署判定为失败。
@@ -437,29 +450,23 @@ install_panel_ca() {
   [ -n "$ARG_PANEL_CA" ] || return 0
   step "安装私有 CA"
   [ -f "$ARG_PANEL_CA" ] || die "找不到 CA 文件：$ARG_PANEL_CA"
-  local target="/usr/local/share/ca-certificates/proxypanel-panel.crt"
-  install -m 0644 "$ARG_PANEL_CA" "$target"
+  install -m 0644 "$ARG_PANEL_CA" "$CA_FILE"
   if command -v update-ca-certificates >/dev/null 2>&1; then
     update-ca-certificates >/dev/null 2>&1 || warn "update-ca-certificates 返回非零，请手工确认"
     ok "已加入系统信任库"
   else
-    warn "系统没有 update-ca-certificates；请在服务单元中设置 SSL_CERT_FILE=$target"
+    ok "已安装到 $CA_FILE；本机没有 update-ca-certificates"
+    info "单元已通过 Environment=SSL_CERT_FILE=$CA_FILE 指定它"
   fi
 }
 
-install_unit() {
-  step "安装 systemd 单元"
-
-  local script_dir
-  script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
-  local candidate="$script_dir/proxypanel-agent.service"
-
-  if [ -f "$candidate" ]; then
-    install -m 0644 "$candidate" "$UNIT_FILE"
-    ok "使用随附的单元文件：$candidate"
-  else
-    info "未找到随附单元文件，写入内置版本"
-    cat > "$UNIT_FILE" <<'EOF'
+# The release installer uses /opt/proxypanel/current; this standalone installer
+# uses /opt/proxypanel/agent. Never copy the release unit into this layout.
+render_unit() {
+  local state_path
+  state_path=$(normalize_state_dir "${ARG_STATE_DIR:-$STATE_DIR}")
+  validate_state_dir "$state_path"
+  cat <<EOF
 [Unit]
 Description=Proxypanel Xray node agent
 After=network-online.target
@@ -467,10 +474,10 @@ Wants=network-online.target
 
 [Service]
 Type=simple
-User=proxypanel-agent
-Group=proxypanel-agent
-WorkingDirectory=/opt/proxypanel/agent
-ExecStart=/opt/proxypanel/agent/agent -config /etc/proxypanel/agent.json
+User=$SERVICE_ACCOUNT
+Group=$SERVICE_ACCOUNT
+WorkingDirectory=$AGENT_DIR
+ExecStart=$AGENT_BIN -config $CONFIG_FILE
 Restart=on-failure
 RestartSec=5
 TimeoutStopSec=75
@@ -480,17 +487,75 @@ NoNewPrivileges=true
 PrivateTmp=true
 ProtectSystem=strict
 ProtectHome=true
-ReadWritePaths=/var/lib/proxypanel-agent
+ReadWritePaths=$state_path
 AmbientCapabilities=CAP_NET_BIND_SERVICE
 CapabilityBoundingSet=CAP_NET_BIND_SERVICE
 RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
+EOF
+  # A nonexistent SSL_CERT_FILE breaks TLS, so override only for an explicit CA.
+  if [ -n "$ARG_PANEL_CA" ]; then
+    printf 'Environment=SSL_CERT_FILE=%s\n' "$CA_FILE"
+  fi
+  cat <<EOF
 
 [Install]
 WantedBy=multi-user.target
 EOF
-    chmod 0644 "$UNIT_FILE"
-  fi
+}
+
+# Canonical form: absolute, exactly one leading slash, no trailing slash.
+normalize_state_dir() {
+  local value=${1:-}
+  while [[ $value == */ && $value != / ]]; do
+    value=${value%/}
+  done
+  printf '%s' "$value"
+}
+
+validate_state_dir() {
+  local value part current='' forbidden
+  local -a parts=()
+  # Normalize first: a typed trailing slash must not change the meaning of the
+  # deny list below (/etc/ has to be rejected exactly like /etc).
+  value=$(normalize_state_dir "${1-}")
+  # No whitespace, controls or systemd %-specifiers: both the unit file and the
+  # JSON config embed this value verbatim.
+  [[ $value =~ ^/[A-Za-z0-9_./-]+$ ]] || die "状态目录必须是无空白/控制字符的绝对路径：$value"
+  for forbidden in "${FORBIDDEN_STATE_EXACT[@]}"; do
+    [ "$value" != "$forbidden" ] || die "拒绝将系统目录用作 Agent 状态目录：$value"
+  done
+  for forbidden in "${FORBIDDEN_STATE_TREES[@]}"; do
+    case "$value" in "$forbidden"|"$forbidden"/*)
+      die "拒绝将系统目录用作 Agent 状态目录：$value" ;;
+    esac
+  done
+  local resolved
+  resolved=$(realpath -m -- "$value")
+  [ "$resolved" = "$value" ] || die "状态路径必须规范化且不能穿过符号链接或包含 ..：$value"
+  IFS='/' read -r -a parts <<< "$value"
+  for part in "${parts[@]}"; do
+    [ -n "$part" ] || continue
+    current="$current/$part"
+    [ ! -L "$current" ] || die "状态路径不能包含符号链接：$current"
+  done
+}
 
+install_unit() {
+  step "安装 systemd 单元"
+  local unit_tmp
+  # The unit embeds the state path and the CA path. Write it 0600 and clean up
+  # through an EXIT trap: validate_state_dir calls die(), which exits the shell
+  # immediately, so an explicit `rm` after render_unit would never run.
+  unit_tmp=$(mktemp)
+  chmod 0600 "$unit_tmp"
+  local had_exit_trap
+  had_exit_trap=$(trap -p EXIT)
+  # shellcheck disable=SC2064 # unit_tmp is expanded now on purpose.
+  trap "rm -f -- '$unit_tmp'" EXIT
+  render_unit > "$unit_tmp" || die "无法生成 systemd 单元"
+  install -m 0644 "$unit_tmp" "$UNIT_FILE"
+  rm -f -- "$unit_tmp"
+  if [ -n "$had_exit_trap" ]; then eval "$had_exit_trap"; else trap - EXIT; fi
   systemctl daemon-reload
   ok "单元已安装：$UNIT_FILE"
 }
@@ -563,18 +628,27 @@ main() {
   install_agent_binary
   prepare_state
   write_config
-  install_panel_ca
-  install_unit
-
   if [ -n "${ARG_KEEP_CONFIG:-}" ]; then
-    ok "沿用现有配置，跳过配置检查与重启"
+    # The preserved config may use a different state directory or CA. Do not
+    # replace its existing unit or trust store using newly supplied parameters.
+    ok "沿用现有配置、服务单元与 CA，跳过配置检查与重启"
+    if [ ! -f "$UNIT_FILE" ]; then
+      warn "现有配置被保留，但 $UNIT_FILE 不存在：本次没有安装单元，服务不会启动。"
+      warn "如确需重新生成单元，请重跑并选择覆盖现有配置。"
+    fi
     report
     return 0
   fi
 
+  install_panel_ca
+  install_unit
   run_config_check
   start_service
   report
 }
 
-main "$@"
+# Sourced by tests (BASH_SOURCE is set) or executed normally. Also tolerate
+# `bash < deploy-agent.sh`, where BASH_SOURCE is unset and set -u would abort.
+if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
+  main "$@"
+fi
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

## deploy/deploy-agent.sh at fe4bf8e
```text
1: #!/usr/bin/env bash
2: # 节点端 Agent 交互式部署脚本（裸机 + systemd，不使用 Docker）。
3: #
4: # 它做什么：
5: #   1. 检查主机环境（root、systemd、架构、依赖工具、磁盘）
6: #   2. 按官方发行包安装 Xray 核心，并用官方 .dgst 核对 SHA2-256
7: #   3. 安装 Agent 可执行文件（本地文件或 URL + SHA256 校验）
8: #   4. 创建系统账号与状态目录，写入 /etc/proxypanel/agent.json（0600/0640 权限）
9: #   5. 安装 systemd 单元，先 -check 再启用启动
10: #
11: # 它不做什么：
12: #   - 不安装/重启数据库，不碰面板
13: #   - 不修改防火墙、不安装 Docker、不下载未校验的二进制
14: #   - 不删除已存在的状态目录（里面有 Agent 身份、计量基线和未上传队列）
15: #
16: # 用法：
17: #   sudo bash deploy-agent.sh                 # 交互式
18: #   sudo bash deploy-agent.sh --dry-run       # 只打印将要执行的操作
19: #   sudo bash deploy-agent.sh --yes ...       # 非交互（参数见 --help）
20: #
21: # systemd 安装路径尚未在目标生产机验证；首次使用先在隔离节点验收。
22: # 脚本级回归不等于已安装/重启过真实宿主服务。
23: set -euo pipefail
24: 
25: readonly SCRIPT_VERSION="1.0"
26: 
27: # 与 internal/agent/xray/version.go 的闭合白名单保持一致；
28: # 版本不符时 Agent 会明确拒绝启动，不会静默降级。
29: readonly SUPPORTED_XRAY_VERSIONS=("26.3.27" "26.3.23")
30: 
31: readonly SERVICE_NAME="proxypanel-agent"
32: readonly SERVICE_ACCOUNT="proxypanel-agent"
33: readonly CONFIG_DIR="/etc/proxypanel"
34: readonly CONFIG_FILE="$CONFIG_DIR/agent.json"
35: readonly STATE_DIR="/var/lib/proxypanel-agent"
36: readonly XRAY_BASE="/opt/proxypanel/xray"
37: readonly AGENT_DIR="/opt/proxypanel/agent"
38: readonly AGENT_BIN="$AGENT_DIR/agent"
39: readonly UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
40: readonly CA_FILE="/usr/local/share/ca-certificates/proxypanel-panel.crt"
41: 
42: # ReadWritePaths must not grant writes to system files or other services' data.
43: # This deny list is defence in depth, not an exhaustive security boundary.
44: # ProtectHome/PrivateTmp also make home and temporary paths unusable for state.
45: readonly -a FORBIDDEN_STATE_TREES=(
46:   /etc /usr /bin /sbin /lib /lib32 /lib64 /libx32 /boot /dev /proc /sys
47:   /run /var/run /var/log /var/lock /var/spool /var/lib/systemd
48:   /var/lib/docker /var/lib/postgresql /var/lib/pgsql /var/lib/mysql
49:   /var/lib/redis /tmp /var/tmp /home /root /opt/proxypanel
50: )
51: readonly -a FORBIDDEN_STATE_EXACT=(
52:   / /var /var/lib /opt /srv /mnt /media
53: )
54: 
55: ASSUME_YES=""
56: DRY_RUN=""
57: ARG_AGENT_BINARY=""
58: ARG_AGENT_URL=""
59: ARG_AGENT_SHA256=""
60: ARG_XRAY_VERSION=""
61: ARG_PANEL_URL=""
62: ARG_REGISTRATION_CODE=""
63: ARG_PANEL_CA=""
64: ARG_STATE_DIR=""
65: ARCH=""
66: WORK_DIR=""
67: 
68: if [ -t 1 ]; then
69:   C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
70:   C_OK=$'\033[32m'; C_WARN=$'\033[33m'; C_ERR=$'\033[31m'
71: else
72:   C_RESET=""; C_BOLD=""; C_DIM=""; C_OK=""; C_WARN=""; C_ERR=""
73: fi
74: 
75: info() { printf '%s\n' "$*"; }
76: step() { printf '\n%s==>%s %s\n' "$C_BOLD" "$C_RESET" "$*"; }
77: ok()   { printf '%s  ok%s %s\n' "$C_OK" "$C_RESET" "$*"; }
78: warn() { printf '%s  警告%s %s\n' "$C_WARN" "$C_RESET" "$*" >&2; }
79: die()  { printf '%s  错误%s %s\n' "$C_ERR" "$C_RESET" "$*" >&2; exit 1; }
80: 
81: usage() {
82:   cat <<'EOF'
83: 节点端 Agent 交互式部署（裸机 + systemd）
84: 
85: 用法：
86:   sudo bash deploy-agent.sh [选项]
87: 
88: 选项：
89:   --agent-binary PATH     使用本地已有的 Agent 可执行文件
90:   --agent-url URL         从 URL 下载 Agent 可执行文件（必须同时给 --agent-sha256）
91:   --agent-sha256 HEX      下载文件的 SHA256（不校验会拒绝执行）
92:   --xray-version V        指定 Xray 版本，可选：26.3.27 / 26.3.23
93:   --panel-url URL         面板地址（必须是 https://）
94:   --registration-code C   一次性注册码（会进入 shell 历史，建议交互输入）
95:   --panel-ca PATH         面板使用私有 CA 时的 CA 证书文件
96:   --state-dir PATH        状态目录，默认 /var/lib/proxypanel-agent
97:   --dry-run               只打印将要执行的操作，不做任何修改
98:   -y, --yes               跳过确认（用于已有参数的非交互部署）
99:   -h, --help              显示本帮助
100: 
101: 说明：
102:   - 官方 Xray 从 GitHub Releases 下载，并用同发布的 .dgst 校验 SHA2-256。
103:   - Agent 二进制必须能通过 -version 输出 proxypanel-agent。
104:   - 已存在的状态目录永远不会被删除或重建。
105: EOF
106: }
107: 
108: while [ $# -gt 0 ]; do
109:   case "$1" in
110:     --agent-binary)      ARG_AGENT_BINARY=${2:?}; shift 2 ;;
111:     --agent-url)         ARG_AGENT_URL=${2:?}; shift 2 ;;
112:     --agent-sha256)      ARG_AGENT_SHA256=${2:?}; shift 2 ;;
113:     --xray-version)      ARG_XRAY_VERSION=${2:?}; shift 2 ;;
114:     --panel-url)         ARG_PANEL_URL=${2:?}; shift 2 ;;
115:     --registration-code) ARG_REGISTRATION_CODE=${2:?}; shift 2 ;;
116:     --panel-ca)          ARG_PANEL_CA=${2:?}; shift 2 ;;
117:     --state-dir)         ARG_STATE_DIR=${2:?}; shift 2 ;;
118:     --dry-run)           DRY_RUN=1; shift ;;
119:     -y|--yes)            ASSUME_YES=1; shift ;;
120:     -h|--help)           usage; exit 0 ;;
121:     *)                   die "未知参数：$1（用 --help 查看用法）" ;;
122:   esac
123: done
124: 
125: # ---------------------------------------------------------------- 交互辅助
126: 
127: ask() { # ask <变量名> <提示> [默认值]
128:   local __var=$1 prompt=$2 default=${3:-} reply=""
129:   if [ -n "$default" ]; then
130:     read -r -p "$prompt [$default]: " reply || die "输入被中断"
131:   else
132:     read -r -p "$prompt: " reply || die "输入被中断"
133:   fi
134:   [ -z "$reply" ] && reply=$default
135:   printf -v "$__var" '%s' "$reply"
136: }
137: 
138: ask_secret() { # 不回显；注册码等同凭据，不留在终端历史里
139:   local __var=$1 prompt=$2 reply=""
140:   read -r -s -p "$prompt: " reply || die "输入被中断"
141:   printf '\n'
142:   printf -v "$__var" '%s' "$reply"
143: }
144: 
145: confirm() { # confirm <提示> [默认 n]
146:   local prompt=$1
147:   [ -n "$ASSUME_YES" ] && return 0
148:   local reply=""
149:   read -r -p "$prompt [y/N]: " reply || die "输入被中断"
150:   case "${reply:-n}" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
151: }
152: 
153: # ---------------------------------------------------------------- 预检
154: 
155: require_root() {
156:   [ "$(id -u)" -eq 0 ] || die "需要 root 权限运行（请用 sudo）"
157: }
158: 
159: require_linux() {
160:   [ "$(uname -s)" = "Linux" ] || die "本脚本只支持 Linux（当前：$(uname -s)）"
161: }
162: 
163: detect_arch() {
164:   local machine
165:   machine=$(uname -m)
166:   case "$machine" in
167:     x86_64|amd64)  ARCH="x86_64" ;;
168:     aarch64|arm64) ARCH="aarch64" ;;
169:     *) die "不支持的架构：$machine（已支持 x86_64 / aarch64）" ;;
170:   esac
171:   # 不能写成 `[ ... ] && warn ...`：条件不成立时该语句返回 1，而它是函数最后一句，
172:   # 在 set -e 下会让整个脚本以 1 退出（真机上就是这么被拦住的）。
173:   if [ "$ARCH" = "aarch64" ]; then
174:     warn "aarch64 未做过运行时验收，只在构建层面验证过。"
175:   fi
176: }
177: 
178: require_systemd() {
179:   command -v systemctl >/dev/null 2>&1 || die "找不到 systemctl，本脚本依赖 systemd"
180:   [ -d /run/systemd/system ] || die "systemd 未作为 init 运行，无法安装服务"
181: }
182: 
183: check_tools() {
184:   local missing=()
185:   for tool in curl sha256sum unzip install useradd realpath mktemp; do
186:     command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
187:   done
188:   if [ ${#missing[@]} -gt 0 ]; then
189:     warn "缺少依赖：${missing[*]}"
190:     info "Debian/Ubuntu: apt-get install -y curl coreutils unzip"
191:     info "RHEL/CentOS:   dnf install -y curl coreutils unzip"
192:     die "请先安装上述工具"
193:   fi
194:   if ! command -v runuser >/dev/null 2>&1 && ! command -v sudo >/dev/null 2>&1; then
195:     die "缺少 runuser 或 sudo，无法以服务账号身份做配置检查"
196:   fi
197: }
198: 
199: check_disk() {
200:   local avail_kb
201:   avail_kb=$(df -Pk /opt 2>/dev/null | awk 'NR==2 {print $4}')
202:   [ -n "${avail_kb:-}" ] || return 0
203:   if [ "$avail_kb" -lt 204800 ]; then
204:     warn "/opt 可用空间不足 200MB（当前约 $((avail_kb / 1024))MB）"
205:     confirm "仍然继续？" || die "已取消"
206:   fi
207: }
208: 
209: detect_nologin() {
210:   for candidate in /usr/sbin/nologin /sbin/nologin /bin/false; do
211:     [ -x "$candidate" ] && { printf '%s' "$candidate"; return 0; }
212:   done
213:   printf '/bin/false'
214: }
215: 
216: # ---------------------------------------------------------------- 输入收集
217: 
218: collect_inputs() {
219:   step "收集部署参数"
220: 
221:   if [ -z "$ARG_PANEL_URL" ]; then
222:     ask ARG_PANEL_URL "面板地址（https://...）"
223:   fi
224:   case "$ARG_PANEL_URL" in
225:     https://*) ;;
226:     http://*)
227:       warn "纯 http 仅允许指向本机回环地址，且需要 allow_loopback_http=true。"
228:       confirm "确认这是本机调试用的 http 面板？" || die "已取消"
229:       ;;
230:     *) die "面板地址必须以 https:// 开头（收到：$ARG_PANEL_URL）" ;;
231:   esac
232:   case "$ARG_PANEL_URL" in
233:     */) ARG_PANEL_URL=${ARG_PANEL_URL%/} ;;
234:   esac
235: 
236:   if [ -z "$ARG_REGISTRATION_CODE" ]; then
237:     ask_secret ARG_REGISTRATION_CODE "一次性注册码（输入不显示）"
238:   fi
239:   [ -n "$ARG_REGISTRATION_CODE" ] || die "注册码不能为空（请在面板「服务器」页生成）"
240: 
241:   if [ -z "$ARG_XRAY_VERSION" ]; then
242:     info ""
243:     info "可选 Xray 版本（Agent 只接受这个闭合白名单）："
244:     local i=1 v
245:     for v in "${SUPPORTED_XRAY_VERSIONS[@]}"; do
246:       info "  $i) $v"
247:       i=$((i + 1))
248:     done
249:     local choice
250:     ask choice "选择版本序号" "1"
251:     case "$choice" in
252:       1) ARG_XRAY_VERSION=${SUPPORTED_XRAY_VERSIONS[0]} ;;
253:       2) ARG_XRAY_VERSION=${SUPPORTED_XRAY_VERSIONS[1]} ;;
254:       *) die "无效的选择：$choice" ;;
255:     esac
256:   fi
257:   local supported=""
258:   for v in "${SUPPORTED_XRAY_VERSIONS[@]}"; do
259:     [ "$v" = "$ARG_XRAY_VERSION" ] && supported="yes"
260:   done
261:   [ -n "$supported" ] || die "不支持的 Xray 版本：$ARG_XRAY_VERSION（可选：${SUPPORTED_XRAY_VERSIONS[*]}）"
262: 
263:   if [ -z "$ARG_AGENT_BINARY" ] && [ -z "$ARG_AGENT_URL" ]; then
264:     info ""
265:     info "Agent 可执行文件来源："
266:     info "  1) 本地已有文件（例如发布包 build/linux-amd64/agent）"
267:     info "  2) 从 URL 下载（需要提供 SHA256）"
268:     local src
269:     ask src "选择来源序号" "1"
270:     case "$src" in
271:       1) ask ARG_AGENT_BINARY "Agent 可执行文件路径" ;;
272:       2) ask ARG_AGENT_URL "下载地址"
273:          ask ARG_AGENT_SHA256 "该文件的 SHA256" ;;
274:       *) die "无效的选择：$src" ;;
275:     esac
276:   fi
277: 
278:   if [ -n "$ARG_AGENT_URL" ] && [ -z "$ARG_AGENT_SHA256" ]; then
279:     die "从 URL 安装必须提供 --agent-sha256，否则无法校验下载内容"
280:   fi
281: 
282:   ARG_STATE_DIR=$(normalize_state_dir "${ARG_STATE_DIR:-$STATE_DIR}")
283:   validate_state_dir "$ARG_STATE_DIR"
284: 
285:   # 非交互模式（-y）下不再提问可选值：没有 --panel-ca 就按"不用私有 CA"处理，
286:   # 否则 read 在无 tty 的环境里会立即 EOF 并把部署判定为失败。
287:   if [ -z "$ARG_PANEL_CA" ] && [ -z "$ASSUME_YES" ] && confirm "面板是否使用私有 CA 证书？（公共证书选 N）"; then
288:     ask ARG_PANEL_CA "CA 证书文件路径"
289:   fi
290: 
291:   info ""
292:   info "${C_BOLD}将要执行：${C_RESET}"
293:   info "  面板地址      : $ARG_PANEL_URL"
294:   info "  Xray 版本     : $ARG_XRAY_VERSION  → $XRAY_BASE/$ARG_XRAY_VERSION/xray"
295:   info "  Agent 来源    : ${ARG_AGENT_BINARY:-$ARG_AGENT_URL}"
296:   info "  安装路径      : $AGENT_BIN"
297:   info "  配置文件      : $CONFIG_FILE（root:$SERVICE_ACCOUNT 0640）"
298:   info "  状态目录      : $ARG_STATE_DIR（$SERVICE_ACCOUNT 0700）"
299:   info "  服务名        : $SERVICE_NAME"
300:   [ -n "$ARG_PANEL_CA" ] && info "  私有 CA       : $ARG_PANEL_CA"
301:   info ""
302: 
303:   if [ -n "$DRY_RUN" ]; then
304:     warn "--dry-run：以上为计划，未做任何修改"
305:     exit 0
306:   fi
307:   confirm "确认开始安装？" || die "已取消"
308: }
309: 
310: # ---------------------------------------------------------------- 安装步骤
311: 
312: create_account() {
313:   step "创建系统账号 $SERVICE_ACCOUNT"
314:   if id "$SERVICE_ACCOUNT" >/dev/null 2>&1; then
315:     ok "账号已存在，复用"
316:   else
317:     local shell_path
318:     shell_path=$(detect_nologin)
319:     useradd --system --user-group --no-create-home --shell "$shell_path" "$SERVICE_ACCOUNT"
320:     ok "已创建系统账号（无登录 shell、无 home）"
321:   fi
322: }
323: 
324: install_xray() {
325:   local version=$1
326:   step "安装官方 Xray $version"
327: 
328:   local asset
329:   case "$ARCH" in
330:     x86_64)  asset="Xray-linux-64.zip" ;;
331:     aarch64) asset="Xray-linux-arm64-v8a.zip" ;;
332:   esac
333:   local base="https://github.com/XTLS/Xray-core/releases/download/v${version}"
334: 
335:   WORK_DIR=$(mktemp -d)
336:   trap 'rm -rf "${WORK_DIR:-}"' EXIT
337: 
338:   info "下载 $asset …"
339:   curl -fsSL --retry 3 --connect-timeout 20 -o "$WORK_DIR/$asset" "$base/$asset" \
340:     || die "下载失败：$base/$asset"
341:   curl -fsSL --retry 3 --connect-timeout 20 -o "$WORK_DIR/$asset.dgst" "$base/$asset.dgst" \
342:     || die "下载官方摘要失败：$base/$asset.dgst"
343: 
344:   local expected actual
345:   expected=$(sed -n 's/.*SHA2-256=[[:space:]]*\([0-9a-fA-F]\{64\}\).*/\1/p' "$WORK_DIR/$asset.dgst" | head -n1 | tr 'A-F' 'a-f')
346:   [ -n "$expected" ] || die "官方 .dgst 中没有可解析的 SHA2-256，拒绝继续"
347:   actual=$(sha256sum "$WORK_DIR/$asset" | awk '{print $1}')
348:   [ "$expected" = "$actual" ] || die "SHA256 不匹配（官方 $expected，实际 $actual）"
349:   ok "官方摘要校验通过：$actual"
350: 
351:   unzip -q -o "$WORK_DIR/$asset" -d "$WORK_DIR/extract" || die "解压失败（需要 unzip）"
352:   [ -f "$WORK_DIR/extract/xray" ] || die "压缩包内没有 xray 可执行文件"
353:   [ -f "$WORK_DIR/extract/geoip.dat" ] || die "压缩包内没有 geoip.dat"
354:   [ -f "$WORK_DIR/extract/geosite.dat" ] || die "压缩包内没有 geosite.dat"
355: 
356:   "$WORK_DIR/extract/xray" version 2>&1 | head -n1 | grep -q "$version" \
357:     || die "下载的核心未报告预期版本 $version"
358:   ok "官方核心版本确认为 $version"
359: 
360:   # 核心必须与自己的 geo 数据同目录：Agent 按 xray_path 所在目录解析它们。
361:   local dest="$XRAY_BASE/$version"
362:   install -d -m 0755 "$dest"
363:   install -m 0755 "$WORK_DIR/extract/xray" "$dest/xray"
364:   install -m 0644 "$WORK_DIR/extract/geoip.dat" "$dest/geoip.dat"
365:   install -m 0644 "$WORK_DIR/extract/geosite.dat" "$dest/geosite.dat"
366:   chown -R root:root "$XRAY_BASE"
367:   ok "已安装到 $dest（root 所有，Agent 只读执行）"
368: }
369: 
370: install_agent_binary() {
371:   step "安装 Agent 可执行文件"
372: 
373:   local source_path="$ARG_AGENT_BINARY"
374:   if [ -n "$ARG_AGENT_URL" ]; then
375:     source_path="$WORK_DIR/agent-download"
376:     info "下载 Agent …"
377:     curl -fsSL --retry 3 --connect-timeout 20 -o "$source_path" "$ARG_AGENT_URL" \
378:       || die "下载失败：$ARG_AGENT_URL"
379:     local actual
380:     actual=$(sha256sum "$source_path" | awk '{print $1}')
381:     local expected
382:     expected=$(printf '%s' "$ARG_AGENT_SHA256" | tr 'A-F' 'a-f')
383:     [ "$expected" = "$actual" ] || die "Agent SHA256 不匹配（期望 $expected，实际 $actual）"
384:     ok "Agent 下载内容校验通过"
385:   fi
386: 
387:   [ -f "$source_path" ] || die "找不到 Agent 文件：$source_path"
388: 
389:   install -d -m 0755 "$AGENT_DIR"
390:   install -m 0755 "$source_path" "$AGENT_BIN"
391: 
392:   local reported
393:   reported=$("$AGENT_BIN" -version 2>&1 | head -n1)
394:   case "$reported" in
395:     *proxypanel-agent*) ok "Agent 版本：$reported" ;;
396:     *) die "该文件不是预期的 Agent 构建（-version 输出：$reported）" ;;
397:   esac
398: }
399: 
400: prepare_state() {
401:   step "准备状态目录 $ARG_STATE_DIR"
402:   if [ -d "$ARG_STATE_DIR" ]; then
403:     ok "目录已存在，保留原有内容（身份、计量基线、待上传队列都不动）"
404:   else
405:     install -d -m 0700 -o "$SERVICE_ACCOUNT" -g "$SERVICE_ACCOUNT" "$ARG_STATE_DIR"
406:     ok "已创建"
407:   fi
408:   chown "$SERVICE_ACCOUNT:$SERVICE_ACCOUNT" "$ARG_STATE_DIR"
409:   chmod 0700 "$ARG_STATE_DIR"
410: }
411: 
412: write_config() {
413:   step "写入配置 $CONFIG_FILE"
414:   install -d -m 0755 "$CONFIG_DIR"
415: 
416:   if [ -f "$CONFIG_FILE" ]; then
417:     warn "已存在配置文件"
418:     if confirm "保留现有配置（不覆盖）？" ; then
419:       ok "保留现有配置"
420:       ARG_KEEP_CONFIG=1
421:       return 0
422:     fi
423:     local backup="${CONFIG_FILE}.$(date -u +%Y%m%dT%H%M%SZ).bak"
424:     cp -p "$CONFIG_FILE" "$backup"
425:     chmod 0600 "$backup"
426:     ok "旧配置已备份到 $backup"
427:   fi
428:   ARG_KEEP_CONFIG=""
429: 
430:   # 值只写进文件，不回显到终端。
431:   umask 077
432:   cat > "$CONFIG_FILE" <<EOF
433: {
434:   "panel_url": "${ARG_PANEL_URL}",
435:   "registration_code": "${ARG_REGISTRATION_CODE}",
436:   "xray_path": "${XRAY_BASE}/${ARG_XRAY_VERSION}/xray",
437:   "state_dir": "${ARG_STATE_DIR}",
438:   "poll_interval_seconds": 5,
439:   "stats_interval_seconds": 5,
440:   "heartbeat_interval_seconds": 15,
441:   "allow_loopback_http": false
442: }
443: EOF
444:   chown root:"$SERVICE_ACCOUNT" "$CONFIG_FILE"
445:   chmod 0640 "$CONFIG_FILE"
446:   ok "已写入（root:$SERVICE_ACCOUNT 0640）"
447: }
448: 
449: install_panel_ca() {
450:   [ -n "$ARG_PANEL_CA" ] || return 0
451:   step "安装私有 CA"
452:   [ -f "$ARG_PANEL_CA" ] || die "找不到 CA 文件：$ARG_PANEL_CA"
453:   install -m 0644 "$ARG_PANEL_CA" "$CA_FILE"
454:   if command -v update-ca-certificates >/dev/null 2>&1; then
455:     update-ca-certificates >/dev/null 2>&1 || warn "update-ca-certificates 返回非零，请手工确认"
456:     ok "已加入系统信任库"
457:   else
458:     ok "已安装到 $CA_FILE；本机没有 update-ca-certificates"
459:     info "单元已通过 Environment=SSL_CERT_FILE=$CA_FILE 指定它"
460:   fi
461: }
462: 
463: # The release installer uses /opt/proxypanel/current; this standalone installer
464: # uses /opt/proxypanel/agent. Never copy the release unit into this layout.
465: render_unit() {
466:   local state_path
467:   state_path=$(normalize_state_dir "${ARG_STATE_DIR:-$STATE_DIR}")
468:   validate_state_dir "$state_path"
469:   cat <<EOF
470: [Unit]
471: Description=Proxypanel Xray node agent
472: After=network-online.target
473: Wants=network-online.target
474: 
475: [Service]
476: Type=simple
477: User=$SERVICE_ACCOUNT
478: Group=$SERVICE_ACCOUNT
479: WorkingDirectory=$AGENT_DIR
480: ExecStart=$AGENT_BIN -config $CONFIG_FILE
481: Restart=on-failure
482: RestartSec=5
483: TimeoutStopSec=75
484: KillMode=control-group
485: UMask=0077
486: NoNewPrivileges=true
487: PrivateTmp=true
488: ProtectSystem=strict
489: ProtectHome=true
490: ReadWritePaths=$state_path
491: AmbientCapabilities=CAP_NET_BIND_SERVICE
492: CapabilityBoundingSet=CAP_NET_BIND_SERVICE
493: RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
494: EOF
495:   # A nonexistent SSL_CERT_FILE breaks TLS, so override only for an explicit CA.
496:   if [ -n "$ARG_PANEL_CA" ]; then
497:     printf 'Environment=SSL_CERT_FILE=%s\n' "$CA_FILE"
498:   fi
499:   cat <<EOF
500: 
501: [Install]
502: WantedBy=multi-user.target
503: EOF
504: }
505: 
506: # Canonical form: absolute, exactly one leading slash, no trailing slash.
507: normalize_state_dir() {
508:   local value=${1:-}
509:   while [[ $value == */ && $value != / ]]; do
510:     value=${value%/}
511:   done
512:   printf '%s' "$value"
513: }
514: 
515: validate_state_dir() {
516:   local value part current='' forbidden
517:   local -a parts=()
518:   # Normalize first: a typed trailing slash must not change the meaning of the
519:   # deny list below (/etc/ has to be rejected exactly like /etc).
520:   value=$(normalize_state_dir "${1-}")
521:   # No whitespace, controls or systemd %-specifiers: both the unit file and the
522:   # JSON config embed this value verbatim.
523:   [[ $value =~ ^/[A-Za-z0-9_./-]+$ ]] || die "状态目录必须是无空白/控制字符的绝对路径：$value"
524:   for forbidden in "${FORBIDDEN_STATE_EXACT[@]}"; do
525:     [ "$value" != "$forbidden" ] || die "拒绝将系统目录用作 Agent 状态目录：$value"
526:   done
527:   for forbidden in "${FORBIDDEN_STATE_TREES[@]}"; do
528:     case "$value" in "$forbidden"|"$forbidden"/*)
529:       die "拒绝将系统目录用作 Agent 状态目录：$value" ;;
530:     esac
531:   done
532:   local resolved
533:   resolved=$(realpath -m -- "$value")
534:   [ "$resolved" = "$value" ] || die "状态路径必须规范化且不能穿过符号链接或包含 ..：$value"
535:   IFS='/' read -r -a parts <<< "$value"
536:   for part in "${parts[@]}"; do
537:     [ -n "$part" ] || continue
538:     current="$current/$part"
539:     [ ! -L "$current" ] || die "状态路径不能包含符号链接：$current"
540:   done
541: }
542: 
543: install_unit() {
544:   step "安装 systemd 单元"
545:   local unit_tmp
546:   # The unit embeds the state path and the CA path. Write it 0600 and clean up
547:   # through an EXIT trap: validate_state_dir calls die(), which exits the shell
548:   # immediately, so an explicit `rm` after render_unit would never run.
549:   unit_tmp=$(mktemp)
550:   chmod 0600 "$unit_tmp"
551:   local had_exit_trap
552:   had_exit_trap=$(trap -p EXIT)
553:   # shellcheck disable=SC2064 # unit_tmp is expanded now on purpose.
554:   trap "rm -f -- '$unit_tmp'" EXIT
555:   render_unit > "$unit_tmp" || die "无法生成 systemd 单元"
556:   install -m 0644 "$unit_tmp" "$UNIT_FILE"
557:   rm -f -- "$unit_tmp"
558:   if [ -n "$had_exit_trap" ]; then eval "$had_exit_trap"; else trap - EXIT; fi
559:   systemctl daemon-reload
560:   ok "单元已安装：$UNIT_FILE"
561: }
562: 
563: run_config_check() {
564:   step "以服务账号身份检查配置"
565:   local runner=()
566:   if command -v runuser >/dev/null 2>&1; then
567:     runner=(runuser -u "$SERVICE_ACCOUNT" --)
568:   else
569:     runner=(sudo -u "$SERVICE_ACCOUNT" --)
570:   fi
571:   local output
572:   if output=$("${runner[@]}" "$AGENT_BIN" -config "$CONFIG_FILE" -check 2>&1); then
573:     ok "配置检查通过"
574:   else
575:     printf '%s\n' "$output" >&2
576:     die "配置检查失败，服务未启动。请修正后重跑本脚本。"
577:   fi
578: }
579: 
580: start_service() {
581:   step "启用并启动 $SERVICE_NAME"
582:   systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || warn "enable 返回非零"
583:   systemctl restart "$SERVICE_NAME"
584:   sleep 3
585:   if systemctl is-active --quiet "$SERVICE_NAME"; then
586:     ok "服务运行中"
587:   else
588:     systemctl status "$SERVICE_NAME" --no-pager -l | head -n 30 >&2 || true
589:     die "服务未能启动，请查看 journalctl -u $SERVICE_NAME -n 100"
590:   fi
591: }
592: 
593: report() {
594:   step "完成"
595:   info "服务状态：$(systemctl is-active "$SERVICE_NAME")"
596:   info ""
597:   info "接下来："
598:   info "  1. 回到面板查看该服务器是否显示 Agent 在线"
599:   info "  2. 在面板创建节点，确认 applied_revision == desired_revision"
600:   info "  3. 用真实客户端连一次，才算真正可用"
601:   info ""
602:   info "常用命令："
603:   info "  systemctl status $SERVICE_NAME"
604:   info "  journalctl -u $SERVICE_NAME -f"
605:   info "  systemctl restart $SERVICE_NAME"
606:   info ""
607:   info "${C_DIM}注意：注册码已写入 $CONFIG_FILE。首次注册成功后可以删除该字段，"
608:   info "      Agent 的 token 已经存在状态目录里。${C_RESET}"
609:   info "${C_DIM}绝对不要删除 $ARG_STATE_DIR：里面有 Agent 身份、计量基线和未上传的用量队列。${C_RESET}"
610: }
611: 
612: # ---------------------------------------------------------------- 主流程
613: 
614: main() {
615:   info "${C_BOLD}proxypanel 节点端 Agent 部署脚本${C_RESET} v${SCRIPT_VERSION}"
616:   info "${C_DIM}裸机 + systemd；不使用 Docker；不改动面板与防火墙${C_RESET}"
617: 
618:   require_root
619:   require_linux
620:   detect_arch
621:   require_systemd
622:   check_tools
623:   check_disk
624:   collect_inputs
625: 
626:   create_account
627:   install_xray "$ARG_XRAY_VERSION"
628:   install_agent_binary
629:   prepare_state
630:   write_config
631:   if [ -n "${ARG_KEEP_CONFIG:-}" ]; then
632:     # The preserved config may use a different state directory or CA. Do not
633:     # replace its existing unit or trust store using newly supplied parameters.
634:     ok "沿用现有配置、服务单元与 CA，跳过配置检查与重启"
635:     if [ ! -f "$UNIT_FILE" ]; then
636:       warn "现有配置被保留，但 $UNIT_FILE 不存在：本次没有安装单元，服务不会启动。"
637:       warn "如确需重新生成单元，请重跑并选择覆盖现有配置。"
638:     fi
639:     report
640:     return 0
641:   fi
642: 
643:   install_panel_ca
644:   install_unit
645:   run_config_check
646:   start_service
647:   report
648: }
649: 
650: # Sourced by tests (BASH_SOURCE is set) or executed normally. Also tolerate
651: # `bash < deploy-agent.sh`, where BASH_SOURCE is unset and set -u would abort.
652: if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
653:   main "$@"
654: fi
```
