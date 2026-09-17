package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestBundledBioSearchCriteriaPreserveReferenceSemanticsWithBroadDefaults(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	lib := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib")
	script := `
import datetime
import json
import sys

sys.path.insert(0, sys.argv[1])

import mcp_biorxiv.server as biorxiv
import mcp_chembl.server as chembl
import mcp_clinical_trials.server as trials
import mcp_drug_regulatory.server as drug_regulatory
import mcp_literature.server as literature
import mcp_structures_interactions.server as structures
from mcp_servers_common.criteria import blank, none_if_blank, reject_blank
from openalex_works.tool import OpenAlexWorks
from openfda_drugsfda.spec import SearchSpec
from openfda_labels.client import OpenFDAClient
from pdb_structures.search import build_query, search_structures

drug_label_discovery = (drug_regulatory.search_drug_labels.__doc__ or "").lower()
for term in ("authoritative", "drug", "label", "parameters", "clinical pharmacology", "pharmacokinetics", "pk"):
    assert term in drug_label_discovery, (term, drug_label_discovery)

fixed_today = datetime.date(2026, 7, 24)
biorxiv._today = lambda: fixed_today
default_window = ("2026-05-25", "2026-07-24", None)
assert biorxiv._window({}) == default_window
assert biorxiv._window({"date_from": None}) == default_window
assert biorxiv._window({"date_from": "2024-01-01"}) == default_window
assert biorxiv._window({"date_to": "2024-01-31"}) == default_window
assert biorxiv._window({"date_from": "2024-01-01", "date_to": "2024-01-31"}) == ("2024-01-01", "2024-01-31", None)
assert [biorxiv._limit({"limit": value}) for value in (0, 1, 100, 101)] == [1, 1, 100, 100]

for args in ({}, {"condition": ""}, {"condition": "   "}, {"unknown": "value"}):
    try:
        trials.search_trials_params(args)
    except ValueError as exc:
        assert str(exc) == (
            "search_trials needs at least one of: condition, intervention, "
            "status, phase, study_type, location, sponsor, advanced_query "
            "(page_token continuations must repeat the original criteria)"
        )
    else:
        raise AssertionError(f"ClinicalTrials accepted empty criteria: {args!r}")
assert trials.search_trials_params({"condition": "NEK7"}) == {"query.cond": "NEK7"}
assert trials.search_trials_params({"condition": " ", "intervention": "NEK7"}) == {"query.intr": "NEK7"}
assert trials.search_trials_params({"condition": "null", "intervention": "NEK7", "advanced_query": "None"}) == {"query.intr": "NEK7"}
assert [trials._page_size({"page_size": value}, 10) for value in (0, 1, 1000, 1001)] == [10, 1, 1000, 1000]
assert trials._page_size({"max_rows": 25}, 10) == 25
assert trials._page_size({"page_size": 20, "max_rows": 20}, 10) == 20
try:
    trials._page_size({"page_size": 20, "max_rows": 10}, 10)
except ValueError as exc:
    assert str(exc) == "page_size and max_rows must match when both are provided"
else:
    raise AssertionError("ClinicalTrials accepted conflicting page_size and max_rows")

class Targets:
    def __init__(self):
        self.calls = []
    def paginate(self, resource, params, max_records):
        self.calls.append((resource, params, max_records))
        return [], 0

targets = Targets()
chembl._targets = lambda: targets
empty_target_page = {"count": 0, "total": 0, "offset": 0, "has_more": False,
                     "next_offset": None, "targets": [], "truncated": False}
assert json.loads(chembl.target_search({})) == empty_target_page
assert targets.calls == [("target", {}, 50)]
targets.calls.clear()
assert json.loads(chembl.target_search({"gene_symbol": None})) == empty_target_page
assert targets.calls == [("target", {}, 50)]
targets.calls.clear()
assert json.loads(chembl.target_search({"gene_symbol": "   "})) == empty_target_page
assert targets.calls == []
assert json.loads(chembl.target_search({"gene_symbol": " ", "organism": "Homo sapiens", "limit": 1001})) == empty_target_page
assert targets.calls == [("target", {"organism__icontains": "Homo sapiens"}, 1000)]

class OpenAlexClient:
    def __init__(self):
        self.calls = []
    def get(self, path, params=None):
        self.calls.append((path, params))
        return {"meta": {"count": 0}, "results": []}

client = OpenAlexClient()
openalex = OpenAlexWorks(client)
for kwargs in ({}, {"query": ""}):
    try:
        openalex.search_works(**kwargs)
    except ValueError as exc:
        assert str(exc) == "pass a query and/or at least one filter"
    else:
        raise AssertionError(f"OpenAlex accepted empty criteria: {kwargs!r}")
openalex.search_works(year_from=2024)
assert client.calls[-1][1]["filter"] == "publication_year:>2023"
openalex.search_works(year_to=2024)
assert client.calls[-1][1]["filter"] == "publication_year:<2025"
openalex.search_works(year_from=2025, year_to=2024)
assert client.calls[-1][1]["filter"] == "publication_year:2025-2024"
class OpenAlexRowsClient:
    def get(self, path, params=None):
        return {"meta": {"count": 600}, "results": [{"id": value} for value in range(600)]}

assert len(OpenAlexWorks(OpenAlexRowsClient())._list("/works", {}, 0)["results"]) == 1
assert len(OpenAlexWorks(OpenAlexRowsClient())._list("/works", {}, 501)["results"]) == 500

for spec in (SearchSpec(),):
    try:
        spec.to_search()
    except ValueError as exc:
        assert str(exc) == "empty SearchSpec: provide at least one filter"
    else:
        raise AssertionError("Drugs@FDA accepted empty criteria")
assert SearchSpec(submission_date_from="20250101").to_search().endswith("[20250101 TO 30000101]")
assert SearchSpec(submission_date_to="20250131").to_search().endswith("[19000101 TO 20250131]")

try:
    build_query()
except ValueError as exc:
    assert str(exc) == (
        "at least one search criterion is required "
        "(text / organism / taxonomy_id / uniprot_accession / "
        "experimental_method / max_resolution / ligand_comp_id)"
    )
else:
    raise AssertionError("PDB accepted empty criteria")
taxonomy_query = build_query(taxonomy_id=9606)
assert taxonomy_query == {
    "type": "terminal",
    "service": "text",
    "parameters": {
        "attribute": "rcsb_entity_source_organism.taxonomy_lineage.id",
        "operator": "exact_match",
        "value": "9606",
    },
}
class PDBClient:
    def post_search(self, payload):
        return None
for max_rows in (0, 1001):
    try:
        search_structures(PDBClient(), text="NEK7", max_rows=max_rows)
    except ValueError as exc:
        assert str(exc) == "max_rows must be 1..1000"
    else:
        raise AssertionError(f"PDB accepted max_rows={max_rows}")
assert search_structures(PDBClient(), text="NEK7", max_rows=1)["total_count"] == 0

def blank_error(field):
    return (
        f"{field} must be non-empty when provided \u2014 "
        "an empty search criterion is not a search (omit the parameter "
        "for the tool's documented default, or provide a value)"
    )

def expect_value_error(expected, call):
    try:
        call()
    except ValueError as exc:
        assert str(exc) == expected, (str(exc), expected)
    else:
        raise AssertionError(f"expected ValueError: {expected}")

assert [blank(value) for value in (None, False, 0, [], "NEK7", "", " \t")] == [
    False, False, False, False, False, True, True,
]
assert none_if_blank(" ") is None
assert none_if_blank(None) is None
assert none_if_blank("NEK7") == "NEK7"
expect_value_error(
    blank_error("alpha, zeta"),
    lambda: reject_blank({"zeta": " ", "kept": "x"}, alpha=""),
)

literature_calls = []
class LiteratureOpenAlex:
    def search_works(self, **kwargs):
        literature_calls.append("works")
        return {"call": "works", "args": kwargs}
    def search_authors(self, query, max_records=25):
        literature_calls.append("authors")
        return {"call": "authors", "query": query, "max_records": max_records}

literature._openalex = lambda: literature_calls.append("openalex") or LiteratureOpenAlex()
works_absent = literature.openalex_search_works()
works_blank = literature.openalex_search_works(query=" ")
assert works_absent["args"]["query"] is None
assert works_blank["args"]["query"] is None
assert literature.openalex_search_works(query=" ", year_from=2024)["args"]["year_from"] == 2024
literature_calls.clear()
expect_value_error(
    blank_error("venue"),
    lambda: literature.openalex_search_works(query="NEK7", venue=" "),
)
assert literature_calls == []
expect_value_error("query must be non-empty", lambda: literature.openalex_search_authors(" "))
assert literature_calls == []
expect_value_error("venue must be non-empty", lambda: literature.openalex_venue_info(" "))
assert literature_calls == []

class LiteratureArxiv:
    def search(self, **kwargs):
        literature_calls.append("arxiv")
        return kwargs

literature._arxiv = lambda: literature_calls.append("arxiv-factory") or LiteratureArxiv()
assert literature.arxiv_search(query=" ")["query"] is None
assert literature.arxiv_search(query=" ", category="q-bio.GN")["category"] == "q-bio.GN"
literature_calls.clear()
expect_value_error(
    blank_error("category"),
    lambda: literature.arxiv_search(query="NEK7", category=" "),
)
assert literature_calls == []

class DrugApplicationResult:
    records = []
    total = 0
    last_updated = None

class DrugApplications:
    def retrieve(self, spec):
        drug_calls.append("applications")
        return DrugApplicationResult()

drug_calls = []
drug_regulatory._drugsfda = lambda: DrugApplications()
assert drug_regulatory.search_drug_applications(brand="aspirin")["total"] == 0
drug_calls.clear()
expect_value_error(
    "raw_search must be non-empty",
    lambda: drug_regulatory.search_drug_applications(raw_search=" "),
)
assert drug_calls == []
expect_value_error(
    "raw_search must be non-empty",
    lambda: drug_regulatory.search_drug_applications(brand="aspirin", raw_search=" "),
)
assert drug_calls == []
drug_regulatory._labels = lambda: object()
def run_labels(spec, client, sections=None, max_records=None):
    drug_calls.append(("labels", max_records))
    return {"search": spec, "total": 0, "count": 0, "records": []}
drug_regulatory.run_spec = run_labels
assert drug_regulatory.search_drug_labels(brand_name="aspirin")["total"] == 0
assert drug_calls == [("labels", 50)]
drug_calls.clear()
assert drug_regulatory.search_drug_labels(brand_name="aspirin", max_records=7)["total"] == 0
assert drug_calls == [("labels", 7)]
drug_calls.clear()
expect_value_error(
    "raw_search must be non-empty",
    lambda: drug_regulatory.search_drug_labels(raw_search=" "),
)
assert drug_calls == []

bounded_requests = []
bounded_client = OpenFDAClient(max_retries=0)
bounded_client._request = lambda params: (
    bounded_requests.append(dict(params))
    or {
        "meta": {"results": {"total": 29}},
        "results": [{"set_id": f"set-{index}"} for index in range(params["limit"])],
    }
)
bounded_records, bounded_total = bounded_client.fetch_all("ingredient", max_records=3)
bounded_client.close()
assert bounded_total == 29 and len(bounded_records) == 3
assert len(bounded_requests) == 1 and bounded_requests[0]["limit"] == 3
expect_value_error(
    "raw_search must be non-empty",
    lambda: drug_regulatory.search_drug_labels(brand_name="aspirin", raw_search=" "),
)
assert drug_calls == []

structure_calls = []
structures.run_search_spec = lambda client, query, max_rows=1000: structure_calls.append(("emdb", query)) or {}
structures.search_by_participant = lambda accession, **kwargs: structure_calls.append(("complex", accession)) or {}
structures.fetch_interactions = lambda query, **kwargs: structure_calls.append(("interactions", query)) or {"records": []}
structures.get_interactor = lambda query, **kwargs: structure_calls.append(("interactor", query)) or {}
structures.search_structures = lambda client, **kwargs: structure_calls.append(("pdb", kwargs)) or {}
expect_value_error("query must be non-empty", lambda: structures.emdb_search_entries(" "))
expect_value_error("accession must be non-empty", lambda: structures.complexportal_search_by_participant(" "))
expect_value_error("query must be non-empty", lambda: structures.intact_fetch_interactions(" "))
expect_value_error("query must be non-empty", lambda: structures.intact_get_interactor(" "))
expect_value_error(
    blank_error("text"),
    lambda: structures.pdb_search_structures(text=" ", taxonomy_id=9606),
)
assert structure_calls == []
structures.emdb_search_entries("NEK7")
structures.complexportal_search_by_participant("P04637")
structures.intact_fetch_interactions("P04637")
structures.intact_get_interactor("P04637")
structures.pdb_search_structures(text=None, taxonomy_id=9606)
assert [item[0] for item in structure_calls] == ["emdb", "complex", "interactions", "interactor", "pdb"]
`
	scriptPath := filepath.Join(t.TempDir(), "criteria_parity.py")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", scriptPath, lib)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("search criteria and broad defaults contract failed: %v\n%s", err, output)
	}
}

func TestBundledChEMBLAndClinicalBlankCriteriaPreserveReferenceSemanticsWithBroadDefaults(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	lib := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib")
	script := `
import asyncio
import json
import sys

sys.path.insert(0, sys.argv[1])

import mcp_chembl.server as chembl
from mcp_cellguide.resolver import CellTypeResolver
import mcp_clinical_trials.server as trials
import mcp_human_genetics.server as human_genetics

def expect_error(expected, call):
    try:
        call()
    except ValueError as exc:
        assert str(exc) == expected, (str(exc), expected)
    else:
        raise AssertionError(f"expected ValueError: {expected}")

def empty(result):
    payload = json.loads(result)
    assert payload["count"] == 0 and payload["total"] == 0, payload
    return payload

calls = []

class Drugs:
    def get_molecules(self, ids, **kwargs):
        calls.append(("molecules", ids, kwargs)); return []
    def paginate(self, path, key, params, max_records):
        calls.append(("compound-name", params, max_records)); return [], 0
    def search_drugs_by_indication(self, indication, **kwargs):
        calls.append(("indication", indication, kwargs))
        return {"drugs": [], "total_parents": 0,
                "indication_query": {"term": indication},
                "total_indication_rows": 0}

chembl._drugs = lambda: Drugs()
for args in ({}, {"name": None}, {"name": " "}):
    before = len(calls)
    empty(chembl.compound_search(args))
    assert len(calls) == before
empty(chembl.compound_search({"name": " ", "chembl_id": "CHEMBL1"}))
assert calls[-1][0] == "molecules"
empty(chembl.compound_search({"name": "aspirin"}))
assert calls[-1][0] == "compound-name"

for indication in (None, " "):
    before = len(calls)
    payload = empty(chembl.drug_search({"indication": indication}))
    assert payload["indication_query"]["term"] == (indication or "")
    assert len(calls) == before
empty(chembl.drug_search({"indication": "cancer"}))
assert calls[-1][0] == "indication"

def single_page(client, resource, params, limit, offset=0):
    calls.append((resource, params, limit)); return [], 0

chembl._single_page = single_page
chembl._bio = lambda: object()
for handler, resource, blank_key, sibling in (
    (chembl.get_bioactivity, "activity", "molecule_chembl_id", {"min_pchembl": 7}),
    (chembl.get_mechanism, "mechanism", "target_chembl_id", {"action_type": "INHIBITOR"}),
):
    before = len(calls)
    empty(handler({blank_key: " "}))
    assert len(calls) == before
    empty(handler({blank_key: None}))
    assert calls[-1] == (resource, {}, 50)
    mixed = {blank_key: " ", **sibling}
    empty(handler(mixed))
    assert calls[-1][0] == resource and calls[-1][1]

sponsor_error = "sponsor_name must be non-empty"
for args in ({}, {"sponsor_name": None}, {"sponsor_name": " "}):
    expect_error(sponsor_error, lambda args=args: trials.sponsor_params(args))
assert "LeadSponsorName" in trials.sponsor_params({"sponsor_name": "NIH"})["filter.advanced"]

eligibility_error = (
    "search_by_eligibility needs at least one of: condition, "
    "eligibility_keywords, min_age, max_age, sex"
)
expect_error(eligibility_error, lambda: trials.eligibility_params({"eligibility_keywords": " "}))
eligibility = trials.eligibility_params({"eligibility_keywords": " ", "condition": "NEK7"})
assert eligibility["query.cond"] == "NEK7" and "filter.advanced" not in eligibility
assert trials.eligibility_params({"eligibility_keywords": None, "condition": "NEK7"}) == eligibility

investigator_error = (
    "search_investigators needs at least one of: investigator_name, "
    "institution, location, condition, status"
)
expect_error(investigator_error, lambda: trials.investigator_params({"investigator_name": " "}))
investigator = trials.investigator_params({"investigator_name": " ", "institution": "NIH"})
assert "LocationFacility" in investigator["filter.advanced"]
assert "OverallOfficialName" not in investigator["filter.advanced"]
assert trials.investigator_params({"investigator_name": None, "institution": "NIH"}) == investigator

endpoint_calls = []
def fetch_page(params, page_size, page_token, count_total, fields):
    endpoint_calls.append(("condition", params)); return {"studies": []}
class TrialClient:
    def get_study(self, nct, fields):
        endpoint_calls.append(("nct", nct)); return {}, {}
trials._fetch_page = fetch_page
trials._client = lambda: TrialClient()
expect_error("analyze_endpoints needs nct_id or condition",
             lambda: trials.analyze_endpoints({"nct_id": " "}))
trials.analyze_endpoints({"nct_id": " ", "condition": "NEK7"})
assert endpoint_calls[-1][0] == "condition"
trials.analyze_endpoints({"nct_id": None, "condition": "NEK7"})
assert endpoint_calls[-1][0] == "condition"
trials.analyze_endpoints({"nct_id": "NCT00000001", "condition": "NEK7"})
assert endpoint_calls[-1] == ("nct", "NCT00000001")

class CellClient:
    def __init__(self):
        self.calls = []
    async def get_cell_info(self, cell_id):
        self.calls.append(("info", cell_id)); return {"id": cell_id}
    async def search_cell_types(self, query, limit):
        self.calls.append(("search", query, limit)); return [{"id": "CL:0000084"}]

cell_client = CellClient()
resolver = CellTypeResolver(cell_client)
assert asyncio.run(resolver.resolve(" ")) is None
assert cell_client.calls == []
assert asyncio.run(resolver.resolve("T cell")) == "CL:0000084"
assert cell_client.calls == [("search", "T cell", 1)]

class GWAS:
    def __init__(self):
        self.calls = []
    def search_traits(self, query, max_records):
        self.calls.append((query, max_records)); return {"efo_traits": []}

gwas = GWAS()
human_genetics._gwas = lambda: gwas
expect_error("query must be non-empty", lambda: human_genetics.gwas_search_traits(" "))
assert gwas.calls == []
assert human_genetics.gwas_search_traits("NEK7")["query"] == "NEK7"
assert gwas.calls == [("NEK7", 500)]
`
	command := exec.Command("python3", "-c", script, lib)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ChEMBL/Clinical criteria and broad defaults contract failed: %v\n%s", err, output)
	}
}

func TestBundledBioFleetBlankCriteriaMatchWorkspaceV0120SliceA(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	lib := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib")
	script := `
import asyncio
import sys

sys.path.insert(0, sys.argv[1])

import mcp_cancer_models.server as cancer
import mcp_cellguide.server as cellguide
import mcp_clinical_genomics.server as clinical
import mcp_genes_ontologies.server as genes

def blank_error(field):
    return (
        f"{field} must be non-empty when provided — "
        "an empty search criterion is not a search (omit the parameter "
        "for the tool's documented default, or provide a value)"
    )

def expect_value_error(expected, call):
    before = len(calls)
    try:
        call()
    except ValueError as exc:
        assert str(exc) == expected, (str(exc), expected)
    else:
        raise AssertionError(f"expected ValueError: {expected}")
    assert len(calls) == before, (expected, calls[before:])

calls = []
class Backend:
    def __getattr__(self, name):
        def invoke(*args, **kwargs):
            calls.append((name, args, kwargs))
            if name == "get_json":
                return {"response": {"docs": [], "numFound": 0}}
            return {}
        return invoke

backend = Backend()
cancer._depmap = lambda: backend
cancer._cbioportal = lambda: backend
expect_value_error(blank_error("tissue"), lambda: cancer.list_models(tissue=" "))
expect_value_error(blank_error("cancer_type, tissue"), lambda: cancer.list_models(tissue=" ", cancer_type=" "))
cancer.list_models(tissue=None, cancer_type=None)
expect_value_error("query must be non-empty", lambda: cancer.search_models(" "))
expect_value_error("query must be non-empty", lambda: cancer.search_genes(" "))
cancer.search_models("A549")
cancer.search_genes("KRAS")
expect_value_error(blank_error("keyword"), lambda: cancer.cbioportal_list_studies(keyword=" ", cancer_type_id="brca"))
cancer.cbioportal_list_studies(keyword=None, cancer_type_id="brca")

class CellClient:
    async def search_cell_types(self, query, limit=10):
        calls.append(("search_cell_types", (query, limit), {}))
        return []

cellguide._client = CellClient()
expect_value_error("query must be non-empty", lambda: asyncio.run(cellguide.search_cell_types(" ")))
asyncio.run(cellguide.search_cell_types("T cell"))

clinical._clingen = lambda: backend
clinical._civic = lambda: backend
for name, call in (
    ("gene", lambda: clinical.clingen_gene_validity(" ")),
    ("gene", lambda: clinical.clingen_dosage_sensitivity(" ")),
    ("gene", lambda: clinical.clingen_actionability(" ")),
):
    expect_value_error(blank_error(name), call)
clinical.clingen_gene_validity(None)
clinical.clingen_dosage_sensitivity(None)
clinical.clingen_actionability(None)
for call in (
    lambda: clinical.civic_search_variants(" "),
    lambda: clinical.civic_search_molecular_profiles(" "),
    lambda: clinical.civic_search_diseases(" "),
    lambda: clinical.civic_search_therapies(" "),
):
    expect_value_error("name must be non-empty", call)
expect_value_error(
    blank_error("disease_name"),
    lambda: clinical.civic_search_evidence(disease_name=" ", therapy_name="vemurafenib"),
)
expect_value_error(
    blank_error("disease_name"),
    lambda: clinical.civic_search_assertions(disease_name=" ", therapy_name="vemurafenib"),
)
clinical.civic_search_variants("V600E")
clinical.civic_search_molecular_profiles("BRAF V600E")
clinical.civic_search_diseases("melanoma")
clinical.civic_search_therapies("vemurafenib")
clinical.civic_search_evidence(disease_name=None, therapy_name="vemurafenib")
clinical.civic_search_assertions(disease_name=None, therapy_name="vemurafenib")

genes._ols = lambda: backend
genes._quickgo = lambda: backend
genes.fetch_annotations = lambda client, query: {"records": [], "total_items": 0, "complete": True}
genes.kegg_find = lambda client, database, query: calls.append(("kegg_find", (database, query), {})) or []
expect_value_error("query must be non-empty", lambda: genes.search_ontology_terms(" "))
expect_value_error("uniprot_accession must be non-empty", lambda: genes.get_go_annotations(" "))
expect_value_error("query must be non-empty", lambda: genes.search_kegg(" "))
genes.search_ontology_terms("kinase")
genes.get_go_annotations("P04637")
genes.search_kegg("TP53")
`
	command := exec.Command("python3", "-c", script, lib)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reference 0.1.20 fleet Slice A parity failed: %v\n%s", err, output)
	}
}

func TestBundledBioFleetBlankCriteriaMatchWorkspaceV0120SliceB(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	lib := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib")
	script := `
import sys

sys.path.insert(0, sys.argv[1])

import mcp_omics_archives.server as omics
import mcp_protein_annotation.server as protein
import mcp_regulation.server as regulation

def blank_error(field):
    return (
        f"{field} must be non-empty when provided — "
        "an empty search criterion is not a search (omit the parameter "
        "for the tool's documented default, or provide a value)"
    )

def expect_value_error(expected, call):
    before = len(calls)
    try:
        call()
    except ValueError as exc:
        assert str(exc) == expected, (str(exc), expected)
    else:
        raise AssertionError(f"expected ValueError: {expected}")
    assert len(calls) == before, (expected, calls[before:])

calls = []
omics._ae_client = lambda: object()
omics._geo_client = lambda: object()
omics._mgnify_client = lambda: object()
omics._pride_client = lambda: object()
omics.search_experiments = lambda spec, **kwargs: calls.append(("arrayexpress", spec)) or {}
omics.search_series = lambda term, **kwargs: calls.append(("geo", term)) or {}
omics.search_studies = lambda spec, **kwargs: calls.append(("mgnify", spec)) or {}
omics.search_projects = lambda client, spec, **kwargs: calls.append(("pride", spec)) or {"records": []}
omics.search_project_proteins = lambda project, **kwargs: calls.append(("pride-proteins", project, kwargs)) or {}
expect_value_error(
    blank_error("query"),
    lambda: omics.arrayexpress_search_experiments(query=" ", organism="Homo sapiens"),
)
expect_value_error(blank_error("term"), lambda: omics.geo_search_series(" "))
expect_value_error(
    blank_error("keyword"),
    lambda: omics.pride_search_projects(keyword=" ", organism="Homo sapiens"),
)
expect_value_error(
    blank_error("keyword"),
    lambda: omics.pride_search_project_proteins("PXD000001", keyword=" "),
)
assert omics.mgnify_search_studies(query=" ", biome_lineage="root:Host-associated") == {}
assert calls[-1] == ("mgnify", {"type": "biome", "lineage": "root:Host-associated"})
expect_value_error(
    "provide exactly one of 'query' or 'biome_lineage'",
    lambda: omics.mgnify_search_studies(query=" "),
)
omics.arrayexpress_search_experiments(query=None, organism="Homo sapiens")
date_cases = [
    ({}, None, None),
    ({"released_after": None, "released_before": None}, None, None),
    ({"released_after": " ", "released_before": ""}, " ", ""),
    ({"released_after": "2024-01-01", "released_before": "2024-12-31"}, "2024-01-01", "2024-12-31"),
]
for kwargs, expected_after, expected_before in date_cases:
    before = len(calls)
    assert omics.arrayexpress_search_experiments(query="NEK7", **kwargs) == {}
    assert len(calls) == before + 1
    spec = calls[-1][1]
    assert spec.released_after == expected_after
    assert spec.released_before == expected_before
omics.geo_search_series("NEK7")
omics.pride_search_projects(keyword=None, organism="Homo sapiens")
omics.pride_search_project_proteins("PXD000001", keyword=None)

protein._entry_client = object()
protein._atlas = type("Atlas", (), {"search": lambda self, query, columns=None: calls.append(("hpa", query)) or []})()
protein.interpro_entry_search.search_entries = lambda **kwargs: calls.append(("interpro", kwargs)) or {}
protein.interpro_entry_search.search_clans = lambda **kwargs: calls.append(("pfam", kwargs)) or {}
expect_value_error(
    blank_error("query"),
    lambda: protein.search_interpro_entries(query=" ", go_term="GO:0004672"),
)
expect_value_error(
    "provide 'query' or 'go_term'",
    lambda: protein.search_interpro_entries(),
)
protein.search_interpro_entries(go_term="GO:0004672")
protein.search_pfam_clans(" ")
assert calls[-1][1]["q"] is None
expect_value_error("query must be non-empty", lambda: protein.search_protein_atlas(" "))
protein.search_protein_atlas("NEK7")

class Encode:
    def search_experiments(self, **kwargs):
        calls.append(("encode-experiments", kwargs)); return {"rows": [], "total": 0, "accessions": []}
    def search_biosamples(self, **kwargs):
        calls.append(("encode-biosamples", kwargs)); return {"rows": [], "total": 0, "accessions": []}
    def list_files(self, **kwargs):
        calls.append(("encode-files", kwargs)); return {"rows": [], "total": 0, "accessions": []}

regulation._encode = Encode()
regulation.jaspar.list_matrices = lambda *args, **kwargs: calls.append(("jaspar", kwargs)) or {"count": 0, "results": []}
regulation.unibind.search_datasets = lambda *args, **kwargs: calls.append(("unibind", kwargs)) or {}
expect_value_error(
    blank_error("target"),
    lambda: regulation.encode_search_experiments(target=" ", organism="Homo sapiens"),
)
expect_value_error(
    blank_error("term_name"),
    lambda: regulation.encode_search_biosamples(term_name=" ", organism="Homo sapiens"),
)
expect_value_error(
    blank_error("file_format"),
    lambda: regulation.encode_list_files(file_format=" ", assay_term_name="ChIP-seq"),
)
expect_value_error(
    blank_error("name"),
    lambda: regulation.jaspar_list_matrices(name=" ", tax_id=9606),
)
expect_value_error(
    blank_error("search"),
    lambda: regulation.unibind_search_tfbs(tf_name="CTCF", search=" "),
)
regulation.encode_search_experiments(target=None, organism="Homo sapiens")
regulation.encode_search_biosamples(term_name=None, organism="Homo sapiens")
regulation.encode_list_files(file_format=None, assay_term_name="ChIP-seq")
regulation.jaspar_list_matrices(name=None, tax_id=9606)
regulation.unibind_search_tfbs(tf_name="CTCF", search=None)
`
	command := exec.Command("python3", "-c", script, lib)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reference 0.1.20 fleet Slice B parity failed: %v\n%s", err, output)
	}
}

func TestBundledBioFleetBlankCriteriaMatchWorkspaceV0120SliceC(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	lib := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib")
	script := `
import sys

sys.path.insert(0, sys.argv[1])

import mcp_biomart.server as biomart
import mcp_expression.server as expression
import mcp_research_resources.server as resources
import mcp_variants.server as variants

def blank_error(field):
    return (
        f"{field} must be non-empty when provided — "
        "an empty search criterion is not a search (omit the parameter "
        "for the tool's documented default, or provide a value)"
    )

def expect_value_error(expected, call):
    before = len(calls)
    try:
        call()
    except ValueError as exc:
        assert str(exc) == expected, (str(exc), expected)
    else:
        raise AssertionError(f"expected ValueError: {expected}")
    assert len(calls) == before, (expected, calls[before:])

calls = []
biomart._configuration = lambda dataset: calls.append(("biomart-config", dataset)) or ({}, {})
assert biomart.get_data({
    "dataset": "hsapiens_gene_ensembl",
    "attributes": ["ensembl_gene_id"],
    "filters": {"gene": " "},
}) == (
    "Error: filter(s) gene have empty values — an empty filter matches "
    "nothing; omit the filter instead"
)
assert biomart.get_data({
    "dataset": "hsapiens_gene_ensembl",
    "attributes": ["ensembl_gene_id"],
    "filters": {"gene": []},
}).startswith("Error: filter(s) gene have empty values")
assert calls == []

class Gtex:
    def sample_info(self, **kwargs):
        calls.append(("gtex", kwargs)); return {"returned": 0, "total": 0}
expression._gtex = lambda dataset: Gtex()
expect_value_error(
    blank_error("tissue_site_detail_id"),
    lambda: expression.gtex_sample_info(tissue_site_detail_id=" ", data_type="RNASEQ"),
)
expression.gtex_sample_info(tissue_site_detail_id=None, data_type="RNASEQ")

class GrantsResult:
    hit_count = 0
    records = []
    facets = {}
class Grants:
    def search(self, spec):
        calls.append(("grants", spec)); return GrantsResult()
resources._grants = lambda: Grants()
expect_value_error(
    blank_error("keyword"),
    lambda: resources.search_grants(keyword=" ", opportunity_number="R01"),
)
resources.search_grants(keyword=None, opportunity_number="R01")

class Clinvar:
    def search(self, query, retmax=50):
        calls.append(("clinvar", query)); return {}
variants._clinvar = lambda: Clinvar()
expect_value_error(blank_error("query"), lambda: variants.clinvar_search(" "))
variants.clinvar_search("NEK7")
`
	command := exec.Command("python3", "-c", script, lib)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reference 0.1.20 fleet Slice C parity failed: %v\n%s", err, output)
	}
}

func TestPackagedBioServersPreserveWorkspaceV0120CriteriaClassification(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	runServer := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "run_server.py")
	observer := t.TempDir()
	observerSource := `import ipaddress, os, socket, urllib.request
urllib.request.getproxies = lambda: {}
urllib.request.getproxies_environment = lambda: {}
_original_connect = socket.socket.connect
def _is_loopback(address):
    host = address[0] if isinstance(address, tuple) else address
    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False
def _blocked(self, address):
    if _is_loopback(address):
        return _original_connect(self, address)
    with open(os.environ["SYNON_SOCKET_MARKER"], "w", encoding="utf-8") as marker:
        marker.write(str(address))
    raise RuntimeError("network access blocked by parity observer")
socket.socket.connect = _blocked
`
	if err := os.WriteFile(filepath.Join(observer, "sitecustomize.py"), []byte(observerSource), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, pkg, tool string
		args            map[string]any
		wantConnect     bool
		wantError       bool
	}{
		{"biorxiv-default-browse", "mcp_biorxiv", "search_preprints", map[string]any{}, true, false},
		{"biorxiv-null-default-browse", "mcp_biorxiv", "search_preprints", map[string]any{"date_from": nil}, true, false},
		{"biorxiv-single-date-default-browse", "mcp_biorxiv", "search_preprints", map[string]any{"date_from": "2024-01-01"}, true, false},
		{"biorxiv-unbounded-cursor", "mcp_biorxiv", "search_preprints", map[string]any{"date_from": "2024-01-01", "date_to": "2024-01-02", "cursor": 1_000_001}, true, false},
		{"biorxiv-limit-schema-reject", "mcp_biorxiv", "search_preprints", map[string]any{"recent_days": 1, "limit": 101}, false, true},
		{"biorxiv-unknown-reject", "mcp_biorxiv", "search_preprints", map[string]any{"unknown": "value"}, false, true},
		{"chembl-default-browse", "mcp_chembl", "target_search", map[string]any{}, true, false},
		{"chembl-null-default-browse", "mcp_chembl", "target_search", map[string]any{"gene_symbol": nil}, true, false},
		{"chembl-blank-zero", "mcp_chembl", "target_search", map[string]any{"gene_symbol": " "}, false, false},
		{"chembl-limit-schema-reject", "mcp_chembl", "target_search", map[string]any{"organism": "Homo sapiens", "limit": 1001}, false, true},
		{"clinical-empty-reject", "mcp_clinical_trials", "search_trials", map[string]any{}, false, true},
		{"clinical-blank-reject", "mcp_clinical_trials", "search_trials", map[string]any{"condition": " "}, false, true},
		{"clinical-blank-with-sibling", "mcp_clinical_trials", "search_trials", map[string]any{"condition": " ", "intervention": "NEK7"}, true, false},
		{"clinical-page-size-schema-reject", "mcp_clinical_trials", "search_trials", map[string]any{"condition": "NEK7", "page_size": 1001}, false, true},
		{"clinical-max-rows-alias", "mcp_clinical_trials", "search_trials", map[string]any{"condition": "NEK7", "max_rows": 20}, true, false},
		{"clinical-matching-limits-alias", "mcp_clinical_trials", "search_trials", map[string]any{"condition": "NEK7", "page_size": 20, "max_rows": 20}, true, false},
		{"clinical-conflicting-limits-reject", "mcp_clinical_trials", "search_trials", map[string]any{"condition": "NEK7", "page_size": 20, "max_rows": 10}, false, true},
		{"openalex-empty-reject", "mcp_literature", "openalex_search_works", map[string]any{}, false, true},
		{"openalex-blank-reject", "mcp_literature", "openalex_search_works", map[string]any{"query": " "}, false, true},
		{"openalex-single-year-clamps", "mcp_literature", "openalex_search_works", map[string]any{"year_from": 2024, "max_records": 501}, true, false},
		{"openalex-reverse-year-browse", "mcp_literature", "openalex_search_works", map[string]any{"year_from": 2025, "year_to": 2024}, true, false},
		{"fda-empty-reject", "mcp_drug_regulatory", "search_drug_applications", map[string]any{}, false, true},
		{"fda-blank-raw-reject", "mcp_drug_regulatory", "search_drug_applications", map[string]any{"raw_search": " "}, false, true},
		{"fda-single-date-no-invented-cap", "mcp_drug_regulatory", "search_drug_applications", map[string]any{"submission_date_from": "20250101", "max_records": 1001}, true, false},
		{"pdb-empty-reject", "mcp_structures_interactions", "pdb_search_structures", map[string]any{}, false, true},
		{"pdb-blank-text-reject", "mcp_structures_interactions", "pdb_search_structures", map[string]any{"text": " ", "taxonomy_id": 9606}, false, true},
		{"pdb-max-rows-reject", "mcp_structures_interactions", "pdb_search_structures", map[string]any{"text": "NEK7", "max_rows": 1001}, false, true},
		{"pdb-zero-taxonomy-no-invented-min", "mcp_structures_interactions", "pdb_search_structures", map[string]any{"taxonomy_id": 0, "max_rows": 1}, true, false},
		{"pdb-zero-resolution-treated-absent", "mcp_structures_interactions", "pdb_search_structures", map[string]any{"max_resolution": 0, "max_rows": 1}, false, true},
		{"cancer-models-blank-tissue", "mcp_cancer_models", "list_models", map[string]any{"tissue": " "}, false, true},
		{"cancer-models-blank-model-query", "mcp_cancer_models", "search_models", map[string]any{"query": " "}, false, true},
		{"cancer-models-blank-gene-query", "mcp_cancer_models", "search_genes", map[string]any{"query": " "}, false, true},
		{"cancer-models-blank-study-keyword", "mcp_cancer_models", "cbioportal_list_studies", map[string]any{"keyword": " ", "cancer_type_id": "brca"}, false, true},
		{"cellguide-blank-query", "mcp_cellguide", "search_cell_types", map[string]any{"query": " "}, false, true},
		{"clingen-blank-validity-gene", "mcp_clinical_genomics", "clingen_gene_validity", map[string]any{"gene": " "}, false, true},
		{"clingen-blank-dosage-gene", "mcp_clinical_genomics", "clingen_dosage_sensitivity", map[string]any{"gene": " "}, false, true},
		{"clingen-blank-actionability-gene", "mcp_clinical_genomics", "clingen_actionability", map[string]any{"gene": " "}, false, true},
		{"civic-blank-variant-name", "mcp_clinical_genomics", "civic_search_variants", map[string]any{"name": " "}, false, true},
		{"civic-blank-evidence-disease", "mcp_clinical_genomics", "civic_search_evidence", map[string]any{"disease_name": " ", "therapy_name": "vemurafenib"}, false, true},
		{"civic-blank-assertion-disease", "mcp_clinical_genomics", "civic_search_assertions", map[string]any{"disease_name": " ", "therapy_name": "vemurafenib"}, false, true},
		{"civic-blank-profile-name", "mcp_clinical_genomics", "civic_search_molecular_profiles", map[string]any{"name": " "}, false, true},
		{"civic-blank-disease-name", "mcp_clinical_genomics", "civic_search_diseases", map[string]any{"name": " "}, false, true},
		{"civic-blank-therapy-name", "mcp_clinical_genomics", "civic_search_therapies", map[string]any{"name": " "}, false, true},
		{"ontology-blank-query", "mcp_genes_ontologies", "search_ontology_terms", map[string]any{"query": " "}, false, true},
		{"quickgo-blank-accession", "mcp_genes_ontologies", "get_go_annotations", map[string]any{"uniprot_accession": " "}, false, true},
		{"kegg-blank-query", "mcp_genes_ontologies", "search_kegg", map[string]any{"query": " "}, false, true},
		{"arrayexpress-blank-query", "mcp_omics_archives", "arrayexpress_search_experiments", map[string]any{"query": " ", "organism": "Homo sapiens"}, false, true},
		{"geo-blank-term", "mcp_omics_archives", "geo_search_series", map[string]any{"term": " "}, false, true},
		{"mgnify-blank-query-with-biome", "mcp_omics_archives", "mgnify_search_studies", map[string]any{"query": " ", "biome_lineage": "root:Host-associated"}, true, false},
		{"mgnify-blank-query-only", "mcp_omics_archives", "mgnify_search_studies", map[string]any{"query": " "}, false, true},
		{"pride-blank-keyword", "mcp_omics_archives", "pride_search_projects", map[string]any{"keyword": " ", "organism": "Homo sapiens"}, false, true},
		{"pride-proteins-blank-keyword", "mcp_omics_archives", "pride_search_project_proteins", map[string]any{"project_accession": "PXD000001", "keyword": " "}, false, true},
		{"interpro-blank-query", "mcp_protein_annotation", "search_interpro_entries", map[string]any{"query": " ", "go_term": "GO:0004672"}, false, true},
		{"interpro-empty-reject", "mcp_protein_annotation", "search_interpro_entries", map[string]any{}, false, true},
		{"pfam-blank-query-browse", "mcp_protein_annotation", "search_pfam_clans", map[string]any{"query": " "}, true, false},
		{"protein-atlas-blank-query", "mcp_protein_annotation", "search_protein_atlas", map[string]any{"query": " "}, false, true},
		{"encode-experiments-blank-target", "mcp_regulation", "encode_search_experiments", map[string]any{"target": " ", "organism": "Homo sapiens"}, false, true},
		{"encode-biosamples-blank-term", "mcp_regulation", "encode_search_biosamples", map[string]any{"term_name": " ", "organism": "Homo sapiens"}, false, true},
		{"encode-files-blank-format", "mcp_regulation", "encode_list_files", map[string]any{"file_format": " ", "assay_term_name": "ChIP-seq"}, false, true},
		{"jaspar-blank-name", "mcp_regulation", "jaspar_list_matrices", map[string]any{"name": " ", "tax_id": 9606}, false, true},
		{"unibind-blank-search", "mcp_regulation", "unibind_search_tfbs", map[string]any{"tf_name": "CTCF", "search": " "}, false, true},
		{"biomart-blank-filter", "mcp_biomart", "get_data", map[string]any{"mart": "ENSEMBL_MART_ENSEMBL", "dataset": "hsapiens_gene_ensembl", "attributes": []string{"ensembl_gene_id"}, "filters": map[string]any{"gene": " "}}, false, false},
		{"gtex-blank-tissue", "mcp_expression", "gtex_sample_info", map[string]any{"tissue_site_detail_id": " ", "data_type": "RNASEQ"}, false, true},
		{"grants-blank-keyword", "mcp_research_resources", "search_grants", map[string]any{"keyword": " ", "opportunity_number": "R01"}, false, true},
		{"clinvar-blank-query", "mcp_variants", "clinvar_search", map[string]any{"query": " "}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(observer, tc.name+".connected")
			config := ServerConfig{
				Type: "stdio", Scope: "bundled", Command: "python3",
				Args: []string{runServer, tc.pkg},
				Env: map[string]string{
					"PYTHONDONTWRITEBYTECODE": "1", "PYTHONPATH": observer, "SYNON_SOCKET_MARKER": marker,
				},
			}
			_, err := CallToolForServer(context.Background(), repositoryRoot, tc.name, tc.tool, tc.args, config)
			_, statErr := os.Stat(marker)
			connected := statErr == nil
			if statErr != nil && !os.IsNotExist(statErr) {
				t.Fatal(statErr)
			}
			if connected != tc.wantConnect {
				t.Fatalf("network connect = %t, want %t (err=%v)", connected, tc.wantConnect, err)
			}
			if tc.wantError && err == nil {
				t.Fatal("empty criteria were accepted")
			}
			if !tc.wantError && !tc.wantConnect && err != nil {
				t.Fatalf("zero-result criteria returned error: %v", err)
			}
		})
	}
}

func TestLivePublicConnectors(t *testing.T) {
	if os.Getenv("SYNON_LIVE_CONNECTOR_TEST") != "1" {
		t.Skip("set SYNON_LIVE_CONNECTOR_TEST=1 for public connector evidence")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	runServer := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "run_server.py")
	for _, tc := range []struct {
		name, pkg, tool string
		args            map[string]any
	}{
		{"biorxiv", "mcp_biorxiv", "search_preprints", map[string]any{"recent_days": 1, "limit": 1}},
		{"clinical_trials", "mcp_clinical_trials", "search_trials", map[string]any{"condition": "NEK7", "page_size": 1}},
		{"chembl", "mcp_chembl", "target_search", map[string]any{"gene_symbol": "NEK7", "limit": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := ServerConfig{
				Type: "stdio", Scope: "bundled", Command: "python3", Args: []string{runServer, tc.pkg},
				Env: map[string]string{"PYTHONDONTWRITEBYTECODE": "1"},
			}
			output, err := CallToolForServer(context.Background(), repositoryRoot, tc.name, tc.tool, tc.args, config)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(output) == "" {
				t.Fatal("public connector returned an empty result")
			}
		})
	}
}

func TestRemoteHTTPMCPListToolsAndCallSSE(t *testing.T) {
	t.Setenv("SYNON_MCP_CONFIG", "")
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	called := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	root := t.TempDir()
	writeMCPConfig(t, root, map[string]any{
		"mcpServers": map[string]any{
			"remote": map[string]any{
				"type":    "http",
				"url":     server.URL,
				"headers": map[string]any{"X-Synon-Test": "remote-mcp"},
			},
		},
	})

	if _, err := ListTools(context.Background(), root, "remote"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ListTools error = %v, want untrusted project config rejection", err)
	}
	if called {
		t.Fatal("project MCP config contacted a remote endpoint during discovery")
	}
}

func TestRemoteHTTPErrorRedactsConfiguredCredentialEcho(t *testing.T) {
	const sentinel = "remote-provider-must-not-echo-this-key"
	for _, mode := range []string{"json-rpc", "plain"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				echoed := r.Header.Get("X-Api-Key")
				if mode == "json-rpc" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"jsonrpc": "2.0", "id": 1,
						"error": map[string]any{"code": -32001, "message": "rejected " + echoed},
					})
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("rejected " + echoed))
			}))
			defer server.Close()

			serverURL, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, port, err := net.SplitHostPort(serverURL.Host)
			if err != nil {
				t.Fatal(err)
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // controlled TLS fixture
			baseDial := transport.DialContext
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				host, dialPort, splitErr := net.SplitHostPort(address)
				if splitErr != nil {
					return nil, splitErr
				}
				if host != "8.8.8.8" || dialPort != port {
					return nil, fmt.Errorf("unexpected fixture dial target %q", address)
				}
				return baseDial(ctx, network, serverURL.Host)
			}
			ctx := withRemoteSecurityFixtureClient(context.Background(), &http.Client{Transport: transport})
			_, err = remoteHTTPRPCWithOptions(ctx, t.TempDir(), ServerConfig{
				Name: "credential-echo", URL: "https://8.8.8.8:" + port,
				Headers: map[string]string{"X-Api-Key": sentinel},
			}, nil, 1, "tools/list", nil, remoteHTTPRPCOptions{
				ProtocolVersion: latestProtocolVersion, Modern: true,
			})
			if err == nil {
				t.Fatal("credential-echoing remote error unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), sentinel) || !strings.Contains(err.Error(), "[redacted]") {
				t.Fatalf("remote error was not safely redacted: %v", err)
			}
		})
	}
}

func TestRemoteHTTPQueryCredentialIsSentAndRedacted(t *testing.T) {
	const sentinel = "query-provider-must-not-echo-this-key"
	seenQuery := make(chan url.Values, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery <- r.URL.Query()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("rejected " + r.URL.Query().Get("apikey")))
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // controlled TLS fixture
	baseDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, dialPort, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		if host != "8.8.8.8" || dialPort != port {
			return nil, fmt.Errorf("unexpected fixture dial target %q", address)
		}
		return baseDial(ctx, network, serverURL.Host)
	}
	ctx := withRemoteSecurityFixtureClient(context.Background(), &http.Client{Transport: transport})
	config := ServerConfig{
		Name: "query-credential", URL: "https://8.8.8.8:" + port + "?mode=fixture",
		QueryParams: map[string]string{"apikey": sentinel},
	}
	serialized, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), sentinel) {
		t.Fatalf("runtime query credential was serialized: %s", serialized)
	}
	_, err = remoteHTTPRPCWithOptions(ctx, t.TempDir(), config, nil, 1, "tools/list", nil, remoteHTTPRPCOptions{
		ProtocolVersion: latestProtocolVersion, Modern: true,
	})
	if err == nil || strings.Contains(err.Error(), sentinel) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("query credential was not safely handled: %v", err)
	}
	select {
	case query := <-seenQuery:
		if query.Get("mode") != "fixture" || query.Get("apikey") != sentinel {
			t.Fatalf("request query=%v", query)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fixture did not receive the remote MCP request")
	}
}

func TestInspectAndCallToolForServerUsesOneInitializedSession(t *testing.T) {
	var methods []string
	initializeCount := 0
	callCount := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		methods = append(methods, req.Method)
		if req.Method != "server/discover" && req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "inspect-session" {
			t.Fatalf("method %s did not reuse initialized session", req.Method)
		}
		switch req.Method {
		case "server/discover":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{
				"code": -32601, "message": "method not found",
			}})
		case "initialize":
			initializeCount++
			w.Header().Set("Mcp-Session-Id", "inspect-session")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
			}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{
				"name": "echo", "inputSchema": map[string]any{"type": "object", "required": []any{"text"}},
				"annotations": map[string]any{"readOnlyHint": true},
			}}}})
		case "tools/call":
			callCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "same session"}},
			}})
		default:
			t.Fatalf("unexpected method %q", req.Method)
		}
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // controlled TLS fixture
	baseDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, dialPort, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		if host != "8.8.8.8" || dialPort != port {
			return nil, fmt.Errorf("unexpected fixture dial target %q", address)
		}
		return baseDial(ctx, network, serverURL.Host)
	}
	ctx := withRemoteSecurityFixtureClient(context.Background(), &http.Client{Transport: transport})
	inspected := 0
	output, err := InspectAndCallToolForServer(ctx, t.TempDir(), "remote", "echo", map[string]any{"text": "hello"}, ServerConfig{
		Type: "http", URL: "https://8.8.8.8:" + port, Name: "remote",
	}, func(tool ToolProjection) error {
		inspected++
		if tool.ToolName != "echo" || tool.InputSchema["type"] != "object" || !tool.ReadOnlyHint {
			return errors.New("unexpected inspected tool")
		}
		return nil
	})
	if err != nil || output != "same session" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if initializeCount != 1 || inspected != 1 || callCount != 1 || strings.Join(methods, ",") != "server/discover,initialize,notifications/initialized,tools/list,tools/call" {
		t.Fatalf("initialize=%d inspected=%d calls=%d methods=%v", initializeCount, inspected, callCount, methods)
	}
}

func TestInspectAndCallToolForServerUsesModernStatelessProtocol(t *testing.T) {
	var methods []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		methods = append(methods, req.Method)
		if got := r.Header.Get("Mcp-Protocol-Version"); got != latestProtocolVersion {
			t.Fatalf("%s protocol header = %q", req.Method, got)
		}
		if got := r.Header.Get("Mcp-Method"); got != req.Method {
			t.Fatalf("%s method header = %q", req.Method, got)
		}
		if got := r.Header.Get("Mcp-Session-Id"); got != "" {
			t.Fatalf("modern request %s carried session %q", req.Method, got)
		}
		params, _ := req.Params.(map[string]any)
		meta, _ := params["_meta"].(map[string]any)
		if meta[metaProtocolVersion] != latestProtocolVersion || meta[metaClientCapabilities] == nil || meta[metaClientInfo] == nil {
			t.Fatalf("%s modern metadata = %#v", req.Method, meta)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"supportedVersions": []string{latestProtocolVersion},
				"capabilities":      map[string]any{"tools": map[string]any{}},
				"resultType":        "complete",
			}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"tools": []any{map[string]any{
					"name": "echo", "inputSchema": map[string]any{"type": "object", "required": []any{"text"}},
					"annotations": map[string]any{"readOnlyHint": true},
				}}, "resultType": "complete",
			}})
		case "tools/call":
			if got := r.Header.Get("Mcp-Name"); got != "echo" {
				t.Fatalf("tools/call name header = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "modern ok"}}, "resultType": "complete",
			}})
		default:
			t.Fatalf("unexpected modern method %q", req.Method)
		}
	}))
	defer server.Close()
	endpoint, client := oauthTLSTestClient(t, server)
	ctx := withRemoteSecurityFixtureClient(context.Background(), client)
	output, err := InspectAndCallToolForServer(ctx, t.TempDir(), "modern", "echo", map[string]any{"text": "hello"}, ServerConfig{
		Type: "http", URL: endpoint, Name: "modern",
	}, func(tool ToolProjection) error {
		if tool.ToolName != "echo" || !tool.ReadOnlyHint {
			return errors.New("unexpected inspected tool")
		}
		return nil
	})
	if err != nil || output != "modern ok" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if got := strings.Join(methods, ","); got != "server/discover,tools/list,tools/call" {
		t.Fatalf("modern methods = %s", got)
	}
}

func TestDecodeToolInputSchemaFailsClosedForMissingAndNonObjectSchemas(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"missing": nil,
		"null":    json.RawMessage(`null`),
		"false":   json.RawMessage(`false`),
		"true":    json.RawMessage(`true`),
		"array":   json.RawMessage(`[]`),
		"string":  json.RawMessage(`"unexpected"`),
	} {
		t.Run(name, func(t *testing.T) {
			if schema, err := decodeToolInputSchema(raw); err == nil || schema != nil {
				t.Fatalf("schema=%#v err=%v, want invalid schema rejection", schema, err)
			}
		})
	}
	if schema, err := decodeToolInputSchema(json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","minimum":9007199254740993}}}`)); err != nil || schema["type"] != "object" {
		t.Fatalf("object schema=%#v err=%v", schema, err)
	}
}

func TestDecodeToolOutputSchemaPreservesOptionalObjectContract(t *testing.T) {
	if schema, err := decodeToolOutputSchema(nil); err != nil || schema != nil {
		t.Fatalf("missing output schema=%#v err=%v", schema, err)
	}
	schema, err := decodeToolOutputSchema(json.RawMessage(`{"type":"object","properties":{"compounds":{"type":"array"}}}`))
	properties, _ := schema["properties"].(map[string]any)
	if err != nil || schema["type"] != "object" || properties["compounds"] == nil {
		t.Fatalf("output schema=%#v err=%v", schema, err)
	}
	if schema, err := decodeToolOutputSchema(json.RawMessage(`[]`)); err == nil || schema != nil {
		t.Fatalf("non-object output schema=%#v err=%v", schema, err)
	}
}

func TestLoadServersDoesNotTrustProjectConfigUnlessExplicitlyConfigured(t *testing.T) {
	t.Setenv("SYNON_MCP_CONFIG", "")
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	root := t.TempDir()
	writeMCPConfig(t, root, map[string]any{"mcpServers": map[string]any{
		"untrusted": map[string]any{"type": "stdio", "command": "sh", "args": []string{"-c", "exit 99"}},
	}})

	configs, err := LoadServers(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("default project config was trusted: %#v", configs)
	}

	t.Setenv("SYNON_MCP_CONFIG", filepath.Join(root, ".mcp.json"))
	configs, err = LoadServers(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := configs["untrusted"]; !ok {
		t.Fatalf("explicit MCP config was not loaded: %#v", configs)
	}
}

func TestListToolsForServerRejectsInsecureRemoteBeforeRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer server.Close()

	_, err := ListToolsForServer(context.Background(), t.TempDir(), "insecure", ServerConfig{
		Type: "http", URL: server.URL, Name: "insecure",
	})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("error=%v, want HTTPS policy rejection", err)
	}
	if called {
		t.Fatal("insecure remote endpoint was contacted")
	}
}

func TestOAuthTokenIsBoundToConnectorIdentityAndCanonicalOrigin(t *testing.T) {
	root := t.TempDir()
	token := oauthTokenCache{AccessToken: "test-token", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	if err := saveOAuthTokenForRemoteOrigin(root, "connector-a", "https://api.example.test/v1/mcp", token); err != nil {
		t.Fatal(err)
	}
	for _, config := range []ServerConfig{
		{ConnectorID: "connector-a", Name: "shared-name", URL: "https://other.example.test/v1/mcp"},
		{ConnectorID: "connector-b", Name: "shared-name", URL: "https://api.example.test/another-path"},
	} {
		if got, ok := loadOAuthAccessTokenForRemote(root, config); ok || got != "" {
			t.Fatalf("token was available across connector/origin boundary: config=%#v token=%q ok=%v", config, got, ok)
		}
	}
	if got, ok := loadOAuthAccessTokenForRemote(root, ServerConfig{ConnectorID: "connector-a", Name: "shared-name", URL: "https://api.example.test/another-path"}); !ok || got != "test-token" {
		t.Fatalf("matching connector/origin token=%q ok=%v", got, ok)
	}
}

const invalidSchemaMCPFixtureEnv = "GO_WANT_INVALID_SCHEMA_MCP_FIXTURE"
const invalidSchemaMCPMarkerEnv = "GO_INVALID_SCHEMA_MCP_MARKER"

func TestCallToolForServerInspectsSchemaBeforeToolsCall(t *testing.T) {
	if os.Getenv(invalidSchemaMCPFixtureEnv) == "1" {
		runInvalidSchemaMCPFixture(t)
		return
	}
	marker := filepath.Join(t.TempDir(), "tools-call-received")
	_, err := CallToolForServer(context.Background(), t.TempDir(), "fixture", "echo", map[string]any{"text": "hello"}, ServerConfig{
		Type: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=TestCallToolForServerInspectsSchemaBeforeToolsCall", "--"},
		Env:  map[string]string{invalidSchemaMCPFixtureEnv: "1", invalidSchemaMCPMarkerEnv: marker},
	})
	if err == nil || !strings.Contains(err.Error(), "inputSchema") {
		t.Fatalf("error=%v, want invalid schema rejection", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("tools/call was sent after invalid schema: %v", statErr)
	}
}

func runInvalidSchemaMCPFixture(t *testing.T) {
	t.Helper()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), maxScannerTokenBytes)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		switch request.Method {
		case "server/discover":
			_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
		case "initialize":
			_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: mustRawJSON(map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}})})
		case "notifications/initialized":
			continue
		case "tools/list":
			_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: mustRawJSON(map[string]any{"tools": []any{map[string]any{"name": "echo"}}})})
		case "tools/call":
			if err := os.WriteFile(os.Getenv(invalidSchemaMCPMarkerEnv), []byte("called"), 0o600); err != nil {
				t.Fatal(err)
			}
			_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: mustRawJSON(map[string]any{"content": []any{map[string]any{"type": "text", "text": "unexpected"}}})})
		default:
			t.Fatalf("unexpected MCP method %q", request.Method)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestRemoteHTTPMCPUnauthorizedListsAuthenticateTool(t *testing.T) {
	t.Setenv("SYNON_MCP_CONFIG", "")
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://auth.example.test/.well-known/oauth-protected-resource"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
	}))
	defer server.Close()

	root := t.TempDir()
	writeMCPConfig(t, root, map[string]any{
		"mcpServers": map[string]any{
			"secure": map[string]any{
				"type": "http",
				"url":  server.URL,
			},
		},
	})
	t.Setenv("SYNON_MCP_CONFIG", filepath.Join(root, ".mcp.json"))

	list, err := ListTools(context.Background(), root, "secure")
	if err != nil {
		t.Fatalf("ListTools error = %v", err)
	}
	if len(list.Servers) != 1 || list.Servers[0].Status != "failed" {
		t.Fatalf("servers = %#v", list.Servers)
	}
	if len(list.Tools) != 0 {
		t.Fatalf("insecure remote config exposed tools: %#v", list.Tools)
	}
}

func TestRemoteHTTPMCPAuthenticateStoresTokenAndCallUsesBearer(t *testing.T) {
	t.Setenv("SYNON_MCP_CONFIG", "")
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	var authCodeSeen bool
	var tokenVerifierSeen bool
	var sawBearer bool
	var authBaseURL string
	var authServer *httptest.Server
	authServer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                           authBaseURL,
				"authorization_endpoint":           authBaseURL + "/authorize",
				"token_endpoint":                   authBaseURL + "/token",
				"response_types_supported":         []string{"code"},
				"grant_types_supported":            []string{"authorization_code"},
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/authorize":
			if r.URL.Query().Get("response_type") != "code" || r.URL.Query().Get("code_challenge") == "" || r.URL.Query().Get("state") == "" || r.URL.Query().Get("resource") == "" {
				t.Fatalf("authorize query = %s", r.URL.RawQuery)
			}
			redirectURI := r.URL.Query().Get("redirect_uri")
			state := r.URL.Query().Get("state")
			http.Redirect(w, r, redirectURI+"?code=auth-code-1&state="+state, http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "auth-code-1" || r.Form.Get("redirect_uri") == "" || r.Form.Get("resource") == "" {
				t.Fatalf("token form = %#v", r.Form)
			}
			if r.Form.Get("code_verifier") == "" {
				t.Fatalf("token missing code_verifier: %#v", r.Form)
			}
			authCodeSeen = true
			tokenVerifierSeen = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token-1", "token_type": "Bearer", "expires_in": 3600})
		default:
			t.Fatalf("unexpected auth path %s", r.URL.Path)
		}
	}))
	defer authServer.Close()

	mcpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token-1" {
			w.Header().Set("WWW-Authenticate", `Bearer authorization_metadata="`+authBaseURL+`/.well-known/oauth-authorization-server"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		sawBearer = true
		var req rpcMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		switch req.Method {
		case "server/discover":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{
				"code": -32601, "message": "method not found",
			}})
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "session-1")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "secure-fixture", "version": "1.0.0"}}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{
				"name": "echo", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
			}}}})
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "secure ok"}},
			}})
			_, _ = w.Write([]byte("event: message\n"))
			_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		default:
			t.Fatalf("unexpected MCP method %q", req.Method)
		}
	}))
	defer mcpServer.Close()
	authURL, err := url.Parse(authServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	mcpURL, err := url.Parse(mcpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, authPort, _ := net.SplitHostPort(authURL.Host)
	_, mcpPort, _ := net.SplitHostPort(mcpURL.Host)
	authBaseURL = "https://8.8.8.8:" + authPort
	mcpBaseURL := "https://8.8.4.4:" + mcpPort
	roots := x509.NewCertPool()
	roots.AddCert(authServer.Certificate())
	roots.AddCert(mcpServer.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
	baseDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		if host == "127.0.0.1" {
			return baseDial(ctx, network, address)
		}
		switch port {
		case authPort:
			return baseDial(ctx, network, authURL.Host)
		case mcpPort:
			return baseDial(ctx, network, mcpURL.Host)
		default:
			return nil, fmt.Errorf("unexpected OAuth fixture port %s", port)
		}
	}
	client := &http.Client{Transport: transport}
	ctx := withRemoteSecurityFixtureClient(context.Background(), client)

	root := t.TempDir()
	writeMCPConfig(t, root, map[string]any{"mcpServers": map[string]any{"secure": map[string]any{"type": "http", "url": mcpBaseURL}}})
	t.Setenv("SYNON_MCP_CONFIG", filepath.Join(root, ".mcp.json"))

	started, err := Authenticate(ctx, root, "secure")
	if err != nil {
		t.Fatalf("Authenticate error = %v", err)
	}
	if started.Status != "auth_url" || started.AuthURL == "" || started.RedirectURI == "" {
		t.Fatalf("Authenticate result = %#v", started)
	}
	resp, err := client.Get(started.AuthURL)
	if err != nil {
		t.Fatalf("visit auth URL: %v", err)
	}
	_ = resp.Body.Close()

	var output string
	for i := 0; i < 40; i++ {
		output, err = CallTool(ctx, root, "secure", "echo", map[string]any{"text": "hello"})
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("CallTool after auth error = %v", err)
	}
	if output != "secure ok" || !sawBearer || !authCodeSeen || !tokenVerifierSeen {
		t.Fatalf("OAuth MCP output=%q sawBearer=%v authCodeSeen=%v verifier=%v", output, sawBearer, authCodeSeen, tokenVerifierSeen)
	}
}

func TestOAuthTokenCacheUsesBoundedPrivatePathAndExpires(t *testing.T) {
	root := t.TempDir()
	serverName := "../secure/server"
	tokenPath := oauthTokenPath(root, serverName)
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("Abs(root) error = %v", err)
	}
	tokenAbs, err := filepath.Abs(tokenPath)
	if err != nil {
		t.Fatalf("Abs(tokenPath) error = %v", err)
	}
	rel, err := filepath.Rel(rootAbs, tokenAbs)
	if err != nil {
		t.Fatalf("Rel(tokenPath) error = %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("token cache path escaped root: %s", rel)
	}
	if filepath.Dir(tokenAbs) != filepath.Join(rootAbs, ".synon", "mcp-oauth") {
		t.Fatalf("token cache should stay under .synon/mcp-oauth: %s", tokenAbs)
	}
	if filepath.Base(tokenAbs) != NormalizeName(serverName)+".json" {
		t.Fatalf("token cache filename should be normalized: %s", filepath.Base(tokenAbs))
	}

	if err := saveOAuthToken(root, serverName, oauthTokenCache{AccessToken: "secret-token", TokenType: "Bearer", ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatalf("saveOAuthToken() error = %v", err)
	}
	if _, err := os.Stat(tokenAbs); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plaintext token cache exists: %v", err)
	}
	vaultPath := filepath.Join(rootAbs, ".synon", "mcp-oauth-vault", "secrets", "vault.enc")
	vault, err := os.ReadFile(vaultPath)
	if err != nil {
		t.Fatalf("read encrypted token vault: %v", err)
	}
	if bytes.Contains(vault, []byte("secret-token")) {
		t.Fatal("OAuth access token is visible in encrypted vault")
	}
	info, err := os.Stat(vaultPath)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("vault mode info=%v err=%v", info, err)
	}
	loaded, ok := loadOAuthAccessToken(root, serverName)
	if !ok || loaded != "secret-token" {
		t.Fatalf("loadOAuthAccessToken() = %q ok=%v", loaded, ok)
	}
	if err := DisconnectOAuth(root, serverName); err != nil {
		t.Fatalf("DisconnectOAuth() error = %v", err)
	}
	if loaded, ok := loadOAuthAccessToken(root, serverName); ok || loaded != "" {
		t.Fatalf("disconnected token should not load, got %q ok=%v", loaded, ok)
	}

	if err := saveOAuthToken(root, serverName, oauthTokenCache{AccessToken: "expired-token", TokenType: "Bearer", ExpiresAt: time.Now().Add(-time.Minute).Unix()}); err != nil {
		t.Fatalf("save expired token: %v", err)
	}
	if loaded, ok := loadOAuthAccessToken(root, serverName); ok || loaded != "" {
		t.Fatalf("expired token should not load, got %q ok=%v", loaded, ok)
	}
}

func TestOAuthTokenCacheMigratesAndDeletesLegacyPlaintext(t *testing.T) {
	root := t.TempDir()
	serverName := "legacy-server"
	path := oauthTokenPath(root, serverName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(oauthTokenCache{
		AccessToken: "legacy-plaintext-token",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, ok := loadOAuthAccessToken(root, serverName)
	if !ok || loaded != "legacy-plaintext-token" {
		t.Fatalf("load legacy token=%q ok=%v", loaded, ok)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy plaintext token was not deleted: %v", err)
	}
}

func TestMigrateLegacyOAuthTokensSweepsAllPlaintextFiles(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, ".synon", "mcp-oauth")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for index, serverName := range []string{"alpha-server", "beta-server"} {
		raw, err := json.Marshal(oauthTokenCache{
			AccessToken: "legacy-token-" + serverName,
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(time.Duration(index+1) * time.Hour).Unix(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(oauthTokenPath(root, serverName), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateLegacyOAuthTokens(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("legacy plaintext remains: %s", entry.Name())
		}
	}
	for _, serverName := range []string{"alpha-server", "beta-server"} {
		loaded, ok := loadOAuthAccessToken(root, serverName)
		if !ok || loaded != "legacy-token-"+serverName {
			t.Fatalf("%s loaded=%q ok=%v", serverName, loaded, ok)
		}
	}
}

func TestValidateOAuthEndpointRequiresHTTPSAndPublicDestination(t *testing.T) {
	for _, accepted := range []string{
		"https://login.example.com/oauth/token",
		"https://8.8.8.8/oauth/token",
	} {
		if err := validateOAuthEndpoint(accepted, "fixture"); err != nil {
			t.Fatalf("accepted %q: %v", accepted, err)
		}
	}
	for _, rejected := range []string{
		"http://127.0.0.1:8123/token",
		"http://[::1]:8123/token",
		"http://localhost:8123/token",
		"http://login.example.com/oauth/token",
		"http://10.0.0.2/token",
		"https://127.0.0.1/token",
		"https://10.0.0.2/token",
		"https://169.254.169.254/token",
		"https://192.0.2.10/token",
		"https://[::1]/token",
		"https://[fe80::1]/token",
		"https://[2001:db8::1]/token",
		"https://[2001::1]/token",
		"https://[2002:7f00::1]/token",
		"https://[fec0::1]/token",
		"ftp://login.example.com/token",
		"https://user:pass@login.example.com/token",
		"https://login.example.com/token#fragment",
		"/relative/token",
	} {
		if err := validateOAuthEndpoint(rejected, "fixture"); err == nil {
			t.Fatalf("rejected URL accepted: %q", rejected)
		}
	}
}

func TestValidateOAuthCallbackURIAllowsOnlyIPv4LoopbackHTTP(t *testing.T) {
	if err := validateOAuthCallbackURI("http://127.0.0.1:8123/callback"); err != nil {
		t.Fatalf("valid callback rejected: %v", err)
	}
	for _, rejected := range []string{
		"https://127.0.0.1:8123/callback",
		"http://localhost:8123/callback",
		"http://[::1]:8123/callback",
		"http://127.0.0.2:8123/callback",
		"http://127.0.0.1/callback",
		"http://127.0.0.1:8123/other",
	} {
		if err := validateOAuthCallbackURI(rejected); err == nil {
			t.Fatalf("rejected callback accepted: %q", rejected)
		}
	}
}

func TestOAuthDiscoveryPrefersProtectedResourceMetadata(t *testing.T) {
	var resourceRequests int
	var baseURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-protected-resource" {
			t.Fatalf("unexpected resource metadata path %q", r.URL.Path)
		}
		resourceRequests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_servers": []string{baseURL + "/issuer"},
		})
	}))
	defer server.Close()
	var client *http.Client
	baseURL, client = oauthTLSTestClient(t, server)
	ctx := WithHTTPClient(context.Background(), client)
	header := `Bearer authorization_metadata="https://9.9.9.9/legacy", resource_metadata="` +
		baseURL + `/.well-known/oauth-protected-resource"`
	got, err := oauthMetadataURLFromAuthenticateHeader(ctx, header)
	if err != nil {
		t.Fatal(err)
	}
	want := baseURL + "/.well-known/oauth-authorization-server/issuer"
	if got != want || resourceRequests != 1 {
		t.Fatalf("metadata=%q want=%q resourceRequests=%d", got, want, resourceRequests)
	}
}

func TestOAuthMetadataURLsPreserveEscapedIssuerPath(t *testing.T) {
	urls, err := oauthMetadataURLsForIssuer("https://auth.example.test/tenant%20one/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://auth.example.test/.well-known/oauth-authorization-server/tenant%20one",
		"https://auth.example.test/tenant%20one/.well-known/openid-configuration",
	}
	if strings.Join(urls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("metadata URLs=%#v want=%#v", urls, want)
	}
}

func TestOAuthMetadataSelectionPrefersDynamicRegistrationAcrossAuthorizationServers(t *testing.T) {
	var baseURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := map[string]any{
			"issuer": baseURL, "authorization_endpoint": baseURL + "/authorize",
			"token_endpoint": baseURL + "/token", "code_challenge_methods_supported": []string{"S256"},
		}
		if r.URL.Path == "/second" {
			metadata["registration_endpoint"] = baseURL + "/register"
			metadata["token_endpoint_auth_methods_supported"] = []string{"client_secret_post"}
		}
		_ = json.NewEncoder(w).Encode(metadata)
	}))
	defer server.Close()
	var client *http.Client
	baseURL, client = oauthTLSTestClient(t, server)
	ctx := WithHTTPClient(context.Background(), client)
	metadata, err := selectOAuthMetadata(ctx, []string{baseURL + "/first", baseURL + "/second"})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.RegistrationEndpoint != baseURL+"/register" {
		t.Fatalf("selected metadata = %#v", metadata)
	}
}

func TestOAuthDynamicRegistrationAndClientSecretPostTokenExchange(t *testing.T) {
	var baseURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["token_endpoint_auth_method"] != "client_secret_post" {
				t.Fatalf("registration body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"client_id": "dynamic-client", "client_secret": "dynamic-secret",
				"token_endpoint_auth_method": "client_secret_post",
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("client_id") != "dynamic-client" || r.Form.Get("client_secret") != "dynamic-secret" ||
				r.Form.Get("code_verifier") != "verifier" || r.Form.Get("resource") != "https://8.8.4.4/mcp" {
				t.Fatalf("token form = %#v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "dynamic-token", "token_type": "Bearer", "expires_in": 60})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var client *http.Client
	baseURL, client = oauthTLSTestClient(t, server)
	ctx := WithHTTPClient(context.Background(), client)
	credentials, err := registerOAuthClient(ctx, oauthMetadata{
		RegistrationEndpoint:              baseURL + "/register",
		TokenEndpointAuthMethodsSupported: []string{"client_secret_post", "client_secret_basic"},
	}, "http://127.0.0.1:8123/callback")
	if err != nil {
		t.Fatal(err)
	}
	token, err := exchangeOAuthCodeForClient(ctx, baseURL+"/token", "https://8.8.4.4/mcp", "code", "http://127.0.0.1:8123/callback", "verifier", credentials)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "dynamic-token" {
		t.Fatalf("token = %#v", token)
	}
}

func TestOAuthIssuerComparisonNormalizesOnlyTrailingSlash(t *testing.T) {
	if !sameOAuthIssuer("https://auth.example.test/", "https://auth.example.test") {
		t.Fatal("equivalent issuers were rejected")
	}
	if sameOAuthIssuer("https://evil.example.test", "https://auth.example.test") || sameOAuthIssuer("", "https://auth.example.test") {
		t.Fatal("mismatched or missing issuer was accepted")
	}
}

func TestOAuthMetadataAndTokenRedirectsAreRejected(t *testing.T) {
	var redirected int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "should-not-be-read"})
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()
	baseURL, client := oauthTLSTestClient(t, server)
	ctx := WithHTTPClient(context.Background(), client)

	if _, err := fetchResourceMetadata(ctx, baseURL+"/resource"); err == nil {
		t.Fatal("resource metadata redirect was accepted")
	}
	if _, err := fetchOAuthMetadata(ctx, baseURL+"/as-metadata"); err == nil {
		t.Fatal("authorization server metadata redirect was accepted")
	}
	if _, err := exchangeOAuthCode(ctx, baseURL+"/token", "https://8.8.8.8/resource", "code", "http://127.0.0.1:8123/callback", "verifier"); err == nil {
		t.Fatal("token redirect was accepted")
	}
	if redirected != 0 {
		t.Fatalf("redirect target was contacted %d times", redirected)
	}
}

func TestOAuthClientPinsResolvedAddressAgainstDNSRebinding(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": "https://8.8.8.8/authorize",
			"token_endpoint":         "https://8.8.8.8/token",
		})
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	var dialedAddress string
	transport := server.Client().Transport.(*http.Transport).Clone()
	baseDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialedAddress = address
		return baseDial(ctx, network, serverURL.Host)
	}
	transport.TLSClientConfig.ServerName = serverURL.Hostname()
	client := *server.Client()
	client.Transport = transport
	ctx := WithHTTPClient(context.Background(), &client)
	ctx = withOAuthResolver(ctx, oauthResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}))

	if _, err := fetchOAuthMetadata(ctx, "https://oauth.example.test:"+port+"/metadata"); err != nil {
		t.Fatalf("fetchOAuthMetadata() error = %v", err)
	}
	if dialedAddress != net.JoinHostPort("8.8.8.8", port) {
		t.Fatalf("dialed address = %q, want pinned public IP", dialedAddress)
	}
}

func TestOAuthClientRejectsPrivateDNSAnswerBeforeDial(t *testing.T) {
	dialed := false
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	}
	ctx := WithHTTPClient(context.Background(), &http.Client{Transport: transport})
	ctx = withOAuthResolver(ctx, oauthResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}))
	if _, err := fetchOAuthMetadata(ctx, "https://oauth.example.test/metadata"); err == nil {
		t.Fatal("private DNS answer was accepted")
	}
	if dialed {
		t.Fatal("transport dialed after private DNS answer")
	}
}

func TestOAuthMetadataRejectsAuthorizationEndpointWithPrivateDNS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": "https://auth.example.test/authorize",
			"token_endpoint":         "https://8.8.8.8/token",
		})
	}))
	defer server.Close()
	baseURL, client := oauthTLSTestClient(t, server)
	ctx := WithHTTPClient(context.Background(), client)
	ctx = withOAuthResolver(ctx, oauthResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "auth.example.test" {
			t.Fatalf("unexpected resolver host %q", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("10.0.0.8")}}, nil
	}))

	if _, err := fetchOAuthMetadata(ctx, baseURL+"/metadata"); err == nil {
		t.Fatal("authorization endpoint with private DNS was accepted")
	}
}

type oauthResolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (resolve oauthResolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return resolve(ctx, host)
}

func oauthTLSTestClient(t *testing.T, server *httptest.Server) (string, *http.Client) {
	t.Helper()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	baseDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return baseDial(ctx, network, serverURL.Host)
	}
	transport.TLSClientConfig.ServerName = serverURL.Hostname()
	client := *server.Client()
	client.Transport = transport
	return "https://8.8.8.8:" + port, &client
}

func TestRemoteWebSocketMCPListToolsAndCall(t *testing.T) {
	t.Setenv("SYNON_MCP_CONFIG", "")
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Fatalf("accept websocket: %v", err)
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		for {
			_, raw, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var req rpcMessage
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("decode websocket request: %v", err)
			}
			switch req.Method {
			case "initialize":
				writeWSRPC(t, r, conn, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"protocolVersion": "2024-11-05",
						"capabilities":    map[string]any{},
						"serverInfo":      map[string]any{"name": "ws-fixture", "version": "1.0.0"},
					},
				})
			case "notifications/initialized":
				writeWSRPC(t, r, conn, map[string]any{"jsonrpc": "2.0", "id": 99, "method": "roots/list"})
			case "tools/list":
				writeWSRPC(t, r, conn, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{"tools": []any{map[string]any{
						"name":        "echo",
						"description": "echo input",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
					}}},
				})
			case "tools/call":
				writeWSRPC(t, r, conn, map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  map[string]any{"content": []any{map[string]any{"type": "text", "text": "ws ok"}}},
				})
			case "":
			default:
				t.Fatalf("unexpected MCP websocket method %q", req.Method)
			}
		}
	}))
	defer server.Close()

	root := t.TempDir()
	writeMCPConfig(t, root, map[string]any{
		"mcpServers": map[string]any{
			"wsremote": map[string]any{
				"type":    "ws",
				"url":     "ws" + strings.TrimPrefix(server.URL, "http"),
				"headers": map[string]any{"X-Synon-Test": "ws-mcp"},
			},
		},
	})
	t.Setenv("SYNON_MCP_CONFIG", filepath.Join(root, ".mcp.json"))
	list, err := ListTools(context.Background(), root, "wsremote")
	if err != nil {
		t.Fatalf("ListTools websocket error = %v", err)
	}
	if len(list.Servers) != 1 || list.Servers[0].Status != "failed" || len(list.Tools) != 0 {
		t.Fatalf("websocket transport was not rejected: %#v", list)
	}
}

func TestSDKMCPBridgeListToolsAndCall(t *testing.T) {
	if os.Getenv("GO_WANT_SDK_MCP_BRIDGE_FIXTURE") == "1" {
		runSDKMCPBridgeFixture(t)
		return
	}
	t.Setenv("SYNON_MCP_CONFIG", "")
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	t.Setenv(hostSecretEnvironment, "host-only-secret")
	root := t.TempDir()
	writeMCPConfig(t, root, map[string]any{
		"mcpServers": map[string]any{
			"sdk-fixture": map[string]any{
				"type":          "sdk",
				"name":          "sdk-fixture",
				"bridgeCommand": os.Args[0],
				"bridgeArgs":    []string{"-test.run=TestSDKMCPBridgeListToolsAndCall", "--"},
				"env":           map[string]any{explicitEnvironment: "sdk-configured-value"},
				"bridgeEnv":     map[string]any{"GO_WANT_SDK_MCP_BRIDGE_FIXTURE": "1"},
			},
		},
	})
	t.Setenv("SYNON_MCP_CONFIG", filepath.Join(root, ".mcp.json"))

	list, err := ListTools(context.Background(), root, "sdk-fixture")
	if err != nil {
		t.Fatalf("ListTools sdk bridge error = %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "mcp__sdk-fixture__echo" {
		t.Fatalf("sdk bridge tools = %#v", list.Tools)
	}
	output, err := CallTool(context.Background(), root, "sdk-fixture", "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool sdk bridge error = %v", err)
	}
	if output != "sdk ok" {
		t.Fatalf("sdk bridge output = %q", output)
	}
}

func TestStdioResourceSupportsBundledKetcherPayloadSize(t *testing.T) {
	const fixtureEnvironment = "GO_WANT_LARGE_MCP_RESOURCE_FIXTURE"
	const payloadBytes = 26190058
	if os.Getenv(fixtureEnvironment) == "1" {
		runLargeMCPResourceFixture(t, payloadBytes)
		return
	}

	root := t.TempDir()
	writeMCPConfig(t, root, map[string]any{
		"mcpServers": map[string]any{
			"large-resource": map[string]any{
				"type":    "stdio",
				"command": os.Args[0],
				"args":    []string{"-test.run=TestStdioResourceSupportsBundledKetcherPayloadSize", "--"},
				"env":     map[string]any{fixtureEnvironment: "1"},
			},
		},
	})
	t.Setenv("SYNON_MCP_CONFIG", filepath.Join(root, ".mcp.json"))
	result, err := ReadResource(context.Background(), root, "large-resource", "ui://ketcher-chemistry/editor")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contents) != 1 || len(result.Contents[0].Text) != payloadBytes {
		t.Fatalf("large MCP resource result = %d contents, %d bytes", len(result.Contents), len(result.Contents[0].Text))
	}
}

func runLargeMCPResourceFixture(t *testing.T, payloadBytes int) {
	t.Helper()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), maxScannerTokenBytes)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	for scanner.Scan() {
		var request rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		if request.ID == nil {
			continue
		}
		response := rpcMessage{JSONRPC: "2.0", ID: request.ID}
		switch request.Method {
		case "initialize":
			response.Result = mustRawJSON(map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"resources": map[string]any{}},
				"serverInfo":      map[string]any{"name": "large-resource", "version": "1.0.0"},
			})
		case "resources/read":
			response.Result = mustRawJSON(map[string]any{
				"contents": []any{map[string]any{
					"uri": "ui://ketcher-chemistry/editor", "mimeType": "text/html;profile=mcp-app",
					"text": strings.Repeat("K", payloadBytes),
				}},
			})
		default:
			response.Error = &rpcError{Code: -32601, Message: "method not found"}
		}
		if err := encoder.Encode(response); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func runSDKMCPBridgeFixture(t *testing.T) {
	t.Helper()
	if value := os.Getenv(hostSecretEnvironment); value != "" {
		t.Fatalf("host secret leaked into SDK MCP bridge: %q", value)
	}
	if value := os.Getenv(explicitEnvironment); value != "sdk-configured-value" {
		t.Fatalf("explicit SDK MCP environment = %q", value)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), maxScannerTokenBytes)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var envelope struct {
			Subtype    string     `json:"subtype"`
			ServerName string     `json:"server_name"`
			Message    rpcMessage `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatalf("decode sdk bridge envelope: %v", err)
		}
		if envelope.Subtype != "mcp_message" {
			t.Fatalf("subtype = %q", envelope.Subtype)
		}
		if envelope.ServerName != "sdk-fixture" {
			t.Fatalf("server_name = %q", envelope.ServerName)
		}
		response := rpcMessage{JSONRPC: "2.0", ID: envelope.Message.ID}
		switch envelope.Message.Method {
		case "initialize":
			response.Result = mustRawJSON(map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "sdk-fixture", "version": "1.0.0"},
			})
		case "notifications/initialized":
			response.Result = json.RawMessage(`null`)
		case "tools/list":
			response.Result = mustRawJSON(map[string]any{"tools": []any{map[string]any{
				"name":        "echo",
				"description": "echo input",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			}}})
		case "tools/call":
			response.Result = mustRawJSON(map[string]any{"content": []any{map[string]any{"type": "text", "text": "sdk ok"}}})
		default:
			response.Error = &rpcError{Code: -32601, Message: "method not found"}
		}
		if err := encoder.Encode(map[string]any{"mcp_response": response}); err != nil {
			t.Fatalf("encode sdk bridge response: %v", err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read sdk bridge stdin: %v", err)
	}
	os.Exit(0)
}

func writeWSRPC(t *testing.T, r *http.Request, conn *websocket.Conn, value map[string]any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal websocket rpc: %v", err)
	}
	if err := conn.Write(r.Context(), websocket.MessageText, raw); err != nil {
		t.Fatalf("write websocket rpc: %v", err)
	}
}

func writeMCPConfig(t *testing.T, root string, value map[string]any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

type pinnedRoundTripperStub struct{}

func (pinnedRoundTripperStub) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}

func (pinnedRoundTripperStub) PublicHTTPSPinned() bool { return true }

func TestSecureRemoteHTTPClientReusesPinnedTransport(t *testing.T) {
	transport := pinnedRoundTripperStub{}
	client := &http.Client{Transport: transport}
	ctx := WithHTTPClient(context.Background(), client)
	secured, err := secureRemoteHTTPClient(ctx, "https://8.8.8.8/mcp")
	if err != nil {
		t.Fatalf("secure client: %v", err)
	}
	if secured.Transport != transport {
		t.Fatalf("pinned transport was replaced: %T", secured.Transport)
	}
	if secured.CheckRedirect == nil {
		t.Fatal("redirect policy must remain explicit")
	}
}

func TestOAuthHTTPClientReusesPinnedTransport(t *testing.T) {
	transport := pinnedRoundTripperStub{}
	client := &http.Client{Transport: transport}
	ctx := WithHTTPClient(context.Background(), client)
	secured, err := oauthHTTPClientForContext(ctx)
	if err != nil {
		t.Fatalf("OAuth client: %v", err)
	}
	if secured.Transport != transport {
		t.Fatalf("pinned OAuth transport was replaced: %T", secured.Transport)
	}
	if secured.CheckRedirect == nil {
		t.Fatal("OAuth redirect policy must remain explicit")
	}
}

func TestOAuthHTTPClientRejectsUnknownCustomTransport(t *testing.T) {
	client := &http.Client{Transport: &roundTripFuncStub{}}
	ctx := WithHTTPClient(context.Background(), client)
	if _, err := oauthHTTPClientForContext(ctx); err == nil {
		t.Fatal("unknown custom OAuth transport was accepted")
	}
}

func TestSecureRemoteHTTPClientRejectsUnknownCustomTransport(t *testing.T) {
	transport := &roundTripFuncStub{}
	client := &http.Client{Transport: transport}
	ctx := WithHTTPClient(context.Background(), client)
	secured, err := secureRemoteHTTPClient(ctx, "https://8.8.8.8/mcp")
	if err != nil {
		t.Fatalf("secure client: %v", err)
	}
	if _, ok := secured.Transport.(pinnedRoundTripperStub); ok {
		t.Fatalf("custom non-pinned transport must not be reused: %T", secured.Transport)
	}
	if _, ok := secured.Transport.(*http.Transport); !ok {
		t.Fatalf("expected fresh default transport, got %T", secured.Transport)
	}
}

func TestStdioCatalogListingFollowsAndDeduplicatesMCPPagination(t *testing.T) {
	const fixtureEnvironment = "GO_WANT_PAGED_MCP_CATALOG_FIXTURE"
	if os.Getenv(fixtureEnvironment) == "1" {
		runPagedMCPCatalogFixture(t)
		return
	}
	root := t.TempDir()
	writeMCPConfig(t, root, map[string]any{
		"mcpServers": map[string]any{
			"paged": map[string]any{
				"type": "stdio", "command": os.Args[0],
				"args": []string{"-test.run=TestStdioCatalogListingFollowsAndDeduplicatesMCPPagination", "--"},
				"env":  map[string]any{fixtureEnvironment: "1"},
			},
		},
	})
	t.Setenv("SYNON_MCP_CONFIG", filepath.Join(root, ".mcp.json"))
	tools, err := ListTools(context.Background(), root, "paged")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 2 || tools.Tools[0].ToolName != "first" || tools.Tools[1].ToolName != "second" {
		t.Fatalf("paginated tools=%#v", tools.Tools)
	}
	resources, err := ListResources(context.Background(), root, "paged")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 || resources[0].URI != "resource://first" || resources[1].URI != "resource://second" {
		t.Fatalf("paginated resources=%#v", resources)
	}
}

func runPagedMCPCatalogFixture(t *testing.T) {
	t.Helper()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), maxScannerTokenBytes)
	encoder := json.NewEncoder(os.Stdout)
	listedTools := false
	listedResources := false
	for scanner.Scan() {
		var request rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: mustRawJSON(map[string]any{
				"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
			})})
		case "notifications/initialized":
			continue
		case "tools/list":
			cursor := pagedMCPFixtureCursor(request.Params)
			if cursor == "next" {
				if !listedTools {
					t.Fatal("tools/list continuation used a new MCP session")
				}
				_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: mustRawJSON(map[string]any{"tools": []any{
					map[string]any{"name": "first", "inputSchema": map[string]any{"type": "object"}},
					map[string]any{"name": "second", "inputSchema": map[string]any{"type": "object"}},
				}})})
				continue
			}
			listedTools = true
			_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: mustRawJSON(map[string]any{
				"tools":      []any{map[string]any{"name": "first", "inputSchema": map[string]any{"type": "object"}}},
				"nextCursor": "next",
			})})
		case "resources/list":
			cursor := pagedMCPFixtureCursor(request.Params)
			if cursor == "next" {
				if !listedResources {
					t.Fatal("resources/list continuation used a new MCP session")
				}
				_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: mustRawJSON(map[string]any{"resources": []any{
					map[string]any{"uri": "resource://first", "name": "first"},
					map[string]any{"uri": "resource://second", "name": "second"},
				}})})
				continue
			}
			listedResources = true
			_ = encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: request.ID, Result: mustRawJSON(map[string]any{
				"resources":  []any{map[string]any{"uri": "resource://first", "name": "first"}},
				"nextCursor": "next",
			})})
		default:
			t.Fatalf("unexpected method %q", request.Method)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func pagedMCPFixtureCursor(params any) string {
	values, _ := params.(map[string]any)
	cursor, _ := values["cursor"].(string)
	return cursor
}

type roundTripFuncStub struct{}

func (roundTripFuncStub) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}
