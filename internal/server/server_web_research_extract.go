package server

import (
	"net/url"
	"strings"
)

func extractWebResearchLinks(baseURL string, content string) []any {
	base, _ := url.Parse(baseURL)
	links := []any{}
	seen := map[string]bool{}
	lower := strings.ToLower(content)
	for index := 0; index < len(lower); {
		pos := strings.Index(lower[index:], "href=")
		if pos < 0 {
			break
		}
		start := index + pos + len("href=")
		if start >= len(content) {
			break
		}
		quote := content[start]
		if quote == '\'' || quote == '"' {
			start++
		} else {
			quote = ' '
		}
		end := start
		for end < len(content) {
			if quote == ' ' {
				if content[end] == ' ' || content[end] == '>' {
					break
				}
			} else if content[end] == quote {
				break
			}
			end++
		}
		raw := strings.TrimSpace(content[start:end])
		if raw != "" {
			resolved := raw
			if base != nil {
				if parsed, err := url.Parse(raw); err == nil {
					resolved = base.ResolveReference(parsed).String()
				}
			}
			if !seen[resolved] {
				seen[resolved] = true
				links = append(links, resolved)
			}
		}
		index = end + 1
	}
	return links
}

func extractWebResearchTables(content string) []any {
	tables := []any{}
	lower := strings.ToLower(content)
	for index := 0; index < len(lower); {
		startRel := strings.Index(lower[index:], "<table")
		if startRel < 0 {
			break
		}
		start := index + startRel
		endRel := strings.Index(lower[start:], "</table>")
		if endRel < 0 {
			break
		}
		end := start + endRel + len("</table>")
		tables = append(tables, content[start:end])
		index = end
	}
	return tables
}
