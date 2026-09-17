package server

import "testing"

func TestSkillSearchMarksAlreadyLoadedExactMatchWithoutHidingOtherCandidates(t *testing.T) {
	run := &sessionRunnerChatRun{}
	run.addExecutedSkillNames("structure-based-molecule-generation")
	input := map[string]any{"query": "structure-based-molecule-generation"}
	value := map[string]any{
		"matches": []string{"drug-discovery-pipeline", "structure-based-molecule-generation"},
		"skill_matches": []map[string]any{
			{"name": "drug-discovery-pipeline"},
			{"name": "structure-based-molecule-generation"},
		},
	}

	result := mapValue(agentRuntimeSkillSearchModelResult(value, input, run))
	if result["reused"] != true || result["status"] != "discovery_already_satisfied" {
		t.Fatalf("exact loaded discovery result=%#v", result)
	}
	loadedMatches := stringArrayValue(result["loaded_matches"])
	if len(loadedMatches) != 1 || loadedMatches[0] != "structure-based-molecule-generation" {
		t.Fatalf("loaded matches=%#v result=%#v", loadedMatches, result)
	}
	if _, present := result["skill_matches"]; present {
		t.Fatalf("exact loaded discovery replayed full Skill details: %#v", result)
	}
	if len(stringArrayValue(result["matches"])) != 2 {
		t.Fatalf("unrelated candidates were hidden: %#v", result)
	}
}
