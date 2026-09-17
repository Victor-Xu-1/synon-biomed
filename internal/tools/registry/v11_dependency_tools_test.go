package registry

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"synon-go/internal/tools/articlefulltext"
)

func TestV11DependencyAliasesAreRegisteredAndExecutable(t *testing.T) {
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<a class="result__a" href="https://example.org/paper">Paper</a>`))
	}))
	defer searchServer.Close()
	articleServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<article>registry full text</article>`))
	}))
	defer articleServer.Close()

	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", searchServer.URL+"/search-web?q={query}")
	reg := DefaultWithArticleFulltextClient(articlefulltext.NewClient(registryPinnedArticleOptions(t, articleServer)))
	for _, name := range []string{"web_search", "fetch_article_fulltext"} {
		tool, ok := reg.Get(name)
		if !ok || !tool.Executable {
			t.Fatalf("tool %q = %#v, present=%v", name, tool, ok)
		}
	}
	search, err := reg.Execute(context.Background(), "web_search", map[string]any{"query": "biomedical evidence"})
	if err != nil || search == nil {
		t.Fatalf("web_search Execute() result=%#v error=%v", search, err)
	}
	fulltext, err := reg.Execute(context.Background(), "fetch_article_fulltext", map[string]any{"pmcid": "PMC42"})
	if err != nil || fulltext == nil {
		t.Fatalf("fetch_article_fulltext Execute() result=%#v error=%v", fulltext, err)
	}
}

type registryArticleResolver struct{}

func (registryArticleResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

func registryPinnedArticleOptions(t *testing.T, server *httptest.Server) articlefulltext.Options {
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
	transport.TLSClientConfig.InsecureSkipVerify = true // Synthetic public IP is mapped to this TLS fixture only.
	transport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		_, requestedPort, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(parsed.Hostname(), requestedPort))
	}
	return articlefulltext.Options{
		BaseURL: "https://www.ebi.ac.uk:" + port, HTTPClient: &http.Client{Transport: transport},
		Resolver: registryArticleResolver{}, TestOnlyAllowCustomOrigin: true,
	}
}
