package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/tools/rcsbfiles"
)

type agentRCSBFetcher struct {
	calls   []rcsbfiles.Input
	body    string
	reader  io.ReadCloser
	err     error
	onFetch func()
}

type formatFallbackRCSBFetcher struct {
	calls []rcsbfiles.Input
	body  string
}

func (f *formatFallbackRCSBFetcher) Fetch(_ context.Context, input rcsbfiles.Input) (rcsbfiles.Result, error) {
	f.calls = append(f.calls, input)
	if input.Format == rcsbfiles.FormatPDB {
		return rcsbfiles.Result{}, rcsbfiles.ErrFileNotFound
	}
	identifier := strings.ToUpper(strings.TrimSpace(input.EntryID))
	return rcsbfiles.Result{
		Body: io.NopCloser(strings.NewReader(f.body)), ResourceKind: input.ResourceKind,
		Identifier: identifier, Format: input.Format, Filename: identifier + ".cif", ContentType: "chemical/x-cif",
	}, nil
}

func (f *agentRCSBFetcher) Fetch(_ context.Context, input rcsbfiles.Input) (rcsbfiles.Result, error) {
	f.calls = append(f.calls, input)
	if f.onFetch != nil {
		f.onFetch()
	}
	if f.err != nil {
		return rcsbfiles.Result{}, f.err
	}
	identifier := strings.ToUpper(strings.TrimSpace(input.EntryID))
	if identifier == "" {
		identifier = strings.ToUpper(strings.TrimSpace(input.ComponentID))
	}
	filename := strings.TrimSpace(input.Filename)
	if filename == "" {
		filename = identifier + "." + string(input.Format)
	}
	body := f.reader
	if body == nil {
		body = io.NopCloser(strings.NewReader(f.body))
	}
	return rcsbfiles.Result{
		Body: body, ResourceKind: input.ResourceKind,
		Identifier: identifier, Format: input.Format, Filename: filename, ContentType: "chemical/x-pdb",
	}, nil
}

type failingRCSBReader struct{ read bool }

func (r *failingRCSBReader) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, errors.New("controlled body failure")
	}
	r.read = true
	return copy(buffer, "partial"), nil
}

func (r *failingRCSBReader) Close() error { return nil }

func TestAgentRCSBDownloadPublishesOneAuthoritativeArtifactAndReplaysIdempotently(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := "HEADER    HUMAN CRBN                                          4TZ4\nATOM      1  N   GLY A   1      11.104  13.207   8.000  1.00 20.00           N  \n"
	fetcher := &agentRCSBFetcher{body: content}
	fixture.server.rcsbFiles = fetcher
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb",
		"human_description": "Downloading validated CRBN coordinates",
	}
	ctx := fixture.toolContext(t, "rcsb-call-1", input)
	first, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "rcsb-call-1", input)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, first)
	if len(artifacts) != 1 || artifacts[0]["input_path"] != "4TZ4.pdb" {
		t.Fatalf("first result = %#v", first)
	}
	assertAgentArtifactResultLinks(t, artifacts[0])
	stored, err := os.ReadFile(filepath.Join(fixture.projectPath, "4TZ4.pdb"))
	if err != nil || string(stored) != content {
		t.Fatalf("workspace content=%q err=%v", stored, err)
	}
	if err := os.Remove(filepath.Join(fixture.projectPath, "4TZ4.pdb")); err != nil {
		t.Fatal(err)
	}
	fetcher.err = errors.New("upstream unavailable during replay")
	second, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "rcsb-call-1", input)
	if err != nil {
		t.Fatal(err)
	}
	secondArtifacts := agentSaveArtifactResults(t, second)
	if len(secondArtifacts) != 1 || secondArtifacts[0]["version_id"] != artifacts[0]["version_id"] {
		t.Fatalf("idempotent replay first=%#v second=%#v", first, second)
	}
	var versions, refs int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE artifact_id=?`, artifacts[0]["artifact_id"]).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND relation='produced'`, fixture.stream.UID, fixture.claim.Attempt).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	restored, restoreErr := os.ReadFile(filepath.Join(fixture.projectPath, "4TZ4.pdb"))
	if restoreErr != nil || string(restored) != content {
		t.Fatalf("replayed workspace content=%q err=%v", restored, restoreErr)
	}
	if versions != 1 || refs != 1 || len(fetcher.calls) != 1 {
		t.Fatalf("versions=%d refs=%d calls=%d", versions, refs, len(fetcher.calls))
	}
	conflictingInput := map[string]any{
		"resource_kind": "entry", "entry_id": "5ABC", "format": "pdb", "filename": "4TZ4.pdb",
		"human_description": "Downloading different coordinates",
	}
	if _, err := fixture.server.executeAgentRCSBFileDownload(
		ctx, fixture.identity, "rcsb-call-1", conflictingInput,
	); !errors.Is(err, errAgentRCSBConflict) {
		t.Fatalf("same source event conflict error = %v", err)
	}
	if len(fetcher.calls) != 1 {
		t.Fatalf("same source event conflict reached network: calls=%d", len(fetcher.calls))
	}
}

func TestNormalizeAgentRCSBOptionalIdentifierDropsModelPlaceholders(t *testing.T) {
	for _, placeholder := range []string{"None", " null ", "NIL", "n/a", ""} {
		if got := normalizeAgentRCSBOptionalIdentifier(placeholder); got != "" {
			t.Fatalf("normalizeAgentRCSBOptionalIdentifier(%q) = %q", placeholder, got)
		}
	}
	if got := normalizeAgentRCSBOptionalIdentifier(" 9CUO "); got != "9CUO" {
		t.Fatalf("valid identifier = %q", got)
	}
}

func TestAgentRCSBDownloadFallsBackFromUnavailablePDBToCIF(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := "data_9CUO\n_entry.id 9CUO\n"
	fetcher := &formatFallbackRCSBFetcher{body: content}
	fixture.server.rcsbFiles = fetcher
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "9CUO", "format": "pdb",
		"human_description": "Downloading current CRBN coordinates",
	}
	result, err := fixture.server.executeAgentRCSBFileDownload(
		fixture.toolContext(t, "rcsb-format-fallback", input), fixture.identity, "rcsb-format-fallback", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || artifacts[0]["input_path"] != "9CUO.cif" {
		t.Fatalf("fallback result = %#v", result)
	}
	if len(fetcher.calls) != 2 || fetcher.calls[0].Format != rcsbfiles.FormatPDB || fetcher.calls[1].Format != rcsbfiles.FormatCIF {
		t.Fatalf("fallback calls = %#v", fetcher.calls)
	}
	stored, readErr := os.ReadFile(filepath.Join(fixture.projectPath, "9CUO.cif"))
	if readErr != nil || string(stored) != content {
		t.Fatalf("fallback workspace content=%q err=%v", stored, readErr)
	}
}

func TestAgentRCSBDownloadReusesIdenticalExistingWorkspaceFile(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := "data_9CUO\n_entry.id 9CUO\n"
	if err := os.WriteFile(filepath.Join(fixture.projectPath, "9CUO.cif"), []byte(content), 0o400); err != nil {
		t.Fatal(err)
	}
	fetcher := &agentRCSBFetcher{body: content}
	fixture.server.rcsbFiles = fetcher
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "9CUO", "format": "cif",
		"human_description": "Reusing current CRBN coordinates",
	}
	result, err := fixture.server.executeAgentRCSBFileDownload(
		fixture.toolContext(t, "rcsb-existing-identical", input), fixture.identity, "rcsb-existing-identical", input,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(agentSaveArtifactResults(t, result)) != 1 || len(fetcher.calls) != 1 {
		t.Fatalf("idempotent existing result=%#v calls=%#v", result, fetcher.calls)
	}
}

func TestAgentRCSBDownloadRejectsAuthorityBeforeNetwork(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentRCSBFetcher{body: "unused"}
	fixture.server.rcsbFiles = fetcher
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb", "human_description": "Downloading coordinates",
	}
	if _, err := fixture.server.executeAgentRCSBFileDownload(context.Background(), fixture.identity, "missing-source", input); !errors.Is(err, errAgentRCSBAuthority) {
		t.Fatalf("missing source error = %v", err)
	}
	tampered := fixture.claim
	tampered.ClaimToken = "stale-token"
	ctx := withTranscriptArtifactRun(context.Background(), &transcriptRunnerAuthority{Stream: fixture.stream, Claim: tampered}, 1)
	if _, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "stale-claim", input); !errors.Is(err, errAgentRCSBAuthority) {
		t.Fatalf("stale claim error = %v", err)
	}
	if len(fetcher.calls) != 0 {
		t.Fatalf("unauthorized request reached network: %#v", fetcher.calls)
	}
}

func TestAgentRCSBDownloadRemovesNewWorkspaceFileWhenClaimExpiresBeforeArtifactCommit(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	content := "HEADER    HUMAN CRBN                                          4TZ4\nATOM      1  N   GLY A   1      11.104  13.207   8.000  1.00 20.00           N  \n"
	fetcher := &agentRCSBFetcher{body: content}
	fetcher.onFetch = func() {
		result, err := fixture.repo.CancelRunner(context.Background(), transcriptstore.CancelRunnerInput{
			StreamUID: fixture.claim.StreamUID, OwnerID: fixture.claim.OwnerID,
			ExpectedAttempt: fixture.claim.Attempt, ClientMessageID: "cancel-before-artifact", ReasonCode: "test_cancel",
		})
		if err != nil || !result.Applied {
			t.Fatalf("cancel result=%#v err=%v", result, err)
		}
	}
	fixture.server.rcsbFiles = fetcher
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb", "human_description": "Downloading coordinates",
	}
	ctx := fixture.toolContext(t, "rcsb-call-stale", input)
	_, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "rcsb-call-stale", input)
	if err == nil {
		t.Fatal("expected stale artifact claim")
	}
	if _, statErr := os.Stat(filepath.Join(fixture.projectPath, "4TZ4.pdb")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("orphaned workspace file stat error = %v", statErr)
	}
	var versions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions`).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("artifact versions=%d err=%v", versions, err)
	}
}

func TestAgentRCSBDownloadRejectsChangedRemoteContentForSameWorkspaceTarget(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentRCSBFetcher{body: "first"}
	fixture.server.rcsbFiles = fetcher
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb", "human_description": "Downloading coordinates",
	}
	ctx := fixture.toolContext(t, "rcsb-call-conflict", input)
	if _, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "rcsb-call-conflict", input); err != nil {
		t.Fatal(err)
	}
	fetcher.body = "different"
	secondCtx := fixture.toolContext(t, "rcsb-call-conflict-2", input)
	if _, err := fixture.server.executeAgentRCSBFileDownload(secondCtx, fixture.identity, "rcsb-call-conflict-2", input); !errors.Is(err, errAgentRCSBConflict) {
		t.Fatalf("changed content error = %v", err)
	}
	if len(fetcher.calls) != 2 {
		t.Fatalf("changed-content comparison calls=%d, want one fetch per request", len(fetcher.calls))
	}
}

func TestPublishAgentRCSBWorkspaceFileNeverDeletesConflictingWriter(t *testing.T) {
	workspaceDir := t.TempDir()
	target := filepath.Join(workspaceDir, "4TZ4.pdb")
	if err := os.WriteFile(target, []byte("concurrent owner content"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.CreateTemp("", "rcsb-publish-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(source.Name())
	defer source.Close()
	content := []byte("download content")
	if _, err := source.Write(content); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if err := publishAgentRCSBWorkspaceFile(
		context.Background(), workspaceDir, "4TZ4.pdb", source, int64(len(content)), hex.EncodeToString(digest[:]),
	); !errors.Is(err, errAgentRCSBConflict) {
		t.Fatalf("publish conflict error = %v", err)
	}
	retained, err := os.ReadFile(target)
	if err != nil || string(retained) != "concurrent owner content" {
		t.Fatalf("conflicting writer content=%q err=%v", retained, err)
	}
}

func TestAgentRCSBDownloadSchemaAndToolPolicy(t *testing.T) {
	app := &Server{rcsbFiles: &agentRCSBFetcher{}}
	schemas := app.agentKernelToolSchemas(&agentKernelContext{}, map[string]struct{}{"download_rcsb_file": {}})
	found := false
	for _, schema := range schemas {
		if schema.Name != "download_rcsb_file" {
			continue
		}
		found = true
		required, _ := schema.Parameters["required"].([]string)
		if !sameStringSet(required, []string{"format", "human_description"}) {
			t.Fatalf("required = %#v", required)
		}
	}
	if !found {
		t.Fatalf("RCSB schema missing: %#v", schemas)
	}
	without := app.agentKernelToolSchemas(&agentKernelContext{}, map[string]struct{}{"save_artifacts": {}})
	for _, schema := range without {
		if schema.Name == "download_rcsb_file" {
			t.Fatalf("RCSB schema ignored tool policy: %#v", without)
		}
	}
}

func TestInferAgentRCSBResourceKindFromIdentifier(t *testing.T) {
	if got := inferAgentRCSBResourceKind(map[string]any{"entry_id": "8RQ8"}); got != rcsbfiles.EntryCoordinates {
		t.Fatalf("entry resource kind = %q", got)
	}
	if got := inferAgentRCSBResourceKind(map[string]any{"component_id": "A1CAV"}); got != rcsbfiles.LigandDefinition {
		t.Fatalf("component resource kind = %q", got)
	}
	if got := inferAgentRCSBResourceKind(map[string]any{"resource_kind": "ligand", "entry_id": "8RQ8"}); got != rcsbfiles.LigandDefinition {
		t.Fatalf("explicit resource kind = %q", got)
	}
}

func TestAgentRCSBDownloadRejectsMalformedInputBeforeNetwork(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentRCSBFetcher{body: "unused"}
	fixture.server.rcsbFiles = fetcher
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb",
		"human_description": "Downloading coordinates", "untrusted_extra": true,
	}
	ctx := fixture.toolContext(t, "rcsb-call-invalid", input)
	if _, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "rcsb-call-invalid", input); err == nil || err.Error() != "rcsb download input contains unsupported fields" {
		t.Fatalf("malformed input error = %v", err)
	}
	if len(fetcher.calls) != 0 {
		t.Fatalf("malformed input reached network: %#v", fetcher.calls)
	}
}

func TestAgentRCSBDownloadNormalizesWorkspaceRelativeFilename(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fixture.server.rcsbFiles = &agentRCSBFetcher{body: "normalized path body"}
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "9CUO", "format": "pdb",
		"filename": "crbn_docking/receptor/9CUO.pdb", "human_description": "Downloading coordinates",
	}
	ctx := fixture.toolContext(t, "rcsb-normalized-filename", input)
	result, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "rcsb-normalized-filename", input)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || artifacts[0]["input_path"] != "9CUO.pdb" {
		t.Fatalf("normalized result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.projectPath, "9CUO.pdb")); err != nil {
		t.Fatalf("normalized workspace file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.projectPath, "crbn_docking")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected nested path stat error = %v", err)
	}
}

func TestNormalizeAgentRCSBFilenameKeepsFlatValidatedTarget(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{name: "workspace relative", input: "crbn_docking/receptor/9CUO.pdb", want: "9CUO.pdb"},
		{name: "windows separators", input: `crbn_docking\receptor\9CUO.pdb`, want: "9CUO.pdb"},
		{name: "already flat", input: "9CUO.pdb", want: "9CUO.pdb"},
		{name: "empty", input: "  ", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeAgentRCSBFilename(test.input); got != test.want {
				t.Fatalf("normalizeAgentRCSBFilename(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestAgentRCSBDownloadBodyFailureLeavesNoWorkspaceOrArtifactState(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fixture.server.rcsbFiles = &agentRCSBFetcher{reader: &failingRCSBReader{}}
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb", "human_description": "Downloading coordinates",
	}
	ctx := fixture.toolContext(t, "rcsb-call-body-failure", input)
	if _, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "rcsb-call-body-failure", input); err == nil {
		t.Fatal("expected body read failure")
	}
	if _, err := os.Stat(filepath.Join(fixture.projectPath, "4TZ4.pdb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace file stat error = %v", err)
	}
	var versions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions`).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("artifact versions=%d err=%v", versions, err)
	}
}

func TestAgentRCSBDownloadLiveOfficialPath(t *testing.T) {
	if os.Getenv("SYNON_TEST_LIVE_RCSB") != "1" {
		t.Skip("set SYNON_TEST_LIVE_RCSB=1 for the official RCSB-to-Artifact integration check")
	}
	fixture := newAgentSaveArtifactsFixture(t)
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb",
		"human_description": "Downloading validated CRBN coordinates",
	}
	ctx := fixture.toolContext(t, "rcsb-live-4tz4", input)
	result, err := fixture.server.executeAgentRCSBFileDownload(ctx, fixture.identity, "rcsb-live-4tz4", input)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 || artifacts[0]["input_path"] != "4TZ4.pdb" {
		t.Fatalf("live result = %#v", result)
	}
	info, err := os.Stat(filepath.Join(fixture.projectPath, "4TZ4.pdb"))
	if err != nil {
		t.Fatalf("live workspace file stat error = %v", err)
	}
	if info.Size() < 128 {
		t.Fatalf("live workspace file size=%d", info.Size())
	}
	var commits int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM transcript_artifact_commits
		WHERE stream_uid=? AND runner_attempt=? AND relation='produced'`, fixture.stream.UID, fixture.claim.Attempt).Scan(&commits); err != nil || commits != 1 {
		t.Fatalf("live commits=%d err=%v", commits, err)
	}
}

func TestAgentRCSBDownloadDirectGatewayIsRetired(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	fetcher := &agentRCSBFetcher{body: "gateway body"}
	fixture.server.rcsbFiles = fetcher
	input := map[string]any{
		"resource_kind": "entry", "entry_id": "4TZ4", "format": "pdb", "human_description": "Downloading coordinates",
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (serverAgentRuntimeToolGateway{
		server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID,
		allowedTools: []string{"download_rcsb_file"},
	}).Execute(fixture.toolContext(t, "rcsb-gateway", input), agentruntime.ToolCall{
		ID: "rcsb-gateway", Name: "download_rcsb_file", Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := mapValue(result.Value)
	if value["code"] != "retired_tool" || value["ok"] != false {
		t.Fatalf("retired gateway result = %#v", result.Value)
	}
	if len(fetcher.calls) != 0 {
		t.Fatalf("retired gateway reached the network: %#v", fetcher.calls)
	}
}
