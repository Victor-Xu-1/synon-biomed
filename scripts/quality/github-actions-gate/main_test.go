package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const checkoutSHA = "d23441a48e516b6c34aea4fa41551a30e30af803"
const setupGoSHA = "924ae3a1cded613372ab5595356fb5720e22ba16"

func testPolicy() actionsPolicy {
	return actionsPolicy{
		Schema:            policySchema,
		UpdatedAt:         "2026-07-29T00:00:00Z",
		Source:            "test",
		BootstrapWorkflow: ".github/workflows/quality.yml",
		BootstrapJob:      "test",
		BootstrapRunner:   "ubuntu-24.04",
		GateStepName:      "Verify immutable GitHub Actions policy",
		AllowedWorkflows:  []string{".github/workflows/quality.yml"},
		WorkflowBootstrapJobs: map[string]string{
			".github/workflows/quality.yml": "test",
		},
		WorkflowCheckouts: map[string]checkoutContract{
			".github/workflows/quality.yml": {Ref: "$" + "{{ github.sha }}", FetchDepth: 1},
		},
		Actions: map[string]actionPin{
			"actions/checkout": {SHA: checkoutSHA, Version: "v6.1.0"},
			"actions/setup-go": {SHA: setupGoSHA, Version: "v6.5.0"},
		},
		Requirements: policyRequirements{
			RemoteActionsFullSHA: true, VersionCommentRequired: true,
			UnlistedRemoteActionForbidden: true, ContinueOnErrorMustBeFalse: true,
			ShellOrListForbidden: true, PersistCredentialsFalseRequired: true,
			LocalActionsForbidden: true, DockerActionsForbidden: true,
			YAMLAnchorsAndAliasesForbidden: true, RequiredRunBlockExact: true,
			ConditionalsForbidden: true, DefaultsForbidden: true,
			EnvironmentOverridesForbidden:   true,
			ReusableWorkflowsForbidden:      true,
			ContainersForbidden:             true,
			ServicesForbidden:               true,
			JobPermissionOverridesForbidden: true,
			RootPermissionsReadOnly:         true,
		},
		RequiredRunCommands: []string{
			"go test -buildvcs=false ./scripts/quality/github-actions-gate",
			"go run -buildvcs=false ./scripts/quality/github-actions-gate --repo . --policy docs/governance/github-actions-pins.json",
		},
	}
}

func validWorkflow() string {
	return `name: quality
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@` + checkoutSHA + ` # v6.1.0
        with:
          ref: ${{ github.sha }}
          fetch-depth: 1
          persist-credentials: false
      - name: Verify immutable GitHub Actions policy
        run: |
          go test -buildvcs=false ./scripts/quality/github-actions-gate
          go run -buildvcs=false ./scripts/quality/github-actions-gate --repo . --policy docs/governance/github-actions-pins.json
      - uses: actions/setup-go@` + setupGoSHA + ` # v6.5.0
`
}

func workflowFixture(t *testing.T, workflow string) string {
	t.Helper()
	root := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	path := filepath.Join(root, ".github", "workflows", "quality.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(workflow), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", root, "add", ".github/workflows/quality.yml").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, output)
	}
	return root
}

func requireGateError(t *testing.T, workflow, code string) {
	t.Helper()
	_, err := check(context.Background(), workflowFixture(t, workflow), testPolicy())
	if err == nil || err.Error() != code {
		t.Fatalf("gate error=%v want=%s", err, code)
	}
}

func TestGateAcceptsExactStructuredWorkflow(t *testing.T) {
	result, err := check(context.Background(), workflowFixture(t, validWorkflow()), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkflowCount != 1 || result.ActionUses != 2 || len(result.Actions) != 2 || result.Actions[0] != "actions/checkout" || result.Actions[1] != "actions/setup-go" {
		t.Fatalf("result=%#v", result)
	}
}

func TestGateAcceptsConfiguredPullRequestWorkflowPair(t *testing.T) {
	policy := testPolicy()
	policy.AllowedWorkflows = []string{".github/workflows/quality-pr.yml", ".github/workflows/quality.yml"}
	policy.WorkflowBootstrapJobs = map[string]string{
		".github/workflows/quality-pr.yml": "pr",
		".github/workflows/quality.yml":    "test",
	}
	policy.WorkflowCheckouts = map[string]checkoutContract{
		".github/workflows/quality-pr.yml": {Ref: "$" + "{{ github.sha }}", FetchDepth: 2},
		".github/workflows/quality.yml":    {Ref: "$" + "{{ github.sha }}", FetchDepth: 1},
	}
	policy.RequiredAggregates = map[string]aggregateContract{
		".github/workflows/quality-pr.yml": {
			JobID: "merge", DisplayName: "Merge Gate / CI required", Needs: []string{"pr", "tests"},
		},
	}
	root := workflowFixture(t, validWorkflow())
	path := filepath.Join(root, ".github", "workflows", "quality-pr.yml")
	if err := os.WriteFile(path, []byte(validPRWorkflow()), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", root, "add", ".github/workflows/quality-pr.yml").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, output)
	}
	result, err := check(context.Background(), root, policy)
	if err != nil {
		t.Fatal(err)
	}
	if result.WorkflowCount != 2 || result.ActionUses != 5 {
		t.Fatalf("result=%#v", result)
	}
}

func validPRWorkflow() string {
	return `name: fast
permissions:
  contents: read
jobs:
  pr:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@` + checkoutSHA + ` # v6.1.0
        with:
          ref: ${{ github.sha }}
          fetch-depth: 2
          persist-credentials: false
      - name: Verify immutable GitHub Actions policy
        run: |
          go test -buildvcs=false ./scripts/quality/github-actions-gate
          go run -buildvcs=false ./scripts/quality/github-actions-gate --repo . --policy docs/governance/github-actions-pins.json
      - uses: actions/setup-go@` + setupGoSHA + ` # v6.5.0
  tests:
    needs: pr
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@` + checkoutSHA + ` # v6.1.0
        with:
          ref: ${{ github.sha }}
          fetch-depth: 2
          persist-credentials: false
  merge:
    name: Merge Gate / CI required
    needs: [pr, tests]
    if: always()
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - name: Verify all required job results
        run: |
          test '${{ needs.pr.result }}' = 'success'
          test '${{ needs.tests.result }}' = 'success'
`
}

func aggregatePairPolicy() actionsPolicy {
	policy := testPolicy()
	policy.AllowedWorkflows = []string{".github/workflows/quality-pr.yml", ".github/workflows/quality.yml"}
	policy.WorkflowBootstrapJobs = map[string]string{
		".github/workflows/quality-pr.yml": "pr",
		".github/workflows/quality.yml":    "test",
	}
	policy.WorkflowCheckouts = map[string]checkoutContract{
		".github/workflows/quality-pr.yml": {Ref: "$" + "{{ github.sha }}", FetchDepth: 2},
		".github/workflows/quality.yml":    {Ref: "$" + "{{ github.sha }}", FetchDepth: 1},
	}
	policy.RequiredAggregates = map[string]aggregateContract{
		".github/workflows/quality-pr.yml": {
			JobID: "merge", DisplayName: "Merge Gate / CI required", Needs: []string{"pr", "tests"},
		},
	}
	return policy
}

func checkWorkflowPair(t *testing.T, prWorkflow string) error {
	t.Helper()
	root := workflowFixture(t, validWorkflow())
	path := filepath.Join(root, ".github", "workflows", "quality-pr.yml")
	if err := os.WriteFile(path, []byte(prWorkflow), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", root, "add", ".github/workflows/quality-pr.yml").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, output)
	}
	_, err := check(context.Background(), root, aggregatePairPolicy())
	return err
}

func TestRequiredAggregateRejectsAnyContractDrift(t *testing.T) {
	tests := []string{
		strings.Replace(validPRWorkflow(), "if: always()", "if: success()", 1),
		strings.Replace(validPRWorkflow(), "needs: [pr, tests]", "needs: [pr]", 1),
		strings.Replace(validPRWorkflow(), "Merge Gate / CI required", "Weak Gate", 1),
		strings.Replace(validPRWorkflow(), " = 'success'", " = 'skipped'", 1),
	}
	for index, workflow := range tests {
		if err := checkWorkflowPair(t, workflow); err == nil || !strings.Contains(err.Error(), "aggregate") {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
}

func TestAggregateRunFailsForFailureCancelledSkippedAndMissing(t *testing.T) {
	template := aggregateRunBlock([]string{"one", "two"})
	one := "$" + "{{ needs.one.result }}"
	two := "$" + "{{ needs.two.result }}"
	for _, result := range []string{"failure", "cancelled", "skipped", ""} {
		command := strings.ReplaceAll(strings.ReplaceAll(template, one, result), two, "success")
		if err := exec.Command("bash", "-euo", "pipefail", "-c", command).Run(); err == nil {
			t.Fatalf("aggregate accepted result %q", result)
		}
	}
	command := strings.ReplaceAll(strings.ReplaceAll(template, one, "success"), two, "success")
	if output, err := exec.Command("bash", "-euo", "pipefail", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("all-success aggregate failed: %v\n%s", err, output)
	}
}

func TestGateRejectsMutableWrongUnlistedAndMissingComments(t *testing.T) {
	tests := []struct {
		name, replacement, code string
	}{
		{"mutable", "actions/checkout@v6 # v6.1.0", "actions_reference_invalid"},
		{"wrong-sha", "actions/checkout@" + strings.Repeat("f", 40) + " # v6.1.0", "actions_reference_not_pinned"},
		{"missing-comment", "actions/checkout@" + checkoutSHA, "actions_reference_not_pinned"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflow := strings.Replace(validWorkflow(), "actions/checkout@"+checkoutSHA+" # v6.1.0", test.replacement, 1)
			requireGateError(t, workflow, test.code)
		})
	}
	unlisted := validWorkflow() + "      - uses: example/action@" + strings.Repeat("a", 40) + " # v1.0.0\n"
	requireGateError(t, unlisted, "actions_reference_not_pinned")
}

func TestGateParsesQuotedAndFlowUsesInsteadOfIgnoringThem(t *testing.T) {
	quoted := validWorkflow() + "      - \"uses\": example/action@" + strings.Repeat("a", 40) + " # v1.0.0\n"
	requireGateError(t, quoted, "actions_reference_not_pinned")
	flow := validWorkflow() + "      - { uses: example/action@" + strings.Repeat("a", 40) + " }\n"
	requireGateError(t, flow, "actions_reference_not_pinned")
}

func TestGateRejectsLocalDockerAliasesAndDuplicateKeys(t *testing.T) {
	requireGateError(t, validWorkflow()+"      - uses: ./local-action\n", "actions_local_action_forbidden")
	requireGateError(t, validWorkflow()+"      - uses: docker://alpine:latest\n", "actions_docker_action_forbidden")
	alias := validWorkflow() + "      - &shared\n        uses: example/action@" + strings.Repeat("a", 40) + " # v1.0.0\n      - *shared\n"
	requireGateError(t, alias, "actions_yaml_alias_forbidden")
	duplicate := strings.Replace(validWorkflow(), "      - name: Verify immutable GitHub Actions policy", "      - name: Verify immutable GitHub Actions policy\n        name: Duplicate", 1)
	requireGateError(t, duplicate, "actions_yaml_duplicate_key")
}

func TestGateRejectsNonFalseContinueOnErrorAndShellBypass(t *testing.T) {
	for _, value := range []string{"true", "${{ true }}", "1"} {
		workflow := strings.Replace(validWorkflow(), "      - name: Verify immutable GitHub Actions policy", "      - name: Verify immutable GitHub Actions policy\n        continue-on-error: "+value, 1)
		requireGateError(t, workflow, "actions_continue_on_error_forbidden")
	}
	for _, command := range []string{"verify||true", "verify || true; next", "verify || recover"} {
		bypass := strings.Replace(validWorkflow(), "          go test", "          "+command+"\n          go test", 1)
		requireGateError(t, bypass, "actions_failure_bypass_forbidden")
	}
}

func TestGateRequiresCheckoutFencingAndExecutableSelfCommands(t *testing.T) {
	missingCredentials := strings.Replace(validWorkflow(), "          persist-credentials: false\n", "", 1)
	requireGateError(t, missingCredentials, "actions_checkout_contract_invalid")
	trueCredentials := strings.Replace(validWorkflow(), "persist-credentials: false", "persist-credentials: true", 1)
	requireGateError(t, trueCredentials, "actions_checkout_contract_invalid")
	for _, replacement := range []string{
		"ref: attacker",
		"fetch-depth: 0",
		"persist-credentials: false\n          repository: attacker/public",
	} {
		original := strings.SplitN(replacement, ":", 2)[0]
		if original == "persist-credentials" {
			original = "persist-credentials: false"
		} else if original == "ref" {
			original = "ref: $" + "{{ github.sha }}"
		} else {
			original = "fetch-depth: 1"
		}
		alternate := strings.Replace(validWorkflow(), original, replacement, 1)
		requireGateError(t, alternate, "actions_checkout_contract_invalid")
	}
	commented := strings.Replace(validWorkflow(), "          go run -buildvcs=false", "          # go run -buildvcs=false", 1)
	requireGateError(t, commented, "actions_bootstrap_order_invalid")
	guarded := strings.Replace(validWorkflow(), "          go test -buildvcs=false", "          if false; then\n          go test -buildvcs=false", 1)
	guarded = strings.Replace(guarded, " --policy docs/governance/github-actions-pins.json\n", " --policy docs/governance/github-actions-pins.json\n          fi\n", 1)
	requireGateError(t, guarded, "actions_bootstrap_order_invalid")
	heredoc := strings.Replace(validWorkflow(), "          go test -buildvcs=false", "          cat <<'EOF'\n          go test -buildvcs=false", 1)
	heredoc = strings.Replace(heredoc, " --policy docs/governance/github-actions-pins.json\n", " --policy docs/governance/github-actions-pins.json\n          EOF\n", 1)
	requireGateError(t, heredoc, "actions_bootstrap_order_invalid")
	customShell := strings.Replace(validWorkflow(), "      - name: Verify immutable GitHub Actions policy", "      - name: Verify immutable GitHub Actions policy\n        shell: python", 1)
	requireGateError(t, customShell, "actions_required_command_invalid")
}

func TestGateRejectsConditionsDefaultsAndEnvironmentOverrides(t *testing.T) {
	conditional := strings.Replace(validWorkflow(), "      - name: Verify immutable GitHub Actions policy", "      - name: Verify immutable GitHub Actions policy\n        if: ${{ false }}", 1)
	requireGateError(t, conditional, "actions_conditional_forbidden")
	requireGateError(t, "defaults:\n  run:\n    shell: python\n"+validWorkflow(), "actions_defaults_forbidden")
	requireGateError(t, "env:\n  GOFLAGS: -run=^$\n"+validWorkflow(), "actions_environment_override_forbidden")
}

func TestGatePermitsOnlyUnconditionalEvidenceCollectionAfterFailure(t *testing.T) {
	policy := testPolicy()
	policy.Requirements.UnconditionalTestEvidenceAllowed = true
	for _, command := range []string{"bash scripts/audit/audit-source-clean.sh"} {
		workflow := validWorkflow() + "      - name: Audit residue\n        if: always()\n        run: " + command + "\n"
		if _, err := check(context.Background(), workflowFixture(t, workflow), policy); err != nil {
			t.Fatal(err)
		}
		for _, condition := range []string{"false", "success()", "always() && false", "${{ always() }}"} {
			bad := strings.Replace(workflow, "if: always()", "if: "+condition, 1)
			if _, err := check(context.Background(), workflowFixture(t, bad), policy); err == nil {
				t.Fatalf("conditional evidence gate %q was accepted", condition)
			}
		}
	}
	guardedTest := validWorkflow() + "      - name: Required runtime gate\n        if: always()\n        run: go test ./...\n"
	if _, err := check(context.Background(), workflowFixture(t, guardedTest), policy); err == nil {
		t.Fatal("conditional non-evidence step accepted")
	}
	bootstrap := strings.Replace(validWorkflow(), "      - name: Verify immutable GitHub Actions policy", "      - name: Verify immutable GitHub Actions policy\n        if: always()", 1)
	if _, err := check(context.Background(), workflowFixture(t, bootstrap), policy); err == nil {
		t.Fatal("conditional bootstrap gate accepted")
	}
}

func TestGateIgnoresNestedActionInputsAndRequiresRealSteps(t *testing.T) {
	commands := "go test -buildvcs=false ./scripts/quality/github-actions-gate\n" +
		"go run -buildvcs=false ./scripts/quality/github-actions-gate --repo . --policy docs/governance/github-actions-pins.json"
	realStep := "      - name: Verify immutable GitHub Actions policy\n        run: |\n          " + strings.ReplaceAll(commands, "\n", "\n          ") + "\n"
	forged := strings.Replace(validWorkflow(), realStep, "", 1)
	forged = strings.Replace(forged, "      - uses: actions/setup-go@"+setupGoSHA+" # v6.5.0", "      - uses: actions/setup-go@"+setupGoSHA+" # v6.5.0\n        with:\n          name: Verify immutable GitHub Actions policy\n          run: |\n            "+strings.ReplaceAll(commands, "\n", "\n            "), 1)
	requireGateError(t, forged, "actions_bootstrap_order_invalid")

	nestedUse := strings.Replace(validWorkflow(), "      - uses: actions/setup-go@"+setupGoSHA+" # v6.5.0", "      - uses: actions/setup-go@"+setupGoSHA+" # v6.5.0\n        with:\n          uses: example/action@"+strings.Repeat("a", 40), 1)
	result, err := check(context.Background(), workflowFixture(t, nestedUse), testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if result.ActionUses != 2 || len(result.Actions) != 2 || result.Actions[0] != "actions/checkout" || result.Actions[1] != "actions/setup-go" {
		t.Fatalf("nested action input changed coverage: %#v", result)
	}

	reusable := strings.Replace(validWorkflow(), "    steps:", "    uses: example/reusable@"+strings.Repeat("a", 40)+"\n    steps:", 1)
	requireGateError(t, reusable, "actions_reusable_workflow_forbidden")
}

func TestGateRequiresBootstrapOrderAndDependentJobs(t *testing.T) {
	gate := "      - name: Verify immutable GitHub Actions policy\n        run: |\n          go test -buildvcs=false ./scripts/quality/github-actions-gate\n          go run -buildvcs=false ./scripts/quality/github-actions-gate --repo . --policy docs/governance/github-actions-pins.json\n"
	setup := "      - uses: actions/setup-go@" + setupGoSHA + " # v6.5.0\n"
	reordered := strings.Replace(validWorkflow(), gate+setup, setup+gate, 1)
	requireGateError(t, reordered, "actions_bootstrap_order_invalid")

	independent := validWorkflow() + "  other:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: echo unsafe\n"
	requireGateError(t, independent, "actions_job_dependency_invalid")
	dependent := validWorkflow() + "  other:\n    needs: test\n    runs-on: ubuntu-24.04\n    steps:\n      - run: echo safe\n"
	if _, err := check(context.Background(), workflowFixture(t, dependent), testPolicy()); err != nil {
		t.Fatal(err)
	}
}

func TestGateBindsBootstrapRunnerAndForbidsPreStepExecutionSurfaces(t *testing.T) {
	wrongRunner := strings.Replace(validWorkflow(), "runs-on: ubuntu-24.04", "runs-on: self-hosted", 1)
	requireGateError(t, wrongRunner, "actions_bootstrap_runner_invalid")
	container := strings.Replace(validWorkflow(), "    runs-on: ubuntu-24.04", "    runs-on: ubuntu-24.04\n    container: attacker/image:latest", 1)
	requireGateError(t, container, "actions_container_forbidden")
	services := strings.Replace(validWorkflow(), "    runs-on: ubuntu-24.04", "    runs-on: ubuntu-24.04\n    services:\n      db:\n        image: attacker/db:latest", 1)
	requireGateError(t, services, "actions_services_forbidden")
	jobPermissions := strings.Replace(validWorkflow(), "    runs-on: ubuntu-24.04", "    runs-on: ubuntu-24.04\n    permissions: write-all", 1)
	requireGateError(t, jobPermissions, "actions_job_permissions_forbidden")
	rootPermissions := strings.Replace(validWorkflow(), "  contents: read", "  contents: write", 1)
	requireGateError(t, rootPermissions, "actions_root_permissions_invalid")
}

func TestValidatePolicyRejectsIncompleteContracts(t *testing.T) {
	policy := testPolicy()
	policy.Requirements.DockerActionsForbidden = false
	if err := validatePolicy(policy); err == nil || err.Error() != "actions_policy_shape_invalid" {
		t.Fatalf("error=%v", err)
	}
	policy = testPolicy()
	policy.Actions["actions/checkout"] = actionPin{SHA: "not-a-sha", Version: "v6.1.0"}
	if err := validatePolicy(policy); err == nil || err.Error() != "actions_policy_action_invalid" {
		t.Fatalf("error=%v", err)
	}
}

func TestValidatePolicyRejectsMutableCheckoutAndAggregateContracts(t *testing.T) {
	policy := testPolicy()
	policy.WorkflowCheckouts[".github/workflows/quality.yml"] = checkoutContract{
		Ref: "main", FetchDepth: 1,
	}
	if err := validatePolicy(policy); err == nil || err.Error() != "actions_policy_checkout_invalid" {
		t.Fatalf("mutable checkout error=%v", err)
	}

	policy = testPolicy()
	policy.RequiredAggregates = map[string]aggregateContract{
		".github/workflows/quality.yml": {
			JobID: "merge", DisplayName: "Merge Gate", Needs: []string{"z-job", "test"},
		},
	}
	if err := validatePolicy(policy); err == nil || err.Error() != "actions_policy_aggregate_invalid" {
		t.Fatalf("unsorted aggregate error=%v", err)
	}

	policy = testPolicy()
	policy.RequiredAggregates = map[string]aggregateContract{
		".github/workflows/not-allowed.yml": {
			JobID: "merge", DisplayName: "Merge Gate", Needs: []string{"other", "test"},
		},
	}
	if err := validatePolicy(policy); err == nil || err.Error() != "actions_policy_aggregate_invalid" {
		t.Fatalf("unlisted aggregate error=%v", err)
	}
}
