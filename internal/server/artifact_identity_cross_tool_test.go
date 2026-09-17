package server

import "testing"

func TestArtifactProducersShareStreamPathIdentity(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := "HEADER    HUMAN CRBN                                          4TZ4\nATOM      1  N   GLY A   1      11.104  13.207   8.000  1.00 20.00           N  \n"
	fixture.server.rcsbFiles = &agentRCSBFetcher{body: content}
	downloadInput := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb",
		"human_description": "Downloading validated coordinates",
	}
	downloadCtx := fixture.toolContext(t, "download-4tz4", downloadInput)
	first, err := fixture.server.executeAgentRCSBFileDownload(
		downloadCtx, fixture.identity, "download-4tz4", downloadInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstArtifacts := agentSaveArtifactResults(t, first)
	if len(firstArtifacts) != 1 {
		t.Fatalf("download artifacts=%#v", first)
	}

	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "4TZ4.pdb", content)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-publish-4tz4", 1, write)
	saveInput := map[string]any{
		"files": []any{"4TZ4.pdb"}, "language": "text",
		"human_description": "Publishing downloaded coordinates",
	}
	second, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-4tz4", saveInput),
		fixture.identity, "save-4tz4", saveInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondArtifacts := agentSaveArtifactResults(t, second)
	if len(secondArtifacts) != 1 {
		t.Fatalf("save artifacts=%#v", second)
	}
	if firstArtifacts[0]["artifact_id"] != secondArtifacts[0]["artifact_id"] {
		t.Fatalf("cross-tool artifact identity first=%#v second=%#v", firstArtifacts[0], secondArtifacts[0])
	}
	if firstArtifacts[0]["version_id"] != secondArtifacts[0]["version_id"] ||
		secondArtifacts[0]["version_number"] != 1 ||
		!boolValue(secondArtifacts[0]["unchanged"], false) {
		t.Fatalf("unchanged cross-tool publication did not reuse the canonical version first=%#v second=%#v", firstArtifacts[0], secondArtifacts[0])
	}

	var artifactCount, versionCount, commitCount int
	if err := fixture.db.QueryRow(
		"SELECT COUNT(*) FROM artifacts WHERE project_id=? AND name=?",
		fixture.stream.ProjectID, "4TZ4.pdb",
	).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(
		"SELECT COUNT(*) FROM artifact_versions WHERE artifact_id=?",
		firstArtifacts[0]["artifact_id"],
	).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(
		"SELECT COUNT(*) FROM transcript_artifact_commits WHERE stream_uid=? AND artifact_id=?",
		fixture.stream.UID, firstArtifacts[0]["artifact_id"],
	).Scan(&commitCount); err != nil {
		t.Fatal(err)
	}
	if artifactCount != 1 || versionCount != 1 || commitCount != 1 {
		t.Fatalf("shared artifact counts artifacts=%d versions=%d commits=%d", artifactCount, versionCount, commitCount)
	}

	replayed, err := fixture.server.executeAgentRCSBFileDownload(
		downloadCtx, fixture.identity, "download-4tz4", downloadInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	replayedArtifacts := agentSaveArtifactResults(t, replayed)
	if len(replayedArtifacts) != 1 ||
		replayedArtifacts[0]["artifact_id"] != firstArtifacts[0]["artifact_id"] ||
		replayedArtifacts[0]["version_id"] != firstArtifacts[0]["version_id"] ||
		len(fixture.server.rcsbFiles.(*agentRCSBFetcher).calls) != 1 {
		t.Fatalf("replayed download=%#v calls=%d", replayed, len(fixture.server.rcsbFiles.(*agentRCSBFetcher).calls))
	}
}
