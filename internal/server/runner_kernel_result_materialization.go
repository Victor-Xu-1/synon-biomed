package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

// durableAgentKernelResultContext retains only the immutable runner authority
// needed to materialize a background kernel result. It does not pin the HTTP
// request, model request, or the full live runner context for a long-running
// cell.
func durableAgentKernelResultContext(ctx context.Context, toolCallID string) context.Context {
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	toolCallID = strings.TrimSpace(toolCallID)
	if run == nil || run.Transcript == nil || toolCallID == "" {
		return context.Background()
	}
	authority := *run.Transcript
	clone := &sessionRunnerChatRun{
		SessionID: run.SessionID, Attempt: run.Attempt, ClaimToken: run.ClaimToken,
		Transcript: &authority,
		ToolSourceEventIDs: map[string]int64{
			toolCallID: run.ToolSourceEventIDs[toolCallID],
		},
	}
	return withTranscriptRunnerChatRun(context.Background(), clone)
}

func (s *Server) materializeAgentKernelTerminalResult(
	ctx context.Context,
	operation workspace.KernelLocalOperation,
	result map[string]any,
	outputLimitBytes int64,
) (agentruntime.MaterializedToolResult, error) {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil || ctx == nil ||
		strings.TrimSpace(operation.ToolCallID) == "" || strings.TrimSpace(operation.Tool) == "" || result == nil {
		return agentruntime.MaterializedToolResult{}, errors.New("kernel tool result materialization authority is unavailable")
	}
	if outputLimitBytes <= 0 {
		outputLimitBytes = defaultSessionRunnerOutputLimitBytes
	}
	checkpointBudget, err := kernelTerminalCheckpointResultBudget(ctx, operation, result, outputLimitBytes)
	if err != nil {
		return agentruntime.MaterializedToolResult{}, err
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	engine := agentruntime.Engine{
		MaxToolResultBytes: runnerLargeToolResultInlineLimit(checkpointBudget),
		// Kernel terminal results are already bound to an immutable durable
		// operation. Use that authority even when a recovery runner is alive;
		// a resumed runner must not rewrite detached evidence under a different
		// claim or source event.
		LargeToolResults: s.largeToolResultAuthorityForKernelOperation(operation),
	}
	materialized, err := engine.MaterializeToolResult(ctx, agentruntime.ToolCall{
		ID: operation.ToolCallID, Name: operation.Tool,
	}, result, agentruntime.ClassifyToolResult(result))
	if err != nil {
		var infrastructureErr *agentruntime.LargeToolResultInfrastructureError
		if errors.As(err, &infrastructureErr) {
			durableAuthority := false
			if authority, ok := engine.LargeToolResults.(runnerLargeToolResultAuthority); ok {
				durableAuthority = authority.durableOperation != nil
			}
			log.Printf("kernel large result materialization failed operation=%q tool_call=%q authority=%T cause=%v",
				operation.OperationID, operation.ToolCallID, engine.LargeToolResults, errors.Unwrap(infrastructureErr))
			log.Printf("kernel large result materialization authority context operation=%q durable=%t runner_context=%t",
				operation.OperationID, durableAuthority, run != nil && run.Transcript != nil)
		}
		return agentruntime.MaterializedToolResult{}, err
	}
	return materialized, nil
}

// ensureKernelToolResultMaterialization is the one-time v39-to-v41 recovery
// boundary for a terminal operation that crashed before its model checkpoint.
// Reconstruction is allowed only while the exact durable tool batch is being
// resumed under a current Transcript claim; normal replay reads v41 only.
func (s *Server) ensureKernelToolResultMaterialization(
	ctx context.Context,
	operation workspace.KernelLocalOperation,
	outputLimitBytes int64,
) error {
	if s == nil || s.workspaceStore == nil || ctx == nil || !workspace.KernelLocalOperationIsTerminal(operation.State) {
		return errors.New("kernel tool result migration authority is unavailable")
	}
	if _, found, err := s.workspaceStore.GetKernelToolResultMaterialization(ctx, operation.OperationID); err != nil {
		return err
	} else if found {
		return nil
	}
	var result map[string]any
	if operation.State == workspace.KernelLocalOperationStateOutcomeUnknown {
		decoder := json.NewDecoder(strings.NewReader(string(operation.ResultJSON)))
		decoder.UseNumber()
		if err := decoder.Decode(&result); err != nil || result == nil || decoder.Decode(&struct{}{}) == nil {
			return errors.New("legacy kernel outcome result is invalid")
		}
	} else {
		var err error
		result, err = s.replayAgentKernelOperationResult(operation)
		if err != nil {
			return err
		}
	}
	materialized, err := s.materializeAgentKernelTerminalResult(ctx, operation, result, outputLimitBytes)
	if err != nil {
		return fmt.Errorf("materialize legacy kernel result: %w", err)
	}
	_, err = s.workspaceStore.CommitLegacyKernelToolResultMaterialization(ctx,
		workspace.CommitLegacyKernelToolResultMaterializationInput{
			OperationID: operation.OperationID, ExpectedState: operation.State,
			ExpectedStateVersion: operation.StateVersion, TerminalResultJSON: materialized.JSON,
			TerminalResultRef: materialized.ResultRef,
		})
	return err
}

func kernelTerminalCheckpointResultBudget(
	ctx context.Context,
	operation workspace.KernelLocalOperation,
	result map[string]any,
	configuredLimit int64,
) (int64, error) {
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	provisional := run != nil && len(run.RequiredScientificCapabilities) > 0
	if run == nil || run.Transcript == nil || run.Transcript.Claim.RunnerID == "" {
		var err error
		run, err = durableKernelTerminalCheckpointBudgetRun(operation)
		if err != nil {
			return 0, err
		}
		// Recovery can no longer consult the expired runner's in-memory
		// capability selection. Reserving the provisional marker is the safe
		// upper envelope and never changes the durable result itself.
		provisional = true
	}
	status, phase := "failed", "failed"
	if agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded {
		status, phase = "completed", "completed"
	}
	message := fmt.Sprintf("tool %s %s", operation.Tool, status)
	options := SessionRunnerChatOptions{RunnerID: run.Transcript.Claim.RunnerID}
	input := chatToolCheckpointInput(options, run, status, message, operation.ToolCallID, phase, map[string]any{
		"toolName": operation.Tool, "toolResult": json.RawMessage("null"),
	})
	payload := transcriptCheckpointPayload(input)
	if provisional {
		payload["visibility"] = "provisional"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal terminal checkpoint budget skeleton: %w", err)
	}
	const nullJSONBytes = 4
	overhead := int64(len(encoded) - nullJSONBytes)
	remaining := int64(transcriptstore.MaxEventPayloadBytes) - overhead
	if remaining <= 0 {
		return 0, errors.New("kernel terminal checkpoint metadata exceeds transcript payload limit")
	}
	if configuredLimit > 0 && configuredLimit < remaining {
		remaining = configuredLimit
	}
	return remaining, nil
}

func durableKernelTerminalCheckpointBudgetRun(
	operation workspace.KernelLocalOperation,
) (*sessionRunnerChatRun, error) {
	if strings.TrimSpace(operation.FrameID) == "" || strings.TrimSpace(operation.RunnerID) == "" ||
		strings.TrimSpace(operation.ToolCallID) == "" || strings.TrimSpace(operation.Tool) == "" {
		return nil, errors.New("durable kernel terminal checkpoint budget authority is unavailable")
	}
	// Transcript claim tokens are always 32 random bytes encoded with raw
	// base64url (43 bytes). Maximal numeric fields reserve enough room for a
	// later retry attempt without retaining an expired claim secret in memory
	// or persisting it in SQLite.
	const transcriptClaimTokenBytes = 43
	return &sessionRunnerChatRun{
		SessionID:    operation.FrameID,
		Attempt:      int(^uint(0) >> 1),
		ClaimToken:   strings.Repeat("x", transcriptClaimTokenBytes),
		AfterEventID: int64(^uint64(0) >> 1),
		Transcript: &transcriptRunnerAuthority{Claim: transcriptstore.RunnerClaim{
			RunnerID: operation.RunnerID,
		}},
	}, nil
}

func (g serverAgentRuntimeToolGateway) trustedAgentRuntimeToolResult(
	ctx context.Context,
	call agentruntime.ToolCall,
	value any,
	parts []agentruntime.ContentPart,
) agentruntime.ToolResult {
	result := agentruntime.ToolResult{Value: value, Parts: parts}
	if g.server == nil || g.server.workspaceStore == nil || !isAgentKernelToolName(call.Name) {
		return result
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil {
		return result
	}
	operation, found, err := g.server.workspaceStore.GetKernelLocalOperationByToolCall(
		ctx, run.Transcript.Stream.OwnerID, run.Transcript.Stream.UID, call.ID,
	)
	if err != nil || !found || !workspace.KernelLocalOperationIsTerminal(operation.State) {
		return result
	}
	materialized, found, err := g.server.workspaceStore.GetKernelToolResultMaterialization(ctx, operation.OperationID)
	if err != nil || !found {
		return result
	}
	result.Materialized = &agentruntime.MaterializedToolResult{
		JSON:   append([]byte(nil), materialized.TerminalResultJSON...),
		SHA256: materialized.TerminalResultSHA256, ResultRef: materialized.ResultRef,
		Outcome: agentruntime.ClassifyToolResult(value),
	}
	bindDurableInlineToolResultValue(&result)
	capabilities := agentRuntimeToolCapabilities(g.toolSchemas, call.Name)
	if taskCapabilities := run.toolCapabilities(call.Name); len(taskCapabilities) > 0 {
		capabilities = taskCapabilities
	}
	g.server.recordAgentRuntimeSemanticFailure(g.sessionID, call, result.Value, capabilities)
	return result
}

// bindDurableInlineToolResultValue makes the committed v41 materialization the
// authority for both bytes and semantic classification. The live kernel return
// map may be reconstructed through a different persistence path (notably after
// workspace-output policy sanitation or restart). Letting that transient map
// disagree with the committed bytes makes Engine reject a recoverable tool
// outcome as an infrastructure failure and terminally stop the parent task.
func bindDurableInlineToolResultValue(result *agentruntime.ToolResult) {
	if result == nil || result.Materialized == nil || result.Materialized.ResultRef != "" ||
		len(result.Materialized.JSON) == 0 {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(result.Materialized.JSON))
	decoder.UseNumber()
	var authoritative any
	if err := decoder.Decode(&authoritative); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return
	}
	result.Value = authoritative
	result.Materialized.Outcome = agentruntime.ClassifyToolResult(authoritative)
}
