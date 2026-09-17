package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/sciencecapability"
)

func TestSaveArtifactsAddsRegistryDeclaredExecutionBundleOutputs(t *testing.T) {
	workspace := t.TempDir()
	output := filepath.Join(workspace, "custom-output")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(output, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(managedExecutionOutputOwnershipMarker,
		`{"schema":"synon.execution-pack-output-owner.v1","execution_pack_id":"capability.engine"}`)
	write("report.md", "# Report\n")
	write("complex.pdb", "ATOM\n")
	write("validation.json", `{"overall_pass":true}`)
	server := &Server{scienceCapabilities: &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "capability.engine", Mode: "local", Skill: "managed-workflow",
				Executable: "python", Script: "executionpacks/run.py",
				Outputs: []sciencecapability.ExecutionOutput{
					{Kind: "report", Path: "out/report.md", Delivery: "snapshot"},
					{Kind: "complex", Path: "out/complex.pdb", Delivery: "snapshot"},
					{Kind: "validation", Path: "out/validation.json", Delivery: "working_data"},
				},
			},
		}},
	}}}}
	request := agentSaveArtifactsRequest{
		Files: []string{"custom-output/report.md"}, Destination: map[string]string{
			"custom-output/report.md": "snapshot",
		},
	}
	request = server.augmentAgentSaveArtifactsWithExecutionBundles(workspace, request, []managedExecutionOutputAuthority{{
		Root: output, PackID: "capability.engine", ExecutionID: "execution-one",
		Digests: map[string]string{
			"custom-output/report.md": "digest-report", "custom-output/complex.pdb": "digest-complex",
			"custom-output/validation.json": "digest-validation",
		},
	}})
	wantFiles := []string{
		"custom-output/report.md",
		"custom-output/complex.pdb",
		"custom-output/validation.json",
	}
	if !reflect.DeepEqual(request.Files, wantFiles) {
		t.Fatalf("files=%#v want=%#v", request.Files, wantFiles)
	}
	wantAdded := []string{"custom-output/complex.pdb", "custom-output/validation.json"}
	if !reflect.DeepEqual(request.BundleAdditions, wantAdded) {
		t.Fatalf("bundle additions=%#v want=%#v", request.BundleAdditions, wantAdded)
	}
	if request.Destination["custom-output/complex.pdb"] != "snapshot" ||
		request.Destination["custom-output/validation.json"] != "working_data" {
		t.Fatalf("destination=%#v", request.Destination)
	}
}

func TestManagedExecutionOutputAuthorityRequiresExactReceiptDigests(t *testing.T) {
	workspaceDir := t.TempDir()
	output := filepath.Join(workspaceDir, "results")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		managedExecutionOutputOwnershipMarker: `{"schema":"synon.execution-pack-output-owner.v1","execution_pack_id":"capability.engine"}`,
		"report.md":                           "# validated\n",
		"raw-engine.log":                      "engine evidence\n",
		"validation.json":                     `{"overall_pass":true}`,
	}
	writes := map[string]string{}
	for name, content := range files {
		path := filepath.Join(output, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(content))
		writes[path] = hex.EncodeToString(digest[:])
	}
	server := &Server{scienceCapabilities: &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "capability.engine", Mode: "local", Skill: "managed-workflow",
				Executable: "python", Script: "executionpacks/run.py",
				Outputs: []sciencecapability.ExecutionOutput{
					{Path: "out/report.md"}, {Path: "out/validation.json"},
				},
			},
		}},
	}}}}
	authority, err := server.verifyManagedExecutionOutputAuthority(
		context.Background(), workspaceDir, output, "capability.engine", "execution-one", writes,
	)
	if err != nil || authority.ExecutionID != "execution-one" || len(authority.Digests) != 4 {
		t.Fatalf("authority=%#v err=%v", authority, err)
	}
	access := workspace.KernelFrameAccess{
		UserID: "owner", RootFrameIncarnationID: "root-incarnation",
		Frame: workspace.Frame{
			ID: "frame", IncarnationID: "frame-incarnation", ProjectID: "project",
			RootFrameID: "frame", AgentName: "OPERON",
		},
	}
	kernelID, err := kernelruntime.StableSessionID(kernelruntime.SessionSpec{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, KernelKind: "bash", Language: "python",
		Environment: "engine-a-runtime", WorkspaceDir: workspaceDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	fileWrites := make([]kernelruntime.FileWrite, 0, len(writes))
	for path, digest := range writes {
		relative, err := filepath.Rel(workspaceDir, path)
		if err != nil {
			t.Fatal(err)
		}
		fileWrites = append(fileWrites, kernelruntime.FileWrite{Path: filepath.ToSlash(relative), SHA256: digest})
	}
	script := filepath.Join(workspaceDir, ".synon", "runtime", "skills", "managed-workflow-abcd", "scripts", "run.py")
	records := []workspace.ExecutionLogRecord{{
		ID: "execution-one", FrameID: access.Frame.ID, KernelID: kernelID,
		KernelKind: "bash", CondaEnv: "engine-a-runtime", Language: "python",
		Source: `python "` + script + `"`, ExitStatus: "ok", FilesWritten: fileWrites,
	}}
	derived, err := server.managedExecutionOutputAuthorities(
		context.Background(), access, workspaceDir, records, nil,
	)
	if err != nil || len(derived) != 1 || derived[0].ExecutionID != "execution-one" {
		t.Fatalf("derived authorities=%#v err=%v", derived, err)
	}
	if err := os.WriteFile(filepath.Join(output, "report.md"), []byte("# replaced\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.verifyManagedExecutionOutputAuthority(
		context.Background(), workspaceDir, output, "capability.engine", "execution-one", writes,
	); err == nil {
		t.Fatal("mutated execution-pack output retained its host-owned authority")
	}
	skipped, err := server.managedExecutionOutputAuthorities(
		context.Background(), access, workspaceDir, records, nil, "capability.engine",
	)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("canonical pack rerun could not bypass only its own stale mount: %#v err=%v", skipped, err)
	}
}

func TestSaveArtifactsDoesNotTrustUnknownExecutionBundleMarker(t *testing.T) {
	workspace := t.TempDir()
	output := filepath.Join(workspace, "output")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, managedExecutionOutputOwnershipMarker), []byte(
		`{"schema":"synon.execution-pack-output-owner.v1","execution_pack_id":"unknown.engine"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "report.md"), []byte("# Report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{scienceCapabilities: &sciencecapability.Catalog{}}
	request := agentSaveArtifactsRequest{Files: []string{"output/report.md"}, Destination: map[string]string{}}
	request = server.augmentAgentSaveArtifactsWithExecutionBundles(workspace, request, nil)
	if len(request.Files) != 1 || len(request.BundleAdditions) != 0 {
		t.Fatalf("untrusted bundle changed request: %#v", request)
	}
}
