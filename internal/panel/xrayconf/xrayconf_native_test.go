package xrayconf

import (
	"encoding/json"
	"testing"

	"proxypanel/internal/panel/domain"
)

const nativeStream = `{
  "network": "tcp",
  "security": "reality",
  "tcpSettings": {"acceptProxyProtocol": true, "header": {"type": "http",
    "request": {"version": "1.1", "method": "GET", "path": ["/"], "headers": {"Host": ["www.example.invalid"]}},
    "response": {"version": "1.1", "status": "200", "reason": "OK"}}},
  "realitySettings": {"dest": "www.example.invalid:443", "xver": 1,
    "serverNames": ["www.example.invalid"], "minClientVer": "1.8.0", "maxTimeDiff": 10000,
    "shortIds": ["0123abcd"], "fingerprint": "chrome", "spiderX": "/"},
  "sockopt": {"mark": 255, "tcpFastOpen": true, "tcpNoDelay": true, "tcpUserTimeout": 10000,
    "domainStrategy": "UseIP", "tcpMptcp": true}
}`

const nativeProtocol = `{
  "decryption": "none",
  "fallbacks": [{"name": "h2", "dest": 8443, "xver": 1, "alpn": "h2"}, {"path": "/ws", "dest": 8080}]
}`

func obj(t *testing.T, raw string) domain.Object {
	t.Helper()
	var o domain.Object
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("bad object: %v", err)
	}
	return o
}

func TestNativeVLESSFieldsReachTheCoreDocument(t *testing.T) {
	node := domain.Node{
		ID: "nd_native", Name: "native", Protocol: "vless", Flow: "xtls-rprx-vision",
		Host: "node.example.invalid", Port: 443, Listen: "0.0.0.0", ServerKey: "SERVER_PRIVATE_KEY",
		StreamSettings:   obj(t, nativeStream),
		ProtocolSettings: obj(t, nativeProtocol),
	}
	raw, err := Build(node, []Client{{Email: ClientEmail("mbr_a"),
		UUID: "11111111-1111-4111-8111-111111111111", Flow: "xtls-rprx-vision"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("json: %v", err)
	}
	inbound := doc["inbounds"].([]any)[0].(map[string]any)
	settings := inbound["settings"].(map[string]any)
	fallbacks, ok := settings["fallbacks"].([]any)
	if !ok || len(fallbacks) != 2 {
		t.Fatalf("fallbacks lost: %v", settings["fallbacks"])
	}
	stream := inbound["streamSettings"].(map[string]any)
	tcp := stream["tcpSettings"].(map[string]any)
	header := tcp["header"].(map[string]any)
	request := header["request"].(map[string]any)
	if request["method"] != "GET" || tcp["acceptProxyProtocol"] != true {
		t.Fatalf("tcpSettings lost fields: %v", tcp)
	}
	reality := stream["realitySettings"].(map[string]any)
	if reality["privateKey"] != "SERVER_PRIVATE_KEY" {
		t.Fatal("private key must be re-injected for the core only")
	}
	if reality["minClientVer"] != "1.8.0" || reality["maxTimeDiff"].(float64) != 10000 {
		t.Fatalf("realitySettings lost fields: %v", reality)
	}
	sock := stream["sockopt"].(map[string]any)
	if sock["tcpMptcp"] != true || sock["domainStrategy"] != "UseIP" {
		t.Fatalf("sockopt lost fields: %v", sock)
	}
	clients := settings["clients"].([]any)
	if clients[0].(map[string]any)["flow"] != "xtls-rprx-vision" {
		t.Fatalf("client flow missing: %v", clients[0])
	}
}
