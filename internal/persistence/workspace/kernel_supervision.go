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

	transcriptstore "synon-go/internal/persistence/transcript"
)

const DefaultKernelDelegationSpawnCap = 1000

type KernelDelegateRequest struct {
	Task           string
	Name           string
	ContextSummary string
	Profile        string
	OutputSchema   map[string]any
	Model          string
}

type CreateKernelDelegatesInput struct {
	ParentFrameID string
	OwnerUserID   string
	ToolUseID     string
	Requests      []KernelDelegateRequest
	SpawnCap      int
	MaxDepth      int
}

type KernelSupervisedChild struct {
	FrameID        string         `json:"frame_id"`
	ParentFrameID  string         `json:"parent_frame_id"`
	RootFrameID    string         `json:"root_frame_id"`
	ProjectID      string         `json:"project_id"`
	OwnerUserID    string         `json:"owner_user_id"`
	ToolUseID      string         `json:"tool_use_id"`
	AgentName      string         `json:"agent_name"`
	Name           string         `json:"name,omitempty"`
	Task           string         `json:"task"`
	ContextSummary string         `json:"context_summary,omitempty"`
	Model          string         `json:"model,omitempty"`
	OutputSchema   map[string]any `json:"output_schema,omitempty"`
	Status         string         `json:"status"`
	Output         map[string]any `json:"output_data,omitempty"`
	Error          string         `json:"error,omitempty"`
	Dispatched     bool           `json:"dispatched"`
	StartedAt      time.Time      `json:"started_at"`
	CompletedAt    *time.Time     `json:"completed_at,omitempty"`
}

type KernelDelegationStats struct {
	SpawnedThisTask int `json:"spawned_this_task"`
	Cap             int `json:"cap"`
	Remaining       int `json:"remaining"`
}

func (s *Store) ensureKernelSupervisionSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS kernel_child_supervision (
			frame_id TEXT PRIMARY KEY REFERENCES frames(id) ON DELETE CASCADE,
			parent_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			root_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			owner_user_id TEXT NOT NULL,
			tool_use_id TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			delegate_name TEXT NOT NULL DEFAULT '',
			task TEXT NOT NULL,
			context_summary TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			output_schema TEXT NOT NULL DEFAULT 'null',
			status TEXT NOT NULL,
			output_data TEXT NOT NULL DEFAULT 'null',
			error TEXT NOT NULL DEFAULT '',
			dispatched INTEGER NOT NULL DEFAULT 0,
			started_at TIMESTAMP NOT NULL,
			completed_at TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS kernel_child_parent_status_idx
			ON kernel_child_supervision(parent_frame_id, status, started_at, frame_id)`,
		`CREATE INDEX IF NOT EXISTS kernel_child_root_spawn_idx
			ON kernel_child_supervision(root_frame_id, started_at, frame_id)`,
		`CREATE TABLE IF NOT EXISTS kernel_child_message_clock (
			frame_id TEXT PRIMARY KEY REFERENCES kernel_child_supervision(frame_id) ON DELETE CASCADE,
			enqueued_generation INTEGER NOT NULL DEFAULT 0,
			consumed_generation INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS kernel_child_messages (
			id TEXT PRIMARY KEY,
			frame_id TEXT NOT NULL REFERENCES kernel_child_supervision(frame_id) ON DELETE CASCADE,
			generation INTEGER NOT NULL,
			sender_frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE CASCADE,
			message TEXT NOT NULL,
			kind TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			consumed_at TIMESTAMP,
			UNIQUE(frame_id, generation)
		)`,
		`CREATE INDEX IF NOT EXISTS kernel_child_messages_pending_idx
			ON kernel_child_messages(frame_id, consumed_at, generation)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("prepare kernel supervision schema: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateKernelSupervisedChildren(ctx context.Context, input CreateKernelDelegatesInput) ([]KernelSupervisedChild, error) {
	if ctx == nil {
		return nil, errors.New("kernel delegation context is required")
	}
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return nil, err
	}
	input.ParentFrameID = strings.TrimSpace(input.ParentFrameID)
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	if input.ParentFrameID == "" || input.OwnerUserID == "" || input.ToolUseID == "" {
		return nil, errors.New("parent frame, owner, and tool use id are required")
	}
	if len(input.Requests) == 0 {
		return []KernelSupervisedChild{}, nil
	}
	if input.SpawnCap <= 0 {
		input.SpawnCap = DefaultKernelDelegationSpawnCap
	}
	if input.MaxDepth <= 0 {
		input.MaxDepth = 1
	}
	if input.MaxDepth > 2 {
		input.MaxDepth = 2
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin kernel delegation: %w", err)
	}
	defer tx.Rollback()

	var projectID, rootFrameID, parentAgent, owner string
	if err := tx.QueryRowContext(ctx, `
		SELECT f.project_id, f.root_frame_id, f.agent_name, p.user_id
		FROM frames f JOIN projects p ON p.id = f.project_id WHERE f.id = ?`, input.ParentFrameID,
	).Scan(&projectID, &rootFrameID, &parentAgent, &owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("kernel delegation parent is unavailable")
		}
		return nil, fmt.Errorf("load kernel delegation parent: %w", err)
	}
	if owner != input.OwnerUserID {
		return nil, errors.New("kernel delegation parent is unavailable")
	}
	depth := 1
	ancestorID := input.ParentFrameID
	for {
		var nextParent string
		err := tx.QueryRowContext(ctx, `SELECT parent_frame_id FROM kernel_child_supervision WHERE frame_id = ?`, ancestorID).Scan(&nextParent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("resolve delegation depth: %w", err)
		}
		depth++
		if depth > input.MaxDepth {
			return nil, fmt.Errorf("delegation max depth %d exceeded", input.MaxDepth)
		}
		ancestorID = nextParent
	}
	var spawned int
	countQuery := `SELECT COUNT(*) FROM kernel_child_supervision WHERE root_frame_id = ?`
	countID := rootFrameID
	if depth > 1 {
		countQuery = `SELECT COUNT(*) FROM kernel_child_supervision WHERE parent_frame_id = ?`
		countID = input.ParentFrameID
	}
	if err := tx.QueryRowContext(ctx, countQuery, countID).Scan(&spawned); err != nil {
		return nil, fmt.Errorf("count kernel delegations: %w", err)
	}
	if spawned+len(input.Requests) > input.SpawnCap {
		return nil, fmt.Errorf("delegation spawn cap exceeded: spawned=%d requested=%d cap=%d", spawned, len(input.Requests), input.SpawnCap)
	}

	now := s.now().UTC()
	children := make([]KernelSupervisedChild, 0, len(input.Requests))
	toolMapping := map[string]any{}
	var rawParentContext sql.NullString
	_ = tx.QueryRowContext(ctx, `SELECT context_data FROM frame_runtime_metadata WHERE frame_id = ?`, input.ParentFrameID).Scan(&rawParentContext)
	parentContext := map[string]any{}
	if rawParentContext.Valid && strings.TrimSpace(rawParentContext.String) != "" {
		_ = json.Unmarshal([]byte(rawParentContext.String), &parentContext)
	}
	if existing, ok := parentContext["_tool_id_to_frame_id"].(map[string]any); ok {
		for key, value := range existing {
			toolMapping[key] = value
		}
	}

	for index, request := range input.Requests {
		request.Task = strings.TrimSpace(request.Task)
		if request.Task == "" {
			return nil, fmt.Errorf("delegate request %d task is required", index)
		}
		profile := strings.TrimSpace(request.Profile)
		if profile == "" {
			profile = parentAgent
		}
		if depth > 1 && !strings.EqualFold(profile, parentAgent) {
			return nil, fmt.Errorf("delegate request %d nested profile must match its parent profile", index)
		}
		frameID := uuid.NewString()
		incarnationID := uuid.NewString()
		var rootSequence int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(root_sequence), 0) + 1 FROM frames WHERE root_frame_id = ?`, rootFrameID).Scan(&rootSequence); err != nil {
			return nil, fmt.Errorf("allocate child root sequence: %w", err)
		}
		name := strings.TrimSpace(request.Name)
		if name == "" {
			name = profile
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO frames (id, incarnation_id, project_id, parent_frame_id, root_frame_id, root_sequence, agent_name, status, conversation_type, name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'processing', 'delegate', ?, ?, ?)`,
			frameID, incarnationID, projectID, input.ParentFrameID, rootFrameID, rootSequence, profile, name, now, now); err != nil {
			return nil, fmt.Errorf("insert delegated child frame: %w", err)
		}
		contextData := map[string]any{
			"task": request.Task, "context_summary": request.ContextSummary,
			"profile": profile, "model": request.Model, "output_schema": request.OutputSchema,
			"_latest_tool_block": map[string]any{"tool_use_id": input.ToolUseID, "status": "processing"},
		}
		rawInput, _ := json.Marshal(map[string]any{"request": request.Task})
		rawContext, _ := json.Marshal(contextData)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO frame_runtime_metadata (frame_id, delegate_name, input_data, context_data, model, task_summary)
			VALUES (?, ?, ?, ?, ?, ?)`, frameID, nullableString(request.Name), string(rawInput), string(rawContext), nullableString(request.Model), request.Task); err != nil {
			return nil, fmt.Errorf("insert delegated child metadata: %w", err)
		}
		rawSchema, _ := json.Marshal(request.OutputSchema)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO kernel_child_supervision
			(frame_id, parent_frame_id, root_frame_id, project_id, owner_user_id, tool_use_id, agent_name, delegate_name, task, context_summary, model, output_schema, status, started_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'processing', ?)`,
			frameID, input.ParentFrameID, rootFrameID, projectID, owner, input.ToolUseID, profile,
			request.Name, request.Task, request.ContextSummary, request.Model, string(rawSchema), now); err != nil {
			return nil, fmt.Errorf("insert child supervision record: %w", err)
		}
		if _, err := appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
			ID:      uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-delegate-user:"+input.ToolUseID+":"+frameID)).String(),
			FrameID: frameID, Type: "user_message", Payload: map[string]any{
				"role": "user", "text": request.Task, "context_summary": request.ContextSummary,
				"messageUuid": "delegate-task-" + frameID,
			},
		}, now); err != nil {
			return nil, err
		}
		toolMapping[input.ToolUseID+fmt.Sprintf(":%d", index)] = frameID
		if len(input.Requests) == 1 {
			toolMapping[input.ToolUseID] = frameID
		}
		children = append(children, KernelSupervisedChild{
			FrameID: frameID, ParentFrameID: input.ParentFrameID, RootFrameID: rootFrameID,
			ProjectID: projectID, OwnerUserID: owner, ToolUseID: input.ToolUseID,
			AgentName: profile, Name: request.Name, Task: request.Task,
			ContextSummary: request.ContextSummary, Model: request.Model,
			OutputSchema: request.OutputSchema, Status: "processing", StartedAt: now,
		})
	}
	parentContext["_tool_id_to_frame_id"] = toolMapping
	rawUpdatedContext, _ := json.Marshal(parentContext)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, context_data) VALUES (?, ?)
		ON CONFLICT(frame_id) DO UPDATE SET context_data = excluded.context_data`, input.ParentFrameID, string(rawUpdatedContext)); err != nil {
		return nil, fmt.Errorf("persist parent child mapping: %w", err)
	}
	childIDs := make([]any, len(children))
	for index := range children {
		childIDs[index] = children[index].FrameID
	}
	if _, err := appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
		ID:      uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-delegate-tool-use:"+input.ParentFrameID+":"+input.ToolUseID)).String(),
		FrameID: input.ParentFrameID, Type: "tool_use", Payload: map[string]any{
			"tool_use_id": input.ToolUseID, "tool_name": "delegate", "child_frame_ids": childIDs,
		},
	}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit kernel delegation: %w", err)
	}
	return children, nil
}

func appendKernelSupervisionFrameEvent(ctx context.Context, tx workspaceTransaction, input FrameEventInput, now time.Time) (FrameEvent, error) {
	payload := input.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, err
	}
	existing, storedPayload, found, err := frameEventByID(ctx, tx, input.ID)
	if err != nil {
		return FrameEvent{}, err
	}
	if found {
		if existing.FrameID != input.FrameID || existing.Type != input.Type || storedPayload != string(raw) {
			return FrameEvent{}, fmt.Errorf("frame event id %q already identifies a different frame event", input.ID)
		}
		return existing, nil
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?`, input.FrameID).Scan(&sequence); err != nil {
		return FrameEvent{}, err
	}
	event := FrameEvent{ID: input.ID, FrameID: input.FrameID, Sequence: sequence, Type: input.Type, Payload: payload, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.FrameID, event.Sequence, event.Type, string(raw), event.CreatedAt); err != nil {
		return FrameEvent{}, err
	}
	return event, nil
}

func (s *Store) MarkKernelChildDispatched(ctx context.Context, frameID, ownerUserID string) error {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE kernel_child_supervision SET dispatched = 1 WHERE frame_id = ? AND owner_user_id = ?`, strings.TrimSpace(frameID), strings.TrimSpace(ownerUserID))
	if err != nil {
		return fmt.Errorf("mark child dispatched: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("delegated child is unavailable")
	}
	return nil
}

func (s *Store) GetKernelSupervisedChild(ctx context.Context, parentFrameID, frameID, ownerUserID string) (KernelSupervisedChild, bool, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return KernelSupervisedChild{}, false, err
	}
	return scanKernelSupervisedChild(s.db.QueryRowContext(ctx, `
		SELECT frame_id, parent_frame_id, root_frame_id, project_id, owner_user_id, tool_use_id,
			agent_name, delegate_name, task, context_summary, model, output_schema, status,
			output_data, error, dispatched, started_at, completed_at
		FROM kernel_child_supervision WHERE frame_id = ? AND parent_frame_id = ? AND owner_user_id = ?`,
		strings.TrimSpace(frameID), strings.TrimSpace(parentFrameID), strings.TrimSpace(ownerUserID)))
}

func (s *Store) ListKernelActiveChildren(ctx context.Context, parentFrameID, ownerUserID string) ([]KernelSupervisedChild, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT frame_id, parent_frame_id, root_frame_id, project_id, owner_user_id, tool_use_id,
			agent_name, delegate_name, task, context_summary, model, output_schema, status,
			output_data, error, dispatched, started_at, completed_at
		FROM kernel_child_supervision
		WHERE parent_frame_id = ? AND owner_user_id = ? AND status IN ('processing','awaiting_user_response')
		ORDER BY started_at, frame_id`, strings.TrimSpace(parentFrameID), strings.TrimSpace(ownerUserID))
	if err != nil {
		return nil, fmt.Errorf("list active delegated children: %w", err)
	}
	defer rows.Close()
	children := []KernelSupervisedChild{}
	for rows.Next() {
		child, _, err := scanKernelSupervisedChild(rows)
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	return children, rows.Err()
}

func (s *Store) KernelDelegationStats(ctx context.Context, frameID, ownerUserID string, cap int) (KernelDelegationStats, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return KernelDelegationStats{}, err
	}
	if cap <= 0 {
		cap = DefaultKernelDelegationSpawnCap
	}
	var rootFrameID, owner string
	if err := s.db.QueryRowContext(ctx, `SELECT f.root_frame_id, p.user_id FROM frames f JOIN projects p ON p.id=f.project_id WHERE f.id=?`, frameID).Scan(&rootFrameID, &owner); err != nil || owner != ownerUserID {
		return KernelDelegationStats{}, errors.New("kernel delegation frame is unavailable")
	}
	var spawned int
	var childRecordCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_child_supervision WHERE frame_id=? AND owner_user_id=?`, frameID, ownerUserID).Scan(&childRecordCount); err != nil {
		return KernelDelegationStats{}, err
	}
	countQuery := `SELECT COUNT(*) FROM kernel_child_supervision WHERE root_frame_id=?`
	countID := rootFrameID
	if childRecordCount != 0 {
		countQuery = `SELECT COUNT(*) FROM kernel_child_supervision WHERE parent_frame_id=?`
		countID = frameID
	}
	if err := s.db.QueryRowContext(ctx, countQuery, countID).Scan(&spawned); err != nil {
		return KernelDelegationStats{}, err
	}
	remaining := cap - spawned
	if remaining < 0 {
		remaining = 0
	}
	return KernelDelegationStats{SpawnedThisTask: spawned, Cap: cap, Remaining: remaining}, nil
}

func (s *Store) CompleteKernelSupervisedChild(ctx context.Context, frameID, ownerUserID, status string, output map[string]any, failure string) (KernelSupervisedChild, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return KernelSupervisedChild{}, err
	}
	status = strings.TrimSpace(status)
	if status != "completed" && status != "failed" && status != "cancelled" && status != "awaiting_user_response" {
		return KernelSupervisedChild{}, fmt.Errorf("unsupported child status %q", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KernelSupervisedChild{}, err
	}
	defer tx.Rollback()
	child, found, err := scanKernelSupervisedChild(tx.QueryRowContext(ctx, `
		SELECT frame_id, parent_frame_id, root_frame_id, project_id, owner_user_id, tool_use_id,
			agent_name, delegate_name, task, context_summary, model, output_schema, status,
			output_data, error, dispatched, started_at, completed_at
		FROM kernel_child_supervision WHERE frame_id=? AND owner_user_id=?`, frameID, ownerUserID))
	if err != nil || !found {
		if err != nil {
			return KernelSupervisedChild{}, err
		}
		return KernelSupervisedChild{}, errors.New("delegated child is unavailable")
	}
	var generation int64
	_ = tx.QueryRowContext(ctx, `SELECT consumed_generation FROM kernel_child_message_clock WHERE frame_id=?`, frameID).Scan(&generation)
	if isKernelChildTerminalStatus(child.Status) {
		completedAt := s.now().UTC()
		if child.CompletedAt != nil {
			completedAt = child.CompletedAt.UTC()
		}
		if err := createKernelChildCompletionNotificationTx(ctx, tx, child, generation, kernelChildPublicStatus(child.Status, child.Output), child.Output, child.Error, completedAt); err != nil {
			return KernelSupervisedChild{}, err
		}
		if err := tx.Commit(); err != nil {
			return KernelSupervisedChild{}, err
		}
		return child, nil
	}
	stoppedByParent, _ := output["stopped_by_parent"].(bool)
	if (status == "completed" || status == "failed") && !stoppedByParent {
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM kernel_child_messages WHERE frame_id=? AND consumed_at IS NULL`, frameID).Scan(&pending); err != nil {
			return KernelSupervisedChild{}, err
		}
		if pending > 0 {
			now := s.now().UTC()
			if _, err := tx.ExecContext(ctx, `UPDATE kernel_child_supervision SET status='processing', completed_at=NULL, error='' WHERE frame_id=?`, frameID); err != nil {
				return KernelSupervisedChild{}, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE frames SET status='processing', updated_at=? WHERE id=?`, now, frameID); err != nil {
				return KernelSupervisedChild{}, err
			}
			if err := tx.Commit(); err != nil {
				return KernelSupervisedChild{}, err
			}
			child.Status, child.CompletedAt, child.Error = "processing", nil, ""
			return child, nil
		}
	}
	rawOutput, _ := json.Marshal(output)
	now := s.now().UTC()
	var completedAt any
	if isKernelChildTerminalStatus(status) {
		completedAt = now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE kernel_child_supervision SET status=?, output_data=?, error=?, completed_at=? WHERE frame_id=?`,
		status, string(rawOutput), failure, completedAt, frameID); err != nil {
		return KernelSupervisedChild{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET status=?, updated_at=? WHERE id=?`, status, now, frameID); err != nil {
		return KernelSupervisedChild{}, err
	}
	eventType := "assistant_message"
	payload := map[string]any{"role": "assistant", "status": status, "output_data": output}
	if failure != "" {
		eventType = "frame_failed"
		payload["error"] = failure
	}
	if _, err := appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
		ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("kernel-child-final:%s:%d:%s", frameID, generation, status))).String(), FrameID: frameID,
		Type: eventType, Payload: payload,
	}, now); err != nil {
		return KernelSupervisedChild{}, err
	}
	if isKernelChildTerminalStatus(status) {
		if err := createKernelChildCompletionNotificationTx(ctx, tx, child, generation, kernelChildPublicStatus(status, output), output, failure, now); err != nil {
			return KernelSupervisedChild{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return KernelSupervisedChild{}, err
	}
	child.Status, child.Output, child.Error = status, output, failure
	if isKernelChildTerminalStatus(status) {
		child.CompletedAt = &now
	}
	return child, nil
}

func kernelChildPublicStatus(status string, output map[string]any) string {
	if status == "completed" {
		if stopped, _ := output["stopped_by_parent"].(bool); stopped {
			return "cancelled"
		}
	}
	return status
}

type KernelStopTreeResult struct {
	Target             KernelSupervisedChild
	StoppedDescendants []KernelSupervisedChild
	Applied            bool
}

func (s *Store) StopKernelSupervisedChildTree(ctx context.Context, parentFrameID, frameID, ownerUserID, reason string) (KernelStopTreeResult, error) {
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return KernelStopTreeResult{}, err
	}
	parentFrameID = strings.TrimSpace(parentFrameID)
	frameID = strings.TrimSpace(frameID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	reason = strings.TrimSpace(reason)
	if parentFrameID == "" || frameID == "" || ownerUserID == "" || reason == "" {
		return KernelStopTreeResult{}, errors.New("kernel stop tree authority is required")
	}
	var result KernelStopTreeResult
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		target, found, err := scanKernelSupervisedChild(tx.QueryRowContext(ctx, `
			SELECT frame_id,parent_frame_id,root_frame_id,project_id,owner_user_id,tool_use_id,
				agent_name,delegate_name,task,context_summary,model,output_schema,status,
				output_data,error,dispatched,started_at,completed_at
			FROM kernel_child_supervision WHERE frame_id=? AND parent_frame_id=? AND owner_user_id=?`,
			frameID, parentFrameID, ownerUserID))
		if err != nil {
			return err
		}
		if !found {
			return errors.New("delegated child is unavailable")
		}
		result.Target = target
		if isKernelChildTerminalStatus(target.Status) {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `WITH RECURSIVE descendants(frame_id,depth) AS (
			SELECT frame_id,1 FROM kernel_child_supervision WHERE parent_frame_id=? AND owner_user_id=?
			UNION ALL
			SELECT child.frame_id,descendants.depth+1
			FROM kernel_child_supervision child JOIN descendants ON child.parent_frame_id=descendants.frame_id
			WHERE child.owner_user_id=?
		)
		SELECT child.frame_id,child.parent_frame_id,child.root_frame_id,child.project_id,child.owner_user_id,child.tool_use_id,
			child.agent_name,child.delegate_name,child.task,child.context_summary,child.model,child.output_schema,child.status,
			child.output_data,child.error,child.dispatched,child.started_at,child.completed_at
		FROM descendants JOIN kernel_child_supervision child ON child.frame_id=descendants.frame_id
		ORDER BY descendants.depth DESC,child.started_at,child.frame_id`, frameID, ownerUserID, ownerUserID)
		if err != nil {
			return err
		}
		descendants := []KernelSupervisedChild{}
		for rows.Next() {
			descendant, _, scanErr := scanKernelSupervisedChild(rows)
			if scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
			descendants = append(descendants, descendant)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, descendant := range descendants {
			if isKernelChildTerminalStatus(descendant.Status) {
				continue
			}
			stopped, applied, err := s.stopKernelSupervisedChildTx(ctx, tx, descendant, reason)
			if err != nil {
				return err
			}
			if applied {
				result.StoppedDescendants = append(result.StoppedDescendants, stopped)
			}
		}
		stopped, applied, err := s.stopKernelSupervisedChildTx(ctx, tx, target, reason)
		if err != nil {
			return err
		}
		result.Target = stopped
		result.Applied = applied
		return nil
	})
	return result, err
}

func (s *Store) stopKernelSupervisedChildTx(ctx context.Context, tx *transcriptstore.ImmediateTransaction, child KernelSupervisedChild, reason string) (KernelSupervisedChild, bool, error) {
	output := make(map[string]any, len(child.Output)+2)
	for key, value := range child.Output {
		output[key] = value
	}
	output["stopped_by_parent"] = true
	output["reason"] = reason
	rawOutput, err := json.Marshal(output)
	if err != nil {
		return KernelSupervisedChild{}, false, errors.New("encode stopped child output")
	}
	now := s.now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE kernel_child_supervision
		SET status='completed',output_data=?,error='',completed_at=?
		WHERE frame_id=? AND owner_user_id=? AND status NOT IN ('completed','failed','cancelled','orphaned')`,
		string(rawOutput), now, child.FrameID, child.OwnerUserID)
	if err != nil {
		return KernelSupervisedChild{}, false, err
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return KernelSupervisedChild{}, false, err
	}
	if changed == 0 {
		current, found, loadErr := scanKernelSupervisedChild(tx.QueryRowContext(ctx, `
			SELECT frame_id,parent_frame_id,root_frame_id,project_id,owner_user_id,tool_use_id,
				agent_name,delegate_name,task,context_summary,model,output_schema,status,
				output_data,error,dispatched,started_at,completed_at
			FROM kernel_child_supervision WHERE frame_id=? AND owner_user_id=?`, child.FrameID, child.OwnerUserID))
		if loadErr != nil || !found {
			if loadErr != nil {
				return KernelSupervisedChild{}, false, loadErr
			}
			return KernelSupervisedChild{}, false, errors.New("delegated child is unavailable")
		}
		return current, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET status='completed',updated_at=? WHERE id=?`, now, child.FrameID); err != nil {
		return KernelSupervisedChild{}, false, err
	}
	var generation int64
	_ = tx.QueryRowContext(ctx, `SELECT consumed_generation FROM kernel_child_message_clock WHERE frame_id=?`, child.FrameID).Scan(&generation)
	if _, err := appendKernelSupervisionFrameEvent(ctx, tx, FrameEventInput{
		ID:      uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("kernel-child-final:%s:%d:completed", child.FrameID, generation))).String(),
		FrameID: child.FrameID, Type: "assistant_message",
		Payload: map[string]any{"role": "assistant", "status": "completed", "output_data": output},
	}, now); err != nil {
		return KernelSupervisedChild{}, false, err
	}
	if err := createKernelChildCompletionNotificationTx(ctx, tx, child, generation, "cancelled", output, "", now); err != nil {
		return KernelSupervisedChild{}, false, err
	}
	child.Status, child.Output, child.Error, child.CompletedAt = "completed", output, "", &now
	return child, true, nil
}

func createKernelChildCompletionNotificationTx(
	ctx context.Context,
	tx workspaceTransaction,
	child KernelSupervisedChild,
	generation int64,
	status string,
	output map[string]any,
	failure string,
	completedAt time.Time,
) error {
	collectedID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("kernel-child-landing-collected:%s:%s:%d", child.ParentFrameID, child.FrameID, generation))).String()
	if _, _, found, err := frameEventByID(ctx, tx, collectedID); err != nil {
		return err
	} else if found {
		return nil
	}
	_, _, err := createNotificationTx(ctx, tx, CreateNotificationInput{
		ID:            uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("kernel-child-notification:%s:%d", child.FrameID, generation))).String(),
		SenderFrameID: child.FrameID, RecipientFrameID: child.ParentFrameID,
		RootFrameID: child.RootFrameID, OwnerUserID: child.OwnerUserID,
		NotificationType: "child_landed",
		Payload: map[string]any{
			"status": status, "_completion_bullets": kernelCompletionBullets(output, failure),
			"wall_s": completedAt.Sub(child.StartedAt).Seconds(), "child_frame_id": child.FrameID,
			"frame_id": child.FrameID, "agent_name": child.AgentName, "generation": generation,
		},
	}, completedAt)
	return err
}

func (s *Store) FlushUndeliveredKernelChildLandings(ctx context.Context, parentFrameID, rootFrameID, ownerUserID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	if err := s.ensureKernelSupervisionSchema(ctx); err != nil {
		return 0, err
	}
	flushed := 0
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := validateNotificationAuthorityTx(ctx, tx, parentFrameID, parentFrameID, rootFrameID, ownerUserID, false); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT child.frame_id FROM kernel_child_supervision child
			LEFT JOIN kernel_child_message_clock clock ON clock.frame_id=child.frame_id
			WHERE child.parent_frame_id=? AND child.root_frame_id=? AND child.owner_user_id=?
				AND child.status IN ('completed','failed','cancelled')
				AND NOT EXISTS (SELECT 1 FROM notifications notification
					WHERE notification.sender_frame_id=child.frame_id
						AND notification.recipient_frame_id=child.parent_frame_id
						AND notification.notification_type='child_landed')
				AND NOT EXISTS (SELECT 1 FROM frame_events collected
					WHERE collected.frame_id=child.parent_frame_id
						AND collected.event_type='kernel_child_landing_collected'
						AND json_extract(collected.payload,'$.child_frame_id')=child.frame_id
						AND json_extract(collected.payload,'$.generation')=COALESCE(clock.consumed_generation,0))
			ORDER BY child.completed_at,child.frame_id`, strings.TrimSpace(parentFrameID), strings.TrimSpace(rootFrameID), strings.TrimSpace(ownerUserID))
		if err != nil {
			return err
		}
		frameIDs := []string{}
		for rows.Next() {
			var frameID string
			if err := rows.Scan(&frameID); err != nil {
				_ = rows.Close()
				return err
			}
			frameIDs = append(frameIDs, frameID)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, frameID := range frameIDs {
			child, found, err := scanKernelSupervisedChild(tx.QueryRowContext(ctx, `
				SELECT frame_id,parent_frame_id,root_frame_id,project_id,owner_user_id,tool_use_id,
					agent_name,delegate_name,task,context_summary,model,output_schema,status,
					output_data,error,dispatched,started_at,completed_at
				FROM kernel_child_supervision WHERE frame_id=? AND parent_frame_id=? AND owner_user_id=?`,
				frameID, strings.TrimSpace(parentFrameID), strings.TrimSpace(ownerUserID)))
			if err != nil || !found {
				if err != nil {
					return err
				}
				return errors.New("undelivered kernel child landing is unavailable")
			}
			var generation int64
			_ = tx.QueryRowContext(ctx, `SELECT consumed_generation FROM kernel_child_message_clock WHERE frame_id=?`, frameID).Scan(&generation)
			completedAt := s.now().UTC()
			if child.CompletedAt != nil {
				completedAt = child.CompletedAt.UTC()
			}
			if err := createKernelChildCompletionNotificationTx(ctx, tx, child, generation, child.Status, child.Output, child.Error, completedAt); err != nil {
				return err
			}
			flushed++
		}
		return nil
	})
	return flushed, err
}

func (s *Store) SendKernelSupervisionMessage(ctx context.Context, sourceFrameID, targetFrameID, ownerUserID, message, kind string) (KernelSupervisedChild, string, error) {
	queued, err := s.QueueKernelSupervisionMessage(ctx, sourceFrameID, targetFrameID, ownerUserID, message, kind)
	if err != nil {
		return KernelSupervisedChild{}, "", err
	}
	return queued.Child, queued.Relation, nil
}

func (s *Store) ListKernelChildArtifacts(ctx context.Context, frameID, ownerUserID string) ([]map[string]any, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, errors.New("workspace store is closed")
	}
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT p.user_id FROM frames f JOIN projects p ON p.id=f.project_id WHERE f.id=?`, frameID).Scan(&owner); err != nil || owner != ownerUserID {
		return nil, 0, errors.New("delegated child is unavailable")
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE subtree(frame_id) AS (
			SELECT ?
			UNION ALL
			SELECT frame.id FROM frames frame JOIN subtree ON frame.parent_frame_id=subtree.frame_id
		)
		SELECT artifact.id,version.id,artifact.name,
			COALESCE(NULLIF(provenance.content_type,''),artifact.kind),version.size_bytes,version.created_at
		FROM artifact_versions version
		JOIN artifacts artifact ON artifact.id=version.artifact_id
		JOIN artifact_version_provenance provenance ON provenance.version_id=version.id
		JOIN subtree ON subtree.frame_id=provenance.frame_id
		ORDER BY version.created_at ASC`, frameID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	type artifactRow struct {
		artifactID, versionID, filename, contentType string
		sizeBytes                                    int64
		createdAt                                    time.Time
	}
	all := []artifactRow{}
	for rows.Next() {
		var item artifactRow
		if err := rows.Scan(&item.artifactID, &item.versionID, &item.filename, &item.contentType, &item.sizeBytes, &item.createdAt); err != nil {
			return nil, 0, err
		}
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	keep := make([]bool, len(all))
	perArtifact := map[string]int{}
	for index := len(all) - 1; index >= 0; index-- {
		if perArtifact[all[index].artifactID] >= 2 {
			continue
		}
		perArtifact[all[index].artifactID]++
		keep[index] = true
	}
	selected := make([]artifactRow, 0, len(all))
	for index, item := range all {
		if keep[index] {
			selected = append(selected, item)
		}
	}
	if len(selected) > 50 {
		selected = selected[len(selected)-50:]
	}
	items := make([]map[string]any, 0, len(selected))
	for _, item := range selected {
		items = append(items, map[string]any{
			"artifact_id": item.artifactID, "version_id": item.versionID,
			"filename": item.filename, "content_type": item.contentType, "size_bytes": item.sizeBytes,
		})
	}
	return items, len(all) - len(items), nil
}

type kernelSupervisionScanner interface{ Scan(...any) error }

func scanKernelSupervisedChild(scanner kernelSupervisionScanner) (KernelSupervisedChild, bool, error) {
	var child KernelSupervisedChild
	var rawSchema, rawOutput string
	var dispatched int
	var completed sql.NullTime
	err := scanner.Scan(&child.FrameID, &child.ParentFrameID, &child.RootFrameID, &child.ProjectID,
		&child.OwnerUserID, &child.ToolUseID, &child.AgentName, &child.Name, &child.Task,
		&child.ContextSummary, &child.Model, &rawSchema, &child.Status, &rawOutput, &child.Error,
		&dispatched, &child.StartedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelSupervisedChild{}, false, nil
	}
	if err != nil {
		return KernelSupervisedChild{}, false, fmt.Errorf("scan delegated child: %w", err)
	}
	child.Dispatched = dispatched != 0
	if completed.Valid {
		child.CompletedAt = &completed.Time
	}
	if strings.TrimSpace(rawSchema) != "" && rawSchema != "null" {
		if err := json.Unmarshal([]byte(rawSchema), &child.OutputSchema); err != nil {
			return KernelSupervisedChild{}, false, fmt.Errorf("decode child output schema: %w", err)
		}
	}
	if strings.TrimSpace(rawOutput) != "" && rawOutput != "null" {
		if err := json.Unmarshal([]byte(rawOutput), &child.Output); err != nil {
			return KernelSupervisedChild{}, false, fmt.Errorf("decode child output: %w", err)
		}
	}
	return child, true, nil
}

func isKernelChildTerminalStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "orphaned"
}

func kernelCompletionBullets(output map[string]any, failure string) []any {
	if strings.TrimSpace(failure) != "" {
		return []any{"Child run failed", failure}
	}
	if response, _ := output["response"].(string); strings.TrimSpace(response) != "" {
		return []any{"Child run completed", truncateKernelSupervisionText(response, 240)}
	}
	if output["structured_output"] != nil {
		return []any{"Child run completed", "Structured output was submitted"}
	}
	return []any{"Child run completed", "No prose response was recorded"}
}

func truncateKernelSupervisionText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "..."
}
