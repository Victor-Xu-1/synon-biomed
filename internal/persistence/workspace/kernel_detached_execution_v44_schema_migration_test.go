package workspace

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestKernelDetachedExecutionV44PublishedIdentity(t *testing.T) {
	checksum, err := kernelDetachedExecutionV44Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "dac48ba4feaf621942ef6aedeb76693a280d46309c498519cbe1fb1fc980dcb3"
	if checksum != published {
		t.Fatalf("published v44 checksum changed: got %s want %s", checksum, published)
	}
}

func TestKernelDetachedExecutionV44CreatesStrictFencedAuthority(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "detached-v44", "call-detached-v44")
	second := createKernelLocalOperationForTest(t, store, repo, claim, "detached-v44-second", "call-detached-v44-second")

	status, err := store.SchemaStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != workspaceSchemaVersion || status.TargetVersion != workspaceSchemaVersion ||
		len(status.Migrations) != workspaceSchemaVersion {
		t.Fatalf("schema status=%#v", status)
	}
	var v51 SchemaMigrationRecord
	for _, migration := range status.Migrations {
		if migration.Version == 51 {
			v51 = migration
			break
		}
	}
	if v51.Version != 51 || v51.Name != "kernel-detached-execution-backend-recreate" || v51.Checksum == "" {
		t.Fatalf("v51 migration=%#v", v51)
	}

	for _, object := range []string{
		"kernel_execution_backends", "kernel_detached_executions",
		"kernel_execution_result_receipts", "kernel_execution_host_calls",
		"kernel_detached_executions_one_active_per_backend",
		"kernel_execution_result_receipts_immutable",
	} {
		var found int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&found); err != nil || found != 1 {
			t.Fatalf("schema object %s count=%d err=%v", object, found, err)
		}
	}
	for _, table := range []string{
		"kernel_execution_backends", "kernel_detached_executions",
		"kernel_execution_result_receipts", "kernel_execution_host_calls",
	} {
		var ddl string
		if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "STRICT") {
			t.Fatalf("table %s is not strict: %s", table, ddl)
		}
	}

	now := time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	sessionOperation := operation
	sessionOperation.KernelID = "kernel-v44"
	sessionOperation.Environment = "python"
	_, sessionSpecJSON, sessionSpecSHA256, err := canonicalKernelExecutionSessionSpec(
		kernelExecutionSessionSpecForTest(sessionOperation),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO kernel_execution_backends(
		backend_id,owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,
		frame_id,frame_incarnation_id,kernel_id,kernel_generation,session_spec_json,session_spec_sha256,protocol_version,
		executor_instance_id,machine_boot_id,executor_pid,executor_pid_start_ticks,
		worker_pid,worker_pid_start_ticks,worker_pgid,socket_path,backend_generation,
		state,state_version,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"backend-v44", operation.OwnerUserID, operation.ProjectID, operation.RootFrameID,
		operation.RootFrameIncarnationID, operation.FrameID, operation.FrameIncarnationID,
		"kernel-v44", 1, sessionSpecJSON, sessionSpecSHA256, 1,
		"executor-v44", "machine-boot-v44", 101, 1001,
		102, 1002, 102, "/run/user/1000/synon-biomed/backend-v44.sock", 1,
		"ready", 1, now, now,
	); err != nil {
		t.Fatal(err)
	}
	insertExecution := func(executionID string, current KernelLocalOperation) error {
		requestJSON := `{"code":"pass"}`
		requestSHA256 := sha256HexString(requestJSON)
		_, insertErr := store.db.Exec(`INSERT INTO kernel_detached_executions(
			execution_id,operation_id,backend_id,backend_generation,request_json,request_sha256,
			confinement_sha256,state,state_version,accepted_at,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, executionID, current.OperationID, "backend-v44", 1,
			requestJSON, requestSHA256, strings.Repeat("b", 64), "accepted", 1, now, now, now)
		return insertErr
	}
	if err := insertExecution("execution-v44", operation); err != nil {
		t.Fatal(err)
	}
	if err := insertExecution("execution-v44-second", second); err == nil {
		t.Fatal("database accepted two active executions for one backend")
	}

	if _, err := store.db.Exec(`INSERT INTO kernel_execution_host_calls(
		execution_id,host_call_id,ordinal,method,request_json,request_sha256,
		state,state_version,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?)`, "execution-v44", "host-call-v44", 0, "artifact.read",
		`{"artifact_id":"artifact-v44"}`, strings.Repeat("c", 64), "pending", 1, now, now); err != nil {
		t.Fatal(err)
	}
	lease := time.Date(2026, 8, 3, 3, 1, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE kernel_execution_host_calls SET state='executing',state_version=2,
		claim_epoch=1,claim_token_sha256=?,claim_expires_at=?,updated_at=?
		WHERE execution_id='execution-v44' AND host_call_id='host-call-v44'`,
		make([]byte, 32), lease, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE kernel_execution_host_calls SET state='completed',state_version=3,
		result_json='{"ok":true}',result_sha256=?,terminal_at=?,updated_at=?
		WHERE execution_id='execution-v44' AND host_call_id='host-call-v44'`,
		strings.Repeat("d", 64), now, now); err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(`INSERT INTO kernel_execution_result_receipts(
		receipt_id,execution_id,operation_id,backend_id,backend_generation,terminal_sequence,
		outcome,result_json,result_sha256,interrupted,timed_out,started_at,finished_at,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "receipt-v44", "execution-v44", operation.OperationID,
		"backend-v44", 1, 1, "completed", `{"ok":true}`, strings.Repeat("e", 64),
		0, 0, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE kernel_detached_executions SET state='terminal',state_version=2,
		terminal_receipt_id='receipt-v44',updated_at=? WHERE execution_id='execution-v44'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE kernel_execution_result_receipts SET result_sha256=? WHERE receipt_id='receipt-v44'`,
		strings.Repeat("f", 64)); err == nil || !strings.Contains(err.Error(), "receipt is immutable") {
		t.Fatalf("receipt update error=%v", err)
	}
	if _, err := store.db.Exec(`DELETE FROM kernel_execution_result_receipts WHERE receipt_id='receipt-v44'`); err == nil ||
		!strings.Contains(err.Error(), "append-only") {
		t.Fatalf("receipt delete error=%v", err)
	}
	if _, err := store.db.Exec(`UPDATE kernel_detached_executions SET state='started',state_version=3,
		terminal_receipt_id=NULL,updated_at=? WHERE execution_id='execution-v44'`, now); err == nil {
		t.Fatal("database accepted a terminal execution restart")
	}
}

func TestKernelDetachedExecutionV44PreflightRejectsPollutedCohort(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, table := range []string{
		"projects", "frames", "kernel_local_operations", "kernel_local_operation_materializations",
	} {
		if _, err := db.Exec(`CREATE TABLE ` + table + `(id TEXT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE kernel_execution_backends(backend_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := preflightKernelDetachedExecutionV44(context.Background(), db); err == nil ||
		!strings.Contains(err.Error(), "cohort is polluted") {
		t.Fatalf("preflight error=%v", err)
	}
}
