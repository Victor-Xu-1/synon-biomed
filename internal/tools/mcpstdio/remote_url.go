package mcpstdio

import (
	"errors"
	"net/url"
	"strings"
)

const (
	maxRuntimeQueryParamNameBytes  = 128
	maxRuntimeQueryParamValueBytes = 16 * 1024
)

// remoteMCPRequestURL applies runtime-only query parameters to a trusted
// connector URL. Query parameters are intentionally kept out of ServerConfig
// JSON and are populated only by the bundled owner-credential boundary.
func remoteMCPRequestURL(rawURL string, queryParams map[string]string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("remote MCP URL is invalid")
	}
	if len(queryParams) == 0 {
		return parsed.String(), nil
	}
	query := parsed.Query()
	for key, value := range queryParams {
		key = strings.TrimSpace(key)
		if key == "" || len(key) > maxRuntimeQueryParamNameBytes || strings.ContainsAny(key, "\r\n\x00") {
			return "", errors.New("remote MCP query parameter name is invalid")
		}
		if value == "" || len(value) > maxRuntimeQueryParamValueBytes || strings.ContainsAny(value, "\r\n\x00") {
			return "", errors.New("remote MCP query parameter value is invalid")
		}
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
