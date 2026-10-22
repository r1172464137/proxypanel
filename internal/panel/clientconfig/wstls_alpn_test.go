package clientconfig

// Regression tests for the two defects the real-core run found on a ws+tls node
// (same node, same export):
//
//   - Xray answered 200 OK while sing-box (tls.insecure=true) and Mihomo timed
//     out with "context deadline exceeded". Both clients negotiated HTTP/2
//     through ALPN, and an HTTP/1.1 Upgrade tunnel cannot run on h2.
//   - The Mihomo export carried "servername" but no "skip-cert-verify", so the
//     core kept verifying a certificate the administrator had explicitly
//     accepted; the setting was emitted inside the trojan branch only.
//
// The endpoints below go through protocol.EndpointFor, i.e. the same projection
// the subscription handler uses, so the server tlsSettings.alpn inheritance that
// triggered the failure is part of the test.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
)

const wsTLSStreamNoALPN = `{"network":"ws","security":"tls","wsSettings":{"path":"/ws","headers":{"Host":"cdn.example.test"}},"tlsSettings":{}}`

const wsTLSStreamWithALPN = `{"network":"ws","security":"tls","wsSettings":{"path":"/ws","headers":{"Host":"cdn.example.test"}},"tlsSettings":{"alpn":["h2","http/1.1"]}}`

func endpointFrom(t *testing.T, protocolName, streamJSON, clientJSON string, port int) protocol.Endpoint {
	t.Helper()
	var stream, client domain.Object
	if err := json.Unmarshal([]byte(streamJSON), &stream); err != nil {
		t.Fatalf("stream settings: %v", err)
	}
	if err := json.Unmarshal([]byte(clientJSON), &client); err != nil {
		t.Fatalf("client settings: %v", err)
	}
	ep, issues := protocol.EndpointFor(domain.Node{
		ID: "nd_ws", Name: "ws-tls", Protocol: protocolName,
		Host: "ws.example.test", Port: port, Listen: "0.0.0.0",
		StreamSettings: stream, ProtocolSettings: domain.NewObject(), ClientSettings: client,
	})
	if len(issues) > 0 {
		t.Fatalf("endpoint issues: %s", issues[0].String())
	}
	return ep
}

func alpnOf(t *testing.T, raw any) []string {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("alpn = %#v, want a list", raw)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("alpn entry = %#v", item)
		}
		out = append(out, s)
	}
	return out
}

// exportedALPN returns the ALPN each format put on the wire for one node.
func exportedALPN(t *testing.T, nc NodeConfig) map[string][]string {
	t.Helper()
	out := map[string][]string{}

	res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{nc}})
	if err == nil {
		stream, _ := xrayOutboundFor(t, res)["streamSettings"].(map[string]any)
		tls, _ := stream["tlsSettings"].(map[string]any)
		if raw, ok := tls["alpn"]; ok {
			out[FormatXray] = alpnOf(t, raw)
		} else {
			out[FormatXray] = nil
		}
	}

	res, err = Render(Request{Format: FormatSingbox, Nodes: []NodeConfig{nc}})
	if err != nil {
		t.Fatalf("sing-box render: %v", err)
	}
	tls, _ := jsonOutboundFor(t, res)["tls"].(map[string]any)
	if raw, ok := tls["alpn"]; ok {
		out[FormatSingbox] = alpnOf(t, raw)
	} else {
		out[FormatSingbox] = nil
	}

	res, err = Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{nc}})
	if err != nil {
		t.Fatalf("mihomo render: %v", err)
	}
	if raw, ok := yamlProxyFor(t, res)["alpn"]; ok {
		out[FormatMihomo] = alpnOf(t, raw)
	} else {
		out[FormatMihomo] = nil
	}

	res, err = Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{nc}})
	if err != nil {
		t.Fatalf("v2rayn render: %v", err)
	}
	link := strings.TrimSpace(string(res.Body))
	if strings.HasPrefix(link, "vmess://") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
		if err != nil {
			t.Fatalf("vmess payload: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("vmess payload: %v", err)
		}
		if v, ok := payload["alpn"].(string); ok && v != "" {
			out[FormatV2rayN] = strings.Split(v, ",")
		} else {
			out[FormatV2rayN] = nil
		}
	} else {
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("share link: %v", err)
		}
		if v := u.Query().Get("alpn"); v != "" {
			out[FormatV2rayN] = strings.Split(v, ",")
		} else {
			out[FormatV2rayN] = nil
		}
	}
	return out
}

// TestWSTLSClientsOfferHTTP11 drives the real machine shape: a ws+tls node whose
// server tlsSettings announces h2 first. Every export must offer http/1.1 only,
// because the WebSocket upgrade cannot be carried over HTTP/2.
func TestWSTLSClientsOfferHTTP11(t *testing.T) {
	cases := map[string]string{
		// The endpoint inherits the server ALPN list - the exact input that made
		// sing-box and Mihomo negotiate h2 and time out.
		"inherited-server-alpn": `{"server_name":"ws.example.test"}`,
		// No ALPN anywhere: the export must not rely on a client default.
		"no-alpn-anywhere": `{"server_name":"ws.example.test"}`,
	}
	for name, clientSettings := range cases {
		clientSettings := clientSettings
		stream := wsTLSStreamWithALPN
		if name == "no-alpn-anywhere" {
			stream = wsTLSStreamNoALPN
		}
		t.Run(name, func(t *testing.T) {
			ep := endpointFrom(t, protocol.VMess, stream, clientSettings, 443)
			nc := NodeConfig{Endpoint: ep, Credential: uuid}
			got := exportedALPN(t, nc)
			for _, format := range []string{FormatXray, FormatSingbox, FormatMihomo, FormatV2rayN} {
				if want := []string{"http/1.1"}; strings.Join(got[format], ",") != strings.Join(want, ",") {
					t.Fatalf("%s alpn = %v, want %v", format, got[format], want)
				}
			}
		})
	}

	// An explicit administrator ALPN cannot express h2 for such a node either: a
	// WebSocket tunnel is HTTP/1.1 by construction, so the export states that.
	t.Run("explicit-h2-is-not-usable", func(t *testing.T) {
		ep := endpointFrom(t, protocol.VMess, wsTLSStreamNoALPN, `{"server_name":"ws.example.test","alpn":["h2"]}`, 443)
		nc := NodeConfig{Endpoint: ep, Credential: uuid}
		for _, format := range []string{FormatXray, FormatSingbox, FormatMihomo, FormatV2rayN} {
			if got := exportedALPN(t, nc)[format]; strings.Join(got, ",") != "http/1.1" {
				t.Fatalf("%s alpn = %v, want [http/1.1]", format, got)
			}
		}
	})

	// Transports that really do run on HTTP/2 keep the administrator's ALPN.
	t.Run("h2-transport-keeps-alpn", func(t *testing.T) {
		ep := endpointFrom(t, protocol.VMess,
			`{"network":"http","security":"tls","httpSettings":{"path":"/h2","host":["cdn.example.test"]},"tlsSettings":{"alpn":["h2"]}}`,
			`{"server_name":"ws.example.test"}`, 443)
		nc := NodeConfig{Endpoint: ep, Credential: uuid}
		if got := ep.ALPN; strings.Join(got, ",") != "h2" {
			t.Fatalf("endpoint alpn = %v", got)
		}
		if got := exportedALPN(t, nc); strings.Join(got[FormatXray], ",") != "h2" || strings.Join(got[FormatSingbox], ",") != "h2" || strings.Join(got[FormatMihomo], ",") != "h2" {
			t.Fatalf("h2 transport alpn = %v", got)
		}
	})
}

// TestMihomoWSKTLSCarriesAllowInsecure is the second half of the real-machine
// failure: client_settings.allow_insecure never reached the Mihomo proxy, so the
// core kept verifying the node certificate.
func TestMihomoWSKTLSCarriesAllowInsecure(t *testing.T) {
	ep := endpointFrom(t, protocol.VMess, wsTLSStreamWithALPN, `{"server_name":"ws.example.test","allow_insecure":true}`, 443)
	if !ep.AllowInsecure {
		t.Fatal("endpoint lost client_settings.allow_insecure")
	}
	nc := NodeConfig{Endpoint: ep, Credential: uuid}

	res, err := Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{nc}})
	if err != nil {
		t.Fatalf("mihomo render: %v", err)
	}
	proxy := yamlProxyFor(t, res)
	if skip, _ := proxy["skip-cert-verify"].(bool); !skip {
		t.Fatalf("mihomo proxy dropped allow_insecure: %v", proxy)
	}
	if got := proxy["network"]; got != "ws" {
		t.Fatalf("mihomo proxy network = %v", got)
	}
	// sing-box expresses the same setting as tls.insecure; it already did, and
	// the pair must stay consistent.
	singboxRes, err := Render(Request{Format: FormatSingbox, Nodes: []NodeConfig{nc}})
	if err != nil {
		t.Fatalf("sing-box render: %v", err)
	}
	tls, _ := jsonOutboundFor(t, singboxRes)["tls"].(map[string]any)
	if insecure, _ := tls["insecure"].(bool); !insecure {
		t.Fatalf("sing-box tls.insecure was dropped: %v", tls)
	}
}

// TestWSTLSPinAndAllowInsecureStaysExplicit keeps the "never degrade silently"
// rule: Xray 26 cannot express allow_insecure without a certificate to pin, and
// a ws+tls node must say so instead of exporting a config that cannot connect.
func TestWSTLSPinAndAllowInsecureStaysExplicit(t *testing.T) {
	ep := endpointFrom(t, protocol.VMess, wsTLSStreamWithALPN, `{"server_name":"ws.example.test","allow_insecure":true}`, 443)
	nc := NodeConfig{Endpoint: ep, Credential: uuid}
	_, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{nc}})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Field != "client_settings.allow_insecure" {
		t.Fatalf("expected an explicit allow_insecure rejection, got %v", err)
	}
	for _, format := range []string{FormatSingbox, FormatMihomo, FormatV2rayN} {
		if _, err := Render(Request{Format: format, Nodes: []NodeConfig{nc}}); err != nil {
			t.Fatalf("%s must express allow_insecure: %v", format, err)
		}
	}
}
