package common

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultSessionOutboundQueueSize     = 512
	defaultSessionOutboundFlushInterval = 250 * time.Millisecond
	defaultSessionOutboundEnqueueWait   = time.Second
)

type IMSessionBinding struct {
	SessionID    string
	Platform     string
	ChatID       string
	SenderID     string
	MessageID    string
	ContextToken string
	TargetType   string
	TargetID     string
	OwnerUserID  string
}

type SessionOutboundEvent struct {
	SessionID    string
	EventID      int64
	ReceiptToken string
	Message      ServerMessage
}

type SessionOutboundSink interface {
	Bind(IMSessionBinding) error
	Enqueue(SessionOutboundEvent) bool
}

type SessionOutboundUnbinder interface {
	Unbind(sessionID string)
}

type SessionOutboundHandler func(context.Context, IMSessionBinding, ServerMessage) (OutboundReport, error)

type SessionOutboundResult struct {
	SessionID    string                 `json:"sessionId"`
	Platform     string                 `json:"platform"`
	ChatID       string                 `json:"chatId"`
	EventID      int64                  `json:"eventId,omitempty"`
	ReceiptToken string                 `json:"-"`
	MessageType  string                 `json:"messageType,omitempty"`
	Report       OutboundReport         `json:"report"`
	Delivery     OutboundDeliveryResult `json:"delivery"`
}

type SessionOutboundDispatcherOptions struct {
	Handlers      map[string]SessionOutboundHandler
	Authorize     func(IMSessionBinding) error
	QueueSize     int
	FlushInterval time.Duration
	EnqueueWait   time.Duration
	Delivery      OutboundDeliveryOptions
	OnResult      func(SessionOutboundResult)
}

type SessionOutboundDispatcher struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	bindings map[string]IMSessionBinding
	workers  map[string]*sessionOutboundWorker
	handlers map[string]SessionOutboundHandler
	options  SessionOutboundDispatcherOptions
}

type sessionOutboundWorker struct {
	sessionID string
	ctx       context.Context
	cancel    context.CancelFunc
	queue     chan SessionOutboundEvent
	delivery  *OutboundDelivery
}

func NewSessionOutboundDispatcher(ctx context.Context, options SessionOutboundDispatcherOptions) (*SessionOutboundDispatcher, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.QueueSize <= 0 {
		options.QueueSize = defaultSessionOutboundQueueSize
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = defaultSessionOutboundFlushInterval
	}
	if options.EnqueueWait <= 0 {
		options.EnqueueWait = defaultSessionOutboundEnqueueWait
	}
	if options.Delivery.Timeout <= 0 {
		options.Delivery.Timeout = 30 * time.Second
	}
	if options.Delivery.MaxAttempts <= 0 {
		options.Delivery.MaxAttempts = 3
	}
	if options.Delivery.RetryDelay <= 0 {
		options.Delivery.RetryDelay = 500 * time.Millisecond
	}
	options.Delivery.Deduplicate = true
	if options.Delivery.MaxDedupKeys <= 0 {
		options.Delivery.MaxDedupKeys = 2048
	}
	handlers := make(map[string]SessionOutboundHandler, len(options.Handlers))
	for platform, handler := range options.Handlers {
		platform = normalizeIMPlatform(platform)
		if platform == "" || handler == nil {
			return nil, errors.New("session outbound handlers require a platform and handler")
		}
		handlers[platform] = handler
	}
	dispatcherCtx, cancel := context.WithCancel(ctx)
	return &SessionOutboundDispatcher{
		ctx: dispatcherCtx, cancel: cancel,
		bindings: make(map[string]IMSessionBinding),
		workers:  make(map[string]*sessionOutboundWorker),
		handlers: handlers,
		options:  options,
	}, nil
}

func (d *SessionOutboundDispatcher) Bind(binding IMSessionBinding) error {
	if d == nil {
		return errors.New("session outbound dispatcher is nil")
	}
	binding.SessionID = strings.TrimSpace(binding.SessionID)
	binding.Platform = normalizeIMPlatform(binding.Platform)
	binding.ChatID = strings.TrimSpace(binding.ChatID)
	binding.SenderID = strings.TrimSpace(binding.SenderID)
	binding.MessageID = strings.TrimSpace(binding.MessageID)
	binding.ContextToken = strings.TrimSpace(binding.ContextToken)
	binding.TargetType = strings.ToLower(strings.TrimSpace(binding.TargetType))
	binding.TargetID = strings.TrimSpace(binding.TargetID)
	binding.OwnerUserID = strings.TrimSpace(binding.OwnerUserID)
	if binding.SessionID == "" || binding.Platform == "" || binding.ChatID == "" || binding.OwnerUserID == "" {
		return errors.New("session outbound binding requires session, platform, chat, and owner")
	}
	if binding.SessionID != "im:"+binding.Platform+":"+binding.ChatID {
		return errors.New("session outbound binding does not match canonical IM session id")
	}
	if _, ok := d.handlers[binding.Platform]; !ok {
		return fmt.Errorf("session outbound handler is not configured for %s", binding.Platform)
	}
	if binding.TargetType == "" {
		binding.TargetType = "chat"
	}
	if binding.TargetID == "" {
		binding.TargetID = binding.ChatID
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.ctx.Done():
		return errors.New("session outbound dispatcher is closed")
	default:
	}
	d.bindings[binding.SessionID] = binding
	if d.workers[binding.SessionID] == nil {
		workerCtx, workerCancel := context.WithCancel(d.ctx)
		worker := &sessionOutboundWorker{
			sessionID: binding.SessionID,
			ctx:       workerCtx,
			cancel:    workerCancel,
			queue:     make(chan SessionOutboundEvent, d.options.QueueSize),
		}
		worker.delivery = NewOutboundDelivery(func(ctx context.Context, message ServerMessage) error {
			current, handler, ok := d.route(worker.sessionID)
			if !ok {
				return errors.New("session outbound route is unavailable")
			}
			_, err := handler(ctx, current, message)
			return err
		}, d.options.Delivery)
		d.workers[binding.SessionID] = worker
		go d.runWorker(worker)
	}
	return nil
}

func (d *SessionOutboundDispatcher) Enqueue(event SessionOutboundEvent) bool {
	if d == nil || strings.TrimSpace(event.SessionID) == "" || len(event.Message) == 0 {
		return false
	}
	event.SessionID = strings.TrimSpace(event.SessionID)
	event.Message = cloneServerMessage(event.Message)
	d.mu.RLock()
	worker := d.workers[event.SessionID]
	binding := d.bindings[event.SessionID]
	d.mu.RUnlock()
	if worker == nil {
		return false
	}
	timer := time.NewTimer(d.options.EnqueueWait)
	defer timer.Stop()
	select {
	case worker.queue <- event:
		return true
	case <-worker.ctx.Done():
		return false
	case <-timer.C:
		d.emitResult(binding, event.EventID, event.ReceiptToken, strings.TrimSpace(stringValue(event.Message["type"])), OutboundReport{Platform: binding.Platform, Error: "session outbound queue is full", DeliverySummary: "failed"}, OutboundDeliveryResult{Error: "session outbound queue is full"})
		return false
	}
}

func (d *SessionOutboundDispatcher) Unbind(sessionID string) {
	if d == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	d.mu.Lock()
	worker := d.workers[sessionID]
	delete(d.bindings, sessionID)
	delete(d.workers, sessionID)
	d.mu.Unlock()
	if worker != nil && worker.cancel != nil {
		worker.cancel()
	}
}

func (d *SessionOutboundDispatcher) Close() {
	if d != nil && d.cancel != nil {
		d.cancel()
	}
}

func (d *SessionOutboundDispatcher) runWorker(worker *sessionOutboundWorker) {
	normalizer := sessionOutboundNormalizer{}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	armed := false
	arm := func() {
		if armed && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d.options.FlushInterval)
		armed = true
	}
	for {
		select {
		case <-worker.ctx.Done():
			return
		case event := <-worker.queue:
			messages, buffered, acknowledged := normalizer.Accept(event)
			if !buffered && armed {
				if timer.Stop() {
					armed = false
				}
			}
			d.deliverNormalized(worker, messages)
			if acknowledged {
				binding, _, ok := d.route(worker.sessionID)
				if ok {
					d.emitResult(binding, event.EventID, event.ReceiptToken, strings.TrimSpace(stringValue(event.Message["type"])),
						OutboundReport{Platform: binding.Platform, DeliverySummary: "already delivered"},
						OutboundDeliveryResult{Delivered: true})
				}
			}
			if buffered {
				arm()
			}
		case <-timer.C:
			armed = false
			d.deliverNormalized(worker, normalizer.Flush())
		}
	}
}

func (d *SessionOutboundDispatcher) deliverNormalized(worker *sessionOutboundWorker, messages []normalizedSessionOutboundMessage) {
	for _, normalized := range messages {
		binding, handler, ok := d.route(worker.sessionID)
		if !ok {
			continue
		}
		if d.options.Authorize != nil {
			if err := d.options.Authorize(binding); err != nil {
				redacted := RedactOutboundText(err.Error(), binding.ContextToken)
				for _, receipt := range normalizedOutboundReceipts(normalized) {
					d.emitResult(binding, receipt.EventID, receipt.ReceiptToken, receipt.MessageType, OutboundReport{
						Platform: binding.Platform, Error: redacted, DeliverySummary: "blocked",
					}, OutboundDeliveryResult{Error: redacted})
				}
				continue
			}
		}
		var report OutboundReport
		result, err := worker.delivery.deliverWithHandler(worker.ctx, normalized.Message, func(ctx context.Context, message ServerMessage) error {
			var err error
			report, err = handler(ctx, binding, message)
			return err
		})
		if err != nil && report.Error == "" {
			report = OutboundReport{Platform: binding.Platform, Error: RedactOutboundText(err.Error(), binding.ContextToken), DeliverySummary: "failed"}
		}
		report.RedactError(binding.ContextToken)
		result.Error = RedactOutboundText(result.Error, binding.ContextToken)
		for _, receipt := range normalizedOutboundReceipts(normalized) {
			d.emitResult(binding, receipt.EventID, receipt.ReceiptToken, receipt.MessageType, report, result)
		}
	}
}

func (d *SessionOutboundDispatcher) route(sessionID string) (IMSessionBinding, SessionOutboundHandler, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	binding, ok := d.bindings[sessionID]
	if !ok {
		return IMSessionBinding{}, nil, false
	}
	handler := d.handlers[binding.Platform]
	return binding, handler, handler != nil
}

func (d *SessionOutboundDispatcher) emitResult(binding IMSessionBinding, eventID int64, receiptToken string, messageType string, report OutboundReport, delivery OutboundDeliveryResult) {
	if d.options.OnResult == nil {
		return
	}
	d.options.OnResult(SessionOutboundResult{
		SessionID:    binding.SessionID,
		Platform:     binding.Platform,
		ChatID:       binding.ChatID,
		EventID:      eventID,
		ReceiptToken: receiptToken,
		MessageType:  strings.TrimSpace(messageType),
		Report:       report,
		Delivery:     delivery,
	})
}

type normalizedSessionOutboundMessage struct {
	EventID      int64
	ReceiptToken string
	Message      ServerMessage
	Receipts     []sessionOutboundReceipt
}

type sessionOutboundReceipt struct {
	EventID      int64
	ReceiptToken string
	MessageType  string
}

type sessionOutboundNormalizer struct {
	buffer           strings.Builder
	streamed         strings.Builder
	lastEventID      int64
	lastReceiptToken string
	pendingReceipts  []sessionOutboundReceipt
}

func (n *sessionOutboundNormalizer) Accept(event SessionOutboundEvent) ([]normalizedSessionOutboundMessage, bool, bool) {
	messageType := strings.TrimSpace(stringValue(event.Message["type"]))
	switch messageType {
	case "transcript_noop":
		return nil, n.buffer.Len() > 0, true
	case "content_delta":
		text := rawFirstStringValue(event.Message, "text", "content")
		if text != "" {
			n.buffer.WriteString(text)
			n.streamed.WriteString(text)
			n.lastEventID = event.EventID
			n.lastReceiptToken = event.ReceiptToken
			n.pendingReceipts = append(n.pendingReceipts, outboundReceipt(event, messageType))
		}
		return nil, n.buffer.Len() > 0, text == ""
	case "message":
		if strings.TrimSpace(stringValue(event.Message["role"])) != "assistant" {
			return nil, n.buffer.Len() > 0, false
		}
		finalText := rawFirstStringValue(event.Message, "text", "content")
		streamed := n.streamed.String()
		if streamed == "" {
			n.buffer.WriteString(finalText)
		} else if strings.HasPrefix(finalText, streamed) {
			n.buffer.WriteString(strings.TrimPrefix(finalText, streamed))
		} else {
			n.buffer.WriteString(finalText)
		}
		if event.EventID > 0 {
			n.lastEventID = event.EventID
		}
		n.lastReceiptToken = event.ReceiptToken
		if n.buffer.Len() > 0 {
			n.pendingReceipts = append(n.pendingReceipts, outboundReceipt(event, messageType))
		}
		messages := n.Flush()
		return messages, false, len(messages) == 0 && finalText == streamed
	case "runner_checkpoint":
		messages := n.Flush()
		phase := strings.TrimSpace(stringValue(event.Message["toolPhase"]))
		if phase != "" {
			typeName := "tool_result"
			if phase == "start" || phase == "running" {
				typeName = "tool_use"
			}
			messages = append(messages, normalizedSessionOutboundMessage{EventID: event.EventID, ReceiptToken: event.ReceiptToken, Message: withOutboundEventID(ServerMessage{
				"type": typeName, "toolName": stringValue(event.Message["toolName"]),
				"toolUseID": stringValue(event.Message["toolCallId"]), "input": event.Message["toolInput"],
				"message": stringValue(event.Message["text"]),
			}, event.EventID)})
			return messages, false, false
		}
		if strings.Contains(strings.ToLower(stringValue(event.Message["text"])), "runner chat started") {
			n.streamed.Reset()
			messages = append(messages, normalizedSessionOutboundMessage{EventID: event.EventID, ReceiptToken: event.ReceiptToken, Message: withOutboundEventID(ServerMessage{"type": "content_start", "blockType": "text"}, event.EventID)})
			return messages, false, false
		}
		// Planning and maintenance checkpoints are durable runner evidence but
		// intentionally have no external IM projection. Acknowledge them so they
		// cannot hold the ordered delivery cursor forever.
		return messages, false, true
	case "runner_finished":
		messages := n.Flush()
		status := strings.ToLower(strings.TrimSpace(stringValue(event.Message["status"])))
		if status == "failed" {
			messages = append(messages, normalizedSessionOutboundMessage{EventID: event.EventID, ReceiptToken: event.ReceiptToken, Message: withOutboundEventID(ServerMessage{
				"type": "error", "message": RedactOutboundText(stringValue(event.Message["text"])),
			}, event.EventID)})
		} else {
			messages = append(messages, normalizedSessionOutboundMessage{EventID: event.EventID, ReceiptToken: event.ReceiptToken, Message: withOutboundEventID(ServerMessage{"type": "message_complete"}, event.EventID)})
		}
		n.streamed.Reset()
		return messages, false, false
	case "reasoning_delta", "thinking", "tool_use", "tool_use_complete", "tool_result", "permission_request", "error", "message_complete":
		messages := n.Flush()
		messages = append(messages, normalizedSessionOutboundMessage{EventID: event.EventID, ReceiptToken: event.ReceiptToken, Message: withOutboundEventID(cloneServerMessage(event.Message), event.EventID)})
		return messages, false, false
	default:
		return nil, n.buffer.Len() > 0, false
	}
}

func (n *sessionOutboundNormalizer) Flush() []normalizedSessionOutboundMessage {
	if n.buffer.Len() == 0 {
		return nil
	}
	text := n.buffer.String()
	eventID := n.lastEventID
	receiptToken := n.lastReceiptToken
	receipts := append([]sessionOutboundReceipt(nil), n.pendingReceipts...)
	n.buffer.Reset()
	n.lastReceiptToken = ""
	n.pendingReceipts = n.pendingReceipts[:0]
	return []normalizedSessionOutboundMessage{{EventID: eventID, ReceiptToken: receiptToken, Receipts: receipts, Message: withOutboundEventID(ServerMessage{"type": "content_delta", "text": text}, eventID)}}
}

func outboundReceipt(event SessionOutboundEvent, messageType string) sessionOutboundReceipt {
	return sessionOutboundReceipt{EventID: event.EventID, ReceiptToken: event.ReceiptToken, MessageType: messageType}
}

func normalizedOutboundReceipts(message normalizedSessionOutboundMessage) []sessionOutboundReceipt {
	if len(message.Receipts) != 0 {
		return message.Receipts
	}
	return []sessionOutboundReceipt{{
		EventID: message.EventID, ReceiptToken: message.ReceiptToken,
		MessageType: strings.TrimSpace(stringValue(message.Message["type"])),
	}}
}

func withOutboundEventID(message ServerMessage, eventID int64) ServerMessage {
	if eventID > 0 {
		message["eventId"] = fmt.Sprintf("%d", eventID)
	}
	return message
}

func cloneServerMessage(message ServerMessage) ServerMessage {
	cloned := make(ServerMessage, len(message))
	for key, value := range message {
		cloned[key] = value
	}
	return cloned
}

func normalizeIMPlatform(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "feishu", "wechat":
		return strings.ToLower(strings.TrimSpace(platform))
	default:
		return ""
	}
}

func rawFirstStringValue(message ServerMessage, keys ...string) string {
	for _, key := range keys {
		if value, ok := message[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
