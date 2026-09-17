package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	memoryMutationReceiptEventType = "memory_mutation_applied"
	memoryMutationReceiptVersion   = 1
	memoryMutationResultIDsMax     = 20
	memoryMutationResultIDBytesMax = 512
)

type memoryMutationReceiptPayload struct {
	Version        int                         `json:"version"`
	Kind           string                      `json:"kind"`
	SourceEventID  int64                       `json:"source_event_id"`
	InputSHA256    string                      `json:"input_sha256"`
	MutationSHA256 string                      `json:"mutation_sha256"`
	Result         MemoryMutationReceiptResult `json:"result"`
}

type memoryMutationSourcePayload struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	ToolPhase  string          `json:"toolPhase"`
	ToolInput  json.RawMessage `json:"toolInput"`
}

func MemoryMutationClientMessageID(streamUID string, sourceEventID int64) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(streamUID) + "\x00" + strconv.FormatInt(sourceEventID, 10)))
	return "memory-mutation:v1:" + hex.EncodeToString(digest[:])
}

// ValidateLiveRunnerClaim verifies the complete runner claim against the
// current active stream inside the caller's BEGIN IMMEDIATE transaction.
func (tx *ImmediateTransaction) ValidateLiveRunnerClaim(ctx context.Context, claim RunnerClaim) (Stream, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return Stream{}, ErrSchemaUnavailable
	}
	stream, err := validateClaimConn(ctx, tx.conn, claim, tx.repository.now().UTC(), true)
	return stream, schemaError(err)
}

// ValidateLiveMemoryMutationClaim keeps the focused memory API while sharing
// the generic live runner authority check with other claimed tool operations.
func (tx *ImmediateTransaction) ValidateLiveMemoryMutationClaim(ctx context.Context, claim RunnerClaim) (Stream, error) {
	return tx.ValidateLiveRunnerClaim(ctx, claim)
}

// ResolveMemoryMutationInvocation binds the logical memory write to its
// durable runner checkpoint. A current live claim for the same task attempt is
// always required. A rotated lease for that attempt may replay an existing
// receipt; a later user task may not read or create a receipt for the old task.
func (tx *ImmediateTransaction) ResolveMemoryMutationInvocation(
	ctx context.Context,
	claim RunnerClaim,
	sourceEventID int64,
) (MemoryMutationInvocation, error) {
	stream, err := tx.ValidateLiveMemoryMutationClaim(ctx, claim)
	if err != nil {
		return MemoryMutationInvocation{}, err
	}
	invocation, err := resolveMemoryMutationSource(ctx, tx.conn, stream, sourceEventID)
	if err != nil {
		return MemoryMutationInvocation{}, schemaError(err)
	}
	event, found, err := findEventByClientID(ctx, tx.conn, stream.UID, invocation.ClientMessageID)
	if err != nil {
		return MemoryMutationInvocation{}, schemaError(err)
	}
	if found {
		receipt, err := decodeMemoryMutationReceipt(event)
		if err != nil || receipt.SourceEventID != invocation.SourceEventID ||
			receipt.InputSHA256 != invocation.InputSHA256 || event.RunnerAttempt == nil ||
			*event.RunnerAttempt != invocation.SourceAttempt {
			return MemoryMutationInvocation{}, ErrMemoryMutationReceiptConflict
		}
		invocation.Receipt = &receipt
		return invocation, nil
	}
	if invocation.SourceAttempt != claim.Attempt {
		return MemoryMutationInvocation{}, ErrMemoryMutationReceiptConflict
	}
	return invocation, nil
}

// AppendMemoryMutationReceipt writes the bounded, non-delivered receipt in
// the same transaction as the workspace memory mutation.
func (tx *ImmediateTransaction) AppendMemoryMutationReceipt(
	ctx context.Context,
	input MemoryMutationReceiptInput,
) (MemoryMutationReceipt, error) {
	if err := validateClaimInput(input.Claim); err != nil {
		return MemoryMutationReceipt{}, err
	}
	if input.SourceEventID <= 0 {
		return MemoryMutationReceipt{}, errors.New("memory mutation source event id is required")
	}
	input.InputSHA256 = normalizeMemoryMutationDigest(input.InputSHA256)
	input.MutationSHA256 = normalizeMemoryMutationDigest(input.MutationSHA256)
	if input.InputSHA256 == "" {
		return MemoryMutationReceipt{}, errors.New("memory mutation input digest must be SHA-256")
	}
	if input.MutationSHA256 == "" {
		return MemoryMutationReceipt{}, errors.New("memory mutation plan digest must be SHA-256")
	}
	result, err := normalizeMemoryMutationReceiptResult(input.Result)
	if err != nil {
		return MemoryMutationReceipt{}, err
	}
	invocation, err := tx.ResolveMemoryMutationInvocation(ctx, input.Claim, input.SourceEventID)
	if err != nil {
		return MemoryMutationReceipt{}, err
	}
	if invocation.Receipt != nil || invocation.InputSHA256 != input.InputSHA256 || invocation.SourceAttempt != input.Claim.Attempt {
		return MemoryMutationReceipt{}, ErrMemoryMutationReceiptConflict
	}
	payload, err := json.Marshal(memoryMutationReceiptPayload{
		Version: memoryMutationReceiptVersion, Kind: "memory_mutation",
		SourceEventID: invocation.SourceEventID, InputSHA256: invocation.InputSHA256,
		MutationSHA256: input.MutationSHA256, Result: result,
	})
	if err != nil {
		return MemoryMutationReceipt{}, err
	}
	attempt := input.Claim.Attempt
	event, created, err := appendEventConn(ctx, tx.conn, invocation.Stream, eventRecord{
		clientMessageID: invocation.ClientMessageID,
		eventType:       memoryMutationReceiptEventType,
		source:          EventSourcePayload,
		runnerAttempt:   &attempt,
		payloadJSON:     payload,
		createdAt:       tx.repository.now().UTC(),
	})
	if err != nil {
		return MemoryMutationReceipt{}, schemaError(err)
	}
	if !created {
		return MemoryMutationReceipt{}, ErrMemoryMutationReceiptConflict
	}
	receipt, err := decodeMemoryMutationReceipt(event)
	return receipt, schemaError(err)
}

func resolveMemoryMutationSource(ctx context.Context, conn *sql.Conn, stream Stream, eventID int64) (MemoryMutationInvocation, error) {
	if eventID <= 0 {
		return MemoryMutationInvocation{}, errors.New("memory mutation source event id is required")
	}
	var eventType string
	var runnerAttempt sql.NullInt64
	var payloadJSON []byte
	err := conn.QueryRowContext(ctx, `SELECT event_type,runner_attempt,payload_json FROM transcript_events
		WHERE stream_uid=? AND event_id=?`, stream.UID, eventID).Scan(&eventType, &runnerAttempt, &payloadJSON)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (!runnerAttempt.Valid || runnerAttempt.Int64 <= 0 || eventType != "runner_checkpoint") {
		return MemoryMutationInvocation{}, ErrMemoryMutationReceiptConflict
	}
	if err != nil {
		return MemoryMutationInvocation{}, err
	}
	var source memoryMutationSourcePayload
	if json.Unmarshal(payloadJSON, &source) != nil || strings.TrimSpace(source.ToolCallID) == "" ||
		strings.TrimSpace(source.ToolName) != "write_memory" || strings.TrimSpace(source.ToolPhase) != "start" {
		return MemoryMutationInvocation{}, ErrMemoryMutationReceiptConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(source.ToolInput))
	decoder.UseNumber()
	var decodedInput any
	if len(source.ToolInput) == 0 || decoder.Decode(&decodedInput) != nil {
		return MemoryMutationInvocation{}, ErrMemoryMutationReceiptConflict
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return MemoryMutationInvocation{}, ErrMemoryMutationReceiptConflict
	}
	toolInput, ok := decodedInput.(map[string]any)
	if !ok || toolInput == nil {
		return MemoryMutationInvocation{}, ErrMemoryMutationReceiptConflict
	}
	canonicalInput, err := json.Marshal(toolInput)
	if err != nil {
		return MemoryMutationInvocation{}, ErrMemoryMutationReceiptConflict
	}
	digest := sha256.Sum256(canonicalInput)
	return MemoryMutationInvocation{
		Stream: stream, SourceEventID: eventID, SourceAttempt: runnerAttempt.Int64,
		ToolCallID: strings.TrimSpace(source.ToolCallID), ToolInputJSON: canonicalInput,
		InputSHA256:     hex.EncodeToString(digest[:]),
		ClientMessageID: MemoryMutationClientMessageID(stream.UID, eventID),
	}, nil
}

func normalizeMemoryMutationDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	digest, err := hex.DecodeString(value)
	if err != nil || len(digest) != sha256.Size {
		return ""
	}
	return value
}

func normalizeMemoryMutationReceiptResult(result MemoryMutationReceiptResult) (MemoryMutationReceiptResult, error) {
	var err error
	if result.Appended, err = normalizeMemoryMutationIDs("appended", result.Appended); err != nil {
		return MemoryMutationReceiptResult{}, err
	}
	if result.Replaced, err = normalizeMemoryMutationIDs("replaced", result.Replaced); err != nil {
		return MemoryMutationReceiptResult{}, err
	}
	if result.Removed, err = normalizeMemoryMutationIDs("removed", result.Removed); err != nil {
		return MemoryMutationReceiptResult{}, err
	}
	return result, nil
}

func normalizeMemoryMutationIDs(kind string, values []string) ([]string, error) {
	if len(values) > memoryMutationResultIDsMax {
		return nil, fmt.Errorf("memory mutation %s result exceeds %d ids", kind, memoryMutationResultIDsMax)
	}
	result := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > memoryMutationResultIDBytesMax {
			return nil, fmt.Errorf("memory mutation %s result contains an invalid id", kind)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("memory mutation %s result contains a duplicate id", kind)
		}
		seen[value] = struct{}{}
		result[index] = value
	}
	return result, nil
}

func decodeMemoryMutationReceipt(event Event) (MemoryMutationReceipt, error) {
	if event.Type != memoryMutationReceiptEventType || event.Source != EventSourcePayload || event.RunnerAttempt == nil || event.FrameEventID != nil {
		return MemoryMutationReceipt{}, ErrMemoryMutationReceiptConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(event.PayloadJSON))
	decoder.DisallowUnknownFields()
	var payload memoryMutationReceiptPayload
	if err := decoder.Decode(&payload); err != nil {
		return MemoryMutationReceipt{}, ErrMemoryMutationReceiptConflict
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return MemoryMutationReceipt{}, ErrMemoryMutationReceiptConflict
	}
	inputDigest := normalizeMemoryMutationDigest(payload.InputSHA256)
	mutationDigest := normalizeMemoryMutationDigest(payload.MutationSHA256)
	result, err := normalizeMemoryMutationReceiptResult(payload.Result)
	if payload.Version != memoryMutationReceiptVersion || payload.Kind != "memory_mutation" || payload.SourceEventID <= 0 ||
		inputDigest == "" || mutationDigest == "" || err != nil ||
		event.ClientMessageID != MemoryMutationClientMessageID(event.StreamUID, payload.SourceEventID) {
		return MemoryMutationReceipt{}, ErrMemoryMutationReceiptConflict
	}
	return MemoryMutationReceipt{
		Event: event, SourceEventID: payload.SourceEventID, InputSHA256: inputDigest,
		MutationSHA256: mutationDigest, Result: result,
	}, nil
}
