package memoryextract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"synon-go/internal/memoryclassifier"
	"synon-go/internal/memorytools"
)

type extractorFunc func(context.Context, ExtractRequest) (map[string]any, error)

func (function extractorFunc) ExtractMemories(ctx context.Context, request ExtractRequest) (map[string]any, error) {
	return function(ctx, request)
}

func TestWorkspaceExtractionRunnerPersistsAndAdvancesCursor(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
		t.Fatal(err)
	}
	classifier := extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		return make([]memorytools.Classification, len(texts)), nil
	})
	service := NewService(store, classifier, nil)
	service.newID = func() string { return "mem_extracted" }
	var request ExtractRequest
	runner := NewRunner(store, service, extractorFunc(func(_ context.Context, input ExtractRequest) (map[string]any, error) {
		request = input
		return map[string]any{"append": []any{map[string]any{"text": "durable project decision", "evidence": "stated"}}}, nil
	}))
	messages := []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "please preserve this decision"}}}}
	result, err := runner.Run(context.Background(), RunInput{
		Scope:    ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"},
		Messages: messages, FinalResponseText: "Decision confirmed.", Config: DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate != "done" || !result.CursorAdvanced || result.CursorAfter != 1 || len(result.Applied.Appended) != 1 {
		t.Fatalf("run result = %#v", result)
	}
	if request.Mode != "forked" || request.MaxTokens != 10240 || request.Deadline <= 0 || !strings.Contains(request.Prompt, "Decision confirmed.") {
		t.Fatalf("extract request = %#v", request)
	}
	memory, found, err := store.GetMemoryOwned(context.Background(), "user", "mem_extracted")
	if err != nil || !found || memory.SubjectProjectID != "project" || memory.Origin != "extractor" {
		t.Fatalf("extracted memory = %#v, %v, %v", memory, found, err)
	}
}

func TestWorkspaceExtractionRunnerUsesConfiguredEnabledDefaultAndOwnerOverride(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	service := NewService(store, extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		return make([]memorytools.Classification, len(texts)), nil
	}), nil)
	calls := 0
	runner := NewRunner(store, service, extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
		calls++
		return map[string]any{}, nil
	}))
	config := DefaultConfig()
	config.Enabled = true
	input := RunInput{
		Scope:    ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"},
		Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "capture this durable preference"}}}},
		Config:   config,
	}
	result, err := runner.Run(context.Background(), input)
	if err != nil || result.Gate != "done" || calls != 1 {
		t.Fatalf("configured enabled extraction = %#v, calls=%d, err=%v", result, calls, err)
	}
	if err := store.SetMemoryEnabled(context.Background(), "user", false); err != nil {
		t.Fatal(err)
	}
	result, err = runner.Run(context.Background(), input)
	if err != nil || result.Gate != "disabled" || calls != 1 {
		t.Fatalf("owner-disabled extraction = %#v, calls=%d, err=%v", result, calls, err)
	}
}

func TestWorkspaceExtractionRunnerGatesAndAdvancesOnlyAtSourceBackedPoints(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		return make([]memorytools.Classification, len(texts)), nil
	}), nil)
	modelCalls := 0
	runner := NewRunner(store, service, extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
		modelCalls++
		return map[string]any{}, nil
	}))
	scope := ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"}

	aside, err := runner.Run(context.Background(), RunInput{
		Scope: scope, AsideParent: true,
		Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "remember this aside finding"}}}},
		Config:   DefaultConfig(),
	})
	if err != nil || aside.Gate != "disabled-aside" || aside.CursorAfter != 0 {
		t.Fatalf("aside result = %#v, %v", aside, err)
	}

	noProse, err := runner.Run(context.Background(), RunInput{Scope: scope, Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "only two"}}}}, Config: DefaultConfig()})
	if err != nil || noProse.Gate != "no-prose" || noProse.CursorAfter != 0 {
		t.Fatalf("no-prose result = %#v, %v", noProse, err)
	}
	agentWroteMessages := []Message{
		{Role: "user", Content: []Block{{Type: BlockText, Text: "please update durable memory"}}},
		{Role: "assistant", Content: []Block{{
			Type: BlockToolUse, ToolName: "write_memory", ToolUseID: "write",
			ToolInput: map[string]any{"replace": []any{map[string]any{"id": "mem"}}},
		}}},
	}
	agentWrote, err := runner.Run(context.Background(), RunInput{Scope: scope, Messages: agentWroteMessages, Config: DefaultConfig()})
	if err != nil || agentWrote.Gate != "agent-wrote" || agentWrote.CursorAfter != len(agentWroteMessages) {
		t.Fatalf("agent-wrote result = %#v, %v", agentWrote, err)
	}
	if modelCalls != 0 {
		t.Fatalf("model calls after gates = %d", modelCalls)
	}
}

func TestWorkspaceExtractionRunnerRepairsCursorOvershoot(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.SetLastExtractMessageIndex(context.Background(), "frame", 9, nil); err != nil || !changed {
		t.Fatalf("seed cursor = %v, %v", changed, err)
	}
	service := NewService(store, extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		return make([]memorytools.Classification, len(texts)), nil
	}), nil)
	runner := NewRunner(store, service, extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
		return map[string]any{}, nil
	}))
	messages := []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "rescan the complete history"}}}, {Role: "assistant", Content: []Block{{Type: BlockText, Text: "done"}}}}
	result, err := runner.Run(context.Background(), RunInput{Scope: ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"}, Messages: messages, Config: DefaultConfig()})
	if err != nil || result.Gate != "done" || !result.Overshoot || result.CursorBefore != 9 || result.CursorAfter != 2 {
		t.Fatalf("overshoot result = %#v, %v", result, err)
	}
}

func TestWorkspaceExtractionRunnerHoldsCursorWhenAppendClassifierUnavailable(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, extractionClassifierFunc(func(context.Context, []string) ([]memorytools.Classification, error) {
		return nil, &memoryclassifier.UnavailableError{Cause: errors.New("auth")}
	}), nil)
	runner := NewRunner(store, service, extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
		return map[string]any{"append": []any{map[string]any{"text": "durable new finding", "evidence": "observed"}}}, nil
	}))
	result, err := runner.Run(context.Background(), RunInput{
		Scope:    ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"},
		Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "remember this durable finding"}}}}, Config: DefaultConfig(),
	})
	if err != nil || result.Gate != "pi-unavailable" || result.CursorAfter != 0 || result.CursorAdvanced {
		t.Fatalf("PI-unavailable runner result = %#v, %v", result, err)
	}
}

func TestWorkspaceExtractionRunnerHoldsCursorAndSwallowsExtractorFailure(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		return make([]memorytools.Classification, len(texts)), nil
	}), nil)
	runner := NewRunner(store, service, extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
		return nil, errors.New("provider unavailable")
	}))

	result, err := runner.Run(context.Background(), RunInput{
		Scope: ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"},
		Messages: []Message{{Role: "user", Content: []Block{{
			Type: BlockText, Text: "retain this durable project decision",
		}}}},
		Config: DefaultConfig(),
	})
	if err != nil || result.Gate != "extractor-failed" || result.ExtractorError != "provider unavailable" || result.CursorAdvanced || result.CursorAfter != 0 {
		t.Fatalf("extractor failure result = %#v, err=%v", result, err)
	}
	if count, err := store.CountMemoriesForUser(context.Background(), "user"); err != nil || count != 0 {
		t.Fatalf("failed extractor persisted %d rows, err=%v", count, err)
	}
}

func TestWorkspaceExtractionRunnerCadenceIsPerProjectAndHoldsCursor(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		return make([]memorytools.Classification, len(texts)), nil
	}), nil)
	calls := 0
	runner := NewRunner(store, service, extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
		calls++
		return map[string]any{}, nil
	}))
	config := DefaultConfig()
	config.ExtractEveryN = 2
	input := RunInput{Scope: ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"}, Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "capture this durable preference"}}}}, Config: config}
	first, err := runner.Run(context.Background(), input)
	if err != nil || first.Gate != "cadence" || first.CursorAfter != 0 {
		t.Fatalf("first cadence result = %#v, %v", first, err)
	}
	second, err := runner.Run(context.Background(), input)
	if err != nil || second.Gate != "done" || second.CursorAfter != 1 || calls != 1 {
		t.Fatalf("second cadence result = %#v, calls=%d, err=%v", second, calls, err)
	}
}

func TestWorkspaceExtractionCadenceSurvivesPerScopeRunnerReconstruction(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
		return make([]memorytools.Classification, len(texts)), nil
	}), nil)
	calls := 0
	model := extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
		calls++
		return map[string]any{}, nil
	})
	cadence := NewCadence()
	config := DefaultConfig()
	config.ExtractEveryN = 2
	input := RunInput{
		Scope: ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"},
		Messages: []Message{{Role: "user", Content: []Block{{
			Type: BlockText, Text: "capture this durable cross-runner preference",
		}}}},
		Config: config,
	}

	first, err := NewRunnerWithCadence(store, service, model, cadence).Run(context.Background(), input)
	if err != nil || first.Gate != "cadence" {
		t.Fatalf("first reconstructed runner result = %#v, %v", first, err)
	}
	second, err := NewRunnerWithCadence(store, service, model, cadence).Run(context.Background(), input)
	if err != nil || second.Gate != "done" || calls != 1 {
		t.Fatalf("second reconstructed runner result = %#v, calls=%d, err=%v", second, calls, err)
	}
}

func TestWorkspaceExtractionRunnerHonorsExplicitZeroMaxPerRun(t *testing.T) {
	store := openExtractionStore(t)
	prepareExtractionScope(t, store)
	if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, nil, nil)
	runner := NewRunner(store, service, extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
		return map[string]any{"append": []any{map[string]any{"text": "must be capped", "evidence": "stated"}}}, nil
	}))
	config := DefaultConfig()
	config.ExtractMax = 0
	result, err := runner.Run(context.Background(), RunInput{
		Scope:    ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"},
		Messages: []Message{{Role: "user", Content: []Block{{Type: BlockText, Text: "capture this durable preference"}}}},
		Config:   config,
	})
	if err != nil || result.Gate != "done" || !result.CursorAdvanced || len(result.Applied.Appended) != 0 {
		t.Fatalf("zero-cap extraction result = %#v, err=%v", result, err)
	}
	if count, err := store.CountMemoriesForUser(context.Background(), "user"); err != nil || count != 0 {
		t.Fatalf("zero-cap extraction persisted %d rows, err=%v", count, err)
	}
}

func TestWorkspaceExtractionFiltersRecalledBodiesOnlyInForkedMode(t *testing.T) {
	const body = "CRBN binding evidence was already recalled from durable memory."
	for _, test := range []struct {
		mode          string
		wantPersisted int
	}{
		{mode: "forked", wantPersisted: 0},
		{mode: "compact", wantPersisted: 1},
	} {
		t.Run(test.mode, func(t *testing.T) {
			store := openExtractionStore(t)
			prepareExtractionScope(t, store)
			if err := store.SetMemoryEnabled(context.Background(), "user", true); err != nil {
				t.Fatal(err)
			}
			service := NewService(store, extractionClassifierFunc(func(_ context.Context, texts []string) ([]memorytools.Classification, error) {
				return make([]memorytools.Classification, len(texts)), nil
			}), nil)
			service.newID = func() string { return "mem_" + test.mode }
			runner := NewRunner(store, service, extractorFunc(func(context.Context, ExtractRequest) (map[string]any, error) {
				return map[string]any{"append": []any{map[string]any{"text": body, "evidence": "observed"}}}, nil
			}))
			config := DefaultConfig()
			config.ExtractMode = test.mode
			result, err := runner.Run(context.Background(), RunInput{
				Scope: ApplyScope{UserID: "user", ProjectID: "project", SourceFrameID: "frame"},
				Messages: []Message{{Role: "user", Content: []Block{{
					Type: BlockText, Text: "preserve the durable CRBN binding evidence",
				}}}},
				RecalledBodies: []string{body}, Config: config,
			})
			if err != nil || result.Gate != "done" || !result.CursorAdvanced {
				t.Fatalf("%s result = %#v, err=%v", test.mode, result, err)
			}
			count, err := store.CountMemoriesForUser(context.Background(), "user")
			if err != nil || count != test.wantPersisted {
				t.Fatalf("%s persisted %d rows, want %d, err=%v", test.mode, count, test.wantPersisted, err)
			}
		})
	}
}
