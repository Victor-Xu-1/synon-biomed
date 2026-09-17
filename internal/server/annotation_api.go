package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	agentruntime "synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/providers"
)

const maxAnnotationAnchorLength = 4096
const maxAnnotationDocumentBytes = 10 << 20

type annotationTarget struct {
	Kind       string
	ArtifactID string
	VersionID  string
	AbsPath    string
	Provider   string
	Path       string
	Checksum   string
}

func (s *Server) handleArtifactAnnotationOperation(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string, segments []string) {
	if len(segments) < 4 || len(segments) > 5 {
		writeAnnotationError(w, http.StatusNotFound, "annotation endpoint not found")
		return
	}
	artifactID, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(artifactID) == "" {
		writeAnnotationError(w, http.StatusBadRequest, "invalid artifact id")
		return
	}
	versionID, err := url.PathUnescape(segments[2])
	if err != nil || strings.TrimSpace(versionID) == "" {
		writeAnnotationError(w, http.StatusBadRequest, "invalid artifact version id")
		return
	}
	target := annotationTarget{Kind: "artifact", ArtifactID: artifactID, VersionID: versionID}
	artifact, version, err := resolveAnnotationTarget(store, userID, "", target)
	if err != nil {
		writeAnnotationStoreError(w, err)
		return
	}
	switch segments[3] {
	case "annotations":
		targetKey := "av:" + version.ID
		if len(segments) == 4 {
			switch r.Method {
			case http.MethodGet:
				s.writeAnnotationList(w, store, artifact.ProjectID, targetKey, nil)
			case http.MethodPost:
				s.createAnnotation(w, r, store, artifact.ProjectID, "artifact", targetKey, version.ContentSHA256)
			default:
				writeAnnotationError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
		annotationID, err := url.PathUnescape(segments[4])
		if err != nil {
			writeAnnotationError(w, http.StatusBadRequest, "invalid annotation id")
			return
		}
		s.handleAnnotationMutation(w, r, store, userID, annotationID, targetKey)
	case "suggest-edits":
		if len(segments) != 4 || r.Method != http.MethodPost {
			writeAnnotationError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		content, err := readAnnotationVersion(store, version.ID, 10000)
		if err != nil {
			writeAnnotationStoreError(w, err)
			return
		}
		version.Content = content
		s.handleSuggestEdits(w, r, artifact, version)
	case "apply-edit":
		if len(segments) != 4 || r.Method != http.MethodPost {
			writeAnnotationError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if version.SizeBytes > maxAnnotationDocumentBytes {
			writeAnnotationError(w, http.StatusRequestEntityTooLarge, "artifact text exceeds 10 MiB edit limit")
			return
		}
		content, err := readAnnotationVersion(store, version.ID, maxAnnotationDocumentBytes)
		if err != nil {
			writeAnnotationStoreError(w, err)
			return
		}
		version.Content = content
		s.handleApplyEdit(w, r, store, userID, artifact, version)
	default:
		writeAnnotationError(w, http.StatusNotFound, "annotation endpoint not found")
	}
}

func (s *Server) handleProjectAnnotations(w http.ResponseWriter, r *http.Request, store *workspace.Store, encodedProjectID string) {
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	projectID, err := url.PathUnescape(encodedProjectID)
	if err != nil || strings.TrimSpace(projectID) == "" {
		writeAnnotationError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var input map[string]any
	if err := decodeAnnotationJSON(r, &input); err != nil {
		writeAnnotationError(w, http.StatusBadRequest, err.Error())
		return
	}
	rawTarget, ok := input["target"].(map[string]any)
	if !ok {
		writeAnnotationError(w, http.StatusBadRequest, "target is required")
		return
	}
	target, err := parseAnnotationTarget(rawTarget)
	if err != nil {
		writeAnnotationError(w, http.StatusBadRequest, err.Error())
		return
	}
	artifact, _, err := resolveAnnotationTarget(store, userID, projectID, target)
	if err != nil {
		writeAnnotationStoreError(w, err)
		return
	}
	if target.Kind == "artifact" {
		projectID = artifact.ProjectID
	}
	targetKey := annotationTargetKey(target)
	switch r.Method {
	case http.MethodPost:
		if strings.HasSuffix(r.URL.Path, "/list") {
			var currentChecksum any
			if target.Kind == "local" {
				currentChecksum = s.grantedFileChecksum(userID, target.AbsPath)
			}
			s.writeAnnotationList(w, store, projectID, targetKey, currentChecksum)
			return
		}
		checksum, _ := input["content_checksum"].(string)
		delete(input, "target")
		delete(input, "content_checksum")
		if checksum == "" && target.Kind == "local" {
			checksum = target.Checksum
			if checksum == "" {
				checksum, _ = s.grantedFileChecksum(userID, target.AbsPath).(string)
			}
		}
		s.createAnnotationFromMap(r.Context(), w, store, userID, projectID, target.Kind, targetKey, checksum, input)
	default:
		writeAnnotationError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleAnnotationRecord(w http.ResponseWriter, r *http.Request) {
	store, ok := s.attachmentStore(w)
	if !ok {
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/annotations/"))
	if len(segments) != 1 {
		writeAnnotationError(w, http.StatusNotFound, "annotation endpoint not found")
		return
	}
	id, err := url.PathUnescape(segments[0])
	if err != nil {
		writeAnnotationError(w, http.StatusBadRequest, "invalid annotation id")
		return
	}
	s.handleAnnotationMutation(w, r, store, userID, id, "")
}

func (s *Server) handleAnnotationMutation(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, id, expectedTargetKey string) {
	if _, err := uuid.Parse(strings.TrimSpace(id)); err != nil {
		writeAnnotationError(w, http.StatusBadRequest, "annotation id must be a UUID")
		return
	}
	annotation, found, err := store.GetAnnotation(id)
	if err != nil {
		writeAnnotationStoreError(w, err)
		return
	}
	if !found || expectedTargetKey != "" && annotation.TargetKey != expectedTargetKey {
		writeAnnotationError(w, http.StatusNotFound, "Annotation "+id+" not found")
		return
	}
	owned, err := store.ProjectOwnedBy(annotation.ProjectID, userID)
	if err != nil {
		writeAnnotationStoreError(w, err)
		return
	}
	if !owned {
		writeAnnotationError(w, http.StatusNotFound, "Annotation "+id+" not found")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var input map[string]any
		if err := decodeAnnotationJSON(r, &input); err != nil {
			writeAnnotationError(w, http.StatusBadRequest, err.Error())
			return
		}
		text, ok := input["text"].(string)
		if !ok || len([]rune(text)) < 1 || len([]rune(text)) > 1000 {
			writeAnnotationError(w, http.StatusBadRequest, "text is required (1-1000 chars)")
			return
		}
		if len(input) != 1 {
			writeAnnotationError(w, http.StatusBadRequest, "only annotation text can be updated")
			return
		}
		updated, err := store.UpdateAnnotationTextOwned(workspaceMutationContext(r), id, userID, text)
		if err != nil {
			writeAnnotationStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, updated.Public())
	case http.MethodDelete:
		deleted, err := store.DeleteAnnotationOwned(workspaceMutationContext(r), id, userID)
		if err != nil {
			writeAnnotationStoreError(w, err)
			return
		}
		if !deleted {
			writeAnnotationError(w, http.StatusNotFound, "Annotation "+id+" not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeAnnotationError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) createAnnotation(w http.ResponseWriter, r *http.Request, store *workspace.Store, projectID, kind, targetKey, checksum string) {
	var input map[string]any
	if err := decodeAnnotationJSON(r, &input); err != nil {
		writeAnnotationError(w, http.StatusBadRequest, err.Error())
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	s.createAnnotationFromMap(r.Context(), w, store, userID, projectID, kind, targetKey, checksum, input)
}

func (s *Server) createAnnotationFromMap(ctx context.Context, w http.ResponseWriter, store *workspace.Store, ownerUserID, projectID, kind, targetKey, checksum string, input map[string]any) {
	if checksum != "" && !isSHA256Hex(checksum) {
		writeAnnotationError(w, http.StatusBadRequest, "content_checksum must be a 64-character SHA-256")
		return
	}
	body, err := normalizeAnnotationBody(input)
	if err != nil {
		writeAnnotationError(w, http.StatusBadRequest, err.Error())
		return
	}
	annotation, err := store.CreateAnnotationOwned(ctx, workspace.CreateAnnotationInput{
		ProjectID: projectID, TargetKind: kind, TargetKey: targetKey,
		ContentChecksum: strings.ToLower(checksum), Body: body,
	}, ownerUserID)
	if err != nil {
		writeAnnotationStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, annotation.Public())
}

func (s *Server) writeAnnotationList(w http.ResponseWriter, store *workspace.Store, projectID, targetKey string, currentChecksum any) {
	annotations, err := store.ListAnnotations(projectID, targetKey)
	if err != nil {
		writeAnnotationStoreError(w, err)
		return
	}
	output := make([]map[string]any, 0, len(annotations))
	for _, annotation := range annotations {
		output = append(output, annotation.Public())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"target_key": targetKey, "annotations": output, "current_checksum": currentChecksum,
	})
}

func (s *Server) handleSuggestEdits(w http.ResponseWriter, r *http.Request, artifact workspace.Artifact, version workspace.ArtifactVersion) {
	if !textArtifactKind(artifact.Kind, artifact.Name) || !utf8.Valid(version.Content) {
		writeAnnotationError(w, http.StatusBadRequest, "suggest-edits requires a text artifact")
		return
	}
	var input map[string]any
	if err := decodeAnnotationJSON(r, &input); err != nil {
		writeAnnotationError(w, http.StatusBadRequest, err.Error())
		return
	}
	selectedText, ok := input["selected_text"].(string)
	if !ok {
		writeAnnotationError(w, http.StatusBadRequest, "selected_text is required")
		return
	}
	annotationText, ok := input["annotation_text"].(string)
	if !ok || strings.TrimSpace(annotationText) == "" {
		writeAnnotationError(w, http.StatusBadRequest, "annotation_text is required")
		return
	}
	mode, _ := input["mode"].(string)
	if mode == "" {
		mode = "edit"
	}
	if mode != "edit" && mode != "ask" {
		writeAnnotationError(w, http.StatusBadRequest, "mode must be edit or ask")
		return
	}
	currentIteration, _ := input["current_iteration"].(string)
	document := string(version.Content)
	if len(document) > 10000 {
		document = document[:10000] + "\n[...truncated...]"
	}
	prompt := annotationEditPrompt(document, selectedText, annotationText, currentIteration, mode)
	suggestion, err := s.annotationLLM(r.Context(), artifact.ProjectID, prompt, mode == "ask")
	if err != nil {
		writeAnnotationStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestion": suggestion})
}

func (s *Server) handleApplyEdit(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string, artifact workspace.Artifact, version workspace.ArtifactVersion) {
	if !textArtifactKind(artifact.Kind, artifact.Name) || !utf8.Valid(version.Content) {
		writeAnnotationError(w, http.StatusBadRequest, "apply-edit requires a text artifact")
		return
	}
	if len(version.Content) > maxAnnotationDocumentBytes {
		writeAnnotationError(w, http.StatusRequestEntityTooLarge, "artifact text exceeds 10 MiB edit limit")
		return
	}
	var input map[string]any
	if err := decodeAnnotationJSON(r, &input); err != nil {
		writeAnnotationError(w, http.StatusBadRequest, err.Error())
		return
	}
	selected, selectedOK := input["selected_text"].(string)
	replacement, replacementOK := input["replacement_text"].(string)
	if !selectedOK || !replacementOK {
		writeAnnotationError(w, http.StatusBadRequest, "selected_text and replacement_text are required")
		return
	}
	before, _ := input["context_before"].(string)
	after, _ := input["context_after"].(string)
	start, end, found := locateAnnotationSelection(string(version.Content), selected, before, after)
	if !found {
		raw, err := s.resolveAnnotationSelectionWithLLM(r.Context(), artifact.ProjectID, string(version.Content), selected, before, after)
		if err != nil {
			writeAnnotationError(w, http.StatusBadRequest, "Selected text not found in raw content. The selection may span formatting that cannot be matched.")
			return
		}
		start, end, found = locateAnnotationSelection(string(version.Content), raw, before, after)
		if !found {
			writeAnnotationError(w, http.StatusBadRequest, "Selected text not found in raw content. The selection may span formatting that cannot be matched.")
			return
		}
	}
	content := append([]byte(nil), version.Content[:start]...)
	content = append(content, []byte(replacement)...)
	content = append(content, version.Content[end:]...)
	_, created, carried, err := store.ApplyArtifactEditRealtime(workspaceMutationContext(r), workspace.ApplyArtifactEditInput{
		ArtifactID: artifact.ID, ProjectID: artifact.ProjectID, Name: artifact.Name, Kind: artifact.Kind,
		Content: content, CreatedBy: userID, ParentVersionID: version.ID,
		FromAnnotationTargetKey: "av:" + version.ID,
	}, userID)
	if err != nil {
		writeAnnotationStoreError(w, err)
		return
	}
	carriedOutput := make([]map[string]any, 0, len(carried))
	for _, annotation := range carried {
		carriedOutput = append(carriedOutput, annotation.Public())
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"carried_annotations": carriedOutput, "version_id": created.ID,
		"version_number": created.VersionNumber, "artifact_id": artifact.ID,
		"frame_id": nil, "agent_name": created.CreatedBy, "language": nil,
		"content_type": artifact.Kind, "size_bytes": len(created.Content),
		"created_at": created.CreatedAt, "file_path": nil, "parent_version_id": created.ParentID,
	})
}

func resolveAnnotationTarget(store *workspace.Store, userID, projectID string, target annotationTarget) (workspace.Artifact, workspace.ArtifactVersion, error) {
	if target.Kind == "artifact" {
		artifact, found, err := store.GetArtifact(target.ArtifactID)
		if err != nil {
			return workspace.Artifact{}, workspace.ArtifactVersion{}, err
		}
		if !found {
			return workspace.Artifact{}, workspace.ArtifactVersion{}, fmt.Errorf("Artifact %s not found", target.ArtifactID)
		}
		owned, err := store.ProjectOwnedBy(artifact.ProjectID, userID)
		if err != nil {
			return workspace.Artifact{}, workspace.ArtifactVersion{}, err
		}
		if !owned {
			return workspace.Artifact{}, workspace.ArtifactVersion{}, fmt.Errorf("Artifact %s not found", target.ArtifactID)
		}
		versionArtifact, version, found, err := store.GetArtifactVersionMetadata(target.VersionID)
		if err != nil {
			return workspace.Artifact{}, workspace.ArtifactVersion{}, err
		}
		if !found {
			return workspace.Artifact{}, workspace.ArtifactVersion{}, fmt.Errorf("Version %s not found", target.VersionID)
		}
		if versionArtifact.ID != artifact.ID {
			return workspace.Artifact{}, workspace.ArtifactVersion{}, fmt.Errorf("Version %s does not belong to artifact %s", target.VersionID, target.ArtifactID)
		}
		return artifact, version, nil
	}
	if strings.TrimSpace(projectID) == "" {
		return workspace.Artifact{}, workspace.ArtifactVersion{}, errors.New("projectId is required for non-artifact targets")
	}
	owned, err := store.ProjectOwnedBy(projectID, userID)
	if err != nil {
		return workspace.Artifact{}, workspace.ArtifactVersion{}, err
	}
	if !owned {
		return workspace.Artifact{}, workspace.ArtifactVersion{}, fmt.Errorf("project %q not found", projectID)
	}
	return workspace.Artifact{}, workspace.ArtifactVersion{}, nil
}

func readAnnotationVersion(store *workspace.Store, versionID string, limit int) ([]byte, error) {
	_, version, reader, found, err := store.OpenArtifactVersionContent(versionID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("version %q not found", versionID)
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact version: %w", err)
	}
	if len(content) > limit {
		content = content[:limit]
		for removed := 0; removed < utf8.UTFMax-1 && len(content) > 0 && !utf8.Valid(content); removed++ {
			content = content[:len(content)-1]
		}
	}
	if version.SizeBytes <= int64(limit) && int64(len(content)) != version.SizeBytes {
		return nil, errors.New("artifact version ended before its recorded size")
	}
	return content, nil
}

func parseAnnotationTarget(input map[string]any) (annotationTarget, error) {
	kind, _ := input["kind"].(string)
	target := annotationTarget{Kind: kind}
	switch kind {
	case "artifact":
		target.ArtifactID, _ = input["artifactId"].(string)
		target.VersionID, _ = input["versionId"].(string)
		if target.ArtifactID == "" || target.VersionID == "" {
			return annotationTarget{}, errors.New("artifactId and versionId are required")
		}
	case "local":
		target.AbsPath, _ = input["absPath"].(string)
		target.Checksum, _ = input["checksum"].(string)
		if !filepath.IsAbs(target.AbsPath) || strings.ContainsAny(target.AbsPath, "\x00\n\r") || len(target.AbsPath) > 4096 {
			return annotationTarget{}, errors.New("local absPath must be absolute and contain no control characters")
		}
		if target.Checksum != "" && !isSHA256Hex(target.Checksum) {
			return annotationTarget{}, errors.New("local checksum must be a 64-character SHA-256")
		}
	case "remote":
		target.Provider, _ = input["provider"].(string)
		target.Path, _ = input["path"].(string)
		if strings.TrimSpace(target.Provider) == "" || len(target.Provider) > 128 || strings.TrimSpace(target.Path) == "" || len(target.Path) > 4096 {
			return annotationTarget{}, errors.New("remote provider and path are required")
		}
	default:
		return annotationTarget{}, errors.New("annotation target kind must be artifact, local, or remote")
	}
	return target, nil
}

func annotationTargetKey(target annotationTarget) string {
	switch target.Kind {
	case "artifact":
		return "av:" + target.VersionID
	case "local":
		return "file:local:" + target.AbsPath
	case "remote":
		provider := strings.ReplaceAll(strings.ReplaceAll(target.Provider, "%", "%25"), ":", "%3A")
		return "file:" + provider + ":" + target.Path
	default:
		return ""
	}
}

func normalizeAnnotationBody(input map[string]any) (map[string]any, error) {
	kind, _ := input["type"].(string)
	if kind == "" {
		kind = "point"
	}
	if kind != "point" && kind != "text_selection" && kind != "screenshot" && kind != "html_element" {
		return nil, errors.New("annotation type must be point, text_selection, screenshot, or html_element")
	}
	text, ok := input["text"].(string)
	if !ok || len([]rune(text)) < 1 || len([]rune(text)) > 1000 {
		return nil, errors.New("text is required (1-1000 chars)")
	}
	body := map[string]any{"type": kind, "text": text}
	for _, field := range []string{"x_percent", "y_percent", "start_line", "start_col", "end_line", "end_col", "page_number"} {
		value, present, err := annotationNumber(input, field)
		if err != nil {
			return nil, err
		}
		if present {
			body[field] = value
		} else {
			body[field] = nil
		}
	}
	for _, field := range []string{"selection_text", "selection_prefix", "screenshot_artifact_id", "element_selector", "element_descriptor"} {
		value, present, err := annotationString(input, field)
		if err != nil {
			return nil, err
		}
		if present && len([]rune(value)) > maxAnnotationAnchorLength {
			value = string([]rune(value)[:maxAnnotationAnchorLength])
		}
		if present {
			body[field] = value
		} else {
			body[field] = nil
		}
	}
	x, xOK := body["x_percent"].(float64)
	y, yOK := body["y_percent"].(float64)
	if xOK && (x < 0 || x > 100) || yOK && (y < 0 || y > 100) {
		return nil, errors.New("x_percent and y_percent must be in [0, 100]")
	}
	if kind == "point" && (!xOK || !yOK) {
		return nil, errors.New("point annotations require x_percent and y_percent")
	}
	if kind == "text_selection" && strings.TrimSpace(stringAnnotationValue(body["selection_text"])) == "" {
		return nil, errors.New("text_selection annotations require selection_text")
	}
	if kind == "screenshot" && strings.TrimSpace(stringAnnotationValue(body["screenshot_artifact_id"])) == "" {
		return nil, errors.New("screenshot annotations require screenshot_artifact_id")
	}
	if kind == "html_element" && strings.TrimSpace(stringAnnotationValue(body["element_selector"])) == "" {
		return nil, errors.New("html_element annotations require element_selector")
	}
	if page, ok := body["page_number"].(float64); ok && (page <= 0 || page != float64(int(page))) {
		return nil, errors.New("page_number must be a positive integer")
	}
	return body, nil
}

func annotationNumber(input map[string]any, field string) (float64, bool, error) {
	value, exists := input[field]
	if !exists || value == nil {
		return 0, false, nil
	}
	number, ok := value.(float64)
	if !ok {
		return 0, false, fmt.Errorf("%s must be a number", field)
	}
	return number, true, nil
}

func annotationString(input map[string]any, field string) (string, bool, error) {
	value, exists := input[field]
	if !exists || value == nil {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", false, fmt.Errorf("%s must be a string", field)
	}
	return text, true, nil
}

func stringAnnotationValue(value any) string {
	text, _ := value.(string)
	return text
}

func decodeAnnotationJSON(r *http.Request, target *map[string]any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON request")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func (s *Server) annotationLLM(ctx context.Context, projectID, prompt string, ask bool) (string, error) {
	system := "Revise the selected text exactly as requested. Return only the revised text, with no explanation or code fence."
	if ask {
		system = "Answer the user's question about the selected passage accurately and concisely."
	}
	return s.annotationLLMWithSystem(ctx, projectID, prompt, system)
}

func (s *Server) annotationLLMWithSystem(ctx context.Context, projectID, prompt, system string) (string, error) {
	options := normalizeSessionRunnerChatOptions(s.compactSummarizer)
	resolution, err := providers.ResolveRunnerModelProfile(s.settingsStore, s.workspaceStore, s.secretStore, providers.ResolutionInput{
		Context:          ctx,
		ProjectID:        strings.TrimSpace(projectID),
		RequestTimeout:   options.RequestTimeout,
		MaxAttempts:      options.MaxAttempts,
		MaxResponseBytes: options.ModelResponseLimitBytes,
	})
	if err != nil {
		return "", err
	}
	var client agentruntime.ModelClient
	if resolution.Resolved && resolution.ModelProfile != nil {
		client, err = providers.NewRuntimeModelClient(*resolution.ModelProfile, s.httpClient, nil)
		if err != nil {
			return "", err
		}
	} else {
		if strings.TrimSpace(options.Endpoint) == "" || strings.TrimSpace(options.Model) == "" {
			return "", errors.New("model-backed annotation editing is not configured")
		}
		client = agentruntime.OpenAIChatClient{
			Endpoint:       options.Endpoint,
			APIKey:         options.APIKey,
			Model:          options.Model,
			HTTPClient:     s.httpClient,
			RequestTimeout: options.RequestTimeout,
			MaxAttempts:    options.MaxAttempts,
		}
	}
	response, err := client.Complete(ctx, agentruntime.ModelRequest{Messages: []agentruntime.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}})
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(response.Message.Content)
	if result == "" {
		return "", errors.New("LLM returned empty suggestion")
	}
	return result, nil
}

func annotationEditPrompt(document, selected, instruction, current, mode string) string {
	if mode == "ask" {
		return "Full document:\n---\n" + document + "\n---\n\nSelected passage (the user is asking about this):\n" + selected + "\n\nQuestion:\n" + instruction
	}
	prompt := "Full document:\n---\n" + document + "\n---\n\nSelected text (original passage in the document):\n" + selected
	if current != "" {
		prompt += "\n\nCurrent iteration (revise THIS, not the selected text):\n" + current
	}
	return prompt + "\n\nRequested change:\n" + instruction + "\n\nRespond with the revised text only. No explanation, no code fences, no preamble."
}

func (s *Server) resolveAnnotationSelectionWithLLM(ctx context.Context, projectID, document, selected, before, after string) (string, error) {
	prompt := "RAW SOURCE:\n" + document + "\n\nRENDERED SELECTION:\n" + selected + "\n\nCONTEXT BEFORE (rendered):\n" + before + "\n\nCONTEXT AFTER (rendered):\n" + after + "\n\nReturn the exact raw-source substring."
	return s.annotationLLMWithSystem(ctx, projectID, prompt, "Return only the exact contiguous substring from RAW SOURCE that corresponds to the rendered selection. Do not explain or add delimiters.")
}

type normalizedAnnotationRune struct {
	value rune
	start int
	end   int
}

type annotationSelectionCandidate struct {
	start int
	end   int
}

func locateAnnotationSelection(document, selected, contextBefore, contextAfter string) (int, int, bool) {
	candidates := []annotationSelectionCandidate{}
	if selected == "" {
		candidates = append(candidates, annotationSelectionCandidate{})
	} else {
		for offset := 0; offset <= len(document)-len(selected); {
			index := strings.Index(document[offset:], selected)
			if index < 0 {
				break
			}
			start := offset + index
			candidates = append(candidates, annotationSelectionCandidate{start: start, end: start + len(selected)})
			offset = start + len(selected)
		}
	}
	if len(candidates) > 0 {
		selected := chooseAnnotationSelection(document, candidates, contextBefore, contextAfter)
		return selected.start, selected.end, true
	}
	doc := normalizedAnnotationRunes(document)
	needle := normalizedAnnotationRunes(selected)
	if len(needle) == 0 {
		return 0, 0, true
	}
	for start := 0; start+len(needle) <= len(doc); start++ {
		match := true
		for index := range needle {
			if doc[start+index].value != needle[index].value {
				match = false
				break
			}
		}
		if match {
			candidates = append(candidates, annotationSelectionCandidate{
				start: doc[start].start, end: doc[start+len(needle)-1].end,
			})
		}
	}
	if len(candidates) > 0 {
		selected := chooseAnnotationSelection(document, candidates, contextBefore, contextAfter)
		return selected.start, selected.end, true
	}
	return 0, 0, false
}

func chooseAnnotationSelection(document string, candidates []annotationSelectionCandidate, before, after string) annotationSelectionCandidate {
	best := candidates[0]
	bestScore := annotationContextScore(document, best, before, after)
	for _, candidate := range candidates[1:] {
		score := annotationContextScore(document, candidate, before, after)
		if score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best
}

func annotationContextScore(document string, candidate annotationSelectionCandidate, before, after string) int {
	score := 0
	before = strings.Join(strings.Fields(before), " ")
	after = strings.Join(strings.Fields(after), " ")
	if before != "" {
		actual := strings.Join(strings.Fields(document[:candidate.start]), " ")
		if strings.HasSuffix(actual, before) {
			score += len([]rune(before))
		}
	}
	if after != "" {
		actual := strings.Join(strings.Fields(document[candidate.end:]), " ")
		if strings.HasPrefix(actual, after) {
			score += len([]rune(after))
		}
	}
	return score
}

func normalizedAnnotationRunes(value string) []normalizedAnnotationRune {
	output := make([]normalizedAnnotationRune, 0, len(value))
	space := false
	for offset := 0; offset < len(value); {
		r, size := utf8.DecodeRuneInString(value[offset:])
		end := offset + size
		if unicode.IsSpace(r) {
			if space {
				output[len(output)-1].end = end
			} else {
				output = append(output, normalizedAnnotationRune{value: ' ', start: offset, end: end})
				space = true
			}
		} else {
			output = append(output, normalizedAnnotationRune{value: r, start: offset, end: end})
			space = false
		}
		offset = end
	}
	return output
}

func textArtifactKind(kind, name string) bool {
	kind = strings.ToLower(strings.TrimSpace(strings.Split(kind, ";")[0]))
	if strings.HasPrefix(kind, "text/") {
		return true
	}
	switch kind {
	case "application/json", "application/javascript", "application/xml", "application/x-yaml", "application/yaml", "application/x-latex", "application/x-tex", "application/toml", "application/x-sh", "application/x-python":
		return true
	}
	extension := strings.ToLower(filepath.Ext(name))
	switch extension {
	case ".txt", ".md", ".json", ".xml", ".yaml", ".yml", ".js", ".ts", ".py", ".r", ".sh", ".toml", ".tex", ".csv":
		return true
	}
	return false
}

func (s *Server) grantedFileChecksum(userID, path string) any {
	if s.settingsStore == nil {
		return nil
	}
	grants, err := s.loadHostGrants(userID)
	if err != nil {
		return nil
	}
	file, err := openGrantedRegularFile(path, grants)
	if err != nil {
		return nil
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func writeAnnotationStoreError(w http.ResponseWriter, err error) {
	message := err.Error()
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, workspace.ErrMutationIdempotencyConflict):
		status = http.StatusConflict
	case strings.Contains(strings.ToLower(message), "not found"), strings.Contains(message, "does not exist"):
		status = http.StatusNotFound
	case strings.Contains(message, "not configured"), strings.Contains(message, "closed"):
		status = http.StatusServiceUnavailable
	}
	writeAnnotationError(w, status, message)
}

func writeAnnotationError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"detail": message})
}
