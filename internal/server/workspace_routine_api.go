package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWorkspaceRoutines(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		ID           string    `json:"id"`
		RootFrameID  string    `json:"rootFrameId"`
		OwnerUserID  string    `json:"ownerUserId"`
		Label        string    `json:"label"`
		OnTick       string    `json:"onTick"`
		EveryMinutes int       `json:"everyMinutes"`
		Enabled      bool      `json:"enabled"`
		NextDue      time.Time `json:"nextDue"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if strings.TrimSpace(input.OwnerUserID) != "" && strings.TrimSpace(input.OwnerUserID) != userID {
		writeWorkspaceJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "routine owner does not match authenticated user"})
		return
	}
	routine, err := store.CreateRoutineRealtime(r.Context(), workspace.CreateRoutineInput{
		ID: input.ID, RootFrameID: input.RootFrameID, OwnerUserID: userID,
		Label: input.Label, OnTick: input.OnTick, EveryMinutes: input.EveryMinutes,
		Enabled: input.Enabled, NextDue: input.NextDue,
	}, "")
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "routine": routine})
}

func (s *Server) handleWorkspaceRoutine(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/routines/"))
	if len(segments) == 1 && segments[0] == "claim" && r.Method == http.MethodPost {
		var input struct {
			Now            time.Time `json:"now"`
			LockTTLSeconds int       `json:"lockTtlSeconds"`
			ClaimToken     string    `json:"claimToken"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		claimToken := strings.TrimSpace(input.ClaimToken)
		if claimToken == "" {
			claimToken = strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		}
		if claimToken == "" {
			claimToken = uuid.NewString()
		}
		routine, claimed, err := store.ClaimNextDueRoutineRealtime(r.Context(), input.Now, time.Duration(input.LockTTLSeconds)*time.Second, userID, claimToken)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		response := map[string]any{"ok": true, "claimed": claimed, "routine": routine}
		if claimed {
			response["claimToken"] = routine.ClaimToken
			response["claimGeneration"] = routine.ClaimGeneration
			response["lockedAt"] = routine.LockedAt
		}
		writeWorkspaceJSON(w, http.StatusOK, response)
		return
	}
	if len(segments) == 0 || len(segments) > 2 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	routineID, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid routine id"})
		return
	}
	if len(segments) == 1 && r.Method == http.MethodGet {
		routine, err := store.GetRoutine(routineID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if routine.OwnerUserID != userID {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "routine not found"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "routine": routine})
		return
	}
	if len(segments) == 2 && segments[1] == "complete" && r.Method == http.MethodPost {
		var input struct {
			At              time.Time `json:"at"`
			Successful      bool      `json:"successful"`
			Result          string    `json:"result"`
			ClaimToken      string    `json:"claimToken"`
			ClaimGeneration int64     `json:"claimGeneration"`
			LockedAt        time.Time `json:"lockedAt"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if strings.TrimSpace(input.ClaimToken) == "" || input.ClaimGeneration < 1 || input.LockedAt.IsZero() {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "claimToken, claimGeneration, and lockedAt are required"})
			return
		}
		current, err := store.GetRoutine(routineID)
		if err != nil || current.OwnerUserID != userID {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "routine not found"})
			return
		}
		claim := current
		claim.ClaimToken, claim.ClaimGeneration = input.ClaimToken, input.ClaimGeneration
		lockedAt := input.LockedAt.UTC()
		claim.LockedAt = &lockedAt
		routine, err := store.CompleteRoutineTickRealtime(r.Context(), claim, input.At, input.Successful, input.Result)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "routine": routine})
		return
	}
	writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
}
