package clientconfig

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
)

const uuid = "11111111-2222-4333-8444-555555555555"

func realityNode() NodeConfig {
	return NodeConfig{
		Endpoint: protocol.Endpoint{
			NodeID: "nd_1", Name: "tokyo-reality", Protocol: protocol.VLESS,
			Address: "node.example.invalid", Port: 443,
			Network: "tcp", Security: "reality", Flow: "xtls-rprx-vision",
			SNI: "www.example.invalid", Fingerprint: "chrome",
			PublicKey: "PUBKEY", ShortID: "0123abcd",
		},
		Credential: uuid,
	}
}

func wsTLSNode() NodeConfig {
	return NodeConfig{
		Endpoint: protocol.Endpoint{
			NodeID: "nd_2", Name: "ws-tls", Protocol: protocol.VMess,
			Address: "ws.example.invalid", Port: 8443,
			Network: "ws", Path: "/ray", Host: "cdn.example.invalid",
			Security: "tls", SNI: "ws.example.invalid", ALPN: []string{"h2", "http/1.1"},
		},
		Credential: uuid,
	}
}

func ssNode() NodeConfig {
	return NodeConfig{
		Endpoint: protocol.Endpoint{
			NodeID: "nd_3", Name: "ss-2022", Protocol: protocol.Shadowsocks,
			Address: "ss.example.invalid", Port: 8388,
			Network: "tcp", Cipher: "2022-blake3-aes-128-gcm",
			ServerPSK: "SERVER-PSK",
		},
		Credential: "MTIzNDU2Nzg5MDEyMzQ1Ng==",
	}
}

func legacySSNode() NodeConfig {
	return NodeConfig{
		Endpoint: protocol.Endpoint{
			NodeID: "nd_4", Name: "ss-legacy", Protocol: protocol.Shadowsocks,
			Address: "ss.example.invalid", Port: 8389,
			Network: "tcp", Cipher: "aes-128-gcm",
		},
		Credential: "legacy-secret",
	}
}

func trojanTLSNode() NodeConfig {
	return NodeConfig{
		Endpoint: protocol.Endpoint{
			NodeID: "nd_5", Name: "trojan-tls", Protocol: protocol.Trojan,
			Address: "trojan.example.invalid", Port: 8443, Network: "tcp",
			Security: "tls", SNI: "trojan.example.invalid", AllowInsecure: true,
			PeerCertSha256: "211c2ef8faeea06a3ee365e03f111840bb287fc6f57a086dbaf1e9d0690c506c",
		},
		Credential: "trojan-secret",
	}
}

func TestRenderXrayRealityHasNoServerSecret(t *testing.T) {
	res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{realityNode()}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	outbounds, _ := doc["outbounds"].([]any)
	if len(outbounds) != 3 {
		t.Fatalf("outbounds = %d", len(outbounds))
	}
	proxy, _ := outbounds[0].(map[string]any)
	if proxy["protocol"] != "vless" {
		t.Fatalf("protocol = %v", proxy["protocol"])
	}
	settings, _ := proxy["settings"].(map[string]any)
	vnext, _ := settings["vnext"].([]any)
	first, _ := vnext[0].(map[string]any)
	users, _ := first["users"].([]any)
	user, _ := users[0].(map[string]any)
	if user["id"] != uuid || user["encryption"] != "none" || user["flow"] != "xtls-rprx-vision" {
		t.Fatalf("user = %v", user)
	}
	ss, _ := proxy["streamSettings"].(map[string]any)
	rs, _ := ss["realitySettings"].(map[string]any)
	if rs["publicKey"] != "PUBKEY" || rs["shortId"] != "0123abcd" || rs["serverName"] != "www.example.invalid" {
		t.Fatalf("realitySettings = %v", rs)
	}
	if got := string(res.Body); strings.Contains(got, "privateKey") {
		t.Fatal("member config must never carry a server private key")
	}
}

func TestRenderXrayEmptyNodesStillValid(t *testing.T) {
	res, err := Render(Request{Format: FormatXray})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("empty set must still be valid JSON: %v", err)
	}
	if res.NodeCount != 0 {
		t.Fatalf("node count = %d", res.NodeCount)
	}
}

func TestRenderSingboxRealityAndWS(t *testing.T) {
	res, err := Render(Request{Format: FormatSingbox, Nodes: []NodeConfig{realityNode(), wsTLSNode()}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	outbounds, _ := doc["outbounds"].([]any)
	if len(outbounds) != 3 {
		t.Fatalf("outbounds = %d", len(outbounds))
	}
	vless, _ := outbounds[0].(map[string]any)
	if vless["type"] != "vless" || vless["uuid"] != uuid || vless["flow"] != "xtls-rprx-vision" {
		t.Fatalf("vless outbound = %v", vless)
	}
	tls, _ := vless["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	if reality["public_key"] != "PUBKEY" || reality["short_id"] != "0123abcd" {
		t.Fatalf("reality = %v", reality)
	}
	if strings.Contains(string(res.Body), "privateKey") {
		t.Fatal("server private key leaked into member config")
	}
	vmess, _ := outbounds[1].(map[string]any)
	if vmess["type"] != "vmess" {
		t.Fatalf("vmess outbound = %v", vmess)
	}
	tr, _ := vmess["transport"].(map[string]any)
	if tr["type"] != "ws" || tr["path"] != "/ray" {
		t.Fatalf("transport = %v", tr)
	}
	headers, _ := tr["headers"].(map[string]any)
	if headers["Host"] != "cdn.example.invalid" {
		t.Fatalf("headers = %v", headers)
	}
}

func TestRenderMihomo(t *testing.T) {
	res, err := Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{realityNode(), ssNode()}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(res.Body)
	for _, want := range []string{"proxies:", "type: vless", "uuid: " + uuid, "reality-opts:", "public-key: PUBKEY", "type: ss", "cipher: 2022-blake3-aes-128-gcm", "proxy-groups:", "MATCH,PROXY"} {
		if !strings.Contains(body, want) {
			t.Fatalf("mihomo output missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "privateKey") {
		t.Fatal("server private key leaked")
	}
}

func TestRenderV2rayN(t *testing.T) {
	res, err := Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{realityNode(), wsTLSNode(), ssNode()}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(res.Body)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 links, got %d: %v", len(lines), lines)
	}
	u, err := url.Parse(lines[0])
	if err != nil {
		t.Fatalf("vless link: %v", err)
	}
	if u.Scheme != "vless" || u.User.Username() != uuid || u.Host != "node.example.invalid:443" {
		t.Fatalf("vless link = %s", lines[0])
	}
	q := u.Query()
	if q.Get("security") != "reality" || q.Get("pbk") != "PUBKEY" || q.Get("sid") != "0123abcd" ||
		q.Get("sni") != "www.example.invalid" || q.Get("flow") != "xtls-rprx-vision" || q.Get("type") != "tcp" {
		t.Fatalf("vless query = %v", q)
	}
	if !strings.HasPrefix(lines[1], "vmess://") {
		t.Fatalf("vmess link = %s", lines[1])
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(lines[1], "vmess://"))
	if err != nil {
		t.Fatalf("vmess base64: %v", err)
	}
	var vm map[string]any
	if err := json.Unmarshal(raw, &vm); err != nil {
		t.Fatalf("vmess json: %v", err)
	}
	if vm["add"] != "ws.example.invalid" || vm["net"] != "ws" || vm["path"] != "/ray" || vm["tls"] != "tls" {
		t.Fatalf("vmess payload = %v", vm)
	}
	if !strings.HasPrefix(lines[2], "ss://") {
		t.Fatalf("ss link = %s", lines[2])
	}
	ssURL, err := url.Parse(lines[2])
	if err != nil {
		t.Fatalf("ss parse: %v", err)
	}
	userinfo, err := base64.RawURLEncoding.DecodeString(ssURL.User.Username())
	if err != nil {
		t.Fatalf("ss userinfo: %v", err)
	}
	if string(userinfo) != "2022-blake3-aes-128-gcm:SERVER-PSK:MTIzNDU2Nzg5MDEyMzQ1Ng==" {
		t.Fatalf("ss userinfo = %s", userinfo)
	}
}

func TestRenderV2rayNBase64Wrapper(t *testing.T) {
	res, err := Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{realityNode()}, Base64: true})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(res.Body)))
	if err != nil {
		t.Fatalf("base64 wrapper: %v", err)
	}
	if !strings.HasPrefix(string(decoded), "vless://") {
		t.Fatalf("decoded = %s", decoded)
	}
}

func TestUnsupportedTransportIsExplicit(t *testing.T) {
	kcp := NodeConfig{
		Endpoint: protocol.Endpoint{
			NodeID: "nd_9", Name: "kcp-node", Protocol: protocol.VLESS,
			Address: "k.example.invalid", Port: 443, Network: "kcp", Security: "tls", SNI: "k.example.invalid",
		},
		Credential: uuid,
	}
	for _, format := range []string{FormatMihomo, FormatSingbox} {
		_, err := Render(Request{Format: format, Nodes: []NodeConfig{kcp}})
		var ue *UnsupportedError
		if !errors.As(err, &ue) {
			t.Fatalf("%s: expected UnsupportedError, got %v", format, err)
		}
		if ue.NodeName != "kcp-node" || ue.Field != "stream_settings.network" {
			t.Fatalf("%s: error = %+v", format, ue)
		}
		if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), uuid) {
			t.Fatalf("%s: error must not contain secrets: %v", format, err)
		}
	}
	// v2rayN can express mKCP
	if _, err := Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{kcp}}); err != nil {
		t.Fatalf("v2rayn should support kcp: %v", err)
	}
}

func TestNormalizeFormat(t *testing.T) {
	cases := map[string]string{
		"": FormatXray, "xray": FormatXray, "sing-box": FormatSingbox, "singbox": FormatSingbox,
		"clash": FormatMihomo, "mihomo": FormatMihomo, "v2rayn": FormatV2rayN, "v2ray": FormatV2rayN,
	}
	for in, want := range cases {
		if got, ok := NormalizeFormat(in); !ok || got != want {
			t.Fatalf("NormalizeFormat(%q) = %q %v", in, got, ok)
		}
	}
	if _, ok := NormalizeFormat("openvpn"); ok {
		t.Fatal("unsupported format must be rejected")
	}
}

func TestRenderDedupesRemarks(t *testing.T) {
	a := realityNode()
	b := realityNode()
	b.Credential = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	res, err := Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{a, b}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(res.Body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	if lines[0] == lines[1] {
		t.Fatal("duplicate links must still differ by credential")
	}
	if !strings.Contains(lines[1], "%2520%2523") && !strings.Contains(lines[1], "%20%23") {
		t.Fatalf("second node should get a unique remark: %s", lines[1])
	}
}

// Templates add presentation defaults, never replace generated endpoints,
// credentials, routes, listeners or proxy groups (including an empty node set).
func TestRenderTemplatePreservesGeneratedFields(t *testing.T) {
	for _, format := range []string{FormatXray, FormatSingbox, FormatMihomo} {
		for _, withNodes := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/nodes=%t", format, withNodes), func(t *testing.T) {
				req := Request{Format: format}
				if withNodes {
					req.Nodes = []NodeConfig{realityNode()}
				}
				decode := func(body []byte) map[string]any {
					t.Helper()
					var doc map[string]any
					var err error
					if format == FormatMihomo {
						err = yaml.Unmarshal(body, &doc)
					} else {
						err = json.Unmarshal(body, &doc)
					}
					if err != nil {
						t.Fatal(err)
					}
					return doc
				}
				base, err := Render(req)
				if err != nil {
					t.Fatal(err)
				}
				original := decode(base.Body)
				req.Template = domain.NewObject()
				for key := range original {
					if err := req.Template.Set(key, map[string]any{"unauthorized": "endpoint-and-credential"}); err != nil {
						t.Fatal(err)
					}
				}
				for _, server := range []string{"1.1.1.1", "9.9.9.9"} {
					if err := req.Template.Set("dns", map[string]any{"servers": []string{server}}); err != nil {
						t.Fatal(err)
					}
					res, err := Render(req)
					if err != nil {
						t.Fatal(err)
					}
					doc := decode(res.Body)
					for key, want := range original {
						if !reflect.DeepEqual(doc[key], want) {
							t.Fatalf("template overwrote panel-generated %s", key)
						}
					}
					dns, _ := doc["dns"].(map[string]any)
					servers, _ := dns["servers"].([]any)
					if len(servers) != 1 || servers[0] != server {
						t.Fatalf("template edit not reflected: dns=%v", dns)
					}
				}
			})
		}
	}
}

// ─── regression tests for the defects found by the real-core matrix ─────────

func xrayOutboundFor(t *testing.T, res Result) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("xray json: %v", err)
	}
	for _, raw := range doc["outbounds"].([]any) {
		ob, _ := raw.(map[string]any)
		switch ob["protocol"] {
		case "", "freedom", "blackhole":
			continue
		default:
			return ob
		}
	}
	t.Fatal("no proxy outbound")
	return nil
}

func yamlProxyFor(t *testing.T, res Result) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("mihomo yaml: %v", err)
	}
	proxies, _ := doc["proxies"].([]any)
	if len(proxies) != 1 {
		t.Fatalf("proxies = %v", doc["proxies"])
	}
	return proxies[0].(map[string]any)
}

func jsonOutboundFor(t *testing.T, res Result) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("sing-box json: %v", err)
	}
	for _, raw := range doc["outbounds"].([]any) {
		ob, _ := raw.(map[string]any)
		if kind, _ := ob["type"].(string); kind != "" && kind != "direct" {
			return ob
		}
	}
	t.Fatal("no proxy outbound")
	return nil
}

// Xray 26.3.27 refuses "allowInsecure" (migrated to pinnedPeerCertSha256), so
// the export must emit the certificate pin of the node it serves.
func TestXrayTLSReplacesAllowInsecureWithPin(t *testing.T) {
	n := trojanTLSNode()
	res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := xrayOutboundFor(t, res)
	stream, _ := out["streamSettings"].(map[string]any)
	tls, _ := stream["tlsSettings"].(map[string]any)
	if _, bad := tls["allowInsecure"]; bad {
		t.Fatalf("xray export must not carry the removed allowInsecure: %v", tls)
	}
	if tls["pinnedPeerCertSha256"] != n.Endpoint.PeerCertSha256 {
		t.Fatalf("pinnedPeerCertSha256 = %v, want %q", tls["pinnedPeerCertSha256"], n.Endpoint.PeerCertSha256)
	}
	if tls["serverName"] != n.Endpoint.SNI {
		t.Fatalf("serverName = %v", tls["serverName"])
	}
}

// allow_insecure without a pinnable certificate is rejected explicitly instead
// of being dropped, while clients that still implement insecure keep working.
func TestXrayAllowInsecureWithoutPinIsExplicit(t *testing.T) {
	n := trojanTLSNode()
	n.Endpoint.PeerCertSha256 = ""
	_, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{n}})
	var ue *UnsupportedError
	if !errors.As(err, &ue) || ue.Field != "client_settings.allow_insecure" {
		t.Fatalf("expected UnsupportedError on client_settings.allow_insecure, got %v", err)
	}
	for _, format := range []string{FormatSingbox, FormatMihomo, FormatV2rayN} {
		if _, err := Render(Request{Format: format, Nodes: []NodeConfig{n}}); err != nil {
			t.Fatalf("%s still supports insecure: %v", format, err)
		}
	}
}

// An explicit administrator pin is honored by Xray and rejected, not ignored,
// by the clients that cannot verify a certificate hash.
func TestExplicitCertificatePinRejectedWhereUnsupported(t *testing.T) {
	n := trojanTLSNode()
	n.Endpoint.PeerCertPinned = true
	if _, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{n}}); err != nil {
		t.Fatalf("xray must honor the pin: %v", err)
	}
	for _, format := range []string{FormatSingbox, FormatMihomo, FormatV2rayN} {
		_, err := Render(Request{Format: format, Nodes: []NodeConfig{n}})
		var ue *UnsupportedError
		if !errors.As(err, &ue) || ue.Field != "client_settings.pinned_peer_cert_sha256" {
			t.Fatalf("%s: expected a pin rejection, got %v", format, err)
		}
	}
}

// Mihomo always wraps trojan in TLS, so a plaintext trojan node must be an
// explicit rejection instead of a proxy that silently switches transport.
func TestMihomoPlaintextTrojanIsExplicit(t *testing.T) {
	n := NodeConfig{
		Endpoint: protocol.Endpoint{
			NodeID: "nd_plain", Name: "plain-trojan", Protocol: protocol.Trojan,
			Address: "t.example.invalid", Port: 8443, Network: "tcp", Security: "",
		},
		Credential: "trojan-secret",
	}
	_, err := Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{n}})
	var ue *UnsupportedError
	if !errors.As(err, &ue) || ue.Field != "stream_settings.security" {
		t.Fatalf("mihomo: expected a security rejection, got %v", err)
	}
	for _, format := range []string{FormatXray, FormatSingbox} {
		if _, err := Render(Request{Format: format, Nodes: []NodeConfig{n}}); err != nil {
			t.Fatalf("%s supports plaintext trojan: %v", format, err)
		}
	}
	// The share link must state security=none: a trojan link without it is
	// imported as TLS by v2rayN.
	res, err := Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("v2rayn: %v", err)
	}
	if !strings.Contains(string(res.Body), "security=none") {
		t.Fatalf("plaintext trojan link must state security=none: %s", res.Body)
	}
}

// Shadowsocks 2022 multi-user clients authenticate with <serverPSK>:<userPSK>.
func TestShadowsocks2022CredentialsCarryServerPSK(t *testing.T) {
	n := ssNode()
	want := n.Endpoint.ServerPSK + ":" + n.Credential

	res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("xray: %v", err)
	}
	settings, _ := xrayOutboundFor(t, res)["settings"].(map[string]any)
	server, _ := settings["servers"].([]any)[0].(map[string]any)
	if server["password"] != want {
		t.Fatalf("xray shadowsocks password does not carry the server PSK")
	}
	if server["method"] != n.Endpoint.Cipher {
		t.Fatalf("xray shadowsocks method = %v", server["method"])
	}

	res, err = Render(Request{Format: FormatSingbox, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("sing-box: %v", err)
	}
	if ob := jsonOutboundFor(t, res); ob["password"] != want {
		t.Fatalf("sing-box shadowsocks password does not carry the server PSK")
	}

	res, err = Render(Request{Format: FormatMihomo, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("mihomo: %v", err)
	}
	if p := yamlProxyFor(t, res); p["password"] != want {
		t.Fatalf("mihomo shadowsocks password does not carry the server PSK")
	}

	res, err = Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("v2rayn: %v", err)
	}
	u, err := url.Parse(strings.TrimSpace(string(res.Body)))
	if err != nil {
		t.Fatalf("ss link: %v", err)
	}
	userinfo, err := base64.RawURLEncoding.DecodeString(u.User.Username())
	if err != nil {
		t.Fatalf("ss userinfo: %v", err)
	}
	if string(userinfo) != n.Endpoint.Cipher+":"+want {
		t.Fatalf("v2rayn ss userinfo does not carry the server PSK")
	}
}

// Without the server PSK the 2022 credential cannot be built: the formats must
// say so instead of emitting the broken password the cores reject.
func TestShadowsocks2022WithoutServerPSKIsExplicit(t *testing.T) {
	n := ssNode()
	n.Endpoint.ServerPSK = ""
	for _, format := range Formats {
		_, err := Render(Request{Format: format, Nodes: []NodeConfig{n}})
		var ue *UnsupportedError
		if !errors.As(err, &ue) || ue.Field != "server_key" {
			t.Fatalf("%s: expected a server_key rejection, got %v", format, err)
		}
		if strings.Contains(err.Error(), n.Credential) {
			t.Fatalf("%s: error must not leak credentials: %v", format, err)
		}
	}
}

// Legacy (non 2022) Shadowsocks keeps the plain member secret in every format.
func TestLegacyShadowsocksKeepsMemberSecret(t *testing.T) {
	n := legacySSNode()
	res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("xray: %v", err)
	}
	settings, _ := xrayOutboundFor(t, res)["settings"].(map[string]any)
	server, _ := settings["servers"].([]any)[0].(map[string]any)
	if server["password"] != n.Credential {
		t.Fatalf("xray legacy shadowsocks password was rewritten")
	}
	res, err = Render(Request{Format: FormatV2rayN, Nodes: []NodeConfig{n}})
	if err != nil {
		t.Fatalf("v2rayn: %v", err)
	}
	u, err := url.Parse(strings.TrimSpace(string(res.Body)))
	if err != nil {
		t.Fatal(err)
	}
	userinfo, err := base64.RawURLEncoding.DecodeString(u.User.Username())
	if err != nil {
		t.Fatal(err)
	}
	if string(userinfo) != n.Endpoint.Cipher+":"+n.Credential {
		t.Fatalf("v2rayn legacy ss userinfo = %s", userinfo)
	}
}
