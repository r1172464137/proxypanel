package protocol

import (
	"encoding/json"
	"testing"

	"proxypanel/internal/panel/domain"
)

// vlessNativeStream lists every streamSettings field the fixed Xray core
// accepts. The panel must normalise them without dropping any.
const vlessNativeStream = `{
  "network": "tcp",
  "security": "reality",
  "tcpSettings": {
    "acceptProxyProtocol": true,
    "header": {
      "type": "http",
      "request": {"version": "1.1", "method": "GET", "path": ["/"], "headers": {"Host": ["www.example.invalid"]}},
      "response": {"version": "1.1", "status": "200", "reason": "OK", "headers": {}}
    }
  },
  "realitySettings": {
    "show": false,
    "dest": "www.example.invalid:443",
    "xver": 1,
    "serverNames": ["www.example.invalid", "cdn.example.invalid"],
    "privateKey": "SERVER_PRIVATE_KEY_PLACEHOLDER",
    "minClientVer": "1.8.0",
    "maxClientVer": "",
    "maxTimeDiff": 10000,
    "shortIds": ["0123abcd", "deadbeef"],
    "fingerprint": "chrome",
    "spiderX": "/"
  },
  "sockopt": {
    "mark": 255,
    "tcpFastOpen": true,
    "tcpNoDelay": true,
    "tcpKeepAliveIdle": 30,
    "tcpUserTimeout": 10000,
    "domainStrategy": "UseIP",
    "dialerProxy": "",
    "acceptProxyProtocol": false,
    "tcpMptcp": true
  }
}`

const vlessNativeProtocol = `{
  "decryption": "none",
  "fallbacks": [
    {"name": "h2", "dest": 8443, "xver": 1, "alpn": "h2"},
    {"path": "/ws", "dest": 8080}
  ]
}`

const vlessNativeClient = `{
  "server_name": "www.example.invalid",
  "public_key": "CLIENT_PUBLIC_KEY_PLACEHOLDER",
  "short_id": "0123abcd",
  "fingerprint": "chrome",
  "alpn": ["h2", "http/1.1"],
  "allow_insecure": false,
  "spider_x": "/"
}`

func mustObject(t *testing.T, raw string) domain.Object {
	t.Helper()
	var o domain.Object
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("bad object: %v", err)
	}
	return o
}

func nativeNode(t *testing.T) Node {
	t.Helper()
	return Node{
		Protocol: VLESS, Flow: "xtls-rprx-vision", Host: "node.example.invalid", Port: 443,
		Listen: "0.0.0.0",
		StreamSettings:   mustObject(t, vlessNativeStream),
		ProtocolSettings: mustObject(t, vlessNativeProtocol),
		ClientSettings:   mustObject(t, vlessNativeClient),
	}
}

func TestVLESSEveryNativeFieldSurvives(t *testing.T) {
	n := nativeNode(t)
	stream, ps, key, issues := Sanitize(n)
	if len(issues) != 0 {
		t.Fatalf("sanitize issues: %v", issues)
	}
	if key != "SERVER_PRIVATE_KEY_PLACEHOLDER" {
		t.Fatalf("private key not extracted: %q", key)
	}
	// every streamSettings key must still be present
	for _, k := range []string{"network", "security", "tcpSettings", "realitySettings", "sockopt"} {
		if !stream.Has(k) {
			t.Fatalf("stream_settings lost %q", k)
		}
	}
	rs, _ := stream.Sub("realitySettings")
	for _, k := range []string{"show", "dest", "xver", "serverNames", "minClientVer", "maxClientVer",
		"maxTimeDiff", "shortIds", "fingerprint", "spiderX"} {
		if !rs.Has(k) {
			t.Fatalf("realitySettings lost %q", k)
		}
	}
	if rs.Has("privateKey") {
		t.Fatal("privateKey must not be persisted in stream_settings")
	}
	sock, _ := stream.Sub("sockopt")
	for _, k := range []string{"mark", "tcpFastOpen", "tcpNoDelay", "tcpKeepAliveIdle", "tcpUserTimeout",
		"domainStrategy", "dialerProxy", "acceptProxyProtocol", "tcpMptcp"} {
		if !sock.Has(k) {
			t.Fatalf("sockopt lost %q", k)
		}
	}
	tcp, _ := stream.Sub("tcpSettings")
	if !tcp.Has("header") || !tcp.Has("acceptProxyProtocol") {
		t.Fatalf("tcpSettings lost fields: %v", tcp)
	}
	if !ps.Has("fallbacks") || !ps.Has("decryption") {
		t.Fatal("protocol_settings lost vless native fields")
	}

	// validation accepts the full native configuration
	n.StreamSettings, n.ProtocolSettings, n.ServerKey = stream, ps, key
	if issues := Validate(n); len(issues) != 0 {
		t.Fatalf("native VLESS config rejected: %v", issues)
	}

	// member facing endpoint exposes the client parameters and never the key
	node := domain.Node{
		ID: "nd_native", Name: "native", Protocol: "vless", Flow: "xtls-rprx-vision",
		Host: "node.example.invalid", Port: 443, Listen: "0.0.0.0", ServerKey: key,
		StreamSettings: stream, ProtocolSettings: ps, ClientSettings: n.ClientSettings,
	}
	ep, epIssues := EndpointFor(node)
	if len(epIssues) != 0 {
		t.Fatalf("endpoint issues: %v", epIssues)
	}
	if ep.PublicKey != "CLIENT_PUBLIC_KEY_PLACEHOLDER" || ep.ShortID != "0123abcd" ||
		ep.SNI != "www.example.invalid" || ep.Fingerprint != "chrome" || ep.Flow != "xtls-rprx-vision" {
		t.Fatalf("endpoint = %+v", ep)
	}
	if ep.PublicKey == key {
		t.Fatal("server private key leaked into the client endpoint")
	}
}

