package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestComputeUsagePreservesV11MetadataAndStateMachine(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.UpsertComputeProvider(ComputeProviderInput{
		Name: "local", UserID: "owner-a", Family: "local", Environments: []string{"python"},
	}); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 7, 12, 1, 2, 3, 0, time.UTC)
	expiresAt := startedAt.Add(time.Hour)
	usage, err := store.CreateComputeUsage(ComputeUsageInput{
		ID: "usage-1", JobID: "job-1", Environment: "python", TierType: "gpu", Provider: "local",
		FrameID: "frame-1", RootFrameID: "root-1", ProjectID: "project-1", ClientUUID: "client-1",
		SubmitCellID: "cell-1", OriginToolUseID: "tool-1", Intent: "screen compounds",
		HardwareDetails: "a100", StartedAt: startedAt, ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.State != ComputeStatePending || usage.FrameID != "frame-1" || usage.RootFrameID != "root-1" ||
		usage.ProjectID != "project-1" || usage.ClientUUID != "client-1" || usage.SubmitCellID != "cell-1" ||
		usage.OriginToolUseID != "tool-1" || usage.Intent != "screen compounds" ||
		usage.HardwareDetails != "a100" || usage.ExpiresAt == nil || !usage.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("usage=%#v", usage)
	}
	if _, err := store.UpdateComputeUsageState(usage.ID, ComputeStateDone, startedAt); err == nil {
		t.Fatal("pending transitioned directly to done")
	}
	for _, next := range []ComputeState{ComputeStateStaging, ComputeStateQueued, ComputeStateRunning} {
		usage, err = store.UpdateComputeUsageState(usage.ID, next, startedAt.Add(time.Minute))
		if err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
	active, err := store.ListActiveComputeUsage("local")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("job without remote handle considered active: %#v", active)
	}
	handle := json.RawMessage(`{"sandboxId":"sb-1","provider":"modal"}`)
	specs := json.RawMessage(`[{"path":"result.csv","kind":"file"}]`)
	usage, err = store.UpdateComputeUsageRemote(usage.ID, "/workspace/job-1", handle, specs, &expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	active, err = store.ListActiveComputeUsage("local")
	if err != nil || len(active) != 1 || active[0].ID != usage.ID {
		t.Fatalf("active=%#v err=%v", active, err)
	}
	usage, err = store.UpdateComputeUsageState(usage.ID, ComputeStateHarvesting, startedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"artifacts":["result.csv"],"wall_s":12.5}`)
	usage, err = store.CompleteComputeUsage(usage.ID, ComputeStateDone, result, startedAt.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if usage.State != ComputeStateDone || usage.EndedAt == nil || !json.Valid(usage.Result) {
		t.Fatalf("completed usage=%#v", usage)
	}
}

func TestComputeUsageSchemaUpgradesLegacyCompactTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE compute_usage (
		id TEXT PRIMARY KEY, job_id TEXT NOT NULL UNIQUE, environment TEXT NOT NULL,
		tier_type TEXT NOT NULL, provider TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending',
		started_at TIMESTAMP NOT NULL, ended_at TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(compute_usage)`)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(columns)
	want := []string{
		"client_uuid", "ended_at", "environment", "expires_at", "frame_id", "hardware_details",
		"id", "intent", "job_id", "origin_tool_use_id", "output_specs", "project_id", "provider",
		"remote_handle", "remote_workdir", "result", "root_frame_id", "started_at", "state",
		"submit_cell_id", "tier_type",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(columns, want) {
		t.Fatalf("columns=%v want=%v", columns, want)
	}
	for _, table := range []string{"poller_lease", "compute_pending_terminate"} {
		var name string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("missing %s: %v", table, err)
		}
	}
	for _, index := range []string{
		"ix_compute_usage_ended_at", "ix_compute_usage_started_at", "ix_compute_usage_expires_at",
		"ix_compute_usage_state", "ix_compute_usage_frame_id", "ix_compute_usage_root_frame_id",
		"ix_compute_usage_root_open",
	} {
		var name string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&name); err != nil {
			t.Fatalf("missing %s: %v", index, err)
		}
	}
}
