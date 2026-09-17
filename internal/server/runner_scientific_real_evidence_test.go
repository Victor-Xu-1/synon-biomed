package server

import (
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
)

func TestSessionRunnerContinuationCarriesContiguousLogicalTaskEvidence(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": "完成一个不相关的旧任务",
		}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolName": "web_search",
		}},
		{EventID: 3, ClientMessageID: "root-task-boundary", Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": "评估公开文献中的蛋白工程",
		}},
		{EventID: 4, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolName": "mcp__pubmed__search_articles",
			"toolResult": map[string]any{"artifacts": []any{map[string]any{
				"artifact_id": "artifact-report", "version_id": "version-report",
			}}},
		}},
		{EventID: 5, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": "继续当前报告，保留已有证据",
		}},
		{EventID: 6, Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "completed", "toolName": "python",
		}},
		{EventID: 7, Message: eventjournal.Message{
			"role": "user", "type": "message", "messageOrigin": "task_intent", "text": "继续当前报告，保留已有证据",
		}},
	}
	prior := sessionRunnerContinuationEvidenceEntries("继续当前报告，保留已有证据", entries)
	if len(prior) != 4 || prior[0].EventID != 3 || prior[3].EventID != 6 {
		t.Fatalf("prior continuation entries=%#v", prior)
	}
	if root := sessionRunnerContinuationRootTaskIntent("继续当前报告，保留已有证据", entries); root != "评估公开文献中的蛋白工程" {
		t.Fatalf("continuation root task intent=%q", root)
	}
	refs := artifactReferencesFromRunnerEntries(prior)
	if len(refs) != 1 || refs[0].ArtifactID != "artifact-report" || refs[0].VersionID != "version-report" {
		t.Fatalf("continuation artifact references=%#v", refs)
	}
}

func TestSessionRunnerTaskContinuationDoesNotSelectScientificRoute(t *testing.T) {
	for _, text := range []string{
		"继续核查公开研究并补充报告。",
		"更新已有报告中的状态分布。",
		"请把刚才已经生成并验证通过的结果重新发布为完整结果包。",
		"Resume the same task with the existing evidence.",
		"Refine the previous answer with a sensitivity analysis.",
		"Republish the results above as one complete previewable bundle.",
	} {
		if !sessionRunnerTaskContinuesPriorWork(text) {
			t.Fatalf("explicit continuation was not recognized: %q", text)
		}
	}
	for _, text := range []string{
		"分析现有临床证据，形成一份新的风险评估。",
		"Analyze existing public evidence for a different compound.",
	} {
		if sessionRunnerTaskContinuesPriorWork(text) {
			t.Fatalf("independent task was classified as a continuation: %q", text)
		}
	}
}

func TestSessionRunnerAuthoritativeSourceSignalPredicate(t *testing.T) {
	if sessionRunnerHasAuthoritativeSourceEvidence([]string{"execution-tool:python"}) {
		t.Fatal("runtime execution alone must not become source evidence")
	}
	if sessionRunnerHasAuthoritativeSourceEvidence([]string{"source-host:pubchem.ncbi.nlm.nih.gov"}) {
		t.Fatal("a host identity without a validated read receipt became source evidence")
	}
	for _, signal := range []string{
		"source-connector:bundled:chembl",
		trustedScientificValidatedPublicWebFetchSignal,
	} {
		if !sessionRunnerHasAuthoritativeSourceEvidence([]string{signal}) {
			t.Fatalf("trusted source signal was not recognized: %q", signal)
		}
	}
}
