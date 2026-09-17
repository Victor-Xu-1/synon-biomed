package workspace

import (
	"context"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptHistoryActivationV31BackfillsLatestFrameAuthority(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for _, version := range []int{23, 24, 25, 26, 27, 28, 29, 30} {
		applyTranscriptMigration(t, db, version)
	}
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
		VALUES('project-authority','owner-a','Authority','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(
		id,project_id,parent_frame_id,root_frame_id,root_sequence,agent_name,status,conversation_type,name,created_at,updated_at)
		VALUES('frame-authority','project-authority',NULL,'frame-authority',0,'OPERON','processing','agent','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	for epoch := 1; epoch <= 2; epoch++ {
		if _, err := db.Exec(`INSERT INTO transcript_streams(
			stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,
			created_at,updated_at) VALUES(?,?,?,?, 'frame_ref','project-authority','frame-authority','frame-authority',?,?,?)`,
			"stream-authority-"+string(rune('0'+epoch)), "owner-a", "frame:frame-authority", "frame-authority", epoch, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 31); err != nil {
		t.Fatal(err)
	}
	var streamUID, readAuthority, writeAuthority string
	var epoch, generation int64
	var activationID []byte
	if err := db.QueryRow(`SELECT active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id FROM transcript_frame_authority
		WHERE owner_id='owner-a' AND session_id='frame-authority'`).Scan(
		&streamUID, &epoch, &generation, &readAuthority, &writeAuthority, &activationID,
	); err != nil {
		t.Fatal(err)
	}
	if streamUID != "stream-authority-2" || epoch != 2 || generation != 1 ||
		readAuthority != "legacy_mixed_v1" || writeAuthority != "legacy_frame_ref_v1" || activationID != nil {
		t.Fatalf("authority stream=%q epoch=%d generation=%d read=%q write=%q activation=%x",
			streamUID, epoch, generation, readAuthority, writeAuthority, activationID)
	}
	if _, err := db.Exec(`UPDATE transcript_frame_authority SET active_stream_uid='stream-authority-1',
		active_epoch=1,authority_generation=2 WHERE owner_id='owner-a' AND session_id='frame-authority'`); err == nil {
		t.Fatal("legacy frame authority moved without an activation receipt")
	}
}

func TestTranscriptHistoryActivationV31RejectsFrameOwnerMismatchAndRollsBack(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for _, version := range []int{23, 24, 25, 26, 27, 28, 29, 30} {
		applyTranscriptMigration(t, db, version)
	}
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
		VALUES('project-foreign','owner-b','Foreign','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(
		id,project_id,parent_frame_id,root_frame_id,root_sequence,agent_name,status,conversation_type,name,created_at,updated_at)
		VALUES('frame-foreign','project-foreign',NULL,'frame-foreign',0,'OPERON','processing','agent','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_streams(
		stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,created_at,updated_at)
		VALUES('stream-foreign','owner-a','frame:frame-foreign','frame-foreign','frame_ref',
		'project-foreign','frame-foreign','frame-foreign',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 31)
	if err == nil || !strings.Contains(err.Error(), "authority is inconsistent") {
		t.Fatalf("owner mismatch migration error=%v", err)
	}
	assertSchemaJournalVersion(t, db, 30)
	for _, object := range transcriptstore.HistoryActivationV31ObjectNames() {
		var count int
		if queryErr := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); queryErr != nil || count != 0 {
			t.Fatalf("rolled-back object %s count=%d err=%v", object, count, queryErr)
		}
	}
}

func TestTranscriptHistoryActivationV31MigrationIdentity(t *testing.T) {
	checksum, err := transcriptHistoryActivationV31Migration.validatedChecksum()
	want := "b98e166a95c3fdcd65e36d99593eb97ac0c51adb0faefb8173dd1b626c0e09bb"
	if err != nil || checksum != want {
		t.Fatalf("v31 checksum=%q want=%q err=%v", checksum, want, err)
	}
}

func TestTranscriptHistoryActivationV31UpgradeReopenAndContract(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 31; version++ {
		if version == 26 || version == 28 {
			applyTranscriptMigration(t, db, version)
			continue
		}
		if version == 23 || version == 24 || version == 25 || version == 27 || version >= 29 {
			applyTranscriptMigration(t, db, version)
		}
	}
	applyTranscriptMigration(t, db, 31)
	assertSchemaJournalVersion(t, db, 31)
	for _, object := range transcriptstore.HistoryActivationV31ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v31 object %s count=%d err=%v", object, count, err)
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
		t.Fatalf("reopened v31 contract: %v", err)
	}
}

func TestTranscriptHistoryActivationV31RejectsPredecessorDriftAndEveryCollision(t *testing.T) {
	t.Run("v30 drift", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for _, version := range []int{23, 24, 25, 26, 27, 28, 29, 30} {
			applyTranscriptMigration(t, db, version)
		}
		if _, err := db.Exec(`DROP INDEX transcript_history_cutover_ready;
			CREATE INDEX transcript_history_cutover_ready
			ON transcript_history_cutover_runs(backfill_id,status,cutover_id)`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 31)
		if err == nil || !strings.Contains(err.Error(), "v30 cohort identity mismatch") {
			t.Fatalf("v30 drift error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 30)
	})

	for _, object := range transcriptstore.HistoryActivationV31ObjectNames() {
		t.Run(object, func(t *testing.T) {
			db, _ := openTargetTwentyTwoProviderDB(t)
			for _, version := range []int{23, 24, 25, 26, 27, 28, 29, 30} {
				applyTranscriptMigration(t, db, version)
			}
			statement := `CREATE TABLE ` + object + `(id TEXT)`
			if strings.HasSuffix(object, "_owner") {
				statement = `CREATE INDEX ` + object + ` ON frames(id)`
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 31)
			if err == nil || !strings.Contains(err.Error(), "cohort is polluted") {
				t.Fatalf("collision error=%v", err)
			}
			assertSchemaJournalVersion(t, db, 30)
		})
	}
}

func TestTranscriptHistoryActivationV31RollsBackPartialDDL(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for _, version := range []int{23, 24, 25, 26, 27, 28, 29, 30} {
		applyTranscriptMigration(t, db, version)
	}
	migration := transcriptHistoryActivationV31Migration
	migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE frames(id TEXT)`)
	if err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "faulted-v31"); err == nil {
		t.Fatal("faulted v31 migration succeeded")
	}
	for _, object := range transcriptstore.HistoryActivationV31ObjectNames() {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rolled-back object %s count=%d err=%v", object, count, err)
		}
	}
	assertSchemaJournalVersion(t, db, 30)
}
