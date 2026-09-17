package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
	"unicode/utf8"
)

const (
	agentWorkspaceReadMaxBytes    = 100 << 10
	agentWorkspaceReadDefaultRows = 2000
	agentWorkspaceReadMaxPages    = 100
	agentWorkspaceVisualMaxBytes  = 25 << 20
	agentWorkspacePDFMaxBytes     = 500 << 20
	agentWorkspaceVisualTotal     = 32 << 20
)

var agentWorkspaceEditLocks [64]sync.Mutex

type agentWorkspaceFileTarget struct {
	root              string
	relativePath      string
	path              string
	displayPath       string
	createParents     bool
	requestedAbsolute bool
}

type agentWorkspaceReadableVersion struct {
	reader      workspace.ArtifactContentReader
	filename    string
	contentType string
	sizeBytes   int64
	versionID   string
	sha256      string
}

type agentRuntimeRichToolResponse struct {
	value any
	parts []agentruntime.ContentPart
}

func unwrapAgentRuntimeRichToolResponse(value any) (any, []agentruntime.ContentPart) {
	response, ok := value.(agentRuntimeRichToolResponse)
	if !ok {
		return value, nil
	}
	return response.value, append([]agentruntime.ContentPart(nil), response.parts...)
}

func agentWorkspaceReadFileToolSchema() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{
		Name: "read_file",
		Description: "Read a UTF-8 text file from this task-scoped workspace or an authorized immutable project version, including an oversized prior tool result. " +
			"Use file_path only for workspace paths. When a prior tool result supplies version_id, including ltr-* identifiers, pass it as version_id and never as file_path. " +
			"An oversized result's immutable large-tool-result-* artifact_id is also accepted in version_id and resolves to that exact result. " +
			"Large text must be read with a 1-based line window. File content is detected independently of its extension. HTML, CSV/TSV, DOCX, XLSX and PPTX use passive built-in readers; PDF and common images are attached as multimodal evidence. " +
			"Other binary or structured formats return a machine-readable reader_contract and reader_selection instead of fake text. Follow that contract to select an available Skill or verified read-only parser, using repl with import host; path = host.artifact_path(version_id) when the immutable original must be materialized. Preserve originals, never execute uploaded macros/programs, and validate extracted content.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"human_description": map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Short present-participle label naming the file evidence being read."},
				"version_id":        map[string]any{"type": "string", "description": "Optional immutable artifact version id or oversized-tool-result version/artifact id owned by this project."},
				"file_path":         map[string]any{"type": "string", "description": "Workspace-relative path, or an absolute path covered by an active host grant."},
				"offset":            map[string]any{"type": "integer", "minimum": 1, "description": "Optional 1-based first text line."},
				"limit":             map[string]any{"type": "integer", "minimum": 1, "description": "Optional number of text lines to return; defaults to 2000 when a window is requested."},
				"json_pointer":      map[string]any{"type": "string", "description": "Optional RFC 6901 path to a JSON field or array element, as listed in the JSON field directory. Reads that value directly; strings are decoded as text. Keep this path when continuing with next_offset; offsets are relative to the selected value."},
				"pages": map[string]any{
					"type": "array", "maxItems": agentWorkspaceReadMaxPages,
					"items":       map[string]any{"type": "integer", "minimum": 1},
					"description": "Optional 1-based PDF pages. It cannot be combined with offset or limit.",
				},
			},
			"required": []string{"human_description"},
		},
	}
}

// normalizeAgentWorkspaceReadFileArguments removes the only ambiguous routing
// state admitted by the public read contract. A durable version authority is
// canonical whenever it is explicitly present. Oversized tool-result ids are
// also structurally distinguishable from paths, so a model placing one in
// file_path can be corrected before validation, permission, and execution.
// Ordinary path-like strings are never probed or rerouted.
func normalizeAgentWorkspaceReadFileArguments(input map[string]any) map[string]any {
	versionID := strings.TrimSpace(stringValue(input["version_id"]))
	filePath := strings.TrimSpace(stringValue(input["file_path"]))
	if versionID != "" {
		if filePath == "" && !workspace.IsRunnerLargeToolResultVersionID(versionID) &&
			!workspace.IsRunnerLargeToolResultArtifactID(versionID) {
			return input
		}
		normalized := copyMapAny(input)
		if filePath != "" {
			delete(normalized, "file_path")
		}
		return normalized
	}
	if !workspace.IsRunnerLargeToolResultVersionID(filePath) {
		return input
	}
	normalized := copyMapAny(input)
	delete(normalized, "file_path")
	normalized["version_id"] = filePath
	return normalized
}

func agentWorkspaceEditFileToolSchema() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{
		Name:        "edit_file",
		Description: "Create, fully rewrite, or exactly edit one UTF-8 file in this task-scoped workspace. Read the target first. For a complete rewrite, old_string MUST be empty and new_string is the complete file; never paste the whole current file into old_string. For a targeted edit, use the smallest exact old_string that matches once, including current whitespace and indentation. Make multiple targeted edits with multiple edit_file calls instead of rebuilding the whole file inside one targeted replacement. If edit_conflict is returned, read the file again and retry once with current exact text instead of repeating the stale call. For links between companion files in the same deliverable set, use their exact workspace-relative paths, for example ![plot](plot.png). Never invent {{artifact:...}} placeholders: that syntax is valid only when copied exactly from a successful save_artifacts result. Never edit a report solely to replace its own artifact reference with a newer version: saving that edit creates another version forever; keep the self-entry as plain text and use the newest save_artifacts reference in the final answer.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"human_description": map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Short present-participle label naming the file being edited."},
				"file_path":         map[string]any{"type": "string", "description": "Workspace-relative path, or an absolute path covered by a read-write host grant."},
				"old_string":        map[string]any{"type": "string", "description": "Exact text to replace once. Empty means replace the complete file."},
				"new_string":        map[string]any{"type": "string", "description": "Replacement text or complete new file content."},
			},
			"required": []string{"file_path", "old_string", "new_string", "human_description"},
		},
	}
}

func preferCanonicalFrameFileToolSchemas(schemas []agentruntime.ToolSchema) []agentruntime.ToolSchema {
	filtered := make([]agentruntime.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		if legacyFrameModelFileTool(schema.Name) {
			continue
		}
		filtered = append(filtered, schema)
	}
	return filtered
}

func legacyFrameModelFileTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "file_list", "file_info", "file_read", "Read", "ReadBatch",
		"file_write", "Write", "NotebookEdit", "file_mkdir", "file_copy", "file_move", "file_delete",
		"file_search", "Glob", "Grep", "file_replace", "Edit", "file_patch", "Patch",
		"json_patch", "code_index", "code_references", "artifact_register", "artifact_list", "artifact_get", "session_export":
		return true
	default:
		return false
	}
}

func isAgentWorkspaceFileTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "read_file", "edit_file":
		return true
	default:
		return false
	}
}

func validateAgentWorkspaceFileToolInput(name string, input map[string]any) error {
	switch strings.TrimSpace(name) {
	case "read_file":
		return validateAgentWorkspaceReadFileInput(input)
	case "edit_file":
		return validateAgentWorkspaceEditFileInput(input)
	default:
		return errors.New("workspace file tool is unavailable")
	}
}

func validateAgentWorkspaceReadFileInput(input map[string]any) error {
	allowed := map[string]bool{"version_id": true, "file_path": true, "offset": true, "limit": true, "pages": true, "human_description": true, "json_pointer": true}
	for key := range input {
		if !allowed[key] {
			return errors.New("read_file arguments are invalid")
		}
	}
	filePath, filePresent := input["file_path"]
	versionID, versionPresent := input["version_id"]
	if filePresent {
		value, ok := filePath.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return errors.New("read_file.file_path must be a non-empty string")
		}
	}
	if versionPresent {
		value, ok := versionID.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return errors.New("read_file.version_id must be a non-empty string")
		}
	}
	if !filePresent && !versionPresent {
		return errors.New("read_file requires file_path or version_id")
	}
	if raw, found := input["json_pointer"]; found {
		pointer, ok := raw.(string)
		if !ok {
			return errors.New("read_file.json_pointer must be a string")
		}
		if _, err := agentWorkspaceJSONPointerTokens(pointer); err != nil {
			return err
		}
		if _, pages := input["pages"]; pages {
			return errors.New("read_file.json_pointer cannot be combined with PDF pages")
		}
	}
	for _, key := range []string{"offset", "limit"} {
		if raw, found := input[key]; found {
			_, ok := strictPositiveAgentWorkspaceInteger(raw)
			if !ok {
				return fmt.Errorf("read_file.%s is invalid", key)
			}
		}
	}
	if raw, found := input["pages"]; found {
		pages, ok := raw.([]any)
		if !ok {
			if typed, typedOK := raw.([]int); typedOK {
				pages = make([]any, len(typed))
				for index, value := range typed {
					pages[index] = value
				}
				ok = true
			}
		}
		if !ok || len(pages) > agentWorkspaceReadMaxPages {
			return errors.New("read_file.pages is invalid")
		}
		for _, page := range pages {
			if _, valid := strictPositiveAgentWorkspaceInteger(page); !valid {
				return errors.New("read_file.pages is invalid")
			}
		}
		if _, offset := input["offset"]; offset {
			return errors.New("read_file.pages cannot be combined with offset or limit")
		}
		if _, limit := input["limit"]; limit {
			return errors.New("read_file.pages cannot be combined with offset or limit")
		}
	}
	return nil
}

func validateAgentWorkspaceEditFileInput(input map[string]any) error {
	allowed := map[string]bool{"file_path": true, "old_string": true, "new_string": true, "human_description": true}
	for key := range input {
		if !allowed[key] {
			return errors.New("edit_file arguments are invalid")
		}
	}
	filePath, fileFound := input["file_path"].(string)
	oldString, oldFound := input["old_string"].(string)
	newString, newFound := input["new_string"].(string)
	if !fileFound || strings.TrimSpace(filePath) == "" {
		return errors.New("edit_file.file_path must be a non-empty string")
	}
	if !oldFound || !newFound {
		return errors.New("edit_file.old_string and new_string must be strings")
	}
	if !utf8.ValidString(oldString) || !utf8.ValidString(newString) {
		return errors.New("edit_file text must be valid UTF-8")
	}
	return nil
}

func strictPositiveAgentWorkspaceInteger(raw any) (int, bool) {
	var value int64
	switch typed := raw.(type) {
	case int:
		value = int64(typed)
	case int32:
		value = int64(typed)
	case int64:
		value = typed
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed > math.MaxInt64 {
			return 0, false
		}
		value = int64(typed)
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		value = parsed
	default:
		return 0, false
	}
	if value <= 0 || value > int64(^uint(0)>>1) {
		return 0, false
	}
	return int(value), true
}
