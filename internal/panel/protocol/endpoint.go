package protocol

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"

	"proxypanel/internal/panel/domain"
)

// Endpoint is the client visible projection of a node. It never contains a
// server private key: only what a client needs to connect.
type Endpoint struct {
	NodeID   string
	Name     string
	Protocol string
	Address  string
	Port     int

	Network     string
	Path        string
	Host        string
	ServiceName string
	GRPCMode    string
	HeaderType  string
	Seed        string
	// TCPHeader is the Xray tcpSettings.header object (e.g. type "http" with a
	// request/response伪装). It is part of the connection semantics of a TCP
	// inbound, so an exporter that cannot express it must reject the node
	// instead of emitting a plain byte stream.
	TCPHeader    map[string]any
	QuicSecurity string
	QuicKey      string
	XHTTPMode    string

	Security      string
	SNI           string
	ALPN          []string
	Fingerprint   string
	PublicKey     string
	ShortID       string
	AllowInsecure bool
	// PeerCertSha256 is the lowercase hex SHA-256 of the leaf certificate the
	// node serves. Xray 26 removed the client "allowInsecure" option and
	// migrated it to "pinnedPeerCertSha256", so this pin is the only supported
	// way to express "this node uses a self-signed certificate". It is derived
	// from the inline certificate in stream_settings or from the administrator
	// supplied client setting; it is never a private key.
	PeerCertSha256 string
	// PeerCertPinned marks PeerCertSha256 as an explicit administrator pin
	// (client_settings) rather than a value derived from the node certificate.
	PeerCertPinned bool

	// ServerPSK is the Shadowsocks 2022 server pre-shared key. Multi-user 2022
	// clients authenticate with "<serverPSK>:<userPSK>", so this is a required
	// client credential for that protocol only.
	ServerPSK string

	Flow    string
	Cipher  string
	AlterID int
}

func normNetwork(network string) string {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "", "tcp", "raw":
		return "tcp"
	case "mkcp":
		return "kcp"
	case "h2":
		return "http"
	case "splithttp":
		return "xhttp"
	default:
		return strings.ToLower(strings.TrimSpace(network))
	}
}

func normSecurity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return ""
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// EndpointFor projects a stored node onto the client view. Issues are returned
// for missing client parameters instead of silently emitting a broken config.
func EndpointFor(node domain.Node) (Endpoint, []Issue) {
	var issues []Issue
	add := func(field, msg string) { issues = append(issues, Issue{Field: field, Message: msg}) }

	stream := node.StreamSettings
	if stream == nil {
		stream = domain.NewObject()
	}
	client := node.ClientSettings
	if client == nil {
		client = domain.NewObject()
	}
	networkRaw, _ := stream.String("network")
	ep := Endpoint{
		NodeID:   node.ID,
		Name:     node.Name,
		Protocol: strings.ToLower(strings.TrimSpace(node.Protocol)),
		Address:  strings.TrimSpace(node.Host),
		// 客户端连的是「对外端口」；未设置时退回实际监听端口。
		Port:     clientPort(node),
		Network:  normNetwork(networkRaw),
		Security: normSecurity(func() string { s, _ := stream.String("security"); return s }()),
		Flow:     strings.TrimSpace(node.Flow),
	}
	if ep.Address == "" {
		add("host", "client facing host is empty")
	}
	if ep.Port == 0 {
		add("port", "client facing port is empty")
	}

	switch ep.Network {
	case "tcp":
		// Xray 26 renamed the TCP transport to RAW: both tcpSettings and
		// rawSettings are valid carriers, and an admin may legitimately use
		// either name. Read both so a rawSettings.header is never silently
		// dropped from the exported client config.
		for _, key := range []string{"tcpSettings", "rawSettings"} {
			tcp, ok := stream.Sub(key)
			if !ok {
				continue
			}
			header, ok := tcp.Sub("header")
			if !ok {
				continue
			}
			// Only a non-"none" header carries parameters a client must
			// reproduce; "none" and an empty header stay absent so the export
			// of an ordinary TCP node is unchanged.
			if headerType, ok := header.String("type"); ok && strings.TrimSpace(headerType) != "" && !strings.EqualFold(strings.TrimSpace(headerType), "none") {
				ep.TCPHeader = map[string]any{}
				for hkey, raw := range header {
					var value any
					if err := json.Unmarshal(raw, &value); err == nil {
						ep.TCPHeader[hkey] = value
					}
				}
			}
		}
	case "ws":
		ws, ok := stream.Sub("wsSettings")
		if !ok {
			add("stream_settings.wsSettings", "websocket transport has no wsSettings")
			break
		}
		ep.Path, _ = ws.String("path")
		if ep.Path == "" {
			ep.Path = "/"
		}
		if headers, ok := ws.Sub("headers"); ok {
			for _, key := range []string{"Host", "host"} {
				if h, ok := headers.String(key); ok && strings.TrimSpace(h) != "" {
					ep.Host = strings.TrimSpace(h)
					break
				}
			}
		}
	case "httpupgrade":
		h, ok := stream.Sub("httpupgradeSettings")
		if !ok {
			add("stream_settings.httpupgradeSettings", "HTTPUpgrade transport has no httpupgradeSettings")
			break
		}
		ep.Path, _ = h.String("path")
		if ep.Path == "" {
			ep.Path = "/"
		}
		ep.Host, _ = h.String("host")
	case "grpc":
		gr, ok := stream.Sub("grpcSettings")
		if !ok {
			add("stream_settings.grpcSettings", "gRPC transport has no grpcSettings")
			break
		}
		ep.ServiceName, _ = gr.String("serviceName")
		if mm, ok := gr.Bool("multiMode"); ok && mm {
			ep.GRPCMode = "multi"
		}
	case "xhttp":
		x, ok := stream.Sub("xhttpSettings")
		if !ok {
			x, ok = stream.Sub("splithttpSettings")
		}
		if !ok {
			add("stream_settings.xhttpSettings", "xhttp transport has no xhttpSettings")
			break
		}
		ep.Path, _ = x.String("path")
		if ep.Path == "" {
			ep.Path = "/"
		}
		ep.Host, _ = x.String("host")
		ep.XHTTPMode, _ = x.String("mode")
	case "kcp":
		k, ok := stream.Sub("kcpSettings")
		if !ok {
			add("stream_settings.kcpSettings", "mKCP transport has no kcpSettings")
			break
		}
		ep.Seed, _ = k.String("seed")
		if header, ok := k.Sub("header"); ok {
			ep.HeaderType, _ = header.String("type")
		}
	case "quic":
		q, ok := stream.Sub("quicSettings")
		if !ok {
			add("stream_settings.quicSettings", "QUIC transport has no quicSettings")
			break
		}
		ep.QuicSecurity, _ = q.String("security")
		ep.QuicKey, _ = q.String("key")
	case "http":
		h, ok := stream.Sub("httpSettings")
		if !ok {
			add("stream_settings.httpSettings", "HTTP/2 transport has no httpSettings")
			break
		}
		ep.Path, _ = h.String("path")
		if hosts, ok := h.Strings("host"); ok && len(hosts) > 0 {
			ep.Host = hosts[0]
		}
	}

	// Security parameters that only the client needs.
	if v, ok := ClientSetting(client, "server_name", "serverName", "sni"); ok {
		ep.SNI = v
	}
	if v, ok := ClientSettingStrings(client, "alpn", "ALPN"); ok {
		ep.ALPN = v
	}
	if v, ok := ClientSetting(client, "fingerprint", "fp"); ok {
		ep.Fingerprint = v
	}
	if b, ok := client.Bool("allow_insecure"); ok {
		ep.AllowInsecure = b
	} else if b, ok := client.Bool("allowInsecure"); ok {
		ep.AllowInsecure = b
	}
	// Xray 26 removed the client "allowInsecure" switch and migrated it to
	// "pinnedPeerCertSha256". An explicit administrator pin wins; otherwise the
	// certificate the node itself serves (inline in stream_settings) is the pin.
	if v, ok := ClientSetting(client, "pinned_peer_cert_sha256", "pinnedPeerCertSha256"); ok {
		pin, err := NormalizeCertSHA256(v)
		if err != nil {
			add("client_settings.pinned_peer_cert_sha256", "not a SHA-256 certificate hash (hex or base64)")
		} else {
			ep.PeerCertSha256, ep.PeerCertPinned = pin, true
		}
	} else if ep.Security == "tls" {
		if pin, ok := inlineLeafCertSHA256(stream); ok {
			ep.PeerCertSha256 = pin
		}
	}

	// REALITY 是 Xray 的 VLESS 专属传输安全层。其他协议（VMess/Trojan/SS）
	// 在核心侧没有对应的入站实现，配置能存进去但永远无法加载。
	// 必须在创建/更新时就拒绝，而不是等到下发给 Agent 才失败 ——
	// 否则会留下一条"看起来正常、实际永远起不来"的节点记录。
	if ep.Security == "reality" && ep.Protocol != VLESS {
		add("stream_settings.security",
			"REALITY 只能用于 VLESS 入站，当前协议是 "+ep.Protocol)
	}

	switch ep.Security {
	case "reality":
		rs, hasRS := stream.Sub("realitySettings")
		if v, ok := clientPublicKey(client); ok {
			ep.PublicKey = v
		} else if hasRS {
			if v, ok := rs.String("publicKey"); ok {
				ep.PublicKey = strings.TrimSpace(v)
			}
		}
		if v, ok := ClientSetting(client, "short_id", "shortId", "sid"); ok {
			ep.ShortID = v
		} else if hasRS {
			if ids, ok := rs.Strings("shortIds"); ok && len(ids) > 0 {
				ep.ShortID = ids[0]
			}
		}
		if ep.SNI == "" {
			if sn, ok := ServerNameFor(client, stream); ok {
				ep.SNI = sn
			}
		}
		if ep.Fingerprint == "" {
			// REALITY clients must present a uTLS fingerprint; the fixed default
			// is documented in the implementation report.
			ep.Fingerprint = "chrome"
		}
		if ep.PublicKey == "" {
			add("client_settings.public_key", "REALITY client public key is missing")
		}
		if ep.SNI == "" {
			add("client_settings.server_name", "REALITY client server name is missing")
		}
	case "tls":
		if ep.SNI == "" {
			if sn, ok := ServerNameFor(client, stream); ok {
				ep.SNI = sn
			}
		}
		if ep.SNI == "" {
			add("client_settings.server_name", "TLS client server name is missing")
		}
		if ts, ok := stream.Sub("tlsSettings"); ok {
			if alpn, ok := ts.Strings("alpn"); ok && len(ep.ALPN) == 0 {
				ep.ALPN = alpn
			}
		}
	}

	switch ep.Protocol {
	case Shadowsocks:
		ep.Cipher = strings.TrimSpace(node.Cipher)
		if ep.Cipher == "" {
			if m, ok := node.ProtocolSettings.String("method"); ok {
				ep.Cipher = strings.ToLower(strings.TrimSpace(m))
			}
		}
		if ep.Cipher == "" {
			add("cipher", "shadowsocks method is missing")
		}
		if IsSS2022(ep.Cipher) {
			// Multi-user Shadowsocks 2022 clients must present the server PSK
			// together with their own PSK; the server PSK never leaves the node
			// for any other protocol or method.
			ep.ServerPSK = strings.TrimSpace(node.ServerKey)
			if ep.ServerPSK == "" {
				if v, ok := node.ProtocolSettings.String("password"); ok {
					ep.ServerPSK = strings.TrimSpace(v)
				}
			}
			if ep.ServerPSK == "" {
				add("server_key", "Shadowsocks 2022 client credentials need the server key")
			}
		}
	case VLESS:
		if ep.Flow != "" && !supportedFlows[ep.Flow] {
			add("flow", "unsupported VLESS flow "+ep.Flow)
		}
	case VMess:
		ep.AlterID = 0 // VMess AEAD only; legacy alterId is not supported
	}
	return ep, issues
}

// IsSS2022 reports whether a Shadowsocks method is one of the 2022 (SIP022)
// methods. Those are the only multi-user methods that need the server PSK in
// the client credential.
func IsSS2022(method string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "2022-")
}

// NormalizeCertSHA256 accepts a certificate SHA-256 hash as lowercase/uppercase
// hex or as standard/base64url (with or without padding) and returns lowercase
// hex, the encoding Xray's pinnedPeerCertSha256 expects.
func NormalizeCertSHA256(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", fmt.Errorf("empty certificate hash")
	}
	if len(v) == 2*sha256.Size {
		if _, err := hex.DecodeString(v); err == nil {
			return strings.ToLower(v), nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if sum, err := enc.DecodeString(v); err == nil && len(sum) == sha256.Size {
			return hex.EncodeToString(sum), nil
		}
	}
	return "", fmt.Errorf("not a SHA-256 certificate hash")
}

// inlineLeafCertSHA256 derives the certificate pin from the first inline
// encipherment certificate of a node's tlsSettings. Only public material is
// read; a node that only references certificate files has no derivable pin.
func inlineLeafCertSHA256(stream domain.Object) (string, bool) {
	ts, ok := stream.Sub("tlsSettings")
	if !ok {
		return "", false
	}
	raw, ok := ts.Get("certificates")
	if !ok {
		return "", false
	}
	var entries []struct {
		Certificate json.RawMessage `json:"certificate"`
		Usage       string          `json:"usage"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", false
	}
	for _, entry := range entries {
		// Only an ENCIPHERMENT entry is served to clients; a usage=verify entry is
		// a root-pool CA and must not become the pin (the real core presents the
		// encipherment certificate even when a CA entry comes first).
		if certUsage(entry.Usage) != usageEncipherment {
			continue
		}
		// Xray accepts the inline certificate either as one PEM document or as
		// the individual PEM lines; the line array is the form the node API,
		// the core's own config and the client matrix fixture use. Both describe
		// the same leaf, so the chunks of one entry are joined before the PEM
		// block is decoded - decoding them one by one never finds a block.
		if pin, ok := pemCertSHA256(strings.Join(certificateStrings(entry.Certificate), "\n")); ok {
			return pin, true
		}
	}
	return "", false
}

// certificateStrings accepts both the array and the single string form of the
// "certificate" field.
func certificateStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}
	}
	return nil
}

func pemCertSHA256(pemText string) (string, bool) {
	rest := []byte(pemText)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return "", false
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		sum := sha256.Sum256(block.Bytes)
		return hex.EncodeToString(sum[:]), true
	}
}

// clientPort 返回写进成员配置的端口。
//
// ExternalPort 用于「对外 443 → 实际监听 20101」这类中转：服务端仍监听 Port，
// 但客户端要连 ExternalPort。转发本身由运维在机器上配置，面板不代管。
func clientPort(node domain.Node) int {
	if node.ExternalPort > 0 {
		return node.ExternalPort
	}
	return node.Port
}
