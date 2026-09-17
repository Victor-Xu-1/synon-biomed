package server

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/realtime"
)

func TestFinalQueryContractsUseOwnedDurableRuntimeState(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills", "draft-one")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("---\nname: draft-one\ndescription: Draft\n---\nDraft body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, compatibilitySkillDraftMarker), []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalSkill := filepath.Join(root, "external-draft")
	if err := os.MkdirAll(externalSkill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalSkill, "SKILL.md"), []byte("---\nname: linked-draft\ndescription: Linked\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalSkill, compatibilitySkillDraftMarker), []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalSkill, filepath.Join(root, "skills", "linked-draft")); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "project", UserID: "owner", Name: "Owned"},
		{ID: "foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Alpha Bench"},
		{ID: "reviewer", ProjectID: "project", ParentFrameID: "root", AgentName: "REVIEWER", Status: "processing", ConversationType: "delegate"},
		{ID: "foreign-root", ProjectID: "foreign", AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Foreign Bench"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SetFrameRuntimeMetadata("root", workspace.FrameRuntimeMetadata{TaskSummary: "Alpha screening", ContextData: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameSubmissionMetadata("reviewer", map[string]any{"_review_target_frame_id": "root"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: "root", Type: "user_message", Payload: map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "text", "text": "Original"}}, "_uuid": "original",
	}}); err != nil {
		t.Fatal(err)
	}
	branch, err := store.ForkCompatibilityRoot(workspace.CompatibilityRootForkInput{RootFrameID: "root", MessageIndex: 0, EditedContent: "Edited", AgentName: "OPERON"})
	if err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	if _, err := store.UpdateFrame("root", workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.WriteArtifactVersion(t.Context(), workspace.WriteArtifactVersionInput{
		ArtifactID: "report", ProjectID: "project", Name: "alpha-report.md", ContentType: "text/markdown",
		Content: bytes.NewBufferString("result"), MaxBytes: 1 << 20, RootFrameID: "root", FrameID: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	seedFinalQueryTokenUsage(t, databasePath)
	if _, found, err := store.TokenClassBreakdownForRoot("root", "owner"); err != nil || !found {
		t.Fatalf("root token class store read found=%v err=%v", found, err)
	}

	app := New(Options{Workspace: store, FileRoot: root, VerifierToken: "verify-token"})
	handler := app.Handler()
	branchMessages := compatJSONRequest(t, handler, http.MethodGet, "/api/frames/root/branches/"+branch.BranchID+"/messages", "owner", nil, http.StatusOK)
	if branchMessages["branch_id"] != branch.BranchID || len(branchMessages["messages"].([]any)) != 1 {
		t.Fatalf("branch messages = %#v", branchMessages)
	}
	compatJSONRequest(t, handler, http.MethodGet, "/api/frames/root/branches/invalid/messages", "owner", nil, http.StatusBadRequest)

	verificationBody := map[string]any{
		"status": "done", "n": 2, "verdict": "warn", "count": 1,
		"claims": []any{map[string]any{"id": "claim-1", "frame_id": "root", "claim_text": "Result is reproducible", "entities": []any{"result"}, "source": "agent"}},
		"checks": []any{map[string]any{"id": "check-1", "artifact_version_id": version.ID, "claim_id": "claim-1", "claim": "Result is reproducible", "verdict": "warn", "severity": "medium", "evidence": "Review required", "reviewer_idx": 0, "source_ref": map[string]any{"frame_id": "root"}, "status": "open", "reflag_count": 0}},
	}
	compatJSONRequest(t, handler, http.MethodPost, "/api/frames/root/verification", "owner", verificationBody, http.StatusForbidden)
	request := compatRequest(t, http.MethodPost, "/api/frames/root/verification", "owner", verificationBody)
	request.Header.Set("X-Synon-Verification-Token", "verify-token")
	recorder := httptestResponse(t, handler, request, http.StatusAccepted)
	_ = recorder
	verification := compatJSONRequest(t, handler, http.MethodGet, "/api/frames/root/verification?status=open", "owner", nil, http.StatusOK)
	if len(verification["checks"].([]any)) != 1 || len(verification["claims"].([]any)) != 1 || len(verification["running"].([]any)) != 1 {
		t.Fatalf("verification = %#v", verification)
	}
	compatJSONRequest(t, handler, http.MethodGet, "/api/frames/root/verification?status=invalid", "owner", nil, http.StatusBadRequest)
	compatJSONRequest(t, handler, http.MethodGet, "/api/frames/root/verification", "other", nil, http.StatusNotFound)
	artifactChecks := compatJSONRequest(t, handler, http.MethodGet, "/api/artifacts/versions/"+version.ID+"/verification", "owner", nil, http.StatusOK)
	if len(artifactChecks["checks"].([]any)) != 1 {
		t.Fatalf("artifact verification = %#v", artifactChecks)
	}
	compatJSONRequest(t, handler, http.MethodGet, "/api/artifacts/versions/"+version.ID+"/verification", "other", nil, http.StatusNotFound)

	frameTokens := compatJSONRequest(t, handler, http.MethodGet, "/api/frames/root/token-classes", "owner", nil, http.StatusOK)
	classes := frameTokens["classes"].(map[string]any)
	mainClass := classes["main"].(map[string]any)
	totals := frameTokens["totals"].(map[string]any)
	if mainClass["input"] != float64(70) || mainClass["credits"] != float64(0.64) ||
		totals["total_cost"] != float64(1.75) || totals["aux_cost"] != float64(0.5) ||
		frameTokens["total_frames"] != float64(2) {
		t.Fatalf("frame tokens = %#v", frameTokens)
	}
	series := compatJSONRequest(t, handler, http.MethodGet, "/api/token-classes?window=24h", "owner", nil, http.StatusOK)
	if len(series["sessions"].([]any)) != 1 {
		t.Fatalf("token series = %#v", series)
	}
	compatJSONRequest(t, handler, http.MethodGet, "/api/token-classes?window=1h", "owner", nil, http.StatusBadRequest)

	artifactsBatch := compatJSONRequest(t, handler, http.MethodGet, "/api/projects/batch/artifacts?pids=project,foreign&limit=20", "owner", nil, http.StatusOK)
	foreignArtifacts, foreignFound := artifactsBatch["foreign"].([]any)
	if len(artifactsBatch) != 2 || len(artifactsBatch["project"].([]any)) != 1 || !foreignFound || len(foreignArtifacts) != 0 {
		t.Fatalf("artifact batch = %#v", artifactsBatch)
	}
	benches := compatJSONArrayRequest(t, handler, http.MethodGet, "/api/projects/project/benches?q=Alpha&limit=20", "owner", nil, http.StatusOK)
	if len(benches) != 1 || benches[0]["id"] != "root" {
		t.Fatalf("bench name search = %#v", benches)
	}
	benchesBatch := compatJSONRequest(t, handler, http.MethodGet, "/api/projects/batch/benches?pids=project,foreign&q=Alpha&limit=20", "owner", nil, http.StatusOK)
	if len(benchesBatch) != 1 || len(benchesBatch["project"].([]any)) != 1 {
		t.Fatalf("bench search batch = %#v", benchesBatch)
	}
	bench := compatJSONRequest(t, handler, http.MethodGet, "/api/frames/root?shallow=true", "owner", nil, http.StatusOK)
	if bench["id"] != "root" || bench["project_id"] != "project" {
		t.Fatalf("bench = %#v", bench)
	}
	drafts := compatJSONRequest(t, handler, http.MethodGet, "/api/skills/drafts", "owner", nil, http.StatusOK)
	if !reflect.DeepEqual(drafts["drafts"], []any{"draft-one"}) {
		t.Fatalf("drafts = %#v", drafts)
	}
	if artifact.ID != "report" {
		t.Fatalf("artifact = %#v", artifact)
	}
}

func TestFinalQueryContractInvalidationsAreExactAndBounded(t *testing.T) {
	frame := realtime.Invalidations("frame_update", map[string]any{"project_id": "project", "root_frame_id": "root", "frame_id": "root", "branch_id": "br_12345678"})
	for _, expected := range []struct {
		name  string
		key   []any
		match string
	}{
		{"branchMessages", []any{"branch-messages", "root", "br_12345678"}, "prefix"},
		{"tokenSeries", []any{"token-series"}, "prefix"},
		{"searchBenchesBatch", []any{"search-benches-batch"}, "prefix"},
		{"benchNameSearch", []any{"bench-name-search", "project"}, "prefix"},
		{"benchNameSearchBatch", []any{"bench-name-search-batch"}, "prefix"},
		{"bench", []any{"bench", "project", "root"}, "exact"},
	} {
		assertFinalQueryInvalidation(t, frame, expected.name, expected.key, expected.match)
	}
	artifact := realtime.Invalidations("artifact_created", map[string]any{"project_id": "project", "artifact_id": "artifact"})
	assertFinalQueryInvalidation(t, artifact, "searchArtifactsBatch", []any{"search-artifacts-batch"}, "prefix")
	verification := realtime.Invalidations("verification_update", map[string]any{"root_frame_id": "root", "version_ids": []any{"version"}})
	assertFinalQueryInvalidation(t, verification, "verificationChecks", []any{"verification-checks", "root"}, "exact")
	assertFinalQueryInvalidation(t, verification, "artifactVerification", []any{"artifact-verification", "version"}, "exact")
	for name, args := range map[string][]any{
		"searchArtifactsBatch": {"query"}, "searchBenchesBatch": {"query"},
		"benchNameSearch": {"project", "query"}, "benchNameSearchBatch": {"projects", "query"},
		"bench": {"project", "root"}, "skillDrafts": {},
	} {
		if _, err := realtime.ResolveQueryKey(name, args...); err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
	}
}

func seedFinalQueryTokenUsage(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE frame_runtime_metadata SET input_tokens=100, output_tokens=20, cache_read_tokens=10, cache_write_tokens=5, total_cost=1.25, aux_cost=0.5, token_class_usage=? WHERE frame_id='root'`, `{"main":{"input":70,"output":20,"cache_read":10,"cache_write":5,"cost":1.0}}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frames SET updated_at=? WHERE id='root'`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func assertFinalQueryInvalidation(t *testing.T, values []realtime.QueryInvalidation, name string, key []any, match string) {
	t.Helper()
	for _, value := range values {
		if value.Query == name && reflect.DeepEqual(value.Key, key) && value.Match == match {
			return
		}
	}
	t.Fatalf("missing invalidation %s %#v/%s in %#v", name, key, match, values)
}

func httptestResponse(t *testing.T, handler http.Handler, request *http.Request, want int) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != want {
		t.Fatalf("%s %s status=%d body=%s", request.Method, request.URL.Path, recorder.Code, recorder.Body.String())
	}
	return recorder
}
