package e2e

// TestDockerClientMatrix is the A15 client matrix. It builds the member
// subscription through the real panel API for every protocol the contract
// declares (VLESS, VMess, Trojan/TLS, Shadowsocks and Shadowsocks 2022), takes
// the real exports of the fixed client formats, loads a single protocol
// outbound/proxy from each export into the pinned Windows cores (Xray,
// sing-box, Mihomo) with only the local SOCKS entry and the forced route
// changed, and then asserts the complete 69,632 byte payload through the real
// SSH tunnel to http://target:18080/.
//
// Supported-combination rejections stay in the exporter unit tests
// (internal/panel/clientconfig/clientconfig_test.go TestUnsupportedTransportIsExplicit);
// this test never fakes a client and never claims a run that did not happen.
// v2rayN is a GUI: its share links are verified here, the import itself is
// recorded as not-run because the GUI step is owned by the parent agent.
//
// Secrets (UUIDs, passwords, subscription tokens, Shadowsocks 2022 PSKs) are
// never logged. Idempotent state lives in the protected docker fixture
// directory as client-fixture.json.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	matrixTargetURL      = "http://target:18080/"
	matrixPayloadUnit    = "verified-payload-"
	matrixPayloadRepeats = 4096
	matrixTrojanSNI      = "matrix-trojan.test"
	matrixGroupName      = "client-matrix-group"
	matrixSubscription   = "client-matrix-subscription"
	matrixMemberName     = "client-matrix-member"
)

// matrixSpec is one node the matrix owns. Ports follow the existing docker
// fixture (Agent 1 keeps 20101 for VLESS, Agent 2 keeps 20201) and the tunnel
// forwards both ranges.
type matrixSpec struct {
	Key      string
	Name     string
	Protocol string
	Cipher   string
	Security string
	Server   int
	Port     int
	Cert     bool
}

var matrixSpecs = []matrixSpec{
	{Key: "vmess-1", Name: "client-matrix-vmess-1", Protocol: "vmess", Security: "none", Server: 0, Port: 20102},
	{Key: "trojan-tls-1", Name: "client-matrix-trojan-tls-1", Protocol: "trojan", Security: "tls", Server: 0, Port: 20103, Cert: true},
	{Key: "ss-1", Name: "client-matrix-ss-1", Protocol: "shadowsocks", Cipher: "aes-128-gcm", Security: "none", Server: 0, Port: 20104},
	{Key: "ss2022-1", Name: "client-matrix-ss2022-1", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm", Security: "none", Server: 0, Port: 20105},
	{Key: "vmess-2", Name: "client-matrix-vmess-2", Protocol: "vmess", Security: "none", Server: 1, Port: 20202},
	{Key: "trojan-tls-2", Name: "client-matrix-trojan-tls-2", Protocol: "trojan", Security: "tls", Server: 1, Port: 20203, Cert: true},
	{Key: "ss-2", Name: "client-matrix-ss-2", Protocol: "shadowsocks", Cipher: "aes-128-gcm", Security: "none", Server: 1, Port: 20204},
	{Key: "ss2022-2", Name: "client-matrix-ss2022-2", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm", Security: "none", Server: 1, Port: 20205},
}

// matrixOrder is the report order: the docker fixture VLESS nodes first, then
// the matrix owned nodes of each agent.
var matrixOrder = []struct {
	Key    string
	Server int
}{
	{"vless-1", 0}, {"vmess-1", 0}, {"trojan-tls-1", 0}, {"ss-1", 0}, {"ss2022-1", 0},
	{"vless-2", 1}, {"vmess-2", 1}, {"trojan-tls-2", 1}, {"ss-2", 1}, {"ss2022-2", 1},
}

type matrixState struct {
	Nodes        map[string]string `json:"nodes"`
	Group        string            `json:"group"`
	Subscription string            `json:"subscription"`
	Member       string            `json:"member"`
	Username     string            `json:"username"`
}

type matrixNode struct {
	Key      string
	ID       string
	Name     string
	Protocol string
	Cipher   string
	Network  string
	Security string
	Port     int
	Server   int
}

type matrixRow struct {
	Client   string
	Version  string
	Node     string
	Protocol string
	Port     string
	Result   string
}

type matrixBinaries struct {
	xray, singbox, mihomo    string
	xrayV, singboxV, mihomoV string
}

func matrixPayload() string { return strings.Repeat(matrixPayloadUnit, matrixPayloadRepeats) }

func TestDockerClientMatrix(t *testing.T) {
	d := dockerSetup(t)
	if len(d.fixture.Servers) < 2 || len(d.fixture.Nodes) < 2 {
		t.Fatal("docker fixture must define two servers and two VLESS nodes")
	}
	bins := matrixPinnedBinaries(t)
	// The agent containers are created by the docker acceptance lifecycle; this
	// test only starts them so the matrix can be run on its own.
	d.remote("start agent1")
	d.remote("start agent2")
	state := matrixEnsure(t, d)
	nodes := matrixNodes(t, d, state)
	d.applied()

	live := matrixPreflight(t, d, nodes)
	exports := matrixFetchExports(t, d, state.Username)
	matrixVerifyV2rayN(t, exports.v2rayn, nodes)

	var rows []matrixRow
	versions := map[string]string{"xray": bins.xrayV, "sing-box": bins.singboxV, "mihomo": bins.mihomoV, "v2rayN": "not-run"}
	for _, n := range nodes {
		label := n.Protocol + "/" + n.Network + "/" + matrixSecurity(n.Security)
		port := strconv.Itoa(n.Port)

		xrayOut, xrayProblem := matrixXrayView(t, exports.xray, n)
		sbOut, sbProblem := matrixSingboxView(t, exports.singbox, n)
		mhProxy, mhProblem := matrixMihomoView(t, exports.mihomo, n)
		if xrayProblem == "" && sbOut != nil && mhProxy != nil {
			matrixCredentialEquality(t, n, xrayOut, sbOut, mhProxy)
		}

		blocker, blocked := matrixNodeBlocker(live, n.Key)
		switch {
		case xrayProblem != "":
			t.Errorf("xray export for node %q (%s): %s", n.Name, label, xrayProblem)
			rows = append(rows, matrixRow{"xray", bins.xrayV, n.Name, label, port, "export mismatch: " + xrayProblem})
		case blocked:
			t.Errorf("xray outbound for node %q (%s) was not exercised: %s", n.Name, label, blocker)
			rows = append(rows, matrixRow{"xray", bins.xrayV, n.Name, label, port, "blocked: " + blocker})
		default:
			row := matrixRunXray(t, bins.xray, bins.xrayV, n, label, xrayOut)
			if !strings.HasPrefix(row.Result, "payload") {
				t.Errorf("xray client row %s %s: %s", n.Name, label, row.Result)
			}
			rows = append(rows, row)
		}

		switch {
		case sbProblem != "":
			t.Errorf("sing-box export for node %q (%s): %s", n.Name, label, sbProblem)
			rows = append(rows, matrixRow{"sing-box", bins.singboxV, n.Name, label, port, "export mismatch: " + sbProblem})
		case blocked:
			t.Errorf("sing-box outbound for node %q (%s) was not exercised: %s", n.Name, label, blocker)
			rows = append(rows, matrixRow{"sing-box", bins.singboxV, n.Name, label, port, "blocked: " + blocker})
		default:
			row := matrixRunSingbox(t, bins.singbox, bins.singboxV, n, label, sbOut)
			if !strings.HasPrefix(row.Result, "payload") {
				t.Errorf("sing-box client row %s %s: %s", n.Name, label, row.Result)
			}
			rows = append(rows, row)
		}

		switch {
		case mhProblem != "":
			t.Errorf("mihomo export for node %q (%s): %s", n.Name, label, mhProblem)
			rows = append(rows, matrixRow{"mihomo", bins.mihomoV, n.Name, label, port, "export mismatch: " + mhProblem})
		case blocked:
			t.Errorf("mihomo proxy for node %q (%s) was not exercised: %s", n.Name, label, blocker)
			rows = append(rows, matrixRow{"mihomo", bins.mihomoV, n.Name, label, port, "blocked: " + blocker})
		default:
			row := matrixRunMihomo(t, bins.mihomo, bins.mihomoV, n, label, mhProxy)
			if !strings.HasPrefix(row.Result, "payload") {
				t.Errorf("mihomo client row %s %s: %s", n.Name, label, row.Result)
			}
			rows = append(rows, row)
		}

		rows = append(rows, matrixRow{"v2rayN", "not-run", n.Name, label, port,
			"not-run: GUI import is the parent step; exported share link verified"})
	}
	matrixReport(t, versions, rows)
}

// ─── pinned binaries ───────────────────────────────────────────────────────

func matrixPinnedBinaries(t *testing.T) matrixBinaries {
	t.Helper()
	b := matrixBinaries{
		xray: os.Getenv("XRAY_BIN"), singbox: os.Getenv("SING_BOX_BIN"), mihomo: os.Getenv("MIHOMO_BIN"),
	}
	for name, path := range map[string]string{"XRAY_BIN": b.xray, "SING_BOX_BIN": b.singbox, "MIHOMO_BIN": b.mihomo} {
		if strings.TrimSpace(path) == "" {
			t.Fatalf("%s is required: the A15 matrix must run against the pinned Windows cores", name)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s is unusable: %v", name, err)
		}
	}
	b.xrayV = matrixVersion(t, b.xray, "version")
	b.singboxV = matrixVersion(t, b.singbox, "version")
	b.mihomoV = matrixVersion(t, b.mihomo, "-v")
	for _, pinned := range []struct{ name, got, want string }{
		{"Xray", b.xrayV, "26.3.27"},
		{"sing-box", b.singboxV, "1.14.3"},
		{"Mihomo", b.mihomoV, "1.19.32"},
	} {
		if !strings.Contains(pinned.got, pinned.want) {
			t.Errorf("%s version %q does not match the pinned %s", pinned.name, pinned.got, pinned.want)
		}
	}
	return b
}

func matrixVersion(t *testing.T, binary string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("core version probe failed: %v %s", err, matrixTrim(out))
	}
	return strings.TrimSpace(matrixFirstLine(string(out)))
}

func matrixFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// ─── idempotent fixture ────────────────────────────────────────────────────

func matrixEnsure(t *testing.T, d *dockerTest) *matrixState {
	t.Helper()
	statePath := filepath.Join(d.private, "client-fixture.json")
	st := &matrixState{Nodes: map[string]string{}, Username: matrixMemberName}
	if raw, err := os.ReadFile(statePath); err == nil {
		if json.Unmarshal(raw, st) != nil || st.Nodes == nil {
			t.Fatalf("invalid saved client matrix fixture %s", statePath)
		}
		st.Username = matrixMemberName
	}
	st.Nodes["vless-1"] = d.fixture.Nodes[0]
	st.Nodes["vless-2"] = d.fixture.Nodes[1]

	var certLines, keyLines []string
	for _, spec := range matrixSpecs {
		serverID := d.fixture.Servers[spec.Server]
		if id := st.Nodes[spec.Key]; id != "" {
			if _, problem := matrixNodeFromAPI(d, spec.Key, id, spec.Server); problem != "" {
				t.Fatalf("saved node %q is unusable (%s); delete %s to recreate the matrix", spec.Key, problem, statePath)
			}
			continue
		}
		if id := matrixFindNode(t, d, serverID, spec.Name); id != "" {
			st.Nodes[spec.Key] = id
			continue
		}
		body := map[string]any{
			"name": spec.Name, "server_id": serverID, "enabled": true,
			"host": "127.0.0.1", "listen": "0.0.0.0", "port": spec.Port,
			"protocol": spec.Protocol, "cipher": spec.Cipher,
			"stream_settings":   map[string]any{"network": "tcp", "security": spec.Security},
			"protocol_settings": map[string]any{},
			"client_settings":   map[string]any{},
		}
		if spec.Cert {
			if certLines == nil {
				certLines, keyLines = matrixCertificate(t)
			}
			stream := body["stream_settings"].(map[string]any)
			stream["tlsSettings"] = map[string]any{"certificates": []any{map[string]any{
				"certificate": certLines, "key": keyLines,
			}}}
			body["client_settings"] = map[string]any{"server_name": matrixTrojanSNI, "allow_insecure": true}
		}
		if strings.HasPrefix(spec.Cipher, "2022-") {
			// Shadowsocks 2022 needs a server PSK; it is random per environment
			// and only the panel stores it (encrypted). It is never logged.
			body["server_key"] = matrixServerKey(t)
		}
		created := d.api.request("POST", "/api/v1/admin/nodes", body)
		id, _ := created["id"].(string)
		if id == "" {
			t.Fatalf("node %q create response lacks an id", spec.Name)
		}
		st.Nodes[spec.Key] = id
	}
	matrixSaveState(t, d, st)

	var nodeIDs []string
	for _, order := range matrixOrder {
		id := st.Nodes[order.Key]
		if id == "" {
			t.Fatalf("matrix node %q has no id", order.Key)
		}
		nodeIDs = append(nodeIDs, id)
	}
	st.Group = matrixEnsureGroup(t, d, st.Group, nodeIDs)
	st.Subscription = matrixEnsureSubscription(t, d, st.Subscription, st.Group)
	st.Member = matrixEnsureMember(t, d, st.Member, st.Subscription)
	matrixSaveState(t, d, st)
	return st
}

func matrixSaveState(t *testing.T, d *dockerTest, st *matrixState) {
	t.Helper()
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatalf("encode client matrix fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d.private, "client-fixture.json"), raw, 0600); err != nil {
		t.Fatalf("save client matrix fixture: %v", err)
	}
}

func matrixEnsureGroup(t *testing.T, d *dockerTest, id string, nodeIDs []string) string {
	t.Helper()
	if id == "" {
		for _, item := range d.matrixItems("/api/v1/admin/node-groups") {
			if matrixStr(item["name"]) == matrixGroupName {
				id = matrixStr(item["id"])
			}
		}
	}
	if id == "" {
		created := d.api.request("POST", "/api/v1/admin/node-groups", map[string]any{"name": matrixGroupName, "node_ids": nodeIDs})
		return matrixStr(created["id"])
	}
	group := d.api.request("GET", "/api/v1/admin/node-groups/"+id, nil)
	if !matrixSameSet(matrixStrings(group["node_ids"]), nodeIDs) {
		d.api.request("PATCH", "/api/v1/admin/node-groups/"+id, map[string]any{
			"version": group["version"], "node_ids": nodeIDs,
		})
	}
	return id
}

func matrixEnsureSubscription(t *testing.T, d *dockerTest, id, groupID string) string {
	t.Helper()
	if id == "" {
		for _, item := range d.matrixItems("/api/v1/admin/subscriptions") {
			if matrixStr(item["name"]) == matrixSubscription {
				id = matrixStr(item["id"])
			}
		}
	}
	if id == "" {
		created := d.api.request("POST", "/api/v1/admin/subscriptions", map[string]any{
			"name": matrixSubscription, "node_group_ids": []string{groupID},
			"quota_bytes": 1 << 30, "timezone": "UTC", "anchor_day": 1,
			"allow_new_bindings": true, "access_enabled": true,
		})
		return matrixStr(created["id"])
	}
	sub := d.api.request("GET", "/api/v1/admin/subscriptions/"+id, nil)
	if !matrixSameSet(matrixStrings(sub["node_group_ids"]), []string{groupID}) {
		d.api.request("PATCH", "/api/v1/admin/subscriptions/"+id, map[string]any{
			"version": sub["version"], "node_group_ids": []string{groupID},
		})
	}
	return id
}

func matrixEnsureMember(t *testing.T, d *dockerTest, id, subscriptionID string) string {
	t.Helper()
	if id == "" {
		for _, item := range d.matrixItems("/api/v1/admin/members") {
			if matrixStr(item["username"]) == matrixMemberName {
				id = matrixStr(item["id"])
			}
		}
	}
	if id == "" {
		created := d.api.request("POST", "/api/v1/admin/members", map[string]any{
			"username": matrixMemberName, "password": d.password, "role": "member",
			"subscription_id": subscriptionID, "active": true, "proxy_enabled": true,
		})
		return matrixStr(created["id"])
	}
	member := d.api.request("GET", "/api/v1/admin/members/"+id, nil)
	patch := map[string]any{"version": member["version"]}
	changed := false
	if matrixStr(member["subscription_id"]) != subscriptionID {
		patch["subscription_id"] = subscriptionID
		changed = true
	}
	if active, _ := member["active"].(bool); !active {
		patch["active"] = true
		changed = true
	}
	if proxy, _ := member["proxy_enabled"].(bool); !proxy {
		patch["proxy_enabled"] = true
		changed = true
	}
	if changed {
		d.api.request("PATCH", "/api/v1/admin/members/"+id, patch)
	}
	return id
}

func matrixFindNode(t *testing.T, d *dockerTest, serverID, name string) string {
	t.Helper()
	for _, item := range d.matrixItems("/api/v1/admin/nodes?server_id=" + url.QueryEscape(serverID)) {
		if matrixStr(item["name"]) == name {
			return matrixStr(item["id"])
		}
	}
	return ""
}

// matrixItems reads one of the panel list endpoints. writeList responses are
// not wrapped in "data", so platformAPI.request cannot be used here.
func (d *dockerTest) matrixItems(path string) []map[string]any {
	d.t.Helper()
	res, err := d.api.client.Get(d.api.base + path)
	if err != nil {
		d.t.Fatalf("list %s failed: %v", path, err)
	}
	defer res.Body.Close()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		d.t.Fatalf("list %s returned invalid JSON", path)
	}
	if res.StatusCode != http.StatusOK {
		d.t.Fatalf("list %s returned %d", path, res.StatusCode)
	}
	return body.Items
}

func matrixNodes(t *testing.T, d *dockerTest, st *matrixState) []matrixNode {
	t.Helper()
	out := make([]matrixNode, 0, len(matrixOrder))
	for _, order := range matrixOrder {
		n, problem := matrixNodeFromAPI(d, order.Key, st.Nodes[order.Key], order.Server)
		if problem != "" {
			t.Fatalf("node %q: %s", order.Key, problem)
		}
		out = append(out, n)
	}
	return out
}

func matrixNodeFromAPI(d *dockerTest, key, id string, server int) (matrixNode, string) {
	if id == "" {
		return matrixNode{}, "missing id"
	}
	res, err := d.api.client.Get(d.api.base + "/api/v1/admin/nodes/" + url.PathEscape(id))
	if err != nil {
		return matrixNode{}, "read failed"
	}
	defer res.Body.Close()
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || body.Data == nil {
		return matrixNode{}, fmt.Sprintf("read returned %d", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return matrixNode{}, fmt.Sprintf("read returned %d", res.StatusCode)
	}
	stream := matrixObj(body.Data["stream_settings"])
	n := matrixNode{
		Key: key, ID: id, Name: matrixStr(body.Data["name"]), Protocol: matrixStr(body.Data["protocol"]),
		Cipher: matrixStr(body.Data["cipher"]), Network: matrixStr(stream["network"]),
		Security: matrixNormSecurity(matrixStr(stream["security"])),
		Port:     matrixNum(body.Data["port"]), Server: server,
	}
	if n.Port == 0 || n.Name == "" {
		return matrixNode{}, "node record is incomplete"
	}
	return n, ""
}

func matrixSecurity(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func matrixNormSecurity(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "none") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// ─── server side preflight ─────────────────────────────────────────────────

// matrixPreflight waits for every node port to accept the tunnel forward and
// returns the blocker for the nodes that never listen. The result contains
// only blocked nodes: absence from the map means the node is ready and has to
// be exercised, presence means it is blocked (see matrixNodeBlocker).
func matrixPreflight(t *testing.T, d *dockerTest, nodes []matrixNode) map[string]string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		missing := 0
		for _, n := range nodes {
			if !matrixDial(n.Port) {
				missing++
			}
		}
		if missing == 0 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	out := map[string]string{}
	for _, n := range nodes {
		if matrixDial(n.Port) {
			continue
		}
		out[n.Key] = fmt.Sprintf("tunnel port 127.0.0.1:%d never accepted a connection (core_instances.state=%q)",
			n.Port, matrixInstanceState(d, n.ID))
	}
	return out
}

// matrixNodeBlocker reads one node out of the preflight result. matrixPreflight
// reports only the nodes that never accepted a connection, so presence in the
// map is the blocker and absence means the node is ready; an empty preflight
// result therefore never blocks a cell and every healthy cell is executed for
// real. A reported node without text still yields a stable reason so a blocked
// row never reads "blocked: " with nothing after it.
func matrixNodeBlocker(live map[string]string, key string) (string, bool) {
	reason, blocked := live[key]
	if !blocked {
		return "", false
	}
	if strings.TrimSpace(reason) == "" {
		reason = "preflight reported the node as blocked without a reason"
	}
	return reason, true
}

func matrixDial(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 700*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func matrixInstanceState(d *dockerTest, nodeID string) string {
	var state string
	if err := d.db.QueryRow(context.Background(), `select coalesce(state,'') from core_instances where node_id=$1`, nodeID).Scan(&state); err != nil {
		return "unknown"
	}
	return state
}

// ─── exports ───────────────────────────────────────────────────────────────

type matrixExports struct {
	xray, singbox map[string]any
	mihomo        map[string]any
	v2rayn        string
}

func matrixFetchExports(t *testing.T, d *dockerTest, username string) matrixExports {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	member := &platformAPI{t: t, base: d.api.base, client: &http.Client{Jar: jar, Transport: d.transport, Timeout: 20 * time.Second}}
	member.login(username, d.password)
	sub := member.request("GET", "/api/v1/me/subscription", nil)
	token := matrixStr(sub["token"])
	if token == "" {
		t.Fatal("member subscription token is empty")
	}
	if formats := matrixStrings(sub["formats"]); !matrixSameSet(formats, []string{"xray", "mihomo", "singbox", "v2rayn"}) {
		t.Errorf("advertised subscription formats = %v", formats)
	}
	var out matrixExports
	for _, format := range []string{"xray", "singbox", "mihomo", "v2rayn"} {
		status, body := matrixRawExport(t, member.client, d.api.base+"/sub/"+token+"?format="+format)
		if status != http.StatusOK {
			t.Fatalf("export %q returned %d (%s)", format, status, matrixExportProblem(body))
		}
		switch format {
		case "xray":
			if err := json.Unmarshal(body, &out.xray); err != nil {
				t.Fatalf("xray export is not JSON: %v", err)
			}
		case "singbox":
			if err := json.Unmarshal(body, &out.singbox); err != nil {
				t.Fatalf("sing-box export is not JSON: %v", err)
			}
		case "mihomo":
			if err := yaml.Unmarshal(body, &out.mihomo); err != nil {
				t.Fatalf("mihomo export is not YAML: %v", err)
			}
		case "v2rayn":
			out.v2rayn = string(body)
		}
	}
	return out
}

func matrixRawExport(t *testing.T, client *http.Client, rawURL string) (int, []byte) {
	t.Helper()
	res, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("subscription export request failed: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		t.Fatalf("subscription export read failed: %v", err)
	}
	return res.StatusCode, body
}

// matrixExportProblem reduces a failed export to the stable, secret free part
// of the panel error envelope: the error code, the message and the
// node/field/reason details. Raw response bodies, credentials, UUIDs,
// passwords, tokens, certificates and private keys are never printed.
func matrixExportProblem(body []byte) string {
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error.Code == "" {
		return "no stable error code in the response"
	}
	out := "code=" + envelope.Error.Code
	if message := strings.TrimSpace(envelope.Error.Message); message != "" {
		out += " message=" + strconv.Quote(matrixRedact(message))
	}
	for _, key := range []string{"node", "field", "reason"} {
		if value, ok := envelope.Error.Details[key].(string); ok && strings.TrimSpace(value) != "" {
			out += " " + key + "=" + strconv.Quote(matrixRedact(value))
		}
	}
	return out
}

// matrixRedact drops everything that can carry a credential from a diagnostic
// string: UUIDs and token, key or PEM sized base64/hex runs.
func matrixRedact(text string) string {
	fields := strings.Fields(text)
	for i, field := range fields {
		bare := strings.Trim(field, `"'(),;:[]{}`)
		if matrixUUID(bare) || matrixOpaque(bare) {
			fields[i] = "<redacted>"
		}
	}
	out := strings.Join(fields, " ")
	if len(out) > 300 {
		out = out[:300] + "..."
	}
	return out
}

// matrixOpaque reports whether a token is only made of credential alphabet and
// is long enough to be a secret rather than a field path.
func matrixOpaque(token string) bool {
	if len(token) < 32 {
		return false
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '+', r == '/', r == '=', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// TestMatrixExportProblemIsSecretFree pins the diagnostic format: a failed
// export only ever reports the panel error code, its message and the
// node/field/reason details, with UUIDs and credential sized runs redacted.
func TestMatrixExportProblemIsSecretFree(t *testing.T) {
	uuid := "11111111-2222-4333-8444-555555555555"
	psk := "MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="
	body := []byte(`{"error":{"code":"protocol_not_supported","message":"xray: node \"client-matrix-trojan-tls-1\" ` +
		`cannot represent client_settings.allow_insecure (secret ` + uuid + ` and ` + psk + `)",` +
		`"details":{"node":"client-matrix-trojan-tls-1","field":"client_settings.allow_insecure","reason":"no pin"}}}`)
	got := matrixExportProblem(body)
	for _, want := range []string{"code=protocol_not_supported", "client-matrix-trojan-tls-1",
		"field=\"client_settings.allow_insecure\"", "reason=\"no pin\""} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic %q lacks %q", got, want)
		}
	}
	for _, leak := range []string{uuid, psk, "{"} {
		if strings.Contains(got, leak) {
			t.Fatalf("diagnostic %q leaks %q", got, leak)
		}
	}
	if got := matrixExportProblem([]byte("<html>upstream 502</html>")); strings.Contains(got, "upstream") {
		t.Fatalf("non JSON bodies must not be echoed: %q", got)
	}
}

// TestMatrixPreflightBlockerSemantics locks the contract between
// matrixPreflight and the per cell switch: matrixPreflight reports only the
// nodes that never accepted a connection. Absence from the result therefore
// means ready - the cell must run the real pinned client - and presence means
// blocked with that reason. The inverted reading reported every healthy cell as
// "not exercised" with an empty reason and replaced the real client runs.
func TestMatrixPreflightBlockerSemantics(t *testing.T) {
	// positive: an empty preflight result keeps every cell on the real run path
	for _, order := range matrixOrder {
		if blocker, blocked := matrixNodeBlocker(map[string]string{}, order.Key); blocked {
			t.Fatalf("node %q was blocked by an empty preflight result: %q", order.Key, blocker)
		}
	}
	// positive: a nil result behaves like an empty one
	if blocker, blocked := matrixNodeBlocker(nil, "vless-1"); blocked {
		t.Fatalf("nil preflight result blocked a node: %q", blocker)
	}

	// negative: only the reported node is blocked and it keeps its reason
	live := map[string]string{
		"ss2022-2": `tunnel port 127.0.0.1:20205 never accepted a connection (core_instances.state="stopped")`,
	}
	for _, order := range matrixOrder {
		blocker, blocked := matrixNodeBlocker(live, order.Key)
		if order.Key != "ss2022-2" {
			if blocked {
				t.Fatalf("node %q was blocked although the preflight did not report it: %q", order.Key, blocker)
			}
			continue
		}
		if !blocked {
			t.Fatalf("node %q would run a client although the preflight blocked it", order.Key)
		}
		if !strings.Contains(blocker, "20205") || !strings.Contains(blocker, "never accepted a connection") {
			t.Fatalf("blocked node lost its reason: %q", blocker)
		}
	}

	// negative: a reported node without text still carries a stable reason, so
	// the report never shows a bare "blocked: "
	if blocker, blocked := matrixNodeBlocker(map[string]string{"vmess-2": "   "}, "vmess-2"); !blocked || strings.TrimSpace(blocker) == "" {
		t.Fatalf("blocked node without text: blocked=%t reason=%q", blocked, blocker)
	}
}

// TestMatrixReportCountsOnlyRealRuns pins the report bookkeeping: only a
// payload row proves a real client run and only the explicit v2rayN not-run
// excuse is neutral; a blocked or mismatched cell is always a failure.
func TestMatrixReportCountsOnlyRealRuns(t *testing.T) {
	rows := []matrixRow{
		{Result: "payload 69632B ok"},
		{Result: "not-run: GUI import is the parent step; exported share link verified"},
		{Result: "blocked: tunnel port 127.0.0.1:20205 never accepted a connection"},
		{Result: "export mismatch: expected exactly one outbound for port 20103, found 0"},
		{Result: "fetch failed: context deadline exceeded"},
	}
	if got := matrixFailedRows(rows); got != 3 {
		t.Fatalf("failed rows = %d, want 3", got)
	}
	if got := matrixFailedRows(nil); got != 0 {
		t.Fatalf("empty report counts = %d", got)
	}
}

// matrixVerifyV2rayN checks the share links of the fourth format without
// printing them and without pretending that a GUI imported them.
func matrixVerifyV2rayN(t *testing.T, body string, nodes []matrixNode) {
	t.Helper()
	seen := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		scheme, port, ok := matrixShareLink(line)
		if !ok {
			t.Errorf("v2rayN export has a link that cannot be parsed (scheme prefix %q)", matrixPrefix(line))
			continue
		}
		if prev, dup := seen[port]; dup {
			t.Errorf("v2rayN export repeats port %d (%s and %s)", port, prev, scheme)
		}
		seen[port] = scheme
	}
	for _, n := range nodes {
		scheme, ok := seen[n.Port]
		if !ok {
			t.Errorf("v2rayN export has no share link for node %q port %d", n.Name, n.Port)
			continue
		}
		if want := matrixShareScheme(n.Protocol); scheme != want {
			t.Errorf("v2rayN share link for node %q is %s, want %s", n.Name, scheme, want)
		}
	}
}

func matrixPrefix(line string) string {
	if i := strings.Index(line, "://"); i > 0 && i < 12 {
		return line[:i]
	}
	return "unknown"
}

func matrixShareScheme(protocol string) string {
	if protocol == "shadowsocks" {
		return "ss"
	}
	return protocol
}

func matrixShareLink(line string) (string, int, bool) {
	scheme := matrixPrefix(line)
	switch scheme {
	case "vmess":
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "vmess://"))
		if err != nil {
			return "", 0, false
		}
		var payload struct {
			Add  string `json:"add"`
			Port string `json:"port"`
		}
		if json.Unmarshal(raw, &payload) != nil || payload.Add != "127.0.0.1" {
			return "", 0, false
		}
		port, err := strconv.Atoi(payload.Port)
		if err != nil {
			return "", 0, false
		}
		return scheme, port, true
	case "vless", "trojan", "ss":
		u, err := url.Parse(line)
		if err != nil || u.Hostname() != "127.0.0.1" {
			return "", 0, false
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			return "", 0, false
		}
		return scheme, port, true
	}
	return "", 0, false
}

// ─── export parameter views (no secret is ever logged) ─────────────────────

// matrixXrayView selects the one exported Xray outbound of the node and checks
// that the protocol parameters survived the export.
func matrixXrayView(t *testing.T, doc map[string]any, n matrixNode) (map[string]any, string) {
	t.Helper()
	var found map[string]any
	count := 0
	for _, raw := range matrixArr(doc["outbounds"]) {
		ob := matrixObj(raw)
		switch matrixStr(ob["protocol"]) {
		case "", "freedom", "blackhole":
			continue
		}
		if matrixXrayPort(ob) == n.Port {
			found, count = ob, count+1
		}
	}
	if count != 1 {
		return nil, fmt.Sprintf("expected exactly one outbound for port %d, found %d", n.Port, count)
	}
	if got := matrixStr(found["protocol"]); got != n.Protocol {
		return nil, fmt.Sprintf("outbound protocol %q, node protocol %q", got, n.Protocol)
	}
	stream := matrixObj(found["streamSettings"])
	if got := matrixStr(stream["network"]); got != n.Network {
		return nil, fmt.Sprintf("outbound transport %q, node transport %q", got, n.Network)
	}
	if got := matrixNormSecurity(matrixStr(stream["security"])); got != n.Security {
		return nil, fmt.Sprintf("outbound security %q, node security %q", got, n.Security)
	}
	settings := matrixObj(found["settings"])
	switch n.Protocol {
	case "vless", "vmess":
		vnext := matrixObj(matrixFirst(settings["vnext"]))
		if matrixStr(vnext["address"]) != "127.0.0.1" || matrixNum(vnext["port"]) != n.Port {
			return nil, "vnext endpoint does not match the node"
		}
		user := matrixObj(matrixFirst(vnext["users"]))
		id := matrixStr(user["id"])
		if !matrixUUID(id) {
			return nil, "vnext user id is missing or not a UUID"
		}
		if n.Protocol == "vless" {
			if got := matrixStr(user["encryption"]); got != "none" {
				return nil, fmt.Sprintf("vless encryption %q, want none", got)
			}
			if got := matrixStr(user["flow"]); got != "" {
				return nil, fmt.Sprintf("vless flow %q but the node has none", got)
			}
		} else {
			if got := matrixNum(user["alterId"]); got != 0 {
				return nil, fmt.Sprintf("vmess alterId %d, want 0", got)
			}
			if got := matrixStr(user["security"]); got != "auto" {
				return nil, fmt.Sprintf("vmess security %q, want auto", got)
			}
		}
		return found, ""
	case "trojan", "shadowsocks":
		server := matrixObj(matrixFirst(settings["servers"]))
		if matrixStr(server["address"]) != "127.0.0.1" || matrixNum(server["port"]) != n.Port {
			return nil, "server endpoint does not match the node"
		}
		if matrixStr(server["password"]) == "" {
			return nil, "server password is empty"
		}
		if n.Protocol == "shadowsocks" && matrixStr(server["method"]) != n.Cipher {
			return nil, fmt.Sprintf("shadowsocks method %q, node cipher %q", matrixStr(server["method"]), n.Cipher)
		}
		if n.Security == "tls" {
			tls := matrixObj(stream["tlsSettings"])
			if matrixStr(tls["serverName"]) != matrixTrojanSNI {
				return nil, fmt.Sprintf("tls serverName %q, node client SNI %q", matrixStr(tls["serverName"]), matrixTrojanSNI)
			}
			// Xray 26 removed allowInsecure; the export must pin the certificate
			// hash instead (hex SHA-256).
			if _, removed := tls["allowInsecure"]; removed {
				return nil, "tls still carries the removed allowInsecure option"
			}
			if pin := matrixStr(tls["pinnedPeerCertSha256"]); len(pin) != 64 || strings.Trim(pin, "0123456789abcdef") != "" {
				return nil, fmt.Sprintf("tls pinnedPeerCertSha256 %q is not a certificate SHA-256 hash", pin)
			}
		}
		return found, ""
	}
	return nil, "unsupported protocol " + n.Protocol
}

func matrixXrayPort(ob map[string]any) int {
	settings := matrixObj(ob["settings"])
	if vnext := matrixObj(matrixFirst(settings["vnext"])); vnext != nil {
		return matrixNum(vnext["port"])
	}
	if server := matrixObj(matrixFirst(settings["servers"])); server != nil {
		return matrixNum(server["port"])
	}
	return 0
}

func matrixSingboxView(t *testing.T, doc map[string]any, n matrixNode) (map[string]any, string) {
	t.Helper()
	var found map[string]any
	count := 0
	for _, raw := range matrixArr(doc["outbounds"]) {
		ob := matrixObj(raw)
		if matrixStr(ob["type"]) == "direct" || matrixStr(ob["type"]) == "" {
			continue
		}
		if matrixNum(ob["server_port"]) == n.Port {
			found, count = ob, count+1
		}
	}
	if count != 1 {
		return nil, fmt.Sprintf("expected exactly one outbound for port %d, found %d", n.Port, count)
	}
	if got := matrixStr(found["type"]); got != n.Protocol {
		return nil, fmt.Sprintf("outbound type %q, node protocol %q", got, n.Protocol)
	}
	if got := matrixStr(found["server"]); got != "127.0.0.1" {
		return nil, fmt.Sprintf("outbound server %q, want 127.0.0.1", got)
	}
	switch n.Protocol {
	case "vless", "vmess":
		if !matrixUUID(matrixStr(found["uuid"])) {
			return nil, "outbound uuid is missing or not a UUID"
		}
		if n.Protocol == "vmess" {
			if got := matrixNum(found["alter_id"]); got != 0 {
				return nil, fmt.Sprintf("vmess alter_id %d, want 0", got)
			}
			if got := matrixStr(found["security"]); got != "auto" {
				return nil, fmt.Sprintf("vmess security %q, want auto", got)
			}
		} else if got := matrixStr(found["flow"]); got != "" {
			return nil, fmt.Sprintf("vless flow %q but the node has none", got)
		}
	case "trojan", "shadowsocks":
		if matrixStr(found["password"]) == "" {
			return nil, "outbound password is empty"
		}
		if n.Protocol == "shadowsocks" && matrixStr(found["method"]) != n.Cipher {
			return nil, fmt.Sprintf("shadowsocks method %q, node cipher %q", matrixStr(found["method"]), n.Cipher)
		}
	}
	if n.Security == "tls" {
		tls := matrixObj(found["tls"])
		if enabled, _ := tls["enabled"].(bool); !enabled {
			return nil, "tls.enabled was dropped"
		}
		if matrixStr(tls["server_name"]) != matrixTrojanSNI {
			return nil, fmt.Sprintf("tls.server_name %q, node client SNI %q", matrixStr(tls["server_name"]), matrixTrojanSNI)
		}
		if insecure, _ := tls["insecure"].(bool); !insecure {
			return nil, "tls.insecure was dropped"
		}
	}
	return found, ""
}

func matrixMihomoView(t *testing.T, doc map[string]any, n matrixNode) (map[string]any, string) {
	t.Helper()
	var found map[string]any
	count := 0
	for _, raw := range matrixArr(doc["proxies"]) {
		proxy := matrixObj(raw)
		if matrixNum(proxy["port"]) == n.Port {
			found, count = proxy, count+1
		}
	}
	if count != 1 {
		return nil, fmt.Sprintf("expected exactly one proxy for port %d, found %d", n.Port, count)
	}
	want := n.Protocol
	if want == "shadowsocks" {
		want = "ss"
	}
	if got := matrixStr(found["type"]); got != want {
		return nil, fmt.Sprintf("proxy type %q, node protocol %q", got, want)
	}
	if got := matrixStr(found["server"]); got != "127.0.0.1" {
		return nil, fmt.Sprintf("proxy server %q, want 127.0.0.1", got)
	}
	switch n.Protocol {
	case "vless", "vmess":
		if !matrixUUID(matrixStr(found["uuid"])) {
			return nil, "proxy uuid is missing or not a UUID"
		}
		if n.Protocol == "vmess" {
			if got := matrixNum(found["alterId"]); got != 0 {
				return nil, fmt.Sprintf("vmess alterId %d, want 0", got)
			}
			if got := matrixStr(found["cipher"]); got != "auto" {
				return nil, fmt.Sprintf("vmess cipher %q, want auto", got)
			}
		}
	case "trojan", "shadowsocks":
		if matrixStr(found["password"]) == "" {
			return nil, "proxy password is empty"
		}
		if n.Protocol == "shadowsocks" && matrixStr(found["cipher"]) != n.Cipher {
			return nil, fmt.Sprintf("shadowsocks cipher %q, node cipher %q", matrixStr(found["cipher"]), n.Cipher)
		}
	}
	if n.Security == "tls" {
		if enabled, _ := found["tls"].(bool); !enabled {
			return nil, "proxy tls was dropped"
		}
		if matrixStr(found["servername"]) != matrixTrojanSNI {
			return nil, fmt.Sprintf("proxy servername %q, node client SNI %q", matrixStr(found["servername"]), matrixTrojanSNI)
		}
		if skip, _ := found["skip-cert-verify"].(bool); !skip {
			return nil, "proxy skip-cert-verify was dropped"
		}
	}
	return found, ""
}

// matrixCredentialEquality proves the three exports carry the same member
// credential and method without ever printing it.
func matrixCredentialEquality(t *testing.T, n matrixNode, xrayOut, sbOut, mhProxy map[string]any) {
	t.Helper()
	xrayCred, xrayMethod := matrixXrayCredential(xrayOut, n.Protocol)
	sbCred, sbMethod := matrixSingboxCredential(sbOut, n.Protocol)
	mhCred, mhMethod := matrixMihomoCredential(mhProxy, n.Protocol)
	if xrayCred != sbCred || xrayCred != mhCred {
		t.Errorf("node %q: exported client credential differs between xray, sing-box and mihomo", n.Name)
	}
	if n.Protocol == "shadowsocks" && (xrayMethod != sbMethod || xrayMethod != mhMethod) {
		t.Errorf("node %q: exported shadowsocks method differs between formats", n.Name)
	}
	if n.Protocol == "shadowsocks" && strings.HasPrefix(n.Cipher, "2022-") {
		// Multi-user 2022 clients must present <serverPSK>:<userPSK>; a bare
		// user PSK is rejected by every pinned core.
		if !strings.Contains(xrayCred, ":") || !strings.Contains(sbCred, ":") || !strings.Contains(mhCred, ":") {
			t.Errorf("node %q: shadowsocks 2022 credential does not carry the server PSK", n.Name)
		}
	}
}

func matrixXrayCredential(out map[string]any, protocol string) (string, string) {
	settings := matrixObj(out["settings"])
	if protocol == "vless" || protocol == "vmess" {
		return matrixStr(matrixObj(matrixFirst(matrixObj(matrixFirst(settings["vnext"]))["users"]))["id"]), ""
	}
	server := matrixObj(matrixFirst(settings["servers"]))
	return matrixStr(server["password"]), matrixStr(server["method"])
}

func matrixSingboxCredential(out map[string]any, protocol string) (string, string) {
	if protocol == "vless" || protocol == "vmess" {
		return matrixStr(out["uuid"]), ""
	}
	return matrixStr(out["password"]), matrixStr(out["method"])
}

func matrixMihomoCredential(proxy map[string]any, protocol string) (string, string) {
	if protocol == "vless" || protocol == "vmess" {
		return matrixStr(proxy["uuid"]), ""
	}
	return matrixStr(proxy["password"]), matrixStr(proxy["cipher"])
}

// ─── real client runs ─────────────────────────────────────────────────────

func matrixRunXray(t *testing.T, binary, version string, n matrixNode, label string, out map[string]any) matrixRow {
	t.Helper()
	row := matrixRow{Client: "xray", Version: version, Node: n.Name, Protocol: label, Port: strconv.Itoa(n.Port)}
	tag := matrixStr(out["tag"])
	check := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "socks-in", "listen": "127.0.0.1", "port": unusedPort(t),
			"protocol": "socks", "settings": map[string]any{"udp": true, "auth": "noauth"},
		}},
		"outbounds": []any{out, map[string]any{"tag": "direct", "protocol": "freedom"}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": tag}}},
	}
	path := writeConfig(t, t.TempDir(), "matrix-xray-client.json", check)
	if err := matrixCoreCheck(t, binary, "run", "-test", "-c", path); err != nil {
		row.Result = "config load rejected: " + err.Error()
		return row
	}
	client, transport, _ := dockerProxy(t, map[string]any{"outbounds": []any{out}})
	defer transport.CloseIdleConnections()
	if dockerFetch(client) {
		row.Result = fmt.Sprintf("payload %dB ok", len(matrixPayload()))
		return row
	}
	_, err := client.Get(matrixTargetURL)
	row.Result = "fetch failed: " + matrixError(err)
	return row
}

func matrixRunSingbox(t *testing.T, binary, version string, n matrixNode, label string, out map[string]any) matrixRow {
	t.Helper()
	row := matrixRow{Client: "sing-box", Version: version, Node: n.Name, Protocol: label, Port: strconv.Itoa(n.Port)}
	port := unusedPort(t)
	doc := map[string]any{
		"log":       map[string]any{"level": "warn"},
		"inbounds":  []any{map[string]any{"type": "socks", "tag": "socks-in", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []any{out, map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": matrixStr(out["tag"])},
	}
	path := matrixWriteFile(t, "matrix-singbox-client.json", doc, true)
	if err := matrixCoreCheck(t, binary, "check", "-c", path); err != nil {
		row.Result = "config load rejected: " + err.Error()
		return row
	}
	if err := matrixStartCore(t, binary, []string{"run", "-c", path}, port); err != nil {
		row.Result = "start failed: " + err.Error()
		return row
	}
	row.Result = matrixProbe(port)
	return row
}

func matrixRunMihomo(t *testing.T, binary, version string, n matrixNode, label string, proxy map[string]any) matrixRow {
	t.Helper()
	row := matrixRow{Client: "mihomo", Version: version, Node: n.Name, Protocol: label, Port: strconv.Itoa(n.Port)}
	port := unusedPort(t)
	name := matrixStr(proxy["name"])
	doc := map[string]any{
		"allow-lan":  false,
		"log-level":  "warning",
		"mixed-port": port,
		"mode":       "rule",
		"proxies":    []any{proxy},
		"proxy-groups": []any{map[string]any{
			"name": "MATRIX", "type": "select", "proxies": []any{name},
		}},
		"rules": []any{"MATCH,MATRIX"},
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		row.Result = "client config encode failed"
		return row
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "matrix-mihomo-client.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		row.Result = "client config write failed"
		return row
	}
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0700); err != nil {
		row.Result = "client work dir failed"
		return row
	}
	if err := matrixCoreCheck(t, binary, "-t", "-f", path, "-d", work); err != nil {
		row.Result = "config load rejected: " + err.Error()
		return row
	}
	if err := matrixStartCore(t, binary, []string{"-f", path, "-d", work}, port); err != nil {
		row.Result = "start failed: " + err.Error()
		return row
	}
	row.Result = matrixProbe(port)
	return row
}

func matrixProbe(port int) string {
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", port)})}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	res, err := client.Get(matrixTargetURL)
	if err != nil {
		return "fetch failed: " + matrixError(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "read failed: " + matrixError(err)
	}
	if string(body) != matrixPayload() {
		return fmt.Sprintf("payload mismatch: %d of %d bytes", len(body), len(matrixPayload()))
	}
	return fmt.Sprintf("payload %dB ok", len(body))
}

func matrixError(err error) string {
	if err == nil {
		return "no response"
	}
	return err.Error()
}

func matrixCoreCheck(t *testing.T, binary string, args ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, matrixTrim(out))
	}
	return nil
}

func matrixStartCore(t *testing.T, binary string, args []string, port int) error {
	t.Helper()
	cmd := exec.Command(binary, args...)
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		return err
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond); err == nil {
			conn.Close()
			return nil
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return fmt.Errorf("core never listened on 127.0.0.1:%d: %s", port, matrixTrim(logs.Bytes()))
}

func matrixWriteFile(t *testing.T, name string, doc any, asJSON bool) string {
	t.Helper()
	var (
		raw []byte
		err error
	)
	if asJSON {
		raw, err = json.Marshal(doc)
	} else {
		raw, err = yaml.Marshal(doc)
	}
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func matrixTrim(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return "no core output"
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	out := strings.Join(lines, " | ")
	if len(out) > 400 {
		out = out[len(out)-400:]
	}
	return out
}

// ─── certificate and secrets ───────────────────────────────────────────────

// matrixCertificate builds a throwaway self-signed certificate for the Trojan
// TLS inbound. The node settings carry it inline, so no Agent mount is needed;
// the private key is only ever sent to the panel as node stream settings and is
// never logged. Clients trust it because the node declares allow_insecure, which
// the panel now maps onto a pinnedPeerCertSha256 pin of this certificate.
func matrixCertificate(t *testing.T) ([]string, []string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test certificate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: matrixTrojanSNI},
		DNSNames:     []string{matrixTrojanSNI},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(720 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("encode test certificate key: %v", err)
	}
	return matrixPEMMLines(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		matrixPEMMLines(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

func matrixPEMMLines(block []byte) []string {
	out := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(block)), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// matrixServerKey returns a fresh 16 byte Shadowsocks 2022 server PSK for
// 2022-blake3-aes-128-gcm. It is never logged and only the panel keeps it.
func matrixServerKey(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generate shadowsocks 2022 key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// ─── report and small helpers ──────────────────────────────────────────────

func matrixReport(t *testing.T, versions map[string]string, rows []matrixRow) {
	t.Helper()
	var b strings.Builder
	b.WriteString("A15 client matrix (real exports, pinned Windows cores, SSH tunnel)\n")
	b.WriteString("target http://target:18080/ payload 69632 bytes\n")
	fmt.Fprintf(&b, "rows with a real defect or blocker: %d of %d\n", matrixFailedRows(rows), len(rows))
	for _, client := range []string{"xray", "sing-box", "mihomo", "v2rayN"} {
		fmt.Fprintf(&b, "%-9s %s\n", client, versions[client])
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%-9s %-16s %-26s %-22s %-6s %s\n", "client", "version", "node", "protocol", "port", "result")
	for _, row := range rows {
		fmt.Fprintf(&b, "%-9s %-16s %-26s %-22s %-6s %s\n",
			row.Client, matrixShortVersion(row.Version), row.Node, row.Protocol, row.Port, row.Result)
	}
	t.Log(b.String())
}

// matrixFailedRows counts the rows that did not prove a real run: a payload row
// passed and a v2rayN row is explicitly not-run, everything else (export
// mismatch, preflight blocker, failed fetch) is a defect or a blocker.
func matrixFailedRows(rows []matrixRow) int {
	failed := 0
	for _, row := range rows {
		if !strings.HasPrefix(row.Result, "payload") && !strings.HasPrefix(row.Result, "not-run") {
			failed++
		}
	}
	return failed
}

func matrixShortVersion(v string) string {
	fields := strings.Fields(v)
	for _, f := range fields {
		if strings.ContainsAny(f, "0123456789") && strings.Contains(f, ".") {
			return strings.TrimPrefix(f, "v")
		}
	}
	if v == "" {
		return "not-run"
	}
	return v
}

func matrixObj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func matrixArr(v any) []any {
	a, _ := v.([]any)
	return a
}

func matrixStr(v any) string {
	s, _ := v.(string)
	return s
}

func matrixStrings(v any) []string {
	out := []string{}
	for _, raw := range matrixArr(v) {
		if s := matrixStr(raw); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func matrixNum(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case uint64:
		return int(n)
	}
	return 0
}

func matrixFirst(v any) any {
	arr := matrixArr(v)
	if len(arr) == 0 {
		return nil
	}
	return arr[0]
}

func matrixSameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]int{}
	for _, v := range got {
		seen[v]++
	}
	for _, v := range want {
		seen[v]--
		if seen[v] < 0 {
			return false
		}
	}
	return true
}

func matrixUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}
