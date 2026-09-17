package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"synon-go/internal/software"
)

func TestDecodeStructureMinimizationRequestAppliesBoundedDefaults(t *testing.T) {
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"content":"mol-data","filename":"ligand.sdf"}`))

	decoded, err := decodeStructureMinimizationRequest(request)
	if err != nil {
		t.Fatalf("decode valid request: %v", err)
	}
	if decoded.Content != "mol-data" || decoded.Filename != "ligand.sdf" {
		t.Fatalf("decoded request lost input: %+v", decoded)
	}
	if decoded.ForceField != "uff" || decoded.MaxIterations != 200 || decoded.Tolerance != 1e-4 {
		t.Fatalf("unexpected defaults: %+v", decoded)
	}
	if decoded.Scope != "ligand" || decoded.ProteinEnvironment != "fixed" {
		t.Fatalf("unexpected ligand minimization contract: %+v", decoded)
	}
}

func TestDecodeStructureMinimizationRequestRejectsAmbiguousPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"content":"x","filename":"a.sdf","unknown":true}`},
		{name: "trailing data", body: `{"content":"x","filename":"a.sdf"}{}`},
		{name: "empty content", body: `{"content":"","filename":"a.sdf"}`},
		{name: "embedded nul", body: "{\"content\":\"x\\u0000y\",\"filename\":\"a.sdf\"}"},
		{name: "unbounded iterations", body: `{"content":"x","filename":"a.sdf","max_iterations":1001}`},
		{name: "unsupported force field", body: `{"content":"x","filename":"a.sdf","force_field":"gfn2"}`},
		{name: "unsupported scope", body: `{"content":"x","filename":"a.sdf","scope":"whole-structure"}`},
		{name: "movable protein", body: `{"content":"x","filename":"a.sdf","protein_environment":"movable"}`},
		{name: "invalid ligand residue", body: `{"content":"x","filename":"a.sdf","ligand_residue_name":"LIGAND"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/", strings.NewReader(test.body))
			if _, err := decodeStructureMinimizationRequest(request); err == nil {
				t.Fatalf("expected %s to be rejected", test.name)
			}
		})
	}
}

func TestStructureMinimizationProgramMovesOnlyLigandInFixedProteinPocket(t *testing.T) {
	preservedPDB := "REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE\n" +
		"HELIX    1   1 ALA A    1  ALA A    1  1                                  1\n" +
		testProteinLigandComplexPDB
	result := runStructureMinimizationProgram(
		t,
		"complex.pdb",
		"complex-minimized.pdb",
		"pdb",
		preservedPDB,
	)
	if result.err != nil {
		t.Fatalf("run ligand minimization: %v\n%s", result.err, result.output)
	}

	minimized, err := os.ReadFile(result.outputPath)
	if err != nil {
		t.Fatalf("read minimized complex: %v", err)
	}
	minimizedText := string(minimized)
	if !strings.Contains(minimizedText, "REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE") ||
		!strings.Contains(minimizedText, "HELIX    1   1 ALA A    1  ALA A    1") {
		t.Fatalf("minimized PDB lost receptor metadata:\n%s", minimizedText)
	}
	if strings.Count(minimizedText, "ATOM  ") != 4 || strings.Count(minimizedText, "HETATM") != 3 {
		t.Fatalf("minimized PDB changed polymer atom record types:\n%s", minimizedText)
	}
	originalCoordinates := pdbCoordinatesBySerial(t, preservedPDB)
	minimizedCoordinates := pdbCoordinatesBySerial(t, minimizedText)
	for serial := 1; serial <= 4; serial++ {
		if coordinateDistance(originalCoordinates[serial], minimizedCoordinates[serial]) > 1e-6 {
			t.Fatalf("protein atom %d moved: before=%v after=%v", serial, originalCoordinates[serial], minimizedCoordinates[serial])
		}
	}
	ligandMoved := false
	for serial := 5; serial <= 7; serial++ {
		if coordinateDistance(originalCoordinates[serial], minimizedCoordinates[serial]) > 0.05 {
			ligandMoved = true
		}
	}
	if !ligandMoved {
		t.Fatal("ligand coordinates did not change")
	}

	report := readTestStructureMinimizationReport(t, result.reportPath)
	beforeEnergy, beforeOK := report["before_energy"].(float64)
	afterEnergy, afterOK := report["after_energy"].(float64)
	if report["scope"] != "ligand" || report["protein_environment"] != "fixed" ||
		report["protein_present"] != true || report["ligand_atom_count"] != float64(3) ||
		report["fixed_atom_count"] != float64(4) || report["generated_conformer"] != false ||
		report["ligand_residue"] != "LIG:Z:101" || report["converged"] != true ||
		!beforeOK || !afterOK || !(afterEnergy < beforeEnergy) {
		t.Fatalf("unexpected ligand-only report: %#v", report)
	}
}

func TestStructureMinimizationProgramMinimizesStandaloneLigandWithoutProtein(t *testing.T) {
	result := runStructureMinimizationProgram(
		t,
		"ligand.mol",
		"ligand-minimized.sdf",
		"mol",
		testStandaloneLigandMOL,
	)
	if result.err != nil {
		t.Fatalf("run standalone ligand minimization: %v\n%s", result.err, result.output)
	}
	report := readTestStructureMinimizationReport(t, result.reportPath)
	if report["protein_present"] != false || report["fixed_atom_count"] != float64(0) ||
		report["ligand_atom_count"] != float64(3) || report["generated_conformer"] != true {
		t.Fatalf("unexpected standalone ligand report: %#v", report)
	}
	if _, err := os.Stat(result.outputPath); err != nil {
		t.Fatalf("standalone minimized ligand is unavailable: %v", err)
	}
}

func TestStructureMinimizationProgramRejectsMultipleLigandCandidates(t *testing.T) {
	result := runStructureMinimizationProgram(
		t,
		"ambiguous-complex.pdb",
		"ambiguous-complex-minimized.pdb",
		"pdb",
		testProteinMultipleLigandsPDB,
	)
	if result.err == nil {
		t.Fatalf("expected ambiguous ligand selection to fail\n%s", result.output)
	}
	report := readTestStructureMinimizationReport(t, result.reportPath)
	if report["ok"] != false || report["message"] != "the protein complex contains multiple ligand candidates; open one ligand pose before minimizing" {
		t.Fatalf("unexpected ambiguous-ligand report: %#v", report)
	}
	if _, err := os.Stat(result.outputPath); !os.IsNotExist(err) {
		t.Fatalf("ambiguous minimization unexpectedly wrote output: %v", err)
	}
}

func TestStructureMinimizationProgramRejectsMultipleStandaloneMolecules(t *testing.T) {
	result := runStructureMinimizationProgram(
		t,
		"multiple-ligands.sdf",
		"multiple-ligands-minimized.sdf",
		"sdf",
		testStandaloneLigandMOL+"\n$$$$\n"+testStandaloneLigandMOL+"\n$$$$\n",
	)
	if result.err == nil {
		t.Fatalf("expected multiple standalone ligands to fail\n%s", result.output)
	}
	report := readTestStructureMinimizationReport(t, result.reportPath)
	if report["ok"] != false || report["message"] != "input contains multiple molecules; open one ligand before minimizing" {
		t.Fatalf("unexpected multiple-molecule report: %#v", report)
	}
	if _, err := os.Stat(result.outputPath); !os.IsNotExist(err) {
		t.Fatalf("multiple-molecule minimization unexpectedly wrote output: %v", err)
	}
}

func TestStructureMinimizationProgramUsesExplicitLigandIdentityFromCurrentPose(t *testing.T) {
	result := runStructureMinimizationProgram(
		t,
		"current-pose.pdb",
		"current-pose-minimized.pdb",
		"pdb",
		testProteinLigandCurrentPosePDB,
		"--ligand-residue-name", "LIG",
	)
	if result.err != nil {
		t.Fatalf("run explicit current-pose minimization: %v\n%s", result.err, result.output)
	}
	report := readTestStructureMinimizationReport(t, result.reportPath)
	if report["protein_present"] != true || report["fixed_atom_count"] != float64(4) ||
		report["ligand_atom_count"] != float64(3) || report["generated_conformer"] != false ||
		report["ligand_residue"] != "LIG:*" {
		t.Fatalf("unexpected explicit-ligand report: %#v", report)
	}
}

type structureMinimizationProgramResult struct {
	outputPath string
	reportPath string
	output     []byte
	err        error
}

func runStructureMinimizationProgram(
	t *testing.T,
	inputName string,
	outputName string,
	inputFormat string,
	content string,
	extraArgs ...string,
) structureMinimizationProgramResult {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	if err := exec.Command(python, "-I", "-c", "import rdkit").Run(); err != nil {
		t.Skip("RDKit is unavailable")
	}

	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "structure_minimization.py")
	inputPath := filepath.Join(tempDir, inputName)
	outputPath := filepath.Join(tempDir, outputName)
	reportPath := filepath.Join(tempDir, "report.json")
	if err := os.WriteFile(scriptPath, structureMinimizationProgram, 0o600); err != nil {
		t.Fatalf("write runtime program: %v", err)
	}
	if err := os.WriteFile(inputPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write minimization fixture: %v", err)
	}

	arguments := []string{
		scriptPath,
		"--input", filepath.Base(inputPath),
		"--output", filepath.Base(outputPath),
		"--report", filepath.Base(reportPath),
		"--format", inputFormat,
		"--force-field", "uff",
		"--scope", "ligand",
		"--protein-environment", "fixed",
		"--max-iterations", "200",
		"--tolerance", "1e-4",
	}
	arguments = append(arguments, extraArgs...)
	command := exec.Command(python, arguments...)
	command.Dir = tempDir
	output, runErr := command.CombinedOutput()
	return structureMinimizationProgramResult{
		outputPath: outputPath,
		reportPath: reportPath,
		output:     output,
		err:        runErr,
	}
}

func readTestStructureMinimizationReport(t *testing.T, path string) map[string]any {
	t.Helper()
	reportBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read minimization report: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatalf("decode minimization report: %v", err)
	}
	return report
}

func pdbCoordinatesBySerial(t *testing.T, content string) map[int][3]float64 {
	t.Helper()
	coordinates := map[int][3]float64{}
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "ATOM") && !strings.HasPrefix(line, "HETATM") {
			continue
		}
		if len(line) < 54 {
			t.Fatalf("short PDB atom line: %q", line)
		}
		serial, err := strconv.Atoi(strings.TrimSpace(line[6:11]))
		if err != nil {
			t.Fatalf("parse PDB serial: %v", err)
		}
		var point [3]float64
		for index, bounds := range [][2]int{{30, 38}, {38, 46}, {46, 54}} {
			point[index], err = strconv.ParseFloat(strings.TrimSpace(line[bounds[0]:bounds[1]]), 64)
			if err != nil {
				t.Fatalf("parse PDB coordinate: %v", err)
			}
		}
		coordinates[serial] = point
	}
	return coordinates
}

func coordinateDistance(left, right [3]float64) float64 {
	dx := left[0] - right[0]
	dy := left[1] - right[1]
	dz := left[2] - right[2]
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

const testProteinLigandComplexPDB = `ATOM      1  N   ALA A   1      -4.000   0.000   0.000  1.00 20.00           N
ATOM      2  CA  ALA A   1      -2.550   0.000   0.000  1.00 20.00           C
ATOM      3  C   ALA A   1      -1.800   1.250   0.000  1.00 20.00           C
ATOM      4  O   ALA A   1      -2.300   2.350   0.000  1.00 20.00           O
HETATM    5  C1  LIG Z 101       0.000   0.000   0.000  1.00 20.00           A
HETATM    6  C2  LIG Z 101       1.400   0.000   0.000  1.00 20.00           A
HETATM    7 Cl   LIG Z 101       3.400   0.000   0.000  1.00 20.00           C
CONECT    5    6
CONECT    6    5    7
CONECT    7    6
END
`

const testStandaloneLigandMOL = `standalone-ligand
  Synon Biomed

  3  2  0  0  0  0  0  0  0  0999 V2000
    0.0000    0.0000    0.0000 C   0  0  0  0  0  0  0  0  0  0  0  0
    0.6000    0.0000    0.0000 C   0  0  0  0  0  0  0  0  0  0  0  0
    1.1000    0.0000    0.0000 O   0  0  0  0  0  0  0  0  0  0  0  0
  1  2  1  0
  2  3  1  0
M  END
`

const testProteinMultipleLigandsPDB = `ATOM      1  N   ALA A   1      -4.000   0.000   0.000  1.00 20.00           N
ATOM      2  CA  ALA A   1      -2.550   0.000   0.000  1.00 20.00           C
ATOM      3  C   ALA A   1      -1.800   1.250   0.000  1.00 20.00           C
ATOM      4  O   ALA A   1      -2.300   2.350   0.000  1.00 20.00           O
HETATM    5  C1  LIG Z 101       0.000   0.000   0.000  1.00 20.00           C
HETATM    6  O1  LIG Z 101       1.100   0.000   0.000  1.00 20.00           O
HETATM    7  C1  DRG Y 201       4.000   0.000   0.000  1.00 20.00           C
HETATM    8  N1  DRG Y 201       5.100   0.000   0.000  1.00 20.00           N
CONECT    5    6
CONECT    6    5
CONECT    7    8
CONECT    8    7
END
`

const testProteinLigandCurrentPosePDB = `ATOM      1  N   ALA A   1      -4.000   0.000   0.000  1.00 20.00           N
ATOM      2  CA  ALA A   1      -2.550   0.000   0.000  1.00 20.00           C
ATOM      3  C   ALA A   1      -1.800   1.250   0.000  1.00 20.00           C
ATOM      4  O   ALA A   1      -2.300   2.350   0.000  1.00 20.00           O
ATOM      5  C1  LIG A  22       0.000   0.000   0.000  1.00 20.00           A
ATOM      6  C2  LIG A  22       1.400   0.000   0.000  1.00 20.00           A
ATOM      7 Cl   LIG A  22       3.400   0.000   0.000  1.00 20.00           C
CONECT    5    6
CONECT    6    5    7
CONECT    7    6
END
`

func TestNormalizeStructureMinimizationFormatUsesSafeOutputNames(t *testing.T) {
	tests := []struct {
		filename string
		input    string
		output   string
		name     string
	}{
		{filename: "ligand.sdf", input: "sdf", output: "sdf", name: "ligand-minimized.sdf"},
		{filename: "ligand.mol", input: "mol", output: "sdf", name: "ligand-minimized.sdf"},
		{filename: "ligand.mol2", input: "mol2", output: "sdf", name: "ligand-minimized.sdf"},
		{filename: "receptor.pdb", input: "pdb", output: "pdb", name: "receptor-minimized.pdb"},
	}

	for _, test := range tests {
		t.Run(test.filename, func(t *testing.T) {
			format, err := normalizeStructureMinimizationFormat(test.filename)
			if err != nil {
				t.Fatalf("normalize %s: %v", test.filename, err)
			}
			if format.input != test.input || format.output != test.output || format.outputName != test.name {
				t.Fatalf("unexpected normalized format: %+v", format)
			}
		})
	}

	for _, filename := range []string{"../ligand.sdf", `..\\ligand.sdf`, "ligand.xyz", ".sdf"} {
		t.Run("reject-"+filename, func(t *testing.T) {
			if _, err := normalizeStructureMinimizationFormat(filename); err == nil {
				t.Fatalf("expected unsafe or unsupported filename %q to be rejected", filename)
			}
		})
	}
}

func TestWriteStructureMinimizationSoftwareErrorReturnsSafeUnifiedContract(t *testing.T) {
	recorder := httptest.NewRecorder()
	providerErr := software.WrapOperationError(
		errors.New("micromamba failed at /secret/runtime/path"),
		"managed_python_runtime_unavailable",
		"managed Python environment is unavailable: /secret/runtime/path",
		"repair_or_prepare_the_service_owned_managed_python_environment_then_retry_the_same_provider_plan",
		true,
	)

	writeStructureMinimizationSoftwareError(recorder, 503, providerErr)
	if recorder.Code != 503 {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes())).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["code"] != "managed_python_runtime_unavailable" ||
		body["message"] != "the verified managed Python environment is unavailable" ||
		body["repair_scope"] != "same_provider_plan" || body["retryable"] != false ||
		body["terminal"] != true || body["failure_kind"] != "provider_degraded" {
		t.Fatalf("unexpected unified error contract: %#v", body)
	}
	if strings.Contains(recorder.Body.String(), "/secret/runtime/path") {
		t.Fatalf("provider implementation detail leaked: %s", recorder.Body.String())
	}
}

func TestStructureMinimizationFailureMessageDoesNotExposeRuntimeDetails(t *testing.T) {
	message := structureMinimizationFailureMessage(map[string]any{
		"message": "Invariant Violation: /secret/runtime/path/rdkit/BFGSOpt.h: bad direction in linearSearch",
	})
	if strings.Contains(message, "secret/runtime") || strings.Contains(message, "BFGSOpt") {
		t.Fatalf("runtime detail leaked into failure message: %q", message)
	}
	if message != "force-field minimization did not complete; validate the input structure and retry" {
		t.Fatalf("unexpected sanitized message: %q", message)
	}

	if got := structureMinimizationFailureMessage(map[string]any{
		"message": "a 3D conformer could not be generated for the input molecule",
	}); got != "a 3D conformer could not be generated for the input molecule" {
		t.Fatalf("expected safe helper message to be preserved, got %q", got)
	}
}

// End of structure minimization API tests.
