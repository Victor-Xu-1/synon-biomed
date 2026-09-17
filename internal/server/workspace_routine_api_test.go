package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceRoutineHTTPAPIClaimsAndCompletesTick(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: "project-1", AgentName: "planner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	now := time.Now().UTC().Truncate(time.Second)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/routines", map[string]any{
		"id": "routine-1", "rootFrameId": "frame-1", "ownerUserId": "local",
		"label": "Daily", "onTick": "summarize", "everyMinutes": 60,
		"enabled": true, "nextDue": now.Add(-time.Minute),
	}, http.StatusOK)
	claim := claimRoutineForTest(t, app, now, "http-claim-token")
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/routines/routine-1/complete", map[string]any{
		"at": now, "successful": true, "result": "completed", "claimToken": claim.ClaimToken,
		"claimGeneration": claim.ClaimGeneration, "lockedAt": claim.LockedAt,
	}, http.StatusOK)

	response := httptest.NewRecorder()
	app.ServeHTTP(response, localWorkspaceRequest(http.MethodGet, "/api/go/routines/routine-1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("\"tickCount\":1")) {
		t.Fatalf("routine = %d: %s", response.Code, response.Body.String())
	}
}

type routineHTTPClaim struct {
	ClaimToken      string    `json:"claimToken"`
	ClaimGeneration int64     `json:"claimGeneration"`
	LockedAt        time.Time `json:"lockedAt"`
}

func claimRoutineForTest(t *testing.T, app http.Handler, now time.Time, token string) routineHTTPClaim {
	t.Helper()
	claimBody, err := json.Marshal(map[string]any{"now": now, "lockTtlSeconds": 30, "claimToken": token})
	if err != nil {
		t.Fatal(err)
	}
	claimResponse := httptest.NewRecorder()
	claimRequest := localWorkspaceRequest(http.MethodPost, "/api/go/routines/claim", bytes.NewReader(claimBody))
	claimRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(claimResponse, claimRequest)
	if claimResponse.Code != http.StatusOK {
		t.Fatalf("claim status=%d: %s", claimResponse.Code, claimResponse.Body.String())
	}
	var claim routineHTTPClaim
	if err := json.NewDecoder(claimResponse.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	return claim
}
