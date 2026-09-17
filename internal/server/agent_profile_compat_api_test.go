package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestAgentProfileCompatibilityAPIEndToEnd(t *testing.T) {
	database := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	app := newV11TestServer(t, Options{Workspace: store}).Handler()

	if got := agentCompatJSON(t, app, http.MethodGet, "/api/agents/OPERON/custom-prompt", nil, http.StatusOK); got != nil {
		t.Fatalf("initial custom prompt = %#v, want null", got)
	}
	prompt := agentCompatJSON(t, app, http.MethodPut, "/api/agents/OPERON/custom-prompt", map[string]any{
		"prompt_text": "Always cite primary evidence.",
	}, http.StatusOK).(map[string]any)
	if prompt["agent_name"] != "OPERON" || prompt["prompt_text"] != "Always cite primary evidence." || prompt["created_at"] == "" || prompt["updated_at"] == "" {
		t.Fatalf("custom prompt = %#v", prompt)
	}
	if got := agentCompatJSON(t, app, http.MethodGet, "/api/agents/OPERON/custom-prompt", nil, http.StatusOK).(map[string]any); got["prompt_text"] != prompt["prompt_text"] {
		t.Fatalf("persisted custom prompt = %#v", got)
	}
	agent := agentCompatJSON(t, app, http.MethodPost, "/api/agents", map[string]any{
		"name": "profile_oracle", "displayName": "Profile Oracle", "description": "Agent profile contract fixture",
		"systemPrompt": "Use the profile fixture.", "skillNames": []string{}, "unrestricted": false,
		"iconKey": "flask", "colorKey": "green", "tags": []string{"fixture"}, "enabled": true,
	}, http.StatusOK).(map[string]any)
	if agent["name"] != "PROFILE_ORACLE" || agent["nameNormalized"] != true || agent["source"] != "user" || agent["displayName"] != "Profile Oracle" || agent["unrestricted"] != false {
		t.Fatalf("created agent = %#v", agent)
	}
	if _, ok := agent["id"].(string); !ok {
		t.Fatalf("created agent id = %#v", agent["id"])
	}

	agent = agentCompatJSON(t, app, http.MethodPatch, "/api/agents/PROFILE_ORACLE", map[string]any{
		"displayName": "Updated Oracle", "description": "Updated fixture", "systemPrompt": "Updated prompt.",
	}, http.StatusOK).(map[string]any)
	if agent["displayName"] != "Updated Oracle" || agent["description"] != "Updated fixture" || agent["systemPrompt"] != "Updated prompt." {
		t.Fatalf("updated agent = %#v", agent)
	}
	agent = agentCompatJSON(t, app, http.MethodPost, "/api/agents/PROFILE_ORACLE/enabled", map[string]any{"enabled": false}, http.StatusOK).(map[string]any)
	if agent["enabled"] != false {
		t.Fatalf("disabled agent = %#v", agent)
	}

	agent = agentCompatJSON(t, app, http.MethodPost, "/api/agents/PROFILE_ORACLE/skills", map[string]any{"skill_name": "literature-review"}, http.StatusOK).(map[string]any)
	assertAgentSkills(t, agent, []string{"literature-review"})
	agent = agentCompatJSON(t, app, http.MethodPut, "/api/agents/PROFILE_ORACLE/skills", map[string]any{
		"attach": []string{"citation-management", "patent-search"}, "detach": []string{"literature-review"},
	}, http.StatusOK).(map[string]any)
	assertAgentSkills(t, agent, []string{"citation-management", "patent-search"})
	agent = agentCompatJSON(t, app, http.MethodDelete, "/api/agents/PROFILE_ORACLE/skills/patent-search", nil, http.StatusOK).(map[string]any)
	assertAgentSkills(t, agent, []string{"citation-management"})

	listed := agentCompatJSON(t, app, http.MethodGet, "/api/agents?names=PROFILE_ORACLE%2COPERON", nil, http.StatusOK).([]any)
	if len(listed) != 2 || listed[0].(map[string]any)["name"] != "OPERON" || listed[1].(map[string]any)["name"] != "PROFILE_ORACLE" {
		t.Fatalf("merged agent list = %#v", listed)
	}

	agentCompatJSON(t, app, http.MethodDelete, "/api/agents/PROFILE_ORACLE", nil, http.StatusNoContent)
	agentCompatJSON(t, app, http.MethodDelete, "/api/agents/OPERON/custom-prompt", nil, http.StatusNoContent)
	if got := agentCompatJSON(t, app, http.MethodGet, "/api/agents/OPERON/custom-prompt", nil, http.StatusOK); got != nil {
		t.Fatalf("deleted custom prompt = %#v, want null", got)
	}
	if response := agentCompatRequest(t, app, http.MethodPost, "/api/agents/OPERON/enabled", map[string]any{"enabled": false}); response.Code < 400 {
		t.Fatalf("OPERON disable status=%d body=%s", response.Code, response.Body.String())
	}
	if response := agentCompatRequest(t, app, http.MethodDelete, "/api/agents/OPERON", nil); response.Code < 400 {
		t.Fatalf("OPERON delete status=%d body=%s", response.Code, response.Body.String())
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, found, err := reopened.GetAgent("local", "PROFILE_ORACLE"); err != nil || found {
		t.Fatalf("deleted profile after restart: found=%v err=%v", found, err)
	}
}

func TestAgentProfileCompatibilityAPIRejectsUnknownSkillAndEmptyPrompt(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := newV11TestServer(t, Options{Workspace: store}).Handler()

	if response := agentCompatRequest(t, app, http.MethodPut, "/api/agents/OPERON/custom-prompt", map[string]any{"prompt_text": "  "}); response.Code != http.StatusBadRequest {
		t.Fatalf("empty prompt status=%d body=%s", response.Code, response.Body.String())
	}
	agentCompatJSON(t, app, http.MethodPost, "/api/agents", map[string]any{
		"name": "VALID_AGENT", "displayName": "Valid", "description": "Valid agent", "skillNames": []string{},
	}, http.StatusOK)
	if response := agentCompatRequest(t, app, http.MethodPost, "/api/agents/VALID_AGENT/skills", map[string]any{"skill_name": "not-a-real-skill"}); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown skill status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBundledOperonProfileMaterializationIsOwnerScoped(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := newV11TestServer(t, Options{Workspace: store}).Handler()
	profileIDs := make(map[string]string, 2)
	for _, ownerID := range []string{"owner-a", "owner-b"} {
		result := agentCompatJSONForUser(
			t,
			handler,
			ownerID,
			http.MethodDelete,
			"/api/agents/"+bundledOperonID+"/connectors/bundled%3Abiomart",
			nil,
			http.StatusOK,
		).(map[string]any)
		if result["ok"] != true {
			t.Fatalf("owner %q connector detach response = %#v", ownerID, result)
		}
		agent, found, err := store.GetAgent(ownerID, "OPERON")
		if err != nil || !found {
			t.Fatalf("owner %q OPERON profile found=%v err=%v", ownerID, found, err)
		}
		if len(agent.ConnectorTombstones) != 1 || agent.ConnectorTombstones[0] != "bundled:biomart" {
			t.Fatalf("owner %q connector tombstones = %#v", ownerID, agent.ConnectorTombstones)
		}
		profileIDs[ownerID] = agent.ID
		projection := agentCompatJSONForUser(
			t,
			handler,
			ownerID,
			http.MethodPost,
			"/api/agents/"+bundledOperonID+"/enabled",
			map[string]any{"enabled": true},
			http.StatusOK,
		).(map[string]any)
		if projection["id"] != bundledOperonID || projection["name"] != "OPERON" {
			t.Fatalf("owner %q public OPERON projection = %#v", ownerID, projection)
		}
	}
	if profileIDs["owner-a"] == "" || profileIDs["owner-a"] == profileIDs["owner-b"] {
		t.Fatalf("owner-scoped profile IDs = %#v", profileIDs)
	}
}

func TestAgentProfileMaterializationErrorsAreTypedAndRedacted(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	handler := New(Options{Workspace: store}).Handler()
	missing := agentCompatRequestForUser(
		t,
		handler,
		"owner-a",
		http.MethodPost,
		"/api/agents/NOT_REAL/enabled",
		map[string]any{"enabled": true},
	)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing agent status=%d body=%s", missing.Code, missing.Body.String())
	}
	var missingBody map[string]any
	if err := json.NewDecoder(missing.Body).Decode(&missingBody); err != nil {
		t.Fatal(err)
	}
	if len(missingBody) != 1 || missingBody["detail"] != "agent not found" {
		t.Fatalf("missing agent body = %#v", missingBody)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	failed := agentCompatRequestForUser(
		t,
		handler,
		"owner-a",
		http.MethodDelete,
		"/api/agents/OPERON/connectors/bundled%3Abiomart",
		nil,
	)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure status=%d body=%s", failed.Code, failed.Body.String())
	}
	var failedBody map[string]any
	if err := json.NewDecoder(failed.Body).Decode(&failedBody); err != nil {
		t.Fatal(err)
	}
	if len(failedBody) != 1 || failedBody["detail"] != "agent profile storage failed" {
		t.Fatalf("storage failure body = %#v", failedBody)
	}
}

func TestAgentConnectorCompatibilityAPIUsesToolExclusionsAndDurableTombstones(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "mcp-fixture", UserID: "local", Name: "fixture", URL: "https://mcp.example.test", Transport: "streamable-http",
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	agentCompatJSON(t, app, http.MethodPost, "/api/agents", map[string]any{
		"name": "CONNECTOR_AGENT", "displayName": "Connector Agent", "description": "Connector contract fixture", "skillNames": []string{},
	}, http.StatusOK)

	attached := agentCompatJSON(t, app, http.MethodPost, "/api/agents/CONNECTOR_AGENT/connectors", map[string]any{
		"server_id": "mcp-fixture",
	}, http.StatusOK).(map[string]any)
	if attached["ok"] != true {
		t.Fatalf("attach response = %#v", attached)
	}
	updated := agentCompatJSON(t, app, http.MethodPut, "/api/agents/CONNECTOR_AGENT/connectors/mcp-fixture/exclusions", map[string]any{
		"excludedTools": []string{"mcp_fixture_search", "mcp_fixture_fetch", "mcp_fixture_search"},
	}, http.StatusOK).(map[string]any)
	if got := updated["aggregated"].([]any); len(got) != 2 || got[0] != "mcp_fixture_search" || got[1] != "mcp_fixture_fetch" {
		t.Fatalf("aggregated exclusions = %#v", got)
	}
	got := agentCompatJSON(t, app, http.MethodGet, "/api/agents/CONNECTOR_AGENT/excluded-tools", nil, http.StatusOK).([]any)
	if len(got) != 2 || got[0] != "mcp_fixture_search" || got[1] != "mcp_fixture_fetch" {
		t.Fatalf("get exclusions = %#v", got)
	}
	if response := agentCompatRequest(t, app, http.MethodPut, "/api/agents/CONNECTOR_AGENT/connectors/not-attached/exclusions", map[string]any{"excludedTools": []string{"x"}}); response.Code != http.StatusNotFound {
		t.Fatalf("not-attached exclusions status=%d body=%s", response.Code, response.Body.String())
	}

	agentCompatJSON(t, app, http.MethodDelete, "/api/agents/CONNECTOR_AGENT/connectors/mcp-fixture", nil, http.StatusOK)
	agent, found, err := store.GetAgent("local", "CONNECTOR_AGENT")
	if err != nil || !found || len(agent.ConnectorTombstones) != 1 || agent.ConnectorTombstones[0] != "mcp-fixture" {
		t.Fatalf("detached agent = %#v found=%v err=%v", agent, found, err)
	}
	if exclusions, err := store.GetAgentConnectorToolExclusions("local", "CONNECTOR_AGENT"); err != nil || len(exclusions) != 0 {
		t.Fatalf("exclusions after detach = %#v err=%v", exclusions, err)
	}
	agentCompatJSON(t, app, http.MethodPost, "/api/agents/CONNECTOR_AGENT/connectors", map[string]any{"server_id": "mcp-fixture"}, http.StatusOK)
	agent, _, err = store.GetAgent("local", "CONNECTOR_AGENT")
	if err != nil || len(agent.ConnectorTombstones) != 0 {
		t.Fatalf("reattached agent = %#v err=%v", agent, err)
	}
}

func agentCompatJSON(t *testing.T, handler http.Handler, method, path string, body any, wantStatus int) any {
	t.Helper()
	return agentCompatJSONForUser(t, handler, "local", method, path, body, wantStatus)
}

func agentCompatJSONForUser(t *testing.T, handler http.Handler, userID, method, path string, body any, wantStatus int) any {
	t.Helper()
	response := agentCompatRequestForUser(t, handler, userID, method, path, body)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, wantStatus, response.Body.String())
	}
	if wantStatus == http.StatusNoContent {
		if response.Body.Len() != 0 {
			t.Fatalf("%s %s returned body for 204: %q", method, path, response.Body.String())
		}
		return nil
	}
	var value any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatalf("decode %s %s: %v body=%q", method, path, err, response.Body.String())
	}
	return value
}

func agentCompatRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return agentCompatRequestForUser(t, handler, "local", method, path, body)
}

func agentCompatRequestForUser(t *testing.T, handler http.Handler, userID, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("X-Synon-User-Id", userID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertAgentSkills(t *testing.T, agent map[string]any, want []string) {
	t.Helper()
	raw, ok := agent["skillNames"].([]any)
	if !ok || len(raw) != len(want) {
		t.Fatalf("skillNames=%#v want=%#v", agent["skillNames"], want)
	}
	for index := range want {
		if raw[index] != want[index] {
			t.Fatalf("skillNames=%#v want=%#v", raw, want)
		}
	}
}
