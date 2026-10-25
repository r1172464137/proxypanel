// Package config loads and validates the node agent configuration file.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AgentVersion is the version reported to the panel and in heartbeats. It is
// bumped only when the shipped agent really changes; 0.1.2 carries the bundled
// official core, the one-line installer and the day-two manager script.
const AgentVersion = "0.1.2"

// Default intervals decided in the implementation contract (5s poll, 5s stats,
// 15s heartbeat).
const (
	DefaultPollInterval      = 5
	DefaultStatsInterval     = 5
	DefaultHeartbeatInterval = 15

	minInterval = 1
	maxInterval = 3600
)

// Config is the on-disk JSON configuration of the agent. It never contains
// executable paths or secrets other than the one-time registration code and
// the agent bearer token (which is normally persisted inside the state
// directory instead).
type Config struct {
	PanelURL                 string `json:"panel_url"`
	RegistrationCode         string `json:"registration_code"`
	Token                    string `json:"token"`
	AgentID                  string `json:"agent_id"`
	XrayPath                 string `json:"xray_path"`
	StateDir                 string `json:"state_dir"`
	PollIntervalSeconds      int    `json:"poll_interval_seconds"`
	StatsIntervalSeconds     int    `json:"stats_interval_seconds"`
	HeartbeatIntervalSeconds int    `json:"heartbeat_interval_seconds"`
	AllowLoopbackHTTP        bool   `json:"allow_loopback_http"`
}

// Load reads, decodes (strictly: unknown fields are rejected) and validates the
// configuration file.
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("config path is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// EnvPanelURL is the environment variable holding the panel base URL when no
// config file is used.
const EnvPanelURL = "PANEL_URL"

// EnvRegistrationCode holds the one-time registration code.
const EnvRegistrationCode = "REGISTRATION_CODE"

// FromEnv builds a configuration from environment variables only. It exists for
// container deployments where writing a JSON file into the image is awkward:
// the same validated Config is produced, so validation and safety rules do not
// differ between file and environment configuration.
func FromEnv() (*Config, error) {
	c := &Config{
		PanelURL:         firstEnv(EnvPanelURL, "PROXYPANEL_"+EnvPanelURL),
		RegistrationCode: firstEnv(EnvRegistrationCode, "PROXYPANEL_"+EnvRegistrationCode),
		Token:            firstEnv("AGENT_TOKEN", "PROXYPANEL_AGENT_TOKEN"),
		AgentID:          firstEnv("AGENT_ID", "PROXYPANEL_AGENT_ID"),
		XrayPath:         firstEnv("XRAY_PATH", "PROXYPANEL_XRAY_PATH"),
		StateDir:         firstEnv("STATE_DIR", "PROXYPANEL_STATE_DIR"),
	}
	if c.XrayPath == "" {
		c.XrayPath = "/opt/xray/xray"
	}
	if c.StateDir == "" {
		c.StateDir = "/var/lib/proxypanel-agent"
	}
	if v := firstEnv("POLL_INTERVAL_SECONDS", "PROXYPANEL_POLL_INTERVAL_SECONDS"); v != "" {
		c.PollIntervalSeconds, _ = strconv.Atoi(v)
	}
	if v := firstEnv("STATS_INTERVAL_SECONDS", "PROXYPANEL_STATS_INTERVAL_SECONDS"); v != "" {
		c.StatsIntervalSeconds, _ = strconv.Atoi(v)
	}
	if v := firstEnv("HEARTBEAT_INTERVAL_SECONDS", "PROXYPANEL_HEARTBEAT_INTERVAL_SECONDS"); v != "" {
		c.HeartbeatIntervalSeconds, _ = strconv.Atoi(v)
	}
	switch strings.ToLower(firstEnv("ALLOW_LOOPBACK_HTTP", "PROXYPANEL_ALLOW_LOOPBACK_HTTP")) {
	case "1", "true", "yes":
		c.AllowLoopbackHTTP = true
	}
	if err := c.Normalize(); err != nil {
		return nil, fmt.Errorf("environment configuration: %w", err)
	}
	return c, nil
}

// firstEnv returns the first non-empty trimmed value of the given names.
func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// FromEnvAvailable reports whether the environment supplies enough identity to
// start without a config file. Presence only: validation still runs in FromEnv.
func FromEnvAvailable() bool {
	return firstEnv(EnvPanelURL, "PROXYPANEL_"+EnvPanelURL) != ""
}

// Normalize applies defaults, resolves the state directory to an absolute path
// and validates the whole document.
func (c *Config) Normalize() error {
	c.PanelURL = strings.TrimRight(strings.TrimSpace(c.PanelURL), "/")
	c.XrayPath = strings.TrimSpace(c.XrayPath)
	c.RegistrationCode = strings.TrimSpace(c.RegistrationCode)
	c.Token = strings.TrimSpace(c.Token)
	c.AgentID = strings.TrimSpace(c.AgentID)

	if c.PollIntervalSeconds == 0 {
		c.PollIntervalSeconds = DefaultPollInterval
	}
	if c.StatsIntervalSeconds == 0 {
		c.StatsIntervalSeconds = DefaultStatsInterval
	}
	if c.HeartbeatIntervalSeconds == 0 {
		c.HeartbeatIntervalSeconds = DefaultHeartbeatInterval
	}
	if strings.TrimSpace(c.StateDir) != "" {
		abs, err := filepath.Abs(c.StateDir)
		if err != nil {
			return fmt.Errorf("resolve state_dir: %w", err)
		}
		c.StateDir = abs
	}
	return c.Validate()
}

// Validate checks the structural rules of the configuration. It does not touch
// the filesystem, so callers can use it without a real Xray binary; the runtime
// checks live in CheckRuntime.
func (c *Config) Validate() error {
	if c.PanelURL == "" {
		return fmt.Errorf("panel_url is required")
	}
	u, err := url.Parse(c.PanelURL)
	if err != nil {
		return fmt.Errorf("panel_url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("panel_url: scheme must be https or http (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("panel_url: host is required")
	}
	if u.User != nil {
		return fmt.Errorf("panel_url: credentials in the URL are not allowed")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("panel_url: query and fragment are not allowed")
	}
	if u.Scheme == "http" {
		if !c.AllowLoopbackHTTP {
			return fmt.Errorf("panel_url: plain http is only allowed for loopback testing and requires allow_loopback_http=true")
		}
		if !IsLoopbackHost(u.Hostname()) {
			return fmt.Errorf("panel_url: plain http is restricted to localhost, 127.0.0.1 or ::1 (got %q)", u.Hostname())
		}
	}
	if c.XrayPath == "" {
		return fmt.Errorf("xray_path is required")
	}
	if !filepath.IsAbs(c.XrayPath) {
		return fmt.Errorf("xray_path must be absolute (got %q)", c.XrayPath)
	}
	if c.StateDir == "" {
		return fmt.Errorf("state_dir is required")
	}
	if !filepath.IsAbs(c.StateDir) {
		return fmt.Errorf("state_dir must be absolute (got %q)", c.StateDir)
	}
	if c.Token != "" && c.AgentID == "" {
		return fmt.Errorf("agent_id is required when token is set")
	}
	if c.AgentID != "" && c.Token == "" {
		return fmt.Errorf("token is required when agent_id is set")
	}
	for name, v := range map[string]int{
		"poll_interval_seconds":      c.PollIntervalSeconds,
		"stats_interval_seconds":     c.StatsIntervalSeconds,
		"heartbeat_interval_seconds": c.HeartbeatIntervalSeconds,
	} {
		if v < minInterval || v > maxInterval {
			return fmt.Errorf("%s must be between %d and %d (got %d)", name, minInterval, maxInterval, v)
		}
	}
	return nil
}

// CheckRuntime verifies filesystem preconditions: the Xray binary exists and is
// a regular file.
func (c *Config) CheckRuntime() error {
	st, err := os.Stat(c.XrayPath)
	if err != nil {
		return fmt.Errorf("xray_path %s: %w", c.XrayPath, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("xray_path %s is not a regular file", c.XrayPath)
	}
	return nil
}

// PollInterval is the snapshot polling / report upload cadence.
func (c *Config) PollInterval() time.Duration {
	return time.Duration(c.PollIntervalSeconds) * time.Second
}

// StatsInterval is the usage sampling cadence.
func (c *Config) StatsInterval() time.Duration {
	return time.Duration(c.StatsIntervalSeconds) * time.Second
}

// HeartbeatInterval is the heartbeat cadence.
func (c *Config) HeartbeatInterval() time.Duration {
	return time.Duration(c.HeartbeatIntervalSeconds) * time.Second
}

// IsLoopbackHost reports whether host is localhost, 127.0.0.0/8 or ::1.
func IsLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
