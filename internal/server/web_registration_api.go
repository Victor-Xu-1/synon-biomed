package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) handleWebRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodPost {
		writeWebRegistrationError(w, http.StatusMethodNotAllowed, "INVALID_REQUEST", "POST is required")
		return
	}
	if !isLoopbackRequest(r) {
		writeWebRegistrationError(w, http.StatusForbidden, "INVALID_REQUEST", "local registration is available only from the local host")
		return
	}
	if s.synonLinkAuth == nil || !s.synonLinkAuth.enabled {
		writeWebRegistrationError(w, http.StatusServiceUnavailable, "SERVER_ERROR", "password authentication is not configured")
		return
	}
	if s.webAccountsError != nil || s.webAccounts == nil || s.webSessionsError != nil || s.webSessions == nil {
		writeWebRegistrationError(w, http.StatusServiceUnavailable, "SERVER_ERROR", "local account store is unavailable")
		return
	}
	const rateLimitKey = "web-register"
	if retryAfter := s.loginLimiter.RetryAfter(r, rateLimitKey); retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64(retryAfter.Round(time.Second)/time.Second)), 10))
		writeWebRegistrationError(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS", "too many registration attempts; try again later")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSynonLinkLoginBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if err := decoder.Decode(&input); err != nil {
		s.loginLimiter.RecordFailure(r, rateLimitKey)
		var tooLarge *http.MaxBytesError
		var syntax *json.SyntaxError
		switch {
		case errors.As(err, &tooLarge):
			writeWebRegistrationError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "registration request is too large")
		case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
			writeWebRegistrationError(w, http.StatusBadRequest, "INVALID_JSON", "invalid JSON body")
		default:
			writeWebRegistrationError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid registration request")
		}
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		s.loginLimiter.RecordFailure(r, rateLimitKey)
		writeWebRegistrationError(w, http.StatusBadRequest, "INVALID_JSON", "registration request must contain one JSON value")
		return
	}
	user, err := s.webAccounts.Register(input.Name, input.Email, input.Password)
	if err != nil {
		var validation *webAccountValidationError
		if errors.As(err, &validation) {
			status := http.StatusBadRequest
			if validation.code == "USERNAME_EXISTS" || validation.code == "EMAIL_EXISTS" {
				status = http.StatusConflict
			} else {
				s.loginLimiter.RecordFailure(r, rateLimitKey)
			}
			writeWebRegistrationError(w, status, validation.code, validation.code)
			return
		}
		writeWebRegistrationError(w, http.StatusInternalServerError, "SERVER_ERROR", "unable to create local account")
		return
	}
	metadata, deviceToken, err := s.webSessions.metadataForSessionCreation(r)
	if err != nil {
		writeWebRegistrationError(w, http.StatusInternalServerError, "SERVER_ERROR", "unable to create session")
		return
	}
	token, csrf, session, err := s.webSessions.Create(user.ID, "local", input.Remember, metadata)
	if err != nil {
		writeWebRegistrationError(w, http.StatusInternalServerError, "SERVER_ERROR", "unable to create session")
		return
	}
	s.loginLimiter.RecordSuccess(r, rateLimitKey)
	user.Provider = "local"
	s.writeWebSession(w, r, token, csrf, deviceToken, session.ExpiresAt, user, input.Remember)
}

func (s *Server) authenticateWebLogin(username, password string) (synonLinkAuthUser, string, error) {
	user, err := s.synonLinkAuth.validateCredentials(username, password)
	if err == nil {
		return user, "local", nil
	}
	if !errors.Is(err, errSynonLinkInvalidLogin) {
		return synonLinkAuthUser{}, "", err
	}
	if s.webAccountsError != nil {
		return synonLinkAuthUser{}, "", fmt.Errorf("load local account store: %w", s.webAccountsError)
	}
	if s.webAccounts == nil {
		return synonLinkAuthUser{}, "", errSynonLinkInvalidLogin
	}
	user, found, err := s.webAccounts.Authenticate(username, password)
	if err != nil {
		return synonLinkAuthUser{}, "", err
	}
	if !found {
		return synonLinkAuthUser{}, "", errSynonLinkInvalidLogin
	}
	return user, "local", nil
}

func (s *Server) writeWebSession(
	w http.ResponseWriter,
	r *http.Request,
	token string,
	csrf string,
	deviceToken string,
	expiresAt string,
	user synonLinkAuthUser,
	remember bool,
) {
	s.writeWebSessionCookies(w, r, token, csrf, deviceToken, remember, expiresAt)
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"user":    webAuthUserResponse(user),
	})
}

func (s *Server) writeWebSessionCookies(
	w http.ResponseWriter,
	r *http.Request,
	token, csrf, deviceToken string,
	remember bool,
	expiresAt string,
) {
	cookie := &http.Cookie{
		Name: webSessionCookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: s.webCookieSecure(r), SameSite: http.SameSiteLaxMode,
	}
	if remember {
		if expiry, err := time.Parse(time.RFC3339Nano, expiresAt); err == nil {
			cookie.MaxAge = max(1, int(time.Until(expiry).Seconds()))
			cookie.Expires = expiry
		}
	}
	http.SetCookie(w, cookie)
	http.SetCookie(w, &http.Cookie{
		Name: webCSRFCookieName, Value: csrf, Path: "/", Secure: s.webCookieSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
	s.writeWebDeviceCookie(w, r, deviceToken)
}

func (s *Server) writeWebDeviceCookie(w http.ResponseWriter, r *http.Request, deviceToken string) {
	deviceExpiry := time.Now().UTC().Add(400 * 24 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name: webDeviceCookieName, Value: deviceToken, Path: "/", HttpOnly: true,
		Secure: s.webCookieSecure(r), SameSite: http.SameSiteLaxMode,
		MaxAge: max(1, int(time.Until(deviceExpiry).Seconds())), Expires: deviceExpiry,
	})
}

func webAuthUserResponse(user synonLinkAuthUser) map[string]any {
	response := map[string]any{"id": user.ID, "username": user.Username}
	if user.DisplayName != "" {
		response["displayName"] = user.DisplayName
	}
	if user.Email != "" {
		response["email"] = user.Email
	}
	if user.Provider != "" {
		response["provider"] = user.Provider
	}
	return response
}

func writeWebRegistrationError(w http.ResponseWriter, status int, code, message string) {
	writeWorkspaceJSON(w, status, map[string]any{"success": false, "code": code, "message": message})
}
