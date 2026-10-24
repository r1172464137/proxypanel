package protocol

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
)

func validateX25519Pair(private, public string) error {
	priv, err := base64.RawURLEncoding.DecodeString(private)
	if err != nil {
		return fmt.Errorf("REALITY private key must be a base64url X25519 key")
	}
	key, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("REALITY private key must contain 32 bytes")
	}
	pub, err := base64.RawURLEncoding.DecodeString(public)
	if err != nil || len(pub) != 32 {
		return fmt.Errorf("REALITY public key must be a base64url X25519 key of 32 bytes")
	}
	if !bytes.Equal(key.PublicKey().Bytes(), pub) {
		return fmt.Errorf("REALITY public key does not match the server private key")
	}
	return nil
}
