package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"synon-go/internal/agentruntime"
	"synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	maxAgentComputeProviderDefinitionBytes   = 64 << 10
	maxAgentComputeProviderRequirementsBytes = 2 << 20
)

type agentComputeProviderDefinition struct {
	ID        string `json:"id"`
	HelperEnv struct {
		Name     string   `json:"name"`
		Packages []string `json:"packages"`
		Pip      []string `json:"pip"`
	} `json:"helperEnv"`
	Requirements struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"requirements,omitempty"`
	Egress struct {
		Control agentComputeProviderEgressEntry   `json:"control"`
		Worker  []agentComputeProviderEgressEntry `json:"worker"`
		Blob    []agentComputeProviderEgressEntry `json:"blob"`
	} `json:"egress"`
}

const computeProviderProvisionNamespace = "compute-provider-environment"

func (s *Server) RunComputeProviderProvisioner(ctx context.Context) error {
	if s == nil || s.kernelManager == nil || !s.kernelManager.ManagedEnvironmentSupervisorEnabled() {
		if ctx != nil {
			<-ctx.Done()
		}
		return nil
	}
	if enabled, err := s.workspaceStore.HasEnabledBYOCProvider("modal"); err == nil && enabled {
		s.notifyComputeProviderProvisioner("modal")
	}
	if enabled, err := s.workspaceStore.HasManagedEndpoints(); err == nil && enabled {
		s.notifyComputeProviderProvisioner("inference")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case providerID := <-s.computeProviderProvisionWake:
			s.runComputeProviderProvisionAttempt(ctx, providerID)
		}
	}
}

func (s *Server) notifyComputeProviderProvisioner(providerID string) {
	if s == nil || s.computeProviderProvisionWake == nil {
		return
	}
	providerID = strings.TrimPrefix(strings.TrimSpace(providerID), "byoc:")
	if providerID == "" {
		return
	}
	select {
	case s.computeProviderProvisionWake <- providerID:
	default:
	}
}

func (s *Server) runComputeProviderProvisionAttempt(ctx context.Context, providerID string) {
	startedAt := time.Now().UTC()
	s.setComputeProviderProvisionStatus(providerID, map[string]any{
		"status": "provisioning", "started_at": startedAt.Format(time.RFC3339Nano),
	})
	environment, err := s.provisionAgentComputeProviderEnvironment(ctx, providerID)
	if context.Cause(ctx) != nil {
		return
	}
	status := map[string]any{
		"status": "ready", "environment": environment.Name, "generation": environment.Generation,
		"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err != nil {
		status = map[string]any{
			"status": "failed", "error": boundedProviderProvisionError(err),
			"updated_at": time.Now().UTC().Format(time.RFC3339Nano),
		}
	}
	s.setComputeProviderProvisionStatus(providerID, status)
	_, _ = s.publishGlobalEvent("environment_status", map[string]any{
		"environments": s.environmentStatus(false), "compute_provider": providerID, "provider_environment": status,
	})
}

func (s *Server) provisionAgentComputeProviderEnvironment(ctx context.Context, providerID string) (kernelruntime.ManagedEnvironment, error) {
	if providerID == "inference" {
		return s.provisionInferenceProviderEnvironment(ctx)
	}
	skillRoot, definition, err := s.loadAgentComputeProviderDefinition(providerID)
	if err != nil {
		return kernelruntime.ManagedEnvironment{}, err
	}
	if definition.Requirements.Path == "" || definition.Requirements.SHA256 == "" {
		return kernelruntime.ManagedEnvironment{}, errors.New("compute provider locked requirements are unavailable")
	}
	requirementsPath, err := providerRelativeRegularFile(skillRoot, definition.Requirements.Path)
	if err != nil {
		return kernelruntime.ManagedEnvironment{}, err
	}
	imports := []string{}
	switch providerID {
	case "modal":
		imports = []string{"modal", "python_socks"}
	default:
		return kernelruntime.ManagedEnvironment{}, fmt.Errorf("compute provider %q has no environment contract", providerID)
	}
	operationDigest := sha256.Sum256([]byte(
		"provider-environment-v1\x00" + providerID + "\x00" + definition.HelperEnv.Name + "\x00" + definition.Requirements.SHA256,
	))
	environment, err := s.kernelManager.CreateManagedEnvironment(ctx, kernelruntime.CreateManagedEnvironmentInput{
		Name: definition.HelperEnv.Name, Language: "python", Packages: definition.HelperEnv.Packages,
		ImportNames: imports, LockedRequirementsPath: requirementsPath,
		LockedRequirementsSHA256: definition.Requirements.SHA256,
		OperationID:              "provider-env-" + hex.EncodeToString(operationDigest[:16]),
	})
	if err != nil {
		return kernelruntime.ManagedEnvironment{}, err
	}
	if err := s.kernelManager.VerifyManagedEnvironmentImports(ctx, environment.Name, imports); err != nil {
		return kernelruntime.ManagedEnvironment{}, fmt.Errorf("verify compute provider environment: %w", err)
	}
	return environment, nil
}

func (s *Server) provisionInferenceProviderEnvironment(ctx context.Context) (kernelruntime.ManagedEnvironment, error) {
	if s == nil || s.kernelManager == nil || strings.TrimSpace(s.runtimeAssetsDir) == "" {
		return kernelruntime.ManagedEnvironment{}, errors.New("managed inference runtime assets are unavailable")
	}
	requirementsPath := filepath.Join(s.runtimeAssetsDir, "compute", "inference_requirements.lock")
	const requirementsSHA256 = "318b702d84611dc1fa36a8a75aa237f6d8641bfe93b2b3a178955c5f8c876fef"
	digest := sha256.Sum256([]byte("provider-environment-v1\x00inference\x00" + requirementsSHA256))
	environment, err := s.kernelManager.CreateManagedEnvironment(ctx, kernelruntime.CreateManagedEnvironmentInput{
		Name: "compute-provider-http", Language: "python", Packages: []string{"python=3.11", "pip"},
		ImportNames: []string{"httpx", "requests"}, LockedRequirementsPath: requirementsPath,
		LockedRequirementsSHA256: requirementsSHA256,
		OperationID:              "provider-env-" + hex.EncodeToString(digest[:16]),
	})
	if err != nil {
		return kernelruntime.ManagedEnvironment{}, err
	}
	if err := s.kernelManager.VerifyManagedEnvironmentImports(ctx, environment.Name, []string{"httpx", "requests"}); err != nil {
		return kernelruntime.ManagedEnvironment{}, fmt.Errorf("verify managed inference environment: %w", err)
	}
	return environment, nil
}

func providerRelativeRegularFile(root, relative string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	relative = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if !filepath.IsAbs(root) || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("compute provider requirements path is invalid")
	}
	candidate := filepath.Join(root, relative)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("compute provider Skill root is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || resolved != candidate || !hostPathWithin(resolvedRoot, resolved) {
		return "", errors.New("compute provider requirements path is unavailable")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("compute provider requirements file is unavailable")
	}
	return resolved, nil
}

func boundedProviderProvisionError(err error) string {
	message := "compute provider environment provisioning failed"
	if err != nil {
		message = strings.TrimSpace(err.Error())
	}
	if len(message) > 1024 {
		message = message[:1024]
	}
	return message
}

func (s *Server) setComputeProviderProvisionStatus(providerID string, value map[string]any) {
	if s == nil || s.runtimeStore == nil {
		return
	}
	_, _ = s.runtimeStore.Set(computeProviderProvisionNamespace, strings.TrimSpace(providerID), value)
}

func (s *Server) computeProviderProvisionStatus(providerID string) map[string]any {
	if s == nil || s.runtimeStore == nil {
		return map[string]any{"status": "unavailable"}
	}
	entry, found, err := s.runtimeStore.Get(computeProviderProvisionNamespace, strings.TrimSpace(providerID))
	if err != nil || !found {
		if providerID == "inference" && s.kernelManager != nil && s.kernelManager.RuntimeReady("python", "compute-provider-http") {
			return map[string]any{"status": "ready", "environment": "compute-provider-http"}
		}
		if s.kernelManager != nil {
			if _, definition, definitionErr := s.loadAgentComputeProviderDefinition(providerID); definitionErr == nil &&
				s.kernelManager.RuntimeReady("python", definition.HelperEnv.Name) {
				return map[string]any{"status": "ready", "environment": definition.HelperEnv.Name}
			}
		}
		return map[string]any{"status": "not_provisioned"}
	}
	return copyMapAny(mapValue(entry.Value))
}

type agentComputeProviderEgressEntry struct {
	Host   string `json:"host"`
	Suffix string `json:"suffix"`
	Port   int    `json:"port"`
}

type agentComputeProviderAuthority struct {
	Definition kernelruntime.ProviderRuntimeSpec
	Provider   workspace.ComputeProvider
}

func (s *Server) agentComputeProviderToolReady(identity *agentKernelContext) bool {
	if s == nil || identity == nil || s.kernelManager == nil || s.workspaceStore == nil {
		return false
	}
	if evidence := s.kernelConfinement(false); !evidence.Available {
		return false
	}
	providers, err := s.workspaceStore.ListComputeProviders(identity.access.UserID)
	if err != nil {
		return false
	}
	for _, provider := range providers {
		if provider.Family != "byoc" {
			continue
		}
		providerID := strings.TrimPrefix(provider.Name, "byoc:")
		if _, err := s.agentComputeProviderAuthority(identity.access, providerID, false); err == nil {
			return true
		}
	}
	endpoints, err := s.workspaceStore.ListManagedEndpoints(identity.access.UserID, "", false)
	if err == nil {
		for _, endpoint := range endpoints {
			if endpoint.State != "failed" && endpoint.State != "stopping" {
				if _, err := s.agentInferenceProviderAuthority(identity.access, endpoint.Name, false); err == nil {
					return true
				}
			}
		}
	}
	return false
}

func (s *Server) addComputeProviderSkillAuthorities(identity *agentKernelContext, authority map[string]struct{}) {
	if s == nil || identity == nil || authority == nil {
		return
	}
	if _, err := s.agentComputeProviderAuthority(identity.access, "modal", false); err == nil {
		authority["compute_provider_modal"] = struct{}{}
	}
	endpoints, err := s.workspaceStore.ListManagedEndpoints(identity.access.UserID, "", false)
	if err != nil {
		return
	}
	for _, endpoint := range endpoints {
		if _, err := s.agentInferenceProviderAuthority(identity.access, endpoint.Name, false); err == nil {
			authority["compute_provider_inference"] = struct{}{}
			return
		}
	}
}

func (s *Server) agentComputeProviderSessionActive(frameID, providerID string) bool {
	if s == nil || s.kernelManager == nil || s.workspaceStore == nil || frameID == "" {
		return false
	}
	providerID = strings.TrimPrefix(strings.TrimSpace(providerID), "byoc:")
	if providerID == "" {
		return false
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(context.Background(), frameID)
	if err != nil || !found {
		return false
	}
	return s.kernelManager.ProviderSessionActive(agentComputeProviderKernelID(access, providerID), providerID, "")
}

func (s *Server) executeAgentComputeProvider(
	ctx context.Context,
	identity *agentKernelContext,
	access workspace.KernelFrameAccess,
	call agentruntime.ToolCall,
	input map[string]any,
) (any, error) {
	providerID := strings.TrimPrefix(strings.TrimSpace(stringValue(input["provider"])), "byoc:")
	code := strings.TrimSpace(stringValue(input["code"]))
	if providerID == "" || code == "" || len([]byte(code)) > 100000 {
		return nil, errors.New("compute_provider requires a bounded provider and Python code")
	}
	if providerID != "modal" {
		if err := s.ensureManagedInferenceEndpointReady(ctx, access, providerID); err != nil {
			return nil, err
		}
	}
	authority, err := s.agentComputeProviderAuthority(access, providerID, true)
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.workspaceDir == "" {
		return nil, errors.New("compute_provider task workspace is unavailable")
	}
	frame := access.Frame
	spec := kernelruntime.SessionSpec{
		KernelID: agentComputeProviderKernelID(access, providerID),
		OwnerID:  access.UserID, ProjectID: frame.ProjectID, FrameID: frame.ID,
		FrameIncarnationID: frame.IncarnationID, RootFrameID: frame.RootFrameID,
		RootFrameIncarnationID: access.RootFrameIncarnationID,
		AgentName:              frame.AgentName, DelegateName: frame.DelegateName,
		KernelKind: "provider", Language: "python", Environment: authority.Definition.Environment,
		WorkspaceDir: identity.workspaceDir,
	}
	worker, _, err := s.kernelManager.StartProviderSession(ctx, spec, authority.Definition)
	if err != nil {
		return nil, err
	}
	reused := worker.ExecutionCount() > 0
	execID := uuid.NewString()
	hostCalls := s.agentKernelHostCallPolicy(ctx, access, identity.workspaceDir, []string{computeProviderToolName}, false)
	hostCalls.AllowedMethods = []string{"host.credentials.get", "host.compute.config_get"}
	hostCalls.MaxCalls = 128
	handle, err := s.kernelManager.Submit(kernelruntime.SubmitRequest{
		KernelID: spec.KernelID, ExpectedGeneration: worker.Generation(),
		OwnerID: spec.OwnerID, ProjectID: spec.ProjectID, FrameID: spec.FrameID,
		FrameIncarnationID: spec.FrameIncarnationID, RootFrameIncarnationID: spec.RootFrameIncarnationID,
		KernelKind: "provider", Language: "python", Environment: spec.Environment,
		ExecID: execID, ToolUseID: call.ID, ToolName: computeProviderToolName,
		Code: code, Origin: "agent", Timeout: 30 * time.Minute, HostCalls: hostCalls,
	})
	if err != nil {
		return nil, err
	}
	started, early, err := awaitAgentKernelExecutionStart(ctx, handle.Started(), handle.Done())
	if err != nil {
		s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
		return nil, err
	}
	session := kernelruntime.EnsuredSession{ID: spec.KernelID, Worker: worker, Reused: reused}
	if early != nil {
		if early.Err != nil {
			return nil, early.Err
		}
		if started.ExecID == "" {
			started = agentKernelSyntheticExecutionStart(kernelruntime.SubmitRequest{
				KernelID: spec.KernelID, FrameID: spec.FrameID, KernelKind: "provider", Language: "python",
				Environment: spec.Environment, ExecID: execID, ToolUseID: call.ID, ToolName: computeProviderToolName,
				Code: code, Origin: "agent",
			}, *early)
		}
		return s.finishAgentKernelExecution(ctx, access, spec, session, started, *early, defaultSessionRunnerOutputLimitBytes, nil, nil, nil, nil)
	}
	s.publishAgentKernelEvent(access, started, "start", map[string]any{
		"source": started.Code, "started_at": started.StartedAt.UTC().Format(time.RFC3339Nano),
	})
	select {
	case outcome := <-handle.Done():
		return s.finishAgentKernelExecution(ctx, access, spec, session, started, outcome, defaultSessionRunnerOutputLimitBytes, nil, nil, nil, nil)
	case <-ctx.Done():
		s.kernelManager.InterruptSession(spec.FrameID, spec.FrameIncarnationID, spec.RootFrameIncarnationID, execID)
		select {
		case <-handle.Done():
		case <-time.After(6 * time.Second):
		}
		return nil, agentKernelContextCause(ctx)
	}
}

func (s *Server) ensureManagedInferenceEndpointReady(ctx context.Context, access workspace.KernelFrameAccess, name string) error {
	endpoint, shouldRun, err := s.workspaceStore.ClaimManagedEndpointStart(name, access.UserID)
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
	result, startErr := compute.RunApprovedStart(ctx, compute.ManagedEndpointRegistration{
		Name: endpoint.Name, URL: endpoint.URL, Port: endpoint.Port,
		CredentialName: credential, SkillName: endpoint.SkillName,
		StartScript: endpoint.StartScript, StopScript: endpoint.StopScript, LivePath: endpoint.LivePath,
	}, endpoint.ApprovedScriptHash, 2*time.Minute)
	if _, finishErr := s.workspaceStore.FinishManagedEndpointStart(endpoint.ClaimHandle, result.Transcript, startErr); finishErr != nil {
		return finishErr
	}
	if startErr != nil {
		return fmt.Errorf("managed inference endpoint start failed and remains failed until explicit Stop or re-registration: %w", startErr)
	}
	return nil
}

func (s *Server) agentComputeProviderAuthority(
	access workspace.KernelFrameAccess,
	providerID string,
	includeCredentials bool,
) (agentComputeProviderAuthority, error) {
	providerID = strings.TrimPrefix(strings.TrimSpace(providerID), "byoc:")
	if providerID == "" {
		return agentComputeProviderAuthority{}, errors.New("compute provider id is required")
	}
	if providerID != "modal" {
		return s.agentInferenceProviderAuthority(access, providerID, includeCredentials)
	}
	provider, found, err := s.workspaceStore.GetComputeProvider("byoc:"+providerID, access.UserID)
	if err != nil || !found || provider.Family != "byoc" {
		return agentComputeProviderAuthority{}, errors.New("compute provider is not configured for this user")
	}
	settings, found, err := s.workspaceStore.GetBYOCSettings(providerID, access.UserID)
	if err != nil || !found || !settings.Enabled {
		return agentComputeProviderAuthority{}, errors.New("compute provider is not enabled")
	}
	skillRoot, definition, err := s.loadAgentComputeProviderDefinition(providerID)
	if err != nil {
		return agentComputeProviderAuthority{}, err
	}
	if s.kernelManager == nil || !s.kernelManager.RuntimeReady("python", definition.HelperEnv.Name) {
		return agentComputeProviderAuthority{}, fmt.Errorf("compute provider environment %q is not ready", definition.HelperEnv.Name)
	}
	credentials := map[string]string{"readiness": "readiness-only"}
	if includeCredentials {
		credentials, err = s.agentComputeProviderCredentials(access.UserID, providerID)
		if err != nil {
			return agentComputeProviderAuthority{}, err
		}
	} else if _, err := s.agentComputeProviderCredentials(access.UserID, providerID); err != nil {
		return agentComputeProviderAuthority{}, err
	}
	installID := "readiness-only"
	if includeCredentials {
		installID, err = s.ensureBYOCInstallID()
		if err != nil {
			return agentComputeProviderAuthority{}, err
		}
	}
	runtimeAssets := strings.TrimSpace(s.runtimeAssetsDir)
	if runtimeAssets == "" {
		return agentComputeProviderAuthority{}, errors.New("compute provider runtime assets are unavailable")
	}
	rules := agentComputeProviderEgressRules(definition)
	if len(rules) == 0 {
		return agentComputeProviderAuthority{}, errors.New("compute provider egress policy is unavailable")
	}
	configurationHash := agentComputeProviderConfigurationHash(providerID, settings, definition)
	appName := strings.TrimSpace(settings.AppName)
	if appName == "" {
		appName = workspace.DefaultModalAppName
	}
	prelude, err := loadAgentModalProviderPrelude(runtimeAssets, appName, configurationHash, providerID)
	if err != nil {
		return agentComputeProviderAuthority{}, err
	}
	return agentComputeProviderAuthority{
		Provider: provider,
		Definition: kernelruntime.ProviderRuntimeSpec{
			ProviderID: providerID, Environment: definition.HelperEnv.Name,
			BootstrapPath:    filepath.Join(runtimeAssets, "compute", "provider_kernel_bootstrap.py"),
			EntrypointPath:   filepath.Join(runtimeAssets, "compute", "operon_compute_provider", "__main__.py"),
			ProviderPath:     filepath.Join(skillRoot, "provider.py"),
			EnvironmentsPath: filepath.Join(skillRoot, "envs"),
			InstallID:        installID,
			OrganizationID:   access.UserID,
			ModalEnvironment: settings.EnvironmentName,
			AppName:          appName,
			PriorAppNames:    append([]string(nil), settings.PriorAppNames...),
			Credentials:      credentials,
			EgressRules:      rules,
			Dial:             s.computeProviderDial,
			ExtraEnvironment: map[string]string{
				"OPERON_BYOC_APP_NAME":             appName,
				"SYNON_PROVIDER_BOUND_CONFIG_HASH": configurationHash,
			},
			Prelude: prelude,
		},
	}, nil
}

func (s *Server) agentInferenceProviderAuthority(
	access workspace.KernelFrameAccess,
	providerID string,
	includeCredentials bool,
) (agentComputeProviderAuthority, error) {
	providerID = strings.TrimSpace(providerID)
	endpoints, err := s.workspaceStore.ListManagedEndpoints(access.UserID, providerID, false)
	if err != nil || len(endpoints) != 1 {
		return agentComputeProviderAuthority{}, errors.New("managed inference endpoint is unavailable")
	}
	endpoint := endpoints[0]
	if endpoint.State == "failed" || endpoint.State == "stopping" || endpoint.State == "starting" {
		return agentComputeProviderAuthority{}, fmt.Errorf("managed inference endpoint is %s", endpoint.State)
	}
	if s.kernelManager == nil || !s.kernelManager.RuntimeReady("python", "compute-provider-http") {
		return agentComputeProviderAuthority{}, errors.New("managed inference provider environment is not ready")
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" {
		return agentComputeProviderAuthority{}, errors.New("managed inference endpoint URL is invalid")
	}
	port := 0
	allowPrivate := false
	switch parsed.Scheme {
	case "https":
		port = 443
		if parsed.Port() != "" {
			port, err = strconv.Atoi(parsed.Port())
		}
	case "http":
		if parsed.Hostname() != "127.0.0.1" {
			return agentComputeProviderAuthority{}, errors.New("managed local inference endpoint must use literal loopback")
		}
		port, err = strconv.Atoi(parsed.Port())
		allowPrivate = true
	default:
		return agentComputeProviderAuthority{}, errors.New("managed inference endpoint scheme is invalid")
	}
	if err != nil || port < 1 || port > 65535 {
		return agentComputeProviderAuthority{}, errors.New("managed inference endpoint port is invalid")
	}
	credentials := map[string]string{"base_url": parsed.String()}
	credentialRevision := "none"
	if parsed.Scheme == "https" {
		credentialName := ""
		if endpoint.CredentialName != nil {
			credentialName = strings.TrimSpace(*endpoint.CredentialName)
		}
		if credentialName == "" {
			return agentComputeProviderAuthority{}, errors.New("managed inference endpoint credential is unavailable")
		}
		credential, err := s.kernelHostCredentialGet(access.UserID, credentialName)
		if err != nil {
			return agentComputeProviderAuthority{}, errors.New("managed inference endpoint credential is unavailable")
		}
		value := ""
		for _, key := range []string{"value", "token", "api_key", "key"} {
			if candidate := strings.TrimSpace(stringValue(mapValue(credential)[key])); candidate != "" {
				value = candidate
				break
			}
		}
		if value == "" {
			return agentComputeProviderAuthority{}, errors.New("managed inference endpoint credential has no scalar value")
		}
		if includeCredentials {
			credentials["credential_name"] = credentialName
			credentials["credential_value"] = value
		} else {
			credentials["credential_name"] = credentialName
			credentials["credential_value"] = "readiness-only"
		}
		credentialRevision = credentialName
	}
	if !includeCredentials && parsed.Scheme == "http" {
		credentials = map[string]string{"base_url": parsed.String()}
	}
	runtimeAssets := strings.TrimSpace(s.runtimeAssetsDir)
	if runtimeAssets == "" {
		return agentComputeProviderAuthority{}, errors.New("managed inference runtime assets are unavailable")
	}
	installDigest := sha256.Sum256([]byte("managed-inference\x00" + access.UserID + "\x00" + endpoint.Name))
	return agentComputeProviderAuthority{Definition: kernelruntime.ProviderRuntimeSpec{
		ProviderID: endpoint.Name, Environment: "compute-provider-http",
		BootstrapPath:    filepath.Join(runtimeAssets, "compute", "provider_kernel_bootstrap.py"),
		EntrypointPath:   filepath.Join(runtimeAssets, "compute", "operon_compute_provider", "__main__.py"),
		ProviderPath:     filepath.Join(runtimeAssets, "compute", "inference_provider.py"),
		EnvironmentsPath: filepath.Join(runtimeAssets, "compute"),
		InstallID:        "endpoint-" + hex.EncodeToString(installDigest[:12]), Credentials: credentials,
		EgressRules: []kernelruntime.ProviderEgressRule{{
			Host: parsed.Hostname(), Port: port, AllowPrivate: allowPrivate,
		}},
		ExtraEnvironment: map[string]string{
			"SYNON_PROVIDER_ENDPOINT_URL":       parsed.String(),
			"SYNON_PROVIDER_CONFIG_REVISION":    credentialRevision,
			"SYNON_PROVIDER_PROXY_LOCAL_TARGET": map[bool]string{true: "1", false: "0"}[allowPrivate],
		},
		Dial: s.computeProviderDial,
	}}, nil
}

func (s *Server) agentComputeProviderConfigProjection(access workspace.KernelFrameAccess, providerName string) (map[string]any, error) {
	providerID := strings.TrimPrefix(strings.TrimSpace(providerName), "byoc:")
	provider, found, err := s.workspaceStore.GetComputeProvider("byoc:"+providerID, access.UserID)
	if err != nil || !found || provider.Family != "byoc" {
		return nil, errors.New("compute provider is unavailable")
	}
	settings, found, err := s.workspaceStore.GetBYOCSettings(providerID, access.UserID)
	if err != nil || !found || !settings.Enabled {
		return nil, errors.New("compute provider is not enabled")
	}
	_, definition, err := s.loadAgentComputeProviderDefinition(providerID)
	if err != nil {
		return nil, err
	}
	appName := strings.TrimSpace(settings.AppName)
	if appName == "" {
		appName = workspace.DefaultModalAppName
	}
	egressMode := strings.TrimSpace(stringValue(settings.EgressPolicy["mode"]))
	return map[string]any{
		"provider": providerID, "environment": settings.EnvironmentName,
		"app_name": appName, "egress_mode": egressMode,
		"config_hash": agentComputeProviderConfigurationHash(providerID, settings, definition),
	}, nil
}

func agentComputeProviderConfigurationHash(
	providerID string,
	settings workspace.BYOCSettings,
	definition agentComputeProviderDefinition,
) string {
	raw, _ := json.Marshal(map[string]any{
		"provider": providerID, "environment": settings.EnvironmentName,
		"app_name": settings.AppName, "egress_policy": settings.EgressPolicy,
		"requirements_sha256": definition.Requirements.SHA256,
		"egress":              agentComputeProviderEgressRules(definition),
	})
	digest := sha256.Sum256(append([]byte("synon-provider-config-v1\x00"), raw...))
	return hex.EncodeToString(digest[:16])
}

func loadAgentModalProviderPrelude(runtimeAssets, appName, configHash, providerID string) (string, error) {
	path := filepath.Join(runtimeAssets, "compute", "modal_kernel_prelude.py")
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("compute provider preload is unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 100001))
	if err != nil || len(raw) == 0 || len(raw) > 100000 {
		return "", errors.New("compute provider preload exceeds the bounded contract")
	}
	prelude := string(raw)
	replacements := map[string]string{
		"__SYNON_APP_NAME__":    strconv.Quote(appName),
		"__SYNON_CONFIG_HASH__": strconv.Quote(configHash),
		"__SYNON_PROVIDER_ID__": strconv.Quote(providerID),
	}
	for marker, value := range replacements {
		if strings.Count(prelude, marker) != 1 {
			return "", errors.New("compute provider preload marker is invalid")
		}
		prelude = strings.Replace(prelude, marker, value, 1)
	}
	return prelude, nil
}

func (s *Server) loadAgentComputeProviderDefinition(providerID string) (string, agentComputeProviderDefinition, error) {
	if s == nil || s.skillCatalog == nil {
		return "", agentComputeProviderDefinition{}, errors.New("compute provider Skill catalog is unavailable")
	}
	var skillPath string
	want := "remote-compute-" + providerID
	for _, skill := range s.skillCatalog.Skills() {
		if skill.Name == want {
			skillPath = skill.Path
			break
		}
	}
	if skillPath == "" || strings.HasPrefix(skillPath, "builtin:") {
		return "", agentComputeProviderDefinition{}, fmt.Errorf("compute provider Skill %q is unavailable", want)
	}
	root := filepath.Dir(skillPath)
	manifestPath := filepath.Join(root, "provider.json")
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", agentComputeProviderDefinition{}, errors.New("compute provider definition is unavailable")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return "", agentComputeProviderDefinition{}, errors.New("compute provider definition is unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxAgentComputeProviderDefinitionBytes+1))
	if err != nil || len(raw) > maxAgentComputeProviderDefinitionBytes {
		return "", agentComputeProviderDefinition{}, errors.New("compute provider definition exceeds the bounded contract")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var definition agentComputeProviderDefinition
	if err := decoder.Decode(&definition); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return "", agentComputeProviderDefinition{}, errors.New("compute provider definition is invalid")
	}
	if definition.ID != providerID || definition.HelperEnv.Name == "" {
		return "", agentComputeProviderDefinition{}, errors.New("compute provider definition identity is invalid")
	}
	if definition.Requirements.Path != "" || definition.Requirements.SHA256 != "" {
		requirementsPath, pathErr := providerRelativeRegularFile(root, definition.Requirements.Path)
		if pathErr != nil {
			return "", agentComputeProviderDefinition{}, pathErr
		}
		requirementsFile, openErr := os.Open(requirementsPath)
		if openErr != nil {
			return "", agentComputeProviderDefinition{}, errors.New("compute provider locked requirements are unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(requirementsFile, maxAgentComputeProviderRequirementsBytes+1))
		_ = requirementsFile.Close()
		digest := sha256.Sum256(raw)
		if readErr != nil || len(raw) > maxAgentComputeProviderRequirementsBytes ||
			hex.EncodeToString(digest[:]) != strings.ToLower(strings.TrimPrefix(definition.Requirements.SHA256, "sha256:")) {
			return "", agentComputeProviderDefinition{}, errors.New("compute provider locked requirements checksum is invalid")
		}
	}
	return root, definition, nil
}

func agentComputeProviderEgressRules(definition agentComputeProviderDefinition) []kernelruntime.ProviderEgressRule {
	entries := append([]agentComputeProviderEgressEntry{definition.Egress.Control}, definition.Egress.Worker...)
	entries = append(entries, definition.Egress.Blob...)
	rules := make([]kernelruntime.ProviderEgressRule, 0, len(entries))
	for _, entry := range entries {
		port := entry.Port
		if port == 0 {
			port = 443
		}
		rules = append(rules, kernelruntime.ProviderEgressRule{Host: entry.Host, Suffix: entry.Suffix, Port: port})
	}
	return rules
}

func (s *Server) agentComputeProviderCredentials(userID, providerID string) (map[string]string, error) {
	if providerID != "modal" {
		return nil, fmt.Errorf("compute provider %q has no credential adapter", providerID)
	}
	profiles, _, profileErr := compute.ReadModalConfigProfiles(s.modalConfigPath)
	if profileErr == nil {
		if profile, found := compute.SelectModalConfigCredential(profiles); found {
			return map[string]string{"token_id": profile.TokenID, "token_secret": profile.TokenSecret}, nil
		}
	}
	secret, found, err := s.modalCredential(userID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("compute provider credentials are unavailable")
	}
	return map[string]string{
		"token_id": secret.Credentials["token_id"], "token_secret": secret.Credentials["token_secret"],
	}, nil
}

func agentComputeProviderKernelID(access workspace.KernelFrameAccess, providerID string) string {
	digest := sha256.Sum256([]byte(
		"synon-provider-kernel-v1\x00" + access.UserID + "\x00" + access.Frame.ProjectID + "\x00" +
			access.Frame.ID + "\x00" + access.Frame.IncarnationID + "\x00" + strings.TrimSpace(providerID),
	))
	return "provider-" + hex.EncodeToString(digest[:16])
}
