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

func (blocks *runnerLanguageProgressBlocks) accept(event agentruntime.ModelStreamEvent) (bool, error) {
	switch event.Kind {
	case agentruntime.ModelStreamEventPublicProgressDelta:
		if event.BlockID == "" || len(blocks.closed) >= 64 {
			return true, errors.New("invalid language progress block")
		}
		if blocks.open == nil {
			blocks.open = &runnerLanguageProgressBlock{id: event.BlockID}
		}
		if blocks.open.id != event.BlockID || len(blocks.open.text)+len(event.ContentDelta) > 16*1024 {
			return true, errors.New("language progress block boundary mismatch")
		}
		blocks.open.text += event.ContentDelta
		return true, nil
	case agentruntime.ModelStreamEventPublicProgressBoundary:
		if blocks.open == nil || blocks.open.id != event.BlockID {
			return true, errors.New("language progress block is not open")
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
		return response, false, errors.New("language progress block is incomplete")
	}
	if len(blocks.closed) == 0 {
		return response, false, nil
	}
	var originalText, localizedText strings.Builder
	for _, block := range blocks.closed {
		text := block.text
		originalText.WriteString(text)
		if sessionRunnerClearlyEnglishProgress(text) {
			candidate := agentruntime.ModelResponse{Message: agentruntime.Message{Content: text, ToolCalls: response.Message.ToolCalls}}
			translated, err := client.localizeOrKeepToolCall(ctx, request, candidate)
			if err != nil {
				return response, false, err
			}
			text = translated.Message.Content
			response.Usage = addModelUsage(response.Usage, translated.Usage)
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
