package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestKernelTrustedMountV62PublishedIdentity(t *testing.T) {
	checksum, err := kernelTrustedMountV62Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "63a7bdaa944309a4b0c8b74126e8a51ab10baca7da77a6f0efccb8650e3ae09d"
	if checksum != published {
		t.Fatalf("published v62 checksum changed: got %s want %s", checksum, published)
	}
}

func TestKernelTrustedMountV62BackfillsServerOwnedMountsAndRemovesInvalidGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now, blobRoot: path + ".blobs"}
	if err := prepareSchemaJournal(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareLegacyWorkspaceSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 61); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-v62", UserID: "owner-v62", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{
		ID: "frame-v62", ProjectID: "project-v62", AgentName: "OPERON",
		Status: FrameStatusProcessing, ConversationType: "task", Name: "Task",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := KernelExecutionSessionSpecV1{
		Version: 1, KernelID: "kernel-v62", OwnerUserID: "owner-v62", ProjectID: "project-v62",
		RootFrameID: frame.RootFrameID, RootFrameIncarnationID: frame.IncarnationID,
		FrameID: frame.ID, FrameIncarnationID: frame.IncarnationID, AgentName: "OPERON",
		KernelKind: "analysis", Language: "python", Environment: "science",
		WorkspaceDir: "/srv/synon/workspace",
		Mounts: []KernelExecutionMountSpecV1{
			{Path: "/srv/synon/data/skills"},
			{Path: "/opt/synon/skills"},
			{Path: "/datasets/public"},
		},
		ProtectedPaths: []string{"/srv/synon/data", "/opt/synon/assets/optional"},
	}
	_, legacyJSON, legacySHA, err := canonicalKernelExecutionSessionSpec(legacy)
	if err != nil {
		t.Fatal(err)
	}
	backend := KernelExecutionBackend{BackendID: "backend-v62", KernelGeneration: 1}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO kernel_execution_backends(
		backend_id,owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,
		frame_id,frame_incarnation_id,kernel_id,kernel_generation,session_spec_json,session_spec_sha256,
		protocol_version,executor_instance_id,machine_boot_id,socket_path,backend_generation,state,state_version,
		created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,1,?,?)`,
		backend.BackendID, "owner-v62", "project-v62", frame.RootFrameID, frame.IncarnationID,
		frame.ID, frame.IncarnationID, legacy.KernelID, backend.KernelGeneration, legacyJSON, legacySHA,
		"executor-v62", "machine-v62", "/tmp/backend-v62.sock", 1, KernelExecutionBackendStateStarting,
		now, now); err != nil {
		t.Fatal(err)
	}
	evolved := legacy
	evolved.Mounts = append([]KernelExecutionMountSpecV1{}, legacy.Mounts...)
	evolved.Mounts[0].Trusted = true
	evolved.Mounts[1].Trusted = true
	_, evolvedJSON, evolvedSHA, err := canonicalKernelExecutionSessionSpec(evolved)
	if err != nil {
		t.Fatal(err)
	}
	invalid := KernelExecutionBackend{BackendID: "backend-v62-invalid-generation", KernelGeneration: 2}
	if _, err := db.Exec(`INSERT INTO kernel_execution_backends(
		backend_id,owner_user_id,project_id,root_frame_id,root_frame_incarnation_id,
		frame_id,frame_incarnation_id,kernel_id,kernel_generation,session_spec_json,session_spec_sha256,
		protocol_version,executor_instance_id,machine_boot_id,socket_path,backend_generation,state,state_version,
		created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?,?,1,?,?)`,
		invalid.BackendID, "owner-v62", "project-v62", frame.RootFrameID, frame.IncarnationID,
		frame.ID, frame.IncarnationID, evolved.KernelID, invalid.KernelGeneration, evolvedJSON, evolvedSHA,
		"executor-v62-invalid", "machine-v62-invalid", "/tmp/backend-v62-invalid.sock", 1,
		KernelExecutionBackendStateStarting, now, now); err != nil {
		t.Fatal(err)
	}

	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 62); err != nil {
		t.Fatal(err)
	}
	upgraded, found, err := store.GetKernelExecutionBackend(ctx, backend.BackendID)
	if err != nil || !found {
		t.Fatalf("upgraded backend=%#v found=%t err=%v", upgraded, found, err)
	}
	decoded, err := DecodeKernelExecutionSessionSpecV1(upgraded.SessionSpecJSON)
	if err != nil {
		t.Fatal(err)
	}
	trustedByPath := map[string]bool{}
	for _, mount := range decoded.Mounts {
		trustedByPath[mount.Path] = mount.Trusted
	}
	if len(decoded.Mounts) != 3 || !trustedByPath["/srv/synon/data/skills"] ||
		!trustedByPath["/opt/synon/skills"] || trustedByPath["/datasets/public"] {
		t.Fatalf("upgraded mounts=%#v", decoded.Mounts)
	}
	if foundBackend, found, err := store.FindKernelExecutionBackendForSession(ctx, evolved); err != nil || !found ||
		foundBackend.BackendID != backend.BackendID {
		t.Fatalf("evolved lookup=%#v found=%t err=%v", foundBackend, found, err)
	}
	if _, found, err := store.GetKernelExecutionBackend(ctx, invalid.BackendID); err != nil || found {
		t.Fatalf("invalid generation remained found=%t err=%v", found, err)
	}
	if _, err := db.Exec(`UPDATE kernel_execution_backends SET session_spec_json='{}' WHERE backend_id=?`, backend.BackendID); err == nil {
		t.Fatal("session specification immutability trigger was not restored")
	}
}

func TestLegacyServerOwnedKernelMountClassification(t *testing.T) {
	spec := KernelExecutionSessionSpecV1{ProtectedPaths: []string{"/srv/synon/data", "/opt/synon/assets/optional"}}
	for _, path := range []string{"/srv/synon/data/skills", "/opt/synon/skills"} {
		if !legacyServerOwnedKernelMount(spec, KernelExecutionMountSpecV1{Path: path}) {
			t.Fatalf("server-owned mount %q was not classified", path)
		}
	}
	for _, mount := range []KernelExecutionMountSpecV1{
		{Path: "/datasets/skills"},
		{Path: "/srv/synon/data/skills", Writable: true},
	} {
		if legacyServerOwnedKernelMount(spec, mount) {
			t.Fatalf("untrusted mount was classified: %#v", mount)
		}
	}
}
