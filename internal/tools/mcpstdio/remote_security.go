package mcpstdio

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type remoteSecurityFixtureClientKey struct{}

// PublicHTTPSPinnedTransport is implemented by transports that already
// enforce per-request public-HTTPS validation and address pinning (the
// mcpdirectory service). secureRemoteHTTPClient reuses such a transport so a
// directory probe can keep a caller-provided transport (for example a test
// transport that terminates TLS on loopback while the advertised endpoint is
// a public address) without weakening the pinning contract. Any other custom
// transport is still rejected.
type PublicHTTPSPinnedTransport interface {
	http.RoundTripper
	PublicHTTPSPinned() bool
}

// withRemoteSecurityFixtureClient is deliberately unexported and used only by
// same-package protocol tests that must terminate TLS on loopback while the
// advertised endpoint remains a public address. Production callers cannot
// select this path through configuration or an exported API.
func withRemoteSecurityFixtureClient(ctx context.Context, client *http.Client) context.Context {
	return context.WithValue(WithHTTPClient(ctx, client), remoteSecurityFixtureClientKey{}, client)
}

// secureRemoteHTTPClient is the mandatory transport for remote MCP RPC. It
// rejects non-public HTTPS endpoints, disables proxies and redirects, and pins
// the resolved public address for the request so DNS cannot redirect a call to
// an internal service after validation.
func secureRemoteHTTPClient(ctx context.Context, rawURL string) (*http.Client, error) {
	parsed, pinned, err := validateRemoteMCPURL(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	expectedHost := normalizeRemoteMCPHost(parsed.Hostname())
	expectedPort := parsed.Port()
	if expectedPort == "" {
		expectedPort = "443"
	}

	baseClient := httpClientForContext(ctx)
	if baseClient != nil {
		if pinned, ok := baseClient.Transport.(PublicHTTPSPinnedTransport); ok && pinned.PublicHTTPSPinned() {
			client := *baseClient
			client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
				return errors.New("remote MCP redirects are not allowed")
			}
			return &client, nil
		}
	}
	if fixture, _ := ctx.Value(remoteSecurityFixtureClientKey{}).(*http.Client); fixture != nil {
		client := *fixture
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return errors.New("remote MCP redirects are not allowed")
		}
		return &client, nil
	}
	client := *baseClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("remote MCP redirects are not allowed")
	}
	// Do not inherit caller transports. A custom DialContext, TLS configuration,
	// proxy, or certificate verifier could otherwise bypass the validated public
	// address and canonical HTTPS identity. The caller may still provide a
	// bounded client timeout through the context wrapper.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = nil
	transport.DialTLSContext = nil
	dialer := &net.Dialer{Timeout: defaultTimeout}
	transport.Proxy = nil
	transport.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		host, port, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, fmt.Errorf("invalid remote MCP dial address %q: %w", address, splitErr)
		}
		if normalizeRemoteMCPHost(host) != expectedHost || port != expectedPort {
			return nil, fmt.Errorf("remote MCP dial target %q does not match pinned destination", address)
		}
		return dialer.DialContext(dialCtx, network, net.JoinHostPort(pinned.String(), expectedPort))
	}
	client.Transport = transport
	return &client, nil
}

func validateRemoteMCPURL(ctx context.Context, rawURL string) (*url.URL, net.IP, error) {
	parsed, err := parseRemoteMCPURL(rawURL)
	if err != nil {
		return nil, nil, err
	}
	pinned, err := resolvePublicOAuthAddress(ctx, parsed.Hostname())
	if err != nil {
		return nil, nil, fmt.Errorf("remote MCP endpoint: %w", err)
	}
	return parsed, pinned, nil
}

func canonicalRemoteMCPOrigin(rawURL string) (string, error) {
	parsed, err := parseRemoteMCPURL(rawURL)
	if err != nil {
		return "", err
	}
	host := normalizeRemoteMCPHost(parsed.Hostname())
	if port := parsed.Port(); port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	}
	return "https://" + host, nil
}

func parseRemoteMCPURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("remote MCP endpoint must be an absolute public HTTPS URL without credentials or fragment")
	}
	host := normalizeRemoteMCPHost(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, fmt.Errorf("remote MCP endpoint %q is local", host)
	}
	if address := net.ParseIP(host); address != nil && !isPublicOAuthIP(address) {
		return nil, fmt.Errorf("remote MCP endpoint %q is not public", host)
	}
	return parsed, nil
}

func normalizeRemoteMCPHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
