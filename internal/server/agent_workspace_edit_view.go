package server

import (
	"context"
	"encoding/json"
	"unicode/utf8"
)

// A mutation receipt describes the state that actually exists, not only that
// a replacement succeeded. This is feedback, not a content-quality gate: it
// never changes or rejects a valid edit or initiates another model call.
func attachAgentWorkspaceEditView(ctx context.Context, authority *agentWorkspaceEditAuthority, receipt map[string]any) {
	path := stringValue(receipt["file_path"])
	receipt["read_with"] = map[string]any{"file_path": path, "human_description": "Reading edited file"}
	reader, _, found, err := authority.openCurrent()
	if err != nil || !found {
		receipt["file_view_status"] = "unavailable"
		return
	}
	defer func() {
		if reader.Close() != nil {
			receipt["file_view_close_error"] = true
		}
	}()
	info, err := reader.Stat()
	if err != nil {
		receipt["file_view_status"] = "unavailable"
		return
	}
	limit := min(int(runnerLargeToolResultInlineLimitBytes), agentWorkspaceReadSerializedLimit(ctx))
	budget := withAgentWorkspaceReadBudget(ctx, int64(limit))
	base, err := json.Marshal(receipt)
	if err != nil {
		receipt["file_view_status"] = "unavailable"
		return
	}
	// Reserve the enclosing receipt, key, and a possible close-error field.
	available := agentWorkspaceReadSerializedLimit(budget) - len(base) - len(`,"file_view":,"file_view_close_error":true`)
	if available <= 0 {
		receipt["file_view_status"] = "read_required"
		return
	}
	viewBudget := withAgentWorkspaceReadBudget(ctx, int64(limit-len(`,"file_view_close_error":true`)))
	// Read only enough bytes to fill this receipt, not the entire edited file.
	// A bounded byte prefix also works for machine-generated single lines.
	content, err := readAgentWorkspaceBounded(ctx, reader, int64(available))
	if err != nil {
		receipt["file_view_status"] = "read_required"
		return
	}
	end := len(content)
	if int64(end) < info.Size() {
		for end > 0 && !utf8.Valid(content[:end]) && len(content)-end < utf8.UTFMax {
			end--
		}
	}
	if !utf8.Valid(content[:end]) {
		receipt["file_view_status"] = "non_utf8"
		return
	}
	low, high, accepted := 0, end, 0
	for low <= high {
		length := low + (high-low)/2
		boundary := length
		for boundary > 0 && boundary < end && !utf8.RuneStart(content[boundary]) {
			boundary--
		}
		receipt["file_view"] = map[string]any{
			"content": string(content[:boundary]), "shown_bytes": boundary,
			"size_bytes": info.Size(), "truncated": int64(boundary) < info.Size(),
		}
		if agentWorkspaceReadResultFits(viewBudget, receipt) {
			accepted = boundary
			low = length + 1
		} else {
			high = length - 1
		}
	}
	if accepted == 0 && info.Size() > 0 {
		delete(receipt, "file_view")
		receipt["file_view_status"] = "read_required"
		return
	}
	receipt["file_view"] = map[string]any{
		"content": string(content[:accepted]), "shown_bytes": accepted,
		"size_bytes": info.Size(), "truncated": int64(accepted) < info.Size(),
	}
}
