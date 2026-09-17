package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	webSessionCookieName = "synon_session"
	webCSRFCookieName    = "synon_csrf"
	webDeviceCookieName  = "synon_device"
	webCSRFHeaderName    = "X-Synon-CSRF-Token"
	webErrorCodeHeader   = "X-Synon-Error-Code"
	webAuthRequiredCode  = "AUTH_SESSION_REQUIRED"
	webCSRFInvalidCode   = "CSRF_INVALID"
)

func (s *Server) handleWebCurrentUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	user, ok := s.webUser(r)
	if !ok {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "authentication required"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"user":    webAuthUserResponse(user),
	})
}

func (s *Server) handleWebLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "POST is required"})
		return
	}
	if !s.approvedPasswordAuthRequest(r) {
		writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{"success": false, "message": "password login requires loopback or the configured HTTPS origin"})
		return
	}
	if s.webSessionsError != nil || s.webSessions == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "Web session store is unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSynonLinkLoginBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid login request"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "login request must contain one JSON value"})
		return
	}
	if retryAfter := s.loginLimiter.RetryAfter(r, input.Username); retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64(retryAfter.Round(time.Second)/time.Second)), 10))
		writeWorkspaceJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": "too many login attempts; try again later"})
		return
	}
	user, authMethod, err := s.authenticateWebLogin(input.Username, input.Password)
	if err != nil {
		if errors.Is(err, errSynonLinkInvalidLogin) {
			s.loginLimiter.RecordFailure(r, input.Username)
		}
		switch {
		case errors.Is(err, errSynonLinkAuthDisabled):
			writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "password login is not configured"})
		case errors.Is(err, errSynonLinkInvalidLogin):
			writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "invalid username or password"})
		default:
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to create session"})
		}
		return
	}
	s.loginLimiter.RecordSuccess(r, input.Username)
	metadata, deviceToken, err := s.webSessions.metadataForSessionCreation(r)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to create session"})
		return
	}
	token, csrf, session, err := s.webSessions.Create(user.ID, authMethod, input.Remember, metadata)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "unable to create session"})
		return
	}
	user.Provider = authMethod
	s.writeWebSession(w, r, token, csrf, deviceToken, session.ExpiresAt, user, input.Remember)
}

func (s *Server) webUser(r *http.Request) (synonLinkAuthUser, bool) {
	if !s.webAuthenticationEnabled() && s.synonLinkAuth != nil && isLoopbackRequest(r) {
		return s.synonLinkAuth.user(), true
	}
	if token := webSessionCookieToken(r); token != "" && s.webSessions != nil {
		session, ok, err := s.webSessions.Authenticate(token, true)
		if err == nil && ok {
			if s.synonLinkAuth != nil && session.AccountID == s.synonLinkAuth.user().ID {
				user := s.synonLinkAuth.user()
				user.Provider = session.AuthMethod
				return user, true
			}
			if user, found := s.webAccounts.UserByID(session.AccountID); found {
				user.Provider = session.AuthMethod
				return user, true
			}
		}
	}
	if token := bearerOrAppSessionToken(r); token != "" && s.synonLinkAuth != nil {
		if revoked, err := s.synonLinkSessionRevoked(token); err != nil || revoked {
			return synonLinkAuthUser{}, false
		}
		return s.synonLinkAuth.AuthenticateScope(token, synonSessionScopeWeb)
	}
	return synonLinkAuthUser{}, false
}

func (s *Server) withWebIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.webAuthenticationEnabled() {
			r.Header.Del("X-Synon-User-Id")
			r.Header.Del("X-Synon-User-Name")
		}
		if strings.TrimSpace(r.Header.Get("X-Synon-User-Id")) == "" {
			if userID := trustedLocalRequestedUser(r, s.webAuthenticationEnabled()); userID != "" {
				r.Header.Set("X-Synon-User-Id", userID)
			}
		}
		if strings.TrimSpace(r.Header.Get("X-Synon-User-Id")) == "" {
			if user, ok := s.webUser(r); ok {
				r.Header.Set("X-Synon-User-Id", user.ID)
				r.Header.Set("X-Synon-User-Name", user.Username)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withWebSessionMigration(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyToken := webSessionCookieToken(r)
		if legacyToken == "" || s.webSessions == nil || s.synonLinkAuth == nil {
			next.ServeHTTP(w, r)
			return
		}
		if _, current, err := s.webSessions.Authenticate(legacyToken, false); err != nil || current {
			next.ServeHTTP(w, r)
			return
		}
		if revoked, err := s.synonLinkSessionRevoked(legacyToken); err != nil || revoked {
			next.ServeHTTP(w, r)
			return
		}
		user, valid := s.synonLinkAuth.AuthenticateScope(legacyToken, synonSessionScopeWeb)
		if !valid {
			next.ServeHTTP(w, r)
			return
		}
		metadata, deviceToken, err := s.webSessions.metadataForSessionCreation(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		token, csrf, session, err := s.webSessions.Create(user.ID, "local", false, metadata)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		if err := s.revokeSynonLinkSession(legacyToken); err != nil {
			_, _, _ = s.webSessions.Revoke(user.ID, session.ID, token)
			next.ServeHTTP(w, r)
			return
		}
		s.writeWebSessionCookies(w, r, token, csrf, deviceToken, false, session.ExpiresAt)
		migrated := r.Clone(r.Context())
		migrated.Header = r.Header.Clone()
		migrated.Header.Del("Cookie")
		for _, cookie := range r.Cookies() {
			if cookie.Name != webSessionCookieName && cookie.Name != webCSRFCookieName {
				migrated.AddCookie(cookie)
			}
		}
		migrated.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: token})
		migrated.AddCookie(&http.Cookie{Name: webCSRFCookieName, Value: csrf})
		next.ServeHTTP(w, migrated)
	})
}

func trustedLocalRequestedUser(r *http.Request, webAuthEnabled bool) string {
	if webAuthEnabled || !isLoopbackRequest(r) {
		return ""
	}
	if userID := strings.TrimSpace(r.URL.Query().Get("userId")); userID != "" {
		return userID
	}
	return strings.TrimSpace(r.URL.Query().Get("user_id"))
}

func (s *Server) withAPIAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !protectedRuntimeRoute(r.URL.Path) || publicRuntimeRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.TrimSpace(r.Header.Get("X-Synon-User-Id")) != "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("WWW-Authenticate", `Bearer realm="synon"`)
		w.Header().Set("X-Synon-Auth-Required", "session")
		w.Header().Set(webErrorCodeHeader, webAuthRequiredCode)
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{
			"success": false, "code": webAuthRequiredCode, "message": "authentication required",
		})
	})
}

func (s *Server) withBrowserCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !browserCSRFRequired(r) || !s.hasAuthenticatedWebSessionCookie(r) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, cookieErr := r.Cookie(webCSRFCookieName)
		headerToken := strings.TrimSpace(r.Header.Get(webCSRFHeaderName))
		sessionToken := webSessionCookieToken(r)
		csrfValid := cookieErr == nil && headerToken != "" && len(headerToken) == len(cookie.Value) &&
			subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookie.Value)) == 1
		if csrfValid {
			csrfValid, _ = s.webSessions.ValidateCSRF(sessionToken, headerToken)
		}
		if !csrfValid {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set(webErrorCodeHeader, webCSRFInvalidCode)
			writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{
				"success": false, "code": webCSRFInvalidCode, "message": "CSRF token is missing or invalid",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func browserCSRFRequired(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return false
	}
	return r.URL.Path != "/login" && r.URL.Path != "/api/auth/login" &&
		r.URL.Path != "/register" && r.URL.Path != "/api/auth/register"
}

func (s *Server) hasAuthenticatedWebSessionCookie(r *http.Request) bool {
	token := webSessionCookieToken(r)
	if token == "" || s.webSessions == nil {
		return false
	}
	_, ok, err := s.webSessions.Authenticate(token, false)
	return err == nil && ok
}

func (s *Server) setWebCSRFCookie(w http.ResponseWriter, r *http.Request) {
	value := s.csrfToken
	if token := webSessionCookieToken(r); token != "" && s.webSessions != nil {
		rotated, err := s.webSessions.RotateCSRF(token)
		if err != nil {
			return
		}
		value = rotated
	}
	http.SetCookie(w, &http.Cookie{
		Name: webCSRFCookieName, Value: value, Path: "/", Secure: s.webCookieSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func protectedRuntimeRoute(path string) bool {
	return path == "/metrics" || path == "/api" || strings.HasPrefix(path, "/api/") || path == "/v1" ||
		strings.HasPrefix(path, "/v1/") || path == "/ws" || strings.HasPrefix(path, "/ws/")
}

func publicRuntimeRequest(r *http.Request) bool {
	switch r.URL.Path {
	case "/health", "/api/health", "/api/auth/login", "/api/auth/logout", "/api/auth/user",
		"/api/auth/register", "/api/auth/providers", "/login", "/register", "/synon-link/ws":
		return true
	case "/api/settings/client":
		return r.Method == http.MethodGet && r.URL.Query().Get("scope") == webClientSettingsScope
	default:
		return isWebExternalAuthRequestPath(r.URL.Path)
	}
}

func (s *Server) webAuthenticationEnabled() bool {
	return s != nil && ((s.synonLinkAuth != nil && s.synonLinkAuth.enabled) ||
		(s.webExternalAuth != nil && s.webExternalAuth.enabled()))
}

func webSessionCookieToken(r *http.Request) string {
	cookie, err := r.Cookie(webSessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func bearerOrAppSessionToken(r *http.Request) string {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authorization) > len("Bearer ") && strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		return strings.TrimSpace(authorization[len("Bearer "):])
	}
	return strings.TrimSpace(r.URL.Query().Get("appSession"))
}

func withBrowserOriginPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && browserOriginRequired(r) && !sameRequestOrigin(origin, r.Host) {
			writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{"error": "cross-origin browser request rejected"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func browserOriginRequired(r *http.Request) bool {
	// Synon Link authenticates from a browser extension origin. Its login route
	// is separately restricted to loopback clients and configured credentials.
	if r.URL.Path == "/api/auth/login" {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, prefix := range []string{"/api/events/ws", "/api/ws", "/ws/"} {
		if r.URL.Path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(r.URL.Path, prefix) {
			return true
		}
	}
	return false
}

func sameRequestOrigin(origin, requestHost string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, strings.TrimSpace(requestHost))
}
