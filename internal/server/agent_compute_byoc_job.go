package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	byocHarvestMargin              = 10 * time.Minute
	byocTerminationGrace           = 60 * time.Second
	byocDefaultJobTimeout          = 30 * time.Minute
	byocDefaultContainer           = 12 * time.Hour
	byocMaximumContainer           = 85500 * time.Second
	byocMaximumInputBytes          = int64(10 << 30)
	byocMaximumInputFiles          = 256
	byocMaximumJobEnvBytes         = 64 << 10
	computeProviderHandleNamespace = "compute-provider-handles"
)

var byocJobEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var byocSubmissionIDPattern = regexp.MustCompile(`^submission-[0-9a-f]{24}$`)
var artifactComputeInputPattern = regexp.MustCompile(`^\{\{artifact:([^{}]+)\}\}$`)

type byocModalJobSpec struct {
	Image       string
	Environment string
	GPU         string
	CPU         int
	Memory      int
	Volumes     map[string]string
	Timeout     time.Duration
	Egress      []string
}

type byocInputArchive struct {
	SHA256 string
	Bytes  int64
}

func (s *Server) submitAgentBYOCJob(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	call agentruntime.ToolCall,
	input map[string]any,
	workspaceDir string,
) (any, error) {
	providerID := publicComputeProviderName(stringValue(input["provider"]))
	if providerID != "modal" {
		return nil, errors.New("unsupported BYOC compute provider")
	}
	authority, err := s.agentComputeProviderAuthority(access, providerID, true)
	if err != nil {
		return nil, err
	}
	if s.providerOperationRunner == nil {
		return nil, errors.New("BYOC provider operation runtime is unavailable")
	}
	settings, found, err := s.workspaceStore.GetBYOCSettings(providerID, access.UserID)
	if err != nil || !found || !settings.Enabled {
		return nil, errors.New("BYOC compute provider is not enabled")
	}
	canonicalProviderParams, err := canonicalModalProviderParams(mapValue(input["provider_params"]))
	if err != nil {
		return nil, err
	}
	input = copyMapAny(input)
	input["provider"] = providerID
	input["provider_params"] = canonicalProviderParams
	spec, err := s.normalizeModalJobSpec(input, settings)
	if err != nil {
		return nil, err
	}
	if err := validateAgentBYOCOutputs(anySliceValue(input["outputs"])); err != nil {
		return nil, err
	}
	if err := s.enforceAgentComputeCapacity(access, authority.Provider); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(access.Frame.RootFrameID + "\x00" + call.ID))
	jobID := "job-" + hex.EncodeToString(digest[:12])
	if existing, existingFound, getErr := s.workspaceStore.GetComputeJob(access.UserID, jobID); getErr == nil && existingFound {
		if existing.State == workspace.ComputeJobPending || existing.State == workspace.ComputeJobStaging {
			s.notifyComputeProviderJobSupervisor()
		}
		return kernelComputeJobProjection(existing), nil
	}
	handleID := strings.TrimSpace(stringValue(input["handle_id"]))
	handleSandbox := ""
	handleTierApproved := false
	handleClaimed := false
	if handleID != "" {
		handleSandbox, handleTierApproved, err = s.claimAgentComputeHandle(
			access, handleID, providerID, mapValue(input["provider_params"]), jobID,
		)
		if err != nil {
			return nil, err
		}
		handleClaimed = true
		defer func() {
			if handleClaimed {
				s.releaseAgentComputeHandle(handleID, jobID, false)
			}
		}()
	}
	if !handleTierApproved {
		if err := s.requireKernelCapabilityInstallApproval(
			ctx, access, "byoc-job:"+jobID, "host.compute.submit_job", "remote_compute", input,
			map[string]any{
				"title":       "Run remote compute job",
				"description": fmt.Sprintf("%s on %s for up to %s", strings.TrimSpace(stringValue(input["intent"])), providerID, spec.Timeout),
			},
		); err != nil {
			return nil, err
		}
		if handleID != "" {
			s.markAgentComputeHandleTierApproved(handleID, jobID)
		}
	}
	jobTimeout := time.Duration(numberValue(input["timeout_seconds"])) * time.Second
	if jobTimeout <= 0 {
		jobTimeout = byocDefaultJobTimeout
	}
	if jobTimeout > spec.Timeout {
		jobTimeout = spec.Timeout
	}
	providerSpec := agentBYOCProviderSandboxSpec(spec)
	providerSpecSHA256, err := agentBYOCProviderSpecSHA256(providerSpec)
	if err != nil {
		return nil, err
	}
	providerConfigSHA256 := strings.TrimSpace(authority.Definition.ExtraEnvironment["SYNON_PROVIDER_BOUND_CONFIG_HASH"])
	if providerConfigSHA256 == "" {
		return nil, errors.New("BYOC provider configuration authority is unavailable")
	}
	durableStage, archive, err := s.stageAgentBYOCJob(jobID, workspaceDir, input, access)
	if err != nil {
		return nil, err
	}
	preserveStage := false
	defer func() {
		if !preserveStage {
			_ = os.RemoveAll(durableStage)
		}
	}()
	frameID, rootID, originID := access.Frame.ID, access.Frame.RootFrameID, call.ID
	submissionDigest := sha256.Sum256([]byte("synon-byoc-submission\x00" + jobID))
	submissionID := "submission-" + hex.EncodeToString(submissionDigest[:12])
	deadline := time.Now().UTC().Add(spec.Timeout + byocHarvestMargin)
	hardware := map[string]any{
		"provider_params": mapValue(input["provider_params"]), "outputs": anySliceValue(input["outputs"]),
		"workspace_dir": workspaceDir,
		"install_id":    authority.Definition.InstallID, "submission_id": submissionID,
		"sandbox_deadline_epoch": deadline.Unix(), "job_timeout_seconds": int(jobTimeout / time.Second),
		"harvest_margin_seconds": int(byocHarvestMargin / time.Second), "termination_grace_seconds": int(byocTerminationGrace / time.Second),
		"handle_id":    handleID,
		"sandbox_hint": handleSandbox,
		"staging_dir":  durableStage, "archive_sha256": archive.SHA256, "archive_bytes": archive.Bytes,
		"provider_spec": providerSpec, "provider_spec_sha256": providerSpecSHA256,
		"provider_config_sha256": providerConfigSHA256,
	}
	s.computeSubmitMu.Lock()
	if capacityErr := s.enforceAgentComputeCapacity(access, authority.Provider); capacityErr != nil {
		s.computeSubmitMu.Unlock()
		return nil, capacityErr
	}
	_, err = s.workspaceStore.CreateComputeJob(access.UserID, workspace.ComputeJob{
		JobID: jobID, ProjectID: access.Frame.ProjectID, Provider: authority.Provider.Name,
		Environment: firstNonEmpty(spec.Environment, "remote"), TierType: "remote",
		FrameID: &frameID, RootFrameID: &rootID, OriginToolUseID: &originID,
		Intent: strings.TrimSpace(stringValue(input["intent"])), HardwareDetails: hardware,
		ProviderFamily: "byoc", ProviderLabel: providerID, SupportsTail: true,
	})
	s.computeSubmitMu.Unlock()
	if err != nil {
		return nil, err
	}
	preserveStage = true
	sandboxID := handleSandbox
	if sandboxID == "" {
		createResult, createErr := s.createAgentBYOCSandbox(ctx, authority.Definition, jobID, submissionID, providerSpec)
		if createErr != nil {
			if _, transitionErr := s.workspaceStore.TransitionComputeJob(access.UserID, jobID, workspace.ComputeJobFailed, providerOperationFailureKind(createErr), time.Now().UTC()); transitionErr == nil {
				preserveStage = false
			}
			return nil, createErr
		}
		sandboxID = strings.TrimSpace(stringValue(createResult["sandbox_id"]))
		if handleID != "" && sandboxID != "" {
			s.bindAgentComputeHandleSandbox(handleID, jobID, sandboxID)
		}
	}
	if sandboxID == "" {
		if _, transitionErr := s.workspaceStore.TransitionComputeJob(access.UserID, jobID, workspace.ComputeJobFailed, "invalid_provider_response", time.Now().UTC()); transitionErr == nil {
			preserveStage = false
		}
		return nil, errors.New("BYOC provider returned no sandbox id")
	}
	if _, err := s.workspaceStore.BindComputeJobExternalForStaging(access.UserID, jobID, sandboxID, ""); err != nil {
		_ = s.terminateAgentBYOCSandbox(context.Background(), authority.Definition, sandboxID)
		return nil, err
	}
	request := agentBYOCSubmissionRequest(authority.Definition.InstallID, submissionID, sandboxID, archive, jobTimeout, deadline, time.Now())
	_, err = s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
		Runtime: authority.Definition, Operation: "submit", Request: request,
		Prepare: func(stage string) error {
			return copyAgentBYOCStagedArchive(durableStage, stage, archive)
		},
	})
	if err != nil {
		_ = s.terminateAgentBYOCSandbox(context.Background(), authority.Definition, sandboxID)
		if _, transitionErr := s.workspaceStore.TransitionComputeJob(access.UserID, jobID, workspace.ComputeJobFailed, providerOperationFailureKind(err), time.Now().UTC()); transitionErr == nil {
			preserveStage = false
		}
		return nil, err
	}
	if _, err := s.workspaceStore.TransitionComputeJob(access.UserID, jobID, workspace.ComputeJobRunning, "", time.Now().UTC()); err != nil {
		return nil, err
	}
	preserveStage = false
	if handleID != "" {
		handleClaimed = false
	}
	_ = s.workspaceStore.AppendComputeJobLog(access.UserID, jobID, "combined", fmt.Sprintf("submitted %d bytes sha256=%s\n", archive.Bytes, archive.SHA256))
	s.notifyComputeProviderJobSupervisor()
	return map[string]any{"job_id": jobID, "provider": providerID, "status": "running", "sandbox_id": sandboxID}, nil
}

func (s *Server) notifyComputeProviderJobSupervisor() {
	if s == nil || s.computeProviderJobWake == nil {
		return
	}
	select {
	case s.computeProviderJobWake <- struct{}{}:
	default:
	}
}

func (s *Server) createAgentComputeHandle(access workspace.KernelFrameAccess, callID, provider string, providerParams map[string]any) (string, error) {
	if s == nil || s.runtimeStore == nil {
		return "", errors.New("compute handle store is unavailable")
	}
	raw, _ := json.Marshal(providerParams)
	digest := sha256.Sum256([]byte("synon-compute-handle-v1\x00" + access.UserID + "\x00" + access.Frame.RootFrameID + "\x00" + callID + "\x00" + provider + "\x00" + string(raw)))
	handleID := "compute-handle-" + hex.EncodeToString(digest[:12])
	paramsDigest := fmt.Sprintf("%x", sha256.Sum256(raw))
	value := map[string]any{
		"handle_id": handleID, "owner_user_id": access.UserID, "project_id": access.Frame.ProjectID,
		"root_frame_id": access.Frame.RootFrameID, "provider": provider,
		"provider_params": copyMapAny(providerParams), "provider_params_sha256": paramsDigest,
		"state": "active", "tier_approved": false, "created_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if existing, found, err := s.runtimeStore.Get(computeProviderHandleNamespace, handleID); err != nil {
		return "", err
	} else if found {
		stored := mapValue(existing.Value)
		if stringValue(stored["owner_user_id"]) != access.UserID || stringValue(stored["root_frame_id"]) != access.Frame.RootFrameID ||
			stringValue(stored["provider_params_sha256"]) != paramsDigest {
			return "", errors.New("compute handle identity conflicts with existing state")
		}
		return handleID, nil
	}
	_, err := s.runtimeStore.Set(computeProviderHandleNamespace, handleID, value)
	return handleID, err
}

func (s *Server) claimAgentComputeHandle(access workspace.KernelFrameAccess, handleID, provider string, providerParams map[string]any, jobID string) (string, bool, error) {
	if s == nil || s.runtimeStore == nil {
		return "", false, errors.New("compute handle store is unavailable")
	}
	s.computeProviderHandleMu.Lock()
	defer s.computeProviderHandleMu.Unlock()
	entry, found, err := s.runtimeStore.Get(computeProviderHandleNamespace, handleID)
	if err != nil || !found {
		return "", false, errors.New("compute handle is unavailable")
	}
	value := copyMapAny(mapValue(entry.Value))
	raw, _ := json.Marshal(providerParams)
	paramsDigest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if stringValue(value["owner_user_id"]) != access.UserID || stringValue(value["root_frame_id"]) != access.Frame.RootFrameID ||
		stringValue(value["provider"]) != provider || stringValue(value["state"]) != "active" ||
		stringValue(value["provider_params_sha256"]) != paramsDigest {
		return "", false, errors.New("compute handle authority changed")
	}
	if busy := strings.TrimSpace(stringValue(value["busy_job_id"])); busy != "" && busy != jobID {
		return "", false, errors.New("compute handle already has an active job")
	}
	value["busy_job_id"] = jobID
	value["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.runtimeStore.Set(computeProviderHandleNamespace, handleID, value); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(stringValue(value["sandbox_id"])), boolValue(value["tier_approved"], false), nil
}

func (s *Server) markAgentComputeHandleTierApproved(handleID, jobID string) {
	s.computeProviderHandleMu.Lock()
	defer s.computeProviderHandleMu.Unlock()
	entry, found, err := s.runtimeStore.Get(computeProviderHandleNamespace, handleID)
	if err != nil || !found {
		return
	}
	value := copyMapAny(mapValue(entry.Value))
	if stringValue(value["busy_job_id"]) != jobID {
		return
	}
	value["tier_approved"] = true
	_, _ = s.runtimeStore.Set(computeProviderHandleNamespace, handleID, value)
}

func (s *Server) bindAgentComputeHandleSandbox(handleID, jobID, sandboxID string) {
	s.computeProviderHandleMu.Lock()
	defer s.computeProviderHandleMu.Unlock()
	entry, found, err := s.runtimeStore.Get(computeProviderHandleNamespace, handleID)
	if err != nil || !found {
		return
	}
	value := copyMapAny(mapValue(entry.Value))
	if stringValue(value["busy_job_id"]) != jobID {
		return
	}
	value["sandbox_id"] = sandboxID
	_, _ = s.runtimeStore.Set(computeProviderHandleNamespace, handleID, value)
}

func (s *Server) releaseAgentComputeHandle(handleID, jobID string, keepSandbox bool) {
	if s == nil || s.runtimeStore == nil || handleID == "" {
		return
	}
	s.computeProviderHandleMu.Lock()
	defer s.computeProviderHandleMu.Unlock()
	entry, found, err := s.runtimeStore.Get(computeProviderHandleNamespace, handleID)
	if err != nil || !found {
		return
	}
	value := copyMapAny(mapValue(entry.Value))
	if stringValue(value["busy_job_id"]) == jobID {
		value["busy_job_id"] = ""
	}
	if !keepSandbox {
		value["sandbox_id"] = ""
	}
	value["updated_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = s.runtimeStore.Set(computeProviderHandleNamespace, handleID, value)
}

func (s *Server) closeAgentComputeHandle(ctx context.Context, access workspace.KernelFrameAccess, provider, handleID string) (map[string]any, error) {
	if s == nil || s.runtimeStore == nil {
		return nil, errors.New("compute handle store is unavailable")
	}
	s.computeProviderHandleMu.Lock()
	entry, found, err := s.runtimeStore.Get(computeProviderHandleNamespace, handleID)
	if err != nil || !found {
		s.computeProviderHandleMu.Unlock()
		return map[string]any{"closed": true, "handle_id": handleID, "idempotent": true}, nil
	}
	value := copyMapAny(mapValue(entry.Value))
	if stringValue(value["owner_user_id"]) != access.UserID || stringValue(value["root_frame_id"]) != access.Frame.RootFrameID || stringValue(value["provider"]) != provider {
		s.computeProviderHandleMu.Unlock()
		return nil, errors.New("compute handle authority changed")
	}
	if strings.TrimSpace(stringValue(value["busy_job_id"])) != "" {
		s.computeProviderHandleMu.Unlock()
		return nil, errors.New("compute handle has an active job; cancel or wait before closing")
	}
	sandboxID := strings.TrimSpace(stringValue(value["sandbox_id"]))
	value["state"] = "closing"
	_, err = s.runtimeStore.Set(computeProviderHandleNamespace, handleID, value)
	s.computeProviderHandleMu.Unlock()
	if err != nil {
		return nil, err
	}
	if sandboxID != "" {
		authority, err := s.agentComputeProviderAuthority(access, strings.TrimPrefix(provider, "byoc:"), true)
		if err != nil {
			return nil, err
		}
		if err := s.terminateAgentBYOCSandbox(ctx, authority.Definition, sandboxID); err != nil {
			return nil, err
		}
	}
	_, _ = s.runtimeStore.Delete(computeProviderHandleNamespace, handleID)
	return map[string]any{"closed": true, "handle_id": handleID, "sandbox_id": sandboxID}, nil
}

func (s *Server) normalizeModalJobSpec(input map[string]any, settings workspace.BYOCSettings) (byocModalJobSpec, error) {
	providerParams := mapValue(input["provider_params"])
	modal, err := canonicalModalProviderParams(providerParams)
	if err != nil {
		return byocModalJobSpec{}, err
	}
	if len(modal) == 0 {
		return byocModalJobSpec{}, errors.New("Modal submit_job requires provider_params")
	}
	allowed := map[string]bool{"image": true, "env": true, "gpu": true, "cpu": true, "memory": true, "volumes": true, "timeout": true}
	for key := range modal {
		if !allowed[key] {
			return byocModalJobSpec{}, fmt.Errorf("provider_params field %q is not allowed for Modal", key)
		}
	}
	spec := byocModalJobSpec{
		Image: strings.TrimSpace(stringValue(modal["image"])), Environment: strings.TrimSpace(stringValue(modal["env"])),
		GPU: strings.TrimSpace(stringValue(modal["gpu"])), CPU: int(numberValue(modal["cpu"])),
		Memory: int(numberValue(modal["memory"])), Volumes: map[string]string{}, Timeout: byocDefaultContainer,
	}
	if spec.Image == "" || len(spec.Image) > 512 || strings.ContainsAny(spec.Image, "\x00\r\n") {
		return byocModalJobSpec{}, errors.New("provider_params.image is required and must be bounded")
	}
	if spec.Environment != "" && !kernelruntime.ValidEnvironmentName(spec.Environment) {
		return byocModalJobSpec{}, errors.New("provider_params.env is invalid")
	}
	if len(spec.GPU) > 64 || spec.CPU < 0 || spec.CPU > 1024 || spec.Memory < 0 || spec.Memory > 1<<30 {
		return byocModalJobSpec{}, errors.New("provider_params resource request is invalid")
	}
	if timeout := time.Duration(numberValue(modal["timeout"])) * time.Second; timeout > 0 {
		spec.Timeout = timeout
	} else if settings.MaxTimeoutSec != nil && *settings.MaxTimeoutSec > 0 {
		spec.Timeout = time.Duration(*settings.MaxTimeoutSec) * time.Second
	}
	if spec.Timeout < time.Minute || spec.Timeout > byocMaximumContainer {
		return byocModalJobSpec{}, errors.New("provider_params.timeout must be 60-85500 seconds")
	}
	for mount, rawName := range mapValue(modal["volumes"]) {
		name := strings.TrimSpace(stringValue(rawName))
		clean := filepath.Clean(mount)
		if !filepath.IsAbs(clean) || clean == "/" || clean == "/work" || strings.HasPrefix(clean, "/work/") || name == "" || len(name) > 128 {
			return byocModalJobSpec{}, errors.New("provider_params.volumes contains an invalid mount")
		}
		spec.Volumes[clean] = name
	}
	mode := strings.TrimSpace(stringValue(settings.EgressPolicy["mode"]))
	switch mode {
	case "blocked":
		spec.Egress = []string{}
	case "allowlist":
		domains := stringArrayValue(settings.EgressPolicy["additional"])
		if boolValue(settings.EgressPolicy["mirror"], false) {
			domains = append(domains, s.configAllowedDomains...)
		}
		sort.Strings(domains)
		spec.Egress = uniqueSortedBYOCDomains(domains)
	case "", "unrestricted":
		spec.Egress = nil
	default:
		return byocModalJobSpec{}, errors.New("BYOC egress policy is invalid")
	}
	return spec, nil
}

func agentBYOCProviderSandboxSpec(spec byocModalJobSpec) map[string]any {
	providerSpec := map[string]any{
		"image": spec.Image, "timeout": int(spec.Timeout / time.Second),
		"harvest_margin_s": int(byocHarvestMargin / time.Second),
	}
	if spec.GPU != "" {
		providerSpec["gpu"] = spec.GPU
	}
	if spec.CPU > 0 {
		providerSpec["cpu"] = spec.CPU
	}
	if spec.Memory > 0 {
		providerSpec["memory"] = spec.Memory
	}
	if len(spec.Volumes) > 0 {
		providerSpec["volumes"] = spec.Volumes
	}
	if spec.Egress != nil {
		providerSpec["egress_allowlist"] = spec.Egress
	}
	return providerSpec
}

func validateAgentBYOCOutputs(outputs []any) error {
	if len(outputs) > 256 {
		return errors.New("BYOC outputs exceed 256 entries")
	}
	for _, raw := range outputs {
		pattern := strings.TrimSpace(stringValue(raw))
		visibility := "featured"
		if item, ok := raw.(map[string]any); ok {
			if len(item) == 0 || len(item) > 2 {
				return errors.New("BYOC output objects accept only glob and visibility")
			}
			for key := range item {
				if key != "glob" && key != "visibility" {
					return fmt.Errorf("BYOC output field %q is not allowed", key)
				}
			}
			pattern = strings.TrimSpace(stringValue(item["glob"]))
			visibility = strings.TrimSpace(firstNonEmpty(stringValue(item["visibility"]), "featured"))
		}
		if pattern == "" || len(pattern) > 4096 || strings.ContainsAny(pattern, "\x00\r\n") || filepath.IsAbs(pattern) || pattern == ".." || strings.HasPrefix(pattern, "../") {
			return errors.New("BYOC output glob is invalid")
		}
		if visibility != "featured" && visibility != "hidden" {
			return errors.New("BYOC output visibility must be featured or hidden")
		}
	}
	return nil
}

func agentBYOCProviderSpecSHA256(spec map[string]any) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return "", errors.New("BYOC provider specification exceeds the bounded contract")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (s *Server) createAgentBYOCSandbox(
	ctx context.Context,
	runtimeSpec kernelruntime.ProviderRuntimeSpec,
	jobID string,
	submissionID string,
	providerSpec map[string]any,
) (map[string]any, error) {
	return s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
		Runtime: runtimeSpec, Operation: "create",
		Request: map[string]any{
			"spec": providerSpec, "install_id": runtimeSpec.InstallID,
			"tags": map[string]string{
				"synonbiomed-job": jobID, "synonbiomed-submission": submissionID,
			},
		},
	})
}

func agentBYOCSubmissionRequest(
	installID string,
	submissionID string,
	sandboxID string,
	archive byocInputArchive,
	jobTimeout time.Duration,
	deadline time.Time,
	now time.Time,
) map[string]any {
	remaining := int(deadline.Sub(now) / time.Second)
	if remaining < 0 {
		remaining = 0
	}
	return map[string]any{
		"sandbox_id": sandboxID, "install_id": installID,
		"submission_id": submissionID, "timeout": int(jobTimeout / time.Second),
		"sandbox_deadline_epoch": deadline.Unix(), "sandbox_remaining_s": remaining,
		"harvest_margin_s": int(byocHarvestMargin / time.Second), "term_grace_s": int(byocTerminationGrace / time.Second),
		"archive_sha256": archive.SHA256,
	}
}

func uniqueSortedBYOCDomains(values []string) []string {
	result := make([]string, 0, len(values))
	previous := ""
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == previous {
			continue
		}
		result = append(result, value)
		previous = value
	}
	return result
}

func (s *Server) writeAgentBYOCInputArchive(stage, workspaceRoot string, input map[string]any, accesses ...workspace.KernelFrameAccess) (byocInputArchive, error) {
	workspaceRoot, err := canonicalHostDirectory(workspaceRoot)
	if err != nil {
		return byocInputArchive{}, errors.New("BYOC task workspace is unavailable")
	}
	wrapper, err := os.ReadFile(filepath.Join(s.runtimeAssetsDir, "compute", "wrapper.sh.tmpl"))
	if err != nil {
		return byocInputArchive{}, errors.New("BYOC wrapper asset is unavailable")
	}
	runTemplate, err := os.ReadFile(filepath.Join(s.runtimeAssetsDir, "compute", "run.sh.tmpl"))
	if err != nil || bytes.Count(runTemplate, []byte("{{COMMAND}}")) != 1 {
		return byocInputArchive{}, errors.New("BYOC command template is unavailable")
	}
	command := strings.TrimSpace(stringValue(input["command"]))
	if command == "" || len(command) > 262144 || strings.ContainsRune(command, '\x00') {
		return byocInputArchive{}, errors.New("BYOC command is invalid")
	}
	runScript := bytes.Replace(runTemplate, []byte("{{COMMAND}}"), []byte(command), 1)
	jobEnv, err := encodeAgentBYOCJobEnvironment(mapValue(input["env"]))
	if err != nil {
		return byocInputArchive{}, err
	}
	archivePath := filepath.Join(stage, "in.tar.gz")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return byocInputArchive{}, errors.New("BYOC input archive could not be created")
	}
	hasher := sha256.New()
	counted := &countingWriter{writer: io.MultiWriter(archive, hasher)}
	gzipWriter := gzip.NewWriter(counted)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	tarWriter := tar.NewWriter(gzipWriter)
	closeArchive := func() error {
		return errors.Join(tarWriter.Close(), gzipWriter.Close(), archive.Close())
	}
	for _, file := range []struct {
		name string
		mode int64
		body []byte
	}{{"_operon_wrapper.sh", 0o755, wrapper}, {"run.sh", 0o755, runScript}, {".job_env", 0o600, jobEnv}} {
		if err := writeBYOCTarBytes(tarWriter, file.name, file.mode, file.body); err != nil {
			_ = closeArchive()
			return byocInputArchive{}, err
		}
	}
	reserved := map[string]bool{"_operon_wrapper.sh": true, "run.sh": true, ".job_env": true}
	totalInput := int64(0)
	inputs := anySliceValue(input["inputs"])
	if len(inputs) > byocMaximumInputFiles {
		_ = closeArchive()
		return byocInputArchive{}, errors.New("BYOC inputs exceed 256 files")
	}
	for _, raw := range inputs {
		item := mapValue(raw)
		source := strings.TrimSpace(stringValue(item["src"]))
		if text, ok := raw.(string); ok {
			source = strings.TrimSpace(text)
		}
		explicitDestination := strings.TrimSpace(firstNonEmpty(stringValue(item["dst"]), stringValue(item["dst_filename"])))
		destination := strings.TrimSpace(firstNonEmpty(explicitDestination, filepath.Base(source)))
		var sourceReader io.ReadCloser
		var sourceSize int64
		artifactID := ""
		artifactURI := false
		if match := artifactComputeInputPattern.FindStringSubmatch(source); len(match) == 2 {
			artifactID = strings.TrimSpace(match[1])
		} else if strings.HasPrefix(source, "artifact://") {
			artifactID = strings.TrimSpace(strings.TrimPrefix(source, "artifact://"))
			artifactURI = true
		}
		if artifactID != "" {
			if len(accesses) != 1 || s.workspaceStore == nil {
				_ = closeArchive()
				return byocInputArchive{}, errors.New("artifact compute input has no task authority")
			}
			artifact, version, reader, found, openErr := s.workspaceStore.OpenArtifactVersionContent(artifactID)
			if !found && openErr == nil && artifactURI {
				artifact, version, reader, found, openErr = s.workspaceStore.OpenCurrentArtifactContent(artifactID)
			}
			if openErr != nil || !found || artifact.ProjectID != accesses[0].Frame.ProjectID {
				if reader != nil {
					_ = reader.Close()
				}
				_ = closeArchive()
				return byocInputArchive{}, errors.New("artifact compute input is unavailable in this task")
			}
			sourceReader, sourceSize = reader, version.SizeBytes
			if artifactURI && explicitDestination == "" {
				_ = reader.Close()
				_ = closeArchive()
				return byocInputArchive{}, errors.New("artifact:// compute inputs require an explicit dst")
			}
			if explicitDestination == "" {
				destination = artifact.Name
			}
		}
		destination = filepath.ToSlash(filepath.Clean(filepath.FromSlash(destination)))
		if source == "" || destination == "." || destination == ".." || strings.HasPrefix(destination, "../") || filepath.IsAbs(destination) || reserved[destination] {
			if sourceReader != nil {
				_ = sourceReader.Close()
			}
			_ = closeArchive()
			return byocInputArchive{}, errors.New("BYOC input source or destination is invalid")
		}
		reserved[destination] = true
		if sourceReader == nil {
			sourcePath, err := canonicalWorkspaceInputFile(workspaceRoot, source)
			if err != nil {
				_ = closeArchive()
				return byocInputArchive{}, err
			}
			info, err := os.Stat(sourcePath)
			if err != nil || !info.Mode().IsRegular() {
				_ = closeArchive()
				return byocInputArchive{}, errors.New("BYOC input file is unavailable")
			}
			file, err := os.Open(sourcePath)
			if err != nil {
				_ = closeArchive()
				return byocInputArchive{}, err
			}
			sourceReader, sourceSize = file, info.Size()
		}
		if sourceSize < 0 || totalInput+sourceSize > byocMaximumInputBytes {
			_ = sourceReader.Close()
			_ = closeArchive()
			return byocInputArchive{}, errors.New("BYOC input file is unavailable or exceeds the transfer cap")
		}
		header := &tar.Header{Name: destination, Mode: 0o644, Size: sourceSize, ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = sourceReader.Close()
			_ = closeArchive()
			return byocInputArchive{}, err
		}
		written, copyErr := io.CopyN(tarWriter, sourceReader, sourceSize)
		closeErr := sourceReader.Close()
		if copyErr != nil || closeErr != nil || written != sourceSize {
			_ = closeArchive()
			return byocInputArchive{}, errors.New("BYOC input changed while staging")
		}
		totalInput += sourceSize
	}
	if err := closeArchive(); err != nil {
		return byocInputArchive{}, err
	}
	return byocInputArchive{SHA256: hex.EncodeToString(hasher.Sum(nil)), Bytes: counted.count}, nil
}

func (s *Server) stageAgentBYOCJob(jobID, workspaceDir string, input map[string]any, accesses ...workspace.KernelFrameAccess) (string, byocInputArchive, error) {
	if s == nil || !computeJobIDPattern.MatchString(jobID) || strings.TrimSpace(s.fileRoot) == "" {
		return "", byocInputArchive{}, errors.New("BYOC durable staging authority is unavailable")
	}
	root := filepath.Join(s.fileRoot, "provider-jobs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", byocInputArchive{}, err
	}
	target := filepath.Join(root, jobID)
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		archive, validateErr := inspectAgentBYOCStagedArchive(filepath.Join(target, "in.tar.gz"))
		return target, archive, validateErr
	}
	temporary, err := os.MkdirTemp(root, "."+jobID+"-")
	if err != nil {
		return "", byocInputArchive{}, err
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return "", byocInputArchive{}, err
	}
	archive, err := s.writeAgentBYOCInputArchive(temporary, workspaceDir, input, accesses...)
	if err != nil {
		_ = os.RemoveAll(temporary)
		return "", byocInputArchive{}, err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.RemoveAll(temporary)
		return "", byocInputArchive{}, err
	}
	return target, archive, nil
}

func inspectAgentBYOCStagedArchive(path string) (byocInputArchive, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > byocMaximumInputBytes {
		return byocInputArchive{}, errors.New("BYOC durable input archive is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return byocInputArchive{}, err
	}
	defer file.Close()
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, byocMaximumInputBytes+1))
	if err != nil || written != info.Size() {
		return byocInputArchive{}, errors.New("BYOC durable input archive could not be verified")
	}
	return byocInputArchive{SHA256: hex.EncodeToString(digest.Sum(nil)), Bytes: written}, nil
}

func copyAgentBYOCStagedArchive(sourceDir, targetDir string, expected byocInputArchive) error {
	observed, err := inspectAgentBYOCStagedArchive(filepath.Join(sourceDir, "in.tar.gz"))
	if err != nil || observed != expected {
		return errors.New("BYOC durable input archive changed before submission")
	}
	source, err := os.Open(filepath.Join(sourceDir, "in.tar.gz"))
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(filepath.Join(targetDir, "in.tar.gz"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.CopyN(target, source, expected.Bytes)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil || written != expected.Bytes {
		return errors.New("BYOC durable input archive copy failed")
	}
	return nil
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(value []byte) (int, error) {
	written, err := w.writer.Write(value)
	w.count += int64(written)
	return written, err
}

func writeBYOCTarBytes(writer *tar.Writer, name string, mode int64, body []byte) error {
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := writer.Write(body)
	return err
}

func encodeAgentBYOCJobEnvironment(values map[string]any) ([]byte, error) {
	if len(values) > 64 {
		return nil, errors.New("BYOC job environment exceeds 64 variables")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		value, ok := values[key].(string)
		upper := strings.ToUpper(key)
		if !ok || !byocJobEnvironmentName.MatchString(key) || len(value) > 16384 ||
			strings.HasPrefix(upper, "OPERON_") || strings.HasPrefix(upper, "SYNON_") ||
			strings.HasSuffix(upper, "_PROXY") || strings.ContainsAny(value, "\x00") {
			return nil, fmt.Errorf("BYOC job environment variable %q is not allowed", key)
		}
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(shellSingleQuote(value))
		builder.WriteByte('\n')
		if builder.Len() > byocMaximumJobEnvBytes {
			return nil, errors.New("BYOC job environment exceeds 64 KiB")
		}
	}
	return []byte(builder.String()), nil
}

func canonicalWorkspaceInputFile(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || strings.ContainsAny(relative, "\x00\r\n") {
		return "", errors.New("BYOC input paths must be workspace-relative")
	}
	candidate := filepath.Join(root, filepath.Clean(filepath.FromSlash(relative)))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || !hostPathWithin(root, resolved) {
		return "", errors.New("BYOC input path escapes the task workspace")
	}
	return resolved, nil
}

func providerOperationFailureKind(err error) string {
	var operationErr *kernelruntime.ProviderOperationError
	if errors.As(err, &operationErr) && operationErr.Kind != "" {
		return operationErr.Kind
	}
	return "provider_error"
}

func (s *Server) terminateAgentBYOCSandbox(ctx context.Context, runtimeSpec kernelruntime.ProviderRuntimeSpec, sandboxID string) error {
	if s == nil || s.providerOperationRunner == nil {
		return errors.New("BYOC provider operation runtime is unavailable")
	}
	_, err := s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
		Runtime: runtimeSpec, Operation: "terminate",
		Request: map[string]any{"sandbox_id": sandboxID, "install_id": runtimeSpec.InstallID},
	})
	return err
}

func (s *Server) cleanupAgentBYOCProvider(ctx context.Context, runtimeSpec kernelruntime.ProviderRuntimeSpec) (compute.BYOCCleanupResult, error) {
	result := compute.BYOCCleanupResult{Terminated: []string{}, Failed: []string{}}
	if s == nil || s.providerOperationRunner == nil {
		result.Skipped = true
		result.Reason = "secure provider operation runtime is unavailable"
		return result, errors.New(result.Reason)
	}
	reconciled, err := s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
		Runtime: runtimeSpec, Operation: "reconcile", Request: map[string]any{"install_id": runtimeSpec.InstallID},
	})
	if err != nil {
		return result, err
	}
	sandboxes := anySliceValue(reconciled["sandboxes"])
	if len(sandboxes) > 1000 {
		return result, errors.New("owned BYOC sandbox inventory exceeds the cleanup cap")
	}
	result.Owned = len(sandboxes)
	for _, raw := range sandboxes {
		sandboxID := strings.TrimSpace(stringValue(mapValue(raw)["sandbox_id"]))
		if sandboxID == "" {
			result.Failed = append(result.Failed, "invalid-sandbox-id")
			continue
		}
		if err := s.terminateAgentBYOCSandbox(ctx, runtimeSpec, sandboxID); err != nil {
			result.Failed = append(result.Failed, sandboxID)
			continue
		}
		result.Terminated = append(result.Terminated, sandboxID)
	}
	if len(result.Failed) > 0 {
		return result, errors.New("one or more owned BYOC sandboxes could not be terminated")
	}
	return result, nil
}
