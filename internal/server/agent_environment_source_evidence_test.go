package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/skills"
)

func TestCanonicalManagedEnvironmentSourceURLNormalizesPipAndVCSForms(t *testing.T) {
	for raw, want := range map[string]string{
		"git+https://github.com/example/Engine.git":                 "https://github.com/example/Engine",
		"pip::git+https://github.com/example/Engine.git@v1.2.3":     "https://github.com/example/Engine",
		"engine @ https://github.com/example/Engine/archive/v1.zip": "https://github.com/example/Engine/archive/v1.zip",
	} {
		if got := canonicalManagedEnvironmentSourceURL(raw); got != want {
			t.Errorf("canonical source for %q=%q want %q", raw, got, want)
		}
	}
	for _, raw := range []string{"engine", "git+ssh://github.com/example/Engine.git", "file:///tmp/engine"} {
		if got := canonicalManagedEnvironmentSourceURL(raw); got != "" {
			t.Errorf("non-HTTPS source %q normalized to %q", raw, got)
		}
	}
}

func TestManagedEnvironmentSourceEvidenceUsesLoadedSkillWithoutAdmittingGuess(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "selected-engine", Body: "Official source: https://github.com/example/SelectedEngine",
	})
	server.skillCatalog = catalog
	run := &sessionRunnerChatRun{}
	run.addExecutedSkillNames("selected-engine")
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	evidence, missing, err := server.managedEnvironmentSourceEvidence(ctx, []string{
		"pip::git+https://github.com/example/SelectedEngine.git",
		"pip::git+https://github.com/invented/SelectedEngine.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evidence, map[string]string{
		"https://github.com/example/SelectedEngine": "skill:selected-engine",
	}) || !reflect.DeepEqual(missing, []string{"https://github.com/invented/SelectedEngine"}) {
		t.Fatalf("evidence=%#v missing=%#v", evidence, missing)
	}
}

func TestManagedEnvironmentSourceEvidenceAcceptsUserProvidedURL(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	run := &sessionRunnerChatRun{TaskIntent: "Install https://example.org/releases/engine.whl for this task."}
	evidence, missing, err := server.managedEnvironmentSourceEvidence(
		withTranscriptRunnerChatRun(context.Background(), run),
		[]string{"pip::https://example.org/releases/engine.whl"},
	)
	if err != nil || len(missing) != 0 || evidence["https://example.org/releases/engine.whl"] != askUserCurrentTaskEvidenceReference {
		t.Fatalf("evidence=%#v missing=%#v err=%v", evidence, missing, err)
	}
}

func TestManagedEnvironmentSourceEvidenceAdmitsStructuredPublicPackageSource(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	source := "https://packages.example.org/wheels/torch.html"
	evidence, missing, err := server.managedEnvironmentSourceEvidence(
		context.Background(), []string{source}, source,
	)
	if err != nil || len(missing) != 0 || evidence[source] != "runtime:public-package-source" {
		t.Fatalf("evidence=%#v missing=%#v err=%v", evidence, missing, err)
	}
	for _, rejected := range []string{
		"https://127.0.0.1/wheels/torch.html",
		"https://localhost/wheels/torch.html",
		"https://user:secret@packages.example.org/wheels/torch.html",
	} {
		if managedEnvironmentPublicPackageSource(rejected) {
			t.Fatalf("unsafe structured package source was admitted: %s", rejected)
		}
	}
}

func TestManagedEnvironmentSourceEvidenceRejectsSelfAuthorizingPreflightDiagnostic(t *testing.T) {
	url := "https://github.com/invented/Engine"
	if managedEnvironmentToolResultProvidesSourceEvidence(map[string]any{
		"ok": true, "executed": false, "status": "implementation_source_evidence_required",
		"missing_sources": []any{url},
	}) {
		t.Fatal("a rejected preflight diagnostic authorized its own guessed source")
	}
	if managedEnvironmentToolResultProvidesSourceEvidence(map[string]any{
		"ok": false, "error": "source lookup failed", "url": url,
	}) {
		t.Fatal("a failed source lookup became source evidence")
	}
	if !managedEnvironmentToolResultProvidesSourceEvidence(map[string]any{
		"ok": true, "records": []any{map[string]any{"url": url}},
	}) {
		t.Fatal("a successful source lookup was not admitted as evidence")
	}
}

func TestManagedEnvironmentSourceEvidencePreservesEnvelopeAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result map[string]any
		want   bool
	}{
		{"outer failed", map[string]any{"ok": false, "result": map[string]any{"ok": true}}, false},
		{"outer not executed", map[string]any{"ok": true, "executed": false, "result": map[string]any{"ok": true}}, false},
		{"inner not executed", map[string]any{"ok": true, "result": map[string]any{"ok": true, "executed": false}}, false},
		{"outer partial", map[string]any{"partial": true, "result": map[string]any{"ok": true}}, false},
		{"inner failed", map[string]any{"ok": true, "result": map[string]any{"ok": false}}, false},
		{"successful wrapper", map[string]any{"ok": true, "executed": true, "result": map[string]any{"ok": true, "url": "https://example.org/source"}}, true},
		{"scientific data not envelope", map[string]any{"ok": true, "result": map[string]any{"records": []any{map[string]any{"executed": false, "status": "failed"}}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedEnvironmentToolResultProvidesSourceEvidence(tc.result); got != tc.want {
				t.Fatalf("accepted=%v want %v", got, tc.want)
			}
		})
	}
}

func TestManagedEnvironmentSourceEvidenceReplaysDurableExecutionState(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	source := "https://example.org/engine.whl"
	for _, executed := range []bool{false, true} {
		callID := "source-rejected"
		if executed {
			callID = "source-read"
		}
		payload, err := json.Marshal(map[string]any{
			"lifecyclePhase": "tool", "toolName": "web_fetch", "toolPhase": "completed",
			"toolCallId": callID, "toolInput": map[string]any{"url": source},
			"toolResult": map[string]any{"ok": true, "executed": executed, "result": map[string]any{"ok": true, "url": source}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := fixture.repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: fixture.claim, ClientMessageID: callID, Phase: transcriptstore.RunnerPhaseExecuting, PayloadJSON: payload,
		}); err != nil {
			t.Fatal(err)
		}
		// Reconstruct the task from durable SQLite receipts, without carrying
		// an in-memory evidence cache across the failed and successful attempt.
		run := &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}}
		ctx := withTranscriptRunnerChatRun(context.Background(), run)
		evidence, missing, err := fixture.server.managedEnvironmentSourceEvidence(ctx, []string{source})
		if err != nil {
			t.Fatal(err)
		}
		if !executed && (len(evidence) != 0 || len(missing) != 1) {
			t.Fatalf("non-executing durable receipt authorized source: %v %v", evidence, missing)
		}
		if executed && (len(missing) != 0 || evidence[source] != "tool-call:source-read") {
			t.Fatalf("repaired source did not recover: %v %v", evidence, missing)
		}
	}
}
