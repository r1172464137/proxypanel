package e2e

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test server serves only the built web bundle; API requests still go to
// the real panel handler. No API response interception or fixtures in JS.
func browserHandler(t *testing.T, api http.Handler) http.Handler {
	t.Helper()
	if os.Getenv("PANEL_BROWSER_TEST") != "1" {
		return api
	}
	dist, err := filepath.Abs("../../web/dist")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("build web/dist before browser integration")
	}
	assets := http.FileServer(http.Dir(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/sub/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			api.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			assets.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	})
}

func runBrowser(t *testing.T, base, password string) {
	t.Helper()
	if os.Getenv("PANEL_BROWSER_TEST") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "../../web/tests/platform-browser.mjs")
	command.Env = append(os.Environ(), "PANEL_BROWSER_URL="+base, "PANEL_BROWSER_PASSWORD="+password)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real browser verification failed: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
