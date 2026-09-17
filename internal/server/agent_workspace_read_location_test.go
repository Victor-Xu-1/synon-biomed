package server

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceVersionReadProvidesTheExactSourceFileForAnalysis(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	raw, err := json.Marshal(map[string]any{"body": strings.Repeat("全文资料\n", 10000) + "SOURCE_TAIL"})
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := fixture.store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "source-for-analysis", ProjectID: fixture.identity.access.Frame.ProjectID,
		Name: "source.json", Kind: "application/json", Content: raw, CreatedBy: fixture.identity.access.UserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	large, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-" + strings.Repeat("c", 32),
		ProjectID:  fixture.stream.ProjectID, RootFrameID: fixture.stream.RootFrameID,
		FrameID: fixture.stream.FrameID, StreamUID: fixture.stream.UID, OwnerUserID: fixture.stream.OwnerID,
		RunnerID: fixture.claim.RunnerID, ClaimToken: fixture.claim.ClaimToken, Attempt: fixture.claim.Attempt,
		SourceEventID: 1, ToolName: "web_fetch", ToolCallID: "source-for-analysis", Content: raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withAgentWorkspaceReadBudget(context.Background(), runnerLargeToolResultInlineLimitBytes)
	for _, selection := range []map[string]any{
		{"version_id": version.ID}, {"version_id": version.ID, "json_pointer": "/body", "limit": 5},
		{"version_id": large.VersionID}, {"version_id": large.ArtifactID},
	} {
		value, err := fixture.server.executeAgentWorkspaceFileTool(ctx, fixture.identity, "read_file", selection)
		if err != nil {
			t.Fatal(err)
		}
		result := mapValue(value)
		path := stringValue(result["file_path"])
		if !filepath.IsAbs(path) || !hostPathWithin(fixture.identity.workspaceDir, path) {
			t.Fatalf("read result has no usable task-local source path: %q", path)
		}
		content, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(content, raw) {
			t.Fatalf("source file is not the complete immutable JSON: %v", err)
		}
		if !agentWorkspaceReadResultFits(ctx, result) || result["file_path_scope"] != "original_source" {
			t.Fatal("source location broke the transport budget or misrepresented a selected view")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0o222 != 0 {
			t.Fatal("immutable source copy is writable")
		}
	}
}

func TestWorkspaceReadLocationIsUsableByTheRealKernel(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	_, version := writeKernelInspectionArtifact(t, store, identity.access, "kernel-source", "source.json", "application/json", `{"body":"complete source 末尾"}`, "kernel-source-write")
	value, err := app.executeAgentWorkspaceFileTool(context.Background(), identity, "read_file", map[string]any{"version_id": version.ID})
	if err != nil {
		t.Fatal(err)
	}
	path := stringValue(mapValue(value)["file_path"])
	encodedPath, err := json.Marshal(path)
	if err != nil {
		t.Fatal(err)
	}
	result := runKernelHostCellNamed(t, app, identity, "Operon", `
import json, os
os.makedirs("analysis-subdir", exist_ok=True)
os.chdir("analysis-subdir")
with open(`+string(encodedPath)+`, encoding="utf-8") as source:
    data = json.load(source)
print(json.dumps({"body": data["body"]}, ensure_ascii=False))
`)
	if result["ok"] != true || decodeLastKernelJSONLine(t, result["stdout"].(string))["body"] != "complete source 末尾" {
		t.Fatalf("real kernel could not consume read_file's source path: %#v", result)
	}
}

func TestWorkspaceVersionReadKeepsEvidenceWhenLocalCacheIsUnavailable(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	_, version, err := fixture.store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "readable-without-cache", ProjectID: fixture.identity.access.Frame.ProjectID,
		Name: "source.txt", Kind: "text/plain", Content: []byte("original source remains readable"), CreatedBy: fixture.identity.access.UserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(fixture.identity.workspaceDir, ".synon-artifacts")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	value, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{"version_id": version.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := mapValue(value)
	if result["content"] != "original source remains readable" || result["file_path"] != nil || result["file_path_error_code"] == nil {
		t.Fatalf("cache failure concealed evidence or invented a path: %#v", result)
	}
	files, err := os.ReadDir(outside)
	if err != nil || len(files) != 0 {
		t.Fatal("source materialization escaped its task workspace")
	}
}
