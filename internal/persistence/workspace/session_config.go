package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var compatibilitySessionConfigKeys = map[string]bool{
	"verifier_mode":                    true,
	"memory_mode":                      true,
	"auto_mode":                        true,
	"reviewer_model":                   true,
	"rc_context_ceiling":               true,
	"python_version":                   true,
	"kernel_idle_timeout":              true,
	"async_local_exec_wallclock_cap_s": true,
	"goal_text":                        true,
	"target_agent":                     true,
}

type CompatibilitySessionConfigResult struct {
	RootFrameID string
	Config      map[string]any
}

// PatchCompatibilitySessionConfig atomically merges v1.1 session knobs into
// the root Frame's _original_input object. The fixed key allowlist makes every
// generated SQLite JSON path trusted rather than request-controlled.
func (s *Store) PatchCompatibilitySessionConfig(frameID string, patch map[string]any) (CompatibilitySessionConfigResult, error) {
	if s == nil || s.db == nil {
		return CompatibilitySessionConfigResult{}, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" || len(patch) == 0 {
		return CompatibilitySessionConfigResult{}, errors.New("frame id and session config patch are required")
	}
	for key := range patch {
		if !compatibilitySessionConfigKeys[key] {
			return CompatibilitySessionConfigResult{}, fmt.Errorf("unsupported session config key %q", key)
		}
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("begin session config transaction: %w", err)
	}
	defer tx.Rollback()
	var rootFrameID string
	if err := tx.QueryRowContext(ctx, `SELECT root_frame_id FROM frames WHERE id = ?`, frameID).Scan(&rootFrameID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompatibilitySessionConfigResult{}, fmt.Errorf("frame %q does not exist", frameID)
		}
		return CompatibilitySessionConfigResult{}, fmt.Errorf("resolve session config root: %w", err)
	}
	var rootParent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT parent_frame_id FROM frames WHERE id = ?`, rootFrameID).Scan(&rootParent); err != nil {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("load session config root: %w", err)
	}
	if rootParent.Valid {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("frame %q has invalid root %q", frameID, rootFrameID)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id, context_data)
		VALUES (?, '{}')
		ON CONFLICT(frame_id) DO NOTHING`, rootFrameID); err != nil {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("initialize session config metadata: %w", err)
	}

	contextExpr := `CASE
		WHEN json_valid(COALESCE(context_data, '{}')) THEN COALESCE(context_data, '{}')
		ELSE '{}'
	END`
	contextExpr = `json_set(` + contextExpr + `, '$._original_input',
		json(CASE
			WHEN json_type(` + contextExpr + `, '$._original_input') = 'object'
				THEN json_extract(` + contextExpr + `, '$._original_input')
			ELSE '{}'
		END))`
	args := make([]any, 0, len(patch)+2)
	orderedKeys := []string{
		"verifier_mode", "memory_mode", "auto_mode", "reviewer_model",
		"rc_context_ceiling", "python_version", "kernel_idle_timeout", "goal_text",
		"async_local_exec_wallclock_cap_s", "target_agent",
	}
	for _, key := range orderedKeys {
		value, present := patch[key]
		if !present {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return CompatibilitySessionConfigResult{}, fmt.Errorf("encode session config %s: %w", key, err)
		}
		contextExpr = `json_set(` + contextExpr + `, '$._original_input.` + key + `', json(?))`
		args = append(args, string(raw))
	}
	args = append(args, rootFrameID)
	if _, err := tx.ExecContext(ctx, `UPDATE frame_runtime_metadata SET context_data = `+contextExpr+` WHERE frame_id = ?`, args...); err != nil {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("patch session config metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE frames SET updated_at = ? WHERE id = ?`, s.now().UTC(), rootFrameID); err != nil {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("advance session config frame revision: %w", err)
	}
	var rawContext string
	if err := tx.QueryRowContext(ctx, `SELECT context_data FROM frame_runtime_metadata WHERE frame_id = ?`, rootFrameID).Scan(&rawContext); err != nil {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("read patched session config: %w", err)
	}
	var storedContext map[string]any
	if err := json.Unmarshal([]byte(rawContext), &storedContext); err != nil {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("decode patched session config: %w", err)
	}
	storedConfig, ok := storedContext["_original_input"].(map[string]any)
	if !ok {
		return CompatibilitySessionConfigResult{}, errors.New("patched session config is not an object")
	}
	if err := tx.Commit(); err != nil {
		return CompatibilitySessionConfigResult{}, fmt.Errorf("commit session config transaction: %w", err)
	}
	s.signalKernelRetentionWake()
	return CompatibilitySessionConfigResult{RootFrameID: rootFrameID, Config: storedConfig}, nil
}
