package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
)

func TestWebShellRoutesValidateAndLaunch(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	filePath := filepath.Join(projectRoot, "report.txt")
	writeWebFSTestFile(t, filePath, "report")
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "local")

	type launch struct {
		action string
		target string
		tool   string
	}
	var launches []launch
	srv.webShellLauncher = func(_ context.Context, action, target, tool string) error {
		launches = append(launches, launch{action: action, target: target, tool: tool})
		return nil
	}
	handler := srv.Handler()
	tests := []struct {
		path string
		body map[string]any
		want launch
	}{
		{"/api/shell/open-file", map[string]any{"file_path": filePath},
			launch{"open-file", filePath, ""}},
		{"/api/shell/show-item-in-folder", map[string]any{"file_path": filePath},
			launch{"show-item-in-folder", filePath, ""}},
		{"/api/shell/open-folder-with", map[string]any{
			"folder_path": projectRoot, "tool": "terminal",
		}, launch{"open-folder-with", projectRoot, "terminal"}},
		{"/api/shell/open-external", map[string]any{"url": "https://example.test/path?q=1"},
			launch{"open-external", "https://example.test/path?q=1", ""}},
	}
	for _, test := range tests {
		response := webFSTestRequest(t, handler, http.MethodPost, test.path, "local", test.body)
		requireWebFSStatus(t, response, http.StatusOK)
	}
	if len(launches) != len(tests) {
		t.Fatalf("launches = %#v", launches)
	}
	for index, want := range tests {
		if launches[index] != want.want {
			t.Fatalf("launch %d = %#v want %#v", index, launches[index], want.want)
		}
	}

	tool := webFSTestRequest(t, handler, http.MethodPost,
		"/api/shell/check-tool-installed", "local", map[string]any{"tool": "sh"})
	requireWebFSStatus(t, tool, http.StatusOK)
	var installed bool
	decodeWebFSTestResponse(t, tool, &installed)
	if !installed {
		t.Fatal("sh should be discoverable in the test environment")
	}

	for _, rawURL := range []string{
		"javascript:alert(1)", "file:///etc/passwd", "https://user:secret@example.test/",
	} {
		response := webFSTestRequest(t, handler, http.MethodPost,
			"/api/shell/open-external", "local", map[string]any{"url": rawURL})
		requireWebFSStatus(t, response, http.StatusBadRequest)
	}
	invalidTool := webFSTestRequest(t, handler, http.MethodPost,
		"/api/shell/open-folder-with", "local", map[string]any{
			"folder_path": projectRoot, "tool": "sh -c",
		})
	requireWebFSStatus(t, invalidTool, http.StatusBadRequest)
}

func TestWebShellLauncherFailureIsVisible(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	filePath := filepath.Join(projectRoot, "report.txt")
	writeWebFSTestFile(t, filePath, "report")
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "local")
	srv.webShellLauncher = func(context.Context, string, string, string) error {
		return errors.New("desktop session is unavailable")
	}
	response := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/shell/open-file", "local", map[string]any{"file_path": filePath})
	requireWebFSStatus(t, response, http.StatusServiceUnavailable)
}
