package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	FrameBranchEdit   = "edit"
	FrameBranchAnswer = "answer"
	FrameBranchAside  = "aside"
)

type CreateFrameBranchInput struct {
	ID               string
	RootFrameID      string
	SourceFrameID    string
	Mode             string
	MessageIndex     int
	EditedContent    string
	ToolUseID        string
	Response         any
	Request          string
	AgentName        string
	ConversationType string
	Name             string
	Metadata         map[string]any
}

type FrameBranchResult struct {
	Frame                 Frame
	Events                []FrameEvent
	SourceFrameID         string
	RuntimeKeepEventCount int
}

func (s *Store) CreateFrameBranch(input CreateFrameBranchInput) (FrameBranchResult, error) {
	if s == nil || s.db == nil {
		return FrameBranchResult{}, errors.New("workspace store is closed")
	}
	input.ID = strings.TrimSpace(input.ID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.SourceFrameID = strings.TrimSpace(input.SourceFrameID)
	input.Mode = strings.TrimSpace(input.Mode)
	if input.ID == "" || input.RootFrameID == "" {
		return FrameBranchResult{}, errors.New("branch id and root frame id are required")
	}
	if input.SourceFrameID == "" {
		input.SourceFrameID = input.RootFrameID
	}
	if input.SourceFrameID == input.ID {
		return FrameBranchResult{}, errors.New("source and target frame ids must differ")
	}
	switch input.Mode {
	case FrameBranchEdit:
		if input.MessageIndex < 0 {
			return FrameBranchResult{}, errors.New("message index must be non-negative")
		}
		if strings.TrimSpace(input.EditedContent) == "" {
			return FrameBranchResult{}, errors.New("edited content is required")
		}
	case FrameBranchAnswer:
		if strings.TrimSpace(input.ToolUseID) == "" {
			return FrameBranchResult{}, errors.New("tool use id is required")
		}
		if input.Response == nil {
			return FrameBranchResult{}, errors.New("ask-user response is required")
		}
	case FrameBranchAside:
		if strings.TrimSpace(input.Request) == "" {
			return FrameBranchResult{}, errors.New("aside request is required")
		}
	default:
		return FrameBranchResult{}, fmt.Errorf("unsupported branch mode %q", input.Mode)
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameBranchResult{}, fmt.Errorf("begin frame branch transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	root, err := branchFrameByID(ctx, tx, input.RootFrameID)
	if err != nil {
		return FrameBranchResult{}, err
	}
	if root.RootFrameID != root.ID {
		return FrameBranchResult{}, fmt.Errorf("frame %q is not a root frame", input.RootFrameID)
	}
	source, err := branchFrameByID(ctx, tx, input.SourceFrameID)
	if err != nil {
		return FrameBranchResult{}, err
	}
	if source.RootFrameID != root.ID || source.ProjectID != root.ProjectID {
		return FrameBranchResult{}, fmt.Errorf("source frame %q does not belong to root frame %q", source.ID, root.ID)
	}
	sourceEvents, err := branchSourceEvents(ctx, tx, source.ID)
	if err != nil {
		return FrameBranchResult{}, err
	}
	prepared, keepCount, err := prepareBranchEvents(input, sourceEvents)
	if err != nil {
		return FrameBranchResult{}, err
	}

	agentName := strings.TrimSpace(input.AgentName)
	if agentName == "" {
		agentName = source.AgentName
	}
	conversationType := strings.TrimSpace(input.ConversationType)
	if conversationType == "" {
		conversationType = "branch"
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		if input.Mode == FrameBranchAside {
			name = strings.TrimSpace("Aside from " + source.Name)
		} else {
			name = strings.TrimSpace("Fork of " + source.Name)
		}
	}
	var rootSequence int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(root_sequence), 0) + 1 FROM frames WHERE root_frame_id = ?", root.ID).Scan(&rootSequence); err != nil {
		return FrameBranchResult{}, fmt.Errorf("allocate branch root sequence: %w", err)
	}
	now := s.now().UTC()
	frame := Frame{
		ID: input.ID, IncarnationID: uuid.NewString(), ProjectID: root.ProjectID, ParentFrameID: source.ID,
		RootFrameID: root.ID, RootSequence: rootSequence, AgentName: agentName,
		Status: FrameStatusProcessing, ConversationType: conversationType, Name: name,
		CreatedAt: now, UpdatedAt: now,
	}
	insertFrame := "INSERT INTO frames (id, incarnation_id, project_id, parent_frame_id, root_frame_id, root_sequence, agent_name, status, conversation_type, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
	if _, err := tx.ExecContext(ctx, insertFrame,
		frame.ID, frame.IncarnationID, frame.ProjectID, frame.ParentFrameID, frame.RootFrameID, frame.RootSequence,
		frame.AgentName, frame.Status, frame.ConversationType, frame.Name, frame.CreatedAt, frame.UpdatedAt,
	); err != nil {
		return FrameBranchResult{}, fmt.Errorf("insert branch frame: %w", err)
	}

	events := make([]FrameEvent, 0, len(prepared))
	insertEvent := "INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)"
	for index, item := range prepared {
		rawPayload, err := json.Marshal(item.Payload)
		if err != nil {
			return FrameBranchResult{}, fmt.Errorf("marshal branch event payload: %w", err)
		}
		event := FrameEvent{
			ID: uuid.NewString(), FrameID: frame.ID, Sequence: int64(index + 1),
			Type: item.Type, Payload: item.Payload, CreatedAt: now.Add(time.Duration(index) * time.Nanosecond),
		}
		if _, err := tx.ExecContext(ctx, insertEvent,
			event.ID, event.FrameID, event.Sequence, event.Type, string(rawPayload), event.CreatedAt,
		); err != nil {
			return FrameBranchResult{}, fmt.Errorf("insert branch event: %w", err)
		}
		events = append(events, event)
	}
	if err := tx.Commit(); err != nil {
		return FrameBranchResult{}, fmt.Errorf("commit frame branch: %w", err)
	}
	return FrameBranchResult{
		Frame: frame, Events: events, SourceFrameID: source.ID,
		RuntimeKeepEventCount: keepCount,
	}, nil
}

func branchFrameByID(ctx context.Context, tx *sql.Tx, id string) (Frame, error) {
	var frame Frame
	query := "SELECT id, project_id, COALESCE(parent_frame_id, ''), root_frame_id, root_sequence, agent_name, status, conversation_type, name, created_at, updated_at FROM frames WHERE id = ?"
	err := tx.QueryRowContext(ctx, query, id).Scan(
		&frame.ID, &frame.ProjectID, &frame.ParentFrameID, &frame.RootFrameID,
		&frame.RootSequence, &frame.AgentName, &frame.Status, &frame.ConversationType,
		&frame.Name, &frame.CreatedAt, &frame.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Frame{}, fmt.Errorf("frame %q does not exist", id)
	}
	if err != nil {
		return Frame{}, fmt.Errorf("get branch frame %q: %w", id, err)
	}
	return frame, nil
}

func branchSourceEvents(ctx context.Context, tx *sql.Tx, frameID string) ([]FrameEvent, error) {
	query := "SELECT id, frame_id, sequence, event_type, payload, created_at FROM frame_events WHERE frame_id = ? ORDER BY sequence"
	rows, err := tx.QueryContext(ctx, query, frameID)
	if err != nil {
		return nil, fmt.Errorf("query source branch events: %w", err)
	}
	defer rows.Close()
	events := make([]FrameEvent, 0)
	for rows.Next() {
		var event FrameEvent
		var rawPayload string
		if err := rows.Scan(&event.ID, &event.FrameID, &event.Sequence, &event.Type, &rawPayload, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan source branch event: %w", err)
		}
		if err := json.Unmarshal([]byte(rawPayload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode source branch event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source branch events: %w", err)
	}
	return events, nil
}

func prepareBranchEvents(input CreateFrameBranchInput, source []FrameEvent) ([]FrameEventInput, int, error) {
	metadata := cloneBranchPayload(input.Metadata)
	metadata["sourceFrameId"] = input.SourceFrameID
	metadata["branchMode"] = input.Mode
	switch input.Mode {
	case FrameBranchEdit:
		if input.MessageIndex >= len(source) {
			return nil, 0, fmt.Errorf("message index %d is outside source history of %d events", input.MessageIndex, len(source))
		}
		prepared := cloneFrameEventInputs(source[:input.MessageIndex+1])
		replacement := prepared[len(prepared)-1].Payload
		replacement["text"] = strings.TrimSpace(input.EditedContent)
		replacement["editedContent"] = strings.TrimSpace(input.EditedContent)
		replacement["sourceEventId"] = source[input.MessageIndex].ID
		prepared = append(prepared, FrameEventInput{Type: "branch_created", Payload: metadata})
		return prepared, input.MessageIndex, nil
	case FrameBranchAnswer:
		matched := -1
		for index, event := range source {
			if branchPayloadContainsString(event.Payload, input.ToolUseID) {
				matched = index
				break
			}
		}
		if matched < 0 {
			return nil, 0, fmt.Errorf("tool use id %q does not exist in source history", input.ToolUseID)
		}
		prepared := cloneFrameEventInputs(source[:matched+1])
		prepared = append(prepared,
			FrameEventInput{Type: "branch_created", Payload: metadata},
			FrameEventInput{Type: "ask_user_answer", Payload: map[string]any{
				"toolUseId": input.ToolUseID, "response": input.Response, "role": "user",
			}},
		)
		return prepared, matched + 1, nil
	case FrameBranchAside:
		prepared := cloneFrameEventInputs(source)
		asideMetadata := cloneBranchPayload(metadata)
		asideMetadata["request"] = strings.TrimSpace(input.Request)
		prepared = append(prepared,
			FrameEventInput{Type: "aside_created", Payload: asideMetadata},
			FrameEventInput{Type: "user_message", Payload: map[string]any{
				"messageUuid": uuid.NewString(), "clientMessageId": uuid.NewString(),
				"text": strings.TrimSpace(input.Request), "role": "user",
			}},
		)
		return prepared, len(source), nil
	default:
		return nil, 0, fmt.Errorf("unsupported branch mode %q", input.Mode)
	}
}

func cloneFrameEventInputs(events []FrameEvent) []FrameEventInput {
	cloned := make([]FrameEventInput, 0, len(events))
	for _, event := range events {
		payload := cloneBranchPayload(event.Payload)
		payload["sourceEventId"] = event.ID
		payload["sourceSequence"] = event.Sequence
		cloned = append(cloned, FrameEventInput{Type: event.Type, Payload: payload})
	}
	return cloned
}

func cloneBranchPayload(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return map[string]any{}
	}
	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil || cloned == nil {
		return map[string]any{}
	}
	return cloned
}

func branchPayloadContainsString(value any, target string) bool {
	switch typed := value.(type) {
	case string:
		return typed == target
	case map[string]any:
		for key, item := range typed {
			if (key == "toolUseId" || key == "tool_use_id" || key == "id") && fmt.Sprint(item) == target {
				return true
			}
			if branchPayloadContainsString(item, target) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if branchPayloadContainsString(item, target) {
				return true
			}
		}
	}
	return false
}
