package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"synon-go/internal/agentruntime"
	"synon-go/internal/tools/shellops"
	"time"
)

func (s *Server) runAgentRuntimeCommandHook(ctx context.Context, eventName string, hookKey string, raw map[string]any, payload map[string]any) *agentRuntimeCommandHookResult {
	command := strings.TrimSpace(stringValue(raw["command"]))
	if command == "" {
		return nil
	}
	pluginRoot := strings.TrimSpace(stringValue(raw["pluginRoot"]))
	if pluginRoot != "" {
		command = strings.ReplaceAll(command, "${SYNON_PLUGIN_ROOT}", pluginRoot)
		command = strings.ReplaceAll(command, "$SYNON_PLUGIN_ROOT", pluginRoot)
	}
	shellName := strings.TrimSpace(stringValue(raw["shell"]))
	if shellName == "" {
		shellName = "Bash"
	}
	if err := shellops.CheckSafety(shellName, command, nil); err != nil {
		return &agentRuntimeCommandHookResult{
			Decision: "block",
			Reason:   err.Error(),
			Audit: map[string]any{
				"hook":    hookKey,
				"event":   eventName,
				"type":    "command",
				"shell":   shellName,
				"command": command,
				"error":   err.Error(),
				"blocked": true,
			},
		}
	}
	stdinBytes, err := json.Marshal(payload)
	if err != nil {
		return &agentRuntimeCommandHookResult{Audit: map[string]any{
			"hook":  hookKey,
			"event": eventName,
			"error": fmt.Sprintf("marshal hook input: %v", err),
		}}
	}
	workdir := stringValue(raw["workdir"])
	if strings.TrimSpace(workdir) == "" && pluginRoot != "" {
		workdir = pluginRoot
	}
	timeout := agentRuntimeHookTimeout(raw)
	result, runErr := shellops.ExecuteShellCommandWithInputEnv(ctx, s.fileRoot, shellName, command, workdir, timeout.Milliseconds(), string(stdinBytes), agentRuntimeHookEnv(raw))
	audit := map[string]any{
		"hook":            hookKey,
		"event":           eventName,
		"type":            "command",
		"shell":           shellName,
		"command":         command,
		"exitCode":        result.ExitCode,
		"stdout":          result.Stdout,
		"stderr":          result.Stderr,
		"stdoutBytes":     result.StdoutBytes,
		"stderrBytes":     result.StderrBytes,
		"stdoutTruncated": result.StdoutTruncated,
		"stderrTruncated": result.StderrTruncated,
		"timeoutMs":       timeout.Milliseconds(),
	}
	if pluginID := strings.TrimSpace(stringValue(raw["pluginId"])); pluginID != "" {
		audit["pluginId"] = pluginID
	}
	if pluginRoot != "" {
		audit["pluginRoot"] = pluginRoot
	}
	parsed := parseAgentRuntimeHookOutput(eventName, command, result.Stdout)
	if parsed == nil {
		parsed = &agentRuntimeCommandHookResult{}
	}
	parsed.Audit = audit
	if runErr != nil {
		audit["error"] = runErr.Error()
		if result.ExitCode == 2 || strings.EqualFold(eventName, "PreToolUse") {
			parsed.Decision = "deny"
			parsed.Reason = agentRuntimeFirstNonEmpty(strings.TrimSpace(result.Stderr), strings.TrimSpace(result.Stdout), runErr.Error())
		}
		return parsed
	}
	return parsed
}

func agentRuntimeHookEnv(raw map[string]any) map[string]string {
	env := map[string]string{}
	if pluginID := strings.TrimSpace(stringValue(raw["pluginId"])); pluginID != "" {
		env["SYNON_PLUGIN_ID"] = pluginID
	}
	if pluginName := strings.TrimSpace(stringValue(raw["pluginName"])); pluginName != "" {
		env["SYNON_PLUGIN_NAME"] = pluginName
	}
	if pluginRoot := strings.TrimSpace(stringValue(raw["pluginRoot"])); pluginRoot != "" {
		env["SYNON_PLUGIN_ROOT"] = pluginRoot
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func (s *Server) runAgentRuntimeHTTPHook(ctx context.Context, eventName string, hookKey string, raw map[string]any, payload map[string]any) *agentRuntimeCommandHookResult {
	target := strings.TrimSpace(stringValue(raw["url"]))
	if target == "" {
		return &agentRuntimeCommandHookResult{Audit: map[string]any{
			"hook":  hookKey,
			"event": eventName,
			"type":  "http",
			"error": "http hook url is required",
		}}
	}
	parsedURL, err := url.Parse(target)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return &agentRuntimeCommandHookResult{Audit: map[string]any{
			"hook":  hookKey,
			"event": eventName,
			"type":  "http",
			"url":   target,
			"error": "http hook url must be an absolute http or https URL",
		}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &agentRuntimeCommandHookResult{Audit: map[string]any{
			"hook":  hookKey,
			"event": eventName,
			"type":  "http",
			"url":   target,
			"error": fmt.Sprintf("marshal hook input: %v", err),
		}}
	}
	timeout := agentRuntimeHookTimeout(raw)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(runCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return &agentRuntimeCommandHookResult{Audit: map[string]any{
			"hook":  hookKey,
			"event": eventName,
			"type":  "http",
			"url":   target,
			"error": err.Error(),
		}}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for key, value := range mapValue(raw["headers"]) {
		header := strings.TrimSpace(key)
		if header != "" {
			req.Header.Set(header, stringValue(value))
		}
	}
	client := http.DefaultClient
	if s != nil && s.httpClient != nil {
		client = s.httpClient
	}
	resp, err := client.Do(req)
	audit := map[string]any{
		"hook":         hookKey,
		"event":        eventName,
		"type":         "http",
		"url":          target,
		"timeoutMs":    timeout.Milliseconds(),
		"requestBytes": len(body),
	}
	if pluginID := strings.TrimSpace(stringValue(raw["pluginId"])); pluginID != "" {
		audit["pluginId"] = pluginID
	}
	if pluginRoot := strings.TrimSpace(stringValue(raw["pluginRoot"])); pluginRoot != "" {
		audit["pluginRoot"] = pluginRoot
	}
	if err != nil {
		audit["error"] = err.Error()
		return &agentRuntimeCommandHookResult{Audit: audit}
	}
	defer resp.Body.Close()
	const maxHTTPHookResponseBytes = 256 * 1024
	limited := io.LimitReader(resp.Body, maxHTTPHookResponseBytes+1)
	data, readErr := io.ReadAll(limited)
	if readErr != nil {
		audit["error"] = readErr.Error()
		return &agentRuntimeCommandHookResult{Audit: audit}
	}
	truncated := len(data) > maxHTTPHookResponseBytes
	if truncated {
		data = data[:maxHTTPHookResponseBytes]
	}
	text := string(data)
	audit["statusCode"] = resp.StatusCode
	audit["responseBody"] = text
	audit["responseBytes"] = len(data)
	audit["responseTruncated"] = truncated
	parsed := parseAgentRuntimeHookOutput(eventName, target, text)
	if parsed == nil {
		parsed = &agentRuntimeCommandHookResult{}
	}
	parsed.Audit = audit
	return parsed
}

func agentRuntimeHookTimeout(raw map[string]any) time.Duration {
	timeout := 10 * time.Second
	if value := numberValue(raw["timeout"]); value > 0 {
		timeout = time.Duration(value) * time.Second
	}
	if timeout > 30*time.Second {
		return 30 * time.Second
	}
	return timeout
}

func (s *Server) runAgentRuntimePromptHook(ctx context.Context, eventName string, hookKey string, raw map[string]any, payload map[string]any) *agentRuntimeCommandHookResult {
	prompt := strings.TrimSpace(stringValue(raw["prompt"]))
	if prompt == "" {
		return &agentRuntimeCommandHookResult{Audit: map[string]any{
			"hook":  hookKey,
			"event": eventName,
			"type":  "prompt",
			"error": "prompt hook prompt is required",
		}}
	}
	options := s.compactSummarizer
	if model := strings.TrimSpace(stringValue(raw["model"])); model != "" {
		options.Model = model
	}
	audit := map[string]any{
		"hook":  hookKey,
		"event": eventName,
		"type":  "prompt",
		"model": strings.TrimSpace(options.Model),
	}
	if pluginID := strings.TrimSpace(stringValue(raw["pluginId"])); pluginID != "" {
		audit["pluginId"] = pluginID
	}
	if pluginRoot := strings.TrimSpace(stringValue(raw["pluginRoot"])); pluginRoot != "" {
		audit["pluginRoot"] = pluginRoot
	}
	if strings.TrimSpace(options.Endpoint) == "" || strings.TrimSpace(options.Model) == "" {
		audit["error"] = "prompt hook requires compact summarizer endpoint and model"
		return &agentRuntimeCommandHookResult{Audit: audit}
	}
	inputJSON, err := json.Marshal(payload)
	if err != nil {
		audit["error"] = fmt.Sprintf("marshal hook input: %v", err)
		return &agentRuntimeCommandHookResult{Audit: audit}
	}
	userPrompt := agentRuntimePromptHookPrompt(prompt, string(inputJSON))
	timeout := agentRuntimeHookTimeout(raw)
	options.RequestTimeout = timeout
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 1
	}
	client := agentruntime.OpenAIChatClient{
		Endpoint:       options.Endpoint,
		APIKey:         options.APIKey,
		Model:          options.Model,
		HTTPClient:     s.httpClient,
		RequestTimeout: options.RequestTimeout,
		MaxAttempts:    options.MaxAttempts,
	}
	response, err := client.Complete(ctx, agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: "You are a Synon hook evaluator. Return one JSON object only. Do not include Markdown."},
			{Role: "user", Content: userPrompt},
		},
	})
	audit["timeoutMs"] = timeout.Milliseconds()
	audit["promptChars"] = len(userPrompt)
	if err != nil {
		audit["error"] = err.Error()
		return &agentRuntimeCommandHookResult{Audit: audit}
	}
	content := strings.TrimSpace(response.Message.Content)
	audit["responseBody"] = content
	audit["responseBytes"] = len(content)
	parsed := parseAgentRuntimeHookOutput(eventName, prompt, content)
	if parsed == nil {
		parsed = &agentRuntimeCommandHookResult{}
	}
	parsed.Audit = audit
	return parsed
}

func (s *Server) runAgentRuntimeAgentHook(ctx context.Context, eventName string, hookKey string, raw map[string]any, payload map[string]any) *agentRuntimeCommandHookResult {
	prompt := strings.TrimSpace(stringValue(raw["prompt"]))
	if prompt == "" {
		return &agentRuntimeCommandHookResult{Audit: map[string]any{
			"hook":  hookKey,
			"event": eventName,
			"type":  "agent",
			"error": "agent hook prompt is required",
		}}
	}
	options := s.compactSummarizer
	if model := strings.TrimSpace(stringValue(raw["model"])); model != "" {
		options.Model = model
	}
	audit := map[string]any{
		"hook":  hookKey,
		"event": eventName,
		"type":  "agent",
		"model": strings.TrimSpace(options.Model),
	}
	if pluginID := strings.TrimSpace(stringValue(raw["pluginId"])); pluginID != "" {
		audit["pluginId"] = pluginID
	}
	if pluginRoot := strings.TrimSpace(stringValue(raw["pluginRoot"])); pluginRoot != "" {
		audit["pluginRoot"] = pluginRoot
	}
	if strings.TrimSpace(options.Endpoint) == "" || strings.TrimSpace(options.Model) == "" {
		audit["error"] = "agent hook requires compact summarizer endpoint and model"
		return &agentRuntimeCommandHookResult{Audit: audit}
	}
	inputJSON, err := json.Marshal(payload)
	if err != nil {
		audit["error"] = fmt.Sprintf("marshal hook input: %v", err)
		return &agentRuntimeCommandHookResult{Audit: audit}
	}
	userPrompt := agentRuntimePromptHookPrompt(prompt, string(inputJSON))
	timeout := agentRuntimeHookTimeout(raw)
	options.RequestTimeout = timeout
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 1
	}
	allowedTools := readOnlyAgentAllowedTools(nil)
	engine := agentruntime.Engine{
		Model: agentruntime.OpenAIChatClient{
			Endpoint:       options.Endpoint,
			APIKey:         options.APIKey,
			Model:          options.Model,
			HTTPClient:     s.httpClient,
			RequestTimeout: options.RequestTimeout,
			MaxAttempts:    options.MaxAttempts,
		},
		Tools: serverAgentRuntimeToolGateway{
			server:        s,
			allowedTools:  allowedTools,
			suppressHooks: true,
		},
		MaxToolResultBytes: defaultSessionRunnerOutputLimitBytes,
	}
	maxToolRounds := int(numberValue(raw["maxToolRounds"]))
	if maxToolRounds <= 0 {
		maxToolRounds = 3
	}
	result, err := engine.Run(ctx, agentruntime.RunRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: "You are a Synon agent hook verifier. You may use read-only tools to inspect state. Return one JSON object only. Do not include Markdown."},
			{Role: "user", Content: userPrompt},
		},
		Tools:                s.agentRuntimeToolSchemasWithContext(ctx, allowedTools),
		MaxToolRounds:        maxToolRounds,
		MaxToolCallsPerRound: max(1, len(allowedTools)),
	})
	audit["timeoutMs"] = timeout.Milliseconds()
	audit["promptChars"] = len(userPrompt)
	audit["maxToolRounds"] = maxToolRounds
	audit["maxToolCallsPerRound"] = max(1, len(allowedTools))
	audit["allowedToolCount"] = len(s.agentRuntimeToolSchemasWithContext(ctx, allowedTools))
	if err != nil {
		audit["error"] = err.Error()
		return &agentRuntimeCommandHookResult{Audit: audit}
	}
	content := strings.TrimSpace(result.FinalMessage.Content)
	audit["responseBody"] = content
	audit["responseBytes"] = len(content)
	parsed := parseAgentRuntimeHookOutput(eventName, prompt, content)
	if parsed == nil {
		parsed = &agentRuntimeCommandHookResult{}
	}
	parsed.Audit = audit
	return parsed
}

func agentRuntimePromptHookPrompt(prompt string, inputJSON string) string {
	replaced := strings.ReplaceAll(prompt, "${ARGUMENTS}", inputJSON)
	replaced = strings.ReplaceAll(replaced, "$ARGUMENTS", inputJSON)
	if replaced != prompt {
		return replaced
	}
	return strings.TrimSpace(prompt) + "\n\nHook input JSON:\n" + inputJSON
}

func parseAgentRuntimeHookOutput(expectedEvent string, command string, stdout string) *agentRuntimeCommandHookResult {
	trimmed := strings.TrimSpace(stdout)
	result := &agentRuntimeCommandHookResult{}
	if trimmed == "" {
		return result
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		result.Audit = map[string]any{"parseError": err.Error()}
		return result
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(raw["decision"]))) {
	case "approve", "allow":
		result.Decision = "allow"
	case "block", "deny":
		result.Decision = "deny"
	case "ask":
		result.Decision = "ask"
	}
	result.Reason = strings.TrimSpace(agentRuntimeFirstNonEmpty(stringValue(raw["reason"]), stringValue(raw["message"])))
	result.SystemMessage = strings.TrimSpace(stringValue(raw["systemMessage"]))
	specific := mapValue(raw["hookSpecificOutput"])
	if len(specific) == 0 {
		return result
	}
	hookEvent := strings.TrimSpace(stringValue(specific["hookEventName"]))
	if hookEvent != "" && !agentRuntimeHookEventMatches(hookEvent, expectedEvent) && !strings.EqualFold(hookEvent, expectedEvent) {
		result.Audit = map[string]any{
			"parseError": fmt.Sprintf("hook returned incorrect event name: expected %s got %s", expectedEvent, hookEvent),
			"command":    command,
		}
		return result
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(specific["permissionDecision"]))) {
	case "allow":
		result.Decision = "allow"
	case "deny":
		result.Decision = "deny"
	case "ask":
		result.Decision = "ask"
	}
	result.Reason = strings.TrimSpace(agentRuntimeFirstNonEmpty(stringValue(specific["permissionDecisionReason"]), result.Reason))
	if updated := mapValue(specific["updatedInput"]); len(updated) > 0 {
		result.UpdatedInput = updated
	}
	result.AdditionalContext = strings.TrimSpace(stringValue(specific["additionalContext"]))
	return result
}
