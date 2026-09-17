package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAgentSavedArtifactStructureRejectsHeaderOnlyPDBQT(t *testing.T) {
	validAtom := "ATOM      1  C1  LIG A   1       1.250  -2.500   3.750  1.00  0.00      0.000 C\n"
	tests := []struct {
		name    string
		path    string
		content string
		wantErr error
	}{
		{name: "valid pdbqt", path: "ligand.pdbqt", content: "REMARK ligand\nMODEL 1\n" + validAtom + "ENDMDL\n"},
		{name: "header only pdbqt", path: "ligand.pdbqt", content: "REMARK ranked poses\n", wantErr: errAgentSavedArtifactStructureEmpty},
		{name: "header only pdb", path: "complex.pdb", content: "HEADER test\nEND\n", wantErr: errAgentSavedArtifactStructureEmpty},
		{name: "unrelated format", path: "report.md", content: "# report\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), filepath.Base(test.path))
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			err = validateAgentSavedArtifactStructure(test.path, file)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v want=%v", err, test.wantErr)
			}
			if offset, seekErr := file.Seek(0, 1); seekErr != nil || offset != 0 {
				t.Fatalf("snapshot offset=%d err=%v", offset, seekErr)
			}
		})
	}
}
