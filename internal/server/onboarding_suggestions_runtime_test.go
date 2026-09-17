package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestStructuredOnboardingCancelWaitsForTranscriptAdmission(t *testing.T) {
	previousMaxProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousMaxProcs) })

	runtimeRoot := t.TempDir()
	store, err := workspace.Open(filepath.Join(runtimeRoot, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-onboarding-cancel", UserID: "local", Name: "Onboarding cancellation",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: runtimeRoot, Workspace: store, Transcript: repository, SkillDirectories: []string{v11SkillsDir(t)}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
		_ = store.Close()
	})

	const frameID = "frame-onboarding-cancel-admission"
	input := map[string]any{
		"target_agent": "ONBOARDING", "project_id": "project-onboarding-cancel", "frame_id": frameID,
		"intent_id": "00000000-0000-4000-8000-000000000311", "onboarding_mode": structuredOnboardingModeV1,
		"input_data": map[string]any{
			"request": "Generate three onboarding tasks.",
			"USER_DATA": map[string]any{
				"selfDescription":   "I need a reproducible translational genomics analysis workflow.",
				"attachedFilenames": []any{},
			},
		},
	}

	// Hold the final admission boundary after the Frame has been created but
	// before submitFrameMessage can atomically establish the Transcript stream.
	// This is the exact state that the browser exposed to the concurrent custom
	// task launch.
	srv.sessionSubmissionMu.Lock()
	submissionLocked := true
	defer func() {
		if submissionLocked {
			srv.sessionSubmissionMu.Unlock()
		}
	}()

	submitDone := make(chan *httptest.ResponseRecorder, 1)
	submitRequest := compatRequest(t, http.MethodPost, "/api/request", "local", input)
	go func() {
		response := httptest.NewRecorder()
		srv.Handler().ServeHTTP(response, submitRequest)
		submitDone <- response
	}()

	var provisionalFrame workspace.CompatibilityFrame
	frameDeadline := time.NewTimer(2 * time.Second)
	defer frameDeadline.Stop()
	for {
		candidate, found, frameErr := store.GetFrame(frameID)
		if frameErr != nil {
			t.Fatal(frameErr)
		}
		if found {
			provisionalFrame = workspace.CompatibilityFrame{Frame: candidate}
			break
		}
		select {
		case response := <-submitDone:
			t.Fatalf("submission returned before Transcript admission barrier: %d %s", response.Code, response.Body.String())
		case <-frameDeadline.C:
			t.Fatal("Frame was not created before Transcript admission barrier")
		default:
			runtime.Gosched()
		}
	}
	if stream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", frameID); err != nil || found {
		t.Fatalf("provisional Frame already had Transcript authority: stream=%#v found=%t err=%v", stream, found, err)
	}

	type cancelOutcome struct {
		result workspace.CancelCompatibilityFrameResult
		err    error
	}
	cancelAttempt := make(chan struct{})
	cancelDone := make(chan cancelOutcome, 1)
	go func() {
		// The unbuffered rendezvous and single scheduler P let the test yield
		// directly into cancelCompatibilityFrameTree's first operation: the
		// compatibility authority lock. This avoids treating goroutine creation
		// or an arbitrary sleep as proof that cancellation reached the barrier.
		cancelAttempt <- struct{}{}
		result, cancelErr := srv.cancelCompatibilityFrameTree(
			context.Background(), provisionalFrame, "onboarding-suggestion-superseded",
		)
		cancelDone <- cancelOutcome{result: result, err: cancelErr}
	}()
	<-cancelAttempt
	runtime.Gosched()

	select {
	case outcome := <-cancelDone:
		t.Fatalf("cancel returned before Transcript admission settled: result=%#v err=%v", outcome.result, outcome.err)
	case <-time.After(250 * time.Millisecond):
	}

	srv.sessionSubmissionMu.Unlock()
	submissionLocked = false

	var submitResponse *httptest.ResponseRecorder
	select {
	case submitResponse = <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("submission did not finish after releasing Transcript admission")
	}
	if submitResponse.Code != http.StatusOK {
		t.Fatalf("submission status=%d body=%s", submitResponse.Code, submitResponse.Body.String())
	}

	var cancelled cancelOutcome
	select {
	case cancelled = <-cancelDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not finish after Transcript admission")
	}
	if cancelled.err != nil {
		t.Fatal(cancelled.err)
	}
	if len(cancelled.result.CancelledFrameIDs) != 1 || cancelled.result.CancelledFrameIDs[0] != frameID {
		t.Fatalf("cancelled frames=%v, want [%s]", cancelled.result.CancelledFrameIDs, frameID)
	}

	frame, found, err := store.GetFrame(frameID)
	if err != nil || !found || frame.Status != "cancelled" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	stream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", frameID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	state, found, err := repository.GetLatestRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || state.Attempt != 1 || state.Status != "cancelled" || state.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("runner state=%#v found=%t err=%v", state, found, err)
	}
	terminal, found, err := repository.GetEventByClientMessageID(
		context.Background(), stream.UID, stream.OwnerID, "runner-cancel:"+stream.UID+":1",
	)
	if err != nil || !found || terminal.Type != "runner_finished" ||
		!strings.Contains(string(terminal.PayloadJSON), `"status":"cancelled"`) {
		t.Fatalf("terminal=%#v found=%t err=%v", terminal, found, err)
	}
	lateClaim, err := repository.ClaimNextRunner(context.Background(), transcriptstore.ClaimNextRunnerInput{
		RunnerID: "late-onboarding-runner", TTL: time.Minute,
	})
	if err != nil || lateClaim.Claimed {
		t.Fatalf("cancelled stream was claimable: result=%#v err=%v", lateClaim, err)
	}
}

func TestStructuredOnboardingRequestRejectsMalformedUserDataBeforeFrameCreation(t *testing.T) {
	runtimeRoot := t.TempDir()
	store, err := workspace.Open(filepath.Join(runtimeRoot, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-onboarding-validation", UserID: "local", Name: "Onboarding validation",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: runtimeRoot, Workspace: store, Transcript: repository, SkillDirectories: []string{v11SkillsDir(t)}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	validDescription := strings.Repeat("x", 40)
	valid := func() map[string]any {
		return map[string]any{
			"target_agent": "ONBOARDING", "project_id": "project-onboarding-validation",
			"intent_id": "00000000-0000-4000-8000-000000000301", "onboarding_mode": structuredOnboardingModeV1,
			"input_data": map[string]any{
				"request":   "Generate three tasks.",
				"USER_DATA": map[string]any{"selfDescription": validDescription, "attachedFilenames": []any{}},
			},
		}
	}
	tests := map[string]func(map[string]any){
		"unknown mode": func(input map[string]any) { input["onboarding_mode"] = "other" },
		"wrong agent":  func(input map[string]any) { input["target_agent"] = "OPERON" },
		"empty input": func(input map[string]any) {
			input["input_data"].(map[string]any)["USER_DATA"].(map[string]any)["selfDescription"] = ""
		},
		"long description": func(input map[string]any) {
			input["input_data"].(map[string]any)["USER_DATA"].(map[string]any)["selfDescription"] = strings.Repeat("x", 2001)
		},
		"unknown user field": func(input map[string]any) {
			input["input_data"].(map[string]any)["USER_DATA"].(map[string]any)["extra"] = true
		},
		"unknown input field": func(input map[string]any) { input["input_data"].(map[string]any)["extra"] = true },
		"filename count mismatch": func(input map[string]any) {
			input["input_data"].(map[string]any)["USER_DATA"].(map[string]any)["attachedFilenames"] = []any{"missing.md"}
		},
		"duplicate refs": func(input map[string]any) {
			ref := map[string]any{"artifact_id": "artifact", "version_id": "version"}
			input["artifact_refs"] = []any{ref, ref}
			input["input_data"].(map[string]any)["USER_DATA"].(map[string]any)["attachedFilenames"] = []any{"a", "a"}
		},
		"too many refs": func(input map[string]any) {
			refs := make([]any, 21)
			filenames := make([]any, 21)
			for index := range refs {
				refs[index] = map[string]any{"artifact_id": "artifact-" + string(rune('a'+index)), "version_id": "version-" + string(rune('a'+index))}
				filenames[index] = "file"
			}
			input["artifact_refs"] = refs
			input["input_data"].(map[string]any)["USER_DATA"].(map[string]any)["attachedFilenames"] = filenames
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := valid()
			mutate(input)
			compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/request", "local", input, http.StatusBadRequest)
			frames, err := store.ListFrames("project-onboarding-validation", 100, 0)
			if err != nil || len(frames) != 0 {
				t.Fatalf("frames=%#v err=%v", frames, err)
			}
		})
	}
	const visibleFrameID = "visible-onboarding-control"
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: visibleFrameID, ProjectID: "project-onboarding-validation", Name: "Visible conversation",
		AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	shortDescription := valid()
	shortDescription["input_data"].(map[string]any)["USER_DATA"].(map[string]any)["selfDescription"] = "short"
	shortResponse := compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/request", "local", shortDescription, http.StatusOK)
	shortFrameID := stringValue(shortResponse["frame_id"])
	if shortFrameID == "" {
		t.Fatalf("short explicit request response=%#v", shortResponse)
	}
	if listed := compatJSONArrayRequest(t, srv.Handler(), http.MethodGet, "/api/frames?project_id=project-onboarding-validation&limit=1", "local", nil, http.StatusOK); len(listed) != 1 || stringValue(listed[0]["id"]) != visibleFrameID {
		t.Fatalf("hidden onboarding frame leaked into frame list: %#v", listed)
	}
	conversations := compatJSONRequest(t, srv.Handler(), http.MethodGet, "/api/conversations?limit=1", "local", nil, http.StatusOK)
	items, _ := conversations["items"].([]any)
	if len(items) != 1 || stringValue(items[0].(map[string]any)["id"]) != visibleFrameID {
		t.Fatalf("hidden onboarding frame leaked into conversations: %#v", conversations)
	}
	projects := compatJSONRequest(t, srv.Handler(), http.MethodGet, "/api/projects?limit=10", "local", nil, http.StatusOK)
	project := compatibilityTestProjectByID(t, projects["projects"], "project-onboarding-validation")
	if numberValue(project["conversation_count"]) != 1 {
		t.Fatalf("hidden onboarding frame leaked into project conversation count: %#v", project)
	}
	active := compatJSONRequest(t, srv.Handler(), http.MethodGet, "/api/conversations/active-count", "local", nil, http.StatusOK)
	if numberValue(active["count"]) != 0 {
		t.Fatalf("hidden onboarding frame leaked into active count: %#v", active)
	}
	dashboard := compatJSONRequest(t, srv.Handler(), http.MethodGet, "/api/projects/dashboard", "local", nil, http.StatusOK)
	if numberValue(dashboard["total_processing"]) != 0 || numberValue(dashboard["total_needs_input"]) != 0 {
		t.Fatalf("hidden onboarding frame leaked into project dashboard: %#v", dashboard)
	}
	compatJSONRequest(t, srv.Handler(), http.MethodGet, "/api/frames/"+shortFrameID+"?shallow=true", "local", nil, http.StatusOK)
	if err := validateStructuredOnboardingUserData(workspaceProjectRequestInput{InputData: map[string]any{
		"request": "Generate three tasks.",
		"USER_DATA": map[string]any{
			"selfDescription":   "",
			"attachedFilenames": []any{"notes.md"},
		},
	}}, 1); err != nil {
		t.Fatalf("attachment-only explicit request rejected: %v", err)
	}
	withoutMode := valid()
	delete(withoutMode, "onboarding_mode")
	withoutMode["artifact_refs"] = []any{map[string]any{"artifact_id": "artifact", "version_id": "version"}}
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/request", "local", withoutMode, http.StatusBadRequest)
}

func TestReadOnboardingAttachmentChunkHonorsCaptureLimitAndCancellation(t *testing.T) {
	content, size, _, err := readOnboardingAttachmentChunk(context.Background(), bytes.NewReader([]byte("exact")), 0, 5)
	if err != nil || string(content) != "exact" || size != 5 {
		t.Fatalf("content=%q size=%d err=%v", content, size, err)
	}
	content, size, _, err = readOnboardingAttachmentChunk(context.Background(), bytes.NewReader([]byte("over-limit")), 0, 5)
	if err != nil || string(content) != "over-" || size != int64(len("over-limit")) {
		t.Fatalf("bounded content=%q size=%d err=%v", content, size, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := readOnboardingAttachmentChunk(cancelled, bytes.NewReader([]byte("text")), 0, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	if _, _, _, err := readOnboardingAttachmentChunk(context.Background(), zeroProgressReader{}, 0, 10); err == nil ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("zero-progress err=%v", err)
	}
	if _, _, _, err := readOnboardingAttachmentChunk(context.Background(), errorReader{}, 0, 10); err == nil ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("reader err=%v", err)
	}
}

func TestStructuredOnboardingRuntimeDoesNotTruncateAttachmentToolRounds(t *testing.T) {
	options := applyStructuredOnboardingRuntime(map[string]any{
		"onboardingMode": structuredOnboardingModeV1,
	}, "ONBOARDING", SessionRunnerChatOptions{MaxToolRounds: 3})
	if options.MaxToolRounds != 0 {
		t.Fatalf("structured onboarding MaxToolRounds=%d, want deadline-bounded unlimited", options.MaxToolRounds)
	}
}

func TestStructuredOnboardingAttachmentInputIsClosedAndContinuationBound(t *testing.T) {
	checksum := strings.Repeat("a", sha256.Size*2)
	versionID, offset, continuation, err := structuredOnboardingAttachmentInput(map[string]any{
		"version_id": "version-1", "offset_bytes": float64(7), "checksum": checksum,
	})
	if err != nil || versionID != "version-1" || offset != 7 || continuation != checksum {
		t.Fatalf("valid continuation version=%q offset=%d checksum=%q err=%v", versionID, offset, continuation, err)
	}
	versionID, offset, continuation, err = structuredOnboardingAttachmentInput(map[string]any{
		"version_id": "version-1", "offset_bytes": float64(0),
	})
	if err != nil || versionID != "version-1" || offset != 0 || continuation != "" {
		t.Fatalf("explicit initial offset version=%q offset=%d checksum=%q err=%v", versionID, offset, continuation, err)
	}
	cases := []map[string]any{
		{"version_id": "version-1", "bogus": float64(1)},
		{"version_id": "version-1", "offset_bytes": float64(7)},
		{"version_id": "version-1", "offset_bytes": float64(0), "checksum": checksum},
		{"version_id": "version-1", "offset_bytes": float64(9007199254740992), "checksum": checksum},
		{"version_id": "version-1", "offset_bytes": float64(7), "checksum": strings.ToUpper(checksum)},
	}
	for index, input := range cases {
		if _, _, _, err := structuredOnboardingAttachmentInput(input); err == nil {
			t.Fatalf("invalid continuation case %d accepted: %#v", index, input)
		}
	}
}

func TestStructuredOnboardingQueuedDeliveryPreservesExactContextAcrossRestart(t *testing.T) {
	runtimeRoot := t.TempDir()
	workspacePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-onboarding-queue", UserID: "local", Name: "Onboarding queue",
	}); err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact-onboarding-queue", ProjectID: "project-onboarding-queue", Name: "queue.md",
		ContentType: "text/markdown", Content: strings.NewReader("queued attachment"), CreatedBy: "local", IsUserUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: runtimeRoot, Workspace: store, Transcript: repository, SkillDirectories: []string{v11SkillsDir(t)}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
		_ = store.Close()
	})
	frameID := "frame-onboarding-queue"
	firstIntent := "00000000-0000-4000-8000-000000000401"
	secondIntent := "00000000-0000-4000-8000-000000000402"
	first := compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/request", "local",
		structuredOnboardingRequest("project-onboarding-queue", frameID, firstIntent, artifact.ID, version.ID, "first"), http.StatusOK)
	if first["status"] != "accepted" {
		t.Fatalf("first=%#v", first)
	}
	second := compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/request", "local",
		structuredOnboardingRequest("project-onboarding-queue", frameID, secondIntent, artifact.ID, version.ID, "second"), http.StatusOK)
	if second["status"] != "message_queued" {
		t.Fatalf("second=%#v", second)
	}

	restartContext, restartCancel := context.WithTimeout(context.Background(), time.Second)
	if err := srv.Close(restartContext); err != nil {
		restartCancel()
		t.Fatal(err)
	}
	restartCancel()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err = store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv = New(Options{FileRoot: runtimeRoot, Workspace: store, Transcript: repository, SkillDirectories: []string{v11SkillsDir(t)}})
	advanced, err := srv.advanceCompatibilityFrameAfterRunner(frameID, "completed")
	if err != nil || advanced != 1 {
		t.Fatalf("advanced=%d err=%v", advanced, err)
	}
	stream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", frameID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	event, found, err := repository.GetEventByClientMessageID(context.Background(), stream.UID, stream.OwnerID, secondIntent)
	if err != nil || !found {
		t.Fatalf("event=%#v found=%t err=%v", event, found, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.PayloadJSON, &payload); err != nil {
		t.Fatal(err)
	}
	refs, err := decodeUserArtifactReferences(payload["artifactRefs"])
	if err != nil || len(refs) != 1 || refs[0].ArtifactID != artifact.ID || refs[0].VersionID != version.ID ||
		payload["messageContext"] != structuredOnboardingMessageContext {
		t.Fatalf("payload=%#v refs=%#v err=%v", payload, refs, err)
	}
	runtimeConfig, _ := payload["runtimeConfig"].(map[string]any)
	if runtimeConfig["onboardingMode"] != structuredOnboardingModeV1 {
		t.Fatalf("runtime config=%#v", runtimeConfig)
	}
}

type zeroProgressReader struct{}

func (zeroProgressReader) Read([]byte) (int, error) { return 0, nil }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func structuredOnboardingRequest(projectID, frameID, intentID, artifactID, versionID, suffix string) map[string]any {
	return map[string]any{
		"target_agent": "ONBOARDING", "project_id": projectID, "frame_id": frameID,
		"intent_id": intentID, "onboarding_mode": structuredOnboardingModeV1,
		"artifact_refs": []any{map[string]any{"artifact_id": artifactID, "version_id": versionID}},
		"input_data": map[string]any{
			"request": "Generate three onboarding suggestions for " + suffix + ".",
			"USER_DATA": map[string]any{
				"selfDescription":   "I study cellular signaling and need reproducible analysis workflows for this " + suffix + " request.",
				"attachedFilenames": []any{"queue.md"},
			},
		},
	}
}

func TestStructuredOnboardingReadsCanonicalAttachmentBeforeThreeTaskSuggestions(t *testing.T) {
	runtimeRoot := t.TempDir()
	workspacePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-onboarding-suggestions", UserID: "local", Name: "Onboarding suggestions",
	}); err != nil {
		t.Fatal(err)
	}
	_, unboundVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact-unbound", ProjectID: "project-onboarding-suggestions", Name: "unbound.md",
		ContentType: "text/markdown", Content: strings.NewReader("UNBOUND SECRET CONTENT"),
		CreatedBy: "local", IsUserUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	attachmentContent := "NEK7 inflammasome microscopy uses CellProfiler masks and manual QC.\n" +
		strings.Repeat("Replicate segmentation controls across the cohort.\n", 256) +
		"FINAL ATTACHMENT QC MARKER"
	artifact, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "artifact-lab-readme", ProjectID: "project-onboarding-suggestions", Name: "lab-readme.md",
		ContentType: "text/markdown", Content: strings.NewReader(attachmentContent),
		CreatedBy: "local", IsUserUpload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		messagesJSON, _ := json.Marshal(payload["messages"])
		toolsJSON, _ := json.Marshal(payload["tools"])
		w.Header().Set("Content-Type", "application/json")
		switch sequence {
		case 1:
			if !strings.Contains(string(messagesJSON), "structured user data") ||
				strings.Contains(string(messagesJSON), "permissions widget") ||
				!strings.Contains(string(toolsJSON), `"name":"read_onboarding_attachment"`) ||
				!strings.Contains(string(toolsJSON), `"name":"ask_user"`) ||
				strings.Contains(string(toolsJSON), `"name":"Read"`) ||
				strings.Contains(string(toolsJSON), `"name":"file_read"`) {
				t.Fatalf("first model request messages=%s tools=%s", messagesJSON, toolsJSON)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read-unbound","type":"function","function":{"name":"read_onboarding_attachment","arguments":"{\"version_id\":\"` + unboundVersion.ID + `\"}"}}]}}]}`))
		case 2:
			if !strings.Contains(string(messagesJSON), "onboarding attachment authority is unavailable") ||
				strings.Contains(string(messagesJSON), "UNBOUND SECRET CONTENT") {
				t.Fatalf("unbound attachment result was not fail-closed: %s", messagesJSON)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read-lab-readme","type":"function","function":{"name":"read_onboarding_attachment","arguments":"{\"version_id\":\"` + version.ID + `\"}"}}]}}]}`))
		case 3:
			messages, _ := payload["messages"].([]any)
			last, _ := messages[len(messages)-1].(map[string]any)
			var toolResult map[string]any
			if err := json.Unmarshal([]byte(stringValue(last["content"])), &toolResult); err != nil {
				t.Fatalf("decode first attachment chunk: %v content=%#v", err, last["content"])
			}
			firstContent := stringValue(toolResult["content"])
			eof, eofOK := toolResult["eof"].(bool)
			nextOffset := int64(numberValue(toolResult["next_offset_bytes"]))
			checksum := stringValue(toolResult["checksum"])
			if !strings.Contains(firstContent, "NEK7 inflammasome microscopy") ||
				!strings.Contains(firstContent, "CellProfiler masks") || !eofOK || eof || nextOffset <= 0 || checksum == "" {
				t.Fatalf("first attachment chunk=%#v", toolResult)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read-lab-readme-tail","type":"function","function":{"name":"read_onboarding_attachment","arguments":"{\"version_id\":\"` + version.ID + `\",\"offset_bytes\":` + strconv.FormatInt(nextOffset, 10) + `,\"checksum\":\"` + checksum + `\"}"}}]}}]}`))
		case 4:
			messages, _ := payload["messages"].([]any)
			last, _ := messages[len(messages)-1].(map[string]any)
			var toolResult map[string]any
			if err := json.Unmarshal([]byte(stringValue(last["content"])), &toolResult); err != nil {
				t.Fatalf("decode final attachment chunk: %v content=%#v", err, last["content"])
			}
			eof, eofOK := toolResult["eof"].(bool)
			if !strings.Contains(stringValue(toolResult["content"]), "FINAL ATTACHMENT QC MARKER") || !eofOK || !eof {
				t.Fatalf("final attachment chunk=%#v", toolResult)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"suggest-three","type":"function","function":{"name":"ask_user","arguments":"{\"question\":\"Where should we start?\",\"header\":\"First task\",\"options\":[{\"label\":\"Map recent NEK7 microscopy methods\",\"description\":\"Compare segmentation and QC approaches.\",\"pros\":\"Establishes an evidence-based method baseline.\",\"cons\":\"Does not yet benchmark the current masks.\",\"readiness\":\"Execution readiness has not been checked.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"tool-call:read-lab-readme-tail\"],\"readiness_evidence\":[],\"selection_basis\":\"scientific_evidence\",\"expected_outcome\":\"A focused method and QC comparison.\",\"selection_rationale\":\"Choose when method selection is the immediate decision.\",\"recommended\":false},{\"label\":\"Benchmark the CellProfiler masks\",\"description\":\"Run a first-pass QC analysis on the current masks.\",\"pros\":\"Directly tests the current workflow.\",\"cons\":\"Requires representative masks and images.\",\"readiness\":\"Representative inputs and compute have not been checked.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"tool-call:read-lab-readme-tail\"],\"readiness_evidence\":[],\"selection_basis\":\"scientific_evidence\",\"expected_outcome\":\"A reproducible mask QC benchmark if the inputs are available.\",\"selection_rationale\":\"Recommended because it directly evaluates the documented workflow.\",\"recommended\":true},{\"label\":\"Automate the microscopy QC workflow\",\"description\":\"Turn the repeated manual checks into a reusable pipeline.\",\"pros\":\"Improves repeatability for future studies.\",\"cons\":\"Automation criteria depend on a validated benchmark.\",\"readiness\":\"Execution readiness has not been checked.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"tool-call:read-lab-readme-tail\"],\"readiness_evidence\":[],\"selection_basis\":\"scientific_evidence\",\"expected_outcome\":\"A reusable microscopy QC workflow after criteria are validated.\",\"selection_rationale\":\"Choose when operational reuse is the immediate priority.\",\"recommended\":false}]}"}}]}}]}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	srv := New(Options{FileRoot: runtimeRoot, Workspace: store, Transcript: repository, SkillDirectories: []string{v11SkillsDir(t)}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
		_ = store.Close()
	})
	response := compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/request", "local", map[string]any{
		"target_agent":    "ONBOARDING",
		"project_id":      "project-onboarding-suggestions",
		"intent_id":       "00000000-0000-4000-8000-000000000201",
		"onboarding_mode": "structured_wizard_v1",
		"artifact_refs": []any{map[string]any{
			"artifact_id": artifact.ID, "version_id": version.ID,
		}},
		"input_data": map[string]any{
			"request": "USER_DATA contains a description of an inflammasome imaging workflow.",
			"USER_DATA": map[string]any{
				"selfDescription":   "I study NEK7 inflammasome microscopy and need reproducible image quality-control workflows.",
				"attachedFilenames": []any{"lab-readme.md"},
			},
		},
	}, http.StatusOK)
	frameID := stringValue(response["frame_id"])
	if frameID == "" {
		t.Fatalf("submit response=%#v", response)
	}
	restartContext, restartCancel := context.WithTimeout(context.Background(), time.Second)
	if err := srv.Close(restartContext); err != nil {
		restartCancel()
		t.Fatal(err)
	}
	restartCancel()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err = store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv = New(Options{FileRoot: runtimeRoot, Workspace: store, Transcript: repository, SkillDirectories: []string{v11SkillsDir(t)}})
	workspaceAccess, workspaceDir, authorized := srv.resolveAgentWorkspaceAuthority(context.Background(), frameID)
	if !authorized || workspaceAccess.UserID != "local" || workspaceAccess.Frame.ID != frameID ||
		workspaceAccess.Frame.ProjectID != "project-onboarding-suggestions" || !filepath.IsAbs(workspaceDir) {
		t.Fatalf("restarted workspace authority: access=%#v workspace=%q authorized=%t", workspaceAccess, workspaceDir, authorized)
	}
	if identity := srv.resolveAgentKernelContext(context.Background(), frameID); identity != nil {
		t.Fatalf("workspace-only attachment authority granted unavailable kernel execution: %#v", identity)
	}
	cycle, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: frameID, RunnerID: "onboarding-suggestion-runner",
		Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "test-model",
		LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 8192, MaxToolRounds: 3,
	})
	if err != nil || !cycle.Claimed || cycle.Status != "awaiting_user_response" || requests.Load() != 4 {
		t.Fatalf("cycle=%#v requests=%d err=%v", cycle, requests.Load(), err)
	}
	frame := compatJSONRequest(t, srv.Handler(), http.MethodGet, "/api/frames/"+frameID+"?shallow=true", "local", nil, http.StatusOK)
	output, _ := frame["output_data"].(map[string]any)
	pending, _ := output["pending_input_requests"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pending requests=%#v frame=%#v", pending, frame)
	}
	request, _ := pending[0].(map[string]any)
	if request["kind"] != "ask" || request["tool_name"] != "ask_user" {
		t.Fatalf("structured suggestion request identity=%#v", request)
	}
	questions, _ := request["questions"].([]any)
	question, _ := questions[0].(map[string]any)
	options, _ := question["options"].([]any)
	if len(questions) != 1 || len(options) != 3 || question["header"] == "Permissions" {
		t.Fatalf("structured suggestion request=%#v", request)
	}
	audits, err := srv.runtimeStore.List(toolGatewayAuditNamespace)
	if err != nil {
		t.Fatal(err)
	}
	auditJSON, _ := json.Marshal(audits)
	if strings.Contains(string(auditJSON), "NEK7 inflammasome microscopy") ||
		!strings.Contains(string(auditJSON), `"content_omitted":true`) {
		t.Fatalf("tool audit leaked attachment content or omitted no redaction marker: %s", auditJSON)
	}

	stream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", frameID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	config, found, err := repository.LatestFrameRuntimeConfig(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || config["onboardingMode"] != "structured_wizard_v1" {
		t.Fatalf("runtime config=%#v found=%t err=%v", config, found, err)
	}
	replay, err := repository.ListRunnerReplay(context.Background(), transcriptstore.ListRunnerReplayInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, MessageLimit: 100, CheckpointLimit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	replayJSON, _ := json.Marshal(replay)
	if strings.Contains(string(replayJSON), "NEK7 inflammasome microscopy") {
		t.Fatalf("runner checkpoint leaked attachment content: %s", replayJSON)
	}
	direct, err := (serverAgentRuntimeToolGateway{
		server: srv, allowedTools: []string{onboardingReadAttachmentToolName},
		sessionID: frameID, outputLimitBytes: defaultSessionRunnerOutputLimitBytes,
	}).Execute(context.Background(), agentruntime.ToolCall{
		ID: "direct-read", Name: onboardingReadAttachmentToolName,
		Arguments: json.RawMessage(`{"version_id":"` + version.ID + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	directValue, _ := direct.Value.(map[string]any)
	if directValue["error"] != "onboarding attachment authority is unavailable" {
		t.Fatalf("direct tool result=%#v", direct.Value)
	}
}

func TestReadOnboardingAttachmentChunkUsesUTF8AlignedBoundedCursor(t *testing.T) {
	content := []byte(strings.Repeat("αβγ scientific evidence\n", 512))
	digest := sha256.Sum256(content)
	wantChecksum := hex.EncodeToString(digest[:])
	const outputLimit = int64(5 * 1024)
	var rebuilt bytes.Buffer
	var offset int64
	var continuationChecksum string
	for calls := 0; calls < 32; calls++ {
		var chunk []byte
		var err error
		if offset == 0 {
			var size int64
			chunk, size, continuationChecksum, err = readOnboardingAttachmentChunk(
				context.Background(), bytes.NewReader(content), offset, outputLimit,
			)
			if err != nil || size != int64(len(content)) || continuationChecksum != wantChecksum {
				t.Fatalf("initial chunk size=%d checksum=%q err=%v", size, continuationChecksum, err)
			}
		} else {
			chunk, err = readOnboardingAttachmentContinuation(
				context.Background(), bytes.NewReader(content), offset, outputLimit, int64(len(content)),
			)
			if err != nil {
				t.Fatalf("continuation offset=%d err=%v", offset, err)
			}
		}
		result, err := fitStructuredOnboardingAttachmentResult(map[string]any{
			"artifact_id": "artifact-large", "version_id": "version-large", "filename": "large.txt",
			"content_type": "text/plain", "size_bytes": len(content), "checksum": continuationChecksum,
		}, chunk, offset, int64(len(content)), outputLimit)
		if err != nil {
			t.Fatalf("fit chunk offset=%d: %v", offset, err)
		}
		encoded, err := json.Marshal(result)
		if err != nil || int64(len(encoded)) > outputLimit {
			t.Fatalf("encoded chunk bytes=%d err=%v result=%#v", len(encoded), err, result)
		}
		piece := stringValue(result["content"])
		if !utf8.ValidString(piece) || piece == "" {
			t.Fatalf("invalid empty or non-UTF8 chunk at offset=%d", offset)
		}
		_, _ = rebuilt.WriteString(piece)
		next := int64(numberValue(result["next_offset_bytes"]))
		if next <= offset {
			t.Fatalf("cursor did not advance: offset=%d next=%d", offset, next)
		}
		offset = next
		if eof, _ := result["eof"].(bool); eof {
			if !bytes.Equal(rebuilt.Bytes(), content) {
				t.Fatal("chunked content did not reconstruct the exact attachment")
			}
			break
		}
		if calls == 31 {
			t.Fatal("chunked reader did not reach eof")
		}
	}
	if _, err := readOnboardingAttachmentContinuation(
		context.Background(), bytes.NewReader(content), 1, outputLimit, int64(len(content)),
	); err == nil || err.Error() != "onboarding attachment text extraction is unavailable" {
		t.Fatalf("mid-rune offset error=%v", err)
	}
}
