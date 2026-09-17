package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
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
	DefaultBaseURL      = "https://ilinkai.weixin.qq.com"
	ChannelVersion      = "2.1.7"
	iLinkAppID          = "bot"
	defaultAPITimeout   = 15 * time.Second
	defaultPollTimeout  = 35 * time.Second
	authorizationHeader = "ilink_bot_token"
)

type GetUpdatesOptions struct {
	GetUpdatesBuf string
	Timeout       time.Duration
}

type GetUpdatesResponse struct {
	Ret                  int       `json:"ret,omitempty"`
	ErrCode              int       `json:"errcode,omitempty"`
	ErrMsg               string    `json:"errmsg,omitempty"`
	Messages             []Message `json:"msgs,omitempty"`
	GetUpdatesBuf        string    `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMS int       `json:"longpolling_timeout_ms,omitempty"`
}

type SendTextOptions struct {
	To           string
	Text         string
	ContextToken string
	Timeout      time.Duration
}

type PollerOptions struct {
	GetUpdatesBuf string
	Interval      time.Duration
	Timeout       time.Duration
}

type PollHandler func(context.Context, Message) error

type Poller struct {
	Client   *http.Client
	BaseURL  string
	Token    string
	Options  PollerOptions
	Interval time.Duration
	Handler  PollHandler

	mu            sync.Mutex
	getUpdatesBuf string
}

type baseInfo struct {
	ChannelVersion string `json:"channel_version"`
}

func NewPoller(client *http.Client, baseURL string, token string, options PollerOptions, handler PollHandler) *Poller {
	interval := options.Interval
	if interval <= 0 {
		interval = time.Second
	}
	return &Poller{
		Client:        client,
		BaseURL:       baseURL,
		Token:         token,
		Options:       options,
		Interval:      interval,
		Handler:       handler,
		getUpdatesBuf: options.GetUpdatesBuf,
	}
}

func (p *Poller) Run(ctx context.Context) error {
	if p == nil {
		return errors.New("wechat poller is nil")
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := p.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		timer := time.NewTimer(timeoutOrDefault(p.Interval, time.Second))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
}

func (p *Poller) RunOnce(ctx context.Context) (GetUpdatesResponse, error) {
	if p == nil {
		return GetUpdatesResponse{}, errors.New("wechat poller is nil")
	}
	p.mu.Lock()
	buffer := p.getUpdatesBuf
	p.mu.Unlock()

	resp, err := GetUpdates(ctx, p.Client, p.BaseURL, p.Token, GetUpdatesOptions{
		GetUpdatesBuf: buffer,
		Timeout:       p.Options.Timeout,
	})
	if err != nil {
		return GetUpdatesResponse{}, err
	}
	for _, message := range resp.Messages {
		if p.Handler != nil {
			if err := p.Handler(ctx, message); err != nil {
				return GetUpdatesResponse{}, err
			}
		}
	}
	p.mu.Lock()
	p.getUpdatesBuf = resp.GetUpdatesBuf
	p.mu.Unlock()
	return resp, nil
}

func (p *Poller) GetUpdatesBuf() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.getUpdatesBuf
}

func GetUpdates(ctx context.Context, client *http.Client, baseURL string, token string, options GetUpdatesOptions) (GetUpdatesResponse, error) {
	body := map[string]any{
		"get_updates_buf": options.GetUpdatesBuf,
		"base_info":       buildBaseInfo(),
	}
	raw, err := postJSON(ctx, client, baseURL, "ilink/bot/getupdates", token, body, timeoutOrDefault(options.Timeout, defaultPollTimeout))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return GetUpdatesResponse{Messages: []Message{}, GetUpdatesBuf: options.GetUpdatesBuf}, nil
		}
		return GetUpdatesResponse{}, err
	}
	var decoded GetUpdatesResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return GetUpdatesResponse{}, err
	}
	if decoded.Messages == nil {
		decoded.Messages = []Message{}
	}
	if err := checkAPIResult(raw, "wechatGetUpdates"); err != nil {
		return GetUpdatesResponse{}, err
	}
	return decoded, nil
}

func SendText(ctx context.Context, client *http.Client, baseURL string, token string, options SendTextOptions) error {
	if strings.TrimSpace(options.To) == "" {
		return errors.New("wechat target user is required")
	}
	if strings.TrimSpace(options.Text) == "" {
		return errors.New("wechat message text is required")
	}
	message := Message{
		ToUserID:     options.To,
		ClientID:     "synon-wechat-" + randomID(),
		MessageType:  2,
		MessageState: 2,
		ContextToken: options.ContextToken,
		ItemList: []MessageItem{
			{Type: 1, TextItem: &TextItem{Text: options.Text}},
		},
	}
	raw, err := postJSON(ctx, client, baseURL, "ilink/bot/sendmessage", token, map[string]any{
		"msg":       message,
		"base_info": buildBaseInfo(),
	}, timeoutOrDefault(options.Timeout, defaultAPITimeout))
	if err != nil {
		return err
	}
	return checkAPIResult(raw, "wechatSendMessage")
}

func buildBaseInfo() baseInfo {
	return baseInfo{ChannelVersion: ChannelVersion}
}

func postJSON(ctx context.Context, client *http.Client, baseURL string, endpoint string, token string, payload any, timeout time.Duration) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
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
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", authorizationHeader)
	req.Header.Set("iLink-App-Id", iLinkAppID)
	req.Header.Set("iLink-App-ClientVersion", fmt.Sprintf("%d", BuildClientVersion(ChannelVersion)))
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(raw)))
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
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

func checkAPIResult(raw []byte, label string) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil
	}
	code := 0
	if ret, ok := record["ret"].(float64); ok {
		code = int(ret)
	} else if errCode, ok := record["errcode"].(float64); ok {
		code = int(errCode)
	}
	if code == 0 {
		return nil
	}
	message, _ := record["errmsg"].(string)
	if message == "" {
		message = string(raw)
	}
	return fmt.Errorf("%s returned %d: %s", label, code, message)
}

func timeoutOrDefault(value time.Duration, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func randomWechatUIN() string {
	return base64.StdEncoding.EncodeToString([]byte(randomID()))
}

func randomID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", data[:])
}
