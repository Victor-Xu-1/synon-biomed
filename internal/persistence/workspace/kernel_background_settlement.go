package workspace

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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	KernelResultSettlementOutboxTopic = "kernel.result.settlement"

	kernelResultSettlementOutboxType          = "kernel.background.settlement.v1"
	kernelResultSettlementLegacySchemaVersion = 1
	kernelResultSettlementSchemaVersion       = 2
	kernelResultSettlementMaxOutputLimitBytes = int64(1024 * 1024)
	kernelResultSettlementOutputRunes         = 262144
	kernelResultSettlementMaxDroppedRoots     = 1024
	kernelResultSettlementMaxTextBytes        = 4096
	kernelResultSettlementMaxAttempts         = 1000
	kernelResultSettlementRealtimePreviewMax  = int64(240 * 1024)
)

type KernelBackgroundSettlementOperationInput struct {
	Operation          KernelLocalOperation
	Claim              transcriptstore.RunnerClaim
	TerminalState      string
	ReasonCode         string
	TerminalResultJSON json.RawMessage
	TerminalResultRef  string
}

type EnqueueKernelBackgroundSettlementInput struct {
	ExecutionLog SaveExecutionLogInput
	Operation    *KernelBackgroundSettlementOperationInput
	Notification CreateNotificationInput
	// ToolID lets the store derive the complete notification payload from the
	// canonical staged result. Legacy callers may leave it empty and provide a
	// payload, which remains byte-compared for compatibility.
	ToolID           string
	ResultCode       string
	Reused           bool
	DroppedRoots     []string
	DurationMS       int64
	OutputLimitBytes int64
}

// KernelBackgroundSettlement is deliberately a small reference envelope. The
// execution output and operation tool result are staged in their existing
// authorities before this envelope is enqueued.
type KernelBackgroundSettlement struct {
	SchemaVersion      int      `json:"schemaVersion"`
	ExecutionID        string   `json:"executionId"`
	ExecutionLogSHA256 string   `json:"executionLogSha256"`
	FrameID            string   `json:"frameId"`
	OperationID        string   `json:"operationId,omitempty"`
	OperationVersion   int64    `json:"operationVersion,omitempty"`
	BootID             string   `json:"bootId,omitempty"`
	TerminalState      string   `json:"terminalState,omitempty"`
	ReasonCode         string   `json:"reasonCode,omitempty"`
	MaterializationSHA string   `json:"materializationSha256,omitempty"`
	NotificationID     string   `json:"notificationId"`
	NotificationSender string   `json:"notificationSender"`
	NotificationTarget string   `json:"notificationTarget"`
	NotificationRoot   string   `json:"notificationRoot"`
	NotificationOwner  string   `json:"notificationOwner"`
	NotificationType   string   `json:"notificationType"`
	ToolID             string   `json:"toolId"`
	ResultCode         string   `json:"resultCode,omitempty"`
	Reused             bool     `json:"reused"`
	DroppedRoots       []string `json:"droppedRoots,omitempty"`
	DurationMS         int64    `json:"durationMs"`
	OutputLimitBytes   int64    `json:"outputLimitBytes"`
}

func (s *Store) EnqueueKernelBackgroundSettlement(
	ctx context.Context,
	input EnqueueKernelBackgroundSettlementInput,
) (OutboxEvent, error) {
	if s == nil || s.db == nil || ctx == nil {
		return OutboxEvent{}, ErrWorkspaceStoreClosed
	}
	prepared, err := s.prepareExecutionLog(input.ExecutionLog)
	if err != nil {
		return OutboxEvent{}, err
	}
	if input.DurationMS < 0 {
		return OutboxEvent{}, errors.New("kernel settlement duration must be non-negative")
	}
	if input.OutputLimitBytes <= 0 {
		input.OutputLimitBytes = kernelResultSettlementMaxOutputLimitBytes
	}
	if input.OutputLimitBytes > kernelResultSettlementMaxOutputLimitBytes {
		return OutboxEvent{}, errors.New("kernel settlement output limit exceeds one MiB")
	}
	droppedRoots, err := normalizeKernelSettlementDroppedRoots(input.DroppedRoots)
	if err != nil {
		return OutboxEvent{}, err
	}
	toolID := strings.TrimSpace(input.ToolID)
	deriveNotification := toolID != ""
	notificationForValidation := input.Notification
	if deriveNotification {
		notificationForValidation.Payload = map[string]any{
			"exec_id": prepared.record.ID, "tool_id": toolID,
		}
	}
	toolID, err = validateKernelSettlementNotification(
		notificationForValidation, prepared.record.ID, prepared.record.FrameID,
	)
	if err != nil {
		return OutboxEvent{}, err
	}
	executionLogSHA, err := kernelSettlementExecutionLogSHA256(prepared.record)
	if err != nil {
		return OutboxEvent{}, err
	}

	envelope := KernelBackgroundSettlement{
		SchemaVersion: kernelResultSettlementSchemaVersion,
		ExecutionID:   prepared.record.ID, ExecutionLogSHA256: executionLogSHA, FrameID: prepared.record.FrameID,
		NotificationID:     strings.TrimSpace(input.Notification.ID),
		NotificationSender: strings.TrimSpace(input.Notification.SenderFrameID),
		NotificationTarget: strings.TrimSpace(input.Notification.RecipientFrameID),
		NotificationRoot:   strings.TrimSpace(input.Notification.RootFrameID),
		NotificationOwner:  strings.TrimSpace(input.Notification.OwnerUserID),
		NotificationType:   strings.TrimSpace(input.Notification.NotificationType),
		ToolID:             toolID, ResultCode: strings.TrimSpace(input.ResultCode),
		Reused: input.Reused, DroppedRoots: droppedRoots,
		DurationMS: input.DurationMS, OutputLimitBytes: input.OutputLimitBytes,
	}
	var materialization KernelToolResultMaterialization
	if input.Operation != nil {
		operationInput := input.Operation
		operationInput.TerminalState = strings.TrimSpace(operationInput.TerminalState)
		operationInput.ReasonCode, err = normalizeKernelLocalOperationReason(operationInput.ReasonCode)
		if err != nil {
			return OutboxEvent{}, err
		}
		if !validKernelSettlementTerminalState(operationInput.TerminalState) {
			return OutboxEvent{}, errors.New("kernel settlement terminal state is invalid")
		}
		if operationInput.Operation.OperationID == "" || operationInput.Operation.State != KernelLocalOperationStateStarted ||
			operationInput.Operation.StateVersion <= 0 || operationInput.Operation.ExecutionID != prepared.record.ID ||
			operationInput.Operation.FrameID != prepared.record.FrameID || operationInput.Operation.BootID == "" {
			return OutboxEvent{}, errors.New("complete started kernel operation settlement is required")
		}
		materialization, err = normalizeKernelToolResultMaterialization(
			operationInput.Operation.OperationID, operationInput.TerminalResultJSON,
			operationInput.TerminalResultRef, executionLogSHA, "native_v41", prepared.record.ExecutedAt,
		)
		if err != nil {
			return OutboxEvent{}, err
		}
		envelope.OperationID = operationInput.Operation.OperationID
		envelope.OperationVersion = operationInput.Operation.StateVersion
		envelope.BootID = operationInput.Operation.BootID
		envelope.TerminalState = operationInput.TerminalState
		envelope.ReasonCode = operationInput.ReasonCode
		envelope.MaterializationSHA = materialization.TerminalResultSHA256
	}
	resultJSON := materialization.TerminalResultJSON
	if input.Operation == nil {
		if !validKernelSettlementResultCode(envelope.ResultCode) {
			return OutboxEvent{}, errors.New("kernel settlement result code is invalid")
		}
		resultJSON, err = kernelSettlementResultJSON(prepared.record, toolID, envelope.ResultCode, input.Reused, droppedRoots)
		if err != nil {
			return OutboxEvent{}, err
		}
	}
	expectedNotification, err := kernelSettlementNotificationPayload(
		prepared.record.ID, toolID, resultJSON, input.OutputLimitBytes,
	)
	if err != nil {
		return OutboxEvent{}, errors.New("kernel settlement notification could not be derived from the staged result")
	}
	if deriveNotification {
		input.Notification.Payload = expectedNotification
	} else if !canonicalJSONEqual(expectedNotification, input.Notification.Payload) {
		return OutboxEvent{}, errors.New("kernel settlement notification does not match the staged result")
	}

	rawEnvelope, err := json.Marshal(envelope)
	if err != nil {
		return OutboxEvent{}, err
	}
	identity := "execution:" + prepared.record.FrameID + ":" + prepared.record.ID
	aggregateType, aggregateID := "kernel_execution", prepared.record.ID
	if envelope.OperationID != "" {
		identity = "operation:" + envelope.OperationID
		aggregateType, aggregateID = "kernel_local_operation", envelope.OperationID
	}
	var event OutboxEvent
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return OutboxEvent{}, err
	}
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if input.Operation != nil {
			if err := validateKernelSettlementOperationTx(ctx, tx, *input.Operation, prepared.record); err != nil {
				return err
			}
		} else if err := validateUnboundKernelSettlementStartTx(ctx, tx, prepared.record.FrameID, prepared.record.ID); err != nil {
			return err
		}
		if _, err := ensureKernelSettlementExecutionLogTx(ctx, tx, prepared); err != nil {
			return err
		}
		if input.Operation != nil {
			if err := ensureStagedKernelToolResultMaterializationTx(
				ctx, tx, materialization, prepared.record.ID,
			); err != nil {
				return err
			}
		}
		var enqueueErr error
		event, enqueueErr = s.enqueueOutboxTransaction(ctx, tx, EnqueueOutboxInput{
			IdempotencyKey: identity, Topic: KernelResultSettlementOutboxTopic,
			PartitionKey: "kernel-result:" + prepared.record.FrameID,
			Type:         kernelResultSettlementOutboxType, AggregateType: aggregateType, AggregateID: aggregateID,
			Payload: rawEnvelope, MaxAttempts: kernelResultSettlementMaxAttempts,
		})
		return enqueueErr
	})
	return event, err
}

// DecodeKernelBackgroundSettlement strictly decodes the small durable
// envelope. Delivery authorization is checked separately against the current
// inflight outbox claim.
func DecodeKernelBackgroundSettlement(event OutboxEvent) (KernelBackgroundSettlement, error) {
	if event.Topic != KernelResultSettlementOutboxTopic || event.Type != kernelResultSettlementOutboxType {
		return KernelBackgroundSettlement{}, errors.New("outbox event is not a kernel result settlement")
	}
	if err := rejectDuplicateKernelSettlementJSONFields(event.Payload); err != nil {
		return KernelBackgroundSettlement{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(event.Payload))
	decoder.DisallowUnknownFields()
	var envelope KernelBackgroundSettlement
	if err := decoder.Decode(&envelope); err != nil {
		return KernelBackgroundSettlement{}, errors.New("decode kernel result settlement")
	}
	if err := requireKernelSettlementJSONEOF(decoder); err != nil {
		return KernelBackgroundSettlement{}, err
	}
	if err := validateKernelSettlementEnvelope(event, envelope); err != nil {
		return KernelBackgroundSettlement{}, err
	}
	return envelope, nil
}

func (s *Store) DeliverKernelBackgroundSettlement(ctx context.Context, event OutboxEvent) error {
	if s == nil || s.db == nil || ctx == nil {
		return ErrWorkspaceStoreClosed
	}
	envelope, err := DecodeKernelBackgroundSettlement(event)
	if err != nil {
		return err
	}
	if event.Status != OutboxStatusInflight || strings.TrimSpace(event.ClaimOwner) == "" ||
		strings.TrimSpace(event.ClaimToken) == "" {
		return ErrOutboxClaimLost
	}
	record, found, err := s.GetExecutionLog(envelope.FrameID, envelope.ExecutionID)
	if err != nil || !found {
		if err != nil {
			return fmt.Errorf("kernel result settlement execution_log_integrity: %w", err)
		}
		return errors.New("kernel result settlement execution_log_integrity: staged execution log is unavailable")
	}
	record.AgentName, record.DelegateName = "", ""
	executionLogSHA, err := kernelSettlementExecutionLogSHA256(record)
	if err != nil || executionLogSHA != envelope.ExecutionLogSHA256 {
		return fmt.Errorf("kernel result settlement execution_log_integrity: %w", ErrKernelLocalOperationConflict)
	}
	if envelope.OperationID != "" {
		materialization, found, err := s.GetKernelToolResultMaterialization(ctx, envelope.OperationID)
		if err != nil || !found {
			if err != nil {
				return fmt.Errorf("kernel result settlement materialization_integrity: %w", err)
			}
			return errors.New("kernel result settlement materialization_integrity: staged materialization is unavailable")
		}
		if materialization.TerminalResultSHA256 != envelope.MaterializationSHA ||
			materialization.ExecutionLogSHA256 != envelope.ExecutionLogSHA256 {
			return fmt.Errorf("kernel result settlement materialization_integrity: %w", ErrKernelLocalOperationConflict)
		}
		notification, err := kernelSettlementNotificationInput(envelope, materialization.TerminalResultJSON)
		if err != nil {
			return fmt.Errorf("kernel result settlement materialization_integrity: %w", err)
		}
		_, err = s.finishKernelLocalOperation(ctx, FinishKernelLocalOperationInput{
			OwnerUserID: envelope.NotificationOwner, OperationID: envelope.OperationID,
			ExpectedStateVersion: envelope.OperationVersion, BootID: envelope.BootID,
			ExecutionID: envelope.ExecutionID, TerminalState: envelope.TerminalState, ReasonCode: envelope.ReasonCode,
			ExecutionLog:       SaveExecutionLogInput{Record: record},
			TerminalResultJSON: materialization.TerminalResultJSON, TerminalResultRef: materialization.ResultRef,
			Notification: &notification,
		}, &kernelSettlementDelivery{
			Claim: kernelSettlementOutboxClaim{
				EventID: event.ID, ClaimOwner: event.ClaimOwner, ClaimToken: event.ClaimToken,
			},
			Envelope: envelope,
		}, "")
		if err != nil {
			return fmt.Errorf("kernel result settlement finish_operation: %w", err)
		}
		return nil
	}
	if err := s.deliverUnboundKernelBackgroundSettlement(ctx, event, envelope, record); err != nil {
		return fmt.Errorf("kernel result settlement finish_operation: %w", err)
	}
	return nil
}

func (s *Store) deliverUnboundKernelBackgroundSettlement(
	ctx context.Context,
	event OutboxEvent,
	envelope KernelBackgroundSettlement,
	record ExecutionLogRecord,
) error {
	resultJSON, err := kernelSettlementResultJSON(record, envelope.ToolID, envelope.ResultCode, envelope.Reused, envelope.DroppedRoots)
	if err != nil {
		return err
	}
	notification, err := kernelSettlementNotificationInput(envelope, resultJSON)
	if err != nil {
		return err
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return err
	}
	return repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateKernelSettlementOutboxClaimTx(ctx, tx, kernelSettlementOutboxClaim{
			EventID: event.ID, ClaimOwner: event.ClaimOwner, ClaimToken: event.ClaimToken,
		}); err != nil {
			return err
		}
		startID := stableKernelBackgroundStartEventID(envelope.FrameID, envelope.ExecutionID)
		var starts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_events
			WHERE id=? AND frame_id=? AND event_type='kernel_execution_background_started'`,
			startID, envelope.FrameID).Scan(&starts); err != nil || starts != 1 {
			return errors.New("background kernel execution start is unavailable")
		}
		lostID := stableKernelBackgroundLostEventID(envelope.FrameID, envelope.ExecutionID)
		var lost int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_events
			WHERE id=? AND frame_id=? AND event_type='kernel_execution_background_lost'`,
			lostID, envelope.FrameID).Scan(&lost); err != nil {
			return err
		}
		if lost != 0 {
			return errors.New("background kernel execution is already terminal")
		}
		_, notificationEvent, err := createNotificationTx(ctx, tx, notification, s.now().UTC())
		if err != nil {
			return err
		}
		if err := enqueueKernelSettlementRealtimeTx(ctx, s, tx, envelope, record, notificationEvent); err != nil {
			return fmt.Errorf("kernel result settlement realtime_projection: %w", err)
		}
		return nil
	})
}

// enqueueKernelSettlementRealtimeTx replaces the observer's former direct
// fanout. Both the durable notification projection and the legacy
// execution_cell_update done event commit with the delayed settlement.
func enqueueKernelSettlementRealtimeTx(
	ctx context.Context,
	store *Store,
	tx workspaceTransaction,
	envelope KernelBackgroundSettlement,
	record ExecutionLogRecord,
	notificationEvent FrameEvent,
) error {
	var frame Frame
	if err := tx.QueryRowContext(ctx, `SELECT id,project_id,root_frame_id,agent_name,status
		FROM frames WHERE id=?`, envelope.FrameID).Scan(
		&frame.ID, &frame.ProjectID, &frame.RootFrameID, &frame.AgentName, &frame.Status,
	); err != nil {
		return err
	}
	if frame.RootFrameID != envelope.NotificationRoot {
		return ErrKernelLocalOperationConflict
	}
	if notificationEvent.ID != "" {
		realtimeID := "frame-event:" + notificationEvent.ID
		if _, err := store.enqueueRealtimeOutboxTransaction(ctx, tx,
			FrameRealtimeEventInput(realtimeID, envelope.NotificationOwner, frame, notificationEvent),
			notificationEvent.ID, "",
		); err != nil {
			return err
		}
	}
	stdoutPreview, stderrPreview, stdoutTruncated, stderrTruncated := kernelSettlementRealtimePreviews(
		record.Stdout, record.Stderr, envelope.OutputLimitBytes,
	)
	done := RealtimeEventInput{
		ID:     "kernel-execution:" + envelope.ExecutionID + ":done",
		UserID: envelope.NotificationOwner, ProjectID: frame.ProjectID,
		RootFrameID: frame.RootFrameID, FrameID: frame.ID, Type: "execution_cell_update",
		Payload: map[string]any{
			"phase": "done", "origin": "agent", "language": record.Language,
			"environment": record.CondaEnv, "kernel_target": record.KernelKind,
			"tool_use_id": envelope.ToolID, "cell_id": envelope.ExecutionID,
			"execution_log_id": record.ID,
			"stdout":           stdoutPreview, "stderr": stderrPreview,
			"stdout_truncated": stdoutTruncated, "stderr_truncated": stderrTruncated,
			"cancelled": record.ExitStatus == "cancelled", "duration_ms": envelope.DurationMS,
			"files_written": record.FilesWritten, "dropped_roots": envelope.DroppedRoots,
		},
	}
	_, err := store.enqueueRealtimeOutboxTransaction(ctx, tx, done, "", "")
	return err
}

func kernelSettlementRealtimePreviews(
	stdout, stderr string,
	outputLimitBytes int64,
) (string, string, bool, bool) {
	budget := outputLimitBytes
	if budget <= 0 || budget > kernelResultSettlementRealtimePreviewMax {
		budget = kernelResultSettlementRealtimePreviewMax
	}
	stdout = truncateKernelSettlementRunes(stdout, kernelResultSettlementOutputRunes)
	stderr = truncateKernelSettlementRunes(stderr, kernelResultSettlementOutputRunes)
	stdoutBudget := budget
	if stderr != "" {
		stdoutBudget = budget * 3 / 4
	}
	stdoutPreview := truncateKernelSettlementBytes(stdout, stdoutBudget)
	remaining := budget - int64(len(stdoutPreview))
	if remaining < 0 {
		remaining = 0
	}
	stderrPreview := truncateKernelSettlementBytes(stderr, remaining)
	return stdoutPreview, stderrPreview, len(stdoutPreview) < len(stdout), len(stderrPreview) < len(stderr)
}

func validateKernelSettlementOperationTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	input KernelBackgroundSettlementOperationInput,
	record ExecutionLogRecord,
) error {
	stored, found, err := getKernelLocalOperationQuery(ctx, tx, input.Operation.OperationID)
	if err != nil || !found {
		if err != nil {
			return err
		}
		return ErrKernelLocalOperationConflict
	}
	claim := input.Claim
	if !kernelLocalOperationOriginMatches(stored, input.Operation) || stored.State != KernelLocalOperationStateStarted ||
		stored.StateVersion != input.Operation.StateVersion || stored.ExecutionID != record.ID ||
		stored.FrameID != record.FrameID || stored.ConfinementSHA256 != input.Operation.ConfinementSHA256 ||
		stored.RunnerID != input.Operation.RunnerID || stored.RunnerAttempt != input.Operation.RunnerAttempt ||
		stored.RunnerClaimSHA256 != input.Operation.RunnerClaimSHA256 || stored.BootID != input.Operation.BootID ||
		stored.KernelID != input.Operation.KernelID || stored.KernelGeneration != input.Operation.KernelGeneration ||
		stored.ExecutionID != input.Operation.ExecutionID ||
		stored.RunnerID != claim.RunnerID || stored.RunnerAttempt != claim.Attempt ||
		stored.StreamUID != claim.StreamUID || stored.OwnerUserID != claim.OwnerID ||
		kernelLocalOperationClaimSHA256(claim.ClaimToken) != stored.RunnerClaimSHA256 {
		return ErrKernelLocalOperationConflict
	}
	return validateKernelLocalOperationCurrentAuthority(ctx, tx, stored, false)
}

func validateUnboundKernelSettlementStartTx(
	ctx context.Context,
	tx workspaceTransaction,
	frameID, executionID string,
) error {
	startID := stableKernelBackgroundStartEventID(frameID, executionID)
	var starts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_events
		WHERE id=? AND frame_id=? AND event_type='kernel_execution_background_started'`,
		startID, frameID).Scan(&starts); err != nil || starts != 1 {
		return errors.New("background kernel execution start is unavailable")
	}
	lostID := stableKernelBackgroundLostEventID(frameID, executionID)
	var lost int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_events
		WHERE id=? AND frame_id=? AND event_type='kernel_execution_background_lost'`,
		lostID, frameID).Scan(&lost); err != nil {
		return err
	}
	if lost != 0 {
		return errors.New("background kernel execution is already terminal")
	}
	return nil
}

func ensureKernelSettlementExecutionLogTx(
	ctx context.Context,
	tx workspaceTransaction,
	prepared preparedExecutionLog,
) (ExecutionLogRecord, error) {
	existing, err := executionLogByIDTx(ctx, tx, prepared.record.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return saveExecutionLogTx(ctx, tx, prepared)
	}
	if err != nil {
		return ExecutionLogRecord{}, err
	}
	expectedSHA, expectedErr := kernelSettlementExecutionLogSHA256(prepared.record)
	existing.AgentName, existing.DelegateName = "", ""
	existingSHA, existingErr := kernelSettlementExecutionLogSHA256(existing)
	if expectedErr != nil || existingErr != nil || expectedSHA != existingSHA {
		return ExecutionLogRecord{}, errors.New("execution log id already identifies another execution")
	}
	if prepared.expectedCount > 0 {
		var ownerID, projectID, frameIncarnationID, rootIncarnationID string
		if err := tx.QueryRowContext(ctx, `SELECT project.user_id,frame.project_id,frame.incarnation_id,root.incarnation_id
			FROM frames frame JOIN projects project ON project.id=frame.project_id
			JOIN frames root ON root.id=frame.root_frame_id AND root.project_id=frame.project_id
			WHERE frame.id=?`, prepared.record.FrameID).Scan(
			&ownerID, &projectID, &frameIncarnationID, &rootIncarnationID,
		); err != nil {
			return ExecutionLogRecord{}, err
		}
		if ownerID != prepared.expectedOwnerID || projectID != prepared.expectedProjectID ||
			frameIncarnationID != prepared.expectedFrameIncarnationID ||
			rootIncarnationID != prepared.expectedRootFrameIncarnationID {
			return ExecutionLogRecord{}, errors.New("execution frame authority changed before the result was persisted")
		}
	}
	return existing, nil
}

func validateKernelSettlementOutboxClaimTx(
	ctx context.Context,
	tx workspaceTransaction,
	claim kernelSettlementOutboxClaim,
) error {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_outbox
		WHERE event_id=? AND topic=? AND event_type=? AND status='inflight'
			AND claim_owner=? AND claim_token=? AND lease_expires_at_ms>`+sqliteNowMillis,
		strings.TrimSpace(claim.EventID), KernelResultSettlementOutboxTopic,
		kernelResultSettlementOutboxType, strings.TrimSpace(claim.ClaimOwner),
		strings.TrimSpace(claim.ClaimToken)).Scan(&found)
	if err != nil {
		return err
	}
	if found != 1 {
		return ErrOutboxClaimLost
	}
	return nil
}

func validateKernelSettlementEnvelope(event OutboxEvent, envelope KernelBackgroundSettlement) error {
	for _, value := range []string{
		envelope.ExecutionID, envelope.ExecutionLogSHA256, envelope.FrameID, envelope.NotificationID,
		envelope.NotificationSender, envelope.NotificationTarget, envelope.NotificationRoot,
		envelope.NotificationOwner, envelope.NotificationType, envelope.ToolID,
	} {
		if strings.TrimSpace(value) == "" || len(value) > kernelResultSettlementMaxTextBytes {
			return errors.New("kernel result settlement identity is invalid")
		}
	}
	if (envelope.SchemaVersion != kernelResultSettlementLegacySchemaVersion &&
		envelope.SchemaVersion != kernelResultSettlementSchemaVersion) ||
		!validLowerHexSHA256(envelope.ExecutionLogSHA256) || envelope.DurationMS < 0 ||
		envelope.OutputLimitBytes <= 0 || envelope.OutputLimitBytes > kernelResultSettlementMaxOutputLimitBytes ||
		envelope.NotificationType != "cell_result" || envelope.FrameID != envelope.NotificationSender ||
		envelope.FrameID != envelope.NotificationTarget || event.MaxAttempts != kernelResultSettlementMaxAttempts ||
		event.PartitionKey != "kernel-result:"+envelope.FrameID {
		return errors.New("kernel result settlement bounds are invalid")
	}
	for _, optional := range []string{
		envelope.OperationID, envelope.BootID, envelope.TerminalState,
		envelope.ReasonCode, envelope.MaterializationSHA, envelope.ResultCode,
	} {
		if len(optional) > kernelResultSettlementMaxTextBytes {
			return errors.New("kernel result settlement optional identity is invalid")
		}
	}
	if _, err := normalizeKernelSettlementDroppedRoots(envelope.DroppedRoots); err != nil {
		return err
	}
	if envelope.OperationID == "" {
		if envelope.SchemaVersion == kernelResultSettlementLegacySchemaVersion && envelope.ResultCode != "" {
			return errors.New("legacy unbound kernel result settlement cannot carry a result code")
		}
		if !validKernelSettlementResultCode(envelope.ResultCode) {
			return errors.New("unbound kernel result settlement code is invalid")
		}
		if envelope.OperationVersion != 0 || envelope.BootID != "" || envelope.TerminalState != "" ||
			envelope.ReasonCode != "" || envelope.MaterializationSHA != "" ||
			event.AggregateType != "kernel_execution" || event.AggregateID != envelope.ExecutionID ||
			event.IdempotencyKey != "execution:"+envelope.FrameID+":"+envelope.ExecutionID {
			return errors.New("unbound kernel result settlement is invalid")
		}
		return nil
	}
	if envelope.OperationVersion <= 0 || envelope.BootID == "" || !validKernelSettlementTerminalState(envelope.TerminalState) ||
		envelope.ReasonCode == "" || !validLowerHexSHA256(envelope.MaterializationSHA) ||
		event.AggregateType != "kernel_local_operation" || event.AggregateID != envelope.OperationID ||
		event.IdempotencyKey != "operation:"+envelope.OperationID {
		return errors.New("operation kernel result settlement is invalid")
	}
	return nil
}

func validateKernelSettlementNotification(
	input CreateNotificationInput,
	executionID, frameID string,
) (string, error) {
	if input.allowCrossRoot || strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.SenderFrameID) == "" ||
		strings.TrimSpace(input.RecipientFrameID) == "" || strings.TrimSpace(input.RootFrameID) == "" ||
		strings.TrimSpace(input.OwnerUserID) == "" || strings.TrimSpace(input.NotificationType) != "cell_result" ||
		strings.TrimSpace(input.SenderFrameID) != frameID || strings.TrimSpace(input.RecipientFrameID) != frameID {
		return "", errors.New("complete same-root kernel settlement notification is required")
	}
	if strings.TrimSpace(stringValue(input.Payload["exec_id"])) != executionID {
		return "", errors.New("kernel settlement notification execution identity is invalid")
	}
	toolID := strings.TrimSpace(stringValue(input.Payload["tool_id"]))
	if toolID == "" || len(toolID) > kernelResultSettlementMaxTextBytes {
		return "", errors.New("kernel settlement notification tool identity is invalid")
	}
	return toolID, nil
}

func kernelSettlementNotificationInput(
	envelope KernelBackgroundSettlement,
	resultJSON []byte,
) (CreateNotificationInput, error) {
	payload, err := kernelSettlementNotificationPayload(
		envelope.ExecutionID, envelope.ToolID, resultJSON, envelope.OutputLimitBytes,
	)
	if err != nil {
		return CreateNotificationInput{}, err
	}
	return CreateNotificationInput{
		ID: envelope.NotificationID, SenderFrameID: envelope.NotificationSender,
		RecipientFrameID: envelope.NotificationTarget, RootFrameID: envelope.NotificationRoot,
		OwnerUserID: envelope.NotificationOwner, NotificationType: envelope.NotificationType, Payload: payload,
	}, nil
}

func kernelSettlementNotificationPayload(
	executionID, toolID string,
	resultJSON []byte,
	outputLimitBytes int64,
) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(resultJSON))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, errors.New("kernel settlement result is invalid")
	}
	if err := requireKernelSettlementJSONEOF(decoder); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	originalCharacters := utf8.RuneCount(encoded)
	output := truncateKernelSettlementRunes(string(encoded), kernelResultSettlementOutputRunes)
	output = truncateKernelSettlementBytes(output, outputLimitBytes)
	output = kernelNotificationTransportPreview(output)
	status := "completed"
	switch strings.TrimSpace(stringValue(result["exit_status"])) {
	case "cancelled":
		status = "interrupted"
	case "ok":
	default:
		status = "errored"
	}
	payload := map[string]any{
		"exec_id": executionID, "tool_id": toolID, "status": status, "output": output,
	}
	if utf8.RuneCountInString(output) < originalCharacters {
		payload["output_truncated_from_chars"] = originalCharacters
	}
	return payload, nil
}

// BuildKernelSettlementNotificationPayload is the single notification
// projection contract shared by live background execution and durable outbox
// settlement. Keeping truncation, status mapping, and JSON normalization here
// prevents a completed kernel result from being rejected because two packages
// independently rendered different previews of the same immutable bytes.
func BuildKernelSettlementNotificationPayload(
	executionID, toolID string,
	resultJSON []byte,
	outputLimitBytes int64,
) (map[string]any, error) {
	return kernelSettlementNotificationPayload(executionID, toolID, resultJSON, outputLimitBytes)
}

func kernelSettlementResultJSON(
	record ExecutionLogRecord,
	toolID string,
	resultCode string,
	reused bool,
	droppedRoots []string,
) ([]byte, error) {
	result := map[string]any{
		"ok": record.ExitStatus == "ok", "exec_id": record.ID, "tool_use_id": toolID,
		"kernel_id": record.KernelID, "kernel_kind": record.KernelKind, "reused": reused,
		"stdout": record.Stdout, "stderr": record.Stderr, "exit_status": record.ExitStatus,
		"cell_index": record.CellIndex, "files_written": record.FilesWritten, "dropped_roots": droppedRoots,
	}
	if resultCode != "" {
		result["code"] = resultCode
	}
	return json.Marshal(result)
}

func validKernelSettlementResultCode(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 128 {
		return false
	}
	for _, current := range value {
		if (current < 'a' || current > 'z') && (current < '0' || current > '9') && current != '_' {
			return false
		}
	}
	return true
}

func kernelSettlementExecutionLogSHA256(record ExecutionLogRecord) (string, error) {
	filesWritten, err := canonicalKernelSettlementJSONValue(record.FilesWritten)
	if err != nil {
		return "", err
	}
	filesRead, err := canonicalKernelSettlementJSONValue(record.FilesRead)
	if err != nil {
		return "", err
	}
	detection, err := canonicalKernelSettlementJSONValue(record.Detection)
	if err != nil {
		return "", err
	}
	value := struct {
		ID           string          `json:"id"`
		FrameID      string          `json:"frame_id"`
		CellIndex    int             `json:"cell_index"`
		KernelID     string          `json:"kernel_id"`
		KernelKind   string          `json:"kernel_kind"`
		CondaEnv     string          `json:"conda_env"`
		Language     string          `json:"language"`
		Source       string          `json:"source"`
		Stdout       string          `json:"stdout"`
		Stderr       string          `json:"stderr"`
		ExitStatus   string          `json:"exit_status"`
		Origin       string          `json:"origin"`
		ExecutedAt   time.Time       `json:"executed_at"`
		FilesWritten json.RawMessage `json:"files_written"`
		FilesRead    json.RawMessage `json:"files_read"`
		ErrorLine    *int            `json:"error_lineno"`
		Detection    json.RawMessage `json:"detection"`
	}{
		ID: record.ID, FrameID: record.FrameID, CellIndex: record.CellIndex,
		KernelID: record.KernelID, KernelKind: record.KernelKind, CondaEnv: record.CondaEnv,
		Language: record.Language, Source: record.Source, Stdout: record.Stdout, Stderr: record.Stderr,
		ExitStatus: record.ExitStatus, Origin: record.Origin, ExecutedAt: record.ExecutedAt.UTC(),
		FilesWritten: filesWritten, FilesRead: filesRead, ErrorLine: record.ErrorLine, Detection: detection,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalKernelSettlementJSONValue(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var canonical any
	if err := decoder.Decode(&canonical); err != nil {
		return nil, err
	}
	canonicalRaw, err := json.Marshal(canonical)
	return json.RawMessage(canonicalRaw), err
}

func normalizeKernelSettlementDroppedRoots(values []string) ([]string, error) {
	if len(values) > kernelResultSettlementMaxDroppedRoots {
		return nil, errors.New("kernel settlement dropped roots exceed the limit")
	}
	if values == nil {
		return nil, nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > kernelResultSettlementMaxTextBytes {
			return nil, errors.New("kernel settlement dropped root is invalid")
		}
		result[index] = value
	}
	return result, nil
}

func validKernelSettlementTerminalState(value string) bool {
	return value == KernelLocalOperationStateCompleted || value == KernelLocalOperationStateFailed ||
		value == KernelLocalOperationStateCancelled
}

func truncateKernelSettlementRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func truncateKernelSettlementBytes(value string, limit int64) string {
	if int64(len(value)) <= limit {
		return value
	}
	cut := int(limit)
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut]
}

func stableKernelBackgroundStartEventID(frameID, executionID string) string {
	return stableWorkspaceUUID("kernel-background-start:" + frameID + ":" + executionID)
}

func stableKernelBackgroundLostEventID(frameID, executionID string) string {
	return stableWorkspaceUUID("kernel-background-lost:" + frameID + ":" + executionID)
}

func stableWorkspaceUUID(value string) string {
	// Keep the exact UUID v5 derivation used by the background execution
	// authority without persisting another copy of the start/result payload.
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(value)).String()
}

func rejectDuplicateKernelSettlementJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeKernelSettlementJSONValue(decoder); err != nil {
		return err
	}
	return requireKernelSettlementJSONEOF(decoder)
}

func consumeKernelSettlementJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("decode kernel result settlement")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, valid := keyToken.(string)
			if err != nil || !valid {
				return errors.New("decode kernel result settlement")
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.New("kernel result settlement contains duplicate fields")
			}
			seen[key] = struct{}{}
			if err := consumeKernelSettlementJSONValue(decoder); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return errors.New("decode kernel result settlement")
		}
	case '[':
		for decoder.More() {
			if err := consumeKernelSettlementJSONValue(decoder); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return errors.New("decode kernel result settlement")
		}
	default:
		return errors.New("decode kernel result settlement")
	}
	return nil
}

func requireKernelSettlementJSONEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("kernel result settlement contains trailing data")
	}
	return nil
}

type kernelSettlementOutboxClaim struct {
	EventID    string
	ClaimOwner string
	ClaimToken string
}

type kernelSettlementDelivery struct {
	Claim    kernelSettlementOutboxClaim
	Envelope KernelBackgroundSettlement
}
