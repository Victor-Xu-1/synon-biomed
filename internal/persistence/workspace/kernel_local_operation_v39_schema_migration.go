package workspace

import (
	"context"
	"errors"
)

const (
	kernelLocalOperationV39CallbackID        = "kernel-local-operation-v39-noop"
	kernelLocalOperationV39PreflightIdentity = "transcript-tool-call-operation-authority-v1"
	kernelLocalOperationV39RuleSpec          = "synon.workspace.kernel-local-operation.v39"
	kernelLocalOperationsDDL                 = `CREATE TABLE kernel_local_operations (
		operation_id TEXT PRIMARY KEY,
		owner_user_id TEXT NOT NULL,
		project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
		root_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE RESTRICT,
		root_frame_incarnation_id TEXT NOT NULL,
		frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE RESTRICT,
		frame_incarnation_id TEXT NOT NULL,
		stream_uid TEXT NOT NULL REFERENCES transcript_streams(stream_uid) ON DELETE RESTRICT,
		branch_id TEXT NOT NULL,
		branch_generation INTEGER NOT NULL CHECK(branch_generation>0),
		source_event_id INTEGER NOT NULL,
		source_publication_seq INTEGER NOT NULL CHECK(source_publication_seq>0),
		source_runner_attempt INTEGER NOT NULL CHECK(source_runner_attempt>0),
		source_client_message_id TEXT NOT NULL,
		tool_call_ordinal INTEGER NOT NULL CHECK(tool_call_ordinal>=0),
		tool_call_id TEXT NOT NULL,
		tool TEXT NOT NULL CHECK(tool IN ('python','r','repl')),
		environment TEXT NOT NULL,
		input_json TEXT NOT NULL CHECK(length(CAST(input_json AS BLOB))<=1048576),
		input_sha256 TEXT NOT NULL CHECK(length(input_sha256)=64 AND input_sha256 NOT GLOB '*[^0-9a-f]*'),
		confinement_sha256 TEXT CHECK(confinement_sha256 IS NULL OR
			(length(confinement_sha256)=64 AND confinement_sha256 NOT GLOB '*[^0-9a-f]*')),
		state TEXT NOT NULL CHECK(state IN ('pending_approval','approved','prepared','started','completed','failed','cancelled','outcome_unknown')),
		state_version INTEGER NOT NULL CHECK(state_version>0),
		approval_request_id TEXT,
		approval_decision_id TEXT,
		approval_decision TEXT CHECK(approval_decision IS NULL OR approval_decision IN ('allow','deny')),
		approval_scope TEXT CHECK(approval_scope IS NULL OR approval_scope IN ('once','conversation','project','always')),
		approval_source TEXT CHECK(approval_source IS NULL OR approval_source IN ('user','policy','remembered','stale_reconcile')),
		approval_actor_id TEXT,
		decided_at TEXT,
		admitted_input_revision INTEGER CHECK(admitted_input_revision IS NULL OR admitted_input_revision>0),
		runner_id TEXT,
		runner_attempt INTEGER CHECK(runner_attempt IS NULL OR runner_attempt>0),
		runner_claim_sha256 TEXT CHECK(runner_claim_sha256 IS NULL OR
			(length(runner_claim_sha256)=64 AND runner_claim_sha256 NOT GLOB '*[^0-9a-f]*')),
		boot_id TEXT,
		kernel_id TEXT,
		kernel_generation INTEGER CHECK(kernel_generation IS NULL OR kernel_generation>0),
		execution_id TEXT,
		execution_log_id TEXT REFERENCES execution_log(id) ON DELETE RESTRICT,
		result_json TEXT CHECK(result_json IS NULL OR length(CAST(result_json AS BLOB))<=1048576),
		result_ref TEXT,
		result_sha256 TEXT CHECK(result_sha256 IS NULL OR
			(length(result_sha256)=64 AND result_sha256 NOT GLOB '*[^0-9a-f]*')),
		reason_code TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		approved_at TEXT,
		prepared_at TEXT,
		started_at TEXT,
		terminal_at TEXT,
		updated_at TEXT NOT NULL,
		UNIQUE(stream_uid,source_event_id,tool_call_ordinal),
		FOREIGN KEY(stream_uid,source_runner_attempt,source_event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE RESTRICT,
		CHECK((runner_id IS NULL AND runner_attempt IS NULL AND runner_claim_sha256 IS NULL) OR
			(runner_id IS NOT NULL AND runner_attempt IS NOT NULL AND runner_claim_sha256 IS NOT NULL)),
		CHECK((approval_decision_id IS NULL AND approval_decision IS NULL AND approval_scope IS NULL AND
			approval_source IS NULL AND approval_actor_id IS NULL AND decided_at IS NULL) OR
			(approval_decision_id IS NOT NULL AND approval_decision IS NOT NULL AND approval_scope IS NOT NULL AND
				approval_source IS NOT NULL AND approval_actor_id IS NOT NULL AND decided_at IS NOT NULL)),
		CHECK((state='pending_approval' AND approval_decision_id IS NULL) OR
			(state!='pending_approval' AND approval_decision_id IS NOT NULL)),
		CHECK(state='pending_approval' OR approval_decision='allow' OR
			(approval_decision='deny' AND state IN ('failed','cancelled'))),
		CHECK(approved_at IS NULL OR approval_decision='allow'),
		CHECK((kernel_id IS NULL AND kernel_generation IS NULL) OR
			(kernel_id IS NOT NULL AND kernel_generation IS NOT NULL)),
		CHECK((state IN ('prepared','started','completed','outcome_unknown') AND confinement_sha256 IS NOT NULL) OR
			(state IN ('pending_approval','approved','failed','cancelled'))),
		CHECK(state!='prepared' OR (runner_id IS NOT NULL AND boot_id IS NOT NULL AND kernel_id IS NOT NULL AND
			confinement_sha256 IS NOT NULL AND execution_id IS NULL AND started_at IS NULL)),
		CHECK(execution_id IS NULL OR (runner_id IS NOT NULL AND boot_id IS NOT NULL AND kernel_id IS NOT NULL AND
			confinement_sha256 IS NOT NULL)),
		CHECK((state IN ('started','completed','outcome_unknown') AND
			execution_id IS NOT NULL AND started_at IS NOT NULL) OR
			(state IN ('pending_approval','approved','prepared','failed','cancelled'))),
		CHECK((execution_id IS NULL AND started_at IS NULL) OR
			(execution_id IS NOT NULL AND started_at IS NOT NULL)),
		CHECK((state IN ('completed','failed','cancelled','outcome_unknown') AND terminal_at IS NOT NULL) OR
			(state IN ('pending_approval','approved','prepared','started') AND terminal_at IS NULL)),
		CHECK((result_json IS NULL AND result_ref IS NULL AND result_sha256 IS NULL) OR
			(result_sha256 IS NOT NULL AND ((result_json IS NULL)!=(result_ref IS NULL)) AND
				state IN ('completed','failed','cancelled','outcome_unknown'))),
		CHECK(state!='completed' OR (execution_log_id IS NOT NULL AND result_sha256 IS NOT NULL)),
		CHECK(execution_log_id IS NULL OR state IN ('completed','failed','cancelled')),
		CHECK(execution_id IS NULL OR state IN ('started','outcome_unknown') OR execution_log_id IS NOT NULL)
	) STRICT`
	kernelLocalOperationProtocolReceiptsDDL = `CREATE TABLE kernel_local_operation_protocol_receipts (
		operation_id TEXT PRIMARY KEY REFERENCES kernel_local_operations(operation_id) ON DELETE RESTRICT,
		stream_uid TEXT NOT NULL,
		runner_attempt INTEGER NOT NULL CHECK(runner_attempt>0),
		event_id INTEGER NOT NULL CHECK(event_id>0),
		tool_call_id TEXT NOT NULL,
		result_sha256 TEXT NOT NULL CHECK(length(result_sha256)=64 AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
		created_at TEXT NOT NULL,
		UNIQUE(stream_uid,event_id),
		FOREIGN KEY(stream_uid,runner_attempt,event_id)
			REFERENCES transcript_events(stream_uid,runner_attempt,event_id) ON DELETE RESTRICT
	) STRICT`
	kernelLocalOperationTransitionsDDL = `CREATE TABLE kernel_local_operation_transitions (
		operation_id TEXT NOT NULL REFERENCES kernel_local_operations(operation_id) ON DELETE RESTRICT,
		transition_seq INTEGER NOT NULL CHECK(transition_seq>0),
		from_state TEXT NOT NULL CHECK(from_state IN ('','pending_approval','approved','prepared','started','completed','failed','cancelled','outcome_unknown')),
		to_state TEXT NOT NULL CHECK(to_state IN ('pending_approval','approved','prepared','started','completed','failed','cancelled','outcome_unknown')),
		state_version INTEGER NOT NULL CHECK(state_version>0),
		runner_attempt INTEGER CHECK(runner_attempt IS NULL OR runner_attempt>0),
		runner_claim_sha256 TEXT CHECK(runner_claim_sha256 IS NULL OR
			(length(runner_claim_sha256)=64 AND runner_claim_sha256 NOT GLOB '*[^0-9a-f]*')),
		boot_id TEXT,
		approval_decision_id TEXT,
		approval_decision TEXT CHECK(approval_decision IS NULL OR approval_decision IN ('allow','deny')),
		approval_scope TEXT CHECK(approval_scope IS NULL OR approval_scope IN ('once','conversation','project','always')),
		approval_source TEXT CHECK(approval_source IS NULL OR approval_source IN ('user','policy','remembered','stale_reconcile')),
		approval_actor_id TEXT,
		admitted_input_revision INTEGER CHECK(admitted_input_revision IS NULL OR admitted_input_revision>0),
		reason_code TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		PRIMARY KEY(operation_id,transition_seq),
		UNIQUE(operation_id,state_version)
	) STRICT`
)

var kernelLocalOperationV39Migration = versionedSchemaMigration{
	version: 39,
	name:    "kernel-local-operation-authority",
	statements: []string{
		kernelLocalOperationsDDL,
		kernelLocalOperationProtocolReceiptsDDL,
		kernelLocalOperationTransitionsDDL,
		`CREATE INDEX kernel_local_operations_runnable_idx ON kernel_local_operations(
			owner_user_id,stream_uid,branch_id,branch_generation,admitted_input_revision,state,
			source_publication_seq,tool_call_ordinal,operation_id
		) WHERE admitted_input_revision IS NOT NULL`,
		`CREATE INDEX kernel_local_operations_frame_idx ON kernel_local_operations(owner_user_id,project_id,frame_id,state,updated_at,operation_id)`,
		`CREATE UNIQUE INDEX kernel_local_operations_approval_request_unique
			ON kernel_local_operations(approval_request_id) WHERE approval_request_id IS NOT NULL`,
		`CREATE TRIGGER kernel_local_operations_identity_immutable BEFORE UPDATE ON kernel_local_operations
		WHEN NEW.operation_id!=OLD.operation_id OR NEW.owner_user_id!=OLD.owner_user_id OR
			NEW.project_id!=OLD.project_id OR NEW.root_frame_id!=OLD.root_frame_id OR
			NEW.root_frame_incarnation_id!=OLD.root_frame_incarnation_id OR NEW.frame_id!=OLD.frame_id OR
			NEW.frame_incarnation_id!=OLD.frame_incarnation_id OR NEW.stream_uid!=OLD.stream_uid OR
			NEW.branch_id!=OLD.branch_id OR NEW.branch_generation!=OLD.branch_generation OR
			NEW.source_event_id!=OLD.source_event_id OR NEW.source_publication_seq!=OLD.source_publication_seq OR
			NEW.source_runner_attempt!=OLD.source_runner_attempt OR NEW.source_client_message_id!=OLD.source_client_message_id OR
			NEW.tool_call_ordinal!=OLD.tool_call_ordinal OR NEW.tool_call_id!=OLD.tool_call_id OR
			NEW.tool!=OLD.tool OR NEW.environment!=OLD.environment OR NEW.input_json!=OLD.input_json OR
			NEW.input_sha256!=OLD.input_sha256 OR NEW.approval_request_id IS NOT OLD.approval_request_id OR
			(OLD.approval_decision_id IS NOT NULL AND (NEW.approval_decision_id IS NOT OLD.approval_decision_id OR
				NEW.approval_decision IS NOT OLD.approval_decision OR NEW.approval_scope IS NOT OLD.approval_scope OR
				NEW.approval_source IS NOT OLD.approval_source OR NEW.approval_actor_id IS NOT OLD.approval_actor_id OR
				NEW.decided_at IS NOT OLD.decided_at)) OR
			(OLD.confinement_sha256 IS NOT NULL AND NEW.confinement_sha256 IS NOT OLD.confinement_sha256) OR
			(OLD.admitted_input_revision IS NOT NULL AND NEW.admitted_input_revision IS NOT OLD.admitted_input_revision AND NOT (
				((OLD.state='prepared' AND NEW.state='approved') OR
					(OLD.state='started' AND NEW.state='outcome_unknown')) AND
				NEW.admitted_input_revision>OLD.admitted_input_revision
			)) OR
			NEW.created_at!=OLD.created_at
		BEGIN SELECT RAISE(ABORT,'kernel local operation identity is immutable'); END`,
		`CREATE TRIGGER kernel_local_operations_state_transition_valid BEFORE UPDATE ON kernel_local_operations
		WHEN NEW.state_version!=OLD.state_version+1 OR NOT (
			(OLD.state='pending_approval' AND NEW.state IN ('approved','failed','cancelled')) OR
			(OLD.state='approved' AND NEW.state IN ('prepared','cancelled')) OR
			(OLD.state='prepared' AND NEW.state IN ('approved','started','cancelled')) OR
			(OLD.state='started' AND NEW.state IN ('completed','failed','cancelled','outcome_unknown'))
		)
		BEGIN SELECT RAISE(ABORT,'kernel local operation state transition is invalid'); END`,
		`CREATE TRIGGER kernel_local_operations_confinement_assignment BEFORE UPDATE ON kernel_local_operations
		WHEN NEW.confinement_sha256 IS NOT OLD.confinement_sha256 AND NOT (
			OLD.state='approved' AND NEW.state='prepared' AND OLD.confinement_sha256 IS NULL AND NEW.confinement_sha256 IS NOT NULL
		)
		BEGIN SELECT RAISE(ABORT,'kernel local operation confinement assignment is invalid'); END`,
		`CREATE TRIGGER kernel_local_operations_admission_assignment BEFORE UPDATE ON kernel_local_operations
		WHEN NEW.admitted_input_revision IS NOT OLD.admitted_input_revision AND NOT (
			(OLD.state='pending_approval' AND NEW.state IN ('approved','failed') AND
				OLD.admitted_input_revision IS NULL AND NEW.admitted_input_revision IS NOT NULL) OR
			(OLD.state='prepared' AND NEW.state='approved' AND
				NEW.admitted_input_revision>COALESCE(OLD.admitted_input_revision,0)) OR
			(OLD.state='started' AND NEW.state='outcome_unknown' AND
				NEW.admitted_input_revision>COALESCE(OLD.admitted_input_revision,0))
		) OR (NEW.admitted_input_revision IS NOT NULL AND NOT EXISTS (
			SELECT 1 FROM transcript_streams stream WHERE stream.stream_uid=NEW.stream_uid
				AND stream.input_revision>=NEW.admitted_input_revision
		))
		BEGIN SELECT RAISE(ABORT,'kernel local operation admission assignment is invalid'); END`,
		`CREATE TRIGGER kernel_local_operation_transition_matches_head BEFORE INSERT ON kernel_local_operation_transitions
		WHEN NEW.transition_seq!=NEW.state_version OR NOT EXISTS (
			SELECT 1 FROM kernel_local_operations operation
			WHERE operation.operation_id=NEW.operation_id AND operation.state=NEW.to_state
				AND operation.state_version=NEW.state_version
		)
		BEGIN SELECT RAISE(ABORT,'kernel local operation transition does not match head'); END`,
		`CREATE TRIGGER kernel_local_operation_transition_initial AFTER INSERT ON kernel_local_operations
		BEGIN
			INSERT INTO kernel_local_operation_transitions(
				operation_id,transition_seq,from_state,to_state,state_version,runner_attempt,
				runner_claim_sha256,boot_id,approval_decision_id,approval_decision,approval_scope,
				approval_source,approval_actor_id,admitted_input_revision,reason_code,created_at
			) VALUES(NEW.operation_id,NEW.state_version,'',NEW.state,NEW.state_version,
				NEW.runner_attempt,NEW.runner_claim_sha256,NEW.boot_id,NEW.approval_decision_id,
				NEW.approval_decision,NEW.approval_scope,NEW.approval_source,NEW.approval_actor_id,
				NEW.admitted_input_revision,
				NEW.reason_code,NEW.created_at);
		END`,
		`CREATE TRIGGER kernel_local_operation_transition_append AFTER UPDATE ON kernel_local_operations
		BEGIN
			INSERT INTO kernel_local_operation_transitions(
				operation_id,transition_seq,from_state,to_state,state_version,runner_attempt,
				runner_claim_sha256,boot_id,approval_decision_id,approval_decision,approval_scope,
				approval_source,approval_actor_id,admitted_input_revision,reason_code,created_at
			) VALUES(NEW.operation_id,NEW.state_version,OLD.state,NEW.state,NEW.state_version,
				COALESCE(NEW.runner_attempt,OLD.runner_attempt),
				COALESCE(NEW.runner_claim_sha256,OLD.runner_claim_sha256),
				COALESCE(NEW.boot_id,OLD.boot_id),NEW.approval_decision_id,NEW.approval_decision,
				NEW.approval_scope,NEW.approval_source,NEW.approval_actor_id,NEW.admitted_input_revision,
				NEW.reason_code,NEW.updated_at);
		END`,
		`CREATE TRIGGER kernel_local_operations_delete_forbidden BEFORE DELETE ON kernel_local_operations
		BEGIN SELECT RAISE(ABORT,'kernel local operation head is append-only'); END`,
		`CREATE TRIGGER kernel_local_operation_transitions_immutable BEFORE UPDATE ON kernel_local_operation_transitions
		BEGIN SELECT RAISE(ABORT,'kernel local operation transition is immutable'); END`,
		`CREATE TRIGGER kernel_local_operation_transitions_append_only BEFORE DELETE ON kernel_local_operation_transitions
		BEGIN SELECT RAISE(ABORT,'kernel local operation transition is append-only'); END`,
		`CREATE TRIGGER kernel_local_operation_protocol_receipts_immutable BEFORE UPDATE ON kernel_local_operation_protocol_receipts
		BEGIN SELECT RAISE(ABORT,'kernel local operation protocol receipt is immutable'); END`,
		`CREATE TRIGGER kernel_local_operation_protocol_receipts_append_only BEFORE DELETE ON kernel_local_operation_protocol_receipts
		BEGIN SELECT RAISE(ABORT,'kernel local operation protocol receipt is append-only'); END`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        kernelLocalOperationV39CallbackID,
		RuleSpec:          kernelLocalOperationV39RuleSpec,
		PreflightIdentity: kernelLocalOperationV39PreflightIdentity,
	},
}

func preflightKernelLocalOperationV39(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var dependencies int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN ('projects','frames','execution_log','transcript_streams','transcript_events')`).Scan(&dependencies); err != nil {
		return errors.New("inspect kernel local operation dependencies")
	}
	if dependencies != 5 {
		return errors.New("kernel local operation dependencies are required")
	}
	var polluted int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name IN ('kernel_local_operations','kernel_local_operation_protocol_receipts','kernel_local_operation_transitions')`).Scan(&polluted); err != nil {
		return errors.New("inspect kernel local operation cohort")
	}
	if polluted != 0 {
		return errors.New("kernel local operation cohort is polluted")
	}
	return nil
}
