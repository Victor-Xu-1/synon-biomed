package localconda

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"synon-go/internal/failurecontract"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/software"
)

const ProviderID = software.LocalProviderID

type EnvironmentManager interface {
	InspectManagedEnvironment(context.Context, string) (kernelruntime.ManagedEnvironment, bool, error)
	CreateManagedEnvironment(context.Context, kernelruntime.CreateManagedEnvironmentInput) (kernelruntime.ManagedEnvironment, error)
	VerifyManagedEnvironmentExecutable(string, string) error
	VerifyManagedEnvironmentImports(context.Context, string, []string) error
}

type managedEnvironmentInventory interface {
	ListManagedEnvironments(context.Context, kernelruntime.ManagedEnvironmentQuery) ([]kernelruntime.ManagedEnvironment, error)
}

// bundledPythonRuntime is the narrow optional capability used when a request
// explicitly reuses the service-owned immutable Python environment. Keeping
// it optional preserves the provider's existing fake/test managers and makes
// accidental package installation impossible for source-environment requests.
type bundledPythonRuntime interface {
	ManagedPythonEnvironmentName() string
	EnsureManagedPythonEnvironment(context.Context) error
	ManagedPythonActiveGeneration() (string, error)
}

type Provider struct {
	manager EnvironmentManager
}

var managedPackageSpec = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(?:\[[A-Za-z0-9_,.-]+\])?(?:\s*(?:==|!=|~=|>=|<=|>|<|=)\s*[A-Za-z0-9*+_.!:-]+(?:\s*,\s*(?:==|!=|~=|>=|<=|>|<|=)\s*[A-Za-z0-9*+_.!:-]+)*)?$`)
var softwareRuntimePackageName = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9._-]*)$`)

// isPythonStandardLibraryRequirement identifies dependency sentinels that
// describe the interpreter's bundled standard library rather than a package
// that a package manager can install. Treating these as external distributions
// would turn a valid inventory request into a guaranteed package-index failure.
func isPythonStandardLibraryRequirement(language string, requirement software.PackageRequirement) bool {
	if language != "python" || requirement.Manager != software.PackageManagerPip {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(requirement.Spec))
	if cut := strings.IndexAny(name, "[<>=!~"); cut >= 0 {
		name = strings.TrimSpace(name[:cut])
	}
	switch name {
	case "stdlib", "python-stdlib", "python_stdlib", "python-standard-library", "python_standard_library":
		return true
	default:
		return false
	}
}
func New(manager EnvironmentManager) (*Provider, error) {
	if manager == nil {
		return nil, errors.New("local conda provider requires a managed environment runtime")
	}
	return &Provider{manager: manager}, nil
}

func (p *Provider) Descriptor() software.ProviderDescriptor {
	return software.ProviderDescriptor{
		ID: ProviderID, Priority: 1000, Local: true,
		Languages:       []string{"python", "r", "native"},
		PackageManagers: []software.PackageManager{software.PackageManagerConda, software.PackageManagerPip},
		Capabilities:    []string{"*"},
	}
}

func (p *Provider) AdmitSoftwareRequest(input software.Request) error {
	request, err := software.NormalizeRequest(input)
	if err != nil {
		return err
	}
	if containsChannel(request.Channels, "defaults") && len(request.Channels) > 1 {
		return errors.New("local conda defaults channel cannot be mixed with another channel")
	}
	for index, requirement := range request.Packages {
		switch requirement.Manager {
		case software.PackageManagerConda:
		case software.PackageManagerPip:
			if request.Language != "python" {
				return errors.New("local conda pip packages require the python language")
			}
		default:
			return fmt.Errorf("local conda package %d uses an unsupported manager", index)
		}
		if !managedPackageSpec.MatchString(requirement.Spec) {
			return fmt.Errorf("local conda package %d has an invalid specification", index)
		}
	}
	return nil
}

func containsChannel(channels []string, expected string) bool {
	for _, channel := range channels {
		if strings.EqualFold(strings.TrimSpace(channel), expected) {
			return true
		}
	}
	return false
}

// SelectCompatibleEnvironment performs the inventory-first part of planning
// before durable approval. Exact request environments remain preferred. When
// they are absent, a verified software-runtime environment may be reused only
// for unversioned package requirements whose installed inventory, executable,
// and import witnesses all satisfy the request. Versioned or channel-pinned
// plans retain their exact content-addressed environment.
func (p *Provider) SelectCompatibleEnvironment(ctx context.Context, plan software.Plan) (software.Plan, error) {
	if p == nil || p.manager == nil || plan.ProviderID != ProviderID {
		return software.Plan{}, errors.New("local conda compatibility inventory is unavailable")
	}
	request, err := software.NormalizeRequest(plan.Request)
	if err != nil {
		return software.Plan{}, err
	}
	requestedEnvironment, err := software.EnvironmentName(ProviderID, request)
	if err != nil || plan.Environment != requestedEnvironment || plan.CompatibleReuse {
		return software.Plan{}, errors.New("local conda compatibility inventory received an invalid plan")
	}
	if request.SourceEnvironment != "" || len(request.Channels) != 0 {
		return plan, nil
	}
	dependencies, reusable := unversionedRequirementNames(request.Packages)
	if !reusable || len(dependencies) == 0 {
		return plan, nil
	}
	if _, found, inspectErr := p.manager.InspectManagedEnvironment(ctx, plan.Environment); found {
		// A present exact environment remains authoritative. Ensure will either
		// reuse it or repair its active generation instead of hiding corruption
		// behind a different compatible environment.
		return plan, nil
	} else if inspectErr != nil {
		return software.Plan{}, software.WrapOperationError(
			inspectErr, "installed_software_preflight_failed",
			fmt.Sprintf("installed software preflight failed: %v", inspectErr),
			"restore_the_managed_environment_inventory_then_retry_this_same_provider_plan", true,
		)
	}
	inventory, ok := p.manager.(managedEnvironmentInventory)
	if !ok {
		return plan, nil
	}
	environments, err := inventory.ListManagedEnvironments(ctx, kernelruntime.ManagedEnvironmentQuery{
		Language: "python", Dependencies: dependencies, IncludePackages: true,
		// Marker/pointer inventory is deliberately cheap and bounded. The
		// selected environment is health-checked by Ensure immediately before
		// execution; probing every historical environment here serializes one
		// Python smoke test per entry and can exhaust a legitimate tool timeout.
		SkipHealth: true,
	})
	if err != nil {
		return software.Plan{}, software.WrapOperationError(
			err, "installed_software_preflight_failed",
			fmt.Sprintf("installed software inventory failed: %v", err),
			"restore_the_managed_environment_inventory_then_retry_this_same_provider_plan", true,
		)
	}
	sort.SliceStable(environments, func(i, j int) bool {
		if len(environments[i].Packages) == len(environments[j].Packages) {
			return environments[i].Name < environments[j].Name
		}
		return len(environments[i].Packages) < len(environments[j].Packages)
	})
	importWitnesses := localCondaImportWitnesses(request)
	for _, environment := range environments {
		if environment.Name == requestedEnvironment || !strings.HasPrefix(environment.Name, "swr-") ||
			environment.Status != "ready" || !managedInventorySatisfies(environment.Packages, dependencies) {
			continue
		}
		if err := p.manager.VerifyManagedEnvironmentExecutable(environment.Name, request.Executable); err != nil {
			continue
		}
		if err := p.manager.VerifyManagedEnvironmentImports(ctx, environment.Name, importWitnesses); err != nil {
			continue
		}
		plan.Environment = environment.Name
		plan.CompatibleReuse = true
		return plan, nil
	}
	return plan, nil
}

func (p *Provider) Ensure(ctx context.Context, plan software.Plan, operationID string) (software.ProvisionReceipt, error) {
	if p == nil || p.manager == nil {
		return software.ProvisionReceipt{}, errors.New("local conda provider is unavailable")
	}
	if plan.ProviderID != ProviderID {
		return software.ProvisionReceipt{}, errors.New("local conda provider received a foreign plan")
	}
	request, err := software.NormalizeRequest(plan.Request)
	if err != nil {
		return software.ProvisionReceipt{}, err
	}
	if err := p.AdmitSoftwareRequest(request); err != nil {
		return software.ProvisionReceipt{}, err
	}
	importWitnesses := localCondaImportWitnesses(request)
	requestedEnvironment, err := software.EnvironmentName(ProviderID, request)
	if err != nil {
		return software.ProvisionReceipt{}, err
	}
	dependencies, reusable := unversionedRequirementNames(request.Packages)
	if plan.Environment != requestedEnvironment &&
		(!plan.CompatibleReuse || !reusable || len(request.Channels) != 0 || request.SourceEnvironment != "" ||
			!strings.HasPrefix(plan.Environment, "swr-")) {
		return software.ProvisionReceipt{}, errors.New("local conda compatible environment selection is invalid")
	}
	if request.SourceEnvironment != "" {
		return p.ensureSourceEnvironment(ctx, plan, request)
	}
	condaPackages := make([]string, 0, len(request.Packages)+1)
	pipPackages := make([]string, 0, len(request.Packages))
	for _, requirement := range request.Packages {
		if isPythonStandardLibraryRequirement(request.Language, requirement) {
			continue
		}
		switch requirement.Manager {
		case software.PackageManagerConda:
			condaPackages = append(condaPackages, requirement.Spec)
		case software.PackageManagerPip:
			pipPackages = append(pipPackages, requirement.Spec)
		default:
			return software.ProvisionReceipt{}, errors.New("local conda plan contains an unsupported package manager")
		}
	}
	if request.Language == "r" && !containsPackage(condaPackages, "r-base") {
		condaPackages = append([]string{"r-base"}, condaPackages...)
	}
	pipPhases := [][]string(nil)
	if len(pipPackages) != 0 {
		pipPhases = [][]string{pipPackages}
	}
	sourceEnvironment := ""
	if request.Language == "python" && len(condaPackages) == 0 && len(pipPackages) > 0 {
		if runtime, ok := p.manager.(bundledPythonRuntime); ok {
			sourceEnvironment = strings.TrimSpace(runtime.ManagedPythonEnvironmentName())
			if sourceEnvironment != "" {
				if err := runtime.EnsureManagedPythonEnvironment(ctx); err != nil {
					return software.ProvisionReceipt{}, software.WrapOperationError(
						err, "managed_python_runtime_unavailable",
						fmt.Sprintf("managed Python environment is unavailable: %v", err),
						"repair_or_prepare_the_service_owned_managed_python_environment_then_retry_the_same_provider_plan", true,
					)
				}
			}
		}
	}
	// A Python interpreter is the single governed command host for every local
	// software environment. Native and R programs still execute as their own
	// binaries; Python supplies the durable, confined argv launcher only.
	environment, found, err := p.manager.InspectManagedEnvironment(ctx, plan.Environment)
	if err != nil {
		if !found {
			return software.ProvisionReceipt{}, software.WrapOperationError(
				err, "installed_software_preflight_failed",
				fmt.Sprintf("installed software preflight failed: %v", err),
				"restore_the_managed_environment_inventory_then_retry_this_same_provider_plan", true,
			)
		}
	}
	if found && err == nil {
		if witnessErr := p.manager.VerifyManagedEnvironmentExecutable(environment.Name, request.Executable); witnessErr != nil {
			err = fmt.Errorf("executable witness failed: %w", witnessErr)
		} else if witnessErr := p.manager.VerifyManagedEnvironmentImports(ctx, environment.Name, importWitnesses); witnessErr != nil {
			err = fmt.Errorf("import witness failed: %w", witnessErr)
		}
	}
	disposition := software.ProvisionDispositionReused
	if !found || err != nil {
		if err != nil {
			// A present but unhealthy active generation is repaired through the
			// same immutable publication path. CreateManagedEnvironment holds the
			// per-environment lock, reuses any healthy exact generation, and only
			// atomically replaces the active pointer after full validation. The
			// invalid generation remains recoverable evidence; no in-place mutation
			// or alternate provider is attempted.
			disposition = software.ProvisionDispositionRepaired
		} else {
			disposition = software.ProvisionDispositionInstalled
		}
		environment, err = p.manager.CreateManagedEnvironment(ctx, kernelruntime.CreateManagedEnvironmentInput{
			Name: plan.Environment, Language: "python", SourceEnvironment: sourceEnvironment, Packages: condaPackages,
			Channels: request.Channels, PipPhases: pipPhases, ImportNames: importWitnesses,
			OperationID: strings.TrimSpace(operationID),
		})
		if err != nil {
			return software.ProvisionReceipt{}, localCondaProvisioningOperationError(err, disposition)
		}
	}
	if environment.Name != plan.Environment || environment.Generation == "" || environment.Status != "ready" {
		return software.ProvisionReceipt{}, errors.New("local conda environment activation receipt is invalid")
	}
	if plan.CompatibleReuse && !managedInventorySatisfies(environment.Packages, dependencies) {
		return software.ProvisionReceipt{}, errors.New("local conda compatible environment inventory no longer satisfies the request")
	}
	if err := p.manager.VerifyManagedEnvironmentExecutable(environment.Name, request.Executable); err != nil {
		return software.ProvisionReceipt{}, software.WrapOperationError(
			err, "software_executable_missing", fmt.Sprintf("executable witness failed: %v", err),
			"correct_the_declared_package_or_executable_in_this_same_provider_plan", true,
		)
	}
	if err := p.manager.VerifyManagedEnvironmentImports(ctx, environment.Name, importWitnesses); err != nil {
		return software.ProvisionReceipt{}, software.WrapOperationError(
			err, "software_import_missing", fmt.Sprintf("import witness failed: %v", err),
			"correct_the_declared_python_distribution_or_import_in_this_same_provider_plan", true,
		)
	}
	return software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: environment.Name, Generation: environment.Generation,
		Packages: append([]string(nil), environment.Packages...), Executable: request.Executable,
		Local: true, Verified: true, Preflight: true, Disposition: disposition,
	}, nil
}

func unversionedRequirementNames(requirements []software.PackageRequirement) ([]string, bool) {
	names := make([]string, 0, len(requirements))
	seen := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		if isPythonStandardLibraryRequirement("python", requirement) {
			continue
		}
		spec := strings.TrimSpace(requirement.Spec)
		match := softwareRuntimePackageName.FindStringSubmatch(spec)
		if len(match) != 2 || match[0] != spec {
			return nil, false
		}
		name := strings.ToLower(match[1])
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, true
}

func localCondaImportWitnesses(request software.Request) []string {
	witnesses := append([]string(nil), request.Imports...)
	if request.Language != "python" {
		return witnesses
	}
	seen := make(map[string]struct{}, len(witnesses)+1)
	for _, witness := range witnesses {
		seen[witness] = struct{}{}
	}
	for _, requirement := range request.Packages {
		if requirement.Manager != software.PackageManagerPip {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(requirement.Spec))
		if cut := strings.IndexAny(name, "[<>=!~"); cut >= 0 {
			name = strings.TrimSpace(name[:cut])
		}
		// Some Python distribution names do not match their import module.
		// These aliases are part of the declarative environment witness so an
		// immutable generation cannot activate merely because pip succeeded.
		alias := ""
		switch strings.NewReplacer("_", "-", ".", "-").Replace(name) {
		case "safe-mol":
			alias = "safe"
		}
		if alias == "" {
			continue
		}
		if _, found := seen[alias]; found {
			continue
		}
		seen[alias] = struct{}{}
		witnesses = append(witnesses, alias)
	}
	return witnesses
}

func managedInventorySatisfies(installed, dependencies []string) bool {
	for _, dependency := range dependencies {
		found := false
		for _, item := range installed {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item)), dependency+"=") {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (p *Provider) ensureSourceEnvironment(
	ctx context.Context,
	plan software.Plan,
	request software.Request,
) (software.ProvisionReceipt, error) {
	runtime, ok := p.manager.(bundledPythonRuntime)
	if !ok {
		return software.ProvisionReceipt{}, errors.New("local conda provider cannot reuse a managed Python environment")
	}
	if runtime.ManagedPythonEnvironmentName() != request.SourceEnvironment || plan.Environment != request.SourceEnvironment {
		return software.ProvisionReceipt{}, errors.New("source environment is not the service-owned managed Python environment")
	}
	if err := runtime.EnsureManagedPythonEnvironment(ctx); err != nil {
		return software.ProvisionReceipt{}, software.WrapOperationError(
			err, "managed_python_runtime_unavailable",
			fmt.Sprintf("managed Python environment is unavailable: %v", err),
			"repair_or_prepare_the_service_owned_managed_python_environment_then_retry_the_same_provider_plan", true,
		)
	}
	generation, err := runtime.ManagedPythonActiveGeneration()
	if err != nil || strings.TrimSpace(generation) == "" {
		if err == nil {
			err = errors.New("managed Python active generation is empty")
		}
		return software.ProvisionReceipt{}, software.WrapOperationError(
			err, "managed_python_generation_unavailable",
			fmt.Sprintf("managed Python generation is unavailable: %v", err),
			"repair_or_prepare_the_service_owned_managed_python_environment_then_retry_the_same_provider_plan", true,
		)
	}
	if err := p.manager.VerifyManagedEnvironmentExecutable(request.SourceEnvironment, request.Executable); err != nil {
		return software.ProvisionReceipt{}, software.WrapOperationError(
			err, "software_executable_missing", fmt.Sprintf("executable witness failed: %v", err),
			"correct_the_declared_executable_in_this_same_provider_plan", true,
		)
	}
	if err := p.manager.VerifyManagedEnvironmentImports(ctx, request.SourceEnvironment, request.Imports); err != nil {
		return software.ProvisionReceipt{}, software.WrapOperationError(
			err, "software_import_missing", fmt.Sprintf("import witness failed: %v", err),
			"repair_the_service_owned_python_distribution_or_import_in_this_same_provider_plan", true,
		)
	}
	return software.ProvisionReceipt{
		ProviderID: ProviderID, Environment: request.SourceEnvironment, Generation: generation,
		Executable: request.Executable, Local: true, Verified: true, Preflight: true,
		Disposition: software.ProvisionDispositionReused,
	}, nil
}

func containsPackage(packages []string, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, spec := range packages {
		lower := strings.ToLower(strings.TrimSpace(spec))
		if lower == name || strings.HasPrefix(lower, name+"=") || strings.HasPrefix(lower, name+" ") {
			return true
		}
	}
	return false
}

// localCondaProvisioningOperationError keeps the reference-runtime distinction
// between a repairable environment defect and an unavailable declarative
// dependency. A solver cannot make a package that is absent from the admitted
// channels appear by retrying; the agent must choose a registered capability or
// correct the spec after reading the documented environment contract.
func localCondaProvisioningOperationError(err error, disposition string) error {
	if err == nil {
		return nil
	}
	diagnostic := strings.TrimSpace(err.Error())
	if len(diagnostic) > 1600 {
		diagnostic = diagnostic[len(diagnostic)-1600:]
	}
	lower := strings.ToLower(diagnostic)
	if localCondaDependencyUnavailable(lower) {
		return &software.OperationError{
			Kind:        failurecontract.NotFound,
			Code:        "software_dependency_unavailable",
			Message:     fmt.Sprintf("declared software dependency is unavailable in the governed provider plan: %s", diagnostic),
			Recovery:    "choose_a_registered_capability_or_documented_provider_plan_with_an_available_dependency; otherwise_correct_the_declared_package_spec_or_channels; do_not_retry_the_same_unavailable_package",
			Retryable:   false,
			RepairScope: "same_task_registered_alternative",
			Cause:       err,
		}
	}
	code := "software_install_failed"
	message := "software installation failed"
	if disposition == software.ProvisionDispositionRepaired {
		code = "software_repair_failed"
		message = "software installation repair failed"
	}
	return software.WrapOperationError(
		err, code, fmt.Sprintf("%s: %s", message, diagnostic),
		"inspect_the_solver_diagnostic_then_correct_packages_or_channels_in_this_same_provider_plan", true,
	)
}

func localCondaDependencyUnavailable(diagnostic string) bool {
	if diagnostic == "" {
		return false
	}
	markers := []string{
		"packagesnotfounderror",
		"unsatisfiableerror",
		"could not solve for environment specs",
		"could not be installed",
		"not found in current channels",
		"no match found",
		"package does not exist",
		"packages do not exist",
	}
	for _, marker := range markers {
		if strings.Contains(diagnostic, marker) {
			return true
		}
	}
	return strings.Contains(diagnostic, "package") &&
		(strings.Contains(diagnostic, "not found") || strings.Contains(diagnostic, "does not exist"))
}
