package server

import (
	"fmt"
	"strings"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
)

// One active selection cursor is derived from existing native read receipts.
// Prior windows remain in Transcript, not a second coverage store or an
// unbounded map. Coverage means retained native read receipts, not current
// provider context, comprehension or scientific correctness.
type runnerConditionReadCoverage struct {
	EventID           int64  `json:"-"`
	ContentID         string `json:"content_id"`
	JSONPointer       string `json:"json_pointer"`
	StartLine         int    `json:"start_line"`
	EndLine           int    `json:"end_line"`
	TotalLines        int    `json:"total_lines"`
	NextOffset        int    `json:"next_offset"`
	ContiguousThrough int    `json:"receipt_prefix_through_line"`
}

func (correction *recoveredRunnerCorrection) observeRead(entry eventjournal.Entry) {
	if correction == nil || correction.Condition == nil || entry.SourceEventType != "runner_checkpoint" {
		return
	}
	message := entry.Message
	if message["type"] != "runner_checkpoint" || message["status"] != "completed" || message["toolPhase"] != "completed" {
		return
	}
	input := decodeReadReuseMap(runnerCheckpointExecutedToolInput(message))
	if !runnerCorrectionReadInput(stringValue(message["toolName"]), input) {
		return
	}
	id := correction.Condition.ContentID()
	if stringValue(input["recovery_condition_id"]) != id {
		return
	}
	result := decodeReadReuseMap(message["toolResult"])
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded || result["recovery_condition_id"] != id ||
		result["evidence_kind"] != "runtime_condition" || result["view_format"] != runnerCorrectionReadViewFormat || numberValue(result["truncated_lines"]) > 0 {
		return
	}
	pointer := stringValue(input["json_pointer"])
	if stringValue(result["json_pointer"]) != pointer {
		return
	}
	total, valid := strictPositiveAgentWorkspaceInteger(result["total_lines"])
	if !valid {
		return
	}
	var start, end int
	showing := strings.TrimSpace(stringValue(result["showing_lines"]))
	if _, err := fmt.Sscanf(showing, "%d-%d", &start, &end); err != nil || showing != fmt.Sprintf("%d-%d", start, end) || start <= 0 || end < start || end > total {
		return
	}
	normalized := normalizeAgentWorkspaceReadFileArguments(input)
	offset, validOffset := strictPositiveAgentWorkspaceInteger(normalized["offset"])
	limit, validLimit := strictPositiveAgentWorkspaceInteger(normalized["limit"])
	if !validOffset || !validLimit || start != offset || end-start+1 > limit {
		return
	}
	next := 0
	if end < total {
		next = end + 1
	}
	if numberValue(result["next_offset"]) != int64(next) {
		return
	}
	through := 0
	if previous := correction.ReadCoverage; previous != nil && previous.ContentID == id && previous.JSONPointer == pointer && previous.TotalLines == total {
		through = previous.ContiguousThrough
	}
	if start <= through+1 {
		through = max(through, end)
	}
	correction.ReadCoverage = &runnerConditionReadCoverage{EventID: entry.EventID, ContentID: id, JSONPointer: pointer, StartLine: start, EndLine: end, TotalLines: total, NextOffset: next, ContiguousThrough: through}
}
