package workspace

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompatibilityCrossReferencesAndConversationMoveUseV11Relations(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []CreateProjectInput{
		{ID: "source", UserID: "local", Name: "Source"},
		{ID: "target", UserID: "local", Name: "Target"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	root, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "source", AgentName: "OPERON", Status: "cancelled", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(CreateFrameInput{
		ID: "child", ProjectID: "source", ParentFrameID: root.ID,
		AgentName: "RESEARCH", Status: "failed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	external, err := store.CreateFrame(CreateFrameInput{
		ID: "external", ProjectID: "source", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	statements := []string{
		`INSERT INTO artifact_folders (id, project_id, name, root_frame_id, created_at, updated_at) VALUES ('target-folder', 'target', 'Results', NULL, ?, ?)`,
		`INSERT INTO artifact_folders (id, project_id, name, root_frame_id, created_at, updated_at) VALUES ('root-folder', 'source', 'Results', 'root', ?, ?)`,
		`INSERT INTO artifact_folders (id, project_id, parent_id, name, root_frame_id, created_at, updated_at) VALUES ('child-folder', 'source', 'root-folder', 'Child', 'root', ?, ?)`,
		`INSERT INTO artifact_folders (id, project_id, name, created_at, updated_at) VALUES ('external-folder', 'source', 'External', ?, ?)`,
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement, now, now); err != nil {
			t.Fatal(err)
		}
	}
	insertArtifactFixture(t, store, "internal-a", "source", "Internal A", "root", root.ID, "root-folder", now)
	insertArtifactFixture(t, store, "internal-b", "source", "Internal B", "root", child.ID, "external-folder", now)
	insertArtifactFixture(t, store, "external-a", "source", "External A", external.ID, external.ID, "external-folder", now)
	if _, err := store.db.Exec(`
		INSERT INTO artifact_versions
		(id, artifact_id, version_number, content, content_sha256, created_at)
		VALUES ('version-a', 'internal-a', 1, X'01', 'sha-a', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO annotations
		(id, project_id, target_kind, target_key, label_idx, body, created_at)
		VALUES ('annotation-artifact', 'source', 'artifact', 'internal-a', 0, 'artifact note', ?),
		       ('annotation-version', 'source', 'artifact', 'av:version-a', 0, 'version note', ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO notes
		(id, project_id, user_id, target_type, target_frame_id, content, created_at, updated_at)
		VALUES ('note-root', 'source', 'local', 'frame', 'root', 'root note', ?, ?),
		       ('note-child', 'source', 'local', 'frame', 'child', 'child note', ?, ?),
		       ('note-external', 'source', 'local', 'frame', 'external', 'external note', ?, ?)`,
		now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameMentionedArtifactIDs(root.ID, []string{"external-a", "internal-a", "external-a", "missing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE frame_runtime_metadata SET mentioned_artifact_ids = ? WHERE frame_id = ?`,
		`[{"artifact_id":"external-a"},"internal-b"]`, child.ID); err != nil {
		t.Fatal(err)
	}

	references, err := store.ListCrossSessionArtifactRefs(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 || references[0].ArtifactID != "external-a" || references[0].Filename != "External A" {
		t.Fatalf("cross-session references = %#v", references)
	}

	result, err := store.MoveCompatibilityConversationToProject(root.ID, "target")
	if err != nil {
		t.Fatal(err)
	}
	if result.RootFrameID != root.ID || result.FromProjectID != "source" || result.ToProjectID != "target" ||
		result.FramesMoved != 2 || result.ArtifactsMoved != 2 || result.FoldersMoved != 2 || result.NotesMoved != 2 || result.Event == nil {
		t.Fatalf("move result = %#v", result)
	}
	assertProjectID(t, store, "frames", "id", root.ID, "target")
	assertProjectID(t, store, "frames", "id", child.ID, "target")
	assertProjectID(t, store, "frames", "id", external.ID, "source")
	assertProjectID(t, store, "artifacts", "id", "internal-a", "target")
	assertProjectID(t, store, "artifacts", "id", "internal-b", "target")
	assertProjectID(t, store, "artifacts", "id", "external-a", "source")
	assertProjectID(t, store, "notes", "id", "note-root", "target")
	assertProjectID(t, store, "notes", "id", "note-child", "target")
	assertProjectID(t, store, "notes", "id", "note-external", "source")
	assertProjectID(t, store, "annotations", "id", "annotation-artifact", "target")
	assertProjectID(t, store, "annotations", "id", "annotation-version", "target")

	var movedRootName, movedRootParent, movedChildParent string
	if err := store.db.QueryRow(`SELECT name, COALESCE(parent_id, '') FROM artifact_folders WHERE id = 'root-folder'`).Scan(&movedRootName, &movedRootParent); err != nil {
		t.Fatal(err)
	}
	if movedRootName != "Results (moved root)" || movedRootParent != "" {
		t.Fatalf("moved root folder name=%q parent=%q", movedRootName, movedRootParent)
	}
	if err := store.db.QueryRow(`SELECT COALESCE(parent_id, '') FROM artifact_folders WHERE id = 'child-folder'`).Scan(&movedChildParent); err != nil {
		t.Fatal(err)
	}
	if movedChildParent != "root-folder" {
		t.Fatalf("moved child folder parent = %q", movedChildParent)
	}
	var internalAFolder, internalBFolder string
	if err := store.db.QueryRow(`SELECT COALESCE(folder_id, '') FROM artifacts WHERE id = 'internal-a'`).Scan(&internalAFolder); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COALESCE(folder_id, '') FROM artifacts WHERE id = 'internal-b'`).Scan(&internalBFolder); err != nil {
		t.Fatal(err)
	}
	if internalAFolder != "root-folder" || internalBFolder != "" {
		t.Fatalf("moved artifact folders internal-a=%q internal-b=%q", internalAFolder, internalBFolder)
	}
	afterMoveReferences, err := store.ListCrossSessionArtifactRefs(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterMoveReferences) != 0 {
		t.Fatalf("post-move cross references = %#v", afterMoveReferences)
	}
	idempotent, err := store.MoveCompatibilityConversationToProject(root.ID, "target")
	if err != nil {
		t.Fatal(err)
	}
	if idempotent.FramesMoved != 0 || idempotent.ArtifactsMoved != 0 || idempotent.Event != nil || idempotent.FromProjectID != "target" {
		t.Fatalf("idempotent move = %#v", idempotent)
	}
}

func TestCompatibilityConversationMoveRejectsActiveAndSubframes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []CreateProjectInput{{ID: "source", Name: "Source"}, {ID: "target", Name: "Target"}} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	root, err := store.CreateFrame(CreateFrameInput{ID: "root", ProjectID: "source", AgentName: "OPERON", Status: "processing", ConversationType: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(CreateFrameInput{ID: "child", ProjectID: "source", ParentFrameID: root.ID, AgentName: "RESEARCH", Status: "failed", ConversationType: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveCompatibilityConversationToProject(root.ID, "target"); err == nil || !strings.Contains(err.Error(), "cannot move session") {
		t.Fatalf("active root move error = %v", err)
	}
	status := "cancelled"
	if _, err := store.UpdateFrame(root.ID, UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveCompatibilityConversationToProject(child.ID, "target"); err == nil || !strings.Contains(err.Error(), "not a conversation root") {
		t.Fatalf("subframe move error = %v", err)
	}
	assertProjectID(t, store, "frames", "id", root.ID, "source")
	assertProjectID(t, store, "frames", "id", child.ID, "source")
}

func insertArtifactFixture(t *testing.T, store *Store, id, projectID, name, rootFrameID, frameID, folderID string, now time.Time) {
	t.Helper()
	if _, err := store.db.Exec(`
		INSERT INTO artifacts
		(id, project_id, name, kind, folder_id, created_at, updated_at)
		VALUES (?, ?, ?, 'file', NULLIF(?, ''), ?, ?)`, id, projectID, name, folderID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO artifact_runtime_metadata (artifact_id, root_frame_id, frame_id)
		VALUES (?, ?, ?)`, id, rootFrameID, frameID); err != nil {
		t.Fatal(err)
	}
}

func assertProjectID(t *testing.T, store *Store, table, keyColumn, id, want string) {
	t.Helper()
	var got string
	query := `SELECT project_id FROM ` + table + ` WHERE ` + keyColumn + ` = ?`
	if err := store.db.QueryRow(query, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s %s project_id = %q, want %q", table, id, got, want)
	}
}
