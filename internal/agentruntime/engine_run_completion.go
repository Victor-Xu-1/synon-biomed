package agentruntime

import (
	"errors"
	"strings"
)

func (e Engine) publishBufferedModelDeltas(events []ModelStreamEvent) error {
	for _, event := range events {
		normalized, err := normalizeModelStreamEvent(event)
		if err != nil {
			return err
		}
		event = normalized
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
	}
	return nil
}

func (e Engine) finishRun(messages []Message, message Message) (RunResult, error) {
	if strings.TrimSpace(message.Content) == "" {
		return RunResult{}, errors.New("agent runtime response message is empty")
	}
	messages = append(messages, message)
	if err := e.emit(Event{Type: EventFinal, Message: message.Content}); err != nil {
		return RunResult{Messages: messages}, err
	}
	return RunResult{FinalMessage: message, Messages: messages}, nil
}
