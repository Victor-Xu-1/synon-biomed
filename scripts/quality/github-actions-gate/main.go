package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const policySchema = "synon.governance.github-actions-pins.v3"

var (
	actionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	fullSHAPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	versionPattern    = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	remoteUsePattern  = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@([0-9a-f]{40})$`)
	failureBypass     = regexp.MustCompile(`\|\|`)
)

type actionPin struct {
	SHA     string `json:"sha"`
	Version string `json:"version"`
}

type policyRequirements struct {
	RemoteActionsFullSHA             bool `json:"remote_actions_full_sha"`
	VersionCommentRequired           bool `json:"version_comment_required"`
	UnlistedRemoteActionForbidden    bool `json:"unlisted_remote_action_forbidden"`
	ContinueOnErrorMustBeFalse       bool `json:"continue_on_error_must_be_false"`
	ShellOrListForbidden             bool `json:"shell_or_list_forbidden"`
	PersistCredentialsFalseRequired  bool `json:"persist_credentials_false_required_for_checkout"`
	LocalActionsForbidden            bool `json:"local_actions_forbidden"`
	DockerActionsForbidden           bool `json:"docker_actions_forbidden"`
	YAMLAnchorsAndAliasesForbidden   bool `json:"yaml_anchors_and_aliases_forbidden"`
	RequiredRunBlockExact            bool `json:"required_run_block_exact"`
	ConditionalsForbidden            bool `json:"conditionals_forbidden"`
	UnconditionalTestEvidenceAllowed bool `json:"unconditional_test_evidence_allowed,omitempty"`
	DefaultsForbidden                bool `json:"defaults_forbidden"`
	EnvironmentOverridesForbidden    bool `json:"environment_overrides_forbidden"`
	ReusableWorkflowsForbidden       bool `json:"reusable_workflows_forbidden"`
	ContainersForbidden              bool `json:"containers_forbidden"`
	ServicesForbidden                bool `json:"services_forbidden"`
	JobPermissionOverridesForbidden  bool `json:"job_permission_overrides_forbidden"`
	RootPermissionsReadOnly          bool `json:"root_permissions_read_only"`
}

type actionsPolicy struct {
	Schema                string                       `json:"schema"`
	UpdatedAt             string                       `json:"updated_at"`
	Source                string                       `json:"source"`
	BootstrapWorkflow     string                       `json:"bootstrap_workflow"`
	BootstrapJob          string                       `json:"bootstrap_job"`
	BootstrapRunner       string                       `json:"bootstrap_runner"`
	GateStepName          string                       `json:"gate_step_name"`
	AllowedWorkflows      []string                     `json:"allowed_workflows,omitempty"`
	WorkflowBootstrapJobs map[string]string            `json:"workflow_bootstrap_jobs,omitempty"`
	WorkflowCheckouts     map[string]checkoutContract  `json:"workflow_checkout_contracts,omitempty"`
	RequiredAggregates    map[string]aggregateContract `json:"required_aggregate_jobs,omitempty"`
	PackagePublishing     map[string]packageContract   `json:"package_publishing,omitempty"`
	Actions               map[string]actionPin         `json:"actions"`
	Requirements          policyRequirements           `json:"requirements"`
	RequiredRunCommands   []string                     `json:"required_run_commands"`
}

type gateResult struct {
	WorkflowCount int      `json:"workflow_count"`
	ActionUses    int      `json:"action_uses"`
	Actions       []string `json:"actions"`
}

type workflowState struct {
	policy          actionsPolicy
	observed        map[string]int
	selfGateBlocks  int
	bootstrapJobs   int
	aggregateJobs   int
	currentWorkflow string
	currentJob      string
}

func loadPolicy(path string) (actionsPolicy, error) {
	data, err := readRegularSingleLink(path)
	if err != nil {
		return actionsPolicy{}, errors.New("actions_policy_invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy actionsPolicy
	if err := decoder.Decode(&policy); err != nil {
		return actionsPolicy{}, errors.New("actions_policy_invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return actionsPolicy{}, errors.New("actions_policy_invalid")
	}
	if err := validatePolicy(policy); err != nil {
		return actionsPolicy{}, err
	}
	return policy, nil
}

func validatePolicy(policy actionsPolicy) error {
	if policy.Schema != policySchema || strings.TrimSpace(policy.Source) == "" || len(policy.Actions) == 0 {
		return errors.New("actions_policy_shape_invalid")
	}
	if _, err := time.Parse(time.RFC3339, policy.UpdatedAt); err != nil {
		return errors.New("actions_policy_shape_invalid")
	}
	if policy.BootstrapWorkflow != ".github/workflows/quality.yml" ||
		!regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`).MatchString(policy.BootstrapJob) ||
		policy.BootstrapRunner != "ubuntu-24.04" ||
		strings.TrimSpace(policy.GateStepName) == "" || len(policy.GateStepName) > 128 {
		return errors.New("actions_policy_shape_invalid")
	}
	if len(policy.AllowedWorkflows) > 0 {
		allowed := append([]string(nil), policy.AllowedWorkflows...)
		sort.Strings(allowed)
		if !reflect.DeepEqual(allowed, policy.AllowedWorkflows) || len(allowed) != len(uniqueStrings(allowed)) {
			return errors.New("actions_policy_workflow_invalid")
		}
		if !containsString(allowed, policy.BootstrapWorkflow) ||
			len(policy.WorkflowBootstrapJobs) != len(allowed) ||
			len(policy.WorkflowCheckouts) != len(allowed) {
			return errors.New("actions_policy_workflow_invalid")
		}
		if policy.WorkflowBootstrapJobs[policy.BootstrapWorkflow] != policy.BootstrapJob {
			return errors.New("actions_policy_workflow_invalid")
		}
		for _, workflow := range allowed {
			if !regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9_-]+\.ya?ml$`).MatchString(workflow) ||
				!regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`).MatchString(policy.WorkflowBootstrapJobs[workflow]) {
				return errors.New("actions_policy_workflow_invalid")
			}
			checkout, ok := policy.WorkflowCheckouts[workflow]
			if !ok || checkout.Ref != "$"+"{{ github.sha }}" || checkout.FetchDepth < 1 || checkout.FetchDepth > 2 {
				return errors.New("actions_policy_checkout_invalid")
			}
		}
		for workflow, aggregate := range policy.RequiredAggregates {
			if !containsString(allowed, workflow) ||
				!regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`).MatchString(aggregate.JobID) ||
				strings.TrimSpace(aggregate.DisplayName) == "" || len(aggregate.DisplayName) > 128 ||
				len(aggregate.Needs) < 2 || len(aggregate.Needs) != len(uniqueStrings(aggregate.Needs)) ||
				!sort.StringsAreSorted(aggregate.Needs) ||
				!containsString(aggregate.Needs, policy.WorkflowBootstrapJobs[workflow]) ||
				containsString(aggregate.Needs, aggregate.JobID) {
				return errors.New("actions_policy_aggregate_invalid")
			}
			for _, job := range aggregate.Needs {
				if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`).MatchString(job) {
					return errors.New("actions_policy_aggregate_invalid")
				}
			}
		}
	} else if len(policy.WorkflowBootstrapJobs) != 0 || len(policy.WorkflowCheckouts) != 0 ||
		len(policy.RequiredAggregates) != 0 {
		return errors.New("actions_policy_workflow_invalid")
	}
	requirements := policy.Requirements
	for workflow, contract := range policy.PackagePublishing {
		if !containsString(policy.AllowedWorkflows, workflow) ||
			!regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`).MatchString(contract.Job) ||
			contract.Job == policy.WorkflowBootstrapJobs[workflow] {
			return errors.New("actions_package_policy_invalid")
		}
	}
	if !requirements.RemoteActionsFullSHA || !requirements.VersionCommentRequired ||
		!requirements.UnlistedRemoteActionForbidden || !requirements.ContinueOnErrorMustBeFalse ||
		!requirements.ShellOrListForbidden || !requirements.PersistCredentialsFalseRequired ||
		!requirements.LocalActionsForbidden || !requirements.DockerActionsForbidden ||
		!requirements.YAMLAnchorsAndAliasesForbidden || !requirements.RequiredRunBlockExact ||
		!requirements.ConditionalsForbidden || !requirements.DefaultsForbidden ||
		!requirements.EnvironmentOverridesForbidden || !requirements.ReusableWorkflowsForbidden ||
		!requirements.ContainersForbidden || !requirements.ServicesForbidden ||
		!requirements.JobPermissionOverridesForbidden || !requirements.RootPermissionsReadOnly {
		return errors.New("actions_policy_shape_invalid")
	}
	for name, pin := range policy.Actions {
		if !actionNamePattern.MatchString(name) || !fullSHAPattern.MatchString(pin.SHA) || !versionPattern.MatchString(pin.Version) {
			return errors.New("actions_policy_action_invalid")
		}
	}
	if len(policy.RequiredRunCommands) == 0 {
		return errors.New("actions_policy_shape_invalid")
	}
	seenCommands := make(map[string]struct{}, len(policy.RequiredRunCommands))
	for _, command := range policy.RequiredRunCommands {
		command = strings.TrimSpace(command)
		if command == "" || strings.ContainsAny(command, "\r\n") {
			return errors.New("actions_policy_command_invalid")
		}
		if _, duplicate := seenCommands[command]; duplicate {
			return errors.New("actions_policy_command_invalid")
		}
		seenCommands[command] = struct{}{}
	}
	return nil
}

func check(ctx context.Context, repo string, policy actionsPolicy) (gateResult, error) {
	if err := validatePolicy(policy); err != nil {
		return gateResult{}, err
	}
	workflows, err := trackedWorkflows(ctx, repo)
	if err != nil {
		return gateResult{}, err
	}
	expectedWorkflows := policy.AllowedWorkflows
	if len(expectedWorkflows) == 0 {
		expectedWorkflows = []string{policy.BootstrapWorkflow}
	}
	if !reflect.DeepEqual(workflows, expectedWorkflows) {
		return gateResult{}, errors.New("actions_bootstrap_workflow_invalid")
	}
	state := workflowState{policy: policy, observed: map[string]int{}}
	for _, relative := range workflows {
		state.currentWorkflow = relative
		path := filepath.Join(repo, filepath.FromSlash(relative))
		data, err := readRegularSingleLink(path)
		if err != nil {
			return gateResult{}, errors.New("actions_workflow_invalid")
		}
		root, err := parseWorkflow(data)
		if err != nil {
			return gateResult{}, err
		}
		if err := validateYAMLShape(root); err != nil {
			return gateResult{}, err
		}
		if err := validateWorkflow(relative, root, &state); err != nil {
			return gateResult{}, err
		}
	}
	if len(state.observed) != len(policy.Actions) {
		return gateResult{}, errors.New("actions_policy_coverage_incomplete")
	}
	for name := range policy.Actions {
		if state.observed[name] == 0 {
			return gateResult{}, errors.New("actions_policy_coverage_incomplete")
		}
	}
	if state.selfGateBlocks != len(expectedWorkflows) ||
		state.bootstrapJobs != len(expectedWorkflows) ||
		state.aggregateJobs != len(policy.RequiredAggregates) {
		return gateResult{}, errors.New("actions_required_command_missing")
	}
	actions := make([]string, 0, len(state.observed))
	actionUses := 0
	for name, count := range state.observed {
		actions = append(actions, name)
		actionUses += count
	}
	sort.Strings(actions)
	return gateResult{WorkflowCount: len(workflows), ActionUses: actionUses, Actions: actions}, nil
}

func trackedWorkflows(ctx context.Context, repo string) ([]string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "git", "-C", repo, "ls-files", "-z", "--", ".github/workflows/*.yml", ".github/workflows/*.yaml")
	output, err := command.Output()
	if err != nil {
		return nil, errors.New("actions_git_invalid")
	}
	var paths []string
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		relative := filepath.ToSlash(filepath.Clean(string(raw)))
		if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, "../") || !strings.HasPrefix(relative, ".github/workflows/") {
			return nil, errors.New("actions_git_invalid")
		}
		paths = append(paths, relative)
	}
	if len(paths) == 0 {
		return nil, errors.New("actions_workflow_missing")
	}
	sort.Strings(paths)
	return paths, nil
}

func parseWorkflow(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, errors.New("actions_workflow_invalid")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("actions_workflow_invalid")
	}
	return document.Content[0], nil
}

func validateYAMLShape(node *yaml.Node) error {
	if node == nil || node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("actions_yaml_alias_forbidden")
	}
	switch node.Kind {
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return errors.New("actions_workflow_invalid")
		}
		seen := map[string]struct{}{}
		for index := 0; index < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" {
				return errors.New("actions_workflow_invalid")
			}
			if _, duplicate := seen[key.Value]; duplicate {
				return errors.New("actions_yaml_duplicate_key")
			}
			seen[key.Value] = struct{}{}
			if err := validateYAMLShape(value); err != nil {
				return err
			}
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, child := range node.Content {
			if err := validateYAMLShape(child); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		return nil
	default:
		return errors.New("actions_workflow_invalid")
	}
	return nil
}

func validateWorkflow(relative string, root *yaml.Node, state *workflowState) error {
	if root.Kind != yaml.MappingNode {
		return errors.New("actions_workflow_invalid")
	}
	if mappingValue(root, "if") != nil {
		return errors.New("actions_conditional_forbidden")
	}
	if mappingValue(root, "defaults") != nil {
		return errors.New("actions_defaults_forbidden")
	}
	if mappingValue(root, "env") != nil {
		return errors.New("actions_environment_override_forbidden")
	}
	if !exactRootPermissions(root) {
		return errors.New("actions_root_permissions_invalid")
	}
	if _, publishing := state.policy.PackagePublishing[relative]; publishing {
		if err := validatePackageTrigger(root); err != nil {
			return err
		}
	}
	jobs := mappingValue(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode || len(jobs.Content) == 0 {
		return errors.New("actions_workflow_invalid")
	}
	if contract, publishing := state.policy.PackagePublishing[relative]; publishing &&
		mappingValue(jobs, contract.Job) == nil {
		return errors.New("actions_package_job_missing")
	}
	bootstrapJob := workflowBootstrapJob(state.policy, relative)
	if bootstrapJob == "" {
		return errors.New("actions_bootstrap_workflow_invalid")
	}
	aggregate, requiresAggregate := state.policy.RequiredAggregates[relative]
	aggregateFound := false
	for index := 0; index < len(jobs.Content); index += 2 {
		jobID := jobs.Content[index].Value
		state.currentJob = jobID
		bootstrap := jobID == bootstrapJob
		job := jobs.Content[index+1]
		if requiresAggregate && jobID == aggregate.JobID {
			if aggregateFound {
				return errors.New("actions_aggregate_invalid")
			}
			if err := validateAggregateJob(job, state, aggregate); err != nil {
				return err
			}
			aggregateFound = true
			state.aggregateJobs++
			continue
		}
		if !bootstrap && !jobNeeds(job, bootstrapJob) {
			return errors.New("actions_job_dependency_invalid")
		}
		if err := validateJob(job, state, bootstrap); err != nil {
			return err
		}
	}
	if requiresAggregate != aggregateFound {
		return errors.New("actions_aggregate_missing")
	}
	return nil
}

func workflowBootstrapJob(policy actionsPolicy, relative string) string {
	if len(policy.WorkflowBootstrapJobs) != 0 {
		return policy.WorkflowBootstrapJobs[relative]
	}
	if relative == policy.BootstrapWorkflow {
		return policy.BootstrapJob
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if !containsString(unique, value) {
			unique = append(unique, value)
		}
	}
	return unique
}

func validateJob(job *yaml.Node, state *workflowState, bootstrap bool) error {
	if job.Kind != yaml.MappingNode {
		return errors.New("actions_workflow_invalid")
	}
	if mappingValue(job, "if") != nil {
		return errors.New("actions_conditional_forbidden")
	}
	if mappingValue(job, "defaults") != nil {
		return errors.New("actions_defaults_forbidden")
	}
	if mappingValue(job, "env") != nil {
		return errors.New("actions_environment_override_forbidden")
	}
	if mappingValue(job, "container") != nil {
		return errors.New("actions_container_forbidden")
	}
	if mappingValue(job, "services") != nil {
		return errors.New("actions_services_forbidden")
	}
	if packagePublisher(state) {
		if !exactPackagePermissions(mappingValue(job, "permissions")) {
			return errors.New("actions_package_permissions_invalid")
		}
	} else if mappingValue(job, "permissions") != nil {
		return errors.New("actions_job_permissions_forbidden")
	}
	runsOn := mappingValue(job, "runs-on")
	if runsOn == nil || runsOn.Kind != yaml.ScalarNode || runsOn.Tag != "!!str" || strings.TrimSpace(runsOn.Value) == "" {
		return errors.New("actions_runner_invalid")
	}
	if bootstrap && runsOn.Value != state.policy.BootstrapRunner {
		return errors.New("actions_bootstrap_runner_invalid")
	}
	if err := validateContinueOnError(job); err != nil {
		return err
	}
	if mappingValue(job, "uses") != nil {
		return errors.New("actions_reusable_workflow_forbidden")
	}
	steps := mappingValue(job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode || len(steps.Content) == 0 {
		return errors.New("actions_workflow_invalid")
	}
	if bootstrap {
		if mappingValue(job, "needs") != nil || len(steps.Content) < 2 {
			return errors.New("actions_bootstrap_order_invalid")
		}
		state.bootstrapJobs++
	}
	for index, step := range steps.Content {
		requireCheckout := bootstrap && index == 0
		requireGate := bootstrap && index == 1
		if err := validateStep(step, state, bootstrap, requireCheckout, requireGate); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(step *yaml.Node, state *workflowState, bootstrapJob, requireCheckout, requireGate bool) error {
	if step.Kind != yaml.MappingNode {
		return errors.New("actions_workflow_invalid")
	}
	if mappingValue(step, "if") != nil &&
		(!state.policy.Requirements.UnconditionalTestEvidenceAllowed || requireCheckout || requireGate || !unconditionalTestEvidenceStep(step)) {
		return errors.New("actions_conditional_forbidden")
	}
	if mappingValue(step, "env") != nil && !packageTokenStep(step, state) && !versionTokenStep(step, state) {
		return errors.New("actions_environment_override_forbidden")
	}
	if err := validateContinueOnError(step); err != nil {
		return err
	}
	uses, run := mappingValue(step, "uses"), mappingValue(step, "run")
	if (uses == nil) == (run == nil) {
		return errors.New("actions_workflow_invalid")
	}
	if uses != nil {
		if requireGate {
			return errors.New("actions_bootstrap_order_invalid")
		}
		if err := validateUse(step, uses, state); err != nil {
			return err
		}
		checkout := strings.HasPrefix(strings.TrimSpace(uses.Value), "actions/checkout@")
		if bootstrapJob && checkout != requireCheckout {
			return errors.New("actions_bootstrap_order_invalid")
		}
		return nil
	}
	if requireCheckout {
		return errors.New("actions_bootstrap_order_invalid")
	}
	return validateRun(step, run, state, requireGate)
}

// Evidence collection may run after failure, but no gate may become skippable.
// This deliberately accepts only literal always() for the existing source
// audit or a pinned upload of this run's bounded test-evidence directory.
func unconditionalTestEvidenceStep(step *yaml.Node) bool {
	condition := mappingValue(step, "if")
	if condition == nil || condition.Kind != yaml.ScalarNode || condition.Tag != "!!str" || condition.Value != "always()" {
		return false
	}
	if run := mappingValue(step, "run"); run != nil {
		return strings.TrimSpace(run.Value) == "bash scripts/audit/audit-source-clean.sh"
	}
	uses := mappingValue(step, "uses")
	if uses == nil || !strings.HasPrefix(uses.Value, "actions/upload-artifact@") {
		return false
	}
	path := mappingValue(mappingValue(step, "with"), "path")
	return path != nil && path.Value == "${{ runner.temp }}/runtime-test-evidence/"
}

func jobNeeds(job *yaml.Node, required string) bool {
	if job == nil || job.Kind != yaml.MappingNode {
		return false
	}
	needs := mappingValue(job, "needs")
	if needs == nil {
		return false
	}
	if needs.Kind == yaml.ScalarNode && needs.Tag == "!!str" {
		return needs.Value == required
	}
	if needs.Kind != yaml.SequenceNode {
		return false
	}
	for _, value := range needs.Content {
		if value.Kind == yaml.ScalarNode && value.Tag == "!!str" && value.Value == required {
			return true
		}
	}
	return false
}

func exactRootPermissions(root *yaml.Node) bool {
	permissions := mappingValue(root, "permissions")
	if permissions == nil || permissions.Kind != yaml.MappingNode || len(permissions.Content) != 2 {
		return false
	}
	key, value := permissions.Content[0], permissions.Content[1]
	return key.Kind == yaml.ScalarNode && key.Tag == "!!str" && key.Value == "contents" &&
		value.Kind == yaml.ScalarNode && value.Tag == "!!str" && value.Value == "read"
}

func validateContinueOnError(mapping *yaml.Node) error {
	value := mappingValue(mapping, "continue-on-error")
	if value == nil {
		return nil
	}
	if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" || value.Value != "false" {
		return errors.New("actions_continue_on_error_forbidden")
	}
	return nil
}

func validateRun(step, value *yaml.Node, state *workflowState, requireGate bool) error {
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return errors.New("actions_workflow_invalid")
	}
	if failureBypass.MatchString(value.Value) {
		return errors.New("actions_failure_bypass_forbidden")
	}
	lines := make([]string, 0, len(state.policy.RequiredRunCommands))
	for _, line := range strings.Split(value.Value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	exactGate := reflect.DeepEqual(lines, state.policy.RequiredRunCommands)
	if !exactGate {
		if requireGate {
			return errors.New("actions_bootstrap_order_invalid")
		}
		return nil
	}
	if !requireGate {
		return errors.New("actions_required_command_invalid")
	}
	name := mappingValue(step, "name")
	if name == nil || name.Kind != yaml.ScalarNode || name.Tag != "!!str" || name.Value != state.policy.GateStepName ||
		mappingValue(step, "shell") != nil || mappingValue(step, "env") != nil || mappingValue(step, "working-directory") != nil {
		return errors.New("actions_required_command_invalid")
	}
	state.selfGateBlocks++
	return nil
}

func validateUse(step *yaml.Node, value *yaml.Node, state *workflowState) error {
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return errors.New("actions_reference_invalid")
	}
	reference := strings.TrimSpace(value.Value)
	if strings.HasPrefix(reference, "./") {
		return errors.New("actions_local_action_forbidden")
	}
	if strings.HasPrefix(reference, "docker://") {
		return errors.New("actions_docker_action_forbidden")
	}
	match := remoteUsePattern.FindStringSubmatch(reference)
	if match == nil {
		return errors.New("actions_reference_invalid")
	}
	name, sha := match[1], match[2]
	pin, found := state.policy.Actions[name]
	comment := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value.LineComment), "#"))
	if !found || pin.SHA != sha || pin.Version != comment {
		return errors.New("actions_reference_not_pinned")
	}
	state.observed[name]++
	if name != "actions/checkout" {
		return nil
	}
	contract, ok := state.policy.WorkflowCheckouts[state.currentWorkflow]
	if !ok || !exactCheckoutInputs(step, contract) {
		return errors.New("actions_checkout_contract_invalid")
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Kind == yaml.ScalarNode && mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func hasMultipleLinks(info os.FileInfo) bool {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return false
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	field := value.FieldByName("Nlink")
	return field.IsValid() && field.CanUint() && field.Uint() != 1
}

func readRegularSingleLink(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || hasMultipleLinks(info) {
		return nil, errors.New("actions_file_invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("actions_file_invalid")
	}
	return data, nil
}

func main() {
	var repo, policyPath string
	flagSet := newFlagSet()
	flagSet.StringVar(&repo, "repo", "", "repository root")
	flagSet.StringVar(&policyPath, "policy", "", "action pin policy")
	if err := flagSet.Parse(os.Args[1:]); err != nil || strings.TrimSpace(repo) == "" || strings.TrimSpace(policyPath) == "" {
		writeFailure("actions_arguments_invalid")
		return
	}
	policy, err := loadPolicy(policyPath)
	if err != nil {
		writeFailure(err.Error())
		return
	}
	result, err := check(context.Background(), repo, policy)
	if err != nil {
		writeFailure(err.Error())
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"ok": true, "workflow_count": result.WorkflowCount, "action_uses": result.ActionUses, "actions": result.Actions,
	})
}

func writeFailure(code string) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "code": code})
	os.Exit(2)
}

func newFlagSet() *flag.FlagSet {
	flags := flag.NewFlagSet("github-actions-gate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}
