package api

import (
	"strings"
	"testing"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
)

// A REALITY node created with only a private key must get the matching public
// key automatically, so the admin never pastes a second value that can drift.
func TestEnsureRealityKeysDerivesPublicFromSuppliedPrivate(t *testing.T) {
	priv, pub, err := protocol.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	stream := domain.NewObject()
	_ = stream.Set("security", "reality")
	n := &domain.Node{Protocol: protocol.VLESS, StreamSettings: stream,
		ClientSettings: domain.NewObject(), ServerKey: priv}
	if err := ensureRealityKeys(n); err != nil {
		t.Fatal(err)
	}
	got, ok := n.ClientSettings.String("public_key")
	if !ok || strings.TrimSpace(got) == "" {
		t.Fatal("public_key not derived")
	}
	if got != pub {
		t.Fatalf("derived public key %q does not match %q", got, pub)
	}
	if n.ServerKey != priv {
		t.Fatal("supplied private key was replaced instead of kept")
	}
}

// An explicitly provided public key must not be overwritten.
func TestEnsureRealityKeysKeepsExplicitPublicKey(t *testing.T) {
	priv, _, err := protocol.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	stream := domain.NewObject()
	_ = stream.Set("security", "reality")
	client := domain.NewObject()
	_ = client.Set("public_key", "explicit-value")
	n := &domain.Node{Protocol: protocol.VLESS, StreamSettings: stream,
		ClientSettings: client, ServerKey: priv}
	if err := ensureRealityKeys(n); err != nil {
		t.Fatal(err)
	}
	got, _ := n.ClientSettings.String("public_key")
	if got != "explicit-value" {
		t.Fatalf("explicit public key overwritten: %q", got)
	}
}

// A node with no private key still gets a full generated pair.
func TestEnsureRealityKeysGeneratesPairWhenMissing(t *testing.T) {
	stream := domain.NewObject()
	_ = stream.Set("security", "reality")
	n := &domain.Node{Protocol: protocol.VLESS, StreamSettings: stream, ClientSettings: domain.NewObject()}
	if err := ensureRealityKeys(n); err != nil {
		t.Fatal(err)
	}
	if n.ServerKey == "" {
		t.Fatal("private key not generated")
	}
	pub, ok := n.ClientSettings.String("public_key")
	if !ok || pub == "" {
		t.Fatal("public key not generated")
	}
	derived, err := protocol.PublicKeyFor(n.ServerKey)
	if err != nil || derived != pub {
		t.Fatalf("generated pair does not match: %v", err)
	}
}

// Non-REALITY nodes must be untouched.
func TestEnsureRealityKeysIgnoresNonReality(t *testing.T) {
	stream := domain.NewObject()
	_ = stream.Set("security", "tls")
	n := &domain.Node{Protocol: protocol.VLESS, StreamSettings: stream, ClientSettings: domain.NewObject()}
	if err := ensureRealityKeys(n); err != nil {
		t.Fatal(err)
	}
	if n.ServerKey != "" {
		t.Fatal("injected a key into a TLS node")
	}
}
