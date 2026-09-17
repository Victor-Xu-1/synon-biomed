package v11

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestPlanTableDispositionsFailsClosedForUnknownNonEmptyTable(t *testing.T) {
	_, err := planTableDispositions(map[string]int64{"projects": 1, "future_durable_state": 2})
	if err == nil || !strings.Contains(err.Error(), "future_durable_state") {
		t.Fatalf("plan error = %v", err)
	}

	planned, err := planTableDispositions(map[string]int64{"future_empty_state": 0})
	if err != nil || len(planned) != 0 {
		t.Fatalf("empty unknown table plan = %#v, err=%v", planned, err)
	}
}

func TestImportWorkspaceLosslesslyArchivesKnownDurableTable(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "target.sqlite")

	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`CREATE TABLE host_call_log (id TEXT PRIMARY KEY, payload BLOB, attempt INTEGER);
		INSERT INTO host_call_log VALUES ('call-1', X'00FF41', 7)`); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := importWorkspace(context.Background(), sourcePath, targetPath, map[string]int64{"host_call_log": 1}); err != nil {
		t.Fatal(err)
	}
	target, err := sql.Open("sqlite", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var id string
	var payload []byte
	var attempt int
	if err := target.QueryRow(`SELECT id, payload, attempt FROM legacy_v11_host_call_log`).Scan(&id, &payload, &attempt); err != nil {
		t.Fatal(err)
	}
	if id != "call-1" || string(payload) != string([]byte{0x00, 0xff, 0x41}) || attempt != 7 {
		t.Fatalf("archived row = id=%q payload=%x attempt=%d", id, payload, attempt)
	}
}

func TestFinalV11SchemaTablesHaveExplicitDispositions(t *testing.T) {
	for _, table := range []string{
		"__drizzle_migrations", "agent_skill_assignments", "agents", "annotations", "artifact_dependencies",
		"artifact_folders", "artifact_versions", "artifacts", "bundled_agent_settings", "capability_settings",
		"cloud_credentials", "compaction_archives", "compute_pending_terminate", "compute_providers", "compute_usage",
		"contact_email_decisions", "content_snapshots", "custom_agent_prompts", "custom_mcp_servers", "custom_skills",
		"directory_attachments", "events", "execution_log", "frame_backfill_poison", "frame_branch_archives",
		"frame_messages", "frame_read_cursors", "frame_system_prompts", "frames", "host_call_log", "host_grants",
		"managed_endpoints", "marketplace_sources", "mcp_agent_assignments", "mcp_tool_grants", "memories",
		"memory_categories", "notes", "notifications", "oauth_tokens", "poller_lease", "projects",
		"queued_user_messages", "routine_schedules", "safety_feedback", "session_claims", "session_concurrency",
		"session_seen_marks", "skill_license_assents", "synon_llm_api_keys", "transcript_annotations",
		"use_intent_declarations", "user_agents", "user_secrets", "verification_checks",
	} {
		if _, ok := v11TableDispositions[table]; !ok {
			t.Errorf("missing disposition for %s", table)
		}
	}
	contact := v11TableDispositions["contact_email_decisions"]
	if contact.action != DispositionImport || contact.target != "contact_email_decisions" {
		t.Fatalf("contact email disposition = %#v", contact)
	}
}
