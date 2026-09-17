package transcript

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const TerminalToolRecoveryEventType = "tool_recovery_settlement"

// AppendTerminalToolRecoveryEvent appends a machine-only settlement event
// after a Frame and its runner attempt are already terminal. It is deliberately
// narrower than AppendRunnerEvent: recovery has no live runner claim and may
// only record an exact durable terminal result (or outcome_unknown when no
// result evidence exists) for an existing active-branch batch.
type AppendTerminalToolRecoveryEventInput struct {
	StreamUID    string
	OwnerID      string
	BatchID      string
	Ordinal      int64
	ToolCallID   string
	PayloadJSON  []byte
	Destinations []string
}

func (tx *ImmediateTransaction) AppendTerminalToolRecoveryEvent(
	ctx context.Context,
	input AppendTerminalToolRecoveryEventInput,
) (Event, bool, error) {
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.BatchID = strings.TrimSpace(input.BatchID)
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	if tx == nil || tx.repository == nil || tx.conn == nil || ctx == nil ||
		input.StreamUID == "" || input.OwnerID == "" || input.BatchID == "" ||
		input.Ordinal < 0 || input.ToolCallID == "" {
		return Event{}, false, errors.New("complete terminal tool recovery authority is required")
	}
	if err := validatePayload(EventSourcePayload, input.PayloadJSON, nil); err != nil {
		return Event{}, false, err
	}
	var payload struct {
		Version    int             `json:"version"`
		Status     string          `json:"status"`
		ToolCallID string          `json:"toolCallId"`
		ToolPhase  string          `json:"toolPhase"`
		ToolResult json.RawMessage `json:"toolResult"`
	}
	if json.Unmarshal(input.PayloadJSON, &payload) != nil || payload.Version != 1 ||
		!terminalToolRecoveryStatus(payload.Status) || payload.ToolPhase != payload.Status ||
		payload.ToolCallID != input.ToolCallID || len(payload.ToolResult) == 0 {
		return Event{}, false, errors.New("terminal tool recovery payload is invalid")
	}
	stream, err := getStreamConn(ctx, tx.conn, input.StreamUID, input.OwnerID)
	if err != nil {
		return Event{}, false, schemaError(err)
	}
	status, terminal, err := frameTerminalStatusConn(ctx, tx.conn, stream)
	if err != nil {
		return Event{}, false, schemaError(err)
	}
	if !terminal || status == "" {
		return Event{}, false, ErrEventConflict
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		input.StreamUID, input.BatchID, strconv.FormatInt(input.Ordinal, 10), input.ToolCallID,
	}, "\x00")))
	event, created, err := appendEventConn(ctx, tx.conn, stream, eventRecord{
		clientMessageID: "terminal-tool-recovery:v1:" + hex.EncodeToString(digest[:]),
		eventType:       TerminalToolRecoveryEventType,
		source:          EventSourcePayload,
		payloadJSON:     input.PayloadJSON,
		destinations:    input.Destinations,
		createdAt:       tx.repository.now().UTC(),
	})
	return event, created, schemaError(err)
}

func terminalToolRecoveryStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "outcome_unknown":
		return true
	default:
		return false
	}
}
