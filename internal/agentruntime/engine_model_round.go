package agentruntime

import (
	"context"
	"errors"
	"strings"
)

const maxBufferedModelStreamEvents = 65_536

// completeModelRound owns the provider streaming boundary. Required-tool
// rounds buffer deltas until the response satisfies the tool-choice contract;
// ordinary rounds publish deltas immediately through the same event path.
func (e Engine) completeModelRound(
	ctx context.Context,
	request ModelRequest,
	bufferUntilToolChoice bool,
) (ModelResponse, []ModelStreamEvent, error) {
	bufferedDeltas := make([]ModelStreamEvent, 0)
	bufferedDeltaBytes := 0
	bufferedPrivateReasoning := false
	bufferedToolBoundary := false
	dispatch := func(event ModelStreamEvent) error {
		normalized, err := normalizeModelStreamEvent(event)
		if err != nil {
			return err
		}
		event = normalized
		explicitPublicProgress := event.Kind == ModelStreamEventPublicProgressDelta ||
			event.Kind == ModelStreamEventPublicProgressBoundary
		if bufferUntilToolChoice && !explicitPublicProgress {
			if event.Kind == ModelStreamEventPrivateReasoning {
				if bufferedPrivateReasoning {
					return nil
				}
				bufferedPrivateReasoning = true
			}
			if event.Kind == ModelStreamEventToolCallBoundary {
				if bufferedToolBoundary {
					return nil
				}
				bufferedToolBoundary = true
			}
			bufferedDeltaBytes += len(event.ContentDelta)
			if bufferedDeltaBytes > maxOpenAIChatResponseBytes {
				return errors.New("agent runtime initial tool-choice response exceeds the bounded buffer")
			}
			if len(bufferedDeltas) >= maxBufferedModelStreamEvents {
				return errors.New("agent runtime initial tool-choice response exceeds the event limit")
			}
			bufferedDeltas = append(bufferedDeltas, event)
			return nil
		}
		if e.OnModelDelta != nil {
			if err := e.OnModelDelta(event); err != nil {
				return err
			}
		}
		if event.ContentDelta != "" {
			if err := e.emit(Event{Type: EventModelDelta, Message: event.ContentDelta}); err != nil {
				return err
			}
		}
		return nil
	}
	normalizeResponse := func(response ModelResponse, decoder *publicProgressEnvelopeDecoder) (ModelResponse, error) {
		if decoder.envelopeCount() == 0 {
			if strings.Contains(response.Message.Content, PublicProgressEnvelopeBegin) ||
				strings.Contains(response.Message.Content, PublicProgressEnvelopeEnd) {
				return ModelResponse{}, errors.New("provider returned unobserved public progress control bytes")
			}
			return response, nil
		}
		if response.Message.Content != decoder.rawContent() {
			return ModelResponse{}, errors.New("provider public progress stream disagrees with its terminal content")
		}
		if len(response.Message.ToolCalls) > 0 {
			response.Message.Content = decoder.visibleContent()
		} else {
			response.Message.Content = decoder.candidateContent()
		}
		return response, nil
	}
	streaming, ok := e.Model.(StreamingModelClient)
	if !ok {
		response, err := e.Model.Complete(ctx, request)
		if err != nil || response.Message.Content == "" {
			return response, bufferedDeltas, err
		}
		decoder := newPublicProgressEnvelopeDecoder(dispatch)
		if err := decoder.write(response.Message.Content); err != nil {
			return ModelResponse{}, bufferedDeltas, err
		}
		if err := decoder.finish(); err != nil {
			return ModelResponse{}, bufferedDeltas, err
		}
		response, err = normalizeResponse(response, decoder)
		return response, bufferedDeltas, err
	}
	decoder := newPublicProgressEnvelopeDecoder(dispatch)
	response, err := streaming.CompleteStream(ctx, request, func(event ModelStreamEvent) error {
		normalized, err := normalizeModelStreamEvent(event)
		if err != nil {
			return err
		}
		event = normalized
		if event.Kind == ModelStreamEventContentDelta {
			return decoder.write(event.ContentDelta)
		}
		if event.Kind == ModelStreamEventToolCallBoundary {
			if err := decoder.boundary(); err != nil {
				return err
			}
		}
		return dispatch(event)
	})
	finishErr := decoder.finish()
	if err != nil {
		if finishErr != nil {
			return response, bufferedDeltas, errors.Join(err, finishErr)
		}
		return response, bufferedDeltas, err
	}
	if finishErr != nil {
		return ModelResponse{}, bufferedDeltas, finishErr
	}
	response, err = normalizeResponse(response, decoder)
	return response, bufferedDeltas, err
}

func normalizeModelStreamEvent(event ModelStreamEvent) (ModelStreamEvent, error) {
	if event.Kind == "" {
		switch {
		case event.ContentDelta != "" && !event.ReasoningActive:
			event.Kind = ModelStreamEventContentDelta
		case event.ContentDelta == "" && event.ReasoningActive:
			event.Kind = ModelStreamEventPrivateReasoning
		default:
			return ModelStreamEvent{}, errors.New("agent runtime model stream event is ambiguous")
		}
	}
	switch event.Kind {
	case ModelStreamEventContentDelta:
		if event.ContentDelta == "" || event.ReasoningActive || event.BlockID != "" {
			return ModelStreamEvent{}, errors.New("agent runtime content delta is invalid")
		}
	case ModelStreamEventPublicProgressDelta:
		if event.ContentDelta == "" || event.ReasoningActive || !validPublicProgressBlockID(event.BlockID) {
			return ModelStreamEvent{}, errors.New("agent runtime public progress delta is invalid")
		}
	case ModelStreamEventPublicProgressBoundary:
		if event.ContentDelta != "" || event.ReasoningActive || !validPublicProgressBlockID(event.BlockID) {
			return ModelStreamEvent{}, errors.New("agent runtime public progress boundary is invalid")
		}
	case ModelStreamEventPrivateReasoning:
		if event.ContentDelta != "" || !event.ReasoningActive || event.BlockID != "" {
			return ModelStreamEvent{}, errors.New("agent runtime private reasoning event is invalid")
		}
	case ModelStreamEventToolCallBoundary:
		if event.ContentDelta != "" || event.ReasoningActive || event.BlockID != "" {
			return ModelStreamEvent{}, errors.New("agent runtime tool-call boundary is invalid")
		}
	default:
		return ModelStreamEvent{}, errors.New("agent runtime model stream event kind is unsupported")
	}
	return event, nil
}
