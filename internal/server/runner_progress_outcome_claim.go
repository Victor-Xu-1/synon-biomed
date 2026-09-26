package server

import (
	"encoding/json"
	"regexp"
	"strings"

	"synon-go/internal/agentruntime"
)

const runnerProgressCompletedOperations = `downloaded|saved|generated|executed|completed|finished|obtained|retrieved|fetched|written|created|installed|updated|validated|verified`

var (
	runnerProgressOperationCompletion = regexp.MustCompile(`(?i)(?:已(?:经)?(?:成功)?|成功)(?:完成|下载|保存|生成|执行|运行|获取|检索|读取|写入|安装|更新|验证)|(?:^|[。！？；\n])\s*完成[^。！？；\n]{0,100}(?:[，。；]|$)|\b(?:successfully\s+(?:[a-z-]+ed|written|run|built)|(?:has|have)\s+(?:been\s+)?(?:` + runnerProgressCompletedOperations + `)|(?:was|were|is|are)\s+(?:now\s+)?(?:` + runnerProgressCompletedOperations + `))\b`)
	runnerProgressNegatedCompletion   = regexp.MustCompile(`(?i)\b(?:not|never|cannot)(?:\s+(?:have|has|had|been|be|yet|now|already)){0,4}\s*$`)
	runnerProgressIntendedCompletion  = regexp.MustCompile(`(?i)\b(?:will|would|could|should|may|might|can|to)(?:\s+(?:have|be|been|now|then)){0,3}\s*$`)
)

// A pre-tool progress stream has no receipt for the proposed action. Operation
// completion claims belong to the validated final candidate or the host's
// actual tool status, never this early publication channel. This conservative
// language guard is domain-neutral; it does not establish scientific truth.
func runnerProgressClaimsOperationCompletion(content string) bool {
	for _, match := range runnerProgressOperationCompletion.FindAllStringIndex(content, -1) {
		prefix := strings.ToLower(content[:match[0]])
		if strings.HasSuffix(prefix, "未") || strings.HasSuffix(prefix, "没有") ||
			runnerProgressNegatedCompletion.MatchString(prefix) ||
			runnerProgressIntendedCompletion.MatchString(prefix) {
			continue
		}
		return true
	}
	return false
}

// sessionRunnerProgressOutcomeAuthority separates safe narration from claims
// that an operation already completed. Those claims may describe a prior
// successful receipt, but they must never be published solely because the same
// model response proposes a tool that has not executed yet.
type sessionRunnerProgressOutcomeAuthority struct {
	hasSuccessfulOperation bool
}

func newSessionRunnerProgressOutcomeAuthority(messages []agentruntime.Message) *sessionRunnerProgressOutcomeAuthority {
	authority := &sessionRunnerProgressOutcomeAuthority{}
	for _, message := range messages {
		if message.Role == "tool" && sessionRunnerToolReceiptSupportsOperationClaim(message.Content) {
			authority.hasSuccessfulOperation = true
		}
	}
	return authority
}

func (authority *sessionRunnerProgressOutcomeAuthority) observe(event agentruntime.Event) {
	if authority == nil || event.Type != agentruntime.EventToolCompleted || event.RejectedBeforeExecution ||
		!sessionRunnerToolReceiptSupportsOperationClaim(event.Result) {
		return
	}
	authority.hasSuccessfulOperation = true
}

func (authority *sessionRunnerProgressOutcomeAuthority) allows(content string) bool {
	if !runnerProgressClaimsOperationCompletion(content) {
		return true
	}
	return authority != nil && authority.hasSuccessfulOperation
}

func sessionRunnerToolReceiptSupportsOperationClaim(content string) bool {
	var value any
	if json.Unmarshal([]byte(content), &value) != nil || agentruntime.ClassifyToolResult(value) != agentruntime.ToolResultSucceeded {
		return false
	}
	return sessionRunnerToolReceiptExecutedOrReused(value)
}

func sessionRunnerToolReceiptExecutedOrReused(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return true
	}
	if executed, found := object["executed"]; found {
		flag, valid := executed.(bool)
		if !valid {
			return false
		}
		return flag || object["reused"] == true
	}
	if nested, found := object["result"]; found {
		return sessionRunnerToolReceiptExecutedOrReused(nested)
	}
	return true
}
