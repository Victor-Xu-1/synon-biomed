package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTranscriptPayloadGenesisV32UpgradeBacksUpV31BeforeWrite(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 31; version++ {
		applyTranscriptMigration(t, db, version)
	}
	now := time.Date(2026, 7, 26, 11, 30, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
		VALUES('project-v31-backup','owner-a','V31 backup sentinel','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.schemaBackupPath == "" {
		t.Fatal("v31 upgrade did not retain a backup")
	}
	backup, err := sql.Open(sqliteDriver, store.schemaBackupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var backupVersion, sentinelProjects, backupGenesisObjects int
	if err := backup.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&backupVersion); err != nil {
		t.Fatal(err)
	}
	if err := backup.QueryRow(`SELECT COUNT(*) FROM projects WHERE id='project-v31-backup'`).Scan(&sentinelProjects); err != nil {
		t.Fatal(err)
	}
	if err := backup.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='transcript_payload_genesis_receipts'`).
		Scan(&backupGenesisObjects); err != nil {
		t.Fatal(err)
	}
	if backupVersion != 31 || sentinelProjects != 1 || backupGenesisObjects != 0 {
		t.Fatalf("backup version=%d sentinel=%d genesis=%d", backupVersion, sentinelProjects, backupGenesisObjects)
	}
	var liveVersion, liveGenesisObjects, violations int
	if err := store.db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&liveVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='transcript_payload_genesis_receipts'`).
		Scan(&liveGenesisObjects); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if liveVersion != workspaceSchemaVersion || liveGenesisObjects != 1 || violations != 0 {
		t.Fatalf("live version=%d genesis=%d violations=%d", liveVersion, liveGenesisObjects, violations)
	}
}

func TestTranscriptPayloadGenesisV32PreservesLegacyAuthorityAndReopens(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 31; version++ {
		applyTranscriptMigration(t, db, version)
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
		VALUES('project-v32','owner-a','Payload genesis','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO frames(
		id,project_id,parent_frame_id,root_frame_id,root_sequence,agent_name,status,conversation_type,name,created_at,updated_at)
		VALUES('frame-v32','project-v32',NULL,'frame-v32',0,'OPERON','processing','agent','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_streams(
		stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,created_at,updated_at)
		VALUES('stream-v32','owner-a','frame:frame-v32','frame-v32','frame_ref',
		'project-v32','frame-v32','frame-v32',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_branches(
		stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
		request_sha256,source_message_id,created_at,updated_at)
		VALUES('stream-v32','br_00000000',NULL,NULL,0,'base','base:stream-v32',zeroblob(32),'',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_branch_state(stream_uid,active_branch_id,generation,updated_at)
		VALUES('stream-v32','br_00000000',1,?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,updated_at)
		VALUES('owner-a','frame-v32','stream-v32',1,1,'legacy_mixed_v1','legacy_frame_ref_v1',NULL,?)`, now); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 32); err != nil {
		t.Fatal(err)
	}
	var streamUID, readAuthority, writeAuthority string
	var epoch, generation int64
	var activationID, genesisID []byte
	if err := db.QueryRow(`SELECT active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id FROM transcript_frame_authority
		WHERE owner_id='owner-a' AND session_id='frame-v32'`).Scan(
		&streamUID, &epoch, &generation, &readAuthority, &writeAuthority, &activationID, &genesisID,
	); err != nil {
		t.Fatal(err)
	}
	if streamUID != "stream-v32" || epoch != 1 || generation != 1 ||
		readAuthority != "legacy_mixed_v1" || writeAuthority != "legacy_frame_ref_v1" ||
		activationID != nil || genesisID != nil {
		t.Fatalf("authority stream=%q epoch=%d generation=%d read=%q write=%q activation=%x genesis=%x",
			streamUID, epoch, generation, readAuthority, writeAuthority, activationID, genesisID)
	}
	var receipts, violations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_payload_genesis_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("genesis receipts=%d err=%v", receipts, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
	assertSchemaJournalVersion(t, db, 32)
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
		t.Fatalf("reopened v32 contract: %v", err)
	}
}

func TestTranscriptPayloadGenesisV32PreservesActivatedV31AuthorityAndReopens(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 31; version++ {
		applyTranscriptMigration(t, db, version)
	}
	fixture := seedActivatedV31PayloadAuthority(t, db)
	var beforeReceipt, beforeTarget string
	if err := db.QueryRow(`SELECT hex(activation_id)||':'||hex(cutover_id)||':'||hex(verification_sha256)||':'||
		hex(lineage_sha256)||':'||hex(cursor_sha256)||':'||hex(shadow_sha256)||':'||hex(materialized_sha256)
		FROM transcript_history_activation_receipts WHERE activation_id=?`, fixture.activationID).Scan(&beforeReceipt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT event_type||':'||hex(payload_json)||':'||client_message_id
		FROM transcript_events WHERE stream_uid=? AND event_id=1`, fixture.targetStreamUID).Scan(&beforeTarget); err != nil {
		t.Fatal(err)
	}
	var beforeViolations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&beforeViolations); err != nil || beforeViolations != 0 {
		t.Fatalf("v31 foreign key violations=%d err=%v", beforeViolations, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.schemaBackupPath == "" {
		_ = store.Close()
		t.Fatal("activated v31 upgrade did not retain a backup")
	}
	backupPath := store.schemaBackupPath
	assertActivatedV32PayloadAuthority(t, store, fixture, beforeReceipt, beforeTarget)
	backup, err := sql.Open(sqliteDriver, backupPath)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	assertActivatedV31Backup(t, backup, fixture, beforeReceipt, beforeTarget)
	if err := backup.Close(); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	recoveryBytes, err := os.ReadFile(backupPath)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.schemaBackupPath != "" {
		_ = reopened.Close()
		t.Fatalf("reopened v32 created another backup %q", reopened.schemaBackupPath)
	}
	assertActivatedV32PayloadAuthority(t, reopened, fixture, beforeReceipt, beforeTarget)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	recoveryPath := filepath.Join(filepath.Dir(path), "activated-v31-forward-recovery.db")
	if err := os.WriteFile(recoveryPath, recoveryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if recovered.schemaBackupPath == "" {
		t.Fatal("forward recovery did not preserve its pre-v32 backup")
	}
	assertActivatedV32PayloadAuthority(t, recovered, fixture, beforeReceipt, beforeTarget)
}

func assertActivatedV32PayloadAuthority(
	t *testing.T,
	store *Store,
	fixture activatedV31PayloadAuthorityFixture,
	wantReceipt, wantTarget string,
) {
	t.Helper()
	var streamUID, readAuthority, writeAuthority string
	var epoch, generation int64
	var activationID, genesisID []byte
	if err := store.db.QueryRow(`SELECT active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id FROM transcript_frame_authority
		WHERE owner_id=? AND session_id=?`, fixture.ownerID, fixture.frameID).Scan(
		&streamUID, &epoch, &generation, &readAuthority, &writeAuthority, &activationID, &genesisID,
	); err != nil {
		t.Fatal(err)
	}
	if streamUID != fixture.targetStreamUID || epoch != 2 || generation != 2 ||
		readAuthority != "transcript_payload_v1" || writeAuthority != "transcript_payload_v1" ||
		string(activationID) != string(fixture.activationID) || genesisID != nil {
		t.Fatalf("authority stream=%q epoch=%d generation=%d read=%q write=%q activation=%x genesis=%x",
			streamUID, epoch, generation, readAuthority, writeAuthority, activationID, genesisID)
	}
	var gotReceipt, gotTarget string
	if err := store.db.QueryRow(`SELECT hex(activation_id)||':'||hex(cutover_id)||':'||hex(verification_sha256)||':'||
		hex(lineage_sha256)||':'||hex(cursor_sha256)||':'||hex(shadow_sha256)||':'||hex(materialized_sha256)
		FROM transcript_history_activation_receipts WHERE activation_id=?`, fixture.activationID).Scan(&gotReceipt); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT event_type||':'||hex(payload_json)||':'||client_message_id
		FROM transcript_events WHERE stream_uid=? AND event_id=1`, fixture.targetStreamUID).Scan(&gotTarget); err != nil {
		t.Fatal(err)
	}
	var receipts, genesisReceipts, violations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_history_activation_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_payload_genesis_receipts`).Scan(&genesisReceipts); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if wantReceipt != gotReceipt || wantTarget != gotTarget || receipts != 1 || genesisReceipts != 0 || violations != 0 {
		t.Fatalf("receipt preserved=%t target preserved=%t receipts=%d genesis=%d violations=%d",
			wantReceipt == gotReceipt, wantTarget == gotTarget, receipts, genesisReceipts, violations)
	}
	assertSchemaJournalVersion(t, store.db, workspaceSchemaVersion)
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	authority, found, err := repository.GetFrameAuthorityBySession(context.Background(), fixture.ownerID, fixture.frameID)
	if err != nil || !found || !authority.TranscriptPayloadActive() || authority.ActiveStreamUID != fixture.targetStreamUID ||
		authority.ActiveEpoch != 2 || authority.AuthorityGeneration != 2 || string(authority.ActivationID) != string(fixture.activationID) ||
		authority.GenesisID != nil {
		t.Fatalf("reopened authority=%#v found=%t err=%v", authority, found, err)
	}
	snapshot, err := repository.GetProjectionSnapshot(context.Background(), fixture.targetStreamUID, fixture.ownerID)
	if err != nil || snapshot.StreamUID != fixture.targetStreamUID ||
		snapshot.BranchID != fixture.targetBranchID || snapshot.ThroughPublicationSequence != 1 {
		t.Fatalf("reopened snapshot=%#v err=%v", snapshot, err)
	}
	if err := repository.ValidateContract(context.Background()); err != nil {
		t.Fatalf("reopened activated current contract: %v", err)
	}
}

func assertActivatedV31Backup(
	t *testing.T,
	db *sql.DB,
	fixture activatedV31PayloadAuthorityFixture,
	wantReceipt, wantTarget string,
) {
	t.Helper()
	var version, genesisObjects, genesisColumns, violations int
	if err := db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='transcript_payload_genesis_receipts'`).Scan(&genesisObjects); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('transcript_frame_authority') WHERE name='genesis_id'`).Scan(&genesisColumns); err != nil {
		t.Fatal(err)
	}
	var gotReceipt, gotTarget string
	if err := db.QueryRow(`SELECT hex(activation_id)||':'||hex(cutover_id)||':'||hex(verification_sha256)||':'||
		hex(lineage_sha256)||':'||hex(cursor_sha256)||':'||hex(shadow_sha256)||':'||hex(materialized_sha256)
		FROM transcript_history_activation_receipts WHERE activation_id=?`, fixture.activationID).Scan(&gotReceipt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT event_type||':'||hex(payload_json)||':'||client_message_id
		FROM transcript_events WHERE stream_uid=? AND event_id=1`, fixture.targetStreamUID).Scan(&gotTarget); err != nil {
		t.Fatal(err)
	}
	var streamUID string
	var epoch, generation int64
	var activationID []byte
	if err := db.QueryRow(`SELECT active_stream_uid,active_epoch,authority_generation,activation_id
		FROM transcript_frame_authority WHERE owner_id=? AND session_id=?`, fixture.ownerID, fixture.frameID).Scan(
		&streamUID, &epoch, &generation, &activationID,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if version != 31 || genesisObjects != 0 || genesisColumns != 0 || wantReceipt != gotReceipt || wantTarget != gotTarget ||
		streamUID != fixture.targetStreamUID || epoch != 2 || generation != 2 ||
		string(activationID) != string(fixture.activationID) || violations != 0 {
		t.Fatalf("backup version=%d genesis objects=%d columns=%d receipt=%t target=%t stream=%q epoch=%d generation=%d activation=%x violations=%d",
			version, genesisObjects, genesisColumns, wantReceipt == gotReceipt, wantTarget == gotTarget,
			streamUID, epoch, generation, activationID, violations)
	}
}

type activatedV31PayloadAuthorityFixture struct {
	ownerID, frameID, targetStreamUID, targetBranchID string
	activationID                                      []byte
}

func seedActivatedV31PayloadAuthority(t *testing.T, db *sql.DB) activatedV31PayloadAuthorityFixture {
	t.Helper()
	fixture := activatedV31PayloadAuthorityFixture{
		ownerID: "owner-v31-active", frameID: "frame-v31-active",
		targetStreamUID: "stream-v31-active-target", targetBranchID: "br_00000001",
		activationID: activatedV31FixtureDigest("activation"),
	}
	now := time.Date(2026, 7, 26, 12, 30, 0, 0, time.UTC)
	projectID := "project-v31-active"
	sourceStreamUID := "stream-v31-active-source"
	sourceBranchID := "br_00000000"
	runID := activatedV31FixtureDigest("classification")
	candidateID := activatedV31FixtureDigest("candidate")
	backfillID := activatedV31FixtureDigest("backfill")
	cutoverID := activatedV31FixtureDigest("cutover")
	digest := activatedV31FixtureDigest("evidence")
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
		VALUES(?,?,?,?,?,?)`, projectID, fixture.ownerID, "Activated v31", "", now, now)
	exec(`INSERT INTO frames(
		id,project_id,parent_frame_id,root_frame_id,root_sequence,agent_name,status,conversation_type,name,created_at,updated_at)
		VALUES(?,?,NULL,?,0,'OPERON','completed','agent','',?,?)`, fixture.frameID, projectID, fixture.frameID, now, now)
	for _, stream := range []struct {
		uid   string
		epoch int
	}{
		{uid: sourceStreamUID, epoch: 1},
		{uid: fixture.targetStreamUID, epoch: 2},
	} {
		exec(`INSERT INTO transcript_streams(
			stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,
			input_revision,consumed_input_revision,next_event_id,next_publication_seq,next_checkpoint_sequence,
			created_at,updated_at) VALUES(?,?,?,?, 'frame_ref',?,?,?,?,1,1,2,2,1,?,?)`,
			stream.uid, fixture.ownerID, "frame:"+fixture.frameID, fixture.frameID,
			projectID, fixture.frameID, fixture.frameID, stream.epoch, now, now)
	}
	for _, branch := range []struct {
		streamUID, branchID string
	}{
		{sourceStreamUID, sourceBranchID},
		{fixture.targetStreamUID, fixture.targetBranchID},
	} {
		exec(`INSERT INTO transcript_branches(
			stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
			request_sha256,source_message_id,created_at,updated_at)
			VALUES(?,?,NULL,NULL,0,'base',?,?, '',?,?)`,
			branch.streamUID, branch.branchID, "base:"+branch.streamUID, digest, now, now)
		exec(`INSERT INTO transcript_branch_state(stream_uid,active_branch_id,generation,updated_at)
			VALUES(?,?,1,?)`, branch.streamUID, branch.branchID, now)
		exec(`INSERT INTO transcript_events(
			stream_uid,event_id,publication_seq,client_message_id,event_type,source,runner_attempt,payload_json,frame_event_id,created_at)
			VALUES(?,1,1,?,'user_message','payload',NULL,?,NULL,?)`,
			branch.streamUID, "message:"+branch.streamUID, []byte(`{"text":"activated v31"}`), now)
		exec(`INSERT INTO transcript_branch_events(stream_uid,branch_id,ordinal,event_id) VALUES(?,?,1,1)`,
			branch.streamUID, branch.branchID)
	}
	exec(`INSERT INTO transcript_history_classification_runs(
		run_id,stream_uid,branch_id,branch_generation,contract_version,through_ordinal,through_publication_seq,
		source_sha256,scan_status,scan_reason,candidate_count,native_count,eligible_count,poison_count,conflict_count,classified_at)
		VALUES(?,?,?,1,1,1,1,?,'complete','none',1,0,1,0,0,?)`,
		runID, sourceStreamUID, sourceBranchID, digest, now)
	exec(`INSERT INTO transcript_history_classification_candidates(
		run_id,candidate_id,first_ordinal,tool_use_id,tool_event_id,result_event_id,runner_attempt,
		disposition,reason_code,state_json,evidence_json,evidence_sha256)
		VALUES(?,?,1,'ask-v31',1,1,1,'eligible','structured_legacy',?, '{}',?)`,
		runID, candidateID, []byte(`{"version":1,"status":"answered","action":"answer","answers":{"q":"a"}}`), digest)
	for _, dimension := range []string{"stable_ids", "branch_order", "ask_user_states", "terminal_facts", "artifact_refs"} {
		exec(`INSERT INTO transcript_history_shadow_comparisons(
			run_id,dimension,verdict,legacy_sha256,candidate_sha256,reason_code,evidence_json,compared_at)
			VALUES(?,?,'match',?,?,'none','{}',?)`, runID, dimension, digest, digest, now)
	}
	exec(`INSERT INTO transcript_history_backfill_runs(
		backfill_id,classification_run_id,stream_uid,branch_id,branch_generation,contract_version,
		source_sha256,staging_sha256,candidate_count,created_at)
		VALUES(?,?,?,?,1,1,?,?,1,?)`, backfillID, runID, sourceStreamUID, sourceBranchID, digest, digest, now)
	exec(`INSERT INTO transcript_history_backfill_candidates(
		backfill_id,classification_run_id,candidate_id,candidate_ordinal,tool_use_id,runner_attempt,
		tool_event_id,result_event_id,tool_frame_event_id,result_frame_event_id,prompt_client_message_id,
		pending_client_message_id,terminal_client_message_id,prompt_json,pending_json,result_json,
		payload_sha256,evidence_sha256,created_at)
		VALUES(?,?,?,1,'ask-v31',1,1,1,'frame-tool','frame-result','prompt-v31','pending-v31','terminal-v31',
		'{}','{}','{}',?,?,?)`, backfillID, runID, candidateID, digest, digest, now)
	exec(`INSERT INTO transcript_history_backfill_cursor_map(
		backfill_id,candidate_id,legacy_ordinal,stable_message_id,created_at)
		VALUES(?,?,1,'message-v31',?)`, backfillID, candidateID, now)
	exec(`INSERT INTO transcript_history_cutover_runs(
		cutover_id,backfill_id,supersedes_cutover_id,stream_uid,owner_id,source_branch_id,source_generation,
		source_through_publication_seq,contract_version,verification_sha256,lineage_sha256,cursor_sha256,
		shadow_sha256,branch_count,event_count,cursor_count,prior_read_authority,target_read_authority,
		status,activated,created_at,updated_at)
		VALUES(?,?,NULL,?,?,?,1,1,1,?,?,?,?,1,1,1,'legacy_mixed_v1','transcript_payload_v1','ready',0,?,?)`,
		cutoverID, backfillID, sourceStreamUID, fixture.ownerID, sourceBranchID, digest, digest, digest, digest, now, now)
	exec(`INSERT INTO transcript_history_cutover_events(
		cutover_id,target_event_key,source_event_id,candidate_id,fact_kind,target_publication_seq,
		client_message_id,event_type,runner_attempt,payload_json,payload_sha256,created_at)
		VALUES(?,'event-v31',1,?,'existing',1,'message-v31','user_message',NULL,?,?,?)`,
		cutoverID, candidateID, []byte(`{"text":"activated v31"}`), digest, now)
	exec(`INSERT INTO transcript_history_cutover_branches(
		cutover_id,source_branch_id,target_branch_id,parent_source_branch_id,parent_target_branch_id,kind,
		source_fork_event_id,target_fork_event_key,fork_point,client_mutation_id,request_sha256,source_message_id,
		source_through_ordinal,source_through_publication_seq,source_membership_sha256,target_membership_sha256,
		created_at,updated_at)
		VALUES(?,?,?,NULL,NULL,'base',NULL,NULL,0,'base-v31',?,'',1,1,?,?,?,?)`,
		cutoverID, sourceBranchID, fixture.targetBranchID, digest, digest, digest, now, now)
	exec(`INSERT INTO transcript_history_cutover_branch_events(
		cutover_id,target_branch_id,target_ordinal,target_event_key,source_ordinal)
		VALUES(?,?,1,'event-v31',1)`, cutoverID, fixture.targetBranchID)
	exec(`INSERT INTO transcript_history_cutover_cursor_map(
		cutover_id,source_branch_id,target_branch_id,source_generation,source_through_publication_seq,
		source_message_index,source_ordinal,stable_message_id,target_publication_seq,target_message_index)
		VALUES(?,?,?,1,1,0,1,'message-v31',1,0)`, cutoverID, sourceBranchID, fixture.targetBranchID)
	for _, dimension := range []string{"stable_ids", "branch_order", "ask_user_states", "terminal_facts", "artifact_refs"} {
		exec(`INSERT INTO transcript_history_cutover_shadow_comparisons(
			cutover_id,source_branch_id,dimension,verdict,legacy_sha256,target_sha256,compared_at)
			VALUES(?,?,?,'match',?,?,?)`, cutoverID, sourceBranchID, dimension, digest, digest, now)
	}
	exec(`INSERT INTO transcript_history_activation_receipts(
		activation_id,cutover_id,source_stream_uid,target_stream_uid,owner_id,session_id,source_epoch,target_epoch,
		prior_activation_id,active_branch_id,branch_generation,source_through_publication_seq,target_through_publication_seq,
		verification_sha256,lineage_sha256,cursor_sha256,shadow_sha256,materialized_sha256,
		attempt_count,receipt_count,checkpoint_count,event_count,branch_count,branch_event_count,cursor_count,
		artifact_commit_count,artifact_ref_count,route_count,realtime_high_water,authority_generation,status,activated_at)
		VALUES(?,?,?,?,?,?,1,2,NULL,?,1,1,1,?,?,?,?,?,0,0,0,1,1,1,1,0,0,0,0,2,'active',?)`,
		fixture.activationID, cutoverID, sourceStreamUID, fixture.targetStreamUID, fixture.ownerID, fixture.frameID,
		fixture.targetBranchID, digest, digest, digest, digest, digest, now)
	exec(`INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,updated_at)
		VALUES(?,?,?,2,2,'transcript_payload_v1','transcript_payload_v1',?,?)`,
		fixture.ownerID, fixture.frameID, fixture.targetStreamUID, fixture.activationID, now)
	return fixture
}

func activatedV31FixtureDigest(value string) []byte {
	digest := sha256.Sum256([]byte("activated-v31-fixture:" + value))
	return digest[:]
}

func TestTranscriptPayloadGenesisV32MigrationIdentity(t *testing.T) {
	checksum, err := transcriptPayloadGenesisV32Migration.validatedChecksum()
	if err != nil || checksum != "2aa998e9a55bbe6af882edbb73b7a880ebbabd5b0ea2c84bd08fa1f29f4d4b67" {
		t.Fatalf("v32 checksum=%q err=%v", checksum, err)
	}
}

func TestTranscriptPayloadGenesisV32RejectsCollisionAndRollsBack(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 31; version++ {
		applyTranscriptMigration(t, db, version)
	}
	if _, err := db.Exec(`CREATE TABLE transcript_payload_genesis_receipts(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 32)
	if err == nil || !strings.Contains(err.Error(), "cohort is polluted") {
		t.Fatalf("collision error=%v", err)
	}
	assertSchemaJournalVersion(t, db, 31)
	for _, object := range transcriptstore.HistoryActivationV31ObjectNames() {
		if object == "transcript_payload_genesis_receipts" {
			continue
		}
		var count int
		if queryErr := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); queryErr != nil || count != 1 {
			t.Fatalf("v31 object %s count=%d err=%v", object, count, queryErr)
		}
	}
}

func TestTranscriptPayloadGenesisV32RollsBackPartialTableRebuild(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	for version := 23; version <= 31; version++ {
		applyTranscriptMigration(t, db, version)
	}
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
		VALUES('project-v32-rollback','owner-a','Rollback','',?,?);
		INSERT INTO frames(
			id,project_id,parent_frame_id,root_frame_id,root_sequence,agent_name,status,conversation_type,name,created_at,updated_at)
		VALUES('frame-v32-rollback','project-v32-rollback',NULL,'frame-v32-rollback',0,
			'OPERON','processing','agent','',?,?);
		INSERT INTO transcript_streams(
			stream_uid,owner_id,external_id,session_id,kind,project_id,root_frame_id,frame_id,epoch,created_at,updated_at)
		VALUES('stream-v32-rollback','owner-a','frame:frame-v32-rollback','frame-v32-rollback','frame_ref',
			'project-v32-rollback','frame-v32-rollback','frame-v32-rollback',1,?,?);
		INSERT INTO transcript_branches(
			stream_uid,branch_id,parent_branch_id,fork_event_id,fork_point,kind,client_mutation_id,
			request_sha256,source_message_id,created_at,updated_at)
		VALUES('stream-v32-rollback','br_00000000',NULL,NULL,0,'base','base:stream-v32-rollback',zeroblob(32),'',?,?);
		INSERT INTO transcript_branch_state(stream_uid,active_branch_id,generation,updated_at)
		VALUES('stream-v32-rollback','br_00000000',1,?);
		INSERT INTO transcript_frame_authority(
			owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
			read_authority,write_authority,activation_id,updated_at)
		VALUES('owner-a','frame-v32-rollback','stream-v32-rollback',1,1,
			'legacy_mixed_v1','legacy_frame_ref_v1',NULL,?)`,
		now, now, now, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	var transitionSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='trigger' AND name='transcript_frame_authority_transition'`).Scan(&transitionSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER transcript_frame_authority_transition`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON;
		UPDATE transcript_frame_authority SET active_epoch=0 WHERE session_id='frame-v32-rollback';
		PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(transitionSQL); err != nil {
		t.Fatal(err)
	}
	err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 32)
	if err == nil {
		t.Fatal("v32 partial rebuild unexpectedly succeeded")
	}
	assertSchemaJournalVersion(t, db, 31)
	var genesisColumns, stagingTables, transitionTriggers, authorityIndexes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('transcript_frame_authority') WHERE name='genesis_id'`).
		Scan(&genesisColumns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='transcript_frame_authority_v31_staging'`).
		Scan(&stagingTables); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name='transcript_frame_authority_transition'`).
		Scan(&transitionTriggers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name='transcript_frame_authority_stream'`).
		Scan(&authorityIndexes); err != nil {
		t.Fatal(err)
	}
	if genesisColumns != 0 || stagingTables != 0 || transitionTriggers != 1 || authorityIndexes != 1 {
		t.Fatalf("rollback schema genesis=%d staging=%d trigger=%d index=%d",
			genesisColumns, stagingTables, transitionTriggers, authorityIndexes)
	}
}
