package workspace

import (
	"context"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestSQLiteFileDatabaseURLKeepsWindowsDriveInLocalPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows drive URI regression")
	}
	value := sqliteFileDatabaseURL(`C:\\workspace\\workspace.sqlite`)
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "" || !strings.HasPrefix(value, "file:///C:/") {
		t.Fatalf("database URL=%q parsed=%#v", value, parsed)
	}
}

func TestWorkspaceReadPoolUsesBoundedConnectionCeiling(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	want := min(max(runtime.GOMAXPROCS(0), 2), workspaceReadPoolMaxConnections)
	if got := store.readDB.Stats().MaxOpenConnections; got != want {
		t.Fatalf("read pool max open connections = %d, want adaptive ceiling %d", got, want)
	}
}

func TestWorkspaceReadPoolRemainsAvailableDuringWriterTransaction(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.CreateProject(CreateProjectInput{ID: "project-visible", UserID: "owner-a", Name: "Visible"}); err != nil {
		t.Fatal(err)
	}
	writer, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer writer.ExecContext(context.Background(), "ROLLBACK")
	if _, err := writer.ExecContext(context.Background(), `
		INSERT INTO projects (id,user_id,name,path,created_at,updated_at)
		VALUES ('project-uncommitted','owner-a','Uncommitted','',?,?)`, time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var visible, uncommitted int
	if err := store.readDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE id='project-visible'),
		       COUNT(*) FILTER (WHERE id='project-uncommitted')
		FROM projects`).Scan(&visible, &uncommitted); err != nil {
		t.Fatalf("concurrent read: %v", err)
	}
	if visible != 1 || uncommitted != 0 {
		t.Fatalf("read snapshot visible=%d uncommitted=%d", visible, uncommitted)
	}
	var queryOnly int
	if err := store.readDB.QueryRowContext(context.Background(), "PRAGMA query_only").Scan(&queryOnly); err != nil {
		t.Fatal(err)
	}
	if queryOnly != 1 {
		t.Fatalf("query_only=%d", queryOnly)
	}
	if _, err := store.readDB.ExecContext(context.Background(), `UPDATE projects SET name='mutated' WHERE id='project-visible'`); err == nil {
		t.Fatal("read pool accepted a mutation")
	}
}

func TestCompatibilityAndTranscriptReadsRemainAvailableDuringWriterTransaction(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.CreateProject(CreateProjectInput{
		ID: "project-live", UserID: "owner-live", Name: "Live project",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-live", ProjectID: "project-live", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent", Name: "Committed name",
	}); err != nil {
		t.Fatal(err)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-live", OwnerID: "owner-live", ExternalID: "frame-live", SessionID: "frame-live",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-live", RootFrameID: "frame-live",
		FrameID: "frame-live", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}

	writer, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer writer.ExecContext(context.Background(), "ROLLBACK")
	if _, err := writer.ExecContext(context.Background(),
		"UPDATE frames SET name='uncommitted name' WHERE id='frame-live'"); err != nil {
		t.Fatal(err)
	}

	frameDone := make(chan struct {
		frame workspaceCompatibilityReadResult
	}, 1)
	go func() {
		frame, found, err := store.GetCompatibilityFrame("frame-live")
		frameDone <- struct {
			frame workspaceCompatibilityReadResult
		}{frame: workspaceCompatibilityReadResult{frame: frame, found: found, err: err}}
	}()
	select {
	case result := <-frameDone:
		if result.frame.err != nil || !result.frame.found || result.frame.frame.ID != "frame-live" || result.frame.frame.Name != "Committed name" {
			t.Fatalf("compatibility frame during writer transaction=%#v", result.frame)
		}
	case <-time.After(time.Second):
		t.Fatal("compatibility frame read waited on the writer pool")
	}

	contextValue, found, err := store.GetFrameRealtimeContext("frame-live")
	if err != nil || !found || contextValue.UserID != "owner-live" {
		t.Fatalf("frame realtime context during writer transaction found=%t value=%#v err=%v", found, contextValue, err)
	}
	owned, err := store.ProjectOwnedBy("project-live", "owner-live")
	if err != nil || !owned {
		t.Fatalf("project ownership during writer transaction owned=%t err=%v", owned, err)
	}
	readContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, found, err := repository.GetFrameStreamBySession(readContext, "owner-live", "frame-live")
	if err != nil || !found || stream.UID != "frame:frame-live" {
		t.Fatalf("transcript stream during writer transaction found=%t stream=%#v err=%v", found, stream, err)
	}
}

type workspaceCompatibilityReadResult struct {
	frame CompatibilityFrame
	found bool
	err   error
}
