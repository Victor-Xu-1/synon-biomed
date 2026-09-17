package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	DefaultOpenAPIBaseURL = "https://open.feishu.cn"
	StreamingElementID    = "streaming_content"

	cardRateLimitedCode     = 230020
	cardContentFailedCode   = 230099
	cardElementLimitSubCode = 11310
)

type TenantTokenProvider func(context.Context) (string, error)

type CardKitClient struct {
	client         *http.Client
	baseURL        string
	getTenantToken TenantTokenProvider
}

type CardKitAPIError struct {
	API  string
	Code int
	Msg  string
}

func (e *CardKitAPIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("cardkit %s failed: code=%d, msg=%s", e.API, e.Code, e.Msg)
}

func NewCardKitClient(client *http.Client, baseURL string, getTenantToken TenantTokenProvider) *CardKitClient {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultOpenAPIBaseURL
	}
	return &CardKitClient{
		client:         client,
		baseURL:        strings.TrimRight(baseURL, "/"),
		getTenantToken: getTenantToken,
	}
}

func (c *CardKitClient) CreateCardEntity(ctx context.Context, card map[string]any) (string, error) {
	resp, err := c.request(ctx, http.MethodPost, "/open-apis/cardkit/v1/cards", nil, map[string]any{
		"type": "card_json",
		"data": mustCardJSONString(card),
	})
	if err != nil {
		return "", err
	}
	cardID := stringValue(resp.Data["card_id"])
	if cardID == "" {
		cardID = stringValue(resp.Raw["card_id"])
	}
	if cardID == "" {
		return "", &CardKitAPIError{API: "card.create", Code: -1, Msg: "response missing card_id"}
	}
	return cardID, nil
}

func (c *CardKitClient) SendCardAsMessage(ctx context.Context, chatID string, cardID string, replyToMessageID string) (string, error) {
	content := mustCardJSONString(map[string]any{
		"type": "card",
		"data": map[string]any{"card_id": cardID},
	})
	body := map[string]any{
		"msg_type": "interactive",
		"content":  content,
	}
	path := ""
	query := url.Values{}
	if strings.TrimSpace(replyToMessageID) != "" {
		path = "/open-apis/im/v1/messages/" + url.PathEscape(replyToMessageID) + "/reply"
	} else {
		path = "/open-apis/im/v1/messages"
		query.Set("receive_id_type", "chat_id")
		body["receive_id"] = chatID
	}
	resp, err := c.request(ctx, http.MethodPost, path, query, body)
	if err != nil {
		return "", err
	}
	messageID := stringValue(resp.Data["message_id"])
	if messageID == "" {
		return "", &CardKitAPIError{API: "im.message", Code: -1, Msg: "response missing message_id"}
	}
	return messageID, nil
}

func (c *CardKitClient) SendRenderedCardAsMessage(ctx context.Context, chatID string, card map[string]any, replyToMessageID string) (string, error) {
	return c.SendRenderedCardAsMessageTo(ctx, "chat_id", chatID, card, replyToMessageID)
}

func (c *CardKitClient) SendRenderedCardAsMessageTo(ctx context.Context, receiveIDType string, receiveID string, card map[string]any, replyToMessageID string) (string, error) {
	body := map[string]any{
		"msg_type": "interactive",
		"content":  mustCardJSONString(card),
	}
	path := ""
	query := url.Values{}
	if strings.TrimSpace(replyToMessageID) != "" {
		path = "/open-apis/im/v1/messages/" + url.PathEscape(replyToMessageID) + "/reply"
	} else {
		path = "/open-apis/im/v1/messages"
		receiveIDType = strings.TrimSpace(receiveIDType)
		receiveID = strings.TrimSpace(receiveID)
		switch receiveIDType {
		case "chat_id", "open_id", "user_id", "union_id", "email":
		default:
			return "", fmt.Errorf("unsupported Feishu receive_id_type %q", receiveIDType)
		}
		if receiveID == "" {
			return "", errors.New("feishu receive id is required")
		}
		query.Set("receive_id_type", receiveIDType)
		body["receive_id"] = receiveID
	}
	resp, err := c.request(ctx, http.MethodPost, path, query, body)
	if err != nil {
		return "", err
	}
	messageID := stringValue(resp.Data["message_id"])
	if messageID == "" {
		return "", &CardKitAPIError{API: "im.message", Code: -1, Msg: "response missing message_id"}
	}
	return messageID, nil
}

func (c *CardKitClient) PatchMessageCard(ctx context.Context, messageID string, card map[string]any) error {
	_, err := c.request(ctx, http.MethodPatch, "/open-apis/im/v1/messages/"+url.PathEscape(messageID), nil, map[string]any{
		"content": mustCardJSONString(card),
	})
	return err
}

func (c *CardKitClient) StreamCardContent(ctx context.Context, cardID string, elementID string, content string, sequence int64) error {
	if strings.TrimSpace(elementID) == "" {
		elementID = StreamingElementID
	}
	_, err := c.request(ctx, http.MethodPut, "/open-apis/cardkit/v1/cards/"+url.PathEscape(cardID)+"/elements/"+url.PathEscape(elementID)+"/content", nil, map[string]any{
		"content":  content,
		"sequence": sequence,
	})
	return err
}

func (c *CardKitClient) SetCardStreamingMode(ctx context.Context, cardID string, streamingMode bool, sequence int64) error {
	settings := mustCardJSONString(map[string]any{"streaming_mode": streamingMode})
	_, err := c.request(ctx, http.MethodPut, "/open-apis/cardkit/v1/cards/"+url.PathEscape(cardID)+"/settings", nil, map[string]any{
		"settings": settings,
		"sequence": sequence,
	})
	return err
}

func (c *CardKitClient) UpdateCardKitCard(ctx context.Context, cardID string, card map[string]any, sequence int64) error {
	_, err := c.request(ctx, http.MethodPut, "/open-apis/cardkit/v1/cards/"+url.PathEscape(cardID), nil, map[string]any{
		"card": map[string]any{
			"type": "card_json",
			"data": mustCardJSONString(card),
		},
		"sequence": sequence,
	})
	return err
}

type cardKitResponse struct {
	Code int            `json:"code"`
	Msg  string         `json:"msg"`
	Data map[string]any `json:"data"`
	Raw  map[string]any
}

func (c *CardKitClient) request(ctx context.Context, method string, path string, query url.Values, body map[string]any) (cardKitResponse, error) {
	if c == nil {
		return cardKitResponse{}, errors.New("feishu cardkit client is nil")
	}
	if c.getTenantToken == nil {
		return cardKitResponse{}, errors.New("feishu tenant token provider is required")
	}
	token, err := c.getTenantToken(ctx)
	if err != nil {
		return cardKitResponse{}, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return cardKitResponse{}, err
	}
	target, err := url.JoinPath(strings.TrimRight(c.baseURL, "/")+"/", strings.TrimPrefix(path, "/"))
	if err != nil {
		return cardKitResponse{}, err
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return cardKitResponse{}, err
	}
	if query != nil {
		parsed.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), bytes.NewReader(raw))
	if err != nil {
		return cardKitResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	httpResp, err := client.Do(req)
	if err != nil {
		return cardKitResponse{}, err
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return cardKitResponse{}, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return cardKitResponse{}, &CardKitAPIError{API: path, Code: httpResp.StatusCode, Msg: string(data)}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cardKitResponse{Data: map[string]any{}, Raw: map[string]any{}}, nil
	}
	var decoded cardKitResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return cardKitResponse{}, err
	}
	var rawMap map[string]any
	_ = json.Unmarshal(data, &rawMap)
	if decoded.Data == nil {
		decoded.Data = map[string]any{}
	}
	decoded.Raw = rawMap
	if decoded.Code != 0 {
		return cardKitResponse{}, &CardKitAPIError{API: path, Code: decoded.Code, Msg: decoded.Msg}
	}
	return decoded, nil
}

var cardSubCodePattern = regexp.MustCompile(`ErrCode:\s*(\d+)`)

func ExtractCardSubCode(message string) int {
	match := cardSubCodePattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return 0
	}
	var code int
	if _, err := fmt.Sscanf(match[1], "%d", &code); err != nil {
		return 0
	}
	return code
}

func IsCardRateLimitError(err error) bool {
	var apiErr *CardKitAPIError
	return errors.As(err, &apiErr) && apiErr.Code == cardRateLimitedCode
}

func IsCardTableLimitError(err error) bool {
	var apiErr *CardKitAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == cardContentFailedCode &&
		ExtractCardSubCode(apiErr.Msg) == cardElementLimitSubCode &&
		strings.Contains(strings.ToLower(apiErr.Msg), "table number over limit")
}

func mustCardJSONString(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
