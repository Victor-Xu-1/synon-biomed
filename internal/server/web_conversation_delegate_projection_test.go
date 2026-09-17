package server

import (
	"context"
	"net/http"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	delegatefixture "synon-go/internal/testsupport/delegatefixture"
)

func TestWebConversationProjectsDelegateToolsAndNotifications(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	fixture, err := delegatefixture.Seed(store)
	if err != nil {
		t.Fatal(err)
	}
	response := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+fixture.ParentFrameID+"/messages?limit=50", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("delegate messages status=%d body=%s", response.Code, response.Body.String())
	}
	page := p3DecodeObject(t, response)
	items, _ := page["items"].([]any)
	if len(items) != 5 {
		t.Fatalf("delegate messages = %#v", page)
	}
	delegation := findProjectedMessage(items, fixture.ToolUseID)
	content, _ := delegation["content"].(map[string]any)
	subagent, _ := content["subagent"].(map[string]any)
	if webString(delegation["type"]) != "tool_call" || webString(content["name"]) != "Agent" ||
		webString(content["status"]) != "completed" || webString(subagent["frameId"]) != fixture.ChildFrameID ||
		webString(subagent["delegateName"]) != fixture.DelegateName || subagent["ordinal"] != float64(1) && subagent["ordinal"] != 1 ||
		subagent["messageCount"] != float64(4) && subagent["messageCount"] != 4 {
		t.Fatalf("delegate projection = %#v", delegation)
	}
	eventMessage := findProjectedMessageByContentName(items, "subagent_events")
	eventContent, _ := eventMessage["content"].(map[string]any)
	events, _ := eventContent["subagentEvents"].([]any)
	if len(events) != 3 || webString(events[0].(map[string]any)["kind"]) != "completion" ||
		webString(events[1].(map[string]any)["kind"]) != "question" ||
		webString(events[2].(map[string]any)["kind"]) != "info" {
		t.Fatalf("subagent events = %#v", eventContent)
	}
	if webString(page["oldest_cursor"]) == "" || webString(page["newest_cursor"]) == "" {
		t.Fatalf("rich message cursors = %#v", page)
	}
	firstMessage, _ := items[0].(map[string]any)
	if webString(firstMessage["msg_id"]) != "delegate-parent-user-001" || webString(firstMessage["id"]) != "delegate-parent-user-001:0" {
		t.Fatalf("rich message identity = %#v", firstMessage)
	}
	single := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+fixture.ParentFrameID+"/messages/"+fixture.ToolUseID, nil, "")
	if single.Code != http.StatusOK || webString(p3DecodeObject(t, single)["id"]) != fixture.ToolUseID {
		t.Fatalf("single delegate message status=%d body=%s", single.Code, single.Body.String())
	}
}

func TestWebConversationProjectsDelegateLineage(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	fixture, err := delegatefixture.Seed(store)
	if err != nil {
		t.Fatal(err)
	}

	rootResponse := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+fixture.ParentFrameID+"/lineage", nil, "")
	if rootResponse.Code != http.StatusOK {
		t.Fatalf("root lineage status=%d body=%s", rootResponse.Code, rootResponse.Body.String())
	}
	root := p3DecodeObject(t, rootResponse)
	directChildren, _ := root["directChildren"].([]any)
	rootChildren, _ := root["rootChildren"].([]any)
	totals, _ := root["totals"].(map[string]any)
	if webString(root["rootFrameId"]) != fixture.ParentFrameID || len(directChildren) != 2 || len(rootChildren) != 2 ||
		int(totals["needsInput"].(float64)) != 1 || int(totals["completed"].(float64)) != 1 {
		t.Fatalf("root lineage = %#v", root)
	}
	first, _ := directChildren[0].(map[string]any)
	second, _ := directChildren[1].(map[string]any)
	if webString(first["frameId"]) != fixture.ChildFrameID || webString(first["status"]) != "completed" ||
		first["directChildCount"] != float64(1) || webString(second["frameId"]) != fixture.SecondChildFrameID ||
		webString(second["status"]) != "needs-input" {
		t.Fatalf("root children = %#v", directChildren)
	}

	childResponse := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+fixture.ChildFrameID+"/lineage", nil, "")
	if childResponse.Code != http.StatusOK {
		t.Fatalf("child lineage status=%d body=%s", childResponse.Code, childResponse.Body.String())
	}
	child := p3DecodeObject(t, childResponse)
	ancestors, _ := child["ancestors"].([]any)
	grandchildren, _ := child["directChildren"].([]any)
	if len(ancestors) != 1 || webString(ancestors[0].(map[string]any)["frameId"]) != fixture.ParentFrameID ||
		len(grandchildren) != 1 || webString(grandchildren[0].(map[string]any)["frameId"]) != fixture.GrandchildFrameID ||
		webString(grandchildren[0].(map[string]any)["status"]) != "failed" {
		t.Fatalf("child lineage = %#v", child)
	}

	childRecord := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+fixture.ChildFrameID, nil, "")
	if childRecord.Code != http.StatusOK || webString(p3DecodeObject(t, childRecord)["id"]) != fixture.ChildFrameID {
		t.Fatalf("child record status=%d body=%s", childRecord.Code, childRecord.Body.String())
	}
	method := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+fixture.ParentFrameID+"/lineage", nil, "")
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("lineage method status=%d body=%s", method.Code, method.Body.String())
	}
}

func TestWebConversationPreservesDelegateProjectionAfterTypedBootstrap(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	fixture, err := delegatefixture.Seed(store)
	if err != nil {
		t.Fatal(err)
	}
	completed := "completed"
	if _, err := store.UpdateFrame(fixture.ParentFrameID, workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report, err := repository.ReconcileNoStreamFrameHistories(
		context.Background(),
		transcriptstore.ReconcileNoStreamFrameHistoriesInput{
			Limit: 4, AfterOwnerID: fixture.UserID, AfterSessionID: "delegate-fixture-",
		},
	)
	if err != nil || report.Scanned != 4 || report.PayloadCreated != 3 || report.Blocked != 1 || report.Deferred != 0 {
		t.Fatalf("bootstrap report=%#v err=%v", report, err)
	}
	processing := "processing"
	if _, err := store.UpdateFrame(fixture.ParentFrameID, workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}

	response := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+fixture.ParentFrameID+"/messages?limit=50", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("delegate messages status=%d body=%s", response.Code, response.Body.String())
	}
	items, _ := p3DecodeObject(t, response)["items"].([]any)
	delegation := findProjectedMessageByCallID(items, fixture.ToolUseID)
	content, _ := delegation["content"].(map[string]any)
	subagent, _ := content["subagent"].(map[string]any)
	if webString(content["name"]) != "Agent" || webString(subagent["frameId"]) != fixture.ChildFrameID ||
		webString(subagent["status"]) != "completed" || subagent["ordinal"] != float64(1) && subagent["ordinal"] != 1 {
		t.Fatalf("typed delegate projection = %#v", delegation)
	}
	eventMessage := findProjectedMessageByContentName(items, "subagent_events")
	eventContent, _ := eventMessage["content"].(map[string]any)
	events, _ := eventContent["subagentEvents"].([]any)
	if len(events) != 3 {
		t.Fatalf("typed subagent events = %#v", eventContent)
	}

	childResponse := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+fixture.ChildFrameID+"/messages?limit=50", nil, "")
	if childResponse.Code != http.StatusOK {
		t.Fatalf("child messages status=%d body=%s", childResponse.Code, childResponse.Body.String())
	}
	childItems, _ := p3DecodeObject(t, childResponse)["items"].([]any)
	grandchild := findProjectedMessageByCallID(childItems, fixture.GrandchildToolUseID)
	grandchildContent, _ := grandchild["content"].(map[string]any)
	grandchildSubagent, _ := grandchildContent["subagent"].(map[string]any)
	if webString(grandchildSubagent["frameId"]) != fixture.GrandchildFrameID ||
		grandchildSubagent["ordinal"] != float64(3) && grandchildSubagent["ordinal"] != 3 {
		t.Fatalf("typed grandchild projection = %#v", grandchild)
	}
}

func findProjectedMessage(items []any, id string) map[string]any {
	for _, item := range items {
		message, _ := item.(map[string]any)
		if webString(message["id"]) == id {
			return message
		}
	}
	return nil
}

func findProjectedMessageByContentName(items []any, name string) map[string]any {
	for _, item := range items {
		message, _ := item.(map[string]any)
		content, _ := message["content"].(map[string]any)
		if webString(content["name"]) == name {
			return message
		}
	}
	return nil
}

func findProjectedMessageByCallID(items []any, callID string) map[string]any {
	for _, item := range items {
		message, _ := item.(map[string]any)
		content, _ := message["content"].(map[string]any)
		if webString(content["call_id"]) == callID {
			return message
		}
	}
	return nil
}
