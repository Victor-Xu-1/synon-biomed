package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestFeedbackCompatibilityUsesRealHTTPSDeliveryAndDurableRedactedAudit(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "owner", Name: "Feedback"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: "frame", Type: "assistant_message", Payload: map[string]any{
		"role": "assistant", "content": "contact owner@example.test bearer top-secret", "_response_id": "api-message-1",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: "frame", Type: "thinking", Payload: map[string]any{
		"type": "thinking", "content": "private reasoning",
	}}); err != nil {
		t.Fatal(err)
	}

	delivered := make(chan map[string]any, 1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != feedbackRPCPath || r.Header.Get("Authorization") != "Bearer feedback-token" ||
			r.Header.Get("Connect-Protocol-Version") != "1" || r.Header.Get("synon_llm-beta") != "beta-a" {
			t.Errorf("feedback wire request path=%q headers=%#v", r.URL.Path, r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode feedback wire request: %v", err)
		}
		delivered <- payload
		writeJSON(w, http.StatusOK, map[string]any{"feedback_id": "feedback-remote-1"})
	}))
	t.Cleanup(upstream.Close)
	app := New(Options{
		Workspace: store, HTTPClient: upstream.Client(),
		Feedback: FeedbackOptions{ServiceURL: upstream.URL, Token: "feedback-token", Beta: "beta-a"},
	}).Handler()

	availability := runtimeCompatJSON(t, app, http.MethodGet, "/api/feedback/available", "owner", nil, http.StatusOK)
	if len(availability) != 2 || availability["available"] != true || availability["safety_available"] != true {
		t.Fatalf("feedback availability=%#v", availability)
	}
	result := runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback", "owner", map[string]any{
		"frameId": "frame", "sentiment": "up", "messageIndex": 1,
		"apiMessageId": "api-message-1", "description": "email owner@example.test token=top-secret",
		"includeTranscript": true,
	}, http.StatusOK)
	if len(result) != 1 || result["feedbackId"] != "feedback-remote-1" {
		t.Fatalf("feedback response=%#v", result)
	}
	payload := <-delivered
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(rawPayload)
	if payload["response_id"] != "api-message-1" || payload["reason"] != "thumbs/up" ||
		payload["platform"] != feedbackPlatform() {
		t.Fatalf("feedback wire contract=%#v", payload)
	}
	for _, forbidden := range []string{"owner@example.test", "top-secret", "private reasoning"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("feedback wire payload leaked %q: %s", forbidden, wire)
		}
	}
	for _, required := range []string{"FEEDBACK_KIND_THUMBS", "thumbs/up", "REDACTED_EMAIL", "REDACTED_TOKEN"} {
		if !strings.Contains(wire, required) {
			t.Fatalf("feedback wire payload missing %q: %s", required, wire)
		}
	}
	records, err := store.ListFeedbackForUser("owner", 10)
	if err != nil || len(records) != 1 || records[0].ID != "feedback-remote-1" || records[0].Kind != "general" {
		t.Fatalf("feedback audit records=%#v err=%v", records, err)
	}

	unconfigured := New(Options{Workspace: store}).Handler()
	unavailable := runtimeCompatJSON(t, unconfigured, http.MethodGet, "/api/feedback/available", "owner", nil, http.StatusOK)
	if len(unavailable) != 2 || unavailable["available"] != false || unavailable["safety_available"] != false {
		t.Fatalf("unconfigured availability=%#v", unavailable)
	}
	runtimeCompatJSON(t, unconfigured, http.MethodPost, "/api/feedback", "owner", map[string]any{
		"sentiment": "down",
	}, http.StatusServiceUnavailable)

	optedOut := New(Options{Workspace: store, Feedback: FeedbackOptions{
		ServiceURL: upstream.URL, Token: "feedback-token", TelemetryDisabled: true,
	}}).Handler()
	optedOutAvailability := runtimeCompatJSON(t, optedOut, http.MethodGet, "/api/feedback/available", "owner", nil, http.StatusOK)
	if optedOutAvailability["available"] != false || optedOutAvailability["safety_available"] != false {
		t.Fatalf("opted-out availability=%#v", optedOutAvailability)
	}
	runtimeCompatJSON(t, optedOut, http.MethodPost, "/api/feedback", "owner", map[string]any{
		"sentiment": "down",
	}, http.StatusForbidden)
	runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback", "owner", map[string]any{
		"sentiment": "down", "telemetryConsent": false,
	}, http.StatusForbidden)

}
func TestFeedbackCompatibilityRejectsForeignFramesAndUpstreamFailure(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "owned", UserID: "owner", Name: "Owned"},
		{ID: "foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "foreign-frame", ProjectID: "foreign", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)
	app := New(Options{Workspace: store, HTTPClient: upstream.Client(), Feedback: FeedbackOptions{
		ServiceURL: upstream.URL, Token: "feedback-token",
	}}).Handler()
	for _, invalid := range []map[string]any{
		{"sentiment": "sideways"},
		{"sentiment": "up", "messageIndex": -1},
		{"sentiment": "up", "apiMessageId": strings.Repeat("x", 129)},
		{"description": strings.Repeat("x", maxGeneralFeedbackDescriptionRunes+1)},
	} {
		runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback", "owner", invalid, http.StatusBadRequest)
	}

	runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback", "owner", map[string]any{
		"frameId": "foreign-frame", "sentiment": "down",
	}, http.StatusNotFound)
	runtimeCompatJSON(t, app, http.MethodPost, "/api/feedback", "owner", map[string]any{
		"description": "service failure",
	}, http.StatusBadGateway)
	records, err := store.ListFeedbackForUser("owner", 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("failed feedback created audit records=%#v err=%v", records, err)
	}
}
