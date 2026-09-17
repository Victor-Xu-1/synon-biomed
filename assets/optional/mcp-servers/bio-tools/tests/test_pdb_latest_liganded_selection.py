from pdb_structures.selection import (
    formula_element_counts,
    qualifying_organic_ligands,
    select_latest_liganded_from_search,
)


def _entry(pdb_id: str, release: str, comp_id: str) -> dict:
    return {
        "rcsb_id": pdb_id,
        "rcsb_accession_info": {"initial_release_date": release},
        "rcsb_entry_info": {
            "nonpolymer_entity_count": 1,
            "nonpolymer_bound_components": [comp_id],
            "resolution_combined": [1.5],
            "structure_determination_methodology": "experimental",
        },
        "rcsb_entry_container_identifiers": {
            "polymer_entity_ids": ["1"],
            "non_polymer_entity_ids": ["2"],
        },
        "struct": {"title": f"{pdb_id} structure"},
        "exptl": [{"method": "X-RAY DIFFRACTION"}],
    }


class FakePDBClient:
    def __init__(self):
        self.entries = {
            "9AAA": _entry("9AAA", "2026-01-01", "NA"),
            "8BBB": _entry("8BBB", "2025-01-01", "G7I"),
        }
        self.components = {
            "NA": {
                "chem_comp": {"id": "NA", "name": "SODIUM ION", "formula": "Na", "formula_weight": 22.99},
                "rcsb_chem_comp_descriptor": {"SMILES": "[Na+]"},
            },
            "G7I": {
                "chem_comp": {"id": "G7I", "name": "DRUG", "formula": "C31 H25 Cl2 F N4 O3", "formula_weight": 591.47},
                "rcsb_chem_comp_descriptor": {"SMILES_stereo": "CC1=CC=CC=C1"},
            },
        }

    def get_data(self, kind: str, *ids: str) -> dict:
        if kind == "entry":
            return self.entries[ids[0]]
        if kind == "nonpolymer_entity":
            pdb_id = ids[0]
            comp_id = "NA" if pdb_id == "9AAA" else "G7I"
            return {
                "rcsb_nonpolymer_entity_container_identifiers": {
                    "entity_id": "2",
                    "nonpolymer_comp_id": comp_id,
                    "auth_asym_ids": ["A"],
                },
                "rcsb_nonpolymer_entity": {
                    "pdbx_description": self.components[comp_id]["chem_comp"]["name"],
                    "pdbx_number_of_molecules": 1,
                },
            }
        if kind == "chemcomp":
            return self.components[ids[0]]
        raise AssertionError((kind, ids))


def test_formula_parser_distinguishes_carbon_from_chlorine():
    assert formula_element_counts("Cl") == {"Cl": 1}
    assert formula_element_counts("C31 H25 Cl2 F N4 O3")["C"] == 31


def test_qualifying_ligands_excludes_common_crystallization_components():
    ligands = [
        {"comp_id": "DMS", "chem_comp": {"formula": "C2 H6 O S", "formula_weight": 78.13, "smiles": "CS(C)=O"}},
        {"comp_id": "DRG", "chem_comp": {"formula": "C12 H14 N2", "formula_weight": 186.26, "smiles": "c1ccccc1"}},
    ]
    assert [item["comp_id"] for item in qualifying_organic_ligands(ligands)] == ["DRG"]


def test_latest_liganded_selection_preserves_release_order_and_rejections():
    search = {
        "sort_by": "initial_release_date",
        "sort_direction": "desc",
        "records": [{"pdb_id": "9AAA"}, {"pdb_id": "8BBB"}],
        "truncated": False,
    }
    result = select_latest_liganded_from_search(FakePDBClient(), search)
    assert result["selected"] is True
    assert result["structure"]["pdb_id"] == "8BBB"
    assert [item["comp_id"] for item in result["organic_ligands"]] == ["G7I"]
    assert result["rejected_newer_candidates"] == [
        {"pdb_id": "9AAA", "reason": "no_qualifying_organic_ligand", "observed_comp_ids": ["NA"]}
    ]
