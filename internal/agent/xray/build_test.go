package xray

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

func baseDocument(port int, emails ...string) []byte {
	clients := make([]map[string]any, 0, len(emails))
	for i, e := range emails {
		clients = append(clients, map[string]any{
			"id":    fmt.Sprintf("11111111-1111-1111-1111-00000000000%d", i),
			"email": e,
		})
	}
	doc := map[string]any{
		"log": map[string]any{"loglevel": "warning", "access": "/var/log/xray/access.log"},
		"inbounds": []any{map[string]any{
			"tag":            "vless-in",
			"port":           port,
			"protocol":       "vless",
			"listen":         "0.0.0.0",
			"settings":       map[string]any{"decryption": "none", "clients": clients},
			"streamSettings": map[string]any{"network": "tcp", "security": "none"},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	doc, err := DecodeDocument(raw)
	if err != nil {
		t.Fatalf("DecodeDocument: %v", err)
	}
	return doc
}

func instanceFor(t *testing.T, users []contracts.AuthorizedUser, emails ...string) contracts.Instance {
	return contracts.Instance{
		ID:     "node-1",
		NodeID: "node-1",
		Config: json.RawMessage(baseDocument(443, emails...)),
		Users:  users,
	}
}

func TestPrepareInjectsManagementPlane(t *testing.T) {
	expiry := time.Now().UTC().Add(24 * time.Hour)
	inst := instanceFor(t, []contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test", ExpiresAt: &expiry}}, "alice@example.test")
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if _, ok := plan.Document["api"]; ok {
		t.Fatal("the filtered document must not contain a management API")
	}
	config, err := plan.Render(23456)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	doc := decode(t, config)
	if _, ok := doc["api"]; !ok {
		t.Fatal("management API missing")
	}
	if _, ok := doc["stats"]; !ok {
		t.Fatal("stats section missing")
	}
	policy := doc["policy"].(map[string]any)
	system := policy["system"].(map[string]any)
	for _, key := range []string{"statsInboundUplink", "statsInboundDownlink", "statsUserUplink", "statsUserDownlink"} {
		if system[key] != true {
			t.Fatalf("policy.system.%s must be true", key)
		}
	}
	levels := policy["levels"].(map[string]any)
	level0, ok := levels["0"].(map[string]any)
	if !ok {
		t.Fatalf("policy level 0 missing: %v", levels)
	}
	if level0["statsUserUplink"] != true || level0["statsUserDownlink"] != true {
		t.Fatalf("user stats are not enabled for level 0: %v", level0)
	}
	rules := doc["routing"].(map[string]any)["rules"].([]any)
	first := rules[0].(map[string]any)
	if first["outboundTag"] != APITag {
		t.Fatalf("api routing rule missing: %v", first)
	}
	inbounds := doc["inbounds"].([]any)
	apiInbound := inbounds[len(inbounds)-1].(map[string]any)
	if apiInbound["tag"] != APITag || apiInbound["protocol"] != "dokodemo-door" {
		t.Fatalf("management API inbound missing: %v", apiInbound)
	}
	if got := apiInbound["port"]; fmt.Sprint(got) != "23456" {
		t.Fatalf("management API port = %v", got)
	}
	// Panel supplied log file paths must not survive.
	logDoc := doc["log"].(map[string]any)
	if logDoc["access"] != "none" {
		t.Fatalf("access log must be disabled, got %v", logDoc["access"])
	}
	if _, ok := logDoc["error"]; ok {
		t.Fatalf("error log path must be stripped: %v", logDoc)
	}
	// clients of the rendered document keep exactly the authorized users
	in := inbounds[0].(map[string]any)
	clients := in["settings"].(map[string]any)["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}
}

func TestPrepareFiltersExpiredAndUnknownClients(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	inst := instanceFor(t, []contracts.AuthorizedUser{
		{MemberID: "m-alice", Email: "alice@example.test", ExpiresAt: &future},
		{MemberID: "m-bob", Email: "bob@example.test", ExpiresAt: &past},
	}, "alice@example.test", "bob@example.test", "mallory@example.test")
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: now})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.ExpiredUsers != 1 {
		t.Fatalf("ExpiredUsers = %d", plan.ExpiredUsers)
	}
	if plan.DroppedClients != 2 {
		t.Fatalf("DroppedClients = %d (bob expired + mallory unauthorized)", plan.DroppedClients)
	}
	if plan.KeptClients != 1 {
		t.Fatalf("KeptClients = %d", plan.KeptClients)
	}
	if _, ok := plan.EmailToMember["alice@example.test"]; !ok {
		t.Fatalf("alice must remain authorized: %v", plan.EmailToMember)
	}
	if !plan.NextExpiry.Equal(future) {
		t.Fatalf("NextExpiry = %v, want %v", plan.NextExpiry, future)
	}
	if len(plan.EffectiveUsers) != 1 || plan.EffectiveUsers[0].MemberID != "m-alice" {
		t.Fatalf("EffectiveUsers = %+v", plan.EffectiveUsers)
	}
}

func TestPrepareEmptyUsersRevokesAllClients(t *testing.T) {
	inst := instanceFor(t, nil, "alice@example.test")
	plan, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if plan.KeptClients != 0 {
		t.Fatalf("KeptClients = %d", plan.KeptClients)
	}
	config, err := plan.Render(20001)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	in := decode(t, config)["inbounds"].([]any)[0].(map[string]any)
	clients := in["settings"].(map[string]any)["clients"].([]any)
	if len(clients) != 0 {
		t.Fatalf("expected an empty client list, got %v", clients)
	}
}

func TestPrepareRejectsUnsafeDocuments(t *testing.T) {
	certRoot := t.TempDir()
	cases := []struct {
		name    string
		mutate  func(doc map[string]any)
		wantSub string
	}{
		{"api section", func(doc map[string]any) { doc["api"] = map[string]any{"tag": "x"} }, "not allowed"},
		{"dns section", func(doc map[string]any) { doc["dns"] = map[string]any{} }, "not allowed"},
		{"outbound proxy", func(doc map[string]any) {
			doc["outbounds"] = []any{map[string]any{"protocol": "socks"}}
		}, "not allowed"},
		{"balancer", func(doc map[string]any) {
			doc["routing"] = map[string]any{"rules": []any{map[string]any{"balancerTag": "b"}}}
		}, "balancerTag"},
		{"forward inbound", func(doc map[string]any) {
			doc["inbounds"].([]any)[0].(map[string]any)["protocol"] = "dokodemo-door"
		}, "not allowed"},
		{"missing clients", func(doc map[string]any) {
			delete(doc["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any), "clients")
		}, "clients"},
		{"client without email", func(doc map[string]any) {
			clients := doc["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["clients"].([]any)
			clients[0].(map[string]any)["email"] = ""
		}, "email"},
		{"reserved inbound tag", func(doc map[string]any) {
			doc["inbounds"].([]any)[0].(map[string]any)["tag"] = APITag
		}, "reserved"},
		{"unsupported network", func(doc map[string]any) {
			doc["inbounds"].([]any)[0].(map[string]any)["streamSettings"].(map[string]any)["network"] = "carrier-pigeon"
		}, "network"},
		{"unknown log key", func(doc map[string]any) {
			doc["log"] = map[string]any{"loglevel": "debug", "syslog": "x"}
		}, "not allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := decode(t, baseDocument(443, "alice@example.test"))
			tc.mutate(doc)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			inst := contracts.Instance{ID: "node-1", NodeID: "node-1", Config: raw,
				Users: []contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}}
			_, err = Prepare(inst, Options{CertRoot: certRoot, Now: time.Now().UTC()})
			if err == nil {
				t.Fatalf("expected rejection")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestCertificatePathsStayInsideCertRoot(t *testing.T) {
	certRoot := t.TempDir()
	build := func(cert, key string) contracts.Instance {
		doc := decode(t, baseDocument(443, "alice@example.test"))
		in := doc["inbounds"].([]any)[0].(map[string]any)
		in["streamSettings"] = map[string]any{
			"network":  "tcp",
			"security": "tls",
			"tlsSettings": map[string]any{
				"certificates": []any{map[string]any{
					"certificateFile": cert,
					"keyFile":         key,
				}},
			},
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return contracts.Instance{ID: "node-1", NodeID: "node-1", Config: raw,
			Users: []contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}}
	}
	good := filepath.Join(certRoot, "fullchain.pem")
	goodKey := filepath.Join(certRoot, "privkey.pem")
	if _, err := Prepare(build(good, goodKey), Options{CertRoot: certRoot, Now: time.Now().UTC()}); err != nil {
		t.Fatalf("certificate inside the agent directory must be accepted: %v", err)
	}
	outside := filepath.Join(filepath.Dir(certRoot), "other", "fullchain.pem")
	if _, err := Prepare(build(outside, goodKey), Options{CertRoot: certRoot, Now: time.Now().UTC()}); err == nil {
		t.Fatal("certificate outside the agent directory must be rejected")
	}
	traversal := certRoot + string(filepath.Separator) + ".." + string(filepath.Separator) + "escape.pem"
	if _, err := Prepare(build(traversal, goodKey), Options{CertRoot: certRoot, Now: time.Now().UTC()}); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if _, err := Prepare(build("relative.pem", goodKey), Options{CertRoot: certRoot, Now: time.Now().UTC()}); err == nil {
		t.Fatal("relative certificate path must be rejected")
	}
}

func TestHashIsStableAndPortIndependent(t *testing.T) {
	inst := instanceFor(t, []contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}}, "alice@example.test")
	first, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	second, err := Prepare(inst, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("hash must be stable: %s vs %s", first.Hash, second.Hash)
	}
	if _, err := first.Render(20001); err != nil {
		t.Fatal(err)
	}
	h1 := first.Hash
	if _, err := second.Render(20002); err != nil {
		t.Fatal(err)
	}
	if h1 != second.Hash {
		t.Fatal("hash must not depend on the management API port")
	}
	changed := instanceFor(t, []contracts.AuthorizedUser{{MemberID: "m1", Email: "alice@example.test"}, {MemberID: "m2", Email: "bob@example.test"}}, "alice@example.test", "bob@example.test")
	third, err := Prepare(changed, Options{CertRoot: t.TempDir(), Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if third.Hash == first.Hash {
		t.Fatal("hash must change when the authorized user set changes")
	}
}

func TestValidateIDsRejectsTraversal(t *testing.T) {
	cases := []struct{ id, node string }{
		{"", ""},
		{"..", ".."},
		{"a/b", "a/b"},
		{`a\b`, `a\b`},
		{"node-1", "node-2"},
		{".hidden", ".hidden"},
	}
	for _, tc := range cases {
		if err := ValidateIDs(tc.id, tc.node); err == nil {
			t.Fatalf("ValidateIDs(%q, %q) must fail", tc.id, tc.node)
		}
	}
	if err := ValidateIDs("node-1", "node-1"); err != nil {
		t.Fatalf("valid id rejected: %v", err)
	}
}

func TestValidateSnapshotRules(t *testing.T) {
	now := time.Now().UTC()
	good := contracts.Snapshot{
		SchemaVersion:     contracts.SchemaVersion,
		ControlGeneration: "gen-1",
		Revision:          1,
		IssuedAt:          now,
		ExpectedUserCount: 1,
		Instances: []contracts.Instance{{
			ID: "node-1", NodeID: "node-1", Config: json.RawMessage(baseDocument(443, "a@b.test")),
			Users: []contracts.AuthorizedUser{{MemberID: "m1", Email: "a@b.test"}},
		}},
	}
	if err := ValidateSnapshot(good, DefaultMaxInstances); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	broken := func(mutate func(s *contracts.Snapshot)) error {
		s := good
		s.Instances = append([]contracts.Instance{}, good.Instances...)
		s.Instances[0].Users = append([]contracts.AuthorizedUser{}, good.Instances[0].Users...)
		mutate(&s)
		return ValidateSnapshot(s, DefaultMaxInstances)
	}
	if err := broken(func(s *contracts.Snapshot) { s.SchemaVersion = "v2" }); err == nil {
		t.Fatal("schema mismatch must be rejected")
	}
	if err := broken(func(s *contracts.Snapshot) { s.ControlGeneration = " " }); err == nil {
		t.Fatal("empty generation must be rejected")
	}
	if err := broken(func(s *contracts.Snapshot) { s.Revision = 0 }); err == nil {
		t.Fatal("revision 0 must be rejected")
	}
	if err := broken(func(s *contracts.Snapshot) { s.ExpectedUserCount = 2 }); err == nil {
		t.Fatal("expected_user_count mismatch must be rejected")
	}
	if err := broken(func(s *contracts.Snapshot) { s.IssuedAt = time.Time{} }); err == nil {
		t.Fatal("zero issued_at must be rejected")
	}
	if err := broken(func(s *contracts.Snapshot) { s.Instances[0].Config = nil }); err == nil {
		t.Fatal("empty config must be rejected")
	}
	if err := broken(func(s *contracts.Snapshot) {
		s.Instances = append(s.Instances, s.Instances[0])
		s.ExpectedUserCount = 2
	}); err == nil {
		t.Fatal("duplicate instance id must be rejected")
	}
	if err := broken(func(s *contracts.Snapshot) { s.Instances[0].NodeID = "other" }); err == nil {
		t.Fatal("instance id must equal node id")
	}
	if err := broken(func(s *contracts.Snapshot) {
		s.Instances[0].Users = append(s.Instances[0].Users, contracts.AuthorizedUser{MemberID: "m1", Email: "c@d.test"})
		s.ExpectedUserCount = 2
	}); err == nil {
		t.Fatal("duplicate member id must be rejected")
	}
}
