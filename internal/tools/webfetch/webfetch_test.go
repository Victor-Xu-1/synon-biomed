package webfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"synon-go/internal/mcpdirectory"
)

type timeoutRoundTripper struct{}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestResearchSourceHTTPPreservesRequestFinalAndResponseMetadata(t *testing.T) {
	payload := []byte("complete source body")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(w, request, "/final", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2025 08:00:00 GMT")
		w.Header().Set("ETag", `"source-v2"`)
		w.Header().Set("Date", "Thu, 11 Sep 2025 09:30:00 GMT")
		w.Header().Set("Content-Language", "en")
		w.Header().Set("Content-Location", "/canonical-record")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	now := time.Date(2026, 9, 11, 1, 2, 3, 4, time.UTC)
	result, err := FetchWithOptions(context.Background(), server.URL+"/start", 1024, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	if result.RequestedURL != server.URL+"/start" || result.URL != server.URL+"/final" || !result.Complete ||
		result.RetrievedAt != now.Format(time.RFC3339Nano) || result.ResponseDate != "Thu, 11 Sep 2025 09:30:00 GMT" ||
		result.LastModified != "Wed, 10 Sep 2025 08:00:00 GMT" || result.ETag != `"source-v2"` ||
		result.ContentLanguage != "en" || result.ContentLocation != "/canonical-record" ||
		result.BodySHA256 != hex.EncodeToString(digest[:]) || result.BodyHashScope != "returned_response_bytes" {
		t.Fatalf("HTTP response metadata=%#v", result)
	}
	if len(result.Redirects) != 1 || result.Redirects[0].From != server.URL+"/start" ||
		result.Redirects[0].To != server.URL+"/final" || result.Redirects[0].StatusCode != http.StatusFound {
		t.Fatalf("redirect provenance=%#v", result.Redirects)
	}
}

func TestResearchSourceHTTPBoundsSelectedUntrustedHeaderMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", strings.Repeat("x", MaxMetadataValueBytes+100))
		_, _ = w.Write([]byte("source"))
	}))
	defer server.Close()
	result, err := FetchWithOptions(context.Background(), server.URL, 1024, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ETag) > MaxMetadataValueBytes || len(result.MetadataTruncated) != 1 || result.MetadataTruncated[0] != "etag" {
		t.Fatalf("unbounded HTTP metadata=%#v", result)
	}
}

func (timeoutRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, timeoutNetworkError{}
}

type timeoutNetworkError struct{}

func (timeoutNetworkError) Error() string   { return "simulated upstream timeout" }
func (timeoutNetworkError) Timeout() bool   { return true }
func (timeoutNetworkError) Temporary() bool { return true }

type readTimeoutBody struct {
	read    bool
	payload []byte
}

func (body *readTimeoutBody) Read(target []byte) (int, error) {
	if body.read {
		return 0, timeoutNetworkError{}
	}
	body.read = true
	payload := body.payload
	if len(payload) == 0 {
		payload = []byte("partial source bytes")
	}
	return copy(target, payload), nil
}

func (*readTimeoutBody) Close() error { return nil }

type readTimeoutRoundTripper struct{}

func (readTimeoutRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       &readTimeoutBody{},
		Request:    request,
	}, nil
}

func TestFetchReadsRealHTTPServerWithLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/source":
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		case "/final":
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Synon Go rewrite source page"))
	}))
	defer server.Close()

	result, err := FetchWithOptions(context.Background(), server.URL+"/source", 64, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d", result.StatusCode)
	}
	if result.ContentType != "text/plain; charset=utf-8" {
		t.Fatalf("ContentType = %q", result.ContentType)
	}
	if result.URL != server.URL+"/final" {
		t.Fatalf("URL = %q, want final response URL %q", result.URL, server.URL+"/final")
	}
	if result.Body != "Synon Go rewrite source page" {
		t.Fatalf("Body = %q", result.Body)
	}
	truncated, err := FetchWithOptions(context.Background(), server.URL+"/final", 10, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
	})
	if err != nil {
		t.Fatalf("bounded truncated read error = %v", err)
	}
	if truncated.Body != "Synon Go r" || !truncated.Truncated || truncated.BytesRead != 10 ||
		truncated.ContentLength != int64(len("Synon Go rewrite source page")) || truncated.Binary {
		t.Fatalf("truncated text result = %#v", truncated)
	}
	if envelope := truncated.ToolResultEnvelope(); envelope["partial"] != true || envelope["sourceUnavailable"] == true {
		t.Fatalf("truncated text envelope = %#v", envelope)
	}
}

func TestFetchReturnsBinaryOverflowAsDownloadHandoffWithoutRawBody(t *testing.T) {
	payload := append([]byte{0x89, 'H', 'D', 'F', '\r', '\n', 0x1a, '\n'}, []byte("scientific matrix payload")...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-hdf5")
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	result, err := FetchWithOptions(context.Background(), server.URL+"/matrix.h5", 8, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
	})
	if err != nil {
		t.Fatalf("binary handoff error = %v", err)
	}
	if !result.Truncated || !result.Binary || result.Body != "" || result.BytesRead != 8 ||
		result.ContentLength != int64(len(payload)) || result.Recovery != "use_dedicated_download_or_fulltext_tool" {
		t.Fatalf("binary handoff result = %#v", result)
	}
}

func TestFetchMarksHTTPErrorAsSourceUnavailableWithoutLosingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"missing compound"}`))
	}))
	defer server.Close()

	result, err := FetchWithOptions(context.Background(), server.URL+"/compound/404", 128, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return server.Client(), nil },
	})
	if err != nil {
		t.Fatalf("FetchWithOptions() error = %v", err)
	}
	if result.StatusCode != http.StatusNotFound || !result.SourceUnavailable {
		t.Fatalf("HTTP error result = %#v", result)
	}
	if result.Error == "" || result.Body != `{"error":"missing compound"}` || result.URL != server.URL+"/compound/404" {
		t.Fatalf("HTTP error diagnostics = %#v", result)
	}
}

func TestFetchMarksTransportTimeoutAsRecoverableSourceUnavailable(t *testing.T) {
	result, err := FetchWithOptions(context.Background(), "https://public.example/source", 128, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) {
			return &http.Client{Transport: timeoutRoundTripper{}}, nil
		},
	})
	if err != nil {
		t.Fatalf("FetchWithOptions() error = %v", err)
	}
	if !result.SourceUnavailable || result.Error != "Public source request timed out." ||
		result.Recovery != "retry_later_or_use_source_specific_tool" || result.URL != "https://public.example/source" {
		t.Fatalf("transport timeout result = %#v", result)
	}
	if envelope := result.ToolResultEnvelope(); envelope["sourceUnavailable"] != true {
		t.Fatalf("transport timeout envelope = %#v", envelope)
	}
}

func TestFetchMarksResponseReadTimeoutAsRecoverablePartialSource(t *testing.T) {
	result, err := FetchWithOptions(context.Background(), "https://public.example/large.xml", 128, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) {
			return &http.Client{Transport: readTimeoutRoundTripper{}}, nil
		},
	})
	if err != nil {
		t.Fatalf("FetchWithOptions() error = %v", err)
	}
	if !result.SourceUnavailable || !result.Partial || result.Error != "Public source response timed out while reading." ||
		result.Recovery != "retry_later_or_use_source_specific_tool" || result.Body != "partial source bytes" ||
		result.BytesRead != len("partial source bytes") || result.ContentType != "application/xml" {
		t.Fatalf("response read timeout result = %#v", result)
	}
	if envelope := result.ToolResultEnvelope(); envelope["sourceUnavailable"] != true || envelope["partial"] != true {
		t.Fatalf("response read timeout envelope = %#v", envelope)
	}
}

func TestFetchTurnsSuccessfulBinaryReadTimeoutIntoGovernedDownloadHandoff(t *testing.T) {
	result, err := FetchWithOptions(context.Background(), "https://public.example/label.pdf", 128, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) {
			return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK, Status: "200 OK",
					Header:  http.Header{"Content-Type": []string{"application/pdf"}},
					Body:    &readTimeoutBody{payload: []byte("%PDF-partial")},
					Request: request,
				}, nil
			})}, nil
		},
	})
	if err != nil {
		t.Fatalf("FetchWithOptions() error = %v", err)
	}
	if result.SourceUnavailable || !result.Partial || !result.Binary || result.Error != "" || result.Body != "" ||
		result.Recovery != "use_dedicated_download_or_fulltext_tool" || result.URL != "https://public.example/label.pdf" {
		t.Fatalf("binary read timeout handoff = %#v", result)
	}
	if envelope := result.ToolResultEnvelope(); envelope["sourceUnavailable"] == true || envelope["partial"] != true {
		t.Fatalf("binary read timeout envelope = %#v", envelope)
	}
}

func TestFetchMarksPublicHostnameResolutionBlockAsRecoverableSourceUnavailable(t *testing.T) {
	result, err := FetchWithOptions(context.Background(), "https://blocked.example/source", 128, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) {
			return nil, fmt.Errorf("resolver sinkhole: %w", mcpdirectory.ErrPublicDestinationUnavailable)
		},
	})
	if err != nil {
		t.Fatalf("FetchWithOptions() error = %v", err)
	}
	if !result.SourceUnavailable || result.URL != "https://blocked.example/source" ||
		result.Error != "Public source address is unavailable or blocked by the local network." ||
		result.Recovery != "retry_later_or_use_source_specific_tool" {
		t.Fatalf("resolution-block result = %#v", result)
	}
}

func TestFetchStillPropagatesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := FetchWithOptions(ctx, "https://public.example/source", 128, Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) {
			return &http.Client{Transport: timeoutRoundTripper{}}, nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchWithOptions() error = %v, want context.Canceled", err)
	}
}

func TestFetchRejectsLocalAndOversizedDestinationsBeforeRequest(t *testing.T) {
	if _, err := Fetch(context.Background(), "http://127.0.0.1:8765/private", 10); err == nil {
		t.Fatal("loopback HTTP destination was accepted")
	}
	if _, err := Fetch(context.Background(), "https://example.org/", MaxResponseLimit+1); err == nil {
		t.Fatal("oversized in-memory response limit was accepted")
	}
}

func TestFetchRevalidatesEveryRedirectDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://127.0.0.1:8765/private")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	requests := 0
	_, err := FetchWithOptions(context.Background(), server.URL, 10, Options{
		ClientForURL: func(_ context.Context, target string) (*http.Client, error) {
			requests++
			if target != server.URL {
				return nil, fmt.Errorf("redirect destination denied")
			}
			return server.Client(), nil
		},
	})
	if err == nil || requests != 2 {
		t.Fatalf("redirect result err=%v requests=%d", err, requests)
	}
}
