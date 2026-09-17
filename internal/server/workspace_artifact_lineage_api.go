package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

// registerWorkspaceArtifactReadCompatibilityRoutes replaces the two existing
// artifact download prefixes with dispatchers that preserve all download paths.
func (s *Server) registerWorkspaceArtifactReadCompatibilityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/artifacts/versions/", s.handleWorkspaceArtifactVersionReadCompatibility)
	mux.HandleFunc("/api/artifacts/", s.handleWorkspaceArtifactReadCompatibility)
}

func (s *Server) handleWorkspaceArtifactReadCompatibility(w http.ResponseWriter, r *http.Request) {
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/artifacts/"))
	if r.Method == http.MethodPost && len(segments) == 1 && segments[0] == "bulk-move" {
		s.handleCompatibilityArtifactBulkMove(w, r)
		return
	}
	if len(segments) >= 1 {
		artifactID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(artifactID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid artifact id")
			return
		}
		if r.Method == http.MethodDelete && len(segments) == 1 {
			s.handleCompatibilityArtifactDelete(w, r, artifactID)
			return
		}
		if r.Method == http.MethodPatch && len(segments) == 2 && segments[1] == "rename" {
			s.handleCompatibilityArtifactRename(w, r, artifactID)
			return
		}
		if r.Method == http.MethodPatch && len(segments) == 2 && segments[1] == "priority" {
			s.handleCompatibilityArtifactPriority(w, r, artifactID)
			return
		}
		if r.Method == http.MethodGet && len(segments) == 2 && segments[1] == "metadata" {
			s.handleCompatibilityArtifactMetadata(w, r, artifactID)
			return
		}
		if r.Method == http.MethodPost && len(segments) == 2 && segments[1] == "copy" {
			s.handleCompatibilityArtifactCopy(w, r, artifactID)
			return
		}
	}
	if len(segments) >= 2 {
		artifactID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(artifactID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid artifact id")
			return
		}
		if r.Method == http.MethodGet && len(segments) == 2 && segments[1] == "versions" {
			s.handleArtifactVersionsCompatibility(w, r, artifactID)
			return
		}
		if len(segments) == 2 && segments[1] == "folder" {
			s.handleArtifactCompatibility(w, r)
			return
		}
		if r.Method == http.MethodGet && len(segments) == 2 && segments[1] == "lineage" {
			s.handleArtifactLineageCompatibility(w, r, artifactID)
			return
		}
		if r.Method == http.MethodPost && len(segments) == 2 && segments[1] == "versions" {
			s.handleArtifactTextVersionCompatibility(w, r, artifactID)
			return
		}
		if r.Method == http.MethodPost && len(segments) == 3 &&
			segments[1] == "versions" && segments[2] == "binary" {
			s.handleArtifactBinaryVersionCompatibility(w, r, artifactID)
			return
		}
	}
	s.handleArtifactDownload(w, r)
}

func (s *Server) handleWorkspaceArtifactVersionReadCompatibility(w http.ResponseWriter, r *http.Request) {
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/artifacts/versions/"))
	if r.Method == http.MethodGet && len(segments) == 2 && segments[1] == "verification" {
		versionID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(versionID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid artifact version id")
			return
		}
		checks, found, err := s.workspaceStore.ListArtifactVerificationChecks(versionID, compatAgentUserID(r))
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Artifact version "+versionID+" not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
		return
	}
	if r.Method == http.MethodGet && len(segments) == 2 && segments[1] == "lineage" {
		versionID, err := url.PathUnescape(segments[0])
		if err != nil || strings.TrimSpace(versionID) == "" {
			writeV11Detail(w, http.StatusBadRequest, "Invalid artifact version id")
			return
		}
		s.handleArtifactVersionLineageCompatibility(w, r, versionID)
		return
	}
	s.handleArtifactVersionDownload(w, r)
}

func (s *Server) handleArtifactVersionsCompatibility(w http.ResponseWriter, r *http.Request, artifactID string) {
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	versions, found, err := store.ListCompatibilityArtifactVersions(r.Context(), userID, artifactID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		return
	}
	response := make([]map[string]any, 0, len(versions))
	for _, version := range versions {
		response = append(response, map[string]any{
			"version_id":        version.VersionID,
			"version_number":    version.VersionNumber,
			"artifact_id":       version.ArtifactID,
			"frame_id":          version.FrameID,
			"agent_name":        version.AgentName,
			"language":          version.Language,
			"content_type":      version.ContentType,
			"size_bytes":        version.SizeBytes,
			"created_at":        version.CreatedAt.UTC().Format(time.RFC3339Nano),
			"file_path":         version.FilePath,
			"parent_version_id": version.ParentVersionID,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleArtifactLineageCompatibility(w http.ResponseWriter, r *http.Request, artifactID string) {
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	slim := r.URL.Query().Get("slim") == "1"
	record, found, err := store.GetCurrentArtifactLineageRecord(artifactID, !slim)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		return
	}
	if !artifactLineageProjectOwned(w, store, record, userID) {
		return
	}
	writeJSON(w, http.StatusOK, artifactLineageCompatibilityResponse(record, slim))
}

func (s *Server) handleArtifactVersionLineageCompatibility(w http.ResponseWriter, r *http.Request, versionID string) {
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	slim := r.URL.Query().Get("slim") == "1"
	record, found, err := store.GetArtifactVersionLineageRecord(versionID, !slim)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Version "+versionID+" not found")
		return
	}
	if !artifactLineageProjectOwned(w, store, record, userID) {
		return
	}
	writeJSON(w, http.StatusOK, artifactLineageCompatibilityResponse(record, slim))
}

func (s *Server) artifactCompatibilityRequestContext(w http.ResponseWriter, r *http.Request) (*workspace.Store, string, bool) {
	if s == nil || s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return nil, "", false
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return nil, "", false
	}
	return s.workspaceStore, userID, true
}

func artifactCompatibilityProjectOwned(w http.ResponseWriter, store *workspace.Store, artifact workspace.Artifact, userID string) bool {
	owned, err := store.ProjectOwnedBy(artifact.ProjectID, userID)
	if err != nil {
		writeV11StoreError(w, err)
		return false
	}
	if !owned {
		writeV11Detail(w, http.StatusNotFound, "Artifact "+artifact.ID+" not found")
		return false
	}
	return true
}

func artifactLineageProjectOwned(w http.ResponseWriter, store *workspace.Store, record workspace.ArtifactLineageRecord, userID string) bool {
	owned, err := store.ProjectOwnedBy(record.ProjectID, userID)
	if err != nil {
		writeV11StoreError(w, err)
		return false
	}
	if !owned {
		writeV11Detail(w, http.StatusNotFound, "Artifact "+record.ArtifactID+" not found")
		return false
	}
	return true
}

func artifactLineageCompatibilityResponse(record workspace.ArtifactLineageRecord, slim bool) map[string]any {
	messages := record.Messages
	environment := record.EnvironmentSnapshot
	interactions := record.Interactions
	if slim {
		messages = nil
		environment = nil
		interactions = nil
	}
	return map[string]any{
		"artifact_id":          record.ArtifactID,
		"version_id":           record.VersionID,
		"version_number":       record.VersionNumber,
		"filename":             record.Filename,
		"code":                 record.Code,
		"code_description":     record.CodeDescription,
		"messages":             messages,
		"environment_snapshot": environment,
		"language":             record.Language,
		"interactions":         interactions,
		"has_cell_sources":     record.HasCellSources,
		"has_messages":         record.HasMessages,
		"has_environment":      record.HasEnvironment,
		"pending":              record.Pending,
		"dependency_mappings":  record.DependencyMappings,
	}
}
