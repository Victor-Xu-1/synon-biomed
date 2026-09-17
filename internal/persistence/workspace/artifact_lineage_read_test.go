package workspace

import (
	"path/filepath"
	"testing"
)

func TestArtifactLineageReadResolvesSnapshotsAndPendingState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, first, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "analysis.py",
		Kind: "text/x-python", Content: []byte("print('one')"), CreatedBy: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO artifact_version_provenance
			(version_id, content_type, extracted_code, lineage_messages, language, dependency_mappings,
			 environment_snapshot, lineage_snapshot_hash, env_snapshot_hash, cell_sources)
		VALUES (?, 'text/x-python', ?, NULL, 'python', ?, NULL, 'messages-hash', 'environment-hash', ?)`,
		first.ID, "print('lineage')", `{"mapping_status":"complete"}`, `[{"kind":"cell","cell_index":4}]`,
	); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []struct {
		hash    string
		content string
	}{
		{hash: "messages-hash", content: `[{"role":"assistant","content":"complete"}]`},
		{hash: "environment-hash", content: `{"python":"3.12"}`},
	} {
		if _, err := store.db.Exec(`INSERT INTO content_snapshots (hash, content, size_bytes, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, snapshot.hash, snapshot.content, len(snapshot.content)); err != nil {
			t.Fatal(err)
		}
	}

	slim, found, err := store.GetArtifactVersionLineageRecord(first.ID, false)
	if err != nil || !found {
		t.Fatalf("slim record found=%v err=%v", found, err)
	}
	if slim.Messages != nil || slim.EnvironmentSnapshot != nil {
		t.Fatalf("unresolved snapshots = %#v", slim)
	}
	if !slim.HasMessages || !slim.HasEnvironment || !slim.HasCellSources || slim.Pending {
		t.Fatalf("unresolved state flags = %#v", slim)
	}

	full, found, err := store.GetArtifactVersionLineageRecord(first.ID, true)
	if err != nil || !found {
		t.Fatalf("full record found=%v err=%v", found, err)
	}
	messages, ok := full.Messages.([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("resolved messages = %#v", full.Messages)
	}
	environment, ok := full.EnvironmentSnapshot.(map[string]any)
	if !ok || environment["python"] != "3.12" {
		t.Fatalf("resolved environment = %#v", full.EnvironmentSnapshot)
	}

	_, second, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "analysis.py",
		Kind: "text/x-python", Content: []byte("print('two')"), CreatedBy: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO artifact_version_provenance (version_id, content_type, language, dependency_mappings)
		VALUES (?, 'text/x-python', 'python', '{"mapping_status":"pending"}')`, second.ID); err != nil {
		t.Fatal(err)
	}
	pending, found, err := store.GetArtifactVersionLineageRecord(second.ID, false)
	if err != nil || !found {
		t.Fatalf("pending record found=%v err=%v", found, err)
	}
	if !pending.Pending {
		t.Fatalf("pending mapping did not set pending=true: %#v", pending)
	}
}
