package v11

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	secretstore "synon-go/internal/persistence/secrets"
	settingsstore "synon-go/internal/persistence/settings"
	workspace "synon-go/internal/persistence/workspace"

	_ "modernc.org/sqlite"
)

func TestMigratePreservesV11StateAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceHome := filepath.Join(root, "v11")
	targetHome := filepath.Join(root, "go")
	fixture := writeV11Fixture(t, sourceHome, true)
	sourceBefore := hashFile(t, fixture.Database)

	inspection, err := Inspect(ctx, fixture.Database)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Schema != SchemaName || inspection.MigrationCount != 94 || inspection.TableCounts["projects"] != 1 || inspection.TableCounts["frames"] != 1 {
		t.Fatalf("inspection = %#v", inspection)
	}
	if len(inspection.SourceSHA256) != 64 || !inspection.QuickCheckOK {
		t.Fatalf("inspection integrity = %#v", inspection)
	}

	report, err := Migrate(ctx, Options{
		SourceDB:       fixture.Database,
		SourceDataDir:  sourceHome,
		TargetHome:     targetHome,
		ConflictPolicy: ConflictAbort,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusCompleted || report.MigrationID == "" || report.SourceSHA256 != inspection.SourceSHA256 {
		t.Fatalf("report = %#v", report)
	}
	for table, want := range map[string]int64{
		"projects": 1, "frames": 1, "frame_messages": 2, "user_agents": 1,
		"artifacts": 1, "artifact_versions": 2, "memories": 2, "routine_schedules": 1,
		"custom_mcp_servers": 1, "mcp_agent_assignments": 1, "mcp_tool_grants": 1,
		"compute_providers": 1, "compute_usage": 1, "model_providers": 2,
		"compute_pending_terminate": 1, "managed_endpoints": 1,
		"artifact_dependencies": 1, "content_snapshots": 1, "execution_log": 1,
		"session_claims": 1, "verification_checks": 1,
		"frame_read_cursors": 1, "frame_system_prompts": 1, "queued_user_messages": 1,
		"transcript_annotations": 1,
		"compaction_archives":    1,
		"session_seen_marks":     1, "memory_categories": 1, "use_intent_declarations": 1,
		"contact_email_decisions": 1,
		"directory_attachments":   1, "user_secrets": 1,
	} {
		if report.Imported[table] != want {
			t.Fatalf("imported[%s] = %d, want %d; report=%#v", table, report.Imported[table], want, report)
		}
	}
	if got := hashFile(t, fixture.Database); got != sourceBefore {
		t.Fatalf("source database changed: before=%s after=%s", sourceBefore, got)
	}

	markerPath := filepath.Join(targetHome, ManifestFilename)
	markerRaw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(markerRaw, []byte(fixture.APIKey)) {
		t.Fatal("migration manifest contains plaintext API key")
	}
	var marker Report
	if err := json.Unmarshal(markerRaw, &marker); err != nil || marker.MigrationID != report.MigrationID {
		t.Fatalf("marker = %#v err=%v", marker, err)
	}
	if report.SourceSnapshot != "" {
		t.Fatalf("clean migration retained source snapshot: %q", report.SourceSnapshot)
	}
	if _, err := os.Stat(filepath.Join(targetHome, ".migration-working")); !os.IsNotExist(err) {
		t.Fatalf("temporary migration snapshot retained: %v", err)
	}

	store, err := workspace.Open(filepath.Join(targetHome, WorkspaceDatabaseRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projects, err := store.ListProjectsForUser(secretstore.DefaultUserID, 10, 0)
	if err != nil || !containsProject(projects, "project-1", "Migration fixture") {
		t.Fatalf("projects = %#v err=%v", projects, err)
	}
	frame, found, err := store.GetFrame("frame-1")
	if err != nil || !found || frame.RootFrameID != "frame-1" || frame.RootSequence != 7 || frame.AgentName != "OPERON" {
		t.Fatalf("frame = %#v found=%v err=%v", frame, found, err)
	}
	transcriptAnnotations, err := store.ListTranscriptAnnotations("frame-1")
	if err != nil || len(transcriptAnnotations) != 1 || transcriptAnnotations[0].ID != "transcript-1" ||
		transcriptAnnotations[0].MessageUUID != "message-2" || transcriptAnnotations[0].Origin != "agent" ||
		transcriptAnnotations[0].ReadAt == nil || transcriptAnnotations[0].Note != "Imported bookmark" {
		t.Fatalf("transcript annotations = %#v err=%v", transcriptAnnotations, err)
	}
	archive, found, err := store.GetCompactionArchive("frame-1", 0)
	if err != nil || !found || archive.ID != "compact-1" || archive.MessageCount != 2 || archive.TokenCount == nil || *archive.TokenCount != 77 || len(archive.Messages) != 2 {
		t.Fatalf("compaction archive = %#v found=%v err=%v", archive, found, err)
	}
	messages, err := store.GetFrameMessagesPage("frame-1", 0, 20)
	if err != nil || len(messages.Messages) < 3 {
		t.Fatalf("messages = %#v err=%v", messages, err)
	}
	agent, found, err := store.GetAgent(secretstore.DefaultUserID, "custom-agent")
	if err != nil || !found || agent.SystemPrompt != "Preserve this prompt" || len(agent.SkillNames) != 1 || agent.SkillNames[0] != "literature-review" {
		t.Fatalf("agent = %#v found=%v err=%v", agent, found, err)
	}
	lineage, err := store.ArtifactLineage("artifact-1")
	if err != nil || len(lineage) != 2 || lineage[0].ParentID != "version-1" || lineage[0].ContentSHA256 != fixture.LatestSHA256 {
		t.Fatalf("lineage = %#v err=%v", lineage, err)
	}
	_, current, reader, found, err := store.OpenCurrentArtifactContent("artifact-1")
	if err != nil || !found {
		t.Fatalf("open artifact found=%v err=%v", found, err)
	}
	defer reader.Close()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != fixture.LatestContent || current.ID != "version-2" {
		t.Fatalf("artifact content=%q version=%#v", content, current)
	}
	memories, err := store.ListActiveMemories(secretstore.DefaultUserID, "project-1")
	if err != nil || len(memories) != 1 || memories[0].ID != "memory-2" {
		t.Fatalf("memories = %#v err=%v", memories, err)
	}
	routine, err := store.GetRoutine("routine-1")
	if err != nil || routine.TickCount != 3 || routine.OwnerUserID != secretstore.DefaultUserID {
		t.Fatalf("routine = %#v err=%v", routine, err)
	}
	contact, found, err := store.LatestContactEmailDecision(secretstore.DefaultUserID)
	if err != nil || !found || contact.ID != "contact-1" || contact.Decision != workspace.ContactEmailDecisionAllowed || contact.Email != "migration@example.test" {
		t.Fatalf("contact email = %#v found=%v err=%v", contact, found, err)
	}
	execution, err := store.ListExecutionLog("frame-1", "version-2")
	if err != nil || len(execution) != 1 || execution[0].ID != "cell-1" || execution[0].Source != `print("ok")` || execution[0].AgentName != "OPERON" {
		t.Fatalf("execution log = %#v err=%v", execution, err)
	}
	rootArtifacts, err := store.ListArtifactsForRoot("frame-1", 10)
	if err != nil || len(rootArtifacts) != 1 || rootArtifacts[0].ID != "artifact-1" {
		t.Fatalf("root artifacts = %#v err=%v", rootArtifacts, err)
	}
	servers, err := store.ListMCPServers(secretstore.DefaultUserID)
	if err != nil || len(servers) != 1 || servers[0].ID != "mcp-1" {
		t.Fatalf("mcp servers = %#v err=%v", servers, err)
	}
	providers, err := store.ListModelProviders(secretstore.DefaultUserID)
	if err != nil || len(providers) != 2 || !containsModelSecretRef(providers, "secret://v11-model-profile-1") {
		t.Fatalf("model providers = %#v err=%v", providers, err)
	}
	secret, found, err := secretstore.New(targetHome).ResolveForUser("v11-model-profile-1", secretstore.DefaultUserID)
	if err != nil || !found || secret.Value != fixture.APIKey {
		t.Fatalf("model secret found=%v err=%v secret=%#v", found, err, secret)
	}
	cloudSecret, found, err := secretstore.New(targetHome).ResolveForUser("user-secret-1", secretstore.DefaultUserID)
	if err != nil || !found || cloudSecret.Credentials["access_key_id"] != "AKIAFIXTURE" || cloudSecret.Credentials["secret_access_key"] != fixture.CloudSecret {
		t.Fatalf("cloud secret found=%v err=%v secret=%#v", found, err, cloudSecret)
	}
	intent, found, err := settingsstore.New(filepath.Join(targetHome, "settings.json")).Get("onboarding.useIntent")
	if err != nil || !found || intent.Value != "drug-discovery" {
		t.Fatalf("migrated use intent = %#v found=%v err=%v", intent, found, err)
	}
	for key, expected := range map[string]any{
		"onboarding.allowlistSeen": true, "onboarding.firstRunCompleted": true,
	} {
		setting, found, err := settingsstore.New(filepath.Join(targetHome, "settings.json")).Get(key)
		if err != nil || !found || setting.Value != expected {
			t.Fatalf("migrated preference %s = %#v found=%v err=%v", key, setting, found, err)
		}
	}
	vaultRaw, err := os.ReadFile(filepath.Join(targetHome, "secrets", "vault.enc"))
	if err != nil || bytes.Contains(vaultRaw, []byte(fixture.APIKey)) {
		t.Fatalf("secret vault leaked plaintext, err=%v", err)
	}
	assertTargetDBRow(t, filepath.Join(targetHome, WorkspaceDatabaseRelativePath), `SELECT COUNT(*) FROM compute_usage WHERE id = 'usage-1' AND state = 'running'`, 1)
	assertTargetDBRow(t, filepath.Join(targetHome, WorkspaceDatabaseRelativePath), `SELECT COUNT(*) FROM compute_pending_terminate WHERE sandbox_id = 'sandbox-1' AND job_id = 'job-1'`, 1)
	assertTargetDBRow(t, filepath.Join(targetHome, WorkspaceDatabaseRelativePath), `SELECT COUNT(*) FROM compute_managed_endpoints WHERE name = 'endpoint-1' AND owner_user_id = 'local' AND state = 'running'`, 1)
	assertTargetDBRow(t, filepath.Join(targetHome, WorkspaceDatabaseRelativePath), `SELECT COUNT(*) FROM verification_checks WHERE id = 'check-1' AND claim_id = 'claim-1'`, 1)
	assertTargetDBRow(t, filepath.Join(targetHome, WorkspaceDatabaseRelativePath), `SELECT COUNT(*) FROM mcp_oauth_status WHERE mcp_server_id = 'mcp-1' AND access_token_ref = 'legacy-encrypted:oauth-1'`, 1)
	assertTargetDBRow(t, filepath.Join(targetHome, WorkspaceDatabaseRelativePath), `SELECT COUNT(*) FROM memories
		WHERE id = 'memory-2' AND category_id = 'category-1'`, 1)
	for table, ownerColumn := range map[string]string{
		"projects":                  "user_id",
		"user_agents":               "user_id",
		"agent_custom_prompts":      "user_id",
		"notes":                     "user_id",
		"memories":                  "user_id",
		"memory_categories":         "user_id",
		"routine_schedules":         "owner_user_id",
		"use_intent_declarations":   "user_id",
		"contact_email_decisions":   "user_id",
		"directory_attachments":     "user_id",
		"custom_mcp_servers":        "user_id",
		"mcp_agent_assignments":     "user_id",
		"mcp_tool_grants":           "user_id",
		"mcp_oauth_status":          "user_id",
		"skill_preferences":         "user_id",
		"model_providers":           "user_id",
		"compute_workbench_jobs":    "owner_user_id",
		"compute_managed_endpoints": "owner_user_id",
	} {
		assertTargetDBRow(t, filepath.Join(targetHome, WorkspaceDatabaseRelativePath),
			`SELECT COUNT(*) FROM `+table+` WHERE `+ownerColumn+` <> 'local'`, 0)
	}
	for table := range map[string]bool{
		"artifact_version_dependencies": true, "content_snapshots": true, "execution_log": true,
		"artifact_version_execution_links": true, "frame_read_cursors": true, "frame_system_prompts": true,
		"compaction_archives":  true,
		"queued_user_messages": true, "session_seen_marks": true, "memory_categories": true,
		"use_intent_declarations": true,
		"contact_email_decisions": true,
		"directory_attachments":   true, "legacy_user_secrets": true,
		"session_claims": true, "verification_checks": true, "compute_pending_terminate": true,
		"compute_managed_endpoints": true,
	} {
		assertTargetDBRow(t, filepath.Join(targetHome, WorkspaceDatabaseRelativePath), `SELECT COUNT(*) FROM `+table, 1)
	}

	repeated, err := Migrate(ctx, Options{SourceDB: fixture.Database, SourceDataDir: sourceHome, TargetHome: targetHome, ConflictPolicy: ConflictAbort})
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Idempotent || repeated.MigrationID != report.MigrationID {
		t.Fatalf("idempotent report = %#v", repeated)
	}
	verified, err := Verify(ctx, targetHome)
	if err != nil || !verified.SourceIdentityRecorded || !verified.WorkspaceQuickCheckOK || !verified.ForeignKeysOK || verified.MigrationID != report.MigrationID {
		t.Fatalf("verification = %#v err=%v", verified, err)
	}
}

func TestMigrateReadsSourceTreeWithoutWritePermission(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("POSIX source permissions are not enforced on Windows")
	}
	ctx := context.Background()
	root := t.TempDir()
	sourceHome := filepath.Join(root, "v11-read-only")
	targetHome := filepath.Join(root, "go")
	fixture := writeV11Fixture(t, sourceHome, true)
	sourceBefore := hashTree(t, sourceHome)
	setTreeModes(t, sourceHome, 0o555, 0o444)
	defer setTreeModes(t, sourceHome, 0o700, 0o600)

	inspection, err := Inspect(ctx, fixture.Database)
	if err != nil {
		t.Fatalf("inspect read-only source: %v", err)
	}
	if !inspection.QuickCheckOK || inspection.TableCounts["frame_messages"] != 2 {
		t.Fatalf("read-only inspection = %#v", inspection)
	}
	report, err := Migrate(ctx, Options{
		SourceDB:       fixture.Database,
		SourceDataDir:  sourceHome,
		TargetHome:     targetHome,
		ConflictPolicy: ConflictAbort,
	})
	if err != nil {
		t.Fatalf("migrate read-only source: %v", err)
	}
	if report.Status != StatusCompleted || report.Imported["frame_messages"] != 2 {
		t.Fatalf("read-only migration report = %#v", report)
	}
	if _, err := Verify(ctx, targetHome); err != nil {
		t.Fatalf("verify read-only migration: %v", err)
	}
	if sourceAfter := hashTree(t, sourceHome); sourceAfter != sourceBefore {
		t.Fatalf("read-only source tree changed: before=%s after=%s", sourceBefore, sourceAfter)
	}
}

func TestMigrateIsAtomicAndRollbackRestoresReplacedTarget(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceHome := filepath.Join(root, "v11")
	fixture := writeV11Fixture(t, sourceHome, true)
	targetHome := filepath.Join(root, "go")
	if err := os.MkdirAll(targetHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetHome, "old-state"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, Options{SourceDB: fixture.Database, SourceDataDir: sourceHome, TargetHome: targetHome, ConflictPolicy: ConflictAbort}); err == nil {
		t.Fatal("non-empty target accepted by abort policy")
	}
	if raw, err := os.ReadFile(filepath.Join(targetHome, "old-state")); err != nil || string(raw) != "keep me" {
		t.Fatalf("target changed after aborted migration: %q err=%v", raw, err)
	}

	report, err := Migrate(ctx, Options{SourceDB: fixture.Database, SourceDataDir: sourceHome, TargetHome: targetHome, ConflictPolicy: ConflictReplace})
	if err != nil || report.BackupPath == "" {
		t.Fatalf("replace report=%#v err=%v", report, err)
	}
	if _, err := os.Stat(filepath.Join(targetHome, "old-state")); !os.IsNotExist(err) {
		t.Fatalf("old target still active: %v", err)
	}
	rollback, err := Rollback(ctx, targetHome, report.MigrationID)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Status != StatusRolledBack || rollback.PreservedMigratedPath == "" {
		t.Fatalf("rollback = %#v", rollback)
	}
	if raw, err := os.ReadFile(filepath.Join(targetHome, "old-state")); err != nil || string(raw) != "keep me" {
		t.Fatalf("restored target = %q err=%v", raw, err)
	}
	preservedVerification, err := Verify(ctx, rollback.PreservedMigratedPath)
	if err != nil {
		t.Fatalf("verify preserved migrated target: %v", err)
	}
	if preservedVerification.MigrationID != report.MigrationID || preservedVerification.TargetHome != rollback.PreservedMigratedPath {
		t.Fatalf("preserved verification = %#v", preservedVerification)
	}

	brokenHome := filepath.Join(root, "broken-v11")
	broken := writeV11Fixture(t, brokenHome, false)
	brokenTarget := filepath.Join(root, "broken-go")
	if _, err := Migrate(ctx, Options{SourceDB: broken.Database, SourceDataDir: brokenHome, TargetHome: brokenTarget, ConflictPolicy: ConflictAbort}); err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("missing artifact error = %v", err)
	}
	if _, err := os.Stat(brokenTarget); !os.IsNotExist(err) {
		t.Fatalf("failed migration left target: %v", err)
	}
}

type v11Fixture struct {
	Database      string
	APIKey        string
	LatestContent string
	LatestSHA256  string
	CloudSecret   string
}

func writeV11Fixture(t *testing.T, home string, writeArtifacts bool) v11Fixture {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(home, "operon-cli.db")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE __drizzle_migrations (id INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL, created_at NUMERIC)`,
		`CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT, description TEXT, context TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, user_id TEXT, uploads_frame_id TEXT, memory_enabled INTEGER)`,
		`CREATE TABLE frames (id TEXT PRIMARY KEY, parent_frame_id TEXT, root_frame_id TEXT, agent_name TEXT NOT NULL, status TEXT NOT NULL, input_data TEXT, output_data TEXT, context_data TEXT, model TEXT, effort TEXT, input_tokens INTEGER, output_tokens INTEGER, total_cost REAL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, completed_at INTEGER, project_id TEXT, name TEXT, conversation_type TEXT NOT NULL, artifact_id TEXT, task_summary TEXT, mentioned_artifact_ids TEXT, specialists_used TEXT, is_hidden INTEGER, status_description TEXT, compute_enabled TEXT, delegate_name TEXT, cache_read_tokens INTEGER, cache_write_tokens INTEGER, last_user_message_at INTEGER, last_extract_msg_idx INTEGER, root_seq INTEGER NOT NULL DEFAULT 0, aux_input_tokens INTEGER, aux_output_tokens INTEGER, aux_cache_read_tokens INTEGER, aux_cache_write_tokens INTEGER, aux_cost REAL, token_class_usage TEXT)`,
		`CREATE TABLE frame_messages (frame_id TEXT NOT NULL, idx INTEGER NOT NULL, msg_json TEXT NOT NULL, PRIMARY KEY(frame_id, idx))`,
		`CREATE TABLE events (id TEXT PRIMARY KEY, frame_id TEXT NOT NULL, event_type TEXT NOT NULL, payload TEXT, timestamp INTEGER NOT NULL)`,
		`CREATE TABLE user_agents (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL, display_name TEXT NOT NULL, description TEXT NOT NULL, system_prompt TEXT NOT NULL, icon_key TEXT NOT NULL, color_key TEXT NOT NULL, tags TEXT NOT NULL, skill_names TEXT NOT NULL, enabled INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, connector_tombstones TEXT NOT NULL, skill_tombstones TEXT NOT NULL, unrestricted INTEGER NOT NULL)`,
		`CREATE TABLE custom_agent_prompts (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, agent_name TEXT NOT NULL, prompt_text TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE artifact_folders (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT, name TEXT NOT NULL, sort_order INTEGER NOT NULL, root_frame_id TEXT, is_conversation_folder INTEGER NOT NULL, is_user_uploads_folder INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE artifacts (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, root_frame_id TEXT NOT NULL, frame_id TEXT, filename TEXT NOT NULL, created_at INTEGER NOT NULL, latest_version_id TEXT, is_user_upload INTEGER NOT NULL, is_ephemeral INTEGER NOT NULL, folder_id TEXT, sort_order INTEGER NOT NULL, priority TEXT NOT NULL, superseded_by_artifact_id TEXT, consumed_at INTEGER, is_branch_mint INTEGER NOT NULL)`,
		`CREATE TABLE artifact_versions (id TEXT PRIMARY KEY, artifact_id TEXT NOT NULL, version_number INTEGER NOT NULL, frame_id TEXT, content_type TEXT NOT NULL, size_bytes INTEGER NOT NULL, checksum TEXT NOT NULL, storage_path TEXT NOT NULL, created_at INTEGER NOT NULL, extracted_code TEXT, code_description TEXT, lineage_messages TEXT, agent_name TEXT, language TEXT, is_intermediate INTEGER NOT NULL, dependency_mappings TEXT, environment_snapshot TEXT, annotations TEXT, parent_version_id TEXT, lineage_snapshot_hash TEXT, env_snapshot_hash TEXT, producing_cell_id TEXT, cell_sources TEXT, is_checkpoint INTEGER NOT NULL)`,
		`CREATE TABLE artifact_dependencies (id TEXT PRIMARY KEY, artifact_version_id TEXT NOT NULL, depends_on_version_id TEXT NOT NULL, reference_name TEXT, created_at INTEGER NOT NULL)`,
		`CREATE TABLE content_snapshots (hash TEXT PRIMARY KEY, content TEXT NOT NULL, size_bytes INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE execution_log (id TEXT PRIMARY KEY, frame_id TEXT NOT NULL, cell_index INTEGER NOT NULL, kernel_id TEXT NOT NULL, conda_env TEXT NOT NULL, language TEXT NOT NULL, source TEXT NOT NULL, stdout TEXT, stderr TEXT, exit_status TEXT NOT NULL, created_at INTEGER NOT NULL, files_written TEXT, error_lineno INTEGER, kernel_kind TEXT, origin TEXT NOT NULL, detection TEXT, files_read TEXT)`,
		`CREATE TABLE session_claims (id TEXT PRIMARY KEY, root_frame_id TEXT NOT NULL, frame_id TEXT NOT NULL, step_id TEXT, claim_text TEXT NOT NULL, entities TEXT, source TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE verification_checks (id TEXT PRIMARY KEY, root_frame_id TEXT NOT NULL, artifact_version_id TEXT, claim_id TEXT, claim TEXT, verdict TEXT NOT NULL, severity TEXT, evidence TEXT, rebuttal TEXT, reviewer_idx INTEGER, reviewer_model TEXT, reviewer_frame_id TEXT, source_ref TEXT NOT NULL, status TEXT NOT NULL, reflag_count INTEGER, created_at INTEGER NOT NULL)`,
		`CREATE TABLE annotations (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, target_kind TEXT NOT NULL, target_key TEXT NOT NULL, label_idx INTEGER NOT NULL, content_checksum TEXT, body TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER)`,
		`CREATE TABLE notes (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, user_id TEXT NOT NULL, target_type TEXT NOT NULL, target_frame_id TEXT NOT NULL, target_message_index INTEGER, target_artifact_id TEXT, content TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE memories (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, body TEXT NOT NULL, subject_project_id TEXT, subject_artifact_id TEXT, subject_version_id TEXT, subject_frame_id TEXT, source_frame_id TEXT, origin TEXT NOT NULL, evidence TEXT NOT NULL, superseded_by TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, last_surfaced_at INTEGER, category_id TEXT)`,
		`CREATE TABLE routine_schedules (id TEXT PRIMARY KEY, root_frame_id TEXT NOT NULL, owner_user_id TEXT NOT NULL, label TEXT, on_tick TEXT NOT NULL, every_minutes INTEGER NOT NULL, enabled INTEGER NOT NULL, locked_at INTEGER, paused_reason TEXT, next_due INTEGER NOT NULL, tick_count INTEGER NOT NULL, missed_ticks INTEGER NOT NULL, last_fire_at INTEGER, last_ok_at INTEGER, idle_streak INTEGER NOT NULL, last_results TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE frame_read_cursors (root_frame_id TEXT PRIMARY KEY, message_uuid TEXT, message_index INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE transcript_annotations (id TEXT PRIMARY KEY, root_frame_id TEXT NOT NULL, message_uuid TEXT, message_index INTEGER NOT NULL, block_index INTEGER NOT NULL, source TEXT NOT NULL, tool_name TEXT, anchor_text TEXT NOT NULL, start_offset INTEGER, end_offset INTEGER, kind TEXT NOT NULL, origin TEXT NOT NULL, read_at INTEGER, note TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE frame_system_prompts (frame_id TEXT PRIMARY KEY, hash TEXT NOT NULL, updated_at INTEGER NOT NULL, payload TEXT NOT NULL)`,
		`CREATE TABLE frame_branch_archives (frame_id TEXT NOT NULL, branch_id TEXT NOT NULL, payload TEXT NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(frame_id, branch_id))`,
		`CREATE TABLE compaction_archives (id TEXT PRIMARY KEY, frame_id TEXT NOT NULL, compaction_index INTEGER NOT NULL, message_count INTEGER NOT NULL, token_count INTEGER, summary TEXT NOT NULL, messages TEXT NOT NULL, created_at INTEGER NOT NULL, UNIQUE(frame_id, compaction_index))`,
		`CREATE TABLE queued_user_messages (seq INTEGER PRIMARY KEY, frame_id TEXT NOT NULL, payload TEXT NOT NULL, intent_id TEXT NOT NULL, state TEXT NOT NULL, resolved_at INTEGER, created_at INTEGER NOT NULL)`,
		`CREATE TABLE session_seen_marks (root_frame_id TEXT PRIMARY KEY, seen_token TEXT NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE memory_categories (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL, name_lower TEXT NOT NULL, guidance TEXT NOT NULL, auto_recall INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE use_intent_declarations (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, org_id TEXT, intent TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE contact_email_decisions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, decision TEXT NOT NULL, email TEXT, notice_version TEXT NOT NULL, notice_text TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE capability_settings (user_id TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL, enabled INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(user_id, kind, key))`,
		`CREATE TABLE directory_attachments (server_uuid TEXT NOT NULL, agent_name TEXT NOT NULL, user_id TEXT NOT NULL, created_at INTEGER NOT NULL, excluded_tools TEXT NOT NULL, PRIMARY KEY(server_uuid, agent_name, user_id))`,
		`CREATE TABLE user_secrets (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL, provider TEXT NOT NULL, encrypted_value TEXT NOT NULL, credential_type TEXT, buckets TEXT, region TEXT, description TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE custom_mcp_servers (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, url TEXT NOT NULL, transport TEXT NOT NULL, oauth_server_url TEXT, client_id TEXT, scopes TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, source TEXT NOT NULL, headers_helper TEXT, resource_identifier TEXT)`,
		`CREATE TABLE mcp_agent_assignments (id TEXT PRIMARY KEY, mcp_server_id TEXT NOT NULL, agent_name TEXT NOT NULL, user_id TEXT NOT NULL, created_at INTEGER NOT NULL, excluded_tools TEXT NOT NULL)`,
		`CREATE TABLE mcp_tool_grants (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, server_id TEXT NOT NULL, tool_name TEXT NOT NULL, decision TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE oauth_tokens (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, mcp_server_id TEXT NOT NULL, encrypted_access_token TEXT NOT NULL, encrypted_refresh_token TEXT, token_type TEXT NOT NULL, expires_at INTEGER, scopes TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, client_id TEXT)`,
		`CREATE TABLE compute_providers (name TEXT PRIMARY KEY, family TEXT NOT NULL, memory_md TEXT NOT NULL, environments TEXT NOT NULL, memory_rev INTEGER NOT NULL, scratch_root TEXT, scheduler TEXT, probed_at INTEGER, data_roots TEXT NOT NULL, ssh_overrides TEXT, max_timeout_sec INTEGER, enabled INTEGER NOT NULL, scratch_root_source TEXT NOT NULL, home TEXT, scratch_root_revalidate_failed_at INTEGER, infer_config TEXT, app_name TEXT, prior_app_names TEXT, max_concurrent_jobs INTEGER, egress_policy TEXT, modal_environment TEXT)`,
		`CREATE TABLE compute_usage (id TEXT PRIMARY KEY, job_id TEXT NOT NULL, environment TEXT NOT NULL, tier_type TEXT NOT NULL, provider TEXT NOT NULL, frame_id TEXT, project_id TEXT, started_at INTEGER NOT NULL, ended_at INTEGER, expires_at INTEGER, client_uuid TEXT, remote_workdir TEXT, remote_handle TEXT, state TEXT NOT NULL, output_specs TEXT, submit_cell_id TEXT, intent TEXT, hardware_details TEXT, root_frame_id TEXT, result TEXT, origin_tool_use_id TEXT)`,
		`CREATE TABLE compute_pending_terminate (sandbox_id TEXT PRIMARY KEY, provider TEXT NOT NULL, enqueued_at INTEGER NOT NULL, attempts INTEGER NOT NULL)`,
		`CREATE TABLE managed_endpoints (name TEXT PRIMARY KEY, url TEXT NOT NULL, port INTEGER NOT NULL, credential_name TEXT, skill_name TEXT NOT NULL, start_script TEXT NOT NULL, stop_script TEXT NOT NULL, live_path TEXT NOT NULL, approved_script_hash TEXT NOT NULL, state TEXT NOT NULL, state_changed_at INTEGER, last_error TEXT, transcript TEXT, created_at INTEGER NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture schema %q: %v", statement, err)
		}
	}
	for index := 0; index < 94; index++ {
		if _, err := db.Exec(`INSERT INTO __drizzle_migrations (hash, created_at) VALUES (?, ?)`, "hash-"+time.Unix(int64(index), 0).UTC().Format("150405"), 1700000000000+index); err != nil {
			t.Fatal(err)
		}
	}
	firstContent := "first version\n"
	latestContent := "second version keeps lineage\n"
	firstSHA := sha256Hex([]byte(firstContent))
	latestSHA := sha256Hex([]byte(latestContent))
	legacyKey := sha256.Sum256([]byte("fixture-user-secret-encryption-key"))
	cloudSecret := "fixture-cloud-secret"
	legacyEncrypted := encryptLegacyV2(t, legacyKey[:], []byte(`{"access_key_id":"AKIAFIXTURE","secret_access_key":"`+cloudSecret+`"}`))
	data := []string{
		`INSERT INTO projects VALUES ('project-1','Migration fixture','desc','{}',1700000000000,1700000009000,'user-1',NULL,1)`,
		`INSERT INTO frames (id,parent_frame_id,root_frame_id,agent_name,status,input_data,output_data,context_data,model,effort,input_tokens,output_tokens,total_cost,created_at,updated_at,completed_at,project_id,name,conversation_type,artifact_id,task_summary,mentioned_artifact_ids,specialists_used,is_hidden,status_description,compute_enabled,delegate_name,cache_read_tokens,cache_write_tokens,last_user_message_at,last_extract_msg_idx,root_seq) VALUES ('frame-1',NULL,NULL,'OPERON','completed','{}','{}','{}','model-a','medium',10,5,0.1,1700000001000,1700000008000,1700000008000,'project-1','Fixture frame','agent',NULL,'done','[]','[]',0,'complete','local',NULL,1,2,1700000007000,1,7)`,
		`INSERT INTO frame_messages VALUES ('frame-1',0,'{"role":"user","content":[{"type":"text","text":"hello"}]}')`,
		`INSERT INTO frame_messages VALUES ('frame-1',1,'{"role":"assistant","content":[{"type":"text","text":"done"}]}')`,
		`INSERT INTO events VALUES ('event-1','frame-1','frame_update','{"status":"completed"}',1700000008000)`,
		`INSERT INTO user_agents VALUES ('agent-1','user-1','custom-agent','Custom Agent','desc','Preserve this prompt','lightning','accent-main','[]','["literature-review"]',1,1700000000000,1700000009000,'[]','[]',0)`,
		`INSERT INTO custom_agent_prompts VALUES ('prompt-1','user-1','custom-agent','Additional prompt',1700000000000,1700000009000)`,
		`INSERT INTO artifact_folders VALUES ('folder-1','project-1',NULL,'Reports',1,'frame-1',1,0,1700000000000,1700000009000)`,
		`INSERT INTO artifacts VALUES ('artifact-1','project-1','frame-1','frame-1','report.txt',1700000002000,'version-2',0,0,'folder-1',1,'high',NULL,NULL,0)`,
		`INSERT INTO artifact_versions VALUES ('version-1','artifact-1',1,'frame-1','text/plain',14,'` + firstSHA + `','project-1/artifact-1/v1-report.txt',1700000003000,NULL,NULL,NULL,'OPERON','text',0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,0)`,
		`INSERT INTO artifact_versions VALUES ('version-2','artifact-1',2,'frame-1','text/plain',29,'` + latestSHA + `','project-1/artifact-1/v2-report.txt',1700000004000,NULL,NULL,NULL,'OPERON','text',0,NULL,NULL,NULL,'version-1',NULL,NULL,'cell-1','[{"cell_index":1,"kind":"cell"}]',0)`,
		`INSERT INTO artifact_dependencies VALUES ('dependency-1','version-2','version-1','input',1700000004000)`,
		`INSERT INTO content_snapshots VALUES ('snapshot-1','snapshot body',13,1700000004000)`,
		`INSERT INTO execution_log VALUES ('cell-1','frame-1',1,'kernel-1','base','python','print("ok")','ok\n','', 'success',1700000004500,'["report.txt"]',NULL,'python','agent',NULL,'[]')`,
		`INSERT INTO session_claims VALUES ('claim-1','frame-1','frame-1','step-1','The result is reproducible','["result"]','agent',1700000004600)`,
		`INSERT INTO verification_checks VALUES ('check-1','frame-1','version-2','claim-1','The result is reproducible','pass','high','verified evidence',NULL,0,'reviewer-model','frame-1','{"kind":"fixture"}','open',0,1700000004700)`,
		`INSERT INTO annotations VALUES ('annotation-1','project-1','artifact-version','version-2',0,'` + latestSHA + `','{"text":"keep"}',1700000005000,NULL)`,
		`INSERT INTO notes VALUES ('note-1','project-1','user-1','frame','frame-1',1,'artifact-1','Keep this note',1700000005000,1700000006000)`,
		`INSERT INTO memories VALUES ('memory-1','user-1','old memory','project-1',NULL,NULL,NULL,NULL,'user','stated','memory-2',1700000000000,1700000005000,NULL,NULL)`,
		`INSERT INTO memories VALUES ('memory-2','user-1','active memory','project-1',NULL,NULL,NULL,NULL,'user','verified',NULL,1700000001000,1700000006000,NULL,'category-1')`,
		`INSERT INTO routine_schedules VALUES ('routine-1','frame-1','user-1','Daily','continue',60,1,NULL,NULL,1700003600000,3,0,1700000000000,1700000000000,0,'ok',1700000000000,1700000009000)`,
		`INSERT INTO frame_read_cursors VALUES ('frame-1','message-2',2,1700000009000)`,
		`INSERT INTO transcript_annotations VALUES ('transcript-1','frame-1','message-2',1,0,'assistant',NULL,'done',0,4,'bookmark','agent',1700000008500,'Imported bookmark',1700000008000,1700000009000)`,
		`INSERT INTO frame_system_prompts VALUES ('frame-1','prompt-hash',1700000009000,'System prompt')`,
		`INSERT INTO frame_branch_archives VALUES ('frame-1','branch-1','{"messages":[]}',1700000009000)`,
		`INSERT INTO compaction_archives VALUES ('compact-1','frame-1',0,2,77,'Imported compact summary','[{"role":"user","text":"hello"},{"role":"assistant","text":"done"}]',1700000008500)`,
		`INSERT INTO queued_user_messages VALUES (1,'frame-1','{"text":"queued"}','intent-queued','queued',NULL,1700000009000)`,
		`INSERT INTO session_seen_marks VALUES ('frame-1','seen-1',1700000009000)`,
		`INSERT INTO memory_categories VALUES ('category-1','user-1','Project facts','project facts','Recall project facts',1,1700000000000,1700000009000)`,
		`INSERT INTO use_intent_declarations VALUES ('intent-1','user-1',NULL,'drug-discovery',1700000000000)`,
		`INSERT INTO contact_email_decisions VALUES ('contact-1','user-1','allowed','migration@example.test','99f2e8cddaf43594d50084dc3f990846f39c0e314d766e474e72df4d7e7fd89a','notice',1700000000500)`,
		`INSERT INTO capability_settings VALUES ('user-1','skill','literature-review',1,1700000009000)`,
		`INSERT INTO directory_attachments VALUES ('bundled:pubmed','custom-agent','user-1',1700000000000,'[]')`,
		`INSERT INTO user_secrets VALUES ('user-secret-1','user-1','cloud','aws','` + legacyEncrypted + `','access_key','["bucket"]','us-east-1','fixture',1700000000000,1700000009000)`,
		`INSERT INTO custom_mcp_servers VALUES ('mcp-1','user-1','fixture-mcp','desc','https://mcp.example.test','streamable-http',NULL,NULL,'scope-a',1700000000000,1700000009000,'custom',NULL,'fixture')`,
		`INSERT INTO mcp_agent_assignments VALUES ('assignment-1','mcp-1','custom-agent','user-1',1700000000000,'[]')`,
		`INSERT INTO mcp_tool_grants VALUES ('grant-1','user-1','mcp-1','search','allow',1700000000000)`,
		`INSERT INTO oauth_tokens VALUES ('oauth-1','user-1','mcp-1','ciphertext','refresh-ciphertext','Bearer',1700003600000,'scope-a',1700000000000,1700000009000,'client-1')`,
		`INSERT INTO compute_providers (name,family,memory_md,environments,memory_rev,scratch_root,scheduler,probed_at,data_roots,ssh_overrides,max_timeout_sec,enabled,scratch_root_source) VALUES ('local-cpu','local','memory','["base"]',1,'/tmp','local',1700000000000,'[]',NULL,600,1,'probe')`,
		`INSERT INTO compute_usage (id,job_id,environment,tier_type,provider,frame_id,project_id,started_at,remote_handle,state,root_frame_id) VALUES ('usage-1','job-1','base','cpu','local-cpu','frame-1','project-1',1700000000000,'{"sandboxId":"sandbox-1"}','running','frame-1')`,
		`INSERT INTO compute_pending_terminate VALUES ('sandbox-1','local-cpu',1700000008000,2)`,
		`INSERT INTO managed_endpoints VALUES ('endpoint-1','http://127.0.0.1:9000',9000,NULL,'fixture-skill','start.sh','stop.sh','/tmp/endpoint','0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef','running',1700000007000,NULL,'started',1700000000000)`,
	}
	for _, statement := range data {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture data %q: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if writeArtifacts {
		writeFile(t, filepath.Join(home, "artifacts", "project-1", "artifact-1", "v1-report.txt"), []byte(firstContent))
		writeFile(t, filepath.Join(home, "artifacts", "project-1", "artifact-1", "v2-report.txt"), []byte(latestContent))
	}
	apiKey := "fixture-api-key-must-be-encrypted"
	profiles := map[string]any{
		"version": 1, "activeProfileId": "profile-1",
		"profiles": []map[string]any{
			{
				"id": "profile-1", "name": "Fixture model", "provider": "openai-compatible",
				"baseUrl": "https://models.example.test/v1", "model": "model-a", "apiKey": apiKey,
				"createdAt": "2023-11-14T22:13:20Z", "updatedAt": "2023-11-14T22:13:29Z",
			},
			{
				"id": "profile-2", "name": "Fixture model", "provider": "openai-compatible",
				"baseUrl": "https://models.example.test/v1", "model": "model-b", "apiKey": "",
				"createdAt": "2023-11-14T22:13:20Z", "updatedAt": "2023-11-14T22:13:30Z",
			},
		},
	}
	rawProfiles, err := json.Marshal(profiles)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "llm-providers.json"), rawProfiles)
	writeFile(t, filepath.Join(home, "preferences.json"), []byte(`{"memoryEnabled":true,"userAllowedDomains":["example.test"],"allowlistOnboardingSeen":true,"firstRunOnboardingComplete":true}`))
	currentKey := sha256.Sum256([]byte("fixture-rotated-user-secret-key"))
	writeFile(t, filepath.Join(home, "auth", "encryption.key"), []byte("USER_SECRET_ENCRYPTION_KEY="+base64.StdEncoding.EncodeToString(currentKey[:])+"\n"))
	writeFile(t, filepath.Join(home, "auth", ".key-backups", "encryption.key.replaced"), []byte("USER_SECRET_ENCRYPTION_KEY="+base64.StdEncoding.EncodeToString(legacyKey[:])+"\n"))
	return v11Fixture{Database: database, APIKey: apiKey, LatestContent: latestContent, LatestSHA256: latestSHA, CloudSecret: cloudSecret}
}

func containsModelSecretRef(providers []workspace.ModelProvider, secretRef string) bool {
	for _, provider := range providers {
		if provider.SecretRef == secretRef {
			return true
		}
	}
	return false
}

func containsProject(projects []workspace.Project, id, name string) bool {
	for _, project := range projects {
		if project.ID == id && project.Name == name {
			return true
		}
	}
	return false
}

func encryptLegacyV2(t *testing.T, sourceKey, plaintext []byte) string {
	t.Helper()
	derived := testHKDFSHA256(sourceKey, []byte("operon:aes-256-gcm:userSecret"))
	block, err := aes.NewCipher(derived)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := []byte("fixture-iv12")
	sealed := gcm.Seal(nil, nonce, plaintext, []byte("v2:userSecret"))
	return "v2:" + base64.StdEncoding.EncodeToString(append(append([]byte{}, nonce...), sealed...))
}

func testHKDFSHA256(inputKey, info []byte) []byte {
	extract := hmac.New(sha256.New, make([]byte, sha256.Size))
	_, _ = extract.Write(inputKey)
	pseudorandomKey := extract.Sum(nil)
	expand := hmac.New(sha256.New, pseudorandomKey)
	_, _ = expand.Write(info)
	_, _ = expand.Write([]byte{1})
	return expand.Sum(nil)
}

func writeFile(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(raw)
}

func hashTree(t *testing.T, root string) string {
	t.Helper()
	digest := sha256.New()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(digest, filepath.ToSlash(relative))
		_, _ = digest.Write([]byte{0})
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func setTreeModes(t *testing.T, root string, directoryMode, fileMode os.FileMode) {
	t.Helper()
	directories := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		return os.Chmod(path, fileMode)
	}); err != nil {
		t.Fatal(err)
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := os.Chmod(directories[index], directoryMode); err != nil {
			t.Fatal(err)
		}
	}
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func assertTargetDBRow(t *testing.T, path, query string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
		t.Fatalf("query %q = %d, want %d, err=%v", query, got, want, err)
	}
}
