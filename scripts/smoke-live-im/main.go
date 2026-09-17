package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/adapters/feishu"
	"synon-go/internal/adapters/wechat"
	"synon-go/internal/buildinfo"
	"synon-go/internal/config"
)

type liveSmokePlan struct {
	Enabled    []string
	Skipped    map[string]bool
	Message    string
	Timeout    time.Duration
	RuntimeURL string
	RequireAll bool
	WeChat     wechatSmokeConfig
	Feishu     feishuSmokeConfig
}

type wechatSmokeConfig struct {
	Token        string
	BaseURL      string
	UserID       string
	ContextToken string
	Message      string
}

type feishuSmokeConfig struct {
	AppID         string
	AppSecret     string
	TenantToken   string
	Domain        string
	ChatID        string
	OpenID        string
	ReceiveIDType string
	ReceiveID     string
	Message       string
}

type liveSmokeOutput struct {
	Status                 string                    `json:"status"`
	Enabled                []string                  `json:"enabled"`
	Skipped                []string                  `json:"skipped"`
	TimeoutSeconds         int                       `json:"timeoutSeconds"`
	Platforms              []liveSmokePlatformPlan   `json:"platforms"`
	Results                []liveSmokePlatformResult `json:"results,omitempty"`
	Error                  string                    `json:"error,omitempty"`
	SecretsRedacted        bool                      `json:"secretsRedacted"`
	RuntimeAuditConfigured bool                      `json:"runtimeAuditConfigured"`
	RuntimeAuditReady      bool                      `json:"runtimeAuditReady"`
}

type liveSmokePlatformPlan struct {
	Platform                string   `json:"platform"`
	Ready                   bool     `json:"ready"`
	MissingCredentialFields []string `json:"missingCredentialFields"`
}

type liveSmokePlatformResult struct {
	Platform             string `json:"platform"`
	Status               string `json:"status"`
	Error                string `json:"error,omitempty"`
	RuntimeAuditRecorded bool   `json:"runtimeAuditRecorded"`
}

type runtimeAuditSession struct {
	baseURL   string
	client    *http.Client
	csrfToken string
}

func main() {
	requireAllFlag := flag.Bool("require-all", false, "fail when any live IM platform is missing credentials")
	jsonFlag := flag.Bool("json", false, "emit machine-readable live IM smoke results")
	planFlag := flag.Bool("plan", false, "only report configured platforms and missing credential fields")
	productName := buildinfo.Release().Name
	runtimeURLFlag := flag.String("runtime-url", "", "record redacted delivery evidence in a running "+productName+" runtime")
	configPathFlag := flag.String("config", "", "load adapter credentials and live smoke targets from a "+productName+" JSON config")
	flag.Parse()

	if configPath := strings.TrimSpace(*configPathFlag); configPath != "" {
		if err := os.Setenv("SYNON_CONFIG", configPath); err != nil {
			fmt.Fprintf(os.Stderr, "set Synon config path: %v\n", err)
			os.Exit(2)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load Synon config: %v\n", err)
		os.Exit(2)
	}
	plan := planFromConfigAndEnv(cfg, os.Getenv)
	result := newLiveSmokeOutput(plan)
	runtimeURL := strings.TrimSpace(firstNonEmpty(*runtimeURLFlag, plan.RuntimeURL))
	result.RuntimeAuditConfigured = runtimeURL != ""
	if *planFlag {
		if runtimeURL != "" {
			ctx, cancel := context.WithTimeout(context.Background(), plan.Timeout)
			_, auditErr := prepareRuntimeAuditSession(
				ctx,
				http.DefaultClient,
				runtimeURL,
				cfg.SynonLinkAuth.Username,
				cfg.SynonLinkAuth.Password,
			)
			cancel()
			if auditErr != nil {
				result.Error = "runtime audit preflight failed: " + auditErr.Error()
			} else {
				result.RuntimeAuditReady = true
			}
		}
		result.Status = "planned"
		emitLiveSmokeOutput(result, true)
		return
	}
	requireAll := *requireAllFlag || plan.RequireAll
	if requireAll && len(plan.Skipped) > 0 {
		result.Status = "missing_credentials"
		result.Error = "missing live IM credentials for: " + strings.Join(skippedNames(plan), ",")
		emitLiveSmokeOutput(result, *jsonFlag)
		os.Exit(2)
	}
	if len(plan.Enabled) == 0 {
		result.Status = "skipped"
		emitLiveSmokeOutput(result, *jsonFlag)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), plan.Timeout)
	defer cancel()
	var auditSession *runtimeAuditSession
	if runtimeURL != "" {
		auditSession, err = prepareRuntimeAuditSession(
			ctx,
			http.DefaultClient,
			runtimeURL,
			cfg.SynonLinkAuth.Username,
			cfg.SynonLinkAuth.Password,
		)
		if err != nil {
			result.Status = "failed"
			result.Error = "runtime audit preflight failed: " + err.Error()
			emitLiveSmokeOutput(result, *jsonFlag)
			os.Exit(1)
		}
		result.RuntimeAuditReady = true
	}

	for _, platform := range plan.Enabled {
		if err := runPlatform(ctx, platform, plan); err != nil {
			result.Status = "failed"
			redacted := redactLiveSmokeSecrets(err.Error(), plan)
			platformResult := liveSmokePlatformResult{Platform: platform, Status: "fail", Error: redacted}
			if auditSession != nil {
				if auditErr := auditSession.record(ctx, platformResult); auditErr == nil {
					platformResult.RuntimeAuditRecorded = true
				} else {
					platformResult.Error += "; runtime audit failed: " + auditErr.Error()
				}
			}
			result.Results = append(result.Results, platformResult)
			result.Error = fmt.Sprintf("%s smoke failed: %s", platform, redacted)
			emitLiveSmokeOutput(result, *jsonFlag)
			os.Exit(1)
		}
		platformResult := liveSmokePlatformResult{Platform: platform, Status: "pass"}
		if auditSession != nil {
			if err := auditSession.record(ctx, platformResult); err != nil {
				platformResult.Error = "runtime audit failed: " + err.Error()
				result.Results = append(result.Results, platformResult)
				result.Status = "failed"
				result.Error = fmt.Sprintf("%s delivery passed but runtime audit failed: %s", platform, err)
				emitLiveSmokeOutput(result, *jsonFlag)
				os.Exit(1)
			}
			platformResult.RuntimeAuditRecorded = true
		}
		result.Results = append(result.Results, platformResult)
	}
	result.Status = "pass"
	emitLiveSmokeOutput(result, *jsonFlag)
}

func prepareRuntimeAuditSession(ctx context.Context, sourceClient *http.Client, rawBaseURL, username, password string) (*runtimeAuditSession, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawBaseURL))
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" || !runtimeAuditLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("runtime audit URL must be an absolute loopback HTTP or HTTPS origin")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create runtime audit cookie jar: %w", err)
	}
	if sourceClient == nil {
		sourceClient = http.DefaultClient
	}
	client := *sourceClient
	client.Jar = jar
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	session := &runtimeAuditSession{baseURL: strings.TrimRight(parsed.String(), "/"), client: &client}

	status, err := session.currentUserStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK {
		return session, nil
	}
	if status != http.StatusUnauthorized {
		return nil, fmt.Errorf("runtime audit identity probe returned HTTP %d", status)
	}
	if strings.TrimSpace(password) == "" {
		return nil, errors.New("runtime audit requires the configured local Web password")
	}
	if err := session.login(ctx, strings.TrimSpace(username), password); err != nil {
		return nil, err
	}
	return session, nil
}

func runtimeAuditLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *runtimeAuditSession) currentUserStatus(ctx context.Context) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/api/auth/user", nil)
	if err != nil {
		return 0, fmt.Errorf("build runtime audit identity probe: %w", err)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("probe runtime audit identity: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	return response.StatusCode, nil
}

func (s *runtimeAuditSession) login(ctx context.Context, username, password string) error {
	payload, err := json.Marshal(map[string]any{"username": username, "password": password, "remember": false})
	if err != nil {
		return fmt.Errorf("encode runtime audit login: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/login", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build runtime audit login: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("authenticate runtime audit session: %w", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&envelope); err != nil {
		return errors.New("runtime audit login returned an invalid response")
	}
	if response.StatusCode != http.StatusOK || !envelope.Success {
		return fmt.Errorf("runtime audit login rejected with HTTP %d", response.StatusCode)
	}
	parsed, _ := url.Parse(s.baseURL)
	hasSession := false
	for _, cookie := range s.client.Jar.Cookies(parsed) {
		switch cookie.Name {
		case "synon_session":
			hasSession = strings.TrimSpace(cookie.Value) != ""
		case "synon_csrf":
			s.csrfToken = strings.TrimSpace(cookie.Value)
		}
	}
	if !hasSession || s.csrfToken == "" {
		return errors.New("runtime audit login did not establish session and CSRF cookies")
	}
	return nil
}

func (s *runtimeAuditSession) record(ctx context.Context, result liveSmokePlatformResult) error {
	platform := strings.ToLower(strings.TrimSpace(result.Platform))
	if platform == "" {
		return errors.New("runtime audit platform is required")
	}
	if result.Status != "pass" && result.Status != "fail" {
		return fmt.Errorf("runtime audit status must be pass or fail, got %q", result.Status)
	}
	recordedAt := time.Now().UTC().Format(time.RFC3339Nano)
	smoke := map[string]any{
		"status":          result.Status,
		"requested":       true,
		"method":          "outbound_delivery",
		"liveChecked":     true,
		"deliveryChecked": true,
		"deliveryPassed":  result.Status == "pass",
		"requiresNetwork": true,
		"checkedAt":       recordedAt,
	}
	if strings.TrimSpace(result.Error) != "" {
		smoke["error"] = strings.TrimSpace(result.Error)
	}
	payload := map[string]any{"input": map[string]any{
		"namespace": "adapter-live-smoke",
		"key":       "platform:" + platform,
		"value": map[string]any{
			"platform":        platform,
			"recordedAt":      recordedAt,
			"smoke":           smoke,
			"secretsRedacted": true,
		},
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode runtime audit: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/api/tools/runtime_set/execute", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build runtime audit request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if s.csrfToken != "" {
		request.Header.Set("X-Synon-CSRF-Token", s.csrfToken)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("write runtime audit: %w", err)
	}
	defer response.Body.Close()
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&envelope); err != nil {
		return fmt.Errorf("decode runtime audit response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		return fmt.Errorf("runtime audit rejected: status=%d error=%s", response.StatusCode, strings.TrimSpace(envelope.Error))
	}
	return nil
}

func planFromEnv(getenv func(string) string) liveSmokePlan {
	return planFromConfigAndEnv(config.Config{}, getenv)
}

func planFromConfigAndEnv(cfg config.Config, getenv func(string) string) liveSmokePlan {
	message := strings.TrimSpace(firstNonEmpty(getenv("SYNON_LIVE_IM_SMOKE_MESSAGE"), cfg.LiveIMSmoke.Message))
	if message == "" {
		identity := buildinfo.Release()
		message = identity.Name + " v" + identity.Version + " live IM smoke"
	}
	timeout := 30 * time.Second
	if cfg.LiveIMSmoke.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.LiveIMSmoke.TimeoutSeconds) * time.Second
	}
	if raw := strings.TrimSpace(getenv("SYNON_LIVE_IM_SMOKE_TIMEOUT_SECONDS")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	plan := liveSmokePlan{
		Skipped:    map[string]bool{},
		Message:    message,
		Timeout:    timeout,
		RuntimeURL: strings.TrimSpace(firstNonEmpty(getenv("SYNON_LIVE_IM_RUNTIME_URL"), cfg.LiveIMSmoke.RuntimeURL)),
		RequireAll: cfg.LiveIMSmoke.RequireAll || envBool(getenv("SYNON_LIVE_IM_REQUIRE_ALL")),
		WeChat: wechatSmokeConfig{
			Token:        strings.TrimSpace(firstNonEmpty(getenv("WECHAT_BOT_TOKEN"), cfg.WeChat.BotToken)),
			BaseURL:      strings.TrimSpace(firstNonEmpty(getenv("WECHAT_BASE_URL"), cfg.WeChat.BaseURL, wechat.DefaultBaseURL)),
			UserID:       strings.TrimSpace(firstNonEmpty(getenv("WECHAT_LIVE_SMOKE_USER_ID"), cfg.LiveIMSmoke.WeChatUserID, getenv("WECHAT_USER_ID"), cfg.WeChat.UserID)),
			ContextToken: strings.TrimSpace(firstNonEmpty(getenv("WECHAT_LIVE_SMOKE_CONTEXT_TOKEN"), cfg.LiveIMSmoke.WeChatContextToken)),
			Message:      message,
		},
		Feishu: feishuSmokeConfig{
			AppID:       strings.TrimSpace(firstNonEmpty(getenv("FEISHU_APP_ID"), cfg.Feishu.AppID)),
			AppSecret:   strings.TrimSpace(firstNonEmpty(getenv("FEISHU_APP_SECRET"), cfg.Feishu.AppSecret)),
			TenantToken: strings.TrimSpace(getenv("FEISHU_TENANT_ACCESS_TOKEN")),
			Domain:      strings.TrimSpace(firstNonEmpty(getenv("FEISHU_DOMAIN"), cfg.Feishu.Domain, "https://open.feishu.cn")),
			ChatID:      strings.TrimSpace(firstNonEmpty(getenv("FEISHU_LIVE_SMOKE_CHAT_ID"), cfg.LiveIMSmoke.FeishuChatID)),
			OpenID:      strings.TrimSpace(firstNonEmpty(getenv("FEISHU_LIVE_SMOKE_OPEN_ID"), cfg.LiveIMSmoke.FeishuOpenID)),
			Message:     message,
		},
	}
	finalizeFeishuSmokeTarget(&plan.Feishu)
	addOrSkip(&plan, "wechat", plan.WeChat.Token != "" && plan.WeChat.UserID != "")
	addOrSkip(&plan, "feishu", plan.Feishu.ReceiveID != "" && (plan.Feishu.TenantToken != "" || plan.Feishu.AppID != "" && plan.Feishu.AppSecret != ""))
	return plan
}

func finalizeFeishuSmokeTarget(cfg *feishuSmokeConfig) {
	if cfg == nil {
		return
	}
	if cfg.ChatID != "" {
		cfg.ReceiveIDType = "chat_id"
		cfg.ReceiveID = cfg.ChatID
		return
	}
	if cfg.OpenID != "" {
		cfg.ReceiveIDType = "open_id"
		cfg.ReceiveID = cfg.OpenID
	}
}

func addOrSkip(plan *liveSmokePlan, platform string, enabled bool) {
	if enabled {
		plan.Enabled = append(plan.Enabled, platform)
		return
	}
	plan.Skipped[platform] = true
}

func runPlatform(ctx context.Context, platform string, plan liveSmokePlan) error {
	switch platform {
	case "wechat":
		return runWeChatSmoke(ctx, plan.WeChat)
	case "feishu":
		return runFeishuSmoke(ctx, plan.Feishu)
	default:
		return fmt.Errorf("unsupported platform %q", platform)
	}
}

func runWeChatSmoke(ctx context.Context, cfg wechatSmokeConfig) error {
	return wechat.SendText(ctx, http.DefaultClient, cfg.BaseURL, cfg.Token, wechat.SendTextOptions{
		To:           cfg.UserID,
		Text:         cfg.Message,
		ContextToken: cfg.ContextToken,
		Timeout:      20 * time.Second,
	})
}

func runFeishuSmoke(ctx context.Context, cfg feishuSmokeConfig) error {
	tokenProvider := func(ctx context.Context) (string, error) {
		if cfg.TenantToken != "" {
			return cfg.TenantToken, nil
		}
		return fetchFeishuTenantToken(ctx, cfg.Domain, cfg.AppID, cfg.AppSecret)
	}
	client := feishu.NewCardKitClient(http.DefaultClient, cfg.Domain, tokenProvider)
	_, err := client.SendRenderedCardAsMessageTo(ctx, cfg.ReceiveIDType, cfg.ReceiveID, feishu.BuildRenderedCard(cfg.Message), "")
	return err
}

func fetchFeishuTenantToken(ctx context.Context, domain string, appID string, appSecret string) (string, error) {
	var decoded struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	body := map[string]string{"app_id": appID, "app_secret": appSecret}
	if err := postJSON(ctx, domain, "/open-apis/auth/v3/tenant_access_token/internal", "", body, &decoded); err != nil {
		return "", err
	}
	if decoded.Code != 0 || strings.TrimSpace(decoded.TenantAccessToken) == "" {
		return "", fmt.Errorf("feishu tenant token missing: code=%d msg=%s", decoded.Code, decoded.Msg)
	}
	return decoded.TenantAccessToken, nil
}

func newLiveSmokeOutput(plan liveSmokePlan) liveSmokeOutput {
	return liveSmokeOutput{
		Status:          "pending",
		Enabled:         append([]string{}, plan.Enabled...),
		Skipped:         skippedNames(plan),
		TimeoutSeconds:  int(plan.Timeout.Seconds()),
		Platforms:       liveSmokePlatformPlans(plan),
		SecretsRedacted: true,
	}
}

func liveSmokePlatformPlans(plan liveSmokePlan) []liveSmokePlatformPlan {
	return []liveSmokePlatformPlan{
		{Platform: "wechat", Ready: !plan.Skipped["wechat"], MissingCredentialFields: missingWeChatFields(plan.WeChat)},
		{Platform: "feishu", Ready: !plan.Skipped["feishu"], MissingCredentialFields: missingFeishuFields(plan.Feishu)},
	}
}

func emitLiveSmokeOutput(result liveSmokeOutput, asJSON bool) {
	if asJSON {
		encoded, err := json.Marshal(result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "LIVE_IM_SMOKE_FAILED error=%v\n", err)
			return
		}
		fmt.Println(string(encoded))
		return
	}
	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "LIVE_IM_SMOKE_%s error=%s\n", strings.ToUpper(result.Status), result.Error)
	}
	for _, item := range result.Results {
		if item.Status == "pass" {
			fmt.Printf("LIVE_IM_SMOKE_OK platform=%s\n", item.Platform)
		} else {
			fmt.Fprintf(os.Stderr, "LIVE_IM_SMOKE_FAILED platform=%s error=%s\n", item.Platform, item.Error)
		}
	}
	if len(result.Results) == 0 {
		fmt.Printf("LIVE_IM_SMOKE_%s platforms=%s\n", strings.ToUpper(result.Status), strings.Join(result.Skipped, ","))
	}
	if len(result.Skipped) > 0 && result.Status == "pass" {
		fmt.Printf("LIVE_IM_SMOKE_SKIPPED platforms=%s\n", strings.Join(result.Skipped, ","))
	}
}

func missingWeChatFields(cfg wechatSmokeConfig) []string {
	missing := []string{}
	if cfg.Token == "" {
		missing = append(missing, "WECHAT_BOT_TOKEN")
	}
	if cfg.UserID == "" {
		missing = append(missing, "WECHAT_LIVE_SMOKE_USER_ID or WECHAT_USER_ID")
	}
	return missing
}

func missingFeishuFields(cfg feishuSmokeConfig) []string {
	missing := []string{}
	if cfg.ReceiveID == "" {
		missing = append(missing, "FEISHU_LIVE_SMOKE_CHAT_ID or FEISHU_LIVE_SMOKE_OPEN_ID")
	}
	if cfg.TenantToken == "" && (cfg.AppID == "" || cfg.AppSecret == "") {
		missing = append(missing, "FEISHU_TENANT_ACCESS_TOKEN or FEISHU_APP_ID+FEISHU_APP_SECRET")
	}
	return missing
}

func redactLiveSmokeSecrets(text string, plan liveSmokePlan) string {
	for _, secret := range []string{
		plan.WeChat.Token,
		plan.WeChat.ContextToken,
		plan.Feishu.AppSecret,
		plan.Feishu.TenantToken,
	} {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "<redacted>")
		}
	}
	return text
}

func postJSON(ctx context.Context, host string, path string, bearer string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	target, err := url.JoinPath(strings.TrimRight(host, "/")+"/", strings.TrimPrefix(path, "/"))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(bearer) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s failed: %s: %s", path, resp.Status, string(data))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("empty JSON response")
	}
	return json.Unmarshal(data, out)
}

func skippedNames(plan liveSmokePlan) []string {
	names := make([]string, 0, len(plan.Skipped))
	for _, platform := range []string{"wechat", "feishu"} {
		if plan.Skipped[platform] {
			names = append(names, platform)
		}
	}
	return names
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func envBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
