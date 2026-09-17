package v11

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
)

type workspaceImportStatement struct {
	table string
	sql   string
}

func importWorkspace(ctx context.Context, sourceDB, targetDB string, sourceCounts map[string]int64) (map[string]int64, error) {
	db, err := sql.Open("sqlite", targetDB)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `ATTACH DATABASE ? AS legacy`, sourceDB); err != nil {
		return nil, fmt.Errorf("attach v1.1 snapshot: %w", err)
	}
	defer db.Exec(`DETACH DATABASE legacy`)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if sourceCounts["compute_usage"] > 0 {
		var duplicateJobID string
		var duplicateCount int64
		err := tx.QueryRowContext(ctx, `SELECT job_id, COUNT(*) FROM legacy.compute_usage
			GROUP BY job_id HAVING COUNT(*) > 1 ORDER BY job_id LIMIT 1`).Scan(&duplicateJobID, &duplicateCount)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("validate v1.1 compute usage job ids: %w", err)
		}
		if err == nil {
			return nil, fmt.Errorf("validate v1.1 compute usage: duplicate job_id %q appears %d times", duplicateJobID, duplicateCount)
		}
	}

	statements := []workspaceImportStatement{
		{"projects", `INSERT INTO projects (id, user_id, name, path, created_at, updated_at)
			SELECT id, 'local', COALESCE(NULLIF(name, ''), id), '',
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.projects`},
		{"frames", `INSERT OR IGNORE INTO projects (id, user_id, name, path, created_at, updated_at)
			SELECT '__synon_v11_unassigned__', 'local', 'Imported unassigned sessions', '',
			` + legacyTime("MIN(created_at)") + `, ` + legacyTime("MAX(updated_at)") + `
			FROM legacy.frames WHERE project_id IS NULL HAVING COUNT(*) > 0`},
		{"frames", `INSERT INTO frames (id, incarnation_id, project_id, parent_frame_id, root_frame_id, root_sequence, agent_name, status, conversation_type, name, created_at, updated_at)
			SELECT id, lower(hex(randomblob(16))), COALESCE(NULLIF(project_id, ''), '__synon_v11_unassigned__'), NULLIF(parent_frame_id, ''),
			COALESCE(NULLIF(root_frame_id, ''), id), COALESCE(root_seq, 0), agent_name,
			CASE lower(status)
				WHEN 'running' THEN 'processing' WHEN 'executing' THEN 'processing'
				WHEN 'in_progress' THEN 'processing' WHEN 'in-progress' THEN 'processing'
				WHEN 'queued' THEN 'processing' WHEN 'pending' THEN 'processing' WHEN 'created' THEN 'processing'
				WHEN 'waiting_input' THEN 'awaiting_user_response' WHEN 'needs_input' THEN 'awaiting_user_response'
				WHEN 'needs-input' THEN 'awaiting_user_response' WHEN 'awaiting_input' THEN 'awaiting_user_response'
				WHEN 'waiting_approval' THEN 'awaiting_plan_approval'
				WHEN 'error' THEN 'failed' WHEN 'canceled' THEN 'cancelled' WHEN 'stopped' THEN 'cancelled'
				WHEN 'cancelling' THEN 'cancelled' WHEN 'canceling' THEN 'cancelled' ELSE lower(status) END,
			COALESCE(NULLIF(conversation_type, ''), 'agent'), COALESCE(name, ''),
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.frames`},
		{"frames", `INSERT INTO frame_runtime_metadata (
			frame_id, delegate_name, input_data, output_data, context_data, model, effort,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_cost,
			completed_at, artifact_id, task_summary, status_description, mentioned_artifact_ids,
			specialists_used, is_hidden, compute_enabled, last_user_message_at, last_extract_msg_idx,
			aux_input_tokens, aux_output_tokens, aux_cache_read_tokens, aux_cache_write_tokens, aux_cost, token_class_usage
		) SELECT id, delegate_name, input_data, output_data, context_data, model, effort,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_cost,
			` + legacyNullableTime("completed_at") + `, artifact_id, task_summary, status_description,
			mentioned_artifact_ids, specialists_used, COALESCE(is_hidden, 0), compute_enabled,
			` + legacyNullableTime("last_user_message_at") + `, last_extract_msg_idx,
			aux_input_tokens, aux_output_tokens, aux_cache_read_tokens, aux_cache_write_tokens, aux_cost, token_class_usage
			FROM legacy.frames`},
		{"user_agents", `INSERT INTO user_agents (id, user_id, name, display_name, description, system_prompt, skill_names, enabled, created_at, updated_at)
			SELECT id, 'local', name, display_name, description, system_prompt, COALESCE(skill_names, '[]'), enabled,
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.user_agents`},
		{"custom_agent_prompts", `INSERT INTO agent_custom_prompts (user_id, agent_name, prompt_text, created_at, updated_at)
			SELECT 'local', agent_name, prompt_text, ` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.custom_agent_prompts`},
		{"mcp_agent_assignments", `INSERT OR IGNORE INTO user_agents (id, user_id, name, display_name, description, system_prompt, skill_names, enabled, created_at, updated_at)
			SELECT 'legacy-assigned:local:' || agent_name, 'local', agent_name, agent_name,
			'Imported v1.1 connector assignment', '', '[]', 1, ` + legacyTime("MIN(created_at)") + `, ` + legacyTime("MAX(created_at)") + `
			FROM legacy.mcp_agent_assignments GROUP BY agent_name`},
		{"artifact_folders", `INSERT INTO artifact_folders (id, project_id, parent_id, name, sort_order, root_frame_id, is_conversation_folder, is_user_uploads_folder, created_at, updated_at)
			SELECT id, project_id, NULLIF(parent_id, ''), name, sort_order, NULLIF(root_frame_id, ''), is_conversation_folder, is_user_uploads_folder,
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.artifact_folders`},
		{"artifacts", `INSERT INTO artifacts (id, project_id, name, kind, current_version_number, folder_id, priority, created_at, updated_at)
			SELECT a.id, a.project_id, a.filename,
			COALESCE((SELECT v.content_type FROM legacy.artifact_versions v WHERE v.id = a.latest_version_id), 'application/octet-stream'),
			COALESCE((SELECT MAX(v.version_number) FROM legacy.artifact_versions v WHERE v.artifact_id = a.id), 0), NULLIF(a.folder_id, ''),
			CASE lower(COALESCE(a.priority, 'unknown'))
				WHEN 'user_starred' THEN 'user_starred' WHEN 'user_hidden' THEN 'user_hidden'
				WHEN 'user_no_priority' THEN 'user_no_priority' ELSE 'unknown' END,
			` + legacyTime("a.created_at") + `,
			` + legacyTime("COALESCE((SELECT MAX(v.created_at) FROM legacy.artifact_versions v WHERE v.artifact_id = a.id), a.created_at)") + `
			FROM legacy.artifacts a`},
		{"artifacts", `INSERT INTO artifact_runtime_metadata (
			artifact_id, root_frame_id, frame_id, latest_version_id, is_user_upload, is_ephemeral,
			sort_order, superseded_by_artifact_id, consumed_at, is_branch_mint
		) SELECT id, NULLIF(root_frame_id, ''), NULLIF(frame_id, ''), NULLIF(latest_version_id, ''),
			is_user_upload, is_ephemeral, sort_order,
			NULLIF(superseded_by_artifact_id, ''), ` + legacyNullableTime("consumed_at") + `, COALESCE(is_branch_mint, 0)
			FROM legacy.artifacts`},
		{"artifact_versions", `INSERT INTO artifact_versions (id, artifact_id, version_number, parent_id, content, content_sha256, storage_path, size_bytes, created_by, created_at)
			SELECT id, artifact_id, version_number, NULLIF(parent_version_id, ''), X'', lower(checksum),
			'artifact-versions/' || CASE WHEN length(id) > 2 THEN substr(id, 1, 2) ELSE id END || '/' || id || '.blob',
			size_bytes, COALESCE(agent_name, ''), ` + legacyTime("created_at") + ` FROM legacy.artifact_versions`},
		{"artifact_versions", `INSERT INTO artifact_version_provenance (
			version_id, frame_id, content_type, extracted_code, code_description, lineage_messages,
			agent_name, language, is_intermediate, dependency_mappings, environment_snapshot,
			annotations, lineage_snapshot_hash, env_snapshot_hash, producing_cell_id, cell_sources, is_checkpoint
		) SELECT id, NULLIF(frame_id, ''), content_type, extracted_code, code_description, lineage_messages,
			agent_name, language, COALESCE(is_intermediate, 0), dependency_mappings, environment_snapshot,
			annotations, lineage_snapshot_hash, env_snapshot_hash, producing_cell_id, cell_sources, COALESCE(is_checkpoint, 0)
			FROM legacy.artifact_versions`},
		{"artifact_dependencies", `INSERT INTO artifact_version_dependencies (id, artifact_version_id, depends_on_version_id, reference_name, created_at)
			SELECT id, artifact_version_id, depends_on_version_id, reference_name, ` + legacyTime("created_at") + ` FROM legacy.artifact_dependencies`},
		{"content_snapshots", `INSERT INTO content_snapshots (hash, content, size_bytes, created_at)
			SELECT hash, content, size_bytes, ` + legacyTime("created_at") + ` FROM legacy.content_snapshots`},
		{"execution_log", `INSERT INTO execution_log (
			id, frame_id, cell_index, kernel_id, kernel_kind, conda_env, language, source, stdout, stderr,
			exit_status, origin, created_at, files_written, files_read, error_lineno, detection
		) SELECT id, frame_id, cell_index, kernel_id, kernel_kind, conda_env, language, source, stdout, stderr,
			exit_status, COALESCE(origin, 'agent'), ` + legacyTime("created_at") + `, files_written, files_read, error_lineno, detection
			FROM legacy.execution_log`},
		{"execution_log", `INSERT OR IGNORE INTO artifact_version_execution_links (version_id, execution_log_id, cell_index)
			SELECT v.id, l.id, l.cell_index
			FROM legacy.artifact_versions v
			JOIN json_each(v.cell_sources) source
			JOIN legacy.execution_log l ON l.frame_id = v.frame_id AND l.cell_index = CAST(json_extract(source.value, '$.cell_index') AS INTEGER)
			WHERE v.cell_sources IS NOT NULL AND json_valid(v.cell_sources)`},
		{"session_claims", `INSERT INTO session_claims (id, root_frame_id, frame_id, step_id, claim_text, entities, source, created_at)
			SELECT id, root_frame_id, frame_id, NULLIF(step_id, ''), claim_text, entities, source,
			` + legacyTime("created_at") + ` FROM legacy.session_claims`},
		{"verification_checks", `INSERT INTO verification_checks (
			id, root_frame_id, artifact_version_id, claim_id, claim, verdict, severity, evidence, rebuttal,
			reviewer_idx, reviewer_model, reviewer_frame_id, source_ref, status, reflag_count, created_at
		) SELECT id, root_frame_id, NULLIF(artifact_version_id, ''), NULLIF(claim_id, ''), claim, verdict,
			severity, evidence, rebuttal, reviewer_idx, reviewer_model, NULLIF(reviewer_frame_id, ''),
			COALESCE(NULLIF(source_ref, ''), '{}'), status, reflag_count, ` + legacyTime("created_at") + `
			FROM legacy.verification_checks`},
		{"annotations", `INSERT INTO annotations (id, project_id, target_kind, target_key, label_idx, content_checksum, body, created_at, updated_at)
			SELECT id, project_id, target_kind, target_key, label_idx, NULLIF(content_checksum, ''), body,
			` + legacyTime("created_at") + `, ` + legacyNullableTime("updated_at") + ` FROM legacy.annotations`},
		{"notes", `INSERT INTO notes (id, project_id, user_id, target_type, target_frame_id, target_message_index, target_artifact_id, content, created_at, updated_at)
			SELECT id, project_id, 'local', target_type, target_frame_id, target_message_index, NULLIF(target_artifact_id, ''), content,
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.notes`},
		{"memories", `INSERT INTO memories (
			id, user_id, body, subject_project_id, subject_artifact_id, subject_version_id,
			subject_frame_id, source_frame_id, category_id, origin, evidence, superseded_by,
			created_at, updated_at, last_surfaced_at
		) SELECT id, 'local', body, NULLIF(subject_project_id, ''), NULLIF(subject_artifact_id, ''),
			NULLIF(subject_version_id, ''), NULLIF(subject_frame_id, ''), NULLIF(source_frame_id, ''),
			NULLIF(category_id, ''), origin, COALESCE(NULLIF(evidence, ''), 'stated'), NULLIF(superseded_by, ''),
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + `,
			` + legacyNullableTime("last_surfaced_at") + ` FROM legacy.memories`},
		{"routine_schedules", `INSERT INTO routine_schedules (id, root_frame_id, owner_user_id, label, on_tick, every_minutes, enabled, locked_at, paused_reason, next_due, tick_count, missed_ticks, last_fire_at, last_ok_at, idle_streak, last_results, created_at, updated_at)
			SELECT id, root_frame_id, 'local', label, on_tick, every_minutes, enabled,
			` + legacyNullableTime("locked_at") + `, paused_reason, ` + legacyTime("next_due") + `, tick_count, missed_ticks,
			` + legacyNullableTime("last_fire_at") + `, ` + legacyNullableTime("last_ok_at") + `, idle_streak, last_results,
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.routine_schedules`},
		{"frame_read_cursors", `INSERT INTO frame_read_cursors (root_frame_id, message_uuid, message_index, updated_at)
			SELECT root_frame_id, message_uuid, message_index, ` + legacyTime("updated_at") + ` FROM legacy.frame_read_cursors`},
		{"transcript_annotations", `INSERT INTO transcript_annotations (
			id, root_frame_id, message_uuid, message_index, block_index, source, tool_name,
			anchor_text, start_offset, end_offset, kind, origin, read_at, note, created_at, updated_at
		) SELECT id, root_frame_id, NULLIF(message_uuid, ''), message_index, COALESCE(block_index, 0),
			source, NULLIF(tool_name, ''), anchor_text, start_offset, end_offset, kind,
			COALESCE(NULLIF(origin, ''), 'user'), ` + legacyNullableTime("read_at") + `, COALESCE(note, ''),
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.transcript_annotations`},
		{"frame_system_prompts", `INSERT INTO frame_system_prompts (frame_id, hash, updated_at, payload)
			SELECT frame_id, hash, ` + legacyTime("updated_at") + `, payload FROM legacy.frame_system_prompts`},
		{"frame_branch_archives", `INSERT INTO frame_branch_archives (frame_id, branch_id, payload, updated_at)
			SELECT frame_id, branch_id, payload, ` + legacyTime("updated_at") + ` FROM legacy.frame_branch_archives`},
		{"compaction_archives", `INSERT INTO compaction_archives
			(id, frame_id, compaction_index, message_count, token_count, summary, messages, created_at)
			SELECT id, frame_id, compaction_index, message_count, token_count, summary, messages,
			` + legacyTime("created_at") + ` FROM legacy.compaction_archives`},
		{"queued_user_messages", `INSERT INTO queued_user_messages (sequence, frame_id, payload, intent_id, state, resolved_at, created_at)
			SELECT seq, frame_id, payload, intent_id, state, ` + legacyNullableTime("resolved_at") + `, ` + legacyTime("created_at") + `
			FROM legacy.queued_user_messages`},
		{"session_seen_marks", `INSERT INTO session_seen_marks (root_frame_id, seen_token, updated_at)
			SELECT root_frame_id, seen_token, ` + legacyTime("updated_at") + ` FROM legacy.session_seen_marks`},
		{"memory_categories", `INSERT INTO memory_categories (id, user_id, name, name_lower, guidance, auto_recall, created_at, updated_at)
			SELECT id, 'local', name, name_lower, guidance, auto_recall, ` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + `
			FROM legacy.memory_categories`},
		{"use_intent_declarations", `INSERT INTO use_intent_declarations (id, user_id, org_id, intent, created_at)
			SELECT id, 'local', org_id, intent, ` + legacyTime("created_at") + ` FROM legacy.use_intent_declarations`},
		{"contact_email_decisions", `INSERT INTO contact_email_decisions
			(id, user_id, decision, email, notice_version, notice_text, created_at)
			SELECT id, 'local', decision, email, notice_version, notice_text, ` + legacyTime("created_at") + `
			FROM legacy.contact_email_decisions`},
		{"directory_attachments", `INSERT INTO directory_attachments (server_uuid, agent_name, user_id, excluded_tools, created_at)
			SELECT server_uuid, agent_name, 'local', COALESCE(excluded_tools, '[]'), ` + legacyTime("created_at") + ` FROM legacy.directory_attachments`},
		{"user_secrets", `INSERT INTO legacy_user_secrets (id, user_id, name, provider, encrypted_value, credential_type, buckets, region, description, created_at, updated_at)
			SELECT id, user_id, name, provider, encrypted_value, credential_type, buckets, region, description,
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.user_secrets`},
		{"custom_mcp_servers", `INSERT INTO custom_mcp_servers (id, user_id, name, url, transport, enabled, created_at, updated_at)
			SELECT id, 'local', name, url,
			CASE lower(transport) WHEN 'http' THEN 'streamable-http' WHEN 'streamable_http' THEN 'streamable-http' ELSE transport END,
			1, ` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.custom_mcp_servers`},
		{"mcp_agent_assignments", `INSERT INTO mcp_agent_assignments (id, mcp_server_id, user_id, agent_name, created_at)
			SELECT id, mcp_server_id, 'local', agent_name, ` + legacyTime("created_at") + ` FROM legacy.mcp_agent_assignments`},
		{"mcp_tool_grants", `INSERT INTO mcp_tool_grants (id, mcp_server_id, user_id, agent_name, tool_name, enabled, created_at, updated_at)
			SELECT CASE WHEN ranked.rn = 1 THEN ranked.id ELSE ranked.id || ':' || ranked.rn END,
				ranked.server_id, 'local', COALESCE(ranked.agent_name, '*'), ranked.tool_name,
				CASE WHEN lower(ranked.decision) = 'allow' THEN 1 ELSE 0 END, ranked.created_at, ranked.created_at
			FROM (
				SELECT g.*, a.agent_name, ` + legacyTime("g.created_at") + ` AS created_at,
					ROW_NUMBER() OVER (PARTITION BY g.id ORDER BY COALESCE(a.agent_name, '*')) AS rn
				FROM legacy.mcp_tool_grants g
				LEFT JOIN legacy.mcp_agent_assignments a ON a.mcp_server_id = g.server_id AND a.user_id = g.user_id
			) ranked`},
		{"oauth_tokens", `INSERT INTO mcp_oauth_status (mcp_server_id, user_id, access_token_ref, token_type, expires_at, scopes, created_at, updated_at)
			SELECT mcp_server_id, 'local', 'legacy-encrypted:' || id, token_type, ` + legacyNullableTime("expires_at") + `, '[]',
			` + legacyTime("created_at") + `, ` + legacyTime("updated_at") + ` FROM legacy.oauth_tokens`},
		{"compute_providers", `INSERT INTO compute_providers (name, owner_user_id, family, environments, memory_md, scratch_root, scheduler, updated_at)
			SELECT name, '*', family, COALESCE(environments, '[]'), COALESCE(memory_md, ''), COALESCE(scratch_root, ''), COALESCE(scheduler, ''),
			` + legacyTime("COALESCE(probed_at, 0)") + ` FROM legacy.compute_providers`},
		{"compute_usage", `INSERT INTO compute_usage (
			id,job_id,environment,tier_type,provider,frame_id,project_id,started_at,ended_at,expires_at,
			client_uuid,remote_workdir,remote_handle,state,output_specs,submit_cell_id,intent,hardware_details,
			root_frame_id,result,origin_tool_use_id
		) SELECT id,job_id,environment,tier_type,provider,NULLIF(frame_id,''),NULLIF(project_id,''),
			` + legacyTime("started_at") + `, ` + legacyNullableTime("ended_at") + `,
			` + legacyNullableTime("expires_at") + `, NULLIF(client_uuid,''),NULLIF(remote_workdir,''),
			remote_handle,
			CASE lower(COALESCE(state,'pending'))
				WHEN 'pending' THEN 'pending' WHEN 'staging' THEN 'staging' WHEN 'queued' THEN 'queued'
				WHEN 'running' THEN 'running' WHEN 'harvesting' THEN 'harvesting' WHEN 'done' THEN 'done'
				WHEN 'failed' THEN 'failed' WHEN 'timed_out' THEN 'timed_out' WHEN 'orphaned' THEN 'orphaned'
				WHEN 'succeeded' THEN 'done' WHEN 'completed' THEN 'done' WHEN 'error' THEN 'failed'
				WHEN 'cancelled' THEN 'orphaned' WHEN 'terminated' THEN 'orphaned' ELSE 'pending' END,
			output_specs,NULLIF(submit_cell_id,''),intent,hardware_details,NULLIF(root_frame_id,''),
			result,NULLIF(origin_tool_use_id,'') FROM legacy.compute_usage`},
		{"compute_usage", `INSERT OR IGNORE INTO projects (id,user_id,name,path,created_at,updated_at)
			SELECT '__synon_v11_compute_unassigned__','local','Imported unassigned compute jobs','',
			` + legacyTime("MIN(u.started_at)") + `,` + legacyTime("MAX(COALESCE(u.ended_at,u.started_at))") + `
			FROM legacy.compute_usage u
			LEFT JOIN legacy.projects p ON p.id=u.project_id
			WHERE NULLIF(u.project_id,'') IS NULL OR p.id IS NULL
			HAVING COUNT(*)>0`},
		{"compute_usage", `INSERT INTO compute_workbench_jobs (
			job_id,owner_user_id,project_id,frame_id,environment,tier_type,provider,state,started_at,ended_at,
			intent_json,hardware_json,origin_tool_use_id,root_frame_id,provider_family,provider_label,
			external_id,external_url,supports_tail,error_kind,left_on_remote_json,system_hint,
			stdout_text,stderr_text
		) SELECT u.job_id,'local',
			CASE WHEN p.id IS NULL THEN '__synon_v11_compute_unassigned__' ELSE u.project_id END,NULLIF(u.frame_id,''),
			u.environment,u.tier_type,u.provider,
			CASE lower(COALESCE(u.state,'pending'))
				WHEN 'pending' THEN 'pending' WHEN 'staging' THEN 'staging' WHEN 'queued' THEN 'queued'
				WHEN 'running' THEN 'running' WHEN 'harvesting' THEN 'harvesting' WHEN 'done' THEN 'done'
				WHEN 'failed' THEN 'failed' WHEN 'timed_out' THEN 'timed_out' WHEN 'orphaned' THEN 'orphaned'
				WHEN 'succeeded' THEN 'done' WHEN 'completed' THEN 'done' WHEN 'error' THEN 'failed'
				WHEN 'cancelled' THEN 'orphaned' WHEN 'terminated' THEN 'orphaned' ELSE 'pending' END,
			` + legacyTime("u.started_at") + `,` + legacyNullableTime("u.ended_at") + `,
			CASE WHEN json_valid(u.intent) THEN u.intent ELSE json_quote(COALESCE(u.intent,'')) END,
			CASE WHEN json_valid(u.hardware_details) THEN u.hardware_details ELSE json_quote(COALESCE(u.hardware_details,'')) END,
			NULLIF(u.origin_tool_use_id,''),NULLIF(u.root_frame_id,''),COALESCE(cp.family,'unknown'),u.provider,
			CASE WHEN json_valid(u.remote_handle) THEN json_extract(u.remote_handle,'$.sandboxId') END,
			CASE WHEN json_valid(u.remote_handle) THEN COALESCE(json_extract(u.remote_handle,'$.externalUrl'),json_extract(u.remote_handle,'$.url')) END,
			1,
			CASE WHEN json_valid(u.result) THEN json_extract(u.result,'$.error_kind') END,
			CASE WHEN json_valid(u.result) THEN COALESCE(json_extract(u.result,'$.left_on_remote'),'[]') ELSE '[]' END,
			CASE WHEN json_valid(u.result) THEN json_extract(u.result,'$.system_hint') END,'',''
			FROM legacy.compute_usage u
			LEFT JOIN legacy.projects p ON p.id=u.project_id
			LEFT JOIN legacy.compute_providers cp ON cp.name=u.provider`},
		{"compute_pending_terminate", `INSERT INTO compute_pending_terminate (sandbox_id, provider, job_id, enqueued_at, attempts)
			SELECT pending.sandbox_id, pending.provider,
				COALESCE((SELECT usage.job_id FROM legacy.compute_usage usage
					WHERE json_valid(usage.remote_handle)
					AND json_extract(usage.remote_handle, '$.sandboxId') = pending.sandbox_id
					ORDER BY usage.started_at DESC, usage.job_id LIMIT 1), ''),
			pending.enqueued_at, MAX(COALESCE(pending.attempts, 0), 0)
			FROM legacy.compute_pending_terminate pending`},
		{"managed_endpoints", `INSERT INTO compute_managed_endpoints (
			name, owner_user_id, url, port, state, location, skill_name, credential_name, live_path,
			start_script, stop_script, approved_script_hash, last_error, transcript, state_changed_at,
			service_dir, service_dir_bytes, stop_claim_token, stop_claim_generation
		) SELECT name, 'local', url, port,
			CASE lower(COALESCE(state, 'stopped'))
				WHEN 'starting' THEN 'starting' WHEN 'running' THEN 'running' WHEN 'stopping' THEN 'stopping'
				WHEN 'failed' THEN 'failed' ELSE 'stopped' END,
			'local', skill_name, NULLIF(credential_name, ''), live_path, start_script, stop_script,
			approved_script_hash, last_error, transcript,
			` + legacyTime("COALESCE(state_changed_at, created_at)") + `, '', NULL, '', 0
			FROM legacy.managed_endpoints`},
		{"capability_settings", `INSERT INTO skill_preferences (user_id, skill_name, enabled, updated_at)
			SELECT 'local', key, enabled, ` + legacyTime("updated_at") + ` FROM legacy.capability_settings WHERE lower(kind) = 'skill'`},
		{"frame_messages", `INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
			SELECT 'legacy-message:' || frame_id || ':' || idx, frame_id, idx + 1, 'message', msg_json,
			` + legacyTime("COALESCE((SELECT created_at FROM legacy.frames f WHERE f.id = frame_messages.frame_id), 0) + idx") + `
			FROM legacy.frame_messages ORDER BY frame_id, idx`},
		{"events", `INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
			SELECT id, frame_id,
				COALESCE((SELECT COUNT(*) FROM legacy.frame_messages m WHERE m.frame_id = events.frame_id), 0)
				+ ROW_NUMBER() OVER (PARTITION BY frame_id ORDER BY timestamp, id),
				event_type, COALESCE(payload, '{}'), ` + legacyTime("timestamp") + ` FROM legacy.events`},
		{"events", `INSERT OR IGNORE INTO realtime_events (id, user_id, project_id, root_frame_id, frame_id, event_type, event_kind, payload, invalidations, created_at)
			SELECT e.id, 'local', COALESCE(f.project_id, ''), COALESCE(f.root_frame_id, f.id), f.id,
				e.event_type, 'legacy', COALESCE(e.payload, '{}'), '[]', ` + legacyTime("e.timestamp") + `
			FROM legacy.events e JOIN legacy.frames f ON f.id = e.frame_id`},
	}
	for _, statement := range statements {
		if sourceCounts[statement.table] == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement.sql); err != nil {
			return nil, fmt.Errorf("import v1.1 %s: %w", statement.table, err)
		}
	}
	dispositions, err := planTableDispositions(sourceCounts)
	if err != nil {
		return nil, err
	}
	for table, disposition := range dispositions {
		if disposition.Action != DispositionArchive || disposition.SourceCount == 0 {
			continue
		}
		query := `CREATE TABLE ` + quoteIdentifier(disposition.Target) + ` AS SELECT * FROM legacy.` + quoteIdentifier(table)
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return nil, fmt.Errorf("archive v1.1 %s: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit v1.1 workspace import: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DETACH DATABASE legacy`); err != nil {
		return nil, fmt.Errorf("detach v1.1 snapshot: %w", err)
	}

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, err
	}
	if rows.Next() {
		var table string
		var rowID int64
		var parent string
		var fkID int
		_ = rows.Scan(&table, &rowID, &parent, &fkID)
		rows.Close()
		return nil, fmt.Errorf("migrated workspace foreign-key violation: table=%s row=%d parent=%s fk=%d", table, rowID, parent, fkID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	imported := map[string]int64{}
	for _, table := range []string{
		"projects", "frames", "user_agents", "artifact_folders", "artifacts", "artifact_versions",
		"annotations", "notes", "memories", "routine_schedules", "custom_mcp_servers",
		"mcp_agent_assignments", "mcp_tool_grants", "compute_providers", "compute_usage", "compute_pending_terminate",
		"artifact_version_dependencies", "content_snapshots", "execution_log", "frame_read_cursors", "transcript_annotations",
		"frame_system_prompts", "frame_branch_archives", "compaction_archives", "queued_user_messages", "session_seen_marks",
		"memory_categories", "use_intent_declarations", "contact_email_decisions", "directory_attachments", "legacy_user_secrets",
		"session_claims", "verification_checks", "compute_managed_endpoints",
	} {
		count, err := countTable(ctx, db, table)
		if err != nil {
			return nil, err
		}
		imported[table] = count
	}
	for source, target := range map[string]string{
		"artifact_dependencies": "artifact_version_dependencies",
		"user_secrets":          "legacy_user_secrets",
		"managed_endpoints":     "compute_managed_endpoints",
	} {
		if _, ok := sourceCounts[source]; ok {
			imported[source] = imported[target]
		}
	}
	for _, table := range []string{"frame_messages", "custom_agent_prompts", "oauth_tokens", "events", "capability_settings"} {
		if count, ok := sourceCounts[table]; ok {
			imported[table] = count
		}
	}
	return imported, nil
}

func legacyTime(expression string) string {
	return `strftime('%Y-%m-%dT%H:%M:%fZ', (` + expression + `) / 1000.0, 'unixepoch')`
}

func legacyNullableTime(expression string) string {
	return `CASE WHEN ` + expression + ` IS NULL THEN NULL ELSE ` + legacyTime(expression) + ` END`
}

func migratedBlobPath(versionID string) string {
	prefix := versionID
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.ToSlash(filepath.Join("artifact-versions", prefix, versionID+".blob"))
}
