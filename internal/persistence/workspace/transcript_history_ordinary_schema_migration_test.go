package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptHistoryOrdinaryV33InstallsFromV32WithoutChangingPriorJournal(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 32; version++ {
		applyTranscriptMigration(t, db, version)
	}
	var beforeV32Name, beforeV32Checksum string
	if err := db.QueryRow(`SELECT name,checksum FROM workspace_schema_migrations WHERE version=32`).
		Scan(&beforeV32Name, &beforeV32Checksum); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 33); err != nil {
		t.Fatal(err)
	}
	var afterV32Name, afterV32Checksum string
	if err := db.QueryRow(`SELECT name,checksum FROM workspace_schema_migrations WHERE version=32`).
		Scan(&afterV32Name, &afterV32Checksum); err != nil {
		t.Fatal(err)
	}
	if beforeV32Name != afterV32Name || beforeV32Checksum != afterV32Checksum {
		t.Fatalf("v32 identity changed name=%q/%q checksum=%q/%q",
			beforeV32Name, afterV32Name, beforeV32Checksum, afterV32Checksum)
	}
	for _, object := range []string{
		"transcript_history_ordinary_cutover_runs",
		"transcript_history_ordinary_cursor_map",
		"transcript_history_activation_freeze_ordinary",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("object %s count=%d err=%v", object, count, err)
		}
	}
	var provenanceColumns, ordinaryColumns, violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('transcript_history_activation_receipts')
		WHERE name='provenance_kind'`).Scan(&provenanceColumns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('transcript_history_activation_receipts')
		WHERE name='ordinary_cutover_id'`).Scan(&ordinaryColumns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if provenanceColumns != 1 || ordinaryColumns != 1 || violations != 0 {
		t.Fatalf("provenance=%d ordinary=%d violations=%d", provenanceColumns, ordinaryColumns, violations)
	}
	assertSchemaJournalVersion(t, db, 33)
}

func TestTranscriptHistoryOrdinaryV33RejectsV31DriftAndRollsBackRebuild(t *testing.T) {
	t.Run("v31 drift", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for version := 23; version <= 32; version++ {
			applyTranscriptMigration(t, db, version)
		}
		if _, err := db.Exec(`DROP TRIGGER transcript_history_activation_validate;
			CREATE TRIGGER transcript_history_activation_validate
			BEFORE INSERT ON transcript_history_activation_receipts BEGIN SELECT 1; END`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 33)
		if err == nil || !strings.Contains(err.Error(), "activation v31 cohort identity mismatch") {
			t.Fatalf("v31 drift error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 32)
	})

	t.Run("rebuild rollback", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for version := 23; version <= 32; version++ {
			applyTranscriptMigration(t, db, version)
		}
		migration := transcriptHistoryOrdinaryV33Migration
		migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE frames(id TEXT)`)
		if err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "faulted-v33"); err == nil {
			t.Fatal("faulted v33 migration succeeded")
		}
		if err := preflightTranscriptHistoryOrdinaryV33(context.Background(), db); err != nil {
			t.Fatalf("v32 cohort was not restored: %v", err)
		}
		for _, object := range []string{
			"transcript_history_ordinary_cutover_runs",
			"transcript_history_ordinary_cursor_map",
			"transcript_history_activation_receipts_v33",
			"transcript_history_activation_freeze_ordinary",
			"transcript_history_activation_immutable",
		} {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rolled-back object %s count=%d err=%v", object, count, err)
			}
		}
		var violations int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
			t.Fatalf("foreign key violations=%d err=%v", violations, err)
		}
		assertSchemaJournalVersion(t, db, 32)
	})

	t.Run("real foreign key violation", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for version := 23; version <= 32; version++ {
			applyTranscriptMigration(t, db, version)
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`PRAGMA defer_foreign_keys=ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO transcript_frame_authority(owner_id,session_id,active_stream_uid,
			active_epoch,authority_generation,read_authority,write_authority,activation_id,genesis_id,updated_at)
			VALUES('owner-bad','frame-bad','missing-stream',1,1,
			'legacy_mixed_v1','legacy_frame_ref_v1',NULL,NULL,?)`, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := rebindActivatedFrameAuthorityAfterV33ReceiptRebuild(context.Background(), tx); err == nil ||
			!strings.Contains(err.Error(), "foreign key violations") {
			t.Fatalf("real foreign key violation error=%v", err)
		}
	})
}

func TestTranscriptHistoryOrdinaryV33PreservesActivatedV31Authority(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 31; version++ {
		applyTranscriptMigration(t, db, version)
	}
	fixture := seedActivatedV31PayloadAuthority(t, db)
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 32); err != nil {
		t.Fatal(err)
	}
	var beforeReceipt, beforeAuthority string
	if err := db.QueryRow(`SELECT hex(activation_id)||':'||hex(cutover_id)||':'||source_stream_uid||':'||
		target_stream_uid||':'||hex(materialized_sha256)||':'||authority_generation
		FROM transcript_history_activation_receipts WHERE activation_id=?`, fixture.activationID).Scan(&beforeReceipt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT active_stream_uid||':'||active_epoch||':'||authority_generation||':'||
		read_authority||':'||write_authority||':'||hex(activation_id)||':'||COALESCE(hex(genesis_id),'')
		FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`, fixture.ownerID, fixture.frameID).
		Scan(&beforeAuthority); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 33); err != nil {
		t.Fatal(err)
	}
	var afterReceipt, afterAuthority, provenance string
	var ordinaryID []byte
	if err := db.QueryRow(`SELECT provenance_kind,ordinary_cutover_id,
		hex(activation_id)||':'||hex(cutover_id)||':'||source_stream_uid||':'||
		target_stream_uid||':'||hex(materialized_sha256)||':'||authority_generation
		FROM transcript_history_activation_receipts WHERE activation_id=?`, fixture.activationID).
		Scan(&provenance, &ordinaryID, &afterReceipt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT active_stream_uid||':'||active_epoch||':'||authority_generation||':'||
		read_authority||':'||write_authority||':'||hex(activation_id)||':'||COALESCE(hex(genesis_id),'')
		FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`, fixture.ownerID, fixture.frameID).
		Scan(&afterAuthority); err != nil {
		t.Fatal(err)
	}
	if provenance != "ask_user_v30" || ordinaryID != nil || beforeReceipt != afterReceipt || beforeAuthority != afterAuthority {
		t.Fatalf("provenance=%q ordinary=%x receipt=%q/%q authority=%q/%q",
			provenance, ordinaryID, beforeReceipt, afterReceipt, beforeAuthority, afterAuthority)
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
	repo := transcriptstore.NewRepository(db)
	if err := repo.ValidateContract(context.Background()); err != nil {
		t.Fatalf("v33 contract: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_replayed_v33_remediation
		BEFORE UPDATE ON transcript_frame_authority BEGIN SELECT RAISE(ABORT,'replayed remediation'); END`); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 33); err != nil {
		t.Fatalf("journaled v33 reran remediation: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_replayed_v33_remediation`); err != nil {
		t.Fatal(err)
	}
	assertSchemaJournalVersion(t, db, 33)
}

func TestTranscriptHistoryOrdinaryV33RemediationIdentityIsExact(t *testing.T) {
	if got := schemaMigrationRemediationID(transcriptHistoryOrdinaryV33Migration); got != transcriptHistoryOrdinaryV33RepairID {
		t.Fatalf("v33 remediation id=%q", got)
	}
	mutated := transcriptHistoryOrdinaryV33Migration
	mutated.statements = append(append([]string(nil), mutated.statements...), `SELECT 1`)
	if got := schemaMigrationRemediationID(mutated); got != "" {
		t.Fatalf("mutated v33 matched remediation %q", got)
	}
	status, err := ExpectedSchemaStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Migrations[32].Version != 33 || status.Migrations[32].RemediationID != transcriptHistoryOrdinaryV33RepairID {
		t.Fatalf("v33 expected status=%#v", status.Migrations[32])
	}
}
