package feishu

import (
	"fmt"
	"regexp"
	"strings"
)

func BuildInitialStreamingCard() map[string]any {
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode": true,
			"update_multi":   true,
		},
		"body": map[string]any{
			"elements": []any{
				map[string]any{
					"tag":        "markdown",
					"content":    "正在思考中...",
					"text_size":  "body",
					"element_id": StreamingElementID,
				},
			},
		},
	}
}

func BuildFinalCardKitCard(renderedMarkdown string) map[string]any {
	content := NormalizeReplyMarkdownForCompactCard(renderedMarkdown)
	if content == "" {
		content = " "
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{},
		"body": map[string]any{
			"elements": []any{
				map[string]any{
					"tag":        "markdown",
					"content":    content,
					"text_size":  "body",
					"element_id": StreamingElementID,
				},
			},
		},
	}
}

func BuildRenderedCard(renderedMarkdown string) map[string]any {
	content := MarkdownToLarkMDForFinalCard(renderedMarkdown)
	if content == "" {
		content = " "
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi": true,
		},
		"body": map[string]any{
			"elements": []any{
				map[string]any{
					"tag": "div",
					"text": map[string]any{
						"tag":     "lark_md",
						"content": content,
					},
					"text_size": "body",
				},
			},
		},
	}
}

func BuildErrorCard(message string) map[string]any {
	if strings.TrimSpace(message) == "" {
		message = "未知错误"
	}
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"update_multi": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": "出错了"},
			"template": "red",
		},
		"body": map[string]any{
			"elements": []any{
				map[string]any{
					"tag":     "markdown",
					"content": message,
				},
			},
		},
	}
}

func MarkdownToLarkMDForFinalCard(markdown string) string {
	text := NormalizeReplyMarkdownForCompactCard(markdown)
	text = regexp.MustCompile(`~~([^~\n]+?)~~`).ReplaceAllString(text, "$1")
	imagePattern := regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
	text = imagePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := imagePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		if strings.HasPrefix(parts[2], "img_") {
			return match
		}
		return parts[1]
	})
	return text
}

func NormalizeReplyMarkdownForCompactCard(markdown string) string {
	codeBlocks := make([]string, 0)
	marker := "___SYNON_CODE_BLOCK_"
	text := regexp.MustCompile("```[\\s\\S]*?```").ReplaceAllStringFunc(markdown, func(block string) string {
		index := len(codeBlocks)
		codeBlocks = append(codeBlocks, block)
		return fmt.Sprintf("%s%d___", marker, index)
	})

	text = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+(.+?)\s*#*\s*$`).ReplaceAllString(text, "$1")
	text = regexp.MustCompile(`(?m)^[ \t]{0,3}\*\*([^*\n]+?)\*\*[ \t]*$`).ReplaceAllString(text, "$1")

	for index, block := range codeBlocks {
		text = strings.ReplaceAll(text, fmt.Sprintf("%s%d___", marker, index), block)
	}
	return strings.TrimSpace(text)
}
