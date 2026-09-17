package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// clearFramePendingInputsForCancellation removes interaction cards in the
// same transaction that makes a Frame terminal. Leaving them behind projects
// a cancelled task as waiting for user input and permits a stale approval UI
// to outlive the tool authority it was meant to resolve.
func clearFramePendingInputsForCancellation(
	ctx context.Context,
	tx workspaceTransaction,
	frameID string,
) error {
	var raw string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(context_data,'{}') FROM frame_runtime_metadata WHERE frame_id=?`, frameID,
	).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load pending inputs for frame cancellation: %w", err)
	}
	contextData := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &contextData); err != nil {
		return errors.New("frame cancellation metadata is invalid")
	}
	if _, found := contextData["_pending_input_requests"]; !found {
		return nil
	}
	delete(contextData, "_pending_input_requests")
	updated, err := json.Marshal(contextData)
	if err != nil {
		return fmt.Errorf("encode cancelled frame metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE frame_runtime_metadata SET context_data=? WHERE frame_id=?`, string(updated), frameID,
	); err != nil {
		return fmt.Errorf("clear cancelled frame pending inputs: %w", err)
	}
	return nil
}
