package xrayconf

import (
	"encoding/json"
	"strings"
	"testing"

	"proxypanel/internal/panel/domain"
)

func node(t *testing.T) domain.Node {
	return domain.Node{
		ID: "nd_1", Name: "tokyo", Protocol: "vless", Flow: "xtls-rprx-vision",
		Host: "node.example.invalid", Port: 443, Listen: "0.0.0.0",
		ServerKey:        "SERVER_PRIVATE_KEY",
		StreamSettings:   obj(t, `{"network":"tcp","security":"reality","realitySettings":{"shortIds":["ab"],"serverNames":["www.example.invalid"]},"sockopt":{"tcpFastOpen":true}}`),
		ProtocolSettings: obj(t, `{"decryption":"none","fallbacks":[{"dest":80}]}`),
	}
}

func TestBuildInjectsClientsAndReinjectsPrivateKey(t *testing.T) {
	raw, err := Build(node(t), []Client{
		{Email: ClientEmail("mbr_b"), UUID: "11111111-1111-4111-8111-111111111111", Flow: "xtls-rprx-vision"},
		{Email: ClientEmail("mbr_a"), UUID: "22222222-2222-4222-8222-222222222222", Flow: "xtls-rprx-vision"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("json: %v", err)
	}
	for _, banned := range []string{"api", "stats", "policy"} {
		if _, ok := doc[banned]; ok {
			t.Fatalf("document must not contain %q (the agent injects its local management API)", banned)
		}
	}
	inbounds := doc["inbounds"].([]any)
	inbound := inbounds[0].(map[string]any)
	if inbound["protocol"] != "vless" || inbound["port"].(float64) != 443 {
		t.Fatalf("inbound = %v", inbound)
	}
	settings := inbound["settings"].(map[string]any)
	if settings["decryption"] != "none" {
		t.Fatalf("decryption = %v", settings["decryption"])
	}
	if _, ok := settings["fallbacks"]; !ok {
		t.Fatal("extended protocol settings must be preserved")
	}
	clients := settings["clients"].([]any)
	if len(clients) != 2 {
		t.Fatalf("clients = %v", clients)
	}
	first := clients[0].(map[string]any)
	if first["email"] != ClientEmail("mbr_a") {
		t.Fatalf("clients must be sorted by email for stable hashes: %v", clients)
	}
	if first["flow"] != "xtls-rprx-vision" {
		t.Fatalf("flow not applied: %v", first)
	}
	stream := inbound["streamSettings"].(map[string]any)
	reality := stream["realitySettings"].(map[string]any)
	if reality["privateKey"] != "SERVER_PRIVATE_KEY" {
		t.Fatalf("server private key must be re-injected for the core: %v", reality)
	}
	if _, ok := stream["sockopt"]; !ok {
		t.Fatal("sockopt must be preserved")
	}
	if strings.Contains(string(raw), "clients\":null") {
		t.Fatal("clients must always be an array")
	}
}

func TestBuildEmptyClientsIsExplicitEmptyArray(t *testing.T) {
	raw, err := Build(node(t), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	settings := doc["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
	clients, ok := settings["clients"].([]any)
	if !ok {
		t.Fatalf("clients must be an array: %v", settings["clients"])
	}
	if len(clients) != 0 {
		t.Fatalf("clients = %v", clients)
	}
}

func TestBuildRejectsAdminClients(t *testing.T) {
	n := node(t)
	n.ProtocolSettings = obj(t, `{"clients":[{"id":"x"}]}`)
	if _, err := Build(n, nil); err == nil {
		t.Fatal("admin supplied clients must be rejected")
	}
}

func TestBuildShadowsocks2022(t *testing.T) {
	n := domain.Node{
		ID: "nd_ss", Name: "ss", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm",
		Host: "ss.example.invalid", Port: 8388, Listen: "0.0.0.0", ServerKey: "SERVER_PSK",
		StreamSettings:   obj(t, `{"network":"tcp","security":"none"}`),
		ProtocolSettings: domain.NewObject(),
	}
	raw, err := Build(n, []Client{{Email: ClientEmail("mbr_a"), Password: "USER_PSK"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	settings := doc["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
	if settings["method"] != "2022-blake3-aes-128-gcm" || settings["password"] != "SERVER_PSK" {
		t.Fatalf("settings = %v", settings)
	}
	if settings["network"] != "tcp,udp" {
		t.Fatalf("ss network default = %v", settings["network"])
	}
}

func TestBuildRequiresUUID(t *testing.T) {
	if _, err := Build(node(t), []Client{{Email: "m-x"}}); err == nil {
		t.Fatal("client without uuid must fail")
	}
}

// Legacy Shadowsocks multi-user inbounds must carry the method on every client:
// Xray 26.3.27 refuses the whole inbound with "unsupported cipher method:" when
// a client has none, and 2022 inbounds must leave it empty.
func TestBuildShadowsocksClientsCarryMethod(t *testing.T) {
	legacy := domain.Node{
		ID: "nd_ss", Name: "ss", Protocol: "shadowsocks", Cipher: "aes-128-gcm",
		Host: "ss.example.invalid", Port: 8388, Listen: "0.0.0.0",
		StreamSettings:   obj(t, `{"network":"tcp","security":"none"}`),
		ProtocolSettings: domain.NewObject(),
	}
	raw, err := Build(legacy, []Client{
		{Email: ClientEmail("mbr_a"), Password: "user-a"},
		{Email: ClientEmail("mbr_b"), Password: "user-b"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	settings := settingsOf(t, raw)
	if _, ok := settings["password"]; ok {
		t.Fatal("legacy shadowsocks must not gain a top level password")
	}
	clients := settings["clients"].([]any)
	if len(clients) != 2 {
		t.Fatalf("clients = %v", clients)
	}
	for _, raw := range clients {
		client := raw.(map[string]any)
		if client["method"] != "aes-128-gcm" {
			t.Fatalf("legacy client without method: %v", client)
		}
		if client["password"] == "" || client["email"] == "" {
			t.Fatalf("client lost its credential: %v", client)
		}
	}

	ss2022 := legacy
	ss2022.Cipher = "2022-blake3-aes-128-gcm"
	ss2022.ServerKey = "SERVER_PSK"
	raw, err = Build(ss2022, []Client{{Email: ClientEmail("mbr_a"), Password: "user-a"}})
	if err != nil {
		t.Fatalf("build 2022: %v", err)
	}
	settings = settingsOf(t, raw)
	if settings["password"] != "SERVER_PSK" {
		t.Fatalf("2022 server password = %v", settings["password"])
	}
	if _, ok := settings["clients"].([]any)[0].(map[string]any)["method"]; ok {
		t.Fatal("2022 multi-user clients must not carry a method")
	}
}

func settingsOf(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
}
