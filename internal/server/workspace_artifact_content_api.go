package server

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	defaultArtifactTextBytes = 1 << 20
	maxArtifactTextBytes     = 10 << 20
)

func (s *Server) handleWorkspaceArtifactBinaryVersion(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, artifactID string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		ProjectID     string `json:"projectId"`
		Name          string `json:"name"`
		Kind          string `json:"kind"`
		ContentBase64 string `json:"contentBase64"`
		CreatedBy     string `json:"createdBy"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !workspaceProjectOwned(w, store, input.ProjectID, userID) {
		return
	}
	content, err := base64.StdEncoding.Strict().DecodeString(input.ContentBase64)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "contentBase64 is invalid"})
		return
	}
	artifact, version, err := store.SaveArtifactVersionRealtime(workspaceMutationContext(r), workspace.SaveArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: input.ProjectID, Name: input.Name,
		Kind: input.Kind, Content: content, CreatedBy: input.CreatedBy,
	}, userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "artifact": artifact, "version": artifactVersionMetadata(version),
	})
}

func (s *Server) handleWorkspaceArtifactCurrentContent(w http.ResponseWriter, r *http.Request, store *workspace.Store, artifactID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	artifact, version, content, found, err := store.OpenCurrentArtifactContent(artifactID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "artifact version not found"})
		return
	}
	defer content.Close()
	contentType := strings.TrimSpace(artifact.Kind)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(version.SizeBytes, 10))
	w.Header().Set("X-Content-SHA256", version.ContentSHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, content)
}

func (s *Server) handleWorkspaceArtifactText(w http.ResponseWriter, r *http.Request, store *workspace.Store, artifactID string) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	limit, err := workspaceArtifactTextLimit(r)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	artifact, version, content, found, err := store.OpenCurrentArtifactContent(artifactID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "artifact version not found"})
		return
	}
	defer content.Close()
	s.writeWorkspaceArtifactText(w, artifact, version, content, limit)
}

func (s *Server) handleWorkspaceArtifactCopy(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, artifactID string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		NewArtifactID   string `json:"newArtifactId"`
		TargetProjectID string `json:"targetProjectId"`
		Name            string `json:"name"`
		CreatedBy       string `json:"createdBy"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !workspaceProjectOwned(w, store, input.TargetProjectID, userID) {
		return
	}
	artifact, version, err := store.CopyArtifactRealtime(
		workspaceMutationContext(r), artifactID, input.TargetProjectID, input.NewArtifactID, input.Name, input.CreatedBy, userID,
	)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact": artifact, "version": artifactVersionMetadata(version)})
}

func (s *Server) handleWorkspaceArtifactVersion(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/artifact-versions/"))
	if len(segments) < 2 || len(segments) > 3 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	versionID, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid artifact version id"})
		return
	}
	artifact, version, found, err := store.GetArtifactVersionMetadata(versionID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "artifact version not found"})
		return
	}
	if !workspaceProjectOwned(w, store, artifact.ProjectID, userID) {
		return
	}
	switch segments[1] {
	case "text":
		if r.Method != http.MethodGet || (len(segments) == 3 && segments[2] != "head") {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		limit, err := workspaceArtifactTextLimit(r)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		_, _, content, found, err := store.OpenArtifactVersionContent(versionID)
		if err != nil || !found {
			status := http.StatusNotFound
			if err != nil {
				status = workspaceStatus(err)
			}
			writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": "artifact version content unavailable"})
			return
		}
		defer content.Close()
		s.writeWorkspaceArtifactText(w, artifact, version, content, limit)
	case "content":
		if r.Method != http.MethodGet || len(segments) != 2 {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		contentType := strings.TrimSpace(artifact.Kind)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		_, _, content, found, err := store.OpenArtifactVersionContent(versionID)
		if err != nil || !found {
			status := http.StatusNotFound
			if err != nil {
				status = workspaceStatus(err)
			}
			writeWorkspaceJSON(w, status, map[string]any{"ok": false, "error": "artifact version content unavailable"})
			return
		}
		defer content.Close()
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(version.SizeBytes, 10))
		w.Header().Set("X-Content-SHA256", version.ContentSHA256)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, content)
	default:
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
	}
}

func (s *Server) writeWorkspaceArtifactText(w http.ResponseWriter, artifact workspace.Artifact, version workspace.ArtifactVersion, reader io.Reader, limit int) {
	content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	truncated := version.SizeBytes > int64(limit) || len(content) > limit
	if len(content) > limit {
		content = content[:limit]
	}
	removed := 0
	for truncated && removed < utf8.UTFMax-1 && len(content) > 0 && !utf8.Valid(content) {
		content = content[:len(content)-1]
		removed++
	}
	if !utf8.Valid(content) {
		writeWorkspaceJSON(w, http.StatusUnsupportedMediaType, map[string]any{"ok": false, "error": "artifact content is not valid UTF-8 text"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "artifactId": artifact.ID, "versionId": version.ID,
		"versionNumber": version.VersionNumber, "text": string(content),
		"bytes": version.SizeBytes, "bytesReturned": len(content), "truncated": truncated,
		"contentSha256": version.ContentSHA256,
	})
}

func workspaceArtifactTextLimit(r *http.Request) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("max_bytes"))
	if raw == "" {
		return defaultArtifactTextBytes, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid max_bytes %q", raw)
	}
	if value > maxArtifactTextBytes {
		value = maxArtifactTextBytes
	}
	return value, nil
}

func artifactVersionMetadata(version workspace.ArtifactVersion) map[string]any {
	return map[string]any{
		"id": version.ID, "artifactId": version.ArtifactID,
		"versionNumber": version.VersionNumber, "parentId": version.ParentID,
		"contentSha256": version.ContentSHA256, "createdBy": version.CreatedBy,
		"createdAt": version.CreatedAt, "contentBytes": version.SizeBytes,
	}
}
