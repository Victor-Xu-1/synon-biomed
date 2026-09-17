package server

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityProjectArtifactAndBenchReadsUseRealOwnedStateAcrossRestart(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateProjectInput{
		{ID: "project-a", UserID: "local", Name: "Project A"},
		{ID: "project-b", UserID: "local", Name: "Project B"},
		{ID: "project-foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []workspace.CreateFrameInput{
		{ID: "root-a", ProjectID: "project-a", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Alpha Bench"},
		{ID: "child-a", ProjectID: "project-a", ParentFrameID: "root-a", AgentName: "ANALYST", Status: "awaiting_user_response", ConversationType: "delegate", Name: "Child"},
		{ID: "hidden-a", ProjectID: "project-a", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Hidden Bench"},
		{ID: "placeholder-a", ProjectID: "project-a", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
		{ID: "root-b", ProjectID: "project-b", AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Beta Bench"},
		{ID: "root-foreign", ProjectID: "project-foreign", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "Foreign Bench"},
	} {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
	}
	if awaiting, err := store.CompatibilityBenchHasDescendantAwaitingInput(context.Background(), "root-a"); err != nil || awaiting {
		t.Fatalf("bare descendant waiting status awaiting=%t err=%v", awaiting, err)
	}
	if _, err := store.SetFrameRuntimeMetadata("child-a", workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_pending_input_requests": []any{map[string]any{"request_id": "ask-child-a"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if awaiting, err := store.CompatibilityBenchHasDescendantAwaitingInput(context.Background(), "root-a"); err != nil || !awaiting {
		t.Fatalf("evidence-backed descendant waiting awaiting=%t err=%v", awaiting, err)
	}
	if _, err := store.SetFrameRuntimeMetadata("root-a", workspace.FrameRuntimeMetadata{TaskSummary: "Analyze sample", ContextData: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameSubmissionMetadata("hidden-a", map[string]any{"request": "private"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "child-a", Type: "assistant_message", Payload: map[string]any{"text": "child finished"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameOutputData("root-a", map[string]any{"result": "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.QueueCompatibilityMessage(
		"child-a", "queued-intent", map[string]any{"text": "Follow up"}, map[string]any{"text": "Follow up"},
	); err != nil {
		t.Fatal(err)
	}

	report, firstReportVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "report", ProjectID: "project-a", Name: "report.md", ContentType: "text/markdown",
		Content: bytes.NewBufferString("version one"), MaxBytes: 1 << 20, RootFrameID: "root-a", FrameID: "root-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, currentReportVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: report.ID, ProjectID: "project-a", Name: "report.md", ContentType: "text/markdown",
		Content: bytes.NewBufferString("version two"), MaxBytes: 1 << 20, ParentVersionID: firstReportVersion.ID,
		RootFrameID: "root-a", FrameID: "child-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, imageVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "image", ProjectID: "project-a", Name: "figure.png", ContentType: "image/png",
		Content: bytes.NewReader([]byte{0x89, 'P', 'N', 'G'}), MaxBytes: 1 << 20, RootFrameID: "root-a", FrameID: "child-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, intermediateVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "intermediate", ProjectID: "project-a", Name: "scratch.txt", ContentType: "text/plain",
		Content: bytes.NewBufferString("scratch"), MaxBytes: 1 << 20, RootFrameID: "root-a", FrameID: "root-a",
	})
	if err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, provenance := range []struct {
		versionID, frameID, contentType, agentName string
		intermediate                               int
	}{
		{firstReportVersion.ID, "root-a", "text/markdown", "OPERON", 0},
		{currentReportVersion.ID, "child-a", "text/markdown", "ANALYST", 0},
		{imageVersion.ID, "child-a", "image/png", "ANALYST", 0},
		{intermediateVersion.ID, "root-a", "text/plain", "OPERON", 1},
	} {
		if _, err := db.Exec(`
			UPDATE artifact_version_provenance
			SET frame_id = ?, content_type = ?, agent_name = ?, language = 'python', is_intermediate = ?
			WHERE version_id = ?`,
			provenance.frameID, provenance.contentType, provenance.agentName, provenance.intermediate, provenance.versionID,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE artifacts SET priority = 'user_starred' WHERE id = 'report'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	server := New(Options{FileRoot: runtimeRoot, Workspace: store})
	benchFrames, found, err := store.ListCompatibilityProjectBenches(context.Background(), "local", "project-a", 10, "")
	if err != nil || !found || len(benchFrames) != 1 {
		t.Fatalf("direct bench read = %#v found=%v err=%v", benchFrames, found, err)
	}
	if _, err := server.compatibilityBenchResponses(context.Background(), benchFrames); err != nil {
		t.Fatalf("direct bench projection: %v", err)
	}
	app := server.Handler()
	assertProjectCollectionReads(t, app, databasePath, currentReportVersion.ID, firstReportVersion.ID)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted := New(Options{FileRoot: runtimeRoot, Workspace: store}).Handler()
	assertProjectCollectionReads(t, restarted, databasePath, currentReportVersion.ID, firstReportVersion.ID)
}

func assertProjectCollectionReads(t *testing.T, app http.Handler, databasePath, currentVersionID, firstVersionID string) {
	t.Helper()
	artifacts := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/project-a/artifacts?limit=10", "local", nil, http.StatusOK)
	if len(artifacts) != 2 {
		t.Fatalf("default artifact list = %#v", artifacts)
	}
	report := compatibilityCollectionItem(t, artifacts, "id", "report")
	if report["version_id"] != currentVersionID || report["version_number"] != float64(2) ||
		report["root_frame_id"] != "root-a" || report["frame_id"] != "child-a" ||
		report["creating_frame_id"] != "root-a" || report["creating_version_id"] != firstVersionID ||
		report["priority"] != "user_starred" || report["agent_name"] != "ANALYST" || report["is_intermediate"] != false {
		t.Fatalf("report projection = %#v", report)
	}
	versions, _ := report["all_version_ids"].([]any)
	if len(versions) != 2 || versions[0] != firstVersionID || versions[1] != currentVersionID {
		t.Fatalf("report versions = %#v", versions)
	}
	filePath, _ := report["file_path"].(string)
	if filePath == "" || !filepath.IsAbs(filePath) || filepath.Clean(filePath) == filepath.Clean(databasePath) {
		t.Fatalf("artifact file path = %q", filePath)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("artifact file path is not readable: %v", err)
	}
	allArtifacts := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/project-a/artifacts?exclude_intermediate=false&limit=10", "local", nil, http.StatusOK)
	if len(allArtifacts) != 3 || compatibilityCollectionItem(t, allArtifacts, "id", "intermediate")["is_intermediate"] != true {
		t.Fatalf("artifact list including intermediate = %#v", allArtifacts)
	}
	for _, target := range []string{"/api/projects/missing/artifacts", "/api/projects/project-foreign/artifacts"} {
		response := compatJSONArrayRequest(t, app, http.MethodGet, target, "local", nil, http.StatusOK)
		if len(response) != 0 {
			t.Fatalf("isolated artifact list %s = %#v", target, response)
		}
	}

	benches := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/project-a/benches?limit=10", "local", nil, http.StatusOK)
	if len(benches) != 1 {
		t.Fatalf("bench list = %#v", benches)
	}
	bench := benches[0]
	children, _ := bench["children"].([]any)
	output, _ := bench["output_data"].(map[string]any)
	queued, _ := bench["queued_user_messages"].([]any)
	if bench["id"] != "root-a" || bench["task_summary"] != "Analyze sample" || bench["has_image_output"] != true ||
		bench["input_data"] != nil || bench["message_count"] != nil || bench["last_activity_at"] == nil || len(children) != 1 ||
		output["result"] != "ready" || output["descendant_awaiting_input"] != true || len(queued) != 1 {
		t.Fatalf("bench projection = %#v", bench)
	}
	queuedMessage, _ := queued[0].(map[string]any)
	if queuedMessage["id"] != "queued-intent" || queuedMessage["text"] != "Follow up" ||
		queuedMessage["frame_id"] != "child-a" || queuedMessage["queued_at"] == nil {
		t.Fatalf("queued bench message = %#v", queuedMessage)
	}
	searched := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/project-a/benches?q=sample", "local", nil, http.StatusOK)
	if len(searched) != 1 {
		t.Fatalf("bench search = %#v", searched)
	}
	short := compatJSONRequest(t, app, http.MethodGet, "/api/projects/project-a/benches?q=x", "local", nil, http.StatusBadRequest)
	if short["detail"] != "q must be at least 2 characters" {
		t.Fatalf("short bench query = %#v", short)
	}
	missing := compatJSONRequest(t, app, http.MethodGet, "/api/projects/missing/benches", "local", nil, http.StatusNotFound)
	if missing["detail"] != "Project missing not found" {
		t.Fatalf("missing bench project = %#v", missing)
	}
	batch := compatJSONRequest(t, app, http.MethodGet,
		"/api/projects/batch/benches?pids=project-a,project-b,project-foreign,missing&limit=10", "local", nil, http.StatusOK)
	if len(batch) != 2 {
		t.Fatalf("bench batch = %#v", batch)
	}
	if values, _ := batch["project-a"].([]any); len(values) != 1 {
		t.Fatalf("project-a batch = %#v", batch["project-a"])
	}
	if values, _ := batch["project-b"].([]any); len(values) != 1 {
		t.Fatalf("project-b batch = %#v", batch["project-b"])
	}
	invalidLimit := compatJSONRequest(t, app, http.MethodGet, "/api/projects/project-a/benches?limit=0", "local", nil, http.StatusBadRequest)
	if invalidLimit["detail"] != "limit must be a positive integer" {
		t.Fatalf("invalid bench limit = %#v", invalidLimit)
	}
	projectIDs := make([]string, 1001)
	for i := range projectIDs {
		projectIDs[i] = fmt.Sprintf("project-%d", i)
	}
	tooMany := compatJSONRequest(t, app, http.MethodGet,
		"/api/projects/batch/benches?pids="+strings.Join(projectIDs, ","), "local", nil, http.StatusBadRequest)
	if tooMany["detail"] != "pids is limited to 1000 project ids" {
		t.Fatalf("oversized bench batch = %#v", tooMany)
	}
}

func compatibilityCollectionItem(t *testing.T, values []map[string]any, key, wanted string) map[string]any {
	t.Helper()
	for _, item := range values {
		if item[key] == wanted {
			return item
		}
	}
	t.Fatalf("item %s=%s not found in %#v", key, wanted, values)
	return nil
}
