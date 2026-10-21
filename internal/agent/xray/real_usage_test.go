//go:build xrayreal

package xray

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

// startEchoServer starts a local TCP target the core may connect to.
func startEchoServer(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

// vlessRequest writes a minimal VLESS header for a TCP request to target.
func vlessRequest(uuid []byte, targetPort int, targetIP [4]byte) []byte {
	buf := make([]byte, 0, 32)
	buf = append(buf, 0x00)    // version
	buf = append(buf, uuid...) // 16 byte user id
	buf = append(buf, 0x00)    // no addons
	buf = append(buf, 0x01)    // command: TCP
	buf = binary.BigEndian.AppendUint16(buf, uint16(targetPort))
	buf = append(buf, 0x01) // address type: IPv4
	buf = append(buf, targetIP[:]...)
	return buf
}

// TestRealXrayPerUserUsage proves that the fixed statsquery command reports
// per-user cumulative counters that the agent can attribute to a member.
func TestRealXrayPerUserUsage(t *testing.T) {
	bin := realXrayPath(t)
	echoPort, closeEcho := startEchoServer(t)
	defer closeEcho()
	inboundPort := freePort(t)
	apiPort := freePort(t)

	aliceID := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	doc := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      "vless-in",
			"listen":   "127.0.0.1",
			"port":     inboundPort,
			"protocol": "vless",
			"settings": map[string]any{
				"decryption": "none",
				"clients": []any{map[string]any{
					"id":    "01020304-0506-0708-090a-0b0c0d0e0f10",
					"email": "alice@example.test",
				}},
			},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	inst := contracts.Instance{
		ID: "node-real-usage", NodeID: "node-real-usage", Config: json.RawMessage(raw),
		Users: []contracts.AuthorizedUser{{MemberID: "member-alice", Email: "alice@example.test"}},
	}
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
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
		t.Fatalf("xray -test: %v", err)
	}
	proc, err := runner.Start(ctx, StartSpec{InstanceID: inst.ID, ConfigPath: cfgPath, LogPath: filepath.Join(dir, "xray.log")})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = proc.Stop(context.Background()) }()
	server := fmt.Sprintf("127.0.0.1:%d", apiPort)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := runner.StatsQuery(ctx, server); err == nil {
			break
		}
		if time.Now().After(deadline) {
			for _, line := range proc.Output() {
				t.Logf("core output: %s", line)
			}
			t.Fatalf("management API did not answer")
		}
		time.Sleep(200 * time.Millisecond)
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", inboundPort), 5*time.Second)
	if err != nil {
		t.Fatalf("dial inbound: %v", err)
	}
	header := vlessRequest(aliceID, echoPort, [4]byte{127, 0, 0, 1})
	if _, err := conn.Write(header); err != nil {
		t.Fatalf("write header: %v", err)
	}
	payload := []byte("hello-vless-usage")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	// The VLESS response starts with version and addon length.
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("read response header: %v", err)
	}
	if head[1] != 0 {
		addon := make([]byte, head[1])
		if _, err := io.ReadFull(conn, addon); err != nil {
			t.Fatalf("read response addons: %v", err)
		}
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo mismatch: %q", buf)
	}

	_ = conn.Close()
	time.Sleep(300 * time.Millisecond)
	var uplink int64
	deadline = time.Now().Add(10 * time.Second)
	for {
		counters, err := runner.StatsQuery(ctx, server)
		if err != nil {
			t.Fatalf("statsquery: %v", err)
		}
		uplink = 0
		for _, c := range counters {
			if c.Name == "user>>>alice@example.test>>>traffic>>>uplink" {
				uplink = c.Value
			}
		}
		if uplink > 0 {
			break
		}
		if time.Now().After(deadline) {
			if counters != nil {
				for _, c := range counters {
					t.Logf("counter %s = %d", c.Name, c.Value)
				}
			}
			t.Fatalf("no per-user uplink counter was reported")
		}
		time.Sleep(200 * time.Millisecond)
	}
	items, unmapped := counterItemsForTest(runner, ctx, server, plan.EmailToMember)
	if len(items) == 0 {
		t.Fatalf("no counter could be attributed to a member")
	}
	if unmapped == 0 {
		t.Logf("all counters were attributable")
	}
	_ = conn.Close()
}

// counterItemsForTest mirrors reporting.CounterItems without importing it to
// keep the real test free of the reporting package.
func counterItemsForTest(runner Runner, ctx context.Context, server string, emailToMember map[string]string) ([]StatCounter, int) {
	counters, err := runner.StatsQuery(ctx, server)
	if err != nil {
		return nil, 0
	}
	var out []StatCounter
	unmapped := 0
	for _, c := range counters {
		parts := splitTriple(c.Name)
		if len(parts) == 4 && parts[0] == "user" && parts[2] == "traffic" {
			if _, ok := emailToMember[parts[1]]; ok {
				out = append(out, c)
				continue
			}
		}
		unmapped++
	}
	return out, unmapped
}

func splitTriple(name string) []string {
	var out []string
	cur := ""
	for i := 0; i < len(name); i++ {
		if name[i] == '>' {
			if i+2 < len(name) && name[i+1] == '>' && name[i+2] == '>' {
				out = append(out, cur)
				cur = ""
				i += 2
				continue
			}
		}
		cur += string(name[i])
	}
	out = append(out, cur)
	return out
}
