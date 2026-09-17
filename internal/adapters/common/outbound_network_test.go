package common

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type outboundMediaResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f outboundMediaResolverFunc) LookupNetIP(ctx context.Context, network string, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestOutboundMediaURLPolicyBlocksPrivateTargetsAndRedirects(t *testing.T) {
	if _, err := ValidateOutboundMediaURL("file:///tmp/result.png", false); err == nil {
		t.Fatal("file URL was accepted as remote outbound media")
	}
	if _, err := ValidateOutboundMediaURL("http://127.0.0.1/result.png", false); err == nil || !strings.Contains(err.Error(), "blocked private") {
		t.Fatalf("private URL error = %v", err)
	}
	if _, err := ValidateOutboundMediaURL("http://127.0.0.1/result.png", true); err != nil {
		t.Fatalf("explicit private fixture URL rejected: %v", err)
	}

	client, err := OutboundMediaHTTPClient(http.DefaultClient, false)
	if err != nil {
		t.Fatalf("secure outbound client: %v", err)
	}
	target, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/private.png", nil)
	origin, _ := http.NewRequest(http.MethodGet, "https://example.com/public.png", nil)
	if err := client.CheckRedirect(target, []*http.Request{origin}); err == nil || !strings.Contains(err.Error(), "blocked private") {
		t.Fatalf("private redirect error = %v", err)
	}
}

func TestOutboundMediaURLPolicyBoundsRedirectsAndRejectsDowngrade(t *testing.T) {
	client, err := OutboundMediaHTTPClient(http.DefaultClient, true)
	if err != nil {
		t.Fatalf("fixture outbound client: %v", err)
	}
	target, _ := http.NewRequest(http.MethodGet, "http://example.com/image.png", nil)
	origin, _ := http.NewRequest(http.MethodGet, "https://example.com/image.png", nil)
	if err := client.CheckRedirect(target, []*http.Request{origin}); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("HTTPS downgrade error = %v", err)
	}
	via := make([]*http.Request, maxOutboundMediaRedirects)
	for index := range via {
		via[index] = origin
	}
	secureTarget, _ := http.NewRequest(http.MethodGet, "https://example.com/image.png", nil)
	if err := client.CheckRedirect(secureTarget, via); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("redirect limit error = %v", err)
	}
}

func TestOutboundMediaHTTPClientPinsDialToValidatedPublicAddress(t *testing.T) {
	pinned := netip.MustParseAddr("8.8.8.8")
	resolver := outboundMediaResolverFunc(func(_ context.Context, network string, host string) ([]netip.Addr, error) {
		if network != "ip" || host != "media.example" {
			t.Fatalf("LookupNetIP(%q, %q)", network, host)
		}
		return []netip.Addr{pinned}, nil
	})
	sentinel := errors.New("dial stopped by test")
	var dialed string
	base := &http.Client{Transport: &http.Transport{DialContext: func(_ context.Context, network string, address string) (net.Conn, error) {
		if network != "tcp" {
			t.Fatalf("dial network = %q", network)
		}
		dialed = address
		return nil, sentinel
	}}}

	client, err := outboundMediaHTTPClient(base, false, resolver)
	if err != nil {
		t.Fatalf("outboundMediaHTTPClient() error = %v", err)
	}
	transport := client.Transport.(*http.Transport)
	if _, err := transport.DialContext(context.Background(), "tcp", "media.example:443"); !errors.Is(err, sentinel) {
		t.Fatalf("DialContext() error = %v", err)
	}
	if dialed != "8.8.8.8:443" {
		t.Fatalf("pinned dial address = %q", dialed)
	}
}

func TestOutboundMediaHTTPClientRejectsDNSRebindingAtDialTime(t *testing.T) {
	calls := 0
	resolver := outboundMediaResolverFunc(func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
		if host != "media.example" {
			t.Fatalf("resolver host = %q", host)
		}
		calls++
		if calls == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	parsed, err := validateOutboundMediaURL(context.Background(), "https://media.example/image.png", false, resolver)
	if err != nil || parsed.Hostname() != "media.example" {
		t.Fatalf("initial URL validation = %v, %v", parsed, err)
	}
	dialed := false
	base := &http.Client{Transport: &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	}}}
	client, err := outboundMediaHTTPClient(base, false, resolver)
	if err != nil {
		t.Fatalf("outboundMediaHTTPClient() error = %v", err)
	}
	transport := client.Transport.(*http.Transport)
	if _, err := transport.DialContext(context.Background(), "tcp", "media.example:443"); err == nil || !strings.Contains(err.Error(), "blocked private") {
		t.Fatalf("rebind DialContext() error = %v", err)
	}
	if dialed {
		t.Fatal("private rebound address reached the underlying dialer")
	}
}
