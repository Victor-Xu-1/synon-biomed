package transcript

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
)

func (r *Repository) RunImmediate(ctx context.Context, fn func(*ImmediateTransaction) error) error {
	_, err := r.RunImmediateWithOutcome(ctx, fn)
	return err
}

// RunImmediateWithOutcome distinguishes callback rollback from a COMMIT call
// whose driver result may be uncertain. Callers that transferred ownership of
// external resources before COMMIT must preserve those resources whenever
// CommitAttempted is true, then resolve the durable receipt independently.
func (r *Repository) RunImmediateWithOutcome(
	ctx context.Context,
	fn func(*ImmediateTransaction) error,
) (outcome ImmediateTransactionOutcome, err error) {
	if r == nil || r.db == nil {
		return outcome, ErrSchemaUnavailable
	}
	if fn == nil {
		return outcome, errors.New("immediate transaction callback is required")
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return outcome, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return outcome, err
	}
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	if err = fn(&ImmediateTransaction{repository: r, conn: conn}); err != nil {
		return outcome, err
	}
	outcome.CommitAttempted = true
	_, err = conn.ExecContext(ctx, "COMMIT")
	if err == nil {
		outcome.Committed = true
	}
	return outcome, err
}

func (r *Repository) withImmediate(ctx context.Context, fn func(*sql.Conn) error) error {
	return r.RunImmediate(ctx, func(tx *ImmediateTransaction) error { return fn(tx.conn) })
}

func (r *Repository) newClaimToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(r.rand, raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

func validateRunnerEventInput(input AppendEventInput) error {
	if err := validateClaimInput(input.Claim); err != nil {
		return err
	}
	eventType := strings.TrimSpace(input.Type)
	if strings.TrimSpace(input.ClientMessageID) == "" || eventType == "" {
		return errors.New("client message id and event type are required")
	}
	if reservedRunnerEventType(eventType) {
		return ErrReservedEventType
	}
	return validatePayload(input.Source, input.PayloadJSON, input.FrameEventID)
}

func reservedRunnerEventType(eventType string) bool {
	switch eventType {
	case "user_message", "user_message_staged", "user_input_response", "runner_checkpoint", "runner_finished", "runner_reclaimed",
		AskUserPromptEventType, AskUserResultEventType, memoryMutationReceiptEventType:
		return true
	default:
		return false
	}
}

func validateClaimInput(claim RunnerClaim) error {
	if strings.TrimSpace(claim.StreamUID) == "" || strings.TrimSpace(claim.OwnerID) == "" ||
		strings.TrimSpace(claim.RunnerID) == "" || claim.Attempt <= 0 || strings.TrimSpace(claim.ClaimToken) == "" ||
		claim.ClaimedInputRevision <= 0 {
		return errors.New("complete runner claim is required")
	}
	return validateResumeRequest(claim.ResumeSource, claim.ResumeCheckpoint)
}

func validatePayload(source EventSource, payload []byte, frameEventID *string) error {
	switch source {
	case EventSourceFrameRef:
		if frameEventID == nil || strings.TrimSpace(*frameEventID) == "" || len(*frameEventID) > 512 || len(payload) != 0 {
			return errors.New("frame reference events require one frame event id and no copied payload")
		}
	case EventSourcePayload:
		if frameEventID != nil || len(payload) == 0 || len(payload) > maxEventPayloadBytes || !json.Valid(payload) {
			return errors.New("payload events require bounded valid JSON and no frame event id")
		}
	default:
		return errors.New("unsupported transcript event source")
	}
	return nil
}

func eventMatches(event Event, record eventRecord) bool {
	return event.Type == record.eventType && event.Source == record.source &&
		nullableInt64Equal(event.RunnerAttempt, record.runnerAttempt) && jsonEqual(event.PayloadJSON, record.payloadJSON) &&
		nullableStringEqual(event.FrameEventID, record.frameEventID)
}

func jsonEqual(left, right []byte) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil && reflect.DeepEqual(l, r)
}

func normalizedDestinations(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func terminalDeliveryDestinations(stream Stream, values []string) []string {
	if stream.Kind == StreamKindFrameRef && strings.TrimSpace(stream.SessionID) != "" {
		values = append(append([]string(nil), values...), "ws")
	}
	return normalizedDestinations(values)
}

func equalDigest(expected []byte, token string) bool {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return len(expected) == len(digest) && string(expected) == string(digest[:])
}

func nullablePayload(payload []byte) any {
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func nullableInt64Equal(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func nullableStringEqual(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func validateFrameEventReferenceConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	eventType string,
	source EventSource,
	frameEventID *string,
) error {
	if source != EventSourceFrameRef {
		return nil
	}
	if stream.Kind != StreamKindFrameRef || frameEventID == nil {
		return ErrEventConflict
	}
	var frameID, storedType, payload string
	err := conn.QueryRowContext(ctx, `SELECT frame_id,event_type,payload FROM frame_events WHERE id=?`, strings.TrimSpace(*frameEventID)).
		Scan(&frameID, &storedType, &payload)
	if errors.Is(err, sql.ErrNoRows) || frameID != stream.FrameID || storedType != strings.TrimSpace(eventType) ||
		len(payload) == 0 || len(payload) > maxEventPayloadBytes || !json.Valid([]byte(payload)) {
		return ErrEventConflict
	}
	return err
}

func validStreamKind(kind StreamKind) bool {
	return kind == StreamKindFrameRef || kind == StreamKindTaskRun || kind == StreamKindStandalone
}

func terminalStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func schemaError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err == nil || errors.Is(err, ErrOwnerMismatch) || errors.Is(err, ErrClaimStale) || errors.Is(err, ErrEventConflict) ||
		errors.Is(err, ErrArtifactMismatch) || errors.Is(err, ErrArtifactMissing) || errors.Is(err, ErrDeliveryClaimStale) ||
		errors.Is(err, ErrCheckpointUnavailable) || errors.Is(err, ErrReservedEventType) ||
		errors.Is(err, ErrDeliveryRouteInactive) || errors.Is(err, ErrDeliveryRouteStale) || errors.Is(err, ErrDeliveryRouteImmutable) ||
		errors.Is(err, ErrTerminalProjectionUnavailable) || errors.Is(err, ErrBranchStateStale) ||
		errors.Is(err, ErrHistoryAuditBudgetExceeded) || errors.Is(err, ErrHistoryBackfillBlocked) ||
		errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return errors.Join(ErrSchemaUnavailable, err)
}
