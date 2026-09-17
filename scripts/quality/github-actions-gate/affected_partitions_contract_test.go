package main

import (
	"strings"
	"testing"
)

func TestAffectedWorkflowPartitionsRetainEveryRequiredResult(t *testing.T) {
	for _, prefix := range []string{"pr", "main"} {
		root := repositoryWorkflow(t, "quality-"+prefix+".yml")
		jobs := mappingValue(root, "jobs")
		quality := mappingValue(jobs, prefix+"-quality")
		output := mappingValue(mappingValue(quality, "outputs"), "go-matrix")
		if output == nil || output.Value != "$"+"{{ steps.go-scope.outputs.matrix }}" {
			t.Fatal("affected partitions must use the reviewed scope planner")
		}
		job := mappingValue(jobs, prefix+"-go-tests")
		strategy := mappingValue(job, "strategy")
		matrix := mappingValue(strategy, "matrix")
		limit := mappingValue(strategy, "max-parallel")
		failFast := mappingValue(strategy, "fail-fast")
		if matrix == nil || matrix.Value != "$"+"{{ fromJSON(needs."+prefix+"-quality.outputs.go-matrix) }}" ||
			limit == nil || limit.Value != "4" || failFast == nil || failFast.Value != "false" {
			t.Fatal("affected test partitions must be bounded and preserve every result")
		}
		planner := namedRun(root, prefix+"-quality", "Plan affected Go partitions")
		if !strings.Contains(planner, "pr_fast_scope.py --matrix") || !strings.Contains(planner, "GITHUB_OUTPUT") {
			t.Fatal("matrix output must come from actual affected-package selection")
		}
		steps := mappingValue(job, "steps")
		testFound, artifactFound := false, false
		for _, step := range steps.Content {
			if run := mappingValue(step, "run"); run != nil && strings.Contains(run.Value, "--log-dir") {
				testFound = strings.Contains(run.Value, "--shard-index '$"+"{{ matrix.index }}'") &&
					strings.Contains(run.Value, "--shard-count '$"+"{{ matrix.count }}'")
			}
			if uses := mappingValue(step, "uses"); uses != nil && strings.HasPrefix(uses.Value, "actions/upload-artifact@") {
				name := mappingValue(mappingValue(step, "with"), "name")
				artifactFound = name != nil && strings.Contains(name.Value, "$"+"{{ matrix.index }}")
			}
		}
		if !testFound || !artifactFound {
			t.Fatal("each partition must execute its exact test selection and retain distinct evidence")
		}
	}
}
