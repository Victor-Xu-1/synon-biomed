package bindingmode

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const pdbFixture = `HEADER    TEST STRUCTURE                                     5FQD
ATOM      1  N   ALA A  10      10.000  10.000  10.000  1.00 20.00           N
ATOM      2  CA  ALA A  10      11.500  10.000  10.000  1.00 20.00           C
ATOM      3  O   GLU A  11      10.000  13.000  10.000  1.00 20.00           O
ATOM      4  C   PHE A  12      13.000  12.000  10.000  1.00 20.00           C
HETATM    5  C1  LIG B 301      10.500  10.500  10.000  1.00 20.00           C
HETATM    6  C2  LIG B 301      11.500  10.500  10.000  1.00 20.00           C
HETATM    7  C3  LIG B 301      12.000  11.500  10.000  1.00 20.00           C
HETATM    8  N1  LIG B 301      11.000  12.000  10.000  1.00 20.00           N
HETATM    9  O1  LIG B 301      10.000  11.500  10.000  1.00 20.00           O
END
`

func pdbAtomLine(record string, serial int, atomName, altLoc, resName, chain string, resSeq int, x, y, z, occupancy float64, element string) string {
	return fmt.Sprintf("%-6s%5d %-4s%1s%3s %1s%4d    %8.3f%8.3f%8.3f%6.2f%6.2f          %2s",
		record, serial, atomName, altLoc, resName, chain, resSeq, x, y, z, occupancy, 20.0, element)
}

func testPDBServer(t *testing.T, body string) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "chemical/x-pdb")
		w.Write([]byte(body))
	}))
	client := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	return srv, client
}

func TestAnalyzeFindsContactsAndBuildsDiagram(t *testing.T) {
	srv, client := testPDBServer(t, pdbFixture)
	defer srv.Close()
	output, err := client.Analyze(nil, Input{PDBID: "5fqd", LigandResidue: "LIG"})
	if err != nil {
		t.Fatal(err)
	}
	if output["pdbId"] != "5FQD" {
		t.Fatalf("pdbId = %#v", output["pdbId"])
	}
	ligand := output["ligand"].(map[string]any)
	if ligand["resName"] != "LIG" || ligand["chain"] != "B" || ligand["atomCount"] != 5 {
		t.Fatalf("ligand = %#v", ligand)
	}
	summary := output["interactionSummary"].(map[string]any)
	if summary["totalContacts"].(int) == 0 {
		t.Fatalf("interaction summary = %#v", summary)
	}
	diagram := output["interactionDiagram"].(map[string]any)
	svg := diagram["svg"].(string)
	if diagram["format"] != "svg" || !strings.Contains(svg, "Protein-ligand interaction map: 5FQD") || !strings.Contains(svg, "ALA A10") {
		t.Fatalf("diagram = %#v", diagram)
	}
	if sources := output["sources"].([]map[string]any); len(sources) != 1 || sources[0]["provider"] != "binding_mode_analysis" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestAnalyzeRejectsInvalidPDBIDBeforeNetwork(t *testing.T) {
	// Use a nil client to prove no network call is made for invalid PDB IDs
	client := NewClient(Options{})
	_, err := client.Analyze(nil, Input{PDBID: "../../secret"})
	if err == nil || !strings.Contains(err.Error(), "valid four-character PDB ID") {
		t.Fatalf("error = %v", err)
	}
}

func TestAnalyzeMissingExplicitLigandReturnsNoLigandOutput(t *testing.T) {
	srv, client := testPDBServer(t, pdbFixture)
	defer srv.Close()
	output, err := client.Analyze(nil, Input{PDBID: "5FQD", LigandResidue: "MISS"})
	if err != nil {
		t.Fatal(err)
	}
	failure, ok := output["failure"].(map[string]any)
	if !ok || failure["kind"] != "binding_ligand_not_found" {
		t.Fatalf("expected binding_ligand_not_found failure, got %#v", output)
	}
}

func TestAnalyzeLigandNotFoundReturnsNoLigandOutput(t *testing.T) {
	// PDB with no suitable ligand candidates
	body := `HEADER    TEST STRUCTURE                                     5FQD
ATOM      1  N   ALA A  10      10.000  10.000  10.000  1.00 20.00           N
END
`
	srv, client := testPDBServer(t, body)
	defer srv.Close()
	output, err := client.Analyze(nil, Input{PDBID: "5FQD"})
	if err != nil {
		t.Fatal(err)
	}
	failure, ok := output["failure"].(map[string]any)
	if !ok || failure["kind"] != "binding_ligand_not_found" {
		t.Fatalf("expected binding_ligand_not_found failure, got %#v", output)
	}
}

func TestAnalyzeChainOnlySelectsMatchingChain(t *testing.T) {
	body := pdbFixture + strings.Join([]string{
		pdbAtomLine("HETATM", 20, "C1", "", "BIG", "C", 401, 30, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 21, "C2", "", "BIG", "C", 401, 31, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 22, "C3", "", "BIG", "C", 401, 32, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 23, "C4", "", "BIG", "C", 401, 33, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 24, "C5", "", "BIG", "C", 401, 34, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 25, "O1", "", "BIG", "C", 401, 35, 30, 30, 1, "O"),
	}, "\n") + "\n"
	srv, client := testPDBServer(t, body)
	defer srv.Close()
	output, err := client.Analyze(nil, Input{PDBID: "5FQD", Chain: "B"})
	if err != nil {
		t.Fatal(err)
	}
	ligand := output["ligand"].(map[string]any)
	if ligand["resName"] != "LIG" || ligand["chain"] != "B" {
		t.Fatalf("chain-only ligand = %#v", ligand)
	}
}

func TestAnalyzeLargestLigandSelectedWhenNoExplicitRequest(t *testing.T) {
	body := strings.Join([]string{
		pdbAtomLine("HETATM", 5, "C1", "", "LIG", "B", 301, 10, 10, 10, 1, "C"),
		pdbAtomLine("HETATM", 6, "C2", "", "LIG", "B", 301, 11, 10, 10, 1, "C"),
		pdbAtomLine("HETATM", 7, "C3", "", "LIG", "B", 301, 12, 10, 10, 1, "C"),
		pdbAtomLine("HETATM", 8, "N1", "", "LIG", "B", 301, 11, 11, 10, 1, "N"),
		pdbAtomLine("HETATM", 9, "O1", "", "LIG", "B", 301, 10, 11, 10, 1, "O"),
		pdbAtomLine("HETATM", 20, "C1", "", "BIG", "C", 401, 30, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 21, "C2", "", "BIG", "C", 401, 31, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 22, "C3", "", "BIG", "C", 401, 32, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 23, "C4", "", "BIG", "C", 401, 33, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 24, "C5", "", "BIG", "C", 401, 34, 30, 30, 1, "C"),
		pdbAtomLine("HETATM", 25, "O1", "", "BIG", "C", 401, 35, 30, 30, 1, "O"),
	}, "\n") + "\n"
	srv, client := testPDBServer(t, body)
	defer srv.Close()
	output, err := client.Analyze(nil, Input{PDBID: "5FQD"})
	if err != nil {
		t.Fatal(err)
	}
	ligand := output["ligand"].(map[string]any)
	if ligand["resName"] != "BIG" {
		t.Fatalf("expected largest ligand BIG, got %#v", ligand)
	}
}

func TestAnalyzeReturnsNoLigandForEmptyPDB(t *testing.T) {
	body := "HEADER    EMPTY TEST                                     5FQD\nEND\n"
	srv, client := testPDBServer(t, body)
	defer srv.Close()
	output, err := client.Analyze(nil, Input{PDBID: "5FQD"})
	if err != nil {
		t.Fatal(err)
	}
	failure, ok := output["failure"].(map[string]any)
	if !ok || failure["kind"] != "binding_ligand_not_found" {
		t.Fatalf("expected binding_ligand_not_found failure, got %#v", output)
	}
}

func TestAnalyzeHTTPErrorIsPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	client := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	_, err := client.Analyze(nil, Input{PDBID: "5FQD", LigandResidue: "LIG"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
}

func TestAnalyzeFallsBackToMMCIFWhenLegacyPDBIsUnavailable(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, ".pdb") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "chemical/x-cif")
		fmt.Fprint(w, `data_9CUO
loop_
_atom_site.group_PDB
_atom_site.id
_atom_site.type_symbol
_atom_site.auth_atom_id
_atom_site.auth_comp_id
_atom_site.auth_asym_id
_atom_site.auth_seq_id
_atom_site.Cartn_x
_atom_site.Cartn_y
_atom_site.Cartn_z
_atom_site.occupancy
ATOM 1 N N ALA A 10 10.0 10.0 10.0 1.0
ATOM 2 C CA ALA A 10 11.5 10.0 10.0 1.0
HETATM 3 C C1 LIG B 301 10.5 10.5 10.0 1.0
HETATM 4 C C2 LIG B 301 11.5 10.5 10.0 1.0
HETATM 5 C C3 LIG B 301 12.0 11.5 10.0 1.0
HETATM 6 N N1 LIG B 301 11.0 12.0 10.0 1.0
HETATM 7 O O1 LIG B 301 10.0 11.5 10.0 1.0
#
`)
	}))
	defer srv.Close()
	client := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	output, err := client.Analyze(nil, Input{PDBID: "9CUO", LigandResidue: "LIG"})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/9CUO.pdb" || paths[1] != "/9CUO.cif" {
		t.Fatalf("download paths = %v", paths)
	}
	structure := output["structure"].(map[string]any)
	if structure["coordinateFormat"] != "cif" || structure["coordinateDownloadUrl"] != srv.URL+"/9CUO.cif" {
		t.Fatalf("structure = %#v", structure)
	}
	if output["ligand"].(map[string]any)["resName"] != "LIG" {
		t.Fatalf("ligand = %#v", output["ligand"])
	}
	if output["diagnostics"].(map[string]any)["parser"] != "mmcif_distance_contacts_v1" {
		t.Fatalf("diagnostics = %#v", output["diagnostics"])
	}
}

func TestNewClientDefaults(t *testing.T) {
	client := NewClient(Options{})
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if client.httpClient == nil {
		t.Fatal("default HTTP client is nil")
	}
	if client.baseURL != defaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", client.baseURL, defaultBaseURL)
	}
	if client.maxBytes != defaultMaxBytes {
		t.Fatalf("maxBytes = %d, want %d", client.maxBytes, defaultMaxBytes)
	}
}

func TestAnalyzeNilClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "chemical/x-pdb")
		w.Write([]byte(pdbFixture))
	}))
	defer srv.Close()
	// nil client auto-creates a default, but we need to override the URL
	client := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	output, err := client.Analyze(nil, Input{PDBID: "5fqd", LigandResidue: "LIG"})
	if err != nil {
		t.Fatal(err)
	}
	if output["pdbId"] != "5FQD" {
		t.Fatalf("pdbId = %#v", output["pdbId"])
	}
}
