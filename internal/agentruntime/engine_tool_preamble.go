package agentruntime

// toolPreamblePublication remembers only publicly emitted prose. A rejected
// tool proposal stays private when its explanation is retained across repair.
type toolPreamblePublication struct {
	content   string
	published bool
}

func (e Engine) publishToolPreamble(
	enforceToolChoice bool,
	message Message,
	bufferedDeltas []ModelStreamEvent,
) (toolPreamblePublication, []ModelStreamEvent, error) {
	publication := toolPreamblePublication{content: message.Content}
	if !enforceToolChoice || len(message.ToolCalls) == 0 ||
		e.AllowToolPreamble == nil || !e.AllowToolPreamble(message.Content) {
		return publication, bufferedDeltas, nil
	}
	// The completed response proves this text precedes tool work, not a final
	// answer. Tool validation remains the caller's responsibility; a rejected
	// proposal must not suppress independently approved public explanation.
	boundary := false
	for _, delta := range bufferedDeltas {
		boundary = boundary || delta.Kind == ModelStreamEventToolCallBoundary
		publication.published = publication.published || delta.ContentDelta != ""
	}
	if !boundary {
		bufferedDeltas = append(bufferedDeltas, ModelStreamEvent{Kind: ModelStreamEventToolCallBoundary})
	}
	if err := e.publishBufferedModelDeltas(bufferedDeltas); err != nil {
		return publication, nil, err
	}
	return publication, nil, nil
}

func (publication toolPreamblePublication) preserveForRepair(messages []Message) []Message {
	if publication.published {
		return append(messages, Message{Role: "assistant", Content: publication.content})
	}
	return messages
}

// Tool-choice feedback belongs only to model context. Retain already-public
// prose separately, without copying the rejected tool proposal into history.
func (publication toolPreamblePublication) toolChoiceRepairMessages(messages []Message, feedback Message) ([]Message, []Message) {
	messages = publication.preserveForRepair(messages)
	modelMessages := append(append([]Message(nil), messages...), feedback)
	return messages, modelMessages
}
