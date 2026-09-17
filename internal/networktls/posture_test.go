package networktls

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolverDefaultsStrictAndRecognizesOperatorOverride(t *testing.T) {
	t.Setenv(MCPX509StrictEnv, "")
	resolver := newResolverForTest(Config{Mode: ModeAuto}, "linux", func(context.Context) (bool, error) {
		return false, nil
	})
	if posture := resolver.Current(context.Background()); !posture.Strict || posture.Source != SourceAutoNone {
		t.Fatalf("default posture = %#v", posture)
	}

	t.Setenv(MCPX509StrictEnv, "relaxed")
	resolver.Invalidate()
	if posture := resolver.Current(context.Background()); posture.Strict || posture.Source != SourceOperatorEnv {
		t.Fatalf("relaxed override posture = %#v", posture)
	}

	t.Setenv(MCPX509StrictEnv, "definitely-not-a-mode")
	resolver.Invalidate()
	posture := resolver.Current(context.Background())
	if !posture.Strict || posture.Source != SourceAutoNone || !posture.Diagnostics.OverrideUnrecognized {
		t.Fatalf("invalid override did not fail closed through auto detection: %#v", posture)
	}
}

func TestResolverHonorsConfigBeforeAutomaticDetection(t *testing.T) {
	t.Setenv(MCPX509StrictEnv, "auto")
	for _, test := range []struct {
		mode   string
		strict bool
	}{
		{mode: ModeStrict, strict: true},
		{mode: ModeRelaxed, strict: false},
	} {
		t.Run(test.mode, func(t *testing.T) {
			called := false
			resolver := newResolverForTest(Config{Mode: test.mode}, "darwin", func(context.Context) (bool, error) {
				called = true
				return true, nil
			})
			posture := resolver.Current(context.Background())
			if posture.Strict != test.strict || posture.Source != SourceConfig || called {
				t.Fatalf("configured posture = %#v, detector called=%v", posture, called)
			}
		})
	}
}

func TestResolverUsesValidatedNetworkCABundleAsAutomaticSignal(t *testing.T) {
	t.Setenv(MCPX509StrictEnv, "")
	bundle := filepath.Join(t.TempDir(), "corporate.pem")
	if err := os.WriteFile(bundle, testCertificatePEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := newResolverForTest(Config{Mode: ModeAuto, CABundle: bundle}, "linux", nil)
	posture := resolver.Current(context.Background())
	if posture.Strict || posture.Source != SourceNetworkCABundle || posture.CABundle != bundle {
		t.Fatalf("CA-bundle posture = %#v", posture)
	}
}

func TestResolverRejectsUnusableBundleAndFailsClosed(t *testing.T) {
	t.Setenv(MCPX509StrictEnv, "")
	missing := filepath.Join(t.TempDir(), "missing.pem")
	resolver := newResolverForTest(Config{Mode: ModeAuto, CABundle: missing}, "linux", nil)
	posture := resolver.Current(context.Background())
	if !posture.Strict || posture.Source != SourceAutoNone || posture.CABundle != "" || posture.Diagnostics.CABundleRefusal == "" {
		t.Fatalf("unusable bundle posture = %#v", posture)
	}
}

func TestResolverCachesForSixMinutesAndCanBeInvalidated(t *testing.T) {
	t.Setenv(MCPX509StrictEnv, "")
	now := time.Unix(1_700_000_000, 0)
	calls := 0
	resolver := newResolverForTest(Config{Mode: ModeAuto}, "darwin", func(context.Context) (bool, error) {
		calls++
		return calls > 1, nil
	})
	resolver.now = func() time.Time { return now }
	if !resolver.Current(context.Background()).Strict || calls != 1 {
		t.Fatalf("first resolution calls=%d", calls)
	}
	now = now.Add(5 * time.Minute)
	if !resolver.Current(context.Background()).Strict || calls != 1 {
		t.Fatalf("cached resolution calls=%d", calls)
	}
	now = now.Add(2 * time.Minute)
	if resolver.Current(context.Background()).Strict || calls != 2 {
		t.Fatalf("refreshed resolution calls=%d", calls)
	}
	resolver.Invalidate()
	_ = resolver.Current(context.Background())
	if calls != 3 {
		t.Fatalf("invalidated resolution calls=%d", calls)
	}
}

func TestAppendCABundleKeepsExistingRootsAndAddsConfiguredCertificates(t *testing.T) {
	pool, err := appendCABundle(x509.NewCertPool(), testCertificatePEM(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(pool.Subjects()) != 1 {
		t.Fatalf("configured certificate subjects = %d", len(pool.Subjects()))
	}
}

func TestHTTPClientUsesConfiguredBundleWithoutMutatingBaseClient(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	bundle := filepath.Join(t.TempDir(), "server.pem")
	certificate := server.Certificate()
	if err := os.WriteFile(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	baseTransport := http.DefaultTransport.(*http.Transport).Clone()
	baseTLSConfig := baseTransport.TLSClientConfig
	var baseRootCAs *x509.CertPool
	if baseTLSConfig != nil {
		baseRootCAs = baseTLSConfig.RootCAs
	}
	base := &http.Client{Transport: baseTransport}
	client, err := HTTPClient(base, bundle)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("TLS response status = %d", response.StatusCode)
	}
	if base.Transport != baseTransport || baseTransport.TLSClientConfig != baseTLSConfig ||
		baseTLSConfig != nil && baseTLSConfig.RootCAs != baseRootCAs {
		t.Fatal("base HTTP client transport was mutated")
	}
}

func TestNormalizeProxyURLAndHTTPClientProxyAuthority(t *testing.T) {
	normalized, err := NormalizeProxyURL(" http://Proxy.Example:8080/ ")
	if err != nil || normalized != "http://proxy.example:8080" {
		t.Fatalf("normalized proxy = %q, %v", normalized, err)
	}
	for _, invalid := range []string{
		"https://proxy.example:8443", "socks5://proxy.example:1080", "http://proxy.example/path",
		"http://user:secret@proxy.example:8080", "http://proxy.example?token=secret", "http://proxy.example:70000",
		"http://\r\nproxy.example",
	} {
		if _, err := NormalizeProxyURL(invalid); err == nil {
			t.Fatalf("invalid proxy %q was accepted", invalid)
		}
	}
	client, err := HTTPClient(&http.Client{}, "", normalized)
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	request, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	proxy, err := transport.Proxy(request)
	if err != nil || proxy == nil || proxy.String() != normalized {
		t.Fatalf("configured HTTP proxy = %#v, %v", proxy, err)
	}
}

func testCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Synon local TLS posture test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	raw, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}
