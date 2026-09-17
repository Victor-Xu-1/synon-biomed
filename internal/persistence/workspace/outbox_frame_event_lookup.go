package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type RealtimeFrameProjection struct {
	Context          FrameRealtimeContext
	Source           FrameEvent
	WebConversation  bool
	TranscriptBacked bool
}

// FindRealtimeFrameProjectionForOutbox captures every mutable workspace value
// required by fanout in one read. A later delete may remove the source rows,
// but the already claimed predecessor can still complete before the ordered
// delete event without re-reading deleted state.
func (s *Store) FindRealtimeFrameProjectionForOutbox(
	ctx context.Context,
	id string,
) (RealtimeFrameProjection, bool, error) {
	if s == nil || s.db == nil {
		return RealtimeFrameProjection{}, false, errors.New("workspace store is closed")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return RealtimeFrameProjection{}, false, errors.New("frame event id is required")
	}
	var projection RealtimeFrameProjection
	var rawPayload, rawContext string
	err := s.db.QueryRowContext(ctx, `
		SELECT event.id,event.frame_id,event.sequence,event.event_type,event.payload,event.created_at,
			project.user_id,frame.id,frame.incarnation_id,frame.project_id,COALESCE(frame.parent_frame_id,''),
			frame.root_frame_id,frame.root_sequence,frame.agent_name,frame.status,
			frame.conversation_type,frame.name,frame.created_at,frame.updated_at,
			COALESCE(metadata.context_data,'{}'),
			EXISTS(SELECT 1
				FROM transcript_frame_authority AS authority
				JOIN transcript_streams AS stream
					ON stream.stream_uid=authority.active_stream_uid
					AND stream.owner_id=authority.owner_id
					AND stream.session_id=authority.session_id
					AND stream.epoch=authority.active_epoch
				WHERE authority.owner_id=project.user_id AND authority.session_id=frame.id
					AND stream.kind='frame_ref'
					AND authority.read_authority='transcript_payload_v1'
					AND authority.write_authority='transcript_payload_v1'
					AND ((length(authority.activation_id)=32 AND authority.genesis_id IS NULL)
						OR (authority.activation_id IS NULL AND length(authority.genesis_id)=32)))
		FROM frame_events AS event
		JOIN frames AS frame ON frame.id=event.frame_id
		JOIN projects AS project ON project.id=frame.project_id
		LEFT JOIN frame_runtime_metadata AS metadata ON metadata.frame_id=frame.id
		WHERE event.id=?`, id).Scan(
		&projection.Source.ID, &projection.Source.FrameID, &projection.Source.Sequence,
		&projection.Source.Type, &rawPayload, &projection.Source.CreatedAt,
		&projection.Context.UserID, &projection.Context.Frame.ID, &projection.Context.Frame.IncarnationID,
		&projection.Context.Frame.ProjectID,
		&projection.Context.Frame.ParentFrameID, &projection.Context.Frame.RootFrameID,
		&projection.Context.Frame.RootSequence, &projection.Context.Frame.AgentName,
		&projection.Context.Frame.Status, &projection.Context.Frame.ConversationType,
		&projection.Context.Frame.Name, &projection.Context.Frame.CreatedAt,
		&projection.Context.Frame.UpdatedAt, &rawContext, &projection.TranscriptBacked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RealtimeFrameProjection{}, false, nil
	}
	if err != nil {
		return RealtimeFrameProjection{}, false, fmt.Errorf("get realtime outbox frame projection: %w", err)
	}
	if err := json.Unmarshal([]byte(rawPayload), &projection.Source.Payload); err != nil {
		return RealtimeFrameProjection{}, false, fmt.Errorf("decode realtime outbox frame event: %w", err)
	}
	contextData := map[string]any{}
	if err := json.Unmarshal([]byte(rawContext), &contextData); err != nil {
		return RealtimeFrameProjection{}, false, fmt.Errorf("decode realtime outbox frame metadata: %w", err)
	}
	_, hasExtra := contextData["web_extra"]
	_, hasAssistant := contextData["web_assistant"]
	projection.WebConversation = hasExtra || hasAssistant
	return projection, true, nil
}

func (s *Store) FindFrameEventForOutbox(ctx context.Context, id string) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return FrameEvent{}, false, errors.New("frame event id is required")
	}
	var event FrameEvent
	var rawPayload string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, frame_id, sequence, event_type, payload, created_at
		FROM frame_events WHERE id = ?`, id).Scan(&event.ID, &event.FrameID, &event.Sequence,
		&event.Type, &rawPayload, &event.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameEvent{}, false, nil
	}
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("get outbox frame event: %w", err)
	}
	if err := json.Unmarshal([]byte(rawPayload), &event.Payload); err != nil {
		return FrameEvent{}, false, fmt.Errorf("decode outbox frame event: %w", err)
	}
	return event, true, nil
}
