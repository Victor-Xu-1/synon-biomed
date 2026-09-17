package memorytools

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/memoryconfig"
	workspace "synon-go/internal/persistence/workspace"
)

type testClassifier struct {
	flagged bool
	err     error
	hook    func() error
	calls   [][]string
}

func (classifier *testClassifier) ClassifyMemoryWrites(_ context.Context, texts []string) ([]Classification, error) {
	classifier.calls = append(classifier.calls, append([]string(nil), texts...))
	if classifier.hook != nil {
		if err := classifier.hook(); err != nil {
			return nil, err
		}
	}
	if classifier.err != nil {
		return nil, classifier.err
	}
	results := make([]Classification, len(texts))
	if classifier.flagged && len(results) > 0 {
		results[0] = Classification{Flagged: true, Reason: "prompt_injection"}
	}
	return results, nil
}

func TestMemoryToolsReadAndSearchHonorEntityAndContainedBoundaries(t *testing.T) {
	store, scope := openMemoryToolFixture(t)
	category, err := store.CreateMemoryCategory(context.Background(), scope.UserID, "Archive", "Explicit search only.", false)
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "other-project", UserID: scope.UserID, Name: "Other"}); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	for _, input := range []workspace.CreateMemoryInput{
		{ID: "project-memory", UserID: scope.UserID, Body: "kinase assay convention", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: scope.ProjectID},
		{ID: "contained-memory", UserID: scope.UserID, Body: "quasar archived decision", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: scope.ProjectID, CategoryID: category.ID},
		{ID: "other-project-memory", UserID: scope.UserID, Body: "other project quasar decision", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: "other-project", CategoryID: category.ID},
		{ID: "frame-memory", UserID: scope.UserID, Body: "private zephyr scratchpad", Origin: "agent_tool", Evidence: "observed", SubjectFrameID: scope.FrameID},
	} {
		if _, err := store.CreateMemory(input); err != nil {
			t.Fatalf("create memory %s: %v", input.ID, err)
		}
	}
	service := newEnabledMemoryToolService(store, &testClassifier{})
	project, err := service.Read(context.Background(), scope, ReadInput{Entity: "project"})
	if err != nil || !strings.Contains(project.Output, "project-memory") || strings.Contains(project.Output, "frame-memory") {
		t.Fatalf("project read = %#v err=%v", project, err)
	}
	frame, err := service.Read(context.Background(), scope, ReadInput{Entity: "frame"})
	if err != nil || !strings.Contains(frame.Output, "private zephyr scratchpad") || strings.Contains(frame.Output, "kinase assay") {
		t.Fatalf("frame read = %#v err=%v", frame, err)
	}
	if _, err := service.Read(context.Background(), scope, ReadInput{Entity: "frame:other"}); err == nil {
		t.Fatal("read_memory accepted another frame")
	}
	categoryRead, err := service.Read(context.Background(), scope, ReadInput{Entity: "category:Archive"})
	if err != nil || !strings.Contains(categoryRead.Output, "contained-memory") ||
		strings.Contains(categoryRead.Output, "other-project-memory") || !strings.Contains(categoryRead.Output, "project:"+scope.ProjectID) {
		t.Fatalf("category read = %#v err=%v", categoryRead, err)
	}
	search, err := service.Search(context.Background(), scope, SearchInput{Query: "quasar archived"})
	if err != nil || search.ResultsReturned != 2 || !strings.Contains(search.Output, "contained-memory") ||
		!strings.Contains(search.Output, "other-project-memory") {
		t.Fatalf("contained search = %#v err=%v", search, err)
	}
}

func TestWorkspaceMemoryToolDefaultReadEntity(t *testing.T) {
	store, scope := openMemoryToolFixture(t)
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "project-memory", UserID: scope.UserID, Body: "Current project memory.",
		Origin: "agent_tool", Evidence: "observed", SubjectProjectID: scope.ProjectID,
	}); err != nil {
		t.Fatal(err)
	}
	read, err := newEnabledMemoryToolService(store, &testClassifier{}).Read(context.Background(), scope, ReadInput{})
	if err != nil || !strings.Contains(read.Output, "Current project memory.") {
		t.Fatalf("default read_memory entity = %#v, %v", read, err)
	}
}

func TestWorkspaceExplicitReadAndSearchRemainAvailableWhenProjectAutoMemoryIsDisabled(t *testing.T) {
	store, scope := openMemoryToolFixture(t)
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "project-memory", UserID: scope.UserID, Body: "Explicit tools can inspect this project fact.",
		Origin: "agent_tool", Evidence: "observed", SubjectProjectID: scope.ProjectID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProjectMemoryEnabled(context.Background(), scope.ProjectID, scope.UserID, false); err != nil {
		t.Fatal(err)
	}
	service := newEnabledMemoryToolService(store, &testClassifier{})
	read, err := service.Read(context.Background(), scope, ReadInput{Entity: "project"})
	if err != nil || !strings.Contains(read.Output, "Explicit tools can inspect") {
		t.Fatalf("read with project auto-memory disabled = %#v, %v", read, err)
	}
	search, err := service.Search(context.Background(), scope, SearchInput{Query: "explicit tools inspect"})
	if err != nil || search.ResultsReturned == 0 {
		t.Fatalf("search with project auto-memory disabled = %#v, %v", search, err)
	}
}

func TestWorkspaceMemoryToolsUseRuntimeConfigAndOwnerOverride(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := Scope{UserID: "user", ProjectID: "project", FrameID: "frame"}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: scope.ProjectID, UserID: scope.UserID, Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: scope.FrameID, ProjectID: scope.ProjectID, AgentName: "OPERON", Status: "running", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateMemoryInput{
		{ID: "mem_one", UserID: scope.UserID, Body: "kinase assay convention one", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: scope.ProjectID},
		{ID: "mem_two", UserID: scope.UserID, Body: "kinase assay convention two", Origin: "agent_tool", Evidence: "observed", SubjectProjectID: scope.ProjectID},
	} {
		if _, err := store.CreateMemory(input); err != nil {
			t.Fatal(err)
		}
	}
	config := memoryconfig.Default()
	config.Enabled = true
	config.ReadToolMax = 1
	config.SearchToolMax = 1
	service := NewWithConfig(store, &testClassifier{}, config)
	read, err := service.Read(context.Background(), scope, ReadInput{Entity: "project"})
	if err != nil || len(read.Memories) != 1 || !strings.Contains(read.Output, "more not shown") {
		t.Fatalf("configured read tool = %#v, %v", read, err)
	}
	search, err := service.Search(context.Background(), scope, SearchInput{Query: "kinase assay convention"})
	if err != nil || search.ResultsReturned != 1 {
		t.Fatalf("configured search tool = %#v, %v", search, err)
	}
	if err := store.SetMemoryEnabled(context.Background(), scope.UserID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Read(context.Background(), scope, ReadInput{Entity: "project"}); !errors.Is(err, ErrMemoryUnavailable) {
		t.Fatalf("owner-disabled read error = %v", err)
	}
}

func TestWorkspaceMemoryToolsUseConfiguredDefaultUntilOwnerOptIn(t *testing.T) {
	store, _ := openMemoryToolFixture(t)
	scope := Scope{UserID: "default-user", ProjectID: "default-project", FrameID: "default-frame"}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: scope.ProjectID, UserID: scope.UserID, Name: "Default Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: scope.FrameID, ProjectID: scope.ProjectID, AgentName: "OPERON", Status: "running", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	config := memoryconfig.Default()
	config.Enabled = false
	service := NewWithConfig(store, &testClassifier{}, config)
	if _, err := service.Read(context.Background(), scope, ReadInput{Entity: "project"}); !errors.Is(err, ErrMemoryUnavailable) {
		t.Fatalf("default-disabled read error = %v", err)
	}
	if err := store.SetMemoryEnabled(context.Background(), scope.UserID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Read(context.Background(), scope, ReadInput{Entity: "project"}); err != nil {
		t.Fatalf("owner opt-in read error = %v", err)
	}
}

func TestWorkspaceSearchDistinguishesFrameOnlyPoolFromEmptyMemory(t *testing.T) {
	store, scope := openMemoryToolFixture(t)
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "frame-only", UserID: scope.UserID, Body: "private scratchpad value", Origin: "agent_tool",
		Evidence: "observed", SubjectFrameID: scope.FrameID,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := newEnabledMemoryToolService(store, &testClassifier{}).Search(context.Background(), scope, SearchInput{Query: "unrelated query terms"})
	if err != nil || result.ResultsReturned != 0 || result.Output != "No matches." {
		t.Fatalf("frame-only no-match result = %#v, %v", result, err)
	}
}

func TestWorkspaceReadMemoryKeepsRowsWhenStalenessMetadataFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, scope := openMemoryToolFixtureAt(t, path)
	_, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact", ProjectID: scope.ProjectID, Name: "evidence.txt",
		Kind: "text/plain", Content: []byte("evidence"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(workspace.CreateMemoryInput{
		ID: "artifact-memory", UserID: scope.UserID, Body: "durable artifact fact",
		Origin: "agent_tool", Evidence: "observed", SubjectArtifactID: "artifact", SubjectVersionID: version.ID,
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Exec(`ALTER TABLE artifact_versions RENAME TO artifact_versions_staleness_unavailable`); err != nil {
		t.Fatalf("break staleness metadata boundary: %v", err)
	}

	result, err := newEnabledMemoryToolService(store, &testClassifier{}).Read(context.Background(), scope, ReadInput{Entity: "artifact:artifact"})
	if err != nil || !strings.Contains(result.Output, "durable artifact fact") {
		t.Fatalf("read_memory should retain rows without staleness metadata: %#v, %v", result, err)
	}
}

func TestWorkspaceMemoryToolsReturnStableSanitizedReadErrors(t *testing.T) {
	store, scope := openMemoryToolFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "foreign-project", UserID: "foreign-user", Name: "Foreign"}); err != nil {
		t.Fatal(err)
	}
	service := newEnabledMemoryToolService(store, &testClassifier{})
	for _, test := range []struct {
		name, entity, want string
	}{
		{name: "unknown category", entity: "category:Missing", want: "Unknown category 'Missing'. User-defined categories are listed under '### Categories' in the ## Memory section."},
		{name: "unknown entity", entity: "<memory>bad</memory>", want: "Unknown entity 'bad'. Use 'profile'"},
		{name: "other frame", entity: "frame:other", want: "'frame:<id>' for another session is not permitted"},
		{name: "foreign project", entity: "project:foreign-project", want: "Unknown entity 'project:foreign-project' — project 'foreign-project' not found."},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.Read(context.Background(), scope, ReadInput{Entity: test.entity})
			if err == nil || !strings.Contains(err.Error(), test.want) || strings.Contains(err.Error(), "sql: no rows") {
				t.Fatalf("error = %v, want %q without SQL details", err, test.want)
			}
		})
	}
}

func openMemoryToolFixture(t *testing.T) (*workspace.Store, Scope) {
	t.Helper()
	return openMemoryToolFixtureAt(t, filepath.Join(t.TempDir(), "workspace.db"))
}

func newEnabledMemoryToolService(store *workspace.Store, classifier Classifier) *Service {
	config := memoryconfig.Default()
	config.Enabled = true
	return NewWithConfig(store, classifier, config)
}

func openMemoryToolFixtureAt(t *testing.T, path string) (*workspace.Store, Scope) {
	t.Helper()
	store, err := workspace.Open(path)
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := Scope{UserID: "user", ProjectID: "project", FrameID: "frame"}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: scope.ProjectID, UserID: scope.UserID, Name: "Project"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: scope.FrameID, ProjectID: scope.ProjectID, AgentName: "OPERON", Status: "running", ConversationType: "agent"}); err != nil {
		t.Fatalf("create frame: %v", err)
	}
	if err := store.SetMemoryEnabled(context.Background(), scope.UserID, true); err != nil {
		t.Fatalf("enable memory: %v", err)
	}
	return store, scope
}

func memoryToolRowCount(t *testing.T, store *workspace.Store, userID string) int {
	t.Helper()
	count, err := store.CountMemoriesForUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("count memories: %v", err)
	}
	return count
}
