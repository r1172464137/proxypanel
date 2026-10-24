package clientconfig_test

// Regression tests for the protocol review (D09/D10/D11) and the TLS handshake
// suite in tls_handshake_test.go. Every assertion runs in its own subtest so a
// single failing combination cannot hide the rest (the previous draft used
// sequential t.Fatal calls in one function).

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"proxypanel/internal/panel/clientconfig"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/xrayconf"
)

func reviewObject(t *testing.T, v string) domain.Object {
	t.Helper()
	var o domain.Object
	if err := json.Unmarshal([]byte(v), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func reviewNode(t *testing.T, stream string) domain.Node {
	t.Helper()
	return domain.Node{ID: "nd_fixture", Name: "fixture", Protocol: "vless", Host: "example.invalid",
		Listen: "127.0.0.1", Port: 23456, StreamSettings: reviewObject(t, stream),
		ProtocolSettings: domain.NewObject(), ClientSettings: domain.NewObject()}
}

// reviewConfig runs the real save path: Sanitize -> Validate -> EndpointFor.
func reviewConfig(t *testing.T, n domain.Node) clientconfig.NodeConfig {
	t.Helper()
	input := protocol.Node{Protocol: n.Protocol, Cipher: n.Cipher, Flow: n.Flow, Host: n.Host,
		Listen: n.Listen, Port: n.Port, StreamSettings: n.StreamSettings,
		ProtocolSettings: n.ProtocolSettings, ClientSettings: n.ClientSettings, ServerKey: n.ServerKey}
	stream, ps, key, issues := protocol.Sanitize(input)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	input.StreamSettings, input.ProtocolSettings, input.ServerKey = stream, ps, key
	if issues := protocol.Validate(input); len(issues) > 0 {
		t.Fatal(issues)
	}
	n.StreamSettings, n.ProtocolSettings, n.ServerKey = stream, ps, key
	ep, issues := protocol.EndpointFor(n)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	return clientconfig.NodeConfig{Endpoint: ep, Credential: "11111111-2222-4333-8444-555555555555"}
}

func requireUnsupported(t *testing.T, n clientconfig.NodeConfig, format, field string) {
	t.Helper()
	_, err := clientconfig.Render(clientconfig.Request{Format: format, Nodes: []clientconfig.NodeConfig{n}})
	var u *clientconfig.UnsupportedError
	if !errors.As(err, &u) || u.Field != field {
		t.Fatalf("%s must explicitly reject %s, got %v", format, field, err)
	}
}

// D09a: an Xray TCP HTTP伪装 header is connection semantics. The Xray JSON
// client must keep it (minus the server-only acceptProxyProtocol); every other
// format must reject the node instead of silently emitting a plain TCP stream.
func TestReviewTCPHeaderPreservedOrRejected(t *testing.T) {
	n := reviewNode(t, `{"network":"tcp","tcpSettings":{"acceptProxyProtocol":true,"header":{"type":"http","request":{"path":["/fixture"],"headers":{"Host":["example.invalid"]}}}}}`)
	nc := reviewConfig(t, n)

	t.Run("server keeps the header", func(t *testing.T) {
		srv, err := xrayconf.Build(n, []xrayconf.Client{{Email: "fixture", UUID: nc.Credential}})
		if err != nil {
			t.Fatal(err)
		}
		var server map[string]any
		if err := json.Unmarshal(srv, &server); err != nil {
			t.Fatal(err)
		}
		stream := server["inbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)
		tcp, ok := stream["tcpSettings"].(map[string]any)
		if !ok || tcp["header"] == nil {
			t.Fatal("server document lost the TCP HTTP header")
		}
	})

	t.Run("xray client keeps the header but not the PROXY protocol switch", func(t *testing.T) {
		rendered, err := clientconfig.Render(clientconfig.Request{Format: "xray", Nodes: []clientconfig.NodeConfig{nc}})
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(rendered.Body, &doc); err != nil {
			t.Fatal(err)
		}
		ss := doc["outbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)
		tcp, ok := ss["tcpSettings"].(map[string]any)
		if !ok || tcp["header"] == nil {
			t.Fatal("client lost TCP HTTP header")
		}
		if tcp["acceptProxyProtocol"] != nil {
			t.Fatal("server-only PROXY protocol must not reach the client")
		}
	})

	for _, format := range []string{"singbox", "mihomo", "v2rayn"} {
		t.Run(format+" rejects explicitly", func(t *testing.T) {
			requireUnsupported(t, nc, format, "stream_settings.tcpSettings.header")
		})
	}
}

// D09b/D09c/D10 and the sing-box xhttp/tag regressions, one subtest per rule.
func TestReviewUnsupportedClientCombinations(t *testing.T) {
	t.Run("VMess gRPC service name", func(t *testing.T) {
		n := reviewNode(t, `{"network":"grpc","grpcSettings":{"serviceName":"fixture"}}`)
		n.Protocol = "vmess"
		nc := reviewConfig(t, n)
		// Xray JSON can express it and must keep it.
		rendered, err := clientconfig.Render(clientconfig.Request{Format: "xray", Nodes: []clientconfig.NodeConfig{nc}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(rendered.Body), "fixture") {
			t.Fatal("xray client export lost the gRPC service name")
		}
		// The VMess share link cannot: reject instead of exporting a link that
		// silently drops the service name.
		requireUnsupported(t, nc, "v2rayn", "stream_settings.grpcSettings")
	})

	t.Run("VMess gRPC multiMode", func(t *testing.T) {
		n := reviewNode(t, `{"network":"grpc","grpcSettings":{"serviceName":"fixture","multiMode":true}}`)
		n.Protocol = "vmess"
		nc := reviewConfig(t, n)
		for _, format := range []string{"singbox", "mihomo"} {
			requireUnsupported(t, nc, format, "stream_settings.grpcSettings.multiMode")
		}
	})

	t.Run("Shadowsocks over TLS", func(t *testing.T) {
		n := reviewNode(t, `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"example.invalid","certificates":[{"certificateFile":"/remote/fixture.pem","keyFile":"/remote/fixture.key"}]}}`)
		n.Protocol = "shadowsocks"
		n.Cipher = "aes-128-gcm"
		n.ClientSettings = reviewObject(t, `{"server_name":"example.invalid"}`)
		nc := reviewConfig(t, n)
		if nc.Endpoint.Security != "tls" {
			t.Fatalf("endpoint security = %q, the test must exercise the TLS branch", nc.Endpoint.Security)
		}
		for _, format := range []string{"singbox", "mihomo", "v2rayn"} {
			requireUnsupported(t, nc, format, "stream_settings.security")
		}
	})

	t.Run("sing-box 1.14.3 has no xhttp", func(t *testing.T) {
		n := reviewNode(t, `{"network":"xhttp","xhttpSettings":{"path":"/fixture","mode":"auto"}}`)
		requireUnsupported(t, reviewConfig(t, n), "singbox", "stream_settings.network")
	})
}

func sampleConfig(t *testing.T) clientconfig.NodeConfig {
	t.Helper()
	return reviewConfig(t, reviewNode(t, `{"network":"tcp"}`))
}

// D11: a node label must never collide with a generated tag or proxy-group
// name. The sing-box check is structural (unique outbound tags) *and*, when the
// pinned binary is available, verified by the core itself.
func TestReviewReservedTags(t *testing.T) {
	for _, name := range []string{"direct", "DIRECT", "PROXY", "REJECT", "proxy", "block"} {
		t.Run(name, func(t *testing.T) {
			nc := sampleConfig(t)
			nc.Endpoint.Name = name

			t.Run("singbox unique outbound tags", func(t *testing.T) {
				rendered, err := clientconfig.Render(clientconfig.Request{Format: "singbox", Nodes: []clientconfig.NodeConfig{nc, nc}})
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Outbounds []struct {
						Tag string `json:"tag"`
					} `json:"outbounds"`
				}
				if err = json.Unmarshal(rendered.Body, &doc); err != nil {
					t.Fatal(err)
				}
				tags := map[string]bool{}
				for _, o := range doc.Outbounds {
					if tags[o.Tag] {
						t.Fatalf("duplicate outbound tag %q", o.Tag)
					}
					tags[o.Tag] = true
				}
				if !tags["direct"] {
					t.Fatal("the generated direct outbound disappeared")
				}
			})

			t.Run("mihomo proxy names avoid built-in policies", func(t *testing.T) {
				rendered, err := clientconfig.Render(clientconfig.Request{Format: "mihomo", Nodes: []clientconfig.NodeConfig{nc, nc}})
				if err != nil {
					t.Fatal(err)
				}
				var doc struct {
					Proxies []struct {
						Name string `yaml:"name"`
					} `yaml:"proxies"`
					ProxyGroups []struct {
						Name    string   `yaml:"name"`
						Proxies []string `yaml:"proxies"`
					} `yaml:"proxy-groups"`
				}
				if err = yaml.Unmarshal(rendered.Body, &doc); err != nil {
					t.Fatalf("mihomo yaml: %v", err)
				}
				reserved := map[string]bool{"DIRECT": true, "REJECT": true, "PROXY": true, "GLOBAL": true, "PASS": true}
				seen := map[string]bool{}
				for _, p := range doc.Proxies {
					if reserved[p.Name] {
						t.Fatalf("proxy name %q collides with a Mihomo built-in policy", p.Name)
					}
					if seen[p.Name] {
						t.Fatalf("duplicate proxy name %q", p.Name)
					}
					seen[p.Name] = true
				}
				for _, g := range doc.ProxyGroups {
					for _, member := range g.Proxies {
						if reserved[member] {
							continue // referring to a built-in target is correct
						}
						if !seen[member] {
							t.Fatalf("proxy-group %q references unknown proxy %q", g.Name, member)
						}
					}
				}
			})

			t.Run("real sing-box core accepts the document", func(t *testing.T) {
				core := strings.TrimSpace(os.Getenv("SING_BOX_BIN"))
				if core == "" {
					t.Skip("SING_BOX_BIN is not set; the core-level check needs the pinned binary")
				}
				if _, err := os.Stat(core); err != nil {
					t.Skipf("SING_BOX_BIN (%s) unavailable: %v", core, err)
				}
				rendered, err := clientconfig.Render(clientconfig.Request{Format: "singbox", Nodes: []clientconfig.NodeConfig{nc, nc}})
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(path, rendered.Body, 0o600); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command(core, "check", "-c", path).CombinedOutput(); err != nil {
					t.Fatalf("sing-box rejected the generated subscription: %v\n%s", err, out)
				}
			})
		})
	}
}
