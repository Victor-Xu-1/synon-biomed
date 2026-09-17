package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityRootForkUsesCanonicalTranscriptAuthority(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-branch-api", "frame-branch-api")
	if _, err := db.Exec(`UPDATE frames SET agent_name='OPERON' WHERE id='frame-branch-api'`); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-branch-api", OwnerID: "local", ExternalID: "frame-branch-api",
		SessionID: "frame-branch-api", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-branch-api", RootFrameID: "frame-branch-api", FrameID: "frame-branch-api", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "branch-api-source",
		FrameEventID: "branch-api-source-frame", MessageUUID: "branch-api-source-message",
		MessageOrigin: "task_intent", Text: "Original request", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("source created=%t err=%v", created, err)
	}
	srv := newV11TestServer(t, Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	baseBranchID, _, _ := transcriptstore.BaseBranchIdentity(stream.UID)
	body := map[string]any{
		"message_index": 0, "edited_content": "Corrected request",
		"verifier_mode": "on", "memory_mode": "off", "target_agent": "OPERON",
		"expected_branch_id": baseBranchID, "expected_generation": 1,
	}
	first := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-branch-api/fork", "local", body, http.StatusOK,
	)
	branchID, _ := first["branch_id"].(string)
	if first["root_frame_id"] != "frame-branch-api" || first["status"] != "accepted" || branchID == "" {
		t.Fatalf("fork response=%#v", first)
	}
	retry := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-branch-api/fork", "local", body, http.StatusOK,
	)
	if retry["branch_id"] != branchID {
		t.Fatalf("retry response=%#v first=%#v", retry, first)
	}
	branches := compatJSONRequest(
		t, srv.Handler(), http.MethodGet, "/api/frames/frame-branch-api/branches", "local", nil, http.StatusOK,
	)
	if branches["active_branch_id"] != branchID || branches["generation"] != float64(2) {
		t.Fatalf("branches response=%#v", branches)
	}
	items, _ := branches["branches"].([]any)
	if len(items) != 2 {
		t.Fatalf("branches response=%#v", branches)
	}
	baseMessages := compatJSONRequest(
		t, srv.Handler(), http.MethodGet,
		"/api/frames/frame-branch-api/branches/"+baseBranchID+"/messages", "local", nil, http.StatusOK,
	)
	baseHistory, _ := baseMessages["messages"].([]any)
	if baseMessages["branch_id"] != baseBranchID || baseMessages["generation"] != float64(2) || len(baseHistory) != 1 {
		t.Fatalf("base branch messages=%#v", baseMessages)
	}
	webPage := p3JSONRequest(
		t, srv, http.MethodGet,
		"/api/conversations/frame-branch-api/messages?branch_id="+baseBranchID+"&limit=10", nil, "local",
	)
	if webPage.Code != http.StatusOK {
		t.Fatalf("web branch status=%d body=%s", webPage.Code, webPage.Body.String())
	}
	var webPayload map[string]any
	if err := json.Unmarshal(webPage.Body.Bytes(), &webPayload); err != nil {
		t.Fatal(err)
	}
	webItems, _ := webPayload["items"].([]any)
	if len(webItems) != 1 {
		t.Fatalf("web branch page=%#v", webPayload)
	}
	webMessage, _ := webItems[0].(map[string]any)
	webMessageID := webString(webMessage["id"])
	singleBranchMessage := p3JSONRequest(
		t, srv, http.MethodGet,
		"/api/conversations/frame-branch-api/messages/"+url.PathEscape(webMessageID)+
			"?branch_id="+url.QueryEscape(baseBranchID), nil, "local",
	)
	if singleBranchMessage.Code != http.StatusOK {
		t.Fatalf("single branch message status=%d body=%s", singleBranchMessage.Code, singleBranchMessage.Body.String())
	}
	singleBranchPayload := p3DecodeObject(t, singleBranchMessage)
	if webString(singleBranchPayload["id"]) != webMessageID {
		t.Fatalf("single branch message=%#v", singleBranchPayload)
	}
	webContent, _ := webMessage["content"].(map[string]any)
	webMetadata, _ := webContent["synonBiomed"].(map[string]any)
	if webMetadata["messageIndex"] != float64(0) || webMetadata["blockIndex"] != float64(0) ||
		webMetadata["branchId"] != baseBranchID {
		t.Fatalf("web branch metadata=%#v", webPayload)
	}
	baseCursor, _ := webPayload["newest_cursor"].(string)
	if baseCursor == "" {
		t.Fatalf("web branch cursor=%#v", webPayload)
	}
	crossBranch := p3JSONRequest(
		t, srv, http.MethodGet,
		"/api/conversations/frame-branch-api/messages?branch_id="+branchID+"&after="+baseCursor, nil, "local",
	)
	if crossBranch.Code != http.StatusBadRequest {
		t.Fatalf("cross-branch cursor status=%d body=%s", crossBranch.Code, crossBranch.Body.String())
	}
	second := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-branch-api/fork", "local", map[string]any{
			"message_index": 0, "edited_content": "Second correction", "source_branch_id": branchID,
		}, http.StatusOK,
	)
	if second["branch_id"] == branchID || second["branch_id"] == "" {
		t.Fatalf("second fork=%#v", second)
	}
	secondBranchID, _ := second["branch_id"].(string)
	stale := p3JSONRequest(
		t, srv, http.MethodGet,
		"/api/conversations/frame-branch-api/messages?branch_id="+baseBranchID+"&after="+baseCursor, nil, "local",
	)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale cursor status=%d body=%s", stale.Code, stale.Body.String())
	}
	state, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state.ActiveBranchID != second["branch_id"] || state.Generation != 3 {
		t.Fatalf("branch state=%#v err=%v", state, err)
	}
	completed := "completed"
	if _, err := store.UpdateFrame(stream.FrameID, workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	continuation := map[string]any{
		"input_data":       map[string]any{"request": "Continue the original branch"},
		"target_branch_id": baseBranchID, "expected_branch_id": secondBranchID,
		"expected_generation": 3, "client_mutation_id": "continue-original-1",
	}
	continued := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-branch-api/message", "local", continuation, http.StatusOK,
	)
	if continued["status"] != "accepted" || continued["message_id"] != "branch-continue:continue-original-1" {
		t.Fatalf("continuation response=%#v", continued)
	}
	retryContinuation := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-branch-api/message", "local", continuation, http.StatusOK,
	)
	if retryContinuation["message_id"] != continued["message_id"] {
		t.Fatalf("continuation retry=%#v first=%#v", retryContinuation, continued)
	}
	state, err = repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state.ActiveBranchID != baseBranchID || state.Generation != 4 {
		t.Fatalf("continued branch state=%#v err=%v", state, err)
	}
	activePage := p3JSONRequest(
		t, srv, http.MethodGet, "/api/conversations/frame-branch-api/messages?limit=10", nil, "local",
	)
	if activePage.Code != http.StatusOK {
		t.Fatalf("active branch page status=%d body=%s", activePage.Code, activePage.Body.String())
	}
	activePayload := p3DecodeObject(t, activePage)
	if activePayload["branch_id"] != baseBranchID || activePayload["branch_generation"] != float64(4) {
		t.Fatalf("active branch page=%#v", activePayload)
	}
	activeItems, _ := activePayload["items"].([]any)
	if len(activeItems) != 2 {
		t.Fatalf("active branch continuation page=%#v", activePayload)
	}
	if _, err := store.UpdateFrame(stream.FrameID, workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	webContinuation := map[string]any{
		"content": "Continue the selected alternate branch", "loading_id": "web-continue-alternate-1",
		"session_options": map[string]any{
			"target_branch_id": secondBranchID, "expected_branch_id": baseBranchID, "expected_generation": 4,
		},
	}
	webContinued := p3JSONRequest(
		t, srv, http.MethodPost, "/api/conversations/frame-branch-api/messages", webContinuation, "local",
	)
	if webContinued.Code != http.StatusAccepted {
		t.Fatalf("web continuation status=%d body=%s", webContinued.Code, webContinued.Body.String())
	}
	webContinuedPayload := p3DecodeObject(t, webContinued)
	if webContinuedPayload["msg_id"] != "branch-continue:web-continue-alternate-1" {
		t.Fatalf("web continuation=%#v", webContinuedPayload)
	}
	webRetry := p3JSONRequest(
		t, srv, http.MethodPost, "/api/conversations/frame-branch-api/messages", webContinuation, "local",
	)
	if webRetry.Code != http.StatusAccepted || p3DecodeObject(t, webRetry)["msg_id"] != webContinuedPayload["msg_id"] {
		t.Fatalf("web retry status=%d body=%s", webRetry.Code, webRetry.Body.String())
	}
	state, err = repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || state.ActiveBranchID != secondBranchID || state.Generation != 5 {
		t.Fatalf("web continued branch state=%#v err=%v", state, err)
	}
	var legacyBranches int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_branch_archives WHERE frame_id=?`, stream.FrameID).Scan(&legacyBranches); err != nil {
		t.Fatal(err)
	}
	if legacyBranches != 0 {
		t.Fatalf("legacy branch archives=%d", legacyBranches)
	}
	missing := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-branch-api/fork", "local", map[string]any{
			"message_index": 99, "edited_content": "Missing source",
		}, http.StatusNotFound,
	)
	if missing["detail"] != "Source message not found" {
		t.Fatalf("missing response=%#v", missing)
	}
}

func TestBranchContinuationRejectsMissingCanonicalStreamWithoutCreatingAuthority(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-branch-missing", "frame-branch-missing")
	srv := newV11TestServer(t, Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	response := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-branch-missing/message", "local",
		map[string]any{
			"input_data":       map[string]any{"request": "must not create a stream"},
			"target_branch_id": "br_deadbeef", "expected_branch_id": "br_00000001",
			"expected_generation": 1, "client_mutation_id": "missing-stream-1",
		},
		http.StatusNotFound,
	)
	if response["detail"] != "Branch not found" {
		t.Fatalf("missing stream response=%#v", response)
	}
	var streams, branches, events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams WHERE session_id='frame-branch-missing'`).Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_branches`).Scan(&branches); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE frame_id='frame-branch-missing'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if streams != 0 || branches != 0 || events != 0 {
		t.Fatalf("missing target mutated streams=%d branches=%d events=%d", streams, branches, events)
	}
}

func TestCompatibilityRootForkAtAnswerUsesCanonicalTranscriptAuthority(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-answer-api", "frame-answer-api")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-answer-api", OwnerID: "local", ExternalID: "frame-answer-api",
		SessionID: "frame-answer-api", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-answer-api", RootFrameID: "frame-answer-api", FrameID: "frame-answer-api", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "answer-api-source",
		FrameEventID: "answer-api-source-frame", MessageUUID: "answer-api-source-message",
		MessageOrigin: "task_intent", Text: "Ask first", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("source created=%t err=%v", created, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "answer-api-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	parked, _, err := store.ParkAskUserWithTranscript(context.Background(), workspace.ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-api", ToolName: "AskUserQuestion",
		Questions: []any{map[string]any{
			"question": "Which structure?", "header": "Structure",
			"options": []any{
				map[string]any{"label": "5FQD", "description": "Use the human CRBN complex."},
				map[string]any{"label": "Predicted", "description": "Use a predicted structure."},
			},
		}},
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim.Claim, ClientMessageID: "pause-answer-api",
		Phase: transcriptstore.RunnerPhaseWaitingUser, Resumable: true,
		PayloadJSON: []byte(`{"status":"awaiting_user_response"}`), Destinations: []string{"ws"},
	})
	if err != nil || len(parked.Events) != 3 {
		t.Fatalf("parked=%#v err=%v", parked, err)
	}
	srv := newV11TestServer(t, Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	baseBranchID, _, _ := transcriptstore.BaseBranchIdentity(stream.UID)
	body := map[string]any{
		"tool_use_id":   "ask-api",
		"response":      map[string]any{"action": "answer", "answers": map[string]any{"Which structure?": "5FQD"}},
		"verifier_mode": "on", "memory_mode": "off", "target_agent": "OPERON",
		"expected_branch_id": baseBranchID, "expected_generation": 1,
	}
	first := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-answer-api/fork-at-answer", "local", body, http.StatusOK,
	)
	branchID, _ := first["branch_id"].(string)
	if first["root_frame_id"] != "frame-answer-api" || first["status"] != "accepted" || branchID == "" {
		t.Fatalf("fork-at-answer response=%#v", first)
	}
	retry := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-answer-api/fork-at-answer", "local", body, http.StatusOK,
	)
	if retry["branch_id"] != branchID {
		t.Fatalf("retry response=%#v first=%#v", retry, first)
	}
	branchMessages, _, found, err := srv.loadTranscriptWebBranchHistory(
		context.Background(), stream.OwnerID, stream.SessionID, branchID,
	)
	if err != nil || !found {
		t.Fatalf("branch history found=%t err=%v", found, err)
	}
	askUserCount := 0
	for _, message := range branchMessages {
		content, _ := message["content"].(map[string]any)
		if message["type"] != "tool_call" || content["call_id"] != "ask-api" {
			continue
		}
		askUserCount++
		output := webString(content["output"])
		if message["status"] != "finish" || content["status"] != "completed" ||
			!strings.Contains(output, `"status":"answered"`) || strings.Contains(output, "awaiting_user_response") {
			t.Fatalf("branch AskUser history=%#v", message)
		}
	}
	if askUserCount != 1 {
		t.Fatalf("branch AskUser count=%d history=%#v", askUserCount, branchMessages)
	}
	runtimeConfig, found, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || runtimeConfig["verifier_mode"] != "on" || runtimeConfig["memory_mode"] != "off" ||
		runtimeConfig["target_agent"] != "OPERON" {
		t.Fatalf("runtime config=%#v found=%t err=%v", runtimeConfig, found, err)
	}
	var legacyBranches int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frame_branch_archives WHERE frame_id=?`, stream.FrameID).Scan(&legacyBranches); err != nil {
		t.Fatal(err)
	}
	if legacyBranches != 0 {
		t.Fatalf("legacy branch archives=%d", legacyBranches)
	}
	missing := compatJSONRequest(
		t, srv.Handler(), http.MethodPost, "/api/frames/frame-answer-api/fork-at-answer", "local", map[string]any{
			"tool_use_id": "missing", "response": map[string]any{"action": "cancel"},
		}, http.StatusNotFound,
	)
	if missing["detail"] != "tool_use_id missing not found in messages" {
		t.Fatalf("missing response=%#v", missing)
	}
}
