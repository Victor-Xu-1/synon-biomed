package transcript

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTranscriptDatabaseURLKeepsWindowsDriveInLocalPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows drive URI regression")
	}
	value := transcriptDatabaseURL(`C:\\workspace\\workspace.sqlite`)
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "" || !strings.HasPrefix(value, "file:///C:/") {
		t.Fatalf("database URL=%q parsed=%#v", value, parsed)
	}
}

func TestOpenUsesExistingSQLiteContractWithoutInstallingSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	db, err := sql.Open("sqlite", transcriptDatabaseURL(path))
	if err != nil {
		t.Fatalf("open setup database: %v", err)
	}
	if _, err := db.Exec(transcriptExternalAuthorityFixture); err != nil {
		t.Fatalf("install workspace schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close setup database: %v", err)
	}

	if repository, err := Open(path); !errors.Is(err, ErrSchemaUnavailable) || repository != nil {
		t.Fatalf("open incomplete contract = %#v, %v; want nil, schema unavailable", repository, err)
	}
	check, err := sql.Open("sqlite", transcriptDatabaseURL(path))
	if err != nil {
		t.Fatalf("reopen setup database: %v", err)
	}
	defer check.Close()
	var count int
	if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'transcript_%'`).Scan(&count); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if count != 0 {
		t.Fatalf("Open installed %d transcript tables", count)
	}
}

func TestOpenRejectsMissingAndSymbolicLinkDatabaseWithoutCreatingFiles(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.sqlite")
	if repository, err := Open(missing); err == nil || repository != nil {
		t.Fatalf("open missing database = %#v, %v", repository, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database was created: %v", err)
	}
	target := filepath.Join(root, "target.sqlite")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.sqlite")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" && strings.Contains(strings.ToLower(err.Error()), "privilege") {
			t.Skipf("Windows symlink capability is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if repository, err := Open(link); err == nil || repository != nil {
		t.Fatalf("open symbolic link = %#v, %v", repository, err)
	}
}

func TestOpenPersistsTranscriptAuthorityAcrossRestart(t *testing.T) {
	filename := "workspace #1?.sqlite"
	if runtime.GOOS == "windows" {
		// '?' is not a legal Windows filename character. The URL helper is
		// covered independently above; this fixture only needs a path with
		// spaces and a hash that can be created on every supported platform.
		filename = "workspace #1.sqlite"
	}
	path := filepath.Join(t.TempDir(), filename)
	db, err := sql.Open("sqlite", transcriptDatabaseURL(path))
	if err != nil {
		t.Fatalf("open setup database: %v", err)
	}
	if _, err := db.Exec(transcriptExternalAuthorityFixture); err != nil {
		t.Fatalf("install workspace schema: %v", err)
	}
	for index, statement := range SchemaContractStatements() {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("install transcript statement %d: %v", index, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close setup database: %v", err)
	}

	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open transcript authority: %v", err)
	}
	var foreignKeys, busyTimeout int
	if err := repository.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys=%d err=%v", foreignKeys, err)
	}
	if err := repository.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil || busyTimeout != 5000 {
		t.Fatalf("busy_timeout=%d err=%v", busyTimeout, err)
	}
	stream, err := repository.CreateStream(context.Background(), CreateStreamInput{
		UID: "standalone:session-a", OwnerID: "owner-a", ExternalID: "session-a", SessionID: "session-a",
		Kind: StreamKindStandalone, Epoch: 1,
	})
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if err := repository.Close(); err != nil {
		t.Fatalf("close transcript authority: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen transcript authority: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.GetStream(context.Background(), stream.UID, "owner-a")
	if err != nil || got.SessionID != "session-a" || got.Epoch != 1 {
		t.Fatalf("reopened stream = %#v, %v", got, err)
	}
}
