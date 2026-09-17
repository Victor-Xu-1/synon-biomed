package server

import (
	"strings"
	"testing"
)

func TestAppendSessionRunnerPublicCommunicationContractUsesOneSystemBoundary(t *testing.T) {
	messages := appendSessionRunnerPublicCommunicationContract([]chatCompletionMessage{{
		Role: "user", Content: "分析该问题",
	}})
	if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("messages=%#v", messages)
	}
	content := messages[0].Content
	for _, required := range []string{
		"Assistant communication has two explicit roles",
		"in the user's language",
		"public_progress field",
		"minimum length indicates whether an update is due",
		"complete final answer",
		"accepted only after completion validation",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("public communication contract missing %q: %q", required, content)
		}
	}
	ordered := appendSessionRunnerPublicCommunicationContract([]chatCompletionMessage{
		{Role: "system", Content: "base"},
		{Role: "system", Content: "skill and execution context"},
		{Role: "user", Content: "分析该问题"},
	})
	if len(ordered) != 4 || ordered[2].Role != "system" ||
		!strings.Contains(ordered[2].Content, "public_progress field") || ordered[3].Role != "user" {
		t.Fatalf("public communication contract is not terminal in the system block: %#v", ordered)
	}
	if strings.Contains(content, "<|PublicProgressBegin|>") || strings.Contains(content, "after the first successful action") ||
		strings.Contains(content, "before each materially new stage") ||
		strings.Contains(content, "Between meaningful evidence or execution steps") {
		t.Fatalf("obsolete forced-progress contract survived: %q", content)
	}
}

func TestSessionRunnerPublicProgressNarrationSuppressesProductInternals(t *testing.T) {
	for _, content := range []string{
		"本轮遵循工具协议修正，并在有界执行单元中继续。",
		"The runtime correction requires another bounded execution unit.",
		"尝试调用 `download_public_scientific_file` 获取全文。",
		"尝试使用 web_fetch 核对页面。",
		"Calling mcp__literature__fetch_article_fulltext before analysis.",
	} {
		if got := sessionRunnerPublicProgressNarration(content); got != "" {
			t.Fatalf("internal progress leaked: input=%q output=%q", content, got)
		}
	}
	if got := sessionRunnerPublicProgressNarration("该来源未提供可下载全文，将改用另一项权威证据继续核对。"); got == "" {
		t.Fatal("scientific recovery explanation was incorrectly suppressed")
	}
	for _, content := range []string{
		"该方法适用于 in_vitro 条件，当前先核对 analysis_report 中的对照组。",
		"该方法适用于 gene_expression_matrix 数据，需结合 protein_binding_affinity 进行解释。",
		"结果显示尚未形成结论，仍需补充独立证据。",
	} {
		if got := sessionRunnerPublicProgressNarration(content); got != content {
			t.Fatalf("scientific progress was over-filtered: input=%q output=%q", content, got)
		}
	}
}

func TestSessionRunnerPublicProgressNarrationKeepsProgressAndWithholdsConclusions(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "concise scientific progress",
			content: "公开证据存在两种相反结果，需要补充原始研究并核对适用人群。",
			want:    "公开证据存在两种相反结果，需要补充原始研究并核对适用人群。",
		},
		{
			name:    "natural first person transition",
			content: "I found conflicting cohort results, so I’ll inspect the primary studies before comparing the estimates.",
			want:    "I found conflicting cohort results, so I’ll inspect the primary studies before comparing the estimates.",
		},
		{
			name:    "formal conclusion before save",
			content: "### 核心结论\n优先推荐方案 A。\n\n### 最终交付文件\n报告已生成。",
		},
		{
			name:    "artifact-bearing candidate",
			content: "结果见[报告]({{artifact:version-1}})。",
		},
		{
			name:    "report-sized tool round",
			content: strings.Repeat("证据仍需核对。", 1000),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sessionRunnerPublicProgressNarration(test.content); got != test.want {
				t.Fatalf("progress narration=%q want=%q", got, test.want)
			}
		})
	}
}

func TestSessionRunnerPublicProgressNarrationRemovesOnlyInternalFragments(t *testing.T) {
	input := "I found two relevant cohorts. The runtime correction will invoke web_fetch. I’ll compare the endpoints next."
	want := "I found two relevant cohorts. I’ll compare the endpoints next."
	if got := sessionRunnerPublicProgressNarration(input); got != want {
		t.Fatalf("progress narration=%q want=%q", got, want)
	}
}

func TestSessionRunnerPublicProgressNarrationKeepsDecimalAndFilenameBoundariesIntact(t *testing.T) {
	input := "The 2.7 Å structure supports the observed pose. I will inspect runner_execution.go before the next comparison. The cohort evidence remains usable."
	want := "The 2.7 Å structure supports the observed pose. The cohort evidence remains usable."
	if got := sessionRunnerPublicProgressNarration(input); got != want {
		t.Fatalf("progress narration=%q want=%q", got, want)
	}
}

func TestSessionRunnerPublicProgressNarrationRemovesProviderThinkMarkers(t *testing.T) {
	for _, input := range []string{
		"开始检索。</think_never_used_abc123>",
		"开始检索。<think>private reasoning</think>继续执行。",
		"开始检索。</think>",
	} {
		got := sessionRunnerPublicProgressNarration(input)
		if strings.Contains(strings.ToLower(got), "think") || strings.Contains(got, "private reasoning") {
			t.Fatalf("internal think marker leaked: input=%q output=%q", input, got)
		}
		if !strings.Contains(got, "开始检索") {
			t.Fatalf("public progress was removed with marker: input=%q output=%q", input, got)
		}
	}
}

func TestSessionRunnerPublicProgressNarrationRejectsControlOnlyFragments(t *testing.T) {
	for _, fragment := range []string{"></", "</", "<", "<|", " \n>\n "} {
		if got := sessionRunnerPublicProgressNarration(fragment); got != "" {
			t.Errorf("control-only progress escaped: %q", got)
		}
	}
	for _, text := range []string{"p < 0.05", "ΔG = −7.2 kcal/mol", "正在检查。", "∞", "✓"} {
		if got := sessionRunnerPublicProgressNarration(text); got != text {
			t.Errorf("scientific notation changed: %q", got)
		}
	}
}

func TestSessionRunnerPublicProgressDeduperSuppressesPrivateRepairReplayOnlyWithinOneSegment(t *testing.T) {
	var deduper sessionRunnerPublicProgressDeduper
	if publish, err := deduper.shouldPublish("progress-1", "正在核对相同的证据。"); err != nil || !publish {
		t.Fatal("first public progress segment was suppressed")
	}
	if publish, err := deduper.shouldPublish("progress-1", "正在核对相同的证据。"); err != nil || publish {
		t.Fatal("exact private-repair replay was published twice")
	}
	if publish, err := deduper.shouldPublish("progress-2", "改用另一项独立证据继续核对。"); err != nil || !publish {
		t.Fatal("materially different recovery progress was suppressed")
	}
	if _, err := deduper.shouldPublish("progress-2", "同一个块被错误改写。"); err == nil {
		t.Fatal("changed text reused an existing public progress block id")
	}
	deduper.reset()
	if publish, err := deduper.shouldPublish("progress-1", "正在核对相同的证据。"); err != nil || !publish {
		t.Fatal("a later assistant segment could not reuse valid narration")
	}
}
