package common

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type OutboundDeliveryOptions struct {
	Timeout      time.Duration
	MaxAttempts  int
	RetryDelay   time.Duration
	Deduplicate  bool
	MaxDedupKeys int
}

type OutboundDeliveryResult struct {
	MessageKey string `json:"messageKey"`
	Attempts   int    `json:"attempts"`
	Delivered  bool   `json:"delivered"`
	Duplicate  bool   `json:"duplicate"`
	Error      string `json:"error,omitempty"`
}

type OutboundDelivery struct {
	handler OutboundBridgeHandler
	options OutboundDeliveryOptions

	mu        sync.Mutex
	delivered map[string]struct{}
	order     []string
}

func NewOutboundDelivery(handler OutboundBridgeHandler, options OutboundDeliveryOptions) *OutboundDelivery {
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = 1
	}
	if options.MaxDedupKeys <= 0 {
		options.MaxDedupKeys = 512
	}
	return &OutboundDelivery{
		handler:   handler,
		options:   options,
		delivered: map[string]struct{}{},
	}
}

func (d *OutboundDelivery) Deliver(ctx context.Context, message ServerMessage) (OutboundDeliveryResult, error) {
	if d == nil {
		return OutboundDeliveryResult{}, errors.New("outbound delivery handler is required")
	}
	return d.deliverWithHandler(ctx, message, d.handler)
}

func (d *OutboundDelivery) deliverWithHandler(ctx context.Context, message ServerMessage, handler OutboundBridgeHandler) (OutboundDeliveryResult, error) {
	if d == nil || handler == nil {
		return OutboundDeliveryResult{}, errors.New("outbound delivery handler is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := MessageDeliveryKey(message)
	result := OutboundDeliveryResult{MessageKey: key}
	if d.options.Deduplicate && d.hasDelivered(key) {
		result.Duplicate = true
		result.Delivered = true
		return result, nil
	}

	var lastErr error
	for attempt := 1; attempt <= d.options.MaxAttempts; attempt++ {
		result.Attempts = attempt
		attemptCtx := ctx
		cancel := func() {}
		if d.options.Timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, d.options.Timeout)
		}
		err := handler(attemptCtx, message)
		cancel()
		if err == nil {
			if d.options.Deduplicate {
				d.markDelivered(key)
			}
			result.Delivered = true
			return result, nil
		}
		lastErr = err
		if attempt < d.options.MaxAttempts && d.options.RetryDelay > 0 {
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
				result.Error = RedactOutboundText(lastErr.Error())
				return result, lastErr
			case <-time.After(d.options.RetryDelay):
			}
		}
	}
	if lastErr == nil {
		lastErr = errors.New("outbound delivery failed")
	}
	result.Error = RedactOutboundText(lastErr.Error())
	return result, lastErr
}

func MessageDeliveryKey(message ServerMessage) string {
	for _, key := range []string{"clientMessageId", "clientMessageID", "eventId", "eventID", "messageId", "messageID", "id"} {
		if value := strings.TrimSpace(stringValue(message[key])); value != "" {
			return key + ":" + value
		}
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return fmt.Sprintf("hash:%p", message)
	}
	sum := sha256.Sum256(raw)
	return "hash:" + hex.EncodeToString(sum[:12])
}

func (d *OutboundDelivery) hasDelivered(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.delivered[key]
	return ok
}

func (d *OutboundDelivery) markDelivered(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.delivered[key]; ok {
		return
	}
	d.delivered[key] = struct{}{}
	d.order = append(d.order, key)
	for len(d.order) > d.options.MaxDedupKeys {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.delivered, oldest)
	}
}
