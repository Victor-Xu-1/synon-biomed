package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type webConversationToolDetailCursor struct {
	Version    int    `json:"v"`
	MessageID  string `json:"message_id"`
	BranchID   string `json:"branch_id"`
	Section    string `json:"section"`
	Path       string `json:"path"`
	Revision   int64  `json:"revision"`
	Offset     int    `json:"offset"`
	ByteOffset int64  `json:"byte_offset"`
	Total      int    `json:"total"`
	Kind       string `json:"kind"`
}

type webConversationToolDetailRequest struct {
	Section  string
	BranchID string
	Path     string
	Offset   int
	Revision int64
	Cursor   *webConversationToolDetailCursor
}

func webConversationToolDetailRequestedBranch(r *http.Request) (string, int, error) {
	branchID := strings.TrimSpace(r.URL.Query().Get("branch_id"))
	if branchID != "" && !validWebTranscriptBranchID(branchID) {
		return "", http.StatusConflict, errors.New("conversation branch changed; refresh and retry")
	}
	cursorValue := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if cursorValue == "" {
		return branchID, http.StatusOK, nil
	}
	cursor, err := decodeWebConversationToolDetailCursor(cursorValue)
	if err != nil {
		return "", http.StatusBadRequest, errors.New("invalid tool detail cursor")
	}
	if branchID != "" && branchID != cursor.BranchID {
		return "", http.StatusBadRequest, errors.New("tool detail cursor conflicts with request")
	}
	return cursor.BranchID, http.StatusOK, nil
}

func parseWebConversationToolDetailRequest(
	r *http.Request,
	messageID string,
	content map[string]any,
	resolvedBranchID string,
) (webConversationToolDetailRequest, int, error) {
	request := webConversationToolDetailRequest{
		Revision: webConversationToolDetailRevision(content["revision"]),
		BranchID: strings.TrimSpace(resolvedBranchID),
		Section:  strings.TrimSpace(r.URL.Query().Get("section")),
		Path:     strings.TrimSpace(r.URL.Query().Get("path")),
	}
	cursorValue := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if cursorValue != "" {
		cursor, decodeErr := decodeWebConversationToolDetailCursor(cursorValue)
		if decodeErr != nil || cursor.MessageID != messageID || cursor.BranchID != request.BranchID ||
			cursor.Revision != request.Revision {
			return webConversationToolDetailRequest{}, http.StatusBadRequest, errors.New("invalid tool detail cursor")
		}
		if request.Section != "" && request.Section != cursor.Section || request.Path != "" && request.Path != cursor.Path {
			return webConversationToolDetailRequest{}, http.StatusBadRequest, errors.New("tool detail cursor conflicts with request")
		}
		request.Section, request.Path, request.Offset, request.Cursor = cursor.Section, cursor.Path, cursor.Offset, &cursor
	}
	if request.BranchID == "" || !validWebTranscriptBranchID(request.BranchID) {
		return webConversationToolDetailRequest{}, http.StatusConflict, errors.New("conversation branch changed; refresh and retry")
	}
	if request.Section == "" {
		request.Section = "output"
	}
	if request.Section != "input" && request.Section != "output" {
		return webConversationToolDetailRequest{}, http.StatusBadRequest, errors.New("invalid tool detail section")
	}
	if expected := strings.TrimSpace(r.URL.Query().Get("revision")); expected != "" {
		parsed, parseErr := strconv.ParseInt(expected, 10, 64)
		if parseErr != nil || parsed < 0 {
			return webConversationToolDetailRequest{}, http.StatusBadRequest, errors.New("invalid tool detail revision")
		}
		if parsed != request.Revision {
			return webConversationToolDetailRequest{}, http.StatusConflict, errors.New("tool detail revision changed")
		}
	}
	return request, http.StatusOK, nil
}

func parseWebConversationToolDetailPath(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 1024 || !strings.HasPrefix(value, "/") {
		return nil, errors.New("invalid tool detail path")
	}
	raw := strings.Split(value[1:], "/")
	if len(raw) > 64 {
		return nil, errors.New("invalid tool detail path")
	}
	segments := make([]string, 0, len(raw))
	for _, segment := range raw {
		if len(segment) > 128 || !validWebConversationToolDetailPathEscapes(segment) {
			return nil, errors.New("invalid tool detail path")
		}
		segments = append(segments, strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~"))
	}
	return segments, nil
}

func validWebConversationToolDetailPathEscapes(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] != '~' {
			continue
		}
		if index+1 >= len(value) || (value[index+1] != '0' && value[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}

func appendWebConversationToolDetailPath(path, segment string) string {
	return path + "/" + segment
}

func escapeWebConversationToolDetailPath(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func encodeWebConversationToolDetailCursor(cursor webConversationToolDetailCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeWebConversationToolDetailCursor(value string) (webConversationToolDetailCursor, error) {
	if len(value) > 2048 {
		return webConversationToolDetailCursor{}, errors.New("invalid cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return webConversationToolDetailCursor{}, err
	}
	var cursor webConversationToolDetailCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.MessageID == "" ||
		!validWebTranscriptBranchID(cursor.BranchID) ||
		(cursor.Section != "input" && cursor.Section != "output") ||
		(cursor.Kind != "array" && cursor.Kind != "object") || cursor.Offset <= 0 ||
		cursor.Total <= cursor.Offset || cursor.ByteOffset <= 0 {
		return webConversationToolDetailCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func webConversationToolDetailRevision(value any) int64 {
	switch typed := value.(type) {
	case float64:
		if typed >= 0 && typed == float64(int64(typed)) {
			return int64(typed)
		}
	case json.Number:
		parsed, _ := typed.Int64()
		if parsed >= 0 {
			return parsed
		}
	case int:
		if typed >= 0 {
			return int64(typed)
		}
	case int64:
		if typed >= 0 {
			return typed
		}
	}
	return 0
}
