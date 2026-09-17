package workspace

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptSchemaMigrationsArePhysicallySplit(t *testing.T) {
	if got := len(transcriptstore.SchemaV23Statements()); got != 10 {
		t.Fatalf("v23 statements=%d", got)
	}
	if got := len(transcriptstore.ArtifactV24Statements()); got != 6 {
		t.Fatalf("v24 statements=%d", got)
	}
	if got := len(transcriptstore.BranchV25Statements()); got != 4 {
		t.Fatalf("v25 statements=%d", got)
	}
	for _, statement := range transcriptstore.SchemaV23Statements() {
		if strings.Contains(statement, "artifact") {
			t.Fatalf("artifact DDL leaked into v23: %q", statement)
		}
	}
}

func TestTranscriptSchemaMigrationIdentitiesAreFrozen(t *testing.T) {
	for _, test := range []struct {
		migration versionedSchemaMigration
		want      string
	}{
		{transcriptV23Migration, "c902d9cfa0407a24ab76e19c72ef908365b2b8fa91987a6bb9a6c1d4975b6db4"},
		{transcriptV24Migration, "efc97a53430c86f9439f656178b0f77faadfb37a6c4fcee9c70bddced8bfa62a"},
		{transcriptV25Migration, "94bab905e8c6e7933eb39231fc8a34f835eba22608e5952d785a1e68f2182df7"},
		{transcriptHistoryClassificationV27Migration, "df82b07d2b270776c900b8aba7679254b4003f83fa693042384c8c87706bd49b"},
		{transcriptHistoryBackfillV29Migration, "9252b74d2506794e7ef090b4804a72ed2ddc9fc227c86645affae1ef8bb78c60"},
		{transcriptHistoryCutoverV30Migration, "66338dacaf3355a362a7275dfcd170775570ba53219721e115c2c6e42ffdc614"},
		{transcriptHistoryActivationV31Migration, "b98e166a95c3fdcd65e36d99593eb97ac0c51adb0faefb8173dd1b626c0e09bb"},
		{transcriptPayloadGenesisV32Migration, "2aa998e9a55bbe6af882edbb73b7a880ebbabd5b0ea2c84bd08fa1f29f4d4b67"},
		{transcriptHistoryOrdinaryV33Migration, "d8b401e9f842e2957ef49761659cd0fa2064890fa083d136d780b00763b1c74b"},
		{transcriptHistoryOrdinaryCursorV34Migration, "cf4d4c952e0d709bcba4120b2458b9fdcb4e327fed6bf1e378d30cead121be54"},
		{transcriptTypedHistoryBootstrapV35Migration, "2c59547c8b86a8e203fd4b6f0bf01d634ee4a5b55f7505e5a51bda334c17ca65"},
	} {
		got, err := test.migration.validatedChecksum()
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("migration %d checksum=%s want %s", test.migration.version, got, test.want)
		}
	}
}

func TestTranscriptHistoryActivationV31RejectsNoopCallbackIdentity(t *testing.T) {
	identity := *transcriptHistoryActivationV31Migration.identityV2
	identity.CallbackID = transcriptSchemaNoopCallbackID
	if transcriptSchemaIdentityKnown(identity) {
		t.Fatal("v31 activation migration accepted the noop callback")
	}
}

func TestTranscriptSchemaMigrationsUpgradeV22AndReopen(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 23)
	assertSchemaJournalVersion(t, db, 23)
	var artifactTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='transcript_artifact_refs'`).Scan(&artifactTables); err != nil || artifactTables != 0 {
		t.Fatalf("v23 artifact tables=%d err=%v", artifactTables, err)
	}
	digest, err := transcriptSchemaCohortDigest(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if digest != transcriptV23CohortSHA256 {
		t.Fatalf("v23 cohort digest=%s want %s", digest, transcriptV23CohortSHA256)
	}
	applyTranscriptMigration(t, db, 24)
	applyTranscriptMigration(t, db, 24)
	assertSchemaJournalVersion(t, db, 24)
	digest, err = transcriptSchemaCohortDigest(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if digest != transcriptV24CohortSHA256 {
		t.Fatalf("v24 cohort digest=%s want %s", digest, transcriptV24CohortSHA256)
	}
	seededAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.Exec(`
		INSERT INTO transcript_streams(
			stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,
			input_revision,consumed_input_revision,next_event_id,next_publication_seq,next_checkpoint_sequence,created_at,updated_at
		) VALUES('stream-backfill','owner-backfill','external-backfill','','standalone','','','',1,1,1,2,2,1,?,?)`, seededAt, seededAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO transcript_events(
			stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,payload_json,frame_event_id,created_at
		) VALUES('stream-backfill',1,1,'message-backfill','user_message','payload',NULL,'{"role":"user","text":"backfill"}',NULL,?)`, seededAt); err != nil {
		t.Fatal(err)
	}
	applyTranscriptMigration(t, db, 25)
	applyTranscriptMigration(t, db, 25)
	assertSchemaJournalVersion(t, db, 25)
	var branchID, activeBranchID, kind string
	var generation, ordinal, eventID int64
	if err := db.QueryRow(`
		SELECT branch.branch_id,state.active_branch_id,branch.kind,state.generation,membership.ordinal,membership.event_id
		FROM transcript_branches branch
		JOIN transcript_branch_state state ON state.stream_uid=branch.stream_uid
		JOIN transcript_branch_events membership ON membership.stream_uid=branch.stream_uid AND membership.branch_id=branch.branch_id
		WHERE branch.stream_uid='stream-backfill'`,
	).Scan(&branchID, &activeBranchID, &kind, &generation, &ordinal, &eventID); err != nil {
		t.Fatal(err)
	}
	if len(branchID) != 11 || !strings.HasPrefix(branchID, "br_") || activeBranchID != branchID ||
		kind != "base" || generation != 1 || ordinal != 1 || eventID != 1 {
		t.Fatalf("branch=%q active=%q kind=%q generation=%d membership=%d/%d", branchID, activeBranchID, kind, generation, ordinal, eventID)
	}
	applyTranscriptMigration(t, db, 27)
	applyTranscriptMigration(t, db, 29)
	applyTranscriptMigration(t, db, 30)
	applyTranscriptMigration(t, db, 31)
	applyTranscriptMigration(t, db, 32)
	applyTranscriptMigration(t, db, 33)
	applyTranscriptMigration(t, db, 34)
	applyTranscriptMigration(t, db, 35)
	applyTranscriptMigration(t, db, 36)
	applyTranscriptMigration(t, db, 37)
	applyTranscriptMigration(t, db, 38)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err := transcriptstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptSchemaMigrationsRejectPollutedAdmissionCohorts(t *testing.T) {
	t.Run("v23", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		if _, err := db.Exec(`CREATE TABLE transcript_unknown(id TEXT)`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 23)
		if err == nil || !strings.Contains(err.Error(), "admission cohort is polluted") {
			t.Fatalf("migration error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 22)
	})
	t.Run("v24", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		applyTranscriptMigration(t, db, 23)
		if _, err := db.Exec(`CREATE INDEX transcript_unknown ON transcript_streams(owner_id)`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 24)
		if err == nil || !strings.Contains(err.Error(), "cohort identity mismatch") {
			t.Fatalf("migration error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 23)
	})
	t.Run("v25", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		applyTranscriptMigration(t, db, 24)
		if _, err := db.Exec(`CREATE TABLE transcript_branch_unknown(id TEXT)`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 25)
		if err == nil || !strings.Contains(err.Error(), "v24 cohort identity mismatch") {
			t.Fatalf("migration error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 24)
	})
}

func TestTranscriptSchemaMigrationTransactionsRollbackPartialDDL(t *testing.T) {
	t.Run("v23", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		migration := transcriptV23Migration
		migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE model_providers(id TEXT)`)
		err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "test-checksum")
		if err == nil {
			t.Fatal("faulted v23 migration succeeded")
		}
		assertNoTranscriptObjects(t, db)
		assertSchemaJournalVersion(t, db, 22)
	})
	t.Run("v24", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		applyTranscriptMigration(t, db, 23)
		migration := transcriptV24Migration
		migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE transcript_streams(id TEXT)`)
		err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "test-checksum")
		if err == nil {
			t.Fatal("faulted v24 migration succeeded")
		}
		var artifacts int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE 'transcript_artifact%'`).Scan(&artifacts); err != nil || artifacts != 0 {
			t.Fatalf("rolled-back artifact objects=%d err=%v", artifacts, err)
		}
		assertSchemaJournalVersion(t, db, 23)
	})
	t.Run("v25", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		applyTranscriptMigration(t, db, 24)
		migration := transcriptV25Migration
		migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE transcript_streams(id TEXT)`)
		err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "test-checksum")
		if err == nil {
			t.Fatal("faulted v25 migration succeeded")
		}
		var branches int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE 'transcript_branch%'`).Scan(&branches); err != nil || branches != 0 {
			t.Fatalf("rolled-back branch objects=%d err=%v", branches, err)
		}
		assertSchemaJournalVersion(t, db, 24)
	})
}

func openTargetTwentyTwoProviderDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, path := openTargetTwentyProviderDB(t)
	applySchemaRescueV21(t, db)
	applyProviderV22(t, db)
	return db, path
}

func applyTranscriptMigration(t *testing.T, db *sql.DB, target int) {
	t.Helper()
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, target); err != nil {
		t.Fatal(err)
	}
}

func assertNoTranscriptObjects(t *testing.T, db *sql.DB) {
	t.Helper()
	objects, err := inspectTranscriptSchemaObjects(context.Background(), db)
	if err != nil || len(objects) != 0 {
		t.Fatalf("transcript objects=%d err=%v", len(objects), err)
	}
}
