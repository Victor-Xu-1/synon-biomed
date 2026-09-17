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

func TestKernelBashV56PublishedIdentity(t *testing.T) {
	checksum, err := kernelBashV56Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "8ce90818b2daf876c80342fc5c7f9484701fa2354c54d28cfffeeb616b8b6c0f"
	if checksum != published {
		t.Fatalf("published v56 checksum changed: got %s want %s", checksum, published)
	}
}

func TestKernelBashV56PreservesOperationGraphAndAdmitsBash(t *testing.T) {
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
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 55); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-v56", UserID: "owner-v56", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-v56", ProjectID: "project-v56", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := transcriptstore.NewRepository(db)
	store.transcriptRepository = repo
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame-v56", OwnerID: "owner-v56", ExternalID: "frame-v56", SessionID: "frame-v56",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-v56", RootFrameID: frame.RootFrameID,
		FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task-v56", FrameEventID: "task-event-v56",
		MessageUUID: "task-message-v56", Text: "Run a managed Bash step.",
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-v56", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	legacy := createKernelLocalOperationForTest(t, store, repo, claimed.Claim, "legacy-v56", "call-legacy-v56")

	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 56); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.GetKernelLocalOperation(ctx, "owner-v56", legacy.OperationID)
	if err != nil || !found || stored.Tool != "python" || stored.InputSHA256 != legacy.InputSHA256 {
		t.Fatalf("preserved operation=%#v found=%t err=%v", stored, found, err)
	}
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='kernel_local_operations'`).Scan(&tableSQL); err != nil ||
		!strings.Contains(tableSQL, "'bash'") || !strings.Contains(tableSQL, "'software_runtime'") {
		t.Fatalf("operation table SQL=%q err=%v", tableSQL, err)
	}

	payload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": "call-bash-v56", "type": "function", "name": "bash",
			"arguments": map[string]any{
				"command": "printf verified", "environment": "verified-env", "human_description": "Running a verified command",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var operations []KernelLocalOperation
	_, _, created, err := repo.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "bash-v56", Phase: transcriptstore.RunnerPhaseExecuting,
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
	if err != nil || !created || len(operations) != 1 || operations[0].Tool != "bash" {
		t.Fatalf("bash operations=%#v created=%t err=%v", operations, created, err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 56); err != nil {
		t.Fatalf("repeat v56 migration: %v", err)
	}
}
