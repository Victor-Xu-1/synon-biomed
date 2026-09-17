package workspaceimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"

	_ "modernc.org/sqlite"
)

func TestInspectCurrentWorkspaceIsDeterministicAndPathRedacted(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, filepath.FromSlash(WorkspaceDatabaseRelative))
	if err := os.MkdirAll(filepath.Dir(database), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "owner-a", Name: "Research", Path: root}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(database, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(database), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(root, 0o700)
		_ = os.Chmod(filepath.Dir(database), 0o700)
		_ = os.Chmod(database, 0o600)
	})
	before := snapshotTree(t, root)

	first, err := Inspect(context.Background(), Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Inspect(context.Background(), Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanSHA256 == "" || first.PlanSHA256 != second.PlanSHA256 {
		t.Fatalf("plan digests first=%q second=%q", first.PlanSHA256, second.PlanSHA256)
	}
	if after := snapshotTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("inspection changed source tree\nbefore=%#v\nafter=%#v", before, after)
	}
	if len(first.Sources) != 1 {
		t.Fatalf("sources=%#v", first.Sources)
	}
	source := first.Sources[0]
	expectedSchema, err := workspace.ExpectedSchemaStatus()
	if err != nil {
		t.Fatal(err)
	}
	if source.Kind != SourceKindCurrent || source.SchemaVersion != expectedSchema.TargetVersion || source.SchemaTarget != expectedSchema.TargetVersion || source.SchemaJournalHash == "" {
		t.Fatalf("source=%#v", source)
	}
	if source.DatabasePath != "" || source.DataRoot != "" {
		t.Fatalf("paths leaked in default plan: %#v", source)
	}
	if source.EntityCounts["projects"] != 1 || source.DistinctOwners != 1 {
		t.Fatalf("owner/count projection=%#v", source)
	}
	assertIssueCodes(t, first.Issues, "target_capacity_not_measured", "target_collision_analysis_pending")

	withPaths, err := Inspect(context.Background(), Options{SourcePath: root, IncludePaths: true})
	if err != nil {
		t.Fatal(err)
	}
	if withPaths.PlanSHA256 != first.PlanSHA256 || withPaths.Sources[0].DatabasePath != database || withPaths.Sources[0].DataRoot != root {
		t.Fatalf("path projection=%#v", withPaths)
	}
}

func TestInspectRejectsSchemaIdentityDrift(t *testing.T) {
	database := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workspace_schema_migrations SET checksum='tampered' WHERE version=31`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := Inspect(context.Background(), Options{SourcePath: database})
	if err != nil {
		t.Fatal(err)
	}
	assertIssueCodes(t, plan.Issues,
		"source_schema_identity_mismatch",
		"target_capacity_not_measured",
		"target_collision_analysis_pending",
	)
}

func TestInspectRejectsAnySQLiteSidecarWithoutOpening(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(strings.TrimPrefix(suffix, "-"), func(t *testing.T) {
			database := filepath.Join(t.TempDir(), "workspace.sqlite")
			store, err := workspace.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(database+suffix, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotTree(t, filepath.Dir(database))
			plan, err := Inspect(context.Background(), Options{SourcePath: database})
			if err != nil {
				t.Fatal(err)
			}
			assertIssueCodes(t, plan.Issues,
				"source_database_has_uncommitted_sidecar",
				"target_capacity_not_measured",
				"target_collision_analysis_pending",
			)
			if after := snapshotTree(t, filepath.Dir(database)); !reflect.DeepEqual(before, after) {
				t.Fatalf("sidecar inspection changed tree before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestInspectBindsExternalContentsAndQuarantinesLegacyAuthority(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, filepath.FromSlash(WorkspaceDatabaseRelative))
	if err := os.MkdirAll(filepath.Dir(database), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(root, "artifacts", "result.bin")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("aaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Inspect(context.Background(), Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sources[0].ExternalFileCount != 1 || first.Sources[0].ExternalManifest == "" {
		t.Fatalf("external manifest=%#v", first.Sources[0])
	}
	if err := os.WriteFile(artifactPath, []byte("bbbb"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Inspect(context.Background(), Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sources[0].ExternalBytes != second.Sources[0].ExternalBytes || first.Sources[0].ExternalManifest == second.Sources[0].ExternalManifest || first.PlanSHA256 == second.PlanSHA256 {
		t.Fatalf("content change not bound first=%#v second=%#v", first.Sources[0], second.Sources[0])
	}
	legacy := filepath.Join(root, "sessions", "index.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := Inspect(context.Background(), Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(third.Issues, "source_external_authority_requires_conversion") {
		t.Fatalf("legacy authority was not quarantined: %#v", third.Issues)
	}
}

func TestInspectBindsTargetContentsWithoutMutatingThem(t *testing.T) {
	sourceRoot := t.TempDir()
	database := filepath.Join(sourceRoot, filepath.FromSlash(WorkspaceDatabaseRelative))
	if err := os.MkdirAll(filepath.Dir(database), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()
	targetFile := filepath.Join(targetRoot, "existing.bin")
	if err := os.WriteFile(targetFile, []byte("aaaa"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(targetRoot, 0o700)
		_ = os.Chmod(targetFile, 0o600)
	})
	before := snapshotTree(t, targetRoot)
	first, err := Inspect(context.Background(), Options{SourcePath: sourceRoot, TargetHome: targetRoot})
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshotTree(t, targetRoot); !reflect.DeepEqual(before, after) {
		t.Fatalf("target inspection changed tree before=%#v after=%#v", before, after)
	}
	if first.TargetSnapshotFiles != 1 || first.TargetSnapshotBytes != 4 || first.TargetSnapshotSHA256 == "" {
		t.Fatalf("target snapshot=%#v", first)
	}
	if first.EstimatedStagingBytes < first.EstimatedSourceBytes*2+stagingWorkingSpaceBytes+4 {
		t.Fatalf("staging estimate excludes target snapshot: %#v", first)
	}
	if err := os.Chmod(targetRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetFile, []byte("bbbb"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	second, err := Inspect(context.Background(), Options{SourcePath: sourceRoot, TargetHome: targetRoot})
	if err != nil {
		t.Fatal(err)
	}
	if first.TargetSnapshotBytes != second.TargetSnapshotBytes || first.TargetSnapshotSHA256 == second.TargetSnapshotSHA256 || first.PlanSHA256 == second.PlanSHA256 {
		t.Fatalf("same-size target content change was not bound first=%#v second=%#v", first, second)
	}
}

func TestInspectReportsOwnerCountsWithoutStableOwnerIdentifiers(t *testing.T) {
	database := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "local", Name: "Local", Path: ""}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_user_settings(user_id,enabled,updated_at) VALUES('scientist@example.com',1,CURRENT_TIMESTAMP),('',1,CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := Inspect(context.Background(), Options{SourcePath: database})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Sources[0].DistinctOwners != 2 || plan.Sources[0].UnresolvedOwners != 1 || !hasIssue(plan.Issues, "source_owner_unresolved") {
		t.Fatalf("owner projection=%#v issues=%#v", plan.Sources[0], plan.Issues)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"local", "scientist@example.com"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("owner identifier leaked in plan: %s", encoded)
		}
	}
}

func TestInspectDetectsRepresentativeActiveAuthorities(t *testing.T) {
	database := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "owner-a", Name: "Research", Path: ""}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: "project-1", AgentName: "OPERON", Status: "processing", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO frame_execution_claims(frame_id,runner_id,state,runner_status,attempt,updated_at_ms) VALUES('frame-1','runner-1','claimed','running',1,1)`,
		`INSERT INTO attachment_uploads(id,user_id,project_id,filename,content_type,total_size,chunk_size,expected_chunks,created_at,updated_at) VALUES('upload-1','owner-a','project-1','input.csv','text/csv',4,4,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		`INSERT INTO workspace_outbox(event_id,idempotency_key,topic,partition_key,event_type,payload_json,status,max_attempts,occurred_at_ms,available_at_ms) VALUES('event-1','key-1','workspace','project-1','frame.updated','{}','pending',3,1,1)`,
		`INSERT INTO compute_pending_terminate(sandbox_id,provider,job_id,enqueued_at,attempts) VALUES('sandbox-1','local','job-1',1,0)`,
		`INSERT INTO poller_lease(provider,holder,expires_at) VALUES('local','holder-1',4102444800000)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	plan, err := Inspect(context.Background(), Options{SourcePath: database})
	if err != nil {
		t.Fatal(err)
	}
	active := plan.Sources[0].ActiveAuthorities
	want := map[string]int{
		"active_frame_execution_claims": 1,
		"incomplete_attachment_uploads": 1,
		"unsettled_workspace_outbox":    1,
		"pending_compute_termination":   1,
		"active_compute_pollers":        1,
	}
	for key, count := range want {
		if active[key] != count {
			t.Fatalf("active authority %s=%d want=%d; all=%#v", key, active[key], count, active)
		}
	}
	assertIssueCodes(t, plan.Issues,
		"source_active_authority_present",
		"target_capacity_not_measured",
		"target_collision_analysis_pending",
	)
}

func TestInspectDiscoversRootAndOrganizationsInStableOrder(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		filepath.FromSlash(WorkspaceDatabaseRelative),
		filepath.Join("orgs", "zeta", filepath.FromSlash(WorkspaceDatabaseRelative)),
		filepath.Join("orgs", "alpha", filepath.FromSlash(WorkspaceDatabaseRelative)),
	} {
		database := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(database), 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := workspace.Open(database)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := Inspect(context.Background(), Options{SourcePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Sources) != 3 {
		t.Fatalf("sources=%#v", plan.Sources)
	}
	ids := []string{plan.Sources[0].ID, plan.Sources[1].ID, plan.Sources[2].ID}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("source ids are not stable: %#v", ids)
	}
}

func TestInspectClassifiesLegacyV11WithoutMutatingIt(t *testing.T) {
	database := filepath.Join(t.TempDir(), "operon.db")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE __drizzle_migrations (id INTEGER PRIMARY KEY, created_at INTEGER NOT NULL)`,
		`CREATE TABLE projects (id TEXT PRIMARY KEY,user_id TEXT NOT NULL)`,
		`CREATE TABLE frames (id TEXT PRIMARY KEY)`,
		`CREATE TABLE artifacts (id TEXT PRIMARY KEY)`,
		`CREATE TABLE artifact_versions (id TEXT PRIMARY KEY)`,
		`INSERT INTO projects(id,user_id) VALUES('project-1','legacy-owner')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Inspect(context.Background(), Options{SourcePath: database, TargetHome: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("legacy source changed during inspection")
	}
	if len(plan.Sources) != 1 || plan.Sources[0].Kind != SourceKindV11 {
		t.Fatalf("plan=%#v", plan)
	}
	assertIssueCodes(t, plan.Issues, "source_requires_v11_migration", "target_collision_analysis_pending")
}

func assertIssueCodes(t *testing.T, issues []Issue, expected ...string) {
	t.Helper()
	actual := make([]string, 0, len(issues))
	for _, issue := range issues {
		actual = append(actual, issue.Code)
	}
	sort.Strings(actual)
	sort.Strings(expected)
	if len(actual) != len(expected) {
		t.Fatalf("issues=%#v want=%#v", actual, expected)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			t.Fatalf("issues=%#v want=%#v", actual, expected)
		}
	}
}

func hasIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String() + ":" + info.ModTime().UTC().Format("20060102T150405.000000000Z")
		if entry.Type().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(content)
			value += ":" + hex.EncodeToString(digest[:])
		}
		result[filepath.ToSlash(relative)] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
