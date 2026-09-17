package toolgateway

import (
	"reflect"
	"testing"
)

func TestPipelineEnforcesCanonicalOrderAndAudit(t *testing.T) {
	var observed []Stage
	steps := make([]Step, 0, len(OrderedStages))
	for _, stage := range OrderedStages {
		stage := stage
		steps = append(steps, Step{Stage: stage, Run: func(*Invocation) {
			observed = append(observed, stage)
		}})
	}
	pipeline, err := NewPipeline(steps...)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}
	pipeline.Run(NewInvocation(t.Context(), "call-1", "python", nil, nil))
	if !reflect.DeepEqual(observed, OrderedStages) {
		t.Fatalf("stage order = %v, want %v", observed, OrderedStages)
	}
}

func TestPipelineShortCircuitsOnlyToPostHooksAndAudit(t *testing.T) {
	var observed []Stage
	steps := make([]Step, 0, len(OrderedStages))
	for _, stage := range OrderedStages {
		stage := stage
		steps = append(steps, Step{Stage: stage, Run: func(invocation *Invocation) {
			observed = append(observed, stage)
			if stage == StageExecute {
				invocation.CompleteWithPostHooks(map[string]any{"ok": false}, "failed", "failed", nil)
			}
		}})
	}
	pipeline := MustPipeline(steps...)
	pipeline.Run(NewInvocation(t.Context(), "call-2", "bash", nil, nil))
	want := []Stage{
		StageNormalize, StageAdmit, StagePreflight, StageFailureBudget,
		StageReviewScope, StagePreHooks, StageRevalidate, StagePermission,
		StageSourceBudget, StageExecute, StagePostHooks, StageAudit,
	}
	if !reflect.DeepEqual(observed, want) {
		t.Fatalf("short-circuit stages = %v, want %v", observed, want)
	}
}

func TestPipelineRejectsReorderedOrMissingStages(t *testing.T) {
	steps := make([]Step, 0, len(OrderedStages))
	for _, stage := range OrderedStages {
		steps = append(steps, Step{Stage: stage, Run: func(*Invocation) {}})
	}
	steps[0], steps[1] = steps[1], steps[0]
	if _, err := NewPipeline(steps...); err == nil {
		t.Fatal("expected reordered pipeline to be rejected")
	}
	if _, err := NewPipeline(steps[:len(steps)-1]...); err == nil {
		t.Fatal("expected missing audit stage to be rejected")
	}
}
