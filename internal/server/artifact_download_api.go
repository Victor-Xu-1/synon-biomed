package server

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	workspace "synon-go/internal/persistence/workspace"
)

const maxSelectedArtifactDownloads = 200

var inlineArtifactVersionContentType = regexp.MustCompile(`(?i)^(image/(png|jpeg|gif|webp|bmp|avif|x-icon|vnd\.microsoft\.icon|tiff)|video/|audio/|text/plain$|application/pdf$|font/)`)

type downloadableArtifact struct {
	Artifact workspace.Artifact
	Version  workspace.ArtifactVersion
}

func (s *Server) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	store, ok := s.attachmentStore(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/artifacts/"))
	if len(segments) >= 4 && segments[1] == "versions" && segments[3] == "archive" {
		s.handleArtifactArchive(w, r, store, userID, segments)
		return
	}
	if len(segments) >= 4 && segments[1] == "versions" {
		s.handleArtifactAnnotationOperation(w, r, store, userID, segments)
		return
	}
	if len(segments) == 3 && segments[1] == "versions" {
		s.handleExactArtifactVersionDownload(w, r, store, userID, segments[0], segments[2])
		return
	}
	if len(segments) == 2 && segments[1] == "script-bundle" {
		s.handleScriptBundle(w, r, store, userID, segments[0])
		return
	}
	if len(segments) != 1 {
		writeAttachmentError(w, http.StatusNotFound, "artifact download endpoint not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	artifactID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(artifactID) == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid artifact id")
		return
	}
	if s.serveRunnerLargeToolResultCurrent(w, r, userID, artifactID) {
		return
	}
	metadata, found, err := store.GetCompatibilityCurrentArtifactDownloadMetadata(r.Context(), userID, artifactID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found {
		writeAttachmentError(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		return
	}
	if queryBool(r, "include_metadata") {
		serveArtifactDownloadMetadata(w, metadata)
		return
	}
	artifact, version, content, found, err := store.OpenCurrentArtifactContent(artifactID)
	if err != nil || !found {
		if err != nil {
			writeAttachmentStoreError(w, err)
		} else {
			writeAttachmentError(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		}
		return
	}
	defer content.Close()
	filename := artifact.Name
	if override := strings.TrimSpace(r.URL.Query().Get("filename")); override != "" {
		filename = override
	}
	serveArtifactVersion(w, r, artifact, version, filename, metadata.ContentType, "attachment", content)
}

func (s *Server) handleExactArtifactVersionDownload(
	w http.ResponseWriter,
	r *http.Request,
	store *workspace.Store,
	userID, encodedArtifactID, encodedVersionID string,
) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	artifactID, artifactErr := url.PathUnescape(encodedArtifactID)
	versionID, versionErr := url.PathUnescape(encodedVersionID)
	artifactID, versionID = strings.TrimSpace(artifactID), strings.TrimSpace(versionID)
	if artifactErr != nil || versionErr != nil || artifactID == "" || versionID == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid artifact version identity")
		return
	}
	if s.serveRunnerLargeToolResultEvidence(w, r, userID, artifactID, versionID) {
		return
	}
	metadata, found, err := store.GetCompatibilityVersionDownloadMetadata(r.Context(), userID, versionID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found || metadata.ArtifactID != artifactID {
		writeAttachmentError(w, http.StatusNotFound, "Artifact version unavailable")
		return
	}
	if queryBool(r, "include_metadata") {
		serveArtifactDownloadMetadata(w, metadata)
		return
	}
	artifact, version, content, found, err := store.OpenArtifactVersionContent(versionID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found || artifact.ID != artifactID || version.ID != versionID {
		if content != nil {
			_ = content.Close()
		}
		writeAttachmentError(w, http.StatusNotFound, "Artifact version unavailable")
		return
	}
	defer content.Close()
	filename := artifact.Name
	if override := strings.TrimSpace(r.URL.Query().Get("filename")); override != "" {
		filename = override
	}
	disposition := "attachment"
	if inlineArtifactVersionContentType.MatchString(strings.TrimSpace(metadata.ContentType)) {
		disposition = "inline"
	}
	serveArtifactVersion(w, r, artifact, version, filename, metadata.ContentType, disposition, content)
}

func (s *Server) handleArtifactVersionDownload(w http.ResponseWriter, r *http.Request) {
	store, ok := s.attachmentStore(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/artifacts/versions/"))
	if len(segments) != 1 {
		writeAttachmentError(w, http.StatusNotFound, "artifact version endpoint not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	versionID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(versionID) == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid artifact version id")
		return
	}
	if s.serveRunnerLargeToolResultEvidence(w, r, userID, "", versionID) {
		return
	}
	metadata, found, err := store.GetCompatibilityVersionDownloadMetadata(r.Context(), userID, versionID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found {
		writeAttachmentError(w, http.StatusNotFound, "Version "+versionID+" not found")
		return
	}
	if queryBool(r, "include_metadata") {
		serveArtifactDownloadMetadata(w, metadata)
		return
	}
	artifact, version, content, found, err := store.OpenArtifactVersionContent(versionID)
	if err != nil || !found {
		if err != nil {
			writeAttachmentStoreError(w, err)
		} else {
			writeAttachmentError(w, http.StatusNotFound, "Version "+versionID+" not found")
		}
		return
	}
	defer content.Close()
	filename := artifact.Name
	if override := strings.TrimSpace(r.URL.Query().Get("filename")); override != "" {
		filename = override
	}
	disposition := "attachment"
	if inlineArtifactVersionContentType.MatchString(strings.TrimSpace(metadata.ContentType)) {
		disposition = "inline"
	}
	serveArtifactVersion(w, r, artifact, version, filename, metadata.ContentType, disposition, content)
}

func serveArtifactVersion(w http.ResponseWriter, r *http.Request, artifact workspace.Artifact, version workspace.ArtifactVersion, filename, contentType, disposition string, content io.ReadSeeker) {
	filename = safeArchiveFilename(filename, artifact.ID)
	contentType = strings.TrimSpace(contentType)
	if contentType == "" || !strings.Contains(contentType, "/") {
		contentType = mime.TypeByExtension(path.Ext(filename))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filename}))
	w.Header().Set("ETag", fmt.Sprintf("\"sha256:%s\"", version.ContentSHA256))
	w.Header().Set("X-Content-SHA256", version.ContentSHA256)
	w.Header().Set("X-Artifact-Id", artifact.ID)
	w.Header().Set("X-Artifact-Version-Id", version.ID)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filename, version.CreatedAt, content)
}

// serveRunnerLargeToolResultCurrent serves the single immutable internal
// evidence version for an artifact-ID-only request. Internal evidence is never
// part of the project-file store, so this path deliberately bypasses artifact
// metadata and listing surfaces.
func (s *Server) serveRunnerLargeToolResultCurrent(
	w http.ResponseWriter,
	r *http.Request,
	userID, artifactID string,
) (handled bool) {
	if s == nil || s.workspaceStore == nil || !isRunnerLargeToolResultArtifactID(artifactID) {
		return false
	}
	record, found, err := s.workspaceStore.GetRunnerLargeToolResult(r.Context(), artifactID, userID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return true
	}
	if !found {
		writeAttachmentError(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		return true
	}
	return s.serveRunnerLargeToolResultEvidence(w, r, userID, artifactID, record.VersionID)
}

// serveRunnerLargeToolResultEvidence serves exact internal evidence content
// while preserving the public artifact version URL contract. It only handles
// internal identities; normal artifact requests fall through to the workspace
// artifact store.
func (s *Server) serveRunnerLargeToolResultEvidence(
	w http.ResponseWriter,
	r *http.Request,
	userID, artifactID, versionID string,
) (handled bool) {
	if s == nil || s.workspaceStore == nil {
		return false
	}
	if artifactID != "" && !isRunnerLargeToolResultArtifactID(artifactID) {
		return false
	}
	if artifactID == "" && !strings.HasPrefix(versionID, "ltr-") {
		return false
	}
	record, content, found, err := s.workspaceStore.OpenRunnerLargeToolResultContent(r.Context(), versionID, userID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return true
	}
	if !found {
		if artifactID == "" || isRunnerLargeToolResultArtifactID(artifactID) {
			writeAttachmentError(w, http.StatusNotFound, "Artifact version unavailable")
			return true
		}
		return false
	}
	if artifactID != "" && record.ArtifactID != artifactID {
		writeAttachmentError(w, http.StatusNotFound, "Artifact version unavailable")
		return true
	}
	defer content.Close()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}
	filename := runnerLargeToolResultFilename(record.ToolName, record.ToolCallID)
	serveRunnerLargeToolResultVersion(w, r, record, filename, content)
	return true
}

func serveRunnerLargeToolResultVersion(
	w http.ResponseWriter,
	r *http.Request,
	record workspace.RunnerLargeToolResult,
	filename string,
	content io.ReadSeeker,
) {
	filename = safeArchiveFilename(filename, record.ArtifactID)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("ETag", fmt.Sprintf("\"sha256:%s\"", record.ContentSHA256))
	w.Header().Set("X-Content-SHA256", record.ContentSHA256)
	w.Header().Set("X-Artifact-Id", record.ArtifactID)
	w.Header().Set("X-Artifact-Version-Id", record.VersionID)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filename, record.CreatedAt, content)
}

func serveArtifactDownloadMetadata(w http.ResponseWriter, metadata workspace.CompatibilityArtifactDownloadMetadata) {
	filename := safeArchiveFilename(
		safeArchiveFilename(metadata.Filename, metadata.ArtifactID)+".metadata.json",
		metadata.ArtifactID+".metadata.json",
	)
	payload := map[string]any{
		"artifact_id": metadata.ArtifactID, "version_id": metadata.VersionID,
		"version_number": metadata.VersionNumber, "filename": metadata.Filename,
		"content_type": metadata.ContentType, "size_bytes": metadata.SizeBytes,
		"created_at": metadata.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z"),
		"agent_name": metadata.AgentName, "is_user_upload": metadata.IsUserUpload,
		"checksum": metadata.Checksum,
	}
	if metadata.ReproductionCode != nil {
		payload["reproduction_code"] = *metadata.ReproductionCode
	}
	if metadata.Environment != nil {
		payload["environment"] = metadata.Environment
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		writeAttachmentError(w, http.StatusInternalServerError, "artifact metadata is unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (s *Server) handleArtifactDownloadSelection(w http.ResponseWriter, r *http.Request) {
	store, ok := s.attachmentStore(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	var ids []string
	switch r.Method {
	case http.MethodGet:
		ids = strings.Split(r.URL.Query().Get("ids"), ",")
	case http.MethodPost:
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		if err := decoder.Decode(&ids); err != nil {
			writeAttachmentError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeAttachmentError(w, http.StatusBadRequest, "request body must contain one JSON array")
			return
		}
	default:
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ids = normalizedArtifactIDs(ids)
	if len(ids) == 0 {
		writeAttachmentError(w, http.StatusBadRequest, "ids query param is required (comma-separated artifact ids)")
		return
	}
	if len(ids) > maxSelectedArtifactDownloads {
		writeAttachmentError(w, http.StatusBadRequest, "Too many artifact ids (max 200).")
		return
	}
	if err := s.writeArtifactZIP(w, r, store, userID, ids, queryBool(r, "include_metadata"), "artifacts.zip"); err != nil {
		writeAttachmentStoreError(w, err)
	}
}

func normalizedArtifactIDs(values []string) []string {
	seen := map[string]bool{}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		ids = append(ids, value)
	}
	return ids
}

func safeArchiveFilename(filename, fallback string) string {
	filename = strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/")
	filename = path.Base(filename)
	filename = strings.Map(func(value rune) rune {
		if value < 0x20 || value == 0x7f {
			return -1
		}
		return value
	}, filename)
	if filename == "" || filename == "." || filename == ".." {
		filename = strings.TrimSpace(fallback)
	}
	if filename == "" {
		filename = "artifact"
	}
	return truncateUTF8Filename(filename, 255)
}

func truncateUTF8Filename(filename string, maxBytes int) string {
	if len(filename) <= maxBytes {
		return filename
	}
	extension := path.Ext(filename)
	if len(extension) > 16 {
		extension = ""
	}
	budget := maxBytes - len(extension)
	if budget < 1 {
		budget = maxBytes
		extension = ""
	}
	base := strings.TrimSuffix(filename, extension)
	for len(base) > budget {
		_, size := utf8.DecodeLastRuneInString(base)
		if size <= 0 {
			break
		}
		base = base[:len(base)-size]
	}
	if base == "" {
		base = "artifact"
		if len(base)+len(extension) > maxBytes {
			extension = ""
		}
	}
	return base + extension
}

func uniqueArchiveFilename(filename string, used map[string]int) string {
	key := strings.ToLower(filename)
	if used[key] == 0 {
		used[key] = 1
		return filename
	}
	extension := path.Ext(filename)
	base := strings.TrimSuffix(filename, extension)
	for index := used[key] + 1; ; index++ {
		candidate := truncateUTF8Filename(base+" ("+strconv.Itoa(index)+")"+extension, 255)
		candidateKey := strings.ToLower(candidate)
		if used[candidateKey] == 0 {
			used[key] = index
			used[candidateKey] = 1
			return candidate
		}
	}
}

func artifactDownloadMetadata(artifact workspace.Artifact, version workspace.ArtifactVersion) map[string]any {
	return map[string]any{
		"id": artifact.ID, "projectId": artifact.ProjectID, "filename": artifact.Name,
		"contentType": artifact.Kind, "currentVersionNumber": artifact.CurrentVersionNumber,
		"versionId": version.ID, "versionNumber": version.VersionNumber, "parentId": version.ParentID,
		"size": version.SizeBytes, "sha256": version.ContentSHA256,
		"createdAt": artifact.CreatedAt, "updatedAt": artifact.UpdatedAt,
		"versionCreatedAt": version.CreatedAt, "createdBy": version.CreatedBy,
	}
}

func validateArtifactIDs(store *workspace.Store, userID string, ids []string) error {
	for _, id := range ids {
		if artifact, found, err := store.GetArtifact(id); err != nil {
			return err
		} else if !found {
			return fmt.Errorf("artifact %q not found", id)
		} else if owned, err := store.ProjectOwnedBy(artifact.ProjectID, userID); err != nil {
			return err
		} else if !owned {
			return fmt.Errorf("artifact %q not found", id)
		}
	}
	return nil
}
