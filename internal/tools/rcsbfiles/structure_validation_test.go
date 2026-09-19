package rcsbfiles

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateCIFLocalStructure(t *testing.T) {
	const coordinates = "data_local\nloop_\n_atom_site.group_PDB\n_atom_site.Cartn_x\n_atom_site.Cartn_y\n_atom_site.Cartn_z\nATOM 1 2 3\n"
	for _, test := range []struct {
		name, content string
		invalid       bool
	}{
		{"coordinates", coordinates, false},
		{"component", "data_lig\n_chem_comp.id LIG\nloop_\n_chem_comp_atom.comp_id\n_chem_comp_atom.atom_id\nLIG C1\n", false},
		{"metadata fragment", "data_local\n_entry.id local\n", true},
		{"incomplete row", coordinates + "ATOM 4 5\n", true},
		{"unclosed text", coordinates + ";unfinished\n", true},
		{"html", "<!-- already saved -->", true},
		{"empty", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateCIF(strings.NewReader(test.content))
			if errors.Is(err, ErrInvalidStructure) != test.invalid {
				t.Fatalf("invalid=%t err=%v", test.invalid, err)
			}
		})
	}
}
