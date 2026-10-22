package protocol

// TLS 入站必须带证书材料。
//
// 之前 Validate 只要求"证书或客户端域名二选一"，于是「TLS + 只填客户端域名」
// 也能通过校验并落库 —— 节点在列表里看起来正常，实际核心启动时报
// "no certificates configured"，只在下发到 Agent 时才失败。
//
// 客户端域名是给成员配置用的，替代不了服务端证书。

import (
	"strings"
	"testing"

	"proxypanel/internal/panel/domain"
)

func tlsNode(t *testing.T, tlsSettings domain.Object) Node {
	t.Helper()
	stream := domain.NewObject()
	_ = stream.Set("network", "tcp")
	_ = stream.Set("security", "tls")
	_ = stream.Set("tlsSettings", tlsSettings)
	client := domain.NewObject()
	_ = client.Set("server_name", "n.example.invalid")
	return Node{
		Protocol: VLESS, Host: "n.example.invalid", Port: 443, Listen: "0.0.0.0",
		StreamSettings:   stream,
		ProtocolSettings: obj(t, `{"decryption":"none"}`),
		ClientSettings:   client,
	}
}

func TestTLSRequiresCertificateMaterial(t *testing.T) {
	// 只有客户端域名、没有证书 → 必须拒绝
	noCert := domain.NewObject()
	_ = noCert.Set("serverName", "n.example.invalid")
	issues := Validate(tlsNode(t, noCert))
	if len(issues) == 0 {
		t.Fatal("TLS 没有证书材料时必须被拒绝")
	}
	found := false
	for _, is := range issues {
		if strings.Contains(is.Field, "certificates") {
			found = true
		}
	}
	if !found {
		t.Fatalf("错误应指向证书字段，实际: %v", issues)
	}
}

func TestTLSAcceptsInlineOrFileCertificates(t *testing.T) {
	// 内联 certificates 数组
	inline := obj(t, `{"serverName":"n.example.invalid","certificates":[{"certificate":["-----BEGIN CERTIFICATE-----"],"key":["-----BEGIN PRIVATE KEY-----"]}]}`)
	if issues := Validate(tlsNode(t, inline)); len(issues) != 0 {
		t.Fatalf("内联证书应通过: %v", issues)
	}
	// 文件路径
	files := obj(t, `{"serverName":"n.example.invalid","certificateFile":"/etc/xray/fullchain.pem","keyFile":"/etc/xray/privkey.pem"}`)
	if issues := Validate(tlsNode(t, files)); len(issues) != 0 {
		t.Fatalf("证书文件路径应通过: %v", issues)
	}
	// 只有 certFile 没有 keyFile 仍算不完整
	half := obj(t, `{"serverName":"n.example.invalid","certificateFile":"/etc/xray/fullchain.pem"}`)
	if issues := Validate(tlsNode(t, half)); len(issues) == 0 {
		t.Fatal("只有 certificateFile 没有 keyFile 必须被拒绝")
	}
	// 空数组也不算有证书
	empty := obj(t, `{"serverName":"n.example.invalid","certificates":[]}`)
	if issues := Validate(tlsNode(t, empty)); len(issues) == 0 {
		t.Fatal("certificates 为空数组必须被拒绝")
	}
}
