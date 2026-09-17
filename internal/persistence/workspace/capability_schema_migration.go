package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func (s *Store) ensureRoutineHostDoneSchema(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(routine_schedules)`)
	if err != nil {
		return fmt.Errorf("inspect routine host done columns: %w", err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan routine host done column: %w", err)
		}
		found[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close routine host done column scan: %w", err)
	}
	columns := []struct{ name, definition string }{
		{"claim_generation", "INTEGER NOT NULL DEFAULT 0"},
		{"claim_token", "TEXT"},
		{"host_done_locked_at", "TIMESTAMP"},
		{"host_done_claim_generation", "INTEGER"},
		{"host_done_claim_token", "TEXT"},
		{"host_had_work", "INTEGER"},
		{"host_done_summary", "TEXT"},
		{"host_done_at", "TIMESTAMP"},
		{"completion_claim_generation", "INTEGER"},
		{"completion_claim_token", "TEXT"},
		{"completion_successful", "INTEGER"},
		{"completion_at", "TIMESTAMP"},
		{"completion_result_hash", "TEXT"},
	}
	for _, column := range columns {
		if found[column.name] {
			continue
		}
		if _, err := executor.ExecContext(ctx, `ALTER TABLE routine_schedules ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return fmt.Errorf("add routine host done column %s: %w", column.name, err)
		}
	}
	return nil
}

func (s *Store) ensureComputeProviderSchema(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(compute_providers)`)
	if err != nil {
		return fmt.Errorf("inspect compute provider columns: %w", err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan compute provider column: %w", err)
		}
		found[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close compute provider column scan: %w", err)
	}
	columns := []struct{ name, definition string }{
		{"owner_user_id", "TEXT NOT NULL DEFAULT 'local'"},
		{"endpoint", "TEXT NOT NULL DEFAULT ''"},
		{"skill_name", "TEXT NOT NULL DEFAULT ''"},
		{"credential_name", "TEXT NOT NULL DEFAULT ''"},
		{"hosted", "INTEGER NOT NULL DEFAULT 0"},
		{"details_md", "TEXT NOT NULL DEFAULT ''"},
		{"details_rev", "INTEGER NOT NULL DEFAULT 0"},
		{"probed_at", "TIMESTAMP"},
		{"data_roots", "TEXT NOT NULL DEFAULT '[]'"},
		{"ssh_overrides", "TEXT NOT NULL DEFAULT '{}'"},
		{"max_concurrent_jobs", "INTEGER"},
		{"max_timeout_sec", "INTEGER"},
		{"enabled", "INTEGER NOT NULL DEFAULT 0"},
		{"app_name", "TEXT"},
		{"prior_app_names", "TEXT NOT NULL DEFAULT '[]'"},
		{"egress_policy", "TEXT"},
		{"modal_environment", "TEXT"},
	}
	for _, column := range columns {
		if found[column.name] {
			continue
		}
		if _, err := executor.ExecContext(ctx, `ALTER TABLE compute_providers ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return fmt.Errorf("add compute provider column %s: %w", column.name, err)
		}
	}
	return nil
}

func (s *Store) ensureComputeUsageSchema(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(compute_usage)`)
	if err != nil {
		return fmt.Errorf("inspect compute usage columns: %w", err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan compute usage column: %w", err)
		}
		found[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close compute usage column scan: %w", err)
	}
	columns := []struct{ name, definition string }{
		{"frame_id", "TEXT"},
		{"project_id", "TEXT"},
		{"expires_at", "TIMESTAMP"},
		{"client_uuid", "TEXT"},
		{"remote_workdir", "TEXT"},
		{"remote_handle", "TEXT"},
		{"output_specs", "TEXT"},
		{"submit_cell_id", "TEXT"},
		{"intent", "TEXT"},
		{"hardware_details", "TEXT"},
		{"root_frame_id", "TEXT"},
		{"result", "TEXT"},
		{"origin_tool_use_id", "TEXT"},
	}
	for _, column := range columns {
		if found[column.name] {
			continue
		}
		if _, err := executor.ExecContext(ctx, `ALTER TABLE compute_usage ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return fmt.Errorf("add compute usage column %s: %w", column.name, err)
		}
	}
	if _, err := executor.ExecContext(ctx, `DROP INDEX IF EXISTS compute_usage_state_idx`); err != nil {
		return fmt.Errorf("drop obsolete compute usage index: %w", err)
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS ix_compute_usage_ended_at ON compute_usage (ended_at)`,
		`CREATE INDEX IF NOT EXISTS ix_compute_usage_started_at ON compute_usage (started_at)`,
		`CREATE INDEX IF NOT EXISTS ix_compute_usage_expires_at ON compute_usage (expires_at)`,
		`CREATE INDEX IF NOT EXISTS ix_compute_usage_state ON compute_usage (state)`,
		`CREATE INDEX IF NOT EXISTS ix_compute_usage_poller_active ON compute_usage (provider,started_at,id)
			WHERE state IN ('staging','queued','running','harvesting') AND remote_handle IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS ix_compute_usage_frame_id ON compute_usage (frame_id)`,
		`CREATE INDEX IF NOT EXISTS ix_compute_usage_root_frame_id ON compute_usage (root_frame_id)`,
		`CREATE INDEX IF NOT EXISTS ix_compute_usage_root_open ON compute_usage (root_frame_id) WHERE ended_at IS NULL`,
	} {
		if _, err := executor.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create compute usage index: %w", err)
		}
	}
	return nil
}

func (s *Store) ensureComputePendingTerminationSchema(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(compute_pending_terminate)`)
	if err != nil {
		return fmt.Errorf("inspect compute pending termination columns: %w", err)
	}
	foundJobID := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "job_id" {
			foundJobID = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !foundJobID {
		if _, err := executor.ExecContext(ctx,
			`ALTER TABLE compute_pending_terminate ADD COLUMN job_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add compute pending termination job id: %w", err)
		}
	}
	if _, err := executor.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS ix_compute_pending_terminate_scan
		ON compute_pending_terminate(enqueued_at,sandbox_id)`); err != nil {
		return fmt.Errorf("create compute pending termination scan index: %w", err)
	}
	return nil
}

func normalizeAgentTags(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (s *Store) ensureAgentProfileSchema(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(user_agents)`)
	if err != nil {
		return fmt.Errorf("inspect agent profile columns: %w", err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan agent profile column: %w", err)
		}
		found[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close agent profile column scan: %w", err)
	}
	columns := []struct{ name, definition string }{
		{"icon_key", "TEXT NOT NULL DEFAULT ''"},
		{"color_key", "TEXT NOT NULL DEFAULT ''"},
		{"tags", "TEXT NOT NULL DEFAULT '[]'"},
		{"skill_tombstones", "TEXT NOT NULL DEFAULT '[]'"},
		{"connector_tombstones", "TEXT NOT NULL DEFAULT '[]'"},
		{"unrestricted", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		if found[column.name] {
			continue
		}
		if _, err := executor.ExecContext(ctx, `ALTER TABLE user_agents ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return fmt.Errorf("add agent profile column %s: %w", column.name, err)
		}
	}
	var promptSchema string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'agent_custom_prompts'`).Scan(&promptSchema); err != nil {
		return fmt.Errorf("inspect custom prompt schema: %w", err)
	}
	if !strings.Contains(strings.ToLower(promptSchema), "references user_agents") {
		return nil
	}
	tx, err := executor.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin custom prompt schema upgrade: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE agent_custom_prompts_without_agent_fk (
			user_id TEXT NOT NULL, agent_name TEXT NOT NULL, prompt_text TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, agent_name))`,
		`INSERT INTO agent_custom_prompts_without_agent_fk SELECT user_id, agent_name, prompt_text, created_at, updated_at FROM agent_custom_prompts`,
		`DROP TABLE agent_custom_prompts`,
		`ALTER TABLE agent_custom_prompts_without_agent_fk RENAME TO agent_custom_prompts`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("upgrade custom prompt schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit custom prompt schema upgrade: %w", err)
	}
	return nil
}

func (s *Store) ensureMCPServerSchema(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(custom_mcp_servers)`)
	if err != nil {
		return fmt.Errorf("inspect custom MCP server schema: %w", err)
	}
	existing := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan custom MCP server schema: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close custom MCP server schema rows: %w", err)
	}
	columns := []struct {
		name       string
		definition string
	}{
		{"description", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_server_url", "TEXT NOT NULL DEFAULT ''"},
		{"client_id", "TEXT NOT NULL DEFAULT ''"},
		{"scopes", "TEXT NOT NULL DEFAULT ''"},
		{"headers_helper", "TEXT NOT NULL DEFAULT ''"},
		{"config_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"builtin", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range columns {
		if existing[column.name] {
			continue
		}
		if _, err := executor.ExecContext(ctx, `ALTER TABLE custom_mcp_servers ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return fmt.Errorf("add custom MCP server %s column: %w", column.name, err)
		}
	}
	return nil
}

func (s *Store) ensureModelProviderDuplicateNames(ctx context.Context, executor schemaMigrationExecutor) error {
	var schema string
	if err := executor.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'model_providers'`).Scan(&schema); err != nil {
		return fmt.Errorf("inspect model provider schema: %w", err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(schema), " "))
	if !strings.Contains(normalized, "unique (user_id, name)") && !strings.Contains(normalized, "unique(user_id, name)") {
		return nil
	}
	tx, err := executor.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin model provider schema upgrade: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE model_providers_without_name_unique (
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
		`INSERT INTO model_providers_without_name_unique
			SELECT id, user_id, name, type, base_url, model, secret_ref, enabled, created_at, updated_at FROM model_providers`,
		`DROP TABLE model_providers`,
		`ALTER TABLE model_providers_without_name_unique RENAME TO model_providers`,
		`CREATE INDEX model_providers_user_updated_idx ON model_providers (user_id, updated_at DESC, id DESC)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("upgrade model provider schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model provider schema upgrade: %w", err)
	}
	return nil
}

func (s *Store) ensureProjectUserColumn(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(projects)`)
	if err != nil {
		return fmt.Errorf("inspect project columns: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan project column: %w", err)
		}
		if name == "user_id" {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close project column scan: %w", err)
	}
	if found {
		return nil
	}
	if _, err := executor.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN user_id TEXT NOT NULL DEFAULT 'local'`); err != nil {
		return fmt.Errorf("add project user column: %w", err)
	}
	return nil
}

func (s *Store) ProjectOwnedBy(projectID, userID string) (bool, error) {
	db := s.readDatabase()
	if db == nil {
		return false, errors.New("workspace store is closed")
	}
	projectID, userID = strings.TrimSpace(projectID), strings.TrimSpace(userID)
	if projectID == "" || userID == "" {
		return false, nil
	}
	var found string
	err := db.QueryRowContext(context.Background(), `SELECT id FROM projects WHERE id = ? AND user_id = ?`, projectID, userID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check project ownership: %w", err)
	}
	return true, nil
}

func (s *Store) ProjectOwnerID(projectID string) (string, bool, error) {
	return s.ProjectOwnerIDContext(context.Background(), projectID)
}

func (s *Store) ensureArtifactFolderColumn(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(artifacts)`)
	if err != nil {
		return fmt.Errorf("inspect artifact columns: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan artifact column: %w", err)
		}
		if name == "folder_id" {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close artifact column scan: %w", err)
	}
	if found {
		return nil
	}
	if _, err := executor.ExecContext(ctx, `
		ALTER TABLE artifacts ADD COLUMN folder_id TEXT
		REFERENCES artifact_folders(id) ON DELETE SET NULL`); err != nil {
		return fmt.Errorf("add artifact folder column: %w", err)
	}
	return nil
}

func (s *Store) ensureArtifactPriorityColumn(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(artifacts)`)
	if err != nil {
		return fmt.Errorf("inspect artifact priority column: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan artifact priority column: %w", err)
		}
		if name == "priority" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate artifact priority columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close artifact priority column scan: %w", err)
	}
	if found {
		return nil
	}
	if _, err := executor.ExecContext(ctx, `ALTER TABLE artifacts ADD COLUMN priority INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add artifact priority column: %w", err)
	}
	return nil
}

func (s *Store) ensureArtifactVersionStorageColumns(ctx context.Context, executor schemaMigrationExecutor) error {
	rows, err := executor.QueryContext(ctx, `PRAGMA table_info(artifact_versions)`)
	if err != nil {
		return fmt.Errorf("inspect artifact version storage columns: %w", err)
	}
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan artifact version storage column: %w", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate artifact version storage columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close artifact version storage column scan: %w", err)
	}
	if !found["storage_path"] {
		if _, err := executor.ExecContext(ctx, `ALTER TABLE artifact_versions ADD COLUMN storage_path TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add artifact version storage path: %w", err)
		}
	}
	if !found["size_bytes"] {
		if _, err := executor.ExecContext(ctx, `ALTER TABLE artifact_versions ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add artifact version size: %w", err)
		}
	}
	if !found["content_available"] {
		if _, err := executor.ExecContext(ctx, `ALTER TABLE artifact_versions ADD COLUMN content_available INTEGER NOT NULL DEFAULT 1 CHECK(content_available IN (0,1))`); err != nil {
			return fmt.Errorf("add artifact version content availability: %w", err)
		}
	}
	artifactRows, err := executor.QueryContext(ctx, `PRAGMA table_info(artifacts)`)
	if err != nil {
		return fmt.Errorf("inspect artifact retention column: %w", err)
	}
	hasRetention := false
	for artifactRows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := artifactRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = artifactRows.Close()
			return fmt.Errorf("scan artifact retention column: %w", err)
		}
		hasRetention = hasRetention || name == "retention_mode"
	}
	if err := artifactRows.Err(); err != nil {
		_ = artifactRows.Close()
		return fmt.Errorf("iterate artifact retention columns: %w", err)
	}
	if err := artifactRows.Close(); err != nil {
		return fmt.Errorf("close artifact retention column scan: %w", err)
	}
	if !hasRetention {
		if _, err := executor.ExecContext(ctx, `ALTER TABLE artifacts ADD COLUMN retention_mode TEXT NOT NULL DEFAULT 'snapshot' CHECK(retention_mode IN ('snapshot','working_data'))`); err != nil {
			return fmt.Errorf("add artifact retention mode: %w", err)
		}
	}
	if _, err := executor.ExecContext(ctx, `
		UPDATE artifact_versions SET size_bytes = length(content)
		WHERE storage_path = '' AND size_bytes = 0 AND length(content) > 0`); err != nil {
		return fmt.Errorf("backfill artifact version sizes: %w", err)
	}
	return nil
}
