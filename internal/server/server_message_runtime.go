package server

import (
	"context"

	"encoding/base64"

	"errors"
	"fmt"

	"strings"

	adaptercommon "synon-go/internal/adapters/common"

	secretstore "synon-go/internal/persistence/secrets"
)

func (s *Server) executeSettingsTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.settingsStore == nil {
		return nil, errors.New("settings store is not configured")
	}
	switch toolName {
	case "Config":
		return s.executeConfigTool(input)
	case "settings_set":
		key := stringValue(input["key"])
		if key == hostGrantsSettingKey || strings.HasPrefix(key, hostGrantsSettingKey+".") {
			return nil, errors.New("host grants must be changed through the host access API")
		}
		setting, err := s.settingsStore.Set(key, input["value"])
		return map[string]any{"setting": setting}, err
	case "settings_get":
		setting, ok, err := s.settingsStore.Get(stringValue(input["key"]))
		if err == nil && !ok {
			err = fmt.Errorf("setting not found: %s", stringValue(input["key"]))
		}
		return map[string]any{"setting": setting}, err
	case "settings_list":
		settings, err := s.settingsStore.List()
		return map[string]any{"settings": settings}, err
	default:
		return nil, fmt.Errorf("unknown settings tool: %s", toolName)
	}
}

func (s *Server) executeConfigTool(input map[string]any) (any, error) {
	settingKey := stringValue(input["setting"])
	settingConfig, ok := supportedConfigSettings()[settingKey]
	if !ok {
		return map[string]any{"success": false, "error": fmt.Sprintf("Unknown setting: %q", settingKey)}, nil
	}
	value, hasValue := input["value"]
	if !hasValue || value == nil {
		setting, found, err := s.settingsStore.Get(configStoreKey(settingKey))
		if err != nil {
			return nil, err
		}
		current := settingConfig.DefaultValue
		if found {
			current = setting.Value
		}
		return map[string]any{"success": true, "operation": "get", "setting": settingKey, "value": current}, nil
	}
	if settingKey == "remoteControlAtStartup" {
		if text, ok := value.(string); ok && strings.EqualFold(strings.TrimSpace(text), "default") {
			if _, err := s.settingsStore.Delete(configStoreKey(settingKey)); err != nil {
				return nil, err
			}
			return map[string]any{
				"success":   true,
				"operation": "set",
				"setting":   settingKey,
				"newValue":  defaultRemoteControlAtStartup(),
			}, nil
		}
	}
	finalValue, err := validateConfigValue(settingKey, settingConfig, value)
	if err != nil {
		return map[string]any{"success": false, "operation": "set", "setting": settingKey, "error": err.Error()}, nil
	}
	storeKey := configStoreKey(settingKey)
	previous, found, err := s.settingsStore.Get(storeKey)
	if err != nil {
		return nil, err
	}
	saved, err := s.settingsStore.Set(storeKey, finalValue)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"success":   true,
		"operation": "set",
		"setting":   settingKey,
		"newValue":  saved.Value,
	}
	if found {
		result["previousValue"] = previous.Value
	}
	return result, nil
}

func configStoreKey(settingKey string) string {
	return "config." + settingKey
}

func defaultRemoteControlAtStartup() bool {
	return false
}

var errIMMessageCallerOwnerUnauthorized = errors.New("im_message caller owner is not authorized")

func (s *Server) executeIMMessageTool(ctx context.Context, input map[string]any) (output any, err error) {
	if err := s.validateRegisteredTool("im_message", input); err != nil {
		return nil, err
	}
	if s.taskStore == nil {
		return nil, errors.New("task store is not configured")
	}
	platform := strings.TrimSpace(stringValue(input["platform"]))
	chatID := firstNonEmpty(stringValue(input["chatId"]), stringValue(input["chat_id"]), stringValue(input["conversationId"]), stringValue(input["conversation_id"]))
	messageID := firstNonEmpty(stringValue(input["messageId"]), stringValue(input["message_id"]), stringValue(input["msgId"]), stringValue(input["msg_id"]))
	messageType := firstNonEmpty(stringValue(input["messageType"]), stringValue(input["message_type"]), stringValue(input["msgtype"]))
	senderID := firstNonEmpty(stringValue(input["senderId"]), stringValue(input["sender_id"]), stringValue(input["senderOpenId"]), stringValue(input["sender_open_id"]), stringValue(input["userId"]), stringValue(input["user_id"]))
	text := strings.TrimSpace(stringValue(input["text"]))
	sourceEventID := firstNonEmpty(stringValue(input["sourceEventId"]), stringValue(input["source_event_id"]), stringValue(input["eventId"]), stringValue(input["event_id"]), stringValue(input["updateId"]), stringValue(input["update_id"]))
	explicitClientMessageID := firstNonEmpty(stringValue(input["clientMessageId"]), stringValue(input["client_message_id"]))
	clientMessageID, err := imMessageDedupID(platform, chatID, explicitClientMessageID, messageID, sourceEventID)
	if err != nil {
		return nil, err
	}
	downloads := input["downloads"]
	if downloads == nil {
		downloads = input["attachments"]
	}
	if platform == "" || chatID == "" || senderID == "" || (text == "" && downloads == nil) {
		return nil, errors.New("im_message platform, chatId, senderId, and text or downloads are required")
	}
	pairing, err := s.checkInboundPairing(platform, senderID)
	if err != nil {
		return nil, err
	}
	var run *sessionRunnerChatRun
	if ctx != nil {
		run, _ = ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	}
	if run != nil && run.Transcript != nil {
		callerOwnerID := strings.TrimSpace(run.Transcript.Stream.OwnerID)
		if callerOwnerID == "" || callerOwnerID != strings.TrimSpace(pairing.OwnerUserID) {
			return nil, errIMMessageCallerOwnerUnauthorized
		}
	} else if s.synonLinkAuth != nil && s.synonLinkAuth.enabled {
		return nil, errIMMessageCallerOwnerUnauthorized
	}
	result := map[string]any{
		"ok":           true,
		"platform":     platform,
		"chatId":       chatID,
		"messageId":    messageID,
		"messageType":  messageType,
		"senderId":     senderID,
		"text":         text,
		"downloads":    downloads,
		"deduplicated": false,
	}
	addNonEmptyResultField(result, "sourceEventId", sourceEventID)
	addNonEmptyResultField(result, "clientMessageId", clientMessageID)
	applyPairingMap(result, pairing)
	if pairing.Blocked {
		return result, nil
	}
	targetType := firstNonEmpty(stringValue(input["targetType"]), stringValue(input["target_type"]))
	targetID := firstNonEmpty(stringValue(input["targetId"]), stringValue(input["target_id"]))
	if targetType == "" || targetID == "" {
		switch platform {
		case "feishu":
			targetType, targetID = "chat", chatID
		case "wechat":
			targetType, targetID = "user", senderID
		default:
			return nil, errors.New("im_message targetType and targetId are required for this platform")
		}
	}
	title := firstNonEmpty(stringValue(input["taskTitle"]), stringValue(input["task_title"]), text, "IM message from "+platform+" "+senderID)
	record := inboundLiveSessionRecord{
		Platform: platform, ChatID: chatID, MessageID: messageID, MessageType: messageType,
		SenderID: senderID, Text: text, Downloads: downloads, SourceEventID: sourceEventID,
		ClientMessageID: clientMessageID, TaskTitle: title, ContextToken: stringValue(input["contextToken"]),
		TargetType: targetType, TargetID: targetID, OwnerUserID: pairing.OwnerUserID,
	}
	fingerprint, err := inboundLiveSessionFingerprint(record)
	if err != nil {
		return nil, err
	}
	if dedup := s.imDedupForPlatform(platform); dedup != nil && clientMessageID != "" {
		reservation, duplicate, acquireErr := dedup.Acquire(ctx, clientMessageID, fingerprint)
		if acquireErr != nil {
			return nil, acquireErr
		}
		if duplicate {
			result["deduplicated"] = true
			return result, nil
		}
		if reservation != nil {
			defer adaptercommon.CompleteMessageDedupReservation(reservation, &err)
		}
	}
	sessionID := imLiveSessionID(platform, chatID)
	task, err := s.ensureInboundTask(sessionID, clientMessageID, title)
	if err != nil {
		return nil, err
	}
	record.TaskID, record.TaskTitle = task.ID, task.Title
	recordedSessionID, journalEventID, err := s.recordInboundLiveSession(record)
	if err != nil {
		rollbackErr := s.rollbackUnprojectedInboundTask(sessionID, clientMessageID, task.ID)
		return nil, errors.Join(err, rollbackErr)
	}
	result["task"] = task
	if recordedSessionID != "" {
		result["sessionId"] = recordedSessionID
		result["journalEventId"] = journalEventID
	}
	return result, nil
}

func (s *Server) imDedupForPlatform(platform string) *adaptercommon.MessageDedup {
	switch strings.TrimSpace(platform) {
	case "feishu":
		return s.feishuDedup
	case "wechat":
		return s.wechatDedup
	default:
		return nil
	}
}

func imMessageDedupID(platform, chatID, clientMessageID, messageID, sourceEventID string) (string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	chatID = strings.TrimSpace(chatID)
	kind, identity := "client", strings.TrimSpace(clientMessageID)
	if identity == "" {
		kind, identity = "message", strings.TrimSpace(messageID)
	}
	if identity == "" {
		kind, identity = "source", strings.TrimSpace(sourceEventID)
	}
	if platform == "" || chatID == "" || identity == "" {
		return "", errors.New("im_message requires a stable clientMessageId, messageId, or sourceEventId")
	}
	if len(platform) > 64 || len(chatID) > 512 || len(identity) > 512 {
		return "", errors.New("im_message identity exceeds size limit")
	}
	encode := base64.RawURLEncoding.EncodeToString
	return "im:" + encode([]byte(platform)) + ":" + encode([]byte(chatID)) + ":" + kind + ":" + encode([]byte(identity)), nil
}

func addNonEmptyResultField(result map[string]any, key string, value string) {
	if value := strings.TrimSpace(value); value != "" {
		result[key] = value
	}
}

func (s *Server) executePairingTool(ctx context.Context, toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s.pairingStore == nil {
		return nil, errors.New("pairing store is not configured")
	}
	switch toolName {
	case "pairing_list":
		users, err := s.pairingStore.List(stringValue(input["platform"]))
		return map[string]any{"users": users}, err
	case "pairing_allow":
		ownerUserID := ""
		if run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun); run != nil && run.Transcript != nil {
			ownerUserID = strings.TrimSpace(run.Transcript.Stream.OwnerID)
		}
		if ownerUserID == "" && (s.synonLinkAuth == nil || !s.synonLinkAuth.enabled) {
			ownerUserID = secretstore.DefaultUserID
		}
		if ownerUserID == "" {
			return nil, errors.New("pairing owner authority is required")
		}
		user, err := s.pairingStore.AllowForOwner(
			stringValue(input["platform"]),
			stringValue(input["userId"]),
			stringValue(input["displayName"]),
			ownerUserID,
		)
		return map[string]any{"user": user}, err
	case "pairing_revoke":
		platform := stringValue(input["platform"])
		userID := stringValue(input["userId"])
		revoked, err := s.pairingStore.Revoke(platform, userID)
		if err == nil && revoked {
			err = s.revokeIMOutboundRoutes(platform, userID)
		}
		return map[string]any{"revoked": revoked}, err
	default:
		return nil, fmt.Errorf("unknown pairing tool: %s", toolName)
	}
}

type configSettingSpec struct {
	Type         string
	Options      []string
	DefaultValue any
}
