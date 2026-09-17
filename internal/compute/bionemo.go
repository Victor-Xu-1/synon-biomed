package compute

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

const DefaultBioNeMoHostedHost = "health.api.nvidia.com"

func NormalizeBioNeMoHostedHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultBioNeMoHostedHost
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("hostedHost must be a host or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("hostedHost must not contain credentials, path, query, or fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", errors.New("hostedHost must contain a valid host")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return "", errors.New("hostedHost must not be loopback or local")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
			return "", errors.New("hostedHost must not be loopback, private, link-local, or reserved")
		}
	}
	if port := parsed.Port(); port != "" {
		return net.JoinHostPort(host, port), nil
	}
	return host, nil
}
