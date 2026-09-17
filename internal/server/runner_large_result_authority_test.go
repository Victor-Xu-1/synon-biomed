package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRunnerLargeToolResultPreviewUsesCompactEvidenceWindow(t *testing.T) {
	raw := []byte(`{"ok":true,"result":"` + strings.Repeat("科学-evidence-", 12_000) + `"}`)
	digest := sha256.Sum256(raw)
	input := agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "large-preview", Name: "WebFetch"},
		RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 64 << 10,
	}
	descriptor, err := buildRunnerLargeToolResultDescriptor(
		context.Background(),
		input,
		workspace.RunnerLargeToolResult{
			ArtifactID: "large-tool-result-" + strings.Repeat("a", 32), VersionID: "ltr-preview",
			ContentType: "application/json", SizeBytes: int64(len(raw)),
			ContentSHA256: hex.EncodeToString(digest[:]),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if len([]byte(descriptor.Preview)) == 0 ||
		int64(len(encoded)) > min(input.MaxInlineBytes, runnerLargeToolResultInlineLimitBytes) ||
		!descriptor.Truncated || !json.Valid([]byte(descriptor.Preview)) ||
		descriptor.ReadWith != `read_file(version_id="ltr-preview")` {
		t.Fatalf("preview-bytes=%d descriptor-bytes=%d truncated=%t", len([]byte(descriptor.Preview)), len(encoded), descriptor.Truncated)
	}
	if err := (runnerLargeToolResultAuthority{}).ValidatePreview(context.Background(), input, descriptor); err != nil {
		t.Fatalf("reading preview must be derived from exact source bytes: %v", err)
	}
}

func TestRunnerEngineSeparatesGatewayCaptureFromLargeResultInlineLimit(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	engine := fixture.server.newAgentRuntimeEngineWithContext(context.Background(), SessionRunnerChatOptions{
		SessionID: fixture.stream.SessionID, OutputLimitBytes: 50_000,
	})
	if engine.MaxToolResultBytes != 16_000 {
		t.Fatalf("large result inline limit=%d, want 16000 while gateway capture remains 50000", engine.MaxToolResultBytes)
	}
	bounded := fixture.server.newAgentRuntimeEngineWithContext(context.Background(), SessionRunnerChatOptions{
		SessionID: fixture.stream.SessionID, OutputLimitBytes: 512,
	})
	if bounded.MaxToolResultBytes != 512 {
		t.Fatalf("explicit smaller inline limit=%d, want 512", bounded.MaxToolResultBytes)
	}
	legacy := New(Options{FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := legacy.Close(ctx); err != nil {
			t.Errorf("close legacy server: %v", err)
		}
	})
	legacyEngine := legacy.newAgentRuntimeEngineWithContext(context.Background(), SessionRunnerChatOptions{
		SessionID: "legacy-result-boundary", OutputLimitBytes: 50_000,
	})
	if legacyEngine.MaxToolResultBytes != 50_000 || legacyEngine.LargeToolResults != nil {
		t.Fatalf("legacy engine unexpectedly changed inline authority=%T limit=%d", legacyEngine.LargeToolResults, legacyEngine.MaxToolResultBytes)
	}
}

func TestRunnerEngineExternalizesResultBelowGatewayCaptureBudget(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx, _ := appendLargeToolResultSource(t, fixture, "large-between-budgets", "WebFetch")
	engine := fixture.server.newAgentRuntimeEngineWithContext(ctx, SessionRunnerChatOptions{
		SessionID: fixture.stream.SessionID, OutputLimitBytes: 50_000,
	})
	value := map[string]any{"ok": true, "result": strings.Repeat("source-evidence-", 1_500)}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 16_000 || len(raw) >= 50_000 {
		t.Fatalf("fixture bytes=%d, want between compact and gateway limits", len(raw))
	}
	materialized, err := engine.MaterializeToolResult(ctx, agentruntime.ToolCall{
		ID: "large-between-budgets", Name: "WebFetch",
	}, value, agentruntime.ToolResultSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor agentruntime.LargeToolResultDescriptor
	if err := json.Unmarshal(materialized.JSON, &descriptor); err != nil {
		t.Fatal(err)
	}
	if materialized.ResultRef != "artifact-version:"+descriptor.VersionID ||
		len([]byte(descriptor.Preview)) == 0 || int64(len(materialized.JSON)) > engine.MaxToolResultBytes ||
		!json.Valid([]byte(descriptor.Preview)) ||
		descriptor.SizeBytes != int64(len(raw)) || !descriptor.Truncated {
		t.Fatalf("materialized=%#v descriptor=%#v", materialized, descriptor)
	}
	if err := (runnerLargeToolResultAuthority{}).ValidatePreview(ctx, agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "large-between-budgets", Name: "WebFetch"},
		RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: engine.MaxToolResultBytes,
	}, descriptor); err != nil {
		t.Fatalf("reading preview must preserve immutable source authority: %v", err)
	}
	assertLargeResultExactContentHTTP(t, fixture.server.Handler(), fixture.stream.OwnerID, descriptor, raw)
}

func TestRunnerLargeToolResultAuthorityPersistsExactVersionAndReplaysAcrossRestart(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultSource(t, fixture, "large-call-1", "WebFetch")
	authority := runnerLargeToolResultAuthority{server: fixture.server}
	raw := []byte(`{"ok":true,"result":"` + strings.Repeat("exact-scientific-evidence-", 600) + `"}`)
	input := agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "large-call-1", Name: "WebFetch"},
		RawJSON:  raw, Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: 1024,
	}

	first, err := authority.Externalize(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := authority.Externalize(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.ArtifactID == "" || first.VersionID == "" || first.SHA256 == "" ||
		first.SizeBytes != int64(len(raw)) || first.ContentType != "application/json" ||
		first.Outcome != agentruntime.ToolResultSucceeded || first.ContentURL == "" ||
		!first.Truncated || first.Preview == "" || !bytes.HasPrefix(raw, []byte(first.Preview)) {
		t.Fatalf("descriptors first=%#v second=%#v", first, second)
	}
	encoded, err := json.Marshal(first)
	if err != nil || int64(len(encoded)) > input.MaxInlineBytes {
		t.Fatalf("descriptor bytes=%d err=%v: %s", len(encoded), err, encoded)
	}
	assertLargeResultLedgerCounts(t, fixture.db, 1, 0)

	reopenedStore, err := workspace.Open(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	reopenedRepo, err := reopenedStore.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(Options{Workspace: reopenedStore, Transcript: reopenedRepo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := restarted.Close(closeCtx); err != nil {
			t.Errorf("close restarted server: %v", err)
		}
	})
	restartedCtx := withTranscriptRunnerChatRun(context.Background(), run)
	replayed, err := (runnerLargeToolResultAuthority{server: restarted}).Externalize(restartedCtx, input)
	if err != nil || replayed != first {
		t.Fatalf("restart replay=%#v err=%v, want %#v", replayed, err, first)
	}
	assertLargeResultLedgerCounts(t, fixture.db, 1, 0)

	assertLargeResultExactContentHTTP(t, restarted.Handler(), fixture.stream.OwnerID, first, raw)

	changed := input
	changed.RawJSON = []byte(`{"ok":true,"result":"` + strings.Repeat("changed-scientific-evidence-", 600) + `"}`)
	if _, err := (runnerLargeToolResultAuthority{server: restarted}).Externalize(restartedCtx, changed); !errors.Is(err, errRunnerLargeToolResultConflict) {
		t.Fatalf("changed-byte replay error=%v", err)
	}
	assertLargeResultLedgerCounts(t, fixture.db, 1, 0)
}

func TestResumePendingNonKernelToolUsesPersistedLargeUnavailableResult(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	options := SessionRunnerChatOptions{
		SessionID: fixture.stream.SessionID, RunnerID: fixture.claim.RunnerID, OutputLimitBytes: 512,
	}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	call := agentruntime.ToolCall{
		ID: "large-unavailable-recovery", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://example.test/missing"}`),
	}
	if err := fixture.server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointChatTool(
		options, run, "running", "tool WebFetch started", call.ID, "start",
		map[string]any{"toolName": call.Name, "toolInput": decodeToolEventJSON(string(call.Arguments))},
	); err != nil {
		t.Fatal(err)
	}
	batchID := run.ToolBatchIDs[call.ID]
	items, err := fixture.store.ListToolCallBatchItems(context.Background(), fixture.stream.OwnerID, batchID)
	if err != nil || len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateRunning ||
		items[0].StartedEventID != run.ToolSourceEventIDs[call.ID] {
		t.Fatalf("started items=%#v source=%d err=%v", items, run.ToolSourceEventIDs[call.ID], err)
	}
	raw := []byte(`{"ok":true,"sourceUnavailable":true,"statusCode":404,"error":"HTTP source returned 404 Not Found.","body":"` +
		strings.Repeat("missing-source-evidence-", 256) + `"}`)
	descriptor, err := (runnerLargeToolResultAuthority{server: fixture.server}).Externalize(
		withTranscriptRunnerChatRun(context.Background(), run),
		agentruntime.LargeToolResultInput{
			ToolCall: call, RawJSON: raw, Outcome: agentruntime.ToolResultUnavailable, MaxInlineBytes: options.OutputLimitBytes,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a runner restart after the immutable write committed but before
	// the terminal lifecycle checkpoint was appended.
	restartedRun := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	resumed, err := fixture.server.resumePendingAgentToolCalls(context.Background(), options, restartedRun)
	if err != nil || !resumed {
		t.Fatalf("resumed=%t err=%v", resumed, err)
	}
	items, err = fixture.store.ListToolCallBatchItems(context.Background(), fixture.stream.OwnerID, batchID)
	if err != nil || len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateCompleted ||
		items[0].ResultRef != "artifact-version:"+descriptor.VersionID || items[0].TerminalEventID <= 0 {
		t.Fatalf("recovered items=%#v descriptor=%#v err=%v", items, descriptor, err)
	}
	assertLargeResultLedgerCounts(t, fixture.db, 1, 0)
}

func TestMaterializeKernelResultUsesDurableOperationAuthorityAfterRunnerLease(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	ctx, run := appendLargeToolResultModelBatchSource(t, fixture, "large-call-recovery", "python")
	_ = ctx
	sourceEventID := run.ToolSourceEventIDs["large-call-recovery"]
	operation := workspace.KernelLocalOperation{
		OperationID:         "kop-large-recovery-authority",
		OwnerUserID:         fixture.stream.OwnerID,
		ProjectID:           fixture.stream.ProjectID,
		RootFrameID:         fixture.stream.RootFrameID,
		FrameID:             fixture.stream.FrameID,
		StreamUID:           fixture.stream.UID,
		SourceEventID:       sourceEventID,
		SourceRunnerAttempt: fixture.claim.Attempt,
		RunnerID:            fixture.claim.RunnerID,
		ToolCallID:          "large-call-recovery",
		Tool:                "python",
	}
	result := map[string]any{
		"ok": true, "exit_status": "ok", "stdout": strings.Repeat("detached-recovery-evidence-", 4000),
		"stderr": "", "exec_id": "exec-large-recovery", "tool_use_id": operation.ToolCallID,
		"kernel_id": "kernel-large-recovery", "kernel_kind": "analysis", "kernel_reused": false,
		"cell_index": 0, "files_written": []any{}, "dropped_roots": []string{},
	}
	first, err := fixture.server.materializeAgentKernelTerminalResult(
		context.Background(), operation, result, 1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.server.materializeAgentKernelTerminalResult(
		context.Background(), operation, result, 1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.ResultRef == "" || !strings.HasPrefix(first.ResultRef, "artifact-version:") ||
		!bytes.Equal(first.JSON, second.JSON) || first.ResultRef != second.ResultRef {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	assertLargeResultLedgerCounts(t, fixture.db, 1, 0)
}

func TestRunnerLargeToolResultAuthorityRejectsLegacyAndMismatchedAuthority(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	canonicalEngine := fixture.server.newAgentRuntimeEngineWithContext(context.Background(), SessionRunnerChatOptions{
		SessionID: fixture.stream.SessionID, OutputLimitBytes: 512,
	})
	if canonicalEngine.LargeToolResults == nil {
		t.Fatal("canonical transcript runner did not receive the large-result authority")
	}
	legacy := New(Options{FileRoot: t.TempDir()})
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := legacy.Close(closeCtx); err != nil {
			t.Errorf("close legacy server: %v", err)
		}
	})
	legacyEngine := legacy.newAgentRuntimeEngineWithContext(context.Background(), SessionRunnerChatOptions{
		SessionID: "legacy-session", OutputLimitBytes: 512,
	})
	if legacyEngine.LargeToolResults != nil {
		t.Fatal("legacy runner received the canonical transcript large-result authority")
	}
	ctx, run := appendLargeToolResultSource(t, fixture, "large-call-auth", "WebFetch")
	authority := runnerLargeToolResultAuthority{server: fixture.server}
	input := agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: "large-call-auth", Name: "WebFetch"},
		RawJSON:  []byte(`{"ok":true,"result":"` + strings.Repeat("x", 2048) + `"}`),
		Outcome:  agentruntime.ToolResultSucceeded, MaxInlineBytes: 512,
	}

	if _, err := authority.Externalize(context.Background(), input); !errors.Is(err, errRunnerLargeToolResultAuthority) {
		t.Fatalf("legacy context error=%v", err)
	}

	ownerRun := cloneLargeResultRun(run)
	ownerRun.Transcript.Stream.OwnerID = "owner-other"
	if _, err := authority.Externalize(withTranscriptRunnerChatRun(context.Background(), ownerRun), input); !errors.Is(err, errRunnerLargeToolResultAuthority) {
		t.Fatalf("owner mismatch error=%v", err)
	}

	claimRun := cloneLargeResultRun(run)
	claimRun.Transcript.Claim.ClaimToken = "wrong-claim-token"
	if _, err := authority.Externalize(withTranscriptRunnerChatRun(context.Background(), claimRun), input); !errors.Is(err, errRunnerLargeToolResultAuthority) {
		t.Fatalf("claim mismatch error=%v", err)
	}

	toolName := input
	toolName.ToolCall.Name = "Read"
	if _, err := authority.Externalize(ctx, toolName); !errors.Is(err, errRunnerLargeToolResultAuthority) {
		t.Fatalf("tool name mismatch error=%v", err)
	}
	assertLargeResultLedgerCounts(t, fixture.db, 0, 0)
}

func appendLargeToolResultSource(
	t *testing.T,
	fixture *agentSaveArtifactsFixture,
	toolCallID, toolName string,
) (context.Context, *sessionRunnerChatRun) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"status": "running", "toolCallId": toolCallID, "toolName": toolName,
		"toolPhase": "start", "toolInput": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, source, created, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "large-result-source-" + toolCallID,
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload, Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("append source created=%t err=%v", created, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript:         &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
		ToolSourceEventIDs: map[string]int64{toolCallID: source.EventID},
	}
	return withTranscriptRunnerChatRun(context.Background(), run), run
}

func appendLargeToolResultModelBatchSource(
	t *testing.T,
	fixture *agentSaveArtifactsFixture,
	toolCallID, toolName string,
) (context.Context, *sessionRunnerChatRun) {
	t.Helper()
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	if err := fixture.server.checkpointChatModelToolCalls(
		SessionRunnerChatOptions{RunnerID: fixture.claim.RunnerID}, run,
		[]agentruntime.ToolCall{{ID: toolCallID, Name: toolName,
			Arguments: json.RawMessage(`{"code":"print(1)","environment":"python"}`)}},
	); err != nil {
		t.Fatal(err)
	}
	operation, found, err := fixture.store.GetKernelLocalOperationByToolCall(
		context.Background(), fixture.stream.OwnerID, fixture.stream.UID, toolCallID,
	)
	if err != nil || !found {
		t.Fatalf("model source operation=%#v found=%t err=%v", operation, found, err)
	}
	run.ToolSourceEventIDs = map[string]int64{toolCallID: operation.SourceEventID}
	return withTranscriptRunnerChatRun(context.Background(), run), run
}

func cloneLargeResultRun(run *sessionRunnerChatRun) *sessionRunnerChatRun {
	authority := *run.Transcript
	cloned := &sessionRunnerChatRun{
		SessionID: run.SessionID, Attempt: run.Attempt, ClaimToken: run.ClaimToken,
		Transcript:         &authority,
		ToolSourceEventIDs: make(map[string]int64, len(run.ToolSourceEventIDs)),
	}
	for key, value := range run.ToolSourceEventIDs {
		cloned.ToolSourceEventIDs[key] = value
	}
	return cloned
}

func assertLargeResultLedgerCounts(t *testing.T, db *sql.DB, evidence, artifactRows int) {
	t.Helper()
	for table, want := range map[string]int{
		"artifact_versions": artifactRows, "transcript_artifact_commits": artifactRows,
		"artifacts": artifactRows, "runner_large_tool_results": evidence,
	} {
		var got int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count=%d, want %d", table, got, want)
		}
	}
}

func assertLargeResultExactContentHTTP(
	t *testing.T,
	handler http.Handler,
	ownerID string,
	descriptor agentruntime.LargeToolResultDescriptor,
	raw []byte,
) {
	t.Helper()
	request := func(method string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, descriptor.ContentURL, nil)
		req.Header.Set("X-Synon-User-Id", ownerID)
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	get := request(http.MethodGet, nil)
	etag := `"sha256:` + descriptor.SHA256 + `"`
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), raw) || get.Header().Get("ETag") != etag ||
		get.Header().Get("X-Artifact-Version-Id") != descriptor.VersionID || get.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET status=%d headers=%#v body-bytes=%d", get.Code, get.Header(), get.Body.Len())
	}
	head := request(http.MethodHead, nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(raw)) {
		t.Fatalf("HEAD status=%d headers=%#v body=%q", head.Code, head.Header(), head.Body.String())
	}
	rangeStart, rangeEnd := 7, 31
	ranged := request(http.MethodGet, map[string]string{"Range": "bytes=7-31"})
	if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), raw[rangeStart:rangeEnd+1]) {
		t.Fatalf("Range status=%d headers=%#v body=%q", ranged.Code, ranged.Header(), ranged.Body.Bytes())
	}
	notModified := request(http.MethodGet, map[string]string{"If-None-Match": etag})
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("If-None-Match status=%d body=%q", notModified.Code, notModified.Body.String())
	}
	precondition := request(http.MethodGet, map[string]string{"If-Match": `"sha256:wrong"`})
	if precondition.Code != http.StatusPreconditionFailed || precondition.Body.Len() != 0 {
		t.Fatalf("If-Match mismatch status=%d body=%q", precondition.Code, precondition.Body.String())
	}
	matched := request(http.MethodGet, map[string]string{"If-Match": etag})
	if matched.Code != http.StatusOK || !bytes.Equal(matched.Body.Bytes(), raw) {
		t.Fatalf("If-Match status=%d body-bytes=%d", matched.Code, matched.Body.Len())
	}
}
