package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWebFSProjectOperationsUseRealFilesystem(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	if err := os.MkdirAll(filepath.Join(projectRoot, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "nested", "child.md"), []byte("# child"), 0o600); err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(projectRoot, "pixel.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := workspace.Open(filepath.Join(stateRoot, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "owned", UserID: "local", Name: "Owned", Path: projectRoot,
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: filepath.Join(stateRoot, "runtime"), Workspace: store})
	handler := srv.Handler()

	dirResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/dir", "local", map[string]any{
		"dir": projectRoot, "root": projectRoot,
	})
	requireWebFSStatus(t, dirResponse, http.StatusOK)
	var nodes []webFSNode
	decodeWebFSTestResponse(t, dirResponse, &nodes)
	if len(nodes) != 3 || nodes[0].Name != "nested" || len(nodes[0].Children) != 1 ||
		nodes[0].Children[0].RelativePath != "nested/child.md" {
		t.Fatalf("directory tree = %#v", nodes)
	}

	listResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/list", "local", map[string]any{
		"root": projectRoot,
	})
	requireWebFSStatus(t, listResponse, http.StatusOK)
	var files []webFSFlatFile
	decodeWebFSTestResponse(t, listResponse, &files)
	if len(files) != 3 || files[0].RelativePath != "nested/child.md" ||
		files[1].RelativePath != "note.txt" || files[2].RelativePath != "pixel.png" {
		t.Fatalf("flat files = %#v", files)
	}

	readResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read", "local", map[string]any{
		"path": "note.txt", "workspace": projectRoot,
	})
	requireWebFSStatus(t, readResponse, http.StatusOK)
	var text string
	decodeWebFSTestResponse(t, readResponse, &text)
	if text != "hello" {
		t.Fatalf("read text = %q", text)
	}

	bufferResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read-buffer", "local", map[string]any{
		"path": filepath.Join(projectRoot, "note.txt"), "workspace": projectRoot,
	})
	requireWebFSStatus(t, bufferResponse, http.StatusOK)
	var encoded string
	decodeWebFSTestResponse(t, bufferResponse, &encoded)
	if decoded, err := base64.StdEncoding.DecodeString(encoded); err != nil || string(decoded) != "hello" {
		t.Fatalf("buffer = %q err=%v", encoded, err)
	}

	imageResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/image-base64", "local", map[string]any{
		"path": filepath.Join(projectRoot, "pixel.png"), "workspace": projectRoot,
	})
	requireWebFSStatus(t, imageResponse, http.StatusOK)
	var dataURL string
	decodeWebFSTestResponse(t, imageResponse, &dataURL)
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("image URL = %q", dataURL)
	}

	metadataResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/metadata", "local", map[string]any{
		"path": filepath.Join(projectRoot, "note.txt"), "workspace": projectRoot,
	})
	requireWebFSStatus(t, metadataResponse, http.StatusOK)
	var metadata map[string]any
	decodeWebFSTestResponse(t, metadataResponse, &metadata)
	if metadata["name"] != "note.txt" || metadata["isDirectory"] != false ||
		metadata["size"].(float64) != 5 || metadata["lastModified"].(float64) <= 0 {
		t.Fatalf("metadata = %#v", metadata)
	}

	writeResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/write", "local", map[string]any{
		"path": "draft.txt", "workspace": projectRoot, "data": "draft",
	})
	requireWebFSStatus(t, writeResponse, http.StatusOK)
	if raw, err := os.ReadFile(filepath.Join(projectRoot, "draft.txt")); err != nil || string(raw) != "draft" {
		t.Fatalf("written=%q err=%v", raw, err)
	}

	renameResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/rename", "local", map[string]any{
		"path": "draft.txt", "workspace": projectRoot, "new_name": "final.txt",
	})
	requireWebFSStatus(t, renameResponse, http.StatusOK)
	var renamed map[string]string
	decodeWebFSTestResponse(t, renameResponse, &renamed)
	if renamed["new_path"] != filepath.Join(projectRoot, "final.txt") {
		t.Fatalf("rename response = %#v", renamed)
	}

	removeResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/remove", "local", map[string]any{
		"path": "final.txt", "workspace": projectRoot,
	})
	requireWebFSStatus(t, removeResponse, http.StatusOK)
	if _, err := os.Stat(filepath.Join(projectRoot, "final.txt")); !os.IsNotExist(err) {
		t.Fatalf("removed file stat err = %v", err)
	}

	tempResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/temp", "local", map[string]any{
		"file_name": "upload.png",
	})
	requireWebFSStatus(t, tempResponse, http.StatusOK)
	var tempPath string
	decodeWebFSTestResponse(t, tempResponse, &tempPath)
	tempRoot, err := srv.webFSTempRoot("local")
	if err != nil || !webFSPathWithin(tempRoot, tempPath) {
		t.Fatalf("temp path=%q root=%q err=%v", tempPath, tempRoot, err)
	}
	if info, err := os.Stat(tempPath); err != nil || !info.Mode().IsRegular() || filepath.Ext(tempPath) != ".png" {
		t.Fatalf("temp info=%v err=%v", info, err)
	}

	largePath := filepath.Join(projectRoot, "large.bin")
	large, err := os.OpenFile(largePath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(maxWebFSReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	tooLarge := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read-buffer", "local", map[string]any{
		"path": largePath, "workspace": projectRoot,
	})
	requireWebFSStatus(t, tooLarge, http.StatusRequestEntityTooLarge)

	unknown := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read", "local", map[string]any{
		"path": "note.txt", "workspace": projectRoot, "unexpected": true,
	})
	requireWebFSStatus(t, unknown, http.StatusBadRequest)

	wrongMethod := webFSTestRequest(t, handler, http.MethodGet, "/api/fs/read", "local", nil)
	requireWebFSStatus(t, wrongMethod, http.StatusMethodNotAllowed)
}

func TestWebFSAuthorizationGrantsTraversalAndCopy(t *testing.T) {
	stateRoot := t.TempDir()
	ownedRoot := filepath.Join(stateRoot, "owned")
	foreignRoot := filepath.Join(stateRoot, "foreign")
	readRoot := filepath.Join(stateRoot, "read-grant")
	writeRoot := filepath.Join(stateRoot, "write-grant")
	for _, path := range []string{ownedRoot, foreignRoot, readRoot, writeRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(foreignRoot, "secret.txt"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readRoot, "source.txt"), []byte("copy me"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(filepath.Join(stateRoot, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, project := range []workspace.CreateProjectInput{
		{ID: "owned", UserID: "owner", Name: "Owned", Path: ownedRoot},
		{ID: "foreign", UserID: "other", Name: "Foreign", Path: foreignRoot},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(Options{
		FileRoot: filepath.Join(stateRoot, "runtime"), Workspace: store,
		SynonLinkAuth: SynonLinkAuthOptions{Username: "operator", Password: "strong-password", UserID: "owner"},
	})
	handler := http.HandlerFunc(srv.handleWebFS)
	if _, err := srv.upsertHostGrant("owner", readRoot, "read"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.upsertHostGrant("owner", writeRoot, "read_write"); err != nil {
		t.Fatal(err)
	}

	localRead := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read", "owner", map[string]any{
		"path": "missing.txt", "workspace": ownedRoot,
	})
	requireWebFSStatus(t, localRead, http.StatusOK)
	if strings.TrimSpace(localRead.Body.String()) != "null" {
		t.Fatalf("missing response = %s", localRead.Body.String())
	}

	foreignRead := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read", "owner", map[string]any{
		"path": filepath.Join(foreignRoot, "secret.txt"), "workspace": foreignRoot,
	})
	requireWebFSStatus(t, foreignRead, http.StatusForbidden)
	foreignAbsolute := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read", "owner", map[string]any{
		"path": filepath.Join(foreignRoot, "secret.txt"),
	})
	requireWebFSStatus(t, foreignAbsolute, http.StatusForbidden)

	traversal := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read", "owner", map[string]any{
		"path": filepath.Join("..", "foreign", "secret.txt"), "workspace": ownedRoot,
	})
	requireWebFSStatus(t, traversal, http.StatusForbidden)

	if err := os.Symlink(filepath.Join(foreignRoot, "secret.txt"), filepath.Join(ownedRoot, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	symlink := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/read", "owner", map[string]any{
		"path": "escape.txt", "workspace": ownedRoot,
	})
	requireWebFSStatus(t, symlink, http.StatusUnsupportedMediaType)

	readOnlyWrite := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/write", "owner", map[string]any{
		"path": filepath.Join(readRoot, "blocked.txt"), "workspace": readRoot, "data": "blocked",
	})
	requireWebFSStatus(t, readOnlyWrite, http.StatusForbidden)
	writeGranted := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/write", "owner", map[string]any{
		"path": filepath.Join(writeRoot, "allowed.txt"), "workspace": writeRoot, "data": "allowed",
	})
	requireWebFSStatus(t, writeGranted, http.StatusOK)

	copyResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/copy", "owner", map[string]any{
		"file_paths": []string{filepath.Join(readRoot, "source.txt")}, "workspace": ownedRoot,
	})
	requireWebFSStatus(t, copyResponse, http.StatusOK)
	var copied struct {
		Copied []string `json:"copied_files"`
		Failed []any    `json:"failed_files"`
	}
	decodeWebFSTestResponse(t, copyResponse, &copied)
	if len(copied.Copied) != 1 || len(copied.Failed) != 0 {
		t.Fatalf("copy response = %#v", copied)
	}
	if raw, err := os.ReadFile(filepath.Join(ownedRoot, "source.txt")); err != nil || string(raw) != "copy me" {
		t.Fatalf("copied=%q err=%v", raw, err)
	}

	if err := os.Symlink(filepath.Join(foreignRoot, "secret.txt"), filepath.Join(readRoot, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	linkCopy := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/copy", "owner", map[string]any{
		"file_paths": []string{filepath.Join(readRoot, "linked.txt")}, "workspace": ownedRoot,
	})
	requireWebFSStatus(t, linkCopy, http.StatusOK)
	var partial map[string]any
	decodeWebFSTestResponse(t, linkCopy, &partial)
	if len(partial["failed_files"].([]any)) != 1 {
		t.Fatalf("symlink copy response = %#v", partial)
	}
}

func TestWebFSRemoteImageEnforcesOutboundAndContentPolicy(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0}
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/html" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>not an image</html>"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer fixture.Close()

	srv := New(Options{FileRoot: t.TempDir()})
	handler := http.HandlerFunc(srv.handleWebFS)
	blocked := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/fetch-remote-image", "local", map[string]any{
		"url": "https://127.0.0.1/private.png",
	})
	requireWebFSStatus(t, blocked, http.StatusForbidden)
	credentials := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/fetch-remote-image", "local", map[string]any{
		"url": "https://user:pass@example.test/image.png",
	})
	requireWebFSStatus(t, credentials, http.StatusBadRequest)

	srv.webImageClientForURL = func(context.Context, string, *http.Client) (*http.Client, error) {
		return fixture.Client(), nil
	}
	image := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/fetch-remote-image", "local", map[string]any{
		"url": fixture.URL + "/image.png",
	})
	requireWebFSStatus(t, image, http.StatusOK)
	var dataURL string
	decodeWebFSTestResponse(t, image, &dataURL)
	if !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("remote image = %q", dataURL)
	}

	html := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/fetch-remote-image", "local", map[string]any{
		"url": fixture.URL + "/html",
	})
	requireWebFSStatus(t, html, http.StatusUnsupportedMediaType)
}

func webFSTestRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	userID string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	request := httptest.NewRequest(method, path, reader)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", userID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requireWebFSStatus(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
}

func decodeWebFSTestResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}
