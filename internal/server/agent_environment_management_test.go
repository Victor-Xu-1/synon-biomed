package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/compute"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/skills"
)

type recordingManagedEnvironmentAuthority struct {
	mu sync.Mutex

	listQuery          kernelruntime.ManagedEnvironmentQuery
	createInput        kernelruntime.CreateManagedEnvironmentInput
	installInput       kernelruntime.MutateManagedPackagesInput
	uninstallInput     kernelruntime.MutateManagedPackagesInput
	registerInput      kernelruntime.RegisterManagedEnvironmentInput
	inspectName        string
	witnessNames       []string
	witnessEnvironment string
	witnessErr         error
	witnessCalls       int
	witnessErrAfter    int
	deleteName         string

	listResult   []kernelruntime.ManagedEnvironment
	mutateResult kernelruntime.ManagedEnvironment
	err          error
	mutateErr    error
	started      chan struct{}
	release      chan struct{}
	hadDeadline  bool
	mutations    int
}

func (a *recordingManagedEnvironmentAuthority) VerifyManagedEnvironmentImports(_ context.Context, environment string, names []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.witnessNames = append([]string(nil), names...)
	a.witnessEnvironment = environment
	a.witnessCalls++
	if a.witnessCalls <= a.witnessErrAfter {
		return nil
	}
	return a.witnessErr
}

func TestManagedEnvironmentReuseRequiresRequestedRuntimeWitness(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	for _, language := range []string{"r", "python"} {
		authority := &recordingManagedEnvironmentAuthority{
			listResult:   []kernelruntime.ManagedEnvironment{{Name: "existing", Language: language, Status: "ready", Generation: "verified-generation", Packages: []string{"fixture=1.0=build=conda"}}},
			mutateResult: kernelruntime.ManagedEnvironment{Name: "existing", Language: language, Status: "ready"},
		}
		input := map[string]any{"mode": "create", "name": "requested", "language": language, "packages": []any{"fixture"}, "import_names": []any{"FixtureNamespace"}, "human_description": "Prepare analysis"}
		result, err := server.executeAgentEnvironmentManagementToolWithAuthority(context.Background(), identity, agentruntime.ToolCall{ID: "reuse-" + language}, manageEnvironmentsToolName, input, authority)
		if err != nil || stringValue(mapValue(result)["mode"]) != "reuse" || authority.witnessEnvironment != "existing" || !reflect.DeepEqual(authority.witnessNames, []string{"FixtureNamespace"}) {
			t.Fatalf("witness bypass: result=%v err=%v names=%v", result, err, authority.witnessNames)
		}
		authority.witnessErr = errors.New("runtime namespace unavailable")
		// Preflight can succeed immediately before a payload becomes invalid.
		// The reuse boundary must revalidate through the same authority.
		authority.witnessErrAfter = authority.witnessCalls + 1
		if _, err := server.executeAgentEnvironmentManagementToolWithAuthority(context.Background(), identity, agentruntime.ToolCall{ID: "reject-" + language}, manageEnvironmentsToolName, input, authority); err == nil {
			t.Fatal("failed runtime witness reported reuse success")
		}
	}
}

func (a *recordingManagedEnvironmentAuthority) ListManagedEnvironments(
	_ context.Context,
	query kernelruntime.ManagedEnvironmentQuery,
) ([]kernelruntime.ManagedEnvironment, error) {
	a.mu.Lock()
	a.listQuery = query
	a.mu.Unlock()
	return append([]kernelruntime.ManagedEnvironment(nil), a.listResult...), a.err
}

func (a *recordingManagedEnvironmentAuthority) CreateManagedEnvironment(
	ctx context.Context,
	input kernelruntime.CreateManagedEnvironmentInput,
) (kernelruntime.ManagedEnvironment, error) {
	a.mu.Lock()
	a.createInput = input
	a.mu.Unlock()
	return a.finishMutation(ctx)
}

func (a *recordingManagedEnvironmentAuthority) InspectManagedEnvironment(
	_ context.Context,
	name string,
) (kernelruntime.ManagedEnvironment, bool, error) {
	a.mu.Lock()
	a.inspectName = name
	a.mu.Unlock()
	if a.err != nil {
		return kernelruntime.ManagedEnvironment{}, false, a.err
	}
	result := a.mutateResult
	if result.Name == "" {
		result.Name = name
	}
	return result, true, nil
}

func (a *recordingManagedEnvironmentAuthority) RegisterManagedEnvironment(
	ctx context.Context,
	input kernelruntime.RegisterManagedEnvironmentInput,
) (kernelruntime.ManagedEnvironment, error) {
	a.mu.Lock()
	a.registerInput = input
	a.mu.Unlock()
	return a.finishMutation(ctx)
}

func (a *recordingManagedEnvironmentAuthority) DeleteManagedEnvironment(_ context.Context, input kernelruntime.DeleteManagedEnvironmentInput) error {
	a.mu.Lock()
	a.deleteName = input.Name
	a.mu.Unlock()
	return a.err
}

func (a *recordingManagedEnvironmentAuthority) InstallManagedPackages(
	ctx context.Context,
	input kernelruntime.MutateManagedPackagesInput,
) (kernelruntime.ManagedEnvironment, error) {
	a.mu.Lock()
	a.installInput = input
	a.mu.Unlock()
	return a.finishMutation(ctx)
}

func (a *recordingManagedEnvironmentAuthority) UninstallManagedPackages(
	ctx context.Context,
	input kernelruntime.MutateManagedPackagesInput,
) (kernelruntime.ManagedEnvironment, error) {
	a.mu.Lock()
	a.uninstallInput = input
	a.mu.Unlock()
	return a.finishMutation(ctx)
}

func (a *recordingManagedEnvironmentAuthority) finishMutation(ctx context.Context) (kernelruntime.ManagedEnvironment, error) {
	a.mu.Lock()
	a.mutations++
	_, a.hadDeadline = ctx.Deadline()
	a.mu.Unlock()
	if a.started != nil {
		select {
		case <-a.started:
		default:
			close(a.started)
		}
	}
	if a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
			return kernelruntime.ManagedEnvironment{}, ctx.Err()
		}
	}
	if a.mutateErr != nil {
		return a.mutateResult, a.mutateErr
	}
	return a.mutateResult, a.err
}

func managedEnvironmentToolFixture(t *testing.T) (*Server, *agentKernelContext) {
	t.Helper()
	store, _, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "owner-env", "project-env", "frame-env")
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), "frame-env")
	if err != nil || !found {
		t.Fatalf("resolve environment tool frame found=%v err=%v", found, err)
	}
	root := t.TempDir()
	manager := kernelruntime.NewManager(kernelruntime.Config{CondaEnvsPath: filepath.Join(root, "envs")})
	return &Server{
		workspaceStore: store, kernelManager: manager,
		hostGPUDetector: func(context.Context) compute.GPUInfo { return compute.UnavailableGPUInfo() },
	}, &agentKernelContext{access: access, workspaceDir: root}
}

func managedEnvironmentTestResources() map[string]any {
	return map[string]any{
		"min_cpu_cores": 1, "min_memory_mb": 256, "min_disk_mb": 256, "accelerator": "none",
	}
}

func TestManagedEnvironmentCreateRequiresExactAnsweredScientificImplementation(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{
		Name: "generation", Language: "python", Status: "ready",
	}}
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Generate candidates with a professional pocket-conditioned engine.",
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation"},
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)

	missing, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		ctx, identity, agentruntime.ToolCall{ID: "missing-implementation"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "preflight", "name": "generation", "packages": []any{"framework", "validator"},
			"resource_requirements": managedEnvironmentTestResources(), "human_description": "Check generation environment",
		}, authority,
	)
	if err != nil || stringValue(mapValue(missing)["status"]) != "implementation_identity_required" {
		t.Fatalf("missing implementation result=%#v err=%v", missing, err)
	}

	blocked, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		ctx, identity, agentruntime.ToolCall{ID: "unanswered-implementation"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "create", "implementation": "Scientific Engine A", "name": "generation",
			"packages": []any{"scientific-engine-a"}, "resource_requirements": managedEnvironmentTestResources(),
			"human_description": "Create generation environment",
		}, authority,
	)
	if err != nil || stringValue(mapValue(blocked)["status"]) != "implementation_selection_required" ||
		authority.createInput.Name != "" {
		t.Fatalf("unanswered implementation result=%#v create=%#v err=%v", blocked, authority.createInput, err)
	}

	run.setSelectedImplementations("Scientific Engine A")
	allowed, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		ctx, identity, agentruntime.ToolCall{ID: "answered-implementation"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "create", "implementation": "Scientific Engine A", "name": "generation",
			"packages": []any{"scientific-engine-a"}, "resource_requirements": managedEnvironmentTestResources(),
			"human_description": "Create generation environment",
		}, authority,
	)
	if err != nil || stringValue(mapValue(allowed)["status"]) != "completed" || authority.createInput.Name != "generation" {
		t.Fatalf("answered implementation result=%#v create=%#v err=%v", allowed, authority.createInput, err)
	}
}

func TestManagedEnvironmentPackageAuthorityRepairsCommonPipSourceForms(t *testing.T) {
	got := normalizeAgentManagedEnvironmentPackageAuthorities([]string{
		"pip:https://example.invalid/engine.whl",
		"conda-forge::rdkit",
	})
	want := []string{
		"pip::https://example.invalid/engine.whl",
		"conda-forge::rdkit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized package authorities=%v want=%v", got, want)
	}
}

func TestManagedEnvironmentBareRepositoryRequiresExplicitInstallContract(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "source-contract"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "create", "name": "scientific-engine", "packages": []any{"git+https://example.org/engine.git"},
			"human_description": "Preparing scientific engine",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "package_source_contract_required" ||
		mapValue(result)["executed"] != false || authority.createInput.Name != "" {
		t.Fatalf("source contract result=%#v create=%#v err=%v", result, authority.createInput, err)
	}
}

func TestManagedEnvironmentCreatePropagatesPhasedWheelAndAcceleratorContract(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	server.hostGPUDetector = func(context.Context) compute.GPUInfo {
		memory := int64(8192)
		name := "test GPU"
		return compute.GPUInfo{Available: true, GPUMemoryMB: &memory, GPUCount: 1, GPUName: &name}
	}
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{
		Name: "generation", Language: "python", Status: "ready",
	}}
	run := &sessionRunnerChatRun{TaskIntent: "Use the verified wheel sources https://data.pyg.org/whl/torch-2.4.0+cu121.html and https://download.pytorch.org/whl/cu121"}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	resources := map[string]any{
		"min_cpu_cores": 2, "min_memory_mb": 4096, "min_disk_mb": 8192,
		"accelerator": "required", "min_accelerator_memory_mb": 2048,
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		ctx, identity, agentruntime.ToolCall{ID: "phased-create"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "create", "name": "generation", "implementation": "Scientific Engine",
			"packages": []any{"pytorch::pytorch=2.4"}, "channels": []any{"pytorch", "pyg"},
			"pip_phases": []any{[]any{"torch-scatter", "torch-geometric"}},
			"pip_args":   []any{"--no-build-isolation"},
			// Supplying an HTML wheel matrix as an extra index is a common
			// model-side mix-up. The boundary must repair its semantic kind
			// before the immutable environment operation is created.
			"pip_extra_index_urls": []any{
				"https://data.pyg.org/whl/torch-2.4.0+cu121.html",
				"https://download.pytorch.org/whl/cu121",
			},
			"import_names":          []any{"torch", "torch_geometric"},
			"resource_requirements": resources, "human_description": "Creating scientific environment",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "completed" {
		t.Fatalf("phased create result=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(authority.createInput.Packages, []string{"pytorch::pytorch=2.4"}) ||
		!reflect.DeepEqual(authority.createInput.PipPhases, [][]string{{"torch-scatter", "torch-geometric"}}) ||
		!reflect.DeepEqual(authority.createInput.PipFindLinks, []string{"https://data.pyg.org/whl/torch-2.4.0+cu121.html"}) ||
		!reflect.DeepEqual(authority.createInput.PipExtraIndexURLs, []string{"https://download.pytorch.org/whl/cu121"}) ||
		!reflect.DeepEqual(authority.createInput.ImportNames, []string{"torch", "torch_geometric"}) ||
		authority.createInput.RequiredAccelerator != "required" {
		t.Fatalf("phased create input=%#v", authority.createInput)
	}
	if authority.listQuery.RequiredAccelerator != "required" {
		t.Fatalf("preflight accelerator query=%#v", authority.listQuery)
	}
}

func TestManagedEnvironmentNormalizesEmbeddedFindLinksWithoutExecutingShell(t *testing.T) {
	packages, links, err := normalizeManagedEmbeddedPipFindLinks([]string{
		"pip::torch-scatter==2.1.2 --find-links https://data.pyg.org/whl/torch-2.4.0+cu121.html",
		"pip::torch-geometric==2.5.3",
	}, nil)
	if err != nil || !reflect.DeepEqual(packages, []string{"pip::torch-scatter==2.1.2", "pip::torch-geometric==2.5.3"}) ||
		!reflect.DeepEqual(links, []string{"https://data.pyg.org/whl/torch-2.4.0+cu121.html"}) {
		t.Fatalf("embedded find-links packages=%#v links=%#v err=%v", packages, links, err)
	}
	if _, _, err := normalizeManagedEmbeddedPipFindLinks([]string{"pip::pkg --find-links one two"}, nil); err == nil {
		t.Fatal("ambiguous embedded find-links was accepted")
	}
}

func TestManagedPipSourceKindsMoveHTMLWheelPagesOutOfExtraIndexes(t *testing.T) {
	findLinks, extraIndexes := normalizeManagedPipSourceKinds(
		[]string{"https://wheels.example.org/release/"},
		[]string{
			"https://data.pyg.org/whl/torch-2.4.0+cu121.html",
			"https://packages.example.org/simple",
		},
	)
	if !reflect.DeepEqual(findLinks, []string{
		"https://data.pyg.org/whl/torch-2.4.0+cu121.html",
		"https://wheels.example.org/release/",
	}) || !reflect.DeepEqual(extraIndexes, []string{"https://packages.example.org/simple"}) {
		t.Fatalf("normalized pip sources find_links=%#v extra_indexes=%#v", findLinks, extraIndexes)
	}
}

func TestManagedPackageInstallRepairsHTMLExtraIndexBeforeMutation(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{
		Name: "generation", Language: "python", Status: "ready",
	}}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "repair-wheel-source"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "generation", "packages": []any{"pip::torch-scatter"},
			"use_pip": true, "human_description": "Installing a verified binary extension",
			"pip_extra_index_urls": []any{
				"https://data.pyg.org/whl/torch-2.4.0+cu121.html",
				"https://download.pytorch.org/whl/cu121",
			},
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "completed" ||
		!reflect.DeepEqual(authority.installInput.PipFindLinks, []string{"https://data.pyg.org/whl/torch-2.4.0+cu121.html"}) ||
		!reflect.DeepEqual(authority.installInput.PipExtraIndexURLs, []string{"https://download.pytorch.org/whl/cu121"}) {
		t.Fatalf("wheel source repair result=%#v input=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedEnvironmentPreflightDoesNotTreatSourceURLAsInstalledDistribution(t *testing.T) {
	dependencies, hasSource := managedEnvironmentPreflightDependencyNames([]string{
		"python=3.10", "pip::git+https://github.com/example/scientific-engine.git", "rdkit",
	})
	if !hasSource || !reflect.DeepEqual(dependencies, []string{"python=3.10", "rdkit"}) {
		t.Fatalf("dependencies=%v hasSource=%t", dependencies, hasSource)
	}
}

func withManagedEnvironmentTestPreflight(
	t *testing.T,
	server *Server,
	identity *agentKernelContext,
	authority managedEnvironmentAuthority,
	toolName string,
	input map[string]any,
) map[string]any {
	t.Helper()
	preflight := make(map[string]any, len(input)+2)
	for key, value := range input {
		preflight[key] = value
	}
	preflight["mode"] = "preflight"
	preflight["human_description"] = "Checking task and machine requirements"
	preflight["resource_requirements"] = managedEnvironmentTestResources()
	delete(preflight, "background")
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "preflight-call"}, toolName, preflight, authority,
	)
	if err != nil {
		t.Fatalf("environment preflight failed: %v", err)
	}
	if !compatibilityPlanBool(mapValue(result)["feasible"]) {
		t.Fatalf("environment preflight did not recommend the feasible local route: %#v", result)
	}
	mutation := make(map[string]any, len(input)+2)
	for key, value := range input {
		mutation[key] = value
	}
	return mutation
}

func TestManagedEnvironmentToolSchemasMatchTheSingleHarnessFlow(t *testing.T) {
	schemas := agentEnvironmentManagementToolSchemas()
	if len(schemas) != 2 || schemas[0].Name != manageEnvironmentsToolName || schemas[1].Name != managePackagesToolName {
		t.Fatalf("managed environment schema order=%#v", agentRuntimeToolSchemaNames(schemas))
	}
	for _, required := range []string{
		"first substantial compute or engine setup",
		"exact implementation selection",
		"compatible ready environment",
		"recoverable decision result",
	} {
		if !strings.Contains(schemas[0].Description, required) {
			t.Fatalf("manage_environments guidance missing %q: %s", required, schemas[0].Description)
		}
	}

	environmentsValidator := compileAgentRuntimeMCPValidator(schemas[0])
	for _, input := range []map[string]any{
		{"mode": "list", "dependencies": []any{"scanpy", "anndata"}, "human_description": "Listing scanpy environments"},
		{"mode": "preflight", "name": "scanpy", "packages": []any{"scanpy", "anndata", "pip::harmonypy"}, "resource_requirements": managedEnvironmentTestResources(), "human_description": "Checking scanpy requirements"},
		{"mode": "create", "name": "scanpy", "python_version": "3.13", "packages": []any{"scanpy", "anndata", "pip::harmonypy"}, "human_description": "Creating scanpy environment", "background": true},
		{"mode": "create", "name": "r-seurat", "language": "r", "packages": []any{"r-seurat"}, "channels": []any{"conda-forge"}, "human_description": "Creating Seurat environment"},
		{"mode": "preflight", "provider": "local-container", "image": "registry.example/science/tool:1.0", "resource_requirements": managedEnvironmentTestResources(), "human_description": "Checking container runtime"},
		{"mode": "create", "provider": "local-container", "image": "registry.example/science/tool:1.0", "network": "egress", "human_description": "Preparing container environment", "background": true},
	} {
		if value := environmentsValidator.Validate(input); value != nil {
			t.Fatalf("valid manage_environments input rejected: input=%#v result=%#v", input, value)
		}
	}
	if value := environmentsValidator.Validate(map[string]any{
		"mode": "create", "image": "registry.example/science/tool:1.0",
		"human_description": "Preparing ambiguous environment",
	}); value == nil || value["code"] != "invalid_tool_arguments" {
		t.Fatalf("container image without explicit provider was admitted: %#v", value)
	}
	if value := environmentsValidator.Validate(map[string]any{
		"mode": "create", "name": "bad", "packages": []any{"numpy"}, "command": "pip install numpy",
		"human_description": "Creating unsafe environment",
	}); value == nil || value["code"] != "invalid_tool_arguments" {
		t.Fatalf("manage_environments admitted a competing command path: %#v", value)
	}

	packagesValidator := compileAgentRuntimeMCPValidator(schemas[1])
	if value := packagesValidator.Validate(map[string]any{
		"mode": "preflight", "environment": "scanpy", "packages": []any{"harmonypy"},
		"resource_requirements": managedEnvironmentTestResources(), "human_description": "Checking Harmony requirements",
	}); value != nil {
		t.Fatalf("valid manage_packages preflight rejected: %#v", value)
	}
	if value := packagesValidator.Validate(map[string]any{
		"mode": "install", "environment": "scanpy", "packages": []any{"harmonypy"}, "use_pip": true,
		"human_description": "Installing Harmony package",
	}); value != nil {
		t.Fatalf("valid manage_packages input rejected: %#v", value)
	}
	if value := packagesValidator.Validate(map[string]any{
		"mode": "uninstall", "environment": "scanpy", "packages": []any{"numpy"},
		"human_description": "Removing numpy package",
	}); value != nil {
		t.Fatalf("manage_packages rejected canonical uninstall: %#v", value)
	}
}

func TestManagedEnvironmentCreateAlwaysRunsGenericPreflight(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		mutateResult: kernelruntime.ManagedEnvironment{Name: "analysis", Language: "python", Status: "ready"},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "advisory-preflight"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "preflight", "name": "analysis", "packages": []any{"example-library"},
			"resource_requirements": map[string]any{
				"min_cpu_cores": 4096, "min_memory_mb": 256, "min_disk_mb": 256, "accelerator": "required",
			},
			"human_description": "Comparing task needs with the current machine",
		}, authority,
	)
	resultMap := mapValue(result)
	if err != nil || !compatibilityPlanBool(resultMap["ok"]) || !compatibilityPlanBool(resultMap["feasible"]) ||
		len(anySliceValue(resultMap["blockers"])) != 0 ||
		len(anySliceValue(resultMap["observed_resource_shortfalls"])) == 0 || authority.createInput.Name != "" ||
		compatibilityPlanBool(resultMap["resource_requirements_verified"]) ||
		stringValue(resultMap["setup_state"]) != "new_setup_feasible" || compatibilityPlanBool(resultMap["reuse_preferred"]) ||
		!compatibilityPlanBool(resultMap["requires_new_environment"]) {
		t.Fatalf("advisory preflight result=%#v create=%#v err=%v", result, authority.createInput, err)
	}

	// A feasible request with no reusable environment proceeds through the
	// ordinary immutable environment route after the mandatory internal check.
	_, err = server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "advisory-create"}, manageEnvironmentsToolName,
		map[string]any{"mode": "create", "name": "analysis", "packages": []any{"example-library"},
			"human_description": "Creating the selected analysis environment"}, authority,
	)
	if err != nil || authority.createInput.Name != "analysis" {
		t.Fatalf("feasible checked create=%#v err=%v", authority.createInput, err)
	}
}

func TestManagedEnvironmentPreflightUsesTotalMemoryCapacityNotTransientFreeMemory(t *testing.T) {
	total := uint64(12 * 1024 * 1024 * 1024)
	available := uint64(6 * 1024 * 1024 * 1024)
	blocked, currentlyBusy := managedEnvironmentMemoryCapacity(&total, &available, 8*1024)
	if blocked || !currentlyBusy {
		t.Fatalf("memory capacity classification blocked=%v currentlyBusy=%v", blocked, currentlyBusy)
	}
	total = uint64(4 * 1024 * 1024 * 1024)
	blocked, currentlyBusy = managedEnvironmentMemoryCapacity(&total, &available, 8*1024)
	if !blocked || currentlyBusy {
		t.Fatalf("insufficient total memory classification blocked=%v currentlyBusy=%v", blocked, currentlyBusy)
	}
}

func TestManagedEnvironmentUnverifiedAcceleratorMemoryIsAdvisory(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	server.hostGPUDetector = func(context.Context) compute.GPUInfo {
		memory := int64(8151)
		name := "test GPU"
		return compute.GPUInfo{Available: true, GPUMemoryMB: &memory, GPUCount: 1, GPUName: &name}
	}
	authority := &recordingManagedEnvironmentAuthority{}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "unverified-gpu-memory"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "preflight", "name": "generation", "packages": []any{"scientific-engine"},
			"resource_requirements": map[string]any{
				"min_cpu_cores": 4, "min_memory_mb": 8192, "min_disk_mb": 10240,
				"accelerator": "required", "min_accelerator_memory_mb": 8192,
			},
			"human_description": "Comparing an unverified GPU estimate",
		}, authority,
	)
	value := mapValue(result)
	if err != nil || !compatibilityPlanBool(value["feasible"]) ||
		len(anySliceValue(value["blockers"])) != 0 ||
		compatibilityPlanBool(value["resource_requirements_verified"]) ||
		!slices.Contains(stringArrayValue(value["observed_resource_shortfalls"]), "insufficient_accelerator_memory") ||
		!slices.Contains(stringArrayValue(value["advisories"]), "unverified_insufficient_accelerator_memory") {
		t.Fatalf("unverified GPU memory preflight=%#v err=%v", result, err)
	}
}

func TestManagedEnvironmentCreateReusesCompatibleReadyEnvironment(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		listResult: []kernelruntime.ManagedEnvironment{{
			Name: "existing-analysis", Language: "python", Status: "ready", Generation: "generation-existing",
		}},
		mutateResult: kernelruntime.ManagedEnvironment{
			Name: "existing-analysis", Language: "python", Status: "ready", Generation: "generation-existing",
		},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "reuse-create"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "create", "name": "new-analysis", "packages": []any{"scanpy", "anndata"},
			"human_description": "Preparing a compatible analysis environment",
		}, authority,
	)
	resultMap := mapValue(result)
	if err != nil || stringValue(resultMap["mode"]) != "reuse" ||
		stringValue(mapValue(resultMap["environment"])["name"]) != "existing-analysis" ||
		authority.createInput.Name != "" {
		t.Fatalf("reuse result=%#v create=%#v err=%v", result, authority.createInput, err)
	}
}

func TestManagedEnvironmentReuseCannotBypassLatestImplementationSelection(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{listResult: []kernelruntime.ManagedEnvironment{{
		Name: "engine-b-runtime", Language: "python", Status: "ready", Generation: "generation-existing",
	}}}
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		withTranscriptRunnerChatRun(context.Background(), run), identity,
		agentruntime.ToolCall{ID: "reuse-wrong-implementation"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "create", "name": "engine-b-runtime", "implementation": "Engine B",
			"packages": []any{"scanpy"}, "human_description": "Preparing the requested implementation",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "selected_implementation_mismatch" ||
		authority.createInput.Name != "" {
		t.Fatalf("reuse bypass result=%#v create=%#v err=%v", result, authority.createInput, err)
	}
}

func TestManagedEnvironmentPreflightMatchesPipPrefixedDependenciesByDistributionName(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		listResult: []kernelruntime.ManagedEnvironment{{Name: "scanpy", Language: "python", Status: "ready"}},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "pip-prefix-preflight"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "preflight", "name": "scanpy", "packages": []any{"scanpy", "pip::harmonypy"},
			"resource_requirements": managedEnvironmentTestResources(),
			"human_description":     "Checking a mixed conda and pip environment",
		}, authority,
	)
	if err != nil || !compatibilityPlanBool(mapValue(result)["ok"]) {
		t.Fatalf("preflight result=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(authority.listQuery.Dependencies, []string{"harmonypy", "scanpy"}) {
		t.Fatalf("dependency query=%#v", authority.listQuery.Dependencies)
	}
}

func TestManagedEnvironmentCanonicalPythonVersionUsesExactPackagePin(t *testing.T) {
	version, err := managedEnvironmentCanonicalPythonVersion("", []string{
		"python=3.10", "pytorch::torchvision", "pip::gdown",
	})
	if err != nil || version != "3.10" {
		t.Fatalf("canonical Python version=%q err=%v", version, err)
	}
	if _, err := managedEnvironmentCanonicalPythonVersion("3.11", []string{"python=3.10"}); err == nil {
		t.Fatal("conflicting Python contracts were accepted")
	}
	if _, err := managedEnvironmentCanonicalPythonVersion("", []string{"python=3.10", "python==3.11"}); err == nil {
		t.Fatal("multiple exact Python pins were accepted")
	}
	version, err = managedEnvironmentCanonicalPythonVersion("", []string{"python>=3.10"})
	if err != nil || version != "" {
		t.Fatalf("broad Python range was rewritten: version=%q err=%v", version, err)
	}
}

func TestManagedEnvironmentPreflightReturnsOneVersionedRecommendationAndBoundedAlternatives(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	candidates := make([]kernelruntime.ManagedEnvironment, 0, 12)
	for index := 0; index < 12; index++ {
		name := fmt.Sprintf("candidate-%02d", index)
		packages := []string{"numpy=2.2.0=build=conda", "scipy=1.14.1=build=conda", "python=3.11=build=conda"}
		if index == 11 {
			name = "requested-analysis"
			packages = append(packages, "matplotlib=3.9.0=build=conda")
		}
		candidates = append(candidates, kernelruntime.ManagedEnvironment{
			Name: name, Language: "python", Status: "ready", Generation: fmt.Sprintf("generation-%02d", index), Packages: packages,
		})
	}
	authority := &recordingManagedEnvironmentAuthority{listResult: candidates}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "bounded-preflight"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "preflight", "name": "requested-analysis", "packages": []any{"numpy", "scipy"},
			"resource_requirements": managedEnvironmentTestResources(),
			"human_description":     "Checking a compatible analysis environment",
		}, authority,
	)
	resultMap := mapValue(result)
	recommended := mapValue(resultMap["recommended_environment"])
	alternatives := anySliceValue(resultMap["compatible_environments"])
	if err != nil || stringValue(recommended["name"]) != "requested-analysis" ||
		!reflect.DeepEqual(anySliceValue(recommended["matched_packages"]), []any{"numpy=2.2.0=build=conda", "scipy=1.14.1=build=conda"}) ||
		len(alternatives) != 8 || stringValue(alternatives[0]) != "requested-analysis" ||
		resultMap["compatible_environment_count"] != 12 ||
		resultMap["omitted_compatible_environment_count"] != 4 || !authority.listQuery.IncludePackages ||
		stringValue(resultMap["setup_state"]) != "reuse_ready" || !compatibilityPlanBool(resultMap["reuse_preferred"]) ||
		compatibilityPlanBool(resultMap["requires_new_environment"]) {
		t.Fatalf("bounded preflight result=%#v query=%#v err=%v", result, authority.listQuery, err)
	}
}

func TestManagedPackageMutationUsesExplicitPipPrefixAsAuthority(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		mutateResult: kernelruntime.ManagedEnvironment{Name: "analysis", Language: "python", Status: "ready"},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "pip-prefix-install"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "analysis", "packages": []any{"pip::beautifulsoup4"},
			"human_description": "Installing the selected parser",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "completed" ||
		!authority.installInput.UsePip || !reflect.DeepEqual(authority.installInput.Packages, []string{"beautifulsoup4"}) {
		t.Fatalf("pip-prefixed mutation result=%#v input=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageSupportingDependencyDoesNotRequireImplementationSelection(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{
		Name: "analysis", Language: "python", Status: "ready",
	}}
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Generate candidates with a professional pocket-conditioned engine.",
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation"},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		withTranscriptRunnerChatRun(context.Background(), run), identity,
		agentruntime.ToolCall{ID: "support-package-install"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "analysis", "packages": []any{"pip::gemmi"},
			"human_description": "Installing a structure parser required by the selected workflow",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "completed" ||
		authority.installInput.Environment != "analysis" ||
		!reflect.DeepEqual(authority.installInput.Packages, []string{"gemmi"}) {
		t.Fatalf("support package result=%#v input=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageWorkflowSkillNameIsNotTreatedAsNewImplementation(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "binding-workflow"})
	server.skillCatalog = catalog
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{
		Name: "selected-engine", Language: "python", Status: "ready",
	}}
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Run the selected engine and inspect its binding mode.",
		SelectedImplementations:        []string{"Selected Engine"},
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation"},
	}
	run.addExecutedSkillNames("binding-workflow")
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		withTranscriptRunnerChatRun(context.Background(), run), identity,
		agentruntime.ToolCall{ID: "workflow-support-package"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "selected-engine", "implementation": "binding-workflow",
			"packages": []any{"gemmi"}, "human_description": "Adding the structure parser",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "completed" ||
		authority.installInput.Environment != "selected-engine" {
		t.Fatalf("workflow support package result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageSupportLabelCannotInventImplementationAfterExplicitRoute(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "selected-engine-runtime", ImplementationIdentities: []string{"Selected Engine"},
	})
	server.skillCatalog = catalog
	authority := &recordingManagedEnvironmentAuthority{mutateResult: kernelruntime.ManagedEnvironment{
		Name: "selected-engine", Language: "python", Status: "ready",
	}}
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Use Selected Engine for this scientific task.",
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation"},
	}
	run.addExecutedSkillNames("selected-engine-runtime")
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		withTranscriptRunnerChatRun(context.Background(), run), identity,
		agentruntime.ToolCall{ID: "fabricated-support-label"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "selected-engine",
			"implementation": "parser from package index via pip", "packages": []any{"parser"},
			"human_description": "Adding the parser needed by the selected engine",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "completed" ||
		authority.installInput.Environment != "selected-engine" {
		t.Fatalf("fabricated support label result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageDedicatedAlternativeRemainsImplementationDecision(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "engine-a", ImplementationIdentities: []string{"Engine A"}})
	catalog.AddSkill(skills.Skill{Name: "engine-b", ImplementationIdentities: []string{"Engine B"}})
	server.skillCatalog = catalog
	authority := &recordingManagedEnvironmentAuthority{}
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Use Engine A for this task.",
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation"},
	}
	run.addExecutedSkillNames("engine-a")
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		withTranscriptRunnerChatRun(context.Background(), run), identity,
		agentruntime.ToolCall{ID: "dedicated-alternative"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "engine-a", "implementation": "Engine B",
			"packages": []any{"engine-b-runtime"}, "human_description": "Changing the scientific engine",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "implementation_selection_required" ||
		authority.installInput.Environment != "" {
		t.Fatalf("dedicated alternative result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageNamedScientificImplementationStillRequiresSelection(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{}
	run := &sessionRunnerChatRun{
		TaskIntent:                     "Generate candidates with a professional pocket-conditioned engine.",
		RequiredScientificCapabilities: []string{"pocket-conditioned-molecule-generation"},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		withTranscriptRunnerChatRun(context.Background(), run), identity,
		agentruntime.ToolCall{ID: "engine-package-install"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "analysis", "implementation": "Scientific Engine A",
			"packages": []any{"pip::scientific-engine-a"}, "human_description": "Installing the scientific engine",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "implementation_selection_required" ||
		authority.installInput.Environment != "" {
		t.Fatalf("engine package selection result=%#v input=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageInstallReusesExactTargetEnvironmentWithoutMutation(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		listResult: []kernelruntime.ManagedEnvironment{{
			Name: "analysis", Language: "python", Status: "ready", Generation: "generation-existing",
		}},
		mutateResult: kernelruntime.ManagedEnvironment{
			Name: "analysis", Language: "python", Status: "ready", Generation: "generation-existing",
		},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "reuse-package-install"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "analysis", "packages": []any{"pip::beautifulsoup4"},
			"human_description": "Preparing the selected parser",
		}, authority,
	)
	resultMap := mapValue(result)
	if err != nil || stringValue(resultMap["mode"]) != "reuse" ||
		stringValue(mapValue(resultMap["environment"])["name"]) != "analysis" ||
		authority.installInput.Environment != "" {
		t.Fatalf("reuse result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageInstallDoesNotRedirectDeltaToUnrelatedEnvironment(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		listResult: []kernelruntime.ManagedEnvironment{{
			Name: "unrelated-docking", Language: "python", Status: "ready", Generation: "generation-existing",
		}},
		mutateResult: kernelruntime.ManagedEnvironment{
			Name: "selected-generator", Language: "python", Status: "ready", Generation: "generation-next",
		},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "targeted-package-install"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "selected-generator", "packages": []any{"gemmi"},
			"human_description": "Adding a parser to the selected generator environment",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "completed" ||
		authority.installInput.Environment != "selected-generator" {
		t.Fatalf("targeted mutation result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageInstallExecutesExplicitUpgradeSourceAgainstExactTarget(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		listResult: []kernelruntime.ManagedEnvironment{{
			Name: "gpu-generator", Language: "python", Status: "ready", Generation: "generation-old",
		}},
		mutateResult: kernelruntime.ManagedEnvironment{
			Name: "gpu-generator", Language: "python", Status: "ready", Generation: "generation-new",
		},
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "explicit-upgrade"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "gpu-generator", "packages": []any{"torch"},
			"use_pip": true, "pip_args": []any{"--upgrade"},
			"pip_extra_index_urls": []any{"https://packages.example.org/current"},
			"human_description":    "Upgrading the framework from its current official binary source",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "completed" ||
		authority.installInput.Environment != "gpu-generator" ||
		!reflect.DeepEqual(authority.installInput.PipArgs, []string{"--upgrade"}) {
		t.Fatalf("explicit upgrade result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageInstallRejectsCPUOnlySourceForRequiredAccelerator(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "cpu-source-conflict"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "gpu-generator", "implementation": "Selected Generator",
			"packages":       []any{"pip::compiled-extension"},
			"pip_find_links": []any{"https://packages.example.org/whl/framework-2.4+cpu.html"},
			"resource_requirements": map[string]any{
				"min_cpu_cores": 1, "min_memory_mb": 1024, "min_disk_mb": 1024, "accelerator": "required",
			},
			"human_description": "Installing the selected GPU extension",
		}, authority,
	)
	resultMap := mapValue(result)
	if err != nil || stringValue(resultMap["status"]) != "accelerator_package_source_conflict" ||
		compatibilityPlanBool(resultMap["executed"]) || authority.installInput.Environment != "" {
		t.Fatalf("accelerator source result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageReuseCannotBypassLatestImplementationSelection(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{listResult: []kernelruntime.ManagedEnvironment{{
		Name: "engine-b-runtime", Language: "python", Status: "ready", Generation: "generation-existing",
	}}}
	run := &sessionRunnerChatRun{SelectedImplementations: []string{"Engine A"}}
	run.addRequiredScientificCapabilities("pocket-conditioned-molecule-generation")
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		withTranscriptRunnerChatRun(context.Background(), run), identity,
		agentruntime.ToolCall{ID: "reuse-package-wrong-implementation"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "engine-b-runtime", "implementation": "Engine B",
			"packages": []any{"pip::beautifulsoup4"}, "human_description": "Preparing the requested implementation",
		}, authority,
	)
	if err != nil || stringValue(mapValue(result)["status"]) != "selected_implementation_mismatch" ||
		authority.installInput.Environment != "" {
		t.Fatalf("package reuse bypass result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedPackageMutationReturnsCoherentSplitForMixedAuthorities(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "mixed-authority-install"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "analysis", "packages": []any{"scanpy", "pip::harmonypy"},
			"human_description": "Preparing analysis dependencies",
		}, authority,
	)
	resultMap := mapValue(result)
	if err != nil || stringValue(resultMap["status"]) != "package_authority_split_required" ||
		resultMap["executed"] != false || authority.installInput.Environment != "" {
		t.Fatalf("mixed-authority result=%#v input=%#v err=%v", result, authority.installInput, err)
	}
}

func TestManagedEnvironmentToolsMapExactlyToTheKernelAuthority(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		listResult:   []kernelruntime.ManagedEnvironment{{Name: "scanpy", Language: "python", Status: "ready"}},
		mutateResult: kernelruntime.ManagedEnvironment{Name: "r-seurat", Language: "r", Generation: "generation-1", Status: "ready"},
	}

	listed, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "list-call"}, manageEnvironmentsToolName,
		map[string]any{"mode": "list", "language": "python", "dependencies": []any{"scanpy"}, "human_description": "Listing scanpy environments"},
		authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	listedMap, _ := listed.(map[string]any)
	if listedMap["count"] != 1 || authority.listQuery.Language != "python" ||
		!reflect.DeepEqual(authority.listQuery.Dependencies, []string{"scanpy"}) || !authority.listQuery.IncludePackages {
		t.Fatalf("list result=%#v query=%#v", listed, authority.listQuery)
	}
	machine := mapValue(listedMap["machine"])
	gpu, hasGPU := machine["accelerator"].(compute.GPUInfo)
	if numberValue(machine["cpu_cores"]) < 1 || machine["total_memory_bytes"] == nil || machine["disk_available_bytes"] == nil || !hasGPU || gpu.Available {
		t.Fatalf("list result is missing the current machine inventory: %#v", listedMap)
	}
	listedWithoutDependencies, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "list-compact"}, manageEnvironmentsToolName,
		map[string]any{"mode": "list", "language": "python", "human_description": "Listing Python environments"}, authority,
	)
	if err != nil || authority.listQuery.IncludePackages {
		t.Fatalf("unfiltered inventory loaded package manifests: result=%#v query=%#v err=%v", listedWithoutDependencies, authority.listQuery, err)
	}
	// The authority fixture returns one fixed inventory regardless of query. Clear
	// the earlier Python fixture so the R create path exercises creation instead
	// of the compatible-environment reuse branch covered by its dedicated test.
	authority.listResult = nil

	created, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "create-call"}, manageEnvironmentsToolName,
		withManagedEnvironmentTestPreflight(t, server, identity, authority, manageEnvironmentsToolName,
			map[string]any{"mode": "create", "name": "r-seurat", "language": "r", "packages": []any{"r-seurat"},
				"channels": []any{"conda-forge"}, "human_description": "Creating Seurat environment"},
		),
		authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	createdMap, _ := created.(map[string]any)
	if createdMap["status"] != "completed" || authority.createInput.Name != "r-seurat" ||
		authority.createInput.Language != "r" || !strings.HasPrefix(authority.createInput.OperationID, "environment-") ||
		authority.createInput.OperationID == "create-call" ||
		!reflect.DeepEqual(authority.createInput.Packages, []string{"r-seurat"}) {
		t.Fatalf("create result=%#v input=%#v", created, authority.createInput)
	}

	_, err = server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "canonical-channel-call"}, manageEnvironmentsToolName,
		withManagedEnvironmentTestPreflight(t, server, identity, authority, manageEnvironmentsToolName,
			map[string]any{"mode": "create", "name": "community-analysis", "packages": []any{"numpy"},
				"channels": []any{"defaults", "conda-forge", "bioconda"}, "human_description": "Creating analysis environment"},
		),
		authority,
	)
	if err != nil || !reflect.DeepEqual(authority.createInput.Channels, []string{"conda-forge", "bioconda"}) {
		t.Fatalf("Harness channel family was not canonicalized: channels=%#v err=%v", authority.createInput.Channels, err)
	}

	authority.mutateResult = kernelruntime.ManagedEnvironment{Name: "scanpy", Language: "python", Generation: "generation-2", Status: "ready"}
	installed, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "install-call"}, managePackagesToolName,
		withManagedEnvironmentTestPreflight(t, server, identity, authority, managePackagesToolName,
			map[string]any{"mode": "install", "environment": "scanpy", "packages": []any{"harmonypy"}, "use_pip": true,
				"human_description": "Installing Harmony package"},
		),
		authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	installedMap, _ := installed.(map[string]any)
	if installedMap["status"] != "completed" || authority.installInput.Environment != "scanpy" ||
		!strings.HasPrefix(authority.installInput.OperationID, "environment-") || authority.installInput.OperationID == "install-call" || !authority.installInput.UsePip ||
		!reflect.DeepEqual(authority.installInput.Packages, []string{"harmonypy"}) {
		t.Fatalf("install result=%#v input=%#v", installed, authority.installInput)
	}
}

func TestCanonicalManagedToolChannelsKeepsStandaloneDefaults(t *testing.T) {
	if got := canonicalManagedToolChannels([]string{"defaults"}); !reflect.DeepEqual(got, []string{"defaults"}) {
		t.Fatalf("standalone defaults changed: %#v", got)
	}
	if got := canonicalManagedToolChannels(nil); got != nil {
		t.Fatalf("omitted channels changed: %#v", got)
	}
}

func TestManagedEnvironmentToolsRouteEveryCanonicalMutationMode(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		mutateResult: kernelruntime.ManagedEnvironment{Name: "analysis", Language: "python", Packages: []string{"numpy==2.0"}, Status: "ready"},
	}
	deleted, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "delete-call"}, manageEnvironmentsToolName,
		map[string]any{"mode": "delete", "name": "obsolete", "human_description": "Removing obsolete environment"}, authority,
	)
	deletedEnvironment, _ := mapValue(deleted)["environment"].(map[string]any)
	if err != nil || authority.deleteName != "obsolete" || deletedEnvironment["status"] != "deactivated" {
		t.Fatalf("delete=%#v name=%q err=%v", deleted, authority.deleteName, err)
	}
	listed, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "list-packages"}, managePackagesToolName,
		map[string]any{"mode": "list", "environment": "analysis", "human_description": "Listing analysis packages"}, authority,
	)
	if err != nil || authority.inspectName != "analysis" || numberValue(mapValue(listed)["package_count"]) != 1 {
		t.Fatalf("package list=%#v inspect=%q err=%v", listed, authority.inspectName, err)
	}
	_, err = server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "uninstall-call"}, managePackagesToolName,
		map[string]any{"mode": "uninstall", "environment": "analysis", "packages": []any{"old-package"}, "use_pip": true, "human_description": "Removing old package"}, authority,
	)
	if err != nil || authority.uninstallInput.Environment != "analysis" || !reflect.DeepEqual(authority.uninstallInput.Packages, []string{"old-package"}) {
		t.Fatalf("uninstall=%#v err=%v", authority.uninstallInput, err)
	}
	_, err = server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "fork-call"}, managePackagesToolName,
		withManagedEnvironmentTestPreflight(t, server, identity, authority, managePackagesToolName,
			map[string]any{"mode": "install", "environment": "analysis", "packages": []any{"new-package"}, "use_pip": true,
				"fork_to": "analysis-next", "pip_args": []any{"--no-deps"}, "human_description": "Forking analysis environment"},
		), authority,
	)
	if err != nil || authority.installInput.ForkTo != "analysis-next" || !reflect.DeepEqual(authority.installInput.PipArgs, []string{"--no-deps"}) {
		t.Fatalf("fork install=%#v err=%v", authority.installInput, err)
	}
}

func TestManagedEnvironmentBackgroundOperationPublishesOneTerminalNotification(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{
		mutateResult: kernelruntime.ManagedEnvironment{Name: "scanpy", Language: "python", Generation: "generation-bg", Status: "ready"},
		started:      make(chan struct{}), release: make(chan struct{}),
	}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "background-create-call"}, manageEnvironmentsToolName,
		withManagedEnvironmentTestPreflight(t, server, identity, authority, manageEnvironmentsToolName,
			map[string]any{"mode": "create", "name": "scanpy", "packages": []any{"scanpy"}, "python_version": "3.11",
				"background": true, "human_description": "Creating scanpy environment"},
		),
		authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	resultMap, _ := result.(map[string]any)
	if resultMap["status"] != "running" || resultMap["notification_id"] == "" || resultMap["operation_id"] == "" {
		t.Fatalf("background result=%#v", result)
	}
	// Admission is durable and does not start an unowned goroutine. Start the
	// same service dispatcher used after a restart to execute the saved request.
	select {
	case <-authority.started:
		t.Fatal("background mutation ran before durable dispatch")
	default:
	}
	dispatchCtx, stopDispatch := context.WithCancel(context.Background())
	dispatchDone := make(chan error, 1)
	go func() { dispatchDone <- server.runTaskOperationDispatcher(dispatchCtx, authority) }()
	t.Cleanup(func() {
		stopDispatch()
		select {
		case <-dispatchDone:
		case <-time.After(3 * time.Second):
			t.Error("task dispatcher did not stop")
		}
	})
	select {
	case <-authority.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background environment operation did not start")
	}
	authority.mu.Lock()
	hadDeadline := authority.hadDeadline
	authority.mu.Unlock()
	if hadDeadline {
		t.Fatal("background environment operation received a wall-clock deadline")
	}
	close(authority.release)

	deadline := time.Now().Add(2 * time.Second)
	for {
		items, consumeErr := server.workspaceStore.ConsumeUnreadNotifications(
			context.Background(), identity.access.Frame.ID, identity.access.Frame.RootFrameID, identity.access.UserID, 10,
		)
		if consumeErr != nil {
			t.Fatal(consumeErr)
		}
		if len(items) == 1 {
			if items[0].ID != resultMap["notification_id"] || items[0].Payload["status"] != "completed" ||
				items[0].Payload["operation_id"] != resultMap["operation_id"] {
				t.Fatalf("terminal notification=%#v result=%#v", items[0], result)
			}
			break
		}
		if len(items) > 1 {
			t.Fatalf("background operation published duplicate notifications: %#v", items)
		}
		if time.Now().After(deadline) {
			t.Fatal("background environment completion notification was not published")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagedEnvironmentFailuresAreBoundedAndDoNotRetry(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{}
	input := withManagedEnvironmentTestPreflight(t, server, identity, authority, managePackagesToolName,
		map[string]any{"mode": "install", "environment": "scanpy", "packages": []any{"bad-package"},
			"human_description": "Installing invalid package"},
	)
	authority.mutateErr = errors.New("solver failed\n" + string(make([]byte, 2000)))
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "failed-install"}, managePackagesToolName,
		input, authority,
	)
	resultMap := mapValue(result)
	failure := mapValue(resultMap["failure"])
	if err != nil || resultMap["ok"] != false || stringValue(resultMap["status"]) != "failed" ||
		stringValue(failure["category"]) != "installation_failed" {
		t.Fatalf("foreground failure=%#v err=%v", result, err)
	}
	if message := boundedManagedEnvironmentError(authority.mutateErr); len([]rune(message)) > 1003 || message == authority.mutateErr.Error() {
		t.Fatalf("bounded failure length=%d message=%q", len([]rune(message)), message)
	}
	diagnosticTail := stringValue(failure["diagnostic_tail"])
	if diagnosticTail == "" || len([]rune(diagnosticTail)) > 2403 {
		t.Fatalf("bounded diagnostic tail length=%d value=%q", len([]rune(diagnosticTail)), diagnosticTail)
	}
}

func TestManagedEnvironmentFailureClassificationUsesTerminalCauseNotWarnings(t *testing.T) {
	inactivityErr := &kernelruntime.ManagedEnvironmentInstallerInactivityError{Duration: 5 * time.Minute}
	category, cause, details := classifyManagedEnvironmentFailure(fmt.Errorf("wrapped: %w", inactivityErr))
	if category != "installer_inactive" || stringValue(details["failure_stage"]) != "installer_execution" ||
		numberValue(details["inactivity_seconds"]) != 300 || !strings.Contains(cause, "no observable output") ||
		!strings.Contains(managedEnvironmentFailureRecovery(category), "do not leave the old process running") {
		t.Fatalf("inactivity category=%q cause=%q details=%#v", category, cause, details)
	}
	err := errors.New("error: [Errno 2] No such file or directory: 'which'\n" +
		"OMP: Warning #182: affinity ignored\nerror: [Errno 2] No such file or directory: 'g++'")
	category, cause, details = classifyManagedEnvironmentFailure(err)
	if category != "missing_build_tool" || stringValue(details["missing_executable"]) != "g++" ||
		!strings.Contains(cause, "required executable") {
		t.Fatalf("missing executable category=%q cause=%q details=%#v", category, cause, details)
	}
	category, _, details = classifyManagedEnvironmentFailure(errors.New("ModuleNotFoundError: No module named 'torch'"))
	if category != "build_isolation_missing_dependency" || stringValue(details["missing_module"]) != "torch" {
		t.Fatalf("missing module category=%q details=%#v", category, details)
	}
	category, _, details = classifyManagedEnvironmentFailure(errors.New(
		"libmamba Could not solve for environment specs\n" +
			"The following package could not be installed\n" +
			"└─ pytorch-cuda =12.8 * does not exist (perhaps a typo or a missing channel).",
	))
	if recovery := managedEnvironmentFailureRecovery(category); category != "dependency_resolution_failed" ||
		stringValue(details["unavailable_package"]) != "pytorch-cuda" ||
		!strings.Contains(recovery, "change only the verified installation authority") ||
		!strings.Contains(recovery, "Do not lower the framework, accelerator, or compiled-extension family") {
		t.Fatalf("solver category=%q details=%#v recovery=%q", category, details, recovery)
	}
	category, _, _ = classifyManagedEnvironmentFailure(errors.New(
		`pip phase 1: pip package specification at index 0 is invalid: "--no-index"`,
	))
	if category != "package_input_contract_invalid" {
		t.Fatalf("package input category=%q", category)
	}
	category, cause, details = classifyManagedEnvironmentFailure(errors.New(
		"Could not fetch URL https://packages.example/whl/index.html: [SSL: UNEXPECTED_EOF_WHILE_READING] EOF occurred in violation of protocol\n" +
			"Looking in links: https://packages.example/whl/index.html\n" +
			"ModuleNotFoundError: No module named 'torch'",
	))
	if category != "package_source_transport_failed" || stringValue(details["transport"]) != "tls" ||
		!strings.Contains(cause, "fallback source-build error is secondary") ||
		!strings.Contains(managedEnvironmentFailureRecovery(category), "manage_packages") {
		t.Fatalf("package transport category=%q cause=%q details=%#v", category, cause, details)
	}
}

func TestManagedEnvironmentDependencyFailurePreservesGenericRecoveryInvariants(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	name, capability := "test accelerator", "12.0"
	memory := int64(16384)
	server.hostGPUDetector = func(context.Context) compute.GPUInfo {
		return compute.GPUInfo{
			Available: true, GPUName: &name, GPUMemoryMB: &memory,
			ComputeCapability: &capability, GPUCount: 1,
		}
	}
	authority := &recordingManagedEnvironmentAuthority{mutateErr: errors.New(
		"libmamba Could not solve for environment specs\n" +
			"The following package could not be installed\n" +
			"└─ accelerator-runtime =12.8 * does not exist.",
	)}
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		context.Background(), identity, agentruntime.ToolCall{ID: "solver-recovery"}, manageEnvironmentsToolName,
		map[string]any{
			"mode": "create", "name": "selected-engine", "implementation": "SelectedEngine",
			"packages": []any{"python=3.10", "framework=2.7.*", "accelerator-runtime=12.8"},
			"resource_requirements": map[string]any{
				"accelerator": "required", "min_cpu_cores": 1, "min_memory_mb": 256, "min_disk_mb": 256,
			},
			"human_description": "Creating the selected scientific environment",
		}, authority,
	)
	value := mapValue(result)
	failure := mapValue(value["failure"])
	invariants := mapValue(failure["recovery_invariants"])
	observed := mapValue(invariants["observed_accelerator"])
	if err != nil || stringValue(failure["category"]) != "dependency_resolution_failed" ||
		stringValue(invariants["implementation"]) != "SelectedEngine" ||
		!slices.Equal(stringArrayValue(invariants["requested_packages"]), []string{
			"python=3.10", "framework=2.7.*", "accelerator-runtime=12.8",
		}) || stringValue(observed["compute_capability"]) != "12.0" ||
		!strings.Contains(stringValue(invariants["policy"]), "remain binding") {
		t.Fatalf("dependency recovery result=%#v err=%v", result, err)
	}
}

func TestManagedPackageMutationRedirectsInvalidatedGenerationToReplacement(t *testing.T) {
	server, identity := managedEnvironmentToolFixture(t)
	authority := &recordingManagedEnvironmentAuthority{listResult: []kernelruntime.ManagedEnvironment{{
		Name: "accelerated-runtime", Language: "python", Generation: "old-generation",
		Packages: []string{"framework==1.0"}, Status: "ready",
	}}}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	run.invalidateManagedEnvironment(
		"accelerated-runtime", "old-generation", "managed_environment_accelerator_incompatible",
	)
	result, err := server.executeAgentEnvironmentManagementToolWithAuthority(
		withTranscriptRunnerChatRun(context.Background(), run), identity,
		agentruntime.ToolCall{ID: "replace-invalid-generation"}, managePackagesToolName,
		map[string]any{
			"mode": "install", "environment": "accelerated-runtime",
			"packages": []any{"compiled-extension"}, "use_pip": true,
			"resource_requirements": map[string]any{
				"accelerator": "required", "min_cpu_cores": 1, "min_memory_mb": 256, "min_disk_mb": 256,
			},
			"human_description": "Preparing a compatible runtime extension",
		}, authority,
	)
	value := mapValue(result)
	if err != nil || stringValue(value["status"]) != "environment_generation_replacement_required" ||
		compatibilityPlanBool(value["executed"]) || !compatibilityPlanBool(value["recoverable"]) ||
		stringValue(value["invalid_generation"]) != "old-generation" || authority.installInput.Environment != "" {
		t.Fatalf("invalid generation mutation result=%#v install=%#v err=%v", result, authority.installInput, err)
	}
}
