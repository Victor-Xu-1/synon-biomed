package server

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"synon-go/internal/networkpolicy"
)

const (
	allowedDomainsSettingKey  = "network.allowedDomains"
	firstRunSettingKey        = "onboarding.firstRunCompleted"
	useIntentSettingKey       = "onboarding.useIntent"
	ambientBackdropSettingKey = "appearance.ambientBackdrop"
	maximumUserAllowedDomains = 64
	defaultUseIntent          = "commercial"
)

func (s *Server) handleRuntimeAllowedDomains(w http.ResponseWriter, r *http.Request) {
	s.handleAllowedDomains(w, r, false)
}

func (s *Server) handleCompatibilityAllowedDomains(w http.ResponseWriter, r *http.Request) {
	s.handleAllowedDomains(w, r, true)
}

func (s *Server) handleAllowedDomains(w http.ResponseWriter, r *http.Request, compatibility bool) {
	if s.settingsStore == nil {
		s.writeAllowedDomainsError(w, http.StatusServiceUnavailable, "settings store is not configured", compatibility)
		return
	}
	isCollection := r.URL.Path == "/api/go/settings/allowed-domains" || r.URL.Path == "/api/preferences/allowed-domains"
	if !isCollection && (!compatibility || r.Method != http.MethodDelete) {
		s.writeAllowedDomainsError(w, http.StatusNotFound, "allowed-domain endpoint not found", compatibility)
		return
	}
	switch r.Method {
	case http.MethodGet:
		domains, err := s.loadAllowedDomains()
		if err != nil {
			s.writeAllowedDomainsError(w, http.StatusInternalServerError, err.Error(), compatibility)
			return
		}
		activeKernelCount := 0
		if s.kernelManager != nil {
			activeKernelCount = s.kernelManager.ActiveCount()
		}
		payload := map[string]any{
			"domains": domains, "configDomains": append([]string{}, s.configAllowedDomains...),
			"deniedDomains": networkpolicy.EffectiveDeniedPatterns(nil, s.configDeniedDomains), "activeKernelCount": activeKernelCount,
		}
		if !compatibility {
			payload["ok"] = true
		}
		writeWorkspaceJSON(w, http.StatusOK, payload)
	case http.MethodPut:
		var input struct {
			Domains []string `json:"domains"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			s.writeAllowedDomainsError(w, http.StatusBadRequest, err.Error(), compatibility)
			return
		}
		if input.Domains == nil {
			s.writeAllowedDomainsError(w, http.StatusBadRequest, "domains: string[] required", compatibility)
			return
		}
		domains, err := s.normalizeGrantableDomains(input.Domains)
		if err != nil {
			s.writeAllowedDomainsError(w, http.StatusBadRequest, err.Error(), compatibility)
			return
		}
		s.allowedDomainsMu.Lock()
		defer s.allowedDomainsMu.Unlock()
		previous, err := s.loadAllowedDomains()
		if err != nil {
			s.writeAllowedDomainsError(w, http.StatusInternalServerError, err.Error(), compatibility)
			return
		}
		if _, err := s.settingsStore.Set(allowedDomainsSettingKey, domains); err != nil {
			s.writeAllowedDomainsError(w, http.StatusInternalServerError, err.Error(), compatibility)
			return
		}
		if err := s.publishAllowedDomainsEvent("replaced", domains); err != nil {
			s.rollbackAllowedDomainsEventFailure(w, "allowed-domain replace", previous, err, compatibility)
			return
		}
		s.writeAllowedDomainsSuccess(w, domains, compatibility)
	case http.MethodPost:
		var input struct {
			Domain string `json:"domain"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			s.writeAllowedDomainsError(w, http.StatusBadRequest, err.Error(), compatibility)
			return
		}
		normalized, err := s.normalizeGrantableDomain(input.Domain)
		if err != nil {
			s.writeAllowedDomainsError(w, http.StatusBadRequest, err.Error(), compatibility)
			return
		}
		s.allowedDomainsMu.Lock()
		defer s.allowedDomainsMu.Unlock()
		previous, err := s.loadAllowedDomains()
		if err != nil {
			s.writeAllowedDomainsError(w, http.StatusInternalServerError, err.Error(), compatibility)
			return
		}
		domains, err := s.normalizeGrantableDomains(append(previous, normalized))
		if err != nil {
			s.writeAllowedDomainsError(w, http.StatusBadRequest, err.Error(), compatibility)
			return
		}
		if _, err := s.settingsStore.Set(allowedDomainsSettingKey, domains); err != nil {
			s.writeAllowedDomainsError(w, http.StatusInternalServerError, err.Error(), compatibility)
			return
		}
		if err := s.publishAllowedDomainsEvent("granted", domains); err != nil {
			s.rollbackAllowedDomainsEventFailure(w, "allowed-domain grant", previous, err, compatibility)
			return
		}
		s.writeAllowedDomainsSuccess(w, domains, compatibility)
	case http.MethodDelete:
		domain := r.URL.Query().Get("domain")
		if compatibility {
			domain = strings.TrimPrefix(r.URL.Path, "/api/preferences/allowed-domains/")
		}
		normalized, err := normalizeAllowedDomain(domain)
		if err != nil {
			s.writeAllowedDomainsError(w, http.StatusBadRequest, err.Error(), compatibility)
			return
		}
		s.allowedDomainsMu.Lock()
		defer s.allowedDomainsMu.Unlock()
		previous, err := s.loadAllowedDomains()
		if err != nil {
			s.writeAllowedDomainsError(w, http.StatusInternalServerError, err.Error(), compatibility)
			return
		}
		domains := make([]string, 0, len(previous))
		for _, domain := range previous {
			if domain != normalized {
				domains = append(domains, domain)
			}
		}
		if _, err := s.settingsStore.Set(allowedDomainsSettingKey, domains); err != nil {
			s.writeAllowedDomainsError(w, http.StatusInternalServerError, err.Error(), compatibility)
			return
		}
		if err := s.publishAllowedDomainsEvent("revoked", domains); err != nil {
			s.rollbackAllowedDomainsEventFailure(w, "allowed-domain revoke", previous, err, compatibility)
			return
		}
		s.writeAllowedDomainsSuccess(w, domains, compatibility)
	default:
		s.writeAllowedDomainsError(w, http.StatusMethodNotAllowed, "method not allowed", compatibility)
	}
}

func (s *Server) normalizeGrantableDomains(values []string) ([]string, error) {
	domains, err := networkpolicy.NormalizePatterns(values, maximumUserAllowedDomains)
	if err != nil {
		return nil, err
	}
	for _, domain := range domains {
		if _, err := s.validateGrantableDomain(domain); err != nil {
			return nil, err
		}
	}
	return domains, nil
}

func (s *Server) normalizeGrantableDomain(value string) (string, error) {
	domain, err := normalizeAllowedDomain(value)
	if err != nil {
		return "", err
	}
	return s.validateGrantableDomain(domain)
}

func (s *Server) validateGrantableDomain(domain string) (string, error) {
	if networkpolicy.PrivateOrReserved(domain) {
		return "", fmt.Errorf("private/reserved host not grantable: %s", domain)
	}
	if denied := networkpolicy.ConflictingPattern(domain, networkpolicy.EffectiveDeniedPatterns([]string{domain}, s.configDeniedDomains)); denied != "" {
		return "", fmt.Errorf("domain %s conflicts with denied domain %s", domain, denied)
	}
	return domain, nil
}

func (s *Server) writeAllowedDomainsSuccess(w http.ResponseWriter, domains []string, compatibility bool) {
	if compatibility {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "domains": domains})
}

func (s *Server) writeAllowedDomainsError(w http.ResponseWriter, status int, message string, compatibility bool) {
	if compatibility {
		writeWorkspaceJSON(w, status, map[string]any{"detail": message})
		return
	}
	writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": message})
}

func (s *Server) rollbackAllowedDomainsEventFailure(w http.ResponseWriter, operation string, previous []string, eventErr error, compatibility bool) {
	_, rollbackErr := s.settingsStore.Set(allowedDomainsSettingKey, previous)
	message := operation + " was rolled back because its realtime event could not be persisted: " + eventErr.Error()
	if rollbackErr != nil {
		message += "; settings rollback failed: " + rollbackErr.Error()
	}
	s.writeAllowedDomainsError(w, http.StatusInternalServerError, message, compatibility)
}

func (s *Server) publishAllowedDomainsEvent(action string, domains []string) error {
	if s.workspaceStore == nil {
		return nil
	}
	_, err := s.publishGlobalEvent("network_access_granted", map[string]any{"action": action, "domains": domains})
	return err
}

func (s *Server) handleRuntimeFirstRun(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "settings store is not configured"})
		return
	}
	if r.Method == http.MethodGet {
		value, err := s.firstRunComplete(preferenceUserID(r))
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "completed": value})
		return
	}
	if r.Method != http.MethodPut {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		Completed bool `json:"completed"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.settingsStore.Set(userPreferenceSettingKey(firstRunSettingKey, preferenceUserID(r)), input.Completed); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "completed": input.Completed})
}

func (s *Server) handleRuntimeUseIntent(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "settings store is not configured"})
		return
	}
	userID := preferenceUserID(r)
	switch r.Method {
	case http.MethodGet:
		intent, declared, err := s.loadUseIntent(userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "value": intent, "declared": declared})
	case http.MethodPut:
		var input struct {
			Value string `json:"value"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !validUseIntent(input.Value) {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "use intent must be commercial or noncommercial"})
			return
		}
		if err := s.storeUseIntent(userID, input.Value); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "value": input.Value, "declared": true})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleCompatibilityFirstRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "settings store is not configured"})
		return
	}
	complete, err := s.firstRunComplete(preferenceUserID(r))
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"complete": complete})
}

func (s *Server) handleCompatibilityFirstRunComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
		return
	}
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "settings store is not configured"})
		return
	}
	key := userPreferenceSettingKey(firstRunSettingKey, preferenceUserID(r))
	if _, err := s.settingsStore.Set(key, true); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleCompatibilityUseIntent(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "settings store is not configured"})
		return
	}
	userID := preferenceUserID(r)
	switch r.Method {
	case http.MethodGet:
		intent, declared, err := s.loadUseIntent(userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"intent": intent, "declared": declared})
	case http.MethodPut:
		var input struct {
			Intent string `json:"intent"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		if !validUseIntent(input.Intent) {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "intent: 'commercial' | 'noncommercial' required"})
			return
		}
		if err := s.storeUseIntent(userID, input.Intent); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"intent": input.Intent, "declared": true})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func (s *Server) firstRunComplete(userID string) (bool, error) {
	key := userPreferenceSettingKey(firstRunSettingKey, userID)
	setting, found, err := s.settingsStore.Get(key)
	if err != nil {
		return false, err
	}
	if found {
		complete, ok := setting.Value.(bool)
		if !ok {
			return false, errors.New("stored first-run preference is invalid")
		}
		if complete {
			return true, nil
		}
	}
	if s.workspaceStore == nil {
		return false, nil
	}
	hasFrame, err := s.workspaceStore.HasUserRootFrameOutsideAgents(userID, []string{"ONBOARDING", "UPLOADS"})
	if err != nil {
		return false, err
	}
	if !hasFrame {
		return false, nil
	}
	if _, err := s.settingsStore.Set(key, true); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) loadUseIntent(userID string) (string, bool, error) {
	setting, found, err := s.settingsStore.Get(userPreferenceSettingKey(useIntentSettingKey, userID))
	if err != nil || !found {
		return defaultUseIntent, false, err
	}
	intent, ok := setting.Value.(string)
	if !ok || !validStoredUseIntent(intent) {
		return "", false, errors.New("stored use intent is invalid")
	}
	return intent, true, nil
}

func (s *Server) storeUseIntent(userID, intent string) error {
	_, err := s.settingsStore.Set(userPreferenceSettingKey(useIntentSettingKey, userID), intent)
	return err
}

func validUseIntent(intent string) bool {
	return intent == "commercial" || intent == "noncommercial"
}

func validStoredUseIntent(intent string) bool {
	intent = strings.TrimSpace(intent)
	return intent != "" && len(intent) <= 64 && !strings.ContainsAny(intent, "\x00\r\n")
}

func preferenceUserID(r *http.Request) string {
	userID := strings.TrimSpace(resolveUserID(r, nil))
	if userID == "" {
		return "local"
	}
	return userID
}

func userPreferenceSettingKey(base, userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" || userID == "local" {
		return base
	}
	digest := sha256.Sum256([]byte(userID))
	return fmt.Sprintf("%s.user.%x", base, digest[:16])
}

func (s *Server) handleRuntimeAmbientBackdrop(w http.ResponseWriter, r *http.Request) {
	if s.settingsStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "settings store is not configured"})
		return
	}
	key := userPreferenceSettingKey(ambientBackdropSettingKey, preferenceUserID(r))
	if r.Method == http.MethodGet {
		value := false
		if setting, found, err := s.settingsStore.Get(key); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		} else if found {
			switch stored := setting.Value.(type) {
			case bool:
				value = stored
			case string:
				migrated, parseErr := strconv.ParseBool(strings.TrimSpace(stored))
				if parseErr != nil {
					writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "stored ambient backdrop preference is invalid"})
					return
				}
				value = migrated
				if _, err := s.settingsStore.Set(key, value); err != nil {
					writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			default:
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "stored ambient backdrop preference is invalid"})
				return
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "value": value})
		return
	}
	if r.Method != http.MethodPut {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		Value *bool `json:"value"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if input.Value == nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "value: boolean required"})
		return
	}
	if _, err := s.settingsStore.Set(key, *input.Value); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "value": *input.Value})
}

func (s *Server) loadAllowedDomains() ([]string, error) {
	setting, found, err := s.settingsStore.Get(allowedDomainsSettingKey)
	if err != nil || !found {
		return []string{}, err
	}
	return normalizeAllowedDomains(settingStringSlice(setting.Value))
}

func settingStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return values
	default:
		return []string{}
	}
}

func normalizeAllowedDomains(values []string) ([]string, error) {
	return networkpolicy.NormalizePatterns(values, 0)
}

func normalizeAllowedDomain(value string) (string, error) {
	return networkpolicy.NormalizePattern(value)
}
