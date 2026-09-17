package transcript

import "strings"

const HistoryOrdinaryCursorContractID = "synon.transcript.history-ordinary-cursor.v34"

const historyOrdinaryCursorMapV34Statement = `CREATE TABLE transcript_history_ordinary_cursor_map (
	ordinary_cutover_id BLOB NOT NULL CHECK(length(ordinary_cutover_id) = 32)
		REFERENCES transcript_history_ordinary_cutover_runs(ordinary_cutover_id) ON DELETE NO ACTION,
	source_stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
	target_stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
	source_branch_id TEXT NOT NULL,
	target_branch_id TEXT NOT NULL,
	source_generation INTEGER NOT NULL CHECK(source_generation > 0),
	source_through_publication_seq INTEGER NOT NULL CHECK(source_through_publication_seq >= 0),
	source_message_index INTEGER NOT NULL CHECK(source_message_index >= 0),
	source_ordinal INTEGER NOT NULL CHECK(source_ordinal > 0),
	source_event_id INTEGER NOT NULL CHECK(source_event_id > 0),
	target_event_id INTEGER NOT NULL CHECK(target_event_id > 0),
	target_publication_seq INTEGER NOT NULL CHECK(target_publication_seq > 0),
	target_message_index INTEGER NOT NULL CHECK(target_message_index >= 0),
	stable_message_id TEXT NOT NULL CHECK(length(stable_message_id) > 0),
	created_at TIMESTAMP NOT NULL,
	PRIMARY KEY(ordinary_cutover_id,source_branch_id,source_message_index),
	UNIQUE(ordinary_cutover_id,target_branch_id,target_message_index),
	FOREIGN KEY(source_stream_uid,source_branch_id,source_ordinal)
		REFERENCES transcript_branch_events(stream_uid,branch_id,ordinal) ON DELETE NO ACTION,
	FOREIGN KEY(source_stream_uid,source_event_id)
		REFERENCES transcript_events(stream_uid,event_id) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,target_event_id)
		REFERENCES transcript_events(stream_uid,event_id) ON DELETE NO ACTION,
	FOREIGN KEY(target_stream_uid,target_publication_seq)
		REFERENCES transcript_events(stream_uid,publication_seq) ON DELETE NO ACTION
)`

var historyOrdinaryCursorV34Statements = []string{
	`PRAGMA defer_foreign_keys=ON`,
	strings.Replace(historyOrdinaryCursorMapV34Statement,
		"CREATE TABLE transcript_history_ordinary_cursor_map (",
		"CREATE TABLE transcript_history_ordinary_cursor_map_v34 (", 1),
	`INSERT INTO transcript_history_ordinary_cursor_map_v34(
		ordinary_cutover_id,source_stream_uid,target_stream_uid,source_branch_id,target_branch_id,
		source_generation,source_through_publication_seq,source_message_index,source_ordinal,source_event_id,
		target_event_id,target_publication_seq,target_message_index,stable_message_id,created_at)
	SELECT cursor.ordinary_cutover_id,cursor.source_stream_uid,cursor.target_stream_uid,
		cursor.source_branch_id,cursor.target_branch_id,cutover.branch_generation,
		cutover.source_through_publication_seq,cursor.source_ordinal-1,cursor.source_ordinal,
		cursor.source_event_id,cursor.target_event_id,cursor.target_publication_seq,
		cursor.target_message_index,cursor.stable_message_id,cursor.created_at
	FROM transcript_history_ordinary_cursor_map cursor
	JOIN transcript_history_ordinary_cutover_runs cutover
		ON cutover.ordinary_cutover_id=cursor.ordinary_cutover_id`,
	`DROP TABLE transcript_history_ordinary_cursor_map`,
	`ALTER TABLE transcript_history_ordinary_cursor_map_v34 RENAME TO transcript_history_ordinary_cursor_map`,
	`CREATE TRIGGER transcript_history_ordinary_cursor_validate
		BEFORE INSERT ON transcript_history_ordinary_cursor_map
		WHEN NOT EXISTS (
			SELECT 1 FROM transcript_history_ordinary_cutover_runs cutover
			WHERE cutover.ordinary_cutover_id=NEW.ordinary_cutover_id
				AND cutover.source_stream_uid=NEW.source_stream_uid
				AND cutover.target_stream_uid=NEW.target_stream_uid
				AND cutover.branch_generation=NEW.source_generation
				AND cutover.source_through_publication_seq=NEW.source_through_publication_seq
				AND NEW.target_publication_seq<=cutover.target_through_publication_seq
		) OR NOT EXISTS (
			SELECT 1 FROM transcript_branch_events member
			JOIN transcript_events event ON event.stream_uid=member.stream_uid AND event.event_id=member.event_id
			WHERE member.stream_uid=NEW.source_stream_uid AND member.branch_id=NEW.source_branch_id
				AND member.ordinal=NEW.source_ordinal AND member.event_id=NEW.source_event_id
				AND event.publication_seq<=NEW.source_through_publication_seq
		) OR NOT EXISTS (
			SELECT 1 FROM transcript_branch_events member
			JOIN transcript_events event ON event.stream_uid=member.stream_uid AND event.event_id=member.event_id
			WHERE member.stream_uid=NEW.target_stream_uid AND member.branch_id=NEW.target_branch_id
				AND member.event_id=NEW.target_event_id AND event.publication_seq=NEW.target_publication_seq
		)
		BEGIN SELECT RAISE(ABORT,'ordinary history cursor authority mismatch'); END`,
	`CREATE TRIGGER transcript_history_ordinary_cursor_immutable
		BEFORE UPDATE ON transcript_history_ordinary_cursor_map
		BEGIN SELECT RAISE(ABORT,'ordinary history cursor is immutable'); END`,
	`CREATE TRIGGER transcript_history_ordinary_cursor_delete_active
		BEFORE DELETE ON transcript_history_ordinary_cursor_map
		WHEN EXISTS (SELECT 1 FROM transcript_history_activation_receipts receipt
			WHERE receipt.ordinary_cutover_id=OLD.ordinary_cutover_id AND receipt.status='active')
		BEGIN SELECT RAISE(ABORT,'active ordinary history cursor cannot be deleted'); END`,
}

func HistoryOrdinaryCursorV34Statements() []string {
	return append([]string(nil), historyOrdinaryCursorV34Statements...)
}

func HistoryOrdinaryCursorV34ObjectNames() []string {
	return []string{
		"transcript_history_ordinary_cursor_map",
		"transcript_history_ordinary_cursor_validate",
		"transcript_history_ordinary_cursor_immutable",
		"transcript_history_ordinary_cursor_delete_active",
	}
}

func HistoryOrdinaryCursorV34CanonicalStatements() []string {
	result := make([]string, 0, len(HistoryOrdinaryCursorV34ObjectNames()))
	for _, name := range HistoryOrdinaryCursorV34ObjectNames() {
		statement, found := historyOrdinaryCursorV34CanonicalStatementByName(name)
		if found {
			result = append(result, statement)
		}
	}
	return result
}

func HistoryOrdinaryCursorMapV34Statement() string {
	return historyOrdinaryCursorMapV34Statement
}

func historyOrdinaryCursorV34CanonicalStatementByName(name string) (string, bool) {
	if name == "transcript_history_ordinary_cursor_map" {
		return strings.Replace(historyOrdinaryCursorMapV34Statement,
			"CREATE TABLE transcript_history_ordinary_cursor_map (",
			"CREATE TABLE \"transcript_history_ordinary_cursor_map\" (", 1), true
	}
	for _, statement := range historyOrdinaryCursorV34Statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.HasPrefix(normalized, "create trigger "+strings.ToLower(name)+" ") {
			return statement, true
		}
	}
	return "", false
}
