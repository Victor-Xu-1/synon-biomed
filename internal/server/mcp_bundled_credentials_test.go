package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestBundledMCPAPIKeyUsesEncryptedOwnerScopedStorage(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	const connectorID = "bundled:tamarind-bio"
	const apiKey = "tamarind-owner-test-secret"
	if err := srv.saveBundledMCPAPIKey("owner-a", connectorID, apiKey); err != nil {
		t.Fatal(err)
	}
	value, found, err := srv.bundledMCPAPIKey("owner-a", connectorID)
	if err != nil || !found || value != apiKey {
		t.Fatalf("owner credential value=%q found=%t err=%v", value, found, err)
	}
	if value, found, err := srv.bundledMCPAPIKey("owner-b", connectorID); err != nil || found || value != "" {
		t.Fatalf("foreign credential value=%q found=%t err=%v", value, found, err)
	}
	vault, err := os.ReadFile(filepath.Join(root, "secrets", "vault.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(vault, []byte(apiKey)) {
		t.Fatal("bundled MCP API key is visible in the encrypted vault")
	}
	restarted := New(Options{FileRoot: root})
	if value, found, err := restarted.bundledMCPAPIKey("owner-a", connectorID); err != nil || !found || value != apiKey {
		t.Fatalf("restarted credential value=%q found=%t err=%v", value, found, err)
	}
	if err := restarted.deleteBundledMCPAPIKey("owner-a", connectorID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := restarted.bundledMCPAPIKey("owner-a", connectorID); err != nil || found {
		t.Fatalf("deleted credential found=%t err=%v", found, err)
	}
}

func TestBundledMCPAPIKeyRejectsEmptyMultilineAndOversizedValues(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	for _, value := range []string{"", "first\nsecond", strings.Repeat("x", maxBundledMCPAPIKeyBytes+1)} {
		if err := srv.saveBundledMCPAPIKey("owner", "bundled:tamarind-bio", value); err == nil {
			t.Fatalf("invalid API key length=%d was accepted", len(value))
		}
	}
}

func TestBundledMCPAPIKeyEndpointRejectsUnknownFieldsBeforeStorage(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := New(Options{FileRoot: root, Workspace: store})
	request := httptest.NewRequest(http.MethodPut, "/api/mcp-servers/connectors/bundled:tamarind-bio/credential",
		strings.NewReader(`{"apiKey":"must-not-be-stored","unexpected":true}`))
	response := httptest.NewRecorder()
	srv.handleBundledMCPAPIKey(response, request, "owner", "bundled:tamarind-bio")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field status=%d body=%s", response.Code, response.Body.String())
	}
	if _, found, err := srv.bundledMCPAPIKey("owner", "bundled:tamarind-bio"); err != nil || found {
		t.Fatalf("rejected credential found=%t err=%v", found, err)
	}
}

func TestBundledMCPDisconnectDeletesOnlyTheRequestingOwnersAPIKey(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := New(Options{FileRoot: root, Workspace: store})
	const connectorID = "bundled:tamarind-bio"
	if err := srv.saveBundledMCPAPIKey("owner-a", connectorID, "owner-a-key"); err != nil {
		t.Fatal(err)
	}
	if err := srv.saveBundledMCPAPIKey("owner-b", connectorID, "owner-b-key"); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	request := authenticatedWorkspaceRequest(
		http.MethodPost,
		"/api/mcp-servers/connectors/bundled:tamarind-bio/disconnect",
		"owner-a",
		nil,
	)
	srv.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("disconnect status=%d body=%s", response.Code, response.Body.String())
	}
	if _, found, err := srv.bundledMCPAPIKey("owner-a", connectorID); err != nil || found {
		t.Fatalf("disconnected owner credential found=%t err=%v", found, err)
	}
	if value, found, err := srv.bundledMCPAPIKey("owner-b", connectorID); err != nil || !found || value != "owner-b-key" {
		t.Fatalf("other owner credential value=%q found=%t err=%v", value, found, err)
	}
}
