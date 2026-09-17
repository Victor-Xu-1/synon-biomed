package workspace

import (
	"path/filepath"
	"testing"
)

func TestCarryForwardArtifactAnnotationsRelocatesAndClearsSelections(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []CreateAnnotationInput{
		{
			ProjectID: "project-1", TargetKind: "artifact", TargetKey: "av:source",
			Body: map[string]any{
				"type": "text_selection", "text": "move", "selection_text": "needle",
				"selection_prefix": "prefix unique", "start_line": 1, "start_col": 2,
				"end_line": 1, "end_col": 8,
			},
		},
		{
			ProjectID: "project-1", TargetKind: "artifact", TargetKey: "av:source",
			Body: map[string]any{
				"type": "text_selection", "text": "missing", "selection_text": "not present",
				"start_line": 1, "start_col": 1, "end_line": 1, "end_col": 3,
			},
		},
	} {
		if _, err := store.CreateAnnotation(input); err != nil {
			t.Fatal(err)
		}
	}
	content := "needle\nother\nprefix unique\nneedle\n"
	carried, err := store.CarryForwardArtifactAnnotations(
		"project-1", "av:source", "av:target", "new-checksum", content,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(carried) != 2 {
		t.Fatalf("carried annotations = %#v", carried)
	}
	if carried[0].Body["start_line"] != 4 || carried[0].Body["end_line"] != 4 {
		t.Fatalf("relocated selection = %#v", carried[0].Body)
	}
	for _, field := range []string{"start_line", "start_col", "end_line", "end_col"} {
		if carried[1].Body[field] != nil {
			t.Fatalf("missing selection retained %s: %#v", field, carried[1].Body)
		}
	}
	stored, err := store.ListAnnotations("project-1", "av:target")
	if err != nil || len(stored) != 2 || stored[0].Body["start_line"] != float64(4) {
		t.Fatalf("stored relocation = %#v err=%v", stored, err)
	}
}
