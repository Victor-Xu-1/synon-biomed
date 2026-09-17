package main

import (
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type checkoutContract struct {
	Ref        string `json:"ref"`
	FetchDepth int    `json:"fetch_depth"`
}

type aggregateContract struct {
	JobID       string   `json:"job_id"`
	DisplayName string   `json:"display_name"`
	Needs       []string `json:"needs"`
}

func validateAggregateJob(job *yaml.Node, state *workflowState, contract aggregateContract) error {
	if job == nil || job.Kind != yaml.MappingNode ||
		!mappingHasExactKeys(job, "name", "needs", "if", "runs-on", "timeout-minutes", "steps") {
		return errors.New("actions_aggregate_invalid")
	}
	name := mappingValue(job, "name")
	condition := mappingValue(job, "if")
	runsOn := mappingValue(job, "runs-on")
	timeout := mappingValue(job, "timeout-minutes")
	if !exactString(name, contract.DisplayName) ||
		!exactString(condition, "always()") ||
		!exactString(runsOn, state.policy.BootstrapRunner) ||
		timeout == nil || timeout.Kind != yaml.ScalarNode || timeout.Tag != "!!int" || timeout.Value != "5" ||
		!exactStringSequence(mappingValue(job, "needs"), contract.Needs) {
		return errors.New("actions_aggregate_invalid")
	}
	steps := mappingValue(job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode || len(steps.Content) != 1 {
		return errors.New("actions_aggregate_invalid")
	}
	step := steps.Content[0]
	if step.Kind != yaml.MappingNode || !mappingHasExactKeys(step, "name", "run") ||
		!exactString(mappingValue(step, "name"), "Verify all required job results") {
		return errors.New("actions_aggregate_invalid")
	}
	run := mappingValue(step, "run")
	if run == nil || run.Kind != yaml.ScalarNode || run.Tag != "!!str" ||
		strings.TrimSpace(run.Value) != aggregateRunBlock(contract.Needs) {
		return errors.New("actions_aggregate_invalid")
	}
	return nil
}

func aggregateRunBlock(needs []string) string {
	lines := make([]string, 0, len(needs))
	for _, job := range needs {
		lines = append(lines, "test '$"+"{{ needs."+job+".result }}' = 'success'")
	}
	return strings.Join(lines, "\n")
}

func exactString(node *yaml.Node, expected string) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!str" && node.Value == expected
}

func exactStringSequence(node *yaml.Node, expected []string) bool {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) != len(expected) {
		return false
	}
	for index, value := range node.Content {
		if !exactString(value, expected[index]) {
			return false
		}
	}
	return true
}

func mappingHasExactKeys(node *yaml.Node, expected ...string) bool {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content) != len(expected)*2 {
		return false
	}
	want := append([]string(nil), expected...)
	sort.Strings(want)
	actual := make([]string, 0, len(expected))
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return false
		}
		actual = append(actual, key.Value)
	}
	sort.Strings(actual)
	return reflect.DeepEqual(actual, want)
}

func exactCheckoutInputs(step *yaml.Node, contract checkoutContract) bool {
	withNode := mappingValue(step, "with")
	if withNode == nil || withNode.Kind != yaml.MappingNode || len(withNode.Content) != 6 {
		return false
	}
	persist := mappingValue(withNode, "persist-credentials")
	depth := mappingValue(withNode, "fetch-depth")
	ref := mappingValue(withNode, "ref")
	return persist != nil && persist.Kind == yaml.ScalarNode && persist.Tag == "!!bool" && persist.Value == "false" &&
		depth != nil && depth.Kind == yaml.ScalarNode && depth.Tag == "!!int" &&
		depth.Value == strconv.Itoa(contract.FetchDepth) &&
		ref != nil && ref.Kind == yaml.ScalarNode && ref.Tag == "!!str" && ref.Value == contract.Ref
}
