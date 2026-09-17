package server

import (
	"context"

	"crypto/sha256"

	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"net/http"

	"os"

	"path/filepath"
	"regexp"

	"strconv"
	"strings"
	"unicode/utf8"

	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/harnesscontract"

	"synon-go/internal/skills"

	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/fileops"
	"synon-go/internal/tools/lspstatic"

	"synon-go/internal/tools/notebookedit"
	"synon-go/internal/tools/registry"
)

type fileReadSnapshot struct {
	Size        int64
	ModUnixNano int64
	SHA256      string
	Partial     bool
}

const toolGatewayAuditNamespace = "tool-gateway-audit"

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/tools")
	if tail == "" || tail == "/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Tool listing only supports GET")
			return
		}
		tools := make([]registry.Tool, 0)
		toolSurfaces := make(map[string]harnesscontract.ToolSurface)
		surfaceCounts := make(map[harnesscontract.ToolSurface]int)
		registeredNames := map[string]struct{}{}
		for _, name := range s.tools.Names() {
			tool, _ := s.tools.Get(name)
			tools = append(tools, tool)
			surface := harnesscontract.ClassifyToolSurface(name)
			toolSurfaces[name] = surface
			surfaceCounts[surface]++
			registeredNames[name] = struct{}{}
		}
		modelRootTools := harnesscontract.RootModelTools()
		registeredModelRootTools := make([]string, 0, len(modelRootTools))
		runtimeInjectedModelRootTools := make([]string, 0, len(modelRootTools))
		for _, name := range modelRootTools {
			if _, registered := registeredNames[name]; registered {
				registeredModelRootTools = append(registeredModelRootTools, name)
			} else {
				runtimeInjectedModelRootTools = append(runtimeInjectedModelRootTools, name)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tools": tools, "tool_surfaces": toolSurfaces, "surface_counts": surfaceCounts,
			"catalog_scope":           "statically registered model tools only; task-scoped tools are injected by their owning runtime, while service operations and inbound compatibility aliases remain outside the model catalog",
			"registered_tool_count":   len(tools),
			"service_operation_count": len(s.registeredOperationNames()),
			"inbound_aliases":         toolcontract.RuntimeAliases(),
			"model_root_tools":        modelRootTools, "fixed_job_tools": harnesscontract.FixedJobModelTools(),
			"registered_model_root_tools":       registeredModelRootTools,
			"runtime_injected_model_root_tools": runtimeInjectedModelRootTools,
		})
		return
	}

	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) == 2 && parts[1] == "execute" {
		if !s.requireLegacySessionSurface(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Tool execution only supports POST")
			return
		}
		var body struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
			return
		}
		response, err := s.executeDirectToolGatewayResponse(r.Context(), parts[0], body.Input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	writeError(w, http.StatusNotFound, "NOT_FOUND", "Unknown tool route")
}

func (s *Server) executeToolResponse(ctx context.Context, toolName string, input map[string]any) (map[string]any, error) {
	canonical, err := canonicalRuntimeToolName(toolName)
	if err != nil {
		return nil, err
	}
	toolName = canonical
	if input == nil {
		input = map[string]any{}
	}
	if toolName == "synon_link" {
		commandID, result, err := s.executeSynonLinkTool(ctx, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "commandId": commandID, "result": result}, nil
	}
	result, err := s.executeToolResult(ctx, toolName, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "result": result}, nil
}

func (s *Server) executeDirectToolGatewayResponse(ctx context.Context, toolName string, input map[string]any) (map[string]any, error) {
	requestedToolName := toolName
	canonical, err := canonicalRuntimeToolName(toolName)
	if err != nil {
		return nil, err
	}
	if input == nil {
		input = map[string]any{}
	}
	started := time.Now().UTC()
	receipt := s.executeExactToolGateway(
		ctx, "http", "", directToolGatewayCallID(canonical, input, started), requestedToolName, input,
		exactServerToolGatewayOptions{PermissionSource: "direct-http", DirectExecutor: true},
	)
	if receipt.Err != nil {
		return nil, receipt.Err
	}
	if receipt.BadRequest {
		return nil, directToolGatewayFailure(receipt.Value)
	}
	if response, ok := receipt.Value.(map[string]any); ok {
		return response, nil
	}
	return map[string]any{"ok": true, "result": receipt.Value}, nil
}

func directToolGatewayFailure(value any) error {
	object, _ := value.(map[string]any)
	message := strings.TrimSpace(firstNonEmpty(stringValue(object["error"]), stringValue(object["message"])))
	if message == "" {
		message = "tool execution failed"
	}
	if stringValue(object["code"]) != "invalid_tool_arguments" {
		return errors.New(message)
	}
	details := make([]string, 0)
	for _, raw := range anySliceValue(object["issues"]) {
		issue := objectMapValue(raw)
		path := strings.TrimSpace(stringValue(issue["path"]))
		keyword := strings.TrimSpace(stringValue(issue["keyword"]))
		if path == "" && keyword == "" {
			continue
		}
		details = append(details, strings.TrimSpace(strings.Join([]string{keyword, path}, " ")))
	}
	if len(details) == 0 {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %s", message, strings.Join(details, ", "))
}

func directToolGatewayCallID(toolName string, input map[string]any, started time.Time) string {
	raw, _ := json.Marshal(map[string]any{
		"tool":      strings.TrimSpace(toolName),
		"input":     input,
		"startedAt": started.Format(time.RFC3339Nano),
	})
	sum := sha256.Sum256(raw)
	return "direct-tool-" + hex.EncodeToString(sum[:])[:24]
}

func mustMarshalRawMessage(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func (s *Server) recordToolGatewayAudit(origin string, toolName string, toolCallID string, originalInput map[string]any, input map[string]any, status string, result any, errorMessage string, started time.Time, extra map[string]any) {
	if s == nil || s.runtimeStore == nil {
		return
	}
	origin = strings.TrimSpace(origin)
	if origin == "" {
		origin = "unknown"
	}
	if canonical, ok := toolcontract.CanonicalAskUser(toolName); ok {
		toolName = canonical
	}
	finished := time.Now().UTC()
	if started.IsZero() {
		started = finished
	}
	durationMs := finished.Sub(started).Milliseconds()
	if durationMs < 0 {
		durationMs = 0
	}
	auditID := toolGatewayAuditID(origin, toolCallID, status)
	value := map[string]any{
		"id":             auditID,
		"origin":         origin,
		"tool":           toolName,
		"toolCallId":     toolCallID,
		"status":         status,
		"input":          input,
		"startedAt":      started.Format(time.RFC3339Nano),
		"completedAt":    finished.Format(time.RFC3339Nano),
		"durationMs":     durationMs,
		"durationMillis": durationMs,
	}
	if !mapsEqualAny(originalInput, input) {
		value["originalInput"] = originalInput
		value["inputUpdatedByHook"] = true
	}
	if timeoutMs, source := toolGatewayTimeoutMillis(toolName, input); timeoutMs > 0 {
		value["timeoutMs"] = timeoutMs
		value["timeoutSource"] = source
	}
	for key, item := range extra {
		value[key] = item
	}
	if result != nil {
		value["result"] = result
	}
	if errorMessage != "" {
		value["error"] = errorMessage
	}
	value, _ = boundedToolGatewayAuditMap(value)
	_ = s.persistToolGatewayAudit(auditID, value, finished)
}

func toolGatewayAuditID(origin string, toolCallID string, status string) string {
	raw, _ := json.Marshal(map[string]any{
		"origin":     strings.TrimSpace(origin),
		"toolCallId": strings.TrimSpace(toolCallID),
		"status":     strings.TrimSpace(status),
	})
	sum := sha256.Sum256(raw)
	return "tool-gateway-" + hex.EncodeToString(sum[:])[:24]
}

func toolGatewayTimeoutMillis(toolName string, input map[string]any) (int64, string) {
	if input == nil {
		return 0, ""
	}
	if timeoutMs := firstNumber(input, "timeoutMs", "timeout_ms"); timeoutMs > 0 {
		return int64(timeoutMs), "input.timeout_ms"
	}
	timeout := numberValue(input["timeout"])
	if timeout <= 0 {
		return 0, ""
	}
	switch strings.TrimSpace(toolName) {
	case "TaskOutput":
		return int64(timeout), "input.timeout_ms"
	default:
		return int64(timeout * 1000), "input.timeout_seconds"
	}
}

func mapsEqualAny(left map[string]any, right map[string]any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftRaw) == string(rightRaw)
}

func copyMapAny(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	copied := make(map[string]any, len(input))
	for key, value := range input {
		copied[key] = value
	}
	return copied
}

func (s *Server) executeToolResult(ctx context.Context, toolName string, input map[string]any) (any, error) {
	canonical, err := canonicalRuntimeToolName(toolName)
	if err != nil {
		return nil, err
	}
	toolName = canonical
	switch {
	case strings.HasPrefix(toolName, "file_") || toolName == "code_index" || toolName == "code_references" || toolName == "json_patch" || toolName == "Read" || toolName == "Write" || toolName == "Edit" || toolName == "NotebookEdit" || toolName == "ReadBatch" || toolName == "Patch" || toolName == "Glob" || toolName == "Grep":
		return s.executeFileTool(toolName, input)
	case toolName == toolcontract.SearchSkills:
		return s.executeSkillSearchTool(input)
	case toolName == toolcontract.Skill:
		return s.executeSkillToolWithContext(ctx, input)
	case toolName == "AgentRuntimeDoctor":
		return s.executeAgentRuntimeDoctorTool(input)
	case strings.HasPrefix(toolName, "approval_remembered_"):
		return s.executeRememberedApprovalTool(toolName, input)
	case toolName == "web_research":
		return s.executeWebResearchTool(ctx, input)
	case toolName == "VisualReview":
		return s.executeVisualReviewTool(ctx, input)
	case toolName == "web_search":
		return s.executeWebSearchTool(ctx, input)
	case toolName == "binding_mode_analysis":
		return s.executeRegisteredTool(ctx, toolName, input)
	case toolName == "LSP":
		return s.executeLSPTool(ctx, input)
	case toolName == "SendUserMessage":
		return s.executeBriefTool(toolName, input)
	case toolName == "SendMessage":
		return s.executeSendMessageTool(ctx, input)
	case toolName == "ToolDoctor":
		return s.executeToolDoctorTool(input)
	case toolName == "im_config":
		return s.executeIMConfigTool(input)
	case toolName == "ListMcpTools" || toolName == "ListMcpResourcesTool" || toolName == "ReadMcpResourceTool" || toolName == "MCPTool" || strings.HasPrefix(toolName, "mcp__"):
		return s.executeMCPTool(ctx, toolName, input)
	case toolName == "shell_exec" || toolName == "Bash" || toolName == "Shell" || toolName == "powershell":
		return s.executeShellTool(ctx, toolName, input)
	case toolName == "Sleep":
		return s.executeSleepTool(ctx, toolName, input)
	case toolName == toolcontract.AskUser:
		return s.executeAskUserQuestionTool(toolName, input)
	case toolName == "TaskOutput":
		return s.executeTaskOutputTool(ctx, toolName, input)
	case toolName == "TaskStop":
		return s.executeTaskStopTool(toolName, input)
	case toolName == "Agent":
		return s.executeAgentTool(ctx, toolName, input)
	case toolName == "CronCreate" || toolName == "CronUpdate" || toolName == "CronList" || toolName == "CronDelete" || toolName == "cron_tick":
		return s.executeCronTool(toolName, input)
	case toolName == "TeamCreate" || toolName == "TeamDelete":
		return s.executeTeamTool(toolName, input)
	case toolName == "TaskRun":
		return s.executeTaskRunTool(input)
	case toolName == "StructuredOutput":
		return s.executeStructuredOutputTool(input)
	case toolName == "EnterWorktree" || toolName == "ExitWorktree":
		return s.executeWorktreeTool(ctx, toolName, input)
	case strings.HasPrefix(toolName, "artifact_"):
		return s.executeArtifactTool(ctx, toolName, input)
	case strings.HasPrefix(toolName, "task_") || toolName == "TaskCreate" || toolName == "TaskGet" || toolName == "TaskList" || toolName == "TaskUpdate":
		return s.executeTaskTool(toolName, input)
	case toolName == "TodoWrite":
		return s.executeTodoWriteTool(toolName, input)
	case strings.HasPrefix(toolName, "settings_") || toolName == "Config":
		return s.executeSettingsTool(toolName, input)
	case toolName == "Compact":
		return s.executeCompactTool(ctx, input)
	case toolName == "im_message":
		return s.executeIMMessageTool(ctx, input)
	case strings.HasPrefix(toolName, "pairing_"):
		return s.executePairingTool(ctx, toolName, input)
	case strings.HasPrefix(toolName, "session_"):
		return s.executeSessionTool(toolName, input)
	case strings.HasPrefix(toolName, "runtime_"):
		return s.executeRuntimeTool(toolName, input)
	default:
		return s.executeRegisteredTool(ctx, toolName, input)
	}
}

func (s *Server) executeFileTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	switch toolName {
	case "file_list":
		return fileops.List(s.fileRoot, stringValue(input["path"]))
	case "file_info":
		return fileops.Info(s.fileRoot, stringValue(input["path"]))
	case "file_read":
		result, err := fileops.Read(s.fileRoot, stringValue(input["path"]), numberValue(input["limit"]), stringValue(input["encoding"]))
		if err == nil {
			_ = s.recordFileReadState(stringValue(input["path"]), result.Truncated)
		}
		return result, err
	case "Read":
		result, err := fileops.OriginalRead(s.fileRoot, stringValue(input["file_path"]), numberValue(input["offset"]), numberValue(input["limit"]))
		if err == nil {
			partial := result.Type == "text" && (result.File.StartLine > 1 || result.File.NumLines < result.File.TotalLines)
			_ = s.recordFileReadState(stringValue(input["file_path"]), partial)
		}
		return result, err
	case "ReadBatch":
		return s.executeFileReadBatchWithState(input)
	case "file_write":
		return fileops.Write(s.fileRoot, stringValue(input["path"]), stringValue(input["content"]), stringValue(input["encoding"]), boolValue(input["overwrite"], true))
	case "Write":
		filePath := stringValue(input["file_path"])
		if !boolValue(input["_taskrun_system"], false) {
			if err := s.requireFreshReadStateIfExists(filePath); err != nil {
				return nil, err
			}
		}
		result, err := fileops.OriginalWrite(s.fileRoot, filePath, stringValue(input["content"]))
		if err == nil {
			_ = s.recordFileReadState(filePath, false)
		}
		return result, err
	case "Edit":
		filePath := stringValue(input["file_path"])
		if !boolValue(input["_taskrun_system"], false) {
			if err := s.requireFreshReadStateIfExists(filePath); err != nil {
				return nil, err
			}
		}
		result, err := fileops.OriginalEdit(s.fileRoot, filePath, stringValue(input["old_string"]), stringValue(input["new_string"]), boolValue(input["replace_all"], false))
		if err == nil {
			_ = s.recordFileReadState(filePath, false)
		}
		return result, err
	case "NotebookEdit":
		if !boolValue(input["_taskrun_system"], false) {
			if err := s.requireFreshReadState(stringValue(input["notebook_path"])); err != nil {
				return nil, err
			}
		}
		result, err := notebookedit.Run(s.fileRoot, notebookedit.Input{
			NotebookPath: stringValue(input["notebook_path"]),
			CellID:       stringValue(input["cell_id"]),
			NewSource:    stringValue(input["new_source"]),
			CellType:     stringValue(input["cell_type"]),
			EditMode:     stringValue(input["edit_mode"]),
		})
		if err == nil && strings.TrimSpace(result.Error) == "" {
			_ = s.recordFileReadState(stringValue(input["notebook_path"]), false)
		}
		return result, err
	case "file_mkdir":
		return fileops.Mkdir(s.fileRoot, stringValue(input["path"]), boolValue(input["recursive"], true))
	case "file_copy":
		return fileops.Copy(s.fileRoot, stringValue(input["path"]), stringValue(input["targetPath"]), boolValue(input["overwrite"], false), boolValue(input["recursive"], false))
	case "file_move":
		return fileops.Move(s.fileRoot, stringValue(input["path"]), stringValue(input["targetPath"]), boolValue(input["overwrite"], false))
	case "file_delete":
		return fileops.Delete(s.fileRoot, stringValue(input["path"]), boolValue(input["recursive"], false))
	case "file_search":
		return fileops.Search(s.fileRoot, stringValue(input["path"]), stringValue(input["query"]), numberValue(input["limit"]))
	case "Glob":
		return fileops.Glob(s.fileRoot, stringValue(input["path"]), stringValue(input["pattern"]), numberValue(input["limit"]))
	case "Grep":
		return fileops.Grep(s.fileRoot, stringValue(input["path"]), stringValue(input["pattern"]), grepOptionsValue(input))
	case "file_replace":
		return fileops.Replace(s.fileRoot, stringValue(input["path"]), stringValue(input["old"]), stringValue(input["new"]))
	case "file_patch":
		return fileops.Patch(s.fileRoot, stringValue(input["path"]), patchOperationsValue(input["operations"]))
	case "Patch":
		return fileops.OriginalPatch(s.fileRoot, stringValue(input["patch"]), boolValue(input["dry_run"], false))
	case "json_patch":
		return fileops.JSONPatch(s.fileRoot, stringValue(input["path"]), jsonPatchOperationsValue(input["operations"]))
	case "code_index":
		return fileops.CodeIndex(s.fileRoot, stringValue(input["path"]), codeIndexOptionsValue(input))
	case "code_references":
		return fileops.CodeReferences(s.fileRoot, stringValue(input["path"]), stringValue(input["symbol"]), codeReferencesOptionsValue(input))
	default:
		return nil, fmt.Errorf("unknown file tool: %s", toolName)
	}
}

func (s *Server) executeFileReadBatch(input map[string]any) (any, error) {
	filePaths := stringArrayValue(input["file_paths"])
	if len(filePaths) == 0 {
		filePaths = stringArrayValue(input["filePaths"])
	}
	maxBytesPerFile := numberValue(input["max_bytes_per_file"])
	if maxBytesPerFile == 0 {
		maxBytesPerFile = numberValue(input["maxBytesPerFile"])
	}
	maxFiles := numberValue(input["max_files"])
	if maxFiles == 0 {
		maxFiles = numberValue(input["maxFiles"])
	}
	return fileops.ReadBatch(s.fileRoot, filePaths, maxBytesPerFile, maxFiles)
}

func (s *Server) executeFileReadBatchWithState(input map[string]any) (any, error) {
	filePaths := stringArrayValue(input["file_paths"])
	if len(filePaths) == 0 {
		filePaths = stringArrayValue(input["filePaths"])
	}
	result, err := s.executeFileReadBatch(input)
	if err == nil {
		partial := numberValue(input["max_bytes_per_file"]) > 0 || numberValue(input["maxBytesPerFile"]) > 0
		for _, path := range filePaths {
			_ = s.recordFileReadState(path, partial)
		}
	}
	return result, err
}

func (s *Server) recordFileReadState(requested string, partial bool) error {
	resolved, err := s.resolveReadStatePath(requested)
	if err != nil {
		return err
	}
	snapshot, err := snapshotFileForReadState(resolved)
	if err != nil {
		return err
	}
	s.readStateMu.Lock()
	defer s.readStateMu.Unlock()
	if s.readState == nil {
		s.readState = map[string]fileReadSnapshot{}
	}
	snapshot.Partial = partial
	s.readState[resolved] = snapshot
	return nil
}

func (s *Server) requireFreshReadStateIfExists(requested string) error {
	resolved, err := s.resolveReadStatePath(requested)
	if err != nil {
		return err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("file read state path is a directory: %s", resolved)
	}
	return s.requireFreshReadStateResolved(resolved)
}

func (s *Server) requireFreshReadState(requested string) error {
	resolved, err := s.resolveReadStatePath(requested)
	if err != nil {
		return err
	}
	return s.requireFreshReadStateResolved(resolved)
}

func (s *Server) requireFreshReadStateResolved(resolved string) error {
	current, err := snapshotFileForReadState(resolved)
	if err != nil {
		return err
	}
	s.readStateMu.Lock()
	previous, ok := s.readState[resolved]
	s.readStateMu.Unlock()
	if !ok || previous.Partial {
		return errors.New("File has not been read yet. Read it first before writing to it.")
	}
	if previous.Size != current.Size || previous.ModUnixNano != current.ModUnixNano || previous.SHA256 != current.SHA256 {
		return errors.New("File has been modified since read, either by the user or by a linter. Read it again before attempting to write it.")
	}
	return nil
}

func (s *Server) resolveReadStatePath(requested string) (string, error) {
	root := strings.TrimSpace(s.fileRoot)
	if root == "" {
		return "", errors.New("file root is not configured")
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", errors.New("file path is required")
	}
	if strings.HasPrefix(requested, `\`) || strings.HasPrefix(requested, "//") {
		return "", errors.New("UNC file paths are not allowed")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if evaluatedRoot, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = evaluatedRoot
	}
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootAbs, target)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if evaluatedTarget, err := filepath.EvalSymlinks(targetAbs); err == nil {
		targetAbs = evaluatedTarget
	}
	if err := ensurePathWithinRoot(rootAbs, targetAbs, "file read state"); err != nil {
		return "", err
	}
	return targetAbs, nil
}

func snapshotFileForReadState(path string) (fileReadSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileReadSnapshot{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileReadSnapshot{}, err
	}
	if info.IsDir() {
		return fileReadSnapshot{}, fmt.Errorf("file read state path is a directory: %s", path)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return fileReadSnapshot{}, err
	}
	return fileReadSnapshot{Size: info.Size(), ModUnixNano: info.ModTime().UnixNano(), SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func (s *Server) executeSkillTool(input map[string]any, toolAuthorities ...map[string]struct{}) (any, error) {
	return s.executeSkillToolWithContext(context.Background(), input, toolAuthorities...)
}

func (s *Server) executeSkillToolWithContext(ctx context.Context, input map[string]any, toolAuthorities ...map[string]struct{}) (any, error) {
	return s.executeSkillToolWithRuntimeSkills(ctx, input, nil, toolAuthorities...)
}

func (s *Server) executeSkillToolWithRuntimeSkills(
	ctx context.Context,
	input map[string]any,
	runtimeSkills []skills.Skill,
	toolAuthorities ...map[string]struct{},
) (any, error) {
	return s.executeSkillToolWithRuntimeSkillsAndPolicy(
		ctx, input, runtimeSkills, runtimeSkillPolicyAuthority{}, toolAuthorities...,
	)
}

func (s *Server) executeSkillToolWithRuntimeSkillsAndPolicy(
	ctx context.Context,
	input map[string]any,
	runtimeSkills []skills.Skill,
	policy runtimeSkillPolicyAuthority,
	toolAuthorities ...map[string]struct{},
) (any, error) {
	if err := s.validateRegisteredTool(toolcontract.Skill, input); err != nil {
		return nil, err
	}
	if s.skillCatalog == nil {
		return nil, errors.New("skill catalog is not configured")
	}
	rawName := strings.TrimSpace(stringValue(input["skill"]))
	if rawName == "" {
		return nil, fmt.Errorf("Invalid skill format: %s", rawName)
	}
	name := strings.TrimPrefix(rawName, "/")
	skill, ok := findCatalogSkill(s.skillCatalog, name)
	if !ok {
		for _, candidate := range runtimeSkills {
			if strings.EqualFold(strings.TrimSpace(candidate.Name), name) {
				skill, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		if run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun); run != nil {
			query := strings.ReplaceAll(name, "-", " ")
			return map[string]any{
				"success": false, "ok": false, "executed": false, "status": "skill_not_found",
				"message":  "The requested Skill is not present in the active catalog: " + name,
				"recovery": "Use search_skills with the exact task and tool terms, then load one returned Skill name verbatim. Do not invent a Skill from an MCP server or method label.",
				"matches":  s.skillCatalog.SearchNames(query, 8),
			}, nil
		}
		return nil, fmt.Errorf("Unknown skill: %s", name)
	}
	if !s.runtimeSkillEnabled(skill.Name) {
		return nil, fmt.Errorf("Skill %s is disabled", skill.Name)
	}
	if len(toolAuthorities) > 0 && !s.agentRuntimeSkillToolsAvailable(skill, toolAuthorities[0]) {
		return nil, errors.New("skill requires tools unavailable to this agent runtime")
	}
	var toolAuthority map[string]struct{}
	if len(toolAuthorities) > 0 {
		toolAuthority = toolAuthorities[0]
	}
	composedSkills, err := s.expandRuntimeSkillDependencies(
		[]skills.Skill{skill}, policy.ExcludedNames, policy.AllowedNames, policy.Restrict, toolAuthority, runtimeSkills,
	)
	if err != nil {
		return nil, err
	}
	effectiveSkills := append([]skills.Skill(nil), composedSkills...)
	effectiveSkill := effectiveSkills[len(effectiveSkills)-1]
	filter := strings.TrimSpace(stringValue(input["filter"]))
	args := stringValue(input["args"])
	loadedNames := make([]string, 0, len(effectiveSkills))
	for _, composed := range effectiveSkills {
		loadedNames = append(loadedNames, composed.Name)
	}
	invocationKey := runtimeSkillInvocationKey(effectiveSkill.Name, args, filter)
	activeRun, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if activeRun != nil {
		if activeRun.hasExecutedSkillInvocationKey(invocationKey) {
			return repeatedRuntimeSkillInvocationResult(effectiveSkill.Name, loadedNames), nil
		}
		for index, composed := range effectiveSkills {
			materialized, err := s.materializeAgentSkillRuntime(ctx, activeRun.SessionID, composed)
			if err != nil {
				return nil, fmt.Errorf("materialize skill runtime %q: %w", composed.Name, err)
			}
			effectiveSkills[index] = materialized
		}
		effectiveSkill = effectiveSkills[len(effectiveSkills)-1]
	}
	if filter != "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(skill.Name)), "mcp-") {
		if filtered, found := runtimeMCPConnectorFilterRenderedBody(effectiveSkill.Body, filter); found {
			effectiveSkill.Body = filtered
			effectiveSkill.BodyHash = serverStringHash(filtered)
			effectiveSkills[len(effectiveSkills)-1] = effectiveSkill
		}
	}
	prompt, err := renderComposedSkillPrompt(effectiveSkills, effectiveSkill.Name, args)
	if err != nil {
		return nil, err
	}
	if activeRun != nil {
		// Only a fully materialized and rendered closure becomes execution
		// authority. Recording every dependency and the exact invocation key
		// makes continuation restore the complete methodology without reloading
		// the same body in a model loop.
		activeRun.addExecutedSkillNames(loadedNames...)
		activeRun.addExecutedSkillInvocationKeys(invocationKey)
		for _, composed := range effectiveSkills {
			activeRun.addRequiredScientificCapabilities(composed.RequiredCapabilities...)
		}
	}
	allowedTools := make([]string, 0)
	requiredCapabilities := make([]string, 0)
	executionAssets := make([]string, 0)
	for _, composed := range effectiveSkills {
		allowedTools = append(allowedTools, composed.Tools...)
		requiredCapabilities = append(requiredCapabilities, composed.RequiredCapabilities...)
		executionAssets = append(executionAssets, renderedSkillExecutionAssets(composed)...)
	}
	allowedTools = uniqueSortedFolded(allowedTools)
	requiredCapabilities = uniqueSortedFolded(requiredCapabilities)
	executionAssets = uniqueSortedFolded(executionAssets)
	data := map[string]any{
		"success":                  true,
		"loaded":                   true,
		"executed":                 false,
		"commandName":              skill.Name,
		"loadedSkills":             loadedNames,
		"requiredSkills":           append([]string(nil), skill.RequiredSkills...),
		"allowedTools":             allowedTools,
		"requiredCapabilities":     requiredCapabilities,
		"status":                   "inline",
		"preferredExecutionAssets": executionAssets,
		"nextAction":               "Follow the loaded contract with one of allowedTools. Do not call Skill again to execute a command.",
	}
	result := map[string]any{
		"data": data,
		"skill": map[string]any{
			"name":           skill.Name,
			"description":    skill.Description,
			"tags":           skill.Tags,
			"keywords":       skill.Keywords,
			"tools":          skill.Tools,
			"arguments":      skill.Arguments,
			"references":     skill.References,
			"requiredSkills": append([]string(nil), skill.RequiredSkills...),
			"path":           effectiveSkill.Path,
			// The model receives one authoritative rendered contract. Returning the
			// source body here as well would reintroduce unresolved argument or Skill
			// directory placeholders beside the corrected prompt.
			"body":                     prompt,
			"body_hash":                serverStringHash(prompt),
			"source_body_hash":         skill.BodyHash,
			"requiredCapabilities":     append([]string(nil), skill.RequiredCapabilities...),
			"preferredExecutionAssets": executionAssets,
		},
		"args":   args,
		"filter": filter,
		"prompt": prompt,
	}
	if s.runtimeStore != nil {
		invocationID := skillInvocationID(skill.Name, args, time.Now().UTC())
		persistCtx := ctx
		if persistCtx == nil {
			persistCtx = context.Background()
		}
		persistCtx, cancelPersist := context.WithTimeout(persistCtx, 2*time.Second)
		defer cancelPersist()
		entry, err := s.runtimeStore.SetContext(persistCtx, "skill-invocations", invocationID, map[string]any{
			"id":           invocationID,
			"skill":        skill.Name,
			"args":         args,
			"filter":       filter,
			"status":       "inline",
			"loadedSkills": loadedNames,
			"allowedTools": allowedTools,
			"path":         effectiveSkill.Path,
			"body_hash":    skill.BodyHash,
			"prompt_hash":  serverStringHash(prompt),
			"createdAt":    time.Now().UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			// Invocation persistence is an audit projection, not the Skill
			// execution authority. Preserve the diagnostic in the result while
			// allowing the model to receive the verified inline Skill contract.
			result["invocation_persistence"] = map[string]any{
				"ok": false, "error": err.Error(), "recoverable": true,
			}
		} else {
			result["invocation"] = entry
		}
	}
	return result, nil
}

func renderSkillPrompt(skill skills.Skill, args string) string {
	body := strings.TrimSpace(skill.Body)
	skillDir := ""
	if strings.TrimSpace(skill.Path) != "" && !strings.HasPrefix(skill.Path, "builtin:") {
		skillDir = filepath.ToSlash(filepath.Dir(skill.Path))
	}
	rendered := substituteSkillArguments(body, args, skill.Arguments)
	rendered = strings.ReplaceAll(rendered, "${SYNON_SKILL_DIR}", skillDir)
	if skillDir != "" && !strings.Contains(rendered, "Base directory for this skill:") {
		rendered = "Base directory for this skill: " + skillDir + "\n\n" + rendered
	}
	if assets := renderedSkillExecutionAssets(skill); len(assets) > 0 {
		rendered = "Default tested execution route:\n" +
			"1. Inspect the first applicable asset's help and preflight.\n" +
			"2. Execute that asset for the supported workflow instead of rebuilding it in inline code.\n" +
			"3. Deviate only after the asset reports a specific unsupported input boundary; adapt only that boundary and preserve bounded-memory loading, validation, provenance, and checkpoints.\n\n" +
			"Preferred assets:\n- " + strings.Join(assets, "\n- ") + "\n\n" + rendered
	}
	return strings.TrimSpace(rendered)
}

func renderComposedSkillPrompt(items []skills.Skill, rootName, args string) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	if len(items) == 1 {
		prompt := renderSkillContract(items[0], args)
		if !utf8.ValidString(prompt) {
			return "", errors.New("skill contract is not valid UTF-8")
		}
		if len(prompt) > maxRuntimeSkillContractBytes {
			return "", fmt.Errorf("skill contract exceeds %d bytes", maxRuntimeSkillContractBytes)
		}
		return prompt, nil
	}
	const heading = "Composed Skill contracts (dependencies first):"
	parts := []string{heading}
	for _, item := range items {
		itemArgs := ""
		if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(rootName)) {
			itemArgs = args
		}
		body := renderSkillContract(item, itemArgs)
		if !utf8.ValidString(body) {
			return "", fmt.Errorf("skill contract %q is not valid UTF-8", item.Name)
		}
		block := "### " + strings.TrimSpace(item.Name) + "\n" + body
		parts = append(parts, strings.TrimSpace(block))
	}
	prompt := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if len(prompt) > maxRuntimeSkillContractBytes {
		return "", fmt.Errorf(
			"composed skill contract exceeds %d bytes across %d skills; narrow the selected methodology instead of truncating it",
			maxRuntimeSkillContractBytes, len(items),
		)
	}
	return prompt, nil
}

func renderSkillContract(skill skills.Skill, args string) string {
	body := renderSkillPrompt(skill, args)
	if len(skill.CriticalConstraints) == 0 {
		return body
	}
	constraints := make([]string, 0, len(skill.CriticalConstraints))
	for _, constraint := range skill.CriticalConstraints {
		if constraint = strings.TrimSpace(constraint); constraint != "" {
			constraints = append(constraints, "- "+constraint)
		}
	}
	if len(constraints) == 0 {
		return body
	}
	return "Critical constraints:\n" + strings.Join(constraints, "\n") + "\n\n" + body
}

func renderedSkillExecutionAssets(skill skills.Skill) []string {
	directory := ""
	if strings.TrimSpace(skill.Path) != "" && !strings.HasPrefix(skill.Path, "builtin:") {
		directory = filepath.ToSlash(filepath.Dir(skill.Path))
	}
	assets := make([]string, 0, len(skill.PreferredExecutionAssets))
	for _, relative := range skill.PreferredExecutionAssets {
		relative = filepath.ToSlash(filepath.Clean(strings.TrimSpace(relative)))
		if directory == "" || relative == "" || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
			continue
		}
		assets = append(assets, directory+"/"+relative)
	}
	return assets
}

func substituteSkillArguments(content string, args string, argumentNames []string) string {
	original := content
	parsedArgs := parseSkillArguments(args)
	for index, name := range argumentNames {
		if strings.TrimSpace(name) == "" || isServerNumericString(name) {
			continue
		}
		pattern := regexp.MustCompile(`\$` + regexp.QuoteMeta(name) + `\b`)
		replacement := ""
		if index < len(parsedArgs) {
			replacement = parsedArgs[index]
		}
		content = pattern.ReplaceAllString(content, replacement)
	}
	indexed := regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`)
	content = indexed.ReplaceAllStringFunc(content, func(match string) string {
		parts := indexed.FindStringSubmatch(match)
		if len(parts) != 2 {
			return ""
		}
		index, err := strconv.Atoi(parts[1])
		if err != nil || index < 0 || index >= len(parsedArgs) {
			return ""
		}
		return parsedArgs[index]
	})
	shorthand := regexp.MustCompile(`\$(\d+)\b`)
	content = shorthand.ReplaceAllStringFunc(content, func(match string) string {
		parts := shorthand.FindStringSubmatch(match)
		if len(parts) != 2 {
			return ""
		}
		index, err := strconv.Atoi(parts[1])
		if err != nil || index < 0 || index >= len(parsedArgs) {
			return ""
		}
		return parsedArgs[index]
	})
	content = strings.ReplaceAll(content, "$ARGUMENTS", args)
	if content == original && strings.TrimSpace(args) != "" {
		content += "\n\nARGUMENTS: " + args
	}
	return content
}

func parseSkillArguments(args string) []string {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	parsed := []string{}
	var builder strings.Builder
	var quote rune
	escaped := false
	for _, r := range args {
		if escaped {
			builder.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			builder.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if builder.Len() > 0 {
				parsed = append(parsed, builder.String())
				builder.Reset()
			}
			continue
		}
		builder.WriteRune(r)
	}
	if escaped {
		builder.WriteRune('\\')
	}
	if builder.Len() > 0 {
		parsed = append(parsed, builder.String())
	}
	return parsed
}

func isServerNumericString(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func findCatalogSkill(catalog *skills.Catalog, name string) (skills.Skill, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	for _, skill := range catalog.Skills() {
		if strings.ToLower(strings.TrimSpace(skill.Name)) == key {
			return skill, true
		}
	}
	return skills.Skill{}, false
}

func skillInvocationID(skillName string, args string, at time.Time) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{skillName, args, at.Format(time.RFC3339Nano)}, "\n")))
	return "skill-" + hex.EncodeToString(sum[:])[:16]
}

func serverStringHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Server) executeSkillSearchTool(input map[string]any, toolAuthorities ...map[string]struct{}) (any, error) {
	return s.executeSkillSearchToolWithRuntimeSkills(input, nil, toolAuthorities...)
}

func (s *Server) executeSkillSearchToolWithRuntimeSkills(
	input map[string]any,
	runtimeSkills []skills.Skill,
	toolAuthorities ...map[string]struct{},
) (any, error) {
	return s.executeSkillSearchToolWithRuntimeSkillsAndPolicy(
		input, runtimeSkills, runtimeSkillPolicyAuthority{}, toolAuthorities...,
	)
}

func (s *Server) executeSkillSearchToolWithRuntimeSkillsAndPolicy(
	input map[string]any,
	runtimeSkills []skills.Skill,
	policy runtimeSkillPolicyAuthority,
	toolAuthorities ...map[string]struct{},
) (any, error) {
	if s == nil {
		return nil, fmt.Errorf("server is required")
	}
	if err := s.validateSkillSearcherInput(input); err != nil {
		return nil, err
	}
	query := strings.TrimSpace(stringValue(input["query"]))
	prefix := strings.ToLower(strings.TrimSpace(stringValue(input["prefix"])))
	maxResults := int(numberValue(input["max_results"]))
	if maxResults <= 0 {
		maxResults = 50
	}
	if maxResults > 200 {
		maxResults = 200
	}
	offset := int(numberValue(input["offset"]))
	if offset < 0 {
		offset = 0
	}
	requestedSkills, selectionMode := skillSearchSelectNames(query)
	matches := []string{}
	allMatchedNames := []string{}
	skillDetails := []map[string]any{}
	total := 0
	totalMatches := 0
	if s.skillCatalog != nil {
		catalogSkills := append(s.skillCatalog.Skills(), runtimeSkills...)
		combinedCatalog := skills.NewCatalog()
		for _, skill := range catalogSkills {
			combinedCatalog.UpsertSkill(skill)
		}
		catalogSkills = combinedCatalog.Skills()
		allSkills := s.filterRuntimeEnabledSkills(catalogSkills, 0)
		matchedSkills := allSkills
		if query != "" {
			matchedSkills = s.filterRuntimeEnabledSkills(combinedCatalog.Search(query, len(catalogSkills)), 0)
		}
		if prefix != "" {
			prefixed := matchedSkills[:0]
			for _, item := range matchedSkills {
				if runtimeSkillSearchPrefixMatches(item, prefix) {
					prefixed = append(prefixed, item)
				}
			}
			matchedSkills = prefixed
		}
		if len(toolAuthorities) > 0 {
			discoverable := s.agentRuntimeDiscoverableSkillSet(allSkills, toolAuthorities[0])
			allSkills = filterRuntimeSkillsByNameSet(allSkills, discoverable)
			matchedSkills = filterRuntimeSkillsByNameSet(matchedSkills, discoverable)
		}
		policyDiscoverable := s.runtimeSkillPolicyDiscoverableSet(allSkills, policy, toolAuthorities...)
		allSkills = filterRuntimeSkillsByNameSet(allSkills, policyDiscoverable)
		matchedSkills = filterRuntimeSkillsByNameSet(matchedSkills, policyDiscoverable)
		totalMatches = len(matchedSkills)
		allMatchedNames = make([]string, 0, totalMatches)
		for _, skill := range matchedSkills {
			allMatchedNames = append(allMatchedNames, skill.Name)
		}
		if offset > len(matchedSkills) {
			offset = len(matchedSkills)
		}
		end := min(offset+maxResults, len(matchedSkills))
		matchedSkills = matchedSkills[offset:end]
		matches = make([]string, 0, len(matchedSkills))
		for _, skill := range matchedSkills {
			matches = append(matches, skill.Name)
			skillDetails = append(skillDetails, runtimeSkillSearchMetadata(skill))
		}
		total = len(allSkills)
	} else {
		total = 0
	}
	missingSkills := []string{}
	if selectionMode {
		matched := make(map[string]bool, len(allMatchedNames))
		for _, name := range allMatchedNames {
			matched[strings.ToLower(strings.TrimSpace(name))] = true
		}
		seenMissing := map[string]bool{}
		for _, name := range requestedSkills {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" || matched[key] || seenMissing[key] {
				continue
			}
			missingSkills = append(missingSkills, name)
			seenMissing[key] = true
		}
	}
	loadErrors := make([]string, 0, len(s.skillErrors))
	for _, item := range s.skillErrors {
		loadErrors = append(loadErrors, fmt.Sprintf("%s: %s", item.Path, item.Err))
	}
	result := map[string]any{
		"matches":             matches,
		"skill_matches":       skillDetails,
		"load_instruction":    "Choose a matching skill, then invoke the skill tool by name. Do not read SKILL.md with workspace file tools.",
		"query":               query,
		"prefix":              prefix,
		"total_skills":        total,
		"total_matches":       totalMatches,
		"returned_count":      len(matches),
		"offset":              offset,
		"has_more":            offset+len(matches) < totalMatches,
		"skill_directories":   len(s.skillDirectories),
		"skill_load_errors":   len(loadErrors),
		"skill_load_messages": loadErrors,
	}
	if offset+len(matches) < totalMatches {
		result["next_offset"] = offset + len(matches)
	}
	if selectionMode {
		result["selection_mode"] = true
		result["requested_skills"] = requestedSkills
		result["missing_skills"] = missingSkills
		result["selection_complete"] = len(missingSkills) == 0
		result["selected_count"] = totalMatches
		result["missing_count"] = len(missingSkills)
	}
	return result, nil
}

// runtimeSkillPolicyDiscoverableSet applies the same root, dependency, and
// live-tool authority used by the Skill loader before a candidate is advertised
// by search_skills. Discovery must never offer a Skill that the immediately
// following skill call will reject under the same immutable turn snapshot.
func (s *Server) runtimeSkillPolicyDiscoverableSet(
	items []skills.Skill,
	policy runtimeSkillPolicyAuthority,
	toolAuthorities ...map[string]struct{},
) map[string]struct{} {
	result := make(map[string]struct{}, len(items))
	var toolAuthority map[string]struct{}
	if len(toolAuthorities) > 0 {
		toolAuthority = toolAuthorities[0]
	}
	for _, skill := range items {
		if _, err := s.expandRuntimeSkillDependencies(
			[]skills.Skill{skill},
			policy.ExcludedNames,
			policy.AllowedNames,
			policy.Restrict,
			toolAuthority,
			items,
		); err != nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(skill.Name))
		if name != "" {
			result[name] = struct{}{}
		}
	}
	return result
}

// runtimeSkillSearchPrefixMatches accepts both the historical skill-name
// prefix and a catalog namespace/directory segment. Models commonly use the
// visible namespace (for example "synonbiomed") as a discovery scope; treating
// it only as a name prefix silently turns a healthy catalog into zero matches.
func runtimeSkillSearchPrefixMatches(skill skills.Skill, prefix string) bool {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(skill.Name)), prefix) {
		return true
	}
	path := strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(skill.Path))))
	for _, segment := range strings.Split(path, "/") {
		if strings.TrimSpace(segment) == prefix {
			return true
		}
	}
	return false
}

func (s *Server) agentRuntimeDiscoverableSkills(items []skills.Skill, toolAuthority map[string]struct{}) []skills.Skill {
	return filterRuntimeSkillsByNameSet(items, s.agentRuntimeDiscoverableSkillSet(items, toolAuthority))
}

func (s *Server) agentRuntimeSkillToolsAvailable(skill skills.Skill, toolAuthority map[string]struct{}) bool {
	_, available := s.agentRuntimeDiscoverableSkillSet([]skills.Skill{skill}, toolAuthority)[strings.ToLower(strings.TrimSpace(skill.Name))]
	return available
}

func (s *Server) agentRuntimeDiscoverableSkillSet(items []skills.Skill, toolAuthority map[string]struct{}) map[string]struct{} {
	return s.agentRuntimeSkillSet(items, toolAuthority, true)
}

// agentRuntimeReferenceableSkillSet applies the same required-tool authority
// boundary as interactive discovery while allowing an unavailable external
// runtime to remain a documented fallback boundary. Reference guidance may be
// useful without that runtime; it must never smuggle in a forbidden tool.
func (s *Server) agentRuntimeReferenceableSkillSet(items []skills.Skill, toolAuthority map[string]struct{}) map[string]struct{} {
	return s.agentRuntimeSkillSet(items, toolAuthority, false)
}

func (s *Server) agentRuntimeSkillSet(items []skills.Skill, toolAuthority map[string]struct{}, requireExternalRuntime bool) map[string]struct{} {
	discoverable := map[string]struct{}{}
	if s == nil || s.tools == nil {
		return discoverable
	}
	report := registry.AuditRuntimeSkillDependencyClosure(
		items,
		s.tools,
		registry.RuntimeResolverFunc(func(name string, class registry.DependencyClass) registry.RuntimeStatus {
			if class == registry.DependencyClassExternalRuntime {
				if requireExternalRuntime && (s.kernelManager == nil || !s.kernelManager.ManagedEnvironmentSupervisorReady()) {
					return registry.RuntimeStatus{Availability: "unavailable", Route: "managed-environment-supervisor"}
				}
				return registry.RuntimeStatus{
					Executable: true, AuthorityResolved: true, Availability: "installable",
					Route: "managed-environment-supervisor",
				}
			}
			if class != registry.DependencyClassTool {
				return registry.RuntimeStatus{Executable: true, Availability: "not-evaluated", Route: "request-tool-authority"}
			}
			canonical, ok := toolcontract.NormalizeRuntimeName(name)
			if !ok {
				return registry.RuntimeStatus{Availability: "unavailable", Route: "agent-runtime-authority"}
			}
			if _, present := toolAuthority[canonical]; !present {
				return registry.RuntimeStatus{Availability: "unavailable", Route: "agent-runtime-authority"}
			}
			return registry.RuntimeStatus{Executable: true, AuthorityResolved: true, Availability: "available", Route: "agent-runtime-authority"}
		}),
	)
	for _, row := range report.Skills {
		if !row.Closed || runtimeSkillHasUnresolvedCapabilityAlias(row, toolAuthority) {
			continue
		}
		discoverable[strings.ToLower(strings.TrimSpace(row.Skill))] = struct{}{}
	}
	return discoverable
}

// runtimeSkillHasUnresolvedCapabilityAlias keeps an unavailable specialization
// of an authorized tool out of discovery without turning every frontmatter
// allowlist entry into a mandatory dependency. A name such as
// "compute_provider_modal" is treated as a capability alias only when its
// underscore-delimited prefix is an actually authorized runtime tool.
func runtimeSkillHasUnresolvedCapabilityAlias(row registry.SkillDependencyStatus, toolAuthority map[string]struct{}) bool {
	for _, dependency := range row.Dependencies {
		if dependency.Class != registry.DependencyClassTool || dependency.RuntimeExecutable || dependency.AuthorityResolved {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(dependency.Name))
		for index := strings.LastIndexByte(name, '_'); index > 0; index = strings.LastIndexByte(name[:index], '_') {
			prefix := strings.TrimSpace(name[:index])
			if _, authorized := toolAuthority[prefix]; authorized {
				return true
			}
		}
	}
	return false
}

func agentRuntimeToolSchemaNameSet(schemas []agentruntime.ToolSchema) map[string]struct{} {
	result := make(map[string]struct{}, len(schemas))
	for _, schema := range schemas {
		if canonical, ok := toolcontract.NormalizeRuntimeName(schema.Name); ok {
			result[canonical] = struct{}{}
		}
	}
	return result
}

func filterRuntimeSkillsByNameSet(items []skills.Skill, allowed map[string]struct{}) []skills.Skill {
	filtered := make([]skills.Skill, 0, len(items))
	for _, skill := range items {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(skill.Name))]; ok {
			filtered = append(filtered, skill)
		}
	}
	return filtered
}

func skillSearchSelectNames(query string) ([]string, bool) {
	trimmed := strings.TrimSpace(query)
	if len(trimmed) < len("select:") || !strings.EqualFold(trimmed[:len("select:")], "select:") {
		return nil, false
	}
	rawNames := strings.Split(trimmed[len("select:"):], ",")
	names := make([]string, 0, len(rawNames))
	seen := map[string]bool{}
	for _, raw := range rawNames {
		name := strings.TrimSpace(raw)
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			continue
		}
		names = append(names, name)
		seen[key] = true
	}
	return names, true
}

func (s *Server) validateSkillSearcherInput(input map[string]any) error {
	query := strings.TrimSpace(stringValue(input["query"]))
	prefix := strings.TrimSpace(stringValue(input["prefix"]))
	if len([]rune(query)) > 2048 || len([]rune(prefix)) > 256 {
		return errors.New("search_skills query or prefix exceeds the bounded contract")
	}
	return nil
}

func (s *Server) executeLSPTool(ctx context.Context, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool("LSP", input); err != nil {
		return nil, err
	}
	return lspstatic.Run(ctx, lspstatic.Input{
		Operation: stringValue(input["operation"]),
		FilePath:  stringValue(input["filePath"]),
		Line:      int(numberValue(input["line"])),
		Character: int(numberValue(input["character"])),
		Query:     stringValue(input["query"]),
		NewName:   stringValue(input["newName"]),
	}, lspstatic.Options{Root: s.fileRoot})
}
