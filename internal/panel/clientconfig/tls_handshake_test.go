package clientconfig_test

// Real-core TLS acceptance: a saved node must produce a document the pinned
// Xray accepts AND serves a usable TLS handshake. The review probe proved that
// `xray run -test` alone is not enough: a legacy top-level certificateFile pair
// passed the config check yet failed every handshake with
// "tls: unrecognized name" (frontend-protocol-review.md, D08).
//
// These tests only use freshly generated synthetic material, a temporary
// directory and a loopback port. They skip when the core binary is absent.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/panel/clientconfig"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/xrayconf"
)

const tlsFixtureSNI = "tls-handshake.example.invalid"

func tlsFixtureCore(t *testing.T) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("XRAY_BIN"))
	if path == "" {
		t.Skip("XRAY_BIN is not set; real TLS handshake acceptance needs the pinned core")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("XRAY_BIN (%s) is not available: %v", path, err)
	}
	return path
}

// writePair writes a real self-signed certificate/private key pair and returns
// PEM text plus the file paths.
func writePair(t *testing.T) (certPEM, keyPEM, certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{tlsFixtureSNI},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
	dir := t.TempDir()
	certPath = filepath.Join(dir, "fixture-cert.pem")
	keyPath = filepath.Join(dir, "fixture-key.pem")
	if err := os.WriteFile(certPath, []byte(certPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, certPath, keyPath
}

func tlsFixtureNode(t *testing.T, tlsSettings string) domain.Node {
	t.Helper()
	var stream domain.Object
	if err := json.Unmarshal([]byte(`{"network":"tcp","security":"tls","tlsSettings":`+tlsSettings+`}`), &stream); err != nil {
		t.Fatal(err)
	}
	var client domain.Object
	if err := json.Unmarshal([]byte(`{"server_name":"`+tlsFixtureSNI+`"}`), &client); err != nil {
		t.Fatal(err)
	}
	var ps domain.Object
	if err := json.Unmarshal([]byte(`{"decryption":"none"}`), &ps); err != nil {
		t.Fatal(err)
	}
	return domain.Node{
		ID: "nd_tls_fixture", Name: "tls-fixture", Protocol: protocol.VLESS,
		Host: tlsFixtureSNI, Port: 8443, Listen: "127.0.0.1",
		StreamSettings: stream, ProtocolSettings: ps, ClientSettings: client,
	}
}

// runHandshake starts the pinned core on an isolated loopback port with the
// given node, waits for the listener and performs a real TLS handshake.
func runHandshake(t *testing.T, core string, node domain.Node) (handshakeErr error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	node.Port = port
	node.ExternalPort = 0

	config, err := xrayconf.Build(node, []xrayconf.Client{{Email: "fixture", UUID: "11111111-2222-4333-8444-555555555555"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(core, "run", "-test", "-c", configPath).CombinedOutput(); err != nil {
		t.Fatalf("core config check rejected the document: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, core, "run", "-c", configPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()
	address := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port))
	ready := false
	for i := 0; i < 150; i++ {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("core never listened on %s", address)
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{
		InsecureSkipVerify: true, // synthetic self-signed fixture; the pin is covered elsewhere
		ServerName:         tlsFixtureSNI,
		MinVersion:         tls.VersionTLS12,
	})
	if conn != nil {
		_ = conn.Close()
	}
	return err
}

func TestSavedNodeTLSHandshakeSucceedsAfterNormalization(t *testing.T) {
	core := tlsFixtureCore(t)
	certPEM, keyPEM, certPath, keyPath := writePair(t)
	cases := map[string]string{
		"core native inline":     `{"serverName":"` + tlsFixtureSNI + `","certificates":[{"certificate":[` + jsonStringUTF8(certPEM) + `],"key":[` + jsonStringUTF8(keyPEM) + `]}]}`,
		"core native files":      `{"serverName":"` + tlsFixtureSNI + `","certificates":[{"certificateFile":` + jsonStringUTF8(certPath) + `,"keyFile":` + jsonStringUTF8(keyPath) + `}]}`,
		"legacy top-level files": `{"serverName":"` + tlsFixtureSNI + `","certificateFile":` + jsonStringUTF8(certPath) + `,"keyFile":` + jsonStringUTF8(keyPath) + `}`,
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			node := tlsFixtureNode(t, settings)
			// The persisted shape is what Sanitize produced when the node was
			// saved, and Validate is what the admin API answers with.
			stream, ps, key, issues := protocol.Sanitize(protocol.Node{
				Protocol: node.Protocol, Host: node.Host, Port: node.Port, Listen: node.Listen,
				StreamSettings: node.StreamSettings, ProtocolSettings: node.ProtocolSettings,
				ClientSettings: node.ClientSettings,
			})
			if len(issues) > 0 {
				t.Fatalf("sanitize: %v", issues)
			}
			node.StreamSettings, node.ProtocolSettings, node.ServerKey = stream, ps, key
			if issues := protocol.Validate(protocol.Node{
				Protocol: node.Protocol, Host: node.Host, Port: node.Port, Listen: node.Listen,
				StreamSettings: node.StreamSettings, ProtocolSettings: node.ProtocolSettings,
				ClientSettings: node.ClientSettings,
			}); len(issues) > 0 {
				t.Fatalf("validate: %v", issues)
			}
			// No legacy top-level field may survive persistence: the core ignores it.
			ts, _ := node.StreamSettings.Sub("tlsSettings")
			if ts.Has("certificateFile") || ts.Has("keyFile") {
				t.Fatal("legacy top-level certificate fields survived sanitize")
			}
			if err := runHandshake(t, core, node); err != nil {
				t.Fatalf("saved node cannot complete a TLS handshake: %v", err)
			}
		})
	}
}

// A node stored before the fix (legacy fields, never re-saved) must still
// produce a handshaking document from Build alone.
func TestHistoricalLegacyTLSRowHandshakesAtBuild(t *testing.T) {
	core := tlsFixtureCore(t)
	_, _, certPath, keyPath := writePair(t)
	node := tlsFixtureNode(t, `{"serverName":"`+tlsFixtureSNI+`","certificateFile":`+jsonStringUTF8(certPath)+`,"keyFile":`+jsonStringUTF8(keyPath)+`}`)
	if err := runHandshake(t, core, node); err != nil {
		t.Fatalf("historical legacy TLS row cannot complete a TLS handshake after Build normalization: %v", err)
	}
}

// Core-usage and half-mixing acceptance is decided by the real core, not by
// reading its source: an independent probe (see the report) showed that
//   - usage=verify CA entries legitimately carry no private key,
//   - usage=issue CA entries sign on demand and therefore need one,
//   - a file+inline mix *across* the certificate and key halves is loaded
//     exactly as written, while a mix *within* one half silently prefers the
//     file.
//
// This test proves the two accepted shapes actually handshake and that a
// verify-only document fails exactly like the core reports it.
func TestReviewTLSVerifyAndMixedHalvesHandshake(t *testing.T) {
	core := tlsFixtureCore(t)
	certPEM, keyPEM, certPath, keyPath := writePair(t)
	caPEM, caKeyPEM := writeCA(t, "ca."+tlsFixtureSNI)

	serverPair := `{"certificate":[` + jsonStringUTF8(certPEM) + `],"key":[` + jsonStringUTF8(keyPEM) + `]}`
	verifyCA := `{"certificate":[` + jsonStringUTF8(caPEM) + `],"usage":"verify"}`

	t.Run("verify CA without key is accepted and the server still serves TLS", func(t *testing.T) {
		node := tlsFixtureNode(t, `{"serverName":"`+tlsFixtureSNI+`","certificates":[`+serverPair+`,`+verifyCA+`]}`)
		node = sanitizedValidated(t, node)
		if err := runHandshake(t, core, node); err != nil {
			t.Fatalf("a legitimate verify CA entry must not break the node: %v", err)
		}
	})

	t.Run("issue CA with its key signs on demand", func(t *testing.T) {
		issueCA := `{"certificate":[` + jsonStringUTF8(caPEM) + `],"key":[` + jsonStringUTF8(caKeyPEM) + `],"usage":"issue"}`
		node := tlsFixtureNode(t, `{"serverName":"`+tlsFixtureSNI+`","certificates":[`+serverPair+`,`+issueCA+`]}`)
		node = sanitizedValidated(t, node)
		if err := runHandshake(t, core, node); err != nil {
			t.Fatalf("usage=issue with a key must work: %v", err)
		}
	})

	t.Run("mixed halves (file cert + inline key) load exactly as written", func(t *testing.T) {
		settings := `{"serverName":"` + tlsFixtureSNI + `","certificates":[{"certificateFile":` +
			jsonStringUTF8(certPath) + `,"key":[` + jsonStringUTF8(keyPEM) + `]}]}`
		node := tlsFixtureNode(t, settings)
		node = sanitizedValidated(t, node)
		if err := runHandshake(t, core, node); err != nil {
			t.Fatalf("file cert + inline key is a valid shape the core loads: %v", err)
		}
	})

	t.Run("mixed halves (inline cert + file key) load exactly as written", func(t *testing.T) {
		settings := `{"serverName":"` + tlsFixtureSNI + `","certificates":[{"certificate":[` +
			jsonStringUTF8(certPEM) + `],"keyFile":` + jsonStringUTF8(keyPath) + `}]}`
		node := tlsFixtureNode(t, settings)
		node = sanitizedValidated(t, node)
		if err := runHandshake(t, core, node); err != nil {
			t.Fatalf("inline cert + file key is a valid shape the core loads: %v", err)
		}
	})
}

// A same-half file+inline mix is rejected because the core silently prefers the
// file: probe 13/24 showed that with a valid file the inline certificate is
// ignored, and with a missing file the whole config is refused. Either way the
// administrator cannot tell which certificate is in use, so Validate refuses it
// instead of guessing. This test pins the underlying core behaviour that
// motivates the refusal.
func TestReviewTLSSameHalfMixCorePrefersFile(t *testing.T) {
	core := tlsFixtureCore(t)
	certAPEM, keyAPEM, certAPath, keyAPath := writePair(t)
	second, _ := writePairFor(t, "second."+tlsFixtureSNI)

	// The core loads the file, never the inline certificate.
	node := tlsFixtureNode(t, `{"serverName":"`+tlsFixtureSNI+`","certificates":[{"certificateFile":`+
		jsonStringUTF8(certAPath)+`,"keyFile":`+jsonStringUTF8(keyAPath)+`,"certificate":[`+jsonStringUTF8(second)+`]}]}`)
	if err := runHandshake(t, core, node); err != nil {
		t.Fatalf("core no longer prefers the file for a same-half mix; the validator's rationale must be revisited: %v", err)
	}

	// And when the file does not exist the inline value is still not used.
	missing := tlsFixtureNode(t, `{"serverName":"`+tlsFixtureSNI+`","certificates":[{"certificateFile":"/nonexistent/fixture.pem","keyFile":`+
		jsonStringUTF8(keyAPath)+`,"certificate":[`+jsonStringUTF8(certAPEM)+`]}]}`)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	missing.Port = port
	doc, err := xrayconf.Build(missing, []xrayconf.Client{{Email: "fixture", UUID: "11111111-2222-4333-8444-555555555555"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(core, "run", "-test", "-c", path).CombinedOutput(); err == nil {
		t.Fatalf("the core accepted a same-half mix whose file is missing (it must not fall back to inline): %s", out)
	}
	_ = keyAPEM
}

// writePairFor generates a second, independent certificate for the given name.
func writePairFor(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
}

// writeCA creates a self-signed CA certificate (and key) for the usage=verify /
// usage=issue cases.
func writeCA(t *testing.T, name string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
}

// sanitizedValidated runs the real save path and fails on any issue.
func sanitizedValidated(t *testing.T, node domain.Node) domain.Node {
	t.Helper()
	stream, ps, key, issues := protocol.Sanitize(protocol.Node{
		Protocol: node.Protocol, Host: node.Host, Port: node.Port, Listen: node.Listen,
		StreamSettings: node.StreamSettings, ProtocolSettings: node.ProtocolSettings,
		ClientSettings: node.ClientSettings,
	})
	if len(issues) > 0 {
		t.Fatalf("sanitize: %v", issues)
	}
	node.StreamSettings, node.ProtocolSettings, node.ServerKey = stream, ps, key
	if issues := protocol.Validate(protocol.Node{
		Protocol: node.Protocol, Host: node.Host, Port: node.Port, Listen: node.Listen,
		StreamSettings: node.StreamSettings, ProtocolSettings: node.ProtocolSettings,
		ClientSettings: node.ClientSettings,
	}); len(issues) > 0 {
		t.Fatalf("validate: %v", issues)
	}
	return node
}

// The exporter must not lose the gRPC client projection: xray JSON carries the
// service name; formats that cannot must reject (see review_regression_test.go).
func TestXrayGRPCClientCarriesServiceName(t *testing.T) {
	var stream domain.Object
	if err := json.Unmarshal([]byte(`{"network":"grpc","grpcSettings":{"serviceName":"fixture-service"}}`), &stream); err != nil {
		t.Fatal(err)
	}
	node := domain.Node{
		ID: "nd_grpc", Name: "grpc", Protocol: protocol.VLESS, Host: "grpc.example.invalid",
		Port: 443, Listen: "127.0.0.1", StreamSettings: stream,
		ProtocolSettings: domain.NewObject(), ClientSettings: domain.NewObject(),
	}
	ep, issues := protocol.EndpointFor(node)
	if len(issues) > 0 {
		t.Fatalf("endpoint issues: %v", issues)
	}
	rendered, err := clientconfig.Render(clientconfig.Request{Format: clientconfig.FormatXray,
		Nodes: []clientconfig.NodeConfig{{Endpoint: ep, Credential: "11111111-2222-4333-8444-555555555555"}}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var doc struct {
		Outbounds []struct {
			StreamSettings struct {
				GRPCSettings struct {
					ServiceName string `json:"serviceName"`
				} `json:"grpcSettings"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(rendered.Body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Outbounds[0].StreamSettings.GRPCSettings.ServiceName != "fixture-service" {
		t.Fatal("xray client export lost the gRPC service name")
	}
}

// jsonStringUTF8 quotes text for embedding in a JSON document built in Go.
func jsonStringUTF8(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
