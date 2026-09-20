package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

// This view version binds persisted line cursors. Change it if escaping,
// directory layout or wrapping width changes; never reuse old offsets blindly.
const runnerCorrectionReadViewFormat = "correction-json-display-lines-v1"
const runnerCorrectionReadLineBytes = 128

func runnerCorrectionReadInput(name string, input map[string]any) bool {
	return normalizeAgentToolName(name) == "readfile" && strings.TrimSpace(stringValue(input["recovery_condition_id"])) != ""
}

func runnerCorrectionReadCall(call agentruntime.ToolCall) bool {
	if normalizeAgentToolName(call.Name) != "readfile" {
		return false
	}
	var input map[string]any
	return json.Unmarshal(call.Arguments, &input) == nil && runnerCorrectionReadInput(call.Name, input)
}

func runnerCorrectionReadDescriptor(condition *transcriptstore.RunnerCorrectionCondition, coverage *runnerConditionReadCoverage) map[string]any {
	id := condition.ContentID()
	descriptor := map[string]any{
		"schema": condition.Schema, "content_id": id, "fingerprint": condition.Fingerprint(),
		"complete_in_context": false,
		"read_with": map[string]any{"recovery_condition_id": id, "offset": 1, "limit": 200,
			"human_description": "Reading the complete recovery condition"},
	}
	if coverage != nil && coverage.ContentID == id {
		descriptor["last_read"] = coverage
		if coverage.NextOffset > 0 {
			descriptor["read_with"] = map[string]any{"recovery_condition_id": id, "json_pointer": coverage.JSONPointer, "offset": coverage.NextOffset, "limit": 200, "human_description": "Continuing the stored recovery condition"}
		} else if coverage.JSONPointer == "" && coverage.ContiguousThrough == coverage.TotalLines {
			delete(descriptor, "read_with")
		}
	}
	return descriptor
}

// This is a read-only source adapter on read_file, not another recovery store.
// The condition was hydrated from the current fenced transcript. Knowing its
// hash never grants access to another task, owner, branch or historical input.
func (s *Server) readRunnerCorrectionCondition(ctx context.Context, identity *agentKernelContext, userID, projectID, id string, input map[string]any) (any, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil || run.CorrectionCondition == nil || identity == nil ||
		run.Transcript.Stream.OwnerID != userID || run.Transcript.Stream.ProjectID != projectID ||
		identity.access.UserID != userID || identity.access.Frame.ProjectID != projectID ||
		identity.access.Frame.ID != run.SessionID || run.Transcript.Stream.SessionID != run.SessionID ||
		id != run.CorrectionCondition.ContentID() {
		return nil, errors.New("recovery condition is unavailable for the current task authority")
	}
	if s == nil || s.transcriptStore == nil {
		return nil, errors.New("recovery condition transcript authority is unavailable")
	}
	stream, err := s.transcriptStore.ValidateLiveRunnerClaim(ctx, run.Transcript.Claim)
	if err != nil {
		return nil, err
	}
	if stream.InputRevision != run.Transcript.Claim.ClaimedInputRevision {
		return nil, transcriptstore.ErrClaimStale
	}
	// The existing per-run read cache is consulted only after live authority.
	// New windows advance diagnostic coverage; repeats do not create progress.
	cacheInput := normalizeAgentWorkspaceReadFileArguments(input)
	cached, seen := run.lookupReadReuse("read_file", cacheInput)
	if seen && agentWorkspaceReadResultFits(ctx, cached) {
		if err := context.Cause(ctx); err != nil {
			return nil, err
		}
		return cached, nil
	}
	raw, err := transcriptstore.EncodeRunnerCorrectionCondition(*run.CorrectionCondition)
	if err != nil {
		return nil, err
	}
	selected := raw
	pointer, _ := input["json_pointer"].(string)
	if pointer != "" {
		selected, err = selectAgentWorkspaceJSONValue(ctx, bytes.NewReader(raw), pointer)
		if err != nil {
			return nil, err
		}
	}
	// Stable small display lines make every long scalar reachable even under a
	// narrow transport budget. These are views; exact JSON remains in Transcript.
	// Keep scalar JSON escaping: an original newline must remain distinct from
	// a display wrap, so quotes and identifiers can be reconstructed exactly.
	view, _, err := agentWorkspaceJSONReadViewWidth(selected, runnerCorrectionReadLineBytes)
	if err != nil {
		return nil, err
	}
	offset, limit := 1, agentWorkspaceReadDefaultRows
	if value, found := input["offset"]; found {
		offset, _ = strictPositiveAgentWorkspaceInteger(value)
	}
	if value, found := input["limit"]; found {
		limit, _ = strictPositiveAgentWorkspaceInteger(value)
	}
	metadata := map[string]any{
		"recovery_condition_id": id, "condition_fingerprint": run.CorrectionCondition.Fingerprint(),
		"evidence_kind": "runtime_condition", "view_format": runnerCorrectionReadViewFormat,
		"json_pointer": pointer, "complete_condition": false,
		"effect": agentruntime.ToolEffectValue(agentruntime.ToolEffectUnchanged, "control-state"),
	}
	reserved, _ := ctx.Value(agentWorkspaceReadLocationReserveKey{}).(int)
	pageCtx := context.WithValue(ctx, agentWorkspaceReadLocationReserveKey{}, reserved+len(`,"reused":true`))
	result, err := readAgentWorkspaceTextWindowWithMetadata(pageCtx, bytes.NewReader(view), "recovery-condition.json", "application/json", int64(len(raw)), offset, limit, metadata)
	if err != nil {
		return nil, err
	}
	if numberValue(result["truncated_lines"]) > 0 {
		return nil, errors.New("reader response budget cannot hold a complete recovery-condition display line")
	}
	var start, end int
	if _, err := fmt.Sscanf(stringValue(result["showing_lines"]), "%d-%d", &start, &end); err != nil {
		return nil, err
	}
	result["complete_condition"] = pointer == "" && start == 1 && end == int(numberValue(result["total_lines"]))
	if start > 0 && end >= start {
		result["effect"] = agentruntime.ToolEffectValue(agentruntime.ToolEffectChanged, "control-state")
	}
	if seen {
		result["reused"] = true
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if !agentWorkspaceReadResultFits(ctx, result) {
		return nil, errors.New("recovery condition page exceeds its transport budget")
	}
	run.storeReadReuse("read_file", cacheInput, result)
	return result, nil
}
