package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

type agentWorkspaceReadBudgetKey struct{}
type agentWorkspaceReadLocationReserveKey struct{}

const agentWorkspaceTextContinuationHint = "The requested line window was automatically bounded. Continue from next_offset when more evidence is required."

// Reserve the actual envelope shape, using the widest representable line
// counters. A fixed 2048-byte reservation could consume the entire available
// page even though its real metadata and a complete display line would fit.
func agentWorkspaceTextWindowOverhead(filename, contentType string, size int64, offset, limit int) int {
	maxIndex := int(^uint(0) >> 1)
	metadata := map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"total_lines": maxIndex, "showing_lines": fmt.Sprintf("%d-%d", maxIndex, maxIndex), "content": "",
		"truncated_lines": maxIndex, "truncated": true, "requested_offset": offset,
		"requested_limit": limit, "next_offset": maxIndex, "system_hint": agentWorkspaceTextContinuationHint,
	}
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": metadata})
	return len(encoded)
}

func withAgentWorkspaceReadBudget(ctx context.Context, limit int64) context.Context {
	if limit <= 0 {
		return ctx
	}
	return context.WithValue(ctx, agentWorkspaceReadBudgetKey{}, limit)
}

func agentWorkspaceReadSerializedLimit(ctx context.Context) int {
	limit, _ := ctx.Value(agentWorkspaceReadBudgetKey{}).(int64)
	if limit <= 0 {
		// Standalone callers retain the existing raw-text limit. The runner
		// supplies its actual serialized transport limit explicitly.
		limit = 6*agentWorkspaceReadMaxBytes + 2048
	} else if limit > agentWorkspaceReadMaxBytes {
		limit = agentWorkspaceReadMaxBytes
	}
	reserved, _ := ctx.Value(agentWorkspaceReadLocationReserveKey{}).(int)
	return max(1, int(limit)-reserved)
}

func agentWorkspaceReadResultFits(ctx context.Context, result any) bool {
	encoded, err := json.Marshal(map[string]any{"ok": true, "result": result})
	return err == nil && len(encoded) <= agentWorkspaceReadSerializedLimit(ctx)
}

func readAgentWorkspaceTextWindowWithMetadata(ctx context.Context, reader io.Reader, filename, contentType string, size int64, offset, limit int, metadata map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	reserved, _ := ctx.Value(agentWorkspaceReadLocationReserveKey{}).(int)
	viewCtx := context.WithValue(ctx, agentWorkspaceReadLocationReserveKey{}, reserved+len(encoded)-1)
	result, err := readAgentWorkspaceTextWindow(viewCtx, reader, filename, contentType, size, offset, limit)
	if err != nil {
		return nil, err
	}
	for key, value := range metadata {
		result[key] = value
	}
	return result, nil
}

// Bound the first display entry by serialized bytes while retaining valid UTF-8.
// Later logical lines remain reachable through the ordinary line cursor.
func boundedAgentWorkspaceTextEntry(entry, marker string, budget int) (string, bool) {
	if budget <= 0 {
		return "", false
	}
	runes := []rune(entry)
	low, high := 0, min(len(runes), budget)
	for low < high {
		mid := low + (high-low+1)/2
		encoded, _ := json.Marshal(string(runes[:mid]) + marker)
		if len(encoded)-2 <= budget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	if low == 0 {
		return "", false
	}
	return string(runes[:low]) + marker, true
}

// JSON indentation alone leaves an article, XML payload or tabular field on a
// single escaped string line. Soft-wrap the read-only view at UTF-8 boundaries
// so line pagination can reach every byte. Stored JSON and its hash are never
// changed. A fixed view width keeps offsets stable across provider rounds.
func agentWorkspaceJSONReadView(raw []byte) ([]byte, bool, error) {
	return agentWorkspaceJSONReadViewWidth(raw, 1024)
}

func agentWorkspaceJSONReadViewWidth(raw []byte, width int) ([]byte, bool, error) {
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, raw, "", "  "); err != nil {
		return nil, false, err
	}
	var view bytes.Buffer
	wrapped := false
	lines := bytes.Split(formatted.Bytes(), []byte{'\n'})
	starts := make([]int, 0, len(lines))
	sourceOffset := 0
	for index, line := range lines {
		if index > 0 {
			view.WriteByte('\n')
		}
		starts = append(starts, sourceOffset)
		for len(line) > width {
			end := width
			for end > 0 && !utf8.RuneStart(line[end]) {
				end--
			}
			view.Write(line[:end])
			view.WriteByte('\n')
			line = line[end:]
			sourceOffset += end
			starts = append(starts, sourceOffset)
			wrapped = true
		}
		view.Write(line)
		sourceOffset += len(line) + 1
	}
	if formatted.Len() > int(runnerLargeToolResultInlineLimitBytes) {
		if directory := agentWorkspaceJSONSectionDirectory(formatted.Bytes(), starts); directory != "" {
			return append([]byte(directory), view.Bytes()...), true, nil
		}
	}
	return view.Bytes(), wrapped, nil
}
