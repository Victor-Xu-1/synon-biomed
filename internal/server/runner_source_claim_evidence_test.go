package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/persistence/workspace"
)

func TestValidateRunnerSourceEvidenceDocumentRequiresAttestedClaimExcerpts(t *testing.T) {
	const excerpt = "This convergent route was accomplished in 70% overall yield over 7 steps in 3 pots."
	corpus := normalizeRunnerSourceEvidenceText("tool metadata DOI 10.1038/example\n" + excerpt + "\nend")
	tests := map[string]struct {
		identifier string
		claims     []any
		want       string
	}{
		"producer label only": {
			identifier: "10.1038/example",
			claims:     []any{"route_overall_yield"},
			want:       "source_claim_missing_attested_excerpt",
		},
		"faithful paraphrase may summarize a read source": {
			identifier: "10.1038/example",
			claims: []any{map[string]any{
				"claim_id": "route_overall_yield", "source_locator": "Results", "evidence_excerpt": "A convergent seven-step route delivered the target in a strong overall yield.",
			}},
		},
		"unread source cannot support a summary": {
			identifier: "10.1038/unread",
			claims: []any{map[string]any{
				"claim_id": "route_overall_yield", "source_locator": "Conclusions", "evidence_excerpt": excerpt,
			}},
			want: "source_claim_source_not_in_durable_receipts",
		},
		"direct quote must be durably attested": {
			identifier: "10.1038/example",
			claims: []any{map[string]any{
				"claim_id": "route_overall_yield", "source_locator": "Conclusions", "evidence_excerpt": excerpt,
				"direct_quote": "This exact quotation was never returned by the source tool.",
			}},
			want: "source_claim_direct_quote_not_in_durable_receipts",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			document, err := json.Marshal(map[string]any{"sources": []any{map[string]any{
				"id": "source-1", "identifier": test.identifier, "claims_supported": test.claims,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			failures, err := scanRunnerSourceEvidenceDocument(context.Background(), strings.NewReader(string(document)), "source_evidence.json", corpus, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.want == "" {
				if len(failures) != 0 {
					t.Fatalf("attested source claim rejected: %#v", failures)
				}
				return
			}
			if len(failures) != 1 || !strings.Contains(failures[0], test.want) {
				t.Fatalf("failures=%#v want=%q", failures, test.want)
			}
		})
	}
}

func TestValidateRunnerSourceEvidenceLedgerRequiresReadExcerptsForIncludedRows(t *testing.T) {
	const excerpt = "The primary endpoint was the percent change in LDL cholesterol from baseline to week twelve."
	corpus := normalizeRunnerSourceEvidenceText("registry result NCT01234567\n" + excerpt + "\nlimitations")
	tests := map[string]struct {
		ledger string
		want   string
	}{
		"durably read included row": {
			ledger: "identifier,source_url,disposition,evidence_excerpt\nNCT01234567,https://clinicaltrials.gov/study/NCT01234567,included,\"" + excerpt + "\"\n",
		},
		"faithful paraphrase may summarize a read record": {
			ledger: "identifier,source_url,disposition,evidence_excerpt\nNCT01234567,https://clinicaltrials.gov/study/NCT01234567,included,\"The trial reported a large LDL cholesterol reduction after twelve weeks.\"\n",
		},
		"concise localized summary is judged by its source receipt rather than length": {
			ledger: "identifier,source_url,disposition,evidence_excerpt\nNCT01234567,https://clinicaltrials.gov/study/NCT01234567,included,\"验证主要终点改善\"\n",
		},
		"unread source cannot support a summary": {
			ledger: "identifier,source_url,disposition,evidence_excerpt\nNCT99999999,https://clinicaltrials.gov/study/NCT99999999,included,\"The trial reported a large LDL cholesterol reduction after twelve weeks.\"\n",
			want:   "source_ledger_source_not_in_durable_receipts",
		},
		"direct quote must match durable content": {
			ledger: "identifier,source_url,disposition,evidence_excerpt,direct_quote\nNCT01234567,https://clinicaltrials.gov/study/NCT01234567,included,\"The trial reported a large LDL cholesterol reduction after twelve weeks.\",\"A quote that is absent from the record\"\n",
			want:   "source_ledger_direct_quote_not_in_durable_receipts",
		},
		"included row needs a locator": {
			ledger: "identifier,source_url,disposition,evidence_excerpt\nNCT01234567,,included,\"" + excerpt + "\"\n",
			want:   "source_ledger_missing_attested_excerpt",
		},
		"included row needs a summary": {
			ledger: "identifier,source_url,disposition,evidence_excerpt\nNCT01234567,https://clinicaltrials.gov/study/NCT01234567,included,\"\"\n",
			want:   "source_ledger_missing_attested_excerpt",
		},
		"excluded candidate is audit data": {
			ledger: "identifier,source_url,disposition,evidence_excerpt\nNCT01234567,https://clinicaltrials.gov/study/NCT01234567,candidate,\"unread search snippet\"\n",
		},
		"bibliography does not claim a read excerpt": {
			ledger: "identifier,source_url,title\nNCT01234567,https://clinicaltrials.gov/study/NCT01234567,Trial title\n",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			failures := validateRunnerSourceEvidenceLedger("sources.csv", []byte(test.ledger), corpus)
			if test.want == "" {
				if len(failures) != 0 {
					t.Fatalf("valid source ledger rejected: %#v", failures)
				}
				return
			}
			if len(failures) != 1 || !strings.Contains(failures[0], test.want) {
				t.Fatalf("failures=%#v want=%q", failures, test.want)
			}
		})
	}
}

func TestValidateRunnerSourceEvidenceLedgerUsesLocalizedKeyConclusion(t *testing.T) {
	ledger := "证据类别,来源,关键结论,标识\n" +
		"领域综述,Expert Opin Ther Pat,该综述系统比较了多个临床候选化合物的机制开发阶段和专利布局特征,doi:10.1080/example\n"
	corpus := normalizeRunnerSourceEvidenceText("full publication record doi:10.1080/example")
	if failures := validateRunnerSourceEvidenceLedger("研发证据表.csv", []byte(ledger), corpus); len(failures) != 0 {
		t.Fatalf("localized key conclusion was not treated as an attested summary: %#v", failures)
	}
}

func TestValidateRunnerSourceEvidenceLedgerRecognizesLocalizedLinkColumn(t *testing.T) {
	const sourceURL = "https://doi.org/10.1000/example"
	ledger := "证据来源,关键结论,链接\n" +
		"Nature,验证了目标机制并报告了可复核的实验结果," + sourceURL + "\n"
	corpus := normalizeRunnerSourceEvidenceText("full publication record " + sourceURL)
	if failures := validateRunnerSourceEvidenceLedger("研发证据表.csv", []byte(ledger), corpus); len(failures) != 0 {
		t.Fatalf("localized link column was not accepted as a source locator: %#v", failures)
	}
}

func TestRunnerSourceEvidenceRejectsInternalToolArtifactHandles(t *testing.T) {
	const handle = "large-tool-result-fcf82d557b7cbcab494ae2d077fdede2"
	evidence := []agentruntime.Message{{Role: "tool", Content: `{
		"artifact_id":"` + handle + `","version_id":"ltr-12345678-1234-1234-1234-123456789abc"
	}`}}
	handles := runnerInternalArtifactHandles(evidence)
	ledger := "来源,关键结论,URL,内部引用\n" +
		"Nature,该研究完整报告了实验方法观察结果和主要结论,https://doi.org/10.1000/example," + handle + "\n"
	corpus := "https://doi.org/10.1000/example"
	failures, err := scanRunnerSourceEvidenceLedger(context.Background(), strings.NewReader(ledger), "证据表.csv", corpus, handles, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0], "machine_validation_internal_runtime_reference") {
		t.Fatalf("internal tool handle was not rejected from the user artifact: %#v", failures)
	}
	if failures, err := scanRunnerSourceEvidenceLedger(context.Background(), strings.NewReader(strings.ReplaceAll(ledger, handle, "https://doi.org/10.1000/example")), "证据表.csv", corpus, handles, true); err != nil || len(failures) != 0 {
		t.Fatalf("portable source locator was rejected: %#v", failures)
	}
}

func TestSessionRunnerSourceClaimEvidenceReadsLatestProducedLedgerAndToolReceipts(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	const excerpt = "The commercial synthesis is a five-step sequence that supplied hundreds of metric tons."
	payload, err := json.Marshal(map[string]any{"sources": []any{map[string]any{
		"id": "source-1", "identifier": "10.1000/commercial-route", "claims_supported": []any{map[string]any{
			"claim_id": "commercial_step_count", "source_locator": "Conclusion", "evidence_excerpt": excerpt,
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-source-evidence", ProjectID: "project-a", Name: "source_evidence.json",
		Kind: "application/json", Content: payload, CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	server := &Server{workspaceStore: store}
	validEvidence := []agentruntime.Message{{Role: "tool", Content: "source body DOI 10.1000/commercial-route: " + excerpt}}
	if failures, err := server.validateSessionRunnerSourceClaimEvidence(context.Background(), "project-a", commits, validEvidence); err != nil || len(failures) != 0 {
		t.Fatalf("valid source evidence failures=%#v err=%v", failures, err)
	}
	if failures, err := server.validateSessionRunnerSourceClaimEvidence(context.Background(), "project-a", commits, nil); err != nil || len(failures) != 1 || !strings.Contains(failures[0], "not_in_durable_receipts") {
		t.Fatalf("unattested source evidence failures=%#v err=%v", failures, err)
	}
}

func TestSessionRunnerSourceClaimEvidenceBlocksInternalHandleFromPublishedLedger(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	const handle = "large-tool-result-fcf82d557b7cbcab494ae2d077fdede2"
	ledger := "来源,关键结论,URL,内部引用\n" +
		"Nature,该研究完整报告了实验方法观察结果和主要结论以及可复核数据,https://doi.org/10.1000/example," + handle + "\n"
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-ledger", ProjectID: "project-a", Name: "研发证据表.csv",
		Kind: "text/csv", Content: []byte(ledger), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	durable := []agentruntime.Message{{Role: "tool", Content: `{
		"artifact_id":"` + handle + `","url":"https://doi.org/10.1000/example",
		"body":"该研究完整报告了实验方法观察结果和主要结论以及可复核数据"
	}`}}
	failures, err := (&Server{workspaceStore: store}).validateSessionRunnerSourceClaimEvidence(
		context.Background(), "project-a", commits, durable,
	)
	if err != nil || len(failures) != 1 || !strings.Contains(failures[0], "machine_validation_internal_runtime_reference") {
		t.Fatalf("internal handle publication failures=%#v err=%v", failures, err)
	}
}
