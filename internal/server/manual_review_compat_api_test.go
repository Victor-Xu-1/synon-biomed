package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestManualFrameAuditRunsOnceAfterAskUserWithoutEnablingAutoReview(t *testing.T) {
	var mainCalls atomic.Int32
	var reviewerCalls atomic.Int32
	reviewerStarted := make(chan struct{})
	releaseReviewer := make(chan struct{})
	var startOnce sync.Once
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		isReviewer := len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "You are the REVIEWER")
		w.Header().Set("Content-Type", "application/json")
		if !isReviewer {
			mainCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "Completed evidence-backed RIPK1 analysis.",
			}}}})
			return
		}
		reviewerCalls.Add(1)
		hasToolResult := false
		for _, message := range request.Messages {
			if strings.TrimSpace(message.ToolCallID) != "" {
				hasToolResult = true
				break
			}
		}
		if !hasToolResult {
			startOnce.Do(func() { close(reviewerStarted) })
			<-releaseReviewer
			review := `{"human_description":"The completed answer addresses the requested analysis.","findings":[]}`
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
					"id": "manual-review-submit", "type": "function", "function": map[string]any{
						"name": sessionReviewerSubmitToolName, "arguments": review,
					},
				}},
			}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "content": "review submitted",
		}}}})
	}))
	defer modelAPI.Close()

	store, server := newManualReviewFixture(t, modelAPI.URL, "off", "Perform a rigorous evidence-backed RIPK1 analysis.")
	streamBeforeAnswer, found, err := server.transcriptStore.GetFrameStreamBySession(
		context.Background(), "owner", "root",
	)
	if err != nil || !found {
		t.Fatalf("stream before Ask User answer=%#v found=%v err=%v", streamBeforeAnswer, found, err)
	}
	if _, _, created, err := server.transcriptStore.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: streamBeforeAnswer.UID, OwnerID: streamBeforeAnswer.OwnerID,
		ClientMessageID: "ask-user-answer-client", FrameEventID: "ask-user-answer-event",
		MessageUUID: "ask-user-answer-message", Text: "Use the evidence-backed option.", MessageOrigin: "input_response",
	}); err != nil || !created {
		t.Fatalf("append Ask User answer created=%v err=%v", created, err)
	}
	intent, found, err := server.transcriptStore.EnsureActiveFrameTaskIntent(
		context.Background(), streamBeforeAnswer.UID, streamBeforeAnswer.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("task intent=%#v found=%v err=%v", intent, found, err)
	}
	streamAfterAnswer, found, err := server.transcriptStore.GetFrameStreamBySession(
		context.Background(), "owner", "root",
	)
	if err != nil || !found || intent.Revision != 1 || streamAfterAnswer.InputRevision != 2 {
		t.Fatalf("intent=%#v stream after answer=%#v found=%v err=%v", intent, streamAfterAnswer, found, err)
	}
	main := runManualReviewFixtureMain(t, server, modelAPI.URL)
	if main.Status != "completed" || mainCalls.Load() != 1 || reviewerCalls.Load() != 0 {
		t.Fatalf("main=%#v mainCalls=%d reviewerCalls=%d", main, mainCalls.Load(), reviewerCalls.Load())
	}
	if frames, err := store.ListFramesForRoot("root"); err != nil || len(frames) != 1 {
		t.Fatalf("automatic review ran while disabled: frames=%#v err=%v", frames, err)
	}
	stream, found, err := server.transcriptStore.GetFrameStreamBySession(context.Background(), "owner", "root")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%v err=%v", stream, found, err)
	}
	state, found, err := server.transcriptStore.GetLatestRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("state=%#v found=%v err=%v", state, found, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: transcriptstore.RunnerClaim{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: state.RunnerID, Attempt: state.Attempt,
		ClaimedInputRevision: state.ClaimedInputRevision, ResumeSource: state.ResumeSource,
	}}
	entries, err := server.loadTranscriptRunnerReplay(context.Background(), authority, 100, 100)
	if err != nil || latestManualReviewCandidate(entries) == "" {
		t.Fatalf("replay candidate missing: entries=%#v err=%v", entries, err)
	}

	app := server.Handler()
	first := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/audit", "owner", map[string]any{}, http.StatusAccepted)
	reviewerFrameID := stringValue(first["frame_id"])
	if reviewerFrameID == "" || first["root_frame_id"] != "root" {
		t.Fatalf("manual audit response=%#v", first)
	}
	select {
	case <-reviewerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("manual reviewer did not start")
	}
	runningProjection := compatJSONRequest(t, app, http.MethodGet, "/api/frames/root", "owner", nil, http.StatusOK)
	if runningProjection["runtime_stage"] != "reviewing" || runningProjection["runtime_review_status"] != "processing" ||
		runningProjection["runtime_review_trigger"] != "manual" || runningProjection["runtime_active"] != false {
		t.Fatalf("running manual review projection=%#v", runningProjection)
	}
	second := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/audit", "owner", map[string]any{}, http.StatusAccepted)
	if second["frame_id"] != reviewerFrameID {
		t.Fatalf("repeated manual audit created another frame: first=%#v second=%#v", first, second)
	}
	close(releaseReviewer)
	waitForManualReviewerStatus(t, store, reviewerFrameID, "completed")
	completedProjection := compatJSONRequest(t, app, http.MethodGet, "/api/frames/root", "owner", nil, http.StatusOK)
	if completedProjection["runtime_stage"] != "review_completed" || completedProjection["runtime_review_status"] != "completed" ||
		completedProjection["runtime_review_verdict"] != "pass" ||
		numberValue(completedProjection["runtime_review_issue_count"]) != 0 ||
		numberValue(completedProjection["runtime_review_blocking_issue_count"]) != 0 ||
		completedProjection["runtime_active"] != false {
		t.Fatalf("completed manual review projection=%#v", completedProjection)
	}

	root, found, err := store.GetFrame("root")
	if err != nil || !found || root.Status != "completed" {
		t.Fatalf("manual review changed completed root: frame=%#v found=%v err=%v", root, found, err)
	}
	config, found, err := server.transcriptStore.LatestFrameRuntimeConfig(context.Background(), "frame:root", "owner")
	if err != nil || !found || config["verifier_mode"] != "off" {
		t.Fatalf("manual review changed auto-review config: config=%#v found=%v err=%v", config, found, err)
	}
	checks, err := store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 1 || checks[0].ReviewerFrameID == nil || *checks[0].ReviewerFrameID != reviewerFrameID ||
		checks[0].Verdict != "pass" || checks[0].Status != "resolved" {
		t.Fatalf("manual review checks=%#v err=%v", checks, err)
	}
	if reviewerCalls.Load() != 1 {
		t.Fatalf("reviewerCalls=%d, want one terminal submit_output round", reviewerCalls.Load())
	}
}

func TestManualFrameAuditReviewsTheFullRootSession(t *testing.T) {
	const (
		firstTask    = "FIRST_TASK_MARKER: establish the assay baseline."
		firstResult  = "FIRST_RESULT_MARKER: baseline evidence was recorded."
		secondTask   = "SECOND_TASK_MARKER: compare the follow-up result."
		secondResult = "SECOND_RESULT_MARKER: follow-up comparison is complete."
	)
	var mainCalls atomic.Int32
	var reviewerCalls atomic.Int32
	var promptMu sync.Mutex
	reviewerPrompt := ""
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		isReviewer := len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "You are the REVIEWER")
		w.Header().Set("Content-Type", "application/json")
		if !isReviewer {
			content := firstResult
			if mainCalls.Add(1) > 1 {
				content = secondResult
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": content,
			}}}})
			return
		}
		reviewerCalls.Add(1)
		promptMu.Lock()
		for _, message := range request.Messages {
			reviewerPrompt += "\n" + message.Content
		}
		promptMu.Unlock()
		review := `{"human_description":"The full root session is traceable.","findings":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "manual-full-session-submit", "type": "function", "function": map[string]any{
					"name": sessionReviewerSubmitToolName, "arguments": review,
				},
			}},
		}}}})
	}))
	defer modelAPI.Close()

	store, server := newManualReviewFixture(t, modelAPI.URL, "off", firstTask)
	if first := runManualReviewFixtureMain(t, server, modelAPI.URL); first.Status != "completed" {
		t.Fatalf("first task=%#v", first)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "root", MessageUUID: "manual-review-second-message", ClientMessageID: "manual-review-second-user",
		Text: secondTask,
	}); err != nil {
		t.Fatal(err)
	}
	if second := runManualReviewFixtureMain(t, server, modelAPI.URL); second.Status != "completed" {
		t.Fatalf("second task=%#v", second)
	}

	response := compatJSONRequest(
		t, server.Handler(), http.MethodPost, "/api/frames/root/audit", "owner", map[string]any{}, http.StatusAccepted,
	)
	waitForManualReviewerStatus(t, store, stringValue(response["frame_id"]), "completed")
	promptMu.Lock()
	gotPrompt := reviewerPrompt
	promptMu.Unlock()
	for _, marker := range []string{firstTask, firstResult, secondTask, secondResult} {
		if !strings.Contains(gotPrompt, marker) {
			t.Fatalf("manual audit omitted %q from the full root transcript:\n%s", marker, gotPrompt)
		}
	}
	if reviewerCalls.Load() != 1 {
		t.Fatalf("reviewerCalls=%d, want one bounded chunk for this small session", reviewerCalls.Load())
	}
	checks, err := store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 1 {
		t.Fatalf("manual full-session checks=%#v err=%v", checks, err)
	}
	sourceRef, ok := checks[0].SourceRef.(map[string]any)
	if !ok || sourceRef["review_scope"] != "full_root_session" ||
		numberValue(sourceRef["review_chunk_count"]) != 1 ||
		numberValue(sourceRef["message_count"]) < 4 ||
		len(stringValue(sourceRef["target_transcript_sha256"])) != 64 {
		t.Fatalf("manual full-session source binding=%#v", checks[0].SourceRef)
	}
}

func TestSessionReviewerTranscriptWindowsUseReviewChunkBoundary(t *testing.T) {
	messages := []agentruntime.Message{
		{Role: "user", Content: strings.Repeat("u", 45_000)},
		{Role: "assistant", Content: strings.Repeat("a", 45_000)},
		{Role: "user", Content: "follow-up"},
		{Role: "assistant", Content: "done"},
	}
	windows := sessionReviewerTranscriptWindows(messages, sessionReviewerTranscriptChunkBytes)
	if len(windows) != 2 {
		t.Fatalf("windows=%#v, want two contract-sized chunks", windows)
	}
	if windows[0].Start != 0 || windows[0].End != 1 || windows[1].Start != 1 || windows[1].End != len(messages) {
		t.Fatalf("unexpected message-boundary chunks: %#v", windows)
	}
	covered := 0
	for _, window := range windows {
		if window.Start != covered || window.End <= window.Start {
			t.Fatalf("non-contiguous review window: %#v", windows)
		}
		covered = window.End
	}
	if covered != len(messages) {
		t.Fatalf("covered=%d messages=%d", covered, len(messages))
	}
}

func TestSessionReviewerTranscriptWindowsFragmentOversizedUTF8MessageWithoutLoss(t *testing.T) {
	content := strings.Repeat("界", 60_000)
	windows := sessionReviewerTranscriptWindows([]agentruntime.Message{{Role: "user", Content: content}}, sessionReviewerTranscriptChunkBytes)
	if len(windows) < 2 {
		t.Fatalf("windows=%d, want multiple bounded fragments", len(windows))
	}
	joined := ""
	for _, window := range windows {
		excerpt := sessionReviewerWindowExcerpt(window)
		if window.Start != 0 || window.End != 1 || len(excerpt) > sessionReviewerTranscriptChunkBytes {
			t.Fatalf("oversized fragment window=%#v bytes=%d", window, len(excerpt))
		}
		if strings.ToValidUTF8(excerpt, "") != excerpt {
			t.Fatalf("fragment is not valid UTF-8: %q", excerpt)
		}
		joined += excerpt
	}
	if strings.Count(joined, "界") != strings.Count(content, "界") {
		t.Fatalf("fragmented projection lost content: got=%d want=%d", strings.Count(joined, "界"), strings.Count(content, "界"))
	}
}

func TestBoundedReviewerTranscriptBlockKeepsValidUTF8AtBothCuts(t *testing.T) {
	value := strings.Repeat("前", 80) + strings.Repeat("后", 80)
	bounded := boundedReviewerTranscriptBlock(value, 128)
	if len(bounded) > 128 || strings.ToValidUTF8(bounded, "") != bounded {
		t.Fatalf("bounded UTF-8 block bytes=%d value=%q", len(bounded), bounded)
	}
	if !strings.Contains(bounded, "middle truncated") || !strings.Contains(bounded, "前") || !strings.Contains(bounded, "后") {
		t.Fatalf("bounded block did not preserve both sides: %q", bounded)
	}
}

func TestSessionReviewerTranscriptProjectionIncludesAttachmentMetadataWithoutRawBytes(t *testing.T) {
	excerpt := sessionReviewerTranscriptExcerpt([]agentruntime.Message{{
		Role: "user",
		Parts: []agentruntime.ContentPart{
			{Type: agentruntime.ContentPartText, Text: "Review the attached report."},
			{Type: agentruntime.ContentPartDocument, Media: &agentruntime.MediaContent{
				Filename: "report.pdf", MIMEType: "application/pdf",
				Source: agentruntime.MediaSource{Type: agentruntime.MediaSourceData, Data: []byte("raw-secret-bytes")},
			}},
		},
	}})
	if !strings.Contains(excerpt, "Review the attached report.") ||
		!strings.Contains(excerpt, "[attachment type=document filename=report.pdf mime_type=application/pdf]") ||
		strings.Contains(excerpt, "raw-secret-bytes") {
		t.Fatalf("attachment review projection=%q", excerpt)
	}
}

func TestAutomaticFrameReviewChunksLongLogicalTaskAndAggregates(t *testing.T) {
	var reviewerCalls atomic.Int32
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		isReviewer := len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "You are the REVIEWER")
		w.Header().Set("Content-Type", "application/json")
		if !isReviewer {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": strings.Repeat("result-evidence ", 3_200),
			}}}})
			return
		}
		call := reviewerCalls.Add(1)
		review := `{"human_description":"This transcript chunk is traceable.","findings":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": fmt.Sprintf("auto-chunk-submit-%d", call), "type": "function", "function": map[string]any{
					"name": sessionReviewerSubmitToolName, "arguments": review,
				},
			}},
		}}}})
	}))
	defer modelAPI.Close()

	store, server := newManualReviewFixture(
		t, modelAPI.URL, "on", strings.Repeat("long-task-evidence ", 3_200),
	)
	result := runManualReviewFixtureMain(t, server, modelAPI.URL)
	if result.Status != "completed" {
		t.Fatalf("automatic long review result=%#v", result)
	}
	if reviewerCalls.Load() < 2 {
		t.Fatalf("reviewerCalls=%d, want multiple contract-sized chunks", reviewerCalls.Load())
	}
	checks, err := store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 1 {
		t.Fatalf("automatic chunked checks=%#v err=%v", checks, err)
	}
	sourceRef, ok := checks[0].SourceRef.(map[string]any)
	if !ok || sourceRef["review_scope"] != "logical_task_terminal" ||
		numberValue(sourceRef["review_chunk_count"]) < 2 ||
		len(stringValue(sourceRef["target_transcript_sha256"])) != 64 {
		t.Fatalf("automatic chunk source binding=%#v", checks[0].SourceRef)
	}
}

func TestAutomaticFrameReviewRunsCheckpointInBackgroundAndReusesReviewerAtTerminal(t *testing.T) {
	var reviewerCalls atomic.Int32
	var reviewerStartedOnce sync.Once
	reviewerStarted := make(chan struct{})
	var promptsMu sync.Mutex
	reviewerPrompts := []string{}
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		system := ""
		if len(request.Messages) > 0 {
			system = request.Messages[0].Content
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(system, "You are the REVIEWER") {
			call := reviewerCalls.Add(1)
			prompt := ""
			for _, message := range request.Messages {
				prompt += "\n" + message.Content
			}
			promptsMu.Lock()
			reviewerPrompts = append(reviewerPrompts, prompt)
			promptsMu.Unlock()
			if strings.Contains(prompt, `"review_scope":"logical_task_checkpoint"`) {
				reviewerStartedOnce.Do(func() { close(reviewerStarted) })
			}
			review := `{"human_description":"The automatic review window is traceable.","findings":[]}`
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
					"id": fmt.Sprintf("background-review-submit-%d", call), "type": "function", "function": map[string]any{
						"name": sessionReviewerSubmitToolName, "arguments": review,
					},
				}},
			}}}})
			return
		}
		if strings.Contains(system, "You are the BOOKMARKER") {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "No bookmark required.",
			}}}})
			return
		}
		hasToolResult := false
		for _, message := range request.Messages {
			if strings.TrimSpace(message.ToolCallID) != "" {
				hasToolResult = true
				break
			}
		}
		if !hasToolResult {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "Searching the registered tools.", "tool_calls": []any{map[string]any{
					"id": "root-tool-search", "type": "function", "function": map[string]any{
						"name": "ToolSearch", "arguments": `{"query":"artifact evidence","max_results":1}`,
					},
				}},
			}}}})
			return
		}
		select {
		case <-reviewerStarted:
		case <-time.After(5 * time.Second):
			http.Error(w, "automatic checkpoint reviewer did not start before the terminal answer", http.StatusGatewayTimeout)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "content": "Final answer after the reviewed tool checkpoint.",
		}}}})
	}))
	defer modelAPI.Close()

	store, server := newManualReviewFixture(
		t, modelAPI.URL, "on", "Search the registered tools, then report the result.",
	)
	result := runManualReviewFixtureMain(t, server, modelAPI.URL)
	if result.Status != "completed" {
		t.Fatalf("automatic background review result=%#v", result)
	}
	if reviewerCalls.Load() != 2 {
		t.Fatalf("reviewerCalls=%d, want one checkpoint window and one terminal tail", reviewerCalls.Load())
	}
	frames, err := store.ListFramesForRoot("root")
	if err != nil {
		t.Fatal(err)
	}
	reviewerFrames := 0
	for _, frame := range frames {
		if frame.AgentName == "REVIEWER" {
			reviewerFrames++
			if frame.Status != "completed" {
				t.Fatalf("reviewer frame=%#v", frame)
			}
		}
	}
	if reviewerFrames != 1 {
		t.Fatalf("reviewerFrames=%d frames=%#v", reviewerFrames, frames)
	}
	checks, err := store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 1 {
		t.Fatalf("automatic background checks=%#v err=%v", checks, err)
	}
	sourceRef, ok := checks[0].SourceRef.(map[string]any)
	if !ok || numberValue(sourceRef["automatic_checkpoint_count"]) != 1 ||
		numberValue(sourceRef["review_chunk_count"]) != 2 ||
		sourceRef["review_scope"] != "logical_task_terminal" {
		t.Fatalf("automatic background source binding=%#v", checks[0].SourceRef)
	}
	promptsMu.Lock()
	prompts := append([]string(nil), reviewerPrompts...)
	promptsMu.Unlock()
	if len(prompts) != 2 ||
		!strings.Contains(prompts[0], `"review_scope":"logical_task_checkpoint"`) ||
		!strings.Contains(prompts[1], `"review_scope":"logical_task_terminal"`) {
		t.Fatalf("reviewer prompts=%#v", prompts)
	}
}

func TestAggregateSessionReviewerReviewsUsesWorstVerdictAndGlobalMessageIndexes(t *testing.T) {
	windows := []sessionReviewerTranscriptWindow{{Start: 0, End: 2}, {Start: 2, End: 5}}
	review := aggregateSessionReviewerReviews([]sessionRunnerReview{
		{
			Verdict: "pass", Summary: "first clean", Issues: []sessionRunnerReviewIssue{{
				MessageIndex: 1, Claim: "supported", Verdict: "pass", Evidence: "trace one",
			}},
		},
		{
			Verdict: "revise", Summary: "second failed", Issues: []sessionRunnerReviewIssue{{
				MessageIndex: 2, Claim: "contradicted", Verdict: "fail", Severity: "high", Evidence: "trace two",
			}},
		},
	}, windows)
	if review.Verdict != "revise" || len(review.Issues) != 2 ||
		review.Issues[0].MessageIndex != 1 || review.Issues[1].MessageIndex != 4 ||
		!strings.Contains(review.Summary, "Chunk 1/2") || !strings.Contains(review.Summary, "Chunk 2/2") {
		t.Fatalf("aggregated review=%#v", review)
	}
}

func TestCompatibilityReviewOutcomeProjectsFindingsAndWarnings(t *testing.T) {
	checks := []workspace.VerificationCheck{
		{
			Verdict: "pass",
			SourceRef: map[string]any{"scientific_decision": map[string]any{
				"effective_verdict": "revise",
			}},
		},
		{Verdict: "fail"},
		{Verdict: "fail"},
		{Verdict: "warn"},
	}
	verdict, issues, blocking := compatibilityReviewOutcome(checks)
	if verdict != "revise" || issues != 3 || blocking != 2 {
		t.Fatalf("outcome=(%q,%d,%d)", verdict, issues, blocking)
	}

	verdict, issues, blocking = compatibilityReviewOutcome([]workspace.VerificationCheck{
		{Verdict: "pass", SourceRef: map[string]any{"review_verdict": "pass"}},
		{Verdict: "warn"},
	})
	if verdict != "pass_with_warnings" || issues != 1 || blocking != 0 {
		t.Fatalf("warning outcome=(%q,%d,%d)", verdict, issues, blocking)
	}
}

func TestManualFrameAuditProtocolFailureDoesNotFailCompletedTask(t *testing.T) {
	var reviewerCalls atomic.Int32
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(request.Messages) == 0 || !strings.Contains(request.Messages[0].Content, "You are the REVIEWER") {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "Completed evidence-backed RIPK1 analysis.",
			}}}})
			return
		}
		reviewerCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "malformed-manual-review", "type": "function", "function": map[string]any{
					"name": sessionReviewerSubmitToolName, "arguments": `{"verdict":`,
				},
			}},
		}}}})
	}))
	defer modelAPI.Close()

	store, server := newManualReviewFixture(t, modelAPI.URL, "off", "Perform a rigorous evidence-backed RIPK1 analysis.")
	main := runManualReviewFixtureMain(t, server, modelAPI.URL)
	if main.Status != "completed" {
		t.Fatalf("main=%#v", main)
	}
	response := compatJSONRequest(
		t, server.Handler(), http.MethodPost, "/api/frames/root/audit", "owner", map[string]any{}, http.StatusAccepted,
	)
	reviewerFrameID := stringValue(response["frame_id"])
	waitForManualReviewerStatus(t, store, reviewerFrameID, "failed")
	projection := compatJSONRequest(t, server.Handler(), http.MethodGet, "/api/frames/root", "owner", nil, http.StatusOK)
	if projection["runtime_stage"] != "review_failed" || projection["runtime_review_status"] != "failed" ||
		projection["runtime_active"] != false {
		t.Fatalf("failed manual review projection=%#v", projection)
	}
	root, found, err := store.GetFrame("root")
	if err != nil || !found || root.Status != "completed" {
		t.Fatalf("review protocol failure changed root: frame=%#v found=%v err=%v", root, found, err)
	}
	checks, err := store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 0 {
		t.Fatalf("malformed reviewer persisted checks=%#v err=%v", checks, err)
	}
	if calls := reviewerCalls.Load(); calls < 2 || calls > int32(sessionReviewerMaxToolRounds+1) {
		t.Fatalf("reviewerCalls=%d, want recovery attempts within the reviewer round budget", calls)
	}
}

func TestAutoReviewProtocolFailureLeavesCompletedTaskWithVisibleReviewFailure(t *testing.T) {
	var reviewerCalls atomic.Int32
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if len(request.Messages) == 0 || !strings.Contains(request.Messages[0].Content, "You are the REVIEWER") {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": "The requested project status is complete.",
			}}}})
			return
		}
		reviewerCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "malformed-auto-review", "type": "function", "function": map[string]any{
					"name": sessionReviewerSubmitToolName, "arguments": `{"verdict":`,
				},
			}},
		}}}})
	}))
	defer modelAPI.Close()

	store, server := newManualReviewFixture(t, modelAPI.URL, "on", "Produce the requested project status.")
	result := runManualReviewFixtureMain(t, server, modelAPI.URL)
	if result.Status != "completed" || result.FinishEventID <= 0 ||
		result.InterruptionReasonCode != "" || result.InterruptionAutoResume {
		t.Fatalf("review failure blocked the completed task: result=%#v", result)
	}
	root, found, err := store.GetFrame("root")
	if err != nil || !found || root.Status != "completed" {
		t.Fatalf("root=%#v found=%v err=%v", root, found, err)
	}
	frames, err := store.ListFramesForRoot("root")
	statuses := map[string]string{}
	for _, frame := range frames {
		statuses[frame.AgentName] = frame.Status
	}
	if err != nil || len(frames) != 3 || statuses["REVIEWER"] != "failed" || statuses["BOOKMARKER"] != "failed" {
		t.Fatalf("reviewer frames=%#v err=%v", frames, err)
	}
	checks, err := store.ListVerificationChecks("root", "")
	if err != nil || len(checks) != 0 {
		t.Fatalf("malformed automatic reviewer persisted checks=%#v err=%v", checks, err)
	}
	// Protocol recovery remains bounded and the failed review remains visible;
	// it is not converted into a false pass check or a failed scientific result.
	if calls := reviewerCalls.Load(); calls < 2 || calls > int32(2*(sessionReviewerMaxToolRounds+1)) {
		t.Fatalf("reviewerCalls=%d, want bounded protocol recovery", calls)
	}
	projection := compatJSONRequest(t, server.Handler(), http.MethodGet, "/api/frames/root", "owner", nil, http.StatusOK)
	if projection["runtime_stage"] != "review_failed" || projection["runtime_active"] != false {
		t.Fatalf("auto review failure projection=%#v", projection)
	}
}

func newManualReviewFixture(t *testing.T, modelEndpoint, verifierMode, taskText string) (*workspace.Store, *Server) {
	return newManualReviewFixtureWithAgent(t, modelEndpoint, verifierMode, "OPERON", taskText)
}

func newManualReviewFixtureWithAgent(
	t *testing.T,
	modelEndpoint, verifierMode, agentName, taskText string,
) (*workspace.Store, *Server) {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "owner", Name: "Project", Path: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: agentName, Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := newV11TestServer(t, Options{
		FileRoot: root, Workspace: store, Transcript: repo,
		CompactSummarizer: SessionRunnerChatOptions{
			Endpoint: modelEndpoint, Model: "test-model", MaxAttempts: 1, RequestTimeout: 5 * time.Second,
			ReplayLimit: 100, OutputLimitBytes: 64 << 10, DisableMCPDiscovery: true,
		},
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "root", MessageUUID: "manual-review-message", ClientMessageID: "manual-review-user",
		Text: taskText, RuntimeConfig: map[string]any{"verifier_mode": verifierMode},
	}); err != nil {
		t.Fatal(err)
	}
	return store, server
}

func runManualReviewFixtureMain(t *testing.T, server *Server, modelEndpoint string) SessionRunnerCycleResult {
	t.Helper()
	stream, found, err := server.transcriptStore.GetFrameStreamBySession(context.Background(), "owner", "root")
	if err != nil || !found {
		t.Fatalf("load manual review fixture stream found=%t err=%v", found, err)
	}
	if stream.InputRevision <= 1 {
		if _, _, created, appendErr := server.transcriptStore.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "manual-review-intake-answer", FrameEventID: "manual-review-intake-answer-event",
			MessageUUID: "manual-review-intake-answer-message", Text: "Use the recommended scope.",
			MessageOrigin: "input_response",
		}); appendErr != nil || !created {
			t.Fatalf("append manual review intake answer created=%t err=%v", created, appendErr)
		}
		if _, _, intentErr := server.transcriptStore.EnsureActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID); intentErr != nil {
			t.Fatalf("ensure manual review fixture task intent: %v", intentErr)
		}
	}
	result, err := server.RunSessionRunnerChatOnce(t.Context(), SessionRunnerChatOptions{
		SessionID: "root", RunnerID: "main-runner", Endpoint: modelEndpoint, Model: "test-model",
		MaxAttempts: 1, LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 << 10,
		AllowedTools: []string{"ToolSearch"}, DisableSkillDiscovery: true, DisableMCPDiscovery: true,
	})
	if err != nil || !result.Claimed {
		t.Fatalf("run main result=%#v err=%v", result, err)
	}
	return result
}

func waitForManualReviewerStatus(t *testing.T, store *workspace.Store, frameID, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		frame, found, err := store.GetFrame(frameID)
		if err != nil {
			t.Fatal(err)
		}
		if found && strings.EqualFold(frame.Status, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	frame, found, err := store.GetFrame(frameID)
	t.Fatalf("reviewer frame=%#v found=%v err=%v, want status %s", frame, found, err, want)
}
