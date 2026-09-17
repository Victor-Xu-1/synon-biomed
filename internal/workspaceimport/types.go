package workspaceimport

const (
	PlanVersion               = 1
	WorkspaceDatabaseRelative = "workspace/synonbiomed-v1.1.sqlite"
	stagingWorkingSpaceBytes  = int64(64 << 20)
)

type SourceKind string

const (
	SourceKindCurrent SourceKind = "synon-workspace"
	SourceKindV11     SourceKind = "claude-science-v1.1"
	SourceKindUnknown SourceKind = "unknown"
)

type Options struct {
	SourcePath   string
	TargetHome   string
	IncludePaths bool
}

type Issue struct {
	Code     string `json:"code"`
	SourceID string `json:"sourceId,omitempty"`
}

type Source struct {
	ID                string         `json:"id"`
	Kind              SourceKind     `json:"kind"`
	DatabasePath      string         `json:"databasePath,omitempty"`
	DataRoot          string         `json:"dataRoot,omitempty"`
	DatabaseSHA256    string         `json:"databaseSha256"`
	DatabaseBytes     int64          `json:"databaseBytes"`
	ExternalBytes     int64          `json:"externalBytes"`
	ExternalFileCount int            `json:"externalFileCount"`
	ExternalManifest  string         `json:"externalManifestSha256,omitempty"`
	SchemaVersion     int            `json:"schemaVersion"`
	SchemaTarget      int            `json:"schemaTarget"`
	SchemaJournalHash string         `json:"schemaJournalSha256,omitempty"`
	DistinctOwners    int            `json:"distinctOwnerCount"`
	UnresolvedOwners  int64          `json:"unresolvedOwners"`
	EntityCounts      map[string]int `json:"entityCounts"`
	ActiveAuthorities map[string]int `json:"activeAuthorities"`
}

type Plan struct {
	Version                int      `json:"version"`
	ReadyForStagedApply    bool     `json:"readyForStagedApply"`
	PlanSHA256             string   `json:"planSha256"`
	Sources                []Source `json:"sources"`
	Issues                 []Issue  `json:"issues"`
	EstimatedSourceBytes   int64    `json:"estimatedSourceBytes"`
	EstimatedStagingBytes  int64    `json:"estimatedStagingBytes"`
	TargetAvailableBytes   int64    `json:"targetAvailableBytes,omitempty"`
	TargetCapacityMeasured bool     `json:"targetCapacityMeasured"`
	TargetSnapshotBytes    int64    `json:"targetSnapshotBytes,omitempty"`
	TargetSnapshotFiles    int      `json:"targetSnapshotFiles,omitempty"`
	TargetSnapshotSHA256   string   `json:"targetSnapshotSha256,omitempty"`
}

type discoveredSource struct {
	database string
	dataRoot string
}
