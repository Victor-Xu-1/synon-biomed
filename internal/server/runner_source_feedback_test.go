package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/httptext"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/webfetch"
)

func TestSourceFeedbackCapturedOriginals(t *testing.T) {
	paths := filepath.SplitList(os.Getenv("SYNON_TEST_SOURCE_BLOBS"))
	if len(paths) == 0 {
		t.Skip("no captured original sources selected")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			input := agentruntime.LargeToolResultInput{
				ToolCall: agentruntime.ToolCall{ID: "captured-source", Name: "source_read"},
				RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 16000,
			}
			record := workspace.RunnerLargeToolResult{
				ArtifactID: "large-tool-result-" + strings.Repeat("d", 32), VersionID: "ltr-captured-source",
				ContentType: "application/json", SizeBytes: int64(len(raw)), ContentSHA256: hex.EncodeToString(digest[:]),
			}
			started := time.Now()
			descriptor, err := buildRunnerLargeToolResultDescriptor(context.Background(), input, record)
			if err != nil {
				t.Fatal(err)
			}
			if err := (runnerLargeToolResultAuthority{}).ValidatePreview(context.Background(), input, descriptor); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(descriptor)
			var view map[string]any
			if json.Unmarshal([]byte(descriptor.Preview), &view) != nil || stringValue(view["content"]) == "" ||
				len(encoded) > 16000 || descriptor.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatal("captured source failed first-feedback contract")
			}
			replay, err := compactRunnerLargeToolResultDescriptorForReplay(string(encoded))
			if err != nil || replay != string(encoded) {
				t.Fatal("captured source replay changed the reading page")
			}
			t.Logf("raw_bytes=%d feedback_bytes=%d format=%s lines=%v next=%v render_and_validate=%s",
				len(raw), len(encoded), view["view_format"], view["showing_lines"], view["next_offset"], time.Since(started))
			var envelope struct {
				Result webfetch.Result `json:"result"`
			}
			if json.Unmarshal(raw, &envelope) == nil && view["view_format"] == "html-readable-display-lines" {
				doc, err := httptext.HTMLDocument(context.Background(), envelope.Result.Body, envelope.Result.URL)
				if err != nil {
					t.Fatal(err)
				}
				first := stringValue(view["content"])
				var combined strings.Builder
				combined.WriteString(first)
				for view["truncated"] == true {
					next := int(numberValue(view["next_offset"]))
					page, err := readAgentWorkspaceFile(withAgentWorkspaceReadBudget(context.Background(), 16000), bytes.NewReader(raw), "source.json", "application/json", int64(len(raw)), map[string]any{"version_id": record.VersionID, "offset": next})
					if err != nil {
						t.Fatal(err)
					}
					view = mapValue(page)
					combined.WriteString(stringValue(view["content"]))
					if view["truncated"] == true && int(numberValue(view["next_offset"])) <= next {
						t.Fatal("captured source cursor stalled")
					}
				}
				visible := 0
				for _, link := range doc.Links {
					if strings.Contains(first, link.Relation) {
						visible++
					}
					if !strings.Contains(combined.String(), link.Relation) {
						t.Fatalf("publisher link relation disappeared: %s", link.Relation)
					}
				}
				t.Logf("publisher_links=%d first_feedback_link_relations=%d metadata_records=%d full_read_complete=true", len(doc.Links), visible, len(doc.Metadata))
			}
		})
	}
}

func TestLargeSearchFeedbackContinuesThroughSameSourceView(t *testing.T) {
	sources := make([]map[string]any, 0, 50)
	for i := 0; i < 50; i++ {
		sources = append(sources, map[string]any{
			"kind": "search_result", "evidenceState": "discovered",
			"title": fmt.Sprintf("Record-%03d", i), "url": fmt.Sprintf("https://example.test/%d", i),
			"snippet": strings.Repeat("Quoted \"original\" data 科学. ", 25),
		})
	}
	raw, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"query": "Generic records", "sources": sources,
		"retrieval": map[string]any{"returned": 50, "exhaustive": false},
	}})
	digest := sha256.Sum256(raw)
	descriptor, err := buildRunnerLargeToolResultDescriptor(context.Background(), agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "search-feedback", Name: "web_search"},
		RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 16000,
	}, workspace.RunnerLargeToolResult{
		ArtifactID: "large-tool-result-" + strings.Repeat("b", 32), VersionID: "ltr-search-feedback",
		ContentType: "application/json", SizeBytes: int64(len(raw)), ContentSHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(descriptor)
	if len(encoded) > 16000 {
		t.Fatalf("first feedback exceeds serialized transport: %d", len(encoded))
	}
	replayed, err := compactRunnerLargeToolResultDescriptorForReplay(string(encoded))
	if err != nil || replayed != string(encoded) || !json.Valid([]byte(descriptor.Preview)) {
		t.Fatalf("replay destroyed the structured reading cursor: %v", err)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(descriptor.Preview), &view); err != nil {
		t.Fatal(err)
	}
	alteredView := copyMapAny(view)
	alteredView["next_offset"] = numberValue(view["next_offset"]) + 1
	altered, _ := json.Marshal(alteredView)
	tampered := descriptor
	tampered.Preview = string(altered)
	if err := (runnerLargeToolResultAuthority{}).ValidatePreview(context.Background(), agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "search-feedback", Name: "web_search"},
		RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 16000,
	}, tampered); err == nil {
		t.Fatal("tampered source continuation was accepted")
	}
	if view["source_state"] != "discovered" || view["truncated"] != true ||
		mapValue(view["read_with"])["version_id"] != descriptor.VersionID {
		t.Fatalf("discovery depth or next read lost: %#v", view)
	}
	var text strings.Builder
	text.WriteString(stringValue(view["content"]))
	next := int(numberValue(view["next_offset"]))
	for next > 0 {
		result, err := readAgentWorkspaceFile(withAgentWorkspaceReadBudget(context.Background(), 16000),
			bytes.NewReader(raw), "search.json", "application/json", int64(len(raw)),
			map[string]any{"version_id": descriptor.VersionID, "offset": next})
		if err != nil {
			t.Fatal(err)
		}
		view = mapValue(result)
		text.WriteString(stringValue(view["content"]))
		following := int(numberValue(view["next_offset"]))
		if view["truncated"] != true {
			break
		}
		if following <= next {
			t.Fatal("continuation did not advance")
		}
		next = following
	}
	for i := 0; i < 50; i++ {
		if strings.Count(text.String(), fmt.Sprintf("Record-%03d", i)) != 1 {
			t.Fatalf("record %d lost or duplicated at first-feedback/read_file boundary", i)
		}
	}
}

func TestLargeWebResearchFeedbackSurfacesRetrievedDocumentsOnFirstPage(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"candidateSources": []any{map[string]any{
			"title": "discovery candidate", "snippet": strings.Repeat("discovery only ", 24000),
		}},
		"documents": []any{map[string]any{
			"url": "https://example.test/primary", "content": strings.Repeat("DECISION_RELEVANT_PRIMARY_RESULT ", 800),
			"readReceipt": map[string]any{"deepRead": true},
		}},
		"quality": map[string]any{"deepReadSources": 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	input := agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "web-research-source", Name: "web_research"},
		RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: runnerLargeToolResultInlineLimitBytes,
	}
	record := workspace.RunnerLargeToolResult{
		ArtifactID: "large-tool-result-" + strings.Repeat("e", 32), VersionID: "ltr-web-research-source",
		ContentType: "application/json", SizeBytes: int64(len(raw)), ContentSHA256: hex.EncodeToString(digest[:]),
	}
	descriptor, err := buildRunnerLargeToolResultDescriptor(context.Background(), input, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := (runnerLargeToolResultAuthority{}).ValidatePreview(context.Background(), input, descriptor); err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	if json.Unmarshal([]byte(descriptor.Preview), &view) != nil || view["view_format"] != "json-field-preview" ||
		view["source_content_included"] != true || !strings.Contains(descriptor.Preview, "DECISION_RELEVANT_PRIMARY_RESULT") {
		t.Fatalf("first feedback did not surface retrieved document content: %#v", view)
	}
	foundDocuments := false
	for _, rawSection := range anySliceValue(view["sections"]) {
		section := mapValue(rawSection)
		if section["json_pointer"] == "/result/documents" {
			foundDocuments = true
			if mapValue(section["read_with"])["json_pointer"] != "/result/documents" {
				t.Fatalf("retrieved document section lost its exact read handle: %#v", section)
			}
		}
	}
	if !foundDocuments || !runnerSourceFeedbackPreview(toolcontract.ExternalizedResultDescriptor{
		VersionID: descriptor.VersionID, SizeBytes: descriptor.SizeBytes, Preview: descriptor.Preview,
	}) {
		t.Fatal("retrieved document preview was not retained as readable source feedback")
	}
}

func TestLargeSourceFeedbackRealHTTPPersistenceAndReadback(t *testing.T) {
	body := "<html><head><style>" + strings.Repeat(".asset{}", 5000) +
		`</style><meta name="citation_pdf_url" content="/download.pdf"><meta name="citation_doi" content="10.1234/dataset"></head><body><h1>Dataset report</h1><table><tr><td>Value</td><td>35</td></tr></table></body></html>`
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()
	fetched, err := webfetch.FetchWithOptions(context.Background(), origin.URL, 1<<20, webfetch.Options{
		ClientForURL: func(context.Context, string) (*http.Client, error) { return origin.Client(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultSource(t, fixture, "source-feedback-call", "web_fetch")
	engine := fixture.server.newAgentRuntimeEngineWithContext(ctx, SessionRunnerChatOptions{
		SessionID: fixture.stream.SessionID, OutputLimitBytes: 50000,
	})
	value := map[string]any{"ok": true, "result": fetched}
	raw, _ := json.Marshal(value)
	materialized, err := engine.MaterializeToolResult(ctx,
		agentruntime.ToolCall{ID: "source-feedback-call", Name: "web_fetch"}, value, agentruntime.ToolResultSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor agentruntime.LargeToolResultDescriptor
	if err := json.Unmarshal(materialized.JSON, &descriptor); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(descriptor.Preview, "Dataset report") || strings.Contains(descriptor.Preview, ".asset{}") {
		t.Fatal("real tool feedback still consists of HTML assets")
	}
	if !strings.Contains(descriptor.Preview, origin.URL+"/download.pdf") || !strings.Contains(descriptor.Preview, "10.1234/dataset") {
		t.Fatal("first feedback lost publisher document link or bibliography")
	}
	assertLargeResultExactContentHTTP(t, fixture.server.Handler(), fixture.stream.OwnerID, descriptor, raw)
	reopened, err := workspace.Open(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	repository, err := reopened.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(Options{Workspace: reopened, Transcript: repository, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = restarted.Close(closeCtx)
	})
	recovery := restarted.newAgentRuntimeEngineWithContext(withTranscriptRunnerChatRun(context.Background(), run),
		SessionRunnerChatOptions{SessionID: fixture.stream.SessionID, OutputLimitBytes: 50000})
	replayed, err := recovery.MaterializeRawToolResult(withTranscriptRunnerChatRun(context.Background(), run),
		agentruntime.ToolCall{ID: "source-feedback-call", Name: "web_fetch"}, raw, agentruntime.ToolResultSucceeded)
	if err != nil || !bytes.Equal(replayed.JSON, materialized.JSON) || replayed.SHA256 != materialized.SHA256 {
		t.Fatalf("live/recovery source feedback diverged: %v", err)
	}
	read, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file",
		map[string]any{"version_id": descriptor.VersionID, "json_pointer": "/result/body"})
	if err != nil || !strings.Contains(stringValue(mapValue(read)["content"]), "<html>") {
		t.Fatalf("raw source was no longer readable: %v", err)
	}
}

func TestLargeSourceFeedbackCancellationAndUnavailableRemainDistinct(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		raw, _ := json.Marshal(map[string]any{"ok": true, "result": webfetch.Result{
			StatusCode: 200, ContentType: "text/html", URL: "https://example.test/data",
			SourceUnavailable: unavailable, Body: "<p>" + strings.Repeat("Original source ", 3000) + "</p>",
		}})
		digest := sha256.Sum256(raw)
		record := workspace.RunnerLargeToolResult{
			ArtifactID: "large-tool-result-" + strings.Repeat("c", 32), VersionID: "ltr-unavailable",
			ContentType: "application/json", SizeBytes: int64(len(raw)), ContentSHA256: hex.EncodeToString(digest[:]),
		}
		input := agentruntime.LargeToolResultInput{
			RawJSON: raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 16000,
		}
		if unavailable {
			input.Outcome = agentruntime.ToolResultUnavailable
			result, err := buildRunnerLargeToolResultDescriptor(context.Background(), input, record)
			if err != nil || result.Outcome != agentruntime.ToolResultUnavailable ||
				strings.Contains(result.Preview, "html-readable-display-lines") {
				t.Fatalf("unavailable result was promoted to readable evidence: %v", err)
			}
		} else {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := buildRunnerLargeToolResultDescriptor(ctx, input, record); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled view continued: %v", err)
			}
			input.MaxInlineBytes = 512
			result, err := buildRunnerLargeToolResultDescriptor(context.Background(), input, record)
			if err != nil || result.VersionID != record.VersionID {
				t.Fatalf("small display budget lost the durable source: %v", err)
			}
		}
	}
}

func TestLargeHTTPFeedbackPresentsReadableContentAndImmutableSource(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"ok": true,
		"result": webfetch.Result{
			StatusCode: 200, ContentType: "text/html", URL: "https://example.test/study",
			Body: "<html><head><script>" + strings.Repeat("ignoredAsset();", 4000) +
				"</script></head><body><h1>Example study</h1><p>Observed result: 27 units.</p>" +
				"<a href='/study.pdf'>Study PDF</a></body></html>",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	descriptor, err := buildRunnerLargeToolResultDescriptor(context.Background(), agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "readable-feedback", Name: "web_fetch"},
		RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 16000,
	}, workspace.RunnerLargeToolResult{
		ArtifactID: "large-tool-result-" + strings.Repeat("a", 32), VersionID: "ltr-readable-feedback",
		ContentType: "application/json", SizeBytes: int64(len(raw)), ContentSHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Observed result: 27 units.", "https://example.test/study.pdf", "html-readable-display-lines"} {
		if !strings.Contains(descriptor.Preview, required) {
			t.Errorf("first feedback lacks %q: %s", required, descriptor.Preview)
		}
	}
	if strings.Contains(descriptor.Preview, "ignoredAsset") || descriptor.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("readable feedback exposed assets or changed original source identity")
	}
}
