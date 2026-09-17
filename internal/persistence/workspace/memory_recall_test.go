package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/memorypolicy"
)

func TestMemoryRecallRanksScopesSupportsChineseAndRecordsAccess(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for _, input := range []CreateProjectInput{
		{ID: "project-current", UserID: "user-1", Name: "Current"},
		{ID: "project-other", UserID: "user-1", Name: "Other"},
		{ID: "project-foreign", UserID: "user-2", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(input); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetMemoryEnabled(context.Background(), "user-1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO memory_categories(id,user_id,name,name_lower,guidance,auto_recall,created_at,updated_at)
		VALUES('category-hidden','user-1','Hidden','hidden','Never auto recall',0,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	memories := []CreateMemoryInput{
		{ID: "current", UserID: "user-1", Body: "当前项目的靶点选择依据", SubjectProjectID: "project-current", Origin: "user", Evidence: "observed"},
		{ID: "other", UserID: "user-1", Body: "其他项目的靶点选择记录", SubjectProjectID: "project-other", Origin: "agent_tool", Evidence: "observed"},
		{ID: "global", UserID: "user-1", Body: "全局靶点选择原则", Origin: "user", Evidence: "stated"},
		{ID: "hidden", UserID: "user-1", Body: "靶点选择隐藏记录", Origin: "agent_tool", Evidence: "observed", CategoryID: "category-hidden"},
	}
	for _, input := range memories {
		if _, err := store.CreateMemory(input); err != nil {
			t.Fatalf("CreateMemory(%s): %v", input.ID, err)
		}
	}
	if _, err := store.CreateMemory(CreateMemoryInput{ID: "foreign", UserID: "user-1", Body: "invalid", SubjectProjectID: "project-foreign", Origin: "user"}); err == nil {
		t.Fatal("foreign project memory scope was accepted")
	}

	recalled, err := store.RecallMemories(context.Background(), MemoryRecallOptions{
		UserID: "user-1", ProjectID: "project-current", Query: "靶点选择", Limit: 10, RecordAccess: true, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled) != 2 || recalled[0].ID != "current" || recalled[1].ID != "global" {
		t.Fatalf("project recall=%#v", recalled)
	}
	if recalled[0].LastSurfacedAt == nil || !recalled[0].LastSurfacedAt.Equal(now) {
		t.Fatalf("recorded recall=%#v", recalled[0])
	}
	crossProject, err := store.RecallMemories(context.Background(), MemoryRecallOptions{
		UserID: "user-1", ProjectID: "project-current", Query: "其他项目 靶点选择记录", Limit: 10, CrossProject: true, Now: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !memoryListHasID(crossProject, "other") || memoryListHasID(crossProject, "hidden") {
		t.Fatalf("cross-project recall=%#v", crossProject)
	}
	if enabled, err := store.ProjectMemoryEnabled(context.Background(), "project-current", "user-1"); err != nil || !enabled {
		t.Fatalf("default project memory enabled=%v err=%v", enabled, err)
	}
	if _, err := store.db.Exec(`UPDATE projects SET memory_enabled=0 WHERE id='project-current'`); err != nil {
		t.Fatal(err)
	}
	if enabled, err := store.ProjectMemoryEnabled(context.Background(), "project-current", "user-1"); err != nil || enabled {
		t.Fatalf("disabled project memory enabled=%v err=%v", enabled, err)
	}
}

func TestMemoryPersistenceUsesWorkspaceTextOriginAndEvidenceContract(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	accepted, err := store.CreateMemory(CreateMemoryInput{
		ID: "accepted", UserID: "owner", Body: strings.Repeat("a", 998) + "🧪", Origin: "agent_tool", Evidence: "observed",
	})
	if err != nil || memorypolicy.UTF16Length(accepted.Body) != memorypolicy.TextMaxUTF16Units {
		t.Fatalf("accepted memory = %#v, %v", accepted, err)
	}
	spaces, err := store.CreateMemory(CreateMemoryInput{ID: "spaces", UserID: "owner", Body: "   ", Origin: "user", Evidence: "stated"})
	if err != nil || spaces.Body != "   " {
		t.Fatalf("whitespace memory = %#v, %v", spaces, err)
	}
	for name, input := range map[string]CreateMemoryInput{
		"utf16_overflow":  {ID: "large", UserID: "owner", Body: strings.Repeat("a", 999) + "🧪", Origin: "user", Evidence: "stated"},
		"legacy_origin":   {ID: "origin", UserID: "owner", Body: "fact", Origin: "agent", Evidence: "observed"},
		"legacy_evidence": {ID: "evidence", UserID: "owner", Body: "fact", Origin: "user", Evidence: "verified"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CreateMemory(input); err == nil {
				t.Fatalf("CreateMemory(%s) unexpectedly succeeded", name)
			}
		})
	}
}

func TestMemoryPersistenceReadsLegacyEnumsAndRefusesAmbiguousRewrite(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateMemory(CreateMemoryInput{
		ID: "legacy", UserID: "owner", Body: "legacy fact", Origin: "agent_tool", Evidence: "observed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WithTransaction(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE memories SET origin = 'agent', evidence = 'verified' WHERE id = 'legacy'`)
		return err
	}); err != nil {
		t.Fatalf("seed legacy memory enums: %v", err)
	}
	rows, err := store.ListActiveMemories("owner", "")
	if err != nil || len(rows) != 1 || rows[0].Origin != "agent" || rows[0].Evidence != "verified" {
		t.Fatalf("legacy rows = %#v, %v", rows, err)
	}
	replacement := "new fact"
	if _, err := store.UpdateMemoryOwned(context.Background(), "legacy", "owner", UpdateMemoryInput{Body: &replacement}); err == nil || !strings.Contains(err.Error(), `unsupported memory origin "agent"`) {
		t.Fatalf("legacy update error = %v", err)
	}
	rows, err = store.ListActiveMemories("owner", "")
	if err != nil || len(rows) != 1 || rows[0].Body != "legacy fact" {
		t.Fatalf("legacy row after rejected update = %#v, %v", rows, err)
	}
}

func TestMemoryValidationAndNoArtificialActiveQuota(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateMemory(CreateMemoryInput{ID: "large", UserID: "user-1", Body: strings.Repeat("x", memorypolicy.TextMaxUTF16Units+1), Origin: "user"}); err == nil {
		t.Fatal("oversized memory body was accepted")
	}
	now := time.Now().UTC()
	if _, err := store.db.Exec(`WITH RECURSIVE seq(value) AS (
		SELECT 1 UNION ALL SELECT value+1 FROM seq WHERE value < ?
	) INSERT INTO memories(id,user_id,body,origin,evidence,created_at,updated_at)
	SELECT printf('memory-%04d',value),'user-1','memory','user','stated',?,? FROM seq`, 2048, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMemory(CreateMemoryInput{ID: "after-bulk", UserID: "user-1", Body: "still accepted", Origin: "user"}); err != nil {
		t.Fatalf("memory after bulk insert: %v", err)
	}
}

func memoryListHasID(memories []Memory, id string) bool {
	for _, memory := range memories {
		if memory.ID == id {
			return true
		}
	}
	return false
}
