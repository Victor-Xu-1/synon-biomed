package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type sessionRunnerContentDeltaBatcher struct {
	mu       sync.Mutex
	flushMu  sync.Mutex
	pending  strings.Builder
	index    int
	last     time.Time
	first    bool
	sticky   error
	now      func() time.Time
	interval time.Duration
	maxBytes int
	persist  func(context.Context, string, int) error
}

func newSessionRunnerContentDeltaBatcher(
	interval time.Duration,
	maxBytes int,
	persist func(context.Context, string, int) error,
) *sessionRunnerContentDeltaBatcher {
	return &sessionRunnerContentDeltaBatcher{
		first: true, now: time.Now, interval: interval, maxBytes: maxBytes, persist: persist,
	}
}

func (b *sessionRunnerContentDeltaBatcher) append(ctx context.Context, delta string) error {
	if b == nil || delta == "" {
		return nil
	}
	b.mu.Lock()
	if b.sticky != nil {
		err := b.sticky
		b.mu.Unlock()
		return err
	}
	force := b.first
	b.first = false
	b.pending.WriteString(delta)
	b.mu.Unlock()
	return b.flush(ctx, force)
}

func (b *sessionRunnerContentDeltaBatcher) boundary(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if err := b.flush(ctx, true); err != nil {
		return err
	}
	b.mu.Lock()
	b.first = true
	b.mu.Unlock()
	return nil
}

func (b *sessionRunnerContentDeltaBatcher) flush(ctx context.Context, force bool) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("content delta flush context is required")
	}
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.mu.Lock()
	if b.sticky != nil {
		err := b.sticky
		b.mu.Unlock()
		return err
	}
	if b.pending.Len() == 0 {
		b.mu.Unlock()
		return nil
	}
	now := b.now()
	if !force && !b.last.IsZero() && b.pending.Len() < b.maxBytes && now.Sub(b.last) < b.interval {
		b.mu.Unlock()
		return nil
	}
	batch := b.pending.String()
	batchIndex := b.index + 1
	b.mu.Unlock()

	if b.persist == nil {
		return errors.New("content delta persistence is not configured")
	}
	if err := b.persist(ctx, batch, batchIndex); err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.pending.String()
	if !strings.HasPrefix(current, batch) {
		return errors.New("content delta buffer lost its persisted prefix")
	}
	b.pending.Reset()
	b.pending.WriteString(current[len(batch):])
	b.index = batchIndex
	b.last = now
	return nil
}

func (b *sessionRunnerContentDeltaBatcher) startTimer(
	parent context.Context,
	operationTimeout time.Duration,
	onError func(error),
) func() error {
	if b == nil {
		return func() error { return nil }
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	tick := b.interval / 4
	if tick <= 0 || tick > 50*time.Millisecond {
		tick = 50 * time.Millisecond
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				operationCtx, stop := context.WithTimeout(ctx, operationTimeout)
				err := b.flush(operationCtx, false)
				stop()
				if err == nil {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				b.mu.Lock()
				if b.sticky == nil {
					b.sticky = err
				}
				b.mu.Unlock()
				if onError != nil {
					onError(err)
				}
				return
			}
		}
	}()
	var once sync.Once
	return func() error {
		once.Do(cancel)
		<-done
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.sticky
	}
}
