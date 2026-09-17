package transcript

const HistoryActivationContractID = "synon.transcript.history-activation.v31"

type historyActivationSchemaObject struct {
	name, statement string
}

var historyActivationV31Objects = []historyActivationSchemaObject{
	{"transcript_history_cutover_activation_identity", `CREATE UNIQUE INDEX transcript_history_cutover_activation_identity
		ON transcript_history_cutover_runs(cutover_id,stream_uid,owner_id)`},
	{"transcript_stream_authority_identity", `CREATE UNIQUE INDEX transcript_stream_authority_identity
		ON transcript_streams(stream_uid,owner_id,session_id,epoch)`,
	},
	{"transcript_history_activation_receipts", `CREATE TABLE transcript_history_activation_receipts (
		activation_id BLOB PRIMARY KEY CHECK(length(activation_id) = 32),
		cutover_id BLOB NOT NULL UNIQUE CHECK(length(cutover_id) = 32)
			REFERENCES transcript_history_cutover_runs(cutover_id) ON DELETE NO ACTION,
		source_stream_uid TEXT NOT NULL
			REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
		target_stream_uid TEXT NOT NULL UNIQUE
			REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
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
		FOREIGN KEY(source_stream_uid,owner_id,session_id,source_epoch)
			REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE NO ACTION,
		FOREIGN KEY(target_stream_uid,owner_id,session_id,target_epoch)
			REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE NO ACTION,
		FOREIGN KEY(target_stream_uid,active_branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION
)`},
	{"transcript_frame_authority", `CREATE TABLE transcript_frame_authority (
		owner_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		active_stream_uid TEXT NOT NULL UNIQUE
			REFERENCES transcript_streams(stream_uid) ON DELETE CASCADE,
		active_epoch INTEGER NOT NULL CHECK(active_epoch > 0),
		authority_generation INTEGER NOT NULL CHECK(authority_generation > 0),
		read_authority TEXT NOT NULL CHECK(read_authority IN ('legacy_mixed_v1','transcript_payload_v1')),
		write_authority TEXT NOT NULL CHECK(write_authority IN ('legacy_frame_ref_v1','transcript_payload_v1')),
		activation_id BLOB
			REFERENCES transcript_history_activation_receipts(activation_id) ON DELETE NO ACTION,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY(owner_id,session_id),
		UNIQUE(owner_id,session_id,authority_generation),
		FOREIGN KEY(active_stream_uid,owner_id,session_id,active_epoch)
			REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE CASCADE,
		FOREIGN KEY(activation_id,active_stream_uid,active_epoch,authority_generation)
			REFERENCES transcript_history_activation_receipts(
				activation_id,target_stream_uid,target_epoch,authority_generation) ON DELETE NO ACTION,
		CHECK(
			(read_authority='legacy_mixed_v1' AND write_authority='legacy_frame_ref_v1' AND activation_id IS NULL) OR
			(read_authority='transcript_payload_v1' AND write_authority='transcript_payload_v1' AND activation_id IS NOT NULL)
		)
)`},
	{"transcript_history_activation_source", `CREATE INDEX transcript_history_activation_source
		ON transcript_history_activation_receipts(source_stream_uid,target_epoch,activated_at)`},
	{"transcript_frame_authority_stream", `CREATE INDEX transcript_frame_authority_stream
		ON transcript_frame_authority(active_stream_uid,authority_generation)`},
	{"transcript_history_activation_validate", `CREATE TRIGGER transcript_history_activation_validate
		BEFORE INSERT ON transcript_history_activation_receipts
		WHEN NOT EXISTS (
			SELECT 1 FROM transcript_history_cutover_runs cutover
			JOIN transcript_streams source ON source.stream_uid=NEW.source_stream_uid
			JOIN transcript_streams target ON target.stream_uid=NEW.target_stream_uid
			WHERE cutover.cutover_id=NEW.cutover_id AND cutover.stream_uid=NEW.source_stream_uid
				AND cutover.owner_id=NEW.owner_id AND source.owner_id=NEW.owner_id
				AND target.owner_id=NEW.owner_id AND source.session_id=NEW.session_id
				AND target.session_id=NEW.session_id AND source.kind='frame_ref' AND target.kind='frame_ref'
				AND source.epoch=NEW.source_epoch AND target.epoch=NEW.target_epoch
		)
		BEGIN SELECT RAISE(ABORT,'history activation authority mismatch'); END`},
	{"transcript_history_activation_freeze_cutover", `CREATE TRIGGER transcript_history_activation_freeze_cutover
		BEFORE UPDATE OF status,activated ON transcript_history_cutover_runs
		WHEN EXISTS (SELECT 1 FROM transcript_history_activation_receipts receipt
			WHERE receipt.cutover_id=OLD.cutover_id)
			AND (NEW.status<>OLD.status OR NEW.activated<>OLD.activated)
		BEGIN SELECT RAISE(ABORT,'activated history cutover is immutable'); END`},
	{"transcript_frame_authority_transition", `CREATE TRIGGER transcript_frame_authority_transition
		BEFORE UPDATE ON transcript_frame_authority
		WHEN NEW.owner_id<>OLD.owner_id OR NEW.session_id<>OLD.session_id
			OR NEW.active_stream_uid<>OLD.active_stream_uid OR NEW.active_epoch<>OLD.active_epoch
			OR NEW.authority_generation<>OLD.authority_generation OR NEW.read_authority<>OLD.read_authority
			OR NEW.write_authority<>OLD.write_authority OR NEW.activation_id IS NOT OLD.activation_id
		BEGIN
			SELECT CASE WHEN OLD.activation_id IS NOT NULL OR NEW.activation_id IS NULL
				OR NEW.authority_generation<>OLD.authority_generation+1
				OR NEW.active_epoch<>OLD.active_epoch+1
				OR NEW.read_authority<>'transcript_payload_v1' OR NEW.write_authority<>'transcript_payload_v1'
			THEN RAISE(ABORT,'invalid frame authority transition') END;
		END`},
}

var historyActivationV31Statements = func() []string {
	result := make([]string, len(historyActivationV31Objects))
	for index, object := range historyActivationV31Objects {
		result[index] = object.statement
	}
	return result
}()

func HistoryActivationV31Statements() []string {
	return append([]string(nil), historyActivationV31Statements...)
}

func HistoryActivationV31TableNames() []string {
	return []string{"transcript_history_activation_receipts", "transcript_frame_authority"}
}

func HistoryActivationV31ObjectNames() []string {
	result := make([]string, len(historyActivationV31Objects))
	for index, object := range historyActivationV31Objects {
		result[index] = object.name
	}
	return result
}

// HistoryActivationV31RetainedObjects returns the v31 objects whose exact SQL
// remains authoritative after v32 replaces the frame-authority table cohort.
func HistoryActivationV31RetainedObjects() ([]string, []string) {
	replaced := map[string]bool{
		"transcript_frame_authority":            true,
		"transcript_frame_authority_stream":     true,
		"transcript_frame_authority_transition": true,
	}
	names := make([]string, 0, len(historyActivationV31Objects)-len(replaced))
	statements := make([]string, 0, len(historyActivationV31Objects)-len(replaced))
	for _, object := range historyActivationV31Objects {
		if replaced[object.name] {
			continue
		}
		names = append(names, object.name)
		statements = append(statements, object.statement)
	}
	return names, statements
}

func historyActivationV31StatementByName(name string) (string, bool) {
	for _, object := range historyActivationV31Objects {
		if object.name == name {
			return object.statement, true
		}
	}
	return "", false
}
