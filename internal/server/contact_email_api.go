package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	workspace "synon-go/internal/persistence/workspace"
)

const contactEmailNoticeText = `Some research data services (such as those run by NCBI, EBI, and OurResearch) ask for a contact email on API requests so they can reach out about problematic traffic. Synon Biomed can send one on your behalf — whether the request comes from built-in tools or from code the agent writes for you.

If you provide an address:
- It is saved locally in your Synon Biomed data directory.
- It is used as the contact parameter on requests to such services.
- It becomes part of the LLM session's context.
- You can change it at any time; a changed address applies immediately.
- You can remove it at any time in Settings; after removal Synon Biomed asks again before any future use.

If you decline, these services are used without an email where possible, and the agent will not ask you for one.`

var contactEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func (s *Server) handleContactEmail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "workspace store is not configured"})
		return
	}
	userID := preferenceUserID(r)
	switch r.Method {
	case http.MethodGet:
		decision, found, err := s.workspaceStore.LatestContactEmailDecision(userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "Could not load the contact email. Please retry."})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, contactEmailResponse(decision, found))
	case http.MethodPut:
		var input struct {
			Email         string `json:"email"`
			NoticeVersion string `json:"notice_version"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid contact email request"})
			return
		}
		email := strings.TrimSpace(input.Email)
		if !validContactEmail(email) {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"detail": "A valid email address is required."})
			return
		}
		noticeVersion := contactEmailNoticeVersion()
		if input.NoticeVersion != noticeVersion {
			writeWorkspaceJSON(w, http.StatusConflict, map[string]any{
				"detail":         "The contact-email disclosure has changed since this page loaded — re-read it before agreeing.",
				"notice_text":    contactEmailNoticeText,
				"notice_version": noticeVersion,
			})
			return
		}
		decision, err := s.workspaceStore.RecordContactEmailDecision(workspace.RecordContactEmailDecisionInput{
			UserID: userID, Decision: workspace.ContactEmailDecisionAllowed, Email: email,
			NoticeVersion: noticeVersion, NoticeText: contactEmailNoticeText,
		})
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "Could not save the contact email. Please retry."})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, contactEmailResponse(decision, true))
	case http.MethodDelete:
		latest, found, err := s.workspaceStore.LatestContactEmailDecision(userID)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "Could not remove the contact email. Please retry."})
			return
		}
		if !found || latest.Decision == workspace.ContactEmailDecisionRevoked {
			writeWorkspaceJSON(w, http.StatusOK, contactEmailResponse(latest, found))
			return
		}
		decision, err := s.workspaceStore.RecordContactEmailDecision(workspace.RecordContactEmailDecisionInput{
			UserID: userID, Decision: workspace.ContactEmailDecisionRevoked,
			NoticeVersion: contactEmailNoticeVersion(), NoticeText: contactEmailNoticeText,
		})
		if err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"detail": "Could not remove the contact email. Please retry."})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, contactEmailResponse(decision, true))
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "method not allowed"})
	}
}

func contactEmailResponse(decision workspace.ContactEmailDecision, found bool) map[string]any {
	var state any
	var email any
	stale := false
	if found {
		state = decision.Decision
		if decision.Decision == workspace.ContactEmailDecisionAllowed {
			email = decision.Email
			stale = decision.NoticeVersion != contactEmailNoticeVersion()
		}
	}
	return map[string]any{
		"decision":       state,
		"email":          email,
		"notice_text":    contactEmailNoticeText,
		"notice_version": contactEmailNoticeVersion(),
		"notice_stale":   stale,
	}
}

func contactEmailNoticeVersion() string {
	digest := sha256.Sum256([]byte(contactEmailNoticeText))
	return hex.EncodeToString(digest[:])
}

func validContactEmail(value string) bool {
	return value != "" && len(value) <= 320 &&
		strings.IndexFunc(value, unicode.IsControl) < 0 && contactEmailPattern.MatchString(value)
}
