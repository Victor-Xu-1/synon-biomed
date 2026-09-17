package server

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"synon-go/internal/toolprogress"
)

func TestAgentPublicScientificProgressReaderReportsBytesPercentAndRate(t *testing.T) {
	var updates []toolprogress.Update
	ctx := toolprogress.WithReporter(context.Background(), func(update toolprogress.Update) {
		updates = append(updates, update)
	})
	reader := newAgentPublicScientificProgressReader(ctx, bytes.NewReader(make([]byte, 500)), 100, 1100)
	reader.lastReportedAt = time.Now().Add(-2 * time.Second)
	reader.lastReportedSize = 100
	if copied, err := io.Copy(io.Discard, reader); err != nil || copied != 500 {
		t.Fatalf("copied=%d err=%v", copied, err)
	}
	reader.Complete()

	found := false
	for _, update := range updates {
		if update.Phase != "downloading_file" || update.BytesCompleted == nil || *update.BytesCompleted != 600 ||
			update.BytesTotal == nil || *update.BytesTotal != 1100 || update.PhasePercent == nil ||
			*update.PhasePercent < 54 || *update.PhasePercent > 55 ||
			update.BytesPerSecond == nil || *update.BytesPerSecond <= 0 {
			continue
		}
		found = true
	}
	if !found {
		t.Fatalf("updates=%#v", updates)
	}
}
