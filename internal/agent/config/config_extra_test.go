package config

import (
	"path/filepath"
	"testing"
)

func TestFromEnvBuildsEquivalentConfig(t *testing.T) {
	t.Setenv("PANEL_URL", "https://panel.example.test")
	t.Setenv("REGISTRATION_CODE", "one-time-code")
	t.Setenv("XRAY_PATH", "/opt/xray/xray")
	t.Setenv("STATE_DIR", "/var/lib/proxypanel-agent")
	t.Setenv("POLL_INTERVAL_SECONDS", "7")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PanelURL != "https://panel.example.test" || cfg.RegistrationCode != "one-time-code" {
		t.Fatalf("identity: %+v", cfg)
	}
	if cfg.XrayPath != "/opt/xray/xray" || !filepath.IsAbs(cfg.StateDir) {
		t.Fatalf("paths: %+v", cfg)
	}
	if cfg.PollIntervalSeconds != 7 || cfg.StatsIntervalSeconds != DefaultStatsInterval || cfg.HeartbeatIntervalSeconds != DefaultHeartbeatInterval {
		t.Fatalf("intervals: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("env config must pass the same validation as file config: %v", err)
	}
}

func TestFromEnvAppliesContainerDefaults(t *testing.T) {
	t.Setenv("PANEL_URL", "https://panel.example.test")
	t.Setenv("XRAY_PATH", "")
	t.Setenv("STATE_DIR", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.XrayPath != "/opt/xray/xray" || cfg.StateDir != "/var/lib/proxypanel-agent" {
		t.Fatalf("defaults: %+v", cfg)
	}
}

func TestFromEnvRejectsCleartextNonLoopbackPanel(t *testing.T) {
	t.Setenv("PANEL_URL", "http://panel.example.test")
	t.Setenv("XRAY_PATH", "/opt/xray/xray")
	if _, err := FromEnv(); err == nil {
		t.Fatal("plaintext public panel URL must be rejected, as with file configuration")
	}
}

func TestFromEnvAvailableRequiresPanelURL(t *testing.T) {
	t.Setenv("PANEL_URL", "")
	if FromEnvAvailable() {
		t.Fatal("empty PANEL_URL must not silently start the agent")
	}
	t.Setenv("PANEL_URL", "https://panel.example.test")
	if !FromEnvAvailable() {
		t.Fatal("PANEL_URL should enable environment configuration")
	}
}
