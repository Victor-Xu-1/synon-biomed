package websearch

import "testing"

func TestDuckDuckGoHTMLKeepsSnippetsWithTheirResult(t *testing.T) {
	for _, body := range []string{
		`<div class="result"><h2><a href="https://example.org/1" class="result__a">First title</a></h2><a class="result__snippet">First <b>full</b> snippet</a></div>
<div class="result"><h2><a class="result__a" href="https://example.org/2">Second title</a></h2></div>
<div class="result"><h2><a class="result__a" href="https://example.org/3">Third title</a></h2><a class="result__snippet">Third &amp; final snippet</a></div>`,
		`<a class="result__a" href="https://example.org/1">First title</a><a class="result__snippet">First <b>full</b> snippet</a>
<a class="result__a" href="https://example.org/2">Second title</a>
<a class="result__a" href="https://example.org/3">Third title</a><a class="result__snippet">Third &amp; final snippet</a>`,
	} {
		results := parseDuckDuckGoHTMLResults(body)
		if len(results) != 3 || results[0].Snippet != "First full snippet" || results[1].Snippet != "" || results[2].Snippet != "Third & final snippet" {
			t.Fatalf("snippet association changed: %#v", results)
		}
	}
}
