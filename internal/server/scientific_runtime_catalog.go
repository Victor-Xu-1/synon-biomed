package server

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"synon-go/internal/sciencecapability"
	"synon-go/internal/software"
)

const (
	scientificRuntimeSelectionSettingKey = "runtime.scientificWarmups.v1"

	commonStructureRuntimeID            = "common-structure-toolkit"
	structureInteractionRuntimeID       = "structure-interaction"
	biomolecularElectrostaticsRuntimeID = "biomolecular-electrostatics"
	autoDockVinaRuntimeID               = "autodock-vina"
	drugChemistryRuntimeID              = "drug-chemistry-process"
	molecularConversionRuntimeID        = "molecular-conversion-quantum"
	qsarADMETRuntimeID                  = "qsar-admet-classical"
	clinicalPharmacometricsRuntimeID    = "clinical-pharmacometrics"
	singleCellOmicsRuntimeID            = "single-cell-omics"
	genomicsCLIRuntimeID                = "genomics-command-line"
	molecularSimulationRuntimeID        = "molecular-simulation"
	medicalImagingRuntimeID             = "medical-imaging"
	instrumentAnalyticsRuntimeID        = "instrument-data-analytics"
)

type scientificRuntimeWarmupDefinition struct {
	ID                    string
	EstimatedInstallBytes int64
	DefaultEnabled        bool
	BuildRequest          func() (software.Request, []string, error)
}

func scientificRuntimeWarmupDefinitions() []scientificRuntimeWarmupDefinition {
	return []scientificRuntimeWarmupDefinition{
		pinnedPythonRuntime(
			commonStructureRuntimeID,
			32,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerPip, Spec: "rdkit==2024.3.5"},
				{Manager: software.PackageManagerPip, Spec: "biopython==1.88"},
				{Manager: software.PackageManagerPip, Spec: "gemmi==0.7.5"},
				{Manager: software.PackageManagerPip, Spec: "PyYAML==6.0.3"},
			},
			nil,
			[]string{"rdkit", "Bio", "gemmi", "yaml"},
			nil,
			5*60,
		),
		{
			ID: structureInteractionRuntimeID, EstimatedInstallBytes: 320 * 1024 * 1024, DefaultEnabled: true,
			BuildRequest: func() (software.Request, []string, error) {
				return newStructureInteractionRuntimeRequest(""), nil, nil
			},
		},
		{
			ID: biomolecularElectrostaticsRuntimeID, EstimatedInstallBytes: 700 * 1024 * 1024, DefaultEnabled: true,
			BuildRequest: func() (software.Request, []string, error) {
				return newStructureElectrostaticRuntimeRequest(""), []string{"apbs", "pdb2pqr", "inputgen"}, nil
			},
		},
		{
			ID: autoDockVinaRuntimeID, EstimatedInstallBytes: 900 * 1024 * 1024, DefaultEnabled: true,
			BuildRequest: func() (software.Request, []string, error) {
				request, _, engine, err := sciencecapability.BuildExecutionPackRuntimeRequest(
					"molecular-docking.autodock-vina",
					15*60,
				)
				if err != nil {
					return software.Request{}, nil, err
				}
				witnesses := make([]string, 0, len(engine.ExecutionPack.CLIWitnesses))
				for _, witness := range engine.ExecutionPack.CLIWitnesses {
					witnesses = append(witnesses, witness.Executable)
				}
				return request, witnesses, nil
			},
		},
		pinnedPythonRuntime(
			drugChemistryRuntimeID,
			180,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerPip, Spec: "thermo==0.6.1"},
				{Manager: software.PackageManagerPip, Spec: "chemicals==1.5.2"},
				{Manager: software.PackageManagerPip, Spec: "chempy==0.10.1"},
				{Manager: software.PackageManagerPip, Spec: "ase==3.29.0"},
				{Manager: software.PackageManagerPip, Spec: "tabulate==0.9.0"},
				{Manager: software.PackageManagerPip, Spec: "requests==2.32.5"},
				{Manager: software.PackageManagerPip, Spec: "beautifulsoup4==4.14.2"},
			},
			nil,
			[]string{"thermo", "chemicals", "chempy", "ase", "tabulate", "requests", "bs4"},
			nil,
			10*60,
		),
		pinnedPythonRuntime(
			molecularConversionRuntimeID,
			650,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerConda, Spec: "openbabel=3.2.1"},
				{Manager: software.PackageManagerConda, Spec: "xtb=6.7.1"},
				{Manager: software.PackageManagerConda, Spec: "ase=3.29.0"},
				{Manager: software.PackageManagerPip, Spec: "dimorphite-dl==2.0.2"},
			},
			[]string{"conda-forge"},
			[]string{"openbabel", "ase", "dimorphite_dl"},
			[]string{"obabel", "xtb"},
			15*60,
		),
		pinnedPythonRuntime(
			qsarADMETRuntimeID,
			600,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerPip, Spec: "scikit-learn==1.9.0"},
				{Manager: software.PackageManagerPip, Spec: "xgboost==3.2.0"},
				{Manager: software.PackageManagerPip, Spec: "lightgbm==4.7.0"},
				{Manager: software.PackageManagerPip, Spec: "shap==0.51.0"},
				{Manager: software.PackageManagerPip, Spec: "optuna==5.0.0"},
				{Manager: software.PackageManagerPip, Spec: "mordredcommunity==2.0.7"},
			},
			nil,
			[]string{"sklearn", "xgboost", "lightgbm", "shap", "optuna", "mordred"},
			nil,
			15*60,
		),
		pinnedPythonRuntime(
			clinicalPharmacometricsRuntimeID,
			220,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerPip, Spec: "lifelines==0.30.3"},
				{Manager: software.PackageManagerPip, Spec: "scikit-learn==1.9.0"},
				{Manager: software.PackageManagerPip, Spec: "pingouin==0.6.1"},
				{Manager: software.PackageManagerPip, Spec: "lmfit==1.3.4"},
				{Manager: software.PackageManagerPip, Spec: "pint==0.25.3"},
				{Manager: software.PackageManagerPip, Spec: "openpyxl==3.1.5"},
			},
			nil,
			[]string{"lifelines", "sklearn", "pingouin", "lmfit", "pint", "openpyxl"},
			nil,
			10*60,
		),
		pinnedPythonRuntime(
			singleCellOmicsRuntimeID,
			720,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerPip, Spec: "scanpy==1.11.5"},
				{Manager: software.PackageManagerPip, Spec: "anndata==0.12.19"},
				{Manager: software.PackageManagerPip, Spec: "harmonypy==0.0.10"},
				{Manager: software.PackageManagerPip, Spec: "leidenalg==0.10.2"},
				{Manager: software.PackageManagerPip, Spec: "igraph==0.11.9"},
			},
			nil,
			[]string{"scanpy", "anndata", "harmonypy", "leidenalg", "igraph"},
			nil,
			15*60,
		),
		pinnedPythonRuntime(
			genomicsCLIRuntimeID,
			300,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerConda, Spec: "samtools=1.24"},
				{Manager: software.PackageManagerConda, Spec: "bcftools=1.24"},
				{Manager: software.PackageManagerConda, Spec: "bedtools=2.31.1"},
				{Manager: software.PackageManagerConda, Spec: "minimap2=2.31"},
				{Manager: software.PackageManagerConda, Spec: "seqkit=2.13.0"},
				{Manager: software.PackageManagerConda, Spec: "pysam=0.24.1"},
			},
			[]string{"conda-forge", "bioconda"},
			[]string{"pysam"},
			[]string{"samtools", "bcftools", "bedtools", "minimap2", "seqkit"},
			15*60,
		),
		pinnedPythonRuntime(
			molecularSimulationRuntimeID,
			560,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerPip, Spec: "MDAnalysis==2.10.0"},
				{Manager: software.PackageManagerPip, Spec: "mdtraj==1.11.1.post2"},
				{Manager: software.PackageManagerPip, Spec: "ParmEd==4.3.1"},
				{Manager: software.PackageManagerPip, Spec: "openmm==8.6.0"},
			},
			nil,
			[]string{"MDAnalysis", "mdtraj", "parmed", "openmm"},
			nil,
			15*60,
		),
		pinnedPythonRuntime(
			medicalImagingRuntimeID,
			420,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerPip, Spec: "pydicom==3.0.2"},
				{Manager: software.PackageManagerPip, Spec: "nibabel==5.4.2"},
				{Manager: software.PackageManagerPip, Spec: "SimpleITK==2.5.6"},
				{Manager: software.PackageManagerPip, Spec: "scikit-image==0.26.0"},
			},
			nil,
			[]string{"pydicom", "nibabel", "SimpleITK", "skimage"},
			nil,
			10*60,
		),
		pinnedPythonRuntime(
			instrumentAnalyticsRuntimeID,
			260,
			true,
			[]software.PackageRequirement{
				{Manager: software.PackageManagerPip, Spec: "allotropy==0.1.55"},
				{Manager: software.PackageManagerPip, Spec: "pandas==2.2.3"},
				{Manager: software.PackageManagerPip, Spec: "openpyxl==3.1.2"},
				{Manager: software.PackageManagerPip, Spec: "pdfplumber==0.9.0"},
			},
			nil,
			[]string{"allotropy", "pandas", "openpyxl", "pdfplumber"},
			nil,
			10*60,
		),
	}
}

func pinnedPythonRuntime(
	id string,
	estimatedInstallMB int64,
	defaultEnabled bool,
	packages []software.PackageRequirement,
	channels []string,
	imports []string,
	executables []string,
	timeoutSeconds int64,
) scientificRuntimeWarmupDefinition {
	return scientificRuntimeWarmupDefinition{
		ID: id, EstimatedInstallBytes: estimatedInstallMB * 1024 * 1024, DefaultEnabled: defaultEnabled,
		BuildRequest: func() (software.Request, []string, error) {
			request, err := software.NormalizeRequest(software.Request{
				Capability: id, Provider: software.LocalProviderID, Language: "python",
				Packages: packages, Channels: channels, Imports: imports, Executable: "python",
				TimeoutSeconds: timeoutSeconds,
			})
			return request, append([]string(nil), executables...), err
		},
	}
}

func scientificRuntimeWarmupDefinitionByID(id string) (scientificRuntimeWarmupDefinition, bool) {
	for _, definition := range scientificRuntimeWarmupDefinitions() {
		if definition.ID == id {
			return definition, true
		}
	}
	return scientificRuntimeWarmupDefinition{}, false
}

func defaultScientificRuntimeWarmupIDs() []string {
	result := make([]string, 0)
	for _, definition := range scientificRuntimeWarmupDefinitions() {
		if definition.DefaultEnabled {
			result = append(result, definition.ID)
		}
	}
	return result
}

func normalizeScientificRuntimeWarmupIDs(values []string) ([]string, error) {
	if len(values) > len(scientificRuntimeWarmupDefinitions()) {
		return nil, errors.New("scientific runtime selection exceeds the supported count")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		id := strings.TrimSpace(raw)
		_, found := scientificRuntimeWarmupDefinitionByID(id)
		if !found {
			return nil, fmt.Errorf("scientific runtime %q is not registered", id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	slices.Sort(result)
	return result, nil
}
