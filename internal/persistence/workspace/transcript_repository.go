package workspace

import (
	"context"
	"errors"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// TranscriptRepository returns the transcript authority over the same SQLite
// pool used by workspace state. The Store retains ownership of the pool.
func (s *Store) TranscriptRepository(ctx context.Context) (*transcriptstore.Repository, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	s.transcriptMu.Lock()
	defer s.transcriptMu.Unlock()
	if s.transcriptRepository != nil {
		return s.transcriptRepository, nil
	}
	status, err := s.SchemaStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.CurrentVersion != workspaceSchemaVersion || workspaceSchemaVersion < 24 {
		return nil, errors.New("workspace transcript schema is unavailable")
	}
	repository := transcriptstore.NewRepositoryWithReadPool(s.db, s.readDB)
	if err := repository.ValidateCurrentContract(ctx); err != nil {
		return nil, err
	}
	s.transcriptRepository = repository
	return repository, nil
}

// TranscriptWebReadModel returns the derived, rebuildable Web message index.
// Canonical Transcript events remain the sole history authority; the Store
// retains ownership of both the serialized writer and query-only reader pools.
func (s *Store) TranscriptWebReadModel(ctx context.Context) (*transcriptstore.WebReadModelRepository, error) {
	if s == nil || s.db == nil || s.readDB == nil {
		return nil, errors.New("workspace store is closed")
	}
	if _, err := s.TranscriptRepository(ctx); err != nil {
		return nil, err
	}
	return transcriptstore.NewWebReadModelRepository(s.db, s.readDB), nil
}
