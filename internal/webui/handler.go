package webui

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var hashedAssetName = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)

const (
	themeInitPath                    = "theme-init.js"
	rdkitWorkerPath                  = "rdkit/rdkit-worker.js"
	pageContentSecurityPolicy        = "default-src 'self'; base-uri 'self'; connect-src 'self' ws: wss:; font-src 'self' data:; form-action 'self'; frame-ancestors 'none'; frame-src 'self' http://mcp-app.localhost:* https://mcp-app.localhost:*; img-src 'self' data: blob:; object-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; worker-src 'self' blob:;"
	rdkitWorkerContentSecurityPolicy = "default-src 'none'; script-src 'self' 'unsafe-eval' 'wasm-unsafe-eval'; connect-src 'self';"
)

type Handler struct {
	root      string
	indexPath string
}

func New(root string) (*Handler, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("web root is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve web root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve web root symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat web root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("web root is not a directory: %s", resolved)
	}
	if err := rejectSymlinks(resolved); err != nil {
		return nil, err
	}
	indexPath := filepath.Join(resolved, "index.html")
	indexInfo, err := os.Stat(indexPath)
	if err != nil {
		return nil, fmt.Errorf("stat web index: %w", err)
	}
	if !indexInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("web index is not a regular file: %s", indexPath)
	}
	return &Handler{root: resolved, indexPath: indexPath}, nil
}

func rejectSymlinks(root string) error {
	return filepath.WalkDir(root, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect web asset %s: %w", entryPath, walkErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("web assets must not contain symlinks: %s", entryPath)
		}
		return nil
	})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	relative, ok := cleanRequestPath(r.URL.Path)
	if !ok {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	if reservedPath(relative) {
		http.NotFound(w, r)
		return
	}
	if relative == "" {
		h.serveFile(w, r, h.indexPath, "index.html", true)
		return
	}
	candidate, exists := h.resolveFile(relative)
	if exists {
		if relative == rdkitWorkerPath {
			setRDKitWorkerSecurityHeaders(w.Header())
		}
		h.serveFile(w, r, candidate, relative, false)
		return
	}
	if filepath.Ext(relative) == "" && acceptsHTML(r) {
		h.serveFile(w, r, h.indexPath, "index.html", true)
		return
	}
	http.NotFound(w, r)
}

func cleanRequestPath(requestPath string) (string, bool) {
	if strings.ContainsAny(requestPath, "\\\x00") {
		return "", false
	}
	for _, segment := range strings.Split(requestPath, "/") {
		if segment == "." || segment == ".." || strings.HasPrefix(segment, ".") {
			return "", false
		}
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if cleaned == "." {
		return "", true
	}
	return cleaned, true
}

func reservedPath(relative string) bool {
	for _, prefix := range []string{"api", "health", "synon-link", "v1", "ws"} {
		if relative == prefix || strings.HasPrefix(relative, prefix+"/") {
			return true
		}
	}
	return false
}

func acceptsHTML(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	return accept == "" || strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
}

func (h *Handler) resolveFile(relative string) (string, bool) {
	candidate := filepath.Join(h.root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || !withinRoot(h.root, resolved) {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

func withinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, filename, requestName string, index bool) {
	file, err := os.Open(filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if index {
		w.Header().Set("Cache-Control", "no-store")
	} else if requestName == themeInitPath {
		// The fixed-name bootstrap must be revalidated after an application upgrade.
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	} else if strings.HasPrefix(requestName, "rdkit/") {
		// The worker protocol, loader, and WASM must advance as one release.
		// Revalidation prevents a new client from running against a stale fixed-name asset.
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	} else if hashedAssetName.MatchString(path.Base(requestName)) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeContent(w, r, path.Base(requestName), info.ModTime(), file)
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", pageContentSecurityPolicy)
	header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=(), payment=(), usb=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

func setRDKitWorkerSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", rdkitWorkerContentSecurityPolicy)
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
}
