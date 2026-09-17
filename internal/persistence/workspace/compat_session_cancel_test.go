package workspace

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCancelCompatibilityFrameTreePreservesAbsorbingTerminalStates(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	type child struct{ id, status string }
	createTree := func(projectID, rootStatus string, children []child) string {
		t.Helper()
		if _, err := store.CreateProject(CreateProjectInput{ID: projectID, Name: projectID}); err != nil {
			t.Fatal(err)
		}
		rootID := projectID + "-root"
		if _, err := store.CreateFrame(CreateFrameInput{
			ID: rootID, ProjectID: projectID, AgentName: "OPERON",
			Status: rootStatus, ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
		for _, child := range children {
			if _, err := store.CreateFrame(CreateFrameInput{
				ID: child.id, ProjectID: projectID, ParentFrameID: rootID,
				AgentName: "OPERON", Status: child.status, ConversationType: "agent",
			}); err != nil {
				t.Fatal(err)
			}
		}
		return rootID
	}
	status := func(id string) string {
		t.Helper()
		frame, found, err := store.GetFrame(id)
		if err != nil || !found {
			t.Fatalf("GetFrame(%q) found=%t err=%v", id, found, err)
		}
		return frame.Status
	}

	rootID := createTree("mixed", "completed", []child{
		{"mixed-active", "processing"}, {"mixed-cancelled", "cancelled"}, {"mixed-failed", "failed"},
	})
	first, err := store.CancelCompatibilityFrameTree(rootID, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mixed-active", "mixed-cancelled"}; !reflect.DeepEqual(first.CancelledFrameIDs, want) {
		t.Fatalf("first cancelled IDs = %v, want %v", first.CancelledFrameIDs, want)
	}
	if len(first.Events) != 1 || first.Events[0].FrameID != "mixed-active" || first.Events[0].Type != "frame_cancelled" {
		t.Fatalf("first events = %#v", first.Events)
	}
	for id, want := range map[string]string{
		rootID: "completed", "mixed-active": "cancelled", "mixed-cancelled": "cancelled", "mixed-failed": "failed",
	} {
		if got := status(id); got != want {
			t.Fatalf("status(%q) = %q, want %q", id, got, want)
		}
	}

	second, err := store.CancelCompatibilityFrameTree(rootID, "stop again")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mixed-active", "mixed-cancelled"}; !reflect.DeepEqual(second.CancelledFrameIDs, want) {
		t.Fatalf("second cancelled IDs = %v, want %v", second.CancelledFrameIDs, want)
	}
	if len(second.Events) != 0 {
		t.Fatalf("idempotent cancel emitted events: %#v", second.Events)
	}
	events, err := store.ListFrameEvents("mixed-active", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "frame_cancelled" {
		t.Fatalf("active frame events = %#v", events)
	}

	terminalRoot := createTree("terminal", "failed", []child{{"terminal-child", "completed"}})
	terminal, err := store.CancelCompatibilityFrameTree(terminalRoot, "no-op")
	if err != nil {
		t.Fatal(err)
	}
	if len(terminal.CancelledFrameIDs) != 0 || len(terminal.Events) != 0 {
		t.Fatalf("all-terminal cancellation = %#v", terminal)
	}
	if got := status(terminalRoot); got != "failed" {
		t.Fatalf("terminal root status = %q", got)
	}
	if got := status("terminal-child"); got != "completed" {
		t.Fatalf("terminal child status = %q", got)
	}
}
