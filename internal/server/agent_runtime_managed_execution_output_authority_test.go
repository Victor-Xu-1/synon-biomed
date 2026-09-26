package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/sciencecapability"
)

func TestManagedExecutionReceiptMountPreventsPythonOutputMutation(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(
		t, filepath.Join(t.TempDir(), "workspace.db"), true,
	)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	output := filepath.Join(identity.workspaceDir, "results")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{
		managedExecutionOutputOwnershipMarker: `{"schema":"synon.execution-pack-output-owner.v1","execution_pack_id":"capability.engine"}`,
		"report.md":                           "validated report\n",
		"validation.json":                     `{"overall_pass":true}`,
	}
	writes := make([]kernelruntime.FileWrite, 0, len(contents))
	for name, content := range contents {
		path := filepath.Join(output, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(content))
		writes = append(writes, kernelruntime.FileWrite{
			Path: filepath.ToSlash(filepath.Join("results", name)), SHA256: hex.EncodeToString(digest[:]),
		})
	}
	app.scienceCapabilities = &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "capability.engine", Mode: "local", Skill: "managed-workflow",
				Executable: "python", Script: "executionpacks/run.py",
				Outputs: []sciencecapability.ExecutionOutput{
					{Path: "out/report.md"}, {Path: "out/validation.json"},
				},
			},
		}},
	}}}
	access := identity.access
	kernelID, err := kernelruntime.StableSessionID(kernelruntime.SessionSpec{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, DelegateName: access.DelegateName,
		KernelKind: "bash", Language: "python", Environment: "python", WorkspaceDir: identity.workspaceDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(identity.workspaceDir, ".synon", "runtime", "skills", "managed-workflow-abcd", "scripts", "run.py")
	seed, err := app.executeAgentKernelTool(
		context.Background(), identity, "python", map[string]any{
			"code":        "persisted_namespace_value = 'kept-after-publication'",
			"environment": "python",
		},
	)
	if err != nil || seed["ok"] != true {
		t.Fatalf("seed persistent namespace result=%#v err=%v", seed, err)
	}
	if _, err := store.SaveExecutionLog(workspace.SaveExecutionLogInput{
		Record: workspace.ExecutionLogRecord{
			ID: "managed-pack-execution", FrameID: access.Frame.ID, KernelID: kernelID,
			KernelKind: "bash", CondaEnv: "python", Language: "python",
			Source: `python "` + script + `"`, ExitStatus: "ok", Origin: "agent", FilesWritten: writes,
		},
		ExpectedOwnerID: access.UserID, ExpectedProjectID: access.Frame.ProjectID,
		ExpectedFrameIncarnationID:     access.Frame.IncarnationID,
		ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
	}); err != nil {
		t.Fatal(err)
	}
	gateway := serverAgentRuntimeToolGateway{server: app, kernel: identity}
	blocked := gateway.agentRuntimeManagedExecutionOutputMutationPreflight(
		context.Background(), "edit_file", map[string]any{"file_path": "results/report.md"},
	)
	if blocked == nil || blocked["status"] != "managed_execution_output_immutable" ||
		blocked["execution_id"] != "managed-pack-execution" {
		t.Fatalf("host file mutation preflight=%#v", blocked)
	}
	for name, command := range map[string]string{
		"second command": `python "` + script + `"` + "\nprintf altered > results/report.md",
		"redirection":    `python "` + script + `" > results/report.md`,
		"attached interpreter code": `python "-cimport pathlib;pathlib.Path('results/report.md').write_text('tampered')" "` +
			script + `"`,
	} {
		t.Run(name+" keeps old output immutable", func(t *testing.T) {
			excludePackID := ""
			if engine, found := app.canonicalManagedExecutionPack("bash", map[string]any{"command": command}); found {
				excludePackID = engine.ExecutionPack.ID
				t.Fatalf("decorated command received canonical pack authority: %q", command)
			}
			mounts, err := app.agentKernelWorkspaceImmutableMounts(
				context.Background(), access, identity.workspaceDir, excludePackID,
			)
			if err != nil {
				t.Fatal(err)
			}
			mountedStableRoot := false
			for _, mount := range mounts {
				if filepath.Clean(mount.Path) == filepath.Join(identity.workspaceDir, ".synon-artifacts") && !mount.Writable {
					mountedStableRoot = true
					break
				}
			}
			if !mountedStableRoot {
				t.Fatalf("stable artifact root lost its immutable mount: %#v", mounts)
			}
			if info, err := os.Lstat(output); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("promoted output did not retain its stable snapshot link: info=%v err=%v", info, err)
			}
		})
	}
	code := `
from pathlib import Path
path = Path("results/report.md")
blocked = False
try:
    path.write_text("mutated report\n", encoding="utf-8")
except OSError:
    blocked = True
print(persisted_namespace_value, blocked, path.read_text(encoding="utf-8").strip())
`
	result, err := app.executeAgentKernelTool(
		context.Background(), identity, "python", map[string]any{"code": code, "environment": "python"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true || !strings.Contains(stringValue(result["stdout"]), "kept-after-publication True validated report") {
		t.Fatalf("immutable execution output result=%#v", result)
	}
	raw, err := os.ReadFile(filepath.Join(output, "report.md"))
	if err != nil || string(raw) != "validated report\n" {
		t.Fatalf("managed output changed: %q err=%v", raw, err)
	}
	// A genuinely lost historical output must remain unavailable evidence, but
	// cannot prevent unrelated repair tools from running in the same task.
	snapshot, err := filepath.EvalSymlinks(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(snapshot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(snapshot, snapshot+".unavailable"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.agentKernelWorkspaceImmutableMounts(context.Background(), access, identity.workspaceDir, ""); err != nil {
		t.Fatalf("receipt-bound dangling alias blocked unrelated repair: %v", err)
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	if blocked := gateway.agentRuntimeManagedExecutionOutputMutationPreflight(context.Background(), "edit_file", map[string]any{"file_path": "new-note.md"}); blocked != nil {
		t.Fatalf("missing historical output blocked an unrelated edit: %#v", blocked)
	}
	if blocked := gateway.agentRuntimeManagedExecutionOutputMutationPreflight(context.Background(), "edit_file", map[string]any{"file_path": "results/report.md"}); blocked == nil {
		t.Fatal("missing output ownership was forgotten")
	}
	result, err = app.executeAgentKernelTool(context.Background(), identity, "python", map[string]any{"code": "print(persisted_namespace_value)", "environment": "python"})
	if err != nil || result["ok"] != true || !strings.Contains(stringValue(result["stdout"]), "kept-after-publication") {
		t.Fatalf("missing historical output blocked unrelated real kernel execution: %#v %v", result, err)
	}
}

func TestManagedExecutionUnavailableOutputCannotBePromoted(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "results")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report.md", "validation.json"} {
		if err := os.WriteFile(filepath.Join(output, name), []byte("unverified replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	app := &Server{scienceCapabilities: &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "capability", AcceptedEngines: []sciencecapability.EngineDefinition{{ID: "engine", ExecutionPack: sciencecapability.ExecutionPack{
			ID: "capability.engine", Mode: "local", Outputs: []sciencecapability.ExecutionOutput{{Path: "out/report.md", Delivery: "snapshot"}, {Path: "out/validation.json", Delivery: "snapshot"}},
		}}},
	}}}}
	authority := managedExecutionOutputAuthority{Root: output, PackID: "capability.engine", ExecutionID: "prior-execution", Unavailable: true, Digests: map[string]string{"results/report.md": strings.Repeat("a", 64), "results/validation.json": strings.Repeat("b", 64)}}
	request := agentSaveArtifactsRequest{Files: []string{"results/report.md"}, Destination: map[string]string{}}
	augmented := app.augmentAgentSaveArtifactsWithExecutionBundles(root, request, []managedExecutionOutputAuthority{authority})
	if len(augmented.Files) != 1 || len(augmented.BundleAdditions) != 0 {
		t.Fatalf("unavailable receipt authorized promotion: %#v", augmented)
	}
	if err := publishManagedExecutionOutputSnapshot(context.Background(), root, authority); !errors.Is(err, errManagedExecutionOutputUnavailable) {
		t.Fatalf("unavailable receipt was published: %v", err)
	}
	if _, err := app.verifyManagedExecutionOutputAuthority(context.Background(), root, filepath.Join(root, "absent"), "capability.engine", "prior-execution", nil); err == nil || errors.Is(err, errManagedExecutionOutputUnavailable) {
		t.Fatalf("missing content hid missing marker authority: %v", err)
	}
}

func TestManagedExecutionSnapshotRecoversInterruptedCompatibilitySwap(t *testing.T) {
	workspaceRoot := t.TempDir()
	outputRoot := filepath.Join(workspaceRoot, "results")
	if err := os.MkdirAll(outputRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("stable report\n")
	if err := os.WriteFile(filepath.Join(outputRoot, "report.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	authority := managedExecutionOutputAuthority{
		Root: outputRoot, PackID: "pack-recovery", ExecutionID: "execution-recovery",
		Digests: map[string]string{"results/report.md": hex.EncodeToString(digest[:])},
	}
	stableRoot := filepath.Join(workspaceRoot, ".synon-artifacts", ".managed")
	finalRoot := filepath.Join(stableRoot, managedExecutionSnapshotID(authority))
	if err := os.MkdirAll(finalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalRoot, "report.md"), content, 0o444); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(managedExecutionSnapshotManifest{
		Schema: managedExecutionSnapshotSchema, PackID: authority.PackID,
		ExecutionID: authority.ExecutionID, Digests: authority.Digests,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalRoot, ".synon-output-snapshot.json"), manifest, 0o444); err != nil {
		t.Fatal(err)
	}
	backupRoot := outputRoot + ".synon-output-backup-" + managedExecutionSnapshotID(authority)
	if err := os.Rename(outputRoot, backupRoot); err != nil {
		t.Fatal(err)
	}

	if err := publishManagedExecutionOutputSnapshot(context.Background(), workspaceRoot, authority); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(outputRoot)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("recovered compatibility path info=%v err=%v", info, err)
	}
	if got, err := os.ReadFile(filepath.Join(outputRoot, "report.md")); err != nil || string(got) != string(content) {
		t.Fatalf("recovered snapshot content=%q err=%v", got, err)
	}
	if _, err := os.Lstat(backupRoot); !os.IsNotExist(err) {
		t.Fatalf("recovery backup remains err=%v", err)
	}
}
