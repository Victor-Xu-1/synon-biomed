package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/rcsbfiles"
)

const (
	agentRCSBFileLimit   = int64(50 << 20)
	agentRCSBFileTimeout = 10 * time.Minute
)

var (
	errAgentRCSBAuthority = errors.New("rcsb download authority is unavailable")
	errAgentRCSBConflict  = errors.New("rcsb download target conflicts with an existing workspace file")
)

func agentRCSBAuthorityFailure(stage string, cause error) error {
	if cause == nil {
		log.Printf("agent rcsb authority rejected stage=%s", stage)
	} else {
		log.Printf("agent rcsb authority rejected stage=%s cause=%v", stage, cause)
	}
	return fmt.Errorf("%w: stage=%s", errAgentRCSBAuthority, stage)
}

type RCSBFileFetcher interface {
	Fetch(context.Context, rcsbfiles.Input) (rcsbfiles.Result, error)
}

func defaultRCSBFileFetcher(configured RCSBFileFetcher) RCSBFileFetcher {
	if configured != nil {
		return configured
	}
	return rcsbfiles.New(rcsbfiles.Options{MaxBytes: agentRCSBFileLimit, Timeout: agentRCSBFileTimeout})
}

func agentRCSBFileToolSchema() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{
		Name:        "download_rcsb_file",
		Description: "Download a validated public RCSB coordinate file or ligand definition into the current scientific workspace and publish it as a durable artifact. Entry PDB requests automatically fall back to mmCIF when RCSB does not publish the legacy PDB format. Existing identical workspace files are reused idempotently. The host performs the network request; analysis kernels remain network-isolated.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"resource_kind": map[string]any{"type": "string", "enum": []string{"entry", "ligand"}},
				"entry_id":      map[string]any{"type": "string", "description": "Four-character RCSB entry ID for entry coordinates."},
				"component_id":  map[string]any{"type": "string", "description": "RCSB chemical component ID for a ligand definition."},
				"format":        map[string]any{"type": "string", "enum": []string{"pdb", "cif"}},
				"filename":      map[string]any{"type": "string", "description": "Optional path-free workspace filename with the matching extension."},
				"human_description": map[string]any{
					"type": "string", "description": "Short present-participle action label describing the downloaded structure.",
				},
			},
			"required": []string{"format", "human_description"},
		},
	}
}

func (s *Server) executeAgentRCSBFileDownload(
	ctx context.Context,
	identity *agentKernelContext,
	toolCallID string,
	input map[string]any,
) (map[string]any, error) {
	if s == nil || s.rcsbFiles == nil || s.workspaceStore == nil || s.transcriptStore == nil || identity == nil {
		return nil, agentRCSBAuthorityFailure("dependencies", nil)
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return nil, errors.New("rcsb download tool call identity is required")
	}
	allowedFields := map[string]bool{
		"resource_kind": true, "entry_id": true, "component_id": true,
		"format": true, "filename": true, "human_description": true,
	}
	for field := range input {
		if !allowedFields[field] {
			return nil, errors.New("rcsb download input contains unsupported fields")
		}
	}
	description := strings.TrimSpace(stringValue(input["human_description"]))
	if description == "" || len([]byte(description)) > 256 {
		return nil, errors.New("rcsb download human_description must be 1-256 bytes")
	}
	access, err := s.validateKernelHostIdentity(ctx, identity.access)
	if err != nil {
		return nil, agentRCSBAuthorityFailure("kernel_identity", err)
	}
	run, ok := transcriptArtifactRunFromContext(ctx)
	if !ok || run.Authority == nil || run.SourceEventID <= 0 {
		return nil, agentRCSBAuthorityFailure("artifact_context", nil)
	}
	stream, claim := run.Authority.Stream, run.Authority.Claim
	if stream.OwnerID != access.UserID || stream.ProjectID != access.Frame.ProjectID ||
		stream.RootFrameID != access.Frame.RootFrameID || stream.FrameID != access.Frame.ID {
		return nil, agentRCSBAuthorityFailure("frame_scope", nil)
	}
	if err := s.transcriptStore.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		validated, validateErr := tx.ValidateLiveRunnerClaim(ctx, claim)
		if validateErr != nil {
			return validateErr
		}
		if validated.UID != stream.UID || validated.OwnerID != stream.OwnerID || validated.FrameID != stream.FrameID {
			return errAgentRCSBAuthority
		}
		return nil
	}); err != nil {
		return nil, agentRCSBAuthorityFailure("runner_claim", err)
	}
	request := rcsbfiles.Input{
		ResourceKind: inferAgentRCSBResourceKind(input),
		EntryID:      normalizeAgentRCSBOptionalIdentifier(stringValue(input["entry_id"])),
		ComponentID:  normalizeAgentRCSBOptionalIdentifier(stringValue(input["component_id"])),
		Format:       rcsbfiles.Format(strings.TrimSpace(stringValue(input["format"]))),
		Filename:     normalizeAgentRCSBFilename(stringValue(input["filename"])),
	}
	prepared, err := rcsbfiles.Prepare(request)
	if err != nil {
		return nil, err
	}
	artifactID := agentRCSBArtifactID(stream.UID, run.SourceEventID, prepared)
	if replayed, found, replayErr := s.replayAgentRCSBFileDownload(
		ctx, identity.workspaceDir, stream, run.SourceEventID, artifactID, prepared, description,
	); replayErr != nil || found {
		return replayed, replayErr
	}
	if err := ensureAgentRCSBWorkspaceTarget(ctx, identity.workspaceDir, prepared.Filename, 0, ""); err != nil {
		return nil, err
	}
	result, err := s.rcsbFiles.Fetch(ctx, request)
	if errors.Is(err, rcsbfiles.ErrFileNotFound) && request.ResourceKind == rcsbfiles.EntryCoordinates &&
		request.Format == rcsbfiles.FormatPDB && request.Filename == "" {
		request.Format = rcsbfiles.FormatCIF
		prepared, err = rcsbfiles.Prepare(request)
		if err != nil {
			return nil, err
		}
		artifactID = agentRCSBArtifactID(stream.UID, run.SourceEventID, prepared)
		if replayed, found, replayErr := s.replayAgentRCSBFileDownload(
			ctx, identity.workspaceDir, stream, run.SourceEventID, artifactID, prepared, description,
		); replayErr != nil || found {
			return replayed, replayErr
		}
		if err := ensureAgentRCSBWorkspaceTarget(ctx, identity.workspaceDir, prepared.Filename, 0, ""); err != nil {
			return nil, err
		}
		result, err = s.rcsbFiles.Fetch(ctx, request)
	}
	if err != nil {
		return nil, err
	}
	if result.Body == nil || result.ResourceKind != prepared.ResourceKind || result.Identifier != prepared.Identifier ||
		result.Format != prepared.Format || result.Filename != prepared.Filename {
		if result.Body != nil {
			_ = result.Body.Close()
		}
		return nil, errors.New("rcsb_file_response_identity_invalid")
	}
	staged, err := stageAgentRCSBFile(ctx, result.Body)
	closeErr := result.Body.Close()
	if err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	defer staged.close()
	if err := ensureAgentRCSBWorkspaceTarget(
		ctx, identity.workspaceDir, prepared.Filename, staged.sizeBytes, staged.contentSHA,
	); err != nil {
		return nil, err
	}
	contentType := "chemical/x-cif"
	if prepared.Format == rcsbfiles.FormatPDB {
		contentType = "chemical/x-pdb"
	}
	if _, err := staged.file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("rcsb download staging failed")
	}
	mutationDigest := sha256.Sum256([]byte(fmt.Sprintf(
		"download-rcsb-file-v1:%s:%d:%s:%s", stream.UID, run.SourceEventID, toolCallID, prepared.Filename,
	)))
	mutationID := "download-rcsb-file-" + hex.EncodeToString(mutationDigest[:])
	artifact, version, err := s.workspaceStore.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(ctx, mutationID),
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: stream.ProjectID, Name: prepared.Filename,
			ContentType: contentType, Content: staged.file, MaxBytes: agentRCSBFileLimit,
			CreatedBy: claim.RunnerID, RootFrameID: stream.RootFrameID, FrameID: stream.FrameID,
			TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
				StreamUID: stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: run.SourceEventID, Relation: "produced",
				ReuseCurrentVersionIfUnchanged: true,
			},
			Language: "rcsb",
		},
		stream.OwnerID,
	)
	if err != nil {
		return nil, err
	}
	if err := publishAgentRCSBWorkspaceFile(
		ctx, identity.workspaceDir, prepared.Filename, staged.file, version.SizeBytes, version.ContentSHA256,
	); err != nil {
		return nil, err
	}
	return s.agentRCSBFileResult(stream, artifact, version, prepared, description), nil
}

func inferAgentRCSBResourceKind(input map[string]any) rcsbfiles.ResourceKind {
	if explicit := strings.TrimSpace(stringValue(input["resource_kind"])); explicit != "" {
		return rcsbfiles.ResourceKind(explicit)
	}
	if normalizeAgentRCSBOptionalIdentifier(stringValue(input["entry_id"])) != "" {
		return rcsbfiles.EntryCoordinates
	}
	if normalizeAgentRCSBOptionalIdentifier(stringValue(input["component_id"])) != "" {
		return rcsbfiles.LigandDefinition
	}
	return ""
}

// normalizeAgentRCSBFilename keeps the host-side download boundary flat even
// when a model supplies a workspace-relative organizational path. The RCSB
// client still validates the resulting basename and extension, so traversal
// segments never reach filesystem operations.
func normalizeAgentRCSBFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return ""
	}
	return filepath.Base(strings.ReplaceAll(filename, `\`, "/"))
}

func normalizeAgentRCSBOptionalIdentifier(identifier string) string {
	identifier = strings.TrimSpace(identifier)
	switch strings.ToLower(identifier) {
	case "none", "null", "nil", "n/a":
		return ""
	default:
		return identifier
	}
}

func agentRCSBArtifactID(streamUID string, _ int64, prepared rcsbfiles.Prepared) string {
	return streamArtifactIDFor(streamUID, prepared.Filename)
}

func (s *Server) replayAgentRCSBFileDownload(
	ctx context.Context,
	workspaceDir string,
	stream transcriptstore.Stream,
	sourceEventID int64,
	artifactID string,
	prepared rcsbfiles.Prepared,
	description string,
) (map[string]any, bool, error) {
	commits, err := s.transcriptStore.ListArtifactCommitReferencesForEvent(
		ctx, stream.UID, stream.OwnerID, sourceEventID,
	)
	if err != nil {
		return nil, false, err
	}
	if len(commits) == 0 {
		return nil, false, nil
	}
	if len(commits) != 1 || commits[0].ArtifactID != artifactID || commits[0].Relation != transcriptstore.ArtifactRelationProduced {
		return nil, true, errAgentRCSBConflict
	}
	artifact, version, content, found, err := s.workspaceStore.OpenArtifactVersionContent(commits[0].VersionID)
	if err != nil || !found {
		if err == nil {
			err = errAgentRCSBAuthority
		}
		return nil, true, err
	}
	defer content.Close()
	if artifact.ID != artifactID || artifact.ProjectID != stream.ProjectID || artifact.Name != prepared.Filename ||
		version.ArtifactID != artifact.ID || version.SizeBytes <= 0 || strings.TrimSpace(version.ContentSHA256) == "" {
		return nil, true, errAgentRCSBConflict
	}
	if s.runtimeStore == nil {
		return nil, true, errAgentRCSBConflict
	}
	entry, metadataFound, metadataErr := s.runtimeStore.Get(artifactRuntimeNamespace, artifact.ID)
	if metadataErr != nil {
		return nil, true, metadataErr
	}
	metadata := mapValue(entry.Value)
	rcsbMetadata := mapValue(metadata["rcsbDownload"])
	if !metadataFound ||
		stringValue(rcsbMetadata["resource_kind"]) != string(prepared.ResourceKind) ||
		stringValue(rcsbMetadata["identifier"]) != prepared.Identifier ||
		stringValue(rcsbMetadata["format"]) != string(prepared.Format) ||
		stringValue(rcsbMetadata["filename"]) != prepared.Filename {
		return nil, true, errAgentRCSBConflict
	}
	if err := publishAgentRCSBWorkspaceFile(
		ctx, workspaceDir, prepared.Filename, content, version.SizeBytes, version.ContentSHA256,
	); err != nil {
		return nil, true, err
	}
	return s.agentRCSBFileResult(stream, artifact, version, prepared, description), true, nil
}

func (s *Server) agentRCSBFileResult(
	stream transcriptstore.Stream,
	artifact workspace.Artifact,
	version workspace.ArtifactVersion,
	prepared rcsbfiles.Prepared,
	description string,
) map[string]any {
	projection := map[string]any{
		"artifactId": artifact.ID, "versionId": version.ID, "kind": "file", "title": artifact.Name,
		"description": description, "relativePath": prepared.Filename, "fileName": artifact.Name,
		"mimeType": artifact.Kind, "sizeBytes": version.SizeBytes, "sha256": version.ContentSHA256,
		"sessionId": stream.SessionID, "runId": version.CreatedBy,
		"rcsbDownload": map[string]any{
			"resource_kind": string(prepared.ResourceKind), "identifier": prepared.Identifier,
			"format": string(prepared.Format), "filename": prepared.Filename,
		},
	}
	if s.runtimeStore != nil {
		if _, err := s.runtimeStore.Set(artifactRuntimeNamespace, artifact.ID, projection); err != nil {
			log.Printf("download_rcsb_file compatibility projection failed for artifact %s; canonical artifact remains authoritative", artifact.ID)
		}
	}
	artifactResult := map[string]any{
		"artifact_id": artifact.ID, "version_id": version.ID, "version_number": version.VersionNumber,
		"filename": artifact.Name, "content_type": artifact.Kind, "size_bytes": version.SizeBytes,
		"checksum": version.ContentSHA256, "storage_path": version.StoragePath,
		"input_path": prepared.Filename, "is_checkpoint": false,
		"root_frame_id": stream.RootFrameID, "environment": "",
	}
	for key, value := range agentArtifactURLs(artifact.ID, version.ID) {
		artifactResult[key] = value
	}
	return map[string]any{
		"artifacts": []any{artifactResult},
		"download": map[string]any{
			"resource_kind": string(prepared.ResourceKind), "identifier": prepared.Identifier,
			"format": string(prepared.Format), "filename": prepared.Filename, "content_type": artifact.Kind,
			"size_bytes": version.SizeBytes, "sha256": version.ContentSHA256,
		},
	}
}

type agentRCSBStagedFile struct {
	file       *os.File
	path       string
	sizeBytes  int64
	contentSHA string
}

func (staged *agentRCSBStagedFile) close() {
	if staged == nil {
		return
	}
	if staged.file != nil {
		_ = staged.file.Close()
		staged.file = nil
	}
	if staged.path != "" {
		_ = os.Remove(staged.path)
		staged.path = ""
	}
}

func stageAgentRCSBFile(ctx context.Context, body io.Reader) (*agentRCSBStagedFile, error) {
	file, err := os.CreateTemp("", "synon-rcsb-download-*")
	if err != nil {
		return nil, errAgentRCSBAuthority
	}
	staged := &agentRCSBStagedFile{file: file, path: file.Name()}
	ok := false
	defer func() {
		if !ok {
			staged.close()
		}
	}()
	hasher := sha256.New()
	written, err := io.Copy(
		io.MultiWriter(file, hasher),
		io.LimitReader(&contextReader{ctx: ctx, reader: body}, agentRCSBFileLimit+1),
	)
	if err != nil || written <= 0 || written > agentRCSBFileLimit || file.Sync() != nil {
		return nil, errors.New("rcsb download staging failed")
	}
	if err := file.Chmod(0o400); err != nil {
		return nil, errors.New("rcsb download staging failed")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("rcsb download staging failed")
	}
	staged.sizeBytes = written
	staged.contentSHA = hex.EncodeToString(hasher.Sum(nil))
	ok = true
	return staged, nil
}

func ensureAgentRCSBWorkspaceTarget(
	ctx context.Context,
	workspaceDir, filename string,
	expectedSize int64,
	expectedSHA string,
) error {
	return agentRCSBWorkspaceDownloadError(ensureAgentWorkspaceDownloadTarget(
		ctx, workspaceDir, filename, expectedSize, expectedSHA, agentRCSBFileLimit,
	))
}

func publishAgentRCSBWorkspaceFile(
	ctx context.Context,
	workspaceDir, filename string,
	content io.ReadSeeker,
	expectedSize int64,
	expectedSHA string,
) error {
	return agentRCSBWorkspaceDownloadError(publishAgentWorkspaceDownloadFile(
		ctx, workspaceDir, filename, content, expectedSize, expectedSHA, agentRCSBFileLimit,
	))
}

func agentRCSBWorkspaceDownloadError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errAgentWorkspaceDownloadConflict) {
		return errAgentRCSBConflict
	}
	return errAgentRCSBAuthority
}
