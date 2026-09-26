package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAgentPublicScientificCacheBackfillsOwnerScopedArtifactProjection(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = `{"tool":"cached"}`
	digest := sha256.Sum256([]byte(content))
	expectedSHA256 := hex.EncodeToString(digest[:])
	artifact, version, err := fixture.store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-cached-source", ProjectID: fixture.stream.ProjectID,
		Name: "cached-resource.json", Kind: "application/json", Content: []byte(content),
		CreatedBy: fixture.claim.RunnerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	const sourceURL = "https://downloads.example.org/releases/cached-resource.json"
	if _, err := fixture.server.runtimeStore.Set(artifactRuntimeNamespace, artifact.ID, map[string]any{
		"artifactId": artifact.ID, "versionId": version.ID,
		"mimeType": artifact.Kind, "sizeBytes": version.SizeBytes, "sha256": version.ContentSHA256,
		"publicScientificDownload": map[string]any{
			"source_url": sourceURL, "filename": artifact.Name, "content_type": artifact.Kind,
			"size_bytes": version.SizeBytes, "sha256": version.ContentSHA256,
		},
	}); err != nil {
		t.Fatal(err)
	}
	request, err := parseAgentPublicScientificFileRequest(map[string]any{
		"url": sourceURL, "filename": artifact.Name, "expected_sha256": expectedSHA256,
		"human_description": "Reusing a verified cache fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	cached, found := fixture.server.findAgentPublicScientificCachedContent(fixture.stream.OwnerID, request)
	if !found {
		t.Fatal("owner-scoped artifact projection was not backfilled")
	}
	defer cached.content.Close()
	read, err := io.ReadAll(cached.content)
	if err != nil || string(read) != content || cached.version.ID != version.ID || cached.artifact.ID != artifact.ID {
		t.Fatalf("cached=%#v content=%q err=%v", cached.record, read, err)
	}
	cacheKey := agentPublicScientificDownloadCacheKey(
		fixture.stream.OwnerID, sourceURL, artifact.Name, expectedSHA256,
	)
	if _, found, err := fixture.server.runtimeStore.Get(agentPublicScientificDownloadCacheNamespace, cacheKey); err != nil || !found {
		t.Fatalf("backfilled index found=%t err=%v", found, err)
	}
	if _, found := fixture.server.findAgentPublicScientificCachedContent("owner-foreign", request); found {
		t.Fatal("foreign owner reused the cached artifact")
	}
	request.ExpectedSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, found := fixture.server.findAgentPublicScientificCachedContent(fixture.stream.OwnerID, request); found {
		t.Fatal("different checksum reused the cached artifact")
	}
}

func TestAgentPublicScientificFileReusesContentAddressedDownloadAcrossTasks(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = `{"tool":"P2Rank","version":"2.5.1"}`
	digest := sha256.Sum256([]byte(content))
	expectedSHA256 := hex.EncodeToString(digest[:])
	fetcher := &agentPublicScientificFileFetcher{body: content, contentType: "application/json"}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)

	const sourceURL = "https://downloads.example.org/releases/p2rank-2.5.1.json"
	firstSourceCallID := appendAgentPublicScientificSourceCheckpoint(
		t, fixture, "cache-source-first", sourceURL, false, nil,
	)
	firstInput := map[string]any{
		"source_tool_call_id": firstSourceCallID,
		"url":                 sourceURL,
		"expected_sha256":     expectedSHA256,
		"human_description":   "Downloading a content-addressed execution resource",
	}
	firstResult, err := fixture.server.executeAgentPublicScientificFileDownload(
		agentPublicScientificToolContext(t, fixture, "cache-download-first", firstInput),
		fixture.identity, "cache-download-first", firstInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstArtifacts := agentSaveArtifactResults(t, firstResult)
	if len(firstArtifacts) != 1 || len(fetcher.callSnapshot()) != 1 {
		t.Fatalf("first result=%#v calls=%#v", firstResult, fetcher.callSnapshot())
	}
	firstVersionID := stringValue(firstArtifacts[0]["version_id"])
	cacheKey := agentPublicScientificDownloadCacheKey(
		fixture.stream.OwnerID, sourceURL, "p2rank-2.5.1.json", expectedSHA256,
	)
	if _, found, err := fixture.server.runtimeStore.Get(agentPublicScientificDownloadCacheNamespace, cacheKey); err != nil || !found {
		t.Fatalf("durable cache index found=%t err=%v", found, err)
	}
	// Simulate an artifact written by a pre-index runtime. The second task must
	// discover its compatibility projection, rebuild the index, and stay local.
	if deleted, err := fixture.server.runtimeStore.Delete(agentPublicScientificDownloadCacheNamespace, cacheKey); err != nil || !deleted {
		t.Fatalf("remove cache index deleted=%t err=%v", deleted, err)
	}

	second := newAgentPublicScientificTaskFixture(
		t, fixture, fixture.stream.OwnerID, fixture.stream.ProjectID, "cache-second",
	)
	secondSourceCallID := appendAgentPublicScientificSourceCheckpoint(
		t, second, "cache-source-second", sourceURL, false, nil,
	)
	secondInput := map[string]any{
		"source_tool_call_id": secondSourceCallID,
		"url":                 sourceURL,
		"expected_sha256":     expectedSHA256,
		"human_description":   "Reusing a content-addressed execution resource",
	}
	secondResult, err := second.server.executeAgentPublicScientificFileDownload(
		agentPublicScientificToolContext(t, second, "cache-download-second", secondInput),
		second.identity, "cache-download-second", secondInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondResult["reused"] != true || secondResult["reuse_scope"] != "owner_content_addressed_cache" {
		t.Fatalf("second result did not expose cache reuse: %#v", secondResult)
	}
	download := mapValue(secondResult["download"])
	if download["reused"] != true || stringValue(download["reused_from_version_id"]) != firstVersionID {
		t.Fatalf("reuse receipt=%#v first_version=%q", download, firstVersionID)
	}
	if calls := fetcher.callSnapshot(); len(calls) != 1 {
		t.Fatalf("cross-task reuse repeated the network download: %#v", calls)
	}
	stored, err := os.ReadFile(filepath.Join(second.projectPath, "p2rank-2.5.1.json"))
	if err != nil || string(stored) != content {
		t.Fatalf("reused workspace content=%q err=%v", stored, err)
	}
	if _, found, err := fixture.server.runtimeStore.Get(agentPublicScientificDownloadCacheNamespace, cacheKey); err != nil || !found {
		t.Fatalf("lazy cache backfill found=%t err=%v", found, err)
	}
}

func TestAgentPublicScientificFileCacheIsOwnerAndChecksumScoped(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const content = `{"dataset":"verified"}`
	digest := sha256.Sum256([]byte(content))
	expectedSHA256 := hex.EncodeToString(digest[:])
	fetcher := &agentPublicScientificFileFetcher{body: content, contentType: "application/json"}
	fixture.server.publicScientificFiles = fetcher
	fixture.server.publicScientificDownloadSlots = make(chan struct{}, 2)
	const sourceURL = "https://downloads.example.org/releases/verified-dataset.json"

	firstSourceCallID := appendAgentPublicScientificSourceCheckpoint(
		t, fixture, "cache-scope-source-first", sourceURL, false, nil,
	)
	firstInput := map[string]any{
		"source_tool_call_id": firstSourceCallID, "url": sourceURL,
		"expected_sha256": expectedSHA256, "human_description": "Downloading a verified dataset",
	}
	if _, err := fixture.server.executeAgentPublicScientificFileDownload(
		agentPublicScientificToolContext(t, fixture, "cache-scope-first", firstInput),
		fixture.identity, "cache-scope-first", firstInput,
	); err != nil {
		t.Fatal(err)
	}

	foreign := newAgentPublicScientificTaskFixture(t, fixture, "owner-foreign", "project-foreign-cache", "cache-foreign")
	foreignSourceCallID := appendAgentPublicScientificSourceCheckpoint(
		t, foreign, "cache-scope-source-foreign", sourceURL, false, nil,
	)
	foreignInput := map[string]any{
		"source_tool_call_id": foreignSourceCallID, "url": sourceURL,
		"expected_sha256": expectedSHA256, "human_description": "Downloading an owner-scoped dataset",
	}
	foreignResult, err := foreign.server.executeAgentPublicScientificFileDownload(
		agentPublicScientificToolContext(t, foreign, "cache-scope-foreign", foreignInput),
		foreign.identity, "cache-scope-foreign", foreignInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	if foreignResult["reused"] == true || len(fetcher.callSnapshot()) != 2 {
		t.Fatalf("foreign owner reused cached bytes: result=%#v calls=%#v", foreignResult, fetcher.callSnapshot())
	}

	wrongHash := newAgentPublicScientificTaskFixture(
		t, fixture, fixture.stream.OwnerID, fixture.stream.ProjectID, "cache-wrong-hash",
	)
	wrongHashSourceCallID := appendAgentPublicScientificSourceCheckpoint(
		t, wrongHash, "cache-scope-source-wrong-hash", sourceURL, false, nil,
	)
	wrongHashInput := map[string]any{
		"source_tool_call_id": wrongHashSourceCallID, "url": sourceURL,
		"expected_sha256":   "0000000000000000000000000000000000000000000000000000000000000000",
		"human_description": "Downloading a differently addressed dataset",
	}
	_, err = wrongHash.server.executeAgentPublicScientificFileDownload(
		agentPublicScientificToolContext(t, wrongHash, "cache-scope-wrong-hash", wrongHashInput),
		wrongHash.identity, "cache-scope-wrong-hash", wrongHashInput,
	)
	if !errors.Is(err, errAgentPublicScientificFileChecksum) || len(fetcher.callSnapshot()) != 3 {
		t.Fatalf("wrong hash error=%v calls=%#v", err, fetcher.callSnapshot())
	}
}

func newAgentPublicScientificTaskFixture(
	t *testing.T,
	base *agentSaveArtifactsFixture,
	ownerUserID, projectID, suffix string,
) *agentSaveArtifactsFixture {
	t.Helper()
	projectPath := filepath.Join(t.TempDir(), "project-"+suffix)
	if projectID != base.stream.ProjectID {
		if _, err := base.store.CreateProject(workspace.CreateProjectInput{
			ID: projectID, UserID: ownerUserID, Name: "Project " + suffix, Path: projectPath,
		}); err != nil {
			t.Fatal(err)
		}
	}
	frameID := "frame-" + suffix
	if _, err := base.store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: projectID, AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := base.store.SetFrameRuntimeMetadata(frameID, workspace.FrameRuntimeMetadata{
		FrameID: frameID, ContextData: map[string]any{"web_extra": map[string]any{}},
	}); err != nil {
		t.Fatal(err)
	}
	access, found, err := base.store.GetKernelFrameAccessContext(context.Background(), frameID)
	if err != nil || !found {
		t.Fatalf("kernel access found=%t err=%v", found, err)
	}
	stream, err := base.repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: access.UserID, ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: access.Frame.ProjectID,
		RootFrameID: access.Frame.RootFrameID, FrameID: access.Frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := base.repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-" + suffix,
		FrameEventID: "user-event-" + suffix, MessageUUID: "user-message-" + suffix,
		Text: "Acquire the verified scientific resource.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append input created=%t err=%v", created, err)
	}
	claimed, err := base.repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-" + suffix,
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	workspaceDir := filepath.Join(t.TempDir(), "task-"+suffix)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &agentSaveArtifactsFixture{
		server: base.server, store: base.store, repo: base.repo, db: base.db,
		stream: stream, claim: claimed.Claim,
		identity:    &agentKernelContext{access: access, workspaceDir: workspaceDir},
		projectPath: workspaceDir, databasePath: base.databasePath,
	}
}
