package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/compat/contracts"
)

func TestRealtimeEventsPersistAllBaselineTypesAndRemainUserScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for index, baseline := range contracts.EventTypes {
		userID := "user-1"
		if index == len(contracts.EventTypes)-1 {
			userID = "user-2"
		}
		if _, err := store.AppendRealtimeEvent(RealtimeEventInput{
			UserID: userID, ProjectID: "project-1", RootFrameID: "root", FrameID: "frame",
			Type: baseline.Name, Payload: map[string]any{"project_id": "project-1", "root_frame_id": "root", "frame_id": "frame"},
		}); err != nil {
			t.Fatalf("append %s: %v", baseline.Name, err)
		}
	}
	global, err := store.AppendRealtimeEvent(RealtimeEventInput{Type: "update_available", Payload: map[string]any{"version": "4.0.3"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.ListRealtimeEvents(RealtimeEventFilter{UserID: "user-1", IncludeGlobal: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(contracts.EventTypes) {
		t.Fatalf("user-scoped event count=%d want=%d", len(events), len(contracts.EventTypes))
	}
	if events[len(events)-1].ID != global.ID || events[len(events)-1].Sequence <= events[0].Sequence {
		t.Fatalf("global/restart event sequence=%#v", events[len(events)-1])
	}
	for _, event := range events {
		if event.UserID == "user-2" {
			t.Fatalf("cross-user event leaked: %#v", event)
		}
	}
	frameUpdates, err := reopened.ListRealtimeEvents(RealtimeEventFilter{UserID: "user-1", Type: "frame_update", Limit: 10})
	if err != nil || len(frameUpdates) != 1 || len(frameUpdates[0].Invalidations) == 0 {
		t.Fatalf("frame update events=%#v err=%v", frameUpdates, err)
	}
}

func TestRealtimeEventAppendIsIdempotentBySourceID(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	input := RealtimeEventInput{
		ID: "frame-event:source-1", UserID: "user-1", ProjectID: "project-1",
		RootFrameID: "root", FrameID: "frame", Type: "frame_messages_delta",
		Payload: map[string]any{"project_id": "project-1", "root_frame_id": "root", "frame_id": "frame", "text": "same"},
	}
	first, err := store.AppendRealtimeEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AppendRealtimeEvent(input)
	if err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	if replayed.Sequence != first.Sequence || replayed.ID != first.ID || !replayed.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("replayed event=%#v first=%#v", replayed, first)
	}
	input.Payload = map[string]any{"project_id": "project-1", "root_frame_id": "root", "frame_id": "frame", "text": "changed"}
	if _, err := store.AppendRealtimeEvent(input); err == nil || !strings.Contains(err.Error(), "different realtime event") {
		t.Fatalf("conflicting source id error=%v", err)
	}
}

func TestRealtimeEventWatermarkAndBoundedPageUseExactScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(user, project, root, frame, eventType string) RealtimeEvent {
		t.Helper()
		event, err := store.AppendRealtimeEvent(RealtimeEventInput{
			UserID: user, ProjectID: project, RootFrameID: root, FrameID: frame,
			Type: eventType, Payload: map[string]any{"scope": project},
		})
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	first := appendEvent("owner-a", "project-a", "root-a", "frame-a", "frame_update")
	appendEvent("owner-b", "project-a", "root-a", "frame-a", "frame_update")
	appendEvent("owner-a", "project-b", "root-b", "frame-b", "text_chunk")
	none := appendEvent("owner-a", "project-a", "root-a", "frame-a", "rolling_compact_status")
	global := appendEvent("", "", "", "", "update_available")

	filter := RealtimeEventFilter{UserID: "owner-a", ProjectID: "project-a", RootFrameID: "root-a", FrameID: "frame-a"}
	latest, err := store.LatestRealtimeEventSequence(filter)
	if err != nil || latest != none.Sequence {
		t.Fatalf("latest=%d want=%d err=%v", latest, none.Sequence, err)
	}
	pageFilter := filter
	pageFilter.ThroughSequence = first.Sequence
	events, err := store.ListRealtimeEvents(pageFilter)
	if err != nil || len(events) != 1 || events[0].Sequence != first.Sequence {
		t.Fatalf("bounded events=%#v err=%v", events, err)
	}
	globalLatest, err := store.LatestRealtimeEventSequence(RealtimeEventFilter{UserID: "owner-b", IncludeGlobal: true})
	if err != nil || globalLatest != global.Sequence {
		t.Fatalf("global latest=%d want=%d err=%v", globalLatest, global.Sequence, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reopenedLatest, err := store.LatestRealtimeEventSequence(filter)
	if err != nil || reopenedLatest != none.Sequence {
		t.Fatalf("reopened latest=%d want=%d err=%v", reopenedLatest, none.Sequence, err)
	}
	resumed := appendEvent("owner-a", "project-a", "root-a", "frame-a", "text_chunk")
	resumedEvents, err := store.ListRealtimeEvents(RealtimeEventFilter{UserID: "owner-a", ProjectID: "project-a", AfterSequence: reopenedLatest})
	if err != nil || len(resumedEvents) != 1 || resumedEvents[0].Sequence != resumed.Sequence {
		t.Fatalf("resumed events=%#v err=%v", resumedEvents, err)
	}
}
