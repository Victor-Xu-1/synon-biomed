package server

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"synon-go/internal/agentruntime"
	"synon-go/internal/tools/webfetch"
)

type agentPublicScientificSourceBinding struct {
	ToolCallID    string
	RedirectHosts []string
}

// Only a completed transport receipt can connect a stable request URL with
// rotating signed destinations. URLs inside the response body are not hops.
func agentPublicScientificWebFetchRedirectBinding(value any) (string, []string, bool) {
	object, ok := agentPublicScientificTransportEnvelope(value)
	if !ok {
		return "", nil, false
	}
	raw, err := json.Marshal(map[string]any{
		"requestedUrl": object["requestedUrl"], "url": object["url"],
		"statusCode": object["statusCode"], "redirects": object["redirects"],
	})
	if err != nil {
		return "", nil, false
	}
	var receipt webfetch.Result
	if json.Unmarshal(raw, &receipt) != nil || receipt.StatusCode < 200 || receipt.StatusCode >= 300 ||
		receipt.RequestedURL == "" || len(receipt.Redirects) == 0 {
		return "", nil, false
	}
	validURL := func(raw string) (string, bool) {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" ||
			(parsed.Port() != "" && parsed.Port() != "443") {
			return "", false
		}
		host, err := agentPublicScientificPublicHostname(parsed)
		return host, err == nil
	}
	if _, valid := validURL(receipt.RequestedURL); !valid {
		return "", nil, false
	}
	current := receipt.RequestedURL
	hosts := make([]string, 0, len(receipt.Redirects))
	seen := map[string]bool{current: true}
	for _, hop := range receipt.Redirects {
		if hop.From != current || seen[hop.To] {
			return "", nil, false
		}
		switch hop.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		default:
			return "", nil, false
		}
		host, valid := validURL(hop.To)
		if !valid {
			return "", nil, false
		}
		hosts = append(hosts, host)
		seen[hop.To] = true
		current = hop.To
	}
	if current != receipt.URL {
		return "", nil, false
	}
	return receipt.RequestedURL, hosts, true
}

// Unwrap only the documented result envelope, never arbitrary nested payloads.
// A nested partial result cannot erase a failure on an enclosing envelope.
func agentPublicScientificTransportEnvelope(value any) (map[string]any, bool) {
	for depth := 0; depth < 4; depth++ {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		// Partial tool results may contain useful data alongside failures in
		// general. Transport authority is narrower: partial bytes cannot hide
		// an explicit failure or unavailable marker on this envelope.
		status := make(map[string]any, 10)
		for _, key := range []string{"ok", "success", "isError", "sourceUnavailable", "error", "failure", "status", "stopReason", "stop_reason"} {
			status[key] = object[key]
		}
		outcome := agentruntime.ClassifyToolResult(status)
		if outcome == agentruntime.ToolResultFailed || outcome == agentruntime.ToolResultUnavailable {
			return nil, false
		}
		if _, present := object["url"]; present {
			return object, true
		}
		value = object["result"]
	}
	return nil, false
}

// Use the same literal/local-host boundary for request URLs and every observed
// redirect. securefetch still resolves and validates addresses on every dial.
func agentPublicScientificPublicHostname(parsed *url.URL) (string, error) {
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	_, literalErr := netip.ParseAddr(host)
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || literalErr == nil {
		return "", errAgentPublicScientificFileSource
	}
	for _, character := range host {
		if character > 0x7f {
			return "", errAgentPublicScientificFileSource
		}
	}
	return host, nil
}
