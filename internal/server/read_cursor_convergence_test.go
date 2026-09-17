package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestReadCursorForUnprojectedStreamedMessageReturnsConflictInsteadOfInternalError(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-cursor-convergence", "frame-cursor-convergence")
	ctx := context.Background()
	stream, err := repo.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: "frame:frame-cursor-convergence", OwnerID: "local", ExternalID: "frame-cursor-convergence",
		SessionID: "frame-cursor-convergence", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-cursor-convergence", RootFrameID: "frame-cursor-convergence",
		FrameID: "frame-cursor-convergence", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "cursor-user-client",
		FrameEventID: "cursor-user-frame-event", MessageUUID: "cursor-user-message", Text: "test",
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo}
	frame, found, err := store.GetCompatibilityFrame(stream.FrameID)
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	body, err := json.Marshal(map[string]any{
		"message_uuid": "transient-streamed-tool-message", "message_index": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.handleCompatibilityFrameReadCursor(
		recorder, httptest.NewRequest(http.MethodPut, "/api/frames/"+frame.ID+"/read-cursor", bytes.NewReader(body)), frame,
	)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("unprojected read cursor status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if cursor, found, err := store.GetReadCursor(frame.ID); err != nil || found {
		t.Fatalf("unprojected coordinate mutated cursor=%#v found=%t err=%v", cursor, found, err)
	}
}

func TestReadCursorProjectionFailuresUseRecoverableConflictStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "projection panic", err: errReadCursorProjectionFailed},
		{name: "projection stale", err: transcriptstore.ErrTranscriptWebProjectionStale},
		{name: "event race", err: transcriptstore.ErrEventConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeV11StoreError(recorder, test.err)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
