package memoryextract

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"synon-go/internal/memoryclassifier"
	"synon-go/internal/memorypolicy"
	"synon-go/internal/memorytools"
	workspace "synon-go/internal/persistence/workspace"

	"github.com/google/uuid"
)

type ExtractionClassifier interface {
	ClassifyExtractionWrites(context.Context, []string) ([]memorytools.Classification, error)
}

type LiteralRepairer interface {
	RepairMemoryReplacement(context.Context, string, string) (string, error)
}

type ApplyScope struct {
	UserID        string
	ProjectID     string
	SourceFrameID string
}

type OperationFailure struct {
	Kind  string
	ID    string
	Error string
}

type ApplyResult struct {
	Appended      []string
	Replaced      []string
	Removed       []string
	PIUnavailable bool
	PIRejected    int
	Failures      []OperationFailure
}

type Service struct {
	store      *workspace.Store
	classifier ExtractionClassifier
	repairer   LiteralRepairer
	newID      func() string
}

func NewService(store *workspace.Store, classifier ExtractionClassifier, repairer LiteralRepairer) *Service {
	return &Service{
		store: store, classifier: classifier, repairer: repairer,
		newID: func() string {
			return "mem_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:memorypolicy.GeneratedIDHexLength]
		},
	}
}

func (s *Service) Apply(ctx context.Context, scope ApplyScope, operations Operations) (ApplyResult, error) {
	result := ApplyResult{Appended: []string{}, Replaced: []string{}, Removed: []string{}, Failures: []OperationFailure{}}
	if s == nil || s.store == nil {
		return result, errors.New("memory extraction store is unavailable")
	}
	scope.UserID = strings.TrimSpace(scope.UserID)
	scope.ProjectID = strings.TrimSpace(scope.ProjectID)
	scope.SourceFrameID = strings.TrimSpace(scope.SourceFrameID)
	if scope.UserID == "" || scope.SourceFrameID == "" {
		return result, errors.New("memory extraction user and source frame are required")
	}

	prepared, classificationTexts := s.prepareAppends(operations.Append, &result)
	if len(classificationTexts) > 0 {
		if s.classifier == nil {
			result.PIUnavailable = true
			return result, nil
		}
		classifications, err := s.classifier.ClassifyExtractionWrites(ctx, classificationTexts)
		if errors.Is(err, memoryclassifier.ErrUnavailable) {
			result.PIUnavailable = true
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if len(classifications) != len(classificationTexts) {
			return result, errors.New("memory extraction classifier returned the wrong result count")
		}
		classificationIndex := 0
		for index := range prepared {
			if prepared[index].skip || prepared[index].frameScoped {
				continue
			}
			prepared[index].classification = classifications[classificationIndex]
			classificationIndex++
		}
	}

	for _, appendOperation := range prepared {
		if appendOperation.skip || appendOperation.frameScoped {
			continue
		}
		if appendOperation.classification.Flagged {
			result.PIRejected++
			continue
		}
		if err := s.applyAppend(ctx, scope, appendOperation, &result); err != nil {
			result.Failures = append(result.Failures, OperationFailure{Kind: "append", Error: err.Error()})
		}
	}
	for _, replacement := range operations.Replace {
		if err := s.applyReplacement(ctx, scope, replacement, &result); err != nil {
			return result, err
		}
	}
	for _, memoryID := range operations.Remove {
		if err := s.applyRemoval(ctx, scope, memoryID, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

type preparedAppend struct {
	operation      AppendOperation
	body           string
	frameScoped    bool
	skip           bool
	classification memorytools.Classification
}

func (s *Service) prepareAppends(operations []AppendOperation, result *ApplyResult) ([]preparedAppend, []string) {
	prepared := make([]preparedAppend, len(operations))
	texts := make([]string, 0, len(operations))
	for index, operation := range operations {
		item := preparedAppend{operation: operation, frameScoped: isFrameEntity(operation.Entity)}
		body, _, err := workspace.PrepareAgentMemoryBody(operation.Text)
		if err != nil {
			item.skip = true
			result.Failures = append(result.Failures, OperationFailure{Kind: "append", Error: err.Error()})
		} else {
			item.body = body
			if !item.frameScoped {
				texts = append(texts, body)
			}
		}
		prepared[index] = item
	}
	return prepared, texts
}

func (s *Service) applyAppend(ctx context.Context, scope ApplyScope, appendOperation preparedAppend, result *ApplyResult) error {
	projectID, artifactID, ok, err := s.resolveAppendEntity(ctx, scope, appendOperation.operation.Entity)
	if err != nil || !ok {
		return err
	}
	categoryID := ""
	categoryName := strings.TrimSpace(appendOperation.operation.Category)
	if categoryName != "" {
		categoryID, err = s.store.MemoryCategoryIDByName(ctx, scope.UserID, categoryName)
		if errors.Is(err, sql.ErrNoRows) {
			categoryID, err = "", nil
		}
		if err != nil {
			return err
		}
	}
	evidence := normalizedEvidence(appendOperation.operation.Evidence)
	mutation := workspace.AgentMemoryMutationBatch{
		OwnerUserID: scope.UserID, ProjectID: scope.ProjectID, FrameID: scope.SourceFrameID,
		Append: []workspace.AgentMemoryAppendMutation{{Input: workspace.CreateMemoryInput{
			ID: s.newID(), UserID: scope.UserID, Body: appendOperation.body,
			Origin: "extractor", Evidence: evidence, SubjectProjectID: projectID,
			SubjectArtifactID: artifactID, SourceFrameID: scope.SourceFrameID, CategoryID: categoryID,
		}}},
	}
	applied, err := s.store.ApplyExtractorMemoryMutations(ctx, mutation)
	if err != nil && categoryID != "" {
		categoryExists, lookupErr := s.store.MemoryCategoryExistsOwned(ctx, scope.UserID, categoryID)
		if lookupErr == nil && !categoryExists {
			mutation.Append[0].Input.CategoryID = ""
			applied, err = s.store.ApplyExtractorMemoryMutations(ctx, mutation)
		}
	}
	if err != nil {
		return err
	}
	if len(applied.Appended) != 1 {
		return errors.New("extractor append did not persist exactly one row")
	}
	result.Appended = append(result.Appended, applied.Appended[0].ID)
	return nil
}

func (s *Service) applyReplacement(ctx context.Context, scope ApplyScope, operation ReplaceOperation, result *ApplyResult) error {
	memoryID := strings.TrimSpace(operation.ID)
	if memoryID == "" || strings.TrimSpace(operation.Text) == "" {
		return nil
	}
	predecessor, found, err := s.store.GetMemoryOwned(ctx, scope.UserID, memoryID)
	if err != nil {
		return err
	}
	if !found || predecessor.SupersededBy != "" || predecessor.Origin == "user" || predecessor.SubjectFrameID != "" {
		return nil
	}
	inScope, err := s.store.AgentMemoryInScope(ctx, predecessor, scope.UserID, scope.ProjectID, "")
	if err != nil {
		return err
	}
	if !inScope {
		return nil
	}
	replacement, _, err := workspace.PrepareAgentMemoryBody(operation.Text)
	if err != nil {
		result.Failures = append(result.Failures, OperationFailure{Kind: "replace", ID: memoryID, Error: err.Error()})
		return nil
	}
	if missing := memorytools.MissingDurableLiterals(predecessor.Body, replacement); len(missing) > 0 {
		if s.repairer == nil {
			result.Failures = append(result.Failures, OperationFailure{Kind: "replace", ID: memoryID, Error: "replacement drops durable literals and no repairer is configured"})
			return nil
		}
		repaired, repairErr := s.repairer.RepairMemoryReplacement(ctx, predecessor.Body, replacement)
		if repairErr != nil {
			result.Failures = append(result.Failures, OperationFailure{Kind: "replace", ID: memoryID, Error: repairErr.Error()})
			return nil
		}
		replacement, _, repairErr = workspace.PrepareAgentMemoryBody(repaired)
		if repairErr != nil ||
			len(memorytools.MissingDurableLiterals(predecessor.Body, replacement)) > 0 ||
			len(memorytools.MissingUpdatedDurableLiterals(operation.Text, replacement)) > 0 ||
			len(memorytools.UnexpectedDurableLiterals(replacement, predecessor.Body, operation.Text)) > 0 {
			result.Failures = append(result.Failures, OperationFailure{Kind: "replace", ID: memoryID, Error: "literal repair failed validation"})
			return nil
		}
	}
	if s.classifier == nil {
		result.Failures = append(result.Failures, OperationFailure{Kind: "replace", ID: memoryID, Error: memoryclassifier.ErrUnavailable.Error()})
		return nil
	}
	classifications, err := s.classifier.ClassifyExtractionWrites(ctx, []string{replacement})
	if errors.Is(err, memoryclassifier.ErrUnavailable) {
		result.Failures = append(result.Failures, OperationFailure{Kind: "replace", ID: memoryID, Error: memoryclassifier.ErrUnavailable.Error()})
		return nil
	}
	if err != nil {
		return err
	}
	if len(classifications) != 1 {
		return errors.New("memory replacement classifier returned the wrong result count")
	}
	if classifications[0].Flagged {
		result.PIRejected++
		return nil
	}
	evidence := strings.TrimSpace(operation.Evidence)
	_, replaced, err := s.store.CreateAndSupersedeExtractorMemory(
		ctx, predecessor, scope.UserID, scope.ProjectID, scope.SourceFrameID,
		s.newID(), replacement, evidence,
	)
	if err != nil {
		result.Failures = append(result.Failures, OperationFailure{Kind: "replace", ID: memoryID, Error: err.Error()})
		return nil
	}
	if replaced {
		result.Replaced = append(result.Replaced, memoryID)
	}
	return nil
}

func (s *Service) applyRemoval(ctx context.Context, scope ApplyScope, requestedID string, result *ApplyResult) error {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID == "" {
		return nil
	}
	for attempt := 0; attempt < memorypolicy.RemoveRetryAttempts; attempt++ {
		active, found, err := s.store.ResolveActiveMemoryOwned(ctx, scope.UserID, requestedID)
		if err != nil {
			return err
		}
		if !found || active.Origin == "user" || active.SubjectFrameID != "" {
			return nil
		}
		inScope, err := s.store.AgentMemoryInScope(ctx, active, scope.UserID, scope.ProjectID, "")
		if err != nil {
			return err
		}
		if !inScope {
			return nil
		}
		mutation, err := s.store.ApplyExtractorMemoryMutations(ctx, workspace.AgentMemoryMutationBatch{
			OwnerUserID: scope.UserID, ProjectID: scope.ProjectID, FrameID: scope.SourceFrameID,
			Remove: []workspace.AgentMemoryRemoveMutation{{ActiveID: active.ID, ExpectedOrigin: active.Origin}},
		})
		if errors.Is(err, workspace.ErrAgentMemoryChanged) {
			continue
		}
		if err != nil {
			return err
		}
		if len(mutation.Removed) == 1 {
			result.Removed = append(result.Removed, mutation.Removed[0])
		}
		return nil
	}
	return nil
}

func (s *Service) resolveAppendEntity(ctx context.Context, scope ApplyScope, raw string) (string, string, bool, error) {
	if isFrameEntity(raw) {
		return "", "", false, nil
	}
	entity := extractorAppendEntity(raw, scope.ProjectID)
	if entity == "profile" && scope.ProjectID != "" {
		entity = "project:" + scope.ProjectID
	}
	resolved, err := s.store.ResolveMemoryEntityScope(ctx, scope.UserID, entity)
	if err != nil {
		return "", "", false, nil
	}
	if scope.ProjectID != "" && resolved.OwningProjectID != "" && resolved.OwningProjectID != scope.ProjectID {
		return "", "", false, nil
	}
	return resolved.SubjectProjectID, resolved.SubjectArtifactID, true, nil
}

func extractorAppendEntity(raw, currentProjectID string) string {
	fallback := "profile"
	if currentProjectID != "" {
		fallback = "project:" + currentProjectID
	}
	entity := strings.TrimSpace(raw)
	switch {
	case entity == "", entity == "project":
		return fallback
	case entity == "profile":
		return entity
	case validExtractorEntityID(entity, "project:"), validExtractorEntityID(entity, "artifact:"):
		return entity
	default:
		return fallback
	}
}

func validExtractorEntityID(entity, prefix string) bool {
	if !strings.HasPrefix(entity, prefix) {
		return false
	}
	id := strings.TrimPrefix(entity, prefix)
	return id != "" && strings.IndexFunc(id, unicode.IsSpace) == -1
}

func isFrameEntity(value string) bool {
	value = strings.TrimSpace(value)
	return value == "frame" || strings.HasPrefix(value, "frame:")
}

func (failure OperationFailure) String() string {
	if failure.ID == "" {
		return fmt.Sprintf("%s: %s", failure.Kind, failure.Error)
	}
	return fmt.Sprintf("%s %s: %s", failure.Kind, failure.ID, failure.Error)
}
