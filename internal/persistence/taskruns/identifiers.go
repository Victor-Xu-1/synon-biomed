package taskruns

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

func generateRunID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("tr_%d", time.Now().UTC().UnixNano())
	}
	return "tr_" + hex.EncodeToString(buf[:])
}

func sanitizeRunID(runID string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == ':' {
			return r
		}
		return '-'
	}, runID)
}

func sanitizeStepID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.ToLower(id)
	id = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, id)
	return strings.Trim(id, "-")
}

func validExecutorKind(kind string) bool {
	switch kind {
	case "system", "agent", "synonlink-browser", "manual":
		return true
	default:
		return false
	}
}

func executorScope(values []string) []string {
	cleaned := compactStrings(values)
	if len(cleaned) > 0 {
		return cleaned
	}
	return []string{"system", "agent", "synonlink-browser", "manual"}
}

func compactStrings(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func copyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func compactText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "...[truncated]"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringFromMap(values map[string]any, key string) string {
	if value, ok := values[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func ratio(numerator int, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func minFloat(a float64, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func allSteps(steps []Step, status string) bool {
	if len(steps) == 0 {
		return false
	}
	for _, step := range steps {
		if step.Status != status {
			return false
		}
	}
	return true
}

func firstStepID(record Record) string {
	if len(record.Steps) == 0 {
		return ""
	}
	return record.Steps[0].ID
}

func isTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}
