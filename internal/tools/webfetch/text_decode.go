package webfetch

import (
	"encoding/base64"

	"synon-go/internal/httptext"
)

func assignWebResponseText(result *Result, body []byte, incomplete bool) {
	if result.Binary {
		return
	}
	text, err := httptext.Decode(body, result.ContentType, incomplete)
	if err == nil {
		result.Body = text
		return
	}
	// Preserve source bytes for recovery. Do not report an ASCII prefix as a
	// complete response or replace an earlier transport failure with this one.
	result.RawBodyBase64 = base64.StdEncoding.EncodeToString(body)
	result.SourceUnavailable = true
	result.Partial = true
	result.Recovery = "decode_original_response_or_use_source_specific_tool"
	if result.Error == "" {
		result.Error = "Public source character encoding could not be decoded."
	}
}
