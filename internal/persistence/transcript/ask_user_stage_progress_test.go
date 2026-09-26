package transcript

import (
	"encoding/json"
	"testing"
)

func askUserStageProgressRecord() map[string]any {
	return map[string]any{
		"schema": "synon.plan_stage_progress.v1", "plan_artifact_id": "plan-1", "plan_version_id": "version-1",
		"completed_count": 1, "remaining_count": 1,
		"completed_steps": []any{map[string]any{"id": "step-1", "title": "Inspect evidence", "status": "completed"}},
		"remaining_steps": []any{map[string]any{"id": "step-2", "title": "Run analysis", "status": "blocked"}},
	}
}

func TestAskUserStageProgressRetainsStrictOptionalQuestionContract(t *testing.T) {
	question := func(stage map[string]any) []any {
		return []any{map[string]any{
			"question": "Which route?", "header": "Next route", "stage_progress": stage,
			"options": []any{map[string]any{"label": "Continue"}, map[string]any{"label": "Revise"}},
		}}
	}
	valid, err := normalizeAskUserQuestions(question(askUserStageProgressRecord()))
	if err != nil || len(valid) != 1 || valid[0].StageProgress == nil ||
		valid[0].StageProgress.CompletedSteps[0].ID != "step-1" || valid[0].StageProgress.RemainingSteps[0].Status != "blocked" {
		t.Fatalf("valid stage progress was not retained: %#v error=%v", valid, err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var restored []any
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	replayed, err := normalizeAskUserQuestions(restored)
	if err != nil || len(replayed) != 1 || replayed[0].StageProgress == nil ||
		replayed[0].StageProgress.PlanVersionID != "version-1" {
		t.Fatalf("stage progress did not round-trip through durable JSON: %#v error=%v", replayed, err)
	}

	invalid := map[string]func(map[string]any){
		"unknown field":    func(stage map[string]any) { stage["fabricated"] = true },
		"wrong schema":     func(stage map[string]any) { stage["schema"] = "future" },
		"missing version":  func(stage map[string]any) { stage["plan_version_id"] = "" },
		"fractional count": func(stage map[string]any) { stage["completed_count"] = 1.5 },
		"wrong count":      func(stage map[string]any) { stage["remaining_count"] = 2 },
		"oversized total":  func(stage map[string]any) { stage["remaining_count"] = 256 },
		"missing step id": func(stage map[string]any) {
			stage["remaining_steps"].([]any)[0].(map[string]any)["id"] = ""
		},
		"duplicate step": func(stage map[string]any) {
			stage["remaining_steps"].([]any)[0].(map[string]any)["id"] = "step-1"
		},
		"false completed": func(stage map[string]any) {
			stage["remaining_steps"].([]any)[0].(map[string]any)["status"] = "completed"
		},
		"unsupported status": func(stage map[string]any) {
			stage["remaining_steps"].([]any)[0].(map[string]any)["status"] = "succeeded"
		},
		"unknown step field": func(stage map[string]any) {
			stage["remaining_steps"].([]any)[0].(map[string]any)["evidence"] = "untrusted"
		},
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			stage := askUserStageProgressRecord()
			mutate(stage)
			if _, err := normalizeAskUserQuestions(question(stage)); err == nil {
				t.Fatalf("invalid stage progress was accepted: %#v", stage)
			}
		})
	}
}
