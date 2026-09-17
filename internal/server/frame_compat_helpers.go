package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func newCompatibilityProjectID() (string, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate project id: %w", err)
	}
	return "proj_" + hex.EncodeToString(random), nil
}

func compatPositiveQuery(r *http.Request, key string, fallback, maximum int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func compatNonNegativeQuery(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func nullableCompatibilityString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func writeV11Detail(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{"detail": detail})
}

func writeV11StoreError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "Internal workspace error"
	var requestError *webConversationRequestError
	switch {
	case errors.As(err, &requestError):
		status = requestError.Status
		message = requestError.Detail
	case errors.Is(err, workspace.ErrReadCursorConflict), errors.Is(err, transcriptstore.ErrBranchStateStale),
		errors.Is(err, transcriptstore.ErrEventConflict), errors.Is(err, transcriptstore.ErrTranscriptWebProjectionStale),
		errors.Is(err, errReadCursorProjectionFailed):
		status = http.StatusConflict
		message = "Read cursor authority changed"
	case strings.Contains(err.Error(), "does not exist"):
		status = http.StatusNotFound
		message = err.Error()
	case strings.Contains(err.Error(), "required"), strings.Contains(err.Error(), "invalid"), strings.Contains(err.Error(), "non-negative"):
		status = http.StatusBadRequest
		message = err.Error()
	}
	writeV11Detail(w, status, message)
}
