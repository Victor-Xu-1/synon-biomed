package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptTypedHistoryBootstrapV35InstallsFromV34AndReopens(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 34; version++ {
		applyTranscriptMigration(t, db, version)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 35); err != nil {
		t.Fatal(err)
	}
	for _, object := range transcriptstore.TypedHistoryBootstrapV35ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("object %s count=%d err=%v", object, count, err)
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
	if err := transcriptstore.NewRepository(db).ValidateContract(context.Background()); err != nil {
		t.Fatalf("v35 contract: %v", err)
	}
	assertSchemaJournalVersion(t, db, 35)
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 35); err != nil {
		t.Fatalf("v35 reopen: %v", err)
	}
}

func TestTranscriptTypedHistoryBootstrapV35RejectsPollutionAndRollsBack(t *testing.T) {
	t.Run("polluted cohort", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for version := 23; version <= 34; version++ {
			applyTranscriptMigration(t, db, version)
		}
		if _, err := db.Exec(`CREATE TABLE transcript_typed_history_bootstrap_receipts(id TEXT)`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 35)
		if err == nil || !strings.Contains(err.Error(), "bootstrap cohort is polluted") {
			t.Fatalf("polluted v35 error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 34)
	})

	t.Run("statement rollback", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for version := 23; version <= 34; version++ {
			applyTranscriptMigration(t, db, version)
		}
		migration := transcriptTypedHistoryBootstrapV35Migration
		migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE frames(id TEXT)`)
		if err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "faulted-v35"); err == nil {
			t.Fatal("faulted v35 migration succeeded")
		}
		for _, object := range transcriptstore.TypedHistoryBootstrapV35ObjectNames() {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rolled-back object %s count=%d err=%v", object, count, err)
			}
		}
		assertSchemaJournalVersion(t, db, 34)
	})

	t.Run("v32 identity drift", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for version := 23; version <= 34; version++ {
			applyTranscriptMigration(t, db, version)
		}
		if _, err := db.Exec(`DROP TRIGGER transcript_payload_genesis_immutable;
			CREATE TRIGGER transcript_payload_genesis_immutable BEFORE UPDATE ON transcript_payload_genesis_receipts
			BEGIN SELECT RAISE(ABORT,'drifted genesis receipt'); END`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 35)
		if err == nil || !strings.Contains(err.Error(), "payload genesis v32 cohort identity mismatch") {
			t.Fatalf("v32 drift error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 34)
	})
}

func TestTranscriptTypedHistoryBootstrapV35CurrentContractRequiresWholeCohort(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= workspaceSchemaVersion; version++ {
		applyTranscriptMigration(t, db, version)
	}
	if err := transcriptstore.NewRepository(db).ValidateCurrentContract(context.Background()); err != nil {
		t.Fatalf("complete current contract: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER transcript_typed_history_bootstrap_delete_active;
		DROP TRIGGER transcript_typed_history_bootstrap_immutable;
		DROP TRIGGER transcript_typed_history_bootstrap_validate;
		DROP TABLE transcript_typed_history_bootstrap_receipts`); err != nil {
		t.Fatal(err)
	}
	if err := transcriptstore.NewRepository(db).ValidateCurrentContract(context.Background()); err == nil {
		t.Fatal("current v35 contract accepted a missing typed bootstrap cohort")
	}
}
