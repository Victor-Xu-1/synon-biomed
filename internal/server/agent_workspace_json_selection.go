package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

func readAgentWorkspaceJSONSelection(ctx context.Context, reader io.ReadSeeker, filename string, size int64, input map[string]any) (any, error) {
	pointer, ok := input["json_pointer"].(string)
	if !ok {
		return nil, errors.New("read_file.json_pointer must be a string")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	selected, err := selectAgentWorkspaceJSONValue(ctx, reader, pointer)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(selected) {
		return nil, errors.New("selected JSON value is not valid UTF-8")
	}
	contentType := "application/json"
	var view []byte
	if len(selected) > 0 && selected[0] == '"' {
		var text string
		if err := json.Unmarshal(selected, &text); err != nil {
			return nil, err
		}
		view = agentWorkspaceSelectedTextView(text)
		contentType = "text/plain; charset=utf-8"
	} else {
		view, _, err = agentWorkspaceJSONReadView(selected)
		if err != nil {
			return nil, err
		}
	}
	metadata := map[string]any{"json_pointer": pointer, "source_size_bytes": size, "view_format": "json-selection-display-lines"}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	// Reserve the exact selection metadata before choosing a page. Appending
	// it afterwards must not push the response over the real transport budget.
	available := agentWorkspaceReadSerializedLimit(ctx) - len(encoded) - 2
	if available <= 0 {
		return nil, errors.New("JSON selection metadata exceeds the available read response budget")
	}
	pageCtx := withAgentWorkspaceReadBudget(ctx, int64(available))
	offset, limit := 1, agentWorkspaceReadDefaultRows
	if value, found := input["offset"]; found {
		offset, ok = strictPositiveAgentWorkspaceInteger(value)
		if !ok {
			return nil, errors.New("read_file.offset is invalid")
		}
	}
	if value, found := input["limit"]; found {
		limit, ok = strictPositiveAgentWorkspaceInteger(value)
		if !ok {
			return nil, errors.New("read_file.limit is invalid")
		}
	}
	result, err := readAgentWorkspaceTextWindow(pageCtx, bytes.NewReader(view), filename, contentType, int64(len(selected)), offset, limit)
	if err != nil {
		return nil, err
	}
	if result["truncated"] == true && stringValue(result["content"]) == "" {
		return nil, errors.New("the selected display line cannot fit the available read response budget")
	}
	for key, value := range metadata {
		result[key] = value
	}
	if !agentWorkspaceReadResultFits(ctx, result) {
		return nil, errors.New("JSON selection page exceeds the read response budget")
	}
	return result, nil
}

// Preserve decoded string text and its original newlines in a stable display
// with UTF-8-safe soft wrapping, so a long scalar has reachable later pages.
func agentWorkspaceSelectedTextView(text string) []byte {
	var view strings.Builder
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			view.WriteByte('\n')
		}
		for len(line) > 1024 {
			end := 1024
			for !utf8.RuneStart(line[end]) {
				end--
			}
			view.WriteString(line[:end])
			view.WriteByte('\n')
			line = line[end:]
		}
		view.WriteString(line)
	}
	return []byte(view.String())
}
