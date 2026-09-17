package v11

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"

	_ "modernc.org/sqlite"
)

func TestImportComputeUsagePreservesAllV11StatesAndMetadata(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "legacy.db")
	targetPath := filepath.Join(root, "target.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE projects (
			id TEXT PRIMARY KEY,user_id TEXT,name TEXT,created_at INTEGER,updated_at INTEGER
		)`,
		`INSERT INTO projects VALUES ('project-1','owner-a','Compute Project',1700000000000,1700000001000)`,
		`CREATE TABLE compute_providers (
			name TEXT PRIMARY KEY, family TEXT NOT NULL, memory_md TEXT NOT NULL, environments TEXT NOT NULL,
			memory_rev INTEGER NOT NULL, scratch_root TEXT, scheduler TEXT, probed_at INTEGER
		)`,
		`CREATE TABLE compute_usage (
			id TEXT PRIMARY KEY, job_id TEXT NOT NULL, environment TEXT NOT NULL, tier_type TEXT NOT NULL,
			provider TEXT NOT NULL, frame_id TEXT, project_id TEXT, started_at INTEGER NOT NULL,
			ended_at INTEGER, expires_at INTEGER, client_uuid TEXT, remote_workdir TEXT, remote_handle TEXT,
			state TEXT NOT NULL, output_specs TEXT, submit_cell_id TEXT, intent TEXT, hardware_details TEXT,
			root_frame_id TEXT, result TEXT, origin_tool_use_id TEXT
		)`,
		`INSERT INTO compute_providers VALUES ('modal','byoc','','["python"]',1,'/tmp','modal',1700000000000)`,
	}
	for _, statement := range statements {
		if _, err := source.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	states := []string{"pending", "staging", "queued", "running", "harvesting", "done", "failed", "timed_out", "orphaned"}
	for index, state := range states {
		ended := any(nil)
		if index >= 5 {
			ended = int64(1700000100000 + index)
		}
		_, err := source.Exec(`INSERT INTO compute_usage (
			id,job_id,environment,tier_type,provider,frame_id,project_id,started_at,ended_at,expires_at,
			client_uuid,remote_workdir,remote_handle,state,output_specs,submit_cell_id,intent,hardware_details,
			root_frame_id,result,origin_tool_use_id
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"usage-"+state, "job-"+state, "python", "gpu", "modal", "frame-"+state, "project-1",
			int64(1700000000000+index), ended, int64(1700003600000+index), "client-1", "/remote/"+state,
			`{"sandboxId":"sb-`+state+`"}`, state, `[{"path":"result.csv"}]`, "cell-1",
			"intent-"+state, "a100", "root-1", `{"status":"`+state+`"}`, "tool-1")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.Exec(`INSERT INTO compute_usage (
		id,job_id,environment,tier_type,provider,project_id,started_at,ended_at,
		remote_handle,state,result
	) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"usage-reused-sandbox", "job-reused-sandbox", "python", "gpu", "modal", "project-1",
		int64(1700000200000), int64(1700000300000), `{"sandboxId":"sb-done"}`, "done",
		`{"status":"done"}`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, project string }{
		{"no-project", ""},
		{"dangling-project", "missing-project"},
	} {
		if _, err := source.Exec(`INSERT INTO compute_usage (
			id,job_id,environment,tier_type,provider,project_id,started_at,state,remote_handle
		) VALUES (?,?,?,?,?,?,?,?,?)`,
			"usage-"+row.id, "job-"+row.id, "python", "gpu", "modal", row.project,
			int64(1700000400000), "done", `{"sandboxId":"sb-`+row.id+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	target, err := workspace.Open(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	imported, err := importWorkspace(context.Background(), sourcePath, targetPath, map[string]int64{
		"projects":          1,
		"compute_providers": 1,
		"compute_usage":     int64(len(states) + 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported["compute_usage"] != int64(len(states)+3) {
		t.Fatalf("imported=%#v", imported)
	}
	db, err := sql.Open("sqlite", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, state := range states {
		var gotState, frameID, projectID, clientUUID, remoteWorkdir, rootFrameID, originToolUseID string
		var remoteHandle, outputSpecs, result string
		var expiresAt any
		err := db.QueryRow(`SELECT state,frame_id,project_id,client_uuid,remote_workdir,remote_handle,
			output_specs,root_frame_id,result,origin_tool_use_id,expires_at
			FROM compute_usage WHERE id=?`, "usage-"+state).Scan(
			&gotState, &frameID, &projectID, &clientUUID, &remoteWorkdir, &remoteHandle,
			&outputSpecs, &rootFrameID, &result, &originToolUseID, &expiresAt)
		if err != nil {
			t.Fatal(err)
		}
		if gotState != state || frameID != "frame-"+state || projectID != "project-1" ||
			clientUUID != "client-1" || remoteWorkdir != "/remote/"+state ||
			remoteHandle != `{"sandboxId":"sb-`+state+`"}` || outputSpecs != `[{"path":"result.csv"}]` ||
			rootFrameID != "root-1" || result != `{"status":"`+state+`"}` ||
			originToolUseID != "tool-1" || expiresAt == nil {
			t.Fatalf("%s row mismatch: state=%s frame=%s project=%s client=%s workdir=%s handle=%s specs=%s root=%s result=%s tool=%s expires=%v",
				state, gotState, frameID, projectID, clientUUID, remoteWorkdir, remoteHandle, outputSpecs,
				rootFrameID, result, originToolUseID, expiresAt)
		}
		var projectedState, ownerID, externalID string
		err = db.QueryRow(`SELECT state,owner_user_id,external_id
			FROM compute_workbench_jobs WHERE job_id=?`, "job-"+state).Scan(
			&projectedState, &ownerID, &externalID)
		if err != nil {
			t.Fatal(err)
		}
		if projectedState != state || ownerID != "local" || externalID != "sb-"+state {
			t.Fatalf("%s projection state=%s owner=%s external=%s", state, projectedState, ownerID, externalID)
		}
	}
	if count := queryCount(t, db, `SELECT COUNT(*) FROM compute_usage WHERE state IN ('staging','queued','running','harvesting') AND remote_handle IS NOT NULL`); count != 4 {
		t.Fatalf("active count=%d", count)
	}
	if count := queryCount(t, db, `SELECT COUNT(*) FROM compute_workbench_jobs WHERE external_id='sb-done' AND state='done'`); count != 2 {
		t.Fatalf("terminal warm-sandbox projection count=%d", count)
	}
	if count := queryCount(t, db, `SELECT COUNT(*) FROM compute_workbench_jobs
		WHERE project_id='__synon_v11_compute_unassigned__'
			AND job_id IN ('job-no-project','job-dangling-project')`); count != 2 {
		t.Fatalf("unassigned compute projections=%d", count)
	}
	if count := queryCount(t, db, `SELECT COUNT(*) FROM projects
		WHERE id='__synon_v11_compute_unassigned__' AND user_id='local'`); count != 1 {
		t.Fatalf("unassigned compute project count=%d", count)
	}
	if count := queryCount(t, db, `SELECT COUNT(*) FROM compute_usage
		WHERE (id='usage-no-project' AND COALESCE(project_id,'')='')
			OR (id='usage-dangling-project' AND project_id='missing-project')`); count != 2 {
		t.Fatalf("authoritative unassigned project preservation=%d", count)
	}
	if count := queryCount(t, db, `SELECT COUNT(*) FROM compute_providers
		WHERE name='modal' AND owner_user_id='*'`); count != 1 {
		t.Fatalf("shared migrated provider count=%d", count)
	}
}

func TestImportComputeUsageRejectsDuplicateJobIDsBeforeWriting(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "legacy.db")
	targetPath := filepath.Join(root, "target.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Exec(`CREATE TABLE compute_usage (
		id TEXT PRIMARY KEY, job_id TEXT NOT NULL, environment TEXT NOT NULL, tier_type TEXT NOT NULL,
		provider TEXT NOT NULL, frame_id TEXT, project_id TEXT, started_at INTEGER NOT NULL,
		ended_at INTEGER, expires_at INTEGER, client_uuid TEXT, remote_workdir TEXT, remote_handle TEXT,
		state TEXT NOT NULL, output_specs TEXT, submit_cell_id TEXT, intent TEXT, hardware_details TEXT,
		root_frame_id TEXT, result TEXT, origin_tool_use_id TEXT
	);
	INSERT INTO compute_usage(id,job_id,environment,tier_type,provider,started_at,state)
	VALUES ('usage-a','duplicate-job','python','cpu','local',1,'done'),
		('usage-b','duplicate-job','python','cpu','local',2,'running')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	target, err := workspace.Open(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = importWorkspace(context.Background(), sourcePath, targetPath, map[string]int64{"compute_usage": 2})
	if err == nil || !strings.Contains(err.Error(), `duplicate job_id "duplicate-job" appears 2 times`) {
		t.Fatalf("duplicate job validation error = %v", err)
	}
	db, err := sql.Open("sqlite", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if count := queryCount(t, db, `SELECT COUNT(*) FROM compute_usage`); count != 0 {
		t.Fatalf("partially imported compute usage rows=%d", count)
	}
}

func queryCount(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(fmt.Errorf("query count: %w", err))
	}
	return count
}
