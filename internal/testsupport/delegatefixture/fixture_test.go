package delegatefixture_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/server"
	delegatefixture "synon-go/internal/testsupport/delegatefixture"
)

func TestSeedCreatesBrowserReadableDelegateChildFrameLifecycle(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture, err := delegatefixture.Seed(store)
	if err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(server.New(server.Options{Workspace: store}).Handler())
	defer app.Close()

	request, err := http.NewRequest(http.MethodGet, app.URL+fixture.TracePath+"?include_messages=true&focus_frame_id="+fixture.ChildFrameID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Synon-User-Id", fixture.UserID)
	response, err := app.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("trace status = %d", response.StatusCode)
	}
	var parent map[string]any
	if err := json.NewDecoder(response.Body).Decode(&parent); err != nil {
		t.Fatal(err)
	}
	if parent["id"] != fixture.ParentFrameID || parent["message_count"] != float64(9) || parent["status"] != "processing" {
		t.Fatalf("parent = %#v", parent)
	}
	parentContext := parent["context_data"].(map[string]any)
	toolMap := parentContext["_tool_id_to_frame_id"].(map[string]any)
	if toolMap[fixture.ToolUseID] != fixture.ChildFrameID {
		t.Fatalf("parent context = %#v", parentContext)
	}
	if toolMap[fixture.SecondToolUseID] != fixture.SecondChildFrameID ||
		toolMap[fixture.SecondNotificationToolUseID] != fixture.SecondChildFrameID {
		t.Fatalf("parallel child mappings = %#v", toolMap)
	}
	parentMessages := parentContext["_messages"].([]any)
	assertDelegateToolMessages(t, parentMessages, fixture.ToolUseID, fixture.NotificationToolUseID, fixture.ChildFrameID)
	assertQuestionNotification(t, parentMessages, fixture.SecondNotificationToolUseID, fixture.SecondChildFrameID)

	children := parent["children"].([]any)
	if len(children) != 2 {
		t.Fatalf("children = %#v", children)
	}
	child := frameByID(t, children, fixture.ChildFrameID)
	childContext := child["context_data"].(map[string]any)
	if child["id"] != fixture.ChildFrameID || child["parent_frame_id"] != fixture.ParentFrameID ||
		child["delegate_name"] != fixture.DelegateName || child["status"] != "completed" ||
		child["message_count"] != float64(4) || len(childContext["_messages"].([]any)) != 4 {
		t.Fatalf("child = %#v", child)
	}
	latest := childContext["_latest_tool_block"].(map[string]any)
	if latest["id"] != fixture.ToolUseID || latest["human_description"] == "" || latest["status"] != "completed" {
		t.Fatalf("latest tool block = %#v", latest)
	}
	childToolMap := childContext["_tool_id_to_frame_id"].(map[string]any)
	if childToolMap[fixture.GrandchildToolUseID] != fixture.GrandchildFrameID {
		t.Fatalf("grandchild mapping = %#v", childToolMap)
	}
	grandchildren := child["children"].([]any)
	if len(grandchildren) != 1 {
		t.Fatalf("grandchildren = %#v", grandchildren)
	}
	grandchild := frameByID(t, grandchildren, fixture.GrandchildFrameID)
	if grandchild["parent_frame_id"] != fixture.ChildFrameID ||
		grandchild["delegate_name"] != fixture.GrandchildDelegateName ||
		grandchild["status"] != "failed" || grandchild["message_count"] != float64(2) {
		t.Fatalf("grandchild = %#v", grandchild)
	}
	secondChild := frameByID(t, children, fixture.SecondChildFrameID)
	if secondChild["parent_frame_id"] != fixture.ParentFrameID ||
		secondChild["delegate_name"] != fixture.SecondDelegateName ||
		secondChild["status"] != "awaiting_user_response" || secondChild["message_count"] != float64(2) {
		t.Fatalf("second child = %#v", secondChild)
	}
	secondLatest := secondChild["context_data"].(map[string]any)["_latest_tool_block"].(map[string]any)
	if secondLatest["id"] != fixture.SecondToolUseID || secondLatest["status"] != "needs_input" {
		t.Fatalf("second child latest tool block = %#v", secondLatest)
	}

	frameRequest, err := http.NewRequest(http.MethodGet, app.URL+"/api/frames/"+fixture.ParentFrameID+"?shallow=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	frameRequest.Header.Set("X-Synon-User-Id", fixture.UserID)
	frameResponse, err := app.Client().Do(frameRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer frameResponse.Body.Close()
	var compatibleFrame map[string]any
	if err := json.NewDecoder(frameResponse.Body).Decode(&compatibleFrame); err != nil {
		t.Fatal(err)
	}
	if frameResponse.StatusCode != http.StatusOK || compatibleFrame["message_count"] != float64(9) {
		t.Fatalf("compatible frame = status %d body %#v", frameResponse.StatusCode, compatibleFrame)
	}

	messagesRequest, err := http.NewRequest(http.MethodGet, app.URL+"/api/frames/"+fixture.ParentFrameID+"/messages?from=0&limit=10", nil)
	if err != nil {
		t.Fatal(err)
	}
	messagesRequest.Header.Set("X-Synon-User-Id", fixture.UserID)
	messagesResponse, err := app.Client().Do(messagesRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer messagesResponse.Body.Close()
	var compatibleMessages map[string]any
	if err := json.NewDecoder(messagesResponse.Body).Decode(&compatibleMessages); err != nil {
		t.Fatal(err)
	}
	if messagesResponse.StatusCode != http.StatusOK || compatibleMessages["total"] != float64(9) || len(compatibleMessages["messages"].([]any)) != 9 {
		t.Fatalf("compatible messages = status %d body %#v", messagesResponse.StatusCode, compatibleMessages)
	}
}

func frameByID(t *testing.T, frames []any, id string) map[string]any {
	t.Helper()
	for _, raw := range frames {
		frame := raw.(map[string]any)
		if frame["id"] == id {
			return frame
		}
	}
	t.Fatalf("frame %q not found in %#v", id, frames)
	return nil
}

func assertQuestionNotification(t *testing.T, messages []any, toolUseID, childFrameID string) {
	t.Helper()
	for _, raw := range messages {
		message := raw.(map[string]any)
		content, _ := message["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if block["type"] != "tool_result" || block["tool_use_id"] != toolUseID {
				continue
			}
			assertSingleQuestionEnvelope(t, block["content"].(string), childFrameID)
			return
		}
	}
	t.Fatalf("question notification %q not found in %#v", toolUseID, messages)
}

func assertDelegateToolMessages(t *testing.T, messages []any, toolUseID, notificationToolUseID, childFrameID string) {
	t.Helper()
	var foundToolUse, foundToolResult, foundNotificationToolUse, foundNotifications bool
	for _, raw := range messages {
		message := raw.(map[string]any)
		content, _ := message["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if block["type"] == "tool_use" && block["id"] == toolUseID {
				input, _ := block["input"].(map[string]any)
				foundToolUse = input["human_description"] != "" && input["child_frame_id"] == childFrameID
			}
			if block["type"] == "tool_result" && block["tool_use_id"] == toolUseID {
				foundToolResult = true
			}
			if block["type"] == "tool_use" && block["id"] == notificationToolUseID {
				foundNotificationToolUse = true
			}
			if block["type"] == "tool_result" && block["tool_use_id"] == notificationToolUseID {
				content, ok := block["content"].(string)
				if !ok {
					t.Fatalf("notification tool_result content = %#v, want JSON string", block["content"])
				}
				assertNotificationEnvelope(t, content, childFrameID)
				foundNotifications = true
			}
		}
	}
	if !foundToolUse || !foundToolResult || !foundNotificationToolUse || !foundNotifications {
		t.Fatalf("delegate blocks tool_use=%v tool_result=%v notification_tool_use=%v notifications=%v messages=%#v", foundToolUse, foundToolResult, foundNotificationToolUse, foundNotifications, messages)
	}
}

func assertNotificationEnvelope(t *testing.T, content, childFrameID string) {
	t.Helper()
	var envelope struct {
		Notifications []struct {
			NotificationType string         `json:"notification_type"`
			SenderFrameID    string         `json:"sender_frame_id"`
			Payload          map[string]any `json:"payload"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatalf("decode notification content: %v: %s", err, content)
	}
	if len(envelope.Notifications) != 3 {
		t.Fatalf("notifications = %#v", envelope.Notifications)
	}
	completion := envelope.Notifications[0]
	if completion.NotificationType != "completion" || completion.SenderFrameID != childFrameID ||
		completion.Payload["status"] != "completed" || completion.Payload["wall_s"] != float64(4.25) {
		t.Fatalf("completion notification = %#v", completion)
	}
	bullets, ok := completion.Payload["_completion_bullets"].([]any)
	if !ok || len(bullets) != 2 || bullets[0] != "Reviewed deterministic fixture evidence." || bullets[1] != "Returned two source-backed findings." {
		t.Fatalf("completion bullets = %#v", completion.Payload["_completion_bullets"])
	}
	for index, wantKind := range []string{"question", "info"} {
		notification := envelope.Notifications[index+1]
		if notification.NotificationType != "child_message" || notification.SenderFrameID != childFrameID ||
			notification.Payload["sender_frame_id"] != childFrameID || notification.Payload["name"] != "literature-review" ||
			notification.Payload["agent_name"] != "LITERATURE_REVIEW" || notification.Payload["kind"] != wantKind {
			t.Fatalf("child notification %d = %#v", index, notification)
		}
		if text, _ := notification.Payload["text"].(string); text == "" {
			t.Fatalf("child notification %d has no text: %#v", index, notification)
		}
	}
}

func assertSingleQuestionEnvelope(t *testing.T, content, childFrameID string) {
	t.Helper()
	var envelope struct {
		Notifications []struct {
			NotificationType string         `json:"notification_type"`
			SenderFrameID    string         `json:"sender_frame_id"`
			Payload          map[string]any `json:"payload"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatalf("decode question notification: %v", err)
	}
	if len(envelope.Notifications) != 1 {
		t.Fatalf("question notifications = %#v", envelope.Notifications)
	}
	notification := envelope.Notifications[0]
	if notification.NotificationType != "child_message" ||
		notification.SenderFrameID != childFrameID ||
		notification.Payload["sender_frame_id"] != childFrameID ||
		notification.Payload["kind"] != "question" ||
		notification.Payload["name"] != "evidence-check" ||
		notification.Payload["agent_name"] != "EVIDENCE_CHECK" {
		t.Fatalf("question notification = %#v", notification)
	}
	if text, _ := notification.Payload["text"].(string); text == "" {
		t.Fatalf("question notification has no text: %#v", notification)
	}
}
