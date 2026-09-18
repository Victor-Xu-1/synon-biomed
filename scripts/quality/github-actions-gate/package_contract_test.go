package main

import (
	"os"
	"strings"
	"testing"
)

func TestPackagePublishingDoesNotGrantWritesToQualityJobs(t *testing.T) {
	policy, err := loadPolicy("../../../docs/governance/github-actions-pins.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../.github/workflows/packages.yml")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, tc := range []struct {
		name, source, want string
	}{
		{"valid", source, ""},
		{"missing-publisher", strings.Split(source, "  publish-package:")[0], "actions_package_job_missing"},
		{"pull-request", strings.Replace(source, "release:\n    types: [published]", "pull_request:", 1), "actions_package_trigger_invalid"},
		{"extra-event", strings.Replace(source, "types: [published]", "types: [published]\n  push:", 1), "actions_package_trigger_invalid"},
		{"draft-event", strings.Replace(source, "types: [published]", "types: [created]", 1), "actions_package_trigger_invalid"},
		{"root-writes", strings.Replace(source, "contents: read", "contents: write", 1), "actions_root_permissions_invalid"},
		{"contents-writes", strings.Replace(source, "      contents: read", "      contents: write", 1), "actions_package_permissions_invalid"},
		{"extra-permission", strings.Replace(source, "packages: write", "packages: write\n      issues: write", 1), "actions_package_permissions_invalid"},
		{"missing-permission", strings.Replace(source, "      packages: write\n", "", 1), "actions_package_permissions_invalid"},
		{"bootstrap-writes", strings.Replace(source, "    timeout-minutes: 10", "    permissions:\n      packages: write\n    timeout-minutes: 10", 1), "actions_job_permissions_forbidden"},
		{"unscoped-token", strings.Replace(source, "python3 -B scripts/packaging/release_package.py", "echo unsafe", 1), "actions_environment_override_forbidden"},
		{"long-lived-token", strings.Replace(source, "$"+"{{ github.token }}", "$"+"{{ secrets.PAT }}", 1), "actions_environment_override_forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := parseWorkflow([]byte(tc.source))
			if err != nil {
				t.Fatal(err)
			}
			state := workflowState{policy: policy, observed: map[string]int{}, currentWorkflow: ".github/workflows/packages.yml"}
			err = validateWorkflow(state.currentWorkflow, root, &state)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
		})
	}
}
