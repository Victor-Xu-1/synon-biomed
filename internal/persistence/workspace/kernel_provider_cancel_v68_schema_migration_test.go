package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	"testing"
	"time"
)

func providerCancellationV67Fixture(t *testing.T) (*Store, KernelExecutionBackend, DetachedKernelExecution) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.sqlite")
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now, blobRoot: path + ".blobs"}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := prepareSchemaJournal(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareLegacyWorkspaceSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 67); err != nil {
		t.Fatal(err)
	}
	store.transcriptRepository = transcriptstore.NewRepository(db)
	_, repo, claim := initializeKernelLocalOperationFixture(t, store)
	_, backend, _, execution := acceptDetachedKernelExecutionFixture(t, store, repo, claim)
	return store, backend, execution
}

func TestProviderCancelV68PreservesGraphAndAcknowledgesIdempotently(t *testing.T) {
	ctx := context.Background()
	store, backend, execution := providerCancellationV67Fixture(t)
	if _, _, err := store.CancelFrameWithTranscript(ctx, backend.FrameID); err != nil {
		t.Fatal(err)
	}
	before, _, err := store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	ack := AcknowledgeKernelExecutionCancelInput{
		ExecutionID: execution.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExpectedVersion: before.StateVersion,
		CancelRequestID: before.CancelRequestID, AckSequence: before.LastObservationSequence + 1, Signal: "provider_cancel",
	}
	// The old constraint rejects a provider acknowledgement, even independently
	// of the Go contract. No historical acknowledgement is rewritten as a signal.
	if _, err := store.db.Exec(`UPDATE kernel_detached_executions SET state_version=state_version+1,
		cancel_ack_sequence=1,cancel_ack_at=?,cancel_signal='provider_cancel' WHERE execution_id=?`,
		time.Now().UTC().Format(time.RFC3339Nano), execution.ExecutionID); err == nil {
		t.Fatal("v67 unexpectedly accepted provider cancellation")
	}
	for i := 0; i < 2; i++ {
		if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 68); err != nil {
			t.Fatal(err)
		}
	}
	after, _, err := store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("execution changed during migration: %v", err)
	}
	for i := 0; i < 2; i++ {
		updated, err := store.AcknowledgeKernelExecutionCancel(ctx, ack)
		if err != nil || updated.CancelSignal != "provider_cancel" || updated.CancelAckAt == nil || updated.StateVersion != before.StateVersion+1 {
			t.Fatalf("provider acknowledgement/replay: state=%s version=%d signal=%s err=%v", updated.State, updated.StateVersion, updated.CancelSignal, err)
		}
	}
	var violations, enforcement, objects int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign keys: %d %v", violations, err)
	}
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enforcement); err != nil || enforcement != 1 {
		t.Fatalf("FK enforcement lost: %d %v", enforcement, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE tbl_name='kernel_detached_executions' AND type IN ('index','trigger') AND sql IS NOT NULL`).Scan(&objects); err != nil || objects != 6 {
		t.Fatalf("execution fences lost: %d %v", objects, err)
	}
	if _, err := store.db.Exec(`UPDATE kernel_detached_executions SET state_version=state_version+1,request_sha256=? WHERE execution_id=?`, strings.Repeat("f", 64), execution.ExecutionID); err == nil {
		t.Fatal("identity fence lost")
	}
}

func TestProviderCancelV68FailureRollsBackSchemaAndJournal(t *testing.T) {
	store, _, execution := providerCancellationV67Fixture(t)
	ctx := context.Background()
	// Force a post-rebuild failure, proving that both copied rows and schema
	// changes roll back with the journal rather than leaving a partial upgrade.
	if _, err := store.db.Exec(`CREATE TRIGGER fail_v68_journal BEFORE INSERT ON workspace_schema_migrations
		WHEN NEW.version=68 BEGIN SELECT RAISE(ABORT,'injected journal failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 68); err == nil {
		t.Fatal("injected failure was ignored")
	}
	var ddl string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='kernel_detached_executions'`).Scan(&ddl); err != nil || !strings.Contains(ddl, kernelCancelSignalsV67) {
		t.Fatalf("schema not rolled back: %v", err)
	}
	if _, found, err := store.GetDetachedKernelExecution(ctx, execution.ExecutionID); err != nil || !found {
		t.Fatalf("execution lost: %v", err)
	}
	var version, staged, enforcement, rename int
	if err := store.db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil || version != 67 {
		t.Fatalf("journal advanced: %d %v", version, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='kernel_detached_executions_v67'`).Scan(&staged); err != nil || staged != 0 {
		t.Fatalf("staging residue: %d %v", staged, err)
	}
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enforcement); err != nil || enforcement != 1 {
		t.Fatalf("FK enforcement: %d %v", enforcement, err)
	}
	if err := store.db.QueryRow(`PRAGMA legacy_alter_table`).Scan(&rename); err != nil || rename != 0 {
		t.Fatalf("rename policy: %d %v", rename, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_v68_journal`); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 68); err != nil {
		t.Fatal(err)
	}
}

func TestProviderCancelV68PreservesTerminalReceiptAcrossReopen(t *testing.T) {
	ctx := context.Background()
	store, backend, execution := providerCancellationV67Fixture(t)
	if _, _, err := store.CancelFrameWithTranscript(ctx, backend.FrameID); err != nil {
		t.Fatal(err)
	}
	current, _, err := store.GetDetachedKernelExecution(ctx, execution.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcknowledgeKernelExecutionCancel(ctx, AcknowledgeKernelExecutionCancelInput{
		ExecutionID: current.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, ExpectedVersion: current.StateVersion,
		CancelRequestID: current.CancelRequestID, AckSequence: 1, Signal: "dequeue",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	before, receipt, err := store.CommitKernelExecutionResult(ctx, CommitKernelExecutionResultInput{
		ReceiptID: "retained-receipt", ExecutionID: current.ExecutionID, BackendGeneration: backend.BackendGeneration,
		ExecutorInstanceID: backend.ExecutorInstanceID, TerminalSequence: 2, Outcome: KernelExecutionResultCancelled,
		ResultJSON: `{"cancelled":true}`, ResultSHA256: sha256HexString(`{"cancelled":true}`), Interrupted: true,
		StartedAt: now, FinishedAt: now, FilesWrittenJSON: `[]`, DroppedRootsJSON: `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, path string
	if err := store.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, store.db, time.Now, 68); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, found, err := reopened.GetDetachedKernelExecution(ctx, execution.ExecutionID)
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Fatalf("execution changed across upgrade/reopen: %v", err)
	}
	persisted, found, err := reopened.GetKernelExecutionResultReceipt(ctx, receipt.ReceiptID)
	if err != nil || !found || !reflect.DeepEqual(receipt, persisted) {
		t.Fatalf("terminal evidence changed across upgrade/reopen: %v", err)
	}
	var violations int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("terminal graph invalid: %d %v", violations, err)
	}
}
