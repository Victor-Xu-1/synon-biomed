package agentruntime

import (
	"context"
	"errors"
)

// Only optional narration can be dropped, and only after a complete native
// response proves it contains independently validated action proposals.
type modelPresentationRecovery struct {
	failure error
	bytes   int
}

type modelPresentationPublicationError struct{ cause error }

func (err modelPresentationPublicationError) Error() string { return err.cause.Error() }
func (err modelPresentationPublicationError) Unwrap() error { return err.cause }

func (recovery *modelPresentationRecovery) retain(err error) error {
	var publication modelPresentationPublicationError
	if errors.As(err, &publication) {
		return err
	}
	var presentation *PublicProgressPresentationError
	if errors.As(err, &presentation) {
		if recovery.failure == nil {
			recovery.failure = err
		}
		return nil
	}
	return err
}

func (recovery *modelPresentationRecovery) finish(ctx context.Context, engine Engine, response ModelResponse, buffered []ModelStreamEvent, err error) (ModelResponse, []ModelStreamEvent, error) {
	if ctx.Err() != nil {
		return ModelResponse{}, nil, ctx.Err()
	}
	if err != nil {
		return ModelResponse{}, nil, err
	}
	if recovery.failure == nil {
		return response, buffered, nil
	}
	if len(response.Message.ToolCalls) == 0 {
		// An invalid final candidate is not successful task completion.
		return ModelResponse{}, nil, recovery.failure
	}
	if err := engine.emit(Event{Type: EventPresentationDiagnostic, Message: "invalid_public_progress"}); err != nil {
		return ModelResponse{}, nil, err
	}
	response.Message.Content = ""
	kept := buffered[:0]
	for _, event := range buffered {
		if event.Kind != ModelStreamEventContentDelta && event.Kind != ModelStreamEventPublicProgressDelta && event.Kind != ModelStreamEventPublicProgressBoundary {
			kept = append(kept, event)
		}
	}
	return response, kept, nil
}
