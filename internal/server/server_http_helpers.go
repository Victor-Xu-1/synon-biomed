package server

import (
	"encoding/json"

	"net/http"

	"strconv"

	"synon-go/internal/synonlink"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"error":   code,
		"message": message,
	})
}

func resolveUserID(r *http.Request, body map[string]any) string {
	if value := r.Header.Get("X-Synon-User-Id"); value != "" {
		return value
	}
	if value := r.URL.Query().Get("userId"); value != "" {
		return value
	}
	if value := r.URL.Query().Get("user_id"); value != "" {
		return value
	}
	if value, ok := body["userId"].(string); ok {
		return value
	}
	return ""
}

func stringInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func synonLinkWarnings([]synonlink.Client) []string { return nil }
