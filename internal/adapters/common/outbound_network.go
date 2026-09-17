package common

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

const maxOutboundMediaRedirects = 5

type outboundMediaResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

var reservedOutboundMediaNetworks = []netip.Prefix{
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

func ValidateOutboundMediaURL(rawURL string, allowPrivate bool) (*url.URL, error) {
	return validateOutboundMediaURL(context.Background(), rawURL, allowPrivate, net.DefaultResolver)
}

func validateOutboundMediaURL(ctx context.Context, rawURL string, allowPrivate bool, resolver outboundMediaResolver) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported outbound media URL scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("outbound media URL must have a host and no user information")
	}
	if !allowPrivate {
		if _, err := resolvePublicOutboundMediaAddresses(ctx, resolver, parsed.Hostname()); err != nil {
			return nil, err
		}
	}
	return parsed, nil
}

// OutboundMediaHTTPClient clones the caller's client and, unless private
// fixtures are explicitly allowed, disables proxies and pins every dial to a
// public address resolved immediately before connection.
func OutboundMediaHTTPClient(client *http.Client, allowPrivate bool) (*http.Client, error) {
	return outboundMediaHTTPClient(client, allowPrivate, net.DefaultResolver)
}

func outboundMediaHTTPClient(client *http.Client, allowPrivate bool, resolver outboundMediaResolver) (*http.Client, error) {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	previous := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= maxOutboundMediaRedirects {
			return errors.New("outbound media redirect limit exceeded")
		}
		if _, err := validateOutboundMediaURL(request.Context(), request.URL.String(), allowPrivate, resolver); err != nil {
			return err
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && request.URL.Scheme != "https" {
			return errors.New("outbound media redirect cannot downgrade HTTPS")
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
	if allowPrivate {
		return &clone, nil
	}

	var transport *http.Transport
	switch base := client.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = base.Clone()
	default:
		return nil, fmt.Errorf("outbound media HTTP transport %T cannot enforce address pinning", client.Transport)
	}
	if transport.DialTLSContext != nil {
		return nil, errors.New("outbound media HTTP client cannot use a custom DialTLSContext")
	}
	originalDial := transport.DialContext
	if originalDial == nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		originalDial = dialer.DialContext
	}
	transport.Proxy = nil
	transport.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid outbound media dial address %q: %w", address, err)
		}
		addresses, err := resolvePublicOutboundMediaAddresses(dialCtx, resolver, host)
		if err != nil {
			return nil, err
		}
		var dialErrors []error
		for _, pinned := range addresses {
			connection, dialErr := originalDial(dialCtx, network, net.JoinHostPort(pinned.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			dialErrors = append(dialErrors, dialErr)
		}
		return nil, fmt.Errorf("dial pinned outbound media destination: %w", errors.Join(dialErrors...))
	}
	clone.Transport = transport
	return &clone, nil
}

func resolvePublicOutboundMediaAddresses(ctx context.Context, resolver outboundMediaResolver, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, fmt.Errorf("blocked private outbound media URL host: %s", host)
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if !isPublicOutboundMediaAddress(literal) {
			return nil, fmt.Errorf("blocked private outbound media URL host: %s", host)
		}
		return []netip.Addr{literal}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve outbound media URL host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("outbound media URL host %q resolved to no addresses", host)
	}
	public := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]struct{}, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublicOutboundMediaAddress(address) {
			return nil, fmt.Errorf("blocked private outbound media URL host: %s resolved to %s", host, address)
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		public = append(public, address)
	}
	return public, nil
}

func isPublicOutboundMediaAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, network := range reservedOutboundMediaNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}
