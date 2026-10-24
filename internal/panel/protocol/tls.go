package protocol

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"

	"proxypanel/internal/panel/domain"
)

// The frozen Xray core (26.3.27) only reads certificate material from
// tlsSettings.certificates[]. Older panel versions accepted
// certificateFile/keyFile directly on tlsSettings, which the core silently
// ignores (frontend-protocol-review.md shows a config that passes
// `xray run -test` but fails every real TLS handshake). NormalizeTLS migrates
// those legacy fields before validation, persistence and config generation.
//
// Facts verified against the pinned core with an independent probe
// (temporary dirs + loopback ports, see tls_handshake_test.go):
//
//   - TLSCertConfig.Build uses `readFileOrString`, which returns the FILE when a
//     file is set and otherwise joins the inline lines. So when one half carries
//     both forms, the inline value is silently ignored by the core.
//   - BuildCertificates only uses entries whose usage is ENCIPHERMENT (or an
//     unrecognised value, which defaults to ENCIPHERMENT) as server
//     certificates; AUTHORITY_VERIFY entries only extend the self-signed root
//     pool and need no private key; AUTHORITY_ISSUE entries sign on demand and
//     do need a key.
//   - A configured entry whose key does not match its certificate is dropped by
//     the core: config-test still passes, the handshake then fails with
//     "tls: unrecognized name".
//   - `certificate` / `key` must be JSON arrays; a bare string is rejected by
//     the core config decoder.
//
// The panel never opens a file: it does not run on the Agent's host, so remote
// path existence stays the Agent's job. Only shape, placement, usage and inline
// material are checked here.

// tlsCertEntry is one certificates[] item in either the inline or file form.
type tlsCertEntry struct {
	Native      map[string]json.RawMessage `json:"-"`
	Certificate json.RawMessage            `json:"certificate"`
	Key         json.RawMessage            `json:"key"`
	CertFile    string                     `json:"certificateFile"`
	KeyFile     string                     `json:"keyFile"`
	Usage       string                     `json:"usage"`
}

func (e *tlsCertEntry) UnmarshalJSON(data []byte) error {
	type plain tlsCertEntry
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &decoded.Native); err != nil {
		return err
	}
	if decoded.Native == nil {
		return fmt.Errorf("certificate entry must be an object")
	}
	*e = tlsCertEntry(decoded)
	return nil
}

// MarshalJSON keeps the persisted entry clean: absent halves are omitted
// instead of being written as JSON null (a null would be indistinguishable from
// an empty inline value after a second normalization pass).
func (e tlsCertEntry) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for k, raw := range e.Native {
		out[k] = raw
	}
	if len(e.Certificate) > 0 && !isJSONNull(e.Certificate) {
		out["certificate"] = e.Certificate
	}
	if len(e.Key) > 0 && !isJSONNull(e.Key) {
		out["key"] = e.Key
	}
	if strings.TrimSpace(e.CertFile) != "" {
		out["certificateFile"] = e.CertFile
	}
	if strings.TrimSpace(e.KeyFile) != "" {
		out["keyFile"] = e.KeyFile
	}
	if strings.TrimSpace(e.Usage) != "" {
		out["usage"] = e.Usage
	}
	return json.Marshal(out)
}

// Certificate usage values understood by the core (lowercase, as the core does
// `strings.ToLower`). Anything else means encipherment.
const (
	usageEncipherment = "encipherment"
	usageVerify       = "verify"
	usageIssue        = "issue"
)

// certUsage mirrors the core: only "verify" and "issue" are special, everything
// else (including an unknown value) is the server-certificate usage.
func certUsage(raw string) string {
	switch strings.ToLower(raw) {
	case usageVerify:
		return usageVerify
	case usageIssue:
		return usageIssue
	default:
		return usageEncipherment
	}
}

func trimField(o domain.Object, key string) string {
	v, _ := o.String(key)
	return strings.TrimSpace(v)
}

func parseCertEntries(ts domain.Object) ([]tlsCertEntry, bool) {
	raw, ok := ts.Get("certificates")
	if !ok {
		return nil, true
	}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, true
	}
	var entries []tlsCertEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false
	}
	return entries, true
}

// normalizeCertArrayForm rewrites the single-string form of certificate/key
// into the one-element array the core decoder requires. It is lossless: the
// same PEM text ends up as the array element.
func normalizeCertArrayForm(entries []tlsCertEntry) ([]tlsCertEntry, bool) {
	changed := false
	for i := range entries {
		for _, field := range []*json.RawMessage{&entries[i].Certificate, &entries[i].Key} {
			if len(*field) == 0 || isJSONNull(*field) {
				continue
			}
			var single string
			if err := json.Unmarshal(*field, &single); err != nil {
				continue // already an array, or a value Validate will reject
			}
			raw, err := json.Marshal([]string{single})
			if err != nil {
				continue
			}
			*field = raw
			changed = true
		}
	}
	return entries, changed
}

// NormalizeTLS makes tlsSettings loadable by the frozen core without silently
// dropping administrator intent:
//
//   - single-string certificate/key → one-element arrays (lossless, the core
//     rejects the string form outright);
//   - legacy top-level certificateFile/keyFile → certificates[] item, but only
//     when no array entry already carries the same pair, and never when the
//     array would make one of the two sources unobservable.
//
// It never opens a file.
func NormalizeTLS(stream domain.Object) []Issue {
	if stream == nil {
		return nil
	}
	ts, ok := stream.Sub("tlsSettings")
	if !ok {
		return nil
	}
	add := func(msg string) []Issue {
		return []Issue{{Field: "stream_settings.tlsSettings.certificates", Message: msg}}
	}

	entries, parsed := parseCertEntries(ts)
	if !parsed {
		return add("certificates 不是合法的证书数组（必须是 JSON 对象数组）")
	}
	entries, changed := normalizeCertArrayForm(entries)
	if changed {
		if err := ts.Set("certificates", entries); err != nil {
			return add(err.Error())
		}
	}

	legacyCert := trimField(ts, "certificateFile")
	legacyKey := trimField(ts, "keyFile")
	if legacyCert == "" && legacyKey == "" {
		if changed {
			_ = stream.Set("tlsSettings", ts)
		}
		return nil
	}
	if legacyCert == "" || legacyKey == "" {
		return add("certificateFile 与 keyFile 必须成对出现（旧版顶层写法）")
	}

	if len(entries) > 0 {
		// The legacy pair and certificates[] coexist. Only an exact duplicate of
		// the first entry's file pair can be merged away: anything else means one
		// of the two sources would become unobservable, so it is an explicit
		// conflict instead of a silent choice.
		first := entries[0]
		firstCert := strings.TrimSpace(first.CertFile)
		firstKey := strings.TrimSpace(first.KeyFile)
		if firstCert == "" || firstKey == "" || firstCert != legacyCert || firstKey != legacyKey {
			return add("tlsSettings 顶层 certificateFile/keyFile 与 certificates[] 同时存在但不是同一对文件；请只保留一种写法（顶层写法已不被核心读取）")
		}
		delete(ts, "certificateFile")
		delete(ts, "keyFile")
		_ = stream.Set("tlsSettings", ts)
		return nil
	}

	entries = []tlsCertEntry{{CertFile: legacyCert, KeyFile: legacyKey, Usage: usageEncipherment}}
	if err := ts.Set("certificates", entries); err != nil {
		return add(err.Error())
	}
	delete(ts, "certificateFile")
	delete(ts, "keyFile")
	if err := stream.Set("tlsSettings", ts); err != nil {
		return add(err.Error())
	}
	return nil
}

// certEntryMaterial reports which halves of an entry are present, so the
// validators can mirror what the core actually loads.
type certEntryMaterial struct {
	hasInlineCert bool
	hasInlineKey  bool
	hasFileCert   bool
	hasFileKey    bool
}

// inlineText joins the chunks of an inline field. An empty array (or an array of
// blank strings) is "absent", exactly like the core's readFileOrString, which
// falls back to the file when the joined string is empty.
func inlineText(raw json.RawMessage) string {
	if len(raw) == 0 || isJSONNull(raw) {
		return ""
	}
	return strings.TrimSpace(strings.Join(certificateStrings(raw), "\n"))
}

func materialOf(entry tlsCertEntry) certEntryMaterial {
	return certEntryMaterial{
		hasInlineCert: inlineText(entry.Certificate) != "",
		hasInlineKey:  inlineText(entry.Key) != "",
		hasFileCert:   strings.TrimSpace(entry.CertFile) != "",
		hasFileKey:    strings.TrimSpace(entry.KeyFile) != "",
	}
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// tlsMaterialIssues validates every certificates[] entry with the core's own
// usage semantics, so a legitimate CA entry is not mistaken for an unusable
// server certificate and an ambiguous entry is not silently resolved.
func tlsMaterialIssues(stream domain.Object) []Issue {
	ts, ok := stream.Sub("tlsSettings")
	if !ok {
		return []Issue{{Field: "stream_settings.tlsSettings", Message: "TLS requires tlsSettings"}}
	}
	entries, parsed := parseCertEntries(ts)
	if !parsed {
		return []Issue{{Field: "stream_settings.tlsSettings.certificates", Message: "certificates 不是合法的证书数组（必须是 JSON 对象数组）"}}
	}
	if len(entries) == 0 {
		return []Issue{{Field: "stream_settings.tlsSettings.certificates", Message: "TLS 入站必须配置证书：内联 certificates，或同时填 certificateFile 与 keyFile"}}
	}

	var issues []Issue
	serving := 0
	for i, entry := range entries {
		field := fmt.Sprintf("stream_settings.tlsSettings.certificates[%d]", i)
		usage := certUsage(entry.Usage)
		for name, raw := range map[string]json.RawMessage{"certificate": entry.Certificate, "key": entry.Key} {
			if len(raw) == 0 || isJSONNull(raw) {
				continue
			}
			var lines []string
			if err := json.Unmarshal(raw, &lines); err != nil {
				issues = append(issues, Issue{Field: field + "." + name, Message: "must be a string array"})
			}
		}
		m := materialOf(entry)
		if m.hasInlineCert {
			if _, err := parseCertificatePEM(inlineText(entry.Certificate)); err != nil {
				issues = append(issues, Issue{Field: field + ".certificate", Message: err.Error()})
			}
		}

		// The core reads the file first and ignores the inline value, so an entry
		// carrying both for the same half would lose the inline material without
		// any warning. Rejecting keeps the administrator in control.
		if m.hasInlineCert && m.hasFileCert {
			issues = append(issues, Issue{Field: field, Message: "同一证书项的 certificate 与 certificateFile 同时存在：核心只读取文件、会静默忽略内联证书，请只保留一种"})
			continue
		}
		if m.hasInlineKey && m.hasFileKey {
			issues = append(issues, Issue{Field: field, Message: "同一证书项的 key 与 keyFile 同时存在：核心只读取文件、会静默忽略内联私钥，请只保留一种"})
			continue
		}
		if !m.hasInlineCert && !m.hasFileCert {
			issues = append(issues, Issue{Field: field, Message: "证书项必须提供 certificate 或 certificateFile"})
			continue
		}

		switch usage {
		case usageVerify:
			// 自签根池项：核心只把它加入 root pool，不要求私钥。
			// The core never touches this key for a verify entry (it only appends the
			// certificate to the root pool), so an unrelated or unparseable key is not
			// a functional problem - probe 4/15 handshakes fine. It is left to the
			// administrator; rejecting it would be an over-restriction.
		case usageIssue:
			serving++
			// 按需签发：核心用该 CA 的私钥签新证书，缺 key 会在握手时报 internal error。
			if !m.hasInlineKey && !m.hasFileKey {
				issues = append(issues, Issue{Field: field, Message: "usage=issue 必须提供私钥（certificate+key 或 certificateFile+keyFile）；核心用它按需签发"})
				continue
			}
			if m.hasInlineCert && m.hasInlineKey {
				if err := validateInlinePair(entry.Certificate, entry.Key); err != nil {
					issues = append(issues, Issue{Field: field, Message: err.Error()})
				}
			}
		default:
			// 服务器证书（含核心默认的未知 usage）：必须能形成可用密钥对。
			serving++
			if !m.hasInlineKey && !m.hasFileKey {
				issues = append(issues, Issue{Field: field, Message: "服务器证书项必须同时提供 key 或 keyFile；核心会丢弃没有私钥的证书"})
				continue
			}
			if m.hasInlineCert && m.hasInlineKey {
				if err := validateInlinePair(entry.Certificate, entry.Key); err != nil {
					issues = append(issues, Issue{Field: field, Message: err.Error()})
				}
			}
		}
	}
	if serving == 0 && len(issues) == 0 {
		issues = append(issues, Issue{Field: "stream_settings.tlsSettings.certificates",
			Message: "TLS 入站至少需要一个服务器证书或签发 CA（usage 为空、encipherment 或 issue）；只有 verify CA 项时核心不会提供证书"})
	}
	return issues
}

// validateInlinePair decodes the inline material exactly as the core would and
// proves the private key belongs to the leaf (first) certificate.
func validateInlinePair(certRaw, keyRaw json.RawMessage) error {
	certs, err := parseCertificatePEM(strings.Join(certificateStrings(certRaw), "\n"))
	if err != nil {
		return err
	}
	signer, err := parsePrivateKeyPEM(strings.Join(certificateStrings(keyRaw), "\n"))
	if err != nil {
		return err
	}
	certPub, err := x509.MarshalPKIXPublicKey(certs[0].PublicKey)
	if err != nil {
		return fmt.Errorf("证书公钥无法解析: %w", err)
	}
	keyPub, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return fmt.Errorf("私钥公钥无法解析: %w", err)
	}
	if !bytes.Equal(certPub, keyPub) {
		return fmt.Errorf("证书与私钥不匹配（不是同一密钥对）")
	}
	return nil
}

func parseCertificatePEM(text string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(text)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("证书 PEM 无法解析: %w", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("证书 PEM 不完整或为空（缺少可解析的 CERTIFICATE 块）")
	}
	return certs, nil
}

func parsePrivateKeyPEM(text string) (crypto.Signer, error) {
	rest := []byte(text)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "PRIVATE KEY":
			key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("私钥 PEM 无法解析: %w", err)
			}
			return asSigner(key)
		case "RSA PRIVATE KEY":
			key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("私钥 PEM 无法解析: %w", err)
			}
			return key, nil
		case "EC PRIVATE KEY":
			key, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("私钥 PEM 无法解析: %w", err)
			}
			return key, nil
		}
	}
	return nil, fmt.Errorf("私钥 PEM 不完整或为空（缺少可解析的 PRIVATE KEY 块）")
}

func asSigner(key any) (crypto.Signer, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return k, nil
	case *ecdsa.PrivateKey:
		return k, nil
	case ed25519.PrivateKey:
		return k, nil
	default:
		return nil, fmt.Errorf("不支持的私钥类型 %T", key)
	}
}
