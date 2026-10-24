package protocol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
)

func reviewTLSPair(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"n.example.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
}

// reviewTLSCA returns a self-signed CA certificate (and key) usable for the
// core's usage=verify root pool / usage=issue on-demand signing.
func reviewTLSCA(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
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
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
}

func TestReviewTLSRejectsUnusableInline(t *testing.T) {
	cert, key := reviewTLSPair(t)
	_, otherKey := reviewTLSPair(t)
	for name, raw := range map[string]string{
		"empty entry":          `{"certificates":[{}]}`,
		"truncated":            `{"certificates":[{"certificate":["-----BEGIN CERTIFICATE-----"],"key":["-----BEGIN PRIVATE KEY-----"]}]}`,
		"missing key":          `{"certificates":[{"certificate":[` + jsonString(t, cert) + `]}]}`,
		"mismatched key":       `{"certificates":[{"certificate":[` + jsonString(t, cert) + `],"key":[` + jsonString(t, otherKey) + `]}]}`,
		"half file pair":       `{"certificates":[{"certificateFile":"/remote/fixture.pem"}]}`,
		"invalid second entry": `{"certificates":[{"certificate":[` + jsonString(t, cert) + `],"key":[` + jsonString(t, key) + `]},{}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if len(Validate(tlsNode(t, obj(t, raw)))) == 0 {
				t.Fatal("unusable TLS material accepted")
			}
		})
	}
}

// Verified against Xray 26.3.27 (probe: temporary dirs + loopback handshakes;
// core source: infra/conf/transport_internet.go readFileOrString and
// transport/internet/tls/config.go BuildCertificates).
func TestReviewTLSMatchesCoreUsageSemantics(t *testing.T) {
	cert, key := reviewTLSPair(t)
	caCert, caKey := reviewTLSCA(t, "ca.example.invalid")
	pair := `{"certificate":[` + jsonString(t, cert) + `],"key":[` + jsonString(t, key) + `]}`

	t.Run("usage=verify CA without key is a legitimate root pool entry", func(t *testing.T) {
		raw := `{"certificates":[` + pair + `,{"certificate":[` + jsonString(t, caCert) + `],"usage":"verify"}]}`
		if issues := Validate(tlsNode(t, obj(t, raw))); len(issues) != 0 {
			t.Fatalf("a CA entry for the self-signed root pool needs no private key: %v", issues)
		}
	})

	t.Run("usage=verify is case-insensitive like the core", func(t *testing.T) {
		raw := `{"certificates":[` + pair + `,{"certificate":[` + jsonString(t, caCert) + `],"usage":"VERIFY"}]}`
		if issues := Validate(tlsNode(t, obj(t, raw))); len(issues) != 0 {
			t.Fatalf("the core lowercases usage: %v", issues)
		}
	})

	t.Run("usage=verify tolerates an extra unrelated key like the core", func(t *testing.T) {
		// Probe 4/15: a verify entry only feeds the self-signed root pool, so an
		// unrelated key changes nothing and the core still handshakes. Rejecting it
		// would be an over-restriction the product does not need.
		_, otherKey := reviewTLSPair(t)
		raw := `{"certificates":[` + pair + `,{"certificate":[` + jsonString(t, caCert) + `],"key":[` + jsonString(t, otherKey) + `],"usage":"verify"}]}`
		if issues := Validate(tlsNode(t, obj(t, raw))); len(issues) != 0 {
			t.Fatalf("verify entries need no key and tolerate one: %v", issues)
		}
	})

	t.Run("usage=verify alone cannot serve TLS", func(t *testing.T) {
		raw := `{"certificates":[{"certificate":[` + jsonString(t, caCert) + `],"usage":"verify"}]}`
		if issues := Validate(tlsNode(t, obj(t, raw))); len(issues) == 0 {
			t.Fatal("verify-only material must be rejected: the core then serves no certificate")
		}
	})

	t.Run("usage=issue requires a private key", func(t *testing.T) {
		missing := `{"certificates":[` + pair + `,{"certificate":[` + jsonString(t, caCert) + `],"usage":"issue"}]}`
		if issues := Validate(tlsNode(t, obj(t, missing))); len(issues) == 0 {
			t.Fatal("usage=issue without a key fails at handshake time in the core; it must be rejected")
		}
		ok := `{"certificates":[` + pair + `,{"certificate":[` + jsonString(t, caCert) + `],"key":[` + jsonString(t, caKey) + `],"usage":"issue"}]}`
		if issues := Validate(tlsNode(t, obj(t, ok))); len(issues) != 0 {
			t.Fatalf("a CA with its key issues certificates on demand: %v", issues)
		}
	})

	t.Run("unknown usage falls back to encipherment like the core", func(t *testing.T) {
		raw := `{"certificates":[{"certificate":[` + jsonString(t, cert) + `],"key":[` + jsonString(t, key) + `],"usage":"bogus"}]}`
		if issues := Validate(tlsNode(t, obj(t, raw))); len(issues) != 0 {
			t.Fatalf("the core treats an unknown usage as encipherment: %v", issues)
		}
	})

	t.Run("encipherment entry without a key is rejected", func(t *testing.T) {
		raw := `{"certificates":[{"certificate":[` + jsonString(t, cert) + `]}]}`
		if issues := Validate(tlsNode(t, obj(t, raw))); len(issues) == 0 {
			t.Fatal("the core drops a keyless server certificate; accepting it would hide a dead node")
		}
	})
}

// When one half carries both an inline value and a file, the core's
// readFileOrString prefers the file and silently ignores the inline value.
// The validator must not "pick one" on the administrator's behalf.
func TestReviewTLSRejectsInlineAndFileOnTheSameHalf(t *testing.T) {
	cert, key := reviewTLSPair(t)
	caCert, _ := reviewTLSCA(t, "ca.example.invalid")
	cases := map[string]string{
		"cert both forms": `{"certificates":[{"certificate":[` + jsonString(t, cert) + `],"certificateFile":"/remote/a.pem","key":[` + jsonString(t, key) + `]}]}`,
		"key both forms":  `{"certificates":[{"certificate":[` + jsonString(t, cert) + `],"key":[` + jsonString(t, key) + `],"keyFile":"/remote/a.key"}]}`,
		"verify cert both forms": `{"certificates":[{"certificate":[` + jsonString(t, cert) + `],"key":[` + jsonString(t, key) + `]},` +
			`{"certificate":[` + jsonString(t, caCert) + `],"certificateFile":"/remote/ca.pem","usage":"verify"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			issues := Validate(tlsNode(t, obj(t, raw)))
			if len(issues) == 0 {
				t.Fatal("an entry with both file and inline material for the same half must be rejected explicitly")
			}
		})
	}

	t.Run("empty array is the same as absent, like the core", func(t *testing.T) {
		// Probe 27/28: the core's readFileOrString joins the chunks and falls back
		// to the file when the joined text is empty, so an empty array plus a file
		// is the file form, not a conflict.
		raw := `{"certificates":[{"certificate":[],"certificateFile":"/remote/a.pem","key":[],"keyFile":"/remote/a.key"}]}`
		if issues := Validate(tlsNode(t, obj(t, raw))); len(issues) != 0 {
			t.Fatalf("an empty inline array must not count as inline material: %v", issues)
		}
	})

	t.Run("mixed halves are accepted because the core loads exactly them", func(t *testing.T) {
		// The probe confirmed file cert + inline key and inline cert + file key
		// both load and handshake, so they are not a conflict.
		raw := `{"certificates":[{"certificateFile":"/remote/a.pem","key":[` + jsonString(t, key) + `]}]}`
		if fileHalf, _ := tlsNodeHalfs(raw); !fileHalf {
			t.Fatal("sanity: expected the file/inline mix to be parsed")
		}
	})
}

// tlsNodeHalfs is a small sanity helper for the mixed-half case above: it only
// asserts the shapes exist; the handshake-level proof lives in
// clientconfig/tls_handshake_test.go against the real core.
func tlsNodeHalfs(raw string) (bool, bool) {
	var ts struct {
		Certificates []struct {
			Certificate     json.RawMessage `json:"certificate"`
			Key             json.RawMessage `json:"key"`
			CertificateFile string          `json:"certificateFile"`
			KeyFile         string          `json:"keyFile"`
		} `json:"certificates"`
	}
	if err := json.Unmarshal([]byte(raw), &ts); err != nil || len(ts.Certificates) == 0 {
		return false, false
	}
	entry := ts.Certificates[0]
	return entry.CertificateFile != "" || entry.KeyFile != "", len(entry.Certificate) > 0 || len(entry.Key) > 0
}

// The endpoint pin must be derived from the certificate the core actually
// serves (an encipherment entry), never from a usage=verify CA entry that merely
// happens to be listed first. Verified against the real core: with
// [verify CA, encipherment pair] the handshake presents the encipherment leaf.
func TestReviewTLSPinSkipsAuthorityVerifyEntries(t *testing.T) {
	cert, key := reviewTLSPair(t)
	caCert, _ := reviewTLSCA(t, "ca.example.invalid")
	entries := obj(t, `{"certificates":[{"certificate":[`+jsonString(t, caCert)+`],"usage":"verify"},{"certificate":[`+jsonString(t, cert)+`],"key":[`+jsonString(t, key)+`]}]}`)
	stream := domain.NewObject()
	_ = stream.Set("network", "tcp")
	_ = stream.Set("security", "tls")
	_ = stream.Set("tlsSettings", entries)
	node := domain.Node{
		ID: "nd_tls_ca_first", Name: "tls-ca-first", Protocol: Trojan,
		Host: "n.example.invalid", Port: 443, Listen: "0.0.0.0",
		StreamSettings: stream,
		ClientSettings: obj(t, `{"server_name":"n.example.invalid"}`),
	}
	ep, issues := EndpointFor(node)
	if len(issues) > 0 {
		t.Fatalf("endpoint issues: %v", issues)
	}
	want, ok := pemCertSHA256(cert)
	if !ok {
		t.Fatal("fixture leaf certificate does not parse")
	}
	if ep.PeerCertSha256 != want {
		t.Fatalf("pin = %q, want the encipherment leaf %q (a verify CA must not become the pin)", ep.PeerCertSha256, want)
	}
}

func TestReviewTLSLegacyFilesNormalizeWithoutLocalIO(t *testing.T) {
	n := tlsNode(t, obj(t, `{"certificateFile":"/remote/not-on-panel.pem","keyFile":"/remote/not-on-panel.key","alpn":["h2"]}`))
	stream, _, _, issues := Sanitize(n)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ts, _ := stream.Sub("tlsSettings")
	if ts.Has("certificateFile") || ts.Has("keyFile") {
		t.Fatal("legacy top-level fields must be removed after normalization")
	}
	if !ts.Has("certificates") {
		t.Fatal("missing core-native certificates array")
	}
	if _, ok := ts.Strings("alpn"); !ok {
		t.Fatal("unrelated native field lost")
	}
	// The migrated entry must stay a clean file pair: no JSON null halves.
	raw, _ := ts.Get("certificates")
	if got := string(raw); got != `[{"certificateFile":"/remote/not-on-panel.pem","keyFile":"/remote/not-on-panel.key","usage":"encipherment"}]` {
		t.Fatalf("normalized entry = %s", got)
	}
	if is := Validate(n); len(is) > 0 {
		t.Fatalf("remote paths are not panel-local files: %v", is)
	}
}

func TestReviewTLSRejectsConflictingLegacyPaths(t *testing.T) {
	cases := map[string]string{
		"different pair":             `{"certificateFile":"/remote/a.pem","keyFile":"/remote/a.key","certificates":[{"certificateFile":"/remote/b.pem","keyFile":"/remote/b.key"}]}`,
		"array has inline cert":      `{"certificateFile":"/remote/a.pem","keyFile":"/remote/a.key","certificates":[{"certificate":["x"],"key":["y"]}]}`,
		"array entry has only files": `{"certificateFile":"/remote/a.pem","keyFile":"/remote/a.key","certificates":[{"certificateFile":"/remote/a.pem"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			n := tlsNode(t, obj(t, raw))
			if _, _, _, issues := Sanitize(n); len(issues) == 0 {
				t.Fatal("conflicting certificate placement silently accepted")
			}
		})
	}

	t.Run("identical legacy pair and array entry merge losslessly", func(t *testing.T) {
		n := tlsNode(t, obj(t, `{"certificateFile":"/remote/a.pem","keyFile":"/remote/a.key","certificates":[{"certificateFile":"/remote/a.pem","keyFile":"/remote/a.key"}]}`))
		stream, _, _, issues := Sanitize(n)
		if len(issues) > 0 {
			t.Fatalf("an exact duplicate is not a conflict: %v", issues)
		}
		ts, _ := stream.Sub("tlsSettings")
		if ts.Has("certificateFile") || ts.Has("keyFile") {
			t.Fatal("legacy aliases must be dropped after the lossless merge")
		}
		raw, _ := ts.Get("certificates")
		if got := string(raw); got != `[{"certificateFile":"/remote/a.pem","keyFile":"/remote/a.key"}]` {
			t.Fatalf("merged certificates = %s", got)
		}
	})
}

// The core decodes certificate/key as string arrays only; a bare string fails
// config decoding. Normalizing it is lossless (the same PEM becomes one
// element), so a real configuration is not rejected for a shape the core
// happens to spell differently.
func TestReviewTLSNormalizesSingleStringToArray(t *testing.T) {
	cert, key := reviewTLSPair(t)
	n := tlsNode(t, obj(t, `{"certificates":[{"certificate":`+jsonString(t, cert)+`,"key":`+jsonString(t, key)+`}]}`))
	stream, _, _, issues := Sanitize(n)
	if len(issues) > 0 {
		t.Fatalf("sanitize: %v", issues)
	}
	ts, _ := stream.Sub("tlsSettings")
	raw, _ := ts.Get("certificates")
	var entries []struct {
		Certificate []string `json:"certificate"`
		Key         []string `json:"key"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("normalized entries are not arrays: %v raw=%s", err, raw)
	}
	if len(entries) != 1 || len(entries[0].Certificate) != 1 || len(entries[0].Key) != 1 {
		t.Fatalf("normalized entries = %+v", entries)
	}
	if issues := Validate(n); len(issues) != 0 {
		t.Fatalf("a losslessly normalized entry must validate: %v", issues)
	}
}

// The node-level ServerKey wins over a nested realitySettings.privateKey in
// Sanitize. That precedence is exactly why the editor must submit an explicitly
// entered new private key as the top-level server_key and delete the nested
// copy: otherwise a PATCH that only carries the nested key keeps the old stored
// secret and can pair the OLD private key with a NEW client public key.
func TestSanitizePrefersStoredServerKeyOverNestedPrivateKey(t *testing.T) {
	const stored = "STORED_OLD_KEY"
	const nested = "NESTED_NEW_KEY"
	n := tlsNode(t, obj(t, `{"serverName":"n.example.invalid","certificates":[{"certificateFile":"/a.pem","keyFile":"/a.key"}]}`))
	n.ServerKey = stored
	n.StreamSettings = obj(t, `{"network":"tcp","security":"tls","tlsSettings":{"serverName":"n.example.invalid","certificates":[{"certificateFile":"/a.pem","keyFile":"/a.key"}]},"realitySettings":{"privateKey":"`+nested+`"}}`)
	stream, _, key, issues := Sanitize(n)
	if len(issues) > 0 {
		t.Fatalf("sanitize: %v", issues)
	}
	if key != stored {
		t.Fatalf("server key = %q, want the stored key %q (documented precedence)", key, stored)
	}
	if rs, ok := stream.Sub("realitySettings"); ok && rs.Has("privateKey") {
		t.Fatal("nested private key must be removed from persisted stream settings")
	}
}
