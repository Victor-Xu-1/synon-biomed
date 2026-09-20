package server

import (
	"context"
	"errors"
	"sort"
	"strings"

	"synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"
)

const bytesPerMiB = uint64(1024 * 1024)

const maxManagedEnvironmentPreflightAlternatives = 8

type managedEnvironmentResourceRequirements struct {
	MinCPUCores            int    `json:"min_cpu_cores"`
	MinMemoryMB            int64  `json:"min_memory_mb"`
	MinDiskMB              int64  `json:"min_disk_mb"`
	Accelerator            string `json:"accelerator"`
	MinAcceleratorMemoryMB int64  `json:"min_accelerator_memory_mb,omitempty"`
}

func managedEnvironmentResourceRequirementsSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"min_cpu_cores":             map[string]any{"type": "integer", "minimum": 1, "maximum": 4096},
			"min_memory_mb":             map[string]any{"type": "integer", "minimum": 256, "maximum": 16777216},
			"min_disk_mb":               map[string]any{"type": "integer", "minimum": 256, "maximum": 67108864},
			"accelerator":               map[string]any{"type": "string", "enum": []string{"none", "optional", "required"}},
			"min_accelerator_memory_mb": map[string]any{"type": "integer", "minimum": 0, "maximum": 16777216},
		},
		"required": []string{"min_cpu_cores", "min_memory_mb", "min_disk_mb", "accelerator"},
	}
}

type managedEnvironmentPreflightSpec struct {
	Operation            string                                 `json:"operation"`
	Implementation       string                                 `json:"implementation,omitempty"`
	Name                 string                                 `json:"name,omitempty"`
	Environment          string                                 `json:"environment,omitempty"`
	ForkTo               string                                 `json:"fork_to,omitempty"`
	Language             string                                 `json:"language"`
	PythonVersion        string                                 `json:"python_version,omitempty"`
	Packages             []string                               `json:"packages"`
	Channels             []string                               `json:"channels,omitempty"`
	Imports              []string                               `json:"imports,omitempty"`
	PackageSourceURLs    []string                               `json:"package_source_urls,omitempty"`
	UsePip               bool                                   `json:"use_pip,omitempty"`
	ResourceRequirements managedEnvironmentResourceRequirements `json:"resource_requirements"`
}

func defaultManagedEnvironmentCreateRequirements(
	requested managedEnvironmentResourceRequirements,
	packageCount int,
) managedEnvironmentResourceRequirements {
	if requested.MinCPUCores <= 0 {
		requested.MinCPUCores = 1
	}
	if requested.MinMemoryMB <= 0 {
		requested.MinMemoryMB = int64(max(1024, packageCount*256))
	}
	if requested.MinDiskMB <= 0 {
		requested.MinDiskMB = int64(max(2048, packageCount*512))
	}
	if strings.TrimSpace(requested.Accelerator) == "" {
		requested.Accelerator = "none"
	}
	return requested
}

func normalizeManagedEnvironmentResourceRequirements(
	value managedEnvironmentResourceRequirements,
) (managedEnvironmentResourceRequirements, error) {
	value.Accelerator = strings.ToLower(strings.TrimSpace(value.Accelerator))
	if value.MinCPUCores < 1 || value.MinCPUCores > 4096 {
		return value, errors.New("resource_requirements.min_cpu_cores must be between 1 and 4096")
	}
	if value.MinMemoryMB < 256 || value.MinMemoryMB > 16*1024*1024 {
		return value, errors.New("resource_requirements.min_memory_mb must be between 256 and 16777216")
	}
	if value.MinDiskMB < 256 || value.MinDiskMB > 64*1024*1024 {
		return value, errors.New("resource_requirements.min_disk_mb must be between 256 and 67108864")
	}
	switch value.Accelerator {
	case "none", "optional", "required":
	default:
		return value, errors.New("resource_requirements.accelerator must be none, optional, or required")
	}
	if value.MinAcceleratorMemoryMB < 0 || value.MinAcceleratorMemoryMB > 16*1024*1024 {
		return value, errors.New("resource_requirements.min_accelerator_memory_mb is out of range")
	}
	if value.Accelerator == "none" && value.MinAcceleratorMemoryMB != 0 {
		return value, errors.New("accelerator memory cannot be requested when accelerator is none")
	}
	return value, nil
}

func normalizeManagedEnvironmentPreflightSpec(spec managedEnvironmentPreflightSpec) (managedEnvironmentPreflightSpec, error) {
	spec.Operation = strings.ToLower(strings.TrimSpace(spec.Operation))
	spec.Implementation = strings.TrimSpace(spec.Implementation)
	if spec.Operation != "create" && spec.Operation != "install" {
		return spec, errors.New("environment preflight operation must be create or install")
	}
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Environment = strings.TrimSpace(spec.Environment)
	spec.ForkTo = strings.TrimSpace(spec.ForkTo)
	spec.Language = strings.ToLower(strings.TrimSpace(spec.Language))
	if spec.Language == "" {
		spec.Language = "python"
	}
	spec.PythonVersion = strings.TrimSpace(spec.PythonVersion)
	spec.Packages = uniqueSortedFolded(spec.Packages)
	spec.Channels = uniqueSortedFolded(canonicalManagedToolChannels(spec.Channels))
	spec.Imports = uniqueSortedFolded(spec.Imports)
	spec.PackageSourceURLs = uniqueSortedFolded(spec.PackageSourceURLs)
	if len(spec.Packages) == 0 {
		return spec, errors.New("environment preflight requires the proposed package set")
	}
	var err error
	spec.ResourceRequirements, err = normalizeManagedEnvironmentResourceRequirements(spec.ResourceRequirements)
	if err != nil {
		return spec, err
	}
	return spec, nil
}

func managedEnvironmentPreflightDependencyNames(packages []string) ([]string, bool) {
	names := make([]string, 0, len(packages))
	hasSourceRequirement := false
	for _, raw := range packages {
		value := strings.TrimSpace(raw)
		if index := strings.Index(value, "::"); index >= 0 {
			value = strings.TrimSpace(value[index+2:])
		}
		lower := strings.ToLower(value)
		if strings.HasPrefix(lower, "git+https://") || strings.HasPrefix(lower, "https://") ||
			strings.Contains(lower, " @ git+https://") || strings.Contains(lower, " @ https://") {
			hasSourceRequirement = true
			continue
		}
		if value != "" {
			names = append(names, value)
		}
	}
	return uniqueSortedFolded(names), hasSourceRequirement
}

// applyImplementationResourceRequirements binds resource requirements to the
// exact catalog implementation under inspection. Loaded Skills describe task
// history, not the capabilities of every later engine or dependency stage.
// An unknown identity has no catalog capability authority; in particular,
// concatenating registered names must not synthesize a composite contract.
func (s *Server) applyImplementationResourceRequirements(
	implementation string,
	requirements managedEnvironmentResourceRequirements,
) (managedEnvironmentResourceRequirements, []string, string) {
	if s == nil || s.skillCatalog == nil {
		return requirements, nil, ""
	}
	skill, found := dedicatedSkillForImplementation(s.skillCatalog, implementation)
	if !found {
		return requirements, nil, ""
	}
	requiredCapabilities := make([]string, 0, len(skill.RequiredCapabilities))
	for _, capability := range skill.RequiredCapabilities {
		capability = strings.ToLower(strings.TrimSpace(capability))
		if capability == "" {
			continue
		}
		requiredCapabilities = append(requiredCapabilities, capability)
		if capability == "gpu" {
			requirements.Accelerator = "required"
		}
	}
	return requirements, uniqueSortedFolded(requiredCapabilities), skill.Name
}

func (s *Server) executeManagedEnvironmentResourcePreflight(
	ctx context.Context,
	identity *agentKernelContext,
	authority managedEnvironmentAuthority,
	spec managedEnvironmentPreflightSpec,
) (map[string]any, error) {
	if s == nil || s.kernelManager == nil || authority == nil || identity == nil {
		return nil, errors.New("environment resource preflight is unavailable")
	}
	if _, err := s.validateKernelHostIdentity(ctx, identity.access); err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(spec.Operation), "install") && strings.TrimSpace(spec.Environment) != "" {
		source, found, err := authority.InspectManagedEnvironment(ctx, strings.TrimSpace(spec.Environment))
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("managed environment does not exist")
		}
		// Mutation retains the source runtime. Neither a tool default nor the
		// package manager used for a stage may change its execution language.
		if source.Language != "" {
			spec.Language = source.Language
		}
	}
	spec, err := normalizeManagedEnvironmentPreflightSpec(spec)
	if err != nil {
		return nil, err
	}
	sourceInputs := append(append([]string(nil), spec.Packages...), spec.PackageSourceURLs...)
	sourceEvidence, missingSources, err := s.managedEnvironmentSourceEvidence(
		ctx, sourceInputs, spec.PackageSourceURLs...,
	)
	if err != nil {
		return nil, err
	}
	if len(missingSources) > 0 {
		return map[string]any{
			"ok": true, "mode": "preflight", "executed": false, "feasible": false,
			"status": "implementation_source_evidence_required", "implementation": spec.Implementation,
			"missing_sources": missingSources,
			"recovery":        "Keep the same implementation. Resolve its canonical public source from the loaded dedicated Skill or a successful current-task source lookup, then retry this exact read-only preflight with the corrected source. Do not infer an owner, repository, release, checkpoint, or entry point from the implementation name.",
		}, nil
	}
	requirements, requiredCapabilities, capabilitySkill := s.applyImplementationResourceRequirements(spec.Implementation, spec.ResourceRequirements)
	inventory := s.kernelManager.ListAllSessionKernelsWithResources(identity.workspaceDir)
	machine := inventory.Machine
	gpu := compute.UnavailableGPUInfo()
	if s.hostGPUDetector != nil {
		gpu = s.hostGPUDetector(ctx)
	}
	observedShortfalls := make([]string, 0, 5)
	advisories := make([]string, 0, 1)
	if machine.Cores < requirements.MinCPUCores {
		observedShortfalls = append(observedShortfalls, "insufficient_cpu")
	}
	memoryBlocked, memoryBusy := managedEnvironmentMemoryCapacity(
		machine.TotalMemoryBytes, machine.AvailableMemoryBytes, requirements.MinMemoryMB,
	)
	if memoryBlocked {
		observedShortfalls = append(observedShortfalls, "insufficient_memory")
	}
	if memoryBusy {
		advisories = append(advisories, "current_available_memory_below_requested_capacity")
	}
	if machine.DiskAvailableBytes == nil || *machine.DiskAvailableBytes < uint64(requirements.MinDiskMB)*bytesPerMiB {
		observedShortfalls = append(observedShortfalls, "insufficient_disk")
	}
	if requirements.Accelerator == "required" && !gpu.Available {
		observedShortfalls = append(observedShortfalls, "accelerator_unavailable")
	}
	if requirements.MinAcceleratorMemoryMB > 0 &&
		(gpu.GPUMemoryMB == nil || *gpu.GPUMemoryMB < requirements.MinAcceleratorMemoryMB) {
		observedShortfalls = append(observedShortfalls, "insufficient_accelerator_memory")
	}
	sort.Strings(observedShortfalls)
	// Numeric minima arrive in a model-authored tool call. Until a reviewed
	// Skill or current official source supplies those values, a mismatch is a
	// useful planning observation but not authority to reject a capable route
	// or force an implementation change. A categorical GPU requirement derived
	// from the exact implementation Skill remains authoritative because it
	// describes the implementation itself, not a guessed capacity threshold.
	verifiedBlockers := make([]string, 0, 1)
	verifiedAcceleratorRequirement := ""
	if capabilitySkill != "" {
		verifiedAcceleratorRequirement = "none"
	}
	for _, capability := range requiredCapabilities {
		if capability == "gpu" {
			verifiedAcceleratorRequirement = "required"
			if !gpu.Available {
				verifiedBlockers = append(verifiedBlockers, "accelerator_unavailable")
			}
			break
		}
	}
	for _, shortfall := range observedShortfalls {
		if shortfall == "accelerator_unavailable" && verifiedAcceleratorRequirement == "required" {
			continue
		}
		advisories = append(advisories, "unverified_"+shortfall)
	}
	sort.Strings(verifiedBlockers)
	sort.Strings(advisories)
	dependencies, hasSourceRequirement := managedEnvironmentPreflightDependencyNames(spec.Packages)
	candidates := []kernelruntime.ManagedEnvironment(nil)
	if !hasSourceRequirement {
		candidates, err = authority.ListManagedEnvironments(ctx, kernelruntime.ManagedEnvironmentQuery{
			Language: spec.Language, Dependencies: dependencies, IncludePackages: true,
			RequiredAccelerator: requirements.Accelerator,
		})
		if err != nil {
			return nil, err
		}
		if run, ok := transcriptRunnerChatRunFromContext(ctx); ok {
			candidates = run.filterReusableManagedEnvironmentCandidates(candidates)
		}
		if len(spec.Imports) > 0 {
			verified := make([]kernelruntime.ManagedEnvironment, 0, len(candidates))
			for _, candidate := range candidates {
				if err := authority.VerifyManagedEnvironmentImports(ctx, candidate.Name, spec.Imports); err == nil {
					verified = append(verified, candidate)
				}
			}
			candidates = verified
		}
		if spec.Operation == "install" {
			candidates = managedEnvironmentMutationReuseCandidates(
				candidates, spec.Environment, spec.ForkTo,
			)
		}
	}
	preferredName := spec.Name
	if spec.Operation == "install" {
		preferredName = spec.Environment
	}
	rankManagedEnvironmentPreflightCandidates(candidates, preferredName)
	candidateNames := make([]string, 0, min(len(candidates), maxManagedEnvironmentPreflightAlternatives))
	for _, candidate := range candidates[:min(len(candidates), maxManagedEnvironmentPreflightAlternatives)] {
		candidateNames = append(candidateNames, candidate.Name)
	}
	var recommendedEnvironment map[string]any
	if len(candidates) > 0 {
		recommendedEnvironment = managedEnvironmentPreflightCandidateSummary(
			candidates[0], dependencies,
		)
	}
	recommendation := spec.Operation
	setupState := "new_setup_feasible"
	if len(candidates) > 0 {
		recommendation = "reuse"
		setupState = "reuse_ready"
	} else if len(verifiedBlockers) > 0 {
		recommendation = "prefer a lighter local environment or an admitted remote compute route"
		setupState = "new_setup_blocked"
	}
	selectedRouteBlockers := verifiedBlockers
	if len(candidates) > 0 {
		selectedRouteBlockers = []string{}
	}
	return map[string]any{
		"ok": true, "mode": "preflight", "feasible": len(selectedRouteBlockers) == 0,
		"operation": spec.Operation, "implementation": spec.Implementation,
		"required_imports": append([]string(nil), spec.Imports...),
		"requirements":     requirements, "required_capabilities": requiredCapabilities,
		"capability_contract_skill":        capabilitySkill,
		"resource_requirements_verified":   false,
		"verified_accelerator_requirement": verifiedAcceleratorRequirement,
		"observed_resource_shortfalls":     observedShortfalls,
		"resource_requirements_note":       "Model-supplied CPU, memory, disk, and accelerator-memory minima are advisory until supported by a reviewed Skill or current official source. They cannot reject a route or force a new user decision. Categorical requirements come only from the exact implementation's catalog Skill; an empty capability_contract_skill means no implementation capability contract was established. Host feasibility is not scientific readiness or implementation authorization.",
		"source_evidence":                  sourceEvidence,
		"machine": map[string]any{
			"sampled_at": machine.SampledAt, "cpu_cores": machine.Cores,
			"total_memory_bytes": machine.TotalMemoryBytes, "available_memory_bytes": machine.AvailableMemoryBytes,
			"disk_total_bytes": machine.DiskTotalBytes, "disk_available_bytes": machine.DiskAvailableBytes,
			"active_kernel_count": machine.KernelCount, "busy_kernel_count": machine.BusyCount,
			"accelerator": gpu,
		},
		"recommended_environment":              recommendedEnvironment,
		"compatible_environments":              candidateNames,
		"compatible_environment_count":         len(candidates),
		"omitted_compatible_environment_count": max(0, len(candidates)-len(candidateNames)),
		"setup_state":                          setupState,
		"reuse_preferred":                      len(candidates) > 0,
		"requires_new_environment":             len(candidates) == 0,
		"blockers":                             selectedRouteBlockers, "mutation_blockers": verifiedBlockers,
		"advisories": advisories, "recommendation": recommendation,
	}, nil
}

// managedEnvironmentMutationReuseCandidates keeps a package delta attached to
// the environment the caller asked to extend. A package such as a parser may
// already exist in many unrelated scientific environments; choosing one of
// those merely because it satisfies the delta would silently replace the
// selected implementation and bind the requested name to the wrong runtime.
// An explicit fork target is the only additional name that may be reused.
func managedEnvironmentMutationReuseCandidates(
	candidates []kernelruntime.ManagedEnvironment,
	environment, forkTo string,
) []kernelruntime.ManagedEnvironment {
	allowed := map[string]struct{}{}
	for _, name := range []string{environment, forkTo} {
		if key := strings.ToLower(strings.TrimSpace(name)); key != "" {
			allowed[key] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	filtered := make([]kernelruntime.ManagedEnvironment, 0, min(len(candidates), len(allowed)))
	for _, candidate := range candidates {
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(candidate.Name))]; ok {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func managedEnvironmentMemoryCapacity(total, available *uint64, minimumMB int64) (blocked, currentlyBusy bool) {
	required := uint64(minimumMB) * bytesPerMiB
	if total == nil || *total < required {
		return true, false
	}
	return false, available != nil && *available < required
}

func (s *Server) managedEnvironmentMachineSnapshot(
	ctx context.Context,
	identity *agentKernelContext,
) map[string]any {
	inventory := s.kernelManager.ListAllSessionKernelsWithResources(identity.workspaceDir)
	machine := inventory.Machine
	gpu := compute.UnavailableGPUInfo()
	if s.hostGPUDetector != nil {
		gpu = s.hostGPUDetector(ctx)
	}
	return map[string]any{
		"sampled_at": machine.SampledAt, "cpu_cores": machine.Cores,
		"total_memory_bytes": machine.TotalMemoryBytes, "available_memory_bytes": machine.AvailableMemoryBytes,
		"disk_total_bytes": machine.DiskTotalBytes, "disk_available_bytes": machine.DiskAvailableBytes,
		"active_kernel_count": machine.KernelCount, "busy_kernel_count": machine.BusyCount,
		"accelerator": gpu,
	}
}

func rankManagedEnvironmentPreflightCandidates(
	candidates []kernelruntime.ManagedEnvironment,
	preferredName string,
) {
	preferredName = strings.TrimSpace(preferredName)
	sort.SliceStable(candidates, func(left, right int) bool {
		leftPreferred := strings.EqualFold(strings.TrimSpace(candidates[left].Name), preferredName)
		rightPreferred := strings.EqualFold(strings.TrimSpace(candidates[right].Name), preferredName)
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		if len(candidates[left].Packages) != len(candidates[right].Packages) {
			return len(candidates[left].Packages) < len(candidates[right].Packages)
		}
		return candidates[left].Name < candidates[right].Name
	})
}

func managedEnvironmentPreflightCandidateSummary(
	environment kernelruntime.ManagedEnvironment,
	dependencies []string,
) map[string]any {
	dependencyNames := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if name := managedEnvironmentPackageBaseName(dependency); name != "" {
			dependencyNames[name] = struct{}{}
		}
	}
	matched := make([]string, 0, len(dependencyNames))
	for _, installed := range environment.Packages {
		if _, found := dependencyNames[managedEnvironmentPackageBaseName(installed)]; found {
			matched = append(matched, installed)
		}
	}
	sort.Strings(matched)
	return map[string]any{
		"name": environment.Name, "language": environment.Language, "status": environment.Status,
		"generation": environment.Generation, "spec_digest": environment.SpecDigest,
		"package_count": len(environment.Packages), "matched_packages": matched,
	}
}

func managedEnvironmentPackageBaseName(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.Index(value, "::"); index >= 0 {
		value = strings.TrimSpace(value[index+2:])
	}
	for index, character := range value {
		if strings.ContainsRune(" =<>!~[", character) {
			value = value[:index]
			break
		}
	}
	return strings.ToLower(strings.TrimSpace(value))
}
