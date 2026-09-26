package server

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/agentruntime"
)

const sessionRunnerArtifactPublicationStaleMarker = "artifact_publication_stale_after_later_work"

type sessionRunnerArtifactPublication struct {
	filename    string
	inputPath   string
	contentType string
	message     int
}

// sessionRunnerArtifactPublicationFreshnessFailures rejects only narrative
// deliverables that the final answer actually exposes and that were published
// before later scientific work completed. This is an ordering invariant, not
// a scientific quality reviewer: the provider remains responsible for the
// report content, while the Harness prevents an older draft from becoming the
// authoritative artifact after newer evidence or computation entered the same
// execution unit.
func sessionRunnerArtifactPublicationFreshnessFailures(
	messages []agentruntime.Message,
	finalContent string,
) []string {
	referencedVersions := sessionRunnerFinalArtifactVersionSet(finalContent)
	if len(referencedVersions) == 0 || len(messages) == 0 {
		return nil
	}
	currentExecutionStart := 0
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(messages[index].Role), "user") {
			currentExecutionStart = index
			break
		}
	}
	toolNames := runnerToolNamesByCallID(messages)
	toolCalls := runnerToolCallsByCallID(messages)
	publications := make(map[string]sessionRunnerArtifactPublication)
	for index, message := range messages {
		// A published version from an earlier user turn is an immutable input to
		// the current execution unit. Later work may create a replacement, but it
		// does not make unrelated prior-turn artifacts stale by mere ordering.
		if index < currentExecutionStart {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") ||
			!strings.EqualFold(strings.TrimSpace(toolNames[message.ToolCallID]), "save_artifacts") {
			continue
		}
		for _, artifact := range sessionRunnerSavedArtifactsFromToolContent(message.Content) {
			versionID := strings.TrimSpace(stringValue(artifact["version_id"]))
			if _, referenced := referencedVersions[versionID]; !referenced ||
				!strings.EqualFold(strings.TrimSpace(stringValue(artifact["retention"])), "snapshot") {
				continue
			}
			filename := strings.TrimSpace(stringValue(artifact["filename"]))
			inputPath := strings.TrimSpace(stringValue(artifact["input_path"]))
			if inputPath == "" {
				inputPath = filename
			}
			contentType := strings.TrimSpace(stringValue(artifact["content_type"]))
			if !sessionRunnerNarrativeDeliverable(filename, contentType) {
				continue
			}
			publications[versionID] = sessionRunnerArtifactPublication{
				filename: filename, inputPath: inputPath, contentType: contentType, message: index,
			}
		}
	}
	if len(publications) == 0 {
		return nil
	}
	failures := make([]string, 0, len(publications))
	for _, publication := range publications {
		laterTool := ""
		for index := publication.message + 1; index < len(messages); index++ {
			message := messages[index]
			if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
				continue
			}
			name := strings.TrimSpace(toolNames[message.ToolCallID])
			if sessionRunnerPostPublicationInspectionTool(name) {
				continue
			}
			if _, ok := sessionRunnerSuccessfulToolResult(message.Content); !ok {
				continue
			}
			call, found := toolCalls[message.ToolCallID]
			if found && sessionRunnerDirectFileMutationMissesPublication(call, publication.inputPath) {
				continue
			}
			laterTool = name
		}
		if laterTool == "" {
			continue
		}
		filename := publication.filename
		if filename == "" {
			filename = "referenced-report"
		}
		failures = append(failures, fmt.Sprintf(
			"%s:%s later_tool=%s",
			sessionRunnerArtifactPublicationStaleMarker, filename, laterTool,
		))
	}
	sort.Strings(failures)
	return failures
}

// sessionRunnerDirectFileMutationMissesPublication prevents one companion
// deliverable from invalidating every other immutable version in the same
// completion candidate. Direct file tools carry an exact target path, so their
// freshness effect is path-local. Evidence acquisition and computation remain
// task-wide because their results may change narrative conclusions even when
// they do not directly write a report file.
//
// Unknown or malformed mutation inputs fail closed: without an exact path the
// caller keeps the existing task-wide invalidation behavior.
func sessionRunnerDirectFileMutationMissesPublication(
	call agentruntime.ToolCall,
	publicationFilename string,
) bool {
	switch normalizeAgentToolName(call.Name) {
	case "editfile", "writefile", "filewrite", "write", "edit", "patch",
		"filepatch", "filereplace", "jsonpatch", "notebookedit":
	default:
		return false
	}
	var input map[string]any
	if json.Unmarshal(call.Arguments, &input) != nil {
		return false
	}
	mutationPath := strings.TrimSpace(firstNonEmpty(
		stringValue(input["file_path"]),
		stringValue(input["path"]),
		stringValue(input["filename"]),
	))
	publicationPath := strings.TrimSpace(publicationFilename)
	if mutationPath == "" || publicationPath == "" {
		return false
	}
	return !strings.EqualFold(
		filepath.ToSlash(filepath.Clean(mutationPath)),
		filepath.ToSlash(filepath.Clean(publicationPath)),
	)
}

func sessionRunnerFinalArtifactVersionSet(content string) map[string]struct{} {
	versions := make(map[string]struct{})
	for _, match := range artifactReferencePattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		if versionID := strings.TrimSpace(strings.TrimPrefix(match[1], "art_")); versionID != "" {
			versions[versionID] = struct{}{}
		}
	}
	return versions
}

func sessionRunnerSuccessfulToolResult(content string) (any, bool) {
	var result any
	if strings.TrimSpace(content) == "" || json.Unmarshal([]byte(content), &result) != nil ||
		agentruntime.ToolResultDidNotExecute(result) ||
		agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return nil, false
	}
	return result, true
}

func sessionRunnerSavedArtifactResults(result any) []map[string]any {
	object, ok := result.(map[string]any)
	if !ok {
		return nil
	}
	if nested, nestedOK := object["result"].(map[string]any); nestedOK {
		object = nested
	}
	values, _ := object["artifacts"].([]any)
	artifacts := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if artifact, ok := value.(map[string]any); ok {
			artifacts = append(artifacts, artifact)
		}
	}
	return artifacts
}

func sessionRunnerNarrativeDeliverable(filename, contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch contentType {
	case "text/markdown", "text/html", "application/pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return true
	}
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(filename))) {
	case ".md", ".markdown", ".html", ".htm", ".pdf", ".docx":
		return true
	default:
		return false
	}
}

// Inspection-only operations may follow publication without changing the
// scientific basis of a report. Every other successful tool result proves the
// task kept doing substantive work, so a referenced narrative deliverable must
// be reconciled and published again before completion.
func sessionRunnerPostPublicationInspectionTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "save_artifacts", "read_file", "file_read", "read", "readbatch",
		"file_read_batch", "file_info", "file_list", "file_search",
		"artifact_get", "update_step_status", "skill", "search_skills", "skill_search":
		return true
	default:
		return false
	}
}
