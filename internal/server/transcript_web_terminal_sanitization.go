package server

import (
	"context"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func (s *Server) publicTranscriptTerminalProjection(
	ctx context.Context,
	stream transcriptstore.Stream,
	projection transcriptstore.TerminalProjection,
) transcriptstore.TerminalProjection {
	if !strings.EqualFold(strings.TrimSpace(projection.TerminalStatus), "failed") {
		return projection
	}
	projection.Detail = publicSessionRunnerFailureMessage(
		projection.Detail,
		s.transcriptWebTaskLanguage(ctx, stream, projection.Detail),
		projection.ReasonCode,
	)
	return projection
}

func (s *Server) sanitizeTranscriptWebTerminalMessages(
	ctx context.Context,
	stream transcriptstore.Stream,
	messages []map[string]any,
) {
	language := ""
	for _, message := range messages {
		if !strings.EqualFold(strings.TrimSpace(webString(message["terminal_status"])), "failed") {
			continue
		}
		content, ok := message["content"].(map[string]any)
		if !ok {
			continue
		}
		if language == "" {
			language = s.transcriptWebTaskLanguage(ctx, stream, webString(content["content"]))
		}
		content["content"] = publicSessionRunnerFailureMessage(webString(content["content"]), language, webString(message["terminal_reason_code"]))
	}
}

// sanitizeTranscriptWebPublicMessages applies the same publication boundary
// to historical assistant text that was persisted before the current runner
// contract. It never rewrites the authoritative transcript; the copy returned
// by the Web read model is filtered so old recovery narration cannot reappear
// after refresh or conversation navigation.
func (s *Server) sanitizeTranscriptWebPublicMessages(messages []map[string]any) {
	sanitizeTranscriptWebPublicMessagesWithCodes(messages, nil)
}

// sanitizeTranscriptWebPublicMessagesForTask may preserve only machine codes
// that the user explicitly requested in the final answer and that are bound to
// the first failed durable receipt in this logical task. All other runtime
// identifiers remain behind the ordinary public-history boundary.
func (s *Server) sanitizeTranscriptWebPublicMessagesForTask(
	ctx context.Context,
	stream transcriptstore.Stream,
	messages []map[string]any,
) {
	sanitizeTranscriptWebPublicMessagesWithCodes(
		messages, s.transcriptWebExplicitFailureCodes(ctx, stream),
	)
}

func sanitizeTranscriptWebPublicMessagesWithCodes(
	messages []map[string]any,
	preservedCodes map[string]struct{},
) {
	for _, message := range messages {
		messageType := strings.ToLower(strings.TrimSpace(webString(message["type"])))
		if messageType != "text" && messageType != "assistant" && messageType != "thinking" {
			continue
		}
		sanitizeTranscriptWebPublicValue(message["content"], preservedCodes)
	}
}

func sanitizeTranscriptWebPublicValue(value any, preservedCodes map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "content", "text", "thinking":
				if text, ok := item.(string); ok {
					typed[key] = sessionRunnerSanitizeHistoricalPublicTextWithCodes(text, preservedCodes)
					continue
				}
			}
			sanitizeTranscriptWebPublicValue(item, preservedCodes)
		}
	case []any:
		for _, item := range typed {
			sanitizeTranscriptWebPublicValue(item, preservedCodes)
		}
	}
}

func sessionRunnerSanitizeHistoricalPublicText(content string) string {
	return sessionRunnerSanitizeHistoricalPublicTextWithCodes(content, nil)
}

func sessionRunnerSanitizeHistoricalPublicTextWithCodes(
	content string,
	preservedCodes map[string]struct{},
) string {
	content = sessionRunnerStripPrivateThinkMarkers(content)
	if !sessionRunnerPublicTextExposesUntrustedInternals(content, preservedCodes) {
		return content
	}
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || sessionRunnerPublicTextExposesUntrustedInternals(line, preservedCodes) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func sessionRunnerPublicTextExposesUntrustedInternals(
	content string,
	preservedCodes map[string]struct{},
) bool {
	if len(preservedCodes) == 0 {
		return sessionRunnerProgressNarrationExposesProductInternals(content)
	}
	masked := sessionRunnerInternalIdentifierPattern.ReplaceAllStringFunc(content, func(identifier string) string {
		if _, allowed := preservedCodes[strings.ToLower(strings.TrimSpace(identifier))]; allowed {
			return "authorized-machine-code"
		}
		return identifier
	})
	return sessionRunnerProgressNarrationExposesProductInternals(masked)
}

func (s *Server) transcriptWebExplicitFailureCodes(
	ctx context.Context,
	stream transcriptstore.Stream,
) map[string]struct{} {
	if s == nil || s.transcriptStore == nil || strings.TrimSpace(stream.UID) == "" ||
		strings.TrimSpace(stream.OwnerID) == "" {
		return nil
	}
	intent, found, err := s.transcriptStore.GetActiveFrameTaskIntent(ctx, stream.UID, stream.OwnerID)
	if err != nil || !found || !buildSessionRunnerExplicitToolContract(intent.Text).ReportFirstFailureCode {
		return nil
	}
	run := &sessionRunnerChatRun{
		TaskIntent: intent.Text, TaskIntentID: intent.ID, TaskIntentRevision: intent.Revision,
		Transcript: &transcriptRunnerAuthority{Stream: stream},
	}
	receipts, err := s.sessionRunnerDurableExplicitToolContractMessages(ctx, run)
	if err != nil {
		return nil
	}
	codes, failed := sessionRunnerFirstFailedToolCodes(receipts)
	if !failed || len(codes) == 0 {
		return nil
	}
	result := make(map[string]struct{}, min(len(codes), 8))
	for _, code := range codes {
		if len(result) >= 8 {
			break
		}
		code = strings.ToLower(strings.TrimSpace(code))
		if sessionRunnerMachineFailureCodePattern.MatchString(code) {
			result[code] = struct{}{}
		}
	}
	return result
}

func (s *Server) transcriptWebTaskLanguage(
	ctx context.Context,
	stream transcriptstore.Stream,
	fallbackText string,
) string {
	if s != nil && s.transcriptStore != nil && strings.TrimSpace(stream.UID) != "" &&
		strings.TrimSpace(stream.OwnerID) != "" {
		intent, found, err := s.transcriptStore.GetActiveFrameTaskIntent(ctx, stream.UID, stream.OwnerID)
		if err == nil && found && strings.TrimSpace(intent.Language) != "" {
			return strings.TrimSpace(intent.Language)
		}
	}
	return sessionRunnerResponseLanguage(fallbackText)
}
