package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAnnotationHTTPAPIPersistsEditsAndCarriesToNewArtifactVersion(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		content := "revised passage"
		if bytes.Contains(body, []byte("RAW SOURCE")) {
			content = "[x]"
		} else if !bytes.Contains(body, []byte("Requested change")) {
			t.Errorf("model request did not contain edit instruction: %s", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": content}}},
		})
	}))
	defer model.Close()

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, firstVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.txt",
		Kind: "text/plain", Content: []byte("title\nhello   world\nend\n"), CreatedBy: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{
		Workspace: store, FileRoot: root, HTTPClient: model.Client(),
		CompactSummarizer: SessionRunnerChatOptions{Endpoint: model.URL, Model: "fixture-model", MaxAttempts: 1, OutputLimitBytes: 1 << 20},
	}).Handler()

	annotationPath := "/api/artifacts/artifact-1/versions/" + firstVersion.ID + "/annotations"
	created := annotationJSONRequest(t, app, http.MethodPost, annotationPath, map[string]any{
		"type": "text_selection", "text": "make concise", "selection_text": "hello world",
	}, http.StatusCreated)
	annotationID, _ := created["id"].(string)
	if annotationID == "" || created["label"] != "①" || created["target_key"] != "av:"+firstVersion.ID {
		t.Fatalf("created annotation = %#v", created)
	}
	foreign := httptest.NewRecorder()
	foreignRequest := httptest.NewRequest(http.MethodGet, annotationPath, nil)
	foreignRequest.Header.Set("X-Synon-User-Id", "user-2")
	app.ServeHTTP(foreign, foreignRequest)
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross-user annotation read = %d: %s", foreign.Code, foreign.Body.String())
	}
	foreignMutation := httptest.NewRecorder()
	foreignBody := bytes.NewBufferString(`{"text":"unauthorized"}`)
	foreignMutationRequest := httptest.NewRequest(http.MethodPatch, "/api/annotations/"+annotationID, foreignBody)
	foreignMutationRequest.Header.Set("X-Synon-User-Id", "user-2")
	foreignMutationRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(foreignMutation, foreignMutationRequest)
	if foreignMutation.Code != http.StatusNotFound {
		t.Fatalf("cross-user annotation mutation = %d: %s", foreignMutation.Code, foreignMutation.Body.String())
	}
	updated := annotationJSONRequest(t, app, http.MethodPatch, "/api/annotations/"+annotationID, map[string]any{
		"text": "make much shorter",
	}, http.StatusOK)
	if updated["text"] != "make much shorter" {
		t.Fatalf("updated annotation = %#v", updated)
	}

	suggestion := annotationJSONRequest(t, app, http.MethodPost,
		"/api/artifacts/artifact-1/versions/"+firstVersion.ID+"/suggest-edits", map[string]any{
			"selected_text": "hello world", "annotation_text": "make concise", "mode": "edit",
		}, http.StatusOK)
	if suggestion["suggestion"] != "revised passage" {
		t.Fatalf("suggestion = %#v", suggestion)
	}

	applied := annotationJSONRequest(t, app, http.MethodPost,
		"/api/artifacts/artifact-1/versions/"+firstVersion.ID+"/apply-edit", map[string]any{
			"selected_text": "hello world", "replacement_text": "hi",
		}, http.StatusCreated)
	newVersionID, _ := applied["version_id"].(string)
	if newVersionID == "" || applied["parent_version_id"] != firstVersion.ID {
		t.Fatalf("applied edit = %#v", applied)
	}
	carried, _ := applied["carried_annotations"].([]any)
	if len(carried) != 1 {
		t.Fatalf("carried annotations = %#v", applied["carried_annotations"])
	}
	_, newVersion, found, err := store.GetArtifactVersion(newVersionID)
	if err != nil || !found || string(newVersion.Content) != "title\nhi\nend\n" {
		t.Fatalf("new version = %#v found=%v err=%v", newVersion, found, err)
	}
	listed := annotationJSONRequest(t, app, http.MethodGet,
		"/api/artifacts/artifact-1/versions/"+newVersionID+"/annotations", nil, http.StatusOK)
	if annotations, _ := listed["annotations"].([]any); len(annotations) != 1 {
		t.Fatalf("new version annotations = %#v", listed)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/annotations/"+annotationID, nil)
	request.Header.Set("X-Synon-User-Id", "user-1")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", response.Code, response.Body.String())
	}

	_, repeatedVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-2", ProjectID: "project-1", Name: "repeated.txt",
		Kind: "text/plain", Content: []byte("first: [x]\nsecond: [x]\n"), CreatedBy: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	reanchored := annotationJSONRequest(t, app, http.MethodPost,
		"/api/artifacts/artifact-2/versions/"+repeatedVersion.ID+"/apply-edit", map[string]any{
			"selected_text": "rendered x", "replacement_text": "chosen",
			"context_before": "second:", "context_after": "",
		}, http.StatusCreated)
	reanchoredID, _ := reanchored["version_id"].(string)
	_, reanchoredVersion, found, err := store.GetArtifactVersion(reanchoredID)
	if err != nil || !found || string(reanchoredVersion.Content) != "first: [x]\nsecond: chosen\n" {
		t.Fatalf("model-reanchored version = %#v found=%v err=%v", reanchoredVersion, found, err)
	}
}

func TestAnnotationSuggestEditsUsesActiveWorkspaceProvider(t *testing.T) {
	staticCalled := false
	staticModel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staticCalled = true
		http.Error(w, "stale static annotation model must not be called", http.StatusTeapot)
	}))
	defer staticModel.Close()
	modelCalled := false
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalled = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("workspace provider request = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer workspace-annotation-key" {
			t.Errorf("workspace provider Authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode workspace provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "workspace-annotation-model" {
			t.Errorf("workspace provider model = %q", request.Model)
		}
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" ||
			!strings.Contains(request.Messages[1].Content, "Requested change") {
			t.Errorf("workspace provider messages = %#v", request.Messages)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "workspace-annotation-response",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "workspace provider revision"}}},
		})
	}))
	defer model.Close()

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "workspace-annotation-project", UserID: "user-1", Name: "Workspace annotation",
	}); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "workspace-annotation-artifact", ProjectID: "workspace-annotation-project",
		Name: "report.md", Kind: "text/markdown", Content: []byte("A verbose scientific statement."), CreatedBy: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{
		Workspace: store, FileRoot: root, HTTPClient: model.Client(),
		CompactSummarizer: SessionRunnerChatOptions{
			Endpoint: staticModel.URL + "/v1/chat/completions", Model: "stale-static-model", MaxAttempts: 1,
		},
	})
	if _, err := srv.settingsStore.Set(webConversationActiveProviderSetting, "workspace-annotation-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{
		ID: "workspace-annotation-secret", UserID: "user-1", Provider: "openai", Value: "workspace-annotation-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "workspace-annotation-provider", UserID: "user-1", Name: "Workspace annotation provider",
		Type: "openai", BaseURL: model.URL + "/v1", Model: "workspace-annotation-model",
		SecretRef: "secret://workspace-annotation-secret", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	suggestion := annotationJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/artifacts/workspace-annotation-artifact/versions/"+version.ID+"/suggest-edits", map[string]any{
			"selected_text": "A verbose scientific statement.", "annotation_text": "Make concise.", "mode": "edit",
		}, http.StatusOK)
	if staticCalled || !modelCalled || suggestion["suggestion"] != "workspace provider revision" {
		t.Fatalf("static called=%v workspace provider called=%v suggestion=%#v", staticCalled, modelCalled, suggestion)
	}
}

func TestLocalFileAnnotationsUseGrantedPathAndCurrentChecksum(t *testing.T) {
	root := t.TempDir()
	granted := filepath.Join(t.TempDir(), "granted")
	if err := os.MkdirAll(granted, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(granted, "notes.txt")
	content := []byte("source text")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, FileRoot: root}).Handler()
	annotationJSONRequest(t, app, http.MethodPost, "/api/go/preferences/host-grants", map[string]any{
		"path": granted, "mode": "read",
	}, http.StatusOK)
	created := annotationJSONRequest(t, app, http.MethodPost, "/api/projects/project-1/annotations", map[string]any{
		"target": map[string]any{"kind": "local", "absPath": path},
		"type":   "point", "text": "check", "x_percent": 12, "y_percent": 34,
	}, http.StatusCreated)
	digest := sha256.Sum256(content)
	wantChecksum := hex.EncodeToString(digest[:])
	if created["content_checksum"] != wantChecksum {
		t.Fatalf("created local annotation = %#v", created)
	}
	listed := annotationJSONRequest(t, app, http.MethodPost, "/api/projects/project-1/annotations/list", map[string]any{
		"target": map[string]any{"kind": "local", "absPath": path},
	}, http.StatusOK)
	if listed["current_checksum"] != wantChecksum || !strings.HasPrefix(listed["target_key"].(string), "file:local:") {
		t.Fatalf("local annotation list = %#v", listed)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(granted, "escape.txt")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	escaped := annotationJSONRequest(t, app, http.MethodPost, "/api/projects/project-1/annotations/list", map[string]any{
		"target": map[string]any{"kind": "local", "absPath": symlink},
	}, http.StatusOK)
	if escaped["current_checksum"] != nil {
		t.Fatalf("symlink outside grant was hashed: %#v", escaped)
	}
	remote := annotationJSONRequest(t, app, http.MethodPost, "/api/projects/project-1/annotations", map[string]any{
		"target": map[string]any{"kind": "remote", "provider": "s3:prod%west", "path": "folder/report.txt"},
		"type":   "point", "text": "remote note", "x_percent": 1, "y_percent": 2,
	}, http.StatusCreated)
	if remote["target_key"] != "file:s3%3Aprod%25west:folder/report.txt" {
		t.Fatalf("remote target key = %#v", remote)
	}
}

func TestAnnotationSelectionUsesContextForRepeatedText(t *testing.T) {
	document := "first: repeat\nsecond: repeat\n"
	start, end, found := locateAnnotationSelection(document, "repeat", "second:", "")
	if !found || document[start:end] != "repeat" || start != strings.LastIndex(document, "repeat") {
		t.Fatalf("selection = %d:%d found=%v", start, end, found)
	}
	formatted := "first: hello   world\nsecond: hello\tworld\n"
	start, end, found = locateAnnotationSelection(formatted, "hello world", "second:", "")
	if !found || formatted[start:end] != "hello\tworld" {
		t.Fatalf("formatted selection = %q found=%v", formatted[start:end], found)
	}
}

func annotationJSONRequest(t *testing.T, app http.Handler, method, path string, body any, status int) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Synon-User-Id", "user-1")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != status {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, response.Code, status, response.Body.String())
	}
	if response.Body.Len() == 0 {
		return map[string]any{}
	}
	var output map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
		t.Fatalf("decode %s %s: %v: %s", method, path, err, response.Body.String())
	}
	return output
}
