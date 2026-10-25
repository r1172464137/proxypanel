package protocol

import (
	"testing"

	"proxypanel/internal/panel/domain"
)

// The public key is a pure function of the private key: an admin supplying only
// the private key must never be required to paste a second value.
func TestPublicKeyForMatchesGeneratedPair(t *testing.T) {
	priv, pub, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	derived, err := PublicKeyFor(priv)
	if err != nil {
		t.Fatal(err)
	}
	if derived != pub {
		t.Fatalf("derived %q != generated %q", derived, pub)
	}
	// And the derived pair passes the existing pair validator.
	if err := validateX25519Pair(priv, derived); err != nil {
		t.Fatalf("derived pair rejected: %v", err)
	}
}

func TestPublicKeyForRejectsInvalidPrivateKey(t *testing.T) {
	for _, bad := range []string{"", "x", "not-base64!!", "x-QXafFLdN42wFnaPh-u3J1_ptOKk"} {
		if _, err := PublicKeyFor(bad); err == nil {
			t.Fatalf("accepted invalid private key %q", bad)
		}
	}
}

// A REALITY node that carries only a private key must end up with the matching
// public key in client_settings after Sanitize + the API-level ensure step.
func TestDerivedPublicKeySatisfiesRealityValidation(t *testing.T) {
	priv, pub, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	derived, err := PublicKeyFor(priv)
	if err != nil {
		t.Fatal(err)
	}
	if derived != pub {
		t.Fatalf("derivation mismatch")
	}
	stream := domain.NewObject()
	_ = stream.Set("network", "tcp")
	_ = stream.Set("security", "reality")
	rs := domain.NewObject()
	_ = rs.Set("dest", "www.microsoft.com:443")
	_ = rs.Set("serverNames", []string{"www.microsoft.com"})
	_ = rs.Set("shortIds", []string{"c804aea035911a9a"})
	_ = stream.Set("realitySettings", rs)
	client := domain.NewObject()
	_ = client.Set("server_name", "www.microsoft.com")
	_ = client.Set("public_key", derived)

	node := Node{Protocol: VLESS, Host: "example.test", Port: 443, Listen: "0.0.0.0",
		StreamSettings: stream, ProtocolSettings: domain.NewObject(), ClientSettings: client,
		ServerKey: priv}
	if issues := Validate(node); len(issues) != 0 {
		t.Fatalf("derived key rejected: %+v", issues)
	}
}
