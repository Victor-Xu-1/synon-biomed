package wechat

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
)

const DefaultOutboundMessageLimit = 2000

type OutboundProcessorOptions struct {
	ContextToken string
	Timeout      time.Duration
	MessageLimit int
}

type OutboundProcessor struct {
	Client  *http.Client
	BaseURL string
	Token   string
	Options OutboundProcessorOptions
}

func NewOutboundProcessor(client *http.Client, baseURL string, token string, options OutboundProcessorOptions) *OutboundProcessor {
	return &OutboundProcessor{
		Client:  client,
		BaseURL: baseURL,
		Token:   token,
		Options: options,
	}
}

func (p *OutboundProcessor) ProcessServerMessage(ctx context.Context, targetUserID string, message adaptercommon.ServerMessage) error {
	_, err := p.ProcessServerMessageWithReport(ctx, targetUserID, message)
	return err
}

func (p *OutboundProcessor) ProcessServerMessageWithReport(ctx context.Context, targetUserID string, message adaptercommon.ServerMessage) (adaptercommon.OutboundReport, error) {
	if p == nil {
		err := errors.New("wechat outbound processor is nil")
		return adaptercommon.OutboundReport{Platform: "wechat", Error: err.Error(), DeliverySummary: "failed"}, err
	}
	report := adaptercommon.NewOutboundReportWithPolicy("wechat", message, p.renderPolicy())
	for _, chunk := range adaptercommon.BuildOutboundChunks(message) {
		report.ObserveChunk(chunk)
		if err := p.SendChunkWithReport(ctx, targetUserID, chunk, &report); err != nil {
			return report.Finish(err)
		}
	}
	return report.Finish(nil)
}

func (p *OutboundProcessor) SendChunk(ctx context.Context, targetUserID string, chunk adaptercommon.OutboundChunk) error {
	return p.SendChunkWithReport(ctx, targetUserID, chunk, nil)
}

func (p *OutboundProcessor) SendChunkWithReport(ctx context.Context, targetUserID string, chunk adaptercommon.OutboundChunk, report *adaptercommon.OutboundReport) error {
	if p == nil {
		return errors.New("wechat outbound processor is nil")
	}
	if chunk.Complete {
		return nil
	}
	text := strings.TrimSpace(chunk.Text)
	if text == "" {
		return nil
	}
	for _, part := range adaptercommon.SplitMessage(text, p.messageLimit()) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if err := SendText(ctx, p.Client, p.BaseURL, p.Token, SendTextOptions{
			To:           targetUserID,
			Text:         part,
			ContextToken: p.Options.ContextToken,
			Timeout:      p.Options.Timeout,
		}); err != nil {
			return err
		}
		report.AddTextPart()
	}
	return nil
}

func (p *OutboundProcessor) messageLimit() int {
	if p.Options.MessageLimit > 0 {
		return p.Options.MessageLimit
	}
	return DefaultOutboundMessageLimit
}

func (p *OutboundProcessor) renderPolicy() adaptercommon.OutboundRenderPolicy {
	return adaptercommon.OutboundRenderPolicy{
		Platform:           "wechat",
		RenderMode:         "wechat-ilink-text",
		MessageLimit:       p.messageLimit(),
		SupportsMarkdown:   false,
		SupportsMedia:      false,
		SupportsCards:      false,
		MediaStrategy:      "text-only",
		CompletionStrategy: "noop",
		Safety: []string{
			"context-token-forwarding",
			"bounded-message-splitting",
		},
	}
}
