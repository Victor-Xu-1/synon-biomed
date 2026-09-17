package feishu

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type PendingDownload struct {
	Kind     string `json:"kind"`
	FileKey  string `json:"fileKey"`
	FileName string `json:"fileName,omitempty"`
}

type InboundPayload struct {
	Text             string            `json:"text"`
	PendingDownloads []PendingDownload `json:"pendingDownloads"`
}

type WebhookEvent struct {
	Challenge string
	Inbound   *InboundEvent
}

type InboundEvent struct {
	EventID      string            `json:"eventId"`
	MessageID    string            `json:"messageId"`
	ChatID       string            `json:"chatId"`
	ChatType     string            `json:"chatType"`
	SenderOpenID string            `json:"senderOpenId"`
	MessageType  string            `json:"messageType"`
	Text         string            `json:"text"`
	Downloads    []PendingDownload `json:"downloads"`
	TaskTitle    string            `json:"taskTitle"`
	DedupID      string            `json:"dedupId"`
}

var mentionPattern = regexp.MustCompile(`@_user_\d+`)

func ParseWebhookEvent(reader io.Reader) (WebhookEvent, error) {
	var root map[string]any
	if err := json.NewDecoder(reader).Decode(&root); err != nil {
		return WebhookEvent{}, err
	}
	if stringValue(root["type"]) == "url_verification" {
		challenge := strings.TrimSpace(stringValue(root["challenge"]))
		if challenge == "" {
			return WebhookEvent{}, errors.New("feishu challenge is required")
		}
		return WebhookEvent{Challenge: challenge}, nil
	}

	eventType := stringValue(valueAt(root, "header", "event_type"))
	if eventType != "" && eventType != "im.message.receive_v1" {
		return WebhookEvent{}, fmt.Errorf("unsupported feishu event type: %s", eventType)
	}
	eventMap, ok := mapValue(root["event"])
	if !ok {
		eventMap = root
	}
	messageMap, ok := mapValue(eventMap["message"])
	if !ok {
		return WebhookEvent{}, errors.New("feishu message event is required")
	}
	inbound, err := inboundFromEvent(root, eventMap, messageMap)
	if err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{Inbound: &inbound}, nil
}

func inboundFromEvent(root map[string]any, eventMap map[string]any, messageMap map[string]any) (InboundEvent, error) {
	messageID := strings.TrimSpace(stringValue(messageMap["message_id"]))
	chatID := strings.TrimSpace(stringValue(messageMap["chat_id"]))
	chatType := strings.TrimSpace(stringValue(messageMap["chat_type"]))
	messageType := strings.TrimSpace(stringValue(messageMap["message_type"]))
	content := stringValue(messageMap["content"])
	senderOpenID := strings.TrimSpace(stringValue(valueAt(eventMap, "sender", "sender_id", "open_id")))
	eventID := strings.TrimSpace(stringValue(valueAt(root, "header", "event_id")))
	if eventID == "" {
		eventID = messageID
	}
	if messageID == "" || chatID == "" || senderOpenID == "" || content == "" || messageType == "" {
		return InboundEvent{}, errors.New("feishu message_id, chat_id, sender open_id, content, and message_type are required")
	}

	payload := ExtractInboundPayload(content, messageType)
	text := StripMentions(payload.Text)
	return InboundEvent{
		EventID:      eventID,
		MessageID:    messageID,
		ChatID:       chatID,
		ChatType:     chatType,
		SenderOpenID: senderOpenID,
		MessageType:  messageType,
		Text:         text,
		Downloads:    payload.PendingDownloads,
		TaskTitle:    NormalizeTaskTitle(text, payload.PendingDownloads),
		DedupID:      "feishu:message:" + messageID,
	}, nil
}

func ExtractInboundPayload(content string, msgType string) InboundPayload {
	parsed, ok := parseMessageContent(content)
	if !ok {
		return InboundPayload{PendingDownloads: []PendingDownload{}}
	}

	switch msgType {
	case "text":
		return InboundPayload{
			Text:             stringValue(parsed["text"]),
			PendingDownloads: []PendingDownload{},
		}
	case "image":
		imageKey := firstString(parsed["image_key"], parsed["imageKey"], parsed["file_key"])
		text := firstString(parsed["text"], parsed["caption"], parsed["title"])
		if imageKey != "" {
			return InboundPayload{
				Text:             text,
				PendingDownloads: []PendingDownload{{Kind: "image", FileKey: imageKey}},
			}
		}
	case "file", "file_archive":
		fileKey := firstString(parsed["file_key"], parsed["fileKey"])
		if fileKey != "" {
			return InboundPayload{
				Text: stringValue(parsed["text"]),
				PendingDownloads: []PendingDownload{{
					Kind:     "file",
					FileKey:  fileKey,
					FileName: firstString(parsed["file_name"], parsed["fileName"]),
				}},
			}
		}
	case "post":
		return extractPostPayload(parsed)
	}

	downloads := make([]PendingDownload, 0)
	text := collectGenericPayload(parsed, &downloads)
	if text == "" {
		text = stringValue(parsed["text"])
	}
	if imageKey := firstString(parsed["image_key"], parsed["imageKey"]); imageKey != "" {
		return InboundPayload{Text: text, PendingDownloads: []PendingDownload{{Kind: "image", FileKey: imageKey}}}
	}
	if fileKey := firstString(parsed["file_key"], parsed["fileKey"]); fileKey != "" {
		return InboundPayload{Text: text, PendingDownloads: []PendingDownload{{
			Kind:     "file",
			FileKey:  fileKey,
			FileName: firstString(parsed["file_name"], parsed["fileName"]),
		}}}
	}
	return InboundPayload{Text: text, PendingDownloads: uniqueDownloads(downloads)}
}

func PostContentMentionsBot(content string, botOpenID string) bool {
	parsed, ok := parseMessageContent(content)
	if !ok {
		return false
	}
	for _, paragraph := range getPostParagraphs(parsed) {
		if postTreeMentionsBot(paragraph, botOpenID) {
			return true
		}
	}
	return false
}

func StripMentions(text string) string {
	return strings.TrimSpace(mentionPattern.ReplaceAllString(text, ""))
}

func NormalizeTaskTitle(text string, downloads []PendingDownload) string {
	title := strings.TrimSpace(text)
	if title == "" && len(downloads) > 0 {
		title = fmt.Sprintf("%d attachment(s)", len(downloads))
	}
	if title == "" {
		title = "message"
	}
	runes := []rune(title)
	if len(runes) > 160 {
		title = strings.TrimSpace(string(runes[:160]))
	}
	return "Feishu: " + title
}

func extractPostPayload(parsed map[string]any) InboundPayload {
	paragraphs := getPostParagraphs(parsed)
	paraTexts := make([]string, 0, len(paragraphs))
	downloads := make([]PendingDownload, 0)
	for _, paragraph := range paragraphs {
		nodes, ok := paragraph.([]any)
		if !ok {
			nodes = []any{paragraph}
		}
		paraDownloads := make([]PendingDownload, 0)
		parts := make([]string, 0, len(nodes))
		for _, node := range nodes {
			if text := collectPostNode(node, &paraDownloads); text != "" {
				parts = append(parts, text)
			}
		}
		if joined := strings.Join(parts, ""); joined != "" {
			paraTexts = append(paraTexts, joined)
		}
		downloads = append(downloads, paraDownloads...)
	}
	return InboundPayload{
		Text:             strings.Join(paraTexts, "\n\n"),
		PendingDownloads: uniqueDownloads(downloads),
	}
}

func parseMessageContent(content string) (map[string]any, bool) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

func getPostParagraphs(parsed map[string]any) []any {
	if value := valueAt(parsed, "zh_cn", "content"); value != nil {
		if paragraphs, ok := value.([]any); ok {
			return paragraphs
		}
	}
	if value := valueAt(parsed, "en_us", "content"); value != nil {
		if paragraphs, ok := value.([]any); ok {
			return paragraphs
		}
	}
	if value, ok := parsed["content"].([]any); ok {
		return value
	}
	return []any{}
}

func collectPostNode(node any, downloads *[]PendingDownload) string {
	if group, ok := node.([]any); ok {
		parts := make([]string, 0, len(group))
		for _, child := range group {
			if text := collectPostNode(child, downloads); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	}
	record, ok := mapValue(node)
	if !ok {
		return ""
	}
	if collectDirectDownloads(record, downloads) {
		return ""
	}
	tag := stringValue(record["tag"])
	ownText := getNodeText(record)
	separator := ""
	if tag == "list" || tag == "list-item" {
		separator = "\n"
	}
	children := make([]string, 0)
	for _, group := range childNodeGroups(record) {
		childTexts := make([]string, 0)
		for _, child := range group {
			if text := collectPostNode(child, downloads); text != "" {
				childTexts = append(childTexts, text)
			}
		}
		if len(childTexts) > 0 {
			children = append(children, strings.Join(childTexts, separator))
		}
	}
	return ownText + strings.Join(children, separator)
}

func collectGenericPayload(node any, downloads *[]PendingDownload) string {
	if group, ok := node.([]any); ok {
		parts := make([]string, 0, len(group))
		for _, child := range group {
			if text := collectGenericPayload(child, downloads); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	record, ok := mapValue(node)
	if !ok {
		return ""
	}
	consumed := collectDirectDownloads(record, downloads)
	ownText := ""
	if !consumed {
		ownText = getNodeText(record)
	}
	children := make([]string, 0)
	for _, group := range childNodeGroups(record) {
		if text := collectGenericPayload(group, downloads); text != "" {
			children = append(children, text)
		}
	}
	return strings.Join(appendNonEmpty([]string{ownText}, children...), "\n")
}

func collectDirectDownloads(record map[string]any, downloads *[]PendingDownload) bool {
	tag := stringValue(record["tag"])
	imageKey := firstString(record["image_key"], record["imageKey"], record["file_key"])
	fileKey := firstString(record["file_key"], record["fileKey"])
	fileName := firstString(record["file_name"], record["fileName"], record["name"])
	if imageKey != "" && (tag == "img" || tag == "image" || tag == "media" || tag == "picture" || tag == "") {
		*downloads = append(*downloads, PendingDownload{Kind: "image", FileKey: imageKey, FileName: fileName})
		return true
	}
	if fileKey != "" && (tag == "file" || tag == "attachment" || tag == "media") {
		*downloads = append(*downloads, PendingDownload{Kind: "file", FileKey: fileKey, FileName: fileName})
		return true
	}
	return false
}

func getNodeText(record map[string]any) string {
	tag := stringValue(record["tag"])
	if tag == "text" || tag == "md" || tag == "a" {
		if direct := firstString(record["text"], record["content"]); direct != "" {
			return direct
		}
	}
	if tag != "" && tag != "at" {
		if direct := firstString(record["text"], record["content"]); direct != "" {
			return direct
		}
	}
	if data, ok := mapValue(record["data"]); ok {
		if inner := firstString(data["text"], data["content"]); inner != "" {
			return inner
		}
	}
	return ""
}

func childNodeGroups(record map[string]any) [][]any {
	groups := make([][]any, 0)
	for _, key := range []string{"children", "elements", "content"} {
		if group, ok := record[key].([]any); ok {
			groups = append(groups, group)
		}
	}
	if data, ok := mapValue(record["data"]); ok {
		for _, key := range []string{"children", "elements", "content"} {
			if group, ok := data[key].([]any); ok {
				groups = append(groups, group)
			}
		}
	}
	return groups
}

func postTreeMentionsBot(node any, botOpenID string) bool {
	if group, ok := node.([]any); ok {
		for _, child := range group {
			if postTreeMentionsBot(child, botOpenID) {
				return true
			}
		}
		return false
	}
	record, ok := mapValue(node)
	if !ok {
		return false
	}
	if postMentionMatchesNode(record, botOpenID) {
		return true
	}
	for _, group := range childNodeGroups(record) {
		for _, child := range group {
			if postTreeMentionsBot(child, botOpenID) {
				return true
			}
		}
	}
	return false
}

func postMentionMatchesNode(record map[string]any, botOpenID string) bool {
	tag := stringValue(record["tag"])
	if tag != "at" && tag != "mention" {
		return false
	}
	ids := make(map[string]struct{})
	for _, value := range []any{record["open_id"], record["user_id"], record["union_id"], record["key"]} {
		if text := strings.TrimSpace(stringValue(value)); text != "" {
			ids[text] = struct{}{}
		}
	}
	if id, ok := mapValue(record["id"]); ok {
		for _, value := range []any{id["open_id"], id["user_id"], id["union_id"]} {
			if text := strings.TrimSpace(stringValue(value)); text != "" {
				ids[text] = struct{}{}
			}
		}
	}
	if botOpenID != "" {
		if _, ok := ids[botOpenID]; ok {
			return true
		}
	}
	return len(ids) == 0 && (record["key"] != nil || record["name"] != nil || record["user_name"] != nil)
}

func uniqueDownloads(downloads []PendingDownload) []PendingDownload {
	seen := make(map[string]struct{})
	result := make([]PendingDownload, 0, len(downloads))
	for _, download := range downloads {
		key := download.Kind + ":" + download.FileKey
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, download)
	}
	return result
}

func firstString(values ...any) string {
	for _, value := range values {
		if text := stringValue(value); text != "" {
			return text
		}
	}
	return ""
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func mapValue(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	record, ok := value.(map[string]any)
	return record, ok
}

func valueAt(root map[string]any, path ...string) any {
	var current any = root
	for _, key := range path {
		record, ok := mapValue(current)
		if !ok {
			return nil
		}
		current = record[key]
	}
	return current
}

func appendNonEmpty(head []string, tail ...string) []string {
	result := make([]string, 0, len(head)+len(tail))
	for _, value := range append(head, tail...) {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}
