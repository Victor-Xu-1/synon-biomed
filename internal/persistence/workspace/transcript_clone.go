package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

type CloneConversationInput struct {
	OwnerUserID                 string
	SourceFrameID               string
	ExpectedSourceIncarnationID string
	TargetFrameID               string
	TargetName                  string
}

type CloneConversationResult struct {
	Frame      CompatibilityFrame
	Transcript transcriptstore.CloneFrameHistoryResult
	FrameEvent FrameEvent
}

// CloneConversationWithTranscript creates the target Frame, filtered runtime
// metadata, empty Genesis stream, canonical history, lifecycle event, and both
// realtime intents in one BEGIN IMMEDIATE transaction.
func (s *Store) CloneConversationWithTranscript(
	ctx context.Context, input CloneConversationInput,
) (CloneConversationResult, error) {
	if s == nil || s.db == nil {
		return CloneConversationResult{}, errors.New("workspace store is closed")
	}
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.SourceFrameID = strings.TrimSpace(input.SourceFrameID)
	input.ExpectedSourceIncarnationID = strings.TrimSpace(input.ExpectedSourceIncarnationID)
	input.TargetFrameID = strings.TrimSpace(input.TargetFrameID)
	input.TargetName = strings.TrimSpace(input.TargetName)
	if input.OwnerUserID == "" || input.SourceFrameID == "" || input.ExpectedSourceIncarnationID == "" ||
		input.TargetFrameID == "" || input.TargetFrameID == input.SourceFrameID || input.TargetName == "" {
		return CloneConversationResult{}, errors.New("complete source and target conversation identity is required")
	}
	var result CloneConversationResult
	repository := transcriptstore.NewRepository(s.db)
	err := repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		source, found, err := queryTraceFrame(ctx, tx, input.SourceFrameID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("source conversation changed before clone: %w", transcriptstore.ErrEventConflict)
		}
		var sourceOwner string
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM projects WHERE id=?`, source.ProjectID).Scan(&sourceOwner); err != nil {
			return fmt.Errorf("resolve source frame owner: %w", err)
		}
		if sourceOwner != input.OwnerUserID {
			return transcriptstore.ErrOwnerMismatch
		}
		if source.IncarnationID != input.ExpectedSourceIncarnationID {
			return fmt.Errorf("source conversation changed before clone: %w", transcriptstore.ErrEventConflict)
		}
		sourceStream, legacyCutoverID, err := tx.ResolveFrameCloneSource(ctx, input.OwnerUserID, source.ID)
		if err != nil {
			return fmt.Errorf("resolve source transcript authority: %w", err)
		}

		targetMetadata, err := cloneConversationRuntimeMetadata(source)
		if err != nil {
			return fmt.Errorf("clone source runtime metadata: %w", err)
		}
		target, targetFound, err := queryTraceFrame(ctx, tx, input.TargetFrameID)
		if err != nil {
			return err
		}
		if targetFound {
			if err := validateExistingConversationCloneTarget(ctx, tx, input, source, target, targetMetadata); err != nil {
				return err
			}
		} else {
			target, err = s.createFrameOutboxTransaction(ctx, tx, CreateFrameInput{
				ID: input.TargetFrameID, ProjectID: source.ProjectID, AgentName: source.AgentName,
				Status: "completed", ConversationType: source.ConversationType, Name: input.TargetName,
			}, input.OwnerUserID)
			if err != nil {
				return fmt.Errorf("create clone frame: %w", err)
			}
			if _, err := s.setFrameRuntimeMetadataTransaction(ctx, tx, target.ID, targetMetadata); err != nil {
				return fmt.Errorf("write clone frame metadata: %w", err)
			}
			if _, err := tx.CreateStream(ctx, transcriptstore.CreateStreamInput{
				UID: "frame:" + target.ID, OwnerID: input.OwnerUserID, ExternalID: target.ID, SessionID: target.ID,
				Kind: transcriptstore.StreamKindFrameRef, ProjectID: target.ProjectID,
				RootFrameID: target.RootFrameID, FrameID: target.ID, Epoch: 1,
			}); err != nil {
				return fmt.Errorf("create clone transcript stream: %w", err)
			}
		}

		clone, err := tx.CloneFrameHistory(ctx, transcriptstore.CloneFrameHistoryInput{
			SourceStreamUID: sourceStream.UID, TargetStreamUID: "frame:" + target.ID,
			OwnerID: input.OwnerUserID, LegacyCutoverID: legacyCutoverID,
		})
		if err != nil {
			return fmt.Errorf("clone canonical transcript history: %w", err)
		}
		frameEventID := cloneConversationStableID("frame-created", target.ID)
		frameEvent, err := s.appendFrameOutboxEventTransaction(ctx, tx, FrameEventInput{
			ID: frameEventID, FrameID: target.ID, Type: "frame_created",
			Payload: map[string]any{
				"projectId": target.ProjectID, "parentFrameId": target.ParentFrameID,
				"agentName": target.AgentName, "status": target.Status,
				"conversationType": target.ConversationType,
			},
		})
		if err != nil {
			return fmt.Errorf("append clone frame lifecycle: %w", err)
		}
		if _, err := s.enqueueRealtimeOutboxTransaction(ctx, tx,
			frameRealtimeInput(cloneConversationStableID("frame-realtime", target.ID), input.OwnerUserID, target, frameEvent),
			frameEvent.ID, ""); err != nil {
			return fmt.Errorf("enqueue clone frame realtime: %w", err)
		}
		if _, err := s.enqueueRealtimeOutboxTransaction(ctx, tx, RealtimeEventInput{
			ID: cloneConversationStableID("list-changed", target.ID), UserID: input.OwnerUserID,
			ProjectID: target.ProjectID, RootFrameID: target.RootFrameID, FrameID: target.ID,
			Type: "conversation.listChanged", Payload: map[string]any{
				"conversation_id": target.ID, "action": "created", "source": "synonbiomed",
			},
		}, "", ""); err != nil {
			return fmt.Errorf("enqueue clone conversation list change: %w", err)
		}
		stored, found, err := queryTraceFrame(ctx, tx, target.ID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return errors.New("cloned frame disappeared before commit")
		}
		result = CloneConversationResult{
			Frame: CompatibilityFrame{
				Frame: stored, TaskSummary: stored.TaskSummary, InputData: stored.InputData,
				IsHidden: stored.IsHidden, ChildIDs: []string{},
			},
			Transcript: clone, FrameEvent: frameEvent,
		}
		return nil
	})
	return result, err
}

func cloneConversationRuntimeMetadata(source Frame) (FrameRuntimeMetadata, error) {
	contextData := make(map[string]any, 2)
	for _, key := range []string{"web_extra", "web_assistant"} {
		if value, found := source.ContextData[key]; found {
			cloned, err := cloneConversationJSONValue(value)
			if err != nil {
				return FrameRuntimeMetadata{}, err
			}
			contextData[key] = cloned
		}
	}
	return FrameRuntimeMetadata{
		DelegateName: source.DelegateName, ContextData: contextData,
		TaskSummary: source.TaskSummary,
	}, nil
}

func cloneConversationJSONValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cloned any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func validateExistingConversationCloneTarget(
	ctx context.Context,
	tx workspaceTransaction,
	input CloneConversationInput,
	source, target Frame,
	wantMetadata FrameRuntimeMetadata,
) error {
	var ownerID string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM projects WHERE id=?`, target.ProjectID).Scan(&ownerID); err != nil {
		return err
	}
	if ownerID != input.OwnerUserID || target.ProjectID != source.ProjectID || target.ParentFrameID != "" ||
		target.RootFrameID != target.ID || target.AgentName != source.AgentName || target.Status != "completed" ||
		target.ConversationType != source.ConversationType || target.Name != input.TargetName ||
		target.DelegateName != wantMetadata.DelegateName || target.TaskSummary != wantMetadata.TaskSummary {
		return fmt.Errorf("target conversation already identifies a different clone: %w", transcriptstore.ErrEventConflict)
	}
	wantContext, err := json.Marshal(wantMetadata.ContextData)
	if err != nil {
		return err
	}
	gotContext, err := json.Marshal(target.ContextData)
	if err != nil {
		return err
	}
	if !bytes.Equal(gotContext, wantContext) {
		return fmt.Errorf("target conversation metadata differs from clone request: %w", transcriptstore.ErrEventConflict)
	}
	return nil
}

func cloneConversationStableID(kind, targetFrameID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("synon-conversation-clone-v1\x00"+kind+"\x00"+targetFrameID)).String()
}

var _ workspaceTransaction = (*sql.Tx)(nil)
var _ workspaceTransaction = (*transcriptstore.ImmediateTransaction)(nil)
