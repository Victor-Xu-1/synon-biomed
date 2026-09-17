// Package runtimekv owns bounded operational state that is not conversation
// history. It uses SQLite so one entry can be updated without rewriting every
// audit, approval, and runtime projection in the process.
package runtimekv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Namespace string    `json:"namespace"`
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Store struct {
	mu       sync.Mutex
	path     string
	now      func() time.Time
	initOnce sync.Once
	db       *sql.DB
	initErr  error
	closed   bool
}

// New opens a lazily initialized SQLite operational-state store. Production
// uses runtime-state.sqlite and never the retired whole-file JSON authority.
func New(path string) *Store {
	return &Store{path: strings.TrimSpace(path), now: time.Now}
}

func (s *Store) Set(namespace string, key string, value any) (Entry, error) {
	return s.SetContext(context.Background(), namespace, key, value)
}

func (s *Store) SetContext(ctx context.Context, namespace string, key string, value any) (Entry, error) {
	if err := validateNamespace(namespace); err != nil {
		return Entry{}, err
	}
	if err := validateKey(key); err != nil {
		return Entry{}, err
	}
	if err := s.lockContext(ctx); err != nil {
		return Entry{}, err
	}
	defer s.mu.Unlock()
	if err := s.ensureDatabase(); err != nil {
		return Entry{}, err
	}
	raw, cloned, err := encodeValue(value)
	if err != nil {
		return Entry{}, err
	}
	scope := inferScope(namespace, key, cloned)
	updatedAt := s.now().UTC()
	var version int
	if err := s.db.QueryRowContext(ctx, `
		INSERT INTO runtime_state_entries(
			namespace,entry_key,value_json,version,owner_user_id,project_id,root_frame_id,frame_id,updated_at
		) VALUES(?,?,?,1,?,?,?,?,?)
		ON CONFLICT(namespace,entry_key) DO UPDATE SET
			value_json=excluded.value_json,
			version=runtime_state_entries.version+1,
			owner_user_id=excluded.owner_user_id,
			project_id=excluded.project_id,
			root_frame_id=excluded.root_frame_id,
			frame_id=excluded.frame_id,
			updated_at=excluded.updated_at
		RETURNING version`,
		namespace, key, raw, scope.OwnerUserID, scope.ProjectID, scope.RootFrameID, scope.FrameID, updatedAt,
	).Scan(&version); err != nil {
		return Entry{}, fmt.Errorf("set runtime state: %w", err)
	}
	return Entry{Namespace: namespace, Key: key, Value: cloned, Version: version, UpdatedAt: updatedAt}, nil
}

func (s *Store) lockContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if s.mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// EditNamespace performs one atomic namespace edit. Values supplied to the
// callback are detached from SQLite, so changed=false discards nested edits.
func (s *Store) EditNamespace(namespace string, edit func(map[string]Entry) (bool, error)) error {
	if err := validateNamespace(namespace); err != nil {
		return err
	}
	if edit == nil {
		return errors.New("runtime namespace edit callback is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDatabase(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin runtime namespace edit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	entries, err := listNamespaceTx(tx, namespace)
	if err != nil {
		return err
	}
	changed, err := edit(entries)
	if err != nil || !changed {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM runtime_state_entries WHERE namespace=?`, namespace); err != nil {
		return fmt.Errorf("replace runtime namespace: %w", err)
	}
	for key, entry := range entries {
		if err := validateKey(key); err != nil {
			return err
		}
		if entry.Namespace != "" && entry.Namespace != namespace {
			return errors.New("runtime namespace edit changed entry namespace")
		}
		if entry.Key != "" && entry.Key != key {
			return errors.New("runtime namespace edit changed entry key")
		}
		if entry.Version <= 0 {
			entry.Version = 1
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = s.now().UTC()
		}
		raw, cloned, err := encodeValue(entry.Value)
		if err != nil {
			return err
		}
		scope := inferScope(namespace, key, cloned)
		if _, err := tx.Exec(`INSERT INTO runtime_state_entries(
			namespace,entry_key,value_json,version,owner_user_id,project_id,root_frame_id,frame_id,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?)`, namespace, key, raw, entry.Version,
			scope.OwnerUserID, scope.ProjectID, scope.RootFrameID, scope.FrameID, entry.UpdatedAt.UTC()); err != nil {
			return fmt.Errorf("write runtime namespace entry: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit runtime namespace edit: %w", err)
	}
	return nil
}

func (s *Store) Get(namespace string, key string) (Entry, bool, error) {
	if err := validateNamespace(namespace); err != nil {
		return Entry{}, false, err
	}
	if err := validateKey(key); err != nil {
		return Entry{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDatabase(); err != nil {
		return Entry{}, false, err
	}
	entry, err := scanEntry(s.db.QueryRow(`SELECT namespace,entry_key,value_json,version,updated_at
		FROM runtime_state_entries WHERE namespace=? AND entry_key=?`, namespace, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

func (s *Store) List(namespace string) ([]Entry, error) {
	if namespace != "" {
		if err := validateNamespace(namespace); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDatabase(); err != nil {
		return nil, err
	}
	query := `SELECT namespace,entry_key,value_json,version,updated_at FROM runtime_state_entries`
	args := []any{}
	if namespace != "" {
		query += ` WHERE namespace=?`
		args = append(args, namespace)
	}
	query += ` ORDER BY namespace,entry_key`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runtime state: %w", err)
	}
	defer rows.Close()
	entries := make([]Entry, 0)
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// ListReadOnly keeps the historical API name. SQLite already returns detached
// values, so the safe read path is identical to List.
func (s *Store) ListReadOnly(namespace string) ([]Entry, error) {
	return s.List(namespace)
}

func (s *Store) Delete(namespace string, key string) (bool, error) {
	if err := validateNamespace(namespace); err != nil {
		return false, err
	}
	if err := validateKey(key); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDatabase(); err != nil {
		return false, err
	}
	result, err := s.db.Exec(`DELETE FROM runtime_state_entries WHERE namespace=? AND entry_key=?`, namespace, key)
	if err != nil {
		return false, fmt.Errorf("delete runtime state: %w", err)
	}
	deleted, err := result.RowsAffected()
	return deleted == 1, err
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		s.closed = true
		return s.initErr
	}
	err := s.db.Close()
	s.db = nil
	s.closed = true
	return err
}
