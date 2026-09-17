package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptDeliveryConvergenceV54PublishedIdentity(t *testing.T) {
	checksum, err := transcriptDeliveryConvergenceV54Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "b73e8f311111ca30df59b3a06605536f6bae38b05759ec0e90b9b7f19a130897"
	if checksum != published {
		t.Fatalf("published v54 checksum changed: got %s want %s", checksum, published)
	}
}

func TestTranscriptDeliveryConvergenceV54RepairsOnlyKnownProjectionFailures(t *testing.T) {
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
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 53); err != nil {
		t.Fatal(err)
	}
	repo := transcriptstore.NewRepository(db)
	seedFailed := func(streamUID, eventType string) {
		t.Helper()
		if _, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
			UID: streamUID, OwnerID: "owner-v54", ExternalID: "external-" + streamUID,
			Kind: transcriptstore.StreamKindStandalone, Epoch: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if _, created, err := repo.AppendUserEvent(ctx, transcriptstore.AppendUserEventInput{
			StreamUID: streamUID, OwnerID: "owner-v54", ClientMessageID: "message-" + streamUID,
			PayloadJSON: []byte(`{"text":"fixture"}`), Destinations: []string{"ws"},
		}); err != nil || !created {
			t.Fatalf("append %s created=%t err=%v", streamUID, created, err)
		}
		if _, err := db.Exec(`UPDATE transcript_events SET event_type=?
			WHERE stream_uid=? AND publication_seq=1`, eventType, streamUID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE transcript_delivery_intents
			SET status='failed',attempt_count=5,last_error_code='projection_failed',
				last_retry_after_ns=1000000000,last_max_attempts=5
			WHERE stream_uid=? AND publication_seq=1 AND destination='ws'`, streamUID); err != nil {
			t.Fatal(err)
		}
	}
	seedFailed("stream-v54-model-only", "user_input_response")
	seedFailed("stream-v54-machine-only", transcriptstore.TerminalToolRecoveryEventType)
	seedFailed("stream-v54-unsupported", "user_message")
	if _, created, err := repo.AppendUserEvent(ctx, transcriptstore.AppendUserEventInput{
		StreamUID: "stream-v54-machine-only", OwnerID: "owner-v54", ClientMessageID: "follower-v54",
		PayloadJSON: []byte(`{"text":"follower"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append follower created=%t err=%v", created, err)
	}

	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 54); err != nil {
		t.Fatal(err)
	}
	for _, streamUID := range []string{"stream-v54-model-only", "stream-v54-machine-only"} {
		var status, code string
		var attempts, maxAttempts int
		if err := db.QueryRow(`SELECT status,attempt_count,last_error_code,last_max_attempts
			FROM transcript_delivery_intents
			WHERE stream_uid=? AND publication_seq=1 AND destination='ws'`, streamUID).
			Scan(&status, &attempts, &code, &maxAttempts); err != nil {
			t.Fatal(err)
		}
		if status != "pending" || attempts != 5 || code != "projection_failed" || maxAttempts != 5 {
			t.Fatalf("repaired %s status=%q attempts=%d code=%q max=%d", streamUID, status, attempts, code, maxAttempts)
		}
	}
	var unsupported string
	if err := db.QueryRow(`SELECT status FROM transcript_delivery_intents
		WHERE stream_uid='stream-v54-unsupported' AND publication_seq=1 AND destination='ws'`).Scan(&unsupported); err != nil {
		t.Fatal(err)
	}
	if unsupported != "failed" {
		t.Fatalf("unsupported projection failure status=%q want failed", unsupported)
	}
	var follower string
	if err := db.QueryRow(`SELECT status FROM transcript_delivery_intents
		WHERE stream_uid='stream-v54-machine-only' AND publication_seq=2 AND destination='ws'`).Scan(&follower); err != nil {
		t.Fatal(err)
	}
	if follower != "pending" {
		t.Fatalf("follower status=%q want pending", follower)
	}
	var indexes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE type='index' AND name='transcript_delivery_order'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("delivery order indexes=%d err=%v", indexes, err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 54); err != nil {
		t.Fatalf("repeat v54 migration: %v", err)
	}
}
