package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestFrameIncarnationMigrationBackfillsAndNewGenerationsNeverReuseAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	store := &Store{db: db, now: func() time.Time { return time.Now().UTC() }, blobRoot: filepath.Dir(path)}
	if err := prepareSchemaJournal(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareLegacyWorkspaceSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, store.now, 25); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,user_id,name,path,created_at,updated_at)
		VALUES('project','owner','Project','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO frames(
		id,project_id,parent_frame_id,root_frame_id,root_sequence,agent_name,status,conversation_type,name,created_at,updated_at
	) VALUES('legacy-frame','project',NULL,'legacy-frame',0,'OPERON','completed','agent','',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO frame_events(
		id,frame_id,sequence,event_type,payload,created_at
	) VALUES('legacy-source','legacy-frame',1,'frame_created','{}',?)`, now); err != nil {
		t.Fatal(err)
	}
	liveEnvelope := `{"version":1,"event":{"id":"legacy-realtime","userId":"owner","projectId":"project","rootFrameId":"legacy-frame","frameId":"legacy-frame","type":"frame_update","payload":{}},"frameEventId":"legacy-source"}`
	orphanEnvelope := `{"version":1,"event":{"id":"orphan-realtime","userId":"owner","projectId":"project","rootFrameId":"missing-frame","frameId":"missing-frame","type":"frame_update","payload":{}},"frameEventId":"missing-source"}`
	for _, row := range []struct {
		id, key, payload string
	}{
		{id: "legacy-outbox", key: "legacy-realtime", payload: liveEnvelope},
		{id: "orphan-outbox", key: "orphan-realtime", payload: orphanEnvelope},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO workspace_outbox(
			event_id,idempotency_key,topic,partition_key,event_type,aggregate_type,aggregate_id,
			payload_json,status,max_attempts,occurred_at_ms,available_at_ms
		) VALUES(?,?,'workspace.realtime','user:owner','frame_update','frame','legacy-frame',?,'pending',16,1,1)`,
			row.id, row.key, row.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, store.now, 26); err != nil {
		t.Fatal(err)
	}
	var legacyIncarnation string
	if err := db.QueryRowContext(ctx, `SELECT incarnation_id FROM frames WHERE id='legacy-frame'`).Scan(&legacyIncarnation); err != nil {
		t.Fatal(err)
	}
	if legacyIncarnation == "" {
		t.Fatal("legacy frame incarnation was not backfilled")
	}
	var migratedIncarnation string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload_json,'$.frameIncarnationId'),'')
		FROM workspace_outbox WHERE event_id='legacy-outbox'`).Scan(&migratedIncarnation); err != nil {
		t.Fatal(err)
	}
	if migratedIncarnation != legacyIncarnation {
		t.Fatalf("migrated outbox incarnation=%q frame=%q", migratedIncarnation, legacyIncarnation)
	}
	var orphanStatus, orphanError string
	if err := db.QueryRowContext(ctx, `SELECT status,COALESCE(last_error,'') FROM workspace_outbox
		WHERE event_id='orphan-outbox'`).Scan(&orphanStatus, &orphanError); err != nil {
		t.Fatal(err)
	}
	if orphanStatus != OutboxStatusDeadLetter || orphanError != "pre_v26_frame_source_unavailable" {
		t.Fatalf("orphan status=%q error=%q", orphanStatus, orphanError)
	}
	// The assertions above intentionally freeze the V26 migration result. The
	// runtime deletion exercised below requires the complete current schema,
	// including Transcript authority and activation tables added after V26.
	if err := applyVersionedSchemaMigrations(ctx, db, store.now); err != nil {
		t.Fatal(err)
	}
	frame, found, err := store.GetFrame("legacy-frame")
	if err != nil || !found {
		t.Fatalf("legacy frame found=%t err=%v", found, err)
	}
	oldOutbox, err := store.GetOutboxEvent(ctx, "legacy-outbox")
	if err != nil {
		t.Fatal(err)
	}
	oldEnvelope, err := DecodeRealtimeOutboxEnvelope(oldOutbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFrameRealtime(ctx, frame, "owner", "legacy-frame-deleted"); err != nil {
		t.Fatal(err)
	}
	if retired, err := store.RealtimeOutboxSourceRetired(ctx, oldOutbox, oldEnvelope); err != nil || !retired {
		t.Fatalf("migrated source retired=%t err=%v", retired, err)
	}
	if err := store.DeleteFrame("legacy-frame"); err == nil {
		t.Fatal("deleted frame unexpectedly remained")
	}
	current, err := store.CreateFrame(CreateFrameInput{
		ID: "legacy-frame", ProjectID: "project", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if current.IncarnationID == "" || current.IncarnationID == legacyIncarnation {
		t.Fatalf("new incarnation=%q legacy=%q", current.IncarnationID, legacyIncarnation)
	}
}

func TestFrameIncarnationColumnRejectsDuplicateAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateFrame(CreateFrameInput{
		ID: "first", ProjectID: project.ID, AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := store.db.Exec(`INSERT INTO frames(
		id,incarnation_id,project_id,parent_frame_id,root_frame_id,root_sequence,agent_name,status,conversation_type,name,created_at,updated_at
	) VALUES('second',?,'project',NULL,'second',0,'OPERON','completed','agent','',?,?)`, first.IncarnationID, now, now); err == nil {
		t.Fatal("duplicate frame incarnation was accepted")
	}
}
