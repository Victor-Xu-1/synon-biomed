package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	toolregistry "synon-go/internal/tools/registry"
)

type artifactReferenceRepairModel struct {
	versionID        string
	content          string
	calls            int
	alwaysUnresolved bool
	testing          *testing.T
}

const unresolvedArtifactVersionID = "00000000-0000-4000-8000-000000000001"

type citationReferenceRepairModel struct {
	calls   int
	testing *testing.T
}

type toolBudgetRepairModel struct {
	calls atomic.Int64
}

type scientificArtifactRepairModel struct {
	badVersionID  string
	goodVersionID string
	calls         int
	testing       *testing.T
}

type durableEvidenceCitationModel struct {
	calls int
}

type referenceEvidenceLedgerRepairModel struct {
	calls   int
	testing *testing.T
}

func TestSessionRunnerMissingLocalArtifactDependenciesRequiresProducedCommit(t *testing.T) {
	root := t.TempDir()
	dependencyPath := filepath.Join(root, "carbonate", "equilibrium.dat")
	if err := os.MkdirAll(filepath.Dir(dependencyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependencyPath, []byte("SOLUTION_MASTER_SPECIES\nC C(+4) 2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootDependencyPath := filepath.Join(root, "phreeqc.dat")
	if err := os.WriteFile(rootDependencyPath, []byte("PHASES\nCalcite\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const streamUID = "stream-local-dependency"
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: transcriptstore.Stream{UID: streamUID}}}
	server := &Server{fileRoot: root}
	finalContent := "Reproduce with `carbonate/equilibrium.dat` and `phreeqc.dat`; public source `https://example.org/equilibrium.dat` is not local."

	missing, err := server.sessionRunnerMissingLocalArtifactDependencies("", run, nil, finalContent)
	if err != nil || !reflect.DeepEqual(missing, []string{"carbonate/equilibrium.dat", "phreeqc.dat"}) {
		t.Fatalf("missing=%#v err=%v", missing, err)
	}

	commits := []transcriptstore.ArtifactReferenceInput{
		{
			ArtifactID: streamArtifactIDFor(streamUID, "carbonate/equilibrium.dat"),
			VersionID:  "version-equilibrium",
			Relation:   transcriptstore.ArtifactRelationProduced,
		},
		{
			ArtifactID: streamArtifactIDFor(streamUID, "phreeqc.dat"),
			VersionID:  "version-root-database",
			Relation:   transcriptstore.ArtifactRelationProduced,
		},
	}
	missing, err = server.sessionRunnerMissingLocalArtifactDependencies("", run, commits, finalContent)
	if err != nil || len(missing) != 0 {
		t.Fatalf("produced dependency missing=%#v err=%v", missing, err)
	}
}

func TestSessionRunnerMissingLocalArtifactDependenciesInspectsProducedScripts(t *testing.T) {
	root := t.TempDir()
	for relativePath, content := range map[string]string{
		"runtime/database/model.dat": "MODEL PARAMETERS\n",
		"runtime/lib/solver.so":      "native library placeholder\n",
		"outputs/results.json":       "{}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const streamUID = "stream-script-dependency"
	store := openRunnerArtifactCompletionStore(t)
	scriptArtifactID := streamArtifactIDFor(streamUID, "scripts/run.py")
	_, scriptVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: scriptArtifactID, ProjectID: "project-a", Name: "run.py", Kind: "text/x-python",
		Content: []byte(`import os, json
LIB = os.path.abspath("runtime/lib/solver.so")
DB = os.path.abspath("runtime/database/model.dat")
with open("outputs/results.json", "w") as handle:
    json.dump({"ok": True}, handle)
`), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: transcriptstore.Stream{UID: streamUID}}}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: scriptArtifactID, VersionID: scriptVersion.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	server := &Server{fileRoot: root, workspaceStore: store}

	missing, err := server.sessionRunnerMissingLocalArtifactDependencies("project-a", run, commits, "Analysis complete.")
	want := []string{"runtime/database/model.dat", "runtime/lib/solver.so"}
	if err != nil || !reflect.DeepEqual(missing, want) {
		t.Fatalf("script dependency missing=%#v want=%#v err=%v", missing, want, err)
	}

	for _, relativePath := range want {
		artifactID := streamArtifactIDFor(streamUID, relativePath)
		_, version, saveErr := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: "project-a", Name: filepath.Base(relativePath), Kind: "binary",
			Content: []byte("immutable dependency\n"), CreatedBy: "runner",
		})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		commits = append(commits, transcriptstore.ArtifactReferenceInput{
			ArtifactID: artifactID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
		})
	}
	missing, err = server.sessionRunnerMissingLocalArtifactDependencies("project-a", run, commits, "Analysis complete.")
	if err != nil || len(missing) != 0 {
		t.Fatalf("closed script dependency missing=%#v err=%v", missing, err)
	}
}

func (model *referenceEvidenceLedgerRepairModel) Complete(
	_ context.Context,
	request agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	model.calls++
	if model.calls == 1 {
		return agentruntime.ModelResponse{Message: agentruntime.Message{
			Role: "assistant", Content: "Unverified DOI: 10.9999/invented",
		}}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	for _, required := range []string{
		`"schema":"synon.reference_evidence_ledger.v1"`,
		`"tool_call_id":"prior-crossref-real"`,
		`"value":"10.1038/s41586-019-1295-z"`,
		"Never invent or rename a tool_call_id",
	} {
		if last.Role != "system" || !strings.Contains(last.Content, required) {
			model.testing.Fatalf("reference evidence correction prompt missing %q: %#v", required, last)
		}
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", Content: "The unsupported citation was removed.",
	}}, nil
}

func (model *durableEvidenceCitationModel) Complete(
	_ context.Context,
	_ agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	model.calls++
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", Content: "Verified structure PDB: 6S73.",
	}}, nil
}

func (model *toolBudgetRepairModel) Complete(_ context.Context, _ agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	switch model.calls.Add(1) {
	case 1, 3:
		return agentruntime.ModelResponse{Message: agentruntime.Message{
			Role: "assistant",
			ToolCalls: []agentruntime.ToolCall{{
				ID: "evidence-call", Name: "mcp__pubmed__search", Arguments: json.RawMessage(`{"query":"KRAS"}`),
			}},
		}}, nil
	case 2:
		return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "[report]({{artifact:" + unresolvedArtifactVersionID + "}})"}}, nil
	default:
		return agentruntime.ModelResponse{}, errors.New("unexpected model call")
	}
}

func (model *citationReferenceRepairModel) Complete(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	model.calls++
	if model.calls > 1 {
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "system" || !strings.Contains(last.Content, "citation identifiers") ||
			!strings.Contains(last.Content, "doi:10.9999/invented") ||
			!strings.Contains(last.Content, "Do not quote, list, or discuss an unsupported identifier") {
			model.testing.Fatalf("citation correction prompt = %#v", last)
		}
		return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "The citation could not be verified, so it was removed."}}, nil
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: "Verified citation: 10.9999/invented"}}, nil
}

func (model *artifactReferenceRepairModel) Complete(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	model.calls++
	if model.calls > 1 {
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "system" || !strings.Contains(last.Content, "unresolved artifact references") {
			model.testing.Fatalf("artifact correction prompt = %#v", last)
		}
	}
	content := strings.TrimSpace(model.content)
	if content == "" {
		content = "[report.md]({{artifact:" + unresolvedArtifactVersionID + "}})"
	}
	if model.calls > 1 && !model.alwaysUnresolved {
		content = "[report.md]({{artifact:" + model.versionID + "}})"
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{Role: "assistant", Content: content}}, nil
}

func (model *scientificArtifactRepairModel) Complete(_ context.Context, request agentruntime.ModelRequest) (agentruntime.ModelResponse, error) {
	model.calls++
	versionID := model.badVersionID
	if model.calls > 1 {
		last := request.Messages[len(request.Messages)-1]
		if last.Role != "system" || !strings.Contains(last.Content, "failed strict parsing in the pinned managed RDKit runtime") ||
			!strings.Contains(last.Content, "invalid_sdf_records") {
			model.testing.Fatalf("scientific artifact correction prompt=%#v", last)
		}
		versionID = model.goodVersionID
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", Content: "[molecules.sdf]({{artifact:" + versionID + "}})",
	}}, nil
}

func TestRunVerifiedSessionAgentRejectsUnresolvedArtifactWithoutAutomaticReplacement(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	_, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-report", ProjectID: "project-a", Name: "report.md",
		Kind: "markdown", Content: []byte("verified report"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &artifactReferenceRepairModel{versionID: version.ID, testing: t}
	server := &Server{workspaceStore: store}
	result, err := server.runVerifiedSessionAgent(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		SessionRunnerChatOptions{},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Create report.md"}}},
		sessionRunnerTaskContract{},
		nil,
		nil,
	)
	var integrityErr *sessionRunnerReferenceIntegrityError
	if !errors.As(err, &integrityErr) || integrityErr.UnresolvedArtifacts != 1 {
		t.Fatalf("error=%#v", err)
	}
	if model.calls != 1 || !strings.Contains(result.FinalMessage.Content, unresolvedArtifactVersionID) || strings.Contains(result.FinalMessage.Content, version.ID) {
		t.Fatalf("calls=%d final=%q", model.calls, result.FinalMessage.Content)
	}
}

func TestRunVerifiedSessionAgentNormalizesMalformedArtifactLinkEnvelopeToPlainLabel(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	_, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-report", ProjectID: "project-a", Name: "report.md",
		Kind: "markdown", Content: []byte("verified report"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	content := "[report.md]({{artifact:" + version.ID + "}}))"
	model := &artifactReferenceRepairModel{content: content, testing: t}
	result, err := (&Server{workspaceStore: store}).runVerifiedSessionAgent(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		SessionRunnerChatOptions{},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Create report.md"}}},
		sessionRunnerTaskContract{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("syntax-only artifact normalization failed: %v", err)
	}
	if model.calls != 1 || result.FinalMessage.Content != "report.md" ||
		len(sessionRunnerArtifactReferenceSyntaxFailures(result.FinalMessage.Content)) != 0 {
		t.Fatalf("calls=%d final=%q", model.calls, result.FinalMessage.Content)
	}
}

func TestRunVerifiedSessionAgentNormalizesNonCanonicalArtifactPlaceholderBeforeIntegrityGate(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	model := &artifactReferenceRepairModel{
		content: "已生成 [母配体]({{artifact:artifact-id-goes-here}})。任务完成。",
		testing: t,
	}
	result, err := (&Server{workspaceStore: store}).runVerifiedSessionAgent(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		SessionRunnerChatOptions{},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Create the deliverable"}}},
		sessionRunnerTaskContract{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("non-canonical placeholder should be safely normalized: %v", err)
	}
	if got := result.FinalMessage.Content; got != "已生成 母配体。任务完成。" {
		t.Fatalf("normalized final=%q", got)
	}
}

func TestRunVerifiedSessionAgentNormalizesNonCanonicalArtifactURIToPlainLabel(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	model := &artifactReferenceRepairModel{
		content: "Files: [report.md](artifact:/artifacts/aca077ca-4a7a-5f92-9003-ae3d43dd9d26) [open](artifact:aca077ca-4a7a-5f92-9003-ae3d43dd9d26)",
		testing: t,
	}
	result, err := (&Server{workspaceStore: store}).runVerifiedSessionAgent(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		SessionRunnerChatOptions{},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Create the deliverable"}}},
		sessionRunnerTaskContract{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("non-canonical artifact URI normalization failed: %v", err)
	}
	if got := result.FinalMessage.Content; got != "Files: report.md open" {
		t.Fatalf("normalized final=%q", got)
	}
}

func TestNormalizeSessionRunnerMalformedArtifactReferencesPreservesValidReferencesAndVisibleText(t *testing.T) {
	valid := "{{artifact:11111111-1111-4111-8111-111111111111}}"
	tests := map[string]struct {
		content string
		want    string
		changed bool
	}{
		"valid reference": {
			content: valid,
			want:    valid,
		},
		"missing close brace in link": {
			content: "Open [report.md]({{artifact:version-broken}) for details.",
			want:    "Open report.md for details.",
			changed: true,
		},
		"surplus close delimiter": {
			content: "Open [report.md]({{artifact:version-broken}})) now.",
			want:    "Open report.md now.",
			changed: true,
		},
		"standalone malformed marker": {
			content: "Artifact: {{artifact:version-broken next.",
			want:    "Artifact:  next.",
			changed: true,
		},
		"mixed valid and malformed": {
			content: valid + "\n[report.md]({{artifact:version-broken})",
			want:    valid + "\nreport.md",
			changed: true,
		},
		"non canonical placeholder": {
			content: "已生成 [母配体]({{artifact:artifact-id-goes-here}})。任务完成。",
			want:    "已生成 母配体。任务完成。",
			changed: true,
		},
		"canonical image destination remains inline": {
			content: "Figure\n\n![plot](" + valid + ")\n",
			want:    "Figure\n\n![plot](" + valid + ")\n",
		},
		"non canonical image destination keeps visible label": {
			content: "Figure\n\n![plot]({{artifact:version-broken}})\n",
			want:    "Figure\n\nplot\n",
			changed: true,
		},
		"placeholder image destination keeps visible label": {
			content: "Figure\n\n![plot](#)\n",
			want:    "Figure\n\nplot\n",
			changed: true,
		},
		"non canonical artifact path URI keeps visible label": {
			content: "Open [report.md](artifact:/artifacts/aca077ca-4a7a-5f92-9003-ae3d43dd9d26).",
			want:    "Open report.md.",
			changed: true,
		},
		"non canonical artifact identity URI keeps visible label": {
			content: "[Click to view](artifact:aca077ca-4a7a-5f92-9003-ae3d43dd9d26)",
			want:    "Click to view",
			changed: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, changed := normalizeSessionRunnerMalformedArtifactReferences(test.content)
			if got != test.want || changed != test.changed {
				t.Fatalf("normalized=%q changed=%t, want=%q changed=%t", got, changed, test.want, test.changed)
			}
			if len(sessionRunnerArtifactReferenceSyntaxFailures(got)) != 0 {
				t.Fatalf("normalization left malformed artifact syntax: %q", got)
			}
		})
	}
}

func TestReplaceSessionRunnerFinalMessageContentUpdatesStreamNormalizedAssistant(t *testing.T) {
	result := agentruntime.RunResult{
		FinalMessage: agentruntime.Message{Role: "assistant", Content: "Files:\n[report](#)"},
		Messages: []agentruntime.Message{
			{Role: "assistant", Content: "Earlier waypoint"},
			{Role: "assistant", Content: "Files: [report](#)"},
		},
	}
	replaceSessionRunnerFinalMessageContent(&result, "Files:\nreport")
	if result.FinalMessage.Content != "Files:\nreport" || result.Messages[1].Content != "Files:\nreport" {
		t.Fatalf("final replacement did not reach persisted message: %#v", result)
	}
	if result.Messages[0].Content != "Earlier waypoint" {
		t.Fatalf("replacement changed prior assistant waypoint: %#v", result.Messages)
	}
}

func TestSessionRunnerArtifactReferenceSyntaxContract(t *testing.T) {
	const versionID = "11111111-1111-4111-8111-111111111111"
	tests := map[string]struct {
		content string
		valid   bool
	}{
		"standalone":              {content: "{{artifact:" + versionID + "}}", valid: true},
		"standalone on own line":  {content: "Figure\n\n{{artifact:" + versionID + "}}\n", valid: true},
		"exact file link":         {content: "[report.md]({{artifact:" + versionID + "}})", valid: true},
		"file link in prose":      {content: "Open [report.md]({{artifact:" + versionID + "}}).", valid: true},
		"surplus close delimiter": {content: "[report.md]({{artifact:" + versionID + "}}))"},
		"missing close delimiter": {content: "[report.md]({{artifact:" + versionID + "}}"},
		"blank link label":        {content: "[]({{artifact:" + versionID + "}})"},
		"image destination":       {content: "![plot]({{artifact:" + versionID + "}})", valid: true},
		"larger URL destination":  {content: "https://example.test/{{artifact:" + versionID + "}}"},
		"inline punctuation":      {content: "See {{artifact:" + versionID + "}}."},
		"malformed placeholder":   {content: "{{artifact:" + versionID + "}"},
		"non UUID placeholder":    {content: "[report.md]({{artifact:artifact-id-goes-here}})"},
		"nested placeholder":      {content: "{{artifact:{{artifact:" + versionID + "}}}}"},
		"empty markdown link":     {content: "[templates.csv]()"},
		"hash placeholder link":   {content: "[templates.csv](#)"},
		"hash placeholder image":  {content: "![plot.png](#)"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			failures := sessionRunnerArtifactReferenceSyntaxFailures(test.content)
			if gotValid := len(failures) == 0; gotValid != test.valid {
				t.Fatalf("valid=%t failures=%#v content=%q", gotValid, failures, test.content)
			}
		})
	}
}

func TestRunVerifiedSessionAgentDoesNotSpendToolBudgetOnAutomaticReplacement(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	model := &toolBudgetRepairModel{}
	var gatewayCalls atomic.Int64
	server := &Server{workspaceStore: store}
	_, err := server.runVerifiedSessionAgent(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		SessionRunnerChatOptions{},
		agentruntime.Engine{
			Model: model,
			Tools: agentruntime.FuncToolGateway(func(context.Context, agentruntime.ToolCall) (agentruntime.ToolResult, error) {
				gatewayCalls.Add(1)
				return agentruntime.ToolResult{Value: map[string]any{"ok": true, "results": []any{}}}, nil
			}),
		},
		agentruntime.RunRequest{
			Messages:      []agentruntime.Message{{Role: "user", Content: "Create the report"}},
			Tools:         []agentruntime.ToolSchema{{Name: "mcp__pubmed__search", Parameters: map[string]any{"type": "object"}}},
			MaxToolRounds: 1,
		},
		sessionRunnerTaskContract{},
		nil,
		nil,
	)
	var integrityErr *sessionRunnerReferenceIntegrityError
	if !errors.As(err, &integrityErr) || integrityErr.UnresolvedArtifacts != 1 {
		t.Fatalf("error = %#v", err)
	}
	if model.calls.Load() != 2 || gatewayCalls.Load() != 1 {
		t.Fatalf("modelCalls=%d gatewayCalls=%d", model.calls.Load(), gatewayCalls.Load())
	}
}

func TestArtifactReferenceValidationPreservesRejectedCandidateWithoutCorrection(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	_, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-report", ProjectID: "project-a", Name: "report.md",
		Kind: "markdown", Content: []byte("verified report"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	resets := []string{}
	model := &artifactReferenceRepairModel{versionID: version.ID, testing: t}
	result, err := (&Server{workspaceStore: store}).runSessionAgentWithArtifactReferenceRepair(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Create report.md"}}},
		nil,
		func(_ int, text string) error {
			resets = append(resets, text)
			return nil
		},
	)
	var integrityErr *sessionRunnerReferenceIntegrityError
	if !errors.As(err, &integrityErr) || integrityErr.UnresolvedArtifacts != 1 {
		t.Fatalf("error=%#v", err)
	}
	if len(resets) != 0 || model.calls != 1 || !strings.Contains(result.FinalMessage.Content, unresolvedArtifactVersionID) {
		t.Fatalf("candidate resets = %#v", resets)
	}
}

func TestArtifactReferenceRepairPreservesFinalRejectedCandidate(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	model := &artifactReferenceRepairModel{alwaysUnresolved: true, testing: t}
	resets := []string{}
	result, err := (&Server{workspaceStore: store}).runSessionAgentWithArtifactReferenceRepair(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Create report.md"}}},
		nil,
		func(_ int, text string) error {
			resets = append(resets, text)
			return nil
		},
	)
	var integrityErr *sessionRunnerReferenceIntegrityError
	if !errors.As(err, &integrityErr) {
		t.Fatalf("error = %#v", err)
	}
	if len(resets) != 0 || model.calls != 1 || !strings.Contains(result.FinalMessage.Content, unresolvedArtifactVersionID) {
		t.Fatalf("candidate resets=%#v calls=%d final=%q", resets, model.calls, result.FinalMessage.Content)
	}
}

func TestRunVerifiedSessionAgentFailsClosedWhenArtifactRepairDoesNotConverge(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	model := &artifactReferenceRepairModel{alwaysUnresolved: true, testing: t}
	server := &Server{workspaceStore: store}
	_, err := server.runVerifiedSessionAgent(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		SessionRunnerChatOptions{},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Create report.md"}}},
		sessionRunnerTaskContract{},
		nil,
		nil,
	)
	var integrityErr *sessionRunnerReferenceIntegrityError
	if !errors.As(err, &integrityErr) || integrityErr.UnresolvedArtifacts != 1 || len(integrityErr.UnsupportedCitations) != 0 {
		t.Fatalf("non-convergent repair error = %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d", model.calls)
	}
}

func TestRunVerifiedSessionAgentPreservesUnboundCitationAsAdvisory(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	model := &citationReferenceRepairModel{testing: t}
	server := &Server{workspaceStore: store}
	result, err := server.runVerifiedSessionAgent(
		context.Background(),
		sessionstore.Session{ID: "frame-a", Project: &sessionstore.Project{ID: "project-a"}},
		SessionRunnerChatOptions{},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Find a verified paper"}}},
		sessionRunnerTaskContract{},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("unbound citation blocked an otherwise complete result: %v", err)
	}
	if model.calls != 1 || !strings.Contains(result.FinalMessage.Content, "10.9999/invented") {
		t.Fatalf("calls=%d final=%q", model.calls, result.FinalMessage.Content)
	}
}

func TestSessionRunnerCitationGroundingUsesGovernedSourceEvidenceToolMetadata(t *testing.T) {
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "binding-4ci1", Name: "binding_mode_analysis", Arguments: json.RawMessage(`{"pdb_id":"4CI1"}`),
		}}},
		{Role: "tool", ToolCallID: "binding-4ci1", Content: `{
			"ok":true,
			"pdbId":"4CI1",
			"sources":[{"provider":"binding_mode_analysis","url":"https://www.rcsb.org/structure/4CI1","metadata":{"pdbId":"4CI1"}}]
		}`},
	}
	server := &Server{tools: toolregistry.Default()}
	unsupported := unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
		messages, len(messages), "Verified structure PDB: 4CI1.", nil, nil, server.sessionRunnerEvidenceTool,
	)
	if len(unsupported) != 0 {
		t.Fatalf("governed source-evidence tool references unsupported = %#v", unsupported)
	}

	forged := append([]agentruntime.Message(nil), messages...)
	forged[0].ToolCalls[0].Name = "python"
	unsupported = unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
		forged, len(forged), "Claimed structure PDB: 4CI1.", nil, nil, server.sessionRunnerEvidenceTool,
	)
	if len(unsupported) != 1 || unsupported[0] != "accession:pdb:4CI1" {
		t.Fatalf("arbitrary Python output unexpectedly became evidence = %#v", unsupported)
	}

	forged[0].ToolCalls[0].Name = "mcp__custom_echo__echo"
	unsupported = unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
		forged, len(forged), "Claimed structure PDB: 4CI1.", nil, nil, server.sessionRunnerEvidenceTool,
	)
	if len(unsupported) != 1 || unsupported[0] != "accession:pdb:4CI1" {
		t.Fatalf("custom MCP name unexpectedly became evidence = %#v", unsupported)
	}
}

func TestSessionRunnerCitationGroundingAcceptsURLsReturnedByWebSearch(t *testing.T) {
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "search-patent", Name: "WebSearch", Arguments: json.RawMessage(`{"query":"US10155740B2"}`),
		}}},
		{Role: "tool", ToolCallID: "search-patent", Content: `{
			"ok":true,
			"results":[{"title":"US10155740B2","url":"https://patents.google.com/patent/US10155740B2/en","snippet":"Patent record"}]
		}`},
	}
	server := &Server{tools: toolregistry.Default()}
	unsupported := unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
		messages, len(messages), "Source: https://patents.google.com/patent/US10155740B2/en", nil, nil, server.sessionRunnerEvidenceTool,
	)
	if len(unsupported) != 0 {
		t.Fatalf("web search result URL was not accepted as source evidence: %#v", unsupported)
	}
}

func TestSessionRunnerCitationGroundingRecognizesStrictChEMBLMechanismEvidence(t *testing.T) {
	requestURL := "https://www.ebi.ac.uk/chembl/api/data/mechanism.json?molecule_chembl_id=CHEMBL3353410&limit=5"
	body := `{"mechanisms":[{"molecule_chembl_id":"CHEMBL3353410","parent_molecule_chembl_id":"CHEMBL3353410","target_chembl_id":"CHEMBL203","mechanism_refs":[{"ref_id":"24893891","ref_type":"PubMed","ref_url":"http://europepmc.org/abstract/MED/24893891"}],"variant_sequence":{"accession":"P00533","tax_id":9606}}]}`
	result, err := json.Marshal(map[string]any{
		"ok": true,
		"result": map[string]any{
			"code": 200, "url": requestURL, "result": body,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]any{"url": requestURL, "prompt": "Extract mechanism fields."})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "chembl-mechanism", Name: "WebFetch", Arguments: arguments}}},
		{Role: "tool", ToolCallID: "chembl-mechanism", Content: string(result)},
	}
	candidate := strings.Join([]string{
		"ChEMBL CHEMBL3353410 targets UniProt P00533.",
		"The authoritative mechanism record cites PMID 24893891.",
		"Source: " + requestURL,
	}, "\n")
	if unsupported := unsupportedSessionRunnerCitationReferences(messages, len(messages), candidate, nil); len(unsupported) != 0 {
		t.Fatalf("strict ChEMBL mechanism evidence was not recognized: %#v", unsupported)
	}

	forgedBody := `{"mechanisms":[{"molecule_chembl_id":"CHEMBL9999999","mechanism_refs":[{"ref_id":"24893891","ref_type":"PubMed"}],"variant_sequence":{"accession":"P00533","tax_id":9606}}]}`
	forgedResult, err := json.Marshal(map[string]any{
		"ok":     true,
		"result": map[string]any{"code": 200, "url": requestURL, "result": forgedBody},
	})
	if err != nil {
		t.Fatal(err)
	}
	forged := append([]agentruntime.Message(nil), messages...)
	forged[1].Content = string(forgedResult)
	unsupported := unsupportedSessionRunnerCitationReferences(forged, len(forged), "UniProt P00533; PMID 24893891", nil)
	if len(unsupported) != 2 {
		t.Fatalf("mismatched ChEMBL molecule authorized evidence: %#v", unsupported)
	}
}

func TestSessionRunnerCitationGroundingUsesDurableEvidenceAcrossAttempts(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const endpoint = "https://data.rcsb.org/rest/v1/core/entry/6S73"
	payload, err := json.Marshal(map[string]any{
		"toolName": "WebFetch", "toolPhase": "completed", "toolCallId": "prior-rcsb-6s73",
		"toolInput": map[string]any{"url": endpoint},
		"toolResult": map[string]any{
			"ok": true,
			"result": map[string]any{
				"code": 200, "url": endpoint,
				"result": `{"entry":{"id":"6S73"}}`,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(
		context.Background(),
		transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: "prior-rcsb-evidence",
			Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: payload,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: fixture.claim, ClientMessageID: "prior-attempt-failed", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := fixture.repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
		ClientMessageID: "repair-next-attempt", PayloadJSON: []byte(`{"text":"continue"}`),
	}); err != nil || !created {
		t.Fatalf("append next-attempt input created=%t err=%v", created, err)
	}
	next, err := fixture.repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
		RunnerID: "runner-next-attempt", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !next.Claimed || next.Claim.Attempt != fixture.claim.Attempt+1 {
		t.Fatalf("next claim=%#v err=%v", next, err)
	}

	model := &durableEvidenceCitationModel{}
	result, err := fixture.server.runSessionAgentWithArtifactReferenceRepair(
		context.Background(),
		sessionstore.Session{ID: "frame-save", Project: &sessionstore.Project{ID: "project-save"}},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Continue the report."}}},
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: next.Claim}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 || !strings.Contains(result.FinalMessage.Content, "PDB: 6S73") {
		t.Fatalf("model calls=%d final=%q", model.calls, result.FinalMessage.Content)
	}
}

func TestSessionRunnerReferenceValidationPreservesUnboundCitationWithoutReplacement(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	const (
		doi      = "10.1038/s41586-019-1295-z"
		endpoint = "https://api.crossref.org/works/10.1038/s41586-019-1295-z"
	)
	payload, err := json.Marshal(map[string]any{
		"toolName": "WebFetch", "toolPhase": "completed", "toolCallId": "prior-crossref-real",
		"toolInput": map[string]any{"url": endpoint},
		"toolResult": map[string]any{
			"ok": true,
			"result": map[string]any{
				"code": 200, "url": endpoint,
				"body": `{"status":"ok","message":{"DOI":"` + doi + `","title":["Cryo-EM structure of the active NLRP3 inflammasome"]}}`,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(
		context.Background(),
		transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: "prior-crossref-evidence",
			Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: payload,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: fixture.claim, ClientMessageID: "prior-attempt-failed", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := fixture.repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
		ClientMessageID: "repair-next-attempt", PayloadJSON: []byte(`{"text":"continue"}`),
	}); err != nil || !created {
		t.Fatalf("append next-attempt input created=%t err=%v", created, err)
	}
	next, err := fixture.repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID,
		RunnerID: "runner-next-attempt", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !next.Claimed {
		t.Fatalf("next claim=%#v err=%v", next, err)
	}
	model := &referenceEvidenceLedgerRepairModel{testing: t}
	result, err := fixture.server.runSessionAgentWithArtifactReferenceRepair(
		context.Background(),
		sessionstore.Session{ID: "frame-save", Project: &sessionstore.Project{ID: "project-save"}},
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Continue the report."}}},
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: next.Claim}},
		nil,
	)
	if err != nil {
		t.Fatalf("unbound citation blocked durable completion: %v", err)
	}
	if model.calls != 1 || !strings.Contains(result.FinalMessage.Content, "10.9999/invented") {
		t.Fatalf("model calls=%d final=%q", model.calls, result.FinalMessage.Content)
	}
}

func TestSessionRunnerDurableEvidenceDeduplicatesOnlyExactCurrentPair(t *testing.T) {
	durableCall := agentruntime.ToolCall{
		ID: "call-a", Name: "WebFetch",
		Arguments: json.RawMessage(`{"prompt":"metadata","url":"https://data.rcsb.org/rest/v1/core/entry/6S73"}`),
	}
	durable := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{durableCall}},
		{Role: "tool", ToolCallID: durableCall.ID, Content: `{"ok":true,"result":{"code":200}}`},
	}
	exactCurrent := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "call-a", Name: "WebFetch",
			Arguments: json.RawMessage(`{"url":"https://data.rcsb.org/rest/v1/core/entry/6S73","prompt":"metadata"}`),
		}}},
		{Role: "tool", ToolCallID: "call-a", Content: `{"result":{"code":200},"ok":true}`},
	}
	if prefix := sessionRunnerDurableEvidencePrefix(durable, exactCurrent); len(prefix) != 0 {
		t.Fatalf("exact duplicate prefix=%#v", prefix)
	}

	collidingCurrent := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "call-a", Name: "WebFetch",
			Arguments: json.RawMessage(`{"url":"https://data.rcsb.org/rest/v1/core/entry/8EJ4","prompt":"metadata"}`),
		}}},
		{Role: "tool", ToolCallID: "call-a", Content: `{"ok":true,"result":{"code":200}}`},
	}
	if prefix := sessionRunnerDurableEvidencePrefix(durable, collidingCurrent); !reflect.DeepEqual(prefix, durable) {
		t.Fatalf("colliding call-id prefix=%#v", prefix)
	}
}

func TestSessionRunnerEquivalentToolEvidenceCopiesCollapseWithoutWeakeningConflict(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "crossref-copy", Name: "WebFetch",
		Arguments: json.RawMessage(`{"url":"https://api.crossref.org/works/10.1000/example","prompt":"metadata"}`),
	}
	result := `{"ok":true,"result":{"code":404}}`
	messages := make([]agentruntime.Message, 0, 6)
	for range 3 {
		messages = append(messages,
			agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
			agentruntime.Message{Role: "tool", ToolCallID: call.ID, Content: result},
		)
	}
	pairs := sessionRunnerUniqueToolEvidencePairs(messages)
	if len(pairs) != 1 || pairs[call.ID].content != result {
		t.Fatalf("equivalent replay pairs=%#v", pairs)
	}
	conflicting := append([]agentruntime.Message(nil), messages...)
	conflicting[len(conflicting)-1].Content = `{"ok":true,"result":{"code":200}}`
	if pairs := sessionRunnerUniqueToolEvidencePairs(conflicting); len(pairs) != 0 {
		t.Fatalf("conflicting replay evidence was accepted: %#v", pairs)
	}
}

func TestSessionRunnerDurableEvidencePaginatesToSnapshotWatermark(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	for index := range 2 {
		payload, err := json.Marshal(map[string]any{
			"toolName": "Read", "toolPhase": "completed",
			"toolCallId": fmt.Sprintf("non-evidence-%d", index),
			"toolInput":  map[string]any{"path": "notes.md"},
			"toolResult": map[string]any{"ok": true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(
			context.Background(),
			transcriptstore.AppendRunnerCheckpointInput{
				Claim: fixture.claim, ClientMessageID: fmt.Sprintf("non-evidence-%d", index),
				Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	const endpoint = "https://data.rcsb.org/rest/v1/core/entry/8EJ4"
	evidence, err := json.Marshal(map[string]any{
		"toolName": "WebFetch", "toolPhase": "completed", "toolCallId": "page-two-rcsb",
		"toolInput": map[string]any{"url": endpoint},
		"toolResult": map[string]any{
			"ok": true,
			"result": map[string]any{
				"code": 200, "url": endpoint,
				"result": `{"entry":{"id":"8EJ4"}}`,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(
		context.Background(),
		transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: "page-two-rcsb",
			Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: evidence,
		},
	); err != nil {
		t.Fatal(err)
	}

	messages, err := fixture.server.sessionRunnerDurableEvidenceMessagesPage(
		context.Background(),
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}},
		2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].ToolCalls) != 1 || messages[0].ToolCalls[0].ID != "page-two-rcsb" ||
		messages[1].ToolCallID != "page-two-rcsb" {
		t.Fatalf("durable paged evidence=%#v", messages)
	}
}

func TestSessionRunnerDurableEvidenceAcceptsOnlyAttestedBundledMCPCheckpoint(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	toolInput := map[string]any{"query": "osimertinib T790M", "max_results": 5}
	toolResult := `{"pmids":["42486940"],"returned_count":1}`
	inputRaw, err := json.Marshal(toolInput)
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := json.Marshal(toolResult)
	if err != nil {
		t.Fatal(err)
	}
	inputSchemaRaw, err := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"schema": "synon.kernel_mcp_evidence.v1", "status": "completed", "toolPhase": "completed",
		"toolName": "mcp__pubmed__search_articles", "toolCallId": "host-pubmed-1",
		"toolInput": toolInput, "toolResult": toolResult,
		"evidenceClass": workspace.KernelMCPEvidenceClassBundledReadOnly,
		"connectorId":   "bundled:pubmed", "connectorSource": "bundled", "readOnlyHint": true,
		"inputSchemaSha256": kernelMCPEvidenceSHA256(inputSchemaRaw),
		"outerToolCallId":   "repl-call-1", "kernelOperationId": "operation-1",
		"executionId": "execution-1", "hostCallId": "host-pubmed-1",
		"kernelId": "kernel-1", "kernelGeneration": int64(1),
		"requestSha256": kernelMCPEvidenceSHA256(inputRaw), "resultSha256": kernelMCPEvidenceSHA256(resultRaw),
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "attested-bundled-mcp", Phase: transcriptstore.RunnerPhaseExecuting,
		PayloadJSON: rawPayload,
	}); err != nil {
		t.Fatal(err)
	}
	forged := make(map[string]any, len(payload))
	for key, value := range payload {
		forged[key] = value
	}
	forged["toolCallId"], forged["hostCallId"], forged["connectorSource"] = "host-custom-1", "host-custom-1", "custom"
	forgedRaw, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "forged-custom-mcp", Phase: transcriptstore.RunnerPhaseExecuting,
		PayloadJSON: forgedRaw,
	}); err != nil {
		t.Fatal(err)
	}
	directInput := map[string]any{"pdb_ids": []any{"7ACK"}}
	directResult := `{"pdb_id":"7ACK","resolution_angstrom":1.8}`
	directInputRaw, err := json.Marshal(directInput)
	if err != nil {
		t.Fatal(err)
	}
	directResultRaw, err := json.Marshal(directResult)
	if err != nil {
		t.Fatal(err)
	}
	directPayload := map[string]any{
		"schema": workspaceMCPSourceEvidenceSchemaV1, "status": "completed", "toolPhase": "completed",
		"toolName": "mcp__structures-interactions__pdb_get_structures", "toolCallId": "direct-pdb-1",
		"toolInput": directInput, "toolResult": directResult,
		"evidenceClass": workspace.KernelMCPEvidenceClassBundledReadOnly,
		"connectorId":   "bundled:structures-interactions", "connectorSource": "bundled", "readOnlyHint": true,
		"inputSchemaSha256": kernelMCPEvidenceSHA256(inputSchemaRaw),
		"requestSha256":     kernelMCPEvidenceSHA256(directInputRaw),
		"resultSha256":      kernelMCPEvidenceSHA256(directResultRaw),
	}
	directPayloadRaw, err := json.Marshal(directPayload)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "attested-direct-bundled-mcp", Phase: transcriptstore.RunnerPhaseExecuting,
		PayloadJSON: directPayloadRaw,
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := fixture.server.sessionRunnerDurableEvidenceMessages(
		context.Background(),
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || len(messages[0].ToolCalls) != 1 || messages[0].ToolCalls[0].ID != "host-pubmed-1" ||
		!messages[0].ToolCalls[0].VerifiedEvidence || messages[1].ToolCallID != "host-pubmed-1" ||
		!strings.Contains(messages[1].Content, "42486940") || len(messages[2].ToolCalls) != 1 ||
		messages[2].ToolCalls[0].ID != "direct-pdb-1" || !messages[2].ToolCalls[0].VerifiedEvidence ||
		messages[3].ToolCallID != "direct-pdb-1" || !strings.Contains(messages[3].Content, "7ACK") {
		t.Fatalf("durable bundled MCP evidence=%#v", messages)
	}
	unsupported := unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
		messages, len(messages), "Selected structure PDB: 7ACK.", nil, nil, fixture.server.sessionRunnerEvidenceTool,
	)
	if len(unsupported) != 0 {
		t.Fatalf("attested direct MCP evidence was rejected: %#v", unsupported)
	}
	if gaps := sessionRunnerExplicitToolContractGaps(
		"通过 MCP 的真实方法查询至少两个不同数据域，至少包含 PubMed。", messages,
	); len(gaps) != 0 {
		t.Fatalf("explicit MCP contract ignored durable nested and direct receipts: %#v", gaps)
	}
	explicitMessages, err := fixture.server.sessionRunnerDurableExplicitToolContractMessages(
		context.Background(),
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if gaps := sessionRunnerExplicitToolContractGaps(
		"通过 MCP 的真实方法查询至少两个不同数据域，至少包含 PubMed。", explicitMessages,
	); len(gaps) != 0 {
		t.Fatalf("explicit-tool projection ignored attested nested and direct MCP receipts: %#v", gaps)
	}
}

func TestSessionRunnerToolCheckpointPersistsDirectWorkspaceMCPEvidence(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	run := &sessionRunnerChatRun{
		SessionID: "frame-save",
		Transcript: &transcriptRunnerAuthority{
			Stream: fixture.stream,
			Claim:  fixture.claim,
		},
	}
	ctx := withWorkspaceMCPSourceEvidenceCollector(context.Background())
	input := map[string]any{"pdb_ids": []any{"7ACK"}}
	result := map[string]any{"ok": true, "result": `{"pdb_id":"7ACK","resolution_angstrom":1.8}`}
	inputSchemaRaw, err := json.Marshal(map[string]any{
		"type": "object", "properties": map[string]any{"pdb_ids": map[string]any{"type": "array"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recordWorkspaceMCPSourceEvidence(ctx, "direct-pdb-runtime", workspaceMCPSourceEvidenceAttestation{
		Schema: workspaceMCPSourceEvidenceSchemaV1, EvidenceClass: workspace.KernelMCPEvidenceClassBundledReadOnly,
		ConnectorID: "bundled:structures-interactions", ConnectorSource: "bundled",
		InputSchemaSHA256: kernelMCPEvidenceSHA256(inputSchemaRaw), ReadOnlyHint: true,
	})
	arguments, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointSessionRunnerToolEvent(ctx, SessionRunnerChatOptions{SessionID: "frame-save"}, run, agentruntime.Event{
		Type: agentruntime.EventToolCompleted, ToolName: "mcp__structures-interactions__pdb_get_structures",
		ToolCallID: "direct-pdb-runtime", Arguments: string(arguments), Result: string(encodedResult),
	}); err != nil {
		t.Fatal(err)
	}
	messages, err := fixture.server.sessionRunnerDurableEvidenceMessages(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].ToolCalls) != 1 ||
		messages[0].ToolCalls[0].ID != "direct-pdb-runtime" || !messages[0].ToolCalls[0].VerifiedEvidence ||
		messages[1].ToolCallID != "direct-pdb-runtime" || !strings.Contains(messages[1].Content, "7ACK") {
		t.Fatalf("durable direct MCP evidence=%#v", messages)
	}
}

func TestSessionRunnerCitationGroundingUsesOnlySuccessfulEvidenceTools(t *testing.T) {
	const doi = "10.1234/verified.2026"
	searchCall := agentruntime.ToolCall{ID: "search-1", Name: "mcp__pubmed__search_articles", Arguments: json.RawMessage(`{"query":"verified paper"}`)}
	fetchCall := agentruntime.ToolCall{ID: "fetch-1", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"https://doi.org/` + doi + `"}`)}
	writeCall := agentruntime.ToolCall{ID: "write-1", Name: "file_write", Arguments: json.RawMessage(`{"path":"report.md","content":"DOI: 10.5555/self.echo"}`)}
	tests := []struct {
		name     string
		messages []agentruntime.Message
		want     int
	}{
		{
			name: "MCP discovery is not claim evidence",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{searchCall}},
				{Role: "tool", ToolCallID: searchCall.ID, Content: `{"ok":true,"result":{"doi":"` + doi + `"}}`},
				{Role: "assistant", Content: "DOI: " + doi},
			},
			want: 1,
		},
		{
			name: "failed fetch is not evidence",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{fetchCall}},
				{Role: "tool", ToolCallID: fetchCall.ID, Content: `{"ok":true,"result":{"ok":false,"error":"missing ` + doi + `"}}`},
				{Role: "assistant", Content: "DOI: " + doi},
			},
			want: 1,
		},
		{
			name: "failed fetch identifier is not itself a candidate claim",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{fetchCall}},
				{Role: "tool", ToolCallID: fetchCall.ID, Content: `{"ok":true,"result":{"sourceUnavailable":true,"url":"https://doi.org/` + doi + `"}}`},
				{Role: "assistant", Content: "No citation could be verified."},
			},
			want: 0,
		},
		{
			name: "unavailable article fulltext cannot ground its DOI",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
					ID: "article-unavailable", Name: "fetch_article_fulltext",
					Arguments: json.RawMessage(`{"doi":"` + doi + `"}`),
				}}},
				{Role: "tool", ToolCallID: "article-unavailable", Content: `{"ok":true,"result":{"available":false,"status":"not_available","doi":"` + doi + `"}}`},
				{Role: "assistant", Content: "DOI: " + doi},
			},
			want: 1,
		},
		{
			name: "substantive article fulltext grounds its DOI",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
					ID: "article-available", Name: "fetch_article_fulltext",
					Arguments: json.RawMessage(`{"doi":"` + doi + `"}`),
				}}},
				{Role: "tool", ToolCallID: "article-available", Content: `{"ok":true,"result":{"available":true,"doi":"` + doi + `","fulltext":"substantive methods and results"}}`},
				{Role: "assistant", Content: "DOI: " + doi},
			},
			want: 0,
		},
		{
			name: "write echo is not evidence",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{writeCall}},
				{Role: "tool", ToolCallID: writeCall.ID, Content: `{"ok":true,"result":{"bytes":32}}`},
				{Role: "assistant", Content: "report saved"},
			},
			want: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finalContent := test.messages[len(test.messages)-1].Content
			if got := unsupportedSessionRunnerCitationCount(test.messages, 0, finalContent, nil); got != test.want {
				t.Fatalf("unsupported citations = %d", got)
			}
		})
	}
}

func TestSessionRunnerAuthorityURLCandidatesNormalizeToStableIdentifiers(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  map[string]struct{}
	}{
		{
			name:  "Crossref work URL",
			value: "https://api.crossref.org/works/10.1038/s41586-019-1295-z?mailto=contact@example.org",
			want:  map[string]struct{}{"doi:10.1038/s41586-019-1295-z": {}},
		},
		{
			name:  "RCSB core entry URL",
			value: "https://data.rcsb.org/rest/v1/core/entry/6S73",
			want:  map[string]struct{}{"accession:pdb:6S73": {}},
		},
		{
			name:  "RCSB structure page URL",
			value: "https://www.rcsb.org/structure/6M0J",
			want:  map[string]struct{}{"accession:pdb:6M0J": {}},
		},
		{
			name:  "RCSB structure download URL",
			value: "https://files.rcsb.org/download/6M0J.cif",
			want:  map[string]struct{}{"accession:pdb:6M0J": {}},
		},
		{
			name:  "Markdown URL label does not absorb its target",
			value: "https://www.rcsb.org/structure/6M0J](https://www.rcsb.org/structure/6M0J)",
			want:  map[string]struct{}{"accession:pdb:6M0J": {}},
		},
		{
			name:  "PubMed E-utilities URL",
			value: "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esummary.fcgi?db=pubmed&id=123,456",
			want:  map[string]struct{}{"pmid:123": {}, "pmid:456": {}},
		},
		{
			name:  "ordinary public URL",
			value: "https://example.org/paper",
			want:  map[string]struct{}{"url:https://example.org/paper": {}},
		},
		{
			name:  "Markdown code URL",
			value: "`https://dailymed.nlm.nih.gov/dailymed/search.cfm?labeltype=all&query=osimertinib`",
			want:  map[string]struct{}{"url:https://dailymed.nlm.nih.gov/dailymed/search.cfm?labeltype=all&query=osimertinib": {}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			positive := map[string]struct{}{}
			addSessionRunnerURLReference(positive, test.value)
			if !reflect.DeepEqual(positive, test.want) {
				t.Fatalf("normalized references=%#v want=%#v", positive, test.want)
			}
		})
	}
}

func TestSessionRunnerCitationGroundingSeparatesSourceLandingPagesFromRecordURLs(t *testing.T) {
	positive := map[string]struct{}{}
	addSessionRunnerReferences(positive,
		"数据来源：[ChEMBL](https://www.ebi.ac.uk/chembl/)），通过受治理工具检索；"+
			"[ClinicalTrials.gov](https://clinicaltrials.gov/)），通过受治理工具检索。")
	if len(positive) != 0 {
		t.Fatalf("source landing pages must not become record citations: %#v", positive)
	}

	addSessionRunnerReferences(positive,
		"记录：[study](https://example.org/paper/)），通过受治理工具检索。")
	want := map[string]struct{}{"url:https://example.org/paper/": {}}
	if !reflect.DeepEqual(positive, want) {
		t.Fatalf("record URL references=%#v want=%#v", positive, want)
	}
}

func TestSessionRunnerCitationGroundingFailsClosedOnAmbiguousToolCallProvenance(t *testing.T) {
	const candidate = "PMID: 42"
	for _, test := range []struct {
		name     string
		messages []agentruntime.Message
	}{
		{
			name: "blank call id",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{Name: "mcp__pubmed__search"}}},
				{Role: "tool", Content: `{"ok":true,"result":{"pmid":42}}`},
			},
		},
		{
			name: "duplicate call id across evidence and non-evidence tools",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "duplicate", Name: "file_write"}}},
				{Role: "tool", ToolCallID: "duplicate", Content: `{"ok":true,"pmid":42}`},
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "duplicate", Name: "mcp__pubmed__search"}}},
				{Role: "tool", ToolCallID: "duplicate", Content: `{"ok":true,"result":{"pmid":42}}`},
			},
		},
		{
			name: "same id with different result",
			messages: []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "duplicate-result", Name: "mcp__pubmed__search"}}},
				{Role: "tool", ToolCallID: "duplicate-result", Content: `{"ok":true,"result":{"pmid":42}}`},
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "duplicate-result", Name: "mcp__pubmed__search"}}},
				{Role: "tool", ToolCallID: "duplicate-result", Content: `{"ok":true,"result":{"pmid":43}}`},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.messages = append(test.messages, agentruntime.Message{Role: "assistant", Content: candidate})
			if got := unsupportedSessionRunnerCitationCount(test.messages, 0, candidate, nil); got != 1 {
				t.Fatalf("ambiguous provenance citations = %d", got)
			}
		})
	}
}

func TestSessionRunnerCitationGroundingCoversTypedScientificReferences(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "evidence-typed", Name: "mcp__pubmed__get_article_metadata", Arguments: json.RawMessage(`{"pmid":"34161704"}`),
	}
	evidence := `{"ok":true,"result":{"status":"verified","pmids":["34161704"],"trial_id":"NCT03785249","doi":"10.1056/NEJMoa2105281","url":"https://pubmed.ncbi.nlm.nih.gov/34161704/","pdb_id":"7O6M"}}`
	grounded := strings.Join([]string{
		"PMID: 34161704", "DOI: 10.1056/NEJMoa2105281", "NCT03785249",
		"https://pubmed.ncbi.nlm.nih.gov/34161704/", "PDB 7O6M",
	}, "\n")
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: evidence},
		{Role: "assistant", Content: grounded},
	}
	if got := unsupportedSessionRunnerCitationCount(messages, 0, grounded, nil); got != 0 {
		t.Fatalf("grounded typed references = %d", got)
	}

	unsupported := strings.Join([]string{
		"PMIDs: 34758227, 35303250", "DOI: 10.9999/invented", "NCT09999999",
		"https://example.invalid/evidence", "PDB 9ZZZ",
	}, "\n")
	if got := unsupportedSessionRunnerCitationCount(nil, 0, unsupported, nil); got != 6 {
		t.Fatalf("unsupported typed references = %d", got)
	}
	artifact := "Mechanism,Primary Evidence Source (PMID)\nSecondary KRAS mutation,35303250\n"
	if got := unsupportedSessionRunnerCitationCount(nil, 0, "Artifact attached.", []string{artifact}); got != 1 {
		t.Fatalf("artifact PMID column references = %d", got)
	}
}

func TestSessionRunnerCitationGroundingReadsExternalizedPreviewIdentifiers(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "evidence-externalized", Name: "mcp__literature__openalex_get_work",
		Arguments: json.RawMessage(`{"doi":"10.1038/s41571-022-00639-9"}`),
	}
	previewPayload := map[string]any{
		"ok": true,
		"result": map[string]any{
			"records": []any{
				map[string]any{
					"doi": "10.1038/s41571-022-00639-9", "pmid": "35534623",
					"title": "ALK resistance review",
				},
			},
		},
	}
	previewJSON, err := json.Marshal(previewPayload)
	if err != nil {
		t.Fatal(err)
	}
	// A large tool result is externalized to an artifact; the verified tool
	// message carries the bounded preview instead of the full result.
	evidenceEnvelope, err := json.Marshal(map[string]any{
		"ok": true,
		"result": map[string]any{
			"artifact_id": "large-tool-result-abc", "content_type": "application/json",
			"content_url": "/api/artifacts/large-tool-result-abc/versions/1",
			"outcome":     "succeeded", "size_bytes": 89243, "truncated": true,
			"preview": string(previewJSON),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := string(evidenceEnvelope)
	grounded := "DOI: 10.1038/s41571-022-00639-9\nPMID: 35534623"
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: evidence},
		{Role: "assistant", Content: grounded},
	}
	if got := unsupportedSessionRunnerCitationCount(messages, 0, grounded, nil); got != 0 {
		t.Fatalf("externalized preview references = %d", got)
	}
}

func TestSessionRunnerCitationGroundingCanonicalizesResolversURLsAndAccessions(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "evidence-canonical", Name: "mcp__pubmed__get_article_metadata", Arguments: json.RawMessage(`{"pmid":"42"}`),
	}
	evidence := `{"ok":true,"result":{"status":"verified","dois":["10.1000/ABC.123","10.1000/foo(bar)"],"pmid":42,"trial_id":"NCT03785249","url":["https://EXAMPLE.com:443/path?q=b&a=1#source","https://root.example.org/"],"pdb_id":"7O6M","uniprot_id":"P04637","refseq_id":"NM_000546.6"}}`
	candidate := strings.Join([]string{
		"DOI resolver: https://doi.org/10.1000/ABC.123.",
		"Balanced DOI resolver: [paper](https://doi.org/10.1000/foo(bar))",
		"PubMed: https://PUBMED.NCBI.NLM.NIH.GOV/42/#history",
		"Trial: https://clinicaltrials.gov/study/NCT03785249?tab=history",
		"Source: [record](https://example.com/path?a=1&q=b).",
		"Root source: https://ROOT.example.org",
		"PDB 7O6M; UniProt P04637; RefSeq NM_000546.6",
	}, "\n")
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: evidence},
		{Role: "assistant", Content: candidate},
	}
	if got := unsupportedSessionRunnerCitationCount(messages, 0, candidate, nil); got != 0 {
		t.Fatalf("canonical typed references = %d", got)
	}
}

func TestSessionRunnerCitationGroundingRejectsNestedFailureAndUnrelatedNumbers(t *testing.T) {
	call := agentruntime.ToolCall{
		ID: "evidence-partial", Name: "mcp__pubmed__search_articles", Arguments: json.RawMessage(`{"query":"failed record"}`),
	}
	for _, status := range []string{"failed", "not_found", "missing", "invalid", "rejected", "denied", "cancelled", "timeout"} {
		t.Run("nested "+status, func(t *testing.T) {
			messages := []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
				{Role: "tool", ToolCallID: call.ID, Content: fmt.Sprintf(`{"ok":true,"result":{"items":[{"status":%q,"pmid":42}]}}`, status)},
				{Role: "assistant", Content: "PMID: 42"},
			}
			if got := unsupportedSessionRunnerCitationCount(messages, 0, "PMID: 42", nil); got != 1 {
				t.Fatalf("nested %s record citations = %d", status, got)
			}
		})
	}
	artifact := "Sample,PMID,Cell count\nA,35303250,999999999\n"
	if got := unsupportedSessionRunnerCitationCount(nil, 0, "Artifact attached.", []string{artifact}); got != 1 {
		t.Fatalf("PMID-column citation count = %d", got)
	}
	unrelated := "Sample,Cell count\nA,35303250\n\nThe PMID field was intentionally omitted."
	if got := unsupportedSessionRunnerCitationCount(nil, 0, "Artifact attached.", []string{unrelated}); got != 0 {
		t.Fatalf("unrelated numeric cells were treated as PMIDs: %d", got)
	}
	if got := unsupportedSessionRunnerCitationCount(nil, 0, "Service health: http://localhost:8765 and http://10.0.0.5/status", nil); got != 0 {
		t.Fatalf("local operational URLs were treated as scientific citations: %d", got)
	}
	mixedTables := strings.Join([]string{
		"| Finding | PMID |",
		"| --- | --- |",
		"| resistance | 35303250 |",
		"",
		"| Structure | Year |",
		"| --- | --- |",
		"| negative control | 2022 |",
	}, "\n")
	recordCall := agentruntime.ToolCall{ID: "pmid-record", Name: "mcp__pubmed__get_article_metadata"}
	evidenceMessages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{recordCall}},
		{Role: "tool", ToolCallID: recordCall.ID, Content: `{"ok":true,"result":{"pmid":"35303250","status":"verified","abstract":"substantive abstract"}}`},
	}
	if got := unsupportedSessionRunnerCitationCount(evidenceMessages, 0, "Artifact attached.", []string{mixedTables}); got != 0 {
		t.Fatalf("later Markdown table values inherited the PMID column: %d", got)
	}
}

func TestSessionRunnerCitationGroundingUsesStructuredAccessionNamespaces(t *testing.T) {
	call := agentruntime.ToolCall{ID: "accession-evidence", Name: "mcp__scientific__lookup", Arguments: json.RawMessage(`{"query":"TP53"}`)}
	evidence := `{"ok":true,"result":{"items":[{"database":"PDB","accession":"7O6M"},{"database":"UniProtKB","accession":"P04637-2"},{"clinvar_id":"VCV000013961.1"},{"rsid":"rs121913529"},{"ensembl_id":"ENSG00000141510.18"},{"refseq_ids":["XP_123456.4","YP_009724390.1","WP_012345678.1","NZ_CP000001.1"]},{"sra_id":"SRS123456"}]}}`
	candidate := "PDB 7O6M; UniProt P04637-2; ClinVar VCV000013961.1; dbSNP rs121913529; Ensembl ENSG00000141510.18; RefSeq XP_123456.4, YP_009724390.1, WP_012345678.1, NZ_CP000001.1; SRA SRS123456"
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: evidence},
		{Role: "assistant", Content: candidate},
	}
	if got := unsupportedSessionRunnerCitationCount(messages, 0, candidate, nil); got != 0 {
		t.Fatalf("structured accession references = %d", got)
	}
}

func TestSessionRunnerCitationGroundingReadsOnlySuccessfulWebFetchBodies(t *testing.T) {
	tests := []struct {
		name        string
		toolName    string
		result      string
		candidate   string
		unsupported int
	}{
		{
			name: "thin web_fetch result body is not evidence", toolName: "web_fetch",
			result:      `{"ok":true,"result":{"statusCode":200,"contentType":"text/html","body":"PMID: 34758227; DOI: 10.7754/verified"}}`,
			candidate:   "PMID: 34758227; DOI: 10.7754/verified",
			unsupported: 2,
		},
		{
			name: "thin WebFetch compatible string result is not evidence", toolName: "WebFetch",
			result:      `{"ok":true,"result":{"code":200,"result":"PMID: 34758227; DOI: 10.7754/verified","url":"https://example.org/source"}}`,
			candidate:   "PMID: 34758227; DOI: 10.7754/verified",
			unsupported: 2,
		},
		{
			name: "non-success HTTP body is not evidence", toolName: "web_fetch",
			result:    `{"ok":true,"result":{"statusCode":404,"contentType":"text/html","body":"PMID: 34758227; DOI: 10.7754/verified","url":"https://example.org/missing"}}`,
			candidate: "PMID: 34758227; DOI: 10.7754/verified; https://example.org/missing", unsupported: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := agentruntime.ToolCall{ID: "fetch-evidence", Name: test.toolName, Arguments: json.RawMessage(`{"url":"https://example.org/source"}`)}
			messages := []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
				{Role: "tool", ToolCallID: call.ID, Content: test.result},
				{Role: "assistant", Content: test.candidate},
			}
			if got := unsupportedSessionRunnerCitationCount(messages, 0, test.candidate, nil); got != test.unsupported {
				t.Fatalf("unsupported citations = %d", got)
			}
		})
	}
}

func TestSessionRunnerCitationGroundingDistinguishesStrictCrossref404FromPositiveEvidence(t *testing.T) {
	const doi = "10.1158/0008-5472.can-23-0456"
	const endpoint = "https://api.crossref.org/works/10.1158/0008-5472.can-23-0456"
	tests := []struct {
		name       string
		requestURL string
		resultURL  string
		code       int
		body       string
		candidate  string
		ok         bool
		want       []string
	}{
		{
			name: "exact 404 permits closed negative line", requestURL: endpoint, resultURL: endpoint,
			code: 404, body: "Resource not found", ok: true,
			candidate: "- DOI " + doi + " — NOT FOUND IN CROSSREF (HTTP 404 verified).",
		},
		{
			name: "404 never grounds positive prose", requestURL: endpoint, resultURL: endpoint,
			code: 404, body: "Resource not found", ok: true,
			candidate: "The resistance mechanism was established by DOI " + doi + ".",
			want:      []string{"doi:" + doi},
		},
		{
			name: "negative and positive occurrences remain unsupported", requestURL: endpoint, resultURL: endpoint,
			code: 404, body: "Resource not found", ok: true,
			candidate: "- DOI " + doi + " — NOT FOUND IN CROSSREF (HTTP 404 verified).\nThe study " + doi + " establishes resistance.",
			want:      []string{"doi:" + doi},
		},
		{
			name: "free prose negative wording is not closed contract", requestURL: endpoint, resultURL: endpoint,
			code: 404, body: "Resource not found", ok: true,
			candidate: "I could not find " + doi + " in Crossref.",
			want:      []string{"doi:" + doi},
		},
		{
			name: "mismatched result URL cannot prove absence", requestURL: endpoint,
			resultURL: "https://api.crossref.org/works/10.9999/other", code: 404, body: "Resource not found", ok: true,
			candidate: "- DOI " + doi + " — NOT FOUND IN CROSSREF (HTTP 404 verified).",
			want:      []string{"doi:" + doi},
		},
		{
			name: "allowlisted mailto does not change exact absence", requestURL: endpoint + "?mailto=example@example.org",
			resultURL: endpoint + "?mailto=example@example.org", code: 404, body: "Resource not found", ok: true,
			candidate: "- DOI " + doi + " — NOT FOUND IN CROSSREF (HTTP 404 verified).",
		},
		{
			name: "failed envelope cannot prove absence", requestURL: endpoint, resultURL: endpoint,
			code: 404, body: "Resource not found", ok: false,
			candidate: "- DOI " + doi + " — NOT FOUND IN CROSSREF (HTTP 404 verified).",
			want:      []string{"doi:" + doi},
		},
		{
			name: "exact 200 Crossref response cannot ground a scientific claim", requestURL: endpoint, resultURL: endpoint,
			code: 200, body: `{"status":"ok","message":{"DOI":"10.1158/0008-5472.CAN-23-0456"}}`, ok: true,
			candidate: "The resistance mechanism was established by DOI " + doi + ".",
			want:      []string{"doi:" + doi},
		},
		{
			name: "removed wording is not an observed Crossref fact", requestURL: endpoint, resultURL: endpoint,
			code: 404, body: "Resource not found", ok: true,
			candidate: "- DOI " + doi + " — REMOVED IN CROSSREF (HTTP 404 verified).",
			want:      []string{"doi:" + doi},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := agentruntime.ToolCall{ID: "crossref", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"` + test.requestURL + `"}`)}
			result, err := json.Marshal(map[string]any{"ok": test.ok, "result": map[string]any{
				"code": test.code, "body": test.body, "url": test.resultURL,
			}})
			if err != nil {
				t.Fatal(err)
			}
			messages := []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
				{Role: "tool", ToolCallID: call.ID, Content: string(result)},
			}
			got := unsupportedSessionRunnerCitationReferences(messages, 0, test.candidate, nil)
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("unsupported=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestSessionRunnerRejectedReferenceV1RejectsCrossref200BibliographicMismatch(t *testing.T) {
	const doi = "10.1158/0008-5472.can-23-0456"
	content := `{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"` + doi + `"},"disposition":"bibliographic_mismatch","evidence":{"provider":"crossref","tool_call_id":"crossref-mismatch","http_status":200,"observed_doi":"` + doi + `","observed_title":"Unrelated Crossref work"}}]}`
	rejected, recognized, failure := decodeSessionRunnerRejectedReferenceV1(content, "artifact:rejected.json@version-a")
	if !recognized || !strings.Contains(failure, "invalid_disposition") || len(rejected) != 0 {
		t.Fatalf("recognized=%v failure=%q rejected=%#v", recognized, failure, rejected)
	}
}

func TestSessionRunnerReferenceEvidenceLedgerUsesOnlyUniqueBoundTranscriptPairs(t *testing.T) {
	const (
		doi      = "10.1038/s41586-019-1295-z"
		endpoint = "https://api.crossref.org/works/10.1038/s41586-019-1295-z"
	)
	validCall := agentruntime.ToolCall{
		ID: "call_crossref_real", Name: "WebFetch",
		Arguments: json.RawMessage(`{"url":"` + endpoint + `"}`),
	}
	ambiguousCall := agentruntime.ToolCall{
		ID: "call_crossref_ambiguous", Name: "WebFetch",
		Arguments: json.RawMessage(`{"url":"https://api.crossref.org/works/10.1000/ambiguous"}`),
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{validCall, ambiguousCall}},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{ambiguousCall}},
		{Role: "tool", ToolCallID: validCall.ID, Content: `{"ok":true,"result":{"code":200,"url":"` + endpoint + `","body":"{\"status\":\"ok\",\"message\":{\"DOI\":\"` + doi + `\",\"title\":[\"Cryo-EM structure of the active NLRP3 inflammasome\"]}}"}}`},
		{Role: "tool", ToolCallID: ambiguousCall.ID, Content: `{"ok":true,"result":{"code":404}}`},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "call_failed", Name: "WebFetch",
			Arguments: json.RawMessage(`{"url":"https://api.crossref.org/works/10.1000/failed"}`),
		}}},
		{Role: "tool", ToolCallID: "call_failed", Content: `{"ok":false,"error":{"code":"upstream_failed"}}`},
	}
	want := `{"schema":"synon.reference_evidence_ledger.v1","crossref":[{"reference":{"kind":"doi","value":"10.1038/s41586-019-1295-z"},"provider":"crossref","tool_call_id":"call_crossref_real","http_status":200,"observed_doi":"10.1038/s41586-019-1295-z","observed_title":"Cryo-EM structure of the active NLRP3 inflammasome"}]}`
	if got := sessionRunnerReferenceEvidenceLedgerJSON(messages); got != want {
		t.Fatalf("reference evidence ledger=%s want=%s", got, want)
	}
}

func TestSessionRunnerRejectedReferenceV1Crossref200NeverGroundsPositiveScientificClaim(t *testing.T) {
	const (
		doi      = "10.1158/0008-5472.can-23-0456"
		endpoint = "https://api.crossref.org/works/10.1158/0008-5472.can-23-0456"
	)
	call := agentruntime.ToolCall{ID: "crossref-mismatch", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"` + endpoint + `"}`)}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: `{"ok":true,"result":{"code":200,"url":"` + endpoint + `","body":"{\"status\":\"ok\",\"message\":{\"DOI\":\"` + doi + `\",\"title\":[\"Unrelated Crossref work\"]}}"}}`},
	}
	claim := "The mechanism is established by DOI " + doi + "."
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(messages, 0, claim, nil, nil); !reflect.DeepEqual(got, []string{"doi:" + doi}) {
		t.Fatalf("unsupported positive claim=%#v", got)
	}
}

func TestSessionRunnerRejectedReferenceV1RejectsBibliographicMismatchBeforeEvidenceLookup(t *testing.T) {
	const doi = "10.1158/0008-5472.can-23-0456"
	content := `{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"` + doi + `"},"disposition":"bibliographic_mismatch","evidence":{"provider":"crossref","tool_call_id":"crossref-mismatch","http_status":200,"observed_doi":"` + doi + `","observed_title":"Expected observed title"}}]}`
	rejected, recognized, failure := decodeSessionRunnerRejectedReferenceV1(content, "artifact:rejected.json@version-a")
	if !recognized || !strings.Contains(failure, "invalid_disposition") || len(rejected) != 0 {
		t.Fatalf("recognized=%v failure=%q rejected=%#v", recognized, failure, rejected)
	}
}

func TestSessionRunnerRejectedReferenceV1RejectsUnknownFieldsFreeTextAndNonCanonicalDOI(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "unknown field", content: `{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"10.1000/example"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"call-a","http_status":404},"reason":"free prose"}]}`},
		{name: "free text field", content: `{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"10.1000/example"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"call-a","http_status":404,"notes":"not found"}}]}`},
		{name: "non canonical DOI", content: `{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"10.1000/EXAMPLE"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"call-a","http_status":404}}]}`},
		{name: "unknown schema version", content: `{"schema":"synon.rejected_reference.v2","records":[]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if rejected, recognized, failure := decodeSessionRunnerRejectedReferenceV1(test.content, "artifact:rejected.json@version-a"); !recognized || failure == "" || len(rejected) != 0 {
				t.Fatalf("recognized=%v failure=%q rejected=%#v", recognized, failure, rejected)
			}
		})
	}
}

func TestSessionRunnerRejectedReferenceV1AcceptsExactCrossref404NotFound(t *testing.T) {
	const (
		doi      = "10.1158/0008-5472.can-23-0456"
		endpoint = "https://api.crossref.org/works/10.1158/0008-5472.can-23-0456"
	)
	content := `{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"` + doi + `"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"crossref-not-found","http_status":404}}]}`
	rejected, recognized, failure := decodeSessionRunnerRejectedReferenceV1(content, "artifact:rejected.json@version-a")
	if !recognized || failure != "" {
		t.Fatalf("recognized=%v failure=%q", recognized, failure)
	}
	call := agentruntime.ToolCall{ID: "crossref-not-found", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"` + endpoint + `"}`)}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: `{"ok":true,"result":{"code":404,"url":"` + endpoint + `","body":"Resource not found"}}`},
	}
	candidates := &sessionRunnerArtifactCandidateReferences{
		positive: map[string]struct{}{}, negative: map[string]struct{}{}, rejected: rejected,
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(messages, 0, "Rejected-reference audit attached.", nil, candidates); len(got) != 0 {
		t.Fatalf("unsupported not-found record=%#v", got)
	}
}

func TestSessionRunnerRejectedReferenceV1AcceptsEquivalentDurableCurrentReplayEvidence(t *testing.T) {
	const (
		doi      = "10.1158/0008-5472.can-23-0456"
		endpoint = "https://api.crossref.org/works/10.1158/0008-5472.can-23-0456"
	)
	content := `{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"` + doi + `"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"crossref-replay","http_status":404}}]}`
	rejected, recognized, failure := decodeSessionRunnerRejectedReferenceV1(content, "artifact:rejected.json@version-replay")
	if !recognized || failure != "" || len(rejected) != 1 {
		t.Fatalf("recognized=%v failure=%q rejected=%#v", recognized, failure, rejected)
	}
	call := agentruntime.ToolCall{ID: "crossref-replay", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"` + endpoint + `"}`)}
	result := `{"ok":true,"result":{"code":404,"url":"` + endpoint + `","body":"Resource not found"}}`
	messages := []agentruntime.Message{}
	for range 3 {
		messages = append(messages,
			agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
			agentruntime.Message{Role: "tool", ToolCallID: call.ID, Content: result},
		)
	}
	candidates := &sessionRunnerArtifactCandidateReferences{
		positive: map[string]struct{}{}, negative: map[string]struct{}{}, rejected: rejected,
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(messages, 0, "Rejected-reference audit attached.", nil, candidates); len(got) != 0 {
		t.Fatalf("equivalent durable/current/replay evidence unsupported=%#v", got)
	}
}

func TestSessionRunnerRejectedReferenceV1UsesLatestProducedAndExplicitArtifactVersions(t *testing.T) {
	const (
		doi      = "10.1158/0008-5472.can-23-0456"
		endpoint = "https://api.crossref.org/works/10.1158/0008-5472.can-23-0456"
	)
	store := openRunnerArtifactCompletionStore(t)
	artifact, badVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-rejected-ledger", ProjectID: "project-a", Name: "rejected-references.json", Kind: "application/json",
		Content: []byte(`{"legacy_wrong_doi":"` + doi + `"}`), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, goodVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: artifact.ID, ProjectID: "project-a", Name: "rejected-references.json", Kind: "application/json",
		Content: []byte(`{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"` + doi + `"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"crossref-not-found","http_status":404}}]}`), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{
		{ArtifactID: artifact.ID, VersionID: badVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: artifact.ID, VersionID: goodVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
	}
	server := &Server{workspaceStore: store}
	session := sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	call := agentruntime.ToolCall{ID: "crossref-not-found", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"` + endpoint + `"}`)}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: `{"ok":true,"result":{"code":404,"url":"` + endpoint + `","body":"Resource not found"}}`},
	}
	candidates, err := server.sessionRunnerCompletionArtifactCandidateReferences(session, run, commits, "No artifact links.")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates.rejected) != 1 {
		t.Fatalf("latest typed artifact rejected=%#v", candidates.rejected)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(messages, 0, "No artifact links.", nil, candidates); len(got) != 0 {
		t.Fatalf("latest typed artifact unsupported=%#v", got)
	}
	candidates, err = server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run, commits, "Explicit legacy version: {{artifact:"+badVersion.ID+"}}",
	)
	if err != nil {
		t.Fatal(err)
	}
	got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(messages, 0, "No artifact links.", nil, candidates)
	if !reflect.DeepEqual(got, []string{"doi:" + doi}) {
		t.Fatalf("explicit historical artifact unsupported=%#v", got)
	}
	diagnostics := sessionRunnerReferenceDiagnostics(got, candidates)
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "rejected-references.json@"+badVersion.ID+"#line=1") {
		t.Fatalf("origin diagnostics=%#v", diagnostics)
	}
}

func TestSessionRunnerRejectedReferenceV1UsesNewestProducedReservedArtifactAcrossArtifactIDs(t *testing.T) {
	const doi = "10.1158/0008-5472.can-23-0456"
	store := openRunnerArtifactCompletionStore(t)
	oldArtifact, oldVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-rejected-old", ProjectID: "project-a",
		Name: sessionRunnerRejectedReferenceArtifactNameV1, Kind: "application/json",
		Content:   []byte(`{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"10.1000/old-invalid"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"old","http_status":404},"unexpected":true}]}`),
		CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	newArtifact, newVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-rejected-new", ProjectID: "project-a",
		Name: sessionRunnerRejectedReferenceArtifactNameV1, Kind: "application/json",
		Content:   []byte(`{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"` + doi + `"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"crossref-new","http_status":404}}]}`),
		CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{
		{ArtifactID: oldArtifact.ID, VersionID: oldVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: newArtifact.ID, VersionID: newVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
	}
	server := &Server{workspaceStore: store}
	session := sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	candidates, err := server.sessionRunnerCompletionArtifactCandidateReferences(session, run, commits, "No artifact links.")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates.contractFailures) != 0 || len(candidates.rejected) != 1 || candidates.rejected[0].reference != "doi:"+doi {
		t.Fatalf("newest reserved authority candidates=%#v", candidates)
	}
	explicit, err := server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run, commits, "Audit old immutable version: {{artifact:"+oldVersion.ID+"}}",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit.contractFailures) != 1 || len(explicit.rejected) != 1 {
		t.Fatalf("explicit old version did not fail closed: %#v", explicit)
	}
	latestArtifact, latestInvalidVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-rejected-latest-invalid", ProjectID: "project-a",
		Name: sessionRunnerRejectedReferenceArtifactNameV1, Kind: "application/json",
		Content: []byte(`{"schema":"synon.rejected_reference.v1","records":[]}`), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits = append(commits, transcriptstore.ArtifactReferenceInput{
		ArtifactID: latestArtifact.ID, VersionID: latestInvalidVersion.ID, Relation: transcriptstore.ArtifactRelationProduced,
	})
	latest, err := server.sessionRunnerCompletionArtifactCandidateReferences(session, run, commits, "No artifact links.")
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.contractFailures) != 1 || len(latest.rejected) != 0 {
		t.Fatalf("invalid latest reserved artifact fell back to old valid version: %#v", latest)
	}
}

func TestSessionRunnerRejectedReferenceV1MalformedArtifactFailsClosedWithoutProseFallback(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-invalid-rejected-ledger", ProjectID: "project-a", Name: "rejected-references.json", Kind: "application/json",
		Content: []byte(`{"schema":"synon.rejected_reference.v1","records":[{"reference":{"kind":"doi","value":"10.1000/example"},"disposition":"not_found","evidence":{"provider":"crossref","tool_call_id":"call-a","http_status":404},"reason":"free prose"}]}`), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := (&Server{workspaceStore: store}).sessionRunnerCompletionArtifactCandidateReferences(
		sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}},
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}},
		[]transcriptstore.ArtifactReferenceInput{{ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced}},
		"No artifact links.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates.contractFailures) != 1 || len(candidates.positive) != 0 || len(candidates.negative) != 0 || len(candidates.rejected) != 0 {
		t.Fatalf("contract failures=%#v positive=%#v negative=%#v rejected=%#v", candidates.contractFailures, candidates.positive, candidates.negative, candidates.rejected)
	}
	if !strings.Contains(candidates.contractFailures[0], "rejected-references.json@"+version.ID+"#invalid_document") {
		t.Fatalf("contract failure origin=%#v", candidates.contractFailures)
	}
}

func TestSessionRunnerRejectedReferenceV1ReservedArtifactNameRequiresTypedDocument(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-reserved-invalid-rejected-ledger", ProjectID: "project-a",
		Name: sessionRunnerRejectedReferenceArtifactNameV1, Kind: "application/json",
		Content:   []byte(`[{"identifier":"DOI 10.1000/example","disposition":"bibliographic_mismatch"}]`),
		CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := (&Server{workspaceStore: store}).sessionRunnerCompletionArtifactCandidateReferences(
		sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}},
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}},
		[]transcriptstore.ArtifactReferenceInput{{
			ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
		}},
		"No artifact links.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates.contractFailures) != 1 || len(candidates.positive) != 0 ||
		len(candidates.negative) != 0 || len(candidates.rejected) != 0 {
		t.Fatalf("contract failures=%#v positive=%#v negative=%#v rejected=%#v",
			candidates.contractFailures, candidates.positive, candidates.negative, candidates.rejected)
	}
	wantOrigin := sessionRunnerRejectedReferenceArtifactNameV1 + "@" + version.ID + "#missing_schema"
	if !strings.Contains(candidates.contractFailures[0], wantOrigin) {
		t.Fatalf("contract failure=%#v want origin %q", candidates.contractFailures, wantOrigin)
	}
}

func TestSessionRunnerCitationGroundingUsesExactClinicalTrialsRecords(t *testing.T) {
	const (
		nct      = "NCT04015076"
		endpoint = "https://clinicaltrials.gov/api/v2/studies/NCT04015076"
	)
	tests := []struct {
		name       string
		requestURL string
		resultURL  string
		code       int
		body       string
		candidate  string
		want       []string
	}{
		{
			name: "exact 200 record grounds its NCT", requestURL: endpoint, resultURL: endpoint, code: 200,
			body:      `{"protocolSection":{"identificationModule":{"nctId":"NCT04015076","briefTitle":"Inzomelid in CAPS"}}}`,
			candidate: "ClinicalTrials.gov record NCT04015076 is registered.",
		},
		{
			name: "exact 404 permits closed negative line", requestURL: endpoint, resultURL: endpoint, code: 404,
			body:      `{"message":"study not found"}`,
			candidate: "- NCT NCT04015076 — NOT FOUND IN CLINICALTRIALS.GOV (HTTP 404 verified).",
		},
		{
			name: "404 never grounds positive prose", requestURL: endpoint, resultURL: endpoint, code: 404,
			body:      `{"message":"study not found"}`,
			candidate: "The trial NCT04015076 completed successfully.",
			want:      []string{"nct:" + nct},
		},
		{
			name: "free prose absence is not the closed contract", requestURL: endpoint, resultURL: endpoint, code: 404,
			body:      `{"message":"study not found"}`,
			candidate: "NCT04015076 could not be found.",
			want:      []string{"nct:" + nct},
		},
		{
			name: "mismatched record cannot ground requested NCT", requestURL: endpoint, resultURL: endpoint, code: 200,
			body:      `{"protocolSection":{"identificationModule":{"nctId":"NCT04382053"}}}`,
			candidate: "ClinicalTrials.gov record NCT04015076 is registered.",
			want:      []string{"nct:" + nct},
		},
		{
			name: "mismatched result URL fails closed", requestURL: endpoint,
			resultURL: "https://clinicaltrials.gov/api/v2/studies/NCT04382053", code: 200,
			body:      `{"protocolSection":{"identificationModule":{"nctId":"NCT04015076"}}}`,
			candidate: "ClinicalTrials.gov record NCT04015076 is registered.",
			want:      []string{"nct:" + nct},
		},
		{
			name: "query parameters are not an exact record contract", requestURL: endpoint + "?format=json", resultURL: endpoint + "?format=json", code: 200,
			body:      `{"protocolSection":{"identificationModule":{"nctId":"NCT04015076"}}}`,
			candidate: "ClinicalTrials.gov record NCT04015076 is registered.",
			want:      []string{"nct:" + nct},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := agentruntime.ToolCall{ID: "clinical-trial", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"` + test.requestURL + `"}`)}
			result, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
				"code": test.code, "body": test.body, "url": test.resultURL,
			}})
			if err != nil {
				t.Fatal(err)
			}
			messages := []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
				{Role: "tool", ToolCallID: call.ID, Content: string(result)},
			}
			got := unsupportedSessionRunnerCitationReferences(messages, 0, test.candidate, nil)
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("unsupported=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestSessionRunnerCitationGroundingReadsNestedMCPStructuredResults(t *testing.T) {
	const (
		requestedOnly = "CHEMBL1111111"
		returned      = "CHEMBL3183703"
	)
	call := agentruntime.ToolCall{
		ID:        "chembl-compound",
		Name:      "mcp__chembl__get_compound_details",
		Arguments: json.RawMessage(`{"chembl_id":"` + requestedOnly + `","limit":1}`),
	}
	result, err := json.Marshal(map[string]any{
		"ok":     true,
		"result": `{"count":1,"compounds":[{"molecule_chembl_id":"` + returned + `","pref_name":"MCC950","molecule_hierarchy":{"active_chembl_id":"` + returned + `"}}]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: string(result)},
	}
	if got := unsupportedSessionRunnerCitationReferences(messages, 0, "Verified compound "+returned+".", nil); len(got) != 0 {
		t.Fatalf("returned structured identifier unsupported=%#v", got)
	}
	if got := unsupportedSessionRunnerCitationReferences(messages, 0, "Requested compound "+requestedOnly+".", nil); !reflect.DeepEqual(got, []string{"accession:chembl:" + requestedOnly}) {
		t.Fatalf("request echo must not become evidence: %#v", got)
	}
}

func TestSessionRunnerCitationGroundingUsesMCPStructuredNotFoundOnlyForNegativeClaims(t *testing.T) {
	const missing = "NCT04303782"
	call := agentruntime.ToolCall{
		ID:        "clinical-trial-not-found",
		Name:      "mcp__clinical-trials__get_trial_details",
		Arguments: json.RawMessage(`{"nct_id":"` + missing + `"}`),
	}
	result, err := json.Marshal(map[string]any{
		"ok":     true,
		"result": `{"found":false,"nct_id":"` + missing + `","error":"Trial not found"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: string(result)},
	}

	negativeReport := "## " + missing + " 状态核查\n" +
		"经 ClinicalTrials.gov 官方接口查询，未检索到 " + missing + " 对应的注册试验记录。"
	if got := unsupportedSessionRunnerCitationReferences(messages, 0, negativeReport, nil); len(got) != 0 {
		t.Fatalf("verified MCP absence was rejected=%#v", got)
	}

	positiveReport := "## " + missing + " 状态核查\n" + missing + " 是一项正在招募的注册试验。"
	if got := unsupportedSessionRunnerCitationReferences(messages, 0, positiveReport, nil); !reflect.DeepEqual(got, []string{"nct:" + missing}) {
		t.Fatalf("MCP absence grounded a positive claim=%#v", got)
	}
	positiveHeading := "## " + missing + " 正在招募"
	if got := unsupportedSessionRunnerCitationReferences(messages, 0, positiveHeading, nil); !reflect.DeepEqual(got, []string{"nct:" + missing}) {
		t.Fatalf("positive heading was treated as a neutral label=%#v", got)
	}
}

func TestSessionRunnerCitationGroundingRejectsAmbiguousNegativeLanguage(t *testing.T) {
	const missing = "NCT04303782"
	call := agentruntime.ToolCall{ID: "clinical-trial-not-found", Name: "mcp__clinical-trials__get_trial_details"}
	result := `{"ok":true,"result":"{\"found\":false,\"nct_id\":\"` + missing + `\"}"}`
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: result},
	}
	claim := "现有信息不足以确认 " + missing + " 不存在。"
	if got := unsupportedSessionRunnerCitationReferences(messages, 0, claim, nil); !reflect.DeepEqual(got, []string{"nct:" + missing}) {
		t.Fatalf("ambiguous absence wording was accepted=%#v", got)
	}
}

func TestSessionRunnerCitationGroundingReadsBoundedPubMedEUtilsResponses(t *testing.T) {
	tests := []struct {
		name          string
		toolName      string
		url           string
		body          string
		candidate     string
		omitResultURL bool
		resultURL     string
		want          int
	}{
		{
			name:      "efetch XML",
			url:       "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/efetch.fcgi?db=pubmed&id=33971321,34096690&retmode=xml",
			body:      `<PubmedArticleSet><PubmedArticle><MedlineCitation><PMID Version="1">33971321</PMID></MedlineCitation><PubmedData><ArticleIdList><ArticleId IdType="doi">10.1000/verified-a</ArticleId></ArticleIdList></PubmedData></PubmedArticle><PubmedArticle><MedlineCitation><PMID Version="1">34096690</PMID><Article><ELocationID EIdType="doi">10.1000/verified-b</ELocationID></Article></MedlineCitation></PubmedArticle></PubmedArticleSet>`,
			candidate: "PMIDs: 33971321, 34096690; DOIs: 10.1000/verified-a, 10.1000/verified-b",
		},
		{
			name:      "truncated esummary with complete uids prefix",
			url:       "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esummary.fcgi?db=pubmed&id=35658005,36764316&retmode=json",
			body:      `{"header":{"type":"esummary"},"result":{"uids":["35658005","36764316"],"35658005":{"uid":"35658005"},"36764316":{"uid":"36764316","title":"truncated`,
			candidate: "PMIDs: 35658005, 36764316",
		},
		{
			name:      "requested id absent from response",
			url:       "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esummary.fcgi?db=pubmed&id=35658005,36764316&retmode=json",
			body:      `{"result":{"uids":["35658005"],"35658005":{"uid":"35658005"}}}`,
			candidate: "PMIDs: 35658005, 36764316",
			want:      1,
		},
		{
			name:      "complete esummary binds DOI to requested UID",
			url:       "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esummary.fcgi?db=pubmed&id=35658005&retmode=json",
			body:      `{"result":{"uids":["35658005"],"35658005":{"uid":"35658005","elocationid":"doi: 10.1000/summary-record"}}}`,
			candidate: "PMID: 35658005; DOI: 10.1000/summary-record",
		},
		{
			name:      "esearch discovery is not verification",
			url:       "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi?db=pubmed&id=35658005&retmode=json",
			body:      `{"esearchresult":{"idlist":["35658005"]}}`,
			candidate: "PMID: 35658005",
			want:      1,
		},
		{
			name:      "unexpected response PMID cannot ground DOI",
			url:       "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/efetch.fcgi?db=pubmed&id=35658005&retmode=xml",
			body:      `<PubmedArticleSet><PubmedArticle><MedlineCitation><PMID>99999999</PMID></MedlineCitation><PubmedData><ArticleIdList><ArticleId IdType="doi">10.1000/mismatch</ArticleId></ArticleIdList></PubmedData></PubmedArticle></PubmedArticleSet>`,
			candidate: "DOI: 10.1000/mismatch",
			want:      1,
		},
		{
			name:      "unassociated DOI fragment is not grounded",
			url:       "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/efetch.fcgi?db=pubmed&id=35658005&retmode=xml",
			body:      `<PubmedArticleSet><PubmedArticle><MedlineCitation><PMID>35658005</PMID></MedlineCitation></PubmedArticle><MalformedFragment><ArticleId IdType="doi">10.1000/unassociated</ArticleId></MalformedFragment></PubmedArticleSet>`,
			candidate: "DOI: 10.1000/unassociated",
			want:      1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := agentruntime.ToolCall{ID: "eutils", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"` + test.url + `"}`)}
			result, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
				"code": 200, "result": test.body, "url": test.url,
			}})
			if err != nil {
				t.Fatal(err)
			}
			messages := []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
				{Role: "tool", ToolCallID: call.ID, Content: string(result)},
			}
			if got := unsupportedSessionRunnerCitationCount(messages, 0, test.candidate, nil); got != test.want {
				t.Fatalf("unsupported citations = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSessionRunnerCitationGroundingReadsVerifiedEuropePMCExactResponses(t *testing.T) {
	tests := []struct {
		name          string
		toolName      string
		url           string
		body          string
		candidate     string
		omitResultURL bool
		resultURL     string
		want          int
	}{
		{
			name:      "exact batch",
			url:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704+OR+EXT_ID%3A33824136&resultType=lite&pageSize=2&format=json",
			body:      `{"version":"6.9","hitCount":2,"request":{"queryString":"EXT_ID:34161704 OR EXT_ID:33824136"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","doi":"10.1056/nejmoa2105281"},{"id":"33824136","source":"MED","pmid":"33824136","doi":"10.1158/2159-8290.cd-21-0365"}]}}`,
			candidate: "PMIDs: 34161704, 33824136; DOIs: 10.1056/NEJMoa2105281, 10.1158/2159-8290.CD-21-0365",
		},
		{
			name:          "lowercase tool exact query without result URL fails closed",
			toolName:      "web_fetch",
			url:           "https://www.ebi.ac.uk./europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json",
			body:          `{"version":"6.9","hitCount":1,"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704"}]}}`,
			candidate:     "PMID: 34161704",
			omitResultURL: true,
			want:          1,
		},
		{
			name:          "lowercase generic query without result URL stays unsupported",
			toolName:      "web_fetch",
			url:           "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=KRAS+G12C+resistance&resultType=lite&pageSize=1&format=json",
			body:          `{"version":"6.9","hitCount":1,"request":{"queryString":"KRAS G12C resistance"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","abstractText":"PMID: 34161704"}]}}`,
			candidate:     "PMID: 34161704",
			omitResultURL: true,
			want:          1,
		},
		{
			name:      "HTTP strict endpoint fails closed",
			url:       "http://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json",
			body:      `{"version":"6.9","hitCount":1,"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","abstractText":"PMID: 34161704"}]}}`,
			candidate: "PMID: 34161704",
			want:      1,
		},
		{
			name:      "userinfo strict endpoint fails closed",
			url:       "https://user@www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json",
			body:      `{"version":"6.9","hitCount":1,"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","abstractText":"PMID: 34161704"}]}}`,
			candidate: "PMID: 34161704",
			want:      1,
		},
		{
			name:      "requested id absent from response",
			url:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704+OR+EXT_ID%3A33824136&resultType=lite&pageSize=2&format=json",
			body:      `{"version":"6.9","hitCount":1,"request":{"queryString":"EXT_ID:34161704 OR EXT_ID:33824136"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704"}]}}`,
			candidate: "PMIDs: 34161704, 33824136",
			want:      1,
		},
		{
			name:      "generic discovery query is not verification",
			url:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=KRAS+G12C+resistance&resultType=lite&pageSize=2&format=json",
			body:      `{"version":"6.9","hitCount":1,"request":{"queryString":"KRAS G12C resistance"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","abstractText":"PMID: 34161704"}]}}`,
			candidate: "PMID: 34161704",
			want:      1,
		},
		{
			name:      "response query mismatch",
			url:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json",
			body:      `{"version":"6.9","hitCount":1,"request":{"queryString":"EXT_ID:33824136"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","abstractText":"PMID: 34161704"}]}}`,
			candidate: "PMID: 34161704",
			want:      1,
		},
		{
			name:      "result URL mismatch fails closed",
			url:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json",
			resultURL: "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A33824136&resultType=lite&pageSize=1&format=json",
			body:      `{"version":"6.9","hitCount":1,"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","abstractText":"PMID: 34161704"}]}}`,
			candidate: "PMID: 34161704",
			want:      1,
		},
		{
			name:      "invalid result URL fails closed",
			url:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json",
			resultURL: "://invalid",
			body:      `{"version":"6.9","hitCount":1,"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","abstractText":"PMID: 34161704"}]}}`,
			candidate: "PMID: 34161704",
			want:      1,
		},
		{
			name:      "non MED source is not a PMID record",
			url:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json",
			body:      `{"version":"6.9","hitCount":1,"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"PPR","pmid":"34161704","abstractText":"PMID: 34161704"}]}}`,
			candidate: "PMID: 34161704",
			want:      1,
		},
		{
			name:      "malformed response is not accepted",
			url:       "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json",
			body:      `{"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","abstractText":"PMID: 34161704"}]`,
			candidate: "PMID: 34161704",
			want:      1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			toolName := test.toolName
			if toolName == "" {
				toolName = "WebFetch"
			}
			call := agentruntime.ToolCall{ID: "europe-pmc", Name: toolName, Arguments: json.RawMessage(`{"url":"` + test.url + `"}`)}
			resultValue := map[string]any{"code": 200, "result": test.body}
			if normalizeAgentToolName(toolName) == "webfetch" && toolName == "web_fetch" {
				resultValue = map[string]any{"statusCode": 200, "contentType": "application/json", "body": test.body}
			}
			if !test.omitResultURL {
				resultURL := test.resultURL
				if resultURL == "" {
					resultURL = test.url
				}
				resultValue["url"] = resultURL
			}
			result, err := json.Marshal(map[string]any{"ok": true, "result": resultValue})
			if err != nil {
				t.Fatal(err)
			}
			messages := []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
				{Role: "tool", ToolCallID: call.ID, Content: string(result)},
			}
			if got := unsupportedSessionRunnerCitationCount(messages, 0, test.candidate, nil); got != test.want {
				t.Fatalf("unsupported citations = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSessionRunnerCitationGroundingRejectsEuropePMCDOIWithoutValidatedRecord(t *testing.T) {
	const (
		exactURL = "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A34161704&resultType=lite&pageSize=1&format=json"
		doi      = "10.1056/NEJMoa2105281"
	)
	tests := []struct {
		name          string
		toolName      string
		url           string
		resultURL     string
		omitResultURL bool
		body          string
	}{
		{
			name:          "lowercase generic query",
			toolName:      "web_fetch",
			url:           "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=KRAS+G12C&resultType=lite&pageSize=1&format=json",
			omitResultURL: true,
			body:          `{"request":{"queryString":"KRAS G12C"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","doi":"10.1056/NEJMoa2105281","abstractText":"DOI: 10.1056/NEJMoa2105281"}]}}`,
		},
		{
			name: "PPR is not a PubMed record", url: exactURL,
			body: `{"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"PPR","pmid":"34161704","doi":"10.1056/NEJMoa2105281"}]}}`,
		},
		{
			name: "response query mismatch", url: exactURL,
			body: `{"request":{"queryString":"EXT_ID:33824136"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","doi":"10.1056/NEJMoa2105281"}]}}`,
		},
		{
			name: "malformed response", url: exactURL,
			body: `{"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","doi":"10.1056/NEJMoa2105281"}]`,
		},
		{
			name: "result URL mismatch", url: exactURL,
			resultURL: "https://www.ebi.ac.uk/europepmc/webservices/rest/search?query=EXT_ID%3A33824136&resultType=lite&pageSize=1&format=json",
			body:      `{"request":{"queryString":"EXT_ID:34161704"},"resultList":{"result":[{"id":"34161704","source":"MED","pmid":"34161704","doi":"10.1056/NEJMoa2105281"}]}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			toolName := test.toolName
			if toolName == "" {
				toolName = "WebFetch"
			}
			call := agentruntime.ToolCall{ID: "europe-pmc-doi", Name: toolName, Arguments: json.RawMessage(`{"url":"` + test.url + `"}`)}
			resultValue := map[string]any{"code": 200, "result": test.body}
			if toolName == "web_fetch" {
				resultValue = map[string]any{"statusCode": 200, "contentType": "application/json", "body": test.body}
			}
			if !test.omitResultURL {
				resultURL := test.resultURL
				if resultURL == "" {
					resultURL = test.url
				}
				resultValue["url"] = resultURL
			}
			result, err := json.Marshal(map[string]any{"ok": true, "result": resultValue})
			if err != nil {
				t.Fatal(err)
			}
			messages := []agentruntime.Message{
				{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
				{Role: "tool", ToolCallID: call.ID, Content: string(result)},
			}
			if got := unsupportedSessionRunnerCitationCount(messages, 0, "DOI: "+doi, nil); got != 1 {
				t.Fatalf("unsupported DOI count = %d, want 1", got)
			}
		})
	}
}

func TestSessionRunnerCitationGroundingReadsVerifiedRCSBEntryResponses(t *testing.T) {
	call := agentruntime.ToolCall{ID: "rcsb", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"https://data.rcsb.org/rest/v1/core/entry/7OCJ"}`)}
	result := `{"ok":true,"result":{"code":200,"result":"{\"entry\":{\"id\":\"7OCJ\"}}","url":"https://data.rcsb.org/rest/v1/core/entry/7OCJ"}}`
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: result},
	}
	candidate := strings.Join([]string{
		"| Claimed PDB ID | Authority record | Verdict |",
		"| --- | --- | --- |",
		"| 7OCJ | https://data.rcsb.org/rest/v1/core/entry/7OCJ | removed after checking the record |",
	}, "\n")
	if got := unsupportedSessionRunnerCitationCount(messages, 0, candidate, nil); got != 0 {
		t.Fatalf("verified RCSB entry was not grounded: %d", got)
	}
	mismatch := strings.ReplaceAll(result, `\"7OCJ\"`, `\"7OIH\"`)
	messages[1].Content = mismatch
	if got := unsupportedSessionRunnerCitationCount(messages, 0, candidate, nil); got != 1 {
		t.Fatalf("mismatched RCSB entry grounded the requested PDB ID: %d", got)
	}
}

func TestSessionRunnerCitationGroundingReadsVerifiedRCSBLinkedReferences(t *testing.T) {
	entryCall := agentruntime.ToolCall{ID: "rcsb-entry", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"https://data.rcsb.org/rest/v1/core/entry/5DE2"}`)}
	entryResult := `{"ok":true,"result":{"code":200,"result":"{\"entry\":{\"id\":\"5DE2\"},\"citation\":[{\"id\":\"primary\",\"rcsb_is_primary\":\"Y\",\"pdbx_database_id_DOI\":\"10.1038/ncomms9771\"}]}","url":"https://data.rcsb.org/rest/v1/core/entry/5DE2"}}`
	entityCall := agentruntime.ToolCall{ID: "rcsb-entity", Name: "WebFetch", Arguments: json.RawMessage(`{"url":"https://data.rcsb.org/rest/v1/core/polymer_entity/5DE2/1"}`)}
	entityResult := `{"ok":true,"result":{"code":200,"result":"{\"rcsb_polymer_entity_container_identifiers\":{\"entry_id\":\"5DE2\",\"entity_id\":\"1\",\"rcsb_id\":\"5DE2_1\",\"uniprot_ids\":[\"Q8TDX7\"],\"reference_sequence_identifiers\":[{\"database_accession\":\"Q8TDX7\",\"database_name\":\"UniProt\",\"provenance_source\":\"SIFTS\"}]}}","url":"https://data.rcsb.org/rest/v1/core/polymer_entity/5DE2/1"}}`
	messages := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{entryCall}},
		{Role: "tool", ToolCallID: entryCall.ID, Content: entryResult},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{entityCall}},
		{Role: "tool", ToolCallID: entityCall.ID, Content: entityResult},
	}
	candidate := "PDB 5DE2 cites DOI 10.1038/ncomms9771 and maps UniProt Q8TDX7 through SIFTS."
	if got := unsupportedSessionRunnerCitationCount(messages, 0, candidate, nil); got != 0 {
		t.Fatalf("verified RCSB linked references were not grounded: %d", got)
	}

	messages[3].Content = strings.Replace(entityResult, `\"entity_id\":\"1\"`, `\"entity_id\":\"2\"`, 1)
	if got := unsupportedSessionRunnerCitationCount(messages, 0, candidate, nil); got != 1 {
		t.Fatalf("mismatched RCSB polymer entity grounded UniProt reference: %d", got)
	}
	messages[3].Content = strings.Replace(entityResult, `\"provenance_source\":\"SIFTS\"`, `\"provenance_source\":\"Author\"`, 1)
	if got := unsupportedSessionRunnerCitationCount(messages, 0, candidate, nil); got != 1 {
		t.Fatalf("non-SIFTS RCSB mapping grounded UniProt reference: %d", got)
	}
}

func TestSessionRunnerCitationGroundingReadsPDBIdentifierLists(t *testing.T) {
	ids := []string{"7OCJ", "7OIH", "7SUS"}
	messages := make([]agentruntime.Message, 0, len(ids)*2)
	for _, id := range ids {
		call := agentruntime.ToolCall{
			ID: "rcsb-" + id, Name: "WebFetch",
			Arguments: json.RawMessage(`{"url":"https://data.rcsb.org/rest/v1/core/entry/` + id + `"}`),
		}
		body, err := json.Marshal(map[string]any{"entry": map[string]any{"id": id}})
		if err != nil {
			t.Fatal(err)
		}
		resultJSON, err := json.Marshal(map[string]any{"ok": true, "result": map[string]any{
			"code": 200, "result": string(body), "url": "https://data.rcsb.org/rest/v1/core/entry/" + id,
		}})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages,
			agentruntime.Message{Role: "assistant", ToolCalls: []agentruntime.ToolCall{call}},
			agentruntime.Message{Role: "tool", ToolCallID: call.ID, Content: string(resultJSON)},
		)
	}
	for _, candidate := range []string{
		"PDB IDs 7OCJ/7OIH/7SUS were checked and removed as unrelated structures.",
		"PDB IDs 7OCJ, 7OIH, and 7SUS were checked and removed as unrelated structures.",
		"PDB structural entries (7OCJ, 7OIH, 7SUS) were checked and removed as unrelated structures.",
	} {
		if got := unsupportedSessionRunnerCitationCount(messages, 0, candidate, nil); got != 0 {
			t.Fatalf("verified PDB identifier list %q was not grounded: %d", candidate, got)
		}
		if got := unsupportedSessionRunnerCitationCount(messages[:len(messages)-2], 0, candidate, nil); got != 1 {
			t.Fatalf("missing RCSB evidence for one identifier in %q produced unsupported=%d", candidate, got)
		}
	}
}

func TestSessionRunnerReferenceDiagnosticsAreSortedAndByteBounded(t *testing.T) {
	references := []string{"nct:NCT09999999", "doi:10.9999/z", "url:https://example.org/" + strings.Repeat("a", 4096)}
	detail := formatSessionRunnerReferenceDiagnostics(references)
	if len(detail) > 2048 || !strings.Contains(detail, "...") || !strings.HasPrefix(detail, "doi:10.9999/z") {
		t.Fatalf("bounded diagnostics = %q (%d bytes)", detail, len(detail))
	}
}

func TestSessionRunnerLegacyArtifactReferenceValidationIsProjectScoped(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifactA, versionA, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-a", ProjectID: "project-a", Name: "a.md", Kind: "markdown", Content: []byte("a"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, versionB, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-b", ProjectID: "project-b", Name: "b.md", Kind: "markdown", Content: []byte("b"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store}
	session := sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}
	content := strings.Join([]string{
		"{{artifact:" + versionA.ID + "}}",
		"{{artifact:art_" + artifactA.ID + "}}",
		"{{artifact:" + versionB.ID + "}}",
		"{{artifact:...}}",
		"{{artifact:...}}",
	}, " ")
	unresolved, err := server.unresolvedSessionRunnerArtifactReferenceCount(session, nil, nil, content)
	if err != nil {
		t.Fatal(err)
	}
	if unresolved != 2 {
		t.Fatalf("unresolved references = %d", unresolved)
	}
}

func TestSessionRunnerFinalAnswerRejectsInternalWorkingDataReference(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-internal", ProjectID: "project-a", Name: "validation_record.json",
		Kind: "application/json", Content: []byte(`{"ok":true}`), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetArtifactRetentionMode(context.Background(), artifact.ID, "project-a", "local", "working_data"); err != nil {
		t.Fatal(err)
	}
	unresolved, err := (&Server{workspaceStore: store}).unresolvedSessionRunnerArtifactReferenceCount(
		sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}, nil, nil,
		"[internal validation]({{artifact:"+version.ID+"}})",
	)
	if err != nil || unresolved != 1 {
		t.Fatalf("internal reference unresolved=%d err=%v", unresolved, err)
	}
}

func TestSessionRunnerCanonicalArtifactReferencesRequireCurrentAttemptPublishingResult(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifactA, versionA, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-current", ProjectID: "project-a", Name: "current.md",
		Kind: "markdown", Content: []byte("current"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, versionB, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-stale", ProjectID: "project-a", Name: "stale.md",
		Kind: "markdown", Content: []byte("stale"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	content := strings.Join([]string{
		"{{artifact:" + versionA.ID + "}}",
		"{{artifact:" + versionB.ID + "}}",
		"{{artifact:art_" + artifactA.ID + "}}",
	}, " ")
	unresolved, err := (&Server{workspaceStore: store}).unresolvedSessionRunnerArtifactReferenceCount(
		sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}},
		run,
		[]transcriptstore.ArtifactReferenceInput{{
			ArtifactID: artifactA.ID, VersionID: versionA.ID, Relation: transcriptstore.ArtifactRelationProduced,
		}},
		content,
	)
	if err != nil {
		t.Fatal(err)
	}
	if unresolved != 2 {
		t.Fatalf("unresolved references = %d", unresolved)
	}
	run.ContinuationArtifactReferences = []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: "artifact-stale", VersionID: versionB.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	carriedCommits := append([]transcriptstore.ArtifactReferenceInput{}, run.ContinuationArtifactReferences...)
	carriedCommits = append(carriedCommits, transcriptstore.ArtifactReferenceInput{
		ArtifactID: artifactA.ID, VersionID: versionA.ID, Relation: transcriptstore.ArtifactRelationProduced,
	})
	unresolved, err = (&Server{workspaceStore: store}).unresolvedSessionRunnerArtifactReferenceCount(
		sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}, run, carriedCommits, content,
	)
	if err != nil || unresolved != 1 {
		t.Fatalf("same logical task carried references unresolved=%d err=%v", unresolved, err)
	}
}

func TestLatestSessionRunnerArtifactReferencesKeepsOnlyCurrentArtifactHead(t *testing.T) {
	refs := []transcriptstore.ArtifactReferenceInput{
		{ArtifactID: "artifact-a", VersionID: "version-a1", Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: "artifact-b", VersionID: "version-b1", Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: "artifact-a", VersionID: "version-a2", Relation: transcriptstore.ArtifactRelationProduced},
	}
	got := latestSessionRunnerArtifactReferences(refs)
	if len(got) != 2 || got[0].ArtifactID != "artifact-a" || got[0].VersionID != "version-a2" ||
		got[1].ArtifactID != "artifact-b" || got[1].VersionID != "version-b1" {
		t.Fatalf("latest artifact heads = %#v", got)
	}
}

func TestSessionRunnerCitationGroundingScansReferencedImmutableTextArtifact(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-evidence", ProjectID: "project-a", Name: "evidence.csv", Kind: "text/csv",
		Content: []byte("claim,doi\nunsupported,10.4242/not-verified\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	texts, err := (&Server{workspaceStore: store}).sessionRunnerReferencedArtifactTexts(
		sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}},
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}},
		commits,
		"[evidence.csv]({{artifact:"+version.ID+"}})",
	)
	if err != nil || len(texts) != 1 {
		t.Fatalf("texts=%#v err=%v", texts, err)
	}
	if got := unsupportedSessionRunnerCitationCount(nil, 0, "Artifact attached.", texts); got != 1 {
		t.Fatalf("unsupported artifact citations = %d", got)
	}
}

func TestSessionRunnerCitationGroundingDoesNotTreatRawExecutionLogAsModelClaim(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-execution-log", ProjectID: "project-a", Name: "vina.log", Kind: "text/x-log",
		Content: []byte("tool banner doi:10.4242/tool-docs https://example.org/tool\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	texts, err := (&Server{workspaceStore: store}).sessionRunnerReferencedArtifactTexts(
		sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}},
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}},
		commits,
		"[vina.log]({{artifact:"+version.ID+"}})",
	)
	if err != nil || len(texts) != 0 {
		t.Fatalf("raw execution log texts=%#v err=%v", texts, err)
	}
}

func TestSessionRunnerCitationGroundingDoesNotTreatMachineScientificDataMetadataAsModelClaim(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	structure, structureVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-structure", ProjectID: "project-a", Name: "8PWC.cif", Kind: "text/plain",
		Content: []byte("_entry.id 8PWC\n_citation.pdbx_database_id_DOI 10.1158/1535-7163.MCT-23-0783\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store}
	session := sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	structureCommit := transcriptstore.ArtifactReferenceInput{
		ArtifactID: structure.ID, VersionID: structureVersion.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}
	candidates, err := server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run, []transcriptstore.ArtifactReferenceInput{structureCommit}, "Structure attached.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Structure attached.", nil, candidates,
	); len(got) != 0 {
		t.Fatalf("machine scientific metadata was treated as narrative citation: %#v", got)
	}

	report, reportVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-report", ProjectID: "project-a", Name: "report.md", Kind: "text/markdown",
		Content: []byte("Structure PDB 8PWC; DOI 10.1158/1535-7163.MCT-23-0783.\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err = server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run,
		[]transcriptstore.ArtifactReferenceInput{
			structureCommit,
			{ArtifactID: report.ID, VersionID: reportVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
		},
		"Report attached.",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"accession:pdb:8PWC", "doi:10.1158/1535-7163.mct-23-0783"}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Report attached.", nil, candidates,
	); !reflect.DeepEqual(got, want) {
		t.Fatalf("narrative report citation gate got=%#v want=%#v", got, want)
	}
}

func TestSessionRunnerCitationGroundingDoesNotTreatAuthoritativeScientificDataMetadataAsModelClaim(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	_, source, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: "scientific-source-payload-checkpoint",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"download_public_scientific_file"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, version, err := fixture.store.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(context.Background(), "scientific-source-payload-artifact"),
		workspace.WriteArtifactVersionInput{
			ArtifactID: "artifact-authoritative-yaml", ProjectID: fixture.stream.ProjectID,
			Name: "authoritative_mechanism.yaml", ContentType: "application/yaml",
			Content:  strings.NewReader("description: authoritative upstream metadata\nsource: http://www.gri.org/\n"),
			MaxBytes: 1 << 20, CreatedBy: fixture.claim.RunnerID,
			RootFrameID: fixture.stream.RootFrameID, FrameID: fixture.stream.FrameID,
			TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
				StreamUID: fixture.stream.UID, RunnerID: fixture.claim.RunnerID,
				ClaimToken: fixture.claim.ClaimToken, Attempt: fixture.claim.Attempt,
				SourceEventID: source.EventID, Relation: string(transcriptstore.ArtifactRelationProduced),
			},
			Language: agentPublicScientificArtifactLanguage,
		},
		fixture.stream.OwnerID,
	)
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{{
		ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
	}}
	candidates, err := fixture.server.sessionRunnerCompletionArtifactCandidateReferences(
		sessionstore.Session{Project: &sessionstore.Project{ID: fixture.stream.ProjectID}},
		&sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}},
		commits,
		"Authoritative mechanism: {{artifact:"+version.ID+"}}",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Authoritative mechanism attached.", nil, candidates,
	); len(got) != 0 {
		t.Fatalf("authoritative scientific source metadata was treated as a model citation: %#v", got)
	}
}

func TestSessionRunnerCitationGroundingScansEveryFinalProducedTextArtifact(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifactA, versionA1, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-a", ProjectID: "project-a", Name: "draft.md", Kind: "text/markdown",
		Content: []byte("provisional structure PDB: 6NP0\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, versionB, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-b", ProjectID: "project-a", Name: "final.md", Kind: "text/markdown",
		Content: []byte("unsupported structure PDB: 7L9V\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, versionA2, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: artifactA.ID, ProjectID: "project-a", Name: "draft.md", Kind: "text/markdown",
		Content: []byte("corrected draft without identifiers\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{
		{ArtifactID: artifactA.ID, VersionID: versionA1.ID, Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: "artifact-b", VersionID: versionB.ID, Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: artifactA.ID, VersionID: versionA2.ID, Relation: transcriptstore.ArtifactRelationProduced},
	}
	server := &Server{workspaceStore: store}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	session := sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}
	candidates, err := server.sessionRunnerCompletionArtifactCandidateReferences(session, run, commits, "No artifact links.")
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(nil, 0, "No artifact links.", nil, candidates); !reflect.DeepEqual(got, []string{"accession:pdb:7L9V"}) {
		t.Fatalf("final produced artifact citations=%#v", got)
	}
	candidates, err = server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run, commits, "Explicit immutable draft: {{artifact:"+versionA1.ID+"}}",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(nil, 0, "No artifact links.", nil, candidates); !reflect.DeepEqual(got, []string{"accession:pdb:6NP0", "accession:pdb:7L9V"}) {
		t.Fatalf("explicit historical artifact citations=%#v", got)
	}
}

func TestSessionRunnerCitationGroundingIgnoresEmbeddedDataURIBytes(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifact, embeddedVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-embedded-report", ProjectID: "project-a", Name: "report.html", Kind: "text/html",
		Content:   []byte(`<html><body><img src="data:image/png;base64,AAAA/rs123456="><p>No scientific identifier is claimed.</p></body></html>`),
		CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{workspaceStore: store}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	session := sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}
	candidates, err := server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run,
		[]transcriptstore.ArtifactReferenceInput{{
			ArtifactID: artifact.ID, VersionID: embeddedVersion.ID, Relation: transcriptstore.ArtifactRelationProduced,
		}},
		"Embedded report attached.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Embedded report attached.", nil, candidates,
	); len(got) != 0 {
		t.Fatalf("embedded data URI bytes were treated as citations: %#v", got)
	}

	visibleArtifact, visibleVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-visible-reference", ProjectID: "project-a", Name: "visible.html", Kind: "text/html",
		Content:   []byte(`<html><body><img src="data:image/png;base64,AAAA/rs123456="><p>Visible dbSNP rs123456.</p></body></html>`),
		CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err = server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run,
		[]transcriptstore.ArtifactReferenceInput{{
			ArtifactID: visibleArtifact.ID, VersionID: visibleVersion.ID, Relation: transcriptstore.ArtifactRelationProduced,
		}},
		"Visible report attached.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Visible report attached.", nil, candidates,
	); !reflect.DeepEqual(got, []string{"accession:dbsnp:RS123456"}) {
		t.Fatalf("visible dbSNP identifier was not retained for grounding: %#v", got)
	}
}

func TestSessionRunnerCitationGroundingDoesNotTreatInternalLargeToolResultAsImplicitFinalArtifact(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	internalArtifactID := "large-tool-result-" + strings.Repeat("a", 32)
	_, internalVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: internalArtifactID, ProjectID: "project-a", Name: "tool-result-deadbeef.json",
		Kind: "application/json", Content: []byte(`{"results":[{"doi":"10.9999/unselected-tool-result"}]}`),
		CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, finalVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-final", ProjectID: "project-a", Name: "final.md", Kind: "text/markdown",
		Content: []byte("final report without unsupported identifiers\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{
		{ArtifactID: internalArtifactID, VersionID: internalVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: "artifact-final", VersionID: finalVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
	}
	server := &Server{workspaceStore: store}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	session := sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}
	candidates, err := server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run, commits, "Final report only.",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Final report only.", nil, candidates,
	); len(got) != 0 {
		t.Fatalf("unselected internal tool-result citations=%#v", got)
	}

	candidates, err = server.sessionRunnerCompletionArtifactCandidateReferences(
		session, run, commits, "Explicit internal evidence: {{artifact:"+internalVersion.ID+"}}",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Explicit internal evidence.", nil, candidates,
	); !reflect.DeepEqual(got, []string{"doi:10.9999/unselected-tool-result"}) {
		t.Fatalf("explicit internal tool-result citations=%#v", got)
	}
}

func TestSessionRunnerCitationGroundingUsesManifestSelectedVersionsAfterRecovery(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	staleArtifact, staleVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-stale-report", ProjectID: "project-a", Name: "stale-report.md", Kind: "text/markdown",
		Content: []byte("superseded unsupported structure PDB: 7L9V\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	finalArtifact, finalVersion, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-final-report", ProjectID: "project-a", Name: "final-report.md", Kind: "text/markdown",
		Content: []byte("final report without unsupported identifiers\n"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	commits := []transcriptstore.ArtifactReferenceInput{
		{ArtifactID: staleArtifact.ID, VersionID: staleVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
		{ArtifactID: finalArtifact.ID, VersionID: finalVersion.ID, Relation: transcriptstore.ArtifactRelationProduced},
	}
	server := &Server{workspaceStore: store}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{}}
	session := sessionstore.Session{Project: &sessionstore.Project{ID: "project-a"}}
	candidates, err := server.sessionRunnerCompletionArtifactCandidateReferencesWithSelection(
		session, run, commits, "Final report only.", map[string]struct{}{finalVersion.ID: {}}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Final report only.", nil, candidates,
	); len(got) != 0 {
		t.Fatalf("unselected historical citations=%#v", got)
	}
	candidates, err = server.sessionRunnerCompletionArtifactCandidateReferencesWithSelection(
		session, run, commits, "Explicit stale: {{artifact:"+staleVersion.ID+"}}",
		map[string]struct{}{finalVersion.ID: {}}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := unsupportedSessionRunnerCitationReferencesWithArtifactCandidates(
		nil, 0, "Explicit stale.", nil, candidates,
	); !reflect.DeepEqual(got, []string{"accession:pdb:7L9V"}) {
		t.Fatalf("explicit historical citations=%#v", got)
	}
}

func TestSessionRunnerScientificArtifactGateRejectsBadFinalWithoutAutomaticReplacement(t *testing.T) {
	fixture := newAgentSaveArtifactsFixtureWithKernelManager(t, realManagedScientificKernelManagerForServerTest(t))
	malformed := strings.Replace(validServerEthanolSDF, "\n  3  2  0", "\nextra header\n  3  2  0", 1)
	artifact, badVersion := seedSessionRunnerSDFCommit(
		t, fixture, "artifact-runner-sdf", "molecules.sdf", malformed, transcriptstore.ArtifactRelationProduced,
	)
	_, goodVersion := seedSessionRunnerSDFCommit(
		t, fixture, artifact.ID, "molecules.sdf", validServerEthanolSDF, transcriptstore.ArtifactRelationProduced,
	)
	_, unreferencedBadVersion := seedSessionRunnerSDFCommit(
		t, fixture, "artifact-unreferenced-sdf", "unreferenced.sdf", malformed, transcriptstore.ArtifactRelationConsumed,
	)
	commits, err := fixture.repo.ListArtifactCommitReferences(
		context.Background(), fixture.stream.UID, fixture.stream.OwnerID, fixture.claim.Attempt,
	)
	if err != nil || len(commits) != 3 {
		t.Fatalf("scientific artifact commits=%#v err=%v", commits, err)
	}
	server := fixture.server
	if failures, err := server.validateSessionRunnerScientificArtifacts(
		context.Background(), "project-save", commits, "No artifact link in this candidate.",
	); err != nil || len(failures) != 0 {
		t.Fatalf("latest corrected SDF failures=%#v err=%v", failures, err)
	}
	if failures, err := server.validateSessionRunnerScientificArtifacts(
		context.Background(), "project-save", commits,
		"Explicit superseded version: {{artifact:"+badVersion.ID+"}}; ignored consumed version: "+unreferencedBadVersion.ID,
	); err != nil || len(failures) != 1 || failures[0] != "invalid_sdf_records" {
		t.Fatalf("explicit bad SDF failures=%#v err=%v", failures, err)
	}

	// Final delivery uses the current produced-version projection. The earlier
	// malformed version above is deliberately superseded, so it is rejected by
	// reference authority before scientific validation. Exercise the scientific
	// completion gate with a current invalid version, not a stale reference.
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
	currentCommits, err := server.sessionRunnerArtifactCommitReferences(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	session := sessionstore.Session{ID: "frame-save", Project: &sessionstore.Project{ID: "project-save"}}
	stale, err := server.unresolvedSessionRunnerArtifactReferences(session, run, currentCommits, "{{artifact:"+badVersion.ID+"}}")
	if err != nil || !reflect.DeepEqual(stale, []string{badVersion.ID}) {
		t.Fatalf("superseded reference=%v err=%v", stale, err)
	}
	_, badVersion = seedSessionRunnerSDFCommit(t, fixture, artifact.ID, "molecules.sdf", malformed+"\n", transcriptstore.ArtifactRelationProduced)

	model := &scientificArtifactRepairModel{
		badVersionID: badVersion.ID, goodVersionID: goodVersion.ID, testing: t,
	}
	discards := 0
	result, err := server.runSessionAgentWithArtifactReferenceRepair(
		context.Background(),
		session,
		agentruntime.Engine{Model: model},
		agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Publish molecules.sdf"}}},
		run,
		func(_ int, text string) error {
			if text != "" {
				t.Fatalf("scientific repair discard text=%q", text)
			}
			discards++
			return nil
		},
	)
	var integrityErr *sessionRunnerReferenceIntegrityError
	if !errors.As(err, &integrityErr) || len(integrityErr.InvalidScientificArtifacts) != 1 {
		t.Fatalf("error=%#v", err)
	}
	if model.calls != 1 || discards != 0 || strings.Contains(result.FinalMessage.Content, goodVersion.ID) ||
		!strings.Contains(result.FinalMessage.Content, badVersion.ID) {
		t.Fatalf("calls=%d discards=%d final=%q", model.calls, discards, result.FinalMessage.Content)
	}
}

func TestSessionRunnerResearchArtifactGateReusesVersionsWithinSameAttemptAfterReclaim(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	artifact, scopeVersion := seedSessionRunnerResearchArtifactCommit(
		t, fixture, fixture.claim, "artifact-scope", "scope.json", []byte(`{"target":"NLRP3"}`),
	)
	interrupted, err := fixture.repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: fixture.claim, ClientMessageID: "research-artifact-restart", ReasonCode: "runtime_draining",
		ResumeDetail: "test process restart", AutoResume: true, Destinations: []string{"ws"},
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interrupt=%#v err=%v", interrupted, err)
	}
	reclaimed, err := fixture.repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, RunnerID: fixture.claim.RunnerID,
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != fixture.claim.Attempt {
		t.Fatalf("reclaim=%#v err=%v", reclaimed, err)
	}
	manifest := fmt.Sprintf(`{
		"schema":"synon.research_provenance_manifest.v1",
		"artifacts_produced":[{
			"artifact_id":%q,"version_id":%q,"filename":"scope.json","sha256":%q
		}]
	}`, artifact.ID, scopeVersion.ID, scopeVersion.ContentSHA256)
	_, manifestVersion := seedSessionRunnerResearchArtifactCommit(
		t, fixture, reclaimed.Claim, "artifact-manifest", "provenance_manifest.json", []byte(manifest),
	)
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: reclaimed.Claim}}
	commits, err := fixture.server.sessionRunnerArtifactCommitReferences(context.Background(), run)
	if err != nil || len(commits) != 2 {
		t.Fatalf("current commits=%#v err=%v", commits, err)
	}
	if commits[0].VersionID != scopeVersion.ID || commits[1].VersionID != manifestVersion.ID {
		t.Fatalf("current commit versions=%#v", commits)
	}
	if failures, err := fixture.server.validateSessionRunnerResearchArtifacts(
		context.Background(), "project-save", commits,
	); err != nil || len(failures) != 0 {
		t.Fatalf("cross-attempt research failures=%v err=%v", failures, err)
	}
	if unresolved, err := fixture.server.unresolvedSessionRunnerArtifactReferenceCount(
		sessionstore.Session{ID: "frame-save", Project: &sessionstore.Project{ID: "project-save"}},
		run, commits, "Final scope: {{artifact:"+scopeVersion.ID+"}}",
	); err != nil || unresolved != 0 {
		t.Fatalf("cross-attempt unresolved=%d err=%v", unresolved, err)
	}
	ledger, err := fixture.server.sessionRunnerResearchArtifactManifestLedger("project-save", commits)
	if err != nil || !strings.Contains(ledger, scopeVersion.ID) || strings.Contains(ledger, manifestVersion.ID) {
		t.Fatalf("current artifact ledger=%q err=%v", ledger, err)
	}
	var scopeVersions int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM artifact_versions WHERE artifact_id=?`, artifact.ID).Scan(&scopeVersions); err != nil || scopeVersions != 1 {
		t.Fatalf("scope versions=%d err=%v", scopeVersions, err)
	}
}

func TestSessionRunnerCompletionExcludesInternalWorkingDataCommits(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	artifact, _ := seedSessionRunnerResearchArtifactCommit(
		t, fixture, fixture.claim, "artifact-internal-plan", "plan_internal.json",
		[]byte(`{"task_summary":"GSE123813 analysis"}`),
	)
	if err := fixture.store.SetArtifactRetentionMode(
		context.Background(), artifact.ID, fixture.stream.ProjectID, fixture.stream.OwnerID, "working_data",
	); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{
		Stream: fixture.stream, Claim: fixture.claim,
	}}
	commits, err := fixture.server.sessionRunnerArtifactCommitReferences(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 0 {
		t.Fatalf("internal working-data commits reached completion validation: %#v", commits)
	}
}

func TestSessionRunnerScientificArtifactGateFailsClosedWhenValidatorIsUnavailable(t *testing.T) {
	store := openRunnerArtifactCompletionStore(t)
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-unavailable-sdf", ProjectID: "project-a", Name: "molecules.sdf",
		Kind: "chemical/x-mdl-sdfile", Content: []byte(validServerEthanolSDF), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	failures, err := (&Server{workspaceStore: store}).validateSessionRunnerScientificArtifacts(
		context.Background(), "project-a",
		[]transcriptstore.ArtifactReferenceInput{{
			ArtifactID: artifact.ID, VersionID: version.ID, Relation: transcriptstore.ArtifactRelationProduced,
		}},
		"[molecules.sdf]({{artifact:"+version.ID+"}})",
	)
	if failures != nil || !errors.Is(err, errScientificArtifactValidationUnavailable) {
		t.Fatalf("unavailable validator failures=%#v err=%v", failures, err)
	}
}

func seedSessionRunnerSDFCommit(
	t *testing.T,
	fixture *agentSaveArtifactsFixture,
	artifactID, name, content string,
	relation transcriptstore.ArtifactRelation,
) (workspace.Artifact, workspace.ArtifactVersion) {
	t.Helper()
	_, source, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: fixture.claim, ClientMessageID: fmt.Sprintf("scientific-commit-%s-%d", artifactID, len(content)),
		Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: []byte(`{"tool":"save_artifacts"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, version, err := fixture.store.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(context.Background(), fmt.Sprintf("scientific-sdf-%d", source.EventID)),
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: "project-save", Name: name,
			ContentType: "chemical/x-mdl-sdfile", Content: strings.NewReader(content), CreatedBy: fixture.claim.RunnerID,
			MaxBytes: 1 << 20, RootFrameID: fixture.stream.RootFrameID, FrameID: fixture.stream.FrameID,
			TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
				StreamUID: fixture.stream.UID, RunnerID: fixture.claim.RunnerID, ClaimToken: fixture.claim.ClaimToken,
				Attempt: fixture.claim.Attempt, SourceEventID: source.EventID, Relation: string(relation),
			},
		},
		fixture.stream.OwnerID,
	)
	if err != nil {
		t.Fatal(err)
	}
	return artifact, version
}

func seedSessionRunnerResearchArtifactCommit(
	t *testing.T,
	fixture *agentSaveArtifactsFixture,
	claim transcriptstore.RunnerClaim,
	artifactID, name string,
	content []byte,
) (workspace.Artifact, workspace.ArtifactVersion) {
	t.Helper()
	_, source, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: fmt.Sprintf("research-commit-%s-%d", artifactID, claim.Attempt),
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"tool":"save_artifacts"}`), Destinations: []string{"ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, version, err := fixture.store.WriteArtifactVersionRealtime(
		workspace.WithMutationIdempotencyKey(context.Background(), fmt.Sprintf("research-artifact-%d", source.EventID)),
		workspace.WriteArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: "project-save", Name: name,
			ContentType: "application/json", Content: strings.NewReader(string(content)),
			CreatedBy: claim.RunnerID, MaxBytes: 1 << 20,
			RootFrameID: fixture.stream.RootFrameID, FrameID: fixture.stream.FrameID,
			TranscriptAssociation: &workspace.ArtifactTranscriptAssociation{
				StreamUID: fixture.stream.UID, RunnerID: claim.RunnerID, ClaimToken: claim.ClaimToken,
				Attempt: claim.Attempt, SourceEventID: source.EventID,
				Relation: string(transcriptstore.ArtifactRelationProduced),
			},
		},
		fixture.stream.OwnerID,
	)
	if err != nil {
		t.Fatal(err)
	}
	return artifact, version
}

func openRunnerArtifactCompletionStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.Open(t.TempDir() + "/workspace.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "project-a", UserID: "local", Name: "Project A"},
		{ID: "project-b", UserID: "local", Name: "Project B"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	return store
}
