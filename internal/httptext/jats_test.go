package httptext

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResearchSourceRecordJATSPreservesArticleSectionsWithoutFrontMatterNoise(t *testing.T) {
	source := `<article xmlns:xlink="http://www.w3.org/1999/xlink"><front><article-meta>` +
		`<title-group><article-title><italic>Evidence</italic> &amp; Results</article-title></title-group>` +
		`<contrib-group>` + strings.Repeat(`<contrib><name><surname>Noise</surname></name><aff>Long affiliation</aff></contrib>`, 100) + `</contrib-group>` +
		`<abstract><p>Complete abstract with a measured value of 42%.</p></abstract>` +
		`</article-meta></front><body><sec><title>Results</title>` +
		`<p>The primary endpoint was met.</p><table-wrap><caption><p>Table 1 efficacy.</p></caption>` +
		`<table><tr><th>Cohort</th><th>ORR</th></tr><tr><td>A</td><td>42%</td></tr></table></table-wrap>` +
		`<fig><caption><p>Figure 1 response duration.</p></caption></fig>` +
		`<p>Publisher source <ext-link xlink:href="https://example.org/data">dataset</ext-link>.</p>` +
		`</sec></body><back><ref-list><ref><mixed-citation>Reference DOI 10.1000/example.</mixed-citation></ref></ref-list></back></article>`
	document, err := JATSDocument(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if document.Title != "Evidence & Results" {
		t.Fatalf("title=%q", document.Title)
	}
	for _, expected := range []string{
		"Abstract", "Complete abstract with a measured value of 42%.", "Body", "Results",
		"The primary endpoint was met.", "Table 1 efficacy.", "Cohort\tORR", "A\t42%",
		"Figure 1 response duration.", "https://example.org/data", "References", "10.1000/example",
	} {
		if !strings.Contains(document.Text, expected) {
			t.Fatalf("JATS reading view lost %q:\n%s", expected, document.Text)
		}
	}
	for _, noise := range []string{"Long affiliation", "NoiseNoise"} {
		if strings.Contains(document.Text, noise) {
			t.Fatalf("front matter noise leaked into reading view: %q", noise)
		}
	}
}

func TestResearchSourceRecordJATSRejectsMalformedOrCancelledInput(t *testing.T) {
	if _, err := JATSDocument(context.Background(), `<article><body><p>broken</body></article>`); err == nil {
		t.Fatal("malformed XML was accepted")
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("reading cancelled")
	cancel(cause)
	if _, err := JATSDocument(ctx, `<article><body><p>source</p></body></article>`); !errors.Is(err, cause) {
		t.Fatalf("cancellation cause=%v", err)
	}
	deep := `<article><body>` + strings.Repeat(`<sec>`, 300) + `<p>source</p>` + strings.Repeat(`</sec>`, 300) + `</body></article>`
	if _, err := JATSDocument(context.Background(), deep); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("excessively deep XML error=%v", err)
	}
}
