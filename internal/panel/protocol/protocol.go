// Package protocol owns the declarative node schema: which protocol/transport/
// security combinations the frozen Xray core supports, which nested fields are
// preserved, and where server-only secrets live. The panel never guesses client
// compatibility here; that is clientconfig's job.
package protocol

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"proxypanel/internal/panel/domain"
)

const (
	VLESS       = "vless"
	VMess       = "vmess"
	Trojan      = "trojan"
	Shadowsocks = "shadowsocks"
)

// Issue is a field level validation problem (dotted path, no secret values).
type Issue struct {
	Field   string
	Message string
}

func (i Issue) String() string { return i.Field + ": " + i.Message }

// Node is the writable node projection validated by the panel.
type Node struct {
	Protocol string
	Flow     string
	Cipher   string
	Listen   string
	Host     string
	Port     int
	// ExternalPort 是客户端连接的端口；0 表示与 Port 相同。
	ExternalPort     int
	StreamSettings   domain.Object
	ProtocolSettings domain.Object
	ClientSettings   domain.Object
	ServerKey        string
}

var supportedProtocols = map[string]bool{
	VLESS: true, VMess: true, Trojan: true, Shadowsocks: true,
}

// Networks accepted by the frozen core (Xray 26.3.27), verified against
// `xray run -test`: tcp/raw, kcp/mkcp, ws, grpc, httpupgrade, xhttp/splithttp.
// The aliases are normalised on output so admin input never silently degrades.
//
// This set must stay identical to allowedNetworks in
// internal/agent/xray/build.go: a value the panel accepts but the core rejects
// makes every later snapshot of every node fail until the node is archived.
var supportedNetworks = map[string]string{
	"": "tcp", "tcp": "tcp", "raw": "tcp", "ws": "ws", "grpc": "grpc",
	"httpupgrade": "httpupgrade", "xhttp": "xhttp", "splithttp": "xhttp",
	"kcp": "kcp", "mkcp": "kcp",
}

var supportedSecurities = map[string]bool{
	"": true, "none": true, "tls": true, "reality": true,
}

// VLESS flows accepted by the fixed core. Legacy xtls-rprx-direct style flows
// were removed upstream and are rejected instead of silently dropped.
var supportedFlows = map[string]bool{
	"": true, "xtls-rprx-vision": true,
}

var supportedSSMethods = map[string]bool{
	"2022-blake3-aes-128-gcm":       true,
	"2022-blake3-aes-256-gcm":       true,
	"2022-blake3-chacha20-poly1305": true,
	"aes-128-gcm":                   true,
	"aes-256-gcm":                   true,
	"chacha20-poly1305":             true,
	"chacha20-ietf-poly1305":        true,
	"xchacha20-poly1305":            true,
	"xchacha20-ietf-poly1305":       true,
	// "none" is accepted by the core only for a single user inbound. The panel
	// always injects a per-member clients array, where Xray 26.3.27 rejects it
	// with "unsupported cipher method: none", so it is not offered at all.
}

var supportedVMessCiphers = map[string]bool{
	"": true, "auto": true, "none": true, "zero": true,
	"aes-128-gcm": true, "chacha20-poly1305": true,
}

// streamSubObjects are validated as JSON objects when present.
var streamSubObjects = []string{
	"tlsSettings", "realitySettings", "wsSettings", "grpcSettings",
	"xhttpSettings", "splithttpSettings", "kcpSettings", "httpSettings",
	"quicSettings", "sockopt", "tcpSettings", "rawSettings",
}

// NormalizeNetwork maps an admin supplied network onto the canonical name.
func NormalizeNetwork(network string) (string, bool) {
	v, ok := supportedNetworks[strings.ToLower(strings.TrimSpace(network))]
	return v, ok
}

// SupportedNetworks returns the canonical transport names accepted by the panel,
// sorted. The agent's xray.allowedNetworks must contain exactly this set.
func SupportedNetworks() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(supportedNetworks))
	for _, canonical := range supportedNetworks {
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out
}

// SupportedSecurities returns the accepted security layers, sorted. The agent's
// xray.allowedSecurities must contain exactly this set.
func SupportedSecurities() []string {
	out := make([]string, 0, len(supportedSecurities))
	for name := range supportedSecurities {
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// streamTransportIssues whitelists the transport (network) and the security
// layer of a stream settings object. Both Validate and Sanitize use it, so the
// admin API rejects an unsupported value with the field that carries it instead
// of persisting a node the core can never load.
func streamTransportIssues(stream domain.Object) []Issue {
	if stream == nil {
		return nil
	}
	var issues []Issue
	networkRaw, _ := stream.String("network")
	if _, ok := NormalizeNetwork(networkRaw); !ok {
		issues = append(issues, Issue{
			Field:   "stream_settings.network",
			Message: fmt.Sprintf("unsupported transport %q (supported: tcp, raw, kcp, mkcp, ws, grpc, httpupgrade, xhttp, splithttp)", networkRaw),
		})
	}
	securityRaw, _ := stream.String("security")
	if security := strings.ToLower(strings.TrimSpace(securityRaw)); !supportedSecurities[security] {
		issues = append(issues, Issue{
			Field:   "stream_settings.security",
			Message: fmt.Sprintf("unsupported security %q (supported: none, tls, reality)", securityRaw),
		})
	}
	return issues
}

// Validate checks the node configuration and returns field level issues.
func Validate(n Node) []Issue {
	var issues []Issue
	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	proto := strings.ToLower(strings.TrimSpace(n.Protocol))
	if !supportedProtocols[proto] {
		add("protocol", "unsupported protocol %q (supported: vless, vmess, trojan, shadowsocks)", n.Protocol)
		return issues
	}
	if n.Port < 1 || n.Port > 65535 {
		add("port", "port must be between 1 and 65535")
	}
	// 对外端口用于「对外 443 → 实际监听 20101」：可选，填了就必须是合法端口，
	// 且不能与实际监听端口相同（相同就没有分离的意义，容易让运维误以为配错了）。
	if n.ExternalPort != 0 {
		if n.ExternalPort < 1 || n.ExternalPort > 65535 {
			add("external_port", "external_port must be between 1 and 65535")
		} else if n.ExternalPort == n.Port {
			add("external_port", "external_port 与实际监听端口相同，没有分离意义；不需要时请留空")
		}
	}
	if strings.TrimSpace(n.Host) == "" {
		add("host", "client facing host is required")
	}
	if strings.TrimSpace(n.Listen) == "" {
		add("listen", "listen address is required")
	}
	if n.StreamSettings == nil {
		n.StreamSettings = domain.NewObject()
	}
	if n.ProtocolSettings == nil {
		n.ProtocolSettings = domain.NewObject()
	}
	if n.ClientSettings == nil {
		n.ClientSettings = domain.NewObject()
	}

	// The transport and the security layer are whitelisted exactly like in
	// Sanitize, so a value the frozen core cannot load is rejected with the
	// offending field instead of being persisted (one such node makes every later
	// snapshot fail until it is archived).
	issues = append(issues, streamTransportIssues(n.StreamSettings)...)
	networkRaw, _ := n.StreamSettings.String("network")
	network, ok := NormalizeNetwork(networkRaw)
	if !ok {
		network = "tcp"
	}
	securityRaw, _ := n.StreamSettings.String("security")
	security := strings.ToLower(strings.TrimSpace(securityRaw))

	// REALITY 是 Xray 的 VLESS 专属安全层：其他协议在核心侧没有对应入站实现。
	// 这里必须拒绝，否则节点会通过校验并落库，但永远渲染不出可加载的配置 ——
	// 只在下发给 Agent 时才失败，留下一条"看起来正常、实际起不来"的记录。
	// （EndpointFor 里有一份同样的检查，用于导出路径。）
	if security == "reality" && proto != VLESS {
		add("stream_settings.security", "REALITY 只能用于 VLESS 入站，当前协议是 %s", proto)
	}

	for _, key := range streamSubObjects {
		if n.StreamSettings.Has(key) {
			if _, ok := n.StreamSettings.Sub(key); !ok {
				add("stream_settings."+key, "must be a JSON object")
			}
		}
	}

	// clients must never be supplied by an administrator: the panel injects the
	// authorized member set, otherwise authorization could be bypassed.
	if n.ProtocolSettings.Has("clients") {
		add("protocol_settings.clients", "clients are managed by the panel and cannot be set directly")
	}

	switch proto {
	case VLESS:
		flow := strings.TrimSpace(n.Flow)
		if !supportedFlows[flow] {
			add("flow", "unsupported VLESS flow %q (supported: empty, xtls-rprx-vision)", n.Flow)
		}
		if dec, ok := n.ProtocolSettings.String("decryption"); ok && dec != "none" && dec != "" {
			add("protocol_settings.decryption", "VLESS decryption must be \"none\"")
		}
		if flow != "" && network != "tcp" && network != "xhttp" {
			add("flow", "VLESS flow %q requires tcp or xhttp transport, not %q", flow, network)
		}
	case VMess:
		if strings.TrimSpace(n.Flow) != "" {
			add("flow", "flow is not applicable to VMess")
		}
		if _, ok := n.ProtocolSettings.String("decryption"); ok {
			add("protocol_settings.decryption", "decryption is a VLESS inbound field, not VMess")
		}
	case Trojan:
		if strings.TrimSpace(n.Flow) != "" {
			add("flow", "flow is not applicable to Trojan")
		}
		if strings.TrimSpace(n.Cipher) != "" {
			add("cipher", "cipher is not applicable to Trojan")
		}
	case Shadowsocks:
		if strings.TrimSpace(n.Flow) != "" {
			add("flow", "flow is not applicable to Shadowsocks")
		}
		method := strings.ToLower(strings.TrimSpace(n.Cipher))
		if method == "" {
			method, _ = n.ProtocolSettings.String("method")
			method = strings.ToLower(strings.TrimSpace(method))
		}
		if method == "" {
			add("cipher", "shadowsocks method is required")
		} else if !supportedSSMethods[method] {
			add("cipher", "unsupported shadowsocks method %q", method)
		}
		if strings.HasPrefix(method, "2022-") {
			if strings.TrimSpace(n.ServerKey) == "" && !n.ProtocolSettings.Has("password") {
				add("server_key", "Shadowsocks 2022 multi user inbound requires a server key (base64 PSK)")
			}
		} else if n.ProtocolSettings.Has("password") {
			add("protocol_settings.password", "a top level inbound password is only valid for Shadowsocks 2022 methods")
		}
	}

	// security specific requirements
	switch security {
	case "reality":
		if _, ok := n.StreamSettings.Sub("realitySettings"); !ok {
			add("stream_settings.realitySettings", "REALITY requires realitySettings")
		}
		rs, _ := n.StreamSettings.Sub("realitySettings")
		priv := strings.TrimSpace(n.ServerKey)
		if pk, ok := rs.String("privateKey"); ok && strings.TrimSpace(pk) != "" {
			if priv != "" && priv != strings.TrimSpace(pk) {
				add("stream_settings.realitySettings.privateKey", "conflicts with server_key")
			} else {
				priv = strings.TrimSpace(pk)
			}
		}
		if priv == "" {
			add("server_key", "REALITY requires a server private key")
		}
		shortIDs, _ := rs.Strings("shortIds")
		if len(shortIDs) == 0 {
			if sid, ok := rs.String("shortId"); !ok || strings.TrimSpace(sid) == "" {
				add("stream_settings.realitySettings.shortIds", "REALITY requires at least one short id")
			}
		}
		if pub, ok := clientPublicKey(n.ClientSettings); !ok {
			add("client_settings.public_key", "REALITY requires the client public key")
		} else if priv != "" {
			if err := validateX25519Pair(priv, pub); err != nil {
				add("client_settings.public_key", "%s", err)
			}
		}
		if _, ok := clientServerName(n); !ok {
			add("client_settings.server_name", "REALITY requires the client server name")
		}
		if network != "tcp" && network != "xhttp" && network != "grpc" && network != "httpupgrade" {
			add("stream_settings.network", "REALITY is not available for transport %q", network)
		}
	case "tls":
		// 证书材料只认核心真实读取的位置：tlsSettings.certificates[]。旧的顶层
		// certificateFile/keyFile 会被 NormalizeTLS 迁移；迁移冲突和不可用的内联
		// 材料都在这里拒绝，而不是落库之后才发现核心不认（见 tls.go）。
		normalized := n.StreamSettings.Clone()
		for _, is := range NormalizeTLS(normalized) {
			issues = append(issues, is)
		}
		if _, ok := normalized.Sub("tlsSettings"); !ok {
			add("stream_settings.tlsSettings", "TLS requires tlsSettings")
		}
		issues = append(issues, tlsMaterialIssues(normalized)...)
		if _, ok := clientServerName(n); !ok {
			add("client_settings.server_name", "TLS 需要客户端域名（成员配置里的 serverName）")
		}
	}

	if (network == "ws" || network == "httpupgrade") && !n.StreamSettings.Has("wsSettings") && network == "ws" {
		add("stream_settings.wsSettings", "websocket transport requires wsSettings")
	}
	if network == "grpc" && !n.StreamSettings.Has("grpcSettings") {
		add("stream_settings.grpcSettings", "gRPC transport requires grpcSettings")
	}
	if network == "xhttp" && !n.StreamSettings.Has("xhttpSettings") && !n.StreamSettings.Has("splithttpSettings") {
		add("stream_settings.xhttpSettings", "xhttp transport requires xhttpSettings")
	}
	return issues
}

// Sanitize returns the settings that are safe to persist with server-only
// secrets removed, plus the extracted secret. Unknown fields are preserved.
//
// The transport (network) and the security layer are whitelisted here as well as
// in Validate, so no caller can persist a combination the frozen core rejects:
// one such node makes every later snapshot fail until it is archived.
func Sanitize(n Node) (stream domain.Object, protocolSettings domain.Object, serverKey string, issues []Issue) {
	stream = n.StreamSettings.Clone()
	if stream == nil {
		stream = domain.NewObject()
	}
	protocolSettings = n.ProtocolSettings.Clone()
	if protocolSettings == nil {
		protocolSettings = domain.NewObject()
	}
	serverKey = strings.TrimSpace(n.ServerKey)

	issues = append(issues, streamTransportIssues(stream)...)
	// Historical rows may still carry the legacy top-level certificateFile/keyFile
	// pair; migrate them to the core-native certificates[] array before persisting
	// again, and report a conflicting placement instead of dropping one source.
	if security, _ := stream.String("security"); strings.EqualFold(strings.TrimSpace(security), "tls") {
		issues = append(issues, NormalizeTLS(stream)...)
	}
	if rs, ok := stream.Sub("realitySettings"); ok {
		// Older panel builds wrote a nested "settings" object holding the
		// client public key. Xray's RealityObject has no such field: client
		// parameters live in client_settings, and an unknown nested object is
		// either rejected by the core or silently ignored. Drop it on save so
		// an already-stored node is repaired instead of carrying dead data.
		if rs.Has("settings") {
			rs.Delete("settings")
			if err := stream.Set("realitySettings", rs); err != nil {
				issues = append(issues, Issue{Field: "stream_settings.realitySettings", Message: err.Error()})
			}
		}
		if pk, ok := rs.String("privateKey"); ok && strings.TrimSpace(pk) != "" {
			if serverKey == "" {
				serverKey = strings.TrimSpace(pk)
			} else if security, _ := stream.String("security"); strings.EqualFold(strings.TrimSpace(security), "reality") && serverKey != strings.TrimSpace(pk) {
				issues = append(issues, Issue{Field: "stream_settings.realitySettings.privateKey", Message: "conflicts with server_key"})
			}
			rs.Delete("privateKey")
			// keep the (non secret) public key that mirrors the private key
			if err := stream.Set("realitySettings", rs); err != nil {
				issues = append(issues, Issue{Field: "stream_settings.realitySettings", Message: err.Error()})
			}
		}
	}

	if strings.EqualFold(n.Protocol, Shadowsocks) {
		if pw, ok := protocolSettings.String("password"); ok && strings.TrimSpace(pw) != "" {
			if serverKey == "" {
				serverKey = strings.TrimSpace(pw)
			}
			protocolSettings.Delete("password")
		}
	}
	return stream, protocolSettings, serverKey, issues
}

// GenerateX25519 returns a REALITY (X25519) private/public key pair in the
// base64url encoding Xray expects.
func GenerateX25519() (privB64 string, pubB64 string, err error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()), nil
}

// PublicKeyFor derives the X25519 public key that pairs with a REALITY private
// key. Admins only ever need to supply or generate the private key: the public
// key is a pure function of it, so requiring them to paste it as well is both
// redundant and a source of mismatched pairs.
func PublicKeyFor(privateB64 string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(privateB64))
	if err != nil {
		return "", fmt.Errorf("REALITY private key must be a base64url X25519 key")
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", fmt.Errorf("REALITY private key must contain 32 bytes")
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

// ClientSetting reads a client setting accepting snake_case (canonical) and
// camelCase aliases.
func ClientSetting(o domain.Object, keys ...string) (string, bool) {
	for _, k := range keys {
		if v, ok := o.String(k); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// ClientSettingStrings reads a string array client setting with aliases.
func ClientSettingStrings(o domain.Object, keys ...string) ([]string, bool) {
	for _, k := range keys {
		if v, ok := o.Strings(k); ok && len(v) > 0 {
			return v, true
		}
	}
	return nil, false
}

func clientPublicKey(c domain.Object) (string, bool) {
	return ClientSetting(c, "public_key", "publicKey", "pbk")
}

// ServerNameFor resolves the client visible server name, falling back to the
// inbound settings when the admin did not repeat it in client_settings.
func ServerNameFor(client, stream domain.Object) (string, bool) {
	if v, ok := ClientSetting(client, "server_name", "serverName", "sni"); ok {
		return v, true
	}
	if rs, ok := stream.Sub("realitySettings"); ok {
		if names, ok := rs.Strings("serverNames"); ok && len(names) > 0 {
			return names[0], true
		}
	}
	if ts, ok := stream.Sub("tlsSettings"); ok {
		if v, ok := ts.String("serverName"); ok && strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	return "", false
}

func clientServerName(n Node) (string, bool) {
	return ServerNameFor(n.ClientSettings, n.StreamSettings)
}

// SortedIssueFields returns the issue keys, used for stable test assertions.
func SortedIssueFields(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Field)
	}
	sort.Strings(out)
	return out
}
