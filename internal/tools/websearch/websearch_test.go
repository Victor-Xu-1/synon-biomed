package websearch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSearchUsesGoHTTPBackendWithoutPythonScript(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("q"), "Synon") {
			t.Fatalf("query was not forwarded to Go search backend: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html><body>
<a class="result__a" href="https://example.com/synon-agent">Synon Agent Runtime</a>
<a class="result__snippet">Go native runtime parity result.</a>
<a class="result__a" href="https://blocked.example/synon">Blocked result</a>
<a class="result__snippet">This result should be filtered.</a>
</body></html>`))
	}))
	defer server.Close()

	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing/websearch.py")
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL+"/search?q={query}")

	out, err := Search(context.Background(), Input{
		Query:          "Synon agent",
		BlockedDomains: []string{"blocked.example"},
		MaxResults:     3,
	}, Options{ClientForURL: testHTTPClientFor(server.Client())})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if out.Failure != nil {
		t.Fatalf("Search should not require Python script when Go HTTP backend is configured: %#v", out.Failure)
	}
	if len(out.Sources) != 1 || out.Sources[0].URL != "https://example.com/synon-agent" || out.Sources[0].Provider != providerName {
		t.Fatalf("Search sources = %#v", out.Sources)
	}
	if out.Diagnostics["backend"] != "go-http" || out.Diagnostics["scriptExists"] != false {
		t.Fatalf("Search diagnostics = %#v", out.Diagnostics)
	}
	if out.Retrieval["returned"] != 1 || out.Retrieval["scope"] != "discovery_window" ||
		out.Retrieval["provider_total_known"] != false {
		t.Fatalf("Search retrieval coverage = %#v", out.Retrieval)
	}
}

func TestSearchRejectsUnrelatedNavigationLinksAsEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body>
<li class="b_algo"><h2><a href="https://support.google.com/accounts/answer/">Google Account Help</a></h2><p>Manage your Google account.</p></li>
<li class="b_algo"><h2><a href="https://support.google.com/websearch/answer/">Google Search Help</a></h2><p>Search settings help.</p></li>
</body></html>`))
	}))
	defer server.Close()

	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing/websearch.py")
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL+"/search?q={query}")
	out, err := Search(context.Background(), Input{
		Query: "MCL1 inhibitor crystal structure PDB BH3 groove drug discovery 2023 2024", MaxResults: 10,
	}, Options{HTTPBackends: []httpSearchBackend{{Name: "bing-html", Endpoint: server.URL + "/search?q={query}"}}, ClientForURL: testHTTPClientFor(server.Client())})
	if err != nil {
		t.Fatalf("Search error = %v", err)
	}
	if out.Failure != nil || len(out.Sources) != 0 {
		t.Fatalf("unrelated navigation links must not become evidence: %#v", out)
	}
	if out.Retrieval["source_unavailable"] == true || out.Retrieval["returned"] != 0 ||
		out.Retrieval["exhaustive"] != false {
		t.Fatalf("unavailable retrieval coverage = %#v", out.Retrieval)
	}
	states, ok := out.Diagnostics["httpBackends"].([]map[string]any)
	if !ok || len(states) != 1 || states[0]["rawResults"] != 2 || states[0]["returnedResults"] != 0 || states[0]["outcome"] != "filtered" {
		t.Fatalf("semantic filtering diagnostics = %#v", out.Diagnostics)
	}
}

func TestSearchRejectsLiveShapedGenericPDBResultsWhenExactIdentifierIsMissing(t *testing.T) {
	for _, query := range []string{
		"PDB 4OQ5 MCL1 inhibitor fragment-based discovery benzoic acid",
		"PDB 9BCG MCL1 inhibitor 2024 J Med Chem Fesik in vivo",
	} {
		t.Run(strings.Fields(query)[1], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(`<!doctype html><html><body>
<li class="b_algo"><h2><a href="https://www.rcsb.org/">RCSB PDB: Homepage</a></h2><p>Explore the Protein Data Bank.</p></li>
<li class="b_algo"><h2><a href="https://www.wwpdb.org/">Worldwide Protein Data Bank</a></h2><p>The global PDB archive.</p></li>
<li class="b_algo"><h2><a href="https://pdb101.rcsb.org/">PDB-101 educational portal</a></h2><p>Learn about protein structures.</p></li>
<li class="b_algo"><h2><a href="https://www.zhihu.com/question/pdb">PDB database tutorial</a></h2><p>Introduction to the PDB database.</p></li>
<li class="b_algo"><h2><a href="https://baike.baidu.com/item/PDB">What is the PDB?</a></h2><p>Generic PDB database overview.</p></li>
<li class="b_algo"><h2><a href="https://pmc.ncbi.nlm.nih.gov/articles/PMC214364/">The Protein Data Bank</a></h2><p>General history of the PDB archive.</p></li>
<li class="b_algo"><h2><a href="https://en.wikipedia.org/wiki/Protein_Data_Bank">Protein Data Bank</a></h2><p>Encyclopedia overview of PDB.</p></li>
<li class="b_algo"><h2><a href="https://www.rcsb.org/search">RCSB PDB search</a></h2><p>Search the structure database.</p></li>
<li class="b_algo"><h2><a href="https://www.wwpdb.org/documentation">PDB archive documentation</a></h2><p>Generic archive documentation.</p></li>
</body></html>`))
			}))
			defer server.Close()

			t.Setenv("SYNON_WEBSEARCH_SCRIPT", "/missing/websearch.py")
			t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL+"/search?q={query}")
			out, err := Search(context.Background(), Input{Query: query, MaxResults: 10}, Options{
				HTTPBackends: []httpSearchBackend{{Name: "bing-html", Endpoint: server.URL + "/search?q={query}"}},
				ClientForURL: testHTTPClientFor(server.Client()),
			})
			if err != nil {
				t.Fatalf("Search error = %v", err)
			}
			if out.Failure != nil || len(out.Sources) != 0 {
				t.Fatalf("generic PDB pages must not satisfy exact-identifier research: %#v", out)
			}
			states := out.Diagnostics["httpBackends"].([]map[string]any)
			if len(states) != 1 || states[0]["rawResults"] != 9 || states[0]["returnedResults"] != 0 {
				t.Fatalf("live-shaped relevance diagnostics = %#v", states)
			}
		})
	}
}

func TestSearchRelevanceRequiresExactIdentifiersOrMultipleDiscriminativeTerms(t *testing.T) {
	if !searchResultRelevant("sotorasib", scriptResult{
		Title: "Sotorasib clinical development", URL: "https://example.org/sotorasib",
	}) {
		t.Fatal("an exact single-term query must retain a result containing that term")
	}
	if searchResultRelevant("sotorasib", scriptResult{
		Title: "Unrelated kinase structure", URL: "https://example.org/other",
	}) {
		t.Fatal("a single-term query must not admit a result without that term")
	}
	if !searchResultRelevant("PDB 4OQ5 MCL1 inhibitor", scriptResult{
		Title: "MCL-1 inhibitor complex", URL: "https://www.rcsb.org/structure/4OQ5",
	}) {
		t.Fatal("result URL containing the exact PDB identifier should be relevant")
	}
	if searchResultRelevant("PDB 4OQ5 MCL1 inhibitor", scriptResult{
		Title: "MCL-1 inhibitor discovery", URL: "https://www.rcsb.org/",
	}) {
		t.Fatal("other matching terms must not substitute for a missing exact PDB identifier")
	}
	if !searchResultRelevant("MCL1 inhibitor fragment discovery", scriptResult{
		Title: "Fragment discovery of MCL-1 inhibitors", URL: "https://example.org/paper",
	}) {
		t.Fatal("multiple discriminative terms should admit a relevant result")
	}
	if searchResultRelevant("MCL1 inhibitor fragment discovery", scriptResult{
		Title: "General inhibitor database", URL: "https://example.org/database",
	}) {
		t.Fatal("one generic overlap must not admit a result")
	}
	if !searchResultRelevant("MCL1 inhibitor", scriptResult{
		Title: "Selective MCL-1 inhibitors", URL: "https://example.org/paper",
	}) {
		t.Fatal("simple singular/plural variation must preserve two discriminative matches")
	}
	if searchResultRelevant("LRRK2 degrader PROTAC clinical trial Parkinson 2024 2025", scriptResult{
		Title: "Discovery of the First-in-Class G9a/GLP PROTAC Degrader", URL: "https://example.org/g9a",
	}) {
		t.Fatal("generic PROTAC overlap must not replace the explicit alphanumeric target identifier")
	}
	if !searchResultRelevant("LRRK2 degrader PROTAC clinical trial Parkinson 2024 2025", scriptResult{
		Title: "First-in-human study of an LRRK2 PROTAC degrader", URL: "https://example.org/lrrk2",
	}) {
		t.Fatal("result containing the explicit alphanumeric target identifier should be relevant")
	}
}

func TestSearchRelevanceBridgesMultilingualScientificTermsWithoutAdmittingUnrelatedResults(t *testing.T) {
	if !searchResultRelevant("万古霉素 成人严重MRSA感染 个体化给药", scriptResult{
		Title:   "Therapeutic monitoring of vancomycin in adults",
		URL:     "https://pubmed.ncbi.nlm.nih.gov/32658968/",
		Snippet: "Consensus guidance for individualized AUC-guided dosing.",
	}) {
		t.Fatal("cross-language scientific aliases should retain a relevant English source")
	}
	if searchResultRelevant("近五年口服肽类药物吸收促进剂与脂质纳米载体", scriptResult{
		Title: "Five Hundred Miles lyrics", URL: "https://example.org/unrelated",
		Snippet: "A general music question with no pharmaceutical evidence.",
	}) {
		t.Fatal("non-Latin input must not make every provider result relevant")
	}
	if !searchResultRelevant("近五年口服肽类药物吸收促进剂与脂质纳米载体", scriptResult{
		Title:   "Lipid nanoparticles for oral peptide delivery",
		URL:     "https://doi.org/10.1000/relevant",
		Snippet: "Absorption and permeation enhancement for peptide therapeutics.",
	}) {
		t.Fatal("cross-language scientific aliases should retain a relevant record")
	}
}

func TestSearchPrefersGoHTTPBackendWhenPythonScriptExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
<a class="result__a" href="https://example.com/native">Native backend Go result</a>
<a class="result__snippet">No Python runtime was invoked.</a>
</body></html>`))
	}))
	defer server.Close()

	root := t.TempDir()
	script := filepath.Join(root, "tools", "websearch.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(script, []byte("raise SystemExit('python backend must not run')\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", script)
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", server.URL+"/search?q={query}")
	t.Setenv("SYNON_WEBSEARCH_PYTHON", "/definitely/missing/python")

	out, err := Search(context.Background(), Input{Query: "native backend", MaxResults: 1}, Options{
		Root: root, ClientForURL: testHTTPClientFor(server.Client()),
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if out.Failure != nil || len(out.Sources) != 1 || out.Sources[0].URL != "https://example.com/native" {
		t.Fatalf("Search() output = %#v", out)
	}
	if out.Diagnostics["backend"] != "go-http" || out.Diagnostics["scriptExists"] != true {
		t.Fatalf("Search() diagnostics = %#v", out.Diagnostics)
	}
}

func TestGoHTTPBackendIsEnabledByDefaultWithExistingScript(t *testing.T) {
	t.Setenv("SYNON_WEBSEARCH_ENDPOINT", "")
	t.Setenv("SYNON_WEBSEARCH_GO", "")
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "")

	script := filepath.Join(t.TempDir(), "websearch.py")
	if err := os.WriteFile(script, []byte("# optional fallback\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if !shouldTryGoHTTPBackend(script) {
		t.Fatal("Go HTTP backend must be enabled by default even when an optional Python script exists")
	}
}

func TestResolveScriptPathRequiresExplicitConfiguration(t *testing.T) {
	root := t.TempDir()
	implicit := filepath.Join(root, "tools", "websearch.py")
	if err := os.MkdirAll(filepath.Dir(implicit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(implicit, []byte("# implicit helper\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", "")
	if got := resolveScriptPath(root); got != "" {
		t.Fatalf("implicit script path = %q", got)
	}
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", implicit)
	if got := resolveScriptPath(root); got != implicit {
		t.Fatalf("explicit script path = %q, want %q", got, implicit)
	}
}

func TestDefaultHTTPBackendsPreferBingBeforeFallbackProviders(t *testing.T) {
	if len(defaultHTTPBackends) == 0 || defaultHTTPBackends[0].Name != "bing-html" {
		t.Fatalf("default search backend order = %#v", defaultHTTPBackends)
	}
}

func TestSearchReportsItsTotalTimeoutWithoutLeakingProcessKillDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<a href="https://example.org/unrelated">Unrelated navigation page</a>`))
	}))
	defer server.Close()

	root := t.TempDir()
	script := filepath.Join(root, "tools", "websearch.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("import time\ntime.sleep(5)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", script)
	started := time.Now()
	out, err := Search(context.Background(), Input{Query: "bounded biomedical search", MaxResults: 3}, Options{
		Root: root, Timeout: 100 * time.Millisecond,
		HTTPBackends: []httpSearchBackend{{Name: "empty", Endpoint: server.URL + "/?q={query}"}},
		ClientForURL: testHTTPClientFor(server.Client()),
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("total timeout was not enforced: %s", elapsed)
	}
	if out.Failure == nil || !strings.Contains(out.Failure.Message, "timed out") {
		t.Fatalf("timeout failure = %#v", out.Failure)
	}
	if out.Diagnostics["timedOut"] != true || out.Diagnostics["error"] != "websearch total time budget expired" {
		t.Fatalf("timeout diagnostics = %#v", out.Diagnostics)
	}
	if strings.Contains(strings.ToLower(out.Failure.Message+fmt.Sprint(out.Diagnostics)), "signal: killed") {
		t.Fatalf("process implementation detail leaked to the model: %#v", out)
	}
}

func TestSearchPropagatesParentCancellation(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "tools", "websearch.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("import time\ntime.sleep(5)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_WEBSEARCH_SCRIPT", script)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Search(ctx, Input{Query: "cancelled biomedical search"}, Options{Root: root}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search() error = %v, want context.Canceled", err)
	}
}

func TestDefaultCrossrefBackendExcludesSupplementaryComponents(t *testing.T) {
	for _, backend := range defaultHTTPBackends {
		if backend.Name != "crossref" {
			continue
		}
		if !strings.Contains(backend.Endpoint, "filter=type:journal-article") {
			t.Fatalf("Crossref endpoint does not exclude supplementary component records: %s", backend.Endpoint)
		}
		return
	}
	t.Fatal("default Crossref backend is missing")
}

func TestDefaultEuropePMCBackendProvidesBroadBiomedicalDiscovery(t *testing.T) {
	for _, backend := range defaultHTTPBackends {
		if backend.Name != "europe-pmc" {
			continue
		}
		if backend.Format != searchFormatEuropePMC || !strings.Contains(backend.Endpoint, "pageSize={limit}") ||
			!strings.Contains(backend.Endpoint, "resultType=core") {
			t.Fatalf("Europe PMC backend = %#v", backend)
		}
		return
	}
	t.Fatal("default Europe PMC backend is missing")
}

func TestOptionalPythonHelperStartsWithoutOptionalSearchPackages(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "optional", "websearch.py"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-S", script, "--print-backends")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dependency-free helper startup failed: %v\n%s", err, output)
	}
	if value := string(output); !strings.Contains(value, "wikipedia") || !strings.Contains(value, "duckduckgo") {
		t.Fatalf("backend plan = %q", value)
	}
}

func TestSearchUsesSurvivingHTTPBackendsAndDeduplicatesResults(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<a href="https://example.org/shared">Resilient runtime search shared result</a>`))
	}))
	defer primary.Close()
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<a href="https://example.org/shared">Resilient runtime search shared duplicate</a><a href="https://example.org/second">Resilient runtime search second result</a>`))
	}))
	defer secondary.Close()

	out, err := Search(context.Background(), Input{Query: "resilient runtime search", MaxResults: 4}, Options{
		HTTPBackends: []httpSearchBackend{
			{Name: "failing", Endpoint: failing.URL + "?q={query}"},
			{Name: "primary", Endpoint: primary.URL + "?q={query}"},
			{Name: "secondary", Endpoint: secondary.URL + "?q={query}"},
		},
		ClientForURL: testHTTPClientFor(http.DefaultClient),
	})
	if err != nil || out.Failure != nil {
		t.Fatalf("Search() output=%#v error=%v", out, err)
	}
	if len(out.Sources) != 2 || out.Sources[0].URL != "https://example.org/shared" || out.Sources[1].URL != "https://example.org/second" {
		t.Fatalf("deduplicated sources = %#v", out.Sources)
	}
	states, ok := out.Diagnostics["httpBackends"].([]map[string]any)
	if !ok || len(states) != 3 || states[0]["status"] != http.StatusServiceUnavailable || states[1]["returnedResults"] != 1 {
		t.Fatalf("backend diagnostics = %#v", out.Diagnostics["httpBackends"])
	}
}

func TestSearchInterleavesSurvivingHTTPBackendResults(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`
			<a href="https://primary.example/one">KRAS degradation primary one</a>
			<a href="https://primary.example/two">KRAS degradation primary two</a>
			<a href="https://primary.example/three">KRAS degradation primary three</a>`))
	}))
	defer primary.Close()
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<a href="https://secondary.example/one">KRAS degradation independent source</a>`))
	}))
	defer secondary.Close()

	out, err := Search(context.Background(), Input{Query: "KRAS degradation", MaxResults: 3}, Options{
		HTTPBackends: []httpSearchBackend{
			{Name: "primary", Endpoint: primary.URL + "?q={query}"},
			{Name: "secondary", Endpoint: secondary.URL + "?q={query}"},
		},
		ClientForURL: testHTTPClientFor(http.DefaultClient),
	})
	if err != nil || out.Failure != nil {
		t.Fatalf("Search() output=%#v error=%v", out, err)
	}
	if len(out.Sources) != 3 || out.Sources[0].URL != "https://primary.example/one" || out.Sources[1].URL != "https://secondary.example/one" || out.Sources[2].URL != "https://primary.example/two" {
		t.Fatalf("backend-balanced sources = %#v", out.Sources)
	}
}

func TestRankHTTPBackendHitsPromotesBroaderQueryCoverage(t *testing.T) {
	hits := []Hit{
		{Title: "OpenAI · GitHub", URL: "https://github.com/openai/"},
		{Title: "Student software development through GitHub repository activity", URL: "https://doi.org/10.1000/example"},
		{Title: "GitHub - openai/codex: Lightweight coding agent", URL: "https://github.com/openai/codex"},
	}
	ranked := rankHTTPBackendHits("OpenAI Codex GitHub repository", hits)
	if ranked[0].URL != "https://github.com/openai/codex" {
		t.Fatalf("relevance-ranked hits=%#v", ranked)
	}
	if searchHitRelevanceScore("OpenAI Codex GitHub repository", ranked[0]) <=
		searchHitRelevanceScore("OpenAI Codex GitHub repository", ranked[1]) {
		t.Fatalf("top relevance score did not exceed runner-up: %#v", ranked)
	}
}

func TestRankHTTPBackendHitsPreservesProviderOrderForEqualScores(t *testing.T) {
	hits := []Hit{
		{Title: "KRAS degradation primary one", URL: "https://primary.example/one"},
		{Title: "KRAS degradation independent source", URL: "https://secondary.example/one"},
		{Title: "KRAS degradation primary two", URL: "https://primary.example/two"},
	}
	ranked := rankHTTPBackendHits("KRAS degradation", hits)
	for index := range hits {
		if ranked[index].URL != hits[index].URL {
			t.Fatalf("equal-score provider order changed: %#v", ranked)
		}
	}
}

func TestRankHTTPBackendHitsUsesSourceQualityAsRelevanceTieBreaker(t *testing.T) {
	hits := []Hit{
		{Title: "Vancomycin dosing guideline", URL: "https://example.com/vancomycin", Snippet: "Clinical dosing guidance."},
		{Title: "Vancomycin dosing guideline", URL: "https://guidance.example.gov/vancomycin", Snippet: "Clinical dosing guidance."},
	}
	ranked := rankHTTPBackendHits("vancomycin dosing guideline", hits)
	if ranked[0].URL != "https://guidance.example.gov/vancomycin" {
		t.Fatalf("quality-ranked hits=%#v", ranked)
	}
}

func TestNormalizeMaxResultsSupportsBroadDiscovery(t *testing.T) {
	for _, test := range []struct {
		input int
		want  int
	}{
		{input: 0, want: 50},
		{input: 20, want: 20},
		{input: 100, want: 100},
		{input: 101, want: 101},
		{input: 201, want: 200},
	} {
		if got := normalizeMaxResults(test.input); got != test.want {
			t.Fatalf("normalizeMaxResults(%d)=%d want %d", test.input, got, test.want)
		}
	}
}

func TestWebSearchCandidatePoolIsBroaderThanNarrowPresentation(t *testing.T) {
	for _, test := range []struct {
		returned int
		want     int
	}{
		{returned: 5, want: 50},
		{returned: 50, want: 50},
		{returned: 120, want: 120},
		{returned: 500, want: 200},
	} {
		if got := webSearchCandidatePoolLimit(test.returned); got != test.want {
			t.Fatalf("returned=%d candidate pool=%d want=%d", test.returned, got, test.want)
		}
	}
}

func TestSearchHTTPEndpointForLimitExpandsProviderBudget(t *testing.T) {
	target := searchHTTPEndpointForLimit(
		"weak base salt screening",
		"https://example.test/search?q={query}&count={limit}",
		80,
	)
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	if parsed.Query().Get("q") != "weak base salt screening" || parsed.Query().Get("count") != "80" {
		t.Fatalf("expanded endpoint=%q", target)
	}
	if got := httpBackendRequestLimit(httpSearchBackend{Name: "bing-html"}, 100); got != 50 {
		t.Fatalf("Bing request limit=%d want 50", got)
	}
	if got := httpBackendRequestLimit(httpSearchBackend{Name: "crossref"}, 100); got != 100 {
		t.Fatalf("Crossref request limit=%d want 100", got)
	}
}

func testHTTPClientFor(client *http.Client) func(context.Context, string) (*http.Client, error) {
	return func(context.Context, string) (*http.Client, error) { return client, nil }
}

func TestParseMediaWikiOpenSearchResults(t *testing.T) {
	body := []byte(`["sickle cell base editing",["Base editing","Gene therapy for sickle cell disease"],["A precise genome editing technique","Overview of gene therapies"],["https://en.wikipedia.org/wiki/Base_editing","https://en.wikipedia.org/wiki/Gene_therapy_for_sickle_cell_disease"]]`)
	results, err := parseMediaWikiOpenSearchResults(body)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if len(results) != 2 || results[0].Title != "Base editing" || results[0].URL != "https://en.wikipedia.org/wiki/Base_editing" || results[1].Snippet != "Overview of gene therapies" {
		t.Fatalf("parsed results = %#v", results)
	}
}

func TestParseCrossrefResultsSkipsSupplementaryComponentDOIs(t *testing.T) {
	body := []byte(`{"message":{"items":[
		{"DOI":"10.1021/example.s001","title":["Supplement file one"],"URL":"https://doi.org/10.1021/example.s001"},
		{"DOI":"10.1021/example.s002","title":["Supplement file two"],"URL":"https://doi.org/10.1021/example.s002"},
		{"DOI":"10.1021/example","title":["KRAS degradation primary article"],"URL":"https://doi.org/10.1021/example"}
	]}}`)
	results, err := parseCrossrefResults(body)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://doi.org/10.1021/example" {
		t.Fatalf("Crossref primary results = %#v", results)
	}
}

func TestParseCrossrefResults(t *testing.T) {
	body := []byte(`{"status":"ok","message":{"items":[
		{"DOI":"10.1000/example","title":["Base editing for sickle cell disease"],"URL":"https://doi.org/10.1000/example","abstract":"<jats:p>Direct repair of the sickle allele.</jats:p>"},
		{"DOI":"10.1000/empty-title","title":[],"URL":"https://doi.org/10.1000/empty-title"}
	]}}`)
	results, err := parseCrossrefResults(body)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if len(results) != 1 || results[0].Title != "Base editing for sickle cell disease" || results[0].URL != "https://doi.org/10.1000/example" || !strings.Contains(results[0].Snippet, "Direct repair") {
		t.Fatalf("parsed results = %#v", results)
	}
}

func TestParseEuropePMCResultsPreservesStablePublicationLinksAndEvidenceText(t *testing.T) {
	body := []byte(`{"resultList":{"result":[
		{"title":"Amorphous solid dispersions for weak bases","authorString":"Li A et al.","journalTitle":"Int J Pharm","pubYear":"2024","doi":"10.1000/asd.2024","pmid":"123","abstractText":"A broad comparison of exposure."},
		{"title":"Lipid formulation study","authorString":"Wang B et al.","journalTitle":"Eur J Pharm Sci","pubYear":"2023","pmid":"456"},
		{"title":"No stable record","source":"","id":""}
	]}}`)
	results, err := parseEuropePMCResults(body)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if len(results) != 2 || !strings.Contains(results[0].URL, "doi.org/10.1000") ||
		!strings.Contains(results[0].Snippet, "Li A et al.") ||
		results[1].URL != "https://europepmc.org/article/MED/456" {
		t.Fatalf("Europe PMC results = %#v", results)
	}
}

func TestParseHTTPBackendResultsDispatchesByFormat(t *testing.T) {
	html, err := parseHTTPBackendResults(httpSearchBackend{Name: "mojeek", Format: searchFormatHTML}, []byte(`<a href="https://example.com/x">Mojeek title</a>`))
	if err != nil || len(html) != 1 || html[0].Title != "Mojeek title" {
		t.Fatalf("html results = %#v err=%v", html, err)
	}
	if _, err := parseHTTPBackendResults(httpSearchBackend{Name: "unknown", Format: "unsupported"}, []byte(`{}`)); err == nil {
		t.Fatal("unsupported format must return an error")
	}
}

func TestParseBingHTMLResultsDecodesExternalRedirectBeforeDomainFiltering(t *testing.T) {
	body := []byte(`<li class="b_algo"><h2><a href="https://www.bing.com/ck/a?!&amp;&amp;u=a1aHR0cHM6Ly93d3cuZmRhLmdvdi9yZWd1bGF0b3J5LWluZm9ybWF0aW9uL3NlYXJjaC1mZGEtZ3VpZGFuY2UtZG9jdW1lbnRz&amp;ntb=1">Search FDA Guidance Documents</a></h2><p>Official FDA guidance search.</p></li>`)
	raw, err := parseHTTPBackendResults(httpSearchBackend{Name: "bing-html", Format: searchFormatHTML}, body)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	hits := selectRelevantHits(raw, Input{
		Query:          "FDA guidance documents",
		BlockedDomains: []string{"bing.com"},
	}, 5)
	if len(hits) != 1 || hits[0].URL != "https://www.fda.gov/regulatory-information/search-fda-guidance-documents" {
		t.Fatalf("decoded Bing result = %#v", hits)
	}
}

func TestParseDuckDuckGoLiteDecodesProtocolRelativeRedirectBeforeDomainFiltering(t *testing.T) {
	body := []byte(`<a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fclinicaltrials.gov%2Fstudy%2FNCT03600883&amp;rut=digest" class="result-link">Study Details | NCT03600883 - ClinicalTrials.gov</a>`)
	raw, err := parseHTTPBackendResults(httpSearchBackend{Name: "duckduckgo-lite", Format: searchFormatHTML}, body)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	hits := selectRelevantHits(raw, Input{
		Query:          "NCT03600883 ClinicalTrials",
		BlockedDomains: []string{"duckduckgo.com"},
	}, 5)
	if len(hits) != 1 || hits[0].URL != "https://clinicaltrials.gov/study/NCT03600883" {
		t.Fatalf("decoded DuckDuckGo Lite result = %#v", hits)
	}
}
