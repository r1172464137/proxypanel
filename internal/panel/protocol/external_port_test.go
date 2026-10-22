package protocol

// 对外端口（external_port）：节点实际监听 A 端口，但客户端连 B 端口。
//
// 用于「节点跑在 20101，对外暴露 443」这类中转场景。端口转发由运维在机器上
// 自行配置，面板只负责把对外地址写进成员配置。

import (
	"strings"
	"testing"

	"proxypanel/internal/panel/domain"
)

func vlessNodeWithPorts(t *testing.T, port, externalPort int) domain.Node {
	t.Helper()
	stream := domain.NewObject()
	_ = stream.Set("network", "tcp")
	_ = stream.Set("security", "none")
	ps := domain.NewObject()
	_ = ps.Set("decryption", "none")
	return domain.Node{
		ID: "nd_ep", Name: "ep", Protocol: VLESS,
		Host: "relay.example.com", Port: port, ExternalPort: externalPort,
		Listen: "0.0.0.0", StreamSettings: stream, ProtocolSettings: ps,
		ClientSettings: domain.NewObject(),
	}
}

func TestEndpointUsesExternalPortForClients(t *testing.T) {
	// 未设对外端口：客户端看到实际端口
	ep, issues := EndpointFor(vlessNodeWithPorts(t, 20101, 0))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
	if ep.Port != 20101 {
		t.Fatalf("未设 external_port 时客户端端口应为实际端口，got %d", ep.Port)
	}

	// 设了对外端口：客户端看到的是它，服务端仍监听原端口
	ep2, issues2 := EndpointFor(vlessNodeWithPorts(t, 20101, 443))
	if len(issues2) != 0 {
		t.Fatalf("unexpected issues: %v", issues2)
	}
	if ep2.Port != 443 {
		t.Fatalf("客户端端口应为 external_port 443，got %d", ep2.Port)
	}
	if ep2.Address != "relay.example.com" {
		t.Fatalf("客户端地址应为 host，got %q", ep2.Address)
	}
}

func TestExternalPortValidation(t *testing.T) {
	// 与监听端口相同：拒绝（没有分离意义，容易误配）
	issues := Validate(toProtocolNode(vlessNodeWithPorts(t, 443, 443)))
	if len(issues) == 0 {
		t.Fatal("external_port 与 port 相同应被拒绝")
	}
	found := false
	for _, is := range issues {
		if strings.Contains(is.Field, "external_port") {
			found = true
		}
	}
	if !found {
		t.Fatalf("错误应指向 external_port，实际: %v", issues)
	}

	// 越界：拒绝
	for _, bad := range []int{-1, 70000} {
		if len(Validate(toProtocolNode(vlessNodeWithPorts(t, 20101, bad)))) == 0 {
			t.Fatalf("external_port=%d 应被拒绝", bad)
		}
	}

	// 合法值：通过
	if issues := Validate(toProtocolNode(vlessNodeWithPorts(t, 20101, 8443))); len(issues) != 0 {
		t.Fatalf("合法的 external_port 应通过: %v", issues)
	}
}
