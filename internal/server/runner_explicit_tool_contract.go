package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"synon-go/internal/agentruntime"
)

type sessionRunnerExplicitToolRequirement struct {
	Name       string
	MinResults int
}

// sessionRunnerExplicitToolContract is a typed completion contract for tasks
// whose user-visible objective is to exercise named tools. ReceiptOnly is set
// only when the user scopes both execution and the final answer to those named
// operations. It never converts a scientific deliverable into a tool receipt.
type sessionRunnerExplicitToolContract struct {
	Requirements []sessionRunnerExplicitToolRequirement
	RequireMCP   bool
	ReceiptOnly  bool
}

var (
	sessionRunnerExplicitSkillTokenPattern = regexp.MustCompile(`(?i)(?:^|[^a-z0-9_])skills?(?:$|[^a-z0-9_])`)
	sessionRunnerToolOnlyExecutionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:只|仅)执行[^。！？\n]{0,240}(?:search_skills|web_search|manage_packages|(?:^|[^a-z0-9_])skills?(?:$|[^a-z0-9_])|mcp)`),
		regexp.MustCompile(`(?i)\b(?:only|solely)\s+(?:execute|perform|run|call|use)[^.!?\n]{0,240}(?:search_skills|web_search|manage_packages|\bskills?\b|\bmcp\b)`),
	}
	sessionRunnerToolOnlyFinalPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:最后|最终)[^。！？\n]{0,160}(?:只|仅)[^。！？\n]{0,160}(?:报告|说明|列出|返回)[^。！？\n]{0,160}(?:工具|search_skills|web_search|manage_packages|skill|mcp)`),
		regexp.MustCompile(`(?i)\b(?:finally|final(?:\s+answer)?)\b[^.!?\n]{0,160}\bonly\b[^.!?\n]{0,160}\b(?:report|state|list|return)\b[^.!?\n]{0,160}(?:\btools?\b|search_skills|web_search|manage_packages|\bskills?\b|\bmcp\b)`),
	}
)

func buildSessionRunnerExplicitToolContract(taskIntent string) sessionRunnerExplicitToolContract {
	normalized := strings.ToLower(strings.TrimSpace(taskIntent))
	compact := strings.Join(strings.Fields(normalized), "")
	if normalized == "" {
		return sessionRunnerExplicitToolContract{}
	}
	contract := sessionRunnerExplicitToolContract{}
	add := func(name string, minimum int) {
		for index := range contract.Requirements {
			if contract.Requirements[index].Name != name {
				continue
			}
			if minimum > contract.Requirements[index].MinResults {
				contract.Requirements[index].MinResults = minimum
			}
			return
		}
		contract.Requirements = append(contract.Requirements, sessionRunnerExplicitToolRequirement{Name: name, MinResults: minimum})
	}
	if strings.Contains(normalized, "search_skills") {
		add("search_skills", 1)
	}
	if strings.Contains(normalized, "web_search") {
		add("web_search", 1)
	}
	if strings.Contains(normalized, "manage_packages") {
		add("manage_packages", 1)
	}
	if sessionRunnerExplicitSkillTokenPattern.MatchString(normalized) && containsAny(normalized, []string{
		"使用", "调用", "加载", "use", "using", "invoke", "load",
	}) {
		minimum := 1
		if strings.Contains(compact, "至少两个") || strings.Contains(normalized, "at least two") {
			minimum = 2
		}
		add("skill", minimum)
	}
	contract.RequireMCP = strings.Contains(normalized, "mcp") &&
		(strings.Contains(normalized, "真实") || strings.Contains(normalized, "actual") || strings.Contains(normalized, "method") || strings.Contains(normalized, "方法"))
	contract.ReceiptOnly = len(contract.Requirements) > 0 &&
		matchesSessionRunnerContractPattern(taskIntent, sessionRunnerToolOnlyExecutionPatterns) &&
		matchesSessionRunnerContractPattern(taskIntent, sessionRunnerToolOnlyFinalPatterns) &&
		len(sessionRunnerExplicitDeliverableNames(taskIntent)) == 0 &&
		len(sessionRunnerExplicitDeliverableFormats(taskIntent)) == 0 &&
		!sessionRunnerRequiresDurableArtifact(taskIntent)
	return contract
}

func matchesSessionRunnerContractPattern(value string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func (contract sessionRunnerExplicitToolContract) gaps(messages []agentruntime.Message) []string {
	completed := sessionRunnerSuccessfulToolExecutionIndex(messages)
	gaps := make([]string, 0, len(contract.Requirements)+1)
	for _, requirement := range contract.Requirements {
		actual := completed[requirement.Name]
		if actual >= requirement.MinResults {
			continue
		}
		if requirement.MinResults == 1 {
			gaps = append(gaps, fmt.Sprintf("the explicitly requested %s action has no successful tool result", requirement.Name))
			continue
		}
		gaps = append(gaps, fmt.Sprintf(
			"the task requires at least %d successful %s results, but the durable tool index contains %d",
			requirement.MinResults, requirement.Name, actual,
		))
	}
	if contract.RequireMCP {
		hasMCP := false
		for name, count := range completed {
			if count > 0 && strings.HasPrefix(name, "mcp__") {
				hasMCP = true
				break
			}
		}
		if !hasMCP {
			gaps = append(gaps, "the explicitly requested real MCP method execution has no successful MCP tool result")
		}
	}
	// Data-domain coverage is semantic, not structural. A single connector can
	// expose several independent sources (for example chemistry aggregators),
	// while two methods on two connectors can still represent the same source.
	// The evidence reviewer sees the attested method names, inputs, and results
	// and owns that judgment. Do not reinterpret examples in free-form user text
	// as hard requirements or substitute connector count for domain count here.
	return gaps
}

func (contract sessionRunnerExplicitToolContract) receiptOnlySatisfied(messages []agentruntime.Message) bool {
	if !contract.ReceiptOnly || len(contract.gaps(messages)) != 0 {
		return false
	}
	allowed := make(map[string]struct{}, len(contract.Requirements))
	for _, requirement := range contract.Requirements {
		allowed[requirement.Name] = struct{}{}
	}
	for name, count := range sessionReviewerToolExecutionIndex(messages) {
		if count.Results == 0 {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(name))
		if _, ok := allowed[normalized]; ok {
			continue
		}
		if contract.RequireMCP && strings.HasPrefix(normalized, "mcp__") {
			continue
		}
		return false
	}
	return true
}

// sessionRunnerExplicitToolContractGaps enforces only tool actions the user
// named explicitly. It does not prescribe an order or infer domain workflows.
func sessionRunnerExplicitToolContractGaps(taskIntent string, messages []agentruntime.Message) []string {
	return buildSessionRunnerExplicitToolContract(taskIntent).gaps(messages)
}

func sessionRunnerSuccessfulToolExecutionIndex(messages []agentruntime.Message) map[string]int {
	callNames := make(map[string]string)
	succeeded := make(map[string]bool)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			callID := strings.TrimSpace(call.ID)
			name := strings.ToLower(strings.TrimSpace(call.Name))
			if callID != "" && name != "" {
				callNames[callID] = name
			}
		}
		if message.Role != "tool" || strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		var result any
		if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
			result = message.Content
		}
		if agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded {
			succeeded[strings.TrimSpace(message.ToolCallID)] = true
		}
	}
	index := make(map[string]int)
	for callID, name := range callNames {
		if succeeded[callID] {
			index[name]++
		}
	}
	return index
}
