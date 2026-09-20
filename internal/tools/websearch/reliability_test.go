package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSearchTransientBackendRecoversWithoutChangingQuery(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "cohort analysis" {
			t.Errorf("query changed: %s", r.URL.RawQuery)
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "temporary", 503)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<a class="result__a" href="https://example.org/cohort-analysis">Cohort analysis</a><a class="result__snippet">Cohort analysis methods.</a>`))
	}))
	defer server.Close()
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing-search-helper")
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL+"/?q={query}")
	output, err := Search(context.Background(), Input{Query: "cohort analysis"}, Options{
		HTTPBackends: []httpSearchBackend{{Name: "controlled", Endpoint: server.URL + "/search?q={query}"}},
		ClientForURL: testHTTPClientFor(server.Client()),
	})
	if err != nil || output.Failure != nil || len(output.Sources) != 1 || calls.Load() != 2 {
		t.Fatalf("transient backend not recovered: calls=%d failure=%#v error=%v", calls.Load(), output.Failure, err)
	}
}

func TestSearchSlowBodyHasIdleBudgetNotHeaderDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for _, part := range []string{`["cohort analysis",`, `["Cohort analysis"],`, `["Cohort analysis methods"],`, `["https://example.org/cohort-analysis"]]`} {
			_, _ = w.Write([]byte(part))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(35 * time.Millisecond):
			}
		}
	}))
	defer server.Close()
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing-search-helper")
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL)
	output, err := Search(context.Background(), Input{Query: "cohort analysis"}, Options{
		Timeout: time.Second, HeaderTimeout: 60 * time.Millisecond, ReadIdleTimeout: 150 * time.Millisecond,
		HTTPBackends: []httpSearchBackend{{Name: "controlled", Format: searchFormatMediaWikiOpenSearch, Endpoint: server.URL}},
		ClientForURL: testHTTPClientFor(server.Client()),
	})
	if err != nil || output.Failure != nil || len(output.Sources) != 1 {
		t.Fatalf("slow progressing response stopped: %#v %v", output.Failure, err)
	}
}

func TestSearchMixedEmptyAndFailedProvidersRetainsClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/failed" {
			http.Error(w, "blocked", 403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["cohort analysis",[],[],[]]`))
	}))
	defer server.Close()
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing-search-helper")
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL)
	out, err := Search(context.Background(), Input{Query: "cohort analysis"}, Options{
		HTTPBackends: []httpSearchBackend{
			{Name: "empty", Format: searchFormatMediaWikiOpenSearch, Endpoint: server.URL + "/empty"},
			{Name: "failed", Format: searchFormatMediaWikiOpenSearch, Endpoint: server.URL + "/failed"},
		}, ClientForURL: testHTTPClientFor(server.Client()),
	})
	if err != nil || out.Failure == nil || out.Failure.Kind != "search_incomplete" || len(out.Sources) != 0 {
		t.Fatalf("mixed failure=%#v error=%v", out.Failure, err)
	}
	states, _ := out.Diagnostics["httpBackends"].([]map[string]any)
	if len(states) != 2 || states[0]["outcome"] != "empty" || states[1]["outcome"] != "http_error" {
		t.Fatalf("provider causes lost: %#v", states)
	}
}

func TestSearchCompletedEmptyResponseIsNotNetworkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["cohort analysis",[],[],[]]`))
	}))
	defer server.Close()
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing-search-helper")
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL+"/?q={query}")
	options := Options{HTTPBackends: []httpSearchBackend{{Name: "controlled", Format: searchFormatMediaWikiOpenSearch, Endpoint: server.URL + "/?q={query}"}}, ClientForURL: testHTTPClientFor(server.Client())}
	output, err := SearchVariants(context.Background(), VariantInput{Query: "cohort analysis"}, options)
	if err != nil || output.Failure != nil || len(output.Sources) != 0 {
		t.Fatalf("empty response mislabeled unavailable: failure=%#v error=%v", output.Failure, err)
	}
	states, _ := output.Diagnostics["httpBackends"].([]map[string]any)
	if len(states) != 1 || states[0]["outcome"] != "empty" {
		t.Fatalf("empty result provenance missing: %#v", states)
	}
}
