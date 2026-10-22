package clientconfig

// Regression tests for the ALPN rule that decides what a client may offer on an
// HTTP/1.1 transport, plus the REALITY path that the original fix missed.
//
// The first fix forced http/1.1 whenever Security == "tls". That covered the
// reported ws+tls node but not ws+REALITY: REALITY also terminates TLS and a ws
// tunnel over REALITY carries the identical HTTP/1.1 Upgrade. For REALITY the
// ALPN does not come from realitySettings (protocol.EndpointFor does not read it
// there) but from client_settings.alpn, so an administrator who sets h2 on a
// REALITY node produced exactly the same broken export the tls node produced.
//
// The rule is keyed on the transport ("does this transport need an HTTP/1.1
// Upgrade?") and gated on "is there a TLS layer at all?" - which is what a
// non-empty Security means.

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"proxypanel/internal/panel/protocol"
)

const realityBase = `"realitySettings":{"serverName":"reality.example.test","fingerprint":"chrome","publicKey":"PUBKEY","shortIds":["abcd"]}`

// ws+REALITY with an administrator ALPN of ["h2","http/1.1"] is the gap the
// tls-only rule left open.
func TestWSRealityALPNIsHTTP11(t *testing.T) {
	stream := `{"network":"ws","security":"reality","wsSettings":{"path":"/ws","headers":{"Host":"cdn.example.test"}},` + realityBase + `}`
	ep := endpointFrom(t, protocol.VMess, stream, `{"server_name":"reality.example.test","alpn":["h2","http/1.1"]}`, 443)
	if ep.Security != "reality" {
		t.Fatalf("endpoint security = %q, want reality (the test must exercise the REALITY path)", ep.Security)
	}
	if len(ep.ALPN) == 0 {
		t.Fatal("the endpoint lost the client ALPN before rendering; the test would prove nothing")
	}

	got := exportedALPN(t, NodeConfig{Endpoint: ep, Credential: uuid})
	expressed := 0
	for _, format := range []string{FormatXray, FormatSingbox, FormatMihomo, FormatV2rayN} {
		alpn := got[format]
		if len(alpn) == 0 {
			// Some clients default to http/1.1 when the field is absent, which is
			// also correct for this transport.
			continue
		}
		expressed++
		if strings.Join(alpn, ",") != "http/1.1" {
			t.Fatalf("%s alpn = %v, want [http/1.1] for a ws+REALITY node", format, alpn)
		}
	}
	if expressed == 0 {
		t.Fatal("no format expressed ALPN at all; the assertion above proved nothing")
	}
}

// httpupgrade over REALITY is the same HTTP/1.1 Upgrade and must follow the rule.
func TestHTTPUpgradeRealityALPNIsHTTP11(t *testing.T) {
	stream := `{"network":"httpupgrade","security":"reality","httpupgradeSettings":{"path":"/up","host":"cdn.example.test"},` + realityBase + `}`
	ep := endpointFrom(t, protocol.VMess, stream, `{"server_name":"reality.example.test","alpn":["h2"]}`, 443)

	// Mihomo has no httpupgrade transport. Render refuses explicitly instead of
	// emitting a proxy it cannot carry, so that is asserted separately.
	nc := NodeConfig{Endpoint: ep, Credential: uuid}
	if _, err := Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{nc}}); err == nil {
		t.Fatal("mihomo must refuse an httpupgrade transport instead of emitting an unusable proxy")
	}

	got := exportedALPNWithoutMihomo(t, nc)
	expressed := 0
	for _, format := range []string{FormatXray, FormatSingbox, FormatV2rayN} {
		alpn := got[format]
		if len(alpn) == 0 {
			continue
		}
		expressed++
		if strings.Join(alpn, ",") != "http/1.1" {
			t.Fatalf("%s httpupgrade+reality alpn = %v, want [http/1.1]", format, alpn)
		}
	}
	if expressed == 0 {
		t.Fatal("no format expressed ALPN at all; the assertion above proved nothing")
	}
}

// The forcing must stay scoped to HTTP/1.1 transports: an administrator who sets
// h2 on a tcp/grpc node means it, and rewriting it would break those nodes.
func TestNonUpgradeTransportsKeepServerALPN(t *testing.T) {
	cases := map[string]struct{ stream, client string }{
		"tcp-tls": {
			`{"network":"tcp","security":"tls","tlsSettings":{"serverName":"tcp.example.test","alpn":["h2","http/1.1"]}}`,
			`{"server_name":"tcp.example.test"}`,
		},
		"grpc-tls": {
			`{"network":"grpc","security":"tls","grpcSettings":{"serviceName":"svc"},"tlsSettings":{"serverName":"grpc.example.test","alpn":["h2","http/1.1"]}}`,
			`{"server_name":"grpc.example.test"}`,
		},
		"tcp-reality": {
			`{"network":"tcp","security":"reality",` + realityBase + `}`,
			`{"server_name":"reality.example.test","alpn":["h2","http/1.1"]}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ep := endpointFrom(t, protocol.VMess, tc.stream, tc.client, 443)
			got := exportedALPN(t, NodeConfig{Endpoint: ep, Credential: uuid})
			kept := 0
			for _, format := range []string{FormatXray, FormatSingbox, FormatMihomo, FormatV2rayN} {
				if strings.Contains(strings.Join(got[format], ","), "h2") {
					kept++
				}
			}
			if kept == 0 {
				t.Fatalf("a non-upgrade transport lost its administrator ALPN in every format (got %+v)", got)
			}
		})
	}
}

// exportedALPNWithoutMihomo renders the ALPN-bearing formats only. exportedALPN
// renders every format and fails the test on a render error, but Mihomo has no
// httpupgrade transport by design, so a caller testing that transport must not
// ask for it.
func exportedALPNWithoutMihomo(t *testing.T, nc NodeConfig) map[string][]string {
	t.Helper()
	out := map[string][]string{}

	res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{nc}})
	if err != nil {
		t.Fatalf("xray render: %v", err)
	}
	if stream, ok := xrayOutboundFor(t, res)["streamSettings"].(map[string]any); ok {
		for _, section := range []string{"tlsSettings", "realitySettings"} {
			if sec, ok := stream[section].(map[string]any); ok {
				if raw, ok := sec["alpn"]; ok {
					out[FormatXray] = append(out[FormatXray], alpnOf(t, raw)...)
				}
			}
		}
	}

	res, err = Render(Request{Format: FormatSingbox, Nodes: []NodeConfig{nc}})
	if err != nil {
		t.Fatalf("sing-box render: %v", err)
	}
	if tls, ok := jsonOutboundFor(t, res)["tls"].(map[string]any); ok {
		if raw, ok := tls["alpn"]; ok {
			out[FormatSingbox] = alpnOf(t, raw)
		}
	}

	res, err = Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{nc}})
	if err != nil {
		t.Fatalf("v2rayN render: %v", err)
	}
	if v := v2rayNLinkALPN(t, res); v != "" {
		out[FormatV2rayN] = strings.Split(v, ",")
	}

	return out
}

// v2rayNLinkALPN extracts the alpn parameter from a rendered share link
// (vmess:// carries a base64 JSON payload; every other scheme uses a query).
func v2rayNLinkALPN(t *testing.T, res Result) string {
	t.Helper()
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
		v, _ := payload["alpn"].(string)
		return v
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("share link: %v", err)
	}
	return u.Query().Get("alpn")
}

// A plain ws node has no TLS layer, so there is no ALPN to force and the export
// must not invent one.
func TestPlainWSDoesNotGainALPN(t *testing.T) {
	stream := `{"network":"ws","security":"none","wsSettings":{"path":"/ws","headers":{"Host":"cdn.example.test"}}}`
	ep := endpointFrom(t, protocol.VMess, stream, `{}`, 80)
	if ep.Security != "none" && ep.Security != "" {
		t.Fatalf("endpoint security = %q, want no TLS layer", ep.Security)
	}
	for format, alpn := range exportedALPN(t, NodeConfig{Endpoint: ep, Credential: uuid}) {
		if len(alpn) != 0 {
			t.Fatalf("%s invented alpn %v on a plain ws node", format, alpn)
		}
	}
}
