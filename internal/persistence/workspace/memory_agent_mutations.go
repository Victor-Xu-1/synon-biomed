package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"synon-go/internal/memorypolicy"
	transcriptstore "synon-go/internal/persistence/transcript"
)

var (
	ErrAgentMemoryMutationForbidden = errors.New("agent memory mutation is not permitted")
	ErrAgentMemoryChanged           = errors.New("memory changed while the write was in flight")
)

type AgentMemoryAppendMutation struct {
	Input CreateMemoryInput
}

type AgentMemoryReplaceMutation struct {
	ActiveID       string
	ExpectedOrigin string
	Body           string
	Evidence       string
}

type AgentMemoryRemoveMutation struct {
	ActiveID       string
	ExpectedOrigin string
}

type AgentMemoryMutationBatch struct {
	OwnerUserID string
	ProjectID   string
	FrameID     string
	Append      []AgentMemoryAppendMutation
	Replace     []AgentMemoryReplaceMutation
	Remove      []AgentMemoryRemoveMutation
}

type AgentMemoryMutationResult struct {
	Appended []Memory
	Replaced []Memory
	Removed  []string
}

type ClaimedAgentMemoryMutationInput struct {
	Claim         transcriptstore.RunnerClaim
	SourceEventID int64
	Batch         AgentMemoryMutationBatch
}

type ClaimedAgentMemoryMutationResult struct {
	Result  transcriptstore.MemoryMutationReceiptResult
	Receipt transcriptstore.MemoryMutationReceipt
	Applied bool
}

func (s *Store) ResolveActiveMemoryOwned(ctx context.Context, userID, memoryID string) (Memory, bool, error) {
	if s == nil || s.db == nil {
		return Memory{}, false, errors.New("workspace store is closed")
	}
	return resolveActiveMemoryOwned(ctx, s.db, strings.TrimSpace(userID), strings.TrimSpace(memoryID))
}

// ResolveClaimedAgentMemoryMutation returns the canonical tool invocation or
// its durable receipt without consulting mutable memory rows or policy state.
func (s *Store) ResolveClaimedAgentMemoryMutation(
	ctx context.Context,
	claim transcriptstore.RunnerClaim,
	sourceEventID int64,
) (transcriptstore.MemoryMutationInvocation, error) {
	if s == nil || s.db == nil {
		return transcriptstore.MemoryMutationInvocation{}, errors.New("workspace store is closed")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return transcriptstore.MemoryMutationInvocation{}, err
	}
	var invocation transcriptstore.MemoryMutationInvocation
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		var err error
		invocation, err = tx.ResolveMemoryMutationInvocation(ctx, claim, sourceEventID)
		return err
	})
	return invocation, err
}

// ApplyClaimedAgentMemoryMutations applies one logical runner tool call and
// its Transcript receipt in the same BEGIN IMMEDIATE transaction. Classifier
// and model work must finish before entering this method.
func (s *Store) ApplyClaimedAgentMemoryMutations(
	ctx context.Context,
	input ClaimedAgentMemoryMutationInput,
) (ClaimedAgentMemoryMutationResult, error) {
	if s == nil || s.db == nil {
		return ClaimedAgentMemoryMutationResult{}, errors.New("workspace store is closed")
	}
	if input.SourceEventID <= 0 {
		return ClaimedAgentMemoryMutationResult{}, errors.New("memory mutation source event id is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return ClaimedAgentMemoryMutationResult{}, err
	}
	result := ClaimedAgentMemoryMutationResult{}
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		invocation, err := tx.ResolveMemoryMutationInvocation(ctx, input.Claim, input.SourceEventID)
		if err != nil {
			return err
		}
		if invocation.Receipt != nil {
			result = claimedAgentMemoryReplay(*invocation.Receipt)
			return nil
		}
		stream := invocation.Stream
		if stream.Kind != transcriptstore.StreamKindFrameRef || stream.OwnerID == "" || stream.ProjectID == "" || stream.FrameID == "" {
			return fmt.Errorf("%w: memory mutation requires an owned frame transcript", ErrAgentMemoryMutationForbidden)
		}
		batch, err := normalizeClaimedAgentMemoryMutationBatch(input.Batch)
		if err != nil {
			return err
		}
		rootFrameID := strings.TrimSpace(stream.RootFrameID)
		if rootFrameID == "" {
			rootFrameID = stream.FrameID
		}
		batch.OwnerUserID, batch.ProjectID, batch.FrameID = stream.OwnerID, stream.ProjectID, rootFrameID
		batch, err = canonicalizeClaimedAgentMemoryMutationScopes(ctx, tx, stream, batch)
		if err != nil {
			return err
		}
		mutationSHA256, err := claimedAgentMemoryMutationPlanDigest(stream, batch)
		if err != nil {
			return err
		}
		mutation, err := s.applyAgentMemoryMutationBatchTx(ctx, tx, batch)
		if err != nil {
			return err
		}
		receipt, err := tx.AppendMemoryMutationReceipt(ctx, transcriptstore.MemoryMutationReceiptInput{
			Claim: input.Claim, SourceEventID: input.SourceEventID,
			InputSHA256: invocation.InputSHA256, MutationSHA256: mutationSHA256,
			Result: agentMemoryMutationReceiptResult(mutation),
		})
		if err != nil {
			return err
		}
		result = ClaimedAgentMemoryMutationResult{Result: receipt.Result, Receipt: receipt, Applied: true}
		return nil
	})
	return result, err
}

func normalizeClaimedAgentMemoryMutationBatch(batch AgentMemoryMutationBatch) (AgentMemoryMutationBatch, error) {
	batch = cloneAgentMemoryMutationBatch(batch)
	if len(batch.Append) > memorypolicy.OperationsPerKindMax || len(batch.Replace) > memorypolicy.OperationsPerKindMax || len(batch.Remove) > memorypolicy.OperationsPerKindMax {
		return AgentMemoryMutationBatch{}, fmt.Errorf("memory mutation operations exceed %d per kind", memorypolicy.OperationsPerKindMax)
	}
	batch.OwnerUserID, batch.ProjectID, batch.FrameID = "", "", ""
	operationIDs := make(map[string]struct{})
	for index := range batch.Append {
		input := batch.Append[index].Input
		input.ID = strings.TrimSpace(input.ID)
		input.UserID = ""
		input.Origin = "agent_tool"
		input.Evidence = strings.ToLower(strings.TrimSpace(input.Evidence))
		input.SubjectProjectID = strings.TrimSpace(input.SubjectProjectID)
		input.SubjectArtifactID = strings.TrimSpace(input.SubjectArtifactID)
		input.SubjectVersionID = strings.TrimSpace(input.SubjectVersionID)
		input.SubjectFrameID = strings.TrimSpace(input.SubjectFrameID)
		input.SourceFrameID = strings.TrimSpace(input.SourceFrameID)
		input.CategoryID = strings.TrimSpace(input.CategoryID)
		body, _, err := PrepareAgentMemoryBody(input.Body)
		if err != nil {
			return AgentMemoryMutationBatch{}, err
		}
		if pattern := FindMemoryExfilPattern(body); pattern != "" {
			return AgentMemoryMutationBatch{}, &MemoryPromptInjectionError{Pattern: pattern}
		}
		input.Body = body
		if input.ID == "" {
			return AgentMemoryMutationBatch{}, errors.New("memory append id is required")
		}
		if _, duplicate := operationIDs[input.ID]; duplicate {
			return AgentMemoryMutationBatch{}, errors.New("memory mutation contains a duplicate id")
		}
		operationIDs[input.ID] = struct{}{}
		if !validAgentMemoryEvidence(input.Evidence) {
			return AgentMemoryMutationBatch{}, errors.New("memory mutation evidence is invalid")
		}
		batch.Append[index].Input = input
	}
	for index := range batch.Replace {
		mutation := batch.Replace[index]
		mutation.ActiveID = strings.TrimSpace(mutation.ActiveID)
		mutation.ExpectedOrigin = strings.TrimSpace(mutation.ExpectedOrigin)
		mutation.Evidence = strings.ToLower(strings.TrimSpace(mutation.Evidence))
		body, _, err := PrepareAgentMemoryBody(mutation.Body)
		if err != nil {
			return AgentMemoryMutationBatch{}, err
		}
		if pattern := FindMemoryExfilPattern(body); pattern != "" {
			return AgentMemoryMutationBatch{}, &MemoryPromptInjectionError{Pattern: pattern}
		}
		mutation.Body = body
		if mutation.ActiveID == "" || !validAgentMutableMemoryOrigin(mutation.ExpectedOrigin) || !validAgentMemoryEvidence(mutation.Evidence) {
			return AgentMemoryMutationBatch{}, fmt.Errorf("%w: memory replacement precondition is invalid", ErrAgentMemoryMutationForbidden)
		}
		if _, duplicate := operationIDs[mutation.ActiveID]; duplicate {
			return AgentMemoryMutationBatch{}, errors.New("memory mutation contains a duplicate id")
		}
		operationIDs[mutation.ActiveID] = struct{}{}
		batch.Replace[index] = mutation
	}
	for index := range batch.Remove {
		mutation := batch.Remove[index]
		mutation.ActiveID = strings.TrimSpace(mutation.ActiveID)
		mutation.ExpectedOrigin = strings.TrimSpace(mutation.ExpectedOrigin)
		if mutation.ActiveID == "" || !validAgentMutableMemoryOrigin(mutation.ExpectedOrigin) {
			return AgentMemoryMutationBatch{}, fmt.Errorf("%w: memory removal precondition is invalid", ErrAgentMemoryMutationForbidden)
		}
		if _, duplicate := operationIDs[mutation.ActiveID]; duplicate {
			return AgentMemoryMutationBatch{}, errors.New("memory mutation contains a duplicate id")
		}
		operationIDs[mutation.ActiveID] = struct{}{}
		batch.Remove[index] = mutation
	}
	return batch, nil
}

func cloneAgentMemoryMutationBatch(batch AgentMemoryMutationBatch) AgentMemoryMutationBatch {
	batch.Append = append([]AgentMemoryAppendMutation(nil), batch.Append...)
	batch.Replace = append([]AgentMemoryReplaceMutation(nil), batch.Replace...)
	batch.Remove = append([]AgentMemoryRemoveMutation(nil), batch.Remove...)
	return batch
}

func validAgentMemoryEvidence(value string) bool {
	switch value {
	case "stated", "observed", "inferred":
		return true
	default:
		return false
	}
}

func validAgentMutableMemoryOrigin(value string) bool {
	return value == "agent_tool" || value == "extractor"
}

func canonicalizeClaimedAgentMemoryMutationScopes(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	stream transcriptstore.Stream,
	batch AgentMemoryMutationBatch,
) (AgentMemoryMutationBatch, error) {
	rootFrameID := strings.TrimSpace(stream.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = stream.FrameID
	}
	for index := range batch.Append {
		input := batch.Append[index].Input
		input.UserID = stream.OwnerID
		if input.SubjectFrameID != "" && input.SubjectFrameID != rootFrameID {
			return AgentMemoryMutationBatch{}, fmt.Errorf("%w: frame scratchpad belongs to another session", ErrAgentMemoryMutationForbidden)
		}
		if input.SourceFrameID != "" && input.SourceFrameID != stream.FrameID {
			return AgentMemoryMutationBatch{}, fmt.Errorf("%w: memory source frame is unavailable", ErrAgentMemoryMutationForbidden)
		}
		projectID, err := validateMemoryScopeTx(ctx, tx, input, stream.OwnerID)
		if err != nil {
			return AgentMemoryMutationBatch{}, err
		}
		if projectID != "" && projectID != stream.ProjectID {
			return AgentMemoryMutationBatch{}, fmt.Errorf("%w: memory belongs to another project", ErrAgentMemoryMutationForbidden)
		}
		batch.Append[index].Input = input
	}
	for _, mutation := range batch.Replace {
		memory, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, mutation.ActiveID, stream.OwnerID))
		if err != nil {
			return AgentMemoryMutationBatch{}, fmt.Errorf("read active memory for replacement: %w", err)
		}
		if memory.SupersededBy != "" || memory.Origin != mutation.ExpectedOrigin {
			return AgentMemoryMutationBatch{}, ErrAgentMemoryChanged
		}
		if memory.Origin == "user" {
			return AgentMemoryMutationBatch{}, fmt.Errorf("%w: user-authored rows cannot be replaced by an agent", ErrAgentMemoryMutationForbidden)
		}
		if err := requireAgentMemoryScopeTx(ctx, tx, memory, stream.OwnerID, stream.ProjectID, rootFrameID); err != nil {
			return AgentMemoryMutationBatch{}, err
		}
	}
	for _, mutation := range batch.Remove {
		memory, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, mutation.ActiveID, stream.OwnerID))
		if err != nil {
			return AgentMemoryMutationBatch{}, fmt.Errorf("read active memory for removal: %w", err)
		}
		if memory.SupersededBy != "" || memory.Origin != mutation.ExpectedOrigin {
			return AgentMemoryMutationBatch{}, ErrAgentMemoryChanged
		}
		if memory.Origin == "user" {
			return AgentMemoryMutationBatch{}, fmt.Errorf("%w: user-authored rows cannot be removed by an agent", ErrAgentMemoryMutationForbidden)
		}
		if err := requireAgentMemoryScopeTx(ctx, tx, memory, stream.OwnerID, stream.ProjectID, rootFrameID); err != nil {
			return AgentMemoryMutationBatch{}, err
		}
		if err := validateAgentMemoryRemovalChainTx(ctx, tx, memory.ID, stream.OwnerID, stream.ProjectID, rootFrameID); err != nil {
			return AgentMemoryMutationBatch{}, err
		}
	}
	return batch, nil
}

func claimedAgentMemoryMutationPlanDigest(stream transcriptstore.Stream, batch AgentMemoryMutationBatch) (string, error) {
	raw, err := json.Marshal(struct {
		StreamUID string                   `json:"stream_uid"`
		Batch     AgentMemoryMutationBatch `json:"batch"`
	}{StreamUID: stream.UID, Batch: batch})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func validateAgentMemoryRemovalChainTx(
	ctx context.Context,
	tx memoryTransaction,
	activeID, ownerID, projectID, frameID string,
) error {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE chain(id) AS (
		SELECT ? UNION SELECT predecessor.id FROM memories AS predecessor JOIN chain ON predecessor.superseded_by=chain.id
		WHERE predecessor.user_id=?
	) SELECT id FROM chain ORDER BY id`, activeID, ownerID)
	if err != nil {
		return fmt.Errorf("read agent memory removal chain: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan agent memory removal chain: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate agent memory removal chain: %w", err)
	}
	if len(ids) == 0 {
		return ErrAgentMemoryChanged
	}
	for _, id := range ids {
		memory, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id=? AND m.user_id=?`, id, ownerID))
		if err != nil {
			return fmt.Errorf("read agent memory removal chain row: %w", err)
		}
		if !validAgentMutableMemoryOrigin(memory.Origin) {
			return fmt.Errorf("%w: memory removal chain contains a protected row", ErrAgentMemoryMutationForbidden)
		}
		if err := requireAgentMemoryScopeTx(ctx, tx, memory, ownerID, projectID, frameID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyAgentMemoryMutationBatchTx(
	ctx context.Context,
	tx memoryTransaction,
	batch AgentMemoryMutationBatch,
) (AgentMemoryMutationResult, error) {
	result := AgentMemoryMutationResult{Appended: []Memory{}, Replaced: []Memory{}, Removed: []string{}}
	for _, appendMutation := range batch.Append {
		memory, err := s.createMemoryTx(ctx, tx, appendMutation.Input, batch.OwnerUserID)
		if err != nil {
			return AgentMemoryMutationResult{}, err
		}
		result.Appended = append(result.Appended, memory)
	}
	for _, replaceMutation := range batch.Replace {
		memory, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, replaceMutation.ActiveID, batch.OwnerUserID))
		if err != nil {
			return AgentMemoryMutationResult{}, fmt.Errorf("read active memory for replacement: %w", err)
		}
		candidate := CreateMemoryInput{
			ID: memory.ID, UserID: memory.UserID, Body: replaceMutation.Body, Origin: memory.Origin,
			Evidence: replaceMutation.Evidence, SubjectProjectID: memory.SubjectProjectID,
			SubjectArtifactID: memory.SubjectArtifactID, SubjectVersionID: memory.SubjectVersionID,
			SubjectFrameID: memory.SubjectFrameID, SourceFrameID: memory.SourceFrameID,
			CategoryID: memory.CategoryID,
		}
		candidate, err = normalizeMemoryInput(candidate)
		if err != nil {
			return AgentMemoryMutationResult{}, err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE memories SET body = ?, evidence = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND superseded_by IS NULL AND origin = ?`,
			candidate.Body, candidate.Evidence, s.now().UTC(), memory.ID, batch.OwnerUserID, memory.Origin)
		if err != nil {
			return AgentMemoryMutationResult{}, fmt.Errorf("replace agent memory: %w", err)
		}
		if err := requireOneMutationRow(updated, "memory", memory.ID); err != nil {
			return AgentMemoryMutationResult{}, fmt.Errorf("%w: %v", ErrAgentMemoryChanged, err)
		}
		memory, err = scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, memory.ID, batch.OwnerUserID))
		if err != nil {
			return AgentMemoryMutationResult{}, fmt.Errorf("read replaced agent memory: %w", err)
		}
		result.Replaced = append(result.Replaced, memory)
	}
	for _, removeMutation := range batch.Remove {
		memory, err := scanMemory(tx.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, removeMutation.ActiveID, batch.OwnerUserID))
		if err != nil {
			return AgentMemoryMutationResult{}, fmt.Errorf("read active memory for removal: %w", err)
		}
		removed, err := tx.ExecContext(ctx, `WITH RECURSIVE chain(id) AS (
			SELECT ? UNION SELECT predecessor.id FROM memories AS predecessor JOIN chain ON predecessor.superseded_by = chain.id
			WHERE predecessor.user_id = ?
		) DELETE FROM memories WHERE user_id = ? AND id IN (SELECT id FROM chain)`,
			memory.ID, batch.OwnerUserID, batch.OwnerUserID)
		if err != nil {
			return AgentMemoryMutationResult{}, fmt.Errorf("remove agent memory chain: %w", err)
		}
		count, err := removed.RowsAffected()
		if err != nil || count < 1 {
			return AgentMemoryMutationResult{}, ErrAgentMemoryChanged
		}
		result.Removed = append(result.Removed, memory.ID)
	}
	return result, nil
}

func agentMemoryMutationReceiptResult(result AgentMemoryMutationResult) transcriptstore.MemoryMutationReceiptResult {
	receipt := transcriptstore.MemoryMutationReceiptResult{Appended: []string{}, Replaced: []string{}, Removed: append([]string(nil), result.Removed...)}
	for _, memory := range result.Appended {
		receipt.Appended = append(receipt.Appended, memory.ID)
	}
	for _, memory := range result.Replaced {
		receipt.Replaced = append(receipt.Replaced, memory.ID)
	}
	return receipt
}

func claimedAgentMemoryReplay(receipt transcriptstore.MemoryMutationReceipt) ClaimedAgentMemoryMutationResult {
	return ClaimedAgentMemoryMutationResult{Result: receipt.Result, Receipt: receipt, Applied: false}
}

type memoryRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveActiveMemoryOwned(ctx context.Context, queryer memoryRowQueryer, userID, memoryID string) (Memory, bool, error) {
	if userID == "" || memoryID == "" {
		return Memory{}, false, nil
	}
	visited := make(map[string]struct{})
	for {
		if _, duplicate := visited[memoryID]; duplicate {
			return Memory{}, false, errors.New("memory supersession cycle detected")
		}
		visited[memoryID] = struct{}{}
		memory, err := scanMemory(queryer.QueryRowContext(ctx, memorySelect+` WHERE m.id = ? AND m.user_id = ?`, memoryID, userID))
		if errors.Is(err, sql.ErrNoRows) {
			return Memory{}, false, nil
		}
		if err != nil {
			return Memory{}, false, fmt.Errorf("resolve active memory: %w", err)
		}
		if memory.SupersededBy == "" {
			return memory, true, nil
		}
		memoryID = memory.SupersededBy
	}
}

func (s *Store) AgentMemoryInScope(ctx context.Context, memory Memory, userID, projectID, frameID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	frameID = strings.TrimSpace(frameID)
	if memory.SubjectFrameID != "" {
		if frameID == "" || memory.SubjectFrameID != frameID {
			return false, nil
		}
		var owner string
		err := s.db.QueryRowContext(ctx, `SELECT project.user_id FROM frames AS frame JOIN projects AS project ON project.id = frame.project_id WHERE frame.id = ?`, frameID).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("verify memory frame scope: %w", err)
		}
		return owner == userID, nil
	}

	targetProjectID := memory.SubjectProjectID
	if memory.SubjectArtifactID != "" {
		err := s.db.QueryRowContext(ctx, `SELECT project_id FROM artifacts WHERE id = ?`, memory.SubjectArtifactID).Scan(&targetProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("verify memory artifact scope: %w", err)
		}
	}
	if memory.SubjectVersionID != "" {
		err := s.db.QueryRowContext(ctx, `SELECT artifact.project_id FROM artifact_versions AS version JOIN artifacts AS artifact ON artifact.id = version.artifact_id WHERE version.id = ?`, memory.SubjectVersionID).Scan(&targetProjectID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("verify memory version scope: %w", err)
		}
	}
	if targetProjectID == "" {
		return true, nil
	}
	if projectID == "" || targetProjectID != projectID {
		return false, nil
	}
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM projects WHERE id = ?`, targetProjectID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("verify memory project scope: %w", err)
	}
	return owner == userID, nil
}

func requireAgentMemoryScopeTx(ctx context.Context, tx memoryTransaction, memory Memory, userID, projectID, frameID string) error {
	if err := requireAgentMemoryArtifactVersionPairTx(ctx, tx, memory); err != nil {
		return err
	}
	if memory.SubjectFrameID != "" {
		if frameID == "" || memory.SubjectFrameID != frameID {
			return fmt.Errorf("%w: frame scratchpad belongs to another session", ErrAgentMemoryMutationForbidden)
		}
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT project.user_id FROM frames AS frame JOIN projects AS project ON project.id = frame.project_id WHERE frame.id = ?`, frameID).Scan(&owner); err != nil {
			return fmt.Errorf("verify memory frame scope: %w", err)
		}
		if owner != userID {
			return fmt.Errorf("%w: frame scratchpad is unavailable", ErrAgentMemoryMutationForbidden)
		}
		return nil
	}

	targetProjectID := memory.SubjectProjectID
	if memory.SubjectArtifactID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM artifacts WHERE id = ?`, memory.SubjectArtifactID).Scan(&targetProjectID); err != nil {
			return fmt.Errorf("verify memory artifact scope: %w", err)
		}
	}
	if memory.SubjectVersionID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT artifact.project_id FROM artifact_versions AS version JOIN artifacts AS artifact ON artifact.id = version.artifact_id WHERE version.id = ?`, memory.SubjectVersionID).Scan(&targetProjectID); err != nil {
			return fmt.Errorf("verify memory version scope: %w", err)
		}
	}
	if targetProjectID == "" {
		return nil
	}
	if projectID == "" || targetProjectID != projectID {
		return fmt.Errorf("%w: memory belongs to another project", ErrAgentMemoryMutationForbidden)
	}
	var projectOwner string
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM projects WHERE id = ?`, targetProjectID).Scan(&projectOwner); err != nil || projectOwner != userID {
		return fmt.Errorf("%w: memory project is unavailable", ErrAgentMemoryMutationForbidden)
	}
	return nil
}

func requireAgentMemoryArtifactVersionPairTx(ctx context.Context, tx memoryTransaction, memory Memory) error {
	if memory.SubjectArtifactID == "" || memory.SubjectVersionID == "" {
		return nil
	}
	var pairCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifact_versions WHERE id=? AND artifact_id=?`,
		memory.SubjectVersionID, memory.SubjectArtifactID).Scan(&pairCount); err != nil {
		return fmt.Errorf("verify agent memory artifact version pair: %w", err)
	}
	if pairCount != 1 {
		return fmt.Errorf("%w: memory artifact version does not belong to artifact", ErrAgentMemoryMutationForbidden)
	}
	return nil
}
