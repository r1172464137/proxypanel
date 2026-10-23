//go:build unix

package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A mispackaged dist may contain symlinks even though HTTP cannot create them.
// Serving one must not expose any file outside the configured build directory.
func TestStaticSymlinksStayWithinRoot(t *testing.T) {
	root := fakeDist(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	const secret = "outside-dist-marker"
	if err := os.WriteFile(outside, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"assets/escape.txt", "favicon-copy.svg"} {
		if err := os.Symlink(outside, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("app.js", filepath.Join(root, "assets", "inside.js")); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Cfg: Config{WebRoot: root}}).Handler()
	for _, name := range []string{"/assets/escape.txt", "/favicon-copy.svg"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", name, nil))
		if r.Code != 404 || strings.Contains(r.Body.String(), secret) {
			t.Fatalf("%s escaped root: %d %q", name, r.Code, r.Body.String())
		}
	}
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/assets/inside.js", nil))
	if r.Code != 200 || !strings.Contains(r.Body.String(), "console.log") {
		t.Fatalf("internal symlink should remain usable: %d %q", r.Code, r.Body.String())
	}
}

func TestValidateWebRootRejectsEscapingIndexSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWebRoot(root); err == nil {
		t.Fatal("escaping index.html symlink was accepted")
	}
}

// A dist directory is normally produced by a build tool, but nothing stops a
// broken pipeline from shipping a FIFO. Opening a FIFO read-only blocks until a
// writer appears, so a single request would pin a goroutine forever. The
// handler must refuse it without blocking.
func TestStaticRejectsNonRegularFileWithoutBlocking(t *testing.T) {
	root := fakeDist(t)
	fifo := filepath.Join(root, "assets", "stream.js")
	if err := syscall.Mkfifo(fifo, 0644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	h := (&Server{Cfg: Config{WebRoot: root}}).Handler()
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", "/assets/stream.js", nil))
		done <- r.Code
	}()
	select {
	case code := <-done:
		if code != 404 {
			t.Fatalf("FIFO in dist served with %d, want 404", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request blocked opening a FIFO; a broken dist can pin a goroutine")
	}
}

// index.html must also be a regular file: a FIFO there would hang every SPA
// route, not only a single asset. Bounded by a timeout so a regression fails
// the test instead of pinning the package until -timeout.
func TestValidateWebRootRejectsFifoIndex(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "index.html"), 0644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- ValidateWebRoot(root) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accepted a FIFO as index.html")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ValidateWebRoot blocked on a FIFO index.html")
	}
}
