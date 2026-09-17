package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkflowBootstrapMappingRejectsConflictsAndInvalidIDs(t *testing.T) {
	for _, name := range []string{"main conflict", "invalid PR ID", "invalid workflow path"} {
		t.Run(name, func(t *testing.T) {
			policy := testPolicy()
			policy.AllowedWorkflows = []string{".github/workflows/quality-pr.yml", ".github/workflows/quality.yml"}
			policy.WorkflowBootstrapJobs = map[string]string{
				".github/workflows/quality-pr.yml": "pr", ".github/workflows/quality.yml": "test",
			}
			policy.WorkflowCheckouts = map[string]checkoutContract{
				".github/workflows/quality-pr.yml": {Ref: "$" + "{{ github.sha }}", FetchDepth: 2},
				".github/workflows/quality.yml":    {Ref: "$" + "{{ github.sha }}", FetchDepth: 1},
			}
			switch name {
			case "main conflict":
				policy.WorkflowBootstrapJobs[policy.BootstrapWorkflow] = "different"
			case "invalid PR ID":
				policy.WorkflowBootstrapJobs[".github/workflows/quality-pr.yml"] = "pr quality"
			case "invalid workflow path":
				policy.AllowedWorkflows[0] = ".github/workflows/sub/quality.yml"
			}
			if err := validatePolicy(policy); err == nil {
				t.Fatal("invalid workflow authority was accepted")
			}
		})
	}
}

func repositoryWorkflow(t *testing.T, name string) *yaml.Node {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseWorkflow(data)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func namedRun(root *yaml.Node, job, step string) string {
	steps := mappingValue(mappingValue(mappingValue(root, "jobs"), job), "steps")
	if steps == nil {
		return ""
	}
	for _, item := range steps.Content {
		if name := mappingValue(item, "name"); name != nil && name.Value == step {
			if run := mappingValue(item, "run"); run != nil {
				return strings.TrimSpace(run.Value)
			}
		}
	}
	return ""
}

func TestRepositoryWorkflowRoutingAndChangedScopeRuntimeEnvironment(t *testing.T) {
	full := repositoryWorkflow(t, "quality.yml")
	pr := repositoryWorkflow(t, "quality-pr.yml")
	main := repositoryWorkflow(t, "quality-main.yml")
	fullEvents := mappingValue(full, "on")
	if fullEvents == nil || len(fullEvents.Content) != 4 || mappingValue(fullEvents, "workflow_dispatch") == nil {
		t.Fatal("full CI must be scheduled and manually dispatched, without redundant push/PR runs")
	}
	schedule := mappingValue(fullEvents, "schedule")
	if schedule == nil || len(schedule.Content) != 1 {
		t.Fatal("expected one daily full-quality schedule")
	}
	cron, timezone := mappingValue(schedule.Content[0], "cron"), mappingValue(schedule.Content[0], "timezone")
	if cron == nil || cron.Value != "0 2 * * *" || timezone == nil || timezone.Value != "Asia/Shanghai" {
		t.Fatal("full CI must run daily at 02:00 Asia/Shanghai")
	}
	prEvents := mappingValue(pr, "on")
	if prEvents == nil || len(prEvents.Content) != 2 || mappingValue(prEvents, "pull_request") == nil {
		t.Fatal("fast CI must run for pull requests")
	}
	mainEvents := mappingValue(main, "on")
	push := mappingValue(mainEvents, "push")
	branches := mappingValue(push, "branches")
	if mainEvents == nil || push == nil || branches == nil || len(branches.Content) != 1 ||
		branches.Content[0].Value != "main" {
		t.Fatal("main integration CI must run only for main pushes")
	}
	fullSetup := namedRun(full, "runtime-quality", "Set up runtime test dependencies")
	if fullSetup == "" || namedRun(pr, "pr-go-tests", "Set up runtime test dependencies") != fullSetup {
		t.Fatal("PR runtime tests must retain the full runtime environment")
	}
	if namedRun(main, "main-go-tests", "Set up runtime test dependencies") != fullSetup {
		t.Fatal("main affected runtime tests must retain the full runtime environment")
	}
	for _, workflow := range []struct {
		root *yaml.Node
		jobs []string
	}{
		{pr, []string{"pr-quality", "pr-go-tests", "pr-frontend-tests"}},
		{main, []string{"main-quality", "main-go-tests", "main-frontend-tests"}},
	} {
		for _, job := range workflow.jobs {
			steps := mappingValue(mappingValue(mappingValue(workflow.root, "jobs"), job), "steps")
			checkout := steps.Content[0]
			inputs := mappingValue(checkout, "with")
			depth := mappingValue(inputs, "fetch-depth")
			ref := mappingValue(inputs, "ref")
			if depth == nil || depth.Value != "2" || ref == nil || ref.Value != "$"+"{{ github.sha }}" {
				t.Fatalf("%s checkout must bind the exact change commit and its parent", job)
			}
			for _, step := range steps.Content {
				if run := mappingValue(step, "run"); run != nil && strings.Contains(run.Value, "git fetch") {
					t.Fatal("private change checkout must not fetch after credentials were removed")
				}
			}
		}
	}
}
