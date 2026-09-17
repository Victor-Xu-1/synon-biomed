package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	v11ArtifactTextLimit   = 10 << 20
	v11ArtifactBinaryLimit = 50 << 20
	v11ArtifactTypeLimit   = 256
	v11ArtifactParentLimit = 64
)

var v11TextArtifactTypes = map[string]struct{}{
	"application/json": {}, "application/xml": {}, "application/yaml": {},
	"application/x-yaml": {}, "application/javascript": {},
	"application/x-latex": {}, "application/x-tex": {},
}

func (s *Server) handleArtifactTextVersionCompatibility(w http.ResponseWriter, r *http.Request, artifactID string) {
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	input, err := decodeV11ArtifactJSON(r)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	content, _ := input["content"].(string)
	contentType, _ := input["content_type"].(string)
	parentID, _ := input["parent_version_id"].(string)
	if content == "" {
		writeV11Detail(w, http.StatusBadRequest, "content is required")
		return
	}
	if len([]byte(content)) > v11ArtifactTextLimit {
		writeV11Detail(w, http.StatusBadRequest, "content exceeds 10MB limit")
		return
	}
	if !validateArtifactVersionStrings(w, contentType, parentID) {
		return
	}
	artifact, current, ok := artifactWriteTarget(w, store, artifactID, userID)
	if !ok {
		return
	}
	resolvedType := artifactWriteContentType(artifact)
	if !isV11TextArtifactType(resolvedType) {
		writeV11Detail(w, http.StatusBadRequest, nonEditableArtifactDetail(resolvedType))
		return
	}
	if strings.TrimSpace(contentType) != "" && contentType != currentContentType(artifact) {
		resolvedType = strings.TrimSpace(contentType)
		if !isV11TextArtifactType(resolvedType) {
			writeV11Detail(w, http.StatusBadRequest, nonEditableArtifactDetail(resolvedType))
			return
		}
	}
	if parentID != "" && !artifactParentOwned(w, store, artifactID, parentID) {
		return
	}
	_, version, err := store.WriteArtifactVersionRealtime(workspaceMutationContext(r), workspace.WriteArtifactVersionInput{
		ArtifactID: artifact.ID, ProjectID: artifact.ProjectID, Name: artifact.Name,
		ContentType: resolvedType, Content: strings.NewReader(content),
		MaxBytes: v11ArtifactTextLimit, ParentVersionID: parentID,
		FreshUserEditMappings:         true,
		CarryAnnotationsFromTargetKey: "av:" + firstNonEmpty(parentID, current.ID),
	}, userID)
	if err != nil {
		writeArtifactWriteStoreError(w, err)
		return
	}
	carried, err := store.ListAnnotations(artifact.ProjectID, "av:"+version.ID)
	if err != nil {
		writeArtifactWriteStoreError(w, err)
		return
	}
	response := artifactWriteResponse(version)
	response["carried_annotations"] = artifactWritePublicAnnotations(carried)
	writeJSON(w, http.StatusCreated, response)
	_ = current
}

func (s *Server) handleArtifactBinaryVersionCompatibility(w http.ResponseWriter, r *http.Request, artifactID string) {
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	contentType := strings.TrimSpace(r.URL.Query().Get("content_type"))
	parentID := strings.TrimSpace(r.URL.Query().Get("parent_version_id"))
	branchValues, branchRequested := r.URL.Query()["branch_as_filename"]
	branchFilename := firstArtifactQueryValue(branchValues)
	content, uploadType, interactions, err := readV11ArtifactMultipart(r)
	if err != nil {
		if errors.Is(err, errArtifactBinaryNotMultipart) {
			writeV11Detail(w, http.StatusNotAcceptable, "the request is not multipart")
		} else if errors.Is(err, errArtifactBinaryTooLarge) {
			writeV11Detail(w, http.StatusBadRequest, "content exceeds 50MB limit")
		} else {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if !validateArtifactVersionStrings(w, contentType, parentID) {
		return
	}
	artifact, current, ok := artifactWriteTarget(w, store, artifactID, userID)
	if !ok {
		return
	}
	if parentID != "" && !artifactParentOwned(w, store, artifactID, parentID) {
		return
	}
	if contentType == "" {
		contentType = strings.TrimSpace(uploadType)
	}
	if contentType == "" {
		contentType = currentContentType(artifact)
	}
	writeInput := workspace.WriteArtifactVersionInput{
		ArtifactID: artifact.ID, ProjectID: artifact.ProjectID, Name: artifact.Name,
		ContentType: contentType, Content: bytes.NewReader(content), MaxBytes: v11ArtifactBinaryLimit,
		ParentVersionID: parentID, FreshUserEditMappings: true, Interactions: interactions,
		CarryCellSources: true,
	}
	if branchRequested {
		if !validArtifactBranchFilename(branchFilename) {
			writeV11Detail(w, http.StatusBadRequest, "branch_as_filename must be non-empty")
			return
		}
		sourceID := parentID
		if sourceID == "" {
			sourceID = current.ID
		}
		writeInput.ArtifactID = uuid.NewString()
		writeInput.Name = branchFilename
		writeInput.ParentVersionID = ""
		writeInput.ProvenanceSourceID = sourceID
		writeInput.FreshUserEditMappings = false
		writeInput.BranchSourceArtifactID = artifact.ID
		writeInput.IsBranchMint = true
	}
	_, version, err := store.WriteArtifactVersionRealtime(workspaceMutationContext(r), writeInput, userID)
	if err != nil {
		writeArtifactWriteStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, artifactWriteResponse(version))
}

func artifactWriteTarget(w http.ResponseWriter, store *workspace.Store, artifactID, userID string) (workspace.Artifact, workspace.ArtifactVersion, bool) {
	artifact, current, found, err := store.GetCurrentArtifactVersionMetadata(artifactID)
	if err != nil {
		writeV11StoreError(w, err)
		return workspace.Artifact{}, workspace.ArtifactVersion{}, false
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
		return workspace.Artifact{}, workspace.ArtifactVersion{}, false
	}
	if !artifactCompatibilityProjectOwned(w, store, artifact, userID) {
		return workspace.Artifact{}, workspace.ArtifactVersion{}, false
	}
	return artifact, current, true
}

func artifactParentOwned(w http.ResponseWriter, store *workspace.Store, artifactID, parentID string) bool {
	parentArtifact, _, found, err := store.GetArtifactVersionMetadata(parentID)
	if err != nil {
		writeV11StoreError(w, err)
		return false
	}
	if !found || parentArtifact.ID != artifactID {
		writeV11Detail(w, http.StatusBadRequest, "parent_version_id is not a version of this artifact")
		return false
	}
	return true
}

func validateArtifactVersionStrings(w http.ResponseWriter, contentType, parentID string) bool {
	if len(contentType) > v11ArtifactTypeLimit {
		writeV11Detail(w, http.StatusBadRequest, "content_type must be at most 256 characters")
		return false
	}
	if len(parentID) > v11ArtifactParentLimit {
		writeV11Detail(w, http.StatusBadRequest, "parent_version_id must be at most 64 characters")
		return false
	}
	return true
}

func artifactWriteContentType(artifact workspace.Artifact) string {
	contentType := currentContentType(artifact)
	if isV11TextArtifactType(contentType) {
		return contentType
	}
	if detected := mime.TypeByExtension(strings.ToLower(filepath.Ext(artifact.Name))); detected != "" {
		if separator := strings.IndexByte(detected, ';'); separator >= 0 {
			detected = detected[:separator]
		}
		return detected
	}
	return contentType
}

func currentContentType(artifact workspace.Artifact) string {
	contentType := strings.TrimSpace(artifact.Kind)
	if contentType == "" {
		return "application/octet-stream"
	}
	return contentType
}

func isV11TextArtifactType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	_, ok := v11TextArtifactTypes[contentType]
	return ok
}

func nonEditableArtifactDetail(contentType string) string {
	return fmt.Sprintf("Content type %s is not text-editable. Use text/* or application/{json,xml,yaml,javascript,x-latex}.", contentType)
}

var (
	errArtifactBinaryTooLarge     = errors.New("artifact binary upload too large")
	errArtifactBinaryNotMultipart = errors.New("artifact binary request is not multipart")
)

func readV11ArtifactMultipart(r *http.Request) ([]byte, string, []map[string]any, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, "", nil, errArtifactBinaryNotMultipart
	}
	var content []byte
	var contentType string
	var interactions []map[string]any
	foundFile := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", nil, fmt.Errorf("read multipart upload: %w", err)
		}
		if part.FileName() != "" && !foundFile {
			foundFile = true
			contentType = part.Header.Get("Content-Type")
			content, err = io.ReadAll(io.LimitReader(part, v11ArtifactBinaryLimit+1))
			_ = part.Close()
			if err != nil {
				return nil, "", nil, fmt.Errorf("read uploaded file: %w", err)
			}
			if len(content) > v11ArtifactBinaryLimit {
				return nil, "", nil, errArtifactBinaryTooLarge
			}
			continue
		}
		if part.FormName() == "interactions" {
			raw, err := io.ReadAll(io.LimitReader(part, 1<<20))
			_ = part.Close()
			if err != nil || json.Unmarshal(raw, &interactions) != nil || len(interactions) > 200 {
				return nil, "", nil, errors.New("interactions must be a JSON array with at most 200 entries")
			}
			continue
		}
		_ = part.Close()
	}
	if !foundFile {
		return nil, "", nil, errors.New("No file uploaded")
	}
	if len(content) == 0 {
		return nil, "", nil, errors.New("content is required")
	}
	return content, contentType, interactions, nil
}

func decodeV11ArtifactJSON(r *http.Request) (map[string]any, error) {
	var target map[string]any
	decoder := json.NewDecoder(io.LimitReader(r.Body, v11ArtifactTextLimit+(1<<20)))
	if err := decoder.Decode(&target); err != nil {
		return nil, fmt.Errorf("invalid request body: %w", err)
	}
	return target, nil
}

func validArtifactBranchFilename(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && utf8.ValidString(name) && len([]byte(name)) <= 255 &&
		filepath.Base(name) == name && !strings.ContainsAny(name, "\x00\r\n/\\")
}

func artifactWriteResponse(version workspace.ArtifactVersion) map[string]any {
	return map[string]any{
		"artifact_id":    version.ArtifactID,
		"version_id":     version.ID,
		"version_number": version.VersionNumber,
	}
}

func artifactWritePublicAnnotations(annotations []workspace.Annotation) []map[string]any {
	result := make([]map[string]any, 0, len(annotations))
	for _, annotation := range annotations {
		result = append(result, annotation.Public())
	}
	return result
}

func firstArtifactQueryValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func writeArtifactWriteStoreError(w http.ResponseWriter, err error) {
	detail := err.Error()
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, workspace.ErrInvalidMutationIdempotencyKey):
		status = http.StatusBadRequest
	case errors.Is(err, workspace.ErrMutationIdempotencyConflict):
		status = http.StatusConflict
	case strings.Contains(detail, "exceeds"), strings.Contains(detail, "parent artifact version"):
		status = http.StatusBadRequest
	case strings.Contains(detail, "does not exist"):
		status = http.StatusNotFound
	}
	writeV11Detail(w, status, detail)
}
