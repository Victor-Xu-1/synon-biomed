package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestSessionBookmarkerSchemaAndSubmissionRequireExactEligibleQuotes(t *testing.T) {
	result := agentruntime.RunResult{
		Messages: []agentruntime.Message{
			{Role: "system", Content: "hidden policy"},
			{Role: "user", Content: "Calculate the result."},
			{Role: "assistant", Content: "**Result:** 42 samples passed validation."},
		},
		FinalMessage: agentruntime.Message{Role: "assistant", Content: "**Result:** 42 samples passed validation."},
	}
	prompt, window := buildSessionBookmarkerPrompt(result)
	if strings.Contains(prompt, "hidden policy") || !strings.Contains(prompt, "--- msg[2] assistant ---") ||
		!strings.Contains(prompt, "⟦transcript:") {
		t.Fatalf("bookmarker prompt=%s", prompt)
	}
	schema := sessionBookmarkerSubmitToolSchema()
	validator := compileAgentRuntimeMCPValidator(schema)
	valid := map[string]any{
		"human_description": "Saving final result bookmark",
		"bookmarks":         []any{map[string]any{"msg_idx": 2, "quote": "**Result:** 42 samples passed validation.", "label": "Final validation result"}},
	}
	if value := validator.Validate(valid); value != nil {
		t.Fatalf("valid bookmark submission rejected: %#v", value)
	}
	scope := &sessionBookmarkerScope{window: window}
	raw, _ := jsonMarshal(valid)
	if err := scope.submit(raw); err != nil {
		t.Fatal(err)
	}
	submission, submitted := scope.snapshot()
	if !submitted || len(submission.Bookmarks) != 1 || submission.Bookmarks[0].MessageIndex != 2 {
		t.Fatalf("submission=%#v submitted=%v", submission, submitted)
	}

	userScope := &sessionBookmarkerScope{window: window}
	invalid, _ := jsonMarshal(map[string]any{
		"human_description": "Saving user prompt bookmark",
		"bookmarks":         []any{map[string]any{"msg_idx": 1, "quote": "Calculate the result.", "label": "User request"}},
	})
	if err := userScope.submit(invalid); err == nil {
		t.Fatal("bookmarker admitted a user-authored quote")
	}
}

func TestSessionBookmarkerPersistsAgentBookmarkIdempotentlyOnCanonicalMessage(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	const answer = "**Finding:** 18 of 20 checks passed."
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: "assistant-answer", FrameID: "root", Type: "assistant_message",
		Payload: map[string]any{"_uuid": "message-final", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": answer}}},
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store})
	result := agentruntime.RunResult{FinalMessage: agentruntime.Message{Role: "assistant", Content: answer}}
	window := map[int]agentruntime.Message{7: {Role: "assistant", Content: answer}}
	submission := sessionBookmarkerSubmission{
		HumanDescription: "Saving validation finding bookmark",
		Bookmarks:        []sessionBookmarkerBookmark{{MessageIndex: 7, Quote: answer, Label: "Validation finding"}},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := app.persistSessionBookmarkerSubmission(
			sessionstore.Session{ID: "root"}, result, nil, 0, submission, window,
		); err != nil {
			t.Fatalf("persist attempt %d: %v", attempt, err)
		}
	}
	annotations, err := store.ListTranscriptAnnotations("root")
	if err != nil || len(annotations) != 1 {
		t.Fatalf("annotations=%#v err=%v", annotations, err)
	}
	annotation := annotations[0]
	if annotation.Origin != "agent" || annotation.Kind != "bookmark" || annotation.MessageIndex != 0 ||
		annotation.MessageUUID != "message-final" || annotation.AnchorText != answer || annotation.Note != "Validation finding" ||
		annotation.StartOffset == nil || *annotation.StartOffset != 0 || annotation.EndOffset == nil || *annotation.EndOffset != len(answer) {
		t.Fatalf("annotation=%#v", annotation)
	}
}

func TestReviewerAndBookmarkerUseDistinctDeterministicHiddenFrames(t *testing.T) {
	reviewer := runnerFixedJobFrameID("REVIEWER", "session", 2, 3)
	bookmarker := runnerFixedJobFrameID("BOOKMARKER", "session", 2, 3)
	if reviewer != runnerReviewerFrameID("session", 2, 3) || reviewer == bookmarker ||
		!strings.HasPrefix(reviewer, "completion-review-") || !strings.HasPrefix(bookmarker, "completion-bookmarker-") {
		t.Fatalf("reviewer=%q bookmarker=%q", reviewer, bookmarker)
	}
}

func TestAutomaticReviewerAndBookmarkerPersistPassAndBookmark(t *testing.T) {
	const candidate = "**Result:** 42 samples passed validation."
	messageHeader := regexp.MustCompile(`--- msg\[([0-9]+)\] assistant ---\n\*\*Result:\*\* 42 samples passed validation\.`)
	var reviewerCalls, bookmarkerCalls atomic.Int32
	var requestMu sync.Mutex
	requestKinds := []string{}
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		system, latestUser, hasToolResult := "", "", false
		for _, message := range request.Messages {
			if message.Role == "system" && system == "" {
				system = message.Content
			}
			if message.Role == "user" {
				latestUser = message.Content
			}
			if message.Role == "tool" {
				hasToolResult = true
			}
		}
		hasSubmit, hasRepl := false, false
		toolNames := []string{}
		for _, tool := range request.Tools {
			toolNames = append(toolNames, tool.Function.Name)
			hasSubmit = hasSubmit || tool.Function.Name == sessionReviewerSubmitToolName
			hasRepl = hasRepl || tool.Function.Name == "repl"
		}
		requestMu.Lock()
		requestKinds = append(requestKinds, fmt.Sprintf("system=%q user=%q tools=%v toolResult=%v", truncateReviewerText(system, 80), truncateReviewerText(latestUser, 80), toolNames, hasToolResult))
		requestMu.Unlock()
		responseMessage := map[string]any{"role": "assistant", "content": candidate}
		if strings.Contains(system, "You are the REVIEWER") || strings.Contains(latestUser, "COMPLETION REVIEW SOURCE BINDING") || hasSubmit && hasRepl {
			if hasToolResult || reviewerCalls.Add(1) > 1 {
				responseMessage = map[string]any{"role": "assistant", "content": "review submitted"}
			} else {
				responseMessage = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
					"id": "review-submit", "type": "function", "function": map[string]any{
						"name":      sessionReviewerSubmitToolName,
						"arguments": `{"human_description":"Reviewing completed result","findings":[]}`,
					},
				}}}
			}
		} else if strings.Contains(system, "You are the BOOKMARKER") || hasSubmit {
			if hasToolResult || bookmarkerCalls.Add(1) > 1 {
				responseMessage = map[string]any{"role": "assistant", "content": "bookmarks submitted"}
			} else {
				match := messageHeader.FindStringSubmatch(latestUser)
				if len(match) != 2 {
					http.Error(w, "candidate message header not found", http.StatusBadRequest)
					return
				}
				arguments := fmt.Sprintf(`{"human_description":"Saving final result bookmark","bookmarks":[{"msg_idx":%s,"quote":"%s","label":"Final validation result"}]}`, match[1], candidate)
				responseMessage = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
					"id": "bookmark-submit", "type": "function", "function": map[string]any{
						"name": sessionReviewerSubmitToolName, "arguments": arguments,
					},
				}}}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "fixed-job-response", "model": "test-model",
			"choices": []any{map[string]any{"message": responseMessage, "finish_reason": "stop"}},
		})
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{FileRoot: root, Workspace: store, Transcript: repository})
	if err := app.sessionStore.Upsert(sessionstore.Session{
		ID: "root", Title: "Reviewed task", WorkDir: root,
		Project:       &sessionstore.Project{ID: "project", Name: "Project", Path: root, BoundAt: time.Now().UTC()},
		Orchestration: map[string]any{"sessionConfig": map[string]any{"verifier_mode": "off"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "root", MessageUUID: "user-message", ClientMessageID: "user-client",
		Text: "Return the validated result.", RuntimeConfig: map[string]any{"verifier_mode": "on"},
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, app, "owner", "root")
	cycle, err := app.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "root", RunnerID: "review-bookmark-runner", Endpoint: modelAPI.URL,
		Model: "test-model", RequestTimeout: time.Minute, MaxAttempts: 1, LeaseTTL: time.Minute,
		ReplayLimit: 100, OutputLimitBytes: 64 << 10, DisableSkillDiscovery: true, DisableMCPDiscovery: true,
	})
	if err != nil || cycle.Status != "completed" {
		t.Fatalf("cycle=%#v err=%v", cycle, err)
	}
	annotations, err := store.ListTranscriptAnnotations("root")
	if err != nil || len(annotations) != 1 || annotations[0].Origin != "agent" || annotations[0].Kind != "bookmark" ||
		annotations[0].AnchorText != candidate || annotations[0].Note != "Final validation result" {
		t.Fatalf("annotations=%#v err=%v", annotations, err)
	}
	frames, err := store.ListFramesForRoot("root")
	statuses := map[string]string{}
	for _, frame := range frames {
		statuses[frame.AgentName] = frame.Status
	}
	if err != nil || statuses["REVIEWER"] != "completed" || statuses["BOOKMARKER"] != "completed" {
		reviewEvents := []workspace.FrameEvent{}
		var reviewerCompat workspace.CompatibilityFrame
		for _, frame := range frames {
			if frame.AgentName == "REVIEWER" {
				reviewEvents, _ = store.ListFrameEvents(frame.ID, 0, 100)
				reviewerCompat, _, _ = store.GetCompatibilityFrame(frame.ID)
			}
		}
		requestMu.Lock()
		requests := append([]string(nil), requestKinds...)
		requestMu.Unlock()
		t.Fatalf("frames=%#v statuses=%#v reviewer=%#v reviewEvents=%#v requests=%#v err=%v", frames, statuses, reviewerCompat, reviewEvents, requests, err)
	}
}

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
