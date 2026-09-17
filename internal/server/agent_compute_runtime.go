package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	workspace "synon-go/internal/persistence/workspace"
)

const (
	listComputeToolName           = "list_compute"
	computeDetailsToolName        = "compute_details"
	askAboutComputeToolName       = "ask_about_compute"
	computeProviderToolName       = "compute_provider"
	sshComputeToolName            = "ssh"
	scpComputeToolName            = "scp"
	submitComputeJobToolName      = "submit_job"
	waitComputeJobToolName        = "wait_job"
	cancelComputeJobToolName      = "cancel_job"
	runningComputeJobsToolName    = "running_jobs"
	setComputeConcurrencyToolName = "set_concurrency_limit"
	maxComputeDetailsBytes        = 32 << 10
)

func publicComputeProviderName(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"byoc:", "ssh:"} {
		if strings.HasPrefix(name, prefix) {
			return strings.TrimPrefix(name, prefix)
		}
	}
	return name
}

func canonicalModalProviderParams(value map[string]any) (map[string]any, error) {
	if nested, found := value["modal"]; found {
		if len(value) != 1 {
			return nil, errors.New("provider_params cannot mix the retired modal wrapper with canonical fields")
		}
		params, ok := nested.(map[string]any)
		if !ok {
			return nil, errors.New("provider_params.modal must be an object")
		}
		return copyMapAny(params), nil
	}
	return copyMapAny(value), nil
}

func (s *Server) enforceAgentComputeProviderLimit(userID string, provider workspace.ComputeProvider) error {
	if provider.MaxConcurrentJobs == nil || *provider.MaxConcurrentJobs < 1 {
		return nil
	}
	live, err := s.workspaceStore.CountActiveComputeJobs(userID, provider.Name)
	if err != nil {
		return err
	}
	if live >= *provider.MaxConcurrentJobs {
		return fmt.Errorf("provider concurrency limit is full (live=%d limit=%d)", live, *provider.MaxConcurrentJobs)
	}
	return nil
}

func (s *Server) enforceAgentComputeCapacity(access workspace.KernelFrameAccess, provider workspace.ComputeProvider) error {
	status, err := s.kernelComputeStatus(access)
	if err != nil {
		return err
	}
	if limit := int(numberValue(status["limit"])); limit > 0 && int(numberValue(status["live"])) >= limit {
		return errors.New("session compute concurrency limit is full")
	}
	return s.enforceAgentComputeProviderLimit(access.UserID, provider)
}

func agentComputeToolSchemas() []agentruntime.ToolSchema {
	provider := map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	human := map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
	return []agentruntime.ToolSchema{
		{
			Name:        listComputeToolName,
			Description: "List compute targets enabled for this task. Re-call after the user changes compute settings.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"human_description": human,
			}},
		},
		{
			Name:        computeDetailsToolName,
			Description: "Read or update the durable provider-wide compute notes. Store only host configuration and verified operational guidance, never task results.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"provider":          provider,
					"mode":              map[string]any{"type": "string", "enum": []string{"read", "append", "replace", "set"}},
					"text":              map[string]any{"type": "string", "maxLength": maxComputeDetailsBytes},
					"old_text":          map[string]any{"type": "string", "maxLength": maxComputeDetailsBytes},
					"human_description": human,
				},
				"required": []string{"provider", "mode", "human_description"},
			},
		},
		{
			Name:        askAboutComputeToolName,
			Description: "Ask one material host-configuration question after probing what can be discovered automatically.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{"provider": provider, "question": map[string]any{"type": "string", "minLength": 4, "maxLength": 2000}, "human_description": human},
				"required":   []string{"provider", "question", "human_description"},
			},
		},
		{
			Name:        computeProviderToolName,
			Description: "Run Python in the selected provider's persistent authenticated SDK kernel. This is a separate confined environment-setup or inference surface, not the scientific Python kernel and not a remote job submission path.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"provider": provider,
					"code":     map[string]any{"type": "string", "minLength": 1, "maxLength": 100000},
				},
				"required": []string{"provider", "code"},
			},
		},
		{
			Name:        sshComputeToolName,
			Description: "Run one synchronous bounded command on an enabled SSH provider for inspection. Use submit_job for long work.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"provider": provider, "intent": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					"command":         map[string]any{"type": "string", "minLength": 1, "maxLength": 65536},
					"timeout_seconds": map[string]any{"type": "number", "minimum": 1, "maximum": 60},
					"login_shell":     map[string]any{"type": "boolean"}, "human_description": human,
				},
				"required": []string{"provider", "intent", "command", "human_description"},
			},
		},
		{
			Name:        scpComputeToolName,
			Description: "Transfer one bounded regular file to or from an enabled SSH provider. Local paths stay inside the task workspace; remote paths are absolute and shell-metacharacter free.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"provider": provider, "direction": map[string]any{"type": "string", "enum": []string{"up", "down"}},
					"local": map[string]any{"type": "string", "maxLength": 4096}, "remote": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					"human_description": human,
				},
				"required": []string{"provider", "direction", "remote", "human_description"},
			},
		},
		{
			Name:        submitComputeJobToolName,
			Description: "Submit one durable background command to an enabled SSH or BYOC compute provider. BYOC submissions stage bounded workspace inputs and always harvest out/, stdout, and stderr after terminal execution.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"provider": provider, "environment": map[string]any{"type": "string"},
					"provider_params": map[string]any{"type": "object"},
					"command":         map[string]any{"type": "string", "minLength": 1, "maxLength": 262144},
					"intent":          map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					"inputs": map[string]any{"type": "array", "maxItems": 256, "items": map[string]any{"anyOf": []any{
						map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
						map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
							"src": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
							"dst": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
						}, "required": []string{"src"}},
					}}},
					"outputs": map[string]any{"type": "array", "maxItems": 256, "items": map[string]any{"anyOf": []any{
						map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
						map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
							"glob":       map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
							"visibility": map[string]any{"type": "string", "enum": []string{"featured", "hidden"}},
						}, "required": []string{"glob"}},
					}}},
					"exclude": map[string]any{"type": "array", "maxItems": 256, "items": map[string]any{"type": "string"}},
					"transfer_limits": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
						"max_file_mb":  map[string]any{"type": "integer", "minimum": 1, "maximum": 20480},
						"max_total_mb": map[string]any{"type": "integer", "minimum": 1, "maximum": 20480},
					}},
					"timeout_seconds": map[string]any{"type": "number", "minimum": 1, "maximum": 86400},
					"scheduler":       map[string]any{"type": "string", "enum": []string{"slurm", "none"}},
					"tier":            map[string]any{"type": "object"}, "human_description": human,
					"env": map[string]any{"type": "object"},
				},
				"required": []string{"provider", "command", "intent", "human_description"},
			},
		},
		{
			Name:        waitComputeJobToolName,
			Description: "Wait up to a bounded timeout for one durable compute job and return its current or terminal status.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{"job_id": map[string]any{"type": "string"}, "provider": provider, "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 1800}, "human_description": human},
				"required":   []string{"job_id", "provider", "timeout_seconds", "human_description"},
			},
		},
		{
			Name:        cancelComputeJobToolName,
			Description: "Cancel one running compute job owned by this task.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{"job_id": map[string]any{"type": "string"}, "provider": provider, "human_description": human},
				"required":   []string{"job_id", "provider", "human_description"},
			},
		},
		{
			Name:        runningComputeJobsToolName,
			Description: "List nonterminal compute jobs in this task.",
			Parameters:  map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"human_description": human}},
		},
		{
			Name:        setComputeConcurrencyToolName,
			Description: "Set the maximum live compute jobs across this task tree.",
			Parameters: map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{"max_concurrent": map[string]any{"type": "integer", "minimum": 1, "maximum": 128}, "human_description": human},
				"required":   []string{"max_concurrent", "human_description"},
			},
		},
	}
}

func isAgentComputeTool(name string) bool {
	switch strings.TrimSpace(name) {
	case listComputeToolName, computeDetailsToolName, askAboutComputeToolName, sshComputeToolName, scpComputeToolName,
		computeProviderToolName,
		submitComputeJobToolName, waitComputeJobToolName, cancelComputeJobToolName,
		runningComputeJobsToolName, setComputeConcurrencyToolName:
		return true
	default:
		return false
	}
}

func (s *Server) executeAgentComputeTool(ctx context.Context, identity *agentKernelContext, call agentruntime.ToolCall, name string, input map[string]any) (any, error) {
	if s == nil || identity == nil || s.workspaceStore == nil {
		return nil, errors.New("compute runtime is unavailable")
	}
	access, err := s.validateKernelHostIdentity(ctx, identity.access)
	if err != nil {
		return nil, err
	}
	switch name {
	case listComputeToolName:
		return s.listAgentComputeProviders(access)
	case computeDetailsToolName:
		return s.executeAgentComputeDetails(access, input)
	case askAboutComputeToolName:
		return s.executeAgentAskAboutCompute(ctx, access, call, input)
	case computeProviderToolName:
		return s.executeAgentComputeProvider(ctx, identity, access, call, input)
	case sshComputeToolName:
		request, err := kernelComputeCommandInput([]any{input["provider"], input["command"], input["intent"], input["timeout_seconds"], input["login_shell"]}, nil)
		if err != nil {
			return nil, err
		}
		provider, found, err := s.kernelComputeProvider(access, request.Provider)
		if err != nil {
			return nil, err
		}
		if !found || provider.Family != "ssh" {
			return nil, errors.New("SSH compute provider is not enabled for this task")
		}
		return runKernelComputeSSHCommand(ctx, provider, request)
	case scpComputeToolName:
		return s.executeAgentSCP(ctx, identity, access, input)
	case submitComputeJobToolName:
		return s.submitAgentComputeJob(ctx, access, call, input, identity.workspaceDir)
	case waitComputeJobToolName:
		return s.waitAgentComputeJob(ctx, access, input)
	case cancelComputeJobToolName:
		return s.cancelAgentComputeJob(ctx, access, input)
	case runningComputeJobsToolName:
		return s.runningAgentComputeJobs(access)
	case setComputeConcurrencyToolName:
		return s.kernelSetComputeConcurrency(access, int(numberValue(input["max_concurrent"])))
	default:
		return nil, errors.New("unsupported compute tool")
	}
}

var agentComputeRemotePathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

func (s *Server) executeAgentSCP(ctx context.Context, identity *agentKernelContext, access workspace.KernelFrameAccess, input map[string]any) (any, error) {
	providerName := strings.TrimSpace(stringValue(input["provider"]))
	provider, found, err := s.kernelComputeProvider(access, providerName)
	if err != nil {
		return nil, err
	}
	if !found || provider.Family != "ssh" {
		return nil, errors.New("SCP requires an enabled SSH provider")
	}
	direction := strings.TrimSpace(stringValue(input["direction"]))
	remotePath := strings.TrimSpace(stringValue(input["remote"]))
	if !agentComputeRemotePathPattern.MatchString(remotePath) || strings.Contains(remotePath, "../") || strings.HasSuffix(remotePath, "/..") {
		return nil, errors.New("scp remote path must be absolute without traversal or shell metacharacters")
	}
	workspaceRoot, err := canonicalHostDirectory(identity.workspaceDir)
	if err != nil {
		return nil, errors.New("scp task workspace is unavailable")
	}
	localPath := strings.TrimSpace(stringValue(input["local"]))
	if direction == "down" && localPath == "" {
		localPath = filepath.Base(remotePath)
	}
	if localPath == "" || filepath.IsAbs(localPath) {
		return nil, errors.New("scp local path must be workspace-relative")
	}
	localPath = filepath.Clean(filepath.FromSlash(localPath))
	if localPath == "." || localPath == ".." || strings.HasPrefix(localPath, ".."+string(filepath.Separator)) {
		return nil, errors.New("scp local path escapes the task workspace")
	}
	absoluteLocal := filepath.Join(workspaceRoot, localPath)
	if direction == "up" {
		info, err := os.Stat(absoluteLocal)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("scp upload source must be an existing regular workspace file")
		}
		if info.Size() > 256<<20 {
			return nil, errors.New("scp transfer exceeds the 256 MiB limit")
		}
	} else if direction != "down" {
		return nil, errors.New("scp direction must be up or down")
	}
	alias := strings.TrimPrefix(provider.Name, "ssh:")
	if alias == "" || strings.HasPrefix(alias, "-") {
		return nil, errors.New("scp SSH provider alias is invalid")
	}
	arguments := []string{"-q", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}
	if user := strings.TrimSpace(stringValue(provider.SSHOverrides["user"])); user != "" {
		arguments = append(arguments, "-o", "User="+user)
	}
	if port := int(numberValue(provider.SSHOverrides["port"])); port > 0 {
		arguments = append(arguments, "-P", strconv.Itoa(port))
	}
	if identityFile := strings.TrimSpace(stringValue(provider.SSHOverrides["identityFile"])); identityFile != "" {
		arguments = append(arguments, "-i", identityFile)
	}
	remote := alias + ":" + remotePath
	if direction == "up" {
		arguments = append(arguments, "--", absoluteLocal, remote)
	} else {
		arguments = append(arguments, "--", remote, absoluteLocal)
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(runCtx, "scp", arguments...)
	stderr := &boundedComputeBuffer{limit: 64 << 10}
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("scp transfer timed out")
		}
		return nil, fmt.Errorf("scp transfer failed: %s", strings.TrimSpace(stderr.String()))
	}
	info, err := os.Stat(absoluteLocal)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<20 {
		if direction == "down" {
			_ = os.Remove(absoluteLocal)
		}
		return nil, errors.New("scp transferred file failed post-transfer validation")
	}
	return map[string]any{"direction": direction, "local_path": filepath.ToSlash(localPath), "remote_path": remotePath, "bytes": info.Size()}, nil
}

func (s *Server) listAgentComputeProviders(access workspace.KernelFrameAccess) (map[string]any, error) {
	providers, err := s.workspaceStore.ListComputeProviders(access.UserID)
	if err != nil {
		return nil, err
	}
	selected, configured, err := s.workspaceStore.SessionComputeProviderSelection(access.UserID, access.Frame.RootFrameID)
	if err != nil {
		return nil, err
	}
	selectedSet := map[string]bool{}
	for _, name := range selected {
		selectedSet[name] = true
	}
	items := make([]any, 0, len(providers))
	for _, provider := range providers {
		if provider.Family == "byoc" {
			providerID := strings.TrimPrefix(provider.Name, "byoc:")
			if _, authorityErr := s.agentComputeProviderAuthority(access, providerID, false); authorityErr == nil {
				items = append(items, map[string]any{
					"name": publicComputeProviderName(provider.Name), "family": provider.Family,
					"capabilities": []string{"provider_kernel", "durable_jobs", "remote_files"}, "job_surface_ready": true,
				})
			}
			continue
		}
		if !agentComputeProviderExecutable(provider) {
			continue
		}
		if configured && !selectedSet[provider.Name] {
			continue
		}
		if provider.Family == "byoc" {
			settings, found, settingsErr := s.workspaceStore.GetBYOCSettings(strings.TrimPrefix(provider.Name, "byoc:"), access.UserID)
			if settingsErr != nil {
				return nil, settingsErr
			}
			if !found || !settings.Enabled {
				continue
			}
		}
		item := map[string]any{"name": publicComputeProviderName(provider.Name), "family": provider.Family}
		if provider.Family == "infer" {
			item["url"] = provider.Endpoint
			item["skillName"] = provider.SkillName
		}
		items = append(items, item)
	}
	endpoints, err := s.workspaceStore.ListManagedEndpoints(access.UserID, "", false)
	if err != nil {
		return nil, err
	}
	for _, endpoint := range endpoints {
		if _, authorityErr := s.agentInferenceProviderAuthority(access, endpoint.Name, false); authorityErr != nil {
			continue
		}
		items = append(items, map[string]any{
			"name": endpoint.Name, "family": "infer", "state": endpoint.State,
			"capabilities": []string{"provider_kernel"},
		})
	}
	return map[string]any{"providers": items}, nil
}

func agentComputeProviderExecutable(provider workspace.ComputeProvider) bool {
	// The model runtime currently owns one verified remote execution path:
	// bounded SSH. BYOC and managed inference require the separate confined
	// provider-kernel boundary; do not advertise their settings records as
	// executable targets until that authority is installed and verified.
	return strings.TrimSpace(provider.Family) == "ssh"
}

func (s *Server) executeAgentComputeDetails(access workspace.KernelFrameAccess, input map[string]any) (any, error) {
	providerName := strings.TrimSpace(stringValue(input["provider"]))
	mode := strings.TrimSpace(stringValue(input["mode"]))
	provider, found, err := s.kernelComputeProvider(access, providerName)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("compute provider is not enabled for this task")
	}
	if mode == "read" {
		return map[string]any{"details": provider.DetailsMD, "provider": publicComputeProviderName(provider.Name), "family": provider.Family}, nil
	}
	text := stringValue(input["text"])
	current := provider.DetailsMD
	switch mode {
	case "append":
		if strings.TrimSpace(text) == "" {
			return nil, errors.New("compute_details append requires text")
		}
		if current != "" {
			current += "\n\n"
		}
		current += strings.TrimSpace(text)
	case "replace":
		oldText := stringValue(input["old_text"])
		if oldText == "" || strings.Count(current, oldText) != 1 {
			return nil, errors.New("compute_details replace old_text must match exactly once")
		}
		current = strings.Replace(current, oldText, text, 1)
	case "set":
		current = text
	default:
		return nil, errors.New("compute_details mode must be read, append, replace, or set")
	}
	if len([]byte(current)) > maxComputeDetailsBytes {
		return nil, errors.New("compute_details would exceed 32KB")
	}
	updated, err := s.workspaceStore.UpdateComputeProviderSettings(provider.Name, access.UserID, current, provider.MaxConcurrentJobs, provider.MaxTimeoutSec)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "provider": publicComputeProviderName(updated.Name), "details_rev": updated.DetailsRev}, nil
}

func (s *Server) executeAgentAskAboutCompute(ctx context.Context, access workspace.KernelFrameAccess, call agentruntime.ToolCall, input map[string]any) (any, error) {
	providerName := strings.TrimSpace(stringValue(input["provider"]))
	question := strings.TrimSpace(stringValue(input["question"]))
	provider, found, err := s.kernelComputeProvider(access, providerName)
	if err != nil || !found {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("compute provider is not enabled for this task")
	}
	return s.executeAgentAskUserQuestion(ctx, access.Frame.ID, call.ID, "ask_user", agentComputeAskUserInput(provider, question))
}

func agentComputeAskUserInput(provider workspace.ComputeProvider, question string) map[string]any {
	providerName := publicComputeProviderName(provider.Name)
	evidence := []any{"compute-provider:" + providerName}
	if sessionRunnerResponseLanguage(question) == "zh" {
		return map[string]any{
			"question": question,
			"header":   "计算环境",
			"options": []any{
				map[string]any{
					"label": "补充配置", "description": "在讨论栏提供当前问题所需的主机配置详情。",
					"pros": "可据此核验并使用已启用的计算环境。", "cons": "需要提供尚无法自动探测的信息。",
					"readiness": "已确认计算提供方 " + providerName + " 在当前任务中启用。", "readiness_status": "configured",
					"decision_evidence": evidence, "readiness_evidence": evidence, "selection_basis": "execution_readiness",
					"requirements":        "需要用户提供问题中所述的主机配置；此选择本身不启动计算。",
					"expected_outcome":    "获得可用于后续环境核验和执行的配置详情。",
					"selection_rationale": "推荐在后续工作依赖该计算环境时选择。", "recommended": true,
				},
				map[string]any{
					"label": "暂时跳过", "description": "不补充该提供方的专用配置，继续处理不依赖它的工作。",
					"pros": "可以继续不依赖该环境的步骤。", "cons": "依赖该环境的步骤可能保持不可执行。",
					"readiness": "可立即继续，但计算提供方 " + providerName + " 的现有配置不会改变。", "readiness_status": "configured",
					"decision_evidence": evidence, "readiness_evidence": evidence, "selection_basis": "execution_readiness",
					"requirements": "无需额外资源或数据传输。", "expected_outcome": "任务继续执行当前不依赖该提供方的部分。",
					"selection_rationale": "仅在当前结果不依赖该计算环境时选择。", "recommended": false,
				},
			},
		}
	}
	return map[string]any{
		"question": question,
		"header":   "Compute host",
		"options": []any{
			map[string]any{
				"label": "Provide details", "description": "Provide the host configuration requested in the discussion field.",
				"pros": "Enables verification and use of the configured compute environment.", "cons": "Requires information that could not be discovered automatically.",
				"readiness": "Compute provider " + providerName + " is enabled for this task.", "readiness_status": "configured",
				"decision_evidence": evidence, "readiness_evidence": evidence, "selection_basis": "execution_readiness",
				"requirements":        "The requested host configuration must be supplied; this choice does not start computation.",
				"expected_outcome":    "Configuration details suitable for subsequent environment verification and execution.",
				"selection_rationale": "Recommended when subsequent work depends on this compute environment.", "recommended": true,
			},
			map[string]any{
				"label": "Skip for now", "description": "Continue with work that does not require provider-specific guidance.",
				"pros": "Allows independent steps to continue.", "cons": "Steps that depend on this environment may remain unavailable.",
				"readiness": "Execution can continue, but the existing configuration for " + providerName + " remains unchanged.", "readiness_status": "configured",
				"decision_evidence": evidence, "readiness_evidence": evidence, "selection_basis": "execution_readiness",
				"requirements": "No additional resources or data transfer.", "expected_outcome": "The task continues only through work that does not depend on this provider.",
				"selection_rationale": "Choose only when the current result does not depend on this compute environment.", "recommended": false,
			},
		},
	}
}

func (s *Server) submitAgentComputeJob(ctx context.Context, access workspace.KernelFrameAccess, call agentruntime.ToolCall, input map[string]any, workspaceDirs ...string) (any, error) {
	providerName := strings.TrimSpace(stringValue(input["provider"]))
	provider, found, err := s.kernelComputeProvider(access, providerName)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("compute provider is not enabled for this task")
	}
	if provider.Family == "byoc" {
		workspaceDir := ""
		if len(workspaceDirs) > 0 {
			workspaceDir = strings.TrimSpace(workspaceDirs[0])
		}
		canonical := copyMapAny(input)
		canonical["provider"] = publicComputeProviderName(provider.Name)
		params, paramsErr := canonicalModalProviderParams(mapValue(input["provider_params"]))
		if paramsErr != nil {
			return nil, paramsErr
		}
		canonical["provider_params"] = params
		return s.submitAgentBYOCJob(ctx, access, call, canonical, workspaceDir)
	}
	if !found || provider.Family != "ssh" {
		return nil, errors.New("durable job submission currently requires an enabled SSH provider")
	}
	workspaceDir := ""
	if len(workspaceDirs) > 0 {
		workspaceDir = strings.TrimSpace(workspaceDirs[0])
	}
	return s.submitAgentSSHJob(ctx, access, call, input, workspaceDir, provider)
}

func (s *Server) waitAgentComputeJob(ctx context.Context, access workspace.KernelFrameAccess, input map[string]any) (any, error) {
	jobID := strings.TrimSpace(stringValue(input["job_id"]))
	provider := strings.TrimSpace(stringValue(input["provider"]))
	timeout := time.Duration(numberValue(input["timeout_seconds"])) * time.Second
	if timeout <= 0 || timeout > 30*time.Minute {
		return nil, errors.New("wait_job timeout_seconds must be 1-1800")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, found, err := s.workspaceStore.GetComputeJob(access.UserID, jobID)
		if err != nil {
			return nil, err
		}
		if !found || job.RootFrameID == nil || *job.RootFrameID != access.Frame.RootFrameID || !computeJobMatchesProvider(job, provider) {
			return nil, errors.New("compute job was not found in this task")
		}
		if isTerminalAgentComputeJobState(job.State) {
			return s.kernelComputeJobDetailedProjection(access.UserID, job), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return map[string]any{"status": "timeout", "job_id": jobID, "provider": provider}, nil
		case <-ticker.C:
		}
	}
}

func (s *Server) cancelAgentComputeJob(ctx context.Context, access workspace.KernelFrameAccess, input map[string]any) (any, error) {
	jobID := strings.TrimSpace(stringValue(input["job_id"]))
	provider := strings.TrimSpace(stringValue(input["provider"]))
	job, found, err := s.workspaceStore.GetComputeJob(access.UserID, jobID)
	if err != nil {
		return nil, err
	}
	if !found || job.RootFrameID == nil || *job.RootFrameID != access.Frame.RootFrameID || !computeJobMatchesProvider(job, provider) {
		return nil, errors.New("compute job was not found in this task")
	}
	if job.ProviderFamily == "byoc" {
		if isTerminalAgentComputeJobState(job.State) {
			return map[string]any{"cancelled": false, "job_id": jobID, "status": job.State}, nil
		}
		authority, err := s.agentComputeProviderAuthority(access, strings.TrimPrefix(job.Provider, "byoc:"), true)
		if err != nil {
			return nil, err
		}
		hardware := mapValue(job.HardwareDetails)
		sandboxIDs := []string{}
		if job.ExternalID != nil && strings.TrimSpace(*job.ExternalID) != "" {
			sandboxIDs = append(sandboxIDs, strings.TrimSpace(*job.ExternalID))
		} else if hint := strings.TrimSpace(stringValue(hardware["sandbox_hint"])); hint != "" {
			sandboxIDs = append(sandboxIDs, hint)
		} else {
			if s.providerOperationRunner == nil {
				return nil, errors.New("BYOC provider operation runtime is unavailable")
			}
			found, findErr := s.providerOperationRunner.RunProviderOperation(ctx, kernelruntime.ProviderOperationInput{
				Runtime: authority.Definition, Operation: "find_owned_submission",
				Request: map[string]any{
					"install_id": authority.Definition.InstallID, "job_id": job.JobID,
					"submission_id": strings.TrimSpace(stringValue(hardware["submission_id"])),
				},
			})
			if findErr != nil {
				return nil, findErr
			}
			sandboxIDs = stringArrayValue(found["sandbox_ids"])
			if len(sandboxIDs) > 2 {
				return nil, errors.New("BYOC cancellation found an invalid remote identity inventory")
			}
		}
		for _, sandboxID := range sandboxIDs {
			if err := s.terminateAgentBYOCSandbox(ctx, authority.Definition, sandboxID); err != nil {
				return nil, err
			}
		}
		updated, err := s.transitionAgentComputeJobTerminal(
			workspace.OwnedComputeJob{OwnerUserID: access.UserID, Job: job},
			workspace.ComputeJobFailed, "cancelled", time.Now().UTC(), nil,
		)
		if err != nil {
			return nil, err
		}
		if handleID := strings.TrimSpace(stringValue(mapValue(job.HardwareDetails)["handle_id"])); handleID != "" {
			s.releaseAgentComputeHandle(handleID, jobID, false)
			if s.runtimeStore != nil {
				_, _ = s.runtimeStore.Delete(computeProviderHandleNamespace, handleID)
			}
		}
		if stage := strings.TrimSpace(stringValue(hardware["staging_dir"])); stage != "" {
			s.removeAgentBYOCRecoveryStage(stage, job.JobID)
		}
		return map[string]any{"cancelled": true, "job_id": jobID, "status": updated.State}, nil
	}
	if job.ProviderFamily == "ssh" && boolValue(mapValue(job.HardwareDetails)["managed_ssh"], false) {
		s.computeRunsMu.Lock()
		cancel := s.computeRuns[jobID]
		s.computeRunsMu.Unlock()
		if cancel != nil {
			cancel()
			return map[string]any{"cancelled": true, "job_id": jobID}, nil
		}
		s.cancelManagedAgentSSHJob(ctx, workspace.OwnedComputeJob{OwnerUserID: access.UserID, Job: job})
		updated, found, err := s.workspaceStore.GetComputeJob(access.UserID, jobID)
		if err != nil || !found {
			return nil, errors.New("cancelled SSH job state could not be read")
		}
		return map[string]any{"cancelled": true, "job_id": jobID, "status": updated.State}, nil
	}
	s.computeRunsMu.Lock()
	cancel := s.computeRuns[jobID]
	s.computeRunsMu.Unlock()
	if cancel == nil {
		return map[string]any{"cancelled": false, "job_id": jobID, "status": job.State}, nil
	}
	cancel()
	return map[string]any{"cancelled": true, "job_id": jobID}, nil
}

func (s *Server) runningAgentComputeJobs(access workspace.KernelFrameAccess) (map[string]any, error) {
	jobs, err := s.workspaceStore.ListComputeJobs(access.UserID, access.Frame.ProjectID)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, job := range jobs {
		if job.RootFrameID == nil || *job.RootFrameID != access.Frame.RootFrameID || isTerminalAgentComputeJobState(job.State) {
			continue
		}
		items = append(items, kernelComputeJobProjection(job))
	}
	return map[string]any{"jobs": items, "count": len(items)}, nil
}

func isTerminalAgentComputeJobState(state string) bool {
	return state == workspace.ComputeJobDone || state == workspace.ComputeJobFailed ||
		state == workspace.ComputeJobTimedOut || state == workspace.ComputeJobOrphaned
}

func computeJobMatchesProvider(job workspace.ComputeJob, requested string) bool {
	requested = strings.TrimSpace(requested)
	return requested == job.Provider || publicComputeProviderName(requested) == publicComputeProviderName(job.Provider)
}
