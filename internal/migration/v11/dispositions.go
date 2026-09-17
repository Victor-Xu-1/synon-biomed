package v11

import (
	"fmt"
	"sort"
)

const (
	DispositionImport  = "import"
	DispositionArchive = "archive"
	DispositionDiscard = "discard-runtime-state"
)

type tableDispositionSpec struct {
	action string
	target string
	reason string
}

// v11TableDispositions is intentionally exhaustive for the final v1.1 schema.
// Adding a source table without adding a disposition makes migration fail closed.
var v11TableDispositions = map[string]tableDispositionSpec{
	"__drizzle_migrations":      {DispositionDiscard, "", "migration journal is represented by the signed migration report"},
	"agent_skill_assignments":   {DispositionArchive, "", "legacy agent-skill state is retained until assignment activation is complete"},
	"agents":                    {DispositionArchive, "", "legacy agent definitions are retained alongside imported user_agents"},
	"annotations":               {DispositionImport, "annotations", "mapped to unified annotations"},
	"artifact_dependencies":     {DispositionImport, "artifact_version_dependencies", "mapped to version dependency graph"},
	"artifact_folders":          {DispositionImport, "artifact_folders", "schema-compatible import"},
	"artifact_versions":         {DispositionImport, "artifact_versions", "metadata and blob content are imported"},
	"artifacts":                 {DispositionImport, "artifacts", "schema-compatible import"},
	"bundled_agent_settings":    {DispositionArchive, "", "retained until bundled-agent preference activation is complete"},
	"canvas_drafts":             {DispositionArchive, "", "legacy table was dropped by v1.1 migration 0047 but unexpected rows are retained"},
	"capability_settings":       {DispositionImport, "skill_preferences", "skill settings are mapped to scoped preferences"},
	"cloud_credentials":         {DispositionArchive, "", "encrypted legacy credential records are retained without exposing plaintext"},
	"compaction_archives":       {DispositionImport, "compaction_archives", "conversation compaction history is active native state"},
	"compute_pending_terminate": {DispositionImport, "compute_pending_terminate", "pending remote termination intent remains active after migration"},
	"compute_providers":         {DispositionImport, "compute_providers", "mapped to scoped compute providers"},
	"compute_usage":             {DispositionImport, "compute_usage", "usage and workbench job projections are imported"},
	"contact_email_decisions":   {DispositionImport, "contact_email_decisions", "privacy decisions remain active after migration"},
	"content_snapshots":         {DispositionImport, "content_snapshots", "schema-compatible import"},
	"custom_agent_prompts":      {DispositionImport, "agent_custom_prompts", "mapped to custom prompt storage"},
	"custom_mcp_servers":        {DispositionImport, "custom_mcp_servers", "mapped to MCP server catalog"},
	"custom_skills":             {DispositionArchive, "", "custom skill records are retained until managed skill activation is complete"},
	"directory_attachments":     {DispositionImport, "directory_attachments", "schema-compatible import"},
	"events":                    {DispositionImport, "frame_events,realtime_events", "mapped to durable frame and realtime events"},
	"execution_log":             {DispositionImport, "execution_log", "schema-compatible import"},
	"file_annotations":          {DispositionArchive, "", "legacy table was replaced by unified annotations; unexpected rows are retained"},
	"frame_backfill_poison":     {DispositionArchive, "", "failed backfill evidence is retained for audit and repair"},
	"frame_branch_archives":     {DispositionImport, "frame_branch_archives", "schema-compatible import"},
	"frame_messages":            {DispositionImport, "frame_events", "messages are projected into ordered frame events"},
	"frame_read_cursors":        {DispositionImport, "frame_read_cursors", "schema-compatible import"},
	"frame_system_prompts":      {DispositionImport, "frame_system_prompts", "schema-compatible import"},
	"frames":                    {DispositionImport, "frames,frame_runtime_metadata", "split into core and runtime metadata"},
	"host_call_log":             {DispositionArchive, "", "host execution audit records are retained for later activation"},
	"host_grants":               {DispositionArchive, "", "host permission grants are retained until permission activation is complete"},
	"managed_endpoints":         {DispositionImport, "compute_managed_endpoints", "managed endpoint lifecycle state is activated in the compute workbench"},
	"marketplace_sources":       {DispositionArchive, "", "marketplace source configuration is retained for later activation"},
	"mcp_agent_assignments":     {DispositionImport, "mcp_agent_assignments", "schema-compatible import"},
	"mcp_tool_grants":           {DispositionImport, "mcp_tool_grants", "mapped to per-agent grants"},
	"memories":                  {DispositionImport, "memories", "memory and direct category relationship are imported"},
	"memory_categories":         {DispositionImport, "memory_categories", "schema-compatible import"},
	"notes":                     {DispositionImport, "notes", "schema-compatible import"},
	"notifications":             {DispositionArchive, "", "durable user notifications are retained for later activation"},
	"oauth_tokens":              {DispositionImport, "mcp_oauth_status", "token references and OAuth metadata are imported"},
	"poller_lease":              {DispositionDiscard, "", "process lease is host-local and must be reacquired by the new runtime"},
	"projects":                  {DispositionImport, "projects", "schema-compatible import"},
	"queued_user_messages":      {DispositionImport, "queued_user_messages", "schema-compatible import"},
	"routine_schedules":         {DispositionImport, "routine_schedules", "schema-compatible import"},
	"safety_feedback":           {DispositionArchive, "", "durable safety feedback is retained for later activation"},
	"session_claims":            {DispositionImport, "session_claims", "verification claims are durable semantic state, not process ownership leases"},
	"session_concurrency":       {DispositionDiscard, "", "process-local concurrency ownership must be reacquired"},
	"session_seen_marks":        {DispositionImport, "session_seen_marks", "schema-compatible import"},
	"skill_license_assents":     {DispositionArchive, "", "license assent evidence is retained for later activation"},
	"synon_llm_api_keys":        {DispositionArchive, "", "legacy encrypted model credentials are retained for controlled conversion"},
	"transcript_annotations":    {DispositionImport, "transcript_annotations", "schema-compatible import into the active transcript annotation service"},
	"use_intent_declarations":   {DispositionImport, "use_intent_declarations", "schema-compatible import"},
	"user_agents":               {DispositionImport, "user_agents", "schema-compatible import"},
	"user_secrets":              {DispositionImport, "legacy_user_secrets", "encrypted credentials are converted into the target vault"},
	"verification_checks":       {DispositionImport, "verification_checks", "verification evidence remains queryable and actionable after migration"},
}

func planTableDispositions(sourceCounts map[string]int64) (map[string]TableDisposition, error) {
	unknown := make([]string, 0)
	result := make(map[string]TableDisposition, len(sourceCounts))
	for table, count := range sourceCounts {
		spec, ok := v11TableDispositions[table]
		if !ok {
			if count > 0 {
				unknown = append(unknown, table)
			}
			continue
		}
		target := spec.target
		if spec.action == DispositionArchive {
			target = archivedTableName(table)
		}
		result[table] = TableDisposition{Action: spec.action, Target: target, Reason: spec.reason, SourceCount: count}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("v1.1 migration has non-empty tables without an explicit disposition: %v", unknown)
	}
	return result, nil
}

func archivedTableName(source string) string {
	return "legacy_v11_" + source
}

func discardedSourceTables(dispositions map[string]TableDisposition) map[string]int64 {
	discarded := map[string]int64{}
	for table, disposition := range dispositions {
		if disposition.Action == DispositionDiscard && disposition.SourceCount > 0 {
			discarded[table] = disposition.SourceCount
		}
	}
	return discarded
}
