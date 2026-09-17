package server

import (
	"net"
	"net/http"
	"strings"
)

type webSessionMetadata struct {
	UserAgent     string
	UserAgentHash string
	DeviceIDHash  string
	IPAddress     string
	NetworkClass  string
}

func webSessionMetadataFromRequest(r *http.Request) webSessionMetadata {
	userAgent := strings.TrimSpace(r.UserAgent())
	label := webUserAgentLabel(userAgent)
	if len(label) > webSessionUserAgentLabelLimit {
		label = label[:webSessionUserAgentLabelLimit]
	}
	return webSessionMetadata{
		UserAgent: label, UserAgentHash: webSecretHash(userAgent),
		IPAddress: webRequestIPAddress(r), NetworkClass: webRequestNetworkClass(r),
	}
}

func webRequestIPAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]")
	}
	if address := net.ParseIP(host); address != nil {
		return address.String()
	}
	return ""
}

func (s *webSessionStore) metadataForSessionCreation(r *http.Request) (webSessionMetadata, string, error) {
	metadata := webSessionMetadataFromRequest(r)
	if cookie, err := r.Cookie(webDeviceCookieName); err == nil && validWebSecret(cookie.Value) {
		metadata.DeviceIDHash = webSecretHash(cookie.Value)
		return metadata, cookie.Value, nil
	}
	deviceToken, err := s.randomSecret()
	if err != nil {
		return webSessionMetadata{}, "", err
	}
	metadata.DeviceIDHash = webSecretHash(deviceToken)
	return metadata, deviceToken, nil
}

func webUserAgentLabel(userAgent string) string {
	browser := "Browser"
	switch {
	case strings.Contains(userAgent, "Edg/"):
		browser = "Microsoft Edge"
	case strings.Contains(userAgent, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(userAgent, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(userAgent, "Safari/"):
		browser = "Safari"
	case strings.Contains(userAgent, "curl/"):
		browser = "curl"
	}
	platform := ""
	switch {
	case strings.Contains(userAgent, "Windows"):
		platform = "Windows"
	case strings.Contains(userAgent, "Mac OS"):
		platform = "macOS"
	case strings.Contains(userAgent, "Linux"):
		platform = "Linux"
	}
	if platform == "" {
		return browser
	}
	return browser + " · " + platform
}

func webRequestNetworkClass(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]")
	}
	address := net.ParseIP(host)
	switch {
	case address == nil:
		return "unknown"
	case address.IsLoopback():
		return "loopback"
	case address.IsPrivate():
		return "private"
	default:
		return "remote"
	}
}
