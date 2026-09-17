package main

import (
	"fmt"
	"os"
	"strings"

	"synon-go/internal/config"
	runtimekv "synon-go/internal/persistence/runtimekv"
	secretstore "synon-go/internal/persistence/secrets"
)

const messageChannelRuntimeNamespace = "message-channels"

// applyPersistedMessageChannelRuntimeConfig is the restart boundary for QR
// pairing. The server stores only a secret reference in the SQLite runtime
// state store; credentials remain in the encrypted vault.
func applyPersistedMessageChannelRuntimeConfig(cfg *config.Config, runtime *runtimekv.Store) error {
	if cfg == nil || strings.TrimSpace(cfg.HomeDir) == "" {
		return nil
	}
	if runtime == nil {
		return fmt.Errorf("message channel runtime state is required")
	}
	entries, err := runtime.List(messageChannelRuntimeNamespace)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read persisted message channel runtime state: %w", err)
	}
	vault := secretstore.New(cfg.HomeDir)
	for _, entry := range entries {
		value, ok := entry.Value.(map[string]any)
		if !ok {
			continue
		}
		platform := strings.ToLower(strings.TrimSpace(messageChannelStringValue(value["platform"])))
		if platform != "feishu" && platform != "wechat" {
			continue
		}
		ownerUserID := strings.TrimSpace(messageChannelStringValue(value["ownerUserId"]))
		if !messageChannelRuntimeOwnerAllowed(ownerUserID, cfg.SynonLinkAuth.Username) {
			continue
		}
		secretID := strings.TrimSpace(messageChannelStringValue(value["credentialSecretId"]))
		if ownerUserID == "" || secretID == "" {
			continue
		}
		secret, found, err := vault.ResolveForUser(secretID, ownerUserID)
		if err != nil {
			return fmt.Errorf("read persisted %s message channel credentials: %w", platform, err)
		}
		if !found {
			continue
		}
		switch platform {
		case "feishu":
			if applyPersistedFeishuCredentials(cfg, secret) {
				cfg.EnabledAdapters = appendMessageChannelAdapter(cfg.EnabledAdapters, "feishu")
			}
		case "wechat":
			if applyPersistedWeChatCredentials(cfg, secret) {
				cfg.EnabledAdapters = appendMessageChannelAdapter(cfg.EnabledAdapters, "wechat")
			}
		}
	}
	return nil
}

func applyPersistedFeishuCredentials(cfg *config.Config, secret secretstore.Secret) bool {
	clientID := strings.TrimSpace(secret.Credentials["client_id"])
	clientSecret := strings.TrimSpace(secret.Credentials["client_secret"])
	if clientID == "" || clientSecret == "" {
		return false
	}
	cfg.Feishu.AppID = clientID
	cfg.Feishu.AppSecret = clientSecret
	return true
}

func applyPersistedWeChatCredentials(cfg *config.Config, secret secretstore.Secret) bool {
	accountID := strings.TrimSpace(secret.Credentials["account_id"])
	botToken := strings.TrimSpace(secret.Credentials["bot_token"])
	if accountID == "" || botToken == "" {
		return false
	}
	cfg.WeChat.AccountID = accountID
	cfg.WeChat.BotToken = botToken
	if baseURL := strings.TrimSpace(secret.Credentials["base_url"]); baseURL != "" {
		cfg.WeChat.BaseURL = baseURL
	}
	if userID := strings.TrimSpace(secret.Credentials["user_id"]); userID != "" {
		cfg.WeChat.UserID = userID
	}
	return true
}

func appendMessageChannelAdapter(adapters []string, target string) []string {
	for _, adapter := range adapters {
		if strings.EqualFold(strings.TrimSpace(adapter), target) {
			return adapters
		}
	}
	return append(adapters, target)
}

func messageChannelRuntimeOwnerAllowed(ownerUserID, configuredUsername string) bool {
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" || ownerUserID == secretstore.DefaultUserID {
		return true
	}
	return ownerUserID == strings.TrimSpace(configuredUsername)
}

func messageChannelStringValue(value any) string {
	text, _ := value.(string)
	return text
}
