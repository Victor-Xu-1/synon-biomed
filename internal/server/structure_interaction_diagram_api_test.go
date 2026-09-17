package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStructureInteractionRuntimeAllowsBoundedColdProvisioning(t *testing.T) {
	if structureInteractionRuntimeTimeout < 5*time.Minute {
		t.Fatalf("interaction runtime timeout is too short for a cold scientific environment: %s", structureInteractionRuntimeTimeout)
	}
	if maxStructureInteractionTimeout < structureInteractionRuntimeTimeout+3*time.Minute {
		t.Fatalf("request timeout must leave execution room after cold provisioning: runtime=%s request=%s", structureInteractionRuntimeTimeout, maxStructureInteractionTimeout)
	}
}

func structureInteractionRequest(t *testing.T, values map[string]any) *http.Request {
	t.Helper()
	payload, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return httptest.NewRequest("POST", "/api/frames/frame-1/structure-interaction-diagram", bytes.NewReader(payload))
}

func TestDecodeStructureInteractionDiagramRequest(t *testing.T) {
	request, err := decodeStructureInteractionDiagramRequest(structureInteractionRequest(t, map[string]any{
		"content":  "ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.00           N\nEND\n",
		"filename": "complex.pdb", "ligand_residue_name": "d01", "smiles": "CO", "ligand_label": "candidate-1",
		"report_only": true,
	}))
	if err != nil {
		t.Fatalf("decode valid request: %v", err)
	}
	if request.LigandResidueName != "D01" || request.Smiles != "CO" || request.Filename != "complex.pdb" || !request.ReportOnly {
		t.Fatalf("normalized request = %+v", request)
	}
}

func TestDecodeStructureInteractionDiagramRequestRejectsUnsafeOrUnsupportedInput(t *testing.T) {
	tests := []map[string]any{
		{"content": "ATOM\n", "filename": "complex.cif", "ligand_residue_name": "D01", "smiles": "CO"},
		{"content": "ATOM\n", "filename": "complex.pdb", "ligand_residue_name": "D01;rm", "smiles": "CO"},
		{"content": "ATOM\n", "filename": "complex.pdb", "ligand_residue_name": "D01", "smiles": "CO\nCC"},
	}
	for index, values := range tests {
		if _, err := decodeStructureInteractionDiagramRequest(structureInteractionRequest(t, values)); err == nil {
			t.Fatalf("case %d accepted invalid input", index)
		}
	}
}

func TestStructureInteractionReportPreservesPocketGeometryReceipt(t *testing.T) {
	var report structureInteractionDiagramReport
	if err := json.Unmarshal([]byte(`{
		"contract":true,
		"ok":true,
		"engine":"Synon 2D Interaction Engine",
		"engine_release":"1.3.0",
		"pocket_radius_angstrom":4.5,
		"pocket_geometry_method":"shared-4.5A-residue-neighborhood",
		"solvent_opening_method":"1.4A-probe-with-8A-bulk-solvent-ray-clearance"
	}`), &report); err != nil {
		t.Fatalf("decode geometry receipt: %v", err)
	}
	if report.EngineRelease != "1.3.0" || report.PocketRadiusAngstrom != 4.5 {
		t.Fatalf("unexpected geometry receipt: %+v", report)
	}
	if report.PocketGeometryMethod == "" || report.SolventOpeningMethod == "" {
		t.Fatalf("missing geometry methods: %+v", report)
	}
}
