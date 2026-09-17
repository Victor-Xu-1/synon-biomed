package common

import (
	"sync"
	"time"
)

type FlushCallback func(text string, isComplete bool) error

type MessageBuffer struct {
	mu              sync.Mutex
	buffer          string
	timer           *time.Timer
	flushing        bool
	pendingComplete bool
	onFlush         FlushCallback
	interval        time.Duration
	charThreshold   int
}

func NewMessageBuffer(onFlush FlushCallback, interval time.Duration, charThreshold int) *MessageBuffer {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if charThreshold <= 0 {
		charThreshold = 200
	}
	return &MessageBuffer{
		onFlush:       onFlush,
		interval:      interval,
		charThreshold: charThreshold,
	}
}

func (b *MessageBuffer) Append(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buffer += text
	if len(b.buffer) >= b.charThreshold {
		b.stopTimerLocked()
		go b.flush(false)
		return
	}
	if b.timer == nil {
		b.timer = time.AfterFunc(b.interval, func() {
			b.flush(false)
		})
	}
}

func (b *MessageBuffer) Complete() {
	b.mu.Lock()
	b.stopTimerLocked()
	if b.flushing {
		b.pendingComplete = true
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.flush(true)
}

func (b *MessageBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buffer = ""
	b.pendingComplete = false
	b.stopTimerLocked()
}

func (b *MessageBuffer) flush(isComplete bool) {
	b.mu.Lock()
	b.stopTimerLocked()
	if b.flushing || b.buffer == "" {
		b.mu.Unlock()
		return
	}
	b.flushing = true
	text := b.buffer
	b.buffer = ""
	b.mu.Unlock()

	if b.onFlush != nil {
		_ = b.onFlush(text, isComplete)
	}

	b.mu.Lock()
	b.flushing = false
	pendingComplete := b.pendingComplete
	b.pendingComplete = false
	b.mu.Unlock()

	if pendingComplete {
		b.flush(true)
	}
}

func (b *MessageBuffer) stopTimerLocked() {
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}
