package server

import (
	"context"

	"errors"
	"fmt"

	"strconv"
	"strings"

	"time"

	"synon-go/internal/agentruntime"

	eventjournal "synon-go/internal/persistence/journal"

	sessionstore "synon-go/internal/persistence/sessions"

	"synon-go/internal/providers"
)

func (s *Server) executeRuntimeTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.runtimeStore == nil {
		return nil, errors.New("runtime store is not configured")
	}
	namespace := strings.TrimSpace(stringValue(input["namespace"]))
	if runtimeToolNamespaceReserved(namespace) {
		return nil, errors.New("runtime namespace is reserved for server authority")
	}
	switch toolName {
	case "runtime_set":
		entry, err := s.runtimeStore.Set(namespace, stringValue(input["key"]), input["value"])
		return map[string]any{"entry": entry}, err
	case "runtime_get":
		entry, ok, err := s.runtimeStore.Get(namespace, stringValue(input["key"]))
		return map[string]any{"entry": entry, "found": ok}, err
	case "runtime_list":
		entries, err := s.runtimeStore.List(namespace)
		if namespace == "" {
			visible := entries[:0]
			for _, entry := range entries {
				if !runtimeToolNamespaceReserved(entry.Namespace) {
					visible = append(visible, entry)
				}
			}
			entries = visible
		}
		return map[string]any{"entries": entries}, err
	case "runtime_delete":
		deleted, err := s.runtimeStore.Delete(namespace, stringValue(input["key"]))
		return map[string]any{"deleted": deleted}, err
	default:
		return nil, fmt.Errorf("unknown runtime tool: %s", toolName)
	}
}

func runtimeToolNamespaceReserved(namespace string) bool {
	return strings.EqualFold(strings.TrimSpace(namespace), agentRuntimeApprovalNamespace)
}

func (s *Server) executeCompactTool(ctx context.Context, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool("Compact", input); err != nil {
		return nil, err
	}
	if s.sessionStore == nil || s.eventJournal == nil || s.runtimeStore == nil {
		return nil, errors.New("compact runtime stores are not configured")
	}
	sessionID := strings.TrimSpace(stringValue(input["sessionId"]))
	if sessionID == "" {
		return nil, errors.New("Compact.sessionId is required")
	}
	session, ok, err := s.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	trigger := strings.ToLower(strings.TrimSpace(stringValue(input["trigger"])))
	if trigger == "" {
		trigger = "manual"
	}
	if trigger != "manual" && trigger != "auto" {
		return nil, fmt.Errorf("Compact.trigger must be manual or auto, got %q", trigger)
	}
	instructions := strings.TrimSpace(stringValue(input["instructions"]))
	projectID := ""
	projectName := ""
	projectPath := ""
	if session.Project != nil {
		projectID = session.Project.ID
		projectName = session.Project.Name
		projectPath = session.Project.Path
	}
	preResults := s.runAgentRuntimeLifecycleHooks(ctx, "PreCompact", trigger, map[string]any{
		"hook_event_name":      "PreCompact",
		"trigger":              trigger,
		"custom_instructions":  instructions,
		"session_id":           session.ID,
		"session_title":        session.Title,
		"session_workdir":      session.WorkDir,
		"session_project_id":   projectID,
		"session_project_name": projectName,
		"session_project_path": projectPath,
	})
	for _, result := range preResults {
		if result.Decision == "deny" || result.Decision == "block" {
			reason := strings.TrimSpace(result.Reason)
			if reason == "" {
				reason = "compact blocked by PreCompact hook"
			}
			return nil, errors.New(reason)
		}
	}
	mergedInstructions := compactMergedInstructions(instructions, preResults)
	entries, err := s.eventJournal.ReadAfter(sessionID, 0, sessionReplayLimit(numberValue(input["limit"])))
	if err != nil {
		return nil, err
	}
	replay := make([]any, 0, len(entries))
	for _, entry := range entries {
		replay = append(replay, entry)
	}
	compactedThroughEventID := maxEventID(0, entries)
	summary, err := buildCompactModelContextSummary(session, entries, mergedInstructions, trigger)
	if err != nil {
		return nil, err
	}
	summaryStatus := "deterministic-context"
	modelSummary := map[string]any{
		"attempted": false,
		"provider":  "deterministic",
	}
	if generated, metadata, err := s.generateCompactModelSummary(ctx, session, entries, mergedInstructions, trigger); err == nil && strings.TrimSpace(generated) != "" {
		summary = generated
		summaryStatus = "model-generated"
		modelSummary = metadata
	} else if err != nil {
		modelSummary = metadata
		modelSummary["error"] = err.Error()
	}
	modelContext := map[string]any{
		"summary":                 summary,
		"compactedThroughEventId": compactedThroughEventID,
		"entryCount":              len(entries),
		"instructions":            mergedInstructions,
		"retainedPolicy":          "runner_model_context_uses_summary_plus_events_after_compact",
	}
	checkpoint := map[string]any{
		"sessionId":               sessionID,
		"instructions":            mergedInstructions,
		"entryCount":              len(entries),
		"createdAt":               time.Now().UTC().Format(time.RFC3339Nano),
		"summaryStatus":           summaryStatus,
		"summary":                 summary,
		"modelContext":            modelContext,
		"modelSummary":            modelSummary,
		"compactedThroughEventId": compactedThroughEventID,
		"note":                    "Go runtime recorded an auditable compact handoff and model-context summary for future runner resumes.",
		"trigger":                 trigger,
		"replay":                  replay,
	}
	compactEvent, err := s.eventJournal.Append(sessionID, eventjournal.Message{
		"type":                    "session_compact",
		"summary":                 summary,
		"summaryStatus":           summaryStatus,
		"modelContext":            modelContext,
		"modelSummary":            modelSummary,
		"instructions":            mergedInstructions,
		"trigger":                 trigger,
		"entryCount":              len(entries),
		"compactedThroughEventId": compactedThroughEventID,
	}, eventjournal.Metadata{
		ClientMessageID: "compact-" + sessionID + "-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
	})
	if err != nil {
		return nil, err
	}
	if compactEvent != nil {
		checkpoint["compactEventId"] = compactEvent.EventID
		modelContext["compactEventId"] = compactEvent.EventID
	}
	archive, err := s.persistCompactionArchive(sessionID, entries, compactEvent)
	if err != nil {
		return nil, fmt.Errorf("persist compaction archive: %w", err)
	}
	if archive != nil {
		checkpoint["compactionArchiveId"] = archive.ID
		checkpoint["compactionIndex"] = archive.CompactionIndex
	}
	entry, err := s.runtimeStore.Set("compact", sessionID, checkpoint)
	if err != nil {
		return nil, err
	}
	postResults := s.runAgentRuntimeLifecycleHooks(ctx, "PostCompact", trigger, map[string]any{
		"hook_event_name": "PostCompact",
		"trigger":         trigger,
		"compact_summary": summary,
		"session_id":      session.ID,
	})
	return map[string]any{
		"entry":              entry,
		"event":              compactEvent,
		"checkpoint":         checkpoint,
		"preHookCount":       len(preResults),
		"postHookCount":      len(postResults),
		"customInstructions": mergedInstructions,
		"archive":            archive,
	}, nil
}

const maxCompactSummaryConversationEntries = 24
const maxCompactSummaryLineRunes = 700
const maxCompactRootTaskIntentRunes = 3000
const maxCompactModelTranscriptEntries = 200
const maxCompactModelTranscriptLineRunes = 3000

const compactModelSystemPrompt = `CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.

Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and the assistant's previous actions. The summary must preserve technical details, code patterns, architectural decisions, errors and fixes, and the exact current work needed to continue without losing context.

Return plain text containing an <analysis> block followed by a <summary> block. The runtime will keep only the <summary> content for future context.

The <summary> block must include these sections:
1. Primary Request and Intent
2. Key Technical Concepts
3. Files and Code Sections
4. Errors and fixes
5. Problem Solving
6. All user messages
7. Pending Tasks
8. Current Work
9. Optional Next Step`

func (s *Server) generateCompactModelSummary(ctx context.Context, session sessionstore.Session, entries []eventjournal.Entry, instructions string, trigger string) (string, map[string]any, error) {
	options := normalizeSessionRunnerChatOptions(s.compactSummarizer)
	metadata := map[string]any{
		"attempted": false,
		"provider":  "deterministic",
		"model":     "",
		"timeoutMs": options.CompactSummaryTimeout.Milliseconds(),
	}
	summaryCtx, cancelSummary := context.WithTimeout(ctx, options.CompactSummaryTimeout)
	defer cancelSummary()
	resolutionInput := providers.ResolutionInput{
		Context:          summaryCtx,
		ProjectID:        sessionRunnerProjectID(session),
		RequestTimeout:   options.RequestTimeout,
		MaxAttempts:      options.MaxAttempts,
		MaxResponseBytes: options.ModelResponseLimitBytes,
	}
	profile, authority, err := s.resolveSessionModelProfile(session, resolutionInput)
	if err != nil {
		metadata["authority"] = authority
		metadata["reason"] = "selected workspace model could not be resolved"
		return "", metadata, err
	}

	var client agentruntime.ModelClient
	if profile != nil {
		metadata["attempted"] = true
		metadata["authority"] = authority
		metadata["provider"] = firstNonEmpty(profile.Provider.Protocol, profile.Provider.Type)
		metadata["providerId"] = profile.Provider.ID
		metadata["model"] = profile.Model
		client, err = providers.NewRuntimeModelClient(*profile, s.httpClient, nil)
		if err != nil {
			return "", metadata, err
		}
	} else {
		if strings.TrimSpace(options.Endpoint) == "" || strings.TrimSpace(options.Model) == "" {
			metadata["reason"] = "compact summarizer endpoint or model is not configured"
			return "", metadata, nil
		}
		metadata["attempted"] = true
		metadata["authority"] = "static-compact-config"
		metadata["provider"] = "openai_chat"
		metadata["model"] = strings.TrimSpace(options.Model)
		client = agentruntime.OpenAIChatClient{
			Endpoint:       options.Endpoint,
			APIKey:         options.APIKey,
			Model:          options.Model,
			HTTPClient:     s.httpClient,
			RequestTimeout: options.RequestTimeout,
			MaxAttempts:    options.MaxAttempts,
		}
	}
	prompt, err := buildCompactModelPrompt(session, entries, instructions, trigger)
	if err != nil {
		return "", metadata, err
	}
	response, err := client.Complete(summaryCtx, agentruntime.ModelRequest{
		Messages: []agentruntime.Message{
			{Role: "system", Content: compactModelSystemPrompt},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		if errors.Is(summaryCtx.Err(), context.DeadlineExceeded) {
			metadata["timedOut"] = true
			return "", metadata, fmt.Errorf("compact model summary exceeded %s: %w", options.CompactSummaryTimeout, err)
		}
		return "", metadata, err
	}
	summary := extractCompactSummaryBlock(response.Message.Content)
	if summary == "" {
		return "", metadata, errors.New("compact model returned an empty summary")
	}
	metadata["source"] = "model"
	metadata["summaryChars"] = len(summary)
	return summary, metadata, nil
}

func buildCompactModelPrompt(session sessionstore.Session, entries []eventjournal.Entry, instructions string, trigger string) (string, error) {
	lines := []string{
		"## Compact Request",
		"Session: " + firstNonEmpty(session.Title, session.ID),
		"Trigger: " + firstNonEmpty(trigger, "manual"),
		fmt.Sprintf("Replay entries: %d", len(entries)),
		fmt.Sprintf("Compacted through event: %d", maxEventID(0, entries)),
	}
	if strings.TrimSpace(session.WorkDir) != "" {
		lines = append(lines, "Workdir: "+strings.TrimSpace(session.WorkDir))
	}
	if strings.TrimSpace(instructions) != "" {
		lines = append(lines, "", "## Compact Instructions", strings.TrimSpace(instructions))
	}
	if rootTask := compactRootTaskIntent(entries); rootTask != "" {
		lines = append(lines, "", "## Root Task Intent", rootTask)
	}
	lines = append(lines, "", "## Conversation Transcript")
	visible := compactVisibleConversationEntries(entries, maxCompactModelTranscriptEntries)
	if len(visible) == 0 {
		lines = append(lines, "No visible user or assistant messages are available.")
	} else {
		for _, entry := range visible {
			role := strings.TrimSpace(stringValue(entry.Message["role"]))
			text := compactText(runnerMessageText(entry.Message), maxCompactModelTranscriptLineRunes)
			if text == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("### Event %d %s", entry.EventID, role), text)
		}
	}
	durable, err := compactDurableRuntimeContextLines(entries)
	if err != nil {
		return "", err
	}
	if len(durable) > 0 {
		lines = append(lines, "", "## Durable Runtime State")
		lines = append(lines, durable...)
	}
	return strings.Join(lines, "\n"), nil
}

func extractCompactSummaryBlock(output string) string {
	text := strings.TrimSpace(output)
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	start := strings.Index(lower, "<summary>")
	end := strings.LastIndex(lower, "</summary>")
	if start >= 0 && end > start {
		start += len("<summary>")
		return strings.TrimSpace(text[start:end])
	}
	return strings.TrimSpace(removeCompactTagBlock(text, "analysis"))
}

func removeCompactTagBlock(text string, tag string) string {
	lower := strings.ToLower(text)
	open := "<" + strings.ToLower(tag) + ">"
	close := "</" + strings.ToLower(tag) + ">"
	for {
		start := strings.Index(lower, open)
		end := strings.Index(lower, close)
		if start < 0 || end < start {
			return text
		}
		end += len(close)
		text = text[:start] + text[end:]
		lower = strings.ToLower(text)
	}
}

func buildCompactModelContextSummary(session sessionstore.Session, entries []eventjournal.Entry, instructions string, trigger string) (string, error) {
	lines := []string{
		"Compact session summary",
		"Session: " + firstNonEmpty(session.Title, session.ID),
		"Trigger: " + firstNonEmpty(trigger, "manual"),
		fmt.Sprintf("Compacted through event: %d of %d replay entries", maxEventID(0, entries), len(entries)),
	}
	if session.WorkDir != "" {
		lines = append(lines, "Workdir: "+session.WorkDir)
	}
	if strings.TrimSpace(instructions) != "" {
		lines = append(lines, "Compact instructions: "+compactText(strings.TrimSpace(instructions), maxCompactSummaryLineRunes))
	}
	if rootTask := compactRootTaskIntent(entries); rootTask != "" {
		lines = append(lines, "Root task intent: "+rootTask)
	}
	recent := compactVisibleConversationEntries(entries, maxCompactSummaryConversationEntries)
	if len(recent) == 0 {
		lines = append(lines, "Recent conversation: no visible user or assistant messages were present before compaction.")
	} else {
		lines = append(lines, "Recent conversation:")
		for _, entry := range recent {
			role := strings.TrimSpace(stringValue(entry.Message["role"]))
			text := compactText(runnerMessageText(entry.Message), maxCompactSummaryLineRunes)
			if text == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("- #%d %s: %s", entry.EventID, role, text))
		}
	}
	durable, err := compactDurableRuntimeContextLines(entries)
	if err != nil {
		return "", err
	}
	lines = append(lines, durable...)
	return strings.Join(lines, "\n"), nil
}

// compactRootTaskIntent is intentionally separate from the recent conversation
// window. Long scientific tasks can contain hundreds of progress turns; the
// root user request remains the identity boundary for every continuation and
// must survive compaction even when it is no longer in the recent window.
func compactRootTaskIntent(entries []eventjournal.Entry) string {
	for _, entry := range entries {
		if !runnerEntryStartsNewLogicalTask(entry) {
			continue
		}
		if text := compactText(runnerMessageText(entry.Message), maxCompactRootTaskIntentRunes); text != "" {
			return text
		}
	}
	return ""
}

func compactVisibleConversationEntries(entries []eventjournal.Entry, limit int) []eventjournal.Entry {
	if limit <= 0 || len(entries) == 0 {
		return nil
	}
	visible := make([]eventjournal.Entry, 0, len(entries))
	for _, entry := range entries {
		role := strings.TrimSpace(stringValue(entry.Message["role"]))
		if role != "user" && role != "assistant" {
			continue
		}
		if runnerMessageText(entry.Message) == "" {
			continue
		}
		visible = append(visible, entry)
	}
	if len(visible) <= limit {
		return visible
	}
	return visible[len(visible)-limit:]
}

func compactMergedInstructions(base string, results []agentRuntimeCommandHookResult) string {
	parts := []string{}
	if strings.TrimSpace(base) != "" {
		parts = append(parts, strings.TrimSpace(base))
	}
	for _, result := range results {
		text := strings.TrimSpace(agentRuntimeFirstNonEmpty(result.AdditionalContext, result.SystemMessage, agentRuntimeHookStdout(result)))
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func agentRuntimeHookStdout(result agentRuntimeCommandHookResult) string {
	if result.Audit == nil {
		return ""
	}
	stdout := strings.TrimSpace(stringValue(result.Audit["stdout"]))
	if stdout == "" {
		return ""
	}
	if strings.HasPrefix(stdout, "{") || strings.HasPrefix(stdout, "[") {
		return ""
	}
	return stdout
}
