package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestKernelSoftwareRuntimeV55PublishedIdentity(t *testing.T) {
	checksum, err := kernelSoftwareRuntimeV55Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "8bdef5e24bdcd7b412075d1c9b6ce2dfbb610214f35a2ee91d5aaf71ed935e30"
	if checksum != published {
		t.Fatalf("published v55 checksum changed: got %s want %s", checksum, published)
	}
}

func TestKernelSoftwareRuntimeV55PreservesCallerForeignKeySetting(t *testing.T) {
	db, _ := openTargetTwentyTwoProviderDB(t)
	var before int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("fixture foreign keys=%d err=%v", before, err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 55); err != nil {
		t.Fatal(err)
	}
	var after, violations int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&after); err != nil || after != before {
		t.Fatalf("foreign key setting changed: before=%d after=%d err=%v", before, after, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
}

func TestKernelSoftwareRuntimeV55PreservesOperationGraphAndAdmitsUnifiedTool(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now, blobRoot: path + ".blobs"}
	if err := prepareSchemaJournal(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareLegacyWorkspaceSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 54); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-v55", UserID: "owner-v55", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-v55", ProjectID: "project-v55", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := transcriptstore.NewRepository(db)
	store.transcriptRepository = repo
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame-v55", OwnerID: "owner-v55", ExternalID: "frame-v55", SessionID: "frame-v55",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-v55", RootFrameID: frame.RootFrameID,
		FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task-v55", FrameEventID: "task-event-v55",
		MessageUUID: "task-message-v55", Text: "Run software.",
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-v55", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	legacy := createKernelLocalOperationForTest(t, store, repo, claimed.Claim, "legacy-v55", "call-legacy-v55")
	var legacyTransitions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_transitions WHERE operation_id=?`, legacy.OperationID).Scan(&legacyTransitions); err != nil || legacyTransitions != 1 {
		t.Fatalf("legacy transitions=%d err=%v", legacyTransitions, err)
	}

	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 55); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.GetKernelLocalOperation(ctx, "owner-v55", legacy.OperationID)
	if err != nil || !found || stored.Tool != "python" || stored.InputSHA256 != legacy.InputSHA256 {
		t.Fatalf("preserved operation=%#v found=%t err=%v", stored, found, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_transitions WHERE operation_id=?`, legacy.OperationID).Scan(&legacyTransitions); err != nil || legacyTransitions != 1 {
		t.Fatalf("preserved transitions=%d err=%v", legacyTransitions, err)
	}
	var foreignKeys, violations, staleReferences int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys=%d err=%v", foreignKeys, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE sql LIKE '%kernel_local_operations_v39%'`).Scan(&staleReferences); err != nil || staleReferences != 0 {
		t.Fatalf("stale v39 references=%d err=%v", staleReferences, err)
	}
	var tableSQL, deleteTriggerSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='kernel_local_operations'`).Scan(&tableSQL); err != nil || !strings.Contains(tableSQL, "'software_runtime'") {
		t.Fatalf("operation table SQL=%q err=%v", tableSQL, err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='kernel_local_operations_delete_forbidden'`).Scan(&deleteTriggerSQL); err != nil || !strings.Contains(deleteTriggerSQL, "workspace_runtime_delete_scopes") {
		t.Fatalf("scoped delete trigger SQL=%q err=%v", deleteTriggerSQL, err)
	}

	payload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": "call-software-v55", "type": "function", "name": "software_runtime",
			"arguments": map[string]any{
				"capability": "sequence-alignment", "language": "native", "executable": "minimap2",
				"packages": []any{map[string]any{"manager": "conda", "spec": "minimap2"}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var operations []KernelLocalOperation
	_, _, created, err := repo.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "software-v55", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, created bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, createErr := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if createErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, createErr
			}
			operations, createErr = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), createErr
		},
	})
	if err != nil || !created || len(operations) != 1 || operations[0].Tool != "software_runtime" {
		t.Fatalf("software operations=%#v created=%t err=%v", operations, created, err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 55); err != nil {
		t.Fatalf("repeat v55 migration: %v", err)
	}
}
