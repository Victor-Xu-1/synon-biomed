package mcpdirectory

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

const testCatalogUUID = "4f5b9638-2dc3-4b70-bbea-9af732ba57ef"

type staticPublicIPResolver struct {
	addresses []netip.Addr
	err       error
}

func (resolver staticPublicIPResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return resolver.addresses, resolver.err
}

func TestValidateHTTPSURLRejectsNonPublicDestinations(t *testing.T) {
	t.Parallel()
	tests := []string{
		"https://0.0.0.0/catalog",
		"https://10.0.0.1/catalog",
		"https://100.64.0.1/catalog",
		"https://127.0.0.1/catalog",
		"https://169.254.169.254/latest/meta-data",
		"https://172.16.0.1/catalog",
		"https://192.0.2.1/catalog",
		"https://192.168.0.1/catalog",
		"https://198.18.0.1/catalog",
		"https://198.51.100.1/catalog",
		"https://203.0.113.1/catalog",
		"https://224.0.0.1/catalog",
		"https://240.0.0.1/catalog",
		"https://[::]/catalog",
		"https://[::1]/catalog",
		"https://[100::1]/catalog",
		"https://[2001:db8::1]/catalog",
		"https://[fc00::1]/catalog",
		"https://[fe80::1]/catalog",
		"https://[ff00::1]/catalog",
	}
	for _, rawURL := range tests {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			if _, err := validateHTTPSURL(rawURL, false); err == nil {
				t.Fatalf("validateHTTPSURL(%q) accepted a non-public destination", rawURL)
			}
		})
	}
}

func TestDirectoryRedirectToLoopbackHasZeroTargetAccess(t *testing.T) {
	var targetAccesses atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetAccesses.Add(1)
		writeEmptyManifest(t, w)
	}))
	defer target.Close()

	var originAccesses atomic.Int64
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originAccesses.Add(1)
		http.Redirect(w, r, target.URL+"/catalog", http.StatusFound)
	}))
	defer origin.Close()

	client, publicURL := publicMappedTLSClient(t, origin)
	service := newTestService(t, client)
	_, _, err := service.AddDirectory(context.Background(), "user-1", AddDirectoryInput{
		Name: "redirect", URL: publicURL + "/catalog", CatalogUUID: testCatalogUUID,
	})
	if err == nil {
		t.Fatal("redirect to loopback was accepted")
	}
	if originAccesses.Load() != 1 {
		t.Fatalf("origin accesses = %d, want 1", originAccesses.Load())
	}
	if targetAccesses.Load() != 0 {
		t.Fatalf("redirect target accesses = %d, want zero", targetAccesses.Load())
	}
}

func TestSecureHTTPClientFollowsPublicRedirectWithPerHopValidation(t *testing.T) {
	var targetAccesses atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetAccesses.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	var originAccesses atomic.Int64
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originAccesses.Add(1)
		http.Redirect(w, r, publicMappedServerURL(t, target)+"/redirected", http.StatusFound)
	}))
	defer origin.Close()

	base, originURL := publicMappedTLSClient(t, origin, target)
	client, err := SecureHTTPClient(context.Background(), originURL+"/origin", base)
	if err != nil {
		t.Fatalf("SecureHTTPClient() error = %v", err)
	}
	response, err := client.Get(originURL + "/origin")
	if err != nil {
		t.Fatalf("GET redirected public URL error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("redirected response status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	if originAccesses.Load() != 1 || targetAccesses.Load() != 1 {
		t.Fatalf("accesses origin=%d target=%d, want one each", originAccesses.Load(), targetAccesses.Load())
	}
}

func TestIsPublicOutboundIPAllowsGlobalIPv6(t *testing.T) {
	address := netip.MustParseAddr("2a03:2880:f12c:83:face:b00c:0:25de")
	if !isPublicOutboundIP(address) {
		t.Fatalf("global IPv6 %s was rejected as non-public", address)
	}
}

func TestResolvePublicAddressesClassifiesHostnameSinkholeWithoutRelaxingLiteralPolicy(t *testing.T) {
	resolver := staticPublicIPResolver{addresses: []netip.Addr{netip.MustParseAddr("2001::1")}}
	if _, err := resolvePublicAddresses(context.Background(), resolver, "blocked.example"); err == nil || !IsPublicDestinationUnavailable(err) {
		t.Fatalf("hostname sinkhole error = %v, want recoverable destination-unavailable classification", err)
	}

	_, err := resolvePublicAddresses(context.Background(), resolver, "2001::1")
	if err == nil || IsPublicDestinationUnavailable(err) {
		t.Fatalf("literal reserved address error = %v, want hard policy rejection", err)
	}
}

func newTestService(t *testing.T, client *http.Client) *Service {
	t.Helper()
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store, t.TempDir(), client)
}

func TestSecureBaseTransportPreservesTrustedUpstreamProxy(t *testing.T) {
	proxy, err := url.Parse("http://proxy.example:8080")
	if err != nil {
		t.Fatal(err)
	}
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	baseTransport.Proxy = http.ProxyURL(proxy)
	transport, err := secureBaseTransport(&http.Client{Transport: baseTransport})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	resolved, err := transport.Proxy(request)
	if err != nil || resolved == nil || resolved.String() != proxy.String() {
		t.Fatalf("trusted upstream proxy = %#v, %v", resolved, err)
	}
}

func publicMappedTLSClient(t *testing.T, server *httptest.Server, additional ...*httptest.Server) (*http.Client, string) {
	t.Helper()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	mappings := map[string]string{port: serverURL.Host}
	for _, mappedServer := range additional {
		mappedURL, parseErr := url.Parse(mappedServer.URL)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		_, mappedPort, splitErr := net.SplitHostPort(mappedURL.Host)
		if splitErr != nil {
			t.Fatal(splitErr)
		}
		mappings[mappedPort] = mappedURL.Host
	}
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Test-only route uses a public synthetic IP.
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		_, requestedPort, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		if mappedAddress, ok := mappings[requestedPort]; ok {
			address = mappedAddress
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return &http.Client{Transport: transport}, "https://8.8.8.8:" + port
}

func publicMappedServerURL(t *testing.T, server *httptest.Server) string {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	return "https://8.8.8.8:" + port
}

func writeEmptyManifest(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"catalogUuid": testCatalogUUID,
		"connectors":  []any{},
	}); err != nil && !strings.Contains(err.Error(), "closed") {
		t.Errorf("encode manifest: %v", err)
	}
}

var _ = tls.VersionTLS13
