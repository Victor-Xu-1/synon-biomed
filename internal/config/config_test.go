package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"synon-go/internal/persistence/transcript"
)

func TestLoadUsesBoundedRunnerToolRoundDefault(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_RUNNER_CHAT_TOOL_ROUND_LIMIT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Runner.ChatToolRoundLimit != 64 {
		t.Fatalf("Runner.ChatToolRoundLimit = %d, want bounded default 64", cfg.Runner.ChatToolRoundLimit)
	}
}

func TestDefaultHomeUsesPlatformUserHomeWhenHOMEIsUnset(t *testing.T) {
	t.Setenv("HOME", "")
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skipf("platform user home is unavailable: %v", err)
	}
	if got, want := defaultHome(), filepath.Join(home, ".synon-go"); got != want {
		t.Fatalf("defaultHome=%q, want %q", got, want)
	}
}

func TestLoadDefaultWebSessionOutlivesTwentyFourHourRun(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_LINK_AUTH_SESSION_TTL_MINUTES", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SynonLinkAuth.SessionTTLMinutes != 48*60 {
		t.Fatalf("SessionTTLMinutes = %d, want 2880", cfg.SynonLinkAuth.SessionTTLMinutes)
	}
}

func TestLoadUsesV11RunnerModelRetryDefault(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_RUNNER_CHAT_MAX_ATTEMPTS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Runner.ChatMaxAttempts != 4 {
		t.Fatalf("Runner.ChatMaxAttempts = %d, want v1.1 default 4", cfg.Runner.ChatMaxAttempts)
	}
}

func TestLoadReadsRealJSONConfigAndEnvironment(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "synon.json")
	if err := os.WriteFile(configPath, []byte(`{
  "host": "127.0.0.1",
  "port": 18080,
  "synon_link_package": "assets/synon-link/synon-link-extension-v0.6.10.zip",
  "synon_link_auth": {
    "username": "file-link-user",
    "password": "file-link-password",
    "session_ttl_minutes": 180
  },
  "plugin_directories": ["file-plugin-a"],
  "enabled_adapters": ["feishu", "wechat"],
  "feishu": {
    "app_id": "file-feishu-app",
    "app_secret": "file-feishu-secret",
    "verification_token": "file-feishu-token",
    "encrypt_key": "file-feishu-encrypt",
    "domain": "https://file.feishu.example"
  },
  "wechat": {
    "account_id": "file-account",
    "bot_token": "file-wechat-token",
    "base_url": "https://file.wechat.example",
    "user_id": "file-user",
    "poll_interval_ms": 2500
  },
  "live_im_smoke": {
    "message": "file live smoke",
    "timeout_seconds": 45,
    "runtime_url": "http://127.0.0.1:18080",
    "require_all": true,
    "wechat_user_id": "wx-live",
    "wechat_context_token": "wx-context",
    "feishu_chat_id": "oc_live",
    "feishu_open_id": "ou_live"
  },
  "runner": {
    "enabled": true,
    "id": "file-runner",
    "provider": "openai_chat",
    "command": "file-runner-command",
    "args": ["--file"],
    "chat_endpoint": "https://file.model.example/v1/chat/completions",
    "chat_api_key": "file-model-key",
    "chat_model": "file-model",
    "chat_system_prompt": "file system prompt",
    "chat_tools": ["task_create", "runtime_get"],
    "chat_timeout_seconds": 45,
    "chat_tool_round_limit": 5,
    "chat_tool_call_batch_limit": 6,
    "chat_max_attempts": 3,
	"chat_workers": 6,
    "command_timeout_seconds": 120,
    "poll_interval_ms": 2000,
    "lease_ttl_seconds": 90,
    "replay_limit": 25,
    "output_limit_bytes": 2048
  },
  "compact_summarizer": {
    "endpoint": "https://file.compact.example/v1/chat/completions",
    "api_key": "file-compact-key",
    "model": "file-compact-model",
    "timeout_seconds": 35,
    "max_attempts": 2
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SYNON_HOME", filepath.Join(root, "state"))
	t.Setenv("SYNON_CONFIG", configPath)
	t.Setenv("SYNON_LINK_AUTH_USERNAME", "env-link-user")
	t.Setenv("SYNON_LINK_AUTH_PASSWORD", "env-link-password")
	t.Setenv("SYNON_LINK_AUTH_SESSION_TTL_MINUTES", "240")
	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "https://biomed.example/")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "google-client-id")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "google-client-secret")
	t.Setenv("SYNON_AUTH_APPLE_CLIENT_ID", "com.synon.biomed.web")
	t.Setenv("SYNON_AUTH_APPLE_TEAM_ID", "apple-team-id")
	t.Setenv("SYNON_AUTH_APPLE_KEY_ID", "apple-key-id")
	t.Setenv("SYNON_AUTH_APPLE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString([]byte("apple-private-key")))
	t.Setenv("SYNON_AUTH_WECHAT_APP_ID", "wechat-app-id")
	t.Setenv("SYNON_AUTH_WECHAT_APP_SECRET", "wechat-app-secret")
	t.Setenv("WECHAT_ACCOUNT_ID", "env-account")
	t.Setenv("WECHAT_BOT_TOKEN", "env-wechat-token")
	t.Setenv("WECHAT_BASE_URL", "https://env.wechat.example")
	t.Setenv("WECHAT_USER_ID", "env-user")
	t.Setenv("WECHAT_POLL_INTERVAL_MS", "250")
	t.Setenv("FEISHU_APP_ID", "env-feishu-app")
	t.Setenv("FEISHU_APP_SECRET", "env-feishu-secret")
	t.Setenv("FEISHU_VERIFICATION_TOKEN", "env-feishu-token")
	t.Setenv("FEISHU_ENCRYPT_KEY", "env-feishu-encrypt")
	t.Setenv("FEISHU_DOMAIN", "https://env.feishu.example")
	t.Setenv("SYNON_PLUGIN_DIRS", "env-plugin-a, env-plugin-b")
	t.Setenv("SYNON_SKILL_DIRS", "env-skill-a, env-skill-b")
	t.Setenv("SYNON_ENABLED_ADAPTERS", "feishu, wechat, feishu")
	t.Setenv("SYNON_RUNNER_ENABLED", "true")
	t.Setenv("SYNON_RUNNER_ID", "env-runner")
	t.Setenv("SYNON_RUNNER_PROVIDER", "openai_chat")
	t.Setenv("SYNON_RUNNER_COMMAND", "env-runner-command")
	t.Setenv("SYNON_RUNNER_ARGS_JSON", `["--env","1"]`)
	t.Setenv("SYNON_RUNNER_CHAT_ENDPOINT", "https://env.model.example/v1/chat/completions")
	t.Setenv("SYNON_RUNNER_CHAT_API_KEY", "env-model-key")
	t.Setenv("SYNON_RUNNER_CHAT_MODEL", "env-model")
	t.Setenv("SYNON_RUNNER_CHAT_SYSTEM_PROMPT", "env system prompt")
	t.Setenv("SYNON_RUNNER_CHAT_TOOLS", "task_create, runtime_set")
	t.Setenv("SYNON_RUNNER_CHAT_TIMEOUT_SECONDS", "30")
	t.Setenv("SYNON_RUNNER_CHAT_TOOL_ROUND_LIMIT", "4")
	t.Setenv("SYNON_RUNNER_CHAT_TOOL_CALL_BATCH_LIMIT", "7")
	t.Setenv("SYNON_RUNNER_CHAT_MAX_ATTEMPTS", "2")
	t.Setenv("SYNON_RUNNER_CHAT_WORKERS", "5")
	t.Setenv("SYNON_RUNNER_COMMAND_TIMEOUT_SECONDS", "90")
	t.Setenv("SYNON_RUNNER_POLL_INTERVAL_MS", "1250")
	t.Setenv("SYNON_RUNNER_LEASE_TTL_SECONDS", "180")
	t.Setenv("SYNON_RUNNER_REPLAY_LIMIT", "50")
	t.Setenv("SYNON_RUNNER_OUTPUT_LIMIT_BYTES", "4096")
	t.Setenv("SYNON_COMPACT_SUMMARIZER_ENDPOINT", "https://env.compact.example/v1/chat/completions")
	t.Setenv("SYNON_COMPACT_SUMMARIZER_API_KEY", "env-compact-key")
	t.Setenv("SYNON_COMPACT_SUMMARIZER_MODEL", "env-compact-model")
	t.Setenv("SYNON_COMPACT_SUMMARIZER_TIMEOUT_SECONDS", "25")
	t.Setenv("SYNON_COMPACT_SUMMARIZER_MAX_ATTEMPTS", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HomeDir != filepath.Join(root, "state") {
		t.Fatalf("HomeDir = %q", cfg.HomeDir)
	}
	if cfg.Address() != "127.0.0.1:18080" {
		t.Fatalf("Address() = %q", cfg.Address())
	}
	if cfg.SynonLinkPackage != "assets/synon-link/synon-link-extension-v0.6.10.zip" {
		t.Fatalf("SynonLinkPackage = %q", cfg.SynonLinkPackage)
	}
	if cfg.SynonLinkAuth.Username != "env-link-user" || cfg.SynonLinkAuth.Password != "env-link-password" || cfg.SynonLinkAuth.SessionTTLMinutes != 240 {
		t.Fatalf("SynonLinkAuth = %#v", cfg.SynonLinkAuth)
	}
	if cfg.WebAuth.PublicBaseURL != "https://biomed.example" ||
		cfg.WebAuth.Google.ClientID != "google-client-id" ||
		cfg.WebAuth.Google.ClientSecret != "google-client-secret" ||
		cfg.WebAuth.Apple.ClientID != "com.synon.biomed.web" ||
		cfg.WebAuth.Apple.TeamID != "apple-team-id" ||
		cfg.WebAuth.Apple.KeyID != "apple-key-id" ||
		cfg.WebAuth.Apple.PrivateKey != "apple-private-key" ||
		cfg.WebAuth.WeChat.AppID != "wechat-app-id" ||
		cfg.WebAuth.WeChat.AppSecret != "wechat-app-secret" {
		t.Fatalf("WebAuth = %#v", cfg.WebAuth)
	}
	serializedWebAuth, err := json.Marshal(cfg.WebAuth)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serializedWebAuth), "google-client-secret") ||
		strings.Contains(string(serializedWebAuth), "apple-private-key") ||
		strings.Contains(string(serializedWebAuth), "wechat-app-secret") ||
		strings.Contains(string(serializedWebAuth), "client_secret") ||
		strings.Contains(string(serializedWebAuth), "app_secret") {
		t.Fatalf("serialized WebAuth leaked the provider secret: %s", serializedWebAuth)
	}
	if len(cfg.PluginDirectories) != 2 || cfg.PluginDirectories[0] != "env-plugin-a" || cfg.PluginDirectories[1] != "env-plugin-b" {
		t.Fatalf("PluginDirectories = %#v", cfg.PluginDirectories)
	}
	if len(cfg.SkillDirectories) != 2 || cfg.SkillDirectories[0] != "env-skill-a" || cfg.SkillDirectories[1] != "env-skill-b" {
		t.Fatalf("SkillDirectories = %#v", cfg.SkillDirectories)
	}
	if len(cfg.EnabledAdapters) != 2 || cfg.EnabledAdapters[0] != "feishu" || cfg.EnabledAdapters[1] != "wechat" {
		t.Fatalf("EnabledAdapters = %#v", cfg.EnabledAdapters)
	}
	if cfg.WeChat.AccountID != "env-account" {
		t.Fatalf("WeChat.AccountID = %q", cfg.WeChat.AccountID)
	}
	if cfg.WeChat.BotToken != "env-wechat-token" {
		t.Fatalf("WeChat.BotToken = %q", cfg.WeChat.BotToken)
	}
	if cfg.WeChat.BaseURL != "https://env.wechat.example" {
		t.Fatalf("WeChat.BaseURL = %q", cfg.WeChat.BaseURL)
	}
	if cfg.WeChat.UserID != "env-user" {
		t.Fatalf("WeChat.UserID = %q", cfg.WeChat.UserID)
	}
	if cfg.WeChat.PollIntervalMS != 250 {
		t.Fatalf("WeChat.PollIntervalMS = %d", cfg.WeChat.PollIntervalMS)
	}
	if cfg.Feishu.AppID != "env-feishu-app" {
		t.Fatalf("Feishu.AppID = %q", cfg.Feishu.AppID)
	}
	if cfg.Feishu.AppSecret != "env-feishu-secret" {
		t.Fatalf("Feishu.AppSecret = %q", cfg.Feishu.AppSecret)
	}
	if cfg.Feishu.VerificationToken != "env-feishu-token" {
		t.Fatalf("Feishu.VerificationToken = %q", cfg.Feishu.VerificationToken)
	}
	if cfg.Feishu.EncryptKey != "env-feishu-encrypt" {
		t.Fatalf("Feishu.EncryptKey = %q", cfg.Feishu.EncryptKey)
	}
	if cfg.Feishu.Domain != "https://env.feishu.example" {
		t.Fatalf("Feishu.Domain = %q", cfg.Feishu.Domain)
	}
	if cfg.LiveIMSmoke.Message != "file live smoke" || cfg.LiveIMSmoke.TimeoutSeconds != 45 || cfg.LiveIMSmoke.RuntimeURL != "http://127.0.0.1:18080" || !cfg.LiveIMSmoke.RequireAll {
		t.Fatalf("LiveIMSmoke base config = %+v", cfg.LiveIMSmoke)
	}
	if cfg.LiveIMSmoke.WeChatUserID != "wx-live" || cfg.LiveIMSmoke.WeChatContextToken != "wx-context" || cfg.LiveIMSmoke.FeishuChatID != "oc_live" || cfg.LiveIMSmoke.FeishuOpenID != "ou_live" {
		t.Fatalf("LiveIMSmoke targets = %+v", cfg.LiveIMSmoke)
	}
	if !cfg.Runner.Enabled || cfg.Runner.ID != "env-runner" || cfg.Runner.Provider != "openai_chat" || cfg.Runner.Command != "env-runner-command" {
		t.Fatalf("Runner identity = %+v", cfg.Runner)
	}
	if len(cfg.Runner.Args) != 2 || cfg.Runner.Args[0] != "--env" || cfg.Runner.Args[1] != "1" {
		t.Fatalf("Runner.Args = %#v", cfg.Runner.Args)
	}
	if cfg.Runner.ChatEndpoint != "https://env.model.example/v1/chat/completions" || cfg.Runner.ChatAPIKey != "env-model-key" || cfg.Runner.ChatModel != "env-model" || cfg.Runner.ChatSystemPrompt != "env system prompt" {
		t.Fatalf("Runner chat config = %+v", cfg.Runner)
	}
	if len(cfg.Runner.ChatTools) != 2 || cfg.Runner.ChatTools[0] != "task_create" || cfg.Runner.ChatTools[1] != "runtime_set" {
		t.Fatalf("Runner.ChatTools = %#v", cfg.Runner.ChatTools)
	}
	if cfg.Runner.ChatTimeoutSeconds != 30 {
		t.Fatalf("Runner.ChatTimeoutSeconds = %d", cfg.Runner.ChatTimeoutSeconds)
	}
	if cfg.Runner.ChatToolRoundLimit != 4 {
		t.Fatalf("Runner.ChatToolRoundLimit = %d", cfg.Runner.ChatToolRoundLimit)
	}
	if cfg.Runner.ChatToolCallBatchLimit != 7 {
		t.Fatalf("Runner.ChatToolCallBatchLimit = %d", cfg.Runner.ChatToolCallBatchLimit)
	}
	if cfg.Runner.ChatMaxAttempts != 2 {
		t.Fatalf("Runner.ChatMaxAttempts = %d", cfg.Runner.ChatMaxAttempts)
	}
	if cfg.Runner.ChatWorkers != 5 {
		t.Fatalf("Runner.ChatWorkers = %d", cfg.Runner.ChatWorkers)
	}
	if cfg.Runner.CommandTimeoutSeconds != 90 {
		t.Fatalf("Runner.CommandTimeoutSeconds = %d", cfg.Runner.CommandTimeoutSeconds)
	}
	if cfg.Runner.PollIntervalMS != 1250 || cfg.Runner.LeaseTTLSeconds != 180 || cfg.Runner.ReplayLimit != 50 || cfg.Runner.OutputLimitBytes != 4096 {
		t.Fatalf("Runner limits = %+v", cfg.Runner)
	}
	if cfg.CompactSummarizer.Endpoint != "https://env.compact.example/v1/chat/completions" ||
		cfg.CompactSummarizer.APIKey != "env-compact-key" ||
		cfg.CompactSummarizer.Model != "env-compact-model" ||
		cfg.CompactSummarizer.TimeoutSeconds != 25 ||
		cfg.CompactSummarizer.MaxAttempts != 3 {
		t.Fatalf("CompactSummarizer = %+v", cfg.CompactSummarizer)
	}
}

func TestLoadAppliesAddressEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_ADDRESS", "0.0.0.0:38123")
	t.Setenv("SYNON_LINK_AUTH_PASSWORD", "remote-test-password")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Host != "0.0.0.0" || cfg.Port != 38123 || cfg.Address() != "0.0.0.0:38123" {
		t.Fatalf("address config = host:%q port:%d addr:%q", cfg.Host, cfg.Port, cfg.Address())
	}
}

func TestP2LoadRejectsUnauthenticatedNonLoopbackBinding(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_ADDRESS", "0.0.0.0:38123")
	t.Setenv("SYNON_LINK_AUTH_PASSWORD", "")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "requires SYNON_LINK_AUTH_PASSWORD") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadAllowsNonLoopbackBindingWithGoogleOIDCAndHTTPSOrigin(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_ADDRESS", "0.0.0.0:38123")
	t.Setenv("SYNON_LINK_AUTH_PASSWORD", "")
	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "https://biomed.example")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "google-client-id")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "google-client-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WebAuth.PublicBaseURL != "https://biomed.example" ||
		cfg.WebAuth.Google.ClientID != "google-client-id" {
		t.Fatalf("WebAuth = %#v", cfg.WebAuth)
	}
}

func TestLoadRejectsInsecureOrPartialGoogleOIDCConfiguration(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "http://biomed.example")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "google-client-id")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "HTTPS origin") {
		t.Fatalf("insecure public origin error = %v", err)
	}

	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "secret-without-client")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "requires SYNON_AUTH_GOOGLE_CLIENT_ID") {
		t.Fatalf("partial Google config error = %v", err)
	}

	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "https://biomed.example")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "google-client-id")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "requires SYNON_AUTH_GOOGLE_CLIENT_SECRET") {
		t.Fatalf("remote public client error = %v", err)
	}
}

func TestLoadAllowsNonLoopbackBindingWithWeChatOAuthAndHTTPSOrigin(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_ADDRESS", "0.0.0.0:38123")
	t.Setenv("SYNON_LINK_AUTH_PASSWORD", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "")
	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "https://biomed.example")
	t.Setenv("SYNON_AUTH_WECHAT_APP_ID", "wechat-app-id")
	t.Setenv("SYNON_AUTH_WECHAT_APP_SECRET", "wechat-app-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WebAuth.PublicBaseURL != "https://biomed.example" ||
		cfg.WebAuth.WeChat.AppID != "wechat-app-id" {
		t.Fatalf("WebAuth = %#v", cfg.WebAuth)
	}
}

func TestLoadAllowsNonLoopbackBindingWithAppleSignIn(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_ADDRESS", "0.0.0.0:38123")
	t.Setenv("SYNON_LINK_AUTH_PASSWORD", "")
	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "https://biomed.example")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "")
	t.Setenv("SYNON_AUTH_WECHAT_APP_ID", "")
	t.Setenv("SYNON_AUTH_WECHAT_APP_SECRET", "")
	t.Setenv("SYNON_AUTH_APPLE_CLIENT_ID", "com.synon.biomed.web")
	t.Setenv("SYNON_AUTH_APPLE_TEAM_ID", "apple-team-id")
	t.Setenv("SYNON_AUTH_APPLE_KEY_ID", "apple-key-id")
	t.Setenv("SYNON_AUTH_APPLE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString([]byte("apple-private-key")))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WebAuth.Apple.ClientID != "com.synon.biomed.web" || cfg.WebAuth.Apple.TeamID != "apple-team-id" {
		t.Fatalf("Apple config = %#v", cfg.WebAuth.Apple)
	}
}

func TestLoadAppliesApplePrivateKeyFile(t *testing.T) {
	root := t.TempDir()
	keyFile := filepath.Join(root, "apple-key.p8")
	if err := os.WriteFile(keyFile, []byte("apple-private-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "https://biomed.example")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "")
	t.Setenv("SYNON_AUTH_WECHAT_APP_ID", "")
	t.Setenv("SYNON_AUTH_WECHAT_APP_SECRET", "")
	t.Setenv("SYNON_AUTH_APPLE_CLIENT_ID", "com.synon.biomed.web")
	t.Setenv("SYNON_AUTH_APPLE_TEAM_ID", "apple-team-id")
	t.Setenv("SYNON_AUTH_APPLE_KEY_ID", "apple-key-id")
	t.Setenv("SYNON_AUTH_APPLE_PRIVATE_KEY_FILE", keyFile)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WebAuth.Apple.PrivateKey != "apple-private-key" {
		t.Fatalf("Apple private key = %q", cfg.WebAuth.Apple.PrivateKey)
	}
}

func TestLoadRejectsMultipleApplePrivateKeySources(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_AUTH_APPLE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString([]byte("apple-private-key")))
	t.Setenv("SYNON_AUTH_APPLE_PRIVATE_KEY_FILE", filepath.Join(t.TempDir(), "apple-key.p8"))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "configure only one") {
		t.Fatalf("multiple Apple key sources error = %v", err)
	}
}

func TestLoadRejectsPartialAppleSignInConfiguration(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "https://biomed.example")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "")
	t.Setenv("SYNON_AUTH_WECHAT_APP_ID", "")
	t.Setenv("SYNON_AUTH_WECHAT_APP_SECRET", "")
	t.Setenv("SYNON_AUTH_APPLE_CLIENT_ID", "com.synon.biomed.web")
	t.Setenv("SYNON_AUTH_APPLE_TEAM_ID", "apple-team-id")
	t.Setenv("SYNON_AUTH_APPLE_KEY_ID", "")
	t.Setenv("SYNON_AUTH_APPLE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString([]byte("apple-private-key")))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "Apple Sign in with Apple requires") {
		t.Fatalf("partial Apple config error = %v", err)
	}
}

func TestLoadRejectsPartialWeChatOAuthConfiguration(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_ID", "")
	t.Setenv("SYNON_AUTH_GOOGLE_CLIENT_SECRET", "")
	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "https://biomed.example")
	t.Setenv("SYNON_AUTH_WECHAT_APP_ID", "wechat-app-id")
	t.Setenv("SYNON_AUTH_WECHAT_APP_SECRET", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "requires both") {
		t.Fatalf("partial WeChat config error = %v", err)
	}

	t.Setenv("SYNON_AUTH_PUBLIC_BASE_URL", "")
	t.Setenv("SYNON_AUTH_WECHAT_APP_SECRET", "wechat-app-secret")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "requires SYNON_AUTH_PUBLIC_BASE_URL") {
		t.Fatalf("missing public origin error = %v", err)
	}
}

func TestLoadAppliesHostPortEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_HOST", "127.0.0.2")
	t.Setenv("SYNON_PORT", "38124")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Host != "127.0.0.2" || cfg.Port != 38124 || cfg.Address() != "127.0.0.2:38124" {
		t.Fatalf("host/port config = host:%q port:%d addr:%q", cfg.Host, cfg.Port, cfg.Address())
	}
}

func TestLoadAppliesWebRootEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_WEB_ROOT", "/opt/synon-go/web")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebRoot != "/opt/synon-go/web" {
		t.Fatalf("WebRoot = %q", cfg.WebRoot)
	}
}

func TestLoadAppliesWeChatEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("WECHAT_ACCOUNT_ID", "env-only-account")
	t.Setenv("WECHAT_BOT_TOKEN", "env-only-token")
	t.Setenv("WECHAT_BASE_URL", "https://env-only.wechat.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WeChat.AccountID != "env-only-account" {
		t.Fatalf("WeChat.AccountID = %q", cfg.WeChat.AccountID)
	}
	if cfg.WeChat.BotToken != "env-only-token" {
		t.Fatalf("WeChat.BotToken = %q", cfg.WeChat.BotToken)
	}
	if cfg.WeChat.BaseURL != "https://env-only.wechat.example" {
		t.Fatalf("WeChat.BaseURL = %q", cfg.WeChat.BaseURL)
	}
}

func TestLoadAppliesFeishuEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("FEISHU_APP_ID", "env-only-feishu-app")
	t.Setenv("FEISHU_APP_SECRET", "env-only-feishu-secret")
	t.Setenv("FEISHU_VERIFICATION_TOKEN", "env-only-feishu-token")
	t.Setenv("FEISHU_ENCRYPT_KEY", "env-only-feishu-encrypt")
	t.Setenv("FEISHU_DOMAIN", "https://env-only.feishu.example")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Feishu.AppID != "env-only-feishu-app" {
		t.Fatalf("Feishu.AppID = %q", cfg.Feishu.AppID)
	}
	if cfg.Feishu.AppSecret != "env-only-feishu-secret" {
		t.Fatalf("Feishu.AppSecret = %q", cfg.Feishu.AppSecret)
	}
	if cfg.Feishu.VerificationToken != "env-only-feishu-token" {
		t.Fatalf("Feishu.VerificationToken = %q", cfg.Feishu.VerificationToken)
	}
	if cfg.Feishu.EncryptKey != "env-only-feishu-encrypt" {
		t.Fatalf("Feishu.EncryptKey = %q", cfg.Feishu.EncryptKey)
	}
	if cfg.Feishu.Domain != "https://env-only.feishu.example" {
		t.Fatalf("Feishu.Domain = %q", cfg.Feishu.Domain)
	}
}

func TestLoadAppliesFeedbackEnvironmentWithoutConfigFile(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("OPERON_SERVICE_URL", " https://feedback.example/base ")
	t.Setenv("SYNON_FEEDBACK_TOKEN", " feedback-token ")
	t.Setenv("SYNON_FEEDBACK_BETA", " beta-a ")
	t.Setenv("SYNON_FEEDBACK_DISABLED", "true")
	t.Setenv("SYNON_DISABLE_TELEMETRY", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Feedback.ServiceURL != "https://feedback.example/base" ||
		cfg.Feedback.Token != "feedback-token" || cfg.Feedback.Beta != "beta-a" ||
		!cfg.Feedback.Disabled || !cfg.DisableTelemetry {
		t.Fatalf("Feedback = %+v", cfg.Feedback)
	}
}

func TestLoadRejectsInvalidFeedbackDisabledEnvironment(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_FEEDBACK_DISABLED", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted invalid SYNON_FEEDBACK_DISABLED")
	}
}

func TestLoadRejectsUnsupportedEnabledAdapter(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_ENABLED_ADAPTERS", "feishu, unknown-channel")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "unsupported enabled adapter: unknown-channel") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsRetiredTelegramAndDingTalkAdapters(t *testing.T) {
	for _, adapter := range []string{"telegram", "dingtalk"} {
		t.Run(adapter, func(t *testing.T) {
			t.Setenv("SYNON_CONFIG", "")
			t.Setenv("SYNON_ENABLED_ADAPTERS", adapter)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "unsupported enabled adapter: "+adapter) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestLoadAcceptsUnlimitedRunnerToolRoundsFromRealConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "synon.json")
	if err := os.WriteFile(configPath, []byte(`{
  "runner": {
    "enabled": true,
    "provider": "workspace",
    "chat_tool_round_limit": 0
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_CONFIG", configPath)
	t.Setenv("SYNON_RUNNER_CHAT_TOOL_ROUND_LIMIT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected unlimited runner config: %v", err)
	}
	if cfg.Runner.ChatToolRoundLimit != 0 {
		t.Fatalf("Runner.ChatToolRoundLimit = %d, want 0 (unlimited)", cfg.Runner.ChatToolRoundLimit)
	}
}

func TestLoadAcceptsUnlimitedRunnerToolRoundsFromEnvironment(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_RUNNER_CHAT_TOOL_ROUND_LIMIT", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected unlimited runner environment override: %v", err)
	}
	if cfg.Runner.ChatToolRoundLimit != 0 {
		t.Fatalf("Runner.ChatToolRoundLimit = %d, want 0 (unlimited)", cfg.Runner.ChatToolRoundLimit)
	}
}

func TestLoadAcceptsUnlimitedRunnerToolCallBatchFromEnvironment(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_RUNNER_CHAT_TOOL_CALL_BATCH_LIMIT", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() rejected unlimited runner tool-call batch: %v", err)
	}
	if cfg.Runner.ChatToolCallBatchLimit != 0 {
		t.Fatalf("Runner.ChatToolCallBatchLimit = %d, want 0 (unlimited)", cfg.Runner.ChatToolCallBatchLimit)
	}
}

func TestLoadRejectsNegativeRunnerToolCallBatchLimit(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_RUNNER_CHAT_TOOL_CALL_BATCH_LIMIT", "-1")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "SYNON_RUNNER_CHAT_TOOL_CALL_BATCH_LIMIT") {
		t.Fatalf("Load() error = %v, want invalid negative tool-call batch limit", err)
	}
}

func TestLoadRejectsNegativeRunnerToolRoundLimit(t *testing.T) {
	t.Setenv("SYNON_CONFIG", "")
	t.Setenv("SYNON_RUNNER_CHAT_TOOL_ROUND_LIMIT", "-1")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "SYNON_RUNNER_CHAT_TOOL_ROUND_LIMIT") {
		t.Fatalf("Load() error = %v, want invalid negative tool-round limit", err)
	}
}

func TestLoadValidatesRunnerReplayLimitBoundary(t *testing.T) {
	tests := []struct {
		name    string
		limit   int64
		wantErr bool
	}{
		{name: "zero", limit: 0, wantErr: true},
		{name: "maximum", limit: transcript.MaxRunnerReplayProjection},
		{name: "above maximum", limit: transcript.MaxRunnerReplayProjection + 1, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SYNON_CONFIG", "")
			t.Setenv("SYNON_RUNNER_REPLAY_LIMIT", strconv.FormatInt(test.limit, 10))

			cfg, err := Load()
			if test.wantErr {
				if err == nil {
					t.Fatalf("Load() accepted replay limit %d", test.limit)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() rejected replay limit %d: %v", test.limit, err)
			}
			if cfg.Runner.ReplayLimit != test.limit {
				t.Fatalf("Runner.ReplayLimit = %d, want %d", cfg.Runner.ReplayLimit, test.limit)
			}
		})
	}
}
