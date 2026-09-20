package server

import (
	"context"
	"errors"
	"strings"

	"synon-go/internal/agentruntime"
)

type runnerLanguageProgressBlock struct{ id, text string }
type runnerLanguageProgressBlocks struct {
	open   *runnerLanguageProgressBlock
	closed []runnerLanguageProgressBlock
}

func (client *sessionRunnerResponseLanguageModelClient) discardProgressPresentation(err error) {
	code := "invalid_progress"
	if failure, ok := err.(sessionRunnerPresentationViolation); ok {
		code = failure.Code
	}
	if client.audit != nil {
		client.audit(map[string]any{"decision": "progress_presentation_discarded", "failure_code": code})
	}
}

func (blocks *runnerLanguageProgressBlocks) accept(event agentruntime.ModelStreamEvent) (bool, error) {
	switch event.Kind {
	case agentruntime.ModelStreamEventPublicProgressDelta:
		if err := agentruntime.ValidateModelStreamEvent(event); err != nil {
			return true, sessionRunnerPresentationViolation{Code: "invalid_progress_event"}
		}
		if event.BlockID == "" {
			return true, sessionRunnerPresentationViolation{Code: "missing_block_identity"}
		}
		if len(blocks.closed) >= 64 {
			return true, sessionRunnerPresentationViolation{Code: "too_many_blocks"}
		}
		if blocks.open == nil {
			blocks.open = &runnerLanguageProgressBlock{id: event.BlockID}
		}
		if blocks.open.id != event.BlockID {
			return true, sessionRunnerPresentationViolation{Code: "block_identity_changed"}
		}
		if len(blocks.open.text)+len(event.ContentDelta) > 16*1024 {
			return true, sessionRunnerPresentationViolation{Code: "block_too_large"}
		}
		blocks.open.text += event.ContentDelta
		return true, nil
	case agentruntime.ModelStreamEventPublicProgressBoundary:
		if err := agentruntime.ValidateModelStreamEvent(event); err != nil {
			return true, sessionRunnerPresentationViolation{Code: "invalid_progress_event"}
		}
		if blocks.open == nil || blocks.open.id != event.BlockID {
			return true, sessionRunnerPresentationViolation{Code: "unexpected_boundary"}
		}
		blocks.closed = append(blocks.closed, *blocks.open)
		blocks.open = nil
		return true, nil
	default:
		return false, nil
	}
}

func (client *sessionRunnerResponseLanguageModelClient) localizeProgressBlocks(ctx context.Context, request agentruntime.ModelRequest, response agentruntime.ModelResponse, blocks runnerLanguageProgressBlocks, emit func(agentruntime.ModelStreamEvent) error) (agentruntime.ModelResponse, bool, error) {
	if blocks.open != nil {
		client.discardProgressPresentation(sessionRunnerPresentationViolation{Code: "incomplete_block"})
		return response, false, nil
	}
	if len(blocks.closed) == 0 {
		return response, false, nil
	}
	var originalText, localizedText strings.Builder
	for _, block := range blocks.closed {
		text := block.text
		originalText.WriteString(text)
		if sessionRunnerRequiresChinese(client.language) && sessionRunnerClearlyEnglishProgress(text) {
			candidate := agentruntime.ModelResponse{Message: agentruntime.Message{Content: text, ToolCalls: response.Message.ToolCalls}}
			translated, err := client.localizeOrKeepToolCall(ctx, request, candidate)
			if err != nil {
				if ctx.Err() != nil {
					return response, false, ctx.Err()
				}
				if errors.Is(err, context.Canceled) {
					return response, false, err
				}
				// A separate valid final answer or original action must not be
				// rejected just because optional progress conversion was unavailable.
				client.discardProgressPresentation(sessionRunnerPresentationViolation{Code: "conversion_unavailable"})
				text = ""
			} else {
				text = translated.Message.Content
				response.Usage = addModelUsage(response.Usage, translated.Usage)
			}
		}
		localizedText.WriteString(text)
		if emit != nil && text != "" {
			if err := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventPublicProgressDelta, BlockID: block.id, ContentDelta: text}); err != nil {
				return response, false, err
			}
			if err := emit(agentruntime.ModelStreamEvent{Kind: agentruntime.ModelStreamEventPublicProgressBoundary, BlockID: block.id}); err != nil {
				return response, false, err
			}
		}
	}
	// The response-contract adapter selects this progress as canonical tool-round
	// content. Any discarded native report draft must not be translated/published
	// again, nor should the canonical event reintroduce the original English.
	if len(response.Message.ToolCalls) > 0 && response.Message.Content == originalText.String() {
		response.Message.Content = localizedText.String()
		return response, true, nil
	}
	return response, false, nil
}
