package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildCompatibilitySessionExportProjectsDurableV11Schema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-export", UserID: "owner-export", Name: "Export"}); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(CreateFrameInput{
		ID: "root-session-123", ProjectID: "project-export", AgentName: "planner",
		Status: "completed", ConversationType: "task", Name: "Durable session",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(CreateFrameInput{
		ID: "child-session-1", ProjectID: "project-export", ParentFrameID: root.ID,
		AgentName: "writer", Status: "completed", ConversationType: "task", Name: "Child",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(root.ID, FrameRuntimeMetadata{
		TaskSummary: "prepare export", ContextData: map[string]any{"_compaction_count": 1, "private": "not-exported"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(child.ID, FrameRuntimeMetadata{DelegateName: "delegate-writer"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	completed := time.Now().UTC().Add(time.Minute)
	if _, err := db.Exec(`
		UPDATE frame_runtime_metadata
		SET model = 'gpt-test', effort = 'high', input_tokens = 120,
			output_tokens = 80, total_cost = 1.23456, completed_at = ?,
			input_data = '{"prompt":"export"}', output_data = '{"ok":true}'
		WHERE frame_id = ?`, completed, root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(FrameEventInput{
		FrameID: root.ID, Type: "user_message", Payload: map[string]any{"role": "user", "text": "export it"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateCompactionArchive(CreateCompactionArchiveInput{
		FrameID: root.ID, MessageCount: 1, TokenCount: intPointer(22), Summary: "earlier context",
		Messages: []map[string]any{{"role": "user", "content": "older"}},
	}); err != nil {
		t.Fatal(err)
	}

	exportedAt := time.Date(2026, time.July, 14, 6, 7, 8, 0, time.UTC)
	exported, found, err := store.BuildCompatibilitySessionExport(context.Background(), "owner-export", "root-sess", exportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("session export not found by root prefix")
	}
	if exported.ExportVersion != "1.0" || exported.RootFrameID != root.ID || exported.ProjectID != "project-export" ||
		exported.ConversationName != "Durable session" || exported.ExportedAt != exportedAt.Format(time.RFC3339Nano) {
		t.Fatalf("export identity = %#v", exported)
	}
	if exported.Summary.TotalFrames != 2 || exported.Summary.TotalTokens.Input != 120 ||
		exported.Summary.TotalTokens.Output != 80 || exported.Summary.TotalCost != 1.2346 ||
		exported.Summary.Status != "completed" {
		t.Fatalf("export summary = %#v", exported.Summary)
	}
	if len(exported.Frames) != 2 || len(exported.Frames[0].ChildrenIDs) != 1 ||
		exported.Frames[0].ChildrenIDs[0] != child.ID || exported.Frames[1].ParentFrameID == nil ||
		*exported.Frames[1].ParentFrameID != root.ID {
		t.Fatalf("export frame tree = %#v", exported.Frames)
	}
	rootExport := exported.Frames[0]
	if rootExport.Model == nil || *rootExport.Model != "gpt-test" || rootExport.Effort == nil || *rootExport.Effort != "high" ||
		rootExport.ContextMetadata["compaction_count"] != float64(1) || rootExport.ContextMetadata["private"] != nil ||
		len(rootExport.Messages) != 1 || len(rootExport.CompactionArchives) != 1 ||
		len(rootExport.CompactionArchives[0].Messages) != 1 {
		t.Fatalf("root export detail = %#v", rootExport)
	}
	if _, found, err := store.BuildCompatibilitySessionExport(context.Background(), "another-owner", root.ID, exportedAt); err != nil || found {
		t.Fatalf("unauthorized export found=%v err=%v", found, err)
	}
}

func intPointer(value int) *int { return &value }
