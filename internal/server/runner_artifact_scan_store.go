package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"synon-go/internal/runtimecontrol"
)

// This private, disposable scan index is derived only from immutable artifact
// bytes. It is never a task ledger or publication authority. SQLite provides
// external sorting/indexing without retaining entire tables in the Go heap.
// The directory and transaction are removed on every exit, including cancel.
type runnerArtifactScanStore struct {
	ctx       context.Context
	directory string
	db        *sql.DB
	tx        *sql.Tx
	nextScope int64
}

func newRunnerArtifactScanStore(ctx context.Context) (_ *runnerArtifactScanStore, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "synon-artifact-scan-*")
	if err != nil {
		return nil, err
	}
	store := &runnerArtifactScanStore{ctx: ctx, directory: directory}
	defer func() {
		if resultErr != nil {
			_ = store.Close()
		}
	}()
	if err := runtimecontrol.CheckDiskCapacity(directory, 0); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "scan.sqlite")
	store.db, err = sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store.db.SetMaxOpenConns(1)
	for _, statement := range []string{"PRAGMA temp_store=FILE", "PRAGMA cache_size=-4096", "PRAGMA mmap_size=0", "PRAGMA journal_mode=DELETE"} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	store.tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, statement := range []string{
		`CREATE TABLE scan_rows(scope INTEGER NOT NULL, ordinal INTEGER NOT NULL, cells TEXT NOT NULL, PRIMARY KEY(scope,ordinal)) WITHOUT ROWID`,
		`CREATE TABLE scan_keys(scope INTEGER NOT NULL, key TEXT NOT NULL, ordinal INTEGER NOT NULL, PRIMARY KEY(scope,key)) WITHOUT ROWID`,
		`CREATE TABLE scan_numbers(scope INTEGER NOT NULL, identity TEXT NOT NULL, number REAL NOT NULL, ordinal INTEGER NOT NULL, PRIMARY KEY(scope,identity,number,ordinal)) WITHOUT ROWID`,
		`CREATE TABLE rank_values(scope INTEGER NOT NULL, ordinal INTEGER NOT NULL, key TEXT NOT NULL, score REAL NOT NULL, rank REAL NOT NULL, PRIMARY KEY(scope,ordinal)) WITHOUT ROWID`,
		`CREATE TABLE rank_scores(scope INTEGER NOT NULL, score REAL NOT NULL, through_count INTEGER NOT NULL, PRIMARY KEY(scope,score)) WITHOUT ROWID`,
	} {
		if _, err := store.exec(statement); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (store *runnerArtifactScanStore) Close() error {
	if store == nil {
		return nil
	}
	var result error
	if store.tx != nil {
		if err := store.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			result = errors.Join(result, err)
		}
		store.tx = nil
	}
	if store.db != nil {
		result = errors.Join(result, store.db.Close())
		store.db = nil
	}
	if store.directory != "" {
		result = errors.Join(result, os.RemoveAll(store.directory))
		store.directory = ""
	}
	return result
}

func (store *runnerArtifactScanStore) scope() int64 { store.nextScope++; return store.nextScope }

func (store *runnerArtifactScanStore) exec(statement string, arguments ...any) (sql.Result, error) {
	if err := store.ctx.Err(); err != nil {
		return nil, err
	}
	if err := runtimecontrol.CheckDiskCapacity(store.directory, 0); err != nil {
		return nil, err
	}
	return store.tx.ExecContext(store.ctx, statement, arguments...)
}

func (store *runnerArtifactScanStore) appendRow(scope int64, ordinal int, row []string) error {
	encoded, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = store.exec(`INSERT INTO scan_rows(scope,ordinal,cells) VALUES(?,?,?)`, scope, ordinal, string(encoded))
	return err
}

func (table runnerCrossArtifactTable) eachRow(ctx context.Context, visit func(int, []string) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if table.scanStore == nil {
		for index, row := range table.rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(index, row); err != nil {
				return err
			}
		}
		return nil
	}
	rows, err := table.scanStore.tx.QueryContext(ctx, `SELECT ordinal,cells FROM scan_rows WHERE scope=? ORDER BY ordinal`, table.scanScope)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var raw []byte
		if err := rows.Scan(&index, &raw); err != nil {
			return err
		}
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("read artifact scan row: %w", err)
		}
		if err := visit(index, values); err != nil {
			return err
		}
	}
	return rows.Err()
}

var errRunnerArtifactDuplicateScanKey = errors.New("duplicate artifact scan key")

func (store *runnerArtifactScanStore) indexRows(table runnerCrossArtifactTable, keyFor func([]string) string, requireUnique bool) (int64, bool, error) {
	scope := store.scope()
	err := table.eachRow(store.ctx, func(ordinal int, row []string) error {
		if len(row) == 0 {
			return nil
		}
		key := keyFor(row)
		query := `INSERT INTO scan_keys(scope,key,ordinal) VALUES(?,?,?) ON CONFLICT(scope,key) DO UPDATE SET ordinal=excluded.ordinal`
		if requireUnique {
			query = `INSERT INTO scan_keys(scope,key,ordinal) VALUES(?,?,?) ON CONFLICT(scope,key) DO NOTHING`
		}
		result, err := store.exec(query, scope, key, ordinal)
		if err != nil {
			return err
		}
		if requireUnique {
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count == 0 {
				return errRunnerArtifactDuplicateScanKey
			}
		}
		return nil
	})
	if errors.Is(err, errRunnerArtifactDuplicateScanKey) {
		_, cleanupErr := store.exec(`DELETE FROM scan_keys WHERE scope=?`, scope)
		return 0, false, cleanupErr
	}
	return scope, err == nil, err
}

func (store *runnerArtifactScanStore) findIndexedRow(table runnerCrossArtifactTable, index int64, key string) ([]string, bool, error) {
	var ordinal int
	if err := store.tx.QueryRowContext(store.ctx, `SELECT ordinal FROM scan_keys WHERE scope=? AND key=?`, index, key).Scan(&ordinal); errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	if table.scanStore == nil {
		return table.rows[ordinal], true, nil
	}
	var raw []byte
	if err := store.tx.QueryRowContext(store.ctx, `SELECT cells FROM scan_rows WHERE scope=? AND ordinal=?`, table.scanScope, ordinal).Scan(&raw); err != nil {
		return nil, false, err
	}
	var row []string
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, false, err
	}
	return row, true, nil
}
