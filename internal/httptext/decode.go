// Package httptext decodes public HTTP text at the transport boundary, before
// tool-specific parsing or source delivery can lose the original characters.
package httptext

import (
	"bytes"
	"errors"
	"mime"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
)

// Decode applies HTML charset prescan or an explicit HTTP charset. Undeclared
// non-HTML text is UTF-8. incomplete permits only an unfinished final UTF-8
// code point, never invalid interior bytes or an invalid complete response.
func Decode(body []byte, contentType string, incomplete bool) (string, error) {
	mediaType, parameters, _ := mime.ParseMediaType(contentType)
	label := strings.TrimSpace(parameters["charset"])
	var sourceEncoding encoding.Encoding
	var name string
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		// Use the standard HTML prescan rather than a second markup parser or
		// content-specific matching. This also gives BOMs/header declarations
		// their specified precedence over meta declarations.
		sourceEncoding, name, _ = charset.DetermineEncoding(body, contentType)
	} else {
		if label == "" {
			label = "utf-8"
		}
		var err error
		sourceEncoding, err = htmlindex.Get(label)
		if err != nil {
			return "", err
		}
		name, err = htmlindex.Name(sourceEncoding)
		if err != nil {
			return "", err
		}
	}
	if name == "utf-8" {
		body = bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
		if utf8.Valid(body) {
			return string(body), nil
		}
		// Only an incomplete final code point may be omitted from an explicitly
		// partial read. Invalid interior bytes must keep their recovery payload.
		if incomplete {
			for start := max(0, len(body)-(utf8.UTFMax-1)); start < len(body); start++ {
				if !utf8.FullRune(body[start:]) && utf8.Valid(body[:start]) {
					return string(body[:start]), nil
				}
			}
		}
		return "", errors.New("invalid UTF-8 source")
	}
	decoded, err := sourceEncoding.NewDecoder().Bytes(body)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}
