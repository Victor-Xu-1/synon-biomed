package server

import (
	"strings"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

// Retained only so an older durable correction can be decoded and resumed.
// New completion decisions are derived from typed Tool, Skill, plan, artifact,
// and operation state and never emit this task-text-classifier reason.
const sessionRunnerRealScientificEvidenceRequiredReasonCode = "real_scientific_evidence_required"

// sessionRunnerContinuationEvidenceEntries returns the contiguous logical-task
// chain when the latest input explicitly continues or repairs current work.
// Repeated continuation turns walk back to their first non-continuation task so
// durable source/compute receipts and review authority are not lost. The walk
// stops at the preceding independent task, preserving input-revision isolation.
func sessionRunnerContinuationEvidenceEntries(
	taskIntent string,
	entries []eventjournal.Entry,
) []eventjournal.Entry {
	if !sessionRunnerTaskContinuesPriorWork(taskIntent) {
		return nil
	}
	intentIndexes := make([]int, 0, 2)
	for index, entry := range entries {
		if runnerEntryStartsNewLogicalTask(entry) {
			intentIndexes = append(intentIndexes, index)
		}
	}
	if len(intentIndexes) < 2 {
		return nil
	}
	startIntent := len(intentIndexes) - 2
	for startIntent > 0 {
		priorIntent := strings.TrimSpace(runnerMessageText(entries[intentIndexes[startIntent]].Message))
		if !sessionRunnerTaskContinuesPriorWork(priorIntent) {
			break
		}
		startIntent--
	}
	start, end := intentIndexes[startIntent], intentIndexes[len(intentIndexes)-1]
	if start >= end {
		return nil
	}
	return append([]eventjournal.Entry(nil), entries[start:end]...)
}

func sessionRunnerContinuationRootTaskIntent(taskIntent string, entries []eventjournal.Entry) string {
	for _, entry := range sessionRunnerContinuationEvidenceEntries(taskIntent, entries) {
		if !runnerEntryStartsNewLogicalTask(entry) {
			continue
		}
		if root := strings.TrimSpace(runnerMessageText(entry.Message)); root != "" {
			return root
		}
	}
	return ""
}

func artifactReferencesFromRunnerEntries(entries []eventjournal.Entry) []transcriptstore.ArtifactReferenceInput {
	result := make([]transcriptstore.ArtifactReferenceInput, 0)
	seen := make(map[string]struct{})
	for _, entry := range entries {
		toolResult, _ := entry.Message["toolResult"].(map[string]any)
		if toolResult == nil {
			toolResult, _ = entry.Message["tool_result"].(map[string]any)
		}
		artifacts, _ := toolResult["artifacts"].([]any)
		for _, value := range artifacts {
			artifact, _ := value.(map[string]any)
			artifactID := strings.TrimSpace(firstNonEmpty(
				stringValue(artifact["artifact_id"]), stringValue(artifact["artifactId"]),
			))
			versionID := strings.TrimSpace(firstNonEmpty(
				stringValue(artifact["version_id"]), stringValue(artifact["versionId"]),
			))
			if artifactID == "" || versionID == "" {
				continue
			}
			key := artifactID + "\x00" + versionID
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, transcriptstore.ArtifactReferenceInput{
				ArtifactID: artifactID,
				VersionID:  versionID,
				Relation:   transcriptstore.ArtifactRelationProduced,
			})
		}
	}
	return result
}

// sessionRunnerTaskContinuesPriorWork resolves conversational continuity only;
// it never selects scientific capabilities, tools, evidence requirements, or a
// completion policy.
func sessionRunnerTaskContinuesPriorWork(taskIntent string) bool {
	normalized := strings.ToLower(strings.TrimSpace(taskIntent))
	if normalized == "" {
		return false
	}
	if transcriptstore.IsExplicitTaskContinuationDirective(normalized) ||
		containsAny(normalized, []string{
			"继续", "接着", "接续", "恢复当前", "恢复这个", "恢复这项", "修复当前", "修复这个", "修复这项",
			"上次", "上一条", "当前报告", "当前交付", "原题", "同一任务", "同一原题", "不要重试",
			"continue", "resume", "keep going", "pick up where", "current deliverable", "current report",
			"same task", "same report", "previous response", "prior report", "do not retry",
		}) {
		return true
	}
	// Words such as "existing", "prior", "已有" or "现有" often describe
	// scientific inputs in a brand-new task. They are continuity evidence only
	// when paired with an explicit update/repair action and a reference to the
	// preceding conversational result.
	chineseReference := containsAny(normalized, []string{
		"已有报告", "现有报告", "已有结果", "现有结果", "此前结果", "之前结果",
		"刚才结果", "刚才生成", "刚才已经生成", "上述结果", "本次结果",
	})
	chineseAction := containsAny(normalized, []string{
		"补充", "更新", "完善", "修订", "纠正", "延续", "完成", "补齐",
		"重新发布", "发布", "重新保存", "保存", "展示", "预览",
	})
	englishReference := containsAny(normalized, []string{
		"existing report", "existing result", "previous result", "previous answer", "prior result", "earlier result",
		"just generated", "results above", "result above", "this result", "these results",
	})
	englishAction := containsAny(normalized, []string{
		"update", "extend", "refine", "revise", "correct", "repair", "finish", "complete",
		"republish", "publish", "save", "display", "preview",
	})
	return (chineseReference && chineseAction) || (englishReference && englishAction)
}

func sessionRunnerHasAuthoritativeSourceEvidence(signals []string) bool {
	for _, signal := range normalizeTrustedScientificReviewSignals(signals) {
		switch {
		case strings.HasPrefix(signal, "source-connector:"),
			strings.HasPrefix(signal, "source-authority:"),
			strings.HasPrefix(signal, trustedScientificEvidenceRecordSignalPrefix),
			signal == "scientific-tool:binding_mode_analysis":
			return true
		}
	}
	return false
}
