package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRunnerEvidenceRecordDepthRequiresRecordReadsNotDiscoveryHits(t *testing.T) {
	messages := []agentruntime.Message{
		runnerEvidenceDepthCall("patent-discovery", "patent_search", map[string]any{
			"operation": "search", "query": "oral peptide",
		}),
		runnerEvidenceDepthResult("patent-discovery", map[string]any{
			"evidenceDepth": "discovery_only",
			"records":       []any{map[string]any{"publicationNumber": "US20230093875A1"}},
		}),
		runnerEvidenceDepthCall("trial-detail", "mcp__clinical-trials__get_trial_details", map[string]any{
			"nct_id": "NCT04616014",
		}),
		runnerEvidenceDepthResult("trial-detail", map[string]any{
			"found": true, "trial": map[string]any{
				"nct_id": "NCT04616014", "title": "Oral insulin",
				"primary_outcomes": []any{map[string]any{"measure": "Exposure"}},
			},
		}),
		runnerEvidenceDepthCall("paper-fulltext", "fetch_article_fulltext", map[string]any{
			"doi": "10.1002/adhm.202500946",
		}),
		runnerEvidenceDepthResult("paper-fulltext", map[string]any{
			"available": true, "evidenceState": "full-text-read", "body": strings.Repeat("evidence ", 80),
		}),
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(messages)
	snapshot := runnerCrossArtifactSnapshot{
		name: "delivery_evidence.csv",
		text: "evidence_type,identifier,url\n" +
			"专利,US20230093875A1,https://patents.google.com/patent/US20230093875A1/en\n" +
			"临床试验,NCT04616014,https://clinicaltrials.gov/study/NCT04616014\n" +
			"文献,10.1002/adhm.202500946,https://doi.org/10.1002/adhm.202500946\n",
	}
	failures := runnerEvidenceRecordDepthFailures(snapshot, index)
	if len(failures) != 1 || !strings.Contains(failures[0], "source_type=专利") ||
		!strings.Contains(failures[0], "identifier=US20230093875A1") {
		t.Fatalf("record-depth failures=%#v", failures)
	}

	messages = append(messages,
		runnerEvidenceDepthCall("patent-lookup", "patent_search", map[string]any{
			"operation": "lookup", "publicationNumber": "US20230093875A1",
		}),
		runnerEvidenceDepthResult("patent-lookup", map[string]any{
			"evidenceDepth": "full_record",
			"records": []any{map[string]any{
				"publicationNumber": "US20230093875A1", "recordDepth": "full_record",
			}},
		}),
	)
	if failures := runnerEvidenceRecordDepthFailures(
		snapshot, runnerEvidenceRecordDepthIndexFromMessages(messages),
	); len(failures) != 0 {
		t.Fatalf("full record reads were rejected: %#v", failures)
	}
}

func TestRunnerEvidenceRecordDepthRejectsTitleOnlyPublicationAndTrialRecords(t *testing.T) {
	titleOnly := []agentruntime.Message{
		runnerEvidenceDepthCall("paper", "mcp__literature__get_work", map[string]any{"id": "https://doi.org/10.1000/example"}),
		runnerEvidenceDepthResult("paper", map[string]any{"doi": "10.1000/example", "title": "A title only record"}),
		runnerEvidenceDepthCall("trial", "mcp__clinical-trials__get_trial_details", map[string]any{"nct_id": "NCT04616014"}),
		runnerEvidenceDepthResult("trial", map[string]any{"found": true, "trial": map[string]any{"title": "A title only trial"}}),
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(titleOnly)
	if index.contains("publication", "doi:10.1000/example") || index.contains("trial", "NCT04616014") {
		t.Fatalf("title-only records counted as read: %#v", index)
	}

	substantive := append(titleOnly,
		runnerEvidenceDepthCall("paper-deep", "mcp__literature__get_work", map[string]any{"id": "https://doi.org/10.1000/example"}),
		runnerEvidenceDepthResult("paper-deep", map[string]any{
			"doi": "10.1000/example", "title": "A read record", "abstract": "Measured efficacy and exposure were reported.",
		}),
		runnerEvidenceDepthCall("trial-deep", "mcp__clinical-trials__get_trial_details", map[string]any{"nct_id": "NCT04616014"}),
		runnerEvidenceDepthResult("trial-deep", map[string]any{
			"found": true, "trial": map[string]any{"primary_outcomes": []any{"Exposure"}},
		}),
	)
	index = runnerEvidenceRecordDepthIndexFromMessages(substantive)
	if !index.contains("publication", "doi:10.1000/example") || !index.contains("trial", "NCT04616014") {
		t.Fatalf("substantive records were not counted: %#v", index)
	}
}

func TestRunnerEvidenceRecordDepthAcceptsClosedPublicationAbstractRecord(t *testing.T) {
	messages := []agentruntime.Message{
		runnerEvidenceDepthCall("paper", "fetch_article_fulltext", map[string]any{"doi": "10.1000/closed"}),
		runnerEvidenceDepthResult("paper", map[string]any{
			"available": false, "recordAvailable": true, "recordDepth": "abstract_record",
			"abstractText": "Methods and measured efficacy results from the randomized study.",
		}),
	}
	if index := runnerEvidenceRecordDepthIndexFromMessages(messages); !index.contains("publication", "doi:10.1000/closed") {
		t.Fatalf("substantive abstract record was discarded when full text was closed: %#v", index)
	}
}

func TestRunnerEvidenceRecordDepthUsesSourceIDAndSeparatesConclusionsFromSources(t *testing.T) {
	messages := []agentruntime.Message{
		runnerEvidenceDepthCall("trial", "mcp__clinical-trials__get_trial_details", map[string]any{"nct_id": "NCT04616014"}),
		runnerEvidenceDepthResult("trial", map[string]any{
			"found": true, "trial": map[string]any{"primary_outcomes": []any{"Exposure"}},
		}),
		runnerEvidenceDepthCall("review", "mcp__literature__get_work", map[string]any{"id": "10.1000/review"}),
		runnerEvidenceDepthResult("review", map[string]any{
			"doi": "10.1000/review", "abstract": "A substantive review of methods and results.",
		}),
	}
	snapshot := runnerCrossArtifactSnapshot{
		name: "research_evidence.csv",
		text: "source_id,source_type,source_url,claim\n" +
			"NCT04616014,clinical_trial,,registered outcome\n" +
			"doi:10.1000/review,综述,,published synthesis\n" +
			"gap-1,信息缺口,,no verified human result\n" +
			"inference-1,推断,,plausible mechanism\n",
	}
	depth := runnerEvidenceRecordDepthIndexFromMessages(messages)
	if failures := runnerEvidenceRecordDepthFailures(snapshot, depth); len(failures) != 0 {
		t.Fatalf("source_id or conclusion classification rejected valid ledger: %#v", failures)
	}
	classes := runnerEvidenceRecordDepthClasses(snapshot, depth)
	if !classes["trial"] || !classes["publication"] || classes["web"] {
		t.Fatalf("evidence classes=%#v", classes)
	}
	for _, conclusionType := range []string{"information_gap", "信息缺口", "inference", "推断"} {
		if got := runnerEvidenceDepthClass(conclusionType); got != "" {
			t.Fatalf("conclusion type %q classified as source class %q", conclusionType, got)
		}
	}
	for _, reviewType := range []string{"review", "systematic_review", "综述", "系统综述"} {
		if got := runnerEvidenceDepthClass(reviewType); got != "publication" {
			t.Fatalf("review type %q classified as %q", reviewType, got)
		}
	}
}

func TestRunnerEvidenceRecordDepthMergesTrustedSignalsAcrossProjectionLag(t *testing.T) {
	index := newRunnerEvidenceRecordDepthIndex()
	runnerEvidenceRecordDepthMergeTrustedSignals(&index, []string{
		"evidence-record:trial:NCT06693843",
		"evidence-record:publication:pmid:42457940",
	})
	snapshot := runnerCrossArtifactSnapshot{
		name: "source_ledger.csv",
		text: "source_type,identifier,source_url\n" +
			"clinical_trial,NCT06693843,https://clinicaltrials.gov/study/NCT06693843\n" +
			"primary_publication,42457940,https://pubmed.ncbi.nlm.nih.gov/42457940/\n",
	}
	if failures := runnerEvidenceRecordDepthFailures(snapshot, index); len(failures) != 0 {
		t.Fatalf("server-trusted record signals did not satisfy typed source rows: %#v", failures)
	}
	if got := runnerEvidenceDepthClass("clinical_trial"); got != "trial" {
		t.Fatalf("clinical_trial class=%q, want trial", got)
	}
	if got := runnerEvidenceCanonicalPublication("https://pubmed.ncbi.nlm.nih.gov/42457940/"); got != "pmid:42457940" {
		t.Fatalf("PubMed locator=%q, want pmid:42457940", got)
	}
}

func TestRunnerEvidenceDepthClassRecognizesNCTSourceType(t *testing.T) {
	if got := runnerEvidenceDepthClass("NCT"); got != "trial" {
		t.Fatalf("NCT source class=%q, want trial", got)
	}
	snapshot := runnerCrossArtifactSnapshot{
		name: "traceable_sources.csv",
		text: "标识符类型,标识符,来源链接\n" +
			"NCT,NCT05261126,https://clinicaltrials.gov/study/NCT05261126\n",
	}
	index := newRunnerEvidenceRecordDepthIndex()
	if failures := runnerEvidenceRecordDepthFailures(snapshot, index); len(failures) != 1 ||
		!strings.Contains(failures[0], "source_type=NCT") ||
		!strings.Contains(failures[0], "source_type=trial") {
		t.Fatalf("NCT row depth failures=%#v", failures)
	}
}

func TestRunnerEvidenceDepthRecognizesIdentifierInLocalizedLinkColumn(t *testing.T) {
	snapshot := runnerCrossArtifactSnapshot{
		name: "evidence.csv",
		text: "来源类型,核心结论,链接\n" +
			"文献,验证了目标机制,https://doi.org/10.1000/example\n",
	}
	index := newRunnerEvidenceRecordDepthIndex()
	index.publications["doi:10.1000/example"] = struct{}{}
	if failures := runnerEvidenceRecordDepthFailures(snapshot, index); len(failures) != 0 {
		t.Fatalf("localized link identifier was not matched to its read record: %#v", failures)
	}
}

func TestRunnerEvidenceRouteExhaustionIsClassScoped(t *testing.T) {
	signals := []string{
		"evidence-route-exhausted:publication:fetcharticlefulltext",
		"evidence-route-exhausted:patent:patentsearch",
	}
	if !runnerEvidenceRouteExhaustedForClass("publication", signals) ||
		!runnerEvidenceRouteExhaustedForClass("patent", signals) {
		t.Fatalf("class exhaustion signals were not recognized: %#v", signals)
	}
	if runnerEvidenceRouteExhaustedForClass("trial", signals) {
		t.Fatal("trial was incorrectly inferred from unrelated source routes")
	}
}

func TestRunnerEvidenceRecordDepthRecognizesExactClinicalTrialReadThroughREPLMCP(t *testing.T) {
	messages := []agentruntime.Message{
		runnerEvidenceDepthCall("trial-repl", "repl", map[string]any{
			"code": `trial = host.mcp("clinical-trials", "get_trial_details", {"nct_id": "NCT04616014"})\nprint(trial.evidence)`,
		}),
		runnerEvidenceDepthResult("trial-repl", map[string]any{
			"ok": true, "exit_status": "ok", "stdout": "structured_record_read",
		}),
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(messages)
	if !index.contains("trial", "NCT04616014") {
		t.Fatalf("exact governed MCP trial read was lost at the REPL boundary: %#v", index)
	}

	notInspected := []agentruntime.Message{
		runnerEvidenceDepthCall("trial-repl", "repl", map[string]any{
			"code": `trial = host.mcp("clinical-trials", "get_trial_details", {"nct_id": "NCT04616014"})`,
		}),
		runnerEvidenceDepthResult("trial-repl", map[string]any{"ok": true, "exit_status": "ok"}),
	}
	if index := runnerEvidenceRecordDepthIndexFromMessages(notInspected); index.contains("trial", "NCT04616014") {
		t.Fatalf("uninspected MCP result was accepted as deep evidence: %#v", index)
	}
}

func TestRunnerEvidenceRecordDepthDecodesStructuredMCPJSONStrings(t *testing.T) {
	call := runnerEvidenceDepthCall(
		"openalex-work", "mcp__literature__openalex_get_work",
		map[string]any{"work_id": "https://doi.org/10.1000/example"},
	)
	structured := `{"doi":"10.1000/example","title":"Example",` +
		`"abstract_inverted_index":{"measured":[0],"outcome":[1]}}`
	encoded, _ := json.Marshal(structured)
	result := agentruntime.Message{Role: "tool", ToolCallID: "openalex-work", Content: string(encoded)}
	index := runnerEvidenceRecordDepthIndexFromMessages([]agentruntime.Message{call, result})
	if !index.contains("publication", "doi:10.1000/example") {
		t.Fatalf("JSON-string MCP record was not decoded as substantive publication evidence: %#v", index)
	}
}

func TestRunnerEvidenceRecordDepthAcceptsSubstantivePublicationLandingPage(t *testing.T) {
	body := "<article><h2>Abstract</h2><p>" + strings.Repeat("Measured outcomes and methods. ", 60) +
		"</p><h2>Results</h2><p>Clinically relevant results and conclusion.</p></article>"
	messages := []agentruntime.Message{
		runnerEvidenceDepthCall("paper-page", "web_fetch", map[string]any{
			"url": "https://doi.org/10.1000/example",
		}),
		runnerEvidenceDepthResult("paper-page", map[string]any{
			"url": "https://doi.org/10.1000/example", "statusCode": 200, "body": body,
		}),
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(messages)
	if !index.contains("publication", "doi:10.1000/example") {
		t.Fatalf("substantive publication landing page was not accepted: %#v", index)
	}

	thin := strings.Replace(body, strings.Repeat("Measured outcomes and methods. ", 60), "A title.", 1)
	messages[1] = runnerEvidenceDepthResult("paper-page", map[string]any{
		"url": "https://doi.org/10.1000/example", "statusCode": 200, "body": thin,
	})
	if index := runnerEvidenceRecordDepthIndexFromMessages(messages); index.contains("publication", "doi:10.1000/example") {
		t.Fatalf("thin title-only landing page was accepted: %#v", index)
	}
}

func TestRunnerEvidenceRecordDepthAcceptsSubstantiveLocalizedPublicationAtExactIdentifierURL(t *testing.T) {
	body := strings.Repeat("本研究系统考察了目标机制、实验方法、观察结果与主要结论，并报告了可复核的数据。", 80)
	messages := []agentruntime.Message{
		runnerEvidenceDepthCall("localized-paper", "web_fetch", map[string]any{
			"url": "https://cjournal.hep.com.cn/CN/10.20053/j.issn1001-5094.20250057",
		}),
		runnerEvidenceDepthResult("localized-paper", map[string]any{
			"url":        "https://cjournal.hep.com.cn/CN/10.20053/j.issn1001-5094.20250057",
			"statusCode": 200, "body": body,
		}),
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(messages)
	if !index.contains("publication", "doi:10.20053/j.issn1001-5094.20250057") {
		t.Fatalf("substantive localized publication was not bound to its exact identifier: %#v", index)
	}
}

func TestRunnerEvidenceRecordDepthRequiresDeepReadForGenericWebLedgerRows(t *testing.T) {
	url := "https://company.example/news/oral-glp1-phase-3"
	snapshot := runnerCrossArtifactSnapshot{
		name: "traceable_sources.csv",
		text: "source_type,identifier,url\n" +
			"corporate_disclosure,phase-3-announcement," + url + "\n",
	}
	discoveryOnly := []agentruntime.Message{
		runnerEvidenceDepthCall("search", "web_search", map[string]any{"query": "oral GLP-1 phase 3"}),
		runnerEvidenceDepthResult("search", map[string]any{
			"sources": []any{map[string]any{"title": "Phase 3 announcement", "url": url}},
		}),
	}
	if failures := runnerEvidenceRecordDepthFailures(
		snapshot, runnerEvidenceRecordDepthIndexFromMessages(discoveryOnly),
	); len(failures) != 1 || !strings.Contains(failures[0], "source_type=web") {
		t.Fatalf("title-only generic source was not rejected: %#v", failures)
	}

	deepRead := append(discoveryOnly,
		runnerEvidenceDepthCall("research", "web_research", map[string]any{
			"operation": "search_and_fetch", "query": "oral GLP-1 phase 3",
		}),
		runnerEvidenceDepthResult("research", map[string]any{
			"documents": []any{map[string]any{
				"url": url, "content": strings.Repeat("Measured phase 3 efficacy and safety evidence. ", 40),
				"readReceipt": map[string]any{"deepRead": true, "responseComplete": true},
			}},
		}),
	)
	if failures := runnerEvidenceRecordDepthFailures(
		snapshot, runnerEvidenceRecordDepthIndexFromMessages(deepRead),
	); len(failures) != 0 {
		t.Fatalf("deep-read generic source was rejected: %#v", failures)
	}
}

func TestRunnerEvidenceRecordDepthRejectsADeepButOffTopicPatentRecord(t *testing.T) {
	messages := []agentruntime.Message{
		runnerEvidenceDepthCall("patent", "patent_search", map[string]any{
			"operation": "lookup", "publicationNumber": "CA2929630C",
			"focusTerms": []any{"ionic liquid", "oral peptide", "delivery"},
		}),
		runnerEvidenceDepthResult("patent", map[string]any{
			"evidenceDepth": "full_record",
			"records": []any{map[string]any{
				"publicationNumber": "CA2929630C", "recordDepth": "full_record",
				"focusRelevant": false, "matchedFocusTerms": []any{"ionic liquid", "delivery"},
			}},
		}),
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(messages)
	if index.contains("patent", "CA2929630C") {
		t.Fatalf("off-topic patent was accepted as screened evidence: %#v", index)
	}
}

func TestRunnerEvidenceRecordDepthAcceptsExactPatentReadFromSameDiscoveryChainAcrossLanguages(t *testing.T) {
	messages := []agentruntime.Message{
		runnerEvidenceDepthCall("patent-search", "patent_search", map[string]any{
			"operation": "search", "query": "口服小分子受体激动剂",
		}),
		runnerEvidenceDepthResult("patent-search", map[string]any{
			"evidenceDepth": "discovery_only",
			"records": []any{map[string]any{
				"publicationNumber": "US20230241233A1", "title": "Receptor agonist compounds",
			}},
		}),
		runnerEvidenceDepthCall("patent-lookup", "patent_search", map[string]any{
			"operation": "lookup", "publication_number": "US20230241233A1",
			"focus_terms": []any{"口服小分子", "受体激动剂"},
		}),
		runnerEvidenceDepthResult("patent-lookup", map[string]any{
			"evidenceDepth": "full_record",
			"records": []any{map[string]any{
				"publicationNumber": "US20230241233A1", "recordDepth": "full_record",
				"focusRelevant": false, "matchedFocusTerms": []any{},
			}},
		}),
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(messages)
	if !index.contains("patent", "US20230241233A1") {
		t.Fatalf("discovery-to-record identity continuity was lost across languages: %#v", index)
	}
}

func TestRunnerEvidenceRecordDepthCorrectionsUseOneProviderNeutralSourceRepairPath(t *testing.T) {
	for _, detail := range []string{
		"evidence_record_depth_missing:evidence.csv row=2 source_type=patent",
		"evidence_record_depth_missing:evidence.csv row=3 source_type=临床试验",
		"evidence_record_depth_missing:evidence.csv row=4 source_type=文献",
		"evidence_record_depth_missing:evidence.csv row=5 source_type=web",
	} {
		if !runnerCorrectionRequiresSourceLocatorRepair("artifact_reference_correction_required", detail) {
			t.Fatalf("record-depth correction was not classified as source repair: %q", detail)
		}
	}
}

func runnerEvidenceDepthCall(
	id string,
	name string,
	arguments map[string]any,
) agentruntime.Message {
	encoded, _ := json.Marshal(arguments)
	return agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: id, Name: name, Arguments: encoded, VerifiedEvidence: true,
	}}}
}

func runnerEvidenceDepthResult(id string, result map[string]any) agentruntime.Message {
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": result})
	return agentruntime.Message{Role: "tool", ToolCallID: id, Content: string(encoded)}
}

func TestRunnerEvidenceRecordDepthCanonicalization(t *testing.T) {
	got := []string{
		runnerEvidenceCanonicalPatent("https://patents.google.com/patent/US-20230093875-A1/en"),
		runnerEvidenceCanonicalPublication("https://doi.org/10.1002/ADHM.202500946"),
		runnerEvidenceCanonicalPublication("PMID: 12345678"),
	}
	want := []string{"US20230093875A1", "doi:10.1002/adhm.202500946", "pmid:12345678"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical identifiers=%#v want=%#v", got, want)
	}
}

func TestRunnerEvidenceRepairExtractsIdentifierFromDepthDiagnostic(t *testing.T) {
	for _, test := range []struct {
		detail, class, identifier string
	}{
		{"evidence_record_depth_missing:table.csv row=2 source_type=publication identifier=doi:10.1080/example", "publication", "doi:10.1080/example"},
		{"evidence_record_depth_missing:table.csv row=3 source_type=trial identifier=NCT01234567", "trial", "NCT01234567"},
		{"evidence_record_depth_missing:table.csv row=4 source_type=patent identifier=AU2021390194B2", "patent", "AU2021390194B2"},
	} {
		class, identifier := runnerEvidenceReferenceRequirement(test.detail)
		if class != test.class || identifier != test.identifier {
			t.Errorf("repair requirement for %q=(%q,%q), want (%q,%q)", test.detail, class, identifier, test.class, test.identifier)
		}
	}
}

func TestRunnerEvidenceRecordDepthVerifiesOnlyRowsActuallyDeclaredByTheArtifact(t *testing.T) {
	index := newRunnerEvidenceRecordDepthIndex()
	index.patents["US20230241233A1"] = struct{}{}
	snapshot := runnerCrossArtifactSnapshot{
		name: "evidence.csv",
		text: "source_type,identifier\npatent,US20230241233A1\nclinical trial,NCT04616014\n",
	}
	if got := runnerEvidenceRecordDepthVerifiedClasses(snapshot, index); !got["patent"] || got["trial"] {
		t.Fatalf("verified evidence classes=%#v", got)
	}
}

func TestRunnerEvidenceRecordDepthUnderstandsLocalizedIdentifierAndDOISourceType(t *testing.T) {
	index := newRunnerEvidenceRecordDepthIndex()
	index.publications["doi:10.2337/db25-750-p"] = struct{}{}
	index.patents["CN115698003B"] = struct{}{}
	snapshot := runnerCrossArtifactSnapshot{
		name: "traceable_sources.csv",
		text: "来源类型,标识符/编号,来源URL\n" +
			"DOI,10.2337/db25-750-p,https://doi.org/10.2337/db25-750-p\n" +
			"专利,CN115698003B,https://patentscope.wipo.int/search/en/result.jsf?query=CN115698003B\n",
	}
	if failures := runnerEvidenceRecordDepthFailures(snapshot, index); len(failures) != 0 {
		t.Fatalf("localized source ledger was not linked to exact records: %#v", failures)
	}
	verified := runnerEvidenceRecordDepthVerifiedClasses(snapshot, index)
	if !verified["publication"] || !verified["patent"] {
		t.Fatalf("localized source classes were not verified: %#v", verified)
	}
}

func TestRunnerEvidenceRecordDepthInfersGenericIdentifierColumn(t *testing.T) {
	index := newRunnerEvidenceRecordDepthIndex()
	index.publications["doi:10.1080/example"] = struct{}{}
	index.patents["AU2021390194B2"] = struct{}{}
	snapshot := runnerCrossArtifactSnapshot{
		name: "研发证据表.csv",
		text: "证据类别,来源,关键结论,标识\n" +
			"领域综述,Expert Opin Ther Pat,九个候选进入临床,doi:10.1080/example\n" +
			"临床管线,企业官网,候选处于一期,https://example.test/pipeline\n" +
			"专利,Google Patents,化合物权利要求,AU2021390194B2\n",
	}
	failures := runnerEvidenceRecordDepthFailures(snapshot, index)
	if len(failures) != 1 || !strings.Contains(failures[0], "source_type=web") ||
		!strings.Contains(failures[0], "row=3") {
		t.Fatalf("generic identifier depth failures=%#v", failures)
	}
	classes := runnerEvidenceRecordDepthClasses(snapshot, index)
	verified := runnerEvidenceRecordDepthVerifiedClasses(snapshot, index)
	if !classes["publication"] || !classes["patent"] || !classes["web"] ||
		!verified["publication"] || !verified["patent"] || verified["web"] {
		t.Fatalf("generic identifier classes=%#v verified=%#v", classes, verified)
	}
}

func TestRunnerEvidenceRecordDepthUnderstandsWideComparisonTableColumns(t *testing.T) {
	index := newRunnerEvidenceRecordDepthIndex()
	index.trials["NCT05869903"] = struct{}{}
	index.publications["doi:10.1038/s41591-021-01391-w"] = struct{}{}
	snapshot := runnerCrossArtifactSnapshot{
		name: "oral_nonpeptide_glp1_evidence_table.csv",
		text: "分子名称,核心专利号,临床试验编号,发表文献DOI,来源URL\n" +
			"Orforglipron,CA3160518C,NCT05869903;NCT06109311,,https://clinicaltrials.gov/study/NCT05869903\n" +
			"Danuglipron,CN117362283B,NCT03985293,10.1038/s41591-021-01391-w,https://www.nature.com/articles/s41591-021-01391-w\n",
	}
	classes := runnerEvidenceRecordDepthClasses(snapshot, index)
	if !classes["patent"] || !classes["trial"] || !classes["publication"] || classes["web"] {
		t.Fatalf("wide evidence classes=%#v", classes)
	}
	verified := runnerEvidenceRecordDepthVerifiedClasses(snapshot, index)
	if verified["patent"] || !verified["trial"] || !verified["publication"] || verified["web"] {
		t.Fatalf("wide verified evidence classes=%#v", verified)
	}
	failures := runnerEvidenceRecordDepthFailures(snapshot, index)
	foundMissingPatent := false
	for _, failure := range failures {
		if strings.Contains(failure, "source_type=patent") && strings.Contains(failure, "identifier=CA3160518C") {
			foundMissingPatent = true
		}
	}
	if !foundMissingPatent {
		t.Fatalf("wide evidence rows lost exact missing-record diagnostics: %#v", failures)
	}
	identifiers := runnerEvidenceRecordDepthClassIdentifiers(snapshot)
	if !reflect.DeepEqual(identifiers["trial"], []string{"NCT03985293", "NCT05869903", "NCT06109311"}) ||
		!reflect.DeepEqual(identifiers["patent"], []string{"CA3160518C", "CN117362283B"}) ||
		!reflect.DeepEqual(identifiers["publication"], []string{"doi:10.1038/s41591-021-01391-w"}) {
		t.Fatalf("wide representative identifiers=%#v", identifiers)
	}
}

func TestRunnerEvidenceRecordDepthWideTableAcceptsAnyVerifiedIdentifierInAMultiValueCell(t *testing.T) {
	index := newRunnerEvidenceRecordDepthIndex()
	index.trials["NCT06109311"] = struct{}{}
	snapshot := runnerCrossArtifactSnapshot{
		name: "candidate_evidence.csv",
		text: "candidate,clinical_trial_ids,source_url\n" +
			"candidate-a,NCT05869903; NCT06109311,https://clinicaltrials.gov/study/NCT06109311\n",
	}
	verified := runnerEvidenceRecordDepthVerifiedClasses(snapshot, index)
	if !verified["trial"] {
		t.Fatalf("multi-value trial cell was not linked to its verified record: %#v", verified)
	}
}

func TestRunnerEvidenceDepthRequirementKeepsRepresentativeIdentifier(t *testing.T) {
	detail := "runner completion reference integrity failed: " +
		"evidence_record_depth_required:trial source_type=trial identifier=NCT05869903 " +
		"reason=at_least_one_substantive_record_read"
	requirements := runnerEvidenceRecordRequirementsFromCorrection(detail)
	if len(requirements) != 1 || requirements[0].class != "trial" || requirements[0].identifier != "NCT05869903" {
		t.Fatalf("depth requirements=%#v", requirements)
	}
}

func TestRecordDepthCorrectionDoesNotAcceptAnUnrelatedRecordFromTheSameClass(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a trial-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed: " +
			"evidence_record_depth_required:evidence.csv source_type=trial identifier=NCT05869903 " +
			"reason=at_least_one_substantive_record_read",
	}
	run.addExecutedSkillNames("deep-literature-investigation")
	run.addTrustedScientificReviewSignals(trustedScientificEvidenceRecordSignalPrefix + "trial:NCT03538743")
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	tools := []agentruntime.ToolSchema{{Name: "repl"}, {Name: "edit_file"}, {Name: "save_artifacts"}}

	choice, _ := sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary}, tools).(map[string]any)
	if choice["name"] != "repl" {
		t.Fatalf("unrelated trial receipt bypassed exact record acquisition: %#v", choice)
	}

	run.addTrustedScientificReviewSignals(trustedScientificEvidenceRecordSignalPrefix + "trial:NCT05869903")
	choice, _ = sessionRunnerCorrectionRequiredToolChoice(run, []agentruntime.Message{boundary}, tools).(map[string]any)
	if choice["name"] != "edit_file" {
		t.Fatalf("exact trial receipt did not advance to artifact repair: %#v", choice)
	}
}

func TestRecordDepthCorrectionUsesDurableExactReceiptBeforeMutation(t *testing.T) {
	run := &sessionRunnerChatRun{
		TaskIntent:       "prepare a trial-backed evidence table",
		CorrectionReason: "artifact_reference_correction_required",
		CorrectionDetail: "runner completion reference integrity failed (unresolved_artifacts=0 malformed_artifact_references=0 " +
			"unsupported_citations=0 invalid_reference_artifacts=0 invalid_scientific_artifacts=0 cross_artifact_failures=1 " +
			"invalid_research_artifacts=0 missing_local_artifacts=0 missing_required_deliverables=0): " +
			"evidence_record_depth_required:evidence.csv source_type=trial identifier=NCT04991480 " +
			"reason=at_least_one_substantive_record_read",
	}
	// The source was already read and server-attested before the malformed save
	// opened this correction window. Requiring a second read here creates a
	// formatting/source loop and discards no new information.
	run.addTrustedScientificReviewSignals(trustedScientificEvidenceRecordSignalPrefix + "trial:NCT04991480")
	boundary := agentruntime.Message{Role: "system", Content: sessionRunnerDurableCorrectionContextMarker}
	editCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "edit-trial-ledger", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"evidence.csv"}`),
	}}}
	editResult := agentruntime.Message{Role: "tool", ToolCallID: "edit-trial-ledger", Content: `{"ok":true,"changed":true}`}
	saveCall := agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
		ID: "save-trial-ledger", Name: "save_artifacts",
		Arguments: json.RawMessage(`{"files":["evidence.csv"]}`),
	}}}
	saveResult := agentruntime.Message{Role: "tool", ToolCallID: "save-trial-ledger", Content: `{
		"ok":true,"artifacts":[{"filename":"evidence.csv","version_id":"version-2","unchanged":false}]}`}
	messages := []agentruntime.Message{boundary, editCall, editResult, saveCall, saveResult}

	if sessionRunnerCorrectionStillRequiresAction(run, messages) {
		t.Fatal("a durable exact source receipt was not reused before the artifact mutation")
	}
}

func TestRunnerEvidenceDepthValidationNamesAndClearsExactWideTableRecord(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(
		t, fixture.projectPath, "trial_evidence.csv",
		"candidate,clinical_trial_ids,source_url\n"+
			"candidate-a,NCT05869903,https://clinicaltrials.gov/study/NCT05869903\n",
	)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "trial-evidence-execution", 1, write)
	input := map[string]any{
		"files": []any{"trial_evidence.csv"}, "language": "text",
		"human_description": "Saving a trial evidence table",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-trial-evidence", input), fixture.identity, "save-trial-evidence", input,
	)
	artifacts := agentSaveArtifactResults(t, result)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("save result=%#v err=%v", result, err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: stringValue(artifacts[0]["artifact_id"]), VersionID: stringValue(artifacts[0]["version_id"]),
		Relation: transcriptstore.ArtifactRelationProduced,
	}}
	failures, err := fixture.server.validateSessionRunnerEvidenceRecordDepth(
		context.Background(), fixture.stream.ProjectID, commits, nil, nil,
		"review the clinical trial progress",
	)
	if err != nil || len(failures) == 0 {
		t.Fatalf("failures=%#v err=%v", failures, err)
	}
	foundTarget := false
	for _, failure := range failures {
		if strings.Contains(failure, "evidence_record_depth_required:trial") &&
			strings.Contains(failure, "identifier=NCT05869903") {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Fatalf("aggregate depth failure lost its exact record: %#v", failures)
	}

	evidence := []agentruntime.Message{
		runnerEvidenceDepthCall("trial-detail", "mcp__clinical-trials__get_trial_details", map[string]any{
			"nct_id": "NCT05869903",
		}),
		runnerEvidenceDepthResult("trial-detail", map[string]any{
			"found": true, "trial": map[string]any{
				"nct_id": "NCT05869903", "brief_title": "Candidate trial",
				"primary_outcomes": []any{map[string]any{"measure": "Body weight"}},
			},
		}),
	}
	failures, err = fixture.server.validateSessionRunnerEvidenceRecordDepth(
		context.Background(), fixture.stream.ProjectID, commits, evidence, nil,
		"review the clinical trial progress",
	)
	if err != nil || len(failures) != 0 {
		t.Fatalf("exact trial read did not clear the aggregate gap: %#v err=%v", failures, err)
	}
}

func TestRunnerEvidenceDepthValidationUsesTrustedExactReceiptAfterReplay(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(
		t, fixture.projectPath, "trial_evidence.csv",
		"candidate,clinical_trial_ids,source_url\n"+
			"candidate-a,NCT05869903,https://clinicaltrials.gov/study/NCT05869903\n",
	)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "trial-evidence-replayed", 1, write)
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-trial-evidence-replayed", map[string]any{
			"files": []any{"trial_evidence.csv"}, "language": "text",
			"human_description": "Save replayed trial evidence",
		}), fixture.identity, "save-trial-evidence-replayed", map[string]any{
			"files": []any{"trial_evidence.csv"}, "language": "text",
			"human_description": "Save replayed trial evidence",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := agentSaveArtifactResults(t, result)
	if len(artifacts) != 1 {
		t.Fatalf("saved artifacts=%#v", artifacts)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: stringValue(artifacts[0]["artifact_id"]),
		VersionID:  stringValue(artifacts[0]["version_id"]),
		Relation:   transcriptstore.ArtifactRelationProduced,
	}}
	trusted := []string{trustedScientificEvidenceRecordSignalPrefix + "trial:NCT05869903"}
	failures, err := fixture.server.validateSessionRunnerEvidenceRecordDepth(
		context.Background(), fixture.stream.ProjectID, commits, nil, trusted,
		"review the clinical trial progress",
	)
	if err != nil || len(failures) != 0 {
		t.Fatalf("trusted replay receipt did not clear the exact trial gap: failures=%#v err=%v", failures, err)
	}
}

func TestRunnerEvidenceDepthDoesNotInventLedgerFromTraceabilityLanguage(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	write := writeAgentSaveArtifactsFile(
		t, fixture.projectPath, "source_table.md",
		"# Sources\n\n- Publication DOI:10.1000/example\n- Trial NCT00000001\n",
	)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "markdown-sources", 1, write)
	input := map[string]any{
		"files": []any{"source_table.md"}, "language": "text",
		"human_description": "Saving a traceable source table",
	}
	result, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-markdown-sources", input), fixture.identity,
		"save-markdown-sources", input,
	)
	artifacts := agentSaveArtifactResults(t, result)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("save result=%#v err=%v", result, err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: stringValue(artifacts[0]["artifact_id"]),
		VersionID:  stringValue(artifacts[0]["version_id"]),
		Relation:   transcriptstore.ArtifactRelationProduced,
	}}
	failures, err := fixture.server.validateSessionRunnerEvidenceRecordDepth(
		context.Background(), fixture.stream.ProjectID, commits, nil, nil,
		"整合公开文献、临床试验与专利信息，形成带可追溯来源表的专业报告。",
	)
	if err != nil || len(failures) != 0 {
		t.Fatalf("traceability language invented evidence-ledger gates: %#v err=%v", failures, err)
	}
}

func TestToolRoundNoProgressBudgetResetsAtFreshAcceptanceFinding(t *testing.T) {
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": sessionRunnerToolRoundNoProgressReasonCode}},
		{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": "artifact_reference_correction_required"}},
		{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": sessionRunnerToolRoundNoProgressReasonCode}},
		{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": sessionRunnerToolRoundNoProgressReasonCode}},
	}
	if got := runnerToolRoundNoProgressInterruptionCount(entries); got != 2 {
		t.Fatalf("current repair-scope no-progress count=%d want=2", got)
	}
}

func TestToolRoundNoProgressBudgetSurvivesRepeatedFindingAndControlOnlyChurn(t *testing.T) {
	detail := "runner completion reference integrity failed (missing_required_deliverables=1)"
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "reason_code": "artifact_reference_correction_required", "resume_detail": detail,
		}},
		{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": sessionRunnerToolRoundNoProgressReasonCode}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": updateStepStatusToolName,
			"toolResult": map[string]any{
				"ok": true, "effect": agentruntime.ToolEffectValue(agentruntime.ToolEffectChanged, "control-state", "plan-progress"),
			},
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "reason_code": "artifact_reference_correction_required", "resume_detail": detail,
		}},
		{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": sessionRunnerToolRoundNoProgressReasonCode}},
	}
	if got := runnerToolRoundNoProgressInterruptionCount(entries); got != 2 {
		t.Fatalf("same unresolved obligation was reset by control-only churn: count=%d want=2", got)
	}

	entries = append(entries, eventjournal.Entry{Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": "web_fetch",
		"toolResult": map[string]any{"ok": true, "body": "new source evidence"},
	}})
	if got := runnerToolRoundNoProgressInterruptionCount(entries); got != 0 {
		t.Fatalf("material evidence did not reset no-progress state: count=%d", got)
	}
}

func TestRunnerEvidenceDepthAcceptsSubstantivePatentPageFetchedOverWeb(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "patent-page", Name: "web_fetch", VerifiedEvidence: true,
		Arguments: json.RawMessage(`{"url":"https://patents.google.com/patent/US10155740B2/en"}`),
	}
	body := strings.Repeat("abstract method result conclusion claims description ", 30)
	result := map[string]any{
		"statusCode": 200,
		"url":        "https://patents.google.com/patent/US10155740B2/en",
		"body":       body,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	index := runnerEvidenceRecordDepthIndexFromMessages([]agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: string(encoded)},
	})
	if !index.contains("patent", "US10155740B2") {
		t.Fatalf("patent page was not recorded as full evidence: %#v", index)
	}
}

func TestResearchSourceDepthAcceptsProviderCompleteAbstractButNotDiscoverySnippet(t *testing.T) {
	evidence := []agentruntime.Message{
		runnerEvidenceDepthCall("search-record", "web_search", map[string]any{"query": "target evidence"}),
		runnerEvidenceDepthResult("search-record", map[string]any{"sources": []any{
			map[string]any{
				"kind": "search_result", "evidenceState": "discovered", "url": "https://doi.org/10.1000/abstract",
				"title": "Complete abstract record", "snippet": "Short discovery text",
				"record": map[string]any{
					"record_depth": "abstract_record", "abstract_complete": true,
					"abstract":    "A complete provider abstract with methods, results, and conclusions.",
					"identifiers": []any{map[string]any{"namespace": "doi", "value": "10.1000/abstract"}},
				},
			},
			map[string]any{
				"kind": "search_result", "evidenceState": "discovered", "url": "https://doi.org/10.1000/snippet",
				"title": "Snippet only", "snippet": "A result snippet without a provider record.",
			},
		}}),
	}
	index := runnerEvidenceRecordDepthIndexFromMessages(evidence)
	if !index.contains("publication", "doi:10.1000/abstract") {
		t.Fatalf("complete provider abstract was not retained as a publication record: %#v", index)
	}
	if index.contains("publication", "doi:10.1000/snippet") {
		t.Fatalf("discovery snippet was promoted to a publication record: %#v", index)
	}
}
