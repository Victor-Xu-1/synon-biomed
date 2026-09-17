package server

import (
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	compute "synon-go/internal/compute"
	workspace "synon-go/internal/persistence/workspace"
)

var computeJobIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// registerComputeWorkbenchRoutes is deliberately separate so the compatibility
// surface can be reviewed and registered as one unit in Server.Handler.
func (s *Server) registerComputeWorkbenchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/compute/gpu", s.handleComputeGPU)
	mux.HandleFunc("/api/compute/gpu/enabled", s.handleComputeGPUEnabled)
	mux.HandleFunc("/api/compute/gpu/detect", s.handleComputeGPUDetect)
	mux.HandleFunc("/api/compute/bionemo/enabled", s.handleComputeBioNeMoEnabled)
	mux.HandleFunc("/api/compute/byoc/", s.handleComputeBYOC)
	mux.HandleFunc("/api/compute/managed-endpoints", s.handleComputeManagedEndpoints)
	mux.HandleFunc("/api/compute/managed-endpoints/", s.handleComputeManagedEndpointAction)
	mux.HandleFunc("/api/compute/jobs", s.handleComputeJobs)
	mux.HandleFunc("/api/compute/jobs/", s.handleComputeJob)
	mux.HandleFunc("/api/compute/ssh-config-aliases", s.handleComputeSSHConfigAliases)
	mux.HandleFunc("/api/compute/ssh-hosts", s.handleComputeSSHHosts)
	mux.HandleFunc("/api/compute/local/hostinfo", s.handleComputeLocalHostInfo)
	mux.HandleFunc("/api/compute/local/files", s.handleComputeLocalFiles)
	mux.HandleFunc("/api/compute/local/download", s.handleComputeLocalDownload)
	mux.HandleFunc("/api/compute/local/import", s.handleComputeLocalImport)
	mux.HandleFunc("/api/compute/session/migrate", s.handleComputeSessionMigrate)
	mux.HandleFunc("/api/compute/session/", s.handleComputeSessionEnabled)
}

func (s *Server) handleComputeBioNeMoEnabled(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := s.workspaceStore.GetComputeBioNeMoSettings()
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		s.putComputeBioNeMoEnabled(w, r)
	default:
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (s *Server) putComputeBioNeMoEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled    *bool   `json:"enabled"`
		Mode       *string `json:"mode"`
		HostedHost *string `json:"hostedHost"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil || body.Enabled == nil {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("enabled is required"))
		return
	}
	current, err := s.workspaceStore.GetComputeBioNeMoSettings()
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	mode := current.Mode
	if body.Mode != nil {
		mode = strings.ToLower(strings.TrimSpace(*body.Mode))
	}
	if mode != "hosted" && mode != "local" {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("mode must be hosted or local"))
		return
	}
	host := current.HostedHost
	if body.HostedHost != nil {
		host = *body.HostedHost
	}
	host, err = compute.NormalizeBioNeMoHostedHost(host)
	if err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	if *body.Enabled {
		ok, err := s.hasNVIDIACredential(compatAgentUserID(r))
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("NVIDIA_API_KEY credential is required to enable BioNeMo"))
			return
		}
	}
	var teardown map[string]any
	if !*body.Enabled {
		var complete bool
		teardown, complete, err = s.teardownManagedEndpoints(r, compatAgentUserID(r))
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		if !complete {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": current.Enabled, "incomplete": true, "teardown": teardown})
			return
		}
	}

	settings, err := s.workspaceStore.SetComputeBioNeMoSettings(workspace.ComputeBioNeMoSettings{
		Enabled: *body.Enabled, Mode: mode, HostedHost: host,
	})
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	response := map[string]any{"enabled": settings.Enabled}
	if settings.Enabled {
		response["mode"] = settings.Mode
		response["hostedHost"] = settings.HostedHost
	} else {
		response["teardown"] = teardown
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) hasNVIDIACredential(userID string) (bool, error) {
	if s.secretStore == nil {
		return false, nil
	}
	secrets, err := s.secretStore.ListForUser(userID)
	if err != nil {
		return false, err
	}
	for _, secret := range secrets {
		identity := strings.ToLower(strings.Join([]string{secret.ID, secret.Name, secret.Provider}, " "))
		if strings.Contains(identity, "nvidia") && strings.TrimSpace(secret.Value) != "" {
			return true, nil
		}
		for key, value := range secret.Credentials {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if strings.TrimSpace(value) != "" && (normalized == "nvidia_api_key" || normalized == "api_key" || normalized == "token") && strings.Contains(identity, "nvidia") {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Server) handleComputeLocalDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	download, err := compute.OpenLocalDownload(r.URL.Query().Get("path"), r.URL.Query().Get("confine"), s.fileRoot)
	if err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	defer download.File.Close()
	info, err := download.File.Stat()
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	disposition := "attachment"
	if r.URL.Query().Get("disposition") == "inline" {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": download.Filename}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, download.Filename, info.ModTime(), download.File)
}

func (s *Server) handleComputeLocalImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
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
	download, err := compute.OpenLocalDownload(body.Path, "", s.fileRoot)
	if err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	defer download.File.Close()
	if download.Size > compute.LocalImportMaxBytes {
		writeAgentCompatError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("file exceeds the %d byte import limit", compute.LocalImportMaxBytes))
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(download.Filename))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if index := strings.IndexByte(contentType, ';'); index >= 0 {
		contentType = contentType[:index]
	}
	mutationContext, artifactID := computeArtifactMutation(r, userID, "local-import")
	artifact, version, err := s.workspaceStore.WriteArtifactVersionRealtime(mutationContext, workspace.WriteArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: body.ProjectID, Name: download.Filename,
		ContentType: contentType, Content: download.File, MaxBytes: compute.LocalImportMaxBytes,
		FreshUserEditMappings: true, RootFrameID: rootID, FrameID: rootID, IsUserUpload: true,
		Interactions: computeArtifactSourceInteraction("compute_local", "local", body.Path),
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

func (s *Server) handleComputeLocalFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	listing, err := compute.ListLocalDirectory(r.URL.Query().Get("path"), home)
	if err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) handleComputeLocalHostInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	writeJSON(w, http.StatusOK, compute.DetectLocalHostInfo())
}

func (s *Server) handleComputeSSHConfigAliases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	result, err := compute.DiscoverSSHConfigAliases(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleComputeSessionMigrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	var body struct {
		FromKey string `json:"fromKey"`
		ToKey   string `json:"toKey"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil ||
		!validComputeSessionKey(body.FromKey) || !strings.HasPrefix(strings.TrimSpace(body.FromKey), "draft-") ||
		!validComputeSessionKey(body.ToKey) {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("fromKey must be a draft tab id and toKey is required"))
		return
	}
	userID := compatAgentUserID(r)
	owned, err := s.workspaceStore.ComputeRootOwnedBy(userID, body.ToKey)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	if !owned {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("frame"))
		return
	}
	if err := s.workspaceStore.MigrateSessionComputeProviders(userID, body.FromKey, body.ToKey); err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleComputeGPUDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	writeJSON(w, http.StatusOK, s.hostGPUDetector(r.Context()))
}

func (s *Server) handleComputeGPU(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		s.putComputeGPUEnabled(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	settings, err := s.workspaceStore.GetComputeGPUSettings()
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	if !settings.Enabled {
		writeJSON(w, http.StatusOK, compute.UnavailableGPUInfo())
		return
	}
	writeJSON(w, http.StatusOK, s.hostGPUDetector(r.Context()))
}

func (s *Server) handleComputeGPUEnabled(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := s.workspaceStore.GetComputeGPUSettings()
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		info := s.hostGPUDetector(r.Context())
		name := any(nil)
		if info.GPUName != nil {
			name = *info.GPUName
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": settings.Enabled, "override": settings.Override,
			"present": info.Available, "name": name,
		})
	case http.MethodPut:
		s.putComputeGPUEnabled(w, r)
	default:
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (s *Server) putComputeGPUEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil || body.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body.enabled must be boolean"})
		return
	}
	settings, err := s.workspaceStore.SetComputeGPUEnabled(*body.Enabled)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": settings.Enabled})
}

func (s *Server) handleComputeManagedEndpoints(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	values, err := s.workspaceStore.ListManagedEndpoints(compatAgentUserID(r), r.URL.Query().Get("name"), r.URL.Query().Get("sizes") == "1")
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, values)
}

func (s *Server) handleComputeManagedEndpointAction(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/compute/managed-endpoints/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "stop" {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("managed endpoint not found"))
		return
	}
	if r.Method != http.MethodPost {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	endpoint, shouldRun, err := s.workspaceStore.ClaimManagedEndpointStop(parts[0], compatAgentUserID(r))
	if errors.Is(err, sql.ErrNoRows) {
		writeAgentCompatError(w, http.StatusNotFound, fmt.Errorf("unknown managed endpoint '%s'", parts[0]))
		return
	}
	if errors.Is(err, workspace.ErrManagedEndpointStopInProgress) {
		writeAgentCompatError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	state := endpoint.State
	if shouldRun {
		credential := ""
		if endpoint.CredentialName != nil {
			credential = *endpoint.CredentialName
		}
		result, stopErr := compute.RunApprovedStop(r.Context(), compute.ManagedEndpointRegistration{
			Name: endpoint.Name, URL: endpoint.URL, Port: endpoint.Port,
			CredentialName: credential, SkillName: endpoint.SkillName,
			StartScript: endpoint.StartScript, StopScript: endpoint.StopScript, LivePath: endpoint.LivePath,
		}, endpoint.ApprovedScriptHash, 60*time.Second)

		state, err = s.workspaceStore.FinishManagedEndpointStop(endpoint.ClaimHandle, result.Transcript, stopErr)
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		if stopErr != nil {
			writeAgentCompatError(w, http.StatusBadGateway, stopErr)
			return
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeAgentCompatError(w, http.StatusNotFound, fmt.Errorf("unknown managed endpoint '%s'", parts[0]))
		return
	}
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": state})
}

func (s *Server) stopManagedEndpointForTeardown(r *http.Request, userID, name string) error {
	endpoint, shouldRun, err := s.workspaceStore.ClaimManagedEndpointStop(name, userID)
	if err != nil {
		return err
	}
	if !shouldRun {
		return nil
	}
	credential := ""
	if endpoint.CredentialName != nil {
		credential = *endpoint.CredentialName
	}
	result, stopErr := compute.RunApprovedStop(r.Context(), compute.ManagedEndpointRegistration{
		Name: endpoint.Name, URL: endpoint.URL, Port: endpoint.Port,
		CredentialName: credential, SkillName: endpoint.SkillName,
		StartScript: endpoint.StartScript, StopScript: endpoint.StopScript, LivePath: endpoint.LivePath,
	}, endpoint.ApprovedScriptHash, 60*time.Second)
	_, finishErr := s.workspaceStore.FinishManagedEndpointStop(endpoint.ClaimHandle, result.Transcript, stopErr)
	if finishErr != nil {
		return finishErr
	}
	return stopErr
}

func (s *Server) teardownManagedEndpoints(r *http.Request, userID string) (map[string]any, bool, error) {
	endpoints, err := s.workspaceStore.ListManagedEndpoints(userID, "", false)
	if err != nil {
		return nil, false, err
	}
	removed := make([]string, 0, len(endpoints))
	failed := make([]map[string]string, 0)
	for _, endpoint := range endpoints {
		if stopErr := s.stopManagedEndpointForTeardown(r, userID, endpoint.Name); stopErr != nil {
			failed = append(failed, map[string]string{"name": endpoint.Name, "error": stopErr.Error()})
			continue
		}
		deleted, deleteErr := s.workspaceStore.DeleteManagedEndpoint(endpoint.Name, userID)
		if deleteErr != nil {
			return nil, false, deleteErr
		}
		if deleted {
			removed = append(removed, endpoint.Name)
		}
	}
	return map[string]any{"removed": removed, "failed": failed}, len(failed) == 0, nil
}

func (s *Server) handleComputeJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if projectID == "" {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("projectId is required"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	owned, err := s.workspaceStore.ProjectOwnedBy(projectID, compatAgentUserID(r))
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	if !owned {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("project"))
		return
	}
	limit := workspace.ComputeJobPageDefault
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > workspace.ComputeJobPageMax {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("limit must be an integer from 1 to 200"))
			return
		}
	}
	page, err := s.workspaceStore.ListActiveComputeJobsPage(compatAgentUserID(r), projectID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		if strings.Contains(err.Error(), "cursor") || strings.Contains(err.Error(), "limit") {
			writeAgentCompatError(w, http.StatusBadRequest, err)
		} else {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
		}
		return
	}
	response := map[string]any{"jobs": page.Jobs}
	if page.NextCursor != "" {
		response["nextCursor"] = page.NextCursor
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleComputeJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/compute/jobs/"), "/")
	parts := strings.Split(path, "/")
	jobID := parts[0]
	if !computeJobIDPattern.MatchString(jobID) {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("invalid jobId"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	if len(parts) == 1 {
		job, found, err := s.workspaceStore.GetComputeJob(compatAgentUserID(r), jobID)
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		stdout, _, _ := s.workspaceStore.GetComputeJobLog(compatAgentUserID(r), jobID, "stdout", 262144)
		stderr, _, _ := s.workspaceStore.GetComputeJobLog(compatAgentUserID(r), jobID, "stderr", 262144)
		writeJSON(w, http.StatusOK, map[string]any{
			"jobId": job.JobID, "environment": job.Environment, "tierType": job.TierType, "provider": job.Provider,
			"frameId": job.FrameID, "projectId": job.ProjectID, "state": job.State, "startedAt": job.StartedAt,
			"intent": job.Intent, "hardwareDetails": job.HardwareDetails, "originToolUseId": job.OriginToolUseID,
			"rootFrameId": job.RootFrameID, "providerFamily": job.ProviderFamily, "providerLabel": job.ProviderLabel,
			"startedAtIso": job.StartedAtISO, "externalId": job.ExternalID, "externalUrl": job.ExternalURL,
			"supportsTail": job.SupportsTail, "endedAtIso": job.EndedAtISO,
			"harvest":   map[string]any{"stdout": map[string]any{"exists": stdout.Exists, "size": stdout.Size}, "stderr": map[string]any{"exists": stderr.Exists, "size": stderr.Size}},
			"errorKind": job.ErrorKind, "leftOnRemote": job.LeftOnRemote, "systemHint": job.SystemHint,
		})
		return
	}
	if len(parts) != 2 || parts[1] != "logs" {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("job not found"))
		return
	}
	stream := "stdout"
	if r.URL.Query().Get("stream") == "stderr" {
		stream = "stderr"
	}
	tail := 65536
	if raw := r.URL.Query().Get("tail"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("tail must be a number"))
			return
		}
		tail = value
	}
	log, found, err := s.workspaceStore.GetComputeJobLog(compatAgentUserID(r), jobID, stream, tail)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("job"))
		return
	}
	lineLimit := 4096
	if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
		lineLimit, err = strconv.Atoi(raw)
		if err != nil || lineLimit < 1 || lineLimit > 4096 {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("lines must be an integer from 1 to 4096"))
			return
		}
	}
	log = limitComputeJobLogLines(log, lineLimit)
	writeJSON(w, http.StatusOK, log)
}

func limitComputeJobLogLines(log workspace.ComputeJobLog, limit int) workspace.ComputeJobLog {
	if limit <= 0 || log.Text == "" {
		return log
	}
	remaining := limit
	end := len(log.Text)
	searchEnd := end
	if searchEnd > 0 && log.Text[searchEnd-1] == '\n' {
		searchEnd--
	}
	start := searchEnd
	for start > 0 && remaining > 0 {
		index := strings.LastIndexByte(log.Text[:start], '\n')
		if index < 0 {
			start = 0
			break
		}
		start = index
		remaining--
	}
	if remaining == 0 && start > 0 {
		start++
	}
	if start > 0 {
		log.Text = log.Text[start:end]
		log.Truncated = true
	}
	return log
}

func (s *Server) handleComputeSessionEnabled(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/compute/session/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "enabled" {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("compute session not found"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	userID := compatAgentUserID(r)
	sessionKey := strings.TrimSpace(parts[0])
	if !validComputeSessionKey(sessionKey) {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("invalid compute session key"))
		return
	}
	if !strings.HasPrefix(sessionKey, "draft-") {
		owned, err := s.workspaceStore.ComputeRootOwnedBy(userID, sessionKey)
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		if !owned {
			writeAgentCompatError(w, http.StatusNotFound, errors.New("frame"))
			return
		}
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		values, configured, err := s.workspaceStore.SessionComputeProviderSelection(userID, sessionKey)
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		if !configured {
			providers, err := s.workspaceStore.ListComputeProviders(userID)
			if err != nil {
				writeAgentCompatError(w, http.StatusInternalServerError, err)
				return
			}
			for _, provider := range providers {
				if provider.Family == "byoc" {
					settings, found, err := s.workspaceStore.GetBYOCSettings(strings.TrimPrefix(provider.Name, "byoc:"), userID)
					if err != nil {
						writeAgentCompatError(w, http.StatusInternalServerError, err)
						return
					}
					if !found || !settings.Enabled {
						continue
					}
				}
				values = append(values, provider.Name)
			}
			sort.Strings(values)
		}
		writeJSON(w, http.StatusOK, values)
		return
	}
	if len(parts) == 3 && r.Method == http.MethodPut {
		var body struct {
			Checked *bool `json:"checked"`
		}
		if err := decodeAgentCompatJSON(r, &body); err != nil || body.Checked == nil {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("body.checked must be boolean"))
			return
		}
		providerName := strings.TrimSpace(parts[2])
		if !validComputeSessionKey(providerName) {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("invalid compute provider name"))
			return
		}
		if err := s.workspaceStore.SetSessionComputeProvider(userID, sessionKey, providerName, *body.Checked); err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func validComputeSessionKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 255 && !containsControl(value)
}
