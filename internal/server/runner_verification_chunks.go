package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
)

const (
	// The review contract projects windows at 80,000 bytes. Prefer whole-message
	// boundaries, then fragment a single oversized projected message without
	// dropping content. Keeping the boundary here as a named reviewer contract
	// avoids provider-context truncation silently removing a session span.
	sessionReviewerTranscriptChunkBytes = 80_000
	sessionReviewerToolInputBytes       = 2_000
	sessionReviewerToolResultBytes      = 4_000
)

type sessionReviewerTranscriptWindow struct {
	Start             int
	End               int
	Messages          []agentruntime.Message
	TranscriptExcerpt string
}

func sessionReviewerTranscriptWindows(
	messages []agentruntime.Message,
	limit int,
) []sessionReviewerTranscriptWindow {
	if len(messages) == 0 {
		return []sessionReviewerTranscriptWindow{{Start: 0, End: 0}}
	}
	if limit <= 0 {
		limit = sessionReviewerTranscriptChunkBytes
	}
	result := make([]sessionReviewerTranscriptWindow, 0, 1)
	start, used := 0, 0
	appendWindow := func(end int) {
		if end <= start {
			return
		}
		result = append(result, sessionReviewerTranscriptWindow{
			Start: start, End: end,
			Messages: append([]agentruntime.Message(nil), messages[start:end]...),
		})
		start = end
		used = 0
	}
	for index, message := range messages {
		projectedBytes := len(sessionReviewerProjectedMessage(message, index-start))
		if projectedBytes > limit {
			appendWindow(index)
			for _, fragment := range splitReviewerProjectedMessage(message, limit) {
				result = append(result, sessionReviewerTranscriptWindow{
					Start: index, End: index + 1,
					Messages:          []agentruntime.Message{message},
					TranscriptExcerpt: fragment,
				})
			}
			start = index + 1
			used = 0
			continue
		}
		if index > start {
			projectedBytes += 2
		}
		if used > 0 && used+projectedBytes > limit {
			appendWindow(index)
			projectedBytes = len(sessionReviewerProjectedMessage(message, 0))
		}
		used += projectedBytes
	}
	appendWindow(len(messages))
	if len(result) == 0 {
		return []sessionReviewerTranscriptWindow{{
			Start: 0, End: len(messages), Messages: append([]agentruntime.Message(nil), messages...),
		}}
	}
	return result
}

func sessionReviewerWindowExcerpt(window sessionReviewerTranscriptWindow) string {
	if window.TranscriptExcerpt != "" {
		return window.TranscriptExcerpt
	}
	return sessionReviewerTranscriptExcerpt(window.Messages)
}

func splitReviewerProjectedMessage(message agentruntime.Message, limit int) []string {
	projected := sessionReviewerProjectedMessage(message, 0)
	if limit <= 0 || len(projected) <= limit {
		return []string{projected}
	}
	const reservedPrefixBytes = 128
	payloadLimit := limit - reservedPrefixBytes
	if payloadLimit <= 0 {
		payloadLimit = limit
	}
	parts := splitReviewerUTF8Blocks(projected, payloadLimit)
	fragments := make([]string, 0, len(parts))
	for index, part := range parts {
		prefix := fmt.Sprintf("[source msg[0] fragment %d/%d]\n", index+1, len(parts))
		if len(prefix)+len(part) > limit {
			part = reviewerUTF8Prefix(part, limit-len(prefix))
		}
		fragments = append(fragments, prefix+part)
	}
	return fragments
}

func splitReviewerUTF8Blocks(value string, limit int) []string {
	if value == "" {
		return []string{""}
	}
	if limit <= 0 {
		return []string{value}
	}
	blocks := make([]string, 0, (len(value)+limit-1)/limit)
	for len(value) > limit {
		cut := reviewerUTF8PrefixLength(value, limit)
		if cut <= 0 {
			cut = limit
		}
		blocks = append(blocks, value[:cut])
		value = value[cut:]
	}
	blocks = append(blocks, value)
	return blocks
}

func sessionReviewerTranscriptExcerpt(messages []agentruntime.Message) string {
	parts := make([]string, 0, len(messages))
	for index, message := range messages {
		parts = append(parts, sessionReviewerProjectedMessage(message, index))
	}
	return strings.Join(parts, "\n\n")
}

func sessionReviewerProjectedMessage(message agentruntime.Message, index int) string {
	role := strings.TrimSpace(message.Role)
	if role == "" {
		role = "unknown"
	}
	lines := []string{fmt.Sprintf("--- msg[%d] %s ---", index, role)}
	text := strings.TrimSpace(message.Content)
	if strings.EqualFold(role, "user") && strings.HasPrefix(strings.ToLower(text), "[harness notice]") {
		text = "[synthetic runtime notice omitted from target claims]"
	}
	if text != "" && !strings.EqualFold(role, "tool") {
		lines = append(lines, text)
	}
	for _, part := range message.Parts {
		switch part.Type {
		case agentruntime.ContentPartText:
			if text := strings.TrimSpace(part.Text); text != "" {
				lines = append(lines, text)
			}
		case agentruntime.ContentPartImage, agentruntime.ContentPartDocument, agentruntime.ContentPartAudio:
			if part.Media == nil {
				continue
			}
			lines = append(lines, fmt.Sprintf(
				"[attachment type=%s filename=%s mime_type=%s]",
				part.Type, strings.TrimSpace(part.Media.Filename), strings.TrimSpace(part.Media.MIMEType),
			))
		}
	}
	for _, call := range message.ToolCalls {
		arguments := boundedReviewerTranscriptBlock(
			strings.TrimSpace(string(call.Arguments)), sessionReviewerToolInputBytes,
		)
		lines = append(lines, fmt.Sprintf(
			"[tool_use %s id=%s] %s",
			strings.TrimSpace(call.Name), strings.TrimSpace(call.ID), arguments,
		))
	}
	if strings.EqualFold(role, "tool") {
		lines = append(lines, fmt.Sprintf(
			"[tool_result for=%s] %s",
			strings.TrimSpace(message.ToolCallID),
			boundedReviewerTranscriptBlock(strings.TrimSpace(message.Content), sessionReviewerToolResultBytes),
		))
	}
	return strings.Join(lines, "\n")
}

func boundedReviewerTranscriptBlock(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	marker := "\n...[middle truncated; tail kept]...\n"
	head := (limit - len(marker)) * 2 / 3
	tail := limit - len(marker) - head
	if head <= 0 || tail <= 0 {
		return reviewerUTF8Prefix(value, limit)
	}
	return reviewerUTF8Prefix(value, head) + marker + reviewerUTF8Suffix(value, tail)
}

func reviewerUTF8Prefix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	return value[:reviewerUTF8PrefixLength(value, limit)]
}

func reviewerUTF8PrefixLength(value string, limit int) int {
	if limit >= len(value) {
		return len(value)
	}
	if limit <= 0 {
		return 0
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return limit
}

func reviewerUTF8Suffix(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit >= len(value) {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

func sessionReviewerTargetMessages(
	originalMessages []agentruntime.Message,
	resultMessages []agentruntime.Message,
) []agentruntime.Message {
	if len(resultMessages) >= len(originalMessages) &&
		sessionReviewerMessagesHavePrefix(resultMessages, originalMessages) {
		return append([]agentruntime.Message(nil), resultMessages...)
	}
	merged := make([]agentruntime.Message, 0, len(originalMessages)+len(resultMessages))
	merged = append(merged, originalMessages...)
	merged = append(merged, resultMessages...)
	return merged
}

func sessionReviewerMessagesHavePrefix(messages, prefix []agentruntime.Message) bool {
	if len(prefix) > len(messages) {
		return false
	}
	for index := range prefix {
		left, leftErr := json.Marshal(messages[index])
		right, rightErr := json.Marshal(prefix[index])
		if leftErr != nil || rightErr != nil || string(left) != string(right) {
			return false
		}
	}
	return true
}

func sessionReviewerTargetTranscriptSHA256(messages []agentruntime.Message) (string, error) {
	encoded, err := json.Marshal(messages)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sessionReviewerChunkBinding(
	binding map[string]any,
	window sessionReviewerTranscriptWindow,
	chunkIndex, chunkCount, messageCount int,
	targetTranscriptSHA256 string,
) map[string]any {
	result := copyMapAny(binding)
	messageOffset := int(numberValue(binding["message_start"]))
	totalMessageCount := int(numberValue(binding["message_count"]))
	if totalMessageCount < messageOffset+messageCount {
		totalMessageCount = messageOffset + messageCount
	}
	result["review_chunk_index"] = chunkIndex
	result["review_chunk_count"] = chunkCount
	result["message_start"] = messageOffset + window.Start
	result["message_end"] = messageOffset + window.End
	result["message_count"] = totalMessageCount
	result["target_transcript_sha256"] = targetTranscriptSHA256
	result["review_unit_id"] = fmt.Sprintf("%s:%d/%d", targetTranscriptSHA256, chunkIndex, chunkCount)
	return result
}

func aggregateSessionReviewerReviews(
	reviews []sessionRunnerReview,
	windows []sessionReviewerTranscriptWindow,
) sessionRunnerReview {
	result := sessionRunnerReview{Verdict: "pass"}
	summaries := make([]string, 0, len(reviews))
	feedback := make([]string, 0, len(reviews))
	refs := make([]string, 0)
	for index, review := range reviews {
		if text := strings.TrimSpace(review.Summary); text != "" {
			if len(reviews) > 1 {
				text = fmt.Sprintf("Chunk %d/%d: %s", index+1, len(reviews), text)
			}
			summaries = append(summaries, text)
		}
		if text := strings.TrimSpace(review.Feedback); text != "" {
			feedback = append(feedback, text)
		}
		refs = append(refs, review.EvidenceRefs...)
		for _, issue := range review.Issues {
			if index < len(windows) {
				width := windows[index].End - windows[index].Start
				if width > 0 && issue.MessageIndex >= width {
					issue.MessageIndex = width - 1
				}
				issue.MessageIndex += windows[index].Start
			}
			result.Issues = append(result.Issues, issue)
		}
		if review.Verdict == "revise" {
			result.Verdict = "revise"
		}
	}
	result.Summary = truncateReviewerText(strings.Join(summaries, "\n"), 16<<10)
	result.Feedback = truncateReviewerText(strings.Join(feedback, "\n\n"), 32<<10)
	result.EvidenceRefs = normalizeReviewerEvidenceRefs(refs)
	if result.Summary == "" {
		result.Summary = "Independent transcript review passed"
	}
	return result
}

func aggregateSessionReviewerBindings(
	base map[string]any,
	bindings []map[string]any,
	targetTranscriptSHA256 string,
	messageCount int,
) (map[string]any, error) {
	result := copyMapAny(base)
	result["target_transcript_sha256"] = targetTranscriptSHA256
	result["message_count"] = messageCount
	result["review_chunk_count"] = len(bindings)
	receipts := make([]sessionReviewerEvidenceReceipt, 0)
	chunks := make([]map[string]any, 0, len(bindings))
	models := make([]string, 0, len(bindings))
	for index, binding := range bindings {
		chunk := map[string]any{
			"review_chunk_index":         index,
			"review_chunk_count":         len(bindings),
			"message_start":              binding["message_start"],
			"message_end":                binding["message_end"],
			"review_verdict":             binding["review_verdict"],
			"read_receipts_sha256":       binding["read_receipts_sha256"],
			"reviewer_transcript_sha256": binding["reviewer_transcript_sha256"],
		}
		if model := strings.TrimSpace(stringValue(binding["reviewer_model"])); model != "" {
			chunk["reviewer_model"] = model
			models = append(models, model)
		}
		chunks = append(chunks, chunk)
		switch values := binding["read_receipts"].(type) {
		case []sessionReviewerEvidenceReceipt:
			receipts = append(receipts, values...)
		case []any:
			for _, value := range values {
				if receipt, ok := value.(sessionReviewerEvidenceReceipt); ok {
					receipts = append(receipts, receipt)
				}
			}
		}
	}
	result["review_chunks"] = chunks
	result["read_receipts"] = receipts
	receiptJSON, err := json.Marshal(receipts)
	if err != nil {
		return nil, err
	}
	receiptDigest := sha256.Sum256(receiptJSON)
	result["read_receipts_sha256"] = hex.EncodeToString(receiptDigest[:])
	chunkJSON, err := json.Marshal(chunks)
	if err != nil {
		return nil, err
	}
	chunkDigest := sha256.Sum256(chunkJSON)
	result["reviewer_transcript_sha256"] = hex.EncodeToString(chunkDigest[:])
	if len(models) > 0 {
		result["reviewer_model"] = models[len(models)-1]
		result["reviewer_models"] = models
	}
	return result, nil
}
