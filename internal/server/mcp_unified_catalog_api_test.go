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

func TestMCPUnifiedCatalogHTTPFiltersSourcesAndPersistsEnabledPerOwner(t *testing.T) {
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", "mcp-ketcher")
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{ID: "custom:owner", UserID: "owner-a", Name: "Owner Custom", URL: "https://example.test/mcp", Transport: "streamable-http"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, FileRoot: t.TempDir()}).Handler()
	get := func(path string) []map[string]any {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, authenticatedWorkspaceURLRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s=%d %s", path, rec.Code, rec.Body.String())
		}
		var result []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	bundled := get("/api/mcp-servers/connectors?user_id=owner-a&source=bundled&limit=100")
	if len(bundled) != 33 {
		t.Fatalf("bundled count=%d", len(bundled))
	}
	for _, connector := range bundled {
		if connector["source"] != "bundled" {
			t.Fatalf("bundled=%#v", connector)
		}
		id, _ := connector["id"].(string)
		wantAuth := "not-required"
		if id == "bundled:omtx-om" || id == "bundled:patsnap-chemical-molecular" || id == "bundled:inductive-bio" ||
			id == "bundled:boltz-api-official" || id == "bundled:tamarind-bio" || id == "bundled:adaptyv-cloud-lab" {
			wantAuth = "unauthorized"
		}
		if connector["authState"] != wantAuth {
			t.Fatalf("bundled auth state=%#v", connector)
		}
	}
	put := func(path string, enabled bool) map[string]any {
		raw, _ := json.Marshal(map[string]any{"enabled": enabled})
		rec := httptest.NewRecorder()
		req := authenticatedWorkspaceURLRequest(http.MethodPut, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s=%d %s", path, rec.Code, rec.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	put("/api/mcp-servers/connectors/custom:owner/enabled?user_id=owner-a", false)
	custom := get("/api/mcp-servers/connectors?user_id=owner-a&source=custom&enabled=false")
	if len(custom) != 1 || custom[0]["id"] != "custom:owner" || custom[0]["connectionStatus"] != "disabled" {
		t.Fatalf("custom=%#v", custom)
	}
	if foreign := get("/api/mcp-servers/connectors?user_id=owner-b&source=custom"); len(foreign) != 0 {
		t.Fatalf("foreign custom=%#v", foreign)
	}
	put("/api/mcp-servers/connectors/bundled:pubmed/enabled?user_id=owner-a", false)
	disabled := get("/api/mcp-servers/connectors?user_id=owner-a&source=bundled&enabled=false")
	if len(disabled) != 1 || disabled[0]["id"] != "bundled:pubmed" {
		t.Fatalf("disabled bundled=%#v", disabled)
	}
	foreignBundled := get("/api/mcp-servers/connectors?user_id=owner-b&source=bundled&enabled=false")
	if len(foreignBundled) != 0 {
		t.Fatalf("foreign bundled preferences=%#v", foreignBundled)
	}
}
