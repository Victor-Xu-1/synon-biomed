package transcript

import "strings"

const HistoryOrdinaryCutoverContractID = "synon.transcript.history-ordinary-cutover.v33"

const historyOrdinaryCutoverRunsV33Statement = `CREATE TABLE transcript_history_ordinary_cutover_runs (
	ordinary_cutover_id BLOB PRIMARY KEY CHECK(length(ordinary_cutover_id) = 32),
	classification_run_id BLOB NOT NULL UNIQUE CHECK(length(classification_run_id) = 32)
		REFERENCES transcript_history_classification_runs(run_id) ON DELETE NO ACTION,
	source_stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
	target_stream_uid TEXT NOT NULL UNIQUE REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
	owner_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	source_epoch INTEGER NOT NULL CHECK(source_epoch > 0),
	target_epoch INTEGER NOT NULL CHECK(target_epoch = source_epoch + 1),
	source_branch_id TEXT NOT NULL,
	target_branch_id TEXT NOT NULL,
	branch_generation INTEGER NOT NULL CHECK(branch_generation > 0),
	source_through_publication_seq INTEGER NOT NULL CHECK(source_through_publication_seq > 0),
	target_through_publication_seq INTEGER NOT NULL CHECK(target_through_publication_seq > 0),
	source_sha256 BLOB NOT NULL CHECK(length(source_sha256) = 32),
	materialized_sha256 BLOB NOT NULL CHECK(length(materialized_sha256) = 32),
	event_count INTEGER NOT NULL CHECK(event_count > 0),
	branch_count INTEGER NOT NULL CHECK(branch_count > 0),
	branch_event_count INTEGER NOT NULL CHECK(branch_event_count > 0),
	attempt_count INTEGER NOT NULL CHECK(attempt_count >= 0),
	receipt_count INTEGER NOT NULL CHECK(receipt_count >= 0),
	checkpoint_count INTEGER NOT NULL CHECK(checkpoint_count >= 0),
	artifact_commit_count INTEGER NOT NULL CHECK(artifact_commit_count >= 0),
	artifact_ref_count INTEGER NOT NULL CHECK(artifact_ref_count >= 0),
	route_count INTEGER NOT NULL CHECK(route_count >= 0),
	cursor_count INTEGER NOT NULL CHECK(cursor_count > 0),
	history_kind TEXT NOT NULL CHECK(history_kind = 'ordinary_no_ask_user_v1'),
	status TEXT NOT NULL CHECK(status = 'active'),
	created_at TIMESTAMP NOT NULL,
	UNIQUE(ordinary_cutover_id,source_stream_uid,owner_id),
	UNIQUE(ordinary_cutover_id,source_branch_id),
	UNIQUE(ordinary_cutover_id,target_branch_id),
	UNIQUE(ordinary_cutover_id,source_stream_uid,source_branch_id),
	UNIQUE(ordinary_cutover_id,target_stream_uid,target_branch_id),
	FOREIGN KEY(source_stream_uid,owner_id,session_id,source_epoch)
		REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,owner_id,session_id,target_epoch)
		REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE NO ACTION,
	FOREIGN KEY(source_stream_uid,source_branch_id)
		REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,target_branch_id)
		REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION
)`

const historyOrdinaryCursorMapV33Statement = `CREATE TABLE transcript_history_ordinary_cursor_map (
	ordinary_cutover_id BLOB NOT NULL CHECK(length(ordinary_cutover_id) = 32)
		REFERENCES transcript_history_ordinary_cutover_runs(ordinary_cutover_id) ON DELETE NO ACTION,
	source_stream_uid TEXT NOT NULL,
	target_stream_uid TEXT NOT NULL,
	source_branch_id TEXT NOT NULL,
	target_branch_id TEXT NOT NULL,
	source_ordinal INTEGER NOT NULL CHECK(source_ordinal > 0),
	source_event_id INTEGER NOT NULL CHECK(source_event_id > 0),
	target_event_id INTEGER NOT NULL CHECK(target_event_id > 0),
	target_publication_seq INTEGER NOT NULL CHECK(target_publication_seq > 0),
	target_message_index INTEGER NOT NULL CHECK(target_message_index >= 0),
	stable_message_id TEXT NOT NULL CHECK(length(stable_message_id) > 0),
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY(ordinary_cutover_id,source_branch_id,source_ordinal),
	UNIQUE(ordinary_cutover_id,target_branch_id,target_event_id),
	FOREIGN KEY(ordinary_cutover_id,source_stream_uid,source_branch_id)
		REFERENCES transcript_history_ordinary_cutover_runs(
			ordinary_cutover_id,source_stream_uid,source_branch_id)
		ON DELETE NO ACTION,
	FOREIGN KEY(ordinary_cutover_id,target_stream_uid,target_branch_id)
		REFERENCES transcript_history_ordinary_cutover_runs(
			ordinary_cutover_id,target_stream_uid,target_branch_id)
		ON DELETE NO ACTION,
	FOREIGN KEY(source_stream_uid,source_branch_id,source_ordinal)
		REFERENCES transcript_branch_events(stream_uid,branch_id,ordinal) ON DELETE NO ACTION,
	FOREIGN KEY(source_stream_uid,source_event_id)
		REFERENCES transcript_events(stream_uid,event_id) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,target_event_id)
		REFERENCES transcript_events(stream_uid,event_id) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,target_publication_seq)
		REFERENCES transcript_events(stream_uid,publication_seq) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,target_branch_id,target_event_id)
		REFERENCES transcript_branch_events(stream_uid,branch_id,event_id) ON DELETE NO ACTION
)`

const historyActivationReceiptsV33Statement = `CREATE TABLE transcript_history_activation_receipts (
	activation_id BLOB PRIMARY KEY CHECK(length(activation_id) = 32),
	provenance_kind TEXT NOT NULL CHECK(provenance_kind IN ('ask_user_v30','ordinary_v33')),
	cutover_id BLOB UNIQUE CHECK(cutover_id IS NULL OR length(cutover_id) = 32)
		REFERENCES transcript_history_cutover_runs(cutover_id) ON DELETE NO ACTION,
	ordinary_cutover_id BLOB UNIQUE CHECK(ordinary_cutover_id IS NULL OR length(ordinary_cutover_id) = 32)
		REFERENCES transcript_history_ordinary_cutover_runs(ordinary_cutover_id) ON DELETE NO ACTION,
	source_stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
	target_stream_uid TEXT NOT NULL UNIQUE REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
	owner_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	source_epoch INTEGER NOT NULL CHECK(source_epoch > 0),
	target_epoch INTEGER NOT NULL CHECK(target_epoch = source_epoch + 1),
	prior_activation_id BLOB CHECK(prior_activation_id IS NULL)
		REFERENCES transcript_history_activation_receipts(activation_id) ON DELETE NO ACTION,
	active_branch_id TEXT NOT NULL,
	branch_generation INTEGER NOT NULL CHECK(branch_generation > 0),
	source_through_publication_seq INTEGER NOT NULL CHECK(source_through_publication_seq > 0),
	target_through_publication_seq INTEGER NOT NULL CHECK(target_through_publication_seq > 0),
	verification_sha256 BLOB NOT NULL CHECK(length(verification_sha256) = 32),
	lineage_sha256 BLOB NOT NULL CHECK(length(lineage_sha256) = 32),
	cursor_sha256 BLOB NOT NULL CHECK(length(cursor_sha256) = 32),
	shadow_sha256 BLOB NOT NULL CHECK(length(shadow_sha256) = 32),
	materialized_sha256 BLOB NOT NULL CHECK(length(materialized_sha256) = 32),
	attempt_count INTEGER NOT NULL CHECK(attempt_count >= 0),
	receipt_count INTEGER NOT NULL CHECK(receipt_count >= 0),
	checkpoint_count INTEGER NOT NULL CHECK(checkpoint_count >= 0),
	event_count INTEGER NOT NULL CHECK(event_count > 0),
	branch_count INTEGER NOT NULL CHECK(branch_count > 0),
	branch_event_count INTEGER NOT NULL CHECK(branch_event_count > 0),
	cursor_count INTEGER NOT NULL CHECK(cursor_count > 0),
	artifact_commit_count INTEGER NOT NULL CHECK(artifact_commit_count >= 0),
	artifact_ref_count INTEGER NOT NULL CHECK(artifact_ref_count >= 0),
	route_count INTEGER NOT NULL CHECK(route_count >= 0),
	realtime_high_water INTEGER NOT NULL CHECK(realtime_high_water >= 0),
	authority_generation INTEGER NOT NULL CHECK(authority_generation > 1),
	status TEXT NOT NULL CHECK(status = 'active'),
	activated_at TIMESTAMP NOT NULL,
	UNIQUE(activation_id,target_stream_uid,target_epoch,authority_generation),
	FOREIGN KEY(cutover_id,source_stream_uid,owner_id)
		REFERENCES transcript_history_cutover_runs(cutover_id,stream_uid,owner_id) ON DELETE NO ACTION,
	FOREIGN KEY(ordinary_cutover_id,source_stream_uid,owner_id)
		REFERENCES transcript_history_ordinary_cutover_runs(ordinary_cutover_id,source_stream_uid,owner_id)
		ON DELETE NO ACTION,
	FOREIGN KEY(source_stream_uid,owner_id,session_id,source_epoch)
		REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,owner_id,session_id,target_epoch)
		REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,active_branch_id)
		REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION,
	CHECK(
		(provenance_kind='ask_user_v30' AND cutover_id IS NOT NULL AND ordinary_cutover_id IS NULL) OR
		(provenance_kind='ordinary_v33' AND cutover_id IS NULL AND ordinary_cutover_id IS NOT NULL)
	)
)`

var historyOrdinaryV33Statements = []string{
	historyOrdinaryCutoverRunsV33Statement,
	historyOrdinaryCursorMapV33Statement,
	`DROP TRIGGER transcript_history_activation_validate`,
	`DROP TRIGGER transcript_history_activation_freeze_cutover`,
	`DROP INDEX transcript_history_activation_source`,
	`PRAGMA defer_foreign_keys=ON`,
	strings.Replace(historyActivationReceiptsV33Statement,
		"CREATE TABLE transcript_history_activation_receipts (",
		"CREATE TABLE transcript_history_activation_receipts_v33 (", 1),
	`INSERT INTO transcript_history_activation_receipts_v33(
		activation_id,provenance_kind,cutover_id,ordinary_cutover_id,source_stream_uid,target_stream_uid,
		owner_id,session_id,source_epoch,target_epoch,prior_activation_id,active_branch_id,branch_generation,
		source_through_publication_seq,target_through_publication_seq,verification_sha256,lineage_sha256,
		cursor_sha256,shadow_sha256,materialized_sha256,attempt_count,receipt_count,checkpoint_count,
		event_count,branch_count,branch_event_count,cursor_count,artifact_commit_count,artifact_ref_count,
		route_count,realtime_high_water,authority_generation,status,activated_at)
	SELECT activation_id,'ask_user_v30',cutover_id,NULL,source_stream_uid,target_stream_uid,
		owner_id,session_id,source_epoch,target_epoch,prior_activation_id,active_branch_id,branch_generation,
		source_through_publication_seq,target_through_publication_seq,verification_sha256,lineage_sha256,
		cursor_sha256,shadow_sha256,materialized_sha256,attempt_count,receipt_count,checkpoint_count,
		event_count,branch_count,branch_event_count,cursor_count,artifact_commit_count,artifact_ref_count,
		route_count,realtime_high_water,authority_generation,status,activated_at
	FROM transcript_history_activation_receipts`,
	`DROP TABLE transcript_history_activation_receipts`,
	`ALTER TABLE transcript_history_activation_receipts_v33 RENAME TO transcript_history_activation_receipts`,
	`CREATE INDEX transcript_history_activation_source
		ON transcript_history_activation_receipts(source_stream_uid,target_epoch,activated_at)`,
	`CREATE TRIGGER transcript_history_activation_validate
		BEFORE INSERT ON transcript_history_activation_receipts
		WHEN NOT EXISTS (
			SELECT 1 FROM transcript_streams source
			JOIN transcript_streams target ON target.stream_uid=NEW.target_stream_uid
			WHERE source.stream_uid=NEW.source_stream_uid AND source.owner_id=NEW.owner_id
				AND target.owner_id=NEW.owner_id AND source.session_id=NEW.session_id
				AND target.session_id=NEW.session_id AND source.kind='frame_ref' AND target.kind='frame_ref'
				AND source.epoch=NEW.source_epoch AND target.epoch=NEW.target_epoch
				AND ((NEW.provenance_kind='ask_user_v30' AND EXISTS (
					SELECT 1 FROM transcript_history_cutover_runs cutover
					WHERE cutover.cutover_id=NEW.cutover_id AND cutover.stream_uid=NEW.source_stream_uid
						AND cutover.owner_id=NEW.owner_id))
				OR (NEW.provenance_kind='ordinary_v33' AND EXISTS (
					SELECT 1 FROM transcript_history_ordinary_cutover_runs cutover
					WHERE cutover.ordinary_cutover_id=NEW.ordinary_cutover_id
						AND cutover.source_stream_uid=NEW.source_stream_uid
						AND cutover.target_stream_uid=NEW.target_stream_uid
						AND cutover.owner_id=NEW.owner_id AND cutover.session_id=NEW.session_id
						AND cutover.source_epoch=NEW.source_epoch AND cutover.target_epoch=NEW.target_epoch
						AND cutover.target_branch_id=NEW.active_branch_id
						AND cutover.branch_generation=NEW.branch_generation
						AND cutover.source_through_publication_seq=NEW.source_through_publication_seq
						AND cutover.target_through_publication_seq=NEW.target_through_publication_seq
						AND cutover.materialized_sha256=NEW.materialized_sha256
						AND cutover.event_count=NEW.event_count
						AND cutover.branch_count=NEW.branch_count
						AND cutover.branch_event_count=NEW.branch_event_count
						AND cutover.attempt_count=NEW.attempt_count
						AND cutover.receipt_count=NEW.receipt_count
						AND cutover.checkpoint_count=NEW.checkpoint_count
						AND cutover.artifact_commit_count=NEW.artifact_commit_count
						AND cutover.artifact_ref_count=NEW.artifact_ref_count
						AND cutover.route_count=NEW.route_count
						AND cutover.cursor_count=NEW.cursor_count)))
		)
		BEGIN SELECT RAISE(ABORT,'history activation authority mismatch'); END`,
	`CREATE TRIGGER transcript_history_activation_freeze_cutover
		BEFORE UPDATE OF status,activated ON transcript_history_cutover_runs
		WHEN EXISTS (SELECT 1 FROM transcript_history_activation_receipts receipt
			WHERE receipt.cutover_id=OLD.cutover_id)
			AND (NEW.status<>OLD.status OR NEW.activated<>OLD.activated)
		BEGIN SELECT RAISE(ABORT,'activated history cutover is immutable'); END`,
	`CREATE TRIGGER transcript_history_activation_freeze_ordinary
		BEFORE UPDATE ON transcript_history_ordinary_cutover_runs
		WHEN EXISTS (SELECT 1 FROM transcript_history_activation_receipts receipt
			WHERE receipt.ordinary_cutover_id=OLD.ordinary_cutover_id)
		BEGIN SELECT RAISE(ABORT,'activated ordinary history cutover is immutable'); END`,
	`CREATE TRIGGER transcript_history_activation_immutable
		BEFORE UPDATE ON transcript_history_activation_receipts
		BEGIN SELECT RAISE(ABORT,'history activation receipt is immutable'); END`,
}

func HistoryOrdinaryV33Statements() []string {
	return append([]string(nil), historyOrdinaryV33Statements...)
}

func HistoryOrdinaryV33ObjectNames() []string {
	return []string{
		"transcript_history_ordinary_cutover_runs",
		"transcript_history_ordinary_cursor_map",
		"transcript_history_activation_receipts",
		"transcript_history_activation_source",
		"transcript_history_activation_validate",
		"transcript_history_activation_freeze_cutover",
		"transcript_history_activation_freeze_ordinary",
		"transcript_history_activation_immutable",
	}
}

func HistoryOrdinaryV33CanonicalStatements() []string {
	names := HistoryOrdinaryV33ObjectNames()
	statements := make([]string, 0, len(names))
	for _, name := range names {
		statement, found := historyOrdinaryV33CanonicalStatementByName(name)
		if found {
			statements = append(statements, statement)
		}
	}
	return statements
}

func HistoryActivationReceiptsV33Statement() string {
	return historyActivationReceiptsV33Statement
}

func historyOrdinaryV33CanonicalStatementByName(name string) (string, bool) {
	if name == "transcript_history_activation_receipts" {
		return strings.Replace(historyActivationReceiptsV33Statement,
			"CREATE TABLE transcript_history_activation_receipts (",
			"CREATE TABLE \"transcript_history_activation_receipts\" (", 1), true
	}
	for _, statement := range historyOrdinaryV33Statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		for _, prefix := range []string{"create table ", "create index ", "create unique index ", "create trigger "} {
			if strings.HasPrefix(normalized, prefix+strings.ToLower(name)+" ") ||
				strings.HasPrefix(normalized, prefix+strings.ToLower(name)+"(") {
				return statement, true
			}
		}
	}
	return "", false
}
