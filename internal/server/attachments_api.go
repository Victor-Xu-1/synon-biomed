package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	maxSingleAttachmentRequest int64 = 64 << 20
	maxChunkUploadRequest      int64 = 65 << 20
	defaultUploadChunkSize     int64 = 5 << 20
	minUploadChunkSize         int64 = 1 << 20
	maxUploadChunkSize         int64 = 64 << 20
)

func (s *Server) handleProjectAttachments(w http.ResponseWriter, r *http.Request) {
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/projects/"))
	if len(segments) == 2 && segments[0] == "batch" {
		s.handleCompatibilityProjectBatchCollection(w, r, segments[1])
		return
	}
	if len(segments) == 1 {
		projectID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(projectID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid project id")
			return
		}
		s.handleCompatibilityProjectRead(w, r, projectID)
		return
	}
	if len(segments) == 2 && segments[1] == "request" {
		projectID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(projectID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid project id")
			return
		}
		s.handleProjectSubmitRequestCompatibility(w, r, projectID)
		return
	}
	if len(segments) == 2 && (segments[1] == "artifacts" || segments[1] == "benches") {
		projectID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(projectID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid project id")
			return
		}
		s.handleCompatibilityProjectCollection(w, r, projectID, segments[1])
		return
	}
	if len(segments) == 2 && segments[1] == "notes" {
		projectID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(projectID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid project id")
			return
		}
		s.handleCompatibilityProjectNotes(w, r, projectID)
		return
	}
	if len(segments) == 3 && segments[1] == "memory" && segments[2] == "enabled" {
		projectID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(projectID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid project id")
			return
		}
		s.handleProjectMemoryEnabledCompatibility(w, r, projectID)
		return
	}
	if len(segments) >= 2 && len(segments) <= 3 && segments[1] == "folders" {
		projectID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(projectID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid project id")
			return
		}
		folderID := ""
		if len(segments) == 3 {
			folderID, err = url.PathUnescape(segments[2])
			if err != nil || strings.TrimSpace(folderID) == "" {
				writeV11Detail(w, http.StatusBadRequest, "Invalid folder id")
				return
			}
		}
		s.handleCompatibilityProjectFolders(w, r, projectID, folderID)
		return
	}
	store, ok := s.attachmentStore(w)
	if !ok {
		return
	}
	if (len(segments) == 2 || len(segments) == 3 && segments[2] == "list") && segments[1] == "annotations" {
		s.handleProjectAnnotations(w, r, store, segments[0])
		return
	}
	if len(segments) != 2 || segments[1] != "attachments" {
		writeAttachmentError(w, http.StatusNotFound, "attachment endpoint not found")
		return
	}
	projectID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(projectID) == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if r.Method != http.MethodPost {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	owned, err := store.ProjectOwnedBy(projectID, userID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !owned {
		writeAttachmentError(w, http.StatusNotFound, "project not found")
		return
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaTypeErr != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		writeAttachmentError(w, http.StatusNotAcceptable, "the request is not multipart")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSingleAttachmentRequest+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		writeAttachmentError(w, status, err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAttachmentError(w, http.StatusBadRequest, "No file uploaded")
		return
	}
	defer file.Close()
	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		if inferred := mime.TypeByExtension(filepath.Ext(header.Filename)); inferred != "" {
			contentType = inferred
		}
	}
	attachment, err := store.CreateAttachment(r.Context(), workspace.CreateAttachmentInput{
		UserID: userID, ProjectID: projectID, Filename: header.Filename,
		ContentType: contentType, Ephemeral: queryBool(r, "ephemeral"),
	}, io.LimitReader(file, maxSingleAttachmentRequest+1))
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, attachmentResponse(attachment))
}

func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request) {
	store, ok := s.attachmentStore(w)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/attachments/"))
	if len(segments) < 1 || len(segments) > 2 {
		writeAttachmentError(w, http.StatusNotFound, "attachment endpoint not found")
		return
	}
	id, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(id) == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if len(segments) == 2 {
		if segments[1] != "metadata" {
			writeAttachmentError(w, http.StatusNotFound, "attachment endpoint not found")
			return
		}
		if r.Method != http.MethodGet {
			writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		attachment, found, err := store.GetAttachment(r.Context(), userID, id)
		if err != nil {
			writeAttachmentStoreError(w, err)
			return
		}
		if !found {
			writeAttachmentError(w, http.StatusNotFound, "Attachment "+id+" not found")
			return
		}
		writeJSON(w, http.StatusOK, attachmentResponse(attachment))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	attachment, file, found, err := store.OpenAttachment(r.Context(), userID, id)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found {
		writeAttachmentError(w, http.StatusNotFound, "Attachment "+id+" not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeAttachmentError(w, http.StatusInternalServerError, "attachment content is unavailable")
		return
	}
	w.Header().Set("Content-Type", attachment.ContentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename}))
	w.Header().Set("ETag", fmt.Sprintf("\"sha256:%s\"", attachment.SHA256))
	w.Header().Set("X-Content-SHA256", attachment.SHA256)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, attachment.Filename, info.ModTime(), file)
}

func (s *Server) handleArtifactUpload(w http.ResponseWriter, r *http.Request) {
	store, ok := s.attachmentStore(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/api/artifacts/upload/")
	switch {
	case tail == "init":
		s.handleAttachmentUploadInit(w, r, store, userID)
	case tail == "chunk":
		s.handleAttachmentUploadChunk(w, r, store, userID)
	case tail == "finalize":
		s.handleAttachmentUploadFinalize(w, r, store, userID)
	case strings.HasPrefix(tail, "status/"):
		s.handleAttachmentUploadStatus(w, r, store, userID, strings.TrimPrefix(tail, "status/"))
	case tail != "" && !strings.Contains(tail, "/"):
		s.handleAttachmentUploadCancel(w, r, store, userID, tail)
	default:
		writeAttachmentError(w, http.StatusNotFound, "attachment upload endpoint not found")
	}
}

func (s *Server) handleAttachmentUploadInit(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var input struct {
		ProjectID   string `json:"project_id"`
		Filename    string `json:"filename"`
		TotalSize   int64  `json:"total_size"`
		ContentType string `json:"content_type"`
		ChunkSize   int64  `json:"chunk_size"`
		FolderID    string `json:"folder_id"`
		Ephemeral   bool   `json:"ephemeral"`
	}
	if err := decodeAttachmentJSON(r, &input); err != nil {
		writeAttachmentError(w, http.StatusBadRequest, err.Error())
		return
	}
	if input.ChunkSize == 0 {
		input.ChunkSize = defaultUploadChunkSize
	}
	if input.ChunkSize < minUploadChunkSize || input.ChunkSize > maxUploadChunkSize {
		writeAttachmentError(w, http.StatusBadRequest, fmt.Sprintf("chunk_size must be between %d and %d bytes", minUploadChunkSize, maxUploadChunkSize))
		return
	}
	upload, err := store.InitAttachmentUpload(r.Context(), workspace.InitAttachmentUploadInput{
		UserID: userID, ProjectID: input.ProjectID, Filename: input.Filename,
		TotalSize: input.TotalSize, ContentType: input.ContentType, ChunkSize: input.ChunkSize,
		FolderID: input.FolderID, Ephemeral: input.Ephemeral,
	})
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"upload_id": upload.ID, "chunk_size": upload.ChunkSize, "total_chunks": upload.ExpectedChunks,
	})
}

func (s *Server) handleAttachmentUploadChunk(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxChunkUploadRequest)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		writeAttachmentError(w, status, err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	uploadID := strings.TrimSpace(r.FormValue("upload_id"))
	chunkIndex, err := strconv.Atoi(strings.TrimSpace(r.FormValue("chunk_index")))
	if uploadID == "" || err != nil {
		writeAttachmentError(w, http.StatusBadRequest, "upload_id, chunk_index, and chunk file are required")
		return
	}
	file, _, err := r.FormFile("chunk")
	if err != nil {
		writeAttachmentError(w, http.StatusBadRequest, "upload_id, chunk_index, and chunk file are required")
		return
	}
	defer file.Close()
	upload, err := store.SaveAttachmentChunk(r.Context(), userID, uploadID, chunkIndex, file)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chunk_index": chunkIndex, "received": true,
		"chunks_remaining": upload.ExpectedChunks - len(upload.ReceivedChunks),
	})
}

func (s *Server) handleAttachmentUploadFinalize(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	uploadID := strings.TrimSpace(r.URL.Query().Get("upload_id"))
	if uploadID == "" {
		writeAttachmentError(w, http.StatusBadRequest, "upload_id query param is required")
		return
	}
	mutationKey := firstNonEmpty(r.Header.Get("Idempotency-Key"), r.Header.Get("X-Synon-Idempotency-Key"))
	if strings.TrimSpace(mutationKey) == "" {
		mutationKey = "attachment-finalize-" + uploadID
	}
	artifact, version, requestedFolderID, err := store.FinalizeArtifactUpload(
		workspace.WithMutationIdempotencyKey(r.Context(), mutationKey), userID, uploadID, r.URL.Query().Get("checksum"),
	)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	targetFolderID, err := chunkedUploadTargetFolder(r.Context(), store, userID, artifact.ProjectID, requestedFolderID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if targetFolderID != "" && artifact.FolderID != targetFolderID {
		_, err = store.UpdateCompatibilityArtifactFolderRealtime(
			workspace.WithMutationIdempotencyKey(r.Context(), mutationKey+"-folder"), userID, artifact.ID,
			workspace.UpdateCompatibilityArtifactFolderInput{FolderSet: true, FolderID: targetFolderID},
		)
		if err != nil {
			writeAttachmentStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"artifact_id": artifact.ID, "version_id": version.ID, "filename": artifact.Name,
		"size_bytes": version.SizeBytes, "checksum": version.ContentSHA256,
	})
}

func (s *Server) handleAttachmentUploadStatus(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, uploadID string) {
	if r.Method != http.MethodGet {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	uploadID, err := url.PathUnescape(uploadID)
	if err != nil || strings.TrimSpace(uploadID) == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid upload id")
		return
	}
	upload, found, err := store.GetAttachmentUpload(r.Context(), userID, uploadID)
	if err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	if !found {
		writeAttachmentError(w, http.StatusNotFound, "attachment upload not found")
		return
	}
	writeJSON(w, http.StatusOK, attachmentUploadResponse(upload))
}

func (s *Server) handleAttachmentUploadCancel(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, uploadID string) {
	if r.Method != http.MethodDelete {
		writeAttachmentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	uploadID, err := url.PathUnescape(uploadID)
	if err != nil || strings.TrimSpace(uploadID) == "" {
		writeAttachmentError(w, http.StatusBadRequest, "invalid upload id")
		return
	}
	if err := store.CancelAttachmentUpload(r.Context(), userID, uploadID); err != nil {
		writeAttachmentStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Upload " + uploadID + " cancelled"})
}

func chunkedUploadTargetFolder(ctx context.Context, store *workspace.Store, userID, projectID, requested string) (string, error) {
	if requested = strings.TrimSpace(requested); requested != "" {
		return requested, nil
	}
	folders, found, err := store.ListCompatibilityFolders(ctx, userID, projectID)
	if err != nil || !found {
		return "", err
	}
	for _, folder := range folders {
		if folder.IsUserUploadsFolder {
			return folder.ID, nil
		}
	}
	return "", nil
}

func (s *Server) attachmentStore(w http.ResponseWriter) (*workspace.Store, bool) {
	if s.workspaceStore == nil {
		writeAttachmentError(w, http.StatusServiceUnavailable, "workspace runtime is not configured")
		return nil, false
	}
	return s.workspaceStore, true
}

func attachmentUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := strings.TrimSpace(resolveUserID(r, nil))
	if userID == "" {
		writeAttachmentError(w, http.StatusUnauthorized, "user identity is required")
		return "", false
	}
	return userID, true
}

func decodeAttachmentJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func attachmentResponse(value workspace.Attachment) map[string]any {
	return map[string]any{
		"id": value.ID, "project_id": value.ProjectID, "projectId": value.ProjectID,
		"filename": value.Filename, "name": value.Filename,
		"content_type": value.ContentType, "contentType": value.ContentType, "mimetype": value.ContentType,
		"size": value.Size, "size_bytes": value.Size, "sha256": value.SHA256,
		"ephemeral": value.Ephemeral, "folder_id": value.FolderID, "folderId": value.FolderID,
		"created_at": value.CreatedAt, "createdAt": value.CreatedAt,
		"updated_at": value.UpdatedAt, "updatedAt": value.UpdatedAt,
	}
}

func attachmentUploadResponse(value workspace.AttachmentUpload) map[string]any {
	missing := make([]int, 0, value.ExpectedChunks-len(value.ReceivedChunks))
	received := make(map[int]bool, len(value.ReceivedChunks))
	for _, index := range value.ReceivedChunks {
		received[index] = true
	}
	for index := 0; index < value.ExpectedChunks; index++ {
		if !received[index] {
			missing = append(missing, index)
		}
	}
	percent := float64(0)
	if value.ExpectedChunks > 0 {
		percent = float64(len(value.ReceivedChunks)*1000/value.ExpectedChunks) / 10
	}
	return map[string]any{
		"upload_id": value.ID, "filename": value.Filename, "total_size": value.TotalSize,
		"chunk_size": value.ChunkSize, "total_chunks": value.ExpectedChunks, "received_chunks": len(value.ReceivedChunks),
		"missing_chunks": missing, "percent_complete": percent, "created_at": value.CreatedAt.UnixMilli(),
	}
}

func queryBool(r *http.Request, key string) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return value == "1" || value == "true" || value == "yes"
}

func writeAttachmentStoreError(w http.ResponseWriter, err error) {
	var capacity *workspace.InsufficientAttachmentSpaceError
	if errors.As(err, &capacity) {
		writeJSON(w, http.StatusInsufficientStorage, map[string]any{"detail": capacity.Error(), "code": "insufficient_upload_storage", "required_bytes": capacity.RequiredBytes, "available_bytes": capacity.AvailableBytes})
		return
	}
	message := err.Error()
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, workspace.ErrMutationIdempotencyConflict):
		status = http.StatusConflict
	case strings.Contains(message, "not found"), strings.Contains(message, "does not exist"):
		status = http.StatusNotFound
	case strings.Contains(message, "exceeds"):
		status = http.StatusRequestEntityTooLarge
	case strings.Contains(message, "closed"):
		status = http.StatusServiceUnavailable
	}
	writeAttachmentError(w, status, message)
}

func writeAttachmentError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"detail": message})
}
