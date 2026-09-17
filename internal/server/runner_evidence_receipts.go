package server

import (
	"strings"

	"synon-go/internal/agentruntime"
)

type sessionRunnerEvidenceReceipt struct {
	call   agentruntime.ToolCall
	result string
}

// sessionRunnerEvidenceReceipts returns exact verified call/result pairs in
// transcript order. It is shared by record-depth indexing and never selects,
// summarizes or promotes source content into model authority.
func sessionRunnerEvidenceReceipts(messages []agentruntime.Message) []sessionRunnerEvidenceReceipt {
	receipts := make([]sessionRunnerEvidenceReceipt, 0, len(messages)/2)
	for index := 0; index+1 < len(messages); index++ {
		callMessage := messages[index]
		resultMessage := messages[index+1]
		if len(callMessage.ToolCalls) != 1 || !callMessage.ToolCalls[0].VerifiedEvidence ||
			resultMessage.Role != "tool" ||
			strings.TrimSpace(resultMessage.ToolCallID) != strings.TrimSpace(callMessage.ToolCalls[0].ID) {
			continue
		}
		receipts = append(receipts, sessionRunnerEvidenceReceipt{
			call: callMessage.ToolCalls[0], result: strings.TrimSpace(resultMessage.Content),
		})
		index++
	}
	return receipts
}
