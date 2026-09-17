package server

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestWebConversationCloneUsesOneCanonicalTranscriptTransaction(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close clone fixture workspace: %v", err)
		}
	})
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app := newV11TestServer(t, Options{Workspace: store, Transcript: repository, FileRoot: t.TempDir()})
	// Join server-owned catalog refreshes before their borrowed stores close.
	t.Cleanup(func() { closeTestServer(t, app) })
	project := createP3Project(t, store, "clone-web-project", "local")
	create := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Canonical source",
		"assistant": map[string]any{
			"id": "synonbiomed:OPERON", "conversation_overrides": map[string]any{"model": "test-model"},
		},
		"extra": map[string]any{"project_id": project.ID, "selected_view": "timeline"},
	}, "")
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	created := p3DecodeObject(t, create)
	sourceID := webString(created["id"])
	metadata, found, err := store.GetFrameRuntimeMetadata(sourceID)
	if err != nil || !found {
		t.Fatalf("source metadata found=%v err=%v", found, err)
	}
	metadata.ContextData["_pending_input_requests"] = []any{map[string]any{"request_id": "stale-source-only"}}
	if _, err := store.SetFrameRuntimeMetadata(sourceID, metadata); err != nil {
		t.Fatal(err)
	}
	send := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+sourceID+"/messages", map[string]any{
		"content": "Clone this durable input",
	}, "")
	if send.Code != http.StatusAccepted {
		t.Fatalf("send status=%d body=%s", send.Code, send.Body.String())
	}
	cloneBody := map[string]any{
		"conversation": created,
		"intent_id":    "bced2209-d4dc-42c7-8d5c-c64ff986cc43",
	}
	outboxBefore, err := store.CountOutboxEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	invalidIntent := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/clone", map[string]any{
		"conversation": created, "intent_id": "not-a-uuid",
	}, "")
	if invalidIntent.Code != http.StatusBadRequest ||
		webString(p3DecodeObject(t, invalidIntent)["message"]) != "invalid clone request" {
		t.Fatalf("invalid intent status=%d body=%s", invalidIntent.Code, invalidIntent.Body.String())
	}
	unsettled := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/clone", cloneBody, "")
	if unsettled.Code != http.StatusConflict ||
		webString(p3DecodeObject(t, unsettled)["message"]) != "conversation clone conflicts with current state" {
		t.Fatalf("unsettled clone status=%d body=%s", unsettled.Code, unsettled.Body.String())
	}
	if outboxAfterRejected, err := store.CountOutboxEvents(context.Background()); err != nil || outboxAfterRejected != outboxBefore {
		t.Fatalf("rejected clone outbox before=%d after=%d err=%v", outboxBefore, outboxAfterRejected, err)
	}
	sourceStream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", sourceID)
	if err != nil || !found {
		t.Fatalf("source stream found=%v err=%v", found, err)
	}
	claimed, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: sourceStream.UID, OwnerID: sourceStream.OwnerID, RunnerID: "clone-source-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("source claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "clone-source-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil || !created {
		t.Fatalf("source finish created=%v err=%v", created, err)
	}
	outboxBefore, err = store.CountOutboxEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clone := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/clone", map[string]any{
		"conversation": created, "intent_id": cloneBody["intent_id"],
	}, "")
	if clone.Code != http.StatusCreated {
		t.Fatalf("clone status=%d body=%s", clone.Code, clone.Body.String())
	}
	cloneID := webString(p3DecodeObject(t, clone)["id"])
	if cloneID == "" || cloneID == sourceID {
		t.Fatalf("clone id=%q source=%q", cloneID, sourceID)
	}
	page := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+cloneID+"/messages?limit=50", nil, "")
	if page.Code != http.StatusOK {
		t.Fatalf("clone messages status=%d body=%s", page.Code, page.Body.String())
	}
	items, _ := p3DecodeObject(t, page)["items"].([]any)
	var clonedText, terminalStatus string
	if len(items) == 2 {
		item, _ := items[0].(map[string]any)
		content, _ := item["content"].(map[string]any)
		clonedText = webString(content["content"])
		terminal, _ := items[1].(map[string]any)
		terminalStatus = webString(terminal["terminal_status"])
	}
	if len(items) != 2 || clonedText != "Clone this durable input" || terminalStatus != "completed" {
		t.Fatalf("clone messages=%#v", items)
	}
	firstMessage, _ := items[0].(map[string]any)
	firstMessageID := webString(firstMessage["id"])
	single := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+cloneID+"/messages/"+url.PathEscape(firstMessageID), nil, "")
	if single.Code != http.StatusOK || webString(p3DecodeObject(t, single)["id"]) != firstMessageID {
		t.Fatalf("single status=%d body=%s first=%q", single.Code, single.Body.String(), firstMessageID)
	}
	latestOne := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+cloneID+"/messages?limit=1", nil, "")
	latestOnePage := p3DecodeObject(t, latestOne)
	latestOneItems, _ := latestOnePage["items"].([]any)
	oldestCursor := webString(latestOnePage["oldest_cursor"])
	if latestOne.Code != http.StatusOK || len(latestOneItems) != 1 || oldestCursor == "" ||
		latestOnePage["has_more_before"] != true || latestOnePage["has_more_after"] != false {
		t.Fatalf("latest one status=%d page=%#v", latestOne.Code, latestOnePage)
	}
	previousOne := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+cloneID+"/messages?limit=1&before="+url.QueryEscape(oldestCursor), nil, "")
	previousOnePage := p3DecodeObject(t, previousOne)
	previousOneItems, _ := previousOnePage["items"].([]any)
	if previousOne.Code != http.StatusOK || len(previousOneItems) != 1 ||
		webString(previousOneItems[0].(map[string]any)["id"]) != firstMessageID {
		t.Fatalf("previous one status=%d page=%#v", previousOne.Code, previousOnePage)
	}
	previousCursor := webString(previousOnePage["newest_cursor"])
	nextOne := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+cloneID+"/messages?limit=1&after="+url.QueryEscape(previousCursor), nil, "")
	nextOnePage := p3DecodeObject(t, nextOne)
	nextOneItems, _ := nextOnePage["items"].([]any)
	if nextOne.Code != http.StatusOK || len(nextOneItems) != 1 ||
		webString(nextOneItems[0].(map[string]any)["terminal_status"]) != "completed" {
		t.Fatalf("next one status=%d page=%#v", nextOne.Code, nextOnePage)
	}
	anchored := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+cloneID+"/messages?limit=1&anchor_message_id="+url.QueryEscape(firstMessageID), nil, "")
	anchoredPage := p3DecodeObject(t, anchored)
	anchoredItems, _ := anchoredPage["items"].([]any)
	if anchored.Code != http.StatusOK || len(anchoredItems) != 1 ||
		webString(anchoredItems[0].(map[string]any)["id"]) != firstMessageID {
		t.Fatalf("anchored status=%d page=%#v", anchored.Code, anchoredPage)
	}
	foreignList := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+cloneID+"/messages?limit=50", nil, "foreign")
	foreignSingle := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+cloneID+"/messages/"+url.PathEscape(firstMessageID), nil, "foreign")
	if foreignList.Code != http.StatusNotFound || foreignSingle.Code != http.StatusNotFound {
		t.Fatalf("foreign list=%d body=%s single=%d body=%s",
			foreignList.Code, foreignList.Body.String(), foreignSingle.Code, foreignSingle.Body.String())
	}
	clonedMetadata, found, err := store.GetFrameRuntimeMetadata(cloneID)
	if err != nil || !found {
		t.Fatalf("clone metadata found=%v err=%v", found, err)
	}
	if clonedMetadata.ContextData["web_extra"] == nil || clonedMetadata.ContextData["web_assistant"] == nil ||
		clonedMetadata.ContextData["_pending_input_requests"] != nil || len(clonedMetadata.ContextData) != 2 {
		t.Fatalf("clone metadata=%#v", clonedMetadata)
	}
	frameEvents, err := store.ListFrameEvents(cloneID, 0, 10)
	if err != nil || len(frameEvents) != 1 || frameEvents[0].Type != "frame_created" {
		t.Fatalf("clone frame events=%#v err=%v", frameEvents, err)
	}
	outboxAfter, err := store.CountOutboxEvents(context.Background())
	if err != nil || outboxAfter != outboxBefore+2 {
		t.Fatalf("clone outbox before=%d after=%d err=%v", outboxBefore, outboxAfter, err)
	}
	retry := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/clone", cloneBody, "")
	if retry.Code != http.StatusCreated || webString(p3DecodeObject(t, retry)["id"]) != cloneID {
		t.Fatalf("retry status=%d body=%s clone=%q", retry.Code, retry.Body.String(), cloneID)
	}
	if outboxAfterRetry, err := store.CountOutboxEvents(context.Background()); err != nil || outboxAfterRetry != outboxAfter {
		t.Fatalf("retry outbox first=%d retry=%d err=%v", outboxAfter, outboxAfterRetry, err)
	}
	secondSource := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Canonical source",
		"assistant": map[string]any{
			"id": "synonbiomed:OPERON", "conversation_overrides": map[string]any{"model": "test-model"},
		},
		"extra": map[string]any{"project_id": project.ID, "selected_view": "timeline"},
	}, "")
	if secondSource.Code != http.StatusCreated {
		t.Fatalf("second source status=%d body=%s", secondSource.Code, secondSource.Body.String())
	}
	secondCreated := p3DecodeObject(t, secondSource)
	secondSourceID := webString(secondCreated["id"])
	completed := "completed"
	if _, err := store.UpdateFrame(secondSourceID, workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	outboxBeforeConflict, err := store.CountOutboxEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conflictingIntent := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/clone", map[string]any{
		"conversation": secondCreated, "intent_id": cloneBody["intent_id"],
	}, "")
	if conflictingIntent.Code != http.StatusConflict ||
		webString(p3DecodeObject(t, conflictingIntent)["message"]) != "conversation clone conflicts with current state" {
		t.Fatalf("conflicting intent status=%d body=%s", conflictingIntent.Code, conflictingIntent.Body.String())
	}
	if outboxAfterConflict, err := store.CountOutboxEvents(context.Background()); err != nil || outboxAfterConflict != outboxBeforeConflict {
		t.Fatalf("conflicting intent outbox first=%d conflict=%d err=%v", outboxBeforeConflict, outboxAfterConflict, err)
	}

	cloneStream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", cloneID)
	if err != nil || !found {
		t.Fatalf("clone stream found=%v err=%v", found, err)
	}
	projected, err := repository.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: cloneStream.UID, OwnerID: "local", Limit: 10,
	})
	if err != nil || len(projected) != 2 || projected[0].Event.Source != transcriptstore.EventSourcePayload ||
		projected[1].Event.Source != transcriptstore.EventSourcePayload {
		t.Fatalf("clone projected=%#v err=%v", projected, err)
	}
	beforeForeignDelete, err := store.CountOutboxEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foreignDelete := p3JSONRequest(t, app, http.MethodDelete, "/api/conversations/"+cloneID, nil, "foreign")
	if foreignDelete.Code != http.StatusNotFound {
		t.Fatalf("foreign delete status=%d body=%s", foreignDelete.Code, foreignDelete.Body.String())
	}
	if afterForeignDelete, err := store.CountOutboxEvents(context.Background()); err != nil || afterForeignDelete != beforeForeignDelete {
		t.Fatalf("foreign delete outbox before=%d after=%d err=%v", beforeForeignDelete, afterForeignDelete, err)
	}
	deleted := p3JSONRequest(t, app, http.MethodDelete, "/api/conversations/"+cloneID, nil, "")
	if deleted.Code != http.StatusOK || deleted.Body.String() != "true\n" {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, found, err := store.GetCompatibilityFrame(cloneID); err != nil || found {
		t.Fatalf("deleted frame found=%t err=%v", found, err)
	}
	if _, found, err := repository.GetFrameStreamBySession(context.Background(), "local", cloneID); err != nil || found {
		t.Fatalf("deleted transcript found=%t err=%v", found, err)
	}
}
