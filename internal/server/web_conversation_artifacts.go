package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

type webConversationArtifactReference struct {
	ArtifactID string `json:"artifact_id"`
	VersionID  string `json:"version_id"`
}

func (s *Server) handleWebConversationArtifacts(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	if userID == "" {
		writeWorkspaceJSON(w, http.StatusUnauthorized, map[string]any{"message": "authentication required"})
		return
	}
	var input struct {
		References []webConversationArtifactReference `json:"references"`
		VersionIDs []string                           `json:"version_ids"`
	}
	if err := decodeWebConversationJSON(w, r, &input); err != nil ||
		len(input.References)+len(input.VersionIDs) > 400 {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid artifact reference batch"})
		return
	}
	references := make([]workspace.CompatibilityArtifactVersionReference, 0, len(input.References))
	for _, reference := range input.References {
		references = append(references, workspace.CompatibilityArtifactVersionReference{
			ArtifactID: reference.ArtifactID, VersionID: reference.VersionID,
		})
	}
	rootFrameID := strings.TrimSpace(frame.RootFrameID)
	if rootFrameID == "" {
		rootFrameID = frame.ID
	}
	artifacts, err := s.workspaceStore.ListAvailableCompatibilityConversationArtifactVersionsByReferences(
		r.Context(), userID, frame.ProjectID, rootFrameID, references,
	)
	if err != nil {
		if errors.Is(err, workspace.ErrCompatibilityArtifactReferenceNotFound) {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "artifact reference not found"})
			return
		}
		writeV11StoreError(w, err)
		return
	}
	versionArtifacts, err := s.workspaceStore.ListAvailableCompatibilityConversationArtifactVersionsByVersionIDs(
		r.Context(), userID, frame.ProjectID, rootFrameID, input.VersionIDs,
	)
	if err != nil {
		if errors.Is(err, workspace.ErrCompatibilityArtifactReferenceNotFound) {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "artifact reference not found"})
			return
		}
		writeV11StoreError(w, err)
		return
	}
	artifacts = append(artifacts, versionArtifacts...)
	if len(artifacts) == 0 {
		writeWorkspaceJSON(w, http.StatusOK, []any{})
		return
	}
	files := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		if isRunnerLargeToolResultArtifactID(artifact.ID) || artifact.RetentionMode == "working_data" {
			continue
		}
		files = append(files, webScientificFile(artifact))
	}
	writeWorkspaceJSON(w, http.StatusOK, []map[string]any{{
		"id":              "synonbiomed-files:" + frame.ID,
		"conversation_id": frame.ID,
		"kind":            "scientific_files",
		"status":          "active",
		"payload": map[string]any{
			"project_id": frame.ProjectID, "root_frame_id": rootFrameID, "files": files,
		},
		"created_at": frame.CreatedAt.UnixMilli(),
		"updated_at": frame.UpdatedAt.UnixMilli(),
	}})
}

func webScientificFile(artifact workspace.CompatibilityConversationArtifact) map[string]any {
	return map[string]any{
		"artifact_id": artifact.ID, "version_id": artifact.VersionID,
		"version_number": artifact.VersionNumber, "project_id": artifact.ProjectID,
		"root_frame_id": artifact.RootFrameID, "frame_id": artifact.FrameID,
		"creating_frame_id": artifact.CreatingFrameID, "filename": artifact.Filename,
		"content_type": artifact.ContentType, "size_bytes": artifact.SizeBytes,
		"preview_kind": webScientificPreviewKind(artifact.Filename, artifact.ContentType),
		"content_url": "/api/artifacts/" + url.PathEscape(artifact.ID) + "/versions/" +
			url.PathEscape(artifact.VersionID),
		"created_at": artifact.CreatedAt.UnixMilli(), "updated_at": artifact.CreatedAt.UnixMilli(),
		"agent_name": artifact.AgentName, "is_user_upload": artifact.IsUserUpload,
		"is_intermediate": artifact.IsIntermediate,
	}
}

func webScientificPreviewKind(filename, contentType string) string {
	name := strings.ToLower(strings.TrimSpace(filename))
	mime := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.Contains(mime, "markdown") || hasWebArtifactExtension(name, ".md", ".markdown"):
		return "markdown"
	case hasWebArtifactExtension(name, ".ipynb") || strings.Contains(mime, "ipynb"):
		return "notebook"
	case hasWebArtifactExtension(name, ".h5ad"):
		return "anndata"
	case hasWebArtifactExtension(name, ".h5", ".hdf5") || strings.Contains(mime, "hdf5"):
		return "hdf5"
	case strings.Contains(mime, "json") || hasWebArtifactExtension(name, ".json"):
		return "json"
	case strings.Contains(mime, "csv") || hasWebArtifactExtension(name, ".csv"):
		return "csv"
	case strings.Contains(mime, "pdf") || hasWebArtifactExtension(name, ".pdf"):
		return "pdf"
	case strings.HasPrefix(mime, "image/"):
		return "image"
	case strings.Contains(mime, "pdb"), strings.Contains(mime, "mmcif"), strings.Contains(mime, "chemical/x-cif"),
		strings.Contains(mime, "mdl-molfile"), strings.Contains(mime, "mdl-sdfile"),
		strings.Contains(mime, "x-mol2"), strings.Contains(mime, "x-xyz"),
		hasWebArtifactExtension(name, ".pdb", ".ent", ".cif", ".mmcif", ".bcif", ".sdf", ".mol", ".mol2", ".xyz", ".gro", ".cube"):
		return "structure"
	case strings.Contains(mime, "daylight-smiles"), strings.Contains(mime, "chemical/smiles"),
		hasWebArtifactExtension(name, ".smi", ".smiles", ".cxsmiles"):
		return "molecule"
	case hasWebArtifactExtension(name, ".aln", ".clustal", ".sto", ".stockholm") ||
		(strings.Contains(name, "align") && hasWebArtifactExtension(name, ".fa", ".fasta", ".faa", ".fna")):
		return "msa"
	case hasWebArtifactExtension(name, ".fa", ".fasta", ".faa", ".fna", ".fastq", ".fq", ".gb", ".gbk"):
		return "sequence"
	case hasWebArtifactExtension(name, ".vcf", ".vcf.gz", ".bed", ".bed.gz", ".bam", ".cram", ".bigwig", ".bw"):
		return "genome"
	case hasWebArtifactExtension(name, ".xlsx", ".xls", ".ods", ".tsv") || strings.Contains(mime, "spreadsheet"):
		return "spreadsheet"
	case hasWebArtifactExtension(name, ".tex") || strings.Contains(mime, "latex"):
		return "latex"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	case strings.HasPrefix(mime, "text/") || hasWebArtifactExtension(name, ".txt"):
		return "text"
	default:
		return "binary"
	}
}

func hasWebArtifactExtension(filename string, extensions ...string) bool {
	for _, extension := range extensions {
		if strings.HasSuffix(filename, extension) {
			return true
		}
	}
	return false
}
