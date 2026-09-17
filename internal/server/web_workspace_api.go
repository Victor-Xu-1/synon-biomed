package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	workspace "synon-go/internal/persistence/workspace"
)

const webProjectArtifactPageContract = "synon.project-artifact-page.v1"

type webProjectArtifactCursor struct {
	Version    int    `json:"v"`
	Scope      string `json:"scope"`
	Revision   string `json:"revision"`
	CreatedAt  string `json:"created_at"`
	ArtifactID string `json:"artifact_id"`
}

func (s *Server) handleWebConversationWorkspace(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	project workspace.CompatibilityProject,
	segments []string,
) {
	if s.workspaceStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "workspace storage is not configured"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	if frame.ProjectID != project.ID {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "workspace not found"})
		return
	}
	switch {
	case len(segments) == 0:
		s.handleWebWorkspaceList(w, r, userID, project.ID, frame.ID)
	case segments[0] == "uploads":
		s.handleWebWorkspaceUpload(w, r, userID, frame, segments[1:])
	case len(segments) == 2 && segments[0] == "artifacts":
		s.handleWebWorkspaceArtifact(w, r, userID, project.ID, segments[1])
	default:
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "workspace endpoint not found"})
	}
}

func (s *Server) handleWebWorkspaceList(w http.ResponseWriter, r *http.Request, userID, projectID, frameID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	requestedPath := strings.TrimSpace(r.URL.Query().Get("path"))
	requestedPath = strings.Trim(requestedPath, "/")
	if requestedPath == "" || requestedPath == "." {
		writeWorkspaceJSON(w, http.StatusOK, []map[string]any{{
			"name": "project-files", "type": "directory", "relative_path": "project-files",
			"read_only": false, "can_rename": false, "can_delete": false,
		}})
		return
	}
	if requestedPath != "project-files" {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "workspace path not found"})
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if len(search) > 256 || !utf8.ValidString(search) {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "workspace search is invalid"})
		return
	}
	limit := 200
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 || parsed > 1000 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "workspace page limit must be between 1 and 1000"})
			return
		}
		limit = parsed
	}
	scope := webProjectArtifactCursorScope(userID, projectID, frameID, search, true)
	var cursor *workspace.CompatibilityProjectArtifactCursor
	if rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor")); rawCursor != "" {
		decoded, err := decodeWebProjectArtifactCursor(rawCursor, scope)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "workspace cursor is invalid"})
			return
		}
		cursor = &decoded
	}
	page, err := s.workspaceStore.ListCompatibilityProjectCurrentArtifactPage(r.Context(), workspace.CompatibilityProjectArtifactPageInput{
		OwnerUserID: userID, ProjectID: projectID, FrameID: frameID, Search: search,
		ExcludeIntermediate: true, ExcludeInternal: true, Limit: limit, Cursor: cursor,
	})
	if err != nil {
		if errors.Is(err, workspace.ErrCompatibilityProjectArtifactCursorStale) {
			writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"message": "workspace artifact index changed; refresh and retry"})
			return
		}
		writeV11StoreError(w, err)
		return
	}
	entries := make([]map[string]any, 0, len(page.Artifacts))
	for _, artifact := range page.Artifacts {
		entries = append(entries, map[string]any{
			"name": artifact.Filename, "type": "file",
			"relative_path": "project-files/" + artifact.Filename,
			"read_only":     false, "can_rename": true, "can_delete": true,
			"artifact_id": artifact.ID, "version_id": artifact.VersionID,
			"content_type": artifact.ContentType, "size_bytes": artifact.SizeBytes,
			"content_url": "/api/artifacts/" + url.PathEscape(artifact.ID) + "/versions/" + url.PathEscape(artifact.VersionID),
		})
	}
	var nextCursor any
	if page.NextCursor != nil {
		nextCursor = encodeWebProjectArtifactCursor(scope, *page.NextCursor)
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"contract": webProjectArtifactPageContract, "items": entries,
		"next_cursor": nextCursor, "has_more": page.HasMore, "total": page.Total,
	})
}

func webProjectArtifactCursorScope(userID, projectID, frameID, search string, excludeIntermediate bool) string {
	value := strings.Join([]string{
		strings.TrimSpace(userID), strings.TrimSpace(projectID), strings.TrimSpace(frameID), strings.TrimSpace(search), strconv.FormatBool(excludeIntermediate),
	}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func encodeWebProjectArtifactCursor(scope string, cursor workspace.CompatibilityProjectArtifactCursor) string {
	payload, _ := json.Marshal(webProjectArtifactCursor{
		Version: 1, Scope: scope, Revision: cursor.Revision,
		CreatedAt: cursor.CreatedAt.UTC().Format(time.RFC3339Nano), ArtifactID: cursor.ArtifactID,
	})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeWebProjectArtifactCursor(raw, scope string) (workspace.CompatibilityProjectArtifactCursor, error) {
	if len(raw) > 2048 {
		return workspace.CompatibilityProjectArtifactCursor{}, errors.New("cursor is too large")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return workspace.CompatibilityProjectArtifactCursor{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var cursor webProjectArtifactCursor
	if err := decoder.Decode(&cursor); err != nil {
		return workspace.CompatibilityProjectArtifactCursor{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return workspace.CompatibilityProjectArtifactCursor{}, errors.New("cursor contains trailing data")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil || cursor.Version != 1 || cursor.Scope != scope || strings.TrimSpace(cursor.Revision) == "" ||
		len(cursor.Revision) > 512 || strings.TrimSpace(cursor.ArtifactID) == "" || len(cursor.ArtifactID) > 512 {
		return workspace.CompatibilityProjectArtifactCursor{}, errors.New("cursor contract mismatch")
	}
	return workspace.CompatibilityProjectArtifactCursor{
		Revision: cursor.Revision, CreatedAt: createdAt.UTC(), ArtifactID: cursor.ArtifactID,
	}, nil
}

func (s *Server) handleWebWorkspaceUpload(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	frame workspace.CompatibilityFrame,
	segments []string,
) {
	switch {
	case len(segments) == 0:
		s.handleWebWorkspaceUploadInit(w, r, userID, frame.ProjectID)
	case len(segments) == 1:
		s.handleWebWorkspaceUploadRecord(w, r, userID, frame.ProjectID, segments[0])
	case len(segments) == 3 && segments[1] == "chunks":
		s.handleWebWorkspaceUploadChunk(w, r, userID, frame.ProjectID, segments[0], segments[2])
	case len(segments) == 2 && segments[1] == "finalize":
		s.handleWebWorkspaceUploadFinalize(w, r, userID, frame, segments[0])
	default:
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "workspace upload endpoint not found"})
	}
}

func (s *Server) handleWebWorkspaceUploadInit(w http.ResponseWriter, r *http.Request, userID, projectID string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	var input struct {
		Filename    string `json:"filename"`
		TotalSize   int64  `json:"total_size"`
		ContentType string `json:"content_type"`
		ChunkSize   int64  `json:"chunk_size"`
		FolderID    string `json:"folder_id"`
		Ephemeral   bool   `json:"ephemeral"`
	}
	if err := decodeWebConversationJSON(w, r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid upload request"})
		return
	}
	if input.ChunkSize == 0 {
		input.ChunkSize = defaultUploadChunkSize
	}
	if input.ChunkSize < minUploadChunkSize || input.ChunkSize > maxUploadChunkSize {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "upload chunk size is outside the supported range"})
		return
	}
	upload, err := s.workspaceStore.InitAttachmentUpload(r.Context(), workspace.InitAttachmentUploadInput{
		UserID: userID, ProjectID: projectID, Filename: input.Filename,
		TotalSize: input.TotalSize, ContentType: input.ContentType, ChunkSize: input.ChunkSize,
		FolderID: input.FolderID, Ephemeral: input.Ephemeral,
	})
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"upload_id": upload.ID, "chunk_size": upload.ChunkSize, "total_chunks": upload.ExpectedChunks,
	})
}

func (s *Server) handleWebWorkspaceUploadRecord(
	w http.ResponseWriter,
	r *http.Request,
	userID, projectID, rawUploadID string,
) {
	uploadID, upload, ok := s.webWorkspaceUploadAccess(w, r, userID, projectID, rawUploadID)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeWorkspaceJSON(w, http.StatusOK, attachmentUploadResponse(upload))
	case http.MethodDelete:
		if err := s.workspaceStore.CancelAttachmentUpload(r.Context(), userID, uploadID); err != nil {
			writeAttachmentStoreError(w, err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"cancelled": true, "upload_id": uploadID})
	default:
		w.Header().Set("Allow", "GET, DELETE")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
	}
}

func (s *Server) handleWebWorkspaceUploadChunk(
	w http.ResponseWriter,
	r *http.Request,
	userID, projectID, rawUploadID, rawIndex string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	uploadID, upload, ok := s.webWorkspaceUploadAccess(w, r, userID, projectID, rawUploadID)
	if !ok {
		return
	}
	chunkIndex, err := strconv.Atoi(rawIndex)
	if err != nil || chunkIndex < 0 || chunkIndex >= upload.ExpectedChunks {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid upload chunk index"})
		return
	}
	expectedSize := upload.ChunkSize
	if chunkIndex == upload.ExpectedChunks-1 {
		expectedSize = upload.TotalSize - int64(chunkIndex)*upload.ChunkSize
	}
	if r.ContentLength > expectedSize {
		writeWorkspaceJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"message": "upload chunk exceeds its expected size"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, expectedSize)
	updated, err := s.workspaceStore.SaveAttachmentChunk(r.Context(), userID, uploadID, chunkIndex, r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeWorkspaceJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"message": "upload chunk exceeds its expected size"})
			return
		}
		writeAttachmentStoreError(w, err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"chunk_index": chunkIndex, "received": true,
		"chunks_remaining": updated.ExpectedChunks - len(updated.ReceivedChunks),
	})
}

func (s *Server) handleWebWorkspaceUploadFinalize(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	frame workspace.CompatibilityFrame,
	rawUploadID string,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	uploadID, _, ok := s.webWorkspaceUploadAccess(w, r, userID, frame.ProjectID, rawUploadID)
	if !ok {
		return
	}
	mutationKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), r.Header.Get("X-Synon-Idempotency-Key"))
	if strings.TrimSpace(mutationKey) == "" {
		mutationKey = "web-workspace-finalize-" + uploadID
	}
	artifact, version, requestedFolderID, err := s.workspaceStore.FinalizeArtifactUploadForFrame(
		workspace.WithMutationIdempotencyKey(r.Context(), mutationKey),
		userID, uploadID, r.URL.Query().Get("checksum"), frame.RootFrameID, frame.ID,
	)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	targetFolderID, err := chunkedUploadTargetFolder(r.Context(), s.workspaceStore, userID, artifact.ProjectID, requestedFolderID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if targetFolderID != "" && artifact.FolderID != targetFolderID {
		_, err = s.workspaceStore.UpdateCompatibilityArtifactFolderRealtime(
			workspace.WithMutationIdempotencyKey(r.Context(), mutationKey+"-folder"), userID, artifact.ID,
			workspace.UpdateCompatibilityArtifactFolderInput{FolderSet: true, FolderID: targetFolderID},
		)
		if err != nil {
			writeAttachmentStoreError(w, err)
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"artifact_id": artifact.ID, "id": artifact.ID, "version_id": version.ID,
		"filename": artifact.Name, "size_bytes": version.SizeBytes, "checksum": version.ContentSHA256,
	})
}

func (s *Server) webWorkspaceUploadAccess(
	w http.ResponseWriter,
	r *http.Request,
	userID, projectID, rawUploadID string,
) (string, workspace.AttachmentUpload, bool) {
	uploadID, err := url.PathUnescape(rawUploadID)
	if err != nil || strings.TrimSpace(uploadID) == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid upload id"})
		return "", workspace.AttachmentUpload{}, false
	}
	upload, found, err := s.workspaceStore.GetAttachmentUpload(r.Context(), userID, uploadID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return "", workspace.AttachmentUpload{}, false
	}
	if !found || upload.ProjectID != projectID {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "upload not found"})
		return "", workspace.AttachmentUpload{}, false
	}
	return uploadID, upload, true
}

func (s *Server) handleWebWorkspaceArtifact(
	w http.ResponseWriter,
	r *http.Request,
	userID, projectID, rawArtifactID string,
) {
	artifactID, err := url.PathUnescape(rawArtifactID)
	if err != nil || strings.TrimSpace(artifactID) == "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid artifact id"})
		return
	}
	artifact, found, err := s.workspaceStore.GetCompatibilityArtifactMetadata(r.Context(), userID, artifactID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found || artifact.ProjectID != projectID {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "artifact not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeWorkspaceJSON(w, http.StatusOK, artifact)
	case http.MethodPatch:
		s.handleCompatibilityArtifactRename(w, r, artifactID)
	case http.MethodDelete:
		s.handleCompatibilityArtifactDelete(w, r, artifactID)
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
	}
}
