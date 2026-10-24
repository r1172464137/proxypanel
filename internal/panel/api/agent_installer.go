package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// AgentInstallerArtifact is one immutable Agent binary served by the panel.
// Binaries are public release artifacts, not credentials; registration still
// requires the short-lived one-time code generated for a specific server.
type AgentInstallerArtifact struct {
	Path   string
	SHA256 string
	Size   int64
}

// AgentInstallerConfig binds the generated installer to this exact panel URL
// and to binaries shipped with the same release. Nil disables one-click install.
type AgentInstallerConfig struct {
	Script      []byte
	PanelURL    string
	XrayVersion string
	Artifacts   map[string]AgentInstallerArtifact
}

// LoadAgentInstaller validates installer inputs once during process startup.
// Missing artifacts are omitted, but every configured artifact must be a
// regular file and is fingerprinted before it can be advertised.
func LoadAgentInstaller(scriptPath, panelURL, xrayVersion string, artifactPaths map[string]string) (*AgentInstallerConfig, error) {
	scriptPath = strings.TrimSpace(scriptPath)
	panelURL = strings.TrimRight(strings.TrimSpace(panelURL), "/")
	if scriptPath == "" {
		return nil, nil
	}
	parsed, err := url.Parse(panelURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, fmt.Errorf("agent installer requires an absolute HTTPS PANEL_PUBLIC_URL")
	}
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("read agent installer: %w", err)
	}
	if len(script) == 0 || len(script) > 512<<10 {
		return nil, fmt.Errorf("agent installer script size is invalid")
	}
	cfg := &AgentInstallerConfig{Script: script, PanelURL: panelURL, XrayVersion: xrayVersion, Artifacts: map[string]AgentInstallerArtifact{}}
	for arch, path := range artifactPaths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("agent artifact %s: %w", arch, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("agent artifact %s is not a regular file", arch)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open agent artifact %s: %w", arch, err)
		}
		h := sha256.New()
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil {
			return nil, fmt.Errorf("hash agent artifact %s", arch)
		}
		cfg.Artifacts[arch] = AgentInstallerArtifact{Path: path, SHA256: hex.EncodeToString(h.Sum(nil)), Size: info.Size()}
	}
	if len(cfg.Artifacts) == 0 {
		return nil, fmt.Errorf("agent installer has no configured binaries")
	}
	return cfg, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (s *Server) agentInstallCommand(code string) (string, bool, string) {
	if s.Cfg.AgentInstaller == nil {
		return "", false, "面板未配置可校验的 Agent 安装包"
	}
	url := s.Cfg.AgentInstaller.PanelURL + "/install-agent.sh"
	command := "curl --proto '=https' --tlsv1.2 -fsSL " + shellQuote(url) +
		" -o /tmp/proxypanel-agent-install.sh && sudo bash /tmp/proxypanel-agent-install.sh -key " + shellQuote(code)
	return command, true, ""
}

func (s *Server) handleAgentInstallerScript(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.AgentInstaller
	if cfg == nil {
		http.NotFound(w, r)
		return
	}
	amd64, amdOK := cfg.Artifacts["amd64"]
	arm64, armOK := cfg.Artifacts["arm64"]
	if !amdOK && !armOK {
		http.NotFound(w, r)
		return
	}
	prefix := "#!/usr/bin/env bash\n" +
		"export PROXYPANEL_INSTALL_PANEL_URL=" + shellQuote(cfg.PanelURL) + "\n" +
		"export PROXYPANEL_INSTALL_XRAY_VERSION=" + shellQuote(cfg.XrayVersion) + "\n" +
		"export PROXYPANEL_INSTALL_ASSUME_YES=1\n"
	if amdOK {
		prefix += "export PROXYPANEL_INSTALL_AGENT_URL_AMD64=" + shellQuote(cfg.PanelURL+"/install-agent/linux-amd64") + "\n" +
			"export PROXYPANEL_INSTALL_AGENT_SHA256_AMD64=" + shellQuote(amd64.SHA256) + "\n"
	}
	if armOK {
		prefix += "export PROXYPANEL_INSTALL_AGENT_URL_ARM64=" + shellQuote(cfg.PanelURL+"/install-agent/linux-arm64") + "\n" +
			"export PROXYPANEL_INSTALL_AGENT_SHA256_ARM64=" + shellQuote(arm64.SHA256) + "\n"
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="proxypanel-agent-install.sh"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, prefix)
	// The original shebang becomes a harmless comment because bash executes the
	// downloaded file; keeping the source byte-for-byte avoids a second installer.
	_, _ = w.Write(cfg.Script)
}

func (s *Server) handleAgentInstallerBinary(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.AgentInstaller
	if cfg == nil {
		http.NotFound(w, r)
		return
	}
	arch := strings.TrimPrefix(r.PathValue("arch"), "linux-")
	artifact, ok := cfg.Artifacts[arch]
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(artifact.Path)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.Size {
		s.writeError(w, r, fmt.Errorf("agent artifact changed after startup"))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="proxypanel-agent-linux-%s"`, arch))
	// The URL is stable across panel upgrades; never let an intermediary cache an
	// older Agent under the new release's advertised SHA256.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(artifact.Path), info.ModTime(), f)
}
