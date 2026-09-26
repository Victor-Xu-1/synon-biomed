package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/tools/securefetch"
)

type repeatingDownloadBytes struct{}

func (repeatingDownloadBytes) Read(value []byte) (int, error) {
	for index := range value {
		value[index] = 'A'
	}
	return len(value), nil
}

func TestPublicDownloadRealTLSResumesManyConnectionsAndPublishesExactBytes(t *testing.T) {
	size := int64(128 << 10)
	if raw := os.Getenv("SYNON_TEST_TRANSFER_BYTES"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1024 {
			t.Fatal("positive transfer size >=1024 required")
		}
		size = parsed
	}
	fixture := newAgentSaveArtifactsFixture(t)
	const source = "https://download.example.org/software-source.txt"
	var calls, transferred atomic.Int64
	chunk := size / 9
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		start := int64(0)
		if raw := r.Header.Get("Range"); raw != "" {
			if _, err := fmt.Sscanf(raw, "bytes=%d-", &start); err != nil || r.Header.Get("If-Range") != `"fixed-source"` {
				t.Errorf("invalid resume headers: range=%q validator=%q", raw, r.Header.Get("If-Range"))
				http.Error(w, "invalid", 400)
				return
			}
		}
		if start < 0 || start >= size {
			t.Errorf("invalid offset %d/%d", start, size)
			http.Error(w, "invalid", 416)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", `"fixed-source"`)
		w.Header().Set("Content-Length", strconv.FormatInt(size-start, 10))
		if start > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, size-1, size))
			w.WriteHeader(http.StatusPartialContent)
		}
		written, _ := io.CopyN(w, repeatingDownloadBytes{}, min(chunk, size-start))
		transferred.Add(written)
		// Most responses deliberately end before Content-Length. The real
		// HTTP parser must detect truncation; no mocked transport error.
	}))
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Fixture-only virtual hostname.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	defer transport.CloseIdleConnections()
	fixture.server.publicScientificFiles = securefetch.New(securefetch.Options{HTTPClient: &http.Client{Transport: transport}, TestOnlyAllowCustomTransport: true, Resolver: scientificRedirectTestResolver{}})
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	hash := sha256.New()
	if _, err := io.CopyN(hash, repeatingDownloadBytes{}, size); err != nil {
		t.Fatal(err)
	}
	expected := hex.EncodeToString(hash.Sum(nil))
	callID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "tls-multipart-source", source, false, nil)
	input := map[string]any{"url": source, "source_tool_call_id": callID, "expected_sha256": expected, "human_description": "Acquiring a complete software resource"}
	ctx, cancel := context.WithTimeout(agentPublicScientificToolContext(t, fixture, "tls-multipart-download", input), 90*time.Second)
	defer cancel()
	result, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "tls-multipart-download", input)
	if err != nil || result["ok"] != true {
		t.Fatalf("real acquisition failed: result=%#v error=%v", result, err)
	}
	file, err := os.Open(filepath.Join(fixture.projectPath, "software-source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash.Reset()
	written, err := io.Copy(hash, file)
	if err != nil || written != size || hex.EncodeToString(hash.Sum(nil)) != expected || transferred.Load() != size || calls.Load() <= 4 {
		t.Fatalf("published transfer mismatch: bytes=%d/%d network_bytes=%d requests=%d error=%v", written, size, transferred.Load(), calls.Load(), err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || !strings.EqualFold(stringValue(artifacts[0]["checksum"]), expected) {
		t.Fatalf("immutable publication mismatch: %#v", artifacts)
	}
	t.Logf("real_TLS_bytes=%d connections=%d exact_SHA256=%s redundant_payload_bytes=%d", size, calls.Load(), expected, transferred.Load()-size)
}

func TestPublicDownloadServerBackoffAndPrefixSurviveServiceBoundary(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = "retained prefix and remaining data"
	const prefix = 9
	var calls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		number := calls.Add(1)
		if number == 2 {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "temporary", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("ETag", `"retry-source"`)
		if number == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(content)))
			_, _ = io.WriteString(w, content[:prefix])
			return
		}
		if r.Header.Get("Range") != "bytes=9-" || r.Header.Get("If-Range") != `"retry-source"` {
			t.Errorf("lost checkpoint: %v", r.Header)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", prefix, len(content)-1, len(content)))
		w.Header().Set("Content-Length", strconv.Itoa(len(content)-prefix))
		w.WriteHeader(206)
		_, _ = io.WriteString(w, content[prefix:])
	}))
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Fixture-only virtual hostname.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	defer transport.CloseIdleConnections()
	fetcher := securefetch.New(securefetch.Options{HTTPClient: &http.Client{Transport: transport}, TestOnlyAllowCustomTransport: true, Resolver: scientificRedirectTestResolver{}})
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificResponseHeaderTimeout = time.Second
	request, err := parseAgentPublicScientificFileRequest(map[string]any{"url": "https://download.example.org/retry.txt", "human_description": "Acquiring a resource"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = fixture.server.fetchAndStageAgentPublicScientificFile(context.Background(), fixture.projectPath, request)
	var delayed *agentPublicScientificTransferInterrupted
	if !errors.As(err, &delayed) || delayed.BytesRetained != prefix || !delayed.Resumable || calls.Load() != 2 {
		t.Fatalf("deferred transfer=%#v calls=%d error=%v", delayed, calls.Load(), err)
	}
	receipt, handled := agentPublicScientificSourceUnavailable(request, err)
	if !handled || receipt["retryable"] != true || receipt["bytes_retained"] != int64(prefix) || receipt["downloaded"] != false {
		t.Fatalf("recovery evidence=%#v", receipt)
	}
	// A newly constructed service must read both offset and cooldown from disk.
	second := &Server{fileRoot: fixture.server.fileRoot, publicScientificFiles: fetcher, publicScientificResponseHeaderTimeout: time.Second}
	_, _, err = second.fetchAndStageAgentPublicScientificFile(context.Background(), fixture.projectPath, request)
	if !errors.As(err, &delayed) || calls.Load() != 2 {
		t.Fatalf("early replay ignored Retry-After: calls=%d error=%v", calls.Load(), err)
	}
	time.Sleep(max(0, time.Until(delayed.RetryNotBefore)) + 20*time.Millisecond)
	staged, _, err := second.fetchAndStageAgentPublicScientificFile(context.Background(), fixture.projectPath, request)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.close()
	value, err := io.ReadAll(staged.file)
	if err != nil || string(value) != content || calls.Load() != 3 {
		t.Fatalf("resumed content=%q calls=%d error=%v", value, calls.Load(), err)
	}
}

func TestPublicDownloadPermanentStatusIsNotAdvertisedRetryable(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusNotImplemented, http.StatusHTTPVersionNotSupported} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "not supported", status)
			}))
			defer upstream.Close()
			transport := upstream.Client().Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			transport.TLSClientConfig.InsecureSkipVerify = true // Fixture-only virtual hostname.
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}
			defer transport.CloseIdleConnections()
			fetcher := securefetch.New(securefetch.Options{HTTPClient: &http.Client{Transport: transport}, TestOnlyAllowCustomTransport: true, Resolver: scientificRedirectTestResolver{}})
			request, err := parseAgentPublicScientificFileRequest(map[string]any{"url": "https://download.example.org/resource.txt", "human_description": "Acquiring a resource"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = fetcher.Fetch(context.Background(), request.DownloadURL, (&Server{}).agentPublicScientificTransferPolicy(request))
			result, handled := agentPublicScientificSourceUnavailable(request, err)
			if !handled || isRetryableAgentPublicScientificDownloadError(err) || result["retryable"] != false || result["http_status"] != status {
				t.Fatalf("retry decision and receipt disagree: result=%#v err=%v", result, err)
			}
		})
	}
}
