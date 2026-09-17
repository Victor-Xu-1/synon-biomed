package articlefulltext

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientFetchesDOIThroughPinnedEuropePMCTransport(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			if query := r.URL.Query().Get("query"); query != `DOI:"10.1000/test.1"` {
				t.Fatalf("query = %q", query)
			}
			if r.URL.Query().Get("resultType") != "core" {
				t.Error("DOI lookup omitted structured metadata/abstract response")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resultList":{"result":[{"pmcid":"PMC12345","doi":"10.1000/test.1","pmid":"12345","title":"Measured source title","abstractText":"Measured endpoints and source conclusions.","authorString":"Source Author","firstPublicationDate":"2025-01-02","journalTitle":"Source Journal"}]}}`))
		case "/PMC12345/fullTextXML":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			_, _ = w.Write([]byte(`<article><body><p>verified full text</p></body></article>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	options, dialed := pinnedTestOptions(t, server, map[string][]netip.Addr{"www.ebi.ac.uk": {netip.MustParseAddr("8.8.8.8")}})
	result, err := NewClient(options).Fetch(context.Background(), Input{DOI: "https://doi.org/10.1000/test.1"})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if result.PMCID != "PMC12345" || result.DOI != "10.1000/test.1" || !strings.Contains(result.Body, "verified full text") {
		t.Fatalf("result = %#v", result)
	}
	if result.Title != "Measured source title" || result.PMID != "12345" ||
		result.AbstractText != "Measured endpoints and source conclusions." ||
		result.AuthorString != "Source Author" || result.PublicationDate != "2025-01-02" ||
		result.JournalTitle != "Source Journal" || !result.RecordAvailable {
		t.Fatalf("full-text handoff discarded resolved source metadata: %#v", result)
	}
	if !result.Available || result.Status != "completed" || !result.Complete ||
		result.EvidenceState != "full-text-read" || result.RecordDepth != "open_access_full_text" || result.BytesRead != len(result.Body) {
		t.Fatalf("availability result = %#v", result)
	}
	for _, address := range dialed() {
		if !strings.HasPrefix(address, "8.8.8.8:") {
			t.Fatalf("transport dialed unpinned address %q", address)
		}
	}
}

func TestClientReturnsStructuredUnavailableForMissingOpenAccessFullText(t *testing.T) {
	t.Run("DOI has no PMCID", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/search" {
				t.Fatalf("unexpected path = %q", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resultList":{"result":[{"doi":"10.1000/closed"}]}}`))
		}))
		defer server.Close()
		options, _ := pinnedTestOptions(t, server, map[string][]netip.Addr{"www.ebi.ac.uk": {netip.MustParseAddr("8.8.8.8")}})
		result, err := NewClient(options).Fetch(context.Background(), Input{DOI: "10.1000/closed"})
		if err != nil || result.Available || result.Status != "not_available" || result.Reason != "no_open_access_full_text" ||
			result.DOI != "10.1000/closed" || !strings.Contains(result.SourceURL, `DOI%3A%2210.1000%2Fclosed%22`) {
			t.Fatalf("result=%#v error=%v", result, err)
		}
	})

	t.Run("DOI without open full text preserves substantive abstract record", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"resultList":{"result":[{"doi":"10.1000/abstract","pmid":"12345678","title":"A measured trial","abstractText":"Methods and measured efficacy results from the randomized study.","authorString":"Doe J et al.","firstPublicationDate":"2025-01-02","journalTitle":"Example Journal"}]}}`))
		}))
		defer server.Close()
		options, _ := pinnedTestOptions(t, server, map[string][]netip.Addr{"www.ebi.ac.uk": {netip.MustParseAddr("8.8.8.8")}})
		result, err := NewClient(options).Fetch(context.Background(), Input{DOI: "10.1000/abstract"})
		if err != nil || result.Available || !result.RecordAvailable || result.Status != "record_available" ||
			result.RecordDepth != "abstract_record" || result.PMID != "12345678" || result.AbstractText == "" {
			t.Fatalf("abstract record result=%#v error=%v", result, err)
		}
	})

	t.Run("PMCID returns 404", func(t *testing.T) {
		server := httptest.NewTLSServer(http.NotFoundHandler())
		defer server.Close()
		options, _ := pinnedTestOptions(t, server, map[string][]netip.Addr{"www.ebi.ac.uk": {netip.MustParseAddr("8.8.4.4")}})
		result, err := NewClient(options).Fetch(context.Background(), Input{PMCID: "PMC404"})
		if err != nil || result.Available || result.Status != "not_available" || result.Reason != "full_text_not_found" || result.StatusCode != http.StatusNotFound {
			t.Fatalf("result=%#v error=%v", result, err)
		}
	})
}

func TestClientFetchesNormalizedPMCIDDirectly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/PMC765/fullTextXML" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(`<article>pmcid body</article>`))
	}))
	defer server.Close()
	options, _ := pinnedTestOptions(t, server, map[string][]netip.Addr{"www.ebi.ac.uk": {netip.MustParseAddr("8.8.4.4")}})
	result, err := NewClient(options).Fetch(context.Background(), Input{PMCID: "pmc765"})
	if err != nil || result.PMCID != "PMC765" || result.Body != `<article>pmcid body</article>` {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestClientRejectsUnsafeIdentifiersAndReservedRedirects(t *testing.T) {
	client := NewClient(Options{})
	for name, input := range map[string]Input{
		"doi injection": {DOI: "10.1000/test?redirect=http://127.0.0.1"},
		"pmcid path":    {PMCID: "PMC1/../../admin"},
		"both ids":      {DOI: "10.1000/test", PMCID: "PMC1"},
		"missing id":    {},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Fetch(context.Background(), input); err == nil {
				t.Fatal("Fetch() should reject unsafe input")
			}
		})
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://redirect.example/fulltext", http.StatusFound)
	}))
	defer server.Close()
	options, _ := pinnedTestOptions(t, server, map[string][]netip.Addr{
		"www.ebi.ac.uk":    {netip.MustParseAddr("8.8.8.8")},
		"redirect.example": {netip.MustParseAddr("192.0.2.10")},
	})
	_, err := NewClient(options).Fetch(context.Background(), Input{PMCID: "PMC1"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "non-public") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestClientRejectsNonHTTPSProductionOrigin(t *testing.T) {
	_, err := NewClient(Options{BaseURL: "http://www.ebi.ac.uk/europepmc/webservices/rest"}).Fetch(context.Background(), Input{PMCID: "PMC1"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "https") {
		t.Fatalf("origin error = %v", err)
	}
	_, err = NewClient(Options{BaseURL: "https://attacker.example/europepmc"}).Fetch(context.Background(), Input{PMCID: "PMC1"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "production origin") {
		t.Fatalf("custom origin error = %v", err)
	}
}

func TestClientRejectsUnexpectedContentAndOversize(t *testing.T) {
	for _, test := range []struct {
		name        string
		contentType string
		body        string
		maxBytes    int64
		want        string
	}{
		{name: "html", contentType: "text/html", body: "<html>login</html>", maxBytes: 32, want: "content type"},
		{name: "large", contentType: "application/xml", body: strings.Repeat("x", 33), maxBytes: 32, want: "exceeds"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			options, _ := pinnedTestOptions(t, server, map[string][]netip.Addr{"www.ebi.ac.uk": {netip.MustParseAddr("1.1.1.1")}})
			options.MaxBytes = test.maxBytes
			_, err := NewClient(options).Fetch(context.Background(), Input{PMCID: "PMC2"})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Fetch() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestClientReturnsStructuredUnavailableForTransientTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte("<article/>"))
	}))
	defer server.Close()
	options, _ := pinnedTestOptions(t, server, map[string][]netip.Addr{
		"www.ebi.ac.uk": {netip.MustParseAddr("1.1.1.1")},
	})
	options.Timeout = 10 * time.Millisecond
	result, err := NewClient(options).Fetch(context.Background(), Input{PMCID: "PMC2"})
	if err != nil {
		t.Fatalf("transient timeout should be a structured source outcome: %v", err)
	}
	if result.Available || !result.SourceUnavailable || !result.Retryable ||
		result.Status != "source_unavailable" || result.Reason != "request_timeout" ||
		result.PMCID != "PMC2" || !strings.Contains(result.SourceURL, "/PMC2/fullTextXML") {
		t.Fatalf("timeout result=%#v", result)
	}
}

func TestClientReturnsStructuredUnavailableForTransientHTTPStatus(t *testing.T) {
	for _, test := range []struct {
		name       string
		input      Input
		statusCode int
		wantReason string
	}{
		{name: "DOI lookup service unavailable", input: Input{DOI: "10.1000/retry"}, statusCode: http.StatusServiceUnavailable, wantReason: "upstream_temporarily_unavailable"},
		{name: "full text rate limited", input: Input{PMCID: "PMC429"}, statusCode: http.StatusTooManyRequests, wantReason: "upstream_rate_limited"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, http.StatusText(test.statusCode), test.statusCode)
			}))
			defer server.Close()
			options, _ := pinnedTestOptions(t, server, map[string][]netip.Addr{
				"www.ebi.ac.uk": {netip.MustParseAddr("1.1.1.1")},
			})
			result, err := NewClient(options).Fetch(context.Background(), test.input)
			if err != nil {
				t.Fatalf("transient HTTP status should be a structured source outcome: %v", err)
			}
			if result.Available || !result.SourceUnavailable || !result.Retryable ||
				result.Status != "source_unavailable" || result.Reason != test.wantReason ||
				result.StatusCode != test.statusCode {
				t.Fatalf("transient HTTP result=%#v", result)
			}
		})
	}
}

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r[host]...), nil
}

func pinnedTestOptions(t *testing.T, server *httptest.Server, addresses staticResolver) (Options, func() []string) {
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
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Synthetic public IP is mapped to this TLS loopback fixture only.
	baseDial := (&net.Dialer{}).DialContext
	var mu sync.Mutex
	dialed := []string{}
	transport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, address)
		mu.Unlock()
		_, requestedPort, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		return baseDial(ctx, network, net.JoinHostPort(serverURL.Hostname(), requestedPort))
	}
	return Options{
			BaseURL:                   "https://www.ebi.ac.uk:" + port,
			HTTPClient:                &http.Client{Transport: transport},
			Resolver:                  addresses,
			TestOnlyAllowCustomOrigin: true,
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), dialed...)
		}
}
