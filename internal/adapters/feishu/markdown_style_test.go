package feishu

import (
	"strings"
	"testing"
)

func TestOptimizeMarkdownForFeishuDowngradesHeadingsOutsideCodeBlocks(t *testing.T) {
	input := "# H1\n## H2\n\n```go\n# keep\n## keep too\n```\n\n#### H4"
	got := OptimizeMarkdownForFeishu(input, 1)
	if !containsAll(got, []string{"#### H1", "##### H2", "# keep", "## keep too", "##### H4"}) {
		t.Fatalf("optimized markdown = %q", got)
	}
	if strings.Contains(got, "\n# H1") || strings.Contains(got, "\n## H2") {
		t.Fatalf("headings were not downgraded = %q", got)
	}
}

func TestOptimizeMarkdownForFeishuAddsSchemaTwoSpacingAndStripsInvalidImages(t *testing.T) {
	input := "text before\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n![keep](img_abc)\n![drop](https://example.com/a.png)\n\n```txt\ncode\n```"
	got := OptimizeMarkdownForFeishu(input, 2)
	if !strings.Contains(got, "text before\n<br>\n| a | b |") {
		t.Fatalf("table prefix spacing missing = %q", got)
	}
	if !strings.Contains(got, "| 1 | 2 |\n<br>") {
		t.Fatalf("table suffix spacing missing = %q", got)
	}
	if !strings.Contains(got, "![keep](img_abc)") || strings.Contains(got, "drop") || strings.Contains(got, "example.com") {
		t.Fatalf("image key filtering failed = %q", got)
	}
	if !strings.Contains(got, "<br>\n```txt\ncode\n```\n<br>") {
		t.Fatalf("code block spacing missing = %q", got)
	}
}

func TestFindMarkdownTablesOutsideCodeBlocks(t *testing.T) {
	input := "```\n| ignored | table |\n|---|---|\n| 1 | 2 |\n```\n\n| real | table |\n|---|---|\n| a | b |"
	matches := FindMarkdownTablesOutsideCodeBlocks(input)
	if len(matches) != 1 {
		t.Fatalf("table matches = %#v", matches)
	}
	if !strings.Contains(matches[0].Raw, "real") {
		t.Fatalf("matched wrong table = %#v", matches[0])
	}
}

func TestSanitizeTextForCardWrapsTablesBeyondLimit(t *testing.T) {
	makeTable := func(label string) string {
		return "| " + label + " h1 | h2 |\n|---|---|\n| v1 | v2 |"
	}
	input := strings.Join([]string{makeTable("A"), makeTable("B"), makeTable("C"), makeTable("D")}, "\n\n")
	got := SanitizeTextForCard(input, FeishuCardTableLimit)
	if !strings.Contains(got, makeTable("A")) || !strings.Contains(got, makeTable("C")) {
		t.Fatalf("kept tables missing = %q", got)
	}
	if !strings.Contains(got, "```\n"+makeTable("D")+"\n```") {
		t.Fatalf("extra table was not wrapped = %q", got)
	}
}
