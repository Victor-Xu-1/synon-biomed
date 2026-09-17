package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptHistoryBackfillV29MigrationIdentity(t *testing.T) {
	checksum, err := transcriptHistoryBackfillV29Migration.validatedChecksum()
	want := "9252b74d2506794e7ef090b4804a72ed2ddc9fc227c86645affae1ef8bb78c60"
	if err != nil || checksum != want {
		t.Fatalf("v29 checksum=%q want=%q err=%v", checksum, want, err)
	}
}

func TestTranscriptHistoryBackfillV29UpgradeReopenAndContract(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 28)
	applyTranscriptMigration(t, db, 29)
	applyTranscriptMigration(t, db, 29)
	assertSchemaJournalVersion(t, db, 29)
	for _, object := range transcriptstore.HistoryBackfillV29ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v29 object %s count=%d err=%v", object, count, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateContract(context.Background()); err != nil {
		t.Fatalf("reopened v29 contract: %v", err)
	}
}

func TestTranscriptHistoryBackfillV29RejectsPredecessorDriftAndEveryCollision(t *testing.T) {
	t.Run("v27 index drift", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		applyTranscriptMigration(t, db, 28)
		if _, err := db.Exec(`DROP INDEX transcript_history_classification_runs_latest;
			CREATE INDEX transcript_history_classification_runs_latest
			ON transcript_history_classification_runs(stream_uid,branch_id,run_id)`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 29)
		if err == nil || !strings.Contains(err.Error(), "v27 cohort identity mismatch") {
			t.Fatalf("v27 drift error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 28)
	})

	t.Run("v28 index drift", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		applyTranscriptMigration(t, db, 28)
		if _, err := db.Exec(`DROP INDEX notifications_recipient_type_idx;
			CREATE INDEX notifications_recipient_type_idx ON notifications(recipient_frame_id,sequence)`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 29)
		if err == nil || !strings.Contains(err.Error(), "v28 cohort identity mismatch") {
			t.Fatalf("v28 drift error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 28)
	})

	for _, object := range transcriptstore.HistoryBackfillV29ObjectNames() {
		t.Run(object, func(t *testing.T) {
			db, _ := openTargetTwentyTwoProviderDB(t)
			applyTranscriptMigration(t, db, 28)
			statement := `CREATE TABLE ` + object + `(id TEXT)`
			if strings.HasSuffix(object, "_stream") || strings.HasSuffix(object, "_source") {
				statement = `CREATE INDEX ` + object + ` ON frames(id)`
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 29)
			if err == nil || !strings.Contains(err.Error(), "cohort is polluted") {
				t.Fatalf("collision error=%v", err)
			}
			assertSchemaJournalVersion(t, db, 28)
		})
	}
}

func TestTranscriptHistoryBackfillV29RollsBackPartialDDL(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 28)
	migration := transcriptHistoryBackfillV29Migration
	migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE frames(id TEXT)`)
	if err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "faulted-v29"); err == nil {
		t.Fatal("faulted v29 migration succeeded")
	}
	for _, object := range transcriptstore.HistoryBackfillV29ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rolled-back object %s count=%d err=%v", object, count, err)
		}
	}
	assertSchemaJournalVersion(t, db, 28)
}
