// Package clientconfig renders member subscriptions for the fixed client set:
// Xray (native JSON), sing-box, Mihomo/Clash.Meta and v2rayN (share links).
// A parameter that a client cannot express is a hard error naming the node and
// the field - never a silently dropped option.
package clientconfig

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
)

// Supported formats (frozen contract: /sub/{token}?format=...).
const (
	FormatXray    = "xray"
	FormatMihomo  = "mihomo"
	FormatSingbox = "singbox"
	FormatV2rayN  = "v2rayn"
)

// Formats is the list advertised to members.
var Formats = []string{FormatXray, FormatMihomo, FormatSingbox, FormatV2rayN}

// NodeConfig pairs a client visible endpoint with the member credential.
type NodeConfig struct {
	Endpoint   protocol.Endpoint
	Credential string // uuid (vless/vmess) or password (trojan/shadowsocks)
	Remark     string
}

// Request is one subscription render.
type Request struct {
	Format           string
	SubscriptionName string
	Nodes            []NodeConfig
	// Template optionally supplies extra top level configuration (dns, rules,
	// log level ...). Panel generated keys always win.
	Template domain.Object
	// Base64 wraps share link lists (v2rayN) in base64, as requested by ?base64=1.
	Base64 bool
}

// Result is a rendered subscription body.
type Result struct {
	Format      string
	ContentType string
	Body        []byte
	NodeCount   int
}

// UnsupportedError marks a node/parameter combination a client cannot express.
// It carries no secret values.
type UnsupportedError struct {
	NodeName string
	NodeID   string
	Client   string
	Field    string
	Reason   string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s: node %q cannot represent %s (%s)", e.Client, e.NodeName, e.Field, e.Reason)
}

// NormalizeFormat maps user input onto the four supported formats.
func NormalizeFormat(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "xray", "xray-json":
		return FormatXray, true
	case "mihomo", "clash", "clash-meta", "meta":
		return FormatMihomo, true
	case "singbox", "sing-box", "sing_box":
		return FormatSingbox, true
	case "v2rayn", "v2ray", "v2rayng", "links":
		return FormatV2rayN, true
	}
	return "", false
}

// Render dispatches to the format encoder.
func Render(req Request) (Result, error) {
	format, ok := NormalizeFormat(req.Format)
	if !ok {
		return Result{}, fmt.Errorf("unsupported format %q", req.Format)
	}
	var (
		body        []byte
		contentType = contentType(format)
		err         error
	)
	switch format {
	case FormatXray:
		body, err = renderXray(req)
	case FormatSingbox:
		body, err = renderSingbox(req)
	case FormatMihomo:
		body, err = renderMihomo(req)
	case FormatV2rayN:
		body, err = renderV2rayN(req)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Format: format, ContentType: contentType, Body: body, NodeCount: len(req.Nodes)}, nil
}

func contentType(format string) string {
	switch format {
	case FormatMihomo:
		return "text/yaml; charset=utf-8"
	case FormatV2rayN:
		return "text/plain; charset=utf-8"
	default:
		return "application/json; charset=utf-8"
	}
}

// reservedTags are the outbound/proxy names the exporters generate themselves.
// A user controllable node name that equals one of them used to produce a
// duplicate outbound tag, which makes the whole subscription unloadable
// (sing-box: "duplicate outbound/endpoint tag: direct"). The set is seeded into
// the uniqueness map, so such a node is renamed instead of colliding.
var reservedTags = []string{
	// sing-box built-ins and generated outbounds/groups.
	"direct", "block", "dns-out", "mixed-in",
	// Xray generated outbounds and inbounds.
	"proxy", "block", "direct", "socks-in", "http-in",
	// Mihomo generated proxy-groups and built-in policies.
	"DIRECT", "REJECT", "PROXY", "GLOBAL", "PASS", "REJECT-DROP", "COMPATIBLE",
}

func reservedTagSet() map[string]bool {
	out := make(map[string]bool, len(reservedTags))
	for _, tag := range reservedTags {
		out[tag] = true
	}
	return out
}

// reservedNameSuffix is appended to a node label that would collide with a
// generated tag; the visible label stays recognisable for the administrator.
const reservedNameSuffix = " (节点)"

// displayName returns a unique, human readable node label. It never returns a
// name that collides with a generated outbound/group tag or with another node.
func displayName(n NodeConfig, index int, used map[string]bool) string {
	name := strings.TrimSpace(n.Remark)
	if name == "" {
		name = strings.TrimSpace(n.Endpoint.Name)
	}
	if name == "" {
		name = fmt.Sprintf("node-%d", index+1)
	}
	candidate := name
	// Only a collision with a *generated* tag is renamed with the suffix; two
	// nodes that share a label keep the established "name #2" numbering.
	if reservedTagSet()[candidate] {
		candidate = name + reservedNameSuffix
	}
	for i := 2; used[candidate]; i++ {
		candidate = fmt.Sprintf("%s #%d", name, i)
	}
	used[candidate] = true
	return candidate
}

// newUsedTags returns the uniqueness map pre-populated with generated tags so
// no node label can take one of them.
func newUsedTags() map[string]bool { return reservedTagSet() }

// requireRepresentableTCPHeader rejects a TCP transport whose伪装 header a
// client format cannot express. Emitting the node without its header would
// silently change the wire protocol (the server expects an HTTP request).
func requireRepresentableTCPHeader(n NodeConfig, client string) error {
	if len(n.Endpoint.TCPHeader) == 0 {
		return nil
	}
	return &UnsupportedError{NodeName: n.Endpoint.Name, NodeID: n.Endpoint.NodeID, Client: client,
		Field:  "stream_settings.tcpSettings.header",
		Reason: "this client cannot reproduce the Xray TCP HTTP伪装 header"}
}

// requireNoShadowsocksTLS rejects Shadowsocks with a TLS layer for formats that
// would otherwise emit a plaintext ss proxy/link, silently dropping TLS.
func requireShadowsocksTLSRepresentable(n NodeConfig, client string) error {
	if n.Endpoint.Protocol != protocol.Shadowsocks || n.Endpoint.Security == "" {
		return nil
	}
	return &UnsupportedError{NodeName: n.Endpoint.Name, NodeID: n.Endpoint.NodeID, Client: client,
		Field:  "stream_settings.security",
		Reason: "Shadowsocks over TLS cannot be represented by this client; it would silently become plaintext"}
}

// requireNoGRPCMultiMode rejects multiMode gRPC, which sing-box and Mihomo
// cannot express (they only implement the single service-name mode).
func requireNoGRPCMultiMode(n NodeConfig, client string) error {
	if n.Endpoint.Network != "grpc" || n.Endpoint.GRPCMode != "multi" {
		return nil
	}
	return &UnsupportedError{NodeName: n.Endpoint.Name, NodeID: n.Endpoint.NodeID, Client: client,
		Field:  "stream_settings.grpcSettings.multiMode",
		Reason: "this client has no multi-mode gRPC transport"}
}

func mergeTemplate(base map[string]any, tmpl domain.Object) {
	for k, raw := range tmpl {
		if _, exists := base[k]; exists {
			continue // panel generated keys win
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		base[k] = v
	}
}

// requireNetwork rejects transports a client cannot express.
func requireNetwork(n NodeConfig, client string, allowed ...string) error {
	for _, a := range allowed {
		if n.Endpoint.Network == a {
			return nil
		}
	}
	return &UnsupportedError{
		NodeName: n.Endpoint.Name, NodeID: n.Endpoint.NodeID, Client: client,
		Field:  "stream_settings.network",
		Reason: "transport " + n.Endpoint.Network + " is not supported by this client",
	}
}

// ─── shared credential projections ─────────────────────────────────────────
// shadowsocksPassword returns the client side Shadowsocks password. Shadowsocks
// 2022 multi-user servers hand each member a user PSK that only works together
// with the server PSK ("<serverPSK>:<userPSK>"); every other method keeps the
// member secret unchanged.
func shadowsocksPassword(client string, n NodeConfig) (string, error) {
	ep := n.Endpoint
	if !protocol.IsSS2022(ep.Cipher) {
		return n.Credential, nil
	}
	if strings.TrimSpace(ep.ServerPSK) == "" {
		return "", &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: client, Field: "server_key",
			Reason: "Shadowsocks 2022 multi-user clients authenticate with <serverPSK>:<userPSK> and the node has no server key"}
	}
	return ep.ServerPSK + ":" + n.Credential, nil
}

// requireNoExplicitPin rejects a format that cannot enforce an administrator
// supplied certificate pin instead of silently dropping the setting.
func requireNoExplicitPin(client string, n NodeConfig) error {
	if !n.Endpoint.PeerCertPinned {
		return nil
	}
	return &UnsupportedError{NodeName: n.Endpoint.Name, NodeID: n.Endpoint.NodeID, Client: client,
		Field:  "client_settings.pinned_peer_cert_sha256",
		Reason: "this client cannot verify a certificate SHA-256 pin"}
}

// http1Transport reports whether the transport tunnels an HTTP/1.1 Upgrade
// request (WebSocket or HTTPUpgrade) inside the TLS session.
func http1Transport(network string) bool {
	switch network {
	case "ws", "httpupgrade":
		return true
	}
	return false
}

// clientALPN returns the ALPN list a client must offer for this endpoint.
//
// A WebSocket or HTTPUpgrade tunnel runs on HTTP/1.1, so offering "h2" through
// ALPN makes the server negotiate HTTP/2 and the upgrade request then never
// completes: the real-core matrix showed a ws+tls node answering 200 for Xray
// (a client that offers no ALPN at all) while sing-box and Mihomo timed out,
// because protocol.EndpointFor copies the server's tlsSettings.alpn ("h2",
// "http/1.1") onto the endpoint. h2 is not a value the administrator can pick
// for such a node - it is physically unable to carry the tunnel - so the export
// states the only usable protocol instead of propagating a broken list. Every
// other transport keeps the administrator's ALPN untouched.
//
// The rule is keyed on the transport, not on Security == "tls": REALITY
// terminates TLS just the same and a ws+REALITY node carries the identical
// HTTP/1.1 upgrade, so it inherits the very same broken ALPN list from
// realitySettings. Gating on "tls" left ws+REALITY exporting h2 to sing-box and
// v2rayN (Xray and Mihomo happened to emit no ALPN and therefore masked the bug).
// The condition is "a TLS-terminating session on an HTTP/1.1 transport", which is
// exactly "Security is set at all" - an empty Security means no TLS layer, so a
// plain ws node keeps whatever ALPN it had (normally none).
func clientALPN(ep protocol.Endpoint) []string {
	if ep.Security != "" && http1Transport(ep.Network) {
		return []string{"http/1.1"}
	}
	return ep.ALPN
}

// sortedFormats returns a stable copy for responses.
func sortedFormats() []string {
	out := append([]string(nil), Formats...)
	sort.Strings(out)
	return out
}

// ─── Xray client JSON ───────────────────────────────────────────────────────

func renderXray(req Request) ([]byte, error) {
	used := newUsedTags()
	outbounds := make([]any, 0, len(req.Nodes)+2)
	for i, n := range req.Nodes {
		ob, err := xrayOutbound(n, displayName(n, i, used), i)
		if err != nil {
			return nil, err
		}
		outbounds = append(outbounds, ob)
	}
	outbounds = append(outbounds,
		map[string]any{"tag": "direct", "protocol": "freedom"},
		map[string]any{"tag": "block", "protocol": "blackhole"},
	)
	doc := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{
				"tag": "socks-in", "listen": "127.0.0.1", "port": 10808, "protocol": "socks",
				"settings": map[string]any{"udp": true, "auth": "noauth"},
			},
			map[string]any{
				"tag": "http-in", "listen": "127.0.0.1", "port": 10809, "protocol": "http",
				"settings": map[string]any{},
			},
		},
		"outbounds": outbounds,
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules": []any{
				map[string]any{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "direct"},
			},
		},
	}
	mergeTemplate(doc, req.Template)
	return json.MarshalIndent(doc, "", "  ")
}

func xrayOutbound(n NodeConfig, name string, index int) (map[string]any, error) {
	ep := n.Endpoint
	tag := "proxy"
	if index > 0 {
		tag = fmt.Sprintf("proxy-%d", index+1)
	}
	out := map[string]any{"tag": tag}
	switch ep.Protocol {
	case protocol.VLESS:
		user := map[string]any{"id": n.Credential, "encryption": "none"}
		if ep.Flow != "" {
			user["flow"] = ep.Flow
		}
		out["protocol"] = "vless"
		out["settings"] = map[string]any{
			"vnext": []any{map[string]any{
				"address": ep.Address, "port": ep.Port, "users": []any{user},
			}},
		}
	case protocol.VMess:
		out["protocol"] = "vmess"
		out["settings"] = map[string]any{
			"vnext": []any{map[string]any{
				"address": ep.Address, "port": ep.Port,
				"users": []any{map[string]any{"id": n.Credential, "alterId": 0, "security": "auto"}},
			}},
		}
	case protocol.Trojan:
		out["protocol"] = "trojan"
		out["settings"] = map[string]any{
			"servers": []any{map[string]any{"address": ep.Address, "port": ep.Port, "password": n.Credential}},
		}
	case protocol.Shadowsocks:
		if ep.Cipher == "" {
			return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "xray", Field: "cipher", Reason: "shadowsocks method is missing"}
		}
		password, err := shadowsocksPassword("xray", n)
		if err != nil {
			return nil, err
		}
		out["protocol"] = "shadowsocks"
		out["settings"] = map[string]any{
			"servers": []any{map[string]any{
				"address": ep.Address, "port": ep.Port, "method": ep.Cipher, "password": password,
			}},
		}
	default:
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "xray", Field: "protocol", Reason: "unsupported protocol " + ep.Protocol}
	}
	ss, err := xrayStreamSettings(n)
	if err != nil {
		return nil, err
	}
	out["streamSettings"] = ss
	return out, nil
}

func xrayStreamSettings(n NodeConfig) (map[string]any, error) {
	ep := n.Endpoint
	ss := map[string]any{"network": ep.Network}
	switch ep.Network {
	case "ws":
		ws := map[string]any{"path": ep.Path}
		if ep.Host != "" {
			ws["headers"] = map[string]any{"Host": ep.Host}
		}
		ss["wsSettings"] = ws
	case "grpc":
		g := map[string]any{"serviceName": ep.ServiceName}
		if ep.GRPCMode == "multi" {
			g["multiMode"] = true
		}
		ss["grpcSettings"] = g
	case "httpupgrade":
		ss["httpupgradeSettings"] = map[string]any{"path": ep.Path, "host": ep.Host}
	case "xhttp":
		x := map[string]any{"path": ep.Path, "host": ep.Host}
		if ep.XHTTPMode != "" {
			x["mode"] = ep.XHTTPMode
		}
		ss["xhttpSettings"] = x
	case "kcp":
		k := map[string]any{"header": map[string]any{"type": firstNonEmpty(ep.HeaderType, "none")}}
		if ep.Seed != "" {
			k["seed"] = ep.Seed
		}
		ss["kcpSettings"] = k
	case "quic":
		ss["quicSettings"] = map[string]any{
			"security": firstNonEmpty(ep.QuicSecurity, "none"),
			"key":      ep.QuicKey,
			"header":   map[string]any{"type": "none"},
		}
	case "http":
		h := map[string]any{"path": ep.Path}
		if ep.Host != "" {
			h["host"] = []string{ep.Host}
		}
		ss["httpSettings"] = h
	case "tcp":
		// A TCP HTTP伪装 header changes what the server expects on the wire;
		// authentic Xray JSON can express it, so reproduce it instead of
		// emitting a plain byte stream.
		if len(ep.TCPHeader) > 0 {
			ss["tcpSettings"] = map[string]any{"header": ep.TCPHeader}
		}
	default:
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "xray", Field: "stream_settings.network", Reason: "unknown transport " + ep.Network}
	}
	switch ep.Security {
	case "tls":
		ts := map[string]any{"serverName": ep.SNI}
		if alpn := clientALPN(ep); len(alpn) > 0 {
			ts["alpn"] = alpn
		}
		if ep.Fingerprint != "" {
			ts["fingerprint"] = ep.Fingerprint
		}
		// Xray 26 removed "allowInsecure" (it refuses the option even as
		// false-negative) and migrated it to "pinnedPeerCertSha256". The pin is
		// the certificate the node serves, or an explicit administrator pin;
		// when neither exists the combination is rejected instead of silently
		// emitting a config the core refuses to load.
		if ep.PeerCertSha256 != "" && (ep.AllowInsecure || ep.PeerCertPinned) {
			ts["pinnedPeerCertSha256"] = ep.PeerCertSha256
		} else if ep.AllowInsecure {
			return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "xray",
				Field:  "client_settings.allow_insecure",
				Reason: "Xray 26 replaced allowInsecure with pinnedPeerCertSha256 and the node has no inline certificate or client_settings.pinned_peer_cert_sha256 to pin"}
		}
		ss["security"] = "tls"
		ss["tlsSettings"] = ts
	case "reality":
		if ep.PublicKey == "" || ep.ShortID == "" && ep.SNI == "" {
			return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "xray", Field: "client_settings.public_key", Reason: "REALITY client parameters are incomplete"}
		}
		ss["security"] = "reality"
		ss["realitySettings"] = map[string]any{
			"serverName":  ep.SNI,
			"fingerprint": firstNonEmpty(ep.Fingerprint, "chrome"),
			"publicKey":   ep.PublicKey,
			"shortId":     ep.ShortID,
			"spiderX":     "/",
		}
	}
	return ss, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
