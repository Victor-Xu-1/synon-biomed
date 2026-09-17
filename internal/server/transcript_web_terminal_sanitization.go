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
		content["content"] = publicSessionRunnerFailureMessage(webString(content["content"]), language)
	}
}

// sanitizeTranscriptWebPublicMessages applies the same publication boundary
// to historical assistant text that was persisted before the current runner
// contract. It never rewrites the authoritative transcript; the copy returned
// by the Web read model is filtered so old recovery narration cannot reappear
// after refresh or conversation navigation.
func (s *Server) sanitizeTranscriptWebPublicMessages(messages []map[string]any) {
	for _, message := range messages {
		messageType := strings.ToLower(strings.TrimSpace(webString(message["type"])))
		if messageType != "text" && messageType != "assistant" && messageType != "thinking" {
			continue
		}
		sanitizeTranscriptWebPublicValue(message["content"])
	}
}

func sanitizeTranscriptWebPublicValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "content", "text", "thinking":
				if text, ok := item.(string); ok {
					typed[key] = sessionRunnerSanitizeHistoricalPublicText(text)
					continue
				}
			}
			sanitizeTranscriptWebPublicValue(item)
		}
	case []any:
		for _, item := range typed {
			sanitizeTranscriptWebPublicValue(item)
		}
	}
}

func sessionRunnerSanitizeHistoricalPublicText(content string) string {
	content = sessionRunnerStripPrivateThinkMarkers(content)
	if !sessionRunnerProgressNarrationExposesProductInternals(content) {
		return content
	}
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || sessionRunnerProgressNarrationExposesProductInternals(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
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
