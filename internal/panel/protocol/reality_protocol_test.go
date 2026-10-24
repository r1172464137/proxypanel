package protocol

// REALITY 是 Xray 的 VLESS 专属传输安全层。
//
// 之前只校验了 REALITY 的参数是否齐全（dest/serverNames/privateKey/客户端公钥），
// 没有校验协议本身。结果：VMess / Trojan / Shadowsocks 配上 reality 也能通过
// 校验并落库，但这些节点在核心侧根本没有对应实现 —— 存进去的是一条永远无法
// 加载的配置，只会在下发给 Agent 时才失败。

import (
	"strings"
	"testing"

	"proxypanel/internal/panel/domain"
)

func realityNodeWithProto(t *testing.T, proto string) domain.Node {
	t.Helper()
	stream := domain.NewObject()
	_ = stream.Set("network", "tcp")
	_ = stream.Set("security", "reality")
	rs := domain.NewObject()
	_ = rs.Set("dest", "www.example.com:443")
	_ = rs.Set("serverNames", []any{"www.example.com"})
	_ = rs.Set("privateKey", "dwdtCnMYpX08FsFyUbJmRd9ML4frwJkqsXf7pR25LCo")
	_ = rs.Set("shortIds", []any{"abcd1234"})
	_ = stream.Set("realitySettings", rs)

	client := domain.NewObject()
	_ = client.Set("server_name", "www.example.com")
	_ = client.Set("public_key", "hSDwCYkwp1R0i33ctD73Wg2_Og0mOBr066SpjqqbTmo")
	_ = client.Set("short_id", "abcd1234")

	ps := domain.NewObject()
	if proto == "shadowsocks" {
		_ = ps.Set("method", "aes-128-gcm")
	}
	if proto == "vless" {
		_ = ps.Set("decryption", "none")
	}
	return domain.Node{
		ID: "nd_t", Name: "t", Protocol: proto,
		Host: "1.2.3.4", Port: 443, Listen: "0.0.0.0",
		StreamSettings: stream, ProtocolSettings: ps, ClientSettings: client,
	}
}

func TestRealityRejectedForNonVLESSProtocols(t *testing.T) {
	// 参数齐全的非 VLESS 协议也必须被拒绝：核心不支持
	for _, proto := range []string{"vmess", "trojan", "shadowsocks"} {
		t.Run(proto, func(t *testing.T) {
			_, issues := EndpointFor(realityNodeWithProto(t, proto))
			if len(issues) == 0 {
				t.Fatalf("%s + REALITY 必须被拒绝", proto)
			}
			found := false
			for _, is := range issues {
				if strings.Contains(is.Message, "REALITY") && strings.Contains(is.Message, "VLESS") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s 的错误信息应说明 REALITY 只能用于 VLESS，实际: %v", proto, issues)
			}
		})
	}
}

func TestRealityAcceptedForVLESS(t *testing.T) {
	// VLESS + REALITY 参数齐全时不应报错
	_, issues := EndpointFor(realityNodeWithProto(t, "vless"))
	if len(issues) != 0 {
		t.Fatalf("VLESS + REALITY 不应报错: %v", issues)
	}
}

func TestRealityWithoutParametersStillReportsFields(t *testing.T) {
	// 校验顺序：协议错误与参数缺失都要报，不能因为协议对了就漏参数检查
	n := realityNodeWithProto(t, "vmess")
	empty := domain.NewObject()
	_ = empty.Set("network", "tcp")
	_ = empty.Set("security", "reality")
	n.StreamSettings = empty
	n.ClientSettings = domain.NewObject()
	_, issues := EndpointFor(n)
	if len(issues) < 2 {
		t.Fatalf("应同时报协议约束与参数缺失，实际只有: %v", issues)
	}
}

// Validate 是节点创建/更新走的校验路径，必须同样拒绝非 VLESS + REALITY。
// （EndpointFor 是导出路径，两处都要拦，否则一条路径漏了就会落库。）
func TestValidateRejectsRealityForNonVLESS(t *testing.T) {
	for _, proto := range []string{"vmess", "trojan", "shadowsocks"} {
		issues := Validate(toProtocolNode(realityNodeWithProto(t, proto)))
		if len(issues) == 0 {
			t.Fatalf("Validate: %s + REALITY 必须被拒绝", proto)
		}
		found := false
		for _, is := range issues {
			if strings.Contains(is.Message, "REALITY") && strings.Contains(is.Message, "VLESS") {
				found = true
			}
		}
		if !found {
			t.Fatalf("Validate: %s 的错误应说明 REALITY 只能配 VLESS，实际: %v", proto, issues)
		}
	}
	// VLESS + REALITY 仍应通过
	if issues := Validate(toProtocolNode(realityNodeWithProto(t, "vless"))); len(issues) != 0 {
		t.Fatalf("Validate: VLESS + REALITY 不应报错: %v", issues)
	}
}

// toProtocolNode 把 domain.Node 转成 Validate 需要的 protocol.Node。
func toProtocolNode(n domain.Node) Node {
	return Node{
		Protocol:         n.Protocol,
		Flow:             n.Flow,
		Cipher:           n.Cipher,
		Listen:           n.Listen,
		Host:             n.Host,
		Port:             n.Port,
		ExternalPort:     n.ExternalPort,
		StreamSettings:   n.StreamSettings,
		ProtocolSettings: n.ProtocolSettings,
		ClientSettings:   n.ClientSettings,
		ServerKey:        n.ServerKey,
	}
}
