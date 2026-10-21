// Package secrets handles master key encryption of stored proxy credentials,
// subscription tokens and notification tokens, plus password hashing.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Key is a 32 byte AES-256-GCM master key.
type Key struct {
	aead cipher.AEAD
}

// ParseKey accepts base64 (std or raw url), hex or a passphrase of at least 32
// characters which is stretched with SHA-256. Empty input is an error: the
// panel never silently runs without encryption.
func ParseKey(raw string) (*Key, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("PANEL_MASTER_KEY is required (base64, hex or >=32 char passphrase)")
	}
	var material []byte
	switch {
	case len(raw) == 64 && isHex(raw):
		b, err := hex.DecodeString(raw)
		if err != nil {
			return nil, err
		}
		material = b
	default:
		if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) == 32 {
			material = b
			break
		}
		if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil && len(b) == 32 {
			material = b
			break
		}
		if len(raw) < 32 {
			return nil, errors.New("PANEL_MASTER_KEY must be 32 bytes (base64/hex) or at least 32 characters")
		}
		sum := sha256.Sum256([]byte(raw))
		material = sum[:]
	}
	if len(material) != 32 {
		return nil, fmt.Errorf("PANEL_MASTER_KEY must decode to 32 bytes, got %d", len(material))
	}
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Key{aead: aead}, nil
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// Encrypt returns nonce||ciphertext.
func (k *Key) Encrypt(plaintext []byte) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, errors.New("secrets: master key not initialised")
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// EncryptString is a convenience wrapper for text secrets.
func (k *Key) EncryptString(s string) ([]byte, error) { return k.Encrypt([]byte(s)) }

// DecryptString reverses EncryptString.
func (k *Key) DecryptString(ct []byte) (string, error) {
	b, err := k.Decrypt(ct)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (k *Key) Decrypt(ciphertext []byte) ([]byte, error) {
	if k == nil || k.aead == nil {
		return nil, errors.New("secrets: master key not initialised")
	}
	ns := k.aead.NonceSize()
	if len(ciphertext) < ns+1 {
		return nil, errors.New("secrets: ciphertext too short")
	}
	return k.aead.Open(nil, ciphertext[:ns], ciphertext[ns:], nil)
}

// NewToken returns a high entropy URL safe token.
func NewToken(nbytes int) string {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		panic("secrets: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is the lookup hash for bearer style tokens (session cookies,
// subscription tokens, agent tokens, invitation codes). Tokens are high
// entropy so a plain SHA-256 lookup hash is sufficient.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares two strings without leaking length-position.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// PasswordHashingCost is the bcrypt work factor used for stored hashes.
const PasswordHashingCost = 12

// HashPassword returns a bcrypt hash. argon2id is not used because
// golang.org/x/crypto/argon2 imports golang.org/x/sys, which is absent from the
// frozen root go.mod (reported to the owner instead of editing root deps).
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), PasswordHashingCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// ValidatePassword enforces the minimum password policy (10..512 characters).
func ValidatePassword(password string) error {
	if len(password) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	if len(password) > 512 {
		return errors.New("password must be at most 512 characters")
	}
	return nil
}

// VerifyPassword verifies a bcrypt hash. argon2id hashes are intentionally not
// produced: golang.org/x/crypto/argon2 requires golang.org/x/sys, which is not
// part of the frozen root go.mod (see backend implementation report).
func VerifyPassword(encoded, password string) (bool, error) {
	switch {
	case strings.HasPrefix(encoded, "$2a$"), strings.HasPrefix(encoded, "$2b$"), strings.HasPrefix(encoded, "$2y$"):
		if err := bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password)); err != nil {
			return false, nil
		}
		return true, nil
	default:
		return false, errors.New("secrets: unsupported password hash")
	}
}
