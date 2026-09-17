package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/synonlink"
)

func TestSynonLinkHTTPClientsAndCommandLifecycle(t *testing.T) {
	link := synonlink.NewService()
	if err := link.Register(synonlink.Client{
		ID:           "sl_api_client",
		UserID:       "local",
		Name:         "Victor Chrome",
		Kind:         "browser",
		Version:      "0.6.10",
		Capabilities: []string{"browserSearch", "humanPageRead"},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	srv := New(Options{SynonLink: link})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	clientsResp, err := http.Get(httpServer.URL + "/api/synon-link?userId=local")
	if err != nil {
		t.Fatalf("GET clients error = %v", err)
	}
	defer clientsResp.Body.Close()
	if clientsResp.StatusCode != http.StatusOK {
		t.Fatalf("clients status = %d", clientsResp.StatusCode)
	}
	var clientsBody struct {
		Clients []synonlink.Client `json:"clients"`
	}
	if err := json.NewDecoder(clientsResp.Body).Decode(&clientsBody); err != nil {
		t.Fatalf("decode clients: %v", err)
	}
	if len(clientsBody.Clients) != 1 || clientsBody.Clients[0].ID != "sl_api_client" {
		t.Fatalf("clients body = %#v", clientsBody)
	}

	commandBody := []byte(`{"userId":"local","clientId":"sl_api_client","name":"search_web","payload":{"query":"Synon"}}`)
	resultCh := make(chan map[string]any, 1)
	errorCh := make(chan error, 1)
	go func() {
		resp, err := http.Post(httpServer.URL+"/api/synon-link/commands", "application/json", bytes.NewReader(commandBody))
		if err != nil {
			errorCh <- err
			return
		}
		defer resp.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			errorCh <- err
			return
		}
		if resp.StatusCode != http.StatusOK {
			errorCh <- &statusError{status: resp.StatusCode, body: body}
			return
		}
		resultCh <- body
	}()

	var pending []synonlink.Command
	for attempt := 0; attempt < 50; attempt++ {
		pending = link.PendingCommands("sl_api_client")
		if len(pending) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("pending commands = %#v", pending)
	}
	if pending[0].Action != "search_web" || pending[0].Payload["query"] != "Synon" {
		t.Fatalf("pending command = %#v", pending[0])
	}
	if err := link.CompleteCommand("sl_api_client", pending[0].ID, map[string]any{"tabId": 123}); err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}

	select {
	case err := <-errorCh:
		t.Fatalf("command API error = %v", err)
	case body := <-resultCh:
		if body["ok"] != true {
			t.Fatalf("command body = %#v", body)
		}
		result, ok := body["result"].(map[string]any)
		if !ok || result["tabId"] != float64(123) {
			t.Fatalf("command result = %#v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("command API did not return after command completion")
	}
}

func TestSynonLinkToolExecutionRoutesThroughService(t *testing.T) {
	link := synonlink.NewService()
	delivery := make(chan synonlink.Command, 1)
	if err := link.Attach(synonlink.Client{
		ID:               "sl_tool_client",
		UserID:           "local",
		Name:             "Tool Chrome",
		Kind:             "browser",
		Capabilities:     []string{"browserSearch"},
		SupportedActions: []string{"search_web"},
	}, delivery); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	srv := New(Options{SynonLink: link})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	resultCh := make(chan map[string]any, 1)
	errorCh := make(chan error, 1)
	go func() {
		body := []byte(`{"input":{"userId":"local","clientId":"sl_tool_client","action":"search_web","payload":{"query":"Synon"}}}`)
		resp, err := http.Post(httpServer.URL+"/api/tools/synon_link/execute", "application/json", bytes.NewReader(body))
		if err != nil {
			errorCh <- err
			return
		}
		defer resp.Body.Close()
		var decoded map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			errorCh <- err
			return
		}
		if resp.StatusCode != http.StatusOK {
			errorCh <- &statusError{status: resp.StatusCode, body: decoded}
			return
		}
		resultCh <- decoded
	}()

	var command synonlink.Command
	select {
	case command = <-delivery:
	case err := <-errorCh:
		t.Fatalf("tool execution returned before delivering command: %v", err)
	case <-time.After(time.Second):
		t.Fatal("tool execution did not deliver command")
	}
	if command.Action != "search_web" || command.Payload["query"] != "Synon" {
		t.Fatalf("delivered command = %#v", command)
	}
	if err := link.CompleteCommand("sl_tool_client", command.ID, map[string]any{"resultCount": 3}); err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}

	select {
	case err := <-errorCh:
		t.Fatalf("tool execution error = %v", err)
	case body := <-resultCh:
		if body["ok"] != true || body["commandId"] != command.ID {
			t.Fatalf("tool execution body = %#v", body)
		}
		result, ok := body["result"].(map[string]any)
		if !ok || result["resultCount"] != float64(3) {
			t.Fatalf("tool execution result = %#v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("tool execution did not return")
	}
}

func TestSynonLinkCapabilitiesEndpoint(t *testing.T) {
	srv := New(Options{SynonLink: synonlink.NewService()})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/api/synon-link/capabilities")
	if err != nil {
		t.Fatalf("GET capabilities error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		ProtocolVersion string `json:"protocolVersion"`
		PolicyVersion   string `json:"policyVersion"`
		Actions         []struct {
			Action string `json:"action"`
		} `json:"actions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if body.ProtocolVersion == "" || body.PolicyVersion == "" {
		t.Fatalf("missing protocol metadata: %#v", body)
	}
	for _, action := range []string{"search_web", "open_tab", "scroll_page", "click_at", "type", "screenshot", "download_file", "cleanup_tabs", "clear_local_files"} {
		if !containsAction(body.Actions, action) {
			t.Fatalf("missing retained action %s: %#v", action, body.Actions)
		}
	}
	for _, action := range []string{"desktop_status", "desktop_click", "desktop_window_focus", "desktop_read_clipboard", "desktop_shell"} {
		if containsAction(body.Actions, action) {
			t.Fatalf("removed desktop action %s remains exposed: %#v", action, body.Actions)
		}
	}
}

func TestSynonLinkCommandPolicyUsesSavedApprovalDefaults(t *testing.T) {
	root := t.TempDir()
	link := synonlink.NewService()
	if err := link.Register(synonlink.Client{
		ID:               "sl_policy_defaults",
		UserID:           "local",
		Name:             "Policy Defaults Chrome",
		Kind:             "browser",
		Capabilities:     []string{"localFiles", "localFileOperations"},
		SupportedActions: []string{"local_file_write"},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	srv := New(Options{SynonLink: link, FileRoot: root})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	settingsBody := []byte(`{"input":{"key":"approval.defaults","value":{"mode":"deny","autoAllowReadOnly":false,"requireReason":true,"rememberDecisions":false}}}`)
	settingsResp, err := http.Post(httpServer.URL+"/api/tools/settings_set/execute", "application/json", bytes.NewReader(settingsBody))
	if err != nil {
		t.Fatalf("POST settings_set error = %v", err)
	}
	defer settingsResp.Body.Close()
	if settingsResp.StatusCode != http.StatusOK {
		var body map[string]any
		_ = json.NewDecoder(settingsResp.Body).Decode(&body)
		t.Fatalf("settings_set status = %d body = %#v", settingsResp.StatusCode, body)
	}

	commandBody := []byte(`{"userId":"local","clientId":"sl_policy_defaults","name":"local_file_write","payload":{"path":"notes.txt","content":"x"}}`)
	commandResp, err := http.Post(httpServer.URL+"/api/synon-link/commands", "application/json", bytes.NewReader(commandBody))
	if err != nil {
		t.Fatalf("POST command error = %v", err)
	}
	defer commandResp.Body.Close()
	var commandError map[string]any
	if err := json.NewDecoder(commandResp.Body).Decode(&commandError); err != nil {
		t.Fatalf("decode command error: %v", err)
	}
	if commandResp.StatusCode != http.StatusBadRequest || !strings.Contains(stringValue(commandError["message"]), "denied by approval defaults") {
		t.Fatalf("command response status=%d body=%#v", commandResp.StatusCode, commandError)
	}

	capResp, err := http.Get(httpServer.URL + "/api/synon-link/capabilities")
	if err != nil {
		t.Fatalf("GET capabilities error = %v", err)
	}
	defer capResp.Body.Close()
	var capabilities map[string]any
	if err := json.NewDecoder(capResp.Body).Decode(&capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	defaults, _ := capabilities["policyDefaults"].(map[string]any)
	if capabilities["policyVersion"] != "2" || defaults["mode"] != "deny" {
		t.Fatalf("capabilities policy defaults = %#v", capabilities)
	}
	actions, _ := capabilities["actions"].([]any)
	foundWrite := false
	for _, raw := range actions {
		action, _ := raw.(map[string]any)
		if action["action"] == "local_file_write" {
			foundWrite = true
			if action["effectiveApprovalPolicy"] != "deny" {
				t.Fatalf("local_file_write effective policy = %#v", action)
			}
		}
	}
	if !foundWrite {
		t.Fatal("capabilities missing local_file_write")
	}
}

func TestSynonLinkRememberedApprovalPersistsThroughSettings(t *testing.T) {
	root := t.TempDir()
	link := synonlink.NewService()
	delivery := make(chan synonlink.Command, 2)
	if err := link.Attach(synonlink.Client{
		ID:               "sl_remember",
		UserID:           "local",
		Name:             "Remember Chrome",
		Kind:             "browser",
		Capabilities:     []string{"localFiles", "localFileOperations"},
		SupportedActions: []string{"local_file_write"},
	}, delivery); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	srv := New(Options{SynonLink: link, FileRoot: root})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	settingsBody := []byte(`{"input":{"key":"approval.defaults","value":{"mode":"confirm","autoAllowReadOnly":false,"requireReason":true,"rememberDecisions":true}}}`)
	settingsResp, err := http.Post(httpServer.URL+"/api/tools/settings_set/execute", "application/json", bytes.NewReader(settingsBody))
	if err != nil {
		t.Fatalf("POST settings_set error = %v", err)
	}
	defer settingsResp.Body.Close()
	if settingsResp.StatusCode != http.StatusOK {
		t.Fatalf("settings_set status = %d", settingsResp.StatusCode)
	}

	completeNextSynonLinkCommand(t, link, delivery, "sl_remember")
	firstBody := []byte(`{"userId":"local","clientId":"sl_remember","name":"local_file_write","payload":{"path":"notes.txt","content":"alpha","approved":true,"approvalReason":"approved once from Web UI"}}`)
	firstResp, err := http.Post(httpServer.URL+"/api/synon-link/commands", "application/json", bytes.NewReader(firstBody))
	if err != nil {
		t.Fatalf("POST first remembered command error = %v", err)
	}
	defer firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusOK {
		var body map[string]any
		_ = json.NewDecoder(firstResp.Body).Decode(&body)
		t.Fatalf("first command status = %d body = %#v", firstResp.StatusCode, body)
	}

	getBody := []byte(`{"input":{"key":"approval.rememberedDecisions"}}`)
	getResp, err := http.Post(httpServer.URL+"/api/tools/settings_get/execute", "application/json", bytes.NewReader(getBody))
	if err != nil {
		t.Fatalf("POST settings_get remembered error = %v", err)
	}
	defer getResp.Body.Close()
	var rememberedBody map[string]any
	if err := json.NewDecoder(getResp.Body).Decode(&rememberedBody); err != nil {
		t.Fatalf("decode remembered settings: %v", err)
	}
	rememberedRaw, _ := json.Marshal(rememberedBody)
	if getResp.StatusCode != http.StatusOK || !strings.Contains(string(rememberedRaw), "approved once from Web UI") {
		t.Fatalf("remembered settings status=%d body=%#v", getResp.StatusCode, rememberedBody)
	}
	doctorResp, err := http.Get(httpServer.URL + "/api/synon-link/doctor?userId=local")
	if err != nil {
		t.Fatalf("GET doctor remembered error = %v", err)
	}
	defer doctorResp.Body.Close()
	var doctorBody map[string]any
	if err := json.NewDecoder(doctorResp.Body).Decode(&doctorBody); err != nil {
		t.Fatalf("decode doctor remembered: %v", err)
	}
	report, _ := doctorBody["report"].(map[string]any)
	rememberedApprovals, _ := report["rememberedApprovals"].(map[string]any)
	if rememberedApprovals["count"] != float64(1) || rememberedApprovals["key"] != "approval.rememberedDecisions" {
		t.Fatalf("doctor remembered approvals = %#v", rememberedApprovals)
	}

	completeNextSynonLinkCommand(t, link, delivery, "sl_remember")
	secondBody := []byte(`{"userId":"local","clientId":"sl_remember","name":"local_file_write","payload":{"path":"notes.txt","content":"beta"}}`)
	secondResp, err := http.Post(httpServer.URL+"/api/synon-link/commands", "application/json", bytes.NewReader(secondBody))
	if err != nil {
		t.Fatalf("POST second remembered command error = %v", err)
	}
	defer secondResp.Body.Close()
	if secondResp.StatusCode != http.StatusOK {
		var body map[string]any
		_ = json.NewDecoder(secondResp.Body).Decode(&body)
		t.Fatalf("second command status = %d body = %#v", secondResp.StatusCode, body)
	}
}

func completeNextSynonLinkCommand(t *testing.T, link *synonlink.Service, delivery <-chan synonlink.Command, clientID string) {
	t.Helper()
	go func() {
		command := <-delivery
		_ = link.CompleteCommand(clientID, command.ID, map[string]any{"ok": true})
	}()
}

func TestSynonLinkInspectAggregatesClientRuntimeState(t *testing.T) {
	link := synonlink.NewService()
	client := synonlink.Client{
		ID:               "sl_inspect",
		UserID:           "usr_inspect",
		Name:             "Inspect Chrome",
		Kind:             "browser",
		Version:          "0.6.10",
		Capabilities:     []string{"browserSearch", "humanPageRead", "localFiles", "localFileOperations"},
		SupportedActions: []string{"search_web", "read_page", "local_file_read", "local_file_write"},
		Status:           "online",
	}
	if err := link.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	writeCommand, writeResult, err := link.SendCommand(ctx, "usr_inspect", "sl_inspect", "local_file_write", map[string]any{
		"path":       "notes.txt",
		"content":    "x",
		"allowApply": true,
		"dryRun":     false,
	})
	if err != nil {
		t.Fatalf("SendCommand(write) error = %v", err)
	}
	if err := link.CompleteCommand("sl_inspect", writeCommand.ID, map[string]any{"applied": true}); err != nil {
		t.Fatalf("CompleteCommand(write) error = %v", err)
	}
	<-writeResult
	if _, _, err := link.SendCommand(ctx, "usr_inspect", "sl_inspect", "search_web", map[string]any{"query": "Synon"}); err != nil {
		t.Fatalf("SendCommand(search) error = %v", err)
	}
	if _, err := link.RequestAccess("usr_inspect", "sl_inspect", "browserSearch", "inspect test"); err != nil {
		t.Fatalf("RequestAccess() error = %v", err)
	}
	srv := New(Options{SynonLink: link})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/api/synon-link/inspect?userId=usr_inspect&clientId=sl_inspect")
	if err != nil {
		t.Fatalf("GET inspect error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		t.Fatalf("inspect status = %d body = %#v", resp.StatusCode, body)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode inspect: %v", err)
	}
	summary, _ := body["summary"].(map[string]any)
	if summary["running"] != float64(1) || summary["logs"] != float64(1) || summary["accessRequests"] != float64(1) {
		t.Fatalf("inspect summary = %#v", summary)
	}
	actions, _ := body["actions"].(map[string]any)
	supported, _ := actions["supported"].([]any)
	blocked, _ := actions["blocked"].([]any)
	if len(supported) == 0 || len(blocked) == 0 {
		t.Fatalf("inspect actions supported=%d blocked=%d body=%#v", len(supported), len(blocked), body)
	}
}

func TestSynonLinkAccessRequestLifecycle(t *testing.T) {
	link := synonlink.NewService()
	if err := link.Register(synonlink.Client{
		ID:               "browser_access",
		UserID:           "local",
		DeviceID:         "browser_device_access",
		Name:             "Victor Chrome",
		Kind:             "browser",
		Capabilities:     []string{"browserSearch"},
		SupportedActions: []string{"search_web"},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	srv := New(Options{SynonLink: link})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	createResp, err := http.Post(httpServer.URL+"/api/synon-link/access-requests", "application/json", bytes.NewReader([]byte(`{
		"userId":"local",
		"clientId":"browser_access",
		"scope":"browserSearch",
		"reason":"search the web"
	}`)))
	if err != nil {
		t.Fatalf("POST access request error = %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d", createResp.StatusCode)
	}
	var created struct {
		OK      bool                    `json:"ok"`
		Request synonlink.AccessRequest `json:"request"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created request: %v", err)
	}
	if !created.OK || created.Request.ID == "" || created.Request.Status != "pending" || created.Request.Scope != "browserSearch" {
		t.Fatalf("created request = %#v", created)
	}

	listResp, err := http.Get(httpServer.URL + "/api/synon-link/access-requests?userId=local&status=pending")
	if err != nil {
		t.Fatalf("GET access requests error = %v", err)
	}
	defer listResp.Body.Close()
	var listed struct {
		Requests []synonlink.AccessRequest `json:"requests"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode listed requests: %v", err)
	}
	if len(listed.Requests) != 1 || listed.Requests[0].ID != created.Request.ID {
		t.Fatalf("listed requests = %#v", listed)
	}

	decisionResp, err := http.Post(httpServer.URL+"/api/synon-link/access-requests/"+created.Request.ID+"/decision", "application/json", bytes.NewReader([]byte(`{
		"userId":"local",
		"approved":true,
		"reason":"approved by test"
	}`)))
	if err != nil {
		t.Fatalf("POST access decision error = %v", err)
	}
	defer decisionResp.Body.Close()
	var decided struct {
		Request synonlink.AccessRequest `json:"request"`
	}
	if err := json.NewDecoder(decisionResp.Body).Decode(&decided); err != nil {
		t.Fatalf("decode decided request: %v", err)
	}
	if decided.Request.Status != "granted" || decided.Request.DecisionReason != "approved by test" {
		t.Fatalf("decided request = %#v", decided)
	}

	deleteReq, err := http.NewRequest(http.MethodDelete, httpServer.URL+"/api/synon-link/access-requests/"+created.Request.ID+"?userId=local&reason=cleanup", nil)
	if err != nil {
		t.Fatalf("NewRequest(delete) error = %v", err)
	}
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	if err != nil {
		t.Fatalf("DELETE access request error = %v", err)
	}
	defer deleteResp.Body.Close()
	var revoked struct {
		Request synonlink.AccessRequest `json:"request"`
	}
	if err := json.NewDecoder(deleteResp.Body).Decode(&revoked); err != nil {
		t.Fatalf("decode revoked request: %v", err)
	}
	if revoked.Request.Status != "revoked" || revoked.Request.DecisionReason != "cleanup" {
		t.Fatalf("revoked request = %#v", revoked)
	}
}

func TestSynonLinkDownloadEndpointServesConfiguredPackage(t *testing.T) {
	root := t.TempDir()
	packagePath := filepath.Join(root, "synon-link-extension-v0.6.10.zip")
	if err := os.WriteFile(packagePath, []byte("ZIPDATA"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	srv := New(Options{
		SynonLink:       synonlink.NewService(),
		LinkPackagePath: packagePath,
	})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	headReq, err := http.NewRequest(http.MethodHead, httpServer.URL+"/api/plugins/synon/link/download", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	headResp, err := http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("HEAD download error = %v", err)
	}
	headResp.Body.Close()
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d", headResp.StatusCode)
	}
	if headResp.Header.Get("Content-Type") != "application/zip" || headResp.Header.Get("Content-Length") != "7" {
		t.Fatalf("HEAD headers = %#v", headResp.Header)
	}

	getResp, err := http.Get(httpServer.URL + "/api/plugins/synon/link/download")
	if err != nil {
		t.Fatalf("GET download error = %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", getResp.StatusCode)
	}
	data := new(bytes.Buffer)
	if _, err := data.ReadFrom(getResp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if data.String() != "ZIPDATA" {
		t.Fatalf("download body = %q", data.String())
	}
}

func TestSynonLinkLogsAndDeviceRevokeEndpoints(t *testing.T) {
	link := synonlink.NewService()
	delivery := make(chan synonlink.Command, 1)
	if err := link.Attach(synonlink.Client{
		ID:               "sld_revoke_api",
		UserID:           "local",
		DeviceID:         "device-api-1",
		Name:             "Victor Chrome",
		Kind:             "browser",
		Capabilities:     []string{"humanPageRead"},
		SupportedActions: []string{"read_page"},
	}, delivery); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	srv := New(Options{SynonLink: link})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	errCh := make(chan error, 1)
	commandCtx, commandCancel := context.WithTimeout(context.Background(), time.Second)
	defer commandCancel()
	go func() {
		body := []byte(`{"userId":"local","clientId":"sld_revoke_api","name":"read_page","payload":{"url":"https://example.test"}}`)
		req, err := http.NewRequestWithContext(commandCtx, http.MethodPost, httpServer.URL+"/api/synon-link/commands", bytes.NewReader(body))
		if err != nil {
			errCh <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			errCh <- &statusError{status: resp.StatusCode}
			return
		}
		var decoded map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			errCh <- err
			return
		}
		if decoded["message"] != "api revoked" {
			errCh <- &statusError{status: resp.StatusCode, body: decoded}
			return
		}
		errCh <- nil
	}()
	command := <-delivery

	req, err := http.NewRequest(http.MethodDelete, httpServer.URL+"/api/synon-link/devices/device-api-1?userId=local", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE device error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE device status = %d", resp.StatusCode)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("command API error = %v", err)
	}

	logsResp, err := http.Get(httpServer.URL + "/api/synon-link/logs?userId=local")
	if err != nil {
		t.Fatalf("GET logs error = %v", err)
	}
	defer logsResp.Body.Close()
	var logsBody struct {
		Logs []synonlink.TaskLog `json:"logs"`
	}
	if err := json.NewDecoder(logsResp.Body).Decode(&logsBody); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if len(logsBody.Logs) != 1 || logsBody.Logs[0].CommandID != command.ID || logsBody.Logs[0].Status != "revoked" {
		t.Fatalf("logs = %#v", logsBody)
	}
}

func TestSynonLinkDoctorReportIncludesBrowserOnlyPolicyMatrix(t *testing.T) {
	link := synonlink.NewService()
	if err := link.Register(synonlink.Client{
		ID:               "sl_doctor_browser",
		UserID:           "usr_doctor",
		Name:             "Doctor Chrome",
		Kind:             "browser",
		Capabilities:     []string{"browserSearch", "humanPageRead"},
		SupportedActions: []string{"search_web", "read_page"},
	}); err != nil {
		t.Fatalf("Register(browser) error = %v", err)
	}
	srv := New(Options{SynonLink: link})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/api/synon-link/doctor?userId=usr_doctor")
	if err != nil {
		t.Fatalf("GET doctor error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("doctor status = %d", resp.StatusCode)
	}
	var body struct {
		OK     bool `json:"ok"`
		Report struct {
			Clients      []synonlink.Client        `json:"clients"`
			Warnings     []string                  `json:"warnings"`
			PolicyMatrix map[string]map[string]any `json:"policyMatrix"`
			Server       map[string]any            `json:"server"`
		} `json:"report"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode doctor: %v", err)
	}
	if !body.OK || len(body.Report.Clients) != 1 {
		t.Fatalf("doctor body = %#v", body)
	}
	if body.Report.PolicyMatrix["search_web"]["autoEligible"] != true {
		t.Fatalf("policy matrix = %#v", body.Report.PolicyMatrix)
	}
	if _, exists := body.Report.PolicyMatrix["desktop_status"]; exists {
		t.Fatalf("desktop action remains in policy matrix: %#v", body.Report.PolicyMatrix)
	}
	if body.Report.Server["protocolVersion"] != "1" || body.Report.Server["policyVersion"] != "1" {
		t.Fatalf("server metadata = %#v", body.Report.Server)
	}
}

type statusError struct {
	status int
	body   map[string]any
}

func (e *statusError) Error() string {
	return "unexpected status"
}

func containsAction(actions []struct {
	Action string `json:"action"`
}, action string) bool {
	for _, candidate := range actions {
		if candidate.Action == action {
			return true
		}
	}
	return false
}

func containsWarning(warnings []string, expected string) bool {
	for _, warning := range warnings {
		if warning == expected {
			return true
		}
	}
	return false
}
