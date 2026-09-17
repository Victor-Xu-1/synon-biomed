package runtimekv

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const sqliteDriver = "sqlite"

func (s *Store) ensureDatabase() error {
	if s == nil {
		return errors.New("runtime store is not configured")
	}
	if s.closed {
		return errors.New("runtime store is closed")
	}
	s.initOnce.Do(func() {
		s.initializeDatabase()
	})
	return s.initErr
}

func (s *Store) initializeDatabase() {
	if s.path == "" {
		s.initErr = errors.New("runtime store path is not configured")
		return
	}
	absolute, err := filepath.Abs(s.path)
	if err != nil {
		s.initErr = fmt.Errorf("resolve runtime store path: %w", err)
		return
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		s.initErr = fmt.Errorf("prepare runtime store directory: %w", err)
		return
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || resolvedParent != parent {
		s.initErr = errors.New("runtime store parent must not contain symbolic links")
		return
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		s.initErr = fmt.Errorf("secure runtime store directory: %w", err)
		return
	}
	if info, err := os.Lstat(absolute); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			s.initErr = errors.New("runtime store must be a regular file")
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.initErr = fmt.Errorf("inspect runtime store: %w", err)
		return
	}
	db, err := sql.Open(sqliteDriver, runtimeSQLiteURL(absolute)+
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		s.initErr = fmt.Errorf("open runtime store: %w", err)
		return
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(runtimeStateSchema); err != nil {
		_ = db.Close()
		s.initErr = fmt.Errorf("initialize runtime store: %w", err)
		return
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		_ = db.Close()
		s.initErr = fmt.Errorf("secure runtime store: %w", err)
		return
	}
	s.path = absolute
	s.db = db
}

const runtimeStateSchema = `CREATE TABLE IF NOT EXISTS runtime_state_entries(
	namespace TEXT NOT NULL,
	entry_key TEXT NOT NULL,
	value_json BLOB NOT NULL CHECK(json_valid(value_json)),
	version INTEGER NOT NULL CHECK(version>0),
	owner_user_id TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL DEFAULT '',
	root_frame_id TEXT NOT NULL DEFAULT '',
	frame_id TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP NOT NULL,
	PRIMARY KEY(namespace,entry_key)
);
CREATE INDEX IF NOT EXISTS runtime_state_updated_idx ON runtime_state_entries(namespace,updated_at);
CREATE INDEX IF NOT EXISTS runtime_state_scope_idx ON runtime_state_entries(project_id,root_frame_id,frame_id);`

func runtimeSQLiteURL(path string) string {
	slashed := filepath.ToSlash(path)
	if len(slashed) >= 2 && slashed[1] == ':' && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

type entryScanner interface {
	Scan(...any) error
}

func scanEntry(scanner entryScanner) (Entry, error) {
	var entry Entry
	var raw []byte
	if err := scanner.Scan(&entry.Namespace, &entry.Key, &raw, &entry.Version, &entry.UpdatedAt); err != nil {
		return Entry{}, err
	}
	if err := json.Unmarshal(raw, &entry.Value); err != nil {
		return Entry{}, fmt.Errorf("decode runtime state %s/%s: %w", entry.Namespace, entry.Key, err)
	}
	return entry, nil
}

func listNamespaceTx(tx *sql.Tx, namespace string) (map[string]Entry, error) {
	rows, err := tx.Query(`SELECT namespace,entry_key,value_json,version,updated_at
		FROM runtime_state_entries WHERE namespace=? ORDER BY entry_key`, namespace)
	if err != nil {
		return nil, fmt.Errorf("read runtime namespace: %w", err)
	}
	defer rows.Close()
	entries := map[string]Entry{}
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries[entry.Key] = entry
	}
	return entries, rows.Err()
}

func encodeValue(value any) ([]byte, any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("encode runtime state: %w", err)
	}
	var cloned any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, nil, fmt.Errorf("clone runtime state: %w", err)
	}
	return raw, cloned, nil
}

func validateNamespace(namespace string) error {
	if namespace == "" {
		return errors.New("runtime namespace is required")
	}
	return validateSegment("runtime namespace", namespace)
}

func validateKey(key string) error {
	if key == "" {
		return errors.New("runtime key is required")
	}
	return validateSegment("runtime key", key)
}

func validateSegment(label string, value string) error {
	if strings.Contains(value, "..") {
		return fmt.Errorf("invalid %s: %s", label, value)
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '-' || r == '.' || r == ':' {
			continue
		}
		return fmt.Errorf("invalid %s: %s", label, value)
	}
	return nil
}
