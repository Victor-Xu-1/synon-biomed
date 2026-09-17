package feishu

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	adaptercommon "synon-go/internal/adapters/common"
)

const (
	FeishuImageMaxBytes = 10 * 1024 * 1024
	FeishuFileMaxBytes  = 100 * 1024 * 1024
)

type MediaClient struct {
	client         *http.Client
	baseURL        string
	getTenantToken TenantTokenProvider
}

type OutboundMediaDispatcher struct {
	Media                 *MediaClient
	HTTPClient            *http.Client
	AllowPrivateImageURLs bool
	ImageMaxBytes         int64
	FileMaxBytes          int64

	uploadedImagesByChat map[string]map[string]string
}

type AttachmentLimitError struct {
	Kind  PendingUploadKind
	Size  int64
	Limit int64
}

func (e *AttachmentLimitError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s attachment is too large: %d > %d", e.Kind, e.Size, e.Limit)
}

func NewMediaClient(client *http.Client, baseURL string, getTenantToken TenantTokenProvider) *MediaClient {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultOpenAPIBaseURL
	}
	return &MediaClient{
		client:         client,
		baseURL:        strings.TrimRight(baseURL, "/"),
		getTenantToken: getTenantToken,
	}
}

func (c *MediaClient) UploadImage(ctx context.Context, buffer []byte, mimeType string) (string, error) {
	resp, err := c.multipartRequest(ctx, "/open-apis/im/v1/images", map[string]string{
		"image_type": "message",
	}, "image", imageFileName(mimeType), buffer)
	if err != nil {
		return "", err
	}
	imageKey := stringValue(resp.Data["image_key"])
	if imageKey == "" {
		imageKey = stringValue(resp.Raw["image_key"])
	}
	if imageKey == "" {
		return "", &CardKitAPIError{API: "im.image.create", Code: -1, Msg: "response missing image_key"}
	}
	return imageKey, nil
}

func (c *MediaClient) UploadFile(ctx context.Context, buffer []byte, fileName string) (string, error) {
	if strings.TrimSpace(fileName) == "" {
		fileName = "attachment.bin"
	}
	resp, err := c.multipartRequest(ctx, "/open-apis/im/v1/files", map[string]string{
		"file_type": detectFeishuFileType(fileName),
		"file_name": fileName,
	}, "file", fileName, buffer)
	if err != nil {
		return "", err
	}
	fileKey := stringValue(resp.Data["file_key"])
	if fileKey == "" {
		fileKey = stringValue(resp.Raw["file_key"])
	}
	if fileKey == "" {
		return "", &CardKitAPIError{API: "im.file.create", Code: -1, Msg: "response missing file_key"}
	}
	return fileKey, nil
}

func (c *MediaClient) SendImageMessage(ctx context.Context, chatID string, imageKey string) (string, error) {
	return c.sendMediaMessage(ctx, chatID, "image", map[string]string{"image_key": imageKey})
}

func (c *MediaClient) SendFileMessage(ctx context.Context, chatID string, fileKey string) (string, error) {
	return c.sendMediaMessage(ctx, chatID, "file", map[string]string{"file_key": fileKey})
}

func (c *MediaClient) sendMediaMessage(ctx context.Context, chatID string, msgType string, content map[string]string) (string, error) {
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("receive_id_type", "chat_id")
	resp, err := c.jsonRequest(ctx, http.MethodPost, "/open-apis/im/v1/messages", query, map[string]any{
		"receive_id": chatID,
		"msg_type":   msgType,
		"content":    string(contentJSON),
	})
	if err != nil {
		return "", err
	}
	messageID := stringValue(resp.Data["message_id"])
	if messageID == "" {
		return "", &CardKitAPIError{API: "im.message", Code: -1, Msg: "response missing message_id"}
	}
	return messageID, nil
}

func (c *MediaClient) multipartRequest(ctx context.Context, path string, fields map[string]string, fileField string, fileName string, fileData []byte) (cardKitResponse, error) {
	if c == nil {
		return cardKitResponse{}, errors.New("feishu media client is nil")
	}
	if c.getTenantToken == nil {
		return cardKitResponse{}, errors.New("feishu tenant token provider is required")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return cardKitResponse{}, err
		}
	}
	part, err := writer.CreateFormFile(fileField, filepath.Base(fileName))
	if err != nil {
		return cardKitResponse{}, err
	}
	if _, err := part.Write(fileData); err != nil {
		return cardKitResponse{}, err
	}
	if err := writer.Close(); err != nil {
		return cardKitResponse{}, err
	}
	return c.doRequest(ctx, http.MethodPost, path, nil, writer.FormDataContentType(), &body)
}

func (c *MediaClient) jsonRequest(ctx context.Context, method string, path string, query url.Values, body map[string]any) (cardKitResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return cardKitResponse{}, err
	}
	return c.doRequest(ctx, method, path, query, "application/json", bytes.NewReader(raw))
}

func (c *MediaClient) doRequest(ctx context.Context, method string, path string, query url.Values, contentType string, body io.Reader) (cardKitResponse, error) {
	if c == nil {
		return cardKitResponse{}, errors.New("feishu media client is nil")
	}
	if c.getTenantToken == nil {
		return cardKitResponse{}, errors.New("feishu tenant token provider is required")
	}
	token, err := c.getTenantToken(ctx)
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
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return cardKitResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return cardKitResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return cardKitResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cardKitResponse{}, &CardKitAPIError{API: path, Code: resp.StatusCode, Msg: string(data)}
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

func NewOutboundMediaDispatcher(media *MediaClient) *OutboundMediaDispatcher {
	return &OutboundMediaDispatcher{
		Media:                media,
		HTTPClient:           http.DefaultClient,
		ImageMaxBytes:        FeishuImageMaxBytes,
		FileMaxBytes:         FeishuFileMaxBytes,
		uploadedImagesByChat: map[string]map[string]string{},
	}
}

func (d *OutboundMediaDispatcher) DispatchImage(ctx context.Context, chatID string, pending PendingUpload) error {
	if d == nil {
		return errors.New("feishu outbound media dispatcher is nil")
	}
	if pending.Kind != PendingUploadImage {
		return fmt.Errorf("pending upload kind %q is not image", pending.Kind)
	}
	if d.imageAlreadySent(chatID, pending.ID) {
		return nil
	}
	buffer, mimeType, err := d.loadImage(ctx, pending.Source)
	if err != nil {
		return err
	}
	if err := checkOutboundMediaSize(PendingUploadImage, int64(len(buffer)), d.imageLimit()); err != nil {
		return err
	}
	imageKey, err := d.Media.UploadImage(ctx, buffer, mimeType)
	if err != nil {
		return err
	}
	if _, err := d.Media.SendImageMessage(ctx, chatID, imageKey); err != nil {
		return err
	}
	d.rememberImage(chatID, pending.ID, imageKey)
	return nil
}

func (d *OutboundMediaDispatcher) DispatchFile(ctx context.Context, chatID string, pending PendingUpload) error {
	if d == nil {
		return errors.New("feishu outbound media dispatcher is nil")
	}
	if pending.Kind != PendingUploadFile {
		return fmt.Errorf("pending upload kind %q is not file", pending.Kind)
	}
	buffer, fileName, err := d.loadFile(pending)
	if err != nil {
		return err
	}
	if err := checkOutboundMediaSize(PendingUploadFile, int64(len(buffer)), d.fileLimit()); err != nil {
		return err
	}
	fileKey, err := d.Media.UploadFile(ctx, buffer, fileName)
	if err != nil {
		return err
	}
	_, err = d.Media.SendFileMessage(ctx, chatID, fileKey)
	return err
}

func (d *OutboundMediaDispatcher) loadImage(ctx context.Context, source UploadSource) ([]byte, string, error) {
	switch source.Kind {
	case UploadSourceBase64:
		buffer, err := base64.StdEncoding.DecodeString(source.Data)
		if err != nil {
			return nil, "", err
		}
		return buffer, defaultString(source.MIME, "image/png"), nil
	case UploadSourcePath:
		if !isSafeOutboundLocalPath(source.Path) {
			return nil, "", fmt.Errorf("unsafe image path: %s", source.Path)
		}
		if err := checkPathSize(source.Path, PendingUploadImage, d.imageLimit()); err != nil {
			return nil, "", err
		}
		buffer, err := os.ReadFile(source.Path)
		if err != nil {
			return nil, "", err
		}
		return buffer, guessFeishuMIME(source.Path, PendingUploadImage), nil
	case UploadSourceURL:
		return d.fetchImage(ctx, source.URL)
	default:
		return nil, "", fmt.Errorf("unsupported image source kind %q", source.Kind)
	}
}

func (d *OutboundMediaDispatcher) loadFile(pending PendingUpload) ([]byte, string, error) {
	switch pending.Source.Kind {
	case UploadSourceBase64:
		buffer, err := base64.StdEncoding.DecodeString(pending.Source.Data)
		if err != nil {
			return nil, "", err
		}
		return buffer, defaultString(strings.TrimSpace(pending.Alt), "attachment.bin"), nil
	case UploadSourcePath:
		if !isSafeOutboundLocalPath(pending.Source.Path) {
			return nil, "", fmt.Errorf("unsafe file path: %s", pending.Source.Path)
		}
		if err := checkPathSize(pending.Source.Path, PendingUploadFile, d.fileLimit()); err != nil {
			return nil, "", err
		}
		buffer, err := os.ReadFile(pending.Source.Path)
		if err != nil {
			return nil, "", err
		}
		fileName := filepath.Base(pending.Source.Path)
		if strings.TrimSpace(fileName) == "" || fileName == "." {
			fileName = defaultString(strings.TrimSpace(pending.Alt), "attachment.bin")
		}
		return buffer, fileName, nil
	case UploadSourceURL:
		return nil, "", fmt.Errorf("remote file URL is not supported: %s", pending.Source.URL)
	default:
		return nil, "", fmt.Errorf("unsupported file source kind %q", pending.Source.Kind)
	}
}

func (d *OutboundMediaDispatcher) fetchImage(ctx context.Context, rawURL string) ([]byte, string, error) {
	if _, err := adaptercommon.ValidateOutboundMediaURL(rawURL, d.AllowPrivateImageURLs); err != nil {
		return nil, "", err
	}
	client, err := adaptercommon.OutboundMediaHTTPClient(d.HTTPClient, d.AllowPrivateImageURLs)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("image fetch failed: %s", resp.Status)
	}
	limit := d.imageLimit() + 1
	buffer, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, "", err
	}
	if err := checkOutboundMediaSize(PendingUploadImage, int64(len(buffer)), d.imageLimit()); err != nil {
		return nil, "", err
	}
	mimeType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" {
		mimeType = "image/png"
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return nil, "", fmt.Errorf("remote image content-type is not image: %s", mimeType)
	}
	return buffer, mimeType, nil
}

func (d *OutboundMediaDispatcher) imageLimit() int64 {
	if d != nil && d.ImageMaxBytes > 0 {
		return d.ImageMaxBytes
	}
	return FeishuImageMaxBytes
}

func (d *OutboundMediaDispatcher) fileLimit() int64 {
	if d != nil && d.FileMaxBytes > 0 {
		return d.FileMaxBytes
	}
	return FeishuFileMaxBytes
}

func (d *OutboundMediaDispatcher) imageAlreadySent(chatID string, pendingID string) bool {
	if pendingID == "" {
		return false
	}
	if d.uploadedImagesByChat == nil {
		d.uploadedImagesByChat = map[string]map[string]string{}
	}
	_, ok := d.uploadedImagesByChat[chatID][pendingID]
	return ok
}

func (d *OutboundMediaDispatcher) rememberImage(chatID string, pendingID string, imageKey string) {
	if pendingID == "" {
		return
	}
	if d.uploadedImagesByChat == nil {
		d.uploadedImagesByChat = map[string]map[string]string{}
	}
	chatCache := d.uploadedImagesByChat[chatID]
	if chatCache == nil {
		chatCache = map[string]string{}
		d.uploadedImagesByChat[chatID] = chatCache
	}
	chatCache[pendingID] = imageKey
}

func checkPathSize(path string, kind PendingUploadKind, limit int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return checkOutboundMediaSize(kind, info.Size(), limit)
}

func checkOutboundMediaSize(kind PendingUploadKind, size int64, limit int64) error {
	if limit <= 0 {
		return nil
	}
	if size > limit {
		return &AttachmentLimitError{Kind: kind, Size: size, Limit: limit}
	}
	return nil
}

func detectFeishuFileType(fileName string) string {
	switch strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".") {
	case "opus":
		return "opus"
	case "mp4":
		return "mp4"
	case "pdf":
		return "pdf"
	case "doc", "docx":
		return "doc"
	case "xls", "xlsx":
		return "xls"
	case "ppt", "pptx":
		return "ppt"
	default:
		return "stream"
	}
}

func guessFeishuMIME(fileName string, kind PendingUploadKind) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".")
	if kind == PendingUploadImage {
		switch ext {
		case "jpg", "jpeg":
			return "image/jpeg"
		case "gif":
			return "image/gif"
		case "webp":
			return "image/webp"
		case "heic":
			return "image/heic"
		case "svg":
			return "image/svg+xml"
		default:
			return "image/png"
		}
	}
	switch ext {
	case "pdf":
		return "application/pdf"
	case "doc":
		return "application/msword"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "ppt":
		return "application/vnd.ms-powerpoint"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case "txt":
		return "text/plain"
	case "json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

func imageFileName(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return "image.jpg"
	case "image/gif":
		return "image.gif"
	case "image/webp":
		return "image.webp"
	case "image/svg+xml":
		return "image.svg"
	default:
		return "image.png"
	}
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
