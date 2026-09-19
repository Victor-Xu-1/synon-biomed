package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolprogress"
	"synon-go/internal/tools/securefetch"
)

type agentPublicScientificFileFetcher struct {
	mu          sync.Mutex
	calls       []agentPublicScientificFetchCall
	body        string
	contentType string
	err         error
	onFetch     func()
	fetch       func(context.Context, string, securefetch.Policy) (*securefetch.Response, error)
}

type agentPublicScientificFetchCall struct {
	URL    string
	Policy securefetch.Policy
}

type blockingScientificResponseBody struct {
	started   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

type prefixThenBlockingScientificResponseBody struct {
	prefix    []byte
	delivered bool
	started   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func (body *prefixThenBlockingScientificResponseBody) Read(buffer []byte) (int, error) {
	if !body.delivered {
		body.delivered = true
		count := copy(buffer, body.prefix)
		close(body.started)
		return count, nil
	}
	<-body.closed
	return 0, context.Canceled
}

func (body *prefixThenBlockingScientificResponseBody) Close() error {
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}

func (b *blockingScientificResponseBody) Read([]byte) (int, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-b.closed
	return 0, context.Canceled
}

func (b *blockingScientificResponseBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestVerifyAgentPublicScientificStagedContentAcceptsHDF5MagicAndRejectsMismatch(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "scientific-content-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(append([]byte{0x89, 'H', 'D', 'F', '\r', '\n', 0x1a, '\n'}, []byte("payload")...)); err != nil {
		t.Fatal(err)
	}
	contentType, err := verifyAgentPublicScientificStagedContent(
		file, "matrix.h5", "", []string{"application/x-hdf5", "application/octet-stream"},
	)
	if err != nil || contentType != "application/x-hdf5" {
		t.Fatalf("content_type=%q err=%v", contentType, err)
	}
	if _, err := verifyAgentPublicScientificStagedContent(
		file, "archive.tar", "", []string{"application/x-tar"},
	); err == nil {
		t.Fatal("HDF5 payload was accepted as tar")
	}
}

func TestVerifyAgentPublicScientificStagedContentAcceptsPatentPDFAndRejectsSpoofedContent(t *testing.T) {
	accepted, err := agentPublicScientificAcceptedTypes("US223898A.pdf")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := os.CreateTemp(t.TempDir(), "patent-pdf-*")
	if err != nil {
		t.Fatal(err)
	}
	defer valid.Close()
	if _, err := valid.Write([]byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n")); err != nil {
		t.Fatal(err)
	}
	contentType, err := verifyAgentPublicScientificStagedContent(
		valid, "US223898A.pdf", "application/octet-stream", accepted,
	)
	if err != nil || contentType != "application/pdf" {
		t.Fatalf("content_type=%q err=%v", contentType, err)
	}

	spoofed, err := os.CreateTemp(t.TempDir(), "spoofed-patent-*")
	if err != nil {
		t.Fatal(err)
	}
	defer spoofed.Close()
	if _, err := spoofed.Write([]byte("<html><body>not a patent PDF</body></html>")); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAgentPublicScientificStagedContent(
		spoofed, "US223898A.pdf", "application/pdf", accepted,
	); err == nil {
		t.Fatal("an HTML payload with an application/pdf header was accepted")
	}
}

func TestAgentPublicScientificFileAcceptsVersionedModelCheckpointAndRejectsHTML(t *testing.T) {
	request, err := parseAgentPublicScientificFileRequest(map[string]any{
		"url":               "https://models.example.org/releases/model.ckpt-600000",
		"human_description": "Downloading a source-attested model checkpoint",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Filename != "model.ckpt-600000" ||
		!sameStringSet(request.AcceptedTypes, []string{
			"application/octet-stream", "application/x-pytorch", "application/vnd.safetensors",
			"application/onnx", "application/x-protobuf",
		}) {
		t.Fatalf("checkpoint request=%#v", request)
	}
	if _, err := parseAgentPublicScientificFileRequest(map[string]any{
		"url": "https://models.example.org/releases/model.ckpt-600000", "filename": "model.dat",
		"human_description": "Downloading a mismatched model checkpoint",
	}); err == nil {
		t.Fatal("a versioned checkpoint URL was renamed across formats")
	}

	valid, err := os.CreateTemp(t.TempDir(), "model-checkpoint-*")
	if err != nil {
		t.Fatal(err)
	}
	defer valid.Close()
	if _, err := valid.Write(append([]byte{0x80, 0x04, 0x95, 0x08, 0, 0, 0, 0}, []byte("checkpoint-payload")...)); err != nil {
		t.Fatal(err)
	}
	contentType, err := verifyAgentPublicScientificStagedContent(
		valid, request.Filename, "application/octet-stream", request.AcceptedTypes,
	)
	if err != nil || contentType != "application/octet-stream" {
		t.Fatalf("checkpoint content_type=%q err=%v", contentType, err)
	}

	spoofed, err := os.CreateTemp(t.TempDir(), "spoofed-checkpoint-*")
	if err != nil {
		t.Fatal(err)
	}
	defer spoofed.Close()
	if _, err := spoofed.WriteString("<html><body>download denied</body></html>"); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyAgentPublicScientificStagedContent(
		spoofed, request.Filename, "application/octet-stream", request.AcceptedTypes,
	); err == nil {
		t.Fatal("an HTML response was accepted as a model checkpoint")
	}
}

func TestVerifyAgentPublicScientificStagedContentAcceptsJSONAndRejectsInvalidPayloads(t *testing.T) {
	accepted, err := agentPublicScientificAcceptedTypes("compound.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		payload string
		valid   bool
	}{
		{name: "object", payload: `{"Record":{"RecordNumber":2153,"Names":["theophylline"]}}`, valid: true},
		{name: "array", payload: `[1,2,3]`, valid: true},
		{name: "HTML spoof", payload: `<html><body>upstream error</body></html>`},
		{name: "multiple roots", payload: `{"first":true}{"second":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "scientific-json-*")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.WriteString(test.payload); err != nil {
				t.Fatal(err)
			}
			contentType, verifyErr := verifyAgentPublicScientificStagedContent(
				file, "compound.json", "application/json", accepted,
			)
			if test.valid {
				if verifyErr != nil || contentType != "application/json" {
					t.Fatalf("content_type=%q err=%v", contentType, verifyErr)
				}
			} else if verifyErr == nil {
				t.Fatalf("invalid JSON accepted as %q", contentType)
			}
		})
	}
}

func (f *agentPublicScientificFileFetcher) Fetch(
	ctx context.Context,
	rawURL string,
	policy securefetch.Policy,
) (*securefetch.Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, agentPublicScientificFetchCall{URL: rawURL, Policy: policy})
	onFetch, fetchErr, fetch := f.onFetch, f.err, f.fetch
	body, contentType := f.body, f.contentType
	f.mu.Unlock()
	if onFetch != nil {
		onFetch()
	}
	if fetch != nil {
		return fetch(ctx, rawURL, policy)
	}
	if fetchErr != nil {
		return nil, fetchErr
	}
	if contentType == "" {
		contentType = "application/gzip"
	}
	parsed, _ := url.Parse(rawURL)
	return &securefetch.Response{
		Body: io.NopCloser(strings.NewReader(body)), StatusCode: 200,
		ContentType: contentType, ContentLength: -1, FinalURL: parsed,
	}, nil
}

type agentPublicScientificUnexpectedEOFReader struct {
	payload []byte
	done    bool
}

func (r *agentPublicScientificUnexpectedEOFReader) Read(buffer []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(buffer, r.payload), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func TestAgentPublicScientificFileDownloadCreatesFreshTaskWorkspace(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fixture.server.publicScientificFiles = &agentPublicScientificFileFetcher{
		body: `{"entry":"4OGI"}`, contentType: "application/json",
	}
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://data.example.org/structures/4OGI.json"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(
		t, fixture, "fresh-workspace-source", sourceURL, false, nil,
	)
	workspaceDir := filepath.Join(fixture.projectPath, "tasks", "fresh-workspace")
	fixture.identity.workspaceDir = workspaceDir
	input := map[string]any{
		"source_tool_call_id": sourceCallID,
		"url":                 sourceURL,
		"human_description":   "Downloading a structure record into a fresh task workspace",
	}
	var progress []toolprogress.Update
	ctx := toolprogress.WithReporter(
		agentPublicScientificToolContext(t, fixture, "fresh-workspace-download", input),
		func(update toolprogress.Update) { progress = append(progress, update) },
	)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(
		ctx, fixture.identity, "fresh-workspace-download", input,
	); err != nil {
		t.Fatal(err)
	}
	ready := false
	for _, update := range progress {
		if update.Phase == "download_ready" && update.BytesCompleted != nil &&
			*update.BytesCompleted == int64(len(`{"entry":"4OGI"}`)) &&
			update.BytesTotal != nil && *update.BytesTotal == int64(len(`{"entry":"4OGI"}`)) &&
			update.BytesPerSecond != nil && *update.BytesPerSecond > 0 {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("final download progress did not retain measured transfer rate: %#v", progress)
	}
	stored, err := os.ReadFile(filepath.Join(workspaceDir, "4OGI.json"))
	if err != nil || string(stored) != `{"entry":"4OGI"}` {
		t.Fatalf("stored=%q err=%v", stored, err)
	}
}

func TestAgentPublicScientificFileDownloadResumesInterruptedTransfer(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = "cell\tgene\tcount\ncell-1\tTP53\t7\n"
	var fetchIndex int
	fetcher := &agentPublicScientificFileFetcher{}
	fetcher.fetch = func(_ context.Context, rawURL string, policy securefetch.Policy) (*securefetch.Response, error) {
		parsed, _ := url.Parse(rawURL)
		fetchIndex++
		switch fetchIndex {
		case 1:
			if policy.RangeStart != 0 || policy.IfRange != "" {
				t.Fatalf("initial policy=%#v", policy)
			}
			return &securefetch.Response{
				Body:       io.NopCloser(&agentPublicScientificUnexpectedEOFReader{payload: []byte(content[:12])}),
				StatusCode: http.StatusOK, ContentType: "application/gzip", ContentLength: int64(len(content)),
				FinalURL: parsed, ETag: `"matrix-v1"`, ContentRangeStart: -1, ContentRangeEnd: -1, ContentRangeTotal: -1,
			}, nil
		case 2:
			if policy.RangeStart != 12 || policy.IfRange != `"matrix-v1"` {
				t.Fatalf("resume policy=%#v", policy)
			}
			return &securefetch.Response{
				Body: io.NopCloser(strings.NewReader(content[12:])), StatusCode: http.StatusPartialContent,
				ContentType: "application/gzip", ContentLength: int64(len(content) - 12), FinalURL: parsed,
				ETag: `"matrix-v1"`, ContentRangeStart: 12, ContentRangeEnd: int64(len(content) - 1), ContentRangeTotal: int64(len(content)),
			}, nil
		default:
			t.Fatalf("unexpected fetch %d", fetchIndex)
			return nil, nil
		}
	}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/resumable.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "resume-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading a resumable count matrix",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "resume-download", input)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "resume-download", input); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.projectPath, "resumable.tsv.gz"))
	if err != nil || string(stored) != content || len(fetcher.callSnapshot()) != 2 {
		t.Fatalf("stored=%q calls=%#v err=%v", stored, fetcher.callSnapshot(), err)
	}
}

func TestAgentPublicScientificFileDownloadResumesAfterCancellationAndServiceBoundary(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = "cell\tgene\tcount\ncell-1\tTP53\t7\ncell-2\tEGFR\t4\n"
	prefix := []byte(content[:17])
	body := &prefixThenBlockingScientificResponseBody{
		prefix: prefix, started: make(chan struct{}), closed: make(chan struct{}),
	}
	firstFetcher := &agentPublicScientificFileFetcher{fetch: func(_ context.Context, rawURL string, policy securefetch.Policy) (*securefetch.Response, error) {
		if policy.RangeStart != 0 || policy.IfRange != "" || !policy.LongLivedTransfer || policy.TransferIdleTimeout <= 0 {
			t.Fatalf("initial durable policy=%#v", policy)
		}
		parsed, _ := url.Parse(rawURL)
		return &securefetch.Response{
			Body: body, StatusCode: http.StatusOK, ContentType: "application/gzip",
			ContentLength: int64(len(content)), FinalURL: parsed, ETag: `"dataset-v1"`,
			ContentRangeStart: -1, ContentRangeEnd: -1, ContentRangeTotal: -1,
		}, nil
	}}
	fixture.server.publicScientificFiles = firstFetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/restart-resumable.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "restart-resume-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading a restart-resumable count matrix",
	}
	callContext, cancel := context.WithCancel(agentPublicScientificToolContext(t, fixture, "restart-resume-first", input))
	firstDone := make(chan error, 1)
	go func() {
		_, err := fixture.server.executeAgentPublicScientificFileDownload(
			callContext, fixture.identity, "restart-resume-first", input,
		)
		firstDone <- err
	}()
	select {
	case <-body.started:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("initial download did not begin")
	}
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled download error=%v", err)
	}
	stagedFiles, err := filepath.Glob(filepath.Join(
		fixture.server.fileRoot, "workspace", "public-scientific-downloads", "*", "content.part",
	))
	if err != nil || len(stagedFiles) != 1 {
		t.Fatalf("durable partial files=%v err=%v", stagedFiles, err)
	}
	info, err := os.Stat(stagedFiles[0])
	if err != nil || info.Size() != int64(len(prefix)) {
		t.Fatalf("durable partial size=%v err=%v", info, err)
	}
	secondFetcher := &agentPublicScientificFileFetcher{fetch: func(_ context.Context, rawURL string, policy securefetch.Policy) (*securefetch.Response, error) {
		if policy.RangeStart != int64(len(prefix)) || policy.IfRange != `"dataset-v1"` {
			t.Fatalf("restart resume policy=%#v", policy)
		}
		parsed, _ := url.Parse(rawURL)
		return &securefetch.Response{
			Body: io.NopCloser(strings.NewReader(content[len(prefix):])), StatusCode: http.StatusPartialContent,
			ContentType: "application/gzip", ContentLength: int64(len(content) - len(prefix)), FinalURL: parsed,
			ETag: `"dataset-v1"`, ContentRangeStart: int64(len(prefix)),
			ContentRangeEnd: int64(len(content) - 1), ContentRangeTotal: int64(len(content)),
		}, nil
	}}
	// Replacing the fetcher models a fresh service instance while keeping the
	// same private FileRoot. The resumed call must derive its offset entirely
	// from durable staging metadata and bytes, not process memory.
	fixture.server.publicScientificFiles = secondFetcher
	resumeContext := agentPublicScientificToolContext(t, fixture, "restart-resume-second", input)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(
		resumeContext, fixture.identity, "restart-resume-second", input,
	); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.projectPath, "restart-resumable.tsv.gz"))
	if err != nil || string(stored) != content {
		t.Fatalf("resumed content=%q err=%v", stored, err)
	}
	stagedFiles, err = filepath.Glob(filepath.Join(
		fixture.server.fileRoot, "workspace", "public-scientific-downloads", "*", "content.part",
	))
	if err != nil || len(stagedFiles) != 0 {
		t.Fatalf("completed durable stages=%v err=%v", stagedFiles, err)
	}
}

func TestAgentPublicScientificFileDownloadAdoptsVerifiedLegacyWorkspacePartial(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = "cell\tgene\tcount\ncell-1\tTP53\t7\ncell-2\tEGFR\t4\n"
	prefix := content[:19]
	filename := "legacy-partial.tsv.gz"
	if err := os.WriteFile(filepath.Join(fixture.projectPath, filename), []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	var fetchIndex int
	fetcher := &agentPublicScientificFileFetcher{fetch: func(_ context.Context, rawURL string, policy securefetch.Policy) (*securefetch.Response, error) {
		parsed, _ := url.Parse(rawURL)
		fetchIndex++
		switch fetchIndex {
		case 1:
			if policy.PrefixBytes != int64(len(prefix)) || policy.RangeStart != 0 || policy.IfRange != "" {
				t.Fatalf("legacy verification policy=%#v", policy)
			}
			return &securefetch.Response{
				Body: io.NopCloser(strings.NewReader(prefix)), StatusCode: http.StatusPartialContent,
				ContentType: "application/gzip", ContentLength: int64(len(prefix)), FinalURL: parsed,
				LastModified: "Tue, 19 Jun 2018 00:12:01 GMT", ContentRangeStart: 0,
				ContentRangeEnd: int64(len(prefix) - 1), ContentRangeTotal: int64(len(content)),
			}, nil
		case 2:
			if policy.PrefixBytes != 0 || policy.RangeStart != int64(len(prefix)) ||
				policy.IfRange != "Tue, 19 Jun 2018 00:12:01 GMT" {
				t.Fatalf("legacy resume policy=%#v", policy)
			}
			return &securefetch.Response{
				Body: io.NopCloser(strings.NewReader(content[len(prefix):])), StatusCode: http.StatusPartialContent,
				ContentType: "application/gzip", ContentLength: int64(len(content) - len(prefix)), FinalURL: parsed,
				LastModified: "Tue, 19 Jun 2018 00:12:01 GMT", ContentRangeStart: int64(len(prefix)),
				ContentRangeEnd: int64(len(content) - 1), ContentRangeTotal: int64(len(content)),
			}, nil
		default:
			t.Fatalf("unexpected legacy partial fetch %d", fetchIndex)
			return nil, nil
		}
	}}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/" + filename
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "legacy-partial-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Resuming a verified prior partial download",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "legacy-partial-download", input)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(
		ctx, fixture.identity, "legacy-partial-download", input,
	); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.projectPath, filename))
	if err != nil || string(stored) != content || fetchIndex != 2 {
		t.Fatalf("adopted legacy content=%q fetches=%d err=%v", stored, fetchIndex, err)
	}
}

func TestAgentPublicScientificFileDownloadRestartsWhenRangeIsIgnored(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = "complete payload"
	var fetchIndex int
	fetcher := &agentPublicScientificFileFetcher{}
	fetcher.fetch = func(_ context.Context, rawURL string, policy securefetch.Policy) (*securefetch.Response, error) {
		parsed, _ := url.Parse(rawURL)
		fetchIndex++
		if fetchIndex == 1 {
			return &securefetch.Response{
				Body:       io.NopCloser(&agentPublicScientificUnexpectedEOFReader{payload: []byte(content[:5])}),
				StatusCode: http.StatusOK, ContentType: "application/gzip", ContentLength: int64(len(content)),
				FinalURL: parsed, ETag: `"old"`, ContentRangeStart: -1, ContentRangeEnd: -1, ContentRangeTotal: -1,
			}, nil
		}
		if policy.RangeStart != 5 || policy.IfRange != `"old"` {
			t.Fatalf("resume policy=%#v", policy)
		}
		return &securefetch.Response{
			Body: io.NopCloser(strings.NewReader(content)), StatusCode: http.StatusOK,
			ContentType: "application/gzip", ContentLength: int64(len(content)), FinalURL: parsed,
			ETag: `"new"`, ContentRangeStart: -1, ContentRangeEnd: -1, ContentRangeTotal: -1,
		}, nil
	}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/restarted.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "restart-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading a restarted count matrix",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "restart-download", input)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "restart-download", input); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.projectPath, "restarted.tsv.gz"))
	if err != nil || string(stored) != content {
		t.Fatalf("stored=%q err=%v", stored, err)
	}
}

func TestAgentPublicScientificFileDownloadRejectsMismatchedResumeRange(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	var fetchIndex int
	fetcher := &agentPublicScientificFileFetcher{}
	fetcher.fetch = func(_ context.Context, rawURL string, _ securefetch.Policy) (*securefetch.Response, error) {
		parsed, _ := url.Parse(rawURL)
		fetchIndex++
		if fetchIndex == 1 {
			return &securefetch.Response{
				Body:       io.NopCloser(&agentPublicScientificUnexpectedEOFReader{payload: []byte("12345")}),
				StatusCode: http.StatusOK, ContentType: "application/gzip", ContentLength: 10,
				FinalURL: parsed, ETag: `"v1"`, ContentRangeStart: -1, ContentRangeEnd: -1, ContentRangeTotal: -1,
			}, nil
		}
		return &securefetch.Response{
			Body: io.NopCloser(strings.NewReader("67890")), StatusCode: http.StatusPartialContent,
			ContentType: "application/gzip", ContentLength: 5, FinalURL: parsed,
			ETag: `"v1"`, ContentRangeStart: 4, ContentRangeEnd: 8, ContentRangeTotal: 10,
		}, nil
	}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/mismatched.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "mismatch-range-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading a range-validated count matrix",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "mismatch-range-download", input)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "mismatch-range-download", input); !errors.Is(err, errAgentPublicScientificFileResume) {
		t.Fatalf("resume mismatch error=%v", err)
	}
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
}

func (f *agentPublicScientificFileFetcher) callSnapshot() []agentPublicScientificFetchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentPublicScientificFetchCall(nil), f.calls...)
}

func TestAgentPublicScientificFileDownloadUsesAttestedLargeMCPResultAndReplays(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := "cell\tgene\tcount\ncell-1\tTP53\t7\n"
	fetcher := &agentPublicScientificFileFetcher{body: content}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "ftp://ftp.ncbi.nlm.nih.gov/geo/samples/GSM3576nnn/GSM3576396/suppl/GSM3576396_C9_R_cell-gene_UMI_table.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "geo-source-1", sourceURL, true, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading the GEO count matrix",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "public-download-1", input)
	first, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "public-download-1", input)
	if err != nil {
		t.Fatal(err)
	}
	filename := "GSM3576396_C9_R_cell-gene_UMI_table.tsv.gz"
	artifacts := agentSaveArtifactResults(t, first)
	if len(artifacts) != 1 || artifacts[0]["input_path"] != filename {
		t.Fatalf("result=%#v", first)
	}
	assertAgentArtifactResultLinks(t, artifacts[0])
	stored, err := os.ReadFile(filepath.Join(fixture.projectPath, filename))
	if err != nil || string(stored) != content {
		t.Fatalf("workspace content=%q err=%v", stored, err)
	}
	workingInputs, err := fixture.store.ListCompatibilityConversationArtifacts(
		context.Background(), fixture.stream.OwnerID, fixture.stream.ProjectID, fixture.stream.RootFrameID, false,
	)
	if err != nil || len(workingInputs) != 1 || !workingInputs[0].IsIntermediate {
		t.Fatalf("durable working inputs=%#v err=%v", workingInputs, err)
	}
	visibleArtifacts, err := fixture.store.ListCompatibilityConversationArtifacts(
		context.Background(), fixture.stream.OwnerID, fixture.stream.ProjectID, fixture.stream.RootFrameID, true,
	)
	if err != nil || len(visibleArtifacts) != 0 {
		t.Fatalf("user-visible downloaded inputs=%#v err=%v", visibleArtifacts, err)
	}
	if _, err := fixture.db.Exec(`UPDATE artifact_version_provenance SET is_intermediate=0 WHERE version_id=?`, artifacts[0]["version_id"]); err != nil {
		t.Fatal(err)
	}
	visibleArtifacts, err = fixture.store.ListCompatibilityConversationArtifacts(
		context.Background(), fixture.stream.OwnerID, fixture.stream.ProjectID, fixture.stream.RootFrameID, true,
	)
	if err != nil || len(visibleArtifacts) != 0 {
		t.Fatalf("legacy consumed input became user-visible=%#v err=%v", visibleArtifacts, err)
	}
	projectArtifacts, err := fixture.store.ListCompatibilityProjectCurrentArtifacts(
		context.Background(), fixture.stream.OwnerID, fixture.stream.ProjectID, true, 20,
	)
	if err != nil || len(projectArtifacts) != 0 {
		t.Fatalf("project tray exposed consumed input=%#v err=%v", projectArtifacts, err)
	}
	calls := fetcher.callSnapshot()
	if len(calls) != 1 || calls[0].URL != strings.Replace(sourceURL, "ftp://", "https://", 1) ||
		!sameStringSet(calls[0].Policy.AllowedHosts, []string{"ftp.ncbi.nlm.nih.gov"}) ||
		calls[0].Policy.MaxBytes != agentPublicScientificFileLimit {
		t.Fatalf("fetch calls=%#v", calls)
	}
	if err := os.Remove(filepath.Join(fixture.projectPath, filename)); err != nil {
		t.Fatal(err)
	}
	fetcher.err = errors.New("upstream unavailable during replay")
	second, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "public-download-1", input)
	if err != nil {
		t.Fatal(err)
	}
	secondArtifacts := agentSaveArtifactResults(t, second)
	if secondArtifacts[0]["version_id"] != artifacts[0]["version_id"] || len(fetcher.callSnapshot()) != 1 {
		t.Fatalf("first=%#v second=%#v calls=%#v", first, second, fetcher.callSnapshot())
	}

	// A resumed runner uses a new source event and tool-call id. The durable
	// completed receipt must still make the same logical download idempotent;
	// it must not contact an unavailable or changed upstream again.
	completedInput, _ := json.Marshal(input)
	completedResult, _ := json.Marshal(first)
	completedPayload, _ := json.Marshal(map[string]any{
		"status": "completed", "toolPhase": "completed",
		"toolName": "download_public_scientific_file", "toolCallId": "public-download-1",
		"toolInput": json.RawMessage(completedInput), "toolResult": json.RawMessage(completedResult),
	})
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "completed-public-download-before-resume",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: completedPayload, Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	resumedSourceCallID := appendAgentPublicScientificSourceCheckpoint(
		t, fixture, "geo-source-resumed", sourceURL, true, nil,
	)
	resumedInput := map[string]any{
		"source_tool_call_id": resumedSourceCallID, "url": sourceURL,
		"human_description": "Reusing the GEO count matrix after resume",
	}
	resumedContext := agentPublicScientificToolContext(t, fixture, "public-download-resumed", resumedInput)
	resumed, err := fixture.server.executeAgentPublicScientificFileDownload(
		resumedContext, fixture.identity, "public-download-resumed", resumedInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	resumedArtifacts := agentSaveArtifactResults(t, resumed)
	if resumedArtifacts[0]["version_id"] != artifacts[0]["version_id"] || len(fetcher.callSnapshot()) != 1 {
		t.Fatalf("first=%#v resumed=%#v calls=%#v", first, resumed, fetcher.callSnapshot())
	}
}

func TestAgentPublicScientificDownloadedFileCanBePromotedWithoutNewVersion(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = `{"entry":"4OGI"}`
	fixture.server.publicScientificFiles = &agentPublicScientificFileFetcher{
		body: content, contentType: "application/json",
	}
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	const sourceURL = "https://data.example.org/structures/4OGI.json"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "promote-source", sourceURL, false, nil)
	downloadInput := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading the verified structure record",
	}
	downloadResult, err := fixture.server.executeAgentPublicScientificFileDownload(
		agentPublicScientificToolContext(t, fixture, "promote-download", downloadInput),
		fixture.identity, "promote-download", downloadInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	downloaded := agentSaveArtifactResults(t, downloadResult)
	if len(downloaded) != 1 {
		t.Fatalf("download result=%#v", downloadResult)
	}

	saveInput := map[string]any{
		"files": []any{"4OGI.json"}, "language": "text",
		"destination":       map[string]any{"4OGI.json": "snapshot"},
		"human_description": "Saving the verified structure file",
	}
	savedResult, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "promote-save", saveInput), fixture.identity, "promote-save", saveInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	saved := agentSaveArtifactResults(t, savedResult)
	if len(saved) != 1 || saved[0]["version_id"] != downloaded[0]["version_id"] || saved[0]["unchanged"] != true {
		t.Fatalf("saved result=%#v downloaded=%#v", savedResult, downloadResult)
	}

	artifactID := stringValue(saved[0]["artifact_id"])
	versionID := stringValue(saved[0]["version_id"])
	var consumed, produced, versions int
	if err := fixture.db.QueryRow(
		`SELECT COUNT(*) FROM transcript_artifact_commits WHERE artifact_id=? AND version_id=? AND relation='consumed'`,
		artifactID, versionID,
	).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(
		`SELECT COUNT(*) FROM transcript_artifact_commits WHERE artifact_id=? AND version_id=? AND relation='produced'`,
		artifactID, versionID,
	).Scan(&produced); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE artifact_id=?`, artifactID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 || produced != 1 || versions != 1 {
		t.Fatalf("artifact lineage consumed=%d produced=%d versions=%d", consumed, produced, versions)
	}

	if err := fixture.store.PublishArtifactVersion(
		context.Background(), versionID, artifactID, fixture.stream.ProjectID, fixture.stream.OwnerID,
	); err != nil {
		t.Fatal(err)
	}
	visible, err := fixture.store.ListCompatibilityConversationArtifacts(
		context.Background(), fixture.stream.OwnerID, fixture.stream.ProjectID, fixture.stream.RootFrameID, true,
	)
	if err != nil || len(visible) != 1 || visible[0].VersionID != versionID || visible[0].Filename != "4OGI.json" {
		t.Fatalf("promoted file visibility=%#v err=%v", visible, err)
	}
}

func TestAgentPublicScientificFileDownloadReportsUnavailableSourceWithoutFailedExecution(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fixture.server.publicScientificFiles = &agentPublicScientificFileFetcher{
		err: &securefetch.FetchError{Code: securefetch.CodeStatus},
	}
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	const sourceURL = "https://example.org/unavailable-review.pdf"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "unavailable-source", sourceURL, true, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID,
		"url":                 sourceURL,
		"filename":            "unavailable-review.pdf",
		"human_description":   "Downloading an open-access review",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "unavailable-download", input)
	result, err := fixture.server.executeAgentPublicScientificFileDownload(
		ctx, fixture.identity, "unavailable-download", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result["sourceUnavailable"] != true || result["downloaded"] != false ||
		agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultUnavailable {
		t.Fatalf("unavailable result=%#v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.projectPath, "unavailable-review.pdf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unavailable source created a workspace file: %v", err)
	}
}

func TestAgentPublicScientificFileDownloadReportsMismatchedPayloadWithoutFailedExecution(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fixture.server.publicScientificFiles = &agentPublicScientificFileFetcher{
		body: "<html><body>upstream denial</body></html>", contentType: "application/pdf",
	}
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	const sourceURL = "https://example.org/guidance.pdf"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "mismatched-source", sourceURL, true, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL, "filename": "guidance.pdf",
		"human_description": "Downloading public guidance",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "mismatched-download", input)
	result, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "mismatched-download", input)
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "source_response_mismatch" || result["downloaded"] != false ||
		agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultUnavailable {
		t.Fatalf("mismatch result=%#v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.projectPath, "guidance.pdf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched source created a workspace file: %v", err)
	}
}

func TestAgentPublicScientificFileDownloadArchivesPatentPDFForPreview(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = "%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n"
	fetcher := &agentPublicScientificFileFetcher{body: content, contentType: "application/pdf"}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://patentimages.storage.googleapis.com/ab/cd/US223898A.pdf"
	sourceCallID := appendAgentPatentSearchSourceCheckpoint(t, fixture, "patent-source-1", sourceURL)
	input := map[string]any{
		"source_tool_call_id": sourceCallID,
		"url":                 sourceURL,
		"filename":            "US223898A.pdf",
		"human_description":   "Downloading the public electric-lamp patent",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "patent-download-1", input)
	result, err := fixture.server.executeAgentPublicScientificFileDownload(
		ctx, fixture.identity, "patent-download-1", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || artifacts[0]["filename"] != "US223898A.pdf" ||
		artifacts[0]["content_type"] != "application/pdf" {
		t.Fatalf("result=%#v", result)
	}
	stored, err := os.ReadFile(filepath.Join(fixture.projectPath, "US223898A.pdf"))
	if err != nil || string(stored) != content {
		t.Fatalf("workspace content=%q err=%v", stored, err)
	}
	if got := webScientificPreviewKind("US223898A.pdf", "application/pdf"); got != "pdf" {
		t.Fatalf("preview kind=%q", got)
	}
	calls := fetcher.callSnapshot()
	if len(calls) != 1 || calls[0].URL != sourceURL ||
		!sameStringSet(calls[0].Policy.AllowedHosts, []string{"patentimages.storage.googleapis.com"}) {
		t.Fatalf("fetch calls=%#v", calls)
	}
}

func TestAgentPublicScientificFileDownloadResolvesUniqueSourceIdentityAndAcceptsTar(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentPublicScientificFileFetcher{body: "tar payload", contentType: "application/x-tar"}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/series/GSE268nnn/GSE268626/suppl/GSE268626_RAW.tar"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "geo-directory-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": "model-invented-opaque-id",
		"url":                 sourceURL,
		"human_description":   "Downloading the GEO archive",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "public-tar-download", input)
	result, err := fixture.server.executeAgentPublicScientificFileDownload(
		ctx, fixture.identity, "public-tar-download", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetcher.callSnapshot()) != 1 {
		t.Fatalf("fetch calls=%#v", fetcher.callSnapshot())
	}
	download, _ := result["download"].(map[string]any)
	if download["source_tool_call_id"] != sourceCallID || download["source_url"] != sourceURL {
		t.Fatalf("download provenance=%#v", download)
	}
}

func TestAgentPublicScientificResultContainsURLTreatsNCBIFTPHTTPSAsEquivalent(t *testing.T) {
	ftpURL := "ftp://ftp.ncbi.nlm.nih.gov/geo/samples/GSM8679nnn/GSM8679596/suppl/GSM8679596_matrix.h5"
	httpsURL := strings.Replace(ftpURL, "ftp://", "https://", 1)
	if !agentPublicScientificResultContainsURL(map[string]any{"url": ftpURL}, httpsURL) {
		t.Fatal("the secure NCBI HTTPS transport was not recognized as equivalent to the authoritative FTP URL")
	}
	if agentPublicScientificResultContainsURL(
		map[string]any{"url": "https://untrusted.example/GSM8679596_matrix.h5"}, httpsURL,
	) {
		t.Fatal("a different host was treated as equivalent")
	}
}

func TestAgentPublicScientificFileDownloadPrefersNewestRepeatedAutoResolvedSource(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentPublicScientificFileFetcher{body: "matrix payload"}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/repeated.tsv.gz"
	appendAgentPublicScientificSourceCheckpoint(t, fixture, "ambiguous-source-1", sourceURL, false, nil)
	newestCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "ambiguous-source-2", sourceURL, false, nil)
	input := map[string]any{
		"url": sourceURL, "human_description": "Downloading a repeatedly discovered matrix",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "ambiguous-download", input)
	result, err := fixture.server.executeAgentPublicScientificFileDownload(
		ctx, fixture.identity, "ambiguous-download", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	download, _ := result["download"].(map[string]any)
	if download["source_tool_call_id"] != newestCallID || len(fetcher.callSnapshot()) != 1 {
		t.Fatalf("download=%#v calls=%#v", download, fetcher.callSnapshot())
	}
}

func TestAgentPublicScientificFileDownloadAcceptsTruncatedWebFetchAsDownloadAuthorityOnly(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/matrix.h5"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "truncated-web-fetch", sourceURL, false, func(payload map[string]any) {
		payload["toolName"] = "web_fetch"
		payload["toolResult"] = json.RawMessage(`{"ok":true,"result":{"statusCode":200,"contentType":"application/x-hdf5","body":"","url":"https://ftp.ncbi.nlm.nih.gov/geo/matrix.h5","truncated":true,"binary":true,"bytesRead":524288,"partial":true,"recovery":"use_dedicated_download_or_fulltext_tool"}}`)
	})
	request, err := parseAgentPublicScientificFileRequest(map[string]any{
		"url": sourceURL, "human_description": "Downloading the truncated HDF5 source",
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := fixture.server.validateAgentPublicScientificSourceURL(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, request,
	)
	if err != nil || resolved != sourceCallID {
		t.Fatalf("resolved=%q want=%q err=%v", resolved, sourceCallID, err)
	}

	request.SourceURL = "https://ftp.ncbi.nlm.nih.gov/geo/other.h5"
	if _, err := fixture.server.validateAgentPublicScientificSourceURL(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, request,
	); !errors.Is(err, errAgentPublicScientificFileSource) {
		t.Fatalf("truncated WebFetch authorized a different URL: %v", err)
	}
	partial := map[string]any{
		"ok": true,
		"result": map[string]any{
			"partial": true, "truncated": true,
			"recovery": "use_dedicated_download_or_fulltext_tool",
			"url":      sourceURL,
			"body":     `{"url":"https://ftp.ncbi.nlm.nih.gov/geo/untrusted-nested.csv.gz"}`,
		},
	}
	discovered := agentPublicScientificCandidatesFromResult(
		agentPublicScientificDownloadDiscoveryValue(partial), sourceCallID, 4,
	)
	if len(discovered) != 1 || discovered[0].URL != sourceURL {
		t.Fatalf("partial discovery expanded beyond exact URL: %#v", discovered)
	}
}

func TestAgentPublicScientificCandidatesAcceptCanonicalRCSBCoordinateURL(t *testing.T) {
	candidates := agentPublicScientificCandidatesFromResult(map[string]any{
		"ok": true,
		"result": map[string]any{
			"records": []any{map[string]any{
				"pdb_id": "9ODR",
				"coordinate_files": map[string]any{
					"mmcif_url": "https://files.rcsb.org/download/9ODR.cif",
				},
			}},
		},
	}, "rcsb-source-call", 4)
	if len(candidates) != 1 || candidates[0].SourceToolCallID != "rcsb-source-call" ||
		candidates[0].URL != "https://files.rcsb.org/download/9ODR.cif" {
		t.Fatalf("RCSB coordinate candidates=%#v", candidates)
	}
}

func TestAgentPublicScientificDownloadPreflightPrivatelyRejectsGuessedURL(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	canonicalURL := "https://files.rcsb.org/download/9ODR.cif"
	appendAgentPublicScientificSourceCheckpoint(t, fixture, "rcsb-coordinate-source", canonicalURL, false, nil)
	schemas := []agentruntime.ToolSchema{agentPublicScientificFileToolSchema()}
	gateway := serverAgentRuntimeToolGateway{
		server:       fixture.server,
		taskRun:      &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream}},
		allowedTools: []string{"download_public_scientific_file"},
		toolSchemas:  schemas, toolValidators: agentRuntimeToolValidators(schemas), hasToolSnapshot: true,
	}
	call := agentruntime.ToolCall{
		ID: "guessed-download", Name: "download_public_scientific_file",
		Arguments: json.RawMessage(`{"url":"https://files.rcsb.org/download/9ODR.cif.gz","filename":"9ODR.cif.gz","human_description":"Downloading coordinates"}`),
	}
	diagnostic := gateway.ToolCallPreflightDiagnostic(call)
	if !strings.Contains(diagnostic, "scientific_download_source_preflight_required") ||
		!strings.Contains(diagnostic, "Do not retry another guessed") ||
		!strings.Contains(diagnostic, "continue with the verified evidence") ||
		!strings.Contains(diagnostic, `url=\"https://files.rcsb.org/download/9ODR.cif\"`) ||
		!strings.Contains(diagnostic, `filename=\"9ODR.cif\"`) {
		t.Fatalf("guessed URL preflight diagnostic=%q", diagnostic)
	}
	call.Arguments = json.RawMessage(`{"url":"https://files.rcsb.org/download/9ODR.cif","filename":"9ODR.cif","human_description":"Downloading coordinates"}`)
	if diagnostic := gateway.ToolCallPreflightDiagnostic(call); diagnostic != "" {
		t.Fatalf("canonical durable source URL was rejected: %s", diagnostic)
	}
}

func TestAgentPublicScientificDownloadPreflightPrivatelyRejectsPathFilename(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	canonicalURL := "https://files.rcsb.org/download/9ODR.cif"
	appendAgentPublicScientificSourceCheckpoint(t, fixture, "rcsb-coordinate-source", canonicalURL, false, nil)
	schemas := []agentruntime.ToolSchema{agentPublicScientificFileToolSchema()}
	gateway := serverAgentRuntimeToolGateway{
		server:       fixture.server,
		taskRun:      &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream}},
		allowedTools: []string{"download_public_scientific_file"},
		toolSchemas:  schemas, toolValidators: agentRuntimeToolValidators(schemas), hasToolSnapshot: true,
	}
	call := agentruntime.ToolCall{
		ID: "nested-download", Name: "download_public_scientific_file",
		Arguments: json.RawMessage(`{"url":"https://files.rcsb.org/download/9ODR.cif","filename":"inputs/9ODR.cif","human_description":"Downloading coordinates"}`),
	}
	diagnostic := gateway.ToolCallPreflightDiagnostic(call)
	if !strings.Contains(diagnostic, "scientific_download_contract_preflight_required") ||
		!strings.Contains(diagnostic, "path-free filename") || !strings.Contains(diagnostic, "continue with sufficient verified evidence") {
		t.Fatalf("nested filename preflight diagnostic=%q", diagnostic)
	}
}

func TestTranscriptArtifactToolSourceRecoversStartedEventFromDurableBatch(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const toolCallID = "durable-download-source"
	input := map[string]any{
		"url":               "https://ftp.ncbi.nlm.nih.gov/geo/durable.tsv.gz",
		"human_description": "Downloading durable source data",
	}
	modelPayload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": toolCallID, "type": "function", "name": "download_public_scientific_file",
			"arguments": input,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var batch workspace.ToolCallBatch
	var items []workspace.ToolCallBatchItem
	_, _, _, err = fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "durable-download-model-call",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: modelPayload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var hookErr error
			batch, items, _, hookErr = fixture.store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if hookErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, hookErr
			}
			return transcriptstore.RunnerCheckpointCommitReceipt{ToolBatch: &transcriptstore.RunnerCheckpointToolBatchReceipt{
				BatchID: batch.BatchID, CallCount: batch.CallCount,
			}}, nil
		},
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("create batch=%#v items=%#v err=%v", batch, items, err)
	}
	batch, err = fixture.store.ClaimToolCallBatch(context.Background(), workspace.ClaimToolCallBatchInput{
		Claim: fixture.claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	startPayload, err := json.Marshal(map[string]any{
		"status": "running", "toolCallId": toolCallID,
		"toolName": "download_public_scientific_file", "toolPhase": "start", "toolInput": input,
	})
	if err != nil {
		t.Fatal(err)
	}
	var started transcriptstore.Event
	_, started, _, err = fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "durable-download-start",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: startPayload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			_, _, hookErr := fixture.store.StartToolCallBatchItemTx(ctx, tx, workspace.StartToolCallBatchItemInput{
				Claim: fixture.claim, BatchID: batch.BatchID, Ordinal: items[0].Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: items[0].StateVersion,
				StartedEvent: event,
			})
			return transcriptstore.RunnerCheckpointCommitReceipt{}, hookErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{
		Transcript:         &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
		ToolBatchIDs:       map[string]string{toolCallID: batch.BatchID},
		ToolBatchOrdinals:  map[string]int64{toolCallID: items[0].Ordinal},
		ToolSourceEventIDs: map[string]int64{},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	resolved, ok := transcriptArtifactRunFromContext(fixture.server.withTranscriptArtifactToolSource(ctx, toolCallID))
	if !ok || resolved.SourceEventID != started.EventID || run.ToolSourceEventIDs[toolCallID] != started.EventID {
		t.Fatalf("resolved=%#v ok=%t cached=%d started=%d", resolved, ok, run.ToolSourceEventIDs[toolCallID], started.EventID)
	}
}

func TestAgentPublicScientificFileDownloadAuthorizesSameOriginRelativeHTMLLink(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentPublicScientificFileFetcher{body: "tar payload", contentType: "application/x-tar"}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	baseURL := "https://ftp.ncbi.nlm.nih.gov/geo/series/GSE284nnn/GSE284191/suppl/"
	sourceURL := baseURL + "GSE284191_RAW.tar"
	if !agentPublicScientificResultContainsURL(map[string]any{
		"url": baseURL,
		"result": `<html><a href="GSE284191_RAW.tar">archive</a>` +
			`<a href="https://untrusted.example/escape.tar">escape</a></html>`,
	}, sourceURL) {
		t.Fatal("same-origin relative HTML link was not recognized by the bounded source parser")
	}
	discovered := agentPublicScientificCandidatesFromResult(map[string]any{
		"url": baseURL,
		"result": `<html><a href="GSE284191_RAW.tar">archive</a>` +
			`<a href="https://untrusted.example/escape.tar">escape</a></html>`,
	}, "geo-html-directory", 8)
	if len(discovered) != 1 || discovered[0].URL != sourceURL {
		t.Fatalf("same-origin candidates=%#v", discovered)
	}
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(
		t, fixture, "geo-html-directory", baseURL, false, func(payload map[string]any) {
			// Match the escaped representation retained by the outer durable
			// checkpoint encoder so the evidence digest covers the exact bytes.
			result := json.RawMessage(`{"ok":true,"result":{"url":"https://ftp.ncbi.nlm.nih.gov/geo/series/GSE284nnn/GSE284191/suppl/","result":"\u003chtml\u003e\u003ca href=\"GSE284191_RAW.tar\"\u003earchive\u003c/a\u003e\u003ca href=\"https://untrusted.example/escape.tar\"\u003eescape\u003c/a\u003e\u003c/html\u003e"}}`)
			payload["toolResult"] = result
			payload["resultSha256"] = kernelMCPEvidenceSHA256(result)
		},
	)
	input := map[string]any{
		"url": sourceURL, "human_description": "Downloading a GEO archive linked by the directory",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "relative-html-download", input)
	snapshot, snapshotErr := fixture.repo.GetProjectionSnapshot(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	projected, projectedErr := fixture.repo.ListProjectedCoordinateEvents(
		context.Background(), transcriptstore.ListProjectedEventsInput{
			StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, BranchID: snapshot.BranchID,
			BranchGeneration: snapshot.BranchGeneration, ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
			Limit: sessionRunnerDurableEvidencePageSize,
		},
	)
	if projectedErr != nil {
		t.Fatal(projectedErr)
	}
	foundAuthorizedSource := false
	for _, event := range projected {
		var checkpoint sessionRunnerDurableToolCheckpoint
		if json.Unmarshal(event.ResolvedPayloadJSON, &checkpoint) == nil && checkpoint.ToolCallID == sourceCallID {
			content, contentErr := fixture.server.agentPublicScientificSourceResult(
				context.Background(), fixture.stream.OwnerID, checkpoint.ToolResult,
			)
			evidenceValid := fixture.server.sessionRunnerDurableCheckpointEvidenceTool(checkpoint)
			urlAuthorized := agentPublicScientificResultContainsURL(content, sourceURL)
			if contentErr != nil || !evidenceValid || !urlAuthorized {
				t.Fatalf("projected source content=%#v err=%v evidence_valid=%t url_authorized=%t checkpoint=%#v", content, contentErr, evidenceValid, urlAuthorized, checkpoint)
			}
			foundAuthorizedSource = true
		}
	}
	if !foundAuthorizedSource {
		t.Fatal("relative HTML source checkpoint was not projected")
	}
	result, err := fixture.server.executeAgentPublicScientificFileDownload(
		ctx, fixture.identity, "relative-html-download", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	download, _ := result["download"].(map[string]any)
	calls := fetcher.callSnapshot()
	if download["source_tool_call_id"] != sourceCallID || download["source_url"] != sourceURL ||
		len(calls) != 1 || calls[0].URL != sourceURL {
		t.Fatalf("download=%#v calls=%#v", download, calls)
	}
}

func TestAgentPublicScientificFileDownloadRejectsUnattestedURLBeforeNetwork(t *testing.T) {
	for _, test := range []struct {
		name      string
		requested string
		mutate    func(map[string]any)
	}{
		{name: "different URL", requested: "https://example.org/not-authorized.tsv.gz"},
		{name: "custom connector", mutate: func(payload map[string]any) { payload["connectorSource"] = "custom" }},
		{name: "digest mismatch", mutate: func(payload map[string]any) { payload["resultSha256"] = strings.Repeat("0", 64) }},
		{name: "failed source", mutate: func(payload map[string]any) {
			failed := json.RawMessage(`{"ok":false,"error":"source failed","url":"https://ftp.ncbi.nlm.nih.gov/geo/allowed.tsv.gz"}`)
			payload["toolResult"] = failed
			payload["resultSha256"] = kernelMCPEvidenceSHA256(failed)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			fetcher := &agentPublicScientificFileFetcher{body: "unused"}
			fixture.server.publicScientificFiles = fetcher
			fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
			sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/allowed.tsv.gz"
			sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "source-"+strings.ReplaceAll(test.name, " ", "-"), sourceURL, false, test.mutate)
			requested := test.requested
			if requested == "" {
				requested = sourceURL
			}
			input := map[string]any{
				"source_tool_call_id": sourceCallID, "url": requested,
				"human_description": "Downloading a count matrix",
			}
			downloadCallID := "download-" + strings.ReplaceAll(test.name, " ", "-")
			ctx := agentPublicScientificToolContext(t, fixture, downloadCallID, input)
			if _, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, downloadCallID, input); !errors.Is(err, errAgentPublicScientificFileSource) {
				t.Fatalf("source error=%v", err)
			}
			if len(fetcher.callSnapshot()) != 0 {
				t.Fatalf("unattested request reached network: %#v", fetcher.callSnapshot())
			}
		})
	}
}

func TestAgentPublicScientificFileDownloadRejectsChecksumMismatchWithoutPublishing(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentPublicScientificFileFetcher{body: "real payload"}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/mismatch.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "checksum-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"expected_sha256":   strings.Repeat("0", 64),
		"human_description": "Downloading a count matrix",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "checksum-download", input)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "checksum-download", input); !errors.Is(err, errAgentPublicScientificFileChecksum) {
		t.Fatalf("checksum error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.projectPath, "mismatch.tsv.gz")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace residue stat error=%v", err)
	}
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
}

func TestAgentPublicScientificFileDownloadQueuesAndCancelsWithoutStartingAnotherFetch(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	var fetches atomic.Int32
	fetcher := &agentPublicScientificFileFetcher{body: "payload", onFetch: func() {
		if fetches.Add(1) == 1 {
			started <- struct{}{}
			<-release
		}
	}}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 1)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/queued.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "queue-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading a queued matrix",
	}
	ctx := agentPublicScientificToolContext(t, fixture, "queue-download", input)
	firstDone := make(chan error, 1)
	go func() {
		_, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "queue-download", input)
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first download did not start")
	}
	secondCtx := agentPublicScientificToolContext(t, fixture, "queue-download-2", input)
	waitCtx, cancel := context.WithTimeout(secondCtx, 40*time.Millisecond)
	defer cancel()
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(waitCtx, fixture.identity, "queue-download-2", input); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued cancellation error=%v", err)
	}
	if len(fetcher.callSnapshot()) != 1 {
		t.Fatalf("queued request bypassed slot: %#v", fetcher.callSnapshot())
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	secondInput := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"filename": "queued-second.tsv.gz", "human_description": "Downloading a second queued matrix",
	}
	secondCtx = agentPublicScientificToolContext(t, fixture, "queue-download-3", secondInput)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(
		secondCtx, fixture.identity, "queue-download-3", secondInput,
	); err != nil {
		t.Fatalf("slot was not released after first download: %v", err)
	}
	if len(fetcher.callSnapshot()) != 2 {
		t.Fatalf("released slot fetch calls=%#v", fetcher.callSnapshot())
	}
}

func TestAgentPublicScientificFileDownloadCancelsBlockedResponseBody(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	body := &blockingScientificResponseBody{started: make(chan struct{}), closed: make(chan struct{})}
	fixture.server.publicScientificFiles = &agentPublicScientificFileFetcher{
		fetch: func(context.Context, string, securefetch.Policy) (*securefetch.Response, error) {
			parsed, _ := url.Parse("https://ftp.ncbi.nlm.nih.gov/geo/blocked.tsv.gz")
			return &securefetch.Response{
				Body: body, StatusCode: http.StatusOK, ContentType: "application/gzip",
				ContentLength: -1, FinalURL: parsed,
			}, nil
		},
	}
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 1)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/blocked.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "blocked-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading a cancellable blocked matrix",
	}
	ctx, cancel := context.WithCancel(agentPublicScientificToolContext(t, fixture, "blocked-download", input))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "blocked-download", input)
		done <- err
	}()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("blocked response body did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked download cancellation error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked response body did not close after cancellation")
	}
}

func TestAgentPublicScientificFileDownloadRejectsCrossToolSourceIdentityBeforeNetwork(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentPublicScientificFileFetcher{body: "unused"}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	sourceURL := "https://ftp.ncbi.nlm.nih.gov/geo/cross-tool.tsv.gz"
	sourceCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "cross-tool-source", sourceURL, false, nil)
	input := map[string]any{
		"source_tool_call_id": sourceCallID, "url": sourceURL,
		"human_description": "Downloading a count matrix",
	}
	ctx := fixture.toolContext(t, "cross-tool-download", input)
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(
		ctx, fixture.identity, "cross-tool-download", input,
	); !errors.Is(err, errAgentPublicScientificFileAuthority) {
		t.Fatalf("cross-tool authority error=%v", err)
	}
	if len(fetcher.callSnapshot()) != 0 {
		t.Fatalf("cross-tool request reached network: %#v", fetcher.callSnapshot())
	}
}

func TestPublicScientificDownloadHasOneExecutableAgentSurface(t *testing.T) {
	app := &Server{publicScientificFiles: &agentPublicScientificFileFetcher{}, publicScientificDownloadSlots: make(chan struct{}, 2)}
	schemas := app.agentKernelToolSchemas(&agentKernelContext{}, map[string]struct{}{"download_public_scientific_file": {}})
	found := 0
	for _, schema := range schemas {
		if schema.Name == "download_public_scientific_file" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("download schema count=%d want=1 schemas=%#v", found, schemas)
	}
	got := app.trustedScientificReviewSignalsForCompletedTool(
		"download_public_scientific_file", nil, map[string]any{"ok": true}, nil,
	)
	if !sameStringSet(got, []string{"scientific-tool:download_public_scientific_file"}) {
		t.Fatalf("scientific signals=%#v", got)
	}
	if agentRuntimeToolNeedsApproval("download_public_scientific_file") {
		t.Fatal("retired download still participates in the live permission surface")
	}
}

func TestAgentPublicScientificFileRequestValidationRejectsUnsafeURLsAndDeliverables(t *testing.T) {
	base := map[string]any{
		"source_tool_call_id": "source-1",
		"url":                 "https://ftp.ncbi.nlm.nih.gov/geo/matrix.tsv.gz",
		"human_description":   "Downloading a count matrix",
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "private host", mutate: func(input map[string]any) { input["url"] = "https://127.0.0.1/matrix.tsv.gz" }},
		{name: "ftp outside exact NCBI host", mutate: func(input map[string]any) { input["url"] = "ftp://example.org/matrix.tsv.gz" }},
		{name: "URL query", mutate: func(input map[string]any) {
			input["url"] = "https://ftp.ncbi.nlm.nih.gov/geo/matrix.tsv.gz?token=secret"
		}},
		{name: "path traversal filename", mutate: func(input map[string]any) { input["filename"] = "../matrix.tsv.gz" }},
		{name: "HTML deliverable", mutate: func(input map[string]any) { input["filename"] = "result.html" }},
		{name: "source format mismatch", mutate: func(input map[string]any) { input["filename"] = "matrix.h5" }},
		{name: "invalid checksum", mutate: func(input map[string]any) { input["expected_sha256"] = "ABC" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := make(map[string]any, len(base))
			for key, value := range base {
				input[key] = value
			}
			test.mutate(input)
			if _, err := parseAgentPublicScientificFileRequest(input); err == nil {
				t.Fatalf("unsafe input accepted: %#v", input)
			}
		})
	}
}

func TestAgentPublicScientificFileRequestNormalizesSupportedAPIFormatEndpoint(t *testing.T) {
	const sourceURL = "https://pubchem.ncbi.nlm.nih.gov/rest/pug_view/data/compound/2153/JSON"
	for _, test := range []struct {
		name     string
		filename string
		want     string
	}{
		{name: "explicit extension", filename: "theophylline_pubchem.json", want: "theophylline_pubchem.json"},
		{name: "omitted filename", want: "2153.json"},
		{name: "format token filename", filename: "JSON", want: "2153.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{
				"url": sourceURL, "human_description": "Downloading a PubChem compound record",
			}
			if test.filename != "" {
				input["filename"] = test.filename
			}
			request, err := parseAgentPublicScientificFileRequest(input)
			if err != nil {
				t.Fatal(err)
			}
			if request.Filename != test.want || request.DownloadURL != sourceURL ||
				!sameStringSet(request.AcceptedTypes, []string{"application/json", "text/json", "text/plain", "application/octet-stream"}) {
				t.Fatalf("request=%#v", request)
			}
		})
	}
	if _, err := parseAgentPublicScientificFileRequest(map[string]any{
		"url": sourceURL, "filename": "compound.csv", "human_description": "Downloading a mismatched record",
	}); err == nil {
		t.Fatal("API format endpoint accepted a mismatched filename extension")
	}
}

func TestAgentPublicScientificFileRequestAcceptsTypedFilenameForExtensionlessDownloadRoute(t *testing.T) {
	request, err := parseAgentPublicScientificFileRequest(map[string]any{
		"url": "https://www.fda.gov/media/72238/download", "filename": "fda_starting_dose_guidance.pdf",
		"human_description": "Downloading an FDA guidance PDF",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Filename != "fda_starting_dose_guidance.pdf" || request.DownloadURL != "https://www.fda.gov/media/72238/download" ||
		!sameStringSet(request.AcceptedTypes, []string{"application/pdf", "application/octet-stream"}) {
		t.Fatalf("request=%#v", request)
	}
}

func TestAgentPublicScientificContentMismatchIsRecoverableSourceOutcome(t *testing.T) {
	request := agentPublicScientificFileRequest{
		SourceURL: "https://example.org/guidance/download", Filename: "guidance.pdf",
	}
	result, handled := agentPublicScientificSourceUnavailable(
		request, errors.New(string(securefetch.CodeContentType)),
	)
	if !handled || result["status"] != "source_response_mismatch" || result["downloaded"] != false ||
		result["code"] != "source_response_mismatch" || result["retryable"] != false {
		t.Fatalf("handled=%v result=%#v", handled, result)
	}
}

func TestAgentPublicScientificFileSourceEnvelopeDoesNotHideFailedResult(t *testing.T) {
	failed := agentPublicScientificNormalizeResultEnvelope(map[string]any{
		"ok":     false,
		"error":  "source failed",
		"result": `{"ok":true,"url":"https://ftp.ncbi.nlm.nih.gov/geo/matrix.tsv.gz"}`,
	}, 0)
	if agentruntime.ClassifyToolResult(failed) != agentruntime.ToolResultFailed {
		t.Fatalf("failed envelope classification=%s value=%#v", agentruntime.ClassifyToolResult(failed), failed)
	}
}

func TestAgentPublicScientificDownloadCandidatesRecoverDurableSourcesAndExcludeCompletedDownloads(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	firstURL := "ftp://ftp.ncbi.nlm.nih.gov/geo/series/GSE125nnn/GSE125527/suppl/GSE125527_UMI_cell_table_sparse.csv.gz"
	secondURL := "https://ftp.ncbi.nlm.nih.gov/geo/series/GSE125nnn/GSE125527/suppl/GSE125527_UMI_gene_table.csv.gz"
	firstCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "candidate-source-externalized", firstURL, true, nil)
	secondCallID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "candidate-source-inline", secondURL, false, nil)
	appendAgentPublicScientificSourceCheckpoint(t, fixture, "candidate-source-failed", "https://ftp.ncbi.nlm.nih.gov/geo/failed.csv.gz", false, func(payload map[string]any) {
		failed := json.RawMessage(`{"ok":false,"error":"source failed","url":"https://ftp.ncbi.nlm.nih.gov/geo/failed.csv.gz"}`)
		payload["toolResult"] = failed
		payload["resultSha256"] = kernelMCPEvidenceSHA256(failed)
	})
	appendAgentPublicScientificSourceCheckpoint(t, fixture, "candidate-source-prose", "https://ftp.ncbi.nlm.nih.gov/geo/not-exact.csv.gz", false, func(payload map[string]any) {
		prose := json.RawMessage(`{"ok":true,"note":"read https://ftp.ncbi.nlm.nih.gov/geo/not-exact.csv.gz next"}`)
		payload["toolResult"] = prose
		payload["resultSha256"] = kernelMCPEvidenceSHA256(prose)
	})

	candidates, err := fixture.server.agentPublicScientificDownloadCandidates(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, 8,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []agentPublicScientificDownloadCandidate{
		{SourceToolCallID: firstCallID, URL: firstURL, Filename: "GSE125527_UMI_cell_table_sparse.csv.gz", HumanDescription: "Downloading public scientific data GSE125527_UMI_cell_table_sparse.csv.gz"},
		{SourceToolCallID: secondCallID, URL: secondURL, Filename: "GSE125527_UMI_gene_table.csv.gz", HumanDescription: "Downloading public scientific data GSE125527_UMI_gene_table.csv.gz"},
	}
	if !reflect.DeepEqual(candidates, want) {
		t.Fatalf("candidates=%#v want=%#v", candidates, want)
	}
	downloadInput, _ := json.Marshal(map[string]any{
		"source_tool_call_id": firstCallID,
		"url":                 firstURL,
		"human_description":   "Downloading the count matrix",
	})
	downloadResult := json.RawMessage(`{"ok":true,"result":{"artifact_id":"downloaded"}}`)
	downloadPayload, _ := json.Marshal(map[string]any{
		"status": "completed", "toolPhase": "completed",
		"toolName": "download_public_scientific_file", "toolCallId": "completed-download",
		"toolInput": json.RawMessage(downloadInput), "toolResult": downloadResult,
	})
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "completed-public-download",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: downloadPayload, Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err = fixture.server.agentPublicScientificDownloadCandidates(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, 8,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].URL != secondURL {
		t.Fatalf("completed download was not excluded: %#v", candidates)
	}
	state, err := fixture.server.agentPublicScientificDownloadRecoveryState(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, 8,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Completed) != 1 || state.Completed[0].URL != firstURL ||
		state.Completed[0].Filename != "GSE125527_UMI_cell_table_sparse.csv.gz" {
		t.Fatalf("completed recovery state = %#v", state.Completed)
	}
	stateContext := agentPublicScientificDownloadRecoveryStateContext(state)
	if !strings.Contains(stateContext, "already present") || !strings.Contains(stateContext, "download is optional") || !strings.Contains(stateContext, firstURL) ||
		!strings.Contains(stateContext, secondURL) || strings.Contains(stateContext, "download_public_scientific_file") ||
		!strings.Contains(stateContext, "mmCIF") {
		t.Fatalf("completed recovery context = %q", stateContext)
	}

	unavailableInput, _ := json.Marshal(map[string]any{
		"source_tool_call_id": secondCallID,
		"url":                 secondURL,
		"human_description":   "Downloading the gene table",
	})
	unavailableResult := json.RawMessage(`{"sourceUnavailable":true,"status":"source_unavailable","downloaded":false,"url":"` + secondURL + `","filename":"GSE125527_UMI_gene_table.csv.gz","http_status":404}`)
	unavailablePayload, _ := json.Marshal(map[string]any{
		"status": "completed", "toolPhase": "completed",
		"toolName": "download_public_scientific_file", "toolCallId": "unavailable-download",
		"toolInput": json.RawMessage(unavailableInput), "toolResult": unavailableResult,
	})
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "unavailable-public-download",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: unavailablePayload, Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	state, err = fixture.server.agentPublicScientificDownloadRecoveryState(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, 8,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 0 || len(state.Unavailable) != 1 ||
		state.Unavailable[0].URL != secondURL || state.Unavailable[0].HTTPStatus != 404 {
		t.Fatalf("definitive unavailable source was not excluded: %#v", state)
	}
	stateContext = agentPublicScientificDownloadRecoveryStateContext(state)
	if !strings.Contains(stateContext, "must not be requested again") || !strings.Contains(stateContext, secondURL) {
		t.Fatalf("unavailable recovery context = %q", stateContext)
	}
	request, err := parseAgentPublicScientificFileRequest(map[string]any{
		"source_tool_call_id": secondCallID,
		"url":                 secondURL,
		"human_description":   "Downloading the gene table again",
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked, found, err := fixture.server.previouslyUnavailableAgentPublicScientificFileDownload(
		context.Background(), fixture.stream, request,
	)
	if err != nil || !found || blocked["code"] != "source_previously_rejected" ||
		blocked["retryable"] != false || blocked["http_status"] != int64(404) {
		t.Fatalf("durable unavailable gate: found=%v result=%#v err=%v", found, blocked, err)
	}
}

func TestRunnerRequiresPublicScientificDownloadRecoveryUsesLatestInterruption(t *testing.T) {
	entries := []eventjournal.Entry{
		{Message: map[string]any{"type": "runner_checkpoint", "status": "interrupted", "reason_code": "real_scientific_evidence_required"}},
	}
	if !runnerRequiresPublicScientificDownloadRecovery(entries) {
		t.Fatal("real-scientific interruption did not require recovery")
	}
	entries = append(entries, eventjournal.Entry{Message: map[string]any{
		"type": "runner_checkpoint", "status": "interrupted", "reason_code": "user_cancelled",
	}})
	if runnerRequiresPublicScientificDownloadRecovery(entries) {
		t.Fatal("an unrelated latest interruption inherited an older recovery requirement")
	}
	entries = append(entries, eventjournal.Entry{Message: map[string]any{
		"type": "runner_checkpoint", "status": "interrupted",
		"reason_code":   sessionRunnerModelProtocolErrorReasonCode,
		"resume_detail": "planned download_public_scientific_file call was rejected before execution",
	}})
	if !runnerRequiresPublicScientificDownloadRecovery(entries) {
		t.Fatal("legacy public-download protocol interruption did not require recovery")
	}
	entries = []eventjournal.Entry{
		{Message: map[string]any{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName": "download_public_scientific_file",
		}},
		{Message: map[string]any{
			"type": "runner_checkpoint", "status": "interrupted",
			"reason_code": sessionRunnerToolRoundNoProgressReasonCode,
		}},
	}
	if !runnerRequiresPublicScientificDownloadRecovery(entries) {
		t.Fatal("download no-progress interruption did not require recovery")
	}
	entries[0].Message["toolName"] = "web_search"
	if runnerRequiresPublicScientificDownloadRecovery(entries) {
		t.Fatal("unrelated no-progress interruption loaded download recovery")
	}
}

func appendAgentPublicScientificSourceCheckpoint(
	t *testing.T,
	fixture *agentSaveArtifactsFixture,
	toolCallID, sourceURL string,
	externalize bool,
	mutate func(map[string]any),
) string {
	t.Helper()
	toolName := "mcp__omics-archives__geo_get_series"
	toolInput := map[string]any{"accession": "GSE12345"}
	inputRaw, _ := json.Marshal(toolInput)
	resultRaw, _ := json.Marshal(map[string]any{
		"ok": true, "result": map[string]any{
			"accession":           "GSE12345",
			"supplementary_files": []any{map[string]any{"url": sourceURL}},
		},
	})
	toolResult := json.RawMessage(resultRaw)
	if externalize {
		digest := sha256.Sum256([]byte(toolName + "\x00" + toolCallID))
		record, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
			ArtifactID: "large-tool-result-" + hex.EncodeToString(digest[:16]),
			ProjectID:  fixture.stream.ProjectID, RootFrameID: fixture.stream.RootFrameID,
			FrameID: fixture.stream.FrameID, StreamUID: fixture.stream.UID,
			OwnerUserID: fixture.stream.OwnerID, RunnerID: fixture.claim.RunnerID,
			ClaimToken: fixture.claim.ClaimToken, Attempt: fixture.claim.Attempt,
			SourceEventID: 1, ToolName: toolName, ToolCallID: toolCallID, Content: resultRaw,
		})
		if err != nil {
			t.Fatal(err)
		}
		descriptor, _ := json.Marshal(map[string]any{
			"artifact_id": record.ArtifactID, "version_id": record.VersionID,
			"sha256": record.ContentSHA256, "size_bytes": record.SizeBytes,
			"content_type": "application/json", "outcome": "succeeded",
			"content_url": "/api/artifacts/" + record.ArtifactID + "/versions/" + record.VersionID,
			"preview":     "externalized scientific source result", "truncated": true,
		})
		toolResult = descriptor
	}
	schemaRaw := []byte(`{"type":"object","properties":{"accession":{"type":"string"}}}`)
	payload := map[string]any{
		"schema": workspaceMCPSourceEvidenceSchemaV1,
		"status": "completed", "toolPhase": "completed",
		"toolName": toolName, "toolCallId": toolCallID,
		"toolInput": json.RawMessage(inputRaw), "toolResult": toolResult,
		"evidenceClass": workspace.KernelMCPEvidenceClassBundledReadOnly,
		"connectorId":   "bundled:omics-archives", "connectorSource": "bundled", "readOnlyHint": true,
		"inputSchemaSha256": kernelMCPEvidenceSHA256(schemaRaw),
		"requestSha256":     kernelMCPEvidenceSHA256(inputRaw),
		"resultSha256":      kernelMCPEvidenceSHA256(toolResult),
	}
	if mutate != nil {
		mutate(payload)
	}
	rawPayload, _ := json.Marshal(payload)
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "public-source-" + toolCallID,
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: rawPayload, Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	return toolCallID
}

func appendAgentPatentSearchSourceCheckpoint(
	t *testing.T,
	fixture *agentSaveArtifactsFixture,
	toolCallID, sourceURL string,
) string {
	t.Helper()
	inputRaw, _ := json.Marshal(map[string]any{
		"operation":          "resolve_download",
		"publication_number": "US223898A",
		"sources":            []string{"google"},
	})
	resultRaw, _ := json.Marshal(map[string]any{
		"operation":         "resolve_download",
		"publicationNumber": "US223898A",
		"records":           []any{},
		"sourceStatuses":    []any{},
		"downloads": []any{map[string]any{
			"source": "google_patents", "state": "resolved", "downloadUrl": sourceURL,
		}},
	})
	rawPayload, _ := json.Marshal(map[string]any{
		"status": "completed", "toolPhase": "completed",
		"toolName": "patent_search", "toolCallId": toolCallID,
		"toolInput": json.RawMessage(inputRaw), "toolResult": json.RawMessage(resultRaw),
	})
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "patent-source-" + toolCallID,
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: rawPayload, Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	return toolCallID
}

func agentPublicScientificToolContext(
	t *testing.T,
	fixture *agentSaveArtifactsFixture,
	toolCallID string,
	input map[string]any,
) context.Context {
	t.Helper()
	rawInput, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	rawPayload, err := json.Marshal(map[string]any{
		"toolCallId": toolCallID, "toolName": "download_public_scientific_file",
		"toolPhase": "start", "toolInput": json.RawMessage(rawInput),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, source, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "public-download-source-" + toolCallID,
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: rawPayload, Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return withTranscriptArtifactRun(
		context.Background(), &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}, source.EventID,
	)
}
