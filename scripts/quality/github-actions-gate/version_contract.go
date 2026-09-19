package main

import "gopkg.in/yaml.v3"

// Only the trusted proposal tool receives the repository-scoped short-lived token.
func versionTokenStep(step *yaml.Node, state *workflowState) bool {
	if state.currentWorkflow != ".github/workflows/version-pr.yml" || state.currentJob != "propose-version" {
		return false
	}
	env, run := mappingValue(step, "env"), mappingValue(step, "run")
	if env == nil || env.Kind != yaml.MappingNode || len(env.Content) != 4 ||
		run == nil || run.Value != "python3 -B scripts/packaging/sync_version_proposal.py" {
		return false
	}
	for key, want := range map[string]string{
		"GH_TOKEN":    "$" + "{{ steps.app-token.outputs.token }}",
		"VERSION_PRS": "$" + "{{ steps.version.outputs.prs }}",
	} {
		value := mappingValue(env, key)
		if value == nil || value.Kind != yaml.ScalarNode || value.Value != want {
			return false
		}
	}
	return true
}
