package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *Store) CreateProjectRealtime(ctx context.Context, input CreateProjectInput, eventID string) (Project, error) {
	eventID = mutationRealtimeID(eventID)
	var project Project
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		project, err = s.CreateProjectOutboxTx(ctx, tx, input)
		if err != nil {
			return err
		}
		_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
			ID: eventID, UserID: normalizedProjectUserID(input.UserID), ProjectID: project.ID,
			Type: "frame_update", Payload: map[string]any{"project_id": project.ID, "action": "project_created"},
		}, "")
		return err
	})
	return project, err
}

func (s *Store) UpdateProjectRealtime(ctx context.Context, id, ownerUserID string, input UpdateProjectInput, eventID string) (Project, error) {
	eventID = mutationRealtimeID(eventID)
	var project Project
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		project, err = s.UpdateProjectOutboxTx(ctx, tx, id, ownerUserID, input)
		if err != nil {
			return err
		}
		_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
			ID: eventID, UserID: ownerUserID, ProjectID: project.ID,
			Type: "frame_update", Payload: map[string]any{"project_id": project.ID, "action": "project_updated"},
		}, "")
		return err
	})
	return project, err
}

func (s *Store) DeleteProjectRealtime(ctx context.Context, id, ownerUserID, eventID string) ([]string, error) {
	eventID = mutationRealtimeID(eventID)
	var blobPaths []string
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT frame.id,frame.root_frame_id,frame.incarnation_id
			FROM frames AS frame JOIN projects AS project ON project.id=frame.project_id
			WHERE frame.project_id=? AND project.user_id=? ORDER BY frame.root_sequence,frame.id`, id, ownerUserID)
		if err != nil {
			return fmt.Errorf("list project frames before delete: %w", err)
		}
		type frameRetirement struct {
			id, rootFrameID, incarnationID string
		}
		retirements := make([]frameRetirement, 0)
		for rows.Next() {
			var retirement frameRetirement
			if err := rows.Scan(&retirement.id, &retirement.rootFrameID, &retirement.incarnationID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan project frame before delete: %w", err)
			}
			retirements = append(retirements, retirement)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate project frames before delete: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close project frame scan before delete: %w", err)
		}
		blobPaths, err = s.DeleteProjectOutboxTx(ctx, tx, id, ownerUserID)
		if err != nil {
			return err
		}
		for _, retirement := range retirements {
			input := RealtimeEventInput{
				ID: uuid.NewString(), UserID: ownerUserID, ProjectID: id,
				RootFrameID: retirement.rootFrameID, FrameID: retirement.id, Type: "frame_update",
				Payload: map[string]any{
					"project_id": id, "root_frame_id": retirement.rootFrameID,
					"frame_id": retirement.id, "action": "deleted",
				},
			}
			if retirement.incarnationID != "" {
				_, err = s.enqueueRealtimeFrameRetirementTx(ctx, tx, input, retirement.incarnationID)
			} else {
				_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, input, "")
			}
			if err != nil {
				return fmt.Errorf("enqueue project frame delete: %w", err)
			}
		}
		_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
			ID: eventID, UserID: ownerUserID, ProjectID: id, Type: "project_deleted",
			Payload: map[string]any{"project_id": id},
		}, "")
		return err
	})
	return blobPaths, err
}

func (s *Store) RemoveProjectArtifactBlobs(paths []string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	seen := make(map[string]struct{}, len(paths))
	cleanupErrors := make([]error, 0)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		absolute, err := s.blobAbsolute(path)
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("clean deleted project artifact blob %q: %w", path, err))
			continue
		}
		if err := os.Remove(absolute); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("clean deleted project artifact blob %q: %w", path, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (s *Store) CreateFrameRealtime(ctx context.Context, input CreateFrameInput, ownerUserID, eventID, frameEventID string) (Frame, error) {
	eventID, frameEventID = mutationRealtimeID(eventID), mutationFrameEventID(frameEventID)
	var frame Frame
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		frame, err = s.CreateFrameOutboxTx(ctx, tx, input, ownerUserID)
		if err != nil {
			return err
		}
		journalEvent, err := s.AppendFrameOutboxEventTx(ctx, tx, FrameEventInput{
			ID: frameEventID, FrameID: frame.ID, Type: "frame_created",
			Payload: map[string]any{"projectId": frame.ProjectID, "parentFrameId": frame.ParentFrameID,
				"agentName": frame.AgentName, "status": frame.Status, "conversationType": frame.ConversationType},
		})
		if err != nil {
			return err
		}
		_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, frameRealtimeInput(eventID, ownerUserID, frame, journalEvent), journalEvent.ID)
		return err
	})
	return frame, err
}

func (s *Store) UpdateFrameRealtime(ctx context.Context, id, ownerUserID string, input UpdateFrameInput, eventID, frameEventID string) (Frame, error) {
	eventID, frameEventID = mutationRealtimeID(eventID), mutationFrameEventID(frameEventID)
	var frame Frame
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		frame, err = s.UpdateFrameOutboxTx(ctx, tx, id, ownerUserID, input)
		if err != nil {
			return err
		}
		journalEvent, err := s.AppendFrameOutboxEventTx(ctx, tx, FrameEventInput{
			ID: frameEventID, FrameID: frame.ID, Type: "frame_updated",
			Payload: map[string]any{"status": frame.Status, "name": frame.Name},
		})
		if err != nil {
			return err
		}
		_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, frameRealtimeInput(eventID, ownerUserID, frame, journalEvent), journalEvent.ID)
		return err
	})
	return frame, err
}

func (s *Store) DeleteFrameRealtime(ctx context.Context, frame Frame, ownerUserID, eventID string) error {
	eventID = mutationRealtimeID(eventID)
	return s.WithTransaction(ctx, func(tx *sql.Tx) error {
		if err := s.DeleteFrameOutboxTx(ctx, tx, frame, ownerUserID); err != nil {
			return err
		}
		_, err := s.enqueueRealtimeFrameRetirementTx(ctx, tx, RealtimeEventInput{
			ID: eventID, UserID: ownerUserID, ProjectID: frame.ProjectID,
			RootFrameID: frame.RootFrameID, FrameID: frame.ID, Type: "frame_update",
			Payload: map[string]any{"project_id": frame.ProjectID, "root_frame_id": frame.RootFrameID,
				"frame_id": frame.ID, "action": "deleted"},
		}, frame.IncarnationID)
		return err
	})
}

func frameRealtimeInput(eventID, userID string, frame Frame, source FrameEvent) RealtimeEventInput {
	input := FrameRealtimeEventInput(eventID, userID, frame, source)
	input.Type = "frame_update"
	return input
}

func (s *Store) CreateRoutineRealtime(ctx context.Context, input CreateRoutineInput, eventID string) (Routine, error) {
	var routine Routine
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		routine, err = s.CreateRoutineOutboxTx(ctx, tx, input)
		if err != nil {
			return err
		}
		if strings.TrimSpace(eventID) == "" {
			eventID = routineRealtimeEventID(routine, "created")
		}
		return s.enqueueRoutineRealtimeTx(ctx, tx, routine, "created", eventID)
	})
	return routine, err
}

func (s *Store) ClaimNextDueRoutineRealtime(ctx context.Context, now time.Time, lockTTL time.Duration, ownerUserID, claimToken string) (Routine, bool, error) {
	var routine Routine
	var claimed bool
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		routine, claimed, err = s.claimNextDueRoutineFencedTx(ctx, tx, now, lockTTL, ownerUserID, claimToken)
		if err != nil || !claimed {
			return err
		}
		return s.enqueueRoutineRealtimeTx(ctx, tx, routine, "claimed", routineRealtimeEventID(routine, "claimed"))
	})
	return routine, claimed, err
}

func (s *Store) CompleteRoutineTickRealtime(ctx context.Context, claim Routine, at time.Time, successful bool, result string) (Routine, error) {
	var routine Routine
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var err error
		routine, _, err = s.completeRoutineTickFencedTx(ctx, tx, claim, at, successful, result)
		if err != nil {
			return err
		}
		return s.enqueueRoutineRealtimeTx(ctx, tx, routine, "completed", routineRealtimeEventID(claim, "completed"))
	})
	return routine, err
}

func (s *Store) enqueueRoutineRealtimeTx(ctx context.Context, tx *sql.Tx, routine Routine, action, eventID string) error {
	frameContext, found, err := s.GetFrameRealtimeContextOutboxTx(ctx, tx, routine.RootFrameID)
	if err != nil {
		return err
	}
	if !found || frameContext.UserID != routine.OwnerUserID {
		return fmt.Errorf("routine root frame %q is unavailable to owner %q", routine.RootFrameID, routine.OwnerUserID)
	}
	_, err = s.EnqueueRealtimeOutboxTx(ctx, tx, RealtimeEventInput{
		ID: eventID, UserID: frameContext.UserID, ProjectID: frameContext.Frame.ProjectID,
		RootFrameID: frameContext.Frame.RootFrameID, FrameID: frameContext.Frame.ID,
		Type: "routine_update", Payload: map[string]any{
			"project_id": frameContext.Frame.ProjectID, "root_frame_id": frameContext.Frame.RootFrameID,
			"frame_id": frameContext.Frame.ID, "routine_id": routine.ID, "action": action,
			"claim_generation": routine.ClaimGeneration,
		},
	}, "")
	return err
}

func routineRealtimeEventID(routine Routine, action string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("synon-routine-realtime-v1\x00%s\x00%d\x00%s", routine.ID, routine.ClaimGeneration, action)))
	return fmt.Sprintf("routine:%x", digest)
}

func mutationRealtimeID(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "mutation:" + uuid.NewString()
}

func mutationFrameEventID(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "mutation-frame:" + uuid.NewString()
}

func normalizedProjectUserID(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "local"
}
