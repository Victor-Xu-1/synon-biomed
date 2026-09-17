package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	adapterfeishu "synon-go/internal/adapters/feishu"
	adapterqrimage "synon-go/internal/adapters/qrimage"
	adapterwechat "synon-go/internal/adapters/wechat"
	secretstore "synon-go/internal/persistence/secrets"
)

const (
	messageChannelRuntimeNamespace       = "message-channels"
	messageChannelSecretPrefix           = "_synon_internal_message_channel_"
	messageChannelSecretProvider         = "_synon_internal_message_channel"
	messageChannelMaxSessionKeyLength    = 256
	messageChannelMaxDisplayNameLength   = 256
	messageChannelMaxCredentialValueSize = 4096
)

type messageChannelQRBinding struct {
	Platform    string
	SessionKey  string
	OwnerUserID string
}

type messageChannelQRStartRequest struct {
	SessionKey string `json:"sessionKey"`
	Force      bool   `json:"force"`
	BotType    string `json:"botType,omitempty"`
	TimeoutMS  int64  `json:"timeoutMs,omitempty"`
}

type messageChannelQRPollRequest struct {
	SessionKey string `json:"sessionKey"`
	TimeoutMS  int64  `json:"timeoutMs,omitempty"`
}

type messageChannelStatus struct {
	Configured bool `json:"configured"`
	Paired     bool `json:"paired"`
}

type messageChannelUnpairRequest struct {
	Channel string `json:"channel"`
}

type messageChannelUnpairResult struct {
	Unpaired           bool `json:"unpaired"`
	PairedUsersRevoked int  `json:"pairedUsersRevoked"`
	RestartScheduled   bool `json:"restartScheduled"`
}

func (s *Server) handleMessageChannelStatuses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "message channel status only supports GET")
		return
	}
	ownerUserID := secretUserID(r)
	feishuPaired, err := s.messageChannelPaired("feishu", ownerUserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "MESSAGE_CHANNEL_STATUS_UNAVAILABLE", "message channel pairing state is unavailable")
		return
	}
	wechatPaired, err := s.messageChannelPaired("wechat", ownerUserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "MESSAGE_CHANNEL_STATUS_UNAVAILABLE", "message channel pairing state is unavailable")
		return
	}
	feishuConfigured, err := s.messageChannelConfigured("feishu", ownerUserID, feishuPaired)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "MESSAGE_CHANNEL_STATUS_UNAVAILABLE", "message channel configuration state is unavailable")
		return
	}
	wechatConfigured, err := s.messageChannelConfigured("wechat", ownerUserID, wechatPaired)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "MESSAGE_CHANNEL_STATUS_UNAVAILABLE", "message channel configuration state is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": map[string]messageChannelStatus{
			"feishu": {Configured: feishuConfigured, Paired: feishuPaired},
			"wechat": {Configured: wechatConfigured, Paired: wechatPaired},
		},
	})
}

func (s *Server) handleMessageChannelUnpair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "message channel unpair only supports POST")
		return
	}
	var body messageChannelUnpairRequest
	if err := decodeMessageChannelJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
		return
	}
	platform := strings.ToLower(strings.TrimSpace(body.Channel))
	if !isSupportedMessageChannel(platform) {
		writeError(w, http.StatusBadRequest, "MESSAGE_CHANNEL_UNSUPPORTED", "Unsupported message channel")
		return
	}
	result, err := s.unpairMessageChannel(platform, secretUserID(r))
	if err != nil {
		log.Printf("message channel %s unpair failed: %v", platform, err)
		writeError(w, http.StatusInternalServerError, "MESSAGE_CHANNEL_UNPAIR_FAILED", "Unable to cancel message channel pairing")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) unpairMessageChannel(platform, ownerUserID string) (messageChannelUnpairResult, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	ownerUserID = strings.TrimSpace(ownerUserID)
	if !isSupportedMessageChannel(platform) {
		return messageChannelUnpairResult{}, errors.New("unsupported message channel")
	}
	if ownerUserID == "" {
		return messageChannelUnpairResult{}, errors.New("message channel owner is required")
	}
	if s.pairingStore == nil {
		return messageChannelUnpairResult{}, errors.New("message channel pairing storage is not configured")
	}

	users, err := s.pairingStore.List(platform)
	if err != nil {
		return messageChannelUnpairResult{}, err
	}
	result := messageChannelUnpairResult{}
	cleanupErrors := make([]error, 0)
	for _, user := range users {
		if strings.TrimSpace(user.OwnerUserID) != ownerUserID {
			continue
		}
		revoked, revokeErr := s.pairingStore.Revoke(platform, user.UserID)
		if revokeErr != nil {
			cleanupErrors = append(cleanupErrors, revokeErr)
			continue
		}
		if !revoked {
			continue
		}
		result.PairedUsersRevoked++
		if routeErr := s.revokeIMOutboundRoutes(platform, user.UserID); routeErr != nil {
			cleanupErrors = append(cleanupErrors, routeErr)
		}
	}

	if s.secretStore != nil {
		deleted, deleteErr := s.secretStore.DeleteForUser(messageChannelSecretID(platform, ownerUserID), ownerUserID)
		if deleteErr != nil {
			cleanupErrors = append(cleanupErrors, deleteErr)
		} else if deleted {
			result.Unpaired = true
		}
	}
	if s.runtimeStore != nil {
		deleted, deleteErr := s.runtimeStore.Delete(messageChannelRuntimeNamespace, messageChannelRuntimeKey(platform, ownerUserID))
		if deleteErr != nil {
			cleanupErrors = append(cleanupErrors, deleteErr)
		} else if deleted {
			result.Unpaired = true
		}
	}
	if result.PairedUsersRevoked > 0 {
		result.Unpaired = true
	}
	s.releaseMessageChannelQRForOwner(platform, ownerUserID)
	if cleanupErr := errors.Join(cleanupErrors...); cleanupErr != nil {
		return result, cleanupErr
	}
	if result.Unpaired && s.restartRuntime != nil {
		if err := s.restartRuntime("message_channel_unpair:" + platform); err != nil {
			return result, err
		}
		result.RestartScheduled = true
	}
	return result, nil
}

func isSupportedMessageChannel(platform string) bool {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "feishu", "wechat":
		return true
	default:
		return false
	}
}

func (s *Server) messageChannelConfigured(platform, ownerUserID string, paired bool) (bool, error) {
	if paired {
		return true, nil
	}
	for _, diagnostic := range s.adapterDiagnostics.Platforms {
		if strings.EqualFold(strings.TrimSpace(diagnostic.Name), platform) && diagnostic.Configured {
			return true, nil
		}
	}
	if s.secretStore == nil {
		return false, nil
	}
	secret, found, err := s.secretStore.ResolveForUser(messageChannelSecretID(platform, ownerUserID), ownerUserID)
	if err != nil {
		return false, err
	}
	return found && secret.Provider == messageChannelSecretProvider, nil
}

func (s *Server) handleFeishuQRStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Feishu QR start only supports POST")
		return
	}
	var body messageChannelQRStartRequest
	if err := decodeMessageChannelJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
		return
	}
	ownerUserID := secretUserID(r)
	if err := s.claimMessageChannelQR("feishu", ownerUserID, body.SessionKey, body.Force); err != nil {
		writeError(w, http.StatusConflict, "MESSAGE_CHANNEL_SESSION_CONFLICT", err.Error())
		return
	}
	result, err := s.feishuDeviceQR.Start(r.Context(), s.httpClient, adapterfeishu.DeviceQRStartOptions{
		Force:      body.Force,
		SessionKey: strings.TrimSpace(body.SessionKey),
		Timeout:    milliseconds(body.TimeoutMS),
	})
	if err != nil {
		s.releaseMessageChannelQR("feishu", ownerUserID, body.SessionKey)
		writeError(w, http.StatusBadRequest, "MESSAGE_CHANNEL_QR_START_FAILED", safeMessageChannelError("Feishu", err))
		return
	}
	qrCodeURL, err := adapterfeishu.EncodeDeviceVerificationURL(result.VerificationURL, 256)
	if err != nil {
		s.releaseMessageChannelQR("feishu", ownerUserID, result.SessionKey)
		writeError(w, http.StatusBadGateway, "MESSAGE_CHANNEL_QR_INVALID", "Feishu returned an invalid verification URL")
		return
	}
	s.bindMessageChannelQR("feishu", ownerUserID, result.SessionKey)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelFeishuQRStartProjection(result, qrCodeURL)})
}

func (s *Server) handleFeishuQRPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Feishu QR poll only supports POST")
		return
	}
	var body messageChannelQRPollRequest
	if err := decodeMessageChannelJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
		return
	}
	_, ownerUserID, ok := s.authorizeMessageChannelPoll(r, "feishu", body.SessionKey)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRErrorProjection("Feishu QR session is not available")})
		return
	}
	result, err := s.feishuDeviceQR.Poll(r.Context(), s.httpClient, adapterfeishu.DeviceQRPollOptions{
		SessionKey: strings.TrimSpace(body.SessionKey),
		Timeout:    milliseconds(body.TimeoutMS),
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRErrorProjection(safeMessageChannelError("Feishu", err))})
		return
	}
	if result.Status == adapterfeishu.DeviceQRStatusConfirmed {
		if err := s.persistMessageChannelConnection("feishu", ownerUserID, result.UserOpenID, result.UserName, map[string]string{
			"client_id":     result.ClientID,
			"client_secret": result.ClientSecret,
			"domain":        result.Domain,
			"open_id":       result.UserOpenID,
			"tenant_brand":  result.TenantBrand,
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRErrorProjection(safeMessageChannelError("Feishu", err))})
			return
		}
		s.feishuDeviceQR.Forget(strings.TrimSpace(body.SessionKey))
		s.releaseMessageChannelQR("feishu", ownerUserID, body.SessionKey)
		result.Connected = true
		result.Message = s.messageChannelPairingNotice("feishu")
	}
	if result.Status == adapterfeishu.DeviceQRStatusExpired || result.Status == adapterfeishu.DeviceQRStatusError {
		s.releaseMessageChannelQR("feishu", ownerUserID, body.SessionKey)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRPollProjection(string(result.Status), result.Connected, result.Message)})
}

func (s *Server) handleWeChatQRStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "WeChat QR start only supports POST")
		return
	}
	var body messageChannelQRStartRequest
	if err := decodeMessageChannelJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
		return
	}
	ownerUserID := secretUserID(r)
	if err := s.claimMessageChannelQR("wechat", ownerUserID, body.SessionKey, body.Force); err != nil {
		writeError(w, http.StatusConflict, "MESSAGE_CHANNEL_SESSION_CONFLICT", err.Error())
		return
	}
	result, err := s.wechatQR.Start(r.Context(), s.httpClient, adapterwechat.QRStartOptions{
		Force:      body.Force,
		SessionKey: strings.TrimSpace(body.SessionKey),
		BotType:    body.BotType,
		Timeout:    milliseconds(body.TimeoutMS),
	})
	if err != nil {
		s.releaseMessageChannelQR("wechat", ownerUserID, body.SessionKey)
		writeError(w, http.StatusBadRequest, "MESSAGE_CHANNEL_QR_START_FAILED", safeMessageChannelError("WeChat", err))
		return
	}
	qrCodeURL, err := adapterqrimage.EncodeURL(result.QRCodeURL, 256)
	if err != nil {
		s.releaseMessageChannelQR("wechat", ownerUserID, result.SessionKey)
		writeError(w, http.StatusBadGateway, "MESSAGE_CHANNEL_QR_INVALID", "WeChat returned an invalid QR URL")
		return
	}
	s.bindMessageChannelQR("wechat", ownerUserID, result.SessionKey)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRStartProjection(result, qrCodeURL)})
}

func (s *Server) handleWeChatQRPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "WeChat QR poll only supports POST")
		return
	}
	var body messageChannelQRPollRequest
	if err := decodeMessageChannelJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid JSON body")
		return
	}
	_, ownerUserID, ok := s.authorizeMessageChannelPoll(r, "wechat", body.SessionKey)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRErrorProjection("WeChat QR session is not available")})
		return
	}
	result, err := s.wechatQR.Poll(r.Context(), s.httpClient, adapterwechat.QRPollOptions{
		SessionKey: strings.TrimSpace(body.SessionKey),
		Timeout:    milliseconds(body.TimeoutMS),
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRErrorProjection(safeMessageChannelError("WeChat", err))})
		return
	}
	status := normalizedMessageChannelQRStatus(string(result.Status))
	if status == "confirmed" {
		if err := s.persistMessageChannelConnection("wechat", ownerUserID, result.UserID, "", map[string]string{
			"account_id": result.AccountID,
			"base_url":   result.BaseURL,
			"bot_token":  result.BotToken,
			"user_id":    result.UserID,
		}); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRErrorProjection(safeMessageChannelError("WeChat", err))})
			return
		}
		s.wechatQR.Forget(body.SessionKey)
		s.releaseMessageChannelQR("wechat", ownerUserID, body.SessionKey)
		result.Connected = true
		result.Message = s.messageChannelPairingNotice("wechat")
	}
	if status == "expired" || status == "error" {
		s.releaseMessageChannelQR("wechat", ownerUserID, body.SessionKey)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": messageChannelQRPollProjection(status, result.Connected, result.Message)})
}

func (s *Server) persistMessageChannelConnection(platform, ownerUserID, userID, displayName string, credentials map[string]string) error {
	platform = strings.ToLower(strings.TrimSpace(platform))
	ownerUserID = strings.TrimSpace(ownerUserID)
	userID = strings.TrimSpace(userID)
	displayName = strings.TrimSpace(displayName)
	if ownerUserID == "" || userID == "" {
		return errors.New("message channel identity is incomplete")
	}
	if len(displayName) > messageChannelMaxDisplayNameLength {
		displayName = displayName[:messageChannelMaxDisplayNameLength]
	}
	for key, value := range credentials {
		if len(strings.TrimSpace(value)) > messageChannelMaxCredentialValueSize {
			return errors.New("message channel credential is too large")
		}
		credentials[key] = strings.TrimSpace(value)
	}
	if s.secretStore == nil || s.pairingStore == nil {
		return errors.New("protected message channel storage is not configured")
	}
	paired, found, err := s.pairingStore.Get(platform, userID)
	if err != nil {
		return err
	}
	if found && paired.OwnerUserID != "" && paired.OwnerUserID != ownerUserID {
		return errors.New("message channel account is already paired to another owner")
	}

	secretID := messageChannelSecretID(platform, ownerUserID)
	previous, hadPrevious, err := s.secretStore.ResolveForUser(secretID, ownerUserID)
	if err != nil {
		return err
	}
	if hadPrevious {
		_, err = s.secretStore.UpdateForUser(secretID, ownerUserID, func(secret *secretstore.Secret) error {
			secret.Provider = messageChannelSecretProvider
			secret.Name = platform + " message channel"
			secret.Credentials = cloneMessageChannelCredentials(credentials)
			secret.Value = ""
			return nil
		})
	} else {
		_, err = s.secretStore.Create(secretstore.Secret{
			ID:          secretID,
			UserID:      ownerUserID,
			Provider:    messageChannelSecretProvider,
			Name:        platform + " message channel",
			Credentials: cloneMessageChannelCredentials(credentials),
			Description: "Protected credentials created by message-channel QR pairing",
		})
	}
	if err != nil {
		return err
	}
	rollbackSecret := func() {
		if hadPrevious {
			_, _ = s.secretStore.UpdateForUser(secretID, ownerUserID, func(secret *secretstore.Secret) error {
				*secret = previous
				return nil
			})
			return
		}
		_, _ = s.secretStore.DeleteForUser(secretID, ownerUserID)
	}

	runtimeKey := messageChannelRuntimeKey(platform, ownerUserID)
	if s.runtimeStore == nil {
		rollbackSecret()
		return errors.New("message channel runtime storage is not configured")
	}
	if _, err := s.runtimeStore.Set(messageChannelRuntimeNamespace, runtimeKey, map[string]any{
		"platform":           platform,
		"ownerUserId":        ownerUserID,
		"credentialSecretId": secretID,
		"pairedUserId":       userID,
		"requiresRestart":    true,
		"updatedAt":          time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		rollbackSecret()
		return err
	}
	if _, err := s.pairingStore.AllowForOwner(platform, userID, displayName, ownerUserID); err != nil {
		_, _ = s.runtimeStore.Delete(messageChannelRuntimeNamespace, runtimeKey)
		rollbackSecret()
		return err
	}
	return nil
}

func (s *Server) messageChannelPairingNotice(platform string) string {
	if s.restartRuntime == nil {
		return platform + " paired; restart Synon Biomed to activate message delivery"
	}
	if err := s.restartRuntime("message_channel_pairing:" + strings.ToLower(strings.TrimSpace(platform))); err != nil {
		log.Printf("message channel %s paired but runtime restart was not scheduled: %v", platform, err)
		return platform + " paired; restart is required before message delivery can start"
	}
	return platform + " paired; runtime restart scheduled"
}

func (s *Server) messageChannelPaired(platform, ownerUserID string) (bool, error) {
	if s.pairingStore == nil {
		return false, nil
	}
	users, err := s.pairingStore.List(platform)
	if err != nil {
		return false, err
	}
	for _, user := range users {
		if strings.TrimSpace(user.OwnerUserID) == strings.TrimSpace(ownerUserID) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) claimMessageChannelQR(platform, ownerUserID, sessionKey string, force bool) error {
	platform = strings.ToLower(strings.TrimSpace(platform))
	ownerUserID = strings.TrimSpace(ownerUserID)
	sessionKey = strings.TrimSpace(sessionKey)
	if len(sessionKey) > messageChannelMaxSessionKeyLength {
		return errors.New("message channel session key is too long")
	}
	if strings.ContainsAny(sessionKey, "\r\n") {
		return errors.New("message channel session key is invalid")
	}
	key := messageChannelBindingKey(platform, sessionKey)
	s.messageChannelQRMu.Lock()
	defer s.messageChannelQRMu.Unlock()
	if current, ok := s.messageChannelQRBindings[key]; ok && current.OwnerUserID != ownerUserID {
		return errors.New("message channel QR session belongs to another owner")
	}
	if force {
		delete(s.messageChannelQRBindings, key)
	}
	return nil
}

func (s *Server) bindMessageChannelQR(platform, ownerUserID, sessionKey string) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	sessionKey = strings.TrimSpace(sessionKey)
	s.messageChannelQRMu.Lock()
	s.messageChannelQRBindings[messageChannelBindingKey(platform, sessionKey)] = messageChannelQRBinding{
		Platform: platform, SessionKey: sessionKey, OwnerUserID: strings.TrimSpace(ownerUserID),
	}
	s.messageChannelQRMu.Unlock()
}

func (s *Server) releaseMessageChannelQR(platform, ownerUserID, sessionKey string) {
	key := messageChannelBindingKey(platform, sessionKey)
	s.messageChannelQRMu.Lock()
	if binding, ok := s.messageChannelQRBindings[key]; ok && binding.OwnerUserID == strings.TrimSpace(ownerUserID) {
		delete(s.messageChannelQRBindings, key)
	}
	s.messageChannelQRMu.Unlock()
}

func (s *Server) releaseMessageChannelQRForOwner(platform, ownerUserID string) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	ownerUserID = strings.TrimSpace(ownerUserID)
	s.messageChannelQRMu.Lock()
	bindings := make([]messageChannelQRBinding, 0)
	for key, binding := range s.messageChannelQRBindings {
		if strings.EqualFold(binding.Platform, platform) && strings.TrimSpace(binding.OwnerUserID) == ownerUserID {
			bindings = append(bindings, binding)
			delete(s.messageChannelQRBindings, key)
		}
	}
	s.messageChannelQRMu.Unlock()
	for _, binding := range bindings {
		switch strings.ToLower(strings.TrimSpace(binding.Platform)) {
		case "feishu":
			if s.feishuDeviceQR != nil {
				s.feishuDeviceQR.Forget(binding.SessionKey)
			}
		case "wechat":
			if s.wechatQR != nil {
				s.wechatQR.Forget(binding.SessionKey)
			}
		}
	}
}

func (s *Server) authorizeMessageChannelPoll(r *http.Request, platform, sessionKey string) (messageChannelQRBinding, string, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	ownerUserID := secretUserID(r)
	s.messageChannelQRMu.Lock()
	binding, ok := s.messageChannelQRBindings[messageChannelBindingKey(platform, sessionKey)]
	s.messageChannelQRMu.Unlock()
	if !ok || binding.OwnerUserID != ownerUserID {
		return messageChannelQRBinding{}, ownerUserID, false
	}
	return binding, ownerUserID, true
}

func messageChannelQRStartProjection(result adapterwechat.QRStartResult, qrCodeURL string) map[string]any {
	projection := map[string]any{
		"sessionKey": result.SessionKey,
		"qrCodeUrl":  qrCodeURL,
		"message":    result.Message,
	}
	return projection
}

func messageChannelFeishuQRStartProjection(result adapterfeishu.DeviceQRStartResult, qrCodeURL string) map[string]any {
	projection := map[string]any{
		"sessionKey": result.SessionKey,
		"qrCodeUrl":  qrCodeURL,
		"message":    result.Message,
	}
	if !result.ExpiresAt.IsZero() {
		projection["expiresAt"] = result.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return projection
}

func messageChannelQRPollProjection(status string, connected bool, message string) map[string]any {
	return map[string]any{"status": normalizedMessageChannelQRStatus(status), "connected": connected && normalizedMessageChannelQRStatus(status) == "confirmed", "message": strings.TrimSpace(message)}
}

func messageChannelQRErrorProjection(message string) map[string]any {
	return map[string]any{"status": "error", "connected": false, "message": strings.TrimSpace(message)}
}

func normalizedMessageChannelQRStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "wait", "waiting", "authorization_pending", "scaned_but_redirect":
		return "wait"
	case "scaned", "scanned", "scan":
		return "scanned"
	case "confirmed", "connected", "success":
		return "confirmed"
	case "expired", "expired_token":
		return "expired"
	default:
		return "error"
	}
}

func decodeMessageChannelJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return decoder.Decode(target)
}

func safeMessageChannelError(platform string, err error) string {
	if err == nil {
		return platform + " QR pairing failed"
	}
	message := strings.TrimSpace(err.Error())
	for _, sensitive := range []string{"bot_token", "access_token", "app_secret", "authorization", "qrcode"} {
		if strings.Contains(strings.ToLower(message), sensitive) {
			return platform + " QR pairing failed; the provider response was rejected"
		}
	}
	if len(message) > 256 {
		message = message[:256] + "..."
	}
	return message
}

func cloneMessageChannelCredentials(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func messageChannelSecretID(platform, ownerUserID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(platform) + "\x00" + strings.TrimSpace(ownerUserID)))
	return messageChannelSecretPrefix + strings.ToLower(strings.TrimSpace(platform)) + "_" + hex.EncodeToString(sum[:16])
}

func messageChannelRuntimeKey(platform, ownerUserID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(platform) + "\x00" + strings.TrimSpace(ownerUserID)))
	return strings.ToLower(strings.TrimSpace(platform)) + "_" + hex.EncodeToString(sum[:16])
}

func messageChannelBindingKey(platform, sessionKey string) string {
	return strings.ToLower(strings.TrimSpace(platform)) + "\x00" + strings.TrimSpace(sessionKey)
}
