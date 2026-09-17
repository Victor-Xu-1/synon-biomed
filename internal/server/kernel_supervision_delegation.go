package server

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/providers"
	"time"
)

func kernelDelegationSpawnCap(access workspace.KernelFrameAccess) int {
	if access.Frame.ID != access.Frame.RootFrameID {
		return maxKernelDelegateBatch
	}
	return workspace.DefaultKernelDelegationSpawnCap
}

func partitionKernelDelegateOutputSchemas(requests []workspace.KernelDelegateRequest) ([]workspace.KernelDelegateRequest, []int, []any) {
	valid := make([]workspace.KernelDelegateRequest, 0, len(requests))
	indexes := make([]int, 0, len(requests))
	results := make([]any, len(requests))
	for index, request := range requests {
		if err := validateKernelDelegateOutputSchema(request.OutputSchema); err != nil {
			results[index] = map[string]any{"status": "failed", "error": "delegate output_schema is invalid"}
			continue
		}
		valid = append(valid, request)
		indexes = append(indexes, index)
	}
	return valid, indexes, results
}

func (s *Server) partitionKernelDelegateAuthorities(
	access workspace.KernelFrameAccess,
	requests []workspace.KernelDelegateRequest,
	indexes []int,
	results []any,
) ([]workspace.KernelDelegateRequest, []int) {
	valid := make([]workspace.KernelDelegateRequest, 0, len(requests))
	validIndexes := make([]int, 0, len(indexes))
	for index, request := range requests {
		original := indexes[index]
		if access.Frame.ID != access.Frame.RootFrameID && strings.TrimSpace(request.Profile) != "" &&
			!strings.EqualFold(strings.TrimSpace(request.Profile), strings.TrimSpace(access.Frame.AgentName)) {
			results[original] = map[string]any{"status": "failed", "error": "delegate profile is unavailable"}
			continue
		}
		candidate := []workspace.KernelDelegateRequest{request}
		if err := s.validateKernelDelegateProfiles(access.UserID, candidate); err != nil {
			results[original] = map[string]any{"status": "failed", "error": "delegate profile is unavailable"}
			continue
		}
		if err := s.resolveKernelDelegateModels(access, candidate); err != nil {
			results[original] = map[string]any{"status": "failed", "error": "delegate model is unavailable"}
			continue
		}
		valid = append(valid, candidate[0])
		validIndexes = append(validIndexes, original)
	}
	return valid, validIndexes
}

func validateKernelDelegateOutputSchema(schema map[string]any) error {
	if schema == nil {
		return nil
	}
	if asynchronous, ok := schema["$async"].(bool); ok && asynchronous {
		return errors.New("async output schema is unsupported")
	}
	acceptsObject := true
	if rawType, found := schema["type"]; found {
		acceptsObject = false
		switch typed := rawType.(type) {
		case string:
			acceptsObject = typed == "object"
		case []any:
			for _, item := range typed {
				if item == "object" {
					acceptsObject = true
					break
				}
			}
		}
	}
	if !acceptsObject {
		return errors.New("output schema must accept an object")
	}
	_, err := compileKernelDraft7Schema(schema)
	return err
}

func kernelDelegateOutputSchemaFromSession(session sessionstore.Session) (map[string]any, bool) {
	if len(session.Orchestration) == 0 {
		return nil, false
	}
	raw, found := session.Orchestration["output_schema"]
	if !found || raw == nil {
		return nil, false
	}
	schema, ok := raw.(map[string]any)
	if !ok || len(schema) == 0 || validateKernelDelegateOutputSchema(schema) != nil {
		return nil, false
	}
	return copyMapAny(schema), true
}

func kernelDelegateSubmitOutputToolSchema(schema map[string]any) agentruntime.ToolSchema {
	return agentruntime.ToolSchema{
		Name:        kernelDelegateSubmitToolName,
		Description: "Submit the delegated task result exactly once in the requested structured shape, then finish.",
		Parameters:  copyMapAny(schema),
	}
}

func (s *Server) executeKernelDelegateSubmitOutput(sessionID string, input map[string]any) (map[string]any, error) {
	if s == nil || s.sessionStore == nil {
		return nil, errors.New("delegated structured-output authority is unavailable")
	}
	session, found, err := s.sessionStore.Get(strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("delegated session is unavailable")
	}
	schema, found := kernelDelegateOutputSchemaFromSession(session)
	if !found {
		return nil, errors.New("submit_output is unavailable for this session")
	}
	if err := validateKernelMCPInput(schema, input); err != nil {
		return nil, errors.New("submit_output does not match the requested schema")
	}
	if s.runtimeStore != nil {
		entries, listErr := s.runtimeStore.List(structuredOutputRuntimeNamespace)
		if listErr != nil {
			return nil, listErr
		}
		for _, entry := range entries {
			record, ok := entry.Value.(map[string]any)
			if ok && strings.TrimSpace(stringValue(record["sessionId"])) == strings.TrimSpace(sessionID) {
				return nil, errors.New("submit_output was already completed for this delegated session")
			}
		}
	}
	return s.executeStructuredOutputTool(map[string]any{
		"schema": schema, "value": copyMapAny(input), "sessionId": strings.TrimSpace(sessionID),
	})
}

func mergeKernelDelegateResults(results []any, indexes []int, valid []any) []any {
	for index, original := range indexes {
		if index < len(valid) {
			results[original] = valid[index]
		}
	}
	return results
}

func (s *Server) resolveKernelDelegateModels(access workspace.KernelFrameAccess, requests []workspace.KernelDelegateRequest) error {
	inheritedModel := ""
	if s.sessionStore != nil {
		if parent, found, err := s.sessionStore.Get(access.Frame.ID); err == nil && found {
			if config, ok := parent.Orchestration["sessionConfig"].(map[string]any); ok {
				inheritedModel = firstNonEmpty(stringValue(config["subagentModel"]), stringValue(config["subagent_model"]))
			}
		}
	}
	cache := map[string]providers.ModelProfile{}
	for index := range requests {
		requested := strings.TrimSpace(requests[index].Model)
		if requested == "" {
			requested = strings.TrimSpace(inheritedModel)
		}
		profile, found := cache[requested]
		if !found {
			var err error
			profile, err = providers.ResolveUserModelProfile(s.settingsStore, s.workspaceStore, s.secretStore, access.UserID, requested, providers.ResolutionInput{
				ProjectID: access.Frame.ProjectID, RequestTimeout: defaultSessionRunnerChatRequestTimeout,
				MaxAttempts: defaultSessionRunnerChatMaxAttempts, MaxResponseBytes: defaultSessionRunnerModelResponseLimitBytes,
			})
			if err != nil {
				return fmt.Errorf("host.delegate request %d model resolution failed: %w", index, err)
			}
			cache[requested] = profile
		}
		requests[index].Model = profile.Model
	}
	return nil
}

func (s *Server) kernelDelegationMaxDepth() int {
	depth := 1
	if s != nil && s.settingsStore != nil {
		if setting, found, err := s.settingsStore.Get("delegation.max_depth"); err == nil && found {
			candidate := int(numberValue(setting.Value))
			if candidate >= 1 && candidate <= 2 {
				depth = candidate
			}
		}
	}
	return depth
}

func (s *Server) validateKernelDelegateProfiles(ownerUserID string, requests []workspace.KernelDelegateRequest) error {
	custom := map[string]bool{}
	if s.workspaceStore != nil {
		agents, err := s.workspaceStore.ListAgents(ownerUserID)
		if err != nil {
			return fmt.Errorf("list available delegate profiles: %w", err)
		}
		for _, agent := range agents {
			custom[strings.ToLower(strings.TrimSpace(agent.Name))] = agent.Enabled
		}
	}
	for index, request := range requests {
		profile := strings.TrimSpace(request.Profile)
		if profile == "" {
			continue
		}
		if s.agentCatalog != nil {
			if agent, found := s.agentCatalog.Agent(profile); found && agent.Enabled {
				continue
			}
		}
		if custom[strings.ToLower(profile)] {
			continue
		}
		return fmt.Errorf("host.delegate request %d profile %q is unavailable or disabled", index, profile)
	}
	return nil
}

type kernelDelegateOptions struct {
	Wait    bool
	Timeout time.Duration
}

func parseKernelDelegateCall(args []any, kwargs map[string]any) ([]workspace.KernelDelegateRequest, kernelDelegateOptions, bool, error) {
	if len(kwargs) != 0 || len(args) < 1 || len(args) > 2 {
		return nil, kernelDelegateOptions{}, false, errors.New("host.delegate expects [requests] and optional options")
	}
	rawRequests, ok := args[0].([]any)
	if !ok {
		return nil, kernelDelegateOptions{}, false, errors.New("host.delegate requests must be a list")
	}
	if len(rawRequests) > maxKernelDelegateBatch {
		return nil, kernelDelegateOptions{}, false, fmt.Errorf("host.delegate supports at most %d requests per call", maxKernelDelegateBatch)
	}
	options := kernelDelegateOptions{Wait: true}
	if len(args) == 2 {
		rawOptions, ok := args[1].(map[string]any)
		if !ok {
			return nil, options, false, errors.New("host.delegate options must be an object")
		}
		for key := range rawOptions {
			if key != "wait" && key != "timeout" && key != "max_concurrency" {
				return nil, options, false, fmt.Errorf("host.delegate unknown option %q", key)
			}
		}
		if rawWait, exists := rawOptions["wait"]; exists {
			wait, ok := rawWait.(bool)
			if !ok {
				return nil, options, false, errors.New("host.delegate wait must be a bool")
			}
			options.Wait = wait
		}
		if rawTimeout, exists := rawOptions["timeout"]; exists {
			timeout, err := positiveKernelSeconds(rawTimeout, 24*time.Hour, "host.delegate timeout")
			if err != nil {
				return nil, options, false, err
			}
			options.Timeout = timeout
		}
		// The reference 0.1.20 runtime accepts max_concurrency as a legacy option but
		// intentionally ignores its value.
	}
	if !options.Wait && options.Timeout > 0 {
		return nil, options, false, errors.New("host.delegate timeout is not valid with wait=False")
	}
	requests := make([]workspace.KernelDelegateRequest, len(rawRequests))
	for index, raw := range rawRequests {
		mapping, ok := raw.(map[string]any)
		if !ok {
			return nil, options, false, fmt.Errorf("host.delegate request %d must be an object", index)
		}
		for key := range mapping {
			switch key {
			case "task", "name", "context_summary", "profile", "output_schema", "model":
			default:
				return nil, options, false, fmt.Errorf("host.delegate request %d has unknown field %q", index, key)
			}
		}
		task, ok := mapping["task"].(string)
		if !ok || strings.TrimSpace(task) == "" {
			return nil, options, false, fmt.Errorf("host.delegate request %d task must be a non-empty string", index)
		}
		request := workspace.KernelDelegateRequest{Task: task}
		for key, target := range map[string]*string{
			"name": &request.Name, "context_summary": &request.ContextSummary,
			"profile": &request.Profile, "model": &request.Model,
		} {
			if value, exists := mapping[key]; exists && value != nil {
				text, ok := value.(string)
				if !ok {
					return nil, options, false, fmt.Errorf("host.delegate request %d %s must be a string or null", index, key)
				}
				*target = text
			}
		}
		if strings.TrimSpace(request.Model) == "" && mapping["model"] != nil {
			return nil, options, false, fmt.Errorf("host.delegate request %d model must be non-empty or null", index)
		}
		if rawSchema, exists := mapping["output_schema"]; exists && rawSchema != nil {
			schema, ok := rawSchema.(map[string]any)
			if !ok {
				return nil, options, false, fmt.Errorf("host.delegate request %d output_schema must be an object or null", index)
			}
			request.OutputSchema = schema
		}
		requests[index] = request
	}
	return requests, options, len(requests) == 1, nil
}

func parseKernelCollectCall(args []any, kwargs map[string]any) ([]string, time.Duration, error) {
	if len(kwargs) != 0 || len(args) != 1 {
		return nil, 0, errors.New("host.collect expects one payload object")
	}
	payload, ok := args[0].(map[string]any)
	if !ok {
		return nil, 0, errors.New("host.collect payload must be an object")
	}
	rawIDs, ok := payload["frame_ids"].([]any)
	if !ok || len(rawIDs) == 0 || len(rawIDs) > maxKernelCollectBatch {
		return nil, 0, fmt.Errorf("host.collect frame_ids must contain 1 to %d ids", maxKernelCollectBatch)
	}
	frameIDs := make([]string, len(rawIDs))
	for index, raw := range rawIDs {
		frameID, ok := raw.(string)
		if !ok || strings.TrimSpace(frameID) == "" || len(frameID) > 256 {
			return nil, 0, fmt.Errorf("host.collect frame_ids[%d] is invalid", index)
		}
		frameIDs[index] = strings.TrimSpace(frameID)
	}
	timeout := defaultKernelCollectTimeout
	if rawTimeout, exists := payload["timeout"]; exists {
		if rawTimeout == nil {
			return nil, 0, errors.New("host.collect timeout=None is not allowed")
		}
		var err error
		timeout, err = positiveKernelSeconds(rawTimeout, maxKernelCollectTimeout, "host.collect timeout")
		if err != nil {
			return nil, 0, err
		}
	}
	return frameIDs, timeout, nil
}

func parseKernelStopChildCall(args []any, kwargs map[string]any) ([]string, bool, string, error) {
	if len(kwargs) != 0 || len(args) != 1 {
		return nil, false, "", errors.New("host.stop_child expects one payload object")
	}
	payload, ok := args[0].(map[string]any)
	if !ok {
		return nil, false, "", errors.New("host.stop_child payload must be an object")
	}
	reason := ""
	if rawReason, exists := payload["reason"]; exists && rawReason != nil {
		var ok bool
		reason, ok = rawReason.(string)
		if !ok {
			return nil, false, "", errors.New("host.stop_child reason must be a string or null")
		}
	}
	raw := payload["child_frame_id"]
	if list, ok := raw.([]any); ok {
		if len(list) == 0 || len(list) > maxKernelStopBatch {
			return nil, false, "", fmt.Errorf("host.stop_child list must contain 1 to %d ids", maxKernelStopBatch)
		}
		ids := make([]string, len(list))
		for index, item := range list {
			id, ok := item.(string)
			if !ok || strings.TrimSpace(id) == "" {
				return nil, false, "", fmt.Errorf("host.stop_child id %d is invalid", index)
			}
			ids[index] = strings.TrimSpace(id)
		}
		return ids, false, reason, nil
	}
	id, ok := raw.(string)
	if !ok || strings.TrimSpace(id) == "" {
		return nil, false, "", errors.New("host.stop_child child_frame_id is invalid")
	}
	return []string{strings.TrimSpace(id)}, true, reason, nil
}

func parseKernelSendMessageCall(args []any, kwargs map[string]any) (string, string, string, error) {
	if len(kwargs) != 0 || len(args) != 1 {
		return "", "", "", errors.New("host.send_message expects one payload object")
	}
	payload, ok := args[0].(map[string]any)
	if !ok {
		return "", "", "", errors.New("host.send_message payload must be an object")
	}
	target, targetOK := payload["target"].(string)
	message, messageOK := payload["message"].(string)
	kind, kindOK := payload["kind"].(string)
	if !targetOK || strings.TrimSpace(target) == "" || len(target) > 256 {
		return "", "", "", errors.New("host.send_message target is invalid")
	}
	if !messageOK || strings.TrimSpace(message) == "" {
		return "", "", "", errors.New("host.send_message message must be non-empty")
	}
	if !kindOK || (kind != "info" && kind != "question") {
		return "", "", "", errors.New("host.send_message kind must be 'info' or 'question'")
	}
	return strings.TrimSpace(target), message, kind, nil
}

func positiveKernelSeconds(value any, max time.Duration, label string) (time.Duration, error) {
	if _, ok := value.(bool); ok {
		return 0, fmt.Errorf("%s must be a positive number", label)
	}
	var seconds float64
	switch typed := value.(type) {
	case int:
		seconds = float64(typed)
	case int64:
		seconds = float64(typed)
	case float64:
		seconds = typed
	default:
		return 0, fmt.Errorf("%s must be a positive number", label)
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", label)
	}
	if max > 0 && seconds > max.Seconds() {
		return 0, fmt.Errorf("%s is capped at %g seconds", label, max.Seconds())
	}
	duration := time.Duration(seconds * float64(time.Second))
	return duration, nil
}
