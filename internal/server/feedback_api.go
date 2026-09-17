package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"synon-go/internal/buildinfo"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	maxGeneralFeedbackDescriptionRunes = 10000
	maxGeneralFeedbackMessages         = 1000
	maxSafetyFeedbackReasonCharacters  = 4000
	feedbackRequestTimeout             = 30 * time.Second
	maxFeedbackResponseBytes           = 1 << 20
	feedbackRPCPath                    = "/synon_llm.operon.api.v1alpha.OperonService/Feedback"
)

type FeedbackOptions struct {
	ServiceURL        string
	Token             string
	Beta              string
	Disabled          bool
	TelemetryDisabled bool
	TokenResolver     func(context.Context, string) (string, bool, error)
}

var (
	feedbackEmailPattern  = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	feedbackPhonePattern  = regexp.MustCompile(`(?:\+?[0-9]{1,3}[-. ]?)?\(?[0-9]{3}\)?[-. ][0-9]{3}[-. ][0-9]{4}`)
	feedbackSecretPattern = regexp.MustCompile(`(?i)(?:bearer\s+|sk-[a-z0-9_-]*|(?:api[_-]?key|token|secret|password)\s*[:=]\s*)[^\s,;"']+`)
)

func (s *Server) handleFeedbackAvailability(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	_, endpointAvailable := s.feedbackEndpoint()
	_, tokenAvailable, tokenErr := s.feedbackToken(r.Context(), userID)
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"available":        endpointAvailable && !s.feedback.Disabled && !s.feedback.TelemetryDisabled,
		"safety_available": tokenErr == nil && tokenAvailable && !s.feedback.Disabled && !s.feedback.TelemetryDisabled,
	})
}

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	store, userID, ok := s.feedbackRequestContext(w, r)
	if !ok {
		return
	}
	var input struct {
		FrameID           string `json:"frameId"`
		Sentiment         any    `json:"sentiment"`
		MessageIndex      *int   `json:"messageIndex"`
		APIMessageID      string `json:"apiMessageId"`
		Description       string `json:"description"`
		TelemetryConsent  *bool  `json:"telemetryConsent"`
		IncludeTranscript bool   `json:"includeTranscript"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	description := input.Description
	sentiment, hasSentiment, err := normalizeFeedbackSentiment(input.Sentiment)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	if utf8.RuneCountInString(description) > maxGeneralFeedbackDescriptionRunes {
		writeV11Detail(w, http.StatusBadRequest, "description must contain at most 10000 characters")
		return
	}
	if utf8.RuneCountInString(input.APIMessageID) > 128 {
		writeV11Detail(w, http.StatusBadRequest, "apiMessageId must contain at most 128 characters")
		return
	}
	if input.MessageIndex != nil && *input.MessageIndex < 0 {
		writeV11Detail(w, http.StatusBadRequest, "messageIndex must be a non-negative integer")
		return
	}
	if !hasSentiment && description == "" {
		writeV11Detail(w, http.StatusBadRequest, "Feedback requires at least one of sentiment or description.")
		return
	}
	if s.feedback.TelemetryDisabled || input.TelemetryConsent != nil && !*input.TelemetryConsent {
		writeV11Detail(w, http.StatusForbidden, "Feedback is disabled by a telemetry opt-out (DO_NOT_TRACK, OPERON_DISABLE_TELEMETRY, OPERON_DISABLE_NONESSENTIAL_TRAFFIC, settings.disable_telemetry, or declined telemetry consent).")
		return
	}
	if s.feedback.Disabled {
		writeV11Detail(w, http.StatusForbidden, "Feedback is disabled for this organization.")
		return
	}
	serviceURL, available := s.feedbackEndpoint()
	if !available {
		writeV11Detail(w, http.StatusServiceUnavailable, "Feedback endpoint is not usable (OPERON_SERVICE_URL set to an empty or non-https value).")
		return
	}

	frameID := strings.TrimSpace(input.FrameID)
	var transcript any
	verifiedResponseID := ""
	if frameID != "" {
		frame, found, loadErr := store.GetFrame(frameID)
		if loadErr != nil {
			writeV11Detail(w, workspaceStatus(loadErr), loadErr.Error())
			return
		}
		if !found || !workspaceProjectOwned(w, store, frame.ProjectID, userID) {
			if !found {
				writeV11Detail(w, http.StatusNotFound, "Frame "+frameID+" not found")
			}
			return
		}
		if input.IncludeTranscript || strings.TrimSpace(input.APIMessageID) != "" {
			messages, messagesErr := feedbackFrameMessages(store, frameID)
			if messagesErr != nil {
				writeV11Detail(w, workspaceStatus(messagesErr), messagesErr.Error())
				return
			}
			candidateResponseID := strings.TrimSpace(input.APIMessageID)
			for _, message := range messages {
				if strings.TrimSpace(stringValue(message["_response_id"])) == candidateResponseID {
					verifiedResponseID = candidateResponseID
					break
				}
			}
			if input.IncludeTranscript {
				transcript, err = feedbackTranscriptJSON(messages)
			}
			if err != nil {
				writeV11Detail(w, workspaceStatus(err), err.Error())
				return
			}
		}
	}
	token, found, tokenErr := s.feedbackToken(r.Context(), userID)
	if tokenErr != nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Feedback credential lookup failed.")
		return
	}
	if !found {
		writeV11Detail(w, http.StatusUnauthorized, "Feedback requires authentication; no credential available.")
		return
	}

	payload := map[string]any{
		"kind": "FEEDBACK_KIND_THUMBS", "sentiment": sentiment,
		"platform": feedbackPlatform(), "version": buildinfo.Release().Version,
	}
	if description != "" {
		payload["description"] = truncateFeedbackRunes(redactFeedbackString(description), maxGeneralFeedbackDescriptionRunes)
	}
	if hasSentiment {
		payload["reason"] = "thumbs/" + sentiment
	}
	if verifiedResponseID != "" {
		payload["response_id"] = verifiedResponseID
	}
	if frameID != "" {
		payload["frame_id"] = frameID
	}
	if transcript != nil {
		payload["transcript_json"] = transcript
	}
	feedbackID, err := s.deliverFeedback(r.Context(), serviceURL, token, payload)
	if err != nil {
		var deliveryError *feedbackDeliveryError
		if errors.As(err, &deliveryError) {
			writeV11Detail(w, deliveryError.Status, deliveryError.Message)
			return
		}
		writeV11Detail(w, http.StatusBadGateway, "Feedback service is unreachable.")
		return
	}
	auditPayload := map[string]any{
		"frameId": frameID, "sentiment": sentiment, "description": payload["description"],
		"apiMessageId": strings.TrimSpace(input.APIMessageID), "includeTranscript": input.IncludeTranscript,
		"messageIndex": input.MessageIndex, "delivery": "remote",
	}
	if _, err := store.SaveFeedback(workspace.FeedbackInput{ID: feedbackID, UserID: userID, Kind: "general", Payload: auditPayload}); err != nil {
		log.Printf("feedback audit persistence failed feedback_id=%q err=%v", feedbackID, err)
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"feedbackId": feedbackID})
}

type feedbackDeliveryError struct {
	Status  int
	Message string
}

func (e *feedbackDeliveryError) Error() string { return e.Message }

func normalizeFeedbackSentiment(value any) (string, bool, error) {
	if value == nil {
		return "", false, nil
	}
	sentiment, ok := value.(string)
	if !ok {
		return "", false, errors.New("sentiment must be a string")
	}
	sentiment = strings.TrimSpace(sentiment)
	if sentiment == "" {
		return "", false, nil
	}
	if sentiment != "up" && sentiment != "down" {
		return "", false, errors.New("sentiment must be up or down")
	}
	return sentiment, true, nil
}

func (s *Server) feedbackEndpoint() (*url.URL, bool) {
	if s == nil {
		return nil, false
	}
	parsed, err := url.Parse(strings.TrimSpace(s.feedback.ServiceURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, false
	}
	parsed.RawQuery, parsed.Fragment = "", ""
	parsed.Path = strings.TrimRight(parsed.Path, "/") + feedbackRPCPath
	return parsed, true
}

func (s *Server) feedbackToken(ctx context.Context, userID string) (string, bool, error) {
	if s == nil {
		return "", false, nil
	}
	if s.feedback.TokenResolver != nil {
		token, found, err := s.feedback.TokenResolver(ctx, userID)
		token = strings.TrimSpace(token)
		return token, found && token != "", err
	}
	token := strings.TrimSpace(s.feedback.Token)
	return token, token != "", nil
}

func feedbackFrameMessages(store *workspace.Store, frameID string) ([]map[string]any, error) {
	messages := make([]map[string]any, 0)
	for len(messages) < maxGeneralFeedbackMessages {
		page, err := store.CompatibilityFrameMessages(frameID, len(messages), 500)
		if err != nil {
			return nil, err
		}
		messages = append(messages, page.Messages...)
		if len(page.Messages) == 0 || len(messages) >= page.Total {
			break
		}
	}
	if len(messages) > maxGeneralFeedbackMessages {
		messages = messages[:maxGeneralFeedbackMessages]
	}
	return messages, nil
}

func feedbackTranscriptJSON(messages []map[string]any) (any, error) {
	transcript := make([]any, 0, len(messages))
	for _, message := range messages {
		if sanitized, keep := sanitizeFeedbackValue(message, 0); keep {
			transcript = append(transcript, sanitized)
		}
	}
	for len(transcript) > 0 {
		raw, err := json.Marshal(transcript)
		if err != nil {
			return nil, err
		}
		if len(raw) <= 48<<10 {
			return string(raw), nil
		}
		transcript = transcript[1:]
	}
	raw, err := json.Marshal(transcript)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func truncateFeedbackRunes(value string, limit int) string {
	values := []rune(value)
	if len(values) > limit {
		values = values[:limit]
	}
	return string(values)
}

func feedbackPlatform() string {
	architecture := runtime.GOARCH
	if architecture == "amd64" {
		architecture = "x64"
	}
	return runtime.GOOS + "-" + architecture
}

func (s *Server) deliverFeedback(ctx context.Context, endpoint *url.URL, token string, payload map[string]any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	requestContext, cancel := context.WithTimeout(ctx, feedbackRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint.String(), bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connect-Protocol-Version", "1")
	if beta := strings.TrimSpace(s.feedback.Beta); beta != "" {
		request.Header.Set("synon_llm-beta", beta)
	}
	client := s.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFeedbackResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxFeedbackResponseBytes {
		return "", &feedbackDeliveryError{Status: http.StatusBadGateway, Message: "Feedback service returned an oversized response."}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &feedbackDeliveryError{Status: http.StatusBadGateway, Message: fmt.Sprintf("Feedback service returned status %d.", response.StatusCode)}
	}
	result := map[string]any{}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &result); err != nil {
			return "", &feedbackDeliveryError{Status: http.StatusBadGateway, Message: "Feedback service returned invalid JSON."}
		}
	}
	feedbackID := strings.TrimSpace(firstNonEmpty(stringValue(result["feedback_id"]), stringValue(result["feedbackId"])))
	if feedbackID == "" {
		feedbackID = "local-" + uuid.NewString()
	}
	return feedbackID, nil
}

func (s *Server) handleSafetyFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	store, userID, ok := s.feedbackRequestContext(w, r)
	if !ok {
		return
	}
	var input struct {
		RootFrameID     json.RawMessage `json:"root_frame_id"`
		Model           string          `json:"model"`
		Reason          string          `json:"reason"`
		ShareTranscript bool            `json:"share_transcript"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	rootFrameID := ""
	rawRootFrameID := strings.TrimSpace(string(input.RootFrameID))
	if rawRootFrameID != "" && rawRootFrameID != "null" {
		if err := json.Unmarshal(input.RootFrameID, &rootFrameID); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "root_frame_id must be a string")
			return
		}
	}
	rootFrameID = strings.TrimSpace(rootFrameID)
	input.Reason = strings.TrimSpace(input.Reason)
	input.Model = strings.TrimSpace(input.Model)
	if rootFrameID == "" {
		writeV11Detail(w, http.StatusBadRequest, "root_frame_id is required")
		return
	}
	if utf8.RuneCountInString(input.Model) > 255 {
		writeV11Detail(w, http.StatusBadRequest, "model must contain at most 255 characters")
		return
	}
	if utf8.RuneCountInString(input.Reason) > maxSafetyFeedbackReasonCharacters {
		writeV11Detail(w, http.StatusBadRequest, "reason must contain at most 4000 characters")
		return
	}
	frame, found, err := store.GetFrame(rootFrameID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"error": err.Error()})
		return
	}
	if !found || !workspaceProjectOwned(w, store, frame.ProjectID, userID) {
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Frame "+rootFrameID+" not found")
		}
		return
	}
	if frame.ParentFrameID != "" || frame.RootFrameID != frame.ID {
		writeV11Detail(w, http.StatusNotFound, "Frame "+rootFrameID+" not found")
		return
	}
	output, hasOutput, err := store.GetFrameOutputData(frame.ID)
	if err != nil {
		writeV11Detail(w, workspaceStatus(err), err.Error())
		return
	}
	if frame.Status != "failed" || !hasOutput || strings.TrimSpace(stringValue(output["stop_reason"])) != "refusal" {
		writeV11Detail(w, http.StatusBadRequest, "Frame "+rootFrameID+" is not in a safety-refusal state")
		return
	}
	payload := map[string]any{
		"root_frame_id":    rootFrameID,
		"model":            nullableFeedbackString(input.Model),
		"reason":           nullableFeedbackString(redactFeedbackString(input.Reason)),
		"share_transcript": input.ShareTranscript,
	}
	if input.ShareTranscript {
		snapshot, snapshotErr := safetyFeedbackTranscript(store, frame.ID)
		if snapshotErr != nil {
			writeV11Detail(w, workspaceStatus(snapshotErr), snapshotErr.Error())
			return
		}
		payload["context_snapshot"] = snapshot
	}
	record, alreadySubmitted, err := store.SaveSafetyFeedback(workspace.FeedbackInput{
		UserID: userID, Kind: "safety", RootFrameID: rootFrameID, Payload: payload,
	})
	if err != nil {
		writeV11Detail(w, workspaceStatus(err), err.Error())
		return
	}
	result := map[string]any{
		"id": record.ID, "root_frame_id": record.RootFrameID,
		"created_at": record.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
	if alreadySubmitted {
		result["already_submitted"] = true
	} else {
		result["wire_delivered"] = false
	}
	writeWorkspaceJSON(w, http.StatusCreated, result)
}

func nullableFeedbackString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func safetyFeedbackTranscript(store *workspace.Store, frameID string) ([]any, error) {
	events, err := store.ListFrameEvents(frameID, 0, 1000)
	if err != nil {
		return nil, err
	}
	snapshot := make([]any, 0, len(events))
	for _, event := range events {
		value, keep := sanitizeFeedbackValue(map[string]any{
			"type": event.Type, "payload": event.Payload,
		}, 0)
		if keep {
			snapshot = append(snapshot, value)
		}
	}
	for len(snapshot) > 0 {
		raw, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(raw) <= 48<<10 {
			break
		}
		snapshot = snapshot[1:]
	}
	return snapshot, nil
}

func sanitizeFeedbackValue(value any, depth int) (any, bool) {
	if depth > 24 {
		return "[nested content omitted]", true
	}
	switch typed := value.(type) {
	case map[string]any:
		kind := strings.ToLower(strings.TrimSpace(stringValue(typed["type"])))
		if kind == "thinking" || kind == "redacted_thinking" {
			return nil, false
		}
		if kind == "image" {
			if source, ok := typed["source"].(map[string]any); ok && stringValue(source["type"]) == "_disk_ref" {
				return map[string]any{"type": "text", "text": "[image omitted - local file]"}, true
			}
		}
		clean := make(map[string]any, len(typed))
		for key, child := range typed {
			if strings.HasPrefix(key, "_") {
				continue
			}
			if sanitized, keep := sanitizeFeedbackValue(child, depth+1); keep {
				clean[key] = sanitized
			}
		}
		return clean, true
	case []any:
		clean := make([]any, 0, len(typed))
		for _, child := range typed {
			if sanitized, keep := sanitizeFeedbackValue(child, depth+1); keep {
				clean = append(clean, sanitized)
			}
		}
		return clean, true
	case string:
		return redactFeedbackString(typed), true
	default:
		return value, true
	}
}

func redactFeedbackString(value string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		value = strings.ReplaceAll(value, home, "~")
	}
	value = feedbackEmailPattern.ReplaceAllString(value, "[REDACTED_EMAIL]")
	value = feedbackPhonePattern.ReplaceAllString(value, "[REDACTED_PHONE]")
	return feedbackSecretPattern.ReplaceAllString(value, "[REDACTED_TOKEN]")
}

func (s *Server) feedbackRequestContext(w http.ResponseWriter, r *http.Request) (*workspace.Store, string, bool) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return nil, "", false
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return nil, "", false
	}
	return store, userID, true
}
