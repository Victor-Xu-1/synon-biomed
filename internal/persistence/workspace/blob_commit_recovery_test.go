package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestOpenRecoversCommittedArtifactBlobBeforeReferencedBlobAudit(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-recovery", UserID: "owner-a", Name: "Recovery"}); err != nil {
		t.Fatal(err)
	}
	content := []byte("committed metadata, pending final rename")
	digest := sha256.Sum256(content)
	digestHex := hex.EncodeToString(digest[:])
	stagingDir := filepath.Join(store.blobRoot, "staging")
	if err := ensurePrivateDirectory(stagingDir); err != nil {
		t.Fatal(err)
	}
	stagingAbsolute := filepath.Join(stagingDir, ".artifact-write-crash-window")
	if err := os.WriteFile(stagingAbsolute, content, 0o600); err != nil {
		t.Fatal(err)
	}
	stagingPath, err := store.blobRelative(stagingAbsolute)
	if err != nil {
		t.Fatal(err)
	}
	finalPath := artifactVersionBlobPath("version-recovery")
	now := time.Now().UTC()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO artifacts (id, project_id, name, kind, current_version_number, created_at, updated_at)
		VALUES ('artifact-recovery', 'project-recovery', 'recovery.txt', 'text/plain', 1, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO artifact_versions
		(id, artifact_id, version_number, content, content_sha256, storage_path, size_bytes, created_at)
		VALUES ('version-recovery', 'artifact-recovery', 1, X'', ?, ?, ?, ?)`, digestHex, finalPath, len(content), now); err != nil {
		t.Fatal(err)
	}
	marker := blobCommitMarker{ID: "artifact-version:version-recovery", Kind: "artifact_version",
		AggregateID: "version-recovery", StagingPath: stagingPath, FinalPath: finalPath,
		SHA256: digestHex, SizeBytes: int64(len(content)), CreatedAt: now}
	if err := store.insertBlobCommitMarkerTx(context.Background(), tx, marker); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	finalAbsolute, err := store.blobAbsolute(finalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(finalAbsolute); !os.IsNotExist(err) {
		t.Fatalf("final blob unexpectedly exists before recovery: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatalf("Open must recover committed marker before missing-reference audit: %v", err)
	}
	defer reopened.Close()
	_, _, reader, found, err := reopened.OpenCurrentArtifactContent("artifact-recovery")
	if err != nil || !found {
		t.Fatalf("recovered content found=%v err=%v", found, err)
	}
	defer reader.Close()
	buffer := new(bytes.Buffer)
	if _, err := buffer.ReadFrom(reader); err != nil || !bytes.Equal(buffer.Bytes(), content) {
		t.Fatalf("recovered content=%q err=%v", buffer.Bytes(), err)
	}
	var markers int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM blob_commit_markers`).Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("remaining markers=%d err=%v", markers, err)
	}
	if _, err := os.Stat(stagingAbsolute); !os.IsNotExist(err) {
		t.Fatalf("staging file survived recovery: %v", err)
	}
}

func TestOpenFinishesCommitUnknownAfterRenameBeforeMarkerAck(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("already renamed")
	digest := sha256.Sum256(content)
	digestHex := hex.EncodeToString(digest[:])
	stagingAbsolute := filepath.Join(store.blobRoot, "staging", ".part-commit-unknown")
	if err := ensurePrivateDirectory(filepath.Dir(stagingAbsolute)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagingAbsolute, content, 0o600); err != nil {
		t.Fatal(err)
	}
	stagingPath, _ := store.blobRelative(stagingAbsolute)
	finalPath := attachmentBlobPath("attachment-commit-unknown")
	finalAbsolute, _ := store.blobAbsolute(finalPath)
	if err := ensurePrivateDirectory(filepath.Dir(finalAbsolute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stagingAbsolute, finalAbsolute); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.db.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at)
		VALUES ('project-attachment-recovery', 'owner-a', 'P', '', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO attachments
		(id, user_id, project_id, filename, content_type, size_bytes, sha256, blob_path, ephemeral, created_at, updated_at)
		VALUES ('attachment-commit-unknown', 'owner-a', 'project-attachment-recovery', 'a.txt', 'text/plain', ?, ?, ?, 0, ?, ?)`,
		len(content), digestHex, finalPath, now, now); err != nil {
		t.Fatal(err)
	}
	marker := blobCommitMarker{ID: "attachment:attachment-commit-unknown", Kind: "attachment",
		AggregateID: "attachment-commit-unknown", StagingPath: stagingPath, FinalPath: finalPath,
		SHA256: digestHex, SizeBytes: int64(len(content)), CreatedAt: now}
	if err := store.insertBlobCommitMarkerTx(context.Background(), tx, marker); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	attachment, file, found, err := reopened.OpenAttachment(context.Background(), "owner-a", "attachment-commit-unknown")
	if err != nil || !found || attachment.SHA256 != digestHex {
		t.Fatalf("attachment=%#v found=%v err=%v", attachment, found, err)
	}
	_ = file.Close()
}

func TestScientificSubmissionBlobMarkerSharesImmediateTransactionAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := []byte("immutable scientific submission archive")
	digest := sha256.Sum256(content)
	digestHex := hex.EncodeToString(digest[:])
	stagingAbsolute := filepath.Join(store.blobRoot, "staging", ".scientific-submission-job-a")
	if err := ensurePrivateDirectory(filepath.Dir(stagingAbsolute)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagingAbsolute, content, 0o600); err != nil {
		t.Fatal(err)
	}
	stagingPath, err := store.blobRelative(stagingAbsolute)
	if err != nil {
		t.Fatal(err)
	}
	marker := blobCommitMarker{
		ID: "scientific-submission:job-a", Kind: "scientific_submission", AggregateID: "job-a",
		StagingPath: stagingPath, FinalPath: "scientific-submissions/job-a/archive.tar.gz",
		SHA256: digestHex, SizeBytes: int64(len(content)), CreatedAt: time.Now().UTC(),
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		return store.insertBlobCommitMarkerTx(context.Background(), tx, marker)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.finalizeBlobCommit(context.Background(), marker); err != nil {
		t.Fatal(err)
	}
	finalAbsolute, err := store.blobAbsolute(marker.FinalPath)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := validateCommittedBlob(finalAbsolute, marker.SizeBytes, marker.SHA256)
	if err != nil || !valid {
		t.Fatalf("scientific submission archive valid=%v err=%v", valid, err)
	}
}
