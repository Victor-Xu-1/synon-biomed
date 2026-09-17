package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

const branchAuthorityUnavailableTestDetail = "Conversation branching is unavailable until unified transcript authority is enabled"

func TestCompatibilityRootForkFailsClosedWithoutTranscriptMutation(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store, SkillDirectories: []string{v11SkillsDir(t)}})
	app := server.Handler()
	sent := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/message", "local", map[string]any{
		"input_data": map[string]any{"request": "Original request."},
	}, http.StatusOK)
	if sent["root_frame_id"] != "frame" || sent["frame_id"] != "frame" || sent["status"] != "accepted" {
		t.Fatalf("message response = %#v", sent)
	}
	cancelled := "cancelled"
	if _, err := store.UpdateFrame("frame", workspace.UpdateFrameInput{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForkCompatibilityRoot(workspace.CompatibilityRootForkInput{
		RootFrameID: "frame", MessageIndex: 0, EditedContent: "Active branch seed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateFrame("frame", workspace.UpdateFrameInput{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	inactiveBranchID := inactiveCompatibilityBranchID(t, store, "frame")
	before := branchAuthorityStateHash(t, server, store, "frame", "local")
	targeted := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/message", "local", map[string]any{
		"input_data": map[string]any{"request": "Continue inactive branch."}, "target_branch_id": inactiveBranchID,
	}, http.StatusConflict)
	if targeted["detail"] != branchAuthorityUnavailableTestDetail {
		t.Fatalf("target branch response = %#v", targeted)
	}
	if after := branchAuthorityStateHash(t, server, store, "frame", "local"); after != before {
		t.Fatalf("target branch mutated state: before=%s after=%s", before, after)
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/fork", "local", map[string]any{
		"message_index": 0, "edited_content": "Invalid agent edit.", "target_agent": "MISSING_AGENT",
	}, http.StatusBadRequest)
	if after := branchAuthorityStateHash(t, server, store, "frame", "local"); after != before {
		t.Fatalf("malformed fork mutated state: before=%s after=%s", before, after)
	}
	unavailable := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/fork", "local", map[string]any{
		"message_index": 0, "edited_content": "Edited request.",
		"verifier_mode": "on", "memory_mode": "off", "ultra_mode": false,
		"plan_mode": false, "target_agent": "OPERON",
	}, http.StatusConflict)
	if unavailable["detail"] != branchAuthorityUnavailableTestDetail {
		t.Fatalf("fork response = %#v", unavailable)
	}
	if after := branchAuthorityStateHash(t, server, store, "frame", "local"); after != before {
		t.Fatalf("unavailable fork mutated state: before=%s after=%s", before, after)
	}
	compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame/fork", "local", nil, http.StatusMethodNotAllowed)
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/missing/fork", "local", map[string]any{}, http.StatusNotFound)
}

func TestCompatibilityRootForkAtAnswerFailsClosedWithoutTranscriptMutation(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "cancelled", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []map[string]any{
		{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "ask-1", "name": "ask_user", "input": map[string]any{"question": "Which channel?"}}}, "_uuid": "assistant"},
		{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "ask-1", "content": `{"status":"awaiting_user_response"}`, "is_error": true}}, "_uuid": "result"},
		{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "trailing"}}, "_uuid": "trailing"},
	} {
		if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: "frame", Type: "user_message", Payload: message}); err != nil {
			t.Fatal(err)
		}
	}
	server := New(Options{FileRoot: root, Workspace: store, SkillDirectories: []string{v11SkillsDir(t)}})
	app := server.Handler()
	before := branchAuthorityStateHash(t, server, store, "frame", "local")
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/fork-at-answer", "local", map[string]any{
		"tool_use_id": "ask-1", "response": map[string]any{"action": "invalid"},
	}, http.StatusBadRequest)
	if after := branchAuthorityStateHash(t, server, store, "frame", "local"); after != before {
		t.Fatalf("malformed answer fork mutated state: before=%s after=%s", before, after)
	}
	unavailable := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/fork-at-answer", "local", map[string]any{
		"tool_use_id":   "ask-1",
		"response":      map[string]any{"action": "answer", "answers": map[string]any{"Which channel?": "stable"}},
		"verifier_mode": "on", "memory_mode": "off", "target_agent": "OPERON",
	}, http.StatusConflict)
	if unavailable["detail"] != branchAuthorityUnavailableTestDetail {
		t.Fatalf("answer fork response = %#v", unavailable)
	}
	if after := branchAuthorityStateHash(t, server, store, "frame", "local"); after != before {
		t.Fatalf("unavailable answer fork mutated state: before=%s after=%s", before, after)
	}
}

func TestBranchMembershipStorageFailureIsRedactedAndReadOnly(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(Options{FileRoot: root, Workspace: store, SkillDirectories: []string{v11SkillsDir(t)}})
	project := createP3Project(t, store, "storage-fault-project", "local")
	created := p3JSONRequest(t, server, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Storage fault", "assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra": map[string]any{"project_id": project.ID, "project_name": project.Name},
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	frameID := webString(p3DecodeObject(t, created)["id"])
	frameBeforeFork, found, err := store.GetFrame(frameID)
	if err != nil || !found {
		t.Fatalf("frame found=%v err=%v", found, err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: frameID, Type: "user_message", Payload: map[string]any{"role": "user", "text": "Seed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForkCompatibilityRoot(workspace.CompatibilityRootForkInput{RootFrameID: frameID, MessageIndex: 0, EditedContent: "Active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &frameBeforeFork.Status}); err != nil {
		t.Fatal(err)
	}
	branchID := inactiveCompatibilityBranchID(t, store, frameID)
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Exec(`UPDATE frame_branch_archives SET payload = '{' WHERE frame_id = ? AND branch_id = ?`, frameID, branchID); err != nil {
		t.Fatal(err)
	}
	before := branchAuthorityStateHash(t, server, store, frameID, "local")
	compat := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/"+frameID+"/message", "local", map[string]any{
		"input_data": map[string]any{"request": "Continue"}, "target_branch_id": branchID,
	}, http.StatusInternalServerError)
	if len(compat) != 1 || compat["detail"] != "Internal workspace error" {
		t.Fatalf("compat storage error = %#v", compat)
	}
	web := p3JSONRequest(t, server, http.MethodPost, "/api/conversations/"+frameID+"/messages", map[string]any{
		"content": "Continue", "session_options": map[string]any{"target_branch_id": branchID},
	}, "")
	webPayload := p3DecodeObject(t, web)
	if web.Code != http.StatusInternalServerError || len(webPayload) != 1 || webString(webPayload["message"]) != "Internal workspace error" {
		t.Fatalf("web storage error status=%d body=%s", web.Code, web.Body.String())
	}
	if after := branchAuthorityStateHash(t, server, store, frameID, "local"); after != before {
		t.Fatalf("storage failures mutated state: before=%s after=%s", before, after)
	}
}

func inactiveCompatibilityBranchID(t *testing.T, store *workspace.Store, frameID string) string {
	t.Helper()
	metadata, found, err := store.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		t.Fatalf("branch metadata found=%v err=%v", found, err)
	}
	branchMeta, _ := metadata.ContextData["_branch_meta"].(map[string]any)
	branches, _ := branchMeta["branches"].(map[string]any)
	for branchID := range branches {
		if branchID != webString(branchMeta["active_branch_id"]) {
			return branchID
		}
	}
	t.Fatalf("no inactive branch in metadata: %#v", branchMeta)
	return ""
}

func branchAuthorityStateHash(t *testing.T, server *Server, store *workspace.Store, rootFrameID, userID string) string {
	t.Helper()
	frames, err := store.ListFramesForRoot(rootFrameID)
	if err != nil {
		t.Fatal(err)
	}
	frameEvents := map[string]any{}
	metadataByFrame := map[string]any{}
	branchArchives := map[string]any{}
	for _, frame := range frames {
		events, err := store.ListFrameEvents(frame.ID, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		frameEvents[frame.ID] = events
		metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			continue
		}
		metadataByFrame[frame.ID] = metadata
		branchMeta, _ := metadata.ContextData["_branch_meta"].(map[string]any)
		branches, _ := branchMeta["branches"].(map[string]any)
		for branchID := range branches {
			messages, found, err := store.GetCompatibilityBranchMessages(frame.ID, branchID)
			branchArchives[frame.ID+":"+branchID] = map[string]any{"found": found, "messages": messages, "error": fmt.Sprint(err)}
		}
	}
	realtime, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: userID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := server.sessionStore.List()
	if err != nil {
		t.Fatal(err)
	}
	journals := map[string]any{}
	for _, session := range sessions {
		entries, err := server.eventJournal.ReadAll(session.ID)
		if err != nil {
			t.Fatal(err)
		}
		journals[session.ID] = entries
	}
	runs, err := server.taskRunStore.List()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"frames": frames, "frameEvents": frameEvents, "realtimeOutbox": realtime,
		"runtimeMetadata": metadataByFrame, "branchArchives": branchArchives,
		"sessions": sessions, "journals": journals, "taskRuns": runs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func answerResultText(message map[string]any) string {
	blocks, _ := message["content"].([]any)
	for _, rawBlock := range blocks {
		block, _ := rawBlock.(map[string]any)
		if block["type"] == "tool_result" {
			return stringValue(block["content"])
		}
	}
	return ""
}
