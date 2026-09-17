package server

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) allowLegacySessionSurface(r *http.Request) bool {
	return s != nil && s.synonLinkAuth != nil && !s.synonLinkAuth.enabled &&
		isLoopbackRequest(r) && isTrustedLegacyLoopbackAuthority(r.Host)
}

func isTrustedLegacyLoopbackAuthority(authority string) bool {
	if authority == "" || authority != strings.TrimSpace(authority) || strings.ContainsAny(authority, "@/?#") {
		return false
	}
	for _, value := range authority {
		if value > 0x7f {
			return false
		}
	}

	host := authority
	port := ""
	hasPort := false
	if strings.HasPrefix(authority, "[") {
		closing := strings.IndexByte(authority, ']')
		if closing < 0 {
			return false
		}
		host = authority[1:closing]
		suffix := authority[closing+1:]
		if suffix != "" {
			if !strings.HasPrefix(suffix, ":") {
				return false
			}
			hasPort = true
			port = suffix[1:]
		}
		if host != "::1" {
			return false
		}
	} else {
		if strings.Count(authority, ":") > 1 {
			return false
		}
		if separator := strings.LastIndexByte(authority, ':'); separator >= 0 {
			host, port, hasPort = authority[:separator], authority[separator+1:], true
		}
		if !strings.EqualFold(host, "localhost") && host != "127.0.0.1" {
			return false
		}
	}

	if hasPort {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 {
			return false
		}
	}
	return true
}

func (s *Server) requireLegacySessionSurface(w http.ResponseWriter, r *http.Request) bool {
	if s.allowLegacySessionSurface(r) {
		return true
	}
	writeError(w, http.StatusNotFound, "NOT_FOUND", "Resource not found")
	return false
}
