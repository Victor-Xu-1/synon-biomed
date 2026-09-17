package fileops

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func splitPatchLines(content string) []string {
	if content == "" {
		return []string{}
	}
	return strings.SplitAfter(content, "\n")
}

func applyPatchOperation(lines []string, operation PatchOperation) ([]string, error) {
	switch operation.Type {
	case "replace_lines":
		if operation.StartLine <= 0 || operation.EndLine < operation.StartLine || operation.EndLine > len(lines) {
			return nil, fmt.Errorf("invalid line range %d-%d", operation.StartLine, operation.EndLine)
		}
		replacement := splitPatchLines(operation.Content)
		updated := make([]string, 0, len(lines)-operation.EndLine+operation.StartLine-1+len(replacement))
		updated = append(updated, lines[:operation.StartLine-1]...)
		updated = append(updated, replacement...)
		updated = append(updated, lines[operation.EndLine:]...)
		return updated, nil
	case "insert_after":
		if operation.Line < 0 || operation.Line > len(lines) {
			return nil, fmt.Errorf("invalid insert line %d", operation.Line)
		}
		inserted := splitPatchLines(operation.Content)
		updated := make([]string, 0, len(lines)+len(inserted))
		updated = append(updated, lines[:operation.Line]...)
		updated = append(updated, inserted...)
		updated = append(updated, lines[operation.Line:]...)
		return updated, nil
	case "append":
		return append(append([]string(nil), lines...), splitPatchLines(operation.Content)...), nil
	default:
		return nil, fmt.Errorf("unsupported patch operation: %s", operation.Type)
	}
}

type unifiedPatchFile struct {
	FilePath   string
	OldPath    string
	NewPath    string
	ChangeType string
	Hunks      []unifiedPatchHunk
}

type unifiedPatchHunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []unifiedPatchLine
}

type unifiedPatchLine struct {
	Type  string
	Value string
}

type unifiedPatchApplyResult struct {
	UpdatedFiles map[string]string
	DeletedFiles []string
	Files        []OriginalPatchFile
}

var unifiedHunkHeaderPattern = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parseUnifiedPatch(patchText string) ([]unifiedPatchFile, error) {
	normalized := strings.ReplaceAll(patchText, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	files := make([]unifiedPatchFile, 0)
	index := 0
	for index < len(lines) {
		for index < len(lines) && (lines[index] == "" || isUnifiedPatchMetadataLine(lines[index])) {
			index++
		}
		if index >= len(lines) {
			break
		}
		if !strings.HasPrefix(lines[index], "--- ") {
			return nil, fmt.Errorf("Malformed unified diff: expected \"---\" header at line %d", index+1)
		}
		oldPath, err := parseUnifiedPatchHeaderPath(lines[index], "---", index+1)
		if err != nil {
			return nil, err
		}
		index++
		if index >= len(lines) || !strings.HasPrefix(lines[index], "+++ ") {
			return nil, fmt.Errorf("Malformed unified diff: expected \"+++\" header at line %d", index+1)
		}
		newPath, err := parseUnifiedPatchHeaderPath(lines[index], "+++", index+1)
		if err != nil {
			return nil, err
		}
		index++
		file := unifiedPatchFile{
			OldPath:    normalizeUnifiedDiffPath(oldPath),
			NewPath:    normalizeUnifiedDiffPath(newPath),
			ChangeType: unifiedPatchChangeType(oldPath, newPath),
		}
		if file.ChangeType == "delete" {
			file.FilePath = file.OldPath
		} else {
			file.FilePath = file.NewPath
		}
		for index < len(lines) && !strings.HasPrefix(lines[index], "--- ") {
			if lines[index] == "" {
				index++
				continue
			}
			match := unifiedHunkHeaderPattern.FindStringSubmatch(lines[index])
			if match == nil {
				if isUnifiedPatchMetadataLine(lines[index]) {
					index++
					continue
				}
				return nil, fmt.Errorf("Malformed unified diff: expected hunk header at line %d", index+1)
			}
			hunk := unifiedPatchHunk{
				OldStart: atoiDefault(match[1], 0),
				OldLines: atoiDefault(match[2], 1),
				NewStart: atoiDefault(match[3], 0),
				NewLines: atoiDefault(match[4], 1),
			}
			index++
			for index < len(lines) && !strings.HasPrefix(lines[index], "@@ ") && !strings.HasPrefix(lines[index], "--- ") {
				line := lines[index]
				if strings.HasPrefix(line, `\ No newline`) {
					index++
					continue
				}
				if line == "" {
					index++
					continue
				}
				marker := line[0]
				value := line[1:]
				switch marker {
				case ' ':
					hunk.Lines = append(hunk.Lines, unifiedPatchLine{Type: "context", Value: value})
				case '-':
					hunk.Lines = append(hunk.Lines, unifiedPatchLine{Type: "remove", Value: value})
				case '+':
					hunk.Lines = append(hunk.Lines, unifiedPatchLine{Type: "add", Value: value})
				default:
					return nil, fmt.Errorf("Malformed unified diff: invalid hunk line at %d", index+1)
				}
				index++
			}
			file.Hunks = append(file.Hunks, hunk)
		}
		if len(file.Hunks) == 0 {
			return nil, fmt.Errorf("Malformed unified diff: %s has no hunks", file.FilePath)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil, errors.New("Malformed unified diff: no file patches found")
	}
	return files, nil
}

func applyUnifiedPatch(files []unifiedPatchFile, currentFiles map[string]string) (unifiedPatchApplyResult, error) {
	result := unifiedPatchApplyResult{
		UpdatedFiles: make(map[string]string, len(files)),
		DeletedFiles: make([]string, 0),
		Files:        make([]OriginalPatchFile, 0, len(files)),
	}
	for _, file := range files {
		current, ok := currentFiles[file.FilePath]
		if !ok {
			return unifiedPatchApplyResult{}, fmt.Errorf("Patch target was not provided: %s", file.FilePath)
		}
		updated, err := applyUnifiedPatchFile(file, current)
		if err != nil {
			return unifiedPatchApplyResult{}, err
		}
		additions, deletions := countUnifiedPatchChanges(file)
		if file.ChangeType == "delete" {
			result.DeletedFiles = append(result.DeletedFiles, file.FilePath)
		} else {
			result.UpdatedFiles[file.FilePath] = updated
		}
		result.Files = append(result.Files, OriginalPatchFile{
			FilePath:   file.FilePath,
			ChangeType: file.ChangeType,
			Additions:  additions,
			Deletions:  deletions,
		})
	}
	return result, nil
}

func applyUnifiedPatchFile(file unifiedPatchFile, current string) (string, error) {
	hadTrailingNewline := strings.HasSuffix(current, "\n") || file.ChangeType == "create"
	source := strings.Split(strings.ReplaceAll(current, "\r\n", "\n"), "\n")
	if len(source) > 0 && source[len(source)-1] == "" {
		source = source[:len(source)-1]
	}
	output := make([]string, 0, len(source))
	sourceIndex := 0
	for _, hunk := range file.Hunks {
		hunkStart := hunk.OldStart - 1
		if hunkStart < 0 {
			hunkStart = 0
		}
		if hunkStart < sourceIndex || hunkStart > len(source) {
			return "", fmt.Errorf("Patch context for %s points outside the current file", file.FilePath)
		}
		output = append(output, source[sourceIndex:hunkStart]...)
		sourceIndex = hunkStart
		for _, line := range hunk.Lines {
			if line.Type == "add" {
				output = append(output, line.Value)
				continue
			}
			if sourceIndex >= len(source) || source[sourceIndex] != line.Value {
				return "", fmt.Errorf("Patch context mismatch in %s at line %d", file.FilePath, sourceIndex+1)
			}
			if line.Type == "context" {
				output = append(output, source[sourceIndex])
			}
			sourceIndex++
		}
	}
	output = append(output, source[sourceIndex:]...)
	content := strings.Join(output, "\n")
	if hadTrailingNewline {
		content += "\n"
	}
	return content, nil
}

func parseUnifiedPatchHeaderPath(line string, marker string, lineNumber int) (string, error) {
	raw := strings.TrimSpace(strings.TrimPrefix(line, marker))
	if raw == "" {
		return "", fmt.Errorf("Malformed unified diff: empty path at line %d", lineNumber)
	}
	fields := strings.Fields(raw)
	if len(fields) > 0 {
		raw = fields[0]
	}
	if raw == "/dev/null" {
		return raw, nil
	}
	return normalizeUnifiedDiffPath(raw), nil
}

func normalizeUnifiedDiffPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	path = strings.TrimPrefix(path, "./")
	return filepath.ToSlash(path)
}

func unifiedPatchChangeType(oldPath string, newPath string) string {
	if strings.TrimSpace(oldPath) == "/dev/null" {
		return "create"
	}
	if strings.TrimSpace(newPath) == "/dev/null" {
		return "delete"
	}
	return "modify"
}

func isUnifiedPatchMetadataLine(line string) bool {
	return strings.HasPrefix(line, "diff --git ") ||
		strings.HasPrefix(line, "index ") ||
		strings.HasPrefix(line, "new file mode ") ||
		strings.HasPrefix(line, "deleted file mode ") ||
		strings.HasPrefix(line, "old mode ") ||
		strings.HasPrefix(line, "new mode ") ||
		strings.HasPrefix(line, "similarity index ") ||
		strings.HasPrefix(line, "rename from ") ||
		strings.HasPrefix(line, "rename to ")
}

func countUnifiedPatchChanges(file unifiedPatchFile) (int, int) {
	additions := 0
	deletions := 0
	for _, hunk := range file.Hunks {
		for _, line := range hunk.Lines {
			switch line.Type {
			case "add":
				additions++
			case "remove":
				deletions++
			}
		}
	}
	return additions, deletions
}

func atoiDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func applyJSONPatchOperation(document any, operation JSONPatchOperation) (any, error) {
	op := strings.TrimSpace(operation.Op)
	if op != "add" && op != "replace" && op != "remove" {
		return nil, fmt.Errorf("unsupported json patch op: %s", op)
	}
	tokens, err := parseJSONPointer(operation.Path)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		switch op {
		case "add", "replace":
			return operation.Value, nil
		case "remove":
			return nil, nil
		}
	}
	return applyJSONPatchAt(document, tokens, operation)
}

func parseJSONPointer(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("json patch path must start with /: %s", path)
	}
	parts := strings.Split(path[1:], "/")
	for index, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		parts[index] = part
	}
	return parts, nil
}

func applyJSONPatchAt(current any, tokens []string, operation JSONPatchOperation) (any, error) {
	if len(tokens) == 0 {
		return applyJSONPatchOperation(current, JSONPatchOperation{Op: operation.Op, Path: "", Value: operation.Value})
	}
	token := tokens[0]
	if len(tokens) == 1 {
		return applyJSONPatchLeaf(current, token, operation)
	}
	switch typed := current.(type) {
	case map[string]any:
		child, ok := typed[token]
		if !ok {
			return nil, fmt.Errorf("json patch path not found: %s", token)
		}
		updated, err := applyJSONPatchAt(child, tokens[1:], operation)
		if err != nil {
			return nil, err
		}
		typed[token] = updated
		return typed, nil
	case []any:
		index, err := jsonArrayIndex(token, len(typed), false)
		if err != nil {
			return nil, err
		}
		updated, err := applyJSONPatchAt(typed[index], tokens[1:], operation)
		if err != nil {
			return nil, err
		}
		typed[index] = updated
		return typed, nil
	default:
		return nil, fmt.Errorf("json patch path traverses non-container at %s", token)
	}
}

func applyJSONPatchLeaf(current any, token string, operation JSONPatchOperation) (any, error) {
	switch typed := current.(type) {
	case map[string]any:
		return applyJSONPatchMapLeaf(typed, token, operation)
	case []any:
		return applyJSONPatchArrayLeaf(typed, token, operation)
	default:
		return nil, fmt.Errorf("json patch path targets non-container at %s", token)
	}
}

func applyJSONPatchMapLeaf(object map[string]any, key string, operation JSONPatchOperation) (any, error) {
	switch operation.Op {
	case "add":
		object[key] = operation.Value
	case "replace":
		if _, ok := object[key]; !ok {
			return nil, fmt.Errorf("json patch replace path not found: %s", key)
		}
		object[key] = operation.Value
	case "remove":
		if _, ok := object[key]; !ok {
			return nil, fmt.Errorf("json patch remove path not found: %s", key)
		}
		delete(object, key)
	}
	return object, nil
}

func applyJSONPatchArrayLeaf(array []any, token string, operation JSONPatchOperation) (any, error) {
	switch operation.Op {
	case "add":
		if token == "-" {
			return append(array, operation.Value), nil
		}
		index, err := jsonArrayIndex(token, len(array), true)
		if err != nil {
			return nil, err
		}
		array = append(array, nil)
		copy(array[index+1:], array[index:])
		array[index] = operation.Value
		return array, nil
	case "replace":
		index, err := jsonArrayIndex(token, len(array), false)
		if err != nil {
			return nil, err
		}
		array[index] = operation.Value
		return array, nil
	case "remove":
		index, err := jsonArrayIndex(token, len(array), false)
		if err != nil {
			return nil, err
		}
		return append(array[:index], array[index+1:]...), nil
	default:
		return nil, fmt.Errorf("unsupported json patch op: %s", operation.Op)
	}
}

func jsonArrayIndex(token string, length int, allowEnd bool) (int, error) {
	index, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("json patch array index must be numeric: %s", token)
	}
	if index < 0 || index > length || (!allowEnd && index == length) {
		return 0, fmt.Errorf("json patch array index out of range: %d", index)
	}
	return index, nil
}

type globMatcher struct {
	full     *regexp.Regexp
	basename *regexp.Regexp
}

type grepFile struct {
	AbsPath string
	RelPath string
}

type grepLine struct {
	File string
	Line int
	Text string
}
