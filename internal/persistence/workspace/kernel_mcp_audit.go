package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type KernelMCPAuditInput struct {
	CallID      string
	FrameID     string
	RootFrameID string
	OwnerUserID string
	Server      string
	Method      string
	Input       map[string]any
}

type KernelMCPAuditTerminalInput struct {
	KernelMCPAuditInput
	Status string
	Reason string
	Result any
}

func (s *Store) BeginKernelMCPAudit(ctx context.Context, input KernelMCPAuditInput) (FrameEvent, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, errors.New("workspace store is closed")
	}
	input = normalizeKernelMCPAuditInput(input)
	if err := validateKernelMCPAuditInput(input); err != nil {
		return FrameEvent{}, err
	}
	rawInput, err := json.Marshal(input.Input)
	if err != nil {
		return FrameEvent{}, errors.New("kernel MCP audit input is invalid")
	}
	var event FrameEvent
	err = transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateNotificationAuthorityTx(ctx, tx, input.FrameID, input.FrameID, input.RootFrameID, input.OwnerUserID, false); err != nil {
			return err
		}
		var appendErr error
		event, appendErr = appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
			ID:      kernelMCPAuditEventID("start", input.FrameID, input.CallID),
			FrameID: input.FrameID,
			Type:    "kernel_mcp_audit_started",
			Payload: map[string]any{
				"call_id": input.CallID, "server_digest": digestKernelMCPAuditValue(input.Server),
				"method": input.Method, "input_digest": digestKernelMCPAuditBytes(rawInput), "input_bytes": len(rawInput),
			},
		}, s.now().UTC())
		return appendErr
	})
	return event, err
}

func (s *Store) FinishKernelMCPAudit(ctx context.Context, input KernelMCPAuditTerminalInput) (FrameEvent, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, errors.New("workspace store is closed")
	}
	input.KernelMCPAuditInput = normalizeKernelMCPAuditInput(input.KernelMCPAuditInput)
	input.Status = strings.TrimSpace(input.Status)
	input.Reason = strings.TrimSpace(input.Reason)
	if err := validateKernelMCPAuditInput(input.KernelMCPAuditInput); err != nil {
		return FrameEvent{}, err
	}
	if input.Status != "completed" && input.Status != "blocked" && input.Status != "failed" && input.Status != "cancelled" {
		return FrameEvent{}, errors.New("kernel MCP audit terminal status is invalid")
	}
	rawResult, err := json.Marshal(input.Result)
	if err != nil {
		rawResult = []byte("null")
	}
	rawInput, err := json.Marshal(input.Input)
	if err != nil {
		return FrameEvent{}, errors.New("kernel MCP audit input is invalid")
	}
	var event FrameEvent
	err = transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		var finishErr error
		event, finishErr = s.finishKernelMCPAuditTx(ctx, tx, input, rawInput, rawResult, 0)
		return finishErr
	})
	return event, err
}

func (s *Store) finishKernelMCPAuditTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	input KernelMCPAuditTerminalInput,
	rawInput, rawResult []byte,
	transcriptEventID int64,
) (FrameEvent, error) {
	if s == nil || tx == nil {
		return FrameEvent{}, errors.New("kernel MCP audit transaction is unavailable")
	}
	if err := validateNotificationAuthorityTx(ctx, tx, input.FrameID, input.FrameID, input.RootFrameID, input.OwnerUserID, false); err != nil {
		return FrameEvent{}, err
	}
	startID := kernelMCPAuditEventID("start", input.FrameID, input.CallID)
	var startCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_events
		WHERE id=? AND frame_id=? AND event_type='kernel_mcp_audit_started'`, startID, input.FrameID).Scan(&startCount); err != nil {
		return FrameEvent{}, err
	}
	if startCount != 1 {
		return FrameEvent{}, errors.New("kernel MCP audit start is unavailable")
	}
	payload := map[string]any{
		"call_id": input.CallID, "status": input.Status, "reason": input.Reason,
		"input_digest": digestKernelMCPAuditBytes(rawInput), "input_bytes": len(rawInput),
		"result_digest": digestKernelMCPAuditBytes(rawResult), "result_bytes": len(rawResult),
	}
	if transcriptEventID > 0 {
		payload["transcript_event_id"] = transcriptEventID
	}
	return appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
		ID: kernelMCPAuditEventID("terminal", input.FrameID, input.CallID), FrameID: input.FrameID,
		Type: "kernel_mcp_audit_terminal", Payload: payload,
	}, s.now().UTC())
}

func normalizeKernelMCPAuditInput(input KernelMCPAuditInput) KernelMCPAuditInput {
	input.CallID = strings.TrimSpace(input.CallID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.Server = strings.TrimSpace(input.Server)
	input.Method = strings.TrimSpace(input.Method)
	if input.Input == nil {
		input.Input = map[string]any{}
	}
	return input
}

func validateKernelMCPAuditInput(input KernelMCPAuditInput) error {
	if input.CallID == "" || input.FrameID == "" || input.RootFrameID == "" || input.OwnerUserID == "" || input.Server == "" || input.Method == "" {
		return errors.New("kernel MCP audit identity is incomplete")
	}
	return nil
}

func kernelMCPAuditEventID(phase, frameID, callID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-mcp-audit:"+phase+":"+frameID+":"+callID)).String()
}

func digestKernelMCPAuditValue(value string) string {
	return digestKernelMCPAuditBytes([]byte(value))
}

func digestKernelMCPAuditBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
