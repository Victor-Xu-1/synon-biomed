package workspace

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCompatibilityProjectionReadsMigratedV11Database(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "proj_example", UserID: "local", Name: "Example project",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "proj_example", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Migrated conversation",
	}); err != nil {
		t.Fatal(err)
	}
	const migratedMentions = `[
		{"artifact_id":"artifact-a","filename":"a.fasta"},
		"artifact-b",
		"artifact-a",
		{"filename":"ignored"},
		7
	]`
	if _, err := store.db.Exec(
		`INSERT INTO frame_runtime_metadata (frame_id, mentioned_artifact_ids)
		 VALUES (?, ?)
		 ON CONFLICT(frame_id) DO UPDATE SET mentioned_artifact_ids = excluded.mentioned_artifact_ids`,
		"root", migratedMentions,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
		 VALUES ('legacy-message', 'root', 1, 'message', ?, CURRENT_TIMESTAMP)`,
		`{"role":"user","content":[{"type":"text","text":"Migrated v1.1 message"}],"_uuid":"legacy-uuid"}`,
	); err != nil {
		t.Fatal(err)
	}
	frames, err := store.ListCompatibilityFrames("local", "", true, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantMentions := []string{"artifact-a", "artifact-b"}
	if len(frames) != 1 || !reflect.DeepEqual(frames[0].MentionedArtifactIDs, wantMentions) {
		t.Fatalf("frames = %#v, want mentions %#v", frames, wantMentions)
	}
	if frames[0].MessageCount != 1 {
		t.Fatalf("message count = %d, want 1", frames[0].MessageCount)
	}
	messages, err := store.CompatibilityFrameMessages("root", 0, 10)
	if err != nil || messages.Total != 1 || len(messages.Messages) != 1 || messages.Messages[0]["_uuid"] != "legacy-uuid" {
		t.Fatalf("migrated messages = %#v, err=%v", messages, err)
	}
	benches, found, err := store.ListCompatibilityProjectBenches(context.Background(), "local", "proj_example", 100, "")
	if err != nil || !found || len(benches) != 1 ||
		!reflect.DeepEqual(benches[0].MentionedArtifactIDs, wantMentions) {
		t.Fatalf("list migrated benches: found=%v count=%d err=%v", found, len(benches), err)
	}
}
