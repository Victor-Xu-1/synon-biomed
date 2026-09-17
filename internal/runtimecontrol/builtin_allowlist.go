package runtimecontrol

import (
	"encoding/json"
	"sort"
	"strings"
)

type BuiltinAllowlistGroup struct {
	ID          string
	Label       string
	Description string
	Locked      bool
	Domains     []string
}

const builtinAllowlistJSON = "[{\"id\":\"pkg\",\"label\":\"Package management\",\"description\":\"pip, conda, npm, CRAN, Bioconductor, GitHub\",\"locked\":true,\"domains\":[\"pypi.org\",\"*.pypi.org\",\"files.pythonhosted.org\",\"conda.anaconda.org\",\"repo.anaconda.com\",\"anaconda.org\",\"*.anaconda.org\",\"*.conda.io\",\"cran.r-project.org\",\"cloud.r-project.org\",\"bioconductor.org\",\"www.bioconductor.org\",\"registry.npmjs.org\",\"github.com\",\"*.github.com\",\"*.githubusercontent.com\"]},{\"id\":\"nih\",\"label\":\"NCBI / NIH\",\"description\":\"PubMed, Entrez, NIH\",\"locked\":false,\"domains\":[\"*.ncbi.nlm.nih.gov\",\"*.nih.gov\",\"cactus.nci.nih.gov\"]},{\"id\":\"genomics\",\"label\":\"Genomics & biology\",\"description\":\"Ensembl, Reactome, KEGG, gnomAD, GTEx, ENCODE\",\"locked\":false,\"domains\":[\"rest.ensembl.org\",\"grch37.rest.ensembl.org\",\"*.ensembl.org\",\"reactome.org\",\"*.reactome.org\",\"rest.kegg.jp\",\"*.kegg.jp\",\"cellguide.cellxgene.cziscience.com\",\"gnomad.broadinstitute.org\",\"gtexportal.org\",\"jaspar.elixir.no\",\"www.encodeproject.org\",\"mygene.info\",\"rfam.org\",\"www.cbioportal.org\",\"sparql.rhea-db.org\",\"bindingdb.org\",\"www.bindingdb.org\",\"r12.finngen.fi\",\"pheweb.jp\",\"api.genome.ucsc.edu\",\"unibind.uio.no\"]},{\"id\":\"proteomics\",\"label\":\"Proteomics\",\"description\":\"UniProt, STRING, EBI, Foldseek, RCSB PDB, Protein Atlas\",\"locked\":false,\"domains\":[\"rest.uniprot.org\",\"*.uniprot.org\",\"string-db.org\",\"*.string-db.org\",\"*.ebi.ac.uk\",\"search.foldseek.com\",\"rcsb.org\",\"*.rcsb.org\",\"*.proteinatlas.org\"]},{\"id\":\"literature\",\"label\":\"Literature & citations\",\"description\":\"Semantic Scholar, arXiv, bioRxiv, Crossref, DOI, OpenAlex\",\"locked\":false,\"domains\":[\"api.semanticscholar.org\",\"api.biorxiv.org\",\"www.biorxiv.org\",\"api.crossref.org\",\"doi.org\",\"api.openalex.org\",\"arxiv.org\",\"*.arxiv.org\"]},{\"id\":\"clinical\",\"label\":\"Clinical & pharma\",\"description\":\"FDA, ClinicalTrials, Open Targets, COSMIC, ClinGen, CIViC\",\"locked\":false,\"domains\":[\"api.fda.gov\",\"clinicaltrials.gov\",\"*.clinicaltrials.gov\",\"api.clinpgx.org\",\"api.platform.opentargets.org\",\"cancer.sanger.ac.uk\",\"actionability.clinicalgenome.org\",\"search.clinicalgenome.org\",\"erepo.genome.network\",\"civicdb.org\",\"api.grants.gov\",\"www.antibodyregistry.org\",\"cartblanche22.docking.org\",\"files.docking.org\"]}]"

var builtinAllowlistGroups = mustBuiltinAllowlistGroups()

func BuiltinAllowlistGroups() []BuiltinAllowlistGroup {
	groups := make([]BuiltinAllowlistGroup, len(builtinAllowlistGroups))
	for i, group := range builtinAllowlistGroups {
		groups[i] = group
		groups[i].Domains = append([]string(nil), group.Domains...)
	}
	return groups
}

func EffectiveBuiltinAllowlist(disabled, disabledGroups []string) []string {
	disabledSet := stringSet(disabled)
	groupSet := stringSet(disabledGroups)
	domains := make([]string, 0)
	seen := map[string]struct{}{}
	for _, group := range builtinAllowlistGroups {
		if !group.Locked && groupSet[group.ID] {
			continue
		}
		for _, domain := range group.Domains {
			if !group.Locked && disabledSet[domain] {
				continue
			}
			if _, exists := seen[domain]; exists {
				continue
			}
			seen[domain] = struct{}{}
			domains = append(domains, domain)
		}
	}
	sort.Strings(domains)
	return domains
}

func NormalizeBuiltinAllowlistSelection(values []string) []string {
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

func mustBuiltinAllowlistGroups() []BuiltinAllowlistGroup {
	var groups []BuiltinAllowlistGroup
	if err := json.Unmarshal([]byte(builtinAllowlistJSON), &groups); err != nil {
		panic("invalid embedded v1.1 builtin allowlist: " + err.Error())
	}
	if len(groups) != 6 {
		panic("embedded v1.1 builtin allowlist count changed")
	}
	return groups
}

func stringSet(values []string) map[string]bool {
	output := make(map[string]bool, len(values))
	for _, value := range NormalizeBuiltinAllowlistSelection(values) {
		output[value] = true
	}
	return output
}
