package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestSearchSlowHeadersBeyondFormerBackendBudget(t *testing.T) {
	if os.Getenv("SYNON_TEST_SLOW_NETWORK") != "1" {
		t.Skip("opt-in nine-second real TLS regression")
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(9 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`["cohort analysis",["Cohort analysis"],["Cohort analysis methods"],["https://example.org/cohort-analysis"]]`))
	}))
	defer upstream.Close()
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing-search-helper")
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", upstream.URL)
	started := time.Now()
	output, err := Search(context.Background(), Input{Query: "cohort analysis"}, Options{
		HTTPBackends:   []httpSearchBackend{{Name: "controlled", Format: searchFormatMediaWikiOpenSearch, Endpoint: upstream.URL}},
		BaseHTTPClient: upstream.Client(),
	})
	if err != nil || output.Failure != nil || len(output.Sources) != 1 {
		t.Fatalf("slow headers failed: %#v %v", output.Failure, err)
	}
	t.Logf("default_backend_elapsed=%s sources=%d", time.Since(started), len(output.Sources))
}
