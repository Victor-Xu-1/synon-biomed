package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/persistence/workspace"
)

func TestTranscriptWebProjectionCacheSingleflightSnapshotOwnerAndErrorIsolation(t *testing.T) {
	cache := transcriptWebProjectionCache{maxEntries: 16, maxBytes: 1 << 20}
	key := testTranscriptWebProjectionCacheKey("owner-a", 41)
	messages := []map[string]any{{
		"id":      "message-a",
		"content": map[string]any{"blocks": []any{map[string]any{"text": "immutable"}}},
	}}
	var builds atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	load := func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
		builds.Add(1)
		startOnce.Do(func() { close(started) })
		<-release
		return messages, key.snapshot(), nil
	}
	const callers = 12
	results := make(chan []map[string]any, callers)
	errorsCh := make(chan error, callers)
	for index := 0; index < callers; index++ {
		go func() {
			projected, _, _, err := cache.getOrLoad(context.Background(), key, load)
			results <- projected
			errorsCh <- err
		}()
	}
	<-started
	deadline := time.Now().Add(5 * time.Second)
	for {
		cache.mu.Lock()
		flight := cache.flights[key]
		waiters := 0
		if flight != nil {
			waiters = flight.waiters
		}
		cache.mu.Unlock()
		if waiters == callers-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("singleflight waiters=%d want=%d", waiters, callers-1)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	for index := 0; index < callers; index++ {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
		projected := <-results
		if len(projected) != 1 || projected[0]["id"] != "message-a" {
			t.Fatalf("projected=%#v", projected)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("same snapshot builds=%d want=1", got)
	}

	messages[0]["id"] = "source-mutated"
	messages[0]["content"].(map[string]any)["blocks"].([]any)[0].(map[string]any)["text"] = "source-mutated"
	first, _, _, err := cache.getOrLoad(context.Background(), key, func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
		t.Fatal("cache hit rebuilt projection")
		return nil, transcriptstore.ProjectionSnapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	page := cloneTranscriptWebProjectionMessages(first)
	page[0]["id"] = "mutated"
	page[0]["content"].(map[string]any)["blocks"].([]any)[0].(map[string]any)["text"] = "mutated"
	second, _, _, err := cache.getOrLoad(context.Background(), key, func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
		t.Fatal("immutable cache hit rebuilt projection")
		return nil, transcriptstore.ProjectionSnapshot{}, nil
	})
	if err != nil || second[0]["id"] != "message-a" ||
		second[0]["content"].(map[string]any)["blocks"].([]any)[0].(map[string]any)["text"] != "immutable" {
		t.Fatalf("immutable projection=%#v err=%v", second, err)
	}
	if &first[0] != &second[0] {
		t.Fatal("cache hit cloned the complete immutable history")
	}

	newSnapshotKey := testTranscriptWebProjectionCacheKey("owner-a", 42)
	if _, _, _, err := cache.getOrLoad(context.Background(), newSnapshotKey, func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
		builds.Add(1)
		return messages, newSnapshotKey.snapshot(), nil
	}); err != nil {
		t.Fatal(err)
	}
	foreignOwnerKey := testTranscriptWebProjectionCacheKey("owner-b", 42)
	if _, _, _, err := cache.getOrLoad(context.Background(), foreignOwnerKey, func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
		builds.Add(1)
		return messages, foreignOwnerKey.snapshot(), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := builds.Load(); got != 3 {
		t.Fatalf("snapshot/owner isolated builds=%d want=3", got)
	}

	errorKey := testTranscriptWebProjectionCacheKey("owner-a", 99)
	wantErr := errors.New("projection failed")
	for attempt := 0; attempt < 2; attempt++ {
		_, _, _, err := cache.getOrLoad(context.Background(), errorKey, func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
			builds.Add(1)
			return nil, transcriptstore.ProjectionSnapshot{}, wantErr
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("attempt=%d err=%v", attempt, err)
		}
	}
	if got := builds.Load(); got != 5 {
		t.Fatalf("failed projection was cached: builds=%d want=5", got)
	}
}

func TestTranscriptWebProjectionCacheLRUEvictsByEntriesAndEstimatedBytes(t *testing.T) {
	cache := transcriptWebProjectionCache{maxEntries: 2, maxBytes: 1 << 20}
	loadCount := map[transcriptWebProjectionCacheKey]int{}
	load := func(key transcriptWebProjectionCacheKey, text string) func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
		return func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
			loadCount[key]++
			return []map[string]any{{"id": key.ownerID, "content": map[string]any{"content": text}}}, key.snapshot(), nil
		}
	}
	key1 := testTranscriptWebProjectionCacheKey("owner-1", 1)
	key2 := testTranscriptWebProjectionCacheKey("owner-2", 1)
	key3 := testTranscriptWebProjectionCacheKey("owner-3", 1)
	for _, key := range []transcriptWebProjectionCacheKey{key1, key2} {
		if _, _, _, err := cache.getOrLoad(context.Background(), key, load(key, "entry")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := cache.getOrLoad(context.Background(), key1, load(key1, "must stay hot")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := cache.getOrLoad(context.Background(), key3, load(key3, "entry")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := cache.getOrLoad(context.Background(), key2, load(key2, "rebuilt after LRU eviction")); err != nil {
		t.Fatal(err)
	}
	if loadCount[key1] != 1 || loadCount[key2] != 2 || loadCount[key3] != 1 {
		t.Fatalf("entry LRU load counts=%#v", loadCount)
	}

	byteCache := transcriptWebProjectionCache{maxEntries: 10, maxBytes: 1 << 20}
	byteKey1 := testTranscriptWebProjectionCacheKey("byte-owner-1", 1)
	byteKey2 := testTranscriptWebProjectionCacheKey("byte-owner-2", 1)
	if _, _, _, err := byteCache.getOrLoad(context.Background(), byteKey1, load(byteKey1, string(bytes.Repeat([]byte("a"), 1024)))); err != nil {
		t.Fatal(err)
	}
	byteCache.mu.Lock()
	firstBytes := byteCache.bytes
	byteCache.maxBytes = firstBytes*2 - 1
	byteCache.mu.Unlock()
	if firstBytes <= 0 {
		t.Fatalf("estimated bytes=%d", firstBytes)
	}
	if _, _, _, err := byteCache.getOrLoad(context.Background(), byteKey2, load(byteKey2, string(bytes.Repeat([]byte("b"), 1024)))); err != nil {
		t.Fatal(err)
	}
	byteCache.mu.Lock()
	entryCount, cachedBytes, maxBytes := len(byteCache.entries), byteCache.bytes, byteCache.maxBytes
	byteCache.mu.Unlock()
	if entryCount != 1 || cachedBytes > maxBytes {
		t.Fatalf("byte LRU entries=%d bytes=%d max=%d", entryCount, cachedBytes, maxBytes)
	}
}

func TestTranscriptWebProjectionStopsAtRequestCancellation(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-projection-cancel", "frame-projection-cancel")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-projection-cancel", MessageUUID: "user-cancel-1",
		ClientMessageID: "user-cancel-client-1", Text: "first",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-projection-cancel")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-cancel-client-2",
		FrameEventID: "frame-event-cancel-2", MessageUUID: "user-cancel-2", Text: "second",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	visited := 0
	err = server.visitTranscriptWebProjectionSnapshot(ctx, stream, stream.OwnerID, snapshot, false,
		func(transcriptstore.ProjectedEvent) error {
			visited++
			cancel()
			return nil
		})
	if !errors.Is(err, context.Canceled) || visited != 1 {
		t.Fatalf("err=%v visited=%d want context canceled after first event", err, visited)
	}

	cache := transcriptWebProjectionCache{maxEntries: 2, maxBytes: 1 << 20}
	key := testTranscriptWebProjectionCacheKey("owner-cancel", 1)
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, _, _, err := cache.getOrLoad(canceled, key, func() ([]map[string]any, transcriptstore.ProjectionSnapshot, error) {
		return []map[string]any{{"id": "must-not-cache"}}, key.snapshot(), nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cache build err=%v", err)
	}
	cache.mu.Lock()
	entries := len(cache.entries)
	cache.mu.Unlock()
	if entries != 0 {
		t.Fatalf("canceled cache build retained %d entries", entries)
	}
}

func TestActivatedTranscriptLongHistoryReusesProjectionForListAnchorAndSingle(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-projection-cache", "frame-projection-cache")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })

	const turns = 106
	const deltasPerTurn = 30
	var stream transcriptstore.Stream
	for turn := 0; turn < turns; turn++ {
		suffix := fmt.Sprintf("%03d", turn)
		if turn == 0 {
			if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
				FrameID: "frame-projection-cache", MessageUUID: "user-" + suffix,
				ClientMessageID: "user-client-" + suffix, Text: "question " + suffix,
			}); err != nil {
				t.Fatal(err)
			}
			var found bool
			var err error
			stream, found, err = repo.GetFrameStreamBySession(context.Background(), "local", "frame-projection-cache")
			if err != nil || !found {
				t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
			}
		} else if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-client-" + suffix,
			FrameEventID: "frame-event-" + suffix, MessageUUID: "user-" + suffix, Text: "question " + suffix,
		}); err != nil {
			t.Fatal(err)
		}
		claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-" + suffix,
			TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
		})
		if err != nil || !claim.Claimed {
			t.Fatalf("turn=%d claim=%#v err=%v", turn, claim, err)
		}
		for delta := 0; delta < deltasPerTurn; delta++ {
			appendRunnerPayloadEvent(t, repo, claim.Claim,
				fmt.Sprintf("delta-%s-%02d", suffix, delta), "content_delta",
				map[string]any{"text": fmt.Sprintf("chunk-%02d ", delta)},
			)
		}
		if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
			Claim: claim.Claim, ClientMessageID: "finish-" + suffix, Status: "completed",
			PayloadJSON: []byte(`{"status":"completed"}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	list := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-projection-cache/messages?limit=50&content_mode=compact", nil, "")
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	etag := list.Header().Get("ETag")
	if etag == "" || list.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("list etag=%q cache-control=%q", etag, list.Header().Get("Cache-Control"))
	}
	if timing := list.Header().Get("Server-Timing"); !strings.HasPrefix(timing, "history_projection;dur=") {
		t.Fatalf("list server-timing=%q", timing)
	}
	cachedRequest := httptest.NewRequest(http.MethodGet,
		"/api/conversations/frame-projection-cache/messages?limit=50&content_mode=compact", nil)
	cachedRequest.RemoteAddr = "127.0.0.1:12345"
	cachedRequest.Header.Set("If-None-Match", etag)
	cached := httptest.NewRecorder()
	server.Handler().ServeHTTP(cached, cachedRequest)
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatalf("cached status=%d body=%q", cached.Code, cached.Body.String())
	}
	page := p3DecodeObject(t, list)
	items, _ := page["items"].([]any)
	if len(items) != 50 || page["has_more_before"] != true {
		t.Fatalf("long history page=%#v", page)
	}
	if builds := testTranscriptWebProjectionBuilds(server); builds != 1 {
		t.Fatalf("list builds=%d want=1", builds)
	}
	oldest := webString(page["oldest_cursor"])
	previous := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-projection-cache/messages?limit=50&content_mode=compact&before="+url.QueryEscape(oldest), nil, "")
	if previous.Code != http.StatusOK {
		t.Fatalf("previous status=%d body=%s", previous.Code, previous.Body.String())
	}
	anchorID := webString(items[len(items)/2].(map[string]any)["id"])
	anchored := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-projection-cache/messages?limit=25&anchor_message_id="+url.QueryEscape(anchorID), nil, "")
	if anchored.Code != http.StatusOK {
		t.Fatalf("anchor status=%d body=%s", anchored.Code, anchored.Body.String())
	}
	single := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-projection-cache/messages/"+url.PathEscape(anchorID), nil, "")
	if single.Code != http.StatusOK || webString(p3DecodeObject(t, single)["id"]) != anchorID {
		t.Fatalf("single status=%d body=%s", single.Code, single.Body.String())
	}
	if builds := testTranscriptWebProjectionBuilds(server); builds != 1 {
		t.Fatalf("same snapshot list/previous/anchor/single builds=%d want=1", builds)
	}

	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "new-snapshot-client",
		FrameEventID: "new-snapshot-frame-event", MessageUUID: "new-snapshot-message", Text: "new snapshot",
	}); err != nil {
		t.Fatal(err)
	}
	newSnapshot := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-projection-cache/messages?limit=50&content_mode=compact", nil, "")
	if newSnapshot.Code != http.StatusOK {
		t.Fatalf("new snapshot status=%d body=%s", newSnapshot.Code, newSnapshot.Body.String())
	}
	if nextETag := newSnapshot.Header().Get("ETag"); nextETag == "" || nextETag == etag {
		t.Fatalf("new snapshot etag=%q previous=%q", nextETag, etag)
	}
	if builds := testTranscriptWebProjectionBuilds(server); builds != 2 {
		t.Fatalf("new snapshot builds=%d want=2", builds)
	}
}

func TestActivatedTranscriptProjectionCacheInvalidatesForLateArtifactBinding(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-artifact-cache", "frame-artifact-cache")
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-cache", ProjectID: "project-artifact-cache", Name: "result.txt", Kind: "text",
		Content: []byte("result"), CreatedBy: "runner-cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_runtime_metadata(artifact_id,root_frame_id,frame_id) VALUES(?,?,?)`,
		artifact.ID, "frame-artifact-cache", "frame-artifact-cache"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO artifact_version_provenance(version_id,frame_id,content_type) VALUES(?,?,?)`,
		version.ID, "frame-artifact-cache", "text/plain"); err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-artifact-cache", OwnerID: "local", ExternalID: "frame-artifact-cache", SessionID: "frame-artifact-cache",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-artifact-cache",
		RootFrameID: "frame-artifact-cache", FrameID: "frame-artifact-cache", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-artifact-cache",
		PayloadJSON: []byte(`{"text":"build it"}`),
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-cache", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	assistantInput := transcriptstore.AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: "assistant-artifact-cache", Type: "assistant_message",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"result ready"}`),
	}
	assistant, refs, created, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), assistantInput)
	if err != nil || !created || len(refs) != 0 {
		t.Fatalf("assistant=%#v refs=%#v created=%t err=%v", assistant, refs, created, err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	warm := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-artifact-cache/messages?limit=50", nil, "")
	if warm.Code != http.StatusOK {
		t.Fatalf("warm status=%d body=%s", warm.Code, warm.Body.String())
	}
	warmItems, _ := p3DecodeObject(t, warm)["items"].([]any)
	assistantMessageID := ""
	for _, item := range warmItems {
		message, _ := item.(map[string]any)
		if content, _ := message["content"].(map[string]any); webString(content["content"]) == "result ready" {
			assistantMessageID = webString(message["id"])
			if refs, _ := message["artifact_refs"].([]any); len(refs) != 0 {
				t.Fatalf("warm artifact refs=%#v", refs)
			}
		}
	}
	if assistantMessageID == "" || testTranscriptWebProjectionBuilds(server) != 1 {
		t.Fatalf("assistant id=%q builds=%d", assistantMessageID, testTranscriptWebProjectionBuilds(server))
	}
	before, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if revision, err := repo.ArtifactReferenceRevision(context.Background(), stream.UID, stream.OwnerID); err != nil || revision != 0 {
		t.Fatalf("warm artifact revision=%d err=%v", revision, err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, stream.UID, claimed.Claim.Attempt, assistant.EventID, 0,
		artifact.ID, version.ID, "produced", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if replayed, bound, replayCreated, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), assistantInput); err != nil || replayCreated || replayed.EventID != assistant.EventID || len(bound) != 1 {
		t.Fatalf("replay=%#v refs=%#v created=%t err=%v", replayed, bound, replayCreated, err)
	}
	after, err := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || after != before {
		t.Fatalf("snapshot after=%#v before=%#v err=%v", after, before, err)
	}
	if revision, err := repo.ArtifactReferenceRevision(context.Background(), stream.UID, stream.OwnerID); err != nil || revision != 1 {
		t.Fatalf("bound artifact revision=%d err=%v", revision, err)
	}
	updated := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-artifact-cache/messages?limit=50", nil, "")
	if updated.Code != http.StatusOK || testTranscriptWebProjectionBuilds(server) != 2 {
		t.Fatalf("updated status=%d builds=%d body=%s", updated.Code, testTranscriptWebProjectionBuilds(server), updated.Body.String())
	}
	exact := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-artifact-cache/messages/"+url.PathEscape(assistantMessageID), nil, "")
	if exact.Code != http.StatusOK {
		t.Fatalf("exact status=%d body=%s", exact.Code, exact.Body.String())
	}
	exactRefs, _ := p3DecodeObject(t, exact)["artifact_refs"].([]any)
	if len(exactRefs) != 1 || webString(exactRefs[0].(map[string]any)["artifact_id"]) != artifact.ID ||
		webString(exactRefs[0].(map[string]any)["version_id"]) != version.ID {
		t.Fatalf("exact refs=%#v", exactRefs)
	}
	if _, bound, replayCreated, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), assistantInput); err != nil || replayCreated || len(bound) != 1 {
		t.Fatalf("second replay refs=%#v created=%t err=%v", bound, replayCreated, err)
	}
	idempotent := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-artifact-cache/messages?limit=50", nil, "")
	if idempotent.Code != http.StatusOK || testTranscriptWebProjectionBuilds(server) != 2 {
		t.Fatalf("idempotent status=%d builds=%d body=%s", idempotent.Code, testTranscriptWebProjectionBuilds(server), idempotent.Body.String())
	}
	if affected, err := repo.MarkArtifactVersionUnavailable(
		context.Background(), stream.OwnerID, artifact.ID, version.ID, transcriptstore.ArtifactDeleted,
	); err != nil || affected != 1 {
		t.Fatalf("mark deleted affected=%d err=%v", affected, err)
	}
	deleted := p3JSONRequest(t, server, http.MethodGet,
		"/api/conversations/frame-artifact-cache/messages/"+url.PathEscape(assistantMessageID), nil, "")
	deletedRefs, _ := p3DecodeObject(t, deleted)["artifact_refs"].([]any)
	if deleted.Code != http.StatusOK || testTranscriptWebProjectionBuilds(server) != 2 || len(deletedRefs) != 1 ||
		webString(deletedRefs[0].(map[string]any)["availability"]) != string(transcriptstore.ArtifactDeleted) {
		t.Fatalf("deleted status=%d builds=%d refs=%#v", deleted.Code, testTranscriptWebProjectionBuilds(server), deletedRefs)
	}
	if revision, err := repo.ArtifactReferenceRevision(context.Background(), stream.UID, stream.OwnerID); err != nil || revision != 1 {
		t.Fatalf("deleted artifact revision=%d err=%v", revision, err)
	}
}

func testTranscriptWebProjectionCacheKey(ownerID string, through int64) transcriptWebProjectionCacheKey {
	authority := transcriptstore.FrameAuthority{
		OwnerID: ownerID, SessionID: "session", ActiveStreamUID: "stream", ActiveEpoch: 3,
		AuthorityGeneration: 7, ReadAuthority: "transcript_payload_v1", WriteAuthority: "transcript_payload_v1",
		ActivationID: bytes.Repeat([]byte{byte(len(ownerID))}, 32),
	}
	stream := transcriptstore.Stream{UID: "stream", OwnerID: ownerID, SessionID: "session", Epoch: 3}
	snapshot := transcriptstore.ProjectionSnapshot{
		StreamUID: "stream", BranchID: "branch", BranchGeneration: 11, ThroughPublicationSequence: through,
	}
	return newTranscriptWebProjectionCacheKey(authority, stream, snapshot, 0)
}

func testTranscriptWebProjectionBuilds(server *Server) int64 {
	server.transcriptWebCache.mu.Lock()
	defer server.transcriptWebCache.mu.Unlock()
	return server.transcriptWebCache.builds
}
