package main

import (
	"strings"
	"testing"
)

func TestTransportRequiresExplicitHTTPOptIn(t *testing.T) {
	t.Setenv("PANEL_TLS_CERT", "")
	t.Setenv("PANEL_TLS_KEY", "")
	t.Setenv("PANEL_TRUST_REVERSE_PROXY", "false")
	if _, _, err := listenMode(false); err == nil {
		t.Fatal("production cleartext must be rejected by default")
	}
	t.Setenv("PANEL_TRUST_REVERSE_PROXY", "true")
	tls, reason, err := listenMode(false)
	if err != nil || tls || !strings.Contains(reason, "reverse proxy") {
		t.Fatalf("proxy HTTP: tls=%v reason=%q err=%v", tls, reason, err)
	}
	if strings.Contains(reason, "loopback") {
		t.Fatal("proxy transport incorrectly claims loopback-only")
	}
}

func TestTransportRejectsIncompleteTLS(t *testing.T) {
	t.Setenv("PANEL_TLS_CERT", "/missing/cert")
	t.Setenv("PANEL_TLS_KEY", "")
	t.Setenv("PANEL_TRUST_REVERSE_PROXY", "true")
	if _, _, err := listenMode(false); err == nil {
		t.Fatal("partial TLS must not silently downgrade to proxy HTTP")
	}
}
