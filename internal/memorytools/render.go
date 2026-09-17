package memorytools

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"synon-go/internal/memorypolicy"
	workspace "synon-go/internal/persistence/workspace"
)

var toolNow = time.Now

func renderEntityDocument(entity resolvedEntity, rows []workspace.Memory, staleness map[string]workspace.MemoryStaleness, limit int) string {
	label := entity.key
	if len(rows) == 0 {
		return fmt.Sprintf("No memories for entity `%s`.", sanitizeInline(label))
	}
	visible := rows[:memoryToolSliceEnd(len(rows), limit)]
	now := toolNow().UTC()
	lines := make([]string, 0, len(visible)+1)
	for _, memory := range visible {
		metadata := []string{memory.ID}
		if entity.categoryID != "" {
			metadata = append(metadata, memoryEntityKey(memory))
		}
		if note, exists := staleness[memory.ID]; exists && note.Badge != "" {
			metadata = append(metadata, note.Badge)
		}
		lines = append(lines, fmt.Sprintf("- [%s] [%s] %s  [%s]",
			formatRelativeAge(memory.UpdatedAt, now), memory.Evidence,
			sanitizeInline(memory.Body), strings.Join(metadata, " · ")))
	}
	count := fmt.Sprintf("%d", len(rows))
	suffix := ""
	if len(rows) > limit {
		if entity.categoryID != "" {
			count = fmt.Sprintf("%d+", limit)
			suffix = "\n- …more not shown; use `search_memory` to narrow."
		} else {
			suffix = fmt.Sprintf("\n- …%d more not shown; use `search_memory` to narrow.", len(rows)-limit)
		}
	}
	return fmt.Sprintf("%s (%s):\n%s%s", sanitizeInline(label), count, strings.Join(lines, "\n"), suffix)
}

// JavaScript Array.slice(0, end) treats a negative end as an offset from the
// tail. Several reference memory limits intentionally have no min(0)
// schema constraint, so their negative behavior is part of the source contract.
func memoryToolSliceEnd(length, end int) int {
	if end < 0 {
		end = length + end
	}
	if end < 0 {
		return 0
	}
	if end > length {
		return length
	}
	return end
}

func renderSearchResults(rows []workspace.Memory, staleness map[string]workspace.MemoryStaleness) (string, []string) {
	lines := make([]string, 0, len(rows))
	entitySet := make(map[string]struct{})
	entityOrder := make([]string, 0)
	truncatedSet := make(map[string]struct{})
	truncatedOrder := make([]string, 0)
	now := toolNow().UTC()
	for _, memory := range rows {
		entity := memoryEntityKey(memory)
		if _, seen := entitySet[entity]; !seen {
			entitySet[entity] = struct{}{}
			entityOrder = append(entityOrder, entity)
		}
		metadata := []string{memory.ID, entity}
		if note, exists := staleness[memory.ID]; exists && note.Badge != "" {
			metadata = append(metadata, note.Badge)
		}
		body := sanitizeInline(memory.Body)
		preview := body
		if memorypolicy.UTF16Length(body) > memorypolicy.SearchBodyPreviewMax {
			if _, seen := truncatedSet[entity]; !seen {
				truncatedSet[entity] = struct{}{}
				truncatedOrder = append(truncatedOrder, entity)
			}
			// The reference runtime takes the first 200 UTF-16 units and then appends
			// the display ellipsis; persistence truncation uses a different
			// 999+ellipsis contract.
			preview = memorypolicy.PrefixUTF16(body, memorypolicy.SearchBodyPreviewMax) + "…"
		}
		lines = append(lines, fmt.Sprintf("[%s] [%s] %s  [%s]",
			formatRelativeAge(memory.UpdatedAt, now), memory.Evidence,
			preview, strings.Join(metadata, " · ")))
	}
	output := strings.Join(lines, "\n")
	if len(truncatedOrder) > 0 {
		additional := ""
		if len(truncatedOrder) > 1 {
			shown := truncatedOrder[1:]
			if len(shown) > memorypolicy.SearchExtraEntitiesMax {
				shown = shown[:memorypolicy.SearchExtraEntitiesMax]
			}
			quoted := make([]string, len(shown))
			for index, entity := range shown {
				quoted[index] = fmt.Sprintf("%q", entity)
			}
			additional = fmt.Sprintf(" (also truncated: %s", strings.Join(quoted, ", "))
			if len(truncatedOrder) > 1+memorypolicy.SearchExtraEntitiesMax {
				additional += ", …"
			}
			additional += ")"
		}
		output += fmt.Sprintf("\n\nBodies truncated to ~%d chars — read_memory(%q) for full bodies%s.", memorypolicy.SearchBodyPreviewMax, truncatedOrder[0], additional)
	}
	entities := append([]string(nil), entityOrder...)
	sort.Strings(entities)
	return output, entities
}

func sanitizeInline(value string) string {
	return workspace.SanitizeMemoryDisplayText(value)
}

func formatRelativeAge(value, now time.Time) string {
	if value.IsZero() {
		return ""
	}
	seconds := now.Sub(value.UTC()).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	minutes := int(seconds/60 + 0.5)
	if minutes < 60 {
		switch minutes {
		case 0:
			return "just now"
		case 1:
			return "1 minute ago"
		default:
			return fmt.Sprintf("%d minutes ago", minutes)
		}
	}
	hours := int(seconds/3600 + 0.5)
	if hours < 24 {
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	}
	days := int(seconds/86400 + 0.5)
	if days < memorypolicy.RelativeAgeDayCutoff {
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
	weeks := int(seconds/604800 + 0.5)
	return fmt.Sprintf("%d weeks ago", weeks)
}
