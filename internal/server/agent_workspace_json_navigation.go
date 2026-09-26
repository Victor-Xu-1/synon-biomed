package server

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Large structured results have no meaningful first page: a discovery array
// can precede every retrieved document. Return addressable fields for an
// unqualified read, together with actual field values. Index-only responses
// hide all substantive content when the caller does not follow another handle.
// Allocation depends on the serialized transport budget, never field meaning.
func agentWorkspaceJSONNavigation(ctx context.Context, raw []byte, filename, contentType string, size int64, input map[string]any) (map[string]any, bool) {
	if input["version_id"] == nil && input["file_path"] == nil {
		return nil, false
	}
	if _, found := input["offset"]; found {
		return nil, false
	}
	if _, found := input["limit"]; found {
		return nil, false
	}
	if _, found := input["json_pointer"]; found {
		return nil, false
	}
	sections := agentWorkspaceJSONSections(raw)
	if len(sections) == 0 {
		return nil, false
	}
	entries := make([]map[string]any, 0, len(sections))
	var content strings.Builder
	for _, section := range sections {
		value := raw[section.start:section.end]
		kind := "number"
		switch value[0] {
		case '{':
			kind = "object"
		case '[':
			kind = "array"
		case '"':
			kind = "string"
		case 't', 'f':
			kind = "boolean"
		case 'n':
			kind = "null"
		}
		read := map[string]any{"json_pointer": section.path, "human_description": "Reading structured file data"}
		if version, ok := input["version_id"]; ok {
			read["version_id"] = version
		} else {
			read["file_path"] = input["file_path"]
		}
		entry := map[string]any{"json_pointer": section.path, "value_type": kind, "size_bytes": len(value), "read_with": read}
		// Compact scalar metadata fits inline. Other leaf values share the
		// remaining transport space below and keep their full-value handles.
		if kind == "number" || kind == "boolean" || kind == "null" {
			entry["value"] = json.RawMessage(value)
		}
		entries = append(entries, entry)
		if content.Len() > 0 {
			content.WriteByte('\n')
		}
		content.WriteString(section.path + " (" + kind + ")")
	}
	result := map[string]any{
		"filename": filename, "content_type": contentType, "size_bytes": size,
		"view_format": "json-field-index", "content": content.String(), "sections": entries,
		"source_content_included": false, "truncated": true, "next_offset": 1,
	}
	// Parent values would duplicate their indexed children. Share available
	// space across leaves; small late values must not be hidden behind the
	// first large discovery array. Every partial value keeps its full handle.
	leaves := make([]bool, len(sections))
	decodedStrings := make(map[int]string)
	for i, section := range sections {
		leaves[i] = i+1 == len(sections) || !strings.HasPrefix(sections[i+1].path, section.path+"/")
		if leaves[i] && raw[section.start] == '"' {
			var value string
			if err := json.Unmarshal(raw[section.start:section.end], &value); err != nil {
				return nil, false
			}
			// Decode once before allocation. A partial JSON string literal would
			// be escaped again when serialized as a preview, wasting transport
			// space and exposing escape syntax instead of the source characters.
			decodedStrings[i] = value
		}
	}
	var best map[string]any
	low, high := 1, agentWorkspaceReadSerializedLimit(ctx)
	for low <= high {
		if ctx.Err() != nil {
			return nil, false
		}
		quota := low + (high-low)/2
		candidate := agentWorkspaceJSONPreview(result, entries, sections, leaves, decodedStrings, raw, quota)
		if agentWorkspaceReadResultFits(ctx, candidate) {
			best = candidate
			low = quota + 1
		} else {
			high = quota - 1
		}
	}
	return best, best != nil
}

func agentWorkspaceJSONPreview(base map[string]any, entries []map[string]any, sections []agentWorkspaceJSONSection, leaves []bool, decodedStrings map[int]string, raw []byte, quota int) map[string]any {
	result := make(map[string]any, len(base))
	for key, value := range base {
		result[key] = value
	}
	views := make([]map[string]any, len(entries))
	for i, entry := range entries {
		view := make(map[string]any, len(entry))
		for key, value := range entry {
			view[key] = value
		}
		if leaves[i] {
			if text, decoded := decodedStrings[i]; decoded {
				view["value_complete"] = len(text) <= quota
				if len(text) <= quota {
					view["value"] = text
				} else {
					end := quota
					for end > 0 && !utf8.RuneStart(text[end]) {
						end--
					}
					view["value_preview"] = text[:end]
				}
				views[i] = view
				continue
			}
			value := raw[sections[i].start:sections[i].end]
			complete := len(value) <= quota
			view["value_complete"] = complete
			if complete {
				view["value"] = json.RawMessage(value)
			} else {
				end := quota
				for end > 0 && !utf8.RuneStart(value[end]) {
					end--
				}
				view["value_preview"] = string(value[:end])
			}
		}
		views[i] = view
	}
	result["sections"] = views
	result["view_format"] = "json-field-preview"
	result["source_content_included"] = true
	return result
}
