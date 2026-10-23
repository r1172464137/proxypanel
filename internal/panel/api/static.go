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
	f, _, err := openStaticFile(root, "index.html")
	if err != nil {
		return err
	}
	return f.Close()
}

// openStaticFile opens name under root and returns only regular files.
//
// The boundary is enforced by os.Root while opening, so a symlink (absolute or
// relative, at any path component) that resolves outside root is rejected. A
// Stat followed by a separate open would allow that escape and leaves a
// check/use window.
//
// The open is non-blocking: a FIFO that a mispackaged dist happens to contain
// would otherwise block the request goroutine forever in open(2). Because the
// file returned here must be a regular file, nothing ever reads from a FIFO.
func openStaticFile(root, name string) (*os.File, os.FileInfo, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	f, err := r.OpenFile(name, os.O_RDONLY|openNonBlocking, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", name)
	}
	return f, info, nil
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
			f, info, err := openStaticFile(abs, filepath.FromSlash(name))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			if name == "index.html" {
				w.Header().Set("Cache-Control", "no-store")
			}
			http.ServeContent(w, r, path.Base(name), info.ModTime(), f)
			return
		}
		serveSPAIndex(w, r, abs)
	}), nil
}

func serveSPAIndex(w http.ResponseWriter, r *http.Request, root string) {
	f, info, err := openStaticFile(root, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	// A stale index references old hashed assets, so never cache the entry page.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", info.ModTime(), f)
}
