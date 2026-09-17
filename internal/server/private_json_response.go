package server

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// writePrivateRevalidatedWorkspaceJSON gives authenticated, immutable response
// snapshots a private validator. The body is still revalidated on every use;
// matching clients avoid retransferring and reparsing a large unchanged JSON
// document. The owner salt prevents validator reuse across login identities.
func writePrivateRevalidatedWorkspaceJSON(
	w http.ResponseWriter,
	r *http.Request,
	ownerID string,
	payload any,
) {
	body, err := json.Marshal(payload)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "internal workspace error"})
		return
	}
	body = append(body, '\n')
	digest := sha256.New()
	_, _ = digest.Write([]byte(strings.TrimSpace(ownerID)))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(body)
	baseETag := `"sha256-` + base64.RawURLEncoding.EncodeToString(digest.Sum(nil))

	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Add("Vary", "Cookie")
	w.Header().Add("Vary", "Accept-Encoding")

	// A matching gzip validator can be answered before recompressing a large
	// unchanged message window. If compression later proves unhelpful, fall
	// back to the identity validator and evaluate that validator separately.
	wantsGzip := len(body) >= 1024 && acceptsHTTPEncoding(r.Header.Get("Accept-Encoding"), "gzip")
	if wantsGzip {
		gzipETag := baseETag[:len(baseETag)-1] + `-gzip"`
		if webLogoETagMatches(r.Header.Get("If-None-Match"), gzipETag) {
			w.Header().Set("ETag", gzipETag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	representation, contentEncoding := compressedJSONRepresentation(wantsGzip, body)
	etag := baseETag
	if contentEncoding != "" {
		etag = baseETag[:len(baseETag)-1] + `-` + contentEncoding + `"`
	}
	w.Header().Set("ETag", etag)
	if webLogoETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if contentEncoding != "" {
		w.Header().Set("Content-Encoding", contentEncoding)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(representation)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(representation)
}

func compressedJSONRepresentation(enabled bool, body []byte) ([]byte, string) {
	if !enabled {
		return body, ""
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return body, ""
	}
	if _, err = writer.Write(body); err == nil {
		err = writer.Close()
	} else {
		_ = writer.Close()
	}
	if err != nil || compressed.Len() >= len(body) {
		return body, ""
	}
	return compressed.Bytes(), "gzip"
}

func acceptsHTTPEncoding(headerValue string, encoding string) bool {
	wildcardQuality := -1.0
	for _, item := range strings.Split(headerValue, ",") {
		parts := strings.Split(item, ";")
		coding := strings.TrimSpace(parts[0])
		if !strings.EqualFold(coding, encoding) && coding != "*" {
			continue
		}
		quality := 1.0
		for _, parameter := range parts[1:] {
			name, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if found && strings.EqualFold(strings.TrimSpace(name), "q") {
				parsed, parseErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
				if parseErr != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if strings.EqualFold(coding, encoding) {
			return quality > 0
		}
		wildcardQuality = quality
	}
	return wildcardQuality > 0
}
