package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type StreamRouterOptions struct {
	OnInboundEvent func(context.Context, InboundEvent) error
}

type StreamRouter struct {
	onInboundEvent func(context.Context, InboundEvent) error
}

type SDKStreamClientConfig struct {
	AppID         string
	AppSecret     string
	Domain        string
	AutoReconnect bool
}

func NewStreamRouter(options StreamRouterOptions) *StreamRouter {
	return &StreamRouter{
		onInboundEvent: options.OnInboundEvent,
	}
}

func NewStreamEventDispatcher(verificationToken string, encryptKey string, router *StreamRouter) *larkdispatcher.EventDispatcher {
	dispatcher := larkdispatcher.NewEventDispatcher(verificationToken, encryptKey)
	dispatcher.InitConfig(larkevent.WithLogger(newRedactingFeishuSDKLogger(os.Stdout)))
	dispatcher.OnCustomizedEvent("im.message.receive_v1", func(ctx context.Context, req *larkevent.EventReq) error {
		if router == nil {
			return nil
		}
		return router.HandleRawEvent(ctx, req.Body)
	})
	return dispatcher
}

func NewSDKStreamClient(cfg SDKStreamClientConfig, dispatcher *larkdispatcher.EventDispatcher) (*larkws.Client, error) {
	if strings.TrimSpace(cfg.AppID) == "" {
		return nil, errors.New("feishu app id is required")
	}
	if strings.TrimSpace(cfg.AppSecret) == "" {
		return nil, errors.New("feishu app secret is required")
	}
	if dispatcher == nil {
		return nil, errors.New("feishu stream event dispatcher is required")
	}
	options := []larkws.ClientOption{
		larkws.WithEventHandler(dispatcher),
		larkws.WithAutoReconnect(cfg.AutoReconnect),
		larkws.WithLogger(newRedactingFeishuSDKLogger(os.Stdout)),
	}
	if strings.TrimSpace(cfg.Domain) != "" {
		options = append(options, larkws.WithDomain(strings.TrimRight(cfg.Domain, "/")))
	}
	return larkws.NewClient(cfg.AppID, cfg.AppSecret, options...), nil
}

func (r *StreamRouter) HandleRawEvent(ctx context.Context, raw []byte) error {
	eventType, err := streamEventType(raw)
	if err != nil {
		return err
	}
	switch eventType {
	case "im.message.receive_v1":
		event, err := ParseWebhookEvent(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		if event.Inbound != nil && r != nil && r.onInboundEvent != nil {
			return r.onInboundEvent(ctx, *event.Inbound)
		}
		return nil
	default:
		return nil
	}
}

func streamEventType(raw []byte) (string, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", err
	}
	return strings.TrimSpace(firstString(valueAt(root, "header", "event_type"), valueAt(root, "event", "type"))), nil
}
