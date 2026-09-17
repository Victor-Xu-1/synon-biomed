package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptHistoryOrdinaryCursorV34InstallsFromV33(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 33; version++ {
		applyTranscriptMigration(t, db, version)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 34); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"source_generation", "source_through_publication_seq", "source_message_index"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('transcript_history_ordinary_cursor_map')
			WHERE name=?`, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("column %s count=%d err=%v", column, count, err)
		}
	}
	for _, object := range []string{
		"transcript_history_ordinary_cursor_validate",
		"transcript_history_ordinary_cursor_immutable",
		"transcript_history_ordinary_cursor_delete_active",
	} {
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
		t.Fatalf("v34 contract: %v", err)
	}
	assertSchemaJournalVersion(t, db, 34)
}

func TestTranscriptHistoryOrdinaryCursorV34PreservesPlainV33Cursor(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 33; version++ {
		applyTranscriptMigration(t, db, version)
	}
	seedPlainV33OrdinaryCursor(t, db)
	var activationBefore, cursorDigestBefore []byte
	if err := db.QueryRow(`SELECT activation_id,cursor_sha256 FROM transcript_history_activation_receipts
		WHERE provenance_kind='ordinary_v33'`).Scan(&activationBefore, &cursorDigestBefore); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT hex(ordinary_cutover_id)||':'||source_stream_uid||':'||target_stream_uid||':'||
		source_branch_id||':'||target_branch_id||':'||source_ordinal||':'||source_event_id||':'||
		target_event_id||':'||target_publication_seq||':'||target_message_index||':'||stable_message_id
		FROM transcript_history_ordinary_cursor_map`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 34); err != nil {
		t.Fatal(err)
	}
	var after string
	var generation, through, messageIndex int64
	if err := db.QueryRow(`SELECT hex(ordinary_cutover_id)||':'||source_stream_uid||':'||target_stream_uid||':'||
		source_branch_id||':'||target_branch_id||':'||source_ordinal||':'||source_event_id||':'||
		target_event_id||':'||target_publication_seq||':'||target_message_index||':'||stable_message_id,
		source_generation,source_through_publication_seq,source_message_index
		FROM transcript_history_ordinary_cursor_map`).Scan(&after, &generation, &through, &messageIndex); err != nil {
		t.Fatal(err)
	}
	if before != after || generation != 1 || through != 1 || messageIndex != 0 {
		t.Fatalf("cursor before=%q after=%q generation=%d through=%d index=%d",
			before, after, generation, through, messageIndex)
	}
	retry, created, err := transcriptstore.NewRepository(db).ActivateOrdinaryFrameHistory(
		context.Background(), strings.Repeat("0", sha256.Size*2), "source-v33", "owner-v33",
	)
	if err != nil || created || retry.ActivationSHA256 != hex.EncodeToString(activationBefore) {
		t.Fatalf("v33 retry=%#v created=%t err=%v", retry, created, err)
	}
	var cursorDigestAfter []byte
	if err := db.QueryRow(`SELECT cursor_sha256 FROM transcript_history_activation_receipts
		WHERE provenance_kind='ordinary_v33'`).Scan(&cursorDigestAfter); err != nil {
		t.Fatal(err)
	}
	if string(cursorDigestAfter) != string(cursorDigestBefore) {
		t.Fatal("v34 retry changed the v33 cursor digest")
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO transcript_events(stream_uid,event_id,publication_seq,client_message_id,
		event_type,source,runner_attempt,payload_json,frame_event_id,created_at) VALUES
		('source-v33',2,2,'source-second','user_message','payload',NULL,'{"content":"second"}',NULL,?),
		('target-v33',2,2,'target-second','history_user_message','payload',NULL,'{"content":"second"}',NULL,?);
		INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id) VALUES
		('source-v33','br_00000001',2,2),('target-v33','br_00000001',2,2)`, now, now); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"foreign target branch": `INSERT INTO transcript_history_ordinary_cursor_map(
			ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
			source_generation,source_through_publication_seq,source_message_index,source_ordinal,source_event_id,
			target_event_id,target_publication_seq,target_message_index,stable_message_id,created_at)
			SELECT ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,'br_missing',
			source_generation,source_through_publication_seq,1,source_ordinal,source_event_id,
			target_event_id,target_publication_seq,1,'missing-branch',created_at
			FROM transcript_history_ordinary_cursor_map LIMIT 1`,
		"stale generation": `INSERT INTO transcript_history_ordinary_cursor_map(
			ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
			source_generation,source_through_publication_seq,source_message_index,source_ordinal,source_event_id,
			target_event_id,target_publication_seq,target_message_index,stable_message_id,created_at)
			SELECT ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
			source_generation+1,source_through_publication_seq,2,source_ordinal,source_event_id,
			target_event_id,target_publication_seq,2,'stale-generation',created_at
			FROM transcript_history_ordinary_cursor_map LIMIT 1`,
		"source membership mismatch": `INSERT INTO transcript_history_ordinary_cursor_map(
			ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
			source_generation,source_through_publication_seq,source_message_index,source_ordinal,source_event_id,
			target_event_id,target_publication_seq,target_message_index,stable_message_id,created_at)
			SELECT ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
			source_generation,source_through_publication_seq,3,1,2,1,1,3,'source-mismatch',created_at
			FROM transcript_history_ordinary_cursor_map LIMIT 1`,
		"target publication mismatch": `INSERT INTO transcript_history_ordinary_cursor_map(
			ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
			source_generation,source_through_publication_seq,source_message_index,source_ordinal,source_event_id,
			target_event_id,target_publication_seq,target_message_index,stable_message_id,created_at)
			SELECT ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
			source_generation,source_through_publication_seq,4,2,2,2,1,4,'target-mismatch',created_at
			FROM transcript_history_ordinary_cursor_map LIMIT 1`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("v34 cursor accepted %s", name)
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
}

func TestTranscriptHistoryOrdinaryCursorV34RejectsDriftAndRollsBack(t *testing.T) {
	t.Run("v33 drift", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for version := 23; version <= 33; version++ {
			applyTranscriptMigration(t, db, version)
		}
		if _, err := db.Exec(`DROP TRIGGER transcript_history_activation_immutable;
			CREATE TRIGGER transcript_history_activation_immutable
			BEFORE UPDATE ON transcript_history_activation_receipts BEGIN SELECT 1; END`); err != nil {
			t.Fatal(err)
		}
		err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 34)
		if err == nil || !strings.Contains(err.Error(), "ordinary history v33 cohort identity mismatch") {
			t.Fatalf("v33 drift error=%v", err)
		}
		assertSchemaJournalVersion(t, db, 33)
	})

	t.Run("rebuild rollback", func(t *testing.T) {
		db, _ := openTargetTwentyTwoProviderDB(t)
		for version := 23; version <= 33; version++ {
			applyTranscriptMigration(t, db, version)
		}
		migration := transcriptHistoryOrdinaryCursorV34Migration
		migration.statements = append(append([]string(nil), migration.statements...), `CREATE TABLE frames(id TEXT)`)
		if err := applyVersionedSchemaMigration(context.Background(), db, time.Now, migration, "faulted-v34"); err == nil {
			t.Fatal("faulted v34 migration succeeded")
		}
		if err := preflightTranscriptHistoryOrdinaryCursorV34(context.Background(), db); err != nil {
			t.Fatalf("v33 cohort was not restored: %v", err)
		}
		for _, object := range []string{
			"transcript_history_ordinary_cursor_map_v34",
			"transcript_history_ordinary_cursor_validate",
			"transcript_history_ordinary_cursor_immutable",
			"transcript_history_ordinary_cursor_delete_active",
		} {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rolled-back object %s count=%d err=%v", object, count, err)
			}
		}
		assertSchemaJournalVersion(t, db, 33)
	})
}

func seedPlainV33OrdinaryCursor(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
			VALUES('project-v33','owner-v33','V33','',?,?)`, []any{now, now}},
		{`INSERT INTO frames(id,project_id,parent_frame_id,root_frame_id,root_sequence,agent_name,status,
			conversation_type,name,created_at,updated_at)
			VALUES('frame-v33','project-v33',NULL,'frame-v33',0,'OPERON','completed','agent','',?,?)`, []any{now, now}},
		{`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
			VALUES('legacy-v33','frame-v33',1,'user_message','{"role":"user","content":"legacy"}',?)`, []any{now}},
		{`INSERT INTO transcript_streams(stream_uid,owner_id,external_id,session_id,kind,project_id,
			root_frame_id,frame_id,epoch,input_revision,consumed_input_revision,next_event_id,
			next_publication_seq,next_checkpoint_sequence,created_at,updated_at) VALUES
			('source-v33','owner-v33','frame-v33','frame-v33','frame_ref','project-v33','frame-v33','frame-v33',1,0,0,2,2,1,?,?),
			('target-v33','owner-v33','frame-v33','frame-v33','frame_ref','project-v33','frame-v33','frame-v33',2,0,0,2,2,1,?,?)`, []any{now, now, now, now}},
		{`INSERT INTO transcript_delivery_routes(stream_uid,destination,current_generation,status,updated_at)
			VALUES('source-v33','ws',1,'active',?),('target-v33','ws',1,'active',?)`, []any{now, now}},
		{`INSERT INTO transcript_branches(stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,
			client_mutation_id,request_sha256,source_message_id,created_at,updated_at) VALUES
			('source-v33','br_00000001',NULL,NULL,0,'base','base-source-v33',zeroblob(32),'',?,?),
			('target-v33','br_00000001',NULL,NULL,0,'base','base-target-v33',zeroblob(32),'',?,?)`, []any{now, now, now, now}},
		{`INSERT INTO transcript_branch_state(stream_uid,active_branch_id,generation,updated_at)
			VALUES('source-v33','br_00000001',1,?),('target-v33','br_00000001',1,?)`, []any{now, now}},
		{`INSERT INTO transcript_events(stream_uid,event_id,publication_seq,client_message_id,event_type,source,
			runner_attempt,payload_json,frame_event_id,created_at) VALUES
			('source-v33',1,1,'legacy-client','user_message','frame_ref',NULL,NULL,'legacy-v33',?),
			('target-v33',1,1,'payload-genesis:legacy-v33','history_user_message','payload',NULL,
			'{"role":"user","content":"legacy"}',NULL,?)`, []any{now, now}},
		{`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id)
			VALUES('source-v33','br_00000001',1,1),('target-v33','br_00000001',1,1)`, nil},
		{`INSERT INTO transcript_history_classification_runs(run_id,stream_uid,branch_id,branch_generation,
			contract_version,through_ordinal,through_publication_seq,source_sha256,scan_status,scan_reason,
			candidate_count,native_count,eligible_count,poison_count,conflict_count,classified_at)
			VALUES(zeroblob(32),'source-v33','br_00000001',1,1,1,1,zeroblob(32),'complete','none',0,0,0,0,0,?)`, []any{now}},
		{`INSERT INTO transcript_history_ordinary_cutover_runs(ordinary_cutover_id,classification_run_id,
			source_stream_uid,target_stream_uid,owner_id,session_id,source_epoch,target_epoch,source_branch_id,
			target_branch_id,branch_generation,source_through_publication_seq,target_through_publication_seq,
			source_sha256,materialized_sha256,event_count,branch_count,branch_event_count,attempt_count,
			receipt_count,checkpoint_count,artifact_commit_count,artifact_ref_count,route_count,cursor_count,
			history_kind,status,created_at) VALUES(randomblob(32),zeroblob(32),'source-v33','target-v33',
			'owner-v33','frame-v33',1,2,'br_00000001','br_00000001',1,1,1,zeroblob(32),
			zeroblob(32),1,1,1,0,0,0,0,0,1,1,'ordinary_no_ask_user_v1','active',?)`, []any{now}},
		{`INSERT INTO transcript_history_ordinary_cursor_map(ordinary_cutover_id,source_stream_uid,
			target_stream_uid,source_branch_id,target_branch_id,source_ordinal,source_event_id,target_event_id,
			target_publication_seq,target_message_index,stable_message_id,created_at)
			SELECT ordinary_cutover_id,'source-v33','target-v33','br_00000001','br_00000001',1,1,1,1,0,
			'payload-genesis:legacy-v33',? FROM transcript_history_ordinary_cutover_runs`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed v33 ordinary cursor: %v", err)
		}
	}
	var cutoverID []byte
	if err := db.QueryRow(`SELECT ordinary_cutover_id FROM transcript_history_ordinary_cutover_runs`).Scan(&cutoverID); err != nil {
		t.Fatal(err)
	}
	materialized := historyRowsDigestForTest(t, db, `SELECT event_id,publication_seq,client_message_id,
		event_type,payload_json,created_at FROM transcript_events WHERE stream_uid='target-v33'
		ORDER BY publication_seq,event_id`)
	lineageHash := sha256.New()
	writeHistoryDigestFieldForTest(lineageHash, []byte(transcriptstore.HistoryOrdinaryCutoverContractID))
	writeHistoryDigestFieldForTest(lineageHash, []byte("lineage"))
	for _, query := range []string{
		`SELECT branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
			request_sha256,source_message_id,created_at,updated_at FROM transcript_branches
			WHERE stream_uid='target-v33' ORDER BY branch_id`,
		`SELECT active_branch_id,generation,updated_at FROM transcript_branch_state WHERE stream_uid='target-v33'`,
		`SELECT branch_id,ordinal,event_id FROM transcript_branch_events
			WHERE stream_uid='target-v33' ORDER BY branch_id,ordinal`,
	} {
		historyRowsDigestIntoForTest(t, db, lineageHash, query)
	}
	lineage := lineageHash.Sum(nil)
	cursorDigest := historyRowsDigestForTest(t, db, `SELECT source_stream_uid,target_stream_uid,
		source_branch_id,target_branch_id,source_ordinal,source_event_id,target_event_id,
		target_publication_seq,target_message_index,stable_message_id
		FROM transcript_history_ordinary_cursor_map ORDER BY source_branch_id,source_ordinal`)
	if _, err := db.Exec(`UPDATE transcript_history_ordinary_cutover_runs SET materialized_sha256=?`, materialized); err != nil {
		t.Fatal(err)
	}
	activationID := sha256.Sum256([]byte("plain-v33-activation"))
	if _, err := db.Exec(`INSERT INTO transcript_history_activation_receipts(
		activation_id,provenance_kind,cutover_id,ordinary_cutover_id,source_stream_uid,target_stream_uid,
		owner_id,session_id,source_epoch,target_epoch,prior_activation_id,active_branch_id,branch_generation,
		source_through_publication_seq,target_through_publication_seq,verification_sha256,lineage_sha256,
		cursor_sha256,shadow_sha256,materialized_sha256,attempt_count,receipt_count,checkpoint_count,
		event_count,branch_count,branch_event_count,cursor_count,artifact_commit_count,artifact_ref_count,
		route_count,realtime_high_water,authority_generation,status,activated_at)
		VALUES(?,'ordinary_v33',NULL,?,'source-v33','target-v33','owner-v33','frame-v33',1,2,NULL,
		'br_00000001',1,1,1,zeroblob(32),?,?,zeroblob(32),?,0,0,0,1,1,1,1,0,0,1,0,2,'active',?)`,
		activationID[:], cutoverID, lineage, cursorDigest, materialized, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_frame_authority(owner_id,session_id,active_stream_uid,
		active_epoch,authority_generation,read_authority,write_authority,activation_id,updated_at)
		VALUES('owner-v33','frame-v33','target-v33',2,2,'transcript_payload_v1','transcript_payload_v1',?,?)`,
		activationID[:], now); err != nil {
		t.Fatal(err)
	}
}

func historyRowsDigestForTest(t *testing.T, db *sql.DB, query string) []byte {
	t.Helper()
	digest := sha256.New()
	historyRowsDigestIntoForTest(t, db, digest, query)
	return digest.Sum(nil)
}

func historyRowsDigestIntoForTest(t *testing.T, db *sql.DB, digest hash.Hash, query string) {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			switch typed := value.(type) {
			case nil:
				writeHistoryDigestFieldForTest(digest, nil)
			case []byte:
				writeHistoryDigestFieldForTest(digest, typed)
			case time.Time:
				writeHistoryDigestFieldForTest(digest, []byte(typed.UTC().Format(time.RFC3339Nano)))
			default:
				writeHistoryDigestFieldForTest(digest, []byte(fmt.Sprint(typed)))
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func writeHistoryDigestFieldForTest(digest hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}
