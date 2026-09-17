package server

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/software"
)

func TestStructureElectrostaticRuntimeUsesPinnedSmallStandardToolchain(t *testing.T) {
	request := newStructureElectrostaticRuntimeRequest("")
	if request.Provider != software.LocalProviderID || request.Executable != "python" {
		t.Fatalf("runtime request=%#v", request)
	}
	packages := make([]string, 0, len(request.Packages))
	for _, requirement := range request.Packages {
		packages = append(packages, requirement.Spec)
	}
	for _, expected := range []string{"apbs=3.4.1", "pdb2pqr==3.7.1", "rdkit==2024.3.5"} {
		if !warmupContainsString(packages, expected) {
			t.Fatalf("runtime packages=%v, missing %q", packages, expected)
		}
	}
	if !warmupContainsString(request.Channels, "conda-forge") {
		t.Fatalf("runtime channels=%v", request.Channels)
	}
}

func TestStructureElectrostaticMapUsesTypedPDB2PQRInputAPI(t *testing.T) {
	for _, expected := range [][]byte{
		[]byte("from pdb2pqr import inputgen, psize"),
		[]byte("space=MESH_SPACING_ANGSTROM"),
		[]byte("float(ionic_strength)"),
		[]byte("CALCULATION_MODE = \"separate-components\""),
		[]byte("CONTRACT_VERSION = \"2.0\""),
		[]byte("INPUT_DIGEST_DOMAIN = b\"synon-biomed:structure-electrostatic-input:v1\""),
		[]byte("to_bytes(8, \"little\")"),
		[]byte("component_jobs.append((\"protein\", None, protein_pqr, protein_dx))"),
		[]byte("component_jobs.extend((\"ligand\", key, ligand_pqr, ligand_dx)"),
		[]byte("--ligand-component"),
		[]byte("input_path, component_grid_values = generate_apbs_input("),
		[]byte("molecule_pqr, system_pqr, output_dx, args.ionic_strength"),
	} {
		if !bytes.Contains(structureElectrostaticMapProgram, expected) {
			t.Fatalf("electrostatic generator is missing %q", expected)
		}
	}
	if bytes.Contains(structureElectrostaticMapProgram, []byte("\"inputgen\",")) {
		t.Fatal("electrostatic generator reintroduced the PDB2PQR 3.7.1 inputgen CLI type bug")
	}
}

func TestStructureElectrostaticMapRequiresSeparateAlignedComponentGrids(t *testing.T) {
	request := structureElectrostaticMapRequest{
		Content: "MODEL        1\nATOM      1  N   ALA A   1\nEND\n", LigandMolBlock: "ligand\nM  END\n",
		PH: 7.4, IonicStrengthMolar: 0.15,
	}
	components := structureElectrostaticMapComponents(request)
	if len(components) != 2 || components[0].Role != "protein" || components[1].Role != "ligand" {
		t.Fatalf("components=%v", components)
	}
	grid := structureElectrostaticGridReport{
		Counts: []int{2, 2, 2}, Origin: []float64{0, 0, 0}, Delta: []float64{1, 1, 1},
		ValueCount: 8, Minimum: -1, Maximum: 1,
	}
	proteinGrid := grid
	ligandGrid := grid
	report := structureElectrostaticMapReport{
		PH: 7.4, IonicStrengthMolar: 0.15, ProteinAtomCount: 8, LigandAtomCount: 4, TotalAtomCount: 12,
		InputSHA256: structureElectrostaticInputSHA256(request), ColorRange: []float64{-5, 5}, ForceField: "AMBER", MeshSpacingAngstrom: 0.65,
		EngineVersion: "3.4.1", PreparationEngine: "PDB2PQR + RDKit", PreparationEngineVersion: "pdb2pqr 3.7.1; RDKit 2024.3.5",
		ProteinChargeMethod: "PDB2PQR AMBER with PROPKA protonation", LigandChargeMethod: "RDKit Gasteiger with explicit hydrogens",
		Grids: structureElectrostaticGridReports{Protein: &proteinGrid, Ligand: &ligandGrid},
	}
	if !structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatalf("aligned component report was rejected: %#v", report.Grids)
	}
	exactDigest := report.InputSHA256
	differentRequest := request
	differentRequest.LigandMolBlock = "different ligand\nM  END\n"
	report.InputSHA256 = structureElectrostaticInputSHA256(differentRequest)
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("legacy report with a well-formed digest for different content was accepted")
	}
	report.InputSHA256 = exactDigest
	misaligned := grid
	misaligned.Origin = []float64{0.25, 0, 0}
	report.Grids.Ligand = &misaligned
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("misaligned protein and ligand grids were accepted")
	}
}

func TestStructureElectrostaticRuntimeFixturesCoverProteinAndLigandInputs(t *testing.T) {
	protein, err := os.ReadFile("testdata/alanine.pdb")
	if err != nil {
		t.Fatalf("read protein fixture: %v", err)
	}
	ligand, err := os.ReadFile("testdata/benzene.sdf")
	if err != nil {
		t.Fatalf("read ligand fixture: %v", err)
	}
	if !bytes.Contains(protein, []byte("ATOM  ")) || !bytes.Contains(ligand, []byte("M  END")) {
		t.Fatal("electrostatic component fixtures do not cover protein and ligand inputs")
	}
}

func TestStructureElectrostaticInputDigestCanonicalVectors(t *testing.T) {
	tests := []struct {
		name     string
		request  structureElectrostaticMapRequest
		expected string
	}{
		{
			name: "legacy",
			request: structureElectrostaticMapRequest{
				Content: "ATOM\n", LigandMolBlock: "ligand\nM  END\n",
			},
			expected: "7f1faffd7cfa9e6101a8da2164dd42ca309e319540f684e26c2ed4f5dbbad9b0",
		},
		{
			name: "batch",
			request: structureElectrostaticMapRequest{
				Content: "ATOM\n",
				LigandMolBlocks: []structureElectrostaticLigandInput{
					{Key: "D02", MolBlock: "two\nM  END\n"},
					{Key: "D01", MolBlock: "one\nM  END\n"},
				},
			},
			expected: "58bc763cbb1dd505055834ac4c95f1c9a9829d32017dd88c8f929bc807178b37",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if digest := structureElectrostaticInputSHA256(test.request); digest != test.expected {
				t.Fatalf("digest=%q, expected %q", digest, test.expected)
			}
		})
	}
}

func TestDecodeStructureElectrostaticMapRequestNormalizesScientificSettings(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"content":              "ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.00           N\nEND\n",
		"filename":             "9R1Z.cif",
		"ligand_mol_block":     "ligand\n  Synon\n\n  0  0  0  0  0  0  0  0  0  0  1 V2000\nM  END\n",
		"ph":                   7.444,
		"ionic_strength_molar": 0.1546,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := decodeStructureElectrostaticMapRequest(
		httptest.NewRequest("POST", "/api/frames/frame/structure-electrostatic-map", bytes.NewReader(payload)),
	)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if request.Filename != "9R1Z.cif" || request.PH != 7.44 || request.IonicStrengthMolar != 0.155 {
		t.Fatalf("normalized request=%+v", request)
	}
	if !request.ligandMolBlockPresent || request.ligandMolBlocksPresent {
		t.Fatalf("legacy request mode=%+v", request)
	}
}

func TestDecodeStructureElectrostaticMapRequestAcceptsAndSortsBoundedLigandBatch(t *testing.T) {
	molBlock := "ligand\n  Synon\n\n  0  0  0  0  0  0  0  0  0  0  1 V2000\nM  END\n"
	payload, err := json.Marshal(map[string]any{
		"content":  "ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.00           N\nEND\n",
		"filename": "complex.pdb",
		"ligand_mol_blocks": []map[string]string{
			{"key": "D02", "mol_block": molBlock},
			{"key": "D01", "mol_block": molBlock},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := decodeStructureElectrostaticMapRequest(
		httptest.NewRequest("POST", "/api/frames/frame/structure-electrostatic-map", bytes.NewReader(payload)),
	)
	if err != nil {
		t.Fatalf("decode batch request: %v", err)
	}
	if !structureElectrostaticIsBatchRequest(request) || request.LigandMolBlock != "" || len(request.LigandMolBlocks) != 2 ||
		request.LigandMolBlocks[0].Key != "D01" || request.LigandMolBlocks[1].Key != "D02" {
		t.Fatalf("normalized batch request=%+v", request)
	}
	components := structureElectrostaticMapComponents(request)
	if len(components) != 3 || components[0].Role != "protein" || components[1].Key != "D01" ||
		components[2].Key != "D02" || components[1].DXFilename == components[2].DXFilename {
		t.Fatalf("batch components=%+v", components)
	}
}

func TestDecodeStructureElectrostaticMapRequestRejectsAmbiguousOrAdversarialLigandBatch(t *testing.T) {
	validMolBlock := "ligand\nM  END\n"
	validContent := "ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.00           N\nEND\n"
	tests := []map[string]any{
		{"content": validContent, "filename": "bad.pdb", "ligand_mol_block": "", "ligand_mol_blocks": []any{}},
		{"content": validContent, "filename": "bad.pdb", "ligand_mol_blocks": nil},
		{"content": validContent, "filename": "bad.pdb", "ph": nil},
		{"content": validContent, "filename": "bad.pdb", "ligand_mol_blocks": []any{}},
		{"content": validContent, "filename": "bad.pdb", "ligand_mol_blocks": []map[string]string{{"key": "D01", "mol_block": validMolBlock}, {"key": "d01", "mol_block": validMolBlock}}},
		{"content": validContent, "filename": "bad.pdb", "ligand_mol_blocks": []map[string]string{{"key": "../D01", "mol_block": validMolBlock}}},
		{"content": validContent, "filename": "bad.pdb", "ligand_mol_blocks": []map[string]string{{"key": "D01", "mol_block": "not a mol block"}}},
	}
	tooMany := make([]map[string]string, maxStructureElectrostaticLigandCount+1)
	for index := range tooMany {
		tooMany[index] = map[string]string{"key": "D" + string(rune('A'+index)), "mol_block": validMolBlock}
	}
	tests = append(tests, map[string]any{"content": validContent, "filename": "bad.pdb", "ligand_mol_blocks": tooMany})
	largeMolBlock := strings.Repeat("x", maxStructureElectrostaticBatchBytes/3) + "\nM  END\n"
	tests = append(tests, map[string]any{
		"content": validContent, "filename": "bad.pdb",
		"ligand_mol_blocks": []map[string]string{{"key": "D01", "mol_block": largeMolBlock}, {"key": "D02", "mol_block": largeMolBlock}, {"key": "D03", "mol_block": largeMolBlock}, {"key": "D04", "mol_block": largeMolBlock}},
	})
	for index, values := range tests {
		payload, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeStructureElectrostaticMapRequest(
			httptest.NewRequest("POST", "/api/frames/frame/structure-electrostatic-map", bytes.NewReader(payload)),
		); err == nil {
			t.Fatalf("batch case %d accepted invalid request", index)
		}
	}
}

func TestStructureElectrostaticMapValidatesKeyedBatchCountsAndSharedGrid(t *testing.T) {
	request := structureElectrostaticMapRequest{
		Content:         "ATOM      1  N   ALA A   1\nEND\n",
		LigandMolBlocks: []structureElectrostaticLigandInput{{Key: "D01", MolBlock: "one\nM  END\n"}, {Key: "D02", MolBlock: "two\nM  END\n"}},
		PH:              7.4, IonicStrengthMolar: 0.15,
	}
	grid := structureElectrostaticGridReport{
		Counts: []int{2, 2, 2}, Origin: []float64{0, 0, 0}, Delta: []float64{1, 1, 1},
		ValueCount: 8, Minimum: -1, Maximum: 1,
	}
	report := structureElectrostaticMapReport{
		PH: 7.4, IonicStrengthMolar: 0.15, ProteinAtomCount: 8, LigandAtomCount: 9, TotalAtomCount: 17,
		InputSHA256: structureElectrostaticInputSHA256(request), ColorRange: []float64{-5, 5}, ForceField: "AMBER", MeshSpacingAngstrom: 0.65,
		EngineVersion: "3.4.1", PreparationEngine: "PDB2PQR + RDKit", PreparationEngineVersion: "pdb2pqr 3.7.1; RDKit 2024.3.5",
		ProteinChargeMethod: "PDB2PQR AMBER with PROPKA protonation", LigandChargeMethod: "RDKit Gasteiger with explicit hydrogens",
		Grids:            structureElectrostaticGridReports{Protein: &grid, Ligands: map[string]structureElectrostaticGridReport{"D01": grid, "D02": grid}},
		LigandAtomCounts: map[string]int{"D01": 4, "D02": 5},
	}
	if !structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatalf("valid batch report rejected: %+v", report)
	}
	exactDigest := report.InputSHA256
	differentRequest := request
	differentRequest.LigandMolBlocks = append([]structureElectrostaticLigandInput(nil), request.LigandMolBlocks...)
	differentRequest.LigandMolBlocks[0].MolBlock = "different ligand\nM  END\n"
	report.InputSHA256 = structureElectrostaticInputSHA256(differentRequest)
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("batch report with a well-formed digest for different content was accepted")
	}
	report.InputSHA256 = exactDigest
	report.ColorRange = []float64{-10, 10}
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("batch report with a non-authoritative color range was accepted")
	}
	report.ColorRange = []float64{-5, 5}
	report.EngineVersion = ""
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("batch report without bounded engine provenance was accepted")
	}
	report.EngineVersion = "3.4.1"
	report.LigandAtomCounts["D02"] = 6
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("batch report with mismatched ligand atom total was accepted")
	}
	report.LigandAtomCounts["D02"] = 5
	shifted := grid
	shifted.Origin = []float64{0.5, 0, 0}
	report.Grids.Ligands["D02"] = shifted
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("batch report with a misaligned ligand grid was accepted")
	}
}

func TestStructureElectrostaticMapKeepsLegacyResponseShapeAndAddsBatchMapsOnlyForBatch(t *testing.T) {
	legacy := structureElectrostaticMapResponsePayload(
		structureElectrostaticMapRequest{LigandMolBlock: "ligand\nM  END\n"},
		map[string]any{"protein": "protein", "ligand": "ligand"},
		map[string]any{"D01": "batch"},
		structureElectrostaticMapReport{},
		map[string]any{},
	)
	legacyMaps := legacy["potential_maps"].(map[string]any)
	if _, exists := legacyMaps["ligands"]; exists {
		t.Fatalf("legacy response unexpectedly changed shape: %+v", legacy)
	}
	batch := structureElectrostaticMapResponsePayload(
		structureElectrostaticMapRequest{LigandMolBlocks: []structureElectrostaticLigandInput{{Key: "D01"}}},
		map[string]any{"protein": "protein"},
		map[string]any{"D01": "batch"},
		structureElectrostaticMapReport{},
		map[string]any{},
	)
	batchMaps := batch["potential_maps"].(map[string]any)
	if _, exists := batchMaps["ligands"]; !exists {
		t.Fatalf("batch response omitted keyed ligand maps: %+v", batch)
	}
}

func TestStructureElectrostaticBatchCacheKeyNormalizesOrderAndIsolatesContent(t *testing.T) {
	access := workspace.KernelFrameAccess{
		UserID: "user-a",
		Frame: workspace.Frame{
			ID: "frame-a", ProjectID: "project-a", IncarnationID: "frame-incarnation-a",
		},
		RootFrameIncarnationID: "root-incarnation-a",
	}
	decode := func(ligands []map[string]string) structureElectrostaticMapRequest {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"content": "ATOM      1  N   ALA A   1\nEND\n", "filename": "complex.pdb", "ligand_mol_blocks": ligands,
		})
		if err != nil {
			t.Fatal(err)
		}
		request, err := decodeStructureElectrostaticMapRequest(
			httptest.NewRequest("POST", "/api/frames/frame/structure-electrostatic-map", bytes.NewReader(payload)),
		)
		if err != nil {
			t.Fatalf("decode batch: %v", err)
		}
		return request
	}
	forward := decode([]map[string]string{
		{"key": "D01", "mol_block": "one\nM  END\n"},
		{"key": "D02", "mol_block": "two\nM  END\n"},
	})
	reversed := decode([]map[string]string{
		{"key": "D02", "mol_block": "two\nM  END\n"},
		{"key": "D01", "mol_block": "one\nM  END\n"},
	})
	baseline := structureElectrostaticMapCacheKey(access, forward)
	if got := structureElectrostaticMapCacheKey(access, reversed); got != baseline {
		t.Fatalf("normalized ligand order changed cache key: got %q want %q", got, baseline)
	}
	contentChanged := decode([]map[string]string{
		{"key": "D01", "mol_block": "changed\nM  END\n"},
		{"key": "D02", "mol_block": "two\nM  END\n"},
	})
	if got := structureElectrostaticMapCacheKey(access, contentChanged); got == baseline {
		t.Fatal("ligand content change reused the batch cache key")
	}
	keyChanged := decode([]map[string]string{
		{"key": "D01", "mol_block": "one\nM  END\n"},
		{"key": "D03", "mol_block": "two\nM  END\n"},
	})
	if got := structureElectrostaticMapCacheKey(access, keyChanged); got == baseline {
		t.Fatal("ligand key change reused the batch cache key")
	}
}

func TestStructureElectrostaticMapRejectsPerLigandAndAggregateReportBounds(t *testing.T) {
	grid := structureElectrostaticGridReport{
		Counts: []int{2, 2, 2}, Origin: []float64{0, 0, 0}, Delta: []float64{1, 1, 1},
		ValueCount: 8, Minimum: -1, Maximum: 1,
	}
	request := structureElectrostaticMapRequest{
		Content:         "ATOM      1  N   ALA A   1\nEND\n",
		LigandMolBlocks: []structureElectrostaticLigandInput{{Key: "D01"}, {Key: "D02"}},
		PH:              7.4, IonicStrengthMolar: 0.15,
	}
	report := structureElectrostaticMapReport{
		PH: 7.4, IonicStrengthMolar: 0.15, InputSHA256: structureElectrostaticInputSHA256(request),
		ColorRange: []float64{-5, 5}, ForceField: "AMBER", MeshSpacingAngstrom: 0.65,
		EngineVersion: "3.4.1", PreparationEngine: "PDB2PQR + RDKit", PreparationEngineVersion: "pdb2pqr 3.7.1; RDKit 2024.3.5",
		ProteinChargeMethod: "PDB2PQR AMBER with PROPKA protonation", LigandChargeMethod: "RDKit Gasteiger with explicit hydrogens",
		ProteinAtomCount: 8, LigandAtomCount: maxStructureElectrostaticPerLigand + 2,
		TotalAtomCount: maxStructureElectrostaticPerLigand + 10,
		Grids: structureElectrostaticGridReports{
			Protein: &grid, Ligands: map[string]structureElectrostaticGridReport{"D01": grid, "D02": grid},
		},
		LigandAtomCounts: map[string]int{"D01": maxStructureElectrostaticPerLigand + 1, "D02": 1},
	}
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("report exceeding the per-ligand atom bound was accepted")
	}

	request.LigandMolBlocks = []structureElectrostaticLigandInput{{Key: "D01"}, {Key: "D02"}, {Key: "D03"}, {Key: "D04"}, {Key: "D05"}}
	report.InputSHA256 = structureElectrostaticInputSHA256(request)
	report.Grids.Ligands = map[string]structureElectrostaticGridReport{
		"D01": grid, "D02": grid, "D03": grid, "D04": grid, "D05": grid,
	}
	report.LigandAtomCounts = map[string]int{
		"D01": maxStructureElectrostaticPerLigand,
		"D02": maxStructureElectrostaticPerLigand,
		"D03": maxStructureElectrostaticPerLigand,
		"D04": maxStructureElectrostaticPerLigand,
		"D05": 1,
	}
	report.LigandAtomCount = maxStructureElectrostaticLigandAtoms + 1
	report.TotalAtomCount = report.ProteinAtomCount + report.LigandAtomCount
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("report exceeding the aggregate ligand atom bound was accepted")
	}

	report.ProteinAtomCount = maxStructureElectrostaticProteinAtoms + 1
	report.LigandAtomCount = 5
	report.TotalAtomCount = report.ProteinAtomCount + report.LigandAtomCount
	report.LigandAtomCounts = map[string]int{"D01": 1, "D02": 1, "D03": 1, "D04": 1, "D05": 1}
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("report exceeding the prepared protein atom bound was accepted")
	}
	report.ProteinAtomCount = 8

	largeGrid := structureElectrostaticGridReport{
		Counts: []int{100, 100, 600}, Origin: []float64{0, 0, 0}, Delta: []float64{1, 1, 1},
		ValueCount: maxStructureElectrostaticGridValues, Minimum: -1, Maximum: 1,
	}
	request.LigandMolBlocks = []structureElectrostaticLigandInput{{Key: "D01"}, {Key: "D02"}, {Key: "D03"}, {Key: "D04"}}
	report.InputSHA256 = structureElectrostaticInputSHA256(request)
	report.LigandAtomCount = 4
	report.TotalAtomCount = 12
	report.LigandAtomCounts = map[string]int{"D01": 1, "D02": 1, "D03": 1, "D04": 1}
	report.Grids = structureElectrostaticGridReports{
		Protein: &largeGrid,
		Ligands: map[string]structureElectrostaticGridReport{
			"D01": largeGrid, "D02": largeGrid, "D03": largeGrid, "D04": largeGrid,
		},
	}
	if structureElectrostaticReportMatchesRequest(report, request) {
		t.Fatal("report exceeding the aggregate grid-value bound was accepted")
	}
}

func TestDecodeStructureElectrostaticMapRequestRejectsUnsafeOrUnboundedInput(t *testing.T) {
	tests := []map[string]any{
		{"content": "", "filename": "empty.pdb"},
		{"content": "ATOM\x00", "filename": "bad.pdb"},
		{"content": "ATOM\n", "filename": "bad.exe"},
		{"content": "ATOM\n", "filename": "bad.pdb", "ph": 15},
		{"content": "ATOM\n", "filename": "bad.pdb", "ionic_strength_molar": 2},
		{"content": "ATOM\n", "filename": "bad.pdb", "ligand_mol_block": strings.Repeat("x", maxStructureElectrostaticLigandBytes+1)},
		{"content": "ATOM\n", "filename": "bad.pdb", "unexpected": true},
	}
	for index, values := range tests {
		payload, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeStructureElectrostaticMapRequest(
			httptest.NewRequest("POST", "/api/frames/frame/structure-electrostatic-map", bytes.NewReader(payload)),
		); err == nil {
			t.Fatalf("case %d accepted invalid request", index)
		}
	}
}

func TestDecodeStructureElectrostaticMapRequestRejectsTrailingPayload(t *testing.T) {
	payload := []byte(`{"content":"ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.00           N\nEND\n","filename":"structure.pdb"}`)
	for _, trailing := range []string{" {}", " trailing"} {
		if _, err := decodeStructureElectrostaticMapRequest(
			httptest.NewRequest("POST", "/api/frames/frame/structure-electrostatic-map", bytes.NewReader(append(payload, trailing...))),
		); err == nil {
			t.Fatalf("accepted trailing payload %q", trailing)
		}
	}
}

func TestEncodeStructureElectrostaticDXRoundTripsGzipPayload(t *testing.T) {
	dx := []byte("object 1 class gridpositions counts 2 2 2\nobject 2 class gridconnections counts 2 2 2\nobject 3 class array type double rank 0 items 8 data follows\n-1 0 1 2 3 4 5 6\n")
	encoded, err := encodeStructureElectrostaticDX(dx)
	if err != nil {
		t.Fatalf("encode DX: %v", err)
	}
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if !bytes.Equal(decoded, dx) {
		t.Fatalf("DX round trip=%q", decoded)
	}
}

func TestReadStructureElectrostaticMapReportRejectsTrailingPayload(t *testing.T) {
	directory := t.TempDir()
	content := `{"color_range":[-5,5]} {"unexpected":true}`
	if err := os.WriteFile(filepath.Join(directory, "electrostatic-report.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readStructureElectrostaticMapReport(directory); err == nil {
		t.Fatal("electrostatic report with trailing JSON was accepted")
	}
}
