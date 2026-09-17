package synonlink

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceRegistersClientAndCompletesCommand(t *testing.T) {
	service := NewService()
	client := Client{
		ID:           "client-1",
		UserID:       "usr_test",
		Name:         "Victor Chrome",
		Kind:         "browser",
		Version:      "0.6.10",
		Capabilities: []string{"tabs", "humanPageRead"},
	}
	if err := service.Register(client); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	clients := service.ListClients("usr_test")
	if len(clients) != 1 || clients[0].ID != "client-1" {
		t.Fatalf("ListClients() = %#v", clients)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command, resultCh, err := service.SendCommand(ctx, "usr_test", "client-1", "read_page", map[string]any{"maxScrolls": 2})
	if err != nil {
		t.Fatalf("SendCommand() error = %v", err)
	}
	pending := service.PendingCommands("client-1")
	if len(pending) != 1 || pending[0].ID != command.ID || pending[0].Action != "read_page" {
		t.Fatalf("PendingCommands() = %#v", pending)
	}

	if err := service.CompleteCommand("client-1", command.ID, map[string]any{"title": "Read page"}); err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}
	select {
	case result := <-resultCh:
		if result.Value["title"] != "Read page" {
			t.Fatalf("result = %#v", result)
		}
	case <-ctx.Done():
		t.Fatal("command result timed out")
	}
	if pending := service.PendingCommands("client-1"); len(pending) != 0 {
		t.Fatalf("pending after completion = %#v", pending)
	}
}

func TestServiceDeliversAccessLifecycleEvents(t *testing.T) {
	service := NewService()
	events := make(chan AccessEvent, 4)
	if err := service.Attach(Client{
		ID:     "client-access",
		UserID: "usr_access_events",
		Name:   "Synon Link",
		Kind:   "browser",
	}, make(chan Command, 1)); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}
	if err := service.AttachAccessEvents("usr_access_events", "client-access", events); err != nil {
		t.Fatalf("AttachAccessEvents() error = %v", err)
	}

	request, err := service.RequestAccess("usr_access_events", "client-access", "local_files", "show permission UI")
	if err != nil {
		t.Fatalf("RequestAccess() error = %v", err)
	}
	created := mustReceiveAccessEvent(t, events)
	if created.Type != "access_event" || created.Event != "access_request_created" || created.Request.ID != request.ID || created.Request.Status != "pending" {
		t.Fatalf("created access event = %#v", created)
	}

	decided, err := service.DecideAccess("usr_access_events", request.ID, true, "approved in extension")
	if err != nil {
		t.Fatalf("DecideAccess() error = %v", err)
	}
	decision := mustReceiveAccessEvent(t, events)
	if decision.Event != "access_request_decided" || decision.Request.ID != decided.ID || decision.Request.Status != "granted" || decision.Request.DecisionReason != "approved in extension" {
		t.Fatalf("decision access event = %#v", decision)
	}

	revoked, err := service.RevokeAccess("usr_access_events", request.ID, "revoked in UI")
	if err != nil {
		t.Fatalf("RevokeAccess() error = %v", err)
	}
	revoke := mustReceiveAccessEvent(t, events)
	if revoke.Event != "access_request_revoked" || revoke.Request.ID != revoked.ID || revoke.Request.Status != "revoked" || revoke.Request.DecisionReason != "revoked in UI" {
		t.Fatalf("revoke access event = %#v", revoke)
	}
}

func TestSendCommandRejectsPolicyViolations(t *testing.T) {
	service := NewService()
	if err := service.Register(Client{
		ID:               "browser-policy",
		UserID:           "usr_policy",
		Name:             "Policy Chrome",
		Kind:             "browser",
		Capabilities:     []string{"browserSearch", "humanPageRead", "localFiles", "localFileOperations"},
		SupportedActions: []string{"search_web", "read_page", "local_file_write"},
	}); err != nil {
		t.Fatalf("Register(browser) error = %v", err)
	}
	if err := service.Register(Client{
		ID:               "browser-limited",
		UserID:           "usr_policy",
		Name:             "Limited Chrome",
		Kind:             "browser",
		Capabilities:     []string{"humanPageRead"},
		SupportedActions: []string{"read_page"},
	}); err != nil {
		t.Fatalf("Register(limited browser) error = %v", err)
	}
	if err := service.Register(Client{
		ID:               "browser-no-cap",
		UserID:           "usr_policy",
		Name:             "No Capability Chrome",
		Kind:             "browser",
		SupportedActions: []string{"search_web"},
	}); err != nil {
		t.Fatalf("Register(no-cap browser) error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cases := []struct {
		name     string
		clientID string
		action   string
		payload  map[string]any
		wantErr  string
	}{
		{name: "unknown action", clientID: "browser-policy", action: "markush_analyze", wantErr: "unsupported action"},
		{name: "removed desktop action", clientID: "browser-policy", action: "desktop_status", wantErr: "unsupported action"},
		{name: "not in supported actions", clientID: "browser-limited", action: "search_web", wantErr: "does not support action"},
		{name: "missing required capability", clientID: "browser-no-cap", action: "search_web", wantErr: "missing capability browserSearch"},
		{name: "approval required", clientID: "browser-policy", action: "local_file_write", payload: map[string]any{"path": "notes.txt", "content": "x"}, wantErr: "requires approval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := service.SendCommand(ctx, "usr_policy", tc.clientID, tc.action, tc.payload)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("SendCommand() error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}

	command, resultCh, err := service.SendCommand(ctx, "usr_policy", "browser-policy", "local_file_write", map[string]any{
		"path":       "notes.txt",
		"content":    "x",
		"allowApply": true,
		"dryRun":     false,
	})
	if err != nil {
		t.Fatalf("approved local_file_write SendCommand() error = %v", err)
	}
	if pending := service.PendingCommands("browser-policy"); len(pending) != 1 || pending[0].ID != command.ID {
		t.Fatalf("PendingCommands() after approved write = %#v", pending)
	}
	if err := service.CompleteCommand("browser-policy", command.ID, map[string]any{"applied": true}); err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}
	<-resultCh
}

func TestSendCommandAppliesApprovalDefaultsProvider(t *testing.T) {
	service := NewService()
	if err := service.Register(Client{
		ID:               "browser-defaults",
		UserID:           "usr_defaults",
		Name:             "Defaults Chrome",
		Kind:             "browser",
		Capabilities:     []string{"localFiles", "localFileOperations"},
		SupportedActions: []string{"local_file_read", "local_file_write"},
	}); err != nil {
		t.Fatalf("Register(browser) error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	service.SetPolicyDefaultsProvider(func() PolicyDefaults {
		return PolicyDefaults{Mode: "allow"}
	})
	command, resultCh, err := service.SendCommand(ctx, "usr_defaults", "browser-defaults", "local_file_write", map[string]any{"path": "notes.txt", "content": "x"})
	if err != nil {
		t.Fatalf("SendCommand() with allow defaults error = %v", err)
	}
	if err := service.CompleteCommand("browser-defaults", command.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}
	<-resultCh

	service.SetPolicyDefaultsProvider(func() PolicyDefaults {
		return PolicyDefaults{Mode: "deny"}
	})
	_, _, err = service.SendCommand(ctx, "usr_defaults", "browser-defaults", "local_file_write", map[string]any{"path": "notes.txt", "content": "x"})
	if err == nil || !strings.Contains(err.Error(), "denied by approval defaults") {
		t.Fatalf("SendCommand() deny defaults error = %v", err)
	}

	service.SetPolicyDefaultsProvider(func() PolicyDefaults {
		return PolicyDefaults{AutoAllowReadOnly: true}
	})
	command, resultCh, err = service.SendCommand(ctx, "usr_defaults", "browser-defaults", "local_file_read", map[string]any{"path": "notes.txt"})
	if err != nil {
		t.Fatalf("SendCommand() with autoAllowReadOnly error = %v", err)
	}
	if err := service.CompleteCommand("browser-defaults", command.ID, map[string]any{"content": "x"}); err != nil {
		t.Fatalf("CompleteCommand() read error = %v", err)
	}
	<-resultCh

	service.SetPolicyDefaultsProvider(func() PolicyDefaults {
		return PolicyDefaults{Mode: "confirm", RequireReason: true}
	})
	_, _, err = service.SendCommand(ctx, "usr_defaults", "browser-defaults", "local_file_write", map[string]any{"path": "notes.txt", "content": "x", "approved": true})
	if err == nil || !strings.Contains(err.Error(), "approval requires reason") {
		t.Fatalf("SendCommand() require reason error = %v", err)
	}
}

func TestSendCommandRemembersApprovedDecisionForSameClientAction(t *testing.T) {
	service := NewService()
	remembered := map[string]RememberedApprovalDecision{}
	service.SetRememberedApprovalStore(
		func(userID, clientID, action string) (RememberedApprovalDecision, bool) {
			decision, ok := remembered[RememberedApprovalKey(userID, clientID, action)]
			return decision, ok
		},
		func(decision RememberedApprovalDecision) error {
			remembered[RememberedApprovalKey(decision.UserID, decision.ClientID, decision.Action)] = decision
			return nil
		},
	)
	service.SetPolicyDefaultsProvider(func() PolicyDefaults {
		return PolicyDefaults{Mode: "confirm", RequireReason: true, RememberDecisions: true}
	})
	for _, clientID := range []string{"browser-remember", "browser-other"} {
		if err := service.Register(Client{
			ID:               clientID,
			UserID:           "usr_remember",
			Name:             clientID,
			Kind:             "browser",
			Capabilities:     []string{"localFiles", "localFileOperations"},
			SupportedActions: []string{"local_file_write"},
		}); err != nil {
			t.Fatalf("Register(%s) error = %v", clientID, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	first, firstResult, err := service.SendCommand(ctx, "usr_remember", "browser-remember", "local_file_write", map[string]any{
		"path":           "notes.txt",
		"content":        "alpha",
		"approved":       true,
		"approvalReason": "user approved file write for this project",
	})
	if err != nil {
		t.Fatalf("first SendCommand() error = %v", err)
	}
	if err := service.CompleteCommand("browser-remember", first.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("CompleteCommand(first) error = %v", err)
	}
	<-firstResult
	decision, ok := remembered[RememberedApprovalKey("usr_remember", "browser-remember", "local_file_write")]
	if !ok || decision.Reason != "user approved file write for this project" || decision.Action != "local_file_write" {
		t.Fatalf("remembered decision = %#v ok=%v", decision, ok)
	}

	second, secondResult, err := service.SendCommand(ctx, "usr_remember", "browser-remember", "local_file_write", map[string]any{
		"path":    "notes.txt",
		"content": "beta",
	})
	if err != nil {
		t.Fatalf("second SendCommand() should use remembered approval, error = %v", err)
	}
	if err := service.CompleteCommand("browser-remember", second.ID, map[string]any{"ok": true}); err != nil {
		t.Fatalf("CompleteCommand(second) error = %v", err)
	}
	<-secondResult

	_, _, err = service.SendCommand(ctx, "usr_remember", "browser-other", "local_file_write", map[string]any{
		"path":    "notes.txt",
		"content": "gamma",
	})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("other client should not reuse remembered approval, error = %v", err)
	}
}

func TestBrowserProtocolActionsMatchExtensionAndRoute(t *testing.T) {
	browserActions := []string{
		"run_task_loop",
		"ensure_workspace",
		"open_local_files",
		"local_files_status",
		"local_files_manifest",
		"local_files_list",
		"local_files_search",
		"local_file_info",
		"local_file_read",
		"local_file_write",
		"local_file_copy",
		"local_file_move",
		"local_file_delete",
		"local_file_mkdir",
		"local_files_organize",
		"bookmarks_status",
		"bookmarks_profile",
		"revoke_bookmarks_permission",
		"open_personalization",
		"search_web",
		"extract_search_results",
		"read_logged_in_page",
		"open_tab",
		"get_active_tab",
		"extract_page",
		"read_page",
		"refresh_tab",
		"scroll_page",
		"wait_for_page",
		"click",
		"click_at",
		"type",
		"screenshot",
		"visual_element_map",
		"find_download_links",
		"download_from_page",
		"download_file",
		"open_downloads_folder",
		"show_downloaded_file",
		"cleanup_tabs",
		"clear_local_files",
		"close_tab",
	}
	policies := PolicyMatrix()
	for _, action := range browserActions {
		profile, ok := policies[action]
		if !ok {
			t.Fatalf("browser action %q missing policy profile", action)
		}
		if profile.ClientKind != "browser" {
			t.Fatalf("browser action %q has client kind %q", action, profile.ClientKind)
		}
	}

	service := NewService()
	delivery := make(chan Command, len(browserActions))
	if err := service.Attach(Client{
		ID:     "browser-full",
		UserID: "usr_browser_full",
		Name:   "Synon Link Extension",
		Kind:   "browser",
		Capabilities: []string{
			"tabs", "tabGroups", "scripting", "activeTab", "captureVisibleTab", "synonLink", "taskLoop",
			"browserSearch", "humanPageRead", "llmMediaCapture", "mediaManifest", "scrollPage", "refreshTab",
			"downloads", "downloadOpen", "downloadDiscovery", "visualClick", "visualElementMap", "pageWait",
			"tabCleanup", "localFiles", "localFileOperations", "localFileAdvanced", "fileSystemAccess",
			"permissionReset", "bookmarks", "personalization", "loggedInPageRead",
		},
		SupportedActions: browserActions,
	}, delivery); err != nil {
		t.Fatalf("Attach(browser-full) error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cases := []struct {
		action  string
		payload map[string]any
	}{
		{action: "open_tab", payload: map[string]any{"url": "https://example.com", "approved": true}},
		{action: "scroll_page", payload: map[string]any{"direction": "down"}},
		{action: "click_at", payload: map[string]any{"x": 10, "y": 20, "approved": true}},
		{action: "type", payload: map[string]any{"value": "Synon", "approved": true}},
		{action: "screenshot", payload: map[string]any{"format": "png"}},
		{action: "download_file", payload: map[string]any{"url": "https://example.com/report.pdf", "approved": true}},
		{action: "clear_local_files", payload: map[string]any{"approved": true}},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			command, resultCh, err := service.SendCommand(ctx, "usr_browser_full", "browser-full", tc.action, tc.payload)
			if err != nil {
				t.Fatalf("SendCommand(%s) error = %v", tc.action, err)
			}
			delivered := <-delivery
			if delivered.ID != command.ID || delivered.Action != tc.action {
				t.Fatalf("delivered command = %#v, want %s/%s", delivered, command.ID, tc.action)
			}
			if err := service.CompleteCommand("browser-full", command.ID, map[string]any{"action": tc.action}); err != nil {
				t.Fatalf("CompleteCommand(%s) error = %v", tc.action, err)
			}
			<-resultCh
		})
	}

	_, _, err := service.SendCommand(ctx, "usr_browser_full", "browser-full", "click_at", map[string]any{"x": 1, "y": 2})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("click_at without approval error = %v", err)
	}
}

func TestDesktopProtocolIsRemovedWhileBrowserProtocolRemainsAvailable(t *testing.T) {
	desktopActions := []string{
		"desktop_task_loop",
		"desktop_status",
		"desktop_request_access",
		"desktop_screenshot",
		"desktop_click",
		"desktop_mouse_move",
		"desktop_mouse_down",
		"desktop_mouse_up",
		"desktop_drag",
		"desktop_cursor_position",
		"desktop_type",
		"desktop_key",
		"desktop_scroll",
		"desktop_displays",
		"desktop_frontmost_app",
		"desktop_app_under_point",
		"desktop_list_apps",
		"desktop_list_running_apps",
		"desktop_window_list",
		"desktop_window_focus",
		"desktop_window_minimize",
		"desktop_window_maximize",
		"desktop_window_restore",
		"desktop_window_move_resize",
		"desktop_window_close",
		"desktop_window_elements",
		"desktop_open_app",
		"desktop_file_list",
		"desktop_file_info",
		"desktop_file_read",
		"desktop_file_write",
		"desktop_file_copy",
		"desktop_file_move",
		"desktop_file_mkdir",
		"desktop_file_delete",
		"desktop_file_open",
		"desktop_read_clipboard",
		"desktop_write_clipboard",
		"desktop_shell",
		"desktop_batch",
	}
	policies := PolicyMatrix()
	for _, action := range desktopActions {
		if _, ok := policies[action]; ok {
			t.Fatalf("removed desktop action %q remains in policy matrix", action)
		}
	}
	service := NewService()
	if err := service.Register(Client{
		ID:       "desktop-full",
		UserID:   "usr_desktop_full",
		Name:     "Synon Link Desktop",
		Kind:     "desktop",
		DeviceID: "device-full",
		Capabilities: []string{
			"desktopAgent", "desktopStatus", "desktopVision", "desktopMouse", "desktopMouseDrag",
			"desktopKeyboard", "desktopClipboard", "desktopApps", "desktopWindows", "desktopWindowElements",
			"desktopFiles", "desktopFileManagement", "desktopFileDelete", "desktopShell", "desktopBatch",
			"desktopTaskLoop", "desktopSafetyGates",
		},
		SupportedActions: desktopActions,
	}); err == nil || !strings.Contains(err.Error(), "desktop") || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("Register(desktop-full) error = %v, want unsupported desktop client", err)
	}
	if err := service.Register(Client{
		ID:               "browser-kept",
		UserID:           "usr_desktop_full",
		Name:             "Synon Link Browser",
		Kind:             "browser",
		Capabilities:     []string{"browserSearch", "desktopStatus"},
		SupportedActions: []string{"search_web", "desktop_status"},
	}); err != nil {
		t.Fatalf("Register(browser-kept) error = %v", err)
	}
	clients := service.ListClients("usr_desktop_full")
	if len(clients) != 1 || clients[0].ID != "browser-kept" ||
		!containsString(clients[0].Capabilities, "browserSearch") || containsString(clients[0].Capabilities, "desktopStatus") ||
		!containsString(clients[0].SupportedActions, "search_web") || containsString(clients[0].SupportedActions, "desktop_status") {
		t.Fatalf("filtered browser client = %#v", clients)
	}
}

func TestRevokeDeviceMarksOfflineCompletesPendingAndRecordsLog(t *testing.T) {
	service := NewService()
	delivery := make(chan Command, 1)
	if err := service.Attach(Client{
		ID:               "browser-3",
		UserID:           "usr_desktop",
		Name:             "Victor Windows PC",
		Kind:             "browser",
		DeviceID:         "device-3",
		Capabilities:     []string{"humanPageRead"},
		SupportedActions: []string{"read_page"},
	}, delivery); err != nil {
		t.Fatalf("Attach() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command, resultCh, err := service.SendCommand(ctx, "usr_desktop", "browser-3", "read_page", nil)
	if err != nil {
		t.Fatalf("SendCommand() error = %v", err)
	}
	<-delivery

	if !service.RevokeDevice("usr_desktop", "device-3", "test revoked") {
		t.Fatal("RevokeDevice() should return true")
	}
	clients := service.ListClients("usr_desktop")
	if len(clients) != 1 || clients[0].Status != "offline" {
		t.Fatalf("clients after revoke = %#v", clients)
	}

	select {
	case result := <-resultCh:
		if result.OK || result.Error != "test revoked" || result.CommandID != command.ID {
			t.Fatalf("revoked command result = %#v", result)
		}
	case <-ctx.Done():
		t.Fatal("revoked command did not complete")
	}

	logs := service.ListTaskLogs("usr_desktop", 10)
	if len(logs) != 1 || logs[0].CommandID != command.ID || logs[0].Status != "revoked" || logs[0].Error != "test revoked" {
		t.Fatalf("logs = %#v", logs)
	}
}

func TestTaskLogsPersistToJSONLAndReload(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "synon-link-task-logs.jsonl")
	service := NewService()
	service.SetTaskLogPath(logPath)
	if err := service.Register(Client{
		ID:               "client-log",
		UserID:           "usr_logs",
		Name:             "Victor Chrome",
		Kind:             "browser",
		Capabilities:     []string{"humanPageRead"},
		SupportedActions: []string{"read_page"},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command, resultCh, err := service.SendCommand(ctx, "usr_logs", "client-log", "read_page", map[string]any{"maxScrolls": 2})
	if err != nil {
		t.Fatalf("SendCommand() error = %v", err)
	}
	if err := service.CompleteCommand("client-log", command.ID, map[string]any{"title": "Read page"}); err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}
	<-resultCh

	fresh := NewService()
	fresh.SetTaskLogPath(logPath)
	logs := fresh.ListTaskLogs("usr_logs", 10)
	if len(logs) != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	if logs[0].CommandID != command.ID || logs[0].Status != "completed" || !logs[0].OK || logs[0].Result["title"] != "Read page" {
		t.Fatalf("log entry = %#v", logs[0])
	}
}

func TestTaskLogsRedactScreenshotBase64WithoutChangingResult(t *testing.T) {
	service := NewService()
	if err := service.Register(Client{
		ID:               "browser-screenshot-log",
		UserID:           "usr_screenshot",
		Name:             "Browser Extension",
		Kind:             "browser",
		Capabilities:     []string{"captureVisibleTab"},
		SupportedActions: []string{"screenshot"},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command, resultCh, err := service.SendCommand(ctx, "usr_screenshot", "browser-screenshot-log", "screenshot", nil)
	if err != nil {
		t.Fatalf("SendCommand() error = %v", err)
	}
	imageBase64 := strings.Repeat("A", 1024)
	if err := service.CompleteCommand("browser-screenshot-log", command.ID, map[string]any{
		"action":      "screenshot",
		"mimeType":    "image/png",
		"imageBase64": imageBase64,
		"size":        int64(768),
	}); err != nil {
		t.Fatalf("CompleteCommand() error = %v", err)
	}

	result := <-resultCh
	if result.Value["imageBase64"] != imageBase64 {
		t.Fatalf("command result imageBase64 was changed")
	}
	logs := service.ListTaskLogs("usr_screenshot", 10)
	if len(logs) != 1 {
		t.Fatalf("logs = %#v", logs)
	}
	loggedImage, _ := logs[0].Result["imageBase64"].(string)
	if loggedImage == "" || loggedImage == imageBase64 || !strings.Contains(loggedImage, "redacted base64 image") {
		t.Fatalf("screenshot imageBase64 was not redacted in task log: %q", loggedImage)
	}
	if logs[0].Result["size"] != int64(768) || logs[0].Result["mimeType"] != "image/png" {
		t.Fatalf("screenshot task log metadata was not preserved: %#v", logs[0].Result)
	}
}

func mustReceiveAccessEvent(t *testing.T, ch <-chan AccessEvent) AccessEvent {
	t.Helper()
	select {
	case event := <-ch:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for access event")
	}
	return AccessEvent{}
}
