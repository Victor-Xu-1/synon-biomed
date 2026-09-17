package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestKernelHostInspectionScopesArtifactsFramesPathsAndLineage(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	if err := os.MkdirAll(identity.workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "child-inspection", ProjectID: identity.access.Frame.ProjectID, ParentFrameID: identity.access.Frame.ID, AgentName: "RESEARCHER", Status: "completed", ConversationType: "chat", Name: "Child evidence"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(identity.access.Frame.ID, workspace.FrameRuntimeMetadata{ContextData: map[string]any{"_messages": []any{
		map[string]any{"role": "user", "content": "inspect the project"},
		map[string]any{"role": "tool", "content": []any{map[string]any{"type": "tool_result", "text": "private tool payload"}}},
		map[string]any{"role": "assistant", "content": "inspection complete"},
	}}, TaskSummary: "Inspect project data"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("child-inspection", workspace.FrameRuntimeMetadata{DelegateName: "evidence-worker", ContextData: map[string]any{}, TaskSummary: "Collect evidence"}); err != nil {
		t.Fatal(err)
	}

	inputArtifact, inputVersion := writeKernelInspectionArtifact(t, store, identity.access, "artifact-input", "input.csv", "text/csv", "name,value\nA,1\n", "inspection-input")
	_, outputVersion := writeKernelInspectionArtifact(t, store, identity.access, "artifact-output", "analysis.txt", "text/plain", "derived from input A\n", "inspection-output")
	largeResult, err := store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-" + strings.Repeat("a", 32),
		ProjectID:  identity.access.Frame.ProjectID, RootFrameID: identity.access.Frame.RootFrameID,
		FrameID: identity.access.Frame.ID, StreamUID: "frame:" + identity.access.Frame.ID,
		OwnerUserID: identity.access.UserID, RunnerID: "inspection-runner", ClaimToken: "inspection-claim",
		Attempt: 1, SourceEventID: 1, ToolName: "web_fetch", ToolCallID: "inspection-large-result",
		Content: []byte(`{"ok":true,"result":{"body":"large immutable evidence"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordArtifactVersionDependency(context.Background(), outputVersion.ID, inputVersion.ID, "input.csv"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-same-owner", UserID: identity.access.UserID, Name: "Same owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "root-same-owner", ProjectID: "project-same-owner", AgentName: "OPERON", Status: "completed", ConversationType: "chat"}); err != nil {
		t.Fatal(err)
	}
	writeKernelInspectionArtifact(t, store, workspace.KernelFrameAccess{UserID: identity.access.UserID, Frame: workspace.Frame{ID: "root-same-owner", RootFrameID: "root-same-owner", ProjectID: "project-same-owner"}}, "artifact-same-owner", "other.md", "text/markdown", "same owner content", "inspection-other")
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-foreign", UserID: "foreign-owner", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "root-foreign", ProjectID: "project-foreign", AgentName: "OPERON", Status: "completed", ConversationType: "chat"}); err != nil {
		t.Fatal(err)
	}
	writeKernelInspectionArtifact(t, store, workspace.KernelFrameAccess{UserID: "foreign-owner", Frame: workspace.Frame{ID: "root-foreign", RootFrameID: "root-foreign", ProjectID: "project-foreign"}}, "artifact-foreign", "secret.txt", "text/plain", "must never leak", "inspection-foreign")
	foreignLargeResult, err := store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-" + strings.Repeat("b", 32),
		ProjectID:  "project-foreign", RootFrameID: "root-foreign", FrameID: "root-foreign",
		StreamUID: "frame:root-foreign", OwnerUserID: "foreign-owner", RunnerID: "foreign-runner",
		ClaimToken: "foreign-claim", Attempt: 1, SourceEventID: 1, ToolName: "web_fetch",
		ToolCallID: "foreign-large-result", Content: []byte(`{"ok":true,"secret":"must never leak"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	result := runKernelHostCellNamed(t, app, identity, "Operon", `
import host, json, os
current = host.artifacts()
all_owned = host.artifacts(project_id="all")
exact = host.artifacts(filename="analysis.txt", exact=True, content="derived from input")
by_version = host.artifacts(version_id="`+outputVersion.ID+`")
path = host.artifact_path("`+outputVersion.ID+`")
with open(path, "r", encoding="utf-8") as handle:
    materialized = handle.read()
large_path = host.artifact_path("`+largeResult.VersionID+`")
with open(large_path, "r", encoding="utf-8") as handle:
    large_materialized = handle.read()
large_path_by_artifact = host.artifact_path("`+largeResult.ArtifactID+`")
lineage = host.lineage["`+outputVersion.ID+`"]
with open(lineage["inputs"][0]["path"], "r", encoding="utf-8") as handle:
    lineage_input = handle.read()
graph_up = host.lineage.graph("`+outputVersion.ID+`", direction="up")
graph_down = host.lineage.graph("`+inputVersion.ID+`", direction="down")
frames = host.frames(roots_only=False)
detail = host.frames(frame_id="`+identity.access.Frame.ID+`", include_tool_results=False, max_results=10)
frame_search = host.frames(pattern="Collect evidence", roots_only=False)
foreign_frame_denied = False
foreign_artifact_denied = False
foreign_large_result_denied = False
try:
    host.frames(frame_id="root-foreign")
except RuntimeError:
    foreign_frame_denied = True
try:
    host.artifact_path("artifact-foreign")
except RuntimeError:
    foreign_artifact_denied = True
try:
    host.artifact_path("`+foreignLargeResult.VersionID+`")
except RuntimeError:
    foreign_large_result_denied = True
print(json.dumps({
    "current": current, "all_owned": all_owned, "exact": exact,
    "by_version": by_version,
    "path": path, "mode": os.stat(path).st_mode & 0o777,
    "materialized": materialized, "lineage": lineage,
    "large_path": large_path, "large_mode": os.stat(large_path).st_mode & 0o777,
    "large_path_by_artifact": large_path_by_artifact, "large_materialized": large_materialized,
    "lineage_input": lineage_input, "graph_up": graph_up,
    "graph_down": graph_down, "frames": frames, "detail": detail,
    "frame_search": frame_search,
    "foreign_frame_denied": foreign_frame_denied,
    "foreign_artifact_denied": foreign_artifact_denied,
    "foreign_large_result_denied": foreign_large_result_denied,
}, sort_keys=True))
`)
	if result["ok"] != true {
		t.Fatalf("inspection kernel result=%#v", result)
	}
	output := decodeLastKernelJSONLine(t, result["stdout"].(string))
	if output["foreign_frame_denied"] != true || output["foreign_artifact_denied"] != true || output["foreign_large_result_denied"] != true {
		t.Fatalf("foreign isolation=%#v", output)
	}
	current := output["current"].(map[string]any)
	allOwned := output["all_owned"].(map[string]any)
	if current["count"] != float64(2) || allOwned["count"] != float64(3) {
		t.Fatalf("artifact scopes current=%#v all=%#v", current, allOwned)
	}
	if current["scope"] != "single" || current["project_id"] != identity.access.Frame.ProjectID || allOwned["scope"] != "all" {
		t.Fatalf("artifact response scopes current=%#v all=%#v", current, allOwned)
	}
	if output["mode"] != float64(0o400) || output["materialized"] != "derived from input A\n" || output["lineage_input"] != "name,value\nA,1\n" {
		t.Fatalf("materialized artifact=%#v", output)
	}
	if output["large_mode"] != float64(0o400) || output["large_materialized"] != `{"ok":true,"result":{"body":"large immutable evidence"}}` ||
		output["large_path_by_artifact"] != output["large_path"] ||
		!strings.HasPrefix(output["large_path"].(string), filepath.Join(identity.workspaceDir, ".synon-artifacts")) {
		t.Fatalf("materialized large result=%#v", output)
	}
	exact := output["exact"].(map[string]any)
	if exact["count"] != float64(1) || exact["artifacts"].([]any)[0].(map[string]any)["latest_version_id"] != outputVersion.ID {
		t.Fatalf("exact artifact filter=%#v", exact)
	}
	byVersion := output["by_version"].(map[string]any)
	if byVersion["scope"] != "version" || byVersion["count"] != float64(1) || byVersion["artifacts"].([]any)[0].(map[string]any)["checksum"] == nil {
		t.Fatalf("artifact version lookup=%#v", byVersion)
	}
	lineage := output["lineage"].(map[string]any)
	if lineage["artifact_id"] != "artifact-output" || len(lineage["inputs"].([]any)) != 1 || lineage["checksum"] == nil {
		t.Fatalf("lineage=%#v", lineage)
	}
	graphUp := output["graph_up"].(map[string]any)
	graphDown := output["graph_down"].(map[string]any)
	if len(graphUp["nodes"].([]any)) != 2 || len(graphUp["edges"].([]any)) != 1 || len(graphDown["nodes"].([]any)) != 2 || len(graphDown["edges"].([]any)) != 1 {
		t.Fatalf("lineage graphs up=%#v down=%#v", graphUp, graphDown)
	}
	frames := output["frames"].(map[string]any)
	if frames["mode"] != "browse" || frames["scope"] != "single" || frames["count"] != float64(2) {
		t.Fatalf("frame list=%#v", frames)
	}
	detail := output["detail"].(map[string]any)
	messages := detail["messages"].([]any)
	if detail["mode"] != "detail" || detail["total_messages"] != float64(3) || detail["filtered_messages"] != float64(2) || len(messages) != 2 || messages[0].(map[string]any)["role"] != "user" || messages[1].(map[string]any)["role"] != "assistant" {
		t.Fatalf("frame detail=%#v", detail)
	}
	frameSearch := output["frame_search"].(map[string]any)
	if frameSearch["mode"] != "search" || frameSearch["total_matches"] != float64(1) || len(frameSearch["matches"].([]any)[0].(map[string]any)["snippets"].([]any)) == 0 {
		t.Fatalf("frame search=%#v", frameSearch)
	}
	if !strings.HasPrefix(output["path"].(string), filepath.Join(identity.workspaceDir, ".synon-artifacts")) {
		t.Fatalf("materialized path escaped workspace: %q", output["path"])
	}
	bulkReads := runKernelHostCellNamed(t, app, identity, "Operon", `
import host
for _ in range(40):
    host.artifacts(limit=1)
print("bulk-ok")
`)
	if bulkReads["ok"] != true || strings.TrimSpace(bulkReads["stdout"].(string)) != "bulk-ok" {
		t.Fatalf("bulk host reads above legacy 32-call ceiling=%#v", bulkReads)
	}
	materializedPath := output["path"].(string)
	if err := os.Chmod(materializedPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(materializedPath, []byte("tampered same-size!!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered := runKernelHostCellNamed(t, app, identity, "Operon", `
import host, json, os
path = host.artifact_path("`+outputVersion.ID+`")
with open(path, encoding="utf-8") as source:
    content = source.read()
print(json.dumps({"content": content, "mode": os.stat(path).st_mode & 0o777}))
`)
	if recovered["ok"] != true {
		t.Fatalf("managed cache corruption was not recovered=%#v", recovered)
	}
	recoveredData := decodeLastKernelJSONLine(t, recovered["stdout"].(string))
	if recoveredData["content"] != "derived from input A\n" || recoveredData["mode"] != float64(0o400) {
		t.Fatalf("cache repair did not restore the exact read-only source=%#v", recoveredData)
	}
	_, original, found, err := store.GetArtifactVersion(outputVersion.ID)
	if err != nil || !found || string(original.Content) != "derived from input A\n" {
		t.Fatalf("cache repair changed the immutable version: found=%t err=%v", found, err)
	}
	if inputArtifact.ProjectID != identity.access.Frame.ProjectID {
		t.Fatalf("input artifact=%#v", inputArtifact)
	}
}

func TestKernelArtifactMaterializationRecordsAccessWithoutGrantingReadAuthority(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	if err := os.MkdirAll(identity.workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, version := writeKernelInspectionArtifact(
		t, store, identity.access, "artifact-input-read", "input.csv", "text/csv",
		"name,value\nA,1\n", "input-read-receipt",
	)

	unread := runKernelHostCellNamed(t, app, identity, "Operon", `
import host
path = host.artifact_path("`+version.ID+`")
host.artifact_path.__globals__["_host_read_artifact_versions"] = {"`+version.ID+`"}
with open("constant.csv", "w", encoding="utf-8") as handle:
    handle.write("constant\n")
print(path)
`)
	if unread["ok"] != true {
		t.Fatalf("unread materialization result=%#v", unread)
	}
	if receipts := anySliceValue(unread["input_artifacts"]); len(receipts) != 0 {
		t.Fatalf("forged materialization state became read authority: %#v", receipts)
	}
	accesses, ok := unread["input_artifact_accesses"].([]agentKernelInputArtifactReceipt)
	if !ok || len(accesses) != 1 || accesses[0].VersionID != version.ID {
		t.Fatalf("materialized access receipts=%#v", unread["input_artifact_accesses"])
	}

	read := runKernelHostCellNamed(t, app, identity, "Operon", `
import host
path = host.artifact_path("`+version.ID+`")
with open(path, "r", encoding="utf-8") as handle:
    content = handle.read()
print(content)
`)
	if read["ok"] != true || !strings.Contains(stringValue(read["stdout"]), "name,value") {
		t.Fatalf("read materialization result=%#v", read)
	}
	if receipts := anySliceValue(read["input_artifacts"]); len(receipts) != 0 {
		t.Fatalf("ordinary kernel read became trusted scientific input authority: %#v", receipts)
	}
	accesses, ok = read["input_artifact_accesses"].([]agentKernelInputArtifactReceipt)
	if !ok || len(accesses) != 1 || accesses[0].VersionID != version.ID || !isSHA256Hex(accesses[0].Checksum) {
		t.Fatalf("read-path access receipts=%#v", read["input_artifact_accesses"])
	}
}

func writeKernelInspectionArtifact(t *testing.T, store *workspace.Store, access workspace.KernelFrameAccess, artifactID, name, contentType, content, key string) (workspace.Artifact, workspace.ArtifactVersion) {
	t.Helper()
	ctx := workspace.WithMutationIdempotencyKey(context.Background(), key)
	artifact, version, err := store.WriteArtifactVersionRealtime(ctx, workspace.WriteArtifactVersionInput{ArtifactID: artifactID, ProjectID: access.Frame.ProjectID, Name: name, ContentType: contentType, Content: strings.NewReader(content), MaxBytes: 1 << 20, RootFrameID: access.Frame.RootFrameID, FrameID: access.Frame.ID, IsUserUpload: true}, access.UserID)
	if err != nil {
		t.Fatal(err)
	}
	return artifact, version
}

func decodeKernelInspectionJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
