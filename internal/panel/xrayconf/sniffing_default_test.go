package xrayconf

import (
	"encoding/json"
	"strings"
	"testing"

	"proxypanel/internal/panel/domain"
)

// sniffing used to be hard-coded in the generated inbound. A node with an empty
// sniffing object must keep producing exactly that, so upgrading the panel never
// changes the config of existing nodes.
func TestEmptySniffingKeepsPreviousDefault(t *testing.T) {
	got := sniffingObject(domain.NewObject())
	if enabled := boolField(t, got, "enabled"); enabled != true {
		t.Fatalf("enabled = %v, want true", enabled)
	}
	raw, _ := got.Get("destOverride")
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if strings.Join(list, ",") != "http,tls,quic" {
		t.Fatalf("destOverride = %v", list)
	}
}

// An explicit object, including {"enabled":false}, is honoured as written.
func TestExplicitSniffingIsHonoured(t *testing.T) {
	sniffing := domain.NewObject()
	_ = sniffing.Set("enabled", false)
	_ = sniffing.Set("routeOnly", true)
	got := sniffingObject(sniffing)
	if enabled := boolField(t, got, "enabled"); enabled != false {
		t.Fatalf("enabled = %v, want false", enabled)
	}
	if routeOnly := boolField(t, got, "routeOnly"); routeOnly != true {
		t.Fatalf("routeOnly = %v, want true", routeOnly)
	}
}

// The generated inbound carries the three inbound-level objects at the same level
// as streamSettings/settings, which is where Xray expects them.
func TestBuildEmitsInboundLevelObjects(t *testing.T) {
	stream := obj(t, `{"network":"tcp","security":"none"}`)
	sniffing := domain.NewObject()
	_ = sniffing.Set("enabled", false)
	allocate := domain.NewObject()
	_ = allocate.Set("strategy", "always")
	_ = allocate.Set("refresh", 5)
	node := domain.Node{
		ID: "nd_test", Name: "n", Listen: "0.0.0.0", Port: 20101,
		Protocol: "vless", StreamSettings: stream, ProtocolSettings: domain.NewObject(),
		ClientSettings: domain.NewObject(), Sniffing: sniffing, Allocate: allocate,
		ExternalProxy: []any{map[string]any{"forceTls": "tls", "dest": "jp.example.com", "port": 443, "remark": "jp"}},
	}
	raw, err := Build(node, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Inbounds []map[string]json.RawMessage `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	in := doc.Inbounds[0]
	for _, key := range []string{"sniffing", "allocate", "externalProxy"} {
		if _, ok := in[key]; !ok {
			t.Errorf("inbound is missing %q; got keys %v", key, keysOf(in))
		}
	}
	if _, nested := in["inboundSettings"]; nested {
		t.Error("inbound must not nest these under a custom object")
	}
}

// allocate/externalProxy are omitted when unset: an empty allocate object is not
// the same as "no allocate" for the core.
func TestBuildOmitsUnsetAllocateAndExternalProxy(t *testing.T) {
	node := domain.Node{
		ID: "nd_test", Name: "n", Listen: "0.0.0.0", Port: 20101,
		Protocol: "vless", StreamSettings: obj(t, `{"network":"tcp","security":"none"}`),
		ProtocolSettings: domain.NewObject(), ClientSettings: domain.NewObject(),
	}
	raw, err := Build(node, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Inbounds []map[string]json.RawMessage `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	in := doc.Inbounds[0]
	for _, key := range []string{"allocate", "externalProxy"} {
		if _, ok := in[key]; ok {
			t.Errorf("%q must be absent when unset", key)
		}
	}
	if _, ok := in["sniffing"]; !ok {
		t.Error("sniffing must always be present (it has a default)")
	}
}

// boolField decodes a boolean out of a domain.Object; Object.String only handles
// JSON strings, so booleans have to be unmarshalled.
func boolField(t *testing.T, o domain.Object, key string) bool {
	t.Helper()
	raw, ok := o.Get(key)
	if !ok {
		t.Fatalf("%q missing from %+v", key, o)
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%q is not a boolean: %v", key, err)
	}
	return v
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
