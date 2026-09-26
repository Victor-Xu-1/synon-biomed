package securefetch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func TestClientStreamsPinnedHTTPSResponseWithinPolicy(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Host == "" || request.Header.Get("User-Agent") != "synon-biomed-test" {
			t.Fatalf("request host/user agent = %q/%q", request.Host, request.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "chemical/x-pdb")
		_, _ = io.WriteString(w, "HEADER    TEST\nATOM      1\n")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	response, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb", policy)
	if err != nil {
		t.Fatalf("Fetch() error = %v cause=%v", err, errors.Unwrap(err))
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "HEADER    TEST\nATOM      1\n" || response.ContentType != "chemical/x-pdb" || response.StatusCode != http.StatusOK {
		t.Fatalf("response = %#v body=%q", response, body)
	}
}

func TestClientLongLivedTransferUsesIdleTimeoutInsteadOfWallClockTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "chemical/x-pdb")
		_, _ = io.WriteString(w, "HEADER\n")
		w.(http.Flusher).Flush()
		time.Sleep(80 * time.Millisecond)
		_, _ = io.WriteString(w, "ATOM\n")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.Timeout = 20 * time.Millisecond
	policy.LongLivedTransfer = true
	policy.TransferIdleTimeout = 250 * time.Millisecond
	response, err := client.Fetch(context.Background(), target+"/long-transfer", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "HEADER\nATOM\n" {
		t.Fatalf("long transfer body=%q err=%v", body, err)
	}
}

func TestClientLongLivedTransferFailsAfterTrueBodyIdle(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "chemical/x-pdb")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(250 * time.Millisecond)
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.LongLivedTransfer = true
	policy.TransferIdleTimeout = 25 * time.Millisecond
	response, err := client.Fetch(context.Background(), target+"/idle-transfer", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, err = io.ReadAll(response.Body)
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() {
		t.Fatalf("idle transfer error=%v", err)
	}
}

func TestTransferDeadlinePolicyRequiresIdleProtection(t *testing.T) {
	policy := Policy{AllowedHosts: []string{"example.org"}, AcceptedMediaTypes: []string{"application/octet-stream"}, MaxBytes: 10 << 30}
	if _, err := compilePolicy(policy); !IsCode(err, CodeInvalidPolicy) {
		t.Fatal("ordinary request silently became unbounded")
	}
	policy.LongLivedTransfer = true
	if _, err := compilePolicy(policy); !IsCode(err, CodeInvalidPolicy) {
		t.Fatal("long transfer accepted without idle protection")
	}
	policy.TransferIdleTimeout = time.Second
	if _, err := compilePolicy(policy); err != nil {
		t.Fatalf("explicit zero total deadline rejected: %v", err)
	}
	policy.Timeout = -time.Second
	if _, err := compilePolicy(policy); !IsCode(err, CodeInvalidPolicy) {
		t.Fatal("negative timeout accepted")
	}
}

func TestClientUsesOnlyExplicitValidatedProxyRoute(t *testing.T) {
	client := New(Options{ProxyURL: "http://127.0.0.1:7890"})
	request, err := http.NewRequest(http.MethodGet, "https://files.rcsb.org/download/4TZ4.pdb", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := client.transport.Proxy(request)
	if err != nil || proxy == nil || proxy.String() != "http://127.0.0.1:7890" {
		t.Fatalf("explicit proxy=%v err=%v", proxy, err)
	}
	invalid := New(Options{ProxyURL: "http://proxy.example/path"})
	_, err = invalid.Fetch(context.Background(), "https://files.rcsb.org/download/4TZ4.pdb", Policy{
		AllowedHosts: []string{"files.rcsb.org"}, AcceptedMediaTypes: []string{"chemical/x-pdb"},
		MaxBytes: 1 << 20, Timeout: time.Second,
	})
	if !IsCode(err, CodeInvalidPolicy) {
		t.Fatalf("invalid explicit proxy error=%v", err)
	}
}

func TestClientSendsAndValidatesResumeRange(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Range"); got != "bytes=5-" {
			t.Fatalf("Range header = %q", got)
		}
		if got := request.Header.Get("If-Range"); got != `"version-1"` {
			t.Fatalf("If-Range header = %q", got)
		}
		w.Header().Set("Content-Type", "chemical/x-pdb")
		w.Header().Set("Content-Range", "bytes 5-9/10")
		w.Header().Set("ETag", `"version-1"`)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "67890")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.RangeStart = 5
	policy.IfRange = `"version-1"`
	policy.MaxBytes = 10
	response, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "67890" || response.ContentRangeStart != 5 ||
		response.ContentRangeEnd != 9 || response.ContentRangeTotal != 10 ||
		response.ETag != `"version-1"` {
		t.Fatalf("response=%#v body=%q", response, body)
	}
}

func TestClientFetchesExactPrefixForLegacyPartialVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Range"); got != "bytes=0-4" || request.Header.Get("If-Range") != "" {
			t.Fatalf("prefix headers=%#v", request.Header)
		}
		w.Header().Set("Content-Type", "chemical/x-pdb")
		w.Header().Set("Content-Range", "bytes 0-4/10")
		w.Header().Set("Last-Modified", "Tue, 19 Jun 2018 00:12:01 GMT")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "12345")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.PrefixBytes = 5
	policy.MaxBytes = 10
	response, err := client.Fetch(context.Background(), target+"/prefix", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "12345" || response.ContentRangeTotal != 10 ||
		ResumeValidator(response) != "Tue, 19 Jun 2018 00:12:01 GMT" {
		t.Fatalf("prefix response=%#v body=%q err=%v", response, body, err)
	}
}

func TestClientReadsExactPrefixWhenRangeIsIgnored(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-4" {
			t.Errorf("prefix range=%q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Type", "chemical/x-pdb")
		w.Header().Set("Content-Length", "10")
		w.Header().Set("ETag", `"fixed"`)
		_, _ = io.WriteString(w, "1234567890")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.MaxBytes, policy.PrefixBytes = 10, 5
	response, err := client.Fetch(context.Background(), target+"/source.pdb", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "12345" || response.ContentLength != 10 {
		t.Fatalf("prefix=%q full_length=%d error=%v", body, response.ContentLength, err)
	}
}

func TestClientRejectsInvalidResumeRangeResponse(t *testing.T) {
	for _, test := range []struct {
		name         string
		contentRange string
	}{
		{name: "wrong start", contentRange: "bytes 4-8/10"},
		{name: "malformed", contentRange: "bytes nope"},
		{name: "oversized total", contentRange: "bytes 5-9/11"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "chemical/x-pdb")
				w.Header().Set("Content-Range", test.contentRange)
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, "67890")
			}))
			defer server.Close()
			client, target, policy := testClient(t, server)
			policy.RangeStart = 5
			policy.IfRange = `"version-1"`
			policy.MaxBytes = 10
			if _, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb", policy); !IsCode(err, CodeContentRange) {
				t.Fatalf("content-range error = %v", err)
			}
		})
	}
}

func TestClientAllowsSafeFullRestartWhenServerIgnoresResumeRange(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Range") != "bytes=5-" || request.Header.Get("If-Range") != `"version-1"` {
			t.Fatalf("resume headers = %#v", request.Header)
		}
		w.Header().Set("Content-Type", "chemical/x-pdb")
		w.Header().Set("ETag", `"version-2"`)
		_, _ = io.WriteString(w, "1234567890")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.RangeStart = 5
	policy.IfRange = `"version-1"`
	policy.MaxBytes = 10
	response, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "1234567890" || response.StatusCode != http.StatusOK ||
		response.ContentRangeStart != -1 || response.ETag != `"version-2"` {
		t.Fatalf("response=%#v body=%q err=%v", response, body, err)
	}
}

func TestClientRejectsUnsafeIfRangePolicyBeforeDial(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid resume policy reached the network")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.RangeStart = 5
	policy.IfRange = "W/\"weak-validator\""
	if _, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb", policy); !IsCode(err, CodeInvalidPolicy) {
		t.Fatalf("weak If-Range error = %v", err)
	}
}

func TestClientRejectsContentLengthAndStreamingOversize(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "content length", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "chemical/x-pdb")
			w.Header().Set("Content-Length", "6")
			_, _ = io.WriteString(w, "123456")
		}},
		{name: "stream", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "chemical/x-pdb")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = io.WriteString(w, "123456")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(test.handler)
			defer server.Close()
			client, target, policy := testClient(t, server)
			policy.MaxBytes = 5
			response, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb", policy)
			if err == nil {
				defer response.Body.Close()
				_, err = io.ReadAll(response.Body)
			}
			if !IsCode(err, CodeResponseTooLarge) {
				t.Fatalf("oversize error = %v", err)
			}
		})
	}
}

func TestClientRejectsRedirectOutsideClosedPolicyBeforeDial(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, "https://127.0.0.1/private", http.StatusFound)
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	_, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb", policy)
	if !IsCode(err, CodeRedirect) {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestClientAllowsBoundedPublicCrossHostRedirectWhenExplicitlyEnabled(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			_, port, err := net.SplitHostPort(request.Host)
			if err != nil {
				t.Fatal(err)
			}
			http.Redirect(w, request, "https://assets.example.org:"+port+"/archive.tar.gz", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "chemical/x-pdb")
		_, _ = io.WriteString(w, "public release payload")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.AllowPublicRedirects = true
	response, err := client.Fetch(context.Background(), target+"/start", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "public release payload" || response.FinalURL.Hostname() != "assets.example.org" {
		t.Fatalf("redirect response=%#v body=%q err=%v", response, body, err)
	}
}

func TestClientPublicRedirectModeStillRejectsPrivateResolution(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, port, err := net.SplitHostPort(request.Host)
		if err != nil {
			t.Fatal(err)
		}
		http.Redirect(w, request, "https://assets.example.org:"+port+"/archive.tar.gz", http.StatusFound)
	}))
	defer server.Close()
	client, target, policy := testClientWithResolver(t, server, resolverFunc(func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host == "assets.example.org" {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}))
	policy.AllowPublicRedirects = true
	_, err := client.Fetch(context.Background(), target+"/start", policy)
	if !IsCode(err, CodeRedirect) {
		t.Fatalf("private redirect error = %v", err)
	}
}

func TestClientPublicRedirectModeDoesNotExpandInitialSourceAuthority(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "chemical/x-pdb")
		_, _ = io.WriteString(w, "unexpected")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	policy.AllowPublicRedirects = true
	target = strings.Replace(target, "files.rcsb.org", "assets.example.org", 1)
	_, err := client.Fetch(context.Background(), target+"/untrusted-start", policy)
	if !IsCode(err, CodeInvalidRequest) {
		t.Fatalf("unlisted initial source error = %v", err)
	}
}

func TestClientRejectsPrivateResolutionAndDoesNotEchoRequest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client, target, policy := testClientWithResolver(t, server, resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}))
	_, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb?token=must-not-appear", policy)
	if !IsCode(err, CodeNonPublic) || err.Error() != string(CodeNonPublic) {
		t.Fatalf("private destination error = %v", err)
	}
}

func TestClientRejectsWrongMediaTypeAndHonorsCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/slow" {
			<-request.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>not a structure</html>")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	if _, err := client.Fetch(context.Background(), target+"/wrong", policy); !IsCode(err, CodeContentType) {
		t.Fatalf("content type error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Fetch(ctx, target+"/slow", policy); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestClientAllowsMissingContentTypeOnlyWhenExplicitlyOptedIn(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Content-Type"] = nil
		_, _ = w.Write([]byte{0x89, 'H', 'D', 'F', '\r', '\n', 0x1a, '\n'})
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	if _, err := client.Fetch(context.Background(), target+"/matrix.h5", policy); !IsCode(err, CodeContentType) {
		t.Fatalf("missing content type without opt-in error=%v", err)
	}
	policy.AllowMissingContentType = true
	response, err := client.Fetch(context.Background(), target+"/matrix.h5", policy)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ContentType != "" {
		t.Fatalf("missing content type was invented: %q", response.ContentType)
	}
}

func TestClientExposesStatusAsStructuredMetadataWithoutChangingSafeError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "upstream body must not appear in the error")
	}))
	defer server.Close()
	client, target, policy := testClient(t, server)
	_, err := client.Fetch(context.Background(), target+"/download/4TZ4.pdb?secret=hidden", policy)
	status, ok := HTTPStatus(err)
	if !ok || status != http.StatusTooManyRequests || err.Error() != string(CodeStatus) {
		t.Fatalf("status=%d ok=%t error=%v", status, ok, err)
	}
}

func testClient(t *testing.T, server *httptest.Server) (*Client, string, Policy) {
	return testClientWithResolver(t, server, resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}))
}

func testClientWithResolver(t *testing.T, server *httptest.Server, resolver AddressResolver) (*Client, string, Policy) {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Test-only TLS server; production clients cannot supply a custom transport.
	dialAddress := server.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, dialAddress)
	}
	base := *server.Client()
	base.Transport = transport
	client := New(Options{
		HTTPClient:                   &base,
		TestOnlyAllowCustomTransport: true,
		Resolver:                     resolver,
	})
	target := "https://files.rcsb.org:" + port
	policy := Policy{
		AllowedHosts: []string{"files.rcsb.org"}, AllowedPorts: []string{strconv.Itoa(mustPort(t, port))},
		AcceptedMediaTypes: []string{"chemical/x-pdb"}, MaxRedirects: 3, MaxBytes: 1 << 20,
		Timeout: 5 * time.Second, UserAgent: "synon-biomed-test",
	}
	return client, target, policy
}

func TestClientRejectsCustomTransportUnlessExplicitlyTestOnly(t *testing.T) {
	dials := 0
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("unexpected dial")
	}
	client := New(Options{HTTPClient: &http.Client{Transport: transport}})
	_, err := client.Fetch(context.Background(), "https://files.rcsb.org/download/4TZ4.pdb", Policy{
		AllowedHosts: []string{"files.rcsb.org"}, AcceptedMediaTypes: []string{"chemical/x-pdb"},
		MaxBytes: 1 << 20, Timeout: time.Second,
	})
	if !IsCode(err, CodeInvalidPolicy) || dials != 0 {
		t.Fatalf("custom transport error=%v dials=%d", err, dials)
	}
}

func TestProductionClientDoesNotInheritMutableHTTPGlobals(t *testing.T) {
	originalClient, originalTransport := http.DefaultClient, http.DefaultTransport
	t.Cleanup(func() {
		http.DefaultClient, http.DefaultTransport = originalClient, originalTransport
	})
	poisonedDials := 0
	poisoned := http.DefaultTransport.(*http.Transport).Clone()
	poisoned.DialContext = func(context.Context, string, string) (net.Conn, error) {
		poisonedDials++
		return nil, errors.New("poisoned global dialer")
	}
	http.DefaultTransport = poisoned
	http.DefaultClient = &http.Client{Transport: poisoned}
	client := New(Options{Resolver: resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = client.transport.DialContext(ctx, "tcp", "files.rcsb.org:443")
	if poisonedDials != 0 || client.transport == poisoned {
		t.Fatalf("production client inherited mutable HTTP globals: poisonedDials=%d", poisonedDials)
	}
}

func TestResolvePublicAddressesRejectsMixedPublicAndPrivateAnswers(t *testing.T) {
	_, err := resolvePublicAddresses(context.Background(), resolverFunc(
		func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
		},
	), "files.rcsb.org")
	if !IsCode(err, CodeNonPublic) {
		t.Fatalf("mixed DNS answer error = %v", err)
	}
}

func TestResolvePublicAddressesPrefersIPv4ForDualStackHosts(t *testing.T) {
	addresses, err := resolvePublicAddresses(context.Background(), resolverFunc(
		func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("2606:2800:220:1:248:1893:25c8:1946"),
				netip.MustParseAddr("93.184.216.34"),
			}, nil
		},
	), "files.rcsb.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 2 || !addresses[0].Is4() || addresses[1].Is4() {
		t.Fatalf("dual-stack dial order = %v", addresses)
	}
}

func TestClientRejectsTLSBypassHooksBeforeDial(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*http.Transport)
	}{
		{name: "DialTLS", configure: func(transport *http.Transport) {
			transport.DialTLS = func(string, string) (net.Conn, error) { return nil, errors.New("unexpected") }
		}},
		{name: "DialTLSContext", configure: func(transport *http.Transport) {
			transport.DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("unexpected")
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			test.configure(transport)
			client := New(Options{
				HTTPClient:                   &http.Client{Transport: transport},
				TestOnlyAllowCustomTransport: true,
			})
			_, err := client.Fetch(context.Background(), "https://files.rcsb.org/download/4TZ4.pdb", Policy{
				AllowedHosts: []string{"files.rcsb.org"}, AcceptedMediaTypes: []string{"chemical/x-pdb"},
				MaxBytes: 1 << 20, Timeout: time.Second,
			})
			if !IsCode(err, CodeInvalidPolicy) {
				t.Fatalf("TLS bypass hook error = %v", err)
			}
		})
	}
}

func TestResolvePublicAddressesRejectsNAT64Encodings(t *testing.T) {
	for _, address := range []string{
		"64:ff9b::7f00:1",
		"64:ff9b::a9fe:a9fe",
		"64:ff9b:1::c0a8:101",
	} {
		t.Run(address, func(t *testing.T) {
			_, err := resolvePublicAddresses(context.Background(), resolverFunc(
				func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr(address)}, nil
				},
			), "files.rcsb.org")
			if !IsCode(err, CodeNonPublic) {
				t.Fatalf("NAT64 address %s error = %v", address, err)
			}
		})
	}
}

func mustPort(t *testing.T, value string) int {
	t.Helper()
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
