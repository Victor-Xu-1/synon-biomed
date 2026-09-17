package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
	adapterfeishu "synon-go/internal/adapters/feishu"
	adapterwechat "synon-go/internal/adapters/wechat"
	"synon-go/internal/config"
)

const maxAdapterCredentialResponseBytes = 1024 * 1024

type cachedAdapterToken struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	fetch     func(context.Context) (string, time.Duration, error)
}

type imOutboundRuntimeHooks struct {
	Authorize func(adaptercommon.IMSessionBinding) error
	OnResult  func(adaptercommon.SessionOutboundResult) error
}

func (c *cachedAdapterToken) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expiresAt) > 2*time.Minute {
		return c.token, nil
	}
	token, ttl, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("adapter credential endpoint returned an empty token")
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	c.token = token
	c.expiresAt = time.Now().UTC().Add(ttl)
	return token, nil
}

func newIMOutboundDispatcher(ctx context.Context, cfg config.Config, baseHTTPClient *http.Client, optionalHooks ...imOutboundRuntimeHooks) (*adaptercommon.SessionOutboundDispatcher, error) {
	if len(optionalHooks) > 1 {
		return nil, errors.New("one IM outbound runtime hook set is allowed")
	}
	hooks := imOutboundRuntimeHooks{}
	if len(optionalHooks) == 1 {
		hooks = optionalHooks[0]
	}
	handlers := map[string]adaptercommon.SessionOutboundHandler{}
	client := newAdapterHTTPClient(baseHTTPClient)

	if adapterEnabled(cfg.EnabledAdapters, "wechat") {
		if strings.TrimSpace(cfg.WeChat.AccountID) == "" || strings.TrimSpace(cfg.WeChat.BotToken) == "" {
			return nil, errors.New("wechat adapter requires account_id and bot_token for bidirectional delivery")
		}
		baseURL, err := validateAdapterBaseURL(firstNonEmpty(strings.TrimSpace(cfg.WeChat.BaseURL), adapterwechat.DefaultBaseURL), "wechat API base")
		if err != nil {
			return nil, err
		}
		handlers["wechat"] = func(ctx context.Context, binding adaptercommon.IMSessionBinding, message adaptercommon.ServerMessage) (adaptercommon.OutboundReport, error) {
			processor := adapterwechat.NewOutboundProcessor(client, baseURL, cfg.WeChat.BotToken, adapterwechat.OutboundProcessorOptions{
				ContextToken: binding.ContextToken,
				Timeout:      30 * time.Second,
			})
			report, err := processor.ProcessServerMessageWithReport(ctx, binding.TargetID, message)
			return sanitizeIMOutboundResult(report, err, cfg.WeChat.BotToken, binding.ContextToken)
		}
	}

	if adapterEnabled(cfg.EnabledAdapters, "feishu") {
		if strings.TrimSpace(cfg.Feishu.AppID) == "" || strings.TrimSpace(cfg.Feishu.AppSecret) == "" {
			return nil, errors.New("feishu adapter requires app_id and app_secret for bidirectional delivery")
		}
		domain, err := validateAdapterBaseURL(firstNonEmpty(strings.TrimSpace(cfg.Feishu.Domain), "https://open.feishu.cn"), "feishu domain")
		if err != nil {
			return nil, err
		}
		tokens := &cachedAdapterToken{fetch: func(ctx context.Context) (string, time.Duration, error) {
			return fetchFeishuRuntimeToken(ctx, client, domain, cfg.Feishu.AppID, cfg.Feishu.AppSecret)
		}}
		tokenProvider := func(ctx context.Context) (string, error) { return tokens.Token(ctx) }
		cardClient := adapterfeishu.NewCardKitClient(client, domain, tokenProvider)
		mediaClient := adapterfeishu.NewMediaClient(client, domain, tokenProvider)
		processor := adapterfeishu.NewServerMessageProcessor(cardClient, adapterfeishu.NewOutboundMediaDispatcher(mediaClient))
		handlers["feishu"] = func(ctx context.Context, binding adaptercommon.IMSessionBinding, message adaptercommon.ServerMessage) (adaptercommon.OutboundReport, error) {
			report, err := processor.HandleWithReport(ctx, binding.TargetID, message)
			return sanitizeIMOutboundResult(report, err, cfg.Feishu.AppSecret)
		}
	}

	if len(handlers) == 0 {
		return nil, nil
	}
	return adaptercommon.NewSessionOutboundDispatcher(ctx, adaptercommon.SessionOutboundDispatcherOptions{
		Handlers:  handlers,
		Authorize: hooks.Authorize,
		OnResult: func(result adaptercommon.SessionOutboundResult) {
			if hooks.OnResult != nil {
				if err := hooks.OnResult(result); err != nil {
					log.Printf("persist IM outbound delivery result session=%s event=%d: %v", result.SessionID, result.EventID, err)
				}
			}
			if result.Delivery.Error != "" || result.Report.Error != "" {
				log.Printf("IM outbound delivery failed platform=%s session=%s event=%d error=%s", result.Platform, result.SessionID, result.EventID, firstNonEmpty(result.Delivery.Error, result.Report.Error))
			}
		},
	})
}

func sanitizeIMOutboundResult(report adaptercommon.OutboundReport, err error, secrets ...string) (adaptercommon.OutboundReport, error) {
	report.RedactError(secrets...)
	if err == nil {
		return report, nil
	}
	return report, errors.New(adaptercommon.RedactOutboundText(err.Error(), secrets...))
}

func newAdapterHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	if clone.Timeout <= 0 {
		clone.Timeout = 35 * time.Second
	}
	clone.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("adapter API redirects are not allowed")
	}
	return &clone
}

func validateAdapterBaseURL(value string, label string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	if parsed.User != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must be an origin without credentials, query, or fragment", label)
	}
	loopback := parsed.Hostname() == "localhost"
	if ip := net.ParseIP(parsed.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return "", fmt.Errorf("%s must use HTTPS except for a loopback test endpoint", label)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func fetchFeishuRuntimeToken(ctx context.Context, client *http.Client, domain string, appID string, appSecret string) (string, time.Duration, error) {
	var response struct {
		Code              int    `json:"code"`
		Message           string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	err := postAdapterCredentialJSON(ctx, client, domain, "/open-apis/auth/v3/tenant_access_token/internal", map[string]string{
		"app_id": appID, "app_secret": appSecret,
	}, &response, appSecret)
	if err != nil {
		return "", 0, err
	}
	if response.Code != 0 || strings.TrimSpace(response.TenantAccessToken) == "" {
		return "", 0, fmt.Errorf("feishu tenant token missing: code=%d message=%s", response.Code, adaptercommon.RedactOutboundText(response.Message, appSecret))
	}
	return response.TenantAccessToken, time.Duration(response.Expire) * time.Second, nil
}

func postAdapterCredentialJSON(ctx context.Context, client *http.Client, baseURL string, path string, body any, output any, secrets ...string) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	target, err := url.JoinPath(strings.TrimRight(baseURL, "/")+"/", strings.TrimPrefix(path, "/"))
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errors.New(adaptercommon.RedactOutboundText(err.Error(), secrets...))
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxAdapterCredentialResponseBytes+1))
	if err != nil {
		return err
	}
	if len(responseBody) > maxAdapterCredentialResponseBytes {
		return errors.New("adapter credential response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("adapter credential endpoint returned %d: %s", response.StatusCode, adaptercommon.RedactOutboundText(string(responseBody), secrets...))
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("decode adapter credential response: %w", err)
	}
	return nil
}
