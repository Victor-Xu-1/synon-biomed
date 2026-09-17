package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	compute "synon-go/internal/compute"
	workspace "synon-go/internal/persistence/workspace"
)

const computeProbeTimeout = 10 * time.Second

var inferenceProviderNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type inferenceProviderCreateRequest struct {
	Name           string          `json:"name"`
	Endpoint       json.RawMessage `json:"endpoint"`
	SkillName      string          `json:"skillName"`
	CredentialName string          `json:"credentialName,omitempty"`
}

func (s *Server) handleInferenceProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	var input inferenceProviderCreateRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	name := strings.TrimSpace(input.Name)
	if !inferenceProviderNamePattern.MatchString(name) {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("name must match ^[a-z0-9][a-z0-9-]{0,63}$"))
		return
	}
	skillName := strings.TrimSpace(input.SkillName)
	if len(skillName) == 0 || len(skillName) > 128 {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("skillName must contain 1 to 128 characters"))
		return
	}
	credentialName := strings.TrimSpace(input.CredentialName)
	if len(credentialName) > 128 {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("credentialName must contain at most 128 characters"))
		return
	}
	endpoint, hosted, err := normalizeInferenceEndpoint(input.Endpoint)
	if err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	_, err = s.workspaceStore.CreateInferenceProvider(workspace.ComputeProviderInput{
		Name:           "infer:" + name,
		UserID:         compatAgentUserID(r),
		Family:         "infer",
		Endpoint:       endpoint,
		SkillName:      skillName,
		CredentialName: credentialName,
		Hosted:         hosted,
		Environments:   []string{},
		Scheduler:      "none",
	})
	if err != nil {
		writeAgentCompatError(w, http.StatusConflict, errors.New("compute provider name is unavailable"))
		return
	}
	// The frontend probes immediately. Starting another asynchronous probe here
	// creates duplicate network work and was the source of the v1.1 CAS race.
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleInferenceProvider(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/compute/inference-providers/"), "/")
	if name == "" {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("inference provider not found"))
		return
	}
	if !strings.HasPrefix(name, "infer:") {
		name = "infer:" + name
	}
	if r.Method != http.MethodDelete {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	deleted, err := s.workspaceStore.DeleteComputeProvider(name, compatAgentUserID(r))
	if err != nil {
		writeAgentCompatError(w, http.StatusConflict, err)
		return
	}
	if !deleted {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("inference provider not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleComputeProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	providers, err := s.workspaceStore.ListComputeProviders(compatAgentUserID(r))
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, errors.New("failed to list compute providers"))
		return
	}
	response := make([]map[string]any, 0, len(providers))
	for _, provider := range providers {
		response = append(response, computeProviderListProjection(provider))
	}
	writeJSON(w, http.StatusOK, response)
}

func computeProviderListProjection(provider workspace.ComputeProvider) map[string]any {
	displayName := provider.Name
	if provider.Family == "infer" {
		displayName = strings.TrimPrefix(provider.Name, "infer:")
	}
	location := "local"
	if provider.Hosted {
		location = "hosted"
	}
	result := map[string]any{
		"name":        provider.Name,
		"displayName": displayName,
		"family":      provider.Family,
		"location":    location,
		"checked":     true,
	}
	if provider.Endpoint != "" {
		result["endpoint"] = provider.Endpoint
	}
	if provider.SkillName != "" {
		result["skillName"] = provider.SkillName
	}
	if strings.HasPrefix(provider.DetailsMD, "Probe failed:") {
		result["probeError"] = strings.TrimSpace(strings.TrimPrefix(provider.DetailsMD, "Probe failed:"))
	}
	return result
}

func (s *Server) handleComputeProvider(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/compute/providers/"), "/")
	if path == "" {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("compute provider not found"))
		return
	}
	parts := strings.Split(path, "/")
	name := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	} else if len(parts) != 1 {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("compute provider route not found"))
		return
	}
	switch action {
	case "probe":
		if r.Method == http.MethodPost {
			s.handleComputeProviderProbe(w, r, name)
			return
		}
	case "data-roots":
		if r.Method == http.MethodPut {
			s.handleComputeProviderDataRoots(w, r, name)
			return
		}
	case "scratch-root":
		if r.Method == http.MethodPut {
			s.handleComputeProviderScratchRoot(w, r, name)
			return
		}
	case "files":
		if r.Method == http.MethodGet {
			s.handleComputeRemoteFiles(w, r, name)
			return
		}
	case "import":
		if r.Method == http.MethodPost {
			s.handleComputeRemoteImport(w, r, name)
			return
		}
	case "download":
		if r.Method == http.MethodGet {
			s.handleComputeRemoteDownload(w, r, name)
			return
		}
	case "":
		switch r.Method {
		case http.MethodGet:
			provider, ok := s.computeProviderForRequest(w, r, name)
			if ok {
				writeJSON(w, http.StatusOK, s.computeProviderDetails(r, provider))
			}
			return
		case http.MethodPatch:
			s.handleComputeProviderPatch(w, r, name)
			return
		case http.MethodDelete:
			s.handleComputeProviderDelete(w, r, name)
			return
		}
	}
	writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func (s *Server) handleComputeSSHHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	var input compute.SSHHostRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	prepared, err := compute.PrepareSSHHost(filepath.Join(home, ".ssh", "config"), home, input)
	if err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	details := ""
	if prepared.InitialContext != "" {
		details = "## User-provided context\n" + prepared.InitialContext
	}
	_, err = s.workspaceStore.UpsertSSHProvider(workspace.ComputeProviderInput{
		Name: "ssh:" + prepared.Alias, UserID: compatAgentUserID(r), Family: "ssh",
		DataRoots: prepared.DataRoots, SSHOverrides: prepared.Overrides, DetailsMD: details,
	})
	if err != nil {
		writeAgentCompatError(w, http.StatusConflict, errors.New("SSH provider name is unavailable"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) computeSSHProviderForRequest(w http.ResponseWriter, r *http.Request, name string) (workspace.ComputeProvider, string, bool) {
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return workspace.ComputeProvider{}, "", false
	}
	provider, found, err := s.workspaceStore.GetComputeProvider(name, compatAgentUserID(r))
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, errors.New("failed to read compute provider"))
		return workspace.ComputeProvider{}, "", false
	}
	if !found || provider.Family != "ssh" || !strings.HasPrefix(provider.Name, "ssh:") {
		writeAgentCompatError(w, http.StatusBadRequest, fmt.Errorf(
			"listRemoteDirectory/importRemoteFile: provider '%s' is not an SSH host", name,
		))
		return workspace.ComputeProvider{}, "", false
	}
	return provider, strings.TrimPrefix(provider.Name, "ssh:"), true
}

func (s *Server) handleComputeRemoteFiles(w http.ResponseWriter, r *http.Request, name string) {
	if strings.HasPrefix(name, "byoc:") {
		listing, err := s.listBYOCRemoteDirectory(r.Context(), compatAgentUserID(r), name, r.URL.Query().Get("path"))
		if err != nil {
			writeComputeRemoteError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, listing)
		return
	}
	provider, alias, ok := s.computeSSHProviderForRequest(w, r, name)
	if !ok {
		return
	}
	requestedPath := r.URL.Query().Get("path")
	listing, err := compute.ListRemoteDirectory(r.Context(), s.computeRemoteDialer, alias, requestedPath, provider.ScratchRoot, "")
	if err != nil {
		writeComputeRemoteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) handleComputeRemoteDownload(w http.ResponseWriter, r *http.Request, name string) {
	var download compute.RemoteDownload
	var err error
	if strings.HasPrefix(name, "byoc:") {
		download, err = s.fetchBYOCRemoteFile(r.Context(), compatAgentUserID(r), name, r.URL.Query().Get("path"), compute.RemoteDownloadMaxBytes)
	} else {
		_, alias, ok := s.computeSSHProviderForRequest(w, r, name)
		if !ok {
			return
		}
		download, err = compute.FetchRemoteFile(r.Context(), s.computeRemoteDialer, alias, r.URL.Query().Get("path"), compute.RemoteDownloadMaxBytes)
	}
	if err != nil {
		writeComputeRemoteError(w, err)
		return
	}
	defer download.Cleanup()
	file, err := os.Open(download.Path)
	if err != nil {
		writeComputeRemoteError(w, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeComputeRemoteError(w, err)
		return
	}
	disposition := "attachment"
	if r.URL.Query().Get("disposition") == "inline" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": download.Filename}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, download.Filename, info.ModTime(), file)
}

func (s *Server) handleComputeRemoteImport(w http.ResponseWriter, r *http.Request, name string) {
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	var body struct {
		Path      string `json:"path"`
		ProjectID string `json:"projectId"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil || strings.TrimSpace(body.Path) == "" || strings.TrimSpace(body.ProjectID) == "" {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("path and projectId are required"))
		return
	}
	userID := compatAgentUserID(r)
	owned, err := s.workspaceStore.ProjectOwnedBy(body.ProjectID, userID)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	if !owned {
		writeAgentCompatError(w, http.StatusNotFound, fmt.Errorf("Project %s not found", body.ProjectID))
		return
	}
	rootID, found, err := s.workspaceStore.ComputeProjectRoot(userID, body.ProjectID)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("project has no root frame"))
		return
	}
	var download compute.RemoteDownload
	if strings.HasPrefix(name, "byoc:") {
		download, err = s.fetchBYOCRemoteFile(r.Context(), userID, name, body.Path, compute.RemoteImportMaxBytes)
	} else {
		_, alias, ok := s.computeSSHProviderForRequest(w, r, name)
		if !ok {
			return
		}
		download, err = compute.FetchRemoteFile(r.Context(), s.computeRemoteDialer, alias, body.Path, compute.RemoteImportMaxBytes)
	}
	if err != nil {
		writeComputeRemoteError(w, err)
		return
	}
	defer download.Cleanup()
	file, err := os.Open(download.Path)
	if err != nil {
		writeComputeRemoteError(w, err)
		return
	}
	defer file.Close()
	contentType := mime.TypeByExtension(filepath.Ext(download.Filename))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if index := strings.IndexByte(contentType, ';'); index >= 0 {
		contentType = contentType[:index]
	}
	mutationContext, artifactID := computeArtifactMutation(r, userID, "remote-import:"+name)
	artifact, version, err := s.workspaceStore.WriteArtifactVersionRealtime(mutationContext, workspace.WriteArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: body.ProjectID, Name: download.Filename,
		ContentType: contentType, Content: file, MaxBytes: compute.RemoteImportMaxBytes,
		FreshUserEditMappings: true, RootFrameID: rootID, FrameID: rootID, IsUserUpload: true,
		Interactions: computeArtifactSourceInteraction("compute_remote", name, body.Path),
	}, userID)
	if err != nil {
		writeComputeArtifactMutationError(w, err)
		return
	}
	filePath, err := s.workspaceStore.ArtifactVersionFilePath(version)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, computeArtifactImportResponse(artifact, version, rootID, filePath))
}

func writeComputeRemoteError(w http.ResponseWriter, err error) {
	var remote *compute.RemoteError
	if !errors.As(err, &remote) {
		remote = &compute.RemoteError{Kind: "other", Message: err.Error()}
	}
	status := http.StatusBadGateway
	switch remote.Kind {
	case "not_found", "not_a_directory":
		status = http.StatusNotFound
	case "permission":
		status = http.StatusForbidden
	case "not_a_file", "too_large", "outside_roots":
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{"detail": remote.Message, "remoteKind": remote.Kind})
}

func (s *Server) handleComputeProviderPatch(w http.ResponseWriter, r *http.Request, name string) {
	provider, ok := s.computeProviderForMutation(w, r, name)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	details := provider.DetailsMD
	if provider.Family == "byoc" {
		s.handleBYOCProviderPatch(w, r, provider, body)
		return
	}
	maxJobs := provider.MaxConcurrentJobs
	maxTimeout := provider.MaxTimeoutSec
	for key, raw := range body {
		switch key {
		case "detailsMd":
			if err := json.Unmarshal(raw, &details); err != nil {
				writeAgentCompatError(w, http.StatusBadRequest, errors.New("detailsMd must be a string"))
				return
			}
		case "maxConcurrentJobs":
			if err := decodeNullableInt(raw, &maxJobs, 0, 64); err != nil {
				writeAgentCompatError(w, http.StatusBadRequest, fmt.Errorf("maxConcurrentJobs: %w", err))
				return
			}
		case "maxTimeoutSec":
			if err := decodeNullableInt(raw, &maxTimeout, 60, 259200); err != nil {
				writeAgentCompatError(w, http.StatusBadRequest, fmt.Errorf("maxTimeoutSec: %w", err))
				return
			}
		default:
			writeAgentCompatError(w, http.StatusBadRequest, fmt.Errorf("unsupported provider field %q", key))
			return
		}
	}
	if provider.Family != "ssh" && provider.Family != "byoc" && (maxJobs != provider.MaxConcurrentJobs || maxTimeout != provider.MaxTimeoutSec) {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("provider limits are only configurable for SSH/BYOC"))
		return
	}
	if _, err := s.workspaceStore.UpdateComputeProviderSettings(provider.Name, provider.UserID, details, maxJobs, maxTimeout); err != nil {
		writeComputeProviderMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeNullableInt(raw json.RawMessage, target **int, minimum, maximum int) error {
	if string(raw) == "null" {
		*target = nil
		return nil
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < minimum || value > maximum {
		return fmt.Errorf("must be null or an integer from %d to %d", minimum, maximum)
	}
	*target = &value
	return nil
}

func (s *Server) handleComputeProviderDelete(w http.ResponseWriter, r *http.Request, name string) {
	provider, ok := s.computeProviderForMutation(w, r, name)
	if !ok {
		return
	}
	deleted, err := s.workspaceStore.DeleteComputeProvider(provider.Name, provider.UserID)
	if err != nil {
		writeAgentCompatError(w, http.StatusConflict, err)
		return
	}
	if !deleted {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("compute provider not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleComputeProviderDataRoots(w http.ResponseWriter, r *http.Request, name string) {
	provider, ok := s.computeProviderForMutation(w, r, name)
	if !ok {
		return
	}
	var body struct {
		Roots *[]string `json:"roots"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil || body.Roots == nil {
		if err == nil {
			err = errors.New("roots is required")
		}
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	roots := make([]string, 0, len(*body.Roots))
	for _, root := range *body.Roots {
		root = strings.TrimSpace(root)
		if root != "" && path.IsAbs(root) {
			roots = append(roots, path.Clean(root))
		}
	}
	if _, err := s.workspaceStore.SetComputeProviderDataRoots(provider.Name, provider.UserID, roots); err != nil {
		writeComputeProviderMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleComputeProviderScratchRoot(w http.ResponseWriter, r *http.Request, name string) {
	provider, ok := s.computeProviderForMutation(w, r, name)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	raw, present := body["scratchRoot"]
	if !present || len(body) != 1 {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("scratchRoot is required and is the only supported field"))
		return
	}
	var scratchRoot *string
	if string(raw) != "null" {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("scratchRoot must be null or a string"))
			return
		}
		value = strings.TrimSpace(value)
		if len(value) == 0 || len(value) > 512 || !path.IsAbs(value) || strings.ContainsRune(value, 0) {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("scratchRoot must be null or an absolute path of at most 512 characters"))
			return
		}
		value = path.Clean(value)
		scratchRoot = &value
	}
	if provider.Family != "ssh" {
		writeAgentCompatError(w, http.StatusBadRequest, fmt.Errorf("scratch_root is only configurable for SSH providers (got family=%s)", provider.Family))
		return
	}
	if _, err := s.workspaceStore.SetComputeProviderScratchRoot(provider.Name, provider.UserID, scratchRoot); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAgentCompatError(w, http.StatusNotFound, errors.New("compute provider not found"))
			return
		}
		writeComputeProviderMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) computeProviderDetails(r *http.Request, provider workspace.ComputeProvider) map[string]any {
	result := map[string]any{
		"name": provider.Name, "family": provider.Family, "detailsMd": provider.DetailsMD,
		"dataRoots": provider.DataRoots, "scratchRoot": nil, "probedAt": nil,
		"maxConcurrentJobs": provider.MaxConcurrentJobs, "maxTimeoutSec": provider.MaxTimeoutSec,
	}
	scratchRootSource := "probe"
	if provider.ScratchRoot != "" {
		scratchRootSource = "user"
	}
	result["scratchRootSource"] = scratchRootSource
	if provider.ScratchRoot != "" {
		result["scratchRoot"] = provider.ScratchRoot
	}
	if provider.ProbedAt != nil {
		result["probedAt"] = provider.ProbedAt.UnixMilli()
	}
	if provider.Family == "byoc" {
		settings, found, err := s.workspaceStore.GetBYOCSettings(strings.TrimPrefix(provider.Name, "byoc:"), provider.UserID)
		if err == nil && found {
			result["detailsMd"] = settings.DetailsMD
			result["maxConcurrentJobs"] = settings.MaxConcurrentJobs
			result["maxTimeoutSec"] = settings.MaxTimeoutSec
			result["appName"] = nullableString(settings.AppName)
			result["egressPolicy"] = settings.EgressPolicy
			result["environmentName"] = nullableString(settings.EnvironmentName)
		}
	}
	if provider.Family == "infer" {
		result["inferConfig"] = map[string]any{"url": provider.Endpoint, "skillName": provider.SkillName, "hosted": provider.Hosted}
		if provider.CredentialName != "" {
			resolved := false
			if s.secretStore != nil {
				if _, found, err := s.secretStore.ResolveForUser(provider.CredentialName, compatAgentUserID(r)); err == nil {
					resolved = found
				}
			}
			result["credentialStatus"] = map[string]any{"name": provider.CredentialName, "resolved": resolved}
		}
	}
	return result
}

func (s *Server) handleComputeProviderProbe(w http.ResponseWriter, r *http.Request, name string) {
	provider, ok := s.computeProviderForRequest(w, r, name)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), computeProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, provider.Endpoint, nil)
	if err == nil {
		var response *http.Response
		response, err = s.httpClient.Do(request)
		if response != nil && response.Body != nil {
			_, _ = io.CopyN(io.Discard, response.Body, 4096)
			_ = response.Body.Close()
		}
	}
	if err != nil {
		details := "Probe failed: endpoint is unreachable."
		if _, storeErr := s.workspaceStore.RecordComputeProbe(provider.Name, provider.UserID, details, nil); storeErr != nil {
			writeComputeProviderMutationError(w, storeErr)
			return
		}
		writeAgentCompatError(w, http.StatusBadGateway, errors.New("probe failed: endpoint is unreachable"))
		return
	}
	probedAt := time.Now().UTC()
	details := "Probe succeeded at " + probedAt.Format(time.RFC3339Nano) + "."
	if _, err := s.workspaceStore.RecordComputeProbe(provider.Name, provider.UserID, details, &probedAt); err != nil {
		writeComputeProviderMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scheduler":       "none",
		"cpus":            0,
		"gpus":            0,
		"memMb":           0,
		"packageManagers": []string{},
	})
}

func (s *Server) computeProviderForRequest(w http.ResponseWriter, r *http.Request, name string) (workspace.ComputeProvider, bool) {
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return workspace.ComputeProvider{}, false
	}
	provider, found, err := s.workspaceStore.GetComputeProvider(name, compatAgentUserID(r))
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, errors.New("failed to read compute provider"))
		return workspace.ComputeProvider{}, false
	}
	if !found {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("compute provider not found"))
		return workspace.ComputeProvider{}, false
	}
	return provider, true
}

func (s *Server) computeProviderForMutation(w http.ResponseWriter, r *http.Request, name string) (workspace.ComputeProvider, bool) {
	provider, ok := s.computeProviderForRequest(w, r, name)
	if !ok {
		return workspace.ComputeProvider{}, false
	}
	if provider.UserID != compatAgentUserID(r) {
		writeAgentCompatError(w, http.StatusForbidden, errors.New("global compute providers are read-only"))
		return workspace.ComputeProvider{}, false
	}
	return provider, true
}

func writeComputeProviderMutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("compute provider not found"))
		return
	}
	writeAgentCompatError(w, http.StatusConflict, errors.New("compute provider changed concurrently"))
}

func normalizeInferenceEndpoint(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, errors.New("endpoint is required")
	}
	var port int
	if err := json.Unmarshal(raw, &port); err == nil {
		if port < 1 || port > 65535 || string(raw) != strconv.Itoa(port) {
			return "", false, errors.New("numeric endpoint must be an integer from 1 to 65535")
		}
		return fmt.Sprintf("http://127.0.0.1:%d", port), false, nil
	}
	var endpoint string
	if err := json.Unmarshal(raw, &endpoint); err != nil {
		return "", false, errors.New("endpoint must be an HTTP URL or port number")
	}
	endpoint = strings.TrimSpace(endpoint)
	if len(endpoint) == 0 || len(endpoint) > 2048 {
		return "", false, errors.New("endpoint must contain 1 to 2048 characters")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", false, errors.New("endpoint must be an absolute HTTP or HTTPS URL without user info")
	}
	host := strings.Trim(parsed.Hostname(), "[]")
	ip := net.ParseIP(host)
	hosted := !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())
	return parsed.String(), hosted, nil
}
