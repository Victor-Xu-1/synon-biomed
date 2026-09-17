package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openWorkspaceMemoryStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustCreateWorkspaceMemory(t *testing.T, store *Store, input CreateMemoryInput) Memory {
	t.Helper()
	memory, err := store.CreateMemory(input)
	if err != nil {
		t.Fatal(err)
	}
	return memory
}

func TestWorkspaceExtractionCursorIsMonotonicAndRepairsOvershootWithCAS(t *testing.T) {
	store := openWorkspaceMemoryStore(t)
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "user", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "GENERAL", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if cursor, err := store.LastExtractMessageIndex(ctx, "frame"); err != nil || cursor != 0 {
		t.Fatalf("initial cursor = %d, %v", cursor, err)
	}
	if changed, err := store.SetLastExtractMessageIndex(ctx, "frame", 8, nil); err != nil || !changed {
		t.Fatalf("advance cursor = %v, %v", changed, err)
	}
	if changed, err := store.SetLastExtractMessageIndex(ctx, "frame", 4, nil); err != nil || changed {
		t.Fatalf("stale monotonic update = %v, %v", changed, err)
	}
	expected := 8
	if changed, err := store.SetLastExtractMessageIndex(ctx, "frame", 4, &expected); err != nil || !changed {
		t.Fatalf("overshoot repair = %v, %v", changed, err)
	}
	if changed, err := store.SetLastExtractMessageIndex(ctx, "frame", 2, &expected); err != nil || changed {
		t.Fatalf("stale overshoot repair = %v, %v", changed, err)
	}
	if cursor, err := store.LastExtractMessageIndex(ctx, "frame"); err != nil || cursor != 4 {
		t.Fatalf("final cursor = %d, %v", cursor, err)
	}
}

func TestWorkspaceExtractionManifestIsProjectRelevantAndExcludesContainedRows(t *testing.T) {
	store := openWorkspaceMemoryStore(t)
	ctx := context.Background()
	for _, project := range []CreateProjectInput{{ID: "current", UserID: "user", Name: "Current"}, {ID: "other", UserID: "user", Name: "Other"}} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 17, 1, 2, 3, 0, time.UTC)
	if _, err := store.db.Exec(`INSERT INTO artifacts (id,project_id,name,kind,created_at,updated_at) VALUES ('artifact','current','assay.csv','file',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	contained, err := store.CreateMemoryCategory(ctx, "user", "Archive", "Explicit only", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []CreateMemoryInput{
		{ID: "profile", UserID: "user", Body: "profile", Origin: "user", Evidence: "stated"},
		{ID: "project", UserID: "user", Body: "project", Origin: "extractor", Evidence: "observed", SubjectProjectID: "current"},
		{ID: "artifact", UserID: "user", Body: "artifact", Origin: "extractor", Evidence: "observed", SubjectArtifactID: "artifact"},
		{ID: "contained", UserID: "user", Body: "contained", Origin: "extractor", Evidence: "observed", SubjectProjectID: "current", CategoryID: contained.ID},
		{ID: "other", UserID: "user", Body: "other", Origin: "extractor", Evidence: "observed", SubjectProjectID: "other"},
	} {
		mustCreateWorkspaceMemory(t, store, input)
	}
	rows, err := store.ListMemoryExtractionRows(ctx, "user", "current")
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		ids[row.ID] = struct{}{}
	}
	for _, id := range []string{"profile", "project", "artifact"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("manifest missing %q: %#v", id, rows)
		}
	}
	for _, id := range []string{"contained", "other"} {
		if _, ok := ids[id]; ok {
			t.Fatalf("manifest unexpectedly included %q: %#v", id, rows)
		}
	}
}

func TestWorkspaceMemoryMultilineSanitizerPreservesMeaningfulBreaks(t *testing.T) {
	got := SanitizeMemoryMultilineText("# Heading\n[Memory] first\n\n\n<memory>second</memory>")
	if got != "Heading\nfirst\n\nsecond" {
		t.Fatalf("multiline sanitized text = %q", got)
	}
	if got := SanitizeMemoryMultilineText("  plain\n\n\ntext  "); got != "  plain\n\ntext  " {
		t.Fatalf("multiline sanitizer changed outer whitespace = %q", got)
	}
}

func TestWorkspaceExtractorReplacementCreatesFencedSuccessor(t *testing.T) {
	store := openWorkspaceMemoryStore(t)
	ctx := context.Background()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "user", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "GENERAL", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	predecessor := mustCreateWorkspaceMemory(t, store, CreateMemoryInput{
		ID: "mem_old", UserID: "user", Body: "old v4.0.2", Origin: "extractor", Evidence: "observed",
		SubjectProjectID: "project", SourceFrameID: "frame",
	})
	successor, replaced, err := store.CreateAndSupersedeExtractorMemory(ctx, predecessor, "user", "project", "frame", "mem_new", "new v4.0.3 (was v4.0.2)", "stated")
	if err != nil || !replaced {
		t.Fatalf("extractor replace = %#v, %v, %v", successor, replaced, err)
	}
	if successor.ID != "mem_new" || successor.Origin != "extractor" || successor.SourceFrameID != "frame" {
		t.Fatalf("successor = %#v", successor)
	}
	var supersededBy string
	if err := store.db.QueryRow(`SELECT superseded_by FROM memories WHERE id = 'mem_old'`).Scan(&supersededBy); err != nil || supersededBy != "mem_new" {
		t.Fatalf("predecessor link = %q, %v", supersededBy, err)
	}

	stale := predecessor
	stale.Body = "stale body"
	if _, replaced, err := store.CreateAndSupersedeExtractorMemory(ctx, stale, "user", "project", "frame", "mem_orphan", "wrong", "observed"); err != nil || replaced {
		t.Fatalf("stale replacement = %v, %v", replaced, err)
	}
	var orphanCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id = 'mem_orphan'`).Scan(&orphanCount); err != nil || orphanCount != 0 {
		t.Fatalf("orphan successor count = %d, %v", orphanCount, err)
	}
}

func TestWorkspaceExtractorReplacementProtectsUserAndCrossProjectRows(t *testing.T) {
	store := openWorkspaceMemoryStore(t)
	ctx := context.Background()
	for _, project := range []CreateProjectInput{{ID: "current", UserID: "user", Name: "Current"}, {ID: "other", UserID: "user", Name: "Other"}} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: "current", AgentName: "GENERAL", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	userRow := mustCreateWorkspaceMemory(t, store, CreateMemoryInput{ID: "user-row", UserID: "user", Body: "user", Origin: "user", Evidence: "stated", SubjectProjectID: "current"})
	if _, _, err := store.CreateAndSupersedeExtractorMemory(ctx, userRow, "user", "current", "frame", "user-new", "changed", "stated"); err == nil || !strings.Contains(err.Error(), ErrAgentMemoryMutationForbidden.Error()) {
		t.Fatalf("user row replacement error = %v", err)
	}
	otherRow := mustCreateWorkspaceMemory(t, store, CreateMemoryInput{ID: "other-row", UserID: "user", Body: "other", Origin: "extractor", Evidence: "observed", SubjectProjectID: "other"})
	if _, _, err := store.CreateAndSupersedeExtractorMemory(ctx, otherRow, "user", "current", "frame", "other-new", "changed", "observed"); err == nil || !strings.Contains(err.Error(), ErrAgentMemoryMutationForbidden.Error()) {
		t.Fatalf("cross-project replacement error = %v", err)
	}
}
