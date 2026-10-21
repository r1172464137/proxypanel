// Command fixture supplies isolated acceptance data and a bounded echo target.
// Never use its credentials, certificate, or plaintext echo in production.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	out := flag.String("init", "", "create fresh private fixture directory")
	flag.Parse()
	if *out != "" {
		initFixture(*out)
		return
	}
	go func() {
		c, e := net.ListenPacket("udp", ":18081")
		if e != nil {
			log.Fatal(e)
		}
		defer c.Close()
		b := make([]byte, 65535)
		for {
			n, a, e := c.ReadFrom(b)
			if e != nil {
				return
			}
			_, _ = c.WriteTo(b[:n], a)
		}
	}()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream" {
			_, _ = io.WriteString(w, strings.Repeat("verified-payload-", 4096))
			return
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			if _, e := io.WriteString(w, "persistent-connection\n"); e != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	log.Fatal(http.ListenAndServe(":18080", nil))
}
func initFixture(out string) {
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		log.Fatal("fixture path must not exist")
	}
	must(os.MkdirAll(out, 0700))
	write := func(name string, b []byte) { must(os.WriteFile(filepath.Join(out, name), b, 0600)) }
	token := func() string { b := make([]byte, 32); _, e := rand.Read(b); must(e); return hex.EncodeToString(b) }
	pg, redis, master, admin := token(), token(), token(), token()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(e)
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "Proxypanel isolated acceptance"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"panel", "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	must(e)
	write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	k, e := x509.MarshalPKCS8PrivateKey(key)
	must(e)
	write("tls.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k}))
	write("postgres.env", []byte("POSTGRES_USER=acceptance\nPOSTGRES_DB=acceptance\nPOSTGRES_PASSWORD="+pg+"\n"))
	write("redis.conf", []byte("bind 0.0.0.0\nprotected-mode yes\nrequirepass "+redis+"\nappendonly yes\nappendfsync everysec\nmaxmemory 32mb\nmaxmemory-policy noeviction\n"))
	write("panel.env", []byte(fmt.Sprintf("DATABASE_URL=postgres://acceptance:%s@postgres:5432/acceptance?sslmode=disable\nREDIS_URL=redis://default:%s@redis:6379/0\nPANEL_MASTER_KEY=%s\nPANEL_LISTEN=0.0.0.0:18443\nPANEL_PUBLIC_URL=https://127.0.0.1:18443\nPANEL_TLS_CERT=/run/fixture/ca.pem\nPANEL_TLS_KEY=/run/fixture/tls.key\nPANEL_MIGRATIONS_DIR=/app/migrations\nPANEL_ADMIN_USER=admin\nPANEL_ADMIN_PASSWORD=%s\n", pg, redis, master, admin)))
	b, e := json.Marshal(map[string]string{"admin_password": admin, "pg_password": pg, "master_key": master})
	must(e)
	write("test.json", b)
	write(".proxypanel-acceptance", []byte("20261021\n"))
	fmt.Println("Private acceptance fixture created; values not printed")
}
func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
