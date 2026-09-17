package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"synon-go/internal/config"
	"synon-go/internal/server"
)

func TestBrowserLoginURLUsesReachableLoopbackForWildcardBinding(t *testing.T) {
	if got := browserLoginURL(config.Config{Host: "0.0.0.0", Port: 8765}); got != "http://127.0.0.1:8765/#/login" {
		t.Fatalf("browserLoginURL() = %q", got)
	}
	if got := browserLoginURL(config.Config{Host: "127.0.0.2", Port: 38766}); got != "http://127.0.0.2:38766/#/login" {
		t.Fatalf("browserLoginURL() = %q", got)
	}
}

func TestApplyRuntimeMemoryConfigUsesLoadedConfiguration(t *testing.T) {
	cfg := config.Config{}
	cfg.Memory.Enabled = true
	cfg.Memory.PIClassifierEnabled = false
	cfg.Memory.SearchToolMax = 7
	cfg.Memory.ReadToolMax = 11

	options := applyRuntimeMemoryConfig(server.Options{}, cfg)
	if options.MemoryConfig == nil {
		t.Fatal("MemoryConfig is nil")
	}
	if !options.MemoryConfig.Enabled || options.MemoryConfig.PIClassifierEnabled ||
		options.MemoryConfig.SearchToolMax != 7 || options.MemoryConfig.ReadToolMax != 11 {
		t.Fatalf("MemoryConfig = %+v", *options.MemoryConfig)
	}

	cfg.Memory.SearchToolMax = 19
	if options.MemoryConfig.SearchToolMax != 7 {
		t.Fatalf("runtime MemoryConfig aliases mutable startup config: %+v", *options.MemoryConfig)
	}
}

func TestRunStressHTTPExercisesRealServerConcurrently(t *testing.T) {
	app := newSynonTestServer(t, server.Options{FileRoot: t.TempDir()})
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()

	var out bytes.Buffer
	err := runStressHTTP([]string{
		"--base-url", httpServer.URL,
		"--requests", "80",
		"--concurrency", "8",
		"--timeout", "5s",
	}, &out)
	if err != nil {
		t.Fatalf("runStressHTTP() error = %v output=%s", err, out.String())
	}
	raw := bytes.TrimSpace(out.Bytes())
	prefix := []byte("STRESS_HTTP=")
	if !bytes.HasPrefix(raw, prefix) {
		t.Fatalf("stress output missing prefix: %s", raw)
	}
	var stats stressStats
	if err := json.Unmarshal(raw[len(prefix):], &stats); err != nil {
		t.Fatalf("decode stress stats: %v output=%s", err, raw)
	}
	if stats.Failures != 0 || stats.HTTPRequests < int64(stats.Requests) || stats.Concurrency != 8 {
		t.Fatalf("stress stats invalid: %#v", stats)
	}
	if stats.Workloads["runtime_kv_cycle"] == 0 || stats.StatusBuckets["200"] == 0 {
		t.Fatalf("stress stats missing real workload evidence: %#v", stats)
	}
}

func TestNewSessionRunnerCommandOptionsUsesExplicitRunnerConfig(t *testing.T) {
	options, enabled, err := newSessionRunnerCommandOptions(config.Config{
		Runner: config.RunnerConfig{
			Enabled:               true,
			Provider:              "command",
			ID:                    "runner-main",
			Command:               "worker-bin",
			Args:                  []string{"--mode", "agent"},
			PollIntervalMS:        1500,
			LeaseTTLSeconds:       240,
			CommandTimeoutSeconds: 120,
			ReplayLimit:           80,
			OutputLimitBytes:      8192,
		},
	})
	if err != nil {
		t.Fatalf("newSessionRunnerCommandOptions() error = %v", err)
	}
	if !enabled {
		t.Fatal("runner options should be enabled")
	}
	if options.RunnerID != "runner-main" || options.Command != "worker-bin" {
		t.Fatalf("runner identity = %+v", options)
	}
	if len(options.Args) != 2 || options.Args[0] != "--mode" || options.Args[1] != "agent" {
		t.Fatalf("runner args = %#v", options.Args)
	}
	if options.CommandTimeout != 120*time.Second || options.PollInterval != 1500*time.Millisecond || options.LeaseTTL != 240*time.Second || options.ReplayLimit != 80 || options.OutputLimitBytes != 8192 {
		t.Fatalf("runner limits = %+v", options)
	}
}

func TestNewSessionRunnerChatOptionsUsesExplicitRunnerConfig(t *testing.T) {
	options, enabled, err := newSessionRunnerChatOptions(config.Config{
		Runner: config.RunnerConfig{
			Enabled:                true,
			Provider:               "openai_chat",
			ID:                     "runner-chat",
			ChatEndpoint:           "https://model.example/v1/chat/completions",
			ChatAPIKey:             "model-key",
			ChatModel:              "model-main",
			ChatSystemPrompt:       "system prompt",
			ChatTools:              []string{"task_create", "runtime_get"},
			ChatTimeoutSeconds:     45,
			ChatToolRoundLimit:     5,
			ChatToolCallBatchLimit: 7,
			ChatMaxAttempts:        3,
			PollIntervalMS:         1500,
			LeaseTTLSeconds:        240,
			ReplayLimit:            80,
			OutputLimitBytes:       8192,
		},
	})
	if err != nil {
		t.Fatalf("newSessionRunnerChatOptions() error = %v", err)
	}
	if !enabled {
		t.Fatal("runner options should be enabled")
	}
	if options.RunnerID != "runner-chat" || options.Endpoint != "https://model.example/v1/chat/completions" || options.APIKey != "model-key" || options.Model != "model-main" || options.SystemPrompt != "system prompt" {
		t.Fatalf("runner chat options = %+v", options)
	}
	if len(options.AllowedTools) != 2 || options.AllowedTools[0] != "task_create" || options.AllowedTools[1] != "runtime_get" {
		t.Fatalf("runner allowed tools = %#v", options.AllowedTools)
	}
	if options.RequestTimeout != 45*time.Second {
		t.Fatalf("runner request timeout = %s", options.RequestTimeout)
	}
	if options.MaxToolRounds != 5 {
		t.Fatalf("runner max tool rounds = %d", options.MaxToolRounds)
	}
	if options.MaxToolCallsPerRound != 7 {
		t.Fatalf("runner max tool calls per round = %d", options.MaxToolCallsPerRound)
	}
	if options.MaxAttempts != 3 {
		t.Fatalf("runner max attempts = %d", options.MaxAttempts)
	}
	if options.PollInterval != 1500*time.Millisecond || options.LeaseTTL != 240*time.Second || options.ReplayLimit != 80 || options.OutputLimitBytes != 8192 {
		t.Fatalf("runner limits = %+v", options)
	}
}

func TestSessionRunnerChatWorkerCountUsesExplicitOrMinimumSchedulerCapacity(t *testing.T) {
	if got := sessionRunnerChatWorkerCount(config.RunnerConfig{ChatWorkers: 7}); got != 7 {
		t.Fatalf("explicit worker count = %d, want 7", got)
	}
	want := runtime.GOMAXPROCS(0)
	if want < minimumSessionRunnerChatWorkers {
		want = minimumSessionRunnerChatWorkers
	}
	if got := sessionRunnerChatWorkerCount(config.RunnerConfig{}); got != want {
		t.Fatalf("automatic worker count = %d, want at least %d", got, want)
	}
}

func TestFrameResumeDispatchWorkerCountKeepsRecoveryQueueConcurrentAndBounded(t *testing.T) {
	if got := frameResumeDispatchWorkerCount(0); got != 2 {
		t.Fatalf("zero worker count = %d, want minimum 2", got)
	}
	if got := frameResumeDispatchWorkerCount(1); got != 2 {
		t.Fatalf("single worker count = %d, want minimum 2", got)
	}
	if got := frameResumeDispatchWorkerCount(3); got != 3 {
		t.Fatalf("explicit worker count = %d, want 3", got)
	}
	if got := frameResumeDispatchWorkerCount(12); got != maxFrameResumeDispatchWorkers {
		t.Fatalf("large worker count = %d, want bounded %d", got, maxFrameResumeDispatchWorkers)
	}
}

func TestSourceBackendStartupAcceptsUnlimitedChatToolRounds(t *testing.T) {
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

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	options, enabled, err := newSessionRunnerChatOptions(cfg)
	if err != nil {
		t.Fatalf("newSessionRunnerChatOptions() rejected unlimited tool rounds: %v", err)
	}
	if !enabled {
		t.Fatal("workspace runner should be enabled")
	}
	if options.MaxToolRounds != 0 {
		t.Fatalf("MaxToolRounds = %d, want 0 (unlimited)", options.MaxToolRounds)
	}
	diagnostics := newRunnerDiagnostics(cfg)
	if diagnostics.ChatToolRoundLimit != 0 {
		t.Fatalf("diagnostics ChatToolRoundLimit = %d, want 0 (unlimited)", diagnostics.ChatToolRoundLimit)
	}
}

func TestNewCompactSummarizerOptionsUsesExplicitCompactConfig(t *testing.T) {
	options := newCompactSummarizerOptions(config.Config{
		Runner: config.RunnerConfig{
			ID:                 "runner-main",
			ChatEndpoint:       "https://runner.example/v1/chat/completions",
			ChatAPIKey:         "runner-key",
			ChatModel:          "runner-model",
			ChatTimeoutSeconds: 60,
			ChatMaxAttempts:    2,
		},
		CompactSummarizer: config.CompactSummarizerConfig{
			Endpoint:       "https://compact.example/v1/chat/completions",
			APIKey:         "compact-key",
			Model:          "compact-model",
			TimeoutSeconds: 30,
			MaxAttempts:    4,
		},
	})
	if options.RunnerID != "synon-go-compact" ||
		options.Endpoint != "https://compact.example/v1/chat/completions" ||
		options.APIKey != "compact-key" ||
		options.Model != "compact-model" ||
		options.RequestTimeout != 30*time.Second ||
		options.MaxAttempts != 4 {
		t.Fatalf("compact summarizer options = %+v", options)
	}
}

func TestNewCompactSummarizerOptionsFallsBackToRunnerChatConfig(t *testing.T) {
	options := newCompactSummarizerOptions(config.Config{
		Runner: config.RunnerConfig{
			ID:                 "runner-main",
			ChatEndpoint:       "https://runner.example/v1/chat/completions",
			ChatAPIKey:         "runner-key",
			ChatModel:          "runner-model",
			ChatTimeoutSeconds: 60,
			ChatMaxAttempts:    2,
		},
	})
	if options.RunnerID != "runner-main" ||
		options.Endpoint != "https://runner.example/v1/chat/completions" ||
		options.APIKey != "runner-key" ||
		options.Model != "runner-model" ||
		options.RequestTimeout != 60*time.Second ||
		options.MaxAttempts != 2 {
		t.Fatalf("fallback compact summarizer options = %+v", options)
	}
}

func TestNewRunnerDiagnosticsDefaultsToSavedWorkspaceAuthority(t *testing.T) {
	diagnostics := newRunnerDiagnostics(config.Config{})
	if !diagnostics.Enabled || diagnostics.Provider != server.WorkspaceSessionRunnerProvider || !diagnostics.RuntimeModelAuthority || diagnostics.ChatEndpoint != "" || diagnostics.ChatModel != "" {
		t.Fatalf("default runner diagnostics = %+v", diagnostics)
	}
	if diagnostics.ChatAPIKeySet || diagnostics.ChatAPIKeySource != "none" {
		t.Fatalf("workspace authority diagnostics must not expose or invent an API key: %+v", diagnostics)
	}
}
func TestNewRunnerDiagnosticsReportsReadinessWithoutLeakingAPIKey(t *testing.T) {
	t.Setenv("SYNON_RUNNER_CHAT_API_KEY", "env-secret")
	diagnostics := newRunnerDiagnostics(config.Config{
		Runner: config.RunnerConfig{
			Enabled:          true,
			Provider:         "openai_chat",
			ID:               "runner-diag",
			ChatEndpoint:     "https://model.example/v1/chat/completions",
			ChatAPIKey:       "env-secret",
			ChatModel:        "model-main",
			ChatTools:        []string{"Read", "ToolSearch"},
			PollIntervalMS:   1000,
			LeaseTTLSeconds:  300,
			ReplayLimit:      200,
			OutputLimitBytes: 1024,
		},
	})
	if !diagnostics.Enabled ||
		diagnostics.Provider != "openai_chat" ||
		diagnostics.RunnerID != "runner-diag" ||
		diagnostics.ChatEndpoint != "https://model.example/v1/chat/completions" ||
		diagnostics.ChatModel != "model-main" ||
		!diagnostics.ChatAPIKeySet ||
		diagnostics.ChatAPIKeySource != "env:SYNON_RUNNER_CHAT_API_KEY" ||
		len(diagnostics.ChatTools) != 2 {
		t.Fatalf("runner diagnostics = %+v", diagnostics)
	}
	if diagnostics.ChatAPIKeySource == "env-secret" {
		t.Fatalf("runner diagnostics leaked API key: %+v", diagnostics)
	}
}

func TestNewAdapterDiagnosticsReportsCredentialReadinessWithoutLeakingSecrets(t *testing.T) {
	diagnostics := newAdapterDiagnostics(config.Config{
		EnabledAdapters: []string{"feishu", "wechat"},
		Feishu: config.FeishuConfig{
			AppID:             "cli_app",
			AppSecret:         "feishu-secret",
			VerificationToken: "verify-token",
			Domain:            "https://feishu.example",
		},
		WeChat: config.WeChatConfig{
			AccountID: "wechat-account",
			BotToken:  "wechat-secret",
			BaseURL:   "https://wechat.example",
		},
	})
	feishu := adapterPlatformDiagnostics(t, diagnostics, "feishu")
	if !feishu.Enabled || !feishu.Configured || !feishu.InboundConfigured || !feishu.OutboundConfigured || feishu.Endpoint != "https://feishu.example" || !feishu.CredentialFields["app_id"] || !feishu.CredentialFields["app_secret"] {
		t.Fatalf("feishu adapter diagnostics = %+v", feishu)
	}
	wechat := adapterPlatformDiagnostics(t, diagnostics, "wechat")
	if !wechat.Enabled || !wechat.Configured || !wechat.InboundConfigured || !wechat.OutboundConfigured || wechat.Endpoint != "https://wechat.example" || !wechat.CredentialFields["bot_token"] {
		t.Fatalf("wechat adapter diagnostics = %+v", wechat)
	}
	raw, err := json.Marshal(diagnostics)
	if err != nil {
		t.Fatalf("marshal adapter diagnostics: %v", err)
	}
	if bytes.Contains(raw, []byte("feishu-secret")) || bytes.Contains(raw, []byte("wechat-secret")) || bytes.Contains(raw, []byte("verify-token")) {
		t.Fatalf("adapter diagnostics leaked secret material: %s", raw)
	}
}

func TestNewAdapterDiagnosticsRequiresWeChatAccountIDAndBotToken(t *testing.T) {
	diagnostics := newAdapterDiagnostics(config.Config{
		EnabledAdapters: []string{"wechat"},
		WeChat: config.WeChatConfig{
			BotToken: "wechat-secret",
		},
	})
	wechat := adapterPlatformDiagnostics(t, diagnostics, "wechat")
	if wechat.Configured {
		t.Fatalf("wechat should require account_id and bot_token before reporting configured: %#v", wechat)
	}
	if wechat.InboundConfigured || wechat.OutboundConfigured {
		t.Fatalf("wechat readiness should fail closed without account_id: %#v", wechat)
	}
	if !wechat.CredentialFields["bot_token"] || wechat.CredentialFields["account_id"] {
		t.Fatalf("wechat credential fields = %#v", wechat.CredentialFields)
	}

	diagnostics = newAdapterDiagnostics(config.Config{
		EnabledAdapters: []string{"wechat"},
		WeChat: config.WeChatConfig{
			AccountID: "wechat-account",
			BotToken:  "wechat-secret",
		},
	})
	wechat = adapterPlatformDiagnostics(t, diagnostics, "wechat")
	if !wechat.Configured || !wechat.InboundConfigured || !wechat.OutboundConfigured {
		t.Fatalf("wechat should be configured when account_id and bot_token are present: %#v", wechat)
	}
}

func TestNewSessionRunnerChatOptionsInfersProviderFromChatConfig(t *testing.T) {
	commandOptions, commandEnabled, err := newSessionRunnerCommandOptions(config.Config{
		Runner: config.RunnerConfig{
			Enabled:            true,
			ID:                 "runner-chat",
			ChatEndpoint:       "https://model.example/v1/chat/completions",
			ChatModel:          "model-main",
			ChatTimeoutSeconds: 120,
			ChatToolRoundLimit: 3,
			ChatMaxAttempts:    2,
			PollIntervalMS:     1000,
			LeaseTTLSeconds:    300,
			ReplayLimit:        200,
			OutputLimitBytes:   1024 * 1024,
		},
	})
	if err != nil {
		t.Fatalf("newSessionRunnerCommandOptions() error = %v", err)
	}
	if commandEnabled || commandOptions.Command != "" {
		t.Fatalf("command runner should stay disabled for inferred chat provider: enabled=%v options=%+v", commandEnabled, commandOptions)
	}

	chatOptions, chatEnabled, err := newSessionRunnerChatOptions(config.Config{
		Runner: config.RunnerConfig{
			Enabled:            true,
			ID:                 "runner-chat",
			ChatEndpoint:       "https://model.example/v1/chat/completions",
			ChatModel:          "model-main",
			ChatTimeoutSeconds: 120,
			ChatToolRoundLimit: 3,
			ChatMaxAttempts:    2,
			PollIntervalMS:     1000,
			LeaseTTLSeconds:    300,
			ReplayLimit:        200,
			OutputLimitBytes:   1024 * 1024,
		},
	})
	if err != nil {
		t.Fatalf("newSessionRunnerChatOptions() error = %v", err)
	}
	if !chatEnabled || chatOptions.Endpoint != "https://model.example/v1/chat/completions" || chatOptions.Model != "model-main" {
		t.Fatalf("chat runner = enabled:%v options:%+v", chatEnabled, chatOptions)
	}
}

func TestNewSessionRunnerCommandOptionsStaysDisabledUntilConfigured(t *testing.T) {
	options, enabled, err := newSessionRunnerCommandOptions(config.Config{})
	if err != nil {
		t.Fatalf("disabled runner error = %v", err)
	}
	if enabled || options.Command != "" {
		t.Fatalf("disabled runner = enabled:%v options:%+v", enabled, options)
	}

	_, enabled, err = newSessionRunnerCommandOptions(config.Config{
		Runner: config.RunnerConfig{Enabled: true},
	})
	if err != nil || enabled {
		t.Fatalf("enabled runner without command should select saved workspace model authority, command enabled=%v err=%v", enabled, err)
	}
	chatOptions, chatEnabled, err := newSessionRunnerChatOptions(config.Config{
		Runner: config.RunnerConfig{Enabled: true},
	})
	if err != nil || !chatEnabled || !chatOptions.RequireSavedModel || chatOptions.Endpoint != "" || chatOptions.Model != "" {
		t.Fatalf("enabled runner workspace authority = enabled:%v options:%+v err:%v", chatEnabled, chatOptions, err)
	}
}

func TestNewSessionRunnerChatOptionsRequiresSavedProviderByDefault(t *testing.T) {
	options, enabled, err := newSessionRunnerChatOptions(config.Config{})
	if err != nil {
		t.Fatalf("workspace authority runner error = %v", err)
	}
	if !enabled || !options.RequireSavedModel || options.Endpoint != "" || options.Model != "" {
		t.Fatalf("workspace authority runner = enabled:%v options:%+v", enabled, options)
	}

	builtin, builtinEnabled, err := newSessionRunnerChatOptions(config.Config{
		Runner: config.RunnerConfig{Provider: "go_builtin"},
	})
	if err != nil || !builtinEnabled || builtin.RequireSavedModel || builtin.Endpoint != server.BuiltinSessionRunnerChatEndpoint || builtin.Model != server.BuiltinSessionRunnerChatModel {
		t.Fatalf("explicit builtin runner = enabled:%v options:%+v err:%v", builtinEnabled, builtin, err)
	}

	_, enabled, err = newSessionRunnerChatOptions(config.Config{
		Runner: config.RunnerConfig{Enabled: true, Provider: "command", Command: "worker-bin"},
	})
	if err != nil || enabled {
		t.Fatalf("command runner should not enable chat options, enabled=%v err=%v", enabled, err)
	}

	_, enabled, err = newSessionRunnerChatOptions(config.Config{
		Runner: config.RunnerConfig{Enabled: true, Provider: "openai_chat", ChatModel: "model-main"},
	})
	if err == nil || enabled {
		t.Fatalf("chat runner without endpoint should fail, enabled=%v err=%v", enabled, err)
	}
}

func TestNewWeChatPollerCreatesTasksThroughServer(t *testing.T) {
	wechatAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/getupdates" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer TOKEN" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{
			"ret": 0,
			"get_updates_buf": "next",
			"msgs": [{
				"message_id": 61001,
				"seq": 8,
				"from_user_id": "wx-user",
				"create_time_ms": 1710000000000,
				"item_list": [{"type": 1, "text_item": {"text": "cli wechat polling task"}}]
			}]
		}`))
	}))
	defer wechatAPI.Close()

	app := newSynonTestServer(t, server.Options{FileRoot: t.TempDir()})
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	postToolForMainTest(t, httpServer.URL, "pairing_allow", map[string]any{
		"platform":    "wechat",
		"userId":      "wx-user",
		"displayName": "wx-user",
	})

	poller, err := newWeChatPoller(http.DefaultClient, config.Config{
		EnabledAdapters: []string{"wechat"},
		WeChat: config.WeChatConfig{
			BotToken:       "TOKEN",
			BaseURL:        wechatAPI.URL,
			PollIntervalMS: 1,
		},
	}, app)
	if err != nil {
		t.Fatalf("newWeChatPoller() error = %v", err)
	}
	if poller == nil {
		t.Fatal("newWeChatPoller() returned nil")
	}
	if _, err := poller.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	tasks := postToolForMainTest(t, httpServer.URL, "task_list", map[string]any{})
	list := tasks["result"].(map[string]any)["tasks"].([]any)
	if len(list) != 1 {
		t.Fatalf("tasks = %#v", list)
	}
	task := list[0].(map[string]any)
	if task["status"] != "open" || task["title"] != "WeChat: cli wechat polling task" {
		t.Fatalf("task = %#v", task)
	}
}

func TestNewWeChatPollerStaysDisabledWithoutAdapterOrToken(t *testing.T) {
	app := newSynonTestServer(t, server.Options{FileRoot: t.TempDir()})
	poller, err := newWeChatPoller(http.DefaultClient, config.Config{
		EnabledAdapters: []string{"feishu"},
		WeChat:          config.WeChatConfig{BotToken: "TOKEN"},
	}, app)
	if err != nil {
		t.Fatalf("disabled poller error = %v", err)
	}
	if poller != nil {
		t.Fatalf("disabled poller = %#v", poller)
	}

	poller, err = newWeChatPoller(http.DefaultClient, config.Config{
		EnabledAdapters: []string{"wechat"},
	}, app)
	if err != nil {
		t.Fatalf("missing token error = %v", err)
	}
	if poller != nil {
		t.Fatalf("missing token poller = %#v", poller)
	}
}

func TestNewFeishuStreamClientUsesOfficialSDKWhenEnabled(t *testing.T) {
	app := newSynonTestServer(t, server.Options{FileRoot: t.TempDir()})
	stream, err := newFeishuStreamClient(config.Config{
		EnabledAdapters: []string{"feishu"},
		Feishu: config.FeishuConfig{
			AppID:             "cli_app",
			AppSecret:         "app-secret",
			VerificationToken: "verification-token",
			EncryptKey:        "encrypt-key",
			Domain:            "https://open.feishu.example",
		},
	}, app)
	if err != nil {
		t.Fatalf("newFeishuStreamClient() error = %v", err)
	}
	if stream == nil {
		t.Fatal("newFeishuStreamClient() returned nil")
	}
	stream.Close()
}

func TestNewFeishuStreamClientStaysDisabledWithoutAdapterOrCredentials(t *testing.T) {
	app := newSynonTestServer(t, server.Options{FileRoot: t.TempDir()})
	stream, err := newFeishuStreamClient(config.Config{
		EnabledAdapters: []string{"wechat"},
		Feishu:          config.FeishuConfig{AppID: "cli_app", AppSecret: "app-secret"},
	}, app)
	if err != nil {
		t.Fatalf("disabled stream client error = %v", err)
	}
	if stream != nil {
		t.Fatalf("disabled stream client = %#v", stream)
	}

	stream, err = newFeishuStreamClient(config.Config{
		EnabledAdapters: []string{"feishu"},
		Feishu:          config.FeishuConfig{AppID: "cli_app"},
	}, app)
	if err != nil {
		t.Fatalf("missing secret error = %v", err)
	}
	if stream != nil {
		t.Fatalf("missing secret stream client = %#v", stream)
	}
}

func postToolForMainTest(t *testing.T, baseURL string, tool string, input map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(baseURL+"/api/tools/"+tool+"/execute", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST tool %s error = %v", tool, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode tool %s: %v", tool, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tool %s status = %d body = %#v", tool, resp.StatusCode, decoded)
	}
	return decoded
}

func adapterPlatformDiagnostics(t *testing.T, diagnostics server.AdapterDiagnostics, name string) server.AdapterPlatformDiagnostics {
	t.Helper()
	for _, platform := range diagnostics.Platforms {
		if platform.Name == name {
			return platform
		}
	}
	t.Fatalf("adapter platform %s missing from %+v", name, diagnostics)
	return server.AdapterPlatformDiagnostics{}
}
