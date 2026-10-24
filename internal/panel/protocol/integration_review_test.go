package protocol

import (
	"encoding/json"
	"testing"
)

func TestTLSNormalizationPreservesEveryNativeEntry(t *testing.T) {
	cert, key := reviewTLSPair(t)
	ts := obj(t, `{"certificates":[{"certificate":`+jsonString(t, cert)+`,"key":`+jsonString(t, key)+`,"oneTimeLoading":true,"buildChain":false,"future":{"n":9007199254740993}},{"certificateFile":"/remote/b.pem","keyFile":"/remote/b.key","future":null}]}`)
	n := tlsNode(t, ts)
	stream, _, _, issues := Sanitize(n)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	normalized, _ := stream.Sub("tlsSettings")
	raw, _ := normalized.Get("certificates")
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || string(entries[0]["oneTimeLoading"]) != "true" || string(entries[0]["buildChain"]) != "false" || string(entries[0]["future"]) != `{"n":9007199254740993}` || string(entries[1]["future"]) != "null" {
		t.Fatalf("native fields lost: %s", raw)
	}
	again, _, _, issues := Sanitize(Node{StreamSettings: stream})
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ats, _ := again.Sub("tlsSettings")
	araw, _ := ats.Get("certificates")
	if string(araw) != string(raw) {
		t.Fatal("normalization is not idempotent")
	}
}

func TestRealityKeyPairValidation(t *testing.T) {
	n := realityNode(t)
	if issues := Validate(n); len(issues) != 0 {
		t.Fatal(issues)
	}
	_, other, err := GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	for _, pub := range []string{other, "malformed"} {
		n.ClientSettings.Set("public_key", pub)
		if !fields(Validate(n))["client_settings.public_key"] {
			t.Fatal("unpaired REALITY key accepted")
		}
	}
	n = realityNode(t)
	n.ServerKey = "conflicting-key"
	if !fields(Validate(n))["stream_settings.realitySettings.privateKey"] {
		t.Fatal("conflicting key sources accepted")
	}
	if _, _, _, issues := Sanitize(n); len(issues) == 0 {
		t.Fatal("sanitize silently discarded conflicting key")
	}
}

func TestTLSIssueOnlyAndMalformedMaterial(t *testing.T) {
	cert, key := reviewTLSCA(t, "fixture CA")
	n := tlsNode(t, obj(t, `{"certificates":[{"usage":"issue","certificate":[`+jsonString(t, cert)+`],"key":[`+jsonString(t, key)+`]}]}`))
	if issues := Validate(n); len(issues) != 0 {
		t.Fatalf("issue CA can serve by signing on demand: %v", issues)
	}
	for _, entry := range []string{`null`, `{"certificateFile":"/a","keyFile":"/b","certificate":42}`, `{"usage":"verify","certificate":["broken"]}`} {
		n = tlsNode(t, obj(t, `{"certificates":[`+entry+`]}`))
		if len(Validate(n)) == 0 {
			t.Fatalf("malformed entry accepted: %s", entry)
		}
	}
}
