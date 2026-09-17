package networktls

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// NormalizeProxyURL validates the one trusted upstream HTTP CONNECT proxy
// accepted by the runtime. Credentials may be embedded for Basic proxy
// authentication, but callers must never log the returned URL.
func NormalizeProxyURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("proxy URL contains control characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" || parsed.Opaque != "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("proxy must be an absolute http:// origin without path, query, or fragment")
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
	}
	numericPort, err := strconv.Atoi(port)
	if err != nil || numericPort < 1 || numericPort > 65535 {
		return "", errors.New("proxy port must be between 1 and 65535")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", errors.New("proxy host is required")
	}
	if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	if parsed.Port() != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), parsed.Port())
	}
	parsed.Scheme = "http"
	parsed.Host = host
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed.String(), nil
}

func parsedProxyURL(value string) (*url.URL, error) {
	normalized, err := NormalizeProxyURL(value)
	if err != nil || normalized == "" {
		return nil, err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, fmt.Errorf("parse normalized proxy URL: %w", err)
	}
	return parsed, nil
}

func configureHTTPProxy(transport *http.Transport, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := parsedProxyURL(value)
	if err != nil {
		return err
	}
	transport.Proxy = http.ProxyURL(parsed)
	return nil
}
