package api

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ValidateWebRoot fails early when PANEL_WEB_ROOT is not a readable SPA build.
func ValidateWebRoot(root string) error {
	dir, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !dir.IsDir() {
		return fmt.Errorf("web root is not a directory: %s", root)
	}
	f, err := os.Open(filepath.Join(root, "index.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("index.html is not a regular file")
	}
	return nil
}

// newStaticHandler serves a read-only dist directory. Unknown API endpoints and
// missing assets must never become 200 HTML through the SPA fallback.
func newStaticHandler(root string) (http.Handler, error) {
	if err := ValidateWebRoot(root); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.Dir(abs))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		// Prevent serving dotfiles or directory listings from an accidentally broad root.
		for _, part := range strings.Split(name, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		for _, prefix := range []string{"api", "sub", "healthz", "readyz"} {
			if name == prefix || strings.HasPrefix(name, prefix+"/") {
				http.NotFound(w, r)
				return
			}
		}
		// Every /assets request is a file request, including extensionless ones.
		if name == "assets" || strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
			info, err := os.Stat(filepath.Join(abs, filepath.FromSlash(name)))
			if err != nil || !info.Mode().IsRegular() {
				http.NotFound(w, r)
				return
			}
			if name == "index.html" {
				serveSPAIndex(w, r, abs)
			} else {
				files.ServeHTTP(w, r)
			}
			return
		}
		serveSPAIndex(w, r, abs)
	}), nil
}

func serveSPAIndex(w http.ResponseWriter, r *http.Request, root string) {
	f, err := os.Open(filepath.Join(root, "index.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	// A stale index references old hashed assets, so never cache the entry page.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", info.ModTime(), f)
}
