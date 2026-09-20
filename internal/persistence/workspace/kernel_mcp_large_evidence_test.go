package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/toolcontract"
)

func TestKernelMCPLargeReferenceCommitEnforcesOperationAuthority(t *testing.T) {
	for name, mutate := range map[string]func(*WriteRunnerLargeToolResultInput){
		"valid":  func(*WriteRunnerLargeToolResultInput) {},
		"stream": func(value *WriteRunnerLargeToolResultInput) { value.StreamUID = "foreign-stream" },
		"root":   func(value *WriteRunnerLargeToolResultInput) { value.RootFrameID = "foreign-root" },
		"call":   func(value *WriteRunnerLargeToolResultInput) { value.ToolCallID = "foreign-call" },
		"tool":   func(value *WriteRunnerLargeToolResultInput) { value.ToolName = "foreign-tool" },
		"claim":  func(value *WriteRunnerLargeToolResultInput) { value.ClaimToken = "stale-claim" },
		"event":  func(value *WriteRunnerLargeToolResultInput) { value.SourceEventID++ },
	} {
		t.Run(name, func(t *testing.T) {
			store, repo, claim := newKernelLocalOperationFixture(t)
			operation := createStartedKernelMCPReplOperationForTest(t, store, repo, claim)
			ctx := context.Background()
			const tool = "mcp__pubmed__fetch_details"
			audit := KernelMCPAuditInput{CallID: "host-large", FrameID: operation.FrameID, RootFrameID: operation.RootFrameID,
				OwnerUserID: operation.OwnerUserID, Server: "pubmed", Method: "fetch_details", Input: map[string]any{"id": "42480804"}}
			if _, err := store.BeginKernelMCPAudit(ctx, audit); err != nil {
				t.Fatal(err)
			}
			result := map[string]any{"pmid": "42480804", "abstract": strings.Repeat("complete-record ", 3000)}
			raw, _ := json.Marshal(result)
			inputRaw, _ := json.Marshal(audit.Input)
			write := WriteRunnerLargeToolResultInput{ArtifactID: "large-tool-result-" + strings.Repeat("a", 32),
				ProjectID: operation.ProjectID, RootFrameID: operation.RootFrameID, FrameID: operation.FrameID,
				StreamUID: operation.StreamUID, OwnerUserID: operation.OwnerUserID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: operation.SourceEventID, ToolName: tool, ToolCallID: audit.CallID, Content: raw}
			mutate(&write)
			record, err := store.WriteRunnerLargeToolResult(ctx, write)
			if err != nil {
				t.Fatal(err)
			}
			descriptor := toolcontract.ExternalizedResultDescriptor{ArtifactID: record.ArtifactID, VersionID: record.VersionID,
				SHA256: record.ContentSHA256, SizeBytes: record.SizeBytes, ContentType: record.ContentType, Outcome: "succeeded",
				ContentURL: "/api/artifacts/" + record.ArtifactID + "/versions/" + record.VersionID, Preview: "bounded source preview", Truncated: true}
			payload, _ := json.Marshal(map[string]any{
				"schema": kernelMCPEvidenceSchemaV1, "status": "completed", "toolPhase": "completed", "toolName": tool, "toolCallId": audit.CallID,
				"toolInput": audit.Input, "toolResult": descriptor, "outerToolCallId": operation.ToolCallID, "kernelOperationId": operation.OperationID,
				"executionId": operation.ExecutionID, "hostCallId": audit.CallID, "kernelId": operation.KernelID, "kernelGeneration": operation.KernelGeneration,
				"requestSha256": digestKernelMCPAuditBytes(inputRaw), "resultSha256": digestKernelMCPAuditBytes(raw),
				"evidenceClass": KernelMCPEvidenceClassBundledReadOnly, "connectorId": "bundled:pubmed", "connectorSource": "bundled",
				"inputSchemaSha256": strings.Repeat("b", 64), "readOnlyHint": true,
			})
			_, event, created, err := repo.AppendRunnerCheckpoint(ctx, transcriptstore.AppendRunnerCheckpointInput{
				Claim: claim, ClientMessageID: "large-evidence", Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: payload,
				CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
					_, err := store.CommitKernelMCPEvidenceTx(ctx, tx, event, KernelMCPEvidenceCommitInput{
						Audit:       KernelMCPAuditTerminalInput{KernelMCPAuditInput: audit, Status: "completed", Result: result},
						OperationID: operation.OperationID, OuterToolCallID: operation.ToolCallID, HostCallID: audit.CallID,
						ExecutionID: operation.ExecutionID, ToolName: tool, KernelID: operation.KernelID, KernelGeneration: operation.KernelGeneration,
						Claim: claim, RequestSHA256: digestKernelMCPAuditBytes(inputRaw), ResultSHA256: digestKernelMCPAuditBytes(raw),
						EvidenceClass: KernelMCPEvidenceClassBundledReadOnly, ConnectorID: "bundled:pubmed", ConnectorSource: "bundled", InputSchemaSHA256: strings.Repeat("b", 64), ReadOnlyHint: true})
					return transcriptstore.RunnerCheckpointCommitReceipt{}, err
				},
			})
			if name == "valid" {
				if err != nil || !created || event.EventID == 0 {
					t.Fatalf("valid externalized source rejected: %v", err)
				}
			} else if err == nil || created || event.EventID != 0 {
				t.Fatalf("foreign large source committed: %v", err)
			}
		})
	}
}
