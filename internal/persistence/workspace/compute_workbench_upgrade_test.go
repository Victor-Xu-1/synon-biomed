package workspace

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestComputeWorkbenchSchemaUpgradesLegacyPartialTablesBeforeIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE compute_workbench_jobs (
			job_id TEXT PRIMARY KEY,
			provider TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'pending',
			external_id TEXT
		)`,
		`INSERT INTO compute_workbench_jobs(job_id,provider,state)
			VALUES('legacy-job','legacy-provider','running')`,
		`CREATE TABLE compute_managed_endpoints (
			name TEXT PRIMARY KEY,
			url TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'stopped'
		)`,
		`INSERT INTO compute_managed_endpoints(name,url,port,state)
			VALUES('legacy-endpoint','http://127.0.0.1:9000',9000,'live')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade partial compute schema: %v", err)
	}
	defer store.Close()
	for table, required := range map[string][]string{
		"compute_workbench_jobs": {
			"owner_user_id", "project_id", "frame_id", "environment", "tier_type",
			"started_at", "ended_at", "intent_json", "hardware_json", "origin_tool_use_id",
			"root_frame_id", "provider_family", "provider_label", "external_url",
			"supports_tail", "error_kind", "left_on_remote_json", "system_hint",
			"stdout_text", "stderr_text",
		},
		"compute_managed_endpoints": {
			"owner_user_id", "location", "skill_name", "credential_name", "live_path",
			"start_script", "stop_script", "approved_script_hash", "last_error",
			"transcript", "state_changed_at", "service_dir", "service_dir_bytes",
		},
	} {
		found := tableColumns(t, store.db, table)
		for _, column := range required {
			if !found[column] {
				t.Fatalf("%s missing upgraded column %s; columns=%v", table, column, found)
			}
		}
	}
	var owner, projectID, environment, family, label, left, stdout, stderr string
	if err := store.db.QueryRow(`SELECT owner_user_id,project_id,environment,provider_family,
		provider_label,left_on_remote_json,stdout_text,stderr_text
		FROM compute_workbench_jobs WHERE job_id='legacy-job'`).Scan(
		&owner, &projectID, &environment, &family, &label, &left, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if owner != "local" || projectID != "" || environment != "" || family != "unknown" ||
		label != "" || left != "[]" || stdout != "" || stderr != "" {
		t.Fatalf("upgraded defaults owner=%q project=%q env=%q family=%q label=%q left=%q stdout=%q stderr=%q",
			owner, projectID, environment, family, label, left, stdout, stderr)
	}
	var indexCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND name IN (
			'compute_workbench_jobs_owner_project_state_idx',
			'compute_workbench_jobs_owner_provider_active_external_idx'
		)`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 2 {
		t.Fatalf("compute workbench indexes=%d", indexCount)
	}
}

func tableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(
			&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey,
		); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}
