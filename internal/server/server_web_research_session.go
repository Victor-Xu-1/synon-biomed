package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"
)

type webResearchSessionState struct {
	ID                       string
	OwnerID                  string
	UpdatedAt                time.Time
	SeenCanonicalURLs        map[string]bool
	FetchedCanonicalURLs     map[string]bool
	UnavailableCanonicalURLs map[string]bool
}

const webResearchSessionIdleTTL = 24 * time.Hour
const maxWebResearchSessions = 1024

type webResearchSessionRef struct {
	State *webResearchSessionState
	Mode  string
}

func (s *Server) resolveWebResearchSession(ctx context.Context, input map[string]any) (*webResearchSessionRef, error) {
	raw := mapValue(input["research_session"])
	if len(raw) == 0 {
		return nil, nil
	}
	ownerID := webResearchSessionOwner(ctx)
	mode := strings.TrimSpace(stringValue(raw["mode"]))
	id := strings.TrimSpace(stringValue(raw["id"]))
	if mode == "" {
		if id != "" {
			mode = "continue"
		} else {
			mode = "start"
		}
	}
	s.webResearchMu.Lock()
	defer s.webResearchMu.Unlock()
	if s.webResearchSessions == nil {
		s.webResearchSessions = map[string]*webResearchSessionState{}
	}
	now := time.Now().UTC()
	for sessionID, state := range s.webResearchSessions {
		if state == nil || (!state.UpdatedAt.IsZero() && now.Sub(state.UpdatedAt) > webResearchSessionIdleTTL) {
			delete(s.webResearchSessions, sessionID)
		}
	}
	if mode == "reset" && id != "" {
		if existing := s.webResearchSessions[id]; existing != nil && existing.OwnerID != "" && ownerID != existing.OwnerID {
			return nil, errors.New("web research session is not available")
		}
		delete(s.webResearchSessions, id)
	}
	if mode == "continue" && id != "" {
		if existing := s.webResearchSessions[id]; existing != nil {
			if existing.OwnerID != "" && ownerID != existing.OwnerID {
				return nil, errors.New("web research session is not available")
			}
			existing.UpdatedAt = now
			return &webResearchSessionRef{State: existing, Mode: mode}, nil
		}
		return nil, errors.New("web research session is not available")
	}
	if id == "" {
		var token [18]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, errors.New("create web research session")
		}
		id = "wr_" + base64.RawURLEncoding.EncodeToString(token[:])
	}
	if _, exists := s.webResearchSessions[id]; exists {
		return nil, errors.New("web research session already exists")
	}
	if len(s.webResearchSessions) >= maxWebResearchSessions {
		oldestID := ""
		var oldest time.Time
		for sessionID, state := range s.webResearchSessions {
			if state != nil && (oldestID == "" || state.UpdatedAt.Before(oldest)) {
				oldestID, oldest = sessionID, state.UpdatedAt
			}
		}
		if oldestID != "" {
			delete(s.webResearchSessions, oldestID)
		}
	}
	state := &webResearchSessionState{
		ID:                       id,
		OwnerID:                  ownerID,
		UpdatedAt:                now,
		SeenCanonicalURLs:        map[string]bool{},
		FetchedCanonicalURLs:     map[string]bool{},
		UnavailableCanonicalURLs: map[string]bool{},
	}
	s.webResearchSessions[id] = state
	return &webResearchSessionRef{State: state, Mode: mode}, nil
}

func webResearchSessionOwner(ctx context.Context) string {
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.Transcript == nil {
		return ""
	}
	return strings.TrimSpace(run.Transcript.Stream.OwnerID)
}

func (s *Server) recordWebResearchSessionSources(session *webResearchSessionRef, sources []any) {
	if session == nil || session.State == nil {
		return
	}
	s.webResearchMu.Lock()
	defer s.webResearchMu.Unlock()
	session.State.UpdatedAt = time.Now().UTC()
	for _, raw := range sources {
		item := mapValue(raw)
		canonical := canonicalWebResearchURL(stringValue(item["url"]))
		if canonical == "" {
			continue
		}
		session.State.SeenCanonicalURLs[canonical] = true
		switch stringValue(item["status"]) {
		case "fetched":
			session.State.FetchedCanonicalURLs[canonical] = true
		case "sourceUnavailable":
			session.State.UnavailableCanonicalURLs[canonical] = true
		}
	}
}

func summarizeWebResearchSession(session *webResearchSessionRef) any {
	if session == nil || session.State == nil {
		return nil
	}
	return map[string]any{
		"id":                 session.State.ID,
		"mode":               session.Mode,
		"seenSources":        float64(len(session.State.SeenCanonicalURLs)),
		"fetchedSources":     float64(len(session.State.FetchedCanonicalURLs)),
		"unavailableSources": float64(len(session.State.UnavailableCanonicalURLs)),
	}
}

func canonicalWebResearchURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	values := parsed.Query()
	for key := range values {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" {
			values.Del(key)
		}
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func webResearchIndependentDomain(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}
