package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/persistence/workspace"
)

const (
	transcriptWebServingOwnerID   = "local"
	transcriptWebServingProjectID = "project-read-model-serving"
	transcriptWebServingFrameID   = "frame-read-model-serving"
	transcriptWebServingStreamUID = "stream-read-model-serving"
)

type transcriptWebReadModelServingFixture struct {
	store      *workspace.Store
	repository *transcriptstore.Repository
	db         *sql.DB
	readModel  *transcriptstore.WebReadModelRepository
	server     *Server
	stream     transcriptstore.Stream
	fence      transcriptstore.TranscriptWebProjectionFence
}

func TestTranscriptWebFenceRejectsStaleProjectorVersion(t *testing.T) {
	fence := transcriptstore.TranscriptWebProjectionFence{
		StateFound: true, StateStatus: "ready",
		StateProjectorVersion:           transcriptstore.TranscriptWebProjectorVersion - 1,
		StateBranchGeneration:           3,
		BranchGeneration:                3,
		StateThroughPublicationSequence: 41,
		ThroughPublicationSequence:      41,
		StateSourceRevision:             7,
		SourceRevision:                  7,
	}
	if transcriptWebFenceReady(fence) {
		t.Fatal("stale projector version was accepted as a ready read model")
	}
	if !transcriptWebFenceCanCatchUp(fence) {
		t.Fatal("stale projector version was not eligible for request-scoped catch-up")
	}
	fence.StateProjectorVersion = transcriptstore.TranscriptWebProjectorVersion
	if !transcriptWebFenceReady(fence) {
		t.Fatal("current projector version was not accepted as ready")
	}
}

func TestTranscriptWebReadModelServingReadyPagesAndCompatibility(t *testing.T) {
	fixture := newTranscriptWebReadModelServingFixture(t, 405)

	tail := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages", nil, "")
	tailPage := requireTranscriptWebServingPage(t, tail, 200, 205, 404)
	if tailPage["has_more_before"] != true || tailPage["has_more_after"] != false {
		t.Fatalf("tail pagination=%#v", tailPage)
	}
	if timing := tail.Header().Get("Server-Timing"); !strings.HasPrefix(timing, "history_read_model;dur=") {
		t.Fatalf("tail server-timing=%q", timing)
	}

	oldest := webString(tailPage["oldest_cursor"])
	before := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages?before="+url.QueryEscape(oldest), nil, "")
	beforePage := requireTranscriptWebServingPage(t, before, 200, 5, 204)
	if beforePage["has_more_before"] != true || beforePage["has_more_after"] != true {
		t.Fatalf("before pagination=%#v", beforePage)
	}
	stableBefore := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages?before="+
			url.QueryEscape(transcriptWebServingMessageID(205)), nil, "")
	requireTranscriptWebServingPage(t, stableBefore, 200, 5, 204)

	previousNewest := webString(beforePage["newest_cursor"])
	after := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages?after="+url.QueryEscape(previousNewest), nil, "")
	afterPage := requireTranscriptWebServingPage(t, after, 200, 205, 404)
	if afterPage["has_more_before"] != true || afterPage["has_more_after"] != false {
		t.Fatalf("after pagination=%#v", afterPage)
	}
	stableAfter := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages?after="+
			url.QueryEscape(transcriptWebServingMessageID(204)), nil, "")
	requireTranscriptWebServingPage(t, stableAfter, 200, 205, 404)

	anchor := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages?anchor_message_id="+
			url.QueryEscape(transcriptWebServingMessageID(200)), nil, "")
	anchorPage := requireTranscriptWebServingPage(t, anchor, 200, 100, 299)
	anchorItems := transcriptWebServingItems(t, anchorPage, "items")
	if webString(anchorItems[100].(map[string]any)["id"]) != transcriptWebServingMessageID(200) {
		t.Fatalf("anchor item=%#v", anchorItems[100])
	}

	singleID := transcriptWebServingMessageID(123)
	single := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages/"+url.PathEscape(singleID), nil, "")
	if single.Code != http.StatusOK || webString(p3DecodeObject(t, single)["id"]) != singleID {
		t.Fatalf("single status=%d body=%s", single.Code, single.Body.String())
	}

	compatList := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/frames/"+transcriptWebServingFrameID+"/messages?from=200&limit=3", nil, "")
	compatPage := p3DecodeObject(t, compatList)
	compatMessages := transcriptWebServingItems(t, compatPage, "messages")
	if compatList.Code != http.StatusOK || servingJSONInt(compatPage["from"]) != 200 ||
		servingJSONInt(compatPage["total"]) != 405 || len(compatMessages) != 3 ||
		webString(compatMessages[0].(map[string]any)["uuid"]) != transcriptWebServingMessageID(200) ||
		webString(compatMessages[2].(map[string]any)["uuid"]) != transcriptWebServingMessageID(202) {
		t.Fatalf("compat list status=%d page=%#v", compatList.Code, compatPage)
	}
	compatLocate := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/frames/"+transcriptWebServingFrameID+"/messages/locate?uuid="+
			url.QueryEscape(transcriptWebServingMessageID(200)), nil, "")
	compatLocation := p3DecodeObject(t, compatLocate)
	if compatLocate.Code != http.StatusOK || servingJSONInt(compatLocation["idx"]) != 200 {
		t.Fatalf("compat locate status=%d payload=%#v", compatLocate.Code, compatLocation)
	}
	compatBranch := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/frames/"+transcriptWebServingFrameID+"/branches/"+
			url.PathEscape(fixture.fence.BranchID)+"/messages?from=200&limit=3", nil, "")
	compatBranchPage := p3DecodeObject(t, compatBranch)
	compatBranchMessages := transcriptWebServingItems(t, compatBranchPage, "messages")
	if compatBranch.Code != http.StatusOK ||
		webString(compatBranchPage["branch_id"]) != fixture.fence.BranchID ||
		servingJSONInt(compatBranchPage["from"]) != 200 ||
		servingJSONInt(compatBranchPage["total"]) != 405 || len(compatBranchMessages) != 3 ||
		webString(compatBranchMessages[0].(map[string]any)["uuid"]) != transcriptWebServingMessageID(200) ||
		webString(compatBranchMessages[2].(map[string]any)["uuid"]) != transcriptWebServingMessageID(202) {
		t.Fatalf("compat branch status=%d page=%#v", compatBranch.Code, compatBranchPage)
	}
}

func TestTranscriptWebReadModelDeclinesARequestedNonActiveBranch(t *testing.T) {
	fixture := newTranscriptWebReadModelServingFixture(t, 3)
	baseBranchID := fixture.fence.BranchID
	fork, err := fixture.repository.ForkFrameUserMessageAtIndex(
		context.Background(), transcriptstore.ForkFrameUserMessageAtIndexInput{
			StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
			SourceBranchID: baseBranchID, ExpectedActiveBranchID: baseBranchID,
			ExpectedGeneration: fixture.fence.BranchGeneration,
			ClientMutationID:   "read-model-non-active-branch",
			SourceMessageIndex: 0, ReplacementText: "branched request", Destinations: []string{"ws"},
		},
	)
	if err != nil || !fork.Created || fork.BranchID == baseBranchID {
		t.Fatalf("fork non-active branch result=%#v err=%v", fork, err)
	}
	if err := fixture.server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, active, err := fixture.server.activatedTranscriptWebReadModel(
		context.Background(), transcriptWebServingOwnerID, transcriptWebServingFrameID, baseBranchID,
	)
	if err != nil || active {
		t.Fatalf("non-active branch cache active=%t err=%v", active, err)
	}
}

func TestCompatibilityFrameSnapshotSurvivesCanonicalMessageProjectionLag(t *testing.T) {
	fixture := newTranscriptWebReadModelServingFixture(t, 1)
	frame, found, err := fixture.store.GetCompatibilityFrame(transcriptWebServingFrameID)
	if err != nil || !found {
		t.Fatalf("load compatibility frame found=%t err=%v", found, err)
	}

	if _, _, created, err := fixture.repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
			ClientMessageID: "read-model-client-lagging",
			FrameEventID:    "read-model-frame-event-lagging",
			MessageUUID:     "read-model-message-lagging",
			Text:            "message awaiting projection",
		},
	); err != nil || !created {
		t.Fatalf("append lagging message created=%t err=%v", created, err)
	}

	projection, err := fixture.server.compatibilityFrameResponse(frame, true, map[string]bool{})
	if err != nil {
		t.Fatalf("frame snapshot must remain available while message projection lags: %v", err)
	}
	if count := projection["message_count"]; count != nil && servingJSONInt(count) != 2 {
		t.Fatalf("canonical message_count=%#v, want omitted while lagging or current count 2 after bounded catch-up", count)
	}

	if err := fixture.server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	projection, err = fixture.server.compatibilityFrameResponse(frame, true, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if servingJSONInt(projection["message_count"]) != 2 {
		t.Fatalf("projected message_count=%#v, want 2", projection["message_count"])
	}
}

func TestTranscriptWebReadModelServingTranslatesStableLegacyCursor(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	const (
		projectID = "project-read-model-legacy-cursor"
		frameID   = "frame-read-model-legacy-cursor"
		streamUID = "frame:frame-read-model-legacy-cursor"
	)
	seedTranscriptWebFrame(t, store, transcriptWebServingOwnerID, projectID, frameID)
	ctx := context.Background()
	source, err := repository.CreateStream(ctx, transcriptstore.CreateStreamInput{
		UID: streamUID, OwnerID: transcriptWebServingOwnerID, ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID,
		FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	forceLegacyTranscriptFrameAuthority(t, db, source)
	if _, err := db.Exec(`UPDATE frames SET status='completed' WHERE id=?`, frameID); err != nil {
		t.Fatal(err)
	}
	frameEventIDs := make([]string, 0, 3)
	for index := 0; index < 3; index++ {
		event, err := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: frameID, Type: "user_message",
			Payload: map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type": "text", "text": fmt.Sprintf("legacy message %d", index),
				}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		frameEventIDs = append(frameEventIDs, event.ID)
	}
	if err := repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		_, appendErr := tx.AppendHistoricalFrameReferences(
			ctx, source.UID, source.OwnerID, frameEventIDs,
		)
		return appendErr
	}); err != nil {
		t.Fatal(err)
	}
	report, err := repository.ReconcileLegacyFrameHistories(
		ctx, transcriptstore.DefaultLegacyFrameHistoryReconciliationInput(),
	)
	if err != nil || report.Activated != 1 {
		t.Fatalf("legacy reconcile report=%#v err=%v", report, err)
	}

	type cursorMapRow struct {
		sourceBranchID, targetBranchID, stableMessageID string
		sourceGeneration, sourceThrough                 int64
		sourceIndex, targetIndex                        int
	}
	rows, err := db.Query(`SELECT source_branch_id,source_generation,
		source_through_publication_seq,source_message_index,target_branch_id,
		target_message_index,stable_message_id
		FROM transcript_history_ordinary_cursor_map ORDER BY source_message_index`)
	if err != nil {
		t.Fatal(err)
	}
	mappings := make([]cursorMapRow, 0, 3)
	for rows.Next() {
		var mapping cursorMapRow
		if err := rows.Scan(
			&mapping.sourceBranchID, &mapping.sourceGeneration, &mapping.sourceThrough,
			&mapping.sourceIndex, &mapping.targetBranchID, &mapping.targetIndex,
			&mapping.stableMessageID,
		); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 3 {
		t.Fatalf("legacy cursor mappings=%#v", mappings)
	}
	active, found, err := repository.GetFrameStreamBySession(ctx, source.OwnerID, source.SessionID)
	if err != nil || !found || active.UID == source.UID {
		t.Fatalf("active stream=%#v found=%t source=%#v err=%v", active, found, source, err)
	}
	if _, _, created, err := repository.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{
		StreamUID: active.UID, OwnerID: active.OwnerID,
		ClientMessageID: "post-activation-client", FrameEventID: "post-activation-frame-event",
		MessageUUID: "post-activation-message", Text: "new active history",
	}); err != nil || !created {
		t.Fatalf("append post-activation message created=%t err=%v", created, err)
	}
	server, _, fence := newTranscriptWebReadModelServingServer(t, store, repository, db, active)

	selected := mappings[1]
	if selected.targetBranchID != fence.BranchID || selected.targetIndex <= 0 ||
		selected.targetIndex >= len(mappings)-1 ||
		(selected.sourceBranchID == fence.BranchID &&
			selected.sourceGeneration == fence.BranchGeneration &&
			selected.sourceThrough == fence.ThroughPublicationSequence) {
		t.Fatalf("legacy mapping did not exercise translation mapping=%#v fence=%#v", selected, fence)
	}
	byTargetIndex := make(map[int]string, len(mappings))
	for _, mapping := range mappings {
		byTargetIndex[mapping.targetIndex] = mapping.stableMessageID
	}
	legacyCursor := encodeTranscriptBranchMessageCursor(transcriptstore.ProjectionSnapshot{
		BranchID: selected.sourceBranchID, BranchGeneration: selected.sourceGeneration,
		ThroughPublicationSequence: selected.sourceThrough,
	}, selected.sourceIndex)

	before := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/"+frameID+"/messages?limit=1&before="+url.QueryEscape(legacyCursor), nil, "")
	requireTranscriptWebServingPageIDs(t, before, byTargetIndex[selected.targetIndex-1])
	after := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/"+frameID+"/messages?limit=1&after="+url.QueryEscape(legacyCursor), nil, "")
	requireTranscriptWebServingPageIDs(t, after, byTargetIndex[selected.targetIndex+1])
}

func TestTranscriptWebReadModelServingReattachesDynamicArtifactReferences(t *testing.T) {
	store, repository, db := newTranscriptWebFixture(t)
	const (
		projectID = "project-read-model-artifact"
		frameID   = "frame-read-model-artifact"
		streamUID = "stream-read-model-artifact"
	)
	seedTranscriptWebFrame(t, store, transcriptWebServingOwnerID, projectID, frameID)
	artifact := seedTranscriptWebReadModelArtifact(
		t, store, db, projectID, frameID, "artifact-read-model-serving",
		"evidence.txt", []byte("durable evidence"), "user",
	)
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: streamUID, OwnerID: transcriptWebServingOwnerID, ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: projectID, RootFrameID: frameID,
		FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	messageID := "read-model-artifact-message"
	artifactEvent, _, created, err := repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "read-model-artifact-client", FrameEventID: "read-model-artifact-event",
			MessageUUID: messageID, Text: "inspect the evidence",
			ArtifactReferences: []transcriptstore.UserArtifactReferenceInput{{
				ArtifactID: artifact.artifactID, VersionID: artifact.versionID,
			}},
		},
	)
	if err != nil || !created {
		t.Fatalf("append artifact message created=%t err=%v", created, err)
	}
	server, _, fence := newTranscriptWebReadModelServingServer(t, store, repository, db, stream)

	var storedRaw []byte
	if err := db.QueryRow(`SELECT message_json FROM transcript_web_messages
		WHERE stream_uid=? AND branch_id=? AND message_id=?`, stream.UID, fence.BranchID, messageID).
		Scan(&storedRaw); err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(storedRaw, &stored); err != nil {
		t.Fatal(err)
	}
	if _, retained := stored["artifact_refs"]; retained {
		t.Fatalf("stored message retained dynamic artifact refs: %#v", stored)
	}

	list := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/"+frameID+"/messages?limit=1", nil, "")
	page := p3DecodeObject(t, list)
	items := transcriptWebServingItems(t, page, "items")
	if list.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("artifact list status=%d page=%#v", list.Code, page)
	}
	assertTranscriptWebServingArtifactReference(
		t, items[0].(map[string]any), artifact, artifactEvent.EventID,
		transcriptstore.ArtifactAvailable,
	)

	before := readTranscriptWebProjectionClock(t, db, stream.UID, fence.BranchID)
	deleted, err := store.DeleteCompatibilityArtifactRealtime(
		context.Background(), stream.OwnerID, artifact.artifactID,
	)
	if err != nil || deleted.VersionsDeleted != 1 {
		t.Fatalf("delete artifact result=%#v err=%v", deleted, err)
	}
	if err := store.RemoveArtifactBlobs(deleted.BlobPaths); err != nil {
		t.Fatal(err)
	}
	single := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/"+frameID+"/messages/"+url.PathEscape(messageID), nil, "")
	if single.Code != http.StatusOK {
		t.Fatalf("artifact single status=%d body=%s", single.Code, single.Body.String())
	}
	assertTranscriptWebServingArtifactReference(
		t, p3DecodeObject(t, single), artifact, artifactEvent.EventID,
		transcriptstore.ArtifactDeleted,
	)
	if after := readTranscriptWebProjectionClock(t, db, stream.UID, fence.BranchID); after != before {
		t.Fatalf("dynamic artifact read rebuilt projection before=%#v after=%#v", before, after)
	}
}

func TestTranscriptWebReadModelServingBuildsMissingProjectionOnDemand(t *testing.T) {
	fixture := newTranscriptWebReadModelServingFixture(t, 1)
	if _, err := fixture.db.Exec(`DELETE FROM transcript_web_projection_state
		WHERE stream_uid=? AND branch_id=?`, fixture.stream.UID, fixture.fence.BranchID); err != nil {
		t.Fatal(err)
	}

	response := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages?limit=1", nil, "")
	page := p3DecodeObject(t, response)
	items := transcriptWebServingItems(t, page, "items")
	if response.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("missing projection catch-up status=%d page=%#v", response.Code, page)
	}
	fence, err := fixture.readModel.GetTranscriptWebProjectionFence(
		context.Background(), fixture.stream.OwnerID, fixture.stream.UID, fixture.fence.BranchID,
	)
	if err != nil || !transcriptWebFenceReady(fence) {
		t.Fatalf("missing projection was not rebuilt fence=%#v err=%v", fence, err)
	}
}

func TestTranscriptWebReadModelServingFailsClosedForUnsafeStates(t *testing.T) {
	for _, state := range []string{"building", "quarantined-empty"} {
		t.Run(state, func(t *testing.T) {
			fixture := newTranscriptWebReadModelServingFixture(t, 1)
			switch state {
			case "building":
				if _, err := fixture.db.Exec(`UPDATE transcript_web_projection_state
					SET status='building',last_error_code=''
					WHERE stream_uid=? AND branch_id=?`, fixture.stream.UID, fixture.fence.BranchID); err != nil {
					t.Fatal(err)
				}
			case "quarantined-empty":
				if _, err := fixture.db.Exec(`UPDATE transcript_web_projection_state
					SET status='quarantined',last_error_code='projection_source_conflict',
						message_count=0,visible_message_count=0,message_artifact_reference_count=0
					WHERE stream_uid=? AND branch_id=?`, fixture.stream.UID, fixture.fence.BranchID); err != nil {
					t.Fatal(err)
				}
			}
			before := readTranscriptWebProjectionClock(
				t, fixture.db, fixture.stream.UID, fixture.fence.BranchID,
			)
			messageID := transcriptWebServingMessageID(0)
			for _, endpoint := range []struct {
				path, field string
			}{
				{path: "/api/conversations/" + transcriptWebServingFrameID + "/messages", field: "message"},
				{path: "/api/conversations/" + transcriptWebServingFrameID + "/messages/" + url.PathEscape(messageID), field: "message"},
				{path: "/api/frames/" + transcriptWebServingFrameID + "/messages?from=0&limit=1", field: "detail"},
				{path: "/api/frames/" + transcriptWebServingFrameID + "/messages/locate?uuid=" + url.QueryEscape(messageID), field: "detail"},
			} {
				response := p3JSONRequest(t, fixture.server, http.MethodGet, endpoint.path, nil, "")
				assertTranscriptWebServingUnavailable(t, response, endpoint.field)
			}
			if after := readTranscriptWebProjectionClock(
				t, fixture.db, fixture.stream.UID, fixture.fence.BranchID,
			); after != before {
				t.Fatalf("%s request rebuilt projection before=%#v after=%#v", state, before, after)
			}
		})
	}
}

func TestTranscriptWebReadModelServingUsesLastVerifiedSnapshotForQuarantine(t *testing.T) {
	fixture := newTranscriptWebReadModelServingFixture(t, 3)
	if _, err := fixture.db.Exec(`UPDATE transcript_web_projection_state
		SET status='quarantined',last_error_code='projection_source_conflict'
		WHERE stream_uid=? AND branch_id=?`, fixture.stream.UID, fixture.fence.BranchID); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := fixture.repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
			ClientMessageID: "terminal-quarantine-late-client", FrameEventID: "terminal-quarantine-late-event",
			MessageUUID: "terminal-quarantine-late-message", Text: "unprojectable terminal tail",
		},
	); err != nil || !created {
		t.Fatalf("append terminal tail created=%t err=%v", created, err)
	}
	page := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages", nil, "")
	requireTranscriptWebServingPage(t, page, 3, 0, 2)
	if through := servingJSONInt(p3DecodeObject(t, page)["through_publication_sequence"]); through !=
		int(fixture.fence.StateThroughPublicationSequence) {
		t.Fatalf("terminal snapshot through=%d want=%d", through, fixture.fence.StateThroughPublicationSequence)
	}

	singleID := transcriptWebServingMessageID(1)
	single := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/conversations/"+transcriptWebServingFrameID+"/messages/"+url.PathEscape(singleID), nil, "")
	if single.Code != http.StatusOK || webString(p3DecodeObject(t, single)["id"]) != singleID {
		t.Fatalf("terminal snapshot single status=%d body=%s", single.Code, single.Body.String())
	}

	compat := p3JSONRequest(t, fixture.server, http.MethodGet,
		"/api/frames/"+transcriptWebServingFrameID+"/messages?from=0&limit=10", nil, "")
	compatPage := p3DecodeObject(t, compat)
	if compat.Code != http.StatusOK || len(transcriptWebServingItems(t, compatPage, "messages")) != 3 {
		t.Fatalf("terminal snapshot compatibility status=%d page=%#v", compat.Code, compatPage)
	}
}

func TestTranscriptWebTerminalPresentationFenceRejectsQuarantinedSnapshot(t *testing.T) {
	fixture := newTranscriptWebReadModelServingFixture(t, 1)
	ready, err := fixture.server.transcriptWebTerminalPresentationReady(context.Background(), fixture.stream)
	if err != nil || !ready {
		t.Fatalf("ready=%t err=%v", ready, err)
	}
	if _, err := fixture.db.Exec(`UPDATE transcript_web_projection_state
		SET status='quarantined',last_error_code='projection_source_conflict'
		WHERE stream_uid=? AND branch_id=?`, fixture.stream.UID, fixture.fence.BranchID); err != nil {
		t.Fatal(err)
	}
	ready, err = fixture.server.transcriptWebTerminalPresentationReady(context.Background(), fixture.stream)
	if err != nil || ready {
		t.Fatalf("quarantined ready=%t err=%v", ready, err)
	}
}

func TestTranscriptWebReadModelServingCatchesUpStaleReadyProjectionOnce(t *testing.T) {
	fixture := newTranscriptWebReadModelServingFixture(t, 1)
	before := readTranscriptWebProjectionClock(t, fixture.db, fixture.stream.UID, fixture.fence.BranchID)
	if _, _, created, err := fixture.repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
			ClientMessageID: "read-model-stale-client", FrameEventID: "read-model-stale-event",
			MessageUUID: "read-model-stale-message", Text: "projection is now stale",
		},
	); err != nil || !created {
		t.Fatalf("append stale event created=%t err=%v", created, err)
	}

	const readers = 12
	responses := make([]*httptest.ResponseRecorder, readers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := range readers {
		go func() {
			defer wait.Done()
			<-start
			responses[index] = p3JSONRequest(t, fixture.server, http.MethodGet,
				"/api/conversations/"+transcriptWebServingFrameID+"/messages", nil, "")
		}()
	}
	close(start)
	wait.Wait()
	for index, response := range responses {
		if response.Code != http.StatusOK {
			t.Fatalf("reader %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	after := readTranscriptWebProjectionClock(t, fixture.db, fixture.stream.UID, fixture.fence.BranchID)
	if after.revision != before.revision+1 {
		t.Fatalf("stale projection rebuilds=%d before=%#v after=%#v",
			after.revision-before.revision, before, after)
	}
}

func TestTranscriptWebReadModelServingIsolatesOwnerAndActiveBranch(t *testing.T) {
	fixture := newTranscriptWebReadModelServingFixture(t, 2)
	messageID := transcriptWebServingMessageID(0)
	if _, err := fixture.readModel.GetTranscriptWebProjectionFence(
		context.Background(), "foreign-owner", fixture.stream.UID, fixture.fence.BranchID,
	); !errors.Is(err, transcriptstore.ErrOwnerMismatch) {
		t.Fatalf("foreign read-model fence error=%v", err)
	}
	for _, endpoint := range []struct {
		method, path string
		body         any
	}{
		{method: http.MethodGet, path: "/api/conversations/" + transcriptWebServingFrameID + "/messages"},
		{method: http.MethodGet, path: "/api/conversations/" + transcriptWebServingFrameID + "/messages/" + url.PathEscape(messageID)},
		{method: http.MethodGet, path: "/api/frames/" + transcriptWebServingFrameID + "/messages?from=0&limit=1"},
		{method: http.MethodGet, path: "/api/frames/" + transcriptWebServingFrameID + "/messages/locate?uuid=" + url.QueryEscape(messageID)},
		{method: http.MethodPost, path: "/api/frames/" + transcriptWebServingFrameID + "/messages/locate", body: map[string]any{"uuids": []string{messageID}}},
		{method: http.MethodGet, path: "/api/frames/" + transcriptWebServingFrameID + "/branches/" +
			url.PathEscape(fixture.fence.BranchID) + "/messages?from=0&limit=1"},
	} {
		response := p3JSONRequest(
			t, fixture.server, endpoint.method, endpoint.path, endpoint.body, "foreign-owner",
		)
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), messageID) ||
			strings.Contains(response.Body.String(), "read-model message") {
			t.Fatalf("foreign path=%s status=%d body=%s", endpoint.path, response.Code, response.Body.String())
		}
	}

	for _, wrongBranch := range []string{"br_not_the_active_branch", "br_deadbeef"} {
		public := p3JSONRequest(t, fixture.server, http.MethodGet,
			"/api/conversations/"+transcriptWebServingFrameID+"/messages?branch_id="+
				url.QueryEscape(wrongBranch), nil, "")
		publicPayload := p3DecodeObject(t, public)
		if public.Code != http.StatusConflict || len(publicPayload) != 1 ||
			strings.ToLower(webString(publicPayload["message"])) != "conversation branch changed; refresh and retry" {
			t.Fatalf("public branch %q mismatch status=%d payload=%#v", wrongBranch, public.Code, publicPayload)
		}
		compat := p3JSONRequest(t, fixture.server, http.MethodGet,
			"/api/frames/"+transcriptWebServingFrameID+"/branches/"+
				url.PathEscape(wrongBranch)+"/messages?from=0&limit=1", nil, "")
		compatPayload := p3DecodeObject(t, compat)
		if compat.Code != http.StatusConflict || len(compatPayload) != 1 ||
			strings.ToLower(webString(compatPayload["detail"])) != "conversation branch changed; refresh and retry" {
			t.Fatalf("compat branch %q mismatch status=%d payload=%#v", wrongBranch, compat.Code, compatPayload)
		}
	}
}

func newTranscriptWebReadModelServingFixture(
	t *testing.T,
	messageCount int,
) transcriptWebReadModelServingFixture {
	t.Helper()
	store, repository, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(
		t, store, transcriptWebServingOwnerID, transcriptWebServingProjectID, transcriptWebServingFrameID,
	)
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: transcriptWebServingStreamUID, OwnerID: transcriptWebServingOwnerID,
		ExternalID: transcriptWebServingFrameID, SessionID: transcriptWebServingFrameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: transcriptWebServingProjectID,
		RootFrameID: transcriptWebServingFrameID, FrameID: transcriptWebServingFrameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < messageCount; index++ {
		if _, _, created, err := repository.AppendFrameUserEvent(
			context.Background(), transcriptstore.AppendFrameUserEventInput{
				StreamUID: stream.UID, OwnerID: stream.OwnerID,
				ClientMessageID: transcriptWebServingClientMessageID(index),
				FrameEventID:    fmt.Sprintf("read-model-frame-event-%03d", index),
				MessageUUID:     transcriptWebServingMessageID(index),
				Text:            fmt.Sprintf("read-model message %03d", index),
			},
		); err != nil || !created {
			t.Fatalf("append message %d created=%t err=%v", index, created, err)
		}
	}
	server, readModel, fence := newTranscriptWebReadModelServingServer(
		t, store, repository, db, stream,
	)
	return transcriptWebReadModelServingFixture{
		store: store, repository: repository, db: db, readModel: readModel,
		server: server, stream: stream, fence: fence,
	}
}

func newTranscriptWebReadModelServingServer(
	t *testing.T,
	store *workspace.Store,
	repository *transcriptstore.Repository,
	db *sql.DB,
	stream transcriptstore.Stream,
) (*Server, *transcriptstore.WebReadModelRepository, transcriptstore.TranscriptWebProjectionFence) {
	t.Helper()
	server := New(Options{Workspace: store, Transcript: repository, FileRoot: t.TempDir()})
	if err := server.stopTranscriptWebDelivery(context.Background()); err != nil {
		t.Fatal(err)
	}
	readModel := transcriptstore.NewWebReadModelRepository(db, db)
	server.transcriptWebReadModel = readModel
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if err := server.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	branch, err := repository.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	fence := requireTranscriptWebReadModelFence(t, readModel, stream, branch.ActiveBranchID)
	if fence.StateStatus != "ready" || fence.StateBranchGeneration != fence.BranchGeneration ||
		fence.StateThroughPublicationSequence != fence.ThroughPublicationSequence ||
		fence.StateSourceRevision != fence.SourceRevision {
		t.Fatalf("read-model fence is not ready: %#v", fence)
	}
	return server, readModel, fence
}

func requireTranscriptWebServingPage(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantCount, wantFirst, wantLast int,
) map[string]any {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("page status=%d body=%s", response.Code, response.Body.String())
	}
	page := p3DecodeObject(t, response)
	items := transcriptWebServingItems(t, page, "items")
	if len(items) != wantCount ||
		webString(items[0].(map[string]any)["id"]) != transcriptWebServingMessageID(wantFirst) ||
		webString(items[len(items)-1].(map[string]any)["id"]) != transcriptWebServingMessageID(wantLast) {
		t.Fatalf("page items=%d first=%#v last=%#v", len(items), items[0], items[len(items)-1])
	}
	return page
}

func requireTranscriptWebServingPageIDs(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantIDs ...string,
) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("page status=%d body=%s", response.Code, response.Body.String())
	}
	items := transcriptWebServingItems(t, p3DecodeObject(t, response), "items")
	if len(items) != len(wantIDs) {
		t.Fatalf("page items=%#v want=%#v", items, wantIDs)
	}
	for index, wantID := range wantIDs {
		if got := webString(items[index].(map[string]any)["id"]); got != wantID {
			t.Fatalf("page item %d id=%q want=%q", index, got, wantID)
		}
	}
}

func transcriptWebServingItems(t *testing.T, payload map[string]any, key string) []any {
	t.Helper()
	items, ok := payload[key].([]any)
	if !ok {
		t.Fatalf("payload %s=%T want array: %#v", key, payload[key], payload)
	}
	return items
}

func assertTranscriptWebServingArtifactReference(
	t *testing.T,
	message map[string]any,
	want transcriptWebReadModelArtifactFixture,
	sourceEventID int64,
	availability transcriptstore.ArtifactAvailability,
) {
	t.Helper()
	references := transcriptWebServingItems(t, message, "artifact_refs")
	if len(references) != 1 {
		t.Fatalf("artifact references=%#v", references)
	}
	reference, ok := references[0].(map[string]any)
	if !ok || webString(reference["artifact_id"]) != want.artifactID ||
		webString(reference["version_id"]) != want.versionID ||
		webString(reference["relation"]) != string(transcriptstore.ArtifactRelationAttached) ||
		servingJSONInt(reference["source_event_id"]) != int(sourceEventID) ||
		servingJSONInt(reference["ordinal"]) != 0 ||
		webString(reference["filename"]) != want.filename ||
		webString(reference["content_type"]) != want.contentType ||
		servingJSONInt(reference["size_bytes"]) != int(want.sizeBytes) ||
		webString(reference["checksum"]) != want.checksum ||
		webString(reference["availability"]) != string(availability) {
		t.Fatalf("artifact reference=%#v want=%#v availability=%q", reference, want, availability)
	}
}

func assertTranscriptWebServingUnavailable(
	t *testing.T,
	response *httptest.ResponseRecorder,
	field string,
) {
	t.Helper()
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Body.Len() > 128 {
		t.Fatalf("unavailable response is not bounded: bytes=%d body=%s", response.Body.Len(), response.Body.String())
	}
	payload := p3DecodeObject(t, response)
	if len(payload) < 1 || len(payload) > 2 ||
		strings.ToLower(strings.TrimSpace(webString(payload[field]))) != "conversation history is still being prepared" {
		t.Fatalf("unavailable payload=%#v field=%q", payload, field)
	}
	if field == "message" && payload["code"] != "HISTORY_NOT_READY" {
		t.Fatalf("unavailable payload missing HISTORY_NOT_READY code: %#v", payload)
	}
	lowerBody := strings.ToLower(response.Body.String())
	for _, internal := range []string{"projection", "building", "quarantined", "stale", "sqlite", "internal"} {
		if strings.Contains(lowerBody, internal) {
			t.Fatalf("unavailable response leaked %q: %s", internal, response.Body.String())
		}
	}
}

func transcriptWebServingMessageID(index int) string {
	return fmt.Sprintf("read-model-message-%03d", index)
}

func transcriptWebServingClientMessageID(index int) string {
	return fmt.Sprintf("read-model-client-%03d", index)
}

func servingJSONInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	default:
		return -1
	}
}
