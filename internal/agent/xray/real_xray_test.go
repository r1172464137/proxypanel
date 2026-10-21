//go:build xrayreal

package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

// realXrayPath returns the official Xray binary used for controlled local tests.
func realXrayPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("XRAY_BIN")
	if path == "" {
		path = `E:\SDK\Xray\v26.3.27\xray.exe`
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("real Xray binary not available (%s): %v", path, err)
	}
	return path
}

// freePort returns a port that is currently bindable. Ports are picked from a
// high range and verified so back-to-back real core tests cannot collide with a
// socket of the previous test.
func freePort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		port := 20000 + rand.Intn(40000)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port
	}
	t.Fatal("no free TCP port found")
	return 0
}

func vlessDocument(port int, emails ...string) []byte {
	clients := make([]map[string]any, 0, len(emails))
	for i, e := range emails {
		clients = append(clients, map[string]any{
			"id":    fmt.Sprintf("11111111-1111-1111-1111-00000000000%d", i),
			"email": e,
			"flow":  "",
		})
	}
	doc := map[string]any{
		"log": map[string]any{"loglevel": "warning", "access": "/tmp/should-be-removed.log"},
		"inbounds": []any{map[string]any{
			"tag":      "vless-in",
			"listen":   "127.0.0.1",
			"port":     port,
			"protocol": "vless",
			"settings": map[string]any{"decryption": "none", "clients": clients},
			"streamSettings": map[string]any{
				"network":  "tcp",
				"security": "none",
			},
			"sniffing": map[string]any{"enabled": true, "destOverride": []any{"http", "tls"}},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}

// TestRealXrayInjectedConfig proves that the agent generated config (management
// API + stats + policy + routing) is accepted and queryable by the official core.
func TestRealXrayInjectedConfig(t *testing.T) {
	bin := realXrayPath(t)
	inboundPort := freePort(t)
	apiPort := freePort(t)
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)

	inst := contracts.Instance{
		ID:     "node-real-1",
		NodeID: "node-real-1",
		Config: json.RawMessage(vlessDocument(inboundPort, "alice@example.test", "bob@example.test")),
		Users: []contracts.AuthorizedUser{
			{MemberID: "member-alice", Email: "alice@example.test"},
			{MemberID: "member-bob", Email: "bob@example.test", ExpiresAt: &expired},
		},
	}
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: now})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.KeptClients != 1 {
		t.Fatalf("expected 1 kept client (bob expired), got %d", plan.KeptClients)
	}
	config, err := plan.Render(apiPort)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, config, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	runner := NewCLIRunner(bin)
	ctx := context.Background()
	if err := runner.Test(ctx, cfgPath); err != nil {
		t.Fatalf("xray -test rejected the generated config: %v", err)
	}
	version, err := runner.Version(ctx)
	if err != nil || version == "" {
		t.Fatalf("xray version: %v (%q)", err, version)
	}
	proc, err := runner.Start(ctx, StartSpec{InstanceID: inst.ID, ConfigPath: cfgPath, LogPath: filepath.Join(dir, "xray.log")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = proc.Stop(context.Background())
		}
	}()
	server := fmt.Sprintf("127.0.0.1:%d", apiPort)
	var counters []StatCounter
	deadline := time.Now().Add(10 * time.Second)
	for {
		counters, err = runner.StatsQuery(ctx, server)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("management API did not answer: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(counters) > 0 {
		t.Logf("counters before traffic: %d", len(counters))
	}
	if err := proc.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	stopped = true
	select {
	case <-proc.Exited():
	case <-time.After(5 * time.Second):
		t.Fatalf("process did not exit after Stop")
	}
}

// TestRealXrayEmptyClients proves that revoking the last user produces a config
// the core still accepts.
func TestRealXrayEmptyClients(t *testing.T) {
	bin := realXrayPath(t)
	inboundPort := freePort(t)
	apiPort := freePort(t)
	inst := contracts.Instance{
		ID:     "node-real-2",
		NodeID: "node-real-2",
		Config: json.RawMessage(vlessDocument(inboundPort, "carol@example.test")),
		Users:  nil, // everything revoked
	}
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.KeptClients != 0 {
		t.Fatalf("expected no kept clients, got %d", plan.KeptClients)
	}
	config, err := plan.Render(apiPort)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, config, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := NewCLIRunner(bin).Test(context.Background(), cfgPath); err != nil {
		t.Fatalf("xray -test rejected the empty client list: %v", err)
	}
}
