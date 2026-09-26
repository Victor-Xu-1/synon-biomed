package webfetch

import "testing"

func TestHTMLContinuationKeepsExactPageIdentityAndFormat(t *testing.T) {
	for _, url := range []string{"https://example.org/page.html", "https://example.org/document?view=full", "https://example.org/"} {
		next := htmlDownloadContinuation(url, "text/html; charset=utf-8")
		if next == nil || next.Tool != "download_public_scientific_file" || next.Arguments["url"] != url || next.Arguments["filename"] == "" || next.ReadWith == "" {
			t.Fatalf("missing exact continuation for %q: %#v", url, next)
		}
	}
	for _, test := range [][2]string{{"https://example.org/data.pdf", "application/pdf"}, {"file:///tmp/a", "text/html"}, {"https://user:secret@example.org/page.html", "text/html"}} {
		if next := htmlDownloadContinuation(test[0], test[1]); next != nil {
			t.Fatalf("unsafe continuation: %#v", next)
		}
	}
}
