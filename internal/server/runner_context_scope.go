package server

import (
	"context"

	"encoding/json"
	"errors"
	"fmt"

	"strings"

	"unicode"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"

	sessionstore "synon-go/internal/persistence/sessions"
)

func runnerRequiresPublicScientificDownloadRecovery(entries []eventjournal.Entry) bool {
	for index := len(entries) - 1; index >= 0; index-- {
		message := entries[index].Message
		if stringValue(message["type"]) != "runner_checkpoint" ||
			!strings.EqualFold(strings.TrimSpace(stringValue(message["status"])), "interrupted") {
			continue
		}
		switch strings.TrimSpace(stringValue(message["reason_code"])) {
		case "real_scientific_evidence_required":
			return true
		case sessionRunnerToolRoundNoProgressReasonCode,
			sessionRunnerToolRoundNoProgressExhaustedReasonCode:
			return runnerInterruptionFollowsPublicScientificDownload(entries, index)
		case sessionRunnerModelProtocolErrorReasonCode:
			return strings.Contains(
				strings.ToLower(stringValue(message["resume_detail"])),
				"download_public_scientific_file",
			)
		default:
			return false
		}
	}
	return false
}

func runnerInterruptionFollowsPublicScientificDownload(entries []eventjournal.Entry, interruptionIndex int) bool {
	for index := interruptionIndex - 1; index >= 0; index-- {
		message := entries[index].Message
		if stringValue(message["type"]) != "runner_checkpoint" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(stringValue(message["status"])), "interrupted") {
			return false
		}
		if !strings.EqualFold(strings.TrimSpace(stringValue(message["toolPhase"])), "completed") {
			continue
		}
		return strings.EqualFold(strings.TrimSpace(stringValue(message["toolName"])), "download_public_scientific_file")
	}
	return false
}

func requiredScientificCapabilitiesFromRunnerEntries(entries []eventjournal.Entry) []string {
	capabilities := make([]string, 0)
	for _, entry := range entries {
		message := entry.Message
		if stringValue(message["type"]) != "runner_checkpoint" ||
			stringValue(message["status"]) != "completed" ||
			stringValue(message["toolPhase"]) != "completed" ||
			!isCompletedSkillToolName(stringValue(message["toolName"])) {
			continue
		}
		capabilities = append(capabilities, stringArrayValue(message["requiredScientificCapabilities"])...)
	}
	if records, _, err := latestPersistedSessionRunnerToolContinuity(entries); err == nil {
		for _, record := range records {
			if record.Successful && record.SkillName != "" {
				capabilities = append(capabilities, record.RequiredScientificCapabilities...)
			}
		}
	}
	return uniqueSortedScientificCapabilities(capabilities)
}

func completedSkillNamesFromRunnerEntries(entries []eventjournal.Entry) []string {
	names := make([]string, 0)
	for _, entry := range entries {
		message := entry.Message
		if stringValue(message["type"]) != "runner_checkpoint" ||
			stringValue(message["status"]) != "completed" ||
			stringValue(message["toolPhase"]) != "completed" ||
			!isCompletedSkillToolName(stringValue(message["toolName"])) {
			continue
		}
		input, _ := message["toolInput"].(map[string]any)
		name := strings.TrimPrefix(strings.TrimSpace(stringValue(input["skill"])), "/")
		if name != "" {
			names = append(names, name)
		}
	}
	if records, _, err := latestPersistedSessionRunnerToolContinuity(entries); err == nil {
		for _, record := range records {
			if record.Successful && record.SkillName != "" {
				names = append(names, record.SkillName)
			}
		}
	}
	return uniqueSortedFolded(names)
}

func completedSkillInvocationKeysFromRunnerEntries(entries []eventjournal.Entry) []string {
	keys := make([]string, 0)
	for _, entry := range entries {
		message := entry.Message
		if stringValue(message["type"]) != "runner_checkpoint" ||
			stringValue(message["status"]) != "completed" ||
			stringValue(message["toolPhase"]) != "completed" ||
			!isCompletedSkillToolName(stringValue(message["toolName"])) {
			continue
		}
		input, _ := message["toolInput"].(map[string]any)
		if key := runtimeSkillInvocationKeyFromInput(input); key != "" {
			keys = append(keys, key)
		}
	}
	if records, _, err := latestPersistedSessionRunnerToolContinuity(entries); err == nil {
		for _, record := range records {
			if !record.Successful || strings.TrimSpace(record.SkillName) == "" {
				continue
			}
			if key := runtimeSkillInvocationKey(record.SkillName, "", ""); key != "" {
				keys = append(keys, key)
			}
		}
	}
	return uniqueSortedFolded(keys)
}

func isCompletedSkillToolName(name string) bool {
	name = strings.TrimSpace(name)
	return name == "skill" || name == "Skill"
}

// runnerEntriesForCurrentLogicalInput keeps durable scientific execution
// signals across retries of the same input revision, but never lets evidence
// from an older user request classify a newer one. Transcript attempts are the
// authority for that boundary; malformed or unresolvable legacy entries fail
// closed instead of silently crossing it.
func (s *Server) runnerEntriesForCurrentLogicalInput(
	ctx context.Context,
	entries []eventjournal.Entry,
	run *sessionRunnerChatRun,
) ([]eventjournal.Entry, error) {
	if run == nil || run.Transcript == nil {
		return entries, nil
	}
	if s == nil || s.transcriptStore == nil {
		return nil, errors.New("transcript runtime is unavailable while scoping scientific evidence")
	}
	currentRevision := run.Transcript.Claim.ClaimedInputRevision
	if currentRevision <= 0 {
		return nil, errors.New("transcript input revision is unavailable while scoping scientific evidence")
	}
	revisions := map[int]int64{run.Attempt: currentRevision}
	for _, entry := range entries {
		attempt, ok := exactPositiveInt(entry.Message["runnerAttempt"])
		if !ok || attempt == run.Attempt {
			continue
		}
		if _, loaded := revisions[attempt]; loaded {
			continue
		}
		state, err := s.transcriptStore.GetRunnerRuntimeState(
			ctx, run.Transcript.Stream.UID, run.Transcript.Stream.OwnerID, int64(attempt),
		)
		if err != nil {
			return nil, fmt.Errorf("scope runner attempt %d scientific evidence: %w", attempt, err)
		}
		revisions[attempt] = state.ClaimedInputRevision
	}
	// A user_input_response advances the transport input revision so a parked
	// runner can be reclaimed, but it does not start a new scientific task. Scope
	// durable evidence from the latest canonical task-intent row when that row is
	// present in the replay. This preserves already loaded Skills and verified
	// tool receipts across approval, AskUser, plan, and recovery responses while
	// still fencing every genuinely newer user task. Legacy replays that predate
	// task-intent classification retain the input-revision fallback below.
	filtered, taskIntentBounded := filterRunnerEntriesByLatestTaskIntent(entries)
	if !taskIntentBounded {
		filtered = filterRunnerEntriesByInputRevision(entries, revisions, currentRevision)
	}
	filtered = appendRunnerResolvedInputResponses(entries, filtered)
	quarantineLegacy, err := runnerEntriesRequireLegacyReviewPolicyQuarantine(filtered)
	if err != nil {
		return nil, err
	}
	if quarantineLegacy {
		// The latest policy for this logical input was produced before scientific
		// signals were task-intent scoped. Do not let later off-topic retries make
		// that legacy classification self-perpetuating. Current-attempt execution
		// can establish a fresh v2 policy from new trusted evidence.
		return appendRunnerResolvedInputResponses(entries, filterRunnerEntriesByAttempt(filtered, run.Attempt)), nil
	}
	return filtered, nil
}

func filterRunnerEntriesByLatestTaskIntent(entries []eventjournal.Entry) ([]eventjournal.Entry, bool) {
	start := -1
	for index, entry := range entries {
		if runnerEntryStartsNewLogicalTask(entry) {
			start = index
		}
	}
	if start < 0 {
		return nil, false
	}
	return append([]eventjournal.Entry(nil), entries[start:]...), true
}

func runnerEntryIsInputResponse(entry eventjournal.Entry) bool {
	message := entry.Message
	if strings.TrimSpace(stringValue(message["messageOrigin"])) == "input_response" ||
		strings.TrimSpace(entry.SourceEventType) == "user_input_response" {
		return true
	}
	// Frame-ref input responses are projected as user_message events whose
	// structured content is a tool_result block.
	return runnerValueContainsToolResult(message["content"]) ||
		strings.HasPrefix(strings.TrimSpace(runnerMessageText(message)), "[tool result id=")
}

// runnerEntryStartsNewLogicalTask distinguishes a new task intent from a
// structured answer that belongs to the task already in progress. Resolved
// input responses may be appended to a scoped replay after runner checkpoints;
// treating them as a new user turn would hide the durable correction that the
// answer is meant to continue.
func runnerEntryStartsNewLogicalTask(entry eventjournal.Entry) bool {
	message := entry.Message
	return strings.TrimSpace(stringValue(message["role"])) == "user" &&
		strings.TrimSpace(stringValue(message["type"])) == "message" &&
		!runnerEntryIsInputResponse(entry) &&
		!transcriptstore.IsExplicitTaskContinuationDirective(runnerMessageText(message))
}

func runnerEntriesContainResolvedInputResponse(entries []eventjournal.Entry) bool {
	for _, entry := range entries {
		message := entry.Message
		origin := strings.TrimSpace(firstNonEmpty(
			stringValue(message["messageOrigin"]), stringValue(message["message_origin"]),
		))
		if origin == "input_response" || strings.TrimSpace(entry.SourceEventType) == "user_input_response" {
			return true
		}
		if strings.TrimSpace(stringValue(message["role"])) != "user" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(runnerMessageText(message)), "Resolved input requests:") {
			return true
		}
	}
	return false
}

func appendRunnerResolvedInputResponses(all, scoped []eventjournal.Entry) []eventjournal.Entry {
	latestUserTaskIndex := -1
	latestUserTaskText := ""
	isTaskIntent := func(entry eventjournal.Entry) bool {
		message := entry.Message
		return !runnerEntryIsInputResponse(entry) &&
			strings.TrimSpace(stringValue(message["role"])) == "user" &&
			(strings.TrimSpace(stringValue(message["type"])) == "message" ||
				strings.TrimSpace(stringValue(message["messageOrigin"])) == "task_intent")
	}
	normalizeTaskIntent := func(message eventjournal.Message) string {
		return strings.Join(strings.Fields(strings.TrimSpace(runnerMessageText(message))), " ")
	}
	for index, entry := range all {
		message := entry.Message
		if isTaskIntent(entry) {
			latestUserTaskIndex = index
			latestUserTaskText = normalizeTaskIntent(message)
		}
	}
	if latestUserTaskIndex < 0 {
		return scoped
	}
	result := append([]eventjournal.Entry(nil), scoped...)
	seen := make(map[int64]struct{}, len(result))
	for _, entry := range result {
		if entry.EventID > 0 {
			seen[entry.EventID] = struct{}{}
		}
	}
	appendEntry := func(entry eventjournal.Entry) {
		if entry.EventID > 0 {
			if _, duplicate := seen[entry.EventID]; duplicate {
				return
			}
			seen[entry.EventID] = struct{}{}
		}
		result = append(result, entry)
	}
	appendResolvedRange := func(start, end int) {
		if start < 0 {
			start = 0
		}
		if end > len(all) {
			end = len(all)
		}
		for _, entry := range all[start:end] {
			if !runnerEntriesContainResolvedInputResponse([]eventjournal.Entry{entry}) {
				continue
			}
			appendEntry(entry)
		}
	}
	// A frame can receive the same task text more than once while a previous
	// attempt is being recovered. Walk back over the whole contiguous run of
	// identical task intents, not only the immediately preceding one: a retry
	// may have been submitted after an earlier retry, while the durable answers
	// still belong to the first task in that run. Stop at a genuinely different
	// task so answers cannot cross a user-request boundary.
	contiguousSameTaskStart := latestUserTaskIndex
	for index := latestUserTaskIndex - 1; index >= 0; index-- {
		if !isTaskIntent(all[index]) {
			continue
		}
		if normalizeTaskIntent(all[index].Message) != latestUserTaskText {
			break
		}
		contiguousSameTaskStart = index
	}
	appendResolvedRange(contiguousSameTaskStart+1, len(all))
	return result
}

func filterRunnerEntriesByInputRevision(
	entries []eventjournal.Entry,
	attemptRevisions map[int]int64,
	currentRevision int64,
) []eventjournal.Entry {
	filtered := make([]eventjournal.Entry, 0, len(entries))
	for _, entry := range entries {
		attempt, ok := exactPositiveInt(entry.Message["runnerAttempt"])
		if !ok || attemptRevisions[attempt] != currentRevision {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func filterRunnerEntriesByAttempt(entries []eventjournal.Entry, attempt int) []eventjournal.Entry {
	filtered := make([]eventjournal.Entry, 0, len(entries))
	for _, entry := range entries {
		entryAttempt, ok := exactPositiveInt(entry.Message["runnerAttempt"])
		if !ok || entryAttempt != attempt {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func runnerEntriesRequireLegacyReviewPolicyQuarantine(entries []eventjournal.Entry) (bool, error) {
	for index := len(entries) - 1; index >= 0; index-- {
		message := entries[index].Message
		if stringValue(message["type"]) != "runner_checkpoint" ||
			stringValue(message["status"]) != "completed" ||
			stringValue(message["toolPhase"]) != "review_policy" {
			continue
		}
		raw, found := message["resolvedReviewPolicy"]
		if !found {
			return false, errors.New("review-policy checkpoint is missing its server policy")
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return false, errors.New("review-policy checkpoint cannot be decoded")
		}
		var envelope struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(encoded, &envelope); err != nil {
			return false, errors.New("review-policy checkpoint cannot be decoded")
		}
		switch strings.TrimSpace(envelope.Schema) {
		case sessionRunnerReviewPolicySchema, previousSessionRunnerReviewPolicySchema:
			return false, nil
		case legacySessionRunnerReviewPolicySchema:
			return true, nil
		default:
			return false, errors.New("review-policy checkpoint has an unsupported schema")
		}
	}
	return false, nil
}

type sessionRunnerAutoCompactResult struct {
	Triggered       bool
	Message         string
	TriggerReason   string
	EstimatedTokens int
	Threshold       int
	ContextWindow   int
	ContextPercent  int
}

// Automatic compaction protects the provider context boundary, not an
// arbitrary task-size budget. Keep headroom for one large tool result and the
// next model output, but scale with the provider-declared context window.
const defaultRunnerAutoCompactContextPercent = 80
const defaultRunnerContextWindow = 1_000_000

func (s *Server) autoCompactSessionForRunner(ctx context.Context, options SessionRunnerChatOptions, session sessionstore.Session, entries []eventjournal.Entry, run *sessionRunnerChatRun) ([]eventjournal.Entry, sessionRunnerAutoCompactResult, error) {
	result := sessionRunnerAutoCompactResult{}
	if s == nil || s.settingsStore == nil {
		return entries, result, nil
	}
	transcriptBacked := run != nil && run.Transcript != nil
	if transcriptBacked {
		if s.transcriptStore == nil {
			return entries, result, errors.New("transcript compact runtime is not configured")
		}
	} else if s.eventJournal == nil || s.runtimeStore == nil || s.sessionStore == nil {
		return entries, result, nil
	}
	if !s.autoCompactEnabled() {
		return entries, result, nil
	}
	estimated, err := sessionRunnerProviderContextTokenEstimate(options.SystemPrompt, entries)
	if err != nil {
		return entries, result, err
	}
	contextWindow := runnerContextWindow(options)
	threshold := s.autoCompactTokenThreshold(contextWindow)
	forceAfterProviderPressure := providerContextPressureRequiresCompaction(entries)
	result.EstimatedTokens = estimated
	result.Threshold = threshold
	result.ContextWindow = contextWindow
	result.ContextPercent = defaultRunnerAutoCompactContextPercent
	if !forceAfterProviderPressure && (threshold <= 0 || estimated < threshold) {
		return entries, result, nil
	}
	result.TriggerReason = "estimated_context_threshold"
	instructions := fmt.Sprintf(
		"Automatic compact before runner model call: estimated context %d tokens reached the %d-token threshold (%d%% of the provider-declared %d-token context window).",
		estimated, threshold, defaultRunnerAutoCompactContextPercent, contextWindow,
	)
	if forceAfterProviderPressure {
		result.TriggerReason = sessionRunnerProviderContextPressureReasonCode
		instructions = fmt.Sprintf(
			"Automatic compact before runner model call after the provider rejected the prior execution unit for context pressure. Preserve the original task, verified evidence, tool outcomes, artifact paths, unresolved work, and failure provenance. Estimated context=%d tokens, configured threshold=%d, context window=%d.",
			estimated, threshold, contextWindow,
		)
	}
	result.Message = fmt.Sprintf(
		"auto compact completed before model call: reason=%s estimated=%d threshold=%d context_window=%d percent=%d",
		result.TriggerReason, estimated, threshold, contextWindow, defaultRunnerAutoCompactContextPercent,
	)
	if transcriptBacked {
		summary, summaryErr := buildCompactModelContextSummary(session, entries, instructions, "auto")
		if summaryErr != nil {
			return entries, result, summaryErr
		}
		summary = s.compactSummaryWithPlanNavigation(session.ID, summary)
		// Automatic recovery must be reproducible even with a weaker selected
		// conversation model. The deterministic handoff is derived from durable
		// user messages, exact tool receipts, failures and pending work; replacing
		// it with a model paraphrase can invent completion, stale date windows or
		// duplicate already-finished work. Manual Compact remains model-enriched,
		// while the runner's automatic continuity path has one authoritative form.
		summaryStatus := "deterministic-context"
		modelSummary := map[string]any{
			"attempted": false,
			"provider":  "deterministic",
			"reason":    "automatic runner compaction uses durable deterministic context",
		}
		compactedThroughEventID := maxEntryIDFromEntries(entries)
		toolContinuity, continuityErr := sessionRunnerToolContinuityRecords(entries)
		if continuityErr != nil {
			return entries, result, continuityErr
		}
		compactDetails := map[string]any{
			"summary":                 summary,
			"summaryStatus":           summaryStatus,
			"modelSummary":            modelSummary,
			"trigger":                 "auto",
			"compactedThroughEventId": compactedThroughEventID,
			"estimatedTokens":         estimated,
			"threshold":               threshold,
			"contextWindow":           contextWindow,
			"contextPercent":          defaultRunnerAutoCompactContextPercent,
			"reason":                  result.TriggerReason,
		}
		if len(toolContinuity) > 0 {
			compactDetails[sessionRunnerToolContinuityField] = toolContinuity
		}
		if err := s.checkpointChatTool(options, run, "completed", result.Message, "", "auto_compact", compactDetails); err != nil {
			return entries, result, err
		}
		updated, err := s.loadTranscriptRunnerReplay(ctx, run.Transcript, int(options.ReplayLimit), int(options.ReplayLimit))
		if err != nil {
			return entries, result, err
		}
		var boundary *eventjournal.Entry
		for index := len(updated) - 1; index >= 0; index-- {
			if isCompactJournalEntry(updated[index]) {
				candidate := updated[index]
				boundary = &candidate
				break
			}
		}
		if _, err := s.persistCompactionArchive(session.ID, entries, boundary); err != nil {
			return entries, result, fmt.Errorf("persist transcript compaction archive: %w", err)
		}
		result.Triggered = true
		return updated, result, nil
	}
	_, err = s.executeCompactTool(ctx, map[string]any{
		"sessionId":    session.ID,
		"trigger":      "auto",
		"instructions": instructions,
		"limit":        float64(maxInt(len(entries)+10, int(options.ReplayLimit))),
	})
	if err != nil {
		return entries, result, err
	}
	updated, err := s.eventJournal.ReadAfter(session.ID, 0, int(options.ReplayLimit))
	if err != nil {
		return entries, result, err
	}
	result.Triggered = true
	if run != nil {
		if err := s.checkpointChatTool(options, run, "completed", result.Message, "", "auto_compact", map[string]any{
			"estimatedTokens": estimated,
			"threshold":       threshold,
			"contextWindow":   contextWindow,
			"contextPercent":  defaultRunnerAutoCompactContextPercent,
			"reason":          result.TriggerReason,
		}); err != nil {
			return entries, result, err
		}
	}
	return updated, result, nil
}

func (s *Server) compactSummaryWithPlanNavigation(frameID, summary string) string {
	if strings.Contains(summary, generatedPlanResearchNavigationSchema) {
		return summary
	}
	navigation := s.generatedPlanDesiredOutputsContext(frameID)
	if navigation == "" {
		return summary
	}
	return summary + "\nDurable plan navigation state:\n" + navigation
}

// providerContextPressureRequiresCompaction reports whether a provider context
// rejection is still unresolved in the durable replay. A later user follow-up
// does not erase the pressure: compacting preserves that input while avoiding a
// second identical rejection. A durable compact boundary or a newer completed
// assistant turn clears the recovery trigger.
func providerContextPressureRequiresCompaction(entries []eventjournal.Entry) bool {
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if isCompactJournalEntry(entry) {
			return false
		}
		message := entry.Message
		if strings.TrimSpace(stringValue(message["type"])) == "message" &&
			strings.TrimSpace(stringValue(message["role"])) == "assistant" {
			return false
		}
		if strings.TrimSpace(stringValue(message["type"])) != "runner_checkpoint" {
			continue
		}
		reason := strings.TrimSpace(stringValue(message["reason_code"]))
		if reason == "" {
			reason = strings.TrimSpace(stringValue(message["reasonCode"]))
		}
		if reason == sessionRunnerProviderContextPressureReasonCode {
			return true
		}
	}
	return false
}

func (s *Server) autoCompactEnabled() bool {
	setting, ok, err := s.settingsStore.Get(configStoreKey("autoCompactEnabled"))
	if err != nil {
		return false
	}
	if !ok {
		return true
	}
	return boolValue(setting.Value, false)
}

func (s *Server) autoCompactTokenThreshold(contextWindow int) int {
	setting, ok, err := s.settingsStore.Get(configStoreKey("autoCompactTokenThreshold"))
	if err == nil && ok {
		if value := int(numberValue(setting.Value)); value > 0 {
			return value
		}
	}
	if contextWindow <= 0 {
		contextWindow = defaultRunnerContextWindow
	}
	threshold := contextWindow * defaultRunnerAutoCompactContextPercent / 100
	return threshold
}

func runnerContextWindow(options SessionRunnerChatOptions) int {
	for _, key := range []string{"contextWindow", "context_window", "contextLimit", "context_limit"} {
		if value := int(numberValue(options.RuntimeSessionConfig[key])); value > 0 && value <= 10_000_000 {
			return value
		}
	}
	return defaultRunnerContextWindow
}

func estimateChatMessagesTokens(messages []chatCompletionMessage) int {
	total := 0
	for _, message := range messages {
		total += 4
		total += estimateTextTokens(message.Role)
		total += estimateTextTokens(message.Content)
		for _, call := range message.ToolCalls {
			total += estimateTextTokens(call.ID) + estimateTextTokens(call.Function.Name) + estimateTextTokens(call.Function.Arguments)
		}
	}
	return total
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	asciiRunes := 0
	nonASCIITokens := 0
	for _, current := range text {
		if current <= unicode.MaxASCII {
			asciiRunes++
			continue
		}
		// CJK text and most non-ASCII symbols do not follow the English
		// four-characters-per-token heuristic. Counting each rune prevents
		// long Chinese conversations from compacting far too late.
		nonASCIITokens++
	}
	return maxInt(1, nonASCIITokens+(asciiRunes+3)/4)
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
