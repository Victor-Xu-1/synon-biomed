package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityFrameDeleteReturnsSuccessAfterCommittedArtifactCleanupFailure(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-delete-artifact", UserID: "local", Name: "Delete artifact project",
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-delete-artifact", ProjectID: "project-delete-artifact", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "frame-delete-artifact-blob", ProjectID: "project-delete-artifact", Name: "delete.txt",
		ContentType: "text/plain", Content: strings.NewReader("artifact content"), MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	folder, err := store.CreateArtifactFolder(workspace.CreateArtifactFolderInput{
		ID: "frame-delete-artifact-folder", ProjectID: "project-delete-artifact", Name: "Frame artifacts",
		RootFrameID: frame.RootFrameID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetArtifactFolder(artifact.ID, folder.ID); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(databasePath+".blobs", filepath.FromSlash(version.StoragePath))
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(abs, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	if err := os.WriteFile(filepath.Join(abs, "undeletable-child"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	serverApp := New(Options{FileRoot: runtimeRoot, Workspace: store})
	t.Cleanup(func() { _ = serverApp.Close(context.Background()) })
	response := compatJSONRequest(t, serverApp.Handler(), http.MethodDelete, "/api/frames/"+frame.ID, "local", nil, http.StatusOK)
	if response["frame_id"] != frame.ID || response["artifact_cleanup_failures"] != float64(1) {
		t.Fatalf("frame delete response=%#v", response)
	}
	if _, found, err := store.GetFrame(frame.ID); err != nil || found {
		t.Fatalf("deleted frame found=%t err=%v", found, err)
	}
}

func TestCompatibilityFrameDeleteReturnsSuccessAfterRuntimeCleanupWarnings(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-delete-runtime", UserID: "local", Name: "Delete runtime project",
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-delete-runtime", ProjectID: "project-delete-runtime", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	brokenRuntimeRoot := filepath.Join(runtimeRoot, "broken-runtime-root")
	if err := os.WriteFile(brokenRuntimeRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	serverApp := New(Options{FileRoot: runtimeRoot, Workspace: store})
	serverApp.sessionStore = sessionstore.NewStore(brokenRuntimeRoot)
	serverApp.eventJournal = eventjournal.NewEventJournal(brokenRuntimeRoot)
	t.Cleanup(func() { _ = serverApp.Close(context.Background()) })

	response := compatJSONRequest(t, serverApp.Handler(), http.MethodDelete, "/api/frames/"+frame.ID, "local", nil, http.StatusOK)
	warnings, _ := response["runtime_cleanup_warnings"].(float64)
	if response["frame_id"] != frame.ID || warnings < 2 {
		t.Fatalf("frame delete response=%#v", response)
	}
	if _, found, err := store.GetFrame(frame.ID); err != nil || found {
		t.Fatalf("deleted frame found=%t err=%v", found, err)
	}
}

func TestReadCursorProjectionFlightContainsLeaderPanic(t *testing.T) {
	type result struct {
		coordinates []canonicalReadCursorCoordinate
		active      bool
		err         error
	}
	server := &Server{}
	started := make(chan struct{})
	release := make(chan struct{})
	results := make(chan result, 2)
	go func() {
		coordinates, _, active, err := server.projectReadCursorCoordinates(context.Background(), "panic", func() (
			[]canonicalReadCursorCoordinate, transcriptstore.ProjectionSnapshot, bool, error,
		) {
			close(started)
			<-release
			panic("private projection panic")
		})
		results <- result{coordinates: coordinates, active: active, err: err}
	}()
	<-started

	server.readCursorCache.mu.Lock()
	flight := server.readCursorCache.flights["panic"]
	server.readCursorCache.mu.Unlock()
	if flight == nil {
		t.Fatal("leader flight was not published")
	}
	go func() {
		<-flight.done
		results <- result{coordinates: flight.coordinates, active: flight.active, err: flight.err}
	}()
	close(release)

	for range 2 {
		got := <-results
		if len(got.coordinates) != 0 || !got.active || !errors.Is(got.err, errReadCursorProjectionFailed) {
			t.Fatalf("panic result = coordinates %#v, active %v, error %v", got.coordinates, got.active, got.err)
		}
	}

	coordinates, _, active, err := server.projectReadCursorCoordinates(context.Background(), "panic", func() (
		[]canonicalReadCursorCoordinate, transcriptstore.ProjectionSnapshot, bool, error,
	) {
		return []canonicalReadCursorCoordinate{{id: "reused", messageID: "reused"}}, transcriptstore.ProjectionSnapshot{}, true, nil
	})
	if err != nil || !active || len(coordinates) != 1 || coordinates[0].id != "reused" {
		t.Fatalf("reused flight = coordinates %#v, active %v, error %v", coordinates, active, err)
	}
}

func TestCompatibilityDeleteFencesAuthorizedFrameIncarnation(t *testing.T) {
	tests := []struct {
		name       string
		newOwner   string
		newProject string
	}{
		{name: "same owner and project", newOwner: "owner-a", newProject: "project-a"},
		{name: "foreign owner and project", newOwner: "owner-b", newProject: "project-b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			if _, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
				ID: "project-a", UserID: "owner-a", Name: "Generation A",
			}, "project-a-created"); err != nil {
				t.Fatal(err)
			}
			first, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
				ID: "reused-frame", ProjectID: "project-a", AgentName: "OPERON",
				Status: "completed", ConversationType: "agent",
			}, "owner-a", "frame-a-created", "frame-a-source")
			if err != nil {
				t.Fatal(err)
			}
			loaded, found, err := store.GetCompatibilityFrame("reused-frame")
			if err != nil || !found || loaded.IncarnationID == "" || loaded.IncarnationID != first.IncarnationID {
				t.Fatalf("loaded=%#v found=%t err=%v", loaded, found, err)
			}
			if err := store.DeleteFrame("reused-frame"); err != nil {
				t.Fatal(err)
			}
			if test.newProject != "project-a" {
				if _, err := store.CreateProjectRealtime(ctx, workspace.CreateProjectInput{
					ID: test.newProject, UserID: test.newOwner, Name: "Generation B",
				}, "project-b-created"); err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
				ID: "reused-frame", ProjectID: test.newProject, AgentName: "OPERON",
				Status: "completed", ConversationType: "agent",
			}, test.newOwner, "frame-b-created", "frame-b-source")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.DeleteCompatibilityFrameTree(
				loaded.ID, "owner-a", loaded.IncarnationID,
			); err == nil {
				t.Fatal("stale authorized frame deleted a newer incarnation")
			}
			preserved, found, err := store.GetFrame(current.ID)
			if err != nil || !found || preserved.ProjectID != test.newProject ||
				preserved.IncarnationID == "" || preserved.IncarnationID != current.IncarnationID ||
				preserved.IncarnationID == first.IncarnationID {
				t.Fatalf("new incarnation=%#v found=%t err=%v", preserved, found, err)
			}
		})
	}
}

func TestRawWorkspaceDeleteAcceptsEventlessCompatibilityFrameIncarnation(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-eventless", UserID: "local", Name: "Eventless compatibility",
	}); err != nil {
		t.Fatal(err)
	}
	serverApp := New(Options{Workspace: store})
	t.Cleanup(func() { _ = serverApp.Close(context.Background()) })
	created := compatJSONRequest(t, serverApp.Handler(), http.MethodPost, "/api/frames", "local", map[string]any{
		"project_id": "project-eventless",
	}, http.StatusCreated)
	frameID, _ := created["root_frame_id"].(string)
	if frameID == "" {
		t.Fatalf("created frame=%#v", created)
	}
	frame, found, err := store.GetFrame(frameID)
	if err != nil || !found || frame.IncarnationID == "" {
		t.Fatalf("eventless frame=%#v found=%t err=%v", frame, found, err)
	}
	serveWorkspaceJSON(t, serverApp.Handler(), http.MethodDelete, "/api/go/frames/"+frameID, nil, http.StatusOK)
	if _, found, err := store.GetFrame(frameID); err != nil || found {
		t.Fatalf("deleted eventless frame found=%t err=%v", found, err)
	}
}

func TestCompatibilityDeleteScopesSurvivingChildAgainstForeignRootReuse(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, project := range []workspace.CreateProjectInput{
		{ID: "project-a", UserID: "owner-a", Name: "Owner A"},
		{ID: "project-b", UserID: "owner-b", Name: "Owner B"},
	} {
		if _, err := store.CreateProjectRealtime(ctx, project, "create-"+project.ID); err != nil {
			t.Fatal(err)
		}
	}
	root, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: "reused-root", ProjectID: "project-a", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	}, "owner-a", "owner-a-root", "owner-a-root-source")
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: "surviving-child", ProjectID: "project-a", ParentFrameID: root.ID, AgentName: "REVIEWER",
		Status: "completed", ConversationType: "delegate",
	}, "owner-a", "owner-a-child", "owner-a-child-source")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFrame(root.ID); err != nil {
		t.Fatal(err)
	}
	foreignRoot, err := store.CreateFrameRealtime(ctx, workspace.CreateFrameInput{
		ID: root.ID, ProjectID: "project-b", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	}, "owner-b", "owner-b-root", "owner-b-root-source")
	if err != nil {
		t.Fatal(err)
	}
	loadedChild, found, err := store.GetCompatibilityFrame(child.ID)
	if err != nil || !found {
		t.Fatalf("child found=%t err=%v", found, err)
	}
	if _, err := store.DeleteCompatibilityFrameTree(
		loadedChild.ID, "owner-a", loadedChild.IncarnationID,
	); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetFrame(child.ID); err != nil || found {
		t.Fatalf("owner-a child found=%t err=%v", found, err)
	}
	preserved, found, err := store.GetFrame(foreignRoot.ID)
	if err != nil || !found || preserved.ProjectID != "project-b" || preserved.IncarnationID != foreignRoot.IncarnationID {
		t.Fatalf("foreign root=%#v found=%t err=%v", preserved, found, err)
	}
}

func TestFrameCompatibilityLifecycleUsesV11ShapesAndDurableState(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	projectResponse := compatJSONRequest(t, app, http.MethodPost, "/api/projects", "local", map[string]any{
		"name": "Frame Session Oracle", "description": "No LLM lifecycle fixture",
		"context": map[string]any{"source": "test"}, "find_or_create": true,
	}, http.StatusOK)
	projectID, _ := projectResponse["project_id"].(string)
	if projectID == "" || projectResponse["name"] != "Frame Session Oracle" ||
		projectResponse["description"] != "No LLM lifecycle fixture" || numberValue(projectResponse["artifact_count"]) != 0 ||
		numberValue(projectResponse["conversation_count"]) != 0 {
		t.Fatalf("project response = %#v", projectResponse)
	}
	if contextData, ok := projectResponse["context"].(map[string]any); !ok || contextData["source"] != "test" {
		t.Fatalf("project context = %#v", projectResponse["context"])
	}

	frameResponse := compatJSONRequest(t, app, http.MethodPost, "/api/frames", "local", map[string]any{
		"project_id": projectID,
	}, http.StatusCreated)
	frameID, _ := frameResponse["root_frame_id"].(string)
	if frameID == "" || frameResponse["project_id"] != projectID {
		t.Fatalf("frame create response = %#v", frameResponse)
	}

	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames?project_id="+projectID+"&root_only=true&limit=10", "local", nil, http.StatusOK)
	if len(listed) != 1 {
		t.Fatalf("listed frames = %#v", listed)
	}
	assertV11EmptyFrameShape(t, listed[0], projectID, frameID, nil)

	shallow := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID+"?shallow=true", "local", nil, http.StatusOK)
	assertV11EmptyFrameShape(t, shallow, projectID, frameID, map[string]any{})

	messages := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID+"/messages?from=0&limit=10", "local", nil, http.StatusOK)
	if messages["frame_id"] != frameID || numberValue(messages["from"]) != 0 || numberValue(messages["total"]) != 0 || len(messages["messages"].([]any)) != 0 {
		t.Fatalf("empty messages = %#v", messages)
	}
	located := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID+"/messages/locate?uuid=oracle-absent-message", "local", nil, http.StatusOK)
	if located["frame_id"] != frameID || located["uuid"] != "oracle-absent-message" || located["idx"] != nil {
		t.Fatalf("absent locate = %#v", located)
	}

	updated := compatJSONRequest(t, app, http.MethodPatch, "/api/frames/"+frameID, "local", map[string]any{
		"name": "Renamed Oracle Frame", "task_summary": "Updated without an LLM",
	}, http.StatusOK)
	if updated["root_frame_id"] != frameID || updated["name"] != "Renamed Oracle Frame" || updated["task_summary"] != "Updated without an LLM" {
		t.Fatalf("updated frame = %#v", updated)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	app = New(Options{Workspace: reopened}).Handler()
	persisted := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID+"?shallow=true", "local", nil, http.StatusOK)
	if persisted["name"] != "Renamed Oracle Frame" || persisted["task_summary"] != "Updated without an LLM" {
		t.Fatalf("persisted frame metadata = %#v", persisted)
	}

	deleted := compatJSONRequest(t, app, http.MethodDelete, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if deleted["frame_id"] != frameID || deleted["root_frame_id"] != frameID || numberValue(deleted["frames_deleted"]) != 1 || numberValue(deleted["artifacts_deleted"]) != 0 {
		t.Fatalf("deleted frame = %#v", deleted)
	}
	missing := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusNotFound)
	if missing["detail"] != "Frame "+frameID+" not found" {
		t.Fatalf("missing frame = %#v", missing)
	}
}

func TestCompatibilityProjectResponseUsesNullForAbsentDescription(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store}).Handler()

	project := compatJSONRequest(t, app, http.MethodPost, "/api/projects", "local", map[string]any{
		"name": "Project Without Description", "find_or_create": true,
	}, http.StatusOK)
	if project["description"] != nil {
		t.Fatalf("description = %#v, want null for v1.1 compatibility", project["description"])
	}
}

func TestCompatibilityPendingRuntimeUsesAcceptedInputClock(t *testing.T) {
	observedAt := time.Date(2026, 8, 10, 12, 30, 0, 0, time.UTC)
	startedAt := observedAt.Add(-1500 * time.Millisecond)
	projection := compatibilityPendingFrameRuntimeProjection(transcriptstore.Stream{
		InputRevision: 9, UpdatedAt: startedAt,
	}, observedAt)
	if projection["runtime_elapsed_ms"] != int64(1500) || projection["runtime_input_revision"] != int64(9) || projection["runtime_stage"] != "starting" ||
		projection["runtime_active"] != true || projection["runtime_attempt"] != nil ||
		projection["runtime_started_at"] != startedAt || projection["runtime_finished_at"] != nil ||
		projection["runtime_observed_at"] != observedAt {
		t.Fatalf("pending runtime projection=%#v", projection)
	}
}

func TestCompatibilityPendingRuntimeUsesObservationForLegacyStreamWithoutTimestamp(t *testing.T) {
	observedAt := time.Date(2026, 8, 10, 12, 30, 0, 0, time.UTC)
	projection := compatibilityPendingFrameRuntimeProjection(transcriptstore.Stream{InputRevision: 9}, observedAt)
	if projection["runtime_active"] != true || projection["runtime_elapsed_ms"] != int64(0) ||
		projection["runtime_started_at"] != observedAt || projection["runtime_observed_at"] != observedAt {
		t.Fatalf("legacy pending runtime projection=%#v", projection)
	}
}

func TestCompatibilityTerminalFrameNeverProjectsPendingStart(t *testing.T) {
	for _, status := range []string{workspace.FrameStatusCompleted, workspace.FrameStatusFailed, workspace.FrameStatusCancelled, "canceled", " CANCELLED "} {
		if !compatibilityFrameStatusTerminal(status) {
			t.Fatalf("terminal frame status %q was not recognized", status)
		}
	}
	for _, status := range []string{workspace.FrameStatusProcessing, "running", "awaiting_user_response", ""} {
		if compatibilityFrameStatusTerminal(status) {
			t.Fatalf("active frame status %q was treated as terminal", status)
		}
	}
}

func TestCompatibilityOnlySuccessfulCompletionWaitsForFinalPresentation(t *testing.T) {
	if !compatibilityFrameNeedsFinalPresentation(workspace.FrameStatusCompleted, false) {
		t.Fatal("completed frame must remain finalizing until its final presentation is readable")
	}
	for _, status := range []string{workspace.FrameStatusFailed, workspace.FrameStatusCancelled, "canceled"} {
		if compatibilityFrameNeedsFinalPresentation(status, false) {
			t.Fatalf("terminal failure %q was falsely projected as finalizing", status)
		}
	}
	if compatibilityFrameNeedsFinalPresentation(workspace.FrameStatusCompleted, true) {
		t.Fatal("presentation-ready completion remained finalizing")
	}
}

func TestCompatibilityCompletedFrameWithoutRuntimeProjectionRemainsReadable(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-legacy-completed", UserID: "local", Name: "Legacy completed frame",
	}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-legacy-completed", ProjectID: "project-legacy-completed", AgentName: "OPERON",
		Status: workspace.FrameStatusCompleted, ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	compatibilityFrame, found, err := store.GetCompatibilityFrame(frame.ID)
	if err != nil || !found {
		t.Fatalf("compatibility frame found=%t err=%v", found, err)
	}

	serverApp := New(Options{Workspace: store})
	t.Cleanup(func() { _ = serverApp.Close(context.Background()) })
	projection, err := serverApp.compatibilityFrameResponse(compatibilityFrame, true, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if projection["status"] != workspace.FrameStatusCompleted {
		t.Fatalf("status=%#v, want completed when no transcript runtime exists", projection["status"])
	}
	if _, exists := projection["runtime_stage"]; exists {
		t.Fatalf("runtime_stage=%#v, want omitted without a transcript runtime", projection["runtime_stage"])
	}
}

func TestFrameCompatibilityProjectsDurableRuntimePresentation(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	projectID, frameID := "project-runtime", "frame-runtime"
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: projectID, UserID: "local", Name: "Runtime presentation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: "local", ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "runtime-task-first",
		FrameEventID: "runtime-task-first-frame", MessageUUID: "runtime-task-first-message", MessageOrigin: "task_intent",
		Text: "Collect the initial evidence.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	firstClaim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runtime-runner-first",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !firstClaim.Claimed {
		t.Fatalf("first claim=%#v err=%v", firstClaim, err)
	}
	if _, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: firstClaim.Claim, ClientMessageID: "runtime-first-finished", Status: "completed",
		PayloadJSON: []byte(`{"summary":"initial evidence complete"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish first created=%t err=%v", created, err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "runtime-task",
		FrameEventID: "runtime-task-frame", MessageUUID: "runtime-task-message", MessageOrigin: "task_intent",
		Text: "Compare the evidence routes.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append second task created=%t err=%v", created, err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runtime-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed || claim.Claim.Attempt <= firstClaim.Claim.Attempt {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	server := New(Options{Workspace: store, Transcript: repository, FileRoot: root})
	for index, audit := range []map[string]any{
		{
			"sessionId": frameID, "attempt": firstClaim.Claim.Attempt,
			"promptTokens": 800, "completionTokens": 200,
			"cacheReadTokens": 0, "cacheWriteTokens": 0, "totalTokens": 1_000,
		},
		{
			"sessionId": frameID, "attempt": claim.Claim.Attempt,
			"promptTokens": 12_000, "completionTokens": 3_000,
			"cacheReadTokens": 1_000, "cacheWriteTokens": 0, "totalTokens": 15_000,
		},
		{
			"sessionId": frameID, "attempt": claim.Claim.Attempt,
			"promptTokens": 400, "completionTokens": 100,
			"cacheReadTokens": 200, "cacheWriteTokens": 0, "totalTokens": 500,
		},
	} {
		if _, err := server.runtimeStore.Set(
			sessionRunnerModelAuditRuntimeNamespace, fmt.Sprintf("frame-runtime-audit-%d", index), audit,
		); err != nil {
			t.Fatal(err)
		}
	}
	app := server.Handler()

	storedOutput := map[string]any{
		"summary":                "preserve me",
		"pending_input_requests": []any{map[string]any{"requestId": "stale", "questions": []any{"stale secret"}}},
	}
	if err := store.SetFrameOutputData(frameID, storedOutput); err != nil {
		t.Fatal(err)
	}
	model, description := "claude-sonnet", "Waiting for operator input"
	if err := store.UpdateFrameRuntimePresentation(frameID, workspace.FrameRuntimePresentationInput{
		Model: &model, StatusDescription: &description,
	}); err != nil {
		t.Fatal(err)
	}
	questions := []any{map[string]any{
		"question": "Which evidence route?", "header": "Evidence",
		"options": []any{
			map[string]any{"label": "Direct", "description": "Use direct evidence."},
			map[string]any{"label": "Review", "description": "Review the alternatives first."},
		},
		"multiSelect": false,
	}}
	pauseInput := transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim.Claim, ClientMessageID: "runtime-pause", Phase: transcriptstore.RunnerPhaseWaitingUser,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_user_response"}`), Destinations: []string{"ws"},
	}
	parked, _, err := store.ParkAskUserWithTranscript(context.Background(), workspace.ParkAskUserInput{
		FrameID: frameID, ToolID: "ask-current", ToolName: "AskUserQuestion", Questions: questions,
	}, pauseInput)
	if err != nil || parked.AlreadyPending || len(parked.Events) != 3 {
		t.Fatalf("park ask user=%#v err=%v", parked, err)
	}
	repeated, _, err := store.ParkAskUserWithTranscript(context.Background(), workspace.ParkAskUserInput{
		FrameID: frameID, ToolID: "ask-current", ToolName: "AskUserQuestion", Questions: questions,
	}, pauseInput)
	if err != nil || !repeated.AlreadyPending || len(repeated.Events) != 3 {
		t.Fatalf("repeat park=%#v err=%v", repeated, err)
	}

	projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if projected["model"] != model || projected["status_description"] != description || projected["status"] != "awaiting_user_response" {
		t.Fatalf("runtime presentation = %#v", projected)
	}
	if projected["runtime_active"] != false || projected["runtime_task_active"] != false ||
		projected["runtime_attempt"] != float64(claim.Claim.Attempt) ||
		projected["runtime_input_revision"] != float64(claim.Claim.ClaimedInputRevision) || projected["runtime_started_at"] == nil ||
		projected["runtime_observed_at"] == nil {
		t.Fatalf("durable runtime timing = %#v", projected)
	}
	if elapsed, ok := projected["runtime_elapsed_ms"].(float64); !ok || elapsed < 0 {
		t.Fatalf("runtime elapsed = %#v", projected["runtime_elapsed_ms"])
	}
	if projected["runtime_input_tokens"] != float64(12_400) || projected["runtime_output_tokens"] != float64(3_100) ||
		projected["runtime_cache_read_tokens"] != float64(1_200) || projected["runtime_total_tokens"] != float64(15_500) ||
		projected["runtime_model_call_count"] != float64(2) || projected["runtime_tool_call_count"] != float64(0) {
		t.Fatalf("current task metrics = %#v", projected)
	}
	if projected["runtime_task_input_tokens"] != float64(13_200) ||
		projected["runtime_task_output_tokens"] != float64(3_300) ||
		projected["runtime_task_cache_read_tokens"] != float64(1_200) ||
		projected["runtime_task_total_tokens"] != float64(16_500) ||
		projected["runtime_task_model_call_count"] != float64(3) ||
		projected["runtime_task_tool_call_count"] != float64(0) ||
		projected["runtime_task_started_at"] == nil || projected["runtime_task_observed_at"] == nil {
		t.Fatalf("task total metrics = %#v", projected)
	}
	if elapsed, ok := projected["runtime_task_elapsed_ms"].(float64); !ok || elapsed < 0 {
		t.Fatalf("task total elapsed = %#v", projected["runtime_task_elapsed_ms"])
	}
	projectedOutput, ok := projected["output_data"].(map[string]any)
	if !ok || projectedOutput["summary"] != "preserve me" {
		t.Fatalf("runtime output = %#v", projected["output_data"])
	}
	wantPending := []any{map[string]any{
		"tool_id": "ask-current", "requestId": "ask-current", "kind": "ask",
		"tool_name": "ask_user", "questions": questions,
	}}
	if !reflect.DeepEqual(projectedOutput["pending_input_requests"], wantPending) {
		t.Fatalf("projected pending=%#v want=%#v", projectedOutput["pending_input_requests"], wantPending)
	}
	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames?project_id="+projectID, "local", nil, http.StatusOK)
	if len(listed) != 1 || !reflect.DeepEqual(listed[0]["output_data"], projectedOutput) {
		t.Fatalf("list/single projection mismatch: list=%#v single=%#v", listed, projectedOutput)
	}
	if _, projectedRuntime := listed[0]["runtime_elapsed_ms"]; projectedRuntime {
		t.Fatalf("frame collection must not perform per-frame runtime projection: %#v", listed[0])
	}
	stored, found, err := store.GetCompatibilityFrame(frameID)
	if err != nil || !found || !reflect.DeepEqual(stored.OutputData, storedOutput) {
		t.Fatalf("stored output mutated: found=%t output=%#v err=%v", found, stored.OutputData, err)
	}
	foreign := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "other", nil, http.StatusNotFound)
	foreignJSON, _ := json.Marshal(foreign)
	if bytes.Contains(foreignJSON, []byte("Which evidence route?")) {
		t.Fatalf("foreign response leaked pending input: %s", foreignJSON)
	}
	nilOutputFrameID := "frame-nil-output"
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: nilOutputFrameID, ProjectID: projectID, AgentName: "OPERON", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ParkAskUser(workspace.ParkAskUserInput{
		FrameID: nilOutputFrameID, ToolID: "ask-nil", ToolName: "AskUserQuestion", Questions: questions,
	}); err != nil {
		t.Fatal(err)
	}
	nilProjection := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+nilOutputFrameID, "local", nil, http.StatusOK)
	nilOutput, ok := nilProjection["output_data"].(map[string]any)
	if !ok || len(nilOutput) != 1 {
		t.Fatalf("nil stored output projection=%#v", nilProjection["output_data"])
	}
	wantNilPending := []any{map[string]any{
		"tool_id": "ask-nil", "requestId": "ask-nil", "kind": "ask",
		"tool_name": "ask_user", "questions": questions,
	}}
	if !reflect.DeepEqual(nilOutput["pending_input_requests"], wantNilPending) {
		t.Fatalf("nil stored output pending=%#v want=%#v", nilOutput["pending_input_requests"], wantNilPending)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedRepository, err := reopened.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app = New(Options{Workspace: reopened, Transcript: reopenedRepository, FileRoot: root}).Handler()
	restarted := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if !reflect.DeepEqual(restarted["output_data"], projectedOutput) {
		t.Fatalf("restart projection=%#v want=%#v", restarted["output_data"], projectedOutput)
	}
	resolved := compatJSONRequest(t, app, http.MethodPost, "/api/frames/"+frameID+"/resolve-input", "local", map[string]any{
		"responses": []any{map[string]any{
			"tool_id": "ask-current", "action": "answer", "answers": map[string]any{"Which evidence route?": "Direct"},
		}},
	}, http.StatusOK)
	if resolved["status"] != "accepted" || resolved["remaining"] != float64(0) {
		t.Fatalf("resolve response=%#v", resolved)
	}
	afterResolve := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	afterOutput, ok := afterResolve["output_data"].(map[string]any)
	if !ok || afterOutput["summary"] != "preserve me" {
		t.Fatalf("resolved output=%#v", afterResolve["output_data"])
	}
	if _, exists := afterOutput["pending_input_requests"]; exists {
		t.Fatalf("resolved output retained pending inputs: %#v", afterOutput)
	}
	stored, found, err = reopened.GetCompatibilityFrame(frameID)
	if err != nil || !found || !reflect.DeepEqual(stored.OutputData, storedOutput) {
		t.Fatalf("resolve mutated stored output: found=%t output=%#v err=%v", found, stored.OutputData, err)
	}
}

func TestFrameCompatibilityProjectionIgnoresLegacyPendingFallbacks(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	projectID := "project-canonical-pending"
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: projectID, UserID: "local", Name: "Canonical pending"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	cases := []struct {
		name      string
		canonical any
		present   bool
	}{
		{name: "absent"},
		{name: "empty", canonical: []any{}, present: true},
		{name: "non-map-items", canonical: []any{"invalid", float64(7)}, present: true},
	}
	for _, test := range cases {
		frameID := "frame-" + test.name
		if _, err := store.CreateFrame(workspace.CreateFrameInput{
			ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "running", ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
		storedOutput := map[string]any{
			"marker": test.name, "pending_input_requests": []any{map[string]any{"requestId": "stale-output"}},
		}
		if err := store.SetFrameOutputData(frameID, storedOutput); err != nil {
			t.Fatal(err)
		}
		contextData := map[string]any{
			"_ask_user_payload":       map[string]any{"requestId": "legacy-ask"},
			"_pending_access_request": map[string]any{"requestId": "legacy-access"},
		}
		if test.present {
			contextData["_pending_input_requests"] = test.canonical
		}
		if _, err := store.SetFrameRuntimeMetadata(frameID, workspace.FrameRuntimeMetadata{ContextData: contextData}); err != nil {
			t.Fatal(err)
		}
		projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
		output, ok := projected["output_data"].(map[string]any)
		if !ok || output["marker"] != test.name {
			t.Fatalf("%s output=%#v", test.name, projected["output_data"])
		}
		if _, exists := output["pending_input_requests"]; exists {
			t.Fatalf("%s projected legacy pending: %#v", test.name, output)
		}
		stored, found, err := store.GetCompatibilityFrame(frameID)
		if err != nil || !found || !reflect.DeepEqual(stored.OutputData, storedOutput) {
			t.Fatalf("%s stored output changed: found=%t output=%#v err=%v", test.name, found, stored.OutputData, err)
		}
	}
	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames?project_id="+projectID, "local", nil, http.StatusOK)
	if len(listed) != len(cases) {
		t.Fatalf("listed frames=%#v", listed)
	}
	for _, frame := range listed {
		output, ok := frame["output_data"].(map[string]any)
		if !ok || output["marker"] == nil {
			t.Fatalf("listed output=%#v", frame["output_data"])
		}
		if _, exists := output["pending_input_requests"]; exists {
			t.Fatalf("list projected legacy pending: %#v", output)
		}
	}
}

func TestFrameCompatibilityProjectsCurrentTaskPlanAuthorityIntoOutput(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-plan-projection", UserID: "local", Name: "Plan projection",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-plan-projection", ProjectID: "project-plan-projection", AgentName: "OPERON",
		Status: "awaiting_plan_approval", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame-plan-projection", workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{
			"_plan_artifact_id": "plan-artifact", "_plan_version_id": "plan-version",
			"_plan_json": map[string]any{"private": "must remain in context"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame-plan-projection", "local", nil, http.StatusOK)
	output, ok := projected["output_data"].(map[string]any)
	if !ok || output["plan_artifact_id"] != "plan-artifact" || output["plan_version_id"] != "plan-version" {
		t.Fatalf("pending plan projection=%#v", projected["output_data"])
	}
	if output["_plan_json"] != nil || output["private"] != nil {
		t.Fatalf("pending plan projection leaked private context: %#v", output)
	}

	processing := "processing"
	if _, err := store.UpdateFrame("frame-plan-projection", workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	resumed := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame-plan-projection", "local", nil, http.StatusOK)
	runningOutput, ok := resumed["output_data"].(map[string]any)
	if !ok || runningOutput["plan_artifact_id"] != "plan-artifact" || runningOutput["plan_version_id"] != "plan-version" {
		t.Fatalf("running task plan projection=%#v", resumed["output_data"])
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-plan-projection")
	if err != nil || !found {
		t.Fatalf("metadata found=%t err=%v", found, err)
	}
	metadata.ContextData["_pending_input_requests"] = []any{map[string]any{"requestId": "legacy-stale"}}
	if _, err := store.SetFrameRuntimeMetadata("frame-plan-projection", metadata); err != nil {
		t.Fatal(err)
	}
	cancelled := "cancelled"
	if _, err := store.UpdateFrame("frame-plan-projection", workspace.UpdateFrameInput{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	terminal := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame-plan-projection", "local", nil, http.StatusOK)
	terminalOutput, ok := terminal["output_data"].(map[string]any)
	if !ok || terminalOutput["plan_artifact_id"] != "plan-artifact" || terminalOutput["plan_version_id"] != "plan-version" {
		t.Fatalf("terminal task plan projection=%#v", terminal["output_data"])
	}
	if _, exists := terminalOutput["pending_input_requests"]; exists {
		t.Fatalf("terminal frame projected a stale pending card: %#v", terminalOutput)
	}
}

func TestFrameCompatibilityRecoversCurrentTaskPlanFromExactFrameArtifactBinding(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-plan-recovery", UserID: "local", Name: "Plan recovery",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-plan-recovery", ProjectID: "project-plan-recovery", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "plan-recovery", ProjectID: "project-plan-recovery", Name: "plan_recovery.json",
		ContentType: "application/json", Content: strings.NewReader(`{"title":"Recovered"}`),
		CreatedBy: "agent", RootFrameID: "frame-plan-recovery", FrameID: "frame-plan-recovery",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()
	projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame-plan-recovery", "local", nil, http.StatusOK)
	output, ok := projected["output_data"].(map[string]any)
	if !ok || output["plan_artifact_id"] != "plan-recovery" || output["plan_version_id"] != version.ID {
		t.Fatalf("recovered current plan projection=%#v", projected["output_data"])
	}
}

func TestFrameCompatibilityEnforcesOwnershipAndMissingProjectBehavior(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-a", UserID: "user-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-a", ProjectID: "project-a", AgentName: "OPERON", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	missingProject := compatJSONRequest(t, app, http.MethodPost, "/api/frames", "user-a", map[string]any{"project_id": "missing"}, http.StatusNotFound)
	if missingProject["detail"] != "Project missing not found" {
		t.Fatalf("missing project response = %#v", missingProject)
	}
	empty := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames?project_id=missing&root_only=true", "user-a", nil, http.StatusOK)
	if len(empty) != 0 {
		t.Fatalf("missing project frame list = %#v", empty)
	}
	foreign := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame-a", "user-b", nil, http.StatusNotFound)
	if foreign["detail"] != "Frame frame-a not found" {
		t.Fatalf("foreign frame response = %#v", foreign)
	}
}

func TestFrameCompatibilityMovesOwnedRootWithV11ResponseAndEvents(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "source", UserID: "local", Name: "Source"},
		{ID: "target", UserID: "local", Name: "Target"},
		{ID: "foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "source", AgentName: "OPERON", Status: "cancelled", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store})
	app := server.Handler()

	refs := compatJSONRequest(t, app, http.MethodGet, "/api/frames/root/cross-session-refs", "local", nil, http.StatusOK)
	if artifacts, ok := refs["artifacts"].([]any); !ok || len(artifacts) != 0 {
		t.Fatalf("empty cross-session references = %#v", refs)
	}
	foreign := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/move", "local", map[string]any{
		"target_project_id": "foreign",
	}, http.StatusNotFound)
	if foreign["detail"] != "Project foreign not found" {
		t.Fatalf("foreign target = %#v", foreign)
	}
	moved := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/move", "local", map[string]any{
		"target_project_id": "target",
	}, http.StatusOK)
	if moved["root_frame_id"] != "root" || moved["from_project_id"] != "source" || moved["to_project_id"] != "target" ||
		numberValue(moved["frames_moved"]) != 1 || numberValue(moved["artifacts_moved"]) != 0 {
		t.Fatalf("move response = %#v", moved)
	}
	idempotent := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/move", "local", map[string]any{
		"target_project_id": "target",
	}, http.StatusOK)
	if idempotent["from_project_id"] != "target" || numberValue(idempotent["frames_moved"]) != 0 || numberValue(idempotent["artifacts_moved"]) != 0 {
		t.Fatalf("idempotent move response = %#v", idempotent)
	}
	frame, found, err := store.GetFrame("root")
	if err != nil || !found || frame.ProjectID != "target" {
		t.Fatalf("moved frame found=%t frame=%#v err=%v", found, frame, err)
	}
	sourceEvents, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: "local", ProjectID: "source", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceEvents) == 0 {
		t.Fatal("source project did not receive a move invalidation event")
	}
}

func TestFrameCompatibilityDeletesCompleteDelegationTree(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrameRealtime(context.Background(), workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "running", ConversationType: "agent",
	}, "local", "realtime-root-created", "frame-event-root-created"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrameRealtime(context.Background(), workspace.CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: "root", AgentName: "RESEARCHER",
		Status: "completed", ConversationType: "delegate",
	}, "local", "realtime-child-created", "frame-event-child-created"); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	deleted := compatJSONRequest(t, server.Handler(), http.MethodDelete, "/api/frames/root", "local", nil, http.StatusOK)
	if numberValue(deleted["frames_deleted"]) != 2 {
		t.Fatalf("tree delete response = %#v", deleted)
	}
	for _, id := range []string{"root", "child"} {
		if _, found, err := store.GetFrame(id); err != nil || found {
			t.Fatalf("frame %s remains found=%v err=%v", id, found, err)
		}
	}
	if pending, err := store.CountUndeliveredRealtimeOutbox(context.Background()); err != nil || pending != 2 {
		t.Fatalf("pending tree delete outbox=%d err=%v", pending, err)
	}
	startServerRealtimeOutbox(t, store, server)
	waitServerRealtimeOutbox(t, store)
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: "local", ProjectID: "project", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	deletedFrames := map[string]bool{}
	for _, event := range events {
		if event.ID == "realtime-root-created" || event.ID == "realtime-child-created" {
			t.Fatalf("obsolete create event materialized: %#v", event)
		}
		if event.Type == "frame_update" && event.Payload["action"] == "deleted" {
			if frameID, _ := event.Payload["frame_id"].(string); frameID != "" {
				deletedFrames[frameID] = true
			}
		}
	}
	if !deletedFrames["root"] || !deletedFrames["child"] {
		t.Fatalf("tree deletion events=%#v", events)
	}
}

func TestFrameCompatibilityLocateSearchesBeyondTheMessagePageLimit(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "running", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= 500; index++ {
		messageID := "message-" + strconv.Itoa(index)
		if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: "frame", Type: "user_message",
			Payload: map[string]any{"uuid": messageID, "role": "user", "content": messageID},
		}); err != nil {
			t.Fatal(err)
		}
	}
	app := New(Options{Workspace: store}).Handler()
	located := compatJSONRequest(t, app, http.MethodGet, "/api/frames/frame/messages/locate?uuid=message-500", "local", nil, http.StatusOK)
	if numberValue(located["idx"]) != 500 {
		t.Fatalf("located long-session message = %#v", located)
	}
}

func TestFrameCompatibilityReadCursorCancelAndQueuedMessageControls(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "running", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "child", ProjectID: "project", ParentFrameID: "root", AgentName: "RESEARCHER", Status: "running", ConversationType: "delegate"}); err != nil {
		t.Fatal(err)
	}
	const queuedID = "00000000-0000-4000-8000-000000000002"
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: queuedID, FrameID: "root", Type: "queued_message",
		Payload: map[string]any{"messageUuid": queuedID, "role": "user", "content": "queued"},
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	initial := httptest.NewRecorder()
	app.ServeHTTP(initial, compatRequest(t, http.MethodGet, "/api/frames/root/read-cursor", "local", nil))
	if initial.Code != http.StatusOK || initial.Header().Get("Content-Type") != "application/json" || initial.Body.String() != "null\n" {
		t.Fatalf("initial read cursor = %d headers=%v body=%q", initial.Code, initial.Header(), initial.Body.String())
	}
	put := compatJSONRequest(t, app, http.MethodPut, "/api/frames/root/read-cursor", "local", map[string]any{
		"message_uuid": nil, "message_index": 0,
	}, http.StatusOK)
	if put["root_frame_id"] != "root" || put["message_uuid"] != nil || numberValue(put["message_index"]) != 0 || put["updated_at"] == nil {
		t.Fatalf("put read cursor = %#v", put)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app = New(Options{Workspace: store}).Handler()
	stored := compatJSONRequest(t, app, http.MethodGet, "/api/frames/root/read-cursor", "local", nil, http.StatusOK)
	if stored["root_frame_id"] != "root" || stored["message_uuid"] != nil || numberValue(stored["message_index"]) != 0 || stored["updated_at"] == nil {
		t.Fatalf("stored read cursor = %#v", stored)
	}
	compatJSONRequest(t, app, http.MethodPut, "/api/frames/root/read-cursor", "local", map[string]any{
		"message_uuid": queuedID, "message_index": 0,
	}, http.StatusOK)
	compatJSONRequest(t, app, http.MethodPut, "/api/frames/root/read-cursor", "local", map[string]any{
		"message_uuid": queuedID, "message_index": 0,
		"observed_message_uuid": queuedID, "repair": true,
	}, http.StatusConflict)
	if _, err := store.PutStableReadCursor("root", queuedID, 1, "", false); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, app, http.MethodPut, "/api/frames/root/read-cursor", "local", map[string]any{
		"message_uuid": queuedID, "message_index": 0,
		"observed_message_uuid": queuedID, "observed_message_index": 0, "repair": true,
	}, http.StatusConflict)

	cancelled := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/cancel?reason=test-stop", "local", nil, http.StatusOK)
	if cancelled["root_frame_id"] != "root" || !equalStringArray(cancelled["cancelled_frames"], []string{"root", "child"}) {
		t.Fatalf("cancel response = %#v", cancelled)
	}
	for _, frameID := range []string{"root", "child"} {
		frame, found, err := store.GetFrame(frameID)
		if err != nil || !found || frame.Status != "cancelled" {
			t.Fatalf("cancelled frame %s = %#v found=%v err=%v", frameID, frame, found, err)
		}
	}
	again := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/cancel?reason=test-stop-again", "local", nil, http.StatusOK)
	if !equalStringArray(again["cancelled_frames"], []string{"root", "child"}) {
		t.Fatalf("repeated cancel response = %#v", again)
	}
	for _, frameID := range []string{"root", "child"} {
		events, err := store.ListFrameEvents(frameID, 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		cancelEvents := 0
		for _, event := range events {
			if event.Type == "frame_cancelled" {
				cancelEvents++
			}
		}
		if cancelEvents != 1 {
			t.Fatalf("frame %s cancel events = %#v", frameID, events)
		}
	}

	missingID := "00000000-0000-4000-8000-000000000001"
	missing := compatJSONRequest(t, app, http.MethodDelete, "/api/frames/root/queued-messages/"+missingID, "local", nil, http.StatusNotFound)
	if missing["detail"] != "Queued message "+missingID+" not found on frame root" {
		t.Fatalf("missing queued message = %#v", missing)
	}
	retracted := compatJSONRequest(t, app, http.MethodDelete, "/api/frames/root/queued-messages/"+queuedID, "local", nil, http.StatusOK)
	if len(retracted) != 3 || retracted["frame_id"] != "root" || retracted["id"] != queuedID || retracted["removed"] != true {
		t.Fatalf("retracted queued message = %#v", retracted)
	}
}

func TestFrameCompatibilityResumeIsOwnershipSafeDurableAndIdempotent(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "cancelled", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: "root", AgentName: "RESEARCHER", Status: "cancelled", ConversationType: "delegate",
	}); err != nil {
		t.Fatal(err)
	}
	app := newV11TestServer(t, Options{FileRoot: runtimeRoot, Workspace: store}).Handler()

	foreign := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/resume", "foreign-user", map[string]any{}, http.StatusNotFound)
	if foreign["detail"] != "Frame root not found" {
		t.Fatalf("foreign resume = %#v", foreign)
	}
	resumed := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/resume", "local", map[string]any{
		"verifier_mode": "on", "memory_mode": "off", "plan_mode": true,
		"ultra_mode": false, "target_agent": "REVIEWER", "model": "test-model",
	}, http.StatusOK)
	assertCompatibilityResumeResponse(t, resumed, "root", "root", "REVIEWER", 1)

	root, found, err := store.GetFrame("root")
	if err != nil || !found || root.Status != "processing" || root.AgentName != "REVIEWER" {
		t.Fatalf("resumed root = %#v found=%v err=%v", root, found, err)
	}
	child, found, err := store.GetFrame("child")
	if err != nil || !found || child.Status != "cancelled" {
		t.Fatalf("non-selected child = %#v found=%v err=%v", child, found, err)
	}
	assertSingleFrameEvent(t, store, "root", "frame_resumed")

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app = newV11TestServer(t, Options{FileRoot: runtimeRoot, Workspace: store}).Handler()
	again := compatJSONRequest(t, app, http.MethodPost, "/api/frames/root/resume", "local", map[string]any{}, http.StatusOK)
	assertCompatibilityResumeResponse(t, again, "root", "root", "REVIEWER", 1)
	assertSingleFrameEvent(t, store, "root", "frame_resumed")
}

func TestFrameCompatibilityQueuedMessageRetractionUsesV11ShapeAndIsExactlyOnce(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	const queuedID = "00000000-0000-4000-8000-000000000002"
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: queuedID, FrameID: "root", Type: "queued_message",
		Payload: map[string]any{"messageUuid": queuedID, "role": "user", "content": "queued"},
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	retracted := compatJSONRequest(t, app, http.MethodDelete, "/api/frames/root/queued-messages/"+queuedID, "local", nil, http.StatusOK)
	if len(retracted) != 3 || retracted["frame_id"] != "root" || retracted["id"] != queuedID || retracted["removed"] != true {
		t.Fatalf("retracted queued message = %#v", retracted)
	}
	events, err := store.ListFrameEvents("root", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	queuedEvents, retractionEvents := 0, 0
	for _, event := range events {
		switch event.Type {
		case "queued_message":
			queuedEvents++
		case "message_retracted":
			retractionEvents++
			if event.Payload["messageUuid"] != queuedID {
				t.Fatalf("retraction payload = %#v", event.Payload)
			}
		}
	}
	if queuedEvents != 0 || retractionEvents != 1 {
		t.Fatalf("queued=%d retracted=%d events=%#v", queuedEvents, retractionEvents, events)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app = New(Options{Workspace: store}).Handler()
	repeated := compatJSONRequest(t, app, http.MethodDelete, "/api/frames/root/queued-messages/"+queuedID, "local", nil, http.StatusNotFound)
	if repeated["detail"] != "Queued message "+queuedID+" not found on frame root" {
		t.Fatalf("repeated retraction = %#v", repeated)
	}
	assertSingleFrameEvent(t, store, "root", "message_retracted")
}

func assertCompatibilityResumeResponse(t *testing.T, response map[string]any, rootID, frameID, agentName string, leaves int) {
	t.Helper()
	if response["root_frame_id"] != rootID || numberValue(response["leaf_frames_ready"]) != int64(leaves) {
		t.Fatalf("resume response = %#v", response)
	}
	frames, ok := response["resumed_frames"].([]any)
	if !ok || len(frames) != 1 {
		t.Fatalf("resume frames = %#v", response["resumed_frames"])
	}
	frame, ok := frames[0].(map[string]any)
	if !ok || frame["frame_id"] != frameID || frame["agent_name"] != agentName {
		t.Fatalf("resumed frame = %#v", frames[0])
	}
}

func assertSingleFrameEvent(t *testing.T, store *workspace.Store, frameID, eventType string) {
	t.Helper()
	events, err := store.ListFrameEvents(frameID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("frame %s event %s count = %d: %#v", frameID, eventType, count, events)
	}
}

func equalStringArray(raw any, want []string) bool {
	values, ok := raw.([]any)
	if !ok || len(values) != len(want) {
		return false
	}
	for index, value := range values {
		if value != want[index] {
			return false
		}
	}
	return true
}

func assertV11EmptyFrameShape(t *testing.T, frame map[string]any, projectID, frameID string, wantInputData any) {
	t.Helper()
	if frame["id"] != frameID || frame["project_id"] != projectID || frame["root_frame_id"] != frameID ||
		frame["agent_name"] != "OPERON" || frame["status"] != "completed" || frame["conversation_type"] != "agent" ||
		frame["parent_frame_id"] != nil || frame["name"] != nil || frame["children"] == nil || len(frame["children"].([]any)) != 0 ||
		numberValue(frame["context_limit"]) != defaultRunnerContextWindow || numberValue(frame["compaction_count"]) != 0 || frame["is_hidden"] != false {
		t.Fatalf("v1.1 empty frame shape = %#v", frame)
	}
	if wantInputData == nil {
		if frame["input_data"] != nil {
			t.Fatalf("list input_data = %#v, want nil", frame["input_data"])
		}
	} else if input, ok := frame["input_data"].(map[string]any); !ok || len(input) != 0 {
		t.Fatalf("shallow input_data = %#v, want empty object", frame["input_data"])
	}
	for _, key := range []string{"activity_counts", "aux_cost", "cache_read_tokens", "cache_write_tokens", "completed_at", "context_data", "context_usage_percent", "context_used", "delegate_name", "effort", "input_tokens", "message_count", "model", "output_data", "output_tokens", "specialists_used", "status_description", "task_summary", "total_cost"} {
		if frame[key] != nil {
			t.Fatalf("frame[%s] = %#v, want nil", key, frame[key])
		}
	}
}

func compatJSONRequest(t *testing.T, handler http.Handler, method, target, userID string, body any, wantStatus int) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	request := compatRequest(t, method, target, userID, body)
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d: %s", method, target, response.Code, wantStatus, response.Body.String())
	}
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s %s response: %v: %s", method, target, err, response.Body.String())
	}
	return decoded
}

func compatJSONArrayRequest(t *testing.T, handler http.Handler, method, target, userID string, body any, wantStatus int) []map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, compatRequest(t, method, target, userID, body))
	if response.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d: %s", method, target, response.Code, wantStatus, response.Body.String())
	}
	var decoded []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s %s response: %v: %s", method, target, err, response.Body.String())
	}
	return decoded
}

func compatRequest(t *testing.T, method, target, userID string, body any) *http.Request {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := newLoopbackTestRequest(method, target, bytes.NewReader(encoded))
	request.Header.Set("X-Synon-User-Id", userID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestFrameCompatibilityProjectsNonResumableProviderInterruptionAsStalled(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	projectID, frameID := "project-provider-paused", "frame-provider-paused"
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: projectID, UserID: "local", Name: "Provider paused"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: "local", ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "provider-paused-input",
		FrameEventID: "provider-paused-frame", MessageUUID: "provider-paused-message", MessageOrigin: "task_intent",
		Text: "Evaluate the provider interruption recovery path.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "provider-paused-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	interrupted, err := repository.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claim.Claim, ClientMessageID: "provider-paused-interruption",
		ReasonCode:   "model_provider_unavailable",
		ResumeDetail: "provider quota is exhausted; choose another model and continue",
		Resumable:    true, AutoResume: false, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	app := New(Options{Workspace: store, Transcript: repository, FileRoot: root}).Handler()
	projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if projected["status"] != "paused" || projected["runtime_active"] != false || projected["runtime_paused"] != true {
		t.Fatalf("model-selection wait projection=%#v", projected)
	}
	if _, failed := projected["runtime_failure_reason"]; failed {
		t.Fatalf("model-selection wait became a failure=%#v", projected)
	}
	if projected["runtime_interruption_reason"] != "model_provider_unavailable" ||
		projected["runtime_interruption_detail"] != "provider quota is exhausted; choose another model and continue" ||
		projected["status_description"] != "provider quota is exhausted; choose another model and continue" {
		t.Fatalf("interruption detail projection=%#v", projected)
	}
	if elapsed, ok := projected["runtime_elapsed_ms"].(float64); !ok || elapsed < 0 {
		t.Fatalf("stalled elapsed=%#v", projected["runtime_elapsed_ms"])
	}
}

func TestFrameCompatibilityKeepsFreshRunnerActiveAcrossKernelRecoveryHandoff(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	projectID, frameID := "project-kernel-handoff", "frame-kernel-handoff"
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: projectID, UserID: "local", Name: "Kernel handoff"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: "local", ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "kernel-handoff-input",
		FrameEventID: "kernel-handoff-frame", MessageUUID: "kernel-handoff-message", MessageOrigin: "task_intent",
		Text: "Continue the long analysis.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append task created=%t err=%v", created, err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-handoff-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	interrupted, err := repository.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claim.Claim, ClientMessageID: "kernel-handoff-interruption",
		ReasonCode: "kernel_operation_pending_recovery", ResumeDetail: "recover the exact operation",
		AutoResume: true, Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := store.CreateAutoResumeDispatch(
		frameID, frameID, projectID, "OPERON", "kernel_operation_pending_recovery",
	)
	if err != nil || resumed.Event == nil {
		t.Fatalf("create recovery dispatch=%#v err=%v", resumed, err)
	}
	dispatch, dispatchClaimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("kernel-handoff-dispatch", time.Minute)
	if err != nil || !dispatchClaimed || dispatch.ResumeEvent.ID != resumed.Event.ID {
		t.Fatalf("claim recovery dispatch=%#v claimed=%t err=%v", dispatch, dispatchClaimed, err)
	}
	app := New(Options{Workspace: store, Transcript: repository, FileRoot: root}).Handler()
	projected := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if projected["status"] != "processing" || projected["runtime_failure_reason"] != nil {
		t.Fatalf("claimed kernel handoff projection=%#v", projected)
	}
	recovered, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-handoff-recovered-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !recovered.Claimed || recovered.Claim.Attempt != claim.Claim.Attempt {
		t.Fatalf("recovered claim=%#v err=%v", recovered, err)
	}
	projected = compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID, "local", nil, http.StatusOK)
	if projected["status"] != "processing" || projected["runtime_active"] != true || projected["runtime_failure_reason"] != nil {
		t.Fatalf("kernel recovery handoff projection=%#v", projected)
	}
}
