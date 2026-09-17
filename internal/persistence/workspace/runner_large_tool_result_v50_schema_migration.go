package workspace

const (
	runnerLargeToolResultV50CallbackID        = "runner-large-tool-result-v50-noop"
	runnerLargeToolResultV50PreflightIdentity = "runner-large-tool-result-v50-empty-v1"
	runnerLargeToolResultV50RuleSpec          = "synon.workspace.runner-large-tool-result.v50"
)

const runnerLargeToolResultV50DDL = `CREATE TABLE runner_large_tool_results (
	artifact_id TEXT PRIMARY KEY CHECK(
		length(artifact_id)=50 AND artifact_id GLOB 'large-tool-result-*'
	),
	version_id TEXT NOT NULL UNIQUE CHECK(version_id GLOB 'ltr-*'),
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
	root_frame_id TEXT NOT NULL,
	frame_id TEXT NOT NULL REFERENCES frames(id) ON DELETE RESTRICT,
	stream_uid TEXT NOT NULL,
	owner_user_id TEXT NOT NULL,
	runner_id TEXT NOT NULL,
	claim_token TEXT NOT NULL,
	attempt INTEGER NOT NULL CHECK(attempt >= 0),
	source_event_id INTEGER NOT NULL CHECK(source_event_id > 0),
	tool_name TEXT NOT NULL,
	tool_call_id TEXT NOT NULL,
	content_type TEXT NOT NULL CHECK(content_type = 'application/json'),
	size_bytes INTEGER NOT NULL CHECK(size_bytes > 0),
	content_sha256 TEXT NOT NULL CHECK(
		length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'
	),
	storage_path TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL
) STRICT`

var runnerLargeToolResultV50Migration = versionedSchemaMigration{
	version: 50,
	name:    "runner-large-tool-result-internal-evidence",
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        runnerLargeToolResultV50CallbackID,
		RuleSpec:          runnerLargeToolResultV50RuleSpec,
		PreflightIdentity: runnerLargeToolResultV50PreflightIdentity,
	},
	statements: []string{
		runnerLargeToolResultV50DDL,
		`CREATE INDEX runner_large_tool_results_project_idx
			ON runner_large_tool_results(project_id, created_at DESC)`,
		`CREATE INDEX runner_large_tool_results_stream_idx
			ON runner_large_tool_results(stream_uid, created_at DESC)`,
		`CREATE INDEX runner_large_tool_results_tool_idx
			ON runner_large_tool_results(stream_uid, tool_call_id, tool_name)`,
		`CREATE TRIGGER runner_large_tool_result_immutable
			BEFORE UPDATE ON runner_large_tool_results
			BEGIN SELECT RAISE(ABORT,'runner large tool result is immutable'); END`,
		`CREATE TRIGGER runner_large_tool_result_delete_forbidden
			BEFORE DELETE ON runner_large_tool_results
			BEGIN SELECT RAISE(ABORT,'runner large tool result is append-only'); END`,
	},
}
