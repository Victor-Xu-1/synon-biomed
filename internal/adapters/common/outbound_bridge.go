package common

import (
	"context"
	"time"
)

type OutboundBridgeOptions struct {
	Timeout      time.Duration
	MaxAttempts  int
	RetryDelay   time.Duration
	Deduplicate  bool
	MaxDedupKeys int
	OnReport     func(OutboundReport)
	OnResult     func(OutboundDeliveryResult)
	OnError      func(error)
}

type OutboundBridgeHandler func(context.Context, ServerMessage) error

func RegisterOutboundHandler(bridge *WsBridge, chatID string, handler OutboundBridgeHandler, options OutboundBridgeOptions) {
	if bridge == nil {
		return
	}
	if handler == nil {
		bridge.OnServerMessage(chatID, nil)
		return
	}
	delivery := NewOutboundDelivery(handler, OutboundDeliveryOptions{
		Timeout:      options.Timeout,
		MaxAttempts:  options.MaxAttempts,
		RetryDelay:   options.RetryDelay,
		Deduplicate:  options.Deduplicate,
		MaxDedupKeys: options.MaxDedupKeys,
	})
	bridge.OnServerMessage(chatID, func(message ServerMessage) {
		result, err := delivery.Deliver(context.Background(), message)
		if options.OnResult != nil {
			options.OnResult(result)
		}
		if err != nil && options.OnError != nil {
			options.OnError(err)
		}
	})
}
