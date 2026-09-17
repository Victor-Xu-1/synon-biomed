package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/realtime"
)

func TestPublicConversationHistoryFailsClosedOutsideActivatedTranscriptAuthority(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-history-cutover", "frame-history-cutover")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})

	_, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-history-cutover", MessageUUID: "canonical-user", ClientMessageID: "canonical-client", Text: "canonical",
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-history-cutover")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	forceLegacyTranscriptFrameAuthority(t, db, stream)
	probe, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "frame-history-cutover", Type: "user_message",
		Payload: map[string]any{"id": "legacy-probe", "role": "user", "text": "legacy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, found, err := store.FindRealtimeFrameProjectionForOutbox(context.Background(), probe.ID)
	if err != nil || !found || projection.TranscriptBacked {
		t.Fatalf("projection=%#v found=%t err=%v", projection, found, err)
	}
	server = New(Options{Workspace: store, FileRoot: t.TempDir()})
	server.transcriptStore = repo
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	for _, path := range []string{
		"/api/conversations/frame-history-cutover/messages?limit=10",
		"/api/conversations/frame-history-cutover/messages/canonical-user",
		"/api/conversations/frame-history-cutover/messages?before=idx%3A0&limit=1",
		"/api/conversations/frame-history-cutover/messages?branch_id=br_00000000&limit=10",
	} {
		response := p3JSONRequest(t, server, http.MethodGet, path, nil, "")
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		var envelope map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope) != 2 || envelope["code"] != "HISTORY_NOT_READY" ||
			envelope["message"] != "conversation history is still being prepared" {
			t.Fatalf("path=%s envelope=%#v", path, envelope)
		}
	}
	legacy := p3JSONRequest(t, server, http.MethodGet,
		"/api/frames/frame-history-cutover/messages?from=0&limit=10", nil, "")
	if legacy.Code != http.StatusOK || !bytes.Contains(legacy.Body.Bytes(), []byte("legacy-probe")) {
		t.Fatalf("legacy status=%d body=%s", legacy.Code, legacy.Body.String())
	}

	frameContext, found, err := store.GetFrameRealtimeContext("frame-history-cutover")
	if err != nil || !found {
		t.Fatalf("frame context found=%t err=%v", found, err)
	}
	before := len(transcriptWebEvents(t, store, "local"))
	if err := server.publishWebFrameEventProjection(frameContext, probe); err != nil {
		t.Fatal(err)
	}
	if after := len(transcriptWebEvents(t, store, "local")); after != before {
		t.Fatalf("dynamic legacy projection added events before=%d after=%d", before, after)
	}
	if err := server.FanoutRealtimeOutbox(
		workspace.RealtimeEvent{Kind: realtime.DeliveryNone}, &projection,
	); err != nil {
		t.Fatal(err)
	}
	if after := len(transcriptWebEvents(t, store, "local")); after != before {
		t.Fatalf("outbox legacy projection added events before=%d after=%d", before, after)
	}
}

func TestActivatedTranscriptHistoryIgnoresFrameOnlyMessages(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-history-active", "frame-history-active")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	_, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-history-active", MessageUUID: "canonical-user", ClientMessageID: "canonical-client", Text: "canonical",
	})
	if err != nil {
		t.Fatal(err)
	}
	frameOnly, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "frame-history-active", Type: "user_message",
		Payload: map[string]any{"id": "frame-only", "role": "user", "text": "must not escape"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, found, err := store.FindRealtimeFrameProjectionForOutbox(context.Background(), frameOnly.ID)
	if err != nil || !found || !projection.TranscriptBacked {
		t.Fatalf("projection=%#v found=%t err=%v", projection, found, err)
	}

	page := transcriptToolHistoryPage(t, server, "/api/conversations/frame-history-active/messages?limit=10", "")
	items, ok := page["items"].([]any)
	if !ok || len(items) != 1 || webString(items[0].(map[string]any)["id"]) != "canonical-user" {
		t.Fatalf("page=%#v", page)
	}
	single := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-history-active/messages/frame-only", nil, "")
	if single.Code != http.StatusNotFound {
		t.Fatalf("single status=%d body=%s", single.Code, single.Body.String())
	}
	frameContext, found, err := store.GetFrameRealtimeContext("frame-history-active")
	if err != nil || !found {
		t.Fatalf("frame context found=%t err=%v", found, err)
	}
	if err := server.publishWebFrameEventProjection(frameContext, frameOnly); err != nil {
		t.Fatal(err)
	}
	if err := server.FanoutRealtimeOutbox(workspace.RealtimeEvent{Kind: realtime.DeliveryNone}, &projection); err != nil {
		t.Fatal(err)
	}
	for _, event := range transcriptWebEvents(t, store, "local") {
		raw, err := json.Marshal(event.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("frame-only")) || bytes.Contains(raw, []byte("must not escape")) {
			t.Fatalf("frame-only live projection=%s", raw)
		}
	}
}

func TestRealtimeOutboxRecognizesActivationBackedTranscriptAuthority(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-history-activation", "frame-history-activation")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-history-activation", OwnerID: "local", ExternalID: "frame-history-activation",
		SessionID: "frame-history-activation", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-history-activation", RootFrameID: "frame-history-activation",
		FrameID: "frame-history-activation", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	forceLegacyTranscriptFrameAuthority(t, db, stream)
	if _, err := db.Exec(`UPDATE frames SET status='completed' WHERE id='frame-history-activation'`); err != nil {
		t.Fatal(err)
	}
	legacyMessage, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "frame-history-activation", Type: "user_message",
		Payload: map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "legacy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		_, appendErr := tx.AppendHistoricalFrameReferences(
			context.Background(), stream.UID, stream.OwnerID, []string{legacyMessage.ID},
		)
		return appendErr
	}); err != nil {
		t.Fatal(err)
	}
	report, err := repo.ReconcileLegacyFrameHistories(
		context.Background(), transcriptstore.DefaultLegacyFrameHistoryReconciliationInput(),
	)
	if err != nil || report.Activated != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	authority, found, err := repo.GetFrameAuthorityBySession(context.Background(), "local", "frame-history-activation")
	if err != nil || !found || !authority.LegacyActivationActive() {
		t.Fatalf("authority=%#v found=%t err=%v", authority, found, err)
	}
	probe, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "frame-history-activation", Type: "frame_paused", Payload: map[string]any{"reason": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, found, err := store.FindRealtimeFrameProjectionForOutbox(context.Background(), probe.ID)
	if err != nil || !found || !projection.TranscriptBacked {
		t.Fatalf("projection=%#v found=%t err=%v", projection, found, err)
	}
}

func TestRealtimeOutboxRejectsMismatchedAuthorityStreamIdentity(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-history-mismatch", "frame-history-mismatch")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-history-mismatch", MessageUUID: "canonical-user", ClientMessageID: "canonical-client", Text: "canonical",
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER transcript_frame_authority_transition`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE transcript_frame_authority SET active_epoch=active_epoch+1
		WHERE owner_id='local' AND session_id='frame-history-mismatch'`); err != nil {
		t.Fatal(err)
	}
	probe, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "frame-history-mismatch", Type: "frame_paused", Payload: map[string]any{"reason": "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, found, err := store.FindRealtimeFrameProjectionForOutbox(context.Background(), probe.ID)
	if err != nil || !found || projection.TranscriptBacked {
		t.Fatalf("projection=%#v found=%t err=%v", projection, found, err)
	}
}
