package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestToolCallBatchCreateForCheckpointIsAtomicDeterministicAndReplaySafe(t *testing.T) {
	store, repo, claim := newToolCallBatchFixture(t)
	payload := toolCallBatchRootPayload(t,
		toolCallBatchTestCall{id: "read-1", name: "read_file", arguments: map[string]any{"path": "notes.txt"}},
		toolCallBatchTestCall{id: "fetch-1", name: "web_fetch", arguments: map[string]any{"url": "https://example.test"}},
	)

	var first ToolCallBatch
	var firstItems []ToolCallBatchItem
	_, source, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-batch-root", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var hookErr error
			first, firstItems, _, hookErr = store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			return toolCallBatchTestCommitReceipt(first, hookErr)
		},
	})
	if err != nil || !created || source.EventID <= 0 {
		t.Fatalf("root event=%#v created=%t err=%v", source, created, err)
	}
	if first.BatchID == "" || first.BatchID != toolCallBatchID(claim.StreamUID, source.EventID) ||
		first.CallCount != 2 || first.NextOrdinal != 0 || first.State != ToolCallBatchStateReady ||
		first.StateVersion != 1 || len(firstItems) != 2 {
		t.Fatalf("batch=%#v items=%#v", first, firstItems)
	}
	if firstItems[0].Ordinal != 0 || firstItems[0].ToolCallID != "read-1" || firstItems[0].ToolName != "read_file" ||
		firstItems[1].Ordinal != 1 || firstItems[1].ToolCallID != "fetch-1" {
		t.Fatalf("items=%#v", firstItems)
	}

	var replay ToolCallBatch
	var replayItems []ToolCallBatchItem
	_, replayEvent, replayCreated, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-batch-root", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var hookErr error
			replay, replayItems, _, hookErr = store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			return toolCallBatchTestCommitReceipt(replay, hookErr)
		},
	})
	if err != nil || replayCreated || replayEvent.EventID != source.EventID || replay.BatchID != first.BatchID ||
		len(replayItems) != len(firstItems) {
		t.Fatalf("replay=%#v items=%#v event=%#v created=%t err=%v", replay, replayItems, replayEvent, replayCreated, err)
	}

	rollbackErr := errors.New("inject tool batch rollback")
	_, _, _, err = repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-batch-rollback", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			if _, _, _, createErr := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event); createErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, createErr
			}
			return transcriptstore.RunnerCheckpointCommitReceipt{}, rollbackErr
		},
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error=%v", err)
	}
	var rolledBackEvents, rolledBackBatches int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND client_message_id='tool-batch-rollback'`, claim.StreamUID).Scan(&rolledBackEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_tool_call_batches WHERE source_client_message_id='tool-batch-rollback'`).Scan(&rolledBackBatches); err != nil {
		t.Fatal(err)
	}
	if rolledBackEvents != 0 || rolledBackBatches != 0 {
		t.Fatalf("rollback events=%d batches=%d", rolledBackEvents, rolledBackBatches)
	}
}

func TestToolCallBatchClaimStartWaitAndTerminalAdvanceUseCAS(t *testing.T) {
	store, repo, claim := newToolCallBatchFixture(t)
	batch, items := createToolCallBatchForTest(t, store, repo, claim, "tool-batch-lifecycle",
		toolCallBatchTestCall{id: "read-1", name: "read_file", arguments: map[string]any{"path": "notes.txt"}},
		toolCallBatchTestCall{id: "fetch-1", name: "web_fetch", arguments: map[string]any{"url": "https://example.test"}},
	)

	runnable, err := store.ListRunnableToolCallBatches(context.Background(), claim, 10)
	if err != nil || len(runnable) != 1 || runnable[0].BatchID != batch.BatchID {
		t.Fatalf("runnable=%#v err=%v", runnable, err)
	}
	batch, err = store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
		Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil || batch.StateVersion != 2 || batch.RunnerAttempt != claim.Attempt || batch.RunnerID != claim.RunnerID {
		t.Fatalf("claimed batch=%#v err=%v", batch, err)
	}

	batch, items[0] = appendToolCallBatchStart(t, store, repo, claim, batch, items[0], "tool-batch-start-0")
	if batch.State != ToolCallBatchStateRunning || items[0].State != ToolCallBatchItemStateRunning ||
		items[0].StartedEventID <= 0 || batch.NextOrdinal != 0 {
		t.Fatalf("started batch=%#v item=%#v", batch, items[0])
	}
	batch, items[0] = appendToolCallBatchWait(t, store, repo, claim, batch, items[0], "tool-batch-wait-0")
	if batch.State != ToolCallBatchStateWaiting || batch.WaitingOrdinal == nil || *batch.WaitingOrdinal != 0 ||
		items[0].State != ToolCallBatchItemStateWaiting || items[0].WaitingEventID <= 0 {
		t.Fatalf("waiting batch=%#v item=%#v", batch, items[0])
	}

	completedResult := writeToolCallBatchExternalizedResult(
		t, store, claim, items[0], "artifact-1",
		[]byte(`{"payload":"`+strings.Repeat("x", 8192)+`"}`),
	)
	batch, items[0] = appendToolCallBatchTerminal(t, store, repo, claim, batch, items[0],
		"tool-batch-terminal-0", ToolCallBatchItemStateCompleted, completedResult, "")
	wantResult, _ := json.Marshal(completedResult)
	wantDigest := sha256.Sum256(wantResult)
	wantResultRef := "artifact-version:" + completedResult["version_id"].(string)
	if batch.State != ToolCallBatchStateReady || batch.NextOrdinal != 1 || batch.WaitingOrdinal != nil ||
		items[0].State != ToolCallBatchItemStateCompleted || items[0].TerminalEventID <= 0 ||
		items[0].TerminalResultSHA256 != hex.EncodeToString(wantDigest[:]) || items[0].ResultRef != wantResultRef {
		t.Fatalf("first terminal batch=%#v item=%#v", batch, items[0])
	}

	batch, items[1] = appendToolCallBatchStart(t, store, repo, claim, batch, items[1], "tool-batch-start-1")
	batch, items[1] = appendToolCallBatchTerminal(t, store, repo, claim, batch, items[1],
		"tool-batch-terminal-1", ToolCallBatchItemStateFailed,
		map[string]any{"ok": false, "error": "upstream unavailable"}, "")
	if batch.State != ToolCallBatchStateSettled || batch.NextOrdinal != 2 || items[1].State != ToolCallBatchItemStateFailed {
		t.Fatalf("settled batch=%#v item=%#v", batch, items[1])
	}
	runnable, err = store.ListRunnableToolCallBatches(context.Background(), claim, 10)
	if err != nil || len(runnable) != 0 {
		t.Fatalf("settled runnable=%#v err=%v", runnable, err)
	}

	stored, found, err := store.GetToolCallBatch(context.Background(), claim.OwnerID, batch.BatchID)
	if err != nil || !found || stored.StateVersion != batch.StateVersion || stored.NextOrdinal != batch.NextOrdinal {
		t.Fatalf("stored=%#v found=%t err=%v", stored, found, err)
	}
	storedItems, err := store.ListToolCallBatchItems(context.Background(), claim.OwnerID, batch.BatchID)
	if err != nil || len(storedItems) != 2 || storedItems[0].TerminalResultSHA256 == "" || storedItems[1].TerminalResultSHA256 == "" {
		t.Fatalf("stored items=%#v err=%v", storedItems, err)
	}
}

func TestToolCallBatchImmutableResultRefRecognizesOnlyExactExternalizedDescriptor(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "scalar", raw: `"plain result"`},
		{name: "ordinary truncated result", raw: `{"truncated":true,"value":"domain payload"}`},
		{name: "ordinary artifact identity", raw: `{"artifact_id":"domain-artifact","value":7}`},
		{name: "descriptor", raw: `{"artifact_id":"artifact-1","content_type":"application/json","content_url":"/api/artifacts/artifact-1/versions/version-1","outcome":"succeeded","preview":"{","sha256":"` + strings.Repeat("a", 64) + `","size_bytes":8192,"truncated":true,"version_id":"version-1"}`, want: "artifact-version:version-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := toolCallBatchImmutableResultRef([]byte(test.raw))
			if err != nil || got != test.want {
				t.Fatalf("ref=%q err=%v, want %q", got, err, test.want)
			}
		})
	}
}

func TestToolCallBatchExternalizedTerminalRequiresExactArtifactReceipt(t *testing.T) {
	store, repo, claim := newToolCallBatchFixture(t)
	batch, items := createToolCallBatchForTest(t, store, repo, claim, "tool-batch-artifact-receipt",
		toolCallBatchTestCall{id: "read-1", name: "read_file", arguments: map[string]any{"path": "large.json"}},
	)
	var err error
	batch, err = store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
		Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, items[0] = appendToolCallBatchStart(t, store, repo, claim, batch, items[0], "tool-batch-artifact-start")

	tryTerminal := func(clientMessageID string, result map[string]any) error {
		payload, _ := json.Marshal(map[string]any{
			"status": ToolCallBatchItemStateCompleted, "toolCallId": items[0].ToolCallID,
			"toolPhase": ToolCallBatchItemStateCompleted, "toolResult": result,
		})
		_, _, _, appendErr := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: claim, ClientMessageID: clientMessageID, Phase: transcriptstore.RunnerPhaseExecuting,
			Resumable: true, PayloadJSON: payload,
			CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
				_, _, finishErr := store.FinishToolCallBatchItemTx(ctx, tx, FinishToolCallBatchItemInput{
					Claim: claim, BatchID: batch.BatchID, Ordinal: items[0].Ordinal,
					ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: items[0].StateVersion,
					TerminalEvent: event, TerminalState: ToolCallBatchItemStateCompleted,
				})
				return transcriptstore.RunnerCheckpointCommitReceipt{}, finishErr
			},
		})
		return appendErr
	}

	missing := map[string]any{
		"artifact_id": "artifact-missing", "version_id": "version-missing",
		"sha256": strings.Repeat("a", 64), "size_bytes": 8192,
		"content_type": "application/json", "outcome": "succeeded",
		"content_url": "/api/artifacts/artifact-missing/versions/version-missing",
		"preview":     "{", "truncated": true,
	}
	if err := tryTerminal("tool-batch-artifact-missing", missing); !errors.Is(err, ErrToolCallBatchConflict) {
		t.Fatalf("missing artifact receipt error=%v", err)
	}
	correct := writeToolCallBatchExternalizedResult(
		t, store, claim, items[0], "artifact-receipt",
		[]byte(`{"payload":"`+strings.Repeat("y", 8192)+`"}`),
	)
	mismatched := make(map[string]any, len(correct))
	for key, value := range correct {
		mismatched[key] = value
	}
	mismatched["sha256"] = strings.Repeat("b", 64)
	if err := tryTerminal("tool-batch-artifact-mismatch", mismatched); !errors.Is(err, ErrToolCallBatchConflict) {
		t.Fatalf("mismatched artifact receipt error=%v", err)
	}

	batch, items[0] = appendToolCallBatchTerminal(t, store, repo, claim, batch, items[0],
		"tool-batch-artifact-correct", ToolCallBatchItemStateCompleted, correct, "")
	if batch.State != ToolCallBatchStateSettled || items[0].ResultRef == "" {
		t.Fatalf("settled batch=%#v item=%#v", batch, items[0])
	}
	var rolledBackEvents int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events
		WHERE stream_uid=? AND client_message_id IN ('tool-batch-artifact-missing','tool-batch-artifact-mismatch')`,
		claim.StreamUID).Scan(&rolledBackEvents); err != nil || rolledBackEvents != 0 {
		t.Fatalf("rolled-back events=%d err=%v", rolledBackEvents, err)
	}
}

func TestToolCallBatchTerminalCASFailureRollsBackCheckpointAndCursor(t *testing.T) {
	store, repo, claim := newToolCallBatchFixture(t)
	batch, items := createToolCallBatchForTest(t, store, repo, claim, "tool-batch-terminal-rollback",
		toolCallBatchTestCall{id: "read-1", name: "read_file", arguments: map[string]any{"path": "notes.txt"}},
	)
	var err error
	batch, err = store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
		Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, items[0] = appendToolCallBatchStart(t, store, repo, claim, batch, items[0], "tool-batch-rollback-start")

	payload, _ := json.Marshal(map[string]any{
		"status": "completed", "toolCallId": items[0].ToolCallID, "toolPhase": "completed",
		"toolResult": map[string]any{"ok": true},
	})
	_, _, _, err = repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-batch-bad-terminal", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			_, _, finishErr := store.FinishToolCallBatchItemTx(ctx, tx, FinishToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: items[0].Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion,
				ExpectedItemStateVersion:  items[0].StateVersion + 1,
				TerminalEvent:             event, TerminalState: ToolCallBatchItemStateCompleted,
			})
			return transcriptstore.RunnerCheckpointCommitReceipt{}, finishErr
		},
	})
	if !errors.Is(err, ErrToolCallBatchStale) {
		t.Fatalf("terminal error=%v", err)
	}
	var terminalEvents int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND client_message_id='tool-batch-bad-terminal'`, claim.StreamUID).Scan(&terminalEvents); err != nil {
		t.Fatal(err)
	}
	stored, found, err := store.GetToolCallBatch(context.Background(), claim.OwnerID, batch.BatchID)
	storedItems, itemsErr := store.ListToolCallBatchItems(context.Background(), claim.OwnerID, batch.BatchID)
	if terminalEvents != 0 || err != nil || itemsErr != nil || !found || stored.NextOrdinal != 0 ||
		stored.State != ToolCallBatchStateRunning || len(storedItems) != 1 || storedItems[0].State != ToolCallBatchItemStateRunning {
		t.Fatalf("events=%d stored=%#v items=%#v found=%t err=%v itemsErr=%v", terminalEvents, stored, storedItems, found, err, itemsErr)
	}
}

func TestToolCallBatchTerminalAcceptsRunnerLargeToolResultEvidence(t *testing.T) {
	store, repo, claim := newToolCallBatchFixture(t)
	batch, items := createToolCallBatchForTest(t, store, repo, claim, "tool-batch-ltr",
		toolCallBatchTestCall{id: "pubmed-1", name: "mcp__pubmed__get_article_metadata", arguments: map[string]any{"pmids": []string{"42453876"}}},
	)
	var err error
	batch, err = store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
		Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, items[0] = appendToolCallBatchStart(t, store, repo, claim, batch, items[0], "tool-batch-ltr-start")

	raw := []byte(`{"articles":[{"pmid":"42453876","title":"Runner large tool result regression"}]}`)
	digest := sha256.Sum256(raw)
	artifactID := "large-tool-result-" + strings.Repeat("a", 32)
	versionID := "ltr-regression-0001"
	now := time.Now().UTC()
	if _, err := store.db.Exec(`INSERT INTO runner_large_tool_results(
		artifact_id,version_id,project_id,root_frame_id,frame_id,stream_uid,owner_user_id,
		runner_id,claim_token,attempt,source_event_id,tool_name,tool_call_id,content_type,
		size_bytes,content_sha256,storage_path,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		artifactID, versionID, "tool-batch-project", "tool-batch-frame", "tool-batch-frame",
		claim.StreamUID, claim.OwnerID, claim.RunnerID, claim.ClaimToken, claim.Attempt,
		items[0].StartedEventID, items[0].ToolName, items[0].ToolCallID, "application/json",
		len(raw), hex.EncodeToString(digest[:]), "large-tool-results/ltr-regression-0001.blob",
		now.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}

	descriptor := map[string]any{
		"artifact_id": artifactID, "version_id": versionID,
		"sha256": hex.EncodeToString(digest[:]), "size_bytes": len(raw),
		"content_type": "application/json", "outcome": "succeeded",
		"content_url": "/api/artifacts/" + artifactID + "/versions/" + versionID,
		"preview":     `{"articles":`, "truncated": true,
	}
	batch, items[0] = appendToolCallBatchTerminal(t, store, repo, claim, batch, items[0],
		"tool-batch-ltr-terminal", ToolCallBatchItemStateCompleted, descriptor, "")
	if batch.State != ToolCallBatchStateSettled || batch.NextOrdinal != 1 ||
		items[0].State != ToolCallBatchItemStateCompleted || items[0].ResultRef != "artifact-version:"+versionID {
		t.Fatalf("batch=%#v item=%#v", batch, items[0])
	}
}

func TestToolCallBatchReconcilesTerminalKernelReceiptFromPreviousRunnerAttempt(t *testing.T) {
	store, repo, firstClaim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(
		t, store, repo, firstClaim, "kernel-reconcile-source", "call-kernel-reconcile",
	)
	batchID := toolCallBatchID(firstClaim.StreamUID, operation.SourceEventID)
	batch, found, err := store.GetToolCallBatch(context.Background(), firstClaim.OwnerID, batchID)
	if err != nil || !found {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
	items, err := store.ListToolCallBatchItems(context.Background(), firstClaim.OwnerID, batchID)
	if err != nil || len(items) < 1 || items[0].ToolCallID != operation.ToolCallID {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	batch, err = store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
		Claim: firstClaim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, items[0] = appendToolCallBatchStart(
		t, store, repo, firstClaim, batch, items[0], "kernel-reconcile-start",
	)

	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: firstClaim.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "kernel-reconcile-decision", Scope: "once", Source: "user",
		ActorID: firstClaim.OwnerID, CurrentClaim: firstClaim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: firstClaim.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: approved.StateVersion, Claim: firstClaim,
		BootID: "kernel-reconcile-boot", KernelID: "kernel-reconcile-kernel", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: firstClaim.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: prepared.StateVersion, Claim: firstClaim,
		BootID: "kernel-reconcile-boot", ExecutionID: "kernel-reconcile-execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalResult := json.RawMessage(`{"ok":true,"exec_id":"kernel-reconcile-execution","stdout":"done\\n"}`)
	finished, err := store.FinishKernelLocalOperation(context.Background(), FinishKernelLocalOperationInput{
		OwnerUserID: firstClaim.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: started.StateVersion, Claim: firstClaim,
		BootID: "kernel-reconcile-boot", ExecutionID: "kernel-reconcile-execution",
		TerminalState: KernelLocalOperationStateCompleted, ReasonCode: "execution_completed",
		TerminalResultJSON: terminalResult,
		ExecutionLog: SaveExecutionLogInput{
			Record: ExecutionLogRecord{
				ID: "kernel-reconcile-execution", FrameID: operation.FrameID, CellIndex: 0,
				KernelID: "kernel-reconcile-kernel", CondaEnv: operation.Environment,
				Language: "python", Source: "print('done')", Stdout: "done\n",
				ExitStatus: "completed", Origin: "agent", ExecutedAt: time.Unix(1_700_300_000, 0).UTC(),
			},
			ExpectedOwnerID: firstClaim.OwnerID, ExpectedProjectID: operation.ProjectID,
			ExpectedFrameIncarnationID:     operation.FrameIncarnationID,
			ExpectedRootFrameIncarnationID: operation.RootFrameIncarnationID,
		},
	})
	if err != nil || finished.Operation.State != KernelLocalOperationStateCompleted {
		t.Fatalf("finished=%#v err=%v", finished, err)
	}

	payload, err := json.Marshal(map[string]any{
		"status": ToolCallBatchItemStateCompleted, "toolCallId": operation.ToolCallID,
		"toolPhase": ToolCallBatchItemStateCompleted, "toolResult": json.RawMessage(terminalResult),
	})
	if err != nil {
		t.Fatal(err)
	}
	var terminalEvent transcriptstore.Event
	_, terminalEvent, _, err = repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: firstClaim, ClientMessageID: "kernel-reconcile-terminal-receipt",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			return transcriptstore.RunnerCheckpointCommitReceipt{}, store.CommitKernelLocalOperationProtocolReceiptTx(
				ctx, tx, operation.OperationID, event,
			)
		},
	})
	if err != nil || terminalEvent.EventID <= 0 {
		t.Fatalf("terminal event=%#v err=%v", terminalEvent, err)
	}
	staleBatch, found, err := store.GetToolCallBatch(context.Background(), firstClaim.OwnerID, batchID)
	staleItems, itemsErr := store.ListToolCallBatchItems(context.Background(), firstClaim.OwnerID, batchID)
	if err != nil || itemsErr != nil || !found || staleBatch.State != ToolCallBatchStateRunning ||
		len(staleItems) < 1 || staleItems[0].State != ToolCallBatchItemStateRunning {
		t.Fatalf("pre-reconcile batch=%#v items=%#v found=%t err=%v itemsErr=%v",
			staleBatch, staleItems, found, err, itemsErr)
	}

	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: firstClaim, ClientMessageID: "kernel-reconcile-attempt-failed", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"simulated batch projection conflict"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: firstClaim.StreamUID, OwnerID: firstClaim.OwnerID,
		ClientMessageID: "kernel-reconcile-continue", PayloadJSON: []byte(`{"text":"continue"}`),
	}); err != nil || !created {
		t.Fatalf("continue created=%t err=%v", created, err)
	}
	second, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: firstClaim.StreamUID, OwnerID: firstClaim.OwnerID,
		RunnerID: "kernel-reconcile-runner-2", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !second.Claimed || second.Claim.Attempt <= firstClaim.Attempt {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	claimedBatch, err := store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
		Claim: second.Claim, BatchID: staleBatch.BatchID, ExpectedStateVersion: staleBatch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.ReconcileToolCallBatchFromKernelProtocolReceipt(
		context.Background(), ReconcileKernelToolCallBatchInput{
			Claim: second.Claim, BatchID: claimedBatch.BatchID, Ordinal: 0,
			OperationID: operation.OperationID,
		},
	)
	if err != nil || !reconciled {
		t.Fatalf("reconciled=%t err=%v", reconciled, err)
	}
	settled, found, err := store.GetToolCallBatch(context.Background(), second.Claim.OwnerID, batchID)
	settledItems, itemsErr := store.ListToolCallBatchItems(context.Background(), second.Claim.OwnerID, batchID)
	if err != nil || itemsErr != nil || !found || settled.State != ToolCallBatchStateReady ||
		settled.NextOrdinal != 1 || len(settledItems) < 1 ||
		settledItems[0].State != ToolCallBatchItemStateCompleted ||
		settledItems[0].TerminalEventID != terminalEvent.EventID {
		t.Fatalf("settled batch=%#v items=%#v found=%t err=%v itemsErr=%v",
			settled, settledItems, found, err, itemsErr)
	}
	if replayed, err := store.ReconcileToolCallBatchFromKernelProtocolReceipt(
		context.Background(), ReconcileKernelToolCallBatchInput{
			Claim: second.Claim, BatchID: claimedBatch.BatchID, Ordinal: 0,
			OperationID: operation.OperationID,
		},
	); err != nil || !replayed {
		t.Fatalf("idempotent replay=%t err=%v", replayed, err)
	}
	var executionLogs int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_log WHERE id='kernel-reconcile-execution'`).Scan(&executionLogs); err != nil || executionLogs != 1 {
		t.Fatalf("execution logs=%d err=%v", executionLogs, err)
	}
}

func TestToolCallBatchClaimCASAllowsSingleWinner(t *testing.T) {
	store, repo, claim := newToolCallBatchFixture(t)
	batch, _ := createToolCallBatchForTest(t, store, repo, claim, "tool-batch-claim-race",
		toolCallBatchTestCall{id: "read-1", name: "read_file", arguments: map[string]any{"path": "notes.txt"}},
	)

	results := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			_, err := store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
				Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
			})
			results <- err
		}()
	}
	start.Done()
	successes, stale := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrToolCallBatchStale):
			stale++
		default:
			t.Fatalf("unexpected claim error=%v", err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("successes=%d stale=%d", successes, stale)
	}
}

type toolCallBatchTestCall struct {
	id        string
	name      string
	arguments map[string]any
}

func newToolCallBatchFixture(t *testing.T) (*Store, *transcriptstore.Repository, transcriptstore.RunnerClaim) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "tool-batch-project", UserID: "tool-batch-owner", Name: "Tool Batch"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "tool-batch-frame", ProjectID: "tool-batch-project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:tool-batch-frame", OwnerID: "tool-batch-owner", ExternalID: "tool-batch-frame", SessionID: "tool-batch-frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "tool-batch-project", RootFrameID: frame.RootFrameID, FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "tool-batch-task", FrameEventID: "tool-batch-task-event",
		MessageUUID: "tool-batch-task-message", Text: "Run the complete tool batch.",
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "tool-batch-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	return store, repo, claimed.Claim
}

func writeToolCallBatchExternalizedResult(
	t *testing.T,
	store *Store,
	claim transcriptstore.RunnerClaim,
	item ToolCallBatchItem,
	artifactID string,
	raw []byte,
) map[string]any {
	t.Helper()
	artifact, version, err := store.WriteArtifactVersionRealtime(
		WithMutationIdempotencyKey(context.Background(), "tool-batch-artifact-"+artifactID),
		WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: "tool-batch-project", Name: artifactID + ".json",
			ContentType: "application/json", Content: bytes.NewReader(raw), MaxBytes: 1 << 20,
			CreatedBy: claim.RunnerID, RootFrameID: "tool-batch-frame", FrameID: "tool-batch-frame",
			TranscriptAssociation: &ArtifactTranscriptAssociation{
				StreamUID: claim.StreamUID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: item.StartedEventID, Relation: "produced",
			},
		},
		claim.OwnerID,
	)
	if err != nil {
		t.Fatal(err)
	}
	preview := raw
	if len(preview) > 64 {
		preview = preview[:64]
	}
	return map[string]any{
		"artifact_id": artifact.ID, "version_id": version.ID,
		"sha256": version.ContentSHA256, "size_bytes": version.SizeBytes,
		"content_type": artifact.Kind, "outcome": "succeeded",
		"content_url": "/api/artifacts/" + artifact.ID + "/versions/" + version.ID,
		"preview":     string(preview), "truncated": true,
	}
}

func toolCallBatchRootPayload(t *testing.T, calls ...toolCallBatchTestCall) []byte {
	t.Helper()
	modelCalls := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		modelCalls = append(modelCalls, map[string]any{
			"id": call.id, "type": "function", "name": call.name, "arguments": call.arguments,
		})
	}
	payload, err := json.Marshal(map[string]any{"status": "running", "modelToolCalls": modelCalls})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func createToolCallBatchForTest(
	t *testing.T,
	store *Store,
	repo *transcriptstore.Repository,
	claim transcriptstore.RunnerClaim,
	clientMessageID string,
	calls ...toolCallBatchTestCall,
) (ToolCallBatch, []ToolCallBatchItem) {
	t.Helper()
	var batch ToolCallBatch
	var items []ToolCallBatchItem
	_, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: clientMessageID, Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: toolCallBatchRootPayload(t, calls...),
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var hookErr error
			batch, items, _, hookErr = store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			return toolCallBatchTestCommitReceipt(batch, hookErr)
		},
	})
	if err != nil || batch.BatchID == "" || len(items) != len(calls) {
		t.Fatalf("batch=%#v items=%#v err=%v", batch, items, err)
	}
	return batch, items
}

func toolCallBatchTestCommitReceipt(
	batch ToolCallBatch,
	err error,
) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
	if err != nil {
		return transcriptstore.RunnerCheckpointCommitReceipt{}, err
	}
	return transcriptstore.RunnerCheckpointCommitReceipt{ToolBatch: &transcriptstore.RunnerCheckpointToolBatchReceipt{
		BatchID: batch.BatchID, CallCount: batch.CallCount,
	}}, nil
}

func appendToolCallBatchStart(
	t *testing.T,
	store *Store,
	repo *transcriptstore.Repository,
	claim transcriptstore.RunnerClaim,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	clientMessageID string,
) (ToolCallBatch, ToolCallBatchItem) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"status": "running", "toolCallId": item.ToolCallID, "toolName": item.ToolName, "toolPhase": "start",
	})
	var updatedBatch ToolCallBatch
	var updatedItem ToolCallBatchItem
	_, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: clientMessageID, Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var hookErr error
			updatedBatch, updatedItem, hookErr = store.StartToolCallBatchItemTx(ctx, tx, StartToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: item.Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: item.StateVersion,
				StartedEvent: event,
			})
			return transcriptstore.RunnerCheckpointCommitReceipt{}, hookErr
		},
	})
	if err != nil {
		t.Fatalf("start batch=%s ordinal=%d: %v", batch.BatchID, item.Ordinal, err)
	}
	return updatedBatch, updatedItem
}

func appendToolCallBatchWait(
	t *testing.T,
	store *Store,
	repo *transcriptstore.Repository,
	claim transcriptstore.RunnerClaim,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	clientMessageID string,
) (ToolCallBatch, ToolCallBatchItem) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"status": "waiting", "toolCallId": item.ToolCallID, "toolPhase": "waiting",
	})
	var updatedBatch ToolCallBatch
	var updatedItem ToolCallBatchItem
	_, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: clientMessageID, Phase: transcriptstore.RunnerPhaseWaitingApproval,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var hookErr error
			updatedBatch, updatedItem, hookErr = store.WaitToolCallBatchItemTx(ctx, tx, WaitToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: item.Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: item.StateVersion,
				WaitingEvent: event,
			})
			return transcriptstore.RunnerCheckpointCommitReceipt{}, hookErr
		},
	})
	if err != nil {
		t.Fatalf("wait batch=%s ordinal=%d: %v", batch.BatchID, item.Ordinal, err)
	}
	return updatedBatch, updatedItem
}

func appendToolCallBatchTerminal(
	t *testing.T,
	store *Store,
	repo *transcriptstore.Repository,
	claim transcriptstore.RunnerClaim,
	batch ToolCallBatch,
	item ToolCallBatchItem,
	clientMessageID, terminalState string,
	result map[string]any,
	resultRef string,
) (ToolCallBatch, ToolCallBatchItem) {
	t.Helper()
	phase := terminalState
	payload, _ := json.Marshal(map[string]any{
		"status": terminalState, "toolCallId": item.ToolCallID, "toolPhase": phase, "toolResult": result,
	})
	var updatedBatch ToolCallBatch
	var updatedItem ToolCallBatchItem
	_, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: clientMessageID, Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var hookErr error
			updatedBatch, updatedItem, hookErr = store.FinishToolCallBatchItemTx(ctx, tx, FinishToolCallBatchItemInput{
				Claim: claim, BatchID: batch.BatchID, Ordinal: item.Ordinal,
				ExpectedBatchStateVersion: batch.StateVersion, ExpectedItemStateVersion: item.StateVersion,
				TerminalEvent: event, TerminalState: terminalState, ResultRef: resultRef,
			})
			return transcriptstore.RunnerCheckpointCommitReceipt{}, hookErr
		},
	})
	if err != nil {
		t.Fatalf("terminal batch=%s ordinal=%d: %v", batch.BatchID, item.Ordinal, err)
	}
	return updatedBatch, updatedItem
}

func (call toolCallBatchTestCall) String() string {
	return fmt.Sprintf("%s:%s", call.id, call.name)
}
