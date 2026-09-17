package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ApplyExtractorMemoryMutations is the private post-completion extraction
// mutation boundary. Interactive write_memory calls use the stronger claimed
// transcript-receipt path; extraction has no model-visible tool invocation, so
// it is fenced by the authenticated owner/project/frame scope instead.
func (s *Store) ApplyExtractorMemoryMutations(ctx context.Context, batch AgentMemoryMutationBatch) (AgentMemoryMutationResult, error) {
	if s == nil || s.db == nil {
		return AgentMemoryMutationResult{}, errors.New("workspace store is closed")
	}
	batch.OwnerUserID = strings.TrimSpace(batch.OwnerUserID)
	batch.ProjectID = strings.TrimSpace(batch.ProjectID)
	batch.FrameID = strings.TrimSpace(batch.FrameID)
	if batch.OwnerUserID == "" || batch.ProjectID == "" {
		return AgentMemoryMutationResult{}, errors.New("extractor memory owner and project are required")
	}
	result := AgentMemoryMutationResult{}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		for index := range batch.Append {
			input := batch.Append[index].Input
			input.UserID = batch.OwnerUserID
			input.Origin = "extractor"
			input.SourceFrameID = batch.FrameID
			projectID, err := validateMemoryScopeTx(ctx, tx, input, batch.OwnerUserID)
			if err != nil {
				return err
			}
			if projectID != "" && projectID != batch.ProjectID {
				return fmt.Errorf("%w: extractor memory belongs to another project", ErrAgentMemoryMutationForbidden)
			}
			batch.Append[index].Input = input
		}
		for _, mutation := range batch.Replace {
			memory, found, err := resolveActiveMemoryOwned(ctx, tx, batch.OwnerUserID, mutation.ActiveID)
			if err != nil || !found {
				return err
			}
			if !validAgentMutableMemoryOrigin(memory.Origin) || memory.Origin != mutation.ExpectedOrigin {
				return ErrAgentMemoryChanged
			}
			if err := requireAgentMemoryScopeTx(ctx, tx, memory, batch.OwnerUserID, batch.ProjectID, batch.FrameID); err != nil {
				return err
			}
		}
		for _, mutation := range batch.Remove {
			memory, found, err := resolveActiveMemoryOwned(ctx, tx, batch.OwnerUserID, mutation.ActiveID)
			if err != nil || !found {
				return err
			}
			if !validAgentMutableMemoryOrigin(memory.Origin) || memory.Origin != mutation.ExpectedOrigin {
				return ErrAgentMemoryChanged
			}
			if err := requireAgentMemoryScopeTx(ctx, tx, memory, batch.OwnerUserID, batch.ProjectID, batch.FrameID); err != nil {
				return err
			}
			if err := validateAgentMemoryRemovalChainTx(ctx, tx, memory.ID, batch.OwnerUserID, batch.ProjectID, batch.FrameID); err != nil {
				return err
			}
		}
		var err error
		result, err = s.applyAgentMemoryMutationBatchTx(ctx, tx, batch)
		return err
	})
	return result, err
}
