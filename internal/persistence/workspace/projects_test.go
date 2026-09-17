package workspace

import (
	"path/filepath"
	"testing"
)

func TestProjectReadUpdateAndDelete(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Original", Path: "/workspace/original"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	name := "Renamed"
	project, err := store.UpdateProject("project-1", UpdateProjectInput{Name: &name})
	if err != nil {
		t.Fatalf("update project: %v", err)
	}
	if project.Name != name || project.Path != "/workspace/original" {
		t.Fatalf("updated project = %#v", project)
	}
	if err := store.DeleteProject(project.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, ok, err := store.GetProject(project.ID); err != nil || ok {
		t.Fatalf("project after delete = ok:%v err:%v", ok, err)
	}
}
