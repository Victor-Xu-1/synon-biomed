package v11

import "time"

const (
	SchemaName                    = "synonbiomed-v1.1"
	ManifestFilename              = "MIGRATION_V1_1.json"
	WorkspaceDatabaseRelativePath = "workspace/synonbiomed-v1.1.sqlite"
	StatusCompleted               = "completed"
	StatusRolledBack              = "rolled_back"
)

type ConflictPolicy string

const (
	ConflictAbort   ConflictPolicy = "abort"
	ConflictReplace ConflictPolicy = "replace"
)

type Options struct {
	SourceDB       string
	SourceDataDir  string
	TargetHome     string
	ConflictPolicy ConflictPolicy
}

type Inspection struct {
	Schema          string           `json:"schema"`
	SourceDB        string           `json:"sourceDb"`
	SourceSHA256    string           `json:"sourceSha256"`
	SourceBytes     int64            `json:"sourceBytes"`
	MigrationCount  int64            `json:"migrationCount"`
	LatestMigration int64            `json:"latestMigration"`
	QuickCheckOK    bool             `json:"quickCheckOk"`
	TableCounts     map[string]int64 `json:"tableCounts"`
}

type TableDisposition struct {
	Action      string `json:"action"`
	Target      string `json:"target,omitempty"`
	Reason      string `json:"reason"`
	SourceCount int64  `json:"sourceCount"`
}

type Report struct {
	Schema            string                      `json:"schema"`
	Status            string                      `json:"status"`
	MigrationID       string                      `json:"migrationId"`
	SourceDB          string                      `json:"sourceDb"`
	SourceSHA256      string                      `json:"sourceSha256"`
	SourceBytes       int64                       `json:"sourceBytes"`
	SourceSnapshot    string                      `json:"sourceSnapshot,omitempty"`
	TargetHome        string                      `json:"targetHome"`
	TargetDatabase    string                      `json:"targetDatabase"`
	BackupPath        string                      `json:"backupPath,omitempty"`
	Imported          map[string]int64            `json:"imported"`
	SourceTableCounts map[string]int64            `json:"sourceTableCounts"`
	ExcludedTables    map[string]int64            `json:"excludedTables,omitempty"`
	TableDispositions map[string]TableDisposition `json:"tableDispositions"`
	StartedAt         time.Time                   `json:"startedAt"`
	CompletedAt       time.Time                   `json:"completedAt"`
	Idempotent        bool                        `json:"idempotent,omitempty"`
}

type RollbackReport struct {
	Schema                string    `json:"schema"`
	Status                string    `json:"status"`
	MigrationID           string    `json:"migrationId"`
	TargetHome            string    `json:"targetHome"`
	RestoredPath          string    `json:"restoredPath"`
	PreservedMigratedPath string    `json:"preservedMigratedPath"`
	CompletedAt           time.Time `json:"completedAt"`
}

type Verification struct {
	Schema                 string `json:"schema"`
	MigrationID            string `json:"migrationId"`
	TargetHome             string `json:"targetHome"`
	SourceIdentityRecorded bool   `json:"sourceIdentityRecorded"`
	WorkspaceQuickCheckOK  bool   `json:"workspaceQuickCheckOk"`
	ForeignKeysOK          bool   `json:"foreignKeysOk"`
}
