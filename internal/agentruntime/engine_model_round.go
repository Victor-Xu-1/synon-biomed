package agentruntime

import (
	"context"
	"errors"
	"strings"
)

// completeModelRound owns the provider streaming boundary. Required-tool
// rounds buffer deltas until the response satisfies the tool-choice contract;
// ordinary rounds publish deltas immediately through the same event path.
func (e Engine) completeModelRound(
	ctx context.Context,
	request ModelRequest,
	bufferUntilToolChoice bool,
) (ModelResponse, []ModelStreamEvent, error) {
	buffered := bufferedModelStream{}
	dispatch := func(event ModelStreamEvent) error {
		normalized, err := normalizeModelStreamEvent(event)
		if err != nil {
			return err
		}
		event = normalized
		explicitPublicProgress := event.Kind == ModelStreamEventPublicProgressDelta ||
			event.Kind == ModelStreamEventPublicProgressBoundary
		if bufferUntilToolChoice && !explicitPublicProgress {
			return buffered.append(event)
		}
		if e.OnModelDelta != nil {
			if err := e.OnModelDelta(event); err != nil {
				return modelPresentationPublicationError{cause: err}
			}
		}
		if event.ContentDelta != "" {
			if err := e.emit(Event{Type: EventModelDelta, Message: event.ContentDelta}); err != nil {
				return modelPresentationPublicationError{cause: err}
			}
		}
		return nil
	}
	normalizeResponse := func(response ModelResponse, decoder *publicProgressEnvelopeDecoder) (ModelResponse, error) {
		if decoder.envelopeCount() == 0 {
			if strings.Contains(response.Message.Content, PublicProgressEnvelopeBegin) ||
				strings.Contains(response.Message.Content, PublicProgressEnvelopeEnd) {
				return response, publicProgressPresentationError("provider returned unobserved public progress control bytes")
			}
			return response, nil
		}
		if response.Message.Content != decoder.rawContent() {
			return response, publicProgressPresentationError("provider public progress stream disagrees with its terminal content")
		}
		if len(response.Message.ToolCalls) > 0 {
			response.Message.Content = decoder.visibleContent()
		} else {
			response.Message.Content = decoder.candidateContent()
		}
		return response, nil
	}
	recovery := modelPresentationRecovery{}
	streaming, ok := e.Model.(StreamingModelClient)
	if !ok {
		response, err := e.Model.Complete(ctx, request)
		if err != nil || response.Message.Content == "" {
			return recovery.finish(ctx, e, response, buffered.events(), err)
		}
		decoder := newPublicProgressEnvelopeDecoder(dispatch)
		if err := recovery.retain(decoder.write(response.Message.Content)); err != nil {
			return recovery.finish(ctx, e, response, buffered.events(), err)
		}
		if recovery.failure == nil {
			if err := recovery.retain(decoder.finish()); err != nil {
				return recovery.finish(ctx, e, response, buffered.events(), err)
			}
		}
		if recovery.failure == nil {
			response, err = normalizeResponse(response, decoder)
			err = recovery.retain(err)
		}
		return recovery.finish(ctx, e, response, buffered.events(), err)
	}
	decoder := newPublicProgressEnvelopeDecoder(dispatch)
	response, err := streaming.CompleteStream(ctx, request, func(event ModelStreamEvent) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		recovery.bytes += len(event.ContentDelta)
		if recovery.bytes > maxOpenAIChatResponseBytes {
			return errors.New("agent runtime model response exceeds the byte limit")
		}
		if recovery.failure != nil {
			return nil
		}
		normalized, err := normalizeModelStreamEvent(event)
		if err != nil {
			return recovery.retain(err)
		}
		event = normalized
		if event.Kind == ModelStreamEventContentDelta {
			return recovery.retain(decoder.write(event.ContentDelta))
		}
		if event.Kind == ModelStreamEventToolCallBoundary {
			if err := decoder.boundary(); err != nil {
				return recovery.retain(err)
			}
		}
		return dispatch(event)
	})
	if err != nil {
		return recovery.finish(ctx, e, response, buffered.events(), err)
	}
	if recovery.failure == nil {
		if err := recovery.retain(decoder.finish()); err != nil {
			return recovery.finish(ctx, e, response, buffered.events(), err)
		}
	}
	if recovery.failure == nil {
		response, err = normalizeResponse(response, decoder)
		err = recovery.retain(err)
	}
	return recovery.finish(ctx, e, response, buffered.events(), err)
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
			return ModelStreamEvent{}, publicProgressPresentationError("agent runtime public progress delta is invalid")
		}
	case ModelStreamEventPublicProgressBoundary:
		if event.ContentDelta != "" || event.ReasoningActive || !validPublicProgressBlockID(event.BlockID) {
			return ModelStreamEvent{}, publicProgressPresentationError("agent runtime public progress boundary is invalid")
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
