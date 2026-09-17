package server

import (
	"path/filepath"
	"strings"
)

// agentRuntimeBashDownloadPreflight keeps public file acquisition on the one
// durable download authority. It is intentionally command-generic: no host,
// dataset, filename, or scientific domain is recognized here. Ordinary Bash
// computation remains available, while file-transfer clients that would own
// their own non-durable lifecycle are redirected before execution.
func agentRuntimeBashDownloadPreflight(publicName string, input map[string]any) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(publicName), "bash") {
		return nil
	}
	command := strings.TrimSpace(stringValue(input["command"]))
	if command == "" || !agentRuntimeBashUsesFileTransferClient(command) {
		return nil
	}
	return map[string]any{
		"ok": false, "status": "durable_download_preflight_required", "executed": false,
		"message":  "This shell command would start a second, non-durable file-download lifecycle and was deferred before execution.",
		"recovery": "Use the exact URL from the completed source result with download_public_scientific_file. That single path preserves partial bytes across cancellation and service restart, validates Range/If-Range and content identity, and publishes the completed file atomically. Use Bash only after the durable download returns the workspace input path.",
	}
}

func agentRuntimeBashUsesFileTransferClient(command string) bool {
	for _, segment := range agentRuntimeBashCommandSegments(command) {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		index := 0
		for index < len(fields) && strings.Contains(fields[index], "=") && !strings.HasPrefix(fields[index], "-") {
			index++
		}
		if index < len(fields) && strings.EqualFold(filepath.Base(strings.Trim(fields[index], `"'`)), "env") {
			index++
			for index < len(fields) && strings.Contains(fields[index], "=") && !strings.HasPrefix(fields[index], "-") {
				index++
			}
		}
		if index < len(fields) && strings.EqualFold(filepath.Base(strings.Trim(fields[index], `"'`)), "command") {
			index++
		}
		if index >= len(fields) {
			continue
		}
		program := strings.ToLower(filepath.Base(strings.Trim(fields[index], `"'`)))
		switch program {
		case "wget", "wget2", "aria2c", "axel":
			return true
		case "curl":
			for _, field := range fields[index+1:] {
				field = strings.Trim(field, `"'`)
				if field == "-o" || field == "-O" || field == "--output" || field == "--remote-name" ||
					strings.HasPrefix(field, "--output=") || strings.HasPrefix(field, "--remote-name-all") {
					return true
				}
			}
		}
	}
	return false
}

func agentRuntimeBashCommandSegments(command string) []string {
	segments := make([]string, 0, 4)
	start := 0
	var quote rune
	escaped := false
	for index, character := range command {
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == '\n' || character == ';' || character == '&' || character == '|' || character == '(' || character == ')' {
			if segment := strings.TrimSpace(command[start:index]); segment != "" {
				segments = append(segments, segment)
			}
			start = index + len(string(character))
		}
	}
	if segment := strings.TrimSpace(command[start:]); segment != "" {
		segments = append(segments, segment)
	}
	return segments
}
