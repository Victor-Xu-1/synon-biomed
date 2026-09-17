package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// GetCompatibilityBranchMessages reads one archived branch or the current
// active branch from the same transaction snapshot as its branch ledger.
func (s *Store) GetCompatibilityBranchMessages(frameID, branchID string) ([]map[string]any, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	branchID = strings.TrimSpace(branchID)
	if frameID == "" || !validCompatibilityBranchID(branchID) {
		return nil, false, errors.New("valid frame id and branch id are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, fmt.Errorf("begin branch message read: %w", err)
	}
	defer tx.Rollback()
	contextData, err := compatibilityRootContext(ctx, tx, frameID)
	if err != nil {
		return nil, false, err
	}
	_, branches, activeBranchID := compatibilityBranchLedger(contextData)
	branchRaw, found := branches[branchID]
	if !found {
		return []map[string]any{}, false, nil
	}
	var raw string
	err = tx.QueryRowContext(ctx, `
		SELECT payload FROM frame_branch_archives
		WHERE frame_id = ? AND branch_id = ?`, frameID, branchID).Scan(&raw)
	if err == nil {
		messages, decodeErr := decodeCompatibilityBranchMessages(raw)
		if decodeErr != nil {
			return nil, false, decodeErr
		}
		return messages, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("load branch archive: %w", err)
	}
	if branchID == activeBranchID {
		messages, loadErr := compatibilityRootMessages(ctx, tx, frameID)
		if loadErr != nil {
			return nil, false, loadErr
		}
		return messages, true, nil
	}
	branch, _ := branchRaw.(map[string]any)
	if inline, ok := branch["messages"].([]any); ok {
		messages := make([]map[string]any, 0, len(inline))
		for _, value := range inline {
			message, ok := value.(map[string]any)
			if !ok {
				return nil, false, errors.New("branch message archive contains a non-object message")
			}
			messages = append(messages, message)
		}
		return messages, true, nil
	}
	return []map[string]any{}, true, nil
}

func decodeCompatibilityBranchMessages(raw string) ([]map[string]any, error) {
	if len(raw) > maxCompactionMessagesBytes {
		return nil, errors.New("branch message archive exceeds the configured size limit")
	}
	messages := []map[string]any{}
	if err := json.Unmarshal([]byte(raw), &messages); err == nil && messages != nil {
		return messages, nil
	}
	var wrapper struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil || wrapper.Messages == nil {
		return nil, errors.New("branch message archive is not valid JSON")
	}
	return wrapper.Messages, nil
}
