package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeDist(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"index.html": "<!doctype html><div id=app></div>", "assets/app.js": "console.log('app')", "assets/app.css": "body{}", "favicon.svg": "<svg/>", ".secret": "hidden"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// No database needed: these tests exercise the real router and SPA handler.
func TestStaticRoutes(t *testing.T) {
	root := fakeDist(t)
	router := (&Server{Cfg: Config{WebRoot: root}}).Handler()
	for _, tc := range []struct {
		method, path string
		status       int
		contains     string
		noStore      bool
	}{
		{"GET", "/", 200, "id=app", true},
		{"GET", "/admin/nodes", 200, "id=app", true},
		{"GET", "/me/subscription/deep?x=1", 200, "id=app", true},
		{"GET", "/index.html", 200, "id=app", true},
		{"GET", "/assets/app.js", 200, "console.log", false},
		{"GET", "/favicon.svg", 200, "<svg/>", false},
		{"HEAD", "/admin/nodes", 200, "", true},
		{"HEAD", "/assets/app.js", 200, "", false},
		{"GET", "/assets/missing.js", 404, "", false},
		{"GET", "/assets/missing", 404, "", false},
		{"GET", "/assets/", 404, "", false},
		{"GET", "/.secret", 404, "", false},
		{"GET", "/api", 404, "", false},
		{"GET", "/api/v1/unknown", 404, "", false},
		{"GET", "/sub", 404, "", false},
		{"GET", "/healthz/unknown", 404, "", false},
		{"GET", "/healthz", 200, "status", false},
		{"POST", "/admin/nodes", 405, "", false},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			if rr.Code != tc.status {
				t.Fatalf("status=%d want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.contains) {
				t.Fatalf("missing %q: %s", tc.contains, rr.Body.String())
			}
			if tc.status != 200 && strings.Contains(rr.Body.String(), "id=app") {
				t.Fatal("SPA swallowed non-SPA request")
			}
			if tc.method == "HEAD" && rr.Body.Len() != 0 {
				t.Fatal("HEAD sent a body")
			}
			if tc.noStore && rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("index must not be cached")
			}
		})
	}
}

func TestStaticAPIOnly(t *testing.T) {
	router := (&Server{}).Handler()
	for _, p := range []string{"/", "/admin/nodes", "/assets/app.js"} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		if rr.Code != 404 {
			t.Fatalf("%s=%d, want 404 without WebRoot", p, rr.Code)
		}
	}
}

func TestValidateWebRoot(t *testing.T) {
	root := fakeDist(t)
	if err := ValidateWebRoot(root); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(root, "absent"), filepath.Join(root, "index.html"), t.TempDir()} {
		if err := ValidateWebRoot(p); err == nil {
			t.Fatalf("accepted unusable web root %s", p)
		}
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "index.html"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWebRoot(dir); err == nil {
		t.Fatal("accepted a directory as index.html")
	}
}
