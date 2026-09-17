package server

import (
	"encoding/json"
	"strings"

	"synon-go/internal/agentruntime"
)

// sessionRunnerFreshExecutionRequested recognizes an explicit request to run
// the calculation or analysis again. It does not choose a domain workflow or
// tool; it only prevents a fresh execution claim from being satisfied by old
// artifacts and prose from a prior input revision.
func sessionRunnerFreshExecutionRequested(messages []agentruntime.Message) bool {
	latest := ""
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.EqualFold(strings.TrimSpace(messages[index].Role), "user") {
			latest = strings.ToLower(strings.TrimSpace(messages[index].Content))
			break
		}
	}
	if latest == "" {
		return false
	}
	for _, negation := range []string{
		"不要重新计算", "无需重新计算", "不用重新计算", "不要重算", "无需重算",
		"do not recalculate", "don't recalculate", "without recalculating", "do not rerun", "don't rerun",
	} {
		if strings.Contains(latest, negation) {
			return false
		}
	}
	for _, request := range []string{
		"重新计算", "重新运算", "重新分析", "重新运行", "重算", "再计算", "再运行",
		"recalculate", "recompute", "rerun", "re-run", "run again", "re-analyze", "reanalyze",
	} {
		if strings.Contains(latest, request) {
			return true
		}
	}
	return false
}

func sessionRunnerHasFreshSuccessfulExecution(messages []agentruntime.Message) bool {
	calls := make(map[string]string)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if id := strings.TrimSpace(call.ID); id != "" && !call.RejectedBeforeExecution {
				calls[id] = strings.TrimSpace(call.Name)
			}
		}
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			continue
		}
		name := calls[strings.TrimSpace(message.ToolCallID)]
		if name == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil {
			continue
		}
		if trustedScientificExecutionToolName(name, result) != "" {
			return true
		}
	}
	return false
}

func sessionRunnerFreshExecutionCompletionError(
	requestMessages, resultMessages []agentruntime.Message,
) error {
	if !sessionRunnerFreshExecutionRequested(requestMessages) ||
		sessionRunnerHasFreshSuccessfulExecution(resultMessages) {
		return nil
	}
	detail := "the latest user input explicitly requested fresh computation or analysis, but this execution unit produced no successful execution receipt; prior artifacts and prior numerical prose cannot satisfy the new request"
	return sessionRunnerCompletionReviewCorrection{
		Summary: detail,
		Issues: []sessionRunnerReviewIssue{{
			MessageIndex: 0,
			Claim:        "perform the requested calculation or analysis in the applicable verified execution path before publishing a replacement conclusion",
			Verdict:      "fail",
			Severity:     "high",
			Evidence:     detail,
		}},
	}
}
