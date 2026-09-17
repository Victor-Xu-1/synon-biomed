package server

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	webSessionsVersion            = 1
	webSessionSecretBytes         = 32
	webSessionAbsoluteTTL         = 12 * time.Hour
	webSessionIdleTTL             = 2 * time.Hour
	webRememberedSessionTTL       = 30 * 24 * time.Hour
	webRememberedSessionIdleTTL   = 7 * 24 * time.Hour
	webSessionTouchInterval       = 5 * time.Minute
	webSessionMaxPerAccount       = 64
	webSecurityEventRetention     = 2000
	webSessionUserAgentLabelLimit = 96
)

type storedWebSession struct {
	ID            string `json:"id"`
	AccountID     string `json:"accountId"`
	TokenHash     string `json:"tokenHash"`
	CSRFHash      string `json:"csrfHash"`
	AuthMethod    string `json:"authMethod"`
	CreatedAt     string `json:"createdAt"`
	LastSeenAt    string `json:"lastSeenAt"`
	IdleExpiresAt string `json:"idleExpiresAt"`
	ExpiresAt     string `json:"expiresAt"`
	Remembered    bool   `json:"remembered"`
	UserAgent     string `json:"userAgent"`
	UserAgentHash string `json:"userAgentHash"`
	DeviceIDHash  string `json:"deviceIdHash,omitempty"`
	IPAddress     string `json:"ipAddress,omitempty"`
	NetworkClass  string `json:"networkClass"`
}

type storedWebSecurityEvent struct {
	ID           string `json:"id"`
	AccountID    string `json:"accountId"`
	SessionID    string `json:"sessionId,omitempty"`
	Type         string `json:"type"`
	AuthMethod   string `json:"authMethod,omitempty"`
	CreatedAt    string `json:"createdAt"`
	Success      bool   `json:"success"`
	UserAgent    string `json:"userAgent,omitempty"`
	NetworkClass string `json:"networkClass,omitempty"`
}

type webSessionsDocument struct {
	Version  int                      `json:"version"`
	Sessions []storedWebSession       `json:"sessions"`
	Events   []storedWebSecurityEvent `json:"events"`
}

type webSessionView struct {
	ID           string `json:"id"`
	AuthMethod   string `json:"authMethod"`
	CreatedAt    string `json:"createdAt"`
	LastSeenAt   string `json:"lastSeenAt"`
	ExpiresAt    string `json:"expiresAt"`
	Remembered   bool   `json:"remembered"`
	UserAgent    string `json:"userAgent"`
	IPAddress    string `json:"ipAddress"`
	NetworkClass string `json:"networkClass"`
	Current      bool   `json:"current"`
	Legacy       bool   `json:"legacy,omitempty"`
}

type webSessionStore struct {
	mu         sync.Mutex
	path       string
	sessions   []storedWebSession
	tokenIndex map[string]int
	nextExpiry time.Time
	events     []storedWebSecurityEvent
	now        func() time.Time
	random     io.Reader
}

func openWebSessionStore(dataDir string) (*webSessionStore, error) {
	store := &webSessionStore{now: time.Now, random: rand.Reader, tokenIndex: map[string]int{}}
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return store, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Web session data directory: %w", err)
	}
	store.path = filepath.Join(dataDir, "webui-sessions.json")
	raw, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		if err := store.persistLocked(); err != nil {
			return nil, err
		}
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Web sessions: %w", err)
	}
	document, err := decodeWebSessionsDocument(raw)
	if err != nil {
		return nil, err
	}
	store.sessions = document.Sessions
	store.events = document.Events
	store.compactExpiredLocked(store.now().UTC())
	store.rebuildTokenIndexLocked()
	if err := store.persistLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func decodeWebSessionsDocument(raw []byte) (webSessionsDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document webSessionsDocument
	if err := decoder.Decode(&document); err != nil {
		return webSessionsDocument{}, fmt.Errorf("parse Web sessions: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return webSessionsDocument{}, errors.New("parse Web sessions: expected one JSON value")
	}
	if document.Version != webSessionsVersion {
		return webSessionsDocument{}, fmt.Errorf("unsupported Web sessions version %d", document.Version)
	}
	seenIDs := map[string]struct{}{}
	seenTokens := map[string]struct{}{}
	for index, session := range document.Sessions {
		if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.AccountID) == "" ||
			!validWebSecretHash(session.TokenHash) || !validWebSecretHash(session.CSRFHash) ||
			(session.DeviceIDHash != "" && !validWebSecretHash(session.DeviceIDHash)) ||
			(session.IPAddress != "" && net.ParseIP(session.IPAddress) == nil) {
			return webSessionsDocument{}, fmt.Errorf("invalid Web session at index %d", index)
		}
		if _, duplicate := seenIDs[session.ID]; duplicate {
			return webSessionsDocument{}, fmt.Errorf("duplicate Web session id %q", session.ID)
		}
		if _, duplicate := seenTokens[session.TokenHash]; duplicate {
			return webSessionsDocument{}, errors.New("duplicate Web session token hash")
		}
		created, createdErr := time.Parse(time.RFC3339Nano, session.CreatedAt)
		lastSeen, lastSeenErr := time.Parse(time.RFC3339Nano, session.LastSeenAt)
		idleExpiry, idleErr := time.Parse(time.RFC3339Nano, session.IdleExpiresAt)
		expiry, expiryErr := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		if createdErr != nil || lastSeenErr != nil || idleErr != nil || expiryErr != nil ||
			lastSeen.Before(created) || !idleExpiry.After(lastSeen) || !expiry.After(created) {
			return webSessionsDocument{}, fmt.Errorf("invalid Web session timestamps at index %d", index)
		}
		seenIDs[session.ID] = struct{}{}
		seenTokens[session.TokenHash] = struct{}{}
	}
	for index, event := range document.Events {
		if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.AccountID) == "" ||
			strings.TrimSpace(event.Type) == "" {
			return webSessionsDocument{}, fmt.Errorf("invalid Web security event at index %d", index)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.CreatedAt); err != nil {
			return webSessionsDocument{}, fmt.Errorf("invalid Web security event timestamp at index %d", index)
		}
	}
	if document.Sessions == nil {
		document.Sessions = []storedWebSession{}
	}
	if document.Events == nil {
		document.Events = []storedWebSecurityEvent{}
	}
	return document, nil
}

func (s *webSessionStore) Create(
	accountID, authMethod string,
	remember bool,
	metadata webSessionMetadata,
) (string, string, storedWebSession, error) {
	if s == nil {
		return "", "", storedWebSession{}, errors.New("Web session store is unavailable")
	}
	token, err := s.randomSecret()
	if err != nil {
		return "", "", storedWebSession{}, fmt.Errorf("generate Web session token: %w", err)
	}
	csrf, err := s.randomSecret()
	if err != nil {
		return "", "", storedWebSession{}, fmt.Errorf("generate Web CSRF token: %w", err)
	}
	now := s.now().UTC()
	absoluteTTL, idleTTL := webSessionAbsoluteTTL, webSessionIdleTTL
	if remember {
		absoluteTTL, idleTTL = webRememberedSessionTTL, webRememberedSessionIdleTTL
	}
	session := storedWebSession{
		ID: uuid.NewString(), AccountID: strings.TrimSpace(accountID),
		TokenHash: webSecretHash(token), CSRFHash: webSecretHash(csrf),
		AuthMethod: strings.TrimSpace(authMethod),
		CreatedAt:  now.Format(time.RFC3339Nano), LastSeenAt: now.Format(time.RFC3339Nano),
		IdleExpiresAt: now.Add(idleTTL).Format(time.RFC3339Nano),
		ExpiresAt:     now.Add(absoluteTTL).Format(time.RFC3339Nano),
		Remembered:    remember, UserAgent: metadata.UserAgent,
		UserAgentHash: metadata.UserAgentHash, DeviceIDHash: metadata.DeviceIDHash,
		IPAddress: metadata.IPAddress, NetworkClass: metadata.NetworkClass,
	}
	if session.AccountID == "" || session.AuthMethod == "" {
		return "", "", storedWebSession{}, errors.New("Web session identity and auth method are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previousSessions, previousEvents := s.snapshotLocked()
	s.compactExpiredLocked(now)
	kept := make([]storedWebSession, 0, len(s.sessions)+1)
	accountSessions := 0
	for _, existing := range s.sessions {
		if existing.AccountID == session.AccountID {
			accountSessions++
			if accountSessions >= webSessionMaxPerAccount {
				continue
			}
		}
		kept = append(kept, existing)
	}
	s.sessions = append([]storedWebSession{session}, kept...)
	s.rebuildTokenIndexLocked()
	s.recordEventLocked(storedWebSecurityEvent{
		AccountID: session.AccountID, SessionID: session.ID, Type: "login_succeeded",
		AuthMethod: session.AuthMethod, Success: true, UserAgent: session.UserAgent,
		NetworkClass: session.NetworkClass,
	})
	if err := s.persistLocked(); err != nil {
		s.restoreLocked(previousSessions, previousEvents)
		return "", "", storedWebSession{}, err
	}
	return token, csrf, session, nil
}

func (s *webSessionStore) Authenticate(token string, touch bool) (storedWebSession, bool, error) {
	if s == nil || strings.TrimSpace(token) == "" {
		return storedWebSession{}, false, nil
	}
	hash := webSecretHash(token)
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	var previousSessions []storedWebSession
	var previousEvents []storedWebSecurityEvent
	hasSnapshot := false
	changed := false
	if s.hasExpiredLocked(now) {
		previousSessions, previousEvents = s.snapshotLocked()
		hasSnapshot = true
		changed = s.compactExpiredLocked(now)
	}
	index, found := s.tokenIndex[hash]
	if found && index >= 0 && index < len(s.sessions) &&
		subtle.ConstantTimeCompare([]byte(s.sessions[index].TokenHash), []byte(hash)) == 1 {
		if touch {
			lastSeen, _ := time.Parse(time.RFC3339Nano, s.sessions[index].LastSeenAt)
			if now.Sub(lastSeen) >= webSessionTouchInterval {
				if !hasSnapshot {
					previousSessions, previousEvents = s.snapshotLocked()
					hasSnapshot = true
				}
				idleTTL := webSessionIdleTTL
				if s.sessions[index].Remembered {
					idleTTL = webRememberedSessionIdleTTL
				}
				absoluteExpiry, _ := time.Parse(time.RFC3339Nano, s.sessions[index].ExpiresAt)
				idleExpiry := now.Add(idleTTL)
				if idleExpiry.After(absoluteExpiry) {
					idleExpiry = absoluteExpiry
				}
				s.sessions[index].LastSeenAt = now.Format(time.RFC3339Nano)
				s.sessions[index].IdleExpiresAt = idleExpiry.Format(time.RFC3339Nano)
				changed = true
			}
		}
		if changed {
			s.rebuildTokenIndexLocked()
			if err := s.persistLocked(); err != nil {
				s.restoreLocked(previousSessions, previousEvents)
				return storedWebSession{}, false, err
			}
		}
		return s.sessions[index], true, nil
	}
	if changed {
		if err := s.persistLocked(); err != nil {
			s.restoreLocked(previousSessions, previousEvents)
			return storedWebSession{}, false, err
		}
	}
	return storedWebSession{}, false, nil
}

func (s *webSessionStore) hasExpiredLocked(now time.Time) bool {
	return !s.nextExpiry.IsZero() && !now.Before(s.nextExpiry)
}

func (s *webSessionStore) ValidateCSRF(token, presented string) (bool, error) {
	session, ok, err := s.Authenticate(token, false)
	if err != nil || !ok || strings.TrimSpace(presented) == "" {
		return false, err
	}
	presentedHash := webSecretHash(presented)
	return subtle.ConstantTimeCompare([]byte(session.CSRFHash), []byte(presentedHash)) == 1, nil
}

func (s *webSessionStore) RotateCSRF(token string) (string, error) {
	if s == nil || strings.TrimSpace(token) == "" {
		return "", errSynonLinkInvalidSession
	}
	csrf, err := s.randomSecret()
	if err != nil {
		return "", fmt.Errorf("generate rotated Web CSRF token: %w", err)
	}
	hash := webSecretHash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	index, found := s.tokenIndex[hash]
	if found && index >= 0 && index < len(s.sessions) &&
		subtle.ConstantTimeCompare([]byte(s.sessions[index].TokenHash), []byte(hash)) == 1 {
		previousSessions, previousEvents := s.snapshotLocked()
		s.sessions[index].CSRFHash = webSecretHash(csrf)
		if err := s.persistLocked(); err != nil {
			s.restoreLocked(previousSessions, previousEvents)
			return "", err
		}
		return csrf, nil
	}
	return "", errSynonLinkInvalidSession
}

func (s *webSessionStore) Events(accountID string, limit int) []storedWebSecurityEvent {
	if s == nil {
		return []storedWebSecurityEvent{}
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]storedWebSecurityEvent, 0, limit)
	for index := len(s.events) - 1; index >= 0 && len(result) < limit; index-- {
		if s.events[index].AccountID == accountID {
			result = append(result, s.events[index])
		}
	}
	return result
}

func (s *webSessionStore) Revoke(accountID, sessionID, currentToken string) (bool, bool, error) {
	if s == nil {
		return false, false, errors.New("Web session store is unavailable")
	}
	currentHash := webSecretHash(currentToken)
	s.mu.Lock()
	defer s.mu.Unlock()
	previousSessions, previousEvents := s.snapshotLocked()
	next := make([]storedWebSession, 0, len(s.sessions))
	revoked, current := false, false
	for _, session := range s.sessions {
		if session.AccountID == accountID && session.ID == sessionID {
			revoked = true
			current = subtle.ConstantTimeCompare([]byte(session.TokenHash), []byte(currentHash)) == 1
			s.recordEventLocked(storedWebSecurityEvent{
				AccountID: accountID, SessionID: session.ID, Type: "session_revoked",
				AuthMethod: session.AuthMethod, Success: true,
			})
			continue
		}
		next = append(next, session)
	}
	if !revoked {
		return false, false, nil
	}
	s.sessions = next
	s.rebuildTokenIndexLocked()
	if err := s.persistLocked(); err != nil {
		s.restoreLocked(previousSessions, previousEvents)
		return false, false, err
	}
	return true, current, nil
}

func (s *webSessionStore) RevokeAll(accountID, _ string) (int, error) {
	if s == nil {
		return 0, errors.New("Web session store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previousSessions, previousEvents := s.snapshotLocked()
	next := make([]storedWebSession, 0, len(s.sessions))
	revoked := 0
	for _, session := range s.sessions {
		if session.AccountID == accountID {
			revoked++
			continue
		}
		next = append(next, session)
	}
	if revoked == 0 {
		return 0, nil
	}
	s.sessions = next
	s.rebuildTokenIndexLocked()
	s.recordEventLocked(storedWebSecurityEvent{AccountID: accountID, Type: "all_sessions_revoked", Success: true})
	if err := s.persistLocked(); err != nil {
		s.restoreLocked(previousSessions, previousEvents)
		return 0, err
	}
	return revoked, nil
}

func (s *webSessionStore) RecordEvent(event storedWebSecurityEvent) error {
	if s == nil {
		return errors.New("Web session store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previousSessions, previousEvents := s.snapshotLocked()
	s.recordEventLocked(event)
	if err := s.persistLocked(); err != nil {
		s.restoreLocked(previousSessions, previousEvents)
		return err
	}
	return nil
}

func (s *webSessionStore) snapshotLocked() ([]storedWebSession, []storedWebSecurityEvent) {
	return append([]storedWebSession(nil), s.sessions...), append([]storedWebSecurityEvent(nil), s.events...)
}

func (s *webSessionStore) restoreLocked(
	sessions []storedWebSession,
	events []storedWebSecurityEvent,
) {
	s.sessions = sessions
	s.events = events
	s.rebuildTokenIndexLocked()
}

func (s *webSessionStore) recordEventLocked(event storedWebSecurityEvent) {
	if strings.TrimSpace(event.AccountID) == "" || strings.TrimSpace(event.Type) == "" {
		return
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.CreatedAt == "" {
		event.CreatedAt = s.now().UTC().Format(time.RFC3339Nano)
	}
	s.events = append(s.events, event)
	if len(s.events) > webSecurityEventRetention {
		s.events = append([]storedWebSecurityEvent(nil), s.events[len(s.events)-webSecurityEventRetention:]...)
	}
}

func (s *webSessionStore) compactExpiredLocked(now time.Time) bool {
	next := make([]storedWebSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		idleExpiry, idleErr := time.Parse(time.RFC3339Nano, session.IdleExpiresAt)
		absoluteExpiry, absoluteErr := time.Parse(time.RFC3339Nano, session.ExpiresAt)
		if idleErr != nil || absoluteErr != nil || !now.Before(idleExpiry) || !now.Before(absoluteExpiry) {
			continue
		}
		next = append(next, session)
	}
	changed := len(next) != len(s.sessions)
	s.sessions = next
	if changed {
		s.rebuildTokenIndexLocked()
	}
	return changed
}

func (s *webSessionStore) rebuildTokenIndexLocked() {
	if s.tokenIndex == nil {
		s.tokenIndex = make(map[string]int, len(s.sessions))
	} else {
		clear(s.tokenIndex)
	}
	s.nextExpiry = time.Time{}
	for index := range s.sessions {
		s.tokenIndex[s.sessions[index].TokenHash] = index
		idleExpiry, idleErr := time.Parse(time.RFC3339Nano, s.sessions[index].IdleExpiresAt)
		absoluteExpiry, absoluteErr := time.Parse(time.RFC3339Nano, s.sessions[index].ExpiresAt)
		if idleErr != nil || absoluteErr != nil {
			s.nextExpiry = time.Unix(0, 0).UTC()
			continue
		}
		expiry := idleExpiry
		if absoluteExpiry.Before(expiry) {
			expiry = absoluteExpiry
		}
		if s.nextExpiry.IsZero() || expiry.Before(s.nextExpiry) {
			s.nextExpiry = expiry
		}
	}
}

func (s *webSessionStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	document := webSessionsDocument{Version: webSessionsVersion, Sessions: s.sessions, Events: s.events}
	if document.Sessions == nil {
		document.Sessions = []storedWebSession{}
	}
	if document.Events == nil {
		document.Events = []storedWebSecurityEvent{}
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Web sessions: %w", err)
	}
	return replacePrivateJSONFile(s.path, append(raw, '\n'))
}
