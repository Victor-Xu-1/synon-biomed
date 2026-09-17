package httptext

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHTMLDocumentPreservesStaticStructureAndSourceLinks(t *testing.T) {
	source := `<html><head><title>A &amp; B</title><base href="/assets/"><script>not-source</script><style>not-source</style></head><body><nav>Navigation</nav><main><h1>Results</h1><p>A <b>measured</b> value &lt; 5 μM; x<sup>2</sup>; H<sub>2</sub>O.</p><table><tr><th colspan="2">Units</th></tr><tr><td>左</td><td>右</td></tr></table><pre> x = 1
  y = 2</pre><math><mfrac><mi>a</mi><mi>b</mi></mfrac></math><a href="paper?id=2&amp;v=3#data">Citation</a><img src="figure.png" alt="Figure 1"><a href="javascript:bad()">Readable label</a><a href="file:///etc/passwd">Local label</a><div hidden>hidden-secret</div><template>not-source</template></main><footer>Additional source</footer></body></html>`
	doc, err := HTMLDocument(context.Background(), source, "https://example.org/start")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "A & B" {
		t.Fatalf("title %q", doc.Title)
	}
	for _, want := range []string{"A measured value < 5 μM; x^(2); H_(2)O.", "左\t右", "colspan=2", " x = 1\n  y = 2", "<mfrac>", "https://example.org/assets/paper?id=2&v=3#data", "Figure 1 <https://example.org/assets/figure.png>", "Readable label", "Local label", "Additional source"} {
		if !strings.Contains(doc.Text, want) {
			t.Errorf("missing %q: %s", want, doc.Text)
		}
	}
	for _, bad := range []string{"not-source", "hidden-secret", "javascript:", "file:///"} {
		if strings.Contains(doc.Text, bad) {
			t.Errorf("unexpected %q in view", bad)
		}
	}
}

func TestHTMLDocumentKeepsMalformedBodyAndCancellation(t *testing.T) {
	doc, err := HTMLDocument(context.Background(), "<p>First <b>第二</b><p>Last", "")
	if err != nil || !strings.Contains(doc.Text, "First 第二") || !strings.Contains(doc.Text, "Last") {
		t.Fatalf("malformed body lost: %+v %v", doc, err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("user cancelled reading")
	cancel(cause)
	if _, err := HTMLDocument(ctx, "<p>source</p>", ""); !errors.Is(err, cause) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}

func BenchmarkHTMLDocumentLargeAssetHead(b *testing.B) {
	source := "<html><head><script>" + strings.Repeat("asset;", 350000) + "</script></head><body><p>Actual scientific content.</p></body></html>"
	b.SetBytes(int64(len(source)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := HTMLDocument(context.Background(), source, "https://example.org"); err != nil {
			b.Fatal(err)
		}
	}
}
