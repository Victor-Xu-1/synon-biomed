package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	kernelruntime "synon-go/internal/kernel"
	"synon-go/internal/sciencecapability"
	"synon-go/internal/skills"
)

func TestAdvertisedRootToolDoesNotRequireSkillPreflight(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "检索公开临床试验并建立可审计药理综述"}
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: run,
		allowedTools: []string{"web_fetch"},
	}
	call := agentruntime.ToolCall{
		ID: "fetch", Name: "web_fetch",
		Arguments: json.RawMessage(`{"url":"https://example.test"}`),
	}
	if diagnostic := gateway.toolCallPreflightDiagnostic(context.Background(), call); diagnostic != "" {
		t.Fatalf("advertised root tool was forced through Skill preflight: %s", diagnostic)
	}
}

func TestManagedImplementationEnvironmentOnlyStartsCanonicalPackEntrypoint(t *testing.T) {
	workspace := t.TempDir()
	script := filepath.Join(workspace, ".synon", "runtime", "skills", "managed-workflow-abcd", "scripts", "run.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("print('managed')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{TaskIntent: "run Engine A", SelectedImplementations: []string{"Engine A"}}
	run.bindManagedEnvironmentImplementation("engine-a-runtime", "Engine A")
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "managed-workflow", ImplementationIdentities: []string{"Engine A"}})
	server := &Server{skillCatalog: catalog, scienceCapabilities: &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "managed-capability", AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine-a", Package: "examplelib", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "managed-capability.engine-a", Mode: "local", Skill: "managed-workflow",
				Executable: "python", Script: "executionpacks/run.py",
				CLIWitnesses: []sciencecapability.ExecutionCLIWitness{{Executable: "example-cli"}},
			},
		}},
	}}}}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	for name, input := range map[string]map[string]any{
		"python argv indirection": {
			"environment": "engine-a-runtime",
			"code":        "import subprocess\ncommand=['example-cli','--version']\nsubprocess.run(command)",
		},
		"shell variable indirection": {
			"environment": "engine-a-runtime", "command": "tool=example-cli; \"$tool\" --version",
		},
		"pack followed by a second line": {
			"environment": "engine-a-runtime",
			"command":     `python "` + script + `"` + "\nprintf altered > results/report.md",
		},
		"pack with output redirection": {
			"environment": "engine-a-runtime",
			"command":     `python "` + script + `" > results/report.md`,
		},
		"attached interpreter code before pack": {
			"environment": "engine-a-runtime",
			"command": `python "-cimport pathlib;pathlib.Path('results/report.md').write_text('tampered')" "` +
				script + `"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			publicName := "python"
			if input["command"] != nil {
				publicName = "bash"
			}
			blocked := server.managedExecutionEnvironmentBoundary(ctx, publicName, input, "engine-a-runtime")
			if blocked == nil || blocked["status"] != "managed_execution_environment_entrypoint_required" || blocked["executed"] != false {
				t.Fatalf("managed environment process boundary=%#v", blocked)
			}
		})
	}
	canonical := map[string]any{
		"environment": "engine-a-runtime", "command": `python "` + script + `"`,
	}
	if blocked := server.managedExecutionEnvironmentBoundary(ctx, "bash", canonical, "engine-a-runtime"); blocked != nil {
		t.Fatalf("canonical execution pack was blocked: %#v", blocked)
	}
}

func TestLoadedSkillGuidanceDoesNotCreateAStringMatchingExecutionGate(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "run one managed workflow"}
	run.addExecutedSkillNames("managed-workflow")
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "managed-workflow", PreferredExecutionAssets: []string{"scripts/run.py"},
	})
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: catalog}, taskRun: run,
		allowedTools: []string{"python"},
	}
	call := agentruntime.ToolCall{
		ID: "alternate", Name: "python",
		Arguments: json.RawMessage(`{"code":"from examplelib import Builder\nBuilder().run()","environment":"science"}`),
	}
	if diagnostic := gateway.toolCallPreflightDiagnostic(context.Background(), call); diagnostic != "" {
		t.Fatalf("Skill guidance became a substring-based execution gate: %s", diagnostic)
	}
}

func TestLoadedSkillDoesNotBlockUnrelatedExecutionBeforePreferredAsset(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "run one managed workflow"}
	run.addExecutedSkillNames("managed-workflow")
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "managed-workflow", PreferredExecutionAssets: []string{"runtime/scripts/run.py"},
	})
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: catalog}, taskRun: run}
	if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("python", map[string]any{
		"code": "print('downstream result formatting')",
	}); blocked != nil {
		t.Fatalf("preferred asset guidance became a global execution gate: %#v", blocked)
	}
}

func TestRegistryManagedExecutionRejectsDirectEngineCallsAndAllowsItsReviewedEntrypoint(t *testing.T) {
	workspace := t.TempDir()
	script := filepath.Join(workspace, ".synon", "runtime", "skills", "managed-workflow-abcd", "scripts", "run.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("import argparse\np=argparse.ArgumentParser()\np.add_argument('--engine')\np.add_argument('--x')\np.add_argument('--y')\np.add_argument('--z')\np.parse_args()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	competing := filepath.Join(workspace, "competing.py")
	if err := os.WriteFile(competing, []byte("from examplelib import Runner\nRunner().run()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{TaskIntent: "run the selected engine"}
	run.addExecutedSkillNames("managed-workflow")
	run.setSelectedImplementations("Engine A")
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "managed-workflow", ImplementationIdentities: []string{"Engine A"},
		PreferredExecutionAssets: []string{"scripts/run.py"},
	})
	scienceCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "managed-capability",
		AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine-a", Package: "examplelib",
			ExecutionPack: sciencecapability.ExecutionPack{
				ID: "managed-capability.engine-a", Mode: "local", Skill: "managed-workflow",
				Executable: "python", Script: "executionpacks/run.py", Imports: []string{"examplelib"},
				CLIWitnesses: []sciencecapability.ExecutionCLIWitness{{Executable: "example-cli"}},
				Parameters: []sciencecapability.ExecutionParameter{
					{Name: "x", Argument: "--x", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "point", EvidenceTerms: []string{"point", "binding-site center"}},
					{Name: "y", Argument: "--y", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "point"},
					{Name: "z", Argument: "--z", Type: "number", Evidence: "resolved-user-input", EvidenceGroup: "point"},
				},
			},
		}},
	}}}
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: catalog, scienceCapabilities: scienceCatalog}, taskRun: run,
		kernel: &agentKernelContext{workspaceDir: workspace},
	}
	for name, input := range map[string]map[string]any{
		"direct bash":                {"command": "which example-cli\nexample-cli --version"},
		"shell variable indirection": {"command": "tool=example-cli; \"$tool\" --version"},
		"pack followed by a second line": {
			"command": `python "` + script + `"` + "\nprintf altered > results/report.md",
		},
		"pack with output redirection": {"command": `python "` + script + `" > results/report.md`},
		"attached interpreter code before pack": {
			"command": `python "-cimport pathlib;pathlib.Path('results/report.md').write_text('tampered')" "` +
				script + `"`,
		},
		"python import":           {"code": "from examplelib import Runner\nRunner().run()"},
		"python argv indirection": {"code": "import subprocess\ncommand = ['example-cli', '--version']\nsubprocess.run(command)"},
		"python subprocess pack path": {"code": `import subprocess, sys
subprocess.run([sys.executable, "` + script + `", "--engine", "example-cli"], check=True)`},
	} {
		t.Run(name, func(t *testing.T) {
			publicName := "bash"
			if input["code"] != nil {
				publicName = "python"
			}
			blocked := gateway.agentRuntimeSkillExecutionContractPreflight(publicName, input)
			if blocked == nil || blocked["status"] != "skill_execution_entrypoint_required" || blocked["executed"] != false {
				t.Fatalf("exclusive execution preflight=%#v", blocked)
			}
		})
	}
	canonicalCommand := `python "` + script + `" --engine example-cli`
	for _, code := range []string{
		"import examplelib\nprint(dir(examplelib))",
		"import examplelib as library\nprint(library.__version__)",
		"import inspect\nfrom examplelib import Runner\nprint(inspect.signature(Runner))",
	} {
		if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("python", map[string]any{"code": code}); blocked != nil {
			t.Fatalf("read-only installed API inspection was blocked: %#v", blocked)
		}
	}
	for _, command := range []string{canonicalCommand + "\n", "# reviewed entry\n" + canonicalCommand + "\n", canonicalCommand + " # execution\n"} {
		if !commandExecutesManagedExecutionPack("managed-workflow", scienceCatalog.Capabilities[0].AcceptedEngines[0].ExecutionPack, command) {
			t.Fatalf("single command formatting lost entrypoint identity: %q", command)
		}
	}
	if tokens, ok := managedExecutionSingleShellCommandTokens(canonicalCommand); !ok {
		t.Fatalf("canonical command did not parse as one argv vector: %q", canonicalCommand)
	} else if len(tokens) != 4 || !agentRuntimeCommandIsSingleMaterializedSkillExecution("bash", canonicalCommand) ||
		!commandExecutesManagedExecutionPack(
			"managed-workflow", scienceCatalog.Capabilities[0].AcceptedEngines[0].ExecutionPack, canonicalCommand,
		) {
		t.Fatalf("canonical command lost exact pack authority: %q", tokens)
	}
	if allowed := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{
		"command": canonicalCommand,
	}); allowed != nil {
		t.Fatalf("reviewed preferred entrypoint was blocked: %#v", allowed)
	}
	redundantCWD := `cd "` + workspace + `" && ` + canonicalCommand
	normalized := gateway.normalizeManagedExecutionRuntimeArguments("bash", map[string]any{"command": redundantCWD})
	if normalized["command"] != canonicalCommand {
		t.Fatalf("exact task-directory prefix was not normalized: %#v", normalized)
	}
	if allowed := gateway.agentRuntimeSkillExecutionContractPreflight("bash", normalized); allowed != nil {
		t.Fatalf("normalized reviewed entrypoint was blocked: %#v", allowed)
	}
	for name, command := range map[string]string{
		"different directory": `cd "` + filepath.Dir(workspace) + `" && ` + canonicalCommand,
		"extra operation":     redundantCWD + ` && printf unexpected`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := gateway.normalizeManagedExecutionRuntimeArguments("bash", map[string]any{"command": command}); got["command"] != command {
				t.Fatalf("unsafe compound command was normalized: %#v", got)
			}
		})
	}
	for name, input := range map[string]map[string]any{
		"python result text": {"code": `print("example-cli")`},
		"shell result text":  {"command": `printf '%s\n' example-cli`},
		"ordinary argument":  {"command": "python wrapper.py --engine example-cli"},
	} {
		t.Run(name, func(t *testing.T) {
			publicName := "bash"
			if input["code"] != nil {
				publicName = "python"
			}
			if blocked := gateway.agentRuntimeSkillExecutionContractPreflight(publicName, input); blocked != nil {
				t.Fatalf("non-executing engine mention was blocked: %#v", blocked)
			}
		})
	}
	for name, command := range map[string]string{
		"workspace copy":  `python "` + competing + `"`,
		"entrypoint copy": `cp "` + script + `" copied.py`,
	} {
		t.Run(name, func(t *testing.T) {
			blocked := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{"command": command})
			if blocked == nil || blocked["status"] != "skill_execution_entrypoint_required" || blocked["competing_source"] == "" {
				t.Fatalf("competing execution source was accepted: %#v", blocked)
			}
		})
	}
	withPoint := `python "` + script + `" --engine example-cli --x 1 --y 2 --z 3`
	if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{"command": withPoint}); blocked == nil || blocked["status"] != "execution_parameter_evidence_required" {
		t.Fatalf("model-derived execution parameters were accepted: %#v", blocked)
	}
	unrelatedNumbers := `python "` + script + `" --engine example-cli --x 3 --y 20 --z 2`
	run.TaskIntent = "run the selected engine with 3 repeats, 20 minutes, and 2 workers"
	if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{"command": unrelatedNumbers}); blocked == nil || blocked["status"] != "execution_parameter_evidence_required" {
		t.Fatalf("unrelated user numbers authorized controlled parameters: %#v", blocked)
	}
	run.TaskIntent = "run this in the data center with values 3 / 20 / 2"
	if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{"command": unrelatedNumbers}); blocked == nil || blocked["status"] != "execution_parameter_evidence_required" {
		t.Fatalf("a broad center word authorized binding-site values: %#v", blocked)
	}
	run.TaskIntent = "Use the validated binding-site center from the attached structure with 3 repeats, a 20 minute budget, and 2 workers."
	if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{"command": unrelatedNumbers}); blocked == nil || blocked["status"] != "execution_parameter_evidence_required" {
		t.Fatalf("non-contiguous operational numbers became a controlled tuple: %#v", blocked)
	}
	run.TaskIntent = "run the selected engine at binding-site center 3 / 20 / 2"
	if allowed := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{"command": unrelatedNumbers}); allowed != nil {
		t.Fatalf("semantically bound user parameter evidence was rejected: %#v", allowed)
	}
	run.TaskIntent = "run the selected engine at point (1, 2, 3)"
	if allowed := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{"command": withPoint}); allowed != nil {
		t.Fatalf("explicit user parameter evidence was rejected: %#v", allowed)
	}
	if allowed := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{
		"command": "cp engine-a-receptor.pdb receptor.pdb",
	}); allowed != nil {
		t.Fatalf("unrelated file operation was blocked: %#v", allowed)
	}
	run.ExecutedSkillNames = nil
	if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("python", map[string]any{
		"code": "import subprocess\nsubprocess.run(['example-cli', '--version'])",
	}); blocked == nil || blocked["status"] != "skill_execution_entrypoint_required" {
		t.Fatalf("selected implementation lost its execution-pack boundary after Skill replay loss: %#v", blocked)
	}
}

func TestExecutionOutputMarkerAloneDoesNotCreateHostMutationAuthority(t *testing.T) {
	workspace := t.TempDir()
	outputDir := filepath.Join(workspace, "results")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, managedExecutionOutputOwnershipMarker), []byte(
		`{"schema":"synon.execution-pack-output-owner.v1","execution_pack_id":"managed-capability.engine-a"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "report.md"), []byte("validated report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway := serverAgentRuntimeToolGateway{
		server:  &Server{skillCatalog: skills.NewCatalog(), scienceCapabilities: &sciencecapability.Catalog{}},
		taskRun: &sessionRunnerChatRun{TaskIntent: "run the managed workflow"},
		kernel:  &agentKernelContext{workspaceDir: workspace},
	}
	if blocked := gateway.agentRuntimeManagedExecutionOutputMutationPreflight(context.Background(), "edit_file", map[string]any{
		"file_path": "results/report.md", "old_string": "validated", "new_string": "translated",
	}); blocked != nil {
		t.Fatalf("a model-writable marker became host mutation authority: %#v", blocked)
	}
	if preflight := gateway.agentRuntimeSkillExecutionContractPreflight("edit_file", map[string]any{
		"file_path": "translated-report.md", "old_string": "", "new_string": "translated",
	}); preflight != nil {
		t.Fatalf("separate narrative file was blocked: %#v", preflight)
	}
	if preflight := gateway.agentRuntimeSkillExecutionContractPreflight("read_file", map[string]any{
		"file_path": "results/report.md",
	}); preflight != nil {
		t.Fatalf("read-only inspection was blocked: %#v", preflight)
	}
	if err := os.WriteFile(filepath.Join(outputDir, managedExecutionOutputOwnershipMarker), []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := gateway.agentRuntimeManagedExecutionOutputMutationPreflight(context.Background(), "edit_file", map[string]any{
		"file_path": "results/report.md", "old_string": "validated", "new_string": "translated",
	})
	if invalid != nil {
		t.Fatalf("an invalid model-writable marker became host mutation authority: %#v", invalid)
	}
}

func TestManagedExecutionPackReceivesCanonicalTaskResponseLanguage(t *testing.T) {
	workspace := t.TempDir()
	script := filepath.Join(workspace, ".synon", "runtime", "skills", "managed-workflow-abcd", "scripts", "run.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(
		"import argparse\np=argparse.ArgumentParser()\np.add_argument('--report-language')\np.parse_args()\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{
		TaskIntent: "运行受管分析并生成中文报告", ResponseLanguage: "zh-CN",
		SelectedImplementations: []string{"Engine A"}, ExecutedSkillNames: []string{"managed-workflow"},
	}
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "managed-workflow", ImplementationIdentities: []string{"Engine A"}})
	scienceCatalog := &sciencecapability.Catalog{Capabilities: []sciencecapability.Definition{{
		ID: "managed-capability",
		AcceptedEngines: []sciencecapability.EngineDefinition{{
			ID: "engine-a", ExecutionPack: sciencecapability.ExecutionPack{
				ID: "managed-capability.engine-a", Mode: "local", Skill: "managed-workflow",
				Executable: "python", Script: "executionpacks/run.py",
				Parameters: []sciencecapability.ExecutionParameter{{
					Name: "report_language", Argument: "--report-language", Type: "string",
					Evidence: "runtime-response-language",
				}},
			},
		}},
	}}}
	gateway := serverAgentRuntimeToolGateway{
		server:  &Server{skillCatalog: catalog, scienceCapabilities: scienceCatalog},
		taskRun: run, kernel: &agentKernelContext{workspaceDir: workspace},
	}
	baseCommand := `python "` + script + `"`
	for _, command := range []string{baseCommand + "\n", baseCommand + " # end\n"} {
		input := gateway.normalizeAdmittedToolArguments("bash", map[string]any{"command": command})
		if got := managedExecutionArgumentValues(stringValue(input["command"]))["--report-language"]; got != "zh" {
			t.Fatalf("formatting swallowed runtime argument: %#v", input)
		}
	}
	normalized := gateway.normalizeAdmittedToolArguments("bash", map[string]any{"command": baseCommand})
	if got := stringValue(normalized["command"]); !strings.HasSuffix(got, "--report-language zh") {
		t.Fatalf("canonical response language was not injected: %q", got)
	}
	if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("bash", normalized); blocked != nil {
		t.Fatalf("runtime-language-bound command was rejected: %#v", blocked)
	}
	override := map[string]any{"command": baseCommand + " --report-language en"}
	override = gateway.normalizeAdmittedToolArguments("bash", override)
	if got := stringValue(override["command"]); strings.Count(got, "--report-language") != 1 {
		t.Fatalf("explicit language override was duplicated: %q", got)
	}
	if blocked := gateway.agentRuntimeSkillExecutionContractPreflight("bash", override); blocked == nil ||
		blocked["status"] != "execution_runtime_parameter_required" {
		t.Fatalf("task-language mismatch was accepted: %#v", blocked)
	}
}

func TestLoadedSkillRequiresCompatibleEnvironmentBeforeBundledScript(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "run one managed workflow"}
	run.addExecutedSkillNames("managed-workflow")
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "managed-workflow", RequiredEnvironmentPackages: []string{"example-runtime", "example-parser>=2"},
	})
	manager := kernelruntime.NewManager(kernelruntime.Config{CondaEnvsPath: t.TempDir()})
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: catalog, kernelManager: manager}, taskRun: run,
	}
	preflight := gateway.agentRuntimeSkillExecutionContractPreflight("bash", map[string]any{
		"environment": "partial-environment",
		"command":     `python "/workspace/.synon/runtime/skills/managed-workflow-abcd/runtime/scripts/run.py" --help`,
	})
	if preflight == nil || preflight["status"] != "skill_environment_preflight_required" {
		t.Fatalf("missing environment preflight=%#v", preflight)
	}
	available := []kernelruntime.ManagedEnvironment{
		{Name: "ready-b", Status: "ready"}, {Name: "ready-a", Status: "ready"}, {Name: "broken", Status: "failed"},
	}
	if selected := selectManagedSkillEnvironment(available, "ready-b"); selected != "ready-b" {
		t.Fatalf("preferred compatible environment selection = %q", selected)
	}
	if selected := selectManagedSkillEnvironment(available, "missing"); selected != "ready-a" {
		t.Fatalf("deterministic compatible environment fallback = %q", selected)
	}
}

func TestLoadedSkillPackageContractDoesNotBlockControlPlaneInspection(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "inspect one loaded workflow before selecting an environment"}
	run.addExecutedSkillNames("managed-workflow")
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "managed-workflow", RequiredEnvironmentPackages: []string{"example-runtime", "example-parser>=2"},
	})
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: catalog}, taskRun: run,
	}
	input := map[string]any{
		"code": `import os; print(os.listdir("/workspace/.synon/runtime/skills/managed-workflow-abcd"))`,
	}
	if preflight := gateway.agentRuntimeSkillExecutionContractPreflight("repl", input); preflight != nil {
		t.Fatalf("control-plane Skill inspection inherited managed package requirements: %#v", preflight)
	}
}

func TestLoadedSkillRejectsIncrementalMutationOfOwnedRuntimePackages(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "run one managed workflow"}
	run.addExecutedSkillNames("managed-workflow")
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "managed-workflow", RequiredEnvironmentPackages: []string{"example-runtime", "example-parser>=2"},
	})
	gateway := serverAgentRuntimeToolGateway{server: &Server{skillCatalog: catalog}, taskRun: run}
	blocked := gateway.agentRuntimeSkillExecutionContractPreflight("manage_packages", map[string]any{
		"mode": "install", "environment": "partial", "packages": []any{"example-parser==2.4"},
	})
	if blocked == nil || blocked["status"] != "skill_environment_immutable_contract_required" {
		t.Fatalf("owned package mutation preflight=%#v", blocked)
	}
	if allowed := gateway.agentRuntimeSkillExecutionContractPreflight("manage_packages", map[string]any{
		"mode": "install", "environment": "partial", "packages": []any{"unrelated-helper"},
	}); allowed != nil {
		t.Fatalf("unrelated package mutation was blocked: %#v", allowed)
	}
}

func TestLoadedSkillDoesNotRevokeAdvertisedRootTool(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "analyze the supplied file"}
	run.addExecutedSkillNames("unrelated-skill")
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: run,
		allowedTools: []string{"read_file"},
	}
	call := agentruntime.ToolCall{
		ID: "read", Name: "read_file",
		Arguments: json.RawMessage(`{"path":"/tmp/input.txt"}`),
	}
	if diagnostic := gateway.toolCallPreflightDiagnostic(context.Background(), call); diagnostic != "" {
		t.Fatalf("loaded Skill revoked an advertised root tool: %s", diagnostic)
	}
}

func TestMaterializedSkillScriptPreflightRejectsMissingEntrypointPrivately(t *testing.T) {
	workspace := t.TempDir()
	scripts := filepath.Join(workspace, ".synon", "runtime", "skills", "workflow-abcd", "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "available.py"), []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := agentRuntimeMaterializedSkillScriptPreflight("bash", map[string]any{
		"command": `python "` + filepath.Join(scripts, "guessed.py") + `" --input data.csv`,
	}, &agentKernelContext{workspaceDir: workspace})
	if result == nil || result["status"] != "skill_entrypoint_preflight_required" ||
		!strings.Contains(stringValue(result["recovery"]), "available.py") {
		t.Fatalf("missing entrypoint preflight=%#v", result)
	}
}

func TestUnresolvedSkillDirectoryPreflightRejectsRootShortcut(t *testing.T) {
	result := agentRuntimeUnresolvedSkillDirectoryPreflight("bash", map[string]any{
		"command": `python "/.synon/runtime/skills/workflow-abcd/scripts/run.py" --help`,
	})
	if result == nil || result["status"] != "skill_runtime_path_preflight_required" {
		t.Fatalf("out-of-workspace Skill lookalike reached process execution: %#v", result)
	}
}

func TestMaterializedSkillScriptPreflightRejectsUnknownArgumentBeforeExecution(t *testing.T) {
	workspace := t.TempDir()
	scripts := filepath.Join(workspace, ".synon", "runtime", "skills", "workflow-abcd", "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scripts, "run.py")
	source := "" +
		"import argparse\n" +
		"parser = argparse.ArgumentParser()\n" +
		"parser.add_argument(\"--input\", required=True)\n" +
		"parser.add_argument(\"--output-dir\", default=\"out\")\n"
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	result := agentRuntimeMaterializedSkillScriptPreflight("bash", map[string]any{
		"command": `python "` + script + `" --inputs data.csv --output-dir out`,
	}, &agentKernelContext{workspaceDir: workspace})
	if result == nil || result["status"] != "skill_arguments_preflight_required" ||
		!strings.Contains(stringValue(result["message"]), "--inputs") ||
		!strings.Contains(stringValue(result["recovery"]), "--input") {
		t.Fatalf("unknown argument preflight=%#v", result)
	}
}

func TestMaterializedSkillScriptPreflightRejectsMissingDeclaredInputsButAllowsOutputs(t *testing.T) {
	workspace := t.TempDir()
	scripts := filepath.Join(workspace, ".synon", "runtime", "skills", "workflow-abcd", "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scripts, "assemble.py")
	source := "" +
		"from pathlib import Path\n" +
		"parser.add_argument(\"--properties\", type=Path, required=True)\n" +
		"parser.add_argument(\"--output-dir\", type=Path, required=True)\n" +
		"properties = read_csv(args.properties, [])\n" +
		"args.output_dir.mkdir(parents=True, exist_ok=True)\n"
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	result := agentRuntimeMaterializedSkillScriptPreflight("bash", map[string]any{
		"command": `python "` + script + `" --properties upstream/data.csv --output-dir final`,
	}, &agentKernelContext{workspaceDir: workspace})
	if result == nil || result["status"] != "skill_input_preflight_required" ||
		!strings.Contains(stringValue(result["message"]), "upstream/data.csv") ||
		strings.Contains(stringValue(result["message"]), "--output-dir") {
		t.Fatalf("missing input preflight=%#v", result)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "upstream"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "upstream", "data.csv"), []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if allowed := agentRuntimeMaterializedSkillScriptPreflight("bash", map[string]any{
		"command": `python "` + script + `" --properties upstream/data.csv --output-dir final`,
	}, &agentKernelContext{workspaceDir: workspace}); allowed != nil {
		t.Fatalf("valid input was blocked: %#v", allowed)
	}
}

func TestMaterializedSkillScriptPreflightExpandsDynamicArguments(t *testing.T) {
	workspace := t.TempDir()
	scripts := filepath.Join(workspace, ".synon", "runtime", "skills", "workflow-abcd", "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scripts, "run.py")
	source := "" +
		"for axis in (\"x\", \"y\", \"z\"):\n" +
		"    parser.add_argument(f\"--size-{axis}\", type=float, required=True)\n"
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := agentRuntimeMaterializedSkillScriptPreflight("bash", map[string]any{
		"command": `python "` + script + `" --size-x 20 --size-y 20 --size-z 20`,
	}, &agentKernelContext{workspaceDir: workspace}); result != nil {
		t.Fatalf("dynamic valid arguments were blocked: %#v", result)
	}
}

func TestMaterializedSkillScriptPreflightFindsResolvedInputsCheckedInLoop(t *testing.T) {
	workspace := t.TempDir()
	scripts := filepath.Join(workspace, ".synon", "runtime", "skills", "workflow-abcd", "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scripts, "dock.py")
	source := "" +
		"parser.add_argument(\"--receptor\", required=True)\n" +
		"parser.add_argument(\"--ligand\", required=True)\n" +
		"receptor_source = (root / args.receptor).resolve()\n" +
		"ligand_source = (root / args.ligand).resolve()\n" +
		"for source in (receptor_source, ligand_source):\n" +
		"    if not source.is_file():\n" +
		"        raise ValueError(\"missing\")\n"
	if err := os.WriteFile(script, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	result := agentRuntimeMaterializedSkillScriptPreflight("bash", map[string]any{
		"command": `python "` + script + `" --receptor receptor.cif --ligand ligands.sdf`,
	}, &agentKernelContext{workspaceDir: workspace})
	if result == nil || result["status"] != "skill_input_preflight_required" ||
		!strings.Contains(stringValue(result["message"]), "receptor.cif") ||
		!strings.Contains(stringValue(result["message"]), "ligands.sdf") {
		t.Fatalf("resolved input loop preflight=%#v", result)
	}
}

func TestMaterializedSkillScriptPreflightRunsInPrivateGatewayAdmission(t *testing.T) {
	workspace := t.TempDir()
	scripts := filepath.Join(workspace, ".synon", "runtime", "skills", "workflow-abcd", "scripts")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scripts, "run.py")
	if err := os.WriteFile(script, []byte("parser.add_argument(\"--input\", required=True)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{TaskIntent: "execute a materialized workflow"}
	run.addExecutedSkillNames("workflow")
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "workflow"})
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{skillCatalog: catalog}, taskRun: run,
		allowedTools: []string{"bash"}, kernel: &agentKernelContext{workspaceDir: workspace},
	}
	call := agentruntime.ToolCall{
		ID: "invalid-script-arguments", Name: "bash",
		Arguments: json.RawMessage(`{"command":"python \"` + script + `\" --inputs data.csv"}`),
	}
	diagnostic := gateway.toolCallPreflightDiagnostic(context.Background(), call)
	if !strings.Contains(diagnostic, "skill_arguments_preflight_required") ||
		!strings.Contains(diagnostic, "--inputs") {
		t.Fatalf("private gateway diagnostic=%q", diagnostic)
	}
}

func TestNamedHostMCPCallUsesLiveSnapshotWithoutSkillAdmission(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "query an attached source"}
	gateway := serverAgentRuntimeToolGateway{
		server: &Server{}, taskRun: run,
		allowedTools: []string{"repl"},
	}
	call := agentruntime.ToolCall{
		ID: "mcp-query", Name: "repl",
		Arguments: json.RawMessage(`{"code":"result = host.mcp(\"literature\", \"search\", query=\"example\")"}`),
	}
	if diagnostic := gateway.toolCallPreflightDiagnostic(context.Background(), call); diagnostic != "" {
		t.Fatalf("named host.mcp call was forced through Skill admission: %s", diagnostic)
	}
}
