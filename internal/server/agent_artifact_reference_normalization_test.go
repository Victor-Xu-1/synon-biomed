package server

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNormalizeAgentSavedArtifactReferenceTextUsesPortableCompanionPaths(t *testing.T) {
	artifactID := "123e4567-e89b-12d3-a456-426614174000"
	versionID := "123e4567-e89b-12d3-a456-426614174999"
	content := strings.Join([]string{
		"![legacy artifact]({{artifact:art_" + artifactID + "}})",
		"![legacy version]({{artifact:art_" + versionID + "}})",
		"![canonical version]({{artifact:" + versionID + "}})",
	}, "\n")
	normalized, changed, err := normalizeAgentSavedArtifactReferenceText(
		content, "reports/report.md", "project-a",
		func(id string) (agentSavedArtifactReferenceResolution, bool, error) {
			if id != artifactID && id != versionID {
				return agentSavedArtifactReferenceResolution{}, false, errors.New("unexpected id")
			}
			return agentSavedArtifactReferenceResolution{
				projectID: "project-a", relativePath: "plots/plot.png",
			}, true, nil
		},
	)
	if err != nil || !changed {
		t.Fatalf("normalized=%q changed=%t err=%v", normalized, changed, err)
	}
	if strings.Count(normalized, "../plots/plot.png") != 3 {
		t.Fatalf("normalized=%q", normalized)
	}
	if strings.Contains(normalized, "{{artifact:") {
		t.Fatalf("durable companion references were not portable: %q", normalized)
	}
}

func TestAgentSaveArtifactsNormalizesArtifactAndVersionAliasesAtPublication(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	dataWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "tables/data.csv", "name,value\nA,1\n")
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-reference-data", 1, dataWrite)
	dataInput := map[string]any{
		"files": []any{"tables/data.csv"}, "language": "python", "environment": "scanpy",
		"human_description": "Saving reference data",
	}
	dataResult, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-reference-data", dataInput), fixture.identity, "save-reference-data", dataInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	dataArtifacts := agentSaveArtifactResults(t, dataResult)
	if len(dataArtifacts) != 1 {
		t.Fatalf("data artifacts=%#v", dataArtifacts)
	}
	artifactID := stringValue(dataArtifacts[0]["artifact_id"])
	versionID := stringValue(dataArtifacts[0]["version_id"])

	reportContent := strings.Join([]string{
		"# Report",
		"[artifact alias]({{artifact:art_" + artifactID + "}})",
		"[version alias]({{artifact:art_" + versionID + "}})",
		"[canonical version]({{artifact:" + versionID + "}})",
	}, "\n")
	reportWrite := writeAgentSaveArtifactsFile(t, fixture.projectPath, "reports/report.md", reportContent)
	fixture.saveExecution(t, fixture.identity.access, fixture.projectPath, "exec-reference-report", 2, reportWrite)
	reportInput := map[string]any{
		"files": []any{"reports/report.md"}, "language": "text",
		"human_description": "Saving portable report",
	}
	reportResult, err := fixture.server.executeAgentSaveArtifacts(
		fixture.toolContext(t, "save-reference-report", reportInput), fixture.identity, "save-reference-report", reportInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	reportArtifacts := agentSaveArtifactResults(t, reportResult)
	if len(reportArtifacts) != 1 {
		t.Fatalf("report artifacts=%#v", reportArtifacts)
	}
	_, _, reader, found, err := fixture.store.OpenArtifactVersionContent(stringValue(reportArtifacts[0]["version_id"]))
	if err != nil || !found {
		t.Fatalf("open report found=%t err=%v", found, err)
	}
	defer reader.Close()
	published, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(published), "../tables/data.csv") != 3 || strings.Contains(string(published), "{{artifact:") {
		t.Fatalf("published report=%q", published)
	}
}

func TestNormalizeAgentSavedArtifactReferenceTextFailsClosed(t *testing.T) {
	marker := "{{artifact:123e4567-e89b-12d3-a456-426614174000}}"
	for name, resolution := range map[string]agentSavedArtifactReferenceResolution{
		"foreign": {projectID: "project-b", relativePath: "plot.png"},
		"self":    {projectID: "project-a", relativePath: "reports/report.md"},
	} {
		t.Run(name, func(t *testing.T) {
			normalized, changed, err := normalizeAgentSavedArtifactReferenceText(
				marker, "reports/report.md", "project-a",
				func(string) (agentSavedArtifactReferenceResolution, bool, error) {
					return resolution, true, nil
				},
			)
			if err != nil || changed || normalized != marker {
				t.Fatalf("normalized=%q changed=%t err=%v", normalized, changed, err)
			}
		})
	}
}
