// Package ids generates server side opaque identifiers. Clients never choose an
// id: every entity id is random, prefixed and URL safe.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

const entropyBytes = 16

// New returns prefix_<26 random chars>.
func New(prefix string) string {
	buf := make([]byte, entropyBytes)
	if _, err := rand.Read(buf); err != nil {
		panic("ids: crypto/rand unavailable: " + err.Error())
	}
	return prefix + "_" + enc.EncodeToString(buf)
}

// Prefixes used across the panel. They are cosmetic but stable.
const (
	Server          = "srv"
	Member          = "mbr"
	Node            = "nd"
	NodeGroup       = "ng"
	Subscription    = "sub"
	Template        = "tpl"
	Agent           = "agt"
	CoreInstance    = "ci"
	Credential      = "crd"
	SubscriptionTok = "stk"
	Session         = "ses"
	Invitation      = "inv"
	Registration    = "rc"
	QuotaAdjustment = "qadj"
	Preview         = "prv"
	Batch           = "bat"
	Task            = "tsk"
	Event           = "evt"
	Audit           = "aud"
	Receipt         = "rcp"
	UsageBatch      = "ubt"
)

// NewUUID returns a random RFC 4122 version 4 UUID, as required by Xray for
// VLESS/VMess user ids.
func NewUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("ids: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	var sb strings.Builder
	sb.Grow(36)
	for i, x := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			sb.WriteByte('-')
		}
		const hex = "0123456789abcdef"
		sb.WriteByte(hex[x>>4])
		sb.WriteByte(hex[x&0x0f])
	}
	return sb.String()
}
