package main

import (
	"path/filepath"
	"testing"

	"synon-go/internal/config"
	runtimekv "synon-go/internal/persistence/runtimekv"
	secretstore "synon-go/internal/persistence/secrets"
)

func TestApplyPersistedMessageChannelRuntimeConfigOverlaysEncryptedCredentials(t *testing.T) {
	root := t.TempDir()
	vault := secretstore.New(root)
	if _, err := vault.Create(secretstore.Secret{
		ID:       "_synon_internal_message_channel_wechat_1",
		UserID:   "local",
		Provider: "_synon_internal_message_channel",
		Credentials: map[string]string{
			"account_id": "account-1",
			"bot_token":  "token-1",
			"base_url":   "https://wechat.example",
			"user_id":    "user-1",
		},
	}); err != nil {
		t.Fatalf("create protected credential: %v", err)
	}
	runtime := runtimekv.New(filepath.Join(root, "runtime-state.sqlite"))
	if _, err := runtime.Set(messageChannelRuntimeNamespace, "wechat-1", map[string]any{
		"platform":           "wechat",
		"ownerUserId":        "local",
		"credentialSecretId": "_synon_internal_message_channel_wechat_1",
	}); err != nil {
		t.Fatalf("persist runtime reference: %v", err)
	}

	cfg := config.Config{HomeDir: root, SynonLinkAuth: config.SynonLinkAuthConfig{Username: "local"}}
	if err := applyPersistedMessageChannelRuntimeConfig(&cfg, runtime); err != nil {
		t.Fatalf("apply persisted config: %v", err)
	}
	if cfg.WeChat.AccountID != "account-1" || cfg.WeChat.BotToken != "token-1" || cfg.WeChat.BaseURL != "https://wechat.example" || cfg.WeChat.UserID != "user-1" {
		t.Fatalf("WeChat config overlay = %#v", cfg.WeChat)
	}
	if len(cfg.EnabledAdapters) != 1 || cfg.EnabledAdapters[0] != "wechat" {
		t.Fatalf("enabled adapters = %#v", cfg.EnabledAdapters)
	}
}

func TestMessageChannelRuntimeOwnerFilterDoesNotApplyAnotherOwner(t *testing.T) {
	if messageChannelRuntimeOwnerAllowed("owner-a", "local") {
		t.Fatal("non-local owner should not be applied to a local runtime")
	}
	if !messageChannelRuntimeOwnerAllowed("local", "different-user") {
		t.Fatal("local owner should be accepted for the local runtime")
	}
}
