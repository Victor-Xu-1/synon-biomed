package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	taskruns "synon-go/internal/persistence/taskruns"
	workspace "synon-go/internal/persistence/workspace"
)

func TestSessionRunnerDelegationToggleUsesOnlyKernelHostAuthority(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "delegation-toggle-project", UserID: "local", Name: "Delegation toggle", Path: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		frameID := fmt.Sprintf("delegation-toggle-%t", enabled)
		if _, err := store.CreateFrame(workspace.CreateFrameInput{
			ID: frameID, ProjectID: project.ID, AgentName: "OPERON", Status: "running",
			ConversationType: "task", Name: frameID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := newV11TestServer(t, Options{FileRoot: root, Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})

	for _, enabled := range []bool{false, true} {
		frameID := fmt.Sprintf("delegation-toggle-%t", enabled)
		options, selected := srv.applySessionRunnerBundledAgent(
			sessionstore.Session{
				ID: frameID,
				Orchestration: map[string]any{
					"sessionConfig": map[string]any{"ultraMode": enabled},
				},
			},
			normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}),
		)
		if selected != "OPERON" {
			t.Fatalf("enabled=%t selected agent=%q", enabled, selected)
		}
		tools := srv.chatRunnerTools(options.AllowedTools)
		hasLegacyDelegationTool := false
		for _, tool := range tools {
			switch tool.Function.Name {
			case "Agent", "TaskRun", "agent_run", "task_run":
				hasLegacyDelegationTool = true
			}
		}
		if hasLegacyDelegationTool {
			t.Fatalf("enabled=%t exposed a legacy direct delegation tool", enabled)
		}
		if enabled {
			if !strings.Contains(options.SystemPrompt, "host.delegate") || strings.Contains(options.SystemPrompt, "multiple Agent calls") {
				t.Fatalf("delegation guidance did not select the kernel host authority: %q", options.SystemPrompt)
			}
		} else if strings.Contains(options.SystemPrompt, "Parallel delegation is enabled for this conversation.") {
			t.Fatalf("delegation-disabled conversation received delegation guidance: %q", options.SystemPrompt)
		}
	}
}

func TestDelegationParentFrameIsAcceptedByTaskRunQueue(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "delegation-frame-project", UserID: "local", Name: "Frame parent", Path: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	frameID := "delegation-frame-parent"
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: project.ID, AgentName: "OPERON", Status: "running",
		ConversationType: "task", Name: "Transcript frame parent",
	}); err != nil {
		t.Fatal(err)
	}
	srv := newV11TestServer(t, Options{FileRoot: root, Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	run, err := srv.taskRunStore.Create(taskruns.Input{
		Action: "start", Objective: "Frame parent queue check", Message: "queue one delegated child",
		Orchestration: map[string]any{"parentSessionId": frameID},
		TaskGraph: &taskruns.Graph{Steps: []taskruns.StepInput{{
			ID: "delegate", Title: "Delegate", Description: "queue one delegated child",
			Executor: map[string]any{"kind": "agent"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := srv.queueTaskRunSession(run, "Agent delegation started")
	if err != nil {
		t.Fatalf("queue transcript-frame child: %v", err)
	}
	child, found, err := srv.sessionStore.Get(queued.SessionID)
	if err != nil || !found {
		t.Fatalf("child session=%#v found=%t err=%v", child, found, err)
	}
	if child.WorkDir != root || child.Project == nil || child.Project.ID != project.ID {
		t.Fatalf("child did not inherit frame boundary: %#v", child)
	}
	if _, found, err := srv.sessionStore.Get(frameID); err != nil || found {
		t.Fatalf("frame parent should remain a workspace projection, found=%t err=%v", found, err)
	}
}

func TestDelegationCapabilityRequiresExplicitConversationToggleForDelegatingAgent(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "delegation-specialist-project", UserID: "local", Name: "Delegation specialist", Path: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	frameID := "delegation-specialist-frame"
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: project.ID, AgentName: "AIDD_EXPERT", Status: "running",
		ConversationType: "task", Name: "Delegation specialist frame",
	}); err != nil {
		t.Fatal(err)
	}
	srv := newV11TestServer(t, Options{FileRoot: root, Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})

	for _, enabled := range []bool{false, true} {
		orchestration := map[string]any{}
		if enabled {
			orchestration["sessionConfig"] = map[string]any{"ultraMode": true}
		}
		options, selected := srv.applySessionRunnerBundledAgent(
			sessionstore.Session{ID: frameID, Orchestration: orchestration},
			normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}),
		)
		if selected != "AIDD_EXPERT" {
			t.Fatalf("enabled=%t selected agent=%q", enabled, selected)
		}
		hasLegacyDelegationTool := false
		for _, tool := range srv.chatRunnerTools(options.AllowedTools) {
			switch tool.Function.Name {
			case "Agent", "TaskRun", "agent_run", "task_run":
				hasLegacyDelegationTool = true
			}
		}
		if hasLegacyDelegationTool {
			t.Fatalf("enabled=%t exposed a legacy direct delegation tool", enabled)
		}
		if enabled != strings.Contains(options.SystemPrompt, "Parallel delegation is enabled for this conversation.") {
			t.Fatalf("enabled=%t kernel-host guidance mismatch: %q", enabled, options.SystemPrompt)
		}
	}
}
