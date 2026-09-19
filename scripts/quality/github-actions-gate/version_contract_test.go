package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionProvenanceTokenCannotEscapeTrustedCommand(t *testing.T) {
	policy, err := loadPolicy("../../../docs/governance/github-actions-pins.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "version-pr.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range [][2]string{
		{"python3 -B scripts/packaging/sync_version_proposal.py", "echo unsafe"},
		{"GH_TOKEN: $" + "{{ steps.app-token.outputs.token }}", "GH_TOKEN: $" + "{{ secrets.PAT }}"},
		{"VERSION_PRS: $" + "{{ steps.version.outputs.prs }}", "VERSION_PRS: untrusted"},
	} {
		mutated := strings.Replace(string(source), change[0], change[1], 1)
		root, err := parseWorkflow([]byte(mutated))
		if err != nil {
			t.Fatal(err)
		}
		state := workflowState{policy: policy, observed: map[string]int{}, currentWorkflow: ".github/workflows/version-pr.yml"}
		err = validateWorkflow(state.currentWorkflow, root, &state)
		if err == nil || err.Error() != "actions_environment_override_forbidden" {
			t.Fatalf("gate error=%v", err)
		}
	}
}

func TestVersionBotOnlyProposesReviewedSingleRepositoryUpdates(t *testing.T) {
	root := repositoryWorkflow(t, "version-pr.yml")
	on := mappingValue(root, "on")
	branches := mappingValue(mappingValue(on, "push"), "branches")
	if len(on.Content) != 4 || branches == nil || len(branches.Content) != 1 || branches.Content[0].Value != "main" {
		t.Fatal("version proposals may run only on main pushes or explicit dispatch")
	}
	jobs := mappingValue(root, "jobs")
	job := mappingValue(jobs, "propose-version")
	environment := mappingValue(job, "environment")
	if environment == nil || environment.Value != "release-automation" ||
		namedRun(root, "version-policy", "Require the protected main ref") != `test "$GITHUB_REF" = refs/heads/main` {
		t.Fatal("release credentials require the main-only automation environment")
	}
	if !jobNeeds(job, "version-policy") || mappingValue(job, "permissions") != nil {
		t.Fatal("version bot must depend on the read-only policy gate")
	}
	steps := mappingValue(job, "steps")
	if len(steps.Content) != 4 {
		t.Fatal("version proposals require trusted checkout, token, planning and provenance")
	}
	token, propose := steps.Content[1], steps.Content[2]
	if mappingValue(propose, "id").Value != "version" ||
		namedRun(root, "propose-version", "Synchronize version proposal provenance") != "python3 -B scripts/packaging/sync_version_proposal.py" {
		t.Fatal("version proposal provenance must be synchronized by trusted tooling")
	}
	if !strings.HasPrefix(mappingValue(token, "uses").Value, "actions/create-github-app-token@") ||
		!strings.HasPrefix(mappingValue(propose, "uses").Value, "googleapis/release-please-action@") {
		t.Fatal("unexpected release automation authority")
	}
	inputs := mappingValue(token, "with")
	want := map[string]string{
		"client-id":                "$" + "{{ vars.RELEASE_APP_ID }}",
		"private-key":              "$" + "{{ secrets.RELEASE_APP_PRIVATE_KEY }}",
		"owner":                    "$" + "{{ github.repository_owner }}",
		"repositories":             "synon-biomed",
		"permission-contents":      "write",
		"permission-issues":        "write",
		"permission-pull-requests": "write",
	}
	if len(inputs.Content) != len(want)*2 {
		t.Fatal("bot token must not add repository, organization or administration permissions")
	}
	for key, value := range want {
		actual := mappingValue(inputs, key)
		if actual == nil || actual.Value != value {
			t.Fatalf("unexpected bot scope %s", key)
		}
	}
	release := mappingValue(propose, "with")
	for key, value := range map[string]string{
		"token": "$" + "{{ steps.app-token.outputs.token }}", "target-branch": "main",
		"config-file": ".github/release-please-config.json", "manifest-file": ".github/release-please-manifest.json",
		"skip-github-release": "true",
	} {
		actual := mappingValue(release, key)
		if actual == nil || actual.Value != value {
			t.Fatalf("version proposals must not bypass reviewed release promotion: %s", key)
		}
	}
}
