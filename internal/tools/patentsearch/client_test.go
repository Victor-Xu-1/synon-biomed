package patentsearch

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/tools/websearch"
)

func TestSearchRunsQueryVariantsConcurrentlyAndKeepsQueryOrder(t *testing.T) {
	t.Parallel()

	var started atomic.Int32
	release := make(chan struct{})
	client := NewClient(Options{Search: func(_ context.Context, input websearch.Input, _ websearch.Options) (websearch.Output, error) {
		if started.Add(1) == 2 {
			close(release)
		}
		select {
		case <-release:
		case <-time.After(2 * time.Second):
			return websearch.Output{}, errors.New("query variants did not run concurrently")
		}
		return websearch.Output{Sources: []websearch.Evidence{{
			URL:   "https://patentscope.wipo.int/result/" + url.QueryEscape(input.Query),
			Title: input.Query, EvidenceState: "discovered",
		}}}, nil
	}})
	output, err := client.Run(context.Background(), Input{
		Operation: OperationSearch, Query: "first query", QueryVariants: []string{"second query"},
		Sources: []string{"wipo"}, MaxResults: 10,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if started.Load() != 2 || len(output.Records) != 2 || output.Records[0].Title != "first query" || output.Records[1].Title != "second query" {
		t.Fatalf("concurrent ordered output=%#v calls=%d", output.Records, started.Load())
	}
}

func TestSearchUsesEveryRequestedPatentSourceAndKeepsProvenance(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	calledDomains := make(map[string]int)
	client := NewClient(Options{
		Search: func(_ context.Context, input websearch.Input, _ websearch.Options) (websearch.Output, error) {
			if len(input.AllowedDomains) == 0 {
				return websearch.Output{}, errors.New("patent search must constrain every discovery query to a source domain")
			}
			domain := input.AllowedDomains[0]
			mu.Lock()
			calledDomains[domain]++
			mu.Unlock()
			return websearch.Output{Sources: []websearch.Evidence{{
				URL:           "https://" + domain + "/result",
				Title:         "Electric lamp patent",
				Snippet:       "A source-specific result",
				EvidenceState: "discovery",
			}}}, nil
		},
		FetchGoogleJSON: func(_ context.Context, target string) ([]byte, error) {
			if !strings.HasPrefix(target, "https://patents.google.com/xhr/query?") {
				t.Fatalf("Google Patents search target = %q", target)
			}
			mu.Lock()
			calledDomains["patents.google.com"]++
			mu.Unlock()
			return []byte(`{"results":{"total_num_results":1,"cluster":[{"result":[{"patent":{"title":"Electric lamp patent","snippet":"A source-specific result","publication_number":"US223898A"}}]}]}}`), nil
		},
	})

	output, err := client.Run(context.Background(), Input{
		Operation:  OperationSearch,
		Query:      "electric lamp filament",
		Sources:    []string{"wipo", "google", "epo", "cnipa"},
		MaxResults: 8,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := len(calledDomains), 4; got != want {
		t.Fatalf("searched source domains = %d, want %d (%v)", got, want, calledDomains)
	}
	if got, want := len(output.Records), 4; got != want {
		t.Fatalf("records = %d, want %d: %#v", got, want, output.Records)
	}
	if got, want := len(output.SourceStatuses), 4; got != want {
		t.Fatalf("source statuses = %d, want %d", got, want)
	}
	seen := map[string]bool{}
	for _, record := range output.Records {
		seen[record.Source] = true
		if record.URL == "" || record.Title == "" {
			t.Fatalf("record lost source evidence: %#v", record)
		}
	}
	for _, source := range []string{"wipo", "google_patents", "epo", "cnipa"} {
		if !seen[source] {
			t.Fatalf("missing %s record in %#v", source, output.Records)
		}
	}
}

func TestGooglePatentSearchParsesBoundedStructuredResults(t *testing.T) {
	t.Parallel()

	client := NewClient(Options{
		Search: func(context.Context, websearch.Input, websearch.Options) (websearch.Output, error) {
			t.Fatal("Google Patents must use its source-specific structured endpoint")
			return websearch.Output{}, nil
		},
		FetchGoogleJSON: func(_ context.Context, target string) ([]byte, error) {
			parsed, err := url.Parse(target)
			if err != nil {
				t.Fatalf("parse target: %v", err)
			}
			if parsed.Scheme != "https" || parsed.Host != "patents.google.com" || parsed.Path != "/xhr/query" {
				t.Fatalf("target = %q", target)
			}
			return []byte(`{"results":{"total_num_results":2,"cluster":[{"result":[` +
				`{"patent":{"title":"<b>Electric</b> lamp","snippet":"  Edison &amp; filament ","publication_number":"US 223898 A"}},` +
				`{"patent":{"title":"Duplicate","publication_number":"US223898A"}}]}]}}`), nil
		},
	})
	output, err := client.Run(context.Background(), Input{
		Operation: OperationSearch, Query: "Thomas Edison electric lamp", Sources: []string{"google"}, MaxResults: 5,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(output.Records) != 1 {
		t.Fatalf("records = %#v", output.Records)
	}
	record := output.Records[0]
	if record.PublicationNumber != "US223898A" || record.Title != "Electric lamp" || record.Snippet != "Edison & filament" {
		t.Fatalf("record = %#v", record)
	}
	if record.URL != "https://patents.google.com/patent/US223898A/en" ||
		record.EvidenceState != "discovered" || record.RecordDepth != "locator_only" {
		t.Fatalf("record provenance = %#v", record)
	}
	if output.EvidenceDepth != "discovery_only" || !strings.Contains(output.NextAction, "operation=lookup") {
		t.Fatalf("search depth guidance = %#v", output)
	}
	if len(output.SourceStatuses) != 1 || output.SourceStatuses[0].Status != "available" {
		t.Fatalf("statuses = %#v", output.SourceStatuses)
	}
}

func TestPatentSearchMergesLanguageVariantsWithTruthfulCoverage(t *testing.T) {
	t.Parallel()

	var queries []string
	var mu sync.Mutex
	client := NewClient(Options{Search: func(_ context.Context, input websearch.Input, _ websearch.Options) (websearch.Output, error) {
		mu.Lock()
		queries = append(queries, input.Query)
		mu.Unlock()
		suffix := "cn"
		if strings.Contains(input.Query, "oral peptide") {
			suffix = "en"
		}
		return websearch.Output{Sources: []websearch.Evidence{
			{URL: "https://patentscope.wipo.int/result/" + suffix, Title: suffix},
			{URL: "https://patentscope.wipo.int/result/shared", Title: "shared"},
		}}, nil
	}})
	output, err := client.Run(context.Background(), Input{
		Operation: OperationSearch, Query: "口服肽递送", QueryVariants: []string{"oral peptide delivery"},
		Sources: []string{"wipo"}, MaxResults: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || len(output.Records) != 2 {
		t.Fatalf("queries=%#v records=%#v", queries, output.Records)
	}
	if output.Records[0].Title != "cn" || output.Records[1].Title != "en" {
		t.Fatalf("bilingual merge records=%#v", output.Records)
	}
	if output.Retrieval["query_variants"] != 2 || output.Retrieval["candidates"] != 4 ||
		output.Retrieval["returned"] != 2 || output.Retrieval["duplicates"] != 1 ||
		output.Retrieval["truncated"] != true || !output.LimitReached {
		t.Fatalf("retrieval=%#v limitReached=%t", output.Retrieval, output.LimitReached)
	}
}

func TestPatentSearchAutomaticallyAddsCrossLanguageDiscoveryVariant(t *testing.T) {
	queries := normalizedPatentSearchQueries("口服肽递送", nil)
	if len(queries) != 2 || queries[0] != "口服肽递送" || !strings.Contains(queries[1], "oral") ||
		!strings.Contains(queries[1], "peptide") || !strings.Contains(queries[1], "delivery") {
		t.Fatalf("queries = %#v", queries)
	}
}

func TestPatentSearchKeepsCrossLanguageVariantWithCallerSynonyms(t *testing.T) {
	queries := normalizedPatentSearchQueries("口服小分子专利", []string{"口服药物专利"})
	if len(queries) < 3 || queries[0] != "口服小分子专利" ||
		!strings.Contains(queries[1], "口服药物专利") ||
		!strings.Contains(queries[len(queries)-1], "oral") || !strings.Contains(queries[len(queries)-1], "patent") {
		t.Fatalf("queries=%#v", queries)
	}
}

func TestSearchReportsSourceFailureInsteadOfClaimingConfigured(t *testing.T) {
	t.Parallel()

	client := NewClient(Options{
		FetchGoogleJSON: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("rate limited")
		},
		Search: func(context.Context, websearch.Input, websearch.Options) (websearch.Output, error) {
			return websearch.Output{}, errors.New("fallback unavailable")
		},
	})
	output, err := client.Run(context.Background(), Input{
		Operation: OperationSearch, Query: "electric lamp", Sources: []string{"google"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(output.Records) != 0 || len(output.Warnings) != 1 {
		t.Fatalf("output = %#v", output)
	}
	if len(output.SourceStatuses) != 1 || output.SourceStatuses[0].Status != "unavailable" ||
		!strings.Contains(output.SourceStatuses[0].Note, "rate limited") {
		t.Fatalf("statuses = %#v", output.SourceStatuses)
	}
}

func TestGooglePatentSearchFallsBackWithinSameSourceAdapter(t *testing.T) {
	t.Parallel()

	client := NewClient(Options{
		FetchGoogleJSON: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("structured endpoint timed out")
		},
		Search: func(_ context.Context, input websearch.Input, _ websearch.Options) (websearch.Output, error) {
			if len(input.AllowedDomains) != 1 || input.AllowedDomains[0] != "patents.google.com" {
				t.Fatalf("fallback escaped Google Patents domain: %#v", input.AllowedDomains)
			}
			return websearch.Output{Sources: []websearch.Evidence{{
				URL: "https://patents.google.com/patent/US223898A/en", Title: "Electric lamp",
			}}}, nil
		},
	})
	output, err := client.Run(context.Background(), Input{
		Operation: OperationSearch, Query: "electric lamp", Sources: []string{"google"}, MaxResults: 5,
	})
	if err != nil || len(output.Records) != 1 || output.SourceStatuses[0].Status != "available" || len(output.Warnings) != 0 {
		t.Fatalf("fallback output=%#v err=%v", output, err)
	}
}

func TestLookupNormalizesPublicationNumberAndBuildsSourceLinks(t *testing.T) {
	t.Parallel()

	client := NewClient(Options{FetchHTML: func(_ context.Context, target string) (string, error) {
		if target != "https://patents.google.com/patent/WO2024123456A1/en" {
			t.Fatalf("lookup target = %q", target)
		}
		return `<article>
			<span itemprop="title">Oral peptide formulation</span>
			<dd itemprop="inventor">Ada Example</dd>
			<dd itemprop="assigneeCurrent">Example Pharma</dd>
			<time itemprop="priorityDate" datetime="2023-01-02"></time>
			<time itemprop="publicationDate" datetime="2024-07-04"></time>
			<span itemprop="status">Pending</span>
			<section itemprop="abstract"><div itemprop="content">An oral peptide formulation using an absorption enhancer.</div></section>
			<section itemprop="description">
				<div id="p0001" class="description-paragraph">Technical field</div>
				<div id="p0002" class="description-paragraph">The absorption enhancer is combined with a peptide.</div>
				<div id="p0020" class="description-paragraph">Example 1</div>
				<div id="p0021" class="description-paragraph">Dogs received the formulation and exposure increased.</div>
			</section>
			<section itemprop="claims">
				<claim num="1"><div class="claim-text">1. An oral peptide composition comprising an absorption enhancer.</div></claim>
				<claim num="2"><div class="claim-text">2. The composition according to claim 1, wherein the peptide is insulin.</div></claim>
			</section>
		</article>`, nil
	}})
	output, err := client.Run(context.Background(), Input{
		Operation:         OperationLookup,
		PublicationNumber: "WO 2024/123456 A1",
		FocusTerms:        []string{"absorption enhancer"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := output.PublicationNumber, "WO2024123456A1"; got != want {
		t.Fatalf("publication number = %q, want %q", got, want)
	}
	if got, want := len(output.Records), 4; got != want {
		t.Fatalf("records = %d, want %d", got, want)
	}
	for _, record := range output.Records {
		if record.PublicationNumber != "WO2024123456A1" || record.URL == "" {
			t.Fatalf("%s lookup record = %#v", record.Source, record)
		}
		if record.Source != "cnipa" && !strings.Contains(record.URL, "WO2024123456A1") {
			t.Fatalf("%s lookup URL does not contain normalized publication number: %s", record.Source, record.URL)
		}
	}
	google := output.Records[1]
	if google.Source != "google_patents" || google.RecordDepth != "full_record" || google.EvidenceState != "record-read" {
		t.Fatalf("Google patent record was not deeply read: %#v", google)
	}
	if google.Title != "Oral peptide formulation" || google.Abstract == "" || len(google.Claims) != 2 ||
		!google.Claims[0].Independent || google.Claims[1].Independent || len(google.ExampleExcerpts) < 2 {
		t.Fatalf("parsed Google patent record = %#v", google)
	}
	if output.EvidenceDepth != "full_record" || !strings.Contains(output.NextAction, "independent claims") {
		t.Fatalf("lookup depth guidance = %#v", output)
	}
}

func TestLookupReadsCurrentGooglePatentClaimsAndDescriptionMarkup(t *testing.T) {
	t.Parallel()

	client := NewClient(Options{FetchHTML: func(_ context.Context, _ string) (string, error) {
		return `<html><head><meta itemprop="description" content="locator metadata"></head><body>
			<section itemprop="abstract"><div itemprop="content">An oral peptide composition.</div></section>
			<section itemprop="description"><div itemprop="content" class="description">
				<div id="p-0002" num="0001" class="description-line">The invention relates to oral delivery of a peptide.</div>
				<div id="p-0042" num="0041" class="description-line">Example 1 increased peptide exposure in dogs.</div>
			</div></section>
			<section itemprop="claims"><div itemprop="content" class="claims">
				<div class="claim"><div id="CLM-00001" num="00001" class="claim-text"><b>1</b>. An oral peptide composition comprising an enhancer.</div></div>
				<div class="claim"><div id="CLM-00002" num="00002" class="claim-text"><b>2</b>. The composition according to claim 1.</div></div>
			</div></section>
		</body></html>`, nil
	}})
	output, err := client.Run(context.Background(), Input{
		Operation: OperationLookup, PublicationNumber: "US20230241233A1", Sources: []string{"google"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(output.Records) != 1 {
		t.Fatalf("records = %#v", output.Records)
	}
	record := output.Records[0]
	if record.RecordDepth != "full_record" || len(record.Claims) != 2 ||
		len(record.DescriptionExcerpts) == 0 || len(record.ExampleExcerpts) == 0 ||
		strings.Join(record.SectionsRead, ",") != "abstract,claims,description,examples" {
		t.Fatalf("current Google record was not deeply read: %#v", record)
	}
}

func TestLookupSeparatesRecordDepthFromTaskFocusRelevance(t *testing.T) {
	record := parseGooglePatentRecord(`
		<html><body>
		<span itemprop="title">Ionic liquids for transdermal drug delivery</span>
		<section itemprop="abstract">An ionic liquid composition for transdermal delivery.</section>
		<section itemprop="claims"><div class="claim-text"><b>1</b> An ionic liquid transdermal composition.</div></section>
		<section itemprop="description"><div id="p0001" class="description-paragraph">Technical field: transdermal delivery through skin.</div></section>
		</body></html>`, Record{PublicationNumber: "US1", EvidenceState: "official-record-location", RecordDepth: "locator"},
		[]string{"ionic liquid", "oral peptide", "delivery"}, 32*1024)
	if record.RecordDepth != "full_record" || record.FocusRelevant || !reflect.DeepEqual(record.MatchedFocusTerms, []string{"delivery", "ionic liquid"}) {
		t.Fatalf("record focus contract = %#v", record)
	}
}

func TestLookupDoesNotCallAnAbstractOnlyRecordFull(t *testing.T) {
	t.Parallel()

	client := NewClient(Options{FetchHTML: func(_ context.Context, _ string) (string, error) {
		return `<section itemprop="abstract"><div itemprop="content">Only an abstract was readable.</div></section>`, nil
	}})
	output, err := client.Run(context.Background(), Input{
		Operation: OperationLookup, PublicationNumber: "US20230040805A1", Sources: []string{"google"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(output.Records) != 1 || output.Records[0].RecordDepth != "partial_record" ||
		output.Records[0].EvidenceState != "record-partially-read" || output.EvidenceDepth != "locator_only" {
		t.Fatalf("abstract-only depth = %#v", output)
	}
}

func TestLookupAcceptsAbstractAndClaimsWhenTranslatedDescriptionIsUnavailable(t *testing.T) {
	record := parseGooglePatentRecord(`
		<section itemprop="abstract"><div itemprop="content">A receptor agonist compound for treating metabolic disease.</div></section>
		<section itemprop="claims"><div class="claim-text"><b>1</b> A compound of Formula I.</div></section>`,
		Record{PublicationNumber: "US1", EvidenceState: "official-record-location", RecordDepth: "locator"},
		nil, 32*1024)
	if record.RecordDepth != "full_record" || record.EvidenceState != "record-read" ||
		strings.Join(record.SectionsRead, ",") != "abstract,claims" {
		t.Fatalf("abstract-plus-claims record was not accepted as a substantive read: %#v", record)
	}
}

func TestLookupReadsTranslatedGooglePatentParagraphMarkup(t *testing.T) {
	t.Parallel()

	client := NewClient(Options{FetchHTML: func(_ context.Context, _ string) (string, error) {
		return `<section itemprop="abstract"><div itemprop="content">Translated patent abstract.</div></section>
			<section itemprop="description"><div itemprop="content"><description>
				<p id="p0001" num="0001"><span>Technical field for oral delivery.</span></p>
				<p id="p0040" num="0040"><span>Example 1 increased exposure.</span></p>
			</description></div></section>
			<section itemprop="claims"><div itemprop="content"><claims>
				<claim id="zh-cl0001" num="0001"><claim-text><span>1. An oral composition.</span></claim-text></claim>
				<claim id="zh-cl0002" num="0002"><claim-text><span>2. The composition according to claim 1.</span></claim-text></claim>
			</claims></div></section>`, nil
	}})
	output, err := client.Run(context.Background(), Input{
		Operation: OperationLookup, PublicationNumber: "CN114728886B", Sources: []string{"google"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	record := output.Records[0]
	if record.RecordDepth != "full_record" || len(record.Claims) != 2 ||
		len(record.DescriptionExcerpts) == 0 || len(record.ExampleExcerpts) == 0 {
		t.Fatalf("translated Google record was not deeply read: %#v", record)
	}
}

func TestResolveDownloadExtractsOnlyTrustedGooglePatentPDF(t *testing.T) {
	t.Parallel()

	t.Run("trusted", func(t *testing.T) {
		client := NewClient(Options{FetchHTML: func(_ context.Context, pageURL string) (string, error) {
			if pageURL != "https://patents.google.com/patent/US1234567A/en" {
				t.Fatalf("page URL = %q", pageURL)
			}
			return `<html><head><meta name="citation_pdf_url" content="https://patentimages.storage.googleapis.com/ab/cd/patent.pdf"></head></html>`, nil
		}})
		output, err := client.Run(context.Background(), Input{
			Operation:         OperationResolveDownload,
			PublicationNumber: "US-1234567-A",
			Sources:           []string{"google"},
		})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if len(output.Downloads) != 1 || output.Downloads[0].State != DownloadResolved {
			t.Fatalf("downloads = %#v", output.Downloads)
		}
		if got := output.Downloads[0].DownloadURL; got != "https://patentimages.storage.googleapis.com/ab/cd/patent.pdf" {
			t.Fatalf("download URL = %q", got)
		}
	})

	t.Run("untrusted host", func(t *testing.T) {
		client := NewClient(Options{FetchHTML: func(context.Context, string) (string, error) {
			return `<meta name="citation_pdf_url" content="https://attacker.example/patent.pdf">`, nil
		}})
		output, err := client.Run(context.Background(), Input{
			Operation:         OperationResolveDownload,
			PublicationNumber: "US1234567A",
			Sources:           []string{"google_patents"},
		})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if len(output.Downloads) != 1 || output.Downloads[0].State != DownloadUnavailable || output.Downloads[0].DownloadURL != "" {
			t.Fatalf("untrusted download was not rejected: %#v", output.Downloads)
		}
	})

	for _, candidate := range []string{
		"https://user@patentimages.storage.googleapis.com/ab/cd/patent.pdf",
		"https://patentimages.storage.googleapis.com:8443/ab/cd/patent.pdf",
		"https://patentimages.storage.googleapis.com/ab/cd/patent.pdf?download=1",
		"https://patentimages.storage.googleapis.com/ab/cd/patent.pdf#page=1",
		"https://patentimages.storage.googleapis.com/ab/cd/patent.pdf.exe",
	} {
		candidate := candidate
		t.Run("rejects unsafe URL "+candidate, func(t *testing.T) {
			client := NewClient(Options{FetchHTML: func(context.Context, string) (string, error) {
				return `<meta name="citation_pdf_url" content="` + candidate + `">`, nil
			}})
			output, err := client.Run(context.Background(), Input{
				Operation: OperationResolveDownload, PublicationNumber: "US1234567A", Sources: []string{"google"},
			})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if len(output.Downloads) != 1 || output.Downloads[0].State != DownloadUnavailable || output.Downloads[0].DownloadURL != "" {
				t.Fatalf("unsafe download was not rejected: %#v", output.Downloads)
			}
		})
	}
}

func TestResolveCNIPADownloadRequiresInteractiveAuthenticationWithoutFetching(t *testing.T) {
	t.Parallel()

	fetchCalls := 0
	client := NewClient(Options{FetchHTML: func(context.Context, string) (string, error) {
		fetchCalls++
		return "", errors.New("must not be called")
	}})
	output, err := client.Run(context.Background(), Input{
		Operation:         OperationResolveDownload,
		PublicationNumber: "CN117123456A",
		Sources:           []string{"china"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if fetchCalls != 0 {
		t.Fatalf("CNIPA interactive boundary made %d fetches", fetchCalls)
	}
	if len(output.Downloads) != 1 || output.Downloads[0].State != DownloadUserActionRequired || !output.Downloads[0].RequiresAuthentication {
		t.Fatalf("downloads = %#v", output.Downloads)
	}
}

func TestRunRejectsInvalidPatentRequests(t *testing.T) {
	t.Parallel()

	client := NewClient(Options{})
	tests := []struct {
		name  string
		input Input
	}{
		{name: "unknown operation", input: Input{Operation: "crawl", Query: "electric lamp"}},
		{name: "search without query", input: Input{Operation: OperationSearch}},
		{name: "lookup without publication", input: Input{Operation: OperationLookup}},
		{name: "invalid publication", input: Input{Operation: OperationLookup, PublicationNumber: "../../secret"}},
		{name: "unknown source", input: Input{Operation: OperationSearch, Query: "electric lamp", Sources: []string{"random"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.Run(context.Background(), test.input); err == nil {
				t.Fatalf("Run(%#v) succeeded, want error", test.input)
			}
		})
	}
}
