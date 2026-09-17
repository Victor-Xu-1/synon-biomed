package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestArtifactArchiveListsTarGzipAndServesFiles(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "archive-tar-1", ProjectID: "project-1", Name: "bundle.tar.gz",
		Kind: "application/gzip", Content: tarGzipFixture(t, map[string]string{"results/data.csv": "name,value\nA,1\n"}), CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	base := "/api/artifacts/" + artifact.ID + "/versions/" + version.ID + "/archive"
	listingResponse := authenticatedArchiveRequest(t, app, base, "user-1")
	if listingResponse.Code != http.StatusOK {
		t.Fatalf("tar.gz listing = %d: %s", listingResponse.Code, listingResponse.Body.String())
	}
	var listing artifactArchiveListing
	if err := json.Unmarshal(listingResponse.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if !archiveListingContains(listing, "results/data.csv", false) {
		t.Fatalf("tar.gz listing = %#v", listing.Entries)
	}
	content := authenticatedArchiveRequest(t, app, base+"/content?entry="+url.QueryEscape("results/data.csv"), "user-1")
	if content.Code != http.StatusOK || content.Body.String() != "name,value\nA,1\n" {
		t.Fatalf("tar.gz content = %d: %q", content.Code, content.Body.String())
	}
}

func TestArtifactArchiveListsNestedArchivesAndServesInnerFiles(t *testing.T) {
	inner := zipFixture(t, map[string]string{
		"results/report.md": "# Nested report\n",
	})
	outer := zipBytesFixture(t, map[string][]byte{
		"README.txt":       []byte("root readme\n"),
		"folder/data.csv":  []byte("name,value\nA,1\n"),
		"nested/inner.zip": inner,
	})

	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "archive-1", ProjectID: "project-1", Name: "bundle.zip",
		Kind: "application/zip", Content: outer, CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	base := "/api/artifacts/" + artifact.ID + "/versions/" + version.ID + "/archive"

	root := authenticatedArchiveRequest(t, app, base, "user-1")
	if root.Code != http.StatusOK {
		t.Fatalf("root listing = %d: %s", root.Code, root.Body.String())
	}
	if !bytes.Contains(root.Body.Bytes(), []byte(`"containers":[]`)) {
		t.Fatalf("root listing must encode an empty container path as []: %s", root.Body.String())
	}
	var rootListing artifactArchiveListing
	if err := json.Unmarshal(root.Body.Bytes(), &rootListing); err != nil {
		t.Fatal(err)
	}
	if !archiveListingContains(rootListing, "README.txt", false) || !archiveListingContains(rootListing, "nested/inner.zip", true) {
		t.Fatalf("root listing = %#v", rootListing.Entries)
	}

	nestedURL := base + "?container=" + url.QueryEscape("nested/inner.zip")
	nested := authenticatedArchiveRequest(t, app, nestedURL, "user-1")
	if nested.Code != http.StatusOK {
		t.Fatalf("nested listing = %d: %s", nested.Code, nested.Body.String())
	}
	var nestedListing artifactArchiveListing
	if err := json.Unmarshal(nested.Body.Bytes(), &nestedListing); err != nil {
		t.Fatal(err)
	}
	if !archiveListingContains(nestedListing, "results/report.md", false) {
		t.Fatalf("nested listing = %#v", nestedListing.Entries)
	}

	contentURL := base + "/content?container=" + url.QueryEscape("nested/inner.zip") + "&entry=" + url.QueryEscape("results/report.md")
	content := authenticatedArchiveRequest(t, app, contentURL, "user-1")
	if content.Code != http.StatusOK || content.Body.String() != "# Nested report\n" {
		t.Fatalf("nested content = %d: %q", content.Code, content.Body.String())
	}
	if got := content.Header().Get("Content-Type"); !strings.Contains(got, "text/markdown") {
		t.Fatalf("content type = %q", got)
	}
}

func TestArtifactArchiveRejectsForeignUsersAndTraversal(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "archive-1", ProjectID: "project-1", Name: "bundle.zip",
		Kind: "application/zip", Content: zipFixture(t, map[string]string{"safe.txt": "safe"}), CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	base := "/api/artifacts/" + artifact.ID + "/versions/" + version.ID + "/archive"
	foreign := authenticatedArchiveRequest(t, app, base, "user-2")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign listing = %d: %s", foreign.Code, foreign.Body.String())
	}
	traversal := authenticatedArchiveRequest(t, app, base+"/content?entry="+url.QueryEscape("../safe.txt"), "user-1")
	if traversal.Code != http.StatusBadRequest {
		t.Fatalf("traversal = %d: %s", traversal.Code, traversal.Body.String())
	}
}

func authenticatedArchiveRequest(t *testing.T, handler http.Handler, target, userID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("X-Synon-User-Id", userID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func archiveListingContains(listing artifactArchiveListing, entryPath string, nestedArchive bool) bool {
	for _, entry := range listing.Entries {
		if entry.Path == entryPath && entry.Archive == nestedArchive {
			return true
		}
	}
	return false
}

func zipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	converted := make(map[string][]byte, len(files))
	for name, content := range files {
		converted[name] = []byte(content)
	}
	return zipBytesFixture(t, converted)
}

func zipBytesFixture(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func tarGzipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		payload := []byte(content)
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(payload))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
