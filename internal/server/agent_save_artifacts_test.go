package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	runtimekv "synon-go/internal/persistence/runtimekv"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/skills"
)

type agentSaveArtifactsFixture struct {
	server       *Server
	store        *workspace.Store
	repo         *transcriptstore.Repository
	db           *sql.DB
	stream       transcriptstore.Stream
	claim        transcriptstore.RunnerClaim
	identity     *agentKernelContext
	projectPath  string
	databasePath string
}

func newAgentSaveArtifactsFixture(t *testing.T) *agentSaveArtifactsFixture {
	return newAgentSaveArtifactsFixtureWithKernelManager(t, nil)
}

func newAgentSaveArtifactsFixtureWithKernelManager(t *testing.T, manager *kernelruntime.Manager) *agentSaveArtifactsFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	projectPath := filepath.Join(t.TempDir(), "authoritative-project")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-save", UserID: "owner-save", Name: "Artifact project", Path: projectPath,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-save", ProjectID: "project-save", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame-save", workspace.FrameRuntimeMetadata{
		FrameID: "frame-save", ContextData: map[string]any{"web_extra": map[string]any{}},
	}); err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), "frame-save")
	if err != nil || !found {
		t.Fatalf("kernel access found=%t err=%v", found, err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-save", OwnerID: access.UserID, ExternalID: access.Frame.ID, SessionID: access.Frame.ID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: access.Frame.ProjectID,
		RootFrameID: access.Frame.RootFrameID, FrameID: access.Frame.ID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "save-user", FrameEventID: "save-user-event",
		MessageUUID: "save-user-message", Text: "Create scientific artifacts.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("append input created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-save", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	fileRoot := filepath.Join(t.TempDir(), "server-data")
	if err := os.MkdirAll(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store, Transcript: repo, FileRoot: fileRoot, KernelManager: manager})
	fixture := &agentSaveArtifactsFixture{
		server: app, store: store, repo: repo, db: db, stream: stream, claim: claimed.Claim,
		identity:    &agentKernelContext{access: access, workspaceDir: projectPath},
		projectPath: projectPath, databasePath: databasePath,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := app.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
		_ = db.Close()
		_ = store.Close()
	})
	return fixture
}

func TestAgentSavedArtifactEvidenceRetainsPriorReceiptsAcrossContinuationUnit(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const doi = "10.1234/continuation.2026"
	payload, err := json.Marshal(map[string]any{
		"toolName": "web_fetch", "toolPhase": "completed", "toolCallId": "source-before-continuation",
		"toolInput": map[string]any{"url": "https://example.test/validated-source"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"doi": doi, "body": strings.Repeat("Substantive observed source material. ", 80),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "source-before-continuation-event",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := fixture.repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
		ClientMessageID: "continuation-user", FrameEventID: "continuation-user-event",
		MessageUUID: "continuation-user-message", Text: "Continue the same report and preserve verified sources.",
	}); err != nil || !created {
		t.Fatalf("append continuation created=%t err=%v", created, err)
	}
	activeIntent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active continuation intent found=%t err=%v", found, err)
	}
	// A long transcript can compact the root task message out of the bounded
	// provider replay. Durable evidence scoping must recover the continuation
	// root from the Transcript authority instead of requiring the caller to
	// reconstruct and inject that boundary from the prompt window.
	activeRun := &sessionRunnerChatRun{
		Transcript:   &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
		TaskIntentID: activeIntent.ID,
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), activeRun)
	messages, err := fixture.server.agentSavedArtifactEvidenceMessages(ctx, transcriptArtifactRun{
		Authority: activeRun.Transcript, SourceEventID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].ToolCalls) != 1 ||
		messages[0].ToolCalls[0].ID != "source-before-continuation" ||
		!strings.Contains(messages[1].Content, doi) {
		t.Fatalf("continuation lost prior durable evidence: %#v", messages)
	}
}

const validServerEthanolSDF = `
     RDKit          2D

  3  2  0  0  0  0  0  0  0  0999 V2000
    0.0000    0.0000    0.0000 C   0  0  0  0  0  0  0  0  0  0  0  0
    1.2990    0.7500    0.0000 C   0  0  0  0  0  0  0  0  0  0  0  0
    2.5981   -0.0000    0.0000 O   0  0  0  0  0  0  0  0  0  0  0  0
  1  2  1  0
  2  3  1  0
M  END
$$$$
`

const validServerEthanolSMILES = "CCO\tETHANOL\n"

func realManagedScientificKernelManagerForServerTest(t *testing.T) *kernelruntime.Manager {
	t.Helper()
	if os.Getenv("SYNON_RUN_REAL_SERVER_SDF") != "1" {
		t.Skip("set SYNON_RUN_REAL_SERVER_SDF=1 and SYNON_TEST_CONDA_ENVS_PATH to exercise the installed managed RDKit runtime")
	}
	envsPath := strings.TrimSpace(os.Getenv("SYNON_TEST_CONDA_ENVS_PATH"))
	if envsPath == "" {
		t.Fatal("SYNON_TEST_CONDA_ENVS_PATH is required for the opted-in server SDF integration tests")
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve server test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	optional := filepath.Join(root, "assets", "optional")
	manager := kernelruntime.NewManager(kernelruntime.Config{
		Python:                   "/usr/bin/python3",
		CondaHome:                filepath.Dir(envsPath),
		CondaEnvsPath:            envsPath,
		CondaRuntimeCatalog:      filepath.Join(optional, "conda-runtimes", "manifest.json"),
		ManagedPythonEnvironment: "synon-biomed-python",
		AssetRoot:                optional,
		ManifestPath:             filepath.Join(optional, "kernel-compute.manifest.json"),
		WorkerPath:               filepath.Join(optional, "kernels", "kernel_worker.py"),
		PythonHelperPath:         filepath.Join(optional, "kernels", "cheminfo_render_helpers.py"),
		SDFValidatorPath:         filepath.Join(optional, "kernels", "sdf_artifact_validator.py"),
	})
	if err := manager.Verify(); err != nil {
		t.Fatalf("verify bundled kernel assets: %v", err)
	}
	python, validator, generation, rdkitVersion, err := manager.ScientificArtifactValidator()
	if err != nil {
		t.Fatalf("resolve installed scientific validator: %v", err)
	}
	if python == "" || validator == "" || generation == "" || rdkitVersion != "2024.03.5" {
		t.Fatalf("scientific validator python=%q validator=%q generation=%q rdkit=%q", python, validator, generation, rdkitVersion)
	}
	return manager
}

func assertAgentSaveArtifactsNoCanonicalWrites(t *testing.T, fixture *agentSaveArtifactsFixture) {
	t.Helper()
	artifacts, err := fixture.store.ListArtifacts("project-save", 100, 0)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	for _, table := range []string{"artifact_versions", "transcript_artifact_commits"} {
		var count int
		if err := fixture.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s writes=%d", table, count)
		}
	}
}

func (fixture *agentSaveArtifactsFixture) toolContext(
	t *testing.T,
	toolCallID string,
	input map[string]any,
) context.Context {
	t.Helper()
	rawInput, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	rawPayload, err := json.Marshal(map[string]any{
		"toolCallId": toolCallID, "toolName": "save_artifacts", "toolPhase": "start",
		"toolInput": json.RawMessage(rawInput),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, source, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "save-source-" + toolCallID,
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: rawPayload, Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return withTranscriptArtifactRun(
		context.Background(), &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}, source.EventID,
	)
}

func (fixture *agentSaveArtifactsFixture) saveExecution(
	t *testing.T,
	access workspace.KernelFrameAccess,
	workspaceDir, executionID string,
	cellIndex int,
	writes ...kernelruntime.FileWrite,
) {
	t.Helper()
	kernelID, err := kernelruntime.StableSessionID(kernelruntime.SessionSpec{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, DelegateName: access.DelegateName,
		KernelKind: "analysis", Language: "python", Environment: "scanpy", WorkspaceDir: workspaceDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SaveExecutionLog(workspace.SaveExecutionLogInput{
		Record: workspace.ExecutionLogRecord{
			ID: executionID, FrameID: access.Frame.ID, CellIndex: cellIndex, KernelID: kernelID,
			KernelKind: "analysis", CondaEnv: "scanpy", Language: "python", Source: "write outputs",
			ExitStatus: "ok", Origin: "agent", FilesWritten: writes,
		},
		ExpectedOwnerID: access.UserID, ExpectedProjectID: access.Frame.ProjectID,
		ExpectedFrameIncarnationID:     access.Frame.IncarnationID,
		ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeAgentSaveArtifactsFile(t *testing.T, root, relativePath, content string) kernelruntime.FileWrite {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(content))
	return kernelruntime.FileWrite{Path: relativePath, SHA256: hex.EncodeToString(digest[:])}
}

func agentSaveArtifactResults(t *testing.T, result map[string]any) []map[string]any {
	t.Helper()
	raw, ok := result["artifacts"].([]any)
	if !ok {
		t.Fatalf("artifacts=%#v", result["artifacts"])
	}
	artifacts := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		artifact, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("artifact=%#v", value)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}

func agentSaveArtifactFailures(t *testing.T, result map[string]any) []map[string]any {
	t.Helper()
	raw, ok := result["errors"].([]any)
	if !ok {
		t.Fatalf("errors=%#v", result["errors"])
	}
	failures := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		failure, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("failure=%#v", value)
		}
		failures = append(failures, failure)
	}
	return failures
}

func assertAgentSaveArtifactFailure(t *testing.T, failure map[string]any, path, code string) {
	t.Helper()
	if failure["path"] != path || failure["code"] != code || failure["retryable"] != false {
		t.Fatalf("failure=%#v path=%q code=%q", failure, path, code)
	}
	recovery, ok := failure["recovery"].(map[string]any)
	if !ok || strings.TrimSpace(stringValue(recovery["action"])) == "" {
		t.Fatalf("failure recovery=%#v", failure)
	}
}

func TestAgentSaveArtifactsMatchesObservedClaudeContract(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	reportWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Report\n")
	tableWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/results.csv", "name,value\nA,1\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-save-1", 3, reportWrite, tableWrite)
	input := map[string]any{
		"files": []any{"out/report.md", "out/results.csv"}, "language": "python", "environment": "scanpy",
		"checkpoints": []any{"out/results.csv"}, "human_description": "Saving analysis outputs",
	}
	ctx := fixture.toolContext(t, "save-call-1", input)
	result, err := fixture.server.executeAgentSaveArtifacts(ctx, fixture.identity, "save-call-1", input)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 2 || result["errors"] != nil {
		t.Fatalf("result=%#v", result)
	}
	expectedTypes := []string{"text/markdown", "text/csv"}
	for index, artifact := range artifacts {
		versionID := stringValue(artifact["version_id"])
		if artifact["input_path"] != input["files"].([]any)[index] || artifact["content_type"] != expectedTypes[index] ||
			artifact["environment"] != "scanpy" || artifact["root_frame_id"] != "frame-save" ||
			artifact["preview_url"] != "/#/artifacts/"+stringValue(artifact["artifact_id"])+"?version="+versionID ||
			artifact["content_url"] != "/api/artifacts/"+stringValue(artifact["artifact_id"])+"/versions/"+versionID ||
			artifact["uri"] != nil ||
			artifact["publication_state"] != "draft" || artifact["artifact_ref"] != nil || artifact["markdown_link"] != nil ||
			len(stringValue(artifact["checksum"])) != 64 || stringValue(artifact["storage_path"]) == "" {
			t.Fatalf("artifact[%d]=%#v", index, artifact)
		}
		if intermediate, found, stateErr := fixture.store.ArtifactVersionIntermediate(versionID); stateErr != nil || !found || !intermediate {
			t.Fatalf("artifact[%d] intermediate=%t found=%t err=%v", index, intermediate, found, stateErr)
		}
		wantCheckpoint := index == 1
		if checkpoint, _ := artifact["is_checkpoint"].(bool); checkpoint != wantCheckpoint {
			t.Fatalf("artifact[%d] checkpoint=%v", index, artifact["is_checkpoint"])
		}
		_, version, reader, found, err := fixture.store.OpenArtifactVersionContent(versionID)
		if err != nil || !found {
			t.Fatalf("open version=%q found=%t err=%v", versionID, found, err)
		}
		content, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || version.ContentSHA256 != stringValue(artifact["checksum"]) || len(content) == 0 {
			t.Fatalf("version=%#v bytes=%d readErr=%v closeErr=%v", version, len(content), readErr, closeErr)
		}
		records, err := fixture.store.ListExecutionLog("frame-save", versionID)
		if err != nil || len(records) != 1 || records[0].ID != "exec-save-1" {
			t.Fatalf("version=%q records=%#v err=%v", versionID, records, err)
		}
	}

	firstReport := artifacts[0]
	replayed, err := fixture.server.executeAgentSaveArtifacts(ctx, fixture.identity, "save-call-1", input)
	if err != nil {
		t.Fatal(err)
	}
	replayedArtifacts := agentSaveArtifactResults(t, replayed)
	if len(replayedArtifacts) != 2 || replayedArtifacts[0]["version_id"] != firstReport["version_id"] || replayedArtifacts[1]["version_id"] != artifacts[1]["version_id"] {
		t.Fatalf("idempotent replay=%#v", replayed)
	}
	unchangedContext := fixture.toolContext(t, "save-call-unchanged", input)
	unchangedResult, err := fixture.server.executeAgentSaveArtifacts(
		unchangedContext, fixture.identity, "save-call-unchanged", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	unchangedArtifacts := agentSaveArtifactResults(t, unchangedResult)
	if len(unchangedArtifacts) != 2 ||
		unchangedArtifacts[0]["version_id"] != firstReport["version_id"] ||
		unchangedArtifacts[1]["version_id"] != artifacts[1]["version_id"] ||
		!boolValue(unchangedArtifacts[0]["unchanged"], false) ||
		!boolValue(unchangedArtifacts[1]["unchanged"], false) {
		t.Fatalf("cross-call unchanged publication=%#v", unchangedResult)
	}

	updatedWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Updated report\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-save-2", 4, updatedWrite)
	versionInput := map[string]any{
		"files": []any{"out/report.md"}, "language": "python", "environment": "scanpy",
		"human_description": "Updating analysis report",
	}
	versionContext := fixture.toolContext(t, "save-call-2", versionInput)
	versionResult, err := fixture.server.executeAgentSaveArtifacts(versionContext, fixture.identity, "save-call-2", versionInput)
	if err != nil {
		t.Fatal(err)
	}
	versions := agentSaveArtifactResults(t, versionResult)
	if len(versions) != 1 || versions[0]["artifact_id"] != firstReport["artifact_id"] || versions[0]["version_number"] != 2 {
		t.Fatalf("version result=%#v", versionResult)
	}
	_, secondVersion, found, err := fixture.store.GetArtifactVersionMetadata(stringValue(versions[0]["version_id"]))
	if err != nil || !found || secondVersion.ParentID != stringValue(firstReport["version_id"]) {
		t.Fatalf("second version=%#v found=%t err=%v", secondVersion, found, err)
	}

	partialInput := map[string]any{
		"files": []any{"out/results.csv", "out/missing.csv"}, "language": "python", "environment": "scanpy",
		"human_description": "Saving available tables",
	}
	partial, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-call-partial", partialInput), fixture.identity, "save-call-partial", partialInput,
	)
	if err != nil || len(agentSaveArtifactResults(t, partial)) != 1 {
		t.Fatalf("partial=%#v err=%v", partial, err)
	}
	errorsFound := agentSaveArtifactFailures(t, partial)
	if len(errorsFound) != 1 || errorsFound[0]["path"] != "out/missing.csv" || errorsFound[0]["code"] != "file_not_found" ||
		strings.Contains(fmt.Sprintf("%#v", errorsFound[0]), fixture.projectPath) {
		t.Fatalf("partial errors=%#v", partial["errors"])
	}
	if partial["ok"] != false || partial["partial"] != true || partial["code"] != "artifact_save_requires_correction" ||
		agentruntime.ClassifyToolResult(partial) != agentruntime.ToolResultPartial {
		t.Fatalf("partial correction envelope=%#v", partial)
	}

	reopened, err := workspace.Open(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if artifact, found, err := reopened.GetArtifact(stringValue(firstReport["artifact_id"])); err != nil || !found || artifact.CurrentVersionNumber != 2 {
		t.Fatalf("reopened artifact=%#v found=%t err=%v", artifact, found, err)
	}
}

func TestAgentSaveArtifactsPersistsUnsupportedEvidenceAsDraftWarning(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Report\nUnsupported PMID 24893891.\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-unverified-report", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md"}, "language": "text", "human_description": "Saving unverified report",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-unverified-report", input), fixture.identity, "save-unverified-report", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("unsupported evidence draft err=%v result=%#v", err, result)
	}
	if outcome := agentruntime.ClassifyToolResult(result); outcome != agentruntime.ToolResultSucceeded {
		t.Fatalf("draft warning classified as %s: %#v", outcome, result)
	}
	warnings := anySliceValue(result["warnings"])
	if len(warnings) != 1 || result["completion_pending"] != nil || result["next_action"] != nil {
		t.Fatalf("unsupported evidence warnings=%#v", result)
	}
	warning := mapValue(warnings[0])
	grading := mapValue(warning["evidence_grading"])
	if warning["code"] != "evidence_binding_advisory" || warning["legacy_code"] != "unsupported_evidence_references" ||
		boolValue(warning["blocking"], true) || !boolValue(warning["quality_advisory"], false) ||
		stringValue(warning["evidence_status"]) != "claim_source_binding_not_established" ||
		stringValue(grading["source_identity"]) != "retained_as_authored" ||
		stringValue(grading["source_authenticity"]) != "not_invalidated" ||
		stringValue(grading["retrieval_depth"]) != "not_established_for_this_binding" ||
		stringValue(grading["source_tier"]) != "not_assigned" ||
		stringValue(grading["semantic_support"]) != "not_established_for_this_claim" ||
		!strings.Contains(fmt.Sprintf("%#v", warning["unsupported_evidence_references"]), "pmid:24893891") {
		t.Fatalf("unsupported evidence diagnostics=%#v", result)
	}
}

func TestAgentSaveArtifactsPersistsLocalizedLedgerGroundingWarning(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const filename = "研发证据表.csv"
	content := "证据类别,来源,关键结论,标识\n" +
		"临床管线,企业官网,企业页面声称该候选已经进入一期临床并正在招募目标患者人群,CompanyName\n"
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, filename, content)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-localized-ledger", 1, write)
	input := map[string]any{
		"files": []any{filename}, "language": "text", "human_description": "Saving localized evidence ledger",
	}
	ctx := withTranscriptRunnerChatRun(
		fixture.toolContext(t, "save-localized-ledger", input),
		&sessionRunnerChatRun{
			Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
			ReviewPolicy: &sessionRunnerResolvedReviewPolicy{
				EvidenceReviewRequired: false,
			},
		},
	)
	result, err := fixture.server.executeAgentSaveArtifacts(
		ctx, fixture.identity, "save-localized-ledger", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("localized evidence draft err=%v result=%#v", err, result)
	}
	warnings := anySliceValue(result["warnings"])
	if len(warnings) != 1 || result["completion_pending"] != nil || result["next_action"] != nil {
		t.Fatalf("localized evidence warnings=%#v", result)
	}
	warning := mapValue(warnings[0])
	if warning["code"] != "evidence_binding_advisory" || warning["legacy_code"] != "unsupported_evidence_references" ||
		boolValue(warning["blocking"], true) || !boolValue(warning["quality_advisory"], false) ||
		!strings.Contains(fmt.Sprintf("%#v", warning["unsupported_evidence_references"]), "evidence_source_locator_missing") {
		t.Fatalf("localized evidence diagnostics=%#v", warning)
	}
}

func TestAgentSaveArtifactsPersistsEmbeddedEvidenceTableGroundingWarning(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const filename = "research_report.md"
	content := "# Report\n\n| 证据ID | 研究主题 | 核心发现 | 文献来源/参考 |\n" +
		"|---|---|---|---|\n" +
		"| E001 | 临床管线 | 企业页面声称该候选已经进入一期临床并正在招募目标患者人群 | Multiple company announcements |\n"
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, filename, content)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-embedded-ledger", 1, write)
	input := map[string]any{
		"files": []any{filename}, "language": "text", "human_description": "Saving report with embedded evidence",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-embedded-ledger", input), fixture.identity, "save-embedded-ledger", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("embedded evidence draft err=%v result=%#v", err, result)
	}
	warnings := anySliceValue(result["warnings"])
	if len(warnings) != 1 || !strings.Contains(
		fmt.Sprintf("%#v", mapValue(warnings[0])["unsupported_evidence_references"]),
		"evidence_source_locator_missing",
	) {
		t.Fatalf("embedded evidence diagnostics=%#v", result)
	}
}

func TestAgentSaveArtifactEvidenceAcceptsGroundedEmbeddedMarkdownTable(t *testing.T) {
	const doi = "10.1080/grounded.example"
	arguments, _ := json.Marshal(map[string]any{"doi": doi})
	result, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
		"recordAvailable": true,
		"abstractText":    "This publication reports a substantive analysis of the candidate mechanism and observed results.",
		"doi":             doi,
	}})
	evidence := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "publication", Name: "fetch_article_fulltext", Arguments: arguments, VerifiedEvidence: true}}},
		{Role: "tool", ToolCallID: "publication", Content: string(result)},
	}
	snapshot, err := os.CreateTemp(t.TempDir(), "grounded-report-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	content := "# Report\n\n| 证据ID | 研究主题 | 核心发现 | 文献来源/参考 |\n" +
		"|---|---|---|---|\n" +
		"| E001 | 作用机制 | 该研究系统分析了候选机制并报告了实质性观察结果和研究结论 | " + doi + " |\n"
	if _, err := snapshot.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err := (&Server{}).validateAgentSavedArtifactEvidenceForPath("report.md", snapshot, evidence); err != nil {
		t.Fatalf("grounded embedded evidence was rejected: %v", err)
	}
}

func TestAgentSaveArtifactsDoesNotAutoReviewCitationsWhenVerifierDisabled(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(
		t, fixture.projectPath, "out/report.md", "# Report\nExternal discussion cites PMID 24893891.\n",
	)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-review-disabled", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md"}, "language": "text",
		"human_description": "Saving report without automatic review",
	}
	ctx := fixture.toolContext(t, "save-review-disabled", input)
	ctx = withTranscriptRunnerChatRun(ctx, &sessionRunnerChatRun{
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
		ReviewPolicy: &sessionRunnerResolvedReviewPolicy{
			EvidenceReviewRequired: false,
		},
	})
	result, err := fixture.server.executeAgentSaveArtifacts(
		ctx, fixture.identity, "save-review-disabled", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("disabled verifier save result=%#v err=%v", result, err)
	}
}

func TestAgentSaveArtifactsKeepsSourceLineageWithoutPlanWhenReviewerDisabled(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if fixture.server.skillCatalog == nil {
		fixture.server.skillCatalog = skills.NewCatalog()
	}
	fixture.server.skillCatalog.AddSkill(skills.Skill{
		Name: "source-workflow", Tools: []string{"web_research"},
	})
	intent, found, err := fixture.repo.EnsureActiveFrameTaskIntent(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("active task intent found=%t err=%v", found, err)
	}
	checkpoint, err := json.Marshal(map[string]any{
		"toolName": "web_research", "toolPhase": "completed", "toolCallId": "research-discovery",
		"toolInput": map[string]any{"operation": "search_and_fetch", "query": "TARGET7 evidence"},
		"toolResult": map[string]any{"ok": true, "result": map[string]any{
			"candidateSources": []any{map[string]any{"url": "https://unsupported.example/discovery"}},
			"documents": []any{map[string]any{
				"url": "https://qualified.example/study", "content": strings.Repeat("TARGET7 measured evidence ", 100),
				"readReceipt": map[string]any{"deepRead": true, "queryRelevant": true},
			}},
			"quality": map[string]any{"deepReadSources": 1},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "research-discovery-checkpoint",
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: checkpoint,
	}); err != nil {
		t.Fatal(err)
	}
	write := writeAgentSaveArtifactsFile(
		t, fixture.projectPath, "out/research.md", "# Research\nUnsupported source: https://unsupported.example/discovery\n",
	)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-research-lineage", 1, write)
	input := map[string]any{
		"files": []any{"out/research.md"}, "language": "text",
		"human_description": "Saving research before final publication",
	}
	run := &sessionRunnerChatRun{
		SessionID: fixture.stream.FrameID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		TaskIntentID: intent.ID, Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
		ReviewPolicy:                   &sessionRunnerResolvedReviewPolicy{EvidenceReviewRequired: false},
		VerificationExplicitlyDisabled: true,
	}
	run.setToolCapabilityCatalog([]agentruntime.ToolSchema{{
		Name: "web_research", Capabilities: []string{"research", "source-evidence"},
		Exposure: agentruntime.ToolExposureDeferred,
	}})
	run.addExecutedSkillNames("source-workflow")
	ctx := withTranscriptRunnerChatRun(fixture.toolContext(t, "save-research-lineage", input), run)
	result, err := fixture.server.executeAgentSaveArtifacts(
		ctx, fixture.identity, "save-research-lineage", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("research save result=%#v err=%v", result, err)
	}
	warnings := anySliceValue(result["warnings"])
	if len(warnings) != 1 || stringValue(mapValue(warnings[0])["code"]) != "evidence_binding_advisory" {
		t.Fatalf("research source lineage was disabled with the reviewer: %#v", result)
	}
}

func TestAgentSaveArtifactsCorrectionPublicationIsIdempotentAndValidatorOwned(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const original = `{"sources":[{"id":"source-1","claims_supported":["claim-1"]}]}`
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "source_evidence.json", original)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-source-ledger", 1, write)
	input := map[string]any{
		"files": []any{"source_evidence.json"}, "language": "text",
		"human_description": "Saving source evidence",
	}
	first, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-source-ledger-1", input), fixture.identity, "save-source-ledger-1", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, first)) != 1 {
		t.Fatalf("initial source ledger save=%#v err=%v", first, err)
	}
	artifactID := stringValue(agentSaveArtifactResults(t, first)[0]["artifact_id"])

	correctionRun := &sessionRunnerChatRun{
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed (cross_artifact_failures=1): cross-artifact consistency failures source_claim_missing_attested_excerpt:source_evidence.json source=source-1 claim=claim-1",
	}
	unchangedContext := withTranscriptRunnerChatRun(
		fixture.toolContext(t, "save-source-ledger-unchanged", input), correctionRun,
	)
	unchanged, err := fixture.server.executeAgentSaveArtifacts(
		unchangedContext, fixture.identity, "save-source-ledger-unchanged", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, unchanged)) != 1 || agentSaveArtifactResults(t, unchanged)[0]["unchanged"] != true {
		t.Fatalf("unchanged correction result=%#v err=%v", unchanged, err)
	}
	if artifact, _, found, err := fixture.store.GetCurrentArtifactVersionMetadata(artifactID); err != nil || !found || artifact.CurrentVersionNumber != 1 {
		t.Fatalf("unchanged correction advanced artifact=%#v found=%t err=%v", artifact, found, err)
	}

	const corrected = `{"sources":[{"id":"source-1","claims_supported":[{"claim_id":"claim-1","source_locator":"tool:source-1","evidence_excerpt":"This durable source excerpt supports claim one exactly."}]}]}`
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "source_evidence.json", corrected)
	correctedContext := withTranscriptRunnerChatRun(
		fixture.toolContext(t, "save-source-ledger-corrected", input), correctionRun,
	)
	correctedResult, err := fixture.server.executeAgentSaveArtifacts(
		correctedContext, fixture.identity, "save-source-ledger-corrected", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, correctedResult)) != 1 {
		t.Fatalf("corrected source ledger save=%#v err=%v", correctedResult, err)
	}
	if artifact, _, found, err := fixture.store.GetCurrentArtifactVersionMetadata(artifactID); err != nil || !found || artifact.CurrentVersionNumber != 2 {
		t.Fatalf("corrected artifact=%#v found=%t err=%v", artifact, found, err)
	}
	// A new execution unit has a new in-memory run object. Idempotency must be
	// derived from the durable current version rather than process-local state.
	idempotent, err := fixture.server.executeAgentSaveArtifacts(
		withTranscriptRunnerChatRun(fixture.toolContext(t, "save-source-ledger-idempotent", input), &sessionRunnerChatRun{
			CorrectionReason: correctionRun.CorrectionReason,
			CorrectionDetail: correctionRun.CorrectionDetail,
		}),
		fixture.identity, "save-source-ledger-idempotent", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, idempotent)) != 1 || agentSaveArtifactResults(t, idempotent)[0]["unchanged"] != true {
		t.Fatalf("idempotent corrected save=%#v err=%v", idempotent, err)
	}
	if artifact, _, found, err := fixture.store.GetCurrentArtifactVersionMetadata(artifactID); err != nil || !found || artifact.CurrentVersionNumber != 2 {
		t.Fatalf("idempotent save advanced artifact=%#v found=%t err=%v", artifact, found, err)
	}
}

func TestAgentSaveArtifactsPublishesRawExecutionLogByteFaithfullyWithoutCitationRewriting(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := strings.Join([]string{
		"workspace=/tmp/task/4611-5dab-9583/2F27",
		"reference=https://github.com/forlilab/Meeko doi:10.1038/nmeth.2681",
		"PDB 9V09 docking completed",
		"",
	}, "\n")
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/execution.log", content)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-raw-log", 1, write)
	input := map[string]any{
		"files": []any{"out/execution.log"}, "language": "bash", "environment": "vina",
		"human_description": "Saving immutable execution log",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-raw-log", input), fixture.identity, "save-raw-log", input,
	)
	if err != nil {
		t.Fatalf("raw execution log save failed: %v result=%#v", err, result)
	}
	artifacts := agentSaveArtifactResults(t, result)
	_, hasFailures := result["errors"]
	if len(artifacts) != 1 || artifacts[0]["filename"] != "execution.log" || hasFailures {
		t.Fatalf("raw execution log result=%#v", result)
	}
	wantSHA := sha256.Sum256([]byte(content))
	if artifacts[0]["checksum"] != hex.EncodeToString(wantSHA[:]) {
		t.Fatalf("raw execution log checksum=%v want=%x", artifacts[0]["checksum"], wantSHA)
	}
	records, err := fixture.store.ListExecutionLog("frame-save", stringValue(artifacts[0]["version_id"]))
	if err != nil || len(records) != 1 || records[0].ID != "execution-raw-log" {
		t.Fatalf("raw execution log lineage=%#v err=%v", records, err)
	}
}

func TestAgentSaveArtifactsRejectsRawExecutionLogWithoutExactExecutionLineage(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/untracked.log", "tool output\n")
	input := map[string]any{
		"files": []any{"out/untracked.log"}, "language": "bash",
		"human_description": "Saving untracked execution log",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-untracked-log", input), fixture.identity, "save-untracked-log", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) {
		t.Fatalf("untracked raw log err=%v result=%#v", err, result)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 || failures[0]["code"] != "raw_execution_lineage_unavailable" {
		t.Fatalf("untracked raw log diagnostics=%#v", result)
	}
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
}

func TestAgentSaveArtifactsPublishesValidMachineReadableJSONAndRejectsMalformedJSON(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	jsonWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/results.json", "{\"value\": 1}\n")
	jsonlWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/results.jsonl", "{\"value\": 1}\n")
	invalidWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/invalid.json", "{not-json}\n")
	csvWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/results.csv", "name,value\nA,1\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-json-policy", 1, jsonWrite, jsonlWrite, invalidWrite, csvWrite)
	input := map[string]any{
		"files":    []any{"out/results.json", "out/results.jsonl", "out/invalid.json", "out/results.csv"},
		"language": "python", "environment": "scanpy", "human_description": "Saving scientific comparison outputs",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-json-policy", input), fixture.identity, "save-json-policy", input,
	)
	if err != nil {
		t.Fatalf("mixed JSON policy save returned error: %v", err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 3 || artifacts[0]["filename"] != "results.json" ||
		artifacts[1]["filename"] != "results.jsonl" || artifacts[2]["filename"] != "results.csv" {
		t.Fatalf("published artifacts=%#v", result)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 {
		t.Fatalf("policy failures=%#v", result["errors"])
	}
	assertAgentSaveArtifactFailure(t, failures[0], "out/invalid.json", "invalid_json_artifact")
	if recovery := failures[0]["recovery"].(map[string]any); !strings.Contains(stringValue(recovery["format_policy"]), "exactly one JSON value") {
		t.Fatalf("JSON recovery=%#v", recovery)
	}
	listed, err := fixture.store.ListArtifacts("project-save", 100, 0)
	if err != nil || len(listed) != 3 {
		t.Fatalf("canonical artifacts=%#v err=%v", listed, err)
	}
}

func TestAgentSaveArtifactsRejectsUnrenderedTemplateBeforeCanonicalWrite(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Results\n{% for row in results %}\n{{ row.value }}\n{% endfor %}\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-unrendered-template", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md"}, "language": "text",
		"human_description": "Saving rendered report",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-unrendered-template", input), fixture.identity, "save-unrendered-template", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) {
		t.Fatalf("unrendered template err=%v result=%#v", err, result)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 {
		t.Fatalf("template failures=%#v", result)
	}
	assertAgentSaveArtifactFailure(t, failures[0], "out/report.md", "unresolved_template_marker")
	recovery := failures[0]["recovery"].(map[string]any)
	if !strings.Contains(stringValue(recovery["template_policy"]), "final rendered bytes") {
		t.Fatalf("template recovery=%#v", recovery)
	}
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
}

func TestAgentSaveArtifactsAllowsRenderedMarkdownFormula(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Results\n\\(k = A e^{-E_a/(RT)}\\)\n\n| T | k |\n|---:|---:|\n| 25 | 0.0042 |\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-rendered-formula", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md"}, "language": "text",
		"human_description": "Saving rendered report",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-rendered-formula", input), fixture.identity, "save-rendered-formula", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 {
		t.Fatalf("rendered formula err=%v result=%#v", err, result)
	}
}

func TestAgentSaveArtifactsDefersUniprotReferenceValidationToCompletion(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", "# Report\nUniProt Q07820 supports the target claim.\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "execution-unverified-uniprot", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md"}, "language": "text", "human_description": "Saving unverified UniProt report",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-unverified-uniprot", input), fixture.identity, "save-unverified-uniprot", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("unsupported UniProt evidence err=%v result=%#v", err, result)
	}
	warnings := anySliceValue(result["warnings"])
	if len(warnings) != 1 {
		t.Fatalf("unsupported UniProt warnings=%#v", result)
	}
	warning := mapValue(warnings[0])
	if warning["code"] != "evidence_binding_advisory" || warning["legacy_code"] != "unsupported_evidence_references" ||
		boolValue(warning["blocking"], true) || !boolValue(warning["quality_advisory"], false) ||
		!strings.Contains(fmt.Sprintf("%#v", warning["unsupported_evidence_references"]), "accession:uniprot:Q07820") {
		t.Fatalf("unsupported UniProt diagnostics=%#v", result)
	}
	recovery := warning["recovery"].(map[string]any)
	if recovery["evidence"] != "retain_real_source_identities_and_distinguish_source_existence_from_claim_support" ||
		!strings.Contains(stringValue(recovery["receipt_format"]), "structured identifier") ||
		!strings.Contains(stringValue(recovery["uniprot_citation_format"]), "UniProt <accession>") {
		t.Fatalf("UniProt recovery=%#v", recovery)
	}
}

func TestAgentSaveArtifactsSamePathUsesOneLineageForCompletion(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	firstWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/final_report.md", "Initial report without external identifiers.\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-lineage-1", 1, firstWrite)
	firstInput := map[string]any{
		"files": []any{"out/final_report.md"}, "language": "python",
		"human_description": "Saving draft report",
	}
	first, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-lineage-1", firstInput), fixture.identity, "save-lineage-1", firstInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstArtifact := agentSaveArtifactResults(t, first)[0]

	secondWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/final_report.md", "Corrected report without unsupported identifiers.\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-lineage-2", 2, secondWrite)
	secondInput := map[string]any{
		"files": []any{"out/final_report.md"}, "language": "python",
		"human_description": "Saving corrected report",
	}
	second, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-lineage-2", secondInput), fixture.identity, "save-lineage-2", secondInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondArtifact := agentSaveArtifactResults(t, second)[0]
	if firstArtifact["artifact_id"] != secondArtifact["artifact_id"] || secondArtifact["version_number"] != 2 {
		t.Fatalf("first=%#v second=%#v", firstArtifact, secondArtifact)
	}

	commits, err := fixture.repo.ListArtifactCommitReferences(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, fixture.claim.Attempt,
	)
	if err != nil || len(commits) != 2 {
		t.Fatalf("commits=%#v err=%v", commits, err)
	}
	candidates, err := fixture.server.sessionRunnerCompletionArtifactCandidateReferences(
		sessionstore.Session{Project: &sessionstore.Project{ID: fixture.stream.ProjectID}},
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}, commits, "Corrected report attached.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if unsupported := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Corrected report attached.", nil, candidates,
	); len(unsupported) != 0 {
		t.Fatalf("superseded draft remained in completion candidates: %#v", unsupported)
	}
}

func TestAgentSaveArtifactsAcceptsValidSDFThroughInstalledRDKitRuntime(t *testing.T) {
	fixture := newAgentSaveArtifactsFixtureWithKernelManager(t, realManagedScientificKernelManagerForServerTest(t))
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/ethanol.sdf", validServerEthanolSDF)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-valid-sdf", 1, write)
	input := map[string]any{
		"files": []any{"out/ethanol.sdf"}, "language": "python", "environment": "synon-biomed-python",
		"human_description": "Saving validated ethanol structures",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-valid-sdf", input), fixture.identity, "save-valid-sdf", input,
	)
	if err != nil || result["errors"] != nil {
		t.Fatalf("save valid SDF result=%#v err=%v", result, err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 {
		t.Fatalf("valid SDF artifacts=%#v", artifacts)
	}
	artifactID := stringValue(artifacts[0]["artifact_id"])
	versionID := stringValue(artifacts[0]["version_id"])
	artifact, version, reader, found, err := fixture.store.OpenArtifactVersionContent(versionID)
	if err != nil || !found {
		t.Fatalf("open valid SDF version=%q found=%t err=%v", versionID, found, err)
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || artifact.ID != artifactID || version.ArtifactID != artifactID || string(content) != validServerEthanolSDF {
		t.Fatalf("artifact=%#v version=%#v bytes=%d readErr=%v closeErr=%v", artifact, version, len(content), readErr, closeErr)
	}
	commits, err := fixture.repo.ListArtifactCommitReferences(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, fixture.claim.Attempt,
	)
	if err != nil || len(commits) != 1 || commits[0].ArtifactID != artifactID || commits[0].VersionID != versionID ||
		commits[0].Relation != transcriptstore.ArtifactRelationProduced {
		t.Fatalf("valid SDF commits=%#v err=%v", commits, err)
	}
}

func TestAgentSaveArtifactsRejectsMalformedSDFWithoutCanonicalWrites(t *testing.T) {
	fixture := newAgentSaveArtifactsFixtureWithKernelManager(t, realManagedScientificKernelManagerForServerTest(t))
	malformed := strings.Replace(validServerEthanolSDF, "\n  3  2  0", "\nextra header\n  3  2  0", 1)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/malformed.sdf", malformed)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-invalid-sdf", 1, write)
	input := map[string]any{
		"files": []any{"out/malformed.sdf"}, "language": "python", "environment": "synon-biomed-python",
		"human_description": "Saving malformed structures",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-invalid-sdf", input), fixture.identity, "save-invalid-sdf", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
		t.Fatalf("malformed SDF result=%#v err=%v", result, err)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 {
		t.Fatalf("malformed SDF failures=%#v", result["errors"])
	}
	assertAgentSaveArtifactFailure(t, failures[0], "out/malformed.sdf", "invalid_scientific_artifact")
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
}

func TestAgentSaveArtifactsAcceptsValidSMILESThroughInstalledRDKitRuntime(t *testing.T) {
	fixture := newAgentSaveArtifactsFixtureWithKernelManager(t, realManagedScientificKernelManagerForServerTest(t))
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/ethanol.smi", validServerEthanolSMILES)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-valid-smiles", 1, write)
	input := map[string]any{
		"files": []any{"out/ethanol.smi"}, "language": "python", "environment": "synon-biomed-python",
		"human_description": "Saving validated ethanol SMILES",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-valid-smiles", input), fixture.identity, "save-valid-smiles", input,
	)
	if err != nil || result["errors"] != nil || len(agentSaveArtifactResults(t, result)) != 1 {
		t.Fatalf("save valid SMILES result=%#v err=%v", result, err)
	}
}

func TestAgentSaveArtifactsRejectsHeaderOnlySMILESWithoutCanonicalWrites(t *testing.T) {
	fixture := newAgentSaveArtifactsFixtureWithKernelManager(t, realManagedScientificKernelManagerForServerTest(t))
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/empty.smi", "candidate_id\tcanonical_smiles\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-invalid-smiles", 1, write)
	input := map[string]any{
		"files": []any{"out/empty.smi"}, "language": "python", "environment": "synon-biomed-python",
		"human_description": "Saving incomplete SMILES",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-invalid-smiles", input), fixture.identity, "save-invalid-smiles", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
		t.Fatalf("header-only SMILES result=%#v err=%v", result, err)
	}
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
}

func TestAgentSaveArtifactsFailsClosedWhenScientificValidatorIsUnavailable(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/ethanol.sdf", validServerEthanolSDF)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-unavailable-sdf", 1, write)
	input := map[string]any{
		"files": []any{"out/ethanol.sdf"}, "language": "python", "environment": "synon-biomed-python",
		"human_description": "Saving structures without a validator",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-unavailable-sdf", input), fixture.identity, "save-unavailable-sdf", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
		t.Fatalf("unavailable validator result=%#v err=%v", result, err)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 || failures[0]["path"] != "out/ethanol.sdf" || failures[0]["code"] != "scientific_validator_unavailable" {
		t.Fatalf("unavailable validator failures=%#v", result["errors"])
	}
	assertAgentSaveArtifactsNoCanonicalWrites(t, fixture)
}

func TestAgentSaveArtifactsRejectsSiblingFrameExternalWrite(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if _, err := fixture.store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-sibling", ProjectID: "project-save", ParentFrameID: "frame-save",
		AgentName: "ANALYST", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	siblingAccess, found, err := fixture.store.GetKernelFrameAccessContext(context.Background(), "frame-sibling")
	if err != nil || !found {
		t.Fatalf("sibling access found=%t err=%v", found, err)
	}
	externalRoot := filepath.Join(t.TempDir(), "external-grant")
	externalWrite := writeAgentSaveArtifactsFile(t, externalRoot, "out/sibling.csv", "secret sibling output")
	externalWrite.Path = filepath.Join(externalRoot, filepath.FromSlash(externalWrite.Path))
	if _, err := fixture.server.upsertHostGrant("owner-save", externalRoot, "read_write"); err != nil {
		t.Fatal(err)
	}
	fixture.saveExecution(t, siblingAccess, fixture.projectPath, "exec-sibling", 1, externalWrite)
	input := map[string]any{
		"files": []any{"out/sibling.csv"}, "language": "python", "environment": "scanpy",
		"human_description": "Saving sibling output",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-call-sibling", input), fixture.identity, "save-call-sibling", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
		t.Fatalf("sibling result=%#v err=%v", result, err)
	}
	errorsFound := agentSaveArtifactFailures(t, result)
	if len(errorsFound) != 1 || errorsFound[0]["path"] != "out/sibling.csv" || errorsFound[0]["code"] != "file_not_found" {
		t.Fatalf("sibling errors=%#v", result["errors"])
	}
	artifacts, err := fixture.store.ListArtifacts("project-save", 100, 0)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("sibling save created artifacts=%#v err=%v", artifacts, err)
	}
}

func TestAgentSaveArtifactsRejectsCrossProjectVersionReference(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	if _, err := fixture.store.CreateProject(workspace.CreateProjectInput{
		ID: "project-foreign", UserID: "owner-save", Name: "Foreign project",
	}); err != nil {
		t.Fatal(err)
	}
	foreign, _, err := fixture.store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "11111111-1111-4111-8111-111111111111", ProjectID: "project-foreign", Name: "foreign.txt",
		Kind: "text/plain", Content: []byte("foreign"), CreatedBy: "owner-save",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.txt", "local")
	input := map[string]any{
		"files": []any{"out/report.txt"}, "language": "text",
		"version_of":        map[string]any{"out/report.txt": foreign.ID},
		"human_description": "Updating foreign report",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-call-foreign", input), fixture.identity, "save-call-foreign", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
		t.Fatalf("foreign result=%#v err=%v", result, err)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 || failures[0]["path"] != "out/report.txt" || failures[0]["code"] != "invalid_version_reference" {
		t.Fatalf("foreign failures=%#v", failures)
	}
	artifacts, err := fixture.store.ListArtifacts("project-save", 100, 0)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("local artifacts=%#v err=%v", artifacts, err)
	}
}

func TestAgentSaveArtifactsRejectsDigestVersionReferenceBeforeAnyBatchWrite(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.txt", "corrected report")
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/dashboard.txt", "new dashboard")
	input := map[string]any{
		"files":             []any{"out/report.txt", "out/dashboard.txt"},
		"language":          "text",
		"version_of":        map[string]any{"out/report.txt": strings.Repeat("a", 64)},
		"human_description": "Saving corrected deliverables",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-call-digest-version", input), fixture.identity, "save-call-digest-version", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || agentSaveArtifactResults(t, result)[0]["input_path"] != "out/dashboard.txt" {
		t.Fatalf("digest version reference result=%#v err=%v", result, err)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 || failures[0]["path"] != "out/report.txt" || failures[0]["code"] != "invalid_version_reference" {
		t.Fatalf("digest failures=%#v", failures)
	}
}

func TestAgentSaveArtifactsResolvesRelativeExecutionLogWriteInsideWorkspace(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	// The file is written by a kernel tool and recorded as a workspace-relative
	// path (the real log shape), not an absolute host path. The fallback must
	// join it to the authorized workspace instead of discarding it.
	reportWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/relative.md", "# Relative\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-relative", 1, reportWrite)
	input := map[string]any{
		"files": []any{"out/relative.md"}, "language": "python", "environment": "scanpy",
		"human_description": "Saving relative-path output",
	}
	ctx := fixture.toolContext(t, "save-call-relative", input)
	result, err := fixture.server.executeAgentSaveArtifacts(ctx, fixture.identity, "save-call-relative", input)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || result["errors"] != nil {
		t.Fatalf("result=%#v", result)
	}
}

func TestAgentSaveArtifactsReturnsRecoverablePartialFailuresWithoutLeakingAbsolutePaths(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/valid.txt", "durable output")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-recoverable-path", 1, write)
	absPath := filepath.Join(fixture.projectPath, "private", "secret.txt")
	input := map[string]any{
		"files": []any{"out/valid.txt", absPath}, "language": "text",
		"human_description": "Saving outputs with a corrected path",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-recoverable-path", input), fixture.identity, "save-recoverable-path", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 {
		t.Fatalf("failures=%#v", failures)
	}
	assertAgentSaveArtifactFailure(t, failures[0], "", "path_must_be_relative")
	if encoded, marshalErr := json.Marshal(result); marshalErr != nil || strings.Contains(string(encoded), absPath) {
		t.Fatalf("absolute path leaked encoded=%q err=%v", encoded, marshalErr)
	}
}

func TestAgentSaveArtifactsReturnsRecoverableVersionFailureAndSavesOtherPaths(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	report := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.txt", "corrected report")
	dashboard := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/dashboard.txt", "new dashboard")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-recoverable-version", 1, report, dashboard)
	input := map[string]any{
		"files": []any{"out/report.txt", "out/dashboard.txt"}, "language": "text",
		"version_of":        map[string]any{"out/report.txt": "11111111-1111-4111-8111-111111111111"},
		"human_description": "Saving corrected deliverables",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-recoverable-version", input), fixture.identity, "save-recoverable-version", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || agentSaveArtifactResults(t, result)[0]["input_path"] != "out/dashboard.txt" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	failures := agentSaveArtifactFailures(t, result)
	if len(failures) != 1 {
		t.Fatalf("failures=%#v", failures)
	}
	assertAgentSaveArtifactFailure(t, failures[0], "out/report.txt", "invalid_version_reference")
	recovery := failures[0]["recovery"].(map[string]any)
	if recovery["version_of"] != "omit_for_same_path" {
		t.Fatalf("version recovery=%#v", recovery)
	}
}

func TestAgentSaveArtifactsReturnsStructuredEvidenceWarningWithDraftArtifact(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := "# Report\nUnsupported PMID 24893891.\n"
	write := writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/report.md", content)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-recoverable-evidence", 1, write)
	input := map[string]any{
		"files": []any{"out/report.md"}, "language": "text", "human_description": "Saving evidence report",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-recoverable-evidence", input), fixture.identity, "save-recoverable-evidence", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	warnings := anySliceValue(result["warnings"])
	if len(warnings) != 1 {
		t.Fatalf("warnings=%#v", warnings)
	}
	warning := mapValue(warnings[0])
	assertAgentSaveArtifactFailure(t, warning, "out/report.md", "evidence_binding_advisory")
	references, ok := warning["unsupported_evidence_references"].([]string)
	if !ok || len(references) != 1 || references[0] != "pmid:24893891" {
		t.Fatalf("evidence references=%#v", warning)
	}
	recovery := warning["recovery"].(map[string]any)
	if recovery["evidence"] != "retain_real_source_identities_and_distinguish_source_existence_from_claim_support" ||
		boolValue(warning["blocking"], true) || !boolValue(warning["quality_advisory"], false) ||
		strings.Contains(stringValue(recovery["action"]), content) {
		t.Fatalf("evidence recovery=%#v", recovery)
	}
}

func TestAgentSaveArtifactsEvidenceRecoveryListsOnlyVerifiedStructuredReferences(t *testing.T) {
	const verifiedPMID = "33971321"
	const unsupportedPMID = "24893891"
	endpoint := "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/efetch.fcgi?db=pubmed&id=" + verifiedPMID + "&retmode=xml"
	arguments, err := json.Marshal(map[string]any{"url": endpoint})
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(map[string]any{
		"ok": true,
		"result": map[string]any{
			"code": 200, "url": endpoint,
			"result": `<PubmedArticleSet><PubmedArticle><MedlineCitation><PMID Version="1">` + verifiedPMID + `</PMID></MedlineCitation></PubmedArticle></PubmedArticleSet>`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "verified-pubmed", Name: "WebFetch", Arguments: arguments}}},
		{Role: "tool", ToolCallID: "verified-pubmed", Content: string(result)},
	}
	snapshot, err := os.CreateTemp(t.TempDir(), "evidence-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if _, err := snapshot.WriteString("Unsupported PMID " + unsupportedPMID + ".\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	evidenceErr := (&Server{}).validateAgentSavedArtifactEvidence(snapshot, messages)
	var typed *agentSavedArtifactEvidenceError
	if !errors.As(evidenceErr, &typed) {
		t.Fatalf("evidence error = %v", evidenceErr)
	}
	if !reflect.DeepEqual(typed.References, []string{"pmid:" + unsupportedPMID}) ||
		!reflect.DeepEqual(typed.AvailableReferences, []string{"pmid:" + verifiedPMID}) || typed.AvailableTotal != 1 {
		t.Fatalf("evidence recovery = %#v", typed)
	}
	failure := agentSaveArtifactFailure("out/report.md", typed)
	if !reflect.DeepEqual(failure["available_evidence_references"], []string{"pmid:" + verifiedPMID}) ||
		failure["available_evidence_reference_count"] != 1 {
		t.Fatalf("structured recovery = %#v", failure)
	}
	if encoded, err := json.Marshal(failure); err != nil || strings.Contains(string(encoded), endpoint) {
		t.Fatalf("recovery leaked source URL: %s err=%v", encoded, err)
	}
}

func TestAgentSaveArtifactEvidenceIgnoresIdentifiersInsideEmbeddedDataURIs(t *testing.T) {
	snapshot, err := os.CreateTemp(t.TempDir(), "embedded-data-uri-*.html")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if _, err := snapshot.WriteString(`<html><body><img src="data:image/png;base64,AAAAArs123456AAA"></body></html>`); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	if err := (&Server{}).validateAgentSavedArtifactEvidence(snapshot, nil); err != nil {
		t.Fatalf("embedded data URI was treated as evidence text: %v", err)
	}
}

func TestAgentSaveArtifactEvidenceStillRejectsIdentifiersOutsideEmbeddedDataURIs(t *testing.T) {
	snapshot, err := os.CreateTemp(t.TempDir(), "embedded-data-uri-with-reference-*.html")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	content := `<html><body><img src="data:image/png;base64,AAAAArs123456AAA">Unsupported PMID 24893891.</body></html>`
	if _, err := snapshot.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	evidenceErr := (&Server{}).validateAgentSavedArtifactEvidence(snapshot, nil)
	var typed *agentSavedArtifactEvidenceError
	if !errors.As(evidenceErr, &typed) {
		t.Fatalf("evidence error = %v", evidenceErr)
	}
	if !reflect.DeepEqual(typed.References, []string{"pmid:24893891"}) {
		t.Fatalf("evidence references = %#v", typed.References)
	}
}

func TestAgentSaveArtifactEvidenceDefersUngroundedLocalizedLedgerBeforeCompletion(t *testing.T) {
	snapshot, err := os.CreateTemp(t.TempDir(), "localized-ledger-*.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	ledger := "证据类别,来源,关键结论,标识\n" +
		"临床管线,企业官网,企业页面声称该候选已经进入一期临床并正在招募目标患者人群,CompanyName\n"
	if _, err := snapshot.WriteString(ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	evidenceErr := (&Server{}).validateAgentSavedArtifactEvidenceForPath("研发证据表.csv", snapshot, nil)
	var typed *agentSavedArtifactEvidenceError
	if !errors.As(evidenceErr, &typed) {
		t.Fatalf("localized ledger evidence error=%v", evidenceErr)
	}
	if len(typed.References) != 2 ||
		!slices.Contains(typed.References, "evidence_source_locator_missing:研发证据表.csv row=2 source_type=evidence") ||
		!slices.Contains(typed.References, "source_ledger_source_not_in_durable_receipts:研发证据表.csv row=2 source=CompanyName") {
		t.Fatalf("localized ledger pre-publication findings=%#v", typed.References)
	}
}

func TestAgentSavedArtifactEvidencePolicyTreatsScientificMachineFormatsAsData(t *testing.T) {
	for _, path := range []string{
		"structure.cif", "structure.mmcif", "complex.pdb", "poses.pdbqt",
		"candidates.sdf", "candidate.mol", "candidate.mol2", "candidates.smi",
	} {
		t.Run(filepath.Ext(path), func(t *testing.T) {
			if policy := agentSavedArtifactEvidencePolicyFor("text/plain", path); policy != agentSavedArtifactEvidenceNone {
				t.Fatalf("policy for %s = %v", path, policy)
			}
		})
	}
	if policy := agentSavedArtifactEvidencePolicyFor("text/markdown", "report.md"); policy != agentSavedArtifactEvidenceCitations {
		t.Fatalf("narrative report policy = %v", policy)
	}
}

func TestCollectVerifiedSessionRunnerReferencesInfersGenericExactAccession(t *testing.T) {
	references := map[string]struct{}{}
	visited := 0
	collectVerifiedSessionRunnerReferences(references, map[string]any{
		"status":     "success",
		"components": []any{map[string]any{"accession": "Q00987"}},
	}, 0, &visited, false)
	if _, found := references["accession:uniprot:Q00987"]; !found {
		t.Fatalf("generic verified UniProt accession was not retained: %#v", references)
	}
}

func TestAgentSaveArtifactsTreatsTildePathAsInvalidRelativeSyntax(t *testing.T) {
	request, err := parseAgentSaveArtifactsRequest(map[string]any{
		"files": []any{"~/analysis/report.md"}, "language": "text", "human_description": "Saving report",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Files) != 0 || len(request.Failures) != 1 || request.Failures[0]["code"] != "path_must_be_relative" {
		t.Fatalf("tilde path request = %#v", request)
	}
}

func TestAgentSaveArtifactsExpandsDestinationDirectoryOnlyAcrossExplicitFiles(t *testing.T) {
	request, err := parseAgentSaveArtifactsRequest(map[string]any{
		"files": []any{
			"structures/grid.png",
			"structures/compound.svg",
			"report.md",
		},
		"destination": map[string]any{
			"structures/": "snapshot",
			"report.md":   "working_data",
		},
		"language":          "text",
		"human_description": "Saving verified outputs",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"structures/grid.png":     "snapshot",
		"structures/compound.svg": "snapshot",
		"report.md":               "working_data",
	}
	if !reflect.DeepEqual(request.Destination, want) {
		t.Fatalf("destination=%#v want=%#v", request.Destination, want)
	}
}

func TestAgentSaveArtifactsExactDestinationOverridesDirectoryRule(t *testing.T) {
	request, err := parseAgentSaveArtifactsRequest(map[string]any{
		"files": []any{"results/report.md", "results/audit.json"},
		"destination": map[string]any{
			"results":           "working_data",
			"results/report.md": "snapshot",
		},
		"language":          "text",
		"human_description": "Saving report and audit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Destination["results/report.md"] != "snapshot" ||
		request.Destination["results/audit.json"] != "working_data" {
		t.Fatalf("destination=%#v", request.Destination)
	}
}

func TestAgentSaveArtifactsDestinationAddsExplicitOmittedFiles(t *testing.T) {
	request, err := parseAgentSaveArtifactsRequest(map[string]any{
		"files": []any{"results/report.md"},
		"destination": map[string]any{
			"results/report.md":         "snapshot",
			"structures/ANALOG-001.png": "snapshot",
			"structures/ANALOG-002.png": "snapshot",
		},
		"language":          "text",
		"human_description": "Saving report and molecule figures",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := []string{
		"results/report.md",
		"structures/ANALOG-001.png",
		"structures/ANALOG-002.png",
	}
	if !reflect.DeepEqual(request.Files, wantFiles) {
		t.Fatalf("files=%#v want=%#v", request.Files, wantFiles)
	}
	for _, path := range wantFiles {
		if request.Destination[path] != "snapshot" {
			t.Fatalf("destination[%q]=%q", path, request.Destination[path])
		}
	}
}

func TestAgentSaveArtifactsDestinationIgnoresNonFileClassifierKeys(t *testing.T) {
	request, err := parseAgentSaveArtifactsRequest(map[string]any{
		"files": []any{"report.md", "evidence.csv"},
		"destination": map[string]any{
			"report.md":    "snapshot",
			"evidence.csv": "snapshot",
			"snapshot":     "snapshot",
			"working_data": "working_data",
		},
		"language":          "text",
		"human_description": "Saving requested outputs",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.Files, []string{"report.md", "evidence.csv"}) {
		t.Fatalf("non-file destination keys became files=%#v", request.Files)
	}
	if !reflect.DeepEqual(request.Destination, map[string]string{
		"report.md": "snapshot", "evidence.csv": "snapshot",
	}) {
		t.Fatalf("destination=%#v", request.Destination)
	}
}

func TestAgentSaveArtifactsPublishesExplicitDestinationFileOmittedFromFiles(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "results/report.md", "# Verified report\n")
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "structures/ANALOG-001.png", "png-bytes")
	input := map[string]any{
		"files": []any{"results/report.md"},
		"destination": map[string]any{
			"results/report.md":         "snapshot",
			"structures/ANALOG-001.png": "snapshot",
		},
		"language":          "text",
		"human_description": "Saving report and molecule figure",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-destination-union", input), fixture.identity,
		"save-destination-union", input,
	)
	if err != nil || len(agentSaveArtifactResults(t, result)) != 2 || result["errors"] != nil {
		t.Fatalf("destination-union result=%#v err=%v", result, err)
	}
}

func TestAgentSaveArtifactsRejectsSymlinkEscapeAndDoesNotRequireCompatibilityProjection(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	externalPath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(externalPath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(fixture.projectPath, "out", "escape.txt")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalPath, linkPath); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"files": []any{"out/escape.txt"}, "language": "text", "human_description": "Saving escaped output",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-call-escape", input), fixture.identity, "save-call-escape", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
		t.Fatalf("escape result=%#v err=%v", result, err)
	}
	errorsFound := agentSaveArtifactFailures(t, result)
	if len(errorsFound) != 1 || errorsFound[0]["path"] != "out/escape.txt" || errorsFound[0]["code"] != "path_not_authorized" {
		t.Fatalf("escape errors=%#v", result["errors"])
	}

	writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/no-projection.txt", "canonical")
	input = map[string]any{
		"files": []any{"out/no-projection.txt"}, "language": "text", "human_description": "Saving canonical output",
	}
	projectionStore := fixture.server.runtimeStore
	fixture.server.runtimeStore = runtimekv.New("")
	result, err = fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-call-no-projection", input), fixture.identity, "save-call-no-projection", input,
	)
	fixture.server.runtimeStore = projectionStore
	if err != nil || len(agentSaveArtifactResults(t, result)) != 1 || result["errors"] != nil {
		t.Fatalf("no projection result=%#v err=%v", result, err)
	}
}

func TestAgentSaveArtifactsPinnedRootRejectsIntermediateDirectorySwap(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/race.txt", "authorized")
	source, err := fixture.server.resolveAgentSavedArtifactSource(
		fixture.identity.access, fixture.projectPath, "out/race.txt", nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	originalDirectory := filepath.Join(fixture.projectPath, "out")
	movedDirectory := filepath.Join(fixture.projectPath, "out-original")
	if err := os.Rename(originalDirectory, movedDirectory); err != nil {
		source.close()
		t.Fatal(err)
	}
	externalDirectory := filepath.Join(t.TempDir(), "external")
	if err := os.MkdirAll(externalDirectory, 0o700); err != nil {
		source.close()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalDirectory, "race.txt"), []byte("external secret"), 0o600); err != nil {
		source.close()
		t.Fatal(err)
	}
	if err := os.Symlink(externalDirectory, originalDirectory); err != nil {
		source.close()
		t.Fatal(err)
	}
	snapshot, _, _, snapshotErr := snapshotAgentSavedArtifact(context.Background(), source)
	source.close()
	if snapshot != nil {
		path := snapshot.Name()
		_ = snapshot.Close()
		_ = os.Remove(path)
	}
	if !errors.Is(snapshotErr, errAgentSavedArtifactPathUnauthorized) {
		t.Fatalf("intermediate swap error=%v", snapshotErr)
	}
	artifacts, err := fixture.store.ListArtifacts("project-save", 100, 0)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("intermediate swap artifacts=%#v err=%v", artifacts, err)
	}
}

func TestAgentSaveArtifactsCancelledSnapshotDoesNotLeaveTemporaryContent(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	writeAgentSaveArtifactsFile(t, fixture.projectPath, "out/eight.bin", "12345678")
	source, err := fixture.server.resolveAgentSavedArtifactSource(
		fixture.identity.access, fixture.projectPath, "out/eight.bin", nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshotDirectory := t.TempDir()
	t.Setenv("TMPDIR", snapshotDirectory)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, _, _, snapshotErr := snapshotAgentSavedArtifact(ctx, source)
	source.close()
	if snapshot != nil {
		path := snapshot.Name()
		_ = snapshot.Close()
		_ = os.Remove(path)
	}
	if !errors.Is(snapshotErr, context.Canceled) {
		t.Fatalf("snapshot error=%v", snapshotErr)
	}
	entries, err := os.ReadDir(snapshotDirectory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled snapshot residue=%v err=%v", entries, err)
	}
}

func TestAgentSaveArtifactsSchemaAndRuntimeReadiness(t *testing.T) {
	manager := kernelruntime.NewManager(kernelruntime.Config{Python: filepath.Join(t.TempDir(), "missing-python")})
	app := &Server{kernelManager: manager}
	schemas := app.agentKernelToolSchemas(&agentKernelContext{}, map[string]struct{}{
		"python": {}, "r": {}, "repl": {}, "save_artifacts": {},
	})
	byName := map[string]agentruntime.ToolSchema{}
	for _, schema := range schemas {
		byName[schema.Name] = schema
	}
	if byName["python"].Name != "" || byName["r"].Name != "" || byName["repl"].Name != "" {
		t.Fatalf("unready execution tools=%#v", byName)
	}
	save, ok := byName["save_artifacts"]
	if !ok || byName["wait_for_notification"].Name == "" {
		t.Fatalf("non-execution schemas=%#v", byName)
	}
	for _, marker := range []string{
		"destination=snapshot", "working_data", "hidden until terminal validation",
		"unchanged bytes reuse", "exact immutable links", "non-blocking quality advisories",
		"never create completion_pending", runnerArtifactTableContractSchema,
	} {
		if !strings.Contains(save.Description, marker) {
			t.Fatalf("save description missing %q: %q", marker, save.Description)
		}
	}
	required, _ := save.Parameters["required"].([]string)
	if !sameStringSet(required, []string{"files", "language", "human_description"}) {
		t.Fatalf("save required=%#v", required)
	}
	properties, _ := save.Parameters["properties"].(map[string]any)
	language, _ := properties["language"].(map[string]any)
	environment, _ := properties["environment"].(map[string]any)
	if language["minLength"] != 1 || language["maxLength"] != 100 || language["pattern"] != `^[A-Za-z0-9][A-Za-z0-9._-]*$` {
		t.Fatalf("language identifier schema=%#v", language)
	}
	if environment["minLength"] != 0 || environment["maxLength"] != 100 || environment["pattern"] != `^(?:|[A-Za-z0-9][A-Za-z0-9._-]*)$` {
		t.Fatalf("environment identifier schema=%#v", environment)
	}
	if !strings.Contains(stringValue(language["description"]), "not a MIME type") ||
		!strings.Contains(stringValue(language["description"]), "mixed file formats") {
		t.Fatalf("language description=%q", language["description"])
	}
	validator := compileAgentRuntimeMCPValidator(save)
	if result := validator.Validate(map[string]any{
		"files": []any{"report.md", "table.csv"}, "language": "markdown/csv",
		"human_description": "Saving report and table",
	}); result == nil || result["code"] != "invalid_tool_arguments" {
		t.Fatalf("save schema admitted path-like language: %#v", result)
	}
	checkpoints, _ := properties["checkpoints"].(map[string]any)
	if checkpoints["maxItems"] != maxAgentSavedArtifacts || checkpoints["uniqueItems"] != true {
		t.Fatalf("checkpoint schema=%#v", checkpoints)
	}
	withoutSave := app.agentKernelToolSchemas(&agentKernelContext{}, map[string]struct{}{"python": {}})
	for _, schema := range withoutSave {
		if schema.Name == "save_artifacts" {
			t.Fatalf("save_artifacts ignored agent tool policy: %#v", withoutSave)
		}
	}
}

func TestAgentSaveArtifactsNormalizesEmptyOptionalEnvironment(t *testing.T) {
	schema := agentSaveArtifactsToolSchema()
	input := map[string]any{
		"files": []any{"report.md"}, "language": "python", "environment": "",
		"human_description": "Saving scientific report",
	}
	if result := compileAgentRuntimeMCPValidator(schema).Validate(input); result != nil {
		t.Fatalf("empty optional environment rejected by schema: %#v", result)
	}
	request, err := parseAgentSaveArtifactsRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if request.Environment != "" {
		t.Fatalf("environment=%q", request.Environment)
	}
}

func TestAgentSaveArtifactsRejectsInvalidLargeFileContent(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	path := filepath.Join(fixture.projectPath, "out", "corrupt-large.cif")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(321003928); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("%PDF-1.7\n"), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"files": []any{"out/corrupt-large.cif"}, "language": "python", "environment": "scanpy",
		"human_description": "Saving processed dataset",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-call-large-regular", input), fixture.identity, "save-call-large-regular", input,
	)
	if !errors.Is(err, errAgentSaveArtifactsNoResults) || len(agentSaveArtifactResults(t, result)) != 0 {
		t.Fatalf("regular large result=%#v err=%v", result, err)
	}
	errorsFound := agentSaveArtifactFailures(t, result)
	if len(errorsFound) != 1 || errorsFound[0]["path"] != "out/corrupt-large.cif" || errorsFound[0]["code"] != "file_content_type_mismatch" {
		t.Fatalf("regular large errors=%#v", result["errors"])
	}
}

func TestAgentSaveArtifactsObservedLargeCheckpoint(t *testing.T) {
	if os.Getenv("SYNON_TEST_LARGE_ARTIFACT") != "1" {
		t.Skip("set SYNON_TEST_LARGE_ARTIFACT=1 for the 321,003,928-byte checkpoint persistence test")
	}
	fixture := newAgentSaveArtifactsFixture(t)
	path := filepath.Join(fixture.projectPath, "out", "intermediate-checkpoint.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(321003928); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"files": []any{"out/intermediate-checkpoint.bin"}, "language": "python", "environment": "scanpy",
		"checkpoints": []any{"out/intermediate-checkpoint.bin"}, "human_description": "Saving processed checkpoint",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-call-large", input), fixture.identity, "save-call-large", input,
	)
	artifacts := agentSaveArtifactResults(t, result)
	if err != nil || len(artifacts) != 1 || artifacts[0]["size_bytes"] != int64(321003928) || artifacts[0]["is_checkpoint"] != true {
		t.Fatalf("large checkpoint result=%#v err=%v", result, err)
	}
	versionID := stringValue(artifacts[0]["version_id"])
	var checkpoint, executionLinks int
	var language, environment string
	if err := fixture.db.QueryRow(`SELECT language,environment_snapshot,is_checkpoint
		FROM artifact_version_provenance WHERE version_id=?`, versionID).Scan(&language, &environment, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM artifact_version_execution_links WHERE version_id=?`, versionID).Scan(&executionLinks); err != nil {
		t.Fatal(err)
	}
	if language != "python" || !strings.Contains(environment, `"environment":"scanpy"`) || checkpoint != 1 || executionLinks != 0 {
		t.Fatalf("large provenance language=%q environment=%q checkpoint=%d links=%d", language, environment, checkpoint, executionLinks)
	}
	reopened, err := workspace.Open(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, reopenedVersion, reader, found, err := reopened.OpenArtifactVersionContent(versionID)
	if err != nil || !found {
		t.Fatalf("reopen large version found=%t err=%v", found, err)
	}
	hasher := sha256.New()
	readBytes, readErr := io.Copy(hasher, reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || readBytes != 321003928 ||
		hex.EncodeToString(hasher.Sum(nil)) != reopenedVersion.ContentSHA256 || reopenedVersion.ContentSHA256 != stringValue(artifacts[0]["checksum"]) {
		t.Fatalf("reopened large bytes=%d digest=%q want=%q readErr=%v closeErr=%v", readBytes, hex.EncodeToString(hasher.Sum(nil)), reopenedVersion.ContentSHA256, readErr, closeErr)
	}
}
