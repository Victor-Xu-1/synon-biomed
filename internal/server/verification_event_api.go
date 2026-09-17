package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

type verificationEventRequest struct {
	Status     string                         `json:"status"`
	Reviewers  int                            `json:"n"`
	Verdict    string                         `json:"verdict"`
	Count      int                            `json:"count"`
	VersionIDs []string                       `json:"version_ids"`
	Checks     *[]workspace.VerificationCheck `json:"checks"`
	Claims     *[]workspace.SessionClaim      `json:"claims"`
}

func (s *Server) handleCompatibilityVerification(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	userID string,
) {
	if r.Method == http.MethodGet {
		status := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("status")))
		checks, err := s.workspaceStore.ListVerificationChecks(frame.RootFrameID, status)
		if err != nil {
			if strings.Contains(err.Error(), "status") {
				writeV11Detail(w, http.StatusBadRequest, err.Error())
				return
			}
			writeV11StoreError(w, err)
			return
		}
		claims, err := s.workspaceStore.ListSessionClaims(frame.RootFrameID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		running, err := s.workspaceStore.ListRunningVerification(frame.RootFrameID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"checks": checks, "claims": claims, "running": running})
		return
	}
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	provided := strings.TrimSpace(r.Header.Get("X-Synon-Verification-Token"))
	if s.verifierToken == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.verifierToken)) != 1 {
		writeV11Detail(w, http.StatusForbidden, "verification publication is not authorized")
		return
	}
	var body verificationEventRequest
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid verification update: "+err.Error())
		return
	}
	body.Status = strings.ToLower(strings.TrimSpace(body.Status))
	body.Verdict = strings.ToLower(strings.TrimSpace(body.Verdict))
	if body.Status != "reviewing" && body.Status != "done" {
		writeV11Detail(w, http.StatusBadRequest, "status must be reviewing or done")
		return
	}
	if body.Reviewers < 1 || body.Reviewers > 16 {
		writeV11Detail(w, http.StatusBadRequest, "n must be between 1 and 16")
		return
	}
	if body.Status == "done" {
		switch body.Verdict {
		case "pass", "warn", "fail", "inconclusive", "error":
		default:
			writeV11Detail(w, http.StatusBadRequest, "done verification updates require a valid verdict")
			return
		}
		if body.Count < 0 {
			writeV11Detail(w, http.StatusBadRequest, "count must not be negative")
			return
		}
	}
	if body.Checks != nil || body.Claims != nil {
		if body.Status != "done" {
			writeV11Detail(w, http.StatusBadRequest, "verification snapshots require status done")
			return
		}
		body.VersionIDs = append(body.VersionIDs, verificationCheckVersionIDs(body.Checks)...)
	}
	versionIDs, ok := normalizedVerificationVersionIDs(body.VersionIDs)
	if !ok {
		writeV11Detail(w, http.StatusBadRequest, "version_ids must contain at most 256 non-empty ids")
		return
	}
	if body.Checks != nil || body.Claims != nil {
		if err := s.workspaceStore.ReplaceVerificationSnapshot(frame.RootFrameID, workspace.VerificationSnapshot{Checks: body.Checks, Claims: body.Claims}); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid verification snapshot: "+err.Error())
			return
		}
	}
	payload := map[string]any{
		"type": "verification_update", "root_frame_id": frame.RootFrameID,
		"frame_id": frame.ID, "status": body.Status, "n": body.Reviewers,
	}
	if body.Status == "done" {
		payload["verdict"] = body.Verdict
		payload["count"] = body.Count
		payload["version_ids"] = versionIDs
	}
	event, err := s.publishCompatEvent(workspace.RealtimeEventInput{
		UserID: userID, ProjectID: frame.ProjectID, RootFrameID: frame.RootFrameID,
		FrameID: frame.ID, Type: "verification_update", Payload: payload,
	})
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, "failed to publish verification update")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"sequence": event.Sequence})
}

func normalizedVerificationVersionIDs(values []string) ([]string, bool) {
	if len(values) > 256 {
		return nil, false
	}
	output := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, false
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		output = append(output, value)
	}
	return output, true
}

func verificationCheckVersionIDs(checks *[]workspace.VerificationCheck) []string {
	if checks == nil {
		return nil
	}
	values := make([]string, 0)
	seen := map[string]bool{}
	for _, check := range *checks {
		if check.ArtifactVersionID == nil {
			continue
		}
		value := strings.TrimSpace(*check.ArtifactVersionID)
		if value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}
