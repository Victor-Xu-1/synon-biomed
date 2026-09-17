package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestP5WebConversationWorkspaceUploadLifecycleAndProjectIsolation(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createProjectFrame := func(projectID, frameID string) {
		t.Helper()
		if _, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
			ID: projectID, UserID: "local", Name: projectID,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateFrame(workspace.CreateFrameInput{
			ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: frameID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	createProjectFrame("project-a", "frame-a")
	createProjectFrame("project-b", "frame-b")
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-a-branch", ProjectID: "project-a", ParentFrameID: "frame-a",
		AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "frame-a-branch",
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, FileRoot: filepath.Join(root, "runtime")})

	payload := []byte("name,score\nSTAT6,0.91\n")
	initResponse := p5WorkspaceRequest(t, app, http.MethodPost, "/api/conversations/frame-a/workspace/uploads",
		fmt.Sprintf(`{"filename":"result.csv","total_size":%d,"content_type":"text/csv","chunk_size":1048576}`, len(payload)))
	if initResponse.Code != http.StatusOK {
		t.Fatalf("init status=%d body=%s", initResponse.Code, initResponse.Body.String())
	}
	var initialized struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(initResponse.Body.Bytes(), &initialized); err != nil || initialized.UploadID == "" {
		t.Fatalf("init payload=%s err=%v", initResponse.Body.String(), err)
	}

	foreignChunk := p5WorkspaceRequestBytes(t, app, http.MethodPost,
		"/api/conversations/frame-b/workspace/uploads/"+initialized.UploadID+"/chunks/0", payload, "application/octet-stream")
	if foreignChunk.Code != http.StatusNotFound {
		t.Fatalf("foreign chunk status=%d body=%s", foreignChunk.Code, foreignChunk.Body.String())
	}
	chunk := p5WorkspaceRequestBytes(t, app, http.MethodPost,
		"/api/conversations/frame-a/workspace/uploads/"+initialized.UploadID+"/chunks/0", payload, "application/octet-stream")
	if chunk.Code != http.StatusOK {
		t.Fatalf("chunk status=%d body=%s", chunk.Code, chunk.Body.String())
	}
	finalized := p5WorkspaceRequest(t, app, http.MethodPost,
		"/api/conversations/frame-a/workspace/uploads/"+initialized.UploadID+"/finalize", "")
	if finalized.Code != http.StatusOK {
		t.Fatalf("finalize status=%d body=%s", finalized.Code, finalized.Body.String())
	}
	var artifact struct {
		ArtifactID string `json:"artifact_id"`
		VersionID  string `json:"version_id"`
	}
	if err := json.Unmarshal(finalized.Body.Bytes(), &artifact); err != nil || artifact.ArtifactID == "" || artifact.VersionID == "" {
		t.Fatalf("finalize payload=%s err=%v", finalized.Body.String(), err)
	}
	metadata, found, err := store.GetCompatibilityArtifactMetadata(t.Context(), "local", artifact.ArtifactID)
	if err != nil || !found || metadata.ProjectID != "project-a" || metadata.RootFrameID == nil || *metadata.RootFrameID != "frame-a" || metadata.FrameID == nil || *metadata.FrameID != "frame-a" {
		t.Fatalf("metadata=%#v found=%v err=%v", metadata, found, err)
	}

	rootList := p5WorkspaceRequest(t, app, http.MethodGet, "/api/conversations/frame-a/workspace?path=.", "")
	if rootList.Code != http.StatusOK || !strings.Contains(rootList.Body.String(), `"relative_path":"project-files"`) {
		t.Fatalf("root status=%d body=%s", rootList.Code, rootList.Body.String())
	}
	files := p5WorkspaceRequest(t, app, http.MethodGet, "/api/conversations/frame-a/workspace?path=project-files", "")
	if files.Code != http.StatusOK || !strings.Contains(files.Body.String(), artifact.ArtifactID) || !strings.Contains(files.Body.String(), "result.csv") {
		t.Fatalf("files status=%d body=%s", files.Code, files.Body.String())
	}
	var artifactPage struct {
		Items []struct {
			ArtifactID string `json:"artifact_id"`
			VersionID  string `json:"version_id"`
			ContentURL string `json:"content_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(files.Body.Bytes(), &artifactPage); err != nil || len(artifactPage.Items) != 1 ||
		artifactPage.Items[0].ArtifactID != artifact.ArtifactID || artifactPage.Items[0].VersionID != artifact.VersionID ||
		artifactPage.Items[0].ContentURL != "/api/artifacts/"+url.PathEscape(artifact.ArtifactID)+"/versions/"+url.PathEscape(artifact.VersionID) {
		t.Fatalf("artifact page=%#v err=%v", artifactPage, err)
	}
	secondPayload := []byte("name,score\nSTAT6,0.97\n")
	_, secondVersion, err := store.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(t.Context(), "web-conversation-artifact-version-2"),
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifact.ArtifactID, ProjectID: "project-a", Name: "result.csv", ContentType: "text/csv",
			Content: bytes.NewReader(secondPayload), MaxBytes: 1 << 20, RootFrameID: "frame-a", FrameID: "frame-a",
			IsUserUpload: true,
		},
		"local",
	)
	if err != nil {
		t.Fatal(err)
	}
	oldVersion := p5WorkspaceRequest(t, app, http.MethodGet, artifactPage.Items[0].ContentURL, "")
	if oldVersion.Code != http.StatusOK || !bytes.Equal(oldVersion.Body.Bytes(), payload) {
		t.Fatalf("old version status=%d body=%q", oldVersion.Code, oldVersion.Body.Bytes())
	}
	currentArtifacts, err := store.ListCompatibilityProjectCurrentArtifacts(t.Context(), "local", "project-a", true, 10)
	if err != nil || len(currentArtifacts) != 1 || currentArtifacts[0].VersionID != secondVersion.ID ||
		len(currentArtifacts[0].AllVersionIDs) != 0 || currentArtifacts[0].CreatingVersionID != nil {
		t.Fatalf("current artifacts=%#v err=%v", currentArtifacts, err)
	}
	versionedArtifacts, err := store.ListCompatibilityProjectArtifacts(t.Context(), "local", "project-a", true, 10)
	if err != nil || len(versionedArtifacts) != 1 || len(versionedArtifacts[0].AllVersionIDs) != 2 ||
		versionedArtifacts[0].CreatingVersionID == nil {
		t.Fatalf("versioned artifacts=%#v err=%v", versionedArtifacts, err)
	}
	conversationArtifactRequest := fmt.Sprintf(
		`{"references":[{"artifact_id":%q,"version_id":%q},{"artifact_id":%q,"version_id":%q}]}`,
		artifact.ArtifactID, artifact.VersionID, artifact.ArtifactID, secondVersion.ID,
	)
	conversationArtifacts := p5WorkspaceRequest(t, app, http.MethodPost, "/api/conversations/frame-a/artifacts", conversationArtifactRequest)
	var artifactCollections []struct {
		Kind    string `json:"kind"`
		Payload struct {
			Files []struct {
				ArtifactID  string `json:"artifact_id"`
				VersionID   string `json:"version_id"`
				ContentURL  string `json:"content_url"`
				PreviewKind string `json:"preview_kind"`
			} `json:"files"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(conversationArtifacts.Body.Bytes(), &artifactCollections); err != nil ||
		conversationArtifacts.Code != http.StatusOK || len(artifactCollections) != 1 ||
		artifactCollections[0].Kind != "scientific_files" || len(artifactCollections[0].Payload.Files) != 2 {
		t.Fatalf("conversation artifacts status=%d body=%s err=%v", conversationArtifacts.Code, conversationArtifacts.Body.String(), err)
	}
	branchArtifacts := p5WorkspaceRequest(t, app, http.MethodPost, "/api/conversations/frame-a-branch/artifacts", conversationArtifactRequest)
	if branchArtifacts.Code != http.StatusOK || !strings.Contains(branchArtifacts.Body.String(), artifact.VersionID) ||
		!strings.Contains(branchArtifacts.Body.String(), `"root_frame_id":"frame-a"`) {
		t.Fatalf("branch conversation artifacts status=%d body=%s", branchArtifacts.Code, branchArtifacts.Body.String())
	}
	versionOnlyRequest := fmt.Sprintf(`{"references":[],"version_ids":[%q]}`, artifact.VersionID)
	versionOnlyResponse := p5WorkspaceRequest(t, app, http.MethodPost, "/api/conversations/frame-a/artifacts", versionOnlyRequest)
	var versionOnlyCollections []struct {
		Payload struct {
			Files []struct {
				ArtifactID string `json:"artifact_id"`
				VersionID  string `json:"version_id"`
			} `json:"files"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(versionOnlyResponse.Body.Bytes(), &versionOnlyCollections); err != nil ||
		versionOnlyResponse.Code != http.StatusOK || len(versionOnlyCollections) != 1 ||
		len(versionOnlyCollections[0].Payload.Files) != 1 ||
		versionOnlyCollections[0].Payload.Files[0].ArtifactID != artifact.ArtifactID ||
		versionOnlyCollections[0].Payload.Files[0].VersionID != artifact.VersionID {
		t.Fatalf("version-only artifacts status=%d body=%s err=%v", versionOnlyResponse.Code, versionOnlyResponse.Body.String(), err)
	}
	legacyConversationArtifacts := p5WorkspaceRequest(t, app, http.MethodGet, "/api/conversations/frame-a/artifacts", "")
	if legacyConversationArtifacts.Code != http.StatusMethodNotAllowed || legacyConversationArtifacts.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("legacy conversation artifacts status=%d allow=%q body=%s", legacyConversationArtifacts.Code,
			legacyConversationArtifacts.Header().Get("Allow"), legacyConversationArtifacts.Body.String())
	}
	invalidConversationArtifacts := p5WorkspaceRequest(t, app, http.MethodPost, "/api/conversations/frame-a/artifacts", `{`)
	if invalidConversationArtifacts.Code != http.StatusBadRequest ||
		invalidConversationArtifacts.Body.String() != "{\"message\":\"invalid artifact reference batch\"}\n" {
		t.Fatalf("invalid conversation artifacts status=%d body=%s", invalidConversationArtifacts.Code,
			invalidConversationArtifacts.Body.String())
	}
	filesByVersion := make(map[string]struct {
		ArtifactID  string
		ContentURL  string
		PreviewKind string
	}, len(artifactCollections[0].Payload.Files))
	for _, file := range artifactCollections[0].Payload.Files {
		filesByVersion[file.VersionID] = struct {
			ArtifactID  string
			ContentURL  string
			PreviewKind string
		}{ArtifactID: file.ArtifactID, ContentURL: file.ContentURL, PreviewKind: file.PreviewKind}
	}
	for _, versionID := range []string{artifact.VersionID, secondVersion.ID} {
		file, found := filesByVersion[versionID]
		wantURL := "/api/artifacts/" + artifact.ArtifactID + "/versions/" + versionID
		if !found || file.ArtifactID != artifact.ArtifactID || file.ContentURL != wantURL || file.PreviewKind != "csv" {
			t.Fatalf("version %q file=%#v found=%t wantURL=%q", versionID, file, found, wantURL)
		}
	}
	for _, test := range []struct {
		versionID string
		content   []byte
	}{{artifact.VersionID, payload}, {secondVersion.ID, secondPayload}} {
		exact := p5WorkspaceRequest(t, app, http.MethodGet,
			"/api/artifacts/"+artifact.ArtifactID+"/versions/"+test.versionID, "")
		if exact.Code != http.StatusOK || !bytes.Equal(exact.Body.Bytes(), test.content) ||
			exact.Header().Get("X-Artifact-Id") != artifact.ArtifactID ||
			exact.Header().Get("X-Artifact-Version-Id") != test.versionID {
			t.Fatalf("exact version=%q status=%d headers=%v body=%q", test.versionID, exact.Code, exact.Header(), exact.Body.Bytes())
		}
	}
	foreignConversationArtifacts := p5WorkspaceRequest(t, app, http.MethodPost, "/api/conversations/frame-b/artifacts", conversationArtifactRequest)
	if foreignConversationArtifacts.Code != http.StatusOK || foreignConversationArtifacts.Body.String() != "[]\n" || strings.Contains(foreignConversationArtifacts.Body.String(), artifact.VersionID) {
		t.Fatalf("foreign conversation artifacts status=%d body=%s", foreignConversationArtifacts.Code, foreignConversationArtifacts.Body.String())
	}
	foreignVersionOnly := p5WorkspaceRequest(t, app, http.MethodPost, "/api/conversations/frame-b/artifacts", versionOnlyRequest)
	if foreignVersionOnly.Code != http.StatusOK || foreignVersionOnly.Body.String() != "[]\n" || strings.Contains(foreignVersionOnly.Body.String(), artifact.VersionID) {
		t.Fatalf("foreign version-only artifacts status=%d body=%s", foreignVersionOnly.Code, foreignVersionOnly.Body.String())
	}

	download := p5WorkspaceRequest(t, app, http.MethodGet, "/api/artifacts/"+artifact.ArtifactID, "")
	if download.Code != http.StatusOK || !bytes.Equal(download.Body.Bytes(), secondPayload) {
		t.Fatalf("download status=%d body=%q", download.Code, download.Body.Bytes())
	}
	rename := p5WorkspaceRequest(t, app, http.MethodPatch,
		"/api/conversations/frame-a/workspace/artifacts/"+artifact.ArtifactID, `{"filename":"renamed.csv"}`)
	if rename.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rename.Code, rename.Body.String())
	}
	foreignDelete := p5WorkspaceRequest(t, app, http.MethodDelete,
		"/api/conversations/frame-b/workspace/artifacts/"+artifact.ArtifactID, "")
	if foreignDelete.Code != http.StatusNotFound {
		t.Fatalf("foreign delete status=%d body=%s", foreignDelete.Code, foreignDelete.Body.String())
	}
	deleted := p5WorkspaceRequest(t, app, http.MethodDelete,
		"/api/conversations/frame-a/workspace/artifacts/"+artifact.ArtifactID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestP5WebScientificPreviewKind(t *testing.T) {
	tests := []struct {
		filename, contentType, want string
	}{
		{"report.md", "text/markdown", "markdown"},
		{"table.csv", "text/csv", "csv"},
		{"structure.pdb", "chemical/x-pdb", "structure"},
		{"ligand.sdf", "chemical/x-mdl-sdfile", "structure"},
		{"ligand.mol", "chemical/x-mdl-molfile", "structure"},
		{"ligand.mol2", "chemical/x-mol2", "structure"},
		{"ligand.xyz", "chemical/x-xyz", "structure"},
		{"library.smi", "chemical/x-daylight-smiles", "molecule"},
		{"library.smiles", "chemical/smiles", "molecule"},
		{"unsupported.ket", "chemical/x-indigo-ket", "binary"},
		{"unsupported.rxn", "chemical/x-mdl-rxnfile", "binary"},
		{"aligned.fasta", "text/x-fasta", "msa"},
		{"sequence.fasta", "text/x-fasta", "sequence"},
		{"variants.vcf.gz", "application/gzip", "genome"},
		{"cells.h5ad", "application/x-hdf5", "anndata"},
		{"analysis.ipynb", "application/x-ipynb+json", "notebook"},
		{"results.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "spreadsheet"},
		{"US223898A.pdf", "application/pdf", "pdf"},
		{"paper.tex", "application/x-latex", "latex"},
		{"payload.bin", "application/octet-stream", "binary"},
	}
	for _, test := range tests {
		t.Run(test.filename, func(t *testing.T) {
			if got := webScientificPreviewKind(test.filename, test.contentType); got != test.want {
				t.Fatalf("webScientificPreviewKind(%q, %q)=%q want %q", test.filename, test.contentType, got, test.want)
			}
		})
	}
}

func p5WorkspaceRequest(t *testing.T, app *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return p5WorkspaceRequestBytes(t, app, method, path, []byte(body), "application/json")
}

func p5WorkspaceRequestBytes(
	t *testing.T,
	app *Server,
	method, path string,
	body []byte,
	contentType string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	if len(body) > 0 {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}
