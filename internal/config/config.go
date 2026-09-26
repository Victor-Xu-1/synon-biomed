package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"synon-go/internal/memoryconfig"
	"synon-go/internal/networkpolicy"
	"synon-go/internal/networktls"
	"synon-go/internal/persistence/transcript"
)

const defaultSynonLinkSessionTTLMinutes = 48 * 60

const webApplePrivateKeyMaxBytes = 16 * 1024

type Config struct {
	HomeDir            string                  `json:"home_dir"`
	DataDirSource      string                  `json:"-"`
	DataDirControlPath string                  `json:"-"`
	DefaultDataDir     string                  `json:"-"`
	CondaHome          string                  `json:"conda_home"`
	CondaEnvsPath      string                  `json:"conda_envs_path"`
	Network            NetworkPolicyConfig     `json:"network"`
	Host               string                  `json:"host"`
	Port               int                     `json:"port"`
	WebRoot            string                  `json:"web_root"`
	SynonLinkPackage   string                  `json:"synon_link_package"`
	SynonLinkAuth      SynonLinkAuthConfig     `json:"synon_link_auth"`
	WebAuth            WebAuthConfig           `json:"web_auth"`
	PluginDirectories  []string                `json:"plugin_directories"`
	SkillDirectories   []string                `json:"skill_directories"`
	EnabledAdapters    []string                `json:"enabled_adapters"`
	Feishu             FeishuConfig            `json:"feishu"`
	WeChat             WeChatConfig            `json:"wechat"`
	LiveIMSmoke        LiveIMSmokeConfig       `json:"live_im_smoke"`
	Runner             RunnerConfig            `json:"runner"`
	Memory             memoryconfig.Config     `json:"memory"`
	DisableTelemetry   bool                    `json:"disable_telemetry"`
	CompactSummarizer  CompactSummarizerConfig `json:"compact_summarizer"`
	Feedback           FeedbackConfig          `json:"feedback"`
}

type NetworkPolicyConfig struct {
	ResponseHeaderTimeoutSeconds int64    `json:"response_header_timeout_seconds,omitempty"`
	ReadIdleTimeoutSeconds       int64    `json:"read_idle_timeout_seconds,omitempty"`
	TransferIdleTimeoutSeconds   int64    `json:"transfer_idle_timeout_seconds,omitempty"`
	SearchTimeoutSeconds         int64    `json:"search_timeout_seconds,omitempty"`
	AllowedDomains               []string `json:"allowed_domains"`
	DeniedDomains                []string `json:"denied_domains"`
	Proxy                        string   `json:"proxy"`
	CABundle                     string   `json:"ca_bundle"`
	MCPX509Strict                string   `json:"mcp_x509_strict"`
}

type SynonLinkAuthConfig struct {
	Username          string `json:"username"`
	Password          string `json:"password"`
	SessionTTLMinutes int    `json:"session_ttl_minutes"`
}

type WebAuthConfig struct {
	PublicBaseURL string                 `json:"public_base_url"`
	Google        WebOIDCProviderConfig  `json:"google"`
	Apple         WebAppleProviderConfig `json:"apple"`
	WeChat        WebOAuthProviderConfig `json:"wechat"`
}

type WebOIDCProviderConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"-"`
}

type WebAppleProviderConfig struct {
	ClientID   string `json:"client_id"`
	TeamID     string `json:"team_id"`
	KeyID      string `json:"key_id"`
	PrivateKey string `json:"-"`
}

type WebOAuthProviderConfig struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"-"`
}

type FeishuConfig struct {
	AppID             string `json:"app_id"`
	AppSecret         string `json:"app_secret"`
	VerificationToken string `json:"verification_token"`
	EncryptKey        string `json:"encrypt_key"`
	Domain            string `json:"domain"`
}

type WeChatConfig struct {
	AccountID      string `json:"account_id"`
	BotToken       string `json:"bot_token"`
	BaseURL        string `json:"base_url"`
	UserID         string `json:"user_id"`
	PollIntervalMS int    `json:"poll_interval_ms"`
}

type LiveIMSmokeConfig struct {
	Message            string `json:"message"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
	RuntimeURL         string `json:"runtime_url"`
	RequireAll         bool   `json:"require_all"`
	WeChatUserID       string `json:"wechat_user_id"`
	WeChatContextToken string `json:"wechat_context_token"`
	FeishuChatID       string `json:"feishu_chat_id"`
	FeishuOpenID       string `json:"feishu_open_id"`
}

type RunnerConfig struct {
	Enabled                bool     `json:"enabled"`
	ID                     string   `json:"id"`
	Provider               string   `json:"provider"`
	Command                string   `json:"command"`
	Args                   []string `json:"args"`
	ChatEndpoint           string   `json:"chat_endpoint"`
	ChatAPIKey             string   `json:"chat_api_key"`
	ChatModel              string   `json:"chat_model"`
	ChatSystemPrompt       string   `json:"chat_system_prompt"`
	ChatTools              []string `json:"chat_tools"`
	ChatTimeoutSeconds     int      `json:"chat_timeout_seconds"`
	ChatToolRoundLimit     int      `json:"chat_tool_round_limit"`
	ChatToolCallBatchLimit int      `json:"chat_tool_call_batch_limit"`
	ChatMaxAttempts        int      `json:"chat_max_attempts"`
	ChatWorkers            int      `json:"chat_workers"`
	CommandTimeoutSeconds  int      `json:"command_timeout_seconds"`
	PollIntervalMS         int      `json:"poll_interval_ms"`
	LeaseTTLSeconds        int      `json:"lease_ttl_seconds"`
	ReplayLimit            int64    `json:"replay_limit"`
	OutputLimitBytes       int64    `json:"output_limit_bytes"`
}

type CompactSummarizerConfig struct {
	Endpoint       string `json:"endpoint"`
	APIKey         string `json:"api_key"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxAttempts    int    `json:"max_attempts"`
}

type FeedbackConfig struct {
	ServiceURL string `json:"service_url"`
	Token      string `json:"token"`
	Beta       string `json:"beta"`
	Disabled   bool   `json:"disabled"`
}

func Load() (Config, error) {
	cfg := Config{
		HomeDir:          defaultHome(),
		Host:             "127.0.0.1",
		Port:             8765,
		SynonLinkPackage: "assets/synon-link/synon-link-extension-v0.6.10.zip",
		SynonLinkAuth: SynonLinkAuthConfig{
			Username:          "local",
			SessionTTLMinutes: defaultSynonLinkSessionTTLMinutes,
		},
		EnabledAdapters: []string{},
		Network: NetworkPolicyConfig{
			MCPX509Strict: networktls.ModeAuto,
		},
		WeChat: WeChatConfig{
			BaseURL:        "https://ilinkai.weixin.qq.com",
			PollIntervalMS: 1000,
		},
		Runner: RunnerConfig{
			ID:                 "synon-go-runner",
			PollIntervalMS:     1000,
			LeaseTTLSeconds:    300,
			ChatTimeoutSeconds: 120,
			// A bounded default prevents one model or tool integration from
			// consuming an unbounded runner attempt. Explicit deployments can
			// choose a different positive limit through configuration.
			ChatToolRoundLimit:     64,
			ChatToolCallBatchLimit: 0,
			ChatMaxAttempts:        4,
			ChatWorkers:            0,
			CommandTimeoutSeconds:  600,
			ReplayLimit:            200,
			OutputLimitBytes:       1024 * 1024,
		},
		Memory: memoryconfig.Default(),
	}

	if home := os.Getenv("SYNON_HOME"); home != "" {
		cfg.HomeDir = home
	}

	configPath := os.Getenv("SYNON_CONFIG")
	if configPath != "" {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			return Config{}, fmt.Errorf("read config %s: %w", configPath, err)
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", configPath, err)
		}
	}
	if cfg.HomeDir == "" {
		cfg.HomeDir = defaultHome()
	}
	if home := os.Getenv("SYNON_HOME"); home != "" {
		cfg.HomeDir = home
	}
	if err := applyDataDirectoryControl(&cfg, configPath); err != nil {
		return Config{}, err
	}
	if err := applyCondaPaths(&cfg); err != nil {
		return Config{}, err
	}
	if err := applyNetworkPolicy(&cfg, configPath); err != nil {
		return Config{}, err
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 8765
	}
	if err := applyAddressEnv(&cfg); err != nil {
		return Config{}, err
	}
	if webRoot := strings.TrimSpace(os.Getenv("SYNON_WEB_ROOT")); webRoot != "" {
		cfg.WebRoot = webRoot
	}
	if cfg.SynonLinkPackage == "" {
		cfg.SynonLinkPackage = "assets/synon-link/synon-link-extension-v0.6.10.zip"
	}
	if err := applySynonLinkAuthEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := applyWebAuthEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := validateRuntimeBinding(cfg); err != nil {
		return Config{}, err
	}
	if err := applyEnabledAdaptersEnv(&cfg); err != nil {
		return Config{}, err
	}
	applyPluginDirectoryEnv(&cfg)
	applySkillDirectoryEnv(&cfg)
	applyFeishuEnv(&cfg)
	if err := applyWeChatEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := applyRunnerEnv(&cfg); err != nil {
		return Config{}, err
	}
	if err := applyCompactSummarizerEnv(&cfg); err != nil {
		return Config{}, err
	}
	if value, found := os.LookupEnv("OPERON_SERVICE_URL"); found {
		cfg.Feedback.ServiceURL = strings.TrimSpace(value)
	}
	if value, found := os.LookupEnv("SYNON_FEEDBACK_TOKEN"); found {
		cfg.Feedback.Token = strings.TrimSpace(value)
	}
	if value, found := os.LookupEnv("SYNON_FEEDBACK_BETA"); found {
		cfg.Feedback.Beta = strings.TrimSpace(value)
	}
	if value, found := os.LookupEnv("SYNON_FEEDBACK_DISABLED"); found {
		disabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("SYNON_FEEDBACK_DISABLED: %w", err)
		}
		cfg.Feedback.Disabled = disabled
	}
	if value, found := os.LookupEnv("SYNON_DISABLE_TELEMETRY"); found {
		disabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return Config{}, fmt.Errorf("SYNON_DISABLE_TELEMETRY: %w", err)
		}
		cfg.DisableTelemetry = disabled
	}
	if err := cfg.Memory.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyNetworkPolicy(cfg *Config, configPath string) error {
	if err := applyNetworkTimeouts(&cfg.Network); err != nil {
		return err
	}
	if value, found := os.LookupEnv("SYNON_NETWORK_ALLOWED_DOMAINS"); found {
		cfg.Network.AllowedDomains = splitCSV(value)
	}
	if value, found := os.LookupEnv("SYNON_NETWORK_DENIED_DOMAINS"); found {
		cfg.Network.DeniedDomains = splitCSV(value)
	}
	if value := strings.TrimSpace(os.Getenv("SYNON_NETWORK_CA_BUNDLE")); value != "" {
		cfg.Network.CABundle = value
	}
	if value := strings.TrimSpace(os.Getenv("SYNON_NETWORK_PROXY")); value != "" {
		cfg.Network.Proxy = value
	}
	if strings.TrimSpace(cfg.Network.Proxy) == "" {
		cfg.Network.Proxy = inheritedHTTPProxy()
	}
	proxy, err := networktls.NormalizeProxyURL(cfg.Network.Proxy)
	if err != nil {
		return fmt.Errorf("network.proxy: %w", err)
	}
	cfg.Network.Proxy = proxy
	mode, err := networktls.ValidateMode(cfg.Network.MCPX509Strict)
	if err != nil {
		return fmt.Errorf("network.mcp_x509_strict: %w", err)
	}
	cfg.Network.MCPX509Strict = mode
	if cfg.Network.CABundle != "" && !filepath.IsAbs(cfg.Network.CABundle) {
		if strings.TrimSpace(configPath) == "" {
			return errors.New("network.ca_bundle must be an absolute path when no config file is set")
		}
		configAbsolute, err := filepath.Abs(configPath)
		if err != nil {
			return fmt.Errorf("network.ca_bundle: resolve config path: %w", err)
		}
		cfg.Network.CABundle = filepath.Join(filepath.Dir(configAbsolute), cfg.Network.CABundle)
	}
	var normalizeErr error
	cfg.Network.AllowedDomains, normalizeErr = networkpolicy.NormalizePatterns(cfg.Network.AllowedDomains, 64)
	if normalizeErr != nil {
		return fmt.Errorf("network.allowed_domains: %w", normalizeErr)
	}
	cfg.Network.DeniedDomains, normalizeErr = networkpolicy.NormalizePatterns(cfg.Network.DeniedDomains, 0)
	if normalizeErr != nil {
		return fmt.Errorf("network.denied_domains: %w", normalizeErr)
	}
	for _, domain := range cfg.Network.AllowedDomains {
		if networkpolicy.PrivateOrReserved(domain) {
			return fmt.Errorf("network.allowed_domains: private/reserved host not grantable: %s", domain)
		}
		deniedPatterns := networkpolicy.EffectiveDeniedPatterns(cfg.Network.AllowedDomains, cfg.Network.DeniedDomains)
		// A public grant can coexist with explicit denied destinations: the
		// runtime evaluates denies first for each concrete connection.
		if denied := networkpolicy.ConflictingPattern(domain, deniedPatterns); domain != networkpolicy.PublicWildcard && denied != "" {
			return fmt.Errorf("network.allowed_domains: %s conflicts with denied domain %s", domain, denied)
		}
	}
	return nil
}

// inheritedHTTPProxy promotes the operator's standard process proxy into the
// explicit runtime configuration used by detached kernel executors. Relying on
// http.ProxyFromEnvironment only inside the executor is insufficient: service
// supervisors and process handoffs can intentionally narrow the child
// environment, causing model/API traffic to use the working host route while
// scientific downloads silently fall back to a much slower direct path.
// Unsupported standard proxy values are ignored here; only the product-specific
// SYNON_NETWORK_PROXY remains a strict configuration error.
func inheritedHTTPProxy() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		candidate := strings.TrimSpace(os.Getenv(key))
		if candidate == "" {
			continue
		}
		normalized, err := networktls.NormalizeProxyURL(candidate)
		if err == nil && normalized != "" {
			return normalized
		}
	}
	return ""
}

func applyCondaPaths(cfg *Config) error {
	if value := strings.TrimSpace(os.Getenv("SYNON_CONDA_HOME")); value != "" {
		cfg.CondaHome = value
	}
	if value := strings.TrimSpace(os.Getenv("SYNON_CONDA_ENVS_PATH")); value != "" {
		cfg.CondaEnvsPath = value
	}
	if strings.TrimSpace(cfg.CondaHome) == "" {
		cfg.CondaHome = filepath.Join(cfg.HomeDir, "conda")
	}
	if !filepath.IsAbs(cfg.CondaHome) {
		return fmt.Errorf("conda_home must be an absolute path")
	}
	if strings.IndexFunc(cfg.CondaHome, unicode.IsControl) >= 0 {
		return fmt.Errorf("conda_home contains control characters")
	}
	cfg.CondaHome = filepath.Clean(cfg.CondaHome)
	if strings.TrimSpace(cfg.CondaEnvsPath) == "" {
		cfg.CondaEnvsPath = filepath.Join(cfg.CondaHome, "envs")
	}
	if !filepath.IsAbs(cfg.CondaEnvsPath) {
		return fmt.Errorf("conda_envs_path must be an absolute path")
	}
	if strings.IndexFunc(cfg.CondaEnvsPath, unicode.IsControl) >= 0 {
		return fmt.Errorf("conda_envs_path contains control characters")
	}
	cfg.CondaEnvsPath = filepath.Clean(cfg.CondaEnvsPath)
	return nil
}

func applyPluginDirectoryEnv(cfg *Config) {
	if directories := os.Getenv("SYNON_PLUGIN_DIRS"); directories != "" {
		cfg.PluginDirectories = splitCSV(directories)
	}
}

func applyEnabledAdaptersEnv(cfg *Config) error {
	adapters := cfg.EnabledAdapters
	if value, found := os.LookupEnv("SYNON_ENABLED_ADAPTERS"); found {
		adapters = splitCSV(value)
	}
	supported := map[string]struct{}{
		"feishu": {},
		"wechat": {},
	}
	normalized := make([]string, 0, len(adapters))
	seen := make(map[string]struct{}, len(adapters))
	for _, adapter := range adapters {
		adapter = strings.ToLower(strings.TrimSpace(adapter))
		if adapter == "" {
			continue
		}
		if _, ok := supported[adapter]; !ok {
			return fmt.Errorf("unsupported enabled adapter: %s", adapter)
		}
		if _, ok := seen[adapter]; ok {
			continue
		}
		seen[adapter] = struct{}{}
		normalized = append(normalized, adapter)
	}
	cfg.EnabledAdapters = normalized
	return nil
}

func applySkillDirectoryEnv(cfg *Config) {
	if directories := os.Getenv("SYNON_SKILL_DIRS"); directories != "" {
		cfg.SkillDirectories = splitCSV(directories)
	}
}

func applySynonLinkAuthEnv(cfg *Config) error {
	if username := strings.TrimSpace(os.Getenv("SYNON_LINK_AUTH_USERNAME")); username != "" {
		cfg.SynonLinkAuth.Username = username
	}
	if password, ok := os.LookupEnv("SYNON_LINK_AUTH_PASSWORD"); ok {
		cfg.SynonLinkAuth.Password = password
	}
	if ttlText := strings.TrimSpace(os.Getenv("SYNON_LINK_AUTH_SESSION_TTL_MINUTES")); ttlText != "" {
		ttl, err := strconv.Atoi(ttlText)
		if err != nil || ttl <= 0 || ttl > 7*24*60 {
			return fmt.Errorf("invalid SYNON_LINK_AUTH_SESSION_TTL_MINUTES: %s", ttlText)
		}
		cfg.SynonLinkAuth.SessionTTLMinutes = ttl
	}
	if strings.TrimSpace(cfg.SynonLinkAuth.Username) == "" {
		cfg.SynonLinkAuth.Username = "local"
	}
	if cfg.SynonLinkAuth.SessionTTLMinutes == 0 {
		cfg.SynonLinkAuth.SessionTTLMinutes = defaultSynonLinkSessionTTLMinutes
	}
	if cfg.SynonLinkAuth.SessionTTLMinutes < 1 || cfg.SynonLinkAuth.SessionTTLMinutes > 7*24*60 {
		return fmt.Errorf("synon_link_auth.session_ttl_minutes must be between 1 and 10080")
	}
	if len(cfg.SynonLinkAuth.Username) > 128 || len(cfg.SynonLinkAuth.Password) > 1024 {
		return fmt.Errorf("synon_link_auth credentials exceed the configured size limit")
	}
	return nil
}

func applyWebAuthEnv(cfg *Config) error {
	if value, ok := os.LookupEnv("SYNON_AUTH_PUBLIC_BASE_URL"); ok {
		cfg.WebAuth.PublicBaseURL = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv("SYNON_AUTH_GOOGLE_CLIENT_ID"); ok {
		cfg.WebAuth.Google.ClientID = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv("SYNON_AUTH_GOOGLE_CLIENT_SECRET"); ok {
		cfg.WebAuth.Google.ClientSecret = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv("SYNON_AUTH_APPLE_CLIENT_ID"); ok {
		cfg.WebAuth.Apple.ClientID = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv("SYNON_AUTH_APPLE_TEAM_ID"); ok {
		cfg.WebAuth.Apple.TeamID = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv("SYNON_AUTH_APPLE_KEY_ID"); ok {
		cfg.WebAuth.Apple.KeyID = strings.TrimSpace(value)
	}
	if err := applyApplePrivateKeyEnv(cfg); err != nil {
		return err
	}
	if value, ok := os.LookupEnv("SYNON_AUTH_WECHAT_APP_ID"); ok {
		cfg.WebAuth.WeChat.AppID = strings.TrimSpace(value)
	}
	if value, ok := os.LookupEnv("SYNON_AUTH_WECHAT_APP_SECRET"); ok {
		cfg.WebAuth.WeChat.AppSecret = strings.TrimSpace(value)
	}
	if len(cfg.WebAuth.Google.ClientID) > 512 || len(cfg.WebAuth.Google.ClientSecret) > 2048 {
		return errors.New("web_auth Google credentials exceed the configured size limit")
	}
	if len(cfg.WebAuth.Apple.ClientID) > 512 || len(cfg.WebAuth.Apple.TeamID) > 512 ||
		len(cfg.WebAuth.Apple.KeyID) > 512 || len(cfg.WebAuth.Apple.PrivateKey) > webApplePrivateKeyMaxBytes {
		return errors.New("web_auth Apple credentials exceed the configured size limit")
	}
	if len(cfg.WebAuth.WeChat.AppID) > 512 || len(cfg.WebAuth.WeChat.AppSecret) > 2048 {
		return errors.New("web_auth WeChat credentials exceed the configured size limit")
	}
	if cfg.WebAuth.Google.ClientSecret != "" && cfg.WebAuth.Google.ClientID == "" {
		return errors.New("SYNON_AUTH_GOOGLE_CLIENT_SECRET requires SYNON_AUTH_GOOGLE_CLIENT_ID")
	}
	appleConfigured := cfg.WebAuth.Apple.ClientID != "" || cfg.WebAuth.Apple.TeamID != "" ||
		cfg.WebAuth.Apple.KeyID != "" || cfg.WebAuth.Apple.PrivateKey != ""
	if appleConfigured && (cfg.WebAuth.Apple.ClientID == "" || cfg.WebAuth.Apple.TeamID == "" ||
		cfg.WebAuth.Apple.KeyID == "" || cfg.WebAuth.Apple.PrivateKey == "") {
		return errors.New("Apple Sign in with Apple requires client ID, team ID, key ID, and private key")
	}
	if (cfg.WebAuth.WeChat.AppID == "") != (cfg.WebAuth.WeChat.AppSecret == "") {
		return errors.New("WeChat login requires both SYNON_AUTH_WECHAT_APP_ID and SYNON_AUTH_WECHAT_APP_SECRET")
	}
	if cfg.WebAuth.WeChat.AppID != "" && cfg.WebAuth.PublicBaseURL == "" {
		return errors.New("WeChat login requires SYNON_AUTH_PUBLIC_BASE_URL")
	}
	if cfg.WebAuth.PublicBaseURL == "" {
		return nil
	}
	parsed, err := url.Parse(cfg.WebAuth.PublicBaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("SYNON_AUTH_PUBLIC_BASE_URL must be an HTTPS origin without path, query, credentials, or fragment")
	}
	cfg.WebAuth.PublicBaseURL = strings.TrimSuffix(parsed.String(), "/")
	if cfg.WebAuth.Google.ClientID != "" && cfg.WebAuth.Google.ClientSecret == "" {
		return errors.New("remote Google OIDC requires SYNON_AUTH_GOOGLE_CLIENT_SECRET")
	}
	return nil
}

func applyApplePrivateKeyEnv(cfg *Config) error {
	if _, rawKeySet := os.LookupEnv("SYNON_AUTH_APPLE_PRIVATE_KEY"); rawKeySet {
		return errors.New("SYNON_AUTH_APPLE_PRIVATE_KEY is not supported; use SYNON_AUTH_APPLE_PRIVATE_KEY_B64 or SYNON_AUTH_APPLE_PRIVATE_KEY_FILE")
	}
	encodedKey, encodedKeySet := os.LookupEnv("SYNON_AUTH_APPLE_PRIVATE_KEY_B64")
	keyFile, keyFileSet := os.LookupEnv("SYNON_AUTH_APPLE_PRIVATE_KEY_FILE")
	configured := 0
	for _, present := range []bool{encodedKeySet, keyFileSet} {
		if present {
			configured++
		}
	}
	if configured > 1 {
		return errors.New("configure only one of SYNON_AUTH_APPLE_PRIVATE_KEY, SYNON_AUTH_APPLE_PRIVATE_KEY_B64, or SYNON_AUTH_APPLE_PRIVATE_KEY_FILE")
	}
	switch {
	case encodedKeySet:
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
		if err != nil {
			return fmt.Errorf("SYNON_AUTH_APPLE_PRIVATE_KEY_B64 is invalid: %w", err)
		}
		cfg.WebAuth.Apple.PrivateKey = strings.TrimSpace(string(decoded))
	case keyFileSet:
		path := strings.TrimSpace(keyFile)
		if path == "" || !filepath.IsAbs(path) || strings.IndexFunc(path, unicode.IsControl) >= 0 {
			return errors.New("SYNON_AUTH_APPLE_PRIVATE_KEY_FILE must be an absolute path without control characters")
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("read SYNON_AUTH_APPLE_PRIVATE_KEY_FILE: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("SYNON_AUTH_APPLE_PRIVATE_KEY_FILE must refer to a regular file")
		}
		if info.Size() > webApplePrivateKeyMaxBytes {
			return errors.New("SYNON_AUTH_APPLE_PRIVATE_KEY_FILE exceeds the configured size limit")
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open SYNON_AUTH_APPLE_PRIVATE_KEY_FILE: %w", err)
		}
		defer file.Close()
		contents, err := io.ReadAll(io.LimitReader(file, webApplePrivateKeyMaxBytes+1))
		if err != nil {
			return fmt.Errorf("read SYNON_AUTH_APPLE_PRIVATE_KEY_FILE: %w", err)
		}
		if len(contents) > webApplePrivateKeyMaxBytes {
			return errors.New("SYNON_AUTH_APPLE_PRIVATE_KEY_FILE exceeds the configured size limit")
		}
		cfg.WebAuth.Apple.PrivateKey = strings.TrimSpace(string(contents))
	}
	return nil
}

func applyAddressEnv(cfg *Config) error {
	if address := strings.TrimSpace(os.Getenv("SYNON_ADDRESS")); address != "" {
		host, portText, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("invalid SYNON_ADDRESS: %s", address)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("invalid SYNON_ADDRESS port: %s", portText)
		}
		if strings.TrimSpace(host) == "" {
			host = "127.0.0.1"
		}
		cfg.Host = host
		cfg.Port = port
	}
	if host := strings.TrimSpace(os.Getenv("SYNON_HOST")); host != "" {
		cfg.Host = host
	}
	if portText := strings.TrimSpace(os.Getenv("SYNON_PORT")); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("invalid SYNON_PORT: %s", portText)
		}
		cfg.Port = port
	}
	return nil
}

func validateRuntimeBinding(cfg Config) error {
	host := strings.Trim(strings.TrimSpace(cfg.Host), "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if address := net.ParseIP(host); address != nil && address.IsLoopback() {
		return nil
	}
	externalAuthConfigured := strings.TrimSpace(cfg.WebAuth.PublicBaseURL) != "" &&
		((strings.TrimSpace(cfg.WebAuth.Google.ClientID) != "" && strings.TrimSpace(cfg.WebAuth.Google.ClientSecret) != "") ||
			(strings.TrimSpace(cfg.WebAuth.Apple.ClientID) != "" && strings.TrimSpace(cfg.WebAuth.Apple.TeamID) != "" &&
				strings.TrimSpace(cfg.WebAuth.Apple.KeyID) != "" && strings.TrimSpace(cfg.WebAuth.Apple.PrivateKey) != "") ||
			(strings.TrimSpace(cfg.WebAuth.WeChat.AppID) != "" && strings.TrimSpace(cfg.WebAuth.WeChat.AppSecret) != ""))
	if strings.TrimSpace(cfg.SynonLinkAuth.Password) == "" && !externalAuthConfigured {
		return fmt.Errorf("non-loopback runtime binding %q requires SYNON_LINK_AUTH_PASSWORD or configured external authentication with SYNON_AUTH_PUBLIC_BASE_URL", cfg.Host)
	}
	return nil
}

func (c Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func defaultHome() string {
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, ".synon-go")
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".synon-go")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".synon-go")
	}
	return ".synon-go"
}

func applyWeChatEnv(cfg *Config) error {
	if cfg.WeChat.BaseURL == "" {
		cfg.WeChat.BaseURL = "https://ilinkai.weixin.qq.com"
	}
	if cfg.WeChat.PollIntervalMS == 0 {
		cfg.WeChat.PollIntervalMS = 1000
	}
	if accountID := os.Getenv("WECHAT_ACCOUNT_ID"); accountID != "" {
		cfg.WeChat.AccountID = accountID
	}
	if token := os.Getenv("WECHAT_BOT_TOKEN"); token != "" {
		cfg.WeChat.BotToken = token
	}
	if baseURL := os.Getenv("WECHAT_BASE_URL"); baseURL != "" {
		cfg.WeChat.BaseURL = baseURL
	}
	if userID := os.Getenv("WECHAT_USER_ID"); userID != "" {
		cfg.WeChat.UserID = userID
	}
	if interval := os.Getenv("WECHAT_POLL_INTERVAL_MS"); interval != "" {
		value, err := strconv.Atoi(interval)
		if err != nil || value <= 0 {
			return fmt.Errorf("invalid WECHAT_POLL_INTERVAL_MS: %s", interval)
		}
		cfg.WeChat.PollIntervalMS = value
	}
	return nil
}

func applyFeishuEnv(cfg *Config) {
	if appID := os.Getenv("FEISHU_APP_ID"); appID != "" {
		cfg.Feishu.AppID = appID
	}
	if appSecret := os.Getenv("FEISHU_APP_SECRET"); appSecret != "" {
		cfg.Feishu.AppSecret = appSecret
	}
	if verificationToken := os.Getenv("FEISHU_VERIFICATION_TOKEN"); verificationToken != "" {
		cfg.Feishu.VerificationToken = verificationToken
	}
	if encryptKey := os.Getenv("FEISHU_ENCRYPT_KEY"); encryptKey != "" {
		cfg.Feishu.EncryptKey = encryptKey
	}
	if domain := os.Getenv("FEISHU_DOMAIN"); domain != "" {
		cfg.Feishu.Domain = domain
	}
}

func applyRunnerEnv(cfg *Config) error {
	if cfg.Runner.ID == "" {
		cfg.Runner.ID = "synon-go-runner"
	}
	if cfg.Runner.PollIntervalMS == 0 {
		cfg.Runner.PollIntervalMS = 1000
	}
	if cfg.Runner.LeaseTTLSeconds == 0 {
		cfg.Runner.LeaseTTLSeconds = 300
	}
	if cfg.Runner.ChatTimeoutSeconds == 0 {
		cfg.Runner.ChatTimeoutSeconds = 120
	}
	if cfg.Runner.ChatToolRoundLimit < 0 {
		return fmt.Errorf("runner chat_tool_round_limit must be zero (unlimited) or positive")
	}
	if cfg.Runner.ChatToolCallBatchLimit < 0 {
		return fmt.Errorf("runner chat_tool_call_batch_limit must be zero (unlimited) or positive")
	}
	if cfg.Runner.ChatMaxAttempts == 0 {
		cfg.Runner.ChatMaxAttempts = 4
	}
	if cfg.Runner.CommandTimeoutSeconds == 0 {
		cfg.Runner.CommandTimeoutSeconds = 600
	}
	if cfg.Runner.ReplayLimit == 0 {
		cfg.Runner.ReplayLimit = 200
	}
	if cfg.Runner.OutputLimitBytes == 0 {
		cfg.Runner.OutputLimitBytes = 1024 * 1024
	}
	if value := os.Getenv("SYNON_RUNNER_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid SYNON_RUNNER_ENABLED: %s", value)
		}
		cfg.Runner.Enabled = enabled
	}
	if id := os.Getenv("SYNON_RUNNER_ID"); id != "" {
		cfg.Runner.ID = id
	}
	if provider := os.Getenv("SYNON_RUNNER_PROVIDER"); provider != "" {
		cfg.Runner.Provider = provider
	}
	if command := os.Getenv("SYNON_RUNNER_COMMAND"); command != "" {
		cfg.Runner.Command = command
	}
	if rawArgs := os.Getenv("SYNON_RUNNER_ARGS_JSON"); rawArgs != "" {
		var args []string
		if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
			return fmt.Errorf("invalid SYNON_RUNNER_ARGS_JSON: %w", err)
		}
		cfg.Runner.Args = args
	}
	if endpoint := os.Getenv("SYNON_RUNNER_CHAT_ENDPOINT"); endpoint != "" {
		cfg.Runner.ChatEndpoint = endpoint
	}
	if apiKey := os.Getenv("SYNON_RUNNER_CHAT_API_KEY"); apiKey != "" {
		cfg.Runner.ChatAPIKey = apiKey
	}
	if model := os.Getenv("SYNON_RUNNER_CHAT_MODEL"); model != "" {
		cfg.Runner.ChatModel = model
	}
	if systemPrompt := os.Getenv("SYNON_RUNNER_CHAT_SYSTEM_PROMPT"); systemPrompt != "" {
		cfg.Runner.ChatSystemPrompt = systemPrompt
	}
	if tools := os.Getenv("SYNON_RUNNER_CHAT_TOOLS"); tools != "" {
		cfg.Runner.ChatTools = splitCSV(tools)
	}
	if value := os.Getenv("SYNON_RUNNER_CHAT_TIMEOUT_SECONDS"); value != "" {
		parsed, err := positiveIntEnv("SYNON_RUNNER_CHAT_TIMEOUT_SECONDS", value)
		if err != nil {
			return err
		}
		cfg.Runner.ChatTimeoutSeconds = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_CHAT_TOOL_ROUND_LIMIT"); value != "" {
		parsed, err := nonNegativeIntEnv("SYNON_RUNNER_CHAT_TOOL_ROUND_LIMIT", value)
		if err != nil {
			return err
		}
		cfg.Runner.ChatToolRoundLimit = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_CHAT_TOOL_CALL_BATCH_LIMIT"); value != "" {
		parsed, err := nonNegativeIntEnv("SYNON_RUNNER_CHAT_TOOL_CALL_BATCH_LIMIT", value)
		if err != nil {
			return err
		}
		cfg.Runner.ChatToolCallBatchLimit = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_CHAT_MAX_ATTEMPTS"); value != "" {
		parsed, err := positiveIntEnv("SYNON_RUNNER_CHAT_MAX_ATTEMPTS", value)
		if err != nil {
			return err
		}
		cfg.Runner.ChatMaxAttempts = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_CHAT_WORKERS"); value != "" {
		parsed, err := nonNegativeIntEnv("SYNON_RUNNER_CHAT_WORKERS", value)
		if err != nil {
			return err
		}
		cfg.Runner.ChatWorkers = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_COMMAND_TIMEOUT_SECONDS"); value != "" {
		parsed, err := positiveIntEnv("SYNON_RUNNER_COMMAND_TIMEOUT_SECONDS", value)
		if err != nil {
			return err
		}
		cfg.Runner.CommandTimeoutSeconds = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_POLL_INTERVAL_MS"); value != "" {
		parsed, err := positiveIntEnv("SYNON_RUNNER_POLL_INTERVAL_MS", value)
		if err != nil {
			return err
		}
		cfg.Runner.PollIntervalMS = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_LEASE_TTL_SECONDS"); value != "" {
		parsed, err := positiveIntEnv("SYNON_RUNNER_LEASE_TTL_SECONDS", value)
		if err != nil {
			return err
		}
		cfg.Runner.LeaseTTLSeconds = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_REPLAY_LIMIT"); value != "" {
		parsed, err := positiveInt64Env("SYNON_RUNNER_REPLAY_LIMIT", value)
		if err != nil {
			return err
		}
		cfg.Runner.ReplayLimit = parsed
	}
	if value := os.Getenv("SYNON_RUNNER_OUTPUT_LIMIT_BYTES"); value != "" {
		parsed, err := positiveInt64Env("SYNON_RUNNER_OUTPUT_LIMIT_BYTES", value)
		if err != nil {
			return err
		}
		cfg.Runner.OutputLimitBytes = parsed
	}
	if cfg.Runner.PollIntervalMS <= 0 {
		return fmt.Errorf("runner poll_interval_ms must be positive")
	}
	if cfg.Runner.LeaseTTLSeconds <= 0 {
		return fmt.Errorf("runner lease_ttl_seconds must be positive")
	}
	if cfg.Runner.ChatTimeoutSeconds <= 0 {
		return fmt.Errorf("runner chat_timeout_seconds must be positive")
	}
	if cfg.Runner.ChatToolRoundLimit < 0 {
		return fmt.Errorf("runner chat_tool_round_limit must be zero (unlimited) or positive")
	}
	if cfg.Runner.ChatToolCallBatchLimit < 0 {
		return fmt.Errorf("runner chat_tool_call_batch_limit must be zero (unlimited) or positive")
	}
	if cfg.Runner.ChatMaxAttempts <= 0 {
		return fmt.Errorf("runner chat_max_attempts must be positive")
	}
	if cfg.Runner.ChatWorkers < 0 {
		return fmt.Errorf("runner chat_workers must be zero (automatic) or positive")
	}
	if cfg.Runner.CommandTimeoutSeconds <= 0 {
		return fmt.Errorf("runner command_timeout_seconds must be positive")
	}
	if cfg.Runner.ReplayLimit <= 0 {
		return fmt.Errorf("runner replay_limit must be positive")
	}
	if cfg.Runner.ReplayLimit > transcript.MaxRunnerReplayProjection {
		return fmt.Errorf("runner replay_limit must not exceed %d", transcript.MaxRunnerReplayProjection)
	}
	if cfg.Runner.OutputLimitBytes <= 0 {
		return fmt.Errorf("runner output_limit_bytes must be positive")
	}
	return nil
}

func applyCompactSummarizerEnv(cfg *Config) error {
	if endpoint := os.Getenv("SYNON_COMPACT_SUMMARIZER_ENDPOINT"); endpoint != "" {
		cfg.CompactSummarizer.Endpoint = endpoint
	}
	if apiKey := os.Getenv("SYNON_COMPACT_SUMMARIZER_API_KEY"); apiKey != "" {
		cfg.CompactSummarizer.APIKey = apiKey
	}
	if model := os.Getenv("SYNON_COMPACT_SUMMARIZER_MODEL"); model != "" {
		cfg.CompactSummarizer.Model = model
	}
	if value := os.Getenv("SYNON_COMPACT_SUMMARIZER_TIMEOUT_SECONDS"); value != "" {
		parsed, err := positiveIntEnv("SYNON_COMPACT_SUMMARIZER_TIMEOUT_SECONDS", value)
		if err != nil {
			return err
		}
		cfg.CompactSummarizer.TimeoutSeconds = parsed
	}
	if value := os.Getenv("SYNON_COMPACT_SUMMARIZER_MAX_ATTEMPTS"); value != "" {
		parsed, err := positiveIntEnv("SYNON_COMPACT_SUMMARIZER_MAX_ATTEMPTS", value)
		if err != nil {
			return err
		}
		cfg.CompactSummarizer.MaxAttempts = parsed
	}
	if cfg.CompactSummarizer.TimeoutSeconds < 0 {
		return fmt.Errorf("compact_summarizer.timeout_seconds must be positive")
	}
	if cfg.CompactSummarizer.MaxAttempts < 0 {
		return fmt.Errorf("compact_summarizer.max_attempts must be positive")
	}
	return nil
}

func positiveIntEnv(name string, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s: %s", name, value)
	}
	return parsed, nil
}

func nonNegativeIntEnv(name string, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("invalid %s: %s", name, value)
	}
	return parsed, nil
}

func positiveInt64Env(name string, value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s: %s", name, value)
	}
	return parsed, nil
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
