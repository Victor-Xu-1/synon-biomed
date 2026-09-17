package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestKernelHostAppRelaysThroughAuthenticatedLiveViewerProtocol(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	createKernelHostAppConnector(t, store, identity.access.UserID)

	registration := mcpAppHTTPJSON(t, app.Handler(), http.MethodPost, "/api/mcp/apps/registrations", identity.access.UserID, map[string]any{
		"root_frame_id": identity.access.Frame.RootFrameID,
		"frame_id":      identity.access.Frame.ID,
		"server":        "viewer-fixture",
		"artifact_id":   "artifact-live-1",
		"tools": []any{map[string]any{
			"name": "highlight_atoms", "description": "Highlight atoms",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"atoms": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				},
				"required": []any{"atoms"}, "additionalProperties": false,
			},
		}},
	}, http.StatusCreated)
	registrationID := stringValue(registration["registration_id"])
	if registrationID == "" {
		t.Fatalf("registration=%#v", registration)
	}

	type kernelCompletion struct {
		result map[string]any
		err    error
	}
	completed := make(chan kernelCompletion, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		result, err := app.executeAgentKernelTool(ctx, identity, "repl", map[string]any{
			"code": `
import host, json
print(json.dumps(host.app("viewer-fixture").tools(), sort_keys=True))
print(json.dumps(host.app("viewer-fixture").highlight_atoms(
    artifact_id="artifact-live-1", atoms=[0, 2, 4]
), sort_keys=True))
`,
		})
		completed <- kernelCompletion{result: result, err: err}
	}()
	time.Sleep(500 * time.Millisecond)
	select {
	case completion := <-completed:
		t.Fatalf("kernel completed before the live viewer request: result=%#v err=%v", completion.result, completion.err)
	default:
	}

	request := mcpAppHTTPJSON(
		t, app.Handler(), http.MethodGet,
		"/api/mcp/apps/requests?registration_id="+registrationID,
		identity.access.UserID, nil, http.StatusOK,
	)
	if request["tool"] != "highlight_atoms" || request["artifact_id"] != "artifact-live-1" {
		t.Fatalf("live viewer request=%#v", request)
	}
	arguments, _ := request["arguments"].(map[string]any)
	atoms, _ := arguments["atoms"].([]any)
	if len(atoms) != 3 || atoms[0] != float64(0) || atoms[2] != float64(4) {
		t.Fatalf("live viewer arguments=%#v", arguments)
	}
	mcpAppHTTPNoContent(t, app.Handler(), http.MethodPost, "/api/mcp/apps/results", identity.access.UserID, map[string]any{
		"registration_id": registrationID,
		"request_id":      request["request_id"],
		"structured_content": map[string]any{
			"highlighted_atoms": []any{0, 2, 4}, "count": 3,
		},
		"content":  []any{map[string]any{"type": "text", "text": "highlighted"}},
		"is_error": false,
	})

	select {
	case completion := <-completed:
		if completion.err != nil {
			t.Fatal(completion.err)
		}
		if completion.result["ok"] != true {
			t.Fatalf("kernel completion=%#v", completion.result)
		}
		stdout, _ := completion.result["stdout"].(string)
		if !strings.Contains(stdout, `"artifact_id": "artifact-live-1"`) ||
			!strings.Contains(stdout, `"count": 3`) ||
			!strings.Contains(stdout, `"highlighted_atoms": [0, 2, 4]`) {
			t.Fatalf("kernel stdout=%q", stdout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("kernel did not resume after live viewer response")
	}
}

func TestMCPAppRegistrationProtocolIsOwnerScopedAndFailClosed(t *testing.T) {
	store, manager, app, identity := newKernelHostTestRuntime(t, filepath.Join(t.TempDir(), "workspace.db"), true)
	defer closeKernelHostTestRuntime(t, app, manager, store)
	createKernelHostAppConnector(t, store, identity.access.UserID)

	body := map[string]any{
		"root_frame_id": identity.access.Frame.RootFrameID,
		"server":        "custom:viewer-fixture",
		"artifact_id":   "artifact-live-1",
		"tools": []any{map[string]any{
			"name":        "highlight_atoms",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false},
		}},
	}
	foreign := mcpAppHTTPJSON(t, app.Handler(), http.MethodPost, "/api/mcp/apps/registrations", "foreign-owner", body, http.StatusNotFound)
	if !strings.Contains(stringValue(foreign["detail"]), "not found") {
		t.Fatalf("foreign registration=%#v", foreign)
	}
	registration := mcpAppHTTPJSON(t, app.Handler(), http.MethodPost, "/api/mcp/apps/registrations", identity.access.UserID, body, http.StatusCreated)
	registrationID := stringValue(registration["registration_id"])
	foreignDelete := mcpAppHTTPJSON(
		t, app.Handler(), http.MethodDelete,
		"/api/mcp/apps/registrations?registration_id="+registrationID,
		"foreign-owner", nil, http.StatusNotFound,
	)
	if !strings.Contains(stringValue(foreignDelete["detail"]), "not found") {
		t.Fatalf("foreign delete=%#v", foreignDelete)
	}
	mcpAppHTTPNoContent(
		t, app.Handler(), http.MethodDelete,
		"/api/mcp/apps/registrations?registration_id="+registrationID,
		identity.access.UserID, nil,
	)
}

func createKernelHostAppConnector(t *testing.T, store *workspace.Store, userID string) {
	t.Helper()
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "custom:viewer-fixture", UserID: userID, Name: "viewer-fixture",
		Description: "live MCP app protocol fixture",
		URL:         "https://example.test/mcp", Transport: "streamable-http",
	}); err != nil {
		t.Fatal(err)
	}
}

func mcpAppHTTPJSON(
	t *testing.T,
	handler http.Handler,
	method, target, userID string,
	body any,
	wantStatus int,
) map[string]any {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(payload))
	request.Header.Set("X-Synon-User-Id", userID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status=%d body=%s", method, target, response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s %s: %v body=%s", method, target, err, response.Body.String())
	}
	return decoded
}

func mcpAppHTTPNoContent(t *testing.T, handler http.Handler, method, target, userID string, body any) {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(payload))
	request.Header.Set("X-Synon-User-Id", userID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("%s %s status=%d body=%s", method, target, response.Code, response.Body.String())
	}
}
