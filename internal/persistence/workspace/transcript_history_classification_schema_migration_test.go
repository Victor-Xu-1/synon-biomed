package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptHistoryClassificationV27UpgradeReopenAndContract(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 26)
	applyTranscriptMigration(t, db, 27)
	applyTranscriptMigration(t, db, 27)
	assertSchemaJournalVersion(t, db, 27)
	for _, object := range transcriptstore.HistoryClassificationV27ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v27 object %s count=%d err=%v", object, count, err)
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_history_classification_runs`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("startup classified history rows=%d err=%v", rows, err)
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
		t.Fatalf("reopened v27 contract: %v", err)
	}
}

func TestTranscriptHistoryClassificationV27RejectsCohortAndIncarnationDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate string
	}{
		{
			name: "v25 transcript cohort",
			mutate: `DROP INDEX transcript_branch_events_event;
				CREATE INDEX transcript_branch_events_event ON transcript_branch_events(stream_uid,branch_id,event_id)`,
		},
		{
			name: "v26 incarnation predicate",
			mutate: `DROP INDEX frames_incarnation_id_unique;
				CREATE UNIQUE INDEX frames_incarnation_id_unique ON frames(incarnation_id) WHERE incarnation_id IS NOT NULL`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, _ := openTargetTwentyTwoProviderDB(t)
			applyTranscriptMigration(t, db, 26)
			if _, err := db.Exec(test.mutate); err != nil {
				t.Fatal(err)
			}
			err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 27)
			if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
				t.Fatalf("drift error=%v", err)
			}
			assertSchemaJournalVersion(t, db, 26)
		})
	}
}

func TestTranscriptHistoryClassificationV27RejectsEveryTargetCollision(t *testing.T) {
	for _, object := range transcriptstore.HistoryClassificationV27ObjectNames() {
		t.Run(object, func(t *testing.T) {
			db, _ := openTargetTwentyTwoProviderDB(t)
			applyTranscriptMigration(t, db, 26)
			statement := `CREATE TABLE ` + object + `(id TEXT)`
			if strings.HasSuffix(object, "_latest") || strings.HasSuffix(object, "_findings") {
				statement = `CREATE INDEX ` + object + ` ON frames(id)`
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 27)
			if err == nil || !strings.Contains(err.Error(), "cohort is polluted") {
				t.Fatalf("collision error=%v", err)
			}
			assertSchemaJournalVersion(t, db, 26)
		})
	}
}

func TestTranscriptHistoryClassificationV27RollsBackPartialDDL(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 26)
	migration := transcriptHistoryClassificationV27Migration
	migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE frames(id TEXT)`)
	if err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "faulted-v27"); err == nil {
		t.Fatal("faulted v27 migration succeeded")
	}
	for _, object := range transcriptstore.HistoryClassificationV27ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rolled-back object %s count=%d err=%v", object, count, err)
		}
	}
	assertSchemaJournalVersion(t, db, 26)
}
