package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestProvenanceCensusAPIUsesLiveWorkspaceAndV11CacheKeys(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	writeProvenanceCensusArtifact(t, store, "managed", false)
	writeProvenanceCensusArtifact(t, store, "upload", true)
	app := New(Options{FileRoot: root, Workspace: store})

	first := requestProvenanceCensus(t, app, "/api/system/provenance-census")
	if first.Detection.WindowDays != 7 || first.ComputedAt == "" {
		t.Fatalf("first census metadata = %#v", first)
	}
	if totalProvenanceCensusArtifacts(first.Classes) != 2 {
		t.Fatalf("first census classes = %#v", first.Classes)
	}
	classes := serverProvenanceClassesByName(first.Classes)
	if classes["managed"].N != 1 || classes["upload"].N != 1 ||
		classes["managed"].WithRealChecksum != 1 || classes["upload"].WithRealChecksum != 1 {
		t.Fatalf("first census classification = %#v", classes)
	}

	writeProvenanceCensusArtifact(t, store, "managed-second", false)
	cached := requestProvenanceCensus(t, app, "/api/system/provenance-census")
	if totalProvenanceCensusArtifacts(cached.Classes) != 2 || cached.ComputedAt != first.ComputedAt {
		t.Fatalf("same-key request did not use 60-second cache: first=%#v cached=%#v", first, cached)
	}
	freshKey := requestProvenanceCensus(t, app, "/api/system/provenance-census?window_days=8")
	if freshKey.Detection.WindowDays != 8 || totalProvenanceCensusArtifacts(freshKey.Classes) != 3 {
		t.Fatalf("fresh cache key = %#v", freshKey)
	}
	capped := requestProvenanceCensus(t, app, "/api/system/provenance-census?window_days=999&split_at=2025-01-01")
	if capped.Detection.WindowDays != 365 || capped.Split == nil || capped.Split.At != "2025-01-01T00:00:00Z" {
		t.Fatalf("capped/split census = %#v", capped)
	}
}

func TestProvenanceCensusAPIRejectsInvalidInputAndUnsupportedMethods(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{FileRoot: root, Workspace: store})

	invalid := httptest.NewRequest(http.MethodGet, "/api/system/provenance-census?split_at=not-a-date", nil)
	invalid.RemoteAddr = "127.0.0.1:12345"
	invalidResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest || !strings.Contains(invalidResponse.Body.String(), "split_at must be an ISO date") {
		t.Fatalf("invalid split status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, "/api/system/provenance-census", nil)
	post.RemoteAddr = "127.0.0.1:12345"
	postResponse := httptest.NewRecorder()
	app.Handler().ServeHTTP(postResponse, post)
	if postResponse.Code != http.StatusMethodNotAllowed || postResponse.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("post status=%d allow=%q body=%s", postResponse.Code, postResponse.Header().Get("Allow"), postResponse.Body.String())
	}

	withoutStore := New(Options{FileRoot: t.TempDir()})
	unavailable := httptest.NewRequest(http.MethodGet, "/api/system/provenance-census", nil)
	unavailable.RemoteAddr = "127.0.0.1:12345"
	unavailableResponse := httptest.NewRecorder()
	withoutStore.Handler().ServeHTTP(unavailableResponse, unavailable)
	if unavailableResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", unavailableResponse.Code, unavailableResponse.Body.String())
	}
}

func TestProvenanceCensusCacheWaiterCanCancel(t *testing.T) {
	var cache provenanceCensusCache
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, err := cache.get(context.Background(), "same", func(context.Context) (workspace.ProvenanceCensusResult, error) {
			close(started)
			<-release
			return workspace.ProvenanceCensusResult{ComputedAt: "complete"}, nil
		})
		firstDone <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.get(ctx, "same", func(context.Context) (workspace.ProvenanceCensusResult, error) {
		t.Fatal("in-flight request must be shared")
		return workspace.ProvenanceCensusResult{}, nil
	}); err == nil {
		t.Fatal("cancelled waiter returned no error")
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func writeProvenanceCensusArtifact(t *testing.T, store *workspace.Store, artifactID string, upload bool) {
	t.Helper()
	if _, _, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: "project", Name: artifactID + ".txt",
		ContentType: "text/plain", Content: strings.NewReader(artifactID), CreatedBy: "local",
		IsUserUpload: upload,
	}); err != nil {
		t.Fatal(err)
	}
}

func requestProvenanceCensus(t *testing.T, app *Server, target string) workspace.ProvenanceCensusResult {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload workspace.ProvenanceCensusResult
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func totalProvenanceCensusArtifacts(classes []workspace.ProvenanceCensusClass) int64 {
	var total int64
	for _, class := range classes {
		total += class.N
	}
	return total
}

func serverProvenanceClassesByName(classes []workspace.ProvenanceCensusClass) map[string]workspace.ProvenanceCensusClass {
	result := make(map[string]workspace.ProvenanceCensusClass, len(classes))
	for _, class := range classes {
		result[class.Class] = class
	}
	return result
}
