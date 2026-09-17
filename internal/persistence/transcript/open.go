package transcript

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Open attaches the transcript authority to an existing workspace SQLite
// database. Schema installation remains migration-owned; Open never creates or
// repairs transcript tables.
func Open(path string) (*Repository, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("transcript database path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("transcript database is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("transcript database must be a regular file")
	}
	db, err := sql.Open("sqlite", transcriptDatabaseURL(path)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	repository := NewRepository(db)
	if err := repository.ValidateCurrentContract(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return repository, nil
}

func transcriptDatabaseURL(path string) string {
	slashed := filepath.ToSlash(path)
	if len(slashed) >= 2 && slashed[1] == ':' && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

// Close releases the database connection owned by Open.
func (r *Repository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}
