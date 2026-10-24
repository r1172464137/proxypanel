package clientconfig

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	"proxypanel/internal/panel/protocol"
)

// ─── sing-box ──────────────────────────────────────────────────────────────

func renderSingbox(req Request) ([]byte, error) {
	used := newUsedTags()
	outbounds := make([]any, 0, len(req.Nodes)+1)
	firstTag := ""
	for i, n := range req.Nodes {
		tag := displayName(n, i, used)
		if firstTag == "" {
			firstTag = tag
		}
		ob, err := singboxOutbound(n, tag, i)
		if err != nil {
			return nil, err
		}
		outbounds = append(outbounds, ob)
	}
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "direct"})
	doc := map[string]any{
		"log": map[string]any{"level": "warn"},
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080},
		},
		"outbounds": outbounds,
		"route": map[string]any{
			"rules": []any{map[string]any{"ip_is_private": true, "outbound": "direct"}},
			"final": firstNonEmpty(firstTag, "direct"),
		},
	}
	mergeTemplate(doc, req.Template)
	return json.MarshalIndent(doc, "", "  ")
}

func singboxOutbound(n NodeConfig, tag string, index int) (map[string]any, error) {
	ep := n.Endpoint
	// sing-box 1.14.3 has no xhttp transport: emitting one makes the whole
	// subscription fail to load with "unknown transport type: xhttp".
	if ep.Network == "xhttp" {
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "sing-box", Field: "stream_settings.network",
			Reason: "sing-box 1.14.3 has no xhttp transport"}
	}
	if err := requireNetwork(n, "sing-box", "tcp", "ws", "grpc", "httpupgrade", "http", "quic"); err != nil {
		return nil, err
	}
	if err := requireNoExplicitPin("sing-box", n); err != nil {
		return nil, err
	}
	if err := requireRepresentableTCPHeader(n, "sing-box"); err != nil {
		return nil, err
	}
	if err := requireNoGRPCMultiMode(n, "sing-box"); err != nil {
		return nil, err
	}
	if err := requireShadowsocksTLSRepresentable(n, "sing-box"); err != nil {
		return nil, err
	}
	out := map[string]any{"tag": tag, "server": ep.Address, "server_port": ep.Port}
	switch ep.Protocol {
	case protocol.VLESS:
		out["type"] = "vless"
		out["uuid"] = n.Credential
		out["packet_encoding"] = "xudp"
		if ep.Flow != "" {
			out["flow"] = ep.Flow
		}
	case protocol.VMess:
		out["type"] = "vmess"
		out["uuid"] = n.Credential
		out["security"] = "auto"
		out["alter_id"] = 0
	case protocol.Trojan:
		out["type"] = "trojan"
		out["password"] = n.Credential
	case protocol.Shadowsocks:
		if ep.Cipher == "" {
			return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "sing-box", Field: "cipher", Reason: "shadowsocks method is missing"}
		}
		password, err := shadowsocksPassword("sing-box", n)
		if err != nil {
			return nil, err
		}
		out["type"] = "shadowsocks"
		out["method"] = ep.Cipher
		out["password"] = password
	default:
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "sing-box", Field: "protocol", Reason: "unsupported protocol " + ep.Protocol}
	}
	if ep.Protocol != protocol.Shadowsocks {
		tls, err := singboxTLS(n)
		if err != nil {
			return nil, err
		}
		if tls != nil {
			out["tls"] = tls
		}
	}
	tr, err := singboxTransport(n)
	if err != nil {
		return nil, err
	}
	if tr != nil {
		out["transport"] = tr
	}
	return out, nil
}

func singboxTLS(n NodeConfig) (map[string]any, error) {
	ep := n.Endpoint
	if ep.Security == "" {
		return nil, nil
	}
	tls := map[string]any{"enabled": true}
	if ep.SNI != "" {
		tls["server_name"] = ep.SNI
	}
	if alpn := clientALPN(ep); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	if ep.AllowInsecure {
		tls["insecure"] = true
	}
	if ep.Fingerprint != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": ep.Fingerprint}
	}
	switch ep.Security {
	case "reality":
		if ep.PublicKey == "" || ep.SNI == "" {
			return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "sing-box", Field: "client_settings.public_key", Reason: "REALITY client parameters are incomplete"}
		}
		tls["reality"] = map[string]any{"enabled": true, "public_key": ep.PublicKey, "short_id": ep.ShortID}
	case "tls":
	default:
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "sing-box", Field: "stream_settings.security", Reason: "unsupported security " + ep.Security}
	}
	return tls, nil
}

func singboxTransport(n NodeConfig) (map[string]any, error) {
	ep := n.Endpoint
	switch ep.Network {
	case "tcp":
		return nil, nil
	case "ws":
		tr := map[string]any{"type": "ws", "path": ep.Path}
		if ep.Host != "" {
			tr["headers"] = map[string]any{"Host": ep.Host}
		}
		return tr, nil
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": ep.ServiceName}, nil
	case "httpupgrade":
		return map[string]any{"type": "httpupgrade", "path": ep.Path, "host": ep.Host}, nil
	case "xhttp":
		// Rejected before this switch (sing-box 1.14.3 has no xhttp transport).
		// Kept explicit so a future core upgrade must deliberately re-enable it.
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "sing-box", Field: "stream_settings.network", Reason: "sing-box has no xhttp transport"}
	case "http":
		tr := map[string]any{"type": "http", "path": ep.Path}
		if ep.Host != "" {
			tr["host"] = []string{ep.Host}
		}
		return tr, nil
	case "quic":
		return map[string]any{"type": "quic"}, nil
	case "kcp":
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "sing-box", Field: "stream_settings.network", Reason: "sing-box has no mKCP transport"}
	}
	return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "sing-box", Field: "stream_settings.network", Reason: "unknown transport " + ep.Network}
}

// ─── Mihomo / Clash.Meta ───────────────────────────────────────────────────

func renderMihomo(req Request) ([]byte, error) {
	used := newUsedTags()
	proxies := make([]map[string]any, 0, len(req.Nodes))
	names := make([]string, 0, len(req.Nodes))
	for i, n := range req.Nodes {
		name := displayName(n, i, used)
		p, err := mihomoProxy(n, name)
		if err != nil {
			return nil, err
		}
		proxies = append(proxies, p)
		names = append(names, name)
	}
	groupProxies := append(append([]string{}, names...), "DIRECT")
	doc := map[string]any{
		"mixed-port": 7890,
		"allow-lan":  false,
		"mode":       "rule",
		"log-level":  "info",
		"proxies":    proxies,
		"proxy-groups": []map[string]any{
			{"name": "PROXY", "type": "select", "proxies": groupProxies},
		},
		"rules": []string{
			"GEOIP,private,DIRECT,no-resolve",
			"MATCH,PROXY",
		},
	}
	mergeTemplate(doc, req.Template)
	return yaml.Marshal(doc)
}

func mihomoProxy(n NodeConfig, name string) (map[string]any, error) {
	ep := n.Endpoint
	if err := requireNetwork(n, "mihomo", "tcp", "ws", "grpc", "http"); err != nil {
		return nil, err
	}
	if err := requireNoExplicitPin("mihomo", n); err != nil {
		return nil, err
	}
	if err := requireRepresentableTCPHeader(n, "mihomo"); err != nil {
		return nil, err
	}
	if err := requireNoGRPCMultiMode(n, "mihomo"); err != nil {
		return nil, err
	}
	if err := requireShadowsocksTLSRepresentable(n, "mihomo"); err != nil {
		return nil, err
	}
	p := map[string]any{"name": name, "server": ep.Address, "port": ep.Port, "udp": true}
	switch ep.Protocol {
	case protocol.VLESS:
		p["type"] = "vless"
		p["uuid"] = n.Credential
		if ep.Flow != "" {
			p["flow"] = ep.Flow
		}
	case protocol.VMess:
		p["type"] = "vmess"
		p["uuid"] = n.Credential
		p["alterId"] = 0
		p["cipher"] = "auto"
	case protocol.Trojan:
		// Mihomo always wraps trojan in TLS: a plaintext trojan node cannot be
		// expressed, and emitting "type: trojan" without tls would silently
		// switch the transport.
		if ep.Security == "" {
			return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "mihomo", Field: "stream_settings.security",
				Reason: "Mihomo trojan always uses TLS; plaintext trojan cannot be represented"}
		}
		p["type"] = "trojan"
		p["password"] = n.Credential
	case protocol.Shadowsocks:
		if ep.Cipher == "" {
			return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "mihomo", Field: "cipher", Reason: "shadowsocks method is missing"}
		}
		password, err := shadowsocksPassword("mihomo", n)
		if err != nil {
			return nil, err
		}
		p["type"] = "ss"
		p["cipher"] = ep.Cipher
		p["password"] = password
	default:
		return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "mihomo", Field: "protocol", Reason: "unsupported protocol " + ep.Protocol}
	}
	if ep.Protocol != protocol.Shadowsocks {
		switch ep.Security {
		case "tls":
			p["tls"] = true
			if ep.SNI != "" {
				p["servername"] = ep.SNI
			}
			if alpn := clientALPN(ep); len(alpn) > 0 {
				p["alpn"] = alpn
			}
			if ep.Fingerprint != "" {
				p["client-fingerprint"] = ep.Fingerprint
			}
		case "reality":
			if ep.PublicKey == "" || ep.SNI == "" {
				return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "mihomo", Field: "client_settings.public_key", Reason: "REALITY client parameters are incomplete"}
			}
			p["tls"] = true
			p["servername"] = ep.SNI
			p["client-fingerprint"] = firstNonEmpty(ep.Fingerprint, "chrome")
			opts := map[string]any{"public-key": ep.PublicKey}
			if ep.ShortID != "" {
				opts["short-id"] = ep.ShortID
			}
			p["reality-opts"] = opts
		case "":
		default:
			return nil, &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "mihomo", Field: "stream_settings.security", Reason: "unsupported security " + ep.Security}
		}
	}
	// Mihomo checks the node certificate unless the proxy itself says otherwise;
	// "skip-cert-verify" is its name for client_settings.allow_insecure. It used
	// to be emitted inside the trojan branch only, so a VLESS/VMess ws+tls node
	// whose administrator disabled the check was exported with the check still
	// on and the real core dropped the connection with a deadline exceeded. The
	// setting belongs to every TLS carrying proxy, so it is decided here for all
	// protocols instead of one case.
	if ep.Protocol != protocol.Shadowsocks && ep.Security != "" && ep.AllowInsecure {
		p["skip-cert-verify"] = true
	}
	switch ep.Network {
	case "ws":
		opts := map[string]any{"path": ep.Path}
		if ep.Host != "" {
			opts["headers"] = map[string]any{"Host": ep.Host}
		}
		p["network"] = "ws"
		p["ws-opts"] = opts
	case "grpc":
		p["network"] = "grpc"
		p["grpc-opts"] = map[string]any{"grpc-service-name": ep.ServiceName}
	case "http":
		opts := map[string]any{"path": ep.Path}
		if ep.Host != "" {
			opts["headers"] = map[string]any{"Host": []string{ep.Host}}
		}
		p["network"] = "http"
		p["http-opts"] = opts
	}
	return p, nil
}

// ─── v2rayN share links ────────────────────────────────────────────────────

func renderV2rayN(req Request) ([]byte, error) {
	used := newUsedTags()
	lines := make([]string, 0, len(req.Nodes))
	for i, n := range req.Nodes {
		link, err := shareLink(n, displayName(n, i, used))
		if err != nil {
			return nil, err
		}
		lines = append(lines, link)
	}
	body := strings.Join(lines, "\n") + "\n"
	if req.Base64 {
		return []byte(base64.StdEncoding.EncodeToString([]byte(body))), nil
	}
	return []byte(body), nil
}

func fragment(name string) string {
	return strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
}

func shareLink(n NodeConfig, name string) (string, error) {
	ep := n.Endpoint
	if err := requireNoExplicitPin("v2rayN", n); err != nil {
		return "", err
	}
	if err := requireRepresentableTCPHeader(n, "v2rayN"); err != nil {
		return "", err
	}
	if err := requireShadowsocksTLSRepresentable(n, "v2rayN"); err != nil {
		return "", err
	}
	// A VMess gRPC share link cannot carry serviceName/multiMode reliably: the
	// base64 payload this format uses has no interoperable field for them, and
	// the previous code silently exported net=grpc without the service name,
	// producing a link that could never connect. Reject instead of guessing.
	if ep.Protocol == protocol.VMess && ep.Network == "grpc" {
		return "", &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "v2rayN",
			Field:  "stream_settings.grpcSettings",
			Reason: "VMess gRPC share links cannot express the gRPC service name; use a JSON / sing-box client instead"}
	}
	hostPort := fmt.Sprintf("%s:%d", ep.Address, ep.Port)
	q := url.Values{}
	setIf := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			q.Set(k, v)
		}
	}
	setIf("type", ep.Network)
	setIf("security", ep.Security)
	if ep.Security == "" && ep.Protocol == protocol.Trojan {
		// v2rayN imports a trojan link without a security parameter as TLS;
		// the plaintext node must therefore be stated explicitly.
		q.Set("security", "none")
	}
	if ep.AllowInsecure {
		// v2rayN's own representation of "accept this certificate"; kept so the
		// setting is not silently dropped even though Xray 26 replaced it.
		q.Set("allowInsecure", "1")
	}
	if ep.Security == "reality" && ep.PublicKey == "" {
		return "", &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "v2rayN", Field: "client_settings.public_key", Reason: "REALITY client parameters are incomplete"}
	}
	if ep.Security == "tls" || ep.Security == "reality" {
		setIf("sni", ep.SNI)
		setIf("fp", ep.Fingerprint)
	}
	if ep.Security == "reality" {
		setIf("pbk", ep.PublicKey)
		setIf("sid", ep.ShortID)
		q.Set("spx", "/")
	}
	if alpn := clientALPN(ep); len(alpn) > 0 {
		setIf("alpn", strings.Join(alpn, ","))
	}
	switch ep.Network {
	case "ws", "httpupgrade", "xhttp", "http":
		setIf("path", ep.Path)
		setIf("host", ep.Host)
		if ep.Network == "xhttp" {
			setIf("mode", ep.XHTTPMode)
		}
	case "grpc":
		setIf("serviceName", ep.ServiceName)
		if ep.GRPCMode == "multi" {
			q.Set("mode", "multi")
		}
	case "kcp":
		setIf("headerType", firstNonEmpty(ep.HeaderType, "none"))
		setIf("seed", ep.Seed)
	case "quic":
		setIf("quicSecurity", ep.QuicSecurity)
		setIf("key", ep.QuicKey)
		setIf("headerType", "none")
	}
	switch ep.Protocol {
	case protocol.VLESS:
		q.Set("encryption", "none")
		setIf("flow", ep.Flow)
		return fmt.Sprintf("vless://%s@%s?%s#%s", n.Credential, hostPort, q.Encode(), fragment(name)), nil
	case protocol.Trojan:
		return fmt.Sprintf("trojan://%s@%s?%s#%s", url.QueryEscape(n.Credential), hostPort, q.Encode(), fragment(name)), nil
	case protocol.VMess:
		payload := map[string]string{
			"v": "2", "ps": name, "add": ep.Address, "port": fmt.Sprintf("%d", ep.Port),
			"id": n.Credential, "aid": "0", "scy": "auto", "net": ep.Network,
			"type": firstNonEmpty(ep.HeaderType, "none"), "host": ep.Host, "path": ep.Path,
			"tls": ep.Security, "sni": ep.SNI,
		}
		if alpn := clientALPN(ep); len(alpn) > 0 {
			payload["alpn"] = strings.Join(alpn, ",")
		}
		if ep.Fingerprint != "" {
			payload["fp"] = ep.Fingerprint
		}
		if ep.AllowInsecure {
			// Same representation v2rayN uses for a self-signed node.
			payload["allowInsecure"] = "1"
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(raw), nil
	case protocol.Shadowsocks:
		if ep.Cipher == "" {
			return "", &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "v2rayN", Field: "cipher", Reason: "shadowsocks method is missing"}
		}
		password, err := shadowsocksPassword("v2rayN", n)
		if err != nil {
			return "", err
		}
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(ep.Cipher + ":" + password))
		return fmt.Sprintf("ss://%s@%s#%s", userinfo, hostPort, fragment(name)), nil
	}
	return "", &UnsupportedError{NodeName: ep.Name, NodeID: ep.NodeID, Client: "v2rayN", Field: "protocol", Reason: "unsupported protocol " + ep.Protocol}
}
