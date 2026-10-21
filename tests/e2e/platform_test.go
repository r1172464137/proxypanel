package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"strings"
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

type platformAPI struct {
	t      *testing.T
	base   string
	client *http.Client
	csrf   string
}

func (a *platformAPI) request(method, path string, body any) map[string]any {
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
func (a *platformAPI) login(username, password string) {
	data := a.request("POST", "/api/v1/auth/login", map[string]any{"username": username, "password": password})
	a.csrf, _ = data["csrf_token"].(string)
	if a.csrf == "" {
		a.t.Fatal("missing CSRF token")
	}
}

// TestPlatformRealCore uses only the explicitly allowed isolated PostgreSQL and
// Redis endpoints. All application objects live in a random schema which is
// removed at completion. Agent and both Xray processes run on local Windows.
func TestPlatformRealCore(t *testing.T) {
	dsn := os.Getenv("PANEL_TEST_DATABASE_URL")
	redisURL := os.Getenv("PANEL_TEST_REDIS_URL")
	bin := os.Getenv("XRAY_BIN")
	if dsn == "" || redisURL == "" || bin == "" {
		t.Skip("PANEL_TEST_DATABASE_URL, PANEL_TEST_REDIS_URL and XRAY_BIN required")
	}
	dbc, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	if dbc.Host != "127.0.0.1" || dbc.Port != 15432 || dbc.Database != "proxypanel" {
		t.Fatal("refusing non-project test PostgreSQL")
	}
	ru, err := url.Parse(redisURL)
	if err != nil || ru.Host != "127.0.0.1:16379" || ru.Path != "/0" {
		t.Fatal("refusing non-project test Redis")
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
	server := httptest.NewServer(browserHandler(t, panel.Handler()))
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	admin := &platformAPI{t: t, base: server.URL, client: &http.Client{Jar: jar, Timeout: 15 * time.Second}}
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
	runBrowser(t, server.URL, password)
	if os.Getenv("PANEL_BROWSER_ONLY") == "1" {
		return
	}
	registration := admin.request("POST", "/api/v1/admin/servers/"+srv["id"].(string)+"/registration-codes", nil)
	cfg := &agentconfig.Config{PanelURL: server.URL, RegistrationCode: registration["code"].(string), XrayPath: bin, StateDir: filepath.Join(t.TempDir(), "agent"), AllowLoopbackHTTP: true, PollIntervalSeconds: 1, StatsIntervalSeconds: 1, HeartbeatIntervalSeconds: 1}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	sup, err := supervisor.New(supervisor.Options{Config: cfg, Logger: log.New(io.Discard, "", 0)})
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
	}()
	waitFor(t, 30*time.Second, "agent registered and snapshot applied", func() bool {
		var n int
		err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE applied_revision > 0`).Scan(&n)
		return err == nil && n > 0
	})
	t.Log("real API registration and Agent snapshot application verified")
	memberJar, _ := cookiejar.New(nil)
	user := &platformAPI{t: t, base: server.URL, client: &http.Client{Jar: memberJar, Timeout: 10 * time.Second}}
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
	payload := strings.Repeat("verified-payload-", 4096)
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
	if err != nil || string(body) != payload {
		t.Fatal("proxied payload mismatch")
	}
	waitFor(t, 20*time.Second, "real usage committed to PostgreSQL", func() bool {
		var total int64
		err := st.DB.QueryRowContext(ctx, `SELECT coalesce(sum(down_bytes),0) FROM usage_entries WHERE member_id=$1`, member["id"]).Scan(&total)
		return err == nil && total >= int64(len(payload))
	})
	t.Logf("exported Xray config transferred %d bytes; usage persisted by Agent", len(payload))
	udpAssociation := openVLESSUDP(t, inbound, firstOutbound)
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
	assertUDPRevoked(t, udpAssociation, revoked)
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
	t.Log("revoked credentials reject new connections and subscription export returns 403")
}
func waitFor(t *testing.T, budget time.Duration, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
