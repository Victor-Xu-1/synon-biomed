// Package workspace owns durable SynonBiomed-compatible workspace state.
//
// It deliberately uses SQLite rather than the legacy JSON indexes because
// artifact lineage and future cross-process scheduling need transactions.
package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const sqliteDriver = "sqlite"

// workspaceReadPoolMaxConnections keeps one workspace from multiplying the
// host CPU count into an unbounded number of SQLite readers. Read traffic is
// owner-scoped and short-lived; a small fixed ceiling protects the process
// when many workspace and realtime requests are active at once.
const workspaceReadPoolMaxConnections = 8

// ErrWorkspaceStoreClosed reports that the workspace store has been closed and
// cannot accept further reads or writes. In-process retry loops must treat it
// as permanent instead of retrying forever.
var ErrWorkspaceStoreClosed = errors.New("workspace store is closed")

type Store struct {
	db                    *sql.DB
	readDB                *sql.DB
	now                   func() time.Time
	blobRoot              string
	schemaBackupPath      string
	attachmentMu          sync.Mutex
	branchMu              sync.Mutex
	outboxWakeMu          sync.Mutex
	outboxWake            chan struct{}
	kernelRetentionWakeMu sync.Mutex
	kernelRetentionWake   chan struct{}
	transcriptMu          sync.Mutex
	transcriptRepository  *transcriptstore.Repository
}

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type CreateProjectInput struct {
	ID     string
	UserID string
	Name   string
	Path   string
}

type UpdateProjectInput struct {
	Name *string
	Path *string
}

type Frame struct {
	ID                   string         `json:"id"`
	IncarnationID        string         `json:"-"`
	ProjectID            string         `json:"projectId"`
	ParentFrameID        string         `json:"parentFrameId,omitempty"`
	RootFrameID          string         `json:"rootFrameId"`
	RootSequence         int64          `json:"rootSequence"`
	AgentName            string         `json:"agentName"`
	Status               string         `json:"status"`
	ConversationType     string         `json:"conversationType"`
	Name                 string         `json:"name,omitempty"`
	DelegateName         string         `json:"delegateName,omitempty"`
	ContextData          map[string]any `json:"contextData,omitempty"`
	InputData            map[string]any `json:"inputData,omitempty"`
	OutputData           map[string]any `json:"outputData,omitempty"`
	MessageCount         int            `json:"messageCount"`
	CompletedAt          *time.Time     `json:"completedAt,omitempty"`
	Model                string         `json:"model,omitempty"`
	Effort               string         `json:"effort,omitempty"`
	InputTokens          *int64         `json:"inputTokens,omitempty"`
	OutputTokens         *int64         `json:"outputTokens,omitempty"`
	CacheReadTokens      *int64         `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens     *int64         `json:"cacheWriteTokens,omitempty"`
	TotalCost            *float64       `json:"totalCost,omitempty"`
	AuxInputTokens       *int64         `json:"auxInputTokens,omitempty"`
	AuxOutputTokens      *int64         `json:"auxOutputTokens,omitempty"`
	AuxCacheReadTokens   *int64         `json:"auxCacheReadTokens,omitempty"`
	AuxCacheWriteTokens  *int64         `json:"auxCacheWriteTokens,omitempty"`
	AuxCost              *float64       `json:"auxCost,omitempty"`
	TaskSummary          string         `json:"taskSummary,omitempty"`
	StatusDescription    string         `json:"statusDescription,omitempty"`
	MentionedArtifactIDs []string       `json:"mentionedArtifactIds,omitempty"`
	SpecialistsUsed      []string       `json:"specialistsUsed,omitempty"`
	IsHidden             bool           `json:"isHidden"`
	CreatedAt            time.Time      `json:"createdAt"`
	UpdatedAt            time.Time      `json:"updatedAt"`
}

type FrameRealtimeContext struct {
	UserID string
	Frame  Frame
}

type CreateFrameInput struct {
	ID               string
	ProjectID        string
	ParentFrameID    string
	AgentName        string
	Status           string
	ConversationType string
	Name             string
}

type UpdateFrameInput struct {
	Status *string
	Name   *string
}

type Agent struct {
	ID                  string    `json:"id"`
	UserID              string    `json:"userId"`
	Name                string    `json:"name"`
	DisplayName         string    `json:"displayName"`
	Description         string    `json:"description"`
	SystemPrompt        string    `json:"systemPrompt"`
	IconKey             string    `json:"iconKey,omitempty"`
	ColorKey            string    `json:"colorKey,omitempty"`
	Tags                []string  `json:"tags,omitempty"`
	SkillNames          []string  `json:"skillNames"`
	SkillTombstones     []string  `json:"skillTombstones"`
	ConnectorTombstones []string  `json:"connectorTombstones"`
	Unrestricted        bool      `json:"unrestricted"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type CreateAgentInput struct {
	ID                  string
	UserID              string
	Name                string
	DisplayName         string
	Description         string
	SystemPrompt        string
	IconKey             string
	ColorKey            string
	Tags                []string
	SkillNames          []string
	SkillTombstones     []string
	ConnectorTombstones []string
	Unrestricted        bool
	Enabled             *bool
}

type Artifact struct {
	ID                   string           `json:"id"`
	ProjectID            string           `json:"projectId"`
	Name                 string           `json:"name"`
	Kind                 string           `json:"kind"`
	CurrentVersionNumber int              `json:"currentVersionNumber"`
	FolderID             string           `json:"folderId,omitempty"`
	Priority             ArtifactPriority `json:"priority"`
	CreatedAt            time.Time        `json:"createdAt"`
	UpdatedAt            time.Time        `json:"updatedAt"`
}

type ArtifactVersion struct {
	ID            string    `json:"id"`
	ArtifactID    string    `json:"artifactId"`
	VersionNumber int       `json:"versionNumber"`
	ParentID      string    `json:"parentId,omitempty"`
	Content       []byte    `json:"content"`
	ContentSHA256 string    `json:"contentSha256"`
	StoragePath   string    `json:"-"`
	SizeBytes     int64     `json:"sizeBytes"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

type SaveArtifactVersionInput struct {
	ArtifactID      string
	ProjectID       string
	Name            string
	Kind            string
	Content         []byte
	CreatedBy       string
	ParentVersionID string
}

// Open creates or upgrades an embedded workspace database at path.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("workspace database path is required")
	}
	path, existed, err := prepareWorkspaceDatabasePath(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		return nil, fmt.Errorf("open workspace database: %w", err)
	}
	db.SetMaxOpenConns(1)
	backupPath, err := backupWorkspaceBeforeSchemaUpgrade(context.Background(), db, path, existed)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	blobRoot := path + ".blobs"
	if err := ensurePrivateDirectory(blobRoot); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prepare workspace blob root: %w", err)
	}
	store := &Store{
		db: db, now: time.Now, blobRoot: blobRoot, schemaBackupPath: backupPath,
		outboxWake: make(chan struct{}), kernelRetentionWake: make(chan struct{}),
	}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	readDB, err := openWorkspaceReadPool(path)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store.readDB = readDB
	if err := store.reconcileArtifactBlobs(context.Background()); err != nil {
		_ = readDB.Close()
		_ = db.Close()
		return nil, err
	}
	if err := pruneWorkspaceSchemaBackups(path, backupPath); err != nil {
		_ = readDB.Close()
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = readDB.Close()
		_ = db.Close()
		return nil, fmt.Errorf("secure workspace database: %w", err)
	}
	return store, nil
}

// OpenExisting attaches a secondary runtime process to an already-migrated
// workspace database. It deliberately performs no schema DDL, backup, blob
// reconciliation, chmod, or directory creation. Schema ownership remains with
// the primary service; executors fail closed when the database is absent or
// its current contract cannot be validated.
func OpenExisting(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("existing workspace database path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect existing workspace database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("existing workspace database must be a regular file")
	}
	databaseURL := sqliteFileDatabaseURL(path)
	db, err := sql.Open(sqliteDriver,
		databaseURL+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open existing workspace database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{
		db: db, now: time.Now, blobRoot: path + ".blobs",
		outboxWake: make(chan struct{}), kernelRetentionWake: make(chan struct{}),
	}
	readDB, err := openWorkspaceReadPool(path)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	store.readDB = readDB
	if _, err := store.TranscriptRepository(context.Background()); err != nil {
		_ = readDB.Close()
		_ = db.Close()
		return nil, fmt.Errorf("validate existing workspace database: %w", err)
	}
	return store, nil
}

func ensureParent(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(path, 0o700)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must not be a symbolic link", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, 0o700)
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	var readErr error
	if s.readDB != nil {
		readErr = s.readDB.Close()
		s.readDB = nil
	}
	s.transcriptMu.Lock()
	s.transcriptRepository = nil
	s.transcriptMu.Unlock()
	writeErr := s.db.Close()
	s.db = nil
	return errors.Join(readErr, writeErr)
}

func openWorkspaceReadPool(path string) (*sql.DB, error) {
	readURL := sqliteFileDatabaseURL(path)
	readDB, err := sql.Open(sqliteDriver, readURL+"?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("open workspace read pool: %w", err)
	}
	connections := min(max(runtime.GOMAXPROCS(0), 2), workspaceReadPoolMaxConnections)
	readDB.SetMaxOpenConns(connections)
	readDB.SetMaxIdleConns(connections)
	if err := readDB.PingContext(context.Background()); err != nil {
		_ = readDB.Close()
		return nil, fmt.Errorf("validate workspace read pool: %w", err)
	}
	return readDB, nil
}

func (s *Store) readDatabase() *sql.DB {
	if s == nil {
		return nil
	}
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}

func sqliteFileDatabaseURL(path string) string {
	slashed := filepath.ToSlash(path)
	// net/url otherwise renders a Windows drive path as file://C:/..., which
	// SQLite parses as a non-local URI authority named "C:". Keep the drive
	// inside the URI path so both primary and secondary Windows runtimes use the
	// same local database.
	if len(slashed) >= 2 && slashed[1] == ':' && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

// CreateProject persists a project and makes the stable ID explicit so imports
// can retain references from the previous runtime.
