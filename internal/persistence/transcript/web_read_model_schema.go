package transcript

import "strings"

const WebReadModelContractID = "synon.transcript.web-read-model.v38"

type webReadModelV38SchemaObject struct {
	name      string
	statement string
}

const transcriptBranchHeadsV38Statement = `CREATE TABLE transcript_branch_heads (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		through_ordinal INTEGER NOT NULL CHECK(through_ordinal>=0),
		tail_event_id INTEGER,
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id)
			REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		FOREIGN KEY(stream_uid,tail_event_id)
			REFERENCES transcript_events(stream_uid,event_id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
		CHECK((through_ordinal=0 AND tail_event_id IS NULL AND through_publication_seq=0) OR
			(through_ordinal>0 AND tail_event_id IS NOT NULL AND through_publication_seq>0))
	) STRICT`

const transcriptBranchHeadsInsertV38Statement = `CREATE TRIGGER transcript_branch_heads_insert
		AFTER INSERT ON transcript_branches
		BEGIN
			INSERT INTO transcript_branch_heads(
				stream_uid,branch_id,through_ordinal,tail_event_id,through_publication_seq,source_revision
			) VALUES(NEW.stream_uid,NEW.branch_id,0,NULL,0,0);
		END`

const transcriptBranchEventsHeadValidateV38Statement = `CREATE TRIGGER transcript_branch_events_head_validate
		BEFORE INSERT ON transcript_branch_events
		WHEN NOT EXISTS (
			SELECT 1 FROM transcript_branch_heads head
			JOIN transcript_events event
				ON event.stream_uid=NEW.stream_uid AND event.event_id=NEW.event_id
			WHERE head.stream_uid=NEW.stream_uid AND head.branch_id=NEW.branch_id
				AND NEW.ordinal=head.through_ordinal+1
				AND event.publication_seq>head.through_publication_seq
		)
		BEGIN SELECT RAISE(ABORT,'transcript branch head mismatch'); END`

const transcriptBranchEventsHeadAdvanceV38Statement = `CREATE TRIGGER transcript_branch_events_head_advance
		AFTER INSERT ON transcript_branch_events
		BEGIN
			UPDATE transcript_branch_heads
			SET through_ordinal=NEW.ordinal,
				tail_event_id=NEW.event_id,
				through_publication_seq=(
					SELECT publication_seq FROM transcript_events
					WHERE stream_uid=NEW.stream_uid AND event_id=NEW.event_id
				)
			WHERE stream_uid=NEW.stream_uid AND branch_id=NEW.branch_id;
		END`

const transcriptBranchEventsImmutableV38Statement = `CREATE TRIGGER transcript_branch_events_immutable
		BEFORE UPDATE ON transcript_branch_events
		BEGIN SELECT RAISE(ABORT,'transcript branch membership is immutable'); END`

const transcriptBranchEventsDeleteGuardV38Statement = `CREATE TRIGGER transcript_branch_events_delete_guard
		BEFORE DELETE ON transcript_branch_events
		WHEN EXISTS (
			SELECT 1 FROM transcript_branches branch
			WHERE branch.stream_uid=OLD.stream_uid AND branch.branch_id=OLD.branch_id
		)
		BEGIN SELECT RAISE(ABORT,'transcript branch membership is immutable'); END`

const transcriptBranchHeadsUpdateGuardV38Statement = `CREATE TRIGGER transcript_branch_heads_update_guard
		BEFORE UPDATE ON transcript_branch_heads
		WHEN NEW.stream_uid<>OLD.stream_uid OR NEW.branch_id<>OLD.branch_id
			OR NOT (
				(NEW.source_revision=OLD.source_revision
					AND NEW.through_ordinal=OLD.through_ordinal+1 AND NEW.tail_event_id IS NOT NULL
					AND EXISTS (
						SELECT 1 FROM transcript_branch_events membership
						JOIN transcript_events event
							ON event.stream_uid=membership.stream_uid AND event.event_id=membership.event_id
						WHERE membership.stream_uid=NEW.stream_uid AND membership.branch_id=NEW.branch_id
							AND membership.ordinal=NEW.through_ordinal
							AND membership.event_id=NEW.tail_event_id
							AND event.publication_seq=NEW.through_publication_seq
							AND event.publication_seq>OLD.through_publication_seq
					))
				OR
				(NEW.through_ordinal=OLD.through_ordinal AND NEW.tail_event_id IS OLD.tail_event_id
					AND NEW.through_publication_seq=OLD.through_publication_seq
					AND NEW.source_revision=OLD.source_revision+1
					AND EXISTS (
						SELECT 1 FROM transcript_web_projection_dirty dirty
						WHERE dirty.stream_uid=NEW.stream_uid AND dirty.branch_id=NEW.branch_id
							AND dirty.source_revision=NEW.source_revision
					))
			)
		BEGIN SELECT RAISE(ABORT,'transcript branch head transition is invalid'); END`

const transcriptBranchHeadsDeleteGuardV38Statement = `CREATE TRIGGER transcript_branch_heads_delete_guard
		BEFORE DELETE ON transcript_branch_heads
		WHEN EXISTS (
			SELECT 1 FROM transcript_branches branch
			WHERE branch.stream_uid=OLD.stream_uid AND branch.branch_id=OLD.branch_id
		)
		BEGIN SELECT RAISE(ABORT,'transcript branch head is immutable'); END`

const transcriptArtifactReferenceHeadsV38Statement = `CREATE TABLE transcript_artifact_reference_heads (
		stream_uid TEXT PRIMARY KEY,
		revision INTEGER NOT NULL CHECK(revision>=0),
		FOREIGN KEY(stream_uid) REFERENCES transcript_streams(stream_uid) ON DELETE CASCADE
	) STRICT`

const transcriptArtifactReferenceHeadsInsertV38Statement = `CREATE TRIGGER transcript_artifact_reference_heads_insert
		AFTER INSERT ON transcript_streams
		BEGIN
			INSERT INTO transcript_artifact_reference_heads(stream_uid,revision) VALUES(NEW.stream_uid,0);
		END`

const transcriptArtifactReferencesHeadRequiredV38Statement = `CREATE TRIGGER transcript_artifact_references_head_required
		BEFORE INSERT ON transcript_artifact_refs
		WHEN NOT EXISTS (
			SELECT 1 FROM transcript_artifact_reference_heads head WHERE head.stream_uid=NEW.stream_uid
		)
		BEGIN SELECT RAISE(ABORT,'transcript artifact reference head is missing'); END`

const transcriptArtifactReferencesHeadAdvanceV38Statement = `CREATE TRIGGER transcript_artifact_references_head_advance
		AFTER INSERT ON transcript_artifact_refs
		BEGIN
			UPDATE transcript_artifact_reference_heads
			SET revision=revision+1
			WHERE stream_uid=NEW.stream_uid;
			INSERT INTO transcript_web_projection_dirty(
				stream_uid,branch_id,source_revision,first_affected_ordinal,reason_mask
			)
			SELECT membership.stream_uid,membership.branch_id,head.source_revision+1,membership.ordinal,1
			FROM transcript_branch_events membership
			JOIN transcript_branch_heads head
				ON head.stream_uid=membership.stream_uid AND head.branch_id=membership.branch_id
			WHERE membership.stream_uid=NEW.stream_uid AND membership.event_id=NEW.source_event_id
			ON CONFLICT(stream_uid,branch_id) DO UPDATE SET
				source_revision=excluded.source_revision,
				first_affected_ordinal=MIN(first_affected_ordinal,excluded.first_affected_ordinal),
				reason_mask=reason_mask|excluded.reason_mask;
			UPDATE transcript_branch_heads
			SET source_revision=(SELECT dirty.source_revision FROM transcript_web_projection_dirty dirty
				WHERE dirty.stream_uid=transcript_branch_heads.stream_uid
					AND dirty.branch_id=transcript_branch_heads.branch_id)
			WHERE stream_uid=NEW.stream_uid AND branch_id IN (
				SELECT branch_id FROM transcript_branch_events membership
				WHERE membership.stream_uid=NEW.stream_uid AND membership.event_id=NEW.source_event_id
			);
		END`

const transcriptArtifactReferencesStructuralImmutableV38Statement = `CREATE TRIGGER transcript_artifact_references_structural_immutable
		BEFORE UPDATE ON transcript_artifact_refs
		WHEN NEW.stream_uid<>OLD.stream_uid OR NEW.runner_attempt<>OLD.runner_attempt
			OR NEW.source_event_id<>OLD.source_event_id OR NEW.ordinal<>OLD.ordinal
			OR NEW.artifact_id<>OLD.artifact_id OR NEW.version_id<>OLD.version_id
			OR NEW.relation<>OLD.relation OR NEW.created_at<>OLD.created_at
		BEGIN SELECT RAISE(ABORT,'transcript artifact reference identity is immutable'); END`

const transcriptArtifactReferencesProjectionRetireV38Statement = `CREATE TRIGGER transcript_artifact_references_projection_retire
		BEFORE DELETE ON transcript_artifact_refs
		BEGIN
			DELETE FROM transcript_web_message_artifact_refs
			WHERE stream_uid=OLD.stream_uid AND runner_attempt=OLD.runner_attempt
				AND source_event_id=OLD.source_event_id AND source_reference_ordinal=OLD.ordinal
				AND artifact_id=OLD.artifact_id AND version_id=OLD.version_id AND relation=OLD.relation;
		END`

const transcriptArtifactReferencesHeadRetireV38Statement = `CREATE TRIGGER transcript_artifact_references_head_retire
		AFTER DELETE ON transcript_artifact_refs
		BEGIN
			INSERT INTO transcript_web_projection_dirty(
				stream_uid,branch_id,source_revision,first_affected_ordinal,reason_mask
			)
			SELECT membership.stream_uid,membership.branch_id,head.source_revision+1,membership.ordinal,1
			FROM transcript_branch_events membership
			JOIN transcript_branch_heads head
				ON head.stream_uid=membership.stream_uid AND head.branch_id=membership.branch_id
			WHERE membership.stream_uid=OLD.stream_uid AND membership.event_id=OLD.source_event_id
			ON CONFLICT(stream_uid,branch_id) DO UPDATE SET
				source_revision=excluded.source_revision,
				first_affected_ordinal=MIN(first_affected_ordinal,excluded.first_affected_ordinal),
				reason_mask=reason_mask|excluded.reason_mask;
			UPDATE transcript_branch_heads
			SET source_revision=(SELECT dirty.source_revision FROM transcript_web_projection_dirty dirty
				WHERE dirty.stream_uid=transcript_branch_heads.stream_uid
					AND dirty.branch_id=transcript_branch_heads.branch_id)
			WHERE stream_uid=OLD.stream_uid AND branch_id IN (
				SELECT branch_id FROM transcript_branch_events membership
				WHERE membership.stream_uid=OLD.stream_uid AND membership.event_id=OLD.source_event_id
			);
			UPDATE transcript_artifact_reference_heads SET revision=revision+1 WHERE stream_uid=OLD.stream_uid;
		END`

const transcriptArtifactReferenceHeadsUpdateGuardV38Statement = `CREATE TRIGGER transcript_artifact_reference_heads_update_guard
		BEFORE UPDATE ON transcript_artifact_reference_heads
		WHEN NEW.stream_uid<>OLD.stream_uid OR NEW.revision<>OLD.revision+1
		BEGIN SELECT RAISE(ABORT,'transcript artifact reference head transition is invalid'); END`

const transcriptArtifactReferenceHeadsDeleteGuardV38Statement = `CREATE TRIGGER transcript_artifact_reference_heads_delete_guard
		BEFORE DELETE ON transcript_artifact_reference_heads
		WHEN EXISTS (
			SELECT 1 FROM transcript_streams stream WHERE stream.stream_uid=OLD.stream_uid
		)
		BEGIN SELECT RAISE(ABORT,'transcript artifact reference head is immutable'); END`

const transcriptWebProjectionDirtyV38Statement = `CREATE TABLE transcript_web_projection_dirty (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		source_revision INTEGER NOT NULL CHECK(source_revision>0),
		first_affected_ordinal INTEGER NOT NULL CHECK(first_affected_ordinal>0),
		reason_mask INTEGER NOT NULL CHECK(reason_mask>0),
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE
	) STRICT`

const transcriptEventsDirtyBranchesV38Statement = `CREATE TRIGGER transcript_events_dirty_branches
		AFTER UPDATE OF event_type,source,runner_attempt,payload_json,frame_event_id ON transcript_events
		WHEN NEW.event_type<>OLD.event_type OR NEW.source<>OLD.source
			OR NEW.runner_attempt IS NOT OLD.runner_attempt OR NEW.payload_json IS NOT OLD.payload_json
			OR NEW.frame_event_id IS NOT OLD.frame_event_id
		BEGIN
			INSERT INTO transcript_web_projection_dirty(
				stream_uid,branch_id,source_revision,first_affected_ordinal,reason_mask
			)
			SELECT membership.stream_uid,membership.branch_id,head.source_revision+1,membership.ordinal,2
			FROM transcript_branch_events membership
			JOIN transcript_branch_heads head
				ON head.stream_uid=membership.stream_uid AND head.branch_id=membership.branch_id
			WHERE membership.stream_uid=NEW.stream_uid AND membership.event_id=NEW.event_id
			ON CONFLICT(stream_uid,branch_id) DO UPDATE SET
				source_revision=excluded.source_revision,
				first_affected_ordinal=MIN(first_affected_ordinal,excluded.first_affected_ordinal),
				reason_mask=reason_mask|excluded.reason_mask;
			UPDATE transcript_branch_heads
			SET source_revision=(SELECT dirty.source_revision FROM transcript_web_projection_dirty dirty
				WHERE dirty.stream_uid=transcript_branch_heads.stream_uid
					AND dirty.branch_id=transcript_branch_heads.branch_id)
			WHERE stream_uid=NEW.stream_uid AND branch_id IN (
				SELECT branch_id FROM transcript_branch_events membership
				WHERE membership.stream_uid=NEW.stream_uid AND membership.event_id=NEW.event_id
			);
		END`

const transcriptWebProjectionStateV38Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version=1),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV47Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV48Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV49Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3,4)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV58Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3,4,5)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV59Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3,4,5,6)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV60Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3,4,5,6,7)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV64Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3,4,5,6,7,8)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV65Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3,4,5,6,7,8,9)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV66Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3,4,5,6,7,8,9,10)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebProjectionStateV67Statement = `CREATE TABLE transcript_web_projection_state (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		projector_version INTEGER NOT NULL CHECK(projector_version IN (1,2,3,4,5,6,7,8,9,10,11)),
		projection_revision INTEGER NOT NULL CHECK(projection_revision>0),
		through_publication_seq INTEGER NOT NULL CHECK(through_publication_seq>=0),
		source_revision INTEGER NOT NULL CHECK(source_revision>=0),
		message_count INTEGER NOT NULL CHECK(message_count>=0),
		visible_message_count INTEGER NOT NULL CHECK(visible_message_count>=0 AND visible_message_count<=message_count),
		message_artifact_reference_count INTEGER NOT NULL CHECK(message_artifact_reference_count>=0),
		projector_state_json TEXT NOT NULL CHECK(json_valid(projector_state_json)),
		projector_state_sha256 TEXT NOT NULL CHECK(length(projector_state_sha256)=64),
		source_chain_sha256 TEXT NOT NULL CHECK(length(source_chain_sha256)=64),
		status TEXT NOT NULL CHECK(status IN ('building','ready','quarantined')),
		last_error_code TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_branches(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((status='quarantined' AND length(last_error_code)>0) OR (status!='quarantined' AND last_error_code=''))
	) STRICT`

const transcriptWebMessagesV38Statement = `CREATE TABLE transcript_web_messages (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL CHECK(ordinal>0),
		message_id TEXT NOT NULL CHECK(length(message_id)>0),
		client_message_id TEXT NOT NULL CHECK(length(client_message_id)>0),
		visible INTEGER NOT NULL CHECK(visible IN (0,1)),
		visible_index INTEGER,
		message_json TEXT NOT NULL CHECK(json_valid(message_json)),
		message_sha256 TEXT NOT NULL CHECK(length(message_sha256)=64),
		first_publication_seq INTEGER NOT NULL CHECK(first_publication_seq>0),
		last_publication_seq INTEGER NOT NULL CHECK(last_publication_seq>=first_publication_seq),
		updated_at TEXT NOT NULL,
		PRIMARY KEY(stream_uid,branch_id,ordinal),
		UNIQUE(stream_uid,branch_id,message_id),
		UNIQUE(stream_uid,branch_id,client_message_id),
		FOREIGN KEY(stream_uid,branch_id) REFERENCES transcript_web_projection_state(stream_uid,branch_id) ON DELETE CASCADE,
		CHECK((visible=1 AND visible_index IS NOT NULL AND visible_index>=0) OR
			(visible=0 AND visible_index IS NULL))
	) STRICT`

const transcriptWebMessageIdentitiesV38Statement = `CREATE TABLE transcript_web_message_identities (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		identity TEXT NOT NULL CHECK(length(identity)>0),
		message_ordinal INTEGER NOT NULL CHECK(message_ordinal>0),
		kind TEXT NOT NULL CHECK(kind IN ('message_id','client_message_id','both')),
		PRIMARY KEY(stream_uid,branch_id,identity),
		UNIQUE(stream_uid,branch_id,message_ordinal,kind),
		FOREIGN KEY(stream_uid,branch_id,message_ordinal)
			REFERENCES transcript_web_messages(stream_uid,branch_id,ordinal) ON DELETE CASCADE
	) STRICT`

const transcriptWebMessageArtifactRefsV38Statement = `CREATE TABLE transcript_web_message_artifact_refs (
		stream_uid TEXT NOT NULL,
		branch_id TEXT NOT NULL,
		message_ordinal INTEGER NOT NULL CHECK(message_ordinal>0),
		ordinal INTEGER NOT NULL CHECK(ordinal>=0),
		source_event_id INTEGER NOT NULL CHECK(source_event_id>0),
		runner_attempt INTEGER,
		source_reference_ordinal INTEGER NOT NULL CHECK(source_reference_ordinal>=0),
		artifact_id TEXT NOT NULL CHECK(length(artifact_id)>0),
		version_id TEXT NOT NULL CHECK(length(version_id)>0),
		relation TEXT NOT NULL CHECK(relation IN ('produced','consumed','cited','attached')),
		filename TEXT NOT NULL DEFAULT '',
		content_type TEXT NOT NULL DEFAULT '',
		size_bytes INTEGER NOT NULL DEFAULT 0 CHECK(size_bytes>=0),
		checksum TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(stream_uid,branch_id,message_ordinal,ordinal),
		UNIQUE(stream_uid,branch_id,message_ordinal,artifact_id,version_id),
		FOREIGN KEY(stream_uid,branch_id,message_ordinal)
			REFERENCES transcript_web_messages(stream_uid,branch_id,ordinal) ON DELETE CASCADE,
		FOREIGN KEY(stream_uid,branch_id,source_event_id)
			REFERENCES transcript_branch_events(stream_uid,branch_id,event_id)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
		FOREIGN KEY(stream_uid,runner_attempt,source_event_id,source_reference_ordinal,artifact_id,version_id,relation)
			REFERENCES transcript_artifact_refs(stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation)
			ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED,
		CHECK((runner_attempt IS NOT NULL AND runner_attempt>0 AND filename='' AND content_type='' AND checksum='') OR
			(runner_attempt IS NULL AND relation='attached' AND filename<>'' AND content_type<>'' AND checksum<>''))
	) STRICT`

const artifactVersionTombstonesV38Statement = `CREATE TABLE artifact_version_tombstones (
		artifact_id TEXT NOT NULL CHECK(length(artifact_id)>0),
		version_id TEXT NOT NULL CHECK(length(version_id)>0),
		project_id TEXT NOT NULL CHECK(length(project_id)>0),
		owner_id TEXT NOT NULL CHECK(length(owner_id)>0),
		deleted_at TEXT NOT NULL CHECK(length(deleted_at)>0),
		PRIMARY KEY(artifact_id,version_id)
	) STRICT`

const artifactVersionTombstonesUpdateGuardV38Statement = `CREATE TRIGGER artifact_version_tombstones_update_guard
		BEFORE UPDATE ON artifact_version_tombstones
		BEGIN SELECT RAISE(ABORT,'artifact version tombstone is immutable'); END`

const artifactVersionTombstonesDeleteGuardV38Statement = `CREATE TRIGGER artifact_version_tombstones_delete_guard
		BEFORE DELETE ON artifact_version_tombstones
		WHEN EXISTS(SELECT 1 FROM projects project WHERE project.id=OLD.project_id)
		BEGIN SELECT RAISE(ABORT,'artifact version tombstone is immutable'); END`

const artifactVersionsTombstoneBeforeArtifactDeleteV38Statement = `CREATE TRIGGER artifact_versions_tombstone_before_artifact_delete
		BEFORE DELETE ON artifacts
		BEGIN
			INSERT OR IGNORE INTO artifact_version_tombstones(
				artifact_id,version_id,project_id,owner_id,deleted_at
			)
			SELECT OLD.id,version.id,OLD.project_id,project.user_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
			FROM artifact_versions version
			JOIN projects project ON project.id=OLD.project_id
			WHERE version.artifact_id=OLD.id;
		END`

const artifactVersionsTombstoneBeforeVersionDeleteV38Statement = `CREATE TRIGGER artifact_versions_tombstone_before_version_delete
		BEFORE DELETE ON artifact_versions
		BEGIN
			INSERT OR IGNORE INTO artifact_version_tombstones(
				artifact_id,version_id,project_id,owner_id,deleted_at
			)
			SELECT OLD.artifact_id,OLD.id,artifact.project_id,project.user_id,strftime('%Y-%m-%dT%H:%M:%fZ','now')
			FROM artifacts artifact
			JOIN projects project ON project.id=artifact.project_id
			WHERE artifact.id=OLD.artifact_id;
		END`

const artifactVersionsRejectTombstoneReuseV38Statement = `CREATE TRIGGER artifact_versions_reject_tombstone_reuse
		BEFORE INSERT ON artifact_versions
		WHEN EXISTS(
			SELECT 1 FROM artifact_version_tombstones tombstone
			WHERE tombstone.artifact_id=NEW.artifact_id AND tombstone.version_id=NEW.id
		)
		BEGIN SELECT RAISE(ABORT,'artifact version identity was retired'); END`

const artifactVersionTombstonesProjectCleanupV38Statement = `CREATE TRIGGER artifact_version_tombstones_project_cleanup
		AFTER DELETE ON projects
		BEGIN DELETE FROM artifact_version_tombstones WHERE project_id=OLD.id; END`

var webReadModelV38CanonicalObjects = []webReadModelV38SchemaObject{
	{"transcript_branch_heads", transcriptBranchHeadsV38Statement},
	{"transcript_artifact_reference_heads", transcriptArtifactReferenceHeadsV38Statement},
	{"transcript_web_projection_dirty", transcriptWebProjectionDirtyV38Statement},
	{"transcript_branch_heads_insert", transcriptBranchHeadsInsertV38Statement},
	{"transcript_branch_events_head_validate", transcriptBranchEventsHeadValidateV38Statement},
	{"transcript_branch_events_head_advance", transcriptBranchEventsHeadAdvanceV38Statement},
	{"transcript_branch_events_immutable", transcriptBranchEventsImmutableV38Statement},
	{"transcript_branch_events_delete_guard", transcriptBranchEventsDeleteGuardV38Statement},
	{"transcript_branch_heads_update_guard", transcriptBranchHeadsUpdateGuardV38Statement},
	{"transcript_branch_heads_delete_guard", transcriptBranchHeadsDeleteGuardV38Statement},
	{"transcript_artifact_reference_heads_insert", transcriptArtifactReferenceHeadsInsertV38Statement},
	{"transcript_artifact_references_head_required", transcriptArtifactReferencesHeadRequiredV38Statement},
	{"transcript_artifact_references_head_advance", transcriptArtifactReferencesHeadAdvanceV38Statement},
	{"transcript_artifact_references_structural_immutable", transcriptArtifactReferencesStructuralImmutableV38Statement},
	{"transcript_artifact_references_projection_retire", transcriptArtifactReferencesProjectionRetireV38Statement},
	{"transcript_artifact_references_head_retire", transcriptArtifactReferencesHeadRetireV38Statement},
	{"transcript_artifact_reference_heads_update_guard", transcriptArtifactReferenceHeadsUpdateGuardV38Statement},
	{"transcript_artifact_reference_heads_delete_guard", transcriptArtifactReferenceHeadsDeleteGuardV38Statement},
	{"transcript_events_dirty_branches", transcriptEventsDirtyBranchesV38Statement},
	{"transcript_web_projection_state", transcriptWebProjectionStateV38Statement},
	{"transcript_web_messages", transcriptWebMessagesV38Statement},
	{"transcript_web_message_identities", transcriptWebMessageIdentitiesV38Statement},
	{"transcript_web_message_identities_ordinal", `CREATE INDEX transcript_web_message_identities_ordinal ON transcript_web_message_identities(stream_uid,branch_id,message_ordinal,identity)`},
	{"transcript_artifact_refs_exact_source", `CREATE UNIQUE INDEX transcript_artifact_refs_exact_source ON transcript_artifact_refs(stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation)`},
	{"transcript_web_message_artifact_refs", transcriptWebMessageArtifactRefsV38Statement},
	{"transcript_web_message_artifact_refs_pair", `CREATE INDEX transcript_web_message_artifact_refs_pair ON transcript_web_message_artifact_refs(artifact_id,version_id,stream_uid,branch_id,message_ordinal,ordinal)`},
	{"artifact_version_tombstones", artifactVersionTombstonesV38Statement},
	{"artifact_version_tombstones_owner_project", `CREATE INDEX artifact_version_tombstones_owner_project ON artifact_version_tombstones(owner_id,project_id,artifact_id,version_id)`},
	{"artifact_version_tombstones_update_guard", artifactVersionTombstonesUpdateGuardV38Statement},
	{"artifact_version_tombstones_delete_guard", artifactVersionTombstonesDeleteGuardV38Statement},
	{"artifact_versions_tombstone_before_artifact_delete", artifactVersionsTombstoneBeforeArtifactDeleteV38Statement},
	{"artifact_versions_tombstone_before_version_delete", artifactVersionsTombstoneBeforeVersionDeleteV38Statement},
	{"artifact_versions_reject_tombstone_reuse", artifactVersionsRejectTombstoneReuseV38Statement},
	{"artifact_version_tombstones_project_cleanup", artifactVersionTombstonesProjectCleanupV38Statement},
	{"transcript_web_messages_visible_index", `CREATE UNIQUE INDEX transcript_web_messages_visible_index ON transcript_web_messages(stream_uid,branch_id,visible_index) WHERE visible=1`},
	{"transcript_web_messages_page", `CREATE INDEX transcript_web_messages_page ON transcript_web_messages(stream_uid,branch_id,visible,visible_index,ordinal)`},
	{"transcript_web_messages_publication", `CREATE INDEX transcript_web_messages_publication ON transcript_web_messages(stream_uid,branch_id,last_publication_seq,ordinal)`},
	{"transcript_web_projection_ready", `CREATE INDEX transcript_web_projection_ready ON transcript_web_projection_state(status,updated_at,stream_uid,branch_id)`},
}

var webReadModelV38Statements = append([]string{
	transcriptBranchHeadsV38Statement,
	`INSERT INTO transcript_branch_heads(
			stream_uid,branch_id,through_ordinal,tail_event_id,through_publication_seq,source_revision
		)
		SELECT branch.stream_uid,branch.branch_id,COUNT(membership.ordinal),
			(SELECT tail.event_id FROM transcript_branch_events tail
				WHERE tail.stream_uid=branch.stream_uid AND tail.branch_id=branch.branch_id
				ORDER BY tail.ordinal DESC LIMIT 1),
			COALESCE((SELECT tail_event.publication_seq
				FROM transcript_branch_events tail
				JOIN transcript_events tail_event
					ON tail_event.stream_uid=tail.stream_uid AND tail_event.event_id=tail.event_id
				WHERE tail.stream_uid=branch.stream_uid AND tail.branch_id=branch.branch_id
				ORDER BY tail.ordinal DESC LIMIT 1),0),0
		FROM transcript_branches branch
		LEFT JOIN transcript_branch_events membership
			ON membership.stream_uid=branch.stream_uid AND membership.branch_id=branch.branch_id
		GROUP BY branch.stream_uid,branch.branch_id`,
	transcriptArtifactReferenceHeadsV38Statement,
	`INSERT INTO transcript_artifact_reference_heads(stream_uid,revision)
		SELECT stream.stream_uid,COUNT(ref.source_event_id)
		FROM transcript_streams stream
		LEFT JOIN transcript_artifact_refs ref ON ref.stream_uid=stream.stream_uid
		GROUP BY stream.stream_uid`,
}, func() []string {
	statements := make([]string, 0, len(webReadModelV38CanonicalObjects)-2)
	for _, object := range webReadModelV38CanonicalObjects[2:] {
		statements = append(statements, object.statement)
	}
	return statements
}()...)

func WebReadModelV38Statements() []string {
	return append([]string(nil), webReadModelV38Statements...)
}

func WebReadModelV38ObjectNames() []string {
	names := make([]string, len(webReadModelV38CanonicalObjects))
	for index, object := range webReadModelV38CanonicalObjects {
		names[index] = object.name
	}
	return names
}

func WebReadModelV38CanonicalStatements() []string {
	statements := make([]string, len(webReadModelV38CanonicalObjects))
	for index, object := range webReadModelV38CanonicalObjects {
		statements[index] = object.statement
	}
	return statements
}

func WebReadModelV47ProjectionStateStatement() string {
	return transcriptWebProjectionStateV47Statement
}

func WebReadModelV48ProjectionStateStatement() string {
	return transcriptWebProjectionStateV48Statement
}

func WebReadModelV49ProjectionStateStatement() string {
	return transcriptWebProjectionStateV49Statement
}

func WebReadModelV58ProjectionStateStatement() string {
	return transcriptWebProjectionStateV58Statement
}

func WebReadModelV59ProjectionStateStatement() string {
	return transcriptWebProjectionStateV59Statement
}

func WebReadModelV60ProjectionStateStatement() string {
	return transcriptWebProjectionStateV60Statement
}

func WebReadModelV64ProjectionStateStatement() string {
	return transcriptWebProjectionStateV64Statement
}

func WebReadModelV65ProjectionStateStatement() string {
	return transcriptWebProjectionStateV65Statement
}

func WebReadModelV66ProjectionStateStatement() string {
	return transcriptWebProjectionStateV66Statement
}

func WebReadModelV67ProjectionStateStatement() string {
	return transcriptWebProjectionStateV67Statement
}

// WebReadModelProjectorV47RebuildStatements replaces the projection parent
// and its dependent materialized-message tables as one migration transaction.
// SQLite rewrites foreign-key targets when a parent table is renamed, so all
// dependents must be rebuilt before the v38 parent can be dropped.
func WebReadModelProjectorV47RebuildStatements() []string {
	return []string{
		`ALTER TABLE transcript_web_projection_state RENAME TO transcript_web_projection_state_v38`,
		transcriptWebProjectionStateV47Statement,
		`INSERT INTO transcript_web_projection_state(
			stream_uid,branch_id,branch_generation,projector_version,projection_revision,
			through_publication_seq,source_revision,message_count,visible_message_count,
			message_artifact_reference_count,projector_state_json,projector_state_sha256,
			source_chain_sha256,status,last_error_code,updated_at
		) SELECT stream_uid,branch_id,branch_generation,projector_version,projection_revision,
			through_publication_seq,source_revision,message_count,visible_message_count,
			message_artifact_reference_count,projector_state_json,projector_state_sha256,
			source_chain_sha256,status,last_error_code,updated_at
		FROM transcript_web_projection_state_v38`,
		`DROP TRIGGER transcript_artifact_references_projection_retire`,
		`ALTER TABLE transcript_web_message_identities RENAME TO transcript_web_message_identities_v38`,
		`ALTER TABLE transcript_web_message_artifact_refs RENAME TO transcript_web_message_artifact_refs_v38`,
		`ALTER TABLE transcript_web_messages RENAME TO transcript_web_messages_v38`,
		transcriptWebMessagesV38Statement,
		`INSERT INTO transcript_web_messages(
			stream_uid,branch_id,ordinal,message_id,client_message_id,visible,visible_index,
			message_json,message_sha256,first_publication_seq,last_publication_seq,updated_at
		) SELECT stream_uid,branch_id,ordinal,message_id,client_message_id,visible,visible_index,
			message_json,message_sha256,first_publication_seq,last_publication_seq,updated_at
		FROM transcript_web_messages_v38`,
		transcriptWebMessageIdentitiesV38Statement,
		`INSERT INTO transcript_web_message_identities(stream_uid,branch_id,identity,message_ordinal,kind)
		SELECT stream_uid,branch_id,identity,message_ordinal,kind FROM transcript_web_message_identities_v38`,
		transcriptWebMessageArtifactRefsV38Statement,
		`INSERT INTO transcript_web_message_artifact_refs(
			stream_uid,branch_id,message_ordinal,ordinal,source_event_id,runner_attempt,
			source_reference_ordinal,artifact_id,version_id,relation,filename,content_type,size_bytes,checksum
		) SELECT stream_uid,branch_id,message_ordinal,ordinal,source_event_id,runner_attempt,
			source_reference_ordinal,artifact_id,version_id,relation,filename,content_type,size_bytes,checksum
		FROM transcript_web_message_artifact_refs_v38`,
		`DROP TABLE transcript_web_message_identities_v38`,
		`DROP TABLE transcript_web_message_artifact_refs_v38`,
		`DROP TABLE transcript_web_messages_v38`,
		`DROP TABLE transcript_web_projection_state_v38`,
		`CREATE INDEX transcript_web_message_identities_ordinal ON transcript_web_message_identities(stream_uid,branch_id,message_ordinal,identity)`,
		`CREATE INDEX transcript_web_message_artifact_refs_pair ON transcript_web_message_artifact_refs(artifact_id,version_id,stream_uid,branch_id,message_ordinal,ordinal)`,
		`CREATE UNIQUE INDEX transcript_web_messages_visible_index ON transcript_web_messages(stream_uid,branch_id,visible_index) WHERE visible=1`,
		`CREATE INDEX transcript_web_messages_page ON transcript_web_messages(stream_uid,branch_id,visible,visible_index,ordinal)`,
		`CREATE INDEX transcript_web_messages_publication ON transcript_web_messages(stream_uid,branch_id,last_publication_seq,ordinal)`,
		`CREATE INDEX transcript_web_projection_ready ON transcript_web_projection_state(status,updated_at,stream_uid,branch_id)`,
		transcriptArtifactReferencesProjectionRetireV38Statement,
	}
}

// WebReadModelProjectorV48RebuildStatements widens the immutable projector
// version constraint while preserving all existing derived rows. The v3
// projector version makes the scheduler rebuild those rows from raw Transcript
// events using the corrected AskUser boundary semantics.
func WebReadModelProjectorV48RebuildStatements() []string {
	return []string{
		`ALTER TABLE transcript_web_projection_state RENAME TO transcript_web_projection_state_v47`,
		transcriptWebProjectionStateV48Statement,
		`INSERT INTO transcript_web_projection_state(
			stream_uid,branch_id,branch_generation,projector_version,projection_revision,
			through_publication_seq,source_revision,message_count,visible_message_count,
			message_artifact_reference_count,projector_state_json,projector_state_sha256,
			source_chain_sha256,status,last_error_code,updated_at
		) SELECT stream_uid,branch_id,branch_generation,projector_version,projection_revision,
			through_publication_seq,source_revision,message_count,visible_message_count,
			message_artifact_reference_count,projector_state_json,projector_state_sha256,
			source_chain_sha256,status,last_error_code,updated_at
		FROM transcript_web_projection_state_v47`,
		`DROP TRIGGER transcript_artifact_references_projection_retire`,
		`ALTER TABLE transcript_web_message_identities RENAME TO transcript_web_message_identities_v47`,
		`ALTER TABLE transcript_web_message_artifact_refs RENAME TO transcript_web_message_artifact_refs_v47`,
		`ALTER TABLE transcript_web_messages RENAME TO transcript_web_messages_v47`,
		transcriptWebMessagesV38Statement,
		`INSERT INTO transcript_web_messages(
			stream_uid,branch_id,ordinal,message_id,client_message_id,visible,visible_index,
			message_json,message_sha256,first_publication_seq,last_publication_seq,updated_at
		) SELECT stream_uid,branch_id,ordinal,message_id,client_message_id,visible,visible_index,
			message_json,message_sha256,first_publication_seq,last_publication_seq,updated_at
		FROM transcript_web_messages_v47`,
		transcriptWebMessageIdentitiesV38Statement,
		`INSERT INTO transcript_web_message_identities(stream_uid,branch_id,identity,message_ordinal,kind)
		SELECT stream_uid,branch_id,identity,message_ordinal,kind FROM transcript_web_message_identities_v47`,
		transcriptWebMessageArtifactRefsV38Statement,
		`INSERT INTO transcript_web_message_artifact_refs(
			stream_uid,branch_id,message_ordinal,ordinal,source_event_id,runner_attempt,
			source_reference_ordinal,artifact_id,version_id,relation,filename,content_type,size_bytes,checksum
		) SELECT stream_uid,branch_id,message_ordinal,ordinal,source_event_id,runner_attempt,
			source_reference_ordinal,artifact_id,version_id,relation,filename,content_type,size_bytes,checksum
		FROM transcript_web_message_artifact_refs_v47`,
		`DROP TABLE transcript_web_message_identities_v47`,
		`DROP TABLE transcript_web_message_artifact_refs_v47`,
		`DROP TABLE transcript_web_messages_v47`,
		`DROP TABLE transcript_web_projection_state_v47`,
		`CREATE INDEX transcript_web_message_identities_ordinal ON transcript_web_message_identities(stream_uid,branch_id,message_ordinal,identity)`,
		`CREATE INDEX transcript_web_message_artifact_refs_pair ON transcript_web_message_artifact_refs(artifact_id,version_id,stream_uid,branch_id,message_ordinal,ordinal)`,
		`CREATE UNIQUE INDEX transcript_web_messages_visible_index ON transcript_web_messages(stream_uid,branch_id,visible_index) WHERE visible=1`,
		`CREATE INDEX transcript_web_messages_page ON transcript_web_messages(stream_uid,branch_id,visible,visible_index,ordinal)`,
		`CREATE INDEX transcript_web_messages_publication ON transcript_web_messages(stream_uid,branch_id,last_publication_seq,ordinal)`,
		`CREATE INDEX transcript_web_projection_ready ON transcript_web_projection_state(status,updated_at,stream_uid,branch_id)`,
		transcriptArtifactReferencesProjectionRetireV38Statement,
	}
}

func WebReadModelProjectorV49RebuildStatements() []string {
	statements := WebReadModelProjectorV48RebuildStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV48Statement {
			statements[index] = transcriptWebProjectionStateV49Statement
			continue
		}
		statements[index] = strings.ReplaceAll(statement, "_v47", "_v48")
	}
	return statements
}

// WebReadModelProjectorV58RebuildStatements widens the immutable projector
// constraint for v5 while preserving derived rows. The version fence causes
// those rows to be rebuilt from the authoritative Transcript event stream.
func WebReadModelProjectorV58RebuildStatements() []string {
	statements := WebReadModelProjectorV49RebuildStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV49Statement {
			statements[index] = transcriptWebProjectionStateV58Statement
			continue
		}
		statements[index] = strings.ReplaceAll(statement, "_v48", "_v57")
	}
	return statements
}

// WebReadModelProjectorV59RebuildStatements widens the immutable projector
// constraint for v6 while preserving derived rows. The version fence causes
// every v5 row to be rebuilt from the authoritative Transcript event stream.
func WebReadModelProjectorV59RebuildStatements() []string {
	statements := WebReadModelProjectorV58RebuildStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV58Statement {
			statements[index] = transcriptWebProjectionStateV59Statement
			continue
		}
		statements[index] = strings.ReplaceAll(statement, "_v57", "_v58")
	}
	return statements
}

// WebReadModelProjectorV60RebuildStatements widens the immutable projector
// constraint for v7 while preserving derived rows. The version fence causes
// every older row, including quarantined v6 rows, to be rebuilt from source.
func WebReadModelProjectorV60RebuildStatements() []string {
	statements := WebReadModelProjectorV59RebuildStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV59Statement {
			statements[index] = transcriptWebProjectionStateV60Statement
			continue
		}
		statements[index] = strings.ReplaceAll(statement, "_v58", "_v59")
	}
	return statements
}

// WebReadModelProjectorV64RebuildStatements widens the immutable projector
// constraint for v8 while preserving derived rows. The version fence causes
// every older row, including quarantined v7 rows, to be rebuilt from source.
func WebReadModelProjectorV64RebuildStatements() []string {
	statements := WebReadModelProjectorV60RebuildStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV60Statement {
			statements[index] = transcriptWebProjectionStateV64Statement
			continue
		}
		statements[index] = strings.ReplaceAll(statement, "_v59", "_v63")
	}
	return statements
}

// WebReadModelProjectorV65RebuildStatements widens the immutable projector
// constraint for v9 while preserving derived rows. The version fence rebuilds
// every older row, including quarantined v8 rows, from the source Transcript.
func WebReadModelProjectorV65RebuildStatements() []string {
	statements := WebReadModelProjectorV64RebuildStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV64Statement {
			statements[index] = transcriptWebProjectionStateV65Statement
			continue
		}
		statements[index] = strings.ReplaceAll(statement, "_v63", "_v64")
	}
	return statements
}

// WebReadModelProjectorV66RebuildStatements widens the immutable projector
// constraint for v10 while preserving derived rows. The version fence rebuilds
// every older row, including quarantined v9 rows, from the source Transcript.
func WebReadModelProjectorV66RebuildStatements() []string {
	statements := WebReadModelProjectorV65RebuildStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV65Statement {
			statements[index] = transcriptWebProjectionStateV66Statement
			continue
		}
		statements[index] = strings.ReplaceAll(statement, "_v64", "_v65")
	}
	return statements
}

// WebReadModelProjectorV67RebuildStatements widens the immutable projector
// constraint for v11 while preserving derived rows. The version fence rebuilds
// every older row, including quarantined v10 rows, from the source Transcript.
func WebReadModelProjectorV67RebuildStatements() []string {
	statements := WebReadModelProjectorV66RebuildStatements()
	for index, statement := range statements {
		if statement == transcriptWebProjectionStateV66Statement {
			statements[index] = transcriptWebProjectionStateV67Statement
			continue
		}
		statements[index] = strings.ReplaceAll(statement, "_v65", "_v66")
	}
	return statements
}
