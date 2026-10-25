package protocol

import (
	"encoding/json"
	"testing"

	"proxypanel/internal/panel/domain"
)

func objFrom(t *testing.T, raw string) domain.Object {
	t.Helper()
	o := domain.NewObject()
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("bad fixture %q: %v", raw, err)
	}
	return o
}

// Unknown keys must survive: the JSON editor is the escape hatch for everything
// the structured form does not model yet.
func TestUnknownInboundKeysSurvive(t *testing.T) {
	sniffing, allocate, proxy, issues := SanitizeInbound(
		objFrom(t, `{"enabled":true,"someFutureKey":{"nested":1}}`),
		objFrom(t, `{"strategy":"always","futureAllocate":7}`),
		[]any{map[string]any{"forceTls": "same", "dest": "example.com", "port": 443, "remark": "hk", "extra": true}},
	)
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	if !sniffing.Has("someFutureKey") {
		t.Error("unknown sniffing key dropped")
	}
	if !allocate.Has("futureAllocate") {
		t.Error("unknown allocate key dropped")
	}
	if len(proxy) != 1 || proxy[0].(map[string]any)["extra"] != true {
		t.Errorf("external_proxy entry altered: %+v", proxy)
	}
}

// Shapes the core would refuse must be rejected with the exact field, not written
// into a config that fails at start-up.
func TestInboundRejectsBadShapes(t *testing.T) {
	cases := []struct {
		name    string
		sniff   string
		alloc   string
		proxy   []any
		wantFld string
	}{
		{"unknown destination", `{"destOverride":["http","dns"]}`, "", nil, "sniffing.destOverride"},
		{"destOverride not array", `{"destOverride":"http"}`, "", nil, "sniffing.destOverride"},
		{"enabled not bool", `{"enabled":"yes"}`, "", nil, "sniffing.enabled"},
		{"routeOnly not bool", `{"routeOnly":1}`, "", nil, "sniffing.routeOnly"},
		{"bad strategy", "", `{"strategy":"sometimes"}`, nil, "allocate.strategy"},
		{"negative refresh", "", `{"refresh":-1}`, nil, "allocate.refresh"},
		{"proxy not object", "", "", []any{"example.com:443"}, "external_proxy"},
		{"proxy missing dest", "", "", []any{map[string]any{"port": 443}}, "external_proxy"},
		{"proxy bad port", "", "", []any{map[string]any{"dest": "a.example", "port": 0}}, "external_proxy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sniffing, allocate domain.Object
			if c.sniff != "" {
				sniffing = objFrom(t, c.sniff)
			}
			if c.alloc != "" {
				allocate = objFrom(t, c.alloc)
			}
			_, _, _, issues := SanitizeInbound(sniffing, allocate, c.proxy)
			if len(issues) == 0 {
				t.Fatalf("accepted invalid input")
			}
			if issues[0].Field != c.wantFld {
				t.Fatalf("issue field = %q, want %q (%+v)", issues[0].Field, c.wantFld, issues)
			}
		})
	}
}

// A valid entry must pass, otherwise the negative cases above could be passing
// because everything is rejected.
func TestInboundAcceptsValidExternalProxy(t *testing.T) {
	_, _, proxy, issues := SanitizeInbound(nil, nil, []any{
		map[string]any{"forceTls": "tls", "dest": "jp.example.com", "port": float64(443), "remark": "jp"},
	})
	if len(issues) != 0 {
		t.Fatalf("valid entry rejected: %+v", issues)
	}
	if len(proxy) != 1 {
		t.Fatalf("proxy = %+v", proxy)
	}
}

// asBool returns (value, ok). Reading it as (ok, _) inverted the meaning and made
// an explicit `false` fail validation, which the browser E2E caught by trying to
// turn sniffing off.
func TestInboundAcceptsExplicitFalseBooleans(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":false}`,
		`{"enabled":true}`,
		`{"metadataOnly":false,"routeOnly":false}`,
		`{"enabled":false,"destOverride":["http"],"routeOnly":true}`,
	} {
		if _, _, _, issues := SanitizeInbound(objFrom(t, raw), nil, nil); len(issues) != 0 {
			t.Fatalf("%s rejected: %+v", raw, issues)
		}
	}
}
