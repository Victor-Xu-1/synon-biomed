package memorytools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"

	"synon-go/internal/memoryconfig"
	"synon-go/internal/memorypolicy"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type Service struct {
	store      *workspace.Store
	classifier Classifier
	config     memoryconfig.Config
}

func New(store *workspace.Store, classifier Classifier) *Service {
	return NewWithConfig(store, classifier, memoryconfig.Default())
}

func NewWithConfig(store *workspace.Store, classifier Classifier, config memoryconfig.Config) *Service {
	return &Service{store: store, classifier: classifier, config: config}
}

type resolvedEntity struct {
	key        string
	projectID  string
	artifactID string
	frameID    string
	categoryID string
	category   string
}

type preparedAppend struct {
	input     AppendInput
	body      string
	truncated bool
}

type preparedReplace struct {
	input     ReplaceInput
	body      string
	truncated bool
}

func (s *Service) Read(ctx context.Context, scope Scope, input ReadInput) (ReadResult, error) {
	scope = normalizeScope(scope)
	if scope.UserID == "" {
		return ReadResult{}, errors.New("read_memory requires a user context")
	}
	if err := s.requireAvailable(ctx, scope); err != nil {
		return ReadResult{}, err
	}
	entity, err := s.resolveEntity(ctx, scope, input.Entity, true, false)
	if err != nil {
		return ReadResult{}, err
	}
	rows, err := s.rowsForEntity(ctx, scope, entity)
	if err != nil {
		return ReadResult{}, err
	}
	staleness, err := s.store.MemoryStalenessFor(ctx, rows)
	if err != nil {
		log.Printf("read_memory staleness metadata unavailable; rendering rows without badges: %v", err)
		staleness = map[string]workspace.MemoryStaleness{}
	}
	limit := s.config.ReadToolMax
	visible := rows[:memoryToolSliceEnd(len(rows), limit)]
	_ = s.store.MarkMemoryRowsSurfaced(ctx, scope.UserID, visible, toolNow().UTC())
	return ReadResult{
		Output:   renderEntityDocument(entity, rows, staleness, limit),
		Memories: visible,
	}, nil
}

func (s *Service) Search(ctx context.Context, scope Scope, input SearchInput) (SearchResult, error) {
	scope = normalizeScope(scope)
	if err := s.requireAvailable(ctx, scope); err != nil {
		return SearchResult{}, err
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return SearchResult{}, errors.New("Missing 'query' argument")
	}
	rows, err := s.store.SearchMemories(ctx, workspace.MemorySearchOptions{
		UserID: scope.UserID, ProjectID: scope.ProjectID, FrameID: scope.FrameID,
		Query: query, Config: &s.config,
	})
	if err != nil {
		return SearchResult{}, err
	}
	if len(rows) == 0 {
		hasPool, poolErr := s.hasSearchPool(ctx, scope)
		if poolErr != nil {
			return SearchResult{}, poolErr
		}
		if !hasPool {
			return SearchResult{Output: "No memories saved yet.", ResultsReturned: 0}, nil
		}
		return SearchResult{Output: "No matches.", ResultsReturned: 0}, nil
	}
	staleness, err := s.store.MemoryStalenessFor(ctx, rows)
	if err != nil {
		return SearchResult{}, err
	}
	_ = s.store.MarkMemoryRowsSurfaced(ctx, scope.UserID, rows, toolNow().UTC())
	output, entities := renderSearchResults(rows, staleness)
	return SearchResult{Output: output, ResultsReturned: len(rows), Entities: entities, Memories: rows}, nil
}

// WriteClaimed executes one write_memory invocation whose identity and raw
// input come from a durable Transcript runner checkpoint. The first receipt
// lookup happens before mutable policy, classifier, category, or memory-row
// reads. All mutations and the receipt commit in one BEGIN IMMEDIATE.
func (s *Service) WriteClaimed(ctx context.Context, authority WriteAuthority) (WriteResult, error) {
	if s == nil || s.store == nil {
		return WriteResult{}, ErrMemoryUnavailable
	}
	invocation, err := s.store.ResolveClaimedAgentMemoryMutation(ctx, authority.Claim, authority.SourceEventID)
	if err != nil {
		return WriteResult{}, err
	}
	if strings.ToLower(strings.TrimSpace(authority.InputSHA256)) != invocation.InputSHA256 {
		return WriteResult{}, errors.New("write_memory invocation does not match its transcript checkpoint")
	}
	if invocation.Receipt != nil {
		return claimedWriteResult(invocation.Receipt.Result), nil
	}
	if authority.FreshWriteAllowed == nil {
		return WriteResult{}, ErrMemoryUnavailable
	}
	allowed, err := authority.FreshWriteAllowed(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	if !allowed {
		return WriteResult{}, ErrMemoryUnavailable
	}
	input, err := decodeClaimedWriteInput(invocation.ToolInputJSON)
	if err != nil {
		return WriteResult{}, err
	}
	scope := claimedWriteScope(invocation)
	if err := s.requireAvailable(ctx, scope); err != nil {
		return WriteResult{}, err
	}
	batch, err := s.prepareClaimedWriteBatch(ctx, scope, invocation, input)
	if err != nil {
		return WriteResult{}, err
	}
	applied, err := s.store.ApplyClaimedAgentMemoryMutations(ctx, workspace.ClaimedAgentMemoryMutationInput{
		Claim: authority.Claim, SourceEventID: authority.SourceEventID, Batch: batch,
	})
	if err != nil {
		return WriteResult{}, err
	}
	return claimedWriteResult(applied.Result), nil
}

func decodeClaimedWriteInput(raw []byte) (WriteInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input WriteInput
	if err := decoder.Decode(&input); err != nil {
		return WriteInput{}, errors.New("write_memory input is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return WriteInput{}, errors.New("write_memory input is invalid")
	}
	return input, nil
}

func claimedWriteScope(invocation transcriptstore.MemoryMutationInvocation) Scope {
	frameID := strings.TrimSpace(invocation.Stream.RootFrameID)
	if frameID == "" {
		frameID = strings.TrimSpace(invocation.Stream.FrameID)
	}
	return normalizeScope(Scope{
		UserID: invocation.Stream.OwnerID, ProjectID: invocation.Stream.ProjectID,
		FrameID: frameID, SourceFrameID: invocation.Stream.FrameID,
	})
}

func (s *Service) prepareClaimedWriteBatch(
	ctx context.Context,
	scope Scope,
	invocation transcriptstore.MemoryMutationInvocation,
	input WriteInput,
) (workspace.AgentMemoryMutationBatch, error) {
	entity, err := s.resolveEntity(ctx, scope, input.Entity, false, true)
	if err != nil {
		return workspace.AgentMemoryMutationBatch{}, err
	}
	if len(input.Append)+len(input.Replace)+len(input.Remove) == 0 {
		return workspace.AgentMemoryMutationBatch{}, errors.New("Nothing to do — pass at least one of append/replace/remove.")
	}
	appendInputs, _ := capSlice(input.Append, MaxOperationsPerKind)
	replaceInputs, _ := capSlice(input.Replace, MaxOperationsPerKind)
	removeInputs, _ := capSlice(input.Remove, MaxOperationsPerKind)
	preparedAppends, err := prepareAppends(appendInputs)
	if err != nil {
		return workspace.AgentMemoryMutationBatch{}, err
	}
	preparedReplacements, err := prepareReplacements(replaceInputs)
	if err != nil {
		return workspace.AgentMemoryMutationBatch{}, err
	}
	classificationTexts := make([]string, 0, len(preparedAppends)+len(preparedReplacements))
	if entity.frameID == "" {
		for _, appendInput := range preparedAppends {
			classificationTexts = append(classificationTexts, appendInput.body)
		}
	}
	for _, replacement := range preparedReplacements {
		classificationTexts = append(classificationTexts, replacement.body)
	}
	if err := s.classifyWrites(ctx, classificationTexts); err != nil {
		return workspace.AgentMemoryMutationBatch{}, err
	}
	categoryID, err := s.resolveWriteCategory(ctx, scope, entity, input.Category, len(preparedAppends) > 0)
	if err != nil {
		return workspace.AgentMemoryMutationBatch{}, err
	}
	batch := workspace.AgentMemoryMutationBatch{}
	for index, appendInput := range preparedAppends {
		batch.Append = append(batch.Append, workspace.AgentMemoryAppendMutation{Input: workspace.CreateMemoryInput{
			ID:   deterministicClaimedMemoryID(invocation.Stream.UID, invocation.SourceEventID, index),
			Body: appendInput.body, Origin: "agent_tool",
			Evidence:         normalizeAgentEvidence(appendInput.input.Evidence, "observed"),
			SubjectProjectID: entity.projectID, SubjectArtifactID: entity.artifactID,
			SubjectFrameID: entity.frameID, SourceFrameID: scope.SourceFrameID, CategoryID: categoryID,
		}})
	}
	for _, replacement := range preparedReplacements {
		requestedID := strings.TrimSpace(replacement.input.ID)
		active, found, err := s.store.ResolveActiveMemoryOwned(ctx, scope.UserID, requestedID)
		if err != nil {
			return workspace.AgentMemoryMutationBatch{}, err
		}
		if !found || active.Origin == "user" || active.SubjectFrameID != "" && active.SubjectFrameID != scope.FrameID {
			continue
		}
		inScope, err := s.store.AgentMemoryInScope(ctx, active, scope.UserID, scope.ProjectID, scope.FrameID)
		if err != nil {
			return workspace.AgentMemoryMutationBatch{}, err
		}
		if !inScope || requestedID != active.ID && len(missingDurableLiterals(active.Body, replacement.body)) > 0 {
			continue
		}
		evidence := active.Evidence
		if strings.TrimSpace(replacement.input.Evidence) != "" {
			evidence = normalizeAgentEvidence(replacement.input.Evidence, active.Evidence)
		}
		batch.Replace = append(batch.Replace, workspace.AgentMemoryReplaceMutation{
			ActiveID: active.ID, ExpectedOrigin: active.Origin, Body: replacement.body, Evidence: evidence,
		})
	}
	for _, rawID := range removeInputs {
		requestedID := strings.TrimSpace(rawID)
		if requestedID == "" {
			continue
		}
		active, found, err := s.store.ResolveActiveMemoryOwned(ctx, scope.UserID, requestedID)
		if err != nil {
			return workspace.AgentMemoryMutationBatch{}, err
		}
		if !found || active.Origin == "user" || active.SubjectFrameID != "" && active.SubjectFrameID != scope.FrameID {
			continue
		}
		inScope, err := s.store.AgentMemoryInScope(ctx, active, scope.UserID, scope.ProjectID, scope.FrameID)
		if err != nil {
			return workspace.AgentMemoryMutationBatch{}, err
		}
		if inScope {
			batch.Remove = append(batch.Remove, workspace.AgentMemoryRemoveMutation{ActiveID: active.ID, ExpectedOrigin: active.Origin})
		}
	}
	return batch, nil
}

func deterministicClaimedMemoryID(streamUID string, sourceEventID int64, ordinal int) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(streamUID) + "\x00" + strconv.FormatInt(sourceEventID, 10) + "\x00append\x00" + strconv.Itoa(ordinal)))
	return "mem_" + hex.EncodeToString(digest[:])[:memorypolicy.GeneratedIDHexLength]
}

func claimedWriteResult(result transcriptstore.MemoryMutationReceiptResult) WriteResult {
	write := WriteResult{
		Appended: append([]string(nil), result.Appended...), Replaced: append([]string(nil), result.Replaced...),
		Removed: append([]string(nil), result.Removed...),
	}
	switch len(write.Appended) + len(write.Replaced) + len(write.Removed) {
	case 0:
		write.Output = "No memory rows changed."
	default:
		write.Output = fmt.Sprintf("Memory updated: %d appended, %d replaced, %d removed.", len(write.Appended), len(write.Replaced), len(write.Removed))
	}
	return write
}

func (s *Service) resolveWriteCategory(ctx context.Context, scope Scope, entity resolvedEntity, raw string, hasAppends bool) (string, error) {
	if !hasAppends || strings.TrimSpace(raw) == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(raw)
	safe := sanitizeInline(raw)
	switch {
	case safe == "" || safe != trimmed:
		return "", nil
	case entity.frameID != "":
		return "", nil
	default:
		categoryID, err := s.store.MemoryCategoryIDByName(ctx, scope.UserID, safe)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return categoryID, nil
	}
}

func prepareAppends(inputs []AppendInput) ([]preparedAppend, error) {
	prepared := make([]preparedAppend, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input.Text) == "" {
			continue
		}
		body, _, err := workspace.PrepareAgentMemoryBody(input.Text)
		if err != nil {
			return nil, err
		}
		truncated := memorypolicy.UTF16Length(strings.TrimSpace(input.Text)) > memorypolicy.TextMaxUTF16Units
		prepared = append(prepared, preparedAppend{input: input, body: body, truncated: truncated})
	}
	return prepared, nil
}

func prepareReplacements(inputs []ReplaceInput) ([]preparedReplace, error) {
	prepared := make([]preparedReplace, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Text) == "" {
			continue
		}
		body, _, err := workspace.PrepareAgentMemoryBody(input.Text)
		if err != nil {
			return nil, err
		}
		truncated := memorypolicy.UTF16Length(strings.TrimSpace(input.Text)) > memorypolicy.TextMaxUTF16Units
		prepared = append(prepared, preparedReplace{input: input, body: body, truncated: truncated})
	}
	return prepared, nil
}

func (s *Service) hasSearchPool(ctx context.Context, scope Scope) (bool, error) {
	rows, err := s.store.ListMemoriesForUser(ctx, scope.UserID, "", "", false)
	if err != nil {
		return false, err
	}
	if len(rows) > 0 {
		return true, nil
	}
	if scope.FrameID != "" {
		frameRows, err := s.store.ListFrameMemories(ctx, scope.UserID, scope.FrameID)
		if err != nil {
			return false, err
		}
		return len(frameRows) > 0, nil
	}
	return false, nil
}

func (s *Service) requireAvailable(ctx context.Context, scope Scope) error {
	if s == nil || s.store == nil {
		return ErrMemoryUnavailable
	}
	if scope.UserID == "" {
		return errors.New("memory tool requires a user context")
	}
	enabled, err := s.store.MemoryEnabledWithDefault(ctx, scope.UserID, s.config.Enabled)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrMemoryUnavailable
	}
	return nil
}

func (s *Service) resolveEntity(ctx context.Context, scope Scope, raw string, allowCategory, _ bool) (resolvedEntity, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "project" {
		if scope.ProjectID == "" {
			return resolvedEntity{key: "profile"}, nil
		}
		raw = "project:" + scope.ProjectID
	}
	if allowCategory && strings.HasPrefix(raw, "category:") {
		name := strings.TrimSpace(strings.TrimPrefix(raw, "category:"))
		if name == "" {
			return resolvedEntity{}, memoryEntityUsageError(raw, allowCategory)
		}
		categoryID, err := s.store.MemoryCategoryIDByName(ctx, scope.UserID, name)
		if errors.Is(err, sql.ErrNoRows) {
			return resolvedEntity{}, fmt.Errorf("Unknown category '%s'. User-defined categories are listed under '### Categories' in the ## Memory section.", sanitizeInline(name))
		}
		if err != nil {
			return resolvedEntity{}, err
		}
		return resolvedEntity{key: "category:" + name, categoryID: categoryID, category: name}, nil
	}
	if raw == "frame" || strings.HasPrefix(raw, "frame:") {
		frameID := scope.FrameID
		if strings.HasPrefix(raw, "frame:") {
			frameID = strings.TrimSpace(strings.TrimPrefix(raw, "frame:"))
		}
		if scope.FrameID == "" || frameID != scope.FrameID {
			return resolvedEntity{}, memoryEntityUsageError(raw, allowCategory)
		}
		if _, err := s.store.ListFrameMemories(ctx, scope.UserID, frameID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return resolvedEntity{}, memoryEntityNotFoundError(raw)
			}
			return resolvedEntity{}, err
		}
		return resolvedEntity{key: "frame:" + frameID, frameID: frameID}, nil
	}
	resolved, err := s.store.ResolveMemoryEntityScope(ctx, scope.UserID, raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return resolvedEntity{}, memoryEntityNotFoundError(raw)
		}
		if strings.HasPrefix(err.Error(), "unknown memory entity") || strings.Contains(err.Error(), "memory entity requires an id") {
			return resolvedEntity{}, memoryEntityUsageError(raw, allowCategory)
		}
		return resolvedEntity{}, err
	}
	if resolved.OwningProjectID != "" && resolved.OwningProjectID != scope.ProjectID {
		return resolvedEntity{}, memoryCrossProjectError(raw)
	}
	key := "profile"
	if resolved.SubjectArtifactID != "" {
		key = "artifact:" + resolved.SubjectArtifactID
	} else if resolved.SubjectProjectID != "" {
		key = "project:" + resolved.SubjectProjectID
	}
	return resolvedEntity{
		key: key, projectID: resolved.SubjectProjectID, artifactID: resolved.SubjectArtifactID,
	}, nil
}

func memoryEntityUsageError(raw string, allowCategory bool) error {
	usage := "Use 'profile', 'project:<pid>', 'artifact:<aid>', or 'frame' (this session's private scratchpad — 'frame:<id>' for another session is not permitted)."
	if allowCategory {
		usage = "Use 'profile', 'project:<pid>', 'artifact:<aid>', 'frame' (this session's private scratchpad — 'frame:<id>' for another session is not permitted), or 'category:<name>' (a user-defined category)."
	}
	return fmt.Errorf("Unknown entity '%s'. %s", sanitizeInline(raw), usage)
}

func memoryEntityNotFoundError(raw string) error {
	safe := sanitizeInline(raw)
	switch {
	case strings.HasPrefix(raw, "project:"):
		id := sanitizeInline(strings.TrimSpace(strings.TrimPrefix(raw, "project:")))
		return fmt.Errorf("Unknown entity '%s' — project '%s' not found.", safe, id)
	case strings.HasPrefix(raw, "artifact:"):
		id := sanitizeInline(strings.TrimSpace(strings.TrimPrefix(raw, "artifact:")))
		return fmt.Errorf("Unknown entity '%s' — artifact '%s' not found.", safe, id)
	default:
		return fmt.Errorf("Unknown entity '%s' — not found.", safe)
	}
}

func memoryCrossProjectError(raw string) error {
	safe := sanitizeInline(raw)
	if strings.HasPrefix(raw, "artifact:") {
		id := sanitizeInline(strings.TrimSpace(strings.TrimPrefix(raw, "artifact:")))
		return fmt.Errorf("Unknown entity '%s' — cross-project (artifact '%s' is not in the current project).", safe, id)
	}
	id := sanitizeInline(strings.TrimSpace(strings.TrimPrefix(raw, "project:")))
	return fmt.Errorf("Unknown entity '%s' — cross-project (project '%s' is not the current project).", safe, id)
}

func (s *Service) rowsForEntity(ctx context.Context, scope Scope, entity resolvedEntity) ([]workspace.Memory, error) {
	if entity.frameID != "" {
		return s.store.ListFrameMemories(ctx, scope.UserID, entity.frameID)
	}
	rows, err := s.store.ListMemoriesForUser(ctx, scope.UserID, "", "", false)
	if err != nil {
		return nil, err
	}
	filtered := make([]workspace.Memory, 0)
	for _, memory := range rows {
		if memory.SubjectFrameID != "" {
			continue
		}
		if !memoryVisibleInScope(memory, scope) {
			continue
		}
		if entity.categoryID != "" {
			if memory.CategoryID == entity.categoryID {
				filtered = append(filtered, memory)
			}
			continue
		}
		if memoryEntityKey(memory) == entity.key {
			filtered = append(filtered, memory)
		}
	}
	return filtered, nil
}

func (s *Service) classifyWrites(ctx context.Context, texts []string) error {
	if len(texts) == 0 {
		return nil
	}
	if s.classifier == nil {
		return ErrMemoryClassifierUnavailable
	}
	results, err := s.classifier.ClassifyMemoryWrites(ctx, texts)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMemoryClassifierUnavailable, err)
	}
	if len(results) != len(texts) {
		return fmt.Errorf("%w: classifier returned %d results for %d inputs", ErrMemoryClassifierUnavailable, len(results), len(texts))
	}
	for _, result := range results {
		if result.Flagged {
			return &ClassifiedWriteError{Reason: result.Reason, Pattern: result.Pattern}
		}
	}
	return nil
}

func normalizeScope(scope Scope) Scope {
	scope.UserID = strings.TrimSpace(scope.UserID)
	scope.ProjectID = strings.TrimSpace(scope.ProjectID)
	scope.FrameID = strings.TrimSpace(scope.FrameID)
	scope.SourceFrameID = strings.TrimSpace(scope.SourceFrameID)
	if scope.SourceFrameID == "" {
		scope.SourceFrameID = scope.FrameID
	}
	return scope
}

func normalizeAgentEvidence(value, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "stated":
		return "stated"
	case "inferred":
		return "inferred"
	case "observed":
		return "observed"
	default:
		return fallback
	}
}

func memoryEntityKey(memory workspace.Memory) string {
	switch {
	case memory.SubjectFrameID != "":
		return "frame:" + memory.SubjectFrameID
	case memory.SubjectArtifactID != "":
		return "artifact:" + memory.SubjectArtifactID
	case memory.SubjectProjectID != "":
		return "project:" + memory.SubjectProjectID
	default:
		return "profile"
	}
}

func memoryVisibleInScope(memory workspace.Memory, scope Scope) bool {
	return memory.SubjectProjectID == "" || memory.SubjectProjectID == scope.ProjectID
}

func capSlice[T any](values []T, limit int) ([]T, int) {
	if len(values) <= limit {
		return values, 0
	}
	return values[:limit], len(values) - limit
}
