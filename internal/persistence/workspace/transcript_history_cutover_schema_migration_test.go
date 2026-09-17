package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptHistoryCutoverV30MigrationIdentity(t *testing.T) {
	checksum, err := transcriptHistoryCutoverV30Migration.validatedChecksum()
	want := "66338dacaf3355a362a7275dfcd170775570ba53219721e115c2c6e42ffdc614"
	if err != nil || checksum != want {
		t.Fatalf("v30 checksum=%q want=%q err=%v", checksum, want, err)
	}
}

func TestTranscriptHistoryCutoverV30UpgradeReopenAndContract(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 29)
	applyTranscriptMigration(t, db, 30)
	applyTranscriptMigration(t, db, 30)
	assertSchemaJournalVersion(t, db, 30)
	for _, object := range transcriptstore.HistoryCutoverV30ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v30 object %s count=%d err=%v", object, count, err)
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
		t.Fatalf("reopened v30 contract: %v", err)
	}
}

func TestTranscriptHistoryCutoverV30RejectsPredecessorDriftAndEveryCollision(t *testing.T) {
	t.Run("v29 drift", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		applyTranscriptMigration(t, db, 29)
		if _, err := db.Exec(`DROP INDEX transcript_history_backfill_stream;
			CREATE INDEX transcript_history_backfill_stream
			ON transcript_history_backfill_runs(stream_uid,backfill_id)`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 30)
		if err == nil || !strings.Contains(err.Error(), "v29 cohort identity mismatch") {
			t.Fatalf("v29 drift error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 29)
	})

	for _, object := range transcriptstore.HistoryCutoverV30ObjectNames() {
		t.Run(object, func(t *testing.T) {
			db, _ := openTargetTwentyTwoProviderDB(t)
			applyTranscriptMigration(t, db, 29)
			statement := `CREATE TABLE ` + object + `(id TEXT)`
			if strings.HasSuffix(object, "_stream") || strings.HasSuffix(object, "_target") {
				statement = `CREATE INDEX ` + object + ` ON frames(id)`
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 30)
			if err == nil || !strings.Contains(err.Error(), "cohort is polluted") {
				t.Fatalf("collision error=%v", err)
			}
			assertSchemaJournalVersion(t, db, 29)
		})
	}
}

func TestTranscriptHistoryCutoverV30RollsBackPartialDDL(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 29)
	migration := transcriptHistoryCutoverV30Migration
	migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE frames(id TEXT)`)
	if err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "faulted-v30"); err == nil {
		t.Fatal("faulted v30 migration succeeded")
	}
	for _, object := range transcriptstore.HistoryCutoverV30ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rolled-back object %s count=%d err=%v", object, count, err)
		}
	}
	assertSchemaJournalVersion(t, db, 29)
}
