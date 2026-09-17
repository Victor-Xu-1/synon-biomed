package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptWebProjectorV47UpgradesV1StateWithoutLosingRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now, blobRoot: path + ".blobs"}
	if err := prepareSchemaJournal(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareLegacyWorkspaceSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 46); err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(CreateProjectInput{ID: "project-v47", UserID: "owner-v47", Name: "v47"})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-v47", ProjectID: project.ID, AgentName: "agent", Status: "pending", ConversationType: "execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := transcriptstore.NewRepository(db)
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "stream-v47", OwnerID: "owner-v47", ExternalID: frame.ID, SessionID: frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: project.ID,
		RootFrameID: frame.RootFrameID, FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.GetProjectionSnapshot(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if _, err := db.Exec(`INSERT INTO transcript_web_projection_state(
		stream_uid,branch_id,branch_generation,projector_version,projection_revision,
		through_publication_seq,source_revision,message_count,visible_message_count,
		message_artifact_reference_count,projector_state_json,projector_state_sha256,
		source_chain_sha256,status,last_error_code,updated_at
	) VALUES(?,?,?,1,1,0,0,1,1,0,'{}',?,?,'quarantined','projection_source_conflict',CURRENT_TIMESTAMP)`,
		stream.UID, snapshot.BranchID, snapshot.BranchGeneration, digest, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_web_messages(
		stream_uid,branch_id,ordinal,message_id,client_message_id,visible,visible_index,
		message_json,message_sha256,first_publication_seq,last_publication_seq,updated_at
	) VALUES(?,?,1,'message-v47','client-v47',1,0,'{}',?,1,1,CURRENT_TIMESTAMP)`,
		stream.UID, snapshot.BranchID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_web_message_identities(
		stream_uid,branch_id,identity,message_ordinal,kind
	) VALUES(?,?,?,1,'both')`, stream.UID, snapshot.BranchID, "message-v47"); err != nil {
		t.Fatal(err)
	}

	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 47); err != nil {
		t.Fatal(err)
	}
	var version int
	var status, code string
	if err := db.QueryRow(`SELECT projector_version,status,last_error_code
		FROM transcript_web_projection_state WHERE stream_uid=? AND branch_id=?`, stream.UID, snapshot.BranchID).
		Scan(&version, &status, &code); err != nil {
		t.Fatal(err)
	}
	if version != 1 || status != "quarantined" || code != "projection_source_conflict" {
		t.Fatalf("migrated state version=%d status=%q code=%q", version, status, code)
	}
	var messages, identities int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_web_messages WHERE stream_uid=? AND branch_id=?`,
		stream.UID, snapshot.BranchID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_web_message_identities WHERE stream_uid=? AND branch_id=?`,
		stream.UID, snapshot.BranchID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || identities != 1 {
		t.Fatalf("preserved child rows messages=%d identities=%d", messages, identities)
	}
	if _, err := db.Exec(`UPDATE transcript_web_projection_state SET projector_version=2
		WHERE stream_uid=? AND branch_id=?`, stream.UID, snapshot.BranchID); err != nil {
		t.Fatalf("projector v2 rejected after migration: %v", err)
	}
	if _, err := db.Exec(`UPDATE transcript_web_projection_state SET projector_version=3
		WHERE stream_uid=? AND branch_id=?`, stream.UID, snapshot.BranchID); err == nil {
		t.Fatal("unsupported projector v3 was accepted")
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 47); err != nil {
		t.Fatalf("repeat v47 migration: %v", err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 48); err != nil {
		t.Fatalf("v48 migration: %v", err)
	}
	if _, err := db.Exec(`UPDATE transcript_web_projection_state SET projector_version=3
		WHERE stream_uid=? AND branch_id=?`, stream.UID, snapshot.BranchID); err != nil {
		t.Fatalf("projector v3 rejected after v48 migration: %v", err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 48); err != nil {
		t.Fatalf("repeat v48 migration: %v", err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 49); err != nil {
		t.Fatalf("v49 migration: %v", err)
	}
	if _, err := db.Exec(`UPDATE transcript_web_projection_state SET projector_version=4
		WHERE stream_uid=? AND branch_id=?`, stream.UID, snapshot.BranchID); err != nil {
		t.Fatalf("projector v4 rejected after v49 migration: %v", err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 49); err != nil {
		t.Fatalf("repeat v49 migration: %v", err)
	}
}

func TestTranscriptWebProjectorFreshAndReopenedWorkspaceUsesCurrentVersionConstraint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var tableSQL string
	if err := reopened.db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, "CHECK(projector_versionIN(1,2,3,4,5,6,7,8,9,10,11))") {
		t.Fatalf("fresh projector constraint=%s", tableSQL)
	}
	status, err := reopened.SchemaStatus(context.Background())
	if err != nil || status.CurrentVersion != workspaceSchemaVersion || status.TargetVersion != workspaceSchemaVersion {
		t.Fatalf("reopened schema status=%#v err=%v", status, err)
	}
}
