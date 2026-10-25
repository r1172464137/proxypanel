package protocol

import (
	"testing"

	"proxypanel/internal/panel/domain"
)

// Xray's RealityObject has no nested "settings" object. An older panel build
// wrote one containing the client public key, so older rows must be repaired on
// save instead of persisting a field the core does not understand.
func TestSanitizeDropsNestedRealitySettings(t *testing.T) {
	stream := domain.NewObject()
	_ = stream.Set("network", "tcp")
	_ = stream.Set("security", "reality")
	rs := domain.NewObject()
	_ = rs.Set("dest", "www.microsoft.com:443")
	_ = rs.Set("serverNames", []string{"www.microsoft.com"})
	_ = rs.Set("shortIds", []string{"c804aea035911a9a"})
	_ = rs.Set("privateKey", "x-QXafFLdN42wFnaPh-u3J1_ptOKk")
	nested := domain.NewObject()
	_ = nested.Set("publicKey", "legacy")
	_ = nested.Set("fingerprint", "chrome")
	_ = nested.Set("spiderX", "/")
	_ = rs.Set("settings", nested)
	_ = stream.Set("realitySettings", rs)

	out, _, key, issues := Sanitize(Node{Protocol: VLESS, StreamSettings: stream})
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	if key == "" {
		t.Fatal("privateKey must be lifted into server_key")
	}
	outRS, ok := out.Sub("realitySettings")
	if !ok {
		t.Fatal("realitySettings missing")
	}
	if outRS.Has("settings") {
		t.Fatal("nested realitySettings.settings must be removed")
	}
	if outRS.Has("privateKey") {
		t.Fatal("privateKey must not stay in the stored stream settings")
	}
}

// rawSettings is the Xray 26 name for the TCP transport. A header declared
// there must reach the exported client config instead of being dropped.
func TestEndpointReadsRawSettingsTCPHeader(t *testing.T) {
	stream := domain.NewObject()
	_ = stream.Set("network", "raw")
	_ = stream.Set("security", "none")
	raw := domain.NewObject()
	header := domain.NewObject()
	_ = header.Set("type", "http")
	_ = header.Set("request", map[string]any{"path": []string{"/ws"}})
	_ = raw.Set("header", header)
	_ = raw.Set("acceptProxyProtocol", true)
	_ = stream.Set("rawSettings", raw)
	node := domain.Node{Protocol: VLESS, Host: "example.test", Port: 443, Listen: "0.0.0.0",
		StreamSettings: stream, ProtocolSettings: domain.NewObject(), ClientSettings: domain.NewObject()}
	ep, issues := EndpointFor(node)
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	if len(ep.TCPHeader) == 0 {
		t.Fatal("rawSettings.header must be read like tcpSettings.header")
	}
}
