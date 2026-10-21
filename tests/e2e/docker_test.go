package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const dockerRemote = "/opt/proxypanel-acceptance-20261021"

type dockerFixture struct {
	Servers, Nodes              []string
	Group, Subscription, Member string
}
type dockerTest struct {
	t             *testing.T
	root, private string
	api           *platformAPI
	transport     *http.Transport
	db            *pgxpool.Pool
	fixture       dockerFixture
	password      string
}

func dockerSetup(t *testing.T) *dockerTest {
	t.Helper()
	if os.Getenv("PANEL_DOCKER_TEST") != "1" {
		t.Skip("explicit isolated Docker acceptance environment required")
	}
	root := os.Getenv("PANEL_DOCKER_ROOT")
	if root == "" {
		t.Fatal("missing Docker workspace root")
	}
	d := &dockerTest{t: t, root: root, private: filepath.Join(root, ".local", "docker-acceptance")}
	var secret map[string]string
	b, e := os.ReadFile(filepath.Join(d.private, "test.json"))
	if e != nil {
		t.Fatal("read private fixture")
	}
	if json.Unmarshal(b, &secret) != nil {
		t.Fatal("decode private fixture")
	}
	d.password = secret["admin_password"]
	ca, e := os.ReadFile(filepath.Join(d.private, "ca.pem"))
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("test CA invalid")
	}
	d.transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(d.transport.CloseIdleConnections)
	jar, _ := cookiejar.New(nil)
	d.api = &platformAPI{t: t, base: "https://127.0.0.1:18443", client: &http.Client{Jar: jar, Transport: d.transport, Timeout: 15 * time.Second}}
	waitFor(t, 40*time.Second, "Docker HTTPS panel readiness", func() bool {
		r, e := d.api.client.Get(d.api.base + "/readyz")
		if e != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
	d.api.login("admin", d.password)
	d.db, e = pgxpool.New(context.Background(), "postgres://acceptance:"+secret["pg_password"]+"@127.0.0.1:25432/acceptance?sslmode=disable")
	if e != nil {
		t.Fatal("connect acceptance db")
	}
	t.Cleanup(d.db.Close)
	state := filepath.Join(d.private, "fixture.json")
	if b, e := os.ReadFile(state); e == nil {
		if json.Unmarshal(b, &d.fixture) != nil {
			t.Fatal("invalid saved fixture")
		}
		return d
	}
	for n := 1; n <= 2; n++ {
		srv := d.api.request("POST", "/api/v1/admin/servers", map[string]any{"name": fmt.Sprintf("docker-node-%d", n), "address": "127.0.0.1", "region": "isolated"})
		sid := srv["id"].(string)
		d.fixture.Servers = append(d.fixture.Servers, sid)
		node := d.api.request("POST", "/api/v1/admin/nodes", map[string]any{"name": fmt.Sprintf("docker-vless-%d", n), "server_id": sid, "enabled": true, "host": "127.0.0.1", "listen": "0.0.0.0", "port": 20001 + n*100, "protocol": "vless", "stream_settings": map[string]any{"network": "tcp", "security": "none"}, "protocol_settings": map[string]any{"decryption": "none"}, "client_settings": map[string]any{}})
		d.fixture.Nodes = append(d.fixture.Nodes, node["id"].(string))
		code := d.api.request("POST", "/api/v1/admin/servers/"+sid+"/registration-codes", nil)
		config := map[string]any{"panel_url": "https://panel:18443", "registration_code": code["code"], "xray_path": "/app/xray", "state_dir": "/state", "poll_interval_seconds": 1, "stats_interval_seconds": 1, "heartbeat_interval_seconds": 2}
		file := writeConfig(t, d.private, fmt.Sprintf("agent%d.json", n), config)
		d.upload(file, fmt.Sprintf("agent%d.json", n))
	}
	g := d.api.request("POST", "/api/v1/admin/node-groups", map[string]any{"name": "docker-group", "node_ids": d.fixture.Nodes})
	d.fixture.Group = g["id"].(string)
	s := d.api.request("POST", "/api/v1/admin/subscriptions", map[string]any{"name": "docker-sub", "node_group_ids": []string{d.fixture.Group}, "quota_bytes": 1 << 30, "timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true})
	d.fixture.Subscription = s["id"].(string)
	m := d.api.request("POST", "/api/v1/admin/members", map[string]any{"username": "docker-alice", "password": d.password, "role": "member", "subscription_id": d.fixture.Subscription})
	d.fixture.Member = m["id"].(string)
	writeConfig(t, d.private, "fixture.json", d.fixture)
	return d
}
func (d *dockerTest) sshArgs() []string {
	return []string{"-i", filepath.Join(d.root, ".local", "database-ssh-key"), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(d.root, ".local", "database-known-hosts"), "-o", "ConnectTimeout=10"}
}
func (d *dockerTest) remote(action string) string {
	d.t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 90*time.Second)
	defer c()
	a := append(d.sshArgs(), "root@192.168.5.203", "sh "+dockerRemote+"/control.sh "+action)
	b, e := exec.CommandContext(ctx, "ssh", a...).CombinedOutput()
	if e != nil {
		d.t.Fatalf("owned Docker action %q failed: %s", action, b)
	}
	return string(b)
}
func (d *dockerTest) upload(file, name string) {
	d.t.Helper()
	if name != "agent1.json" && name != "agent2.json" {
		d.t.Fatal("invalid agent upload")
	}
	a := append(d.sshArgs(), file, "root@192.168.5.203:"+dockerRemote+"/secrets/"+name)
	if e := exec.Command("scp", a...).Run(); e != nil {
		d.t.Fatal("private config upload failed")
	}
}
func (d *dockerTest) memberPatch(values map[string]any) {
	d.t.Helper()
	m := d.api.request("GET", "/api/v1/admin/members/"+d.fixture.Member, nil)
	member, ok := m["member"].(map[string]any)
	if !ok || member["version"] == nil {
		d.t.Fatal("member detail missing versioned member object")
	}
	values["version"] = member["version"]
	d.api.request("PATCH", "/api/v1/admin/members/"+d.fixture.Member, values)
}
func (d *dockerTest) applied() {
	d.t.Helper()
	waitFor(d.t, 45*time.Second, "both Docker Agents at desired revision", func() bool {
		var n int
		e := d.db.QueryRow(context.Background(), `select count(*) from agents where applied_revision=desired_revision and applied_revision>0 and archived=false`).Scan(&n)
		if e != nil {
			d.t.Fatalf("agent revision query failed: %v", e)
		}
		return n == 2
	})
}
func (d *dockerTest) export(format string) map[string]any {
	d.t.Helper()
	jar, _ := cookiejar.New(nil)
	u := &platformAPI{t: d.t, base: d.api.base, client: &http.Client{Jar: jar, Transport: d.transport, Timeout: 15 * time.Second}}
	u.login("docker-alice", d.password)
	s := u.request("GET", "/api/v1/me/subscription", nil)
	req, _ := http.NewRequest("GET", d.api.base+"/sub/"+s["token"].(string)+"?format="+format, nil)
	r, e := u.client.Do(req)
	if e != nil {
		d.t.Fatal("export request failed")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		d.t.Fatalf("export status=%d", r.StatusCode)
	}
	var cfg map[string]any
	if json.NewDecoder(r.Body).Decode(&cfg) != nil {
		d.t.Fatal("export decode failed")
	}
	return cfg
}
func (d *dockerTest) ledger() int64 {
	d.t.Helper()
	var n int64
	if e := d.db.QueryRow(context.Background(), `select coalesce(sum(up_bytes+down_bytes),0) from usage_entries where member_id=$1`, d.fixture.Member).Scan(&n); e != nil {
		d.t.Fatal("ledger query failed")
	}
	return n
}
func dockerProxy(t *testing.T, cfg map[string]any) (*http.Client, *http.Transport, map[string]any) {
	t.Helper()
	out := cfg["outbounds"].([]any)[0].(map[string]any)
	port := unusedPort(t)
	cfg["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}
	cfg["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": out["tag"]}}}
	startCore(t, os.Getenv("XRAY_BIN"), writeConfig(t, t.TempDir(), "docker-client.json", cfg), port)
	tr := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", port)})}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}, tr, out
}
func dockerFetch(c *http.Client) bool {
	r, e := c.Get("http://target:18080/")
	if e != nil {
		return false
	}
	defer r.Body.Close()
	b, e := io.ReadAll(r.Body)
	return e == nil && string(b) == strings.Repeat("verified-payload-", 4096)
}
func dockerUDP(t *testing.T, out map[string]any) net.Conn {
	t.Helper()
	server := out["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)
	user := server["users"].([]any)[0].(map[string]any)
	uuid, e := hex.DecodeString(strings.ReplaceAll(user["id"].(string), "-", ""))
	if e != nil {
		t.Fatal("bad uuid")
	}
	port := int(server["port"].(float64))
	c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if e != nil {
		t.Fatal("UDP association connect failed")
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	h := append([]byte{0}, uuid...)
	h = append(h, 0, 2)
	h = binary.BigEndian.AppendUint16(h, 18081)
	h = append(h, 2, 6)
	h = append(h, []byte("target")...)
	p := []byte("docker-real-udp-echo")
	h = binary.BigEndian.AppendUint16(h, uint16(len(p)))
	h = append(h, p...)
	if _, e = c.Write(h); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 4)
	if _, e = io.ReadFull(c, b); e != nil || b[0] != 0 || b[1] != 0 {
		t.Fatal("UDP response failed")
	}
	data := make([]byte, binary.BigEndian.Uint16(b[2:]))
	if _, e = io.ReadFull(c, data); e != nil || string(data) != string(p) {
		t.Fatal("UDP echo mismatch")
	}
	c.SetDeadline(time.Time{})
	t.Logf("Docker Xray UDP echo verified: %d bytes", len(data))
	return c
}

// dockerServerNow is the only wall clock used by the offline expiry assertions.
// A failed clock query is an infrastructure failure, never evidence of expiry.
func dockerServerNow(db *pgxpool.Pool) (time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var now time.Time
	err := db.QueryRow(ctx, `SELECT now()`).Scan(&now)
	return now.UTC(), err
}

// dockerStream has a bounded opening phase and an overall safety deadline, not
// the short http.Client timeout used for one-shot requests. Cleanup cancels any
// reader even when an assertion fails before revocation.
func dockerStream(t *testing.T, tr *http.Transport) io.ReadCloser {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	opening := time.AfterFunc(5*time.Second, cancel)
	defer opening.Stop()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://target:18080/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatalf("open persistent Docker TCP stream: %v", err)
	}
	t.Cleanup(func() { _ = r.Body.Close() })
	if r.StatusCode != http.StatusOK {
		t.Fatalf("persistent Docker TCP stream status=%d", r.StatusCode)
	}
	if _, err := io.ReadFull(r.Body, make([]byte, 22)); err != nil {
		t.Fatalf("persistent Docker TCP stream never became active: %v", err)
	}
	return r.Body
}

// dockerUDPEcho keeps an already established VLESS UDP association active.
// There is no VLESS response header after the initial dockerUDP exchange.
func dockerUDPEcho(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	payload := "docker-real-udp-echo"
	packet := binary.BigEndian.AppendUint16(nil, uint16(len(payload)))
	packet = append(packet, payload...)
	if _, err := conn.Write(packet); err != nil {
		return err
	}
	var size [2]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return err
	}
	body := make([]byte, binary.BigEndian.Uint16(size[:]))
	if _, err := io.ReadFull(conn, body); err != nil {
		return err
	}
	if string(body) != payload {
		return fmt.Errorf("Docker UDP echo payload mismatch")
	}
	return nil
}

func TestDockerAcceptance(t *testing.T) {
	d := dockerSetup(t)
	d.remote("agent1")
	d.remote("agent2")
	d.remote("start agent1")
	d.remote("start agent2")
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true, "expires_at": nil})
	d.applied()
	c, tr, out := dockerProxy(t, d.export("xray"))
	before := d.ledger()
	if !dockerFetch(c) {
		t.Fatal("Docker proxy payload failed")
	}
	waitFor(t, 20*time.Second, "Docker usage ledger", func() bool { return d.ledger() >= before+69632 })
	t.Log("Docker TLS panel + two Agents + tunneled Xray traffic and actual ledger verified")
	udp := dockerUDP(t, out)
	stream := dockerStream(t, tr)
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stream); close(done) }()
	preview := d.api.request("POST", "/api/v1/admin/batches/preview", map[string]any{"operation": "set_active", "member_ids": []string{d.fixture.Member}, "values": map[string]any{"active": false}})
	revoked := time.Now()
	d.api.request("POST", "/api/v1/admin/batches", map[string]any{"preview_id": preview["id"], "confirm": true})
	select {
	case <-done:
		t.Logf("Docker existing TCP terminated in %.3fs", time.Since(revoked).Seconds())
	case <-time.After(60 * time.Second):
		t.Fatal("TCP hard revocation exceeded 60s")
	}
	assertUDPRevoked(t, udp, revoked)
	tr.CloseIdleConnections()
	if dockerFetch(c) {
		t.Fatal("new connection allowed after revoke")
	}
	d.applied()
	d.remote("restart agent1")
	time.Sleep(3 * time.Second)
	if dockerFetch(c) {
		t.Fatal("agent restart resurrected revoked member")
	}
	t.Log("Agent restart preserved revocation")
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true})
	d.applied()
	// Revocation deliberately retires old credentials; re-enable uses a fresh export.
	c, tr, _ = dockerProxy(t, d.export("xray"))
	waitFor(t, 15*time.Second, "proxy after re-enable", func() bool { return dockerFetch(c) })
	time.Sleep(3 * time.Second)
	stable := d.ledger()
	restartsBefore := strings.TrimSpace(d.remote("restarts agent1"))
	d.remote("crash agent1")
	waitFor(t, 30*time.Second, "automatic Agent restart", func() bool { return strings.TrimSpace(d.remote("restarts agent1")) != restartsBefore && dockerFetch(c) })
	d.applied()
	if d.ledger() < stable {
		t.Fatal("ledger regressed")
	}
	t.Log("Container crash restart recovers Agent identity and forwarding")
	d.remote("stop agent2")
	d.remote("remove agent2")
	d.remote("agent2")
	d.applied()
	var agents int
	if err := d.db.QueryRow(context.Background(), `select count(*) from agents where archived=false`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if agents != 2 {
		t.Fatal("volume remount changed Agent identity")
	}
	t.Log("Uninstall/recreate preserving named state volume retained both Agent identities")
	// Use the latest credentials and keep real sessions open across the expiry-
	// only snapshot update. The server clock, not Windows UTC, defines expiry.
	c, tr, out = dockerProxy(t, d.export("xray"))
	if !dockerFetch(c) {
		t.Fatal("proxy unavailable before offline expiry setup")
	}
	expiryUDP := dockerUDP(t, out)
	expiryStream := dockerStream(t, tr)
	started := time.Now() // monotonic diagnostic / harness watchdog only
	serverNow := func() time.Time {
		now, err := dockerServerNow(d.db)
		if err != nil {
			t.Fatalf("PostgreSQL expiry clock query failed: %v", err)
		}
		return now
	}
	type observation struct {
		serverTime time.Time
		elapsed    time.Duration
		err        error
		clockErr   error
	}
	observe := func(err error) observation {
		elapsed := time.Since(started)
		now, clockErr := dockerServerNow(d.db)
		return observation{serverTime: now, elapsed: elapsed, err: err, clockErr: clockErr}
	}
	// Capture the server timestamp at disconnect, not later when the main test
	// may finish a five-second new-connection probe. Both channels are buffered
	// so readers always exit; cleanup closes/cancels the underlying I/O.
	tcpClosed, udpClosed := make(chan observation, 1), make(chan observation, 1)
	go func() { _, err := io.Copy(io.Discard, expiryStream); tcpClosed <- observe(err) }()
	udpCtx, stopUDP := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopUDP()
	defer expiryUDP.Close()
	defer expiryStream.Close()
	go func() {
		for {
			if err := dockerUDPEcho(expiryUDP); err != nil {
				udpClosed <- observe(err)
				return
			}
			select {
			case <-udpCtx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()
	expiry := serverNow().Add(15 * time.Second)
	d.memberPatch(map[string]any{"expires_at": expiry.Format(time.RFC3339Nano)})
	d.applied()
	if now := serverNow(); !now.Before(expiry) {
		t.Fatalf("expiry setup consumed its delivery window: server_now=%s expiry=%s", now.Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano))
	}
	panelNeedsStart := true
	defer func() {
		if panelNeedsStart {
			d.remote("start panel")
		}
	}() // registered before stop, including stop-command failure
	d.remote("stop panel")
	stoppedAt := serverNow()
	if !stoppedAt.Before(expiry) {
		t.Fatalf("panel did not stop before expiry: server_now=%s expiry=%s", stoppedAt.Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano))
	}
	t.Logf("Offline expiry: PG server_now=%s delivered_expiry=%s remaining=%.3fs local_monotonic_elapsed=%.3fs (local UTC not used)", stoppedAt.Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano), expiry.Sub(stoppedAt).Seconds(), time.Since(started).Seconds())
	check := func(kind string, event observation) {
		if event.clockErr != nil {
			t.Fatalf("%s: PostgreSQL expiry clock query failed: %v", kind, event.clockErr)
		}
		t.Logf("Offline %s: PG observed_at=%s after_expiry=%.3fs local_monotonic_elapsed=%.3fs read_error=%v", kind, event.serverTime.Format(time.RFC3339Nano), event.serverTime.Sub(expiry).Seconds(), event.elapsed.Seconds(), event.err)
		if event.serverTime.Before(expiry) {
			t.Fatalf("%s revoked before delivered expiry according to PostgreSQL", kind)
		}
		if event.serverTime.After(expiry.Add(60 * time.Second)) {
			t.Fatalf("%s exceeded 60s after delivered expiry according to PostgreSQL", kind)
		}
		if kind != "new connection rejected" && event.err != nil {
			var netErr net.Error
			if !errors.Is(event.err, io.EOF) && !errors.Is(event.err, io.ErrUnexpectedEOF) && (!errors.As(event.err, &netErr) || netErr.Timeout()) {
				t.Fatalf("%s did not prove transport termination (timeouts do not count): %v", kind, event.err)
			}
		}
	}
	tcpDone, udpDone, newDenied := false, false, false
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	watchdog := time.NewTimer(90 * time.Second)
	defer watchdog.Stop()
	for !tcpDone || !udpDone || !newDenied {
		select {
		case event := <-tcpClosed:
			check("existing TCP stream terminated", event)
			tcpDone = true
		case event := <-udpClosed:
			check("existing UDP association terminated", event)
			udpDone = true
		case <-poll.C:
			if !newDenied {
				tr.CloseIdleConnections()
				if !dockerFetch(c) {
					check("new connection rejected", observe(nil))
					newDenied = true
				}
			}
			if now := serverNow(); now.After(expiry.Add(60 * time.Second)) {
				// Drain recorded disconnects before judging the remaining states.
				if !tcpDone {
					select {
					case event := <-tcpClosed:
						check("existing TCP stream terminated", event)
						tcpDone = true
					default:
					}
				}
				if !udpDone {
					select {
					case event := <-udpClosed:
						check("existing UDP association terminated", event)
						udpDone = true
					default:
					}
				}
				if !tcpDone || !udpDone || !newDenied {
					t.Fatalf("offline expiry exceeded server-clock 60s budget: TCP=%v UDP=%v new_denied=%v server_now=%s expiry=%s", tcpDone, udpDone, newDenied, now.Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano))
				}
			}
		case <-watchdog.C:
			t.Fatal("offline expiry harness watchdog expired; server-clock observation did not complete (not a local UTC expiry assertion)")
		}
	}
	d.remote("start panel")
	panelNeedsStart = false
	waitFor(t, 30*time.Second, "panel resumed", func() bool {
		r, e := d.api.client.Get(d.api.base + "/readyz")
		if e != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
	d.api.login("admin", d.password)
	d.memberPatch(map[string]any{"active": false, "expires_at": nil})
	d.applied()
	t.Log(d.remote("inspect"))
}
