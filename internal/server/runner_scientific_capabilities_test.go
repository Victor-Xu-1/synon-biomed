package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	"synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
	"synon-go/internal/providers"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestRequiredScientificCapabilitiesComeFromSelectedSkillContract(t *testing.T) {
	selected := []skills.Skill{
		{Name: "diffdock", RequiredCapabilities: []string{"molecular-docking"}},
		{Name: "pipeline", RequiredCapabilities: []string{"binding-affinity", "molecular-docking"}},
	}
	got := requiredScientificCapabilities(selected)
	if strings.Join(got, ",") != "binding-affinity,molecular-docking" {
		t.Fatalf("required=%#v", got)
	}
}

func scientificCapabilityTestWitness(capability, engine, enginePackage, scoreKind string, weights bool) sciencecapability.Witness {
	digest := strings.Repeat("a", 64)
	witness := sciencecapability.Witness{
		Version: sciencecapability.WitnessVersion, Capability: capability,
		Engine: engine, EnginePackage: enginePackage, EngineVersion: "1.0.0",
		ProfileSHA256: digest, CodeSHA256: digest, EnvironmentSHA256: digest,
		Provider: "local-conda", JobID: "job-1", State: "completed", ScoreKind: scoreKind,
		Inputs: map[string]string{"receptor": digest, "ligand": digest},
		Artifacts: []sciencecapability.ArtifactWitness{
			{Kind: "ranked-pose", SHA256: digest}, {Kind: "execution-log", SHA256: digest},
		},
		CompletedAt: time.Now().UTC(),
	}
	if weights {
		witness.WeightsSHA256 = digest
	}
	return witness
}

func TestAgentSkillInvocationBindsTrustedScientificCapabilitiesToCurrentRun(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(skillsDir, "real-docking")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: real-docking
description: Run a real docking engine
required-capabilities: [molecular-docking]
---
Use only a server-verified docking execution.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: t.TempDir(), SkillDirectories: []string{skillsDir}})
	run := &sessionRunnerChatRun{}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"Skill"}}
	result, err := gateway.executeAgentToolResponse(
		withTranscriptRunnerChatRun(t.Context(), run),
		agentruntime.ToolCall{ID: "skill-call", Name: "Skill"},
		"Skill", map[string]any{"skill": "real-docking"},
	)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := run.requiredScientificCapabilitiesSnapshot()
	if strings.Join(capabilities, ",") != "molecular-docking" {
		t.Fatalf("required capabilities=%#v", capabilities)
	}
	if names := run.executedSkillNamesSnapshot(); strings.Join(names, ",") != "real-docking" {
		t.Fatalf("executed Skills=%#v", names)
	}
	response, ok := result.(string)
	if !ok || !strings.Contains(response, `<skill-metadata name="real-docking"`) ||
		!strings.Contains(response, "This capability is now active for the current task") {
		t.Fatalf("skill response=%#v", result)
	}
}

func TestScientificCapabilitiesResumeOnlyFromCompletedSkillCheckpoint(t *testing.T) {
	entries := []journal.Entry{
		{Message: journal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": "Skill",
			"toolInput":                      map[string]any{"skill": "/real-docking"},
			"requiredScientificCapabilities": []any{"molecular-docking", "binding-affinity"},
		}},
		{Message: journal.Message{
			"type": "runner_checkpoint", "status": "failed", "toolPhase": "failed", "toolName": "Skill",
			"toolInput":                      map[string]any{"skill": "failed-generation"},
			"requiredScientificCapabilities": []any{"molecule-generation"},
		}},
		{Message: journal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": "MCPTool",
			"requiredScientificCapabilities": []any{"untrusted-capability"},
		}},
	}
	capabilities := requiredScientificCapabilitiesFromRunnerEntries(entries)
	if strings.Join(capabilities, ",") != "binding-affinity,molecular-docking" {
		t.Fatalf("resumed capabilities=%#v", capabilities)
	}
	if names := completedSkillNamesFromRunnerEntries(entries); strings.Join(names, ",") != "real-docking" {
		t.Fatalf("resumed executed Skills=%#v", names)
	}
	keys := completedSkillInvocationKeysFromRunnerEntries(entries)
	if len(keys) != 1 || keys[0] != runtimeSkillInvocationKey("real-docking", "", "") {
		t.Fatalf("resumed exact Skill invocation keys=%#v", keys)
	}
}

func TestScientificCapabilitiesResumeFromExecutedSkillIdentity(t *testing.T) {
	entries := []journal.Entry{{Message: journal.Message{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": "skill",
		"toolInput":         map[string]any{"skill": "chemistry"},
		"executedToolInput": map[string]any{"skill": "p2rank-pocket-detection"},
	}}}

	if names := completedSkillNamesFromRunnerEntries(entries); !reflect.DeepEqual(names, []string{"p2rank-pocket-detection"}) {
		t.Fatalf("recovered executed Skill identity=%#v", names)
	}
	keys := completedSkillInvocationKeysFromRunnerEntries(entries)
	if !reflect.DeepEqual(keys, []string{runtimeSkillInvocationKey("p2rank-pocket-detection", "", "")}) {
		t.Fatalf("recovered executed Skill invocation=%#v", keys)
	}
}

func TestEffectiveSelectedSkillsPreferCompletedExecutionIdentity(t *testing.T) {
	run := &sessionRunnerChatRun{}
	if got := sessionRunnerEffectiveSelectedSkillNames([]string{"chemistry"}, run); !reflect.DeepEqual(got, []string{"chemistry"}) {
		t.Fatalf("unexecuted request=%#v", got)
	}
	run.addExecutedSkillNames("p2rank-pocket-detection")
	if got := sessionRunnerEffectiveSelectedSkillNames([]string{"chemistry"}, run); !reflect.DeepEqual(got, []string{"p2rank-pocket-detection"}) {
		t.Fatalf("completed execution did not replace stale request=%#v", got)
	}
}

func TestScientificSignalsDoNotCrossTranscriptInputRevisions(t *testing.T) {
	entries := []journal.Entry{
		{Message: journal.Message{
			"type": "runner_checkpoint", "runnerAttempt": 4, "status": "completed", "toolPhase": "completed", "toolName": "Skill",
			"toolInput": map[string]any{"skill": "old-medchem"}, "requiredScientificCapabilities": []any{"molecule-generation"},
		}},
		{Message: journal.Message{
			"type": "runner_checkpoint", "runnerAttempt": 5, "status": "completed", "toolPhase": "completed", "toolName": "Skill",
			"toolInput": map[string]any{"skill": "current-analysis"}, "requiredScientificCapabilities": []any{"binding-affinity"},
		}},
		{Message: journal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": "Skill",
			"toolInput": map[string]any{"skill": "unscoped-legacy"}, "requiredScientificCapabilities": []any{"molecular-docking"},
		}},
	}

	filtered := filterRunnerEntriesByInputRevision(entries, map[int]int64{4: 7, 5: 8}, 8)
	if names := completedSkillNamesFromRunnerEntries(filtered); strings.Join(names, ",") != "current-analysis" {
		t.Fatalf("scoped executed Skills=%#v", names)
	}
	if capabilities := requiredScientificCapabilitiesFromRunnerEntries(filtered); strings.Join(capabilities, ",") != "binding-affinity" {
		t.Fatalf("scoped capabilities=%#v", capabilities)
	}
}

func TestExplicitSkillSelectionFailsClosedForMissingDuplicateOrExcludedContract(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(skillsDir, "real-docking")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: real-docking
description: Run a real molecular docking engine
required-capabilities: [molecular-docking]
---
Use a verified docking engine.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: t.TempDir(), SkillDirectories: []string{skillsDir}})
	for name, selection := range map[string]struct {
		selected []string
		excluded []string
	}{
		"missing":   {selected: []string{"missing-skill"}},
		"duplicate": {selected: []string{"real-docking", "real-docking"}},
		"excluded":  {selected: []string{"real-docking"}, excluded: []string{"real-docking"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := srv.runtimeSkillsByName(selection.selected, selection.excluded); !errors.Is(err, errSelectedSkillContractUnavailable) {
				t.Fatalf("runtimeSkillsByName() error = %v", err)
			}
		})
	}
}

func TestExplicitUnavailableSkillInterruptsWithoutCallingProvider(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-skill-contract-recovery", "frame-skill-contract-recovery")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"must not run"}}]}`))
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-skill-contract-recovery", MessageUUID: "message-skill-contract-recovery",
		ClientMessageID: "client-skill-contract-recovery", Text: "Analyze a real public scientific dataset.",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := server.RunSessionRunnerChatOnce(t.Context(), SessionRunnerChatOptions{SessionID: "frame-skill-contract-recovery", RunnerID: "runner-skill-contract-recovery",
		Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "stream-model",
		LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 1 << 20, MaxAttempts: 1,
		DisableSkillDiscovery: true, SelectedSkillNames: []string{"removed-skill"},
		ModelProfile: &providers.ModelProfile{
			Provider: providers.ProviderProfile{ID: "skill-contract-recovery", Type: "openai", Protocol: providers.ProtocolOpenAICompatible, Endpoint: provider.URL},
			Model:    "stream-model", APIKey: "test-key", Request: providers.RequestProfile{MaxAttempts: 1, MaxResponseBytes: 1 << 20},
		},
	})
	if err != nil || result.Status != "interrupted" ||
		result.InterruptionReasonCode != "selected_skill_contract_unavailable" ||
		!result.InterruptionAutoResume || result.FinishEventID != 0 || requests.Load() != 0 {
		t.Fatalf("result=%#v provider_requests=%d err=%v", result, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-skill-contract-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ThroughPublicationSequence: stream.NextPublication - 1, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	foundDiagnostic := false
	for _, event := range projected {
		if event.Event.Type != "runner_checkpoint" {
			continue
		}
		payload := map[string]any{}
		if json.Unmarshal(event.ResolvedPayloadJSON, &payload) == nil &&
			stringValue(payload["reason_code"]) == "selected_skill_contract_unavailable" &&
			strings.Contains(stringValue(payload["resume_detail"]), "removed-skill") {
			foundDiagnostic = true
		}
	}
	if !foundDiagnostic {
		t.Fatalf("missing durable unavailable-skill diagnostic: %#v", projected)
	}
}
