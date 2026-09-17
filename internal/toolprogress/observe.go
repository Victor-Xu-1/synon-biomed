package toolprogress

import (
	"context"
	"time"
)

// Observe coalesces real progress without backpressuring execution. The caller
// owns one sink. Completion flushes the latest fact before returning.
func Observe[T any](ctx context.Context, interval time.Duration, execute func(context.Context) (T, error), publish func(*Update, time.Duration, int)) (T, error) {
	type outcome struct {
		result T
		err    error
	}
	heartbeatInterval := interval
	if interval <= 0 {
		interval = time.Second
		heartbeatInterval = 30 * time.Second
	}
	done := make(chan outcome, 1)
	updates := make(chan Update, 1)
	started := time.Now()
	lastPublished := started
	executionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	executionCtx = WithReporter(executionCtx, func(update Update) {
		update = Normalize(update)
		select {
		case <-executionCtx.Done():
			return
		default:
		}
		// Keep only the newest observation. Installer output can be very chatty;
		// progress must never backpressure or stall the real tool.
		select {
		case updates <- update:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- update:
			default:
			}
		}
	})
	go func() {
		result, err := execute(executionCtx)
		done <- outcome{result: result, err: err}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	progressOrdinal := 0
	structuredPublished := false
	dirty := false
	var current *Update
	emit := func(progress *Update) {
		lastPublished = time.Now()
		dirty = false
		progressOrdinal++
		var snapshot *Update
		if progress != nil {
			value := Clone(*progress)
			snapshot = &value
		}
		publish(snapshot, time.Since(started), progressOrdinal)
	}
	applyUpdate := func(update Update) {
		dirty = true
		if current != nil {
			merged := Merge(*current, update)
			current = &merged
		} else {
			value := Clone(update)
			current = &value
		}
		// Publish the first observed state immediately. Every later installer
		// line, including rapid download/extract phase alternation, is latest-
		// wins and reaches the durable timeline only on the periodic checkpoint.
		if !structuredPublished {
			emit(current)
			structuredPublished = true
		}
	}
	for {
		select {
		case outcome := <-done:
			// A very short operation can publish its first observed phase and
			// finish before the scheduler receives the update. Drain the newest
			// nonblocking observation so the event lifecycle remains ordered.
			select {
			case update := <-updates:
				applyUpdate(update)
			default:
			}
			if dirty {
				emit(current)
			}
			return outcome.result, outcome.err
		case update := <-updates:
			applyUpdate(update)
		case <-ticker.C:
			if dirty || time.Since(lastPublished) >= heartbeatInterval {
				emit(current)
			}
		}
	}
}
