package server

import (
	"context"
	"encoding/json"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) commitKernelMCPSourceEvidence(ctx context.Context, authority *kernelMCPTranscriptEvidenceAuthority,
	audit workspace.KernelMCPAuditInput, call kernelruntime.HostCall, toolName string,
	input map[string]any, output any, attestation kernelMCPSourceEvidenceAttestation,
) error {
	requestRaw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	resultRaw, err := json.Marshal(output)
	if err != nil {
		return err
	}
	requestSHA, resultSHA := kernelMCPEvidenceSHA256(requestRaw), kernelMCPEvidenceSHA256(resultRaw)
	checkpointResult, err := s.kernelMCPCheckpointResult(ctx, authority, call, toolName, resultRaw)
	if err != nil {
		return err
	}
	operation, claim := authority.operation, authority.identity.RunnerClaim
	payload := map[string]any{
		"schema": "synon.kernel_mcp_evidence.v1", "status": "completed", "toolPhase": "completed",
		"toolName": toolName, "toolCallId": call.ID, "toolInput": input, "toolResult": checkpointResult,
		"outerToolCallId": operation.ToolCallID, "kernelOperationId": operation.OperationID,
		"executionId": operation.ExecutionID, "hostCallId": call.ID, "kernelId": operation.KernelID,
		"kernelGeneration": operation.KernelGeneration, "requestSha256": requestSHA, "resultSha256": resultSHA,
		"evidenceClass": attestation.Class, "connectorId": attestation.ConnectorID,
		"connectorSource": attestation.ConnectorSource, "inputSchemaSha256": attestation.InputSchemaSHA256,
		"readOnlyHint": attestation.ReadOnlyHint,
	}
	_, err = s.checkpointTranscriptRunnerEventWithDestinationsAndHook(ctx,
		&transcriptRunnerAuthority{Stream: authority.stream, Claim: claim},
		transcriptstore.RunnerPhaseExecuting, "kernel-mcp-evidence-"+call.ID, payload, true, nil,
		func(commitCtx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			_, commitErr := s.workspaceStore.CommitKernelMCPEvidenceTx(commitCtx, tx, event, workspace.KernelMCPEvidenceCommitInput{
				Audit:       workspace.KernelMCPAuditTerminalInput{KernelMCPAuditInput: audit, Status: "completed", Result: output},
				OperationID: operation.OperationID, OuterToolCallID: operation.ToolCallID, HostCallID: call.ID,
				ExecutionID: operation.ExecutionID, ToolName: toolName, KernelID: operation.KernelID,
				KernelGeneration: operation.KernelGeneration, Claim: claim, RequestSHA256: requestSHA, ResultSHA256: resultSHA,
				EvidenceClass: attestation.Class, ConnectorID: attestation.ConnectorID, ConnectorSource: attestation.ConnectorSource,
				InputSchemaSHA256: attestation.InputSchemaSHA256, ReadOnlyHint: attestation.ReadOnlyHint,
			})
			return transcriptstore.RunnerCheckpointCommitReceipt{}, commitErr
		})
	return err
}

// Preserve nested MCP result bytes in the existing immutable evidence store
// before committing a bounded transcript checkpoint. The commit hook binds
// this reference to the live outer operation and the original result digest;
// no second source authority or user-visible artifact is created.
func (s *Server) kernelMCPCheckpointResult(ctx context.Context, authority *kernelMCPTranscriptEvidenceAuthority,
	call kernelruntime.HostCall, toolName string, raw json.RawMessage,
) (any, error) {
	if int64(len(raw)) <= runnerLargeToolResultInlineLimitBytes {
		return raw, nil
	}
	stream, operation, claim := authority.stream, authority.operation, authority.identity.RunnerClaim
	artifactID, _ := runnerLargeToolResultIdentities(stream, call.ID, toolName)
	record, err := s.workspaceStore.WriteRunnerLargeToolResult(ctx, workspace.WriteRunnerLargeToolResultInput{
		ArtifactID: artifactID, ProjectID: stream.ProjectID, RootFrameID: stream.RootFrameID,
		FrameID: stream.FrameID, StreamUID: stream.UID, OwnerUserID: stream.OwnerID,
		RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken, Attempt: claim.Attempt,
		SourceEventID: operation.SourceEventID, ToolName: toolName, ToolCallID: call.ID, Content: raw,
	})
	if err != nil {
		return nil, err
	}
	return buildRunnerLargeToolResultDescriptor(ctx, agentruntime.LargeToolResultInput{
		ToolCall: agentruntime.ToolCall{ID: call.ID, Name: toolName}, RawJSON: raw,
		Outcome: agentruntime.ToolResultSucceeded, MaxInlineBytes: runnerLargeToolResultInlineLimitBytes,
	}, record)
}
