package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSynonLinkSessionTTL = 48 * time.Hour
	maxSynonLinkLoginBytes     = 4 * 1024
	maxSynonLinkSessionBytes   = 2048
	synonSessionVersion        = 1
	synonSessionScopeWeb       = "web"
	synonSessionScopeLink      = "synon-link"
)

var (
	errSynonLinkAuthDisabled   = errors.New("Synon Link login is not configured")
	errSynonLinkInvalidLogin   = errors.New("invalid username or password")
	errSynonLinkInvalidSession = errors.New("invalid or expired Synon Link session")
)

type SynonLinkAuthOptions struct {
	Username string
	Password string
	UserID   string
	TTL      time.Duration
}

type synonLinkAuthUser struct {
	ID          string `json:"id"`
	UserID      string `json:"userId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Provider    string `json:"provider"`
}

type synonLinkSessionPayload struct {
	Version     int    `json:"v"`
	UserID      string `json:"uid"`
	Username    string `json:"usr"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Scope       string `json:"scp"`
	Expires     int64  `json:"exp"`
	Nonce       string `json:"nonce"`
}

type synonLinkAuthenticator struct {
	enabled      bool
	usernameHash [sha256.Size]byte
	passwordHash [sha256.Size]byte
	signingKey   [sha256.Size]byte
	username     string
	userID       string
	ttl          time.Duration
	now          func() time.Time
	random       io.Reader
}

func newSynonLinkAuthenticator(options SynonLinkAuthOptions) *synonLinkAuthenticator {
	username := strings.TrimSpace(options.Username)
	password := options.Password
	userID := strings.TrimSpace(options.UserID)
	if username == "" {
		username = "local"
	}
	if userID == "" {
		userID = "local"
	}
	ttl := options.TTL
	if ttl <= 0 {
		ttl = defaultSynonLinkSessionTTL
	}
	auth := &synonLinkAuthenticator{
		enabled:  password != "",
		username: username,
		userID:   userID,
		ttl:      ttl,
		now:      time.Now,
		random:   rand.Reader,
	}
	auth.usernameHash = sha256.Sum256([]byte(username))
	auth.passwordHash = sha256.Sum256([]byte(password))
	auth.signingKey = sha256.Sum256([]byte("synon-link-session\x00" + username + "\x00" + userID + "\x00" + password))
	return auth
}

func (a *synonLinkAuthenticator) Login(username, password string) (string, synonLinkAuthUser, error) {
	return a.LoginForScope(username, password, synonSessionScopeLink)
}

func (a *synonLinkAuthenticator) LoginForScope(username, password, scope string) (string, synonLinkAuthUser, error) {
	user, err := a.validateCredentials(username, password)
	if err != nil {
		return "", synonLinkAuthUser{}, err
	}
	return a.createSessionForUser(user, scope)
}

func (a *synonLinkAuthenticator) validateCredentials(username, password string) (synonLinkAuthUser, error) {
	if a == nil || !a.enabled {
		return synonLinkAuthUser{}, errSynonLinkAuthDisabled
	}
	usernameHash := sha256.Sum256([]byte(strings.TrimSpace(username)))
	passwordHash := sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(usernameHash[:], a.usernameHash[:]) != 1 || subtle.ConstantTimeCompare(passwordHash[:], a.passwordHash[:]) != 1 {
		return synonLinkAuthUser{}, errSynonLinkInvalidLogin
	}
	return a.user(), nil
}

func (a *synonLinkAuthenticator) createSessionForUser(user synonLinkAuthUser, scope string) (string, synonLinkAuthUser, error) {
	if a == nil || !a.enabled {
		return "", synonLinkAuthUser{}, errSynonLinkAuthDisabled
	}
	if !validSynonSessionScope(scope) {
		return "", synonLinkAuthUser{}, errSynonLinkInvalidSession
	}
	user.ID = strings.TrimSpace(user.ID)
	user.UserID = strings.TrimSpace(user.UserID)
	user.Username = strings.TrimSpace(user.Username)
	if user.ID == "" {
		user.ID = user.UserID
	}
	if user.UserID == "" {
		user.UserID = user.ID
	}
	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}
	if user.Provider == "" {
		user.Provider = "local"
	}
	if user.ID == "" || user.UserID == "" || user.Username == "" ||
		len(user.ID) > 512 || len(user.Username) > 512 || len(user.DisplayName) > 512 || len(user.Email) > 512 {
		return "", synonLinkAuthUser{}, errSynonLinkInvalidSession
	}
	nonceBytes := make([]byte, 18)
	if _, err := io.ReadFull(a.random, nonceBytes); err != nil {
		return "", synonLinkAuthUser{}, fmt.Errorf("generate Synon Link session nonce: %w", err)
	}
	payload := synonLinkSessionPayload{
		Version: synonSessionVersion, UserID: user.UserID, Username: user.Username,
		DisplayName: user.DisplayName, Email: user.Email, Scope: scope,
		Expires: a.now().UTC().Add(a.ttl).Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes),
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return "", synonLinkAuthUser{}, err
	}
	payloadPart := base64.RawURLEncoding.EncodeToString(encodedPayload)
	signaturePart := base64.RawURLEncoding.EncodeToString(a.sign([]byte(payloadPart)))
	return payloadPart + "." + signaturePart, user, nil
}

func (a *synonLinkAuthenticator) Authenticate(token string) (synonLinkAuthUser, bool) {
	payload, ok := a.sessionPayload(token)
	if !ok {
		return synonLinkAuthUser{}, false
	}
	if !validSynonSessionScope(payload.Scope) || !a.validSessionPayload(payload) {
		return synonLinkAuthUser{}, false
	}
	return userFromSynonSession(payload), true
}

func (a *synonLinkAuthenticator) AuthenticateScope(token, scope string) (synonLinkAuthUser, bool) {
	payload, ok := a.sessionPayload(token)
	if !ok || payload.Scope != scope || !a.validSessionPayload(payload) {
		return synonLinkAuthUser{}, false
	}
	return userFromSynonSession(payload), true
}

func (a *synonLinkAuthenticator) validSessionPayload(payload synonLinkSessionPayload) bool {
	return a != nil && payload.Version == synonSessionVersion &&
		strings.TrimSpace(payload.UserID) != "" && strings.TrimSpace(payload.Username) != "" &&
		payload.Nonce != "" && payload.Expires > a.now().UTC().Unix()
}

func userFromSynonSession(payload synonLinkSessionPayload) synonLinkAuthUser {
	displayName := payload.DisplayName
	if displayName == "" {
		displayName = payload.Username
	}
	return synonLinkAuthUser{
		ID: payload.UserID, UserID: payload.UserID, Username: payload.Username,
		DisplayName: displayName, Email: payload.Email, Provider: "local",
	}
}

func validSynonSessionScope(scope string) bool {
	return scope == synonSessionScopeWeb || scope == synonSessionScopeLink
}

func (a *synonLinkAuthenticator) sessionPayload(token string) (synonLinkSessionPayload, bool) {
	if a == nil || !a.enabled || len(token) == 0 || len(token) > maxSynonLinkSessionBytes {
		return synonLinkSessionPayload{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return synonLinkSessionPayload{}, false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, a.sign([]byte(parts[0]))) {
		return synonLinkSessionPayload{}, false
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payloadJSON) > maxSynonLinkSessionBytes {
		return synonLinkSessionPayload{}, false
	}
	var payload synonLinkSessionPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return synonLinkSessionPayload{}, false
	}
	return payload, true
}

func (a *synonLinkAuthenticator) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, a.signingKey[:])
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func (a *synonLinkAuthenticator) user() synonLinkAuthUser {
	return synonLinkAuthUser{
		ID: a.userID, UserID: a.userID, Username: a.username, DisplayName: a.username, Provider: "local",
	}
}

func (s *Server) handleSynonLinkLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST is required")
		return
	}
	if !isLoopbackRequest(r) {
		writeError(w, http.StatusForbidden, "LOCAL_ONLY", "Synon Link login is available only from the local host")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSynonLinkLoginBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid login request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "login request must contain one JSON value")
		return
	}
	if retryAfter := s.loginLimiter.RetryAfter(r, input.Username); retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(max(1, int64(retryAfter.Round(time.Second)/time.Second)), 10))
		writeError(w, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "too many login attempts; try again later")
		return
	}
	token, user, err := s.synonLinkAuth.LoginForScope(input.Username, input.Password, synonSessionScopeLink)
	if err != nil {
		if errors.Is(err, errSynonLinkInvalidLogin) {
			s.loginLimiter.RecordFailure(r, input.Username)
		}
		if errors.Is(err, errSynonLinkAuthDisabled) {
			writeError(w, http.StatusServiceUnavailable, "AUTH_NOT_CONFIGURED", err.Error())
			return
		}
		if errors.Is(err, errSynonLinkInvalidLogin) {
			writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "AUTH_ERROR", "unable to create Synon Link session")
		return
	}
	s.loginLimiter.RecordSuccess(r, input.Username)
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": user, "expiresIn": int64(s.synonLinkAuth.ttl.Seconds())})
}

func (s *Server) handleSynonLinkCompatibilityWebSocket(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("appSession"))
	if revoked, err := s.synonLinkSessionRevoked(token); err != nil || revoked {
		http.Error(w, errSynonLinkInvalidSession.Error(), http.StatusUnauthorized)
		return
	}
	user, ok := s.synonLinkAuth.AuthenticateScope(token, synonSessionScopeLink)
	if !ok {
		http.Error(w, errSynonLinkInvalidSession.Error(), http.StatusUnauthorized)
		return
	}
	request := r.Clone(r.Context())
	requestURL := *r.URL
	query := requestURL.Query()
	query.Set("userId", user.ID)
	requestURL.RawQuery = query.Encode()
	request.URL = &requestURL
	s.handleSynonLinkWebSocket(w, request)
}

func isLoopbackRequest(r *http.Request) bool {
	host := strings.TrimSpace(r.RemoteAddr)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
