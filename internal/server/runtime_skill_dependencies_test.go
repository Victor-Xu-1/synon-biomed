package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/skills"
)

func TestExpandRuntimeSkillDependenciesOrdersAndDeduplicatesClosure(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "foundation"})
	catalog.AddSkill(skills.Skill{Name: "screening", RequiredSkills: []string{"foundation"}})
	catalog.AddSkill(skills.Skill{Name: "deep-review", RequiredSkills: []string{"screening", "foundation"}})
	server := &Server{skillCatalog: catalog}

	closure, err := server.expandRuntimeSkillDependencies(
		[]skills.Skill{{Name: "deep-review", RequiredSkills: []string{"screening", "foundation"}}},
		nil, nil, false, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"foundation", "screening", "deep-review"}
	if len(closure) != len(want) {
		t.Fatalf("closure=%#v", closure)
	}
	for index := range want {
		if closure[index].Name != want[index] {
			t.Fatalf("closure order=%#v want=%#v", closure, want)
		}
	}
}

func TestExpandRuntimeSkillDependenciesFailsClosedOnCycleAndPolicyConflict(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "a", RequiredSkills: []string{"b"}})
	catalog.AddSkill(skills.Skill{Name: "b", RequiredSkills: []string{"a"}})
	server := &Server{skillCatalog: catalog}

	_, err := server.expandRuntimeSkillDependencies(
		[]skills.Skill{{Name: "a", RequiredSkills: []string{"b"}}}, nil, nil, false, nil,
	)
	if err == nil || !errors.Is(err, errSelectedSkillContractUnavailable) || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error=%v", err)
	}

	catalog = skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "foundation"})
	server.skillCatalog = catalog
	_, err = server.expandRuntimeSkillDependencies(
		[]skills.Skill{{Name: "leaf", RequiredSkills: []string{"foundation"}}},
		[]string{"foundation"}, nil, false, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Fatalf("excluded dependency error=%v", err)
	}
}

func TestSkillGatewayEnforcesDependencyPolicyAuthority(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "foundation", Body: "FOUNDATION CONTRACT"})
	catalog.AddSkill(skills.Skill{
		Name: "leaf", Body: "LEAF CONTRACT", RequiredSkills: []string{"foundation"},
	})
	server := New(Options{FileRoot: t.TempDir()})
	server.skillCatalog = catalog
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})

	tests := []struct {
		name       string
		policy     runtimeSkillPolicyAuthority
		wantError  string
		wantLoaded bool
	}{
		{
			name: "leaf outside allow-list",
			policy: runtimeSkillPolicyAuthority{
				AllowedNames: []string{"foundation"}, Restrict: true,
			},
			wantError: "skill \"leaf\" is outside the current allow-list",
		},
		{
			name: "leaf explicitly excluded",
			policy: runtimeSkillPolicyAuthority{
				ExcludedNames: []string{"leaf"},
			},
			wantError: "skill \"leaf\" is excluded by the current policy",
		},
		{
			name: "dependency outside allow-list",
			policy: runtimeSkillPolicyAuthority{
				AllowedNames: []string{"leaf"}, Restrict: true,
			},
			wantError: "outside the current allow-list",
		},
		{
			name: "dependency explicitly excluded",
			policy: runtimeSkillPolicyAuthority{
				ExcludedNames: []string{"foundation"},
			},
			wantError: "requires excluded skill",
		},
		{
			name: "complete policy authority",
			policy: runtimeSkillPolicyAuthority{
				AllowedNames: []string{"leaf", "foundation"}, Restrict: true,
			},
			wantLoaded: true,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := &sessionRunnerChatRun{SessionID: "policy-session"}
			gateway := serverAgentRuntimeToolGateway{
				server: server, taskRun: run, allowedTools: []string{"Skill"}, skillPolicy: test.policy,
			}
			ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
			result, err := gateway.executeAgentToolResponse(
				ctx,
				agentruntime.ToolCall{ID: fmt.Sprintf("skill-policy-%d", index), Name: "Skill"},
				"Skill", map[string]any{"skill": "leaf"},
			)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("policy error=%v want=%q", err, test.wantError)
				}
				if loaded := run.executedSkillNamesSnapshot(); len(loaded) != 0 {
					t.Fatalf("rejected dependency mutated executed Skills=%#v", loaded)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			text, ok := result.(string)
			if !test.wantLoaded || !ok || strings.Index(text, "FOUNDATION CONTRACT") < 0 ||
				strings.Index(text, "FOUNDATION CONTRACT") >= strings.Index(text, "LEAF CONTRACT") {
				t.Fatalf("allowed dependency result=%#v", result)
			}
			loaded := run.executedSkillNamesSnapshot()
			if len(loaded) != 2 || !stringSliceContains(loaded, "foundation") || !stringSliceContains(loaded, "leaf") {
				t.Fatalf("allowed dependency executed Skills=%#v", loaded)
			}
		})
	}
}

func TestSkillSearchAndLoadShareDependencyPolicyAuthority(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "foundation", Body: "FOUNDATION CONTRACT"})
	catalog.AddSkill(skills.Skill{
		Name: "leaf", Body: "LEAF CONTRACT", RequiredSkills: []string{"foundation"},
	})
	server := New(Options{FileRoot: t.TempDir()})
	server.skillCatalog = catalog
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})

	gateway := serverAgentRuntimeToolGateway{
		server: server, allowedTools: []string{"search_skills", "skill"},
		skillPolicy: runtimeSkillPolicyAuthority{
			AllowedNames: []string{"leaf"}, Restrict: true,
		},
	}
	blockedSearch, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "search-blocked-dependency", Name: "search_skills",
		Arguments: json.RawMessage(`{"query":"select:leaf"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	blockedPayload := mapValue(blockedSearch.Value)
	if matches := stringArrayValue(blockedPayload["matches"]); len(matches) != 0 {
		t.Fatalf("search advertised an unloadable Skill: %#v", blockedPayload)
	}
	if missing := stringArrayValue(blockedPayload["missing_skills"]); len(missing) != 1 || missing[0] != "leaf" {
		t.Fatalf("policy-scoped missing Skills=%#v payload=%#v", missing, blockedPayload)
	}

	gateway.skillPolicy.AllowedNames = []string{"foundation", "leaf"}
	allowedSearch, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "search-complete-closure", Name: "search_skills",
		Arguments: json.RawMessage(`{"query":"select:leaf"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	allowedPayload := mapValue(allowedSearch.Value)
	if matches := stringArrayValue(allowedPayload["matches"]); len(matches) != 1 || matches[0] != "leaf" {
		t.Fatalf("complete policy did not advertise the loadable Skill: %#v", allowedPayload)
	}
	loaded, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "load-complete-closure", Name: "skill",
		Arguments: json.RawMessage(`{"skill":"leaf"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, ok := loaded.Value.(string)
	if !ok || strings.Index(prompt, "FOUNDATION CONTRACT") < 0 ||
		strings.Index(prompt, "FOUNDATION CONTRACT") >= strings.Index(prompt, "LEAF CONTRACT") {
		t.Fatalf("loadable search result did not produce the dependency closure: %#v", loaded.Value)
	}
}

func TestRepeatedSkillInvocationReturnsOneCompactReuseReceipt(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "analysis-workflow", Body: "CANONICAL CONTRACT"})
	server := New(Options{FileRoot: t.TempDir()})
	server.skillCatalog = catalog
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	run := &sessionRunnerChatRun{SessionID: "skill-reuse-session"}
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)

	first, err := server.executeSkillToolWithRuntimeSkillsAndPolicy(
		ctx, map[string]any{"skill": "analysis-workflow"}, nil, runtimeSkillPolicyAuthority{}, nil,
	)
	if err != nil || !strings.Contains(stringValue(mapValue(first)["prompt"]), "CANONICAL CONTRACT") {
		t.Fatalf("first Skill load=%#v err=%v", first, err)
	}
	second, err := server.executeSkillToolWithRuntimeSkillsAndPolicy(
		ctx, map[string]any{"skill": "analysis-workflow"}, nil, runtimeSkillPolicyAuthority{}, nil,
	)
	secondPayload := mapValue(second)
	if err != nil || secondPayload["reused"] != true || secondPayload["status"] != "already_loaded" ||
		secondPayload["prompt"] != nil {
		t.Fatalf("repeated Skill load=%#v err=%v", second, err)
	}
	entries, err := server.runtimeStore.List("skill-invocations")
	if err != nil || len(entries) != 1 {
		t.Fatalf("repeated Skill persisted duplicate invocations=%#v err=%v", entries, err)
	}

	third, err := server.executeSkillToolWithRuntimeSkillsAndPolicy(
		ctx, map[string]any{"skill": "analysis-workflow", "args": "different scope"},
		nil, runtimeSkillPolicyAuthority{}, nil,
	)
	if err != nil || mapValue(third)["reused"] == true ||
		!strings.Contains(stringValue(mapValue(third)["prompt"]), "CANONICAL CONTRACT") {
		t.Fatalf("materially different Skill invocation=%#v err=%v", third, err)
	}
	entries, err = server.runtimeStore.List("skill-invocations")
	if err != nil || len(entries) != 2 {
		t.Fatalf("distinct Skill invocation audit=%#v err=%v", entries, err)
	}
}

type repeatedSkillRunnerModel struct {
	round    int
	sawReuse bool
	sawHint  bool
}

func (model *repeatedSkillRunnerModel) Complete(
	_ context.Context,
	request agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	model.round++
	if model.round <= 2 {
		return agentruntime.ModelResponse{Message: agentruntime.Message{
			Role: "assistant",
			ToolCalls: []agentruntime.ToolCall{{
				ID: fmt.Sprintf("load-skill-%d", model.round), Name: "skill",
				Arguments: json.RawMessage(`{"skill":"analysis-workflow"}`),
			}},
		}}, nil
	}
	for _, message := range request.Messages {
		model.sawReuse = model.sawReuse || (message.Role == "tool" && strings.Contains(message.Content, `"reused":true`))
		model.sawHint = model.sawHint || (message.Role == "system" && strings.Contains(message.Content, "only idempotent receipts"))
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", Content: "Loaded Skill: analysis-workflow",
	}}, nil
}

func TestAgentEngineConvergesAfterWeakModelRepeatsLoadedSkill(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "analysis-workflow", Body: "CANONICAL CONTRACT"})
	server := New(Options{FileRoot: t.TempDir()})
	server.skillCatalog = catalog
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	run := &sessionRunnerChatRun{SessionID: "skill-loop-engine"}
	model := &repeatedSkillRunnerModel{}
	gateway := serverAgentRuntimeToolGateway{
		server: server, taskRun: run, sessionID: run.SessionID,
		allowedTools: []string{"skill"}, suppressHooks: true,
	}
	engine := agentruntime.Engine{Model: model, Tools: gateway}
	result, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "Load the analysis workflow once."}},
		Tools: []agentruntime.ToolSchema{{
			Name: "skill", Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"skill": map[string]any{"type": "string"}},
				"required": []string{"skill"},
			},
		}},
		MaxToolRounds: 4, MaxToolCallsPerRound: 1, MaxConsecutiveIdenticalToolRounds: 3,
	})
	if err != nil || result.FinalMessage.Content != "Loaded Skill: analysis-workflow" ||
		model.round != 3 || !model.sawReuse || !model.sawHint {
		t.Fatalf("engine did not converge after the compact reuse receipt: result=%#v model=%#v err=%v", result, model, err)
	}
	entries, err := server.runtimeStore.List("skill-invocations")
	if err != nil || len(entries) != 1 {
		t.Fatalf("engine repeated full Skill materialization=%#v err=%v", entries, err)
	}
}

type alternatingSkillDiscoveryRunnerModel struct {
	round int
}

func (model *alternatingSkillDiscoveryRunnerModel) Complete(
	_ context.Context,
	_ agentruntime.ModelRequest,
) (agentruntime.ModelResponse, error) {
	model.round++
	name := "skill"
	arguments := json.RawMessage(`{"skill":"analysis-workflow"}`)
	if model.round == 1 || model.round == 4 {
		name = "search_skills"
		arguments = json.RawMessage(`{"query":"analysis-workflow","max_results":10,"offset":0}`)
	}
	return agentruntime.ModelResponse{Message: agentruntime.Message{
		Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: fmt.Sprintf("skill-discovery-%d", model.round), Name: name, Arguments: arguments,
		}},
	}}, nil
}

func TestAgentEngineBoundsAlternatingLoadedSkillDiscovery(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "analysis-workflow", Body: "CANONICAL CONTRACT"})
	server := New(Options{FileRoot: t.TempDir()})
	server.skillCatalog = catalog
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	run := &sessionRunnerChatRun{SessionID: "alternating-skill-loop"}
	model := &alternatingSkillDiscoveryRunnerModel{}
	gateway := serverAgentRuntimeToolGateway{
		server: server, taskRun: run, sessionID: run.SessionID,
		allowedTools: []string{"search_skills", "skill"}, suppressHooks: true,
	}
	_, err := (agentruntime.Engine{Model: model, Tools: gateway}).Run(
		context.Background(), agentruntime.RunRequest{
			Messages: []agentruntime.Message{{Role: "user", Content: "Find and load the analysis workflow once."}},
			Tools: []agentruntime.ToolSchema{
				{Name: "search_skills", Parameters: map[string]any{"type": "object"}},
				{Name: "skill", Parameters: map[string]any{"type": "object"}},
			},
			MaxToolRounds: 8, MaxToolCallsPerRound: 1, MaxConsecutiveIdenticalToolRounds: 3,
		},
	)
	var noProgress *agentruntime.ToolRoundNoProgressError
	if !errors.As(err, &noProgress) || noProgress.Limit != 3 || model.round != 5 {
		t.Fatalf("alternating Skill discovery rounds=%d error=%v", model.round, err)
	}
	entries, listErr := server.runtimeStore.List("skill-invocations")
	if listErr != nil || len(entries) != 1 {
		t.Fatalf("alternating Skill loop rematerialized contracts=%#v err=%v", entries, listErr)
	}
}

func TestAgentRuntimeEngineBindsSkillPolicyAuthority(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	options := SessionRunnerChatOptions{
		SessionID: "skill-policy-engine", Endpoint: BuiltinSessionRunnerChatEndpoint,
		AllowedSkillNames:  []string{"leaf", "foundation"},
		ExcludedSkillNames: []string{"blocked"}, RestrictSkillDiscovery: true,
	}
	engine := server.newAgentRuntimeEngineWithContext(context.Background(), options)
	gateway, ok := engine.Tools.(serverAgentRuntimeToolGateway)
	if !ok {
		t.Fatalf("engine gateway=%T", engine.Tools)
	}
	if !gateway.skillPolicy.Restrict ||
		!stringSliceContains(gateway.skillPolicy.AllowedNames, "leaf") ||
		!stringSliceContains(gateway.skillPolicy.AllowedNames, "foundation") ||
		!stringSliceContains(gateway.skillPolicy.ExcludedNames, "blocked") {
		t.Fatalf("bound Skill policy=%#v", gateway.skillPolicy)
	}
}

func TestDeepLiteratureDependencyRestoresCanonicalMethodologyFromLeafSelection(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve server source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	server := New(Options{FileRoot: t.TempDir()})
	productionCatalog, _, loadErrors := loadSkillCatalog(
		nil, []string{filepath.Join(repositoryRoot, "skills", "synonbiomed")}, t.TempDir(),
	)
	if len(loadErrors) != 0 {
		t.Fatalf("production Skill catalog errors=%#v", loadErrors)
	}
	server.skillCatalog = productionCatalog
	deep, found := findCatalogSkill(server.skillCatalog, "deep-literature-investigation")
	if !found {
		t.Fatal("deep-literature-investigation is missing")
	}
	authority := map[string]struct{}{
		"search_skills": {}, "skill": {}, "repl": {}, "web_search": {},
		"web_fetch": {}, "web_research": {}, "fetch_article_fulltext": {}, "save_artifacts": {},
	}
	closure, err := server.expandRuntimeSkillDependencies(
		[]skills.Skill{deep}, nil, nil, false, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(closure) != 2 || closure[0].Name != "literature-review" || closure[1].Name != "deep-literature-investigation" {
		t.Fatalf("deep literature closure=%#v", closure)
	}
	contextText, err := server.runtimeSkillContextFromSkills(closure)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"### literature-review", "### deep-literature-investigation", "Grounding: retrieve first", "claim-evidence map"} {
		if !strings.Contains(contextText, required) {
			t.Fatalf("composed context missing %q: %s", required, contextText)
		}
	}
}

func TestOperonCanDiscoverAndLoadStructureBasedMoleculeGenerationEngines(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve server source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	productionCatalog, _, loadErrors := loadSkillCatalog(
		nil, []string{filepath.Join(repositoryRoot, "skills", "synonbiomed")}, t.TempDir(),
	)
	if len(loadErrors) != 0 {
		t.Fatalf("production Skill catalog errors=%#v", loadErrors)
	}
	rawManifest, err := os.ReadFile(filepath.Join(
		repositoryRoot, "assets", "synonbiomed", "agents", "operon-skills.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	var operonManifest struct {
		Skills []string `json:"skills"`
	}
	if err := json.Unmarshal(rawManifest, &operonManifest); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir()})
	server.skillCatalog = productionCatalog
	runtimeRoot := t.TempDir()
	server.kernelManager = kernelruntime.NewManager(kernelruntime.Config{
		Micromamba: filepath.Join(runtimeRoot, "micromamba"), CondaHome: filepath.Join(runtimeRoot, "conda"),
		CondaEnvsPath: filepath.Join(runtimeRoot, "conda", "envs"), CondaRuntimeCatalog: filepath.Join(runtimeRoot, "catalog.json"),
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	supervisorContext, cancelSupervisor := context.WithCancel(context.Background())
	supervisorDone := make(chan error, 1)
	go func() { supervisorDone <- server.kernelManager.RunManagedEnvironmentSupervisor(supervisorContext) }()
	t.Cleanup(func() {
		cancelSupervisor()
		select {
		case <-supervisorDone:
		case <-time.After(2 * time.Second):
			t.Error("managed environment supervisor did not stop")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for !server.kernelManager.ManagedEnvironmentSupervisorReady() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !server.kernelManager.ManagedEnvironmentSupervisorReady() {
		t.Fatal("managed environment supervisor did not become ready")
	}
	policy := runtimeSkillPolicyAuthority{AllowedNames: operonManifest.Skills, Restrict: true}
	authority := map[string]struct{}{
		"search_skills": {}, "skill": {}, "ask_user": {}, "repl": {}, "web_search": {}, "web_fetch": {},
		"list_compute": {}, "manage_environments": {}, "manage_packages": {}, "bash": {},
		"python": {}, "read_file": {}, "edit_file": {}, "download_public_scientific_file": {}, "save_artifacts": {},
	}

	searched, err := server.executeSkillSearchToolWithRuntimeSkillsAndPolicy(
		map[string]any{"query": "select:structure-based-molecule-generation"}, nil, policy, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	matches := stringArrayValue(mapValue(searched)["matches"])
	if len(matches) != 1 || matches[0] != "structure-based-molecule-generation" {
		t.Fatalf("OPERON structure-based generation search=%#v", searched)
	}
	loaded, err := server.executeSkillToolWithRuntimeSkillsAndPolicy(
		context.Background(), map[string]any{"skill": "structure-based-molecule-generation"},
		nil, policy, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	prompt := stringValue(mapValue(loaded)["prompt"])
	for _, required := range []string{
		"The standard professional workflow is authoritative",
		"never downgrade silently",
		"Ask at a material route choice",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("loaded structure-based generation contract missing %q", required)
		}
	}

	searched, err = server.executeSkillSearchToolWithRuntimeSkillsAndPolicy(
		map[string]any{"query": "pocket2mol local pocket generation", "max_results": 5}, nil, policy, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	matches = stringArrayValue(mapValue(searched)["matches"])
	if len(matches) == 0 || matches[0] != "pocket2mol-local" {
		t.Fatalf("OPERON Pocket2Mol ranking=%#v", searched)
	}
	loaded, err = server.executeSkillToolWithRuntimeSkillsAndPolicy(
		context.Background(), map[string]any{"skill": "pocket2mol-local"}, nil, policy, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	if prompt = stringValue(mapValue(loaded)["prompt"]); !strings.Contains(prompt, "official pengxingang/Pocket2Mol") ||
		!strings.Contains(prompt, "Do not fall back") {
		t.Fatalf("loaded Pocket2Mol contract is incomplete: %s", prompt)
	}
}

func TestRenderComposedSkillPromptKeepsDependenciesBeforeLeaf(t *testing.T) {
	prompt, err := renderComposedSkillPrompt([]skills.Skill{
		{Name: "foundation", Body: "FOUNDATION CONTRACT", CriticalConstraints: []string{"GROUND EVERY CLAIM"}},
		{Name: "leaf", Body: "LEAF CONTRACT {{args}}", Arguments: []string{"args"}},
	}, "leaf", "task input")
	if err != nil {
		t.Fatal(err)
	}
	foundation := strings.Index(prompt, "FOUNDATION CONTRACT")
	leaf := strings.Index(prompt, "LEAF CONTRACT")
	if foundation < 0 || leaf < 0 || foundation >= leaf || !strings.Contains(prompt, "task input") ||
		!strings.Contains(prompt, "GROUND EVERY CLAIM") {
		t.Fatalf("composed prompt=%q", prompt)
	}
}

func TestRenderComposedSkillPromptRejectsOversizeInsteadOfTruncating(t *testing.T) {
	body := strings.Repeat("完整方法。", maxRuntimeSkillContractBytes/len("完整方法。")+1)
	prompt, err := renderComposedSkillPrompt([]skills.Skill{{Name: "oversize", Body: body}}, "oversize", "")
	if err == nil || prompt != "" || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize prompt len=%d err=%v", len(prompt), err)
	}
}

func TestSkillToolLoadsAndRecordsDependencyClosureInOneCall(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "foundation", Body: "FOUNDATION CONTRACT", Tools: []string{"web_fetch"}})
	catalog.AddSkill(skills.Skill{
		Name: "deep-review", Body: "DEEP CONTRACT", Tools: []string{"web_research"},
		RequiredSkills: []string{"foundation"},
	})
	server := New(Options{FileRoot: t.TempDir()})
	server.skillCatalog = catalog
	run := &sessionRunnerChatRun{SessionID: "dependency-session"}
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
	result, err := server.executeSkillToolWithRuntimeSkills(
		ctx,
		map[string]any{"skill": "deep-review"},
		nil,
		map[string]struct{}{"web_fetch": {}, "web_research": {}},
	)
	if err != nil {
		t.Fatal(err)
	}
	payload := mapValue(result)
	prompt := stringValue(payload["prompt"])
	if strings.Index(prompt, "FOUNDATION CONTRACT") < 0 ||
		strings.Index(prompt, "FOUNDATION CONTRACT") >= strings.Index(prompt, "DEEP CONTRACT") {
		t.Fatalf("composed skill prompt=%q", prompt)
	}
	loaded := run.executedSkillNamesSnapshot()
	if len(loaded) != 2 || !stringSliceContains(loaded, "foundation") || !stringSliceContains(loaded, "deep-review") {
		t.Fatalf("recorded loaded skills=%#v", loaded)
	}
	data := mapValue(payload["data"])
	got, ok := data["loadedSkills"].([]string)
	if !ok || !stringSliceContains(got, "foundation") || !stringSliceContains(got, "deep-review") {
		t.Fatalf("tool result loadedSkills=%#v", data["loadedSkills"])
	}
}

func TestExplicitDeepLiteratureSkillReturnsCompleteRealDependencyContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve server source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	server := New(Options{FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	productionCatalog, _, loadErrors := loadSkillCatalog(
		nil, []string{filepath.Join(repositoryRoot, "skills", "synonbiomed")}, t.TempDir(),
	)
	if len(loadErrors) != 0 {
		t.Fatalf("production Skill catalog errors=%#v", loadErrors)
	}
	server.skillCatalog = productionCatalog
	run := &sessionRunnerChatRun{SessionID: "real-deep-literature-session"}
	ctx := context.WithValue(context.Background(), transcriptRunnerChatRunContextKey{}, run)
	authority := map[string]struct{}{
		"search_skills": {}, "skill": {}, "repl": {}, "web_search": {},
		"web_fetch": {}, "web_research": {}, "fetch_article_fulltext": {}, "save_artifacts": {},
	}
	result, err := server.executeSkillToolWithRuntimeSkills(
		ctx, map[string]any{"skill": "deep-literature-investigation"}, nil, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload := mapValue(result)
	prompt := stringValue(payload["prompt"])
	if !utf8.ValidString(prompt) {
		t.Fatal("explicit composed Skill prompt is not valid UTF-8")
	}
	literatureAt := strings.Index(prompt, "### literature-review")
	deepAt := strings.Index(prompt, "### deep-literature-investigation")
	if literatureAt < 0 || deepAt < 0 || literatureAt >= deepAt {
		t.Fatalf("dependency order is incomplete: literature=%d deep=%d", literatureAt, deepAt)
	}
	for _, required := range []string{
		"### Patent evidence is a separate source class",
		"## Style pass before saving",
		"Maintain a claim-evidence map before drafting",
		"## Deliverables",
		"Each cited ledger row must retain a `source_url`",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("complete explicit contract missing %q (bytes=%d)", required, len(prompt))
		}
	}
	data := mapValue(payload["data"])
	loadedSkills, ok := data["loadedSkills"].([]string)
	if !ok || len(loadedSkills) != 2 || loadedSkills[0] != "literature-review" || loadedSkills[1] != "deep-literature-investigation" {
		t.Fatalf("loadedSkills=%#v", data["loadedSkills"])
	}
	entries, err := server.runtimeStore.List("skill-invocations")
	if err != nil || len(entries) != 1 {
		t.Fatalf("persisted skill invocations=%#v err=%v", entries, err)
	}
	persisted := mapValue(entries[0].Value)
	persistedSkills, ok := persisted["loadedSkills"].([]any)
	if !ok || len(persistedSkills) != 2 || stringValue(persistedSkills[0]) != "literature-review" ||
		stringValue(persistedSkills[1]) != "deep-literature-investigation" {
		t.Fatalf("persisted loadedSkills=%#v", persisted["loadedSkills"])
	}
}
