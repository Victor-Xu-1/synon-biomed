package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
			mountedReadOnly := false
			for _, mount := range mounts {
				if filepath.Clean(mount.Path) == filepath.Clean(output) && !mount.Writable {
					mountedReadOnly = true
					break
				}
			}
			if !mountedReadOnly {
				t.Fatalf("promoted output lost its immutable mount: %#v", mounts)
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
print(blocked, path.read_text(encoding="utf-8").strip())
`
	result, err := app.executeAgentKernelTool(
		context.Background(), identity, "python", map[string]any{"code": code, "environment": "python"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true || !strings.Contains(stringValue(result["stdout"]), "True validated report") {
		t.Fatalf("immutable execution output result=%#v", result)
	}
	raw, err := os.ReadFile(filepath.Join(output, "report.md"))
	if err != nil || string(raw) != "validated report\n" {
		t.Fatalf("managed output changed: %q err=%v", raw, err)
	}
}
