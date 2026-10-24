// Command panel runs the proxypanel control plane.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"proxypanel/internal/panel/api"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/limit"
	"proxypanel/internal/panel/maintenance"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func loadAgentInstaller(publicURL string) (*api.AgentInstallerConfig, error) {
	scriptPath := strings.TrimSpace(os.Getenv("PANEL_AGENT_INSTALLER_PATH"))
	explicit := scriptPath != "" || strings.TrimSpace(os.Getenv("PANEL_AGENT_BINARY_AMD64")) != "" || strings.TrimSpace(os.Getenv("PANEL_AGENT_BINARY_ARM64")) != ""
	if scriptPath == "" {
		for _, candidate := range []string{"/app/install/deploy-agent.sh", "deploy/deploy-agent.sh"} {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				scriptPath = candidate
				break
			}
		}
	}
	artifacts := map[string]string{
		"amd64": strings.TrimSpace(os.Getenv("PANEL_AGENT_BINARY_AMD64")),
		"arm64": strings.TrimSpace(os.Getenv("PANEL_AGENT_BINARY_ARM64")),
	}
	defaults := map[string]string{
		"amd64": "/app/install/agents/linux-amd64/agent",
		"arm64": "/app/install/agents/linux-arm64/agent",
	}
	configured := false
	for arch, path := range artifacts {
		if path == "" {
			if info, err := os.Stat(defaults[arch]); err == nil && info.Mode().IsRegular() {
				artifacts[arch] = defaults[arch]
			}
		}
		if artifacts[arch] != "" {
			configured = true
		}
	}
	if scriptPath == "" || !configured {
		if explicit {
			return nil, errors.New("PANEL_AGENT_INSTALLER_PATH requires at least one PANEL_AGENT_BINARY_* artifact")
		}
		return nil, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(publicURL), "https://") {
		// A non-HTTPS public URL only disables the one-click installer (the
		// command would download over cleartext). Failing here would take the
		// whole panel down for a feature the operator simply cannot use yet.
		return nil, nil
	}
	return api.LoadAgentInstaller(scriptPath, publicURL, "26.3.27", artifacts)
}

func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// listenMode requires explicit opt-in for HTTP behind a TLS-terminating proxy.
// Development remains loopback-only; proxy deployments may use a container bridge.
func listenMode(dev bool) (tls bool, reason string, err error) {
	cert := strings.TrimSpace(os.Getenv("PANEL_TLS_CERT"))
	key := strings.TrimSpace(os.Getenv("PANEL_TLS_KEY"))
	if cert != "" || key != "" {
		if cert == "" || key == "" {
			return false, "", errors.New("PANEL_TLS_CERT and PANEL_TLS_KEY must be set together")
		}
		if _, statErr := os.Stat(cert); statErr != nil {
			return false, "", errors.New("PANEL_TLS_CERT is not readable: " + statErr.Error())
		}
		if _, statErr := os.Stat(key); statErr != nil {
			return false, "", errors.New("PANEL_TLS_KEY is not readable: " + statErr.Error())
		}
		return true, "direct HTTPS with PANEL_TLS_CERT/PANEL_TLS_KEY", nil
	}
	trustProxy := strings.EqualFold(env("PANEL_TRUST_REVERSE_PROXY", "false"), "true")
	if trustProxy {
		return false, "TLS terminated by a trusted reverse proxy", nil
	}
	if dev {
		return false, "development mode (PANEL_DEV=true)", nil
	}
	return false, "", errors.New("refusing cleartext HTTP in production: set PANEL_TLS_CERT/PANEL_TLS_KEY, " +
		"or PANEL_TRUST_REVERSE_PROXY=true behind a TLS-terminating proxy, or PANEL_DEV=true for local development")
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(os.Args) > 1 && os.Args[1] == "check" {
		os.Exit(runCheck(context.Background(), os.Args[2:], os.Stdout, os.Stderr))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dev := strings.EqualFold(env("PANEL_DEV", "false"), "true")
	listen := env("PANEL_LISTEN", "127.0.0.1:8080")
	host, _, splitErr := net.SplitHostPort(listen)
	if splitErr != nil {
		log.Fatalf("PANEL_LISTEN: %v", splitErr)
	}
	if dev && !isLoopback(host) {
		log.Fatal("PANEL_DEV requires a loopback PANEL_LISTEN address")
	}
	useTLS, modeReason, err := listenMode(dev)
	if err != nil {
		log.Fatalf("transport: %v", err)
	}
	webRoot := strings.TrimSpace(os.Getenv("PANEL_WEB_ROOT"))
	if webRoot != "" {
		if err := api.ValidateWebRoot(webRoot); err != nil {
			log.Fatalf("PANEL_WEB_ROOT=%q: %v", webRoot, err)
		}
	}

	key, err := secrets.ParseKey(os.Getenv("PANEL_MASTER_KEY"))
	if err != nil {
		log.Fatalf("PANEL_MASTER_KEY: %v", err)
	}
	st, err := store.Open(ctx, store.Options{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		MasterKey:           key,
		MigrationsDir:       env("PANEL_MIGRATIONS_DIR", "migrations"),
		MaxConns:            16,
		UpgradeEvidenceFile: os.Getenv("PANEL_UPGRADE_EVIDENCE_FILE"),
	})
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	if err := bootstrapAdmin(ctx, st); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}

	// 只在"反向代理终止 TLS"这条路径上允许监听非回环地址（容器里宿主机 nginx
	// 必须能连进来，容器内无法只绑 127.0.0.1）。clientIP/isTrustedProxy
	// 只采信来自回环、私网或链路本地对端的 XFF，但这不能替代网络隔离：
	// 宿主端口必须只发布到回环，容器网络不能接入不可信服务。
	//
	// 直接 TLS 不受此限制；开发明文仍由上面的检查限制为回环。
	if !useTLS && !dev && !isLoopback(host) {
		if !strings.EqualFold(env("PANEL_TRUST_REVERSE_PROXY", "false"), "true") {
			log.Fatalf("transport: cleartext HTTP on non-loopback %q requires either "+
				"PANEL_TLS_CERT/PANEL_TLS_KEY or PANEL_TRUST_REVERSE_PROXY=true", host)
		}
		log.Printf("transport: warning: listening on non-loopback %q; only loopback/private "+
			"peers' X-Forwarded-For is trusted, so the reverse proxy must reach the panel "+
			"over a private network", host)
	}
	log.Printf("transport: %s", modeReason)

	limiter, err := limit.New(os.Getenv("REDIS_URL"))
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer limiter.Close()
	if strings.TrimSpace(os.Getenv("REDIS_URL")) == "" {
		log.Printf("redis: no REDIS_URL configured, login rate limiting uses the bounded local fallback")
	}

	// 前后端分离部署的可选配置。默认值保持与之前完全一致：
	// 不配 PANEL_ALLOWED_ORIGINS 就没有 CORS，Cookie 仍是 Strict。
	cookieSameSite, err := cookieSameSiteFromEnv(dev)
	if err != nil {
		log.Fatalf("PANEL_COOKIE_SAMESITE: %v", err)
	}
	allowedOrigins := strings.TrimSpace(os.Getenv("PANEL_ALLOWED_ORIGINS"))
	if allowedOrigins != "" {
		log.Printf("cors: enabled for %s (前后端分离部署)", allowedOrigins)
	}

	// 前端静态托管是可选的：配了就由面板同时提供 SPA，不配就只提供 API。
	// 目录不可用时直接退出 —— 让它带着坏配置跑起来，表现是"页面打不开但
	// 容器健康"，比启动失败难查得多。
	if webRoot != "" {
		log.Printf("web: serving SPA from %s", webRoot)
	} else {
		log.Printf("web: PANEL_WEB_ROOT not set, serving API only (static files must be hosted separately)")
	}
	installer, err := loadAgentInstaller(os.Getenv("PANEL_PUBLIC_URL"))
	if err != nil {
		log.Fatalf("agent installer: %v", err)
	}
	if installer != nil {
		log.Printf("agent installer: enabled for %d architecture(s)", len(installer.Artifacts))
	}

	srv := &api.Server{
		Store:       st,
		RateLimiter: limiter,
		Cfg: api.Config{
			Dev: dev, Listen: listen, PublicURL: os.Getenv("PANEL_PUBLIC_URL"),
			RedisDSN: os.Getenv("REDIS_URL"), SessionTTL: 12 * time.Hour,
			AllowedOrigins: allowedOrigins,
			CookieSameSite: cookieSameSite,
			CookieDomain:   strings.TrimSpace(os.Getenv("PANEL_COOKIE_DOMAIN")),
			WebRoot:        webRoot,
			AgentInstaller: installer,
		},
	}
	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Durable alert reconciliation, notification outbox and safe history cleanup.
	go maintenance.New(st, nil).Run(ctx)

	// Dispatch/revocation SLA sweeper: unresolved tasks become timeout, they are
	// never reported as applied.
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if _, err := st.AdvancePeriods(ctx, now.UTC()); err != nil {
					log.Printf("period advance: %v", err)
				}

				timedOut, superseded, err := st.SweepTimeouts(ctx, now.UTC())
				if err != nil {
					log.Printf("sweep: %v", err)
					continue
				}
				if timedOut > 0 || superseded > 0 {
					log.Printf("sweep: %d task(s) timed out, %d item(s) superseded", timedOut, superseded)
				}
			}
		}
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdown
		cancel()
		shutCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	log.Printf("panel listening on %s (dev=%t, tls=%t)", listen, dev, useTLS)
	if useTLS {
		err = httpSrv.ListenAndServeTLS(os.Getenv("PANEL_TLS_CERT"), os.Getenv("PANEL_TLS_KEY"))
	} else {
		err = httpSrv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
}

func bootstrapAdmin(ctx context.Context, st *store.Store) error {
	total, _, err := st.CountMembers(ctx)
	if err != nil || total > 0 {
		return err
	}
	username := strings.TrimSpace(os.Getenv("PANEL_ADMIN_USER"))
	password := os.Getenv("PANEL_ADMIN_PASSWORD")
	if username == "" || password == "" {
		log.Printf("no members yet: set PANEL_ADMIN_USER/PANEL_ADMIN_PASSWORD to bootstrap the first administrator")
		return nil
	}
	hash, err := secrets.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := st.CreateMember(ctx, &domain.Member{
		ID: ids.New(ids.Member), Username: username, PasswordHash: hash,
		Role: "admin", Active: true, ProxyEnabled: true,
	}); err != nil {
		return err
	}
	log.Printf("bootstrap administrator %q created", username)
	return nil
}

// cookieSameSiteFromEnv 把 PANEL_COOKIE_SAMESITE 转成 http.SameSite。
//
// 关键校验：SameSite=None 必须配 Secure。浏览器会**静默丢弃**不满足这个组合的
// Cookie，表现为"登录接口返回 200 但下一个请求就是 401"，极难排查。所以这里
// 直接让进程启动失败，而不是让线上出现这种幽灵问题。
func cookieSameSiteFromEnv(dev bool) (http.SameSite, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("PANEL_COOKIE_SAMESITE")))
	switch raw {
	case "":
		return http.SameSiteStrictMode, nil
	case "strict":
		return http.SameSiteStrictMode, nil
	case "lax":
		return http.SameSiteLaxMode, nil
	case "none":
		if dev {
			return 0, fmt.Errorf("SameSite=None 需要 Secure Cookie，而 PANEL_DEV=true 时 Cookie 不带 Secure；请关闭 PANEL_DEV 或改用 lax")
		}
		// 非 dev 模式下 Secure 恒为 true（见 handlers.go 的 !s.Cfg.Dev），组合成立。
		return http.SameSiteNoneMode, nil
	default:
		return 0, fmt.Errorf("must be strict, lax or none (got %q)", raw)
	}
}
