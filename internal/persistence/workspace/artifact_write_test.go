package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestWriteArtifactVersionPersistsExecutionProvenanceAtomically(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []CreateProjectInput{
		{ID: "project-a", UserID: "owner-a", Name: "Project A"},
		{ID: "project-b", UserID: "owner-b", Name: "Project B"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []CreateFrameInput{
		{ID: "frame-a", ProjectID: "project-a", AgentName: "OPERON", Status: "processing", ConversationType: "agent"},
		{ID: "frame-b", ProjectID: "project-b", AgentName: "OPERON", Status: "processing", ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []ExecutionLogRecord{
		{ID: "exec-a", FrameID: "frame-a", CellIndex: 4, KernelID: "kernel-a", CondaEnv: "scanpy", Language: "python", Source: "write report", ExitStatus: "ok"},
		{ID: "exec-a2", FrameID: "frame-a", CellIndex: 9, KernelID: "kernel-a", CondaEnv: "scanpy", Language: "python", Source: "finalize report", ExitStatus: "ok"},
		{ID: "exec-b", FrameID: "frame-b", CellIndex: 2, KernelID: "kernel-b", CondaEnv: "base", Language: "python", Source: "foreign", ExitStatus: "ok"},
	} {
		if _, err := store.SaveExecutionLog(SaveExecutionLogInput{Record: record}); err != nil {
			t.Fatal(err)
		}
	}

	newInput := func(checkpoint bool, executionIDs ...string) WriteArtifactVersionInput {
		return WriteArtifactVersionInput{
			ArtifactID: "artifact-a", ProjectID: "project-a", Name: "report.md", ContentType: "text/markdown",
			Content: bytes.NewBufferString("report"), MaxBytes: 1024, CreatedBy: "runner-a",
			RootFrameID: "frame-a", FrameID: "frame-a", Language: "python", Environment: "scanpy",
			IsCheckpoint: checkpoint, ExecutionLogIDs: executionIDs,
		}
	}
	ctx := WithMutationIdempotencyKey(context.Background(), "artifact-execution-provenance")
	_, version, err := store.WriteArtifactVersionRealtime(ctx, newInput(true, " exec-a ", "exec-a"), "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	_, replayed, err := store.WriteArtifactVersionRealtime(ctx, newInput(true, "exec-a", " exec-a "), "owner-a")
	if err != nil || replayed.ID != version.ID {
		t.Fatalf("idempotent replay version=%q want=%q err=%v", replayed.ID, version.ID, err)
	}
	for name, changed := range map[string]WriteArtifactVersionInput{
		"checkpoint": func() WriteArtifactVersionInput { value := newInput(false, "exec-a"); return value }(),
		"language": func() WriteArtifactVersionInput {
			value := newInput(true, "exec-a")
			value.Language = "r"
			return value
		}(),
		"environment": func() WriteArtifactVersionInput {
			value := newInput(true, "exec-a")
			value.Environment = "base"
			return value
		}(),
		"execution": func() WriteArtifactVersionInput { value := newInput(true, "exec-a2"); return value }(),
	} {
		if _, _, err := store.WriteArtifactVersionRealtime(ctx, changed, "owner-a"); !errors.Is(err, ErrMutationIdempotencyConflict) {
			t.Fatalf("%s changed request error=%v", name, err)
		}
	}

	var language, environment, producingCellID, cellSources string
	var envHash sql.NullString
	var checkpoint, links int
	if err := store.db.QueryRow(`
		SELECT language,environment_snapshot,env_snapshot_hash,producing_cell_id,cell_sources,is_checkpoint
		FROM artifact_version_provenance WHERE version_id=?`, version.ID).Scan(
		&language, &environment, &envHash, &producingCellID, &cellSources, &checkpoint,
	); err != nil {
		t.Fatal(err)
	}
	if language != "python" || producingCellID != "exec-a" || checkpoint != 1 || envHash.Valid {
		t.Fatalf("provenance language=%q producing=%q checkpoint=%d envHash=%#v", language, producingCellID, checkpoint, envHash)
	}
	var environmentValue map[string]any
	if err := json.Unmarshal([]byte(environment), &environmentValue); err != nil ||
		environmentValue["environment"] != "scanpy" || environmentValue["language"] != "python" ||
		environmentValue["kind"] != "environment_binding" || environmentValue["snapshot_status"] != "not_captured" {
		t.Fatalf("environment=%q decoded=%#v err=%v", environment, environmentValue, err)
	}
	var sources []map[string]any
	if err := json.Unmarshal([]byte(cellSources), &sources); err != nil || len(sources) != 1 || sources[0]["execution_log_id"] != "exec-a" || sources[0]["cell_index"] != float64(4) {
		t.Fatalf("cellSources=%q decoded=%#v err=%v", cellSources, sources, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_version_execution_links WHERE version_id=?`, version.ID).Scan(&links); err != nil || links != 1 {
		t.Fatalf("execution links=%d err=%v", links, err)
	}
	records, err := store.ListExecutionLog("frame-a", version.ID)
	if err != nil || len(records) != 1 || records[0].ID != "exec-a" {
		t.Fatalf("version execution records=%#v err=%v", records, err)
	}

	multi := newInput(false, "exec-a2", "exec-a")
	multi.ArtifactID = "artifact-multi-lineage"
	_, multiVersion, err := store.WriteArtifactVersionRealtime(
		WithMutationIdempotencyKey(context.Background(), "artifact-multi-lineage"), multi, "owner-a",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT producing_cell_id,cell_sources FROM artifact_version_provenance WHERE version_id=?`, multiVersion.ID).Scan(
		&producingCellID, &cellSources,
	); err != nil {
		t.Fatal(err)
	}
	sources = nil
	if err := json.Unmarshal([]byte(cellSources), &sources); err != nil || len(sources) != 2 ||
		sources[0]["execution_log_id"] != "exec-a" || sources[0]["cell_index"] != float64(4) ||
		sources[1]["execution_log_id"] != "exec-a2" || sources[1]["cell_index"] != float64(9) || producingCellID != "exec-a2" {
		t.Fatalf("ordered sources=%q decoded=%#v producer=%q err=%v", cellSources, sources, producingCellID, err)
	}

	blobFilesBefore, markersBefore := 0, 0
	if err := filepath.WalkDir(store.blobRoot, func(_ string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			blobFilesBefore++
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM blob_commit_markers`).Scan(&markersBefore); err != nil {
		t.Fatal(err)
	}
	foreign := newInput(false, "exec-b")
	foreign.ArtifactID = "artifact-foreign-lineage"
	if _, _, err := store.WriteArtifactVersionRealtime(
		WithMutationIdempotencyKey(context.Background(), "artifact-foreign-lineage"), foreign, "owner-a",
	); err == nil {
		t.Fatal("artifact write accepted an execution from another project")
	}
	if _, found, err := store.GetArtifact("artifact-foreign-lineage"); err != nil || found {
		t.Fatalf("foreign lineage created artifact found=%t err=%v", found, err)
	}
	var versions, provenance, foreignLinks, ledger, markers int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE artifact_id='artifact-foreign-lineage'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_version_provenance provenance
		JOIN artifact_versions version ON version.id=provenance.version_id WHERE version.artifact_id='artifact-foreign-lineage'`).Scan(&provenance); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact_version_execution_links link
		JOIN artifact_versions version ON version.id=link.version_id WHERE version.artifact_id='artifact-foreign-lineage'`).Scan(&foreignLinks); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM mutation_result_ledger WHERE idempotency_key='artifact-foreign-lineage'`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM blob_commit_markers`).Scan(&markers); err != nil {
		t.Fatal(err)
	}
	blobFilesAfter := 0
	if err := filepath.WalkDir(store.blobRoot, func(_ string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			blobFilesAfter++
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if versions != 0 || provenance != 0 || foreignLinks != 0 || ledger != 0 || markers != markersBefore || blobFilesAfter != blobFilesBefore {
		t.Fatalf("foreign rollback versions=%d provenance=%d links=%d ledger=%d markers=%d->%d blobs=%d->%d", versions, provenance, foreignLinks, ledger, markersBefore, markers, blobFilesBefore, blobFilesAfter)
	}
}

func TestWriteArtifactVersionZeroValueDoesNotInheritProducerIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, produced, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "artifact", ProjectID: "project", Name: "report.md", ContentType: "text/markdown",
		Content: bytes.NewBufferString("produced"), MaxBytes: 1024, Language: "python", Environment: "scanpy",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, edited, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "artifact", ProjectID: "project", Name: "report.md", ContentType: "text/markdown",
		Content: bytes.NewBufferString("user edit"), MaxBytes: 1024, ParentVersionID: produced.ID,
		FreshUserEditMappings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var language, environment, envHash, cellSources sql.NullString
	if err := store.db.QueryRow(`SELECT language,environment_snapshot,env_snapshot_hash,cell_sources
		FROM artifact_version_provenance WHERE version_id=?`, edited.ID).Scan(
		&language, &environment, &envHash, &cellSources,
	); err != nil {
		t.Fatal(err)
	}
	if language.Valid || environment.Valid || envHash.Valid || cellSources.Valid {
		t.Fatalf("user edit inherited producer identity language=%#v environment=%#v envHash=%#v cellSources=%#v", language, environment, envHash, cellSources)
	}
	_, producedWithoutEnvironment, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "artifact", ProjectID: "project", Name: "report.md", ContentType: "text/markdown",
		Content: bytes.NewBufferString("new producer"), MaxBytes: 1024, ParentVersionID: produced.ID,
		Language: "python",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT environment_snapshot,env_snapshot_hash FROM artifact_version_provenance WHERE version_id=?`, producedWithoutEnvironment.ID).Scan(
		&environment, &envHash,
	); err != nil {
		t.Fatal(err)
	}
	if environment.Valid || envHash.Valid {
		t.Fatalf("new producer inherited parent environment environment=%#v envHash=%#v", environment, envHash)
	}
}

func TestWriteArtifactVersionBindsExactLiveTranscriptClaimAtomically(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "frame-a", ProjectID: "project-a", AgentName: "agent", Status: "queued", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-a", OwnerID: "owner-a", ExternalID: "frame-a", SessionID: "frame-a",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-a", RootFrameID: "frame-a", FrameID: "frame-a", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "user-a", FrameEventID: "frame-user-a",
		MessageUUID: "message-a", Text: "create report", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append user created=%t err=%v", created, err)
	}
	claimResult, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-a", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimResult.Claimed {
		t.Fatalf("claim=%#v err=%v", claimResult, err)
	}
	claim := claimResult.Claim
	_, source, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "tool-start", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"tool":"artifact_register"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := WriteArtifactVersionInput{
		ArtifactID: "artifact-a", ProjectID: "project-a", Name: "report.txt", ContentType: "text/plain",
		Content: bytes.NewBufferString("report"), MaxBytes: 1024, RootFrameID: "frame-a", FrameID: "frame-a",
		TranscriptAssociation: &ArtifactTranscriptAssociation{
			StreamUID: stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
			Attempt: claim.Attempt, SourceEventID: source.EventID, Relation: "produced",
		},
	}
	_, version, err := store.WriteArtifactVersionRealtime(WithMutationIdempotencyKey(context.Background(), "artifact-commit"), input, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	var committedVersion, relation string
	if err := store.db.QueryRow(`SELECT version_id,relation FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND source_event_id=?`, stream.UID, claim.Attempt, source.EventID).
		Scan(&committedVersion, &relation); err != nil || committedVersion != version.ID || relation != "produced" {
		t.Fatalf("commit version=%q relation=%q err=%v", committedVersion, relation, err)
	}
	stale := input
	stale.ArtifactID = "artifact-stale"
	stale.Content = bytes.NewBufferString("stale")
	copyAssociation := *input.TranscriptAssociation
	copyAssociation.ClaimToken = "wrong-token"
	stale.TranscriptAssociation = &copyAssociation
	if _, _, err := store.WriteArtifactVersionRealtime(WithMutationIdempotencyKey(context.Background(), "artifact-stale"), stale, "owner-a"); !errors.Is(err, errArtifactTranscriptClaimStale) {
		t.Fatalf("stale claim error=%v", err)
	}
	var staleArtifacts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifacts WHERE id='artifact-stale'`).Scan(&staleArtifacts); err != nil || staleArtifacts != 0 {
		t.Fatalf("stale artifact count=%d err=%v", staleArtifacts, err)
	}
}

func TestWriteArtifactVersionConcurrentWritersAdvanceCurrentExactlyOnceEach(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "owner-a", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, first, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.txt",
		Kind: "text/plain", Content: []byte("initial"),
	})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 12
	versions := make(chan ArtifactVersion, writers)
	errorsFound := make(chan error, writers)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, version, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
				ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.txt",
				ContentType: "text/plain", Content: bytes.NewBufferString(fmt.Sprintf("edit-%d", index)),
				MaxBytes: 1024, ParentVersionID: first.ID, FreshUserEditMappings: true,
			})
			if err != nil {
				errorsFound <- err
				return
			}
			versions <- version
		}(index)
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent write: %v", err)
	}
	close(versions)
	numbers := make(map[int]bool, writers)
	for version := range versions {
		if version.ParentID != first.ID {
			t.Fatalf("explicit parent = %q, want %q", version.ParentID, first.ID)
		}
		numbers[version.VersionNumber] = true
	}
	if len(numbers) != writers {
		t.Fatalf("unique version numbers = %d, want %d: %#v", len(numbers), writers, numbers)
	}
	artifact, found, err := store.GetArtifact("artifact-1")
	if err != nil || !found || artifact.CurrentVersionNumber != writers+1 {
		t.Fatalf("current artifact = %+v found=%v err=%v", artifact, found, err)
	}
}

func TestWriteArtifactVersionRejectsOversizeBeforeMetadataCommit(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "large.bin",
		ContentType: "application/octet-stream", Content: bytes.NewReader(bytes.Repeat([]byte("x"), 17)),
		MaxBytes: 16,
	}); err == nil {
		t.Fatal("expected oversize write failure")
	}
	if _, found, err := store.GetArtifact("artifact-1"); err != nil || found {
		t.Fatalf("oversize write created metadata: found=%v err=%v", found, err)
	}
	if err := filepath.WalkDir(store.blobRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Fatalf("oversize write left blob %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteArtifactVersionUsesContainedPrivateGeneratedBlobPath(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "../../unsafe-name.bin",
		ContentType: "application/octet-stream", Content: bytes.NewReader([]byte("payload")),
		MaxBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := store.blobAbsolute(version.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(store.blobRoot, absolute)
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		t.Fatalf("blob escaped root: path=%q relative=%q err=%v", absolute, relative, err)
	}
	if filepath.Base(absolute) != version.ID+".blob" {
		t.Fatalf("blob path uses caller filename: %q", absolute)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("blob permissions = %o, want private", info.Mode().Perm())
	}
}
