package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestMCPDiscoverySemaphoreBoundsConcurrentConnectorProbes(t *testing.T) {
	srv := &Server{mcpDiscoverySlots: make(chan struct{}, 1)}
	if err := srv.acquireMCPDiscoverySlot(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := srv.acquireMCPDiscoverySlot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second probe acquisition error = %v, want deadline", err)
	}
	srv.releaseMCPDiscoverySlot()
	if err := srv.acquireMCPDiscoverySlot(context.Background()); err != nil {
		t.Fatalf("released probe slot was not reusable: %v", err)
	}
	srv.releaseMCPDiscoverySlot()
}

func TestWorkspaceMCPRuntimeResolutionPreservesCancellationCause(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	enabled := true
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "operon", UserID: "local", Name: "OPERON", DisplayName: "OPERON",
		Description: "MCP cancellation fixture", SystemPrompt: "Use MCP tools.", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "Project", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent", Name: "MCP cancellation",
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Workspace: store, FileRoot: root})
	cause := fmt.Errorf("%w: MCP resolution stopped", ErrGenerationStopped)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	discovery := srv.agentRuntimeWorkspaceMCPToolSchemas(ctx, "frame", nil, nil)
	if discovery.Unavailable || len(discovery.Schemas) != 0 {
		t.Fatalf("cancelled task was misreported as recoverable MCP unavailability: %#v", discovery)
	}

	if _, _, err := srv.workspaceMCPRuntimeContextWithContext(ctx, "frame"); !errors.Is(err, ErrGenerationStopped) {
		t.Fatalf("workspace MCP context cancellation = %v", err)
	}
	_, handled, err := srv.executeWorkspaceMCPTool(ctx, "frame", "mcp__fixture__echo", map[string]any{"text": "ignored"})
	if !handled || !errors.Is(err, ErrGenerationStopped) {
		t.Fatalf("workspace MCP execution handled=%v cancellation=%v", handled, err)
	}
}
