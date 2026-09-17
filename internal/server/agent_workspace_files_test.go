package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAgentWorkspaceFileToolsMatchCanonicalEditAndReadContract(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID,
		toolSchemas: []agentruntime.ToolSchema{
			agentWorkspaceReadFileToolSchema(), agentWorkspaceEditFileToolSchema(), agentSaveArtifactsToolSchema(),
		},
		hasToolSnapshot: true, suppressHooks: true,
	}

	created := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "reports/result.txt", "old_string": "", "new_string": "alpha\nbeta\n",
	})
	if created["success"] != true || created["created"] != true || created["file_path"] != "reports/result.txt" || created["bytes_written"] != 11 {
		t.Fatalf("create result=%#v", created)
	}
	read := executeAgentWorkspaceToolForTest(t, gateway, "read_file", map[string]any{"file_path": "reports/result.txt"})
	if read["content"] != "alpha\nbeta\n" || read["size_bytes"] != int64(11) || !strings.HasPrefix(stringValue(read["content_type"]), "text/plain") {
		t.Fatalf("read result=%#v", read)
	}

	aliasCall := agentruntime.ToolCall{
		ID: "provider-full-write", Name: "edit_file",
		Arguments: json.RawMessage(`{"path":"validation.json","content":{"errors":[],"input_checks":{"exists":true}},"human_description":"Writing validation record"}`),
	}
	if !gateway.AdmitsToolCall(aliasCall) {
		t.Fatal("unambiguous provider full-write aliases were not admitted")
	}
	aliasResult, err := gateway.Execute(context.Background(), aliasCall)
	if err != nil {
		t.Fatal(err)
	}
	if value := mapValue(aliasResult.Value); value["success"] != true || value["file_path"] != "validation.json" {
		t.Fatalf("alias full-write result=%#v", value)
	}
	validationBytes, err := os.ReadFile(filepath.Join(fixture.projectPath, "validation.json"))
	if err != nil || !json.Valid(validationBytes) || !bytes.Contains(validationBytes, []byte(`"input_checks"`)) {
		t.Fatalf("alias full-write content=%q err=%v", validationBytes, err)
	}

	overwritten := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "reports/result.txt", "old_string": "", "new_string": "gamma\ngamma\n",
	})
	if overwritten["success"] != true || overwritten["created"] != false || overwritten["bytes_written"] != 12 {
		t.Fatalf("overwrite result=%#v", overwritten)
	}
	replaced := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "reports/result.txt", "old_string": "gamma\ngamma\n", "new_string": "delta\ndelta\n",
	})
	if replaced["success"] != true || replaced["bytes_written"] != 12 || replaced["changed"] != true {
		t.Fatalf("replace result=%#v", replaced)
	}
	unchanged := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "reports/result.txt", "old_string": "delta\ndelta\n", "new_string": "delta\ndelta\n",
	})
	if unchanged["success"] != true || unchanged["changed"] != false {
		t.Fatalf("unchanged edit result=%#v", unchanged)
	}

	duplicate := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "reports/result.txt", "old_string": "delta", "new_string": "unsafe",
	})
	if duplicate["ok"] != false || duplicate["executed"] != false ||
		duplicate["status"] != "edit_preflight_required" || duplicate["code"] != "edit_conflict" {
		t.Fatalf("duplicate replacement=%#v", duplicate)
	}
	afterDuplicate, err := os.ReadFile(filepath.Join(fixture.projectPath, "reports", "result.txt"))
	if err != nil || string(afterDuplicate) != "delta\ndelta\n" {
		t.Fatalf("content after rejected duplicate=%q err=%v", afterDuplicate, err)
	}

	invalid := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "reports/result.txt", "old_string": "delta", "new_string": "unsafe", "replace_all": true,
	})
	if invalid["ok"] != false || invalid["code"] != "invalid_tool_arguments" ||
		!strings.Contains(stringValue(invalid["message"]), "JSON Schema") {
		t.Fatalf("invalid input=%#v", invalid)
	}

	window := executeAgentWorkspaceToolForTest(t, gateway, "read_file", map[string]any{
		"file_path": "reports/result.txt", "offset": 2, "limit": 1,
	})
	if window["total_lines"] != 2 || window["showing_lines"] != "2-2" || window["content"] != "2\tdelta\n" {
		t.Fatalf("line window=%#v", window)
	}
}

func TestLargeToolResultReadDefaultsToBoundedIndentedJSONWindow(t *testing.T) {
	versionID := "ltr-5eb3c6c8-4e4b-4737-9a49-8227795afbf5"
	normalized := normalizeAgentWorkspaceReadFileArguments(map[string]any{
		"version_id": versionID, "human_description": "Reading evidence",
	})
	if normalized["offset"] != nil || normalized["limit"] != nil {
		t.Fatalf("normalized=%#v", normalized)
	}
	raw := []byte(`{"ok":true,"result":{"items":[{"title":"one"},{"title":"two"}]}}`)
	read, err := readAgentWorkspaceFile(
		context.Background(), bytes.NewReader(raw), "web_search.json", "application/json",
		int64(len(raw)), map[string]any{"offset": 1, "limit": 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	window := read.(map[string]any)
	if window["showing_lines"] != "1-3" || !strings.Contains(stringValue(window["content"]), "\n") {
		t.Fatalf("window=%#v", window)
	}
}

func TestAgentWorkspaceEditConflictReturnsStructuredRecoveryContract(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": "current.txt", "old_string": "", "new_string": "current durable content",
	}); err != nil {
		t.Fatal(err)
	}
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID,
		toolSchemas: []agentruntime.ToolSchema{agentWorkspaceEditFileToolSchema()}, hasToolSnapshot: true,
		suppressHooks: true,
	}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "stale-edit", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"current.txt","old_string":"stale content","new_string":"replacement","human_description":"Updating current text"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := mapValue(result.Value)
	if payload["ok"] != false || payload["executed"] != false || payload["status"] != "edit_preflight_required" ||
		payload["code"] != "edit_conflict" || payload["retryable"] != true ||
		!strings.Contains(stringValue(payload["recovery"]), "old_string empty") ||
		!strings.Contains(stringValue(payload["recovery"]), "smallest current exact substring") ||
		!strings.Contains(stringValue(payload["recovery"]), "Never copy the whole document") {
		t.Fatalf("edit conflict recovery payload=%#v", payload)
	}
	if !strings.Contains(agentWorkspaceEditFileToolSchema().Description, "Read the target first") ||
		!strings.Contains(agentWorkspaceEditFileToolSchema().Description, "multiple edit_file calls") {
		t.Fatalf("edit_file schema does not instruct the model to refresh targeted-edit authority")
	}
}

func TestAgentWorkspaceFileToolsIsolateProjectsArtifactsAndHostGrants(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": "private.txt", "old_string": "", "new_string": "owner-a",
	}); err != nil {
		t.Fatal(err)
	}

	projectBPath := filepath.Join(t.TempDir(), "project-b")
	if err := os.MkdirAll(projectBPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateProject(workspace.CreateProjectInput{
		ID: "project-b", UserID: "owner-b", Name: "Project B", Path: projectBPath,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-b", ProjectID: "project-b", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	accessB, found, err := fixture.store.GetKernelFrameAccessContext(context.Background(), "frame-b")
	if err != nil || !found {
		t.Fatalf("project B frame found=%t err=%v", found, err)
	}
	identityB := &agentKernelContext{access: accessB, workspaceDir: projectBPath}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), identityB, "read_file", map[string]any{
		"file_path": filepath.Join(fixture.projectPath, "private.txt"),
	}); err == nil || err.Error() != "workspace file path is outside the authorized workspace" {
		t.Fatalf("cross-project absolute read err=%v", err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), identityB, "edit_file", map[string]any{
		"file_path": filepath.Join(fixture.projectPath, "private.txt"), "old_string": "", "new_string": "owner-b",
	}); err == nil || err.Error() != "workspace file path is outside the authorized workspace" {
		t.Fatalf("cross-project absolute edit err=%v", err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), identityB, "edit_file", map[string]any{
		"file_path": "private.txt", "old_string": "", "new_string": "owner-b",
	}); err != nil {
		t.Fatal(err)
	}
	ownerA, _ := os.ReadFile(filepath.Join(fixture.projectPath, "private.txt"))
	ownerB, _ := os.ReadFile(filepath.Join(projectBPath, "private.txt"))
	if string(ownerA) != "owner-a" || string(ownerB) != "owner-b" {
		t.Fatalf("project files A=%q B=%q", ownerA, ownerB)
	}

	_, ownedVersion, err := fixture.store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-owned", ProjectID: fixture.identity.access.Frame.ProjectID,
		Name: "owned.txt", Kind: "text/plain", Content: []byte("owned-version"), CreatedBy: fixture.identity.access.UserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownedRead, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"version_id": ownedVersion.ID,
	})
	if err != nil || stringValue(ownedRead.(map[string]any)["content"]) != "owned-version" {
		t.Fatalf("owned artifact read=%#v err=%v", ownedRead, err)
	}
	largeContent := []byte(`{"ok":true,"result":{"document":"durable oversized tool evidence"}}`)
	largeIdentity := sha256.Sum256([]byte("web_fetch\x00large-read"))
	largeRecord, err := fixture.store.WriteRunnerLargeToolResult(context.Background(), workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-" + hex.EncodeToString(largeIdentity[:16]),
		ProjectID:  fixture.stream.ProjectID, RootFrameID: fixture.stream.RootFrameID,
		FrameID: fixture.stream.FrameID, StreamUID: fixture.stream.UID,
		OwnerUserID: fixture.stream.OwnerID, RunnerID: fixture.claim.RunnerID,
		ClaimToken: fixture.claim.ClaimToken, Attempt: fixture.claim.Attempt,
		SourceEventID: 1, ToolName: "web_fetch", ToolCallID: "large-read", Content: largeContent,
	})
	if err != nil {
		t.Fatal(err)
	}
	largeRead, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"version_id": largeRecord.VersionID,
	})
	largeReadMap, _ := largeRead.(map[string]any)
	var gotLargeJSON, wantLargeJSON map[string]any
	jsonErr := json.Unmarshal([]byte(stringValue(largeReadMap["content"])), &gotLargeJSON)
	wantErr := json.Unmarshal(largeContent, &wantLargeJSON)
	if err != nil || jsonErr != nil || wantErr != nil || !reflect.DeepEqual(gotLargeJSON, wantLargeJSON) ||
		stringValue(largeReadMap["content_type"]) != "application/json" {
		t.Fatalf("owned oversized tool result read=%#v err=%v", largeRead, err)
	}
	largeReadByArtifactID, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"version_id": largeRecord.ArtifactID,
	})
	largeReadByArtifactMap, _ := largeReadByArtifactID.(map[string]any)
	var gotLargeArtifactJSON map[string]any
	jsonErr = json.Unmarshal([]byte(stringValue(largeReadByArtifactMap["content"])), &gotLargeArtifactJSON)
	if err != nil || jsonErr != nil || !reflect.DeepEqual(gotLargeArtifactJSON, wantLargeJSON) ||
		stringValue(largeReadByArtifactMap["content_type"]) != "application/json" {
		t.Fatalf("owned oversized tool result artifact handle read=%#v err=%v", largeReadByArtifactID, err)
	}
	readSchema := agentWorkspaceReadFileToolSchema()
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID,
		toolSchemas:     []agentruntime.ToolSchema{readSchema},
		toolValidators:  agentRuntimeToolValidators([]agentruntime.ToolSchema{readSchema}),
		hasToolSnapshot: true, suppressHooks: true,
	}
	largeReadFromMisplacedVersion := executeAgentWorkspaceToolForTest(t, gateway, "read_file", map[string]any{
		"file_path": largeRecord.VersionID, "limit": 100,
	})
	if !strings.Contains(stringValue(largeReadFromMisplacedVersion["content"]), "durable oversized tool evidence") ||
		largeReadFromMisplacedVersion["content_type"] != "application/json" {
		t.Fatalf("misplaced oversized result id was not normalized=%#v", largeReadFromMisplacedVersion)
	}
	if !strings.Contains(readSchema.Description, "pass it as version_id") {
		t.Fatalf("read_file schema does not distinguish durable version ids from paths: %s", readSchema.Description)
	}
	_, foreignVersion, err := fixture.store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-foreign", ProjectID: "project-b",
		Name: "foreign.txt", Kind: "text/plain", Content: []byte("foreign-version"), CreatedBy: "owner-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"version_id": foreignVersion.ID,
	}); err == nil || err.Error() != "read_file artifact version is unavailable" {
		t.Fatalf("foreign artifact read err=%v", err)
	}

	externalRW := t.TempDir()
	if _, err := fixture.server.upsertHostGrant(fixture.identity.access.UserID, externalRW, "read_write"); err != nil {
		t.Fatal(err)
	}
	externalPath := filepath.Join(externalRW, "granted.txt")
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": externalPath, "old_string": "", "new_string": "granted",
	}); err != nil {
		t.Fatal(err)
	}
	externalRead, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"file_path": externalPath,
	})
	if err != nil || stringValue(externalRead.(map[string]any)["content"]) != "granted" {
		t.Fatalf("external read=%#v err=%v", externalRead, err)
	}

	externalRO := t.TempDir()
	if _, err := fixture.server.upsertHostGrant(fixture.identity.access.UserID, externalRO, "read"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": filepath.Join(externalRO, "denied.txt"), "old_string": "", "new_string": "denied",
	}); err == nil || err.Error() != "workspace file path is outside the authorized workspace" {
		t.Fatalf("read-only host grant edit err=%v", err)
	}
}

func TestAgentWorkspaceEditFileSerializesSamePath(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": "concurrent.txt", "old_string": "", "new_string": "seed",
	}); err != nil {
		t.Fatal(err)
	}
	const workers = 32
	start := make(chan struct{})
	results := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(value string) {
			defer wait.Done()
			<-start
			_, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
				"file_path": "concurrent.txt", "old_string": "seed", "new_string": value,
			})
			results <- err
		}(string(rune('A' + index)))
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		if err.Error() != "edit_file old_string was not found" {
			t.Fatalf("unexpected concurrent edit err=%v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent edit successes=%d, want 1", succeeded)
	}
}

func TestAgentWorkspaceEditFileNormalizesUniqueTripleQuotedOldString(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": "report.md", "old_string": "", "new_string": "# Report\n\nOriginal conclusion.\n", "human_description": "Creating report",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": "report.md", "old_string": `"""Original conclusion."""`, "new_string": "Verified conclusion.", "human_description": "Updating conclusion",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"file_path": "report.md", "human_description": "Reading report",
	})
	if err != nil {
		t.Fatal(err)
	}
	if content := stringValue(result.(map[string]any)["content"]); content != "# Report\n\nVerified conclusion.\n" {
		t.Fatalf("content=%q", content)
	}
}

func TestAgentWorkspaceEditFilePreservesStrictTripleQuoteSemantics(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": "source.py", "old_string": "", "new_string": `value = """literal"""\nliteral\nliteral\n`, "human_description": "Creating source",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": "source.py", "old_string": `"""literal"""`, "new_string": `"""updated"""`, "human_description": "Updating literal",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": "source.py", "old_string": `"""literal"""`, "new_string": "ambiguous", "human_description": "Updating repeated text",
	}); err == nil || err.Error() != "edit_file old_string was not found" {
		t.Fatalf("ambiguous fallback err=%v", err)
	}
}

func TestAgentWorkspaceSecureEditAuthorityAnchorsTheOpenedParent(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("secure handle implementation is available on Linux and Windows")
	}
	root := t.TempDir()
	originalParent := filepath.Join(root, "reports")
	if err := os.MkdirAll(originalParent, 0o700); err != nil {
		t.Fatal(err)
	}
	authority, err := openAgentWorkspaceEditAuthority(root, filepath.Join("reports", "result.txt"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.close()
	staging, err := authority.createStaging(0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer staging.discard(authority)
	if _, err := staging.file.Write([]byte("anchored")); err != nil {
		t.Fatal(err)
	}
	movedParent := filepath.Join(root, "reports-original")
	if err := os.Rename(originalParent, movedParent); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, originalParent); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := authority.commit(staging); err != nil {
		t.Fatal(err)
	}
	inside, err := os.ReadFile(filepath.Join(movedParent, "result.txt"))
	if err != nil || string(inside) != "anchored" {
		t.Fatalf("anchored content=%q err=%v", inside, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside path must remain untouched: %v", err)
	}
}

func TestAgentWorkspaceEditRejectsWorkspaceProtectedPaths(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	protected := []string{
		".gitconfig", ".gitmodules", ".bashrc", ".zshenv", ".ripgreprc", ".mcp.json",
		filepath.Join(".ssh", "config"), filepath.Join(".aws", "credentials"), filepath.Join(".gcloud", "configurations", "x"),
		filepath.Join(".git", "config"), filepath.Join(".git", "hooks", "pre-commit"), filepath.Join(".git", "modules", "submodule"),
		filepath.Join(".synon", "runtime", "skills", "reviewed-skill", "scripts", "run.py"),
		filepath.Join(".vscode", "settings.json"), filepath.Join(".idea", "workspace.xml"),
	}
	for _, path := range protected {
		t.Run(filepath.ToSlash(path), func(t *testing.T) {
			_, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
				"file_path": path, "old_string": "", "new_string": "blocked",
			})
			if err == nil || err.Error() != "edit_file cannot modify protected configuration" {
				t.Fatalf("protected path %q err=%v", path, err)
			}
		})
	}
	for _, path := range []string{
		".gitignore", "foo.gitconfig", "notes.bashrc", filepath.Join("notes", ".ssh-key-review.txt"),
		filepath.Join("applications", "api", "main.go"), filepath.Join("src", "Library", "Preferences", "schema.json"),
		filepath.Join(".config", "systemd", "user", "repo.service"), filepath.Join("src", "config.json"),
	} {
		if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
			"file_path": path, "old_string": "", "new_string": "allowed",
		}); err != nil {
			t.Fatalf("allowed path %q err=%v", path, err)
		}
	}
	protectedGrant := filepath.Join(t.TempDir(), ".ssh")
	if err := os.MkdirAll(protectedGrant, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.upsertHostGrant(fixture.identity.access.UserID, protectedGrant, "read_write"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "edit_file", map[string]any{
		"file_path": filepath.Join(protectedGrant, "config"), "old_string": "", "new_string": "blocked",
	}); err == nil || err.Error() != "edit_file cannot modify protected configuration" {
		t.Fatalf("protected grant root err=%v", err)
	}
}

func TestProtectedAgentWorkspaceEditPathUsesHomeAndSystemAnchors(t *testing.T) {
	home := t.TempDir()
	tests := []struct {
		name      string
		path      string
		goos      string
		protected bool
	}{
		{name: "mac home applications", path: filepath.Join(home, "Applications", "Bio.app", "config"), goos: "darwin", protected: true},
		{name: "mac home preferences", path: filepath.Join(home, "Library", "Preferences", "bio.plist"), goos: "darwin", protected: true},
		{name: "mac system launch agent", path: filepath.Join(string(filepath.Separator), "Library", "LaunchAgents", "bio.plist"), goos: "darwin", protected: true},
		{name: "linux home systemd", path: filepath.Join(home, ".config", "systemd", "user", "bio.service"), goos: "linux", protected: true},
		{name: "linux home autostart", path: filepath.Join(home, ".config", "autostart", "bio.desktop"), goos: "linux", protected: true},
		{name: "repo applications", path: filepath.Join(home, "repo", "applications", "api", "main.go"), goos: "linux", protected: false},
		{name: "repo library preferences", path: filepath.Join(home, "repo", "Library", "Preferences", "schema.json"), goos: "darwin", protected: false},
		{name: "repo linux config", path: filepath.Join(home, "repo", ".config", "systemd", "user", "repo.service"), goos: "linux", protected: false},
		{name: "suffix is not basename", path: filepath.Join(home, "repo", "foo.gitconfig"), goos: "linux", protected: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := protectedAgentWorkspaceEditPathWithContext(testCase.path, testCase.goos, home); got != testCase.protected {
				t.Fatalf("protected=%v, want %v for %q", got, testCase.protected, testCase.path)
			}
		})
	}
	canonicalHome := t.TempDir()
	homeAlias := filepath.Join(t.TempDir(), "home-alias")
	if err := os.Symlink(canonicalHome, homeAlias); err == nil {
		canonicalTarget := filepath.Join(canonicalHome, ".config", "systemd", "user", "bio.service")
		if !protectedAgentWorkspaceEditPathWithContext(canonicalTarget, "linux", homeAlias) {
			t.Fatalf("canonical target escaped a symlinked home boundary: %q", canonicalTarget)
		}
	}
	missingHome := filepath.Join(t.TempDir(), "missing-home")
	if !protectedAgentWorkspaceEditPathWithContext(filepath.Join(t.TempDir(), "ordinary.txt"), "linux", missingHome) {
		t.Fatal("unavailable home boundary did not fail closed")
	}
}

func TestAgentWorkspaceReadRejectsHardLinkedTargets(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	outside := filepath.Join(t.TempDir(), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(fixture.projectPath, "linked-secret.txt")
	if err := os.Link(outside, linked); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	_, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"file_path": "linked-secret.txt",
	})
	if err == nil || err.Error() != "read_file could not open the requested file" || strings.Contains(err.Error(), "outside-secret") {
		t.Fatalf("read hardlink err=%v", err)
	}
}

func TestAgentWorkspaceReadUsesUnboundedLineLimitWithCancellation(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	var content strings.Builder
	for index := 0; index < 2501; index++ {
		fmt.Fprintf(&content, "v%d\n", index)
	}
	if err := os.WriteFile(filepath.Join(fixture.projectPath, "long.txt"), []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"file_path": "long.txt", "offset": 1, "limit": 2501,
	})
	if err != nil {
		t.Fatal(err)
	}
	window := result.(map[string]any)
	if window["total_lines"] != 2501 || window["showing_lines"] != "1-2501" {
		t.Fatalf("large window=%#v", window)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.server.executeAgentWorkspaceFileTool(cancelled, fixture.identity, "read_file", map[string]any{
		"file_path": "long.txt", "offset": 1, "limit": 2501,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read err=%v", err)
	}
}

func TestAgentWorkspaceEditPersistsReplaySafeExecutionProvenance(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	callID := "edit-provenance"
	run := &sessionRunnerChatRun{
		Transcript:         &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
		ToolSourceEventIDs: map[string]int64{callID: 1},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID,
		toolSchemas: []agentruntime.ToolSchema{agentWorkspaceEditFileToolSchema()}, hasToolSnapshot: true, suppressHooks: true,
	}
	arguments := json.RawMessage(`{"file_path":"provenance.txt","old_string":"","new_string":"verified","human_description":"Writing provenance text"}`)
	first, err := gateway.Execute(ctx, agentruntime.ToolCall{ID: callID, Name: "edit_file", Arguments: arguments})
	if err != nil || first.Value.(map[string]any)["success"] != true {
		t.Fatalf("first edit=%#v err=%v", first.Value, err)
	}
	trackedContext := fixture.server.withTranscriptArtifactToolSource(ctx, callID)
	executionID, ok := agentWorkspaceEditExecutionID(trackedContext, fixture.identity.access)
	if !ok {
		t.Fatal("execution identity was not derived")
	}
	record, found, err := fixture.store.GetExecutionLog(fixture.identity.access.Frame.ID, executionID)
	if err != nil || !found {
		t.Fatalf("execution record found=%t err=%v", found, err)
	}
	wantDigest := sha256.Sum256([]byte("verified"))
	gotDigest, ok := agentWorkspaceExecutionFileDigest(record.FilesWritten, "provenance.txt")
	if !ok || gotDigest != hex.EncodeToString(wantDigest[:]) || record.KernelKind != "host_tool" || record.Language != "diff" {
		t.Fatalf("execution record=%#v", record)
	}
	replayed, err := gateway.Execute(ctx, agentruntime.ToolCall{ID: callID, Name: "edit_file", Arguments: arguments})
	if err != nil || replayed.Value.(map[string]any)["success"] != true {
		t.Fatalf("replayed edit=%#v err=%v", replayed.Value, err)
	}
	saveInput := map[string]any{
		"files": []any{"provenance.txt"}, "language": "diff", "environment": "workspace",
		"human_description": "Saving the edited report",
	}
	saved, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-edited-file", saveInput), fixture.identity, "save-edited-file", saveInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, saved)
	if len(artifacts) != 1 {
		t.Fatalf("saved artifacts=%#v", saved)
	}
	var links int
	if err := fixture.db.QueryRow(`
		SELECT COUNT(*) FROM artifact_version_execution_links
		WHERE version_id=? AND execution_log_id=?`, stringValue(artifacts[0]["version_id"]), executionID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 1 {
		t.Fatalf("artifact provenance links=%d want=1", links)
	}
}

func TestAgentWorkspaceEditRetryRepairsDurableProvenanceAfterDatabaseFailure(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	callID := "edit-provenance-recovery"
	run := &sessionRunnerChatRun{
		Transcript:         &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
		ToolSourceEventIDs: map[string]int64{callID: 1},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID,
		toolSchemas: []agentruntime.ToolSchema{agentWorkspaceEditFileToolSchema()}, hasToolSnapshot: true, suppressHooks: true,
	}
	arguments := json.RawMessage(`{"file_path":"recoverable.txt","old_string":"","new_string":"durable","human_description":"Writing recoverable text"}`)
	if err := fixture.store.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			CREATE TRIGGER fail_agent_file_edit_execution
			BEFORE INSERT ON execution_log WHEN NEW.id LIKE 'edit-file-%'
			BEGIN SELECT RAISE(ABORT, 'forced execution log failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := gateway.Execute(ctx, agentruntime.ToolCall{ID: callID, Name: "edit_file", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	failedValue, ok := failed.Value.(map[string]any)
	if !ok || failedValue["ok"] != false || failedValue["error"] != "edit_file result is durable but provenance remains pending; retry the same call" {
		t.Fatalf("first edit=%#v err=%v", failed.Value, err)
	}
	content, err := os.ReadFile(filepath.Join(fixture.projectPath, "recoverable.txt"))
	if err != nil || string(content) != "durable" {
		t.Fatalf("durable content=%q err=%v", content, err)
	}
	trackedContext := fixture.server.withTranscriptArtifactToolSource(ctx, callID)
	executionID, ok := agentWorkspaceEditExecutionID(trackedContext, fixture.identity.access)
	if !ok {
		t.Fatal("execution identity was not derived")
	}
	target, err := fixture.server.resolveAgentWorkspaceFileTarget(
		fixture.identity, fixture.identity.access.UserID, "recoverable.txt", true,
	)
	if err != nil {
		t.Fatal(err)
	}
	requestFingerprint := agentWorkspaceEditRequestFingerprint(fixture.identity.access, target, "", "durable")
	receipt, found, err := fixture.store.GetAgentFileEditReceipt(
		context.Background(), fixture.identity.access.UserID, executionID, requestFingerprint,
	)
	if err != nil || !found || receipt.State != "prepared" {
		t.Fatalf("prepared receipt=%#v found=%t err=%v", receipt, found, err)
	}
	if err := fixture.store.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DROP TRIGGER fail_agent_file_edit_execution`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	replayed, err := gateway.Execute(ctx, agentruntime.ToolCall{ID: callID, Name: "edit_file", Arguments: arguments})
	if err != nil || replayed.Value.(map[string]any)["success"] != true {
		t.Fatalf("recovery edit=%#v err=%v", replayed.Value, err)
	}
	receipt, found, err = fixture.store.GetAgentFileEditReceipt(
		context.Background(), fixture.identity.access.UserID, executionID, requestFingerprint,
	)
	if err != nil || !found || receipt.State != "completed" {
		t.Fatalf("completed receipt=%#v found=%t err=%v", receipt, found, err)
	}
	var records int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM execution_log WHERE id=?`, executionID).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 1 {
		t.Fatalf("execution records=%d want=1", records)
	}
}

func TestAgentWorkspaceEditProvenanceRetriesOnlyTransientContention(t *testing.T) {
	attempts := 0
	want := workspace.AgentFileEditReceipt{ExecutionID: "edit-file-retry", FrameID: "frame-retry"}
	got, created, err := retryAgentWorkspaceEditReceiptPreparation(context.Background(), func() (workspace.AgentFileEditReceipt, bool, error) {
		attempts++
		if attempts < 3 {
			return workspace.AgentFileEditReceipt{}, false, errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return want, true, nil
	})
	if err != nil || !created || got.ExecutionID != want.ExecutionID || attempts != 3 {
		t.Fatalf("receipt=%#v created=%t attempts=%d err=%v", got, created, attempts, err)
	}

	attempts = 0
	permanent := errors.New("receipt is invalid")
	_, _, err = retryAgentWorkspaceEditReceiptPreparation(context.Background(), func() (workspace.AgentFileEditReceipt, bool, error) {
		attempts++
		return workspace.AgentFileEditReceipt{}, false, permanent
	})
	if !errors.Is(err, permanent) || attempts != 1 {
		t.Fatalf("permanent attempts=%d err=%v", attempts, err)
	}
}

func TestAgentWorkspaceReadReturnsRealModelVisibleImageAndPDFParts(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	imagePath := filepath.Join(fixture.projectPath, "figure.png")
	if err := os.WriteFile(imagePath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(fixture.projectPath, "paper.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\n%%EOF"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		kind agentruntime.ContentPartType
	}{
		{path: "figure.png", kind: agentruntime.ContentPartImage},
		{path: "paper.pdf", kind: agentruntime.ContentPartDocument},
	} {
		result, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{"file_path": test.path})
		if err != nil {
			t.Fatal(err)
		}
		rich, ok := result.(agentRuntimeRichToolResponse)
		if !ok || len(rich.parts) != 1 || rich.parts[0].Type != test.kind {
			t.Fatalf("visual result for %s=%#v", test.path, result)
		}
		if _, err := agentruntime.NormalizeModelRequestMedia(agentruntime.ModelRequest{
			Messages: []agentruntime.Message{{Role: "user", Parts: rich.parts}},
		}); err != nil {
			t.Fatalf("normalize %s: %v", test.path, err)
		}
	}
}

func TestAgentWorkspaceReadRendersSelectedPDFPagesIntoModelVisibleImages(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm is not installed")
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is not installed")
	}
	fixture := newAgentSaveArtifactsFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.projectPath, "paper.pdf"), minimalAgentWorkspacePDF(), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"file_path": "paper.pdf", "pages": []any{float64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rich, ok := result.(agentRuntimeRichToolResponse)
	if !ok || len(rich.parts) != 1 || rich.parts[0].Type != agentruntime.ContentPartImage || rich.parts[0].Media == nil {
		t.Fatalf("rendered result=%#v", result)
	}
	value := rich.value.(map[string]any)
	if !strings.Contains(stringValue(value["content"]), "Synon") ||
		mapValue(value["text_extraction"])["status"] != "parsed" ||
		mapValue(value["reader_contract"])["status"] != "parsed_and_attached" {
		t.Fatalf("PDF dual-channel result=%#v", value)
	}
	if _, err := agentruntime.NormalizeModelRequestMedia(agentruntime.ModelRequest{
		Messages: []agentruntime.Message{{Role: "user", Parts: rich.parts}},
	}); err != nil {
		t.Fatalf("normalize rendered page: %v", err)
	}
}

func minimalAgentWorkspacePDF() []byte {
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		"<< /Length 38 >>\nstream\nBT /F1 12 Tf 20 100 Td (Synon) Tj ET\nendstream",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return output.Bytes()
}

func TestAgentRuntimeFrameModelToolSchemasHideGlobalFileAndArtifactAuthorities(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "Read"}, {Name: "file_read"}, {Name: "Write"}, {Name: "file_write"},
		{Name: "artifact_register"}, {Name: "artifact_list"}, {Name: "artifact_get"}, {Name: "session_export"},
		agentWorkspaceReadFileToolSchema(), agentWorkspaceEditFileToolSchema(), agentSaveArtifactsToolSchema(),
		{Name: "python"},
	}
	filtered := preferCanonicalFrameFileToolSchemas(schemas)
	names := agentRuntimeToolSchemaNameSet(filtered)
	for _, required := range []string{"read_file", "edit_file", "save_artifacts", "python"} {
		if _, found := names[required]; !found {
			t.Fatalf("canonical schema %q missing from %#v", required, names)
		}
	}
	for _, forbidden := range []string{"Read", "file_read", "Write", "file_write", "artifact_register", "artifact_list", "artifact_get", "session_export"} {
		if _, found := names[forbidden]; found {
			t.Fatalf("legacy schema %q remains in %#v", forbidden, names)
		}
	}
	if !agentRuntimeToolNeedsApproval("edit_file") {
		t.Fatal("edit_file must pass through runtime approval policy")
	}
	legacyOnly := preferCanonicalFrameFileToolSchemas([]agentruntime.ToolSchema{{Name: "Read"}, {Name: "Write"}, {Name: "session_export"}, {Name: "python"}})
	if names := agentRuntimeToolSchemaNameSet(legacyOnly); len(names) != 1 {
		t.Fatalf("legacy-only fallback remained visible: %#v", names)
	} else if _, found := names["python"]; !found {
		t.Fatalf("canonical non-file tool missing from legacy filter: %#v", names)
	}
}

func TestAgentKernelDefaultWorkspaceIsStableProjectScopedAndOutsideProtectedRoot(t *testing.T) {
	fileRoot := filepath.Join(t.TempDir(), "runtime-data")
	if err := os.MkdirAll(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	app := &Server{fileRoot: fileRoot}
	projectA, err := app.defaultAgentKernelWorkspace("project-a")
	if err != nil {
		t.Fatal(err)
	}
	projectAAgain, err := app.defaultAgentKernelWorkspace("project-a")
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := app.defaultAgentKernelWorkspace("project-b")
	if err != nil {
		t.Fatal(err)
	}
	if projectA == "" || projectA != projectAAgain || projectA == projectB {
		t.Fatalf("workspace A=%q again=%q B=%q", projectA, projectAAgain, projectB)
	}
	projectWhitespace, err := app.defaultAgentKernelWorkspace(" project-a")
	if err != nil {
		t.Fatal(err)
	}
	if projectWhitespace == projectA {
		t.Fatalf("distinct persisted project identities collided: %q", projectA)
	}
	alias := fileRoot + "-alias"
	if err := os.Symlink(fileRoot, alias); err == nil {
		aliasWorkspace, aliasErr := (&Server{fileRoot: alias}).defaultAgentKernelWorkspace("project-a")
		if aliasErr != nil || aliasWorkspace != projectA {
			t.Fatalf("canonical file-root alias workspace=%q err=%v want=%q", aliasWorkspace, aliasErr, projectA)
		}
	}
	canonicalRoot, err := canonicalHostDirectory(fileRoot)
	if err != nil {
		t.Fatal(err)
	}
	absoluteWorkspace, err := filepath.Abs(projectA)
	if err != nil {
		t.Fatal(err)
	}
	if hostPathWithin(canonicalRoot, absoluteWorkspace) || hostPathWithin(absoluteWorkspace, canonicalRoot) {
		t.Fatalf("default workspace %q overlaps protected root %q", absoluteWorkspace, canonicalRoot)
	}
}

func TestAgentKernelDefaultTaskWorkspaceIsStableRootIncarnationScoped(t *testing.T) {
	fileRoot := filepath.Join(t.TempDir(), "runtime-data")
	if err := os.MkdirAll(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	app := &Server{fileRoot: fileRoot}
	projectWorkspace, err := app.defaultAgentKernelWorkspace("project-a")
	if err != nil {
		t.Fatal(err)
	}
	taskA, err := app.defaultAgentKernelTaskWorkspace("project-a", "root-a", "incarnation-a")
	if err != nil {
		t.Fatal(err)
	}
	taskAAgain, err := app.defaultAgentKernelTaskWorkspace("project-a", "root-a", "incarnation-a")
	if err != nil {
		t.Fatal(err)
	}
	taskB, err := app.defaultAgentKernelTaskWorkspace("project-a", "root-b", "incarnation-b")
	if err != nil {
		t.Fatal(err)
	}
	newIncarnation, err := app.defaultAgentKernelTaskWorkspace("project-a", "root-a", "incarnation-new")
	if err != nil {
		t.Fatal(err)
	}
	if taskA == "" || taskA != taskAAgain || taskA == taskB || taskA == newIncarnation {
		t.Fatalf("task A=%q again=%q task B=%q new incarnation=%q", taskA, taskAAgain, taskB, newIncarnation)
	}
	if !hostPathWithin(projectWorkspace, taskA) || filepath.Dir(filepath.Dir(taskA)) != projectWorkspace {
		t.Fatalf("task workspace %q is not isolated beneath project workspace %q", taskA, projectWorkspace)
	}
	if _, err := os.Stat(taskA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only task workspace resolution created a directory: %v", err)
	}
	if _, err := app.defaultAgentKernelTaskWorkspace("project-a", "root-a", ""); err == nil {
		t.Fatal("missing root incarnation unexpectedly produced a task workspace")
	}
}

func TestCanonicalAgentWorkspaceRootIsReadOnlyUntilMutationExecution(t *testing.T) {
	fileRoot := t.TempDir()
	workspacePath := filepath.Join(filepath.Dir(fileRoot), filepath.Base(fileRoot)+"-workspaces", "project-workspace")
	app := &Server{fileRoot: fileRoot}
	identity := &agentKernelContext{workspaceDir: workspacePath}
	target, err := app.canonicalAgentWorkspaceRoot(identity)
	if err != nil {
		t.Fatal(err)
	}
	if target != workspacePath {
		t.Fatalf("workspace target=%q want=%q", target, workspacePath)
	}
	if _, err := os.Stat(workspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only authority resolution created workspace: %v", err)
	}
	ensured, err := app.ensureAgentWorkspaceRoot(identity)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ensured)
	if err != nil || !info.IsDir() {
		t.Fatalf("ensured workspace info=%#v err=%v", info, err)
	}
}

func executeAgentWorkspaceToolForTest(
	t *testing.T,
	gateway serverAgentRuntimeToolGateway,
	name string,
	input map[string]any,
) map[string]any {
	t.Helper()
	if _, found := input["human_description"]; !found && (name == "read_file" || name == "edit_file") {
		input = copyMapAny(input)
		if name == "read_file" {
			input["human_description"] = "Reading workspace evidence"
		} else {
			input["human_description"] = "Editing workspace file"
		}
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{ID: "call-" + name, Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("tool %s result=%#v", name, result.Value)
	}
	return value
}

func TestReadAgentWorkspaceTextWindowTruncatesOversizedLines(t *testing.T) {
	oversized := strings.Repeat("a", agentWorkspaceReadMaxBytes+16<<10) + "\nsecond line\n"
	result, err := readAgentWorkspaceTextWindow(
		context.Background(), strings.NewReader(oversized), "tool-result.json", "application/json",
		int64(len(oversized)), 1, 100,
	)
	if err != nil {
		t.Fatalf("readAgentWorkspaceTextWindow error = %v", err)
	}
	if numberValue(result["total_lines"]) != 2 {
		t.Fatalf("total_lines = %#v, want 2", result["total_lines"])
	}
	if numberValue(result["truncated_lines"]) != 1 {
		t.Fatalf("truncated_lines = %#v, want 1", result["truncated_lines"])
	}
	content := stringValue(result["content"])
	if !strings.Contains(content, "content truncated") || !strings.Contains(content, "second line") {
		t.Fatalf("window content = %q, want bounded preview plus following lines", content[:200])
	}
	if strings.Contains(content, strings.Repeat("a", agentWorkspaceReadMaxBytes+1)) {
		t.Fatal("window content must not carry the full oversized line")
	}
}

func TestReadAgentWorkspaceFileAutoBoundsLargeText(t *testing.T) {
	var source strings.Builder
	for index := 0; index < 5000; index++ {
		fmt.Fprintf(&source, "<p>scientific evidence row %04d with bounded content</p>\n", index)
	}
	payload := []byte(source.String())
	resultValue, err := readAgentWorkspaceFile(
		context.Background(), bytes.NewReader(payload), "report.html", "text/html",
		int64(len(payload)), map[string]any{},
	)
	if err != nil {
		t.Fatalf("readAgentWorkspaceFile error = %v", err)
	}
	result := resultValue.(map[string]any)
	if result["_policy_skip"] == true || result["truncated"] != true {
		t.Fatalf("large text did not return a successful bounded preview: %#v", result)
	}
	if numberValue(result["next_offset"]) <= 1 || len(stringValue(result["content"])) > agentWorkspaceReadMaxBytes {
		t.Fatalf("large text continuation metadata is invalid: %#v", result)
	}
}

func TestReadAgentWorkspaceTextWindowAutoBoundsOversizedRequest(t *testing.T) {
	var source strings.Builder
	for index := 0; index < 4000; index++ {
		fmt.Fprintf(&source, "%04d %s\n", index, strings.Repeat("evidence", 8))
	}
	result, err := readAgentWorkspaceTextWindow(
		context.Background(), strings.NewReader(source.String()), "report.html", "text/html",
		int64(source.Len()), 1, 4000,
	)
	if err != nil {
		t.Fatalf("readAgentWorkspaceTextWindow error = %v", err)
	}
	if result["_policy_skip"] == true || result["truncated"] != true || numberValue(result["next_offset"]) <= 1 {
		t.Fatalf("oversized line request did not return a continuation: %#v", result)
	}
	if len(stringValue(result["content"])) > agentWorkspaceReadMaxBytes {
		t.Fatalf("bounded content bytes=%d", len(stringValue(result["content"])))
	}
}

func TestAgentWorkspaceReadMissingPathHasDistinctRecoverableError(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	_, err := fixture.server.executeAgentWorkspaceFileTool(context.Background(), fixture.identity, "read_file", map[string]any{
		"file_path": "handoff/label/info.txt",
	})
	if err == nil || err.Error() != "read_file file does not exist in task workspace: handoff/label/info.txt" {
		t.Fatalf("missing workspace file error=%v", err)
	}

}
