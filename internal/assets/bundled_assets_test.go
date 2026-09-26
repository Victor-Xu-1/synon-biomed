package assets

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestBundledSynonBiomedSkillsManifest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	manifest, err := Load(filepath.Join(repositoryRoot, "assets", "synonbiomed", "skills.manifest.json"))
	if err != nil {
		t.Fatalf("load bundled skills manifest: %v", err)
	}
	report, err := Verify(filepath.Join(repositoryRoot, "skills", "synonbiomed"), manifest)
	if err != nil {
		t.Fatalf("verify bundled skills: %v", err)
	}
	if len(manifest.Skills) < 99 || report.Checked != len(manifest.Files) || report.TotalBytes <= 0 {
		t.Fatalf("bundled skill report = %#v", report)
	}
	for _, required := range []string{
		"alphafold2", "binding-mode-analysis", "boltz", "boltz2-nim", "chai1",
		"cheminfo-render", "chem-physical-properties", "compound-sourcing", "diffdock",
		"diffdock-nim", "drug-discovery-pipeline", "esmfold2", "fair-esm2", "genmol-nim",
		"ligandmpnn", "molmim-nim", "msa-structure-prediction-pipeline", "openfold3",
		"proteinmpnn", "quantum-chemistry-postprocessing", "rfdiffusion-nim", "solublempnn",
		"cuequivariance", "nvmolkit-usage", "autodock-vina", "document-workbench", "ngs-analysis-router",
		"capability-acquisition", "p2rank-pocket-detection", "pocket2mol-local", "sbdd-ppi-workflow",
		"structure-based-molecule-generation", "protein-design-strategy", "antibody-design-strategy", "rna-design-strategy",
		"analytical-method-lifecycle", "clinical-biostatistics", "clinical-development-plan", "clinical-trial-protocol",
		"cmc-control-strategy", "dmpk-adme-strategy", "drug-development-lifecycle", "formulation-development",
		"instrument-data-to-allotrope", "medicinal-chemistry-optimization", "nextflow-development",
		"nonclinical-safety-strategy", "pharmacovigilance-risk-management", "postmarketing-lifecycle-management",
		"process-development-scale-up", "regulatory-submission-strategy", "scientific-problem-selection",
		"single-cell-rna-analysis", "stability-shelf-life",
	} {
		if !slices.Contains(manifest.Skills, required) {
			t.Fatalf("required v1.1 skill missing from manifest: %s", required)
		}
		if info, err := os.Stat(filepath.Join(repositoryRoot, "skills", "synonbiomed", required)); err != nil || !info.IsDir() {
			t.Fatalf("required v1.1 skill missing on disk: %s: %v", required, err)
		}
	}
	for _, required := range []string{
		"alphafold2", "boltz", "borzoi", "chai1", "compute-env-setup", "customize", "diffdock",
		"esmfold2", "evo2", "fair-esm2", "figure-composer", "figure-style", "indication-dossier",
		"ligandmpnn", "literature-review", "managed-model-endpoints", "openfold3", "paper-narrative",
		"pdf-explore", "product-self-knowledge", "proteinmpnn", "remote-compute-modal", "remote-compute-ssh",
		"scgpt", "scvi-tools", "self-awareness", "skill-creator", "solublempnn", "using-model-endpoint",
	} {
		if !slices.Contains(manifest.Skills, required) {
			t.Fatalf("required reference Harness skill missing from manifest: %s", required)
		}
	}
	if len(manifest.Excluded) != 0 {
		t.Fatalf("v1.1 capability manifest must not exclude shipped skills: %v", manifest.Excluded)
	}
}

func TestBundledBioToolsManifest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	manifest, err := Load(filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools.manifest.json"))
	if err != nil {
		t.Fatalf("load bundled bio-tools manifest: %v", err)
	}
	if manifest.Entrypoint != "run_server.py" || manifest.Runtime.Kind != "python" || !manifest.Runtime.Optional {
		t.Fatalf("bio-tools runtime metadata = %#v", manifest)
	}
	report, err := Verify(filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools"), manifest)
	if err != nil {
		t.Fatalf("verify bundled bio-tools: %v", err)
	}
	if report.Checked < 300 || report.TotalBytes < 1024*1024 {
		t.Fatalf("bundled bio-tools report = %#v", report)
	}
}

func TestBundledBioToolsTLSPostureRunsBeforeConnectorImport(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	root := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools")
	launcher, err := os.ReadFile(filepath.Join(root, "run_server.py"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(launcher)
	policyIndex := strings.Index(text, "tls_policy.apply_posture()")
	connectorIndex := strings.Index(text, "importlib.import_module")
	if policyIndex < 0 || connectorIndex < 0 || policyIndex > connectorIndex {
		t.Fatalf("bundled connector launcher does not apply TLS posture before import")
	}
	command := exec.Command(python, filepath.Join(root, "tests", "test_tls_policy.py"))
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bundled TLS policy tests failed: %v\n%s", err, output)
	}
}

func TestBundledKetcherChemistryManifest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	manifest, err := Load(filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "ketcher-chemistry.manifest.json"))
	if err != nil {
		t.Fatalf("load bundled Ketcher manifest: %v", err)
	}
	if manifest.Entrypoint != "widget/index.html.gz" || manifest.Runtime.Kind != "go-stdio" || manifest.Runtime.Optional {
		t.Fatalf("Ketcher runtime metadata = %#v", manifest)
	}
	if manifest.Source != "EPAM Ketcher" || manifest.Version != "3.12.0" || manifest.Runtime.MinimumVersion != "" {
		t.Fatalf("Ketcher provenance must not define a second Synon product identity: %#v", manifest)
	}
	report, err := Verify(filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "ketcher-chemistry"), manifest)
	if err != nil {
		t.Fatalf("verify bundled Ketcher assets: %v", err)
	}
	if report.Checked != 2 || report.TotalBytes != 7591902 {
		t.Fatalf("bundled Ketcher report = %#v", report)
	}
}

func TestBundledBioToolsPreserveV11ChEMBLADMETTool(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Join(filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), "assets", "optional", "mcp-servers", "bio-tools")
	var domains map[string][]string
	readBundledJSON(t, filepath.Join(root, "lib", "mcp_bio", "domains.json"), &domains)
	if !slices.Contains(domains["chembl"], "get_admet") {
		t.Fatal("v1.1 get_admet tool is missing from aggregate domain registry")
	}
	var schemas struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	readBundledJSON(t, filepath.Join(root, "lib", "mcp_chembl", "schemas.json"), &schemas)
	foundSchema := false
	for _, tool := range schemas.Tools {
		if tool.Name == "get_admet" {
			foundSchema = true
			break
		}
	}
	if !foundSchema {
		t.Fatal("v1.1 get_admet tool is missing from ChEMBL schemas")
	}
	serverContent, err := os.ReadFile(filepath.Join(root, "lib", "mcp_chembl", "server.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serverContent), "def get_admet(") ||
		!strings.Contains(string(serverContent), `"get_admet": get_admet`) {
		t.Fatal("v1.1 get_admet handler or registration is missing")
	}
	marshalContent, err := os.ReadFile(filepath.Join(root, "lib", "mcp_chembl", "marshal.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(marshalContent), "def admet_response(") {
		t.Fatal("v1.1 ADMET response projection is missing")
	}
}

func TestBundledAgentTemplatesManifest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	manifest, err := Load(filepath.Join(repositoryRoot, "assets", "synonbiomed", "agents.manifest.json"))
	if err != nil {
		t.Fatalf("load bundled agents manifest: %v", err)
	}
	if len(manifest.Agents) != 14 {
		t.Fatalf("agent template count = %d", len(manifest.Agents))
	}
	report, err := Verify(filepath.Join(repositoryRoot, "assets", "synonbiomed", "agents"), manifest)
	if err != nil {
		t.Fatalf("verify bundled agents: %v", err)
	}
	if report.Checked != len(manifest.Files) || report.TotalBytes <= 0 {
		t.Fatalf("bundled agent report = %#v", report)
	}
	for _, required := range []string{"aidd-expert", "computational-chem-expert", "dmpk-expert", "medchem-expert", "structural-biology-expert"} {
		if !slices.Contains(manifest.Agents, required) {
			t.Fatalf("required v1.1 agent missing from manifest: %s", required)
		}
		if info, err := os.Stat(filepath.Join(repositoryRoot, "assets", "synonbiomed", "agents", required)); err != nil || !info.IsDir() {
			t.Fatalf("required v1.1 agent missing on disk: %s: %v", required, err)
		}
	}
	if len(manifest.Excluded) != 0 {
		t.Fatalf("v1.1 capability manifest must not exclude shipped agents: %v", manifest.Excluded)
	}
}

func TestBundledKernelComputeManifest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	manifest, err := Load(filepath.Join(repositoryRoot, "assets", "optional", "kernel-compute.manifest.json"))
	if err != nil {
		t.Fatalf("load kernel compute manifest: %v", err)
	}
	declared := make(map[string]bool, len(manifest.Files))
	for _, entry := range manifest.Files {
		declared[entry.Path] = true
	}
	for _, required := range []string{
		"kernels/synon_biomed_runtime/__init__.py",
		"kernels/synon_biomed_runtime/cheminfo_render.py",
		"kernels/cheminfo_render_helpers.py",
		"kernels/sdf_artifact_validator.py",
	} {
		if !declared[required] {
			t.Fatalf("required scientific Python runtime asset missing from manifest: %s", required)
		}
	}
	report, err := Verify(filepath.Join(repositoryRoot, "assets", "optional"), manifest)
	if err != nil {
		t.Fatalf("verify kernel compute assets: %v", err)
	}
	if report.Checked != len(manifest.Files) || report.TotalBytes <= 0 {
		t.Fatalf("kernel compute report = %#v", report)
	}
}

func TestBundledScientificPythonAssetsUseOneVerifiedHelperAuthority(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	read := func(relative string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		return string(raw)
	}

	canonical := read("assets/optional/kernels/synon_biomed_runtime/cheminfo_render.py")
	for _, symbol := range []string{"def render_molecule_images(", "use_svg: bool = True", "metadata_filename: str | os.PathLike[str] | None = None", "def save_py3dmol_html(", "def make_py3dmol_view_html("} {
		if !strings.Contains(canonical, symbol) {
			t.Fatalf("canonical cheminfo helper missing %s", symbol)
		}
	}
	skill := read("skills/synonbiomed/cheminfo-render/SKILL.md")
	if !strings.Contains(skill, "use_svg=True") || !strings.Contains(skill, "Do not invent other") {
		t.Fatalf("cheminfo skill does not document the canonical rendering contract")
	}
	for _, relative := range []string{
		"assets/optional/kernels/cheminfo_render_helpers.py",
		"skills/synonbiomed/cheminfo-render/kernel.py",
		"skills/synonbiomed/cheminfo-render/cheminfo_render_helpers.py",
	} {
		shim := read(relative)
		if strings.Contains(shim, "from kernel import") {
			t.Fatalf("%s retains the ambiguous generic kernel import", relative)
		}
		if !strings.Contains(shim, "cheminfo_render") {
			t.Fatalf("%s does not re-export the canonical cheminfo helper", relative)
		}
	}

	validator := read("assets/optional/kernels/sdf_artifact_validator.py")
	for _, contract := range []string{
		"Chem.ForwardSDMolSupplier(",
		"def validate_smiles(",
		"Chem.MolFromSmiles(",
		"empty_smiles_records",
		"sanitize=True",
		"removeHs=False",
		"strictParsing=True",
		"parsed_count == delimiter_count",
	} {
		if !strings.Contains(validator, contract) {
			t.Fatalf("SDF validator missing strict parser contract %q", contract)
		}
	}
}

func TestBundledPythonWorkerUsesOneModularRuntimeAndHostBridge(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(repositoryRoot, "assets", "optional", "kernels", name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if _, err := os.Stat(filepath.Join(repositoryRoot, "assets", "optional", "kernels", "oracle_kernel_worker.py")); !os.IsNotExist(err) {
		t.Fatalf("retired worker remains in the active asset tree: %v", err)
	}
	bootstrap := string(read("kernel_worker.py"))
	if !strings.Contains(bootstrap, "from synon_biomed_runtime.worker_execution import run") {
		t.Fatal("kernel entrypoint does not delegate to its owned runtime")
	}
	for _, module := range []string{"worker_transport.py", "worker_streams.py", "worker_compile.py", "worker_execution.py", "worker_safety.py", "worker_reads.py"} {
		if len(read(filepath.Join("synon_biomed_runtime", module))) == 0 {
			t.Fatalf("worker module %s is empty", module)
		}
	}
	bridge := string(read("synon_host_bridge.py"))
	for _, marker := range []string{
		"def bind_cell(", "def disable_cell(", "def finish_cell(", "ModuleType(\"host\")", "\"type\": \"host_call\"",
		"def _compute_error_from_host(", "class ComputeConcurrencyFull(", "_compute_accessor.JobPending",
	} {
		if !strings.Contains(bridge, marker) {
			t.Fatalf("host bridge missing protocol marker %q", marker)
		}
	}
	for _, forbidden := range []string{
		"_python_execution_preflight", "rdkit", "meeko", "autodock", "vina",
		"scanpy", "software_runtime", "DroidSansFallbackFull.ttf",
	} {
		if strings.Contains(strings.ToLower(bootstrap+"\n"+bridge), strings.ToLower(forbidden)) {
			t.Fatalf("kernel adapter contains forbidden domain/preflight marker %q", forbidden)
		}
	}
}

func TestSynonHostViewImageUsesTheManagedPythonImagePath(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	bridge := filepath.Join(repositoryRoot, "assets", "optional", "kernels", "synon_host_bridge.py")
	output := filepath.Join(t.TempDir(), "view.png")
	script := `
import importlib.util
import json
import sys
from PIL import Image

spec = importlib.util.spec_from_file_location("synon_host_bridge_view_test", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
image = Image.new("RGB", (120, 80), "white")
result = module._host_view_image(image, crop=(0.25, 0.25, 0.75, 0.75), max_size=32, out=sys.argv[2])
with Image.open(sys.argv[2]) as saved:
    assert saved.size == (32, 21), saved.size
assert result["original_size"] == (120, 80)
assert result["crop"] == (30, 20, 90, 60)
print(json.dumps(result, sort_keys=True))
`
	command := exec.Command(python, "-c", script, bridge, output)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	result, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("host.view_image contract failed: %v\n%s", err, result)
	}
	if !strings.Contains(string(result), `"saved_to"`) {
		t.Fatalf("host.view_image result = %s", result)
	}
}

func TestSDFArtifactValidatorProtocolRejectsInvalidEnvelope(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable for the standalone validator protocol test")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	validator := filepath.Join(repositoryRoot, "assets", "optional", "kernels", "sdf_artifact_validator.py")
	for _, test := range []struct {
		name string
		body []byte
		code string
	}{
		{name: "empty", code: "empty_sdf"},
		{name: "no delimiter", body: []byte("not an SD file\n"), code: "missing_record_delimiter"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(python, "-I", validator)
			command.Stdin = bytes.NewReader(test.body)
			output, runErr := command.Output()
			var exitErr *exec.ExitError
			if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 2 {
				t.Fatalf("invalid SDF validator exit = %v, output=%s", runErr, output)
			}
			var payload struct {
				SchemaVersion  int    `json:"schemaVersion"`
				OK             bool   `json:"ok"`
				Code           string `json:"code"`
				DelimiterCount int    `json:"delimiterCount"`
				ParsedCount    int    `json:"parsedCount"`
			}
			if err := json.Unmarshal(output, &payload); err != nil {
				t.Fatalf("decode validator output %q: %v", output, err)
			}
			if payload.SchemaVersion != 2 || payload.OK || payload.Code != test.code ||
				payload.DelimiterCount != 0 || payload.ParsedCount != 0 {
				t.Fatalf("invalid SDF validator payload = %#v", payload)
			}
		})
	}
	command := exec.Command(python, "-I", validator, "--format", "smi")
	command.Stdin = strings.NewReader("candidate_id\tcanonical_smiles\n")
	output, runErr := command.Output()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("header-only SMILES validator exit=%v output=%s", runErr, output)
	}
	var smilesPayload struct {
		SchemaVersion int    `json:"schemaVersion"`
		Format        string `json:"format"`
		OK            bool   `json:"ok"`
		Code          string `json:"code"`
		ParsedCount   int    `json:"parsedCount"`
	}
	if err := json.Unmarshal(output, &smilesPayload); err != nil {
		t.Fatal(err)
	}
	if smilesPayload.SchemaVersion != 2 || smilesPayload.Format != "smi" || smilesPayload.OK ||
		smilesPayload.Code != "empty_smiles_records" || smilesPayload.ParsedCount != 0 {
		t.Fatalf("header-only SMILES validator payload=%#v", smilesPayload)
	}
}

func TestBundledMicromambaManifestIncludesLicenseAndPinnedVersion(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	root := filepath.Join(repositoryRoot, "assets", "optional", "micromamba")
	manifest, err := Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatalf("load micromamba manifest: %v", err)
	}
	if manifest.Version != "2.9.0+synon.1" || manifest.License != "BSD-3-Clause" || manifest.LicenseFile != "LICENSE" ||
		manifest.SourceURL != "https://github.com/mamba-org/mamba/tree/2676ec2050f7dd5b8a524287526f50a8a4fb9652" {
		t.Fatalf("micromamba provenance metadata = %#v", manifest)
	}
	report, err := Verify(root, manifest)
	if err != nil {
		t.Fatalf("verify micromamba assets: %v", err)
	}
	required := map[string]bool{
		"linux-x86_64/micromamba": true, "LICENSE": true, "BUILD.md": true,
		"build-linux-64.sh": true, "build-linux-64.lock": true,
		"link-script-exit.patch": true, "collect-build-notices.py": true, "DEPENDENCY-NOTICES.txt": true,
	}
	for _, entry := range manifest.Files {
		if !required[entry.Path] {
			t.Fatalf("unexpected or duplicate installer asset %q", entry.Path)
		}
		delete(required, entry.Path)
	}
	if len(required) != 0 || report.Checked != 8 || report.TotalBytes <= 0 {
		t.Fatalf("micromamba asset report = %#v", report)
	}
}

func TestNativeScientificInstallerAssetManifestsVerify(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "assets", "optional", "micromamba")
	for _, platform := range []struct {
		name, executable string
	}{
		{name: "windows-x86_64", executable: "micromamba.exe"},
		{name: "darwin-x86_64", executable: "micromamba"},
		{name: "darwin-arm64", executable: "micromamba"},
	} {
		t.Run(platform.name, func(t *testing.T) {
			platformRoot := filepath.Join(root, platform.name)
			manifest, err := Load(filepath.Join(platformRoot, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Source != "mamba-org/micromamba-releases@2.9.0-0" ||
				manifest.Version != "2.9.0" || manifest.License != "BSD-3-Clause" ||
				manifest.LicenseFile != "LICENSE" || manifest.Entrypoint != platform.executable {
				t.Fatalf("native installer provenance=%#v", manifest)
			}
			report, err := Verify(platformRoot, manifest)
			if err != nil || report.Checked != 2 {
				t.Fatalf("native installer verification=%#v err=%v", report, err)
			}
		})
	}
}

func TestBundledSynonLinkExtensionManifest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	root := filepath.Join(repositoryRoot, "assets", "synon-link")
	manifest, err := Load(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatalf("load Synon Link manifest: %v", err)
	}
	if manifest.Runtime.Kind != "chrome-extension" || manifest.Runtime.MinimumVersion != "114" || manifest.Runtime.Optional {
		t.Fatalf("Synon Link runtime metadata = %#v", manifest.Runtime)
	}
	report, err := Verify(root, manifest)
	if err != nil {
		t.Fatalf("verify Synon Link extension: %v", err)
	}
	if report.Checked != 1 || report.TotalBytes != 94621 {
		t.Fatalf("Synon Link asset report = %#v", report)
	}
}

func readBundledJSON(t *testing.T, path string, target any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, target); err != nil {
		t.Fatal(err)
	}
}
