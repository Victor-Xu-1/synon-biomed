package wechat

import (
	"fmt"
	"mime"
	"path/filepath"
	"strconv"
	"strings"
)

type MessageItem struct {
	Type      int         `json:"type"`
	MsgID     string      `json:"msg_id,omitempty"`
	TextItem  *TextItem   `json:"text_item,omitempty"`
	ImageItem *ImageItem  `json:"image_item,omitempty"`
	FileItem  *FileItem   `json:"file_item,omitempty"`
	VideoItem *VideoItem  `json:"video_item,omitempty"`
	RefMsg    *RefMessage `json:"ref_msg,omitempty"`
}

type TextItem struct {
	Text string `json:"text,omitempty"`
}

type RefMessage struct {
	Title       string       `json:"title,omitempty"`
	MessageItem *MessageItem `json:"message_item,omitempty"`
}

type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

type ImageItem struct {
	Media      *CDNMedia `json:"media,omitempty"`
	ThumbMedia *CDNMedia `json:"thumb_media,omitempty"`
	AESKey     string    `json:"aeskey,omitempty"`
	URL        string    `json:"url,omitempty"`
}

type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	Length   string    `json:"len,omitempty"`
}

type VideoItem struct {
	Media      *CDNMedia `json:"media,omitempty"`
	ThumbMedia *CDNMedia `json:"thumb_media,omitempty"`
}

type MediaCandidate struct {
	Kind              string `json:"kind"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	MimeType          string `json:"mimeType,omitempty"`
	AESKey            string `json:"aesKey,omitempty"`
	EncryptQueryParam string `json:"encryptQueryParam,omitempty"`
}

type Message struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMS int64         `json:"create_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
}

func BuildClientVersion(version string) int {
	parts := strings.Split(version, ".")
	values := [3]int{}
	for i := 0; i < len(parts) && i < len(values); i++ {
		value, err := strconv.Atoi(parts[i])
		if err == nil {
			values[i] = value
		}
	}
	return ((values[0] & 0xff) << 16) | ((values[1] & 0xff) << 8) | (values[2] & 0xff)
}

func ExtractText(items []MessageItem) string {
	for _, item := range items {
		if item.Type == 1 && item.TextItem != nil {
			text := item.TextItem.Text
			if item.RefMsg == nil {
				return text
			}

			var parts []string
			if item.RefMsg.Title != "" {
				parts = append(parts, item.RefMsg.Title)
			}
			if item.RefMsg.MessageItem != nil {
				refBody := ExtractText([]MessageItem{*item.RefMsg.MessageItem})
				if refBody != "" {
					parts = append(parts, refBody)
				}
			}
			if len(parts) == 0 {
				return text
			}
			return fmt.Sprintf("[引用: %s]\n%s", strings.Join(parts, " | "), text)
		}

	}
	return ""
}

func CollectMediaCandidates(items []MessageItem) []MediaCandidate {
	var candidates []MediaCandidate
	for _, item := range items {
		switch item.Type {
		case 2:
			if item.ImageItem == nil {
				continue
			}
			media := firstMedia(item.ImageItem.Media, item.ImageItem.ThumbMedia)
			url := item.ImageItem.URL
			if media != nil && media.FullURL != "" {
				url = media.FullURL
			}
			if url == "" {
				continue
			}
			candidate := MediaCandidate{
				Kind: "image",
				Name: fmt.Sprintf("wechat-image-%s.jpg", fallbackID(item.MsgID, "image")),
				URL:  url,
			}
			if item.ImageItem.AESKey != "" {
				candidate.AESKey = item.ImageItem.AESKey
			}
			if media != nil {
				candidate.EncryptQueryParam = media.EncryptQueryParam
				if candidate.AESKey == "" {
					candidate.AESKey = media.AESKey
				}
			}
			candidates = append(candidates, candidate)
		case 4:
			if item.FileItem == nil || item.FileItem.Media == nil || item.FileItem.Media.FullURL == "" {
				continue
			}
			name := item.FileItem.FileName
			if name == "" {
				name = fmt.Sprintf("wechat-file-%s", fallbackID(item.MsgID, "file"))
			}
			candidates = append(candidates, MediaCandidate{
				Kind:              "file",
				Name:              name,
				URL:               item.FileItem.Media.FullURL,
				MimeType:          mimeTypeForName(name),
				AESKey:            item.FileItem.Media.AESKey,
				EncryptQueryParam: item.FileItem.Media.EncryptQueryParam,
			})
		case 5:
			if item.VideoItem == nil || item.VideoItem.Media == nil || item.VideoItem.Media.FullURL == "" {
				continue
			}
			name := fmt.Sprintf("wechat-video-%s.mp4", fallbackID(item.MsgID, "video"))
			candidates = append(candidates, MediaCandidate{
				Kind:              "file",
				Name:              name,
				URL:               item.VideoItem.Media.FullURL,
				MimeType:          mimeTypeForName(name),
				AESKey:            item.VideoItem.Media.AESKey,
				EncryptQueryParam: item.VideoItem.Media.EncryptQueryParam,
			})
		}
	}
	return candidates
}

func DedupID(message Message) string {
	if message.MessageID != 0 || message.Seq != 0 || message.CreateTimeMS != 0 {
		return fmt.Sprintf("wechat:message:%d:%d:%d", message.MessageID, message.Seq, message.CreateTimeMS)
	}
	if len(message.ItemList) > 0 && message.ItemList[0].MsgID != "" {
		return "wechat:item:" + message.ItemList[0].MsgID
	}
	return "wechat:message:" + message.FromUserID
}

func TaskTitle(text string, media []MediaCandidate) string {
	title := strings.TrimSpace(text)
	if title == "" && len(media) > 0 {
		title = fmt.Sprintf("%d attachment(s)", len(media))
	}
	if title == "" {
		title = "message"
	}
	runes := []rune(title)
	if len(runes) > 160 {
		title = strings.TrimSpace(string(runes[:160]))
	}
	return "WeChat: " + title
}

func firstMedia(candidates ...*CDNMedia) *CDNMedia {
	for _, candidate := range candidates {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

func fallbackID(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func mimeTypeForName(name string) string {
	if detected := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); detected != "" {
		if semicolon := strings.Index(detected, ";"); semicolon >= 0 {
			return detected[:semicolon]
		}
		return detected
	}
	return "application/octet-stream"
}
