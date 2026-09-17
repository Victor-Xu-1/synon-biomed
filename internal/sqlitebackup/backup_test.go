package sqlitebackup

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBackupFromConnCreatesConsistentPrivateSnapshot(t *testing.T) {
	root := privateTempDir(t)
	sourcePath := filepath.Join(t.TempDir(), "source.sqlite")
	db, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;
		CREATE TABLE parents(id INTEGER PRIMARY KEY);
		CREATE TABLE facts(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parents(id), value TEXT);
		INSERT INTO parents(id) VALUES (1);
		INSERT INTO facts(parent_id, value) VALUES (1, 'before')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO facts(parent_id, value) VALUES (1, 'committed')`); err != nil {
		t.Fatal(err)
	}
	sourceDigest, err := fileDigest(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	result, err := BackupFromConn(context.Background(), conn, root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path == "" || result.Directory == "" || len(result.SHA256) != 64 || result.SizeBytes <= 0 {
		t.Fatalf("backup result = %#v", result)
	}
	info, err := os.Lstat(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v", info.Mode())
	}
	assertSnapshotRows(t, result.Path, 2)
	afterBackupDigest, err := fileDigest(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if afterBackupDigest != sourceDigest {
		t.Fatalf("source database bytes changed: before=%s after=%s", sourceDigest, afterBackupDigest)
	}
	if _, err := db.Exec(`INSERT INTO facts(parent_id, value) VALUES (1, 'after')`); err != nil {
		t.Fatal(err)
	}
	assertSnapshotRows(t, result.Path, 2)
}

func TestBackupFromConnRejectsUnsafeRootsAndCleansFailures(t *testing.T) {
	source, conn := sqliteSourceConnection(t)
	_ = source

	unsafe := t.TempDir()
	if err := os.Chmod(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupFromConn(context.Background(), conn, unsafe); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("unsafe root error = %v", err)
	}

	realRoot := privateTempDir(t)
	symlink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupFromConn(context.Background(), conn, symlink); err == nil || !strings.Contains(err.Error(), "symbolic") {
		t.Fatalf("symlink root error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BackupFromConn(cancelled, conn, realRoot); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	entries, err := os.ReadDir(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial backup directories retained: %#v", entries)
	}
}

func TestBackupFromConnRejectsUnsupportedDriverAndConcurrentCallsAreIsolated(t *testing.T) {
	registerUnsupportedDriver.Do(func() { sql.Register("sqlitebackup-unsupported", unsupportedDriver{}) })
	db, err := sql.Open("sqlitebackup-unsupported", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BackupFromConn(context.Background(), conn, privateTempDir(t)); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported driver error = %v", err)
	}
	_ = conn.Close()

	_, source := sqliteSourceConnection(t)
	root := privateTempDir(t)
	results := make(chan Result, 2)
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := BackupFromConn(context.Background(), source, root)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	close(results)
	paths := map[string]bool{}
	for result := range results {
		paths[result.Path] = true
		assertSnapshotRows(t, result.Path, 1)
	}
	if len(paths) != 2 {
		t.Fatalf("backup paths = %#v", paths)
	}
}

func sqliteSourceConnection(t *testing.T) (string, *sql.Conn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE facts(id INTEGER PRIMARY KEY, value TEXT); INSERT INTO facts(value) VALUES ('value')`); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return path, conn
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertSnapshotRows(t *testing.T, path string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", filepath.ToSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var quick string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick check = %q err=%v", quick, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM facts`).Scan(&count); err != nil || count != want {
		t.Fatalf("snapshot rows = %d, want %d, err=%v", count, want, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("snapshot contains a foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

var registerUnsupportedDriver sync.Once

type unsupportedDriver struct{}

func (unsupportedDriver) Open(string) (driver.Conn, error) { return unsupportedConn{}, nil }

type unsupportedConn struct{}

func (unsupportedConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unsupported") }
func (unsupportedConn) Close() error                        { return nil }
func (unsupportedConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }
