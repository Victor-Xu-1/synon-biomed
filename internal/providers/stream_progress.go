package providers

import (
	"context"
	"io"
	"time"
)

// Decoder-confirmed progress renews a streaming request. SSE comments and
// repeated transport heartbeats cannot keep a stalled generation alive.
func recordProviderStreamProgress(reader io.Reader) {
	if progress, ok := reader.(interface{ recordProgress() }); ok {
		progress.recordProgress()
	}
}

func (reader *openAIChatStreamIdleReader) recordProgress() {
	reader.reset()
}

// Error bodies and non-SSE fallbacks have no incremental protocol decoder.
// Their bounded byte reader still needs transport-idle cancellation.
func readProviderStreamResponseBody(body io.Reader, limit int64, timeout time.Duration, cancel context.CancelCauseFunc) ([]byte, error) {
	reader := newOpenAIChatStreamIdleReader(body, timeout, cancel)
	reader.semanticProgress = false
	defer reader.Stop()
	return readBoundedProviderBody(reader, limit)
}
