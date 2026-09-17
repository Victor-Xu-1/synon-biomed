package mcpdirectory

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type publicIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// ErrPublicDestinationUnavailable marks a syntactically valid public-hostname
// request that could not produce a usable public address. Callers may expose
// this as a recoverable source outage without weakening the SSRF boundary: no
// connection is attempted and literal/local/private destinations remain hard
// policy errors.
var ErrPublicDestinationUnavailable = errors.New("public destination is unavailable")

type publicDestinationUnavailableError struct {
	message string
	cause   error
}

func (err *publicDestinationUnavailableError) Error() string { return err.message }

func (err *publicDestinationUnavailableError) Unwrap() error { return err.cause }

func (err *publicDestinationUnavailableError) Is(target error) bool {
	return target == ErrPublicDestinationUnavailable
}

// IsPublicDestinationUnavailable reports whether a public hostname was denied
// before dialing because DNS failed, returned no address, or resolved to a
// non-public/sinkhole address.
func IsPublicDestinationUnavailable(err error) bool {
	return errors.Is(err, ErrPublicDestinationUnavailable)
}

var reservedOutboundNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// ValidatePublicHTTPSURL applies the same syntax, DNS, and reserved-network
// policy used by directory discovery before another MCP surface stores a URL.
func ValidatePublicHTTPSURL(ctx context.Context, rawURL string) error {
	parsed, err := validateHTTPSURL(rawURL, false)
	if err != nil {
		return err
	}
	_, err = resolvePublicAddresses(ctx, nil, normalizeHost(parsed.Hostname()))
	return err
}

// SecureHTTPClient returns the address-pinned client used by directory probes
// so custom MCP tool discovery cannot bypass the shared outbound policy.
func SecureHTTPClient(ctx context.Context, rawURL string, base *http.Client) (*http.Client, error) {
	return (&Service{client: base}).secureHTTPClient(ctx, rawURL)
}

func (s *Service) secureHTTPClient(ctx context.Context, rawURL string) (*http.Client, error) {
	_, _, err := resolvePublicHTTPSURL(ctx, s.resolver, rawURL)
	if err != nil {
		return nil, err
	}
	baseClient := s.client
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	transport, err := secureBaseTransport(baseClient)
	if err != nil {
		return nil, err
	}
	client := *baseClient
	client.Transport = &publicHTTPSRoundTripper{base: transport, resolver: s.resolver}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("MCP directory redirect chain exceeds 10 hops")
		}
		if _, _, err := resolvePublicHTTPSURL(request.Context(), s.resolver, request.URL.String()); err != nil {
			return fmt.Errorf("MCP directory redirect destination is not an approved public HTTPS origin: %w", err)
		}
		return nil
	}
	return &client, nil
}

// publicHTTPSRoundTripper resolves and pins every outgoing request separately.
// net/http invokes it again after each accepted redirect, so a redirect cannot
// reuse the prior hop's DNS decision or pinned dial target.
type publicHTTPSRoundTripper struct {
	base     *http.Transport
	resolver publicIPResolver
}

// PublicHTTPSPinned reports that this transport already enforces the shared
// public-HTTPS pinning contract, so layered callers may reuse it instead of
// replacing it with a fresh transport that would discard a trusted transport
// provided through the server's HTTP client.
func (t *publicHTTPSRoundTripper) PublicHTTPSPinned() bool { return true }

func (t *publicHTTPSRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("MCP directory request URL is required")
	}
	parsed, addresses, err := resolvePublicHTTPSURL(request.Context(), t.resolver, request.URL.String())
	if err != nil {
		return nil, fmt.Errorf("MCP directory request destination is not an approved public HTTPS origin: %w", err)
	}
	transport := t.base.Clone()
	var proxyURL *url.URL
	if transport.Proxy != nil {
		var proxyErr error
		proxyURL, proxyErr = transport.Proxy(request)
		if proxyErr != nil {
			return nil, fmt.Errorf("resolve trusted MCP upstream proxy: %w", proxyErr)
		}
	}
	if proxyURL == nil {
		configurePinnedDial(transport, normalizeHost(parsed.Hostname()), parsed.Port(), addresses)
	}
	return transport.RoundTrip(request)
}

func resolvePublicHTTPSURL(ctx context.Context, resolver publicIPResolver, rawURL string) (*url.URL, []netip.Addr, error) {
	parsed, err := validateHTTPSURL(rawURL, false)
	if err != nil {
		return nil, nil, err
	}
	addresses, err := resolvePublicAddresses(ctx, resolver, normalizeHost(parsed.Hostname()))
	if err != nil {
		return nil, nil, err
	}
	return parsed, addresses, nil
}

func secureBaseTransport(baseClient *http.Client) (*http.Transport, error) {
	var transport *http.Transport
	switch baseTransport := baseClient.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = baseTransport.Clone()
	default:
		return nil, fmt.Errorf("MCP directory HTTP client transport %T cannot enforce address pinning", baseClient.Transport)
	}
	if transport.DialTLSContext != nil {
		return nil, errors.New("MCP directory HTTP client cannot use a custom DialTLSContext")
	}
	return transport, nil
}

func configurePinnedDial(transport *http.Transport, host, port string, addresses []netip.Addr) {
	if port == "" {
		port = "443"
	}
	originalDial := transport.DialContext
	if originalDial == nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		originalDial = dialer.DialContext
	}
	transport.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		dialHost, dialPort, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, fmt.Errorf("invalid MCP directory dial address %q: %w", address, splitErr)
		}
		if normalizeHost(dialHost) != host || dialPort != port {
			return nil, fmt.Errorf("MCP directory dial target %q does not match pinned destination", address)
		}
		var dialErrors []error
		for _, pinned := range addresses {
			conn, dialErr := originalDial(dialCtx, network, net.JoinHostPort(pinned.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			dialErrors = append(dialErrors, dialErr)
		}
		return nil, fmt.Errorf("dial pinned MCP directory destination: %w", errors.Join(dialErrors...))
	}
}

func resolvePublicAddresses(ctx context.Context, resolver publicIPResolver, host string) ([]netip.Addr, error) {
	if isLocalHostname(host) {
		return nil, fmt.Errorf("MCP directory destination %q is local", host)
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if !isPublicOutboundIP(literal) {
			return nil, fmt.Errorf("MCP directory destination %q is not a public address", host)
		}
		return []netip.Addr{literal}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &publicDestinationUnavailableError{
			message: fmt.Sprintf("resolve MCP directory destination %q: %v", host, err),
			cause:   err,
		}
	}
	if len(addresses) == 0 {
		return nil, &publicDestinationUnavailableError{
			message: fmt.Sprintf("MCP directory destination %q resolved to no addresses", host),
		}
	}
	public := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublicOutboundIP(address) {
			return nil, &publicDestinationUnavailableError{
				message: fmt.Sprintf("MCP directory destination %q resolved to non-public address %s", host, address),
			}
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		public = append(public, address)
	}
	return public, nil
}

func isPublicOutboundIP(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, network := range reservedOutboundNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func isLocalHostname(host string) bool {
	host = normalizeHost(host)
	return host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local")
}
