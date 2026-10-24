package protocol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
)

func obj(t *testing.T, raw string) domain.Object {
	t.Helper()
	var o domain.Object
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("bad object %s: %v", raw, err)
	}
	return o
}

// selfSignedPEM returns a one-off ECDSA leaf certificate as PEM text and the
// lowercase hex SHA-256 of its DER encoding (the Xray pin encoding).
func selfSignedPEM(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "matrix-trojan.test"},
		DNSNames:              []string{"matrix-trojan.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), hex.EncodeToString(sum[:])
}

// jsonString encodes a value as a JSON string literal for direct embedding.
func jsonString(t *testing.T, v string) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func realityNode(t *testing.T) Node {
	t.Helper()
	return Node{
		Protocol: VLESS,
		Flow:     "xtls-rprx-vision",
		Host:     "node.example.invalid",
		Port:     443,
		Listen:   "0.0.0.0",
		StreamSettings: obj(t, `{
			"network":"tcp",
			"security":"reality",
			"realitySettings":{"privateKey":"dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo","shortIds":["0123abcd",""],"serverNames":["www.example.invalid"],"dest":"www.example.invalid:443"},
			"sockopt":{"tcpFastOpen":true,"mark":255}
		}`),
		ProtocolSettings: obj(t, `{"decryption":"none","fallbacks":[{"dest":80}]}`),
		ClientSettings:   obj(t, `{"server_name":"www.example.invalid","public_key":"hSDwCYkwp1R0i33ctD73Wg2_Og0mOBr066SpjqqbTmo","short_id":"0123abcd","fingerprint":"chrome","alpn":["h2","http/1.1"]}`),
	}
}

func fields(issues []Issue) map[string]bool {
	m := map[string]bool{}
	for _, i := range issues {
		m[i.Field] = true
	}
	return m
}

func TestValidateRealityVLESS(t *testing.T) {
	if issues := Validate(realityNode(t)); len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
}

func TestValidateRejectsUnknownProtocolAndTransport(t *testing.T) {
	n := realityNode(t)
	n.Protocol = "hysteria2"
	if f := fields(Validate(n)); !f["protocol"] {
		t.Fatalf("expected protocol issue, got %v", f)
	}
	n = realityNode(t)
	n.StreamSettings["network"] = json.RawMessage(`"icmp"`)
	if f := fields(Validate(n)); !f["stream_settings.network"] {
		t.Fatalf("expected network issue, got %v", f)
	}
}

func TestValidateRejectsAdminSuppliedClients(t *testing.T) {
	n := realityNode(t)
	n.ProtocolSettings["clients"] = json.RawMessage(`[{"id":"00000000-0000-4000-8000-000000000000"}]`)
	if f := fields(Validate(n)); !f["protocol_settings.clients"] {
		t.Fatalf("admin supplied clients must be rejected, got %v", f)
	}
}

func TestValidateRealityRequirements(t *testing.T) {
	n := realityNode(t)
	delete(n.StreamSettings, "realitySettings")
	n.ClientSettings = domain.NewObject()
	f := fields(Validate(n))
	for _, want := range []string{"stream_settings.realitySettings", "client_settings.public_key", "client_settings.server_name"} {
		if !f[want] {
			t.Fatalf("missing issue %q in %v", want, f)
		}
	}
}

func TestValidateRejectsLegacyFlow(t *testing.T) {
	n := realityNode(t)
	n.Flow = "xtls-rprx-direct"
	if f := fields(Validate(n)); !f["flow"] {
		t.Fatalf("legacy flow must be rejected, got %v", f)
	}
}

func TestValidateFlowTransportCombination(t *testing.T) {
	n := realityNode(t)
	n.StreamSettings["network"] = json.RawMessage(`"ws"`)
	n.StreamSettings["wsSettings"] = json.RawMessage(`{"path":"/ray"}`)
	n.StreamSettings["security"] = json.RawMessage(`"none"`)
	delete(n.StreamSettings, "realitySettings")
	n.ClientSettings = domain.NewObject()
	if f := fields(Validate(n)); !f["flow"] {
		t.Fatalf("vision flow over ws must be rejected, got %v", f)
	}
}

func TestValidateShadowsocks2022(t *testing.T) {
	mk := func(method string, extra string) Node {
		return Node{
			Protocol: Shadowsocks, Cipher: method, Host: "n.example.invalid", Port: 8388, Listen: "0.0.0.0",
			StreamSettings:   obj(t, `{"network":"tcp","security":"none"}`),
			ProtocolSettings: obj(t, `{"network":"tcp"`+extra+`}`),
			ClientSettings:   domain.NewObject(),
		}
	}
	if issues := Validate(mk("2022-blake3-aes-128-gcm", "")); !fields(issues)["server_key"] {
		t.Fatalf("2022 requires a server key: %v", issues)
	}
	if issues := Validate(mk("2022-blake3-aes-128-gcm", `,"password":"c2VydmVyLWtleQ=="`)); len(issues) != 0 {
		t.Fatalf("2022 with server password should validate: %v", issues)
	}
	if issues := Validate(mk("chacha20-ietf-poly1305", `,"password":"nope"`)); !fields(issues)["protocol_settings.password"] {
		t.Fatalf("legacy SS must not accept an inbound password: %v", issues)
	}
	if issues := Validate(mk("rc4-md5", "")); !fields(issues)["cipher"] {
		t.Fatalf("unsupported SS method must be rejected: %v", issues)
	}
}

func TestValidateVMessAndTrojan(t *testing.T) {
	// TLS 入站必须带证书材料（否则核心启动失败），所以这里给一组证书文件路径。
	v := Node{
		Protocol: VMess, Cipher: "auto", Host: "n.example.invalid", Port: 443, Listen: "0.0.0.0",
		StreamSettings: obj(t, `{"network":"ws","security":"tls","wsSettings":{"path":"/ws"},`+
			`"tlsSettings":{"serverName":"n.example.invalid","certificateFile":"/etc/xray/fullchain.pem","keyFile":"/etc/xray/privkey.pem"}}`),
		ProtocolSettings: domain.NewObject(),
		ClientSettings:   domain.NewObject(),
	}
	if issues := Validate(v); len(issues) != 0 {
		t.Fatalf("vmess/ws/tls should validate: %v", issues)
	}
	v.Flow = "xtls-rprx-vision"
	if f := fields(Validate(v)); !f["flow"] {
		t.Fatalf("flow not applicable to vmess: %v", f)
	}
	tr := Node{
		Protocol: Trojan, Host: "n.example.invalid", Port: 443, Listen: "0.0.0.0",
		StreamSettings: obj(t, `{"network":"grpc","security":"tls","grpcSettings":{"serviceName":"g"},`+
			`"tlsSettings":{"serverName":"n.example.invalid","certificateFile":"/etc/xray/fullchain.pem","keyFile":"/etc/xray/privkey.pem"}}`),
		ProtocolSettings: domain.NewObject(),
		ClientSettings:   domain.NewObject(),
	}
	if issues := Validate(tr); len(issues) != 0 {
		t.Fatalf("trojan/grpc/tls should validate: %v", issues)
	}
}

func TestSanitizeMovesSecretsOutOfPersistedSettings(t *testing.T) {
	n := realityNode(t)
	stream, ps, serverKey, issues := Sanitize(n)
	if len(issues) != 0 {
		t.Fatalf("sanitize issues: %v", issues)
	}
	if serverKey != "dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo" {
		t.Fatalf("server key not extracted: %q", serverKey)
	}
	if stream.Has("realitySettings") {
		rs, _ := stream.Sub("realitySettings")
		if rs.Has("privateKey") {
			t.Fatal("private key must not be persisted in stream_settings")
		}
		if !rs.Has("shortIds") {
			t.Fatal("shortIds must be preserved")
		}
	}
	if !ps.Has("fallbacks") || !stream.Has("sockopt") {
		t.Fatal("unknown/extended fields must be preserved, not dropped")
	}

	ss := Node{Protocol: Shadowsocks, Cipher: "2022-blake3-aes-256-gcm", ProtocolSettings: obj(t, `{"method":"2022-blake3-aes-256-gcm","password":"PSK"}`)}
	_, ps2, key2, _ := Sanitize(ss)
	if key2 != "PSK" {
		t.Fatalf("ss server password not extracted: %q", key2)
	}
	if ps2.Has("password") {
		t.Fatal("ss server password must not stay in protocol_settings")
	}
}

func TestGenerateX25519(t *testing.T) {
	priv, pub, err := GenerateX25519()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(priv) != 43 || len(pub) != 43 {
		t.Fatalf("unexpected key encoding lengths: %d %d", len(priv), len(pub))
	}
	if priv == pub {
		t.Fatal("public key must differ from private key")
	}
}

func toDomain(n Node) domain.Node {
	return domain.Node{
		ID:               "nd_1",
		Name:             "tokyo-1",
		Protocol:         n.Protocol,
		Flow:             n.Flow,
		Cipher:           n.Cipher,
		Host:             n.Host,
		Port:             n.Port,
		Listen:           n.Listen,
		StreamSettings:   n.StreamSettings,
		ProtocolSettings: n.ProtocolSettings,
		ClientSettings:   n.ClientSettings,
		ServerKey:        n.ServerKey,
	}
}

func TestEndpointForReality(t *testing.T) {
	n := realityNode(t)
	n.StreamSettings, n.ProtocolSettings, n.ServerKey, _ = Sanitize(n)
	ep, issues := EndpointFor(toDomain(n))
	if len(issues) != 0 {
		t.Fatalf("endpoint issues: %v", issues)
	}
	if ep.Network != "tcp" || ep.Security != "reality" || ep.Flow != "xtls-rprx-vision" {
		t.Fatalf("endpoint = %+v", ep)
	}
	if ep.PublicKey != "hSDwCYkwp1R0i33ctD73Wg2_Og0mOBr066SpjqqbTmo" || ep.ShortID != "0123abcd" || ep.SNI != "www.example.invalid" {
		t.Fatalf("reality client params = %+v", ep)
	}
	if ep.Fingerprint != "chrome" {
		t.Fatalf("fingerprint = %q", ep.Fingerprint)
	}
}

func TestEndpointForTransportParams(t *testing.T) {
	n := domain.Node{
		ID: "nd_2", Name: "ws-node", Protocol: VLESS, Host: "n.example.invalid", Port: 443,
		StreamSettings: obj(t, `{"network":"ws","security":"tls","wsSettings":{"path":"/ray","headers":{"Host":"cdn.example.invalid"}},"tlsSettings":{"serverName":"n.example.invalid","alpn":["h2"]}}`),
		ClientSettings: domain.NewObject(),
	}
	ep, issues := EndpointFor(n)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if ep.Path != "/ray" || ep.Host != "cdn.example.invalid" || ep.SNI != "n.example.invalid" {
		t.Fatalf("ws endpoint = %+v", ep)
	}
	if len(ep.ALPN) != 1 || ep.ALPN[0] != "h2" {
		t.Fatalf("alpn = %v", ep.ALPN)
	}

	gr := domain.Node{
		ID: "nd_3", Name: "grpc", Protocol: Trojan, Host: "n.example.invalid", Port: 443,
		StreamSettings: obj(t, `{"network":"grpc","security":"tls","grpcSettings":{"serviceName":"svc","multiMode":true},"tlsSettings":{"serverName":"n.example.invalid"}}`),
		ClientSettings: domain.NewObject(),
	}
	ep, issues = EndpointFor(gr)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if ep.ServiceName != "svc" || ep.GRPCMode != "multi" {
		t.Fatalf("grpc endpoint = %+v", ep)
	}
}

func TestEndpointMissingRealityParams(t *testing.T) {
	n := domain.Node{
		ID: "nd_4", Protocol: VLESS, Host: "n.example.invalid", Port: 443,
		StreamSettings: obj(t, `{"network":"tcp","security":"reality","realitySettings":{"shortIds":["ab"]}}`),
		ClientSettings: domain.NewObject(),
	}
	_, issues := EndpointFor(n)
	f := fields(issues)
	if !f["client_settings.public_key"] || !f["client_settings.server_name"] {
		t.Fatalf("expected explicit missing-param issues, got %v", f)
	}
}

func TestValidateShadowsocksRejectsMultiUserUnsupportedMethod(t *testing.T) {
	// Xray 26.3.27 accepts "none" only for a single user inbound; the panel
	// always injects a clients array, where the core rejects it.
	n := Node{
		Protocol: Shadowsocks, Cipher: "none", Host: "n.example.invalid", Port: 8388, Listen: "0.0.0.0",
		StreamSettings:   obj(t, `{"network":"tcp","security":"none"}`),
		ProtocolSettings: domain.NewObject(),
		ClientSettings:   domain.NewObject(),
	}
	if issues := Validate(n); !fields(issues)["cipher"] {
		t.Fatalf("shadowsocks none must be rejected: %v", issues)
	}
}

func TestEndpointForShadowsocks2022CarriesServerPSK(t *testing.T) {
	node := domain.Node{
		ID: "nd_ss", Name: "ss", Protocol: Shadowsocks, Cipher: "2022-blake3-aes-128-gcm",
		Host: "ss.example.invalid", Port: 8388, Listen: "0.0.0.0", ServerKey: "SERVER_PSK",
		StreamSettings:   obj(t, `{"network":"tcp","security":"none"}`),
		ProtocolSettings: domain.NewObject(),
		ClientSettings:   domain.NewObject(),
	}
	ep, issues := EndpointFor(node)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if ep.ServerPSK != "SERVER_PSK" {
		t.Fatalf("server psk = %q", ep.ServerPSK)
	}
	node.Cipher = "aes-128-gcm"
	node.ServerKey = "LEGACY_SERVER_SECRET"
	ep, issues = EndpointFor(node)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if ep.ServerPSK != "" {
		t.Fatalf("legacy shadowsocks must not expose a server secret: %q", ep.ServerPSK)
	}
	node.Cipher = "2022-blake3-aes-128-gcm"
	node.ServerKey = ""
	if _, issues := EndpointFor(node); !fields(issues)["server_key"] {
		t.Fatalf("2022 without a server key must be refused: %v", issues)
	}
}

func TestEndpointForTLSDerivesCertificatePin(t *testing.T) {
	certPEM, wantPin := selfSignedPEM(t)
	node := domain.Node{
		ID: "nd_tls", Name: "tls", Protocol: Trojan, Host: "t.example.invalid", Port: 8443, Listen: "0.0.0.0",
		StreamSettings:   obj(t, `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"t.example.invalid","certificates":[{"certificate":`+jsonString(t, certPEM)+`}]}}`),
		ProtocolSettings: domain.NewObject(),
		ClientSettings:   obj(t, `{"server_name":"t.example.invalid","allow_insecure":true}`),
	}
	ep, issues := EndpointFor(node)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if ep.PeerCertSha256 != wantPin || ep.PeerCertPinned {
		t.Fatalf("pin = %q pinned=%t want %q", ep.PeerCertSha256, ep.PeerCertPinned, wantPin)
	}
	if !ep.AllowInsecure {
		t.Fatal("allow_insecure was dropped")
	}
}

// TestEndpointForTLSDerivesCertificatePinFromLineArray covers the second Xray
// accepted form of an inline certificate: the individual PEM lines instead of
// one PEM document. A node stored that way (the node API and the client matrix
// fixture use it) must derive the same pin, otherwise the Xray export answers
// 422 for a Trojan/TLS node that only sets allow_insecure.
func TestEndpointForTLSDerivesCertificatePinFromLineArray(t *testing.T) {
	certPEM, wantPin := selfSignedPEM(t)
	lines, err := json.Marshal(strings.Split(strings.TrimSpace(certPEM), "\n"))
	if err != nil {
		t.Fatal(err)
	}
	node := domain.Node{
		ID: "nd_tls_lines", Name: "tls-lines", Protocol: Trojan, Host: "t.example.invalid", Port: 8443, Listen: "0.0.0.0",
		StreamSettings:   obj(t, `{"network":"tcp","security":"tls","tlsSettings":{"certificates":[{"certificate":`+string(lines)+`}]}}`),
		ProtocolSettings: domain.NewObject(),
		ClientSettings:   obj(t, `{"server_name":"t.example.invalid","allow_insecure":true}`),
	}
	ep, issues := EndpointFor(node)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if ep.PeerCertSha256 != wantPin || ep.PeerCertPinned {
		t.Fatalf("pin = %q pinned=%t want %q", ep.PeerCertSha256, ep.PeerCertPinned, wantPin)
	}
}

func TestEndpointForExplicitCertificatePin(t *testing.T) {
	_, pin := selfSignedPEM(t)
	node := domain.Node{
		ID: "nd_tls", Name: "tls", Protocol: Trojan, Host: "t.example.invalid", Port: 8443, Listen: "0.0.0.0",
		StreamSettings:   obj(t, `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"t.example.invalid"}}`),
		ProtocolSettings: domain.NewObject(),
		ClientSettings:   obj(t, `{"server_name":"t.example.invalid","pinned_peer_cert_sha256":"`+pin+`"}`),
	}
	ep, issues := EndpointFor(node)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if ep.PeerCertSha256 != pin || !ep.PeerCertPinned {
		t.Fatalf("explicit pin = %q pinned=%t", ep.PeerCertSha256, ep.PeerCertPinned)
	}
	if ep.AllowInsecure {
		t.Fatal("allow_insecure must not be inferred from a pin")
	}
}

func TestNormalizeCertSHA256(t *testing.T) {
	_, hexPin := selfSignedPEM(t)
	raw, err := hex.DecodeString(hexPin)
	if err != nil {
		t.Fatal(err)
	}
	for _, enc := range []string{
		hexPin,
		strings.ToUpper(hexPin),
		base64.StdEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
	} {
		got, err := NormalizeCertSHA256(enc)
		if err != nil || got != hexPin {
			t.Fatalf("NormalizeCertSHA256(%q) = %q %v", enc, got, err)
		}
	}
	if _, err := NormalizeCertSHA256("not-a-hash"); err == nil {
		t.Fatal("invalid pin must be rejected")
	}
}
