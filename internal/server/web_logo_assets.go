package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"synon-go/internal/logoassets"
)

const webLogoAssetPrefix = "/api/assets/logos/"

func (s *Server) handleWebLogoAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"ok": false, "error": "GET or HEAD is required",
		})
		return
	}
	name := strings.TrimPrefix(r.URL.Path, webLogoAssetPrefix)
	content, err := logoassets.Read(name)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, logoassets.ErrInvalidName):
			status = http.StatusBadRequest
		case errors.Is(err, fs.ErrNotExist):
			status = http.StatusNotFound
		}
		writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": "logo asset is unavailable"})
		return
	}

	contentType := "image/svg+xml"
	if strings.HasSuffix(strings.ToLower(name), ".png") {
		contentType = "image/png"
	}
	digest := sha256.Sum256(content)
	etag := `"sha256-` + hex.EncodeToString(digest[:]) + `"`
	setWebLogoAssetHeaders(w.Header(), contentType, etag, len(content))
	if webLogoETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func setWebLogoAssetHeaders(header http.Header, contentType string, etag string, length int) {
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.Itoa(length))
	header.Set("Cache-Control", "public, max-age=86400, immutable")
	header.Set("ETag", etag)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	if contentType == "image/svg+xml" {
		header.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	}
}

func webLogoETagMatches(value string, current string) bool {
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.HasPrefix(candidate, "W/") {
			candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "W/"))
		}
		if candidate == current {
			return true
		}
	}
	return false
}
