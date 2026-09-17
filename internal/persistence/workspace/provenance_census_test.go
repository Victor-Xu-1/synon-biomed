package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestGetProvenanceCensusReproducesV11CoverageAndIntegrity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.now = func() time.Time {
		return time.Date(2026, time.July, 16, 8, 9, 10, 0, time.UTC)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}

	managed := saveProvenanceCensusArtifact(t, store, "managed", "analysis.py")
	upload := saveProvenanceCensusArtifact(t, store, "upload", "input.csv")
	reference := saveProvenanceCensusArtifact(t, store, "reference", "reference.json")
	if _, err := store.db.Exec(`
		INSERT INTO artifact_runtime_metadata (artifact_id, latest_version_id, is_user_upload)
		VALUES ('upload', ?, 1)`, upload.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE artifact_versions SET storage_path = '~/reference/reference.json' WHERE id = ?`, reference.ID); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		versionID         string
		producingCellID   any
		extractedCode     any
		dependencyMapping string
	}{
		{managed.ID, "cell-1", "print('managed')", `{"inputs":[]}`},
		{upload.ID, nil, nil, `{"inputs":[{"version_id":"` + reference.ID + `"}],"outputs":[]}`},
		{reference.ID, nil, nil, `{"mapping_status":"pending","mapping_attempts":2}`},
	} {
		if _, err := store.db.Exec(`
			INSERT INTO artifact_version_provenance
				(version_id, content_type, producing_cell_id, extracted_code, dependency_mappings)
			VALUES (?, 'application/octet-stream', ?, ?, ?)`,
			row.versionID, row.producingCellID, row.extractedCode, row.dependencyMapping,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`
		INSERT INTO artifact_version_dependencies
			(id, artifact_version_id, depends_on_version_id, reference_name, created_at)
		VALUES ('dependency', ?, ?, 'reference', CURRENT_TIMESTAMP)`, managed.ID, reference.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE artifact_versions
		SET created_at = CASE
			WHEN id = ? THEN '2025-01-01T00:00:00Z'
			ELSE '2025-02-01T00:00:00Z'
		END`, managed.ID); err != nil {
		t.Fatal(err)
	}

	splitAt := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	result, err := store.GetProvenanceCensus(context.Background(), ProvenanceCensusOptions{
		DetectionWindowDays: 12.5,
		SplitAt:             &splitAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	classes := provenanceClassesByName(result.Classes)
	assertProvenanceClass(t, classes["managed"], ProvenanceCensusClass{
		Class: "managed", N: 1, WithProducingCell: 1, WithExtractedCode: 1,
		WithDependencyEdges: 1, WithRealChecksum: 1,
	})
	assertProvenanceClass(t, classes["upload"], ProvenanceCensusClass{
		Class: "upload", N: 1, WithRealChecksum: 1,
	})
	assertProvenanceClass(t, classes["reference"], ProvenanceCensusClass{
		Class: "reference", N: 1, WithRealChecksum: 1,
	})
	if result.PendingMappings.Total != 1 || result.PendingMappings.ByAttempts["2"] != 1 {
		t.Fatalf("pending mappings = %#v", result.PendingMappings)
	}
	if result.TornFinalRows != 1 || result.OrphanEdgeRows != 1 {
		t.Fatalf("integrity torn=%d orphan=%d", result.TornFinalRows, result.OrphanEdgeRows)
	}
	if result.ComputedAt != "2026-07-16T08:09:10Z" || result.DurationMS < 0 {
		t.Fatalf("timing computed=%q duration=%d", result.ComputedAt, result.DurationMS)
	}
	if result.Detection.WindowDays != 12.5 || result.Detection.Mix == nil || len(result.Detection.Mix) != 0 {
		t.Fatalf("detection = %#v", result.Detection)
	}
	if result.Split == nil || result.Split.At != splitAt.Format(time.RFC3339Nano) {
		t.Fatalf("split = %#v", result.Split)
	}
	pre := provenanceClassesByName(result.Split.Pre)
	post := provenanceClassesByName(result.Split.Post)
	if pre["managed"].N != 1 || post["upload"].N != 1 || post["reference"].N != 1 {
		t.Fatalf("split pre=%#v post=%#v", pre, post)
	}

	if _, err := store.db.Exec(`UPDATE artifact_version_provenance SET dependency_mappings = '{invalid' WHERE version_id = ?`, reference.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProvenanceCensus(context.Background(), ProvenanceCensusOptions{}); err != nil {
		t.Fatalf("invalid legacy JSON must not crash the census: %v", err)
	}
}

func TestGetProvenanceCensusHonorsCancellationAndNormalizesWindow(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.GetProvenanceCensus(ctx, ProvenanceCensusOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled census error = %v", err)
	}
	result, err := store.GetProvenanceCensus(context.Background(), ProvenanceCensusOptions{DetectionWindowDays: 999})
	if err != nil {
		t.Fatal(err)
	}
	if result.Detection.WindowDays != 365 || result.Classes == nil || result.PendingMappings.ByAttempts == nil {
		t.Fatalf("normalized empty census = %#v", result)
	}
}

func saveProvenanceCensusArtifact(t *testing.T, store *Store, artifactID, name string) ArtifactVersion {
	t.Helper()
	_, version, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: "project", Name: name,
		Kind: "application/octet-stream", Content: []byte(artifactID), CreatedBy: "local",
	})
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func provenanceClassesByName(classes []ProvenanceCensusClass) map[string]ProvenanceCensusClass {
	result := make(map[string]ProvenanceCensusClass, len(classes))
	for _, class := range classes {
		result[class.Class] = class
	}
	return result
}

func assertProvenanceClass(t *testing.T, actual, expected ProvenanceCensusClass) {
	t.Helper()
	if actual != expected {
		t.Fatalf("provenance class = %#v, want %#v", actual, expected)
	}
}
