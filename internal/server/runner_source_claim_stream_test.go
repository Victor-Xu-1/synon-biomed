package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestSourceClaimEvidenceLargeOrdinaryTableIsNotLedger(t *testing.T) {
	content := "a,b\n" + strings.Repeat("1,2\n", (8<<20)/4+1)
	failures := sourceClaimStreamFixture(t, "results.csv", content, nil)
	if len(failures) != 0 {
		t.Fatalf("ordinary data was incorrectly treated as a bounded evidence ledger: %v", failures)
	}
}

func TestSourceClaimEvidenceStreamsLargeLedgerTail(t *testing.T) {
	const locator = "https://example.test/source"
	row := "source," + locator + ",included," + strings.Repeat("e", 2048) + "\n"
	content := "identifier,source_url,disposition,evidence_excerpt\n" + strings.Repeat(row, 4200) + "tail,,included,summary\n"
	failures := sourceClaimStreamFixture(t, "ledger.csv", content, []agentruntime.Message{{Role: "tool", Content: locator}})
	if len(failures) != 1 || failures[0] != "source_ledger_missing_attested_excerpt:ledger.csv row=4202 source=tail" {
		t.Fatalf("large ledger tail was not checked with its original row number: %v", failures)
	}
}

func TestSourceClaimEvidenceStreamsLargeDocumentTail(t *testing.T) {
	const locator = "https://example.test/source"
	source := map[string]any{
		"id": "source", "identifier": locator,
		"claims_supported": []any{map[string]any{"claim_id": "claim", "source_locator": locator, "evidence_excerpt": strings.Repeat("e", 5000)}},
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	content := `{"sources":[` + strings.Repeat(string(encoded)+",", 1800) + `{"id":"tail","identifier":"https://example.test/source","claims_supported":[{"claim_id":"last","source_locator":"","evidence_excerpt":"summary"}]}]}`
	failures := sourceClaimStreamFixture(t, "source_evidence.json", content, []agentruntime.Message{{Role: "tool", Content: locator}})
	if len(failures) != 1 || failures[0] != "source_claim_missing_attested_excerpt:source_evidence.json source=tail claim=last" {
		t.Fatalf("large JSON source tail was not checked: %v", failures)
	}
}

func sourceClaimStreamFixture(t *testing.T, name, content string, messages []agentruntime.Message) []string {
	t.Helper()
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "stream-evidence", ProjectID: "project-a", Name: name, Kind: "text/plain", Content: []byte(content), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced}}
	failures, err := (&Server{workspaceStore: store}).validateSessionRunnerSourceClaimEvidence(context.Background(), "project-a", commits, messages)
	if err != nil {
		t.Fatal(err)
	}
	return failures
}

func TestSourceClaimEvidenceStreamingJSONContractEdges(t *testing.T) {
	const handle = "private-artifact-handle-123456789"
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"null", "null", nil},
		{"empty sources", `{"sources":[]}`, nil},
		{"root metadata", `{"metadata":{"values":["` + handle + `"]},"sources":[]}`, []string{"machine_validation_internal_runtime_reference:source_evidence.json"}},
		{"key is not a value", `{"metadata":{"` + handle + `":"safe"},"sources":[]}`, nil},
		{"incomplete tail", `{"sources":[{}`, []string{"source_evidence_invalid_json:source_evidence.json"}},
		{"trailing value", `{"sources":[]} {}`, []string{"source_evidence_invalid_json:source_evidence.json"}},
		{"wrong root", `[]`, []string{"source_evidence_invalid_json:source_evidence.json"}},
		{"last field semantics", `{"sources":[{"claims_supported":["invalid"]}],"sources":[]}`, nil},
		{"source index retained", `{"sources":[{}, {}, {"claims_supported":["missing"]}]}`, []string{"source_claim_missing_attested_excerpt:source_evidence.json source=index-2 claim=missing"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanRunnerSourceEvidenceDocument(context.Background(), strings.NewReader(tc.text), "source_evidence.json", "", map[string]struct{}{handle: {}})
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("got=%v want=%v err=%v", got, tc.want, err)
			}
		})
	}
}

func TestSourceClaimEvidenceStreamingDelimitedBoundaries(t *testing.T) {
	const locator = "https://example.test/source"
	text := "\ufeffidentifier\tsource_url\tevidence_excerpt\nsource\t" + locator + "\t\"multiline\nsummary\"\ntail\t\tsummary\n"
	got, err := scanRunnerSourceEvidenceLedger(context.Background(), strings.NewReader(text), "ledger.tsv", locator, nil, true)
	if err != nil || !slices.Equal(got, []string{"source_ledger_missing_attested_excerpt:ledger.tsv row=3 source=tail"}) {
		t.Fatalf("logical row count/BOM/TSV/multiline=%v err=%v", got, err)
	}
	got, err = scanRunnerSourceEvidenceLedger(context.Background(), strings.NewReader("identifier,source_url,evidence_excerpt\nsource,\"unterminated"), "ledger.csv", locator, nil, true)
	if err != nil || !slices.Equal(got, []string{"source_evidence_invalid_delimited:ledger.csv"}) {
		t.Fatalf("malformed tail=%v err=%v", got, err)
	}
}

type sourceEvidenceFailReader struct{ err error }

func (reader sourceEvidenceFailReader) Read([]byte) (int, error) { return 0, reader.err }

func TestSourceClaimEvidenceStreamingPreservesIOAndCancellation(t *testing.T) {
	sentinel := errors.New("controlled read failure")
	for _, jsonSource := range []bool{false, true} {
		scan := func(ctx context.Context, reader io.Reader) ([]string, error) {
			if jsonSource {
				return scanRunnerSourceEvidenceDocument(ctx, reader, "source_evidence.json", "", nil)
			}
			return scanRunnerSourceEvidenceLedger(ctx, reader, "ledger.csv", "", nil, true)
		}
		if _, err := scan(context.Background(), sourceEvidenceFailReader{sentinel}); !errors.Is(err, sentinel) {
			t.Fatalf("JSON=%t lost read error: %v", jsonSource, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := scan(ctx, strings.NewReader("{}")); !errors.Is(err, context.Canceled) {
			t.Fatalf("JSON=%t lost cancellation: %v", jsonSource, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows := 0
	err := visitRunnerEvidenceRows(ctx, strings.NewReader("id\nfirst\nsecond\n"), "table.csv", nil, func(int, map[string]int, []string) error { rows++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || rows != 1 {
		t.Fatalf("scan after cancellation: rows=%d err=%v", rows, err)
	}
}
