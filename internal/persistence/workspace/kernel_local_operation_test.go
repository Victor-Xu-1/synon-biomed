package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/software"
)

func TestKernelLocalOperationOriginCommitsAndRollsBackWithCheckpoint(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	payload := kernelLocalOperationCheckpointPayload(t, "call-python", "print(1)")
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "unfenced-tool-batch", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
	}); err == nil {
		t.Fatal("kernel tool-call checkpoint bypassed the durable operation hook")
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "empty-receipt-tool-batch", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(context.Context, *transcriptstore.ImmediateTransaction, transcriptstore.Event, bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			return transcriptstore.RunnerCheckpointCommitReceipt{}, nil
		},
	}); err == nil {
		t.Fatal("kernel tool-call checkpoint accepted an empty durable commit receipt")
	}
	var committed []KernelLocalOperation
	checkpoint, event, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-batch-1", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, created bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			if !created {
				t.Fatal("first checkpoint hook was not created")
			}
			batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			committed, err = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, committed), err
		},
	})
	if err != nil || !created || checkpoint.EventID <= 0 || event.EventID != checkpoint.EventID {
		t.Fatalf("checkpoint=%#v event=%#v created=%t err=%v", checkpoint, event, created, err)
	}
	if len(committed) != 1 {
		t.Fatalf("operations=%#v", committed)
	}
	operation := committed[0]
	if operation.OperationID != kernelLocalOperationID(event.StreamUID, event.EventID, 0) ||
		operation.State != KernelLocalOperationStatePendingApproval || operation.StateVersion != 1 ||
		operation.Tool != "python" || operation.Environment != "science" || operation.ToolCallID != "call-python" ||
		operation.SourceRunnerAttempt != claim.Attempt || operation.SourceClientMessageID != "tool-batch-1" ||
		operation.FrameIncarnationID == "" || operation.RootFrameIncarnationID == "" || operation.ConfinementSHA256 != "" {
		t.Fatalf("operation=%#v", operation)
	}
	stored, found, err := store.GetKernelLocalOperation(context.Background(), "owner", operation.OperationID)
	if err != nil || !found || !kernelLocalOperationOriginMatches(stored, operation) {
		t.Fatalf("stored=%#v found=%t err=%v", stored, found, err)
	}
	byApproval, found, err := store.GetKernelLocalOperationByApprovalRequest(
		context.Background(), "owner", operation.ApprovalRequestID,
	)
	if err != nil || !found || byApproval.OperationID != operation.OperationID {
		t.Fatalf("approval operation=%#v found=%t err=%v", byApproval, found, err)
	}
	byToolCall, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), "owner", operation.StreamUID, operation.ToolCallID,
	)
	if err != nil || !found || byToolCall.OperationID != operation.OperationID {
		t.Fatalf("tool-call operation=%#v found=%t err=%v", byToolCall, found, err)
	}
	if _, found, err := store.GetKernelLocalOperation(context.Background(), "foreign-owner", operation.OperationID); err != nil || found {
		t.Fatalf("foreign found=%t err=%v", found, err)
	}
	if _, found, err := store.GetKernelLocalOperationByApprovalRequest(
		context.Background(), "foreign-owner", operation.ApprovalRequestID,
	); err != nil || found {
		t.Fatalf("foreign approval found=%t err=%v", found, err)
	}
	if _, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), "foreign-owner", operation.StreamUID, operation.ToolCallID,
	); err != nil || found {
		t.Fatalf("foreign tool call found=%t err=%v", found, err)
	}
	var transitions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_transitions WHERE operation_id=?`, operation.OperationID).Scan(&transitions); err != nil || transitions != 1 {
		t.Fatalf("transitions=%d err=%v", transitions, err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_branch_state SET generation=generation+1 WHERE stream_uid=?`, event.StreamUID); err != nil {
		t.Fatal(err)
	}

	var replayed []KernelLocalOperation
	_, replayEvent, replayCreated, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-batch-1", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, created bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			if created {
				t.Fatal("replayed checkpoint was recreated")
			}
			batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			replayed, err = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, replayed), err
		},
	})
	if err != nil || replayCreated || replayEvent.EventID != event.EventID || len(replayed) != 1 || replayed[0].OperationID != operation.OperationID {
		t.Fatalf("replay event=%#v created=%t operations=%#v err=%v", replayEvent, replayCreated, replayed, err)
	}
	var operationCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations WHERE stream_uid=? AND source_event_id=?`,
		event.StreamUID, event.EventID).Scan(&operationCount); err != nil || operationCount != 1 {
		t.Fatalf("branch-switch replay operation count=%d err=%v", operationCount, err)
	}

	rollbackMarker := errors.New("rollback after dependent authority")
	_, rolledBackEvent, rolledBackCreated, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-batch-rollback", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: kernelLocalOperationCheckpointPayload(t, "call-rollback", "print(2)"),
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, created bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			if !created {
				t.Fatal("rollback checkpoint was not created inside transaction")
			}
			if _, _, _, createErr := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event); createErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, createErr
			}
			operations, createErr := store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			if createErr != nil || len(operations) != 1 {
				t.Fatalf("rollback operations=%#v err=%v", operations, createErr)
			}
			return transcriptstore.RunnerCheckpointCommitReceipt{}, rollbackMarker
		},
	})
	if !errors.Is(err, rollbackMarker) || rolledBackCreated || rolledBackEvent.EventID != 0 {
		t.Fatalf("rollback event=%#v created=%t err=%v", rolledBackEvent, rolledBackCreated, err)
	}
	var rolledBackEvents, rolledBackOperations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND client_message_id='tool-batch-rollback'`, claim.StreamUID).Scan(&rolledBackEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations WHERE tool_call_id='call-rollback'`).Scan(&rolledBackOperations); err != nil {
		t.Fatal(err)
	}
	if rolledBackEvents != 0 || rolledBackOperations != 0 {
		t.Fatalf("rolled back event=%d operation=%d", rolledBackEvents, rolledBackOperations)
	}
}

func TestCountActiveKernelLocalOperationsIgnoresPausedContinuationReceipt(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	payload := kernelLocalOperationCheckpointPayload(t, "call-active-count", "print(1)")
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "active-count-checkpoint", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			operations, err := store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), err
		},
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountActiveKernelLocalOperations(context.Background()); err != nil || count != 1 {
		t.Fatalf("running operation count=%d err=%v", count, err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountActiveKernelLocalOperations(context.Background()); err != nil || count != 0 {
		t.Fatalf("expired operation count=%d err=%v", count, err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim, ClientMessageID: "pause-active-count", Status: "cancelled",
		PayloadJSON: []byte(`{"status":"cancelled"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountActiveKernelLocalOperations(context.Background()); err != nil || count != 0 {
		t.Fatalf("paused continuation receipt count=%d err=%v", count, err)
	}
}

func TestKernelLocalOperationInputUsesClosedPerToolContract(t *testing.T) {
	repl, environment, err := canonicalKernelLocalOperationInput("repl", json.RawMessage(`{"code":"print(1)","fresh":true}`))
	if err != nil || environment != "repl" || string(repl) != `{"code":"print(1)","fresh":true}` {
		t.Fatalf("repl=%s environment=%q err=%v", repl, environment, err)
	}
	python, environment, err := canonicalKernelLocalOperationInput("python", json.RawMessage(`{"environment":"science","code":"print(1)","background":true}`))
	if err != nil || environment != "science" || string(python) != `{"background":true,"code":"print(1)","environment":"science"}` {
		t.Fatalf("python=%s environment=%q err=%v", python, environment, err)
	}
	softwareRaw := json.RawMessage(`{"capability":"sequence-alignment","language":"native","packages":[{"manager":"conda","spec":"minimap2"}],"executable":"minimap2"}`)
	softwareInput, environment, err := canonicalKernelLocalOperationInput("software_runtime", softwareRaw)
	if err != nil || !strings.HasPrefix(environment, "swr-") || strings.Contains(string(softwareInput), `"timeout_seconds"`) {
		t.Fatalf("software=%s environment=%q err=%v", softwareInput, environment, err)
	}
	request, err := software.DecodeRequestJSON(softwareInput)
	if err != nil {
		t.Fatal(err)
	}
	wantEnvironment, err := software.EnvironmentName(software.LocalProviderID, request)
	if err != nil || environment != wantEnvironment {
		t.Fatalf("software environment=%q want=%q err=%v", environment, wantEnvironment, err)
	}
	for name, raw := range map[string]json.RawMessage{
		"unknown":    []byte(`{"capability":"x","language":"native","packages":[{"manager":"conda","spec":"x"}],"executable":"x","command":"x --help"}`),
		"shell_path": []byte(`{"capability":"x","language":"native","packages":[{"manager":"conda","spec":"x"}],"executable":"bin/x"}`),
		"provider":   []byte(`{"capability":"x","provider":"fallback-provider","language":"native","packages":[{"manager":"conda","spec":"x"}],"executable":"x"}`),
	} {
		if _, _, err := canonicalKernelLocalOperationInput("software_runtime", raw); err == nil {
			t.Fatalf("unsafe software input %s was accepted", name)
		}
	}
	for name, raw := range map[string]json.RawMessage{
		"duplicate":      []byte(`{"code":"a","code":"b","environment":"science"}`),
		"unknown":        []byte(`{"code":"a","environment":"science","timeout":0}`),
		"wrong_type":     []byte(`{"code":"a","environment":"science","background":"true"}`),
		"repl_env":       []byte(`{"code":"a","environment":"science"}`),
		"invalid_utf8":   append([]byte(`{"code":"`), append([]byte{0xff}, []byte(`","environment":"science"}`)...)...),
		"trailing_value": []byte(`{"code":"a","environment":"science"} {}`),
	} {
		tool := "python"
		if name == "repl_env" {
			tool = "repl"
		}
		if _, _, err := canonicalKernelLocalOperationInput(tool, raw); err == nil {
			t.Fatalf("%s input was accepted: %q", name, raw)
		}
	}
}

func TestSoftwareRuntimeCallCommitsToKernelOperationLedger(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	payload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": "call-software", "type": "function", "name": "software_runtime",
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
	_, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "software-tool-batch", Phase: transcriptstore.RunnerPhaseExecuting,
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
	if err != nil || !created || len(operations) != 1 || operations[0].Tool != "software_runtime" ||
		!strings.HasPrefix(operations[0].Environment, "swr-") || operations[0].State != KernelLocalOperationStatePendingApproval {
		t.Fatalf("software operation=%#v created=%t err=%v", operations, created, err)
	}
}

func TestRejectedKernelToolCallRemainsDurableWithoutExecutionOperation(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	payload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": "invalid-r-import", "type": "function", "name": "software_runtime",
			"arguments": map[string]any{
				"capability": "r-summary", "language": "r", "executable": "Rscript",
				"packages": []any{map[string]any{"manager": "conda", "spec": "r-jsonlite=2.0.0"}},
				"imports":  []any{"jsonlite"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var batch ToolCallBatch
	var operations []KernelLocalOperation
	_, event, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "invalid-tool-batch", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var createErr error
			batch, _, _, createErr = store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if createErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, createErr
			}
			operations, createErr = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), createErr
		},
	})
	if err != nil || !created || event.EventID <= 0 || batch.CallCount != 1 || len(operations) != 0 {
		t.Fatalf("event=%#v created=%t batch=%#v operations=%#v err=%v", event, created, batch, operations, err)
	}
	var itemCount, operationCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_tool_call_items WHERE batch_id=?`, batch.BatchID).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations WHERE stream_uid=? AND source_event_id=?`,
		event.StreamUID, event.EventID).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if itemCount != 1 || operationCount != 0 {
		t.Fatalf("durable tool items=%d execution operations=%d", itemCount, operationCount)
	}
}

func TestKernelLocalOperationCommitReceiptRejectsGhostOperationAndRollsBack(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	payload := kernelLocalOperationCheckpointPayload(t, "call-source", "print(1)")
	_, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "ghost-operation-batch", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			operations, err := store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			if err != nil || len(operations) != 1 {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO kernel_local_operations SELECT
				?,owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,frame_id,frame_incarnation_id,
				stream_uid,branch_id,branch_generation,source_event_id,source_publication_seq,source_runner_attempt,
				source_client_message_id,1,?,tool,environment,input_json,input_sha256,confinement_sha256,state,
				state_version,?,approval_decision_id,approval_decision,approval_scope,approval_source,
				approval_actor_id,decided_at,runner_id,runner_attempt,runner_claim_sha256,boot_id,kernel_id,kernel_generation,
				execution_id,execution_log_id,result_json,result_ref,result_sha256,reason_code,created_at,approved_at,
				prepared_at,started_at,terminal_at,updated_at
			FROM kernel_local_operations WHERE operation_id=?`,
				"ghost-operation", "ghost-call", "approval:ghost-operation", operations[0].OperationID)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), nil
		},
	})
	if err == nil {
		t.Fatal("checkpoint accepted an unreceipted ghost operation")
	}
	var events, operations int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE client_message_id='ghost-operation-batch'`).Scan(&events)
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations WHERE source_client_message_id='ghost-operation-batch'`).Scan(&operations)
	if events != 0 || operations != 0 {
		t.Fatalf("postcondition failure did not rollback event=%d operations=%d", events, operations)
	}
}

func TestKernelLocalOperationOriginRejectsMalformedBatchBeforeCommit(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	payload := kernelLocalOperationCheckpointPayload(t, "duplicate", "print(1)")
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	calls := envelope["modelToolCalls"].([]any)
	envelope["modelToolCalls"] = append(calls, calls[0])
	payload, _ = json.Marshal(envelope)
	_, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "duplicate-batch", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			operations, err := store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), err
		},
	})
	if err == nil {
		t.Fatal("duplicate tool-call identity was accepted")
	}
	var events, operations int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE client_message_id='duplicate-batch'`).Scan(&events)
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations`).Scan(&operations)
	if events != 0 || operations != 0 {
		t.Fatalf("events=%d operations=%d", events, operations)
	}
}

func TestPendingKernelLocalOperationRecoveryWaitsForLiveSourceRunner(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "pending-live", "call-pending-live")
	if operation.State != KernelLocalOperationStatePendingApproval {
		t.Fatalf("operation=%#v", operation)
	}

	pending, err := store.ListPendingKernelLocalOperations(context.Background(), 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("live source runner exposed operation to recovery: pending=%#v err=%v", pending, err)
	}
	var leaseExpiresAt time.Time
	if err := store.db.QueryRow(`SELECT expires_at FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`,
		claim.StreamUID, claim.Attempt).Scan(&leaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	if due, found, err := store.NextKernelLocalOperationRecoveryDue(context.Background(), "boot-current"); err != nil || !found || !due.Equal(leaseExpiresAt.UTC()) {
		t.Fatalf("pending approval recovery due=%s found=%t err=%v want=%s", due, found, err, leaseExpiresAt.UTC())
	}

	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	pending, err = store.ListPendingKernelLocalOperations(context.Background(), 10)
	if err != nil || len(pending) != 1 || pending[0].OperationID != operation.OperationID {
		t.Fatalf("expired source runner did not expose operation to recovery: pending=%#v err=%v", pending, err)
	}
	if due, found, err := store.NextKernelLocalOperationRecoveryDue(context.Background(), "boot-current"); err != nil || found || !due.IsZero() {
		t.Fatalf("expired pending approval recovery due=%s found=%t err=%v", due, found, err)
	}
}

func TestKernelLocalOperationStateMachineFencesPreparedAndStartedWork(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "state-machine", "call-state")
	approvalInput := ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 1,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true,
		DecisionID: "decision-state-machine", Scope: "once", Source: "user", ActorID: "owner",
		CurrentClaim: claim,
	}
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), approvalInput)
	if err != nil || approved.State != KernelLocalOperationStateApproved || approved.StateVersion != 2 ||
		approved.ApprovalRequestID != operation.ApprovalRequestID || approved.ApprovedAt == nil {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	if replay, err := store.ResolveKernelLocalOperationApproval(context.Background(), approvalInput); err != nil || replay.StateVersion != 2 {
		t.Fatalf("approval retry=%#v err=%v", replay, err)
	}
	confinementSHA := strings.Repeat("b", 64)
	for _, invalidSHA := range []string{strings.Repeat("B", 64), strings.Repeat("z", 64), strings.Repeat("b", 63)} {
		if _, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
			OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 2,
			Claim: claim, BootID: "boot-a", KernelID: "kernel-a", KernelGeneration: 1,
			ConfinementSHA256: invalidSHA,
		}); err == nil {
			t.Fatalf("invalid confinement sha was accepted: %q", invalidSHA)
		}
	}
	prepareInput := PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 2,
		Claim:  claim,
		BootID: "boot-a", KernelID: "kernel-a", KernelGeneration: 1, ConfinementSHA256: confinementSHA,
	}
	prepareWake := store.KernelRetentionWake()
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), prepareInput)
	if err != nil || prepared.State != KernelLocalOperationStatePrepared || prepared.StateVersion != 3 ||
		prepared.ConfinementSHA256 != confinementSHA || prepared.PreparedAt == nil {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	select {
	case <-prepareWake:
	default:
		t.Fatal("prepared transition did not wake kernel recovery")
	}
	var preparedLease time.Time
	if err := store.db.QueryRow(`SELECT expires_at FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`,
		claim.StreamUID, claim.Attempt).Scan(&preparedLease); err != nil {
		t.Fatal(err)
	}
	if due, found, err := store.NextKernelLocalOperationRecoveryDue(context.Background(), "boot-current"); err != nil || !found || !due.Equal(preparedLease.UTC()) {
		t.Fatalf("prepared recovery due=%s found=%t err=%v want=%s", due, found, err, preparedLease.UTC())
	}
	if replay, err := store.PrepareKernelLocalOperation(context.Background(), prepareInput); err != nil || replay.StateVersion != 3 {
		t.Fatalf("prepare retry=%#v err=%v", replay, err)
	}
	wrongClaim := claim
	wrongClaim.ClaimToken = "wrong-claim-token"
	if _, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 3,
		Claim:  wrongClaim,
		BootID: "boot-a", ExecutionID: "execution-a",
	}); err == nil {
		t.Fatalf("wrong claim start error=%v", err)
	}
	if _, err := store.ReclaimPreparedKernelLocalOperation(context.Background(), ReclaimPreparedKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 3,
		RunnerID: claim.RunnerID, RunnerAttempt: claim.Attempt,
		RunnerClaimSHA256: kernelLocalOperationClaimSHA256(claim.ClaimToken), BootID: "boot-a",
		ReasonCode: "runner_restarted_before_start",
	}); !errors.Is(err, ErrKernelLocalOperationStale) {
		t.Fatalf("live prepared reclaim error=%v", err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if due, found, err := store.NextKernelLocalOperationRecoveryDue(context.Background(), "boot-current"); err != nil || found || !due.IsZero() {
		t.Fatalf("expired prepared recovery due=%s found=%t err=%v", due, found, err)
	}
	if _, err := store.db.Exec(`UPDATE frames SET status=? WHERE id='frame'`, FrameStatusFailed); err != nil {
		t.Fatal(err)
	}
	reclaimInput := ReclaimPreparedKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 3,
		RunnerID: claim.RunnerID, RunnerAttempt: claim.Attempt,
		RunnerClaimSHA256: kernelLocalOperationClaimSHA256(claim.ClaimToken),
		BootID:            "boot-a", ReasonCode: "runner_restarted_before_start",
	}
	reclaimed, err := store.ReclaimPreparedKernelLocalOperation(context.Background(), reclaimInput)
	if err != nil || reclaimed.State != KernelLocalOperationStateApproved || reclaimed.StateVersion != 4 ||
		reclaimed.RunnerID != "" || reclaimed.ExecutionID != "" || reclaimed.ConfinementSHA256 != confinementSHA {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	var recoveredFrameStatus string
	if err := store.db.QueryRow(`SELECT status FROM frames WHERE id='frame'`).Scan(&recoveredFrameStatus); err != nil {
		t.Fatal(err)
	}
	if recoveredFrameStatus != FrameStatusProcessing {
		t.Fatalf("recovered frame status=%q want=%q", recoveredFrameStatus, FrameStatusProcessing)
	}
	if replay, err := store.ReclaimPreparedKernelLocalOperation(context.Background(), reclaimInput); err != nil || replay.StateVersion != 4 {
		t.Fatalf("reclaim retry=%#v err=%v", replay, err)
	}
	claimedAgain, err := repo.ClaimNextRunner(context.Background(), transcriptstore.ClaimNextRunnerInput{
		RunnerID: "runner-b", TTL: time.Minute,
	})
	if err != nil || !claimedAgain.Claimed {
		var inputRevision, consumedRevision int64
		var attemptStatus, frameStatus string
		var expiresAt time.Time
		_ = store.db.QueryRow(`SELECT input_revision,consumed_input_revision FROM transcript_streams WHERE stream_uid=?`, claim.StreamUID).
			Scan(&inputRevision, &consumedRevision)
		_ = store.db.QueryRow(`SELECT status,expires_at FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`, claim.StreamUID, claim.Attempt).
			Scan(&attemptStatus, &expiresAt)
		_ = store.db.QueryRow(`SELECT status FROM frames WHERE id='frame'`).Scan(&frameStatus)
		t.Fatalf("reclaim runner=%#v err=%v stream=%d/%d attempt=%s expires=%s frame=%s now=%s",
			claimedAgain, err, inputRevision, consumedRevision, attemptStatus, expiresAt, frameStatus, time.Now())
	}
	claim = claimedAgain.Claim
	prepareInput = PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 4,
		Claim:  claim,
		BootID: "boot-b", KernelID: "kernel-b", KernelGeneration: 2, ConfinementSHA256: confinementSHA,
	}
	prepared, err = store.PrepareKernelLocalOperation(context.Background(), prepareInput)
	if err != nil || prepared.StateVersion != 5 || prepared.RunnerID != "runner-b" {
		t.Fatalf("reprepared=%#v err=%v", prepared, err)
	}
	if replay, err := store.PrepareKernelLocalOperation(context.Background(), prepareInput); err != nil || replay.StateVersion != 5 {
		t.Fatalf("reprepare retry=%#v err=%v", replay, err)
	}
	startInput := StartKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 5,
		Claim:  claim,
		BootID: "boot-b", ExecutionID: "execution-b",
	}
	startWake := store.KernelRetentionWake()
	started, err := store.StartKernelLocalOperation(context.Background(), startInput)
	if err != nil || started.State != KernelLocalOperationStateStarted || started.StateVersion != 6 || started.StartedAt == nil {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	select {
	case <-startWake:
	default:
		t.Fatal("started transition did not wake kernel recovery")
	}
	if due, found, err := store.NextKernelLocalOperationRecoveryDue(context.Background(), "boot-b"); err != nil || found || !due.IsZero() {
		t.Fatalf("current-boot started recovery due=%s found=%t err=%v", due, found, err)
	}
	if due, found, err := store.NextKernelLocalOperationRecoveryDue(context.Background(), "boot-current"); err != nil || !found || !due.After(time.Now().UTC()) {
		t.Fatalf("foreign-boot started recovery due=%s found=%t err=%v", due, found, err)
	}
	if replay, err := store.StartKernelLocalOperation(context.Background(), startInput); err != nil || replay.StateVersion != 6 {
		t.Fatalf("start retry=%#v err=%v", replay, err)
	}
	if _, err := store.ReclaimPreparedKernelLocalOperation(context.Background(), ReclaimPreparedKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 6,
		RunnerID: claim.RunnerID, RunnerAttempt: claim.Attempt,
		RunnerClaimSHA256: kernelLocalOperationClaimSHA256(claim.ClaimToken),
		BootID:            "boot-b", ReasonCode: "must_not_replay_started",
	}); !errors.Is(err, ErrKernelLocalOperationStale) {
		t.Fatalf("started reclaim error=%v", err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE frames SET status=? WHERE id='frame'`, FrameStatusFailed); err != nil {
		t.Fatal(err)
	}
	unknownInput := MarkKernelLocalOperationOutcomeUnknownInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 6,
		ExecutionID: "execution-b", CurrentBootID: "boot-current", ReasonCode: "service_restarted_after_start",
	}
	unknown, err := store.MarkKernelLocalOperationOutcomeUnknown(context.Background(), unknownInput)
	if err != nil || unknown.State != KernelLocalOperationStateOutcomeUnknown || unknown.StateVersion != 7 || unknown.TerminalAt == nil {
		t.Fatalf("unknown=%#v err=%v", unknown, err)
	}
	if err := store.db.QueryRow(`SELECT status FROM frames WHERE id='frame'`).Scan(&recoveredFrameStatus); err != nil {
		t.Fatal(err)
	}
	if recoveredFrameStatus != FrameStatusProcessing {
		t.Fatalf("outcome-unknown recovery frame status=%q want=%q", recoveredFrameStatus, FrameStatusProcessing)
	}
	if replay, err := store.MarkKernelLocalOperationOutcomeUnknown(context.Background(), unknownInput); err != nil || replay.StateVersion != 7 {
		t.Fatalf("unknown retry=%#v err=%v", replay, err)
	}
	var transitions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_transitions WHERE operation_id=?`, operation.OperationID).Scan(&transitions); err != nil || transitions != 7 {
		t.Fatalf("transitions=%d err=%v", transitions, err)
	}
	if _, err := store.db.Exec(`UPDATE kernel_local_operations SET state='approved',state_version=8 WHERE operation_id=?`, operation.OperationID); err == nil {
		t.Fatal("database accepted an invalid terminal-to-approved transition")
	}
}

func TestKernelOperationTerminalFrameDoesNotReenterRecovery(t *testing.T) {
	for _, status := range []string{FrameStatusCompleted, FrameStatusCancelled, "canceled", " CANCELLED "} {
		if kernelOperationFrameAllowsRecovery(status) {
			t.Fatalf("terminal frame status %q was allowed to reenter recovery", status)
		}
	}
	for _, status := range []string{FrameStatusProcessing, FrameStatusFailed, "running", ""} {
		if !kernelOperationFrameAllowsRecovery(status) {
			t.Fatalf("recoverable frame status %q was rejected", status)
		}
	}
}

func TestOutcomeUnknownSettlementDoesNotRequeueCancelledFrame(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "cancelled-settlement", "call-cancelled")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "decision-cancelled", Scope: "once",
		Source: "user", ActorID: "owner", CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: approved.OwnerUserID, OperationID: approved.OperationID,
		ExpectedStateVersion: approved.StateVersion, Claim: claim,
		BootID: "boot-cancelled", KernelID: "kernel-cancelled", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: prepared.OwnerUserID, OperationID: prepared.OperationID,
		ExpectedStateVersion: prepared.StateVersion, Claim: claim,
		BootID: "boot-cancelled", ExecutionID: "execution-cancelled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE frames SET status=? WHERE id='frame'`, FrameStatusCancelled); err != nil {
		t.Fatal(err)
	}
	var beforeRevision int64
	if err := store.db.QueryRow(`SELECT input_revision FROM transcript_streams WHERE stream_uid=?`, claim.StreamUID).
		Scan(&beforeRevision); err != nil {
		t.Fatal(err)
	}
	settled, err := store.MarkKernelLocalOperationOutcomeUnknown(context.Background(), MarkKernelLocalOperationOutcomeUnknownInput{
		OwnerUserID: started.OwnerUserID, OperationID: started.OperationID,
		ExpectedStateVersion: started.StateVersion, ExecutionID: started.ExecutionID,
		CurrentBootID: "boot-current", ReasonCode: "detached_backend_evidence_lost",
	})
	if err != nil || settled.State != KernelLocalOperationStateOutcomeUnknown {
		t.Fatalf("settled=%#v err=%v", settled, err)
	}
	var afterRevision int64
	var frameStatus string
	if err := store.db.QueryRow(`SELECT input_revision FROM transcript_streams WHERE stream_uid=?`, claim.StreamUID).
		Scan(&afterRevision); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT status FROM frames WHERE id='frame'`).Scan(&frameStatus); err != nil {
		t.Fatal(err)
	}
	if afterRevision != beforeRevision || frameStatus != FrameStatusCancelled {
		t.Fatalf("cancelled settlement revision=%d->%d frame=%q", beforeRevision, afterRevision, frameStatus)
	}
}

func TestFailApprovedKernelLocalOperationMaterializesPrestartFailure(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "prestart-failure", "call-prestart-failure")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "decision-prestart-failure", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claim,
	})
	if err != nil || approved.State != KernelLocalOperationStateApproved {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	terminalResult := json.RawMessage(`{"ok":false,"error":"working directory is outside the authorized workspace"}`)
	failed, err := store.FailApprovedKernelLocalOperation(context.Background(), FailApprovedKernelLocalOperationInput{
		OwnerUserID: approved.OwnerUserID, OperationID: approved.OperationID,
		ExpectedStateVersion: approved.StateVersion, Claim: claim,
		ReasonCode: "execution_preflight_failed", TerminalResultJSON: terminalResult,
	})
	if err != nil || failed.State != KernelLocalOperationStateCancelled || failed.ExecutionID != "" ||
		failed.TerminalAt == nil || failed.ReasonCode != "execution_preflight_failed" {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	materialized, found, err := store.GetKernelToolResultMaterialization(context.Background(), failed.OperationID)
	var gotResult, wantResult any
	gotErr := json.Unmarshal(materialized.TerminalResultJSON, &gotResult)
	wantErr := json.Unmarshal(terminalResult, &wantResult)
	if err != nil || !found || gotErr != nil || wantErr != nil || !reflect.DeepEqual(gotResult, wantResult) ||
		materialized.ExecutionLogSHA256 != "" {
		t.Fatalf("materialized=%#v found=%t err=%v", materialized, found, err)
	}
	replayed, err := store.FailApprovedKernelLocalOperation(context.Background(), FailApprovedKernelLocalOperationInput{
		OwnerUserID: approved.OwnerUserID, OperationID: approved.OperationID,
		ExpectedStateVersion: approved.StateVersion, Claim: claim,
		ReasonCode: "execution_preflight_failed", TerminalResultJSON: terminalResult,
	})
	if err != nil || replayed.StateVersion != failed.StateVersion {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
}

func TestExpiredApprovedKernelOperationIsRecoverableWithoutReplayingExecution(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "approved-expiry", "call-approved-expiry")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "decision-approved-expiry", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claim,
	})
	if err != nil || approved.State != KernelLocalOperationStateApproved || approved.ExecutionID != "" {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	if candidates, err := store.ListExpiredApprovedKernelLocalOperationRecoveryCandidates(context.Background(), 10); err != nil || len(candidates) != 0 {
		t.Fatalf("live approved candidates=%#v err=%v", candidates, err)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=?
		WHERE stream_uid=? AND attempt=?`, store.now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListExpiredApprovedKernelLocalOperationRecoveryCandidates(context.Background(), 10)
	if err != nil || len(candidates) != 1 || candidates[0].OperationID != approved.OperationID ||
		candidates[0].State != KernelLocalOperationStateApproved || candidates[0].ExecutionID != "" {
		t.Fatalf("expired approved candidates=%#v err=%v", candidates, err)
	}
}

func TestExpiredApprovedKernelOperationPagesDoNotStarveOlderOperations(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	const total = 25
	for index := 0; index < total; index++ {
		operation := createKernelLocalOperationForTest(
			t, store, repo, claim, fmt.Sprintf("approved-page-%02d", index), fmt.Sprintf("call-approved-page-%02d", index),
		)
		approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
			OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
			ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
			Approved: true, DecisionID: fmt.Sprintf("decision-approved-page-%02d", index), Scope: "once",
			Source: "policy", ActorID: "system", CurrentClaim: claim,
		})
		if err != nil || approved.State != KernelLocalOperationStateApproved {
			t.Fatalf("operation %d approved=%#v err=%v", index, approved, err)
		}
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=?
		WHERE stream_uid=? AND attempt=?`, store.now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]struct{}, total)
	for offset := 0; ; {
		page, err := store.ListExpiredApprovedKernelLocalOperationRecoveryCandidatesPage(
			context.Background(), offset, 7,
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range page {
			if _, duplicate := seen[operation.OperationID]; duplicate {
				t.Fatalf("operation %s repeated across pages", operation.OperationID)
			}
			seen[operation.OperationID] = struct{}{}
		}
		offset += len(page)
		if len(page) < 7 {
			break
		}
	}
	if len(seen) != total {
		t.Fatalf("paged operations=%d want=%d", len(seen), total)
	}
}

func TestReleasePreparedKernelLocalOperationPreservesAdmittedCallForRetry(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "prepared-release", "call-prepared-release")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "decision-prepared-release", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: approved.OwnerUserID, OperationID: approved.OperationID,
		ExpectedStateVersion: approved.StateVersion, Claim: claim,
		BootID: "boot-prepared-release", KernelID: "kernel-prepared-release", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	releaseInput := ReleasePreparedKernelLocalOperationInput{
		OwnerUserID: prepared.OwnerUserID, OperationID: prepared.OperationID,
		ExpectedStateVersion: prepared.StateVersion, Claim: claim,
		BootID: "boot-prepared-release", ReasonCode: "infrastructure_setup_interrupted",
	}
	released, err := store.ReleasePreparedKernelLocalOperation(context.Background(), releaseInput)
	if err != nil || released.State != KernelLocalOperationStateApproved ||
		released.StateVersion != prepared.StateVersion+1 || released.RunnerID != "" ||
		released.RunnerAttempt != 0 || released.RunnerClaimSHA256 != "" || released.BootID != "" ||
		released.KernelID != "" || released.KernelGeneration != 0 || released.PreparedAt != nil ||
		released.ExecutionID != "" || released.ConfinementSHA256 != prepared.ConfinementSHA256 ||
		released.AdmittedInputRevision != prepared.AdmittedInputRevision {
		t.Fatalf("released=%#v err=%v", released, err)
	}
	runnable, err := store.ListRunnableKernelLocalOperations(context.Background(), claim, 10)
	if err != nil || len(runnable) != 1 || runnable[0].OperationID != released.OperationID {
		t.Fatalf("runnable=%#v err=%v", runnable, err)
	}
	replayed, err := store.ReleasePreparedKernelLocalOperation(context.Background(), releaseInput)
	if err != nil || replayed.StateVersion != released.StateVersion {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
}

func TestKernelLocalOperationFinishCommitsLogTransitionEventAndReplayAtomically(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "terminal-batch", "call-terminal")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 1,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true,
		DecisionID: "decision-terminal", Scope: "once", Source: "user", ActorID: "owner",
		CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: approved.StateVersion,
		Claim: claim, BootID: "boot-terminal", KernelID: "kernel-terminal", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: prepared.StateVersion,
		Claim: claim, BootID: "boot-terminal", ExecutionID: "execution-terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	finishInput := FinishKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: started.StateVersion,
		Claim: claim, BootID: "boot-terminal", ExecutionID: "execution-terminal",
		TerminalState: KernelLocalOperationStateCompleted, ReasonCode: "execution_completed",
		TerminalResultJSON: json.RawMessage(`{"ok":true,"exec_id":"execution-terminal","stdout":"1\n"}`),
		ExecutionLog: SaveExecutionLogInput{
			Record: ExecutionLogRecord{
				ID: "execution-terminal", FrameID: operation.FrameID, CellIndex: 0, KernelID: "kernel-terminal",
				CondaEnv: operation.Environment, Language: "python", Source: "print(1)", Stdout: "1\n",
				ExitStatus: "completed", Origin: "agent", ExecutedAt: time.Unix(1_700_000_000, 0).UTC(),
			},
			ExpectedOwnerID: "owner", ExpectedProjectID: operation.ProjectID,
			ExpectedFrameIncarnationID:     operation.FrameIncarnationID,
			ExpectedRootFrameIncarnationID: operation.RootFrameIncarnationID,
		},
		Notification: &CreateNotificationInput{
			ID: "kernel-terminal-notification", SenderFrameID: operation.FrameID,
			RecipientFrameID: operation.FrameID, RootFrameID: operation.RootFrameID,
			OwnerUserID: operation.OwnerUserID, NotificationType: "cell_result",
			Payload: map[string]any{"exec_id": "execution-terminal", "status": "completed"},
		},
	}
	finished, err := store.FinishKernelLocalOperation(context.Background(), finishInput)
	if err != nil || !finished.Created || finished.Operation.State != KernelLocalOperationStateCompleted ||
		finished.Operation.StateVersion != started.StateVersion+1 || finished.Operation.ExecutionLogID != "execution-terminal" ||
		finished.Operation.ExecutionLogRef != "execution-log:execution-terminal" || !validLowerHexSHA256(finished.Operation.ResultSHA256) ||
		finished.Event.Type != "kernel_local_operation_terminal" ||
		finished.Notification.ID != "kernel-terminal-notification" || finished.NotificationEvent.ID == "" {
		t.Fatalf("finished=%#v err=%v", finished, err)
	}
	if log, found, err := store.GetExecutionLog(operation.FrameID, "execution-terminal"); err != nil || !found ||
		log.Stdout != "1\n" || log.Source != "print(1)" {
		t.Fatalf("log=%#v found=%t err=%v", log, found, err)
	}
	materialized, found, err := store.GetKernelToolResultMaterialization(context.Background(), operation.OperationID)
	if err != nil || !found || materialized.ResultRef != "" ||
		string(materialized.TerminalResultJSON) != `{"exec_id":"execution-terminal","ok":true,"stdout":"1\n"}` ||
		!validLowerHexSHA256(materialized.TerminalResultSHA256) || !validLowerHexSHA256(materialized.ExecutionLogSHA256) {
		t.Fatalf("materialized=%#v found=%t err=%v", materialized, found, err)
	}
	if replay, err := store.FinishKernelLocalOperation(context.Background(), finishInput); err != nil || replay.Created ||
		replay.Operation.StateVersion != finished.Operation.StateVersion || replay.Event.ID != finished.Event.ID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	var logs, events, transitions, notifications int
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM execution_log WHERE id='execution-terminal'`).Scan(&logs)
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM frame_events WHERE id=?`, finished.Event.ID).Scan(&events)
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operation_transitions WHERE operation_id=?`, operation.OperationID).Scan(&transitions)
	_ = store.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id='kernel-terminal-notification'`).Scan(&notifications)
	if logs != 1 || events != 1 || transitions != 5 || notifications != 1 {
		t.Fatalf("logs=%d events=%d transitions=%d notifications=%d", logs, events, transitions, notifications)
	}
}

func TestKernelLocalOperationApprovalDenialIsTerminalAndDoesNotPrepare(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "denied", "call-denied")
	denied, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 1,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: false,
		DecisionID: "decision-denied", Scope: "once", Source: "user", ActorID: "owner",
		AdmitRunnerRevision: true,
	})
	if err != nil || denied.State != KernelLocalOperationStateFailed || denied.StateVersion != 2 ||
		denied.ReasonCode != "approval_denied" || denied.TerminalAt == nil {
		t.Fatalf("denied=%#v err=%v", denied, err)
	}
	if _, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: 2,
		Claim:  claim,
		BootID: "boot", KernelID: "kernel", KernelGeneration: 1, ConfinementSHA256: strings.Repeat("b", 64),
	}); !errors.Is(err, ErrKernelLocalOperationStale) {
		t.Fatalf("denied prepare error=%v", err)
	}
}

func TestListRunnableKernelLocalOperationsRequiresRunnableToolBatch(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "terminal-batch", "call-terminal-batch")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "terminal-batch-decision", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claim,
	})
	if err != nil || approved.State != KernelLocalOperationStateApproved {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	operations, err := store.ListRunnableKernelLocalOperations(context.Background(), claim, 10)
	if err != nil || len(operations) != 1 || operations[0].OperationID != approved.OperationID {
		t.Fatalf("initial operations=%#v err=%v", operations, err)
	}

	batchID := toolCallBatchID(claim.StreamUID, operation.SourceEventID)
	result, err := store.db.Exec(`UPDATE transcript_tool_call_batches SET
		state='outcome_unknown',state_version=state_version+1,reason_code='terminal_frame_orphaned_tool',updated_at=?
		WHERE batch_id=? AND state='ready'`, store.now().UTC(), batchID)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("rows=%d err=%v", rows, err)
	}
	operations, err = store.ListRunnableKernelLocalOperations(context.Background(), claim, 10)
	if err != nil || len(operations) != 0 {
		t.Fatalf("terminal batch leaked runnable operations=%#v err=%v", operations, err)
	}
}

func TestKernelLocalOperationExternalDecisionIsVisibleOnlyToTheNewClaim(t *testing.T) {
	for _, approved := range []bool{true, false} {
		t.Run(map[bool]string{true: "allow", false: "deny"}[approved], func(t *testing.T) {
			store, repo, claim := newKernelLocalOperationFixture(t)
			operation := createKernelLocalOperationForTest(t, store, repo, claim, "external-decision", "call-external")
			pausePayload, _ := json.Marshal(map[string]any{
				"status": "awaiting_approval", "operation_id": operation.OperationID,
				"request_id": operation.ApprovalRequestID,
			})
			pauseCheckpoint, _, _, err := repo.PauseRunnerForApproval(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
				Claim: claim, ClientMessageID: "external-decision-pause", Phase: transcriptstore.RunnerPhaseWaitingApproval,
				Resumable: true, PayloadJSON: pausePayload,
			})
			if err != nil || pauseCheckpoint.Sequence <= 0 {
				t.Fatal(err)
			}
			reason := ""
			if !approved {
				reason = "approval_denied"
			}
			resolved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
				OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: operation.StateVersion,
				ApprovalRequestID: operation.ApprovalRequestID, Approved: approved,
				DecisionID: "external-decision-id", Scope: "once", Source: "user", ActorID: "owner",
				ReasonCode: reason, AdmitRunnerRevision: true,
			})
			if err != nil || resolved.AdmittedInputRevision <= claim.ClaimedInputRevision {
				t.Fatalf("resolved=%#v err=%v", resolved, err)
			}
			if operations, err := store.ListRunnableKernelLocalOperations(context.Background(), claim, 10); err == nil || len(operations) != 0 {
				t.Fatalf("old claim operations=%#v err=%v", operations, err)
			}
			reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
				StreamUID: claim.StreamUID, OwnerID: claim.OwnerID, RunnerID: "runner-external-resume",
				TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
				ResumeCheckpoint: pauseCheckpoint.Sequence,
			})
			if err != nil || !reclaimed.Claimed {
				t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
			}
			operations, err := store.ListRunnableKernelLocalOperations(context.Background(), reclaimed.Claim, 10)
			if err != nil || len(operations) != 1 || operations[0].OperationID != operation.OperationID {
				t.Fatalf("new claim operations=%#v err=%v", operations, err)
			}
			if approved && operations[0].State != KernelLocalOperationStateApproved {
				t.Fatalf("approved operation=%#v", operations[0])
			}
			if !approved && (operations[0].State != KernelLocalOperationStateFailed || len(operations[0].ResultJSON) == 0) {
				t.Fatalf("denied operation=%#v", operations[0])
			}
		})
	}
}

func TestKernelLocalOperationBackgroundRecoveryExcludesLegacyLostDetection(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "background-recovery", "call-background")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: operation.StateVersion,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true,
		DecisionID: "decision-background", Scope: "once", Source: "user", ActorID: "owner", CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: approved.StateVersion,
		Claim: claim, BootID: "boot-background", KernelID: "kernel-background", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: prepared.StateVersion,
		Claim: claim, BootID: "boot-background", ExecutionID: "execution-background",
	})
	if err != nil || started.State != KernelLocalOperationStateStarted {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	access, found, err := store.GetKernelFrameAccess("frame")
	if err != nil || !found {
		t.Fatalf("access=%#v found=%t err=%v", access, found, err)
	}
	execution := BackgroundKernelExecution{
		ExecID: started.ExecutionID, ToolID: operation.ToolCallID, ToolName: operation.Tool,
		FrameID: operation.FrameID, RootFrameID: operation.RootFrameID,
		FrameIncarnationID:     operation.FrameIncarnationID,
		RootFrameIncarnationID: operation.RootFrameIncarnationID, StartedAt: time.Now().UTC(),
	}
	if _, err := store.RecordBackgroundKernelExecutionStarted(context.Background(), access, execution); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.ListPendingBackgroundKernelExecutions(context.Background(), access); err != nil ||
		len(pending) != 1 || pending[0].ExecID != execution.ExecID {
		t.Fatalf("operation-backed pending=%#v err=%v", pending, err)
	}
	if event, marked, err := store.MarkBackgroundKernelExecutionLostIfPending(context.Background(), access, execution); err != nil || marked || event.ID != "" {
		t.Fatalf("operation-backed legacy loss event=%#v marked=%t err=%v", event, marked, err)
	}
	var lostEvents, notifications int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM frame_events
		WHERE frame_id=? AND event_type='kernel_execution_background_lost'`, operation.FrameID).Scan(&lostEvents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE recipient_frame_id=?`, operation.FrameID).
		Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if lostEvents != 0 || notifications != 0 {
		t.Fatalf("legacy recovery mutated operation-backed execution lost=%d notifications=%d", lostEvents, notifications)
	}
}

func createKernelLocalOperationForTest(
	t *testing.T,
	store *Store,
	repo *transcriptstore.Repository,
	claim transcriptstore.RunnerClaim,
	clientMessageID, callID string,
) KernelLocalOperation {
	t.Helper()
	var operations []KernelLocalOperation
	_, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: clientMessageID, Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: kernelLocalOperationCheckpointPayload(t, callID, "print(1)"),
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, err := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if err != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, err
			}
			operations, err = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), err
		},
	})
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	return operations[0]
}

func kernelLocalOperationCommitReceipt(operations []KernelLocalOperation) transcriptstore.RunnerCheckpointCommitReceipt {
	receipt := transcriptstore.RunnerCheckpointCommitReceipt{
		KernelOperations: make([]transcriptstore.RunnerCheckpointKernelOperationReceipt, 0, len(operations)),
	}
	for _, operation := range operations {
		receipt.KernelOperations = append(receipt.KernelOperations, transcriptstore.RunnerCheckpointKernelOperationReceipt{
			OperationID: operation.OperationID, Ordinal: operation.ToolCallOrdinal,
			ToolCallID: operation.ToolCallID, Tool: operation.Tool, InputSHA256: operation.InputSHA256,
			ApprovalRequestID:   operation.ApprovalRequestID,
			RequestedEventID:    kernelLocalOperationApprovalRequestedEventID(operation.OperationID),
			InitialStateVersion: operation.StateVersion,
		})
	}
	return receipt
}

func kernelLocalOperationCommitReceiptWithBatch(
	batch ToolCallBatch,
	operations []KernelLocalOperation,
) transcriptstore.RunnerCheckpointCommitReceipt {
	receipt := kernelLocalOperationCommitReceipt(operations)
	receipt.ToolBatch = &transcriptstore.RunnerCheckpointToolBatchReceipt{
		BatchID: batch.BatchID, CallCount: batch.CallCount,
	}
	return receipt
}

func newKernelLocalOperationFixture(t *testing.T) (*Store, *transcriptstore.Repository, transcriptstore.RunnerClaim) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return initializeKernelLocalOperationFixture(t, store)
}

func initializeKernelLocalOperationFixture(t *testing.T, store *Store) (*Store, *transcriptstore.Repository, transcriptstore.RunnerClaim) {
	t.Helper()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "owner", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: frame.RootFrameID,
		FrameID: frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "task-event",
		MessageUUID: "task-message", Text: "Analyze data.",
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	return store, repo, claimed.Claim
}

func kernelLocalOperationCheckpointPayload(t *testing.T, callID, code string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{
			map[string]any{
				"id": callID, "type": "function", "name": "python",
				"arguments": map[string]any{"code": code, "environment": "science"},
			},
			map[string]any{
				"id": "read-file-" + callID, "type": "function", "name": "read_file",
				"arguments": map[string]any{"path": "notes.txt"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
