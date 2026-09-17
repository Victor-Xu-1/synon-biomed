package workspace

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestCompleteKernelLocalOperationPreflightMaterializesApprovedControlResult(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "prestart-completion", "call-prestart-completion")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "decision-prestart-completion", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claim,
	})
	if err != nil || approved.State != KernelLocalOperationStateApproved {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	terminalResult := json.RawMessage(`{"ok":true,"status":"dependency_preflight_required","executed":false,"missing_distributions":["biopython"]}`)
	completed, err := store.CompleteKernelLocalOperationPreflight(context.Background(), CompleteKernelLocalOperationPreflightInput{
		OwnerUserID: approved.OwnerUserID, OperationID: approved.OperationID,
		ExpectedStateVersion: approved.StateVersion, ExpectedState: approved.State, Claim: claim,
		ReasonCode: "dependency_preflight_required", TerminalResultJSON: terminalResult,
	})
	if err != nil || completed.State != KernelLocalOperationStateCancelled || completed.ExecutionID != "" ||
		completed.TerminalAt == nil || completed.ReasonCode != "dependency_preflight_required" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	materialized, found, err := store.GetKernelToolResultMaterialization(context.Background(), completed.OperationID)
	var gotResult, wantResult any
	gotErr := json.Unmarshal(materialized.TerminalResultJSON, &gotResult)
	wantErr := json.Unmarshal(terminalResult, &wantResult)
	if err != nil || !found || gotErr != nil || wantErr != nil || !reflect.DeepEqual(gotResult, wantResult) ||
		materialized.ExecutionLogSHA256 != "" {
		t.Fatalf("materialized=%#v found=%t err=%v", materialized, found, err)
	}
	replayed, err := store.CompleteKernelLocalOperationPreflight(context.Background(), CompleteKernelLocalOperationPreflightInput{
		OwnerUserID: approved.OwnerUserID, OperationID: approved.OperationID,
		ExpectedStateVersion: approved.StateVersion, ExpectedState: approved.State, Claim: claim,
		ReasonCode: "dependency_preflight_required", TerminalResultJSON: terminalResult,
	})
	if err != nil || replayed.StateVersion != completed.StateVersion {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
}

func TestCompleteKernelLocalOperationPreflightNeedsNoExecutionApproval(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "source-preflight", "call-source-preflight")
	terminalResult := json.RawMessage(`{"ok":true,"status":"source_preflight_required","executed":false}`)
	completed, err := store.CompleteKernelLocalOperationPreflight(context.Background(), CompleteKernelLocalOperationPreflightInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ExpectedState: operation.State, Claim: claim,
		ReasonCode: "source_preflight_required", TerminalResultJSON: terminalResult,
	})
	if err != nil || completed.State != KernelLocalOperationStateCancelled || completed.ExecutionID != "" ||
		completed.ApprovalDecision != "deny" || completed.ApprovalSource != "policy" ||
		completed.ApprovalActorID != "system" || completed.ApprovedAt != nil || completed.TerminalAt == nil ||
		completed.ReasonCode != "source_preflight_required" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	materialized, found, err := store.GetKernelToolResultMaterialization(context.Background(), completed.OperationID)
	var gotPendingResult, wantPendingResult any
	gotPendingErr := json.Unmarshal(materialized.TerminalResultJSON, &gotPendingResult)
	wantPendingErr := json.Unmarshal(terminalResult, &wantPendingResult)
	if err != nil || !found || gotPendingErr != nil || wantPendingErr != nil ||
		!reflect.DeepEqual(gotPendingResult, wantPendingResult) {
		t.Fatalf("materialized=%#v found=%t err=%v", materialized, found, err)
	}
	runnable, err := store.ListRunnableKernelLocalOperations(context.Background(), claim, 10)
	if err != nil || len(runnable) != 1 || runnable[0].OperationID != completed.OperationID ||
		runnable[0].AdmittedInputRevision != claim.ClaimedInputRevision {
		t.Fatalf("runnable=%#v err=%v", runnable, err)
	}
}
