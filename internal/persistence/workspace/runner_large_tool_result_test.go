package workspace

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerLargeToolResultStoreKeepsEvidenceOutOfArtifacts(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedRunnerLargeToolResultProject(t, store)

	ctx := context.Background()
	content := []byte(`{"ok":true,"result":[{"doi":"10.9999/evidence"}]}`)
	record, err := store.WriteRunnerLargeToolResult(ctx, runnerLargeToolResultInput("call-1", content))
	if err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	if record.VersionID == "" || record.ArtifactID != "large-tool-result-"+strings.Repeat("a", 32) ||
		record.SizeBytes != int64(len(content)) || record.ContentSHA256 == "" {
		t.Fatalf("record=%#v", record)
	}
	if !IsRunnerLargeToolResultVersionID(record.VersionID) ||
		IsRunnerLargeToolResultVersionID("ltr-not-a-version") {
		t.Fatalf("large result version identity was not classified exactly: %q", record.VersionID)
	}
	assertRunnerLargeToolResultCounts(t, store, 1, 0)

	replayed, found, err := store.FindRunnerLargeToolResult(ctx, record.ArtifactID, "call-1", "WebFetch")
	if err != nil || !found || replayed.VersionID != record.VersionID {
		t.Fatalf("replay found=%t err=%v record=%#v", found, err, replayed)
	}

	_, reader, found, err := store.OpenRunnerLargeToolResultContent(ctx, record.VersionID, "owner-ltr")
	if err != nil || !found {
		t.Fatalf("open found=%t err=%v", found, err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(got) != string(content) {
		t.Fatalf("read content bytes=%d err=%v", len(got), err)
	}

	if _, _, found, err := store.OpenRunnerLargeToolResultContent(ctx, record.VersionID, "owner-other"); err != nil || found {
		t.Fatalf("foreign owner open found=%t err=%v", found, err)
	}

	page, err := store.ListCompatibilityProjectArtifacts(ctx, "owner-ltr", "project-ltr", true, 100)
	if err != nil {
		t.Fatalf("list project artifacts: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("internal evidence leaked into project artifacts: %d rows", len(page))
	}
}

func TestRunnerLargeToolResultStoreRejectsInvalidAndChangedEvidence(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedRunnerLargeToolResultProject(t, store)

	ctx := context.Background()
	content := []byte(`{"ok":true,"result":[{"doi":"10.9999/evidence"}]}`)
	input := runnerLargeToolResultInput("call-invalid", content)
	input.Content = []byte(`{"broken":`)
	if _, err := store.WriteRunnerLargeToolResult(ctx, input); !errors.Is(err, errRunnerLargeToolResultInvalid) {
		t.Fatalf("invalid JSON error=%v", err)
	}

	input = runnerLargeToolResultInput("call-changed", content)
	first, err := store.WriteRunnerLargeToolResult(ctx, input)
	if err != nil {
		t.Fatalf("write first: %v", err)
	}
	input.Content = []byte(`{"ok":false,"result":[]}`)
	if _, err := store.WriteRunnerLargeToolResult(ctx, input); !errors.Is(err, ErrRunnerLargeToolResultConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	second, found, err := store.FindRunnerLargeToolResult(ctx, first.ArtifactID, "call-changed", "WebFetch")
	if err != nil || !found || second.ContentSHA256 != first.ContentSHA256 {
		t.Fatalf("unchanged evidence found=%t err=%v", found, err)
	}
	assertRunnerLargeToolResultCounts(t, store, 1, 0)
}

func TestRunnerLargeToolResultStoreSurvivesReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	seedRunnerLargeToolResultProject(t, store)
	ctx := context.Background()
	content := []byte(`{"ok":true,"result":"reopen-evidence"}`)
	record, err := store.WriteRunnerLargeToolResult(ctx, runnerLargeToolResultInput("call-reopen", content))
	if err != nil {
		_ = store.Close()
		t.Fatalf("write evidence: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatalf("reopen workspace: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	_, reader, found, err := reopened.OpenRunnerLargeToolResultContent(ctx, record.VersionID, "owner-ltr")
	if err != nil || !found {
		t.Fatalf("reopen content found=%t err=%v", found, err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(got) != string(content) {
		t.Fatalf("reopen read bytes=%d err=%v", len(got), err)
	}
	assertRunnerLargeToolResultCounts(t, reopened, 1, 0)
}

func seedRunnerLargeToolResultProject(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-ltr", UserID: "owner-ltr", Name: "Large tool result project", Path: t.TempDir(),
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-ltr", ProjectID: "project-ltr", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatalf("create frame: %v", err)
	}
}

func runnerLargeToolResultInput(toolCallID string, content []byte) WriteRunnerLargeToolResultInput {
	return WriteRunnerLargeToolResultInput{
		ArtifactID: "large-tool-result-" + strings.Repeat("a", 32), ProjectID: "project-ltr",
		RootFrameID: "frame-ltr", FrameID: "frame-ltr", StreamUID: "frame:frame-ltr",
		OwnerUserID: "owner-ltr", RunnerID: "runner-ltr", ClaimToken: "claim-ltr",
		Attempt: 1, SourceEventID: 7, ToolName: "WebFetch", ToolCallID: toolCallID,
		Content: content,
	}
}

func assertRunnerLargeToolResultCounts(t *testing.T, store *Store, evidence, artifactRows int) {
	t.Helper()
	for table, want := range map[string]int{
		"artifacts": artifactRows, "artifact_versions": artifactRows,
		"runner_large_tool_results": evidence,
	} {
		var got int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count=%d, want %d", table, got, want)
		}
	}
}
