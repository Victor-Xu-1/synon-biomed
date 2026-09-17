package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	adaptercommon "synon-go/internal/adapters/common"
)

type ServerMessageProcessor struct {
	cardClient      *CardKitClient
	mediaDispatcher *OutboundMediaDispatcher

	mu            sync.Mutex
	cards         map[string]*StreamingCard
	imageWatchers map[string]*ImageBlockWatcher
	fileWatchers  map[string]*FileBlockWatcher

	mediaWG  sync.WaitGroup
	errMu    sync.Mutex
	mediaErr []error
}

func NewServerMessageProcessor(cardClient *CardKitClient, mediaDispatcher *OutboundMediaDispatcher) *ServerMessageProcessor {
	return &ServerMessageProcessor{
		cardClient:      cardClient,
		mediaDispatcher: mediaDispatcher,
		cards:           map[string]*StreamingCard{},
		imageWatchers:   map[string]*ImageBlockWatcher{},
		fileWatchers:    map[string]*FileBlockWatcher{},
	}
}

func (p *ServerMessageProcessor) Handle(ctx context.Context, chatID string, msg adaptercommon.ServerMessage) error {
	_, err := p.HandleWithReport(ctx, chatID, msg)
	return err
}

func (p *ServerMessageProcessor) HandleWithReport(ctx context.Context, chatID string, msg adaptercommon.ServerMessage) (adaptercommon.OutboundReport, error) {
	if p == nil {
		err := errors.New("feishu server message processor is nil")
		return adaptercommon.OutboundReport{Platform: "feishu", Error: err.Error(), DeliverySummary: "failed"}, err
	}
	report := adaptercommon.NewOutboundReportWithPolicy("feishu", msg, p.renderPolicy())
	err := p.handle(ctx, chatID, msg, &report)
	return report.Finish(err)
}

func (p *ServerMessageProcessor) handle(ctx context.Context, chatID string, msg adaptercommon.ServerMessage, report *adaptercommon.OutboundReport) error {
	if strings.TrimSpace(chatID) == "" {
		return errors.New("feishu chat id is required")
	}
	switch messageType(msg) {
	case "", "connected", "status":
		return nil
	case "content_start":
		switch stringField(msg, "blockType") {
		case "text":
			report.ObserveChunk(adaptercommon.OutboundChunk{Kind: "start"})
			card, err := p.getOrCreateCard(chatID, "")
			if err != nil {
				return err
			}
			if err := card.EnsureCreated(ctx); err != nil {
				return err
			}
			report.AddCardUpdate()
			return nil
		case "tool_use":
			report.ObserveChunk(adaptercommon.OutboundChunk{Kind: "tool"})
			card, err := p.getOrCreateCard(chatID, "")
			if err != nil {
				return err
			}
			if err := card.EnsureCreated(ctx); err != nil {
				return err
			}
			card.StartTool(firstStringField(msg, "toolUseId", "toolUseID"), stringField(msg, "toolName"))
			report.AddCardUpdate()
			return nil
		default:
			return nil
		}
	case "content_delta":
		text := stringField(msg, "text")
		if text == "" {
			return nil
		}
		report.ObserveChunk(adaptercommon.OutboundChunk{Kind: "text", Text: text})
		card, err := p.getOrCreateCard(chatID, "")
		if err != nil {
			return err
		}
		if err := card.EnsureCreated(ctx); err != nil {
			return err
		}
		card.AppendText(text)
		report.AddTextPartOnly()
		report.AddCardUpdate()
		p.dispatchOutboundMediaWithReport(ctx, chatID, text, report)
		return nil
	case "thinking", "reasoning_delta":
		report.ObserveChunk(adaptercommon.OutboundChunk{Kind: "reasoning", Text: stringField(msg, "text")})
		card, err := p.getOrCreateCard(chatID, "")
		if err != nil {
			return err
		}
		if err := card.EnsureCreated(ctx); err != nil {
			return err
		}
		card.AppendReasoning(stringField(msg, "text"))
		report.AddCardUpdate()
		return nil
	case "tool_use_complete", "tool_result":
		report.ObserveChunk(adaptercommon.OutboundChunk{Kind: "tool"})
		card, err := p.getOrCreateCard(chatID, "")
		if err != nil {
			return err
		}
		if err := card.EnsureCreated(ctx); err != nil {
			return err
		}
		toolUseID := firstStringField(msg, "toolUseId", "toolUseID")
		toolName := stringField(msg, "toolName")
		if firstBoolField(msg, "is_error", "isError") {
			card.FailTool(toolUseID, toolName)
		} else {
			card.CompleteTool(toolUseID, toolName)
		}
		report.AddCardUpdate()
		return nil
	case "message_complete":
		report.ObserveChunk(adaptercommon.OutboundChunk{Kind: "complete", Complete: true})
		if card := p.cardFor(chatID); card != nil {
			if err := card.Finalize(ctx); err != nil {
				return err
			}
			report.AddCardUpdate()
		}
		p.clearChat(chatID)
		return nil
	case "error":
		report.ObserveChunk(adaptercommon.OutboundChunk{Kind: "error", Text: stringField(msg, "message")})
		message := stringField(msg, "message")
		if strings.TrimSpace(message) == "" {
			message = "unknown error"
		}
		if card := p.cardFor(chatID); card != nil {
			if err := card.Abort(ctx, message); err != nil {
				return err
			}
			report.AddCardUpdate()
			p.clearChat(chatID)
			return nil
		}
		if p.cardClient == nil {
			return errors.New("feishu card client is required")
		}
		_, err := p.cardClient.SendRenderedCardAsMessage(ctx, chatID, BuildErrorCard(message), "")
		if err == nil {
			report.AddCardUpdate()
		}
		return err
	default:
		return nil
	}
}

func (p *ServerMessageProcessor) HasActiveCard(chatID string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cards[chatID] != nil
}

func (p *ServerMessageProcessor) WaitForMedia(ctx context.Context) error {
	if p == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		p.mediaWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.errMu.Lock()
	defer p.errMu.Unlock()
	err := errors.Join(p.mediaErr...)
	p.mediaErr = nil
	return err
}

func (p *ServerMessageProcessor) getOrCreateCard(chatID string, replyToMessageID string) (*StreamingCard, error) {
	if p.cardClient == nil {
		return nil, errors.New("feishu card client is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cards == nil {
		p.cards = map[string]*StreamingCard{}
	}
	card := p.cards[chatID]
	if card == nil {
		card = NewStreamingCard(p.cardClient, chatID, replyToMessageID)
		p.cards[chatID] = card
	}
	return card, nil
}

func (p *ServerMessageProcessor) cardFor(chatID string) *StreamingCard {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cards[chatID]
}

func (p *ServerMessageProcessor) imageWatcher(chatID string) *ImageBlockWatcher {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.imageWatchers == nil {
		p.imageWatchers = map[string]*ImageBlockWatcher{}
	}
	watcher := p.imageWatchers[chatID]
	if watcher == nil {
		watcher = NewImageBlockWatcher()
		p.imageWatchers[chatID] = watcher
	}
	return watcher
}

func (p *ServerMessageProcessor) fileWatcher(chatID string) *FileBlockWatcher {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fileWatchers == nil {
		p.fileWatchers = map[string]*FileBlockWatcher{}
	}
	watcher := p.fileWatchers[chatID]
	if watcher == nil {
		watcher = NewFileBlockWatcher()
		p.fileWatchers[chatID] = watcher
	}
	return watcher
}

func (p *ServerMessageProcessor) clearChat(chatID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cards, chatID)
	delete(p.imageWatchers, chatID)
	delete(p.fileWatchers, chatID)
}

func (p *ServerMessageProcessor) renderPolicy() adaptercommon.OutboundRenderPolicy {
	policy := adaptercommon.OutboundRenderPolicy{
		Platform:           "feishu",
		RenderMode:         "feishu-cardkit-stream",
		SupportsMarkdown:   true,
		SupportsMedia:      p.mediaDispatcher != nil,
		SupportsCards:      true,
		MediaStrategy:      "cardkit-stream-plus-media-dispatcher",
		CompletionStrategy: "finalize-or-abort-card-and-clear-watchers",
		Safety: []string{
			"cardkit-table-sanitization",
			"async-media-error-collection",
			"watcher-state-cleared-on-complete",
		},
	}
	if p.mediaDispatcher == nil {
		policy.MediaStrategy = "cardkit-stream-no-media-dispatcher"
	}
	return policy
}

func (p *ServerMessageProcessor) dispatchOutboundMediaWithReport(ctx context.Context, chatID string, text string, report *adaptercommon.OutboundReport) {
	if p.mediaDispatcher == nil || strings.TrimSpace(text) == "" {
		return
	}
	for _, pending := range p.imageWatcher(chatID).Feed(text) {
		p.dispatchMediaAsync(ctx, func(ctx context.Context) error {
			return p.mediaDispatcher.DispatchImage(ctx, chatID, pending)
		})
		report.AddMediaUpload()
	}
	for _, pending := range p.fileWatcher(chatID).Feed(text) {
		p.dispatchMediaAsync(ctx, func(ctx context.Context) error {
			return p.mediaDispatcher.DispatchFile(ctx, chatID, pending)
		})
		report.AddMediaUpload()
	}
}

func (p *ServerMessageProcessor) dispatchMediaAsync(ctx context.Context, run func(context.Context) error) {
	p.mediaWG.Add(1)
	go func() {
		defer p.mediaWG.Done()
		if err := run(ctx); err != nil {
			p.errMu.Lock()
			p.mediaErr = append(p.mediaErr, err)
			p.errMu.Unlock()
		}
	}()
}

func messageType(msg adaptercommon.ServerMessage) string {
	return stringField(msg, "type")
}

func stringField(msg adaptercommon.ServerMessage, key string) string {
	value, _ := msg[key]
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func firstStringField(msg adaptercommon.ServerMessage, keys ...string) string {
	for _, key := range keys {
		if value := stringField(msg, key); value != "" {
			return value
		}
	}
	return ""
}

func firstBoolField(msg adaptercommon.ServerMessage, keys ...string) bool {
	for _, key := range keys {
		if value, ok := msg[key].(bool); ok {
			return value
		}
	}
	return false
}
