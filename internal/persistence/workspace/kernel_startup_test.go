package workspace

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func unstartedBackendV68Fixture(t *testing.T) (*Store, KernelExecutionBackend, DetachedKernelExecution) {
	t.Helper()
	store, backend, execution := providerCancellationV67Fixture(t)
	ctx := context.Background()
	if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 68); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishKernelExecutionBackend(ctx, FinishKernelExecutionBackendInput{BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID}); err != nil {
		t.Fatal(err)
	}
	spec, err := DecodeKernelExecutionSessionSpecV1(backend.SessionSpecJSON)
	if err != nil {
		t.Fatal(err)
	}
	backend, err = store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{BackendID: backend.BackendID, ExecutorInstanceID: "unstarted-executor", MachineBootID: backend.MachineBootID, SocketPath: backend.SocketPath, KernelID: backend.KernelID, KernelGeneration: backend.KernelGeneration, SessionSpec: spec})
	if err != nil {
		t.Fatal(err)
	}
	return store, backend, execution
}

func TestKernelStartupV69PreservesGraphAndRequiresFailureEvidence(t *testing.T) {
	ctx := context.Background()
	store, before, execution := unstartedBackendV68Fixture(t)
	// This is the production failure: the old schema cannot settle a launch
	// failure without fabricating a PID.
	if _, err := store.FinishKernelExecutionBackend(ctx, FinishKernelExecutionBackendInput{BackendID: before.BackendID, BackendGeneration: before.BackendGeneration, ExecutorInstanceID: before.ExecutorInstanceID}); err == nil {
		t.Fatal("v68 accepted an unstarted terminal backend")
	}
	for i := 0; i < 2; i++ {
		if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 69); err != nil {
			t.Fatal(err)
		}
	}
	after, found, err := store.GetKernelExecutionBackend(ctx, before.BackendID)
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("backend changed during migration: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE kernel_execution_backends SET state='stopped',state_version=state_version+1 WHERE backend_id=?`, before.BackendID); err == nil {
		t.Fatal("terminal transition accepted without receipt")
	}
	if _, err := store.db.Exec(`UPDATE kernel_execution_backends SET backend_generation=backend_generation+1,state_version=state_version+1 WHERE backend_id=?`, before.BackendID); err == nil {
		t.Fatal("competing starting generation accepted")
	}
	failure := KernelStartupFailure{BackendID: before.BackendID, BackendGeneration: before.BackendGeneration, ExecutorInstanceID: before.ExecutorInstanceID, Stage: "launch"}
	for i := 0; i < 2; i++ {
		if err := store.FailKernelExecutorStartup(ctx, failure); err != nil {
			t.Fatal(err)
		}
	}
	terminal, _, err := store.GetKernelExecutionBackend(ctx, before.BackendID)
	if err != nil || terminal.State != KernelExecutionBackendStateStopped || terminal.ExecutorPID != 0 || terminal.StateVersion != before.StateVersion+1 {
		t.Fatalf("terminal=%#v error=%v", terminal, err)
	}
	receipt, found, err := store.GetKernelStartupFailure(ctx, before.BackendID, before.BackendGeneration)
	if err != nil || !found || receipt.Stage != "launch" {
		t.Fatalf("receipt=%#v found=%v error=%v", receipt, found, err)
	}
	retained, found, err := store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
	if err != nil || !found || !reflect.DeepEqual(execution, retained) {
		t.Fatalf("historical execution changed: %v", err)
	}
	spec, _ := DecodeKernelExecutionSessionSpecV1(before.SessionSpecJSON)
	recreated, err := store.RecreateKernelExecutionBackend(ctx, RecreateKernelExecutionBackendInput{BackendID: before.BackendID, ExecutorInstanceID: "next-executor", MachineBootID: before.MachineBootID, SocketPath: before.SocketPath, KernelID: before.KernelID, KernelGeneration: before.KernelGeneration, SessionSpec: spec})
	if err != nil || recreated.BackendGeneration != before.BackendGeneration+1 {
		t.Fatal(err)
	}
	if err := store.FailKernelExecutorStartup(ctx, failure); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("old failure changed new generation: %v", err)
	}
	var violations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign keys=%d error=%v", violations, err)
	}
}

func TestKernelStartupFailureRollsBackReceiptAndTerminalTogether(t *testing.T) {
	ctx := context.Background()
	store, backend, _ := unstartedBackendV68Fixture(t)
	if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 69); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER injected_terminal_failure BEFORE UPDATE ON kernel_execution_backends WHEN NEW.state='stopped' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	err := store.FailKernelExecutorStartup(ctx, KernelStartupFailure{BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID, Stage: "worker_start"})
	if err == nil {
		t.Fatal("injected terminal failure ignored")
	}
	if _, found, err := store.GetKernelStartupFailure(ctx, backend.BackendID, backend.BackendGeneration); err != nil || found {
		t.Fatalf("partial receipt survived: found=%v error=%v", found, err)
	}
	after, _, err := store.GetKernelExecutionBackend(ctx, backend.BackendID)
	if err != nil || !reflect.DeepEqual(backend, after) {
		t.Fatalf("head changed: %v", err)
	}
}

func TestKernelStartupClaimFencesCompetingProcessAndActivation(t *testing.T) {
	ctx := context.Background()
	store, backend, _ := unstartedBackendV68Fixture(t)
	if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 69); err != nil {
		t.Fatal(err)
	}
	claim := ActivateKernelExecutionBackendInput{BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID, ExecutorPID: 1234, ExecutorPIDStartTicks: 9876}
	for i := 0; i < 2; i++ {
		if err := store.ClaimKernelExecutorStartup(ctx, claim); err != nil {
			t.Fatal(err)
		}
	}
	other := claim
	other.ExecutorPID++
	if err := store.ClaimKernelExecutorStartup(ctx, other); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("competing executor claim: %v", err)
	}
	other.WorkerPID, other.WorkerPIDStartTicks, other.WorkerPGID, other.HeartbeatSequence = 1240, 9880, 1240, 1
	if _, err := store.ActivateKernelExecutionBackend(ctx, other); err == nil {
		t.Fatal("competing process activated")
	}
	claim.WorkerPID, claim.WorkerPIDStartTicks, claim.WorkerPGID, claim.HeartbeatSequence = 1240, 9880, 1240, 1
	if _, err := store.ActivateKernelExecutionBackend(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err := store.FailKernelExecutorStartup(ctx, KernelStartupFailure{BackendID: backend.BackendID, BackendGeneration: backend.BackendGeneration, ExecutorInstanceID: backend.ExecutorInstanceID, Stage: "activation"}); !errors.Is(err, ErrKernelExecutionBackendStale) {
		t.Fatalf("late startup failure changed ready backend: %v", err)
	}
}

func TestKernelStartupV69MigrationFailureRestoresSchemaAndJournal(t *testing.T) {
	ctx := context.Background()
	store, before, _ := unstartedBackendV68Fixture(t)
	if _, err := store.db.Exec(`CREATE TRIGGER fail_v69_journal BEFORE INSERT ON workspace_schema_migrations WHEN NEW.version=69 BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 69); err == nil {
		t.Fatal("injected journal failure ignored")
	}
	after, _, err := store.GetKernelExecutionBackend(ctx, before.BackendID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("migration rollback changed backend: %v", err)
	}
	var version, objects, enforcement, rename int
	for query, target := range map[string]*int{
		`SELECT MAX(version) FROM workspace_schema_migrations`:                                                                   &version,
		`SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('kernel_execution_backends_v68','kernel_execution_startup_receipts')`: &objects,
		`PRAGMA foreign_keys`: &enforcement, `PRAGMA legacy_alter_table`: &rename,
	} {
		if err := store.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if version != 68 || objects != 0 || enforcement != 1 || rename != 0 {
		t.Fatalf("partial upgrade: version=%d objects=%d foreignKeys=%d rename=%d", version, objects, enforcement, rename)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_v69_journal`); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 69); err != nil {
		t.Fatal(err)
	}
}
