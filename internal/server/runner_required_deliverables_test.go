package server

import (
	"reflect"
	"testing"
)

func TestSessionRunnerDoesNotInventFileTypesFromGenericTaskLanguage(t *testing.T) {
	tasks := []string{
		"Use real public data to compute the comparison and deliver reproducible data, a validation report, a one-page decision summary, and a 12-slide presentation.",
		"为难溶性弱碱性口服小分子制定开发策略，给出关键实验、风险优先级、决策标准和完整报告。",
		"给 SYN-ANA-118 API 做一个稳定性指示 HPLC 方法并验证结果。",
	}
	for _, task := range tasks {
		if missing := missingSessionRunnerRequiredDeliverables(task, nil); len(missing) != 0 {
			t.Fatalf("task %q invented filename requirements: %#v", task, missing)
		}
	}
}

func TestSessionRunnerRequiresOneArtifactForExplicitDownloadableOutput(t *testing.T) {
	tasks := []string{
		"Compare the compounds and make every result downloadable and reproducible.",
		"比较四种化合物，所有结果都要可以下载并能够复核。",
	}
	for _, task := range tasks {
		if missing := missingSessionRunnerRequiredDeliverables(task, nil); !reflect.DeepEqual(
			missing, []string{"at least one verified downloadable artifact"},
		) {
			t.Fatalf("task %q missing=%#v", task, missing)
		}
		if missing := missingSessionRunnerRequiredDeliverables(task, []string{"report.md"}); len(missing) != 0 {
			t.Fatalf("task %q rejected a real artifact: %#v", task, missing)
		}
	}
}

func TestSessionRunnerDoesNotTreatInputDownloadAsOutputArtifactRequirement(t *testing.T) {
	task := "Download the public dataset, analyze it, and summarize the findings in the final answer."
	if sessionRunnerRequiresDurableArtifact(task) {
		t.Fatalf("input download was treated as an output artifact requirement")
	}
}

func TestSessionRunnerRequiresArtifactForExplicitSaveWithoutAsSyntax(t *testing.T) {
	tasks := []string{
		"Download the public structure, validate it, and save a concise report together with the raw file or log.",
		"下载完成后校验文件存在、实际字节数、结构文件可解析，并保存一个简短结果报告和原始文件或日志。",
		"请把这个公开结构数据整理到当前项目，确认数据可用，并保留原始数据和一份简短的核验说明。",
		"Preserve the original file and retain the validation report.",
		"保留原始文件和核验报告。",
	}
	for _, task := range tasks {
		if !sessionRunnerRequiresDurableArtifact(task) {
			t.Fatalf("task %q did not retain its explicit save requirement", task)
		}
		if missing := missingSessionRunnerRequiredDeliverables(task, nil); !reflect.DeepEqual(
			missing, []string{"at least one verified downloadable artifact"},
		) {
			t.Fatalf("task %q missing=%#v", task, missing)
		}
	}
}

func TestSessionRunnerPreservationInstructionsDoNotInventArtifacts(t *testing.T) {
	for _, task := range []string{
		"Preserve the existing column order.",
		"Retain the original precision in your answer.",
		"保留两位小数，回答计算结果。",
		"留存这个判断，继续解释原因。",
	} {
		if missing := missingSessionRunnerRequiredDeliverables(task, nil); len(missing) != 0 {
			t.Errorf("task %q invented artifact requirements: %v", task, missing)
		}
	}
}

func TestSessionRunnerRequiresOnlyExplicitlyNamedOutputArtifacts(t *testing.T) {
	task := "Run the reproducible workflow; output provenance.csv、templates.csv、compounds.csv、validation.json and report.md. Use input_seed.csv as an input."
	if got := sessionRunnerExplicitDeliverableNames(task); !reflect.DeepEqual(got, []string{
		"compounds.csv", "provenance.csv", "report.md", "templates.csv", "validation.json",
	}) {
		t.Fatalf("explicit deliverables=%#v", got)
	}
	missing := missingSessionRunnerRequiredDeliverables(task, []string{
		"provenance.csv", "compounds.csv", "validation.json", "report.md",
	})
	if !reflect.DeepEqual(missing, []string{"artifact templates.csv"}) {
		t.Fatalf("missing explicit deliverables=%#v", missing)
	}
}

func TestSessionRunnerRecognizesCreatedSMILESAsExactDeliverable(t *testing.T) {
	for _, task := range []string{
		"创建 molecules.smi，然后保存并验证。",
		"Create molecules.smi, then save and validate it.",
	} {
		if got := sessionRunnerExplicitDeliverableNames(task); !reflect.DeepEqual(got, []string{"molecules.smi"}) {
			t.Fatalf("task %q deliverables=%#v", task, got)
		}
		missing := missingSessionRunnerRequiredDeliverables(task, nil)
		if len(missing) == 0 || missing[0] != "artifact molecules.smi" {
			t.Fatalf("task %q missing=%#v", task, missing)
		}
		if missing := missingSessionRunnerRequiredDeliverables(task, []string{"molecules.smi"}); len(missing) != 0 {
			t.Fatalf("task %q rejected molecules.smi: %#v", task, missing)
		}
	}
}

func TestSessionRunnerExplicitDeliverableNamesIgnoreInputsAndMentions(t *testing.T) {
	task := "Read input_seed.csv and compare it with reference.json. Save the final table as results.csv."
	if got := sessionRunnerExplicitDeliverableNames(task); !reflect.DeepEqual(got, []string{"results.csv"}) {
		t.Fatalf("explicit deliverables=%#v", got)
	}
}

func TestSessionRunnerRequiresEveryExplicitUnnamedDeliverableFormat(t *testing.T) {
	tasks := []string{
		"Design six pocket-conditioned molecules and deliver an editable 3D SDF, a candidate summary CSV, and a concise professional report.",
		"设计6个口袋条件分子，交付可编辑三维 SDF、候选汇总 CSV 和简洁专业报告。",
	}
	for _, task := range tasks {
		if got := sessionRunnerExplicitDeliverableFormats(task); !reflect.DeepEqual(got, []string{"csv", "sdf"}) {
			t.Fatalf("task %q formats=%#v", task, got)
		}
		missing := missingSessionRunnerRequiredDeliverables(task, []string{"project_report.md"})
		if !reflect.DeepEqual(missing, []string{"a verified .csv artifact", "a verified .sdf artifact"}) {
			t.Fatalf("task %q missing=%#v", task, missing)
		}
		if missing = missingSessionRunnerRequiredDeliverables(task, []string{
			"designed_candidates.sdf", "candidate_summary.csv", "project_report.md",
		}); len(missing) != 0 {
			t.Fatalf("task %q rejected complete formats: %#v", task, missing)
		}
		if !sessionRunnerArtifactNameSatisfiesRequiredDeliverable(task, "designed_candidates.sdf") ||
			!sessionRunnerArtifactNameSatisfiesRequiredDeliverable(task, "candidate_summary.csv") {
			t.Fatalf("task %q did not recognize its requested formats", task)
		}
	}
}

func TestSessionRunnerRecognizesMarkdownAndCSVOutputFormats(t *testing.T) {
	task := "生成可直接使用的Markdown专业报告与CSV证据表。"
	if got := sessionRunnerExplicitDeliverableFormats(task); !reflect.DeepEqual(got, []string{"csv", "md"}) {
		t.Fatalf("explicit deliverable formats=%#v", got)
	}
	if missing := missingSessionRunnerRequiredDeliverables(task, []string{"evidence.csv"}); !reflect.DeepEqual(
		missing, []string{"a verified .md artifact"},
	) {
		t.Fatalf("missing Markdown deliverable=%#v", missing)
	}
	if missing := missingSessionRunnerRequiredDeliverables(task, []string{"report.md", "evidence.csv"}); len(missing) != 0 {
		t.Fatalf("complete Markdown and CSV delivery rejected=%#v", missing)
	}
}

func TestSessionRunnerExplicitDeliverableFormatsIgnoreInputFormats(t *testing.T) {
	task := "Read input_seed.csv and inspect the reference SDF. Deliver a concise professional report."
	if got := sessionRunnerExplicitDeliverableFormats(task); len(got) != 0 {
		t.Fatalf("input formats became output requirements: %#v", got)
	}
}
