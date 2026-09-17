package localconda

import (
	"context"
	"errors"
	"strings"
	"testing"

	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/software"
)

type fakeManager struct {
	input                kernelruntime.CreateManagedEnvironmentInput
	inspectResult        kernelruntime.ManagedEnvironment
	inspectFound         bool
	inspectErr           error
	inspectCalls         int
	createCalls          int
	witnessEnv           string
	witnessExec          string
	witnessImports       []string
	createErr            error
	witnessErr           error
	importWitnessErr     error
	recoverImportWitness bool
}

type inventoryManager struct {
	*fakeManager
	environments []kernelruntime.ManagedEnvironment
	inspect      map[string]kernelruntime.ManagedEnvironment
	listCalls    int
}

type fakeBundledManager struct {
	*fakeManager
	ensured bool
}

func (m *fakeBundledManager) ManagedPythonEnvironmentName() string { return "synon-biomed-python" }
func (m *fakeBundledManager) EnsureManagedPythonEnvironment(context.Context) error {
	m.ensured = true
	return nil
}
func (m *fakeBundledManager) ManagedPythonActiveGeneration() (string, error) {
	return "bundled-generation", nil
}

func (m *inventoryManager) InspectManagedEnvironment(_ context.Context, name string) (kernelruntime.ManagedEnvironment, bool, error) {
	m.inspectCalls++
	environment, found := m.inspect[name]
	return environment, found, nil
}

func (m *inventoryManager) ListManagedEnvironments(_ context.Context, query kernelruntime.ManagedEnvironmentQuery) ([]kernelruntime.ManagedEnvironment, error) {
	m.listCalls++
	if query.Language != "python" || !query.IncludePackages || !query.SkipHealth {
		return nil, errors.New("unexpected inventory query")
	}
	return append([]kernelruntime.ManagedEnvironment(nil), m.environments...), nil
}

func (m *fakeManager) InspectManagedEnvironment(_ context.Context, _ string) (kernelruntime.ManagedEnvironment, bool, error) {
	m.inspectCalls++
	return m.inspectResult, m.inspectFound, m.inspectErr
}

func (m *fakeManager) CreateManagedEnvironment(_ context.Context, input kernelruntime.CreateManagedEnvironmentInput) (kernelruntime.ManagedEnvironment, error) {
	m.createCalls++
	m.input = input
	if m.createErr != nil {
		return kernelruntime.ManagedEnvironment{}, m.createErr
	}
	return kernelruntime.ManagedEnvironment{Name: input.Name, Language: input.Language, Generation: "gen-1", Packages: input.Packages, Status: "ready"}, nil
}

func (m *fakeManager) VerifyManagedEnvironmentExecutable(environment, executable string) error {
	m.witnessEnv, m.witnessExec = environment, executable
	return m.witnessErr
}

func (m *fakeManager) VerifyManagedEnvironmentImports(_ context.Context, environment string, imports []string) error {
	m.witnessEnv = environment
	m.witnessImports = append([]string(nil), imports...)
	if m.recoverImportWitness && m.importWitnessErr != nil {
		err := m.importWitnessErr
		m.importWitnessErr = nil
		return err
	}
	return m.importWitnessErr
}

func TestProviderBuildsOneImmutablePythonHostedGeneration(t *testing.T) {
	manager := &fakeManager{}
	provider, err := New(manager)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	request := software.Request{
		Capability: "document-conversion", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{
			{Manager: software.PackageManagerConda, Spec: "pandoc"},
			{Manager: software.PackageManagerPip, Spec: "pypandoc==1.15"},
		},
		Channels: []string{"conda-forge"}, Imports: []string{"pypandoc"},
	}
	plan, err := controller.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := controller.Ensure(context.Background(), plan, "tool-call-1")
	if err != nil {
		t.Fatal(err)
	}
	if manager.input.Language != "python" || len(manager.input.Packages) != 1 || manager.input.Packages[0] != "pandoc" ||
		len(manager.input.PipPhases) != 1 || len(manager.input.PipPhases[0]) != 1 || manager.input.PipPhases[0][0] != "pypandoc==1.15" ||
		len(manager.input.ImportNames) != 1 || manager.input.ImportNames[0] != "pypandoc" ||
		len(manager.witnessImports) != 1 || manager.witnessImports[0] != "pypandoc" ||
		manager.witnessEnv != plan.Environment || manager.witnessExec != "python" || receipt.Generation != "gen-1" ||
		manager.inspectCalls != 1 || manager.createCalls != 1 || !receipt.Preflight ||
		receipt.Disposition != software.ProvisionDispositionInstalled {
		t.Fatalf("unexpected provider request/receipt: input=%#v receipt=%#v witness=%s/%s", manager.input, receipt, manager.witnessEnv, manager.witnessExec)
	}
}

func TestProviderDerivesSafeMolImportWitnessBeforeEnvironmentActivation(t *testing.T) {
	manager := &fakeManager{}
	provider, err := New(manager)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Resolve(software.Request{
		Capability: "genmol-molecule-generation", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{
			{Manager: software.PackageManagerPip, Spec: "safe-mol>=0.1.14"},
			{Manager: software.PackageManagerPip, Spec: "requests"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(context.Background(), plan, "safe-mol-import-witness"); err != nil {
		t.Fatal(err)
	}
	if len(manager.input.ImportNames) != 1 || manager.input.ImportNames[0] != "safe" ||
		len(manager.witnessImports) != 1 || manager.witnessImports[0] != "safe" {
		t.Fatalf("safe-mol import witness was not validated before activation: input=%#v witness=%#v",
			manager.input.ImportNames, manager.witnessImports)
	}
}

func TestProviderReusesVerifiedInstalledEnvironmentBeforeCreating(t *testing.T) {
	manager := &fakeManager{inspectFound: true, inspectResult: kernelruntime.ManagedEnvironment{
		Name: "placeholder", Language: "python", Generation: "existing-generation",
		Packages: []string{"python=3.13.2=h1=conda-forge", "xtb=6.7.1=h2=conda-forge"}, Status: "ready",
	}}
	provider, err := New(manager)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	request := software.Request{
		Capability: "semiempirical-quantum", Language: "native", Executable: "xtb",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "xtb"}},
	}
	plan, err := controller.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	manager.inspectResult.Name = plan.Environment
	receipt, err := controller.Ensure(context.Background(), plan, "tool-call-reuse")
	if err != nil {
		t.Fatal(err)
	}
	if manager.inspectCalls != 1 || manager.createCalls != 0 || receipt.Generation != "existing-generation" ||
		receipt.Disposition != software.ProvisionDispositionReused || !receipt.Preflight {
		t.Fatalf("installed environment was not reused: manager=%#v receipt=%#v", manager, receipt)
	}
}

func TestProviderClonesBundledPythonBeforeInstallingPipOnlyRequirements(t *testing.T) {
	base := &fakeManager{}
	manager := &fakeBundledManager{fakeManager: base}
	provider, err := New(manager)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Resolve(software.Request{
		Capability: "document-analysis", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "pypandoc"}},
		Imports:  []string{"pypandoc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := controller.Ensure(context.Background(), plan, "pip-only-clone")
	if err != nil {
		t.Fatal(err)
	}
	if !manager.ensured || base.input.SourceEnvironment != "synon-biomed-python" ||
		len(base.input.Packages) != 0 || len(base.input.PipPhases) != 1 ||
		len(base.input.PipPhases[0]) != 1 || base.input.PipPhases[0][0] != "pypandoc" ||
		receipt.Disposition != software.ProvisionDispositionInstalled {
		t.Fatalf("pip-only clone input=%#v receipt=%#v ensured=%t", base.input, receipt, manager.ensured)
	}
}

func TestProviderSkipsPythonStandardLibrarySentinelBeforeProvisioning(t *testing.T) {
	manager := &fakeManager{}
	provider, err := New(manager)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Resolve(software.Request{
		Capability: "report-validation", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "stdlib>=3.12"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := controller.Ensure(context.Background(), plan, "stdlib-sentinel")
	if err != nil || manager.createCalls != 1 || len(manager.input.Packages) != 0 || len(manager.input.PipPhases) != 0 || receipt.Disposition != software.ProvisionDispositionInstalled {
		t.Fatalf("standard-library sentinel reached package installation: input=%#v receipt=%#v err=%v", manager.input, receipt, err)
	}
}

func TestProviderSelectsCompatibleInstalledSupersetBeforeInstallation(t *testing.T) {
	installed := kernelruntime.ManagedEnvironment{
		Name: "swr-existing", Language: "python", Generation: "existing-generation", Status: "ready",
		Packages: []string{
			"matplotlib=3.11.1=pypi_0=pypi", "numpy=2.4.6=pypi_0=pypi",
			"pillow=12.3.0=pypi_0=pypi", "scipy=1.17.1=pypi_0=pypi",
		},
	}
	manager := &inventoryManager{
		fakeManager:  &fakeManager{},
		environments: []kernelruntime.ManagedEnvironment{installed},
		inspect:      map[string]kernelruntime.ManagedEnvironment{"swr-existing": installed},
	}
	provider, err := New(manager)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Resolve(software.Request{
		Capability: "pk-model", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{
			{Manager: software.PackageManagerPip, Spec: "numpy"},
			{Manager: software.PackageManagerPip, Spec: "scipy"},
			{Manager: software.PackageManagerPip, Spec: "matplotlib"},
			{Manager: software.PackageManagerPip, Spec: "pillow"},
		},
		Imports: []string{"numpy", "scipy", "matplotlib", "PIL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestedEnvironment := plan.Environment
	plan, err = provider.SelectCompatibleEnvironment(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CompatibleReuse || plan.Environment != "swr-existing" || plan.Environment == requestedEnvironment || manager.listCalls != 1 {
		t.Fatalf("compatible inventory selection=%#v manager=%#v", plan, manager)
	}
	receipt, err := controller.Ensure(context.Background(), plan, "tool-call-compatible-reuse")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Environment != "swr-existing" || receipt.Generation != "existing-generation" ||
		receipt.Disposition != software.ProvisionDispositionReused || manager.createCalls != 0 {
		t.Fatalf("compatible environment was not reused: receipt=%#v manager=%#v", receipt, manager)
	}
}

func TestProviderKeepsVersionedRequestOnExactEnvironment(t *testing.T) {
	manager := &inventoryManager{fakeManager: &fakeManager{}, inspect: map[string]kernelruntime.ManagedEnvironment{}}
	provider, _ := New(manager)
	controller, _ := software.NewController(provider)
	plan, err := controller.Resolve(software.Request{
		Capability: "versioned-analysis", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "numpy>=2.0"}},
		Imports:  []string{"numpy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := provider.SelectCompatibleEnvironment(context.Background(), plan)
	if err != nil || selected.Environment != plan.Environment || selected.CompatibleReuse || manager.listCalls != 0 {
		t.Fatalf("versioned request selected a guessed superset: selected=%#v err=%v manager=%#v", selected, err, manager)
	}
}

func TestProviderRepairsPresentUnhealthyInstallationThroughSameImmutablePlan(t *testing.T) {
	manager := &fakeManager{inspectFound: true, inspectErr: errors.New("corrupt active generation")}
	provider, _ := New(manager)
	controller, _ := software.NewController(provider)
	request := software.Request{
		Capability: "semiempirical-quantum", Language: "native", Executable: "xtb",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "xtb"}},
	}
	plan, err := controller.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := controller.Ensure(context.Background(), plan, "tool-call-corrupt")
	if err != nil || manager.inspectCalls != 1 || manager.createCalls != 1 ||
		receipt.Disposition != software.ProvisionDispositionRepaired || !receipt.Preflight {
		t.Fatalf("unhealthy installation was not repaired by the same plan: calls=%d receipt=%#v err=%v", manager.createCalls, receipt, err)
	}
}

func TestProviderRepairsPresentEnvironmentAfterImportWitnessFailure(t *testing.T) {
	manager := &fakeManager{
		inspectFound: true,
		inspectResult: kernelruntime.ManagedEnvironment{
			Name: "placeholder", Language: "python", Generation: "broken-generation", Status: "ready",
		},
		importWitnessErr: errors.New("safe import is incompatible"), recoverImportWitness: true,
	}
	provider, _ := New(manager)
	controller, _ := software.NewController(provider)
	plan, err := controller.Resolve(software.Request{
		Capability: "genmol-molecule-generation", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "safe-mol>=0.1.14"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := controller.Ensure(context.Background(), plan, "repair-safe-import")
	if err != nil || manager.createCalls != 1 || receipt.Disposition != software.ProvisionDispositionRepaired ||
		len(manager.input.ImportNames) != 1 || manager.input.ImportNames[0] != "safe" {
		t.Fatalf("import-incompatible environment was not atomically repaired: calls=%d input=%#v receipt=%#v err=%v",
			manager.createCalls, manager.input, receipt, err)
	}
}

func TestProviderDoesNotInstallWhenPreflightInventoryIsUnavailable(t *testing.T) {
	manager := &fakeManager{inspectFound: false, inspectErr: errors.New("inventory unavailable")}
	provider, _ := New(manager)
	controller, _ := software.NewController(provider)
	request := software.Request{
		Capability: "semiempirical-quantum", Language: "native", Executable: "xtb",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "xtb"}},
	}
	plan, err := controller.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(context.Background(), plan, "tool-call-unavailable"); err == nil ||
		!strings.Contains(err.Error(), "installed software preflight failed") || manager.createCalls != 0 {
		t.Fatalf("unavailable preflight was hidden by an install: calls=%d err=%v", manager.createCalls, err)
	}
}

func TestProviderDoesNotHideImportWitnessFailure(t *testing.T) {
	manager := &fakeManager{importWitnessErr: errors.New("missing import")}
	provider, _ := New(manager)
	controller, _ := software.NewController(provider)
	request := software.Request{
		Capability: "table-analysis", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "polars"}},
		Imports:  []string{"polars"},
	}
	plan, err := controller.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(context.Background(), plan, "tool-call-import"); err == nil {
		t.Fatal("import witness failure was hidden")
	}
}

func TestProviderDoesNotHideExecutableWitnessFailure(t *testing.T) {
	manager := &fakeManager{witnessErr: errors.New("missing binary")}
	provider, _ := New(manager)
	controller, _ := software.NewController(provider)
	request := software.Request{
		Capability: "sequence-alignment", Language: "native", Executable: "minimap2",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "minimap2"}},
	}
	plan, err := controller.Resolve(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Ensure(context.Background(), plan, "tool-call-2"); err == nil {
		t.Fatal("executable witness failure was hidden")
	}
}

func TestProviderClassifiesUnavailableSolverDependencyAsNonRetryableAlternative(t *testing.T) {
	manager := &fakeManager{
		createErr: errors.New("managed environment operation failed: PackagesNotFoundError: The following packages are not available from current channels: autodocktools"),
	}
	provider, err := New(manager)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Resolve(software.Request{
		Capability: "receptor-preparation", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "autodocktools"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Ensure(context.Background(), plan, "solver-unavailable")
	if err == nil {
		t.Fatal("unavailable dependency was reported as a successful installation")
	}
	var operationErr *software.OperationError
	if !errors.As(err, &operationErr) {
		t.Fatalf("solver error lost its structured operation contract: %v", err)
	}
	if operationErr.Code != "software_dependency_unavailable" || operationErr.Retryable ||
		operationErr.RepairScope != "same_task_registered_alternative" ||
		!strings.Contains(operationErr.Recovery, "registered_capability") {
		t.Fatalf("unexpected unavailable dependency contract: %#v", operationErr)
	}
}

func TestLocalCondaDependencyUnavailableRecognizesSolverDiagnosticsOnly(t *testing.T) {
	for _, diagnostic := range []string{
		"PackagesNotFoundError: package does not exist",
		"LibMambaUnsatisfiableError: could not solve for environment specs",
		"pip: Could not be installed because no matching distribution was found",
	} {
		if !localCondaDependencyUnavailable(strings.ToLower(diagnostic)) {
			t.Fatalf("solver diagnostic was not classified as unavailable: %q", diagnostic)
		}
	}
	if localCondaDependencyUnavailable("managed environment operation failed: permission denied") {
		t.Fatal("permission failure was misclassified as package unavailability")
	}
}

func TestProviderRejectsUnsupportedManagerLanguageCombinationDuringResolution(t *testing.T) {
	provider, err := New(&fakeManager{})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	request := software.Request{
		Capability: "statistical-modeling", Language: "r", Executable: "Rscript",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerPip, Spec: "numpy==2.3.2"}},
	}
	if _, err := controller.Resolve(request); !errors.Is(err, software.ErrNoProvider) {
		t.Fatalf("unsupported provider combination was admitted: %v", err)
	}
}

func TestProviderRejectsMixedDefaultsChannelDuringResolution(t *testing.T) {
	provider, err := New(&fakeManager{})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Resolve(software.Request{
		Capability: "mixed-channels", Language: "python", Executable: "python",
		Packages: []software.PackageRequirement{{Manager: software.PackageManagerConda, Spec: "numpy"}},
		Channels: []string{"conda-forge", "defaults"}, Imports: []string{"numpy"},
	})
	if !errors.Is(err, software.ErrNoProvider) {
		t.Fatalf("mixed defaults channel was admitted: %v", err)
	}
}

type bundledRuntimeManager struct {
	*fakeManager
	ensureCalls int
}

func (m *bundledRuntimeManager) ManagedPythonEnvironmentName() string {
	return "synon-biomed-python"
}

func (m *bundledRuntimeManager) EnsureManagedPythonEnvironment(context.Context) error {
	m.ensureCalls++
	return nil
}

func (m *bundledRuntimeManager) ManagedPythonActiveGeneration() (string, error) {
	return "bundled-generation", nil
}

func TestProviderReusesServiceOwnedPythonEnvironmentWithoutInstallation(t *testing.T) {
	manager := &bundledRuntimeManager{fakeManager: &fakeManager{}}
	provider, err := New(manager)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := software.NewController(provider)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controller.Resolve(software.Request{
		Capability:        "structure-energy-minimization",
		Language:          "python",
		SourceEnvironment: "synon-biomed-python",
		Imports:           []string{"rdkit"},
		Executable:        "python",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := controller.Ensure(context.Background(), plan, "structure-minimization-test")
	if err != nil {
		t.Fatal(err)
	}
	if manager.ensureCalls != 1 || manager.inspectCalls != 0 || manager.createCalls != 0 ||
		manager.witnessEnv != "synon-biomed-python" || manager.witnessExec != "python" ||
		receipt.Environment != "synon-biomed-python" || receipt.Generation != "bundled-generation" ||
		receipt.Disposition != software.ProvisionDispositionReused || !receipt.Verified || !receipt.Preflight {
		t.Fatalf("bundled runtime was not reused as one governed source environment: manager=%#v receipt=%#v", manager, receipt)
	}
}
