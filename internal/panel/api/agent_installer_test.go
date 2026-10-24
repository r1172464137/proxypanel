package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentInstallerServesRenderedScriptAndPinnedBinaries(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "deploy-agent.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/env bash\necho installer-body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	amdPath := filepath.Join(dir, "agent-amd64")
	armPath := filepath.Join(dir, "agent-arm64")
	if err := os.WriteFile(amdPath, []byte("owned-amd64-agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(armPath, []byte("owned-arm64-agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgentInstaller(scriptPath, "https://panel.example.com", "26.3.27", map[string]string{
		"amd64": amdPath, "arm64": armPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Cfg: Config{AgentInstaller: cfg}}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	res, err := http.Get(ts.URL + "/install-agent.sh")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	text := string(body)
	if res.StatusCode != http.StatusOK || !strings.Contains(text, "PROXYPANEL_INSTALL_PANEL_URL='https://panel.example.com'") ||
		!strings.Contains(text, "PROXYPANEL_INSTALL_AGENT_SHA256_AMD64='") || !strings.Contains(text, "echo installer-body") {
		t.Fatalf("rendered installer status=%d body=%q", res.StatusCode, text)
	}
	if got := res.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("script cache control=%q", got)
	}

	binary, err := http.Get(ts.URL + "/install-agent/linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(binary.Body)
	binary.Body.Close()
	if binary.StatusCode != http.StatusOK || string(raw) != "owned-amd64-agent" {
		t.Fatalf("binary status=%d body=%q", binary.StatusCode, raw)
	}
	if got := binary.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("mutable installer artifact cache control=%q", got)
	}
	sum := sha256.Sum256(raw)
	if cfg.Artifacts["amd64"].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("served binary does not match advertised SHA256")
	}
	missing, _ := http.Get(ts.URL + "/install-agent/linux-riscv64")
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unsupported architecture status=%d", missing.StatusCode)
	}
}

func TestAgentInstallCommandUsesOnlyOneTimeCode(t *testing.T) {
	s := &Server{Cfg: Config{AgentInstaller: &AgentInstallerConfig{
		PanelURL: "https://panel.example.com", Artifacts: map[string]AgentInstallerArtifact{"amd64": {}},
	}}}
	command, available, reason := s.agentInstallCommand("short-lived-key")
	if !available || reason != "" {
		t.Fatalf("installer unavailable: %q", reason)
	}
	for _, want := range []string{
		"https://panel.example.com/install-agent.sh",
		"sudo bash /tmp/proxypanel-agent-install.sh -key 'short-lived-key'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q: %s", want, command)
		}
	}
	if strings.Contains(command, "agent token") || strings.Contains(command, "PANEL_MASTER_KEY") {
		t.Fatal("install command contains a permanent secret")
	}
}

func TestLoadAgentInstallerFailsClosed(t *testing.T) {
	if cfg, err := LoadAgentInstaller("", "https://panel.example.com", "26.3.27", nil); err != nil || cfg != nil {
		t.Fatalf("empty installer should be disabled: cfg=%v err=%v", cfg, err)
	}
	script := filepath.Join(t.TempDir(), "installer.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentInstaller(script, "http://panel.example.com", "26.3.27", map[string]string{"amd64": script}); err == nil {
		t.Fatal("cleartext public URL enabled one-click installer")
	}
	if _, err := LoadAgentInstaller(script, "https://panel.example.com", "26.3.27", map[string]string{"amd64": filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing binary enabled one-click installer")
	}
}
