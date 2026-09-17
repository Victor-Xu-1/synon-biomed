package server

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	computeProviderJobPollInterval = 15 * time.Second
	computeProviderJobFailureLimit = 3
	computeProviderHarvestMaxFiles = 10000
	computeProviderHarvestMaxBytes = int64(20 << 30)
	computeProviderProbeNamespace  = "compute-provider-job-probes"
)

func (s *Server) RunComputeProviderJobSupervisor(ctx context.Context) error {
	if s == nil || s.workspaceStore == nil {
		if ctx != nil {
			<-ctx.Done()
		}
		return nil
	}
	ticker := time.NewTicker(computeProviderJobPollInterval)
	defer ticker.Stop()
	s.reconcileActiveComputeProviderJobs(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.computeProviderJobWake:
			s.reconcileActiveComputeProviderJobs(ctx)
		case <-ticker.C:
			s.reconcileActiveComputeProviderJobs(ctx)
		}
	}
}

func (s *Server) reconcileActiveComputeProviderJobs(ctx context.Context) {
	jobs := []workspace.OwnedComputeJob{}
	if s.providerOperationRunner != nil {
		byocJobs, err := s.workspaceStore.ListActiveBYOCJobs(1000)
		if err != nil {
			return
		}
		jobs = append(jobs, byocJobs...)
	}
	sshJobs, err := s.workspaceStore.ListActiveSSHJobs(1000)
	if err != nil {
		return
	}
	jobs = append(jobs, sshJobs...)
	for _, owned := range jobs {
		jobKey := owned.OwnerUserID + "\x00" + owned.Job.JobID
		s.computeProviderJobsMu.Lock()
		if s.computeProviderJobs[jobKey] {
			s.computeProviderJobsMu.Unlock()
			continue
		}
		s.computeProviderJobs[jobKey] = true
		s.computeProviderJobsMu.Unlock()
		go func(item workspace.OwnedComputeJob, key string) {
			defer func() {
				s.computeProviderJobsMu.Lock()
				delete(s.computeProviderJobs, key)
				s.computeProviderJobsMu.Unlock()
			}()
			if item.Job.ProviderFamily == "ssh" {
				s.reconcileAgentSSHJob(ctx, item)
				return
			}
			s.reconcileComputeProviderJob(ctx, item)
		}(owned, jobKey)
	}
}

func (s *Server) reconcileComputeProviderJob(ctx context.Context, owned workspace.OwnedComputeJob) {
	job := owned.Job
	if job.FrameID == nil || job.RootFrameID == nil {
		s.failComputeProviderJob(owned, workspace.ComputeJobOrphaned, "authority_missing", nil)
		return
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, *job.FrameID)
	if err != nil || !found || access.UserID != owned.OwnerUserID || access.Frame.RootFrameID != *job.RootFrameID {
		s.failComputeProviderJob(owned, workspace.ComputeJobOrphaned, "authority_changed", nil)
		return
	}
	providerID := strings.TrimPrefix(job.Provider, "byoc:")
	authority, err := s.agentComputeProviderAuthority(access, providerID, true)
	if err != nil {
		s.handleComputeProviderProbeFailure(owned, err)
		return
	}
	hardware := mapValue(job.HardwareDetails)
	installID := strings.TrimSpace(stringValue(hardware["install_id"]))
	if installID == "" || installID != authority.Definition.InstallID {
		s.failComputeProviderJob(owned, workspace.ComputeJobOrphaned, "install_identity_changed", nil)
		return
	}
	if job.State == workspace.ComputeJobPending || job.State == workspace.ComputeJobStaging {
		var recovered bool
		job, recovered = s.recoverAgentBYOCSubmission(ctx, owned, access, authority, hardware)
		if !recovered {
			return
		}
		owned.Job = job
	}
	if job.ExternalID == nil || strings.TrimSpace(*job.ExternalID) == "" {
		s.failComputeProviderJob(owned, workspace.ComputeJobOrphaned, "remote_identity_missing", nil)
		return
	}
	probe, err := s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
		Runtime: authority.Definition, Operation: "wait",
		Request: map[string]any{
			"sandbox_id": *job.ExternalID, "install_id": installID, "poll_seconds": 2, "probe_only": true,
		},
	})
	if err != nil {
		if s.handleComputeProviderProbeFailure(owned, err) {
			s.cleanupFailedComputeProviderSandbox(owned, authority.Definition)
		}
		return
	}
	s.resetComputeProviderProbeFailures(job.JobID)
	if stdout := strings.TrimSpace(stringValue(probe["stdout_tail"])); stdout != "" {
		_ = s.workspaceStore.AppendComputeJobLog(owned.OwnerUserID, job.JobID, "stdout", stdout+"\n")
	}
	if stderr := strings.TrimSpace(stringValue(probe["stderr_tail"])); stderr != "" {
		_ = s.workspaceStore.AppendComputeJobLog(owned.OwnerUserID, job.JobID, "stderr", stderr+"\n")
	}
	if !boolValue(probe["ready"], false) {
		return
	}
	if job.State == workspace.ComputeJobRunning {
		updated, transitionErr := s.workspaceStore.TransitionComputeJob(owned.OwnerUserID, job.JobID, workspace.ComputeJobHarvesting, "", time.Now().UTC())
		if transitionErr != nil {
			return
		}
		job = updated
	}
	workspaceDir := strings.TrimSpace(stringValue(hardware["workspace_dir"]))
	harvestedFiles := []string{}
	_, err = s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
		Runtime: authority.Definition, Operation: "wait",
		Request: map[string]any{
			"sandbox_id": *job.ExternalID, "install_id": installID, "poll_seconds": 2,
			"output_cap_bytes": byocMaximumInputBytes,
		},
		Collect: func(stage string, _ map[string]any) error {
			var extractErr error
			harvestedFiles, extractErr = extractAgentBYOCHarvest(stage, workspaceDir, job.JobID)
			return extractErr
		},
	})
	if err != nil {
		harvesting := workspace.OwnedComputeJob{OwnerUserID: owned.OwnerUserID, Job: job}
		if s.handleComputeProviderProbeFailure(harvesting, err) {
			s.cleanupFailedComputeProviderSandbox(harvesting, authority.Definition)
		}
		return
	}
	next, errorKind := classifyComputeProviderTerminal(probe)
	terminalDetails := map[string]any{
		"exit_code":   int(numberValue(probe["job_exit_code"])),
		"job_wall_s":  int(numberValue(probe["job_wall_s"])),
		"stdout_tail": stringValue(probe["stdout_tail"]), "stderr_tail": stringValue(probe["stderr_tail"]),
		"output_files":      harvestedFiles,
		"output_file_count": len(harvestedFiles),
		"featured_files":    featuredComputeProviderFiles(harvestedFiles, anySliceValue(hardware["outputs"])),
		"deadline_fired":    boolValue(probe["deadline_fired"], false),
		"job_timeout_fired": boolValue(probe["job_timeout_fired"], false),
	}
	_ = s.workspaceStore.SetComputeJobResult(owned.OwnerUserID, job.JobID, terminalDetails)
	_, err = s.transitionAgentComputeJobTerminal(owned, next, errorKind, time.Now().UTC(), terminalDetails)
	if err != nil {
		return
	}
	handleID := strings.TrimSpace(stringValue(hardware["handle_id"]))
	if handleID != "" {
		s.releaseAgentComputeHandle(handleID, job.JobID, true)
	} else {
		_ = s.terminateAgentBYOCSandbox(context.Background(), authority.Definition, *job.ExternalID)
	}
}

func (s *Server) recoverAgentBYOCSubmission(
	ctx context.Context,
	owned workspace.OwnedComputeJob,
	access workspace.KernelFrameAccess,
	authority agentComputeProviderAuthority,
	hardware map[string]any,
) (workspace.ComputeJob, bool) {
	job := owned.Job
	stage, archive, err := s.validateAgentBYOCRecoveryState(job, authority.Definition, hardware)
	if err != nil {
		s.failAgentBYOCRecovery(owned, authority.Definition, workspace.ComputeJobOrphaned, "recovery_state_invalid", err, "", "")
		return job, false
	}
	deadline := time.Unix(int64(numberValue(hardware["sandbox_deadline_epoch"])), 0).UTC()
	if !deadline.After(time.Now()) {
		sandboxID := ""
		if job.ExternalID != nil {
			sandboxID = strings.TrimSpace(*job.ExternalID)
		}
		s.failAgentBYOCRecovery(owned, authority.Definition, workspace.ComputeJobTimedOut, "timeout", errors.New("BYOC submission deadline elapsed before recovery"), sandboxID, stage)
		return job, false
	}
	submissionID := strings.TrimSpace(stringValue(hardware["submission_id"]))
	jobTimeout := time.Duration(numberValue(hardware["job_timeout_seconds"])) * time.Second
	handleID := strings.TrimSpace(stringValue(hardware["handle_id"]))
	sandboxID := ""
	if job.ExternalID != nil {
		sandboxID = strings.TrimSpace(*job.ExternalID)
	}
	if job.State == workspace.ComputeJobPending {
		sandboxID = strings.TrimSpace(stringValue(hardware["sandbox_hint"]))
		if sandboxID != "" && handleID == "" {
			s.failAgentBYOCRecovery(owned, authority.Definition, workspace.ComputeJobOrphaned, "recovery_state_invalid", errors.New("BYOC sandbox hint has no durable handle authority"), "", stage)
			return job, false
		}
		if sandboxID == "" {
			found, findErr := s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
				Runtime: authority.Definition, Operation: "find_owned_submission",
				Request: map[string]any{
					"install_id": authority.Definition.InstallID, "job_id": job.JobID, "submission_id": submissionID,
				},
			})
			if findErr != nil {
				if s.handleComputeProviderProbeFailure(owned, findErr) {
					s.removeAgentBYOCRecoveryStage(stage, job.JobID)
				}
				return job, false
			}
			rawIDs := anySliceValue(found["sandbox_ids"])
			sandboxIDs := stringArrayValue(found["sandbox_ids"])
			if len(rawIDs) != len(sandboxIDs) || len(sandboxIDs) > 2 {
				s.failAgentBYOCRecovery(owned, authority.Definition, workspace.ComputeJobOrphaned, "ambiguous_remote_identity", errors.New("provider returned an invalid owned submission inventory"), "", stage)
				return job, false
			}
			switch len(sandboxIDs) {
			case 0:
				providerSpec := mapValue(hardware["provider_spec"])
				created, createErr := s.createAgentBYOCSandbox(ctx, authority.Definition, job.JobID, submissionID, providerSpec)
				if createErr != nil {
					if s.handleComputeProviderProbeFailure(owned, createErr) {
						s.removeAgentBYOCRecoveryStage(stage, job.JobID)
					}
					return job, false
				}
				sandboxID = strings.TrimSpace(stringValue(created["sandbox_id"]))
			case 1:
				sandboxID = strings.TrimSpace(sandboxIDs[0])
			default:
				s.failAgentBYOCRecovery(owned, authority.Definition, workspace.ComputeJobOrphaned, "ambiguous_remote_identity", errors.New("multiple provider sandboxes match one durable submission"), "", stage)
				return job, false
			}
		}
		if sandboxID == "" || len(sandboxID) > 512 || strings.ContainsAny(sandboxID, "\x00\r\n") {
			s.failAgentBYOCRecovery(owned, authority.Definition, workspace.ComputeJobOrphaned, "remote_identity_invalid", errors.New("provider returned an invalid sandbox id"), "", stage)
			return job, false
		}
		updated, bindErr := s.workspaceStore.BindComputeJobExternalForStaging(owned.OwnerUserID, job.JobID, sandboxID, "")
		if bindErr != nil {
			transient := &kernelruntime.ProviderOperationError{Kind: "transient", Message: "durable sandbox binding could not be committed"}
			if s.handleComputeProviderProbeFailure(owned, transient) {
				_ = s.terminateAgentBYOCSandbox(context.Background(), authority.Definition, sandboxID)
				s.removeAgentBYOCRecoveryStage(stage, job.JobID)
			}
			return job, false
		}
		job = updated
		owned.Job = updated
		if handleID != "" {
			s.bindAgentComputeHandleSandbox(handleID, job.JobID, sandboxID)
		}
	}
	if job.State != workspace.ComputeJobStaging || sandboxID == "" {
		s.failAgentBYOCRecovery(owned, authority.Definition, workspace.ComputeJobOrphaned, "recovery_state_invalid", errors.New("BYOC recovery did not reach the staging authority"), sandboxID, stage)
		return job, false
	}
	request := agentBYOCSubmissionRequest(authority.Definition.InstallID, submissionID, sandboxID, archive, jobTimeout, deadline, time.Now())
	_, err = s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
		Runtime: authority.Definition, Operation: "submit", Request: request,
		Prepare: func(operationStage string) error {
			return copyAgentBYOCStagedArchive(stage, operationStage, archive)
		},
	})
	if err != nil {
		if s.handleComputeProviderProbeFailure(owned, err) {
			s.cleanupFailedComputeProviderSandbox(owned, authority.Definition)
			s.removeAgentBYOCRecoveryStage(stage, job.JobID)
		}
		return job, false
	}
	updated, err := s.workspaceStore.TransitionComputeJob(owned.OwnerUserID, job.JobID, workspace.ComputeJobRunning, "", time.Now().UTC())
	if err != nil {
		_ = s.workspaceStore.AppendComputeJobLog(owned.OwnerUserID, job.JobID, "stderr", "submission reached the provider but its running state could not be committed; recovery will retry idempotently\n")
		return job, false
	}
	s.resetComputeProviderProbeFailures(job.JobID)
	s.removeAgentBYOCRecoveryStage(stage, job.JobID)
	_ = s.workspaceStore.AppendComputeJobLog(owned.OwnerUserID, job.JobID, "combined", fmt.Sprintf("recovered submission %d bytes sha256=%s\n", archive.Bytes, archive.SHA256))
	return updated, true
}

func (s *Server) validateAgentBYOCRecoveryState(
	job workspace.ComputeJob,
	runtimeSpec kernelruntime.ProviderRuntimeSpec,
	hardware map[string]any,
) (string, byocInputArchive, error) {
	if s == nil || strings.TrimSpace(s.fileRoot) == "" || !computeJobIDPattern.MatchString(job.JobID) {
		return "", byocInputArchive{}, errors.New("BYOC durable staging authority is unavailable")
	}
	expectedStage := filepath.Clean(filepath.Join(s.fileRoot, "provider-jobs", job.JobID))
	recordedStage := filepath.Clean(strings.TrimSpace(stringValue(hardware["staging_dir"])))
	if recordedStage == "." || recordedStage != expectedStage {
		return "", byocInputArchive{}, errors.New("BYOC durable staging path changed")
	}
	info, err := os.Lstat(expectedStage)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", byocInputArchive{}, errors.New("BYOC durable staging directory is invalid")
	}
	resolved, err := filepath.EvalSymlinks(expectedStage)
	if err != nil || filepath.Clean(resolved) != expectedStage {
		return "", byocInputArchive{}, errors.New("BYOC durable staging directory changed")
	}
	archive, err := inspectAgentBYOCStagedArchive(filepath.Join(expectedStage, "in.tar.gz"))
	if err != nil {
		return "", byocInputArchive{}, err
	}
	expectedArchive := byocInputArchive{
		SHA256: strings.TrimSpace(stringValue(hardware["archive_sha256"])),
		Bytes:  int64(numberValue(hardware["archive_bytes"])),
	}
	if archive != expectedArchive {
		return "", byocInputArchive{}, errors.New("BYOC durable input archive no longer matches its authority record")
	}
	submissionID := strings.TrimSpace(stringValue(hardware["submission_id"]))
	if !byocSubmissionIDPattern.MatchString(submissionID) {
		return "", byocInputArchive{}, errors.New("BYOC durable submission id is invalid")
	}
	jobTimeout := int64(numberValue(hardware["job_timeout_seconds"]))
	deadlineEpoch := int64(numberValue(hardware["sandbox_deadline_epoch"]))
	latestDeadline := job.StartedAt.UTC().Add(byocMaximumContainer + byocHarvestMargin + 5*time.Minute)
	if jobTimeout < 1 || time.Duration(jobTimeout)*time.Second > byocMaximumContainer || deadlineEpoch < 1 || time.Unix(deadlineEpoch, 0).After(latestDeadline) {
		return "", byocInputArchive{}, errors.New("BYOC durable deadline contract is invalid")
	}
	currentConfig := strings.TrimSpace(runtimeSpec.ExtraEnvironment["SYNON_PROVIDER_BOUND_CONFIG_HASH"])
	if currentConfig == "" || strings.TrimSpace(stringValue(hardware["provider_config_sha256"])) != currentConfig {
		return "", byocInputArchive{}, errors.New("BYOC provider configuration changed while the job was recoverable")
	}
	providerSpec := mapValue(hardware["provider_spec"])
	providerSpecSHA256, err := agentBYOCProviderSpecSHA256(providerSpec)
	if err != nil || providerSpecSHA256 != strings.TrimSpace(stringValue(hardware["provider_spec_sha256"])) {
		return "", byocInputArchive{}, errors.New("BYOC provider specification changed while the job was recoverable")
	}
	return expectedStage, archive, nil
}

func (s *Server) failAgentBYOCRecovery(
	owned workspace.OwnedComputeJob,
	runtimeSpec kernelruntime.ProviderRuntimeSpec,
	next string,
	kind string,
	runErr error,
	sandboxID string,
	stage string,
) {
	if !s.failComputeProviderJob(owned, next, kind, runErr) {
		return
	}
	if sandboxID != "" {
		_ = s.terminateAgentBYOCSandbox(context.Background(), runtimeSpec, sandboxID)
	}
	s.removeAgentBYOCRecoveryStage(stage, owned.Job.JobID)
}

func (s *Server) cleanupFailedComputeProviderSandbox(owned workspace.OwnedComputeJob, runtimeSpec kernelruntime.ProviderRuntimeSpec) {
	if owned.Job.ExternalID == nil {
		return
	}
	sandboxID := strings.TrimSpace(*owned.Job.ExternalID)
	if sandboxID != "" {
		_ = s.terminateAgentBYOCSandbox(context.Background(), runtimeSpec, sandboxID)
	}
}

func (s *Server) removeAgentBYOCRecoveryStage(stage, jobID string) {
	if s == nil || stage == "" || !computeJobIDPattern.MatchString(jobID) {
		return
	}
	expected := filepath.Clean(filepath.Join(s.fileRoot, "provider-jobs", jobID))
	if filepath.Clean(stage) == expected {
		_ = os.RemoveAll(expected)
	}
}

func (s *Server) handleComputeProviderProbeFailure(owned workspace.OwnedComputeJob, runErr error) bool {
	kind := providerOperationFailureKind(runErr)
	definitive := kind == "not_found" || kind == "ownership_mismatch" || kind == "unauthorized" || kind == "invalid_request"
	failures := s.incrementComputeProviderProbeFailures(owned.Job.JobID, kind)
	if !definitive && failures < computeProviderJobFailureLimit {
		_ = s.workspaceStore.AppendComputeJobLog(owned.OwnerUserID, owned.Job.JobID, "stderr", fmt.Sprintf("provider probe %d/%d failed: %s\n", failures, computeProviderJobFailureLimit, kind))
		return false
	}
	next := workspace.ComputeJobOrphaned
	if kind == "unauthorized" || kind == "invalid_request" {
		next = workspace.ComputeJobFailed
	}
	s.failComputeProviderJob(owned, next, kind, runErr)
	return true
}

func (s *Server) failComputeProviderJob(owned workspace.OwnedComputeJob, next, kind string, runErr error) bool {
	_, err := s.transitionAgentComputeJobTerminal(owned, next, kind, time.Now().UTC(), nil)
	if err != nil {
		return false
	}
	if runErr != nil {
		_ = s.workspaceStore.AppendComputeJobLog(owned.OwnerUserID, owned.Job.JobID, "stderr", boundedProviderProvisionError(runErr)+"\n")
	}
	if handleID := strings.TrimSpace(stringValue(mapValue(owned.Job.HardwareDetails)["handle_id"])); handleID != "" {
		s.releaseAgentComputeHandle(handleID, owned.Job.JobID, false)
	}
	return true
}

func (s *Server) transitionAgentComputeJobTerminal(
	owned workspace.OwnedComputeJob,
	next, kind string,
	at time.Time,
	details map[string]any,
) (workspace.ComputeJob, error) {
	if owned.Job.FrameID == nil || owned.Job.RootFrameID == nil {
		return s.workspaceStore.TransitionComputeJob(owned.OwnerUserID, owned.Job.JobID, next, kind, at)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	projected := owned.Job
	projected.State = next
	endedAt := at.Format(time.RFC3339Nano)
	projected.EndedAtISO = &endedAt
	if strings.TrimSpace(kind) == "" {
		projected.ErrorKind = nil
	} else {
		errorKind := strings.TrimSpace(kind)
		projected.ErrorKind = &errorKind
	}
	payload := kernelComputeJobProjection(projected)
	for key, value := range details {
		payload[key] = value
	}
	updated, _, _, err := s.workspaceStore.TransitionComputeJobWithNotification(
		context.Background(), owned.OwnerUserID, owned.Job.JobID, next, kind, at,
		workspace.CreateNotificationInput{
			ID:            uuid.NewSHA1(uuid.NameSpaceOID, []byte("synon-compute:"+owned.Job.JobID)).String(),
			SenderFrameID: *owned.Job.FrameID, RecipientFrameID: *owned.Job.FrameID, RootFrameID: *owned.Job.RootFrameID,
			OwnerUserID: owned.OwnerUserID, NotificationType: "compute_done", Payload: payload,
		},
	)
	return updated, err
}

func (s *Server) incrementComputeProviderProbeFailures(jobID, kind string) int {
	if s.runtimeStore == nil {
		return computeProviderJobFailureLimit
	}
	count := 0
	entry, found, _ := s.runtimeStore.Get(computeProviderProbeNamespace, jobID)
	if found {
		count = int(numberValue(mapValue(entry.Value)["count"]))
	}
	count++
	_, _ = s.runtimeStore.Set(computeProviderProbeNamespace, jobID, map[string]any{
		"count": count, "kind": kind, "updated_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	return count
}

func (s *Server) resetComputeProviderProbeFailures(jobID string) {
	if s.runtimeStore != nil {
		_, _ = s.runtimeStore.Delete(computeProviderProbeNamespace, jobID)
	}
}

func classifyComputeProviderTerminal(probe map[string]any) (string, string) {
	exitCode := int(numberValue(probe["job_exit_code"]))
	if boolValue(probe["deadline_fired"], false) || boolValue(probe["job_timeout_fired"], false) {
		return workspace.ComputeJobTimedOut, "timeout"
	}
	if strings.TrimSpace(stringValue(probe["phase_read_error"])) != "" {
		return workspace.ComputeJobFailed, "phase_invalid"
	}
	if exitCode == 0 {
		return workspace.ComputeJobDone, ""
	}
	return workspace.ComputeJobFailed, "nonzero_exit"
}

type agentComputeHarvestPolicy struct {
	Outputs       []any
	Exclude       []string
	MaxFileBytes  int64
	MaxTotalBytes int64
	RemoteURI     func(string) string
}

func extractAgentBYOCHarvest(stage, workspaceDir, jobID string) ([]string, error) {
	files, _, err := extractAgentComputeHarvest(stage, workspaceDir, jobID, agentComputeHarvestPolicy{})
	return files, err
}

func extractAgentComputeHarvest(
	stage string,
	workspaceDir string,
	jobID string,
	policy agentComputeHarvestPolicy,
) ([]string, []map[string]any, error) {
	workspaceRoot, err := canonicalHostDirectory(workspaceDir)
	if err != nil {
		return nil, nil, errors.New("compute harvest workspace is unavailable")
	}
	target := filepath.Join(workspaceRoot, "hpc", jobID)
	existing := false
	existingFiles := []string{}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		existing = true
		existingFiles, err = listRelativeRegularFiles(workspaceRoot, target, computeProviderHarvestMaxFiles)
		if err != nil {
			return nil, nil, err
		}
	}
	if !existing {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, nil, err
		}
	}
	temporary := ""
	if !existing {
		temporary, err = os.MkdirTemp(filepath.Dir(target), "."+jobID+"-harvest-")
		if err != nil {
			return nil, nil, err
		}
		defer os.RemoveAll(temporary)
	}
	archive, err := os.Open(filepath.Join(stage, "out.tar.gz"))
	if err != nil {
		return nil, nil, errors.New("compute harvest archive is unavailable")
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return nil, nil, errors.New("compute harvest archive is invalid")
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	files := []string{}
	left := []map[string]any{}
	extractedTotal := int64(0)
	scannedTotal := int64(0)
	entries := 0
	maxTotal := policy.MaxTotalBytes
	if maxTotal <= 0 || maxTotal > computeProviderHarvestMaxBytes {
		maxTotal = computeProviderHarvestMaxBytes
	}
	maxFile := policy.MaxFileBytes
	if maxFile <= 0 || maxFile > computeProviderHarvestMaxBytes {
		maxFile = computeProviderHarvestMaxBytes
	}
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, nil, errors.New("compute harvest archive could not be read")
		}
		entries++
		if entries > computeProviderHarvestMaxFiles || header.Size < 0 || scannedTotal+header.Size > computeProviderHarvestMaxBytes {
			return nil, nil, errors.New("compute harvest archive exceeds the extraction cap")
		}
		scannedTotal += header.Size
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || name == ".." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return nil, nil, errors.New("compute harvest archive contains an unsafe path")
		}
		destinationRoot := temporary
		if existing {
			destinationRoot = target
		}
		destination := filepath.Join(destinationRoot, name)
		if !hostPathWithin(destinationRoot, destination) {
			return nil, nil, errors.New("compute harvest archive escapes the extraction root")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if !existing {
				if err := os.MkdirAll(destination, 0o700); err != nil {
					return nil, nil, err
				}
			}
		case tar.TypeReg, tar.TypeRegA:
			archiveName := filepath.ToSlash(name)
			include, reason := agentComputeHarvestInclusion(archiveName, header.Size, extractedTotal, maxFile, maxTotal, policy)
			if !include {
				if _, err := io.CopyN(io.Discard, reader, header.Size); err != nil {
					return nil, nil, errors.New("compute harvest skipped file could not be drained")
				}
				if reason != "excluded" && policy.RemoteURI != nil {
					left = append(left, map[string]any{
						"uri": policy.RemoteURI(archiveName), "path": archiveName,
						"size": header.Size, "reason": reason,
					})
				}
				continue
			}
			extractedTotal += header.Size
			workspaceRelative := filepath.ToSlash(filepath.Join("hpc", jobID, name))
			files = append(files, workspaceRelative)
			if existing {
				if _, err := io.CopyN(io.Discard, reader, header.Size); err != nil {
					return nil, nil, errors.New("compute harvest existing file could not be drained")
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return nil, nil, err
			}
			file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, nil, err
			}
			written, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil || written != header.Size {
				return nil, nil, errors.New("compute harvest file changed during extraction")
			}
		default:
			return nil, nil, errors.New("compute harvest archive contains a non-regular entry")
		}
	}
	if existing {
		return existingFiles, left, nil
	}
	if err := os.Rename(temporary, target); err != nil {
		return nil, nil, err
	}
	return files, left, nil
}

func agentComputeHarvestInclusion(
	archiveName string,
	size int64,
	extractedTotal int64,
	maxFile int64,
	maxTotal int64,
	policy agentComputeHarvestPolicy,
) (bool, string) {
	isLog := archiveName == "stdout.log" || archiveName == "stderr.log"
	if !isLog {
		if !strings.HasPrefix(archiveName, "out/") {
			return false, "excluded"
		}
		for _, pattern := range policy.Exclude {
			if computeOutputGlobMatches(pattern, archiveName) {
				return false, "excluded"
			}
		}
	}
	if !isLog && len(policy.Outputs) > 0 {
		matched := false
		for _, raw := range policy.Outputs {
			pattern := strings.TrimSpace(stringValue(raw))
			if item := mapValue(raw); len(item) > 0 {
				pattern = strings.TrimSpace(stringValue(item["glob"]))
			}
			if computeOutputGlobMatches(pattern, archiveName) {
				matched = true
				break
			}
		}
		if !matched {
			return false, "not_selected"
		}
	}
	if size > maxFile {
		return false, "max_file_bytes"
	}
	if extractedTotal+size > maxTotal {
		return false, "max_total_bytes"
	}
	return true, ""
}

func listRelativeRegularFiles(workspaceRoot, target string, limit int) ([]string, error) {
	files := []string{}
	err := filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(workspaceRoot, path)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(relative))
			if len(files) > limit {
				return errors.New("BYOC harvest inventory exceeds the file cap")
			}
		}
		return nil
	})
	return files, err
}

func featuredComputeProviderFiles(files []string, outputs []any) []string {
	featured := []string{}
	for _, file := range files {
		if !strings.Contains(file, "/out/") {
			continue
		}
		if len(outputs) == 0 {
			featured = append(featured, file)
		} else {
			for _, raw := range outputs {
				pattern := strings.TrimSpace(stringValue(raw))
				visibility := "featured"
				if item := mapValue(raw); len(item) > 0 {
					pattern = strings.TrimSpace(firstNonEmpty(stringValue(item["glob"]), stringValue(item["path"])))
					visibility = strings.TrimSpace(firstNonEmpty(stringValue(item["visibility"]), "featured"))
				}
				if visibility == "hidden" || pattern == "" {
					continue
				}
				base := strings.TrimPrefix(file[strings.Index(file, "/out/")+1:], "out/")
				candidate := "out/" + base
				if computeOutputGlobMatches(pattern, candidate) {
					featured = append(featured, file)
					break
				}
			}
		}
		if len(featured) == 20 {
			break
		}
	}
	return featured
}

func computeOutputGlobMatches(pattern, candidate string) bool {
	pattern = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(pattern)))), "./")
	candidate = strings.TrimPrefix(filepath.ToSlash(candidate), "./")
	if pattern == "" || pattern == "." || strings.HasPrefix(pattern, "../") || filepath.IsAbs(pattern) {
		return false
	}
	relative := strings.TrimPrefix(candidate, "out/")
	targets := []string{relative}
	if strings.HasPrefix(pattern, "out/") {
		targets = []string{candidate}
	}
	var expression strings.Builder
	expression.WriteString("^")
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				index++
				if index+1 < len(pattern) && pattern[index+1] == '/' {
					index++
					expression.WriteString("(?:.*/)?")
				} else {
					expression.WriteString(".*")
				}
			} else {
				expression.WriteString("[^/]*")
			}
		case '?':
			expression.WriteString("[^/]")
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[index])))
		}
	}
	expression.WriteString("$")
	compiled, err := regexp.Compile(expression.String())
	if err != nil {
		return false
	}
	for _, target := range targets {
		if compiled.MatchString(target) {
			return true
		}
	}
	return false
}
