package server

import (
	"bytes"
	"context"

	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"net/http"

	"os"

	"path/filepath"

	"sort"

	"strings"

	"time"
	"unicode/utf8"

	researchartifacts "synon-go/internal/artifacts"

	sessionstore "synon-go/internal/persistence/sessions"
)

const artifactRuntimeNamespace = "artifacts"

const structuredOutputRuntimeNamespace = "structured-output"

const maxArtifactContentBytes = 64 * 1024

func (s *Server) executeSessionTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.sessionStore == nil || s.eventJournal == nil {
		return nil, errors.New("session store is not configured")
	}
	switch toolName {
	case "session_store":
		sessionID := stringValue(input["sessionId"])
		session := sessionstore.Session{
			ID:      sessionID,
			Title:   stringValue(input["title"]),
			WorkDir: stringValue(input["workDir"]),
		}
		if session.Title == "" {
			session.Title = sessionID
		}
		if err := s.sessionStore.Upsert(session); err != nil {
			return nil, err
		}
		stored, ok, err := s.sessionStore.Get(sessionID)
		if err == nil && !ok {
			err = fmt.Errorf("session not found after store: %s", sessionID)
		}
		return map[string]any{"session": stored, "stored": ok}, err
	case "session_list":
		sessions, err := s.sessionStore.List()
		return map[string]any{"sessions": sessions}, err
	case "session_get":
		session, ok, err := s.sessionStore.Get(stringValue(input["sessionId"]))
		if err == nil && !ok {
			err = fmt.Errorf("session not found: %s", stringValue(input["sessionId"]))
		}
		return map[string]any{"session": session, "found": ok}, err
	case "session_replay":
		entries, err := s.eventJournal.ReadAfter(
			stringValue(input["sessionId"]),
			numberValue(input["afterEventId"]),
			sessionReplayLimit(numberValue(input["limit"])),
		)
		return map[string]any{"entries": entries}, err
	case "session_event_journal":
		return s.executeSessionEventJournalTool(input)
	case "session_append":
		event, err := s.appendSessionToolEvent(input)
		delivered := false
		if err == nil && event != nil {
			delivered = s.publishSessionEntry(event)
		}
		return map[string]any{"event": event, "delivered": delivered}, err
	case "session_claim":
		return s.claimSessionRunner(input)
	case "session_heartbeat":
		return s.heartbeatSessionRunner(input)
	case "session_release":
		return s.releaseSessionRunner(input)
	case "session_runner_checkpoint":
		return s.checkpointSessionRunner(input)
	case "session_runner_next":
		return s.nextSessionRunner(input)
	case "session_runner_finish":
		return s.finishSessionRunner(input)
	case "session_runner_pick":
		return s.pickSessionRunner(input)
	case "session_runner_queue":
		return s.sessionStore.RunnerQueueSnapshot()
	case "session_runner_backlog":
		return s.sessionStore.RunnerBacklog(sessionstore.RunnerBacklogOptions{
			Limit:           int(numberValue(input["limit"])),
			IncludeRunning:  boolValue(input["includeRunning"], false),
			IncludeTerminal: boolValue(input["includeTerminal"], false),
			ProjectID:       stringValue(input["projectId"]),
			State:           stringValue(input["state"]),
		})
	case "session_bind_project":
		return s.bindSessionProject(input)
	case "session_fork":
		return s.forkSession(input)
	case "session_rewind":
		return s.rewindSession(input)
	case "session_export":
		return s.exportSession(input)
	default:
		return nil, fmt.Errorf("unknown session tool: %s", toolName)
	}
}

func (s *Server) executeSessionEventJournalTool(input map[string]any) (any, error) {
	operation := strings.TrimSpace(stringValue(input["operation"]))
	if operation == "" {
		operation = "replay"
	}
	switch operation {
	case "append", "write":
		event, err := s.appendSessionToolEvent(input)
		delivered := false
		if err == nil && event != nil {
			delivered = s.publishSessionEntry(event)
		}
		return map[string]any{"operation": "append", "event": event, "delivered": delivered}, err
	case "replay", "read", "list":
		entries, err := s.eventJournal.ReadAfter(
			stringValue(input["sessionId"]),
			numberValue(input["afterEventId"]),
			sessionReplayLimit(numberValue(input["limit"])),
		)
		return map[string]any{"operation": "replay", "entries": entries}, err
	default:
		return nil, fmt.Errorf("unsupported session_event_journal operation: %s", operation)
	}
}

func (s *Server) executeArtifactTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.runtimeStore == nil {
		return nil, errors.New("artifact store is not configured")
	}
	switch toolName {
	case "artifact_register":
		if authority, ok := transcriptArtifactRunFromContext(ctx); ok {
			return s.registerTranscriptArtifact(ctx, authority, input)
		}
		return s.registerArtifact(input)
	case "artifact_list":
		return s.listArtifacts(input)
	case "artifact_get":
		artifactID := strings.TrimSpace(stringValue(input["artifactId"]))
		entry, ok, err := s.runtimeStore.Get(artifactRuntimeNamespace, artifactID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("artifact not found: %s", artifactID)
		}
		artifact := objectMapValue(entry.Value)
		result := map[string]any{"artifact": entry.Value, "found": true}
		content, err := s.artifactContentPayload(artifact)
		if err != nil {
			return nil, err
		}
		for key, value := range content {
			result[key] = value
		}
		return result, nil
	case "artifact_research_audit":
		return s.auditResearchArtifacts(input)
	default:
		return nil, fmt.Errorf("unknown artifact tool: %s", toolName)
	}
}

func (s *Server) artifactContentPayload(artifact map[string]any) (map[string]any, error) {
	if len(artifact) == 0 {
		return nil, errors.New("artifact record is invalid")
	}
	resolved, _, err := resolveArtifactPath(s.fileRoot, stringValue(artifact["relativePath"]))
	if err != nil {
		return nil, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	buffer := make([]byte, maxArtifactContentBytes+1)
	n, err := io.ReadFull(file, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	truncated := n > maxArtifactContentBytes
	if truncated {
		n = maxArtifactContentBytes
	}
	data := buffer[:n]
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return nil, err
	}
	contentSha := hex.EncodeToString(hasher.Sum(nil))
	recordSha := strings.TrimSpace(stringValue(artifact["sha256"]))
	matchesRecord := recordSha != "" && recordSha == contentSha
	if _, ok := artifact["sizeBytes"]; ok {
		matchesRecord = matchesRecord && numberValue(artifact["sizeBytes"]) == info.Size()
	}
	encoding := "base64"
	content := base64.StdEncoding.EncodeToString(data)
	if artifactContentIsText(data, stringValue(artifact["mimeType"])) {
		encoding = "utf-8"
		content = string(data)
	}
	return map[string]any{
		"content":              content,
		"encoding":             encoding,
		"contentBytes":         n,
		"contentTruncated":     truncated,
		"contentLimitBytes":    maxArtifactContentBytes,
		"contentSizeBytes":     info.Size(),
		"contentSha256":        contentSha,
		"contentMatchesRecord": matchesRecord,
	}, nil
}

func artifactContentIsText(data []byte, mimeType string) bool {
	lowerMime := strings.ToLower(strings.TrimSpace(mimeType))
	if strings.HasPrefix(lowerMime, "text/") || strings.Contains(lowerMime, "charset=utf-8") || strings.Contains(lowerMime, "json") || strings.Contains(lowerMime, "xml") {
		return utf8.Valid(data)
	}
	return utf8.Valid(data) && !bytes.Contains(data, []byte{0})
}

func (s *Server) registerArtifact(input map[string]any) (map[string]any, error) {
	return s.registerArtifactWithPolicy(input, false)
}

// registerArtifactWithPolicy is the legacy artifact-registration authority.
// The compatibility flag is retained for existing session-export callers;
// JSON and JSONL are valid user artifacts when their content is well formed.
func (s *Server) registerArtifactWithPolicy(input map[string]any, _ bool) (map[string]any, error) {
	resolved, rel, err := resolveArtifactPath(s.fileRoot, stringValue(input["path"]))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("artifact path is a directory: %s", rel)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	if err := validateAgentSavedArtifactJSONBytes(rel, data); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	kind := strings.TrimSpace(stringValue(input["kind"]))
	if kind == "" {
		kind = "file"
	}
	title := strings.TrimSpace(stringValue(input["title"]))
	if title == "" {
		title = filepath.Base(rel)
	}
	artifactID := artifactIDFor(rel, sha)
	artifact := map[string]any{
		"artifactId":   artifactID,
		"kind":         kind,
		"title":        title,
		"description":  strings.TrimSpace(stringValue(input["description"])),
		"relativePath": rel,
		"fileName":     filepath.Base(rel),
		"mimeType":     detectArtifactMIME(data),
		"sizeBytes":    info.Size(),
		"sha256":       sha,
		"sessionId":    strings.TrimSpace(stringValue(input["sessionId"])),
		"runId":        strings.TrimSpace(stringValue(input["runId"])),
		"createdAt":    time.Now().UTC().Format(time.RFC3339Nano),
	}
	if metadata, ok := input["metadata"]; ok && metadata != nil {
		artifact["metadata"] = metadata
	}
	entry, err := s.runtimeStore.Set(artifactRuntimeNamespace, artifactID, artifact)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifact": entry.Value, "stored": true}, nil
}

func (s *Server) listArtifacts(input map[string]any) (map[string]any, error) {
	entries, err := s.runtimeStore.List(artifactRuntimeNamespace)
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(stringValue(input["sessionId"]))
	runID := strings.TrimSpace(stringValue(input["runId"]))
	kind := strings.TrimSpace(stringValue(input["kind"]))
	artifacts := make([]any, 0, len(entries))
	for _, entry := range entries {
		artifact, ok := entry.Value.(map[string]any)
		if !ok {
			continue
		}
		if sessionID != "" && strings.TrimSpace(stringValue(artifact["sessionId"])) != sessionID {
			continue
		}
		if runID != "" && strings.TrimSpace(stringValue(artifact["runId"])) != runID {
			continue
		}
		if kind != "" && strings.TrimSpace(stringValue(artifact["kind"])) != kind {
			continue
		}
		artifacts = append(artifacts, artifact)
	}
	sort.SliceStable(artifacts, func(i, j int) bool {
		left := objectMapValue(artifacts[i])
		right := objectMapValue(artifacts[j])
		leftCreated := stringValue(left["createdAt"])
		rightCreated := stringValue(right["createdAt"])
		if leftCreated != rightCreated {
			return leftCreated > rightCreated
		}
		leftPath := stringValue(left["relativePath"])
		rightPath := stringValue(right["relativePath"])
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		return stringValue(left["artifactId"]) < stringValue(right["artifactId"])
	})
	return map[string]any{"artifacts": artifacts, "count": len(artifacts)}, nil
}

func (s *Server) auditResearchArtifacts(input map[string]any) (map[string]any, error) {
	files := stringArrayValue(input["files"])
	source := "input.files"
	if len(files) == 0 {
		listed, err := s.listArtifacts(input)
		if err != nil {
			return nil, err
		}
		rawArtifacts, _ := listed["artifacts"].([]any)
		files = make([]string, 0, len(rawArtifacts))
		for _, raw := range rawArtifacts {
			artifact := objectMapValue(raw)
			relativePath := strings.TrimSpace(stringValue(artifact["relativePath"]))
			if relativePath != "" {
				files = append(files, relativePath)
			}
		}
		source = "artifact_registry"
	}
	audit := researchartifacts.ClassifyResearchArtifacts(files)
	return map[string]any{
		"source":        source,
		"files":         audit.Files,
		"phases":        audit.Phases,
		"finalMarkdown": audit.FinalMarkdown,
		"finalHtml":     audit.FinalHTML,
		"signature":     audit.Signature,
		"fileCount":     len(audit.Files),
	}, nil
}

func artifactIDFor(relativePath string, sha string) string {
	sum := sha256.Sum256([]byte(relativePath + "\x00" + sha))
	encoded := hex.EncodeToString(sum[:])
	return "artifact-" + encoded[:24]
}

func detectArtifactMIME(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	limit := len(data)
	if limit > 512 {
		limit = 512
	}
	return http.DetectContentType(data[:limit])
}

func resolveArtifactPath(root string, requestedPath string) (string, string, error) {
	if strings.TrimSpace(root) == "" {
		return "", "", errors.New("file root is not configured")
	}
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		return "", "", errors.New("artifact_register.path is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	if evaluatedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = evaluatedRoot
	}
	var target string
	if filepath.IsAbs(requestedPath) {
		target, err = filepath.Abs(requestedPath)
	} else {
		target, err = filepath.Abs(filepath.Join(rootAbs, requestedPath))
	}
	if err != nil {
		return "", "", err
	}
	if evaluatedTarget, err := filepath.EvalSymlinks(target); err == nil {
		target = evaluatedTarget
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil {
		return "", "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("artifact path escapes file root: %s", filepath.ToSlash(rel))
	}
	if rel == "" {
		return "", "", errors.New("artifact_register.path must name a file")
	}
	return target, filepath.ToSlash(rel), nil
}

func (s *Server) executeStructuredOutputTool(input map[string]any) (map[string]any, error) {
	if err := s.validateRegisteredTool("StructuredOutput", input); err != nil {
		return nil, err
	}
	structured := input
	value, hasValue := input["value"]
	_, hasSchema := input["schema"]
	wrapperMode := isStructuredOutputWrapperInput(input, hasValue, hasSchema)
	if wrapperMode {
		structured = mapValue(value)
	}
	if schema, ok := input["schema"]; wrapperMode && ok && schema != nil {
		if err := validateStructuredOutputSchema(mapValue(schema), structured); err != nil {
			return nil, fmt.Errorf("StructuredOutput schema mismatch: %w", err)
		}
	}
	result := map[string]any{
		"data":              "Structured output provided successfully",
		"structured_output": structured,
	}
	if s != nil && s.runtimeStore != nil {
		id := structuredOutputID(structured)
		record := map[string]any{
			"structuredOutputId": id,
			"data":               result["data"],
			"structured_output":  structured,
			"sessionId":          strings.TrimSpace(stringValue(input["sessionId"])),
			"runId":              strings.TrimSpace(stringValue(input["runId"])),
			"createdAt":          time.Now().UTC().Format(time.RFC3339Nano),
		}
		if schema, ok := input["schema"]; wrapperMode && ok && schema != nil {
			record["schema"] = schema
		}
		if _, err := s.runtimeStore.Set(structuredOutputRuntimeNamespace, id, record); err != nil {
			return nil, err
		}
		result["structuredOutputId"] = id
		result["stored"] = true
	} else {
		result["stored"] = false
	}
	return result, nil
}

func isStructuredOutputWrapperInput(input map[string]any, hasValue bool, hasSchema bool) bool {
	if !hasValue || !hasSchema {
		return false
	}
	for key := range input {
		switch key {
		case "schema", "value", "sessionId", "runId":
		default:
			return false
		}
	}
	return true
}

func structuredOutputID(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(append(raw, []byte(time.Now().UTC().Format(time.RFC3339Nano))...))
	return "structured-" + hex.EncodeToString(sum[:])[:24]
}

func validateStructuredOutputSchema(schema map[string]any, value any) error {
	if len(schema) == 0 {
		return nil
	}
	return validateStructuredOutputValue("root", schema, value)
}

func validateStructuredOutputValue(path string, schema map[string]any, value any) error {
	schemaType := strings.TrimSpace(stringValue(schema["type"]))
	if schemaType != "" {
		if err := validateStructuredOutputType(path, schemaType, value); err != nil {
			return err
		}
	}
	if schemaType == "object" || schema["properties"] != nil || schema["required"] != nil {
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object", path)
		}
		for _, required := range stringArrayValue(schema["required"]) {
			if _, ok := object[required]; !ok {
				return fmt.Errorf("%s.%s: required property is missing", path, required)
			}
		}
		properties := mapValue(schema["properties"])
		for key, rawPropertySchema := range properties {
			if propertyValue, ok := object[key]; ok {
				propertySchema := mapValue(rawPropertySchema)
				if len(propertySchema) > 0 {
					if err := validateStructuredOutputValue(path+"."+key, propertySchema, propertyValue); err != nil {
						return err
					}
				}
			}
		}
	}
	if schemaType == "array" || schema["items"] != nil {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array", path)
		}
		itemSchema := mapValue(schema["items"])
		if len(itemSchema) > 0 {
			for index, item := range items {
				if err := validateStructuredOutputValue(fmt.Sprintf("%s[%d]", path, index), itemSchema, item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateStructuredOutputType(path string, schemaType string, value any) error {
	switch schemaType {
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("%s: expected object", path)
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("%s: expected array", path)
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s: expected string", path)
		}
	case "number", "integer":
		switch value.(type) {
		case float64, float32, int, int64, int32, json.Number:
		default:
			return fmt.Errorf("%s: expected number", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s: expected boolean", path)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("%s: expected null", path)
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %q", path, schemaType)
	}
	return nil
}
