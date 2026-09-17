package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverWebUIPrefersReleaseDirectory(t *testing.T) {
	root := t.TempDir()
	writeWebIndex(t, filepath.Join(root, "web"), "release-ui")
	writeWebIndex(t, filepath.Join(root, "frontend", "out", "renderer"), "source-ui")
	handler, resolved, err := discoverWebUI("", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if handler == nil || resolved != filepath.Join(root, "web") {
		t.Fatalf("handler=%v resolved=%q", handler, resolved)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "release-ui") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestDiscoverWebUIAllowsAPIOnlyAndRejectsBadExplicitRoot(t *testing.T) {
	handler, resolved, err := discoverWebUI("", []string{t.TempDir()})
	if err != nil || handler != nil || resolved != "" {
		t.Fatalf("handler=%v resolved=%q err=%v", handler, resolved, err)
	}
	if _, _, err := discoverWebUI(filepath.Join(t.TempDir(), "missing"), nil); err == nil || !strings.Contains(err.Error(), "configure workbench web UI") {
		t.Fatalf("explicit missing root error = %v", err)
	}
}

func TestDiscoverWebUIDisabledSentinelDisablesUI(t *testing.T) {
	root := t.TempDir()
	writeWebIndex(t, filepath.Join(root, "frontend", "out", "renderer"), "source-ui")
	for _, sentinel := range []string{"disabled", "off", " DISABLED "} {
		handler, resolved, err := discoverWebUI(sentinel, []string{root})
		if err != nil {
			t.Fatalf("sentinel %q: %v", sentinel, err)
		}
		if handler != nil || resolved != "" {
			t.Fatalf("sentinel %q: handler=%v resolved=%q", sentinel, handler, resolved)
		}
	}
}

func writeWebIndex(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
