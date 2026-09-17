package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTrustedScientificReviewSignalsRestoreOnlyFromSuccessfulServerEvidence(t *testing.T) {
	kernelInput := map[string]any{"pdb_id": "8D7U"}
	kernelInputJSON, err := json.Marshal(kernelInput)
	if err != nil {
		t.Fatal(err)
	}
	kernelResult := `{"pdb_id":"8D7U","ligands":[{"comp_id":"QFC"}]}`
	kernelResultJSON, err := json.Marshal(kernelResult)
	if err != nil {
		t.Fatal(err)
	}
	kernelSchemaJSON, err := json.Marshal(map[string]any{
		"type": "object", "properties": map[string]any{"pdb_id": map[string]any{"type": "string"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName":      "mcp__pubmed__search_articles",
			"schema":        workspaceMCPSourceEvidenceSchemaV1,
			"evidenceClass": workspace.KernelMCPEvidenceClassBundledReadOnly,
			"connectorId":   "bundled:pubmed", "connectorSource": "bundled", "readOnlyHint": true,
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName":  "WebFetch",
			"toolInput": map[string]any{"url": "https://pubchem.ncbi.nlm.nih.gov/compound/156124857"},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName":  "save_artifacts",
			"toolInput": map[string]any{"files": []any{"design/candidates.sdf", "design/readme.csv"}},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "failed", "toolPhase": "failed",
			"toolName":  "WebFetch",
			"toolInput": map[string]any{"url": "https://pubchem.ncbi.nlm.nih.gov/compound/invalid"},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName":      "mcp__custom__search",
			"evidenceClass": workspace.KernelMCPEvidenceClassBundledReadOnly,
			"connectorId":   "custom:pubmed-lookalike", "connectorSource": "custom", "readOnlyHint": true,
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"schema": "synon.kernel_mcp_evidence.v1", "toolName": "mcp__structures-interactions__pdb_get_ligands",
			"toolCallId": "host-pdb-1", "toolInput": kernelInput, "toolResult": kernelResult,
			"evidenceClass": workspace.KernelMCPEvidenceClassBundledReadOnly,
			"connectorId":   "bundled:structures-interactions", "connectorSource": "bundled", "readOnlyHint": true,
			"inputSchemaSha256": kernelMCPEvidenceSHA256(kernelSchemaJSON),
			"outerToolCallId":   "repl-pdb-1", "kernelOperationId": "operation-pdb-1",
			"executionId": "execution-pdb-1", "hostCallId": "host-pdb-1",
			"kernelId": "kernel-pdb-1", "kernelGeneration": int64(1),
			"requestSha256": kernelMCPEvidenceSHA256(kernelInputJSON),
			"resultSha256":  kernelMCPEvidenceSHA256(kernelResultJSON),
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName": softwareRuntimeToolName, "toolResult": map[string]any{"ok": true},
			trustedScientificReviewSignalsField: []any{
				"execution-tool:software_runtime", "capability-execution:molecular-docking",
			},
		}},
	}

	want := []string{
		"artifact-format:csv",
		"artifact-format:sdf",
		"capability-execution:molecular-docking",
		"execution-tool:software_runtime",
		"source-connector:bundled:pubmed",
		"source-connector:bundled:structures-interactions",
	}
	if got := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("trusted scientific signals = %#v, want %#v", got, want)
	}
}

func TestTrustedScientificEvidenceRecordSignalsPreserveExactDeepReads(t *testing.T) {
	patent := trustedScientificEvidenceRecordSignals(
		"patent_search",
		map[string]any{"operation": "lookup", "publication_number": "CN117362283B"},
		map[string]any{"ok": true, "result": map[string]any{
			"evidenceDepth": "full_record",
			"records": []any{map[string]any{
				"publicationNumber": "CN117362283B", "recordDepth": "full_record", "focusRelevant": true,
			}},
		}},
	)
	if !reflect.DeepEqual(patent, []string{"evidence-record:patent:CN117362283B"}) {
		t.Fatalf("patent record signals=%#v", patent)
	}
	trial := trustedScientificEvidenceRecordSignals(
		"mcp__clinical-trials__get_trial_details",
		map[string]any{"nct_id": "NCT04616014"},
		map[string]any{"ok": true, "result": map[string]any{
			"found": true, "trial": map[string]any{"primary_outcomes": []any{"Exposure"}},
		}},
	)
	if !reflect.DeepEqual(trial, []string{"evidence-record:trial:NCT04616014"}) {
		t.Fatalf("trial record signals=%#v", trial)
	}
	trialString := trustedScientificEvidenceRecordSignals(
		"mcp__clinical-trials__get_trial_details",
		map[string]any{"nct_id": "NCT04616014"},
		`{"found":true,"trial":{"nct_id":"NCT04616014","brief_summary":"Measured exposure and safety.","primary_outcomes":[{"measure":"Exposure"}]}}`,
	)
	if !reflect.DeepEqual(trialString, []string{"evidence-record:trial:NCT04616014"}) {
		t.Fatalf("string-encoded trial record signals=%#v", trialString)
	}
	webURL := "https://company.example/disclosures/phase-3"
	web := trustedScientificEvidenceRecordSignals(
		"web_research",
		map[string]any{"operation": "search_and_fetch", "query": "phase 3"},
		map[string]any{"documents": []any{map[string]any{
			"url": webURL, "content": strings.Repeat("Measured efficacy and safety evidence. ", 40),
			"readReceipt": map[string]any{"deepRead": true, "responseComplete": true},
		}}},
	)
	wantWeb := []string{"evidence-record:web:" + runnerEvidenceCanonicalWeb(webURL)}
	if !reflect.DeepEqual(web, wantWeb) {
		t.Fatalf("generic web record signals=%#v want=%#v", web, wantWeb)
	}
}

func TestTrustedScientificPatentDiscoveryContinuitySurvivesCrossLanguageLookup(t *testing.T) {
	discovery := trustedScientificPatentDiscoverySignals(
		"patent_search",
		map[string]any{"operation": "search", "query": "口服小分子受体激动剂"},
		map[string]any{"ok": true, "result": map[string]any{
			"records": []any{map[string]any{"publicationNumber": "CN115698003B"}},
		}},
	)
	wantDiscovery := []string{"evidence-discovery:patent:CN115698003B"}
	if !reflect.DeepEqual(discovery, wantDiscovery) || !validTrustedScientificReviewSignal(discovery[0]) {
		t.Fatalf("patent discovery signals=%#v want=%#v", discovery, wantDiscovery)
	}
	lookup := trustedScientificPatentContinuitySignals(
		"patent_search",
		map[string]any{
			"operation": "lookup", "publication_number": "CN115698003B",
			"focus_terms": []any{"口服小分子", "受体激动剂"},
		},
		map[string]any{"ok": true, "result": map[string]any{
			"evidenceDepth": "full_record",
			"records": []any{map[string]any{
				"publicationNumber": "CN115698003B", "recordDepth": "full_record", "focusRelevant": false,
			}},
		}},
		discovery, "",
	)
	if !reflect.DeepEqual(lookup, []string{"evidence-record:patent:CN115698003B"}) {
		t.Fatalf("cross-language patent lookup lost discovery continuity: %#v", lookup)
	}
	if signals := trustedScientificPatentContinuitySignals(
		"patent_search",
		map[string]any{"operation": "lookup", "publication_number": "CN115698003B"},
		map[string]any{"result": map[string]any{
			"evidenceDepth": "full_record",
			"records":       []any{map[string]any{"recordDepth": "full_record"}},
		}},
		nil, "",
	); len(signals) != 0 {
		t.Fatalf("undiscovered patent was promoted to screened evidence: %#v", signals)
	}
	correction := "evidence_record_depth_missing:sources.csv row=2 source_type=patent identifier=CN115698003B"
	if signals := trustedScientificPatentContinuitySignals(
		"patent_search",
		map[string]any{"operation": "lookup", "publication_number": "CN115698003B"},
		map[string]any{"result": map[string]any{
			"evidenceDepth": "full_record",
			"records":       []any{map[string]any{"recordDepth": "full_record"}},
		}},
		nil, correction,
	); !reflect.DeepEqual(signals, []string{"evidence-record:patent:CN115698003B"}) {
		t.Fatalf("validator-directed exact patent read lost continuity: %#v", signals)
	}
}

func TestTrustedScientificReviewSignalsPersistMetadataOnlyOpenAlexRouteExhaustion(t *testing.T) {
	input := map[string]any{"work_id": "https://doi.org/10.1111/dom.71032"}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result := `{"id":"https://openalex.org/W1234567890","doi":"https://doi.org/10.1111/dom.71032","title":"Oral small molecule GLP-1 receptor agonists","abstract":null,"abstract_policy":"omitted due to upstream license"}`
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	schemaJSON, err := json.Marshal(map[string]any{
		"type": "object", "properties": map[string]any{"work_id": map[string]any{"type": "string"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"schema": "synon.kernel_mcp_evidence.v1", "toolName": "mcp__literature__openalex_get_work",
		"toolCallId": "host-openalex-1", "toolInput": input, "toolResult": result,
		"evidenceClass": workspace.KernelMCPEvidenceClassBundledReadOnly,
		"connectorId":   "bundled:literature", "connectorSource": "bundled", "readOnlyHint": true,
		"inputSchemaSha256": kernelMCPEvidenceSHA256(schemaJSON),
		"outerToolCallId":   "repl-openalex-1", "kernelOperationId": "operation-openalex-1",
		"executionId": "execution-openalex-1", "hostCallId": "host-openalex-1",
		"kernelId": "kernel-openalex-1", "kernelGeneration": int64(1),
		"requestSha256": kernelMCPEvidenceSHA256(inputJSON),
		"resultSha256":  kernelMCPEvidenceSHA256(resultJSON),
	}}

	want := []string{
		"evidence-route-exhausted:publication:repl",
		"source-connector:bundled:literature",
	}
	if got := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{entry}); !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata-only OpenAlex route signals=%#v want=%#v", got, want)
	}
	if got := trustedScientificEvidenceRecordSignals(
		"mcp__literature__openalex_get_work", input, result,
	); len(got) != 0 {
		t.Fatalf("metadata-only OpenAlex result became a substantive record: %#v", got)
	}
}

func TestTrustedScientificReviewSignalsPersistOpenAlexDiscoveryIdentifiers(t *testing.T) {
	input := map[string]any{"query": "oral small molecule", "include_abstracts": true}
	result := `{"records":[{"doi":"10.1000/title-only","title":"Title only","abstract":null}]}`
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	schemaJSON, err := json.Marshal(map[string]any{
		"type": "object", "properties": map[string]any{
			"query":             map[string]any{"type": "string"},
			"include_abstracts": map[string]any{"type": "boolean"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"schema": "synon.kernel_mcp_evidence.v1", "toolName": "mcp__literature__openalex_search_works",
		"toolCallId":    "host-openalex-search-1",
		"toolInput":     input,
		"toolResult":    result,
		"evidenceClass": workspace.KernelMCPEvidenceClassBundledReadOnly,
		"connectorId":   "bundled:literature", "connectorSource": "bundled", "readOnlyHint": true,
		"inputSchemaSha256": kernelMCPEvidenceSHA256(schemaJSON), "outerToolCallId": "repl-openalex-search-1",
		"kernelOperationId": "operation-openalex-search-1", "executionId": "execution-openalex-search-1",
		"hostCallId": "host-openalex-search-1", "kernelId": "kernel-openalex-search-1",
		"kernelGeneration": int64(1), "requestSha256": kernelMCPEvidenceSHA256(inputJSON),
		"resultSha256": kernelMCPEvidenceSHA256(resultJSON),
	}}

	got := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{entry})
	want := []string{
		"evidence-discovery:publication:doi:10.1000/title-only",
		"source-connector:bundled:literature",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenAlex discovery signals=%#v want=%#v", got, want)
	}
}

func TestTrustedScientificEvidenceDiscoverySignalsRetainTrialIdentifiers(t *testing.T) {
	got := trustedScientificEvidenceDiscoverySignals(
		"mcp__clinical-trials__search_trials",
		`{"items":[{"nct_id":"NCT04616014","title":"Candidate trial"}]}`,
	)
	want := []string{"evidence-discovery:trial:NCT04616014"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trial discovery signals=%#v want=%#v", got, want)
	}
}

func TestTrustedScientificReviewSignalsPersistUnavailableFullTextRouteExhaustion(t *testing.T) {
	server := &Server{}
	got := server.trustedScientificReviewSignalsForCompletedTool(
		"fetch_article_fulltext",
		map[string]any{"doi": "10.1000/unavailable"},
		map[string]any{"ok": true, "result": map[string]any{
			"available": false, "recordAvailable": false, "status": "not_available",
		}},
		nil,
	)
	want := []string{
		"evidence-route-exhausted:publication:fetcharticlefulltext",
		"scientific-tool:fetch_article_fulltext",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unavailable full-text route signals=%#v want=%#v", got, want)
	}
}

func TestTrustedScientificReviewSignalsPersistUnwrappedUnavailableFullTextRouteExhaustion(t *testing.T) {
	server := &Server{}
	got := server.trustedScientificReviewSignalsForCompletedTool(
		"fetch_article_fulltext",
		map[string]any{"doi": "10.1000/unavailable"},
		map[string]any{
			"available": false, "recordAvailable": false, "sourceUnavailable": true,
			"status": "source_unavailable", "statusCode": float64(503),
		},
		nil,
	)
	want := []string{"evidence-route-exhausted:publication:fetcharticlefulltext"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unwrapped unavailable full-text route signals=%#v want=%#v", got, want)
	}
	run := &sessionRunnerChatRun{}
	run.addTrustedScientificReviewSignals(got...)
	if persisted := run.trustedScientificReviewSignalsSnapshot(); !reflect.DeepEqual(persisted, want) {
		t.Fatalf("normalized unavailable route signal was dropped from durable state: %#v", persisted)
	}
}

func TestTrustedScientificReviewSignalsPersistCompletedEmptyPatentSearch(t *testing.T) {
	server := &Server{}
	got := server.trustedScientificReviewSignalsForCompletedTool(
		"patent_search",
		map[string]any{"operation": "search", "query": "oral small molecule inhibitor"},
		map[string]any{"ok": true, "result": map[string]any{
			"operation": "search", "records": []any{},
			"retrieval": map[string]any{"complete": true, "source_attempts": 20, "source_failures": 20},
		}},
		nil,
	)
	want := []string{"evidence-route-exhausted:patent:patentsearch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("completed empty patent search signals=%#v want=%#v", got, want)
	}
	run := &sessionRunnerChatRun{}
	run.addTrustedScientificReviewSignals(got...)
	if persisted := run.trustedScientificReviewSignalsSnapshot(); !reflect.DeepEqual(persisted, want) {
		t.Fatalf("empty patent route signal was not retained in durable state: %#v", persisted)
	}
}

func TestTrustedScientificReviewSignalsReplaySuccessfulExecutionWithoutLegacySignalField(t *testing.T) {
	digest := strings.Repeat("a", 64)
	verifiedRuntime := map[string]any{
		"ok": true, "status": "completed", "exit_status": "ok", "provider_id": "local-conda",
		"environment": "runtime-environment", "executable": "python",
		"runtime_generation": digest, "request_digest": digest,
		"stdout_sha256": digest, "stderr_sha256": digest,
		"cleanup": map[string]any{
			"process_group_terminated": true, "process_tree_terminated": true, "temporary_streams_closed": true,
		},
		"outputs": []any{},
	}
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName": "python", "toolResult": map[string]any{"ok": true, "stdout": "validated"},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName": "repl", "toolResult": map[string]any{"ok": true, "result": "validated"},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName": softwareRuntimeToolName, "toolResult": verifiedRuntime,
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "failed", "toolPhase": "failed",
			"toolName": "python", "toolResult": map[string]any{"ok": false, "error": "failed"},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
			"toolName": "python",
		}},
	}
	want := []string{"execution-tool:python", "execution-tool:repl", "execution-tool:software_runtime"}
	if got := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed execution signals = %#v, want %#v", got, want)
	}
}

func TestTrustedScientificReviewSignalsRequireSourceBeforeMaterializedExecution(t *testing.T) {
	source := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName":                          "download_public_scientific_file",
		trustedScientificReviewSignalsField: []any{trustedScientificValidatedPublicDownloadSignal},
	}}
	preparation := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName":   "python",
		"toolResult": map[string]any{"ok": true, "exit_status": "ok", "files_written": []any{}},
	}}
	analysis := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "python",
		"toolResult": map[string]any{
			"ok": true, "exit_status": "ok",
			"files_written": []any{map[string]any{"path": "results/composition.csv"}},
		},
	}}

	preparedBeforeSource := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{preparation, source})
	if foldedSetContains(foldedSet(preparedBeforeSource...), trustedScientificSourceGroundedExecutionSignalPrefix+"python") {
		t.Fatalf("pre-source environment preparation became source-grounded analysis: %#v", preparedBeforeSource)
	}
	analysedAfterSource := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{preparation, source, analysis})
	if !foldedSetContains(foldedSet(analysedAfterSource...), trustedScientificSourceGroundedExecutionSignalPrefix+"python") {
		t.Fatalf("post-source materialized analysis lost its causal evidence: %#v", analysedAfterSource)
	}
}

func TestTrustedScientificReviewSignalsAcceptValidatedRCSBSearchOnly(t *testing.T) {
	server := &Server{}
	valid := map[string]any{
		"retrieved_at":          "2026-08-16T08:00:00Z",
		"search_request_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"entries": []any{map[string]any{
			"entry_id": "8EHV", "initial_release_date": "2023-09-20T00:00:00.000+00:00",
			"structure_url": "https://www.rcsb.org/structure/8EHV",
			"metadata_url":  "https://data.rcsb.org/rest/v1/core/entry/8EHV",
		}},
	}
	want := []string{
		"scientific-tool:search_rcsb_structures", "source-host:data.rcsb.org", "source-host:search.rcsb.org",
	}
	if got := server.trustedScientificReviewSignalsForCompletedTool("search_rcsb_structures", nil, valid, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("valid RCSB search signals=%#v", got)
	}

	invalid := map[string]any{}
	for key, value := range valid {
		invalid[key] = value
	}
	invalid["entries"] = []any{map[string]any{
		"entry_id": "8EHV", "initial_release_date": "2023-09-20T00:00:00.000+00:00",
		"structure_url": "https://www.rcsb.org/structure/8EHV",
		"metadata_url":  "https://attacker.invalid/rest/v1/core/entry/8EHV",
	}}
	if got := server.trustedScientificReviewSignalsForCompletedTool("search_rcsb_structures", nil, invalid, nil); !reflect.DeepEqual(got, []string{"scientific-tool:search_rcsb_structures"}) {
		t.Fatalf("invalid RCSB search trusted source signals=%#v", got)
	}
}

func TestTrustedScientificReviewSignalsExcludeRecoverableUnavailableWebFetch(t *testing.T) {
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName":  "web_fetch",
		"toolInput": map[string]any{"url": "https://chembl.ebi.ac.uk/api/data/molecule/invalid"},
		"toolResult": map[string]any{
			"statusCode":        float64(404),
			"sourceUnavailable": true,
			"error":             "HTTP source returned 404 Not Found.",
		},
	}}}

	if got := trustedScientificReviewSignalsFromRunnerEntries(entries); len(got) != 0 {
		t.Fatalf("unavailable WebFetch restored trusted scientific signals: %#v", got)
	}
}

func TestTrustedScientificReviewSignalsExcludeUnavailableArticleFullText(t *testing.T) {
	server := &Server{}
	unavailable := map[string]any{
		"ok": true,
		"result": map[string]any{
			"available": false,
			"status":    "not_available",
			"reason":    "no_open_access_full_text",
			"sourceUrl": "https://www.ebi.ac.uk/europepmc/webservices/rest/articles/PMC1/fullTextXML",
		},
	}
	if got := server.trustedScientificReviewSignalsForCompletedTool(
		"fetch_article_fulltext", map[string]any{"pmcid": "PMC1"}, unavailable, nil,
	); !reflect.DeepEqual(got, []string{
		"evidence-route-exhausted:publication:fetcharticlefulltext",
		"scientific-tool:fetch_article_fulltext",
	}) {
		t.Fatalf("unavailable full text gained source authority: %#v", got)
	}

	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "fetch_article_fulltext", "toolResult": unavailable,
		trustedScientificReviewSignalsField: []any{
			"scientific-tool:fetch_article_fulltext", "source-host:www.ebi.ac.uk",
		},
	}}}
	if got := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(got, []string{"scientific-tool:fetch_article_fulltext"}) {
		t.Fatalf("unavailable replay restored source authority: %#v", got)
	}
}

func TestTrustedScientificReviewSignalsAcceptAvailableArticleFullText(t *testing.T) {
	server := &Server{}
	available := map[string]any{
		"ok": true,
		"result": map[string]any{
			"available": true,
			"status":    "available",
			"body":      "Primary article full text.",
			"sourceUrl": "https://www.ebi.ac.uk/europepmc/webservices/rest/articles/PMC1/fullTextXML",
		},
	}
	got := server.trustedScientificReviewSignalsForCompletedTool(
		"fetch_article_fulltext", map[string]any{"pmcid": "PMC1"}, available, nil,
	)
	found := false
	for _, signal := range got {
		found = found || strings.EqualFold(signal, "source-host:www.ebi.ac.uk")
	}
	if !found {
		t.Fatalf("available full text did not gain source authority: %#v", got)
	}
}

func TestTrustedScientificReviewSignalsRecordRealExecutionTools(t *testing.T) {
	server := &Server{}
	result := map[string]any{"ok": true, "stdout": "real runtime output"}
	got := server.trustedScientificReviewSignalsForCompletedTool("python", nil, result, nil)
	if !reflect.DeepEqual(got, []string{"execution-tool:python"}) {
		t.Fatalf("python signals=%#v", got)
	}
	got = server.trustedScientificReviewSignalsForCompletedTool("python", nil, map[string]any{
		"ok": true, "status": "code_preflight_required", "executed": false,
	}, nil)
	if len(got) != 0 {
		t.Fatalf("non-executing Python preflight became scientific evidence: %#v", got)
	}
	got = server.trustedScientificReviewSignalsForCompletedTool("download_rcsb_file", nil, result, nil)
	if !reflect.DeepEqual(got, []string{"scientific-tool:download_rcsb_file", "source-host:files.rcsb.org"}) {
		t.Fatalf("RCSB signals=%#v", got)
	}
}

func TestTrustedScientificReviewSignalsRecognizeManagedLanguageRuntime(t *testing.T) {
	server := &Server{}
	input := map[string]any{"environment": "crbn-binding-analysis"}
	result := map[string]any{
		"ok": true, "exit_status": "ok",
		"kernel_id": "kernel-managed", "exec_id": "exec-managed",
		"stdout": "RDKit version: 2026.03.5",
	}
	got := server.trustedScientificReviewSignalsForCompletedTool("python", input, result, nil)
	want := []string{"execution-tool:python", "managed-environment-execution:python"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("managed Python signals=%#v, want %#v", got, want)
	}
	systemInput := map[string]any{"environment": "python"}
	if got := server.trustedScientificReviewSignalsForCompletedTool("python", systemInput, result, nil); !reflect.DeepEqual(got, []string{"execution-tool:python"}) {
		t.Fatalf("system Python was promoted to managed execution: %#v", got)
	}
}

func TestTrustedScientificReviewSignalsRecordRCSBDownloadAsAuthoritativeSource(t *testing.T) {
	server := &Server{}
	result := map[string]any{"ok": true, "artifact_id": "artifact-rcsb", "source_url": "https://files.rcsb.org/download/9OHQ.cif"}
	got := server.trustedScientificReviewSignalsForCompletedTool("download_rcsb_file", nil, result, nil)
	if !reflect.DeepEqual(got, []string{"scientific-tool:download_rcsb_file", "source-host:files.rcsb.org"}) {
		t.Fatalf("RCSB authoritative source signals=%#v", got)
	}
}

func TestTrustedScientificReviewSignalsRecordValidatedPublicDownloadWithoutHostAllowlist(t *testing.T) {
	server := &Server{}
	input := map[string]any{
		"url": "https://www.crystallography.net/cod/9003308.cif",
	}
	result := map[string]any{
		"ok": true,
		"download": map[string]any{
			"source_tool_call_id": "cod-source-call",
			"source_url":          "https://www.crystallography.net/cod/9003308.cif",
		},
		"artifacts": []any{map[string]any{
			"artifact_id": "cod-cif-artifact", "version_id": "cod-cif-version",
		}},
	}
	got := server.trustedScientificReviewSignalsForCompletedTool(
		"download_public_scientific_file", input, result, nil,
	)
	want := []string{
		"scientific-tool:download_public_scientific_file",
		trustedScientificValidatedPublicDownloadSignal,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validated public download signals=%#v want=%#v", got, want)
	}
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "download_public_scientific_file", "toolInput": input, "toolResult": result,
	}}}
	if restored := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(restored, want) {
		t.Fatalf("restored validated public download signals=%#v want=%#v", restored, want)
	}
}

func TestTrustedScientificReviewSignalsRecordValidatedPublicWebFetchWithoutHostAllowlist(t *testing.T) {
	server := &Server{}
	input := map[string]any{
		"url": "https://webbook.nist.gov/cgi/cbook.cgi?ID=C64175&Mask=4",
	}
	result := map[string]any{
		"ok": true,
		"result": map[string]any{
			"code": float64(200), "url": input["url"],
			"result": "NIST Chemistry WebBook phase change data",
		},
	}
	got := server.trustedScientificReviewSignalsForCompletedTool("WebFetch", input, result, nil)
	want := []string{trustedScientificValidatedPublicWebFetchSignal, "source-host:webbook.nist.gov"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validated public WebFetch signals=%#v want=%#v", got, want)
	}
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "WebFetch", "toolInput": input, "toolResult": result,
	}}}
	if restored := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(restored, want) {
		t.Fatalf("restored validated public WebFetch signals=%#v want=%#v", restored, want)
	}
}

func TestTrustedScientificReviewSignalsAcceptOnlySubstantialDurableSearchExcerpt(t *testing.T) {
	server := &Server{}
	retrievedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result := map[string]any{"ok": true, "result": map[string]any{
		"sources": []any{map[string]any{
			"status": "search_result", "evidenceState": "discovered", "sourceQuality": "discovery",
			"title": "Mechanistic PK-PD modelling for first-in-human dose selection",
			"url":   "https://doi.org/10.1111/example", "canonicalUrl": "https://doi.org/10.1111/example",
			"snippet":      strings.Repeat("Mechanistic models integrate pharmacology and exposure evidence. ", 5),
			"qualityScore": float64(80), "retrievedAt": retrievedAt,
		}},
	}}
	want := []string{trustedScientificValidatedSearchExcerptSignal, "source-host:doi.org"}
	if got := server.trustedScientificReviewSignalsForCompletedTool("web_search", nil, result, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("validated search excerpt signals=%#v want=%#v", got, want)
	}
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "web_search", "toolResult": result,
	}}}
	if got := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed search excerpt signals=%#v want=%#v", got, want)
	}

	nested := result["result"].(map[string]any)
	source := nested["sources"].([]any)[0].(map[string]any)
	source["snippet"] = "short discovery title only"
	if got := server.trustedScientificReviewSignalsForCompletedTool("web_search", nil, result, nil); len(got) != 0 {
		t.Fatalf("short search metadata became authoritative evidence: %#v", got)
	}
	source["snippet"] = strings.Repeat("Substantial but unavailable. ", 12)
	nested["failure"] = map[string]any{"kind": "search_unavailable"}
	if got := server.trustedScientificReviewSignalsForCompletedTool("web_search", nil, result, nil); len(got) != 0 {
		t.Fatalf("failed search became authoritative evidence: %#v", got)
	}
}

func TestTrustedScientificReviewSignalsRequireCompleteDeepWebResearchRead(t *testing.T) {
	server := &Server{}
	result := map[string]any{"ok": true, "result": map[string]any{
		"sources": []any{map[string]any{
			"status": "fetched", "url": "https://example.org/primary-record",
			"readReceipt": map[string]any{
				"deepRead": true, "responseComplete": true,
				"extractableCharacters": float64(1200), "truncated": false, "partial": false,
			},
		}},
	}}
	want := []string{trustedScientificValidatedWebResearchReadSignal, "source-host:example.org"}
	if got := server.trustedScientificReviewSignalsForCompletedTool("web_research", nil, result, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("deep WebResearch signals=%#v want=%#v", got, want)
	}
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "web_research", "toolResult": result,
	}}}
	if got := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed deep WebResearch signals=%#v want=%#v", got, want)
	}

	receipt := result["result"].(map[string]any)["sources"].([]any)[0].(map[string]any)["readReceipt"].(map[string]any)
	receipt["responseComplete"] = false
	receipt["truncated"] = true
	if got := server.trustedScientificReviewSignalsForCompletedTool("web_research", nil, result, nil); len(got) != 0 {
		t.Fatalf("truncated WebResearch source became authoritative evidence: %#v", got)
	}
}

func TestTrustedScientificReviewSignalsRestoreValidatedPublicWebFetchFromLargeResultReceipt(t *testing.T) {
	input := map[string]any{
		"url": "https://webbook.nist.gov/cgi/cbook.cgi?ID=C108883&Mask=4",
	}
	result := map[string]any{
		"artifact_id": "large-tool-result-f4fc0f7a59f2d318dce482877297d5c5",
		"version_id":  "ltr-bb09bb3c-c08b-4991-8e2b-14bdf5e07428",
		"sha256":      strings.Repeat("a", 64), "size_bytes": float64(112590),
		"content_type": "application/json", "outcome": "succeeded",
		"content_url": "/api/artifacts/large-tool-result-f4fc0f7a59f2d318dce482877297d5c5/versions/ltr-bb09bb3c-c08b-4991-8e2b-14bdf5e07428",
		"preview":     `{"ok":true,"result":{"code":200`, "truncated": true,
	}
	entries := []eventjournal.Entry{{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "WebFetch", "toolInput": input, "toolResult": result,
	}}}
	want := []string{trustedScientificValidatedPublicWebFetchSignal, "source-host:webbook.nist.gov"}
	if restored := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(restored, want) {
		t.Fatalf("restored large-result WebFetch signals=%#v want=%#v", restored, want)
	}
}

func TestTrustedScientificReviewSignalsRejectUnprovenPublicWebFetch(t *testing.T) {
	server := &Server{}
	validURL := "https://webbook.nist.gov/cgi/cbook.cgi?ID=C64175&Mask=4"
	tests := []struct {
		name   string
		input  map[string]any
		result map[string]any
	}{
		{
			name:   "private input",
			input:  map[string]any{"url": "https://127.0.0.1/source"},
			result: map[string]any{"code": float64(200), "url": "https://127.0.0.1/source", "result": "private"},
		},
		{
			name:   "reserved input",
			input:  map[string]any{"url": "https://source.example.test/data"},
			result: map[string]any{"code": float64(200), "url": "https://source.example.test/data", "result": "reserved"},
		},
		{
			name:   "non https input",
			input:  map[string]any{"url": "http://webbook.nist.gov/source"},
			result: map[string]any{"code": float64(200), "url": "http://webbook.nist.gov/source", "result": "plain http"},
		},
		{
			name:   "unavailable",
			input:  map[string]any{"url": validURL},
			result: map[string]any{"code": float64(404), "url": validURL, "sourceUnavailable": true, "error": "not found"},
		},
		{
			name:   "empty body",
			input:  map[string]any{"url": validURL},
			result: map[string]any{"code": float64(200), "url": validURL, "result": ""},
		},
		{
			name:   "forged large receipt",
			input:  map[string]any{"url": validURL},
			result: map[string]any{"artifact_id": "not-runner-evidence", "outcome": "succeeded"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := server.trustedScientificReviewSignalsForCompletedTool("WebFetch", test.input, test.result, nil); len(got) != 0 {
				t.Fatalf("unproven WebFetch gained source authority: %#v", got)
			}
		})
	}
}

func TestTrustedScientificReviewSignalsRejectUnprovenPublicDownload(t *testing.T) {
	server := &Server{}
	input := map[string]any{"url": "https://data.example.test/result.cif"}
	for _, result := range []map[string]any{
		{"ok": true, "download": map[string]any{"source_url": input["url"]}},
		{"ok": true, "download": map[string]any{
			"source_tool_call_id": "source", "source_url": "https://other.example.test/result.cif",
		}, "artifacts": []any{map[string]any{"artifact_id": "a", "version_id": "v"}}},
	} {
		got := server.trustedScientificReviewSignalsForCompletedTool(
			"download_public_scientific_file", input, result, nil,
		)
		if !reflect.DeepEqual(got, []string{"scientific-tool:download_public_scientific_file"}) {
			t.Fatalf("unproven public download gained source authority: %#v", got)
		}
	}
}

func TestTrustedScientificReviewSignalsEnrichReviewOnlyAfterUserOptIn(t *testing.T) {
	signals := []string{"source-host:pubchem.ncbi.nlm.nih.gov"}
	policy, err := resolveSessionRunnerReviewPolicy(sessionRunnerReviewPolicyInput{
		SessionID: "frame-medchem-source", StreamUID: "frame:frame-medchem-source", RunnerAttempt: 1,
		ClaimedInputRevision: 2, RootAgent: "OPERON", UserRequestedEvidenceReview: true,
		TrustedScientificSignals: signals,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !policy.EvidenceReviewRequired || len(policy.ScientificReviewerProfiles) != 0 {
		t.Fatalf("source-resolved policy = %#v", policy)
	}
	if !foldedSetContains(foldedSet(policy.ResolutionSignals...), signals[0]) {
		t.Fatalf("policy omitted trusted source signal: %#v", policy.ResolutionSignals)
	}
}

func TestSessionRunnerToolCheckpointPersistsTrustedScientificReviewSignals(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	run := &sessionRunnerChatRun{
		SessionID:  "frame-save",
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	ctx := withWorkspaceMCPSourceEvidenceCollector(context.Background())
	recordWorkspaceMCPSourceEvidence(ctx, "direct-pubmed-runtime", workspaceMCPSourceEvidenceAttestation{
		Schema: workspaceMCPSourceEvidenceSchemaV1, EvidenceClass: workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID: "bundled:pubmed", ConnectorSource: "bundled", ReadOnlyHint: true,
	})
	arguments, err := json.Marshal(map[string]any{"query": "KRAS G12D"})
	if err != nil {
		t.Fatal(err)
	}
	encodedResult, err := json.Marshal(map[string]any{"ok": true, "result": `{"pmids":["40997784"]}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointSessionRunnerToolEvent(ctx, SessionRunnerChatOptions{SessionID: "frame-save"}, run, agentruntime.Event{
		Type: agentruntime.EventToolCompleted, ToolName: "mcp__pubmed__search_articles",
		ToolCallID: "direct-pubmed-runtime", Arguments: string(arguments), Result: string(encodedResult),
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"source-connector:bundled:pubmed"}
	if got := run.trustedScientificReviewSignalsSnapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("live trusted scientific signals = %#v, want %#v", got, want)
	}
	events, err := fixture.repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]eventjournal.Entry, 0, len(events))
	for _, event := range events {
		payload := eventjournal.Message{}
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		payload["type"] = event.Event.Type
		entries = append(entries, eventjournal.Entry{Message: payload})
	}
	if got := trustedScientificReviewSignalsFromRunnerEntries(entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("durable trusted scientific signals = %#v, want %#v", got, want)
	}
}

func TestTrustedScientificReviewSignalsTreatValidatedUserArtifactsAsTaskInputAuthority(t *testing.T) {
	userInput := eventjournal.Entry{Message: eventjournal.Message{
		"type": "user_input_response", "role": "user",
		"artifactRefs": []any{map[string]any{
			"artifact_id": "artifact-input", "version_id": "version-input",
			"filename": "target.pdb", "content_type": "chemical/x-pdb",
			"size_bytes": float64(49491), "checksum": strings.Repeat("a", 64),
		}},
	}}
	analysis := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "python",
		"toolResult": map[string]any{
			"ok": true, "exit_status": "ok",
			"files_written": []any{map[string]any{"path": "results/input_audit.csv"}},
			"scientific_witness": map[string]any{
				"inputs": map[string]any{"primary": strings.Repeat("a", 64)},
			},
		},
	}}

	signals := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{userInput, analysis})
	want := []string{
		"execution-tool:python",
		trustedScientificUserArtifactInputSignal,
		trustedScientificUserArtifactDigestSignalPrefix + strings.Repeat("a", 64),
		trustedScientificUserArtifactGroundedExecutionSignalPrefix + "python",
	}
	if !reflect.DeepEqual(signals, want) {
		t.Fatalf("user-artifact analysis signals=%#v, want %#v", signals, want)
	}
	if sessionRunnerHasAuthoritativeSourceEvidence(signals) {
		t.Fatal("an unrelated user artifact gained authority for an explicit public-source task")
	}
}

func TestTrustedScientificReviewSignalsIgnoreKernelAuthoredInputArtifactClaims(t *testing.T) {
	userInput := eventjournal.Entry{Message: eventjournal.Message{
		"type": "user_input_response", "role": "user",
		"artifactRefs": []any{map[string]any{
			"artifact_id": "artifact-input", "version_id": "version-input",
			"filename": "target.pdb", "size_bytes": float64(49491),
			"checksum": strings.Repeat("a", 64),
		}},
	}}
	forged := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "python",
		"toolResult": map[string]any{
			"ok": true, "exit_status": "ok",
			"files_written": []any{map[string]any{"path": "results/constant.csv"}},
			"input_artifacts": []any{map[string]any{
				"version_id": "version-input", "checksum": strings.Repeat("a", 64),
			}},
		},
	}}

	signals := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{userInput, forged})
	if containsAskUserEvidenceReference(signals, trustedScientificUserArtifactGroundedExecutionSignalPrefix+"python") {
		t.Fatalf("kernel-authored input_artifacts gained grounded authority: %#v", signals)
	}
}

func TestTrustedScientificReviewSignalsDoNotGroundUnrelatedOutputAfterAttachment(t *testing.T) {
	userInput := eventjournal.Entry{Message: eventjournal.Message{
		"type": "user_input_response", "role": "user",
		"artifactRefs": []any{map[string]any{
			"artifact_id": "artifact-input", "version_id": "version-input",
			"filename": "target.pdb", "content_type": "chemical/x-pdb",
			"size_bytes": float64(49491), "checksum": strings.Repeat("a", 64),
		}},
	}}
	unrelated := eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": "python",
		"toolResult": map[string]any{
			"ok": true, "exit_status": "ok",
			"files_written": []any{map[string]any{"path": "results/constant.csv"}},
		},
	}}
	signals := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{userInput, unrelated})
	if foldedSetContains(foldedSet(signals...), trustedScientificUserArtifactGroundedExecutionSignalPrefix+"python") {
		t.Fatalf("unrelated output acquired attachment-grounded execution: %#v", signals)
	}
}

func TestTrustedScientificReviewSignalsRejectMalformedUserArtifactAuthority(t *testing.T) {
	entry := eventjournal.Entry{Message: eventjournal.Message{
		"type": "message", "role": "user",
		"artifactRefs": []any{map[string]any{
			"artifact_id": "artifact-input", "version_id": "version-input",
			"filename": "target.pdb", "content_type": "chemical/x-pdb", "size_bytes": float64(1),
		}},
	}}
	if got := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{entry}); len(got) != 0 {
		t.Fatalf("malformed artifact references gained authority: %#v", got)
	}
}

func TestFailedMCPCheckpointRetainsAttemptAttestationWithoutEvidenceSignal(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	run := &sessionRunnerChatRun{
		SessionID:  fixture.stream.SessionID,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	ctx := withWorkspaceMCPSourceEvidenceCollector(context.Background())
	inputSchema := []byte(`{"type":"object","properties":{"query":{"type":"string"}}}`)
	recordWorkspaceMCPSourceEvidence(ctx, "failed-pubmed-runtime", workspaceMCPSourceEvidenceAttestation{
		Schema: workspaceMCPSourceEvidenceSchemaV1, EvidenceClass: workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID: "bundled:pubmed", ConnectorSource: "bundled", ReadOnlyHint: true,
		InputSchemaSHA256: kernelMCPEvidenceSHA256(inputSchema),
	})
	arguments := `{"query":"POLQ inhibitor"}`
	result := `{"ok":false,"error":{"code":"upstream_reset"}}`
	if err := fixture.server.checkpointSessionRunnerToolEvent(ctx,
		SessionRunnerChatOptions{SessionID: fixture.stream.SessionID}, run, agentruntime.Event{
			Type: agentruntime.EventToolFailed, ToolName: "mcp__pubmed__search_articles",
			ToolCallID: "failed-pubmed-runtime", Arguments: arguments, Result: result,
		}); err != nil {
		t.Fatal(err)
	}
	if signals := run.trustedScientificReviewSignalsSnapshot(); len(signals) != 0 {
		t.Fatalf("failed MCP attempt was promoted to scientific evidence: %#v", signals)
	}
	events, err := fixture.repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		var checkpoint sessionRunnerDurableToolCheckpoint
		if json.Unmarshal(event.ResolvedPayloadJSON, &checkpoint) != nil || checkpoint.ToolCallID != "failed-pubmed-runtime" {
			continue
		}
		found = true
		if checkpoint.ToolPhase != "failed" || !validSessionRunnerMCPDurableCheckpoint(checkpoint) ||
			!researchCheckpointExecutedTerminal(checkpoint) || !fixture.server.sessionRunnerResearchAttemptTool(checkpoint) {
			t.Fatalf("failed MCP terminal identity was not durable: %#v", checkpoint)
		}
	}
	if !found {
		t.Fatal("failed MCP checkpoint was not persisted")
	}
}
