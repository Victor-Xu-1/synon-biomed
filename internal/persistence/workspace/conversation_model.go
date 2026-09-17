package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type CompatibilityConversationModelResult struct {
	RootFrameID string
	Model       string
	Revision    int64
}

// SetCompatibilityConversationModel atomically changes only the model
// override owned by one conversation. It deliberately does not mutate frame
// status, runner leases, transcript state, or the user's global default.
func (s *Store) SetCompatibilityConversationModel(frameID, model string) (CompatibilityConversationModelResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityConversationModelResult{}, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	model = strings.TrimSpace(model)
	if frameID == "" || model == "" {
		return CompatibilityConversationModelResult{}, errors.New("frame id and conversation model are required")
	}
	patch, err := json.Marshal(map[string]any{
		"web_assistant": map[string]any{
			"conversation_overrides": map[string]any{"model": model},
		},
	})
	if err != nil {
		return CompatibilityConversationModelResult{}, fmt.Errorf("encode conversation model patch: %w", err)
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilityConversationModelResult{}, fmt.Errorf("begin conversation model transaction: %w", err)
	}
	defer tx.Rollback()

	var rootFrameID string
	if err := tx.QueryRowContext(ctx, `SELECT root_frame_id FROM frames WHERE id = ?`, frameID).Scan(&rootFrameID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompatibilityConversationModelResult{}, fmt.Errorf("frame %q does not exist", frameID)
		}
		return CompatibilityConversationModelResult{}, fmt.Errorf("resolve conversation model root: %w", err)
	}
	var rootParent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT parent_frame_id FROM frames WHERE id = ?`, rootFrameID).Scan(&rootParent); err != nil {
		return CompatibilityConversationModelResult{}, fmt.Errorf("load conversation model root: %w", err)
	}
	if rootParent.Valid {
		return CompatibilityConversationModelResult{}, fmt.Errorf("frame %q has invalid root %q", frameID, rootFrameID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, context_data)
		VALUES (?, '{}')
		ON CONFLICT(frame_id) DO NOTHING`, rootFrameID); err != nil {
		return CompatibilityConversationModelResult{}, fmt.Errorf("initialize conversation model metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE frame_runtime_metadata
		SET context_data = json_set(
			json_patch(
				CASE
					WHEN json_valid(COALESCE(context_data, '{}')) THEN COALESCE(context_data, '{}')
					ELSE '{}'
				END,
				json(?)
			),
			'$.web_assistant.conversation_overrides.model_revision',
			COALESCE(CAST(json_extract(
				CASE
					WHEN json_valid(COALESCE(context_data, '{}')) THEN COALESCE(context_data, '{}')
					ELSE '{}'
				END,
				'$.web_assistant.conversation_overrides.model_revision'
			) AS INTEGER), 0) + 1
		)
		WHERE frame_id = ?`, string(patch), rootFrameID); err != nil {
		return CompatibilityConversationModelResult{}, fmt.Errorf("patch conversation model metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET updated_at = ? WHERE id = ?`, s.now().UTC(), rootFrameID); err != nil {
		return CompatibilityConversationModelResult{}, fmt.Errorf("advance conversation model frame revision: %w", err)
	}
	var observed string
	var revision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(json_extract(context_data, '$.web_assistant.conversation_overrides.model'), ''),
			COALESCE(CAST(json_extract(context_data, '$.web_assistant.conversation_overrides.model_revision') AS INTEGER), 0)
		FROM frame_runtime_metadata WHERE frame_id = ?`, rootFrameID).Scan(&observed, &revision); err != nil {
		return CompatibilityConversationModelResult{}, fmt.Errorf("read patched conversation model: %w", err)
	}
	if strings.TrimSpace(observed) != model || revision <= 0 {
		return CompatibilityConversationModelResult{}, errors.New("conversation model update was not observed")
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityConversationModelResult{}, fmt.Errorf("commit conversation model transaction: %w", err)
	}
	return CompatibilityConversationModelResult{RootFrameID: rootFrameID, Model: model, Revision: revision}, nil
}
