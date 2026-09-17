package server

import (
	"strings"
	"testing"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestREPLRecoveryPreflightRejectsStaleReceiverAndAcceptsSelfContainedCell(t *testing.T) {
	gateway := serverAgentRuntimeToolGateway{taskRun: &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{
		Claim: transcriptstore.RunnerClaim{ResumeSource: transcriptstore.ResumeSourceCheckpoint},
	}}}
	stale := gateway.agentRuntimeREPLRecoveryStatePreflight("repl", map[string]any{
		"code": `pdb_ids = [rec["pdb_id"] for rec in pdb_search.get("records", [])]`,
	})
	if stale == nil || stale["status"] != "repl_recovery_state_preflight_required" ||
		!strings.Contains(stringValue(stale["message"]), "pdb_search") {
		t.Fatalf("stale receiver preflight=%#v", stale)
	}
	selfContained := gateway.agentRuntimeREPLRecoveryStatePreflight("repl", map[string]any{
		"code": `import host
pdb_search = host.mcp("structures-interactions", "pdb_search_structures", uniprot_accession="Q96SW2")
pdb_ids = [rec["pdb_id"] for rec in pdb_search.get("records", [])]`,
	})
	if selfContained != nil {
		t.Fatalf("self-contained recovery cell was blocked: %#v", selfContained)
	}
	injectedHost := gateway.agentRuntimeREPLRecoveryStatePreflight("repl", map[string]any{
		"code": `import json
result = host.mcp("pubmed", "get_article_metadata", pmids=["123"])
articles = result.get("articles", []) if isinstance(result, dict) else []
print(list(result.keys()))
json.dumps(articles)`,
	})
	if injectedHost != nil {
		t.Fatalf("injected host or Python conditional keyword was mistaken for stale state: %#v", injectedHost)
	}
	methodInspection := gateway.agentRuntimeREPLRecoveryStatePreflight("repl", map[string]any{
		"code": `import host
methods = host.mcp.list_methods("pubmed")
print([method.get("name") for method in methods])`,
	})
	if methodInspection != nil {
		t.Fatalf("nested host.mcp method inspection was mistaken for stale mcp state: %#v", methodInspection)
	}
	lambdaAndFunctionLocals := gateway.agentRuntimeREPLRecoveryStatePreflight("repl", map[string]any{
		"code": `result = host.mcp("structures-interactions", "pdb_search_structures", uniprot_accession="Q07820")
result_sorted = sorted(result, key=lambda x: x["resolution"])
def ligand_ids(record, fallback=None):
    return record.get("ligands", fallback)
print([ligand_ids(row) for row in result_sorted])`,
	})
	if lambdaAndFunctionLocals != nil {
		t.Fatalf("lambda or function-local parameters were mistaken for stale process state: %#v", lambdaAndFunctionLocals)
	}
	fresh := serverAgentRuntimeToolGateway{taskRun: &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{
		Claim: transcriptstore.RunnerClaim{ResumeSource: transcriptstore.ResumeSourceFresh},
	}}}
	if blocked := fresh.agentRuntimeREPLRecoveryStatePreflight("repl", map[string]any{"code": `print(pdb_search.get("records"))`}); blocked != nil {
		t.Fatalf("fresh persistent REPL cell was blocked: %#v", blocked)
	}
	retry := serverAgentRuntimeToolGateway{taskRun: &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{
		Claim: transcriptstore.RunnerClaim{ResumeSource: transcriptstore.ResumeSourceFresh, Attempt: 2},
	}}}
	if blocked := retry.agentRuntimeREPLRecoveryStatePreflight("repl", map[string]any{
		"code": `print("variables remain visible in. old output")
# prior_result.get("records") must not be read from this comment
print("done")`,
	}); blocked != nil {
		t.Fatalf("strings or comments produced a recovery false positive: %#v", blocked)
	}
	if blocked := retry.agentRuntimeREPLRecoveryStatePreflight("repl", map[string]any{
		"code": `print(prior_result.get("records"))`,
	}); blocked == nil || !strings.Contains(stringValue(blocked["message"]), "prior_result") {
		t.Fatalf("fresh-labelled retry did not enforce recovery: %#v", blocked)
	}
}

func TestREPLRecoveryPreflightRecognizesContextManagerBindings(t *testing.T) {
	gateway := serverAgentRuntimeToolGateway{taskRun: &sessionRunnerChatRun{Transcript: &transcriptRunnerAuthority{
		Claim: transcriptstore.RunnerClaim{ResumeSource: transcriptstore.ResumeSourceCheckpoint},
	}}}
	input := map[string]any{"code": `from pathlib import Path
source_path = Path("source.bin")
target_path = Path("target.bin")
with source_path.open("rb") as source_handle:
    payload = source_handle.read()
with target_path.open("wb") as target_handle:
    target_handle.write(payload)`}
	if blocked := gateway.agentRuntimeREPLRecoveryStatePreflight("repl", input); blocked != nil {
		t.Fatalf("self-contained context-manager bindings were rejected: %#v", blocked)
	}
}
