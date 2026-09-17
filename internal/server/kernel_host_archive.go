package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func isKernelArchiveHostMethod(method string) bool {
	return method == "host.archive.search" || method == "host.archive.page"
}

func (s *Server) handleKernelArchiveHostCall(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	method string,
	args []any,
	kwargs map[string]any,
) (any, error) {
	scope := sessionReviewerEvidenceScopeFromContext(ctx)
	if scope == nil || s == nil || s.workspaceStore == nil {
		return nil, kernelruntime.NewHostCallError("permission_denied", "archive access is available only to the active reviewer")
	}
	targetFrameID := strings.TrimSpace(scope.sessionID)
	if targetFrameID == "" || access.Frame.RootFrameID == "" {
		return nil, kernelruntime.NewHostCallError("unavailable", "review archive authority is incomplete")
	}
	target, found, err := s.workspaceStore.GetFrame(targetFrameID)
	if err != nil || !found || target.RootFrameID != access.Frame.RootFrameID || target.ProjectID != access.Frame.ProjectID {
		return nil, kernelruntime.NewHostCallError("permission_denied", "review target is outside the reviewer root")
	}
	switch method {
	case "host.archive.search":
		return s.searchReviewerArchive(ctx, targetFrameID, args, kwargs)
	case "host.archive.page":
		return s.pageReviewerArchive(ctx, targetFrameID, args, kwargs)
	default:
		return nil, kernelruntime.NewHostCallError("method_not_allowed", "archive method is not allowed")
	}
}

func (s *Server) searchReviewerArchive(
	ctx context.Context,
	frameID string,
	args []any,
	kwargs map[string]any,
) (map[string]any, error) {
	if len(args) != 1 || len(kwargs) > 1 {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.archive.search expects query and optional limit")
	}
	query, ok := args[0].(string)
	query = strings.TrimSpace(query)
	if !ok || query == "" || len([]rune(query)) > 512 {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.archive.search query is invalid")
	}
	limit, err := archiveOptionalInt(kwargs, "limit", 8, 1, 20)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
	}
	results := make([]map[string]any, 0, limit)
	after := int64(0)
	for len(results) < limit {
		page, err := s.workspaceStore.GetFrameMessagesPage(frameID, after, 500)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("storage_error", "review transcript history is unavailable")
		}
		for _, event := range page.Messages {
			raw, _ := json.Marshal(event.Payload)
			if !strings.Contains(strings.ToLower(string(raw)), strings.ToLower(query)) {
				continue
			}
			results = append(results, map[string]any{
				"source": "transcript", "sequence": event.Sequence, "event_type": event.Type,
				"content": boundedReviewerEvidence(string(raw), 2048),
			})
			if len(results) == limit {
				break
			}
		}
		if !page.HasMore || page.NextSequence <= after {
			break
		}
		after = page.NextSequence
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if len(results) < limit {
		archives, err := s.workspaceStore.ListCompactionArchives(frameID)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("storage_error", "review compaction archives are unavailable")
		}
		for _, archive := range archives {
			if !strings.Contains(strings.ToLower(archive.Summary), strings.ToLower(query)) {
				continue
			}
			results = append(results, map[string]any{
				"source": "compaction_archive", "archive_index": archive.CompactionIndex,
				"content": boundedReviewerEvidence(archive.Summary, 2048),
			})
			if len(results) == limit {
				break
			}
		}
	}
	return map[string]any{"query": query, "count": len(results), "results": results}, nil
}

func (s *Server) pageReviewerArchive(
	ctx context.Context,
	frameID string,
	args []any,
	kwargs map[string]any,
) (map[string]any, error) {
	if len(args) != 1 || len(kwargs) > 2 {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.archive.page expects archive_index and optional offset/limit")
	}
	indexValue, ok := exactNonNegativeInteger(args[0])
	if !ok {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.archive.page archive_index is invalid")
	}
	offset, err := archiveOptionalInt(kwargs, "offset", 0, 0, 1_000_000)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
	}
	limit, err := archiveOptionalInt(kwargs, "limit", 50, 1, 200)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
	}
	archive, found, err := s.workspaceStore.GetCompactionArchive(frameID, int(indexValue))
	if err != nil || !found {
		return nil, kernelruntime.NewHostCallError("not_found", "review compaction archive was not found")
	}
	if offset > len(archive.Messages) {
		offset = len(archive.Messages)
	}
	end := min(len(archive.Messages), offset+limit)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"archive_index": archive.CompactionIndex, "summary": archive.Summary,
		"offset": offset, "next_offset": end, "has_more": end < len(archive.Messages),
		"messages": archive.Messages[offset:end],
	}, nil
}

func exactNonNegativeInteger(value any) (int64, bool) {
	number := numberValue(value)
	if number < 0 {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	default:
		return 0, false
	}
}

func archiveOptionalInt(payload map[string]any, key string, fallback, minimum, maximum int) (int, error) {
	raw, found := payload[key]
	if !found || raw == nil {
		return fallback, nil
	}
	value, ok := exactNonNegativeInteger(raw)
	if !ok || value < int64(minimum) || value > int64(maximum) {
		return 0, fmt.Errorf("host archive %s must be an integer from %d to %d", key, minimum, maximum)
	}
	return int(value), nil
}
