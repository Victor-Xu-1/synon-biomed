package feishu

import (
	"fmt"
	"regexp"
	"strings"
)

const FeishuCardTableLimit = 3

type MarkdownTableMatch struct {
	Index  int
	Length int
	Raw    string
}

func OptimizeMarkdownForFeishu(text string, cardVersion int) string {
	if cardVersion <= 0 {
		cardVersion = 2
	}
	codeBlocks := make([]string, 0)
	marker := "___SYNON_FEISHU_CODE_BLOCK_"
	codeBlockPattern := regexp.MustCompile("```[\\s\\S]*?```")
	optimized := codeBlockPattern.ReplaceAllStringFunc(text, func(block string) string {
		index := len(codeBlocks)
		codeBlocks = append(codeBlocks, block)
		return fmt.Sprintf("%s%d___", marker, index)
	})

	if regexp.MustCompile(`(?m)^#{1,3} `).MatchString(text) {
		optimized = regexp.MustCompile(`(?m)^#{2,6} (.+)$`).ReplaceAllString(optimized, "##### $1")
		optimized = regexp.MustCompile(`(?m)^# (.+)$`).ReplaceAllString(optimized, "#### $1")
	}

	if cardVersion >= 2 {
		optimized = regexp.MustCompile(`(?m)^(#{4,5} .+)\n{1,2}(#{4,5} )`).ReplaceAllString(optimized, "$1\n<br>\n$2")
		optimized = addTableSpacing(optimized)
	}

	optimized = regexp.MustCompile(`\n{3,}`).ReplaceAllString(optimized, "\n\n")
	for index, block := range codeBlocks {
		replacement := block
		if cardVersion >= 2 {
			replacement = "\n<br>\n" + block + "\n<br>\n"
		}
		optimized = strings.ReplaceAll(optimized, fmt.Sprintf("%s%d___", marker, index), replacement)
	}
	return stripInvalidImageKeys(optimized)
}

func FindMarkdownTablesOutsideCodeBlocks(text string) []MarkdownTableMatch {
	lineStarts := make([]int, 0)
	lines := strings.SplitAfter(text, "\n")
	offset := 0
	for _, line := range lines {
		lineStarts = append(lineStarts, offset)
		offset += len(line)
	}

	matches := make([]MarkdownTableMatch, 0)
	insideCode := false
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") {
			insideCode = !insideCode
			continue
		}
		if insideCode || i+1 >= len(lines) {
			continue
		}
		if !isMarkdownTableRow(lines[i]) || !isMarkdownTableSeparator(lines[i+1]) {
			continue
		}
		startLine := i
		endLine := i + 2
		for endLine < len(lines) && isMarkdownTableRow(lines[endLine]) {
			endLine++
		}
		start := lineStarts[startLine]
		end := len(text)
		if endLine < len(lineStarts) {
			end = lineStarts[endLine]
		}
		raw := text[start:end]
		matches = append(matches, MarkdownTableMatch{Index: start, Length: len(raw), Raw: strings.TrimRight(raw, "\n")})
		i = endLine - 1
	}
	return matches
}

func SanitizeTextForCard(text string, tableLimit int) string {
	if tableLimit < 0 {
		tableLimit = 0
	}
	matches := FindMarkdownTablesOutsideCodeBlocks(text)
	if len(matches) <= tableLimit {
		return text
	}
	result := text
	for i := len(matches) - 1; i >= tableLimit; i-- {
		match := matches[i]
		replacement := "```\n" + match.Raw + "\n```"
		result = result[:match.Index] + replacement + result[match.Index+match.Length:]
	}
	return result
}

func addTableSpacing(text string) string {
	matches := FindMarkdownTablesOutsideCodeBlocks(text)
	if len(matches) == 0 {
		return text
	}
	result := text
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		before := result[:match.Index]
		after := result[match.Index+match.Length:]
		if strings.HasSuffix(before, "\n\n") {
			before = strings.TrimSuffix(before, "\n\n") + "\n<br>\n"
		} else if strings.HasSuffix(before, "\n") {
			before = strings.TrimSuffix(before, "\n") + "\n<br>\n"
		}
		if strings.TrimSpace(after) != "" && !strings.HasPrefix(after, "\n<br>") {
			after = "\n<br>\n" + strings.TrimLeft(after, "\n")
		}
		result = before + match.Raw + after
	}
	return result
}

func stripInvalidImageKeys(text string) string {
	if !strings.Contains(text, "![") {
		return text
	}
	return regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`).ReplaceAllStringFunc(text, func(match string) string {
		parts := regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`).FindStringSubmatch(match)
		if len(parts) == 3 && strings.HasPrefix(parts[2], "img_") {
			return match
		}
		return ""
	})
}

func isMarkdownTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") && strings.Count(trimmed, "|") >= 2
}

func isMarkdownTableSeparator(line string) bool {
	if !isMarkdownTableRow(line) {
		return false
	}
	for _, char := range strings.Trim(line, " |\t\r\n") {
		if char != '-' && char != ':' && char != '|' && char != ' ' {
			return false
		}
	}
	return strings.Contains(line, "-")
}
