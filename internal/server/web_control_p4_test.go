package server

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestP4AssistantListUsesPrivateConditionalCompression(t *testing.T) {
	app, _ := newP3WebConversationServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/assistants", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-cache" ||
		response.Header().Get("ETag") == "" || response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status=%d cache=%q etag=%q encoding=%q body=%q", response.Code,
			response.Header().Get("Cache-Control"), response.Header().Get("ETag"),
			response.Header().Get("Content-Encoding"), response.Body.String())
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := payload["data"].([]any)
	if payload["success"] != true || len(data) == 0 {
		t.Fatalf("payload=%#v", payload)
	}
	for _, value := range data {
		item, _ := value.(map[string]any)
		if _, exposesSystemPrompt := item["context"]; exposesSystemPrompt {
			t.Fatalf("assistant list exposed detail-only system prompt: %#v", item)
		}
	}

	cachedRequest := httptest.NewRequest(http.MethodGet, "/api/assistants", nil)
	cachedRequest.RemoteAddr = "127.0.0.1:12345"
	cachedRequest.Header.Set("Accept-Encoding", "gzip")
	cachedRequest.Header.Set("If-None-Match", response.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	app.Handler().ServeHTTP(cached, cachedRequest)
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		body, _ := io.ReadAll(cached.Body)
		t.Fatalf("cached status=%d body=%q", cached.Code, body)
	}
}

func TestP4AssistantRulesAndSkillAuthorityReachTheRunner(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	available := p4UserSkillNames(t, app, 2)
	createAssistant := p3JSONRequest(t, app, http.MethodPost, "/api/assistants", map[string]any{
		"name": "P4 Runtime Specialist", "description": "Exercises runtime authority.",
		"enabled_skills": []string{available[0]},
	}, "")
	if createAssistant.Code != http.StatusCreated {
		t.Fatalf("create assistant status=%d body=%s", createAssistant.Code, createAssistant.Body.String())
	}
	assistantEnvelope := p3DecodeObject(t, createAssistant)
	assistant, _ := assistantEnvelope["data"].(map[string]any)
	assistantID := webString(assistant["id"])
	if assistantID == "" {
		t.Fatalf("assistant response=%#v", assistantEnvelope)
	}

	rule := "Always cite the durable source event before reaching a conclusion."
	writeRule := p3JSONRequest(t, app, http.MethodPost, "/api/skills/assistant-rule/write", map[string]any{
		"assistant_id": assistantID, "content": rule, "locale": "en-US",
	}, "")
	if writeRule.Code != http.StatusOK {
		t.Fatalf("write rule status=%d body=%s", writeRule.Code, writeRule.Body.String())
	}
	project := createP3Project(t, store, "p4-authority-project", "local")
	createConversation := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Authority test", "assistant": map[string]any{
			"id": assistantID, "conversation_overrides": map[string]any{"skill_ids": []string{available[0]}},
		},
		"extra": map[string]any{"project_id": project.ID},
	}, "")
	if createConversation.Code != http.StatusCreated {
		t.Fatalf("create conversation status=%d body=%s", createConversation.Code, createConversation.Body.String())
	}
	conversationID := webString(p3DecodeObject(t, createConversation)["id"])
	options, selected, err := app.applySessionRunnerAgentProfile(
		sessionstore.Session{ID: conversationID, Orchestration: map[string]any{}},
		SessionRunnerChatOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(options.SystemPrompt, rule) || selected != assistantID || !options.RestrictSkillDiscovery {
		t.Fatalf("runner authority selected=%q prompt=%q options=%+v", selected, options.SystemPrompt, options)
	}
	if !p4ContainsFold(options.SelectedSkillNames, available[0]) || p4ContainsFold(options.AllowedSkillNames, available[1]) {
		t.Fatalf("runner skills selected=%v allowed=%v", options.SelectedSkillNames, options.AllowedSkillNames)
	}

	forbidden := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Try an unassigned skill", "inject_skills": []string{available[1]},
	}, "")
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("forbidden skill status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
	allowed := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Use the assigned skill", "inject_skills": []string{available[0]},
	}, "")
	if allowed.Code != http.StatusAccepted {
		t.Fatalf("allowed skill status=%d body=%s", allowed.Code, allowed.Body.String())
	}
	tooManySkills := make([]string, 65)
	for index := range tooManySkills {
		tooManySkills[index] = available[0]
	}
	overLimit := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Reject an unbounded composer selection", "inject_skills": tooManySkills,
	}, "")
	if overLimit.Code != http.StatusBadRequest {
		t.Fatalf("over-limit composer status=%d body=%s", overLimit.Code, overLimit.Body.String())
	}

	disable := p3JSONRequest(t, app, http.MethodPatch, "/api/assistants/"+assistantID+"/state", map[string]any{
		"enabled": false,
	}, "")
	if disable.Code != http.StatusOK {
		t.Fatalf("disable assistant status=%d body=%s", disable.Code, disable.Body.String())
	}
	afterDisable := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Must not run after disable",
	}, "")
	if afterDisable.Code != http.StatusConflict {
		t.Fatalf("disabled assistant status=%d body=%s", afterDisable.Code, afterDisable.Body.String())
	}
}

func TestP4AssistantDefaultsRejectUnavailableRuntimeAuthority(t *testing.T) {
	app, _ := newP3WebConversationServer(t)
	available := p4UserSkillNames(t, app, 2)
	tests := []struct {
		name     string
		defaults map[string]any
		models   []string
	}{
		{
			name: "skill outside assistant authority",
			defaults: map[string]any{
				"skills": map[string]any{"mode": "fixed", "value": []string{available[1]}},
			},
		},
		{
			name: "unknown MCP",
			defaults: map[string]any{
				"mcps": map[string]any{"mode": "fixed", "value": []string{"missing-mcp"}},
			},
		},
		{
			name: "unknown configured model",
			defaults: map[string]any{
				"model": map[string]any{"mode": "fixed", "value": "missing-model"},
			},
			models: []string{"known-model"},
		},
		{
			name: "unsupported permission",
			defaults: map[string]any{
				"permission": map[string]any{"mode": "fixed", "value": "allow-everything"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{
				"name": "Invalid " + test.name, "description": "Must be rejected.",
				"enabled_skills": []string{available[0]}, "defaults": test.defaults,
			}
			if len(test.models) > 0 {
				body["models"] = test.models
			}
			response := p3JSONRequest(t, app, http.MethodPost, "/api/assistants", body, "")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestP4AssistantDetailReturnsEffectiveAuthority(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	agent, err := app.ensureAgentProfile(store, "local", "OPERON")
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := app.webAssistantRecord("local", "synonbiomed:operon")
	if err != nil || !found {
		t.Fatalf("OPERON record found=%v err=%v", found, err)
	}
	allowedSkills := app.webAssistantAllowedSkillNames(record)
	allowedMCPs, err := app.webAssistantAllowedMCPIDs("local", record)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowedSkills) < 2 || len(allowedMCPs) < 1 {
		t.Fatalf("need authority fixture skills=%v mcps=%v", allowedSkills, allowedMCPs)
	}
	tombstonedSkill := allowedSkills[0]
	disabledSkill := allowedSkills[1]
	tombstonedMCP := allowedMCPs[0]
	if _, err := store.UpdateAgent("local", agent.Name, workspace.UpdateAgentInput{
		SkillTombstones: &[]string{tombstonedSkill},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAgentConnectorTombstone("local", agent.Name, tombstonedMCP, true); err != nil {
		t.Fatal(err)
	}
	config, err := app.getWebAssistantConfig("local", agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	config.DisabledBuiltinSkills = []string{disabledSkill}
	if err := app.setWebAssistantConfig("local", agent.Name, config); err != nil {
		t.Fatal(err)
	}

	detailResponse := p3JSONRequest(t, app, http.MethodGet, "/api/assistants/synonbiomed%3Aoperon", nil, "")
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	detailEnvelope := p3DecodeObject(t, detailResponse)
	detail, _ := detailEnvelope["data"].(map[string]any)
	capabilities, _ := detail["capabilities"].(map[string]any)
	effectiveSkills := webAssistantStringValues(capabilities["allowed_skill_ids"])
	effectiveMCPs := webAssistantStringValues(capabilities["allowed_mcp_ids"])
	if p4ContainsFold(effectiveSkills, tombstonedSkill) || p4ContainsFold(effectiveSkills, disabledSkill) {
		t.Fatalf("excluded skills leaked into authority: %v", effectiveSkills)
	}
	if p4ContainsFold(effectiveMCPs, tombstonedMCP) {
		t.Fatalf("tombstoned MCP leaked into authority: %v", effectiveMCPs)
	}

	project := createP3Project(t, store, "p4-effective-authority-project", "local")
	create := func(skills, mcps []string) *httptest.ResponseRecorder {
		return p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
			"name": "Effective authority", "assistant": map[string]any{
				"id": "synonbiomed:operon", "conversation_overrides": map[string]any{
					"skill_ids": skills, "mcp_ids": mcps,
				},
			},
			"extra": map[string]any{"project_id": project.ID},
		}, "")
	}
	if response := create(effectiveSkills, effectiveMCPs); response.Code != http.StatusCreated {
		t.Fatalf("effective authority status=%d body=%s", response.Code, response.Body.String())
	}
	if response := create(append(effectiveSkills, tombstonedSkill), effectiveMCPs); response.Code != http.StatusForbidden {
		t.Fatalf("tombstoned skill status=%d body=%s", response.Code, response.Body.String())
	}
	if response := create(effectiveSkills, append(effectiveMCPs, tombstonedMCP)); response.Code != http.StatusForbidden {
		t.Fatalf("tombstoned MCP status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestP4OperonConversationWithoutMCPOverrideKeepsConnectedAuthority(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "p4-operon-default-mcp-project", "local")
	record, found, err := app.webAssistantRecord("local", "synonbiomed:operon")
	if err != nil || !found {
		t.Fatalf("OPERON record found=%v err=%v", found, err)
	}
	want, err := app.webAssistantAllowedMCPIDs("local", record)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("OPERON fixture has no connected MCP authority")
	}
	draftCapabilityResponse := p3JSONRequest(
		t,
		app,
		http.MethodGet,
		"/api/assistants/synonbiomed%3Aoperon/composer-capabilities",
		nil,
		"",
	)
	if draftCapabilityResponse.Code != http.StatusOK {
		t.Fatalf("draft composer capabilities status=%d body=%s", draftCapabilityResponse.Code, draftCapabilityResponse.Body.String())
	}
	draftCapabilities := p3DecodeObject(t, draftCapabilityResponse)
	if len(webAssistantStringValues(draftCapabilities["skills"])) == 0 {
		t.Fatalf("draft composer capabilities omitted Skills: %#v", draftCapabilities)
	}
	draftStatuses, _ := draftCapabilities["mcp_statuses"].([]any)
	if len(draftStatuses) != len(want) {
		t.Fatalf("draft composer MCP statuses=%#v want ids=%v", draftStatuses, want)
	}
	for _, rawStatus := range draftStatuses {
		status, _ := rawStatus.(map[string]any)
		if !p4ContainsFold(want, webString(status["id"])) || webString(status["status"]) != "loaded" {
			t.Fatalf("draft composer MCP status outside runtime authority: %#v", status)
		}
	}
	created := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "OPERON default MCP authority",
		"assistant": map[string]any{
			"id": "synonbiomed:operon",
		},
		"extra": map[string]any{"project_id": project.ID},
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	conversationID := webString(p3DecodeObject(t, created)["id"])
	capabilityResponse := p3JSONRequest(
		t,
		app,
		http.MethodGet,
		"/api/conversations/"+conversationID+"/composer-capabilities",
		nil,
		"",
	)
	if capabilityResponse.Code != http.StatusOK {
		t.Fatalf("composer capabilities status=%d body=%s", capabilityResponse.Code, capabilityResponse.Body.String())
	}
	capabilities := p3DecodeObject(t, capabilityResponse)
	if len(webAssistantStringValues(capabilities["skills"])) == 0 {
		t.Fatalf("composer capabilities omitted Skills: %#v", capabilities)
	}
	statuses, _ := capabilities["mcp_statuses"].([]any)
	if len(statuses) != len(want) {
		t.Fatalf("composer MCP statuses=%#v want ids=%v", statuses, want)
	}
	for _, rawStatus := range statuses {
		status, _ := rawStatus.(map[string]any)
		if !p4ContainsFold(want, webString(status["id"])) || webString(status["status"]) != "loaded" {
			t.Fatalf("composer MCP status outside runtime authority: %#v", status)
		}
	}
	runtimeContext, found, err := app.workspaceMCPRuntimeContext(conversationID)
	if err != nil || !found {
		t.Fatalf("runtime context found=%v err=%v", found, err)
	}
	got := make([]string, 0, len(runtimeContext.Connectors))
	for _, connector := range runtimeContext.Connectors {
		if connector.Enabled {
			got = append(got, connector.ID)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("default OPERON MCP authority got=%v want=%v", got, want)
	}
	for _, id := range want {
		if !p4ContainsFold(got, id) {
			t.Fatalf("default OPERON MCP authority omitted %q: got=%v", id, got)
		}
	}

	selectedID := want[0]
	selectedTurn := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content":               "Use the selected connector for this turn",
		"inject_mcp_server_ids": []string{selectedID},
		"loading_id":            "turn-scoped-mcp-selection",
	}, "")
	if selectedTurn.Code != http.StatusAccepted {
		t.Fatalf("selected MCP turn status=%d body=%s", selectedTurn.Code, selectedTurn.Body.String())
	}
	selectedContext, found, err := app.workspaceMCPRuntimeContext(conversationID)
	if err != nil || !found {
		t.Fatalf("selected runtime context found=%v err=%v", found, err)
	}
	if len(selectedContext.Connectors) != 1 || selectedContext.Connectors[0].ID != selectedID {
		t.Fatalf("turn-scoped MCP fence connectors=%v want=%q", selectedContext.Connectors, selectedID)
	}
	session, found, err := app.sessionStore.Get(conversationID)
	if err != nil || !found {
		t.Fatalf("selected turn session found=%v err=%v", found, err)
	}
	options, _, err := app.applySessionRunnerAgentProfile(session, SessionRunnerChatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(options.SystemPrompt, "MCP connectors explicitly selected for this turn") ||
		!strings.Contains(options.SystemPrompt, selectedID) {
		t.Fatalf("turn-scoped MCP prompt=%q", options.SystemPrompt)
	}

	rejected := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content":               "Use an unavailable connector",
		"inject_mcp_server_ids": []string{"missing-mcp"},
		"loading_id":            "turn-scoped-mcp-rejected",
	}, "")
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("unavailable MCP status=%d body=%s", rejected.Code, rejected.Body.String())
	}
}

func TestP4OperonDefaultsKeepDynamicSkillDiscovery(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	if app.skillCatalog == nil || len(app.skillCatalog.Skills()) <= 64 {
		t.Fatalf("OPERON catalog fixture must exercise more than 64 skills")
	}
	profile, err := app.ensureAgentProfile(store, "local", "OPERON")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Unrestricted {
		t.Fatalf("materialized OPERON profile unrestricted=%v", profile.Unrestricted)
	}

	detailResponse := p3JSONRequest(t, app, http.MethodGet, "/api/assistants/synonbiomed%3Aoperon", nil, "")
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	detailEnvelope := p3DecodeObject(t, detailResponse)
	detail, _ := detailEnvelope["data"].(map[string]any)
	defaults, _ := detail["defaults"].(map[string]any)
	skillDefaults, _ := defaults["skills"].(map[string]any)
	if skillDefaults["mode"] != "auto" {
		t.Fatalf("OPERON skill defaults=%#v", skillDefaults)
	}

	project := createP3Project(t, store, "p4-operon-dynamic-skill-project", "local")
	created := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "OPERON dynamic skill discovery",
		"assistant": map[string]any{
			"id": "synonbiomed:operon",
		},
		"extra": map[string]any{"project_id": project.ID},
	}, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	conversationID := webString(p3DecodeObject(t, created)["id"])
	options, _, err := app.applySessionRunnerAgentProfile(
		sessionstore.Session{ID: conversationID, Orchestration: map[string]any{}},
		SessionRunnerChatOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(options.SelectedSkillNames) != 0 || !options.RestrictSkillDiscovery {
		t.Fatalf("OPERON runtime skill authority selected=%v allowed=%v restricted=%v", options.SelectedSkillNames, options.AllowedSkillNames, options.RestrictSkillDiscovery)
	}
	if len(options.AllowedSkillNames) != len(profile.SkillNames) {
		t.Fatalf("OPERON allowed manifest count=%d want=%d", len(options.AllowedSkillNames), len(profile.SkillNames))
	}
	if _, err := app.runtimeSkillsByName(options.SelectedSkillNames, options.ExcludedSkillNames); err != nil {
		t.Fatalf("OPERON dynamic skill authority failed before discovery: %v", err)
	}
}

func TestP4RestrictedAssistantRejectsUnattachedEnabledMCPDefault(t *testing.T) {
	app, _ := newP3WebConversationServer(t)
	operon, found, err := app.webAssistantRecord("local", "synonbiomed:operon")
	if err != nil || !found {
		t.Fatalf("OPERON record found=%v err=%v", found, err)
	}
	ownerEnabled, err := app.webAssistantAllowedMCPIDs("local", operon)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerEnabled) == 0 {
		t.Fatal("OPERON fixture has no connected MCP authority")
	}
	response := p3JSONRequest(t, app, http.MethodPost, "/api/assistants", map[string]any{
		"name":        "Restricted MCP default",
		"description": "Has no connector attachments.",
		"defaults": map[string]any{
			"mcps": map[string]any{
				"mode":  "fixed",
				"value": []string{ownerEnabled[0]},
			},
		},
	}, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestP4WebApprovalMemoryIsExactAndUserScoped(t *testing.T) {
	app, _ := newP3WebConversationServer(t)
	if err := app.rememberWebApproval("local", "desktop_shell", "PowerShell"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		user, action, command string
		want                  bool
	}{
		{"local", "desktop_shell", "PowerShell", true},
		{"foreign", "desktop_shell", "PowerShell", false},
		{"local", "desktop_shell", "Bash", false},
		{"local", "search_web", "", true},
		{"local", "unknown_action", "", false},
	} {
		approved, err := app.webConversationApproval(test.user, test.action, test.command)
		if err != nil {
			t.Fatalf("approval %q/%q/%q: %v", test.user, test.action, test.command, err)
		}
		if approved != test.want {
			t.Fatalf("approval %q/%q/%q=%v want=%v", test.user, test.action, test.command, approved, test.want)
		}
	}
	if _, err := app.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "deny"}); err != nil {
		t.Fatal(err)
	}
	approved, err := app.webConversationApproval("local", "desktop_shell", "PowerShell")
	if err != nil || approved {
		t.Fatalf("deny override approved=%v err=%v", approved, err)
	}
}

func TestP4DurableFrameEventsProjectWebMessageTurnAndConfirmationEvents(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "p4-event-project", "local")
	create := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Realtime projection", "extra": map[string]any{"project_id": project.ID},
	}, "")
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	conversationID := webString(p3DecodeObject(t, create)["id"])
	send := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Emit durable realtime evidence",
	}, "")
	if send.Code != http.StatusAccepted {
		t.Fatalf("send status=%d body=%s", send.Code, send.Body.String())
	}
	frameEvents, err := store.ListFrameEvents(conversationID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var userSource workspace.FrameEvent
	for _, event := range frameEvents {
		if event.Type == "user_message" {
			userSource = event
		}
	}
	if userSource.ID == "" {
		t.Fatalf("frame events=%#v", frameEvents)
	}
	userEvent, found, err := store.GetRealtimeEventByID("web-user-created:" + userSource.ID)
	if err != nil || !found || userEvent.Type != "message.userCreated" || userEvent.UserID != "local" {
		t.Fatalf("user projection=%#v found=%v err=%v", userEvent, found, err)
	}

	finishSource, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: "p4-runner-finished", FrameID: conversationID, Type: "runner_finished",
		Payload: map[string]any{"role": "system", "status": "completed", "text": "runner completed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.publishWorkspaceEvent(finishSource); err != nil {
		t.Fatal(err)
	}
	turnEvent, found, err := store.GetRealtimeEventByID("web-turn-completed:" + finishSource.ID)
	if err != nil || !found || turnEvent.Type != "turn.completed" || webString(turnEvent.Payload["status"]) != "finished" {
		t.Fatalf("turn projection=%#v found=%v err=%v", turnEvent, found, err)
	}

	metadata, found, err := store.GetFrameRuntimeMetadata(conversationID)
	if err != nil || !found {
		t.Fatalf("metadata found=%v err=%v", found, err)
	}
	metadata.ContextData["_pending_input_requests"] = []any{
		map[string]any{"tool_id": "approval-p4", "kind": "mcp_tool", "tool_name": "MCPTool"},
	}
	if _, err := store.SetFrameRuntimeMetadata(conversationID, metadata); err != nil {
		t.Fatal(err)
	}
	waiting := "awaiting_user_response"
	if _, err := store.UpdateFrame(conversationID, workspace.UpdateFrameInput{Status: &waiting}); err != nil {
		t.Fatal(err)
	}
	pendingSource, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: "p4-pending-input", FrameID: conversationID, Type: "frame_update", Payload: map[string]any{"status": waiting},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.publishWorkspaceEvent(pendingSource); err != nil {
		t.Fatal(err)
	}
	confirmationID := "web-confirmation-add:" + conversationID + ":" + webEventComponent("approval-p4")
	confirmation, found, err := store.GetRealtimeEventByID(confirmationID)
	if err != nil || !found || confirmation.Type != "confirmation.add" || webString(confirmation.Payload["id"]) != "approval-p4" {
		t.Fatalf("confirmation projection=%#v found=%v err=%v", confirmation, found, err)
	}
	resolved := p3JSONRequest(t, app, http.MethodPost, "/api/frames/"+conversationID+"/resolve-input", map[string]any{
		"responses": []map[string]any{{"tool_id": "approval-p4", "action": "deny"}},
	}, "")
	if resolved.Code != http.StatusOK {
		t.Fatalf("direct resolve status=%d body=%s", resolved.Code, resolved.Body.String())
	}
	removals, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: project.ID, Type: "confirmation.remove", Limit: 10,
	})
	if err != nil || len(removals) != 1 || webString(removals[0].Payload["id"]) != "approval-p4" {
		t.Fatalf("direct confirmation removals=%#v err=%v", removals, err)
	}
}

func p4UserSkillNames(t *testing.T, app *Server, count int) []string {
	t.Helper()
	result := make([]string, 0, count)
	for _, skill := range app.skillCatalog.Skills() {
		if compatibilityRequiredPlatformSkill(skill) {
			continue
		}
		result = append(result, skill.Name)
		if len(result) == count {
			return result
		}
	}
	t.Fatalf("need %d user-visible skills, got %d", count, len(result))
	return nil
}

func p4ContainsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}
