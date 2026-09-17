package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultQRLoginTTL     = 5 * time.Minute
	defaultQRLoginTimeout = 35 * time.Second
	defaultQRBotType      = "3"
)

type QRLoginStatus string

const (
	QRStatusWait               QRLoginStatus = "wait"
	QRStatusScanned            QRLoginStatus = "scaned"
	QRStatusConfirmed          QRLoginStatus = "confirmed"
	QRStatusExpired            QRLoginStatus = "expired"
	QRStatusScannedButRedirect QRLoginStatus = "scaned_but_redirect"
	QRStatusNotStarted         QRLoginStatus = "not_started"
)

type QRStartOptions struct {
	Force      bool
	SessionKey string
	BotType    string
	Timeout    time.Duration
}

type QRStartResult struct {
	QRCodeURL  string `json:"qrCodeUrl"`
	Message    string `json:"message"`
	SessionKey string `json:"sessionKey"`
}

type QRPollOptions struct {
	SessionKey string
	Timeout    time.Duration
}

type QRPollResult struct {
	Connected bool          `json:"connected"`
	Status    QRLoginStatus `json:"status"`
	Message   string        `json:"message"`
	BotToken  string        `json:"-"`
	AccountID string        `json:"-"`
	BaseURL   string        `json:"-"`
	UserID    string        `json:"-"`
}

type QRLoginManager struct {
	BaseURL string
	TTL     time.Duration

	mu     sync.Mutex
	logins map[string]activeQRLogin
}

type activeQRLogin struct {
	SessionKey     string
	QRCode         string
	QRCodeURL      string
	CurrentBaseURL string
	StartedAt      time.Time
}

type qrStartResponse struct {
	Ret              int    `json:"ret,omitempty"`
	ErrCode          int    `json:"errcode,omitempty"`
	ErrMsg           string `json:"errmsg,omitempty"`
	QRCode           string `json:"qrcode"`
	QRCodeImageURL   string `json:"qrcode_img_content"`
	QRCodeImageAlias string `json:"qrcode_img_url,omitempty"`
}

type qrStatusResponse struct {
	Ret          int    `json:"ret,omitempty"`
	ErrCode      int    `json:"errcode,omitempty"`
	ErrMsg       string `json:"errmsg,omitempty"`
	Status       string `json:"status"`
	RedirectHost string `json:"redirect_host,omitempty"`
	BotToken     string `json:"bot_token,omitempty"`
	AccountID    string `json:"ilink_bot_id,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	UserID       string `json:"ilink_user_id,omitempty"`
}

func NewQRLoginManager(baseURL string) *QRLoginManager {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	return &QRLoginManager{
		BaseURL: strings.TrimRight(baseURL, "/"),
		TTL:     defaultQRLoginTTL,
		logins:  make(map[string]activeQRLogin),
	}
}

func (m *QRLoginManager) Start(ctx context.Context, client *http.Client, options QRStartOptions) (QRStartResult, error) {
	if m == nil {
		return QRStartResult{}, errors.New("wechat qr login manager is nil")
	}
	now := time.Now()
	sessionKey := strings.TrimSpace(options.SessionKey)
	if sessionKey == "" {
		sessionKey = randomID()
	}

	m.mu.Lock()
	m.purgeExpiredLocked(now)
	if login, ok := m.logins[sessionKey]; ok && !options.Force {
		m.mu.Unlock()
		return QRStartResult{
			QRCodeURL:  login.QRCodeURL,
			Message:    "wechat QR login already started",
			SessionKey: login.SessionKey,
		}, nil
	}
	m.mu.Unlock()

	botType := strings.TrimSpace(options.BotType)
	if botType == "" {
		botType = defaultQRBotType
	}
	raw, err := getJSON(ctx, client, m.BaseURL, "ilink/bot/get_bot_qrcode", map[string]string{
		"bot_type": botType,
	}, timeoutOrDefault(options.Timeout, defaultQRLoginTimeout))
	if err != nil {
		return QRStartResult{}, err
	}
	if err := checkAPIResult(raw, "wechatQRCodeStart"); err != nil {
		return QRStartResult{}, err
	}
	var decoded qrStartResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return QRStartResult{}, err
	}
	qrURL := strings.TrimSpace(decoded.QRCodeImageURL)
	if qrURL == "" {
		qrURL = strings.TrimSpace(decoded.QRCodeImageAlias)
	}
	if strings.TrimSpace(decoded.QRCode) == "" || qrURL == "" {
		return QRStartResult{}, fmt.Errorf("wechat QR start returned incomplete payload: %s", string(raw))
	}

	login := activeQRLogin{
		SessionKey:     sessionKey,
		QRCode:         decoded.QRCode,
		QRCodeURL:      qrURL,
		CurrentBaseURL: m.BaseURL,
		StartedAt:      now,
	}
	m.mu.Lock()
	m.logins[sessionKey] = login
	m.mu.Unlock()

	return QRStartResult{
		QRCodeURL:  qrURL,
		Message:    "wechat QR login started",
		SessionKey: sessionKey,
	}, nil
}

func (m *QRLoginManager) Poll(ctx context.Context, client *http.Client, options QRPollOptions) (QRPollResult, error) {
	if m == nil {
		return QRPollResult{}, errors.New("wechat qr login manager is nil")
	}
	sessionKey := strings.TrimSpace(options.SessionKey)
	if sessionKey == "" {
		return QRPollResult{}, errors.New("wechat QR session key is required")
	}

	m.mu.Lock()
	m.purgeExpiredLocked(time.Now())
	login, ok := m.logins[sessionKey]
	m.mu.Unlock()
	if !ok {
		return QRPollResult{Status: QRStatusNotStarted, Message: "wechat QR login has not started"}, nil
	}

	raw, err := getJSON(ctx, client, login.CurrentBaseURL, "ilink/bot/get_qrcode_status", map[string]string{
		"qrcode": login.QRCode,
	}, timeoutOrDefault(options.Timeout, defaultQRLoginTimeout))
	if err != nil {
		return QRPollResult{}, err
	}
	if err := checkAPIResult(raw, "wechatQRCodePoll"); err != nil {
		return QRPollResult{}, err
	}
	var decoded qrStatusResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return QRPollResult{}, err
	}

	status := QRLoginStatus(strings.TrimSpace(decoded.Status))
	switch status {
	case QRStatusWait, QRStatusScanned:
		return QRPollResult{Status: status, Message: qrStatusMessage(status)}, nil
	case QRStatusScannedButRedirect:
		nextBaseURL := baseURLFromRedirectHost(decoded.RedirectHost)
		if nextBaseURL != "" {
			login.CurrentBaseURL = nextBaseURL
			m.mu.Lock()
			m.logins[sessionKey] = login
			m.mu.Unlock()
		}
		return QRPollResult{Status: status, Message: qrStatusMessage(status), BaseURL: nextBaseURL}, nil
	case QRStatusExpired:
		m.delete(sessionKey)
		return QRPollResult{Status: status, Message: qrStatusMessage(status)}, nil
	case QRStatusConfirmed:
		baseURL := strings.TrimSpace(decoded.BaseURL)
		if baseURL == "" {
			baseURL = login.CurrentBaseURL
		}
		return QRPollResult{
			Connected: decoded.BotToken != "" && decoded.AccountID != "",
			Status:    status,
			Message:   qrStatusMessage(status),
			BotToken:  decoded.BotToken,
			AccountID: decoded.AccountID,
			BaseURL:   strings.TrimRight(baseURL, "/"),
			UserID:    decoded.UserID,
		}, nil
	default:
		return QRPollResult{}, fmt.Errorf("wechat QR poll returned unknown status %q", decoded.Status)
	}
}

func (m *QRLoginManager) delete(sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.logins, sessionKey)
}

// Forget removes a confirmed or otherwise finalized QR session after the
// caller has durably stored its credentials and pairing state.
func (m *QRLoginManager) Forget(sessionKey string) {
	if m == nil {
		return
	}
	m.delete(strings.TrimSpace(sessionKey))
}

func (m *QRLoginManager) purgeExpiredLocked(now time.Time) {
	ttl := m.TTL
	if ttl <= 0 {
		ttl = defaultQRLoginTTL
	}
	for key, login := range m.logins {
		if now.Sub(login.StartedAt) > ttl {
			delete(m.logins, key)
		}
	}
}

func getJSON(ctx context.Context, client *http.Client, baseURL string, endpoint string, query map[string]string, timeout time.Duration) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	requestCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	target, err := url.JoinPath(strings.TrimRight(baseURL, "/")+"/", endpoint)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	values := parsed.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	parsed.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("iLink-App-Id", iLinkAppID)
	req.Header.Set("iLink-App-ClientVersion", fmt.Sprintf("%d", BuildClientVersion(ChannelVersion)))
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("wechat %s failed: %s: %s", endpoint, resp.Status, string(data))
	}
	return data, nil
}

func baseURLFromRedirectHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimRight(host, "/")
	}
	return "https://" + strings.TrimRight(host, "/")
}

func qrStatusMessage(status QRLoginStatus) string {
	switch status {
	case QRStatusWait:
		return "waiting for QR scan"
	case QRStatusScanned:
		return "QR code scanned, waiting for confirmation"
	case QRStatusScannedButRedirect:
		return "QR code scanned, redirected to another WeChat gateway"
	case QRStatusConfirmed:
		return "wechat QR login confirmed"
	case QRStatusExpired:
		return "wechat QR code expired"
	default:
		return string(status)
	}
}
