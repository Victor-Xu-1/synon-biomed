package workspace

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// CarryForwardArtifactAnnotations retargets annotations and applies the v1.1
// text-selection relocation rule against the newly written content.
func (s *Store) CarryForwardArtifactAnnotations(projectID, fromTargetKey, toTargetKey, checksum, content string) ([]Annotation, error) {
	carried, err := s.CarryForwardAnnotations(projectID, fromTargetKey, toTargetKey, checksum)
	if err != nil {
		return nil, err
	}
	for index := range carried {
		annotation := &carried[index]
		if annotation.Body["type"] != "text_selection" || !artifactPositiveLine(annotation.Body["start_line"]) {
			continue
		}
		selection, _ := annotation.Body["selection_text"].(string)
		if selection == "" {
			continue
		}
		prefix, _ := annotation.Body["selection_prefix"].(string)
		start, end, found := relocateArtifactSelection(content, selection, prefix)
		if found {
			annotation.Body["start_line"] = start
			annotation.Body["end_line"] = end
		} else {
			for _, field := range []string{"start_line", "start_col", "end_line", "end_col"} {
				annotation.Body[field] = nil
			}
		}
		encoded, err := json.Marshal(annotation.Body)
		if err != nil {
			return nil, fmt.Errorf("encode relocated artifact annotation: %w", err)
		}
		if _, err := s.db.Exec(`UPDATE annotations SET body = ? WHERE id = ?`, string(encoded), annotation.ID); err != nil {
			return nil, fmt.Errorf("persist relocated artifact annotation: %w", err)
		}
	}
	return carried, nil
}

func artifactPositiveLine(value any) bool {
	switch number := value.(type) {
	case int:
		return number >= 1
	case int64:
		return number >= 1
	case float64:
		return number >= 1
	default:
		return false
	}
}

func relocateArtifactSelection(content, selection, prefix string) (int, int, bool) {
	selection = strings.TrimSpace(selection)
	if selection == "" || content == "" {
		return 0, 0, false
	}
	positions := artifactSubstringPositions(content, selection, 50)
	if len(positions) > 0 {
		position := chooseArtifactSelectionPosition(content, positions, prefix)
		return artifactLineAt(content, position), artifactLineAt(content, position+len(selection)-1), true
	}
	normalized, offsets := normalizeArtifactSelection(content)
	normalizedSelection, _ := normalizeArtifactSelection(selection)
	if len(normalizedSelection) < 3 {
		return 0, 0, false
	}
	positions = artifactSubstringPositions(normalized, normalizedSelection, 50)
	if len(positions) > 0 {
		position := chooseArtifactSelectionPosition(normalized, positions, prefix)
		startOffset := offsets[position]
		endOffset := offsets[position+len(normalizedSelection)-1]
		return artifactLineAt(content, startOffset), artifactLineAt(content, endOffset), true
	}
	for _, length := range []int{60, 40, 24} {
		if length > len(normalizedSelection) {
			length = len(normalizedSelection)
		}
		candidate := normalizedSelection[:length]
		if len(candidate) < 12 {
			break
		}
		positions = artifactSubstringPositions(normalized, candidate, 50)
		if len(positions) == 0 {
			continue
		}
		position := chooseArtifactSelectionPosition(normalized, positions, prefix)
		start := artifactLineAt(content, offsets[position])
		return start, start + strings.Count(selection, "\n"), true
	}
	return 0, 0, false
}

func artifactSubstringPositions(content, selection string, limit int) []int {
	positions := make([]int, 0, 1)
	for offset := 0; offset <= len(content) && len(positions) < limit; {
		index := strings.Index(content[offset:], selection)
		if index < 0 {
			break
		}
		position := offset + index
		positions = append(positions, position)
		offset = position + 1
	}
	return positions
}

func chooseArtifactSelectionPosition(content string, positions []int, prefix string) int {
	if len(positions) == 1 {
		return positions[0]
	}
	normalizedPrefix, _ := normalizeArtifactSelection(prefix)
	if len(normalizedPrefix) >= 8 {
		matching := make([]int, 0, len(positions))
		for _, position := range positions {
			before := content[:position]
			if len(before) > 300 {
				before = before[len(before)-300:]
			}
			normalizedBefore, _ := normalizeArtifactSelection(before)
			if strings.HasSuffix(normalizedBefore, normalizedPrefix) {
				matching = append(matching, position)
			}
		}
		if len(matching) > 0 {
			return matching[0]
		}
	}
	return positions[0]
}

func normalizeArtifactSelection(value string) (string, []int) {
	var normalized strings.Builder
	offsets := make([]int, 0, len(value))
	space := true
	for offset, character := range value {
		if unicode.IsSpace(character) {
			if !space {
				normalized.WriteByte(' ')
				offsets = append(offsets, offset)
				space = true
			}
			continue
		}
		normalized.WriteRune(character)
		for count := 0; count < len(string(character)); count++ {
			offsets = append(offsets, offset)
		}
		space = false
	}
	result := strings.TrimSpace(normalized.String())
	if len(offsets) > len(result) {
		offsets = offsets[:len(result)]
	}
	return result, offsets
}

func artifactLineAt(content string, offset int) int {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}
	return 1 + strings.Count(content[:offset], "\n")
}
