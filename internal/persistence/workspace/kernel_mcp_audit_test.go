package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestKernelMCPAuditIsDurableIdempotentAndRedacted(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	input := KernelMCPAuditInput{
		CallID: "mcp-call-1", FrameID: "root-1", RootFrameID: "root-1", OwnerUserID: "owner-1",
		Server: "private-server-secret", Method: "search", Input: map[string]any{"token": "input-secret", "query": "NEK7"},
	}
	started, err := store.BeginKernelMCPAudit(context.Background(), input)
	if err != nil || started.Type != "kernel_mcp_audit_started" {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	retry, err := store.BeginKernelMCPAudit(context.Background(), input)
	if err != nil || retry.ID != started.ID {
		t.Fatalf("start retry=%#v err=%v", retry, err)
	}
	terminalInput := KernelMCPAuditTerminalInput{KernelMCPAuditInput: input, Status: "completed", Result: map[string]any{"secret": "result-secret"}}
	terminal, err := store.FinishKernelMCPAudit(context.Background(), terminalInput)
	if err != nil || terminal.Type != "kernel_mcp_audit_terminal" {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
	terminalRetry, err := store.FinishKernelMCPAudit(context.Background(), terminalInput)
	if err != nil || terminalRetry.ID != terminal.ID {
		t.Fatalf("terminal retry=%#v err=%v", terminalRetry, err)
	}
	conflict := terminalInput
	conflict.Status = "failed"
	if _, err := store.FinishKernelMCPAudit(context.Background(), conflict); err == nil {
		t.Fatal("conflicting terminal audit succeeded")
	}
	events, err := store.ListFrameEvents("root-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-server-secret", "input-secret", "result-secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("audit leaked %q: %s", forbidden, raw)
		}
	}
}

func TestKernelMCPEvidenceCommitsCheckpointAndTerminalAuditAtomically(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createStartedKernelMCPReplOperationForTest(t, store, repo, claim)
	auditInput := KernelMCPAuditInput{
		CallID: "host-call-1", FrameID: operation.FrameID, RootFrameID: operation.RootFrameID,
		OwnerUserID: operation.OwnerUserID, Server: "pubmed", Method: "search_articles",
		Input: map[string]any{"query": "NEK7", "max_results": 1},
	}
	if _, err := store.BeginKernelMCPAudit(context.Background(), auditInput); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"pmid": "42480804", "title": "Evidence"}
	requestRaw, _ := json.Marshal(auditInput.Input)
	resultRaw, _ := json.Marshal(result)
	inputSchemaRaw, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}})
	requestSHA := digestKernelMCPAuditBytes(requestRaw)
	resultSHA := digestKernelMCPAuditBytes(resultRaw)
	inputSchemaSHA := digestKernelMCPAuditBytes(inputSchemaRaw)
	payload, err := json.Marshal(map[string]any{
		"schema": kernelMCPEvidenceSchemaV1, "status": "completed", "toolPhase": "completed",
		"toolName": "mcp__pubmed__search_articles", "toolCallId": auditInput.CallID,
		"toolInput": auditInput.Input, "toolResult": result,
		"outerToolCallId": operation.ToolCallID, "kernelOperationId": operation.OperationID,
		"executionId": operation.ExecutionID, "hostCallId": auditInput.CallID,
		"kernelId": operation.KernelID, "kernelGeneration": operation.KernelGeneration,
		"requestSha256": requestSHA, "resultSha256": resultSHA,
		"evidenceClass": KernelMCPEvidenceClassBundledReadOnly, "connectorId": "bundled:pubmed",
		"connectorSource": "bundled", "inputSchemaSha256": inputSchemaSHA, "readOnlyHint": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var terminal FrameEvent
	_, event, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "kernel-mcp-evidence-host-call-1",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var commitErr error
			terminal, commitErr = store.CommitKernelMCPEvidenceTx(ctx, tx, event, KernelMCPEvidenceCommitInput{
				Audit:       KernelMCPAuditTerminalInput{KernelMCPAuditInput: auditInput, Status: "completed", Result: result},
				OperationID: operation.OperationID, OuterToolCallID: operation.ToolCallID,
				HostCallID: auditInput.CallID, ExecutionID: operation.ExecutionID,
				ToolName: "mcp__pubmed__search_articles", KernelID: operation.KernelID,
				KernelGeneration: operation.KernelGeneration, Claim: claim,
				RequestSHA256: requestSHA, ResultSHA256: resultSHA,
				EvidenceClass: KernelMCPEvidenceClassBundledReadOnly, ConnectorID: "bundled:pubmed",
				ConnectorSource: "bundled", InputSchemaSHA256: inputSchemaSHA, ReadOnlyHint: true,
			})
			return transcriptstore.RunnerCheckpointCommitReceipt{}, commitErr
		},
	})
	if err != nil || !created || event.EventID <= 0 || terminal.Type != "kernel_mcp_audit_terminal" {
		t.Fatalf("event=%#v created=%t terminal=%#v err=%v", event, created, terminal, err)
	}
	terminalEventID, eventIDOK := terminal.Payload["transcript_event_id"].(int64)
	if !eventIDOK || terminalEventID != event.EventID {
		t.Fatalf("terminal payload=%#v event=%d", terminal.Payload, event.EventID)
	}

	forgedPayload := append([]byte(nil), payload...)
	forgedPayload = []byte(strings.Replace(string(forgedPayload), requestSHA, strings.Repeat("0", 64), 1))
	_, rolledBack, forgedCreated, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "kernel-mcp-evidence-forged",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: forgedPayload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			_, commitErr := store.CommitKernelMCPEvidenceTx(ctx, tx, event, KernelMCPEvidenceCommitInput{
				Audit:       KernelMCPAuditTerminalInput{KernelMCPAuditInput: auditInput, Status: "completed", Result: result},
				OperationID: operation.OperationID, OuterToolCallID: operation.ToolCallID,
				HostCallID: auditInput.CallID, ExecutionID: operation.ExecutionID,
				ToolName: "mcp__pubmed__search_articles", KernelID: operation.KernelID,
				KernelGeneration: operation.KernelGeneration, Claim: claim,
				RequestSHA256: requestSHA, ResultSHA256: resultSHA,
				EvidenceClass: KernelMCPEvidenceClassBundledReadOnly, ConnectorID: "bundled:pubmed",
				ConnectorSource: "bundled", InputSchemaSHA256: inputSchemaSHA, ReadOnlyHint: true,
			})
			return transcriptstore.RunnerCheckpointCommitReceipt{}, commitErr
		},
	})
	if err == nil || forgedCreated || rolledBack.EventID != 0 {
		t.Fatalf("forged event=%#v created=%t err=%v", rolledBack, forgedCreated, err)
	}
	var forgedCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND client_message_id=?`,
		claim.StreamUID, "kernel-mcp-evidence-forged").Scan(&forgedCount); err != nil || forgedCount != 0 {
		t.Fatalf("forged events=%d err=%v", forgedCount, err)
	}
}

func createStartedKernelMCPReplOperationForTest(
	t *testing.T,
	store *Store,
	repo *transcriptstore.Repository,
	claim transcriptstore.RunnerClaim,
) KernelLocalOperation {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": "outer-repl-call", "type": "function", "name": "repl",
			"arguments": map[string]any{"code": "import host\nhost.mcp('pubmed', 'search_articles', query='NEK7')"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var operations []KernelLocalOperation
	_, _, _, err = repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "outer-repl-batch", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: payload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, createErr := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if createErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, createErr
			}
			operations, createErr = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), createErr
		},
	})
	if err != nil || len(operations) != 1 {
		t.Fatalf("operations=%#v err=%v", operations, err)
	}
	operation := operations[0]
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "approve-repl", Scope: "once", Source: "user",
		ActorID: operation.OwnerUserID, CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: approved.StateVersion, Claim: claim, BootID: "boot-repl",
		KernelID: "kernel-repl", KernelGeneration: 1, ConfinementSHA256: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: prepared.StateVersion, Claim: claim, BootID: "boot-repl",
		ExecutionID: "cell-repl-1",
	})
	if err != nil || started.State != KernelLocalOperationStateStarted || started.KernelGeneration != 1 {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	return started
}
