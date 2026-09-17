package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
)

const maxResearchContinuationActions = 8

// researchSourceContinuation retains a source tool's own unfinished-work
// decision as process state. It does not infer scientific sufficiency from a
// source count, elapsed time, or model prose; only a structured, executed
// source result can open this continuation.
func researchSourceContinuation(call agentruntime.ToolCall, result any) map[string]any {
	object := runnerEvidenceDepthResultObject(mustMarshalRunnerCorrectionResult(result))
	if object == nil {
		return nil
	}
	decision := mapValue(object["retrievalDecision"])
	if !boolValue(decision["continueRecommended"], false) {
		return nil
	}
	actions := researchContinuationActions(object["nextActions"], call.Name)
	if len(actions) == 0 {
		return nil
	}
	continuation := map[string]any{
		"stop_reason":  strings.TrimSpace(stringValue(decision["stopReason"])),
		"next_actions": actions,
	}
	if session := mapValue(object["research_session"]); strings.TrimSpace(stringValue(session["id"])) != "" {
		continuation["research_session"] = map[string]any{
			"id": strings.TrimSpace(stringValue(session["id"])), "mode": "continue",
		}
	}
	return continuation
}

func researchContinuationActions(value any, toolName string) []any {
	actions := make([]any, 0, min(len(anySliceValue(value)), maxResearchContinuationActions))
	seen := map[string]struct{}{}
	for _, raw := range anySliceValue(value) {
		item := mapValue(raw)
		action := strings.ToLower(strings.TrimSpace(stringValue(item["action"])))
		query := strings.TrimSpace(stringValue(item["query"]))
		url := strings.TrimSpace(stringValue(item["url"]))
		if action == "" || query == "" && url == "" {
			continue
		}
		expectedTool := researchContinuationModelTool(
			action,
			strings.TrimSpace(stringValue(item["tool"])),
			strings.TrimSpace(toolName),
		)
		key := action + "\x00" + query + "\x00" + url + "\x00" + strings.ToLower(expectedTool)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized := map[string]any{"action": action}
		if query != "" {
			normalized["query"] = query
		}
		if url != "" {
			normalized["url"] = url
		}
		if reason := strings.TrimSpace(stringValue(item["reason"])); reason != "" {
			normalized["reason"] = reason
		}
		if expectedTool != "" {
			normalized["tool"] = expectedTool
		}
		actions = append(actions, normalized)
		if len(actions) >= maxResearchContinuationActions {
			break
		}
	}
	return actions
}

// researchPendingSourceAttemptContinuation follows the latest source-owned
// frontier. A terminal invocation consumes only the exact route it executed;
// usable material settles the lane, while an unusable result advances to the
// next discovered alternative without becoming report evidence.
func researchPendingSourceAttemptContinuation(attempts []sessionRunnerResearchSourceAttempt) map[string]any {
	var pending map[string]any
	for _, attempt := range attempts {
		if len(pending) > 0 {
			actionIndex := researchAttemptContinuationActionIndex(attempt, pending)
			if actionIndex < 0 {
				// An unrelated source route neither discharges nor replaces the
				// exact unfinished action. Any usable material it returned remains
				// evidence, but it cannot change the active navigation identity.
				continue
			}
			if attempt.MaterialUsable {
				pending = nil
			} else {
				pending = researchContinuationWithoutAction(pending, actionIndex)
				if len(pending) > 0 {
					// The attempted route settled without usable material. Advance
					// to the next source-owned alternative instead of replaying it.
					continue
				}
			}
		}
		if len(attempt.ResearchContinuation) == 0 {
			continue
		}
		pending = copyMapAny(attempt.ResearchContinuation)
		pending["source_event_id"] = attempt.EventID
		pending["source_tool_call_id"] = attempt.ToolCallID
		digest := sha256.Sum256([]byte(fmt.Sprintf(
			"%d\x00%s\x00%s", attempt.EventID, attempt.ToolCallID, attempt.ResultSHA256,
		)))
		pending["continuation_id"] = hex.EncodeToString(digest[:])
	}
	return pending
}

func researchAttemptContinuationActionIndex(
	attempt sessionRunnerResearchSourceAttempt,
	continuation map[string]any,
) int {
	if attempt.EventID <= int64(numberValue(continuation["source_event_id"])) {
		return -1
	}
	for index, raw := range anySliceValue(continuation["next_actions"]) {
		action := mapValue(raw)
		expectedTool := researchContinuationExpectedTool(action)
		if expectedTool == "" || normalizeAgentToolName(attempt.ToolName) != normalizeAgentToolName(expectedTool) {
			continue
		}
		if query := strings.TrimSpace(stringValue(action["query"])); query != "" &&
			strings.TrimSpace(stringValue(attempt.Request["query"])) != query {
			continue
		}
		if url := strings.TrimSpace(stringValue(action["url"])); url != "" &&
			canonicalWebResearchURL(stringValue(attempt.Request["url"])) != canonicalWebResearchURL(url) {
			continue
		}
		if expectedSession := strings.TrimSpace(stringValue(mapValue(continuation["research_session"])["id"])); expectedSession != "" && normalizeAgentToolName(expectedTool) == "webresearch" {
			actualSession := strings.TrimSpace(stringValue(mapValue(attempt.Request["research_session"])["id"]))
			if actualSession != expectedSession {
				continue
			}
		}
		return index
	}
	return -1
}

func researchContinuationWithoutAction(continuation map[string]any, actionIndex int) map[string]any {
	actions := anySliceValue(continuation["next_actions"])
	if actionIndex < 0 || actionIndex >= len(actions) {
		return copyMapAny(continuation)
	}
	remaining := make([]any, 0, len(actions)-1)
	remaining = append(remaining, actions[:actionIndex]...)
	remaining = append(remaining, actions[actionIndex+1:]...)
	if len(remaining) == 0 {
		return nil
	}
	result := copyMapAny(continuation)
	result["next_actions"] = remaining
	return result
}
