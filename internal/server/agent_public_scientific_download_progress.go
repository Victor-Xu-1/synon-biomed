package server

import (
	"context"
	"io"
	"time"

	"synon-go/internal/toolprogress"
)

const agentPublicScientificProgressInterval = time.Second

type agentPublicScientificProgressReader struct {
	ctx              context.Context
	reader           io.Reader
	startingOffset   int64
	expectedTotal    int64
	transferred      int64
	lastReportedAt   time.Time
	lastReportedSize int64
}

func newAgentPublicScientificProgressReader(
	ctx context.Context,
	reader io.Reader,
	startingOffset, expectedTotal int64,
) *agentPublicScientificProgressReader {
	progress := &agentPublicScientificProgressReader{
		ctx: ctx, reader: reader, startingOffset: startingOffset, expectedTotal: expectedTotal,
		lastReportedAt: time.Now(), lastReportedSize: startingOffset,
	}
	reportAgentPublicScientificDownloadProgress(ctx, "downloading_file", startingOffset, expectedTotal, nil)
	return progress
}

func (r *agentPublicScientificProgressReader) Read(buffer []byte) (int, error) {
	read, err := r.reader.Read(buffer)
	r.transferred += int64(read)
	now := time.Now()
	if now.Sub(r.lastReportedAt) >= agentPublicScientificProgressInterval || err != nil {
		r.report(now)
	}
	return read, err
}

func (r *agentPublicScientificProgressReader) report(now time.Time) {
	if r == nil {
		return
	}
	completed := r.startingOffset + r.transferred
	elapsed := now.Sub(r.lastReportedAt).Seconds()
	var rate *float64
	if elapsed > 0 && completed >= r.lastReportedSize {
		value := float64(completed-r.lastReportedSize) / elapsed
		rate = &value
	}
	reportAgentPublicScientificDownloadProgress(r.ctx, "downloading_file", completed, r.expectedTotal, rate)
	r.lastReportedAt = now
	r.lastReportedSize = completed
}

func (r *agentPublicScientificProgressReader) Complete() {
	if r == nil {
		return
	}
	r.report(time.Now())
}

func reportAgentPublicScientificDownloadProgress(
	ctx context.Context,
	phase string,
	completed, total int64,
	bytesPerSecond *float64,
) {
	completedValue := completed
	update := toolprogress.Update{
		Phase: phase, BytesCompleted: &completedValue, BytesPerSecond: bytesPerSecond,
		Indeterminate: total <= 0,
	}
	if total > 0 {
		totalValue := total
		percent := float64(completed) / float64(total) * 100
		update.BytesTotal = &totalValue
		update.PhasePercent = &percent
	}
	toolprogress.Report(ctx, update)
}
