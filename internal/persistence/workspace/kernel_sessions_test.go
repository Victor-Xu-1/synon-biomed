package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestKernelFrameAccessCarriesCurrentRootIncarnation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: root.ID,
		AgentName: "ANALYST", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, read := range []func() (KernelFrameAccess, bool, error){
		func() (KernelFrameAccess, bool, error) { return store.GetKernelFrameAccess(child.ID) },
		func() (KernelFrameAccess, bool, error) {
			return store.GetKernelFrameAccessContext(context.Background(), child.ID)
		},
	} {
		access, found, err := read()
		if err != nil || !found || access.Frame.IncarnationID != child.IncarnationID ||
			access.RootFrameIncarnationID != root.IncarnationID {
			t.Fatalf("kernel access=%#v found=%t err=%v", access, found, err)
		}
	}

	if _, err := store.db.Exec(`UPDATE frames SET incarnation_id='replacement-root-incarnation' WHERE id=?`, root.ID); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccess(child.ID)
	if err != nil || !found || access.Frame.IncarnationID != child.IncarnationID ||
		access.RootFrameIncarnationID != "replacement-root-incarnation" {
		t.Fatalf("updated root access=%#v found=%t err=%v", access, found, err)
	}
}
