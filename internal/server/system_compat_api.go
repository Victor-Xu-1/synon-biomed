package server

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	kernelruntime "synon-go/internal/kernel"
)

const synonLinkRevokedSessionNamespace = "synon-link-revoked-sessions"

type environmentProjection struct {
	EnvironmentName string  `json:"env_name"`
	AgentName       *string `json:"agent_name"`
	Language        string  `json:"language"`
	PackageCount    int     `json:"package_count"`
	Status          string  `json:"status"`
	StartedAt       string  `json:"started_at,omitempty"`
	Phase           string  `json:"phase,omitempty"`
	LastProgressAt  string  `json:"last_progress_at,omitempty"`
	Error           string  `json:"error,omitempty"`
	FailureSource   string  `json:"failure_source,omitempty"`
}

func (s *Server) handleCurrentUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(resolveUserID(r, nil))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"error": "user identity is required"})
		return
	}
	email := strings.TrimSpace(r.Header.Get("X-Synon-User-Email"))
	if strings.ContainsAny(email, "\r\n\x00") || len(email) > 320 {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid user email"})
		return
	}
	if email == "" {
		email = localIdentityEmail(userID)
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"user_id":        userID,
		"email":          email,
		"provider":       "local",
		"has_api_key":    hasConfiguredModelAPIKey(),
		"shared_api_key": false,
		"auth_mode":      "local_header",
	})
}

func (s *Server) handleEnvironmentStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if _, ok := attachmentUserID(w, r); !ok {
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"environments":          s.environmentStatus(false),
		"conda_disabled_reason": s.condaDisabledReason(),
	})
}

func (s *Server) handleEnvironmentRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if _, ok := attachmentUserID(w, r); !ok {
		return
	}
	before := s.environmentStatus(false)
	retried := make([]string, 0, len(before))
	repairErrors := map[string]string{}
	retryRuntime := false
	retryConfinement := false
	for _, environment := range before {
		if environment.Status == "failed" {
			switch environment.FailureSource {
			case "discovery":
				continue
			case "confinement":
				retried = append(retried, environment.EnvironmentName)
				retryConfinement = true
				continue
			}
			if s.kernelManager == nil {
				continue
			}
			retryRuntime = true
			retried = append(retried, environment.EnvironmentName)
			if environment.Language == "r" {
				// Legacy/unconfigured R status is read-only during migration. Do
				// not revive the removed dynamic package installer; only the
				// verified bundled R authority may be retried.
				if !s.kernelManager.ManagedRProvisioningEnabled() {
					retried = retried[:len(retried)-1]
					continue
				}
				if err := s.kernelManager.RepairDefaultREnvironment(r.Context()); err != nil {
					repairErrors[environment.EnvironmentName] = err.Error()
				}
			} else if environment.Language == "python" &&
				environment.EnvironmentName == s.kernelManager.ManagedPythonEnvironmentName() {
				if err := s.kernelManager.RetryManagedPythonEnvironment(r.Context()); err != nil {
					repairErrors[environment.EnvironmentName] = "managed Python scientific runtime repair failed"
				}
			}
		}
	}
	if retryConfinement {
		s.kernelConfinementEvidence(true)
	}
	status := before
	if len(retried) > 0 {
		status = s.environmentStatus(retryRuntime)
	}
	if _, err := s.publishGlobalEvent("environment_status", map[string]any{
		"environments": status, "conda_disabled_reason": s.condaDisabledReason(),
	}); err != nil {
		writeDomainEventError(w, "environment retry", err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"retried": retried, "disabled_reason": s.condaDisabledReason(), "repair_errors": repairErrors,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(resolveUserID(r, nil))
	tokensDeleted := false
	if webToken := webSessionCookieToken(r); webToken != "" && s.webSessions != nil {
		session, valid, err := s.webSessions.Authenticate(webToken, false)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"error": "read Web session"})
			return
		}
		if !valid {
			writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid or expired session"})
			return
		}
		if userID != "" && userID != session.AccountID {
			writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{"error": "session identity does not match requested user"})
			return
		}
		userID = session.AccountID
		_, _, err = s.webSessions.Revoke(userID, session.ID, webToken)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"error": "revoke Web session"})
			return
		}
		tokensDeleted = true
	}
	token := bearerOrAppSessionToken(r)
	if token == "" {
		token = legacySynonLinkCompatibilityCookieToken(r)
	}
	if token != "" {
		tokenUser, valid := s.synonLinkAuth.Authenticate(token)
		if !valid {
			writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid or expired session"})
			return
		}
		if userID != "" && userID != tokenUser.ID {
			writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{"error": "session identity does not match requested user"})
			return
		}
		userID = tokenUser.ID
		if err := s.revokeSynonLinkSession(token); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"error": "revoke session: " + err.Error()})
			return
		}
		tokensDeleted = true
	}
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"error": "user identity is required"})
		return
	}
	if _, err := s.publishUserEvent(userID, "auth_status_changed", map[string]any{
		"authenticated": false, "auth_mode": "local_header",
	}); err != nil {
		writeDomainEventError(w, "logout", err)
		return
	}
	for _, name := range []string{webSessionCookieName, webCSRFCookieName, "session", "auth_token"} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Path: "/", MaxAge: -1,
			Expires: time.Unix(1, 0).UTC(), HttpOnly: name != webCSRFCookieName,
			Secure: s.webCookieSecure(r), SameSite: http.SameSiteLaxMode,
		})
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"status": "local_session", "tokens_deleted": tokensDeleted, "restart_pending": false,
	})
}

func legacySynonLinkCompatibilityCookieToken(r *http.Request) string {
	for _, name := range []string{"session", "auth_token"} {
		if cookie, err := r.Cookie(name); err == nil && strings.TrimSpace(cookie.Value) != "" {
			return strings.TrimSpace(cookie.Value)
		}
	}
	return ""
}

func (s *Server) revokeSynonLinkSession(token string) error {
	if s == nil || s.runtimeStore == nil {
		return fmt.Errorf("durable runtime store is not configured")
	}
	payload, ok := s.synonLinkAuth.sessionPayload(token)
	if !ok {
		return errSynonLinkInvalidSession
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	_, err := s.runtimeStore.Set(synonLinkRevokedSessionNamespace, key, map[string]any{"expires_at": payload.Expires})
	return err
}

func (s *Server) synonLinkSessionRevoked(token string) (bool, error) {
	if strings.TrimSpace(token) == "" || s == nil || s.runtimeStore == nil {
		return false, nil
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	entry, found, err := s.runtimeStore.Get(synonLinkRevokedSessionNamespace, key)
	if err != nil || !found {
		return false, err
	}
	expires := int64(0)
	if value, ok := entry.Value.(map[string]any); ok {
		switch typed := value["expires_at"].(type) {
		case float64:
			expires = int64(typed)
		case int64:
			expires = typed
		case int:
			expires = int64(typed)
		}
	}
	if expires <= 0 {
		return true, errors.New("revoked session record is invalid")
	}
	if expires > s.synonLinkAuth.now().UTC().Unix() {
		return true, nil
	}
	_, err = s.runtimeStore.Delete(synonLinkRevokedSessionNamespace, key)
	return false, err
}

func (s *Server) environmentStatus(retry bool) []environmentProjection {
	status := []environmentProjection{{
		EnvironmentName: "synon-go-runtime", Language: "go", PackageCount: 0, Status: "ready",
	}}
	if s == nil || s.kernelManager == nil {
		kernelStatus := "skipped"
		diagnostic := ""
		failureSource := ""
		if s != nil && s.kernelDiscoveryErr != nil {
			kernelStatus = "failed"
			diagnostic = s.kernelDiscoveryReason()
			failureSource = "discovery"
		}
		status = append(status, environmentProjection{
			EnvironmentName: "python-kernel-sidecar", Language: "python", PackageCount: 0, Status: kernelStatus, Error: diagnostic,
			FailureSource: failureSource,
		})
		return status
	}
	confinementReason := s.kernelConfinementReason(false)
	for _, runtimeStatus := range s.kernelManager.RuntimeEnvironmentStatuses(retry) {
		failureSource := ""
		if runtimeStatus.Status == "failed" {
			failureSource = "runtime"
		} else if confinementReason != "" {
			runtimeStatus.Status = "failed"
			runtimeStatus.Error = confinementReason
			failureSource = "confinement"
		}
		startedAt := ""
		if !runtimeStatus.StartedAt.IsZero() {
			startedAt = runtimeStatus.StartedAt.UTC().Format(time.RFC3339)
		}
		lastProgressAt := ""
		if !runtimeStatus.LastProgressAt.IsZero() {
			lastProgressAt = runtimeStatus.LastProgressAt.UTC().Format(time.RFC3339)
		}
		status = append(status, environmentProjection{
			EnvironmentName: runtimeStatus.EnvironmentName,
			Language:        runtimeStatus.Language,
			PackageCount:    runtimeStatus.PackageCount,
			Status:          runtimeStatus.Status,
			StartedAt:       startedAt,
			Phase:           runtimeStatus.Phase,
			LastProgressAt:  lastProgressAt,
			Error:           runtimeStatus.Error,
			FailureSource:   failureSource,
		})
	}
	return status
}

func (s *Server) condaDisabledReason() any {
	if s == nil || s.kernelManager == nil {
		if s != nil && s.kernelDiscoveryErr != nil {
			return s.kernelDiscoveryReason()
		}
		return "kernel runtime is not configured"
	}
	if reason := s.kernelConfinementReason(false); reason != "" {
		return reason
	}
	if err := s.kernelManager.MicromambaError(); err != nil {
		return err.Error()
	}
	return nil
}

func (s *Server) kernelDiscoveryReason() string {
	if s == nil || s.kernelDiscoveryErr == nil {
		return ""
	}
	diagnostic := kernelruntime.DiagnoseDiscoveryError(s.kernelDiscoveryErr)
	return diagnostic.Code + ": " + diagnostic.Message
}

func (s *Server) kernelConfinementEvidence(retry bool) kernelruntime.ConfinementEvidence {
	if s == nil || s.kernelManager == nil || s.kernelConfinement == nil {
		return kernelruntime.ConfinementEvidence{Available: false, Mode: "unavailable", Reason: "kernel runtime is not configured"}
	}
	return s.kernelConfinement(retry)
}

func (s *Server) kernelConfinementReason(retry bool) string {
	if s == nil || s.kernelManager == nil {
		return ""
	}
	diagnostic := kernelruntime.DiagnoseConfinementEvidence(s.kernelConfinementEvidence(retry))
	if diagnostic.Code == "" {
		return ""
	}
	return diagnostic.Code + ": " + diagnostic.Message
}

func (s *Server) kernelUnavailableReason() string {
	if reason := s.kernelDiscoveryReason(); reason != "" {
		return reason
	}
	return s.kernelConfinementReason(false)
}

func localIdentityEmail(userID string) string {
	var builder strings.Builder
	for _, value := range strings.ToLower(userID) {
		switch {
		case value >= 'a' && value <= 'z', value >= '0' && value <= '9', value == '.', value == '-', value == '_':
			builder.WriteRune(value)
		default:
			builder.WriteByte('-')
		}
	}
	local := strings.Trim(builder.String(), "-.")
	if local == "" {
		local = "local"
	}
	return local + "@local.synon"
}

func hasConfiguredModelAPIKey() bool {
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "SYNON_LLM_API_KEY"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}
