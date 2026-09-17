package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceFrameBranchingHTTPAPIPreservesRunnableContext(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "foreign-project", UserID: "foreign", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root-frame", ProjectID: "project-1", AgentName: "research",
		Status: "running", ConversationType: "task", Name: "Root",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "foreign-frame", ProjectID: "foreign-project", AgentName: "research",
		Status: "running", ConversationType: "task", Name: "Foreign",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "root-frame", MessageUUID: "message-1",
		ClientMessageID: "client-1", Text: "Original request",
	}); err != nil {
		t.Fatal(err)
	}
	ask := map[string]any{
		"type": "ask_user", "toolUseId": "ask-1",
		"question": "Which release channel?", "role": "assistant",
	}
	if err := server.appendClientWebSocketMessage("root-frame", "assistant", ask); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "root-frame", Type: "ask_user",
		Payload: map[string]any{"toolUseId": "ask-1", "question": "Which release channel?", "role": "assistant"},
	}); err != nil {
		t.Fatal(err)
	}

	before := branchAuthorityStateHash(t, server, store, "root-frame", "local")
	beforeForeign := branchAuthorityStateHash(t, server, store, "foreign-frame", "foreign")
	malformed := p3JSONRequest(t, server, http.MethodPost, "/api/go/frames/root-frame/fork", map[string]any{
		"editedContent": "Missing message index",
	}, "local")
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed fork status=%d body=%s", malformed.Code, malformed.Body.String())
	}
	method := p3JSONRequest(t, server, http.MethodGet, "/api/go/frames/root-frame/fork", nil, "local")
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("fork method status=%d body=%s", method.Code, method.Body.String())
	}
	for _, target := range []string{"/api/go/frames/missing/fork", "/api/go/frames/foreign-frame/fork"} {
		response := p3JSONRequest(t, server, http.MethodPost, target, map[string]any{}, "local")
		if response.Code != http.StatusNotFound {
			t.Fatalf("POST %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	fork := p3JSONRequest(t, server, http.MethodPost, "/api/go/frames/root-frame/fork", map[string]any{
		"messageIndex": 0, "editedContent": "Revised request",
		"sourceBranchId": "root-frame", "targetAgent": "reviewer",
		"verifierMode": true, "memoryMode": "strict",
	}, "local")
	if fork.Code != http.StatusConflict || webString(p3DecodeObject(t, fork)["error"]) != branchAuthorityUnavailableTestDetail {
		t.Fatalf("fork status=%d body=%s", fork.Code, fork.Body.String())
	}

	malformedAnswer := p3JSONRequest(t, server, http.MethodPost, "/api/go/frames/root-frame/fork-at-answer", map[string]any{
		"response": map[string]any{"channel": "stable"},
	}, "local")
	if malformedAnswer.Code != http.StatusBadRequest {
		t.Fatalf("malformed answer status=%d body=%s", malformedAnswer.Code, malformedAnswer.Body.String())
	}
	answerFork := p3JSONRequest(t, server, http.MethodPost, "/api/go/frames/root-frame/fork-at-answer", map[string]any{
		"toolUseId": "ask-1", "response": map[string]any{"channel": "stable"},
		"sourceBranchId": "root-frame", "planMode": true,
	}, "local")
	if answerFork.Code != http.StatusConflict || webString(p3DecodeObject(t, answerFork)["error"]) != branchAuthorityUnavailableTestDetail {
		t.Fatalf("answer fork status=%d body=%s", answerFork.Code, answerFork.Body.String())
	}
	if after := branchAuthorityStateHash(t, server, store, "root-frame", "local"); after != before {
		t.Fatalf("rejected workspace branches mutated state: before=%s after=%s", before, after)
	}
	if after := branchAuthorityStateHash(t, server, store, "foreign-frame", "foreign"); after != beforeForeign {
		t.Fatalf("rejected workspace branches mutated foreign state: before=%s after=%s", beforeForeign, after)
	}

	authServer := New(Options{FileRoot: t.TempDir(), Workspace: store, SynonLinkAuth: SynonLinkAuthOptions{
		Username: "operator", Password: "test-secret-password", UserID: "local",
	}})
	authHandler := authServer.Handler()
	validFork := map[string]any{"messageIndex": 0, "editedContent": "Revised request"}
	if response := authenticatedWorkspaceBranchRequest(t, authHandler, "/api/go/frames/root-frame/fork", validFork, nil, nil, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated fork status=%d body=%s", response.Code, response.Body.String())
	}
	sessionCookie, csrfCookie := loginObservabilityCookies(t, authHandler)
	if response := authenticatedWorkspaceBranchRequest(t, authHandler, "/api/go/frames/root-frame/fork", validFork, sessionCookie, nil, ""); response.Code != http.StatusForbidden {
		t.Fatalf("missing-CSRF fork status=%d body=%s", response.Code, response.Body.String())
	}
	if response := authenticatedWorkspaceBranchRequest(t, authHandler, "/api/go/frames/foreign-frame/fork", validFork, sessionCookie, csrfCookie, csrfCookie.Value); response.Code != http.StatusNotFound {
		t.Fatalf("foreign fork status=%d body=%s", response.Code, response.Body.String())
	}
	if response := authenticatedWorkspaceBranchRequest(t, authHandler, "/api/go/frames/root-frame/fork", validFork, sessionCookie, csrfCookie, csrfCookie.Value); response.Code != http.StatusConflict {
		t.Fatalf("owned fork status=%d body=%s", response.Code, response.Body.String())
	}

	aside := postWorkspaceBranch(t, app, "/api/go/frames/root-frame/aside", map[string]any{
		"request": "Check the release notes independently.",
		"model":   "review-model", "intentId": "intent-1", "asSession": true,
	})
	asideFrame := decodeWorkspaceBranchFrame(t, aside)
	if asideFrame.ParentFrameID != "root-frame" || asideFrame.ConversationType != "aside_session" || webString(aside["sourceFrameId"]) != "root-frame" {
		t.Fatalf("aside frame = %#v", asideFrame)
	}
	asideEvents, err := store.ListFrameEvents(asideFrame.ID, 0, 20)
	if err != nil || len(asideEvents) != 4 ||
		asideEvents[2].Type != "aside_created" || asideEvents[2].Payload["model"] != "review-model" ||
		asideEvents[3].Type != "user_message" || asideEvents[3].Payload["text"] != "Check the release notes independently." {
		t.Fatalf("aside events = %#v, err=%v", asideEvents, err)
	}
	assertRunnableBranchSession(t, server, asideFrame.ID, 3, "Check the release notes independently.")
}

func authenticatedWorkspaceBranchRequest(t *testing.T, handler http.Handler, target string, body any, sessionCookie, csrfCookie *http.Cookie, csrfToken string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	for _, cookie := range []*http.Cookie{sessionCookie, csrfCookie} {
		if cookie != nil {
			request.AddCookie(cookie)
		}
	}
	if csrfToken != "" {
		request.Header.Set(webCSRFHeaderName, csrfToken)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func postWorkspaceBranch(t *testing.T, app http.Handler, target string, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", "local")
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("POST %s status = %d: %s", target, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func decodeWorkspaceBranchFrame(t *testing.T, response map[string]any) workspace.Frame {
	t.Helper()
	raw, err := json.Marshal(response["frame"])
	if err != nil {
		t.Fatal(err)
	}
	var frame workspace.Frame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.ID == "" {
		t.Fatalf("branch response = %#v", response)
	}
	return frame
}

func assertRunnableBranchSession(t *testing.T, server *Server, sessionID string, wantEvents int, wantLastText string) {
	t.Helper()
	session, found, err := server.sessionStore.Get(sessionID)
	if err != nil || !found || session.LastRole != "user" || session.MessageCount != wantEvents {
		t.Fatalf("branch session = %#v, found=%v, err=%v", session, found, err)
	}
	entries, err := server.eventJournal.ReadAfter(sessionID, 0, 20)
	if err != nil || len(entries) != wantEvents {
		t.Fatalf("branch journal = %#v, err=%v", entries, err)
	}
	if wantLastText != "" && entries[len(entries)-1].Message["text"] != wantLastText {
		t.Fatalf("branch last message = %#v", entries[len(entries)-1].Message)
	}
}
