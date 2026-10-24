package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentInstallerFromExplicitReleaseFiles(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "deploy-agent.sh")
	amd := filepath.Join(dir, "agent-amd64")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(amd, []byte("agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANEL_AGENT_INSTALLER_PATH", script)
	t.Setenv("PANEL_AGENT_BINARY_AMD64", amd)
	t.Setenv("PANEL_AGENT_BINARY_ARM64", "")
	cfg, err := loadAgentInstaller("https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Artifacts) != 1 || cfg.Artifacts["amd64"].SHA256 == "" {
		t.Fatalf("installer config=%+v", cfg)
	}
}

func TestLoadAgentInstallerExplicitConfigFailsClosed(t *testing.T) {
	t.Setenv("PANEL_AGENT_INSTALLER_PATH", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("PANEL_AGENT_BINARY_AMD64", "")
	if _, err := loadAgentInstaller("https://panel.example.com"); err == nil {
		t.Fatal("explicit installer without artifacts did not fail")
	}
}
