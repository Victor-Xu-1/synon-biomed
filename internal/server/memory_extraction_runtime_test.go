package server

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/memoryconfig"
	"synon-go/internal/memoryextract"
	"synon-go/internal/memorytools"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceMemoryExtractionRuntimePersistsAndRecallsAcrossSessions(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	journal := eventjournal.NewEventJournal(filepath.Join(t.TempDir(), "journal"))
	server.eventJournal = journal
	server.memoryConfig = memoryconfig.Default()
	server.memoryConfig.ExtractEnabled = true
	server.memoryConfig.ExtractMode = "haiku"
	server.memoryConfig.PIClassifierEnabled = false
	for _, message := range []eventjournal.Message{
		{"type": "message", "role": "user", "text": "Remember that EGFR assay reports must include confidence intervals."},
		{"type": "message", "role": "assistant", "text": "I will preserve that durable reporting requirement."},
		{"type": "runner_finished", "status": "completed"},
	} {
		if _, err := journal.Append("frame-root", message, eventjournal.Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	runtime := newMemoryExtractionRuntimeWithFactory(server, func(scope memorytools.Scope, mode string, _ *agentruntime.ModelRequest) memoryextract.Extractor {
		if scope.UserID != "user-1" || scope.ProjectID != "project-1" || scope.SourceFrameID != "frame-root" || mode != "compact" {
			t.Fatalf("scope=%#v mode=%q", scope, mode)
		}
		return memoryExtractorFunc(func(_ context.Context, request memoryextract.ExtractRequest) (map[string]any, error) {
			if !strings.Contains(request.Transcript, "EGFR assay reports") || request.Mode != "compact" {
				t.Fatalf("request=%#v", request)
			}
			return map[string]any{"append": []any{map[string]any{
				"text": "EGFR assay reports must include confidence intervals.", "evidence": "stated", "entity": "project:project-1",
			}}}, nil
		})
	})
	result, err := runtime.RunCompletedRoot(context.Background(), "frame-root")
	if err != nil || result.Gate != "done" || len(result.Applied.Appended) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	rows, err := server.workspaceStore.ListMemoryExtractionRows(context.Background(), "user-1", "project-1")
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].Body, "confidence intervals") {
		t.Fatalf("rows=%#v err=%v", rows, err)
	}
	recalled, err := server.workspaceStore.RecallMemories(context.Background(), workspace.MemoryRecallOptions{
		UserID: "user-1", ProjectID: "project-1", Query: "EGFR confidence intervals reporting requirement", CrossProject: true,
		Config: &server.memoryConfig,
	})
	if err != nil || len(recalled) != 1 || recalled[0].ID != rows[0].ID {
		t.Fatalf("recalled=%#v err=%v", recalled, err)
	}
}

func TestWorkspaceMemoryExtractionRuntimeHonorsEveryPolicyGateAndDeduplicatesCompletion(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	journal := eventjournal.NewEventJournal(filepath.Join(t.TempDir(), "journal"))
	server.eventJournal = journal
	server.memoryConfig.ExtractMode = "haiku"
	server.memoryConfig.PIClassifierEnabled = false
	for _, message := range []eventjournal.Message{
		{"type": "message", "role": "user", "text": "Remember the validated CRBN assay convention for future work."},
		{"type": "message", "role": "assistant", "text": "The validated convention is durable."},
		{"type": "runner_finished", "status": "completed"},
	} {
		if _, err := journal.Append("frame-root", message, eventjournal.Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int64
	runtime := newMemoryExtractionRuntimeWithFactory(server, func(memorytools.Scope, string, *agentruntime.ModelRequest) memoryextract.Extractor {
		return memoryExtractorFunc(func(context.Context, memoryextract.ExtractRequest) (map[string]any, error) {
			calls.Add(1)
			return map[string]any{}, nil
		})
	})
	ctx := context.Background()
	if err := server.workspaceStore.SetMemoryEnabled(ctx, "user-1", false); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.RunCompletedRoot(ctx, "frame-root"); err != nil || result.Gate != "disabled" {
		t.Fatalf("user-off result=%#v err=%v", result, err)
	}
	if err := server.workspaceStore.SetMemoryEnabled(ctx, "user-1", true); err != nil {
		t.Fatal(err)
	}
	if err := server.workspaceStore.SetProjectMemoryEnabled(ctx, "project-1", "user-1", false); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.RunCompletedRoot(ctx, "frame-root"); err != nil || result.Gate != "disabled" {
		t.Fatalf("project-off result=%#v err=%v", result, err)
	}
	if err := server.workspaceStore.SetProjectMemoryEnabled(ctx, "project-1", "user-1", true); err != nil {
		t.Fatal(err)
	}
	if err := server.setMemoryAutoExtractionEnabled("user-1", false); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.RunCompletedRoot(ctx, "frame-root"); err != nil || result.Gate != "disabled-auto" {
		t.Fatalf("auto-off result=%#v err=%v", result, err)
	}
	if err := server.setMemoryAutoExtractionEnabled("user-1", true); err != nil {
		t.Fatal(err)
	}
	if err := server.sessionStore.Upsert(sessionstore.Session{
		ID: "frame-root", Project: &sessionstore.Project{ID: "project-1"},
		Orchestration: map[string]any{"frame_id": "frame-root", "sessionConfig": map[string]any{"memory_mode": "off"}},
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := runtime.RunCompletedRoot(ctx, "frame-root"); err != nil || result.Gate != "disabled" {
		t.Fatalf("session-off result=%#v err=%v", result, err)
	}
	if err := server.sessionStore.Upsert(sessionstore.Session{
		ID: "frame-root", Project: &sessionstore.Project{ID: "project-1"},
		Orchestration: map[string]any{"frame_id": "frame-root", "sessionConfig": map[string]any{"memory_mode": "on"}},
	}); err != nil {
		t.Fatal(err)
	}
	first := runtime.ScheduleCompletedRootEvent("frame-root", 3)
	second := runtime.ScheduleCompletedRootEvent("frame-root", 3)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("extractor calls=%d, want 1", calls.Load())
	}
}

func TestWorkspaceMemoryExtractionRuntimeRejectsChildAndAsideFrames(t *testing.T) {
	ctx := context.Background()

	t.Run("child frame", func(t *testing.T) {
		server := openWorkspaceMemoryToolServer(t)
		child, err := server.workspaceStore.CreateFrame(workspace.CreateFrameInput{
			ID: "frame-child", ProjectID: "project-1", ParentFrameID: "frame-root",
			AgentName: "SPECIALIST", Status: "completed", ConversationType: "agent",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.sessionStore.Upsert(sessionstore.Session{
			ID: child.ID, Project: &sessionstore.Project{ID: "project-1"},
			Orchestration: map[string]any{"frame_id": child.ID},
		}); err != nil {
			t.Fatal(err)
		}
		runtime := newMemoryExtractionRuntimeWithFactory(server, nil)
		result, err := runtime.RunCompletedRoot(ctx, child.ID)
		if err != nil || result.Gate != "not-root" {
			t.Fatalf("child result=%#v err=%v", result, err)
		}
	})

	t.Run("aside frame", func(t *testing.T) {
		server := openWorkspaceMemoryToolServer(t)
		if _, err := server.workspaceStore.SetFrameRuntimeMetadata("frame-root", workspace.FrameRuntimeMetadata{
			ContextData: map[string]any{"_aside_parent": "frame-parent"},
		}); err != nil {
			t.Fatal(err)
		}
		runtime := newMemoryExtractionRuntimeWithFactory(server, nil)
		result, err := runtime.RunCompletedRoot(ctx, "frame-root")
		if err != nil || result.Gate != "disabled-aside" {
			t.Fatalf("aside result=%#v err=%v", result, err)
		}
	})
}

func TestWorkspaceForkedExtractionFiltersRecalledBodies(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	if _, err := server.workspaceStore.CreateMemory(workspace.CreateMemoryInput{
		ID: "mem_recalled", UserID: "user-1", Body: "Do not re-extract this recalled preference.",
		Origin: "user", Evidence: "stated",
	}); err != nil {
		t.Fatal(err)
	}
	runtime := newMemoryExtractionRuntimeWithFactory(server, nil)
	bodies, err := runtime.recalledBodies(context.Background(), memorytools.Scope{
		UserID: "user-1", ProjectID: "project-1", FrameID: "frame-root", SourceFrameID: "frame-root",
	}, []memoryextract.Message{{Role: "user", Content: []memoryextract.Block{{
		Type: memoryextract.BlockText,
		Text: "<memory_recall signal=\"user_message\">[mem_recalled] preference</memory_recall>",
	}}}})
	if err != nil || len(bodies) != 1 || bodies[0] != "Do not re-extract this recalled preference." {
		t.Fatalf("recalled bodies=%#v err=%v", bodies, err)
	}
}

func TestWorkspaceCompletedWebFrameExtractsWithoutLegacySessionIndex(t *testing.T) {
	server := openWorkspaceMemoryToolServer(t)
	journal := eventjournal.NewEventJournal(filepath.Join(t.TempDir(), "journal"))
	server.eventJournal = journal
	server.memoryConfig.ExtractMode = "haiku"
	server.memoryConfig.PIClassifierEnabled = false
	deleted, err := server.sessionStore.Delete("frame-root")
	if err != nil || !deleted {
		t.Fatalf("delete compatibility session deleted=%v err=%v", deleted, err)
	}
	for _, message := range []eventjournal.Message{
		{"type": "message", "role": "user", "text": "Remember the EGFR evidence-reporting standard."},
		{"type": "message", "role": "assistant", "text": "The reporting standard is durable."},
		{"type": "runner_finished", "status": "completed"},
	} {
		if _, err := journal.Append("frame-root", message, eventjournal.Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	runtime := newMemoryExtractionRuntimeWithFactory(server, func(scope memorytools.Scope, mode string, _ *agentruntime.ModelRequest) memoryextract.Extractor {
		if scope.SourceFrameID != "frame-root" || scope.FrameID != "frame-root" || mode != "compact" {
			t.Fatalf("scope=%#v mode=%q", scope, mode)
		}
		return memoryExtractorFunc(func(context.Context, memoryextract.ExtractRequest) (map[string]any, error) {
			return map[string]any{"append": []any{map[string]any{
				"text": "EGFR reports must preserve evidence provenance.", "evidence": "stated", "entity": "project:project-1",
			}}}, nil
		})
	})
	result, err := runtime.RunCompletedRoot(context.Background(), "frame-root")
	if err != nil || result.Gate != "done" || len(result.Applied.Appended) != 1 {
		t.Fatalf("web-frame result=%#v err=%v", result, err)
	}
}
