package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAgentRuntimeToolSchemasHideWorktreeToolsOutsideGitProject(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	nonGit := identity.workspaceDir
	if err := os.MkdirAll(nonGit, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProject(identity.access.Frame.ProjectID, workspace.UpdateProjectInput{Path: &nonGit}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.transcriptStore = repository
	if _, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + identity.access.Frame.ID, OwnerID: identity.access.UserID,
		ExternalID: identity.access.Frame.ID, SessionID: identity.access.Frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: identity.access.Frame.ProjectID,
		RootFrameID: identity.access.Frame.RootFrameID, FrameID: identity.access.Frame.ID, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	for _, schema := range app.agentRuntimeToolSchemasWithContextOptions(context.Background(),
		[]string{"EnterWorktree", "ExitWorktree"}, true, identity.access.Frame.ID) {
		if schema.Name == "EnterWorktree" || schema.Name == "ExitWorktree" {
			t.Fatalf("non-git project exposed %s", schema.Name)
		}
	}

	command := exec.Command("git", "init", nonGit)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	resolved := app.resolveAgentKernelContext(context.Background(), identity.access.Frame.ID)
	if resolved == nil {
		t.Fatal("resolved kernel context is unavailable after git init")
	}
	if _, err := findGitRoot(context.Background(), resolved.workspaceDir); err != nil {
		t.Fatalf("resolved workspace %q is not a git repository: %v", resolved.workspaceDir, err)
	}
	if !app.agentRuntimeWorktreeToolsAvailable(context.Background(), resolved) {
		t.Fatalf("worktree availability remained false for %q", resolved.workspaceDir)
	}
	found := map[string]bool{}
	for _, schema := range app.agentRuntimeToolSchemasWithContextOptions(context.Background(),
		[]string{"EnterWorktree", "ExitWorktree"}, true, identity.access.Frame.ID) {
		found[schema.Name] = true
	}
	if !found["EnterWorktree"] || !found["ExitWorktree"] {
		t.Fatalf("git project worktree schemas=%#v registered-enter=%t registered-exit=%t", found,
			hasRegisteredTool(app.tools, "EnterWorktree"), hasRegisteredTool(app.tools, "ExitWorktree"))
	}
}
