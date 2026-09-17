package workspace

import (
	"context"
	"errors"
	"fmt"
)

func (s *Store) migrate(ctx context.Context) error {
	return s.migrateWithExecutor(ctx, s.db)
}

func (s *Store) migrateWithExecutor(ctx context.Context, executor schemaMigrationExecutor) error {
	if executor == nil {
		return errors.New("workspace schema migration executor is required")
	}
	if err := prepareSchemaJournal(ctx, executor); err != nil {
		return err
	}
	if err := s.prepareLegacyWorkspaceSchema(ctx, executor); err != nil {
		return err
	}
	return applyVersionedSchemaMigrations(ctx, executor, s.now)
}

func (s *Store) prepareLegacyWorkspaceSchema(ctx context.Context, executor schemaMigrationExecutor) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL DEFAULT 'local',
			name TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS project_runtime_metadata (
			project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
			description TEXT NOT NULL DEFAULT '',
			context_data TEXT NOT NULL DEFAULT 'null'
		)`,
		`CREATE TABLE IF NOT EXISTS frames (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			parent_frame_id TEXT REFERENCES frames(id) ON DELETE SET NULL,
			root_frame_id TEXT NOT NULL,
			root_sequence INTEGER NOT NULL DEFAULT 0,
			agent_name TEXT NOT NULL,
			status TEXT NOT NULL,
			conversation_type TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS frames_project_root_seq_idx
			ON frames (project_id, root_frame_id, root_sequence)`,
		`CREATE TABLE IF NOT EXISTS frame_events (
			id TEXT PRIMARY KEY,
			frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			sequence INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			payload TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			UNIQUE (frame_id, sequence)
		)`,
		`CREATE INDEX IF NOT EXISTS frame_events_cursor_idx
			ON frame_events (frame_id, sequence)`,
		`CREATE TABLE IF NOT EXISTS frame_trace_clock (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			revision INTEGER NOT NULL CHECK (revision >= 0)
		)`,
		`INSERT OR IGNORE INTO frame_trace_clock (singleton, revision) VALUES (1, 0)`,
		`CREATE TABLE IF NOT EXISTS frame_trace_revisions (
			frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
			revision INTEGER NOT NULL CHECK (revision >= 0)
		)`,
		`INSERT OR IGNORE INTO frame_trace_revisions (frame_id, revision)
			SELECT id, 0 FROM frames`,
		`CREATE TRIGGER IF NOT EXISTS frames_trace_insert
			AFTER INSERT ON frames BEGIN
				UPDATE frame_trace_clock SET revision = revision + 1 WHERE singleton = 1;
				INSERT INTO frame_trace_revisions (frame_id, revision)
				VALUES (NEW.id, (SELECT revision FROM frame_trace_clock WHERE singleton = 1))
				ON CONFLICT(frame_id) DO UPDATE SET revision = excluded.revision;
			END`,
		`CREATE TRIGGER IF NOT EXISTS frames_trace_update
			AFTER UPDATE ON frames BEGIN
				UPDATE frame_trace_clock SET revision = revision + 1 WHERE singleton = 1;
				INSERT INTO frame_trace_revisions (frame_id, revision)
				VALUES (NEW.id, (SELECT revision FROM frame_trace_clock WHERE singleton = 1))
				ON CONFLICT(frame_id) DO UPDATE SET revision = excluded.revision;
			END`,
		`CREATE TRIGGER IF NOT EXISTS frames_trace_delete
			AFTER DELETE ON frames BEGIN
				UPDATE frame_trace_clock SET revision = revision + 1 WHERE singleton = 1;
			END`,
		`CREATE TRIGGER IF NOT EXISTS frame_events_trace_insert
			AFTER INSERT ON frame_events BEGIN
				UPDATE frame_trace_clock SET revision = revision + 1 WHERE singleton = 1;
				INSERT INTO frame_trace_revisions (frame_id, revision)
				VALUES (NEW.frame_id, (SELECT revision FROM frame_trace_clock WHERE singleton = 1))
				ON CONFLICT(frame_id) DO UPDATE SET revision = excluded.revision;
			END`,
		`CREATE TABLE IF NOT EXISTS frame_read_cursors (
			root_frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
			message_uuid TEXT,
			message_index INTEGER NOT NULL CHECK (message_index >= 0),
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS frame_read_cursors_updated_idx
			ON frame_read_cursors (updated_at)`,
		`CREATE TABLE IF NOT EXISTS transcript_annotations (
			id TEXT PRIMARY KEY,
			root_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			message_uuid TEXT,
			message_index INTEGER NOT NULL CHECK (message_index >= 0),
			block_index INTEGER NOT NULL DEFAULT 0 CHECK (block_index >= 0),
			source TEXT NOT NULL CHECK (source IN ('assistant', 'tool_input', 'tool_result')),
			tool_name TEXT,
			anchor_text TEXT NOT NULL,
			start_offset INTEGER CHECK (start_offset IS NULL OR start_offset >= 0),
			end_offset INTEGER CHECK (end_offset IS NULL OR end_offset >= 0),
			kind TEXT NOT NULL CHECK (kind IN ('annotation', 'bookmark')),
			origin TEXT NOT NULL DEFAULT 'user' CHECK (origin IN ('user', 'agent')),
			read_at TIMESTAMP,
			note TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			CHECK (start_offset IS NULL OR end_offset IS NULL OR start_offset <= end_offset)
		)`,
		`CREATE INDEX IF NOT EXISTS transcript_annotations_root_created_idx
			ON transcript_annotations (root_frame_id, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS user_agents (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			description TEXT NOT NULL,
			system_prompt TEXT NOT NULL,
			icon_key TEXT NOT NULL DEFAULT '',
			color_key TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '[]',
			skill_names TEXT NOT NULL DEFAULT '[]',
			skill_tombstones TEXT NOT NULL DEFAULT '[]',
			connector_tombstones TEXT NOT NULL DEFAULT '[]',
			unrestricted INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE (user_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS agent_custom_prompts (
			user_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			prompt_text TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, agent_name)
		)`,
		`CREATE TABLE IF NOT EXISTS agent_connector_exclusions (
			user_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			connector_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, agent_name, connector_id),
			FOREIGN KEY (user_id, agent_name) REFERENCES user_agents(user_id, name)
				ON UPDATE CASCADE ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS skill_preferences (
			user_id TEXT NOT NULL,
			skill_name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, skill_name)
		)`,
		`CREATE TABLE IF NOT EXISTS model_providers (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			base_url TEXT NOT NULL,
			model TEXT NOT NULL,
			secret_ref TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS model_providers_user_updated_idx
			ON model_providers (user_id, updated_at DESC, id DESC)`,
		`CREATE TABLE IF NOT EXISTS custom_mcp_servers (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL,
			transport TEXT NOT NULL,
			oauth_server_url TEXT NOT NULL DEFAULT '',
			client_id TEXT NOT NULL DEFAULT '',
			scopes TEXT NOT NULL DEFAULT '',
			headers_helper TEXT NOT NULL DEFAULT '',
			config_json TEXT NOT NULL DEFAULT '{}',
			builtin INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE (user_id, name)
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_tool_grants (
			id TEXT PRIMARY KEY,
			mcp_server_id TEXT NOT NULL REFERENCES custom_mcp_servers(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE (mcp_server_id, user_id, agent_name, tool_name)
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_connector_tool_policies (
			user_id TEXT NOT NULL,
			connector_id TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, connector_id, tool_name)
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_agent_assignments (
			id TEXT PRIMARY KEY,
			mcp_server_id TEXT NOT NULL REFERENCES custom_mcp_servers(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			UNIQUE (mcp_server_id, user_id, agent_name),
			FOREIGN KEY (user_id, agent_name) REFERENCES user_agents(user_id, name)
				ON UPDATE CASCADE ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS agent_connector_attachments (
			server_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			source TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (server_id, user_id, agent_name),
			FOREIGN KEY (user_id, agent_name) REFERENCES user_agents(user_id, name)
				ON UPDATE CASCADE ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS agent_connector_tool_exclusions (
			server_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			position INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (server_id, user_id, agent_name, tool_name),
			FOREIGN KEY (server_id, user_id, agent_name)
				REFERENCES agent_connector_attachments(server_id, user_id, agent_name) ON DELETE CASCADE
		)`,
		`CREATE TRIGGER IF NOT EXISTS sync_custom_mcp_assignment_insert
		AFTER INSERT ON mcp_agent_assignments
		BEGIN
			INSERT INTO agent_connector_attachments (server_id, user_id, agent_name, source, created_at)
			VALUES (NEW.mcp_server_id, NEW.user_id, NEW.agent_name, 'custom', NEW.created_at)
			ON CONFLICT(server_id, user_id, agent_name) DO UPDATE SET source = 'custom';
		END`,
		`CREATE TRIGGER IF NOT EXISTS sync_custom_mcp_assignment_delete
		AFTER DELETE ON mcp_agent_assignments
		BEGIN
			DELETE FROM agent_connector_attachments
			WHERE server_id = OLD.mcp_server_id AND user_id = OLD.user_id
				AND agent_name = OLD.agent_name AND source = 'custom';
		END`,
		`INSERT INTO agent_connector_attachments (server_id, user_id, agent_name, source, created_at)
		SELECT mcp_server_id, user_id, agent_name, 'custom', created_at FROM mcp_agent_assignments
		WHERE true
		ON CONFLICT(server_id, user_id, agent_name) DO UPDATE SET source = 'custom'`,
		`CREATE TABLE IF NOT EXISTS mcp_oauth_status (
			mcp_server_id TEXT NOT NULL REFERENCES custom_mcp_servers(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			access_token_ref TEXT NOT NULL,
			token_type TEXT NOT NULL,
			expires_at TIMESTAMP,
			scopes TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (mcp_server_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_directories (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			url TEXT NOT NULL,
			catalog_uuid TEXT NOT NULL,
			etag TEXT NOT NULL DEFAULT '',
			last_status TEXT NOT NULL DEFAULT 'pending',
			last_error TEXT NOT NULL DEFAULT '',
			last_checked_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE (user_id, catalog_uuid)
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_directory_connectors (
			user_id TEXT NOT NULL,
			id TEXT NOT NULL,
			directory_id TEXT NOT NULL REFERENCES mcp_directories(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL,
			transport TEXT NOT NULL,
			auth_required INTEGER NOT NULL DEFAULT 0,
			auth_hint TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			last_status TEXT NOT NULL DEFAULT 'configured',
			last_error TEXT NOT NULL DEFAULT '',
			tool_count INTEGER NOT NULL DEFAULT 0,
			content_sha256 TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS mcp_directory_connectors_directory_idx
			ON mcp_directory_connectors (directory_id, id)`,
		`CREATE TABLE IF NOT EXISTS mcp_connector_runtime_state (
			user_id TEXT NOT NULL,
			source TEXT NOT NULL,
			connector_id TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			last_status TEXT NOT NULL DEFAULT 'configured',
			last_error TEXT NOT NULL DEFAULT '',
			tool_count INTEGER NOT NULL DEFAULT 0,
			schema_sha256 TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, source, connector_id)
		)`,
		`CREATE TABLE IF NOT EXISTS mcp_directory_agent_assignments (
			user_id TEXT NOT NULL,
			connector_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, connector_id, agent_name),
			FOREIGN KEY (user_id, connector_id) REFERENCES mcp_directory_connectors(user_id, id) ON DELETE CASCADE,
			FOREIGN KEY (user_id, agent_name) REFERENCES user_agents(user_id, name) ON UPDATE CASCADE ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS compute_providers (
			name TEXT PRIMARY KEY,
			owner_user_id TEXT NOT NULL DEFAULT 'local',
			family TEXT NOT NULL,
			endpoint TEXT NOT NULL DEFAULT '',
			skill_name TEXT NOT NULL DEFAULT '',
			credential_name TEXT NOT NULL DEFAULT '',
			hosted INTEGER NOT NULL DEFAULT 0,
			environments TEXT NOT NULL DEFAULT '[]',
			data_roots TEXT NOT NULL DEFAULT '[]',
			ssh_overrides TEXT NOT NULL DEFAULT '{}',
			max_concurrent_jobs INTEGER,
			max_timeout_sec INTEGER,
			memory_md TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 0,
			app_name TEXT,
			prior_app_names TEXT NOT NULL DEFAULT '[]',
			egress_policy TEXT,
			modal_environment TEXT,
			scratch_root TEXT NOT NULL DEFAULT '',
			scheduler TEXT NOT NULL DEFAULT '',
			details_md TEXT NOT NULL DEFAULT '',
			details_rev INTEGER NOT NULL DEFAULT 0,
			probed_at TIMESTAMP,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS compute_usage (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL UNIQUE,
			environment TEXT NOT NULL,
			tier_type TEXT NOT NULL,
			provider TEXT NOT NULL,
			frame_id TEXT,
			project_id TEXT,
			started_at TIMESTAMP NOT NULL,
			ended_at TIMESTAMP,
			expires_at TIMESTAMP,
			client_uuid TEXT,
			remote_workdir TEXT,
			remote_handle TEXT,
			state TEXT NOT NULL DEFAULT 'pending',
			output_specs TEXT,
			submit_cell_id TEXT,
			intent TEXT,
			hardware_details TEXT,
			root_frame_id TEXT,
			result TEXT,
			origin_tool_use_id TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS poller_lease (
			provider TEXT PRIMARY KEY NOT NULL,
			holder TEXT NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS compute_pending_terminate (
			sandbox_id TEXT PRIMARY KEY NOT NULL,
			provider TEXT NOT NULL,
			job_id TEXT NOT NULL DEFAULT '',
			enqueued_at INTEGER NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0)
		)`,
		`CREATE TABLE IF NOT EXISTS routine_schedules (
			id TEXT PRIMARY KEY,
			root_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			owner_user_id TEXT NOT NULL,
			label TEXT,
			on_tick TEXT NOT NULL,
			every_minutes INTEGER NOT NULL CHECK (every_minutes > 0),
			enabled INTEGER NOT NULL DEFAULT 0,
			locked_at TIMESTAMP,
			claim_generation INTEGER NOT NULL DEFAULT 0,
			claim_token TEXT,
			paused_reason TEXT,
			next_due TIMESTAMP NOT NULL,
			tick_count INTEGER NOT NULL DEFAULT 0,
			missed_ticks INTEGER NOT NULL DEFAULT 0,
			last_fire_at TIMESTAMP,
			last_ok_at TIMESTAMP,
			idle_streak INTEGER NOT NULL DEFAULT 0,
			last_results TEXT,
			host_done_locked_at TIMESTAMP,
			host_done_claim_generation INTEGER,
			host_done_claim_token TEXT,
			host_had_work INTEGER,
			host_done_summary TEXT,
			host_done_at TIMESTAMP,
			completion_claim_generation INTEGER,
			completion_claim_token TEXT,
			completion_successful INTEGER,
			completion_at TIMESTAMP,
			completion_result_hash TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE (root_frame_id)
		)`,
		`CREATE INDEX IF NOT EXISTS routine_schedules_due_idx
			ON routine_schedules (next_due) WHERE enabled = 1`,
		`CREATE TABLE IF NOT EXISTS artifact_folders (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			parent_id TEXT REFERENCES artifact_folders(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			sort_order INTEGER NOT NULL DEFAULT 0,
			root_frame_id TEXT REFERENCES frames(id) ON DELETE SET NULL,
			is_conversation_folder INTEGER NOT NULL DEFAULT 0,
			is_user_uploads_folder INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE (project_id, parent_id, name)
		)`,
		`CREATE INDEX IF NOT EXISTS artifact_folders_project_idx
			ON artifact_folders (project_id, parent_id, sort_order)`,
		`CREATE TABLE IF NOT EXISTS artifacts (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			current_version_number INTEGER NOT NULL DEFAULT 0,
			folder_id TEXT REFERENCES artifact_folders(id) ON DELETE SET NULL,
			priority INTEGER NOT NULL DEFAULT 0,
			retention_mode TEXT NOT NULL DEFAULT 'snapshot' CHECK(retention_mode IN ('snapshot','working_data')),
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS artifact_versions (
			id TEXT PRIMARY KEY,
			artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
			version_number INTEGER NOT NULL,
			parent_id TEXT REFERENCES artifact_versions(id),
			content BLOB NOT NULL,
			content_sha256 TEXT NOT NULL,
			storage_path TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			created_by TEXT NOT NULL DEFAULT '',
			content_available INTEGER NOT NULL DEFAULT 1 CHECK(content_available IN (0,1)),
			created_at TIMESTAMP NOT NULL,
			UNIQUE (artifact_id, version_number)
		)`,
		`CREATE INDEX IF NOT EXISTS artifact_versions_lineage_idx
			ON artifact_versions (artifact_id, version_number DESC)`,
		`CREATE TABLE IF NOT EXISTS annotations (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			target_kind TEXT NOT NULL,
			target_key TEXT NOT NULL,
			label_idx INTEGER NOT NULL,
			content_checksum TEXT,
			body TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS annotations_project_target_idx
			ON annotations (project_id, target_key, label_idx)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS annotations_target_label_unique_idx
			ON annotations (project_id, target_key, label_idx)`,
		`CREATE TABLE IF NOT EXISTS attachments (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			filename TEXT NOT NULL,
			content_type TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			sha256 TEXT NOT NULL,
			blob_path TEXT NOT NULL UNIQUE,
			ephemeral INTEGER NOT NULL DEFAULT 0,
			folder_id TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS attachments_user_project_idx
			ON attachments (user_id, project_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS attachment_uploads (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			filename TEXT NOT NULL,
			content_type TEXT NOT NULL,
			total_size INTEGER NOT NULL,
			chunk_size INTEGER NOT NULL,
			expected_chunks INTEGER NOT NULL,
			ephemeral INTEGER NOT NULL DEFAULT 0,
			folder_id TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS attachment_uploads_user_idx
			ON attachment_uploads (user_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS attachment_upload_chunks (
			upload_id TEXT NOT NULL REFERENCES attachment_uploads(id) ON DELETE CASCADE,
			chunk_index INTEGER NOT NULL,
			size_bytes INTEGER NOT NULL,
			sha256 TEXT NOT NULL,
			blob_path TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (upload_id, chunk_index)
		)`,
		`CREATE INDEX IF NOT EXISTS attachment_upload_chunks_upload_idx
			ON attachment_upload_chunks (upload_id, chunk_index)`,
		`CREATE TABLE IF NOT EXISTS notes (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			target_type TEXT NOT NULL,
			target_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			target_message_index INTEGER,
			target_artifact_id TEXT REFERENCES artifacts(id) ON DELETE SET NULL,
			content TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS notes_project_idx
			ON notes (project_id, target_type, target_frame_id)`,
		`CREATE TABLE IF NOT EXISTS memories (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			body TEXT NOT NULL,
			subject_project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
			origin TEXT NOT NULL,
			evidence TEXT NOT NULL DEFAULT 'stated',
			superseded_by TEXT REFERENCES memories(id) ON DELETE SET NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS memories_active_user_idx
			ON memories (user_id, superseded_by)`,
		`CREATE INDEX IF NOT EXISTS memories_project_idx
			ON memories (subject_project_id)`,
		`CREATE TABLE IF NOT EXISTS frame_runtime_metadata (
			frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
			delegate_name TEXT,
			input_data TEXT,
			output_data TEXT,
			context_data TEXT,
			model TEXT,
			effort TEXT,
			input_tokens INTEGER,
			output_tokens INTEGER,
			cache_read_tokens INTEGER,
			cache_write_tokens INTEGER,
			total_cost REAL,
			completed_at TIMESTAMP,
			artifact_id TEXT,
			task_summary TEXT,
			status_description TEXT,
			mentioned_artifact_ids TEXT,
			specialists_used TEXT,
			is_hidden INTEGER NOT NULL DEFAULT 0,
			compute_enabled TEXT,
			last_user_message_at TIMESTAMP,
			last_extract_msg_idx INTEGER,
			aux_input_tokens INTEGER,
			aux_output_tokens INTEGER,
			aux_cache_read_tokens INTEGER,
			aux_cache_write_tokens INTEGER,
			aux_cost REAL,
			token_class_usage TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS artifact_runtime_metadata (
			artifact_id TEXT PRIMARY KEY REFERENCES artifacts(id) ON DELETE CASCADE,
			root_frame_id TEXT,
			frame_id TEXT,
			latest_version_id TEXT,
			is_user_upload INTEGER NOT NULL DEFAULT 0,
			is_ephemeral INTEGER NOT NULL DEFAULT 0,
			sort_order INTEGER NOT NULL DEFAULT 0,
			priority_label TEXT NOT NULL DEFAULT 'unknown',
			superseded_by_artifact_id TEXT,
			consumed_at TIMESTAMP,
			is_branch_mint INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS artifact_runtime_metadata_root_idx
			ON artifact_runtime_metadata (root_frame_id, artifact_id)`,
		`CREATE TABLE IF NOT EXISTS artifact_version_provenance (
			version_id TEXT PRIMARY KEY REFERENCES artifact_versions(id) ON DELETE CASCADE,
			frame_id TEXT,
			content_type TEXT NOT NULL,
			extracted_code TEXT,
			code_description TEXT,
			lineage_messages TEXT,
			agent_name TEXT,
			language TEXT,
			is_intermediate INTEGER NOT NULL DEFAULT 0,
			dependency_mappings TEXT,
			environment_snapshot TEXT,
			annotations TEXT,
			lineage_snapshot_hash TEXT,
			env_snapshot_hash TEXT,
			producing_cell_id TEXT,
			cell_sources TEXT,
			is_checkpoint INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS artifact_version_dependencies (
			id TEXT PRIMARY KEY,
			artifact_version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
			depends_on_version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
			reference_name TEXT,
			created_at TIMESTAMP NOT NULL,
			UNIQUE (artifact_version_id, depends_on_version_id)
		)`,
		`CREATE TABLE IF NOT EXISTS content_snapshots (
			hash TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			size_bytes INTEGER NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS execution_log (
			id TEXT PRIMARY KEY,
			frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			cell_index INTEGER NOT NULL,
			kernel_id TEXT NOT NULL,
			kernel_kind TEXT,
			conda_env TEXT NOT NULL,
			language TEXT NOT NULL,
			source TEXT NOT NULL,
			stdout TEXT,
			stderr TEXT,
			exit_status TEXT NOT NULL,
			origin TEXT NOT NULL DEFAULT 'agent',
			created_at TIMESTAMP NOT NULL,
			files_written TEXT,
			files_read TEXT,
			error_lineno INTEGER,
			detection TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS execution_log_frame_cell_idx
			ON execution_log (frame_id, cell_index, id)`,
		`CREATE TABLE IF NOT EXISTS artifact_version_execution_links (
			version_id TEXT NOT NULL REFERENCES artifact_versions(id) ON DELETE CASCADE,
			execution_log_id TEXT NOT NULL REFERENCES execution_log(id) ON DELETE CASCADE,
			cell_index INTEGER NOT NULL,
			PRIMARY KEY (version_id, execution_log_id)
		)`,
		`CREATE TABLE IF NOT EXISTS frame_system_prompts (
			frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
			hash TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS frame_branch_archives (
			frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			branch_id TEXT NOT NULL,
			payload TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (frame_id, branch_id)
		)`,
		`CREATE TABLE IF NOT EXISTS session_claims (
			id TEXT PRIMARY KEY,
			root_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			step_id TEXT,
			claim_text TEXT NOT NULL,
			entities TEXT,
			source TEXT NOT NULL CHECK (source IN ('agent', 'llm_extracted')),
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS session_claims_root_idx
			ON session_claims (root_frame_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS session_claims_frame_idx
			ON session_claims (frame_id, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS verification_checks (
			id TEXT PRIMARY KEY,
			root_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			artifact_version_id TEXT REFERENCES artifact_versions(id) ON DELETE CASCADE,
			claim_id TEXT REFERENCES session_claims(id) ON DELETE SET NULL,
			claim TEXT,
			verdict TEXT NOT NULL CHECK (verdict IN ('pass', 'warn', 'fail', 'inconclusive')),
			severity TEXT,
			evidence TEXT,
			rebuttal TEXT,
			reviewer_idx INTEGER,
			reviewer_model TEXT,
			reviewer_frame_id TEXT REFERENCES frames(id) ON DELETE SET NULL,
			source_ref TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'unaddressed')),
			reflag_count INTEGER,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS verification_checks_root_idx
			ON verification_checks (root_frame_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS verification_checks_status_idx
			ON verification_checks (root_frame_id, status, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS verification_checks_artifact_version_idx
			ON verification_checks (artifact_version_id, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS compaction_archives (
			id TEXT PRIMARY KEY,
			frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			compaction_index INTEGER NOT NULL,
			message_count INTEGER NOT NULL,
			token_count INTEGER,
			summary TEXT NOT NULL,
			messages TEXT NOT NULL,
			source_event_id INTEGER,
			created_at TIMESTAMP NOT NULL,
			UNIQUE (frame_id, compaction_index),
			UNIQUE (frame_id, source_event_id)
		)`,
		`CREATE INDEX IF NOT EXISTS compaction_archives_frame_idx
			ON compaction_archives (frame_id, compaction_index)`,
		`CREATE TABLE IF NOT EXISTS queued_user_messages (
			sequence INTEGER PRIMARY KEY,
			frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			payload TEXT NOT NULL,
			intent_id TEXT NOT NULL UNIQUE,
			state TEXT NOT NULL,
			resolved_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS queued_user_messages_frame_idx
			ON queued_user_messages (frame_id, sequence)`,
		`CREATE TABLE IF NOT EXISTS session_seen_marks (
			root_frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
			seen_token TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS memory_categories (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			name_lower TEXT NOT NULL,
			guidance TEXT NOT NULL,
			auto_recall INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE (user_id, name_lower)
		)`,
		`CREATE TABLE IF NOT EXISTS memory_category_assignments (
			memory_id TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
			category_id TEXT REFERENCES memory_categories(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS use_intent_declarations (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			org_id TEXT,
			intent TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS use_intent_declarations_user_idx
			ON use_intent_declarations (user_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS contact_email_decisions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			decision TEXT NOT NULL CHECK (decision IN ('allowed', 'declined', 'revoked')),
			email TEXT,
			notice_version TEXT NOT NULL,
			notice_text TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS contact_email_decisions_user_idx
			ON contact_email_decisions (user_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS directory_attachments (
			server_uuid TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			user_id TEXT NOT NULL,
			excluded_tools TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY (server_uuid, agent_name, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS legacy_user_secrets (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			provider TEXT NOT NULL,
			encrypted_value TEXT NOT NULL,
			credential_type TEXT,
			buckets TEXT,
			region TEXT,
			description TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS runtime_feedback (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			root_frame_id TEXT,
			payload TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS runtime_feedback_user_created_idx
			ON runtime_feedback (user_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS runtime_feedback_frame_idx
			ON runtime_feedback (root_frame_id, created_at DESC)`,
		`DELETE FROM runtime_feedback
			WHERE kind = 'safety' AND rowid NOT IN (
				SELECT MIN(rowid) FROM runtime_feedback
				WHERE kind = 'safety' GROUP BY user_id, root_frame_id
			)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS runtime_feedback_safety_once_idx
			ON runtime_feedback (user_id, root_frame_id) WHERE kind = 'safety'`,
		`CREATE TABLE IF NOT EXISTS realtime_events (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			user_id TEXT NOT NULL,
			project_id TEXT NOT NULL DEFAULT '',
			root_frame_id TEXT NOT NULL DEFAULT '',
			frame_id TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL,
			event_kind TEXT NOT NULL,
			payload TEXT NOT NULL,
			invalidations TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS realtime_events_user_sequence_idx
			ON realtime_events (user_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS realtime_events_project_sequence_idx
			ON realtime_events (project_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS realtime_events_frame_sequence_idx
			ON realtime_events (frame_id, sequence)`,
		`CREATE TABLE IF NOT EXISTS workspace_outbox (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL UNIQUE,
			idempotency_key TEXT NOT NULL,
			topic TEXT NOT NULL,
			partition_key TEXT NOT NULL,
			event_type TEXT NOT NULL,
			aggregate_type TEXT NOT NULL DEFAULT '',
			aggregate_id TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL,
			headers_json TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL CHECK (status IN ('pending', 'inflight', 'delivered', 'dead_letter')),
			attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
			max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
			claim_owner TEXT,
			claim_token TEXT,
			last_error TEXT,
			occurred_at_ms INTEGER NOT NULL,
			available_at_ms INTEGER NOT NULL,
			claimed_at_ms INTEGER,
			lease_expires_at_ms INTEGER,
			delivered_at_ms INTEGER,
			dead_lettered_at_ms INTEGER,
			UNIQUE (topic, idempotency_key)
		)`,
		`CREATE INDEX IF NOT EXISTS workspace_outbox_ready_idx
			ON workspace_outbox (status, available_at_ms, lease_expires_at_ms, sequence)`,
		`CREATE INDEX IF NOT EXISTS workspace_outbox_partition_idx
			ON workspace_outbox (partition_key, status, available_at_ms, sequence)`,
	}
	for _, statement := range statements {
		if _, err := executor.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate workspace database: %w", err)
		}
	}
	if _, err := executor.ExecContext(ctx, `
		UPDATE queued_user_messages
		SET state = 'queued', resolved_at = NULL
		WHERE state = 'delivering'`); err != nil {
		return fmt.Errorf("recover compatibility message deliveries: %w", err)
	}
	if err := s.ensureModelProviderDuplicateNames(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureArtifactFolderColumn(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureProjectUserColumn(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureArtifactPriorityColumn(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureArtifactVersionStorageColumns(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureAgentProfileSchema(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureMCPServerSchema(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureComputeProviderSchema(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureComputeUsageSchema(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureComputePendingTerminationSchema(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureComputeWorkbenchSchemaWithExecutor(ctx, executor); err != nil {
		return err
	}
	if err := s.ensureRoutineHostDoneSchema(ctx, executor); err != nil {
		return err
	}
	if err := s.activateArchivedCompactionArchivesWithExecutor(ctx, executor); err != nil {
		return err
	}
	if err := s.activateArchivedContactEmailDecisionsWithExecutor(ctx, executor); err != nil {
		return err
	}
	if _, err := executor.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS artifacts_folder_idx ON artifacts (folder_id)`); err != nil {
		return fmt.Errorf("create artifact folder index: %w", err)
	}
	return nil
}
