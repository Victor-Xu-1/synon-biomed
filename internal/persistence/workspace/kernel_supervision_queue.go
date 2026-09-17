package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type KernelChildQueuedMessage struct {
	ID            string    `json:"id"`
	FrameID       string    `json:"frame_id"`
	Generation    int64     `json:"generation"`
	SenderFrameID string    `json:"sender_frame_id"`
	Message       string    `json:"message"`
	Kind          string    `json:"kind"`
	CreatedAt     time.Time `json:"created_at"`
}

type KernelMessageQueueResult struct {
	TargetFrameID string
	Relation      string
	Action        string
	Queued        *KernelChildQueuedMessage
	Child         KernelSupervisedChild
}

// QueueKernelSupervisionMessage atomically persists topology validation,
// notification visibility, generation wakeup, and terminal resume state.
func (s *Store) QueueKernelSupervisionMessage(ctx context.Context, sourceFrameID, targetFrameID, ownerUserID, message, kind string) (KernelMessageQueueResult, error) {
	return s.QueueKernelSupervisionMessageWithID(ctx, sourceFrameID, targetFrameID, ownerUserID, uuid.NewString(), message, kind)
}

func (s *Store) QueueKernelSupervisionMessageWithID(ctx context.Context, sourceFrameID, targetFrameID, ownerUserID, messageID, message, kind string) (KernelMessageQueueResult, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return KernelMessageQueueResult{}, err
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return KernelMessageQueueResult{}, errors.New("message id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KernelMessageQueueResult{}, err
	}
	defer tx.Rollback()
	var sourceParent, sourceRoot, sourceProject, sourceAgent, sourceName, owner string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(f.parent_frame_id,''), f.root_frame_id, f.project_id, f.agent_name, COALESCE(f.name,''), p.user_id FROM frames f JOIN projects p ON p.id=f.project_id WHERE f.id=?`, sourceFrameID).Scan(&sourceParent, &sourceRoot, &sourceProject, &sourceAgent, &sourceName, &owner); err != nil || owner != ownerUserID {
		return KernelMessageQueueResult{}, errors.New("message target is unavailable")
	}
	if targetFrameID == "parent" {
		targetFrameID = sourceParent
	}
	if strings.TrimSpace(targetFrameID) == "" {
		return KernelMessageQueueResult{}, errors.New("message target is unavailable")
	}
	var targetParent, targetProject, targetOwner string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(f.parent_frame_id,''), f.project_id, p.user_id FROM frames f JOIN projects p ON p.id=f.project_id WHERE f.id=?`, targetFrameID).Scan(&targetParent, &targetProject, &targetOwner); err != nil || targetOwner != ownerUserID || targetProject != sourceProject {
		return KernelMessageQueueResult{}, errors.New("message target is unavailable")
	}
	relation := ""
	if sourceParent == targetFrameID {
		relation = "parent"
	} else if targetParent == sourceFrameID {
		relation = "child"
	} else {
		return KernelMessageQueueResult{}, errors.New("message target is outside the direct supervision topology")
	}
	result := KernelMessageQueueResult{TargetFrameID: targetFrameID, Relation: relation, Action: "sent"}
	now := s.now().UTC()
	notificationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-child-message-notification:"+sourceFrameID+":"+targetFrameID+":"+messageID)).String()
	notificationInput := CreateNotificationInput{
		ID: notificationID, SenderFrameID: sourceFrameID, RecipientFrameID: targetFrameID,
		RootFrameID: sourceRoot, OwnerUserID: ownerUserID, NotificationType: "child_message",
		Payload: map[string]any{
			"sender_frame_id": sourceFrameID, "name": sourceName, "agent_name": sourceAgent,
			"text": message, "kind": kind,
		},
	}
	if _, _, err = createNotificationTx(ctx, tx, notificationInput, now); err != nil {
		return KernelMessageQueueResult{}, err
	}
	child, found, err := scanKernelSupervisedChild(tx.QueryRowContext(ctx, `
		SELECT frame_id, parent_frame_id, root_frame_id, project_id, owner_user_id, tool_use_id,
			agent_name, delegate_name, task, context_summary, model, output_schema, status,
			output_data, error, dispatched, started_at, completed_at
		FROM kernel_child_supervision WHERE frame_id=? AND owner_user_id=?`, targetFrameID, ownerUserID))
	if err != nil {
		return KernelMessageQueueResult{}, err
	}
	if found {
		queueID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-child-message-queue:"+sourceFrameID+":"+targetFrameID+":"+messageID)).String()
		var existing KernelChildQueuedMessage
		err := tx.QueryRowContext(ctx, `SELECT id,frame_id,generation,sender_frame_id,message,kind,created_at
			FROM kernel_child_messages WHERE id=?`, queueID).Scan(&existing.ID, &existing.FrameID, &existing.Generation,
			&existing.SenderFrameID, &existing.Message, &existing.Kind, &existing.CreatedAt)
		if err == nil {
			if existing.FrameID != targetFrameID || existing.SenderFrameID != sourceFrameID || existing.Message != message || existing.Kind != kind {
				return KernelMessageQueueResult{}, errors.New("message id already identifies another child message")
			}
			result.Child, result.Queued, result.Action = child, &existing, "injected"
			if err := tx.Commit(); err != nil {
				return KernelMessageQueueResult{}, fmt.Errorf("commit idempotent supervision message queue: %w", err)
			}
			return result, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return KernelMessageQueueResult{}, err
		}
		wasTerminal := isKernelChildTerminalStatus(child.Status)
		if _, err := tx.ExecContext(ctx, `INSERT INTO kernel_child_message_clock(frame_id,enqueued_generation,consumed_generation) VALUES(?,1,0)
			ON CONFLICT(frame_id) DO UPDATE SET enqueued_generation=enqueued_generation+1`, targetFrameID); err != nil {
			return KernelMessageQueueResult{}, err
		}
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT enqueued_generation FROM kernel_child_message_clock WHERE frame_id=?`, targetFrameID).Scan(&generation); err != nil {
			return KernelMessageQueueResult{}, err
		}
		queued := &KernelChildQueuedMessage{ID: queueID, FrameID: targetFrameID, Generation: generation, SenderFrameID: sourceFrameID, Message: message, Kind: kind, CreatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO kernel_child_messages(id,frame_id,generation,sender_frame_id,message,kind,created_at) VALUES(?,?,?,?,?,?,?)`, queued.ID, queued.FrameID, queued.Generation, queued.SenderFrameID, queued.Message, queued.Kind, queued.CreatedAt); err != nil {
			return KernelMessageQueueResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kernel_child_supervision SET status='processing', completed_at=NULL, error='' WHERE frame_id=?`, targetFrameID); err != nil {
			return KernelMessageQueueResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE frames SET status='processing', updated_at=? WHERE id=?`, now, targetFrameID); err != nil {
			return KernelMessageQueueResult{}, err
		}
		child.Status = "processing"
		child.CompletedAt = nil
		child.Error = ""
		result.Child, result.Queued = child, queued
		result.Action = "injected"
		if wasTerminal {
			result.Action = "resumed"
		}
	}
	if err := tx.Commit(); err != nil {
		return KernelMessageQueueResult{}, fmt.Errorf("commit supervision message queue: %w", err)
	}
	return result, nil
}

func (s *Store) ListPendingKernelChildMessages(ctx context.Context, frameID, ownerUserID string) ([]KernelChildQueuedMessage, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id,m.frame_id,m.generation,m.sender_frame_id,m.message,m.kind,m.created_at
		FROM kernel_child_messages m JOIN kernel_child_supervision c ON c.frame_id=m.frame_id
		WHERE m.frame_id=? AND c.owner_user_id=? AND m.consumed_at IS NULL
		ORDER BY m.generation`, frameID, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []KernelChildQueuedMessage{}
	for rows.Next() {
		var item KernelChildQueuedMessage
		if err := rows.Scan(&item.ID, &item.FrameID, &item.Generation, &item.SenderFrameID, &item.Message, &item.Kind, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) MarkKernelChildMessageConsumed(ctx context.Context, frameID, ownerUserID, messageID string, generation int64) error {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE kernel_child_messages SET consumed_at=? WHERE id=? AND frame_id=? AND consumed_at IS NULL AND EXISTS(SELECT 1 FROM kernel_child_supervision c WHERE c.frame_id=? AND c.owner_user_id=?)`, s.now().UTC(), messageID, frameID, frameID, ownerUserID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE kernel_child_message_clock SET consumed_generation=MAX(consumed_generation,?) WHERE frame_id=?`, generation, frameID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) KernelChildHasPendingMessages(ctx context.Context, frameID, ownerUserID string) (bool, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return false, err
	}
	var pending int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_child_messages m JOIN kernel_child_supervision c ON c.frame_id=m.frame_id WHERE m.frame_id=? AND c.owner_user_id=? AND m.consumed_at IS NULL`, frameID, ownerUserID).Scan(&pending)
	return pending > 0, err
}

func (s *Store) ReopenKernelSupervisedChild(ctx context.Context, frameID, ownerUserID string) (KernelSupervisedChild, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return KernelSupervisedChild{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE kernel_child_supervision SET status='processing',completed_at=NULL,error='' WHERE frame_id=? AND owner_user_id=?`, frameID, ownerUserID)
	if err != nil {
		return KernelSupervisedChild{}, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return KernelSupervisedChild{}, errors.New("delegated child is unavailable")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE frames SET status='processing',updated_at=? WHERE id=?`, s.now().UTC(), frameID); err != nil {
		return KernelSupervisedChild{}, err
	}
	child, found, err := s.GetKernelSupervisedChild(ctx, "", frameID, ownerUserID)
	if err == nil && !found {
		// GetKernelSupervisedChild requires the direct parent; load it first.
		var parent string
		if scanErr := s.db.QueryRowContext(ctx, `SELECT parent_frame_id FROM kernel_child_supervision WHERE frame_id=? AND owner_user_id=?`, frameID, ownerUserID).Scan(&parent); scanErr != nil {
			return KernelSupervisedChild{}, scanErr
		}
		child, found, err = s.GetKernelSupervisedChild(ctx, parent, frameID, ownerUserID)
	}
	if err != nil || !found {
		return KernelSupervisedChild{}, errors.New("delegated child is unavailable")
	}
	return child, nil
}
