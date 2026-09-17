package workspace

const legacyMemoryCategoryAssignmentsDDL = `CREATE TABLE IF NOT EXISTS memory_category_assignments (
	memory_id TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
	category_id TEXT REFERENCES memory_categories(id) ON DELETE SET NULL
)`

var legacyTaskIntentDDL = []string{
	`CREATE TABLE IF NOT EXISTS frame_task_intents (
		id TEXT PRIMARY KEY,
		frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
		revision INTEGER NOT NULL,
		source_event_id TEXT NOT NULL UNIQUE REFERENCES frame_events(id) ON DELETE CASCADE,
		source_message_id TEXT NOT NULL,
		origin TEXT NOT NULL,
		language TEXT NOT NULL,
		text TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		UNIQUE (frame_id, revision),
		UNIQUE (frame_id, source_message_id)
	)`,
	`CREATE INDEX IF NOT EXISTS frame_task_intents_frame_revision_idx
		ON frame_task_intents (frame_id, revision DESC)`,
	`CREATE TABLE IF NOT EXISTS frame_active_task_intents (
		frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
		task_intent_id TEXT NOT NULL UNIQUE REFERENCES frame_task_intents(id) ON DELETE CASCADE,
		updated_at TIMESTAMP NOT NULL
	)`,
}

var rescuedWorkspaceSchemaMigrations = []versionedSchemaMigration{
	{
		version: 4,
		name:    "claude-science-memory-schema",
		statements: []string{
			`ALTER TABLE memories DROP COLUMN access_count`,
			`ALTER TABLE memories DROP COLUMN importance`,
		},
	},
	{
		version: 5,
		name:    "claude-science-memory-lookup-indexes",
		statements: []string{
			`CREATE INDEX IF NOT EXISTS mem_src_frame_idx ON memories (source_frame_id)`,
			`CREATE INDEX IF NOT EXISTS mem_superseded_idx ON memories (superseded_by)`,
			`CREATE INDEX IF NOT EXISTS mem_category_idx ON memory_category_assignments (category_id, memory_id)`,
		},
	},
	{
		version: 6,
		name:    "claude-science-frame-memory-lifecycle",
		statements: []string{
			`CREATE TRIGGER IF NOT EXISTS delete_frame_scoped_memories
			BEFORE DELETE ON frames
			FOR EACH ROW
			BEGIN
				DELETE FROM memories WHERE subject_frame_id = OLD.id;
			END`,
		},
	},
	{
		version: 7,
		name:    "claude-science-direct-memory-category",
		statements: []string{
			`ALTER TABLE memories ADD COLUMN category_id TEXT REFERENCES memory_categories(id) ON DELETE SET NULL`,
			`UPDATE memories SET category_id = (
				SELECT category_id FROM memory_category_assignments WHERE memory_id = memories.id
			) WHERE EXISTS (
				SELECT 1 FROM memory_category_assignments WHERE memory_id = memories.id
			)`,
			`DROP INDEX IF EXISTS mem_category_idx`,
			`DROP TABLE memory_category_assignments`,
			`CREATE INDEX mem_category_idx ON memories (category_id)`,
		},
	},
	{
		version: 8,
		name:    "claude-science-canonical-frame-status",
		statements: []string{
			`UPDATE frames SET status = CASE status
				WHEN 'running' THEN 'processing'
				WHEN 'executing' THEN 'processing'
				WHEN 'in_progress' THEN 'processing'
				WHEN 'in-progress' THEN 'processing'
				WHEN 'queued' THEN 'processing'
				WHEN 'pending' THEN 'processing'
				WHEN 'created' THEN 'processing'
				WHEN 'waiting_input' THEN 'awaiting_user_response'
				WHEN 'needs_input' THEN 'awaiting_user_response'
				WHEN 'needs-input' THEN 'awaiting_user_response'
				WHEN 'awaiting_input' THEN 'awaiting_user_response'
				WHEN 'waiting_approval' THEN 'awaiting_plan_approval'
				WHEN 'error' THEN 'failed'
				WHEN 'canceled' THEN 'cancelled'
				WHEN 'stopped' THEN 'cancelled'
				WHEN 'cancelling' THEN 'cancelled'
				WHEN 'canceling' THEN 'cancelled'
				ELSE status END`,
			`CREATE TABLE frame_status_migration_guard (
				status TEXT NOT NULL CHECK (status = 'canonical')
			)`,
			`INSERT INTO frame_status_migration_guard(status)
				SELECT status FROM frames
				WHERE status NOT IN ('processing','awaiting_user_response','awaiting_plan_approval','completed','failed','cancelled','success','replaced')`,
			`DROP TABLE frame_status_migration_guard`,
			`CREATE TRIGGER frames_status_insert_guard
			BEFORE INSERT ON frames
			WHEN NEW.status NOT IN ('processing','awaiting_user_response','awaiting_plan_approval','completed','failed','cancelled','success','replaced')
			BEGIN
				SELECT RAISE(ABORT, 'non-canonical frame status');
			END`,
			`CREATE TRIGGER frames_status_update_guard
			BEFORE UPDATE OF status ON frames
			WHEN NEW.status NOT IN ('processing','awaiting_user_response','awaiting_plan_approval','completed','failed','cancelled','success','replaced')
			BEGIN
				SELECT RAISE(ABORT, 'non-canonical frame status');
			END`,
		},
	},
	{
		version: 9,
		name:    "claude-science-artifact-message-ownership",
		statements: []string{
			`ALTER TABLE artifact_version_provenance ADD COLUMN source_tool_call_id TEXT`,
			`ALTER TABLE artifact_version_provenance ADD COLUMN source_message_event_id TEXT REFERENCES frame_events(id) ON DELETE SET NULL`,
			`CREATE INDEX artifact_version_provenance_source_tool_idx
				ON artifact_version_provenance (frame_id, source_tool_call_id, version_id)`,
			`CREATE INDEX artifact_version_provenance_source_message_idx
				ON artifact_version_provenance (source_message_event_id, version_id)`,
		},
	},
	{
		version: 10,
		name:    "claude-science-durable-artifact-lineage-jobs",
		statements: []string{
			`CREATE TABLE artifact_lineage_jobs (
				id TEXT PRIMARY KEY,
				version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
				artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
				project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
				frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
				owner_user_id TEXT NOT NULL,
				phase TEXT NOT NULL CHECK (phase IN ('dependency_mapping', 'code_extraction')),
				status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
				attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
				available_at_ms INTEGER NOT NULL,
				lease_owner TEXT,
				claim_token TEXT,
				claimed_at_ms INTEGER,
				lease_expires_at_ms INTEGER,
				last_error TEXT,
				created_at_ms INTEGER NOT NULL,
				updated_at_ms INTEGER NOT NULL,
				completed_at_ms INTEGER,
				UNIQUE (version_id, phase)
			)`,
			`CREATE INDEX artifact_lineage_jobs_ready_idx
				ON artifact_lineage_jobs (status, available_at_ms, lease_expires_at_ms, created_at_ms, id)`,
			`CREATE INDEX artifact_lineage_jobs_version_idx
				ON artifact_lineage_jobs (version_id, status, phase)`,
			`INSERT INTO artifact_lineage_jobs
				(id, version_id, artifact_id, project_id, frame_id, owner_user_id, phase,
				 status, attempts, available_at_ms, created_at_ms, updated_at_ms)
			SELECT 'artifact-lineage:' || version.id || ':dependency_mapping', version.id,
				artifact.id, artifact.project_id, provenance.frame_id, project.user_id,
				'dependency_mapping', 'pending',
				CASE WHEN json_valid(provenance.dependency_mappings)
					THEN MAX(COALESCE(json_extract(provenance.dependency_mappings, '$.mapping_attempts'), 0), 0)
					ELSE 0 END,
				CAST(strftime('%s', 'now') AS INTEGER) * 1000,
				CAST(strftime('%s', 'now') AS INTEGER) * 1000,
				CAST(strftime('%s', 'now') AS INTEGER) * 1000
			FROM artifact_versions AS version
			JOIN artifacts AS artifact ON artifact.id = version.artifact_id
			JOIN projects AS project ON project.id = artifact.project_id
			JOIN artifact_version_provenance AS provenance ON provenance.version_id = version.id
			WHERE provenance.frame_id IS NOT NULL
				AND json_valid(provenance.dependency_mappings)
				AND json_extract(provenance.dependency_mappings, '$.mapping_status') = 'pending'`,
			`INSERT INTO artifact_lineage_jobs
				(id, version_id, artifact_id, project_id, frame_id, owner_user_id, phase,
				 status, attempts, available_at_ms, created_at_ms, updated_at_ms)
			SELECT 'artifact-lineage:' || version.id || ':code_extraction', version.id,
				artifact.id, artifact.project_id, provenance.frame_id, project.user_id,
				'code_extraction', 'pending', 0,
				CAST(strftime('%s', 'now') AS INTEGER) * 1000,
				CAST(strftime('%s', 'now') AS INTEGER) * 1000,
				CAST(strftime('%s', 'now') AS INTEGER) * 1000
			FROM artifact_versions AS version
			JOIN artifacts AS artifact ON artifact.id = version.artifact_id
			JOIN projects AS project ON project.id = artifact.project_id
			JOIN artifact_version_provenance AS provenance ON provenance.version_id = version.id
			WHERE provenance.frame_id IS NOT NULL
				AND COALESCE(provenance.language, '') != 'text'
				AND COALESCE(provenance.extracted_code, '') = ''
				AND (
					COALESCE(provenance.lineage_snapshot_hash, '') != ''
					OR COALESCE(provenance.lineage_messages, '') NOT IN ('', 'null', '[]')
					OR COALESCE(provenance.cell_sources, '') NOT IN ('', 'null', '[]')
				)`,
		},
	},
	{
		version: 11,
		name:    "freeze-exhausted-artifact-lineage-mappings",
		statements: []string{
			`UPDATE artifact_version_provenance
			SET dependency_mappings = json_set(
				dependency_mappings,
				'$.mapping_status', 'complete',
				'$.mapping_attempts', 3,
				'$.terminal_sweep', 'frozen'
			)
			WHERE json_valid(dependency_mappings)
				AND json_extract(dependency_mappings, '$.mapping_status') = 'pending'
				AND CAST(COALESCE(json_extract(dependency_mappings, '$.mapping_attempts'), 0) AS INTEGER) >= 3`,
		},
	},
	{
		version: 12,
		name:    "complete-exhausted-artifact-lineage-jobs",
		statements: []string{
			`UPDATE artifact_lineage_jobs
			SET status = 'completed', lease_owner = NULL, claim_token = NULL,
				claimed_at_ms = NULL, lease_expires_at_ms = NULL,
				last_error = COALESCE(last_error, 'dependency mapping attempts exhausted before durable recovery'),
				updated_at_ms = MAX(updated_at_ms, CAST(strftime('%s', 'now') AS INTEGER) * 1000),
				completed_at_ms = COALESCE(completed_at_ms, CAST(strftime('%s', 'now') AS INTEGER) * 1000)
			WHERE phase = 'dependency_mapping'
				AND status IN ('pending', 'running')
				AND attempts >= 3`,
		},
	},
	{
		version: 13,
		name:    "canonical-frame-compaction-count",
		statements: []string{
			`INSERT OR IGNORE INTO frame_runtime_metadata (frame_id, context_data)
			SELECT DISTINCT archive.frame_id, '{}'
			FROM compaction_archives AS archive`,
			`UPDATE frame_runtime_metadata
			SET context_data = json_set(
				CASE
					WHEN context_data IS NOT NULL AND json_valid(context_data) THEN context_data
					ELSE '{}'
				END,
				'$._compaction_count',
				(
					SELECT COALESCE(MAX(archive.compaction_index), -1) + 1
					FROM compaction_archives AS archive
					WHERE archive.frame_id = frame_runtime_metadata.frame_id
				)
			)
			WHERE EXISTS (
				SELECT 1 FROM compaction_archives AS archive
				WHERE archive.frame_id = frame_runtime_metadata.frame_id
			)`,
		},
	},
	{
		version: 14,
		name:    "canonical-frame-execution-claims",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS frame_execution_claims (
				frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
				runner_id TEXT NOT NULL DEFAULT '',
				state TEXT NOT NULL CHECK (state IN ('claimed', 'parked', 'terminal')),
				runner_status TEXT NOT NULL,
				attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt >= 1),
				previous_runner_id TEXT,
				reclaimed_expired_lease INTEGER NOT NULL DEFAULT 0,
				claimed_at_ms INTEGER,
				last_heartbeat_at_ms INTEGER,
				expires_at_ms INTEGER,
				last_checkpoint TEXT,
				last_checkpoint_at_ms INTEGER,
				last_checkpoint_event_id INTEGER,
				updated_at_ms INTEGER NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS frame_execution_claims_ready_idx
				ON frame_execution_claims (state, expires_at_ms, updated_at_ms, frame_id)`,
			`CREATE TABLE IF NOT EXISTS workspace_legacy_imports (
				kind TEXT PRIMARY KEY,
				source_path TEXT NOT NULL,
				source_sha256 TEXT NOT NULL,
				record_count INTEGER NOT NULL DEFAULT 0 CHECK (record_count >= 0),
				applied_at_ms INTEGER NOT NULL
			)`,
		},
	},
	{
		version: 15,
		name:    "canonical-frame-event-revisions",
		statements: []string{
			`CREATE TRIGGER IF NOT EXISTS frame_events_trace_update
			AFTER UPDATE ON frame_events BEGIN
				UPDATE frame_trace_clock SET revision = revision + 1 WHERE singleton = 1;
				INSERT INTO frame_trace_revisions (frame_id, revision)
				VALUES (NEW.frame_id, (SELECT revision FROM frame_trace_clock WHERE singleton = 1))
				ON CONFLICT(frame_id) DO UPDATE SET revision = excluded.revision;
			END`,
			`CREATE TRIGGER IF NOT EXISTS frame_events_trace_delete
			AFTER DELETE ON frame_events BEGIN
				UPDATE frame_trace_clock SET revision = revision + 1 WHERE singleton = 1;
				INSERT INTO frame_trace_revisions (frame_id, revision)
				VALUES (OLD.frame_id, (SELECT revision FROM frame_trace_clock WHERE singleton = 1))
				ON CONFLICT(frame_id) DO UPDATE SET revision = excluded.revision;
			END`,
		},
	},
	{
		version: 16,
		name:    "canonical-task-intent-frame-lifecycle",
		statements: []string{
			`ALTER TABLE frame_active_task_intents RENAME TO frame_active_task_intents_v15`,
			`ALTER TABLE frame_task_intents RENAME TO frame_task_intents_v15`,
			`CREATE TABLE frame_task_intents (
				id TEXT PRIMARY KEY,
				frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
				revision INTEGER NOT NULL,
				source_event_id TEXT NOT NULL UNIQUE REFERENCES frame_events(id) ON DELETE CASCADE,
				source_message_id TEXT NOT NULL,
				origin TEXT NOT NULL,
				language TEXT NOT NULL,
				text TEXT NOT NULL,
				created_at TIMESTAMP NOT NULL,
				UNIQUE (frame_id, revision),
				UNIQUE (frame_id, source_message_id)
			)`,
			`INSERT INTO frame_task_intents
				(id, frame_id, revision, source_event_id, source_message_id, origin, language, text, created_at)
			SELECT id, frame_id, revision, source_event_id, source_message_id, origin, language, text, created_at
			FROM frame_task_intents_v15`,
			`CREATE TABLE frame_active_task_intents (
				frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
				task_intent_id TEXT NOT NULL UNIQUE REFERENCES frame_task_intents(id) ON DELETE CASCADE,
				updated_at TIMESTAMP NOT NULL
			)`,
			`INSERT INTO frame_active_task_intents (frame_id, task_intent_id, updated_at)
			SELECT frame_id, task_intent_id, updated_at FROM frame_active_task_intents_v15`,
			`DROP TABLE frame_active_task_intents_v15`,
			`DROP TABLE frame_task_intents_v15`,
			`CREATE INDEX frame_task_intents_frame_revision_idx
				ON frame_task_intents (frame_id, revision DESC)`,
		},
	},
	{
		version: 17,
		name:    "canonical-frame-event-delete-lifecycle",
		statements: []string{
			`DROP TRIGGER frame_events_trace_delete`,
			`CREATE TRIGGER frame_events_trace_delete
			AFTER DELETE ON frame_events BEGIN
				UPDATE frame_trace_clock SET revision = revision + 1 WHERE singleton = 1;
				INSERT INTO frame_trace_revisions (frame_id, revision)
				SELECT OLD.frame_id, revision FROM frame_trace_clock
				WHERE singleton = 1
					AND EXISTS (SELECT 1 FROM frames WHERE id = OLD.frame_id)
				ON CONFLICT(frame_id) DO UPDATE SET revision = excluded.revision;
			END`,
		},
	},
	{
		version: 18,
		name:    "claude-science-canonical-artifact-priority",
		statements: []string{
			`ALTER TABLE artifacts RENAME COLUMN priority TO legacy_numeric_priority`,
			`ALTER TABLE artifacts ADD COLUMN priority TEXT NOT NULL DEFAULT 'unknown'
				CHECK (priority IN ('unknown', 'user_starred', 'user_hidden', 'user_no_priority'))`,
			`UPDATE artifacts
			SET priority = COALESCE((
				SELECT CASE
					WHEN metadata.priority_label IN ('unknown', 'user_starred', 'user_hidden', 'user_no_priority')
						THEN metadata.priority_label
					ELSE 'unknown'
				END
				FROM artifact_runtime_metadata AS metadata
				WHERE metadata.artifact_id = artifacts.id
			), 'unknown')`,
			`ALTER TABLE artifact_runtime_metadata DROP COLUMN priority_label`,
			`ALTER TABLE artifacts DROP COLUMN legacy_numeric_priority`,
		},
	},
	{
		version: 19,
		name:    "claude-science-nullable-project-description",
		statements: []string{
			`ALTER TABLE project_runtime_metadata RENAME TO project_runtime_metadata_v18`,
			`CREATE TABLE project_runtime_metadata (
				project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
				description TEXT,
				context_data TEXT NOT NULL DEFAULT 'null'
			)`,
			`INSERT INTO project_runtime_metadata (project_id, description, context_data)
			SELECT project_id, NULLIF(description, ''), context_data
			FROM project_runtime_metadata_v18`,
			`DROP TABLE project_runtime_metadata_v18`,
		},
	},
	{
		version: 20,
		name:    "claude-science-canonical-approval-policy",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS approval_policy_grants (
				id TEXT PRIMARY KEY,
				owner_user_id TEXT NOT NULL,
				kind TEXT NOT NULL,
				grant_key TEXT NOT NULL,
				scope TEXT NOT NULL CHECK (scope IN ('conversation', 'project', 'always')),
				tier TEXT NOT NULL CHECK (tier IN ('allow', 'deny', 'ask')),
				scope_target_id TEXT NOT NULL DEFAULT '',
				origin_user_id TEXT,
				origin_project_id TEXT,
				origin_root_frame_id TEXT,
				created_at_ms INTEGER NOT NULL,
				updated_at_ms INTEGER NOT NULL,
				CHECK (
					(scope = 'always' AND scope_target_id = '') OR
					(scope IN ('conversation', 'project') AND scope_target_id != '')
				),
				UNIQUE (owner_user_id, kind, grant_key, scope, scope_target_id)
			)`,
			`CREATE INDEX IF NOT EXISTS approval_policy_grants_owner_kind_idx
				ON approval_policy_grants (owner_user_id, kind, tier, scope, scope_target_id)`,
			`CREATE TABLE IF NOT EXISTS approval_policy_imports (
				source TEXT PRIMARY KEY,
				source_sha256 TEXT NOT NULL,
				grant_count INTEGER NOT NULL CHECK (grant_count >= 0),
				imported_at_ms INTEGER NOT NULL
			)`,
			`INSERT INTO approval_policy_grants (
				id, owner_user_id, kind, grant_key, scope, tier, scope_target_id,
				origin_user_id, created_at_ms, updated_at_ms
			)
			SELECT 'legacy-mcp-global:' || grant.id, grant.user_id, 'mcp_tool',
				grant.mcp_server_id || char(0) || grant.tool_name, 'always',
				CASE WHEN grant.enabled THEN 'allow' ELSE 'deny' END, '', grant.user_id,
				COALESCE(CAST(strftime('%s', grant.created_at) AS INTEGER) * 1000,
					CAST(strftime('%s', 'now') AS INTEGER) * 1000),
				COALESCE(CAST(strftime('%s', grant.updated_at) AS INTEGER) * 1000,
					CAST(strftime('%s', 'now') AS INTEGER) * 1000)
			FROM mcp_tool_grants AS grant
			WHERE grant.agent_name = '*'
			ON CONFLICT(owner_user_id, kind, grant_key, scope, scope_target_id) DO UPDATE SET
				tier = excluded.tier, origin_user_id = excluded.origin_user_id,
				updated_at_ms = excluded.updated_at_ms`,
			`INSERT INTO approval_policy_grants (
				id, owner_user_id, kind, grant_key, scope, tier, scope_target_id,
				origin_user_id, created_at_ms, updated_at_ms
			)
			SELECT 'legacy-mcp-connector:' || policy.user_id || ':' || policy.connector_id || ':' || policy.tool_name,
				policy.user_id, 'mcp_tool', policy.connector_id || char(0) || policy.tool_name,
				'always', CASE WHEN policy.enabled THEN 'allow' ELSE 'deny' END, '', policy.user_id,
				COALESCE(CAST(strftime('%s', policy.updated_at) AS INTEGER) * 1000,
					CAST(strftime('%s', 'now') AS INTEGER) * 1000),
				COALESCE(CAST(strftime('%s', policy.updated_at) AS INTEGER) * 1000,
					CAST(strftime('%s', 'now') AS INTEGER) * 1000)
			FROM mcp_connector_tool_policies AS policy
			WHERE true
			ON CONFLICT(owner_user_id, kind, grant_key, scope, scope_target_id) DO UPDATE SET
				tier = excluded.tier, origin_user_id = excluded.origin_user_id,
				updated_at_ms = excluded.updated_at_ms`,
		},
	},
}
