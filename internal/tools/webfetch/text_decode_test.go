package webfetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"synon-go/internal/httptext"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

func TestFetchDecodesDeclaredTextWithoutDroppingTheDocument(t *testing.T) {
	for _, test := range []struct {
		name, media, text string
		encoding          encoding.Encoding
	}{
		{"html-meta-gb2312", "text/html", `<html><head><meta charset="gb2312"></head><body>完整资料与结果，末尾数据</body></html>`, simplifiedchinese.GBK},
		{"html-http-equiv", "text/html", `<html><head><meta http-equiv="content-type" content="text/html; charset=gb2312"></head><body>完整资料与结果，末尾数据</body></html>`, simplifiedchinese.GBK},
		{"header-precedence", "text/html; charset=gbk", `<html><head><meta charset="utf-8"></head><body>完整资料与结果，末尾数据</body></html>`, simplifiedchinese.GBK},
		{"header-big5", "text/html; charset=big5", `<html><body>完整資料與結果，末尾資料</body></html>`, traditionalchinese.Big5},
		{"header-latin1", "text/plain; charset=iso-8859-1", "résultats complets à la fin", charmap.ISO8859_1},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := test.encoding.NewEncoder().Bytes([]byte(test.text))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.media)
				w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
				_, _ = w.Write(payload)
			}))
			defer server.Close()
			result, err := FetchWithOptions(context.Background(), server.URL, MaxResponseLimit, Options{
				ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
			})
			if err != nil || result.Body != test.text || result.BytesRead != len(payload) || result.SourceUnavailable || result.Partial {
				t.Fatalf("complete encoded document was lost: result=%#v error=%v", result, err)
			}
		})
	}
}

func TestFetchInvalidUTF8DoesNotPretendPrefixIsTheWholeResponse(t *testing.T) {
	payload := append([]byte("start"), 0xff)
	payload = append(payload, []byte(strings.Repeat("END", 1000))...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	result, err := FetchWithOptions(context.Background(), server.URL, MaxResponseLimit, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
	})
	if err != nil || result.BytesRead != len(payload) || !result.SourceUnavailable || result.Error == "" || bytes.Equal([]byte(result.Body), payload[:5]) {
		t.Fatalf("invalid encoding silently lost content: %#v %v", result, err)
	}
	restored, err := base64.StdEncoding.DecodeString(result.RawBodyBase64)
	if err != nil || !bytes.Equal(restored, payload) {
		t.Fatalf("original response is not recoverable: %v", err)
	}
}

func TestDecodeWebTextKeepsUTF8PartialBoundaryDistinctFromCorruptInterior(t *testing.T) {
	partial := append([]byte("complete prefix"), []byte("中")[:2]...)
	text, err := httptext.Decode(partial, "text/plain; charset=utf-8", true)
	if err != nil || text != "complete prefix" {
		t.Fatalf("partial text=%q err=%v", text, err)
	}
	if _, err := httptext.Decode(partial, "text/plain; charset=utf-8", false); err == nil {
		t.Fatal("incomplete UTF-8 was accepted in a complete response")
	}
	if _, err := httptext.Decode(append([]byte{0xff}, partial...), "text/plain; charset=utf-8", true); err == nil {
		t.Fatal("invalid interior byte was silently omitted")
	}
	if _, err := httptext.Decode([]byte("source"), "text/plain; charset=unknown-encoding", false); err == nil {
		t.Fatal("unsupported encoding was silently accepted")
	}
}

func TestWebResponseBinarySniffDoesNotSplitUTF8(t *testing.T) {
	for _, char := range []string{"θ", "中", "🧬"} {
		for split := 1; split < len(char); split++ {
			body := []byte(strings.Repeat("a", 4096-split) + char + "end")
			if webResponseIsBinary("https://example.org/resource", "", body, false) {
				t.Fatalf("valid text classified as binary at %q byte %d", char, split)
			}
		}
	}
	for _, body := range [][]byte{[]byte("data\x00binary"), []byte("data\xffinvalid"), append([]byte(strings.Repeat("a", 4095)), 0xff)} {
		if !webResponseIsBinary("https://example.org/resource", "", body, false) {
			t.Fatal("binary content accepted as text")
		}
	}
	partial := append([]byte("text"), []byte("中")[:2]...)
	if webResponseIsBinary("https://example.org/resource", "", partial, true) || !webResponseIsBinary("https://example.org/resource", "", partial, false) {
		t.Fatal("partial and malformed complete responses were conflated")
	}
}
