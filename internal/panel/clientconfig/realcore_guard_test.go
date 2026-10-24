//go:build corematrix

package clientconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Optional private evidence retention; configs contain synthetic credentials.
func localGuardDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("SS2022_GUARD_EVIDENCE_DIR")
	if root == "" {
		return t.TempDir()
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("guard evidence root must exist with mode 0700")
	}
	dir, err := os.MkdirTemp(root, "guard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("private_guard_evidence=%s", dir)
	return dir
}

// File-backed output avoids concurrent bytes.Buffer reads. No shell/process-group
// cleanup: only this exact child is killed and reaped, including on test failure.
func localGuardStart(t *testing.T, bin string, args []string, port int, path string) func() string {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		f.Close()
		t.Fatal(err)
	}
	t.Logf("guard_child pid=%d port=%d log=%s", cmd.Process.Pid, port, path)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done; _ = f.Close() })
	snapshot := func() string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			c.Close()
			return snapshot
		}
		select {
		case err := <-done:
			done <- err
			t.Fatalf("guard child exited before readiness (not a rejection): %v", err)
		case <-timer.C:
			t.Fatal("guard readiness timeout (not a rejection)")
		case <-tick.C:
		}
	}
}

// Only fresh, protocol-specific server diagnostics are an authentication oracle.
// A successful oracle cancels the pending HTTP operation; cancellation is not the
// oracle. A timer, EOF, refused connection or child exit alone never passes.
func localGuardAwait(t *testing.T, snapshot func() string, offset int, marker string, finished <-chan error) bool {
	t.Helper()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		fresh := snapshot()[offset:]
		for _, line := range strings.Split(fresh, "\n") {
			if strings.Contains(line, "app/proxyman/inbound: connection ends > ") && strings.Contains(line, marker) && strings.HasSuffix(line, ": invalid request") {
				t.Logf("guard_oracle offset=%d diagnostic=%s", offset, line)
				return true
			}
		}
		select {
		case <-timer.C:
			t.Errorf("guard result=not_proven: no fresh %q server diagnostic (offset=%d); timeout is not rejection", marker, offset)
			return false
		case err := <-finished:
			// Inspect the log once more; transport errors never supply the verdict.
			finished = nil
			t.Logf("guard transport ended (not an oracle): %v", err)
		case <-tick.C:
		}
	}
}

func localGuardRequest(ctx context.Context, socks int, target string) error {
	tr := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: fmt.Sprintf("127.0.0.1:%d", socks)})}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, err = io.Copy(io.Discard, res.Body)
	if err == nil {
		return fmt.Errorf("invalid request unexpectedly reached HTTP origin, status=%d", res.StatusCode)
	}
	return err
}

func localGuardDebugServer(t *testing.T, raw []byte, dir string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// Diagnostics only: no inbound, credentials or routing changes.
	doc["log"] = map[string]any{"loglevel": "debug"}
	return localWrite(t, dir, "server.json", localJSON(t, doc))
}

// Keep paths stable within one private run, not in shared core/lab directories.
func localGuardLogPath(dir, name string) string { return filepath.Join(dir, name+".log") }
