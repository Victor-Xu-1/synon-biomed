package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
)

const sessionRunnerDurableEvidencePageSize = 1000

type sessionRunnerDurableToolCheckpoint struct {
	Schema                  string          `json:"schema"`
	ToolName                string          `json:"toolName"`
	ToolPhase               string          `json:"toolPhase"`
	ToolCallID              string          `json:"toolCallId"`
	ToolInput               json.RawMessage `json:"toolInput"`
	ExecutedToolInput       json.RawMessage `json:"executedToolInput"`
	ToolResult              json.RawMessage `json:"toolResult"`
	ToolCapabilities        []string        `json:"toolCapabilities,omitempty"`
	RejectedBeforeExecution bool            `json:"rejectedBeforeExecution"`
	EvidenceClass           string          `json:"evidenceClass"`
	ConnectorID             string          `json:"connectorId"`
	ConnectorSource         string          `json:"connectorSource"`
	InputSchemaSHA256       string          `json:"inputSchemaSha256"`
	ReadOnlyHint            bool            `json:"readOnlyHint"`
	OuterToolCallID         string          `json:"outerToolCallId"`
	KernelOperationID       string          `json:"kernelOperationId"`
	ExecutionID             string          `json:"executionId"`
	HostCallID              string          `json:"hostCallId"`
	KernelID                string          `json:"kernelId"`
	KernelGeneration        int64           `json:"kernelGeneration"`
	RequestSHA256           string          `json:"requestSha256"`
	ResultSHA256            string          `json:"resultSha256"`
	LifecyclePhase          string          `json:"lifecyclePhase"`
	ReviewKind              string          `json:"reviewKind"`
}

// sessionRunnerDurableEvidenceMessages rebuilds evidence call/result pairs from
// the owner-scoped immutable Transcript projection. Completion checks therefore
// keep recognizing evidence after compaction, process restart, lease expiry, and
// a later runner attempt; the browser and the model context are not evidence
// authorities.
func (s *Server) sessionRunnerDurableEvidenceMessages(
	ctx context.Context,
	run *sessionRunnerChatRun,
) ([]agentruntime.Message, error) {
	return s.sessionRunnerDurableToolMessagesPage(
		ctx, run, sessionRunnerDurableEvidencePageSize, false, s.sessionRunnerDurableCheckpointEvidenceTool,
	)
}

func (s *Server) sessionRunnerDurableEvidenceMessagesPage(
	ctx context.Context,
	run *sessionRunnerChatRun,
	pageSize int,
) ([]agentruntime.Message, error) {
	return s.sessionRunnerDurableToolMessagesPage(ctx, run, pageSize, false, s.sessionRunnerDurableCheckpointEvidenceTool)
}

// sessionRunnerDurableExplicitToolContractMessages rebuilds governed completed
// and failed tool receipts for the current logical task. Successful receipts
// prove that a user-named action ran; failed receipts preserve an explicitly
// requested failure-code report. They are deliberately separate from
// scientific source evidence and therefore never authorize claims or citations.
func (s *Server) sessionRunnerDurableExplicitToolContractMessages(
	ctx context.Context,
	run *sessionRunnerChatRun,
) ([]agentruntime.Message, error) {
	return s.sessionRunnerDurableToolMessagesPage(
		ctx, run, sessionRunnerDurableEvidencePageSize, true, s.sessionRunnerDurableCheckpointExplicitTool,
	)
}

func (s *Server) sessionRunnerDurableToolMessagesPage(
	ctx context.Context,
	run *sessionRunnerChatRun,
	pageSize int,
	includeFailed bool,
	accept func(sessionRunnerDurableToolCheckpoint) bool,
) ([]agentruntime.Message, error) {
	if run == nil || run.Transcript == nil {
		return nil, nil
	}
	if s == nil || s.transcriptStore == nil {
		return nil, errors.New("runner transcript evidence authority is unavailable")
	}
	if pageSize <= 0 || pageSize > sessionRunnerDurableEvidencePageSize {
		return nil, errors.New("runner transcript evidence page size is invalid")
	}
	if accept == nil {
		return nil, errors.New("runner transcript tool receipt policy is unavailable")
	}
	authority := run.Transcript
	taskBoundary, taskBoundaryRequired, err := s.sessionRunnerDurableTaskBoundary(ctx, run)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(
		ctx, authority.Stream.UID, authority.Stream.OwnerID,
	)
	if err != nil {
		return nil, err
	}
	after := int64(0)
	messages := make([]agentruntime.Message, 0)
	taskBoundaryReached := !taskBoundaryRequired
	for {
		page, err := s.transcriptStore.ListProjectedCoordinateEvents(
			ctx,
			transcriptstore.ListProjectedEventsInput{
				StreamUID: authority.Stream.UID, OwnerID: authority.Stream.OwnerID,
				BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
				AfterPublicationSequence:   after,
				ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
				Limit:                      pageSize,
			},
		)
		if err != nil {
			return nil, err
		}
		for _, projected := range page {
			after = projected.Event.PublicationSeq
			if !taskBoundaryReached && sessionRunnerProjectedEventMatchesTaskBoundary(projected.Event, taskBoundary) {
				taskBoundaryReached = true
			}
			if !taskBoundaryReached {
				continue
			}
			if projected.Event.Type != "runner_checkpoint" {
				continue
			}
			payload := projected.ResolvedPayloadJSON
			if len(payload) == 0 {
				payload = projected.Event.PayloadJSON
			}
			var checkpoint sessionRunnerDurableToolCheckpoint
			if json.Unmarshal(payload, &checkpoint) != nil {
				continue
			}
			phase := strings.ToLower(strings.TrimSpace(checkpoint.ToolPhase))
			if phase != "completed" && (!includeFailed || phase != "failed") || !accept(checkpoint) {
				continue
			}
			checkpoint.ToolCallID = strings.TrimSpace(checkpoint.ToolCallID)
			arguments, ok := sessionRunnerDurableToolArguments(checkpoint.ToolInput)
			if checkpoint.ToolCallID == "" || !ok {
				continue
			}
			result, ok := sessionRunnerDurableToolResult(checkpoint.ToolResult)
			if !ok {
				continue
			}
			result, err = s.restoreDurableEvidencePayload(ctx, authority.Stream, checkpoint, projected.Event.EventID, result)
			if err != nil {
				if errors.Is(err, errRunnerLargeToolResultUnavailable) {
					// Historical externalized evidence may have been pruned. Keep
					// the immutable checkpoint, but do not synthesize a tool result
					// from its preview; later execution can reacquire the source.
					continue
				}
				return nil, err
			}
			call := agentruntime.ToolCall{
				ID: checkpoint.ToolCallID, Name: checkpoint.ToolName, Arguments: arguments,
				VerifiedEvidence: true,
			}
			messages = append(messages,
				agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
				agentruntime.Message{Role: "tool", ToolCallID: checkpoint.ToolCallID, Content: result},
			)
		}
		if len(page) < pageSize {
			break
		}
	}
	if !taskBoundaryReached {
		return nil, errors.New("runner transcript task boundary is unavailable")
	}
	return messages, nil
}

func (s *Server) sessionRunnerDurableTaskBoundary(
	ctx context.Context,
	run *sessionRunnerChatRun,
) (string, bool, error) {
	if run == nil || run.Transcript == nil || run.Transcript.Stream.Kind != transcriptstore.StreamKindFrameRef {
		return "", false, nil
	}
	// An admitted internal job owns a dedicated child frame and its complete
	// transcript. Its generated admission input is not a user task intent.
	// Ordinary runs still require the active logical-task boundary below; owner,
	// branch projection and immutable receipt checks apply to both scopes.
	if run.frameOwnedJob {
		return "", false, nil
	}
	intent, found, err := s.transcriptStore.GetActiveFrameTaskIntent(
		ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID,
	)
	if err != nil {
		return "", false, err
	}
	if !found || strings.TrimSpace(intent.SourceEventID) == "" {
		return "", false, errors.New("runner transcript task intent is unavailable")
	}
	if expected := strings.TrimSpace(run.TaskIntentID); expected != "" && expected != strings.TrimSpace(intent.ID) {
		return "", false, errors.New("runner transcript task intent changed during completion verification")
	}
	intents, err := s.transcriptStore.ListActiveFrameTaskIntents(
		ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID,
	)
	if err != nil {
		return "", false, err
	}
	activeIndex := -1
	for index := len(intents) - 1; index >= 0; index-- {
		if strings.TrimSpace(intents[index].ID) == strings.TrimSpace(intent.ID) {
			activeIndex = index
			break
		}
	}
	if activeIndex < 0 {
		return "", false, errors.New("runner transcript active task intent is missing from durable history")
	}
	rootIndex := activeIndex
	for rootIndex > 0 && sessionRunnerTaskContinuesPriorWork(intents[rootIndex].Text) {
		rootIndex--
	}
	boundary := strings.TrimSpace(intents[rootIndex].SourceEventID)
	if boundary == "" {
		return "", false, errors.New("runner transcript logical task boundary is unavailable")
	}
	return boundary, true, nil
}

func sessionRunnerProjectedEventMatchesTaskBoundary(event transcriptstore.Event, boundary string) bool {
	boundary = strings.TrimSpace(boundary)
	if boundary == "" {
		return false
	}
	if strings.TrimSpace(event.ClientMessageID) == boundary {
		return true
	}
	return event.FrameEventID != nil && strings.TrimSpace(*event.FrameEventID) == boundary
}

func (s *Server) sessionRunnerDurableCheckpointEvidenceTool(checkpoint sessionRunnerDurableToolCheckpoint) bool {
	name := strings.TrimSpace(checkpoint.ToolName)
	if !strings.HasPrefix(strings.ToLower(name), "mcp__") {
		return s.sessionRunnerEvidenceTool(name)
	}
	return validSessionRunnerMCPDurableCheckpoint(checkpoint)
}

func (s *Server) sessionRunnerDurableCheckpointExplicitTool(checkpoint sessionRunnerDurableToolCheckpoint) bool {
	name := strings.TrimSpace(checkpoint.ToolName)
	if name == "" || strings.TrimSpace(checkpoint.ReviewKind) != "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(name), "mcp__") {
		// MCP is intentionally executed through the persistent control kernel.
		// Its child receipt therefore has the attested kernel evidence schema,
		// not the outer root-tool lifecycle marker. Treat that single validated
		// receipt as both source evidence and proof that an explicitly requested
		// MCP method ran; requiring lifecyclePhase=tool here creates a competing
		// completion path that can never recognize the canonical REPL route.
		return validSessionRunnerMCPDurableCheckpoint(checkpoint)
	}
	if !strings.EqualFold(strings.TrimSpace(checkpoint.LifecyclePhase), "tool") {
		return false
	}
	// Ordinary root-tool checkpoints are emitted only after the server runtime
	// dispatched the exact snapshotted tool. The immutable completed checkpoint
	// is the execution authority; it must not be reinterpreted through today's
	// registry because a later deploy may legitimately change that catalog.
	return true
}

func validSessionRunnerMCPDurableCheckpoint(checkpoint sessionRunnerDurableToolCheckpoint) bool {
	if checkpoint.EvidenceClass != workspace.KernelMCPEvidenceClassBundledReadOnly ||
		checkpoint.ConnectorSource != "bundled" || strings.TrimSpace(checkpoint.ConnectorID) == "" ||
		!checkpoint.ReadOnlyHint || !validKernelMCPEvidenceDigest(checkpoint.InputSchemaSHA256) ||
		!validKernelMCPEvidenceDigest(checkpoint.RequestSHA256) || !validKernelMCPEvidenceDigest(checkpoint.ResultSHA256) {
		return false
	}
	switch checkpoint.Schema {
	case "synon.kernel_mcp_evidence.v1":
		if strings.TrimSpace(checkpoint.OuterToolCallID) == "" ||
			strings.TrimSpace(checkpoint.KernelOperationID) == "" || strings.TrimSpace(checkpoint.ExecutionID) == "" ||
			strings.TrimSpace(checkpoint.HostCallID) != strings.TrimSpace(checkpoint.ToolCallID) ||
			strings.TrimSpace(checkpoint.KernelID) == "" || checkpoint.KernelGeneration <= 0 {
			return false
		}
	case workspaceMCPSourceEvidenceSchemaV1:
		// Direct runner MCP evidence is bound to the immutable Transcript tool
		// call itself; kernel-only identities must not be forged onto this shape.
		if strings.TrimSpace(checkpoint.OuterToolCallID) != "" ||
			strings.TrimSpace(checkpoint.KernelOperationID) != "" || strings.TrimSpace(checkpoint.ExecutionID) != "" ||
			strings.TrimSpace(checkpoint.HostCallID) != "" || strings.TrimSpace(checkpoint.KernelID) != "" ||
			checkpoint.KernelGeneration != 0 {
			return false
		}
	default:
		return false
	}
	input := bytes.TrimSpace(sessionRunnerDurableExecutedToolInput(checkpoint))
	result := bytes.TrimSpace(checkpoint.ToolResult)
	resultSHA := kernelMCPEvidenceSHA256(result)
	if checkpoint.Schema == "synon.kernel_mcp_evidence.v1" {
		// The committed nested MCP receipt retains the original result digest.
		// Its full contents are restored and scope/hash-checked by the normal
		// durable evidence reader before any scientific consumer receives them.
		if descriptor, _, externalized, err := toolcontract.DecodeExternalizedResult(result); externalized {
			if err != nil || descriptor.Outcome != string(agentruntime.ToolResultSucceeded) {
				return false
			}
			resultSHA = descriptor.SHA256
		}
	}
	return json.Valid(input) && json.Valid(result) &&
		kernelMCPEvidenceSHA256(input) == checkpoint.RequestSHA256 &&
		resultSHA == checkpoint.ResultSHA256
}

func sessionRunnerDurableExecutedToolInput(checkpoint sessionRunnerDurableToolCheckpoint) json.RawMessage {
	executed := bytes.TrimSpace(checkpoint.ExecutedToolInput)
	if len(executed) > 0 && !bytes.Equal(executed, []byte("null")) && json.Valid(executed) {
		return executed
	}
	return checkpoint.ToolInput
}

func validKernelMCPEvidenceDigest(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func sessionRunnerDurableToolArguments(raw json.RawMessage) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage(`{}`), true
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) == nil && object != nil {
		return append(json.RawMessage(nil), raw...), true
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) != nil {
		return nil, false
	}
	decoded := bytes.TrimSpace([]byte(encoded))
	if json.Unmarshal(decoded, &object) != nil || object == nil {
		return nil, false
	}
	return append(json.RawMessage(nil), decoded...), true
}

func sessionRunnerDurableToolResult(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		encoded = strings.TrimSpace(encoded)
		if encoded == "" || !json.Valid([]byte(encoded)) {
			return "", false
		}
		return encoded, true
	}
	if !json.Valid(raw) {
		return "", false
	}
	return string(raw), true
}

// sessionRunnerDurableEvidencePrefix omits a durable copy only when the current
// transcript contains one semantically unique pair for the same call identity.
// Equivalent durable/current/replay copies collapse, while any differing name,
// arguments, result, or incomplete copy remains visible to the fail-closed pair
// validator.
func sessionRunnerDurableEvidencePrefix(
	durable, current []agentruntime.Message,
) []agentruntime.Message {
	currentPairs := sessionRunnerUniqueToolEvidencePairs(current)
	prefix := make([]agentruntime.Message, 0, len(durable))
	for index := 0; index < len(durable); index++ {
		message := durable[index]
		if len(message.ToolCalls) == 1 && index+1 < len(durable) {
			call := message.ToolCalls[0]
			result := durable[index+1]
			id := strings.TrimSpace(call.ID)
			if result.Role == "tool" && strings.TrimSpace(result.ToolCallID) == id {
				if currentPair, found := currentPairs[id]; found && !call.VerifiedEvidence &&
					sessionRunnerEvidencePairEquivalent(call, result.Content, currentPair.call, currentPair.content) {
					index++
					continue
				}
				prefix = append(prefix, message, result)
				index++
				continue
			}
		}
		prefix = append(prefix, message)
	}
	return prefix
}

func sessionRunnerEvidencePairEquivalent(
	leftCall agentruntime.ToolCall,
	leftResult string,
	rightCall agentruntime.ToolCall,
	rightResult string,
) bool {
	return normalizeAgentToolName(leftCall.Name) == normalizeAgentToolName(rightCall.Name) &&
		sessionRunnerCanonicalEvidenceJSON(leftCall.Arguments) == sessionRunnerCanonicalEvidenceJSON(rightCall.Arguments) &&
		sessionRunnerCanonicalEvidenceJSON([]byte(leftResult)) == sessionRunnerCanonicalEvidenceJSON([]byte(rightResult))
}

func sessionRunnerCanonicalEvidenceJSON(value []byte) string {
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		return ""
	}
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return string(value)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return string(value)
	}
	return string(canonical)
}
