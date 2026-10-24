package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"proxypanel/internal/agent/control"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	agentconfig "proxypanel/internal/agent/config"
	"proxypanel/internal/agent/supervisor"
	"proxypanel/internal/panel/api"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

type dataPlaneAPI struct {
	t      *testing.T
	base   string
	client *http.Client
	csrf   string
}

func (a *dataPlaneAPI) request(method, path string, body any) map[string]any {
	a.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		a.t.Fatal(err)
	}
	req, err := http.NewRequest(method, a.base+path, bytes.NewReader(raw))
	if err != nil {
		a.t.Fatal("construct API request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", a.csrf)
	if path == "/api/v1/admin/batches" {
		req.Header.Set("Idempotency-Key", fmt.Sprintf("e2e-%d", time.Now().UnixNano()))
	}
	res, err := a.client.Do(req)
	if err != nil {
		a.t.Fatalf("API %s failed", method)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		a.t.Fatalf("API %s response is not JSON", method)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := "unknown"
		if e, ok := out["error"].(map[string]any); ok {
			code, _ = e["code"].(string)
		}
		a.t.Fatalf("API %s %s returned %d (%s)", method, path, res.StatusCode, code)
	}
	data, ok := out["data"].(map[string]any)
	if !ok {
		a.t.Fatal("API response lacks data object")
	}
	return data
}
func (a *dataPlaneAPI) login(username, password string) {
	data := a.request("POST", "/api/v1/auth/login", map[string]any{"username": username, "password": password})
	a.csrf, _ = data["csrf_token"].(string)
	if a.csrf == "" {
		a.t.Fatal("missing CSRF token")
	}
}

// TestDataPlaneProductReadiness is a real loopback DEV data chain, not an
// HTTPS deployment claim. Only bootstrap admin is seeded; all business objects
// are created by the real API. No synthetic samples or ledger writes.
func TestDataPlaneProductReadiness(t *testing.T) {
	dsn := os.Getenv("PANEL_TEST_DATABASE_URL")
	redisURL := "" // process-local limiter: no shared Redis writes
	bin := os.Getenv("XRAY_BIN")
	if dsn == "" || bin == "" {
		t.Fatal("not_run: PANEL_TEST_DATABASE_URL and XRAY_BIN required")
	}
	dbc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	if dbc.Host != "127.0.0.1" || !strings.HasPrefix(dbc.Database, "dp_owned_") {
		t.Fatal("refusing non-project test PostgreSQL")
	}
	ctx := context.Background()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("connect isolated PostgreSQL failed")
	}
	defer db.Close(ctx)
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "ppe2e_" + hex.EncodeToString(random)
	t.Logf("owned test schema: %s", schema)
	if _, err := db.Exec(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := db.Exec(clean, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error("test schema cleanup failed")
		} else {
			t.Log("cleanup: owned schema dropped")
		}
	}()
	key, err := secrets.ParseKey(strings.Repeat("e2e-only-random-schema-", 3))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{DatabaseURL: dsn, MasterKey: key, MigrationsDir: "../../migrations", SearchPath: schema})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	password := "E2e-only-password-" + hex.EncodeToString(random)
	hash, err := secrets.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateMember(ctx, &domain.Member{ID: ids.New(ids.Member), Username: "admin", PasswordHash: hash, Role: "admin", Active: true, ProxyEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := limit.New(redisURL)
	if err != nil {
		t.Fatal("create Redis limiter failed")
	}
	defer limiter.Close()
	panel := &api.Server{Store: st, RateLimiter: limiter, Cfg: api.Config{Dev: true, Listen: "127.0.0.1:8080", RedisDSN: redisURL, SessionTTL: time.Hour}}
	server := httptest.NewServer(panel.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	admin := &dataPlaneAPI{t: t, base: server.URL, client: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
	admin.login("admin", password)
	srv := admin.request("POST", "/api/v1/admin/servers", map[string]any{"name": "local-e2e", "address": "127.0.0.1", "region": "test"})
	inbound := unusedPort(t)
	node := admin.request("POST", "/api/v1/admin/nodes", map[string]any{
		"name": "local-vless", "server_id": srv["id"], "enabled": true, "host": "127.0.0.1", "listen": "127.0.0.1", "port": inbound, "protocol": "vless",
		"stream_settings": map[string]any{"network": "tcp", "security": "none"}, "protocol_settings": map[string]any{"decryption": "none"}, "client_settings": map[string]any{},
	})
	group := admin.request("POST", "/api/v1/admin/node-groups", map[string]any{"name": "e2e-group", "node_ids": []any{node["id"]}})
	sub := admin.request("POST", "/api/v1/admin/subscriptions", map[string]any{"name": "e2e-sub", "node_group_ids": []any{group["id"]}, "quota_bytes": 1 << 30, "timezone": "Asia/Shanghai", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true})
	member := admin.request("POST", "/api/v1/admin/members", map[string]any{"username": "alice", "password": password, "role": "member", "subscription_id": sub["id"]})
	registration := admin.request("POST", "/api/v1/admin/servers/"+srv["id"].(string)+"/registration-codes", nil)
	audit := &dataPlaneUsageAudit{st: st}
	ackProxy, err := newAckProxy(server.URL, audit)
	if err != nil {
		t.Fatal(err)
	}
	defer ackProxy.Close()
	controlClient, err := control.New(ackProxy.URL(), agentconfig.AgentVersion)
	if err != nil {
		t.Fatal(err)
	}
	ackHTTPClient := ackProxy.Client()
	defer ackHTTPClient.CloseIdleConnections()
	controlClient.SetHTTPClient(ackHTTPClient)
	cfg := &agentconfig.Config{PanelURL: ackProxy.URL(), RegistrationCode: registration["code"].(string), XrayPath: bin, StateDir: filepath.Join(t.TempDir(), "agent"), AllowLoopbackHTTP: true, PollIntervalSeconds: 1, StatsIntervalSeconds: 1, HeartbeatIntervalSeconds: 1}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	var agentLogs bytes.Buffer
	sup, err := supervisor.New(supervisor.Options{Config: cfg, Client: controlClient, Logger: log.New(&agentLogs, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	agentCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- sup.Run(agentCtx) }()
	defer func() {
		stop()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("agent did not stop")
		}
		sup.Close()
		if t.Failed() {
			t.Log("sanitized Agent diagnostics:\n" + regexp.MustCompile(`[A-Za-z0-9_+/=-]{24,}`).ReplaceAllString(agentLogs.String(), "[redacted]"))
		}
		t.Log("cleanup: owned Agent and server core stopped")
	}()
	waitFor(t, 30*time.Second, "agent registered and snapshot applied", func() bool {
		var n int
		err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE applied_revision > 0`).Scan(&n)
		return err == nil && n > 0
	})
	t.Log("real API registration and Agent snapshot application verified")
	memberJar, _ := cookiejar.New(nil)
	user := &dataPlaneAPI{t: t, base: server.URL, client: &http.Client{Jar: memberJar, Timeout: 10 * time.Second}}
	user.login("alice", password)
	subscription := user.request("GET", "/api/v1/me/subscription", nil)
	token, ok := subscription["token"].(string)
	if !ok || token == "" {
		t.Fatal("member subscription token missing")
	}
	export, err := http.Get(server.URL + "/sub/" + token + "?format=xray")
	if err != nil {
		t.Fatal("subscription download failed")
	}
	defer export.Body.Close()
	if export.StatusCode != 200 {
		t.Fatalf("subscription export status %d", export.StatusCode)
	}
	var clientConfig map[string]any
	if err := json.NewDecoder(export.Body).Decode(&clientConfig); err != nil {
		t.Fatal("invalid Xray export")
	}
	exportFingerprint, err := json.Marshal(clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual initial Xray export sha256=%x", sha256.Sum256(exportFingerprint))
	socksPort := unusedPort(t)
	clientConfig["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}
	// Production exports bypass private targets. This test is intentionally
	// all-loopback, so force traffic through the actual exported proxy outbound.
	outbounds, ok := clientConfig["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		t.Fatal("export missing proxy outbound")
	}
	firstOutbound := outbounds[0].(map[string]any)
	if firstOutbound["protocol"] != "vless" {
		t.Fatal("first outbound is not the member proxy")
	}
	clientConfig["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": firstOutbound["tag"]}}}
	path := writeConfig(t, t.TempDir(), "exported-client.json", clientConfig)
	startCore(t, bin, path, socksPort)
	proxy := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", socksPort)})}
	defer proxy.CloseIdleConnections()
	proxyClient := &http.Client{Transport: proxy, Timeout: 10 * time.Second}
	payload := strings.Repeat("verified-payload-", 4096) // exactly 69632 bytes
	if len(payload) != 69632 {
		t.Fatal("fixture length changed")
	}
	expectedHash := sha256.Sum256([]byte(payload))
	t.Logf("fixture bytes=%d sha256=%x", len(payload), expectedHash)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream" {
			_, _ = io.WriteString(w, payload)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			if _, err := io.WriteString(w, "persistent-connection\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer target.Close()
	res, err := proxyClient.Get(target.URL)
	if err != nil {
		t.Fatal("proxy request using actual subscription export failed")
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK || len(body) != 69632 || sha256.Sum256(body) != expectedHash {
		t.Fatal("proxied payload mismatch")
	}
	waitFor(t, 20*time.Second, "real usage committed to PostgreSQL", func() bool {
		var total int64
		err := st.DB.QueryRowContext(ctx, `SELECT coalesce(sum(down_bytes),0) FROM usage_entries WHERE member_id=$1`, member["id"]).Scan(&total)
		return err == nil && total >= int64(len(payload))
	})
	t.Logf("exported Xray config transferred %d bytes; usage persisted by Agent", len(payload))
	waitFor(t, 30*time.Second, "lost ACK resent without double credit", func() bool { return audit.verified() })
	if ackProxy.droppedCount() != 1 {
		t.Fatal("expected exactly one lost committed ACK")
	}
	t.Log("real Agent sampler/outbox: accepted ACK lost, identical batch resent, duplicate ACK; per-batch PG rows and bytes unchanged")
	audit.mu.Lock()
	for _, credit := range audit.first {
		t.Logf("real committed batch payload sha256=%x rows=%d bytes=%d", credit.payload, credit.rows, credit.total)
	}
	audit.mu.Unlock()
	status := user.request("GET", "/api/v1/me/status", nil)
	var ledgerTotal int64
	if err := st.DB.QueryRowContext(ctx, `SELECT coalesce(sum(up_bytes+down_bytes),0) FROM usage_entries WHERE member_id=$1`, member["id"]).Scan(&ledgerTotal); err != nil {
		t.Fatal(err)
	}
	gotMember := status["member"].(map[string]any)
	if gotMember["used_bytes"] != float64(ledgerTotal) || ledgerTotal < 69632 {
		t.Fatalf("member API != PG: API=%v PG=%d", gotMember["used_bytes"], ledgerTotal)
	}
	t.Logf("member API used_bytes == PG usage ledger: %d", ledgerTotal)
	// No Client.Timeout: the deadline below must measure revocation, not a client timer.
	streaming := &http.Client{Transport: proxy}
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	streamReq, _ := http.NewRequestWithContext(streamCtx, "GET", target.URL+"/stream", nil)
	stream, err := streaming.Do(streamReq)
	if err != nil {
		t.Fatal("open persistent proxied stream failed")
	}
	defer stream.Body.Close()
	first := make([]byte, 22)
	if _, err := io.ReadFull(stream.Body, first); err != nil {
		t.Fatal("persistent stream never became active")
	}
	streamDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stream.Body); close(streamDone) }()
	preview := admin.request("POST", "/api/v1/admin/batches/preview", map[string]any{"operation": "set_active", "member_ids": []any{member["id"]}, "values": map[string]any{"active": false}})
	revoked := time.Now()
	admin.request("POST", "/api/v1/admin/batches", map[string]any{"preview_id": preview["id"], "confirm": true})
	select {
	case <-streamDone:
		t.Logf("existing TCP stream terminated %.3fs after batch revocation", time.Since(revoked).Seconds())
	case <-time.After(60 * time.Second):
		t.Fatal("existing connection survived the 60s hard-revocation budget")
	}
	proxy.CloseIdleConnections()
	proxyClient.Timeout = 3 * time.Second
	if res, err := proxyClient.Get(target.URL); err == nil {
		res.Body.Close()
		t.Fatal("new proxy request succeeded after revocation")
	}
	denied, err := http.Get(server.URL + "/sub/" + token + "?format=xray")
	if err != nil {
		t.Fatal("check revoked subscription failed")
	}
	denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked subscription status %d, want 403", denied.StatusCode)
	}
	if time.Since(revoked) > 60*time.Second {
		t.Fatal("new/old denial exceeded shared 60s budget")
	}
	t.Logf("new + existing connections denied within %.3fs; export=403", time.Since(revoked).Seconds())
	preview = admin.request("POST", "/api/v1/admin/batches/preview", map[string]any{"operation": "set_active", "member_ids": []any{member["id"]}, "values": map[string]any{"active": true}})
	admin.request("POST", "/api/v1/admin/batches", map[string]any{"preview_id": preview["id"], "confirm": true})
	restoredMember, err := st.GetMember(ctx, member["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("restoration member active=%v proxy_enabled=%v", restoredMember.Active, restoredMember.ProxyEnabled)
	// Batch access reconciliation permanently revokes credentials on disable.
	// Restoring access requires importing the newly exported credential, while
	// the old client must remain denied (not merely eventually become usable).
	if res, err := proxyClient.Get(target.URL); err == nil {
		res.Body.Close()
		t.Fatal("permanently revoked credential regained access")
	}
	restoredExport, err := http.Get(server.URL + "/sub/" + token + "?format=xray")
	if err != nil {
		t.Fatal("restored export download failed")
	}
	var restoredConfig map[string]any
	if restoredExport.StatusCode != 200 {
		restoredExport.Body.Close()
		t.Fatalf("restored export status=%d", restoredExport.StatusCode)
	}
	err = json.NewDecoder(restoredExport.Body).Decode(&restoredConfig)
	restoredExport.Body.Close()
	if err != nil {
		t.Fatal("restored export invalid")
	}
	restoredFingerprint, err := json.Marshal(restoredConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual restored Xray export sha256=%x", sha256.Sum256(restoredFingerprint))
	restoredOut := restoredConfig["outbounds"].([]any)[0].(map[string]any)
	if restoredOut["protocol"] != "vless" {
		t.Fatal("restored outbound not VLESS")
	}
	restoredPort := unusedPort(t)
	restoredConfig["inbounds"] = []any{map[string]any{"listen": "127.0.0.1", "port": restoredPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}
	restoredConfig["routing"] = map[string]any{"rules": []any{map[string]any{"type": "field", "network": "tcp,udp", "outboundTag": restoredOut["tag"]}}}
	startCore(t, bin, writeConfig(t, t.TempDir(), "restored-client.json", restoredConfig), restoredPort)
	restoredTransport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", restoredPort)})}
	defer restoredTransport.CloseIdleConnections()
	restoredClient := &http.Client{Transport: restoredTransport, Timeout: 3 * time.Second}
	waitFor(t, 60*time.Second, "real transfer restored", func() bool {
		restoredTransport.CloseIdleConnections()
		res, err := restoredClient.Get(target.URL)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return err == nil && res.StatusCode == 200 && len(b) == 69632 && sha256.Sum256(b) == expectedHash
	})
	t.Log("restored: newly exported Xray credentials transfer 69632 bytes with matching SHA256; permanently revoked old credentials still denied")
}

// Observes the real upstream response and read-only PG ledger at commit time.
// The reused TLS ACK proxy drops only the first accepted response.
type dataPlaneCredit struct {
	payload     [32]byte
	rows, total int64
}
type dataPlaneUsageAudit struct {
	st        *store.Store
	mu        sync.Mutex
	first     map[string]dataPlaneCredit
	duplicate bool
}

func (a *dataPlaneUsageAudit) RoundTrip(r *http.Request) (*http.Response, error) {
	var raw []byte
	if r.URL.Path == control.PathUsage {
		raw, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	res, err := http.DefaultTransport.RoundTrip(r)
	if err != nil || r.URL.Path != control.PathUsage {
		return res, err
	}
	answer, err := io.ReadAll(res.Body)
	res.Body.Close()
	res.Body = io.NopCloser(bytes.NewReader(answer))
	if err != nil {
		return res, err
	}
	var batch struct {
		BatchID string `json:"batch_id"`
	}
	var reply struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &batch) != nil || json.Unmarshal(answer, &reply) != nil {
		return res, nil
	}
	var c dataPlaneCredit
	c.payload = sha256.Sum256(raw)
	if err := a.st.DB.QueryRowContext(r.Context(), `SELECT count(*),coalesce(sum(up_bytes+down_bytes),0) FROM usage_entries WHERE batch_id=$1`, batch.BatchID).Scan(&c.rows, &c.total); err != nil {
		return res, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.first == nil {
		a.first = make(map[string]dataPlaneCredit)
	}
	if reply.Data.Status == "accepted" {
		a.first[batch.BatchID] = c
	}
	if reply.Data.Status == "duplicate" {
		old, ok := a.first[batch.BatchID]
		a.duplicate = ok && old == c && c.rows > 0 && c.total > 0
	}
	return res, nil
}
func (a *dataPlaneUsageAudit) verified() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.duplicate }
