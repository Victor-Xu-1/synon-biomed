package webfetch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchLargeBinaryHandsOffAfterSmallPrefix(t *testing.T) {
	const size = 8 << 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(size))
		_, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", size)))
	}))
	defer server.Close()
	result, err := FetchWithOptions(context.Background(), server.URL+"/archive.tar", MaxResponseLimit, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
	})
	if err != nil || !result.Binary || !result.Partial || result.Complete || result.SourceUnavailable || result.BytesRead > 4096 || result.Recovery != "use_dedicated_download_or_fulltext_tool" {
		t.Fatalf("binary acquisition stayed in page reader: bytes=%d complete=%t unavailable=%t error=%v", result.BytesRead, result.Complete, result.SourceUnavailable, err)
	}
}

func TestFetchTransientStatusRetriesSameRead(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "temporary", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("source document"))
	}))
	defer server.Close()
	result, err := FetchWithOptions(context.Background(), server.URL+"/paper.txt", 1024, Options{ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil }})
	if err != nil || result.SourceUnavailable || result.Body != "source document" || calls.Load() != 2 {
		t.Fatalf("transient read failed: calls=%d result=%#v error=%v", calls.Load(), result, err)
	}
}
