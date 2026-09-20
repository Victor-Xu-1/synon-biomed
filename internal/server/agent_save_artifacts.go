package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	runtimekv "synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/runtimecontrol"
)

const maxAgentSavedArtifacts = 256

var (
	errAgentSavedArtifactNotRegular         = errors.New("saved artifact is not a regular file")
	errAgentSavedArtifactPathUnauthorized   = errors.New("saved artifact path is not authorized")
	errAgentSavedArtifactPathAmbiguous      = errors.New("saved artifact path is ambiguous")
	errAgentSavedArtifactVersionInvalid     = errors.New("saved artifact version reference is invalid")
	errAgentSavedArtifactProjection         = errors.New("saved artifact compatibility projection failed")
	errAgentSavedArtifactSourceChanged      = errors.New("saved artifact source changed during snapshot")
	errAgentSaveArtifactsNoResults          = errors.New("save_artifacts did not publish any files")
	errAgentSavedArtifactJSONInvalid        = errors.New("saved JSON artifact is invalid")
	errAgentSavedArtifactPythonInvalid      = errors.New("saved Python artifact is invalid")
	errAgentSavedArtifactTemplateUnresolved = errors.New("saved artifact contains unresolved template markers")
)

type agentSaveArtifactsRequest struct {
	Files            []string
	Failures         []map[string]any
	RejectedPaths    map[string]bool
	Language         string
	Environment      string
	VersionOf        map[string]string
	Checkpoints      map[string]bool
	Destination      map[string]string
	HumanDescription string
	BundleAdditions  []string
}

type agentSaveArtifactDestinationRule struct {
	path        string
	destination string
	exact       bool
}

type agentSavedArtifactSource struct {
	root         string
	rootHandle   *os.Root
	relativePath string
	absolutePath string
}

func (source *agentSavedArtifactSource) close() {
	if source != nil && source.rootHandle != nil {
		_ = source.rootHandle.Close()
		source.rootHandle = nil
	}
}

func agentSaveArtifactsToolSchema() agentruntime.ToolSchema {
	return agentruntime.ToolSchema{
		Name:        "save_artifacts",
		Description: "Save declared workspace files as durable artifacts. Use destination=snapshot only for final user-facing reports, tables, figures, media, or explicitly requested source files; use working_data for internal evidence, validation, provenance, logs, scripts, notebooks, and JSON. Snapshot drafts stay hidden until terminal validation and unchanged bytes reuse the current version. Save fully materialized, internally consistent files and use workspace-relative links between companions. Never edit a file to insert its own artifact reference or chase version links. The Harness publishes exact immutable links with the final answer. Evidence and consistency checks run as non-blocking quality advisories after valid bytes are retained; they never create completion_pending. When differently labelled Markdown/CSV tables intentionally project one small shared record set, an optional working_data JSON file with schema synon.artifact-table-contract.v1 can declare identity_fields, compare_fields, records, and per-artifact header mappings without changing visible columns.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"files": map[string]any{
					"type": "array", "minItems": 1, "maxItems": maxAgentSavedArtifacts,
					"items":       map[string]any{"type": "string"},
					"description": "Relative paths to files produced in the current scientific workspace.",
				},
				"language": map[string]any{
					"type": "string", "minLength": 1, "maxLength": 100,
					"pattern":     `^[A-Za-z0-9][A-Za-z0-9._-]*$`,
					"description": "One path-free producer identifier for the whole batch, such as python, r, repl, or text; this is not a MIME type or a slash-separated format list. Use text when saving mixed file formats.",
				},
				"environment": map[string]any{
					"type": "string", "minLength": 0, "maxLength": 100,
					"pattern":     `^(?:|[A-Za-z0-9][A-Za-z0-9._-]*)$`,
					"description": "Path-free managed environment identifier associated with the producing execution, when applicable.",
				},
				"version_of": map[string]any{
					"type": "object", "additionalProperties": map[string]any{"type": "string"},
					"description": "Map each input path being updated to its existing artifact_id or version_id.",
				},
				"checkpoints": map[string]any{
					"type": "array", "maxItems": maxAgentSavedArtifacts, "uniqueItems": true,
					"items":       map[string]any{"type": "string"},
					"description": "Subset of files that are expensive intermediate checkpoints.",
				},
				"destination": map[string]any{
					"type": "object", "additionalProperties": map[string]any{"type": "string", "enum": []string{"working_data", "snapshot"}},
					"description": "Map each input path to working_data (internal durable evidence, hidden from user UI) or snapshot (user-facing deliverable). An explicit file key also declares that file when it was accidentally omitted from files; a directory key only classifies files already declared beneath it. Outputs assigned a delivery role by a validated scientific execution pack are included automatically as one coherent result bundle.",
				},
				"human_description": map[string]any{
					"type": "string", "description": "Short present-participle action label describing the files being saved.",
				},
			},
			"required": []string{"files", "language", "human_description"},
		},
	}
}

func (s *Server) executeAgentSaveArtifacts(
	ctx context.Context,
	identity *agentKernelContext,
	toolCallID string,
	input map[string]any,
) (map[string]any, error) {
	if s == nil || s.workspaceStore == nil || s.transcriptStore == nil || identity == nil {
		return nil, errors.New("save_artifacts authority is unavailable")
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return nil, errors.New("save_artifacts tool call identity is required")
	}
	request, err := parseAgentSaveArtifactsRequest(input)
	if err != nil {
		return nil, err
	}
	access, err := s.validateKernelHostIdentity(ctx, identity.access)
	if err != nil {
		return nil, err
	}
	run, ok := transcriptArtifactRunFromContext(ctx)
	if !ok || run.Authority == nil {
		return nil, errors.New("save_artifacts requires an active transcript tool source")
	}
	stream, claim := run.Authority.Stream, run.Authority.Claim
	if stream.OwnerID != access.UserID || stream.ProjectID != access.Frame.ProjectID ||
		stream.RootFrameID != access.Frame.RootFrameID || stream.FrameID != access.Frame.ID {
		return nil, errors.New("save_artifacts transcript authority does not match the active frame")
	}
	priorVersions := make(map[string]workspace.ArtifactVersion, len(request.VersionOf))
	preflightFailures := make(map[string]map[string]any, len(request.VersionOf))
	for relativePath, reference := range request.VersionOf {
		expectedArtifactID := agentSavedArtifactID(stream.UID, relativePath)
		priorArtifact, priorVersion, found, lookupErr := s.resolveAgentSavedArtifactVersion(reference)
		if lookupErr != nil || !found || priorArtifact.ProjectID != stream.ProjectID || priorArtifact.ID != expectedArtifactID {
			preflightFailures[relativePath] = agentSaveArtifactFailure(relativePath, errAgentSavedArtifactVersionInvalid)
			continue
		}
		priorVersions[relativePath] = priorVersion
	}
	records, err := s.workspaceStore.ListExecutionLog(access.Frame.ID, "")
	if err != nil {
		return nil, errors.New("save_artifacts execution lineage could not be read")
	}
	executionBindings, err := s.workspaceStore.KernelLocalExecutionBindings(ctx, access)
	if err != nil {
		return nil, errors.New("save_artifacts operation lineage could not be read")
	}
	outputAuthorities, err := s.managedExecutionOutputAuthorities(
		ctx, access, identity.workspaceDir, records, executionBindings,
	)
	if err != nil {
		return nil, err
	}
	request = s.augmentAgentSaveArtifactsWithExecutionBundles(
		identity.workspaceDir, request, outputAuthorities,
	)
	var evidenceMessages []agentruntime.Message
	evidenceMessagesLoaded := false
	evidenceMessagesUnavailable := false

	artifacts := make([]any, 0, len(request.Files))
	failures := make([]any, 0, len(request.Failures)+len(preflightFailures))
	// Citation/evidence quality never prevents persistence of valid bytes.
	// Return an advisory with the saved version so the same outer model loop may
	// improve it; structural and format integrity remain separate fail-closed
	// checks at both save and terminal publication.
	warnings := make([]any, 0)
	for _, failure := range request.Failures {
		failures = append(failures, failure)
	}
	for index, relativePath := range request.Files {
		if request.RejectedPaths[relativePath] {
			continue
		}
		if failure, found := preflightFailures[relativePath]; found {
			failures = append(failures, failure)
			continue
		}
		source, resolveErr := s.resolveAgentSavedArtifactSource(
			access, identity.workspaceDir, relativePath, records, executionBindings,
		)
		if resolveErr != nil {
			log.Printf("save_artifacts resolve failed: file=%q workspaceDir=%q err=%v", relativePath, identity.workspaceDir, resolveErr)
			failures = append(failures, agentSaveArtifactFailure(relativePath, resolveErr))
			continue
		}
		snapshot, sizeBytes, contentSHA, snapshotErr := snapshotAgentSavedArtifact(ctx, source)
		source.close()
		if snapshotErr != nil {
			failures = append(failures, agentSaveArtifactFailure(relativePath, snapshotErr))
			continue
		}
		snapshotPath := snapshot.Name()
		if validationErr := validateAgentFileContentType(relativePath, snapshot); validationErr != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshotPath)
			failures = append(failures, agentSaveArtifactFailure(relativePath, validationErr))
			continue
		}
		normalizedReferences, normalizationErr := s.normalizeAgentSavedArtifactReferences(ctx, snapshot, relativePath, stream.ProjectID)
		if normalizationErr != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshotPath)
			failures = append(failures, agentSaveArtifactFailure(relativePath, normalizationErr))
			continue
		}
		if normalizedReferences {
			sizeBytes, contentSHA, normalizationErr = digestAgentSavedArtifactSnapshot(snapshot)
			if normalizationErr != nil {
				_ = snapshot.Close()
				_ = os.Remove(snapshotPath)
				failures = append(failures, agentSaveArtifactFailure(relativePath, normalizationErr))
				continue
			}
		}
		if validationErr := validateAgentSavedArtifactTemplates(ctx, relativePath, snapshot); validationErr != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshotPath)
			failures = append(failures, agentSaveArtifactFailure(relativePath, validationErr))
			continue
		}
		if validationErr := validateAgentSavedArtifactJSON(ctx, relativePath, snapshot); validationErr != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshotPath)
			failures = append(failures, agentSaveArtifactFailure(relativePath, validationErr))
			continue
		}
		if validationErr := validateAgentSavedArtifactDelimited(relativePath, snapshot); validationErr != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshotPath)
			failures = append(failures, agentSaveArtifactFailure(relativePath, validationErr))
			continue
		}
		if validationErr := s.validateAgentSavedPythonArtifact(ctx, relativePath, snapshot); validationErr != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshotPath)
			failures = append(failures, agentSaveArtifactFailure(relativePath, validationErr))
			continue
		}
		if validationErr := validateAgentSavedArtifactStructure(relativePath, snapshot); validationErr != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshotPath)
			failures = append(failures, agentSaveArtifactFailure(relativePath, validationErr))
			continue
		}
		extension := strings.ToLower(filepath.Ext(relativePath))
		if extension == ".sdf" || extension == ".smi" || extension == ".smiles" {
			var validationErr error
			if extension == ".sdf" {
				_, validationErr = s.validateSDFArtifact(ctx, snapshot)
			} else {
				_, validationErr = s.validateSMILESArtifact(ctx, snapshot)
			}
			if validationErr != nil {
				_ = snapshot.Close()
				_ = os.Remove(snapshotPath)
				failures = append(failures, agentSaveArtifactFailure(relativePath, validationErr))
				continue
			}
			if _, seekErr := snapshot.Seek(0, io.SeekStart); seekErr != nil {
				_ = snapshot.Close()
				_ = os.Remove(snapshotPath)
				failures = append(failures, agentSaveArtifactFailure(relativePath, seekErr))
				continue
			}
		}
		contentType := detectAgentSavedArtifactMIME(relativePath, snapshot)
		executionIDs := agentSavedArtifactExecutionIDs(
			records, access, identity.workspaceDir, source.absolutePath, contentSHA, executionBindings,
		)
		switch agentSavedArtifactEvidencePolicyFor(contentType, relativePath) {
		case agentSavedArtifactEvidenceRawExecution:
			if len(executionIDs) == 0 {
				_ = snapshot.Close()
				_ = os.Remove(snapshotPath)
				failures = append(failures, agentSaveArtifactFailure(relativePath, errAgentSavedArtifactRawExecutionLineage))
				continue
			}
		case agentSavedArtifactEvidenceCitations:
			checkCitations := s.agentSavedArtifactCitationIntegrityRequired(ctx)
			structuredEvidence, structuredEvidenceErr := agentSavedArtifactContainsStructuredEvidence(ctx, relativePath, snapshot)
			if structuredEvidenceErr != nil {
				_ = snapshot.Close()
				_ = os.Remove(snapshotPath)
				failures = append(failures, agentSaveArtifactFailure(relativePath, structuredEvidenceErr))
				continue
			}
			if !checkCitations && !structuredEvidence {
				break
			}
			if !evidenceMessagesLoaded {
				var evidenceErr error
				evidenceMessages, evidenceErr = s.agentSavedArtifactEvidenceMessages(ctx, run)
				if evidenceErr != nil {
					warning := agentSaveArtifactFailure(relativePath, evidenceErr)
					annotateAgentSavedArtifactEvidenceAdvisory(warning)
					warnings = append(warnings, warning)
					evidenceMessagesUnavailable = true
				}
				evidenceMessagesLoaded = true
			}
			if evidenceMessagesUnavailable {
				break
			}
			if evidenceErr := s.validateAgentSavedArtifactEvidenceStream(
				ctx, relativePath, snapshot, evidenceMessages, checkCitations,
			); evidenceErr != nil {
				warning := agentSaveArtifactFailure(relativePath, evidenceErr)
				annotateAgentSavedArtifactEvidenceAdvisory(warning)
				warnings = append(warnings, warning)
			}
		}
		artifactID := agentSavedArtifactID(stream.UID, relativePath)
		currentArtifact, currentVersion, found, lookupErr := s.workspaceStore.GetCurrentArtifactVersionMetadata(artifactID)
		if lookupErr != nil {
			_ = snapshot.Close()
			_ = os.Remove(snapshotPath)
			return nil, errors.New("save_artifacts current publication could not be read")
		}
		retention := strings.TrimSpace(request.Destination[relativePath])
		if retention == "" && found && currentArtifact.ProjectID == stream.ProjectID {
			if storedMode, modeFound, modeErr := s.workspaceStore.ArtifactRetentionMode(currentArtifact.ID); modeErr != nil {
				_ = snapshot.Close()
				_ = os.Remove(snapshotPath)
				return nil, modeErr
			} else if modeFound {
				retention = storedMode
			}
		}
		if retention == "" {
			retention = "snapshot"
		}

		mutationDigest := sha256.Sum256([]byte(fmt.Sprintf(
			"save-artifacts:%s:%d:%s:%d:%s", stream.UID, run.SourceEventID, toolCallID, index, relativePath,
		)))
		mutationID := "save-artifacts-" + hex.EncodeToString(mutationDigest[:])
		writeArtifactVersion := func(parentVersionID string) (workspace.Artifact, workspace.ArtifactVersion, error) {
			return s.workspaceStore.WriteArtifactVersionRealtime(
				workspace.WithMutationIdempotencyKey(ctx, mutationID),
				workspace.WriteArtifactVersionInput{
					ArtifactID: artifactID, ProjectID: stream.ProjectID, Name: filepath.Base(relativePath),
					ContentType: contentType, Content: snapshot, MaxBytes: max(sizeBytes, 1),
					CreatedBy: claim.RunnerID, ParentVersionID: parentVersionID,
					RootFrameID: stream.RootFrameID, FrameID: stream.FrameID,
					TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
						StreamUID: stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
						Attempt: claim.Attempt, SourceEventID: run.SourceEventID, Relation: "produced",
					},
					Language: request.Language, Environment: request.Environment,
					IsIntermediate: retention == "snapshot",
					IsCheckpoint:   request.Checkpoints[relativePath], ExecutionLogIDs: executionIDs,
				},
				stream.OwnerID,
			)
		}
		// Reuse the existing immutable version without staging a second blob, but
		// still append a produced transcript association in the same authoritative
		// realtime transaction. Without that association a consumed download
		// remained absent from the final file collection after an explicit save.
		sameCurrentVersion := found && currentArtifact.ProjectID == stream.ProjectID &&
			strings.EqualFold(strings.TrimSpace(currentVersion.ContentSHA256), strings.TrimSpace(contentSHA)) &&
			currentVersion.SizeBytes == sizeBytes
		if sameCurrentVersion {
			closeErr := snapshot.Close()
			_ = os.Remove(snapshotPath)
			if closeErr != nil {
				failures = append(failures, agentSaveArtifactFailure(relativePath, closeErr))
				continue
			}
			association := &workspace.ArtifactTranscriptAssociation{
				StreamUID: stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: run.SourceEventID, Relation: "produced",
			}
			artifact, version, associationErr := s.workspaceStore.AssociateArtifactVersionRealtime(
				workspace.WithMutationIdempotencyKey(ctx, mutationID),
				workspace.AssociateArtifactVersionInput{
					ArtifactID: artifactID, ProjectID: stream.ProjectID, VersionID: currentVersion.ID,
					RootFrameID: stream.RootFrameID, FrameID: stream.FrameID,
					TranscriptAssociation: association,
				},
				stream.OwnerID,
			)
			if associationErr != nil {
				failures = append(failures, agentSaveArtifactFailure(relativePath, associationErr))
				continue
			}
			if explicit := strings.TrimSpace(request.Destination[relativePath]); explicit != "" {
				if modeErr := s.workspaceStore.SetArtifactRetentionMode(ctx, artifact.ID, stream.ProjectID, stream.OwnerID, explicit); modeErr != nil {
					return nil, modeErr
				}
			}
			intermediate, intermediateFound, intermediateErr := s.workspaceStore.ArtifactVersionIntermediate(version.ID)
			if intermediateErr != nil {
				return nil, intermediateErr
			}
			if !intermediateFound {
				// Legacy versions without provenance were already published by the
				// pre-draft path; do not hide them during an unchanged replay.
				intermediate = false
			}
			artifacts = append(artifacts, agentSavedArtifactResult(
				artifact, version, relativePath, contentType,
				request.Checkpoints[relativePath], stream.RootFrameID,
				request.Environment, retention, intermediate, true,
			))
			continue
		}

		parentVersionID := ""
		if priorVersion, found := priorVersions[relativePath]; found {
			parentVersionID = priorVersion.ID
		}
		artifact, version, writeErr := writeArtifactVersion(parentVersionID)
		closeErr := snapshot.Close()
		_ = os.Remove(snapshotPath)
		if writeErr != nil || closeErr != nil {
			if writeErr == nil {
				writeErr = closeErr
			}
			failures = append(failures, agentSaveArtifactFailure(relativePath, writeErr))
			continue
		}
		retentionError := ""
		if explicit := strings.TrimSpace(request.Destination[relativePath]); explicit != "" {
			if modeErr := s.workspaceStore.SetArtifactRetentionMode(ctx, artifact.ID, stream.ProjectID, stream.OwnerID, explicit); modeErr != nil {
				retentionError = modeErr.Error()
			}
		}
		if retention == "working_data" && retentionError == "" {
			if _, pruneErr := s.workspaceStore.PruneArtifactVersionContentExcept(ctx, artifact.ID, version.ID, stream.ProjectID, stream.OwnerID); pruneErr != nil {
				retentionError = pruneErr.Error()
			}
		}
		projection := map[string]any{
			"artifactId": artifact.ID, "versionId": version.ID, "kind": "file", "title": artifact.Name,
			"description":  request.HumanDescription,
			"relativePath": relativePath, "fileName": artifact.Name, "mimeType": contentType,
			"sizeBytes": version.SizeBytes, "sha256": version.ContentSHA256,
			"sessionId": stream.SessionID, "runId": claim.RunnerID,
			"retention": retention,
		}
		if s.runtimeStore != nil {
			if metadataErr := carryForwardRCSBArtifactRuntimeMetadata(s.runtimeStore, artifact.ID, projection); metadataErr != nil {
				log.Printf("save_artifacts compatibility metadata carry-forward failed for artifact %s: %v", artifact.ID, metadataErr)
			}
			if _, projectionErr := s.runtimeStore.Set(artifactRuntimeNamespace, artifact.ID, projection); projectionErr != nil {
				log.Printf("save_artifacts compatibility projection failed for artifact %s; canonical artifact remains authoritative", artifact.ID)
			}
		}
		artifactResult := agentSavedArtifactResult(
			artifact, version, relativePath, contentType, request.Checkpoints[relativePath],
			stream.RootFrameID, request.Environment, retention, retention == "snapshot", false,
		)
		if retentionError != "" {
			artifactResult["retention_error"] = retentionError
		}
		artifacts = append(artifacts, artifactResult)
	}
	if warning := s.agentSavedArtifactBatchQualityAdvisory(ctx, stream.ProjectID, artifacts); warning != nil {
		warnings = append(warnings, warning)
	}
	result := map[string]any{"artifacts": artifacts}
	if len(request.BundleAdditions) > 0 {
		result["execution_bundle_files"] = append([]string(nil), request.BundleAdditions...)
	}
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	if len(failures) > 0 {
		result["errors"] = failures
		if len(artifacts) == 0 {
			return result, agentSaveArtifactsNoResultsError(failures)
		}
	}
	return result, nil
}

func agentSavedArtifactResult(
	artifact workspace.Artifact,
	version workspace.ArtifactVersion,
	relativePath, contentType string,
	isCheckpoint bool,
	rootFrameID, environment, retention string,
	isIntermediate bool,
	unchanged bool,
) map[string]any {
	result := map[string]any{
		"artifact_id": artifact.ID, "version_id": version.ID, "version_number": version.VersionNumber,
		"filename": artifact.Name, "content_type": contentType, "size_bytes": version.SizeBytes,
		"checksum": version.ContentSHA256, "storage_path": version.StoragePath,
		"input_path": relativePath, "is_checkpoint": isCheckpoint,
		"root_frame_id": rootFrameID,
		"environment":   environment, "retention": retention,
	}
	for key, value := range agentArtifactURLs(artifact.ID, version.ID) {
		result[key] = value
	}
	if retention == "snapshot" && isIntermediate {
		result["publication_state"] = "draft"
		result["message"] = "durable draft saved; the Harness will publish the validated terminal version"
	} else if retention == "snapshot" {
		artifactRef := agentArtifactReference(version.ID)
		result["publication_state"] = "published"
		result["artifact_ref"] = artifactRef
		result["markdown_link"] = "[" + artifact.Name + "](" + artifactRef + ")"
	} else {
		result["publication_state"] = "working_data"
	}
	if unchanged {
		result["unchanged"] = true
		if !isIntermediate && retention == "snapshot" {
			result["message"] = "the artifact bytes already match the current published version"
		}
	}
	return result
}

// agentSaveArtifactsNoResultsError summarizes per-file failures so the model
// can act on the actual cause (for example unsupported evidence references)
// instead of retrying identical input.
func agentSaveArtifactsNoResultsError(failures []any) error {
	message := errAgentSaveArtifactsNoResults.Error()
	var evidenceRefs []string
	var otherCodes []string
	seenRefs := map[string]struct{}{}
	seenCodes := map[string]struct{}{}
	for _, raw := range failures {
		failure, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		code := strings.TrimSpace(stringValue(failure["code"]))
		if code != "" {
			if _, seen := seenCodes[code]; !seen {
				seenCodes[code] = struct{}{}
				otherCodes = append(otherCodes, code)
			}
		}
		for _, rawRef := range anySliceValue(failure["unsupported_evidence_references"]) {
			ref := strings.TrimSpace(fmt.Sprint(rawRef))
			if ref == "" {
				continue
			}
			if _, seen := seenRefs[ref]; !seen {
				seenRefs[ref] = struct{}{}
				evidenceRefs = append(evidenceRefs, ref)
			}
		}
	}
	if len(evidenceRefs) > 0 {
		sort.Strings(evidenceRefs)
		if len(evidenceRefs) > 20 {
			evidenceRefs = append(evidenceRefs[:20], "...")
		}
		message += "; unsupported evidence references: " + strings.Join(evidenceRefs, ", ")
	}
	if len(otherCodes) > 0 {
		sort.Strings(otherCodes)
		message += "; failure codes: " + strings.Join(otherCodes, ", ")
	}
	return fmt.Errorf("%w%s", errAgentSaveArtifactsNoResults, strings.TrimPrefix(message, errAgentSaveArtifactsNoResults.Error()))
}

// streamArtifactIDFor is the canonical identity for a user-visible file in a
// transcript stream. All producers (downloads, explicit saves, and direct
// registrations) must use the same stream/path key so re-publishing a file
// appends a version instead of creating a second current artifact.
func streamArtifactIDFor(streamUID, relativePath string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf(
		"agent-save-artifact:%s:%s", strings.TrimSpace(streamUID), filepath.ToSlash(strings.TrimSpace(relativePath)),
	))).String()
}

func agentSavedArtifactID(streamUID, relativePath string) string {
	return streamArtifactIDFor(streamUID, relativePath)
}

func carryForwardRCSBArtifactRuntimeMetadata(
	store *runtimekv.Store,
	artifactID string,
	projection map[string]any,
) error {
	if store == nil {
		return nil
	}
	entry, found, err := store.Get(artifactRuntimeNamespace, artifactID)
	if err != nil || !found {
		return err
	}
	previous := mapValue(entry.Value)
	if rcsbDownload := mapValue(previous["rcsbDownload"]); len(rcsbDownload) > 0 {
		projection["rcsbDownload"] = rcsbDownload
	}
	return nil
}

func parseAgentSaveArtifactsRequest(input map[string]any) (agentSaveArtifactsRequest, error) {
	allowed := map[string]bool{
		"files": true, "language": true, "environment": true,
		"version_of": true, "checkpoints": true, "destination": true, "human_description": true,
	}
	for key := range input {
		if !allowed[key] {
			return agentSaveArtifactsRequest{}, errors.New("save_artifacts input contains unsupported fields")
		}
	}
	description, ok := input["human_description"].(string)
	if !ok || strings.TrimSpace(description) == "" || len([]byte(description)) > 256 {
		return agentSaveArtifactsRequest{}, errors.New("save_artifacts human_description must be 1-256 bytes")
	}
	files, failures, err := parseAgentSaveArtifactFiles(input["files"])
	if err != nil {
		return agentSaveArtifactsRequest{}, err
	}
	files, destinationRules, err := parseAgentSaveArtifactDestinationRules(input["destination"], files)
	if err != nil {
		return agentSaveArtifactsRequest{}, err
	}
	if len(files)+len(failures) > maxAgentSavedArtifacts {
		return agentSaveArtifactsRequest{}, fmt.Errorf("save_artifacts supports at most %d files", maxAgentSavedArtifacts)
	}
	request := agentSaveArtifactsRequest{
		Files: files, Failures: failures, RejectedPaths: map[string]bool{}, VersionOf: map[string]string{}, Checkpoints: map[string]bool{}, Destination: map[string]string{},
		HumanDescription: strings.TrimSpace(description),
	}
	rawLanguage, found := input["language"]
	language, ok := rawLanguage.(string)
	if !found || !ok || strings.TrimSpace(language) == "" || !kernelruntime.ValidEnvironmentName(strings.TrimSpace(language)) {
		return agentSaveArtifactsRequest{}, errors.New("save_artifacts language must be a bounded path-free identifier")
	}
	request.Language = strings.TrimSpace(language)
	if raw, found := input["environment"]; found {
		value, ok := raw.(string)
		if !ok {
			return agentSaveArtifactsRequest{}, errors.New("save_artifacts environment must be a bounded path-free identifier")
		}
		value = strings.TrimSpace(value)
		if value != "" {
			if !kernelruntime.ValidEnvironmentName(value) {
				return agentSaveArtifactsRequest{}, errors.New("save_artifacts environment must be a bounded path-free identifier")
			}
			request.Environment = value
		}
	}
	fileSet := map[string]bool{}
	for _, file := range files {
		fileSet[file] = true
	}
	if raw, found := input["version_of"]; found {
		values, ok := raw.(map[string]any)
		if !ok {
			if typed, typedOK := raw.(map[string]string); typedOK {
				values = make(map[string]any, len(typed))
				for key, value := range typed {
					values[key] = value
				}
			} else {
				return agentSaveArtifactsRequest{}, errors.New("save_artifacts version_of must be an object")
			}
		}
		for rawPath, rawReference := range values {
			path, normalizeErr := normalizeAgentSavedArtifactPath(rawPath)
			reference, referenceOK := rawReference.(string)
			if !referenceOK {
				reference = ""
			}
			reference = strings.TrimSpace(reference)
			_, uuidErr := uuid.Parse(reference)
			if normalizeErr != nil {
				request.Failures = append(request.Failures, agentSaveArtifactInputFailure(-1, errAgentSavedArtifactVersionInvalid))
				continue
			}
			if !referenceOK || reference == "" || len([]byte(reference)) > 128 || uuidErr != nil || !fileSet[path] {
				request.Failures = append(request.Failures, agentSaveArtifactFailure(path, errAgentSavedArtifactVersionInvalid))
				request.RejectedPaths[path] = true
				continue
			}
			request.VersionOf[path] = reference
		}
	}
	if raw, found := input["checkpoints"]; found {
		values, listErr := strictAgentArtifactStringList(raw, "checkpoints", false)
		if listErr != nil {
			return agentSaveArtifactsRequest{}, listErr
		}
		for _, path := range values {
			if !fileSet[path] {
				return agentSaveArtifactsRequest{}, errors.New("save_artifacts checkpoints must be a subset of files")
			}
			if request.Checkpoints[path] {
				return agentSaveArtifactsRequest{}, errors.New("save_artifacts checkpoints must be unique")
			}
			request.Checkpoints[path] = true
		}
	}
	if len(destinationRules) > 0 {
		// Directory rules are a bounded shorthand over the explicit files list;
		// they never enumerate the workspace. Exact file rules intentionally
		// override a containing directory rule.
		for _, rule := range destinationRules {
			if rule.exact {
				continue
			}
			prefix := rule.path + "/"
			for filePath := range fileSet {
				if !strings.HasPrefix(filePath, prefix) {
					continue
				}
				if previous, exists := request.Destination[filePath]; exists && previous != rule.destination {
					return agentSaveArtifactsRequest{}, errors.New("save_artifacts destination directory rules must not conflict")
				}
				request.Destination[filePath] = rule.destination
			}
		}
		for _, rule := range destinationRules {
			if rule.exact {
				request.Destination[rule.path] = rule.destination
			}
		}
	}
	return request, nil
}

func parseAgentSaveArtifactDestinationRules(
	raw any,
	files []string,
) ([]string, []agentSaveArtifactDestinationRule, error) {
	if raw == nil {
		return files, nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, errors.New("save_artifacts destination must be an object")
	}
	fileSet := make(map[string]bool, len(files)+len(values))
	for _, file := range files {
		fileSet[file] = true
	}
	keys := make([]string, 0, len(values))
	for rawPath := range values {
		keys = append(keys, rawPath)
	}
	sort.Strings(keys)
	rules := make([]agentSaveArtifactDestinationRule, 0, len(keys))
	for _, rawPath := range keys {
		path, normalizeErr := normalizeAgentSavedArtifactPath(rawPath)
		destination, destinationOK := values[rawPath].(string)
		destination = strings.TrimSpace(destination)
		if normalizeErr != nil || !destinationOK ||
			(destination != "working_data" && destination != "snapshot") {
			return nil, nil, errors.New("save_artifacts destination entries must map files to working_data or snapshot")
		}
		exact := fileSet[path]
		if !exact {
			prefix := path + "/"
			directoryRule := false
			for filePath := range fileSet {
				if strings.HasPrefix(filePath, prefix) {
					directoryRule = true
					break
				}
			}
			if !directoryRule {
				// The explicit files array is authoritative. A destination key may
				// recover one accidentally omitted file only when it is visibly a
				// file path. Provider-generated classifier labels such as
				// "snapshot" and "working_data" must not become competing file
				// declarations and later surface as file-not-found results.
				if strings.TrimSpace(filepath.Ext(filepath.Base(path))) == "" {
					continue
				}
				files = append(files, path)
				fileSet[path] = true
				exact = true
			}
		}
		rules = append(rules, agentSaveArtifactDestinationRule{
			path: path, destination: destination, exact: exact,
		})
	}
	if len(files) > maxAgentSavedArtifacts {
		return nil, nil, fmt.Errorf("save_artifacts supports at most %d files", maxAgentSavedArtifacts)
	}
	return files, rules, nil
}

func parseAgentSaveArtifactFiles(raw any) ([]string, []map[string]any, error) {
	values := []any{}
	switch typed := raw.(type) {
	case []any:
		values = append(values, typed...)
	case []string:
		for _, value := range typed {
			values = append(values, value)
		}
	default:
		return nil, nil, errors.New("save_artifacts files must be an array")
	}
	if len(values) == 0 {
		return nil, nil, errors.New("save_artifacts files must not be empty")
	}
	files := make([]string, 0, len(values))
	failures := make([]map[string]any, 0)
	seen := make(map[string]struct{}, len(values))
	for index, rawValue := range values {
		value, ok := rawValue.(string)
		if !ok {
			failures = append(failures, agentSaveArtifactInputFailure(index, errors.New("save_artifacts paths must be strings")))
			continue
		}
		path, err := normalizeAgentSavedArtifactPath(value)
		if err != nil {
			failures = append(failures, agentSaveArtifactInputFailure(index, err))
			continue
		}
		if _, duplicate := seen[path]; duplicate {
			failures = append(failures, agentSaveArtifactFailure(path, errors.New("save_artifacts files must be unique")))
			continue
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}
	return files, failures, nil
}

func strictAgentArtifactStringList(raw any, field string, required bool) ([]string, error) {
	if raw == nil && !required {
		return []string{}, nil
	}
	values := []string{}
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("save_artifacts %s must contain only strings", field)
			}
			values = append(values, value)
		}
	case []string:
		values = append(values, typed...)
	default:
		return nil, fmt.Errorf("save_artifacts %s must be an array", field)
	}
	if required && len(values) == 0 {
		return nil, fmt.Errorf("save_artifacts %s must not be empty", field)
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized, err := normalizeAgentSavedArtifactPath(value)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	return result, nil
}

func normalizeAgentSavedArtifactPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) ||
		!utf8.ValidString(value) || len([]byte(value)) > 4096 || filepath.IsAbs(value) {
		return "", errors.New("save_artifacts paths must be bounded relative UTF-8 paths")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("save_artifacts paths must not contain control characters")
		}
	}
	cleaned := filepath.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", errors.New("save_artifacts paths must stay inside the authorized workspace")
	}
	return filepath.ToSlash(cleaned), nil
}

func (s *Server) resolveAgentSavedArtifactSource(
	access workspace.KernelFrameAccess,
	workspaceDir, relativePath string,
	records []workspace.ExecutionLogRecord,
	executionBindings map[string]string,
) (agentSavedArtifactSource, error) {
	workspaceRoot, err := canonicalHostDirectory(workspaceDir)
	if err != nil {
		return agentSavedArtifactSource{}, errors.New("save_artifacts workspace authority is unavailable")
	}
	workspaceSource, found, err := agentSavedArtifactSourceWithinRoot(workspaceRoot, filepath.FromSlash(relativePath))
	if err != nil {
		return agentSavedArtifactSource{}, err
	}
	if found {
		return workspaceSource, nil
	}

	candidates := map[string]agentSavedArtifactSource{}
	for _, record := range records {
		if !agentSavedArtifactExecutionAuthorized(record, access, workspaceDir, executionBindings) {
			continue
		}
		for _, write := range agentSavedArtifactFileWrites(record.FilesWritten) {
			logged := filepath.Clean(write.Path)
			if !filepath.IsAbs(logged) {
				// Kernel tool writes are logged as workspace-relative paths.
				// Join them to the authorized workspace so the fallback can
				// still locate a file that landed outside the canonical root
				// (for example through a bind mount or a sandbox commit).
				logged = filepath.Join(workspaceDir, logged)
			}
			if filepath.ToSlash(logged) != relativePath && !strings.HasSuffix(filepath.ToSlash(logged), "/"+relativePath) {
				continue
			}
			source, allowed, authorizeErr := s.agentSavedArtifactSourceAuthorized(access.UserID, workspaceRoot, logged)
			if authorizeErr != nil {
				return agentSavedArtifactSource{}, authorizeErr
			}
			if allowed {
				if existing, duplicate := candidates[source.absolutePath]; duplicate {
					source.close()
					candidates[source.absolutePath] = existing
				} else {
					candidates[source.absolutePath] = source
				}
			}
		}
	}
	if len(candidates) != 1 {
		for _, source := range candidates {
			source.close()
		}
		if len(candidates) == 0 {
			return agentSavedArtifactSource{}, os.ErrNotExist
		}
		return agentSavedArtifactSource{}, errAgentSavedArtifactPathAmbiguous
	}
	for _, source := range candidates {
		return source, nil
	}
	return agentSavedArtifactSource{}, os.ErrNotExist
}

func (s *Server) agentSavedArtifactSourceAuthorized(
	userID, workspaceRoot, candidate string,
) (agentSavedArtifactSource, bool, error) {
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return agentSavedArtifactSource{}, false, err
	}
	if hostPathWithin(workspaceRoot, candidate) {
		relative, err := filepath.Rel(workspaceRoot, candidate)
		if err != nil {
			return agentSavedArtifactSource{}, false, err
		}
		return agentSavedArtifactSourceWithinRoot(workspaceRoot, relative)
	}
	if s == nil || s.settingsStore == nil {
		return agentSavedArtifactSource{}, false, nil
	}
	grants, err := s.loadHostGrants(userID)
	if err != nil {
		return agentSavedArtifactSource{}, false, errors.New("save_artifacts host grants could not be verified")
	}
	var bestRoot string
	for _, grant := range grants {
		root, resolveErr := canonicalHostDirectory(grant.Path)
		if resolveErr == nil && hostPathWithin(root, candidate) && len(root) > len(bestRoot) {
			bestRoot = root
		}
	}
	if bestRoot == "" {
		return agentSavedArtifactSource{}, false, nil
	}
	relative, err := filepath.Rel(bestRoot, candidate)
	if err != nil {
		return agentSavedArtifactSource{}, false, err
	}
	return agentSavedArtifactSourceWithinRoot(bestRoot, relative)
}

func agentSavedArtifactSourceWithinRoot(root, relative string) (agentSavedArtifactSource, bool, error) {
	root, err := canonicalHostDirectory(root)
	if err != nil {
		return agentSavedArtifactSource{}, false, err
	}
	relative = filepath.Clean(relative)
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return agentSavedArtifactSource{}, false, errAgentSavedArtifactPathUnauthorized
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return agentSavedArtifactSource{}, false, err
	}
	if _, err := rootHandle.Stat(relative); err != nil {
		_ = rootHandle.Close()
		if errors.Is(err, os.ErrNotExist) {
			return agentSavedArtifactSource{}, false, nil
		}
		return agentSavedArtifactSource{}, false, errors.Join(errAgentSavedArtifactPathUnauthorized, err)
	}
	return agentSavedArtifactSource{
		root: root, rootHandle: rootHandle, relativePath: relative,
		absolutePath: filepath.Clean(filepath.Join(root, relative)),
	}, true, nil
}

func (s *Server) resolveAgentSavedArtifactVersion(reference string) (workspace.Artifact, workspace.ArtifactVersion, bool, error) {
	artifact, version, found, err := s.workspaceStore.GetArtifactVersionMetadata(reference)
	if err != nil || found {
		return artifact, version, found, err
	}
	return s.workspaceStore.GetCurrentArtifactVersionMetadata(reference)
}

func agentSavedArtifactExecutionIDs(
	records []workspace.ExecutionLogRecord,
	access workspace.KernelFrameAccess,
	workspaceDir, resolvedPath, contentSHA string,
	executionBindings map[string]string,
) []string {
	type candidate struct {
		id        string
		cellIndex int
	}
	resolvedPath = filepath.Clean(resolvedPath)
	matches := []candidate{}
	for _, record := range records {
		if !agentSavedArtifactExecutionAuthorized(record, access, workspaceDir, executionBindings) {
			continue
		}
		for _, write := range agentSavedArtifactFileWrites(record.FilesWritten) {
			path := filepath.Clean(write.Path)
			if !filepath.IsAbs(path) {
				path = filepath.Join(workspaceDir, path)
			}
			if path == resolvedPath && strings.EqualFold(strings.TrimSpace(write.SHA256), contentSHA) {
				matches = append(matches, candidate{id: record.ID, cellIndex: record.CellIndex})
				break
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].cellIndex != matches[j].cellIndex {
			return matches[i].cellIndex > matches[j].cellIndex
		}
		return matches[i].id > matches[j].id
	})
	if len(matches) == 0 {
		return nil
	}
	return []string{matches[0].id}
}

func agentSavedArtifactExecutionAuthorized(
	record workspace.ExecutionLogRecord,
	access workspace.KernelFrameAccess,
	workspaceDir string,
	executionBindings map[string]string,
) bool {
	recordID, recordKernelID := strings.TrimSpace(record.ID), strings.TrimSpace(record.KernelID)
	if record.FrameID != access.Frame.ID || recordID == "" || recordKernelID == "" ||
		strings.TrimSpace(record.KernelKind) == "" || strings.TrimSpace(record.Language) == "" ||
		strings.TrimSpace(record.CondaEnv) == "" {
		return false
	}
	if operationKernelID, found := executionBindings[recordID]; found {
		return strings.TrimSpace(operationKernelID) == recordKernelID
	}
	expected, err := kernelruntime.StableSessionID(kernelruntime.SessionSpec{
		OwnerID: access.UserID, ProjectID: access.Frame.ProjectID,
		FrameID: access.Frame.ID, FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameID: access.Frame.RootFrameID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName: access.Frame.AgentName, DelegateName: access.DelegateName,
		KernelKind: strings.TrimSpace(record.KernelKind), Language: strings.TrimSpace(record.Language),
		Environment: strings.TrimSpace(record.CondaEnv), WorkspaceDir: workspaceDir,
	})
	return err == nil && expected == recordKernelID
}

func agentSavedArtifactFileWrites(raw any) []kernelruntime.FileWrite {
	switch typed := raw.(type) {
	case []kernelruntime.FileWrite:
		result := make([]kernelruntime.FileWrite, 0, len(typed))
		for _, write := range typed {
			if strings.TrimSpace(write.PolicyCode) == "" {
				result = append(result, write)
			}
		}
		return result
	case []any:
		result := make([]kernelruntime.FileWrite, 0, len(typed))
		for _, item := range typed {
			mapping, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(stringValue(mapping["policy_code"])) != "" {
				continue
			}
			result = append(result, kernelruntime.FileWrite{
				Path: stringValue(mapping["path"]), SHA256: stringValue(mapping["sha256"]),
			})
		}
		return result
	default:
		return nil
	}
}

func detectAgentSavedArtifactMIME(path string, snapshot *os.File) string {
	if inferred := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))); inferred != "" {
		if mediaType, _, err := mime.ParseMediaType(inferred); err == nil && strings.TrimSpace(mediaType) != "" {
			return mediaType
		}
	}
	if snapshot == nil {
		return "application/octet-stream"
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return "application/octet-stream"
	}
	buffer := make([]byte, 512)
	count, _ := snapshot.Read(buffer)
	_, _ = snapshot.Seek(0, io.SeekStart)
	return detectArtifactMIME(buffer[:count])
}

// validateAgentSavedArtifactJSON accepts machine-readable user deliverables
// while preventing malformed .json/.jsonl files from becoming canonical
// artifacts. The snapshot has already passed the normal path, authorization,
// and snapshot checks; this function never interprets JSON as runtime protocol.
func validateAgentSavedArtifactJSON(ctx context.Context, path string, snapshot *os.File) (resultErr error) {
	extension := strings.ToLower(filepath.Ext(strings.TrimSpace(path)))
	if extension != ".json" && extension != ".jsonl" {
		return nil
	}
	if snapshot == nil {
		return errAgentSavedArtifactJSONInvalid
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return err
	}
	defer func() { _, err := snapshot.Seek(0, io.SeekStart); resultErr = errors.Join(resultErr, err) }()
	if ctx == nil {
		ctx = context.Background()
	}
	reader := bufio.NewReader(&contextReader{ctx: ctx, reader: snapshot})
	if extension == ".json" {
		decoder := json.NewDecoder(reader)
		token, err := decoder.Token()
		if err == nil {
			err = skipRunnerSourceJSONValue(decoder, token, func(string) {})
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.Join(errAgentSavedArtifactJSONInvalid, err)
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w", errAgentSavedArtifactJSONInvalid)
		}
	} else {
		lineNumber := 0
		valueCount := 0
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				lineNumber++
				line = bytes.TrimSpace(line)
				if len(line) == 0 {
					if errors.Is(err, io.EOF) {
						break
					}
					continue
				}
				if !json.Valid(line) {
					return fmt.Errorf("%w at line %d", errAgentSavedArtifactJSONInvalid, lineNumber)
				}
				valueCount++
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
		}
		if valueCount == 0 {
			return errAgentSavedArtifactJSONInvalid
		}
	}
	return nil
}

func validateAgentSavedArtifactJSONBytes(path string, data []byte) error {
	if strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".json") {
		if !json.Valid(data) {
			return errAgentSavedArtifactJSONInvalid
		}
		return nil
	}
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".jsonl") {
		return nil
	}
	lineNumber := 0
	valueCount := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		lineNumber++
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return fmt.Errorf("%w at line %d", errAgentSavedArtifactJSONInvalid, lineNumber)
		}
		valueCount++
	}
	if valueCount == 0 {
		return errAgentSavedArtifactJSONInvalid
	}
	return nil
}

func agentSaveArtifactInputFailure(index int, err error) map[string]any {
	failure := agentSaveArtifactFailure("", err)
	if index >= 0 {
		failure["input_index"] = index
	}
	return failure
}

func agentSaveArtifactFailure(path string, err error) map[string]any {
	code := "save_failed"
	if errors.Is(err, os.ErrNotExist) {
		code = "file_not_found"
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = "cancelled"
	} else if errors.Is(err, errAgentSavedArtifactNotRegular) {
		code = "not_regular_file"
	} else if errors.Is(err, errAgentSavedArtifactPathUnauthorized) {
		code = "path_not_authorized"
	} else if errors.Is(err, errAgentSavedArtifactPathAmbiguous) {
		code = "path_ambiguous"
	} else if errors.Is(err, errAgentSavedArtifactVersionInvalid) {
		code = "invalid_version_reference"
	} else if errors.Is(err, errAgentFileContentTypeMismatch) {
		code = "file_content_type_mismatch"
	} else if errors.Is(err, errAgentFileStructureInvalid) {
		code = "invalid_file_structure"
	} else if errors.Is(err, errAgentSavedArtifactJSONInvalid) {
		code = "invalid_json_artifact"
	} else if errors.Is(err, errAgentSavedArtifactDelimitedInvalid) {
		code = "invalid_delimited_artifact"
	} else if errors.Is(err, errAgentSavedArtifactTemplateUnresolved) {
		code = "unresolved_template_marker"
	} else if errors.Is(err, errAgentSavedArtifactProjection) {
		code = "projection_failed"
	} else if errors.Is(err, errAgentSavedArtifactSourceChanged) {
		code = "source_changed_during_save"
	} else if errors.Is(err, runtimecontrol.ErrInsufficientDiskSpace) {
		code = "insufficient_disk_space"
	} else if errors.Is(err, errInvalidScientificArtifact) {
		code = "invalid_scientific_artifact"
	} else if errors.Is(err, errScientificArtifactValidationUnavailable) {
		code = "scientific_validator_unavailable"
	} else if errors.Is(err, errAgentSavedArtifactEvidenceUnavailable) {
		code = "evidence_validation_unavailable"
	} else if errors.Is(err, errAgentSavedArtifactTextInvalid) {
		code = "invalid_text_encoding"
	} else if errors.Is(err, errAgentSavedArtifactRawExecutionLineage) {
		code = "raw_execution_lineage_unavailable"
	} else {
		var evidenceErr *agentSavedArtifactEvidenceError
		if errors.As(err, &evidenceErr) {
			failure := agentSaveArtifactFailurePayload(path, "unsupported_evidence_references", evidenceErr.References, evidenceErr.Total)
			if len(evidenceErr.AvailableReferences) > 0 {
				failure["available_evidence_references"] = append([]string(nil), evidenceErr.AvailableReferences...)
				failure["available_evidence_reference_count"] = evidenceErr.AvailableTotal
				if evidenceErr.AvailableTotal > len(evidenceErr.AvailableReferences) {
					failure["available_evidence_references_truncated"] = true
				}
			}
			return failure
		}
		var tooLarge *workspace.ArtifactContentTooLargeError
		if errors.As(err, &tooLarge) {
			code = "file_too_large"
		}
	}
	if code == "save_failed" && strings.Contains(err.Error(), "relative UTF-8 paths") {
		code = "path_must_be_relative"
	}
	failure := agentSaveArtifactFailurePayload(path, code, nil, 0)
	if code == "file_content_type_mismatch" || code == "invalid_file_structure" {
		failure["validation_detail"] = err.Error()
	}
	if code == "invalid_delimited_artifact" || code == "invalid_json_artifact" {
		failure["validation_detail"] = strings.TrimSpace(err.Error())
	}
	addScientificArtifactFailureFeedback(failure, err)
	return failure
}

func agentSaveArtifactFailurePayload(path, code string, unsupported []string, unsupportedTotal int) map[string]any {
	retryable := false
	recovery := map[string]any{
		"action": "fix_the_failed_file_then_retry_only_that_file",
		"retry":  "do_not_retry_unchanged_input",
	}
	switch code {
	case "invalid_scientific_artifact":
		recovery["action"] = "inspect_the_parser_diagnosis_and_repair_the_file_before_retrying"
		recovery["diagnostic"] = "validation_code identifies the parser failure; validation counts summarize records, not line numbers; validate the complete repaired file"
	case "scientific_validator_unavailable":
		recovery["action"] = "restore_the_managed_scientific_validator_before_retrying"
		recovery["data_policy"] = "validator failure is not evidence of invalid file content; preserve the file"
	case "source_changed_during_save":
		retryable = true
		recovery["action"] = "wait_for_the_producer_to_finish_then_save_the_stable_file"
	case "insufficient_disk_space":
		recovery["action"] = "restore_available_storage_then_retry_only_the_unsaved_file"
		recovery["data_policy"] = "retain_existing_artifacts_and_do_not_delete_user_data_automatically"
	case "path_must_be_relative":
		recovery["action"] = "replace_with_workspace_relative_path"
		recovery["files"] = "use a relative path inside the current workspace"
	case "invalid_version_reference":
		recovery["action"] = "save_same_relative_path_without_version_of"
		recovery["version_of"] = "omit_for_same_path"
	case "file_content_type_mismatch", "invalid_file_structure":
		retryable = true
		recovery["action"] = "read_the_file_and_restore_valid_content_before_saving"
	case "unsupported_evidence_references":
		recovery["action"] = "review_claim_source_binding_if_material_to_output_quality"
		recovery["evidence"] = "retain_real_source_identities_and_distinguish_source_existence_from_claim_support"
		recovery["receipt_format"] = "a successful logical-task source result whose structured identifier equals each cited identifier"
		recovery["available_evidence_references"] = "use the bounded top-level list when present; otherwise acquire or bind a source only when the model judges it material"
		recovery["uniprot_citation_format"] = "UniProt <accession>, only after a successful source receipt returns that uniprot_id or accession"
	case "invalid_json_artifact":
		recovery["action"] = "write_valid_json_or_jsonl_then_retry_only_that_file"
		recovery["format_policy"] = ".json must contain exactly one JSON value; .jsonl must contain one JSON value per non-empty line"
	case "invalid_delimited_artifact":
		retryable = true
		recovery["action"] = "write_consistent_csv_or_tsv_then_retry_only_that_file"
		recovery["writer"] = "use a standard CSV or TSV writer for the complete table instead of manually escaping a multiline delimited string"
		recovery["format_policy"] = "quote fields that contain delimiters, quotes, or newlines, and keep every record aligned to the header field count"
		recovery["diagnostic"] = "use validation_detail to repair the first reported malformed record, then validate the complete file before retrying"
	case "unresolved_template_marker":
		recovery["action"] = "materialize_the_template_with_real_values_then_retry_only_that_file"
		recovery["template_policy"] = "saved deliverables must contain final rendered bytes; replace Jinja, Mustache, or other non-artifact template markers with actual computed content"
	case "raw_execution_lineage_unavailable":
		recovery["action"] = "rerun_the_producing_tool_then_save_the_exact_logged_file"
		recovery["lineage"] = "raw execution artifacts require a current-frame file-write receipt with the same path and SHA-256"
	}
	failure := map[string]any{
		"path":      path,
		"code":      code,
		"retryable": retryable,
		"recovery":  recovery,
	}
	if len(unsupported) > 0 {
		failure["unsupported_evidence_references"] = append([]string(nil), unsupported...)
		if unsupportedTotal > len(unsupported) {
			failure["unsupported_evidence_references_truncated"] = true
			failure["unsupported_evidence_reference_count"] = unsupportedTotal
		}
	}
	return failure
}
