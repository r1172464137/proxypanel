package e2e

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
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestXrayVLESSRealityVision uses real local server/client core processes. It
// never connects to the shared Linux host or a public REALITY target.
func TestXrayVLESSRealityVision(t *testing.T) {
	binary := os.Getenv("XRAY_BIN")
	if binary == "" {
		t.Skip("XRAY_BIN is required for real-core verification")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("proxypanel-verified-", 4096)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, payload) }))
	defer target.Close()
	decoy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "local-reality-target") }))
	decoy.EnableHTTP2 = true
	decoy.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	decoy.StartTLS()
	defer decoy.Close()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	priv := base64.RawURLEncoding.EncodeToString(key.Bytes())
	pub := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	proxyPort, apiPort, socksPort := unusedPort(t), unusedPort(t), unusedPort(t)
	user := "66f8a568-4756-46d8-85a8-cb24a2d29fbc"
	server := map[string]any{
		"log":    map[string]any{"loglevel": "warning"},
		"api":    map[string]any{"tag": "api", "services": []string{"StatsService", "HandlerService"}},
		"stats":  map[string]any{},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}}},
		"inbounds": []any{
			map[string]any{"tag": "proxy", "listen": "127.0.0.1", "port": proxyPort, "protocol": "vless", "settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": user, "email": "member-test", "flow": "xtls-rprx-vision", "level": 0}}}, "streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"show": false, "target": decoy.Listener.Addr().String(), "serverNames": []string{"localhost"}, "privateKey": priv, "shortIds": []string{"a1b2"}}}},
			map[string]any{"tag": "api", "listen": "127.0.0.1", "port": apiPort, "protocol": "dokodemo-door", "settings": map[string]any{"address": "127.0.0.1"}},
		},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
		"routing":   map[string]any{"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}}},
	}
	client := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true}}},
		"outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": proxyPort, "users": []any{map[string]any{"id": user, "encryption": "none", "flow": "xtls-rprx-vision"}}}}}, "streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"fingerprint": "chrome", "serverName": "localhost", "password": pub, "shortId": "a1b2"}}}},
	}
	dir := t.TempDir()
	serverPath := writeConfig(t, dir, "server.json", server)
	clientPath := writeConfig(t, dir, "client.json", client)
	for _, path := range []string{serverPath, clientPath} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		out, err := exec.CommandContext(ctx, binary, "run", "-test", "-c", path).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("core config validation failed: %v\n%s", err, out)
		}
	}
	serverProcess := startCore(t, binary, serverPath, proxyPort)
	startCore(t, binary, clientPath, socksPort)
	proxyURL := &url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", socksPort)}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	response, err := httpClient.Get(target.URL)
	if err != nil {
		t.Fatalf("real VLESS REALITY Vision request failed: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != payload {
		t.Fatalf("proxy payload mismatch: bytes=%d err=%v", len(body), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	out, err := exec.CommandContext(ctx, binary, "api", "statsquery", "--server="+fmt.Sprintf("127.0.0.1:%d", apiPort), "-pattern", "user>>>").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("stats API failed: %v %s", err, out)
	}
	var stats struct {
		Stat []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	if err = json.Unmarshal(out, &stats); err != nil {
		t.Fatalf("stats JSON: %v %s", err, out)
	}
	var down int64
	for _, s := range stats.Stat {
		if s.Name == "user>>>member-test>>>traffic>>>downlink" {
			down, _ = strconv.ParseInt(strings.Trim(string(s.Value), "\""), 10, 64)
		}
	}
	if down < int64(len(payload)) {
		t.Fatalf("downlink counter too small: %d, payload %d", down, len(payload))
	}
	t.Logf("REALITY+Vision transfer verified: payload=%d counter=%d", len(payload), down)
	// Stop the server, clear cached client connections, and prove no new proxy
	// request succeeds. Fine-grained member revocation is tested at Agent level.
	if err = serverProcess.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = serverProcess.Wait()
	transport.CloseIdleConnections()
	httpClient.Timeout = 3 * time.Second
	if response, err = httpClient.Get(target.URL); err == nil {
		response.Body.Close()
		t.Fatal("proxy succeeded after server termination")
	}
	t.Log("stopped core rejects subsequent proxy connection")
}

func unusedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}
func writeConfig(t *testing.T, dir, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func startCore(t *testing.T, binary, path string, port int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(binary, "run", "-c", path)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	t.Fatalf("core did not become ready: %s", logs.String())
	return nil
}
