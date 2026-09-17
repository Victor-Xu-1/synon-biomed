package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStructureInteractionPocketGeometryAsset(t *testing.T) {
	python := os.Getenv("SYNON_STRUCTURE_TEST_PYTHON")
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 is required to verify the staged pocket geometry module")
		}
	}
	directory := t.TempDir()
	for name, content := range map[string][]byte{
		"structure_interaction_pocket_geometry.py": structureInteractionPocketGeometry,
		"structure_interaction_renderer.py":        structureInteractionDiagramRenderer,
		"structure_interaction_solvent.py":         structureInteractionSolvent,
	} {
		if err := os.WriteFile(filepath.Join(directory, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `import ast
from pathlib import Path
from structure_interaction_pocket_geometry import boundary_paths, solvent_halos, solvent_boundary_segments
renderer = ast.parse(Path('structure_interaction_renderer.py').read_text())
ast.parse(Path('structure_interaction_solvent.py').read_text())
assert any(isinstance(node, ast.ImportFrom) and node.module == 'structure_interaction_pocket_geometry' for node in renderer.body)
points = [(x,450) for x in range(700,901,10)]
assert boundary_paths(points,(800,450),[0],[],[])
assert solvent_boundary_segments(points,{0:(700,450),1:(900,450)},{1:[(1,0)]},[])
assert 'data-exposure-atom="0"' in solvent_halos({0:(700,350)},[{'atom_index':0,'accessible_fraction':.25}])
`
	if os.Getenv("SYNON_STRUCTURE_TEST_PYTHON") != "" {
		script += `
import numpy as np
from structure_interaction_solvent import receptor_surface_exposure
directions=np.array([[1.,0.,0.]]*32)
result=receptor_surface_exposure(np.array([[0.,0.,0.]]),np.array([1.7]),[('A',1,'ALA')],np.empty((0,3)),np.array([]),{('A',1,'ALA')},directions,lambda p:np.ones(len(p),dtype=bool))
assert result[('A',1,'ALA')]['fraction']==1
`
	}
	command := exec.Command(python, "-B", "-c", script)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("staged pocket geometry import failed: %v\n%s", err, output)
	}
}
