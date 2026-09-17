package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWebOfficeCLIHelperProcess(t *testing.T) {
	if os.Getenv("SYNON_TEST_OFFICECLI_HELPER") != "1" {
		return
	}
	port := ""
	for index, argument := range os.Args {
		if argument == "--port" && index+1 < len(os.Args) {
			port = os.Args[index+1]
			break
		}
	}
	if _, err := strconv.Atoi(port); err != nil {
		panic("missing OfficeCLI helper port")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		panic(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("office-preview-ready"))
	})}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}

func configureWebOfficeHelper(t *testing.T) {
	t.Helper()
	prefix, err := json.Marshal([]string{
		"-test.run=^TestWebOfficeCLIHelperProcess$", "--",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_OFFICECLI_PATH", os.Args[0])
	t.Setenv("SYNON_OFFICECLI_ARGS_JSON", string(prefix))
	t.Setenv("SYNON_TEST_OFFICECLI_HELPER", "1")
}

func TestWebOfficePreviewStartsReusesStopsAndCloses(t *testing.T) {
	configureWebOfficeHelper(t)
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	filePath := filepath.Join(projectRoot, "report.docx")
	writeWebFSTestFile(t, filePath, "fixture")
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "local")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.stopAllWebOfficeWatches(ctx)
	})
	handler := srv.Handler()
	start := func() string {
		response := webFSTestRequest(t, handler, http.MethodPost,
			"/api/word-preview/start", "local", map[string]any{
				"file_path": filePath, "workspace": projectRoot,
			})
		requireWebFSStatus(t, response, http.StatusOK)
		var result struct {
			URL   string `json:"url"`
			Error string `json:"error"`
		}
		decodeWebFSTestResponse(t, response, &result)
		if result.URL == "" || result.Error != "" {
			t.Fatalf("start = %#v", result)
		}
		return result.URL
	}
	firstURL := start()
	if secondURL := start(); secondURL != firstURL {
		t.Fatalf("duplicate start URL = %q want %q", secondURL, firstURL)
	}

	key := webOfficeWatchKey("local", "word", filePath)
	srv.webOfficeMu.Lock()
	watch := srv.webOfficeWatches[key]
	count := len(srv.webOfficeWatches)
	srv.webOfficeMu.Unlock()
	if count != 1 || watch == nil {
		t.Fatalf("office watch count=%d watch=%#v", count, watch)
	}
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", watch.Port))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("helper status = %d", response.StatusCode)
	}

	stop := webFSTestRequest(t, handler, http.MethodPost,
		"/api/word-preview/stop", "local", map[string]any{"file_path": filePath})
	requireWebFSStatus(t, stop, http.StatusOK)
	srv.webOfficeMu.Lock()
	count = len(srv.webOfficeWatches)
	srv.webOfficeMu.Unlock()
	if count != 0 {
		t.Fatalf("office watch count after stop = %d", count)
	}

	_ = start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Fatal(err)
	}
	srv.webOfficeMu.Lock()
	count = len(srv.webOfficeWatches)
	srv.webOfficeMu.Unlock()
	if count != 0 {
		t.Fatalf("office watch count after close = %d", count)
	}
}

func TestWebOfficePreviewStartsOwnedArtifactVersionWithoutExposingBlobPath(t *testing.T) {
	configureWebOfficeHelper(t)
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "local")
	_, version, err := srv.workspaceStore.SaveArtifactVersionFromReader(context.Background(), workspace.SaveArtifactVersionReaderInput{
		ArtifactID: "office-artifact", ProjectID: "web-fs-project", Name: "report.docx",
		Kind: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Content: bytes.NewBufferString("fixture"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.stopAllWebOfficeWatches(ctx)
	})

	response := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/word-preview/start", "local", map[string]any{
			"artifact_id": "office-artifact", "version_id": version.ID,
		})
	requireWebFSStatus(t, response, http.StatusOK)
	var result struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	decodeWebFSTestResponse(t, response, &result)
	if result.URL == "" || result.Error != "" {
		t.Fatalf("artifact start = %#v", result)
	}
}

func TestWebOfficePreviewReportsMissingCLIAndInvalidFiles(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	docxPath := filepath.Join(projectRoot, "report.docx")
	textPath := filepath.Join(projectRoot, "report.txt")
	writeWebFSTestFile(t, docxPath, "fixture")
	writeWebFSTestFile(t, textPath, "fixture")
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "local")

	t.Setenv("SYNON_OFFICECLI_PATH", filepath.Join(stateRoot, "missing-officecli"))
	missing := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/word-preview/start", "local", map[string]any{"file_path": docxPath})
	requireWebFSStatus(t, missing, http.StatusOK)
	var result map[string]any
	decodeWebFSTestResponse(t, missing, &result)
	if result["error"] != "OFFICECLI_NOT_FOUND" || result["url"] != "" {
		t.Fatalf("missing OfficeCLI response = %#v", result)
	}

	invalid := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/word-preview/start", "local", map[string]any{"file_path": textPath})
	requireWebFSStatus(t, invalid, http.StatusBadRequest)
}
