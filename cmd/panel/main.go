// Command panel runs the proxypanel control plane.
package main

import (
	"context"
	"errors"
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

func isLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// listenMode decides how the process exposes HTTP. Plain cleartext to a public
// interface is only possible when it is explicitly a trusted reverse proxy
// setup on loopback, or in development mode.
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
		return false, "TLS terminated by a trusted reverse proxy on loopback", nil
	}
	if dev {
		return false, "development mode (PANEL_DEV=true)", nil
	}
	return false, "", errors.New("refusing cleartext HTTP in production: set PANEL_TLS_CERT/PANEL_TLS_KEY, " +
		"or PANEL_TRUST_REVERSE_PROXY=true with PANEL_LISTEN on a loopback address, or PANEL_DEV=true for local development")
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
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

	key, err := secrets.ParseKey(os.Getenv("PANEL_MASTER_KEY"))
	if err != nil {
		log.Fatalf("PANEL_MASTER_KEY: %v", err)
	}
	st, err := store.Open(ctx, store.Options{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		MasterKey:     key,
		MigrationsDir: env("PANEL_MIGRATIONS_DIR", "migrations"),
		MaxConns:      16,
	})
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer st.Close()

	if err := bootstrapAdmin(ctx, st); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}

	useTLS, modeReason, err := listenMode(dev)
	if err != nil {
		log.Fatalf("transport: %v", err)
	}
	if !useTLS && !dev && !isLoopback(host) {
		log.Fatalf("transport: PANEL_TRUST_REVERSE_PROXY requires PANEL_LISTEN on a loopback address, got %q", host)
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

	srv := &api.Server{
		Store:       st,
		RateLimiter: limiter,
		Cfg: api.Config{
			Dev: dev, Listen: listen, PublicURL: os.Getenv("PANEL_PUBLIC_URL"),
			RedisDSN: os.Getenv("REDIS_URL"), SessionTTL: 12 * time.Hour,
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
