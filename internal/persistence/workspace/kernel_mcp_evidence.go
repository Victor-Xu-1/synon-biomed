package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/toolcontract"
)

const kernelMCPEvidenceSchemaV1 = "synon.kernel_mcp_evidence.v1"

const KernelMCPEvidenceClassBundledReadOnly = "bundled-readonly-source-v1"

// KernelMCPEvidenceCommitInput binds one successful server-executed host.mcp
// call to the exact live repl operation that issued it. Model output, stdout,
// files, and artifact prose are deliberately absent from this authority.
type KernelMCPEvidenceCommitInput struct {
	Audit             KernelMCPAuditTerminalInput
	OperationID       string
	OuterToolCallID   string
	HostCallID        string
	ExecutionID       string
	ToolName          string
	KernelID          string
	KernelGeneration  int64
	Claim             transcriptstore.RunnerClaim
	RequestSHA256     string
	ResultSHA256      string
	EvidenceClass     string
	ConnectorID       string
	ConnectorSource   string
	InputSchemaSHA256 string
	ReadOnlyHint      bool
}

// CommitKernelMCPEvidenceTx validates the complete durable execution identity
// and commits the redacted Frame audit terminal in the same transaction as the
// caller's canonical runner checkpoint. A failure rolls both authorities back
// before the provider result can be returned to the kernel.
func (s *Store) CommitKernelMCPEvidenceTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	event transcriptstore.Event,
	input KernelMCPEvidenceCommitInput,
) (FrameEvent, error) {
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.OuterToolCallID = strings.TrimSpace(input.OuterToolCallID)
	input.HostCallID = strings.TrimSpace(input.HostCallID)
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.ToolName = strings.TrimSpace(input.ToolName)
	input.KernelID = strings.TrimSpace(input.KernelID)
	input.RequestSHA256 = strings.TrimSpace(input.RequestSHA256)
	input.ResultSHA256 = strings.TrimSpace(input.ResultSHA256)
	input.EvidenceClass = strings.TrimSpace(input.EvidenceClass)
	input.ConnectorID = strings.TrimSpace(input.ConnectorID)
	input.ConnectorSource = strings.TrimSpace(input.ConnectorSource)
	input.InputSchemaSHA256 = strings.TrimSpace(input.InputSchemaSHA256)
	input.Audit.KernelMCPAuditInput = normalizeKernelMCPAuditInput(input.Audit.KernelMCPAuditInput)
	input.Audit.Status = strings.TrimSpace(input.Audit.Status)
	input.Audit.Reason = strings.TrimSpace(input.Audit.Reason)
	if s == nil || tx == nil || event.EventID <= 0 || event.RunnerAttempt == nil ||
		input.OperationID == "" || input.OuterToolCallID == "" || input.HostCallID == "" ||
		input.ExecutionID == "" || input.ToolName == "" || input.KernelID == "" ||
		input.KernelGeneration <= 0 || !validKernelMCPEvidenceSHA256(input.RequestSHA256) ||
		!validKernelMCPEvidenceSHA256(input.ResultSHA256) ||
		input.EvidenceClass != KernelMCPEvidenceClassBundledReadOnly || input.ConnectorID == "" ||
		input.ConnectorSource != "bundled" || !validKernelMCPEvidenceSHA256(input.InputSchemaSHA256) || !input.ReadOnlyHint ||
		input.Audit.Status != "completed" || input.Audit.Reason != "" ||
		validateKernelMCPAuditInput(input.Audit.KernelMCPAuditInput) != nil {
		return FrameEvent{}, errors.New("kernel MCP evidence identity is incomplete")
	}
	if input.Audit.CallID != input.HostCallID {
		return FrameEvent{}, fmt.Errorf("kernel MCP evidence call mismatch: %w", ErrKernelLocalOperationConflict)
	}
	operation, found, err := getKernelLocalOperationQuery(ctx, tx, input.OperationID)
	if err != nil {
		return FrameEvent{}, err
	}
	if !found || operation.State != KernelLocalOperationStateStarted || operation.Tool != "repl" ||
		operation.ToolCallID != input.OuterToolCallID || operation.ExecutionID != input.ExecutionID ||
		operation.KernelID != input.KernelID || operation.KernelGeneration != input.KernelGeneration ||
		operation.StreamUID != event.StreamUID || operation.RunnerAttempt != *event.RunnerAttempt ||
		operation.RunnerID != input.Claim.RunnerID || operation.RunnerClaimSHA256 != kernelLocalOperationClaimSHA256(input.Claim.ClaimToken) {
		return FrameEvent{}, fmt.Errorf("kernel MCP evidence operation mismatch: %w", ErrKernelLocalOperationConflict)
	}
	if operation.OwnerUserID != input.Audit.OwnerUserID || operation.FrameID != input.Audit.FrameID ||
		operation.RootFrameID != input.Audit.RootFrameID {
		return FrameEvent{}, fmt.Errorf("kernel MCP evidence frame mismatch: %w", ErrKernelLocalOperationConflict)
	}
	if err := validateKernelLocalOperationLiveClaim(ctx, tx, operation, input.Claim); err != nil {
		return FrameEvent{}, err
	}
	var eventType string
	var runnerAttempt int64
	var payloadJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT event_type,runner_attempt,payload_json
		FROM transcript_events WHERE stream_uid=? AND event_id=?`, event.StreamUID, event.EventID).Scan(
		&eventType, &runnerAttempt, &payloadJSON,
	); err != nil {
		return FrameEvent{}, err
	}
	if eventType != "runner_checkpoint" || runnerAttempt != *event.RunnerAttempt || !bytes.Equal(payloadJSON, event.PayloadJSON) {
		return FrameEvent{}, fmt.Errorf("kernel MCP evidence checkpoint mismatch: %w", ErrKernelLocalOperationConflict)
	}
	var payload struct {
		Schema            string          `json:"schema"`
		Status            string          `json:"status"`
		ToolPhase         string          `json:"toolPhase"`
		ToolName          string          `json:"toolName"`
		ToolCallID        string          `json:"toolCallId"`
		ToolInput         json.RawMessage `json:"toolInput"`
		ToolResult        json.RawMessage `json:"toolResult"`
		OuterToolCallID   string          `json:"outerToolCallId"`
		KernelOperationID string          `json:"kernelOperationId"`
		ExecutionID       string          `json:"executionId"`
		HostCallID        string          `json:"hostCallId"`
		KernelID          string          `json:"kernelId"`
		KernelGeneration  int64           `json:"kernelGeneration"`
		RequestSHA256     string          `json:"requestSha256"`
		ResultSHA256      string          `json:"resultSha256"`
		EvidenceClass     string          `json:"evidenceClass"`
		ConnectorID       string          `json:"connectorId"`
		ConnectorSource   string          `json:"connectorSource"`
		InputSchemaSHA256 string          `json:"inputSchemaSha256"`
		ReadOnlyHint      bool            `json:"readOnlyHint"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) == nil ||
		payload.Schema != kernelMCPEvidenceSchemaV1 || payload.Status != "completed" || payload.ToolPhase != "completed" ||
		payload.ToolName != input.ToolName || payload.ToolCallID != input.HostCallID ||
		payload.OuterToolCallID != input.OuterToolCallID || payload.KernelOperationID != input.OperationID ||
		payload.ExecutionID != input.ExecutionID || payload.HostCallID != input.HostCallID ||
		payload.KernelID != input.KernelID || payload.KernelGeneration != input.KernelGeneration ||
		payload.RequestSHA256 != input.RequestSHA256 || payload.ResultSHA256 != input.ResultSHA256 ||
		payload.EvidenceClass != input.EvidenceClass || payload.ConnectorID != input.ConnectorID ||
		payload.ConnectorSource != input.ConnectorSource || payload.InputSchemaSHA256 != input.InputSchemaSHA256 ||
		payload.ReadOnlyHint != input.ReadOnlyHint {
		return FrameEvent{}, fmt.Errorf("kernel MCP evidence payload mismatch: %w", ErrKernelLocalOperationConflict)
	}
	rawInput, err := json.Marshal(input.Audit.Input)
	if err != nil || digestKernelMCPAuditBytes(rawInput) != input.RequestSHA256 || !kernelMCPJSONEqual(payload.ToolInput, rawInput) {
		return FrameEvent{}, fmt.Errorf("kernel MCP evidence request mismatch: %w", ErrKernelLocalOperationConflict)
	}
	rawResult, err := json.Marshal(input.Audit.Result)
	if err != nil || digestKernelMCPAuditBytes(rawResult) != input.ResultSHA256 {
		return FrameEvent{}, fmt.Errorf("kernel MCP evidence result mismatch: %w", ErrKernelLocalOperationConflict)
	}
	descriptor, _, externalized, descriptorErr := toolcontract.DecodeExternalizedResult(payload.ToolResult)
	if descriptorErr != nil {
		return FrameEvent{}, descriptorErr
	}
	if externalized {
		if descriptor.Outcome != "succeeded" ||
			descriptor.SHA256 != input.ResultSHA256 || descriptor.SizeBytes != int64(len(rawResult)) {
			return FrameEvent{}, fmt.Errorf("kernel MCP evidence result mismatch: %w", ErrKernelLocalOperationConflict)
		}
		record, found, err := scanRunnerLargeToolResultRow(tx.QueryRowContext(ctx,
			`SELECT `+runnerLargeToolResultSelect+` WHERE version_id=? AND owner_user_id=?`, descriptor.VersionID, operation.OwnerUserID))
		if err != nil {
			return FrameEvent{}, err
		}
		if !found || record.ArtifactID != descriptor.ArtifactID || record.StreamUID != operation.StreamUID ||
			record.ProjectID != operation.ProjectID || record.RootFrameID != operation.RootFrameID || record.FrameID != operation.FrameID ||
			record.ToolCallID != input.HostCallID || record.ToolName != input.ToolName || record.SourceEventID != operation.SourceEventID ||
			record.RunnerID != input.Claim.RunnerID || record.ClaimToken != input.Claim.ClaimToken || record.Attempt != input.Claim.Attempt ||
			record.ContentSHA256 != descriptor.SHA256 || record.SizeBytes != descriptor.SizeBytes || record.ContentType != descriptor.ContentType {
			return FrameEvent{}, fmt.Errorf("kernel MCP evidence result reference mismatch: %w", ErrKernelLocalOperationConflict)
		}
	} else if !kernelMCPJSONEqual(payload.ToolResult, rawResult) {
		return FrameEvent{}, fmt.Errorf("kernel MCP evidence result mismatch: %w", ErrKernelLocalOperationConflict)
	}
	return s.finishKernelMCPAuditTx(ctx, tx, input.Audit, rawInput, rawResult, event.EventID)
}

func validKernelMCPEvidenceSHA256(value string) bool {
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

func kernelMCPJSONEqual(left, right []byte) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || leftDecoder.Decode(&struct{}{}) == nil ||
		rightDecoder.Decode(&rightValue) != nil || rightDecoder.Decode(&struct{}{}) == nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}
