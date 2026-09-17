package server

import (
	"context"
	"testing"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestArtifactDraftIsHiddenUntilValidatedCompletionPublishesSameVersion(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Decision report\n\nSupported conclusion.\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-draft-report", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md"}, "language": "text",
		"destination":       map[string]any{"out/report.md": "snapshot"},
		"human_description": "Saving the completed decision report",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-draft-report", input), fixture.identity, "save-draft-report", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || artifacts[0]["publication_state"] != "draft" ||
		artifacts[0]["artifact_ref"] != nil || artifacts[0]["markdown_link"] != nil {
		t.Fatalf("draft result=%#v", result)
	}
	artifactID := stringValue(artifacts[0]["artifact_id"])
	versionID := stringValue(artifacts[0]["version_id"])
	if listed, listErr := fixture.store.ListCompatibilityConversationArtifacts(
		context.Background(), "owner-save", "project-save", "frame-save", true,
	); listErr != nil || len(listed) != 0 {
		t.Fatalf("visible before completion=%#v err=%v", listed, listErr)
	}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifactID, VersionID: versionID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	if err := fixture.server.publishSessionRunnerCompletionArtifacts(
		context.Background(),
		sessionstore.Session{ID: "frame-save", Project: &sessionstore.Project{ID: "project-save"}},
		run, commits,
	); err != nil {
		t.Fatal(err)
	}
	if intermediate, found, stateErr := fixture.store.ArtifactVersionIntermediate(versionID); stateErr != nil || !found || intermediate {
		t.Fatalf("published intermediate=%t found=%t err=%v", intermediate, found, stateErr)
	}
	listed, listErr := fixture.store.ListCompatibilityConversationArtifacts(
		context.Background(), "owner-save", "project-save", "frame-save", true,
	)
	if listErr != nil || len(listed) != 1 || listed[0].VersionID != versionID || listed[0].IsIntermediate {
		t.Fatalf("visible after completion=%#v err=%v", listed, listErr)
	}
}

func TestArtifactDraftFromPartiallySuccessfulSaveBatchPublishesAtCompletion(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Decision report\n\nSupported conclusion.\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-partial-report", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md", "out/missing.csv"}, "language": "text",
		"destination": map[string]any{
			"out/report.md": "snapshot", "out/missing.csv": "snapshot",
		},
		"human_description": "Saving the completed report and data table",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-partial-report", input), fixture.identity, "save-partial-report", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || len(anySliceValue(mapValue(result)["errors"])) != 1 {
		t.Fatalf("partial save result=%#v", result)
	}
	artifactID := stringValue(artifacts[0]["artifact_id"])
	versionID := stringValue(artifacts[0]["version_id"])
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifactID, VersionID: versionID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	if err := fixture.server.publishSessionRunnerCompletionArtifacts(
		context.Background(),
		sessionstore.Session{ID: "frame-save", Project: &sessionstore.Project{ID: "project-save"}},
		run, commits,
	); err != nil {
		t.Fatal(err)
	}
	if intermediate, found, stateErr := fixture.store.ArtifactVersionIntermediate(versionID); stateErr != nil || !found || intermediate {
		t.Fatalf("published partial-save intermediate=%t found=%t err=%v", intermediate, found, stateErr)
	}
}

func TestArtifactDraftPublishesFromDurableCommitsWithoutProviderHistory(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Preserved report\n\nLatest durable bytes.\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-compacted-draft", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md", "out/missing.csv"}, "language": "text",
		"destination":       map[string]any{"out/report.md": "snapshot", "out/missing.csv": "snapshot"},
		"human_description": "Save a report with a missing companion",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(fixture.toolContext(t, "save-compacted-draft", input), fixture.identity, "save-compacted-draft", input)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || len(anySliceValue(mapValue(result)["errors"])) != 1 {
		t.Fatalf("partial save=%#v", result)
	}
	versionID := stringValue(artifacts[0]["version_id"])
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	commits, err := fixture.server.sessionRunnerArtifactCommitReferences(context.Background(), run)
	if err != nil || len(commits) != 1 || commits[0].VersionID != versionID {
		t.Fatalf("durable commits=%#v err=%v", commits, err)
	}
	if err := fixture.server.publishSessionRunnerCompletionArtifacts(context.Background(), sessionstore.Session{ID: "frame-save", Project: &sessionstore.Project{ID: "project-save"}}, run, commits); err != nil {
		t.Fatal(err)
	}
	listed, err := fixture.store.ListCompatibilityConversationArtifacts(context.Background(), "owner-save", "project-save", "frame-save", true)
	if err != nil || len(listed) != 1 || listed[0].VersionID != versionID || listed[0].IsIntermediate {
		t.Fatalf("published list=%#v err=%v", listed, err)
	}
}
