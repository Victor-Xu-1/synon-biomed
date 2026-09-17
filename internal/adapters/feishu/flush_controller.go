package feishu

import (
	"sync"
	"time"
)

const (
	CardKitThrottle       = 500 * time.Millisecond
	PatchThrottle         = 1500 * time.Millisecond
	LongGapThreshold      = 2 * time.Second
	BatchAfterGapDuration = 300 * time.Millisecond
)

type FlushController struct {
	mu             sync.Mutex
	doFlush        func() error
	flushInFlight  bool
	needsReflush   bool
	pendingTimer   *time.Timer
	lastUpdateTime time.Time
	completed      bool
	ready          bool
	waiters        []chan struct{}
}

func NewFlushController(doFlush func() error) *FlushController {
	return &FlushController{doFlush: doFlush}
}

func (c *FlushController) Complete() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completed = true
	c.cancelPendingFlushLocked()
}

func (c *FlushController) CancelPendingFlush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelPendingFlushLocked()
}

func (c *FlushController) WaitForFlush() {
	c.mu.Lock()
	if !c.flushInFlight {
		c.mu.Unlock()
		return
	}
	waiter := make(chan struct{})
	c.waiters = append(c.waiters, waiter)
	c.mu.Unlock()
	<-waiter
}

func (c *FlushController) SetCardMessageReady(ready bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ready = ready
	if ready {
		c.lastUpdateTime = time.Now()
	}
}

func (c *FlushController) CardMessageReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *FlushController) Flush() error {
	c.mu.Lock()
	if !c.ready || c.completed {
		c.mu.Unlock()
		return nil
	}
	if c.flushInFlight {
		c.needsReflush = true
		c.mu.Unlock()
		return nil
	}
	c.flushInFlight = true
	c.needsReflush = false
	c.lastUpdateTime = time.Now()
	c.mu.Unlock()

	var err error
	if c.doFlush != nil {
		err = c.doFlush()
	}

	c.mu.Lock()
	c.lastUpdateTime = time.Now()
	c.flushInFlight = false
	waiters := c.waiters
	c.waiters = nil
	for _, waiter := range waiters {
		close(waiter)
	}
	if c.needsReflush && !c.completed && c.pendingTimer == nil {
		c.needsReflush = false
		c.pendingTimer = time.AfterFunc(0, func() {
			c.mu.Lock()
			c.pendingTimer = nil
			c.mu.Unlock()
			_ = c.Flush()
		})
	}
	c.mu.Unlock()
	return err
}

func (c *FlushController) ThrottledUpdate(throttle time.Duration) error {
	if throttle <= 0 {
		throttle = CardKitThrottle
	}
	c.mu.Lock()
	if !c.ready || c.completed {
		c.mu.Unlock()
		return nil
	}
	now := time.Now()
	elapsed := now.Sub(c.lastUpdateTime)
	if elapsed >= throttle {
		c.cancelPendingFlushLocked()
		if elapsed > LongGapThreshold {
			c.lastUpdateTime = now
			c.pendingTimer = time.AfterFunc(BatchAfterGapDuration, func() {
				c.mu.Lock()
				c.pendingTimer = nil
				c.mu.Unlock()
				_ = c.Flush()
			})
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		return c.Flush()
	}
	if c.pendingTimer == nil {
		delay := throttle - elapsed
		c.pendingTimer = time.AfterFunc(delay, func() {
			c.mu.Lock()
			c.pendingTimer = nil
			c.mu.Unlock()
			_ = c.Flush()
		})
	}
	c.mu.Unlock()
	return nil
}

func (c *FlushController) cancelPendingFlushLocked() {
	if c.pendingTimer != nil {
		c.pendingTimer.Stop()
		c.pendingTimer = nil
	}
}
