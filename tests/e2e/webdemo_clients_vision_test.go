package e2e

// web-demo group E: real VLESS/REALITY/Vision traffic through the pinned
// official Xray server generator (internal/panel/xrayconf.Build) and the real
// subscription exports (internal/panel/clientconfig.Render).
//
// What this file adds beyond the archived single closure in xray_test.go:
//   - a deterministic 69,632-byte payload with full-body SHA-256 (not a status
//     code, port liveness or config lint),
//   - per-user uplink/downlink counters read back from the real StatsService
//     of the same server process,
//   - a strict post-stop negative: the proxy port must refuse the TCP
//     connection (ECONNREFUSED) and the SOCKS request must fail with that same
//     refusal, never a timeout, followed by a legal restart that serves the
//     exact content again on the same REALITY keys,
//   - the same REALITY server exercised by the sing-box and Mihomo exports in a
//     separate test, so the required Vision cell is not conflated with them.
//
// Only loopback sockets, freshly generated keys and t.TempDir are used. No
// secret is logged: fixtures are throwaway and only sizes/hashes/counters are
// printed.

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"proxypanel/internal/panel/clientconfig"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/xrayconf"
)

const (
	webdemoVisionPayloadBytes = 69632
	webdemoVisionUUID         = "9f0c1d2e-3a4b-4c5d-8e6f-7a8b9c0d1e2f"
	webdemoVisionSNI          = "localhost"
)

func webdemoVisionPayload() []byte {
	unit := []byte("webdemo-vision-payload-")
	buf := bytes.Repeat(unit, webdemoVisionPayloadBytes/len(unit)+1)
	return buf[:webdemoVisionPayloadBytes]
}

// webdemoCore resolves one pinned binary and records its version and SHA-256.
// A missing binary is a hard failure: the matrix must never silently skip.
func webdemoCore(t *testing.T, env, want string, versionArgs ...string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(env))
	if path == "" {
		t.Fatalf("%s is required for the real-core web-demo client evidence", env)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s=%s: %v", env, path, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, versionArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", env, versionArgs, err, out)
	}
	found := false
	for _, field := range strings.Fields(string(out)) {
		if strings.TrimPrefix(field, "v") == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s reports %q; this evidence is pinned to %s", env, strings.TrimSpace(string(out)), want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf(`webdemo_core={"env":%q,"path":%q,"version":%q,"sha256":"%x"}`, env, path, want, sha256.Sum256(raw))
	return path
}

type webdemoVisionFixture struct {
	node      domain.Node
	serverDoc map[string]any
	serverCfg string
	dir       string
	proxyPort int
	apiPort   int
	targetURL string
	payload   []byte
}

func webdemoVisionNode(t *testing.T, proxyPort int, decoyAddr string) domain.Node {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	priv := base64.RawURLEncoding.EncodeToString(key.Bytes())
	pub := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	node := domain.Node{
		ID: "nd_webdemo_vision", Name: "webdemo-vision", Protocol: protocol.VLESS,
		Host: "127.0.0.1", Listen: "127.0.0.1", Port: proxyPort,
		Flow:           "xtls-rprx-vision",
		StreamSettings: domain.NewObject(), ProtocolSettings: domain.NewObject(), ClientSettings: domain.NewObject(),
	}
	_ = node.StreamSettings.Set("network", "tcp")
	_ = node.StreamSettings.Set("security", "reality")
	_ = node.StreamSettings.Set("realitySettings", map[string]any{
		"show": false, "dest": decoyAddr, "serverNames": []string{webdemoVisionSNI},
		"privateKey": priv, "shortIds": []string{"a1b2"},
	})
	_ = node.ProtocolSettings.Set("decryption", "none")
	_ = node.ClientSettings.Set("server_name", webdemoVisionSNI)
	_ = node.ClientSettings.Set("public_key", pub)
	_ = node.ClientSettings.Set("short_id", "a1b2")
	_ = node.ClientSettings.Set("fingerprint", "chrome")
	pan := protocol.Node{
		Protocol: node.Protocol, Flow: node.Flow, Host: node.Host, Port: node.Port, Listen: node.Listen,
		StreamSettings: node.StreamSettings, ProtocolSettings: node.ProtocolSettings,
		ClientSettings: node.ClientSettings, ServerKey: node.ServerKey,
	}
	stream, ps, serverKey, issues := protocol.Sanitize(pan)
	if len(issues) > 0 {
		t.Fatalf("sanitize REALITY node: %v", issues)
	}
	node.StreamSettings, node.ProtocolSettings, node.ServerKey = stream, ps, serverKey
	pan.StreamSettings, pan.ProtocolSettings, pan.ServerKey = stream, ps, serverKey
	if node.ServerKey == "" {
		t.Fatal("REALITY server private key was lost by the save path")
	}
	if issues := protocol.Validate(pan); len(issues) > 0 {
		t.Fatalf("validate REALITY node: %v", issues)
	}
	return node
}

// webdemoVisionFixtureBuild creates the server document exactly as the panel
// would for this node, then adds only the Agent's management API, stats service
// and policy levels so the counters can be read back from the real core.
func webdemoVisionFixtureBuild(t *testing.T) *webdemoVisionFixture {
	t.Helper()
	payload := webdemoVisionPayload()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(target.Close)
	decoy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "webdemo-local-reality-decoy")
	}))
	decoy.EnableHTTP2 = true
	decoy.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	decoy.StartTLS()
	t.Cleanup(decoy.Close)

	proxyPort, apiPort := unusedPort(t), unusedPort(t)
	node := webdemoVisionNode(t, proxyPort, decoy.Listener.Addr().String())
	serverDoc, err := xrayconf.Build(node, []xrayconf.Client{{
		MemberID: "m-webdemo-vision", Email: "webdemo-vision", UUID: webdemoVisionUUID, Flow: "xtls-rprx-vision",
	}})
	if err != nil {
		t.Fatalf("server document: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(serverDoc, &doc); err != nil {
		t.Fatalf("server document json: %v", err)
	}
	doc["api"] = map[string]any{"tag": "api", "services": []string{"StatsService", "HandlerService"}}
	doc["stats"] = map[string]any{}
	doc["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{
		"statsUserUplink": true, "statsUserDownlink": true,
	}}}
	// Xray only counts users whose policy level carries the stats flags.
	for _, raw := range doc["inbounds"].([]any) {
		in := raw.(map[string]any)
		if in["tag"] != "node" {
			continue
		}
		settings, _ := in["settings"].(map[string]any)
		for _, c := range settings["clients"].([]any) {
			c.(map[string]any)["level"] = 0
		}
	}
	inbounds, _ := doc["inbounds"].([]any)
	doc["inbounds"] = append(inbounds, map[string]any{
		"tag": "api", "listen": "127.0.0.1", "port": apiPort, "protocol": "dokodemo-door",
		"settings": map[string]any{"address": "127.0.0.1"},
	})
	doc["routing"] = map[string]any{"rules": []any{
		map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
	}}
	dir := t.TempDir()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "server.json")
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return &webdemoVisionFixture{
		node: node, serverDoc: doc, serverCfg: cfg, dir: dir,
		proxyPort: proxyPort, apiPort: apiPort, targetURL: target.URL, payload: payload,
	}
}

// webdemoLogBuffer is a mutex-guarded log sink: the core keeps writing from its
// own goroutines while the test reads the log for the refusal line.
type webdemoLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *webdemoLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *webdemoLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// webdemoStartCore starts a core and waits for its local listening socket.
func webdemoStartCore(t *testing.T, bin string, args []string, port int) func() {
	_, stop := webdemoStartCoreLog(t, bin, args, port)
	return stop
}

// webdemoStartCoreLog also exposes the captured core log so a negative can cite
// the concrete refusal instead of a bare error string. Cleanup is registered the
// moment the process exists, so t.Fatalf paths can never leak a core; the
// returned stop is idempotent and bounded.
func webdemoStartCoreLog(t *testing.T, bin string, args []string, port int) (func() string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	log := &webdemoLogBuffer{}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("%s did not start: %v", filepath.Base(bin), err)
	}
	pid := cmd.Process.Pid
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Errorf("%s (pid %d) did not exit within 20s of stop", filepath.Base(bin), pid)
			}
		})
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return log.String, stop
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatalf("%s never listened on 127.0.0.1:%d\n%s", filepath.Base(bin), port, webdemoTrim(log.String()))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func webdemoTrim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1200 {
		return s[:1200] + "..."
	}
	return s
}

func webdemoCheck(t *testing.T, bin string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(bin), args, err, webdemoTrim(string(out)))
	}
}

func webdemoSOCKS(port int) *http.Transport {
	return &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", port)})}
}

// webdemoFetch returns the complete body through the SOCKS entry.
func webdemoFetch(transport *http.Transport, target string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Transport: transport, Timeout: timeout}
	res, err := client.Get(target)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", res.StatusCode)
	}
	return body, nil
}

// webdemoStats reads the per-user counters from the real StatsService.
func webdemoStats(t *testing.T, bin string, apiPort int) (up, down int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "api", "statsquery",
		"--server="+fmt.Sprintf("127.0.0.1:%d", apiPort), "-pattern", "user>>>").CombinedOutput()
	if err != nil {
		t.Fatalf("stats API: %v %s", err, out)
	}

	var stats struct {
		Stat []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(out, &stats); err != nil {
		t.Fatalf("stats JSON: %v %s", err, out)
	}
	for _, s := range stats.Stat {
		v := strings.Trim(string(s.Value), `"`)
		switch s.Name {
		case "user>>>webdemo-vision>>>traffic>>>uplink":
			up, _ = strconv.ParseInt(v, 10, 64)
		case "user>>>webdemo-vision>>>traffic>>>downlink":
			down, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return up, down
}

func webdemoAssertPayload(t *testing.T, client string, body, want []byte) string {
	t.Helper()
	if len(body) != len(want) {
		t.Fatalf("%s payload = %d bytes, want %d", client, len(body), len(want))
	}
	got, exp := sha256.Sum256(body), sha256.Sum256(want)
	if got != exp {
		t.Fatalf("%s payload SHA256 = %x, want %x", client, got, exp)
	}
	return fmt.Sprintf("%x", got)
}

// webdemoExport renders the real subscription projection of one node.
func webdemoExport(t *testing.T, format string, n clientconfig.NodeConfig) []byte {
	t.Helper()
	res, err := clientconfig.Render(clientconfig.Request{Format: format, Nodes: []clientconfig.NodeConfig{n}})
	if err != nil {
		t.Fatalf("%s export: %v", format, err)
	}
	return res.Body
}

// webdemoRunXrayClient loads the real Xray export into Xray and fetches the
// target through it.
func webdemoStartXrayClient(t *testing.T, bin string, n clientconfig.NodeConfig, dir string) (int, func() string, func()) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(webdemoExport(t, clientconfig.FormatXray, n), &doc); err != nil {
		t.Fatalf("xray export json: %v", err)
	}
	outbound := webdemoXrayProxy(t, doc)
	stream, _ := outbound["streamSettings"].(map[string]any)
	reality, _ := stream["realitySettings"].(map[string]any)
	if reality == nil || fmt.Sprint(reality["password"]) == "" || fmt.Sprint(reality["shortId"]) == "" {
		t.Fatalf("xray export lost the REALITY client parameters: %v", reality)
	}
	settings, _ := outbound["settings"].(map[string]any)
	vnext, _ := settings["vnext"].([]any)
	if len(vnext) != 1 {
		t.Fatalf("xray export vnext entries = %d", len(vnext))
	}
	users, _ := vnext[0].(map[string]any)["users"].([]any)
	if len(users) != 1 || fmt.Sprint(users[0].(map[string]any)["flow"]) != "xtls-rprx-vision" {
		t.Fatalf("xray export lost the Vision flow: %v", users)
	}
	socks := unusedPort(t)
	doc["inbounds"] = []any{map[string]any{
		"tag": "socks-in", "listen": "127.0.0.1", "port": socks, "protocol": "socks",
		"settings": map[string]any{"udp": false, "auth": "noauth"},
	}}
	// The subscription export sends private destinations direct, which is the
	// correct product behaviour. The fixture target is owned loopback, so this
	// row clears the routing rule to prove the REALITY outbound itself carries
	// the traffic; no outbound parameter is changed.
	doc["routing"] = map[string]any{"domainStrategy": "AsIs", "rules": []any{}}
	// Fixture-only diagnostics: debug logging never changes the outbound. The
	// negative below cites the client core's own refusal line.
	doc["log"] = map[string]any{"loglevel": "debug"}
	cfg, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "xray-client.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	webdemoCheck(t, bin, "run", "-test", "-c", path)
	logs, stop := webdemoStartCoreLog(t, bin, []string{"run", "-c", path}, socks)
	return socks, logs, stop
}

// webdemoFetchThrough starts from a fresh transport on the client SOCKS entry so
// a previous idle connection cannot mask a dead route.
func webdemoFetchThrough(t *testing.T, socks int, target string, timeout time.Duration) []byte {
	t.Helper()
	transport := webdemoSOCKS(socks)
	defer transport.CloseIdleConnections()
	body, err := webdemoFetch(transport, target, timeout)
	if err != nil {
		t.Fatalf("traffic through SOCKS 127.0.0.1:%d: %v", socks, err)
	}
	return body
}

func webdemoXrayProxy(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	outbounds, _ := doc["outbounds"].([]any)
	for _, raw := range outbounds {
		ob, _ := raw.(map[string]any)
		switch fmt.Sprint(ob["protocol"]) {
		case "", "freedom", "blackhole":
			continue
		default:
			return ob
		}
	}
	t.Fatal("xray export has no proxy outbound")
	return nil
}

func webdemoStartSingboxClient(t *testing.T, bin string, n clientconfig.NodeConfig, dir string) (int, func()) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(webdemoExport(t, clientconfig.FormatSingbox, n), &doc); err != nil {
		t.Fatalf("sing-box export json: %v", err)
	}
	outbounds, _ := doc["outbounds"].([]any)
	var proxy map[string]any
	for _, raw := range outbounds {
		ob, _ := raw.(map[string]any)
		if fmt.Sprint(ob["type"]) != "direct" && ob["type"] != nil {
			proxy = ob
		}
	}
	if proxy == nil {
		t.Fatal("sing-box export has no proxy outbound")
	}
	tlsBlock, _ := proxy["tls"].(map[string]any)
	reality, _ := tlsBlock["reality"].(map[string]any)
	if reality == nil || fmt.Sprint(reality["public_key"]) == "" {
		t.Fatalf("sing-box export lost REALITY: %v", tlsBlock)
	}
	if fmt.Sprint(proxy["flow"]) != "xtls-rprx-vision" {
		t.Fatalf("sing-box export lost the Vision flow: %v", proxy["flow"])
	}
	// The bundled route sends private IPs direct, which is correct product
	// behaviour but would bypass the proxy for the owned loopback target. This
	// row forces the final route to the exported outbound; the outbound itself
	// is untouched.
	doc["route"] = map[string]any{"rules": []any{}, "final": fmt.Sprint(proxy["tag"])}
	socks := unusedPort(t)
	doc["inbounds"] = []any{map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": socks}}
	cfg, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "singbox-client.json")
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	webdemoCheck(t, bin, "check", "-c", path)
	return socks, webdemoStartCore(t, bin, []string{"run", "-c", path}, socks)
}

func webdemoStartMihomoClient(t *testing.T, bin string, n clientconfig.NodeConfig, dir string) (int, func()) {
	t.Helper()
	var exported map[string]any
	if err := yaml.Unmarshal(webdemoExport(t, clientconfig.FormatMihomo, n), &exported); err != nil {
		t.Fatalf("mihomo export yaml: %v", err)
	}
	proxies, _ := exported["proxies"].([]any)
	if len(proxies) != 1 {
		t.Fatalf("mihomo export carried %d proxies", len(proxies))
	}
	proxy, _ := proxies[0].(map[string]any)
	opts, _ := proxy["reality-opts"].(map[string]any)
	if opts == nil || fmt.Sprint(opts["public-key"]) == "" {
		t.Fatalf("mihomo export lost REALITY: %v", proxy)
	}
	if fmt.Sprint(proxy["flow"]) != "xtls-rprx-vision" {
		t.Fatalf("mihomo export lost the Vision flow: %v", proxy["flow"])
	}
	name, _ := proxy["name"].(string)
	socks := unusedPort(t)
	cfg := map[string]any{
		"allow-lan": false, "log-level": "warning", "mixed-port": socks, "mode": "rule",
		"proxies":      []any{proxy},
		"proxy-groups": []any{map[string]any{"name": "WEBDEMO", "type": "select", "proxies": []any{name}}},
		"rules":        []any{"MATCH,WEBDEMO"},
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mihomo-client.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(dir, "mihomo-work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	webdemoCheck(t, bin, "-t", "-f", path, "-d", work)
	return socks, webdemoStartCore(t, bin, []string{"-f", path, "-d", work}, socks)
}

// webdemoStatsAfter traffic polls the real StatsService until the counters show
// the transfer this cell just made, or the bounded deadline expires. It never
// substitutes a synthetic value: the returned deltas are read from the core.
func webdemoStatsAfter(t *testing.T, bin string, apiPort int, up0, down0 int64, want int) (int64, int64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var up, down int64
	for {
		up, down = webdemoStats(t, bin, apiPort)
		if up-up0 > 0 && down-down0 >= int64(want) {
			return up - up0, down - down0
		}
		if time.Now().After(deadline) {
			return up - up0, down - down0
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// webdemoVisionCell logs one matrix cell with real content and counters.
func webdemoVisionCell(t *testing.T, client string, body, want []byte, upDelta, downDelta int64) {
	t.Helper()
	sum := webdemoAssertPayload(t, client, body, want)
	if upDelta <= 0 {
		t.Fatalf("%s uplink counter did not advance: %d", client, upDelta)
	}
	if downDelta < int64(len(want)) {
		t.Fatalf("%s downlink counter %d < payload %d", client, downDelta, len(want))
	}
	t.Logf(`webdemo_cell={"client":%q,"protocol":"vless","network":"tcp","security":"reality","flow":"xtls-rprx-vision","result":"supported_pass","bytes":%d,"sha256":%q,"uplink":%d,"downlink":%d}`,
		client, len(body), sum, upDelta, downDelta)
}

// TestWebDemoVisionTrafficStatsAndStop is the required REALITY+Vision cell: full
// payload and SHA-256 through the real exported Xray client config, real
// uplink/downlink counters from the same server process, strict post-stop
// refusal, then a legal restart control that serves the exact content again.
func TestWebDemoVisionTrafficStatsAndStop(t *testing.T) {
	xrayBin := webdemoCore(t, "XRAY_BIN", "26.3.27", "version")
	fixture := webdemoVisionFixtureBuild(t)
	ep, issues := protocol.EndpointFor(fixture.node)
	if len(issues) > 0 {
		t.Fatalf("endpoint: %v", issues)
	}
	if ep.Security != "reality" || ep.Flow != "xtls-rprx-vision" || ep.PublicKey == "" || ep.ShortID == "" {
		t.Fatalf("endpoint lost REALITY/Vision parameters: %+v", ep)
	}
	nc := clientconfig.NodeConfig{Endpoint: ep, Credential: webdemoVisionUUID}

	webdemoCheck(t, xrayBin, "run", "-test", "-c", fixture.serverCfg)
	stopServer := webdemoStartCore(t, xrayBin, []string{"run", "-c", fixture.serverCfg}, fixture.proxyPort)

	// Positive A: the real exported Xray client config, full payload + counters.
	// The client core's own t.Cleanup is registered inside the helper.
	socks, clientLog, _ := webdemoStartXrayClient(t, xrayBin, nc, fixture.dir)
	up0, down0 := webdemoStats(t, xrayBin, fixture.apiPort)
	body := webdemoFetchThrough(t, socks, fixture.targetURL, 20*time.Second)
	up1, down1 := webdemoStatsAfter(t, xrayBin, fixture.apiPort, up0, down0, len(fixture.payload))
	sum := webdemoAssertPayload(t, "xray", body, fixture.payload)
	webdemoVisionCell(t, "xray", body, fixture.payload, up1, down1)
	t.Logf(`webdemo_positive={"stage":"xray-export-client","bytes":%d,"sha256":%q,"uplink":%d,"downlink":%d}`,
		len(body), sum, up1, down1)

	// Strict negative: stop the server. The client core stays up, so the SOCKS
	// entry is live and only the REALITY server can be the failing part.
	stopServer()
	dialErr := webdemoAwaitRefusal(fixture.proxyPort)
	if dialErr == nil {
		t.Fatalf("the stopped core still accepted TCP on the REALITY port %d", fixture.proxyPort)
	}
	var netErr net.Error
	if errors.As(dialErr, &netErr) && netErr.Timeout() {
		t.Fatalf("not_proven: dial to the stopped core timed out instead of being refused: %v", dialErr)
	}
	if !webdemoRefused(dialErr) {
		t.Fatalf("not_proven: stopped-core dial diagnostic is not a refusal: %v", dialErr)
	}
	liveClient := webdemoSOCKS(socks)
	_, proxyErr := webdemoFetch(liveClient, fixture.targetURL, 5*time.Second)
	liveClient.CloseIdleConnections()
	if proxyErr == nil {
		t.Fatal("proxy traffic succeeded through a live client after the server was stopped")
	}
	if errors.As(proxyErr, &netErr) && netErr.Timeout() {
		t.Fatalf("not_proven: post-stop proxy failure was a timeout, not a refusal: %v", proxyErr)
	}
	// The client core is still alive, so the failure must be its own refusal to
	// connect to the stopped REALITY port, not a vanished SOCKS listener.
	refusal := ""
	waitLog := time.Now().Add(10 * time.Second)
	for time.Now().Before(waitLog) {
		text := clientLog()
		if strings.Contains(text, fmt.Sprintf("127.0.0.1:%d", fixture.proxyPort)) && webdemoRefusedText(text) {
			refusal = webdemoRefusalLine(text, fixture.proxyPort)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if refusal == "" {
		t.Fatalf("not_proven: client core log has no concrete refusal for the stopped REALITY port %d\n%s",
			fixture.proxyPort, webdemoTrim(clientLog()))
	}
	t.Logf("webdemo_client_refusal=%q", refusal)
	t.Logf(`webdemo_negative={"case":"stopped-core-refuses","port":%d,"dial":%q,"proxy":%q,"refused":true,"timeout":false}`,
		fixture.proxyPort, dialErr.Error(), proxyErr.Error())

	// Legal before/after control: restart the same document and serve the exact
	// content again through the same client core and the same REALITY keys.
	stopServer = webdemoStartCore(t, xrayBin, []string{"run", "-c", fixture.serverCfg}, fixture.proxyPort)
	up2, down2 := webdemoStats(t, xrayBin, fixture.apiPort)
	after := webdemoFetchThrough(t, socks, fixture.targetURL, 20*time.Second)
	up3, down3 := webdemoStatsAfter(t, xrayBin, fixture.apiPort, up2, down2, len(fixture.payload))
	sum = webdemoAssertPayload(t, "xray", after, fixture.payload)
	if down3 < int64(len(fixture.payload)) {
		t.Fatalf("post-restart downlink delta %d < payload %d", down3, len(fixture.payload))
	}
	if up3 <= 0 {
		t.Fatalf("post-restart uplink delta did not advance: %d", up3)
	}
	t.Logf(`webdemo_recovery={"case":"restart-serves-same-content","bytes":%d,"sha256":%q,"uplink":%d,"downlink":%d}`,
		len(after), sum, up3, down3)
}

// webdemoAwaitRefusal waits until a dial to port fails and returns that error.
func webdemoAwaitRefusal(port int) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			return err
		}
		_ = conn.Close()
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}

// webdemoRefusedText reports whether a core log shows a refused or reset dial.
func webdemoRefusedText(text string) bool {
	low := strings.ToLower(text)
	return strings.Contains(low, "connection refused") || strings.Contains(low, "reset by peer") ||
		strings.Contains(low, "connection reset")
}

// webdemoRefusalLine returns the concrete log line mentioning the port.
func webdemoRefusalLine(text string, port int) string {
	marker := fmt.Sprintf("127.0.0.1:%d", port)
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, marker) && webdemoRefusedText(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func webdemoRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused")
}

// TestWebDemoRealityCrossClient runs the sing-box and Mihomo exports against the
// same real REALITY+Vision server. A failure here is a real compatibility result
// for that client, not something to hide behind the Xray row.
func TestWebDemoRealityCrossClient(t *testing.T) {
	xrayBin := webdemoCore(t, "XRAY_BIN", "26.3.27", "version")
	singboxBin := webdemoCore(t, "SING_BOX_BIN", "1.14.3", "version")
	mihomoBin := webdemoCore(t, "MIHOMO_BIN", "1.19.33", "-v")
	fixture := webdemoVisionFixtureBuild(t)
	ep, issues := protocol.EndpointFor(fixture.node)
	if len(issues) > 0 {
		t.Fatalf("endpoint: %v", issues)
	}
	nc := clientconfig.NodeConfig{Endpoint: ep, Credential: webdemoVisionUUID}
	webdemoCheck(t, xrayBin, "run", "-test", "-c", fixture.serverCfg)
	stop := webdemoStartCore(t, xrayBin, []string{"run", "-c", fixture.serverCfg}, fixture.proxyPort)
	defer stop()

	for _, client := range []struct {
		name  string
		bin   string
		start func(*testing.T, string, clientconfig.NodeConfig, string) (int, func())
	}{
		{"sing-box", singboxBin, webdemoStartSingboxClient},
		{"mihomo", mihomoBin, webdemoStartMihomoClient},
	} {
		client := client
		t.Run(client.name, func(t *testing.T) {
			socks, stopClient := client.start(t, client.bin, nc, fixture.dir)
			defer stopClient()
			up0, down0 := webdemoStats(t, xrayBin, fixture.apiPort)
			body := webdemoFetchThrough(t, socks, fixture.targetURL, 20*time.Second)
			up1, down1 := webdemoStatsAfter(t, xrayBin, fixture.apiPort, up0, down0, len(fixture.payload))
			webdemoVisionCell(t, client.name, body, fixture.payload, up1, down1)
		})
	}
}
