package clientconfig

import (
	"encoding/json"
	"errors"
	"net/url"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
	"strings"
	"testing"
)

func TestHTTPUpgradeProjectionAndExplicitIncompatibility(t *testing.T) {
	var stream domain.Object
	if err := json.Unmarshal([]byte(`{"network":"httpupgrade","security":"none","httpupgradeSettings":{"path":"/private-upgrade","host":"transport.example.invalid"}}`), &stream); err != nil {
		t.Fatal(err)
	}
	ep, issues := protocol.EndpointFor(domain.Node{ID: "nd_upgrade", Name: "upgrade", Protocol: "vless", Host: "127.0.0.1", Port: 8443, StreamSettings: stream})
	if len(issues) != 0 {
		t.Fatalf("projection: %+v", issues)
	}
	if ep.Path != "/private-upgrade" || ep.Host != "transport.example.invalid" || ep.Network != "httpupgrade" {
		t.Fatalf("lost transport parameters: %+v", ep)
	}
	node := NodeConfig{Endpoint: ep, Credential: uuid}
	for _, format := range []string{FormatXray, FormatSingbox, FormatV2rayN} {
		result, err := Render(Request{Format: format, Nodes: []NodeConfig{node}})
		if err != nil {
			t.Fatalf("%s render: %v", format, err)
		}
		body := string(result.Body)
		if format == FormatV2rayN {
			u, err := url.Parse(strings.TrimSpace(body))
			if err != nil {
				t.Fatal(err)
			}
			if u.Query().Get("path") != ep.Path || u.Query().Get("host") != ep.Host || u.Query().Get("type") != "httpupgrade" {
				t.Fatal("URI parameters dropped")
			}
		} else if !strings.Contains(body, ep.Path) || !strings.Contains(body, ep.Host) || !strings.Contains(body, "httpupgrade") {
			t.Fatalf("%s lost upgrade fields", format)
		}
	}
	_, err := Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{node}})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Mihomo must explicitly reject HTTPUpgrade rather than output websocket: %v", err)
	}
}
