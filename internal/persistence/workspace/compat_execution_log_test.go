package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatibilityExecutionLogUsesDurableCellSourcesAndFrameScope(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "owned", UserID: "local", Name: "Owned"}); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "owned", AgentName: "OPERON", Status: "completed", ConversationType: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(CreateFrameInput{
		ID: "child", ProjectID: "owned", ParentFrameID: root.ID,
		AgentName: "ANALYST", Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "artifact", ProjectID: "owned", Name: "result.txt", ContentType: "text/plain",
		Content: strings.NewReader("result"), RootFrameID: root.ID, FrameID: root.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutionLog(SaveExecutionLogInput{
		Record: ExecutionLogRecord{ID: "root-cell", FrameID: root.ID, CellIndex: 3, KernelID: "kernel-root",
			KernelKind: "analysis", CondaEnv: "python", Language: "python", Source: "print('root')",
			Stdout: "root\n", ExitStatus: "success", FilesWritten: []string{"result.txt"}},
		VersionIDs: []string{version.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveExecutionLog(SaveExecutionLogInput{Record: ExecutionLogRecord{
		ID: "child-cell", FrameID: child.ID, CellIndex: 8, KernelID: "kernel-child",
		CondaEnv: "python", Language: "python", Source: "print('child')", ExitStatus: "success",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM artifact_version_execution_links WHERE version_id = ?`, version.ID); err != nil {
		t.Fatal(err)
	}

	versionRecords, found, err := store.ListCompatibilityExecutionLog(context.Background(), "local", root.ID, version.ID)
	if err != nil || !found || len(versionRecords) != 1 || versionRecords[0].ID != "root-cell" {
		t.Fatalf("version execution records = %#v found=%v err=%v", versionRecords, found, err)
	}
	if versionRecords[0].KernelKind == nil || *versionRecords[0].KernelKind != "analysis" ||
		versionRecords[0].Stdout == nil || *versionRecords[0].Stdout != "root\n" ||
		versionRecords[0].Stderr != nil || versionRecords[0].FilesWritten == nil {
		t.Fatalf("version execution projection = %#v", versionRecords[0])
	}
	rootRecords, found, err := store.ListCompatibilityExecutionLog(context.Background(), "local", root.ID, "")
	if err != nil || !found || len(rootRecords) != 2 {
		t.Fatalf("root execution records = %#v found=%v err=%v", rootRecords, found, err)
	}
	childRecords, found, err := store.ListCompatibilityExecutionLog(context.Background(), "local", child.ID, "")
	if err != nil || !found || len(childRecords) != 1 || childRecords[0].ID != "child-cell" {
		t.Fatalf("child execution records = %#v found=%v err=%v", childRecords, found, err)
	}
	missingVersion, found, err := store.ListCompatibilityExecutionLog(context.Background(), "local", root.ID, "missing")
	if err != nil || !found || len(missingVersion) != 0 {
		t.Fatalf("missing version execution records = %#v found=%v err=%v", missingVersion, found, err)
	}
	if _, found, err := store.ListCompatibilityExecutionLog(context.Background(), "other", root.ID, ""); err != nil || found {
		t.Fatalf("foreign execution frame found=%v err=%v", found, err)
	}
}

func TestCompatibilityExecutionLogPageKeepsDurableHistoryOutsideBoundedTail(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 405; index++ {
		if _, err := store.SaveExecutionLog(SaveExecutionLogInput{Record: ExecutionLogRecord{
			ID: fmt.Sprintf("cell-%03d", index), FrameID: frame.ID, CellIndex: index, KernelID: "kernel",
			CondaEnv: "python", Language: "python", Source: fmt.Sprintf("print(%d)", index), ExitStatus: "ok",
		}}); err != nil {
			t.Fatal(err)
		}
	}

	first, found, err := store.ListCompatibilityExecutionLogPage(context.Background(), "owner", frame.ID, "", 200)
	if err != nil || !found || len(first.Records) != 200 || first.Records[0].CellIndex != 205 ||
		first.Records[199].CellIndex != 404 || first.NextBefore == "" {
		t.Fatalf("first page range=%d..%d count=%d cursor=%q found=%v err=%v",
			first.Records[0].CellIndex, first.Records[len(first.Records)-1].CellIndex,
			len(first.Records), first.NextBefore, found, err)
	}
	second, found, err := store.ListCompatibilityExecutionLogPage(
		context.Background(), "owner", frame.ID, first.NextBefore, 200,
	)
	if err != nil || !found || len(second.Records) != 200 || second.Records[0].CellIndex != 5 ||
		second.Records[199].CellIndex != 204 || second.NextBefore == "" {
		t.Fatalf("second page=%#v found=%v err=%v", second, found, err)
	}
	third, found, err := store.ListCompatibilityExecutionLogPage(
		context.Background(), "owner", frame.ID, second.NextBefore, 200,
	)
	if err != nil || !found || len(third.Records) != 5 || third.Records[0].CellIndex != 0 ||
		third.Records[4].CellIndex != 4 || third.NextBefore != "" {
		t.Fatalf("third page=%#v found=%v err=%v", third, found, err)
	}
	if _, _, err := store.ListCompatibilityExecutionLogPage(
		context.Background(), "owner", frame.ID, "not-a-cursor", 200,
	); err == nil || !strings.Contains(err.Error(), "cursor is invalid") {
		t.Fatalf("invalid cursor error=%v", err)
	}
	if _, found, err := store.ListCompatibilityExecutionLogPage(
		context.Background(), "foreign", frame.ID, "", 200,
	); err != nil || found {
		t.Fatalf("foreign page found=%v err=%v", found, err)
	}
}

func TestSaveExecutionLogRejectsRetiredFrameIncarnation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE frames SET incarnation_id='replacement-incarnation' WHERE id=?`, frame.ID); err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveExecutionLog(SaveExecutionLogInput{
		Record: ExecutionLogRecord{
			ID: "stale-cell", FrameID: frame.ID, CellIndex: 1, KernelID: "stale-kernel",
			CondaEnv: "python", Language: "python", Source: "print('stale')", ExitStatus: "ok",
		},
		ExpectedOwnerID: "owner", ExpectedProjectID: "project",
		ExpectedFrameIncarnationID:     frame.IncarnationID,
		ExpectedRootFrameIncarnationID: frame.IncarnationID,
	})
	if err == nil || !strings.Contains(err.Error(), "authority changed") {
		t.Fatalf("stale execution result error=%v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_log WHERE id='stale-cell'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale execution log rows=%d", count)
	}
}

func TestSaveExecutionLogRejectsRetiredRootIncarnation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	root, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateFrame(CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: root.ID,
		AgentName: "ANALYST", Status: "processing", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE frames SET incarnation_id='replacement-root-incarnation' WHERE id=?`, root.ID); err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveExecutionLog(SaveExecutionLogInput{
		Record: ExecutionLogRecord{
			ID: "stale-root-cell", FrameID: child.ID, CellIndex: 1, KernelID: "stale-kernel",
			CondaEnv: "python", Language: "python", Source: "print('stale')", ExitStatus: "ok",
		},
		ExpectedOwnerID: "owner", ExpectedProjectID: "project",
		ExpectedFrameIncarnationID:     child.IncarnationID,
		ExpectedRootFrameIncarnationID: root.IncarnationID,
	})
	if err == nil || !strings.Contains(err.Error(), "authority changed") {
		t.Fatalf("stale root execution result error=%v", err)
	}
}
