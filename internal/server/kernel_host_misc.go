package server

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	compute "synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

func isKernelMiscHostMethod(method string) bool {
	switch method {
	case "host.capabilities", "host.credentials.list", "host.credentials.get", "host.credentials.request",
		"host.exec_peek", "host.exec_interrupt", "host.findings", "host.findings.mark_addressed",
		"host.get_local_compute_stats", "host.get_user_email", "host.query", "host.query.schema", "host.reasoning_model", "host.submit_output":
		return true
	case "host.model_endpoints.free_port", "host.model_endpoints.register":
		return true
	default:
		return false
	}
}

func (s *Server) handleKernelMiscHostCall(
	ctx context.Context,
	access workspace.KernelFrameAccess,
	method string,
	args []any,
	kwargs map[string]any,
) (any, error) {
	switch method {
	case "host.capabilities":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		capabilities := map[string]any{}
		for _, name := range kernelHostMethods {
			capabilities[name] = true
		}
		return capabilities, nil
	case "host.credentials.list":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		return s.kernelHostCredentialList(access.UserID)
	case "host.credentials.get":
		name, err := kernelHostSingleNameInput(args, kwargs, method)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		return s.kernelHostCredentialGet(access.UserID, name)
	case "host.credentials.request":
		provider, err := kernelHostSingleNameInput(args, kwargs, method)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		credential, err := s.kernelHostCredentialGet(access.UserID, provider)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("credential_unavailable", "credential is unavailable or was not granted")
		}
		for _, key := range []string{"value", "token", "api_key", "key"} {
			if value := strings.TrimSpace(stringValue(mapValue(credential)[key])); value != "" {
				return value, nil
			}
		}
		return nil, kernelruntime.NewHostCallError("credential_unavailable", "credential has no scalar value")
	case "host.exec_peek":
		execID, err := kernelHostSingleNameInput(args, kwargs, method)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		return s.kernelHostExecutionPeek(access, execID)
	case "host.exec_interrupt":
		execID, err := kernelHostSingleNameInput(args, kwargs, method)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		if s == nil || s.kernelManager == nil {
			return nil, kernelruntime.NewHostCallError("unavailable", "kernel runtime is unavailable")
		}
		result := s.kernelManager.InterruptSession(access.Frame.ID, access.Frame.IncarnationID, access.RootFrameIncarnationID, execID)
		if !result.Interrupted && !result.Dequeued {
			return nil, kernelruntime.NewHostCallError("not_found", "background execution was not found")
		}
		return map[string]any{"interrupted": result.Interrupted, "dequeued": result.Dequeued, "via": result.Via, "reason": result.Reason}, nil
	case "host.findings":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		if s == nil || s.workspaceStore == nil {
			return nil, kernelruntime.NewHostCallError("unavailable", "finding store is unavailable")
		}
		checks, err := s.workspaceStore.ListVerificationChecks(access.Frame.RootFrameID, "open")
		if err != nil {
			return nil, kernelruntime.NewHostCallError("storage_error", "findings could not be read")
		}
		return checks, nil
	case "host.findings.mark_addressed":
		ids, note, err := kernelHostFindingAddressInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		if err := s.workspaceStore.ResolveVerificationChecks(access.Frame.RootFrameID, ids, note); err != nil {
			return nil, kernelruntime.NewHostCallError("storage_error", "findings could not be marked addressed")
		}
		return map[string]any{"addressed": ids, "note": note, "status": "pending_review"}, nil
	case "host.get_local_compute_stats":
		return s.kernelHostLocalComputeStats(access), nil
	case "host.get_user_email":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		if email, found := s.webAccounts.EmailForID(access.UserID); found {
			return email, nil
		}
		return nil, kernelruntime.NewHostCallError("contact_email_unavailable", "contact email is unavailable or was not granted")
	case "host.query":
		input, err := kernelHostQueryInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		return s.workspaceStore.ReadOnlyQuery(ctx, workspace.ReadOnlyQueryInput{
			UserID: access.UserID, ProjectID: access.Frame.ProjectID, SQL: input.SQL,
			Params: input.Params, Limit: input.Limit, Scope: input.Scope,
		})
	case "host.query.schema":
		if len(args) != 0 {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.query.schema takes only optional scope keyword")
		}
		scope := strings.TrimSpace(stringValue(kwargs["scope"]))
		if scope == "" {
			scope = "project"
		}
		return s.workspaceStore.ReadOnlyQuerySchema(ctx, access.UserID, access.Frame.ProjectID, scope)
	case "host.model_endpoints.free_port":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		port, err := s.kernelHostManagedEndpointFreePort(access.UserID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"port": port}, nil
	case "host.model_endpoints.register":
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.model_endpoints.register requires one configuration object")
		}
		input, ok := args[0].(map[string]any)
		if !ok {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "host.model_endpoints.register configuration must be an object")
		}
		return s.kernelHostRegisterManagedEndpoint(ctx, access, input)
	case "host.reasoning_model":
		if err := kernelHostExpectNoArguments(args, kwargs, method); err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		profile, err := s.resolveKernelHostModel(ctx, access, "")
		if err != nil {
			return nil, err
		}
		return profile.Model, nil
	case "host.submit_output":
		output, bullets, err := kernelHostSubmitOutputInput(args, kwargs)
		if err != nil {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", err.Error())
		}
		result, err := s.executeStructuredOutputTool(map[string]any{
			"value": output, "schema": map[string]any{}, "sessionId": access.Frame.ID,
		})
		if err != nil {
			return nil, err
		}
		result["status"] = "success"
		result["completion_bullets"] = bullets
		return result, nil
	default:
		return nil, kernelruntime.NewHostCallError("method_not_allowed", "host method is not allowed")
	}
}

type kernelHostQueryRequest struct {
	SQL    string
	Params []any
	Limit  int
	Scope  string
}

func kernelHostQueryInput(args []any, kwargs map[string]any) (kernelHostQueryRequest, error) {
	request := kernelHostQueryRequest{
		SQL:   strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 0, "sql"))),
		Limit: int(numberValue(kernelHostArgumentValue(args, kwargs, 2, "limit"))),
		Scope: strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 4, "scope"))),
	}
	rawParams := kernelHostArgumentValue(args, kwargs, 1, "params")
	if rawParams != nil {
		values, ok := rawParams.([]any)
		if !ok {
			return kernelHostQueryRequest{}, errors.New("host.query params must be an array")
		}
		request.Params = append([]any(nil), values...)
	}
	if boolValue(kernelHostArgumentValue(args, kwargs, 3, "df"), false) {
		return kernelHostQueryRequest{}, errors.New("host.query df=true is unavailable in the stdlib-only repl; use raw rows")
	}
	if request.SQL == "" {
		return kernelHostQueryRequest{}, errors.New("host.query SQL is required")
	}
	return request, nil
}

func (s *Server) kernelHostCredentialList(userID string) ([]map[string]any, error) {
	if s == nil || s.secretStore == nil {
		return []map[string]any{}, nil
	}
	secrets, err := s.secretStore.ListForUser(userID)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("storage_error", "credentials could not be listed")
	}
	items := make([]map[string]any, 0, len(secrets))
	for _, secret := range secrets {
		name := strings.TrimSpace(secret.Name)
		if name == "" {
			name = secret.ID
		}
		items = append(items, map[string]any{
			"name": name, "provider": secret.Provider, "credential_type": secret.CredentialType,
			"is_primary": false, "buckets": append([]string(nil), secret.Buckets...), "region": secret.Region,
		})
	}
	return items, nil
}

func (s *Server) kernelHostCredentialGet(userID, requested string) (any, error) {
	if s == nil || s.secretStore == nil {
		return nil, kernelruntime.NewHostCallError("credential_unavailable", "credential store is unavailable")
	}
	secrets, err := s.secretStore.ListForUser(userID)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("storage_error", "credential could not be read")
	}
	requested = strings.TrimSpace(requested)
	for _, secret := range secrets {
		if !strings.EqualFold(secret.ID, requested) && !strings.EqualFold(secret.Name, requested) && !strings.EqualFold(secret.Provider, requested) {
			continue
		}
		name := strings.TrimSpace(secret.Name)
		if name == "" {
			name = secret.ID
		}
		result := map[string]any{
			"name": name, "provider": secret.Provider, "credential_type": secret.CredentialType,
			"buckets": append([]string(nil), secret.Buckets...), "region": secret.Region,
		}
		if strings.TrimSpace(secret.Value) != "" {
			result["value"] = secret.Value
		}
		for key, value := range secret.Credentials {
			result[key] = value
		}
		return result, nil
	}
	return nil, kernelruntime.NewHostCallError("credential_unavailable", "credential was not found")
}

func (s *Server) kernelHostExecutionPeek(access workspace.KernelFrameAccess, execID string) (any, error) {
	if s != nil && s.kernelManager != nil {
		for _, stream := range s.kernelManager.ListExecStreams(access.Frame.ID) {
			if stream.ExecID == execID {
				return map[string]any{"status": stream.Status, "exec_id": stream.ExecID, "stdout": stream.Stdout, "tool_name": stream.ToolName, "started_at": stream.StartedAt}, nil
			}
		}
	}
	if s == nil || s.workspaceStore == nil {
		return nil, kernelruntime.NewHostCallError("not_found", "execution was not found")
	}
	record, found, err := s.workspaceStore.GetExecutionLog(access.Frame.ID, execID)
	if err != nil {
		return nil, kernelruntime.NewHostCallError("storage_error", "execution result could not be read")
	}
	if !found {
		return nil, kernelruntime.NewHostCallError("not_found", "execution was not found")
	}
	return map[string]any{"status": "done", "exec_id": record.ID, "stdout": record.Stdout, "stderr": record.Stderr, "exit_status": record.ExitStatus}, nil
}

func kernelHostFindingAddressInput(args []any, kwargs map[string]any) ([]string, string, error) {
	raw := kernelHostArgumentValue(args, kwargs, 0, "ids")
	ids := stringArrayValue(raw)
	if len(ids) == 0 || len(ids) > 200 {
		return nil, "", errors.New("host.findings.mark_addressed requires 1-200 ids")
	}
	note := strings.TrimSpace(stringValue(kernelHostArgumentValue(args, kwargs, 1, "note")))
	if note == "" || len([]rune(note)) > 2000 {
		return nil, "", errors.New("host.findings.mark_addressed requires a bounded note")
	}
	return ids, note, nil
}

func (s *Server) kernelHostLocalComputeStats(access workspace.KernelFrameAccess) map[string]any {
	machine := map[string]any{"cores": runtime.NumCPU(), "host_cores": runtime.NumCPU()}
	if file, err := os.Open("/proc/meminfo"); err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 {
				continue
			}
			value, _ := strconv.ParseFloat(fields[1], 64)
			switch strings.TrimSuffix(fields[0], ":") {
			case "MemTotal":
				machine["total_gb"] = value / 1024 / 1024
			case "MemAvailable":
				machine["available_gb"] = value / 1024 / 1024
			}
		}
	}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) >= 2 {
			machine["load1"], _ = strconv.ParseFloat(fields[0], 64)
			machine["load5"], _ = strconv.ParseFloat(fields[1], 64)
		}
	}
	kernels := any(nil)
	if s != nil && s.kernelManager != nil {
		streams := s.kernelManager.ListExecStreams(access.Frame.ID)
		kernels = map[string]any{"count": s.kernelManager.ActiveCount(), "busy": len(streams), "total_rss_gb": nil}
	}
	return map[string]any{"machine": machine, "kernels": kernels, "degraded": false, "sampled_at": time.Now().UTC().Format(time.RFC3339Nano)}
}

func kernelHostSubmitOutputInput(args []any, kwargs map[string]any) (map[string]any, []string, error) {
	output, ok := kernelHostArgumentValue(args, kwargs, 0, "output").(map[string]any)
	if !ok || output == nil {
		return nil, nil, errors.New("host.submit_output output must be an object")
	}
	bullets := stringArrayValue(kernelHostArgumentValue(args, kwargs, 1, "completion_bullets"))
	if len(bullets) > 4 {
		return nil, nil, errors.New("host.submit_output completion_bullets supports at most four items")
	}
	return copyMapAny(output), bullets, nil
}

func (s *Server) kernelHostManagedEndpointFreePort(userID string) (int, error) {
	if s == nil || s.workspaceStore == nil {
		return 0, kernelruntime.NewHostCallError("unavailable", "managed endpoint store is unavailable")
	}
	settings, err := s.workspaceStore.GetComputeBioNeMoSettings()
	if err != nil || !settings.Enabled {
		return 0, kernelruntime.NewHostCallError("not_connected", "managed model endpoints are not enabled")
	}
	endpoints, err := s.workspaceStore.ListManagedEndpoints(userID, "", false)
	if err != nil {
		return 0, kernelruntime.NewHostCallError("storage_error", "managed endpoints could not be listed")
	}
	used := map[int]bool{}
	for _, endpoint := range endpoints {
		used[endpoint.Port] = true
	}
	for attempt := 0; attempt < 256; attempt++ {
		offset, err := cryptorand.Int(cryptorand.Reader, big.NewInt(10000))
		if err != nil {
			return 0, kernelruntime.NewHostCallError("unavailable", "secure port allocation failed")
		}
		port := 20000 + int(offset.Int64())
		if used[port] {
			continue
		}
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}
	return 0, kernelruntime.NewHostCallError("unavailable", "no managed endpoint port is available")
}

func (s *Server) kernelHostRegisterManagedEndpoint(ctx context.Context, access workspace.KernelFrameAccess, input map[string]any) (any, error) {
	settings, err := s.workspaceStore.GetComputeBioNeMoSettings()
	if err != nil || !settings.Enabled {
		return nil, kernelruntime.NewHostCallError("not_connected", "managed model endpoints are not enabled")
	}
	name := strings.TrimSpace(stringValue(input["name"]))
	endpointURL := strings.TrimSpace(stringValue(input["url"]))
	skillName := strings.TrimSpace(stringValue(input["skill"]))
	credential := strings.TrimSpace(stringValue(input["credential"]))
	if credential == "" {
		credential = "NVIDIA_API_KEY"
	}
	if !inferenceProviderNamePattern.MatchString(name) || skillName == "" || len(skillName) > 128 || credential != "NVIDIA_API_KEY" {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "managed endpoint name, skill, or credential is invalid")
	}
	parsed, err := url.Parse(endpointURL)
	if err != nil || parsed.User != nil || parsed.Host == "" {
		return nil, kernelruntime.NewHostCallError("invalid_arguments", "managed endpoint URL is invalid")
	}
	hosted := parsed.Scheme == "https"
	startScript := strings.TrimSpace(stringValue(input["start"]))
	stopScript := strings.TrimSpace(stringValue(input["stop"]))
	livePath := strings.TrimSpace(stringValue(input["live"]))
	port := 0
	if hosted {
		if startScript != "" || stopScript != "" || livePath != "" {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "remote managed endpoints take no local lifecycle scripts")
		}
	} else {
		if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || startScript == "" || stopScript == "" || !strings.HasPrefix(livePath, "/") {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "local managed endpoints require a literal 127.0.0.1 HTTP URL plus start, stop, and live values")
		}
		port, err = strconv.Atoi(parsed.Port())
		if err != nil || port < 20000 || port > 29999 {
			return nil, kernelruntime.NewHostCallError("invalid_arguments", "local managed endpoint port must be in 20000-29999")
		}
	}
	registration := compute.ManagedEndpointRegistration{
		Name: name, URL: parsed.String(), Port: port, CredentialName: credential, SkillName: skillName,
		StartScript: startScript, StopScript: stopScript, LivePath: livePath,
	}
	hash := compute.ApprovedManagedEndpointHash(registration)
	existing, listErr := s.workspaceStore.ListManagedEndpoints(access.UserID, name, false)
	if listErr != nil {
		return nil, listErr
	}
	changed := true
	if len(existing) == 1 && strings.TrimPrefix(existing[0].ApprovedScriptHash, "sha256:") == hash {
		changed = false
	}
	if changed {
		approvalInput := copyMapAny(input)
		approvalInput["port"] = port
		if err := s.requireKernelCapabilityInstallApproval(ctx, access, "model-endpoint:"+name, methodModelEndpointRegister, "managed_model_endpoint", approvalInput, map[string]any{
			"title":       "Register managed model endpoint " + name,
			"description": "Review the exact endpoint, lifecycle scripts, readiness path, and credential name before registration.",
		}); err != nil {
			return nil, err
		}
	}
	serviceDir := ""
	state := "live"
	location := "remote"
	if !hosted {
		serviceDir = filepath.Join(s.fileRoot+"-managed-endpoints", name)
		if err := os.MkdirAll(serviceDir, 0o700); err != nil {
			return nil, kernelruntime.NewHostCallError("storage_error", "managed endpoint service directory could not be created")
		}
		state, location = "stopped", "local"
	}
	credentialCopy := credential
	if err := s.workspaceStore.UpsertManagedEndpoint(workspace.ManagedEndpoint{
		Name: name, URL: parsed.String(), Port: port, State: state, Location: location,
		SkillName: skillName, CredentialName: &credentialCopy, LivePath: livePath,
		StartScript: startScript, StopScript: stopScript, ApprovedScriptHash: hash,
		ServiceDir: serviceDir, RegisteredBy: access.UserID,
	}); err != nil {
		return nil, err
	}
	s.notifyComputeProviderProvisioner("inference")
	result := map[string]any{"registered": true, "changed": changed, "name": name, "url": parsed.String()}
	if hosted {
		result["hosted"] = true
	} else {
		result["port"] = port
		result["service_dir"] = serviceDir
	}
	return result, nil
}

const methodModelEndpointRegister = "host.model_endpoints.register"
