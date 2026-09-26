package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"synon-go/internal/agentruntime"
)

var (
	sessionRunnerFirstFailureCodeReportPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:最终|最后)[^。！？\n]{0,160}(?:报告|说明|列出|返回)[^。！？\n]{0,80}(?:首次|第一次|初次)[^。！？\n]{0,30}(?:失败|错误)[^。！？\n]{0,12}(?:代码|码)`),
		regexp.MustCompile(`(?i)(?:report|include|state|list|return)[^.!?\n]{0,100}(?:first|initial)[^.!?\n]{0,24}(?:failure|error)[^.!?\n]{0,16}code`),
	}
	sessionRunnerFirstFailureCodeReportNegationPattern = regexp.MustCompile(
		`(?i)(?:不要|无需|不必|禁止|do\s+not|don't|without)[^。！？.!?\n]{0,80}(?:首次|第一次|初次|first|initial)[^。！？.!?\n]{0,40}(?:失败|错误|failure|error)`,
	)
	sessionRunnerMachineFailureCodePattern = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9_.:-]{0,119}$`)
)

func sessionRunnerRequiresFirstFailureCodeReport(taskIntent string) bool {
	if sessionRunnerFirstFailureCodeReportNegationPattern.MatchString(taskIntent) {
		return false
	}
	for _, pattern := range sessionRunnerFirstFailureCodeReportPatterns {
		if pattern.MatchString(taskIntent) {
			return true
		}
	}
	return false
}

func (contract sessionRunnerExplicitToolContract) finalGaps(messages []agentruntime.Message, finalContent string) []string {
	if !contract.ReportFirstFailureCode {
		return nil
	}
	codes, found := sessionRunnerFirstFailedToolCodes(messages)
	if !found {
		return []string{"the final answer must report the first failure code, but no failed tool receipt exists in the current logical task"}
	}
	if len(codes) == 0 {
		return []string{"the final answer must report the first failure code, but the first failed tool receipt has no stable machine code"}
	}
	normalizedFinal := strings.ToLower(finalContent)
	for _, code := range codes {
		if strings.Contains(normalizedFinal, strings.ToLower(code)) {
			return nil
		}
	}
	return []string{fmt.Sprintf(
		"the final answer omits the first failed tool receipt code; include one of: %s",
		strings.Join(codes, ", "),
	)}
}

// Engine results may retain earlier assistant narration in FinalMessage for
// provider compatibility. Completion contracts must inspect the latest actual
// no-tool assistant candidate, not receipt-backed progress that happened to
// mention the requested code before the work finished.
func sessionRunnerLatestFinalCandidateContent(messages []agentruntime.Message, fallback string) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == "assistant" && len(message.ToolCalls) == 0 && strings.TrimSpace(message.Content) != "" {
			return message.Content
		}
	}
	return fallback
}

func sessionRunnerFirstFailedToolCodes(messages []agentruntime.Message) ([]string, bool) {
	for _, message := range messages {
		if message.Role != "tool" || strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil ||
			!agentruntime.ClassifyToolResult(result).Failed() ||
			agentruntime.ToolResultDidNotExecute(result) {
			continue
		}
		return sessionRunnerMachineFailureCodes(result), true
	}
	return nil, false
}

func sessionRunnerMachineFailureCodes(value any) []string {
	result := []string{}
	seen := map[string]struct{}{}
	add := func(value any) {
		code := strings.TrimSpace(stringValue(value))
		key := strings.ToLower(code)
		if !sessionRunnerMachineFailureCodePattern.MatchString(code) {
			return
		}
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		result = append(result, code)
	}
	record := mapValue(value)
	add(record["validation_code"])
	for _, raw := range anySliceValue(record["errors"]) {
		errorRecord := mapValue(raw)
		add(errorRecord["validation_code"])
		add(errorRecord["code"])
	}
	add(record["code"])
	return result
}
