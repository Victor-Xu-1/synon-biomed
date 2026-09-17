package common

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionOutboundDispatcherCoalescesDeltasAndAvoidsFinalDuplicate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan ServerMessage, 8)
	dispatcher, err := NewSessionOutboundDispatcher(ctx, SessionOutboundDispatcherOptions{
		FlushInterval: 10 * time.Millisecond,
		Handlers: map[string]SessionOutboundHandler{
			"wechat": func(_ context.Context, _ IMSessionBinding, message ServerMessage) (OutboundReport, error) {
				received <- cloneServerMessage(message)
				return OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	defer dispatcher.Close()
	if err := dispatcher.Bind(IMSessionBinding{SessionID: "im:wechat:42", Platform: "wechat", ChatID: "42", TargetID: "42", OwnerUserID: "local"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	events := []SessionOutboundEvent{
		{SessionID: "im:wechat:42", EventID: 1, Message: ServerMessage{"type": "runner_checkpoint", "text": "runner chat started"}},
		{SessionID: "im:wechat:42", EventID: 2, Message: ServerMessage{"type": "content_delta", "text": "hello"}},
		{SessionID: "im:wechat:42", EventID: 3, Message: ServerMessage{"type": "content_delta", "text": " world"}},
		{SessionID: "im:wechat:42", EventID: 4, Message: ServerMessage{"type": "message", "role": "assistant", "text": "hello world"}},
		{SessionID: "im:wechat:42", EventID: 5, Message: ServerMessage{"type": "runner_finished", "status": "completed"}},
	}
	for _, event := range events {
		if !dispatcher.Enqueue(event) {
			t.Fatalf("enqueue event %d", event.EventID)
		}
	}

	messages := receiveOutboundMessages(t, received, 3)
	if messages[0]["type"] != "content_start" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1]["type"] != "content_delta" || messages[1]["text"] != "hello world" || messages[1]["eventId"] != "4" {
		t.Fatalf("coalesced message = %#v", messages[1])
	}
	if messages[2]["type"] != "message_complete" || messages[2]["eventId"] != "5" {
		t.Fatalf("completion message = %#v", messages[2])
	}
	select {
	case extra := <-received:
		t.Fatalf("unexpected duplicate final message: %#v", extra)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestSessionOutboundDispatcherAcknowledgesExactFinalWithoutDuplicateSend(t *testing.T) {
	received := make(chan ServerMessage, 2)
	results := make(chan SessionOutboundResult, 2)
	dispatcher, err := NewSessionOutboundDispatcher(context.Background(), SessionOutboundDispatcherOptions{
		FlushInterval: time.Millisecond,
		Handlers: map[string]SessionOutboundHandler{
			"wechat": func(_ context.Context, _ IMSessionBinding, message ServerMessage) (OutboundReport, error) {
				received <- cloneServerMessage(message)
				return OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result SessionOutboundResult) { results <- result },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	binding := IMSessionBinding{SessionID: "im:wechat:receipt", Platform: "wechat", ChatID: "receipt", OwnerUserID: "local"}
	if err := dispatcher.Bind(binding); err != nil {
		t.Fatal(err)
	}
	if !dispatcher.Enqueue(SessionOutboundEvent{SessionID: binding.SessionID, EventID: 1, Message: ServerMessage{"type": "content_delta", "text": "complete"}}) {
		t.Fatal("enqueue delta")
	}
	first := receiveOutboundResult(t, results)
	if !first.Delivery.Delivered || first.EventID != 1 {
		t.Fatalf("delta result=%#v", first)
	}
	if !dispatcher.Enqueue(SessionOutboundEvent{SessionID: binding.SessionID, EventID: 2, ReceiptToken: "receipt-2", Message: ServerMessage{"type": "message", "role": "assistant", "text": "complete"}}) {
		t.Fatal("enqueue final")
	}
	second := receiveOutboundResult(t, results)
	if !second.Delivery.Delivered || second.EventID != 2 || second.ReceiptToken != "receipt-2" || second.Delivery.Attempts != 0 {
		t.Fatalf("no-op final result=%#v", second)
	}
	if message := <-received; message["text"] != "complete" {
		t.Fatalf("delivered message=%#v", message)
	}
	select {
	case duplicate := <-received:
		t.Fatalf("duplicate final send=%#v", duplicate)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSessionOutboundDispatcherAcknowledgesInvisiblePlanningCheckpoint(t *testing.T) {
	received := make(chan ServerMessage, 1)
	results := make(chan SessionOutboundResult, 1)
	dispatcher, err := NewSessionOutboundDispatcher(context.Background(), SessionOutboundDispatcherOptions{
		Handlers: map[string]SessionOutboundHandler{
			"wechat": func(_ context.Context, _ IMSessionBinding, message ServerMessage) (OutboundReport, error) {
				received <- cloneServerMessage(message)
				return OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result SessionOutboundResult) { results <- result },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	binding := IMSessionBinding{SessionID: "im:wechat:planning", Platform: "wechat", ChatID: "planning", OwnerUserID: "local"}
	if err := dispatcher.Bind(binding); err != nil {
		t.Fatal(err)
	}
	if !dispatcher.Enqueue(SessionOutboundEvent{
		SessionID: binding.SessionID, EventID: 7, ReceiptToken: "checkpoint-receipt",
		Message: ServerMessage{"type": "runner_checkpoint", "phase": "planning", "text": "preparing task plan"},
	}) {
		t.Fatal("enqueue planning checkpoint")
	}
	result := receiveOutboundResult(t, results)
	if !result.Delivery.Delivered || result.EventID != 7 || result.ReceiptToken != "checkpoint-receipt" || result.Delivery.Attempts != 0 {
		t.Fatalf("planning no-op receipt=%#v", result)
	}
	select {
	case message := <-received:
		t.Fatalf("invisible planning checkpoint was sent externally: %#v", message)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSessionOutboundDispatcherSendsCorrectedFinalAfterStreamedDelta(t *testing.T) {
	received := make(chan ServerMessage, 2)
	results := make(chan SessionOutboundResult, 2)
	dispatcher, err := NewSessionOutboundDispatcher(context.Background(), SessionOutboundDispatcherOptions{
		FlushInterval: time.Millisecond,
		Handlers: map[string]SessionOutboundHandler{
			"wechat": func(_ context.Context, _ IMSessionBinding, message ServerMessage) (OutboundReport, error) {
				received <- cloneServerMessage(message)
				return OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result SessionOutboundResult) { results <- result },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	binding := IMSessionBinding{SessionID: "im:wechat:corrected", Platform: "wechat", ChatID: "corrected", OwnerUserID: "local"}
	if err := dispatcher.Bind(binding); err != nil {
		t.Fatal(err)
	}
	if !dispatcher.Enqueue(SessionOutboundEvent{SessionID: binding.SessionID, EventID: 1, Message: ServerMessage{"type": "content_delta", "text": "partial"}}) {
		t.Fatal("enqueue delta")
	}
	if result := receiveOutboundResult(t, results); !result.Delivery.Delivered || result.EventID != 1 {
		t.Fatalf("delta result=%#v", result)
	}
	if !dispatcher.Enqueue(SessionOutboundEvent{SessionID: binding.SessionID, EventID: 2, ReceiptToken: "receipt-2", Message: ServerMessage{"type": "message", "role": "assistant", "text": "corrected final"}}) {
		t.Fatal("enqueue corrected final")
	}
	if result := receiveOutboundResult(t, results); !result.Delivery.Delivered || result.EventID != 2 || result.ReceiptToken != "receipt-2" || result.Delivery.Attempts != 1 {
		t.Fatalf("corrected final result=%#v", result)
	}
	if first, second := <-received, <-received; first["text"] != "partial" || second["text"] != "corrected final" {
		t.Fatalf("delivered messages=%#v %#v", first, second)
	}
}

func TestSessionOutboundDispatcherSettlesEveryCoalescedDeltaReceipt(t *testing.T) {
	received := make(chan ServerMessage, 2)
	results := make(chan SessionOutboundResult, 2)
	dispatcher, err := NewSessionOutboundDispatcher(context.Background(), SessionOutboundDispatcherOptions{
		FlushInterval: 50 * time.Millisecond,
		Handlers: map[string]SessionOutboundHandler{
			"wechat": func(_ context.Context, _ IMSessionBinding, message ServerMessage) (OutboundReport, error) {
				received <- cloneServerMessage(message)
				return OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result SessionOutboundResult) { results <- result },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer dispatcher.Close()
	binding := IMSessionBinding{SessionID: "im:wechat:coalesced", Platform: "wechat", ChatID: "coalesced", OwnerUserID: "local"}
	if err := dispatcher.Bind(binding); err != nil {
		t.Fatal(err)
	}
	for _, event := range []SessionOutboundEvent{
		{SessionID: binding.SessionID, EventID: 1, ReceiptToken: "receipt-1", Message: ServerMessage{"type": "content_delta", "text": "a"}},
		{SessionID: binding.SessionID, EventID: 2, ReceiptToken: "receipt-2", Message: ServerMessage{"type": "content_delta", "text": "b"}},
	} {
		if !dispatcher.Enqueue(event) {
			t.Fatalf("enqueue event %d", event.EventID)
		}
	}
	first, second := receiveOutboundResult(t, results), receiveOutboundResult(t, results)
	if first.EventID != 1 || first.ReceiptToken != "receipt-1" || !first.Delivery.Delivered ||
		second.EventID != 2 || second.ReceiptToken != "receipt-2" || !second.Delivery.Delivered {
		t.Fatalf("coalesced receipts=%#v %#v", first, second)
	}
	if message := <-received; message["text"] != "ab" || message["eventId"] != "2" {
		t.Fatalf("coalesced delivery=%#v", message)
	}
	select {
	case extra := <-received:
		t.Fatalf("extra coalesced delivery=%#v", extra)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSessionOutboundDispatcherRetriesThenDeduplicatesDeliveredEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	results := make(chan SessionOutboundResult, 4)
	dispatcher, err := NewSessionOutboundDispatcher(ctx, SessionOutboundDispatcherOptions{
		Delivery: OutboundDeliveryOptions{MaxAttempts: 2, RetryDelay: time.Millisecond, Timeout: time.Second},
		Handlers: map[string]SessionOutboundHandler{
			"feishu": func(_ context.Context, _ IMSessionBinding, _ ServerMessage) (OutboundReport, error) {
				if calls.Add(1) == 1 {
					return OutboundReport{Platform: "feishu"}, errors.New("temporary failure")
				}
				return OutboundReport{Platform: "feishu", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result SessionOutboundResult) { results <- result },
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	defer dispatcher.Close()
	if err := dispatcher.Bind(IMSessionBinding{SessionID: "im:feishu:oc_1", Platform: "feishu", ChatID: "oc_1", OwnerUserID: "local"}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	event := SessionOutboundEvent{SessionID: "im:feishu:oc_1", EventID: 10, Message: ServerMessage{"type": "error", "message": "failed"}}
	if !dispatcher.Enqueue(event) {
		t.Fatal("first enqueue failed")
	}
	first := receiveOutboundResult(t, results)
	if first.MessageType != "error" || !first.Delivery.Delivered || first.Delivery.Attempts != 2 || first.Delivery.Duplicate {
		t.Fatalf("first delivery result = %#v", first)
	}
	if !dispatcher.Enqueue(event) {
		t.Fatal("duplicate enqueue failed")
	}
	second := receiveOutboundResult(t, results)
	if !second.Delivery.Delivered || !second.Delivery.Duplicate {
		t.Fatalf("duplicate delivery = %#v", second.Delivery)
	}
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d, want 2", calls.Load())
	}
}

func TestSessionOutboundDispatcherValidatesCanonicalOwnedBinding(t *testing.T) {
	dispatcher, err := NewSessionOutboundDispatcher(context.Background(), SessionOutboundDispatcherOptions{
		Handlers: map[string]SessionOutboundHandler{"wechat": func(context.Context, IMSessionBinding, ServerMessage) (OutboundReport, error) {
			return OutboundReport{}, nil
		}},
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	defer dispatcher.Close()
	for _, binding := range []IMSessionBinding{
		{SessionID: "im:wechat:user-1", Platform: "wechat", ChatID: "other", OwnerUserID: "local"},
		{SessionID: "im:wechat:user-1", Platform: "wechat", ChatID: "user-1"},
	} {
		if err := dispatcher.Bind(binding); err == nil {
			t.Fatalf("unsafe binding accepted: %#v", binding)
		}
	}
}

func TestSessionOutboundDispatcherFailsClosedWhenRouteAuthorizationIsRevoked(t *testing.T) {
	var handlerCalls atomic.Int32
	results := make(chan SessionOutboundResult, 1)
	dispatcher, err := NewSessionOutboundDispatcher(context.Background(), SessionOutboundDispatcherOptions{
		Authorize: func(binding IMSessionBinding) error {
			if binding.SenderID != "paired-user" {
				t.Fatalf("authorize binding = %#v", binding)
			}
			return errors.New("route pairing was revoked")
		},
		Handlers: map[string]SessionOutboundHandler{
			"wechat": func(context.Context, IMSessionBinding, ServerMessage) (OutboundReport, error) {
				handlerCalls.Add(1)
				return OutboundReport{Platform: "wechat", DeliverySummary: "delivered"}, nil
			},
		},
		OnResult: func(result SessionOutboundResult) { results <- result },
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	defer dispatcher.Close()
	if err := dispatcher.Bind(IMSessionBinding{
		SessionID: "im:wechat:42", Platform: "wechat", ChatID: "42",
		SenderID: "paired-user", OwnerUserID: "local",
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !dispatcher.Enqueue(SessionOutboundEvent{
		SessionID: "im:wechat:42", EventID: 12,
		Message: ServerMessage{"type": "message_complete"},
	}) {
		t.Fatal("enqueue blocked event")
	}
	result := receiveOutboundResult(t, results)
	if result.Delivery.Delivered || result.Report.DeliverySummary != "blocked" || !strings.Contains(result.Delivery.Error, "revoked") {
		t.Fatalf("blocked result = %#v", result)
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", handlerCalls.Load())
	}
}

func TestSessionOutboundDispatcherUnbindCancelsInFlightDelivery(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	dispatcher, err := NewSessionOutboundDispatcher(context.Background(), SessionOutboundDispatcherOptions{
		Handlers: map[string]SessionOutboundHandler{
			"wechat": func(ctx context.Context, _ IMSessionBinding, _ ServerMessage) (OutboundReport, error) {
				close(started)
				<-ctx.Done()
				close(canceled)
				return OutboundReport{Platform: "wechat"}, ctx.Err()
			},
		},
		Delivery: OutboundDeliveryOptions{MaxAttempts: 1, Timeout: time.Minute},
	})
	if err != nil {
		t.Fatalf("new dispatcher: %v", err)
	}
	defer dispatcher.Close()
	if err := dispatcher.Bind(IMSessionBinding{
		SessionID: "im:wechat:77", Platform: "wechat", ChatID: "77",
		SenderID: "paired-user", OwnerUserID: "local",
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !dispatcher.Enqueue(SessionOutboundEvent{
		SessionID: "im:wechat:77", EventID: 20,
		Message: ServerMessage{"type": "error", "message": "cancel me"},
	}) {
		t.Fatal("enqueue in-flight event")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not start")
	}
	dispatcher.Unbind("im:wechat:77")
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("unbind did not cancel in-flight delivery")
	}
	if dispatcher.Enqueue(SessionOutboundEvent{
		SessionID: "im:wechat:77", EventID: 21,
		Message: ServerMessage{"type": "message_complete"},
	}) {
		t.Fatal("unbound route accepted another event")
	}
}

func receiveOutboundMessages(t *testing.T, source <-chan ServerMessage, count int) []ServerMessage {
	t.Helper()
	result := make([]ServerMessage, 0, count)
	for len(result) < count {
		select {
		case message := <-source:
			result = append(result, message)
		case <-time.After(2 * time.Second):
			t.Fatalf("received %d/%d outbound messages", len(result), count)
		}
	}
	return result
}

func receiveOutboundResult(t *testing.T, source <-chan SessionOutboundResult) SessionOutboundResult {
	t.Helper()
	select {
	case result := <-source:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for outbound delivery result")
		return SessionOutboundResult{}
	}
}
