//go:build xrayreal

package supervisor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/xrayconf"
)

// This is one continuous integration proof, not a concatenation of protocol and
// lifecycle reports. The panel's historical/native projection is consumed by
// the actual Agent, with real TLS payloads on both sides of a core replacement.
func TestProtocolAgentTLSLifecycle(t *testing.T) {
	for _, files := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_files=%v", files), func(t *testing.T) {
			sup, panel, _ := startRealAgent(t)
			ctx := context.Background()
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "agent.test"}, DNSNames: []string{"agent.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			priv, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
			secret := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM([]byte(cert)) {
				t.Fatal("root fixture")
			}
			ts := domain.NewObject()
			if files {
				root := filepath.Join(sup.cfg.StateDir, "certs")
				cp, kp := filepath.Join(root, "fixture.pem"), filepath.Join(root, "fixture.key")
				if err := os.WriteFile(cp, []byte(cert), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(kp, []byte(secret), 0600); err != nil {
					t.Fatal(err)
				}
				_ = ts.Set("certificateFile", cp)
				_ = ts.Set("keyFile", kp)
			} else {
				// Historical bare PEM strings must become native arrays before Agent use.
				_ = ts.Set("certificates", []any{map[string]any{"certificate": cert, "key": secret}})
			}
			stream := domain.NewObject()
			_ = stream.Set("network", "tcp")
			_ = stream.Set("security", "tls")
			_ = stream.Set("tlsSettings", ts)
			node := domain.Node{ID: "node-1", Name: "integration", Protocol: protocol.VLESS, Host: "127.0.0.1", Listen: "127.0.0.1", Port: freeTCPPort(t), StreamSettings: stream, ProtocolSettings: domain.NewObject(), ClientSettings: domain.NewObject()}
			_ = node.ProtocolSettings.Set("decryption", "none")
			_ = node.ClientSettings.Set("server_name", "agent.test")
			normalized, ps, _, issues := protocol.Sanitize(protocol.Node{Protocol: node.Protocol, Host: node.Host, Port: node.Port, Listen: node.Listen, StreamSettings: stream, ProtocolSettings: node.ProtocolSettings, ClientSettings: node.ClientSettings})
			if len(issues) != 0 {
				t.Fatal(issues)
			}
			if issues := protocol.Validate(protocol.Node{Protocol: node.Protocol, Host: node.Host, Port: node.Port, Listen: node.Listen, StreamSettings: normalized, ProtocolSettings: ps, ClientSettings: node.ClientSettings}); len(issues) != 0 {
				t.Fatal(issues)
			}
			// Keep the original historical row: Build itself must normalize, not rely
			// on an administrator saving the node again.
			raw, err := xrayconf.Build(node, []xrayconf.Client{{Email: "alice@example.test", UUID: "01020304-0506-0708-090a-0b0c0d0e0f10"}})
			if err != nil {
				t.Fatal(err)
			}
			users := []contracts.AuthorizedUser{{MemberID: "member-tls", Email: "alice@example.test"}}
			inst := contracts.Instance{ID: "node-1", NodeID: "node-1", Config: raw, Users: users}
			echo := realEchoServer(t)
			dial := func() *tls.Conn {
				c, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", node.Port), &tls.Config{RootCAs: roots, ServerName: "agent.test", MinVersion: tls.VersionTLS12})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.Close() })
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				h := append([]byte{0}, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16)
				h = append(h, 0, 1)
				h = binary.BigEndian.AppendUint16(h, uint16(echo))
				h = append(h, 1, 127, 0, 0, 1)
				payload := []byte("normalized-agent-tls-payload")
				if _, err := c.Write(append(h, payload...)); err != nil {
					t.Fatal(err)
				}
				response := make([]byte, 2+len(payload))
				if _, err := io.ReadFull(c, response); err != nil {
					t.Fatal(err)
				}
				if string(response[2:]) != string(payload) {
					t.Fatalf("payload mismatch: %q", response)
				}
				return c
			}
			var generations []string
			var live *tls.Conn
			for rev := int64(1); rev <= 2; rev++ {
				if rev == 2 {
					var doc map[string]any
					if err := json.Unmarshal(inst.Config, &doc); err != nil {
						t.Fatal(err)
					}
					doc["log"] = map[string]any{"loglevel": "error"}
					inst.Config, _ = json.Marshal(doc)
				}
				panel.setSnapshot("protocol-agent", rev, []contracts.Instance{inst}, 1)
				if err := sup.syncOnce(ctx); err != nil {
					t.Fatal(err)
				}
				live = dial()
				records, err := sup.store.Instances(ctx)
				if err != nil || len(records) != 1 {
					t.Fatalf("records %v %v", records, err)
				}
				generations = append(generations, records[0].CoreGeneration)
				if err := sup.sampleOnce(ctx); err != nil {
					t.Fatal(err)
				}
				if rev == 1 {
					_ = live.Close()
				}
			}
			if generations[0] == "" || generations[0] == generations[1] {
				t.Fatalf("replacement reused generation: %v", generations)
			}
			pending, err := sup.store.PendingBatches(ctx, 100)
			if err != nil {
				t.Fatal(err)
			}
			measured := map[string]int64{}
			for _, b := range pending {
				for _, s := range b.Samples {
					if s.MemberID == "member-tls" {
						measured[s.CoreGeneration] += s.UploadBytes
					}
				}
			}
			for _, g := range generations {
				if measured[g] <= 0 {
					t.Fatalf("generation missing real usage: %v", measured)
				}
			}
			oldExit := sup.mgr.ExitSignal("node-1")
			panel.setSnapshot("protocol-agent", 3, nil, 0)
			if err := sup.syncOnce(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-oldExit:
			case <-time.After(5 * time.Second):
				t.Fatal("revoked process exit not confirmed")
			}
			_ = live.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, err := live.Read(make([]byte, 1)); err == nil {
				t.Fatal("revoked TLS session survived")
			}
			reports := panel.appliedReports()
			if len(reports) != 3 || reports[2].State != "applied" || reports[2].Revision != 3 {
				t.Fatalf("receipts: %+v", reports)
			}
			intent, err := sup.store.ApplyIntent(ctx)
			if err != nil || intent != nil {
				t.Fatalf("intent not consumed: %v %v", intent, err)
			}
			records, err := sup.store.Instances(ctx)
			if err != nil || len(records) != 0 {
				t.Fatalf("removed records: %v %v", records, err)
			}
			t.Logf("normalized TLS -> Agent apply -> verified TLS payload -> generation replacement %v -> per-generation usage %v -> revocation exit/session/receipt confirmed", generations, measured)
		})
	}
}
