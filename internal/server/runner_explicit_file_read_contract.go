package server

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
)

var (
	sessionRunnerExplicitPostWriteReadPatterns = []*regexp.Regexp{
		regexp.MustCompile(
			`(?i)(?:重新|再次|重读|回读|复读|再(?:次)?(?:读取|查看)|` +
				`re-?read|reread|read\s+(?:it\s+)?back|read\s+(?:the\s+)?(?:file\s+)?again|` +
				`reopen(?:\s+it)?(?:\s+and)?\s+read)` +
				`[^。！？.!?\n]{0,120}\b(` + sessionRunnerExplicitDeliverableFilenameExpression + `)\b`,
		),
		regexp.MustCompile(
			`(?i)\bread\s+(?:the\s+)?(?:file\s+)?\b(` + sessionRunnerExplicitDeliverableFilenameExpression + `)\b\s+back\b`,
		),
	}
	sessionRunnerExplicitPostWriteReadNegationPattern = regexp.MustCompile(
		`(?i)(?:不要|无需|不必|禁止|do\s+not|don't|without)[^。！？.!?\n]{0,32}$`,
	)
)

// Explicit post-write reads are user-required execution evidence. They are
// narrower than generic words such as inspect or verify: only an unambiguous
// re-read/read-back request tied to a concrete task-relative filename creates
// this deterministic completion obligation.
func sessionRunnerExplicitPostWriteReadTargets(taskIntent string) []string {
	seen := map[string]struct{}{}
	for _, pattern := range sessionRunnerExplicitPostWriteReadPatterns {
		for _, match := range pattern.FindAllStringSubmatchIndex(taskIntent, -1) {
			if len(match) != 4 || match[2] < 0 || match[3] <= match[2] {
				continue
			}
			clauseStart := sessionRunnerDeliverableClauseStart(taskIntent[:match[0]])
			if sessionRunnerExplicitPostWriteReadNegationPattern.MatchString(taskIntent[clauseStart:match[0]]) {
				continue
			}
			name := sessionRunnerExplicitFileTarget(taskIntent[match[2]:match[3]])
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	targets := make([]string, 0, len(seen))
	for target := range seen {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets
}

type sessionRunnerExplicitFileCall struct {
	name    string
	targets []string
}

func sessionRunnerExplicitPostWriteReadGaps(targets []string, messages []agentruntime.Message) []string {
	if len(targets) == 0 {
		return nil
	}
	calls := map[string]sessionRunnerExplicitFileCall{}
	lastWrite := map[string]int{}
	lastRead := map[string]int{}
	for index, message := range messages {
		for _, call := range message.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			if callID == "" {
				continue
			}
			var input map[string]any
			if json.Unmarshal(call.Arguments, &input) != nil {
				continue
			}
			calls[callID] = sessionRunnerExplicitFileCall{
				name:    normalizeAgentToolName(call.Name),
				targets: sessionRunnerExplicitToolFileTargets(call.Name, input, nil),
			}
		}
		if message.Role != "tool" || strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		call, found := calls[strings.TrimSpace(message.ToolCallID)]
		if !found {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil ||
			agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded ||
			agentruntime.IsNonExecutingPreflight(result) {
			continue
		}
		callTargets := append([]string(nil), call.targets...)
		callTargets = append(callTargets, sessionRunnerExplicitToolFileTargets(call.name, nil, mapValue(result))...)
		callTargets = uniqueSessionRunnerExplicitFileTargets(callTargets)
		switch call.name {
		case "editfile", "filewrite", "write", "filepatch", "filereplace", "patch", "saveartifacts", "artifactregister":
			for _, target := range callTargets {
				lastWrite[target] = index
			}
		case "readfile", "fileread":
			for _, target := range callTargets {
				lastRead[target] = index
			}
		}
	}
	gaps := make([]string, 0, len(targets))
	for _, target := range targets {
		writeIndex, wrote := lastWrite[target]
		readIndex, read := lastRead[target]
		if wrote && read && readIndex > writeIndex {
			continue
		}
		gaps = append(gaps, fmt.Sprintf(
			"the explicitly required post-write read of %s has no successful read_file receipt after the latest successful edit or save",
			target,
		))
	}
	return gaps
}

func sessionRunnerExplicitToolFileTargets(toolName string, input, result map[string]any) []string {
	name := normalizeAgentToolName(toolName)
	targets := []string{}
	var add func(any)
	add = func(value any) {
		switch typed := value.(type) {
		case string:
			if target := sessionRunnerExplicitFileTarget(typed); target != "" {
				targets = append(targets, target)
			}
		case []any:
			for _, item := range typed {
				add(item)
			}
		case []string:
			for _, item := range typed {
				add(item)
			}
		}
	}
	for _, record := range []map[string]any{input, result} {
		if record == nil {
			continue
		}
		for _, key := range []string{"file_path", "path", "input_path", "filename", "name"} {
			add(record[key])
		}
		if name == "saveartifacts" || name == "artifactregister" {
			add(record["files"])
			for _, raw := range anySliceValue(record["artifacts"]) {
				artifact := mapValue(raw)
				add(artifact["input_path"])
				add(artifact["filename"])
			}
		}
	}
	return uniqueSessionRunnerExplicitFileTargets(targets)
}

func sessionRunnerExplicitFileTarget(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	if value == "" || strings.Contains(value, "://") {
		return ""
	}
	base := strings.ToLower(filepath.Base(value))
	if base == "." || base == ".." ||
		!strings.EqualFold(sessionRunnerExplicitDeliverableNamePattern.FindString(base), base) {
		return ""
	}
	return base
}

func uniqueSessionRunnerExplicitFileTargets(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = sessionRunnerExplicitFileTarget(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
