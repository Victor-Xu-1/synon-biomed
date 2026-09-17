package server

import (
	"context"
	"errors"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) compatibilityFrameCanonicalMessageCount(
	frame workspace.CompatibilityFrame,
) (int, bool, error) {
	if s == nil || s.transcriptStore == nil || frame.ID == "" {
		return 0, false, nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frame.ID)
	if err != nil || !found {
		return 0, false, err
	}
	if s.transcriptWebReadModel != nil {
		readModel, active, err := s.activatedTranscriptWebReadModel(
			context.Background(), frameContext.UserID, frame.ID, "",
		)
		if err != nil && active && errors.Is(err, transcriptstore.ErrHistoryBackfillBlocked) {
			// The frame is the runtime/control authority; the Transcript Web read
			// model only supplies its optional message_count decoration. New
			// events deliberately make that projection unavailable until the
			// projector reaches the same fence. Do not turn this normal bounded
			// lag into a 503 for the whole frame snapshot. Omitting the count keeps
			// the canonical history fail-closed without falling back to a stale
			// legacy count.
			return 0, true, nil
		}
		if err != nil || !active {
			return 0, active, err
		}
		return readModel.fence.VisibleMessageCount, true, nil
	}
	messages, _, active, err := s.loadActivatedTranscriptWebHistory(
		context.Background(), frameContext.UserID, frame.ID, "",
	)
	if err != nil || !active {
		return 0, active, err
	}
	return len(transcriptCompatibilityMessages(messages)), true, nil
}

func (s *Server) compatibilityFrameContextProjection(frame workspace.CompatibilityFrame) (map[string]any, error) {
	contextData := make(map[string]any, len(frame.ContextData))
	for key, value := range frame.ContextData {
		contextData[key] = value
	}
	if s == nil || s.transcriptStore == nil || frame.ID == "" {
		return contextData, nil
	}
	frameContext, found, err := s.workspaceStore.GetFrameRealtimeContext(frame.ID)
	if err != nil || !found {
		return nil, err
	}
	authority, found, err := s.transcriptStore.GetFrameAuthorityBySession(
		context.Background(), frameContext.UserID, frame.ID,
	)
	if err != nil {
		return nil, err
	}
	if !found || !authority.TranscriptPayloadActive() {
		return contextData, nil
	}
	stream, found, err := s.transcriptStore.GetFrameStreamBySession(context.Background(), frameContext.UserID, frame.ID)
	if err != nil || !found {
		if err == nil {
			err = transcriptstore.ErrEventConflict
		}
		return nil, err
	}
	state, branches, err := s.transcriptStore.ListBranches(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		return nil, err
	}
	items := make(map[string]any, len(branches))
	for _, branch := range branches {
		var parent any
		if branch.ParentBranchID != "" {
			parent = branch.ParentBranchID
		}
		var forkPoint any
		if branch.ForkEventID != nil {
			forkPoint = branch.ForkPoint
		}
		items[branch.BranchID] = map[string]any{
			"parent_id": parent, "fork_point": forkPoint, "kind": branch.Kind,
			"source_message_id": nullableCompatibilityString(branch.SourceMessageID),
			"created_at":        branch.CreatedAt.UTC().Format(time.RFC3339Nano),
			"updated_at":        branch.UpdatedAt.UTC().Format(time.RFC3339Nano),
			"messages":          nil, "child_frame_ids": nil, "tool_id_to_frame_id": nil,
		}
	}
	contextData["_branch_meta"] = map[string]any{
		"active_branch_id": state.ActiveBranchID, "generation": state.Generation, "branches": items,
	}
	return contextData, nil
}
