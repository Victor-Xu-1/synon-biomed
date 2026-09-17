package workspace

import (
	"strings"
	"testing"
)

func TestListActiveFramesForRootUsesIndexedLiveProjection(t *testing.T) {
	store, err := Open(t.TempDir() + "/workspace.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ id, status string }{
		{id: "active", status: "awaiting_user_response"},
		{id: "completed", status: "completed"},
		{id: "failed", status: "failed"},
		{id: "cancelled", status: "cancelled"},
	} {
		if _, err := store.CreateFrame(CreateFrameInput{
			ID: fixture.id, ProjectID: "project", ParentFrameID: root.ID,
			AgentName: "RESEARCH", Status: fixture.status, ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := store.ListActiveFramesForRoot("project", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].ID != root.ID || frames[1].ID != "active" {
		t.Fatalf("active frames = %#v", frames)
	}

	rows, err := store.db.Query(`EXPLAIN QUERY PLAN
		SELECT id FROM frames
		WHERE project_id=? AND root_frame_id=?
		  AND status NOT IN ('completed','failed','cancelled','canceled','stopped')
		ORDER BY root_sequence,created_at,id`, "project", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "frames_project_root_seq_idx") {
		t.Fatalf("active frame query plan = %s", plan.String())
	}
}
