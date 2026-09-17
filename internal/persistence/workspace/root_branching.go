package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const maxCompatibilityForkContentBytes = 1 << 20

type CompatibilityRootForkInput struct {
	RootFrameID    string
	MessageIndex   int
	EditedContent  string
	SourceBranchID string
	AgentName      string
}

type CompatibilityAskUserForkResponse struct {
	Action     string
	Answers    map[string]string
	RawAnswers json.RawMessage
	Message    string
}

type CompatibilityRootAnswerForkInput struct {
	RootFrameID    string
	ToolUseID      string
	Response       CompatibilityAskUserForkResponse
	SourceBranchID string
	AgentName      string
}

type CompatibilityRootForkResult struct {
	RootFrameID string
	BranchID    string
	ProjectID   string
	Messages    []map[string]any
	Event       FrameEvent
}

type CompatibilityRootBranchActivationResult struct {
	RootFrameID string
	BranchID    string
	ProjectID   string
	Changed     bool
	Event       FrameEvent
}

type compatibilityRootBranchRequest struct {
	RootFrameID    string
	SourceBranchID string
	AgentName      string
}

type compatibilityRootBranchMutation func(
	messages []map[string]any,
	contextData map[string]any,
) (updatedMessages []map[string]any, forkPoint int, err error)

func (s *Store) ForkCompatibilityRoot(input CompatibilityRootForkInput) (CompatibilityRootForkResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityRootForkResult{}, errors.New("workspace store is closed")
	}
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.EditedContent = strings.TrimSpace(input.EditedContent)
	input.SourceBranchID = strings.TrimSpace(input.SourceBranchID)
	input.AgentName = strings.TrimSpace(input.AgentName)
	if input.RootFrameID == "" {
		return CompatibilityRootForkResult{}, errors.New("root frame id is required")
	}
	if input.MessageIndex < 0 {
		return CompatibilityRootForkResult{}, fmt.Errorf("invalid message index: %d", input.MessageIndex)
	}
	if input.EditedContent == "" {
		return CompatibilityRootForkResult{}, errors.New("edited_content cannot be empty")
	}
	if len(input.EditedContent) > maxCompatibilityForkContentBytes {
		return CompatibilityRootForkResult{}, errors.New("edited_content exceeds 1MB limit")
	}
	if input.SourceBranchID != "" && !validCompatibilityBranchID(input.SourceBranchID) {
		return CompatibilityRootForkResult{}, fmt.Errorf("source branch %s not found", input.SourceBranchID)
	}
	return s.mutateCompatibilityRootBranch(compatibilityRootBranchRequest{
		RootFrameID: input.RootFrameID, SourceBranchID: input.SourceBranchID, AgentName: input.AgentName,
	}, func(messages []map[string]any, contextData map[string]any) ([]map[string]any, int, error) {
		if input.MessageIndex >= len(messages) {
			return nil, 0, fmt.Errorf("invalid message index: %d", input.MessageIndex)
		}
		clearCompatibilityForkTransientContext(contextData)
		edited := map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "text", "text": input.EditedContent}},
			"_uuid": uuid.NewString(),
		}
		updated := cloneCompatibilityMessages(messages[:input.MessageIndex])
		return append(updated, edited), input.MessageIndex, nil
	})
}

func (s *Store) ForkCompatibilityRootAtAnswer(input CompatibilityRootAnswerForkInput) (CompatibilityRootForkResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityRootForkResult{}, errors.New("workspace store is closed")
	}
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.ToolUseID = strings.TrimSpace(input.ToolUseID)
	input.SourceBranchID = strings.TrimSpace(input.SourceBranchID)
	input.AgentName = strings.TrimSpace(input.AgentName)
	input.Response.Action = strings.TrimSpace(input.Response.Action)
	if input.RootFrameID == "" || input.ToolUseID == "" {
		return CompatibilityRootForkResult{}, errors.New("root frame id and tool use id are required")
	}
	if input.SourceBranchID != "" && !validCompatibilityBranchID(input.SourceBranchID) {
		return CompatibilityRootForkResult{}, fmt.Errorf("source branch %s not found", input.SourceBranchID)
	}
	switch input.Response.Action {
	case "answer", "decide_for_me", "discuss", "cancel":
	default:
		return CompatibilityRootForkResult{}, fmt.Errorf("unsupported ask_user action %q", input.Response.Action)
	}
	return s.mutateCompatibilityRootBranch(compatibilityRootBranchRequest{
		RootFrameID: input.RootFrameID, SourceBranchID: input.SourceBranchID, AgentName: input.AgentName,
	}, func(messages []map[string]any, contextData map[string]any) ([]map[string]any, int, error) {
		location, err := locateCompatibilityAskUser(messages, input.ToolUseID)
		if err != nil {
			return nil, 0, err
		}
		content, err := compatibilityAskUserResponseContent(location.Question, input.Response)
		if err != nil {
			return nil, 0, err
		}
		updated := cloneCompatibilityMessages(messages[:location.ResultMessageIndex])
		resultMessage := cloneCompatibilityMessages(messages[location.ResultMessageIndex : location.ResultMessageIndex+1])[0]
		blocks, _ := resultMessage["content"].([]any)
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "tool_result" && block["tool_use_id"] == input.ToolUseID {
				block["content"] = content
				delete(block, "is_error")
				break
			}
		}
		resultMessage["_uuid"] = uuid.NewString()
		delete(resultMessage, "_intent_id")
		updated = append(updated, resultMessage)
		pruneCompatibilityAnswerForkContext(contextData, updated)
		return updated, location.ResultMessageIndex, nil
	})
}

// ActivateCompatibilityRootBranch replaces the root frame transcript with a
// previously persisted branch. The active transcript is archived first, so a
// later switch is lossless and a following message continues from the branch
// the user actually selected in the Web client.
func (s *Store) ActivateCompatibilityRootBranch(rootFrameID, branchID string) (CompatibilityRootBranchActivationResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityRootBranchActivationResult{}, errors.New("workspace store is closed")
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	branchID = strings.TrimSpace(branchID)
	if rootFrameID == "" {
		return CompatibilityRootBranchActivationResult{}, errors.New("root frame id is required")
	}
	if !validCompatibilityBranchID(branchID) {
		return CompatibilityRootBranchActivationResult{}, fmt.Errorf("branch %s not found", branchID)
	}

	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilityRootBranchActivationResult{}, fmt.Errorf("begin branch activation transaction: %w", err)
	}
	defer tx.Rollback()

	var projectID, status string
	var parentID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT project_id, parent_frame_id, status FROM frames WHERE id = ?`, rootFrameID).Scan(&projectID, &parentID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompatibilityRootBranchActivationResult{}, fmt.Errorf("frame %s not found", rootFrameID)
		}
		return CompatibilityRootBranchActivationResult{}, fmt.Errorf("load branch root frame: %w", err)
	}
	if parentID.Valid {
		return CompatibilityRootBranchActivationResult{}, errors.New("can only activate branches on root frames")
	}
	if status != "completed" {
		return CompatibilityRootBranchActivationResult{}, fmt.Errorf("cannot activate a branch while frame status is %s", status)
	}

	contextData, err := compatibilityRootContext(ctx, tx, rootFrameID)
	if err != nil {
		return CompatibilityRootBranchActivationResult{}, err
	}
	branchMeta, branches, activeBranchID := compatibilityBranchLedger(contextData)
	if _, found := branches[branchID]; !found {
		return CompatibilityRootBranchActivationResult{}, fmt.Errorf("branch %s not found", branchID)
	}
	if branchID == activeBranchID {
		return CompatibilityRootBranchActivationResult{
			RootFrameID: rootFrameID, BranchID: branchID, ProjectID: projectID,
		}, nil
	}

	currentMessages, err := compatibilityRootMessages(ctx, tx, rootFrameID)
	if err != nil {
		return CompatibilityRootBranchActivationResult{}, err
	}
	if activeBranchID != "" {
		if err := putCompatibilityBranchArchive(ctx, tx, rootFrameID, activeBranchID, currentMessages, s.now().UTC()); err != nil {
			return CompatibilityRootBranchActivationResult{}, err
		}
	}
	selectedMessages, err := getCompatibilityBranchArchive(ctx, tx, rootFrameID, branchID)
	if err != nil {
		return CompatibilityRootBranchActivationResult{}, err
	}
	now := s.now().UTC()
	clearCompatibilityForkTransientContext(contextData)
	branchMeta["active_branch_id"] = branchID
	branchMeta["branches"] = branches
	contextData["_branch_meta"] = branchMeta
	if err := replaceCompatibilityRootMessages(ctx, tx, rootFrameID, selectedMessages, now); err != nil {
		return CompatibilityRootBranchActivationResult{}, err
	}
	rawContext, err := json.Marshal(contextData)
	if err != nil {
		return CompatibilityRootBranchActivationResult{}, fmt.Errorf("encode activated branch context: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, context_data, output_data, completed_at)
		VALUES (?, ?, '{}', ?)
		ON CONFLICT(frame_id) DO UPDATE SET
			context_data = excluded.context_data,
			output_data = '{}',
			completed_at = excluded.completed_at`, rootFrameID, string(rawContext), now); err != nil {
		return CompatibilityRootBranchActivationResult{}, fmt.Errorf("persist activated branch context: %w", err)
	}
	event, err := appendCompatibilityBranchEvent(ctx, tx, rootFrameID, "branch_activated", map[string]any{
		"rootFrameId": rootFrameID, "branchId": branchID, "previousBranchId": activeBranchID,
	}, now.Add(time.Duration(len(selectedMessages))*time.Nanosecond))
	if err != nil {
		return CompatibilityRootBranchActivationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityRootBranchActivationResult{}, fmt.Errorf("commit branch activation: %w", err)
	}
	return CompatibilityRootBranchActivationResult{
		RootFrameID: rootFrameID, BranchID: branchID, ProjectID: projectID, Changed: true, Event: event,
	}, nil
}

type compatibilityAskUserLocation struct {
	Question           string
	ResultMessageIndex int
}

func locateCompatibilityAskUser(messages []map[string]any, toolUseID string) (compatibilityAskUserLocation, error) {
	question := ""
	toolFound := false
	for _, message := range messages {
		blocks, _ := message["content"].([]any)
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			if block["type"] != "tool_use" || block["id"] != toolUseID {
				continue
			}
			toolFound = true
			name, _ := block["name"].(string)
			if _, ok := transcriptstore.CanonicalAskUserToolNameV1(name); !ok {
				return compatibilityAskUserLocation{}, errors.New("Can only edit ask_user answers")
			}
			input, _ := block["input"].(map[string]any)
			question = strings.TrimSpace(compatibilityStringValue(input["question"]))
			if question == "" {
				return compatibilityAskUserLocation{}, errors.New("ask_user tool_use missing question")
			}
		}
	}
	if !toolFound {
		return compatibilityAskUserLocation{}, fmt.Errorf("tool_use_id %s not found in messages", toolUseID)
	}
	for index, message := range messages {
		blocks, _ := message["content"].([]any)
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "tool_result" && block["tool_use_id"] == toolUseID {
				return compatibilityAskUserLocation{Question: question, ResultMessageIndex: index}, nil
			}
		}
	}
	return compatibilityAskUserLocation{}, fmt.Errorf("tool_use_id %s not found in messages", toolUseID)
}

func compatibilityAskUserResponseContent(question string, response CompatibilityAskUserForkResponse) (string, error) {
	switch response.Action {
	case "decide_for_me":
		return "User delegated this choice. Decide only within the unchanged canonical task: preserve every explicit priority and requirement, do not turn a suggested preference into a new hard constraint, and choose the option that best satisfies the canonical objective.", nil
	case "discuss":
		message := strings.TrimSpace(response.Message)
		if message == "" {
			return "", errors.New("Message is required for 'discuss' action")
		}
		return "The user wants to discuss these questions further. Their message: " + message + ". Respond to their input, then use ask_user again if you still need answers.", nil
	case "cancel":
		return "User cancelled the question. Continue without an answer — use your best judgment or skip this step.", nil
	case "answer":
		answers := response.Answers
		rawAnswers := []byte(nil)
		if len(response.RawAnswers) > 0 {
			if err := json.Unmarshal(response.RawAnswers, &answers); err != nil {
				return "", fmt.Errorf("invalid ask_user answers: %w", err)
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, response.RawAnswers); err != nil {
				return "", fmt.Errorf("compact ask_user answers: %w", err)
			}
			rawAnswers = compact.Bytes()
		} else {
			var err error
			rawAnswers, err = json.Marshal(answers)
			if err != nil {
				return "", fmt.Errorf("encode ask_user answers: %w", err)
			}
		}
		if _, found := answers[question]; !found {
			return "", fmt.Errorf("Missing answers for: %s", question)
		}
		return `{"status":"answered","answers":` + string(rawAnswers) + `}`, nil
	default:
		return "", fmt.Errorf("unsupported ask_user action %q", response.Action)
	}
}

func (s *Store) mutateCompatibilityRootBranch(input compatibilityRootBranchRequest, mutate compatibilityRootBranchMutation) (CompatibilityRootForkResult, error) {
	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilityRootForkResult{}, fmt.Errorf("begin root branch transaction: %w", err)
	}
	defer tx.Rollback()
	var projectID, status string
	var parentID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT project_id, parent_frame_id, status FROM frames WHERE id = ?`, input.RootFrameID).Scan(&projectID, &parentID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompatibilityRootForkResult{}, fmt.Errorf("frame %s not found", input.RootFrameID)
		}
		return CompatibilityRootForkResult{}, fmt.Errorf("load root branch frame: %w", err)
	}
	if parentID.Valid {
		return CompatibilityRootForkResult{}, errors.New("can only fork root frames")
	}
	if status == "processing" || status == "awaiting_user_response" {
		return CompatibilityRootForkResult{}, errors.New("cannot fork while processing. Cancel first.")
	}
	contextData, err := compatibilityRootContext(ctx, tx, input.RootFrameID)
	if err != nil {
		return CompatibilityRootForkResult{}, err
	}
	messages, err := compatibilityRootMessages(ctx, tx, input.RootFrameID)
	if err != nil {
		return CompatibilityRootForkResult{}, err
	}
	branchMeta, branches, activeBranchID := compatibilityBranchLedger(contextData)
	if input.SourceBranchID != "" && input.SourceBranchID != activeBranchID {
		if _, found := branches[input.SourceBranchID]; !found {
			return CompatibilityRootForkResult{}, fmt.Errorf("source branch %s not found", input.SourceBranchID)
		}
		if activeBranchID != "" {
			if err := putCompatibilityBranchArchive(ctx, tx, input.RootFrameID, activeBranchID, messages, s.now().UTC()); err != nil {
				return CompatibilityRootForkResult{}, err
			}
		}
		messages, err = getCompatibilityBranchArchive(ctx, tx, input.RootFrameID, input.SourceBranchID)
		if err != nil {
			return CompatibilityRootForkResult{}, err
		}
		activeBranchID = input.SourceBranchID
	}
	updatedMessages, forkPoint, err := mutate(messages, contextData)
	if err != nil {
		return CompatibilityRootForkResult{}, err
	}
	now := s.now().UTC()
	if activeBranchID == "" {
		activeBranchID, err = allocateCompatibilityBranchID(ctx, tx, input.RootFrameID, branches)
		if err != nil {
			return CompatibilityRootForkResult{}, err
		}
		branches[activeBranchID] = compatibilityBranchMetadata(nil, 0, now)
	}
	if err := putCompatibilityBranchArchive(ctx, tx, input.RootFrameID, activeBranchID, messages, now); err != nil {
		return CompatibilityRootForkResult{}, err
	}
	branchID, err := allocateCompatibilityBranchID(ctx, tx, input.RootFrameID, branches)
	if err != nil {
		return CompatibilityRootForkResult{}, err
	}
	branches[branchID] = compatibilityBranchMetadata(activeBranchID, forkPoint, now)
	branchMeta["active_branch_id"] = branchID
	branchMeta["branches"] = branches
	contextData["_branch_meta"] = branchMeta
	if err := replaceCompatibilityRootMessages(ctx, tx, input.RootFrameID, updatedMessages, now); err != nil {
		return CompatibilityRootForkResult{}, err
	}
	rawContext, err := json.Marshal(contextData)
	if err != nil {
		return CompatibilityRootForkResult{}, fmt.Errorf("encode root branch context: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, context_data, output_data, completed_at)
		VALUES (?, ?, '{}', NULL)
		ON CONFLICT(frame_id) DO UPDATE SET
			context_data = excluded.context_data,
			output_data = '{}',
			completed_at = NULL`, input.RootFrameID, string(rawContext)); err != nil {
		return CompatibilityRootForkResult{}, fmt.Errorf("persist root branch context: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE frames SET status = 'processing',
			agent_name = CASE WHEN ? = '' THEN agent_name ELSE ? END,
			updated_at = ? WHERE id = ?`, input.AgentName, input.AgentName, now, input.RootFrameID); err != nil {
		return CompatibilityRootForkResult{}, fmt.Errorf("activate root branch: %w", err)
	}
	event, err := appendCompatibilityBranchEvent(ctx, tx, input.RootFrameID, "branch_created", map[string]any{
		"rootFrameId": input.RootFrameID, "branchId": branchID,
		"sourceBranchId": activeBranchID, "messageIndex": forkPoint,
	}, now.Add(time.Duration(len(updatedMessages))*time.Nanosecond))
	if err != nil {
		return CompatibilityRootForkResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityRootForkResult{}, fmt.Errorf("commit root branch transaction: %w", err)
	}
	return CompatibilityRootForkResult{
		RootFrameID: input.RootFrameID, BranchID: branchID, ProjectID: projectID,
		Messages: updatedMessages, Event: event,
	}, nil
}

func compatibilityRootContext(ctx context.Context, tx *sql.Tx, frameID string) (map[string]any, error) {
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT context_data FROM frame_runtime_metadata WHERE frame_id = ?`, frameID).Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("load root branch context: %w", err)
	}
	contextData := map[string]any{}
	if raw.Valid && strings.TrimSpace(raw.String) != "" {
		if err := json.Unmarshal([]byte(raw.String), &contextData); err != nil {
			return nil, fmt.Errorf("decode root branch context: %w", err)
		}
	}
	if contextData == nil {
		contextData = map[string]any{}
	}
	return contextData, nil
}

func compatibilityRootMessages(ctx context.Context, tx workspaceTransaction, frameID string) ([]map[string]any, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM frame_events WHERE frame_id = ? AND `+compatibilityMessagePredicate+` ORDER BY sequence`, frameID)
	if err != nil {
		return nil, fmt.Errorf("load root branch messages: %w", err)
	}
	defer rows.Close()
	messages := make([]map[string]any, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan root branch message: %w", err)
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(raw), &message); err != nil {
			return nil, fmt.Errorf("decode root branch message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate root branch messages: %w", err)
	}
	return messages, nil
}

func compatibilityBranchLedger(contextData map[string]any) (map[string]any, map[string]any, string) {
	meta, _ := contextData["_branch_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	branches, _ := meta["branches"].(map[string]any)
	if branches == nil {
		branches = map[string]any{}
	}
	active, _ := meta["active_branch_id"].(string)
	return meta, branches, strings.TrimSpace(active)
}

func compatibilityBranchMetadata(parent any, forkPoint int, now time.Time) map[string]any {
	return map[string]any{
		"parent_id": parent, "fork_point": forkPoint,
		"created_at": now.Format(time.RFC3339Nano), "updated_at": now.Format(time.RFC3339Nano),
		"messages": nil, "child_frame_ids": nil, "tool_id_to_frame_id": nil,
	}
}

func allocateCompatibilityBranchID(ctx context.Context, tx *sql.Tx, frameID string, branches map[string]any) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var random [4]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("generate branch id: %w", err)
		}
		candidate := "br_" + hex.EncodeToString(random[:])
		if _, found := branches[candidate]; found {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM frame_branch_archives WHERE frame_id = ? AND branch_id = ?`, frameID, candidate).Scan(&count); err != nil {
			return "", fmt.Errorf("check branch id: %w", err)
		}
		if count == 0 {
			return candidate, nil
		}
	}
	return "", errors.New("allocate unique branch id")
}

func validCompatibilityBranchID(value string) bool {
	if len(value) != 11 || !strings.HasPrefix(value, "br_") {
		return false
	}
	decoded, err := hex.DecodeString(value[3:])
	return err == nil && len(decoded) == 4
}

func putCompatibilityBranchArchive(ctx context.Context, tx *sql.Tx, frameID, branchID string, messages []map[string]any, now time.Time) error {
	raw, err := json.Marshal(messages)
	if err != nil {
		return fmt.Errorf("encode branch archive: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_branch_archives (frame_id, branch_id, payload, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(frame_id, branch_id) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at`,
		frameID, branchID, string(raw), now); err != nil {
		return fmt.Errorf("persist branch archive: %w", err)
	}
	return nil
}

func getCompatibilityBranchArchive(ctx context.Context, tx *sql.Tx, frameID, branchID string) ([]map[string]any, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM frame_branch_archives WHERE frame_id = ? AND branch_id = ?`, frameID, branchID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("source branch %s not found", branchID)
		}
		return nil, fmt.Errorf("load branch archive: %w", err)
	}
	var messages []map[string]any
	if err := json.Unmarshal([]byte(raw), &messages); err == nil && messages != nil {
		return messages, nil
	}
	var wrapper struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil || wrapper.Messages == nil {
		return nil, fmt.Errorf("decode source branch %s archive", branchID)
	}
	return wrapper.Messages, nil
}

func replaceCompatibilityRootMessages(ctx context.Context, tx workspaceTransaction, frameID string, messages []map[string]any, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM frame_events WHERE frame_id = ? AND `+compatibilityMessagePredicate, frameID); err != nil {
		return fmt.Errorf("clear active branch messages: %w", err)
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM frame_events WHERE frame_id = ?`, frameID).Scan(&sequence); err != nil {
		return fmt.Errorf("allocate active branch message sequence: %w", err)
	}
	for index, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("encode active branch message: %w", err)
		}
		sequence++
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, uuid.NewString(), frameID, sequence,
			compatibilityMessageEventType(message), string(raw), now.Add(time.Duration(index)*time.Nanosecond)); err != nil {
			return fmt.Errorf("insert active branch message: %w", err)
		}
	}
	return nil
}

func compatibilityMessageEventType(message map[string]any) string {
	role, _ := message["role"].(string)
	switch role {
	case "assistant":
		return "assistant_message"
	case "system":
		return "system_message"
	default:
		return "user_message"
	}
}

func appendCompatibilityBranchEvent(ctx context.Context, tx workspaceTransaction, frameID, eventType string, payload map[string]any, now time.Time) (FrameEvent, error) {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?`, frameID).Scan(&sequence); err != nil {
		return FrameEvent{}, fmt.Errorf("allocate branch event sequence: %w", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("encode branch event: %w", err)
	}
	event := FrameEvent{ID: uuid.NewString(), FrameID: frameID, Sequence: sequence, Type: eventType, Payload: payload, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.FrameID, event.Sequence, event.Type, string(raw), event.CreatedAt); err != nil {
		return FrameEvent{}, fmt.Errorf("insert branch event: %w", err)
	}
	return event, nil
}

func cloneCompatibilityMessages(messages []map[string]any) []map[string]any {
	raw, _ := json.Marshal(messages)
	var cloned []map[string]any
	_ = json.Unmarshal(raw, &cloned)
	if cloned == nil {
		cloned = []map[string]any{}
	}
	return cloned
}

func clearCompatibilityForkTransientContext(contextData map[string]any) {
	delete(contextData, "_running_children")
	delete(contextData, "_tool_id_to_frame_id")
	delete(contextData, "_delegated_agents")
	clearCompatibilitySharedForkTransientContext(contextData)
}

func clearCompatibilitySharedForkTransientContext(contextData map[string]any) {
	for _, key := range []string{
		"_streaming_buffer",
		"_pending_user_message", "_seen_viewport_versions", "_pending_input_requests",
		"_ask_user_payload", "_pending_access_request", "_checkpoint_inflight_start",
		"_checkpoint_rewinds", "_n_subagent_spawns", "_n_reviewer_rounds",
	} {
		delete(contextData, key)
	}
}

func pruneCompatibilityAnswerForkContext(contextData map[string]any, messages []map[string]any) {
	toolIDs := map[string]bool{}
	for _, message := range messages {
		blocks, _ := message["content"].([]any)
		for _, rawBlock := range blocks {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "tool_use" {
				if id := strings.TrimSpace(compatibilityStringValue(block["id"])); id != "" {
					toolIDs[id] = true
				}
			}
		}
	}
	childIDs := map[string]bool{}
	if mapping, ok := contextData["_tool_id_to_frame_id"].(map[string]any); ok {
		kept := map[string]any{}
		for toolID, frameID := range mapping {
			if !toolIDs[toolID] {
				continue
			}
			kept[toolID] = frameID
			if id := strings.TrimSpace(compatibilityStringValue(frameID)); id != "" {
				childIDs[id] = true
			}
		}
		contextData["_tool_id_to_frame_id"] = kept
	}
	if running, ok := contextData["_running_children"].(map[string]any); ok {
		kept := map[string]any{}
		for frameID, state := range running {
			if childIDs[frameID] {
				kept[frameID] = state
			}
		}
		contextData["_running_children"] = kept
	}
	if delegated, ok := contextData["_delegated_agents"].([]any); ok {
		kept := make([]any, 0, len(delegated))
		for _, frameID := range delegated {
			if childIDs[strings.TrimSpace(compatibilityStringValue(frameID))] {
				kept = append(kept, frameID)
			}
		}
		contextData["_delegated_agents"] = kept
	}
	clearCompatibilitySharedForkTransientContext(contextData)
}

func compatibilityStringValue(value any) string {
	text, _ := value.(string)
	return text
}
