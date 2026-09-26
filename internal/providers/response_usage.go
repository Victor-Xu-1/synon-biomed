package providers

import "synon-go/internal/agentruntime"

// All JSON and streaming transports return the same per-request usage that
// they audit. Consumers must not need to scan cumulative operational audits.
func (c *runtimeModelClient) responseWithUsage(response agentruntime.ModelResponse, record AuditRecord) agentruntime.ModelResponse {
	response.RequestID = record.RequestID
	response.Model = firstNonEmpty(response.Model, c.profile.Model)
	response.Usage = agentruntime.ModelUsage{
		InputTokens: record.PromptTokens, OutputTokens: record.CompletionTokens,
		CacheReadTokens: record.CacheReadTokens, CacheWriteTokens: record.CacheWriteTokens,
		TotalTokens: record.TotalTokens,
	}
	return response
}
