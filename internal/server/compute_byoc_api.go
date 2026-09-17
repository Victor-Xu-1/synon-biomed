package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	compute "synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"

	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
)

const byocLifecycleAuditNamespace = "compute-byoc-lifecycle-audit"

func (s *Server) handleComputeBYOC(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/compute/byoc/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] != "modal" {
		writeAgentCompatError(w, http.StatusNotFound, errors.New("BYOC provider not found"))
		return
	}
	if s.workspaceStore == nil {
		writeAgentCompatError(w, http.StatusServiceUnavailable, errors.New("workspace store is unavailable"))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.getComputeBYOC(w, r, parts[0])
		return
	}
	if len(parts) == 2 && parts[1] == "enabled" && r.Method == http.MethodPut {
		s.putComputeBYOCEnabled(w, r, parts[0])
		return
	}
	writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func (s *Server) getComputeBYOC(w http.ResponseWriter, r *http.Request, provider string) {
	userID := compatAgentUserID(r)
	settings, _, err := s.workspaceStore.GetBYOCSettings(provider, userID)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	secret, hasStored, err := s.modalCredential(userID)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	profiles, tomlMissing, credsError := modalProfiles(s.modalConfigPath, secret, hasStored)
	response := map[string]any{
		"provider": provider, "profiles": profiles, "enabled": settings.Enabled,
		"detailsMd": settings.DetailsMD, "hasStoredCredential": hasStored,
		"egressPolicy":       settings.EgressPolicy,
		"runtimeEnvironment": s.computeProviderProvisionStatus(provider),
	}
	if credsError != nil {
		response["credsError"] = credsError
	}
	if tomlMissing {
		response["tomlMissing"] = true
	}
	if settings.MaxConcurrentJobs != nil {
		response["maxConcurrentJobs"] = settings.MaxConcurrentJobs
	}
	if settings.MaxTimeoutSec != nil {
		response["maxTimeoutSec"] = settings.MaxTimeoutSec
	}
	if settings.AppName != "" {
		response["appName"] = settings.AppName
	}
	if settings.EnvironmentName != "" {
		response["environmentName"] = settings.EnvironmentName
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) putComputeBYOCEnabled(w http.ResponseWriter, r *http.Request, provider string) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil || body.Enabled == nil {
		writeAgentCompatError(w, http.StatusBadRequest, errors.New("enabled must be boolean"))
		return
	}
	userID := compatAgentUserID(r)
	leaseHolder := "byoc-toggle-" + uuid.NewString()
	_, acquired, err := s.workspaceStore.AcquireComputeReconcileLease(
		userID, "byoc:"+provider, leaseHolder, 3*time.Minute,
	)
	if err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	if !acquired {
		writeAgentCompatError(w, http.StatusConflict, errors.New("BYOC lifecycle operation is already in progress"))
		return
	}
	defer func() {
		_, _ = s.workspaceStore.ReleaseComputeReconcileLease(userID, "byoc:"+provider, leaseHolder)
	}()
	var secureRuntime kernelruntime.ProviderRuntimeSpec
	var secureRuntimeErr error
	if !*body.Enabled {
		authority, authorityErr := s.agentComputeProviderAuthority(workspace.KernelFrameAccess{UserID: userID}, provider, true)
		if authorityErr != nil {
			secureRuntimeErr = authorityErr
		} else {
			secureRuntime = authority.Definition
		}
	}
	_, err = s.workspaceStore.SetBYOCEnabled(provider, userID, *body.Enabled)
	if err != nil {
		writeAgentCompatError(w, http.StatusConflict, err)
		return
	}
	if *body.Enabled {
		s.notifyComputeProviderProvisioner(provider)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var cleanupResult compute.BYOCCleanupResult
	cleanupErr := secureRuntimeErr
	installID := secureRuntime.InstallID
	if cleanupErr == nil {
		cleanupResult, cleanupErr = s.cleanupAgentBYOCProvider(r.Context(), secureRuntime)
	}
	s.recordBYOCLifecycleAudit(provider, userID, installID, cleanupResult, cleanupErr)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBYOCProviderPatch(w http.ResponseWriter, r *http.Request, provider workspace.ComputeProvider, body map[string]json.RawMessage) {
	providerName := strings.TrimPrefix(provider.Name, "byoc:")
	userID := compatAgentUserID(r)
	for attempt := 0; attempt < 3; attempt++ {
		settings, found, err := s.workspaceStore.GetBYOCSettings(providerName, userID)
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeAgentCompatError(w, http.StatusNotFound, errors.New("BYOC provider not found"))
			return
		}
		if err := applyBYOCProviderPatch(&settings, body); err != nil {
			writeAgentCompatError(w, http.StatusBadRequest, err)
			return
		}
		if _, err := s.workspaceStore.UpdateBYOCSettings(settings.Provider, userID, settings); err == nil {
			if settings.Enabled {
				s.notifyComputeProviderProvisioner(providerName)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		} else if !errors.Is(err, workspace.ErrBYOCRevisionConflict) {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeAgentCompatError(w, http.StatusConflict, workspace.ErrBYOCRevisionConflict)
}

func applyBYOCProviderPatch(settings *workspace.BYOCSettings, body map[string]json.RawMessage) error {
	for key, raw := range body {
		switch key {
		case "detailsMd":
			if err := json.Unmarshal(raw, &settings.DetailsMD); err != nil {
				return errors.New("detailsMd must be a string")
			}
		case "maxConcurrentJobs":
			if err := decodeNullableInt(raw, &settings.MaxConcurrentJobs, 0, 64); err != nil {
				return err
			}
		case "maxTimeoutSec":
			if err := decodeNullableInt(raw, &settings.MaxTimeoutSec, 60, 259200); err != nil {
				return err
			}
		case "appName":
			if err := json.Unmarshal(raw, &settings.AppName); err != nil || compute.ValidateBYOCAppName(settings.AppName) != nil {
				return errors.New("invalid appName")
			}
		case "environmentName":
			if string(raw) == "null" {
				settings.EnvironmentName = ""
				continue
			}
			if err := json.Unmarshal(raw, &settings.EnvironmentName); err != nil || compute.ValidateModalEnvironment(settings.EnvironmentName) != nil {
				return errors.New("invalid environmentName")
			}
		case "egressPolicy":
			if string(raw) == "null" {
				settings.EgressPolicy = nil
				continue
			}
			var policy map[string]any
			if err := json.Unmarshal(raw, &policy); err != nil {
				return errors.New("invalid egressPolicy")
			}
			normalized, err := compute.NormalizeBYOCEgressPolicy(policy)
			if err != nil {
				return err
			}
			settings.EgressPolicy = normalized
		default:
			return errors.New("unsupported BYOC provider field")
		}
	}
	return nil
}

func (s *Server) modalCredential(userID string) (secretstore.Secret, bool, error) {
	if s.secretStore == nil {
		return secretstore.Secret{}, false, nil
	}
	secrets, err := s.secretStore.ListForUser(userID)
	if err != nil {
		return secretstore.Secret{}, false, err
	}
	for _, secret := range secrets {
		if strings.EqualFold(secret.Provider, "modal") &&
			strings.TrimSpace(secret.Credentials["token_id"]) != "" &&
			strings.TrimSpace(secret.Credentials["token_secret"]) != "" {
			return secret, true, nil
		}
	}
	return secretstore.Secret{}, false, nil
}

func modalProfiles(path string, secret secretstore.Secret, hasStored bool) ([]map[string]any, bool, any) {
	profiles := []map[string]any{}
	configProfiles, tomlMissing, err := compute.ReadModalConfigProfiles(path)
	if err == nil {
		for _, profile := range configProfiles {
			item := map[string]any{
				"name": profile.Name, "active": profile.Active,
				"tokenIdMasked": profile.TokenIDMasked,
			}
			if profile.Workspace != "" {
				item["workspace"] = profile.Workspace
			}
			profiles = append(profiles, item)
		}
		return profiles, false, nil
	}
	if tomlMissing && hasStored {
		profiles = append(profiles, map[string]any{
			"name": "Synon Biomed (stored)", "active": true,
			"tokenIdMasked": compute.MaskModalToken(secret.Credentials["token_id"]),
		})
		return profiles, true, nil
	}
	if tomlMissing {
		return profiles, true, "~/.modal.toml not found \u2014 run `modal token new` to create one."
	}
	return profiles, false, err.Error()
}

func (s *Server) recordBYOCLifecycleAudit(provider, userID, installID string, result compute.BYOCCleanupResult, runErr error) {
	if s.runtimeStore == nil {
		return
	}
	status := "completed"
	if runErr != nil || len(result.Failed) > 0 {
		status = "incomplete"
	} else if result.Skipped {
		status = "skipped"
	}
	record := map[string]any{
		"provider": provider, "userId": userID, "installId": installID,
		"enabled": false, "status": status, "owned": result.Owned,
		"terminated": append([]string(nil), result.Terminated...),
		"failed":     append([]string(nil), result.Failed...),
		"skipped":    result.Skipped, "reason": result.Reason,
		"recordedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if runErr != nil {
		message := runErr.Error()
		if len(message) > 512 {
			message = message[:512]
		}
		record["error"] = message
	}
	_, _ = s.runtimeStore.Set(byocLifecycleAuditNamespace, uuid.NewString(), record)
}

const byocInstallIDSetting = "compute.byoc.installId"

func (s *Server) ensureBYOCInstallID() (string, error) {
	if s.settingsStore == nil {
		return "", errors.New("settings store is unavailable")
	}
	setting, err := s.settingsStore.Update(byocInstallIDSetting, func(current any, found bool) (any, error) {
		if found {
			if value, ok := current.(string); ok && strings.TrimSpace(value) != "" {
				return value, nil
			}
		}
		return uuid.NewString(), nil
	})
	if err != nil {
		return "", err
	}
	value, ok := setting.Value.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", errors.New("BYOC install id is invalid")
	}
	return value, nil
}
