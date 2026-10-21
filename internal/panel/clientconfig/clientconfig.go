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

// displayName returns a unique, human readable node label.
func displayName(n NodeConfig, index int, used map[string]bool) string {
	name := strings.TrimSpace(n.Remark)
	if name == "" {
		name = strings.TrimSpace(n.Endpoint.Name)
	}
	if name == "" {
		name = fmt.Sprintf("node-%d", index+1)
	}
	candidate := name
	for i := 2; used[candidate]; i++ {
		candidate = fmt.Sprintf("%s #%d", name, i)
	}
	used[candidate] = true
	return candidate
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

// sortedFormats returns a stable copy for responses.
func sortedFormats() []string {
	out := append([]string(nil), Formats...)
	sort.Strings(out)
	return out
}

// ─── Xray client JSON ───────────────────────────────────────────────────────

func renderXray(req Request) ([]byte, error) {
	used := map[string]bool{}
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
		// plain TCP needs no sub settings
	default:
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "xray", Field: "stream_settings.network", Reason: "unknown transport " + ep.Network}
	}
	switch ep.Security {
	case "tls":
		ts := map[string]any{"serverName": ep.SNI}
		if len(ep.ALPN) > 0 {
			ts["alpn"] = ep.ALPN
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
