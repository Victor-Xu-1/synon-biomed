package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/config"
)

func TestNewIMOutboundDispatcherStaysDisabledWithoutEnabledAdapters(t *testing.T) {
	dispatcher, err := newIMOutboundDispatcher(context.Background(), config.Config{}, nil)
	if err != nil {
		t.Fatalf("newIMOutboundDispatcher() error = %v", err)
	}
	if dispatcher != nil {
		dispatcher.Close()
		t.Fatalf("newIMOutboundDispatcher() = %#v", dispatcher)
	}
}

func TestNewIMOutboundDispatcherFailsClosedWithoutBidirectionalCredentials(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{name: "wechat", cfg: config.Config{EnabledAdapters: []string{"wechat"}, WeChat: config.WeChatConfig{BotToken: "token"}}, want: "account_id"},
		{name: "feishu", cfg: config.Config{EnabledAdapters: []string{"feishu"}, Feishu: config.FeishuConfig{AppID: "app"}}, want: "app_secret"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher, err := newIMOutboundDispatcher(context.Background(), tc.cfg, nil)
			if dispatcher != nil {
				dispatcher.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("newIMOutboundDispatcher() dispatcher=%#v error=%v", dispatcher, err)
			}
		})
	}
}

func TestNewIMOutboundDispatcherBuildsEveryRetainedPlatformHandler(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
	}{
		{name: "wechat", cfg: config.Config{EnabledAdapters: []string{"wechat"}, WeChat: config.WeChatConfig{AccountID: "account", BotToken: "token", BaseURL: "https://wechat.example"}}},
		{name: "feishu", cfg: config.Config{EnabledAdapters: []string{"feishu"}, Feishu: config.FeishuConfig{AppID: "app", AppSecret: "secret", Domain: "https://feishu.example"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher, err := newIMOutboundDispatcher(context.Background(), tc.cfg, nil)
			if err != nil {
				t.Fatalf("newIMOutboundDispatcher() error = %v", err)
			}
			if dispatcher == nil {
				t.Fatal("newIMOutboundDispatcher() returned nil")
			}
			dispatcher.Close()
		})
	}
}

func TestValidateAdapterBaseURLRejectsCredentialAndTransportDowngrades(t *testing.T) {
	for _, value := range []string{
		"http://api.example",
		"https://user:password@api.example",
		"https://api.example?token=secret",
		"file:///tmp/adapter.sock",
	} {
		if _, err := validateAdapterBaseURL(value, "test API"); err == nil {
			t.Fatalf("validateAdapterBaseURL(%q) accepted unsafe origin", value)
		}
	}
	if got, err := validateAdapterBaseURL("http://127.0.0.1:18080/fixture/", "test API"); err != nil || got != "http://127.0.0.1:18080/fixture" {
		t.Fatalf("loopback fixture origin = %q, %v", got, err)
	}
}

func TestCachedAdapterTokenReusesFreshCredential(t *testing.T) {
	var calls atomic.Int32
	cache := &cachedAdapterToken{fetch: func(context.Context) (string, time.Duration, error) {
		calls.Add(1)
		return "token-value", time.Hour, nil
	}}
	for index := 0; index < 2; index++ {
		token, err := cache.Token(context.Background())
		if err != nil || token != "token-value" {
			t.Fatalf("Token() = %q, %v", token, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("credential fetch calls = %d", calls.Load())
	}
}
