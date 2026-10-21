package clientconfig

// These tests replay the A15 client matrix fixture
// (tests/e2e/client_matrix_test.go) through the real export pipeline: the admin
// create body is JSON decoded into domain.Object exactly like api.decodeJSON,
// then protocol.Sanitize, protocol.Validate, protocol.EndpointFor and
// clientconfig.Render run for every format. A fixture node the panel would
// answer with 422 is therefore visible offline, without the docker acceptance
// environment.
//
// The fixture is the one the matrix builds: the two docker fixture VLESS nodes
// plus VMess, Trojan/TLS with an inline self-signed certificate, legacy
// Shadowsocks and Shadowsocks 2022 with a server key.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
)

const matrixFixtureSNI = "matrix-trojan.test"

// matrixFixtureServerPSK is the Shadowsocks 2022 server key of the fixture.
// The matrix generates it randomly; a fixed value keeps the export inspectable.
var matrixFixtureServerPSK = base64.StdEncoding.EncodeToString([]byte("matrix-psk-16byt"))

// matrixCreateBody mirrors the api.handleCreateNode request body.
type matrixCreateBody struct {
	Name             string        `json:"name"`
	ServerID         string        `json:"server_id"`
	Enabled          *bool         `json:"enabled"`
	Host             string        `json:"host"`
	Port             int           `json:"port"`
	Listen           string        `json:"listen"`
	Protocol         string        `json:"protocol"`
	Flow             string        `json:"flow"`
	Cipher           string        `json:"cipher"`
	ServerKey        string        `json:"server_key"`
	StreamSettings   domain.Object `json:"stream_settings"`
	ProtocolSettings domain.Object `json:"protocol_settings"`
	ClientSettings   domain.Object `json:"client_settings"`
}

type matrixFixtureCase struct {
	Key      string
	Protocol string
	Cipher   string
	Security string
	Port     int
	Cert     bool
}

// matrixFixtureCases mirrors matrixSpecs of the e2e matrix.
var matrixFixtureCases = []matrixFixtureCase{
	{Key: "vmess-1", Protocol: "vmess", Security: "none", Port: 20102},
	{Key: "trojan-tls-1", Protocol: "trojan", Security: "tls", Port: 20103, Cert: true},
	{Key: "ss-1", Protocol: "shadowsocks", Cipher: "aes-128-gcm", Security: "none", Port: 20104},
	{Key: "ss2022-1", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm", Security: "none", Port: 20105},
}

func TestMatrixFixtureExportsEveryFormat(t *testing.T) {
	certLines, keyLines := matrixFixtureCertificate(t)
	nodes := matrixFixtureNodes(t, certLines, keyLines)
	if len(nodes) != 5 {
		t.Fatalf("fixture nodes = %d", len(nodes))
	}
	for _, format := range Formats {
		res, err := Render(Request{Format: format, SubscriptionName: "proxypanel", Nodes: nodes})
		if err != nil {
			t.Fatalf("%s cannot render the matrix fixture: %v", format, err)
		}
		if res.NodeCount != len(nodes) {
			t.Fatalf("%s node count = %d", format, res.NodeCount)
		}
	}
}

// TestMatrixFixtureTrojanTLSUsesCertificatePin proves the fixture's
// allow_insecure setting survives the export as the Xray 26 certificate pin.
func TestMatrixFixtureTrojanTLSUsesCertificatePin(t *testing.T) {
	certLines, keyLines := matrixFixtureCertificate(t)
	nodes := matrixFixtureNodes(t, certLines, keyLines)
	var trojan NodeConfig
	for _, n := range nodes {
		if n.Endpoint.Protocol == protocol.Trojan {
			trojan = n
		}
	}
	if trojan.Endpoint.PeerCertSha256 == "" {
		t.Fatal("fixture Trojan node lost the certificate pin")
	}
	res, err := Render(Request{Format: FormatXray, Nodes: []NodeConfig{trojan}})
	if err != nil {
		t.Fatalf("xray render: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("xray export is not JSON: %v", err)
	}
	outbound := matrixFirstOutbound(t, doc)
	stream, _ := outbound["streamSettings"].(map[string]any)
	tlsSettings, _ := stream["tlsSettings"].(map[string]any)
	if _, removed := tlsSettings["allowInsecure"]; removed {
		t.Fatal("xray export still carries the removed allowInsecure option")
	}
	if got, _ := tlsSettings["pinnedPeerCertSha256"].(string); got != trojan.Endpoint.PeerCertSha256 {
		t.Fatalf("pinnedPeerCertSha256 = %q", got)
	}
}

// TestMatrixFixtureShadowsocks2022CarriesServerPSK proves the fixture node with
// a server key exports a usable multi-user credential in every format.
func TestMatrixFixtureShadowsocks2022CarriesServerPSK(t *testing.T) {
	certLines, keyLines := matrixFixtureCertificate(t)
	nodes := matrixFixtureNodes(t, certLines, keyLines)
	want := matrixFixtureServerPSK + ":"
	for _, format := range []string{FormatXray, FormatSingbox, FormatMihomo} {
		res, err := Render(Request{Format: format, Nodes: nodes})
		if err != nil {
			t.Fatalf("%s render: %v", format, err)
		}
		if !strings.Contains(string(res.Body), want) {
			t.Fatalf("%s export dropped the Shadowsocks 2022 server PSK", format)
		}
	}
	links, err := Render(Request{Format: FormatV2rayN, Nodes: nodes})
	if err != nil {
		t.Fatalf("v2rayn render: %v", err)
	}
	if !matrixShareLinkCarries(string(links.Body), want) {
		t.Fatal("v2rayN share link dropped the Shadowsocks 2022 server PSK")
	}
}

// matrixShareLinkCarries reports whether a shared secret survives in the base64
// encoded Shadowsocks share links.
func matrixShareLinkCarries(body, want string) bool {
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ss://") {
			continue
		}
		payload := strings.TrimPrefix(line, "ss://")
		if i := strings.IndexByte(payload, '#'); i >= 0 {
			payload = payload[:i]
		}
		candidates := []string{payload}
		if i := strings.IndexByte(payload, '@'); i >= 0 {
			candidates = append(candidates, payload[:i])
		}
		for _, candidate := range candidates {
			decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(candidate, "="))
			if err != nil {
				continue
			}
			if strings.Contains(string(decoded), want) {
				return true
			}
		}
	}
	return false
}

func matrixFixtureNodes(t *testing.T, certLines, keyLines []string) []NodeConfig {
	t.Helper()
	bodies := []map[string]any{{
		"name": "docker-vless-1", "server_id": "srv_1", "enabled": true,
		"host": "127.0.0.1", "listen": "0.0.0.0", "port": 20101,
		"protocol": "vless", "cipher": "",
		"stream_settings":   map[string]any{"network": "tcp", "security": "none"},
		"protocol_settings": map[string]any{"decryption": "none"},
		"client_settings":   map[string]any{},
	}}
	for _, spec := range matrixFixtureCases {
		body := map[string]any{
			"name": spec.Key, "server_id": "srv_1", "enabled": true,
			"host": "127.0.0.1", "listen": "0.0.0.0", "port": spec.Port,
			"protocol": spec.Protocol, "cipher": spec.Cipher,
			"stream_settings":   map[string]any{"network": "tcp", "security": spec.Security},
			"protocol_settings": map[string]any{},
			"client_settings":   map[string]any{},
		}
		if spec.Cert {
			body["stream_settings"] = map[string]any{
				"network": "tcp", "security": "tls",
				"tlsSettings": map[string]any{"certificates": []any{map[string]any{
					"certificate": certLines, "key": keyLines,
				}}},
			}
			body["client_settings"] = map[string]any{"server_name": matrixFixtureSNI, "allow_insecure": true}
		}
		if strings.HasPrefix(spec.Cipher, "2022-") {
			body["server_key"] = matrixFixtureServerPSK
		}
		bodies = append(bodies, body)
	}
	out := make([]NodeConfig, 0, len(bodies))
	for _, body := range bodies {
		out = append(out, matrixFixtureNode(t, body))
	}
	return out
}

// matrixFixtureNode runs one create body through the sanitize, validate and
// endpoint projection the export handler uses.
func matrixFixtureNode(t *testing.T, body map[string]any) NodeConfig {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode fixture body: %v", err)
	}
	var in matrixCreateBody
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("fixture body rejected by the admin decode step: %v", err)
	}
	listen := in.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	node := protocol.Node{
		Protocol: in.Protocol, Flow: in.Flow, Cipher: in.Cipher, Listen: listen,
		Host: in.Host, Port: in.Port, StreamSettings: in.StreamSettings,
		ProtocolSettings: in.ProtocolSettings, ClientSettings: in.ClientSettings,
		ServerKey: in.ServerKey,
	}
	stream, settings, key, issues := protocol.Sanitize(node)
	if len(issues) > 0 {
		t.Fatalf("fixture %q sanitize: %v", in.Name, issues)
	}
	node.StreamSettings, node.ProtocolSettings, node.ServerKey = stream, settings, key
	if issues := protocol.Validate(node); len(issues) > 0 {
		t.Fatalf("fixture %q rejected by node validation: %v", in.Name, issues)
	}
	stored := domain.Node{
		ID: "nd_" + in.Name, Name: in.Name, Enabled: true, Host: in.Host, Port: in.Port,
		Listen: listen, Protocol: in.Protocol, Flow: in.Flow, Cipher: in.Cipher,
		StreamSettings: node.StreamSettings, ProtocolSettings: node.ProtocolSettings,
		ClientSettings: node.ClientSettings, ServerKey: node.ServerKey,
	}
	ep, issues := protocol.EndpointFor(stored)
	if len(issues) > 0 {
		t.Fatalf("fixture %q cannot be exported: %v", in.Name, issues[0].String())
	}
	return NodeConfig{Endpoint: ep, Credential: matrixFixtureCredential(in.Protocol, in.Cipher)}
}

func matrixFixtureCredential(protocolName, cipher string) string {
	switch {
	case protocolName == protocol.VLESS || protocolName == protocol.VMess:
		return "11111111-2222-4333-8444-555555555555"
	case strings.HasPrefix(cipher, "2022-"):
		return base64.StdEncoding.EncodeToString([]byte("matrix-user-psk1"))
	default:
		return "matrix-member-secret"
	}
}

func matrixFirstOutbound(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	outbounds, _ := doc["outbounds"].([]any)
	for _, raw := range outbounds {
		ob, _ := raw.(map[string]any)
		switch ob["protocol"] {
		case "", "freedom", "blackhole":
			continue
		}
		return ob
	}
	t.Fatal("xray export has no proxy outbound")
	return nil
}

// matrixFixtureCertificate builds the throwaway self-signed certificate the
// matrix stores inline in the Trojan TLS inbound.
func matrixFixtureCertificate(t *testing.T) ([]string, []string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: matrixFixtureSNI},
		DNSNames:     []string{matrixFixtureSNI},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(720 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("encode key: %v", err)
	}
	return pemLines(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		pemLines(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

func pemLines(block []byte) []string {
	out := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(block)), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			out = append(out, line)
		}
	}
	return out
}
