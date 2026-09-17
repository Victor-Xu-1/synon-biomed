package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestSynonBiomedCatalogUsesLiveOwnerScopedRuntimeData(t *testing.T) {
	t.Setenv("SYNON_BUNDLED_CONNECTOR_EXECUTABLE", "mcp-ketcher")
	root := t.TempDir()
	assetsRoot := filepath.Join(root, "runtime-assets")
	for _, directory := range []string{
		filepath.Join(assetsRoot, "compute"),
		filepath.Join(assetsRoot, "micromamba"),
		filepath.Join(assetsRoot, "mcp-servers"),
		filepath.Join(assetsRoot, "seed"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	seedManifest := `{
	  "root_frame": {"id": "root-stat6"},
	  "artifacts": [{"id": "a1"}, {"id": "a2"}],
	  "child_frames": {"one": {}, "two": {}},
	  "folders": [{"id": "f1"}]
	}`
	if err := os.WriteFile(filepath.Join(assetsRoot, "seed", "manifest_crispr_screen.json"), []byte(seedManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, input := range []workspace.CreateCompatibilityProjectInput{
		{ID: "project-local", UserID: "local", Name: "Local project", Description: "Owner visible"},
		{ID: "project-foreign", UserID: "foreign", Name: "Foreign project", Description: "Must not leak"},
	} {
		if _, _, err := store.CreateCompatibilityProject(input); err != nil {
			t.Fatal(err)
		}
	}
	app := New(Options{
		FileRoot: root, RuntimeAssetsDir: assetsRoot, Workspace: store,
		SkillDirectories: []string{v11SkillsDir(t)},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/synonbiomed/catalog", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "localhost:8765"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Product string `json:"product"`
		Runtime struct {
			RuntimeAssetsDir string                        `json:"runtimeAssetsDir"`
			Agents           synonBiomedCountedNames       `json:"agents"`
			Skills           synonBiomedCountedNames       `json:"skills"`
			MCPServers       synonBiomedCountedNames       `json:"mcpServers"`
			ThirdPartyAssets synonBiomedCountedNames       `json:"thirdPartyAssets"`
			SeedProjects     synonBiomedSeedProjectCatalog `json:"seedProjects"`
		} `json:"runtime"`
		Backend struct {
			BaseURL  string                           `json:"baseUrl"`
			Projects synonBiomedBackendProjectCatalog `json:"projects"`
		} `json:"backend"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Product != "Synon Biomed" || payload.Backend.BaseURL != "http://localhost:8765" {
		t.Fatalf("product/base URL = %q %q", payload.Product, payload.Backend.BaseURL)
	}
	assertSameTestPath(t, payload.Runtime.RuntimeAssetsDir, assetsRoot)
	if payload.Runtime.Agents.Count == 0 {
		t.Fatal("expected bundled agents in catalog")
	}
	if payload.Runtime.Skills.Count == 0 || !containsCatalogString(payload.Runtime.Skills.Names, "synon-runtime") {
		t.Fatalf("skills=%v", payload.Runtime.Skills.Names)
	}
	if payload.Runtime.MCPServers.Count == 0 || !containsCatalogString(payload.Runtime.MCPServers.Names, "ketcher-chemistry") {
		t.Fatalf("MCP servers=%v", payload.Runtime.MCPServers.Names)
	}
	if !containsCatalogString(payload.Runtime.ThirdPartyAssets.Names, "compute") ||
		!containsCatalogString(payload.Runtime.ThirdPartyAssets.Names, "micromamba") {
		t.Fatalf("third-party assets=%v", payload.Runtime.ThirdPartyAssets.Names)
	}
	if payload.Runtime.SeedProjects.Count != 1 {
		t.Fatalf("seed projects=%#v", payload.Runtime.SeedProjects)
	}
	seed := payload.Runtime.SeedProjects.Projects[0]
	if seed.Slug != "crispr_screen" || seed.Name != "CRISPR Screen" || seed.RootFrameID != "root-stat6" ||
		seed.ArtifactCount != 2 || seed.ChildFrameCount != 2 || seed.FolderCount != 1 {
		t.Fatalf("seed=%#v", seed)
	}
	if payload.Backend.Projects.Count != 1 || len(payload.Backend.Projects.Projects) != 1 ||
		payload.Backend.Projects.Projects[0].ProjectID != "project-local" ||
		strings.Contains(response.Body.String(), "project-foreign") {
		t.Fatalf("owner project catalog=%#v", payload.Backend.Projects)
	}
}

func TestSynonBiomedProjectCreateReturnsCamelCaseAndPersistsContext(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{FileRoot: root, Workspace: store})
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/synonbiomed/projects",
		strings.NewReader(`{"name":"Oncology Review","description":"STAT6 program","context":"CRBN evidence"}`),
	)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Project synonBiomedBackendProject `json:"project"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Project.ProjectID == "" || payload.Project.Name != "Oncology Review" ||
		payload.Project.Description == nil || *payload.Project.Description != "STAT6 program" ||
		payload.Project.CreatedAt == nil || payload.Project.UpdatedAt == nil {
		t.Fatalf("project=%#v", payload.Project)
	}
	persisted, found, err := store.GetCompatibilityProject("local", payload.Project.ProjectID)
	if err != nil || !found || persisted.ContextData != "CRBN evidence" {
		t.Fatalf("persisted=%#v found=%t err=%v", persisted, found, err)
	}

	list := httptest.NewRequest(http.MethodGet, "/api/synonbiomed/projects", nil)
	list.RemoteAddr = "127.0.0.1:12345"
	listResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), payload.Project.ProjectID) {
		t.Fatalf("list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
}

func TestSynonBiomedProjectAndCatalogRejectInvalidOrUnauthenticatedRequests(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{FileRoot: root, Workspace: store})

	invalid := httptest.NewRequest(http.MethodPost, "/api/synonbiomed/projects", strings.NewReader(`{"name":"","extra":true}`))
	invalid.RemoteAddr = "127.0.0.1:12345"
	invalidResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}

	remote := httptest.NewRequest(http.MethodGet, "/api/synonbiomed/catalog", nil)
	remote.RemoteAddr = "203.0.113.8:12345"
	remoteResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusUnauthorized {
		t.Fatalf("remote status=%d body=%s", remoteResponse.Code, remoteResponse.Body.String())
	}
}

func TestSystemInfoMapsConfiguredDirectoriesAndRuntime(t *testing.T) {
	root := t.TempDir()
	cacheDir := filepath.Join(root, "cache-override")
	workDir := filepath.Join(root, "work-override")
	logDir := filepath.Join(root, "log-override")
	t.Setenv("SYNON_AI_CACHE_DIR", cacheDir)
	t.Setenv("SYNON_AI_WORK_DIR", workDir)
	t.Setenv("SYNON_AI_LOG_DIR", logDir)
	app := New(Options{FileRoot: root})
	request := httptest.NewRequest(http.MethodGet, "/api/system/info", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		CacheDir string `json:"cache_dir"`
		WorkDir  string `json:"work_dir"`
		LogDir   string `json:"log_dir"`
		Platform string `json:"platform"`
		Arch     string `json:"arch"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	assertSameTestPath(t, payload.CacheDir, cacheDir)
	assertSameTestPath(t, payload.WorkDir, workDir)
	assertSameTestPath(t, payload.LogDir, logDir)
	if payload.Platform != runtime.GOOS || payload.Arch != runtime.GOARCH {
		t.Fatalf("runtime=%s/%s", payload.Platform, payload.Arch)
	}
}

func TestCollectSynonBiomedSeedProjectsRejectsOversizedManifest(t *testing.T) {
	root := t.TempDir()
	seedDir := filepath.Join(root, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", maxSynonBiomedSeedManifestBytes+1)
	if err := os.WriteFile(filepath.Join(seedDir, "manifest_large.json"), []byte(oversized), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := collectSynonBiomedSeedProjects(root); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("error=%v", err)
	}
}

func assertSameTestPath(t *testing.T, got, want string) {
	t.Helper()
	gotAbsolute, err := filepath.Abs(got)
	if err != nil {
		t.Fatal(err)
	}
	wantAbsolute, err := filepath.Abs(want)
	if err != nil {
		t.Fatal(err)
	}
	if gotAbsolute != wantAbsolute {
		t.Fatalf("path=%q want=%q", gotAbsolute, wantAbsolute)
	}
}

func containsCatalogString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
