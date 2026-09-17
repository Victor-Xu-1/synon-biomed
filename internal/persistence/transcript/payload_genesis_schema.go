package transcript

const HistoryPayloadGenesisContractID = "synon.transcript.payload-genesis.v32"

type payloadGenesisSchemaObject struct {
	name, statement string
}

const transcriptPayloadGenesisReceiptsV32Statement = `CREATE TABLE transcript_payload_genesis_receipts (
		genesis_id BLOB PRIMARY KEY CHECK(length(genesis_id) = 32),
		stream_uid TEXT NOT NULL UNIQUE
			REFERENCES transcript_streams(stream_uid) ON DELETE NO ACTION,
		owner_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		epoch INTEGER NOT NULL CHECK(epoch = 1),
		active_branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation > 0),
		authority_generation INTEGER NOT NULL CHECK(authority_generation = 1),
		source_kind TEXT NOT NULL CHECK(source_kind IN ('empty','frame_import','canonical_clone')),
		source_stream_uid TEXT,
		source_epoch INTEGER,
		source_event_count INTEGER NOT NULL CHECK(source_event_count >= 0),
		source_sha256 BLOB NOT NULL CHECK(length(source_sha256) = 32),
		status TEXT NOT NULL CHECK(status = 'active'),
		created_at TIMESTAMP NOT NULL,
		UNIQUE(genesis_id,stream_uid,epoch,authority_generation),
		FOREIGN KEY(stream_uid,owner_id,session_id,epoch)
			REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE NO ACTION,
		FOREIGN KEY(stream_uid,active_branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE NO ACTION,
		CHECK(
			(source_kind='empty' AND source_stream_uid IS NULL AND source_epoch IS NULL
				AND source_event_count=0) OR
			(source_kind='frame_import' AND source_stream_uid IS NULL AND source_epoch IS NULL
				AND source_event_count>0) OR
			(source_kind='canonical_clone' AND length(source_stream_uid)>0 AND source_epoch>0)
		)
)`

const transcriptFrameAuthorityV32Statement = `CREATE TABLE transcript_frame_authority (
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
		genesis_id BLOB
			REFERENCES transcript_payload_genesis_receipts(genesis_id) ON DELETE NO ACTION,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY(owner_id,session_id),
		UNIQUE(owner_id,session_id,authority_generation),
		FOREIGN KEY(active_stream_uid,owner_id,session_id,active_epoch)
			REFERENCES transcript_streams(stream_uid,owner_id,session_id,epoch) ON DELETE CASCADE,
		FOREIGN KEY(activation_id,active_stream_uid,active_epoch,authority_generation)
			REFERENCES transcript_history_activation_receipts(
				activation_id,target_stream_uid,target_epoch,authority_generation) ON DELETE NO ACTION,
		FOREIGN KEY(genesis_id,active_stream_uid,active_epoch,authority_generation)
			REFERENCES transcript_payload_genesis_receipts(
				genesis_id,stream_uid,epoch,authority_generation) ON DELETE NO ACTION,
		CHECK(
			(read_authority='legacy_mixed_v1' AND write_authority='legacy_frame_ref_v1'
				AND activation_id IS NULL AND genesis_id IS NULL) OR
			(read_authority='transcript_payload_v1' AND write_authority='transcript_payload_v1'
				AND ((activation_id IS NOT NULL AND genesis_id IS NULL) OR
					(activation_id IS NULL AND genesis_id IS NOT NULL)))
		)
)`

const transcriptPayloadGenesisValidateV32Statement = `CREATE TRIGGER transcript_payload_genesis_validate
		BEFORE INSERT ON transcript_payload_genesis_receipts
		WHEN NOT EXISTS (
			SELECT 1 FROM transcript_streams stream
			JOIN transcript_branch_state state ON state.stream_uid=stream.stream_uid
			JOIN transcript_branches branch
				ON branch.stream_uid=stream.stream_uid AND branch.branch_id=state.active_branch_id
			JOIN frames frame ON frame.id=stream.frame_id AND frame.project_id=stream.project_id
			JOIN projects project ON project.id=frame.project_id
			WHERE stream.stream_uid=NEW.stream_uid AND stream.owner_id=NEW.owner_id
				AND stream.session_id=NEW.session_id AND stream.epoch=NEW.epoch
				AND stream.kind='frame_ref' AND NEW.epoch=1
				AND project.user_id=NEW.owner_id AND frame.root_frame_id=stream.root_frame_id
				AND state.active_branch_id=NEW.active_branch_id
				AND state.generation=NEW.branch_generation
				AND NEW.authority_generation=1
				AND NEW.source_event_count=(
					SELECT COUNT(*) FROM transcript_events imported
					WHERE imported.stream_uid=stream.stream_uid AND imported.source='payload'
				)
				AND (
					(NEW.source_kind='empty' AND NEW.source_event_count=0) OR
					(NEW.source_kind='frame_import'
						AND NEW.source_event_count=(
							SELECT COUNT(*) FROM frame_events legacy WHERE legacy.frame_id=stream.frame_id
								AND legacy.event_type IN ('message','user_message','assistant_message',
									'system_message','tool_use','tool_result','ask_user_answer')
						)
						AND NEW.source_event_count=(
							SELECT COUNT(*) FROM transcript_events imported
							WHERE imported.stream_uid=stream.stream_uid
								AND imported.client_message_id LIKE 'payload-genesis:%'
						)) OR
					(NEW.source_kind='canonical_clone' AND NEW.source_stream_uid<>NEW.stream_uid
						AND NEW.source_epoch>0)
				)
		)
		BEGIN SELECT RAISE(ABORT,'payload genesis authority mismatch'); END`

const transcriptPayloadGenesisImmutableV32Statement = `CREATE TRIGGER transcript_payload_genesis_immutable
		BEFORE UPDATE ON transcript_payload_genesis_receipts
		BEGIN SELECT RAISE(ABORT,'payload genesis receipt is immutable'); END`

const transcriptFrameAuthorityTransitionV32Statement = `CREATE TRIGGER transcript_frame_authority_transition
		BEFORE UPDATE ON transcript_frame_authority
		WHEN NEW.owner_id<>OLD.owner_id OR NEW.session_id<>OLD.session_id
			OR NEW.active_stream_uid<>OLD.active_stream_uid OR NEW.active_epoch<>OLD.active_epoch
			OR NEW.authority_generation<>OLD.authority_generation OR NEW.read_authority<>OLD.read_authority
			OR NEW.write_authority<>OLD.write_authority OR NEW.activation_id IS NOT OLD.activation_id
			OR NEW.genesis_id IS NOT OLD.genesis_id
		BEGIN
			SELECT CASE WHEN OLD.activation_id IS NOT NULL OR OLD.genesis_id IS NOT NULL
				OR NEW.activation_id IS NULL OR NEW.genesis_id IS NOT NULL
				OR NEW.authority_generation<>OLD.authority_generation+1
				OR NEW.active_epoch<>OLD.active_epoch+1
				OR NEW.read_authority<>'transcript_payload_v1' OR NEW.write_authority<>'transcript_payload_v1'
			THEN RAISE(ABORT,'invalid frame authority transition') END;
		END`

var payloadGenesisV32CanonicalObjects = []payloadGenesisSchemaObject{
	{"transcript_payload_genesis_receipts", transcriptPayloadGenesisReceiptsV32Statement},
	{"transcript_payload_genesis_validate", transcriptPayloadGenesisValidateV32Statement},
	{"transcript_payload_genesis_immutable", transcriptPayloadGenesisImmutableV32Statement},
	{"transcript_frame_authority", transcriptFrameAuthorityV32Statement},
	{"transcript_frame_authority_stream", `CREATE INDEX transcript_frame_authority_stream
		ON transcript_frame_authority(active_stream_uid,authority_generation)`},
	{"transcript_frame_authority_transition", transcriptFrameAuthorityTransitionV32Statement},
}

var payloadGenesisV32Statements = []string{
	transcriptPayloadGenesisReceiptsV32Statement,
	transcriptPayloadGenesisValidateV32Statement,
	transcriptPayloadGenesisImmutableV32Statement,
	`DROP TRIGGER transcript_frame_authority_transition`,
	`DROP INDEX transcript_frame_authority_stream`,
	`ALTER TABLE transcript_frame_authority RENAME TO transcript_frame_authority_v31_staging`,
	transcriptFrameAuthorityV32Statement,
	`INSERT INTO transcript_frame_authority(
		owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
		read_authority,write_authority,activation_id,genesis_id,updated_at)
		SELECT owner_id,session_id,active_stream_uid,active_epoch,authority_generation,
			read_authority,write_authority,activation_id,NULL,updated_at
		FROM transcript_frame_authority_v31_staging`,
	`DROP TABLE transcript_frame_authority_v31_staging`,
	`CREATE INDEX transcript_frame_authority_stream
		ON transcript_frame_authority(active_stream_uid,authority_generation)`,
	transcriptFrameAuthorityTransitionV32Statement,
}

func PayloadGenesisV32Statements() []string {
	return append([]string(nil), payloadGenesisV32Statements...)
}

func PayloadGenesisV32CanonicalObjectNames() []string {
	result := make([]string, len(payloadGenesisV32CanonicalObjects))
	for index, object := range payloadGenesisV32CanonicalObjects {
		result[index] = object.name
	}
	return result
}

func PayloadGenesisV32CanonicalStatements() []string {
	result := make([]string, len(payloadGenesisV32CanonicalObjects))
	for index, object := range payloadGenesisV32CanonicalObjects {
		result[index] = object.statement
	}
	return result
}

func payloadGenesisV32CanonicalStatementByName(name string) (string, bool) {
	for _, object := range payloadGenesisV32CanonicalObjects {
		if object.name == name {
			return object.statement, true
		}
	}
	return "", false
}
