package workspace

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepDataLifecycleUsesReferenceRetentionWithoutTouchingActiveWork(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-100 * 24 * time.Hour)
	store.now = func() time.Time { return old }
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "Project A"}); err != nil {
		t.Fatal(err)
	}
	for _, frame := range []CreateFrameInput{
		{ID: "terminal-root", ProjectID: "project-a", AgentName: "OPERON", Status: FrameStatusCompleted, ConversationType: "agent"},
		{ID: "active-root", ProjectID: "project-a", AgentName: "OPERON", Status: FrameStatusProcessing, ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}

	terminalArtifact, terminalVersion, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "ephemeral-terminal", ProjectID: "project-a", Name: "scratch.txt", Kind: "text",
		Content: []byte("terminal scratch"), CreatedBy: "terminal-root",
	})
	if err != nil {
		t.Fatal(err)
	}
	activeArtifact, activeVersion, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "ephemeral-active", ProjectID: "project-a", Name: "active.txt", Kind: "text",
		Content: []byte("active scratch"), CreatedBy: "active-root",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		artifactID, rootID, frameID, versionID string
	}{
		{terminalArtifact.ID, "terminal-root", "terminal-root", terminalVersion.ID},
		{activeArtifact.ID, "active-root", "active-root", activeVersion.ID},
	} {
		if _, err := store.db.Exec(`INSERT INTO artifact_runtime_metadata(
			artifact_id,root_frame_id,frame_id,latest_version_id,is_ephemeral
		) VALUES(?,?,?,?,1)`, row.artifactID, row.rootID, row.frameID, row.versionID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO content_snapshots(hash,content,size_bytes,created_at) VALUES
		('orphan-old','old',3,?),('orphan-recent','recent',6,?),('referenced-old','kept',4,?)`,
		old, now.Add(-30*time.Minute), old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact_version_provenance(
		version_id,content_type,lineage_snapshot_hash
	) VALUES(?,'text/plain','referenced-old')`, activeVersion.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO compute_usage(
		id,job_id,environment,tier_type,provider,frame_id,project_id,started_at,ended_at,state,root_frame_id
	) VALUES
		('usage-old','job-old','env','cpu','local','terminal-root','project-a',?,?, 'completed','terminal-root'),
		('usage-active','job-active','env','cpu','local','active-root','project-a',?,NULL,'running','active-root')`, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO memories(id,user_id,body,origin,evidence,superseded_by,created_at,updated_at) VALUES
		('memory-current','owner-a','current','user','stated',NULL,?,?),
		('memory-old','owner-a','old','user','stated','memory-current',?,?)`, old, old, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO queued_user_messages(
		sequence,frame_id,payload,intent_id,state,resolved_at,created_at
	) VALUES
		(1,'terminal-root','{}','resolved-old','drained',?,?),
		(2,'active-root','{}','still-queued','queued',NULL,?)`, old, old, old); err != nil {
		t.Fatal(err)
	}
	for _, input := range []RealtimeEventInput{
		{ID: "terminal-old", UserID: "owner-a", ProjectID: "project-a", RootFrameID: "terminal-root", FrameID: "terminal-root", Type: "frame_update", OccurredAt: old},
		{ID: "active-old", UserID: "owner-a", ProjectID: "project-a", RootFrameID: "active-root", FrameID: "active-root", Type: "frame_update", OccurredAt: old},
		{ID: "transcript-history-rebase:retained", UserID: "owner-a", ProjectID: "project-a", RootFrameID: "terminal-root", FrameID: "terminal-root", Type: "frame_update", OccurredAt: old},
	} {
		if _, err := store.AppendRealtimeEvent(input); err != nil {
			t.Fatal(err)
		}
	}
	delivered, err := store.EnqueueOutbox(context.Background(), EnqueueOutboxInput{
		ID: "outbox-delivered", IdempotencyKey: "outbox-delivered", Topic: "test.lifecycle",
		Type: "test", Payload: json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.EnqueueOutbox(context.Background(), EnqueueOutboxInput{
		ID: "outbox-pending", IdempotencyKey: "outbox-pending", Topic: "test.lifecycle",
		Type: "test", Payload: json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE workspace_outbox SET status='delivered',delivered_at_ms=? WHERE event_id=?`, old.UnixMilli(), delivered.ID); err != nil {
		t.Fatal(err)
	}

	store.now = func() time.Time { return now }
	report, err := store.SweepDataLifecycle(context.Background(), DefaultDataLifecyclePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if report.ContentSnapshots != 1 || report.EphemeralArtifacts != 1 || report.ComputeUsage != 1 ||
		report.SupersededMemories != 1 || report.ResolvedQueuedIntents != 1 || report.RealtimeEvents != 1 ||
		report.DeliveredOutbox != 1 || report.ExpiredDeadLetters != 0 || !report.Changed() {
		t.Fatalf("lifecycle report = %#v", report)
	}
	assertLifecycleRowCount(t, store, "artifacts", "id='ephemeral-terminal'", 0)
	assertLifecycleRowCount(t, store, "artifacts", "id='ephemeral-active'", 1)
	assertLifecycleRowCount(t, store, "content_snapshots", "hash='orphan-old'", 0)
	assertLifecycleRowCount(t, store, "content_snapshots", "hash IN ('orphan-recent','referenced-old')", 2)
	assertLifecycleRowCount(t, store, "compute_usage", "id='usage-active'", 1)
	assertLifecycleRowCount(t, store, "memories", "id='memory-current'", 1)
	assertLifecycleRowCount(t, store, "queued_user_messages", "intent_id='still-queued'", 1)
	assertLifecycleRowCount(t, store, "realtime_events", "id='active-old'", 1)
	assertLifecycleRowCount(t, store, "realtime_events", "id='transcript-history-rebase:retained'", 1)
	assertLifecycleRowCount(t, store, "workspace_outbox", "event_id='outbox-pending'", 1)
}

func TestSweepDataLifecycleRejectsZeroPolicyAndKeepsData(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SweepDataLifecycle(context.Background(), DataLifecyclePolicy{}); err == nil {
		t.Fatal("zero lifecycle policy was accepted")
	}
}

func assertLifecycleRowCount(t *testing.T, store *Store, table, predicate string, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE ` + predicate).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s WHERE %s count=%d want=%d", table, predicate, got, want)
	}
}
