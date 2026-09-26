package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
)

// ValidatePreview attests a reading view by rebuilding it from the exact
// raw source already checked by the engine. Metadata labels alone are never
// sufficient, and the model cannot supply its own preview implementation.
func (authority runnerLargeToolResultAuthority) ValidatePreview(
	ctx context.Context, input agentruntime.LargeToolResultInput, descriptor agentruntime.LargeToolResultDescriptor,
) error {
	expected, matched, err := runnerSourceFeedbackDescriptor(ctx, input, workspace.RunnerLargeToolResult{
		ArtifactID: descriptor.ArtifactID, VersionID: descriptor.VersionID,
		ContentType: descriptor.ContentType, SizeBytes: descriptor.SizeBytes, ContentSHA256: descriptor.SHA256,
	})
	if err != nil {
		return err
	}
	if !matched || expected != descriptor {
		return errors.New("source reading preview does not match its immutable source")
	}
	return nil
}

// A log prefix is not the model's reading surface. Reuse read_file's typed
// view for first feedback, with the same immutable source and continuation
// coordinates. No extra source acquisition or model call happens here.
func runnerSourceFeedbackDescriptor(
	ctx context.Context, input agentruntime.LargeToolResultInput, record workspace.RunnerLargeToolResult,
) (agentruntime.LargeToolResultDescriptor, bool, error) {
	if len(input.RawJSON) > maxAgentWorkspaceStructuredJSONBytes {
		return agentruntime.LargeToolResultDescriptor{}, false, nil
	}
	base := agentruntime.LargeToolResultDescriptor{
		ArtifactID: record.ArtifactID, VersionID: record.VersionID, SHA256: record.ContentSHA256,
		SizeBytes: record.SizeBytes, ContentType: record.ContentType, Outcome: input.Outcome,
		ContentURL: "/api/artifacts/" + url.PathEscape(record.ArtifactID) + "/versions/" + url.PathEscape(record.VersionID),
		ReadWith:   toolcontract.CanonicalReadWith(record.VersionID), Truncated: true,
	}
	envelope, _ := json.Marshal(base)
	inlineLimit := min(input.MaxInlineBytes, runnerLargeToolResultInlineLimitBytes)
	budget := int(inlineLimit) - len(envelope)
	read := map[string]any{"version_id": record.VersionID}
	for budget > 0 {
		if err := ctx.Err(); err != nil {
			return agentruntime.LargeToolResultDescriptor{}, false, err
		}
		view, matched, err := agentWorkspaceStructuredView(
			withAgentWorkspaceReadBudget(ctx, int64(budget)), input.RawJSON,
			runnerLargeToolResultFilename(input.ToolCall.Name, input.ToolCall.ID), record.SizeBytes, read,
		)
		if !matched && json.Valid(input.RawJSON) {
			view, matched = agentWorkspaceJSONNavigation(
				withAgentWorkspaceReadBudget(ctx, int64(budget)), input.RawJSON,
				runnerLargeToolResultFilename(input.ToolCall.Name, input.ToolCall.ID), record.ContentType,
				record.SizeBytes, read,
			)
		}
		if !matched {
			return agentruntime.LargeToolResultDescriptor{}, false, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return agentruntime.LargeToolResultDescriptor{}, false, ctx.Err()
			}
			// The raw source has already been persisted. A display-only failure
			// must not turn successful acquisition into a failed tool execution.
			log.Printf("runner_source_feedback_fallback tool=%q reason=display_budget", input.ToolCall.Name)
			return agentruntime.LargeToolResultDescriptor{}, false, nil
		}
		view["source_version_id"] = record.VersionID
		view["source_size_bytes"] = record.SizeBytes
		if next, present := view["next_offset"]; present {
			view["read_with"] = map[string]any{"version_id": record.VersionID, "offset": next}
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			return agentruntime.LargeToolResultDescriptor{}, false, err
		}
		candidate := base
		candidate.Preview = string(encoded)
		transport, err := json.Marshal(candidate)
		if err != nil {
			return agentruntime.LargeToolResultDescriptor{}, false, err
		}
		excess := len(transport) - int(inlineLimit)
		if excess <= 0 {
			return candidate, true, nil
		}
		// Account for the actual outer JSON escaping, not a second fixed
		// truncation policy. The read view owns all line and cursor handling.
		budget -= excess
	}
	log.Printf("runner_source_feedback_fallback tool=%q reason=display_budget", input.ToolCall.Name)
	return agentruntime.LargeToolResultDescriptor{}, false, nil
}

func runnerSourceFeedbackPreview(descriptor toolcontract.ExternalizedResultDescriptor) bool {
	encoded, err := json.Marshal(descriptor)
	if err != nil || len(encoded) > int(runnerLargeToolResultInlineLimitBytes) {
		return false
	}
	var view struct {
		Format                string `json:"view_format"`
		Version               string `json:"source_version_id"`
		Size                  int64  `json:"source_size_bytes"`
		Content               string `json:"content"`
		SourceContentIncluded bool   `json:"source_content_included"`
	}
	if json.Unmarshal([]byte(descriptor.Preview), &view) != nil ||
		view.Version != descriptor.VersionID || view.Size != descriptor.SizeBytes || view.Content == "" {
		return false
	}
	switch view.Format {
	case "html-readable-display-lines", "search-results-display-lines", "json-record-directory-display-lines", "jats-readable-display-lines":
		return true
	case "article-record-display-lines":
		return view.SourceContentIncluded
	case "json-field-preview":
		return view.SourceContentIncluded
	default:
		return false
	}
}
