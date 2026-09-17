package discoveryquery

import (
	"strings"
	"testing"
)

func TestTermsBridgeChineseScientificIntentToEnglishMetadata(t *testing.T) {
	terms := Terms("请搜索化合物并分析配体对接结果")
	for _, expected := range []string{"search", "compound", "ligand", "docking", "analysis"} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing %q", terms, expected)
		}
	}
}

func TestTermsComposePocketAndMoleculeDesignIntentWithoutAFullPromptRule(t *testing.T) {
	terms := Terms("设计一组与结合口袋相匹配且结构多样的三维小分子候选")
	for _, expected := range []string{"pocket", "structure-based", "generation", "design", "molecule"} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing %q", terms, expected)
		}
	}
}

func TestTermsBridgeChineseProteinAntibodyAndRNADesignDomains(t *testing.T) {
	for _, test := range []struct {
		query string
		terms []string
	}{
		{query: "为目标表位设计纳米抗体并优化CDR", terms: []string{"antibody", "nanobody", "cdr", "design"}},
		{query: "进行结合蛋白骨架生成和蛋白序列设计", terms: []string{"protein", "binder", "backbone", "design"}},
		{query: "根据三维RNA骨架完成RNA反向折叠", terms: []string{"rna", "inverse-folding", "design"}},
	} {
		got := Terms(test.query)
		for _, expected := range test.terms {
			if !contains(got, expected) {
				t.Fatalf("Terms(%q) = %v, missing %q", test.query, got, expected)
			}
		}
	}
}

func TestTermsKeepsGenericNucleicAcidDesignDistinctFromRNA(t *testing.T) {
	terms := Terms("规划一个核酸设计项目")
	for _, expected := range []string{"nucleic-acid", "design"} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing %q", terms, expected)
		}
	}
	if contains(terms, "rna") {
		t.Fatalf("generic nucleic-acid request was silently classified as RNA: %v", terms)
	}
}

func TestTermsMatchesMixedLanguageScientificPhrasesAcrossWhitespace(t *testing.T) {
	terms := Terms("根据三维 RNA 骨架完成 RNA 反向折叠")
	for _, expected := range []string{"rna", "design", "inverse-folding"} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing %q", terms, expected)
		}
	}
}

func TestTermsBridgeChinesePharmaceuticalDevelopmentDomains(t *testing.T) {
	terms := Terms("制剂处方与工艺放大、稳定性、临床药理和晶型研究")
	for _, expected := range []string{
		"formulation", "process", "scale-up", "stability", "clinical", "pharmacology", "polymorph",
	} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing %q", terms, expected)
		}
	}
}

func TestTermsBridgeChineseRetrievalConceptsToEnglishEvidenceMetadata(t *testing.T) {
	terms := Terms("口服肽类药物的吸收促进剂、脂质纳米载体、离子液体递送与临床试验")
	for _, expected := range []string{
		"oral", "peptide", "absorption", "enhancer", "lipid", "nanoparticle", "ionic", "liquid", "delivery", "clinical", "trial",
	} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing %q", terms, expected)
		}
	}
}

func TestCrossLanguageVariantsBridgeScientificDiscoveryInBothDirections(t *testing.T) {
	if got := CrossLanguageVariants("口服肽类药物的脂质纳米递送与临床试验"); len(got) != 1 ||
		!strings.Contains(got[0], "oral") || !strings.Contains(got[0], "peptide") ||
		!strings.Contains(got[0], "lipid") || !strings.Contains(got[0], "clinical") {
		t.Fatalf("Chinese to English variants = %#v", got)
	}
	if got := CrossLanguageVariants("oral peptide delivery clinical trial"); len(got) != 1 ||
		!strings.Contains(got[0], "口服") || !strings.Contains(got[0], "肽") ||
		!strings.Contains(got[0], "临床试验") {
		t.Fatalf("English to Chinese variants = %#v", got)
	}
}

func TestCrossLanguageVariantsKeepScientificContextWithoutSynonymFlood(t *testing.T) {
	got := CrossLanguageVariants("近三年口服小分子药物肝代谢与转运体相互作用评估方法")
	if len(got) != 1 {
		t.Fatalf("variants = %#v", got)
	}
	for _, expected := range []string{"oral", "molecule", "drug", "liver", "metabolism", "transporter", "interaction", "assessment", "methods"} {
		if !strings.Contains(got[0], expected) {
			t.Fatalf("variant %q missing contextual term %q", got[0], expected)
		}
	}
	for _, noisy := range []string{"chemistry", "pharmaceutical"} {
		if strings.Contains(got[0], noisy) {
			t.Fatalf("variant %q contains synonym-flood term %q", got[0], noisy)
		}
	}
}

func TestTermsPreserveEnglishAndGenerateChineseNGrams(t *testing.T) {
	terms := Terms("MCP 查询蛋白结构")
	for _, expected := range []string{"mcp", "查询", "蛋白", "结构", "query", "protein", "structure"} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing %q", terms, expected)
		}
	}
}

func TestTermsAreBounded(t *testing.T) {
	terms := Terms("搜索查询查找检索读取打开查看获取下载保存产物文件项目任务会话对话工具技能化合物小分子分子配体分子对接结合模式相互作用理化性质化学性质结构图渲染结构蛋白质序列专利文献论文数据分析")
	if len(terms) > maxTerms {
		t.Fatalf("Terms() returned %d terms, want at most %d", len(terms), maxTerms)
	}
}

func TestTermsDropEnglishStopWordsWithoutDroppingDomainTokens(t *testing.T) {
	terms := Terms("find a protein structure in R")
	for _, expected := range []string{"find", "protein", "structure", "r"} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing %q", terms, expected)
		}
	}
	for _, forbidden := range []string{"a", "in"} {
		if contains(terms, forbidden) {
			t.Fatalf("Terms() = %v, retained stop word %q", terms, forbidden)
		}
	}
}

func TestTermsDropNumericMeasurementsButKeepScientificIdentifiers(t *testing.T) {
	terms := Terms("25 0.10 -3.5 9XZG TP53 Q1E phase3")
	for _, forbidden := range []string{"25", "0.10", "-3.5"} {
		if contains(terms, forbidden) {
			t.Fatalf("Terms() = %v, retained numeric measurement %q", terms, forbidden)
		}
	}
	for _, expected := range []string{"9xzg", "tp53", "q1e", "phase3"} {
		if !contains(terms, expected) {
			t.Fatalf("Terms() = %v, missing identifier %q", terms, expected)
		}
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
