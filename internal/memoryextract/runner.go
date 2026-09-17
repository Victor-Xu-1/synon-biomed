package memoryextract

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"synon-go/internal/memoryconfig"
	"synon-go/internal/memorypolicy"
	workspace "synon-go/internal/persistence/workspace"
)

type Extractor interface {
	ExtractMemories(context.Context, ExtractRequest) (map[string]any, error)
}

type ExtractRequest struct {
	Mode       string
	Prompt     string
	Transcript string
	Messages   []Message
	Delta      []Message
	SchemaJSON string
	Model      string
	MaxTokens  int
	Deadline   time.Duration
}

type Config struct {
	Enabled        bool
	ExtractEnabled bool
	ExtractMode    string
	ExtractEveryN  int
	ExtractMax     int
	ExtractDelta   bool
}

func DefaultConfig() Config {
	source := memoryconfig.Default()
	return ConfigFromMemoryConfig(source)
}

func ConfigFromMemoryConfig(source memoryconfig.Config) Config {
	mode := source.ExtractMode
	if mode == "haiku" {
		mode = "compact"
	}
	return Config{
		Enabled:        source.Enabled,
		ExtractEnabled: source.ExtractEnabled,
		ExtractMode:    mode,
		ExtractEveryN:  source.ExtractEveryN,
		ExtractMax:     source.ExtractMaxPerRun,
		ExtractDelta:   source.ExtractDelta,
	}
}

type RunInput struct {
	Scope             ApplyScope
	AsideParent       bool
	SessionMemoryMode string
	Model             string
	Messages          []Message
	FinalResponseText string
	RecalledBodies    []string
	Config            Config
}

type RunResult struct {
	Gate           string
	CursorBefore   int
	CursorAfter    int
	CursorAdvanced bool
	CursorError    string
	ExtractorError string
	Overshoot      bool
	DeltaCount     int
	Applied        ApplyResult
}

type Runner struct {
	store   *workspace.Store
	service *Service
	model   Extractor
	cadence *Cadence
}

// Cadence owns process-lifetime extraction counters. A runtime shares one
// Cadence across per-scope Runner instances so extract_every_n remains stable
// without leaking counters across independent Server values.
type Cadence struct {
	mu       sync.Mutex
	projects map[string]int
}

func NewRunner(store *workspace.Store, service *Service, model Extractor) *Runner {
	return NewRunnerWithCadence(store, service, model, NewCadence())
}

func NewCadence() *Cadence {
	return &Cadence{projects: make(map[string]int)}
}

func NewRunnerWithCadence(store *workspace.Store, service *Service, model Extractor, cadence *Cadence) *Runner {
	if cadence == nil {
		cadence = NewCadence()
	}
	return &Runner{store: store, service: service, model: model, cadence: cadence}
}

func (r *Runner) Run(ctx context.Context, input RunInput) (RunResult, error) {
	result := RunResult{Gate: "start"}
	if r == nil || r.store == nil || r.service == nil {
		return result, errors.New("memory extraction runner is unavailable")
	}
	input.Scope.UserID = strings.TrimSpace(input.Scope.UserID)
	input.Scope.ProjectID = strings.TrimSpace(input.Scope.ProjectID)
	input.Scope.SourceFrameID = strings.TrimSpace(input.Scope.SourceFrameID)
	if input.Scope.UserID == "" {
		result.Gate = "no-userId"
		return result, nil
	}
	if input.AsideParent {
		result.Gate = "disabled-aside"
		return result, nil
	}
	if input.SessionMemoryMode == "off" {
		result.Gate = "disabled-session"
		return result, nil
	}
	config := normalizeConfig(input.Config)
	enabled, err := r.store.MemoryEnabledWithDefault(ctx, input.Scope.UserID, config.Enabled)
	if err != nil {
		return result, err
	}
	if !enabled {
		result.Gate = "disabled"
		return result, nil
	}
	if !config.ExtractEnabled {
		result.Gate = "disabled"
		return result, nil
	}
	if input.Scope.ProjectID != "" {
		projectEnabled, err := r.store.ProjectMemoryEnabledSetting(ctx, input.Scope.ProjectID, input.Scope.UserID)
		if err != nil {
			return result, err
		}
		if projectEnabled != nil && !*projectEnabled {
			result.Gate = "disabled"
			return result, nil
		}
	}
	if len(input.Messages) == 0 {
		result.Gate = "no-messages"
		return result, nil
	}

	cursor, err := r.store.LastExtractMessageIndex(ctx, input.Scope.SourceFrameID)
	if err != nil {
		return result, err
	}
	result.CursorBefore = cursor
	result.CursorAfter = cursor
	result.Overshoot = config.ExtractDelta && cursor > len(input.Messages)
	start := 0
	if config.ExtractDelta && !result.Overshoot {
		start = cursor
	}
	delta := input.Messages[start:]
	result.DeltaCount = len(delta)
	if len(delta) == 0 {
		result.Gate = "no-delta"
		return result, nil
	}
	if !HasUserProseSince(delta) {
		result.Gate = "no-prose"
		return result, nil
	}
	if HasMemoryWritesSince(delta, config.ExtractMax) {
		result.Gate = "agent-wrote"
		r.advanceCursor(ctx, input.Scope.SourceFrameID, len(input.Messages), cursor, result.Overshoot, config.ExtractDelta, &result)
		return result, nil
	}
	if config.ExtractEveryN > 1 && !r.cadencePass(input.Scope.ProjectID, config.ExtractEveryN) {
		result.Gate = "cadence"
		return result, nil
	}
	if r.model == nil {
		return result, errors.New("memory extraction model is unavailable")
	}

	memories, err := r.store.ListMemoryExtractionRows(ctx, input.Scope.UserID, input.Scope.ProjectID)
	if err != nil {
		return result, err
	}
	categories, err := r.store.ListMemoryCategories(ctx, input.Scope.UserID)
	if err != nil {
		return result, err
	}
	manifest := ManifestRows(memories)
	promptCategories := make([]Category, 0, len(categories))
	for _, category := range categories {
		promptCategories = append(promptCategories, Category{Name: category.Name, Guidance: category.Guidance})
	}
	prompt, err := BuildExtractPrompt(PromptOptions{
		Mode: config.ExtractMode, MaxPerKind: config.ExtractMax, Manifest: manifest,
		Categories: promptCategories, FinalResponseText: input.FinalResponseText,
	})
	if err != nil {
		return result, err
	}
	maxTokens := memorypolicy.ExtractionMaxTokens
	if config.ExtractMode == "forked" {
		maxTokens = memorypolicy.ExtractionForkedMaxTokens
	}
	request := ExtractRequest{
		Mode: config.ExtractMode, Prompt: prompt,
		Transcript: DigestTranscript(ProjectCompactMessages(delta), memorypolicy.ExtractionTranscriptMaxUTF16Units),
		Messages:   append([]Message(nil), input.Messages...), Delta: append([]Message(nil), delta...),
		SchemaJSON: EmitSchemaJSON(), MaxTokens: maxTokens,
		Deadline: memorypolicy.ExtractionDeadline, Model: strings.TrimSpace(input.Model),
	}
	modelCtx, cancel := context.WithTimeout(ctx, request.Deadline)
	defer cancel()
	raw, err := r.model.ExtractMemories(modelCtx, request)
	if err != nil {
		result.Gate = "extractor-failed"
		// Extraction is best-effort background work:
		// preserve diagnostics and the cursor, but never fail the user turn.
		result.ExtractorError = err.Error()
		return result, nil
	}
	operations := ParseEmitted(raw, KnownMutableManifestIDs(manifest), config.ExtractMax)
	if config.ExtractMode == "forked" && (len(operations.Append) > 0 || len(operations.Replace) > 0) {
		operations = DropRecalledOperations(operations, input.RecalledBodies)
		operations = DropMatchingOperations(operations, CollectServerToolStubBodies(input.Messages))
	}
	applied, err := r.service.Apply(ctx, input.Scope, operations)
	result.Applied = applied
	if err != nil {
		return result, err
	}
	if applied.PIUnavailable {
		result.Gate = "pi-unavailable"
		return result, nil
	}
	r.advanceCursor(ctx, input.Scope.SourceFrameID, len(input.Messages), cursor, result.Overshoot, config.ExtractDelta, &result)
	result.Gate = "done"
	return result, nil
}

func normalizeConfig(config Config) Config {
	if strings.TrimSpace(config.ExtractMode) == "" {
		config.ExtractMode = memorypolicy.ExtractModeDefault
	}
	return config
}

func (r *Runner) cadencePass(projectID string, every int) bool {
	if every <= 1 {
		return true
	}
	if r.cadence == nil {
		r.cadence = NewCadence()
	}
	r.cadence.mu.Lock()
	defer r.cadence.mu.Unlock()
	next := r.cadence.projects[projectID] + 1
	r.cadence.projects[projectID] = next
	return next%every == 0
}

func (r *Runner) advanceCursor(ctx context.Context, frameID string, next, previous int, overshoot, enabled bool, result *RunResult) {
	if !enabled {
		return
	}
	var expected *int
	if overshoot {
		expected = &previous
	}
	changed, err := r.store.SetLastExtractMessageIndex(ctx, frameID, next, expected)
	if err != nil {
		result.CursorError = err.Error()
		return
	}
	result.CursorAdvanced = changed
	if current, readErr := r.store.LastExtractMessageIndex(ctx, frameID); readErr == nil {
		result.CursorAfter = current
	} else {
		result.CursorError = readErr.Error()
	}
}
