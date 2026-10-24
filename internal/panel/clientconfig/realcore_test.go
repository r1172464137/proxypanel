//go:build corematrix

// Package clientconfig local real-core client matrix. It drives the real
// exporters of this package and the real server document of
// internal/panel/xrayconf into the pinned official cores, then asserts a
// complete payload through each client's SOCKS entry:
//
//	go test -tags corematrix ./internal/panel/clientconfig -run TestLocalCore -v
//
// The cores are the ones pinned by the A15 acceptance (Xray 26.3.27, sing-box
// 1.14.3, Mihomo 1.19.32). Only loopback sockets are used; nothing here needs
// Docker or the SSH tunnel used by the Docker matrix.
package clientconfig

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/xrayconf"
)

const (
	localPayloadUnit    = "verified-payload-"
	localPayloadRepeats = 4096
	localSNI            = "matrix-trojan.test"
	localUUID           = "11111111-2222-4333-8444-555555555555"
)

func localPayload() string { return strings.Repeat(localPayloadUnit, localPayloadRepeats) }

type localCores struct{ xray, singbox, mihomo string }

// localPinnedCores resolves the three fixed binaries and refuses to run against
// any other version. An operator may explicitly pin a local core with
// <BIN_ENV>_VERSION (for example MIHOMO_BIN_VERSION=1.19.33); this does not
// replace or upgrade a binary. The actual version and binary SHA256 are logged.
func localPinnedCores(t *testing.T) localCores {
	t.Helper()
	cores := localCores{
		xray:    localCore(t, "XRAY_BIN", `E:\SDK\Xray\v26.3.27\xray.exe`, []string{"version"}, "26.3.27"),
		singbox: localCore(t, "SING_BOX_BIN", `E:\SDK\sing-box\1.14.3\sing-box-1.14.3-windows-amd64\sing-box.exe`, []string{"version"}, "1.14.3"),
		mihomo:  localCore(t, "MIHOMO_BIN", `E:\SDK\mihomo\1.19.32\mihomo-windows-amd64.exe`, []string{"-v"}, "1.19.32"),
	}
	return cores
}

func localCore(t *testing.T, env, fallback string, versionArgs []string, wantVersion string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(env))
	if path == "" {
		path = fallback
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not available (%s): %v", env, path, err)
	}
	if pin := strings.TrimSpace(os.Getenv(env + "_VERSION")); pin != "" {
		wantVersion = pin
	}
	out := localOutput(t, path, versionArgs...)
	matched := false
	for _, field := range strings.Fields(out) {
		if strings.TrimPrefix(field, "v") == wantVersion {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("%s reports %q, the pinned version is %s", env, strings.TrimSpace(out), wantVersion)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("core env=%s path=%s version=%s sha256=%x", env, path, wantVersion, sha256.Sum256(raw))
	return path
}

func localOutput(t *testing.T, bin string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", filepath.Base(bin), args, err, out)
	}
	return string(out)
}

func localCheck(t *testing.T, bin string, args ...string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("core config check: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, localTrimmed(out))
	}
	return nil
}

// Only a running core's nonzero check exit is a configuration rejection.
// Missing executables, cancellation and wall timeouts are infrastructure failures.
func localRequireConfigRejection(t *testing.T, err error) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() <= 0 {
		t.Fatalf("core check failed without a configuration rejection: %v", err)
	}
}

func localRequireTrafficRejection(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("invalid credentials still produced traffic")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("traffic timeout is not evidence of credential rejection: %v", err)
	}
}

func localAssertPayload(t *testing.T, client, body, want string) {
	t.Helper()
	if body != want {
		t.Fatalf("%s client payload = %d bytes, want %d (content mismatch)", client, len(body), len(want))
	}
	gotHash, wantHash := sha256.Sum256([]byte(body)), sha256.Sum256([]byte(want))
	if gotHash != wantHash {
		t.Fatalf("%s payload SHA256 = %x, want %x", client, gotHash, wantHash)
	}
	t.Logf(`matrix_cell={"client":%q,"result":"supported_pass","bytes":%d,"sha256":"%x"}`, client, len(body), gotHash)
}

func localTrimmed(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 800 {
		return s[:800] + "..."
	}
	return s
}

func localFreePort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		return port
	}
	t.Fatal("no free loopback port")
	return 0
}

// localStart launches a core and waits for its listening socket. The returned
// function stops it and reports the log when the core died early.
func localStart(t *testing.T, bin string, args []string, port int) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("%s did not start: %v", filepath.Base(bin), err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		_ = cmd.Wait()
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return stop
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatalf("%s never listened on 127.0.0.1:%d\n%s", filepath.Base(bin), port, localTrimmed(log.Bytes()))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// localFetch fetches the target through the SOCKS entry of a client.
func localFetch(port int, target string) (string, error) {
	transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", port)})}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	res, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", res.StatusCode)
	}
	return string(body), nil
}

func localWrite(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func localJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	return raw
}

func localObject(t *testing.T, raw string) domain.Object {
	t.Helper()
	var o domain.Object
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("bad object %s: %v", raw, err)
	}
	return o
}

// localCertificate creates the one-off self-signed TLS certificate used by the
// matrix and returns it with the pin derived from its public part.
func localCertificate(t *testing.T) (certPEM, keyPEM, pin string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(20261021),
		Subject:               pkix.Name{CommonName: localSNI},
		DNSNames:              []string{localSNI},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		hex.EncodeToString(sum[:])
}

// localCase is one protocol shape of the matrix.
type localCase struct {
	key      string
	build    func(port int) (domain.Node, string, xrayconf.Client)
	rejected map[string]string // client -> expected UnsupportedError field
}

func localSS2022PSK(t *testing.T) (serverPSK, userPSK string) {
	t.Helper()
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	serverPSK = base64.StdEncoding.EncodeToString(buf)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return serverPSK, base64.StdEncoding.EncodeToString(buf)
}

func localCases(t *testing.T, certPEM, keyPEM string) []localCase {
	t.Helper()
	serverPSK, userPSK := localSS2022PSK(t)
	base := func(protocolName, cipher string, stream, clientSettings domain.Object) domain.Node {
		return domain.Node{
			ID: "nd_" + protocolName, Name: "local-" + protocolName, Protocol: protocolName,
			Cipher: cipher, Host: "127.0.0.1", Listen: "127.0.0.1",
			StreamSettings: stream, ProtocolSettings: domain.NewObject(), ClientSettings: clientSettings,
		}
	}
	plain := func() domain.Object { return localObject(t, `{"network":"tcp","security":"none"}`) }
	// Xray requires the inline certificate and key as string arrays.
	tlsStream := localObject(t, `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"`+localSNI+`","certificates":[{"certificate":[`+localQuote(t, certPEM)+`],"key":[`+localQuote(t, keyPEM)+`]}]}}`)
	tlsClient := localObject(t, `{"server_name":"`+localSNI+`","allow_insecure":true}`)
	emptyClient := domain.NewObject()

	return []localCase{
		{
			key: "vless-tcp-none",
			build: func(port int) (domain.Node, string, xrayconf.Client) {
				n := base(protocol.VLESS, "", plain(), emptyClient)
				n.Port = port
				return n, localUUID, xrayconf.Client{MemberID: "m-1", Email: "m-1", UUID: localUUID}
			},
		},
		{
			key: "vmess-tcp-none",
			build: func(port int) (domain.Node, string, xrayconf.Client) {
				n := base(protocol.VMess, "", plain(), emptyClient)
				n.Port = port
				return n, localUUID, xrayconf.Client{MemberID: "m-1", Email: "m-1", UUID: localUUID}
			},
		},
		{
			key: "trojan-tcp-tls",
			build: func(port int) (domain.Node, string, xrayconf.Client) {
				n := base(protocol.Trojan, "", tlsStream, tlsClient)
				n.Port = port
				return n, "trojan-secret", xrayconf.Client{MemberID: "m-1", Email: "m-1", Password: "trojan-secret"}
			},
		},
		{
			// The real machine shape of defect 5: a ws+tls node whose server
			// tlsSettings announces h2 first. Xray connected, sing-box and
			// Mihomo timed out until the exports offered http/1.1 and Mihomo
			// carried skip-cert-verify.
			key: "vless-ws-tls",
			build: func(port int) (domain.Node, string, xrayconf.Client) {
				stream := localObject(t, `{"network":"ws","security":"tls",`+
					`"wsSettings":{"path":"/matrix-ws","headers":{"Host":"`+localSNI+`"}},`+
					`"tlsSettings":{"serverName":"`+localSNI+`","alpn":["h2","http/1.1"],`+
					`"certificates":[{"certificate":[`+localQuote(t, certPEM)+`],"key":[`+localQuote(t, keyPEM)+`]}]}}`)
				n := base(protocol.VLESS, "", stream, tlsClient)
				n.Port = port
				return n, localUUID, xrayconf.Client{MemberID: "m-1", Email: "m-1", UUID: localUUID}
			},
		},
		{
			key: "trojan-tcp-none",
			build: func(port int) (domain.Node, string, xrayconf.Client) {
				n := base(protocol.Trojan, "", plain(), emptyClient)
				n.Port = port
				return n, "trojan-secret", xrayconf.Client{MemberID: "m-1", Email: "m-1", Password: "trojan-secret"}
			},
			// Mihomo always wraps trojan in TLS; the export must refuse instead
			// of emitting a proxy that silently switches transport.
			rejected: map[string]string{"mihomo": "stream_settings.security"},
		},
		{
			key: "ss-aes-128-gcm",
			build: func(port int) (domain.Node, string, xrayconf.Client) {
				n := base(protocol.Shadowsocks, "aes-128-gcm", plain(), emptyClient)
				n.Port = port
				return n, "legacy-secret", xrayconf.Client{MemberID: "m-1", Email: "m-1", Password: "legacy-secret"}
			},
		},
		{
			key: "ss2022-blake3-aes-128-gcm",
			build: func(port int) (domain.Node, string, xrayconf.Client) {
				n := base(protocol.Shadowsocks, "2022-blake3-aes-128-gcm", plain(), emptyClient)
				n.Port = port
				n.ServerKey = serverPSK
				return n, userPSK, xrayconf.Client{MemberID: "m-1", Email: "m-1", Password: userPSK}
			},
		},
	}
}

// localQuote encodes a PEM document as a JSON string.
func localQuote(t *testing.T, v string) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestLocalCoreClientMatrix loads the real exports into the three pinned cores
// and requires a complete payload through each one.
func TestLocalCoreClientMatrix(t *testing.T) {
	cores := localPinnedCores(t)
	certPEM, keyPEM, pin := localCertificate(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, localPayload())
	}))
	defer target.Close()
	want := localPayload()

	for _, tc := range localCases(t, certPEM, keyPEM) {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			port := localFreePort(t)
			node, credential, srvClient := tc.build(port)
			ep, issues := protocol.EndpointFor(node)
			if len(issues) > 0 {
				t.Fatalf("endpoint issues: %v", issues)
			}
			if node.Cipher == "2022-blake3-aes-128-gcm" {
				if ep.ServerPSK != node.ServerKey || !strings.HasSuffix(ep.ServerPSK, "==") {
					t.Fatal("2022 endpoint must carry the server PSK for the client credential")
				}
			}
			if node.StreamSettings.Has("tlsSettings") && ep.PeerCertSha256 != pin {
				t.Fatalf("tls endpoint pin = %q, want the served certificate hash", ep.PeerCertSha256)
			}
			nc := NodeConfig{Endpoint: ep, Credential: credential}

			serverDoc, err := xrayconf.Build(node, []xrayconf.Client{srvClient})
			if err != nil {
				t.Fatalf("server config: %v", err)
			}
			dir := t.TempDir()
			srvPath := localWrite(t, dir, "server.json", serverDoc)
			if err := localCheck(t, cores.xray, "run", "-test", "-c", srvPath); err != nil {
				t.Fatalf("pinned Xray rejects the generated server config: %v", err)
			}
			stopServer := localStart(t, cores.xray, []string{"run", "-c", srvPath}, port)
			defer stopServer()

			t.Run("xray", func(t *testing.T) {
				localRunXrayClient(t, cores.xray, nc, dir, target.URL, want, "")
			})
			t.Run("sing-box", func(t *testing.T) {
				localRunSingboxClient(t, cores.singbox, nc, dir, target.URL, want)
			})
			t.Run("mihomo", func(t *testing.T) {
				if field, reject := tc.rejected["mihomo"]; reject {
					_, err := Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{nc}})
					var ue *UnsupportedError
					if !errors.As(err, &ue) || ue.Field != field {
						t.Fatalf("mihomo must reject %q with UnsupportedError on %s, got %v", tc.key, field, err)
					}
					t.Logf(`matrix_cell={"client":"mihomo","result":"declared_incompatible_rejected","field":%q}`, field)
				} else {
					localRunMihomoClient(t, cores.mihomo, nc, dir, target.URL, want)
				}
			})
		})
	}
}

func localXrayProxy(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	for _, raw := range localArray(doc["outbounds"]) {
		ob := localMap(raw)
		switch localString(ob["protocol"]) {
		case "", "freedom", "blackhole":
			continue
		default:
			return ob
		}
	}
	t.Fatal("export has no proxy outbound")
	return nil
}

func localSingboxOutbound(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	for _, raw := range localArray(doc["outbounds"]) {
		ob := localMap(raw)
		if kind := localString(ob["type"]); kind != "" && kind != "direct" {
			return ob
		}
	}
	t.Fatal("export has no proxy outbound")
	return nil
}

func localMihomoProxy(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	for _, raw := range localArray(doc["proxies"]) {
		return localMap(raw)
	}
	t.Fatal("export has no proxy")
	return nil
}

func localRunXrayClient(t *testing.T, bin string, n NodeConfig, dir, targetURL, want, pinOverride string) {
	t.Helper()
	res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("xray export: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("xray export json: %v", err)
	}
	proxy := localXrayProxy(t, doc)
	stream := localMap(proxy["streamSettings"])
	if tls := localMap(stream["tlsSettings"]); tls != nil {
		if _, bad := tls["allowInsecure"]; bad {
			t.Fatal("xray export still carries the removed allowInsecure option")
		}
		if pinOverride != "" {
			tls["pinnedPeerCertSha256"] = pinOverride
		}
	}
	socks := localFreePort(t)
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "socks-in", "listen": "127.0.0.1", "port": socks, "protocol": "socks",
			"settings": map[string]any{"udp": true, "auth": "noauth"},
		}},
		"outbounds": []any{proxy},
		"routing":   map[string]any{"domainStrategy": "AsIs", "rules": []any{}},
	}
	path := localWrite(t, dir, "xray-client.json", localJSON(t, cfg))
	if err := localCheck(t, bin, "run", "-test", "-c", path); err != nil {
		if pinOverride != "" {
			localRequireConfigRejection(t, err)
			return // a rejected config also enforces the pin
		}
		t.Fatalf("pinned Xray rejects the exported client config: %v", err)
	}
	stop := localStart(t, bin, []string{"run", "-c", path}, socks)
	defer stop()
	body, err := localFetch(socks, targetURL)
	if pinOverride != "" {
		localRequireTrafficRejection(t, err)
		return
	}
	if err != nil {
		t.Fatalf("xray client traffic: %v", err)
	}
	localAssertPayload(t, "xray", body, want)
}

func localRunSingboxClient(t *testing.T, bin string, n NodeConfig, dir, targetURL, want string) {
	t.Helper()
	res, err := Render(Request{Format: FormatSingbox, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("sing-box export: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("sing-box export json: %v", err)
	}
	ob := localSingboxOutbound(t, doc)
	socks := localFreePort(t)
	cfg := map[string]any{
		"log":       map[string]any{"level": "warn"},
		"inbounds":  []any{map[string]any{"type": "socks", "tag": "socks-in", "listen": "127.0.0.1", "listen_port": socks}},
		"outbounds": []any{ob, map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": localString(ob["tag"])},
	}
	path := localWrite(t, dir, "singbox-client.json", localJSON(t, cfg))
	if err := localCheck(t, bin, "check", "-c", path); err != nil {
		t.Fatalf("sing-box rejects the exported client config: %v", err)
	}
	stop := localStart(t, bin, []string{"run", "-c", path}, socks)
	defer stop()
	body, err := localFetch(socks, targetURL)
	if err != nil {
		t.Fatalf("sing-box client traffic: %v", err)
	}
	localAssertPayload(t, "sing-box", body, want)
}

func localRunMihomoClient(t *testing.T, bin string, n NodeConfig, dir, targetURL, want string) {
	t.Helper()
	res, err := Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("mihomo export: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("mihomo export yaml: %v", err)
	}
	proxy := localMihomoProxy(t, doc)
	name := localString(proxy["name"])
	socks := localFreePort(t)
	cfg := map[string]any{
		"allow-lan":  false,
		"log-level":  "warning",
		"mixed-port": socks,
		"mode":       "rule",
		"proxies":    []any{proxy},
		"proxy-groups": []any{map[string]any{
			"name": "MATRIX", "type": "select", "proxies": []any{name},
		}},
		"rules": []any{"MATCH,MATRIX"},
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("mihomo config encode: %v", err)
	}
	path := localWrite(t, dir, "mihomo-client.yaml", raw)
	work := filepath.Join(dir, "mihomo-work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := localCheck(t, bin, "-t", "-f", path, "-d", work); err != nil {
		t.Fatalf("mihomo rejects the exported client config: %v", err)
	}
	stop := localStart(t, bin, []string{"-f", path, "-d", work}, socks)
	defer stop()
	body, err := localFetch(socks, targetURL)
	if err != nil {
		t.Fatalf("mihomo client traffic: %v", err)
	}
	localAssertPayload(t, "mihomo", body, want)
}

// TestLocalCoreCredentialGuards proves the two credential fixes are enforced by
// the cores, not only by the exporter: the wrong form fails, the exported form
// works (the positive case already runs in TestLocalCoreClientMatrix).
func TestLocalCoreCredentialGuards(t *testing.T) {
	cores := localPinnedCores(t)
	certPEM, keyPEM, pin := localCertificate(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, localPayload())
	}))
	defer target.Close()

	t.Run("xray-pin-is-enforced", func(t *testing.T) {
		cases := localCases(t, certPEM, keyPEM)
		var tlsCase localCase
		for _, c := range cases {
			if c.key == "trojan-tcp-tls" {
				tlsCase = c
			}
		}
		port := localFreePort(t)
		node, credential, srvClient := tlsCase.build(port)
		ep, issues := protocol.EndpointFor(node)
		if len(issues) > 0 || ep.PeerCertSha256 != pin {
			t.Fatalf("endpoint pin = %q issues %v", ep.PeerCertSha256, issues)
		}
		serverDoc, err := xrayconf.Build(node, []xrayconf.Client{srvClient})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		srvPath := localWrite(t, dir, "server.json", serverDoc)
		stop := localStart(t, cores.xray, []string{"run", "-c", srvPath}, port)
		defer stop()
		// Correct pin works.
		localRunXrayClient(t, cores.xray, NodeConfig{Endpoint: ep, Credential: credential}, dir, target.URL, localPayload(), "")
		// A different certificate hash must break the connection.
		wrong := strings.Repeat("00", 32)
		localRunXrayClient(t, cores.xray, NodeConfig{Endpoint: ep, Credential: credential}, dir, target.URL, localPayload(), wrong)
	})

	t.Run("ss2022-user-psk-only-fails", func(t *testing.T) {
		serverPSK, userPSK := localSS2022PSK(t)
		node := domain.Node{
			ID: "nd_ss", Name: "local-ss2022", Protocol: protocol.Shadowsocks,
			Cipher: "2022-blake3-aes-128-gcm", Host: "127.0.0.1", Listen: "127.0.0.1", Port: localFreePort(t),
			StreamSettings:   localObject(t, `{"network":"tcp","security":"none"}`),
			ProtocolSettings: domain.NewObject(), ClientSettings: domain.NewObject(), ServerKey: serverPSK,
		}
		ep, issues := protocol.EndpointFor(node)
		if len(issues) > 0 || ep.ServerPSK != serverPSK {
			t.Fatalf("endpoint issues: %v", issues)
		}
		serverDoc, err := xrayconf.Build(node, []xrayconf.Client{{MemberID: "m-1", Email: "m-1", Password: userPSK}})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		srvPath := localWrite(t, dir, "server.json", serverDoc)
		stop := localStart(t, cores.xray, []string{"run", "-c", srvPath}, node.Port)
		defer stop()
		// The exporter's credential (serverPSK:userPSK) works ...
		localRunXrayClient(t, cores.xray, NodeConfig{Endpoint: ep, Credential: userPSK}, dir, target.URL, localPayload(), "")
		// ... while the bare user PSK, the pre-fix export, is rejected.
		// The exporter itself refuses to emit it (see the unit tests); here the
		// password is replaced directly to prove the core needs the prefix.
		res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{{Endpoint: ep, Credential: userPSK}}})
		if err != nil {
			t.Fatalf("xray export with the server PSK: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(res.Body, &doc); err != nil {
			t.Fatal(err)
		}
		proxy := localXrayProxy(t, doc)
		settings := localMap(proxy["settings"])
		server := localMap(localArray(settings["servers"])[0])
		if want := serverPSK + ":" + userPSK; localString(server["password"]) != want {
			t.Fatal("the exported 2022 password must be <serverPSK>:<userPSK>")
		}
		// Pre-fix form: the bare user PSK the old exporter emitted.
		server["password"] = userPSK
		// Same doc, but bypassing the exporter's guard: prove the core rejects it.
		brokenDoc := map[string]any{
			"log": map[string]any{"loglevel": "warning"},
			"inbounds": []any{map[string]any{
				"tag": "socks-in", "listen": "127.0.0.1", "port": localFreePort(t), "protocol": "socks",
				"settings": map[string]any{"udp": true, "auth": "noauth"},
			}},
			"outbounds": []any{proxy},
			"routing":   map[string]any{"domainStrategy": "AsIs", "rules": []any{}},
		}
		socks := localMap(localArray(brokenDoc["inbounds"])[0])["port"].(int)
		path := localWrite(t, dir, "xray-user-psk-only.json", localJSON(t, brokenDoc))
		if err := localCheck(t, cores.xray, "run", "-test", "-c", path); err != nil {
			localRequireConfigRejection(t, err)
			return // the core rejected the config outright
		}
		stopClient := localStart(t, cores.xray, []string{"run", "-c", path}, socks)
		defer stopClient()
		_, err = localFetch(socks, target.URL)
		localRequireTrafficRejection(t, err)
	})
}

func localMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func localArray(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

func localString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
