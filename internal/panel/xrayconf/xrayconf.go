// Package xrayconf builds the complete declarative Xray document for one node
// instance. The panel owns generation; the agent only adds its local
// management API, stats/policy and verification (contract: Instance.Config is a
// full Xray document without a management API).
package xrayconf

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
)

// Client is one injected inbound user. Email is the stable member mapping the
// agent also receives in Snapshot.Instances[].Users.
type Client struct {
	MemberID string
	Email    string
	UUID     string
	Password string
	Flow     string
}

// ClientEmail is the stable member -> inbound client email mapping.
func ClientEmail(memberID string) string { return "m-" + memberID }

// Build returns the full Xray configuration for a node with the given clients.
func Build(node domain.Node, clients []Client) (json.RawMessage, error) {
	proto := strings.ToLower(strings.TrimSpace(node.Protocol))
	stream := node.StreamSettings.Clone()
	if stream == nil {
		stream = domain.NewObject()
	}
	// Historical rows persisted before certificate normalization may still carry
	// certificateFile/keyFile directly on tlsSettings, which the frozen core
	// ignores. Migrate them here as well so an already-stored node generates a
	// usable document without an administrator re-saving it.
	if security, _ := stream.String("security"); strings.EqualFold(strings.TrimSpace(security), "tls") {
		if issues := protocol.NormalizeTLS(stream); len(issues) > 0 {
			return nil, fmt.Errorf("tls settings: %s", issues[0])
		}
	}
	ps := node.ProtocolSettings.Clone()
	if ps == nil {
		ps = domain.NewObject()
	}
	if ps.Has("clients") {
		return nil, fmt.Errorf("protocol_settings.clients must not be set by an administrator")
	}

	// Re-inject server only secrets that are encrypted at rest.
	if node.ServerKey != "" {
		switch {
		case strings.EqualFold(ep(stream), "reality") && proto == protocol.VLESS:
			rs, ok := stream.Sub("realitySettings")
			if !ok {
				rs = domain.NewObject()
			}
			if err := rs.Set("privateKey", node.ServerKey); err != nil {
				return nil, err
			}
			if err := stream.Set("realitySettings", rs); err != nil {
				return nil, err
			}
		case proto == protocol.Shadowsocks:
			if err := ps.Set("password", node.ServerKey); err != nil {
				return nil, err
			}
		}
	}

	// Shadowsocks needs its method before the clients are built: legacy (non
	// 2022) multi-user inbounds carry the method on every client, while 2022
	// multi-user inbounds must leave the per-client method empty.
	ssMethod := ""
	if proto == protocol.Shadowsocks {
		ssMethod = strings.ToLower(strings.TrimSpace(node.Cipher))
		if ssMethod == "" {
			if m, ok := ps.String("method"); ok {
				ssMethod = strings.ToLower(strings.TrimSpace(m))
			}
		}
		if ssMethod == "" {
			return nil, fmt.Errorf("shadowsocks method is required")
		}
	}

	inboundClients := make([]domain.Object, 0, len(clients))
	seen := map[string]bool{}
	sorted := append([]Client(nil), clients...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Email < sorted[j].Email })
	for _, c := range sorted {
		if c.Email == "" {
			return nil, fmt.Errorf("client without email")
		}
		if seen[c.Email] {
			return nil, fmt.Errorf("duplicate client email %q", c.Email)
		}
		seen[c.Email] = true
		obj := domain.NewObject()
		if err := obj.Set("email", c.Email); err != nil {
			return nil, err
		}
		switch proto {
		case protocol.VLESS, protocol.VMess:
			if c.UUID == "" {
				return nil, fmt.Errorf("client %q has no uuid", c.Email)
			}
			if err := obj.Set("id", c.UUID); err != nil {
				return nil, err
			}
			if proto == protocol.VMess {
				if err := obj.Set("alterId", 0); err != nil {
					return nil, err
				}
				if err := obj.Set("security", "auto"); err != nil {
					return nil, err
				}
			}
			if flow := strings.TrimSpace(c.Flow); flow != "" && proto == protocol.VLESS {
				if err := obj.Set("flow", flow); err != nil {
					return nil, err
				}
			}
		case protocol.Trojan, protocol.Shadowsocks:
			if c.Password == "" {
				return nil, fmt.Errorf("client %q has no password", c.Email)
			}
			if proto == protocol.Shadowsocks && !protocol.IsSS2022(ssMethod) {
				// Xray 26.3.27 reads the cipher from each client of a multi-user
				// inbound; a missing method fails the whole inbound with
				// "unsupported cipher method:" (empty). 2022 inbounds are the
				// opposite: they reject a per-client method.
				if err := obj.Set("method", ssMethod); err != nil {
					return nil, err
				}
			}
			if err := obj.Set("password", c.Password); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported protocol %q", node.Protocol)
		}
		inboundClients = append(inboundClients, obj)
	}

	settings := domain.NewObject()
	for k, v := range ps {
		settings[k] = v
	}
	switch proto {
	case protocol.VLESS:
		if _, ok := settings.String("decryption"); !ok {
			if err := settings.Set("decryption", "none"); err != nil {
				return nil, err
			}
		}
	case protocol.Shadowsocks:
		if err := settings.Set("method", ssMethod); err != nil {
			return nil, err
		}
		if _, ok := settings.String("network"); !ok {
			if err := settings.Set("network", "tcp,udp"); err != nil {
				return nil, err
			}
		}
	}
	if err := settings.Set("clients", inboundClients); err != nil {
		return nil, err
	}

	inbound := domain.NewObject()
	if err := inbound.Set("tag", "node"); err != nil {
		return nil, err
	}
	if err := inbound.Set("listen", node.Listen); err != nil {
		return nil, err
	}
	if err := inbound.Set("port", node.Port); err != nil {
		return nil, err
	}
	if err := inbound.Set("protocol", proto); err != nil {
		return nil, err
	}
	if err := inbound.Set("settings", settings); err != nil {
		return nil, err
	}
	if err := inbound.Set("streamSettings", stream); err != nil {
		return nil, err
	}
	// Inbound-level objects, in the same shape Xray and 3x-ui use. sniffing used to
	// be hard-coded here; the panel now stores it per node, and an empty object
	// still yields the previous behaviour so upgraded nodes generate identical
	// configs. allocate/externalProxy are emitted only when the operator set them,
	// because an empty allocate object is not equivalent to "no allocate".
	if err := inbound.Set("sniffing", sniffingObject(node.Sniffing)); err != nil {
		return nil, err
	}
	if len(node.Allocate) > 0 {
		if err := inbound.Set("allocate", node.Allocate); err != nil {
			return nil, err
		}
	}
	if len(node.ExternalProxy) > 0 {
		if err := inbound.Set("externalProxy", node.ExternalProxy); err != nil {
			return nil, err
		}
	}

	doc := map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": []any{inbound},
		"outbounds": []any{
			map[string]any{"tag": "direct", "protocol": "freedom"},
			map[string]any{"tag": "block", "protocol": "blackhole"},
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules": []any{
				map[string]any{"type": "field", "ip": []string{"geoip:private"}, "outboundTag": "direct"},
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func ep(stream domain.Object) string {
	v, _ := stream.String("security")
	return v
}

// sniffingObject returns the inbound sniffing object. Historically the panel
// always emitted {enabled:true, destOverride:[http,tls,quic]}; keeping that as the
// default for an empty object means existing nodes keep generating the same
// config, while an explicit object (including {"enabled":false}) is honoured
// field by field.
func sniffingObject(o domain.Object) domain.Object {
	if len(o) == 0 {
		return domain.Object{
			"enabled":      json.RawMessage(`true`),
			"destOverride": json.RawMessage(`["http","tls","quic"]`),
		}
	}
	return o
}
