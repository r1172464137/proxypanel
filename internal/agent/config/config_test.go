package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// absTestPath builds a platform-appropriate absolute path so the config tests
// exercise Normalize() rather than the host's notion of an absolute path. A
// hard-coded `C:\...` literal is not absolute on Linux and would make the
// production check look broken on non-Windows runners.
func absTestPath(t *testing.T, elem ...string) string {
	t.Helper()
	root := "/"
	if runtime.GOOS == "windows" {
		root = t.TempDir()
	}
	return filepath.Join(append([]string{root}, elem...)...)
}

// jsonPath renders a path for embedding inside a JSON string literal.
func jsonPath(p string) string {
	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, fmt.Sprintf(`{
		"panel_url": "https://panel.example.test/",
		"xray_path": %s,
		"state_dir": "  state  "
	}`, jsonPath(absTestPath(t, "SDK", "xray.exe"))))
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PanelURL != "https://panel.example.test" {
		t.Fatalf("panel_url = %q", cfg.PanelURL)
	}
	if cfg.PollIntervalSeconds != DefaultPollInterval || cfg.StatsIntervalSeconds != DefaultStatsInterval || cfg.HeartbeatIntervalSeconds != DefaultHeartbeatInterval {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
	if !filepath.IsAbs(cfg.StateDir) {
		t.Fatalf("state_dir was not resolved to an absolute path: %q", cfg.StateDir)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := writeConfig(t, fmt.Sprintf(`{"panel_url":"https://p.test","xray_path":%s,"state_dir":"s","exec_shell":"rm -rf /"}`,
		jsonPath(absTestPath(t, "x.exe"))))
	if _, err := Load(path); err == nil {
		t.Fatal("unknown field must be rejected")
	}
}

func TestPlainHTTPRequiresLoopbackOptIn(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		allow   bool
		wantErr bool
	}{
		{"https ok", "https://panel.example.test", false, false},
		{"http without opt-in", "http://127.0.0.1:8080", false, true},
		{"http loopback opt-in", "http://127.0.0.1:8080", true, false},
		{"http localhost opt-in", "http://localhost:8080", true, false},
		{"http ipv6 loopback", "http://[::1]:8080", true, false},
		{"http remote", "http://panel.example.test", true, true},
		{"http private ip", "http://192.168.1.10:8080", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{PanelURL: tc.url, XrayPath: absTestPath(t, "x.exe"), StateDir: t.TempDir(), AllowLoopbackHTTP: tc.allow}
			err := cfg.Normalize()
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %s", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateRejectsUnsafeInput(t *testing.T) {
	base := func() *Config {
		return &Config{PanelURL: "https://p.test", XrayPath: absTestPath(t, "x.exe"), StateDir: absTestPath(t, "state")}
	}
	t.Run("relative xray path", func(t *testing.T) {
		c := base()
		c.XrayPath = "xray"
		if err := c.Normalize(); err == nil {
			t.Fatal("relative xray_path must be rejected")
		}
	})
	t.Run("query in panel url", func(t *testing.T) {
		c := base()
		c.PanelURL = "https://p.test/?redirect=http://evil"
		if err := c.Normalize(); err == nil {
			t.Fatal("query in panel_url must be rejected")
		}
	})
	t.Run("credentials in panel url", func(t *testing.T) {
		c := base()
		c.PanelURL = "https://user:pass@p.test"
		if err := c.Normalize(); err == nil {
			t.Fatal("credentials in panel_url must be rejected")
		}
	})
	t.Run("token without agent id", func(t *testing.T) {
		c := base()
		c.Token = "abc"
		if err := c.Normalize(); err == nil {
			t.Fatal("token without agent_id must be rejected")
		}
	})
	t.Run("interval too large", func(t *testing.T) {
		c := base()
		c.PollIntervalSeconds = 100000
		if err := c.Normalize(); err == nil {
			t.Fatal("out of range interval must be rejected")
		}
	})
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "127.0.0.1", "127.5.5.5", "::1", "LOCALHOST"} {
		if !IsLoopbackHost(h) {
			t.Fatalf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"", "example.com", "10.0.0.1", "localhost.evil.test"} {
		if IsLoopbackHost(h) {
			t.Fatalf("%q must not be treated as loopback", h)
		}
	}
}

func TestCheckRuntimeRequiresFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{PanelURL: "https://p.test", XrayPath: filepath.Join(dir, "missing.exe"), StateDir: dir}
	if err := cfg.CheckRuntime(); err == nil {
		t.Fatal("missing xray binary must be reported")
	}
	bin := filepath.Join(dir, "xray")
	if err := os.WriteFile(bin, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.XrayPath = bin
	if err := cfg.CheckRuntime(); err != nil {
		t.Fatalf("CheckRuntime: %v", err)
	}
	if !strings.HasSuffix(cfg.XrayPath, "xray") {
		t.Fatal("unexpected path mutation")
	}
}
