package server

import (
	"slices"
	"strings"
	"testing"
)

func TestResearchEvidenceGraphRejectsMissingIDsDanglingEdgesAndUnsupportedClaims(t *testing.T) {
	failures := validateResearchEvidenceGraph([]byte(`{
		"nodes":{
			"claims":[{"id":"C1"},{"id":"C2"}],
			"sources":[{"id":"S1"}],
			"trials":[{"nct":"NCT00000001"}]
		},
		"edges":[{"source":"MISSING","target":"C1","relation":"supports"}]
	}`))
	for _, want := range []string{
		"evidence_graph.json:node_missing_id:trials:0",
		"evidence_graph.json:dangling_edge_source:MISSING",
		"evidence_graph.json:unsupported_claim:C2",
	} {
		if !slices.Contains(failures, want) {
			t.Fatalf("failures %v do not contain %q", failures, want)
		}
	}
}

func TestResearchEvidenceGraphAcceptsClosedReferentiallyCompleteGraph(t *testing.T) {
	failures := validateResearchEvidenceGraph([]byte(`{
		"version":"1",
		"nodes":{"claims":[{"id":"C1"}],"sources":[{"id":"S1"}]},
		"edges":[{"source":"S1","target":"C1","relation":"supports"}]
	}`))
	if len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
}

func TestResearchProvenanceManifestBindsEveryProducedArtifactHash(t *testing.T) {
	scopeHash := strings.Repeat("a", 64)
	graphHash := strings.Repeat("b", 64)
	produced := map[string]researchProducedArtifact{
		"scope-v1":    {artifactID: "scope-artifact", name: "scope.json", versionID: "scope-v1", sha256: scopeHash},
		"graph-v1":    {artifactID: "graph-artifact", name: "evidence_graph.json", versionID: "graph-v1", sha256: graphHash},
		"manifest-v1": {artifactID: "manifest-artifact", name: "provenance_manifest.json", versionID: "manifest-v1", sha256: strings.Repeat("c", 64)},
	}
	failures, _ := validateResearchProvenanceManifest([]byte(`{
		"schema":"synon.research_provenance_manifest.v1",
		"artifacts_produced":[
			{"artifact_id":"scope-artifact","version_id":"scope-v1","filename":"scope.json","sha256":"`+strings.Repeat("d", 64)+`"},
			{"artifact_id":"missing-artifact","version_id":"missing-v1","filename":"compute_results.jsonl","sha256":"schema_only_no_data"}
		]
	}`), produced)
	for _, want := range []string{
		"provenance_manifest.json:sha256_mismatch:scope.json",
		"provenance_manifest.json:invalid_sha256:missing-v1",
	} {
		if !slices.Contains(failures, want) {
			t.Fatalf("failures %v do not contain %q", failures, want)
		}
	}

	valid, selected := validateResearchProvenanceManifest([]byte(`{
		"schema":"synon.research_provenance_manifest.v1",
		"artifacts_produced":[
			{"artifact_id":"scope-artifact","version_id":"scope-v1","sha256":"`+scopeHash+`"},
			{"artifact_id":"graph-artifact","version_id":"graph-v1","filename":"evidence_graph.json","sha256":"`+graphHash+`"}
		]
	}`), produced)
	if len(valid) != 0 || len(selected) != 2 || selected["scope.json"].versionID != "scope-v1" {
		t.Fatalf("valid manifest failures=%v selected=%#v", valid, selected)
	}

	selfReference, _ := validateResearchProvenanceManifest([]byte(`{
		"schema":"synon.research_provenance_manifest.v1",
		"artifacts_produced":[
			{"artifact_id":"manifest-artifact","version_id":"manifest-v1","sha256":"`+strings.Repeat("c", 64)+`"}
		]
	}`), produced)
	if !slices.Contains(selfReference, "provenance_manifest.json:self_reference_forbidden") {
		t.Fatalf("self reference failures = %v", selfReference)
	}

	unknown, _ := validateResearchProvenanceManifest([]byte(`{
		"schema":"synon.research_provenance_manifest.v1",
		"artifacts_produced":[{"artifact_id":"scope-artifact","version_id":"scope-v1","sha256":"`+scopeHash+`","notes":"not allowed"}]
	}`), produced)
	if !slices.Contains(unknown, "provenance_manifest.json:invalid_json") {
		t.Fatalf("unknown-field failures=%v", unknown)
	}

	stale, _ := validateResearchProvenanceManifest([]byte(`{
		"schema":"synon.research_provenance_manifest.v1",
		"artifacts_produced":[{"artifact_id":"scope-artifact","version_id":"scope-v0","sha256":"`+scopeHash+`"}]
	}`), produced)
	if !slices.Contains(stale, "provenance_manifest.json:unbound_version:scope-v0") {
		t.Fatalf("stale-version failures=%v", stale)
	}
}
