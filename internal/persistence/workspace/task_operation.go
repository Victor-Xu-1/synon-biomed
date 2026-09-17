package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const TaskOperationOutboxTopic = "task.operation"
const taskOperationType = "task.operation.v1"

func (s *Store) FindTaskOperation(ctx context.Context, id string) (OutboxEvent, bool, error) {
	event, err := scanOutboxEvent(s.db.QueryRowContext(ctx, outboxSelect+` WHERE event_id=? AND topic=?`,
		DeriveOutboxEventID(TaskOperationOutboxTopic, id), TaskOperationOutboxTopic))
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxEvent{}, false, nil
	}
	return event, err == nil, err
}

func (s *Store) CountPendingTaskOperations(ctx context.Context, access KernelFrameAccess) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	var count, dead int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN status='dead_letter' THEN 1 ELSE 0 END),0) FROM workspace_outbox WHERE topic=? AND aggregate_id=?
		AND status IN ('pending','inflight','dead_letter') AND json_extract(payload_json,'$.owner_id')=?
		AND json_extract(payload_json,'$.frame_incarnation_id')=? AND json_extract(payload_json,'$.root_frame_incarnation_id')=?`,
		TaskOperationOutboxTopic, access.Frame.ID, access.UserID, access.Frame.IncarnationID, access.RootFrameIncarnationID).Scan(&count, &dead)
	if err == nil && dead > 0 {
		return 0, errors.New("durable task operation delivery requires recovery; no live execution is implied")
	}
	return count, err
}

// TaskOperation is a durable dispatch envelope, not another task state store.
// The existing outbox owns scheduling; the notification owns the terminal result.
type TaskOperation struct {
	Version                int                                    `json:"version"`
	ID                     string                                 `json:"id"`
	OwnerID                string                                 `json:"owner_id"`
	FrameID                string                                 `json:"frame_id"`
	FrameIncarnationID     string                                 `json:"frame_incarnation_id"`
	RootFrameID            string                                 `json:"root_frame_id"`
	RootFrameIncarnationID string                                 `json:"root_frame_incarnation_id"`
	Tool                   string                                 `json:"tool"`
	NotificationID         string                                 `json:"notification_id"`
	FrameEventSequence     int64                                  `json:"frame_event_sequence"`
	Request                json.RawMessage                        `json:"request"`
	Observation            *transcriptstore.ToolOperationObserver `json:"observation,omitempty"`
	// Admission is caller authority, not persisted credentials. The observer
	// and outbox request are created together by EnqueueTaskOperation.
	ObservationAdmission *transcriptstore.ToolOperationAdmission `json:"-"`
}

func (s *Store) EnqueueTaskOperation(ctx context.Context, operation TaskOperation) (OutboxEvent, error) {
	if operation.Observation != nil {
		return OutboxEvent{}, errors.New("task observation must be created by the admission transaction")
	}
	if operation.Version != 1 || !validDetachedIdentity(operation.ID) || !validDetachedIdentity(operation.NotificationID) ||
		strings.TrimSpace(operation.Tool) == "" || len(operation.Request) > 64*1024 || !json.Valid(operation.Request) {
		return OutboxEvent{}, errors.New("invalid task operation envelope")
	}
	var event OutboxEvent
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return event, err
	}
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateTaskOperationAuthority(ctx, tx, operation, true); err != nil {
			return err
		}
		var original string
		err := tx.QueryRowContext(ctx, `SELECT payload_json FROM workspace_outbox WHERE event_id=?`, DeriveOutboxEventID(TaskOperationOutboxTopic, operation.ID)).Scan(&original)
		if err == nil {
			var prior TaskOperation
			if err := json.Unmarshal([]byte(original), &prior); err != nil {
				return err
			}
			operation.FrameEventSequence = prior.FrameEventSequence
			operation.Observation = prior.Observation
			if admission := operation.ObservationAdmission; admission != nil && prior.Observation != nil {
				if admission.CallID != prior.Observation.CallID || admission.ToolName != operation.Tool ||
					admission.Claim.StreamUID != prior.Observation.StreamUID || admission.Claim.OwnerID != operation.OwnerID {
					return errors.New("task observation admission conflicts with original owner")
				}
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM frame_events WHERE frame_id=?`, operation.FrameID).Scan(&operation.FrameEventSequence); err != nil {
				return err
			}
			if admission := operation.ObservationAdmission; admission != nil {
				if admission.ToolName != operation.Tool || admission.Claim.OwnerID != operation.OwnerID {
					return errors.New("task observation belongs to another owner or tool")
				}
				var frameID string
				if err := tx.QueryRowContext(ctx, `SELECT frame_id FROM transcript_streams WHERE stream_uid=? AND owner_id=?`, admission.Claim.StreamUID, operation.OwnerID).Scan(&frameID); err != nil {
					return err
				}
				if frameID != operation.FrameID {
					return errors.New("task observation belongs to another frame")
				}
				bound := *admission
				bound.DurableOperationID = operation.ID
				observer, _, err := tx.BeginToolOperationObservation(ctx, bound)
				if err != nil {
					return err
				}
				operation.Observation = &observer
			}
		} else {
			return err
		}
		raw, err := json.Marshal(operation)
		if err != nil {
			return err
		}
		event, err = s.enqueueOutboxTransaction(ctx, tx, EnqueueOutboxInput{
			IdempotencyKey: operation.ID, Topic: TaskOperationOutboxTopic, Type: taskOperationType,
			PartitionKey: operation.FrameID, AggregateType: "frame", AggregateID: operation.FrameID,
			Payload: raw, MaxAttempts: 1000,
		})
		return err
	})
	if err == nil {
		s.signalOutboxWake()
	}
	return event, err
}

// CheckTaskOperation validates ownership before side effects, not merely at
// settlement. A cancel event remains authoritative even if the frame resumes.
func (s *Store) CheckTaskOperation(ctx context.Context, event OutboxEvent) (bool, error) {
	return checkTaskOperation(ctx, s.db, event)
}

func checkTaskOperation(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, event OutboxEvent) (bool, error) {
	operation, err := DecodeTaskOperation(event)
	if err != nil {
		return false, err
	}
	var cancelled bool
	err = query.QueryRowContext(ctx, `SELECT frame.status IN ('cancelled','completed','failed') OR EXISTS(
		SELECT 1 FROM frame_events cancellation WHERE cancellation.frame_id=frame.id
		AND cancellation.event_type='frame_cancelled' AND cancellation.sequence>?)
		FROM workspace_outbox dispatch JOIN frames frame ON frame.id=dispatch.aggregate_id
		JOIN frames root ON root.id=frame.root_frame_id JOIN projects project ON project.id=frame.project_id
		WHERE dispatch.event_id=? AND dispatch.topic=? AND dispatch.status='inflight' AND dispatch.claim_token=?
		AND dispatch.payload_json=? AND dispatch.lease_expires_at_ms>`+sqliteNowMillis+`
		AND project.user_id=? AND frame.incarnation_id=? AND root.incarnation_id=? AND root.id=?`,
		operation.FrameEventSequence, event.ID, TaskOperationOutboxTopic, event.ClaimToken, string(event.Payload), operation.OwnerID,
		operation.FrameIncarnationID, operation.RootFrameIncarnationID, operation.RootFrameID).Scan(&cancelled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrOutboxClaimLost
	}
	return cancelled, err
}

func DecodeTaskOperation(event OutboxEvent) (TaskOperation, error) {
	var operation TaskOperation
	if event.Topic != TaskOperationOutboxTopic || event.Type != taskOperationType ||
		json.Unmarshal(event.Payload, &operation) != nil || operation.Version != 1 ||
		operation.ID != event.IdempotencyKey || operation.FrameID != event.AggregateID ||
		operation.NotificationID == "" || !json.Valid(operation.Request) {
		return operation, errors.New("task operation dispatch identity is invalid")
	}
	if observation := operation.Observation; observation != nil &&
		(observation.DurableOperationID != operation.ID || observation.OwnerID != operation.OwnerID ||
			observation.ToolName != operation.Tool || observation.OperationID == "" || observation.StreamUID == "") {
		return operation, errors.New("task operation observation identity is invalid")
	}
	return operation, nil
}

func validateTaskOperationAuthority(ctx context.Context, tx workspaceTransaction, operation TaskOperation, requireActive bool) error {
	var owner, frameIncarnation, rootIncarnation, rootID, status string
	err := tx.QueryRowContext(ctx, `SELECT project.user_id,frame.incarnation_id,root.incarnation_id,frame.root_frame_id,frame.status
		FROM frames frame JOIN projects project ON project.id=frame.project_id
		JOIN frames root ON root.id=frame.root_frame_id WHERE frame.id=?`, operation.FrameID).
		Scan(&owner, &frameIncarnation, &rootIncarnation, &rootID, &status)
	if err != nil {
		return err
	}
	if owner != operation.OwnerID || frameIncarnation != operation.FrameIncarnationID || rootIncarnation != operation.RootFrameIncarnationID || rootID != operation.RootFrameID {
		return errors.New("task operation frame authority changed")
	}
	if requireActive && (status == "cancelled" || status == "completed" || status == "failed") {
		return errors.New("task operation frame is terminal")
	}
	return nil
}

// SettleTaskOperation commits the result and consumes its still-live claim in
// one transaction. A stale worker cannot publish a late result for a new owner.
func (s *Store) SettleTaskOperation(ctx context.Context, event OutboxEvent, payload map[string]any) error {
	operation, err := DecodeTaskOperation(event)
	if err != nil {
		return err
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return err
	}
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateTaskOperationAuthority(ctx, tx, operation, false); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE workspace_outbox SET status='delivered',delivered_at_ms=`+sqliteNowMillis+`,
			claim_owner=NULL,claim_token=NULL,claimed_at_ms=NULL,lease_expires_at_ms=NULL,last_error=NULL
			WHERE event_id=? AND topic=? AND status='inflight' AND claim_token=? AND payload_json=? AND lease_expires_at_ms>`+sqliteNowMillis,
			event.ID, TaskOperationOutboxTopic, event.ClaimToken, string(event.Payload))
		if err != nil {
			return err
		}
		if err := requireOutboxClaim(result); err != nil {
			return err
		}
		if operation.Observation != nil {
			status, _ := payload["status"].(string)
			if status != "completed" && status != "cancelled" {
				status = "failed"
			}
			if _, err := tx.AppendNextToolOperationObservation(ctx, *operation.Observation, status, map[string]any{
				"toolResult": payload, "progress": map[string]any{"phase": "operation_" + status, "indeterminate": false},
			}); err != nil {
				return err
			}
		}
		_, _, err = createNotificationTx(ctx, tx, CreateNotificationInput{
			ID: operation.NotificationID, OwnerUserID: operation.OwnerID, SenderFrameID: operation.FrameID,
			RecipientFrameID: operation.FrameID, RootFrameID: operation.RootFrameID, NotificationType: "cell_result", Payload: payload,
		}, s.now().UTC())
		if err != nil {
			return err
		}
		var resumeID string
		err = tx.QueryRowContext(ctx, `SELECT e.id FROM frame_events e JOIN frames frame ON frame.id=e.frame_id
			WHERE e.frame_id=? AND e.event_type='frame_resumed' AND json_extract(e.payload,'$.dispatch.status')='registered'
			AND frame.status NOT IN ('cancelled','completed','failed') ORDER BY e.sequence DESC LIMIT 1`, operation.FrameID).Scan(&resumeID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, _, err = wakeCompatibilityFrameResumeDispatchTx(ctx, tx, resumeID, s.now().UTC())
		return err
	})
	if err == nil {
		s.signalOutboxWake()
	}
	return err
}
