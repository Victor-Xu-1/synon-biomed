package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/processsupervisor"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/software"
	"synon-go/internal/software/localcontainer"
)

const (
	manageEnvironmentsToolName = "manage_environments"
	managePackagesToolName     = "manage_packages"
)

type manageEnvironmentsInput struct {
	Mode                 string                                 `json:"mode"`
	HumanDescription     string                                 `json:"human_description"`
	Provider             string                                 `json:"provider,omitempty"`
	Implementation       string                                 `json:"implementation,omitempty"`
	Dependencies         []string                               `json:"dependencies,omitempty"`
	Name                 string                                 `json:"name,omitempty"`
	Language             string                                 `json:"language,omitempty"`
	PythonVersion        string                                 `json:"python_version,omitempty"`
	Packages             []string                               `json:"packages,omitempty"`
	Channels             []string                               `json:"channels,omitempty"`
	PipPhases            [][]string                             `json:"pip_phases,omitempty"`
	PipArgs              []string                               `json:"pip_args,omitempty"`
	PipFindLinks         []string                               `json:"pip_find_links,omitempty"`
	PipExtraIndexURLs    []string                               `json:"pip_extra_index_urls,omitempty"`
	ImportNames          []string                               `json:"import_names,omitempty"`
	Image                string                                 `json:"image,omitempty"`
	Network              string                                 `json:"network,omitempty"`
	SourcePath           string                                 `json:"source_path,omitempty"`
	VenvPath             string                                 `json:"venv_path,omitempty"`
	Create               bool                                   `json:"create,omitempty"`
	Extras               []string                               `json:"extras,omitempty"`
	Force                bool                                   `json:"force,omitempty"`
	Background           bool                                   `json:"background,omitempty"`
	ResourceRequirements managedEnvironmentResourceRequirements `json:"resource_requirements,omitempty"`
}

type managePackagesInput struct {
	Mode                 string                                 `json:"mode"`
	HumanDescription     string                                 `json:"human_description"`
	Implementation       string                                 `json:"implementation,omitempty"`
	Environment          string                                 `json:"environment"`
	Packages             []string                               `json:"packages"`
	Channels             []string                               `json:"channels,omitempty"`
	UsePip               bool                                   `json:"use_pip,omitempty"`
	ForkTo               string                                 `json:"fork_to,omitempty"`
	PipArgs              []string                               `json:"pip_args,omitempty"`
	PipFindLinks         []string                               `json:"pip_find_links,omitempty"`
	PipExtraIndexURLs    []string                               `json:"pip_extra_index_urls,omitempty"`
	Background           bool                                   `json:"background,omitempty"`
	ResourceRequirements managedEnvironmentResourceRequirements `json:"resource_requirements,omitempty"`
}

func applyRegisteredExecutionPackEnvironmentContract(
	request *manageEnvironmentsInput,
	pack sciencecapability.ExecutionPack,
) error {
	if request == nil || strings.TrimSpace(pack.ID) == "" || len(pack.Packages) == 0 {
		return errors.New("registered execution-pack environment contract is incomplete")
	}
	provider := strings.ToLower(strings.TrimSpace(pack.Provider))
	if provider != "" && provider != strings.ToLower(strings.TrimSpace(request.Provider)) {
		return fmt.Errorf("registered execution pack requires provider %s", provider)
	}
	condaPackages, pipPackages, err := registeredExecutionPackPackageSpecs(pack)
	if err != nil {
		return err
	}
	request.Language = strings.ToLower(strings.TrimSpace(pack.Language))
	request.PythonVersion = ""
	request.Packages = condaPackages
	request.PipPhases = nil
	if len(pipPackages) > 0 {
		request.PipPhases = [][]string{pipPackages}
	}
	request.PipArgs = nil
	request.PipFindLinks = nil
	request.PipExtraIndexURLs = nil
	request.Channels = append([]string(nil), pack.Channels...)
	request.ImportNames = append([]string(nil), pack.Imports...)
	return nil
}

func registeredExecutionPackPackageSpecs(
	pack sciencecapability.ExecutionPack,
) (condaPackages, pipPackages []string, resultErr error) {
	condaPackages = make([]string, 0, len(pack.Packages))
	pipPackages = make([]string, 0, len(pack.Packages))
	for _, requirement := range pack.Packages {
		spec := strings.TrimSpace(requirement.Spec)
		if spec == "" {
			return nil, nil, errors.New("registered execution pack contains an empty package requirement")
		}
		switch strings.ToLower(strings.TrimSpace(requirement.Manager)) {
		case "conda":
			condaPackages = append(condaPackages, spec)
		case "pip":
			pipPackages = append(pipPackages, spec)
		default:
			return nil, nil, fmt.Errorf("registered execution pack uses unsupported package manager %q", requirement.Manager)
		}
	}
	return condaPackages, pipPackages, nil
}

func registeredExecutionPackEnvironmentContractResult(
	request manageEnvironmentsInput,
	pack sciencecapability.ExecutionPack,
	err error,
) map[string]any {
	return map[string]any{
		"ok": false, "executed": false, "status": "execution_pack_environment_contract_mismatch",
		"implementation": request.Implementation, "execution_pack_id": pack.ID,
		"requested_provider": request.Provider, "required_provider": pack.Provider,
		"message":  "The environment request does not match the selected implementation's registered execution-pack contract: " + err.Error(),
		"recovery": "Retry preflight or create for the same selected implementation. The Harness will supply the registered provider, language, package managers, exact package pins, channels, and import witnesses; do not substitute an image, package alias, source string, or partial dependency list.",
	}
}

type managedEnvironmentAuthority interface {
	VerifyManagedEnvironmentImports(context.Context, string, []string) error
	ListManagedEnvironments(context.Context, kernelruntime.ManagedEnvironmentQuery) ([]kernelruntime.ManagedEnvironment, error)
	InspectManagedEnvironment(context.Context, string) (kernelruntime.ManagedEnvironment, bool, error)
	CreateManagedEnvironment(context.Context, kernelruntime.CreateManagedEnvironmentInput) (kernelruntime.ManagedEnvironment, error)
	RegisterManagedEnvironment(context.Context, kernelruntime.RegisterManagedEnvironmentInput) (kernelruntime.ManagedEnvironment, error)
	DeleteManagedEnvironment(context.Context, kernelruntime.DeleteManagedEnvironmentInput) error
	InstallManagedPackages(context.Context, kernelruntime.MutateManagedPackagesInput) (kernelruntime.ManagedEnvironment, error)
	UninstallManagedPackages(context.Context, kernelruntime.MutateManagedPackagesInput) (kernelruntime.ManagedEnvironment, error)
}

func agentEnvironmentManagementToolSchemas() []agentruntime.ToolSchema {
	return []agentruntime.ToolSchema{
		{
			Name:        manageEnvironmentsToolName,
			Description: "Manage one governed execution environment without choosing the scientific implementation for the user. mode=list inventories reusable local-conda and local-container environments plus the current CPU, memory, and accelerator snapshot. mode=preflight checks an exact proposal without mutation and identifies a compatible ready environment when one exists. mode=create publishes an immutable environment: local-conda uses packages/channels/pip phases; local-container resolves and pins one selected OCI image, verifies Docker and any required NVIDIA runtime, then prepares a reusable command host. The provider is explicit and never changes after an error. Before the first substantial compute or engine setup, preserve the user's exact implementation selection. A missing or conflicting selection returns a recoverable decision result without mutation. Container pulls and package installation have no implicit wall-clock deadline, publish observed progress, and must resume the same implementation rather than silently falling back. mode=delete and mode=register apply only to local-conda environments.",
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"mode":              map[string]any{"type": "string", "enum": []string{"list", "preflight", "create", "delete", "register"}},
					"human_description": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					"provider": map[string]any{
						"type": "string", "enum": []string{software.LocalProviderID, localcontainer.ProviderID}, "default": software.LocalProviderID,
						"description": "Execution provider. Keep the exact selected provider through retries; do not switch it because one setup attempt failed.",
					},
					"implementation": map[string]any{
						"type": "string", "minLength": 2, "maxLength": 160,
						"description": "Exact scientific engine, service, or executable being provisioned. After Ask User, copy the selected implementation value verbatim; put explanations only in human_description and never append a qualifier, colon suffix, alias, or method description. Required at runtime when a loaded Skill declares a scientific capability; do not name only a runtime, framework, or generic dependency bundle.",
					},
					"dependencies": map[string]any{
						"type": "array", "maxItems": 128, "items": map[string]any{"type": "string", "maxLength": 256},
					},
					"name":           map[string]any{"type": "string", "minLength": 1, "maxLength": 100},
					"language":       map[string]any{"type": "string", "enum": []string{"python", "r"}, "default": "python"},
					"python_version": map[string]any{"type": "string", "pattern": `^3\.[0-9]{1,2}(?:\.[0-9]{1,2})?$`},
					"packages": map[string]any{
						"type": "array", "minItems": 1, "maxItems": 256, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					},
					"channels": map[string]any{
						"type": "array", "maxItems": 32, "items": map[string]any{"type": "string", "maxLength": 128},
					},
					"pip_phases": map[string]any{
						"type": "array", "maxItems": 16,
						"description": "Ordered pip installations after the conda base. When the chosen conda authority lacks a build compatible with the observed machine and reviewed Skill evidence, preserve that compatibility target: keep conda minimal and install the exact compatible framework and extensions here from verified pip sources. Never lower to a known-incompatible accelerator or framework family merely to satisfy the conda solver.",
						"items":       map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}},
					},
					"pip_args": map[string]any{
						"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "enum": []string{"--no-build-isolation", "--no-deps", "--pre", "--upgrade", "--force-reinstall", "--no-cache-dir"}},
					},
					"pip_find_links": map[string]any{
						"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "format": "uri", "maxLength": 2048},
					},
					"pip_extra_index_urls": map[string]any{
						"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "format": "uri", "maxLength": 2048},
					},
					"import_names": map[string]any{
						"type": "array", "maxItems": 256, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					},
					"image": map[string]any{
						"type": "string", "minLength": 1, "maxLength": 1024,
						"description": "Official or reviewed OCI image reference for local-container. A tag may be supplied for first preparation; the runtime resolves and persists the immutable image content identity before execution.",
					},
					"network": map[string]any{
						"type": "string", "enum": []string{"egress", "none"}, "default": "egress",
					},
					"source_path": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					"venv_path":   map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
					"create":      map[string]any{"type": "boolean", "default": false},
					"extras": map[string]any{
						"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
					},
					"force":                 map[string]any{"type": "boolean", "default": false},
					"background":            map[string]any{"type": "boolean", "default": false},
					"resource_requirements": managedEnvironmentResourceRequirementsSchema(),
				},
				"required": []string{"mode", "human_description"},
				"oneOf": []any{
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "list"}}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "preflight"}, "provider": map[string]any{"const": software.LocalProviderID}}, "required": []string{"name", "packages", "resource_requirements"}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "preflight"}, "provider": map[string]any{"const": localcontainer.ProviderID}}, "required": []string{"provider", "image", "resource_requirements"}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "create"}, "provider": map[string]any{"const": software.LocalProviderID}}, "required": []string{"name", "packages"}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "create"}, "provider": map[string]any{"const": localcontainer.ProviderID}}, "required": []string{"provider", "image"}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "delete"}, "provider": map[string]any{"const": software.LocalProviderID}}, "required": []string{"name"}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "register"}, "provider": map[string]any{"const": software.LocalProviderID}}, "required": []string{"name", "source_path"}},
				},
			},
		},
		{
			Name:        managePackagesToolName,
			Description: "List packages or inspect a proposed change with mode=preflight. When the package change installs or changes the scientific engine, implementation must name that actual engine or service rather than a framework or generic dependency bundle. A small supporting parser, validator, or file-format dependency added to an existing environment may omit implementation; that omission never authorizes treating the support package as the scientific engine. The preflight result carries typed first-setup/reuse state. Before first installation of a substantial scientific implementation, obtain the user's exact implementation selection after preflighting viable alternatives; an explicitly user-named implementation or compatible ready environment can continue without another question. mode=install returns a recoverable decision result without mutating when a supplied implementation identity or selection conflicts, and otherwise performs the same resource preflight. Mutations publish a new immutable generation atomically and never modify the active generation in place. Prefixing every package with pip:: selects the pip authority automatically; mixed conda and pip mutations must be issued as separate coherent operations. Use pip_find_links or pip_extra_index_urls for source-specific binary wheels instead of embedding command-line flags in package strings. Use fork_to when a different dependency set requires a dedicated environment.",
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"mode":              map[string]any{"type": "string", "enum": []string{"preflight", "install", "uninstall", "list"}},
					"human_description": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					"implementation": map[string]any{
						"type": "string", "minLength": 2, "maxLength": 160,
						"description": "Exact scientific engine, service, or executable whose packages are being changed. After Ask User, copy the selected implementation value verbatim; put explanations only in human_description and never append a qualifier, colon suffix, alias, or method description. Required at runtime when a loaded Skill declares a scientific capability.",
					},
					"environment": map[string]any{"type": "string", "minLength": 1, "maxLength": 100},
					"packages": map[string]any{
						"type": "array", "minItems": 1, "maxItems": 256,
						"items": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
					},
					"channels": map[string]any{
						"type": "array", "maxItems": 32, "items": map[string]any{"type": "string", "maxLength": 128},
					},
					"use_pip": map[string]any{"type": "boolean", "default": false},
					"fork_to": map[string]any{"type": "string", "minLength": 1, "maxLength": 100},
					"pip_args": map[string]any{
						"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "enum": []string{"--no-build-isolation", "--no-deps", "--pre", "--upgrade", "--force-reinstall", "--no-cache-dir"}},
					},
					"pip_find_links": map[string]any{
						"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "format": "uri", "maxLength": 2048},
					},
					"pip_extra_index_urls": map[string]any{
						"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "format": "uri", "maxLength": 2048},
					},
					"background":            map[string]any{"type": "boolean", "default": false},
					"resource_requirements": managedEnvironmentResourceRequirementsSchema(),
				},
				"required": []string{"mode", "human_description", "environment"},
				"oneOf": []any{
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "list"}}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "preflight"}}, "required": []string{"packages", "resource_requirements"}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "install"}}, "required": []string{"packages"}},
					map[string]any{"properties": map[string]any{"mode": map[string]any{"const": "uninstall"}}, "required": []string{"packages"}},
				},
			},
		},
	}
}

func isAgentEnvironmentManagementTool(name string) bool {
	name = strings.TrimSpace(name)
	return name == manageEnvironmentsToolName || name == managePackagesToolName
}

func (s *Server) executeAgentEnvironmentManagementTool(
	ctx context.Context,
	identity *agentKernelContext,
	call agentruntime.ToolCall,
	name string,
	input map[string]any,
) (any, error) {
	if s == nil || s.kernelManager == nil || identity == nil {
		return nil, errors.New("managed environment runtime is unavailable")
	}
	return s.executeAgentEnvironmentManagementToolWithAuthority(ctx, identity, call, name, input, s.kernelManager)
}

func (s *Server) executeAgentEnvironmentManagementToolWithAuthority(
	ctx context.Context,
	identity *agentKernelContext,
	call agentruntime.ToolCall,
	name string,
	input map[string]any,
	authority managedEnvironmentAuthority,
) (result any, resultErr error) {
	defer func() {
		if resultErr == nil {
			result = s.bindRegistrySelectedImplementationReceipt(ctx, result)
		}
	}()
	if s == nil || authority == nil || identity == nil {
		return nil, errors.New("managed environment runtime is unavailable")
	}
	access, err := s.validateKernelHostIdentity(ctx, identity.access)
	if err != nil {
		return nil, err
	}
	switch name {
	case manageEnvironmentsToolName:
		var request manageEnvironmentsInput
		if err := decodeStrictToolInput(input, &request); err != nil {
			return nil, err
		}
		request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
		if request.Provider == "" {
			request.Provider = software.LocalProviderID
		}
		mode := strings.ToLower(strings.TrimSpace(request.Mode))
		if mode == "preflight" || mode == "create" {
			request.Implementation = s.canonicalManagedEnvironmentImplementation(ctx, request.Implementation)
			if pack, bound := s.registeredManagedEnvironmentExecutionPack(ctx, request.Implementation); bound {
				if err := applyRegisteredExecutionPackEnvironmentContract(&request, pack); err != nil {
					return registeredExecutionPackEnvironmentContractResult(request, pack, err), nil
				}
			}
		}
		if request.Provider == localcontainer.ProviderID {
			if err := validateManagedEnvironmentHumanDescription(request.HumanDescription); err != nil {
				return nil, err
			}
			return s.executeAgentContainerEnvironmentTool(ctx, identity, access, call, request)
		}
		if request.Provider != software.LocalProviderID {
			return nil, errors.New("manage_environments provider is unavailable")
		}
		// Egress is the native conda behavior and also the public schema default.
		// Read-only inventory/preflight never applies a network policy. Do not
		// reject either as a container operation, but never promise isolation
		// for a mutating conda operation or reinterpret an OCI image as packages.
		network := strings.ToLower(strings.TrimSpace(request.Network))
		readOnly := request.Mode == "list" || request.Mode == "preflight"
		if strings.TrimSpace(request.Image) != "" || (!readOnly && network != "" && network != "egress") {
			return nil, errors.New("container image and network require provider local-container")
		}
		if sources := unprefixedManagedSourceReferences(request.Packages); len(sources) != 0 {
			return managedSourcePackageContractResult(name, sources), nil
		}
		request.Packages, request.PipFindLinks, err = normalizeManagedEmbeddedPipFindLinks(request.Packages, request.PipFindLinks)
		if err != nil {
			return managedPipSourceOptionContractResult(name, err), nil
		}
		for index := range request.PipPhases {
			request.PipPhases[index], request.PipFindLinks, err = normalizeManagedEmbeddedPipFindLinks(request.PipPhases[index], request.PipFindLinks)
			if err != nil {
				return managedPipSourceOptionContractResult(name, err), nil
			}
		}
		request.PipFindLinks, request.PipExtraIndexURLs = normalizeManagedPipSourceKinds(
			request.PipFindLinks, request.PipExtraIndexURLs,
		)
		request.Packages = normalizeAgentManagedEnvironmentPackageAuthorities(request.Packages)
		request.PythonVersion, err = managedEnvironmentCanonicalPythonVersion(
			request.PythonVersion, request.Packages,
		)
		if err != nil {
			return nil, err
		}
		preflightPackages := managedEnvironmentPreflightPackages(request.Packages, request.PipPhases)
		if err := validateManagedEnvironmentHumanDescription(request.HumanDescription); err != nil {
			return nil, err
		}
		if request.Mode == "preflight" {
			if decision, required := s.managedEnvironmentImplementationDecision(ctx, request.Implementation, false); required {
				return decision, nil
			}
			return s.executeManagedEnvironmentResourcePreflight(ctx, identity, authority, managedEnvironmentPreflightSpec{
				Operation: "create", Implementation: request.Implementation, Name: request.Name, Language: request.Language,
				PythonVersion: request.PythonVersion, Packages: preflightPackages, Channels: request.Channels,
				Imports:              request.ImportNames,
				PackageSourceURLs:    managedPipPackageSourceURLs(request.PipFindLinks, request.PipExtraIndexURLs),
				ResourceRequirements: request.ResourceRequirements,
			})
		}
		if request.Mode == "list" {
			environments, err := authority.ListManagedEnvironments(ctx, kernelruntime.ManagedEnvironmentQuery{
				Language: strings.TrimSpace(request.Language), Dependencies: request.Dependencies,
				IncludePackages: len(request.Dependencies) > 0,
			})
			if err != nil {
				return nil, err
			}
			summaries := make([]map[string]any, 0, len(environments))
			for _, environment := range environments {
				summaries = append(summaries, managedEnvironmentInventorySummary(environment))
			}
			result := map[string]any{
				"mode": "list", "environments": summaries, "count": len(summaries),
				"machine": s.managedEnvironmentMachineSnapshot(ctx, identity),
			}
			if containerManager, managerErr := s.localContainerEnvironmentManager(); managerErr == nil {
				containers, listErr := containerManager.List(ctx)
				if listErr != nil {
					result["container_status"] = "unavailable"
					result["container_diagnostic"] = boundedManagedEnvironmentError(listErr)
				} else {
					result["container_environments"] = containers
					result["container_count"] = len(containers)
				}
			} else {
				result["container_status"] = "unavailable"
				result["container_diagnostic"] = boundedManagedEnvironmentError(managerErr)
			}
			return result, nil
		}
		if request.Mode == "delete" {
			if strings.TrimSpace(request.Name) == "" {
				return nil, errors.New("manage_environments delete requires name")
			}
			operationID, err := stableServerOperationID(
				"environment",
				access.UserID+"\x00"+access.Frame.RootFrameID+"\x00"+name,
				map[string]any{"tool": name, "mode": request.Mode, "name": strings.TrimSpace(request.Name), "call_id": call.ID},
			)
			if err != nil {
				return nil, err
			}
			operation := managedOperationRequest{Kind: "delete", DeleteName: strings.TrimSpace(request.Name)}
			return s.executeManagedEnvironmentOperation(ctx, access, call, name, request.Background, map[string]any{"operation_id": operationID}, operation, authority)
		}
		if request.Mode == "register" {
			if strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.SourcePath) == "" {
				return nil, errors.New("manage_environments register requires name and source_path")
			}
			if err := s.requireManagedEnvironmentRegistrationGrant(access.UserID, request.SourcePath, request.VenvPath); err != nil {
				return nil, err
			}
			language := strings.ToLower(strings.TrimSpace(request.Language))
			if language == "" {
				language = "python"
			}
			operationID, err := stableServerOperationID(
				"environment",
				access.UserID+"\x00"+access.Frame.RootFrameID+"\x00"+name,
				stableAuthorityInput(map[string]any{
					"tool": name, "mode": request.Mode, "name": request.Name, "language": language,
					"source_path": request.SourcePath, "venv_path": request.VenvPath,
					"create": request.Create, "extras": request.Extras, "force": request.Force,
				}),
			)
			if err != nil {
				return nil, err
			}
			operation := managedOperationRequest{Kind: "register", Register: &kernelruntime.RegisterManagedEnvironmentInput{
				Name: request.Name, Language: language, SourcePath: request.SourcePath, VenvPath: request.VenvPath,
				Create: request.Create, Extras: request.Extras, Force: request.Force, OperationID: operationID,
			}}
			return s.executeManagedEnvironmentOperation(ctx, access, call, name, request.Background, map[string]any{"operation_id": operationID}, operation, authority)
		}
		if request.Mode != "create" || strings.TrimSpace(request.Name) == "" || len(request.Packages) == 0 {
			return nil, errors.New("manage_environments create request is incomplete")
		}
		language := strings.ToLower(strings.TrimSpace(request.Language))
		if language == "" {
			language = "python"
		}
		if language != "python" && language != "r" {
			return nil, errors.New("manage_environments language must be python or r")
		}
		hasPipStages := len(request.PipPhases) != 0
		for _, requirement := range request.Packages {
			hasPipStages = hasPipStages || strings.HasPrefix(requirement, "pip::")
		}
		if language == "r" && !hasPipStages && strings.TrimSpace(request.PythonVersion) != "" {
			return nil, errors.New("manage_environments python_version is valid only for Python environments")
		}
		request.Channels = canonicalManagedToolChannels(request.Channels)
		if decision, required := s.managedEnvironmentImplementationDecision(ctx, request.Implementation, false); required {
			return decision, nil
		}
		preflight, err := s.executeManagedEnvironmentResourcePreflight(ctx, identity, authority, managedEnvironmentPreflightSpec{
			Operation: "create", Implementation: request.Implementation, Name: request.Name, Language: language,
			PythonVersion: request.PythonVersion, Packages: preflightPackages, Channels: request.Channels,
			Imports:              request.ImportNames,
			PackageSourceURLs:    managedPipPackageSourceURLs(request.PipFindLinks, request.PipExtraIndexURLs),
			ResourceRequirements: defaultManagedEnvironmentCreateRequirements(request.ResourceRequirements, len(request.Packages)),
		})
		if err != nil {
			return nil, err
		}
		// A compatible cached environment is still an execution route for the
		// requested scientific implementation. Reuse must obey the same latest
		// AskUser authority as creating a new generation; otherwise an older
		// implementation can bypass the user's current choice merely by already
		// existing on disk.
		if decision, required := s.managedEnvironmentImplementationDecision(ctx, request.Implementation, true); required {
			return decision, nil
		}
		compatible := skillStringValues(preflight["compatible_environments"])
		if len(compatible) > 0 {
			selected := compatible[0]
			for _, candidate := range compatible {
				if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(request.Name)) {
					selected = candidate
					break
				}
			}
			environment, found, inspectErr := authority.InspectManagedEnvironment(ctx, selected)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if !found || strings.TrimSpace(environment.Name) == "" || environment.Status != "ready" {
				return nil, errors.New("compatible managed environment became unavailable during preflight")
			}
			if err := authority.VerifyManagedEnvironmentImports(ctx, environment.Name, request.ImportNames); err != nil {
				return nil, err
			}
			return map[string]any{
				"tool": manageEnvironmentsToolName, "status": "completed", "mode": "reuse",
				"environment":    managedEnvironmentInventorySummary(environment),
				"requested_name": request.Name, "requested_packages": append([]string(nil), request.Packages...),
				"compatible_environments": compatible, "preflight": preflight,
			}, nil
		}
		if requirement, evidenceErr := s.managedEnvironmentSetupEvidenceRequirement(ctx, request.Implementation); evidenceErr != nil {
			return nil, evidenceErr
		} else if requirement != nil {
			requirement["preflight"] = preflight
			return requirement, nil
		}
		if !compatibilityPlanBool(preflight["feasible"]) {
			preflight["status"] = "resource_choice_required"
			preflight["executed"] = false
			preflight["decision_required"] = true
			preflight["message"] = "The proposed environment does not fit the current machine snapshot. Select a lighter dependency set, an admitted remote route, or ask the user when the material tradeoff is unresolved."
			return preflight, nil
		}
		operationID, err := stableServerOperationID(
			"environment",
			access.UserID+"\x00"+access.Frame.RootFrameID+"\x00"+name,
			stableAuthorityInput(map[string]any{
				"tool": name, "mode": request.Mode, "name": request.Name, "language": language,
				"python_version": request.PythonVersion, "packages": request.Packages, "channels": request.Channels,
				"pip_phases": request.PipPhases, "pip_args": request.PipArgs, "pip_find_links": request.PipFindLinks,
				"pip_extra_index_urls": request.PipExtraIndexURLs, "import_names": request.ImportNames,
				"resource_requirements": request.ResourceRequirements, "implementation": request.Implementation,
			}),
		)
		if err != nil {
			return nil, err
		}
		requiredAccelerator := managedEnvironmentPreflightRequiredAccelerator(preflight, request.ResourceRequirements.Accelerator)
		operation := managedOperationRequest{Kind: "create", Create: &kernelruntime.CreateManagedEnvironmentInput{
			Name: request.Name, Language: language, PythonVersion: request.PythonVersion,
			Packages: request.Packages, Channels: request.Channels, PipPhases: request.PipPhases,
			PipArgs: request.PipArgs, PipFindLinks: request.PipFindLinks,
			PipExtraIndexURLs: request.PipExtraIndexURLs, ImportNames: request.ImportNames,
			RequiredAccelerator: requiredAccelerator, OperationID: operationID,
		}}
		return s.executeManagedEnvironmentOperation(ctx, access, call, name, request.Background, map[string]any{
			"mode": "create", "implementation": strings.TrimSpace(request.Implementation),
			"requested_packages": append([]string(nil), request.Packages...), "preflight": preflight,
			"operation_id": operationID,
		}, operation, authority)
	case managePackagesToolName:
		var request managePackagesInput
		if err := decodeStrictToolInput(input, &request); err != nil {
			return nil, err
		}
		if sources := unprefixedManagedSourceReferences(request.Packages); len(sources) != 0 {
			return managedSourcePackageContractResult(name, sources), nil
		}
		request.Packages, request.PipFindLinks, err = normalizeManagedEmbeddedPipFindLinks(request.Packages, request.PipFindLinks)
		if err != nil {
			return managedPipSourceOptionContractResult(name, err), nil
		}
		request.PipFindLinks, request.PipExtraIndexURLs = normalizeManagedPipSourceKinds(
			request.PipFindLinks, request.PipExtraIndexURLs,
		)
		request.Implementation = s.canonicalManagedEnvironmentImplementation(ctx, request.Implementation)
		request.Implementation = s.canonicalManagedPackageImplementation(ctx, request.Implementation)
		if err := validateManagedEnvironmentHumanDescription(request.HumanDescription); err != nil {
			return nil, err
		}
		request.Packages, request.UsePip, err = normalizeManagedPackageMutationAuthority(request.Packages, request.UsePip)
		if err != nil {
			return map[string]any{
				"tool": name, "ok": false, "status": "package_authority_split_required", "executed": false,
				"message":  "Conda and pip package changes use separate immutable operations.",
				"recovery": "Submit the unprefixed conda requirements first, then submit the pip:: requirements as a second package operation against the resulting environment generation.",
			}, nil
		}
		if strings.TrimSpace(request.Environment) == "" {
			return nil, errors.New("manage_packages requires environment")
		}
		if localcontainer.IsEnvironmentName(request.Environment) {
			return map[string]any{
				"tool": managePackagesToolName, "ok": true, "executed": false,
				"status": "container_image_replacement_required", "environment": request.Environment,
				"message":  "Container environments are immutable image generations and cannot be changed by a package mutation.",
				"recovery": "Preserve the selected scientific implementation. Prepare a reviewed successor image through manage_environments with provider local-container, then run and validate the same workload in the returned environment. Do not mutate a live container or fall back to a different implementation.",
			}, nil
		}
		if request.Mode == "preflight" {
			if strings.TrimSpace(request.Implementation) != "" {
				if decision, required := s.managedEnvironmentImplementationDecision(ctx, request.Implementation, false); required {
					return decision, nil
				}
			}
			return s.executeManagedEnvironmentResourcePreflight(ctx, identity, authority, managedEnvironmentPreflightSpec{
				Operation: "install", Implementation: request.Implementation,
				Environment: request.Environment, ForkTo: request.ForkTo,
				Language: "python", Packages: request.Packages, Channels: request.Channels, UsePip: request.UsePip,
				PackageSourceURLs:    managedPipPackageSourceURLs(request.PipFindLinks, request.PipExtraIndexURLs),
				ResourceRequirements: request.ResourceRequirements,
			})
		}
		if request.Mode == "list" {
			environment, found, err := authority.InspectManagedEnvironment(ctx, request.Environment)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, errors.New("managed environment does not exist")
			}
			return map[string]any{"mode": "list", "environment": environment, "package_count": len(environment.Packages)}, nil
		}
		if (request.Mode != "install" && request.Mode != "uninstall") || len(request.Packages) == 0 {
			return nil, errors.New("manage_packages mode must be install, uninstall, or list")
		}
		request.Channels = canonicalManagedToolChannels(request.Channels)
		var preflight map[string]any
		if request.Mode == "install" {
			hasImplementation := strings.TrimSpace(request.Implementation) != ""
			if hasImplementation {
				if decision, required := s.managedEnvironmentImplementationDecision(ctx, request.Implementation, false); required {
					return decision, nil
				}
			}
			preflight, err = s.executeManagedEnvironmentResourcePreflight(ctx, identity, authority, managedEnvironmentPreflightSpec{
				Operation: "install", Implementation: request.Implementation,
				Environment: request.Environment, ForkTo: request.ForkTo,
				Language: "python", Packages: request.Packages, Channels: request.Channels, UsePip: request.UsePip,
				PackageSourceURLs: managedPipPackageSourceURLs(request.PipFindLinks, request.PipExtraIndexURLs),
				ResourceRequirements: defaultManagedEnvironmentCreateRequirements(
					request.ResourceRequirements, len(request.Packages),
				),
			})
			if err != nil {
				return nil, err
			}
			// Reusing an immutable package-compatible environment is still a
			// scientific implementation decision. Apply exact user-choice
			// continuity before either reuse or mutation can return success.
			if hasImplementation {
				if decision, required := s.managedEnvironmentImplementationDecision(ctx, request.Implementation, true); required {
					return decision, nil
				}
			}
			if run, ok := transcriptRunnerChatRunFromContext(ctx); ok {
				if invalid, found := run.managedEnvironmentInvalidationSnapshot(request.Environment); found {
					return map[string]any{
						"tool": managePackagesToolName, "ok": true, "executed": false, "recoverable": true,
						"status":      "environment_generation_replacement_required",
						"environment": request.Environment, "invalid_generation": invalid.Generation,
						"incompatibility_code": invalid.Code, "preflight": preflight,
						"recovery": "The selected environment generation has already failed a real runtime compatibility witness in this task. Preserve the scientific implementation, inputs, and completed artifacts; do not install more packages into or fork from that generation. Use manage_environments preflight/create to publish one coherent replacement generation from verified sources, then validate imports and the real workload before continuing.",
					}, nil
				}
			}
			compatible := skillStringValues(preflight["compatible_environments"])
			if managedPackageMutationRequiresExecution(request) {
				compatible = nil
			}
			if len(compatible) > 0 {
				selected := compatible[0]
				for _, candidate := range compatible {
					if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(request.Environment)) {
						selected = candidate
						break
					}
				}
				environment, found, inspectErr := authority.InspectManagedEnvironment(ctx, selected)
				if inspectErr != nil {
					return nil, inspectErr
				}
				if !found || strings.TrimSpace(environment.Name) == "" || environment.Status != "ready" {
					return nil, errors.New("compatible managed environment became unavailable during package preflight")
				}
				return map[string]any{
					"tool": managePackagesToolName, "status": "completed", "mode": "reuse",
					"environment":             managedEnvironmentInventorySummary(environment),
					"requested_environment":   request.Environment,
					"requested_packages":      append([]string(nil), request.Packages...),
					"compatible_environments": compatible, "preflight": preflight,
				}, nil
			}
			if requirement, evidenceErr := s.managedEnvironmentSetupEvidenceRequirement(ctx, request.Implementation); evidenceErr != nil {
				return nil, evidenceErr
			} else if requirement != nil {
				requirement["tool"] = managePackagesToolName
				requirement["preflight"] = preflight
				return requirement, nil
			}
			requiredAccelerator := managedEnvironmentPreflightRequiredAccelerator(
				preflight, request.ResourceRequirements.Accelerator,
			)
			if conflicts := managedPipAcceleratorSourceConflicts(
				managedPipPackageSourceURLs(request.PipFindLinks, request.PipExtraIndexURLs),
				requiredAccelerator,
			); len(conflicts) > 0 {
				return map[string]any{
					"tool": managePackagesToolName, "ok": true, "executed": false,
					"status": "accelerator_package_source_conflict", "feasible": false,
					"implementation": request.Implementation, "conflicting_sources": conflicts,
					"message":   "The selected implementation requires an accelerator, but the proposed package source is explicitly CPU-only.",
					"recovery":  "Keep the selected implementation. Inspect the exact installed framework version and accelerator build, then use the current official binary matrix for that same accelerator family. Do not fall through to a CPU wheel or source build when the reviewed workflow requires GPU execution.",
					"preflight": preflight,
				}, nil
			}
			if !compatibilityPlanBool(preflight["feasible"]) {
				preflight["status"] = "resource_choice_required"
				preflight["executed"] = false
				preflight["decision_required"] = true
				preflight["message"] = "The proposed package change does not fit the current machine snapshot. Select a lighter dependency set, an admitted remote route, or ask the user when the material tradeoff is unresolved."
				return preflight, nil
			}
		}
		operationID, err := stableServerOperationID(
			"environment",
			access.UserID+"\x00"+access.Frame.RootFrameID+"\x00"+name,
			stableAuthorityInput(map[string]any{
				"tool": name, "mode": request.Mode, "environment": request.Environment,
				"packages": request.Packages, "channels": request.Channels, "use_pip": request.UsePip,
				"fork_to": request.ForkTo, "pip_args": request.PipArgs, "pip_find_links": request.PipFindLinks,
				"pip_extra_index_urls": request.PipExtraIndexURLs,
				"required_accelerator": request.ResourceRequirements.Accelerator, "implementation": request.Implementation,
			}),
		)
		if err != nil {
			return nil, err
		}
		requiredAccelerator := managedEnvironmentPreflightRequiredAccelerator(preflight, request.ResourceRequirements.Accelerator)
		operation := managedOperationRequest{Kind: request.Mode, Packages: &kernelruntime.MutateManagedPackagesInput{
			Environment: request.Environment, Packages: request.Packages, Channels: request.Channels,
			UsePip: request.UsePip, ForkTo: request.ForkTo, PipArgs: request.PipArgs,
			PipFindLinks: request.PipFindLinks, PipExtraIndexURLs: request.PipExtraIndexURLs,
			RequiredAccelerator: requiredAccelerator, OperationID: operationID,
		}}
		return s.executeManagedEnvironmentOperation(ctx, access, call, name, request.Background, map[string]any{
			"mode": request.Mode, "implementation": strings.TrimSpace(request.Implementation),
			"requested_packages": append([]string(nil), request.Packages...), "preflight": preflight,
			"operation_id": operationID,
		}, operation, authority)
	default:
		return nil, errors.New("unsupported managed environment tool")
	}
}

func managedEnvironmentPreflightRequiredAccelerator(preflight map[string]any, fallback string) string {
	if requirements := mapValue(preflight["requirements"]); requirements != nil {
		if value := strings.TrimSpace(stringValue(requirements["accelerator"])); value != "" {
			return value
		}
	}
	return strings.TrimSpace(fallback)
}

func managedPipPackageSourceURLs(findLinks, extraIndexes []string) []string {
	result := append([]string(nil), findLinks...)
	return append(result, extraIndexes...)
}

func managedPipAcceleratorSourceConflicts(sources []string, requiredAccelerator string) []string {
	if !strings.EqualFold(strings.TrimSpace(requiredAccelerator), "required") {
		return nil
	}
	conflicts := make([]string, 0)
	for _, source := range sources {
		parsed, err := url.Parse(strings.TrimSpace(source))
		if err != nil || parsed == nil {
			continue
		}
		normalizedPath := strings.NewReplacer(
			"/", " ", "-", " ", "_", " ", "+", " ", ".", " ",
		).Replace(strings.ToLower(parsed.EscapedPath()))
		for _, token := range strings.Fields(normalizedPath) {
			if token == "cpu" {
				conflicts = append(conflicts, source)
				break
			}
		}
	}
	return uniqueSortedFolded(conflicts)
}

// managedPackageMutationRequiresExecution distinguishes a plain idempotent
// "ensure package exists" request from an explicit source, build, upgrade, or
// fork operation. Package-name inventory alone cannot prove that such a
// mutation is already satisfied; returning reuse would silently ignore the
// requested compatibility repair.
func managedPackageMutationRequiresExecution(request managePackagesInput) bool {
	return len(request.Channels) > 0 || len(request.PipArgs) > 0 ||
		len(request.PipFindLinks) > 0 || len(request.PipExtraIndexURLs) > 0 ||
		strings.TrimSpace(request.ForkTo) != ""
}

// canonicalManagedPackageImplementation removes a workflow/utility Skill name
// that a weak model placed in the scientific implementation field. A loaded
// Skill without implementation identities cannot become a new user-selected
// engine merely because one of its support imports is missing. Dedicated
// implementation Skills retain their exact identity and selection contract.
func (s *Server) canonicalManagedPackageImplementation(ctx context.Context, requested string) string {
	requested = strings.TrimSpace(requested)
	run, _ := transcriptRunnerChatRunFromContext(ctx)
	if requested == "" || run == nil || s == nil || s.skillCatalog == nil {
		return requested
	}
	for _, loaded := range run.executedSkillNamesSnapshot() {
		skill, found := findCatalogSkill(s.skillCatalog, loaded)
		if !found || len(skill.ImplementationIdentities) != 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(skill.Name), requested) {
			return ""
		}
	}
	requestedIsDedicated := false
	for _, skill := range s.skillCatalog.Skills() {
		for _, identity := range skill.ImplementationIdentities {
			if taskImplementationMatchesRegistered(requested, identity) {
				requestedIsDedicated = true
				break
			}
		}
		if requestedIsDedicated {
			break
		}
	}
	if requestedIsDedicated {
		return requested
	}
	activeImplementation := len(run.selectedImplementationsSnapshot()) > 0
	if !activeImplementation {
		for _, skill := range s.skillCatalog.Skills() {
			for _, identity := range skill.ImplementationIdentities {
				if taskExplicitlyNamesImplementation(run.TaskIntent, identity) {
					activeImplementation = true
					break
				}
			}
			if activeImplementation {
				break
			}
		}
	}
	if activeImplementation {
		// Once the task owns a scientific implementation, a package mutation
		// cannot invent an unrelated implementation identity. A real route
		// change must first be represented by a dedicated implementation Skill
		// and the normal user-choice continuity contract.
		return ""
	}
	return requested
}

var managedEnvironmentExactPythonPackagePattern = regexp.MustCompile(
	`(?i)^(?:[a-z0-9._-]+::)?python\s*(?:==|=)\s*(3\.[0-9]{1,2}(?:\.[0-9]{1,2})?)$`,
)

// managedEnvironmentCanonicalPythonVersion makes the dedicated python_version
// field and an exact package-level Python pin one coherent contract. Models
// frequently express the pin in packages; silently replacing it with the
// manager default can make an otherwise valid legacy scientific stack fail far
// downstream. Broad ranges remain governed by python_version/default behavior.
func managedEnvironmentCanonicalPythonVersion(explicit string, packages []string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	observed := ""
	for _, raw := range packages {
		value := strings.TrimSpace(raw)
		if strings.HasPrefix(strings.ToLower(value), "pip::") {
			continue
		}
		match := managedEnvironmentExactPythonPackagePattern.FindStringSubmatch(value)
		if len(match) != 2 {
			continue
		}
		if observed != "" && observed != match[1] {
			return "", errors.New("packages contain conflicting exact Python versions")
		}
		observed = match[1]
	}
	if observed == "" {
		return explicit, nil
	}
	if explicit != "" && explicit != observed {
		return "", errors.New("python_version conflicts with the exact Python package requirement")
	}
	return observed, nil
}

func managedEnvironmentPreflightPackages(condaPackages []string, pipPhases [][]string) []string {
	result := append([]string(nil), condaPackages...)
	for _, phase := range pipPhases {
		for _, requirement := range phase {
			value := strings.TrimSpace(requirement)
			if value != "" {
				result = append(result, "pip::"+value)
			}
		}
	}
	return result
}

func unprefixedManagedSourceReferences(packages []string) []string {
	result := make([]string, 0)
	for _, raw := range packages {
		value := strings.TrimSpace(raw)
		lower := strings.ToLower(value)
		if strings.HasPrefix(lower, "pip::") || strings.HasPrefix(lower, "pip:") {
			continue
		}
		if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") ||
			strings.HasPrefix(lower, "git+https://") || strings.HasPrefix(lower, "git+http://") {
			result = append(result, value)
		}
	}
	return result
}

func managedSourcePackageContractResult(tool string, sources []string) map[string]any {
	return map[string]any{
		"tool": tool, "ok": true, "executed": false, "status": "package_source_contract_required",
		"source_count": len(sources),
		"message":      "A source repository or download URL is not automatically an installable package.",
		"recovery":     "Inspect the verified source first. Use an explicit pip:: requirement only when packaging metadata establishes that installation contract; otherwise keep the checkout as task source and provision its documented dependencies in ordered managed phases.",
	}
}

func normalizeManagedEmbeddedPipFindLinks(packages, existing []string) ([]string, []string, error) {
	normalized := make([]string, 0, len(packages))
	links := append([]string(nil), existing...)
	for _, raw := range packages {
		value := strings.TrimSpace(raw)
		fields := strings.Fields(value)
		option := -1
		for index, field := range fields {
			if field == "--find-links" || field == "-f" {
				if option >= 0 {
					return nil, nil, errors.New("multiple embedded pip source options are ambiguous")
				}
				option = index
			}
		}
		if option < 0 {
			normalized = append(normalized, value)
			continue
		}
		if option != 1 || len(fields) != 3 || strings.TrimSpace(fields[0]) == "" || strings.TrimSpace(fields[2]) == "" {
			return nil, nil, errors.New("embedded pip source option must contain one requirement and one URL")
		}
		normalized = append(normalized, fields[0])
		links = append(links, fields[2])
	}
	return normalized, uniqueSortedFolded(links), nil
}

// normalizeManagedPipSourceKinds repairs a common structured-source mismatch
// without guessing a package or implementation. pip's extra-index option
// expects a package index, while project-hosted HTML wheel matrices are link
// pages and must be passed through --find-links. Keeping that distinction at
// the tool boundary prevents pip from appending distribution names to an HTML
// document and then silently falling back to an incompatible source build.
func normalizeManagedPipSourceKinds(findLinks, extraIndexURLs []string) ([]string, []string) {
	normalizedFindLinks := append([]string(nil), findLinks...)
	normalizedExtraIndexes := make([]string, 0, len(extraIndexURLs))
	for _, raw := range extraIndexURLs {
		value := strings.TrimSpace(raw)
		parsed, err := url.Parse(value)
		if err == nil && parsed != nil {
			path := strings.ToLower(strings.TrimSpace(parsed.Path))
			if strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".htm") {
				normalizedFindLinks = append(normalizedFindLinks, value)
				continue
			}
		}
		normalizedExtraIndexes = append(normalizedExtraIndexes, value)
	}
	return uniqueSortedFolded(normalizedFindLinks), uniqueSortedFolded(normalizedExtraIndexes)
}

func managedPipSourceOptionContractResult(tool string, cause error) map[string]any {
	message := "A pip wheel source must be supplied separately from the package requirement."
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = cause.Error()
	}
	return map[string]any{
		"tool": tool, "ok": true, "executed": false, "status": "pip_source_option_contract_required",
		"message":  message,
		"recovery": "Keep package names in packages or pip_phases and place each verified public wheel page in pip_find_links (or a simple-index base in pip_extra_index_urls), then retry the same implementation.",
	}
}

func normalizeManagedPackageMutationAuthority(packages []string, usePip bool) ([]string, bool, error) {
	normalized := make([]string, 0, len(packages))
	pipPrefixed := 0
	for _, raw := range packages {
		value := normalizeAgentManagedEnvironmentPackageAuthority(raw)
		if strings.HasPrefix(strings.ToLower(value), "pip::") {
			value = strings.TrimSpace(value[len("pip::"):])
			pipPrefixed++
		}
		normalized = append(normalized, value)
	}
	if pipPrefixed == 0 {
		return normalized, usePip, nil
	}
	if usePip || pipPrefixed == len(normalized) {
		return normalized, true, nil
	}
	return nil, false, errors.New("mixed package authorities require separate immutable operations")
}

func normalizeAgentManagedEnvironmentPackageAuthorities(packages []string) []string {
	normalized := make([]string, 0, len(packages))
	for _, raw := range packages {
		normalized = append(normalized, normalizeAgentManagedEnvironmentPackageAuthority(raw))
	}
	return normalized
}

func normalizeAgentManagedEnvironmentPackageAuthority(raw string) string {
	value := strings.TrimSpace(raw)
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "pip::"):
		return "pip::" + strings.TrimSpace(value[len("pip::"):])
	case strings.HasPrefix(lower, "pip:"):
		return "pip::" + strings.TrimSpace(value[len("pip:"):])
	case strings.HasPrefix(lower, "git+https://"), strings.HasPrefix(lower, "git+http://"),
		strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"):
		// A URL is source evidence, not an implicit installer authority. The
		// execution boundary returns a recoverable contract result before this
		// helper is reached; preserve the value for direct callers as well.
		return value
	default:
		return value
	}
}

// canonicalManagedToolChannels keeps the model-facing environment path on one
// deterministic channel family. The kernel authority continues to reject
// mixed families for direct callers; the Harness boundary merely removes the
// redundant commercial default when the request already names a community
// channel. This is equivalent to the declarative community spec and prevents
// a non-executing resolver error from becoming a failed user-visible tool step.
func canonicalManagedToolChannels(channels []string) []string {
	usesCommunity := false
	for _, channel := range channels {
		if value := strings.ToLower(strings.TrimSpace(channel)); value != "" && value != "defaults" {
			usesCommunity = true
			break
		}
	}
	if !usesCommunity {
		return channels
	}
	result := make([]string, 0, len(channels))
	for _, channel := range channels {
		if strings.EqualFold(strings.TrimSpace(channel), "defaults") {
			continue
		}
		result = append(result, channel)
	}
	return result
}

func validateManagedEnvironmentHumanDescription(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 256 || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("managed environment human_description must be one bounded line")
	}
	return nil
}

func (s *Server) requireManagedEnvironmentRegistrationGrant(userID, sourcePath, venvPath string) error {
	if s == nil {
		return errors.New("managed environment registration authority is unavailable")
	}
	source, err := canonicalHostDirectory(sourcePath)
	if err != nil {
		return errors.New("manage_environments register source_path is unavailable")
	}
	venvPath = strings.TrimSpace(venvPath)
	if venvPath == "" {
		venvPath = filepath.Join(source, ".venv")
	}
	venv, err := filepath.Abs(venvPath)
	if err != nil {
		return errors.New("manage_environments register venv_path is invalid")
	}
	venv = filepath.Clean(venv)
	grants, err := s.loadHostGrants(userID)
	if err != nil {
		return errors.New("manage_environments register host grants could not be verified")
	}
	allowed := func(path string) bool {
		for _, grant := range grants {
			if grant.Mode != "read_write" {
				continue
			}
			root, resolveErr := canonicalHostDirectory(grant.Path)
			if resolveErr == nil && hostPathWithin(root, path) {
				return true
			}
		}
		return false
	}
	if !allowed(source) || !allowed(venv) {
		return errors.New("manage_environments register requires read-write host grants covering source_path and venv_path")
	}
	return nil
}

func decodeStrictToolInput(input map[string]any, target any) error {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 256<<10 {
		return errors.New("managed environment input is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("managed environment input is invalid: %w", err)
	}
	return ensureJSONEOF(decoder)
}

func managedEnvironmentInventorySummary(environment kernelruntime.ManagedEnvironment) map[string]any {
	return map[string]any{
		"name": environment.Name, "language": environment.Language, "status": environment.Status,
	}
}

func managedEnvironmentOperationReceipt(
	toolName, status string,
	environment kernelruntime.ManagedEnvironment,
	metadata map[string]any,
) map[string]any {
	receipt := map[string]any{
		"status": status,
		"tool":   toolName,
		"environment": map[string]any{
			"name": environment.Name, "language": environment.Language, "kind": environment.Kind,
			"generation": environment.Generation, "spec_digest": environment.SpecDigest,
			"status": environment.Status, "package_count": len(environment.Packages),
		},
	}
	for key, value := range metadata {
		receipt[key] = value
	}
	return receipt
}

func boundedManagedEnvironmentError(err error) string {
	if err == nil {
		return ""
	}
	sanitized := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, err.Error())
	message := strings.Join(strings.Fields(sanitized), " ")
	characters := []rune(message)
	if len(characters) > 1000 {
		message = string(characters[:1000]) + "..."
	}
	return message
}

func boundedManagedEnvironmentErrorTail(err error, maximumRunes int) string {
	if err == nil || maximumRunes <= 0 {
		return ""
	}
	sanitized := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, err.Error())
	message := strings.Join(strings.Fields(sanitized), " ")
	characters := []rune(message)
	if len(characters) <= maximumRunes {
		return message
	}
	return "..." + string(characters[len(characters)-maximumRunes:])
}

var (
	managedEnvironmentMissingExecutablePattern  = regexp.MustCompile(`(?i)no such file or directory:\s*['\"]([^'\"]+)['\"]`)
	managedEnvironmentMissingModulePattern      = regexp.MustCompile(`(?i)no module named\s+['\"]([^'\"]+)['\"]`)
	managedEnvironmentUnavailablePackagePattern = regexp.MustCompile(`(?im)[└├][─-]?\s*([^\s=<>!~]+).*?does not exist`)
)

func managedEnvironmentFailureReceipt(toolName string, err error, metadata ...map[string]any) map[string]any {
	category, cause, details := classifyManagedEnvironmentFailure(err)
	failure := map[string]any{
		"category": category, "cause": cause, "details": details,
		"diagnostic_tail": boundedManagedEnvironmentErrorTail(err, 2400),
		"recovery":        managedEnvironmentFailureRecovery(category),
	}
	receipt := map[string]any{
		"tool": toolName, "ok": false, "status": "failed",
		"error":   boundedManagedEnvironmentError(err),
		"failure": failure,
	}
	combinedMetadata := map[string]any{}
	for _, values := range metadata {
		for key, value := range values {
			receipt[key] = value
			combinedMetadata[key] = value
		}
	}
	if category == "dependency_resolution_failed" {
		failure["recovery_invariants"] = managedEnvironmentRecoveryInvariants(combinedMetadata)
	}
	return receipt
}

func managedEnvironmentRecoveryInvariants(metadata map[string]any) map[string]any {
	invariants := map[string]any{
		"policy": "The selected scientific implementation and compatibility facts established before mutation remain binding until newer verified evidence supersedes them. Change the installation candidate, source authority, or phase partition without silently weakening those facts.",
	}
	if implementation := strings.TrimSpace(stringValue(metadata["implementation"])); implementation != "" {
		invariants["implementation"] = implementation
	}
	if packages := stringArrayValue(metadata["requested_packages"]); len(packages) != 0 {
		invariants["requested_packages"] = append([]string(nil), packages...)
	}
	preflight := mapValue(metadata["preflight"])
	if capabilities := stringArrayValue(preflight["required_capabilities"]); len(capabilities) != 0 {
		invariants["required_capabilities"] = append([]string(nil), capabilities...)
	}
	if accelerator := strings.TrimSpace(stringValue(preflight["verified_accelerator_requirement"])); accelerator != "" && !strings.EqualFold(accelerator, "none") {
		invariants["verified_accelerator_requirement"] = accelerator
	}
	if machine := mapValue(preflight["machine"]); machine != nil {
		if accelerator := managedEnvironmentJSONMap(machine["accelerator"]); accelerator != nil {
			invariants["observed_accelerator"] = accelerator
		}
	}
	return invariants
}

func managedEnvironmentJSONMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	result := map[string]any{}
	if err := json.Unmarshal(raw, &result); err != nil || len(result) == 0 {
		return nil
	}
	return result
}

func managedEnvironmentFailureRecovery(category string) string {
	switch strings.TrimSpace(category) {
	case "package_source_transport_failed":
		return "Keep the selected implementation and immutable environment history. Retry the exact official package source through manage_packages after checking the observed TLS/proxy/redirect path; preserve package-cache and partial-transfer evidence. If the official host remains unreachable, use only an officially linked mirror or an explicit source-build route with its build prerequisites. Do not treat a fallback source archive's later compiler/import error as the original cause."
	case "build_isolation_missing_dependency":
		return "Keep the selected implementation. Confirm that the intended wheel source is reachable first; if a source build is actually required, add the missing build dependency through manage_packages or use the reviewed --no-build-isolation phase when the official build contract requires it, then retry only that package phase."
	case "missing_build_tool":
		return "Keep the selected implementation. Add the exact missing compiler or build executable through the managed environment authority, preserve completed downloads, then retry only the source-build package phase."
	case "dependency_resolution_failed":
		return "Keep the selected implementation and every compatibility condition already established by the observed machine and reviewed evidence. If the compatible build is absent from the chosen package authority, change only the verified installation authority or split the plan into a minimal base plus ordered managed pip phases. Do not lower the framework, accelerator, or compiled-extension family to a build already known not to support the observed device merely to satisfy the solver. Then retry only the affected immutable environment step."
	case "installer_inactive":
		return "Keep the selected implementation, immutable environment history, and completed package-cache evidence. Verify the selected source transport or local resource path, then retry only the affected environment step. If the same inactivity recurs, use a materially different verified source or execution provider; do not leave the old process running or start a competing installer."
	default:
		return "Keep the selected implementation. Inspect its verified source and environment contract, correct this causal condition, and retry only the affected immutable environment step."
	}
}

func classifyManagedEnvironmentFailure(err error) (category, cause string, details map[string]any) {
	message := ""
	if err != nil {
		message = strings.TrimSpace(err.Error())
	}
	details = map[string]any{}
	var inactivity *kernelruntime.ManagedEnvironmentInstallerInactivityError
	if errors.As(err, &inactivity) {
		details["failure_stage"] = "installer_execution"
		if inactivity.Duration > 0 {
			details["inactivity_seconds"] = int64(inactivity.Duration / time.Second)
		}
		return "installer_inactive", "The local installer remained alive but produced no observable output, CPU work, process-tree change, or I/O within its bounded activity window.", details
	}
	var processInactivity *processsupervisor.InactivityError
	if errors.As(err, &processInactivity) {
		details["failure_stage"] = "installer_execution"
		if processInactivity.Duration > 0 {
			details["inactivity_seconds"] = int64(processInactivity.Duration / time.Second)
		}
		return "installer_inactive", "The local installer remained alive but produced no observable output, CPU work, process-tree change, or I/O within its bounded activity window.", details
	}
	lowerMessage := strings.ToLower(message)
	packageSourceTransportFailed := strings.Contains(lowerMessage, "could not fetch url") &&
		(strings.Contains(lowerMessage, "looking in links:") || strings.Contains(lowerMessage, "no matching distribution") ||
			strings.Contains(lowerMessage, "could not find a version"))
	if packageSourceTransportFailed || strings.Contains(lowerMessage, "certificate verify failed") {
		details["failure_stage"] = "package_source_fetch"
		transport := "network"
		if strings.Contains(lowerMessage, "ssl") || strings.Contains(lowerMessage, "certificate") ||
			strings.Contains(lowerMessage, "unexpected_eof_while_reading") {
			transport = "tls"
		}
		details["transport"] = transport
		return "package_source_transport_failed", "The selected package source could not be reached reliably; any later fallback source-build error is secondary until this transport path is repaired or deliberately replaced.", details
	}
	if matches := managedEnvironmentMissingExecutablePattern.FindAllStringSubmatch(message, -1); len(matches) > 0 {
		match := matches[len(matches)-1]
		details["missing_executable"] = match[1]
		return "missing_build_tool", "A source-build subprocess could not locate a required executable.", details
	}
	if strings.Contains(lowerMessage, "does not appear to be a python project") {
		return "source_not_installable_package", "The selected source is a repository checkout, not an installable Python package.", details
	}
	if matches := managedEnvironmentMissingModulePattern.FindAllStringSubmatch(message, -1); len(matches) > 0 {
		match := matches[len(matches)-1]
		details["missing_module"] = match[1]
		return "build_isolation_missing_dependency", "A package build environment could not import a required build dependency.", details
	}
	if strings.Contains(lowerMessage, "could not solve for environment specs") {
		if matches := managedEnvironmentUnavailablePackagePattern.FindStringSubmatch(message); len(matches) > 1 {
			details["unavailable_package"] = strings.TrimSpace(matches[1])
		}
		return "dependency_resolution_failed", "The declared versions, channels, or build variants could not be resolved together.", details
	}
	if strings.Contains(lowerMessage, "package specification") && strings.Contains(lowerMessage, "is invalid") {
		return "package_input_contract_invalid", "A package requirement combined installer options with the package identity or used unsupported syntax.", details
	}
	if strings.Contains(lowerMessage, "not atomically activated") {
		return "environment_not_active", "The requested source environment has no verified active generation.", details
	}
	return "installation_failed", "The environment operation ended unsuccessfully; use the terminal diagnostic rather than earlier warnings as the causal evidence.", details
}
