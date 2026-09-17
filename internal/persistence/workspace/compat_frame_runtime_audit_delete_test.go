package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestDeleteCompatibilityFrameTreeReportsArtifactCleanupFailureAfterCommit(t *testing.T) {
	store, _, _ := newKernelLocalOperationFixture(t)
	frame, found, err := store.GetCompatibilityFrame("frame")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	artifact, version, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "delete-artifact", ProjectID: "project", Name: "delete.txt",
		ContentType: "text/plain", Content: strings.NewReader("artifact content"), MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	folder, err := store.CreateArtifactFolder(CreateArtifactFolderInput{
		ID: "delete-frame-folder", ProjectID: "project", Name: "Frame artifacts", RootFrameID: frame.RootFrameID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetArtifactFolder(artifact.ID, folder.ID); err != nil {
		t.Fatal(err)
	}
	abs, err := store.blobAbsolute(version.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	if err := os.WriteFile(filepath.Join(abs, "undeletable-child"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeleteCompatibilityFrameTree(frame.ID, "owner", frame.IncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.FramesDeleted != 1 || deleted.ArtifactsDeleted != 1 || deleted.ArtifactCleanupFailures != 1 {
		t.Fatalf("deleted=%#v", deleted)
	}
	if _, found, err := store.GetFrame(frame.ID); err != nil || found {
		t.Fatalf("deleted frame found=%t err=%v", found, err)
	}
	if _, found, err := store.GetArtifact(artifact.ID); err != nil || found {
		t.Fatalf("deleted artifact found=%t err=%v", found, err)
	}
}

func TestDeleteCompatibilityFrameTreeRemovesRuntimeAuditCohort(t *testing.T) {
	store, repository, claim := newKernelLocalOperationFixture(t)
	payload := kernelLocalOperationCheckpointPayload(t, "call-delete", "print('delete')")
	checkpoint, _, created, err := repository.AppendRunnerCheckpoint(
		context.Background(),
		transcriptstore.AppendRunnerCheckpointInput{
			Claim: claim, ClientMessageID: "tool-batch-delete", Phase: transcriptstore.RunnerPhaseExecuting,
			Resumable: true, PayloadJSON: payload,
			CommitHook: func(
				ctx context.Context,
				tx *transcriptstore.ImmediateTransaction,
				event transcriptstore.Event,
				created bool,
			) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
				batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
				if err != nil {
					return transcriptstore.RunnerCheckpointCommitReceipt{}, err
				}
				operations, err := store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
				return kernelLocalOperationCommitReceiptWithBatch(batch, operations), err
			},
		},
	)
	if err != nil || !created || checkpoint.EventID <= 0 {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}
	if _, err := store.db.Exec(`DELETE FROM kernel_local_operations`); err == nil {
		t.Fatal("runtime audit operation was deleted without an authorized root scope")
	}

	frame, found, err := store.GetCompatibilityFrame("frame")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	if _, err := store.AppendRealtimeEvent(RealtimeEventInput{
		ID: "retired-realtime", UserID: "owner", ProjectID: "project", RootFrameID: frame.ID,
		FrameID: frame.ID, Type: "frame_update", Payload: map[string]any{"action": "updated"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueOutbox(context.Background(), EnqueueOutboxInput{
		ID: "retired-outbox", IdempotencyKey: "retired-outbox", Topic: "test.retired",
		Type: "test", AggregateType: "frame", AggregateID: frame.ID,
	}); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteCompatibilityFrameTree(frame.ID, "owner", frame.IncarnationID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.FramesDeleted != 1 || deleted.RootFrameID != frame.ID {
		t.Fatalf("deleted=%#v", deleted)
	}
	var retiredRealtime, retiredOutbox int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM realtime_events WHERE id='retired-realtime'`).Scan(&retiredRealtime); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workspace_outbox WHERE event_id='retired-outbox'`).Scan(&retiredOutbox); err != nil {
		t.Fatal(err)
	}
	if retiredRealtime != 0 || retiredOutbox != 0 {
		t.Fatalf("retired transport realtime=%d outbox=%d", retiredRealtime, retiredOutbox)
	}

	for _, table := range []string{
		"kernel_local_operation_protocol_receipts",
		"kernel_local_operation_transitions",
		"kernel_local_operation_materializations",
		"kernel_local_operations",
		"transcript_tool_call_items",
		"transcript_tool_call_batches",
		"transcript_streams",
		"workspace_runtime_delete_scopes",
	} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d", table, count)
		}
	}
}

func TestDeleteProjectRealtimeRemovesRuntimeAuditCohort(t *testing.T) {
	store, repository, claim := newKernelLocalOperationFixture(t)
	payload := kernelLocalOperationCheckpointPayload(t, "call-project-delete", "print('project delete')")
	checkpoint, _, created, err := repository.AppendRunnerCheckpoint(
		context.Background(),
		transcriptstore.AppendRunnerCheckpointInput{
			Claim: claim, ClientMessageID: "tool-batch-project-delete", Phase: transcriptstore.RunnerPhaseExecuting,
			Resumable: true, PayloadJSON: payload,
			CommitHook: func(
				ctx context.Context,
				tx *transcriptstore.ImmediateTransaction,
				event transcriptstore.Event,
				created bool,
			) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
				batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
				if err != nil {
					return transcriptstore.RunnerCheckpointCommitReceipt{}, err
				}
				operations, err := store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
				return kernelLocalOperationCommitReceiptWithBatch(batch, operations), err
			},
		},
	)
	if err != nil || !created || checkpoint.EventID <= 0 {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}

	if _, err := store.DeleteProjectRealtime(context.Background(), "project", "owner", ""); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"kernel_local_operation_protocol_receipts",
		"kernel_local_operation_transitions",
		"kernel_local_operation_materializations",
		"kernel_local_operations",
		"transcript_tool_call_items",
		"transcript_tool_call_batches",
		"transcript_streams",
		"workspace_runtime_delete_scopes",
		"frames",
		"projects",
	} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d", table, count)
		}
	}
}

func TestDeleteProjectRealtimeRemovesRunnerLargeToolResult(t *testing.T) {
	store, _, _ := newKernelLocalOperationFixture(t)
	const storagePath = "runner-large-tool-results/delete.json"
	_, err := store.db.Exec(`INSERT INTO runner_large_tool_results(
		artifact_id,version_id,project_id,root_frame_id,frame_id,stream_uid,owner_user_id,
		runner_id,claim_token,attempt,source_event_id,tool_name,tool_call_id,content_type,
		size_bytes,content_sha256,storage_path,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"large-tool-result-"+strings.Repeat("a", 32), "ltr-delete", "project", "frame", "frame",
		"frame:frame", "owner", "runner", "claim", 1, 1, "mcp__fixture__large", "call-large",
		"application/json", 3, strings.Repeat("b", 64), storagePath, "2026-08-08T00:00:00Z")
	if err != nil {
		t.Fatalf("insert runner large tool result: %v", err)
	}

	paths, err := store.DeleteProjectRealtime(context.Background(), "project", "owner", "")
	if err != nil {
		t.Fatalf("delete project with runner large tool result: %v", err)
	}
	if len(paths) != 1 || paths[0] != storagePath {
		t.Fatalf("deleted runner large tool result paths=%v", paths)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM runner_large_tool_results").Scan(&count); err != nil {
		t.Fatalf("count runner large tool results: %v", err)
	}
	if count != 0 {
		t.Fatalf("runner large tool result rows=%d", count)
	}
}

func TestDeleteCompatibilityFrameTreeRemovesRunnerLargeToolResult(t *testing.T) {
	store, _, _ := newKernelLocalOperationFixture(t)
	_, err := store.db.Exec(`INSERT INTO runner_large_tool_results(
		artifact_id,version_id,project_id,root_frame_id,frame_id,stream_uid,owner_user_id,
		runner_id,claim_token,attempt,source_event_id,tool_name,tool_call_id,content_type,
		size_bytes,content_sha256,storage_path,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"large-tool-result-"+strings.Repeat("c", 32), "ltr-frame-delete", "project", "frame", "frame",
		"frame:frame", "owner", "runner", "claim", 1, 1, "mcp__fixture__large", "call-frame-large",
		"application/json", 3, strings.Repeat("d", 64), "runner-large-tool-results/frame-delete.json",
		"2026-08-08T00:00:00Z")
	if err != nil {
		t.Fatalf("insert runner large tool result: %v", err)
	}
	frame, found, err := store.GetCompatibilityFrame("frame")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	if _, err := store.DeleteCompatibilityFrameTree(frame.ID, "owner", frame.IncarnationID); err != nil {
		t.Fatalf("delete frame with runner large tool result: %v", err)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM runner_large_tool_results").Scan(&count); err != nil {
		t.Fatalf("count runner large tool results: %v", err)
	}
	if count != 0 {
		t.Fatalf("runner large tool result rows=%d", count)
	}
}
