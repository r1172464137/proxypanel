package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The panel must keep serving when only the optional installer cannot be built.
func TestLoadAgentInstallerNonHTTPSDisablesInsteadOfFailing(t *testing.T) {
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
	for _, publicURL := range []string{"http://10.144.144.1:8081", "", "ftp://x"} {
		cfg, err := loadAgentInstaller(publicURL)
		if err != nil {
			t.Fatalf("public URL %q must disable the installer, not kill the panel: %v", publicURL, err)
		}
		if cfg != nil {
			t.Fatalf("public URL %q must not enable one-click install", publicURL)
		}
	}
}
