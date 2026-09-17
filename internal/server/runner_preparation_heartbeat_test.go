package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestSessionRunnerPreparationHeartbeatRetainsClaimAcrossDiscovery(t *testing.T) {
	for _, mode := range []string{"complete", "drain", "heartbeat_failure"} {
		t.Run(mode, func(t *testing.T) {
			store, repo, db := newTranscriptWebFixture(t)
			seedTranscriptWebFrame(t, store, "local", "preparation-project", "preparation-frame")
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var enteredOnce, cancelledOnce, releaseOnce sync.Once
			releaseDiscovery := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseDiscovery()
			catalog := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode discovery: %v", err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var result any
				switch request.Method {
				case "server/discover":
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID,
						"error": map[string]any{"code": -32601, "message": "Method not found"}})
					return
				case "initialize":
					result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}}
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
					return
				case "tools/list":
					enteredOnce.Do(func() { close(entered) })
					select {
					case <-release:
					case <-r.Context().Done():
						cancelledOnce.Do(func() { close(cancelled) })
						return
					}
					result = map[string]any{"tools": []map[string]any{{"name": "echo", "inputSchema": map[string]any{"type": "object"}}}}
				default:
					t.Errorf("unexpected discovery method %q", request.Method)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			}))
			t.Cleanup(catalog.Close)
			catalogURL, client := mcpDirectoryPublicTLSClient(t, catalog)
			enabled := true
			if _, err := store.CreateAgent(workspace.CreateAgentInput{
				ID: "preparation-agent", UserID: "local", Name: "synon", DisplayName: "Preparation fixture",
				Description: "Discover the controlled local MCP fixture.", SystemPrompt: "Reply ready.", Unrestricted: true, Enabled: &enabled,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateMCPServer(workspace.MCPServerInput{
				ID: "slow-preparation", UserID: "local", Name: "slow", URL: catalogURL, Transport: "streamable-http", Enabled: &enabled,
			}); err != nil {
				t.Fatal(err)
			}
			var modelRequests atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				modelRequests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ready"}}]}`))
			}))
			t.Cleanup(provider.Close)
			app := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), HTTPClient: client})
			t.Cleanup(func() { closeTestServer(t, app) })
			connectors, err := app.mcpDirectory.ListUnifiedConnectors(t.Context(), "local")
			if err != nil {
				t.Fatal(err)
			}
			for _, connector := range connectors {
				if connector.Source == "bundled" {
					if _, err := app.mcpDirectory.SetUnifiedEnabled(t.Context(), "local", connector.ID, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, _, err := app.submitFrameMessage(store, frameMessageSubmission{
				FrameID: "preparation-frame", MessageUUID: "preparation-input", ClientMessageID: "preparation-input", Text: "Reply ready.",
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			type outcome struct {
				result SessionRunnerCycleResult
				err    error
			}
			done := make(chan outcome, 1)
			const ttl = 2 * time.Second
			go func() {
				result, err := app.RunSessionRunnerChatOnce(ctx, SessionRunnerChatOptions{
					SessionID: "preparation-frame", RunnerID: "preparation-runner", Endpoint: provider.URL,
					Model: "test-model", APIKey: "test-key", MaxAttempts: 1, LeaseTTL: ttl,
					PreparationTimeout: 10 * time.Second, DisableSkillDiscovery: true,
				})
				done <- outcome{result, err}
			}()
			select {
			case <-entered:
			case result := <-done:
				t.Fatalf("runner stopped before controlled discovery: %#v err=%v", result.result, result.err)
			case <-ctx.Done():
				t.Fatal("controlled MCP discovery did not start")
			}
			stream, found, err := repo.GetFrameStreamBySession(ctx, "local", "preparation-frame")
			if err != nil || !found {
				t.Fatalf("preparation stream found=%t err=%v", found, err)
			}
			initial, found, err := repo.GetLatestRunnerRuntimeState(ctx, stream.UID, stream.OwnerID)
			if err != nil || !found {
				t.Fatalf("preparation claim found=%t err=%v", found, err)
			}
			if mode == "heartbeat_failure" {
				// Reject only renewal of this still-valid claim. Checkpoints,
				// terminal writes, and reads remain available in the real SQLite DB.
				if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_preparation_heartbeat
					BEFORE UPDATE OF expires_at ON transcript_runner_attempts
					WHEN OLD.status='running' AND NEW.expires_at>OLD.expires_at
					BEGIN SELECT RAISE(ABORT, 'controlled heartbeat renewal failure'); END`); err != nil {
					t.Fatal(err)
				}
				select {
				case outcome := <-done:
					state, found, stateErr := repo.GetLatestRunnerRuntimeState(ctx, stream.UID, stream.OwnerID)
					t.Logf("heartbeat failure result=%#v err=%v state=%#v", outcome.result, outcome.err, state)
					if stateErr != nil || !found || state.Status != "running" || !state.ExpiresAt.After(time.Now().UTC()) {
						t.Fatalf("renewal fault did not leave a live, nonterminal claim: state=%#v found=%t err=%v", state, found, stateErr)
					}
					var finished int
					if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transcript_events
						WHERE stream_uid=? AND event_type='runner_finished'`, stream.UID).Scan(&finished); err != nil {
						t.Fatal(err)
					}
					if finished != 0 || outcome.result.Status == "cancelled" || outcome.result.Status == "canceled" || modelRequests.Load() != 0 {
						t.Fatalf("renewal fault became a user terminal or model call: finished=%d result=%#v modelRequests=%d", finished, outcome.result, modelRequests.Load())
					}
					if outcome.err == nil || !strings.Contains(outcome.err.Error(), "renew transcript runner lease") ||
						!strings.Contains(outcome.err.Error(), "controlled heartbeat renewal failure") {
						t.Fatalf("preparation discarded the real heartbeat failure: %v", outcome.err)
					}
				case <-ctx.Done():
					t.Fatal("runner did not return its preparation heartbeat failure")
				}
				select {
				case <-cancelled:
				case <-ctx.Done():
					t.Fatal("heartbeat failure did not cancel the real MCP request")
				}
				return
			}
			originalExpiry := initial.ClaimedAt.Add(ttl)
			timer := time.NewTimer(time.Until(originalExpiry.Add(150 * time.Millisecond)))
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				t.Fatal("preparation ended before crossing the original lease")
			}
			state, _, err := repo.GetLatestRunnerRuntimeState(ctx, stream.UID, stream.OwnerID)
			if err != nil || state.Status != "running" || !state.ExpiresAt.After(time.Now().UTC()) || !state.ExpiresAt.After(originalExpiry) {
				t.Fatalf("preparation lost its live lease: state=%#v originalExpiry=%s err=%v", state, originalExpiry, err)
			}
			if modelRequests.Load() != 0 {
				t.Fatal("model ran before controlled discovery completed")
			}
			if mode == "drain" {
				if err := app.Drain(ctx); err != nil {
					t.Fatalf("drain preparation: %v", err)
				}
				select {
				case <-cancelled:
				case <-ctx.Done():
					t.Fatal("runtime drain did not cancel the real MCP request")
				}
			} else {
				releaseDiscovery()
			}
			select {
			case outcome := <-done:
				if outcome.err != nil {
					t.Fatal(outcome.err)
				}
				if mode == "drain" {
					if outcome.result.Status != "interrupted" || outcome.result.InterruptionReasonCode != "runtime_draining" || modelRequests.Load() != 0 {
						t.Fatalf("drained preparation=%#v modelRequests=%d", outcome.result, modelRequests.Load())
					}
				} else if outcome.result.Status != "completed" || modelRequests.Load() != 1 {
					t.Fatalf("prepared runner=%#v modelRequests=%d", outcome.result, modelRequests.Load())
				}
			case <-ctx.Done():
				t.Fatal("runner did not settle after controlled preparation")
			}
		})
	}
}
