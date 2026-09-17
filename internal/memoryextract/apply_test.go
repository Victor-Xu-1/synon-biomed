package memoryextract

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/memoryclassifier"
	"synon-go/internal/memorytools"
	workspace "synon-go/internal/persistence/workspace"
)

type extractionClassifierFunc func(context.Context, []string) ([]memorytools.Classification, error)

func (function extractionClassifierFunc) ClassifyExtractionWrites(ctx context.Context, texts []string) ([]memorytools.Classification, error) {
	return function(ctx, texts)
}

type literalRepairerFunc func(context.Context, string, string) (string, error)

func (function literalRepairerFunc) RepairMemoryReplacement(ctx context.Context, oldText, updatedText string) (string, error) {
	return function(ctx, oldText, updatedText)
}

func TestWorkspaceApplyEmittedUsesExtractorScopesAndSuccessorChains(t *testing.T) {
	store := openExtractionStore(t)
	ctx := context.Background()
	for _, project := range []workspace.CreateProjectInput{{ID: "current", UserID: "user", Name: "Current"}, {ID: "other", UserID: "user", Name: "Other"}} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "current", AgentName: "GENERAL", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	category, err := store.CreateMemoryCategory(ctx, "user", "Methods", "Stable methods", true)
	if err != nil {
		t.Fatal(err)
	}
	createExtractionMemory(t, store, workspace.CreateMemoryInput{ID: "mem_replace", UserID: "user", Body: "Use v4.0.2 at /opt/synon/config.json", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: "current", SourceFrameID: "frame"})
	createExtractionMemory(t, store, workspace.CreateMemoryInput{ID: "mem_remove", UserID: "user", Body: "obsolete", Origin: "extractor", Evidence: "inferred", SubjectProjectID: "current", SourceFrameID: "frame"})
	createExtractionMemory(t, store, workspace.CreateMemoryInput{ID: "mem_user", UserID: "user", Body: "protected", Origin: "user", Evidence: "stated", SubjectProjectID: "current"})
	createExtractionMemory(t, store, workspace.CreateMemoryInput{ID: "mem_other", UserID: "user", Body: "other", Origin: "extractor", Evidence: "observed", SubjectProjectID: "other"})

	var classified [][]string
	classifier := extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		classified = append(classified, append([]string(nil), texts...))
		results := make([]memorytools.Classification, len(texts))
		for index, text := range texts {
			if strings.Contains(text, "flagged") {
				results[index] = memorytools.Classification{Flagged: true, Reason: "classifier"}
			}
		}
		return results, nil
	})
	repairer := literalRepairerFunc(func(_ context.Context, oldText, updatedText string) (string, error) {
		if !strings.Contains(oldText, "v4.0.2") || updatedText != "Use v4.0.3" {
			t.Fatalf("repair input old=%q updated=%q", oldText, updatedText)
		}
		return "Use v4.0.3 (was v4.0.2) at /opt/synon/config.json", nil
	})
	service := NewService(store, classifier, repairer)
	ids := []string{"mem_append", "mem_successor"}
	service.newID = func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}

	result, err := service.Apply(ctx, ApplyScope{UserID: "user", ProjectID: "current", SourceFrameID: "frame"}, Operations{
		Append: []AppendOperation{
			{Text: "durable profile-like fact", Evidence: "stated", Entity: "profile", Category: "Methods"},
			{Text: "flagged external instruction", Evidence: "observed", Entity: "project:current"},
			{Text: "private scratch state", Evidence: "inferred", Entity: "frame"},
			{Text: "cross-project fact", Evidence: "observed", Entity: "project:other"},
		},
		Replace: []ReplaceOperation{
			{ID: "mem_replace", Text: "Use v4.0.3", Evidence: "stated"},
			{ID: "mem_user", Text: "must not change", Evidence: "stated"},
		},
		Remove: []string{"mem_remove", "mem_other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 0 || result.PIUnavailable || result.PIRejected != 1 {
		t.Fatalf("apply result = %#v", result)
	}
	if len(result.Appended) != 1 || result.Appended[0] != "mem_append" || len(result.Replaced) != 1 || result.Replaced[0] != "mem_replace" || len(result.Removed) != 1 || result.Removed[0] != "mem_remove" {
		t.Fatalf("mutation result = %#v", result)
	}
	if len(classified) != 2 || len(classified[0]) != 3 || len(classified[1]) != 1 {
		t.Fatalf("classifier batches = %#v", classified)
	}

	appended, found, err := store.GetMemoryOwned(ctx, "user", "mem_append")
	if err != nil || !found || appended.SubjectProjectID != "current" || appended.CategoryID != category.ID || appended.Origin != "extractor" {
		t.Fatalf("appended memory = %#v, %v, %v", appended, found, err)
	}
	active, found, err := store.ResolveActiveMemoryOwned(ctx, "user", "mem_replace")
	if err != nil || !found || active.ID != "mem_successor" || active.Body != "Use v4.0.3 (was v4.0.2) at /opt/synon/config.json" || active.Origin != "agent_tool" {
		t.Fatalf("replacement head = %#v, %v, %v", active, found, err)
	}
	if _, found, err := store.GetMemoryOwned(ctx, "user", "mem_remove"); err != nil || found {
		t.Fatalf("removed memory still exists = %v, %v", found, err)
	}
	for _, id := range []string{"mem_user", "mem_other"} {
		if _, found, err := store.GetMemoryOwned(ctx, "user", id); err != nil || !found {
			t.Fatalf("protected memory %s = %v, %v", id, found, err)
		}
	}
}

func TestWorkspaceApplyRejectsRepairThatDropsUpdatedLiterals(t *testing.T) {
	store := openExtractionStore(t)
	ctx := context.Background()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "current", UserID: "user", Name: "Current"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "current", AgentName: "GENERAL", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	createExtractionMemory(t, store, workspace.CreateMemoryInput{
		ID: "mem_replace", UserID: "user", Body: "Use build 123456.", Origin: "extractor",
		Evidence: "observed", SubjectProjectID: "current", SourceFrameID: "frame",
	})
	service := NewService(
		store,
		extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
			return make([]memorytools.Classification, len(texts)), nil
		}),
		literalRepairerFunc(func(context.Context, string, string) (string, error) {
			return "Use build 654321 (was 123456).", nil
		}),
	)
	result, err := service.Apply(ctx, ApplyScope{UserID: "user", ProjectID: "current", SourceFrameID: "frame"}, Operations{
		Replace: []ReplaceOperation{{ID: "mem_replace", Text: "Use build 654321 with /new/path/2."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Replaced) != 0 || len(result.Failures) != 1 || result.Failures[0].Error != "literal repair failed validation" {
		t.Fatalf("apply result = %#v", result)
	}
	active, found, err := store.ResolveActiveMemoryOwned(ctx, "user", "mem_replace")
	if err != nil || !found || active.ID != "mem_replace" {
		t.Fatalf("active memory = %#v, found=%v err=%v", active, found, err)
	}
}

func TestWorkspaceExtractorKeepsArtifactSubjectSeparateFromOwningProject(t *testing.T) {
	store := openExtractionStore(t)
	ctx := context.Background()
	for _, project := range []workspace.CreateProjectInput{
		{ID: "current", UserID: "user", Name: "Current"},
		{ID: "other", UserID: "user", Name: "Other"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "current", AgentName: "GENERAL", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.SaveArtifactVersionInput{
		{ArtifactID: "current-artifact", ProjectID: "current", Name: "current.txt", Kind: "text/plain", Content: []byte("current")},
		{ArtifactID: "other-artifact", ProjectID: "other", Name: "other.txt", Kind: "text/plain", Content: []byte("other")},
	} {
		if _, _, err := store.SaveArtifactVersion(input); err != nil {
			t.Fatal(err)
		}
	}
	classifier := extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		return make([]memorytools.Classification, len(texts)), nil
	})
	service := NewService(store, classifier, nil)
	service.newID = func() string { return "artifact-memory" }

	result, err := service.Apply(ctx, ApplyScope{UserID: "user", ProjectID: "current", SourceFrameID: "frame"}, Operations{
		Append: []AppendOperation{
			{Text: "Current artifact durable fact", Entity: "artifact:current-artifact", Evidence: "observed"},
			{Text: "Other artifact durable fact", Entity: "artifact:other-artifact", Evidence: "observed"},
		},
	})
	if err != nil || len(result.Appended) != 1 || result.Appended[0] != "artifact-memory" {
		t.Fatalf("extractor artifact result = %#v, %v", result, err)
	}
	memory, found, err := store.GetMemoryOwned(ctx, "user", "artifact-memory")
	if err != nil || !found || memory.SubjectProjectID != "" || memory.SubjectArtifactID != "current-artifact" {
		t.Fatalf("extracted artifact memory = %#v, %v, %v", memory, found, err)
	}
}

func TestWorkspaceExtractorFallsBackMalformedEntitiesButRejectsValidUnavailableScopes(t *testing.T) {
	store := openExtractionStore(t)
	ctx := context.Background()
	for _, project := range []workspace.CreateProjectInput{
		{ID: "current", UserID: "user", Name: "Current"},
		{ID: "other", UserID: "user", Name: "Other"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "current", AgentName: "GENERAL", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	var classified []string
	classifier := extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		classified = append(classified, texts...)
		return make([]memorytools.Classification, len(texts)), nil
	})
	service := NewService(store, classifier, nil)
	ids := []string{"fallback-unknown", "fallback-malformed"}
	service.newID = func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}

	result, err := service.Apply(ctx, ApplyScope{UserID: "user", ProjectID: "current", SourceFrameID: "frame"}, Operations{
		Append: []AppendOperation{
			{Text: "Unknown entity syntax falls back", Entity: "workspace", Evidence: "observed"},
			{Text: "Malformed project entity falls back", Entity: "project:other project", Evidence: "observed"},
			{Text: "Syntactically valid missing project is skipped", Entity: "project:missing", Evidence: "observed"},
			{Text: "Syntactically valid cross project is skipped", Entity: "project:other", Evidence: "observed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(classified) != 4 {
		t.Fatalf("classified append texts = %#v", classified)
	}
	if len(result.Appended) != 2 || result.Appended[0] != "fallback-unknown" || result.Appended[1] != "fallback-malformed" {
		t.Fatalf("extractor entity fallback result = %#v", result)
	}
	for _, id := range result.Appended {
		memory, found, err := store.GetMemoryOwned(ctx, "user", id)
		if err != nil || !found || memory.SubjectProjectID != "current" || memory.SubjectArtifactID != "" {
			t.Fatalf("fallback memory %s = %#v, %v, %v", id, memory, found, err)
		}
	}
}

func TestWorkspaceAppendPIUnavailableHoldsWholeApplyBatch(t *testing.T) {
	store := openExtractionStore(t)
	ctx := context.Background()
	prepareExtractionScope(t, store)
	createExtractionMemory(t, store, workspace.CreateMemoryInput{ID: "mem_remove", UserID: "user", Body: "keep on retry", Origin: "extractor", Evidence: "observed", SubjectProjectID: "project", SourceFrameID: "frame"})
	service := NewService(store, extractionClassifierFunc(func(context.Context, []string) ([]memorytools.Classification, error) {
		return nil, &memoryclassifier.UnavailableError{Cause: errors.New("auth")}
	}), nil)
	result, err := service.Apply(ctx, ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"}, Operations{
		Append: []AppendOperation{{Text: "new durable fact", Evidence: "observed"}},
		Remove: []string{"mem_remove"},
	})
	if err != nil || !result.PIUnavailable || len(result.Appended)+len(result.Removed)+len(result.Replaced) != 0 {
		t.Fatalf("PI-unavailable result = %#v, %v", result, err)
	}
	if _, found, err := store.GetMemoryOwned(ctx, "user", "mem_remove"); err != nil || !found {
		t.Fatalf("remove ran despite held batch = %v, %v", found, err)
	}
}

func TestWorkspaceReplacementPIUnavailableKeepsPredecessorButDoesNotHoldBatch(t *testing.T) {
	store := openExtractionStore(t)
	ctx := context.Background()
	prepareExtractionScope(t, store)
	createExtractionMemory(t, store, workspace.CreateMemoryInput{ID: "mem_replace", UserID: "user", Body: "stable value", Origin: "extractor", Evidence: "observed", SubjectProjectID: "project", SourceFrameID: "frame"})
	createExtractionMemory(t, store, workspace.CreateMemoryInput{ID: "mem_remove", UserID: "user", Body: "obsolete", Origin: "extractor", Evidence: "observed", SubjectProjectID: "project", SourceFrameID: "frame"})
	service := NewService(store, extractionClassifierFunc(func(context.Context, []string) ([]memorytools.Classification, error) {
		return nil, &memoryclassifier.UnavailableError{Cause: errors.New("timeout")}
	}), nil)
	result, err := service.Apply(ctx, ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"}, Operations{
		Replace: []ReplaceOperation{{ID: "mem_replace", Text: "updated stable value", Evidence: "observed"}},
		Remove:  []string{"mem_remove"},
	})
	if err != nil || result.PIUnavailable || len(result.Replaced) != 0 || len(result.Removed) != 1 || len(result.Failures) != 1 {
		t.Fatalf("replacement unavailable result = %#v, %v", result, err)
	}
	active, found, err := store.ResolveActiveMemoryOwned(ctx, "user", "mem_replace")
	if err != nil || !found || active.ID != "mem_replace" {
		t.Fatalf("predecessor changed = %#v, %v, %v", active, found, err)
	}
}

func openExtractionStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func prepareExtractionScope(t *testing.T, store *workspace.Store) {
	t.Helper()
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "user", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "GENERAL", Status: "completed", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
}

func createExtractionMemory(t *testing.T, store *workspace.Store, input workspace.CreateMemoryInput) workspace.Memory {
	t.Helper()
	memory, err := store.CreateMemoryOwned(context.Background(), input, input.UserID)
	if err != nil {
		t.Fatal(err)
	}
	return memory
}
