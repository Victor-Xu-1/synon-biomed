package software

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type testProvider ProviderDescriptor

func (p testProvider) Descriptor() ProviderDescriptor { return ProviderDescriptor(p) }

func validRequest() Request {
	return Request{
		Capability: "sequence-alignment", Language: "native",
		Packages: []PackageRequirement{{Manager: PackageManagerConda, Spec: "minimap2=2.28"}},
		Channels: []string{"bioconda", "conda-forge"}, Executable: "minimap2",
		Arguments:       []string{"-a", "reference.fa", "reads.fq"},
		ExpectedOutputs: []OutputWitness{{Path: "out/alignment.sam", MinBytes: 1}},
	}
}

func TestNormalizeRequestAndIdentityAreStable(t *testing.T) {
	input := validRequest()
	first, err := NormalizeRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeRequest(first)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.TimeoutSeconds != 0 {
		t.Fatalf("normalization is not idempotent: first=%#v second=%#v", first, second)
	}
	digestA, err := RequestDigest(input)
	if err != nil {
		t.Fatal(err)
	}
	digestB, _ := RequestDigest(first)
	environmentA, err := EnvironmentName("local-conda", input)
	if err != nil {
		t.Fatal(err)
	}
	changedCommand := input
	changedCommand.Arguments = []string{"--version"}
	environmentB, err := EnvironmentName("local-conda", changedCommand)
	if err != nil {
		t.Fatal(err)
	}
	if digestA != digestB || environmentA != environmentB || len(environmentA) != len("swr-")+24 {
		t.Fatalf("stable identity mismatch: %s %s %s %s", digestA, digestB, environmentA, environmentB)
	}
	pythonRequest := Request{
		Capability: "table-analysis", Language: "python", Executable: "python",
		Packages: []PackageRequirement{{Manager: PackageManagerPip, Spec: "polars"}},
	}
	environmentC, err := EnvironmentName("local-conda", pythonRequest)
	if err != nil {
		t.Fatal(err)
	}
	pythonRequestWithWitness := pythonRequest
	pythonRequestWithWitness.Imports = []string{"polars"}
	environmentD, err := EnvironmentName("local-conda", pythonRequestWithWitness)
	if err != nil {
		t.Fatal(err)
	}
	digestC, err := RequestDigest(pythonRequest)
	if err != nil {
		t.Fatal(err)
	}
	digestD, err := RequestDigest(pythonRequestWithWitness)
	if err != nil {
		t.Fatal(err)
	}
	if environmentC != environmentD || digestC == digestD {
		t.Fatalf("validation-only import witness changed environment content identity: %s %s; request digests %s %s", environmentC, environmentD, digestC, digestD)
	}
}

func TestResolverContractIsCapabilityAndSoftwareAgnostic(t *testing.T) {
	resolver, err := NewResolver(testProvider{
		ID: "generic-provider", Priority: 100, Local: true,
		Languages:       []string{"python", "r", "native"},
		PackageManagers: []PackageManager{PackageManagerConda, PackageManagerPip}, Capabilities: []string{"*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := []Request{
		{Capability: "table-analysis", Language: "python", Packages: []PackageRequirement{{Manager: PackageManagerPip, Spec: "polars==1.32.3"}}, Imports: []string{"polars"}, Executable: "python", Arguments: []string{"pipeline.py"}},
		{Capability: "statistical-modeling", Language: "r", Packages: []PackageRequirement{{Manager: PackageManagerConda, Spec: "r-data.table=1.17.8"}}, Executable: "Rscript", Arguments: []string{"analysis.R"}},
		{Capability: "sequence-alignment", Language: "native", Packages: []PackageRequirement{{Manager: PackageManagerConda, Spec: "minimap2=2.28"}}, Executable: "minimap2", Arguments: []string{"--version"}},
	}
	for _, request := range requests {
		plan, resolveErr := resolver.Resolve(request)
		if resolveErr != nil || plan.ProviderID != "generic-provider" || plan.Environment == "" || plan.RequestDigest == "" {
			t.Fatalf("capability=%q plan=%#v err=%v", request.Capability, plan, resolveErr)
		}
	}
}

func TestNormalizeRequestRejectsShellAndEscapingPaths(t *testing.T) {
	tests := []Request{validRequest(), validRequest(), validRequest(), validRequest()}
	tests[0].Executable = "bin/minimap2"
	tests[1].Arguments = []string{"ok", "bad\x00arg"}
	tests[2].ExpectedOutputs = []OutputWitness{{Path: "../escape.sam"}}
	tests[3].Packages = []PackageRequirement{{Manager: PackageManagerConda, Spec: "minimap2\n--unsafe"}}
	for index, input := range tests {
		if _, err := NormalizeRequest(input); err == nil {
			t.Fatalf("unsafe request %d was accepted", index)
		}
	}
}

func TestNormalizeRequestPreservesSafeTaskRelativeWorkingDirectory(t *testing.T) {
	input := validRequest()
	input.WorkingDir = "analysis/run"
	normalized, err := NormalizeRequest(input)
	if err != nil || normalized.WorkingDir != "analysis/run" {
		t.Fatalf("normalized working_dir=%q err=%v", normalized.WorkingDir, err)
	}
	input.WorkingDir = "../escape"
	if _, err := NormalizeRequest(input); err == nil || !strings.Contains(err.Error(), "task workspace") {
		t.Fatalf("escaping working_dir error=%v", err)
	}
}

func TestNormalizeRequestBindsParsedOutputsAssertionsAndComparisons(t *testing.T) {
	request := Request{
		Capability: "generic-comparison", Language: "python", Executable: "python",
		ExpectedOutputs: []OutputWitness{
			{Path: "out/validation.json", Format: "json", RequiredJSONTrue: []string{"/checks/parsed", "/checks/finite"}},
			{Path: "out/results.csv", MinRecords: 2},
		},
		Comparisons: []TabularComparisonContract{{
			Path: "out/results.csv", DerivedColumns: []string{"relative_value"},
			BasisColumns: []string{"unit", "reference"}, GroupColumns: []string{"series"},
		}},
	}
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ExpectedOutputs[0].Format != "json" || normalized.ExpectedOutputs[1].MinRecords != 2 ||
		len(normalized.Comparisons) != 1 || normalized.Comparisons[0].BasisColumns[1] != "reference" {
		t.Fatalf("normalized quality contract=%#v outputs=%#v", normalized.Comparisons, normalized.ExpectedOutputs)
	}

	clone := func() Request {
		result := request
		result.ExpectedOutputs = append([]OutputWitness(nil), request.ExpectedOutputs...)
		result.ExpectedOutputs[0].RequiredJSONTrue = append([]string(nil), request.ExpectedOutputs[0].RequiredJSONTrue...)
		result.Comparisons = append([]TabularComparisonContract(nil), request.Comparisons...)
		result.Comparisons[0].DerivedColumns = append([]string(nil), request.Comparisons[0].DerivedColumns...)
		result.Comparisons[0].BasisColumns = append([]string(nil), request.Comparisons[0].BasisColumns...)
		result.Comparisons[0].GroupColumns = append([]string(nil), request.Comparisons[0].GroupColumns...)
		return result
	}
	tests := []Request{clone(), clone(), clone(), clone()}
	tests[0].ExpectedOutputs[0].RequiredJSONTrue = []string{"checks/parsed"}
	tests[1].ExpectedOutputs[0].Format = "csv"
	tests[2].Comparisons[0].Path = "out/missing.csv"
	tests[3].Comparisons[0].BasisColumns = []string{"relative_value"}
	for index, input := range tests {
		if _, err := NormalizeRequest(input); err == nil {
			t.Fatalf("invalid quality contract %d was accepted", index)
		}
	}
}

func TestNormalizeRequestCanonicalizesGroupingKeysOutOfComparisonBasis(t *testing.T) {
	request := Request{
		Capability: "grouped-comparison", Language: "python", Executable: "python",
		ExpectedOutputs: []OutputWitness{{Path: "out/results.csv", Format: "csv"}},
		Comparisons: []TabularComparisonContract{{
			Path: "out/results.csv", DerivedColumns: []string{"change_pct"},
			BasisColumns: []string{"Regimen", "Parameter", "Base_value"},
			GroupColumns: []string{"regimen", "parameter"},
		}},
	}
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.Comparisons[0].BasisColumns; !slices.Equal(got, []string{"Base_value"}) {
		t.Fatalf("canonical basis columns=%#v", got)
	}

	request.Comparisons[0].BasisColumns = []string{"Regimen", "Parameter"}
	if _, err := NormalizeRequest(request); err == nil || !strings.Contains(err.Error(), "no non-group basis columns") {
		t.Fatalf("group-only comparison basis error=%v", err)
	}
}

func TestNormalizeRequestBindsScientificEvidenceToManagedFiles(t *testing.T) {
	request := Request{
		Capability: "molecular-docking", Language: "python", Executable: "python",
		Packages:  []PackageRequirement{{Manager: PackageManagerConda, Spec: "vina=1.2.7"}},
		Arguments: []string{"docking_pipeline.py"},
		ExpectedOutputs: []OutputWitness{
			{Path: "out/ranked_poses.pdbqt", MinBytes: 64},
			{Path: "out/vina.log", MinBytes: 64},
		},
		ScientificEvidence: &ScientificEvidenceRequest{
			Engine: "autodock-vina", EnginePackage: "vina", ScoreKind: "affinity-kcal-mol",
			Inputs: []ScientificFileWitness{
				{Kind: "receptor", Path: "inputs/receptor.pdbqt"},
				{Kind: "ligand", Path: "inputs/ligands.sdf"},
			},
			Artifacts: []ScientificFileWitness{
				{Kind: "ranked-pose", Path: "out/ranked_poses.pdbqt"},
				{Kind: "execution-log", Path: "out/vina.log"},
			},
			CodePaths: []string{"docking_pipeline.py"},
		},
	}
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ScientificEvidence == nil ||
		normalized.ScientificEvidence.EnginePackage != "vina" ||
		normalized.ScientificEvidence.Artifacts[0].Path != "out/ranked_poses.pdbqt" {
		t.Fatalf("scientific evidence=%#v", normalized.ScientificEvidence)
	}
	first, err := ScientificEvidenceDigest(*normalized.ScientificEvidence)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ScientificEvidenceDigest(*normalized.ScientificEvidence)
	if err != nil || first != second || len(first) != 64 {
		t.Fatalf("scientific evidence digest first=%q second=%q err=%v", first, second, err)
	}
}

func TestNormalizeRequestRejectsUnboundScientificEvidence(t *testing.T) {
	base := Request{
		Capability: "molecular-docking", Language: "python", Executable: "python",
		Packages:        []PackageRequirement{{Manager: PackageManagerConda, Spec: "vina=1.2.7"}},
		ExpectedOutputs: []OutputWitness{{Path: "out/ranked_poses.pdbqt", MinBytes: 64}},
		ScientificEvidence: &ScientificEvidenceRequest{
			Engine: "autodock-vina", EnginePackage: "vina", ScoreKind: "affinity-kcal-mol",
			Inputs:    []ScientificFileWitness{{Kind: "receptor", Path: "receptor.pdbqt"}},
			Artifacts: []ScientificFileWitness{{Kind: "ranked-pose", Path: "out/ranked_poses.pdbqt"}},
		},
	}
	tests := []Request{base, base, base, base}
	tests[0].ScientificEvidence = &ScientificEvidenceRequest{
		Engine: "autodock-vina", EnginePackage: "gnina", ScoreKind: "affinity-kcal-mol",
		Inputs: base.ScientificEvidence.Inputs, Artifacts: base.ScientificEvidence.Artifacts,
	}
	tests[1].ScientificEvidence = &ScientificEvidenceRequest{
		Engine: "autodock-vina", EnginePackage: "vina", ScoreKind: "affinity-kcal-mol",
		Inputs:    []ScientificFileWitness{{Kind: "receptor", Path: "../escape.pdbqt"}},
		Artifacts: base.ScientificEvidence.Artifacts,
	}
	tests[2].ScientificEvidence = &ScientificEvidenceRequest{
		Engine: "autodock-vina", EnginePackage: "vina", ScoreKind: "affinity-kcal-mol",
		Inputs:    base.ScientificEvidence.Inputs,
		Artifacts: []ScientificFileWitness{{Kind: "ranked-pose", Path: "out/undeclared.pdbqt"}},
	}
	tests[3].ScientificEvidence = &ScientificEvidenceRequest{
		Engine: "autodock-vina", EnginePackage: "vina", ScoreKind: "affinity-kcal-mol",
		Inputs:    []ScientificFileWitness{{Kind: "receptor", Path: "receptor.pdbqt"}, {Kind: "receptor", Path: "other.pdbqt"}},
		Artifacts: base.ScientificEvidence.Artifacts,
	}
	for index, request := range tests {
		if _, err := NormalizeRequest(request); err == nil {
			t.Fatalf("unbound scientific evidence %d was accepted", index)
		}
	}
}

func TestResolverAcceptsProviderDefinedPackageManagerWithoutCoreChanges(t *testing.T) {
	const manager PackageManager = "cran"
	resolver, err := NewResolver(testProvider{
		ID: "r-library-provider", Priority: 100, Local: true,
		Languages: []string{"r"}, PackageManagers: []PackageManager{manager}, Capabilities: []string{"*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(Request{
		Capability: "statistical-modeling", Language: "r", Executable: "Rscript",
		Packages: []PackageRequirement{{Manager: manager, Spec: "data.table@1.17.8"}},
	})
	if err != nil || plan.ProviderID != "r-library-provider" {
		t.Fatalf("provider-defined manager plan=%#v err=%v", plan, err)
	}
}

func TestResolverSelectsExactlyOneProvider(t *testing.T) {
	resolver, err := NewResolver(
		testProvider{ID: "local-conda", Priority: 100, Local: true, Languages: []string{"native", "python", "r"}, PackageManagers: []PackageManager{PackageManagerConda, PackageManagerPip}, Capabilities: []string{"*"}},
		testProvider{ID: "specialized-local", Priority: 10, Local: true, Languages: []string{"native"}, PackageManagers: []PackageManager{PackageManagerConda}, Capabilities: []string{"sequence-alignment"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := resolver.Resolve(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProviderID != "local-conda" || plan.Environment == "" || plan.RequestDigest == "" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	explicit := validRequest()
	explicit.Provider = "specialized-local"
	plan, err = resolver.Resolve(explicit)
	if err != nil || plan.ProviderID != "specialized-local" {
		t.Fatalf("explicit provider was not honored: plan=%#v err=%v", plan, err)
	}
}

func TestResolverRejectsAmbiguityAndNeverSubstitutesExplicitProvider(t *testing.T) {
	resolver, err := NewResolver(
		testProvider{ID: "provider-a", Priority: 10, Local: true, Languages: []string{"native"}, PackageManagers: []PackageManager{PackageManagerConda}, Capabilities: []string{"*"}},
		testProvider{ID: "provider-b", Priority: 10, Local: true, Languages: []string{"native"}, PackageManagers: []PackageManager{PackageManagerConda}, Capabilities: []string{"*"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(validRequest()); !errors.Is(err, ErrAmbiguousProvider) {
		t.Fatalf("ambiguous providers were not rejected: %v", err)
	}
	explicit := validRequest()
	explicit.Provider = "missing-provider"
	if _, err := resolver.Resolve(explicit); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("missing explicit provider was silently substituted: %v", err)
	}
}
func TestSourceEnvironmentReusesManagedNameAndBindsInputDigest(t *testing.T) {
	request := Request{
		Capability:        "structure-energy-minimization",
		Language:          "python",
		SourceEnvironment: "synon-biomed-python",
		InputSHA256:       strings.Repeat("a", 64),
		Executable:        "python",
		Arguments:         []string{"_structure_minimize.py"},
	}
	normalized, err := NormalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SourceEnvironment != request.SourceEnvironment || normalized.InputSHA256 != request.InputSHA256 {
		t.Fatalf("source contract was not normalized: %#v", normalized)
	}
	environment, err := EnvironmentName(LocalProviderID, request)
	if err != nil || environment != request.SourceEnvironment {
		t.Fatalf("source environment did not remain the selected identity: environment=%q err=%v", environment, err)
	}

	other := request
	other.InputSHA256 = strings.Repeat("b", 64)
	firstDigest, err := RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := RequestDigest(other)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatal("input digest was not bound into the request digest")
	}

	invalid := request
	invalid.Packages = []PackageRequirement{{Manager: PackageManagerConda, Spec: "rdkit"}}
	if _, err := NormalizeRequest(invalid); err == nil {
		t.Fatal("source environment unexpectedly allowed package installation")
	}
}
