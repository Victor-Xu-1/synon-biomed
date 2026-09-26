package agentruntime

import (
	"errors"
	"strings"
)

const bufferedModelStreamChunkBytes = 64 << 10

// Provider transport fragmentation is not a response-size contract. Retain
// complete text in coalesced chunks under the existing total byte budget,
// preserving typed marker order without allocating one entry per token.
type bufferedModelStream struct {
	items            []ModelStreamEvent
	content          strings.Builder
	bytes            int
	privateReasoning bool
	toolBoundary     bool
}

func (buffer *bufferedModelStream) append(event ModelStreamEvent) error {
	switch event.Kind {
	case ModelStreamEventPrivateReasoning:
		if buffer.privateReasoning {
			return nil
		}
		buffer.privateReasoning = true
	case ModelStreamEventToolCallBoundary:
		if buffer.toolBoundary {
			return nil
		}
		buffer.toolBoundary = true
	}
	if len(event.ContentDelta) > maxOpenAIChatResponseBytes-buffer.bytes {
		return errors.New("agent runtime initial tool-choice response exceeds the bounded buffer")
	}
	buffer.bytes += len(event.ContentDelta)
	if event.Kind != ModelStreamEventContentDelta {
		buffer.flush()
		buffer.items = append(buffer.items, event)
		return nil
	}
	if buffer.content.Len()+len(event.ContentDelta) > bufferedModelStreamChunkBytes {
		buffer.flush()
	}
	if len(event.ContentDelta) >= bufferedModelStreamChunkBytes {
		// A single already-bounded event needs no copy or byte slicing, which
		// also preserves UTF-8 when a character crosses the chunk target.
		buffer.items = append(buffer.items, event)
		return nil
	}
	buffer.content.WriteString(event.ContentDelta)
	return nil
}

func (buffer *bufferedModelStream) flush() {
	if buffer.content.Len() == 0 {
		return
	}
	buffer.items = append(buffer.items, ModelStreamEvent{Kind: ModelStreamEventContentDelta, ContentDelta: buffer.content.String()})
	buffer.content.Reset()
}

func (buffer *bufferedModelStream) events() []ModelStreamEvent {
	buffer.flush()
	return buffer.items
}
