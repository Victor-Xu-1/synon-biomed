package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/tools/securefetch"
)

func TestPublicDownloadRetainedProgressDoesNotExhaustLifetimeRetries(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		name := "disconnected-stream"
		if segmented {
			name = "bounded-range-responses"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			const payload = "sample\tvalue\n" + "one\t1111111111\n" + "two\t2222222222\n" + "three\t3333333333\n"
			const source = "https://data.example.org/measurements.tsv"
			calls := 0
			fetcher := &agentPublicScientificFileFetcher{fetch: func(ctx context.Context, raw string, policy securefetch.Policy) (*securefetch.Response, error) {
				calls++
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				start := int(policy.RangeStart)
				end := min(len(payload), start+7)
				if start < 0 || start >= len(payload) || (start > 0 && policy.IfRange != `"measurement-v1"`) {
					t.Fatalf("invalid retained continuation: %#v", policy)
				}
				final, _ := url.Parse(raw)
				response := &securefetch.Response{FinalURL: final, StatusCode: http.StatusOK,
					ContentType: "text/tab-separated-values", ContentLength: int64(len(payload)), ETag: `"measurement-v1"`,
					ContentRangeStart: -1, ContentRangeEnd: -1, ContentRangeTotal: -1}
				if start > 0 {
					response.StatusCode = http.StatusPartialContent
					response.ContentRangeStart, response.ContentRangeEnd, response.ContentRangeTotal = int64(start), int64(len(payload)-1), int64(len(payload))
					response.ContentLength = int64(len(payload) - start)
				}
				if segmented && start > 0 {
					response.ContentRangeEnd, response.ContentLength = int64(end-1), int64(end-start)
					response.Body = io.NopCloser(strings.NewReader(payload[start:end]))
				} else if end < len(payload) {
					response.Body = io.NopCloser(&agentPublicScientificUnexpectedEOFReader{payload: []byte(payload[start:end])})
				} else {
					response.Body = io.NopCloser(strings.NewReader(payload[start:]))
				}
				return response, nil
			}}
			fixture.server.publicScientificFiles = fetcher
			fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
			callID := appendAgentPublicScientificSourceCheckpoint(t, fixture, "retained-progress-source", source, false, nil)
			input := map[string]any{"url": source, "source_tool_call_id": callID, "human_description": "Acquiring a complete dataset"}
			ctx, cancel := context.WithTimeout(agentPublicScientificToolContext(t, fixture, "retained-progress-download", input), 15*time.Second)
			defer cancel()
			result, err := fixture.server.executeAgentPublicScientificFileDownload(ctx, fixture.identity, "retained-progress-download", input)
			if err != nil {
				t.Fatalf("advancing transfer stopped after %d connections: %v", calls, err)
			}
			if calls <= 4 || len(agentSaveArtifactResults(t, result)) != 1 {
				t.Fatalf("connections=%d result=%#v", calls, result)
			}
			stored, err := os.ReadFile(filepath.Join(fixture.projectPath, "measurements.tsv"))
			if err != nil || string(stored) != payload {
				t.Fatalf("stored=%q err=%v", stored, err)
			}
		})
	}
}

func TestPublicDownloadRepeatedPrefixIsNotRetainedProgress(t *testing.T) {
	for _, validator := range []string{"", `"unchanged"`} {
		t.Run("validator="+validator, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			calls := 0
			fixture.server.publicScientificFiles = &agentPublicScientificFileFetcher{fetch: func(_ context.Context, raw string, _ securefetch.Policy) (*securefetch.Response, error) {
				calls++
				final, _ := url.Parse(raw)
				return &securefetch.Response{FinalURL: final, StatusCode: http.StatusOK, ContentType: "text/plain", ContentLength: 100,
					ETag: validator, Body: io.NopCloser(&agentPublicScientificUnexpectedEOFReader{payload: []byte("same prefix")})}, nil
			}}
			request, err := parseAgentPublicScientificFileRequest(map[string]any{"url": "https://data.example.org/stalled.txt", "human_description": "Acquiring a file"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _, err = fixture.server.fetchAndStageAgentPublicScientificFile(ctx, fixture.projectPath, request)
			if err == nil || errors.Is(err, context.DeadlineExceeded) || calls > 5 {
				t.Fatalf("repeated prefix bypassed no-progress budget: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestPublicDownloadChangingRepresentationsCannotResetStallBudget(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	calls := 0
	fixture.server.publicScientificFiles = &agentPublicScientificFileFetcher{fetch: func(_ context.Context, raw string, _ securefetch.Policy) (*securefetch.Response, error) {
		calls++
		final, _ := url.Parse(raw)
		// If-Range may legitimately return a replacement representation with
		// 200. Increasing prefixes from different versions are not cumulative
		// progress toward one complete file.
		return &securefetch.Response{FinalURL: final, StatusCode: http.StatusOK, ContentType: "text/plain", ContentLength: 1000,
			ETag: fmt.Sprintf("\"revision-%d\"", calls),
			Body: io.NopCloser(&agentPublicScientificUnexpectedEOFReader{payload: []byte(strings.Repeat("x", calls))})}, nil
	}}
	request, err := parseAgentPublicScientificFileRequest(map[string]any{"url": "https://data.example.org/changing.txt", "human_description": "Acquiring a file"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	_, _, err = fixture.server.fetchAndStageAgentPublicScientificFile(ctx, fixture.projectPath, request)
	var interrupted *agentPublicScientificTransferInterrupted
	if !errors.As(err, &interrupted) || interrupted.StalledAttempts != agentPublicScientificDownloadMaxAttempts || calls != 1+agentPublicScientificDownloadMaxAttempts {
		t.Fatalf("replacement source bypassed stall accounting: calls=%d failure=%#v err=%v", calls, interrupted, err)
	}
}
