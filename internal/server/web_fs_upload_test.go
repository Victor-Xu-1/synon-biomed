package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWebFSUploadFeedsAuthenticatedConversationInputAuthority(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "project-web-upload", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-web-upload", ProjectID: project.ID, AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Web upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	compatFrame, found, err := store.GetCompatibilityFrame(frame.ID)
	if err != nil || !found {
		t.Fatalf("compatibility frame found=%v err=%v", found, err)
	}

	payload := []byte("compound,value\naspirin,1\n")
	body, contentType := multipartAttachmentBody(t, "file", "组合验收.csv", payload, map[string]string{
		"conversation_id": frame.ID,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/fs/upload", body)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Synon-User-Id", "local")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Success bool   `json:"success"`
		Data    string `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || !result.Success || result.Data == "" {
		t.Fatalf("upload response=%s err=%v", response.Body.String(), err)
	}
	root, err := app.webFSTempRoot("local")
	if err != nil || !webFSPathWithin(root, result.Data) {
		t.Fatalf("uploaded path=%q root=%q err=%v", result.Data, root, err)
	}
	if filepath.Base(result.Data) != "组合验收.csv" {
		t.Fatalf("uploaded filename=%q", filepath.Base(result.Data))
	}
	if uploaded, err := os.ReadFile(result.Data); err != nil || string(uploaded) != string(payload) {
		t.Fatalf("uploaded bytes=%q err=%v", uploaded, err)
	}
	app.synonLinkAuth.enabled = true
	materialized, err := app.materializeWebConversationInputFiles(context.Background(), compatFrame, []string{result.Data})
	if err != nil || len(materialized) != 1 || materialized[0] != "inputs/组合验收.csv" {
		t.Fatalf("authenticated materialization=%#v err=%v", materialized, err)
	}
	app.synonLinkAuth.enabled = false

	sent := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+frame.ID+"/messages", map[string]any{
		"content": "Read the uploaded CSV.", "files": []string{result.Data},
	}, "local")
	if sent.Code != http.StatusAccepted {
		t.Fatalf("send status=%d body=%s", sent.Code, sent.Body.String())
	}
	session, found, err := app.sessionStore.Get(frame.ID)
	if err != nil || !found {
		t.Fatalf("session found=%v err=%v", found, err)
	}
	input, _ := session.Orchestration["inputData"].(map[string]any)
	files, _ := input["files"].([]any)
	if len(files) != 1 || files[0] != "inputs/组合验收.csv" {
		t.Fatalf("materialized files=%#v input=%#v", files, input)
	}
}

func TestWebFSUploadRejectsForeignConversationAndUnsafeFilename(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "project-upload-owner", "owner")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-upload-owner", ProjectID: project.ID, AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Owned upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	requestUpload := func(user string, fields map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		body, contentType := multipartAttachmentBody(t, "file", "evidence.txt", []byte("evidence"), fields)
		request := httptest.NewRequest(http.MethodPost, "/api/fs/upload", body)
		request.RemoteAddr = "127.0.0.1:12345"
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("X-Synon-User-Id", user)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		return response
	}

	foreign := requestUpload("other", map[string]string{"conversation_id": frame.ID})
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign upload status=%d body=%s", foreign.Code, foreign.Body.String())
	}
	unsafe := requestUpload("owner", map[string]string{"file_name": "../escape.txt"})
	if unsafe.Code != http.StatusBadRequest || !strings.Contains(unsafe.Body.String(), "plain UTF-8 file name") {
		t.Fatalf("unsafe upload status=%d body=%s", unsafe.Code, unsafe.Body.String())
	}
}

func TestWebConversationInputRejectsUnscopedAbsoluteFileWithWebAuthentication(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	app.synonLinkAuth.enabled = true
	project := createP3Project(t, store, "project-scoped-input", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-scoped-input", ProjectID: project.ID, AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Scoped input",
	})
	if err != nil {
		t.Fatal(err)
	}
	compatFrame, found, err := store.GetCompatibilityFrame(frame.ID)
	if err != nil || !found {
		t.Fatalf("compatibility frame found=%v err=%v", found, err)
	}
	source := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(source, []byte("must not be imported"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = app.materializeWebConversationInputFiles(context.Background(), compatFrame, []string{source})
	if err == nil || !strings.Contains(err.Error(), "could not be imported") {
		t.Fatalf("unscoped input err=%v", err)
	}
}
