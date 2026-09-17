package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	runnermachine "synon-go/internal/sessionrunner"
)

func TestSessionRunnerRealModelToolRoundFollowsCanonicalPhases(t *testing.T) {
	t.Setenv("SYNON_WEBSEARCH_MODE", "disabled")
	var calls atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"phase-search","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"phase task\"}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"phase run completed"}}]}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	server := New(Options{FileRoot: root})
	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "phase-session", Title: "Phase session", WorkDir: root,
		CreatedAt: now, UpdatedAt: now, LastUserMessageAt: now, MessageCount: 1, LastRole: "user",
	}
	if err := server.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	claimedSession, claimed, err := server.sessionStore.ClaimRunner(session.ID, "phase-runner", time.Minute)
	if err != nil || !claimed || claimedSession.Runner == nil {
		t.Fatalf("claim phase session claimed=%t err=%v session=%#v", claimed, err, claimedSession)
	}
	entries := []eventjournal.Entry{{
		SessionID: session.ID, EventID: 1,
		Message: eventjournal.Message{"type": "message", "role": "user", "text": "create one task and report completion"},
	}}
	run := &sessionRunnerChatRun{
		SessionID: session.ID, Attempt: claimedSession.Runner.Attempt,
		ClaimToken: sessionstore.RunnerClaimFromSession(claimedSession).ClaimToken,
	}
	result, err := server.runSessionRunnerChat(context.Background(), SessionRunnerChatOptions{
		RunnerID:         "phase-runner",
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "phase-model",
		AllowedTools:     []string{"web_search"},
		MaxToolRounds:    2,
		MaxAttempts:      1,
		ReplayLimit:      20,
		OutputLimitBytes: 64 * 1024,
	}, session, entries, run)
	if err != nil {
		t.Fatalf("runSessionRunnerChat: %v", err)
	}
	if result != "phase run completed" || calls.Load() != 2 {
		t.Fatalf("result=%q calls=%d", result, calls.Load())
	}
	history := run.phaseMachine.History()
	observed := make([]runnermachine.Phase, len(history))
	for index, transition := range history {
		observed[index] = transition.To
	}
	want := []runnermachine.Phase{
		runnermachine.PhaseClaim,
		runnermachine.PhaseRecovery,
		runnermachine.PhaseContext,
		runnermachine.PhaseSnapshot,
		runnermachine.PhaseProvider,
		runnermachine.PhaseTool,
		runnermachine.PhaseProvider,
		runnermachine.PhaseVerify,
		runnermachine.PhaseComplete,
	}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("runner phases = %v, want %v", observed, want)
	}
}
