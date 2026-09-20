package server

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html/charset"
	"synon-go/internal/tools/securefetch"
)

func agentPublicScientificHTMLFilename(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".html", ".htm", ".xhtml":
		return true
	default:
		return false
	}
}

// HTML remains untrusted source data. This check preserves its media identity;
// it neither executes markup nor follows embedded resources. Browser delivery
// and read_file separately enforce the passive-document boundary.
func verifyAgentPublicScientificHTML(file *os.File, reported string, accepted []string) (string, error) {
	fail := errors.New(string(securefetch.CodeContentType))
	media, parameters, err := mime.ParseMediaType(strings.TrimSpace(reported))
	if err != nil {
		return "", fail
	}
	allowed := false
	for _, candidate := range accepted {
		allowed = allowed || media == candidate
	}
	if !allowed {
		return "", fail
	}
	var prefix [512]byte
	n, err := file.ReadAt(prefix[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	encoding, _, _ := charset.DetermineEncoding(prefix[:n], reported)
	if label, declared := parameters["charset"]; declared {
		encoding, _ = charset.Lookup(label)
		if encoding == nil {
			return "", fail
		}
	}
	decoded, err := encoding.NewDecoder().Bytes(prefix[:n])
	if err != nil {
		return "", fail
	}
	decoded = bytes.TrimSpace(bytes.TrimPrefix(decoded, []byte{0xef, 0xbb, 0xbf}))
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(decoded))
	if detected == "text/html" {
		return mime.FormatMediaType(media, parameters), nil
	}
	if media == "application/xhtml+xml" {
		// XML declarations are common for XHTML. Only the XHTML root namespace
		// grants this format, not arbitrary XML relabeled by the remote server.
		decoder := xml.NewDecoder(encoding.NewDecoder().Reader(io.NewSectionReader(file, 0, 64<<10)))
		// The transport declaration/BOM already selected and applied the
		// decoder. Do not decode those UTF-8 bytes again when XML repeats it.
		decoder.CharsetReader = func(_ string, source io.Reader) (io.Reader, error) { return source, nil }
		for {
			token, err := decoder.Token()
			if err != nil {
				return "", fail
			}
			if start, ok := token.(xml.StartElement); ok {
				if start.Name.Local == "html" && start.Name.Space == "http://www.w3.org/1999/xhtml" {
					return mime.FormatMediaType(media, parameters), nil
				}
				return "", fail
			}
		}
	}
	return "", fail
}
