package webfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"net/url"
	"path"
	"strings"
)

// DownloadContinuation points to the existing governed complete-source path.
// It is navigation advice, never authority to download a different URL.
type DownloadContinuation struct {
	Tool      string            `json:"tool"`
	Arguments map[string]string `json:"arguments"`
	ReadWith  string            `json:"read_with"`
}

func htmlDownloadContinuation(rawURL, contentType string) *DownloadContinuation {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil || media != "text/html" && media != "application/xhtml+xml" {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.Port() != "" {
		return nil
	}
	extension := strings.ToLower(path.Ext(parsed.Path))
	if extension != "" && extension != ".html" && extension != ".htm" && extension != ".xhtml" {
		return nil
	}
	if extension == "" {
		extension = ".html"
		if media == "application/xhtml+xml" {
			extension = ".xhtml"
		}
	}
	digest := sha256.Sum256([]byte(rawURL))
	return &DownloadContinuation{
		Tool: "download_public_scientific_file",
		Arguments: map[string]string{
			"url": rawURL, "filename": "web-source-" + hex.EncodeToString(digest[:8]) + extension,
			"human_description": "Preserving the complete source document",
		},
		ReadWith: "read_file(version_id=<downloaded version_id>, byte_offset=0, byte_limit=<page size>); follow next_byte_offset",
	}
}
