package workspace

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRescuedSchemaMigrationIdentitiesAreFrozen(t *testing.T) {
	want := map[int]struct{ name, checksum string }{
		4:  {"claude-science-memory-schema", "2d1ace770a6548ff28271b67cae4e35f6e524e8123964b53a6da3ee7069f470e"},
		5:  {"claude-science-memory-lookup-indexes", "4aac1673c6e12bcfa78c42f1fbe5e8ce1a4543c8ee4a8be9b438b74e0460ed93"},
		6:  {"claude-science-frame-memory-lifecycle", "57410c35a28b9cf4d79b198aa96ab453f15525c40689c8ee31ea9555acec504f"},
		7:  {"claude-science-direct-memory-category", "8fd94cc952cf75ca341608927a67db4328acf908d9b61d80157bbe974e8e9460"},
		8:  {"claude-science-canonical-frame-status", "a3650d14752d87cb06aa4161e4e916f994839280690bbca3437c42595474ef24"},
		9:  {"claude-science-artifact-message-ownership", "c7c528c51fe5061070bd307e8d352926d834aca99a025d5d0437025390e70899"},
		10: {"claude-science-durable-artifact-lineage-jobs", "5104dc4494c39d19335f9812259b788d8b3aaf1270ac95aecd4644ce21e74017"},
		11: {"freeze-exhausted-artifact-lineage-mappings", "1872f27ec48190641d9eeaf913c9f18ab48a104f5b87059512d5b3df53f265bc"},
		12: {"complete-exhausted-artifact-lineage-jobs", "2f3c13860f1923aeec65439dd9ed874166c1c5b504dcb5462e7524d2b4e02198"},
		13: {"canonical-frame-compaction-count", "29b7028c7eaed64639cae0b038602f137713b643512e94f292f42552b6b1cd8c"},
		14: {"canonical-frame-execution-claims", "e54bdb339c3c1e6d3ffa17045807ddf031d100673cefddec62bed30025d681d9"},
		15: {"canonical-frame-event-revisions", "a8fe58670d1df52e0e6d57708db7c79db3d7d9266c7e3f3f0bc203c1bd45aadc"},
		16: {"canonical-task-intent-frame-lifecycle", "aba863ca536ab0e61d620b02777cdc50289a21fb55c8a6d1f4df254c36040d12"},
		17: {"canonical-frame-event-delete-lifecycle", "71c8b104f09f72a62c9711d8620a6d080b2f111f781719bc860c5c2a19e4027c"},
		18: {"claude-science-canonical-artifact-priority", "07f85cca7fc22769c5a939c49117701008799a836f487b43f6762ccf2e30ae5e"},
		19: {"claude-science-nullable-project-description", "af2548ec8ec80ba6a8b795e4e01b255aa5f11b94a97aebf5d5eb8e5ef1a88c16"},
		20: {"claude-science-canonical-approval-policy", "98108d9a47b26f6d38180fdf0bfa6ba04a825e00559fa065a95d5120cd2be99e"},
	}
	if len(workspaceSchemaMigrations) < 20 {
		t.Fatalf("migration manifest has %d entries", len(workspaceSchemaMigrations))
	}
	seenNames := map[string]bool{}
	for index, migration := range workspaceSchemaMigrations[:20] {
		if migration.version != index+1 || seenNames[migration.name] {
			t.Fatalf("migration %d is not a unique contiguous identity: %#v", index+1, migration)
		}
		seenNames[migration.name] = true
		if expected, ok := want[migration.version]; ok {
			if migration.name != expected.name || migration.checksum() != expected.checksum {
				t.Errorf("migration %d identity name=%q checksum=%s", migration.version, migration.name, migration.checksum())
			}
		}
	}
}

func TestRescuedAuthorityMigrationsUpgradeAndResumeSyntheticTargetThree(t *testing.T) {
	for _, stopAfter := range []int{7, 8, 10, 12, 14, 15} {
		t.Run(fmt.Sprintf("resume_from_%d", stopAfter), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workspace.sqlite")
			seedTargetThreeAuthorityFixture(t, path)
			db, err := sql.Open(sqliteDriver, path)
			if err != nil {
				t.Fatal(err)
			}
			now := func() time.Time { return time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC) }
			if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, stopAfter); err != nil {
				t.Fatalf("apply through %d: %v", stopAfter, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db, err = sql.Open(sqliteDriver, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 15); err != nil {
				t.Fatalf("resume through 15: %v", err)
			}
			if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 15); err != nil {
				t.Fatalf("idempotent reapply: %v", err)
			}
			assertTargetFifteenAuthorityFixture(t, db)
		})
	}
}

func TestRescuedSchemaMigrationsUpgradeAndResumeSyntheticTargetFifteen(t *testing.T) {
	for _, stopAfter := range []int{15, 16, 18, 19, 20} {
		t.Run(fmt.Sprintf("resume_from_%d", stopAfter), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workspace.sqlite")
			seedTargetThreeAuthorityFixture(t, path)
			db, err := sql.Open(sqliteDriver, path)
			if err != nil {
				t.Fatal(err)
			}
			now := func() time.Time { return time.Date(2026, 7, 22, 4, 0, 0, 0, time.UTC) }
			if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 15); err != nil {
				t.Fatalf("prepare target 15: %v", err)
			}
			seedTargetFifteenAuthorityFixture(t, db)
			if stopAfter > 15 {
				if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, stopAfter); err != nil {
					t.Fatalf("apply through %d: %v", stopAfter, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db, err = sql.Open(sqliteDriver, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 20); err != nil {
				t.Fatalf("resume through 20: %v", err)
			}
			if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 20); err != nil {
				t.Fatalf("idempotent reapply: %v", err)
			}
			assertTargetTwentyAuthorityFixture(t, db)
		})
	}
}

func TestRescuedMemoryMigrationsUpgradeAndResumeSyntheticTargetThree(t *testing.T) {
	for _, stopAfter := range []int{3, 4, 5, 6, 7} {
		t.Run(fmt.Sprintf("resume_from_%d", stopAfter), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workspace.sqlite")
			seedTargetThreeMemoryFixture(t, path)
			db, err := sql.Open(sqliteDriver, path)
			if err != nil {
				t.Fatal(err)
			}
			now := func() time.Time { return time.Date(2026, 7, 22, 2, 0, 0, 0, time.UTC) }
			if stopAfter > 3 {
				if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, stopAfter); err != nil {
					t.Fatalf("apply through %d: %v", stopAfter, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db, err = sql.Open(sqliteDriver, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 7); err != nil {
				t.Fatalf("resume through 7: %v", err)
			}
			if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 7); err != nil {
				t.Fatalf("idempotent reapply: %v", err)
			}
			assertTargetSevenMemoryFixture(t, db)
		})
	}
}

func TestRescuedMemoryMigrationsRejectJournalGapBeforeSchemaWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	seedTargetThreeMemoryFixture(t, path)
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migration5 := workspaceSchemaMigrations[4]
	if _, err := db.Exec(`INSERT INTO workspace_schema_migrations(version,name,checksum,applied_at) VALUES(5,?,?,CURRENT_TIMESTAMP)`,
		migration5.name, migration5.checksum()); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 7); err == nil || !strings.Contains(err.Error(), "contiguous") {
		t.Fatalf("gap error = %v", err)
	}
	var accessColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name IN ('access_count','importance')`).Scan(&accessColumns); err != nil {
		t.Fatal(err)
	}
	if accessColumns != 2 {
		t.Fatalf("schema changed before gap rejection: access columns=%d", accessColumns)
	}
}

func TestRescuedMemoryMigrationsRejectUnsupportedTargetBeforeSchemaWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	seedTargetThreeMemoryFixture(t, path)
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, len(workspaceSchemaMigrations)+1); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported target error = %v", err)
	}
	var accessColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name IN ('access_count','importance')`).Scan(&accessColumns); err != nil {
		t.Fatal(err)
	}
	if accessColumns != 2 {
		t.Fatalf("schema changed before unsupported target rejection: access columns=%d", accessColumns)
	}
}

func TestRescuedCanonicalStatusMigrationRollsBackUnknownStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	seedTargetThreeMemoryFixture(t, path)
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := func() time.Time { return time.Date(2026, 7, 22, 3, 30, 0, 0, time.UTC) }
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE frames SET status='unresolved_external_status' WHERE id='frame'`); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, now, 8); err == nil {
		t.Fatal("unknown frame status migration unexpectedly succeeded")
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("journal version=%d err=%v", version, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM frames WHERE id='frame'`).Scan(&status); err != nil || status != "unresolved_external_status" {
		t.Fatalf("frame status=%q err=%v", status, err)
	}
	var guardObjects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE name IN ('frame_status_migration_guard','frames_status_insert_guard','frames_status_update_guard')`).Scan(&guardObjects); err != nil || guardObjects != 0 {
		t.Fatalf("rolled-back guard objects=%d err=%v", guardObjects, err)
	}
}

func seedTargetThreeMemoryFixture(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	blobRoot := path + ".blobs"
	if err := ensurePrivateDirectory(blobRoot); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	store := &Store{db: db, now: time.Now, blobRoot: blobRoot}
	ctx := context.Background()
	if err := prepareSchemaJournal(ctx, db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := store.prepareLegacyWorkspaceSchema(ctx, db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, store.now, 3); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC)
	project, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: project.ID, AgentName: "agent", Status: "running", ConversationType: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO memory_categories(id,user_id,name,name_lower,guidance,auto_recall,created_at,updated_at)
		VALUES('category','owner','Category','category','Guidance',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO memories(id,user_id,body,subject_project_id,subject_frame_id,origin,evidence,
		access_count,importance,created_at,updated_at) VALUES('memory','owner','Body',?,?,'user','verified',3,8,?,?)`,
		project.ID, frame.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO memory_category_assignments(memory_id,category_id) VALUES('memory','category')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func seedTargetThreeAuthorityFixture(t *testing.T, path string) {
	t.Helper()
	seedTargetThreeMemoryFixture(t, path)
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 22, 1, 30, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO artifacts(id,project_id,name,kind,current_version_number,created_at,updated_at)
			VALUES('artifact','project','Report','text',1,?,?)`, []any{now, now}},
		{`INSERT INTO artifact_versions(id,artifact_id,version_number,content,content_sha256,created_at)
			VALUES('version','artifact',1,'report','sha256',?)`, []any{now}},
		{`INSERT INTO artifact_version_provenance(version_id,frame_id,content_type,language,dependency_mappings)
			VALUES('version','frame','text/markdown','text','{"mapping_status":"pending","mapping_attempts":3}')`, nil},
		{`INSERT INTO compaction_archives(id,frame_id,compaction_index,message_count,summary,messages,created_at)
			VALUES('archive','frame',0,2,'summary','[]',?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed authority fixture: %v", err)
		}
	}
}

func seedTargetFifteenAuthorityFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range legacyTaskIntentDDL {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare target-15 task intent fixture: %v", err)
		}
	}
	now := time.Date(2026, 7, 22, 3, 30, 0, 0, time.UTC)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
			VALUES('event-task','frame',1,'user_message','{}',?)`, []any{now}},
		{`INSERT INTO frame_task_intents(id,frame_id,revision,source_event_id,source_message_id,origin,language,text,created_at)
			VALUES('intent','frame',1,'event-task','message','user_task','en','task',?)`, []any{now}},
		{`INSERT INTO frame_active_task_intents(frame_id,task_intent_id,updated_at)
			VALUES('frame','intent',?)`, []any{now}},
		{`INSERT INTO artifact_runtime_metadata(artifact_id,priority_label)
			VALUES('artifact','user_starred')`, nil},
		{`INSERT OR REPLACE INTO project_runtime_metadata(project_id,description,context_data)
			VALUES('project','','null')`, nil},
		{`INSERT INTO mcp_connector_tool_policies(user_id,connector_id,tool_name,enabled,updated_at)
			VALUES('owner','connector','read_tool',1,?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed target-15 authority fixture: %v", err)
		}
	}
}

func assertTargetSevenMemoryFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("journal version=%d err=%v", version, err)
	}
	var category string
	if err := db.QueryRow(`SELECT category_id FROM memories WHERE id='memory'`).Scan(&category); err != nil || category != "category" {
		t.Fatalf("category=%q err=%v", category, err)
	}
	var legacyTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='memory_category_assignments'`).Scan(&legacyTable); err != nil || legacyTable != 0 {
		t.Fatalf("legacy assignment table=%d err=%v", legacyTable, err)
	}
	var removedColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name IN ('access_count','importance')`).Scan(&removedColumns); err != nil || removedColumns != 0 {
		t.Fatalf("removed columns=%d err=%v", removedColumns, err)
	}
	var quick string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick_check=%q err=%v", quick, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after target-7 migration")
	}
	if _, err := db.Exec(`DELETE FROM frames WHERE id='frame'`); err != nil {
		t.Fatal(err)
	}
	var memories int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id='memory'`).Scan(&memories); err != nil || memories != 0 {
		t.Fatalf("frame-scoped memory count=%d err=%v", memories, err)
	}
}

func assertTargetFifteenAuthorityFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil || version != 15 {
		t.Fatalf("journal version=%d err=%v", version, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM frames WHERE id='frame'`).Scan(&status); err != nil || status != "processing" {
		t.Fatalf("frame status=%q err=%v", status, err)
	}
	var sourceColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('artifact_version_provenance')
		WHERE name IN ('source_tool_call_id','source_message_event_id')`).Scan(&sourceColumns); err != nil || sourceColumns != 2 {
		t.Fatalf("artifact provenance source columns=%d err=%v", sourceColumns, err)
	}
	var mappingStatus, terminalSweep string
	if err := db.QueryRow(`SELECT json_extract(dependency_mappings,'$.mapping_status'),
		json_extract(dependency_mappings,'$.terminal_sweep') FROM artifact_version_provenance WHERE version_id='version'`).Scan(&mappingStatus, &terminalSweep); err != nil || mappingStatus != "complete" || terminalSweep != "frozen" {
		t.Fatalf("mapping status=%q sweep=%q err=%v", mappingStatus, terminalSweep, err)
	}
	var jobStatus string
	if err := db.QueryRow(`SELECT status FROM artifact_lineage_jobs WHERE version_id='version' AND phase='dependency_mapping'`).Scan(&jobStatus); err != nil || jobStatus != "completed" {
		t.Fatalf("lineage job status=%q err=%v", jobStatus, err)
	}
	var compactionCount int
	if err := db.QueryRow(`SELECT json_extract(context_data,'$._compaction_count') FROM frame_runtime_metadata WHERE frame_id='frame'`).Scan(&compactionCount); err != nil || compactionCount != 1 {
		t.Fatalf("compaction count=%d err=%v", compactionCount, err)
	}
	for _, table := range []string{"frame_execution_claims", "workspace_legacy_imports"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
	for _, trigger := range []string{"frames_status_insert_guard", "frames_status_update_guard", "frame_events_trace_update", "frame_events_trace_delete"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil || count != 1 {
			t.Fatalf("trigger %s count=%d err=%v", trigger, count, err)
		}
	}
	var quick string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick_check=%q err=%v", quick, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after target-15 migration")
	}
}

func assertTargetTwentyAuthorityFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil || version != 20 {
		t.Fatalf("journal version=%d err=%v", version, err)
	}
	var sourceEventID, activeIntentID string
	if err := db.QueryRow(`SELECT source_event_id FROM frame_task_intents WHERE id='intent'`).Scan(&sourceEventID); err != nil || sourceEventID != "event-task" {
		t.Fatalf("task intent source event=%q err=%v", sourceEventID, err)
	}
	if err := db.QueryRow(`SELECT task_intent_id FROM frame_active_task_intents WHERE frame_id='frame'`).Scan(&activeIntentID); err != nil || activeIntentID != "intent" {
		t.Fatalf("active task intent=%q err=%v", activeIntentID, err)
	}
	var priority string
	if err := db.QueryRow(`SELECT priority FROM artifacts WHERE id='artifact'`).Scan(&priority); err != nil || priority != "user_starred" {
		t.Fatalf("artifact priority=%q err=%v", priority, err)
	}
	var legacyPriorityColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('artifact_runtime_metadata') WHERE name='priority_label'`).Scan(&legacyPriorityColumns); err != nil || legacyPriorityColumns != 0 {
		t.Fatalf("legacy artifact priority columns=%d err=%v", legacyPriorityColumns, err)
	}
	var description sql.NullString
	if err := db.QueryRow(`SELECT description FROM project_runtime_metadata WHERE project_id='project'`).Scan(&description); err != nil || description.Valid {
		t.Fatalf("project description=%#v err=%v", description, err)
	}
	var tier, grantKeyHex string
	if err := db.QueryRow(`SELECT tier, hex(grant_key) FROM approval_policy_grants
		WHERE owner_user_id='owner' AND kind='mcp_tool'`).Scan(&tier, &grantKeyHex); err != nil || tier != "allow" || grantKeyHex != "636F6E6E6563746F7200726561645F746F6F6C" {
		t.Fatalf("approval tier=%q key=%q err=%v", tier, grantKeyHex, err)
	}
	var quick string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&quick); err != nil || quick != "ok" {
		t.Fatalf("quick_check=%q err=%v", quick, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after target-20 migration")
	}
}
