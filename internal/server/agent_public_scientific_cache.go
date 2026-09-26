package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"mime"
	"sort"
	"strings"
	"sync"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/toolcontract"
)

const agentPublicScientificDownloadCacheNamespace = "public-scientific-download-cache"

type agentPublicScientificDownloadCacheRecord struct {
	OwnerUserID string
	ArtifactID  string
	VersionID   string
	ProjectID   string
	SourceURL   string
	Filename    string
	ContentType string
	SizeBytes   int64
	SHA256      string
}

type agentPublicScientificCachedContent struct {
	record   agentPublicScientificDownloadCacheRecord
	artifact workspace.Artifact
	version  workspace.ArtifactVersion
	content  workspace.ArtifactContentReader
}

type agentPublicScientificDownloadFlight struct {
	done chan struct{}
}

type agentPublicScientificProjectionCandidate struct {
	record    agentPublicScientificDownloadCacheRecord
	updatedAt time.Time
}

func agentPublicScientificDownloadCacheKey(ownerUserID, sourceURL, filename, contentSHA256 string) string {
	ownerUserID = strings.TrimSpace(ownerUserID)
	sourceURL = strings.TrimSpace(sourceURL)
	filename = strings.TrimSpace(filename)
	contentSHA256 = strings.TrimSpace(contentSHA256)
	if ownerUserID == "" || sourceURL == "" || filename == "" ||
		!toolcontract.ValidLowerHexSHA256(contentSHA256) {
		return ""
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"public-scientific-download-cache-v1", ownerUserID, sourceURL, filename, contentSHA256,
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func (record agentPublicScientificDownloadCacheRecord) value() map[string]any {
	return map[string]any{
		"owner_user_id": record.OwnerUserID,
		"artifact_id":   record.ArtifactID,
		"version_id":    record.VersionID,
		"project_id":    record.ProjectID,
		"source_url":    record.SourceURL,
		"filename":      record.Filename,
		"content_type":  record.ContentType,
		"size_bytes":    record.SizeBytes,
		"sha256":        record.SHA256,
	}
}

func agentPublicScientificDownloadCacheRecordValue(value any) (agentPublicScientificDownloadCacheRecord, bool) {
	recordValue := mapValue(value)
	record := agentPublicScientificDownloadCacheRecord{
		OwnerUserID: strings.TrimSpace(stringValue(recordValue["owner_user_id"])),
		ArtifactID:  strings.TrimSpace(stringValue(recordValue["artifact_id"])),
		VersionID:   strings.TrimSpace(stringValue(recordValue["version_id"])),
		ProjectID:   strings.TrimSpace(stringValue(recordValue["project_id"])),
		SourceURL:   strings.TrimSpace(stringValue(recordValue["source_url"])),
		Filename:    strings.TrimSpace(stringValue(recordValue["filename"])),
		ContentType: strings.TrimSpace(stringValue(recordValue["content_type"])),
		SizeBytes:   numberValue(recordValue["size_bytes"]),
		SHA256:      strings.TrimSpace(stringValue(recordValue["sha256"])),
	}
	valid := record.OwnerUserID != "" && record.ArtifactID != "" && record.VersionID != "" &&
		record.ProjectID != "" && record.SourceURL != "" && record.Filename != "" &&
		record.ContentType != "" && record.SizeBytes > 0 && toolcontract.ValidLowerHexSHA256(record.SHA256)
	return record, valid
}

func (s *Server) indexAgentPublicScientificDownload(
	stream transcriptstore.Stream,
	artifact workspace.Artifact,
	version workspace.ArtifactVersion,
	request agentPublicScientificFileRequest,
) {
	if s == nil || s.runtimeStore == nil || strings.TrimSpace(stream.OwnerID) == "" ||
		strings.TrimSpace(request.SourceURL) == "" || strings.TrimSpace(request.Filename) == "" ||
		version.SizeBytes <= 0 || !toolcontract.ValidLowerHexSHA256(version.ContentSHA256) {
		return
	}
	if request.ExpectedSHA256 != "" && request.ExpectedSHA256 != version.ContentSHA256 {
		log.Printf("download_public_scientific_file cache index skipped for artifact %s: checksum mismatch", artifact.ID)
		return
	}
	record := agentPublicScientificDownloadCacheRecord{
		OwnerUserID: stream.OwnerID,
		ArtifactID:  artifact.ID,
		VersionID:   version.ID,
		ProjectID:   artifact.ProjectID,
		SourceURL:   request.SourceURL,
		Filename:    request.Filename,
		ContentType: artifact.Kind,
		SizeBytes:   version.SizeBytes,
		SHA256:      version.ContentSHA256,
	}
	if err := s.persistAgentPublicScientificDownloadCacheRecord(record); err != nil {
		log.Printf("download_public_scientific_file cache index failed for artifact %s: %v", artifact.ID, err)
	}
}

func (s *Server) persistAgentPublicScientificDownloadCacheRecord(
	record agentPublicScientificDownloadCacheRecord,
) error {
	if s == nil || s.runtimeStore == nil {
		return nil
	}
	key := agentPublicScientificDownloadCacheKey(
		record.OwnerUserID, record.SourceURL, record.Filename, record.SHA256,
	)
	if key == "" {
		return errors.New("public scientific download cache record is invalid")
	}
	_, err := s.runtimeStore.Set(agentPublicScientificDownloadCacheNamespace, key, record.value())
	return err
}

func (s *Server) reuseOrCoordinateCachedAgentPublicScientificFileDownload(
	ctx context.Context,
	workspaceDir string,
	stream transcriptstore.Stream,
	claim transcriptstore.RunnerClaim,
	sourceEventID int64,
	toolCallID string,
	artifactID string,
	request agentPublicScientificFileRequest,
) (map[string]any, bool, func(), error) {
	cacheKey := agentPublicScientificDownloadCacheKey(
		stream.OwnerID, request.SourceURL, request.Filename, request.ExpectedSHA256,
	)
	if cacheKey == "" {
		return nil, false, nil, nil
	}
	for {
		reused, found, err := s.reuseCachedAgentPublicScientificFileDownload(
			ctx, workspaceDir, stream, claim, sourceEventID, toolCallID, artifactID, request,
		)
		if err != nil || found {
			return reused, found, nil, err
		}
		leader, wait, release := s.beginAgentPublicScientificDownloadFlight(cacheKey)
		if leader {
			// Close the lookup-to-flight race: a prior leader may have populated
			// the durable cache just before this caller registered the flight.
			reused, found, err = s.reuseCachedAgentPublicScientificFileDownload(
				ctx, workspaceDir, stream, claim, sourceEventID, toolCallID, artifactID, request,
			)
			if err != nil || found {
				release()
				return reused, found, nil, err
			}
			return nil, false, release, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, nil, ctx.Err()
		case <-wait:
		}
	}
}

func (s *Server) beginAgentPublicScientificDownloadFlight(cacheKey string) (bool, <-chan struct{}, func()) {
	s.publicScientificDownloadCacheMu.Lock()
	if s.publicScientificDownloadCacheFlights == nil {
		s.publicScientificDownloadCacheFlights = make(map[string]*agentPublicScientificDownloadFlight)
	}
	if existing := s.publicScientificDownloadCacheFlights[cacheKey]; existing != nil {
		s.publicScientificDownloadCacheMu.Unlock()
		return false, existing.done, nil
	}
	flight := &agentPublicScientificDownloadFlight{done: make(chan struct{})}
	s.publicScientificDownloadCacheFlights[cacheKey] = flight
	s.publicScientificDownloadCacheMu.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.publicScientificDownloadCacheMu.Lock()
			if s.publicScientificDownloadCacheFlights[cacheKey] == flight {
				delete(s.publicScientificDownloadCacheFlights, cacheKey)
				close(flight.done)
			}
			s.publicScientificDownloadCacheMu.Unlock()
		})
	}
	return true, nil, release
}

func (s *Server) reuseCachedAgentPublicScientificFileDownload(
	ctx context.Context,
	workspaceDir string,
	stream transcriptstore.Stream,
	claim transcriptstore.RunnerClaim,
	sourceEventID int64,
	toolCallID string,
	artifactID string,
	request agentPublicScientificFileRequest,
) (map[string]any, bool, error) {
	cached, found := s.findAgentPublicScientificCachedContent(stream.OwnerID, request)
	if !found {
		return nil, false, nil
	}
	defer cached.content.Close()
	if err := ensureAgentWorkspaceDownloadTarget(
		ctx, workspaceDir, request.Filename, cached.version.SizeBytes, cached.version.ContentSHA256,
		request.maximumBytes(),
	); err != nil {
		return nil, true, agentPublicScientificWorkspaceDownloadError(err)
	}
	reportAgentPublicScientificDownloadProgress(
		ctx, "reusing_cached_download", cached.version.SizeBytes, cached.version.SizeBytes, nil,
	)
	mutationDigest := sha256.Sum256([]byte(fmt.Sprintf(
		"reuse-cached-public-scientific-file-v1:%s:%d:%s:%s:%s:%s",
		stream.UID, sourceEventID, toolCallID, request.Filename, request.SourceURL, cached.version.ID,
	)))
	reusedArtifact, reusedVersion, err := s.workspaceStore.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(ctx, "reuse-cached-public-scientific-file-"+hex.EncodeToString(mutationDigest[:])),
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: stream.ProjectID, Name: request.Filename,
			ContentType: cached.artifact.Kind, Content: cached.content, MaxBytes: request.maximumBytes(),
			ParentVersionID: request.RecoveryParentVersionID,
			CreatedBy:       claim.RunnerID, RootFrameID: stream.RootFrameID, FrameID: stream.FrameID,
			ProvenanceSourceID: cached.version.ID, ReadSourceProjectID: cached.artifact.ProjectID,
			TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
				StreamUID: stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: sourceEventID, Relation: "consumed",
				ReuseCurrentVersionIfUnchanged: true,
			},
			Language: agentPublicScientificArtifactLanguage, IsIntermediate: true,
		},
		stream.OwnerID,
	)
	if err != nil {
		return nil, true, err
	}
	if reusedVersion.SizeBytes != cached.version.SizeBytes ||
		reusedVersion.ContentSHA256 != cached.version.ContentSHA256 ||
		reusedVersion.ContentSHA256 != request.ExpectedSHA256 {
		return nil, true, errAgentPublicScientificFileConflict
	}
	replayArtifact, replayVersion, replayContent, replayFound, err :=
		s.workspaceStore.OpenArtifactVersionContentForOwner(reusedVersion.ID, stream.OwnerID)
	if err != nil || !replayFound || replayArtifact.ID != reusedArtifact.ID || replayVersion.ID != reusedVersion.ID {
		return nil, true, errAgentPublicScientificFileAuthority
	}
	defer replayContent.Close()
	if err := publishAgentWorkspaceDownloadFile(
		ctx, workspaceDir, request.Filename, replayContent, reusedVersion.SizeBytes, reusedVersion.ContentSHA256,
		request.maximumBytes(),
	); err != nil {
		return nil, true, agentPublicScientificWorkspaceDownloadError(err)
	}
	reportAgentPublicScientificDownloadProgress(
		ctx, "download_ready", reusedVersion.SizeBytes, reusedVersion.SizeBytes, nil,
	)
	result := s.agentPublicScientificFileResult(stream, reusedArtifact, reusedVersion, request)
	result["reused"] = true
	result["reuse_scope"] = "owner_content_addressed_cache"
	download := mapValue(result["download"])
	download["reused"] = true
	download["reused_from_artifact_id"] = cached.artifact.ID
	download["reused_from_version_id"] = cached.version.ID
	return result, true, nil
}

func (s *Server) findAgentPublicScientificCachedContent(
	ownerUserID string,
	request agentPublicScientificFileRequest,
) (agentPublicScientificCachedContent, bool) {
	if s == nil || s.runtimeStore == nil || s.workspaceStore == nil || request.ExpectedSHA256 == "" {
		return agentPublicScientificCachedContent{}, false
	}
	cacheKey := agentPublicScientificDownloadCacheKey(
		ownerUserID, request.SourceURL, request.Filename, request.ExpectedSHA256,
	)
	if cacheKey == "" {
		return agentPublicScientificCachedContent{}, false
	}
	if entry, found, err := s.runtimeStore.Get(agentPublicScientificDownloadCacheNamespace, cacheKey); err != nil {
		log.Printf("download_public_scientific_file cache lookup failed: %v", err)
	} else if found {
		if record, valid := agentPublicScientificDownloadCacheRecordValue(entry.Value); valid {
			if cached, ok := s.openAgentPublicScientificCachedContent(record, ownerUserID, request); ok {
				return cached, true
			}
		}
	}
	return s.backfillAgentPublicScientificCachedContent(ownerUserID, request, cacheKey)
}

func (s *Server) openAgentPublicScientificCachedContent(
	record agentPublicScientificDownloadCacheRecord,
	ownerUserID string,
	request agentPublicScientificFileRequest,
) (agentPublicScientificCachedContent, bool) {
	if record.OwnerUserID != ownerUserID || record.SourceURL != request.SourceURL ||
		record.Filename != request.Filename || record.SHA256 != request.ExpectedSHA256 ||
		(request.RegisteredSize > 0 && record.SizeBytes != request.RegisteredSize) ||
		!agentPublicScientificCachedContentTypeAllowed(record.ContentType, request.AcceptedTypes) {
		return agentPublicScientificCachedContent{}, false
	}
	artifact, version, content, found, err :=
		s.workspaceStore.OpenArtifactVersionContentForOwner(record.VersionID, ownerUserID)
	if err != nil || !found {
		if err != nil && !errors.Is(err, workspace.ErrArtifactContentPruned) {
			log.Printf("download_public_scientific_file cached content unavailable for version %s: %v", record.VersionID, err)
		}
		return agentPublicScientificCachedContent{}, false
	}
	if artifact.ID != record.ArtifactID || artifact.ProjectID != record.ProjectID ||
		artifact.Name != request.Filename || artifact.Kind != record.ContentType ||
		version.ArtifactID != artifact.ID || version.ID != record.VersionID ||
		version.SizeBytes != record.SizeBytes || version.ContentSHA256 != record.SHA256 {
		_ = content.Close()
		return agentPublicScientificCachedContent{}, false
	}
	return agentPublicScientificCachedContent{
		record: record, artifact: artifact, version: version, content: content,
	}, true
}

func (s *Server) backfillAgentPublicScientificCachedContent(
	ownerUserID string,
	request agentPublicScientificFileRequest,
	cacheKey string,
) (agentPublicScientificCachedContent, bool) {
	entries, err := s.runtimeStore.List(artifactRuntimeNamespace)
	if err != nil {
		log.Printf("download_public_scientific_file cache backfill failed: %v", err)
		return agentPublicScientificCachedContent{}, false
	}
	candidates := make([]agentPublicScientificProjectionCandidate, 0)
	for _, entry := range entries {
		projection := mapValue(entry.Value)
		download := mapValue(projection["publicScientificDownload"])
		if stringValue(download["source_url"]) != request.SourceURL ||
			stringValue(download["filename"]) != request.Filename ||
			stringValue(download["sha256"]) != request.ExpectedSHA256 {
			continue
		}
		sizeBytes := numberValue(download["size_bytes"])
		if sizeBytes <= 0 || (request.RegisteredSize > 0 && sizeBytes != request.RegisteredSize) {
			continue
		}
		contentType := strings.TrimSpace(stringValue(download["content_type"]))
		if contentType == "" {
			contentType = strings.TrimSpace(stringValue(projection["mimeType"]))
		}
		if !agentPublicScientificCachedContentTypeAllowed(contentType, request.AcceptedTypes) {
			continue
		}
		candidate := agentPublicScientificDownloadCacheRecord{
			OwnerUserID: ownerUserID,
			ArtifactID:  strings.TrimSpace(stringValue(projection["artifactId"])),
			VersionID:   strings.TrimSpace(stringValue(projection["versionId"])),
			SourceURL:   request.SourceURL,
			Filename:    request.Filename,
			ContentType: contentType,
			SizeBytes:   sizeBytes,
			SHA256:      request.ExpectedSHA256,
		}
		if candidate.ArtifactID == "" || candidate.VersionID == "" {
			continue
		}
		candidates = append(candidates, agentPublicScientificProjectionCandidate{
			record: candidate, updatedAt: entry.UpdatedAt,
		})
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		return candidates[left].updatedAt.After(candidates[right].updatedAt)
	})
	for _, candidate := range candidates {
		artifact, version, content, found, openErr :=
			s.workspaceStore.OpenArtifactVersionContentForOwner(candidate.record.VersionID, ownerUserID)
		if openErr != nil || !found {
			continue
		}
		if artifact.ID != candidate.record.ArtifactID || artifact.Name != request.Filename ||
			artifact.Kind != candidate.record.ContentType || version.ArtifactID != artifact.ID ||
			version.ID != candidate.record.VersionID || version.SizeBytes != candidate.record.SizeBytes ||
			version.ContentSHA256 != candidate.record.SHA256 {
			_ = content.Close()
			continue
		}
		candidate.record.ProjectID = artifact.ProjectID
		if _, err := s.runtimeStore.Set(
			agentPublicScientificDownloadCacheNamespace, cacheKey, candidate.record.value(),
		); err != nil {
			log.Printf("download_public_scientific_file cache backfill index failed for artifact %s: %v", artifact.ID, err)
		}
		return agentPublicScientificCachedContent{
			record: candidate.record, artifact: artifact, version: version, content: content,
		}, true
	}
	return agentPublicScientificCachedContent{}, false
}

func agentPublicScientificCachedContentTypeAllowed(contentType string, acceptedTypes []string) bool {
	contentType = strings.TrimSpace(contentType)
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = strings.ToLower(parsed)
	} else {
		contentType = strings.ToLower(contentType)
	}
	for _, accepted := range acceptedTypes {
		accepted = strings.TrimSpace(accepted)
		if parsed, _, err := mime.ParseMediaType(accepted); err == nil {
			accepted = parsed
		}
		if strings.EqualFold(contentType, accepted) {
			return true
		}
	}
	return false
}
