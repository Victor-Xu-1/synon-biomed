package feishu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultDeviceRegistrationBaseURL = "https://accounts.feishu.cn"
	deviceRegistrationPath           = "/oauth/v1/app/registration"
	defaultDeviceRegistrationTTL     = 10 * time.Minute
	defaultDeviceRegistrationTimeout = 35 * time.Second
	defaultDevicePollInterval        = 5 * time.Second
	maxDeviceRegistrationSessions    = 32
	maxDeviceResponseBytes           = 128 * 1024
)

type DeviceQRStatus string

const (
	DeviceQRStatusWait      DeviceQRStatus = "wait"
	DeviceQRStatusConfirmed DeviceQRStatus = "confirmed"
	DeviceQRStatusExpired   DeviceQRStatus = "expired"
	DeviceQRStatusError     DeviceQRStatus = "error"
)

type DeviceQRStartOptions struct {
	SessionKey string
	Force      bool
	Timeout    time.Duration
}

type DeviceQRStartResult struct {
	SessionKey      string    `json:"sessionKey"`
	VerificationURL string    `json:"verificationUrl"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Message         string    `json:"message"`
}

type DeviceQRPollOptions struct {
	SessionKey string
	Timeout    time.Duration
}

type DeviceQRPollResult struct {
	Status       DeviceQRStatus `json:"status"`
	Connected    bool           `json:"connected"`
	Message      string         `json:"message"`
	ClientID     string         `json:"-"`
	ClientSecret string         `json:"-"`
	UserOpenID   string         `json:"-"`
	UserName     string         `json:"-"`
	TenantBrand  string         `json:"-"`
	Domain       string         `json:"-"`
}

type DeviceQRLoginManager struct {
	BaseURL string
	TTL     time.Duration

	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]deviceQRSession
}

type deviceQRSession struct {
	SessionKey      string
	DeviceCode      string
	VerificationURL string
	ExpiresAt       time.Time
	NextPollAt      time.Time
	PollInterval    time.Duration
	BaseURL         string
}

type deviceRegistrationResponse struct {
	DeviceCode              string `json:"device_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

func NewDeviceQRLoginManager(baseURL string) *DeviceQRLoginManager {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultDeviceRegistrationBaseURL
	}
	return &DeviceQRLoginManager{
		BaseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		TTL:      defaultDeviceRegistrationTTL,
		now:      time.Now,
		sessions: make(map[string]deviceQRSession),
	}
}

func (m *DeviceQRLoginManager) Start(ctx context.Context, client *http.Client, options DeviceQRStartOptions) (DeviceQRStartResult, error) {
	if m == nil {
		return DeviceQRStartResult{}, errors.New("feishu device QR manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = http.DefaultClient
	}
	now := m.clock().UTC()
	sessionKey := strings.TrimSpace(options.SessionKey)
	if sessionKey == "" {
		sessionKey = randomDeviceSessionKey()
	}
	if len(sessionKey) > 128 {
		return DeviceQRStartResult{}, errors.New("feishu device QR session key is too long")
	}

	m.mu.Lock()
	m.purgeExpiredLocked(now)
	if existing, ok := m.sessions[sessionKey]; ok && !options.Force {
		m.mu.Unlock()
		return deviceQRStartResult(existing, "feishu device QR authorization is already waiting"), nil
	}
	if _, ok := m.sessions[sessionKey]; !ok && len(m.sessions) >= maxDeviceRegistrationSessions {
		m.mu.Unlock()
		return DeviceQRStartResult{}, errors.New("too many active feishu device QR sessions")
	}
	m.mu.Unlock()

	timeout := timeoutOrDefault(options.Timeout, defaultDeviceRegistrationTimeout)
	initPayload, initStatus, err := m.postForm(ctx, client, map[string]string{"action": "init"}, timeout)
	if err != nil {
		return DeviceQRStartResult{}, err
	}
	if initStatus < http.StatusOK || initStatus >= http.StatusMultipleChoices {
		return DeviceQRStartResult{}, registrationAPIError(initPayload, initStatus, "initialize")
	}
	if methods := deviceStringSlice(initPayload["supported_auth_methods"]); len(methods) > 0 && !deviceContainsFold(methods, "client_secret") {
		return DeviceQRStartResult{}, errors.New("feishu device authorization does not support client_secret")
	}

	beginPayload, beginStatus, err := m.postForm(ctx, client, map[string]string{
		"action":            "begin",
		"archetype":         "PersonalAgent",
		"auth_method":       "client_secret",
		"request_user_info": "open_id",
	}, timeout)
	if err != nil {
		return DeviceQRStartResult{}, err
	}
	if beginStatus < http.StatusOK || beginStatus >= http.StatusMultipleChoices {
		return DeviceQRStartResult{}, registrationAPIError(beginPayload, beginStatus, "begin")
	}
	var decoded deviceRegistrationResponse
	if err := decodeRegistrationResponse(beginPayload, &decoded); err != nil {
		return DeviceQRStartResult{}, err
	}
	deviceCode := strings.TrimSpace(decoded.DeviceCode)
	verificationURL := strings.TrimSpace(decoded.VerificationURIComplete)
	if deviceCode == "" || verificationURL == "" {
		return DeviceQRStartResult{}, errors.New("feishu device authorization returned an incomplete QR payload")
	}
	if err := validateVerificationURL(verificationURL); err != nil {
		return DeviceQRStartResult{}, err
	}

	ttl := m.registrationTTL(time.Duration(decoded.ExpiresIn) * time.Second)
	interval := boundedDevicePollInterval(time.Duration(decoded.Interval) * time.Second)
	session := deviceQRSession{
		SessionKey:      sessionKey,
		DeviceCode:      deviceCode,
		VerificationURL: verificationURL,
		ExpiresAt:       now.Add(ttl),
		NextPollAt:      now,
		PollInterval:    interval,
		BaseURL:         m.baseURL(),
	}
	m.mu.Lock()
	m.purgeExpiredLocked(now)
	m.sessions[sessionKey] = session
	m.mu.Unlock()
	return deviceQRStartResult(session, "feishu device QR authorization started"), nil
}

func (m *DeviceQRLoginManager) Poll(ctx context.Context, client *http.Client, options DeviceQRPollOptions) (DeviceQRPollResult, error) {
	if m == nil {
		return DeviceQRPollResult{}, errors.New("feishu device QR manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = http.DefaultClient
	}
	sessionKey := strings.TrimSpace(options.SessionKey)
	if sessionKey == "" {
		return DeviceQRPollResult{}, errors.New("feishu device QR session key is required")
	}
	now := m.clock().UTC()
	m.mu.Lock()
	m.purgeExpiredLocked(now)
	session, ok := m.sessions[sessionKey]
	if ok && now.Before(session.NextPollAt) {
		m.mu.Unlock()
		return DeviceQRPollResult{Status: DeviceQRStatusWait, Message: "feishu device QR authorization is waiting"}, nil
	}
	if !ok {
		m.mu.Unlock()
		return DeviceQRPollResult{Status: DeviceQRStatusExpired, Message: "feishu device QR authorization expired"}, nil
	}
	session.NextPollAt = now.Add(session.PollInterval)
	m.sessions[sessionKey] = session
	m.mu.Unlock()

	timeout := timeoutOrDefault(options.Timeout, defaultDeviceRegistrationTimeout)
	payload, status, err := m.postForm(ctx, client, map[string]string{
		"action":      "poll",
		"device_code": session.DeviceCode,
	}, timeout)
	if err != nil {
		return DeviceQRPollResult{}, err
	}
	response := registrationResponse(payload)
	switch strings.ToLower(deviceStringValue(response["error"])) {
	case "authorization_pending", "pending":
		return DeviceQRPollResult{Status: DeviceQRStatusWait, Message: "feishu device QR is waiting for confirmation"}, nil
	case "slow_down":
		m.mu.Lock()
		if current, exists := m.sessions[sessionKey]; exists {
			current.PollInterval = boundedDevicePollInterval(current.PollInterval + 5*time.Second)
			m.sessions[sessionKey] = current
		}
		m.mu.Unlock()
		return DeviceQRPollResult{Status: DeviceQRStatusWait, Message: "feishu device QR polling is rate limited"}, nil
	case "access_denied", "expired_token", "expired":
		m.delete(sessionKey)
		return DeviceQRPollResult{Status: DeviceQRStatusExpired, Message: "feishu device QR authorization was not completed"}, nil
	}
	if status >= http.StatusInternalServerError {
		return DeviceQRPollResult{}, registrationAPIError(payload, status, "poll")
	}
	clientID := deviceStringValue(payload["client_id"])
	clientSecret := deviceStringValue(payload["client_secret"])
	if status >= http.StatusBadRequest && deviceStringValue(response["error"]) == "" && (clientID == "" || clientSecret == "") {
		return DeviceQRPollResult{}, registrationAPIError(payload, status, "poll")
	}
	if clientID == "" || clientSecret == "" {
		return DeviceQRPollResult{Status: DeviceQRStatusWait, Message: "feishu device QR is waiting for confirmation"}, nil
	}
	userInfo := deviceMapValue(payload["user_info"])
	return DeviceQRPollResult{
		Status:       DeviceQRStatusConfirmed,
		Connected:    true,
		Message:      "feishu device QR authorization confirmed",
		ClientID:     clientID,
		ClientSecret: clientSecret,
		UserOpenID:   deviceFirstNonEmpty(deviceStringValue(userInfo["open_id"]), deviceStringValue(userInfo["user_id"])),
		UserName:     deviceFirstNonEmpty(deviceStringValue(userInfo["name"]), deviceStringValue(userInfo["display_name"])),
		TenantBrand:  deviceStringValue(userInfo["tenant_brand"]),
		Domain:       session.BaseURL,
	}, nil
}

func (m *DeviceQRLoginManager) postForm(
	ctx context.Context,
	client *http.Client,
	values map[string]string,
	timeout time.Duration,
) (map[string]any, int, error) {
	requestCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	form := url.Values{}
	for key, value := range values {
		form.Set(key, value)
	}
	target, err := url.JoinPath(m.baseURL()+"/", deviceRegistrationPath)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDeviceResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(raw) > maxDeviceResponseBytes {
		return nil, resp.StatusCode, errors.New("feishu device authorization response is too large")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode feishu device authorization response: %w", err)
	}
	return registrationResponse(payload), resp.StatusCode, nil
}

func decodeRegistrationResponse(payload map[string]any, target *deviceRegistrationResponse) error {
	raw, err := json.Marshal(registrationResponse(payload))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func registrationResponse(payload map[string]any) map[string]any {
	if data := deviceMapValue(payload["data"]); len(data) > 0 {
		merged := make(map[string]any, len(payload)+len(data))
		for key, value := range payload {
			merged[key] = value
		}
		for key, value := range data {
			merged[key] = value
		}
		return merged
	}
	return payload
}

func registrationAPIError(payload map[string]any, status int, action string) error {
	response := registrationResponse(payload)
	code := deviceFirstNonEmpty(deviceStringValue(response["error"]), deviceStringValue(response["code"]))
	description := deviceFirstNonEmpty(deviceStringValue(response["error_description"]), deviceStringValue(response["message"]), deviceStringValue(response["error_msg"]))
	if code == "" {
		code = strconv.Itoa(status)
	}
	if description == "" {
		description = "provider returned an invalid response"
	}
	return fmt.Errorf("feishu device authorization %s failed (%s): %s", action, code, description)
}

func deviceQRStartResult(session deviceQRSession, message string) DeviceQRStartResult {
	return DeviceQRStartResult{
		SessionKey:      session.SessionKey,
		VerificationURL: session.VerificationURL,
		ExpiresAt:       session.ExpiresAt,
		Message:         message,
	}
}

func (m *DeviceQRLoginManager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *DeviceQRLoginManager) baseURL() string {
	baseURL := strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	if baseURL == "" {
		return defaultDeviceRegistrationBaseURL
	}
	return baseURL
}

func (m *DeviceQRLoginManager) registrationTTL(value time.Duration) time.Duration {
	if value > 0 {
		return boundedDeviceTTL(value)
	}
	if m.TTL > 0 {
		return boundedDeviceTTL(m.TTL)
	}
	return defaultDeviceRegistrationTTL
}

func (m *DeviceQRLoginManager) purgeExpiredLocked(now time.Time) {
	for key, session := range m.sessions {
		if !session.ExpiresAt.IsZero() && !now.Before(session.ExpiresAt) {
			delete(m.sessions, key)
		}
	}
}

func (m *DeviceQRLoginManager) delete(sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionKey)
}

// Forget removes a confirmed or otherwise finalized device session after the
// caller has durably stored its credentials and pairing state.
func (m *DeviceQRLoginManager) Forget(sessionKey string) {
	if m == nil {
		return
	}
	m.delete(strings.TrimSpace(sessionKey))
}

func boundedDeviceTTL(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultDeviceRegistrationTTL
	}
	if value < time.Minute {
		return time.Minute
	}
	if value > defaultDeviceRegistrationTTL {
		return defaultDeviceRegistrationTTL
	}
	return value
}

func boundedDevicePollInterval(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultDevicePollInterval
	}
	if value < time.Second {
		return time.Second
	}
	if value > time.Minute {
		return time.Minute
	}
	return value
}

func timeoutOrDefault(value time.Duration, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func validateVerificationURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("feishu device authorization returned an invalid verification URL")
	}
	return nil
}

func randomDeviceSessionKey() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "feishu-qr-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("feishu-qr-%d", time.Now().UnixNano())
}

func deviceStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text := deviceStringValue(item); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func deviceContainsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func deviceMapValue(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func deviceStringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func deviceFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
