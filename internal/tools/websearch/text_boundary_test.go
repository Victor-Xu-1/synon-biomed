package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestCompactStringPreservesUTF8AtEveryByteBoundary(t *testing.T) {
	for _, source := range []string{"ASCII", "完整检索摘要", "Aθ中🧬末尾"} {
		for limit := 1; limit < len(source); limit++ {
			got := compactString(source, limit)
			prefix := strings.TrimSuffix(got, "...[truncated]")
			if !utf8.ValidString(got) || len(prefix) > limit || !strings.HasPrefix(source, prefix) {
				t.Fatalf("source=%q limit=%d corrupted preview=%q", source, limit, got)
			}
		}
		if got := compactString(source, len(source)); got != source {
			t.Fatalf("complete snippet changed: %q", got)
		}
	}
}

func TestSearchDecodesHTMLCharacterSetBeforeParsingEvidence(t *testing.T) {
	for _, media := range []string{"text/html; charset=gbk", "text/html"} {
		t.Run(media, func(t *testing.T) {
			body := `<html><head><meta charset="gb2312"></head><body><a class="result__a" href="https://example.org/source">Synon 完整资料</a><a class="result__snippet">Synon 检索摘要保留中文末尾</a></body></html>`
			encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", media)
				_, _ = w.Write(encoded)
			}))
			defer server.Close()
			out, err := Search(context.Background(), Input{Query: "Synon", MaxResults: 3}, Options{
				HTTPBackends: []httpSearchBackend{{Name: "test", Endpoint: server.URL + "?q={query}", Format: searchFormatHTML}},
				ClientForURL: testHTTPClientFor(server.Client()),
			})
			if err != nil || out.Failure != nil || len(out.Sources) != 1 {
				t.Fatalf("search unavailable: %#v %v", out, err)
			}
			if out.Sources[0].Title != "Synon 完整资料" || out.Sources[0].Snippet != "Synon 检索摘要保留中文末尾" {
				t.Fatalf("source corrupted before evidence delivery: %#v", out.Sources[0])
			}
		})
	}
}
