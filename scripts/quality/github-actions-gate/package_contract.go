package main

import (
	"errors"

	"gopkg.in/yaml.v3"
)

// Publishing is an explicit, narrow exception; PR and test jobs remain read-only.
type packageContract struct {
	Job string `json:"job"`
}

func validatePackageTrigger(root *yaml.Node) error {
	on := mappingValue(root, "on")
	if on == nil || on.Kind != yaml.MappingNode || len(on.Content) != 2 || on.Content[0].Value != "release" {
		return errors.New("actions_package_trigger_invalid")
	}
	release := on.Content[1]
	types := mappingValue(release, "types")
	if release.Kind != yaml.MappingNode || len(release.Content) != 2 ||
		types == nil || types.Kind != yaml.SequenceNode || len(types.Content) != 1 || types.Content[0].Value != "published" {
		return errors.New("actions_package_trigger_invalid")
	}
	return nil
}

func packagePublisher(state *workflowState) bool {
	contract, ok := state.policy.PackagePublishing[state.currentWorkflow]
	return ok && contract.Job == state.currentJob
}

func exactPackagePermissions(value *yaml.Node) bool {
	if value == nil || value.Kind != yaml.MappingNode || len(value.Content) != 6 {
		return false
	}
	for key, want := range map[string]string{"contents": "read", "actions": "read", "packages": "write"} {
		got := mappingValue(value, key)
		if got == nil || got.Kind != yaml.ScalarNode || got.Tag != "!!str" || got.Value != want {
			return false
		}
	}
	return true
}

func packageTokenStep(step *yaml.Node, state *workflowState) bool {
	if !packagePublisher(state) {
		return false
	}
	env := mappingValue(step, "env")
	run := mappingValue(step, "run")
	token := mappingValue(env, "GH_TOKEN")
	return env != nil && env.Kind == yaml.MappingNode && len(env.Content) == 2 &&
		token != nil && token.Kind == yaml.ScalarNode && token.Value == "$"+"{{ github.token }}" &&
		run != nil && run.Value == "python3 -B scripts/packaging/release_container.py"
}
