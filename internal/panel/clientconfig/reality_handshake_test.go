package clientconfig_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"proxypanel/internal/panel/clientconfig"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/xrayconf"
)

// One synthetic Xray-to-Xray REALITY closure, not the multi-client matrix.
// Both the camouflage target and payload destination are owned loopback servers.
func TestSavedNodeRealityHandshakePayload(t *testing.T) {
	core := tlsFixtureCore(t)
	certPEM, keyPEM, _, _ := writePair(t)
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	camouflage := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "camouflage is not the payload destination", http.StatusForbidden)
	}))
	camouflage.EnableHTTP2 = true
	camouflage.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	camouflage.StartTLS()
	defer camouflage.Close()
	const payload = "owned-loopback-reality-payload"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, payload) }))
	defer target.Close()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	node := domain.Node{
		ID: "nd_reality_handshake", Name: "reality-handshake", Protocol: protocol.VLESS,
		Host: "127.0.0.1", Listen: "127.0.0.1", Port: handshakePort(t),
		StreamSettings: domain.NewObject(), ProtocolSettings: domain.NewObject(), ClientSettings: domain.NewObject(),
	}
	_ = node.StreamSettings.Set("network", "tcp")
	_ = node.StreamSettings.Set("security", "reality")
	_ = node.StreamSettings.Set("realitySettings", map[string]any{
		"dest": camouflage.Listener.Addr().String(), "serverNames": []string{tlsFixtureSNI},
		"privateKey": base64.RawURLEncoding.EncodeToString(key.Bytes()), "shortIds": []string{"abcd1234"},
	})
	_ = node.ProtocolSettings.Set("decryption", "none")
	_ = node.ClientSettings.Set("server_name", tlsFixtureSNI)
	_ = node.ClientSettings.Set("public_key", base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()))
	_ = node.ClientSettings.Set("short_id", "abcd1234")
	_ = node.ClientSettings.Set("fingerprint", "chrome")
	node = sanitizedValidated(t, node)
	if node.ServerKey == "" {
		t.Fatal("save path lost the REALITY private key")
	}
	const uuid = "11111111-2222-4333-8444-555555555555"
	serverDoc, err := xrayconf.Build(node, []xrayconf.Client{{Email: "fixture", UUID: uuid}})
	if err != nil {
		t.Fatal(err)
	}
	startHandshakeCore(t, core, "server", serverDoc, node.Port)
	ep, issues := protocol.EndpointFor(node)
	if len(issues) != 0 {
		t.Fatalf("endpoint: %v", issues)
	}
	rendered, err := clientconfig.Render(clientconfig.Request{Format: clientconfig.FormatXray,
		Nodes: []clientconfig.NodeConfig{{Endpoint: ep, Credential: uuid}}})
	if err != nil {
		t.Fatal(err)
	}
	var clientDoc map[string]any
	if err := json.Unmarshal(rendered.Body, &clientDoc); err != nil {
		t.Fatal(err)
	}
	socksPort := handshakePort(t)
	// Only the local entry point is fixture-owned; exported outbounds are unchanged.
	clientDoc["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}
	raw, err := json.Marshal(clientDoc)
	if err != nil {
		t.Fatal(err)
	}
	startHandshakeCore(t, core, "client", raw, socksPort)
	proxy, err := url.Parse(fmt.Sprintf("socks5://127.0.0.1:%d", socksPort))
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("REALITY payload request: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != payload {
		t.Fatalf("REALITY payload: status=%d body=%q", response.StatusCode, body)
	}
}

func handshakePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func startHandshakeCore(t *testing.T, core, label string, doc []byte, port int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, doc, 0600); err != nil {
		t.Fatal(err)
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer checkCancel()
	if out, err := exec.CommandContext(checkCtx, core, "run", "-test", "-c", path).CombinedOutput(); err != nil {
		t.Fatalf("%s config: %v\n%s", label, err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, core, "run", "-c", path)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	// Registered after TempDir: kill/wait exactly this child before directory removal.
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if cmd.ProcessState == nil {
			t.Errorf("%s owned PID %d not reaped", label, cmd.Process.Pid)
		}
		t.Logf("%s owned PID %d reaped; tempdir=%s", label, cmd.Process.Pid, dir)
		if t.Failed() {
			t.Logf("%s core log:\n%s", label, log.String())
		}
	})
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("%s did not listen on owned port %s", label, address)
}
